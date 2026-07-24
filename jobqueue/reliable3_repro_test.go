//go:build reliability_repro

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

// This file holds EXPERIMENTAL reproducers for the three remaining reliable3
// reliability issues. They are gated behind the `reliability_repro` build tag so
// they are NOT part of `make test`; run them with:
//
//	go test -tags reliability_repro ./jobqueue/ -run <TestName>
//
// (or via the developers/wrdev.sh overcount-check / limit-stall-check /
// priority-fairness-check commands). Unlike the shipped red-until-fixed TDD
// tests, these reproducers ASSERT THE BUGGY BEHAVIOUR: they PASS on current
// (unfixed) code, demonstrating each defect, and would flip if the corresponding
// fix were applied (except the limit-stall one - see its comment).
//
// They deliberately exercise the real accounting primitives (countJobInGroup,
// accountForRunningJobs, seedLimitGroupBudgets, the limiter, and
// scheduler.ProcessNotRunningOnHost) rather than a full manager, so each defect
// is shown at the smallest faithful level. Helpers newOverProvisionServer and
// opEnvInt are shared with reliable3_overprovision_test.go.

package jobqueue

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

// reproCaptureCtx returns a context whose clog output is captured into the
// returned buffer, so a test can assert whether a code path logged anything.
func reproCaptureCtx(ctx context.Context) (context.Context, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	handler := log15.StreamHandler(buf, log15.LogfmtFormat())

	return clog.ContextWithLogHandler(ctx, handler), buf
}

// TestReliable3OverCountRunningSnapshot reproduces reliable3 ISSUE 2b: a single
// scheduler group's final scheduling count can exceed its limit group's limit,
// because countJobInGroup caps the READY count against remaining capacity read
// EARLY (when seedLimitGroupBudgets first reads GetRemainingCapacity), while
// accountForRunningJobs later adds ALL run-sub-queue jobs of the group on top
// with no limit check. Reserves that land between those two reads make the
// running snapshot larger than the early capacity read reflected, so
//
//	finalCount = cappedReady + running = (limit - initialRunning) + (initialRunning + window) = limit + window
//
// which exceeds the limit by the number of window reserves (production saw
// count=3313 for a 2000 limit). Deterministic: the interleaving is controlled
// directly. Scale knobs: WR_OP_LIMIT, WR_RC_INITIAL, WR_RC_WINDOW (keep
// WR_RC_INITIAL + WR_RC_WINDOW <= WR_OP_LIMIT: you cannot have more jobs running
// than the limit).
func TestReliable3OverCountRunningSnapshot(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	req := &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}

	Convey("A group's count exceeds its limit when reserves land between the capacity read and the running snapshot", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		initialRunning := opEnvInt("WR_RC_INITIAL", 300)
		windowReserves := opEnvInt("WR_RC_WINDOW", 1500)
		if initialRunning+windowReserves > limit {
			windowReserves = limit - initialRunning
		}
		readyBacklog := limit + windowReserves + 100

		s := newOverProvisionServer(limit)
		grpName := "200:30:1:1:samehash" + jobSchedLimitGroupSeparator + "lg"

		// (1) initialRunning jobs are already reserved (holding limit slots) at the
		// instant buildSchedulerGroups first reads this limit group's capacity.
		for range initialRunning {
			s.limiter.Increment(ctx, []string{"lg"})
		}
		So(s.limiter.GetRemainingCapacity(ctx, []string{"lg"}), ShouldEqual, limit-initialRunning)

		// (2) buildSchedulerGroups counts the ready backlog. seedLimitGroupBudgets
		// reads GetRemainingCapacity NOW (= limit - initialRunning) and caps the
		// ready count there.
		groups := make(map[string]*sgroup)
		groupLimits := make(map[string]int)

		for range readyBacklog {
			s.countJobInGroup(ctx, groups, groupLimits, schedulerGroupSnapshot{
				group:        grpName,
				requirements: req,
				priority:     0,
			})
		}

		cappedReady := groups[grpName].count

		// (3) THE NON-ATOMIC WINDOW: windowReserves more jobs reserve slots (limiter
		// usage grows) AFTER the capacity read above, and are now in the run queue.
		for range windowReserves {
			s.limiter.Increment(ctx, []string{"lg"})
		}

		totalRunning := initialRunning + windowReserves

		q := queue.New(ctx, "reliable3-overcount-repro")
		defer func() { _ = q.Destroy() }()

		defs := make([]*queue.ItemDef, 0, totalRunning)
		for i := range totalRunning {
			job := &Job{}
			job.setSchedulerGroup(grpName)
			defs = append(defs, &queue.ItemDef{
				Key:          fmt.Sprintf("run-%d", i),
				ReserveGroup: grpName,
				Data:         job,
				TTR:          time.Hour,
				StartQueue:   queue.SubQueueRun,
			})
		}

		added, _, err := q.AddMany(ctx, defs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, totalRunning)

		// (4) accountForRunningJobs adds ALL running jobs on top, with no limit check.
		s.accountForRunningJobs(q, groups)
		finalCount := groups[grpName].count

		t.Logf("OVERCOUNT-REPRO: limit=%d cappedReady=%d running=%d => finalCount=%d (exceeds limit by %d)",
			limit, cappedReady, totalRunning, finalCount, finalCount-limit)

		Convey("the final count exceeds the limit (the 2b over-count bug is present)", func() {
			So(finalCount, ShouldBeGreaterThan, limit)
		})
	})
}

// TestReliable3LimitSlotStall reproduces reliable3 ISSUE 1's CONSEQUENCE: once a
// limit group is full of phantom slots (reserved-then-lost jobs parked in
// SubQueueRun whose limit slots were never released because death-confirmation
// never succeeds - see TestReliable3SilentConfirmFailure), every new ready job
// in that limit group is limit-skipped, so scheduling stalls. Modelled at the
// limiter + countJobInGroup level (faithful to the mechanism: the limiter is the
// single source of truth for slot occupancy). Scale knobs: WR_OP_LIMIT (phantom
// slots) and WR_STALL_READY (new ready jobs).
//
// NB: a "loud only" fix (logging could-not-determine) does NOT change this
// reproducer - the phantom slots still exhaust the limit, so it still passes.
// That is the point: loud-only surfaces the stall, it does not remove it.
func TestReliable3LimitSlotStall(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	req := &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}

	Convey("Phantom slots from unconfirmable lost jobs exhaust the limit group and skip all new ready jobs", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		newReady := opEnvInt("WR_STALL_READY", 5000)

		s := newOverProvisionServer(limit)
		grpName := "200:30:1:1:samehash" + jobSchedLimitGroupSeparator + "lg"

		// fill the limit group with `limit` phantom slots: reserved-then-lost jobs
		// whose limit slots were never released (confirmation never succeeds).
		for range limit {
			s.limiter.Increment(ctx, []string{"lg"})
		}

		// the limiter is now full: a further increment is refused.
		So(s.limiter.Increment(ctx, []string{"lg"}), ShouldBeFalse)
		So(s.limiter.GetRemainingCapacity(ctx, []string{"lg"}), ShouldEqual, 0)

		groups := make(map[string]*sgroup)
		groupLimits := make(map[string]int)

		for range newReady {
			s.countJobInGroup(ctx, groups, groupLimits, schedulerGroupSnapshot{
				group:        grpName,
				requirements: req,
				priority:     0,
			})
		}

		g := groups[grpName]

		t.Logf("LIMIT-STALL-REPRO: limit=%d phantomSlots=%d newReady=%d => scheduled=%d skipped=%d",
			limit, limit, newReady, g.count, g.skipped)

		Convey("no new ready jobs are scheduled - the limit group is stalled by phantom slots", func() {
			So(g.count, ShouldEqual, 0)
			So(g.skipped, ShouldEqual, newReady)
		})
	})
}

// TestReliable3SilentConfirmFailure reproduces reliable3 ISSUE 1's ROOT: the
// death-confirmation path fails SILENTLY, so a broken reclaim masquerades as a
// healthy manager. Two facets:
//
//  1. scheduler.ProcessNotRunningOnHost returns false ("still running / cannot
//     confirm") on a getHost/ssh failure with NO log - the mock scheduler's
//     getHost returns (nil,false), which is exactly the could-not-determine case.
//  2. lsf.initialize swallows a private-key read error (the `if err == nil`
//     store), so a mis-pathed key leaves ssh permanently broken with NO log.
//
// The "loud only" fix would add a warn to each; this reproducer then flips
// (a log appears). Note this only makes the failure VISIBLE - it does not stop
// the resulting stall (see TestReliable3LimitSlotStall).
func TestReliable3SilentConfirmFailure(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("ProcessNotRunningOnHost silently returns cannot-confirm (false) when the host is unavailable", t, func() {
		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc: func(context.Context, string) {},
		})
		So(err, ShouldBeNil)

		logCtx, buf := reproCaptureCtx(ctx)
		notRunning := sched.ProcessNotRunningOnHost(logCtx, 12345, "unreachable-host")

		t.Logf("SILENT-CONFIRM-REPRO: ProcessNotRunningOnHost returned notRunning=%v (expected false), log=%q",
			notRunning, buf.String())

		Convey("it reports the job as still running (false), i.e. does not confirm death", func() {
			So(notRunning, ShouldBeFalse)
		})

		Convey("and emits NO log about the could-not-determine outcome (the silent-failure bug)", func() {
			So(buf.String(), ShouldBeEmpty)
		})
	})

	Convey("lsf.initialize swallows a private-key read error with no log", t, func() {
		logCtx, buf := reproCaptureCtx(ctx)
		_, err := scheduler.New(logCtx, "lsf", &scheduler.ConfigLSF{
			Deployment:     "development",
			Shell:          "bash",
			PrivateKeyPath: "/nonexistent/wr-reliable3-repro-key",
		})

		if err != nil {
			t.Logf("KEY-SWALLOW-REPRO: SKIPPED - lsf.initialize needs a working LSF here: %v", err)
			SkipConvey("LSF not usable on this host, cannot exercise lsf.initialize", func() {})

			return
		}

		t.Logf("KEY-SWALLOW-REPRO: lsf.initialize err=%v, log=%q", err, buf.String())

		Convey("the bad key path produces no key-related warning (the silent-swallow bug)", func() {
			So(strings.ToLower(buf.String()), ShouldNotContainSubstring, "key")
		})
	})
}

// TestReliable3PriorityFairnessStarvation reproduces reliable3 ISSUE 2a's
// remaining refinement: the shared per-limit-group budget is allocated
// FIRST-COME across sibling scheduler groups. buildSchedulerGroups scans ready
// jobs in scheduler-group (map) order, not priority order, so if a low-priority
// sibling is scanned first it consumes the whole shared budget and a
// higher-priority sibling gets nothing. Deterministic: feed the low-priority
// sibling's ready jobs first, then the high-priority sibling's. Scale knob:
// WR_OP_LIMIT and WR_PF_READY (extra ready per group beyond the limit).
func TestReliable3PriorityFairnessStarvation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	req := &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}

	Convey("A low-priority sibling scanned first starves a higher-priority sibling of the shared budget", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		readyPerGroup := limit + opEnvInt("WR_PF_READY", 500)

		lowGrp := "100:30:1:1:samehash" + jobSchedLimitGroupSeparator + "lg"  // priority 0
		highGrp := "200:30:1:1:samehash" + jobSchedLimitGroupSeparator + "lg" // priority 250

		s := newOverProvisionServer(limit)

		groups := make(map[string]*sgroup)
		groupLimits := make(map[string]int)

		// low-priority sibling scanned first (arbitrary map order in production)...
		for range readyPerGroup {
			s.countJobInGroup(ctx, groups, groupLimits, schedulerGroupSnapshot{
				group:        lowGrp,
				requirements: req,
				priority:     0,
			})
		}

		// ...then the high-priority sibling, by which point the budget is gone.
		for range readyPerGroup {
			s.countJobInGroup(ctx, groups, groupLimits, schedulerGroupSnapshot{
				group:        highGrp,
				requirements: req,
				priority:     250,
			})
		}

		t.Logf("PRIORITY-FAIRNESS-REPRO: limit=%d low(pri0).count=%d high(pri250).count=%d high.skipped=%d",
			limit, groups[lowGrp].count, groups[highGrp].count, groups[highGrp].skipped)

		Convey("the higher-priority sibling is starved (the first-come allocation bug is present)", func() {
			So(groups[lowGrp].count, ShouldEqual, limit)
			So(groups[highGrp].count, ShouldEqual, 0)
			So(groups[highGrp].skipped, ShouldBeGreaterThan, 0)
		})
	})
}
