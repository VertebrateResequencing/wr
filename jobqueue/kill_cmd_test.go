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
	"testing"

	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

var (
	errTestKillFailed     = errors.New("kill failed")
	errTestNextStepFailed = errors.New("next step failed")
)

func TestTerminateChildren(t *testing.T) {
	Convey("Given a child process that has already gone", t, func() {
		gone := exec.CommandContext(context.Background(), "true")
		So(gone.Run(), ShouldBeNil)

		child := &process.Process{Pid: int32(gone.Process.Pid)} //nolint:gosec // an OS pid always fits in an int32.
		job := &Job{Cmd: "the job's cmd"}

		Convey("terminateChildren logs the failure to terminate it, with its error", func() {
			ctx, buf := cmdLogSyncCapture(context.Background())

			errk := terminateChildren(ctx, job, []*process.Process{child}, nil)
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
