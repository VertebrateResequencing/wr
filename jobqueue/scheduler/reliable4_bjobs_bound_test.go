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

// This is the .docs/bugfixes/260827-1.md item 1 regression test for the LSF side.
// Every scheduling pass asks LSF what it already has via `bjobs -w`, which lists
// every job in the invoking user's account rather than just wr's, and that exec
// had no timeout and no context at all: production's account held 22,500+ foreign
// pending jobs, so the pass - and, through decrementGroupCount, the archive RPC
// behind it - waited however long mbatchd felt like taking.
//
// Bounding it needs two things together, and the tests here fail if either goes.
// The exec context kills a bjobs that will not answer; the non-zero WaitDelay
// force-closes the output pipe afterwards, because otherwise a descendant that
// inherited it keeps the read blocked and the timeout bounds nothing. That
// force-close only happens for pipes os/exec owns, which is why parseBjobs gives
// the exec an io.Writer Stdout instead of scanning Cmd.StdoutPipe: measured with
// StdoutPipe, a descendant holding the pipe for 30s outlasted a 2s exec context
// and a 0.5s WaitDelay in full.
//
// The other half of the pair is that a bjobs which did NOT deliver its whole list
// must be an error, since the counts it feeds decide whether wr submits more
// runners or kills some.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// testBjobsListJobs is how many jobs of the scheduled cmd the fake `bjobs -w`
	// list reports. It only has to be more than one for a short read to be
	// visibly short.
	testBjobsListJobs = 5

	// testBjobsHangSecs is how many seconds the fake bjobs takes to answer a list
	// call in the hang tests. It is far longer than bjobsBoundMax, so a pass that
	// waits for LSF instead of bounding it is unmistakable.
	testBjobsHangSecs = 30

	// testBjobsLingerSecs is how many seconds the fake bjobs' backgrounded
	// descendant holds the inherited stdout pipe open after bjobs itself has
	// exited successfully. It is far longer than both testPipeCloseGrace and
	// bjobsBoundMax, so the exec deterministically ends in exec.ErrWaitDelay and a
	// read left waiting on the descendant is unmistakable.
	testBjobsLingerSecs = 20

	// bjobsBoundMax is how long a bounded bjobs query is allowed to take in these
	// tests. The bound being proved is testBjobsExecTimeout plus
	// testPipeCloseGrace (half a second), so this leaves a very loaded host ample
	// slack while staying far below what an unbounded query costs.
	bjobsBoundMax = 10 * time.Second

	// testBjobsExecTimeout is the exec timeout the hang tests run with, so they
	// cost milliseconds rather than the shipped 30 seconds.
	testBjobsExecTimeout = 300 * time.Millisecond
)

// TestReliable4BjobsBound covers the `bjobs -w` half of 260827-1 item 1: the
// query that opens every scheduling pass must come back within its own bound
// whatever LSF does, and when it has not delivered the whole list it must say so
// rather than hand back a short one.
func TestReliable4BjobsBound(t *testing.T) {
	req := &Requirements{RAM: 100, Time: time.Minute, Cores: 1, Other: map[string]string{}}

	Convey("Given an lsf scheduler whose `bjobs -w` will not answer", t, func() {
		dir := t.TempDir()
		jArgsFile := filepath.Join(dir, "jargs")
		s := newFakeLSFScheduler(t, dir, jArgsFile, fakeLSFDelays{
			bjobsSleepSecs: testBjobsHangSecs,
			bjobsListJobs:  testBjobsListJobs,
		})

		setBjobsTunables(t, testBjobsExecTimeout)

		ctx, _ := captureLogCtx()

		Convey("Scheduled() gives up with an error instead of waiting for LSF", func() {
			start := time.Now()
			count, err := s.scheduled(ctx, "false")
			elapsed := time.Since(start)

			So(err, ShouldNotBeNil)
			So(elapsed, ShouldBeLessThan, bjobsBoundMax)

			// the count must not be offered as if it were the truth: nothing was
			// read, and a caller that believed 0 were scheduled would submit a
			// whole fleet's worth of duplicates.
			So(count, ShouldEqual, 0)
		})

		Convey("schedule() fails retryably without submitting against a short count", func() {
			start := time.Now()
			err := s.schedule(ctx, "false", req, 0, 2)
			elapsed := time.Since(start)

			// no bsub at all: the pass must not act on a count it never obtained,
			// which for a count of 0 means submitting a whole fleet of duplicate
			// runners. Asserted first so a swallowed error cannot mask it.
			_, statErr := os.Stat(jArgsFile)
			So(errors.Is(statErr, os.ErrNotExist), ShouldBeTrue)

			So(err, ShouldNotBeNil)
			So(elapsed, ShouldBeLessThan, bjobsBoundMax)
		})
	})

	Convey("Given a `bjobs -w` that delivers its whole list then leaves a descendant on its pipe", t, func() {
		dir := t.TempDir()
		jArgsFile := filepath.Join(dir, "jargs")
		s := newFakeLSFScheduler(t, dir, jArgsFile, fakeLSFDelays{
			bjobsLingerSecs: testBjobsLingerSecs,
			bjobsListJobs:   testBjobsListJobs,
		})

		// the exec timeout stays at its shipped value here: what has to bound this
		// query is the pipe-close grace, since bjobs itself exited immediately.
		setBjobsTunables(t, defaultBjobsExecTimeout)

		ctx, logs := captureLogCtx()

		Convey("Scheduled() returns the complete list it read, promptly", func() {
			start := time.Now()
			count, err := s.scheduled(ctx, "false")
			elapsed := time.Since(start)

			So(err, ShouldBeNil)
			So(elapsed, ShouldBeLessThan, bjobsBoundMax)

			// every line bjobs printed was parsed: a lingering descendant is
			// accepted as the complete read it is, never as a short list.
			So(count, ShouldEqual, testBjobsListJobs)

			// the lingering pipe is not an error, but it does mean an LSF command
			// outlived its own exit, so it must not be silent either.
			So(logs.String(), ShouldContainSubstring, "holding its output pipe open")
		})
	})
}

// setBjobsTunables sets the bjobs exec timeout to the given value, and the bjobs
// pipe-close grace to testPipeCloseGrace, for the duration of the test,
// restoring both afterwards.
func setBjobsTunables(t *testing.T, execTimeout time.Duration) {
	t.Helper()

	origTimeout, origGrace := bjobsExecTimeout, bjobsPipeCloseGrace
	bjobsExecTimeout, bjobsPipeCloseGrace = execTimeout, testPipeCloseGrace

	t.Cleanup(func() {
		bjobsExecTimeout, bjobsPipeCloseGrace = origTimeout, origGrace
	})
}
