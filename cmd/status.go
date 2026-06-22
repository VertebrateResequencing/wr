/*******************************************************************************
 * Copyright (c) 2016-2022, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/carbocation/runningvariance"
	"github.com/spf13/cobra"
)

const (
	shortTimeFormat = "06/1/2-15:04:05"
	allRepGrps      = "all above"
)

var (
	errStatusStateFiltersFile = errors.New("state filters (--buried/--running/--pending/--dependent) are only " +
		"supported in default or report group (-i) mode; remove them when using -f")
	errStatusStateFiltersCmdLine = errors.New("state filters (--buried/--running/--pending/--dependent) are only " +
		"supported in default or report group (-i) mode; remove them when using -l")
	errStatusStateFiltersInternal = errors.New("state filters (--buried/--running/--pending/--dependent) cannot " +
		"be used with --internal because internal job lookups cannot be state-filtered")
)

// options for this cmd
var (
	cmdFileStatus   string
	cmdIDStatus     string
	cmdIDIsSubStr   bool
	cmdIDIsInternal bool
	cmdLine         string
	showBuried      bool
	showRunning     bool
	showPending     bool
	showDependent   bool
	showEnv         bool
	outputFormat    string
	statusLimit     int
	fromHost        string
)

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get status of commands",
	Long: `You can find the status of commands you've previously added using
"wr add" by running this command.

Specify one of the flags -f, -l  or -i to choose which commands you want the
status of. If none are supplied, you will get the status of all your currently
incomplete commands.

-i is the report group (-i) you supplied to "wr add" when you added the job(s)
you want the status of now. Combining with -z lets you get the status of jobs
in multiple report groups, assuming you have arranged that related groups share
some substring. Alternatively -y lets you specify -i as the internal job id
reported when using this command.

The file to provide -f is in the format taken by "wr add".

In -f and -l mode you must provide the cwd the commands were set to run in, if
CwdMatters (and must NOT be provided otherwise). Likewise provide the mounts
option that was used when the command was added, if any. You can do this by
using the -c and --mounts/--mounts_json options in -l mode, or by providing the
same file you gave to "wr add" in -f mode.

There are 6 output formats to choose from with -o (you can shorten the output
name to just the first letter, eg. -o c):
  "counts" just displays the count of jobs in each possible state.
  "summary" shows the counts broken down by report group, along with the mean
    (and standard deviation) resource usage of completed jobs in each report
    group, and the internal identifiers of any buried jobs, broken down by exit
    code+failure reason.
  "details" groups jobs with the same state, reason for failure and exitcode
    together and shows the complete details of --limit random jobs in each group
    (and you are told how many are not being displayed). A limit of 0 turns off
    grouping and shows all your desired commands individually, but you could hit
    a timeout if retrieving the details of very many (tens of thousands+)
	commands. If more than 1000 buried jobs get displayed, their STDOUT and
	STDERR are not shown.
  "plain" outputs 2 tab separated columns: internal job id and current state of
	that job. Possible states are: delayed, ready, reserved, running, lost,
	buried, complete. If any jobs are buried, exits non-0 as well.
  "table" outputs aligned rows for the grouped jobs returned by the status
    query. As with details/json, --limit controls how many matching jobs can be
    shown from each status group. Columns can be customised with WR_STATUS_FORMAT
    using FIELD:width entries.
  "json" simply dumps the complete details of every job out as an array of
    JSON objects. The properties of the JSON objects are described in the
    documentation for wr's REST API. If more than 1000 buried jobs get
	returned, their STDOUT and STDERR are excluded.

Note that when jobs run, wr only stores the head and tail of STDOUT and STDERR,
and these are only kept and displayed for buried jobs. This should be all you
need for debugging. If your command produces something you must keep on STDOUT
or STDERR, your command should write that to a file itself with a normal shell
redirect (eg. "mycmd > stdout.txt").
`,
	Run: func(cmd *cobra.Command, args []string) {
		set := countGetJobArgs()
		if set > 1 {
			die("-f, -i and -l are mutually exclusive; only specify one of them")
		}

		cmdStates := statusStateFilters()
		if err := validateStatusStateFilters(cmdStates); err != nil {
			die("%s", err)
		}

		timeout := time.Duration(timeoutint) * time.Second

		jq := connect(timeout)
		var err error
		defer func() {
			err = jq.Disconnect()
			if err != nil {
				warn("Disconnecting from the server failed: %s", err)
			}
		}()

		if !statusOutputUsesGroupedJobs(outputFormat) {
			statusLimit = 0
		}

		showEnv = showEnv && statusOutputGetsEnv(outputFormat)

		useFastStatus := canUseFastStatusOutput(outputFormat)

		statusSummaries := getFastStatusSummaries(jq, cmdStates, useFastStatus, outputFormat)
		if statusSummaries == nil {
			useFastStatus = false
		}

		var jobs []*jobqueue.Job
		showextra := cmdFileStatus == ""

		if !useFastStatus {
			jobs = getJobsForStates(jq, cmdStates, set == 0, statusLimit, statusOutputGetsStd(outputFormat), showEnv)

			if fromHost != "" {
				var subset []*jobqueue.Job

				for _, job := range jobs {
					if job.Host == fromHost || job.HostID == fromHost || job.HostIP == fromHost {
						subset = append(subset, job)
					}
				}

				jobs = subset
			}
		}

		switch outputFormat {
		case statusOutputFormatCounts, statusOutputFormatCountsAlias:
			if useFastStatus {
				printStatusCounts(mergeStatusSummaries(statusSummaries).Counts)

				break
			}

			var d, re, b, ru, l, c, dep int
			for _, job := range jobs {
				switch job.State {
				case jobqueue.JobStateDelayed:
					d += 1 + job.Similar
				case jobqueue.JobStateReady:
					re += 1 + job.Similar
				case jobqueue.JobStateBuried:
					b += 1 + job.Similar
				case jobqueue.JobStateReserved, jobqueue.JobStateRunning:
					ru += 1 + job.Similar
				case jobqueue.JobStateLost:
					l += 1 + job.Similar
				case jobqueue.JobStateComplete:
					c += 1 + job.Similar
				case jobqueue.JobStateDependent:
					dep += 1 + job.Similar
				}
			}

			printStatusCounts(map[jobqueue.JobState]int{
				jobqueue.JobStateComplete:  c,
				jobqueue.JobStateRunning:   ru,
				jobqueue.JobStateReady:     re,
				jobqueue.JobStateDependent: dep,
				jobqueue.JobStateLost:      l,
				jobqueue.JobStateDelayed:   d,
				jobqueue.JobStateBuried:    b,
			})
		case statusOutputFormatPlain, statusOutputFormatPlainAlias:
			buried := false
			for _, job := range jobs {
				fmt.Printf("%s\t%s\n", job.Key(), job.State)
				if job.State == jobqueue.JobStateBuried {
					buried = true
				}
			}
			if buried {
				os.Exit(1)
			}
			os.Exit(0)
		case statusOutputFormatSummary, statusOutputFormatSummaryAlias:
			if useFastStatus {
				printStatusSummaries(statusSummaries)

				break
			}

			counts := make(map[string]map[jobqueue.JobState]int)
			buried := make(map[string]map[string][]string)
			memory := make(map[string]*runningvariance.RunningStat)
			disk := make(map[string]*runningvariance.RunningStat)
			walltime := make(map[string]*runningvariance.RunningStat)
			cputime := make(map[string]*runningvariance.RunningStat)
			startends := make(map[string][]time.Time)
			counts[allRepGrps] = make(map[jobqueue.JobState]int)
			for _, job := range jobs {
				if _, exists := counts[job.RepGroup]; !exists {
					counts[job.RepGroup] = make(map[jobqueue.JobState]int)
				}
				state := job.State
				if state == jobqueue.JobStateReserved {
					state = jobqueue.JobStateRunning
				}
				counts[job.RepGroup][job.State]++
				counts[allRepGrps][job.State]++

				switch state { //nolint:exhaustive
				case jobqueue.JobStateBuried:
					if _, exists := buried[job.RepGroup]; !exists {
						buried[job.RepGroup] = make(map[string][]string)
					}
					group := fmt.Sprintf("exitcode.%d,\"%s\"", job.Exitcode, job.FailReason)
					buried[job.RepGroup][group] = append(buried[job.RepGroup][group], job.Key())
				case jobqueue.JobStateComplete:
					if _, exists := memory[job.RepGroup]; !exists {
						memory[job.RepGroup] = runningvariance.NewRunningStat()
						disk[job.RepGroup] = runningvariance.NewRunningStat()
						walltime[job.RepGroup] = runningvariance.NewRunningStat()
						cputime[job.RepGroup] = runningvariance.NewRunningStat()
						startends[job.RepGroup] = []time.Time{job.StartTime, job.EndTime}
					}
					memory[job.RepGroup].Push(float64(job.PeakRAM))
					disk[job.RepGroup].Push(float64(job.PeakDisk))
					walltime[job.RepGroup].Push(float64(job.WallTime()))
					cputime[job.RepGroup].Push(float64(job.CPUtime))
					if job.StartTime.Before(startends[job.RepGroup][0]) {
						startends[job.RepGroup][0] = job.StartTime
					}
					if job.EndTime.After(startends[job.RepGroup][1]) {
						startends[job.RepGroup][1] = job.EndTime
					}

					if _, exists := memory[allRepGrps]; !exists {
						memory[allRepGrps] = runningvariance.NewRunningStat()
						disk[allRepGrps] = runningvariance.NewRunningStat()
						walltime[allRepGrps] = runningvariance.NewRunningStat()
						cputime[allRepGrps] = runningvariance.NewRunningStat()
						startends[allRepGrps] = []time.Time{job.StartTime, job.EndTime}
					}
					memory[allRepGrps].Push(float64(job.PeakRAM))
					disk[allRepGrps].Push(float64(job.PeakDisk))
					walltime[allRepGrps].Push(float64(job.WallTime()))
					cputime[allRepGrps].Push(float64(job.CPUtime))
					if job.StartTime.Before(startends[allRepGrps][0]) {
						startends[allRepGrps][0] = job.StartTime
					}
					if job.EndTime.After(startends[allRepGrps][1]) {
						startends[allRepGrps][1] = job.EndTime
					}
				}
			}

			// sort RepGroups for a nicer display
			all := counts[allRepGrps]
			delete(counts, allRepGrps)
			var rgs []string
			for rg := range counts {
				rgs = append(rgs, rg)
			}
			sort.Strings(rgs)

			if len(rgs) > 1 {
				rgs = append(rgs, allRepGrps)
				counts[allRepGrps] = all
			}

			// display summary for each RepGroup
			for _, rg := range rgs {
				var usage string
				if counts[rg][jobqueue.JobStateComplete] > 0 {
					usage = fmt.Sprintf(" memory=%dMB(+/-%dMB) disk=%dMB(+/-%dMB) walltime=%s(+/-%s) cputime=%s(+/-%s)", int(memory[rg].Mean()), int(memory[rg].StandardDeviation()), int(disk[rg].Mean()), int(disk[rg].StandardDeviation()), time.Duration(walltime[rg].Mean()), time.Duration(walltime[rg].StandardDeviation()), time.Duration(cputime[rg].Mean()), time.Duration(cputime[rg].StandardDeviation()))

					if counts[rg][jobqueue.JobStateComplete] > 1 {
						usage = usage + fmt.Sprintf(" started=%s ended=%s elapsed=%s", startends[rg][0].Format(shortTimeFormat), startends[rg][1].Format(shortTimeFormat), startends[rg][1].Sub(startends[rg][0]))
					}
				}

				var dead string
				if counts[rg][jobqueue.JobStateBuried] > 0 {
					// sort the bury groups
					var bgs []string
					for bg := range buried[rg] {
						bgs = append(bgs, bg)
					}
					sort.Strings(bgs)

					for _, bg := range bgs {
						dead += fmt.Sprintf(" %s=%s", bg, strings.Join(buried[rg][bg], ","))
					}
				}

				fmt.Printf("%s : complete=%d running=%d ready=%d dependent=%d lost=%d delayed=%d buried=%d%s%s\n", rg, counts[rg][jobqueue.JobStateComplete], counts[rg][jobqueue.JobStateRunning], counts[rg][jobqueue.JobStateReady], counts[rg][jobqueue.JobStateDependent], counts[rg][jobqueue.JobStateLost], counts[rg][jobqueue.JobStateDelayed], counts[rg][jobqueue.JobStateBuried], usage, dead)
			}
		case statusOutputFormatDetails, statusOutputFormatDetailsAlias:
			// print out status information for each job
			for _, job := range jobs {
				cwd := job.Cwd
				var mounts string
				if len(job.MountConfigs) > 0 {
					mounts = fmt.Sprintf("Mounts: %s\n", job.MountConfigs)
				}
				var homeChanged string
				if job.ActualCwd != "" {
					cwd = job.ActualCwd
					if job.ChangeHome {
						homeChanged = "Changed home: true\n"
					}
				}
				var modules string
				if len(job.Modules) > 0 {
					modules += fmt.Sprintf("Modules: %s; ", strings.Join(job.Modules, ", "))
				}
				var groups string
				if len(job.DepGroups) > 0 {
					groups = fmt.Sprintf("Dependency groups: %s; ", strings.Join(job.DepGroups, ", "))
				}
				if len(job.Dependencies) > 0 {
					groups += fmt.Sprintf("Dependencies: %s; ", strings.Join(job.Dependencies.Stringify(), ", "))
				}
				if len(job.LimitGroups) > 0 {
					groups += fmt.Sprintf("Limit groups: %s; ", strings.Join(job.LimitGroups, ", "))
				}
				var containerInfo string
				if job.WithDocker != "" {
					containerInfo = fmt.Sprintf("Cmd running inside docker container running image: %s\n", job.WithDocker)
				}
				if job.WithSingularity != "" {
					containerInfo = fmt.Sprintf("Cmd running inside singularity container running image: %s\n", job.WithSingularity)
				}
				if job.ContainerMounts != "" {
					containerInfo += fmt.Sprintf("Container has these mounts: %s\n", job.ContainerMounts)
				}
				if job.MonitorDocker != "" {
					dockerID := job.MonitorDocker
					if dockerID == "?" {
						dockerID += " (first container started after cmd)"
					}
					containerInfo += fmt.Sprintf("Docker container monitoring turned on for: %s\n", dockerID)
				}
				var behaviours string
				if len(job.Behaviours) > 0 {
					behaviours = fmt.Sprintf("Behaviours: %s\n", job.Behaviours)
				}
				var other string
				if len(job.Requirements.Other) > 0 {
					var others []string
					for key, val := range job.Requirements.Other {
						others = append(others, key+":"+val)
					}
					other = fmt.Sprintf("Resource requirements: %s\n", strings.Join(others, ", "))
				}

				cores := strconv.FormatFloat(job.Requirements.Cores, 'f', -1, 64)

				fmt.Printf("\n# %s\nCwd: %s\n%s%s%s%s%s%sId: %s (%s); "+
					"Requirements group: %s; %sPriority: %d; Attempts: %d\n"+
					"Expected requirements: { memory: %dMB; time: %s; cpus: %s disk: %dGB }\n",
					job.Cmd, cwd, modules, mounts, homeChanged, containerInfo, behaviours, other, job.RepGroup, job.Key(),
					job.ReqGroup, groups, job.Priority, job.Attempts,
					job.Requirements.RAM, job.Requirements.Time, cores, job.Requirements.Disk)

				switch job.State {
				case jobqueue.JobStateDelayed:
					ready := job.EndTime.Add(job.DelayTime)
					fmt.Printf("Status: delayed following a problem, prior to retrying; will become ready in %s (attempted at %s)\n",
						time.Until(ready).Round(time.Second), job.StartTime.Format(shortTimeFormat))
				case jobqueue.JobStateReady:
					fmt.Println("Status: ready to be picked up by a `wr runner`")
				case jobqueue.JobStateDependent:
					fmt.Println("Status: dependent on other jobs")
				case jobqueue.JobStateBuried:
					fmt.Printf("Status: buried - you need to fix the problem and then `wr retry` (attempted at %s)\n", job.StartTime.Format(shortTimeFormat))
				case jobqueue.JobStateReserved, jobqueue.JobStateRunning:
					fmt.Printf("Status: running (started %s)\n", job.StartTime.Format(shortTimeFormat))
				case jobqueue.JobStateLost:
					fmt.Printf("Status: lost contact (started %s; lost %s)\n", job.StartTime.Format(shortTimeFormat), job.EndTime.Format(shortTimeFormat))
				case jobqueue.JobStateComplete:
					fmt.Printf("Status: complete (started %s; ended %s)\n", job.StartTime.Format(shortTimeFormat), job.EndTime.Format(shortTimeFormat))
				}

				if job.FailReason != "" {
					fmt.Printf("Previous problem: %s\n", job.FailReason)
				}

				var hostID string
				if job.HostID != "" {
					hostID = ", ID: " + job.HostID
				}

				if job.Exited {
					prefix := "Stats"
					if job.State != jobqueue.JobStateComplete {
						prefix = "Stats of previous attempt"
					}
					fmt.Printf("%s: { Exit code: %d; Peak memory: %dMB; Peak disk: %dMB; Wall time: %s; CPU time: %s }\nHost: %s (IP: %s%s); Pid: %d\n", prefix, job.Exitcode, job.PeakRAM, job.PeakDisk, job.WallTime(), job.CPUtime, job.Host, job.HostIP, hostID, job.Pid)
					if showextra && job.Exitcode != 0 {
						stdout, errs := job.StdOut()
						if errs != nil {
							warn("problem reading the cmd's STDOUT: %s", errs)
						} else if stdout != "" {
							fmt.Printf("StdOut:\n%s\n", stdout)
						} else {
							fmt.Printf("StdOut: [none]\n")
						}
						stderr, errs := job.StdErr()
						if errs != nil {
							warn("problem reading the cmd's STDERR: %s", errs)
						} else if stderr != "" {
							fmt.Printf("StdErr:\n%s\n", stderr)
						} else {
							fmt.Printf("StdErr: [none]\n")
						}
					}
				} else if job.State == jobqueue.JobStateRunning || job.State == jobqueue.JobStateLost {
					fmt.Printf("Stats: { Wall time: %s }\nHost: %s (IP: %s%s); Pid: %d\n", job.WallTime(), job.Host, job.HostIP, hostID, job.Pid)
					//*** we should be able to peek at STDOUT & STDERR, and see
					// Peak memory during a run... but is that possible/ too
					// expensive? Maybe we could communicate directly with the
					// runner?...
				} else if showextra {
					// it's possible for jobs that got buried before they even
					// ran to have details of the bury in their stderr
					stderr, errs := job.StdErr()
					if errs == nil && stderr != "" {
						fmt.Printf("Details: %s\n", stderr)
					}
				}

				if showextra && showEnv {
					env, erre := job.Env()
					if erre != nil {
						warn("problem reading the cmd's Env: %s", erre)
					} else {
						fmt.Printf("Env: %s\n", env)
					}
				}

				if job.Similar > 0 {
					fr := ""
					if job.FailReason != "" {
						fr = " and problem"
					}
					er := ""
					if job.Exited && job.Exitcode != 0 {
						if fr != "" {
							er = ", exit code"
						} else {
							er = " and exit code"
						}
					}
					fmt.Printf("+ %d other commands with the same status%s%s\n", job.Similar, er, fr)
				}
			}
		case statusOutputFormatJSON, statusOutputFormatJSONAlias:
			jstati := make([]jobqueue.JStatus, len(jobs))
			for i, job := range jobs {
				jstati[i], err = job.ToStatus()
				if err != nil {
					die("failed to convert job to status: %s", err)
				}
			}

			encoder := json.NewEncoder(os.Stdout)
			encoder.SetEscapeHTML(false)
			err = encoder.Encode(jstati)
			if err != nil {
				die("failed to encode jobs: %s", err)
			}
		case statusOutputFormatTable, statusOutputFormatTableAlias:
			err = writeStatusTable(os.Stdout, jobs)
			if err != nil {
				die("failed to write status table: %s", err)
			}
		default:
			die("invalid -o format specified")
		}

		writeStatusAlerts(os.Stdout, jq, outputFormat)

		fmt.Printf("\n")
	},
}

func init() {
	RootCmd.AddCommand(statusCmd)

	// flags specific to this sub-command
	statusCmd.Flags().StringVarP(&cmdFileStatus, "file", "f", "", "file containing commands you want the status of; - means read from STDIN")
	statusCmd.Flags().StringVarP(&cmdIDStatus, "identifier", "i", "", "identifier of the commands you want the status of")
	statusCmd.Flags().BoolVarP(&cmdIDIsSubStr, "search", "z", false, "treat -i as a substring to match against all report groups")
	statusCmd.Flags().BoolVarP(&cmdIDIsInternal, "internal", "y", false, "treat -i as an internal job id")
	statusCmd.Flags().StringVarP(&cmdLine, "cmdline", "l", "", "a command line you want the status of")
	statusCmd.Flags().StringVarP(&cmdCwd, "cwd", "c", "", "working dir that the command(s) specified by -l or -f were set to run in")
	statusCmd.Flags().StringVarP(&mountJSON, "mount_json", "j", "", "mounts that the command(s) specified by -l or -f were set to use (JSON format)")
	statusCmd.Flags().StringVar(&mountSimple, "mounts", "", "mounts that the command(s) specified by -l or -f were set to use (simple format)")
	statusCmd.Flags().BoolVarP(&showBuried, "buried", "b", false, "in default or -i mode only, only show the status of buried commands")
	statusCmd.Flags().StringVar(&fromHost, "host", "", "filter output to only show the status of commands that ran on the given host (ID, name or IP)")
	statusCmd.Flags().BoolVarP(&showRunning, "running", "r", false, "in default or -i mode only, only show the status of running commands")
	statusCmd.Flags().BoolVar(&showPending, "pending", false,
		"in default or -i mode only, only show the status of pending commands")
	statusCmd.Flags().BoolVar(&showDependent, "dependent", false,
		"in default or -i mode only, only show the status of dependent commands")
	statusCmd.Flags().BoolVarP(&showEnv, "env", "e", false, "in -o d mode, except in -f mode, also show the environment variables the command(s) ran with")
	statusCmd.Flags().StringVarP(&outputFormat, "output", "o", "details",
		"['counts','summary','details','plain','table','json'] output format")
	statusCmd.Flags().IntVar(&statusLimit, "limit", 1,
		"for grouped outputs (details, table and json), number of commands that share "+
			"the same properties to display per group; 0 displays all")

	statusCmd.Flags().IntVar(&timeoutint, "timeout", 120, "how long (seconds) to wait to get a reply from 'wr manager'")
}

func countGetJobArgs() int {
	set := 0
	if cmdFileStatus != "" {
		set++
	}
	if cmdIDStatus != "" {
		set++
	}
	if cmdLine != "" {
		set++
	}
	if cmdAll {
		set++
	}
	return set
}

func statusStateFilters() []jobqueue.JobState {
	var states []jobqueue.JobState
	if showBuried {
		states = append(states, jobqueue.JobStateBuried)
	}

	if showRunning {
		states = append(states, jobqueue.JobStateRunning)
	}

	if showPending {
		states = append(states, jobqueue.JobStateReady)
	}

	if showDependent {
		states = append(states, jobqueue.JobStateDependent)
	}

	return states
}

func validateStatusStateFilters(cmdStates []jobqueue.JobState) error {
	if len(cmdStates) == 0 {
		return nil
	}

	switch {
	case cmdFileStatus != "":
		return errStatusStateFiltersFile
	case cmdLine != "":
		return errStatusStateFiltersCmdLine
	case cmdIDStatus != "" && cmdIDIsInternal:
		return errStatusStateFiltersInternal
	}

	return nil
}

func canUseFastStatusOutput(format string) bool {
	if fromHost != "" || cmdFileStatus != "" || cmdLine != "" || (cmdIDStatus != "" && cmdIDIsInternal) {
		return false
	}

	switch format {
	case "counts", "c", "summary", "s":
		return true
	default:
		return false
	}
}

func getFastStatusSummaries(jq *jobqueue.Client, cmdStates []jobqueue.JobState, useFastStatus bool,
	format string) map[string]*jobqueue.RepGroupStatus {
	if !useFastStatus {
		return nil
	}

	match := jobqueue.RepGroupMatchExact
	if cmdIDIsSubStr {
		match = jobqueue.RepGroupMatchSubStr
	}

	includeComplete := cmdIDStatus != "" || cmdAll
	includeStatusDetails := format == "summary" || format == "s"

	summaries, err := jq.GetStatusByRepGroupMatch(cmdIDStatus, match, cmdStates, includeComplete, includeStatusDetails)
	if err != nil {
		if fastStatusUnsupported(err) {
			return nil
		}

		die("failed to get job status counts corresponding to your settings: %s", err)
	}

	return summaries
}

func printStatusCounts(counts map[jobqueue.JobState]int) {
	fmt.Printf("complete: %d\nrunning: %d\nready: %d\ndependent: %d\nlost contact: %d\ndelayed: %d\nburied: %d\n",
		counts[jobqueue.JobStateComplete],
		counts[jobqueue.JobStateRunning]+counts[jobqueue.JobStateReserved],
		counts[jobqueue.JobStateReady],
		counts[jobqueue.JobStateDependent],
		counts[jobqueue.JobStateLost],
		counts[jobqueue.JobStateDelayed],
		counts[jobqueue.JobStateBuried])
}

func fastStatusUnsupported(err error) bool {
	var jqErr jobqueue.Error

	return errors.As(err, &jqErr) && jqErr.Err == jobqueue.ErrUnknownCommand
}

func printStatusSummaries(summaries map[string]*jobqueue.RepGroupStatus) {
	rgs := make([]string, 0, len(summaries))
	for rg := range summaries {
		rgs = append(rgs, rg)
	}

	sort.Strings(rgs)

	if len(rgs) > 1 {
		summaries[allRepGrps] = mergeStatusSummaries(summaries)
		rgs = append(rgs, allRepGrps)
	}

	for _, rg := range rgs {
		printRepGroupStatusSummary(rg, summaries[rg])
	}
}

func mergeStatusSummaries(summaries map[string]*jobqueue.RepGroupStatus) *jobqueue.RepGroupStatus {
	merged := jobqueue.NewRepGroupStatus()
	for _, summary := range summaries {
		merged.Merge(summary)
	}

	return merged
}

func printRepGroupStatusSummary(rg string, summary *jobqueue.RepGroupStatus) {
	counts := summary.Counts
	usage := statusSummaryUsage(summary)
	dead := statusSummaryBuried(summary)

	fmt.Printf("%s : complete=%d running=%d ready=%d dependent=%d lost=%d delayed=%d buried=%d%s%s\n",
		rg,
		counts[jobqueue.JobStateComplete],
		counts[jobqueue.JobStateRunning]+counts[jobqueue.JobStateReserved],
		counts[jobqueue.JobStateReady],
		counts[jobqueue.JobStateDependent],
		counts[jobqueue.JobStateLost],
		counts[jobqueue.JobStateDelayed],
		counts[jobqueue.JobStateBuried],
		usage,
		dead)
}

func statusSummaryUsage(summary *jobqueue.RepGroupStatus) string {
	if summary.Counts[jobqueue.JobStateComplete] == 0 || summary.Memory.NumDataValues() == 0 {
		return ""
	}

	usage := fmt.Sprintf(" memory=%dMB(+/-%dMB) disk=%dMB(+/-%dMB) walltime=%s(+/-%s) cputime=%s(+/-%s)",
		int(summary.Memory.Mean()),
		int(summary.Memory.StandardDeviation()),
		int(summary.Disk.Mean()),
		int(summary.Disk.StandardDeviation()),
		time.Duration(summary.Walltime.Mean()),
		time.Duration(summary.Walltime.StandardDeviation()),
		time.Duration(summary.CPUtime.Mean()),
		time.Duration(summary.CPUtime.StandardDeviation()))

	if summary.Counts[jobqueue.JobStateComplete] > 1 && !summary.StartTime.IsZero() && !summary.EndTime.IsZero() {
		usage += fmt.Sprintf(" started=%s ended=%s elapsed=%s",
			summary.StartTime.Format(shortTimeFormat),
			summary.EndTime.Format(shortTimeFormat),
			summary.EndTime.Sub(summary.StartTime))
	}

	return usage
}

func statusSummaryBuried(summary *jobqueue.RepGroupStatus) string {
	if summary.Counts[jobqueue.JobStateBuried] == 0 {
		return ""
	}

	bgs := make([]string, 0, len(summary.Buried))
	for bg := range summary.Buried {
		bgs = append(bgs, bg)
	}

	sort.Strings(bgs)

	var dead strings.Builder
	for _, bg := range bgs {
		fmt.Fprintf(&dead, " %s=%s", bg, strings.Join(summary.Buried[bg], ","))
	}

	return dead.String()
}

func getJobsForStates(jq *jobqueue.Client, cmdStates []jobqueue.JobState, all bool,
	statusLimit int, showStd, showEnv bool) []*jobqueue.Job {
	if len(cmdStates) == 0 {
		return getJobs(jq, "", all, statusLimit, showStd, showEnv)
	}

	var jobs []*jobqueue.Job
	for _, cmdState := range cmdStates {
		jobs = append(jobs, getJobs(jq, cmdState, all, statusLimit, showStd, showEnv)...)
	}

	return jobs
}

func getJobs(jq *jobqueue.Client, cmdState jobqueue.JobState, all bool, statusLimit int, showStd, showEnv bool) []*jobqueue.Job {
	var jobs []*jobqueue.Job
	var err error

	switch {
	case all:
		// get all jobs
		jobs, err = jq.GetIncomplete(statusLimit, cmdState, showStd, showEnv)
	case cmdIDStatus != "":
		if cmdIDIsInternal {
			// get the job with this internal id
			var job *jobqueue.Job
			job, err = jq.GetByEssence(&jobqueue.JobEssence{
				JobKey: cmdIDStatus,
			}, showStd, showEnv)
			if job != nil {
				jobs = append(jobs, job)
			}
		} else {
			// get all jobs with this identifier (repgroup)
			jobs, err = jq.GetByRepGroup(cmdIDStatus, cmdIDIsSubStr, statusLimit, cmdState, showStd, showEnv)
		}
	case cmdFileStatus != "":
		// parse the supplied commands
		parsedJobs, _, _ := parseCmdFile(jq, false)

		// round-trip via the server to get those that actually exist in
		// the queue
		jes := jobsToJobEssenses(parsedJobs)
		jobs, err = jq.GetByEssences(jes)
		if len(jobs) < len(parsedJobs) {
			warn("%d/%d cmds were not found", len(parsedJobs)-len(jobs), len(parsedJobs))
		}
	case cmdLine != "":
		// get job that has the supplied command
		var defaultMounts jobqueue.MountConfigs
		if mountJSON != "" || mountSimple != "" {
			defaultMounts = mountParse(mountJSON, mountSimple)
		}
		var job *jobqueue.Job
		job, err = jq.GetByEssence(&jobqueue.JobEssence{Cmd: cmdLine, Cwd: cmdCwd, MountConfigs: defaultMounts}, showStd, showEnv)
		if job != nil {
			jobs = append(jobs, job)
		}
	}

	if err != nil {
		die("failed to get jobs corresponding to your settings: %s", err)
	}

	return jobs
}

func jobsToJobEssenses(jobs []*jobqueue.Job) []*jobqueue.JobEssence {
	jes := make([]*jobqueue.JobEssence, 0, len(jobs))
	for _, job := range jobs {
		jes = append(jes, job.ToEssense())
	}
	return jes
}
