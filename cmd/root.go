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
	"os"
    "github.com/sb10/vrpipe/internal"
	"github.com/spf13/cobra"
)

// these variables are accessible by all subcommands
var deployment string
var config internal.Config

// This represents the base command when called without any subcommands
var RootCmd = &cobra.Command{
	Use:   "vrpipe",
	Short: "VRPipe is a software pipeline management system.",
	Long: `VRPipe is a software pipeline management system.
You use it to run the same sequence of commands (a "pipeline") on many different
input files (which comprise a "datasource"). To get started you'd:

Create a pipeline with:   vrpipe create
Define a datasource with: vrpipe datasource
Define a setup with:      vrpipe setup
Monitor progress with:    vrpipe status`,
}

// Execute adds all child commands to the root command sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
}

func init() {
    // global flags
	RootCmd.PersistentFlags().StringVar(&deployment, "deployment", "", "Use the production or development configuration (defaults to development in the vrpipe git repository directory, otherwise is taken from $VRPIPE_DEPLOYMENT or defaults to production)")
    
    cobra.OnInitialize(initConfig)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	config = internal.ConfigLoad(deployment)
}
