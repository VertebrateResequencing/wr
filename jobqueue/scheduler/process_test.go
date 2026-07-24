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

package scheduler

import (
	"bytes"
	"context"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

// captureLogCtx returns a context whose clog output is captured into the returned
// buffer, so a test can assert whether a code path logged anything.
func captureLogCtx() (context.Context, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	handler := log15.StreamHandler(buf, log15.LogfmtFormat())

	return clog.ContextWithLogHandler(context.Background(), handler), buf
}

type processStatusScheduler struct {
	mock
	host Host
}

func (s *processStatusScheduler) getHost(_ string) (Host, bool) {
	return s.host, s.host != nil
}

type processStatusHost struct {
	stdout string
	err    error
}

func (h *processStatusHost) RunCmd(_ context.Context, _ string, _ bool) (string, string, error) {
	return h.stdout, "", h.err
}

func TestProcessNotRunningOnHostUsesProcessState(t *testing.T) {
	Convey("ProcessNotRunningOnHost treats absent processes as not running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: ""}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeTrue)
	})

	Convey("ProcessNotRunningOnHost treats zombie processes as not running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Z+\n"}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeTrue)
	})

	Convey("ProcessNotRunningOnHost treats sleeping processes as still running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "S\n"}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeFalse)
	})

	Convey("ProcessNotRunningOnHost treats host command failures as inconclusive", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{err: context.Canceled}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeFalse)
	})
}

// TestProcessNotRunningOnHostLogsWhenInconclusive is the reliable3 §1 regression
// test: when ProcessNotRunningOnHost cannot determine whether a process is alive
// or dead (so a lost job's death cannot be confirmed) it must fail LOUDLY - a warn
// log - rather than silently returning "assume alive". The three could-not-
// determine cases are a missing host, a host command (ssh) error, and ps output
// that is neither empty nor a recognised process state. Crucially, the alive/dead
// verdict for a correctly-configured, working check must be UNCHANGED and produce
// no spurious warning.
func TestProcessNotRunningOnHostLogsWhenInconclusive(t *testing.T) {
	Convey("A could-not-determine outcome returns false AND logs a warning", t, func() {
		Convey("when the host cannot be found", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: nil}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})

		Convey("when the host command errors", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{err: context.Canceled}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})

		Convey("when the ps output is neither empty nor a plausible process state", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "3\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})
	})

	Convey("A working check keeps its verdict and logs nothing", t, func() {
		Convey("a live (sleeping) process is still reported running, no warning", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Ss\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldBeEmpty)
		})

		Convey("an absent process is still reported not-running, no warning", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: ""}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeTrue)
			So(buf.String(), ShouldBeEmpty)
		})

		Convey("a zombie process is still reported not-running, no warning", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Z+\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeTrue)
			So(buf.String(), ShouldBeEmpty)
		})
	})
}
