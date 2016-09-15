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
	"bytes"
	"errors"
	"fmt"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/kardianos/osext"
	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"io"
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
		var serverIP string
		usingExistingServer := false
		servers := provider.Servers()
		for sID, externalIP := range servers {
			if ok, _ := provider.CheckServer(sID); ok {
				serverIP = externalIP
				usingExistingServer = true
				info("using existing %s server at %s", providerName, serverIP)
				break
			}
		}
		if serverIP == "" {
			info("please wait while a server is spawned on %s...", providerName)
			_, serverIP, _, err = provider.Spawn(osPrefix, 2048, 20, 1, true)
			if err != nil {
				provider.TearDown()
				die("failed to launch a server in %s: %s", providerName, err)
			}
		}

		// ssh to the server, copy over our exe, and start running wr manager
		// there
		info("please wait while I start 'wr manager' on the %s server at %s...", providerName, serverIP)
		bootstrapOnRemote(provider, osUsername, serverIP, serverPort, exe, mp, wp, usingExistingServer)

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
		err = startForwarding(serverIP, serverPort, osUsername, keyPath, mp, filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fm.pid"))
		if err != nil {
			provider.TearDown()
			die("failed to set up port forwarding to %s:%d: %s", serverIP, mp, err)
		}
		err = startForwarding(serverIP, serverPort, osUsername, keyPath, wp, filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fw.pid"))
		if err != nil {
			provider.TearDown()
			die("failed to set up port forwarding to %s:%d: %s", serverIP, wp, err)
		}

		// check that we can now connect to the remote manager
		//<-time.After(15 * time.Second)
		jq = connect(10 * time.Second)
		if jq == nil {
			provider.TearDown()
			die("could not talk to wr manager on server at %s after 10s", serverIP)
		}
		sstats, err := jq.ServerStats()
		if err != nil {
			provider.TearDown()
			die("wr manager on server at %s started but doesn't seem to be functional: %s", serverIP, err)
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

	cloudTearDownCmd.Flags().StringVarP(&providerName, "provider", "p", "openstack", "['openstack'] cloud provider")
	cloudTearDownCmd.Flags().BoolVarP(&forceTearDown, "force", "f", false, "force teardown even when the remote manager cannot be accessed")
}

// *** so far we have only the below usage of doing things via ssh, but this
// will probably have to move out to a separate ssh package, or perhaps the
// cloud package in the future...

func bootstrapOnRemote(provider *cloud.Provider, user string, serverIP string, serverPort string, exe string, mp int, wp int, wrMayHaveStarted bool) {
	// parse private key and make config
	key, err := ssh.ParsePrivateKey([]byte(provider.PrivateKey()))
	if err != nil {
		provider.TearDown()
		die("failed to parse private key: %s", err)
	}
	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(key),
		},
	}

	// dial in to remote host, allowing certain errors that indicate that the
	// network or server isn't really ready for ssh yet; wait for up to 35mins
	// for success (can take this long when the network was only just created!)
	hostAndPort := serverIP + ":" + serverPort
	sshClient, err := ssh.Dial("tcp", hostAndPort, sshConfig)
	if err != nil {
		limit := time.After(10 * time.Minute)
		ticker := time.NewTicker(1 * time.Second)
		ticks := 0
	DIAL:
		for {
			select {
			case <-ticker.C:
				ticks++
				sshClient, err = ssh.Dial("tcp", hostAndPort, sshConfig)
				if err != nil && (strings.HasSuffix(err.Error(), "connection timed out") || strings.HasSuffix(err.Error(), "no route to host") || strings.HasSuffix(err.Error(), "connection refused")) {
					if ticks == 2 {
						info("this may take a few minutes, please be patient...")
					}
					continue DIAL
				}
				// worked, or failed with a different error: stop trying
				ticker.Stop()
				break DIAL
			case <-limit:
				ticker.Stop()
				err = errors.New("giving up waiting for ssh to work")
				break DIAL
			}
		}
		if err != nil {
			provider.TearDown()
			die("failed to connect to the server at %s: %s", hostAndPort, err)
		}
	}

	// upload ourselves
	remoteExe := filepath.Join(cloudBinDir, "wr")
	err = uploadFileToRemote(sshClient, exe, remoteExe)
	if err != nil && !wrMayHaveStarted {
		provider.TearDown()
		die("failed to upload wr to the server at %s: %s", hostAndPort, err)
	}

	// create a config file on the remote to have the remote wr work on the same
	// ports that we'd use locally
	err = createFileOnRemote(sshClient, fmt.Sprintf("managerport: \"%d\"\nmanagerweb: \"%d\"\n", mp, wp), wrConfigFileName)
	if err != nil {
		provider.TearDown()
		die("failed to create our config file on the server at %s: %s", hostAndPort, err)
	}

	_, err = runCmdOnRemote(sshClient, "chmod u+x "+remoteExe, false)
	if err != nil && !wrMayHaveStarted {
		provider.TearDown()
		die("failed to make remote wr executable: %s", err)
	}

	// start up the manager
	var alreadyStarted bool
	if wrMayHaveStarted {
		response, err := runCmdOnRemote(sshClient, fmt.Sprintf("%s manager status --deployment %s", remoteExe, config.Deployment), false)
		if err != nil && response == "started\n" {
			alreadyStarted = true
		}
	}
	if !alreadyStarted {
		_, err = runCmdOnRemote(sshClient, fmt.Sprintf("%s manager start --deployment %s -s local", remoteExe, config.Deployment), true)
		if err != nil {
			provider.TearDown()
			die("failed to make start wr manager on the remote server: %s", err)
		}

		// wait a few seconds for the manager to start listening on its ports
		<-time.After(3 * time.Second)
	}
}

func runCmdOnRemote(sshClient *ssh.Client, cmd string, background bool) (response string, err error) {
	// create a session
	session, err := sshClient.NewSession()
	if err != nil {
		return
	}
	defer session.Close()

	if background {
		cmd = "sh -c 'nohup " + cmd + " > /dev/null 2>&1 &'"
	}

	var b bytes.Buffer
	session.Stdout = &b
	if err = session.Run(cmd); err != nil {
		return
	}
	response = b.String()
	return
}

func uploadFileToRemote(sshClient *ssh.Client, source string, dest string) (err error) {
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return
	}
	defer client.Close()

	// create all parent dirs of dest
	err = makeRemoteParentDirs(sshClient, dest)
	if err != nil {
		return
	}

	// open source, create dest
	sourceFile, err := os.Open(source)
	if err != nil {
		return
	}
	defer sourceFile.Close()

	destFile, err := client.Create(dest)
	if err != nil {
		return
	}

	// copy the file content over
	_, err = io.Copy(destFile, sourceFile)
	return
}

func createFileOnRemote(sshClient *ssh.Client, content string, dest string) (err error) {
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return
	}
	defer client.Close()

	// create all parent dirs of dest
	err = makeRemoteParentDirs(sshClient, dest)
	if err != nil {
		return
	}

	// create dest
	destFile, err := client.Create(dest)
	if err != nil {
		return
	}

	// write the content
	_, err = io.WriteString(destFile, content)
	return
}

func makeRemoteParentDirs(sshClient *ssh.Client, dest string) (err error) {
	//*** it would be nice to do this with client.Mkdir, but that doesn't do
	// the equivalent of mkdir -p, and errors out if dirs already exist... for
	// now it's easier to just call mkdir
	dir := filepath.Dir(dest)
	if dir != "." {
		_, err = runCmdOnRemote(sshClient, "mkdir -p "+dir, false)
		if err != nil {
			return
		}
	}
	return
}

func startForwarding(serverIP, serverPort, serverUser, keyFile string, port int, pidPath string) (err error) {
	// first check if pidPath already has a pid and if that pid is alive
	if pid, running := checkProcess(pidPath); running {
		//info("assuming the process with id %d is already forwarding port %d to %s:%d", pid, port, serverIP, port)
		return
	}

	// start ssh -L running
	cmd := exec.Command("ssh", "-i", keyFile, "-o", "ExitOnForwardFailure yes", "-qnNTL", fmt.Sprintf("%d:0.0.0.0:%d", port, port), fmt.Sprintf("%s@%s", serverUser, serverIP))
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
