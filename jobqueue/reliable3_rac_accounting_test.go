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

// Regression tests for the reliable3 §2b (over-count) and §2a-priority
// (priority-fair budget allocation) rac-accounting fixes. They build on the §2a
// shared-per-limit-group budget (TestReliable3LimitGroupOverProvision): here we
// additionally assert that (§2b) once currently-running jobs are added on top of
// the capped ready count, a limit group's summed runner request is trimmed back to
// its limit even when reserves landed between the capacity read and the running
// snapshot; and that (§2a-priority) the shared budget is allocated to
// higher-priority sibling scheduler groups first, so a low-priority sibling
// scanned first cannot starve a higher-priority one. These assert the FIXED
// behaviour (they fail on the pre-fix accounting and pass after the fix), driving
// the rac cycle's live ready-job selection (racCountReadyJobs); the build-tagged
// reliable3_repro_test.go checks run the same arrangements at production scale.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// addRunningJobs adds n jobs to the queue's run sub-queue, all in the given
// scheduler group and with the given job priority, so accountForRunningJobs counts
// them (and picks up their priority). Returns how many were added and any error,
// for the caller to assert.
func addRunningJobs(ctx context.Context, q *queue.Queue, group string, priority uint8, n int) (int, error) {
	defs := make([]*queue.ItemDef, 0, n)

	for i := range n {
		// a valid Requirements is needed for the running-only-group path
		// (groupForRunningJob clones job.Requirements when the group is not already
		// present from the ready phase).
		job := &Job{Priority: priority, Requirements: &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}}
		job.setSchedulerGroup(group)
		defs = append(defs, &queue.ItemDef{
			Key:          fmt.Sprintf("%s-run-%d", group, i),
			ReserveGroup: group,
			Data:         job,
			TTR:          time.Hour,
			StartQueue:   queue.SubQueueRun,
		})
	}

	added, _, err := q.AddMany(ctx, defs)

	return added, err
}

func TestReliable3RacAccountingCaps(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	lg := []string{"lg"}

	Convey("A single group's count never exceeds its limit once running jobs are added (§2b)", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		initialRunning := opEnvInt("WR_RC_INITIAL", 300)
		windowReserves := opEnvInt("WR_RC_WINDOW", 1500)

		if initialRunning+windowReserves > limit {
			windowReserves = limit - initialRunning
		}

		s := newOverProvisionServer(limit)
		jobs := racReadyJobs("rc", 200, 0, lg, limit+windowReserves+100)
		grpName := racGroupOf(jobs[0])

		// initialRunning jobs hold slots when the ready budget is read...
		for range initialRunning {
			s.limiter.Increment(ctx, []string{"lg"})
		}

		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, jobs)

		// ...then windowReserves more reserve AFTER that read (the non-atomic window)
		// and land in the run queue.
		for range windowReserves {
			s.limiter.Increment(ctx, []string{"lg"})
		}

		totalRunning := initialRunning + windowReserves

		q := queue.New(ctx, "reliable3-overcount-fix")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		added, err := addRunningJobs(ctx, q, grpName, 0, totalRunning)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, totalRunning)

		s.accountForRunningJobs(q, groups)

		Convey("the final count is capped at the limit", func() {
			So(groups[grpName].count, ShouldBeLessThanOrEqualTo, limit)
		})
	})

	Convey("Summed count across sibling groups with running jobs stays within the limit (§2b)", t, func() {
		limit := 100
		siblings := 4
		runningPerSibling := limit / siblings

		s := newOverProvisionServer(limit)

		grpNames := make([]string, siblings)
		jobs := []*Job(nil)

		for i := range siblings {
			sibling := racReadyJobs(fmt.Sprintf("rc%d", i), 100+i*100, 0, lg, limit)
			grpNames[i] = racGroupOf(sibling[0])
			jobs = append(jobs, sibling...)
		}

		// ready budget is read now (nothing running yet) => summed ready == limit.
		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, jobs)

		q := queue.New(ctx, "reliable3-overcount-siblings")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		// each sibling then gets running jobs on top (reserves that landed after).
		for i := range siblings {
			s.limiter.Increment(ctx, []string{"lg"})

			added, err := addRunningJobs(ctx, q, grpNames[i], 0, runningPerSibling)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, runningPerSibling)
		}

		s.accountForRunningJobs(q, groups)

		total := 0
		for _, grp := range groups {
			total += grp.count
		}

		Convey("the summed request does not exceed the limit", func() {
			So(total, ShouldBeLessThanOrEqualTo, limit)
		})
	})

	Convey("A scheduler group with no limit group is never capped (§2b)", t, func() {
		limit := 100
		readyJobs := 150
		running := 60

		s := newOverProvisionServer(limit)
		jobs := racReadyJobs("rc", 200, 0, nil, readyJobs) // no limit group
		grpName := racGroupOf(jobs[0])

		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, jobs)

		q := queue.New(ctx, "reliable3-nolimit")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		added, err := addRunningJobs(ctx, q, grpName, 0, running)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, running)

		s.accountForRunningJobs(q, groups)

		Convey("its count is the full ready+running total, uncapped", func() {
			So(groups[grpName].count, ShouldEqual, readyJobs+running)
		})
	})

	Convey("The shared budget favours the higher-priority sibling scanned last (§2a-priority)", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		readyPerGroup := limit + opEnvInt("WR_PF_READY", 500)

		low := racReadyJobs("low", 100, 0, lg, readyPerGroup)     // priority 0
		high := racReadyJobs("high", 200, 250, lg, readyPerGroup) // priority 250
		lowGrp, highGrp := racGroupOf(low[0]), racGroupOf(high[0])

		s := newOverProvisionServer(limit)
		groups := make(map[string]*sgroup)

		// low-priority sibling's ready jobs are scanned FIRST, high-priority LAST.
		racCountReadyJobs(ctx, s, groups, append(low, high...))

		Convey("the high-priority sibling gets the budget, the low-priority one is starved", func() {
			So(groups[highGrp].count, ShouldEqual, limit)
			So(groups[lowGrp].count, ShouldEqual, 0)
		})
	})

	Convey("A running-only high-priority group is not trimmed before a lower-priority ready sibling", t, func() {
		// COMMENT B regression: accountForRunningJobs must give a running-only group
		// the priority of its running jobs, otherwise the cap (trimGroupsToLimit,
		// which trims lowest-priority first) would trim the high-priority running
		// group's runners ahead of a genuinely lower-priority ready sibling.
		limit := 100
		runCount := limit / 2

		s := newOverProvisionServer(limit)
		readyLow := racReadyJobs("readylow", 100, 10, lg, limit) // priority 10 (ready)
		readyLowGrp := racGroupOf(readyLow[0])
		runHighGrp := racGroupOf(racReadyJobs("runhigh", 200, 250, lg, 1)[0]) // running-only

		// the low-priority ready sibling fills the whole limit first (held=0 =>
		// budget=limit), so nothing is skipped and there is no ready backlog.
		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, readyLow)
		So(groups[readyLowGrp].count, ShouldEqual, limit)

		// the high-priority sibling then appears purely via running jobs (reserves
		// that landed after the capacity read), overshooting the limit by runCount.
		q := queue.New(ctx, "reliable3-priority-running")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		added, err := addRunningJobs(ctx, q, runHighGrp, 250, runCount)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, runCount)

		s.accountForRunningJobs(q, groups)

		Convey("the running-only group's priority reflects its running jobs", func() {
			So(groups[runHighGrp].priority, ShouldEqual, 250)
		})

		Convey("the high-priority running group keeps all its runners", func() {
			So(groups[runHighGrp].count, ShouldEqual, runCount)
		})

		Convey("the lower-priority ready sibling is the one trimmed, summed at the limit", func() {
			So(groups[readyLowGrp].count, ShouldEqual, limit-runCount)
			So(groups[readyLowGrp].count+groups[runHighGrp].count, ShouldEqual, limit)
		})
	})

	Convey("The priority sort is gated to when a limit-group budget can be contended (§2a-priority)", t, func() {
		// a rac cycle must consider ready jobs highest-priority-first when the
		// shared per-limit-group budget can be contended (a ready job carries a
		// limit group); otherwise the sort could not change the outcome, so it is
		// skipped, leaving the ready jobs in the order they came.
		Convey("with ready jobs carrying a limit group it sorts, preserving priority-fairness", func() {
			limit := 5
			readyPerGroup := limit + 3

			s := newOverProvisionServer(limit)
			low := racReadyJobs("low", 100, 0, lg, readyPerGroup)     // scanned first
			high := racReadyJobs("high", 200, 250, lg, readyPerGroup) // scanned last
			lowGrp, highGrp := racGroupOf(low[0]), racGroupOf(high[0])

			groups := make(map[string]*sgroup)
			candidates := racCountReadyJobs(ctx, s, groups, append(low, high...))

			So(candidates[0].snapshot.group, ShouldEqual, highGrp)
			So(candidates[len(candidates)-1].snapshot.group, ShouldEqual, lowGrp)

			So(groups[highGrp].count, ShouldEqual, limit)
			So(groups[lowGrp].count, ShouldEqual, 0)
		})

		Convey("with no ready job carrying a limit group the sort is skipped (order untouched)", func() {
			s := newOverProvisionServer(100)           // a count limit IS configured...
			low := racReadyJobs("low", 100, 0, nil, 3) // ...but these carry no limit group
			high := racReadyJobs("high", 200, 250, nil, 3)
			lowGrp, highGrp := racGroupOf(low[0]), racGroupOf(high[0])

			groups := make(map[string]*sgroup)
			candidates := racCountReadyJobs(ctx, s, groups, append(low, high...))

			// a sort would have moved the priority-250 jobs to the front; the
			// original low-then-high order surviving proves the sort was skipped.
			So(candidates[0].snapshot.group, ShouldEqual, lowGrp)
			So(candidates[0].snapshot.priority, ShouldEqual, uint8(0))
			So(candidates[len(candidates)-1].snapshot.group, ShouldEqual, highGrp)

			// and with no limit group nothing is capped: every ready job is counted.
			So(groups[lowGrp].count, ShouldEqual, 3)
			So(groups[highGrp].count, ShouldEqual, 3)
		})

		Convey("with no count limit configured at all nothing is capped, whatever the order", func() {
			// the cycle still sorts here, deliberately: it gates on limit-group
			// presence, not on GetLimits(), because a limit is enforced via
			// GetRemainingCapacity's callback even before its group is first
			// vivified. The sort then cannot change the outcome.
			lim := limiter.New(func(_ context.Context, _ string) *limiter.GroupData { return nil })
			s := &Server{limiter: lim, previouslyScheduledGroups: make(map[string]*sgroup)}

			low := racReadyJobs("low", 100, 0, lg, 4)
			high := racReadyJobs("high", 200, 250, lg, 4)

			groups := make(map[string]*sgroup)
			racCountReadyJobs(ctx, s, groups, append(low, high...))

			So(s.limiter.GetLimits(), ShouldBeEmpty)
			So(groups[racGroupOf(low[0])].count, ShouldEqual, 4)
			So(groups[racGroupOf(high[0])].count, ShouldEqual, 4)
		})
	})

	Convey("The cap trims running over-count without inflating skipped (§2b-skipped)", t, func() {
		// COMMENT A: the shared budget guarantees summed READY count <= remaining
		// capacity, so the amount the cap trims is exactly the running over-count
		// (drift). That is NOT deferred ready work, so it must not be added to
		// skipped: the genuine ready backlog is already recorded by the rac cycle,
		// and inflating skipped with running units would pin the target at the limit
		// with no ready work to backfill (re-over-provisioning).
		limit := 100
		initialHeld := 20
		readyBacklog := limit + 50
		drift := 30

		s := newOverProvisionServer(limit)
		jobs := racReadyJobs("rc", 200, 0, lg, readyBacklog)
		grpName := racGroupOf(jobs[0])

		// initialHeld slots are held when the ready budget is read => remaining = 80.
		for range initialHeld {
			s.limiter.Increment(ctx, []string{"lg"})
		}

		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, jobs)

		readyCount := groups[grpName].count
		readySkipped := groups[grpName].skipped

		So(readyCount, ShouldEqual, limit-initialHeld)                  // 80 counted
		So(readySkipped, ShouldEqual, readyBacklog-(limit-initialHeld)) // 70 skipped backlog

		// drift more reserves land after the read; all totalRunning jobs are running.
		totalRunning := initialHeld + drift

		q := queue.New(ctx, "reliable3-skipped-not-inflated")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		added, err := addRunningJobs(ctx, q, grpName, 0, totalRunning)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, totalRunning)

		s.accountForRunningJobs(q, groups)

		Convey("count is capped at the limit (pre-cap it was ready+running > limit)", func() {
			So(groups[grpName].count, ShouldEqual, limit)
		})

		Convey("skipped still reflects ONLY the ready backlog, not the trimmed running over-count", func() {
			So(groups[grpName].skipped, ShouldEqual, readySkipped)
		})

		Convey("a completion is absorbed by the ready-backlog skipped, so the target does not ratchet down", func() {
			s.previouslyScheduledGroups[grpName] = groups[grpName]
			So(s.hasSkippedScheduledGroups(), ShouldBeTrue)

			countBefore := groups[grpName].count
			So(groups[grpName].decrement(1), ShouldEqual, -1)
			So(groups[grpName].count, ShouldEqual, countBefore)
		})
	})
}
