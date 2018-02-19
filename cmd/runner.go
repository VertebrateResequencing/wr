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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/kardianos/osext"
	"github.com/spf13/cobra"
)

// options for this cmd
var schedgrp string
var reserveint int
var rserver string
var maxtime int

// runnerCmd represents the runner command
var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Run queued commands",
	Long: `A runner runs commands that were queued by the add or setup commands.

You won't normally run this yourself directly - "wr manager" spawns these as
needed.

A runner will pick up a queued command and run it. Once that cmd completes, the
runner will pick up another and so on. Once max_time has been used (or would be
used based on the expected time to complete of the next queued command), the
runner stops picking up new commands and exits instead; max_time does not cause
the runner to kill itself if the cmd it is running takes longer than max_time to
complete.`,
	Run: func(cmd *cobra.Command, args []string) {
		// the server receive timeout must be greater than the time we'll wait
		// to Reserve()
		if timeoutint < (reserveint + 5) {
			timeoutint = reserveint + 5
		}
		timeout := time.Duration(timeoutint) * time.Second
		rtimeout := time.Duration(reserveint) * time.Second

		jobqueue.AppName = "wr"

		jq, err := jobqueue.Connect(rserver, timeout)
		if err != nil {
			die("%s", err)
		}
		defer jq.Disconnect()

		// in case any job we execute has a Cmd that calls `wr add`, we will
		// override their environment to make that call work
		var envOverrides []string
		if rserver != "" {
			hostPort := strings.Split(rserver, ":")
			if len(hostPort) == 2 {
				envOverrides = append(envOverrides, "WR_MANAGERHOST="+hostPort[0])
				envOverrides = append(envOverrides, "WR_MANAGERPORT="+hostPort[1])
			}

			// add our own wr exe to the path in case its not there
			exe, err := osext.Executable()
			if err != nil {
				die("%s", err)
			}
			exePath := filepath.Dir(exe)
			envOverrides = append(envOverrides, "PATH="+os.Getenv("PATH")+":"+exePath)
		}

		// we'll stop the below loop before using up too much time
		var endTime time.Time
		if maxtime > 0 {
			endTime = time.Now().Add(time.Duration(maxtime) * time.Minute)
		} else {
			endTime = time.Now().AddDate(1, 0, 0) // default to allowing us a year to run
		}

		// loop, reserving and running commands from the queue, until there
		// aren't any more commands in the queue
		numrun := 0
		exitReason := fmt.Sprintf("there are no more commands in scheduler group '%s'", schedgrp)
		for {
			var job *jobqueue.Job
			var err error
			if schedgrp == "" {
				job, err = jq.Reserve(rtimeout)
			} else {
				job, err = jq.ReserveScheduled(rtimeout, schedgrp)
			}

			if err != nil {
				die("%s", err) //*** we want this in a central log so we can know if/why our runners are failing
			}
			if job == nil {
				break
			}

			// see if we have enough time left to run this
			if time.Now().Add(job.Requirements.Time).After(endTime) {
				jq.Release(job, nil, job.FailReason)
				exitReason = "we're about to hit our maximum time limit"
				break
			}

			// actually run the cmd
			if len(envOverrides) > 0 {
				job.EnvAddOverride(envOverrides)
			}
			err = jq.Execute(job, config.RunnerExecShell)
			if err != nil {
				warn("%s", err)
				if jqerr, ok := err.(jobqueue.Error); ok && jqerr.Err == jobqueue.FailReasonSignal {
					exitReason = "we received a signal to stop"
					break
				}
			} else {
				info("command [%s] ran OK (exit code %d)", job.Cmd, job.Exitcode)
			}

			numrun++
		}

		info("wr runner exiting, having run %d commands, because %s", numrun, exitReason)
	},
}

func init() {
	RootCmd.AddCommand(runnerCmd)

	// flags specific to this sub-command
	runnerCmd.Flags().StringVarP(&schedgrp, "scheduler_group", "s", "", "specify the scheduler group to limit which commands can be acted on")
	runnerCmd.Flags().IntVar(&timeoutint, "timeout", 30, "how long (seconds) to wait to get a reply from 'wr manager'")
	runnerCmd.Flags().IntVarP(&reserveint, "reserve_timeout", "r", 2, "how long (seconds) to wait for there to be a command in the queue, before exiting")
	runnerCmd.Flags().IntVarP(&maxtime, "max_time", "m", 0, "maximum time (minutes) to run for before exiting; 0 means unlimited")
	runnerCmd.Flags().StringVar(&rserver, "server", internal.DefaultServer(), "ip:port of wr manager")
}
