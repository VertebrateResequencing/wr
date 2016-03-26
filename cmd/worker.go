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
	"github.com/sb10/vrpipe/jobqueue"
	"github.com/spf13/cobra"
	"log"
	"time"
)

var queuename string

// workerCmd represents the worker command
var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run a queued command",
	Long: `A worker runs commands that were queued by the setup command.
You won't normally run this yourself directly - "vrpipe controller" runs this as
needed.`,
	Run: func(cmd *cobra.Command, args []string) {
		jobqueue, err := jobqueue.Connect(config.Beanstalk, "vrpipe."+queuename, false)
		if err != nil {
			log.Fatal(err)
		}

		for {
			job, err := jobqueue.Reserve(5 * time.Second)
			if err != nil {
				log.Fatal(err)
			}
			if job == nil {
				break
			}
			stats, err := job.Stats()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("stats: %s; time left: %d\n", stats.State, stats.TimeLeft)
			stats2, err := jobqueue.Stats()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("ready: %d; reserved: %d\n", stats2.Ready, stats2.Reserved)
			err = job.Delete()
			if err != nil {
				log.Fatal(err)
			}
		}

		stats, err := jobqueue.DaemonStats()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("producers: %d, workers: %d, pid: %d, hostname: %s\n", stats.Producers, stats.Workers, stats.Pid, stats.Hostname)

		jobqueue.Disconnect()
	},
}

func init() {
	RootCmd.AddCommand(workerCmd)

	// flags specific to this sub-command
	workerCmd.Flags().StringVar(&queuename, "queue", "des", "Specify the queue to pull jobs from [des|cmd]")
}
