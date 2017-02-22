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

// Package cmd is the cobra file that enables subcommands and handles
// command-line args
package cmd

import (
	"fmt"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"os"
	"os/user"
	"syscall"
	"time"
)

// these variables are accessible by all subcommands.
var deployment string
var config internal.Config

// these are shared by some of the subcommands.
var addr string
var timeoutint int
var cmdCwd string

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

Then you either directly add commands you want to run to the queue:
$ wr add

Or you define a workflow that works out the commands for you:
Create a workflow with:                           $ wr create
Define a datasource with:                         $ wr datasource
Set up an instance of workflow + datasource with: $ wr setup
[create, datasource and setup commands are not yet implemented; just use add for
now]

At this point your commands should be running, and you can monitor their
progress with:
$ wr status

Finally, you can find your output files with:
$ wr outputs`,
}

// Execute adds all child commands to the root command and sets flags
// appropriately. This is called by main.main(). It only needs to happen once to
// the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
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
	addr = config.ManagerHost + ":" + config.ManagerPort
}

// realUsername returns the username of the current user.
func realUsername() string {
	self, err := user.Current()
	if err != nil {
		die("could not get username: %s", err)
	}
	return self.Username
}

// cloudResourceName returns a user and deployment specific string that can be
// used to name cloud resources so they can be identified as having been created
// by wr. username arg defaults to the real username of the user running wr.
func cloudResourceName(username string) string {
	if username == "" {
		username = realUsername()
	}
	return "wr-" + config.Deployment + "-" + username
}

// info is a convenience to print a msg to STDOUT.
func info(msg string, a ...interface{}) {
	fmt.Fprintf(os.Stdout, "info: %s\n", fmt.Sprintf(msg, a...))
}

// warn is a convenience to print a msg to STDERR.
func warn(msg string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "warning: %s\n", fmt.Sprintf(msg, a...))
}

// die is a convenience to print an error to STDERR and exit indicating error.
func die(msg string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: %s\n", fmt.Sprintf(msg, a...))
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
func daemonize(pidFile string, umask int, extraArgs ...string) (child *os.Process, context *daemon.Context) {
	args := os.Args
	hadDeployment := false
	for _, arg := range args {
		if arg == "--deployment" {
			hadDeployment = true
			break
		}
	}
	if !hadDeployment {
		args = append(args, "--deployment")
		args = append(args, config.Deployment)
	}

	for _, extra := range extraArgs {
		args = append(args, extra)
	}

	context = &daemon.Context{
		PidFileName: pidFile,
		PidFilePerm: 0644,
		WorkDir:     "/",
		Args:        args,
		Umask:       umask,
	}

	var err error
	child, err = context.Reborn()
	if err != nil {
		die("failed to daemonize: %s", err)
	}
	return
}

// stopdaemon stops the daemon created by daemonize() by sending it SIGTERM and
// checking it really exited
func stopdaemon(pid int, source string, name string) bool {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err != nil {
		warn("wr %s is running with pid %d according to %s, but failed to send it SIGTERM: %s", name, pid, source, err)
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
		warn("wr %s, running with pid %d according to %s, is still running %ds after I sent it a SIGTERM", name, pid, source, giveupseconds)
	}

	return ok
}

// sAddr gets a nice manager address to report in logs, preferring hostname,
// falling back on the ip address if that wasn't set
func sAddr(s *jobqueue.ServerInfo) (addr string) {
	addr = s.Host
	if addr == "localhost" {
		addr = s.Addr
	} else {
		addr += ":" + s.Port
	}
	return
}

// connect gives you a client connected to a queue that shouldn't be used; use
// the client just for calling non-queue-specific methods such as getting
// server status or shutting it down etc.
func connect(wait time.Duration) *jobqueue.Client {
	jq, jqerr := jobqueue.Connect("localhost:"+config.ManagerPort, "test_queue", wait)
	if jqerr == nil {
		return jq
	}
	return nil
}
