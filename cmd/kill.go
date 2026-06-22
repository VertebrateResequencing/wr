/*******************************************************************************
 * Copyright (c) 2018-2019, 2021, 2024 Genome Research Ltd.
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
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/spf13/cobra"
)

// options for this cmd
var (
	confirmDead bool
	cmdAge      string
)

// killCmd represents the kill command
var killCmd = &cobra.Command{
	Use:   "kill",
	Short: "Kill running commands",
	Long: `You can kill commands you've previously added with "wr add" that
are currently running using this command.

After killing commands, there will be a delay before the commands "realise" they
have been killed and actually stop running. At that point they will become
buried and you can "wr remove" them if desired.

Specify one of the flags -f, -l, -i or -a to choose which commands you want to
remove. Amongst those, only running jobs will be affected. If the --confirmdead
option is specified, only "lost contact" jobs will be affected.

-i is the report group (-i) you supplied to "wr add" when you added the job(s)
you want to now kill. Combining with -z lets you kill jobs in multiple report
groups, assuming you have arranged that related groups share some substring.
Alternatively -y lets you specify -i as the internal job id reported during
"wr status".

The file to provide -f is in the format taken by "wr add".

In -f and -l mode you must provide the cwd the commands were set to run in, if
CwdMatters (and must NOT be provided otherwise). Likewise provide the mounts
options that was used when the command was added, if any. You can do this by
using the -c and --mounts/--mounts_json options in -l mode, or by providing the
same file you gave to "wr add" in -f mode.`,
	Run: func(cmd *cobra.Command, args []string) {
		set := countGetJobArgs()
		if set > 1 {
			die("-f, -i, -l and -a are mutually exclusive; only specify one of them")
		}
		if set == 0 {
			die("1 of -f, -i, -l or -a is required")
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

		jstate := jobqueue.JobStateRunning
		if confirmDead {
			jstate = jobqueue.JobStateLost
		}
		jobs := getJobs(jq, jstate, cmdAll, 0, false, false)

		if len(jobs) == 0 {
			die("No matching jobs found")
		}

		var age time.Duration
		if cmdAge != "" {
			age, err = time.ParseDuration(cmdAge)
			if err != nil {
				die("--age was not specified correctly: %s", err)
			}
		}

		if age != 0 {
			var oldJobs []*jobqueue.Job
			for _, job := range jobs {
				var thisAge time.Duration
				if confirmDead {
					thisAge = time.Since(job.EndTime)
				} else {
					thisAge = job.WallTime()
				}

				if thisAge >= age {
					oldJobs = append(oldJobs, job)
				}
			}

			jobs = oldJobs
		}

		if len(jobs) == 0 {
			die("No matching jobs were older than %s", cmdAge)
		}

		jes := jobsToJobEssenses(jobs)
		killed, err := jq.Kill(jes)
		if err != nil {
			die("failed to kill desired jobs: %s", err)
		}
		info("Initiated the termination of %d running commands (out of %d eligible)", killed, len(jobs))
	},
}

func init() {
	RootCmd.AddCommand(killCmd)

	// flags specific to this sub-command
	killCmd.Flags().BoolVarP(&cmdAll, "all", "a", false, "kill all running jobs")
	killCmd.Flags().StringVarP(&cmdFileStatus, "file", "f", "", "file containing commands you want to kill; - means read from STDIN")
	killCmd.Flags().StringVarP(&cmdIDStatus, "identifier", "i", "", "identifier of the commands you want to kill")
	killCmd.Flags().BoolVarP(&cmdIDIsSubStr, "search", "z", false, "treat -i as a substring to match against all report groups")
	killCmd.Flags().BoolVarP(&cmdIDIsInternal, "internal", "y", false, "treat -i as an internal job id")
	killCmd.Flags().StringVarP(&cmdLine, "cmdline", "l", "", "a command line you want to kill")
	killCmd.Flags().StringVarP(&cmdCwd, "cwd", "c", "", "working dir that the command(s) specified by -l or -f were set to run in")
	killCmd.Flags().StringVarP(&mountJSON, "mount_json", "j", "", "mounts that the command(s) specified by -l or -f were set to use (JSON format)")
	killCmd.Flags().StringVar(&mountSimple, "mounts", "", "mounts that the command(s) specified by -l or -f were set to use (simple format)")
	killCmd.Flags().BoolVar(&confirmDead, "confirmdead", false, "only confirm that lost contact jobs are dead")
	killCmd.Flags().StringVar(&cmdAge, "age", "", "only kill jobs that have been running longer than this (or in --confirmdead mode, that have been lost longer than this). [specify units such as m for minutes or h for hours]")

	killCmd.Flags().IntVar(&timeoutint, "timeout", 120, "how long (seconds) to wait to get a reply from 'wr manager'")
}
