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
	"github.com/sb10/vrpipe/jobqueue"
	"github.com/spf13/cobra"
	"time"
)

// options for this cmd
var queuename string
var schedgrp string
var reserveint int

// runnerCmd represents the runner command
var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Run queued commands",
	Long: `A runner runs commands that were queued by the add or setup commands.

You won't normally run this yourself directly - "vrpipe manager" spawns these as
needed.`,
	Run: func(cmd *cobra.Command, args []string) {
		if queuename == "" {
			fatal("--queue is required")
		}

		// the server receive timeout must be greater than the time we'll wait
		// to Reserve()
		if timeoutint < (reserveint + 5) {
			timeoutint = reserveint + 5
		}
		timeout := time.Duration(timeoutint) * time.Second
		rtimeout := time.Duration(reserveint) * time.Second

		jq, err := jobqueue.Connect(addr, queuename, timeout)
		if err != nil {
			fatal("%s", err)
		}
		defer jq.Disconnect()

		// loop, reserving and running commands from the queue, until there
		// aren't any more commands in the queue
		numrun := 0
		for {
			var job *jobqueue.Job
			var err error
			if schedgrp == "" {
				job, err = jq.Reserve(rtimeout)
			} else {
				job, err = jq.ReserveScheduled(rtimeout, schedgrp)
			}

			if err != nil {
				fatal("%s", err)
			}
			if job == nil {
				break
			}

			cmd := job.Cmd
			info("would run cmd [%s]", cmd)

			numrun++
		}

		info("vrpipe runner exiting, having run %d commands, because there are no more commands in queue '%s' in scheduler group '%s'", numrun, queuename, schedgrp)
	},
}

func init() {
	RootCmd.AddCommand(runnerCmd)

	// flags specific to this sub-command
	runnerCmd.Flags().StringVarP(&queuename, "queue", "q", "cmds", "specify the queue to pull commands from")
	runnerCmd.Flags().StringVarP(&schedgrp, "scheduler_group", "s", "", "specify the scheduler group to limit which commands can be acted on")
	runnerCmd.Flags().IntVar(&timeoutint, "timeout", 30, "how long (seconds) to wait to get a reply from 'vrpipe manager'")
	runnerCmd.Flags().IntVarP(&reserveint, "reserve_timeout", "r", 25, "how long (seconds) to wait for there to be a command in the queue, before exiting")
}
