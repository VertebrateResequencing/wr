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
    "log"
	"github.com/spf13/cobra"
    "github.com/sb10/vrpipe/jobqueue"
)

// setupCmd represents the setup command
var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Set up a particular instance of a pipeline and run it",
	Long: `Set up a particular instance of a pipeline and run it.

You use this command to define what pipeline to run, what datasource to use
(what the pipeline's input data will be), and what options to pass to the steps
of the pipeline and the pipeline itself.

Once defined the pipeline will immediately start running.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Setup will try to connect to beanstalk...\n")
        
        jobqueue, err := jobqueue.Connect(config.Beanstalk, jobqueue.TubeDES, true)
        if err != nil {
            log.Fatal(err)
        }
        
        job, err := jobqueue.Add("test job 1", 30)
        if err != nil {
            log.Fatal(err)
        }
        fmt.Printf("Added job %d\n", job.ID)
        
        job, err = jobqueue.Add("test job 2", 40)
        if err != nil {
            log.Fatal(err)
        }
        fmt.Printf("Added job %d\n", job.ID)
        
        jobqueue.Disconnect()
        fmt.Printf("All done.\n")
	},
}

func init() {
	RootCmd.AddCommand(setupCmd)

	// flags specific to this sub-command
	// setupCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
