// Copyright © 2016-2021 Genome Research Limited
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
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/bsub"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/mattn/go-shellwords"
	"github.com/wtsi-ssg/wr/clog"
)

// scanBufferSize is used when scanning bjobs -w output. The default buffer size
// is 65536, but bjob names can be much bigger, so we allow for a larger buffer.
const scanBufferSize = 1000 * bufio.MaxScanTokenSize

// lsf is our implementer of scheduleri
type lsf struct {
	config             *ConfigLSF
	months             map[string]int
	dateRegex          *regexp.Regexp
	bsubRegex          *regexp.Regexp
	memLimitMultiplier float32
	queues             map[string]map[string]int
	sortedqs           []string
	sortedqKeys        []int
	bsubExe            string
	bjobsExe           string
	bkillExe           string
	privateKey         string
}

// ConfigLSF represents the configuration options required by the LSF scheduler.
// All are required with no usable defaults.
type ConfigLSF struct {
	// Deployment is one of "development" or "production".
	Deployment string

	// Shell is the shell to use to run the commands to interact with your job
	// scheduler; 'bash' is recommended.
	Shell string

	// PrivateKeyPath is the path to your private key that can be used to ssh
	// to LSF farm nodes to check on jobs if they become non-responsive.
	PrivateKeyPath string
}

// initialize finds out about lsf's hosts and queues
func (s *lsf) initialize(ctx context.Context, config interface{}) error {
	s.config = config.(*ConfigLSF)

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

	bmgroups := make(map[string]map[string]bool)
	parsedBmgroups := false

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
	reNumUnit := regexp.MustCompile(`(\d+(?:\.\d+)?) (\w)`)
	reRunLimit := regexp.MustCompile(`RUNLIMIT`)
	reParseRunlimit := regexp.MustCompile(`^\s*(\d+)(?:\.\d+)? min`)
	reUserHosts := regexp.MustCompile(`^(USERS|HOSTS):\s+(.+?)\s*$`)
	reChunkJobSize := regexp.MustCompile(`^CHUNK_JOB_SIZE:\s+(\d+)`)
	for bqScanner.Scan() {
		line := bqScanner.Text()

		if matches := reQueue.FindStringSubmatch(line); len(matches) == 2 {
			queue = matches[1]
			s.queues[queue] = make(map[string]int)
			continue
		}

		switch {
		case queue == "":
			continue
		case rePrio.MatchString(line):
			nextIsPrio = true
			continue
		case nextIsPrio:
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
		case reDefaultLimits.MatchString(line):
			lookingAtDefaults = true
			continue
		case reDefaultsFinished.MatchString(line) || !lookingAtDefaults:
			lookingAtDefaults = false

			switch {
			case reMemlimit.MatchString(line):
				nextIsMemlimit = 0
				for _, word := range strings.Fields(line) {
					nextIsMemlimit++
					if word == "MEMLIMIT" {
						break
					}
				}
				continue
			case nextIsMemlimit > 0:
				if matches := reNumUnit.FindAllStringSubmatch(line, -1); matches != nil && len(matches) >= nextIsMemlimit-1 {
					val, err := strconv.ParseFloat(matches[nextIsMemlimit-1][1], 32)
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
					s.queues[queue]["memlimit"] = int(val)
					updateHighest("memlimit", int(val))
				}
				nextIsMemlimit = 0
			case reRunLimit.MatchString(line):
				nextIsRunlimit = true
				continue
			case nextIsRunlimit:
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

				s.queues[queue]["num_users"] = len(users)
				if users["all"] {
					s.queues[queue]["num_users"] = 10000
				}

				me, err := internal.Username()
				if err != nil {
					return Error{"lsf", "initialize", fmt.Sprintf("could not get current user: %s", err)}
				}

				if !users["all"] && !users[me] {
					delete(s.queues, queue)
					queue = ""
				}
			} else if matches[2] != "all" {
				hosts := make(map[string]bool)
				for _, val := range vals {
					if strings.HasSuffix(val, "/") {
						// this is a group name, look it up in bmgroup
						if !parsedBmgroups {
							perr := s.parseBmgroups(bmgroups)
							if perr != nil {
								return perr
							}
							parsedBmgroups = true
						}
						val = strings.TrimSuffix(val, "/")
						if servers, exists := bmgroups[val]; exists {
							for server := range servers {
								hosts[server] = true
							}
						} else {
							hosts[val] = true
						}
					} else {
						hosts[val] = true
					}
				}
				s.queues[queue][kind] = len(hosts)
				updateHighest(kind, len(hosts))
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
	// [weight, sort-order]. We want to avoid chunked queues because that means
	// jobs will run sequentially instead of in parallel. For time and memory,
	// prefer the queue that is more limited, since we suppose they might be
	// less busy or will at least become free sooner
	criteriaHandling := map[string][]int{
		"hosts":      {18, 1}, // weight, sort order
		"max_user":   {10, 1},
		"max":        {5, 1},
		"prio":       {5, 0},
		"chunk_size": {10000, 0},
		"runlimit":   {5, 1},
		"memlimit":   {1, 0},
		"num_users":  {15, 0},
	}

	// fill in some default values for the criteria on all the queues
	defaults := map[string]int{"num_users": 10000, "runlimit": 31536000, "memlimit": 10000000, //nolint:mnd
		"max": 10000000, "max_user": 10000000, "users": 10000000, "hosts": 10000000, "chunk_size": 0} //nolint:mnd
	for criterion, highest := range highest {
		if highest > 0 {
			defaults[criterion] = highest + 1
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

	// instead of range over criteriaHandling, because max_user must come first
	for _, criterion := range []string{
		"max_user", "max", "hosts",
		"prio", "chunk_size", "num_users", "runlimit", "memlimit",
	} {
		// sort queues by this criterion
		sorted := internal.SortMapKeysByMapIntValue(s.queues, criterion, criteriaHandling[criterion][1] == 1)

		weight := criteriaHandling[criterion][0]
		prevVal := -1
		rank := 0
		for _, queue := range sorted {
			val := s.queues[queue][criterion]
			if prevVal != -1 {
				diff := int(math.Abs(float64(val) - float64(prevVal)))
				if diff >= 1 {
					rank++
				}
			}

			ranking[queue] += rank * weight

			prevVal = val
		}
	}

	s.sortedqs = internal.SortMapKeysByIntValue(ranking, false)

	// now s.sortedqs has [0] containing our default preferred order or queues,
	// and other numbers which can be tested against any global maximum number
	// of jobs that we should submit to LSF, and if lower than any of those
	// we prefer the order described there
	//*** we probably don't need this if we won't be having a global max
	// specified by the user

	// if a job becomes lost, scheduler needs to ssh to the host to check on the
	// process, so we store our private key
	if content, err := os.ReadFile(internal.TildaToHome(s.config.PrivateKeyPath)); err == nil {
		s.privateKey = string(content)
	}

	return nil
}

// parseBmgroups parses the output of `bmgroup`, storing group name as a key in
// the supplied map, with a map of hosts in that group as the value.
func (s *lsf) parseBmgroups(groups map[string]map[string]bool) error {
	bmgcmd := exec.Command(s.config.Shell, "-c", "bmgroup -w") // #nosec
	bmgout, err := bmgcmd.StdoutPipe()
	if err != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to create pipe for [bmgroup]: %s", err)}
	}
	if err = bmgcmd.Start(); err != nil {
		return Error{"lsf", "initialize", fmt.Sprintf("failed to start [bmgroup]: %s", err)}
	}
	bmgScanner := bufio.NewScanner(bmgout)
	for bmgScanner.Scan() {
		line := bmgScanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for i, field := range fields {
			if i == 0 {
				continue
			}
			if groups[fields[0]] == nil {
				groups[fields[0]] = make(map[string]bool)
			}
			if strings.HasSuffix(field, "/") {
				lookup := strings.TrimSuffix(field, "/")
				for server := range groups[lookup] {
					groups[fields[0]][server] = true
				}
			} else {
				groups[fields[0]][field] = true
			}
		}
	}
	return nil
}

// reserveTimeout achieves the aims of ReserveTimeout().
func (s *lsf) reserveTimeout(ctx context.Context, req *Requirements) int {
	if val, defined := req.Other["rtimeout"]; defined {
		timeout, err := strconv.Atoi(val)
		if err != nil {
			clog.Error(ctx, fmt.Sprintf("Failed to convert timeout to integer: %s", err))
			return defaultReserveTimeout
		}
		return timeout
	}
	return defaultReserveTimeout
}

// maxQueueTime achieves the aims of MaxQueueTime().
func (s *lsf) maxQueueTime(req *Requirements) time.Duration {
	queue, err := s.determineQueue(req)
	if err == nil {
		return time.Duration(s.queues[queue]["runlimit"]) * time.Second
	}
	return infiniteQueueTime
}

// schedule achieves the aims of Schedule(). Note that if rescheduling a cmd
// at a lower count, we cannot guarantee that only that number get run; it may
// end up being a few more.
func (s *lsf) schedule(ctx context.Context, cmd string, req *Requirements, priority uint8, count int) error {
	// use the given queue or find the best queue for these resource
	// requirements
	queue, err := s.determineQueue(req)
	if err != nil {
		return err // impossible to run cmd with these reqs
	}

	// get the details of everything already in the scheduler for this cmd,
	// removing from the queue anything not currently running when we're over
	// the desired count
	scheduledCount, err := s.checkCmd(ctx, cmd, count)
	if err != nil {
		return err
	}
	stillNeeded := count - scheduledCount
	if stillNeeded < 1 {
		return nil
	}

	bsubArgs := s.generateBsubArgs(ctx, queue, req, cmd, stillNeeded)

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
			defer internal.LogPanic(ctx, "lsf scheduling", true)

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

// scheduled achieves the aims of Scheduled().
func (s *lsf) scheduled(ctx context.Context, cmd string) (int, error) {
	return s.checkCmd(ctx, cmd, -1)
}

// generateBsubArgs generates the appropriate bsub args for the given req and
// cmd and queue
func (s *lsf) generateBsubArgs(ctx context.Context, queue string, req *Requirements, cmd string, needed int) []string {
	args, err := generateBsubArgs(queue, req, cmd, s.config.Deployment, needed, s.memLimitMultiplier)
	if err != nil {
		clog.Warn(ctx, err.Error())
	}

	return args
}

func generateBsubArgs(queue string, req *Requirements, cmd, deployment string, needed int, memLimitMultiplier float32) ([]string, error) {
	var bsubArgs []string
	megabytes := req.RAM
	m := float32(megabytes) * memLimitMultiplier

	bsubArgs = append(bsubArgs, "-q", queue, "-M", fmt.Sprintf("%0.0f", m),
		"-R", fmt.Sprintf("select[mem>%[1]d] rusage[mem=%[1]d] span[hosts=1]", megabytes))

	var err error

	if val, ok := req.Other["scheduler_misc"]; ok {
		var parts []string

		parts, err = parseUserArgs(val, strconv.FormatInt(int64(megabytes), 10))
		bsubArgs = append(bsubArgs, parts...)
	}

	if req.Cores > 1 {
		bsubArgs = append(bsubArgs, "-n", fmt.Sprintf("%d", int(math.Ceil(req.Cores))))
	}

	// for checkCmd() to work efficiently we must always set a job name that
	// corresponds to the cmd. It must also be unique otherwise LSF would not
	// start running jobs with duplicate names until previous ones complete
	name := jobName(cmd, deployment, true)

	if needed > 1 {
		name += fmt.Sprintf("[1-%d]", needed)
	}

	bsubArgs = append(bsubArgs, "-J", name, "-o", "/dev/null", "-e", "/dev/null", cmd)

	return bsubArgs, err
}

func parseUserArgs(userArgs, megabytes string) ([]string, error) {
	words, err := shellwords.Parse(userArgs)
	if err != nil {
		return nil, fmt.Errorf("scheduler misc option ignored since could not be parsed: %w", err)
	}

	for n := 0; n < len(words); n += 2 {
		if !strings.HasPrefix(words[n], "-") {
			return nil, errors.New("invalid lsf bsub options")
		}

		if words[n] != "-R" {
			continue
		}

		reqs, err := bsub.ParseBsubR(words[n+1])
		if err != nil {
			return nil, fmt.Errorf("scheduler misc option ignored since could not be parsed: %w", err)
		}

		reqs.ReplaceMemoryAndHosts(megabytes, "1")

		words[n+1] = reqs.String()
	}

	return words, nil
}

type BsubValidator map[string]bool

func (s BsubValidator) Validate(opts string) (valid bool) {
	var ok bool

	if valid, ok = s[opts]; ok {
		return valid
	}

	defer func() {
		s[opts] = valid
	}()

	args, err := generateBsubArgs("anything", &Requirements{
		RAM:   1,
		Other: map[string]string{"scheduler_misc": opts},
	}, "echo", "production", 1, 1)
	if err != nil {
		return false
	}

	cmd := exec.Command("bsub", args...)

	cmd.Env = append(os.Environ(), "BSUB_CHK_RESREQ=1")
	err = cmd.Run()
	valid = err == nil

	return valid
}

// recover achieves the aims of Recover(). We don't have to do anything, since
// when the cmd finishes running, LSF itself will clean up.
func (s *lsf) recover(ctx context.Context, cmd string, req *Requirements, host *RecoveredHostDetails) error {
	return nil
}

// busy returns true if there are any jobs with our jobName() prefix in any
// queue. It also returns true if the most recently submitted job is pending or
// running
func (s *lsf) busy(ctx context.Context) bool {
	count, err := s.checkCmd(ctx, "", -1)
	if err != nil {
		// busy() doesn't return an error, so just assume we're busy
		return true
	}
	return count > 0
}

// determineQueue picks a queue, preferring ones that are more likely to run our
// job the soonest (amongst those that are capable of running it). If req.Other
// contains a scheduler_queue value, returns that instead.
func (s *lsf) determineQueue(req *Requirements) (string, error) {
	if queue, ok := req.Other["scheduler_queue"]; ok {
		return queue, nil
	}

	seconds := req.Time.Seconds() + minimumQueueTime.Seconds()

	var queuesToAvoid []string
	if req.Other["scheduler_queues_avoid"] != "" {
		queuesToAvoid = strings.Split(req.Other["scheduler_queues_avoid"], ",")
	}

	for _, queue := range s.sortedqs {
		if queueShouldBeAvoided(queue, queuesToAvoid) {
			continue
		}

		if s.queueHasTooLittleMemory(queue, req) {
			continue
		}

		if s.queueHasTooLittleTime(queue, seconds) {
			continue
		}

		return queue, nil
	}

	return "", Error{"lsf", "determineQueue", ErrImpossible}
}

func queueShouldBeAvoided(queue string, queuesToAvoid []string) bool {
	for _, queueToAvoid := range queuesToAvoid {
		if strings.Contains(queue, queueToAvoid) {
			return true
		}
	}

	return false
}

func (s *lsf) queueHasTooLittleMemory(queue string, req *Requirements) bool {
	return s.queues[queue]["memlimit"] > 0 && s.queues[queue]["memlimit"] < req.RAM
}

func (s *lsf) queueHasTooLittleTime(queue string, seconds float64) bool {
	return s.queues[queue]["runlimit"] > 0 && float64(s.queues[queue]["runlimit"]) < seconds
}

// checkCmd asks LSF how many of the supplied cmd are running, and if max >= 0
// is supplied, kills any extraneous non-running jobs for the cmd. If the
// supplied cmd is the empty string, it will report/act on all cmds submitted
// by schedule() for this deployment.
func (s *lsf) checkCmd(ctx context.Context, cmd string, max int) (count int, err error) {
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
		cb := func(jobID, stat, jobName string) {
			count++
			if count > max && stat != "RUN" {
				var sidaid string
				if strings.HasSuffix(jobID, "]") {
					sidaid = jobID
				} else if aidmatch := reAid.FindStringSubmatch(jobName); len(aidmatch) == 2 {
					sidaid = jobID + "[" + aidmatch[1] + "]"
				}
				if sidaid != "" {
					toKill = append(toKill, sidaid)
				}
				count--
			}
		}
		err = s.parseBjobs(jobPrefix, cb)

		if len(toKill) > 1 {
			killcmd := exec.Command(s.bkillExe, toKill...) // #nosec
			out, errk := killcmd.CombinedOutput()
			if errk != nil && !strings.HasPrefix(string(out), "Job has already finished") {
				clog.Warn(ctx, "checkCmd bkill failed", "cmd", s.bkillExe, "toKill", toKill, "err", errk, "out", string(out))
			}
		}
	} else {
		cb := func(jobID, stat, jobName string) {
			count++
		}
		err = s.parseBjobs(jobPrefix, cb)
	}

	return count, err
}

type bjobsCB func(jobID, stat, jobName string)

// parseBjobs runs bjobs, filters on a job name prefix, excludes exited jobs and
// gives columns 1 (JOBID), 3 (STAT) and 7 (JOB_NAME) to your callback for each
// bjobs output line.
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
	bjScanner.Buffer([]byte{}, scanBufferSize)

	for bjScanner.Scan() {
		line := bjScanner.Text()
		fields := strings.Fields(line)

		if len(fields) > 7 {
			if fields[2] == "EXIT" || fields[2] == "DONE" || !strings.HasPrefix(fields[6], jobPrefix) {
				continue
			}
			callback(fields[0], fields[2], fields[6])
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

// getHost returns a cloud.Server for the given host.
func (s *lsf) getHost(host string) (Host, bool) {
	name := "unknown"
	if user, err := user.Current(); err == nil {
		name = user.Username
	}

	server := cloud.NewServer(name, host, s.privateKey)
	if server == nil {
		return nil, false
	}

	return server, true
}

// setMessageCallBack does nothing at the moment, since we don't generate any
// messages for the user.
func (s *lsf) setMessageCallBack(ctx context.Context, cb MessageCallBack) {}

// setBadServerCallBack does nothing, since we're not a cloud-based scheduler.
func (s *lsf) setBadServerCallBack(ctx context.Context, cb BadServerCallBack) {}

// cleanup bkills any remaining jobs we created
func (s *lsf) cleanup(ctx context.Context) {
	toKill := []string{"-b"}
	cb := func(jobID, stat, jobName string) {
		toKill = append(toKill, jobID)
	}
	err := s.parseBjobs(fmt.Sprintf("wr%s_", s.config.Deployment[0:1]), cb)
	if err != nil {
		clog.Error(ctx, "cleaup parse bjobs failed", "err", err)
	}
	if len(toKill) > 1 {
		killcmd := exec.Command(s.bkillExe, toKill...) // #nosec
		err = killcmd.Run()
		if err != nil {
			clog.Warn(ctx, "cleanup bkill failed", "err", err)
		}
	}
}
