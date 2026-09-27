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
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

// killWindDownTTR is the manager's TTR in the kill wind-down tests, and
// killWindDownStall how long each keeps its runner busy after the kill: far
// longer than the TTR, as a slow unmount, upload or CPU-starved runner can be.
const (
	killWindDownTTR   = time.Second
	killWindDownStall = 3 * killWindDownTTR
)

// TestKilledJobOutlivingTTRIsBuriedAsKilled: once a runner has acted on a kill,
// it may take longer than the manager's TTR to finish up and report, whether it
// is still before the command started or after it was killed. The manager must
// not take the job's reservation away as lost in the meantime: the runner's own
// report must be what buries the job, as killed. Nor must the kill itself,
// which can be slow (it waits for the command's children to die), stop the
// runner touching the job.
func TestKilledJobOutlivingTTRIsBuriedAsKilled(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond
	serverConfig.Timings.ItemTTR = killWindDownTTR

	Convey("Given a live manager with a short TTR", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		cwd := filepath.Join(t.TempDir(), "job")
		So(os.MkdirAll(cwd, 0o700), ShouldBeNil)

		addAndReserve := func(repGroup string, cmd string, monitorDocker string) *Job {
			job := &Job{
				Cmd: cmd, Cwd: cwd, CwdMatters: true, MonitorDocker: monitorDocker,
				RepGroup: repGroup, ReqGroup: repGroup,
				Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
			}

			added, _, erra := jq.Add([]*Job{job}, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(added, ShouldEqual, 1)

			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)

			return reserved
		}

		soBuriedAsKilled := func(repGroup string, execErr error) {
			var jqerr Error

			So(errors.As(execErr, &jqerr), ShouldBeTrue)
			So(jqerr.Err, ShouldEqual, FailReasonKilled)

			jobs, errg := jq.GetByRepGroup(repGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonKilled)
		}

		Convey("a job killed while running, whose runner is slow after the kill, is buried as killed", func() {
			const repGroup = "kill_wind_down_running"

			reserved := addAndReserve(repGroup, "sleep 30", "")

			jq.afterWaitHook = func() { time.Sleep(killWindDownStall) }

			killed := make(chan int, 1)

			go func() { killed <- killOnceStarted(jq, reserved) }()

			execErr := jq.Execute(ctx, reserved, "/bin/sh")

			So(<-killed, ShouldEqual, 1)

			soBuriedAsKilled(repGroup, execErr)
		})

		Convey("a job whose kill itself is slow is buried as killed", func() {
			const repGroup = "kill_wind_down_slow_kill"

			reserved := addAndReserve(repGroup, "sleep 30", "")

			jq.childProcessesHook = func(pid int32) ([]*process.Process, error) {
				time.Sleep(killWindDownStall)

				return getChildProcesses(pid)
			}

			killed := make(chan int, 1)

			go func() { killed <- killOnceStarted(jq, reserved) }()

			execErr := jq.Execute(ctx, reserved, "/bin/sh")

			So(<-killed, ShouldEqual, 1)

			soBuriedAsKilled(repGroup, execErr)
		})

		Convey("a job killed before its command starts, whose runner is slow to get there, is buried as killed", func() {
			const repGroup = "kill_wind_down_unstarted"

			release := make(chan struct{})
			t.Setenv("DOCKER_HOST", gatedDockerSocket(t, release))
			t.Setenv("DOCKER_API_VERSION", dockerTestAPIVersion)

			reserved := addAndReserve(repGroup, "sleep 30", "?")

			killed, errk := jq.Kill([]*JobEssence{{JobKey: reserved.Key()}})
			So(errk, ShouldBeNil)
			So(killed, ShouldEqual, 1)

			go func() {
				time.Sleep(killWindDownStall)
				close(release)
			}()

			soBuriedAsKilled(repGroup, jq.Execute(ctx, reserved, "/bin/sh"))
		})
	})
}
