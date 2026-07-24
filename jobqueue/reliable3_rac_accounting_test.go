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
// behaviour (they fail on the pre-fix accounting and pass after the fix); the
// build-tagged reliable3_repro_test.go reproducers assert the old buggy behaviour.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// countReadySnapshots feeds n copies of a ready-job snapshot for the given
// scheduler group and priority into the slice, for driving countReadyJobsByPriority.
func readySnapshots(snapshots []schedulerGroupSnapshot, group string, priority uint8,
	req *scheduler.Requirements, n int) []schedulerGroupSnapshot {
	for range n {
		snapshots = append(snapshots, schedulerGroupSnapshot{
			group:        group,
			requirements: req,
			priority:     priority,
		})
	}

	return snapshots
}

// addRunningJobs adds n jobs to the queue's run sub-queue, all in the given
// scheduler group, so accountForRunningJobs counts them. Returns how many were
// added and any error, for the caller to assert.
func addRunningJobs(ctx context.Context, q *queue.Queue, group string, n int) (int, error) {
	defs := make([]*queue.ItemDef, 0, n)

	for i := range n {
		job := &Job{}
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
	req := &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}
	lgSuffix := jobSchedLimitGroupSeparator + "lg"

	Convey("A single group's count never exceeds its limit once running jobs are added (§2b)", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		initialRunning := opEnvInt("WR_RC_INITIAL", 300)
		windowReserves := opEnvInt("WR_RC_WINDOW", 1500)

		if initialRunning+windowReserves > limit {
			windowReserves = limit - initialRunning
		}

		s := newOverProvisionServer(limit)
		grpName := "200:30:1:1:samehash" + lgSuffix

		// initialRunning jobs hold slots when the ready budget is read...
		for range initialRunning {
			s.limiter.Increment(ctx, []string{"lg"})
		}

		groups := make(map[string]*sgroup)
		snapshots := readySnapshots(nil, grpName, 0, req, limit+windowReserves+100)
		s.countReadyJobsByPriority(ctx, groups, snapshots)

		// ...then windowReserves more reserve AFTER that read (the non-atomic window)
		// and land in the run queue.
		for range windowReserves {
			s.limiter.Increment(ctx, []string{"lg"})
		}

		totalRunning := initialRunning + windowReserves

		q := queue.New(ctx, "reliable3-overcount-fix")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		added, err := addRunningJobs(ctx, q, grpName, totalRunning)
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
		snapshots := []schedulerGroupSnapshot(nil)

		for i := range siblings {
			grpNames[i] = fmt.Sprintf("%d:30:1:1:samehash", 100+i*100) + lgSuffix
			snapshots = readySnapshots(snapshots, grpNames[i], 0, req, limit)
		}

		// ready budget is read now (nothing running yet) => summed ready == limit.
		groups := make(map[string]*sgroup)
		s.countReadyJobsByPriority(ctx, groups, snapshots)

		q := queue.New(ctx, "reliable3-overcount-siblings")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		// each sibling then gets running jobs on top (reserves that landed after).
		for i := range siblings {
			s.limiter.Increment(ctx, []string{"lg"})

			added, err := addRunningJobs(ctx, q, grpNames[i], runningPerSibling)
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
		grpName := "200:30:1:1:samehash" // no ~lg suffix

		groups := make(map[string]*sgroup)
		s.countReadyJobsByPriority(ctx, groups, readySnapshots(nil, grpName, 0, req, readyJobs))

		q := queue.New(ctx, "reliable3-nolimit")
		defer func() { So(q.Destroy(), ShouldBeNil) }()

		added, err := addRunningJobs(ctx, q, grpName, running)
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

		lowGrp := "100:30:1:1:samehash" + lgSuffix  // priority 0
		highGrp := "200:30:1:1:samehash" + lgSuffix // priority 250

		s := newOverProvisionServer(limit)
		groups := make(map[string]*sgroup)

		// low-priority sibling's ready jobs are scanned FIRST, high-priority LAST.
		snapshots := readySnapshots(nil, lowGrp, 0, req, readyPerGroup)
		snapshots = readySnapshots(snapshots, highGrp, 250, req, readyPerGroup)

		s.countReadyJobsByPriority(ctx, groups, snapshots)

		Convey("the high-priority sibling gets the budget, the low-priority one is starved", func() {
			So(groups[highGrp].count, ShouldEqual, limit)
			So(groups[lowGrp].count, ShouldEqual, 0)
		})
	})
}
