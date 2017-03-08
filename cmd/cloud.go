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
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
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

// cloudBinDir is where we will upload executables to our created cloud server;
// it needs to be somewhere that is likely to be writable on all OS images, and
// in particular not in the home dir since we may want to run commands on
// spawned servers that are running different OS images with different user.
const cloudBinDir = "/tmp"

// wrConfigFileName is the name of our main config file, which we need when we
// create on on our created cloud server
const wrConfigFileName = ".wr_config.yml"

// options for this cmd
var providerName string
var maxServers int
var serverKeepAlive int
var osPrefix string
var osUsername string
var osRAM int
var osDisk int
var flavorRegex string
var postCreationScript string
var cloudGatewayIP string
var cloudCIDR string
var cloudDNS string
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

Deploy then sets up ssh forwarding in the background that lets you use the
normal wr command line utilities such as 'wr add' and view the wr website
locally, even though the manager is actually running remotely. Note that this
precludes starting wr manager locally as well. Also be aware of the way that
'wr add' works, with it associating your current environment variables and
working directory with the cmds you want to run; you will have to make sure
these make sense when the cmd is run on the OS you specify.

Deploy can work with any given OS image because it uploads wr to any server it
creates; your OS image does not have to have wr installed on it. The only
requirements of the OS image are that it support ssh and sftp on port 22, and
that it be a 64bit linux-like system with /proc/*/smaps, /tmp and some local
writeable disk space in the home directory.

The openstack provider needs these environment variables to be set:
OS_TENANT_ID, OS_AUTH_URL, OS_PASSWORD, OS_REGION_NAME, OS_USERNAME
You can get these values by logging in to your OpenStack dashboard web interface
and navigating to Compute -> Access & Security. From there click the 'API
Access' tab and then click the 'Download Openstack RC File' button.

Note that when specifying the OpenStack environment variable 'OS_AUTH_URL', it
must work from within an OpenStack server running your chosen OS image. This is
most likely to succeed if you use an IP address instead of a host name.`,
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

		var postCreation []byte
		if postCreationScript != "" {
			var err error
			postCreation, err = ioutil.ReadFile(postCreationScript)
			if err != nil {
				die("--script %s could not be read: %s", postCreationScript, err)
			}
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
		provider, err := cloud.New(providerName, cloudResourceName(""), filepath.Join(config.ManagerDir, "cloud_resources."+providerName))
		if err != nil {
			die("failed to connect to %s: %s", providerName, err)
		}
		serverPort := "22"
		info("please wait while %s resources are created...", providerName)
		err = provider.Deploy(&cloud.DeployConfig{
			RequiredPorts:  []int{22, mp, wp},
			GatewayIP:      cloudGatewayIP,
			CIDR:           cloudCIDR,
			DNSNameServers: strings.Split(cloudDNS, ","),
		})
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
			flavor, err := provider.CheapestServerFlavor(1, osRAM, flavorRegex)
			if err != nil {
				provider.TearDown()
				die("failed to launch a server in %s: %s", providerName, err)
			}
			server, err = provider.Spawn(osPrefix, osUsername, flavor.ID, osDisk, 0*time.Second, true)
			if err != nil {
				provider.TearDown()
				die("failed to launch a server in %s: %s", providerName, err)
			}
			err = server.WaitUntilReady(postCreation)
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

		// before stopping the manager, make sure we can interact with the
		// provider - that our credentials are correct
		provider, err := cloud.New(providerName, cloudResourceName(""), filepath.Join(config.ManagerDir, "cloud_resources."+providerName))
		if err != nil {
			die("failed to connect to %s: %s", providerName, err)
		}

		// now check if the ssh forwarding is up
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
	defaultConfig := internal.DefaultConfig()
	cloudDeployCmd.Flags().StringVarP(&providerName, "provider", "p", "openstack", "['openstack'] cloud provider")
	cloudDeployCmd.Flags().StringVarP(&osPrefix, "os", "o", defaultConfig.CloudOS, "prefix name of the OS image your servers should use")
	cloudDeployCmd.Flags().StringVarP(&osUsername, "username", "u", defaultConfig.CloudUser, "username needed to log in to the OS image specified by --os")
	cloudDeployCmd.Flags().IntVarP(&osRAM, "os_ram", "r", defaultConfig.CloudRAM, "ram (MB) needed by the OS image specified by --os")
	cloudDeployCmd.Flags().IntVarP(&osDisk, "os_disk", "d", defaultConfig.CloudDisk, "minimum disk (GB) for servers")
	cloudDeployCmd.Flags().StringVarP(&flavorRegex, "flavor", "f", defaultConfig.CloudFlavor, "a regular expression to limit server flavors that can be automatically picked")
	cloudDeployCmd.Flags().StringVarP(&postCreationScript, "script", "s", defaultConfig.CloudScript, "path to a start-up script that will be run on each server created")
	cloudDeployCmd.Flags().IntVarP(&serverKeepAlive, "keepalive", "k", defaultConfig.CloudKeepAlive, "how long in seconds to keep idle spawned servers alive for")
	cloudDeployCmd.Flags().IntVarP(&maxServers, "max_servers", "m", defaultConfig.CloudServers+1, "maximum number of servers to spawn; 0 means unlimited (default 0)")
	cloudDeployCmd.Flags().StringVar(&cloudGatewayIP, "network_gateway_ip", defaultConfig.CloudGateway, "gateway IP for the created subnet")
	cloudDeployCmd.Flags().StringVar(&cloudCIDR, "network_cidr", defaultConfig.CloudCIDR, "CIDR of the created subnet")
	cloudDeployCmd.Flags().StringVar(&cloudDNS, "network_dns", defaultConfig.CloudDNS, "comma separated DNS name server IPs to use in the created subnet")

	cloudTearDownCmd.Flags().StringVarP(&providerName, "provider", "p", "openstack", "['openstack'] cloud provider")
	cloudTearDownCmd.Flags().BoolVarP(&forceTearDown, "force", "f", false, "force teardown even when the remote manager cannot be accessed")
}

func bootstrapOnRemote(provider *cloud.Provider, server *cloud.Server, exe string, mp int, wp int, wrMayHaveStarted bool) {
	// upload ourselves to /tmp
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

	_, _, err = server.RunCmd("chmod u+x "+remoteExe, false)
	if err != nil && !wrMayHaveStarted {
		provider.TearDown()
		die("failed to make remote wr executable: %s", err)
	}

	// copy over our cloud resource details, including our ssh key
	cRN := cloudResourceName("")
	localResourceFile := filepath.Join(config.ManagerDir, "cloud_resources."+providerName+"."+cRN)
	remoteResourceFile := filepath.Join("./.wr_"+config.Deployment, "cloud_resources."+providerName+"."+cRN)
	err = server.UploadFile(localResourceFile, remoteResourceFile)
	if err != nil && !wrMayHaveStarted {
		provider.TearDown()
		die("failed to upload wr cloud resources file to the server at %s: %s", server.IP, err)
	}
	localKeyFile := filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".key")
	err = ioutil.WriteFile(localKeyFile, []byte(provider.PrivateKey()), 0600)
	if err != nil {
		provider.TearDown()
		die("failed to create key file %s: %s", localKeyFile, err)
	}
	remoteKeyFile := filepath.Join("./.wr_"+config.Deployment, "cloud_resources."+providerName+".key")
	err = server.UploadFile(localKeyFile, remoteKeyFile)
	if err != nil && !wrMayHaveStarted {
		provider.TearDown()
		die("failed to upload wr cloud key file to the server at %s: %s", server.IP, err)
	}
	_, _, err = server.RunCmd("chmod 600 "+remoteResourceFile, false)
	_, _, err = server.RunCmd("chmod 600 "+remoteKeyFile, false)

	// start up the manager
	var alreadyStarted bool
	if wrMayHaveStarted {
		response, _, err := server.RunCmd(fmt.Sprintf("%s manager status --deployment %s", remoteExe, config.Deployment), false)
		if err != nil && response == "started\n" {
			alreadyStarted = true
		}
	}
	if !alreadyStarted {
		// build a command prefix that sets all the required env vars for this
		// provider
		envvarPrefix := ""
		envvars, _ := cloud.RequiredEnv(providerName)
		for _, envvar := range envvars {
			envvarPrefix += fmt.Sprintf("%s=\"%s\" ", envvar, os.Getenv(envvar))
		}

		var postCreationArg string
		if postCreationScript != "" {
			// copy over the post creation script to the server so remote
			// manager can use it
			remoteScriptFile := filepath.Join("./.wr_"+config.Deployment, "cloud_resources."+providerName+".script")
			err = server.UploadFile(postCreationScript, remoteScriptFile)
			if err != nil && !wrMayHaveStarted {
				provider.TearDown()
				die("failed to upload wr cloud script file to the server at %s: %s", server.IP, err)
			}

			postCreationArg = " -p " + remoteScriptFile
		}

		var flavorArg string
		if flavorRegex != "" {
			flavorArg = " -l '" + flavorRegex + "'"
		}

		var osDiskArg string
		if osDisk > 0 {
			osDiskArg = " -d " + strconv.Itoa(osDisk)
		}

		// get the manager running
		mCmd := fmt.Sprintf("%s%s manager start --deployment %s -s %s -k %d -o '%s' -r %d -m %d -u %s%s%s%s --cloud_gateway_ip '%s' --cloud_cidr '%s' --cloud_dns '%s' --local_username '%s'", envvarPrefix, remoteExe, config.Deployment, providerName, serverKeepAlive, osPrefix, osRAM, maxServers-1, osUsername, postCreationArg, flavorArg, osDiskArg, cloudGatewayIP, cloudCIDR, cloudDNS, realUsername())
		_, _, err = server.RunCmd(mCmd, false)
		if err != nil {
			provider.TearDown()
			die("failed to start wr manager on the remote server: %s", err)
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
	cmd := exec.Command("ssh", "-i", keyFile, "-o", "ExitOnForwardFailure yes", "-o", "UserKnownHostsFile /dev/null", "-o", "StrictHostKeyChecking no", "-qngNTL", fmt.Sprintf("%d:0.0.0.0:%d", port, port), fmt.Sprintf("%s@%s", serverUser, serverIP))
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
