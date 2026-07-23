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

// This file is a regression test for the scheduler-group lock-ordering deadlock
// found by real-LSF Tier-B validation (.docs/reliable2/phase2/validation.md).
// Under heavy churn the manager hard-deadlocked because scheduleGroup held the
// per-sgroup write lock across the (minutes-long) external scheduler command
// (bsub): archive handlers holding s.psgmutex.RLock then blocked forever trying
// to take that sgroup's RLock in hasSkips, and a pending psgmutex.Lock (the RAC
// scheduling callback) barred all further readers, freezing the manager.
//
// The test reproduces the deadlock in-process with a blocking mock scheduler (no
// real LSF): a scheduleGroup call is parked "in bsub", and we assert that the
// archive skip-check path (psgmutex.RLock -> hasSkippedScheduledGroups ->
// sgroup.RLock) and the RAC writer path (psgmutex.Lock) both still complete
// within a short timeout. Pre-fix (sgroup write lock held across scheduleRunners)
// they deadlock and the test times out; post-fix (scheduleRunners runs against a
// snapshot with no sgroup lock held) they complete.

import (
	"context"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/sb10/waitgroup"
	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2ScheduleGroupDeadlock reproduces the Tier-B scheduler-group
// lock-ordering deadlock deterministically and asserts the fix dissolves it.
func TestReliable2ScheduleGroupDeadlock(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A scheduleGroup whose scheduler command is slow must not wedge the archive/scheduling paths", t, func() {
		// A blocking mock scheduler stands in for a slow external scheduler
		// command (eg. bsub): every Schedule() call parks on block until we
		// close it, so a scheduleGroup's scheduleRunners is held "in bsub".
		block := make(chan struct{})
		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc:    func(context.Context, string) {},
			ScheduleBlock: block,
		})
		So(err, ShouldBeNil)

		s := &Server{
			previouslyScheduledGroups: make(map[string]*sgroup),
			wg:                        waitgroup.New(),
			scheduler:                 sched,
			rc:                        "reliable2-deadlock-runner %s",
			ServerInfo:                &ServerInfo{},
		}

		grp := &sgroup{
			name:     "reliable2_deadlock_rg",
			count:    3,
			skipped:  1,
			req:      &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
			priority: 0,
		}

		// Mirror the RAC scheduling callback (scheduleGroupRunners): under
		// s.psgmutex, record and schedule the group, then release s.psgmutex.
		// scheduleRunners now runs in its own goroutine and parks in the blocked
		// scheduler, standing in for a group stuck mid-bsub.
		s.psgmutex.Lock()
		s.scheduleGroup(ctx, grp.name, grp)
		s.psgmutex.Unlock()

		// Always release the blocked scheduler so the scheduleRunners goroutine
		// can finish and s.wg can drain, no matter how the test exits.
		defer func() {
			close(block)
			s.wg.Wait(5 * time.Second)
		}()

		roleB := make(chan struct{})
		roleC := make(chan struct{})

		// Role B (archive path, decrementGroupCount lines): take s.psgmutex.RLock,
		// then hasSkippedScheduledGroups iterates all groups calling
		// sgroup.hasSkips() -> sgroup.RLock() while still holding s.psgmutex.RLock.
		// Pre-fix this blocks forever on grp's write lock (held across bsub).
		go func() {
			s.psgmutex.RLock()
			_ = s.hasSkippedScheduledGroups()
			s.psgmutex.RUnlock()
			close(roleB)
		}()

		// Role C (RAC writer): wants s.psgmutex.Lock. Once pending, Go's RWMutex
		// bars all new s.psgmutex.RLock, which is what froze the whole manager.
		go func() {
			s.psgmutex.Lock()
			_ = len(s.previouslyScheduledGroups)
			s.psgmutex.Unlock()
			close(roleC)
		}()

		Convey("the archive skip-check completes without blocking on the scheduling sgroup lock", func() {
			So(closedWithin(roleB, 3*time.Second), ShouldBeTrue)
		})

		Convey("the RAC scheduling writer can still acquire psgmutex", func() {
			So(closedWithin(roleC, 3*time.Second), ShouldBeTrue)
		})
	})
}

// closedWithin reports whether ch is closed (or receives) within d.
func closedWithin(ch <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}
