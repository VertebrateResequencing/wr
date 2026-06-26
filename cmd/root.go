/*******************************************************************************
 * Copyright (c) 2016-2018, 2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package cmd

// this is the cobra file that enables subcommands and handles command-line args

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
)

// maxCloudResourceUsernameLength is the maximum length that cloud username can
// be. It is limited because it will form part of cloudResourceName(), which
// will in turn form hostnames, which have max length 63. cloudResourceName()
// has a fixed prefix of length up to 8, and host names will include a UUID of
// length 36 and a prefix length 1, leaving 18 characters for the username.
const maxCloudResourceUsernameLength = 18

// these variables are accessible by all subcommands.
var (
	deployment string
	config     *internal.Config
)

// these are shared by some of the subcommands.
var (
	addr       string
	caFile     string
	timeoutint int
	cmdCwd     string
)

// RootCmd represents the base command when called without any subcommands.
var RootCmd = &cobra.Command{
	Use:   "wr",
	Short: "wr is a software workflow management system.",
	Long: `wr is a software workflow management system and command runner.

You use it to run the same sequence of commands (a "workflow") on many different
input files (which comprise a "datasource").

Initially, you start the management system, which maintains a queue of the
commands you want to run:
$ wr manager start

Then you add commands you want to run to the queue:
$ wr add

At this point your commands should be running, and you can monitor their
progress with:
$ wr status`,
}

// Execute adds all child commands to the root command and sets flags
// appropriately. This is called by main.main(). It only needs to happen once to
// the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		die("%s", err.Error())
	}
}

// ExecuteLSF is for treating a call to wr as if `wr lsf xxx` was called, for
// the LSF emulation to work.
func ExecuteLSF(cmd string) {
	args := append([]string{"lsf", cmd}, os.Args[1:]...)
	command, _, err := RootCmd.Find(args)
	if err != nil {
		die("%s", err.Error())
	}
	RootCmd.SetArgs(args)
	if err := command.Execute(); err != nil {
		die("%s", err.Error())
	}
}

func init() {
	// set up logging to stderr
	clog.ToDefaultAtLevel("info")

	// global flags
	RootCmd.PersistentFlags().StringVar(&deployment, "deployment", internal.DefaultDeployment(context.Background()),
		"use production or development config")

	cobra.OnInitialize(initConfig)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	config = internal.ConfigLoadFromCurrentDir(context.Background(), deployment)
	clog.ConfigureFileRotation(clog.FileRotationConfig{
		MaxSizeMB:  config.LogsMaxSizeMB,
		MaxBackups: config.LogsMaxBackups,
		MaxAgeDays: config.LogsMaxAgeDays,
		Compress:   config.LogsCompress,
	})
	addr = config.ManagerHost + ":" + config.ManagerPort
	caFile = config.ManagerCAFile
}

// managerAddrFile returns the path to the file that stores the manager's actual address.
func managerAddrFile() string {
	return filepath.Join(filepath.Dir(config.ManagerTokenFile), "manager.addr")
}

// token reads and returns the token from the file created when the manager
// starts.
func token() ([]byte, error) {
	token, err := os.ReadFile(config.ManagerTokenFile)
	if err != nil {
		return nil, err
	}
	return token, nil
}

// managerAddr reads and returns the address from the file created when the manager
// starts.
func managerAddr() (string, error) {
	addrBytes, err := os.ReadFile(managerAddrFile())
	if err != nil {
		return "", err
	}

	return string(addrBytes), nil
}

// realUsername returns the username of the current user.
func realUsername() string {
	username, err := internal.Username()
	if err != nil {
		die("could not get username: %s", err)
	}
	return username
}

// cloudResourceName returns a user and deployment specific string that can be
// used to name cloud resources so they can be identified as having been created
// by wr. username arg defaults to the real username of the user running wr.
func cloudResourceName(username string) string {
	if username == "" {
		username = realUsername()
	}
	var dep string
	if config.Deployment == internal.Production {
		dep = "prod"
	} else {
		dep = "dev"
	}
	return "wr-" + dep + "-" + username
}

// info is a convenience to log a message at the Info level.
func info(msg string, a ...any) {
	clog.Info(context.Background(), fmt.Sprintf(msg, a...))
}

// warn is a convenience to log a message at the Warn level.
func warn(msg string, a ...any) {
	clog.Warn(context.Background(), fmt.Sprintf(msg, a...))
}

// die is a convenience to log a message at the Error level and exit non zero.
func die(msg string, a ...any) {
	clog.Error(context.Background(), fmt.Sprintf(msg, a...))
	os.Exit(1)
}

// createWorkingDir ensures the main working directory is available
func createWorkingDir() {
	_, err := os.Stat(config.ManagerDir)
	if err != nil {
		if os.IsNotExist(err) {
			// try and create the directory
			err = os.MkdirAll(config.ManagerDir, os.ModePerm)
			if err != nil {
				die("could not create the working directory '%s': %v", config.ManagerDir, err)
			}
		} else {
			die("could not access or create the working directory '%s': %v", config.ManagerDir, err)
		}
	}
}

// daemonize spawns a child copy of ourselves with the correct deployment (we
// need to be careful because the default deployment depends on current dir, and
// the child is forced to run from /). Supplying extraArgs can override earlier
// args (to eg. re-specify an option with a relative path with an absolute
// path).
func daemonize(pidFile string, umask int, extraArgs ...string) (*os.Process, *daemon.Context) {
	args := os.Args
	hadDeployment := slices.Contains(args, "--deployment")
	if !hadDeployment {
		args = append(args, "--deployment")
		args = append(args, config.Deployment)
	}

	args = append(args, extraArgs...)

	dContext := &daemon.Context{
		PidFileName: pidFile,
		PidFilePerm: 0o644,
		WorkDir:     "/",
		Args:        args,
		Umask:       umask,
	}

	child, err := dContext.Reborn()
	if err != nil {
		// try again, deleting the pidFile first
		errr := os.Remove(pidFile)
		if errr != nil && !os.IsNotExist(errr) {
			warn("failed to delete existing pid file: %s", errr)
		}

		child, err = dContext.Reborn()
		if err != nil {
			die("failed to daemonize: %s", err)
		}
	}
	return child, dContext
}

// stopdaemon stops the daemon created by daemonize() by sending it SIGTERM and
// checking it really exited
func stopdaemon(pid int, source string) bool {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err != nil {
		warn("wr manager is running with pid %d according to %s, but failed to send it SIGTERM: %s", pid, source, err)
		return false
	}

	// wait a while for the daemon to gracefully close down
	giveupseconds := 120
	giveup := time.After(time.Duration(giveupseconds) * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	stopped := make(chan bool, 1)
	go func() {
		for {
			select {
			case <-ticker.C:
				err = syscall.Kill(pid, syscall.Signal(0))
				if err == nil {
					// pid is still running
					continue
				}
				// assume the error was "no such process" *** should I do a string comparison to confirm?
				ticker.Stop()
				stopped <- true
				return
			case <-giveup:
				ticker.Stop()
				stopped <- false
				return
			}
		}
	}()
	ok := <-stopped

	// if it didn't stop, offer to force kill it? That's a bit dangerous...
	// just warn for now
	if !ok {
		warn("wr manager, running with pid %d according to %s, is still running %ds after I sent it a SIGTERM", pid, source, giveupseconds)
	}

	return ok
}

// sAddr gets a nice manager address to report in logs, preferring hostname,
// falling back on the ip address if that wasn't set
func sAddr(s *jobqueue.ServerInfo) string {
	saddr := s.Host
	if saddr == "localhost" {
		saddr = s.Addr
	} else {
		saddr += ":" + s.Port
	}
	return saddr
}

// connect gives you a connected client. Dies on error. Dies if there is no
// token file. Does not die or report any kind of error if an optional bool is
// supplied true.
func connect(wait time.Duration, expectedToBeDown ...bool) *jobqueue.Client {
	shouldWarn := !(len(expectedToBeDown) == 1 && expectedToBeDown[0])

	token, err := token()
	if err != nil && shouldWarn {
		die("could not read token file; has the manager been started? [%s]", err)
	}

	// try to get the actual address from the manager.addr file first
	serverAddr, addrErr := managerAddr()

	var jq *jobqueue.Client

	if addrErr == nil { //nolint:nestif
		jq, err = jobqueue.Connect(serverAddr, caFile, config.ManagerCertDomain, token, wait)
		if err == nil {
			return jq
		}

		if shouldWarn {
			warn("failed to connect to manager at address from file (%s): %s, falling back to config address", serverAddr, err)
		}
	}

	// fall back to using the config-defined address
	jq, err = jobqueue.Connect(config.ManagerHost+":"+config.ManagerPort, caFile, config.ManagerCertDomain, token, wait)
	if err != nil && shouldWarn {
		die("%s", err)
	}

	return jq
}
