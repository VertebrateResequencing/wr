// Copyright © 2016 Genome Research Limited
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
	// "github.com/VertebrateResequencing/wr/cloud"
	"github.com/kardianos/osext"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"runtime"
	"time"
)

// options for this cmd
var cloudType string

// cloudCmd represents the cloud command
var cloudCmd = &cobra.Command{
	Use:   "cloud",
	Short: "Cloud infrastructure creation",
	Long: `Cloud infrastructure creation.

To run wr in the cloud, you need to create at least 1 cloud server with certain
ports open so that you can start running "wr manager" on it. From there the
manager will run your commands on additional servers spawned as demand dictates.

The cloud sub-commands make it easy to get started, interact with that remote
manager, and clean up afterwards.`,
}

// deploy sub-command brings up a "head" node in the cloud and starts a proxy
// daemon to interact with the manager we spawn there
var cloudDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy a manager to a cloud server",
	Long: `Start up 'wr manager' on a cloud server.

Deploy creates all the necessary cloud resources (networks, keys, security
profiles etc.) and starts a cloud server, on which 'wr manager' is run.

Deploy then daemonizes in to the background to run a proxy server that lets you
use the normal wr command line utilities such as 'wr add' and view the wr
website locally, even though the manager is actually running remotely.`,
	Run: func(cmd *cobra.Command, args []string) {
		// first we need our working directory to exist
		createWorkingDir()

		// check to see if the manager is already running (regardless of the
		// state of the pid file); we can't proxy if a manager is already up
		jq := connect(10 * time.Millisecond)
		if jq != nil {
			sstats, err := jq.ServerStats()
			var pid int
			if err == nil {
				pid = sstats.ServerInfo.PID
			}
			die("wr manager on port %s is already running (pid %d); please stop it before trying again.", config.ManagerPort, pid)
		}

		// we will spawn wr on the remote server we will create, which means we
		// need to know the path to ourselves in case we're not in the user's
		// $PATH
		exe, err := osext.Executable()
		if err != nil {
			die("could not get the path to wr: %s", err)
		}
		info("exe: %s", exe)

		// get all necessary cloud resources in place, along with 1 server that
		// will be our "head node"

		serverID := "temp"

		// now daemonize to bring up a proxy server so user can easily interact
		// with the remote manager
		child, context := daemonize(config.DeployPidFile, config.ManagerUmask)
		if child != nil {
			// parent; wait a while for our child to bring up the proxy server
			// before exiting
			jq := connect(10 * time.Second)
			if jq == nil {
				die("could not talk to wr manager on server %s after 10s", serverID)
			}
			sstats, err := jq.ServerStats()
			if err != nil {
				die("wr manager on server %s started but doesn't seem to be functional: %s", serverID, err)
			}

			info("wr manager remotely started on %s", sAddr(sstats.ServerInfo))
			info("wr's web interface can be reached locally at http://localhost:%s", sstats.ServerInfo.WebPort)
		} else {
			// daemonized child, that will run until signalled to stop
			defer context.Release()
			startProxy()
		}
	},
}

// teardown sub-command deletes all cloud resources we created and then stops
// the daemon by sending it a term signal
var cloudTearDownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Delete all cloud resources that deploy created",
	Long: `Immediately stop the remote workflow manager, saving its state.

Deletes all cloud resources that wr created (servers, networks, keys, security
profiles etc.). (Except for any files that were saved to persistent cloud
storage.)

Note that any runners that are currently running will die, along with any
commands they were running. It is more graceful to issue 'wr manager drain'
first, and regularly rerun drain until it reports the manager is stopped, and
only then request a teardown.`,
	Run: func(cmd *cobra.Command, args []string) {
		// try and stop the remote manager; doing this results in a graceful
		// saving of the db locally
		jq := connect(100 * time.Millisecond)
		if jq != nil {
			ok := jq.ShutdownServer()
			if ok {
				info("the remote wr manager was shut down")
			} else {
				warn("there was an error trying to shut down the remote wr manager: %s", err)
			}
		} else {
			warn("the remote wr manager could not be connected to in order to shut it down; hopefully it was all ready stopped")
		}

		// teardown cloud resources we created

		// unlike manager, this is hopefully pretty simple and straightforward
		// to kill, relying on the pid file?...
		pid, err := daemon.ReadPidFile(config.DeployPidFile)
		if err == nil {
			if stopdaemon(pid, "pid file "+config.DeployPidFile, "cloud deploy") {
				info("wr cloud deploy proxy daemon was stopped successfully")
			} else {
				die("could not stop the wr cloud deploy proxy daemon running with pid %d", pid)
			}
		} else {
			die("could not read the pid from pid file %s; was the deploy proxy actually running? [%s]", config.DeployPidFile, err)
		}
	},
}

func init() {
	RootCmd.AddCommand(cloudCmd)
	cloudCmd.AddCommand(cloudDeployCmd)
	cloudCmd.AddCommand(cloudTearDownCmd)

	// flags specific to these sub-commands
	cloudDeployCmd.Flags().StringVarP(&cloudType, "type", "t", "openstack", "['openstack'] cloud provider")
	cloudTearDownCmd.Flags().StringVarP(&cloudType, "type", "t", "openstack", "['openstack'] cloud provider")
}

func startProxy() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	// start a proxy server that goes from localhost ports manager_port and
	// manager_web to the head node we spawned
	//***
}
