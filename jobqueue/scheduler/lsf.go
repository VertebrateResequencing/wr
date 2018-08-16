// Copyright © 2016-2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package scheduler

// This file contains a scheduleri implementation for 'lsf': running jobs
// via IBM's (ne Platform's) Load Sharing Facility.

import (
	"bufio"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/inconshreveable/log15"
)

// lsf is our implementer of scheduleri
type lsf struct {
	config             *ConfigLSF
	months             map[string]int
	dateRegex          *regexp.Regexp
	bsubRegex          *regexp.Regexp
	memLimitMultiplier float32
	queues             map[string]map[string]int
	sortedqs           map[int][]string
	sortedqKeys        []int
	bsubExe            string
	bjobsExe           string
	bkillExe           string
	log15.Logger
}

// ConfigLSF represents the configuration options required by the LSF scheduler.
// All are required with no usable defaults.
type ConfigLSF struct {
	// deployment is one of "development" or "production".
	Deployment string

	// shell is the shell to use to run the commands to interact with your job
	// scheduler; 'bash' is recommended.
	Shell string
}

// initialize finds out about lsf's hosts and queues
func (s *lsf) initialize(config interface{}, logger log15.Logger) error {
	s.config = config.(*ConfigLSF)
	s.Logger = logger.New("scheduler", "lsf")

	// find the real paths to the main LSF exes, since thanks to wr's LSF
	// compatibility mode, might not be the first in $PATH
	s.bsubExe = internal.Which("bsub")
	s.bjobsExe = internal.Which("bjobs")
	s.bkillExe = internal.Which("bkill")

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
	s.memLimitMultiplier = float32(1000)                                                                          // by default assume it's KB
	cmdout, err := exec.Command(s.config.Shell, "-c", "lsadmin showconf lim | grep LSF_UNIT_FOR_LIMITS").Output() // #nosec
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
	bqcmd := exec.Command(s.config.Shell, "-c", "bqueues -l") // #nosec
	bqout, err := bqcmd.StdoutPipe()
	if err != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to create pipe for [bqueues -l]: %s", err)}
	}
	if err = bqcmd.Start(); err != nil {
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

		if matches := reQueue.FindStringSubmatch(line); len(matches) == 2 {
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
					case "T":
						val *= 1000000
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
				if matches := reParseRunlimit.FindStringSubmatch(line); len(matches) == 2 {
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

		if matches := reUserHosts.FindStringSubmatch(line); len(matches) == 3 {
			kind := strings.ToLower(matches[1])
			vals := strings.Fields(matches[2])
			if kind == "users" {
				users := make(map[string]bool)
				for _, val := range vals {
					users[val] = true
				}

				me, err := internal.Username()
				if err != nil {
					return Error{"lsf", "initialize", fmt.Sprintf("could not get current user: %s", err)}
				}

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

		if matches := reChunkJobSize.FindStringSubmatch(line); len(matches) == 2 {
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
		"hosts":      {10, 1, 25, 2}, // weight, sort order, significant change, default multiplier
		"max_user":   {6, 1, 10, 10},
		"max":        {5, 1, 20, 5},
		"prio":       {4, 1, 50, 0},
		"chunk_size": {10000, 0, 1, 0},
		"runlimit":   {2, 0, 3600, 12},
		"memlimit":   {2, 0, 16000, 10},
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
	for key := range s.sortedqs {
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

// reserveTimeout achieves the aims of ReserveTimeout().
func (s *lsf) reserveTimeout(req *Requirements) int {
	if val, defined := req.Other["rtimeout"]; defined {
		timeout, err := strconv.Atoi(val)
		if err != nil {
			s.Logger.Error(fmt.Sprintf("Failed to convert timeout to integer: %s", err))
			return defaultReserveTimeout
		}
		s.Logger.Debug(fmt.Sprintf("setting runner timeout to %v", timeout))
		return timeout
	} else {
		return defaultReserveTimeout
	}
}

// maxQueueTime achieves the aims of MaxQueueTime().
func (s *lsf) maxQueueTime(req *Requirements) time.Duration {
	queue, err := s.determineQueue(req, 0)
	if err == nil {
		return time.Duration(s.queues[queue]["runlimit"]) * time.Second
	}
	return infiniteQueueTime
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
	if err != nil {
		return err
	}
	stillNeeded := count - scheduledCount
	if stillNeeded < 1 {
		return nil
	}
	var bsubArgs []string

	megabytes := req.RAM
	m := float32(megabytes) * s.memLimitMultiplier
	bsubArgs = append(bsubArgs, "-q", queue, "-M", fmt.Sprintf("%0.0f", m), "-R", fmt.Sprintf("'select[mem>%d] rusage[mem=%d] span[hosts=1]'", megabytes, megabytes))
	if req.Cores > 1 {
		bsubArgs = append(bsubArgs, "-n", fmt.Sprintf("%d", req.Cores))
	}
	// if len(req.Other) > 0 {
	// *** not yet implemented; would check this map for lsf-related keys
	// and handle them appropriately...
	// }

	// for checkCmd() to work efficiently we must always set a job name that
	// corresponds to the cmd. It must also be unique otherwise LSF would not
	// start running jobs with duplicate names until previous ones complete
	name := jobName(cmd, s.config.Deployment, true)
	if stillNeeded > 1 {
		name += fmt.Sprintf("[1-%d]", stillNeeded)
	}
	bsubArgs = append(bsubArgs, "-J", name, "-o", "/dev/null", "-e", "/dev/null", cmd)

	// submit to the queue
	bsubcmd := exec.Command(s.bsubExe, bsubArgs...) // #nosec
	bsubout, err := bsubcmd.Output()
	if err != nil {
		return Error{"lsf", "schedule", fmt.Sprintf("failed to run %s %s: %s", s.bsubExe, bsubArgs, err)}
	}

	// unfortunately, a job can be successfully submitted to the queue but not
	// immediately appear in bjobs, and if it completes in less than a few
	// seconds, it will never appear there (unless you supply bjobs the job id).
	// This means that our busy() method, if called immediately after the
	// schedule(), would return false, even though the job may actually be
	// running. To solve this issue we will wait until bjobs -w <jobid> is found
	// and only then return. If a subsequent busy() call returns false, that
	// means the job completed and we're really not busy.
	if matches := s.bsubRegex.FindStringSubmatch(string(bsubout)); len(matches) == 2 {
		ready := make(chan bool, 1)
		go func() {
			defer internal.LogPanic(s.Logger, "lsf scheduling", true)

			limit := time.After(10 * time.Second)
			ticker := time.NewTicker(100 * time.Millisecond)
			for {
				select {
				case <-ticker.C:
					bjcmd := exec.Command(s.bjobsExe, "-w", matches[1]) // #nosec
					bjout, errf := bjcmd.CombinedOutput()
					if errf != nil {
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

	return err
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
func (s *lsf) determineQueue(req *Requirements, globalMax int) (string, error) {
	seconds := req.Time.Seconds()
	mb := req.RAM
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

		return queue, nil
	}

	return "", Error{"lsf", "determineQueue", ErrImpossible}
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
	var jobPrefix string
	if cmd == "" {
		jobPrefix = fmt.Sprintf("wr%s_", s.config.Deployment[0:1])
	} else {
		jobPrefix = jobName(cmd, s.config.Deployment, false)
	}

	if max >= 0 {
		// to avoid a race condition where we collect id[index]s to kill here,
		// then later kill them all, though some may have started running by
		// then, we used to collect the jod ids now, then later bmod to allow
		// 0 running, then repeat the bjobs to find the ones to kill, kill them,
		// then bmod back to allowing lots to run. However, use of bmod resulted
		// in big rescheduling delays, and overall it seemed better (in terms of
		// getting jobs run quicker) to allow the race condition and allow some
		// cmds to start running and then get killed.
		reAid := regexp.MustCompile(`\[(\d+)\]$`)
		toKill := []string{"-b"}
		cb := func(matches []string) {
			count++
			if count > max && matches[2] != "RUN" {
				sidaid := matches[1]
				if aidmatch := reAid.FindStringSubmatch(matches[3]); len(aidmatch) == 2 {
					sidaid = sidaid + "[" + aidmatch[1] + "]"
				}
				toKill = append(toKill, sidaid)
				count--
			}
		}
		err = s.parseBjobs(jobPrefix, cb)

		if len(toKill) > 1 {
			killcmd := exec.Command(s.bkillExe, toKill...) // #nosec
			errk := killcmd.Run()
			if errk != nil {
				s.Warn("checkCmd bkill failed", "err", errk)
			}
		}
	} else {
		cb := func(matches []string) {
			count++
		}
		err = s.parseBjobs(jobPrefix, cb)
	}

	return count, err
}

type bjobsCB func(matches []string)

// parseBjobs runs bjobs, filters on a job name prefix, excludes exited jobs and
// gives matches to
// `^(\d+)\s+\S+\s+(\S+)\s+\S+\s+\S+\s+\S+\s+(jobPrefix\S+)` to your
// callback for each bjobs output line.
func (s *lsf) parseBjobs(jobPrefix string, callback bjobsCB) error {
	bjcmd := exec.Command(s.config.Shell, "-c", s.bjobsExe+" -w") // #nosec
	bjout, err := bjcmd.StdoutPipe()
	if err != nil {
		return Error{"lsf", "parseBjobs", fmt.Sprintf("failed to create pipe for [bjobs -w]: %s", err)}
	}
	err = bjcmd.Start()
	if err != nil {
		return Error{"lsf", "parseBjobs", fmt.Sprintf("failed to start [bjobs -w]: %s", err)}
	}
	bjScanner := bufio.NewScanner(bjout)

	reParse := regexp.MustCompile(`^(\d+)\s+\S+\s+(\S+)\s+\S+\s+\S+\s+\S+\s+(` + jobPrefix + `\S+)`)
	for bjScanner.Scan() {
		line := bjScanner.Text()

		if matches := reParse.FindStringSubmatch(line); len(matches) == 4 {
			if matches[2] == "EXIT" || matches[2] == "DONE" {
				continue
			}
			callback(matches)
		}
	}

	if err = bjScanner.Err(); err != nil {
		return Error{"lsf", "parseBjobs", fmt.Sprintf("failed to read everything from [bjobs -w]: %s", err)}
	}
	err = bjcmd.Wait()
	if err != nil {
		err = Error{"lsf", "parseBjobs", fmt.Sprintf("failed to finish running [bjobs -w]: %s", err)}
	}
	return err
}

// hostToID always returns an empty string, since we're not in the cloud.
func (s *lsf) hostToID(host string) string {
	return ""
}

// setMessageCallBack does nothing at the moment, since we don't generate any
// messages for the user.
func (s *lsf) setMessageCallBack(cb MessageCallBack) {}

// setBadServerCallBack does nothing, since we're not a cloud-based scheduler.
func (s *lsf) setBadServerCallBack(cb BadServerCallBack) {}

// cleanup bkills any remaining jobs we created
func (s *lsf) cleanup() {
	toKill := []string{"-b"}
	cb := func(matches []string) {
		toKill = append(toKill, matches[1])
	}
	err := s.parseBjobs(fmt.Sprintf("wr%s_", s.config.Deployment[0:1]), cb)
	if err != nil {
		s.Error("cleaup parse bjobs failed", "err", err)
	}
	if len(toKill) > 1 {
		killcmd := exec.Command(s.bkillExe, toKill...) // #nosec
		err = killcmd.Run()
		if err != nil {
			s.Warn("cleanup bkill failed", "err", err)
		}
	}
}
