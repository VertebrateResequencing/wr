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
	"fmt"
	"github.com/pivotal-golang/bytefmt"
	"github.com/sb10/vrpipe/jobqueue"
	"github.com/spf13/cobra"
	"time"
)

// options for this cmd
var cmdTorun string
var cmdCwd string
var reqGroup string
var cmdId string
var cmdTime string
var cmdMem string
var cmdOvr int
var cmdPri int

// addCmd represents the add command
var addCmd = &cobra.Command{
	Use:   "add",
	Short: "Add commands to the queue",
	Long: `Manually add commands you want run to the queue.

By providing the --memory and --time hints, vrpipe manager can do a better job
of spawning runners to handle these commands. The manager learns how much memory
and time commands in the same requirements_group actually used in the past, and
will use its own values unless you set an override. For this learning to work
well, you should add commands in batches that you think will have similar memory
and time requirements and give them a consistent and meaningful
requirements_group name that you will use again in the future.

For example, if you want to run an executable called "exop", and you know that
the memory and time requirements of exop vary with the size of its input file,
you might batch your commands so that all the input files in one batch have
sizes in a certain range, and then provide a requirements_group that describes
this, eg. "exop.1-2G" for inputs in the 1 to 2 GB range.

(Don't name your requirements_group after the expected requirements themselves,
such as "5GB.1hr", because then the manager can't learn about your commands - it
is only learning about how good your estimates are! The name of your executable
should almost always be part of the requirements_group name.)

The identifier option is an arbitrary name you can give your commands so you can
query their status later. If you split your commands into multiple batches with
different requirements_groups, you can give all the different batches the same
identifier, so you can track them in one go.`,
	Run: func(cmd *cobra.Command, args []string) {
		// check the command line options
		if reqGroup == "" {
			fatal("--requirements_group is required")
		}
		var cmdMB int
		if cmdMem == "" {
			cmdMB = 0
		} else {
			mb, err := bytefmt.ToMegabytes(cmdMem)
			if err != nil {
				fatal("--memory was not specified correctly: %s", err)
			}
			cmdMB = int(mb)
			info("--memory is %s", cmdMB)
		}
		var cmdDuration time.Duration
		if cmdTime == "" {
			cmdDuration = 0 * time.Second
		} else {
			cmdDuration, err := time.ParseDuration(cmdTime)
			if err != nil {
				fatal("--time was not specified correctly: %s", err)
			}
			info("--time is %s", cmdDuration)
		}
		if cmdOvr < 0 || cmdOvr > 2 {
			fatal("--override must be in the range 0..2")
		}
		if cmdPri < 0 || cmdPri > 255 {
			fatal("--priority must be in the range 0..255")
		}

		timeout := time.Duration(timeoutint) * time.Second

		jq, err := jobqueue.Connect(addr, "cmds", timeout)
		if err != nil {
			fatal("%s", err)
		}
		defer jq.Disconnect()

		var jobs []*jobqueue.Job
		for i := 0; i < 10; i++ {
			jobs = append(jobs, jobqueue.NewJob(fmt.Sprintf("test cmd %d", i), "/fake/cwd", reqGroup, cmdMB, cmdDuration, 1, uint8(cmdOvr), uint8(cmdPri)))
		}

		inserts, dups, err := jq.Add(jobs)
		if err != nil {
			fatal("%s", err)
		}

		info("Added %d new commands to queue %s (%d were duplicates)", inserts, "cmds", dups)
	},
}

func init() {
	RootCmd.AddCommand(addCmd)

	// flags specific to this sub-command
	addCmd.Flags().StringVarP(&cmdCwd, "cwd", "c", "", "an arbitrary identifier that identifies the kind of commands you are adding, such that future commands you add with this same requirements_group are likely to have similar memory and time requirements")
	addCmd.Flags().StringVarP(&reqGroup, "requirements_group", "r", "", "an arbitrary string that identifies the kind of commands you are adding, such that future commands you add with this same requirements_group are likely to have similar memory and time requirements")
	addCmd.Flags().StringVarP(&cmdMem, "memory", "m", "1G", "your estimate for the peak memory the added commands will each use when run [specify units such as M for Megabytes or G for Gigabytes]")
	addCmd.Flags().StringVarP(&cmdTime, "time", "t", "1h", "your estimate for how time the added commands will each take to run [specify units such as m for minutes or h for hours]")
	addCmd.Flags().IntVarP(&cmdOvr, "override", "o", 0, "0: do not override vrpipe's learned values for memory and time for previously run commands in requirements_group; 1: override if yours are higher; 2: always override")
	addCmd.Flags().IntVarP(&cmdPri, "priority", "p", 0, "[0-255] give all the added commands this priority; commands with higher priorities will get run sooner than others")

	addCmd.Flags().IntVarP(&timeoutint, "timeout", "t", 30, "how long (seconds) to wait to get a reply from 'vrpipe manager'")
}
