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
	"fmt"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/kardianos/osext"
	"github.com/spf13/cobra"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cloudBinDir is where we will upload executables to our created cloud server
const cloudBinDir = ".wr/bin"

// wrConfigFileName is the name of our main config file, which we need when we
// create on on our created cloud server
const wrConfigFileName = ".wr_config.yml"

// options for this cmd
var providerName string
var maxServers int
var serverKeepAlive int
var osPrefix string
var osUsername string
var forceTearDown bool

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
		if providerName == "" {
			die("--provider is required")
		}
		if osPrefix == "" {
			die("--os is required")
		}
		if osUsername == "" {
			die("--username is required")
		}

		// first we need our working directory to exist
		createWorkingDir()

		// check to see if the manager is already running (regardless of the
		// state of the pid file); we can't proxy if a manager is already up
		jq := connect(1 * time.Second)
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

		// get all necessary cloud resources in place
		mp, err := strconv.Atoi(config.ManagerPort)
		if err != nil {
			die("bad manager_port [%s]: %s", config.ManagerPort, err)
		}
		wp, err := strconv.Atoi(config.ManagerWeb)
		if err != nil {
			die("bad manager_web [%s]: %s", config.ManagerWeb, err)
		}
		provider, err := cloud.New(providerName, "wr-"+config.Deployment, filepath.Join(config.ManagerDir, "cloud_resources."+providerName))
		if err != nil {
			die("failed to connect to %s: %s", providerName, err)
		}
		serverPort := "22"
		info("please wait while %s resources are created...", providerName)
		err = provider.Deploy([]int{22, mp, wp})
		if err != nil {
			die("failed to create resources in %s: %s", providerName, err)
		}

		// get/spawn a "head node" server
		var server *cloud.Server
		usingExistingServer := false
		servers := provider.Servers()
		for _, thisServer := range servers {
			if thisServer.Alive() {
				usingExistingServer = true
				server = thisServer
				info("using existing %s server at %s", providerName, server.IP)
				break
			}
		}
		if server == nil {
			info("please wait while a server is spawned on %s...", providerName)
			flavor, err := provider.CheapestServerFlavor(2048, 1, 1)
			if err != nil {
				provider.TearDown()
				die("failed to launch a server in %s: %s", providerName, err)
			}
			server, err = provider.Spawn(osPrefix, osUsername, flavor.ID, 0*time.Second, true)
			if err != nil {
				provider.TearDown()
				die("failed to launch a server in %s: %s", providerName, err)
			}
		}

		// ssh to the server, copy over our exe, and start running wr manager
		// there
		info("please wait while I start 'wr manager' on the %s server at %s...", providerName, server.IP)
		bootstrapOnRemote(provider, server, exe, mp, wp, usingExistingServer)

		// rather than daemonize and use a go ssh forwarding library or
		// implement myself using the net package, since I couldn't get them
		// to work reliably and completely, we'll just spawn ssh -L in the
		// background and keep note of the pids so we can kill them during
		// teardown
		keyPath := filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".key")
		err = ioutil.WriteFile(keyPath, []byte(provider.PrivateKey()), 0600)
		if err != nil {
			provider.TearDown()
			die("failed to create key file %s: %s", keyPath, err)
		}
		err = startForwarding(server.IP, serverPort, osUsername, keyPath, mp, filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fm.pid"))
		if err != nil {
			provider.TearDown()
			die("failed to set up port forwarding to %s:%d: %s", server.IP, mp, err)
		}
		err = startForwarding(server.IP, serverPort, osUsername, keyPath, wp, filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fw.pid"))
		if err != nil {
			provider.TearDown()
			die("failed to set up port forwarding to %s:%d: %s", server.IP, wp, err)
		}

		// check that we can now connect to the remote manager
		jq = connect(10 * time.Second)
		if jq == nil {
			provider.TearDown()
			die("could not talk to wr manager on server at %s after 10s", server.IP)
		}
		sstats, err := jq.ServerStats()
		if err != nil {
			provider.TearDown()
			die("wr manager on server at %s started but doesn't seem to be functional: %s", server.IP, err)
		}

		info("wr manager remotely started on %s", sAddr(sstats.ServerInfo))
		info("wr's web interface can be reached locally at http://localhost:%s", sstats.ServerInfo.WebPort)
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
		if providerName == "" {
			die("--provider is required")
		}

		// first check if the ssh forwarding is up
		fmPidFile := filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fm.pid")
		fmPid, fmRunning := checkProcess(fmPidFile)

		// try and stop the remote manager; doing this results in a graceful
		// saving of the db locally
		noManagerMsg := "; deploy first or use --force option"
		noManagerForcedMsg := "; tearing down anyway!"
		if fmRunning {
			jq := connect(1 * time.Second)
			if jq != nil {
				ok := jq.ShutdownServer()
				if ok {
					info("the remote wr manager was shut down")
				} else {
					msg := "there was an error trying to shut down the remote wr manager"
					if forceTearDown {
						warn(msg + noManagerForcedMsg)
					} else {
						die(msg + noManagerMsg)
					}
				}
			} else {
				msg := "the remote wr manager could not be connected to in order to shut it down"
				if forceTearDown {
					warn(msg + noManagerForcedMsg)
				} else {
					die(msg + noManagerMsg)
				}
			}
		} else {
			if forceTearDown {
				warn("the deploy port forwarding is not running, so the remote manager could not be stopped" + noManagerForcedMsg)
			} else {
				die("the deploy port forwarding is not running, so can't safely teardown" + noManagerMsg)
			}
		}

		// teardown cloud resources we created
		provider, err := cloud.New(providerName, "wr-"+config.Deployment, filepath.Join(config.ManagerDir, "cloud_resources."+providerName))
		if err != nil {
			die("failed to connect to %s: %s", providerName, err)
		}
		err = provider.TearDown()
		if err != nil {
			die("failed to delete the cloud resources previously created: %s", err)
		}
		os.Remove(filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".key"))
		info("deleted all cloud resources previously created")

		// kill the ssh forwarders
		if fmRunning {
			err = killProcess(fmPid)
			if err == nil {
				os.Remove(fmPidFile)
			}
		}
		fwPidFile := filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fw.pid")
		if fwPid, fwRunning := checkProcess(fwPidFile); fwRunning {
			err = killProcess(fwPid)
			if err == nil {
				os.Remove(fwPidFile)
			}
		}
	},
}

func init() {
	RootCmd.AddCommand(cloudCmd)
	cloudCmd.AddCommand(cloudDeployCmd)
	cloudCmd.AddCommand(cloudTearDownCmd)

	// flags specific to these sub-commands
	cloudDeployCmd.Flags().StringVarP(&providerName, "provider", "p", "openstack", "['openstack'] cloud provider")
	cloudDeployCmd.Flags().StringVarP(&osPrefix, "os", "o", "Ubuntu 16", "prefix name of the OS image your servers should use")
	cloudDeployCmd.Flags().StringVarP(&osUsername, "username", "u", "ubuntu", "username needed to log in to the OS image specified by --os")
	cloudDeployCmd.Flags().IntVarP(&serverKeepAlive, "keepalive", "k", 60, "how long in seconds to keep idle spawned servers alive for")
	cloudDeployCmd.Flags().IntVarP(&maxServers, "max_servers", "m", 0, "maximum number of servers to spawn (0 means unlimited)")

	cloudTearDownCmd.Flags().StringVarP(&providerName, "provider", "p", "openstack", "['openstack'] cloud provider")
	cloudTearDownCmd.Flags().BoolVarP(&forceTearDown, "force", "f", false, "force teardown even when the remote manager cannot be accessed")
}

// *** so far we have only the below usage of doing things via ssh, but this
// will probably have to move out to a separate ssh package, or perhaps the
// cloud package in the future...

func bootstrapOnRemote(provider *cloud.Provider, server *cloud.Server, exe string, mp int, wp int, wrMayHaveStarted bool) {
	// upload ourselves
	remoteExe := filepath.Join(cloudBinDir, "wr")
	err := server.UploadFile(exe, remoteExe)
	if err != nil && !wrMayHaveStarted {
		provider.TearDown()
		die("failed to upload wr to the server at %s: %s", server.IP, err)
	}

	// create a config file on the remote to have the remote wr work on the same
	// ports that we'd use locally
	err = server.CreateFile(fmt.Sprintf("managerport: \"%d\"\nmanagerweb: \"%d\"\n", mp, wp), wrConfigFileName)
	if err != nil {
		provider.TearDown()
		die("failed to create our config file on the server at %s: %s", server.IP, err)
	}

	_, err = server.RunCmd("chmod u+x "+remoteExe, false)
	if err != nil && !wrMayHaveStarted {
		provider.TearDown()
		die("failed to make remote wr executable: %s", err)
	}

	// start up the manager
	var alreadyStarted bool
	if wrMayHaveStarted {
		response, err := server.RunCmd(fmt.Sprintf("%s manager status --deployment %s", remoteExe, config.Deployment), false)
		if err != nil && response == "started\n" {
			alreadyStarted = true
		}
	}
	if !alreadyStarted {
		_, err = server.RunCmd(fmt.Sprintf("%s manager start --deployment %s -s openstack", remoteExe, config.Deployment), true)
		if err != nil {
			provider.TearDown()
			die("failed to make start wr manager on the remote server: %s", err)
		}

		// wait a few seconds for the manager to start listening on its ports
		<-time.After(3 * time.Second)
	}
}

func startForwarding(serverIP, serverPort, serverUser, keyFile string, port int, pidPath string) (err error) {
	// first check if pidPath already has a pid and if that pid is alive
	if _, running := checkProcess(pidPath); running {
		//info("assuming the process with id %d is already forwarding port %d to %s:%d", pid, port, serverIP, port)
		return
	}

	// start ssh -L running
	cmd := exec.Command("ssh", "-i", keyFile, "-o", "ExitOnForwardFailure yes", "-qngNTL", fmt.Sprintf("%d:0.0.0.0:%d", port, port), fmt.Sprintf("%s@%s", serverUser, serverIP))
	err = cmd.Start()
	if err != nil {
		return
	}

	// store ssh's pid to file
	err = ioutil.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0600)

	// don't cmd.Wait(); ssh will continue running in the background after we
	// exit

	return
}

func checkProcess(pidPath string) (pid int, running bool) {
	// read file (treat errors such as file not existing as no process)
	pidBytes, err := ioutil.ReadFile(pidPath)
	if err != nil {
		return
	}

	// convert file contents to pid (also treating errors as no process)
	pid, err = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		return
	}

	// see if the pid is running
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	err = process.Signal(syscall.Signal(0))
	running = err == nil
	return
}

func killProcess(pid int) (err error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	err = process.Signal(syscall.Signal(9))
	return
}
