// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

package scheduler

// This file contains a scheduleri implementation for 'lsf': running jobs
// via IBM's (ne Platform's) Load Sharing Facility.

import (
	"bufio"
	"fmt"
	"github.com/sb10/vrpipe/internal"
	"math"
	"os/exec"
	"os/user"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lsf is our implementer of scheduleri
type lsf struct {
	deployment         string
	shell              string
	months             map[string]int
	dateRegex          *regexp.Regexp
	bsubRegex          *regexp.Regexp
	memLimitMultiplier float32
	queues             map[string]map[string]int
	sortedqs           map[int][]string
	sortedqKeys        []int
}

// initialize finds out about lsf's hosts and queues
func (s *lsf) initialize(deployment string, shell string) error {
	s.deployment = deployment
	s.shell = shell

	// set up what should be global vars, but we don't really want these taking
	// up space if the user never uses LSF
	s.months = map[string]int{
		"Jan": 1,
		"Feb": 2,
		"Mar": 3,
		"Apr": 4,
		"May": 5,
		"Jun": 6,
		"Jul": 7,
		"Aug": 8,
		"Sep": 9,
		"Oct": 10,
		"Nov": 11,
		"Dec": 12,
	}
	s.dateRegex = regexp.MustCompile(`(\w+)\s+(\d+) (\d+):(\d+):(\d+)`)
	s.bsubRegex = regexp.MustCompile(`^Job <(\d+)>`)

	// use lsadmin to see what units memlimit (bsub -M) is in
	s.memLimitMultiplier = float32(1000) // by default assume it's KB
	cmdout, err := exec.Command(shell, "-c", "lsadmin showconf lim | grep LSF_UNIT_FOR_LIMITS").Output()
	if err != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to run [lsadmin showconf lim | grep LSF_UNIT_FOR_LIMITS]: %s", err)}
	}
	if len(cmdout) > 0 {
		uflRegex := regexp.MustCompile(`=\s*(\w)`)
		unit := uflRegex.FindStringSubmatch(string(cmdout))
		if len(unit) == 2 && unit[1] != "" {
			switch unit[1] {
			case "M":
				s.memLimitMultiplier = float32(1)
			case "G":
				s.memLimitMultiplier = float32(0.001)
				// 'K' is our default
			}
		}
	}

	// parse bqueues -l to figure out what usable queues we have
	bqcmd := exec.Command(shell, "-c", "bqueues -l")
	bqout, err := bqcmd.StdoutPipe()
	if err != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to create pipe for [bqueues -l]: %s", err)}
	}
	if err := bqcmd.Start(); err != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to start [bqueues -l]: %s", err)}
	}
	bqScanner := bufio.NewScanner(bqout)
	s.queues = make(map[string]map[string]int)
	queue := ""
	nextIsPrio := false
	lookingAtDefaults := false
	nextIsMemlimit := 0
	nextIsRunlimit := false
	highest := map[string]int{"runlimit": 0, "memlimit": 0, "max": 0, "max_user": 0, "users": 0, "hosts": 0}
	updateHighest := func(htype string, val int) {
		if val > highest[htype] {
			highest[htype] = val
		}
	}
	reQueue := regexp.MustCompile(`^QUEUE: (\S+)`)
	rePrio := regexp.MustCompile(`^PRIO\s+NICE\s+STATUS\s+MAX\s+JL\/U`)
	reDefaultLimits := regexp.MustCompile(`^DEFAULT LIMITS:`)
	reDefaultsFinished := regexp.MustCompile(`^MAXIMUM LIMITS:|^SCHEDULING PARAMETERS`)
	reMemlimit := regexp.MustCompile(`MEMLIMIT`)
	reNumUnit := regexp.MustCompile(`(\d+) (\w)`)
	reRunLimit := regexp.MustCompile(`RUNLIMIT`)
	reParseRunlimit := regexp.MustCompile(`^\s*(\d+)(?:\.0)? min`)
	reUserHosts := regexp.MustCompile(`^(USERS|HOSTS):\s+(.+?)\s*$`)
	reChunkJobSize := regexp.MustCompile(`^CHUNK_JOB_SIZE:\s+(\d+)`)
	for bqScanner.Scan() {
		line := bqScanner.Text()

		if matches := reQueue.FindStringSubmatch(line); matches != nil && len(matches) == 2 {
			queue = matches[1]
			s.queues[queue] = make(map[string]int)
			continue
		}
		if queue == "" {
			continue
		}

		if rePrio.MatchString(line) {
			nextIsPrio = true
			continue
		} else if nextIsPrio {
			fields := strings.Fields(line)
			s.queues[queue]["prio"], err = strconv.Atoi(fields[0])
			if err != nil {
				return Error{"lsf", "initialize", fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
			}
			if fields[3] != "-" {
				i, err := strconv.Atoi(fields[3])
				if err != nil {
					return Error{"lsf", "initialize", fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
				}
				s.queues[queue]["max"] = i
				updateHighest("max", i)
			}
			if fields[4] != "-" {
				i, err := strconv.Atoi(fields[4])
				if err != nil {
					return Error{"lsf", "initialize", fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
				}
				s.queues[queue]["max_user"] = i
				updateHighest("max_user", i)
			}
			nextIsPrio = false
		} else if reDefaultLimits.MatchString(line) {
			lookingAtDefaults = true
			continue
		} else if reDefaultsFinished.MatchString(line) {
			lookingAtDefaults = false
			continue
		} else if !lookingAtDefaults {
			if reMemlimit.MatchString(line) {
				nextIsMemlimit = 0
				for _, word := range strings.Fields(line) {
					nextIsMemlimit++
					if word == "MEMLIMIT" {
						break
					}
				}
				continue
			} else if nextIsMemlimit > 0 {
				if matches := reNumUnit.FindAllStringSubmatch(line, -1); matches != nil && len(matches) >= nextIsMemlimit-1 {
					val, err := strconv.Atoi(matches[nextIsMemlimit-1][1])
					if err != nil {
						return Error{"lsf", "initialize", fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
					}
					unit := matches[nextIsMemlimit-1][2]
					switch unit {
					case "G":
						val *= 1000
					case "K":
						val /= 1000
					}
					s.queues[queue]["memlimit"] = val
					updateHighest("memlimit", val)
				}
				nextIsMemlimit = 0
			} else if reRunLimit.MatchString(line) {
				nextIsRunlimit = true
				continue
			} else if nextIsRunlimit {
				if matches := reParseRunlimit.FindStringSubmatch(line); matches != nil && len(matches) == 2 {
					mins, err := strconv.Atoi(matches[1])
					if err != nil {
						return Error{"lsf", "initialize", fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
					}
					s.queues[queue]["runlimit"] = mins * 60
					// updateHighest("runlimit", ...) for queues that do not
					// specify a run limit, we won't base the default on the
					// highest value seen on other queues, but on a hard-coded 1
					// year
				}
				nextIsRunlimit = false
			}
		}

		if matches := reUserHosts.FindStringSubmatch(line); matches != nil && len(matches) == 3 {
			kind := strings.ToLower(matches[1])
			vals := strings.Fields(matches[2])
			if kind == "users" {
				users := make(map[string]bool)
				for _, val := range vals {
					users[val] = true
				}

				user, err := user.Current()
				if err != nil {
					return Error{"lsf", "initialize", fmt.Sprintf("could not get current user: %s", err)}
				}
				me := user.Username

				if !users["all"] && !users[me] {
					delete(s.queues, queue)
					queue = ""
				}
			} else {
				if matches[2] != "all" {
					s.queues[queue][kind] = len(vals)
					updateHighest(kind, len(vals))
				}
			}
		}

		if matches := reChunkJobSize.FindStringSubmatch(line); matches != nil && len(matches) == 2 {
			chunks, err := strconv.Atoi(matches[1])
			if err != nil {
				return Error{"lsf", "initialize", fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
			}
			s.queues[queue]["chunk_size"] = chunks
		}
	}

	if serr := bqScanner.Err(); serr != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to read everything from [bqueues -l]: %s", serr)}
	}
	if err := bqcmd.Wait(); err != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to finish running [bqueues -l]: %s", err)}
	}

	// for each criteria we're going to sort the queues on later, hard-code
	// [weight, sort-order, significant_change, highest_multiplier]. We want to
	// avoid chunked queues because that means jobs will run sequentially
	// instead of in parallel. For time and memory, prefer the queue that is
	// more limited, since we suppose they might be less busy or will at least
	// become free sooner
	criteriaHandling := map[string][]int{
		"hosts":      []int{10, 1, 25, 2}, // weight, sort order, significant change, default multiplier
		"max_user":   []int{6, 1, 10, 10},
		"max":        []int{5, 1, 20, 5},
		"prio":       []int{4, 1, 50, 0},
		"chunk_size": []int{10000, 0, 1, 0},
		"runlimit":   []int{2, 0, 3600, 12},
		"memlimit":   []int{2, 0, 16000, 10},
	}

	// fill in some default values for the criteria on all the queues
	defaults := map[string]int{"runlimit": 31536000, "memlimit": 10000000, "max": 10000000, "max_user": 10000000, "users": 10000000, "hosts": 10000000, "chunk_size": 0}
	for criterion, highest := range highest {
		if highest > 0 {
			defaults[criterion] = highest + (criteriaHandling[criterion][2] * criteriaHandling[criterion][3])
		}
	}
	for _, qmap := range s.queues {
		for criterion, cdefault := range defaults {
			if _, wasSet := qmap[criterion]; !wasSet {
				qmap[criterion] = cdefault
			}
		}
	}

	// sort the queues, those most likely to run jobs sooner coming first
	ranking := make(map[string]int)
	punishedForMax := make(map[string][2]int)
	for _, criterion := range []string{"max_user", "max", "hosts", "prio", "chunk_size", "runlimit", "memlimit"} { // instead of range over criteriaHandling, because max_user must come first
		// sort queues by this criterion
		reverse := false
		if criteriaHandling[criterion][1] == 1 {
			reverse = true
		}
		sorted := internal.SortMapKeysByMapIntValue(s.queues, criterion, reverse)

		weight := criteriaHandling[criterion][0]
		significantChange := criteriaHandling[criterion][2]
		prevVal := -1
		rank := 0
		for _, queue := range sorted {
			val := s.queues[queue][criterion]
			if prevVal != -1 {
				diff := int(math.Abs(float64(val) - float64(prevVal)))
				if diff >= significantChange {
					if criterion == "runlimit" {
						// because the variance in runlimit can be so massive,
						// increase rank sequentially
						rank++
					} else {
						rank += int(math.Ceil(float64(diff) / float64(significantChange)))
					}
				}
			}
			punishment := rank * weight
			if punishment > 0 {
				if criterion == "max_user" {
					punishedForMax[queue] = [2]int{val, punishment}
				} else if criterion == "max" {
					if _, punished := punishedForMax[queue]; punished {
						// don't double-punish for queues that have both
						// max_user and max
						punishment = 0
					} else {
						punishedForMax[queue] = [2]int{val, punishment}
					}
				}
				ranking[queue] += punishment
			}

			prevVal = val
		}
	}

	s.sortedqs = make(map[int][]string)
	s.sortedqs[0] = internal.SortMapKeysByIntValue(ranking, false)

	for queue, vp := range punishedForMax {
		thisRanking := make(map[string]int)
		for rq, rp := range ranking {
			if rq == queue {
				rp -= vp[1]
			}
			thisRanking[rq] = rp
		}
		s.sortedqs[vp[0]] = internal.SortMapKeysByIntValue(thisRanking, false)
	}
	for key, _ := range s.sortedqs {
		s.sortedqKeys = append(s.sortedqKeys, key)
	}
	sort.Sort(sort.IntSlice(s.sortedqKeys))

	// now s.sortedqs has [0] containing our default preferred order or queues,
	// and other numbers which can be tested against any global maximum number
	// of jobs that we should submit to LSF, and if lower than any of those
	// we prefer the order described there
	//*** we probably don't need this if we won't be having a global max
	// specified by the user

	return nil
}

// schedule achieves the aims of Schedule(). Note that if rescheduling a cmd
// at a lower count, we cannot guarantee that only that number get run; it may
// end up being a few more.
func (s *lsf) schedule(cmd string, req *Requirements, count int) error {
	// find the best queue for these resource requirements
	queue, err := s.determineQueue(req, 0)
	if err != nil {
		return err // impossible to run cmd with these reqs
	}

	// get the details of everything already in the scheduler for this cmd,
	// removing from the queue anything not currently running when we're over
	// the desired count
	scheduledCount, err := s.checkCmd(cmd, count)
	stillNeeded := count - scheduledCount
	if stillNeeded < 1 {
		return nil
	}
	var bsubArgs []string

	megabytes := req.Memory
	m := float32(megabytes) * s.memLimitMultiplier
	//reqString := fmt.Sprintf("-q %s -M%0.0f -R 'select[mem>%d] rusage[mem=%d]'", queue, m, megabytes, megabytes)
	bsubArgs = append(bsubArgs, "-q", queue, "-M", fmt.Sprintf("%0.0f", m), "-R", fmt.Sprintf("'select[mem>%d] rusage[mem=%d]'", megabytes, megabytes))
	if req.CPUs > 1 {
		//reqString += fmt.Sprintf(" -n%d -R 'span[hosts=1]'", req.CPUs)
		bsubArgs = append(bsubArgs, "-n", fmt.Sprintf("%d", req.CPUs), "-R", "'span[hosts=1]'")
	}
	if req.Other != "" {
		//reqString += " " + req.Other
		bsubArgs = append(bsubArgs, req.Other) // *** this probably won't work?
	}

	// for checkCmd() to work efficiently we must always set a job name that
	// corresponds to the cmd. It must also be unique otherwise LSF would not
	// start running jobs with duplicate names until previous ones complete
	name := jobName(cmd, s.deployment, true)
	if stillNeeded > 1 {
		name += fmt.Sprintf("[1-%d]", stillNeeded)
	}
	bsubArgs = append(bsubArgs, "-J", name, "-o", "/dev/null", "-e", "/dev/null", cmd)

	// submit to the queue
	//bsub := "bsub -J " + name + " -o /dev/null -e /dev/null " + reqString + " '" + cmd + "'"
	//bsubcmd := exec.Command(s.shell, "-c", bsub)
	bsubcmd := exec.Command("bsub", bsubArgs...)
	bsubout, err := bsubcmd.Output()
	if err != nil {
		return Error{"lsf", "schedule", fmt.Sprintf("failed to run bsub %s: %s", bsubArgs, err)}
	}

	// unfortunately, a job can be successfully submitted to the queue but not
	// immediately appear in bjobs, and if it completes in less than a few
	// seconds, it will never appear there (unless you supply bjobs the job id).
	// This means that our busy() method, if called immediately after the
	// schedule(), would return false, even though the job may actually be
	// running. To solve this issue we will wait until bjobs -w <jobid> is found
	// and only then return. If a subsequent busy() call returns false, that
	// means the job completed and we're really not busy.
	if matches := s.bsubRegex.FindStringSubmatch(string(bsubout)); matches != nil && len(matches) == 2 {
		ready := make(chan bool, 1)
		go func() {
			limit := time.After(10 * time.Second)
			ticker := time.NewTicker(100 * time.Millisecond)
			for {
				select {
				case <-ticker.C:
					bjcmd := exec.Command("bjobs", "-w", matches[1])
					bjout, err := bjcmd.CombinedOutput()
					if err != nil {
						continue
					}
					if len(bjout) > 46 {
						ticker.Stop()
						ready <- true
						return
					}
					continue
				case <-limit:
					ticker.Stop()
					ready <- false
					return
				}
			}
		}()
		ok := <-ready
		if !ok {
			return Error{"lsf", "schedule", "after running bsub, failed to find the submitted jobs in bjobs"}
		}
	} else {
		return Error{"lsf", "schedule", fmt.Sprintf("bsub %s returned unexpected output: %s", bsubArgs, bsubout)}
	}

	return nil
}

// busy returns true if there are any jobs with our jobName() prefix in any
// queue. It also returns true if the most recently submitted job is pending or
// running
func (s *lsf) busy() bool {
	count, err := s.checkCmd("", -1)
	if err != nil {
		// busy() doesn't return an error, so just assume we're busy
		return true
	}
	return count > 0
}

// determineQueue picks a queue, preferring ones that are more likely to run our
// job the soonest (amongst those that are capable of running it). *** globalMax
// option and associated code may be removed if we never have a way for user
// to pass this in.
func (s *lsf) determineQueue(req *Requirements, globalMax int) (chosenQueue string, err error) {
	seconds := req.Time.Seconds()
	mb := req.Memory
	sortedQueue := 0
	if globalMax > 0 {
		for _, queueKey := range s.sortedqKeys {
			if globalMax <= queueKey {
				sortedQueue = queueKey
				break
			}
		}
	}

	for _, queue := range s.sortedqs[sortedQueue] {
		memLimit := s.queues[queue]["memlimit"]
		if memLimit > 0 && memLimit < mb {
			continue
		}

		timeLimit := s.queues[queue]["runlimit"]
		if timeLimit > 0 && float64(timeLimit) < seconds {
			continue
		}

		chosenQueue = queue
		break
	}

	if chosenQueue == "" {
		err = Error{"lsf", "determineQueue", ErrImpossible}
	}

	return
}

// checkCmd asks LSF how many of the supplied cmd are running, and if max >= 0
// is supplied, kills any extraneous non-running jobs for the cmd. If the
// supplied cmd is the empty string, it will report/act on all cmds submitted
// by schedule() for this deployment.
func (s *lsf) checkCmd(cmd string, max int) (count int, err error) {
	// bjobs -w does not output a column for both array index and the command.
	// The LSF related modules on CPAN either just parse the command line output
	// or don't work. Ideally we'd use the C-API's lsb_readjobinfo call, but we
	// don't want to be troubled by compilation issues and different versions.
	// We REALLY don't want to manually parse the entire output of bjobs -l for
	// all jobs. Instead when submitting we'll have arranged that JOB_NAME be
	// set to jobName(cmd, ..., true), and in the case of a job array it will
	// have [array_index] appended to it. This lets us use a single bjobs -w
	// call to get all that we need. We can't use -J name to limit which jobs
	// bjobs -w reports on, since we may have submitted the cmd multiple times
	// as multiple different arrays, each with a uniqified job name. It gets
	// uniquified because otherwise none of the jobs in the second array would
	// start until the first array with the same name ended.
	toKill := []string{"-b"}
	var jobPrefix string
	if cmd == "" {
		jobPrefix = fmt.Sprintf("vrp%s_", s.deployment[0:1])
	} else {
		jobPrefix = jobName(cmd, s.deployment, false)
	}

	bjcmd := exec.Command(s.shell, "-c", "bjobs -w")
	bjout, err := bjcmd.StdoutPipe()
	if err != nil {
		err = Error{"lsf", "checkCmd", fmt.Sprintf("failed to create pipe for [bjobs -w]: %s", err)}
		return
	}
	err = bjcmd.Start()
	if err != nil {
		err = Error{"lsf", "checkCmd", fmt.Sprintf("failed to start [bjobs -w]: %s", err)}
		return
	}
	bjScanner := bufio.NewScanner(bjout)

	reParse := regexp.MustCompile(`^(\d+)\s+\S+\s+(\S+)\s+\S+\s+\S+\s+\S+\s+(` + jobPrefix + `\S+)`)
	reAid := regexp.MustCompile(`\[(\d+)\]$`)
	for bjScanner.Scan() {
		line := bjScanner.Text()

		if matches := reParse.FindStringSubmatch(line); matches != nil && len(matches) == 4 {
			if matches[2] == "EXIT" {
				continue
			}
			count++

			if max >= 0 && count > max && matches[2] != "RUN" {
				sidaid := matches[1]
				if aidmatch := reAid.FindStringSubmatch(matches[3]); aidmatch != nil && len(aidmatch) == 2 {
					sidaid = sidaid + "[" + aidmatch[1] + "]"
				}
				toKill = append(toKill, sidaid)
			}
		}
	}

	if serr := bjScanner.Err(); serr != nil {
		err = Error{"lsf", "checkCmd", fmt.Sprintf("failed to read everything from [bjobs -w]: %s", serr)}
		return
	}
	err = bjcmd.Wait()
	if err != nil {
		err = Error{"lsf", "checkCmd", fmt.Sprintf("failed to finish running [bjobs -w]: %s", err)}
		return
	}

	if len(toKill) > 1 {
		killcmd := exec.Command("bkill", toKill...)
		killcmd.Run()
		//*** because of the time between the bsub and this bkill, some more
		// jobs may have started running, so reschedules to drop the count may
		// not run the number of jobs expected
	}
	return
}
