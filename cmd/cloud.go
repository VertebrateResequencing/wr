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
	"github.com/scottkiss/gosshtool"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
		f, _ := os.Create(fmt.Sprintf("/nfs/users/nfs_s/sb10/src/go/src/github.com/VertebrateResequencing/wr/stderr.%d", os.Getpid()))
		syscall.Dup2(int(f.Fd()), int(os.Stderr.Fd()))

		warn("WR_CLOUD_DEPLOY_SERVERIP is %s", os.Getenv("WR_CLOUD_DEPLOY_SERVERIP"))

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

		// set up cloud resources, start a server, run wr on it; we do this
		// now, before "forking" our port forwarding child, because we want
		// info and error lines printed to STDOUT/ERR as we go; on the other
		// hand, the child can't repeat this so we have the ENV VAR check to
		// skip
		serverIP := os.Getenv("WR_CLOUD_DEPLOY_SERVERIP")
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
		if serverIP == "" {
			warn("setting up")
			// get all necessary cloud resources in place
			info("please wait while %s resources are created...", providerName)
			err = provider.Deploy([]int{22, mp, wp})
			if err != nil {
				die("failed to create resources in %s: %s", providerName, err)
			}

			// get/spawn a "head node" server
			info("please wait while a server is spawned on %s...", providerName)
			servers := provider.Servers()
			for sID, externalIP := range servers {
				if ok, _ := provider.CheckServer(sID); ok {
					serverIP = externalIP
					break
				}
			}
			if serverIP == "" {
				_, serverIP, _, err = provider.Spawn(osPrefix, 2048, 20, 1, true)
				if err != nil {
					provider.TearDown()
					die("failed to launch a server in %s: %s", providerName, err)
				}
			}

			// ssh to the server, copy over our exe, and start running wr manager
			// there
			info("please wait while I start 'wr manager' on the %s server at %s...", providerName, serverIP)
			bootstrapOnRemote(provider, osUsername, serverIP, serverPort, exe, mp, wp)

			// before deaemonizing, set an env var so that the "forked" child
			// won't repeat this block (and so it knows the server ip)
			os.Setenv("WR_CLOUD_DEPLOY_SERVERIP", serverIP)
		}

		// now daemonize to bring up a port forwarder so user can easily
		// interact with the remote manager
		child, context := daemonize(config.DeployPidFile, config.ManagerUmask)
		if child != nil {
			warn("parent will check on child")
			// parent; wait a while for our child to bring up the proxy server
			// before exiting
			<-time.After(15 * time.Second)
			jq := connect(10 * time.Second)
			if jq == nil {
				//provider.TearDown()
				die("could not talk to wr manager on server at %s after 10s", serverIP)
			}
			sstats, err := jq.ServerStats()
			if err != nil {
				provider.TearDown()
				die("wr manager on server at %s started but doesn't seem to be functional: %s", serverIP, err)
			}

			info("wr manager remotely started on %s", sAddr(sstats.ServerInfo))
			info("wr's web interface can be reached locally at http://localhost:%s", sstats.ServerInfo.WebPort)
		} else {
			// daemonized child, that will run until signalled to stop
			defer context.Release()
			warn("child will start forwarder")
			startForwarder(serverIP, serverPort, mp, wp, provider, osUsername)
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
		if providerName == "" {
			die("--provider is required")
		}

		// first check that the deploy proxy server is up
		pid, piderr := daemon.ReadPidFile(config.DeployPidFile)
		proxyRunning := false
		if piderr == nil {
			err := syscall.Kill(pid, syscall.Signal(0))
			if err == nil {
				proxyRunning = true
			}
		} else {
			warn("could not read the pid from pid file %s; is the deploy proxy actually running? [%s]", config.DeployPidFile, piderr)
		}

		// try and stop the remote manager; doing this results in a graceful
		// saving of the db locally
		if proxyRunning {
			jq := connect(100 * time.Millisecond)
			if jq != nil {
				ok := jq.ShutdownServer()
				if ok {
					info("the remote wr manager was shut down")
				} else {
					warn("there was an error trying to shut down the remote wr manager")
				}
			} else {
				warn("the remote wr manager could not be connected to in order to shut it down; hopefully it was all ready stopped")
			}
		} else {
			die("the deploy proxy is not running, so can't safely teardown; deploy first")
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
		info("deleted all cloud resources previously created")

		// unlike manager, this is hopefully pretty simple and straightforward
		// to kill, relying on the pid?...
		if stopdaemon(pid, "pid file "+config.DeployPidFile, "cloud deploy") {
			info("wr cloud deploy daemon was stopped successfully")
		} else {
			die("could not stop the wr cloud deploy daemon (pid %d) running", pid)
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
}

func startForwarder(serverIP, serverPort string, mp, wp int, provider *cloud.Provider, user string) {
	runtime.GOMAXPROCS(runtime.NumCPU())
	<-time.After(10 * time.Second)
	hostAndPort := serverIP + ":" + serverPort

	// start port forwarding that goes from localhost ports manager_port and
	// manager_web to the head node we spawned
	sshConfig := getSSHConfig(provider, user)
	go listen(mp, sshConfig, hostAndPort)
	go listen(wp, sshConfig, hostAndPort)
	if false {
		server := new(gosshtool.LocalForwardServer)
		server.LocalBindAddress = fmt.Sprintf(":%d", mp)
		server.RemoteAddress = fmt.Sprintf("localhost:%d", mp)
		server.SshServerAddress = serverIP
		server.SshPrivateKey = provider.PrivateKey()
		server.SshUserName = user
		warn("server: %+v", server)
		server.Start(started)
		defer server.Stop()

		server2 := new(gosshtool.LocalForwardServer)
		server2.LocalBindAddress = fmt.Sprintf(":%d", wp)
		server2.RemoteAddress = fmt.Sprintf("localhost:%d", wp)
		server2.SshServerAddress = serverIP
		server2.SshPrivateKey = provider.PrivateKey()
		server2.SshUserName = user
		warn("server2: %+v", server2)
		server2.Start(started)
		defer server2.Stop()
	}

	// wait until we receive a signal to stop
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	warn("starting infinite for loop")
	for {
		select {
		case <-sigs:
			warn("received sig")
			return
		}
	}
}

func started() {
	warn("go over ssh server started")
}

// forwarding based on http://stackoverflow.com/a/21655505/675083
func listen(port int, sshConfig *ssh.ClientConfig, serverAddress string) {
	localAddress := fmt.Sprintf("localhost:%d", port)
	listener, err := net.Listen("tcp", localAddress)
	if err != nil {
		die("could not listen on %s: %s", port, err)
	}
	warn("listening to %s", localAddress)

	for {
		mConn, err := listener.Accept()
		warn("got a message on %s", localAddress)
		if err == nil {
			go forward(mConn, sshConfig, serverAddress, localAddress)
		}
	}
}

func forward(localConn net.Conn, config *ssh.ClientConfig, serverAddress string, localAddress string) {
	// connect to server
	sshClientConn, err := ssh.Dial("tcp", serverAddress, config)
	if err != nil {
		die("ssh.Dial to %s failed: %s", serverAddress, err)
	}
	warn("dialed in to %s", serverAddress)

	// connect to local port on remote server
	sshConn, err := sshClientConn.Dial("tcp", localAddress)
	if err != nil {
		die("ssh.Dial to %s failed: %s", localAddress, err)
	}
	warn("subdialed to %s", localAddress)

	// copy localConn.Reader to sshConn.Writer
	go func() {
		_, err = io.Copy(sshConn, localConn)
		if err != nil {
			die("io.Copy l to s failed: %v", err)
		}
	}()

	// copy sshConn.Reader to localConn.Writer
	go func() {
		_, err = io.Copy(localConn, sshConn)
		if err != nil {
			die("io.Copy s to l failed: %v", err)
		}
	}()
}

// *** so far we have only the below usage of doing things via ssh, but this
// will probably have to move out to a separate ssh package, or perhaps the
// cloud package in the future...

func getSSHConfig(provider *cloud.Provider, user string) *ssh.ClientConfig {
	// parse private key and make config
	key, err := ssh.ParsePrivateKey([]byte(provider.PrivateKey()))
	if err != nil {
		provider.TearDown()
		die("failed to parse private key: %s", err)
	}
	c := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(key),
		},
	}
	warn("ssh.ClientConfig: %+v", c)
	return c
}

func bootstrapOnRemote(provider *cloud.Provider, user string, serverIP string, serverPort string, exe string, mp int, wp int) {
	sshConfig := getSSHConfig(provider, user)

	// dial in to remote host, allowing certain errors that indicate that the
	// network or server isn't really ready for ssh yet; wait for up to 35mins
	// for success (can take this long when the network was only just created!)
	before := time.Now()
	hostAndPort := serverIP + ":" + serverPort
	sshClient, err := ssh.Dial("tcp", hostAndPort, sshConfig)
	if err != nil {
		limit := time.After(10 * time.Minute)
		ticker := time.NewTicker(1 * time.Second)
		ticks := 0
		info("waiting for ssh to the server to work...")
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

	info("ssh successful, will start wr manager remotely...")
	info("that took %s", time.Since(before))

	// upload ourselves
	remoteExe := filepath.Join(cloudBinDir, "wr")
	err = uploadFileToRemote(sshClient, exe, remoteExe)
	if err != nil {
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
	if err != nil {
		provider.TearDown()
		die("failed to make remote wr executable: %s", err)
	}
	_, err = runCmdOnRemote(sshClient, fmt.Sprintf("%s manager start --deployment %s -s local", remoteExe, config.Deployment), true)
	ioutil.WriteFile("key", []byte(provider.PrivateKey()), 0644)
	if err != nil {
		provider.TearDown()
		die("failed to make start wr manager on the remote server: %s", err)
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
