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

// This is the .docs/bugfixes/260827-1.md item 1 regression test for the server
// side. decrementGroupCount is the last thing archiveCompletedJob does before the
// archive RPC replies, and it called scheduleRunners inline - so a completion did
// not get its reply until the external scheduler command had finished. On LSF
// that command starts with a `bjobs -w` over every job in the account (22,500+
// foreign pending ones during the production incident), which is why a steady
// fraction of archive RPCs each absorbed a whole LSF round trip.
//
// scheduleGroup had already been given a goroutine for exactly this reason ("the
// external scheduler command (eg. bsub) can be slow"); this path had not.
//
// Deferring it must not turn into dropping it, and must not defer the count: the
// group's count has to be right the moment the caller returns, because concurrent
// skip checks and the next rac pass read it. Both halves are asserted here, with
// the mock scheduler parked in its "bsub" by ConfigMock.ScheduleBlock (the same
// fixture TestReliable2ScheduleGroupDeadlock uses).

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/sb10/waitgroup"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// reliable4AsyncGroupCount is the count the scheduler group starts with, so
	// one decrement leaves a distinct, non-zero count to schedule at.
	reliable4AsyncGroupCount = 3

	// reliable4AsyncTimeout is how long an operation that must not wait on the
	// external scheduler is given. The scheduler is parked indefinitely, so any
	// value proves the same thing; this one is generous for a loaded host.
	reliable4AsyncTimeout = 3 * time.Second

	// reliable4AsyncWait is how long the deferred scheduling is given to actually
	// reach the scheduler once it is released.
	reliable4AsyncWait = 10 * time.Second

	// reliable4AsyncPollFreq is how often reachedWithin re-reads its counter.
	reliable4AsyncPollFreq = 10 * time.Millisecond
)

// TestReliable4ArchiveSchedulingAsync asserts that the scheduling step of a
// completion (decrementGroupCount) does not make its caller wait for the external
// scheduler, while still both applying the decrement immediately and getting the
// decremented count to the scheduler.
func TestReliable4ArchiveSchedulingAsync(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a previously scheduled group and a scheduler parked in its external command", t, func() {
		// every Schedule() call parks on block until it is closed, standing in for
		// an LSF round trip that takes tens of seconds.
		block := make(chan struct{})

		// started counts the runners the external scheduler was actually asked
		// for, which is how the deferred scheduling is observed arriving.
		var started atomic.Int64

		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc:    func(context.Context, string) { started.Add(1) },
			ScheduleBlock: block,
		})
		So(err, ShouldBeNil)

		s := &Server{
			previouslyScheduledGroups: make(map[string]*sgroup),
			wg:                        waitgroup.New(),
			scheduler:                 sched,
			rc:                        "reliable4-async-runner %s",
			ServerInfo:                &ServerInfo{},
		}

		grp := &sgroup{
			name:     "reliable4_async_rg",
			count:    reliable4AsyncGroupCount,
			req:      &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
			priority: 0,
		}
		s.previouslyScheduledGroups[grp.name] = grp

		// release lets the parked Schedule() finish and the goroutines s.wg tracks
		// drain, however the test exits.
		release := sync.OnceFunc(func() {
			close(block)
			s.wg.Wait(reliable4AsyncWait)
		})
		defer release()

		done := make(chan struct{})

		go func() {
			s.decrementGroupCount(ctx, grp.name)
			close(done)
		}()

		Convey("the completion's decrement returns without waiting for that command", func() {
			So(closedWithin(done, reliable4AsyncTimeout), ShouldBeTrue)

			// the decrement was applied, and by the drop asked for: deferring the
			// scheduling must not turn into losing the count a concurrent skip
			// check or the next rac pass reads.
			So(grp.snapshot().count, ShouldEqual, reliable4AsyncGroupCount-1)
		})

		Convey("the scheduling it deferred still reaches the scheduler, at the decremented count", func() {
			So(closedWithin(done, reliable4AsyncTimeout), ShouldBeTrue)

			release()

			So(reachedWithin(&started, reliable4AsyncGroupCount-1, reliable4AsyncWait), ShouldBeTrue)
			So(started.Load(), ShouldEqual, reliable4AsyncGroupCount-1)
		})
	})
}

// reachedWithin reports whether counter reaches at least want within d, polling
// because the mock scheduler's runner goroutines start independently of the
// Schedule() call that spawned them.
func reachedWithin(counter *atomic.Int64, want int64, d time.Duration) bool {
	deadline := time.Now().Add(d)

	for time.Now().Before(deadline) {
		if counter.Load() >= want {
			return true
		}

		<-time.After(reliable4AsyncPollFreq)
	}

	return counter.Load() >= want
}
