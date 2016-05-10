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

package cmd

import (
	"bufio"
	"fmt"
	"github.com/sb10/vrpipe/jobqueue"
	"github.com/spf13/cobra"
	"io"
	"os"
	"strings"
	"time"
)

// options for this cmd
var cmdFileStatus string
var cmdIdStatus string
var cmdLine string
var showStd bool
var showEnv bool

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get status of commands",
	Long: `You can find the status of commands you've previously added using
"vrpipe add" or "vrpipe setup" by running this command.

Specify one of the flags -f, -l  or -i to choose which commands you want the
status of. If none are supplied, it gives you an overview of all your currently
incomplete commands.

The file to provide -f is in the same format as that taken by "vrpipe add"
(though only the first 2 columns are considered).

In -f and -l mode you must provide the cwd the commands were set to run in. You
can do this by using the -c option, or in -f mode your file can contain the
second column holding the cwd, in case it's different for each command. If not
supplied at all, cwd will default to your current directory, but you won't get
any status if you're not in the same directory you were in when you first added
the commands, or if you added them with a different cwd.`,
	Run: func(cmd *cobra.Command, args []string) {
		set := 0
		if cmdFileStatus != "" {
			set++
		}
		if cmdIdStatus != "" {
			set++
		}
		if cmdLine != "" {
			set++
		}
		if set > 1 {
			fatal("-f, -i and -c are mutually exclusive; only specify one of them")
		}
		if cmdCwd == "" {
			pwd, err := os.Getwd()
			if err != nil {
				fatal("%s", err)
			}
			cmdCwd = pwd
		}
		timeout := time.Duration(timeoutint) * time.Second

		jq, err := jobqueue.Connect(addr, "cmds", timeout)
		if err != nil {
			fatal("%s", err)
		}
		defer jq.Disconnect()

		var jobs []*jobqueue.Job
		showextra := false
		switch {
		case set == 0:
			// get unfinished jobs
			//*** jobs, err = jq.GetUnfinished()
		case cmdIdStatus != "":
			// get all jobs with this id
			//*** jobs, err = jq.GetByID(cmdIdStatus)
		case cmdFileStatus != "":
			// get jobs that have the supplied commands. We support the same
			// format of file that "vrpipe add" takes, but only care about the
			// first column
			var reader io.Reader
			if cmdFileStatus == "-" {
				reader = os.Stdin
			} else {
				reader, err = os.Open(cmdFileStatus)
				if err != nil {
					fatal("could not open file '%s': %s", cmdFileStatus, err)
				}
				defer reader.(*os.File).Close()
			}
			scanner := bufio.NewScanner(reader)
			var cmds []string
			for scanner.Scan() {
				cols := strings.Split(scanner.Text(), "\t")
				colsn := len(cols)
				if colsn < 1 || cols[0] == "" {
					continue
				}
				cmds = append(cmds, cols[0])
			}
			// jobs, err = jq.GetByCmds(cmds)
		default:
			// get job that has the supplied command
			var job *jobqueue.Job
			job, err = jq.GetByCmd(cmdLine, cmdCwd, showStd, showEnv)
			jobs = append(jobs, job)
			showextra = true
		}

		if err != nil {
			fatal("failed to get jobs corresponding to your settings: %s", err)
		}

		// print out status information for each job
		for _, job := range jobs {
			fmt.Printf("\n# %s\nCwd: %s\nId: %s; Requirements Group: %s; Priority: %d; Attempts: %d\nExpected Requirements: { memory: %dMB; time: %s; cpus: %d }\n", job.Cmd, job.Cwd, job.RepGroup, job.ReqGroup, job.Priority, job.Attempts, job.Memory, job.Time, job.CPUs)

			switch job.State {
			case "delayed":
				fmt.Printf("Status: %s following a temporary problem, will become ready soon\n", job.State)
			case "ready":
				fmt.Printf("Status: %s to be picked up by a `vrpipe runner`\n", job.State)
			case "buried":
				fmt.Printf("Status: %s - you need to fix the problem and then `vrpipe kick`\n", job.State)
			case "running", "complete":
				fmt.Printf("Status: %s\n", job.State)
			}

			if job.Exited {
				prefix := "Stats"
				if job.State != "complete" {
					prefix = "Stats of previous attempt"
				}
				fmt.Printf("%s: { Exit code: %d; Peak memory: %dMB; Wall time: %s; CPU time: %s }\nHost: %s; PID: %d\n", prefix, job.Exitcode, job.Peakmem, job.Walltime, job.CPUtime, job.Host, job.Pid)
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
			} else if job.State == "running" {
				fmt.Printf("Stats: { Wall time: %s }\nHost: %s; PID: %d\n", job.Walltime, job.Host, job.Pid)
				//*** we should be able to peek at STDOUT & STDERR, and see
				// Peak memory during a run... but is that possible/ too
				// expensive? Maybe we could communicate directly with the
				// runner?...
			}

			if showextra && showEnv {
				env, err := job.Env()
				if err != nil {
					warn("problem reading the cmd's ENV: %s", err)
				} else {
					fmt.Printf("ENV: %s\n", env)
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
	statusCmd.Flags().StringVarP(&cmdIdStatus, "identifier", "i", "", "identifier of the commands you want the status of")
	statusCmd.Flags().StringVarP(&cmdLine, "cmdline", "l", "", "a command line you want the status of")
	statusCmd.Flags().StringVarP(&cmdCwd, "cwd", "c", "", "working dir that the command(s) specified by -l or -f were set to run in")
	statusCmd.Flags().BoolVarP(&showStd, "std", "s", false, "in -l mode only, also show the most recent STDOUT and STDERR of the command")
	statusCmd.Flags().BoolVarP(&showEnv, "env", "e", false, "in -l mode only, also show the environment variables the command ran with")

	statusCmd.Flags().IntVar(&timeoutint, "timeout", 30, "how long (seconds) to wait to get a reply from 'vrpipe manager'")
}
