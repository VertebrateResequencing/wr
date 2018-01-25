// Copyright © 2016-2017 Genome Research Limited
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

package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/spf13/cobra"
)

const shortTimeFormat = "06/1/2-15:04:05"

// options for this cmd
var cmdFileStatus string
var cmdIDStatus string
var cmdLine string
var showBuried bool
var showStd bool
var showEnv bool
var quietMode bool
var statusLimit int

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get status of commands",
	Long: `You can find the status of commands you've previously added using
"wr add" or "wr setup" by running this command.

Specify one of the flags -f, -l  or -i to choose which commands you want the
status of. If none are supplied, it gives you an overview of all your currently
incomplete commands.

The file to provide -f is in the format cmd\tcwd\tmounts, with the last 2
columns optional.

In -f and -l mode you must provide the cwd the commands were set to run in, if
CwdMatters (and must NOT be provided otherwise). Likewise provide the mounts
JSON that was used when the command was added, if any. You can do this by using
the -c and --mounts options, or in -f mode your file can specify the cwd and
mounts, in case it's different for each command.

By default, commands with the same state, reason for failure and exitcode are
grouped together and only a random 1 of them is displayed (and you are told how
many were skipped). --limit changes how many commands in each of these groups
are displayed. A limit of 0 turns off grouping and shows all your desired
commands individually, but you could hit a timeout if retrieving the details of
very many (tens of thousands+) commands.`,
	Run: func(cmd *cobra.Command, args []string) {
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
		if set > 1 {
			die("-f, -i and -l are mutually exclusive; only specify one of them")
		}
		var cmdState jobqueue.JobState
		if showBuried {
			cmdState = jobqueue.JobStateBuried
		}
		timeout := time.Duration(timeoutint) * time.Second

		var defaultMounts jobqueue.MountConfigs
		if cmdMounts != "" {
			defaultMounts = mountParseJSON(cmdMounts)
		}

		jq, err := jobqueue.Connect(addr, "cmds", timeout)
		if err != nil {
			die("%s", err)
		}
		defer jq.Disconnect()

		var jobs []*jobqueue.Job
		showextra := true
		switch {
		case set == 0:
			// get incomplete jobs
			jobs, err = jq.GetIncomplete(statusLimit, cmdState, showStd, showEnv)
		case cmdIDStatus != "":
			// get all jobs with this identifier (repgroup)
			jobs, err = jq.GetByRepGroup(cmdIDStatus, statusLimit, cmdState, showStd, showEnv)
		case cmdFileStatus != "":
			// get jobs that have the supplied commands. We support a cmd\tcwd
			// format file
			var reader io.Reader
			if cmdFileStatus == "-" {
				reader = os.Stdin
			} else {
				reader, err = os.Open(cmdFileStatus)
				if err != nil {
					die("could not open file '%s': %s", cmdFileStatus, err)
				}
				defer reader.(*os.File).Close()
			}
			scanner := bufio.NewScanner(reader)
			var jes []*jobqueue.JobEssence
			desired := 0
			for scanner.Scan() {
				cols := strings.Split(scanner.Text(), "\t")
				colsn := len(cols)
				if colsn < 1 || cols[0] == "" {
					continue
				}
				var cwd string
				if colsn < 2 || cols[1] == "" {
					cwd = cmdCwd
				} else {
					cwd = cols[1]
				}

				var mounts jobqueue.MountConfigs
				if colsn < 3 || cols[2] == "" {
					mounts = defaultMounts
				} else {
					mounts = mountParseJSON(cols[2])
				}

				jes = append(jes, &jobqueue.JobEssence{Cmd: cols[0], Cwd: cwd, MountConfigs: mounts})
				desired++
			}
			jobs, err = jq.GetByEssences(jes)
			if len(jobs) < desired {
				warn("%d/%d cmds were not found", desired-len(jobs), desired)
			}
			showextra = false
		default:
			// get job that has the supplied command
			var job *jobqueue.Job
			job, err = jq.GetByEssence(&jobqueue.JobEssence{Cmd: cmdLine, Cwd: cmdCwd, MountConfigs: defaultMounts}, showStd, showEnv)
			if job != nil {
				jobs = append(jobs, job)
			}
		}

		if err != nil {
			die("failed to get jobs corresponding to your settings: %s", err)
		}

		if quietMode {
			var d, re, b, ru, l, c int
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
				}
			}
			fmt.Printf("complete: %d\nrunning: %d\nready: %d\nlost contact: %d\ndelayed: %d\nburied: %d\n", c, ru, re, l, d, b)
		} else {
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
				var behaviours string
				if len(job.Behaviours) > 0 {
					behaviours = fmt.Sprintf("Behaviours: %s\n", job.Behaviours)
				}
				fmt.Printf("\n# %s\nCwd: %s\n%s%s%sId: %s; Requirements group: %s; Priority: %d; Attempts: %d\nExpected requirements: { memory: %dMB; time: %s; cpus: %d disk: %dGB }\n", job.Cmd, cwd, mounts, homeChanged, behaviours, job.RepGroup, job.ReqGroup, job.Priority, job.Attempts, job.Requirements.RAM, job.Requirements.Time, job.Requirements.Cores, job.Requirements.Disk)

				switch job.State {
				case jobqueue.JobStateDelayed:
					fmt.Printf("Status: delayed following a temporary problem, will become ready soon (attempted at %s)\n", job.StartTime.Format(shortTimeFormat))
				case jobqueue.JobStateReady:
					fmt.Println("Status: ready to be picked up by a `wr runner`")
				case jobqueue.JobStateBuried:
					fmt.Printf("Status: buried - you need to fix the problem and then `wr kick` (attempted at %s)\n", job.StartTime.Format(shortTimeFormat))
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
					fmt.Printf("%s: { Exit code: %d; Peak memory: %dMB; Wall time: %s; CPU time: %s }\nHost: %s (IP: %s%s); Pid: %d\n", prefix, job.Exitcode, job.PeakRAM, job.WallTime(), job.CPUtime, job.Host, job.HostIP, hostID, job.Pid)
					if showextra && showStd && job.Exitcode != 0 {
						stdout, err := job.StdOut()
						if err != nil {
							warn("problem reading the cmd's STDOUT: %s", err)
						} else if stdout != "" {
							fmt.Printf("StdOut:\n%s\n", stdout)
						} else {
							fmt.Printf("StdOut: [none]\n")
						}
						stderr, err := job.StdErr()
						if err != nil {
							warn("problem reading the cmd's STDERR: %s", err)
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
				} else if showextra && showStd {
					// it's possible for jobs that got buried before they even
					// ran to have details of the bury in their stderr
					stderr, err := job.StdErr()
					if err == nil && stderr != "" {
						fmt.Printf("Details: %s\n", stderr)
					}
				}

				if showextra && showEnv {
					env, err := job.Env()
					if err != nil {
						warn("problem reading the cmd's Env: %s", err)
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
		}

		fmt.Printf("\n")
	},
}

func init() {
	RootCmd.AddCommand(statusCmd)

	// flags specific to this sub-command
	statusCmd.Flags().StringVarP(&cmdFileStatus, "file", "f", "", "file containing commands you want the status of; - means read from STDIN")
	statusCmd.Flags().StringVarP(&cmdIDStatus, "identifier", "i", "", "identifier of the commands you want the status of")
	statusCmd.Flags().StringVarP(&cmdLine, "cmdline", "l", "", "a command line you want the status of")
	statusCmd.Flags().StringVarP(&cmdCwd, "cwd", "c", "", "working dir that the command(s) specified by -l or -f were set to run in")
	statusCmd.Flags().StringVar(&cmdMounts, "mounts", "", "mounts that the command(s) specified by -l or -f were set to use")
	statusCmd.Flags().BoolVarP(&showBuried, "buried", "b", false, "in default or -i mode only, only show the status of buried commands")
	statusCmd.Flags().BoolVarP(&showStd, "std", "s", false, "except in -f mode, also show the most recent STDOUT and STDERR of incomplete commands")
	statusCmd.Flags().BoolVarP(&showEnv, "env", "e", false, "except in -f mode, also show the environment variables the command(s) ran with")
	statusCmd.Flags().BoolVarP(&quietMode, "quiet", "q", false, "minimal verbosity: just display status counts")
	statusCmd.Flags().IntVar(&statusLimit, "limit", 1, "number of commands that share the same properties to display; 0 displays all")

	statusCmd.Flags().IntVar(&timeoutint, "timeout", 30, "how long (seconds) to wait to get a reply from 'wr manager'")
}
