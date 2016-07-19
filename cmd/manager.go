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

package cmd

import (
	"fmt"
	"github.com/sb10/vrpipe/internal"
	"github.com/sb10/vrpipe/jobqueue"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"log"
	"os"
	"runtime"
	"syscall"
	"time"
)

// options for this cmd
var foreground bool
var scheduler string

// managerCmd represents the manager command
var managerCmd = &cobra.Command{
	Use:   "manager",
	Short: "Pipeline manager",
	Long: `The pipeline management system.

The vrpipe manager works in the background, doing all the work of ensuring your
commands get run successfully.

It maintains both a temporary queue of the commands you want to run, and a
permanent history of commands you've run in the past, along with a simple
key/val database that can be used to store result metadata associated with
output files. As commands are added to the queue, it makes sure to spawn
sufficient 'vrpipe runner' agents to get them all run.

You'll need to start this daemon with the 'start' sub-command before you can
achieve anything useful with the other vrpipe commands. If the background
process that is spawned when you run this dies, your pipelines will become
stalled until you run the 'start' sub-command again.

If the manager fails to start or dies unexpectedly, you can check the logs which
are by default found in ~/.vrpipe_[deployment]/log.`,
}

// start sub-command starts the daemon
var managerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start pipeline management",
	Long: `Start the pipeline manager, daemonizing it in to the background
(unless --foreground option is supplied).`,
	Run: func(cmd *cobra.Command, args []string) {
		// first we need our working directory to exist
		_, err := os.Stat(config.Manager_dir)
		if err != nil {
			if os.IsNotExist(err) {
				// try and create the directory
				err = os.MkdirAll(config.Manager_dir, os.ModePerm)
				if err != nil {
					fatal("could not create the working directory '%s': %v", config.Manager_dir, err)
				}
			} else {
				fatal("could not access or create the working directory '%s': %v", config.Manager_dir, err)
			}
		}

		// check to see if the manager is already running (regardless of the
		// state of the pid file), giving us a meaningful error message in the
		// most obvious case of failure to start
		jq := connect(10 * time.Millisecond)
		if jq != nil {
			sstats, err := jq.ServerStats()
			var pid int
			if err == nil {
				pid = sstats.ServerInfo.PID
			}
			fatal("vrpipe manager on port %s is already running (pid %d)", config.Manager_port, pid)
		}

		// now daemonize unless in foreground mode
		if foreground {
			syscall.Umask(config.Manager_umask)
			startJQ(true)
		} else {
			// when we spawn a child it will be called with our args, but we
			// must ensure that the --deployment is correct, since the default
			// for that depends on the dir we are in, and our child is forced to
			// start in the root dir
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

			context := &daemon.Context{
				PidFileName: config.Manager_pid_file,
				PidFilePerm: 0644,
				WorkDir:     "/",
				Args:        args,
				Umask:       config.Manager_umask,
			}
			child, err := context.Reborn()
			if err != nil {
				fatal("failed to daemonize: %s", err)
			}
			if child != nil {
				// parent; wait a while for our child to bring up the manager
				// before exiting
				jq := connect(10 * time.Second)
				if jq == nil {
					fatal("vrpipe manager failed to start on port %s after 10s", config.Manager_port)
				}
				sstats, err := jq.ServerStats()
				if err != nil {
					fatal("vrpipe manager started but doesn't seem to be functional: %s", err)
				}
				logStarted(sstats.ServerInfo)
			} else {
				// daemonized child, that will run until signalled to stop
				defer context.Release()
				startJQ(false)
			}
		}
	},
}

// stop sub-command stops the daemon by sending it a term signal
var managerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop pipeline management",
	Long: `Immediately stop the pipeline manager, saving its state.

Note that any runners that are currently running will die, along with any
commands they were running. It is more graceful to use use 'drain' instead.`,
	Run: func(cmd *cobra.Command, args []string) {
		// the daemon could be running but be non-responsive, or it could have
		// exited but left the pid file in place; to best cover all
		// eventualities we check the pid file first, try and terminate its pid,
		// then confirm we can't connect
		pid, err := daemon.ReadPidFile(config.Manager_pid_file)
		var stopped bool
		if err == nil {
			stopped = stopdaemon(pid, "pid file "+config.Manager_pid_file)
		} else {
			// probably no pid file, we'll see if the daemon is up by trying to
			// connect
			jq := connect(1 * time.Second)
			if jq == nil {
				fatal("vrpipe manager does not seem to be running on port %s", config.Manager_port)
			}
		}

		var jq *jobqueue.Client
		if stopped {
			// we'll do a quick test to confirm the daemon is down
			jq = connect(10 * time.Millisecond)
			if jq != nil {
				warn("according to the pid file %s, vrpipe manager was running with pid %d, and I terminated that pid, but the manager is still up on port %s!", config.Manager_pid_file, pid, config.Manager_port)
			} else {
				info("vrpipe manager running on port %s was gracefully shut down", config.Manager_port)
				return
			}
		} else {
			// we failed to SIGTERM the pid in the pid file, let's take some
			// time to confirm the daemon is really up
			jq = connect(5 * time.Second)
			if jq == nil {
				fatal("according to the pid file %s, vrpipe manager for port %s was running with pid %d, but that process could not be terminated and the manager could not be connected to; most likely the pid file is wrong and the manager is not running - after confirming, delete the pid file before trying to start the manager again", config.Manager_pid_file, config.Manager_port, pid)
			}
		}

		// we managed to connect to the daemon; get it's real pid and try to
		// stop it again
		sstats, err := jq.ServerStats()
		if err != nil {
			fatal("even though I was able to connect to the manager, it failed to tell me its true pid; giving up trying to stop it")
		}
		spid := sstats.ServerInfo.PID
		jq.Disconnect()

		stopped = stopdaemon(spid, "the manager itself")
		if stopped {
			info("vrpipe manager running on port %s was gracefully shut down", config.Manager_port)
		} else {
			info("I've tried everything; giving up trying to stop the manager", config.Manager_port)
		}
	},
}

// drain sub-command makes the server stop spawning new runners and stops it
// letting existing runners reserve jobs, and when there are no more runners
// running it will exit by itself
var managerDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Drain the pipeline manager of running jobs and then stop",
	Long: `Wait for currently running jobs to finish and then gracefully stop the pipeline manager, saving its state.

While draining you can continue to add new Jobs, but nothing new will start
running until the drain completes (or the manager is stopped) and the manager is
then started again.

It is safe to repeat this command to get an update on how long before the drain
completes.`,
	Run: func(cmd *cobra.Command, args []string) {
		// first try and connect
		jq := connect(5 * time.Second)
		if jq == nil {
			fatal("could not connect to the manager on port %s, so could not initiate a drain; has it already been stopped?", config.Manager_port)
		}

		// we managed to connect to the daemon; ask it to go in to drain mode
		numLeft, etc, err := jq.DrainServer()
		if err != nil {
			fatal("even though I was able to connect to the manager, it failed to enter drain mode: %s", err)
		}

		if numLeft == 0 {
			info("vrpipe manager running on port %s is drained: there were no jobs still running, so the manger should stop right away.", config.Manager_port)
		} else if numLeft == 1 {
			info("vrpipe manager running on port %s is now draining; there is a job still running, and it should complete in less than %s", config.Manager_port, numLeft, etc)
		} else {
			info("vrpipe manager running on port %s is now draining; there are %d jobs still running, and they should complete in less than %s", config.Manager_port, numLeft, etc)
		}

		jq.Disconnect()
	},
}

// status sub-command tells if the manger is up or down
// stop sub-command stops the daemon by sending it a term signal
var managerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get status of the pipeline manager",
	Long:  `Find out if the pipeline manager is currently running or not.`,
	Run: func(cmd *cobra.Command, args []string) {
		// see if pid file suggests it is supposed to be running
		pid, err := daemon.ReadPidFile(config.Manager_pid_file)
		if err == nil {
			// confirm
			jq := connect(5 * time.Second)
			if jq != nil {
				reportLiveStatus(jq)
				return
			}

			fatal("vrpipe manager on port %s is supposed to be running with pid %d, but is non-responsive", config.Manager_port, pid)
		}

		// no pid file, so it's supposed to be down; confirm
		jq := connect(10 * time.Millisecond)
		if jq == nil {
			fmt.Println("stopped")
		} else {
			reportLiveStatus(jq)
		}
	},
}

// reportLiveStatus is used by the status command on a working connection to
// distinguish between the server being in a normal 'started' state or the
// 'drain' state.
func reportLiveStatus(jq *jobqueue.Client) {
	sstats, err := jq.ServerStats()
	if err != nil {
		fatal("even though I was able to connect to the manager, it wasn't able to tell me about itself: %s", err)
	}
	mode := sstats.ServerInfo.Mode
	fmt.Println(mode)
}

func init() {
	RootCmd.AddCommand(managerCmd)
	managerCmd.AddCommand(managerStartCmd)
	managerCmd.AddCommand(managerDrainCmd)
	managerCmd.AddCommand(managerStopCmd)
	managerCmd.AddCommand(managerStatusCmd)

	// flags specific to these sub-commands
	managerStartCmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "do not daemonize")
	managerStartCmd.Flags().StringVarP(&scheduler, "scheduler", "s", internal.DefaultScheduler(), "['local','lsf'] job scheduler")
}

func connect(wait time.Duration) *jobqueue.Client {
	jq, jqerr := jobqueue.Connect("localhost:"+config.Manager_port, "test_queue", wait)
	if jqerr == nil {
		return jq
	}
	return nil
}

func stopdaemon(pid int, source string) bool {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err != nil {
		warn("vrpipe manager is running with pid %d according to %s, but failed to send it SIGTERM: %s", pid, source, err)
		return false
	}

	// wait a while for the daemon to gracefully close down
	giveupseconds := 15
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
		warn("vrpipe manager, running with pid %d according to %s, is still running %ds after I sent it a SIGTERM", pid, source, giveupseconds)
	}

	return ok
}

// get a nice address to report in logs, preferring hostname, falling back
// on the ip address if that wasn't set
func sAddr(s *jobqueue.ServerInfo) (addr string) {
	addr = s.Host
	if addr == "localhost" {
		addr = s.Addr
	} else {
		addr += ":" + s.Port
	}
	return
}

func logStarted(s *jobqueue.ServerInfo) {
	info("vrpipe manager started on %s, pid %d", sAddr(s), s.PID)
}

func startJQ(sayStarted bool) {
	runtime.GOMAXPROCS(runtime.NumCPU())

	// start the jobqueue server
	server, msg, err := jobqueue.Serve(config.Manager_port, config.Manager_web, scheduler, config.Runner_exec_shell, "vrpipe runner -q %s -s '%s' --deployment %s --server '%s' -r %d", config.Manager_db_file, config.Manager_db_bk_file, config.Deployment)

	if sayStarted && err == nil {
		logStarted(server.ServerInfo)
	}

	// start logging to configured file
	logfile, errlog := os.OpenFile(config.Manager_log_file, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if errlog != nil {
		warn("could not log to %s, will log to STDOUT: %v", config.Manager_log_file, errlog)
	} else {
		defer logfile.Close()
		log.SetOutput(logfile)
	}

	// log to file failure to Serve
	if err != nil {
		if msg != "" {
			log.Printf("vrpipe manager : %s\n", msg)
		}
		log.Printf("vrpipe manager failed to start : %s\n", err)
		os.Exit(1)
	}

	// log to file that we started
	addr := sAddr(server.ServerInfo)
	log.Printf("vrpipe manager started on %s\n", addr)
	if msg != "" {
		log.Printf("vrpipe manager : %s\n", msg)
	}

	// block forever while the jobqueue does its work
	err = server.Block()
	if err != nil {
		jqerr, ok := err.(jobqueue.Error)
		switch {
		case ok && jqerr.Err == jobqueue.ErrClosedTerm:
			log.Printf("vrpipe manager on %s gracefully stopped (received SIGTERM)\n", addr)
		case ok && jqerr.Err == jobqueue.ErrClosedInt:
			log.Printf("vrpipe manager on %s gracefully stopped (received SIGINT)\n", addr)
		case ok && jqerr.Err == jobqueue.ErrClosedStop:
			log.Printf("vrpipe manager on %s gracefully stopped (following a drain)\n", addr)
		default:
			log.Printf("vrpipe manager on %s exited unexpectedly: %s\n", addr, err)
		}
	}
}
