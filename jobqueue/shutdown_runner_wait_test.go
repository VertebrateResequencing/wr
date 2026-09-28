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
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// shutdownRunnerWaitTestBound is how long TestShutdownRunnerWaitIsBounded lets a
// shutdown take before deciding it is waiting forever.
const shutdownRunnerWaitTestBound = 30 * time.Second

// TestShutdownRunnerWaitIsBounded stops a manager whose one runner reserves a
// job and then never exits, as a runner whose command survives the kill does,
// so the scheduler reports it busy for ever. The shutdown must still finish, and
// finish cleanly: freelist synced, final backup written, database closed.
func TestShutdownRunnerWaitIsBounded(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	runnerStarted := make(chan struct{}, 1)
	testDone := make(chan struct{})

	defer close(testDone)

	// the runner reserves and starts a job, then never exits
	serverConfig.SchedulerName = schedulerNameMock
	serverConfig.RunnerCmd = mockRunnerCmd
	serverConfig.SchedulerConfig = &jqs.ConfigMock{
		RunnerFunc: func(_ context.Context, cmd string) {
			if startStuckRunnerJob(config, addr, serverConfig.TokenFile, cmd) {
				runnerStarted <- struct{}{}
			}

			<-testDone
		},
	}
	serverConfig.Timings.TouchInterval = 100 * time.Millisecond
	serverConfig.Timings.ShutdownRunnerWait = time.Second
	serverConfig.forceBackups = true

	Convey("Given a manager with a runner that never exits and a job it holds", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		_, _, err = jq.Add([]*Job{{
			Cmd: "echo never finishes", Cwd: testCwd, RepGroup: "runner_wait",
			ReqGroup: "runner_wait", Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)

		started := false

		select {
		case <-runnerStarted:
			started = true
		case <-time.After(10 * time.Second):
		}

		So(started, ShouldBeTrue)
		So(server.HasRunners(ctx), ShouldBeTrue)

		Convey("stopping it finishes the clean shutdown anyway, saying how many were still running", func() {
			logs := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			stopped := make(chan struct{})

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			finished := false

			select {
			case <-stopped:
				finished = true
			case <-time.After(shutdownRunnerWaitTestBound):
			}

			So(finished, ShouldBeTrue)
			So(logs.String(), ShouldContainSubstring, "gave up waiting for runners to exit")
			So(logs.String(), ShouldContainSubstring, "runningJobs=1")
			So(boltFreelistSynced(serverConfig.DBFile), ShouldBeTrue)

			_, err = os.Stat(serverConfig.DBFileBackup)
			So(err, ShouldBeNil)
		})
	})
}

// startStuckRunnerJob does what a runner does up to running a command: it
// reserves a job in the scheduler group of the runner command cmd and reports
// it started. It reports whether it got that far.
func startStuckRunnerJob(config internal.Config, addr, tokenFile, cmd string) bool {
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return false
	}

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, 10*time.Second)
	if err != nil {
		return false
	}

	// the job stays running on the manager after this client goes: it is the
	// scheduler's runner that never exits, not this connection
	defer disconnect(jq)

	job, err := jq.ReserveScheduled(10*time.Second, fakeRunnerSchedGrp(cmd))
	if err != nil || job == nil {
		return false
	}

	return jq.Started(job, os.Getpid()) == nil
}
