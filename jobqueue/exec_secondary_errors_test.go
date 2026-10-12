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
	. "github.com/smartystreets/goconvey/convey"
)

// execSecondaryBehaviourOutput is what the test job's failing behaviour prints,
// so its problem can be recognised wherever it is reported.
const execSecondaryBehaviourOutput = "the-behaviour-broke"

// errTestUnmount stands in for a problem unmounting a job's mounts.
var errTestUnmount = errors.New("unmount trouble")

// TestExecResultErr covers how the problems Execute met are combined with its
// job's final outcome into what Execute returns.
func TestExecResultErr(t *testing.T) {
	Convey("Given the problems Execute met besides its command's outcome", t, func() {
		var problems execProblems
		problems.add(errTestBehaviour, "behaviour(s) also had problem(s)")
		problems.add(errTestUnmount, "unmounting also caused problem(s)")

		job := &Job{Cmd: "a cmd"}

		Convey("a failed command's error leads, and still identifies its fail reason", func() {
			sigErr := Error{clientOpExecute, job.Key(), FailReasonSignal}
			err := problems.resultErr(context.Background(), job,
				execOutcome{myerr: sigErr, failreason: FailReasonSignal, dorelease: true})

			So(err.Error(), ShouldEqual, sigErr.Error()+
				"; behaviour(s) also had problem(s): cleanup failed"+
				"; unmounting also caused problem(s): unmount trouble")

			var jqerr Error
			So(errors.As(err, &jqerr), ShouldBeTrue)
			So(jqerr.Err, ShouldEqual, FailReasonSignal)
			So(errors.Is(err, errTestBehaviour), ShouldBeTrue)
			So(errors.Is(err, errTestUnmount), ShouldBeTrue)
		})

		Convey("a release for an output that did not upload returns the problems once", func() {
			err := problems.resultErr(context.Background(), job, uploadFailedOutcome(nil))

			So(err.Error(), ShouldEqual,
				"cleanup failed; unmounting also caused problem(s): unmount trouble")
		})

		Convey("a successful command returns nil, and logs them instead", func() {
			logCtx, logs := captureLogCtx(context.Background())

			So(problems.resultErr(logCtx, job, execOutcome{doarchive: true}), ShouldBeNil)
			So(logs.String(), ShouldContainSubstring, execSucceededWithProblemsMsg)
			So(logs.String(), ShouldContainSubstring, "unmount trouble")
		})
	})

	Convey("Given no problems, a failed command's error is returned as is", t, func() {
		var problems execProblems

		err := problems.resultErr(context.Background(), &Job{},
			execOutcome{myerr: errTestCmdExited, dobury: true})
		So(err, ShouldEqual, errTestCmdExited)
	})
}

// TestExecuteReportsSecondaryErrors covers the problems Execute meets besides
// its command's own outcome, such as a behaviour that fails: they must reach
// the caller with a failed command's error, and the runner's log when the
// command worked (Execute then returns nil, per its contract).
func TestExecuteReportsSecondaryErrors(t *testing.T) {
	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a client connected to a server, with its log captured", t, func() {
		logCtx, logs := cmdLogSyncCapture(context.Background())

		server, _, token, err := serve(logCtx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(logCtx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("a failed command whose behaviour also fails returns both problems", func() {
			job := execSecondaryJob(t, jq, "exit 1")

			err := jq.Execute(logCtx, job, "/bin/sh")
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "exited with code 1")
			So(err.Error(), ShouldContainSubstring, "behaviour(s) also had problem(s)")
			So(err.Error(), ShouldContainSubstring, execSecondaryBehaviourOutput)
		})

		Convey("a successful command whose behaviour fails returns nil, but logs the problem", func() {
			job := execSecondaryJob(t, jq, "true")

			So(jq.Execute(logCtx, job, "/bin/sh"), ShouldBeNil)

			out := logs.String()
			So(out, ShouldContainSubstring, execSucceededWithProblemsMsg)
			So(out, ShouldContainSubstring, execSecondaryBehaviourOutput)
			So(out, ShouldContainSubstring, job.Key())
		})
	})
}

// execSecondaryJob adds and reserves a job running cmd, with a behaviour that
// fails on exit.
func execSecondaryJob(t *testing.T, jq *Client, cmd string) *Job {
	t.Helper()

	cwd := filepath.Join(t.TempDir(), "job")
	So(os.MkdirAll(cwd, cmdLogTestMode), ShouldBeNil)

	repGroup := "exec_secondary_errors"
	job := &Job{
		Cmd: cmd, Cwd: cwd, CwdMatters: true,
		RepGroup: repGroup, ReqGroup: repGroup,
		Requirements: &jqs.Requirements{
			RAM: 10, Time: 10 * time.Second, Other: make(map[string]string),
		},
		Behaviours: Behaviours{
			{When: OnExit, Do: Run, Arg: "echo " + execSecondaryBehaviourOutput + " >&2; exit 7"},
		},
	}

	added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, 1)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, job.Key())

	return reserved
}
