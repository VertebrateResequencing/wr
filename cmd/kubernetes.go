// Copyright © 2016-2018 Genome Research Limited
// Author: Theo Barber-Bany <tb15@sanger.ac.uk>.
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
	"bufio"
	"encoding/gob"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/sevlyar/go-daemon"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	kubedeployment "github.com/VertebrateResequencing/wr/kubernetes/deployment"
	"github.com/inconshreveable/log15"
	"github.com/kardianos/osext"
	"github.com/sb10/l15h"
	"github.com/spf13/cobra"
)

// podBinDir is where we will upload executables to our created pod.
// it is a volume mount added to the init container and the container that will
// run wr. As defining a volume mount overwrites whatever is in that directory
// we want this to be unique.
const podBinDir = "/wr-tmp"

// podScriptDir is where the configMap will be mounted.
const podScriptDir = "/scripts/"

// options for this cmd
var podPostCreationScript string
var postCreationConfigMap string
var podDNS string
var podConfigFiles string
var kubeDebug bool
var maxPods int

// cloudCmd represents the cloud command
var kubeCmd = &cobra.Command{
	Use:   "kubernetes",
	Short: "Kubernetes cluster interfacing",
	Long: `Kubernetes cluster interfacing.

To run wr on a kubernetes cluster, you need to deploy the "wr manager" to a 
unique namespace. From there the manager will run your commands on additional
pods spawned as demand dictates.

The kubernetes sub-commands make it easy to get started, interact with that remote
manager, and clean up afterwards.`,
}

// deploy sub-command brings up a "head" pod in the cluster and starts a proxy
// daemon to interact with the manager we spawn there
var kubeDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy a manager to a kubernetes cluster",
	Long: `Start up 'wr manager' on a kubernetes cluster.

Deploy creates a 'wr manager' pod. In a production deployment the remote manager 
will use a copy of the latest version of the wr database, taken from your S3 db
backup location, or if you don't use S3, from your local filesystem.

Deploy then sets up port forwarding in the background that lets you use the
normal wr command line utilities such as 'wr add' and view the wr website
locally, even though the manager is actually running remotely. Note that this
precludes starting wr manager locally as well. Also be aware that while 'wr add'
normally associates your current environment variables and working directory
with the cmds you want to run, with a remote deployment the working directory
defaults to /tmp, and commands will be run with the non-login environment
variables of the server the command is run on.

The --script option value can be, for example, the path to a bash script that
you want to run on any created pod before any commands run on them. You
might install some software for example. Note that the script is run by default
as root. If necessary you may specify a user. If  your bash script has commands with
'sudo' you may need to install sudo. This is usually when the image does not include
it. For debian based images this may look like 'apt-get -y install sudo'.

The --config_files option lets you specify comma separated arbitrary text file
paths that should be copied from your local system to any created cloud servers.
Absolute paths will be copied to the same absolute path on the server. For files
that should be transferred from your home directory to the cloud server's home
directory (which could be at different absolute paths), prefix your path with
"~/". If the local path of a file is unrelated to the remote path, separate the
paths with a colon to specify source and destination, eg.
"~/projectSpecific/.s3cfg:~/.s3cfg".
Local paths that don't exist are silently ignored.
This option is important if you want to be able to queue up commands that rely
on the --mounts option to 'wr add': you'd specify your s3 config file(s) which
contain your credentials for connecting to your s3 bucket(s).

Deploy can work with most container images because it uploads wr to any pod it
creates; your image does not have to have wr installed on it. The only
requirements of the image are that it has tar installed, and bash.
For --mounts to work, fuse-utils must be installed, and /etc/fuse.conf should
already have user_allow_other set or at least be present and commented out
(wr will enable it). By default 'ubuntu:latest' is used. Currently only docker
hub is supported`,
	Run: func(cmd *cobra.Command, args []string) {

		var postCreation []byte
		if podPostCreationScript != "" {
			var err error
			postCreation, err = ioutil.ReadFile(podPostCreationScript)
			if err != nil {
				die("--script %s could not be read: %s", podPostCreationScript, err)
			}
		}

		// first we need our working directory to exist
		createWorkingDir()

		// check to see if the manager is already running (regardless of the
		// state of the pid file); we can't proxy if a manager is already up
		jq := connect(1 * time.Second)
		if jq != nil {
			die("wr manager on port %s is already running (pid %d); please stop it before trying again.", config.ManagerPort, jq.ServerInfo.PID)
		}

		// we will spawn wr on the remote server we will create, which means we
		// need to know the path to ourselves in case we're not in the user's
		// $PATH
		exe, err := osext.Executable()
		if err != nil {
			die("could not get the path to wr: %s", err)
		}

		// for debug purposes, set up logging to STDERR
		kubeLogger := log15.New()
		logLevel := log15.LvlWarn
		if kubeDebug {
			logLevel = log15.LvlDebug
		}
		kubeLogger.SetHandler(log15.LvlFilterHandler(logLevel, l15h.CallerInfoHandler(log15.StderrHandler)))

		// get all necessary cloud resources in place
		mp, err := strconv.Atoi(config.ManagerPort)
		if err != nil {
			die("bad manager_port [%s]: %s", config.ManagerPort, err)
		}
		wp, err := strconv.Atoi(config.ManagerWeb)
		if err != nil {
			die("bad manager_web [%s]: %s", config.ManagerWeb, err)
		}
		// Set up the client
		c := kubedeployment.Controller{
			Client: &client.Kubernetesp{},
		}
		info("Authenticating against the provided cluster.")
		// Authenticate and populate Kubernetesp with clientset and restconfig.
		c.Clientset, c.Restconfig, err = c.Client.Authenticate()
		if err != nil {
			die("Could not authenticate against the cluster: %s", err)
		}

		// Daemonise here
		fwPidPath := filepath.Join(config.ManagerDir, "kubernetes_resources.fw.pid")
		cntxt := daemon.Context{
			PidFileName: fwPidPath,
			PidFilePerm: 0644,
			WorkDir:     "/",
			Umask:       027,
			Args:        args,
		}
		child, err := cntxt.Reborn()
		if err != nil {
			die("failed to daemonize: %s", err)
		}
		if child != nil {
			// PostParent() (Runs in the parent process after spawning child)
			info("please wait while %s resources are created...", providerName)

			// check that we can now connect to the remote manager
			// I'm setting the timeout to 120s, as some initscripts may
			// take a long time to complete.
			jq = connect(120 * time.Second)
			if jq == nil {
				die("could not talk to wr manager on server at %s after 120s")
			}

			info("wr manager remotely started on %s", sAddr(jq.ServerInfo))
			info("wr's web interface can be reached locally at http://localhost:%s", jq.ServerInfo.WebPort)
		} else {
			defer cntxt.Release()
			// PostChild() (what the child will run)

			debugStr := ""
			if cloudDebug {
				debugStr = " --debug"
			}

			// Look for a set of resources in the manager directory
			// If found, load them else use a new empty set.
			resourcePath := filepath.Join(config.ManagerDir, "kubernetes_resources")
			var resources *cloud.Resources
			var scriptName string
			var configMapName string
			if _, serr := os.Stat(resourcePath); os.IsNotExist(serr) {
				info("Using new set of resources, none found.")
				resources = &cloud.Resources{ResourceName: "Kubernetes", Details: make(map[string]string)}
				info("Initialising clients.")
				// Populate the rest of Kubernetesp
				err = c.Client.Initialize(c.Clientset)
				if err != nil {
					panic(err)
				}
				// Create the configMap
				scriptName = filepath.Base(podPostCreationScript)
				configMapName = strings.TrimSuffix(scriptName, filepath.Ext(scriptName))
				err = c.Client.CreateInitScriptConfigMap(configMapName, string(postCreation))
				if err != nil {
					panic(err)
				}
				// Store the namespace and configMapName for fun and profit.
				resources.Details["namespace"] = c.Client.NewNamespaceName
				resources.Details["configMapName"] = configMapName
				resources.Details["scriptName"] = scriptName

				// Save resources.
				file, err := os.OpenFile(resourcePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					panic(err)
				}
				encoder := gob.NewEncoder(file)
				encoder.Encode(resources)
				_ = file.Close()

			} else {
				file, err := os.Open(resourcePath)
				if err != nil {
					die("Could not open resource file with path: %s", err)
				}
				decoder := gob.NewDecoder(file)
				err = decoder.Decode(resources)
				if err != nil {
					panic(err)
				}
				namespace := resources.Details["namespace"]
				configMapName = resources.Details["configMapName"]
				scriptName = resources.Details["scriptName"]
				// Populate the rest of Kubernetesp
				err = c.Client.Initialize(c.Clientset, namespace)
				if err != nil {
					panic(err)
				}
				internal.LogClose(kubeLogger, file, "resource file", "path", resourcePath)
			}

			remoteExe := filepath.Join(podBinDir, "wr-linux")
			m := maxPods - 1

			mCmd := fmt.Sprintf("%s manager start --deployment %s --scheduler kubernetes --cloud_keepalive %d  --cloud_servers %d --config_map %s --cloud_dns '%s' --timeout %d%s",
				remoteExe, config.Deployment, serverKeepAlive, m, configMapName, podDNS, managerTimeoutSeconds, debugStr)
			binaryArgs := strings.Fields(mCmd)
			// Specify deployment options
			c.Opts = &kubedeployment.DeployOpts{
				ContainerImage: "ubuntu:latest",
				TempMountPath:  podBinDir,
				Files: []client.FilePair{
					{exe, podBinDir},
				},
				BinaryPath:      podScriptDir + scriptName,
				BinaryArgs:      binaryArgs,
				ConfigMapName:   configMapName,
				ConfigMountPath: podScriptDir,
				RequiredPorts:   []int{mp, wp},
			}
		}

	},
}

// teardown sub-command deletes all cloud resources we created and then stops
// the daemon by sending it a term signal
var kubeTearDownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Delete all kubernetes resources that deploy created",
	Long: `Immediately stop the remote workflow manager, saving its state.

Deletes all kubernetes resources that wr created (pods, deployments, config maps, namespaces).
(Except for any files that were saved to persistent cloud storage.)

Note that any runners that are currently running will die, along with any
commands they were running. It is more graceful to issue 'wr manager drain'
first, and regularly rerun drain until it reports the manager is stopped, and
only then request a teardown (you'll need to add the --force option). But this
is only a good idea if you have configured wr to back up its database to S3, as
otherwise your database going forward will not reflect anything you did during
that kubernetes deployment.

If you don't back up to S3, the teardown command tries to copy the remote
database locally, which is only possible while the remote server is still up
and accessible.`,
	Run: func(cmd *cobra.Command, args []string) {
		// before stopping the manager, make sure we can interact with the
		// provider - that our credentials are correct
		provider, err := cloud.New(providerName, cloudResourceName(""), filepath.Join(config.ManagerDir, "cloud_resources."+providerName))
		if err != nil {
			die("failed to connect to %s: %s", providerName, err)
		}

		// now check if the ssh forwarding is up
		fmPidFile := filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fm.pid")
		fmPid, fmRunning := checkProcess(fmPidFile)

		// try and stop the remote manager
		noManagerMsg := "; deploy first or use --force option"
		noManagerForcedMsg := "; tearing down anyway - you may lose changes if not backing up the database to S3!"
		serverHadProblems := false
		if fmRunning {
			jq := connect(1 * time.Second)
			if jq != nil {
				var syncMsg string
				if internal.IsRemote(config.ManagerDbBkFile) {
					if _, errf := os.Stat(config.ManagerDbFile); !os.IsNotExist(errf) {
						// move aside the local database so that if the manager is
						// started locally, the database will be restored from S3
						// and have the history of what was run in the cloud
						if errf = os.Rename(config.ManagerDbFile, config.ManagerDbFile+".old"); err == nil {
							syncMsg = "; the local database will be updated from S3 if manager started locally"
						} else {
							warn("could not rename the local database; if the manager is started locally, it will not be updated with the latest changes in S3! %s", errf)
						}
					}
				} else {
					// copy the remote database locally, so if the manager is
					// started locally we have the history of what was run in
					// the cloud. The gap between backing up and shutting down
					// is "fine"; though some db writes may occur, the user
					// obviously doesn't care about them. On recovery we won't
					// break any pipelines.
					errf := jq.BackupDB(config.ManagerDbFile)
					if errf != nil {
						msg := "there was an error trying to sync the remote database: " + errf.Error()
						if forceTearDown {
							warn(msg + noManagerForcedMsg)
						} else {
							die(msg)
						}
					}
					syncMsg = " and local database updated"
				}

				ok := jq.ShutdownServer()
				if ok {
					info("the remote wr manager was shut down" + syncMsg)
				} else {
					msg := "there was an error trying to shut down the remote wr manager"
					if forceTearDown {
						warn(msg + noManagerForcedMsg)
						serverHadProblems = true
					} else {
						die(msg)
					}
				}
			} else {
				msg := "the remote wr manager could not be connected to in order to shut it down"
				if forceTearDown {
					warn(msg + noManagerForcedMsg)
					serverHadProblems = true
				} else {
					die(msg + noManagerMsg)
				}
			}
		} else {
			if forceTearDown {
				warn("the deploy port forwarding is not running, so the remote manager could not be stopped" + noManagerForcedMsg)
				serverHadProblems = true
			} else {
				die("the deploy port forwarding is not running, so can't safely teardown" + noManagerMsg)
			}
		}

		// copy over any manager logs that got created locally (ignore errors,
		// and overwrite any existing file) *** currently missing the final
		// shutdown message doing things this way, but ok?...
		headNode := provider.HeadNode()
		if headNode != nil && headNode.Alive() {
			cloudLogFilePath := config.ManagerLogFile + "." + providerName
			errf := headNode.DownloadFile(filepath.Join("./.wr_"+config.Deployment, "log"), cloudLogFilePath)

			if errf != nil {
				warn("could not download the remote log file: %s", errf)
			} else {
				// display any crit lines in that log file
				if errf == nil {
					f, errf := os.Open(cloudLogFilePath)
					if errf == nil {
						explained := false
						scanner := bufio.NewScanner(f)
						for scanner.Scan() {
							line := scanner.Text()
							if strings.Contains(line, "lvl=crit") {
								if !explained {
									warn("looks like the manager on the remote server suffered critical errors:")
									explained = true
								}
								fmt.Println(line)
							}
						}

						if serverHadProblems {
							info("the remote manager log has been saved to %s", cloudLogFilePath)
						}
					}
				}
			}
		}

		// teardown cloud resources we created
		err = provider.TearDown()
		if err != nil {
			die("failed to delete the cloud resources previously created: %s", err)
		}
		err = os.Remove(filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".key"))
		if err != nil {
			warn("failed to delete the cloud resources file: %s", err)
		}
		info("deleted all cloud resources previously created")

		// kill the ssh forwarders
		if fmRunning {
			err = killProcess(fmPid)
			if err == nil {
				err = os.Remove(fmPidFile)
				if err != nil && !os.IsNotExist(err) {
					warn("failed to remove the forwarder pid file %s: %s", fmPidFile, err)
				}
			}
		}
		fwPidFile := filepath.Join(config.ManagerDir, "cloud_resources."+providerName+".fw.pid")
		if fwPid, fwRunning := checkProcess(fwPidFile); fwRunning {
			err = killProcess(fwPid)
			if err == nil {
				err = os.Remove(fwPidFile)
				if err != nil && !os.IsNotExist(err) {
					warn("failed to remove the forwarder pid file %s: %s", fwPidFile, err)
				}
			}
		}
	},
}

func init() {
	RootCmd.AddCommand(kubeCmd)
	kubeCmd.AddCommand(kubeDeployCmd)
	kubeCmd.AddCommand(kubeTearDownCmd)

	// flags specific to these sub-commands
	defaultConfig := internal.DefaultConfig(appLogger)
	kubeDeployCmd.Flags().StringVarP(&podPostCreationScript, "script", "s", defaultConfig.CloudScript, "path to a start-up script that will be run on each pod created")
	kubeDeployCmd.Flags().IntVarP(&serverKeepAlive, "keepalive", "k", defaultConfig.CloudKeepAlive, "how long in seconds to keep idle spawned servers alive for; 0 means forever")
	kubeDeployCmd.Flags().IntVarP(&maxServers, "max_servers", "m", defaultConfig.CloudServers+1, "maximum number of servers to spawn; 0 means unlimited (default 0)")
	kubeDeployCmd.Flags().StringVar(&podDNS, "network_dns", defaultConfig.CloudDNS, "comma separated DNS name server IPs to on the created pods")
	kubeDeployCmd.Flags().StringVarP(&podConfigFiles, "config_files", "c", defaultConfig.CloudConfigFiles, "comma separated paths of config files to copy to spawned pods")
	kubeDeployCmd.Flags().IntVarP(&managerTimeoutSeconds, "timeout", "t", 10, "how long to wait in seconds for the manager to start up")
	kubeDeployCmd.Flags().BoolVar(&kubeDebug, "debug", false, "include extra debugging information in the logs")

	kubeTearDownCmd.Flags().BoolVarP(&forceTearDown, "force", "f", false, "force teardown even when the remote manager cannot be accessed")
}

// func bootstrapOnRemote(provider *cloud.Provider, server *cloud.Server, exe string, mp int, wp int, keyPath string, wrMayHaveStarted bool) {

// 	if !alreadyStarted {
// 		// create a file containing all the env vars for this provider, so that
// 		// we can source it later
// 		envvars, _ := cloud.AllEnv(providerName)
// 		envvarExports := ""
// 		for _, env := range envvars {
// 			val := os.Getenv(env)
// 			if val == "" {
// 				continue
// 			}
// 			// *** this is bash-like only; is that a problem?
// 			envvarExports += fmt.Sprintf("export %s=\"%s\"\n", env, val)
// 		}
// 		err = server.CreateFile(envvarExports, wrEnvFileName)
// 		if err != nil {
// 			teardown(provider)
// 			die("failed to create our environment variables file on the server at %s: %s", server.IP, err)
// 		}
// 		_, _, err = server.RunCmd("chmod 600 "+wrEnvFileName, false)
// 		if err != nil {
// 			warn("failed to chmod 600 %s: %s", wrEnvFileName, err)
// 		}

// 		var configFilesArg string
// 		if cloudConfigFiles != "" {
// 			// strip any local file locations
// 			var remoteConfigFiles []string
// 			for _, cf := range strings.Split(cloudConfigFiles, ",") {
// 				parts := strings.Split(cf, ":")
// 				if len(parts) == 2 {
// 					remoteConfigFiles = append(remoteConfigFiles, parts[1])
// 				} else {
// 					remoteConfigFiles = append(remoteConfigFiles, cf)
// 				}
// 			}

// 			configFilesArg = " --cloud_config_files '" + strings.Join(remoteConfigFiles, ",") + "'"
// 		}

// 		debugStr := ""
// 		if cloudDebug {
// 			debugStr = " --debug"
// 		}
// 		mCmd := fmt.Sprintf("source %s && %s manager start --deployment %s -s %s -k %d -o '%s' -r %d -m %d -u %s%s%s%s%s --cloud_gateway_ip '%s' --cloud_cidr '%s' --cloud_dns '%s' --local_username '%s' --timeout %d%s && rm %s", wrEnvFileName, remoteExe, config.Deployment, providerName, serverKeepAlive, osPrefix, osRAM, m, osUsername, postCreationArg, flavorArg, osDiskArg, configFilesArg, cloudGatewayIP, cloudCIDR, cloudDNS, realUsername(), managerTimeoutSeconds, debugStr, wrEnvFileName)

// 		_, e, err := server.RunCmd(mCmd, false)
// 		if err != nil {
// 			warn("failed to start wr manager on the remote server")
// 			if len(e) > 0 {
// 				color.Red(e)
// 			}

// 			// copy over any manager logs that got created locally (ignore
// 			// errors, and overwrite any existing file)
// 			cloudLogFilePath := config.ManagerLogFile + "." + providerName
// 			errf := server.DownloadFile(filepath.Join("./.wr_"+config.Deployment, "log"), cloudLogFilePath)

// 		}
// 	}
// }

// func checkProcess(pidPath string) (pid int, running bool) {
// 	// read file (treat errors such as file not existing as no process)
// 	pidBytes, err := ioutil.ReadFile(pidPath)
// 	if err != nil {
// 		return pid, running
// 	}

// 	// convert file contents to pid (also treating errors as no process)
// 	pid, err = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
// 	if err != nil {
// 		return pid, running
// 	}

// 	// see if the pid is running
// 	process, err := os.FindProcess(pid)
// 	if err != nil {
// 		return pid, running
// 	}
// 	err = process.Signal(syscall.Signal(0))
// 	running = err == nil
// 	return pid, running
// }

// func killProcess(pid int) error {
// 	process, err := os.FindProcess(pid)
// 	if err != nil {
// 		return err
// 	}
// 	return process.Signal(syscall.Signal(9))
// }
