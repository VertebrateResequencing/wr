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

// These are the .docs/bugfixes/260824-1.md regression tests. bsub and bkill are
// run with a Cmd.WaitDelay so that a hung one killed by its exec timeout still
// returns (see bsubPipeCloseGrace), but Go starts that timer when the child
// exits normally as well as when the exec context is cancelled, and returns
// exec.ErrWaitDelay instead of nil if the pipes are still open when it fires. So
// an utterly ordinary SUCCESSFUL bsub/bkill that leaves a descendant holding the
// inherited pipe a moment longer than the grace was reported as a failure: wr
// never learned the id of an array LSF had accepted (backing that scheduler
// group off by up to 30m), and a bkill that had actually killed 500 elements was
// reported as reclaiming nothing, abandoning the cycle's remaining batches.
//
// The fake bsub/bkill exes here reproduce that descendant: they print their
// normal output, background a subshell which inherits the pipe, and exit 0. Both
// tests assert on the trace that descendant leaves in what wr reports, so they
// fail if the fixture ever stops producing one and they quietly become another
// plain-success test.

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// testPipeLingerSecs is how many seconds the fake exes' backgrounded
	// descendant holds the inherited pipe open. It is far longer than
	// testPipeCloseGrace, so the exec deterministically ends in
	// exec.ErrWaitDelay rather than racing the grace.
	testPipeLingerSecs = 4

	// waitDelayExitField is how a kill summary reports a bkill that exited
	// cleanly and then lingered on its pipes: Go's exec.ErrWaitDelay text, in the
	// summary's exit field rather than its failure one.
	waitDelayExitField = `exit="exec: WaitDelay expired before I/O complete"`

	// bkillLingeringBodyFmt is a fake bkill that reports every id it was given as
	// terminated and exits 0, but leaves a backgrounded descendant holding its
	// pipes open for the given number of seconds.
	bkillLingeringBodyFmt = `for a in "$@"; do
  if [ "$a" = "-b" ]; then continue; fi
  echo "Job <$a> is being terminated"
done
( sleep %d ) &
exit 0
`

	// bkillSilentLingeringBodyFmt is a fake bkill that accepts the kill request
	// without saying anything (as `bkill -b` can) and exits 0, but leaves a
	// backgrounded descendant holding its pipes open for the given number of
	// seconds.
	bkillSilentLingeringBodyFmt = `( sleep %d ) &
exit 0
`
)

// TestReliable4BsubPipeLinger covers the bsub half of 260824-1: a bsub that exits
// successfully, naming the array LSF accepted, must be treated as the successful
// submission it is however long a descendant it left behind holds the output pipe
// open.
func TestReliable4BsubPipeLinger(t *testing.T) {
	req := &Requirements{RAM: 100, Time: time.Minute, Cores: 1, Other: map[string]string{}}

	Convey("Given an lsf scheduler whose successful bsub leaves a descendant holding its output pipe", t, func() {
		dir := t.TempDir()
		jArgsFile := filepath.Join(dir, "jargs")
		s := newFakeLSFScheduler(t, dir, jArgsFile, fakeLSFDelays{lingerSecs: testPipeLingerSecs})
		setPipeCloseGraces(t, testPipeCloseGrace)

		ctx, logs := captureLogCtx()

		Convey("schedule() reports the submission LSF accepted as a success", func() {
			err := s.schedule(ctx, "false", req, 0, 2)
			So(err, ShouldBeNil)

			names, sizes := parseJArrays(t, jArgsFile)
			So(len(names), ShouldEqual, 1)
			So(sizes, ShouldResemble, []int{2})

			// the lingering pipe is not an error, but it does mean an LSF command
			// outlived its own exit, so it must not be silent either.
			So(logs.String(), ShouldContainSubstring, "holding its output pipe open")
		})
	})
}

// TestReliable4BkillPipeLinger covers the bkill half of 260824-1: a bkill that
// exits successfully must count as having run to completion (so the cycle's
// remaining batches are still issued) and its elements must be reported as
// killed, however long a descendant it left behind holds the pipes open. The
// lingering itself is reported too, at warn: wr force-closed the pipes of a live
// descendant and may have read only part of what bkill said.
func TestReliable4BkillPipeLinger(t *testing.T) {
	Convey("Given excess elements and a successful bkill that names what it terminated", t, func() {
		h := newBkillHarness(t, bkillTestElements, fmt.Sprintf(bkillLingeringBodyFmt, testPipeLingerSecs))
		setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

		ctx, logs := captureLogCtx()

		Convey("every batch is issued and every element is reported killed", func() {
			count, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 0)

			invocations := h.invocations(t)
			_, total, seen := summariseInvocations(invocations)

			So(len(invocations), ShouldEqual, (bkillTestElements+bkillTestBatch-1)/bkillTestBatch)
			So(total, ShouldEqual, bkillTestElements)
			So(countNotSeenExactlyOnce(h.ids, seen), ShouldEqual, 0)

			assertLingeringKillSummary(logs.String(), bkillTestElements)
		})
	})

	Convey("Given excess elements and a successful bkill that says nothing", t, func() {
		h := newBkillHarness(t, bkillTestElements, fmt.Sprintf(bkillSilentLingeringBodyFmt, testPipeLingerSecs))
		setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

		ctx, logs := captureLogCtx()

		Convey("its clean exit still counts every element it was given as killed", func() {
			count, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 0)

			_, total, seen := summariseInvocations(h.invocations(t))
			So(total, ShouldEqual, bkillTestElements)
			So(countNotSeenExactlyOnce(h.ids, seen), ShouldEqual, 0)

			assertLingeringKillSummary(logs.String(), bkillTestElements)
		})
	})
}

// assertLingeringKillSummary asserts that the given cycle logging reports every
// one of killed elements as killed with nothing abandoned or unexplained, and
// reports the lingering pipe that made this cycle worth reproducing: named in the
// summary's exit field, at warn so an operator at the default log level sees it,
// and never in its failure field (which is where the bug used to put it, along
// with abandoning the rest of the cycle).
func assertLingeringKillSummary(logged string, killed int) {
	So(logged, ShouldContainSubstring, fmt.Sprintf("killed=%d", killed))
	So(logged, ShouldContainSubstring, "abandoned=0")
	So(logged, ShouldContainSubstring, "unaccounted=0")
	So(logged, ShouldContainSubstring, waitDelayExitField)
	So(logged, ShouldContainSubstring, "lvl=warn")
	So(logged, ShouldNotContainSubstring, " err=")
}
