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

// Package cmd is the cobra file that enables subcommands and handles
// command-line args
package cmd

import (
	"fmt"
	"github.com/sb10/vrpipe/internal"
	"github.com/spf13/cobra"
	"os"
)

// these variables are accessible by all subcommands
var deployment string
var config internal.Config

// these are shared by some of the subcommands
var addr string
var timeoutint int

// This represents the base command when called without any subcommands
var RootCmd = &cobra.Command{
	Use:   "vrpipe",
	Short: "VRPipe is a software pipeline management system.",
	Long: `VRPipe is a software pipeline management system.

You use it to run the same sequence of commands (a "pipeline") on many different
input files (which comprise a "datasource").

Initially, you start the management system, which maintains a queue of the
commands you want to run:
$ vrpipe manager start

Then you either directly add commands you want to run to the queue:
$ vrpipe add

Or you define a pipeline that works out the commands for you:
Create a pipeline with:                           $ vrpipe create
Define a datasource with:                         $ vrpipe datasource
Set up an instance of pipeline + datasource with: $ vrpipe setup

At this point your commands should be running, and you can monitor their
progress with:
$ vrpipe status

Finally, you can find your output files with:
$ vrpipe outputs`,
}

// Execute adds all child commands to the root command and sets flags
// appropriately. This is called by main.main(). It only needs to happen once to
// the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
}

func init() {
	// global flags
	RootCmd.PersistentFlags().StringVar(&deployment, "deployment", internal.DefaultDeployment(), "use production or development config")

	cobra.OnInitialize(initConfig)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	config = internal.ConfigLoad(deployment, false)
	addr = config.Manager_host + ":" + config.Manager_port
}

// info is a convenience to print a msg to STDOUT
func info(msg string, a ...interface{}) {
	fmt.Fprintf(os.Stdout, "info: %s\n", fmt.Sprintf(msg, a...))
}

// warn is a convenience to print a msg to STDERR
func warn(msg string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "warning: %s\n", fmt.Sprintf(msg, a...))
}

// fatal is a convenience to print an error to STDERR and exit indicating error
func fatal(msg string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: %s\n", fmt.Sprintf(msg, a...))
	os.Exit(1)
}
