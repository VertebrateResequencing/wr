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

package jobqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	log15 "github.com/inconshreveable/log15/v3"
	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

// stopSlowCheckWindow is how long TestCompletionDuringStopWithSlowResourceCheck
// lets its manager's shutdown wait for the runner, standing in for
// ShutdownRunnerWait. It is far longer than a runner needs to report a
// finished command, even after waiting checkingReportWait for a stuck resource
// check, and far shorter than checkingFinishTimeout.
const stopSlowCheckWindow = 10 * time.Second

// slowCheckReportMargin is how much longer than checkingReportWait
// TestExecuteWithSlowResourceCheck allows a runner to take to report a finished
// command, for the work it does after the command exits.
const slowCheckReportMargin = 5 * time.Second

// TestCompletionDuringStopWithSlowResourceCheck: a command that exits 0 just
// before its manager is stopped must be recorded as complete while the stop is
// still waiting for runners, even when the runner's resource checking
// goroutine is stuck in a slow read (as a /proc walk can be on a busy node).
// Otherwise the stop finishes without the report, the scheduler kills the
// runner, and the job runs again after the restart.
func TestCompletionDuringStopWithSlowResourceCheck(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond
	serverConfig.dontWipeDevDB = true

	Convey("Given a job that exits 0 while its runner's resource check is stuck", t, func() {
		executed := make(chan struct{})
		inShutdown := make(chan struct{})

		// the manager waits for runners for at most stopSlowCheckWindow, or
		// until Execute has returned, which it does only once it has reported
		shutdownRunnersWaitHook = func() {
			close(inShutdown)

			select {
			case <-executed:
			case <-time.After(stopSlowCheckWindow):
			}
		}

		defer func() { shutdownRunnersWaitHook = nil }()

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		checking := make(chan struct{})
		releaseCheck := make(chan struct{})

		var checkingOnce sync.Once

		jq.processTreeCPUtimeHook = func(int) time.Duration {
			checkingOnce.Do(func() { close(checking) })
			<-releaseCheck

			return 0
		}

		defer close(releaseCheck)

		waited := make(chan struct{})
		jq.afterWaitHook = func() { close(waited) }

		dir := t.TempDir()
		goFile := filepath.Join(dir, "go")
		cwd := filepath.Join(dir, "job")
		So(os.MkdirAll(cwd, 0o700), ShouldBeNil)

		const repGroup = "completion_during_stop_slow_check"

		job := &Job{
			Cmd: "until [ -e " + goFile + " ]; do sleep 0.05; done", Cwd: cwd, CwdMatters: true,
			RepGroup: repGroup, ReqGroup: repGroup,
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
		}

		added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		go func() {
			defer close(executed)

			erre := jq.Execute(ctx, reserved, "/bin/sh")
			if erre != nil {
				t.Logf("Execute: %s", erre)
			}
		}()

		So(closedWithin(checking, stopExitedZeroWait), ShouldBeTrue)
		So(os.WriteFile(goFile, nil, 0o600), ShouldBeNil)
		So(closedWithin(waited, stopExitedZeroWait), ShouldBeTrue)

		stopped := make(chan struct{})

		go func() {
			server.Stop(ctx, true)
			close(stopped)
		}()

		So(closedWithin(inShutdown, stopExitedZeroWait), ShouldBeTrue)
		So(closedWithin(stopped, stopSlowCheckWindow+stopExitedZeroWait), ShouldBeTrue)

		shutdownRunnersWaitHook = nil

		Convey("it is complete after the manager restarts", func() {
			server, _, token, err = serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer server.Stop(ctx, true)

			jq2, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq2)

			jobs, errg := jq2.GetByRepGroup(repGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateComplete)
			So(jobs[0].Exitcode, ShouldEqual, 0)
		})
	})
}

// TestExecuteWithSlowResourceCheck: once a command has exited, its runner
// reports it within about checkingReportWait, even while its resource checking
// goroutine is stuck in a slow read; but when that goroutine is killing the
// command for using too much memory, the report still waits for the kill, so
// that the job is buried for its memory use.
func TestExecuteWithSlowResourceCheck(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond

	Convey("Given a live manager", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		dir := t.TempDir()
		cwd := filepath.Join(dir, "job")
		So(os.MkdirAll(cwd, 0o700), ShouldBeNil)

		addAndReserve := func(repGroup, cmd string) *Job {
			job := &Job{
				Cmd: cmd, Cwd: cwd, CwdMatters: true, RepGroup: repGroup, ReqGroup: repGroup,
				Requirements: &jqs.Requirements{RAM: 1, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
			}

			added, _, erra := jq.Add([]*Job{job}, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(added, ShouldEqual, 1)

			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)

			return reserved
		}

		Convey("a command that exits 0 while a resource check is stuck is reported soon after", func() {
			const repGroup = "slow_check_exit_zero"

			checking := make(chan struct{})
			releaseCheck := make(chan struct{})

			var checkingOnce sync.Once

			jq.processTreeCPUtimeHook = func(int) time.Duration {
				checkingOnce.Do(func() { close(checking) })
				<-releaseCheck

				return 0
			}

			defer close(releaseCheck)

			var waitedAt time.Time

			jq.afterWaitHook = func() { waitedAt = time.Now() }

			goFile := filepath.Join(dir, "go")
			reserved := addAndReserve(repGroup, "until [ -e "+goFile+" ]; do sleep 0.05; done")

			logs := &touchLogCapture{}
			execCtx := clog.ContextWithLogHandler(ctx, logs.handler())
			executed := make(chan error, 1)

			go func() { executed <- jq.Execute(execCtx, reserved, "/bin/sh") }()

			So(closedWithin(checking, stopExitedZeroWait), ShouldBeTrue)
			So(os.WriteFile(goFile, nil, 0o600), ShouldBeNil)

			select {
			case erre := <-executed:
				So(erre, ShouldBeNil)
				So(time.Since(waitedAt), ShouldBeLessThan, checkingReportWait+slowCheckReportMargin)
			case <-time.After(checkingReportWait + slowCheckReportMargin + stopExitedZeroWait):
				So("Execute returned", ShouldBeEmpty)
			}

			jobs, errg := jq.GetByRepGroup(repGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateComplete)

			So(logs.levelsOf(checkingStillReadingMsg), ShouldResemble, []log15.Lvl{log15.LvlInfo})
			So(logs.levelsOf(checkingKillAbandonedMsg), ShouldBeEmpty)
		})

		Convey("a command whose resource checks finish promptly logs no wait for them", func() {
			logs := &touchLogCapture{}
			reserved := addAndReserve("prompt_check_exit_zero", "sleep 0.2")

			So(jq.Execute(clog.ContextWithLogHandler(ctx, logs.handler()), reserved, "/bin/sh"), ShouldBeNil)
			So(logs.levelsOf(checkingStillReadingMsg), ShouldBeEmpty)
			So(logs.levelsOf(checkingKillAbandonedMsg), ShouldBeEmpty)
		})

		Convey("a command killed for its memory use is buried for it, though the kill is slow", func() {
			const repGroup = "slow_check_mem_kill"

			// under LSF, a SIGKILL counts as being for memory only when wr's
			// own kill was
			si := jq.CurrentServerInfo()
			So(si, ShouldNotBeNil)
			si.Scheduler = "lsf"
			jq.adoptServerInfo(si)

			// any memory use over the job's 1MB needs a kill
			jq.percentMemoryKill = 0

			defer func() { jq.percentMemoryKill = ClientPercentMemoryKill }()

			// the kill waits on reading each child's start time, before and
			// after terminating it, so takes far longer than checkingReportWait
			// after the command itself has been killed and waited for
			jq.processStartHook = func(pid int32) (int64, error) {
				time.Sleep(checkingReportWait + time.Second)

				p, errp := process.NewProcess(pid)
				if errp != nil {
					return 0, errp
				}

				return p.CreateTime()
			}

			reserved := addAndReserve(repGroup,
				`perl -e '$SIG{TERM} = "IGNORE"; my $x = "a" x 20_000_000; sleep 30' >/dev/null 2>&1 & wait`)

			execErr := jq.Execute(ctx, reserved, "/bin/sh")

			var jqerr Error

			So(errors.As(execErr, &jqerr), ShouldBeTrue)
			So(jqerr.Err, ShouldEqual, FailReasonRAM)

			jobs, errg := jq.GetByRepGroup(repGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonRAM)
		})
	})
}

// levelsOf returns the level of each record c holds whose message is msg.
func (c *touchLogCapture) levelsOf(msg string) []log15.Lvl {
	c.mu.Lock()
	defer c.mu.Unlock()

	var lvls []log15.Lvl

	for _, rec := range c.records {
		if rec.msg == msg {
			lvls = append(lvls, rec.lvl)
		}
	}

	return lvls
}
