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
	"log"
	"runtime"
)

// managerCmd represents the manager command
var managerCmd = &cobra.Command{
	Use:   "manager",
	Short: "Start pipeline management",
	Long: `Start the pipeline management system.

The vrpipe manager works in the background, doing all the work of getting your
jobs run successfully.

You'll need to start this running before you can achieve anything useful with
the other vrpipe commands. If the background process that is spawned when you
run this dies, your pipelines will become stalled until you rerun this
command.`,
	Run: func(cmd *cobra.Command, args []string) {
		runtime.GOMAXPROCS(runtime.NumCPU())
		server, err := jobqueue.Serve(config.Manager_port)
		if err != nil {
			log.Fatal(err)
		}
		err = server.Block()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	RootCmd.AddCommand(managerCmd)

	// flags specific to this sub-command
	// managerCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
