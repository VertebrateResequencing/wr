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

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// killTestStartWait bounds how long a kill test waits for its command to be
// reported started; it is free on success.
const killTestStartWait = 20 * time.Second

// killTestPollInterval is how often a kill test polls the manager.
const killTestPollInterval = 10 * time.Millisecond

// killOnceStarted waits, for up to killTestStartWait, until the manager has
// recorded the start of job's command, then kills job, returning how many jobs
// were killed. Since Execute reports the start only after starting the command,
// the kill can only reach the runner once there is a command to kill. The job's
// pid is no sign of that: a reservation already records the runner's own pid.
func killOnceStarted(jq *Client, job *Job) int {
	deadline := time.Now().Add(killTestStartWait)

	for time.Now().Before(deadline) {
		got, err := jq.GetByRepGroup(job.RepGroup, false, 0, JobStateRunning, false, false)
		if err == nil && len(got) == 1 && !got[0].StartTime.IsZero() {
			killed, errk := jq.Kill([]*JobEssence{{JobKey: job.Key()}})
			if errk != nil {
				return 0
			}

			return killed
		}

		time.Sleep(killTestPollInterval)
	}

	return 0
}

// killHelperStartDelay is how long TestKillOnceStartedWaitsForTheStart leaves
// its job reserved before reporting it started.
const killHelperStartDelay = 500 * time.Millisecond

// TestKillOnceStartedWaitsForTheStart: the kill tests rely on killOnceStarted
// killing only once the command has started, so it must not kill a job that is
// merely reserved, although the manager already knows a pid for it then.
func TestKillOnceStartedWaitsForTheStart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a live manager and a reserved job", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const repGroup = "kill_helper_waits"

		job := &Job{
			Cmd: "sleep 30", Cwd: t.TempDir(), RepGroup: repGroup, ReqGroup: repGroup,
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
		}

		added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		Convey("killOnceStarted kills it only once it has been reported started", func() {
			killedAt := make(chan time.Time, 1)

			go func() {
				killOnceStarted(jq, reserved)

				killedAt <- time.Now()
			}()

			time.Sleep(killHelperStartDelay)

			startedAt := time.Now()

			So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

			So((<-killedAt).After(startedAt), ShouldBeTrue)
		})
	})
}
