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
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

// killAfterExitWait is how long the test watches for a late kill after Execute
// has returned: many touch intervals, and more than terminateGrace.
const killAfterExitWait = 2 * time.Second

// TestKillAfterCmdExitKillsNothing: a kill whose touch reply arrives after the
// command has exited and been waited for must kill nothing. Its pid is free for
// reuse by then, so a sweep of that pid's children could signal an unrelated
// process tree. The test's child-process hook stands in for such reuse by naming
// an unrelated process as the old pid's child.
func TestKillAfterCmdExitKillsNothing(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond

	Convey("Given a live manager and a job whose exit behaviour is still running", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		standIn := exec.CommandContext(ctx, "sleep", "30")
		So(standIn.Start(), ShouldBeNil)

		standInDone := make(chan struct{})

		go func() {
			standIn.Wait() //nolint:errcheck
			close(standInDone)
		}()

		defer func() {
			standIn.Process.Kill() //nolint:errcheck
			<-standInDone
		}()

		var swept atomic.Int32

		jq.childProcessesHook = func(int32) ([]*process.Process, error) {
			swept.Add(1)

			p, errp := process.NewProcess(int32(standIn.Process.Pid)) //nolint:gosec // an OS pid always fits in an int32.

			return []*process.Process{p}, errp
		}

		var touches atomic.Int32

		jq.liveTouchHook = func(*JobEndState) { touches.Add(1) }

		cwd := filepath.Join(t.TempDir(), "job")
		So(os.MkdirAll(cwd, 0o700), ShouldBeNil)

		waited := filepath.Join(t.TempDir(), "waited")

		const repGroup = "kill_after_exit"

		job := &Job{
			Cmd: "exit 0", Cwd: cwd, CwdMatters: true,
			RepGroup: repGroup, ReqGroup: repGroup,
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
			Behaviours:   Behaviours{{When: OnExit, Do: Run, Arg: "touch " + waited + " && sleep 1"}},
		}

		added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		Convey("A kill made once the command has been waited for kills nothing", func() {
			// behaviours only run once the command has been waited for, so the
			// kill is made, and its touch reply comes back, after that.
			killed := make(chan int, 1)

			go func() {
				killed <- killOnceFileExists(jq, job, waited)
			}()

			So(jq.Execute(ctx, reserved, "/bin/sh"), ShouldBeNil)
			So(<-killed, ShouldEqual, 1)

			touchesAfterExecute := touches.Load()

			select {
			case <-standInDone:
			case <-time.After(killAfterExitWait):
			}

			So(touchesAfterExecute, ShouldBeGreaterThan, 0)
			So(swept.Load(), ShouldEqual, 0)

			select {
			case <-standInDone:
				So("the stand-in was signalled", ShouldBeEmpty)
			default:
			}
		})
	})
}

// killOnceFileExists waits, for up to killAfterExitWait, until path exists,
// then kills job, returning how many jobs were killed.
func killOnceFileExists(jq *Client, job *Job, path string) int {
	deadline := time.Now().Add(killAfterExitWait)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			killed, errk := jq.Kill([]*JobEssence{{JobKey: job.Key()}})
			if errk != nil {
				return 0
			}

			return killed
		}

		time.Sleep(10 * time.Millisecond)
	}

	return 0
}
