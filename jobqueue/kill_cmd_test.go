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
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

var (
	errTestKillFailed     = errors.New("kill failed")
	errTestNextStepFailed = errors.New("next step failed")
)

// errTestNoStartTime stands in for a process start time that cannot be read.
var errTestNoStartTime = errors.New("no start time")

func TestTerminateChildren(t *testing.T) {
	Convey("Given a child process that has already gone", t, func() {
		// the child is listed while it is alive, as getChildProcesses lists
		// one, and only then made to exit and be reaped.
		gone := exec.CommandContext(context.Background(), "sleep", "30")
		So(gone.Start(), ShouldBeNil)

		child, err := process.NewProcess(int32(gone.Process.Pid)) //nolint:gosec // an OS pid always fits in an int32.
		So(err, ShouldBeNil)

		So(gone.Process.Kill(), ShouldBeNil)
		So(gone.Wait(), ShouldNotBeNil)

		job := &Job{Cmd: "the job's cmd"}

		Convey("terminateChildren logs the failure to terminate it, with its error", func() {
			ctx, buf := cmdLogSyncCapture(context.Background())

			errk := (&Client{}).terminateChildren(ctx, job, []*process.Process{child}, nil)
			So(errk, ShouldNotBeNil)

			out := buf.String()
			So(out, ShouldContainSubstring, "failed to kill child of cmd")
			So(out, ShouldContainSubstring, "err=")
			So(out, ShouldContainSubstring, errk.Error())
			So(out, ShouldNotContainSubstring, "killed child of cmd")
		})
	})
}

func TestChainKillErr(t *testing.T) {
	Convey("chainKillErr", t, func() {
		errk := errTestKillFailed
		next := errTestNextStepFailed

		Convey("leaves errk unchanged when the next step worked", func() {
			So(chainKillErr(errk, nil, "the next step"), ShouldEqual, errk)
		})

		Convey("returns next when nothing failed before it", func() {
			So(chainKillErr(nil, next, "the next step"), ShouldEqual, next)
			So(chainKillErr(nil, nil, "the next step"), ShouldBeNil)
		})

		Convey("wraps both when both failed", func() {
			chained := chainKillErr(errk, next, "the next step")
			So(chained.Error(), ShouldEqual, "kill failed, and the next step failed: next step failed")
			So(errors.Is(chained, errk), ShouldBeTrue)
			So(errors.Is(chained, next), ShouldBeTrue)
		})
	})
}

func TestTerminateChildrenFollowUpKill(t *testing.T) {
	Convey("Given a child of a killed cmd that ignores SIGTERM", t, func() {
		child, done := startTermIgnorer(t)
		job := &Job{Cmd: "the job's cmd"}
		c := &Client{}

		var reads atomic.Int32

		Convey("it is SIGKILLed after the grace period, reading its real start time", func() {
			c.terminateChildren(context.Background(), job, []*process.Process{child}, nil) //nolint:errcheck

			So(exitedSoon(done), ShouldBeTrue)
		})

		Convey("it is SIGKILLed after the grace period if it is still the same process", func() {
			c.processStartHook = func(int32) (int64, error) { return 1000, nil }

			c.terminateChildren(context.Background(), job, []*process.Process{child}, nil) //nolint:errcheck

			So(exitedSoon(done), ShouldBeTrue)
		})

		Convey("it is not SIGKILLed if its pid now belongs to a different process", func() {
			c.processStartHook = func(int32) (int64, error) { return 1000 + int64(reads.Add(1)), nil }

			c.terminateChildren(context.Background(), job, []*process.Process{child}, nil) //nolint:errcheck

			So(exitedSoon(done), ShouldBeFalse)
		})

		Convey("it is not SIGKILLed if what process has its pid can't be told", func() {
			c.processStartHook = func(int32) (int64, error) { return 0, errTestNoStartTime }

			ctx, buf := cmdLogSyncCapture(context.Background())
			c.terminateChildren(ctx, job, []*process.Process{child}, nil) //nolint:errcheck

			So(exitedSoon(done), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "not sending SIGKILL")
		})
	})
}

// startTermIgnorer starts a process that ignores SIGTERM, so that only the
// follow-up SIGKILL of terminateChildren can end it, and returns it as a child
// process along with a channel closed once it has exited.
func startTermIgnorer(t *testing.T) (*process.Process, <-chan struct{}) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "sh", "-c", `trap "" TERM; while :; do sleep 0.1; done`)
	So(cmd.Start(), ShouldBeNil)

	done := make(chan struct{})

	go func() {
		cmd.Wait() //nolint:errcheck
		close(done)
	}()

	t.Cleanup(func() {
		cmd.Process.Kill() //nolint:errcheck
		<-done
	})

	// let the trap be set before anything signals it
	time.Sleep(200 * time.Millisecond)

	p, err := process.NewProcess(int32(cmd.Process.Pid)) //nolint:gosec // an OS pid always fits in an int32.
	So(err, ShouldBeNil)

	return p, done
}

// exitedSoon reports whether done is closed within a second: well after
// terminateGrace, when any follow-up SIGKILL will have been sent.
func exitedSoon(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-time.After(time.Second):
		return false
	}
}
