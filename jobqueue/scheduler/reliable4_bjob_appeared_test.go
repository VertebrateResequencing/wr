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

// This is the .docs/bugfixes/260827-2.md item 5 regression test. After every
// bsub, submitToQueue waits for the submitted job to show up in bjobs, polling
// `bjobs -w <id>`. That exec had no context, no timeout and no WaitDelay, and
// bjobsAppearTimeout did not bound it either: the deadline lived in the same
// select as the poll, so it could not be chosen while a poll was running. One
// appearance check LSF never answered therefore hung waitForBjob ->
// submitToQueue -> schedule() indefinitely, and Scheduler.Schedule's per-cmd
// limiter meant that scheduler group was never scheduled again.
//
// Two bounds have to hold, so they are asserted separately. The wait as a whole
// must end when its window closes however long a poll runs, which is only
// observable when a poll CAN outrun the window - the shipped bjobsExecTimeout is
// three times the shipped bjobsAppearTimeout, so it can. And one poll must end
// within its own exec bound, so the poller an ended wait abandons does not sit in
// LSF indefinitely.

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// testBjobsAppearLateSecs is how many seconds the fake bjobs takes to answer
	// the appearance check in the appearance-window test. It is many times
	// testBjobsAppearTimeout but under the shipped bjobsExecTimeout, so the poll
	// outruns the window without the exec bound ending it - which is the case the
	// window has to be enforced for.
	testBjobsAppearLateSecs = 3

	// testBjobsAppearHangSecs is how many seconds the fake bjobs takes to answer
	// the appearance check in the exec-bound test. It is well beyond that bound,
	// so a check that absorbs the hanging round trip rather than bounding it is
	// unmistakable.
	testBjobsAppearHangSecs = 10

	// testBjobsAppearTimeout is the appearance window the window test runs with,
	// so it costs milliseconds rather than the shipped 10 seconds.
	testBjobsAppearTimeout = 300 * time.Millisecond

	// bjobsAppearWaitMax is how long a whole appearance wait may take in these
	// tests. The bound being proved is testBjobsAppearTimeout, so this leaves a
	// very loaded host six times that, while staying well below the
	// testBjobsAppearLateSecs a wait that waits for its poll costs.
	bjobsAppearWaitMax = 2 * time.Second

	// bjobsAppearCheckMax is how long one appearance check may take in these
	// tests. The bound being proved is testBjobsExecTimeout plus
	// testPipeCloseGrace, half a second, so this leaves ample slack while staying
	// far below the testBjobsAppearHangSecs an unbounded check costs.
	bjobsAppearCheckMax = 5 * time.Second
)

// TestReliable4BjobAppearedBound covers 260827-2 item 5: the post-bsub
// appearance wait must come back within its own stated window whatever `bjobs`
// does, and each poll it makes must be bounded too.
func TestReliable4BjobAppearedBound(t *testing.T) {
	Convey("Given an lsf whose `bjobs -w <id>` appearance check outlasts the appearance window", t, func() {
		dir := t.TempDir()
		s := newFakeLSFScheduler(t, dir, filepath.Join(dir, "jargs"), fakeLSFDelays{})

		writeLateAppearanceBjobs(t, s.bjobsExe)
		setBjobsAppearTimeout(t, testBjobsAppearTimeout)

		ctx, _ := captureLogCtx()

		Convey("waitForBjob gives up when its window closes, not when the poll finally ends", func() {
			start := time.Now()
			appeared := s.waitForBjob(ctx, "321")
			elapsed := time.Since(start)

			// unchanged: a job that has not appeared within the window counts as
			// not appeared, and submitToQueue turns that into a retryable failure.
			// A true here means the wait sat through the whole poll to hear it.
			So(appeared, ShouldBeFalse)
			So(elapsed, ShouldBeLessThan, bjobsAppearWaitMax)
		})
	})

	Convey("Given an lsf whose `bjobs -w <id>` appearance check will not answer", t, func() {
		dir := t.TempDir()
		s := newFakeLSFScheduler(t, dir, filepath.Join(dir, "jargs"), fakeLSFDelays{
			bjobsAppearSleepSecs: testBjobsAppearHangSecs,
		})

		Convey("one appearance check gives up within its own exec bound", func() {
			start := time.Now()
			appeared := s.bjobAppeared("321", testBjobsExecTimeout, testPipeCloseGrace)
			elapsed := time.Since(start)

			// a poll that could not be answered is simply "not appeared yet"; what
			// matters is that it ends, so the poller behind an abandoned wait does
			// not sit in LSF indefinitely.
			So(appeared, ShouldBeFalse)
			So(elapsed, ShouldBeLessThan, bjobsAppearCheckMax)
		})
	})

	Convey("Given an lsf whose `bjobs -w <id>` answers at once", t, func() {
		dir := t.TempDir()
		s := newFakeLSFScheduler(t, dir, filepath.Join(dir, "jargs"), fakeLSFDelays{})

		ctx, _ := captureLogCtx()

		Convey("waitForBjob reports the submitted job as having appeared", func() {
			start := time.Now()
			appeared := s.waitForBjob(ctx, "321")
			elapsed := time.Since(start)

			// so the bounds above cannot be met by never reporting a job as
			// appeared, which would make every successful bsub a failed schedule.
			So(appeared, ShouldBeTrue)
			So(elapsed, ShouldBeLessThan, bjobsAppearWaitMax)
		})

		Convey("an appearance check of a job LSF does report says so", func() {
			So(s.bjobAppeared("321", bjobsExecTimeout, bjobsPipeCloseGrace), ShouldBeTrue)
		})
	})
}

// writeLateAppearanceBjobs replaces the fake bjobs at path with one whose
// appearance check answers testBjobsAppearLateSecs late rather than not at all,
// so a wait that waits for its poll gets an answer to report. The list call
// answers at once, reporting nothing.
func writeLateAppearanceBjobs(t *testing.T, path string) {
	t.Helper()

	writeFakeExe(t, path, fmt.Sprintf(`#!/bin/bash
if [ -n "$2" ]; then
  sleep %d
  echo "$2 sb10 RUN normal host1 host2 fakejobname000000000000000 Jul 22 12:00"
  exit 0
fi
exit 0
`, testBjobsAppearLateSecs))
}

// setBjobsAppearTimeout sets how long waitForBjob waits for a submitted job to
// appear, for the duration of the test, restoring it afterwards.
func setBjobsAppearTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	orig := bjobsAppearTimeout
	bjobsAppearTimeout = timeout

	t.Cleanup(func() {
		bjobsAppearTimeout = orig
	})
}
