/*******************************************************************************
 * Copyright (c) 2016-2019, 2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

import (
	"context"
	"errors"
	"fmt"
	"log/syslog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/inconshreveable/log15/v3"
	"github.com/kardianos/osext"
	"github.com/spf13/cobra"
)

const logDirPerm = 0o770

// jobLineFixedArgs is how many key-value args logJobLine always logs (the job's
// key and its bounded command line).
const jobLineFixedArgs = 4

// runnerMinProcs is the number of OS threads the runner forces when there is
// only a single CPU, so that we don't lock up if we mount.
const runnerMinProcs = 2

// runnerTimeoutBufferSeconds is the minimum number of seconds by which the
// server receive timeout must exceed the Reserve() timeout.
const runnerTimeoutBufferSeconds = 5

// hostPortParts is the number of colon-separated parts in a valid host:port
// string.
const hostPortParts = 2

// runner flag defaults.
const (
	runnerConnectTimeout = 30
	runnerReserveTimeout = 2
)

// options for this cmd.
var (
	schedgrp         string
	timeoutintRunner int
	reserveint       int
	rserver          string
	rdomain          string
	maxtime          int
	logToSyslog      bool
	logToDir         string
)

// runnerCmd represents the runner command.
var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Run queued commands",
	Long: `A runner runs commands that were queued by the add or setup commands.

You won't normally run this yourself directly - "wr manager" spawns these as
needed.

A runner will pick up a queued command and run it. Once that cmd completes, the
runner will pick up another and so on. Once max_time has been used (or would be
used based on the expected time to complete of the next queued command), the
runner stops picking up new commands and exits instead; max_time does not cause
the runner to kill itself if the cmd it is running takes longer than max_time to
complete.`,
	Run: func(_ *cobra.Command, _ []string) {
		if runtime.NumCPU() == 1 {
			// we might lock up with only 1 proc if we mount
			runtime.GOMAXPROCS(runnerMinProcs)
		}

		if logToSyslog {
			handler, err := log15.SyslogHandler(syslog.LOG_USER, "wrrunner", log15.LogfmtFormat())
			if err != nil {
				warn("failed to set up syslog logging: %s", err)
			} else {
				clog.ToHandlerAtLevel(handler, "info")
			}
		} else if logToDir != "" {
			logDir := filepath.Join(logToDir, time.Now().Format("06.01.02"))

			err := os.MkdirAll(logDir, logDirPerm)
			if err != nil {
				warn("failed to create log file dir, logging disabled: %s", err)
			} else {
				host, err := os.Hostname()
				if err != nil {
					host = "unknown"
				}

				logPath := filepath.Join(logDir, fmt.Sprintf("%s.%s.%d",
					time.Now().Format("15-04-05"), host, os.Getpid()))

				handler, err := clog.CreateFileHandlerAtLevel(logPath, "info")
				if err != nil {
					warn("failed to set up file logging: %s", err)
				} else {
					clog.ToHandlerAtLevel(handler, "info")
				}
			}
		}

		extraStartInfo := ""

		lsfJobID := os.Getenv("LSB_JOBID")
		if lsfJobID != "" {
			lsfJobIndex := os.Getenv("LSB_JOBINDEX")

			indexStr := ""
			if lsfJobIndex != "" {
				indexStr = fmt.Sprintf("[%s]", lsfJobIndex)
			}

			extraStartInfo = fmt.Sprintf("; LSF job id %s%s", lsfJobID, indexStr)
		}

		info("wr runner started for scheduler group '%s'; pid: %d%s", schedgrp, os.Getpid(), extraStartInfo)

		// the server receive timeout must be greater than the time we'll wait
		// to Reserve()
		if timeoutintRunner < (reserveint + runnerTimeoutBufferSeconds) {
			timeoutintRunner = reserveint + runnerTimeoutBufferSeconds
		}

		timeout := time.Duration(timeoutintRunner) * time.Second
		rtimeout := time.Duration(reserveint) * time.Second

		jobqueue.AppName = "wr"

		token, err := token()
		if err != nil {
			die("%s", err)
		}

		jq, err := jobqueue.Connect(rserver, caFile, rdomain, token, timeout)
		if err != nil {
			die("%s", err)
		}
		defer func() {
			err = jq.Disconnect()
			if err != nil {
				warn("Disconnecting from the server failed: %s", err)
			}
		}()

		// tell the server which scheduler element (e.g. LSF "jobid[index]") this
		// runner is, so a job it reserves is never killed as excess mid-job. Empty
		// when not running under a recognised scheduler.
		jq.SetReserveSchedulerID(reserveSchedulerID())

		// in case any job we execute has a Cmd that calls `wr add`, we will
		// override their environment to make that call work
		var (
			envOverrides []string
			exePath      string
		)

		if rserver != "" {
			hostPort := strings.Split(rserver, ":")
			if len(hostPort) == hostPortParts {
				envOverrides = append(envOverrides, "WR_MANAGERHOST="+hostPort[0])
				envOverrides = append(envOverrides, "WR_MANAGERPORT="+hostPort[1])
			}

			envOverrides = append(envOverrides, "WR_MANAGERCERTDOMAIN="+rdomain)

			// later we will add our own wr exe to the path if not there
			exe, err := osext.Executable()
			if err != nil {
				die("%s", err)
			}

			exePath = filepath.Dir(exe)
		}

		// we'll stop the below loop before using up too much time
		var endTime time.Time
		if maxtime > 0 {
			endTime = time.Now().Add(time.Duration(maxtime) * time.Minute)
		} else {
			endTime = time.Now().AddDate(1, 0, 0) // default to allowing us a year to run
		}

		// loop, reserving and running commands from the queue, until there
		// aren't any more commands in the queue
		numrun := 0
		exitReason := fmt.Sprintf("there are no more commands in scheduler group '%s'", schedgrp)

		var jobTime time.Duration
		for {
			// see if we have enough time to run a new job before we should
			// exit
			if time.Now().Add(jobTime).After(endTime) {
				exitReason = "we're about to hit our maximum time limit"

				break
			}

			var (
				job *jobqueue.Job
				err error
			)
			if schedgrp == "" {
				job, err = jq.Reserve(rtimeout)
			} else {
				job, err = jq.ReserveScheduled(rtimeout, schedgrp)
			}

			if err != nil {
				die("%s", err)
			}

			if job == nil {
				break
			}

			logJobLine(context.Background(), "reserved a job", job, "attempts", job.Attempts)

			if job.Requirements.Time != jobTime {
				// confirm we have enough time left to run this
				jobTime = job.Requirements.Time
				if time.Now().Add(jobTime).After(endTime) {
					err = jq.Release(job, nil, "not enough time to run")
					if err != nil {
						// oh well?
						warn("job release after running out of time failed: %s", err)
					}

					exitReason = "we're about to hit our maximum time limit"

					break
				}
			}

			// actually run the cmd
			if len(envOverrides) > 0 {
				// add exePath to this job's PATH
				env, erre := job.Env()
				if erre != nil {
					err = jq.Release(job, nil, "failed to read job's Env")
					if err != nil {
						warn("job release after Env() fail: %s", erre)
					}

					exitReason = "Env failed"

					break
				}

				for _, envvar := range env {
					pair := strings.Split(envvar, "=")
					if pair[0] == "PATH" {
						if !strings.Contains(pair[1], exePath) {
							envOverrides = append(envOverrides, envvar+":"+exePath)
						}

						break
					}
				}

				err = job.EnvAddOverride(envOverrides)
				if err != nil {
					err = jq.Release(job, nil, "failed to add env var overrides")
					if err != nil {
						// oh well?
						warn("job release after envaddoverride fail: %s", err)
					}

					exitReason = "EnvAddOverride failed"

					break
				}
			}

			logJobLine(context.Background(), "will start executing", job)
			err = jq.Execute(context.Background(), job, config.RunnerExecShell)
			numrun++

			if err != nil {
				warn("%s", err)

				// Keep this as a direct assertion: wrapped jobqueue errors must not change runner control flow.
				var jqerr jobqueue.Error
				if errors.As(err, &jqerr) {
					if strings.Contains(jqerr.Err, jobqueue.FailReasonSignal) {
						exitReason = "we received a signal to stop"

						break
					} else if strings.Contains(jqerr.Err, jobqueue.ErrStopReserving) {
						exitReason = "we reconnected to a new server"

						break
					}
				}
			} else {
				logJobLine(context.Background(), "command ran OK", job, "exitcode", job.Exitcode)
			}
		}

		info("wr runner exiting, having run %d commands, because %s", numrun, exitReason)
	},
}

// logJobLine logs msg about job at info level, always naming the job's key and a
// BOUNDED rendering of its command line, plus any extra key-value pairs.
//
// The runner logs its job's command line on every reservation, every start and
// every success, and the command line is entirely user-supplied: production
// measured a p99 of 24,261 bytes and a maximum of 1,345,498 bytes for a single
// "reserved a job" line, and the same job's Cmd was written out again by the two
// other lines. 0d22eda cut the multiplier (an unrunnable job is attempted once
// rather than forever) but not the per-line size, which is what this does.
//
// The key is what keeps the abbreviation free of cost to an operator: `wr status`
// yields the whole command line for it. internal.Abbreviate is presentation only,
// so the command actually executed is unaffected.
func logJobLine(ctx context.Context, msg string, job *jobqueue.Job, extra ...any) {
	args := make([]any, 0, len(extra)+jobLineFixedArgs)
	args = append(args, "key", job.Key(), "cmd", internal.Abbreviate(job.Cmd))
	args = append(args, extra...)

	clog.Info(ctx, msg, args...)
}

// reserveSchedulerID returns this runner's scheduler element id in the
// "jobid[index]" form that matches the scheduler's killable id, so the server
// can mark the element reserved and never kill it as excess. Currently only LSF
// is supported (via LSB_JOBID, with [LSB_JOBINDEX] appended when set); it
// returns "" when not running under LSF.
func reserveSchedulerID() string {
	jobID := os.Getenv("LSB_JOBID")
	if jobID == "" {
		return ""
	}

	if index := os.Getenv("LSB_JOBINDEX"); index != "" {
		return fmt.Sprintf("%s[%s]", jobID, index)
	}

	return jobID
}

func init() {
	ctx := context.Background()

	RootCmd.AddCommand(runnerCmd)

	// flags specific to this sub-command
	runnerCmd.Flags().StringVarP(&schedgrp, "scheduler_group", "s", "",
		"specify the scheduler group to limit which commands can be acted on")
	runnerCmd.Flags().IntVar(&timeoutintRunner, "timeout", runnerConnectTimeout,
		"how long (seconds) to wait to get a reply from 'wr manager'")
	runnerCmd.Flags().IntVarP(&reserveint, "reserve_timeout", "r", runnerReserveTimeout,
		"how long (seconds) to wait for there to be a command in the queue, before exiting")
	runnerCmd.Flags().IntVarP(&maxtime, "max_time", "m", 0,
		"maximum time (minutes) to run for before exiting; 0 means unlimited")
	runnerCmd.Flags().StringVar(&rserver, "server", internal.DefaultServer(ctx), "ip:port of wr manager")
	runnerCmd.Flags().StringVar(&rdomain, "domain", internal.DefaultConfig(ctx).ManagerCertDomain,
		"domain the manager's cert is valid for")
	runnerCmd.Flags().BoolVar(&logToSyslog, "syslog", false, "enable logging to syslog")
	runnerCmd.Flags().StringVar(&logToDir, "logdir", "", "enable logging to files within the given dir")
}
