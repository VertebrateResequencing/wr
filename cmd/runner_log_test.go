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

// This file tests reliable4 ITEM C2: the runner logged the whole command line on
// every job, so a pathological Cmd put ~130KB into each of several log lines per
// job (measured p99 24,261 bytes and a max of 1,345,498 bytes for a single
// "reserved a job" line in production).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

// runnerLogTestCmdBytes is how long a test command line is. It is far over
// internal.AbbreviateMax, so an unbounded log line is unmistakable, while
// staying under Linux's 128KB MAX_ARG_STRLEN so the command remains runnable.
const runnerLogTestCmdBytes = 20000

// runnerLogTestMaxLine is the generous bound a single bounded log line must stay
// under: the abbreviated command plus its key, level, timestamp and extras.
const runnerLogTestMaxLine = 1024

// runnerLogTestSentinel ends a test command line. Filler bytes alone cannot
// prove anything (they also appear in the kept prefix), so the tail is a
// distinctive marker that can only be present if the WHOLE command line was
// logged.
const runnerLogTestSentinel = "-TAIL-OF-THE-CMD"

// runnerLogTestRepGroup is the rep group the test jobs use.
const runnerLogTestRepGroup = "runner-log-test"

func TestRunnerJobLogLine(t *testing.T) {
	Convey("Given a job with a 20KB command line", t, func() {
		job := runnerLogTestJob()
		cmd := job.Cmd
		key := job.Key()

		Convey("A runner log line about it is bounded but still identifies it", func() {
			ctx, buf := runnerLogCapture()

			logJobLine(ctx, "reserved a job", job, "attempts", job.Attempts)

			out := buf.String()
			t.Logf("RUNNERLOG-MEASURED cmdLen=%d lineBytes=%d", len(cmd), len(out))
			So(out, ShouldContainSubstring, "reserved a job")
			So(len(out), ShouldBeLessThan, runnerLogTestMaxLine)

			// the key is what lets an operator get the whole command back out of
			// `wr status`, so bounding the line must not cost them that.
			So(out, ShouldContainSubstring, key)
			So(out, ShouldContainSubstring, "truncated")
			So(out, ShouldNotContainSubstring, runnerLogTestSentinel)
			So(out, ShouldContainSubstring, "attempts=0")
		})

		Convey("The extra key-values are logged too", func() {
			ctx, buf := runnerLogCapture()

			logJobLine(ctx, "command ran OK", job, "exitcode", 0)

			out := buf.String()
			So(out, ShouldContainSubstring, "command ran OK")
			So(out, ShouldContainSubstring, "exitcode=0")
			So(len(out), ShouldBeLessThan, runnerLogTestMaxLine)
		})

		Convey("Bounding the log line does NOT alter the job", func() {
			ctx, _ := runnerLogCapture()

			logJobLine(ctx, "will start executing", job)

			// truncation must be presentation-only: the runner goes on to execute
			// job.Cmd, so a fix that mutated it would run the wrong command.
			So(job.Cmd, ShouldEqual, cmd)
			So(len(job.Cmd), ShouldBeGreaterThan, runnerLogTestCmdBytes)
			So(job.Key(), ShouldEqual, key)
		})

		Convey("A short command line is logged in full", func() {
			short := &jobqueue.Job{Cmd: "echo hello", Cwd: statusTestCwd, RepGroup: runnerLogTestRepGroup}
			ctx, buf := runnerLogCapture()

			logJobLine(ctx, "reserved a job", short)

			out := buf.String()
			So(out, ShouldContainSubstring, "echo hello")
			So(out, ShouldNotContainSubstring, "truncated")
			So(len(short.Cmd), ShouldBeLessThanOrEqualTo, internal.AbbreviateMax)
		})
	})
}

// runnerLogTestJob returns a job with a deliberately huge command line.
func runnerLogTestJob() *jobqueue.Job {
	return &jobqueue.Job{
		Cmd:      "true # " + strings.Repeat("c", runnerLogTestCmdBytes) + runnerLogTestSentinel,
		Cwd:      statusTestCwd,
		RepGroup: runnerLogTestRepGroup,
	}
}

// runnerLogCapture returns a context whose clog output is captured into the
// returned buffer.
func runnerLogCapture() (context.Context, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	handler := log15.StreamHandler(buf, log15.LogfmtFormat())

	return clog.ContextWithLogHandler(context.Background(), handler), buf
}
