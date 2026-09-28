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
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// stopExitedZeroWait bounds each wait in TestExitedZeroDuringStopIsComplete.
const stopExitedZeroWait = 20 * time.Second

// errStopExitedZeroTimeout is what TestExitedZeroDuringStopIsComplete records
// when Execute has not returned in time.
var errStopExitedZeroTimeout = errors.New("Execute did not return")

// TestExitedZeroDuringStopIsComplete: a command that exits 0 while its manager
// is being stopped must be recorded as complete, even though the stop's kill
// reaches the runner after the command has exited but before the runner has
// finished waiting for it, and so kills a process that has already exited.
func TestExitedZeroDuringStopIsComplete(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond
	serverConfig.dontWipeDevDB = true

	Convey("Given a job whose command has exited 0 when its manager starts stopping", t, func() {
		inShutdown := make(chan struct{})
		releaseShutdown := make(chan struct{})

		shutdownRunnersWaitHook = func() {
			close(inShutdown)
			<-releaseShutdown
		}

		defer func() { shutdownRunnersWaitHook = nil }()

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		var kills atomic.Int32

		jq.beforeServerKillHook = func() { kills.Add(1) }

		dir := t.TempDir()
		exited := filepath.Join(dir, "exited")
		cwd := filepath.Join(dir, "job")
		So(os.MkdirAll(cwd, 0o700), ShouldBeNil)

		const repGroup = "exited_zero_during_stop"

		// the backgrounded sleep keeps the command's stdout open after the
		// command itself has exited 0, so the runner has not yet waited for the
		// command when the stop's kill arrives (the kill then ends the sleep).
		job := &Job{
			Cmd: "touch " + exited + "; (sleep 2 &); exit 0", Cwd: cwd, CwdMatters: true,
			RepGroup: repGroup, ReqGroup: repGroup,
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
		}

		added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		executed := make(chan error, 1)

		go func() { executed <- jq.Execute(ctx, reserved, "/bin/sh") }()

		So(fileAppears(exited, stopExitedZeroWait), ShouldBeTrue)

		stopped := make(chan struct{})

		go func() {
			server.Stop(ctx, true)
			close(stopped)
		}()

		reachedShutdown := false

		select {
		case <-inShutdown:
			reachedShutdown = true
		case <-time.After(stopExitedZeroWait):
		}

		// without this the rest could pass without ever exercising a command
		// that exits 0 while the shutdown is in progress
		So(reachedShutdown, ShouldBeTrue)

		Convey("it is recorded as complete, and is still complete after a restart", func() {
			var execErr error

			select {
			case execErr = <-executed:
			case <-time.After(stopExitedZeroWait):
				execErr = errStopExitedZeroTimeout
			}

			close(releaseShutdown)
			<-stopped

			shutdownRunnersWaitHook = nil

			So(kills.Load(), ShouldBeGreaterThan, 0)
			So(execErr, ShouldBeNil)

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
