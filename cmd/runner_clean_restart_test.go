/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/internal/testcerts"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// cleanRestartRunnerBound is how long TestRunnerOutlivesCleanRestart lets the
// runner take to finish once its command can exit.
const cleanRestartRunnerBound = 30 * time.Second

// cleanRestartRepGroup is the rep group of TestRunnerOutlivesCleanRestart's job.
const cleanRestartRepGroup = "clean-restart"

// cleanRestartShell and cleanRestartScheduler are how
// TestRunnerOutlivesCleanRestart's manager runs its job.
const (
	cleanRestartShell     = "bash"
	cleanRestartScheduler = "local"
)

// cleanRestartManager is a manager TestRunnerOutlivesCleanRestart can stop
// cleanly, as `wr manager stop` would, and start again on the same database.
type cleanRestartManager struct {
	t         *testing.T
	serverCfg jobqueue.ServerConfig
	server    *jobqueue.Server
	token     []byte
}

// newCleanRestartManager points config at a fresh deployment directory and
// returns a manager for it, not yet started. Its touch interval is longer than
// the test, so its runner never touches, and so never learns of a stop.
func newCleanRestartManager(t *testing.T) *cleanRestartManager {
	t.Helper()

	setManagerStopTestConfigPidFile(t, "")

	dir := config.ManagerDir
	config.ManagerCertFile = filepath.Join(dir, "cert.pem")
	config.ManagerKeyFile = filepath.Join(dir, "key.pem")
	config.ManagerCertDomain = stopHelperDomain
	config.ManagerDBFile = filepath.Join(dir, "db")
	config.RunnerExecShell = cleanRestartShell

	So(testcerts.Write(config.ManagerCAFile, config.ManagerCertFile, config.ManagerKeyFile,
		stopHelperDomain), ShouldBeNil)

	return &cleanRestartManager{
		t: t,
		serverCfg: jobqueue.ServerConfig{
			Port:            config.ManagerPort,
			SchedulerName:   cleanRestartScheduler,
			SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
			DBFile:          config.ManagerDBFile,
			DBFileBackup:    config.ManagerDBFile + "_bk",
			TokenFile:       config.ManagerTokenFile,
			CAFile:          config.ManagerCAFile,
			CertFile:        config.ManagerCertFile,
			KeyFile:         config.ManagerKeyFile,
			CertDomain:      stopHelperDomain,
			Deployment:      internal.Production,
			Timings: jobqueue.ServerTimings{
				TouchInterval:      time.Hour,
				ItemTTR:            2 * time.Hour,
				RetryWait:          100 * time.Millisecond,
				ShutdownSocketWait: time.Millisecond,
			},
		},
	}
}

// start serves the manager, and stops it when the test ends.
func (m *cleanRestartManager) start() {
	server, _, token, err := jobqueue.Serve(context.Background(), m.serverCfg)
	So(err, ShouldBeNil)

	<-server.Serving()

	m.server, m.token = server, token

	m.t.Cleanup(func() { server.Stop(context.Background(), true) })
}

// cleanStop stops the manager and deletes its token, as `wr manager stop` does.
func (m *cleanRestartManager) cleanStop() {
	m.server.Stop(context.Background(), true)

	So(os.Remove(config.ManagerTokenFile), ShouldBeNil)
}

// client connects to the manager as a user would.
func (m *cleanRestartManager) client() *jobqueue.Client {
	jq, err := jobqueue.Connect("localhost:"+config.ManagerPort, config.ManagerCAFile, stopHelperDomain,
		m.token, 10*time.Second)
	So(err, ShouldBeNil)

	m.t.Cleanup(func() { jq.Disconnect() }) //nolint:errcheck

	return jq
}

// jobState returns the state of the test's one job.
func (m *cleanRestartManager) jobState() jobqueue.JobState {
	jobs, err := m.client().GetByRepGroup(cleanRestartRepGroup, false, 0, "", false, false)
	if err != nil || len(jobs) != 1 {
		return ""
	}

	return jobs[0].State
}

func TestRunnerOutlivesCleanRestart(t *testing.T) {
	Convey("Given a runner whose command is still running when its manager is cleanly restarted", t, func() {
		manager := newCleanRestartManager(t)
		manager.start()

		release := filepath.Join(t.TempDir(), "release")

		_, _, err := manager.client().Add([]*jobqueue.Job{{
			Cmd:          "while [ ! -e " + release + " ]; do sleep 0.1; done",
			Cwd:          t.TempDir(),
			RepGroup:     cleanRestartRepGroup,
			ReqGroup:     cleanRestartRepGroup,
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Hour, Cores: 1, Other: map[string]string{}},
		}}, os.Environ(), true)
		So(err, ShouldBeNil)

		exited := startTestRunner(t)

		So(pollUntilTrue(func() bool { return manager.jobState() == jobqueue.JobStateRunning }), ShouldBeTrue)

		manager.cleanStop()

		Convey("its command exiting 0 is recorded as complete by the new manager", func() {
			manager.start()

			So(os.WriteFile(release, nil, 0o600), ShouldBeNil)

			exitCode := -1

			select {
			case exitCode = <-exited:
			case <-time.After(cleanRestartRunnerBound):
			}

			So(exitCode, ShouldEqual, 0)
			So(manager.jobState(), ShouldEqual, jobqueue.JobStateComplete)
		})

		Convey("if it cannot read the new manager's token, it gives up and exits instead of retrying for a day", func() {
			manager.serverCfg.TokenFile = filepath.Join(t.TempDir(), "elsewhere.token")
			manager.start()

			So(os.WriteFile(release, nil, 0o600), ShouldBeNil)

			exitCode := -1

			select {
			case exitCode = <-exited:
			case <-time.After(cleanRestartRunnerBound):
			}

			So(exitCode, ShouldEqual, 1)
		})
	})
}

// startTestRunner runs `wr runner` in the background against the manager,
// returning a channel that gets its exit code when it exits.
func startTestRunner(t *testing.T) <-chan int {
	t.Helper()

	origCmdExit := cmdExit
	origServer, origDomain, origGrp := rserver, rdomain, schedgrp
	origReserve, origTimeout, origMax := reserveint, timeoutintRunner, maxtime

	cmdExit = func(code int) { panic(commandExitPanic{code: code}) }
	rserver, rdomain, schedgrp = "localhost:"+config.ManagerPort, stopHelperDomain, ""
	reserveint, timeoutintRunner, maxtime = 1, 10, 0

	exited := make(chan int, 1)

	go func() {
		exited <- recoverCommandExit(func() { runnerCmd.Run(runnerCmd, nil) })
	}()

	t.Cleanup(func() {
		cmdExit = origCmdExit
		rserver, rdomain, schedgrp = origServer, origDomain, origGrp
		reserveint, timeoutintRunner, maxtime = origReserve, origTimeout, origMax
	})

	return exited
}
