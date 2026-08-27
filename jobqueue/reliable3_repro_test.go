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

// This file holds the reliable3 reliability checks that run outside `make test`,
// behind the `reliability_repro` build tag:
//
//	go test -tags reliability_repro ./jobqueue/ -run <TestName>
//
// (or via the developers/wrdev.sh overcount-check / limit-stall-check /
// priority-fairness-check commands).
//
// They began as reproducers that ASSERTED THE BUGGY BEHAVIOUR, so each PASSed on
// PRE-fix code. Three of the four defects have since been fixed on this branch, so
// those three now assert the FIXED INVARIANT instead - a reproducer whose exit
// code says "the bug is present" is worse than useless once the bug is gone,
// because a zero exit then means the opposite of what a reader assumes:
//
//   - TestReliable3OverCountRunningSnapshot builds the 2b over-count arrangement
//     and asserts the summed count is capped at the limit group's limit
//     (capGroupCountsToLimits). It still checks that the arrangement really does
//     over-count BEFORE the cap, so it cannot pass vacuously.
//   - TestReliable3LimitSlotStall asserts the limiter invariant it always
//     asserted - a limit group whose slots are all held schedules nothing more -
//     which is correct behaviour, not a defect; only its framing changed.
//   - TestReliable3ConfirmFailureIsLoud (was TestReliable3SilentConfirmFailure)
//     asserts that a death-confirmation that cannot succeed, and an unreadable
//     ssh private key, each WARN, naming the host and pid / the key path.
//
// TestReliable3PriorityFairnessStarvation is the exception and stays INVERTED,
// because reliable3 issue 2a's priority-fairness defect is STILL PRESENT: the
// shared per-limit-group budget is handed out first-come, so a low-priority
// sibling scanned first starves a higher-priority one. Its PASS therefore means
// "the bug reproduced", which is why its wrdev.sh mode prints a banner saying so
// rather than letting a zero exit read as an invariant holding. See
// .docs/reliable4/prod-validation-260827.md for that outstanding item.
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

// TestReliable3OverCountRunningSnapshot builds the exact arrangement of reliable3
// ISSUE 2b and asserts the FIXED invariant: a scheduler group's final scheduling
// count never exceeds its limit group's limit.
//
// The arrangement: countJobInGroup caps the READY count against remaining capacity
// read EARLY (when seedLimitGroupBudgets first reads GetRemainingCapacity), and
// accountForRunningJobs then adds ALL run-sub-queue jobs of the group on top.
// Reserves that land between those two reads make the running snapshot larger than
// the early capacity read reflected, so the count BEFORE any cap is
//
//	cappedReady + running = (limit - initialRunning) + (initialRunning + window) = limit + window
//
// ie. over the limit by the number of window reserves (production saw count=3313
// for a 2000 limit). capGroupCountsToLimits, called at the end of
// accountForRunningJobs, trims the summed sibling counts back to the limit.
//
// Both halves are asserted, because either alone would be a false PASS: that the
// UNCAPPED count really does exceed the limit (or the arrangement is not
// reproducing the over-count at all, and the cap is being credited for nothing),
// and that the capped count is at or under it while still asking for runners (a
// cap that trimmed everything to zero would satisfy an upper bound alone). Removing
// the capGroupCountsToLimits call turns this red.
//
// Deterministic: the interleaving is controlled directly. Scale knobs: WR_OP_LIMIT,
// WR_RC_INITIAL, WR_RC_WINDOW (keep WR_RC_INITIAL + WR_RC_WINDOW <= WR_OP_LIMIT:
// you cannot have more jobs running than the limit).
func TestReliable3OverCountRunningSnapshot(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	req := &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}

	Convey("A group's count stays within its limit when reserves land between the capacity read and the running snapshot", t, func() {
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

		// (4) accountForRunningJobs adds ALL running jobs on top, then
		// capGroupCountsToLimits trims the summed sibling counts back to the limit.
		// uncappedCount is what the group's count would be without that trim, ie. the
		// over-count this arrangement exists to produce.
		uncappedCount := cappedReady + totalRunning

		s.accountForRunningJobs(q, groups)
		finalCount := groups[grpName].count

		t.Logf("OVERCOUNT-REPRO: limit=%d cappedReady=%d running=%d => uncappedCount=%d "+
			"(over the limit by %d) finalCount=%d (over the limit by %d)",
			limit, cappedReady, totalRunning, uncappedCount, uncappedCount-limit,
			finalCount, finalCount-limit)

		Convey("the arrangement really does over-count before the cap", func() {
			So(uncappedCount, ShouldBeGreaterThan, limit)
		})

		Convey("the final count does not exceed the limit (the 2b over-count is capped)", func() {
			So(finalCount, ShouldBeLessThanOrEqualTo, limit)
		})

		Convey("and runners are still requested for the work that fits", func() {
			So(finalCount, ShouldBeGreaterThan, 0)
		})
	})
}

// TestReliable3LimitSlotStall asserts the limiter invariant behind reliable3 ISSUE
// 1's CONSEQUENCE: a limit group whose slots are ALL held schedules nothing more,
// however long the ready backlog behind it is - every new ready job is
// limit-skipped rather than scheduled. That is correct behaviour and must stay
// (breaking it is the over-provisioning family overprovision-check guards), so
// unlike the other reliable3 checks in this file nothing about this test's
// assertions needed to change; only the framing did.
//
// What made it a reproducer was where the held slots came from in production:
// phantom slots, ie. reserved-then-lost jobs parked in SubQueueRun whose limit
// slots were never released because death-confirmation never succeeded. This test
// does not exercise that cause at all - it holds the slots by incrementing the
// limiter directly - so its PASS says nothing about whether phantom slots can
// still form. The confirmation path itself is covered by
// TestReliable3ConfirmFailureIsLoud here, and by the reliable4 runner-pid liveness
// and lost-runner-backstop tests in the main suite.
//
// Modelled at the limiter + countJobInGroup level, faithful to the mechanism: the
// limiter is the single source of truth for slot occupancy. Scale knobs:
// WR_OP_LIMIT (held slots) and WR_STALL_READY (new ready jobs).
func TestReliable3LimitSlotStall(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	req := &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}

	Convey("A limit group whose slots are all held skips every new ready job", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		newReady := opEnvInt("WR_STALL_READY", 5000)

		s := newOverProvisionServer(limit)
		grpName := "200:30:1:1:samehash" + jobSchedLimitGroupSeparator + "lg"

		// hold every one of the limit group's slots, as production's phantom slots
		// did: reserved-then-lost jobs whose slots were never released.
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

		t.Logf("LIMIT-STALL-REPRO: limit=%d heldSlots=%d newReady=%d => scheduled=%d skipped=%d",
			limit, limit, newReady, g.count, g.skipped)

		Convey("no new ready jobs are scheduled, and every one is recorded as skipped", func() {
			So(g.count, ShouldEqual, 0)
			So(g.skipped, ShouldEqual, newReady)
		})
	})
}

// TestReliable3ConfirmFailureIsLoud asserts the FIXED invariant for reliable3
// ISSUE 1's ROOT. That root was silence: the death-confirmation path failed with no
// log at all, so a manager whose reclaim was completely broken looked healthy, and
// the phantom-slot stall it caused (TestReliable3LimitSlotStall) had no visible
// cause. Two facets, one assertion each:
//
//  1. scheduler.ProcessNotRunningOnHost must WARN when it cannot determine whether
//     the process is alive. It still returns false ("still running / cannot
//     confirm"), which is the safe answer - a job is only re-run once its runner is
//     CONFIRMED dead (DEVELOPERS.md rule 4) - but it must say so. The mock
//     scheduler's getHost returns (nil,false), which is exactly the
//     could-not-determine case (Scheduler.warnCannotConfirm).
//  2. lsf.initialize must WARN when the configured ssh private key cannot be read.
//     It used to swallow the error (an `if err == nil` store), so a mis-pathed key
//     left ssh - and therefore every death confirmation - permanently broken and
//     silent (lsf.loadPrivateKey).
//
// The warnings are asserted to NAME the thing an operator has to act on (the host
// and pid; the key path), not merely to be non-empty: a bare "something went wrong"
// would leave the operator exactly where the silent version did. Removing either
// warn turns this red.
//
// Facet 2 needs a usable LSF, so it SKIPS where there is none. The gate reports
// which facets were measured rather than treating a skip as coverage: see the
// CONFIRM-LOUD-REPRO line, which is what developers/wrdev.sh limit-stall-check
// parses.
func TestReliable3ConfirmFailureIsLoud(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("ProcessNotRunningOnHost warns when it cannot confirm whether the process is alive", t, func() {
		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc: func(context.Context, string) {},
		})
		So(err, ShouldBeNil)

		logCtx, buf := reproCaptureCtx(ctx)
		notRunning := sched.ProcessNotRunningOnHost(logCtx, 12345, "unreachable-host")
		logged := buf.String()

		t.Logf("CONFIRM-LOUD-REPRO: cannotConfirmWarned=%v namesHost=%v namesPid=%v notRunning=%v log=%q",
			logged != "", strings.Contains(logged, "unreachable-host"), strings.Contains(logged, "12345"),
			notRunning, logged)

		Convey("it still reports the job as maybe-running (false), i.e. does not confirm death", func() {
			So(notRunning, ShouldBeFalse)
		})

		Convey("and it warns, naming the host and pid an operator has to look at", func() {
			So(logged, ShouldNotBeEmpty)
			So(strings.ToLower(logged), ShouldContainSubstring, "could not confirm")
			So(logged, ShouldContainSubstring, "unreachable-host")
			So(logged, ShouldContainSubstring, "12345")
		})
	})

	Convey("lsf.initialize warns when the configured private key cannot be read", t, func() {
		badKey := "/nonexistent/wr-reliable3-repro-key"

		logCtx, buf := reproCaptureCtx(ctx)
		_, err := scheduler.New(logCtx, "lsf", &scheduler.ConfigLSF{
			Deployment:     "development",
			Shell:          "bash",
			PrivateKeyPath: badKey,
		})

		if err != nil {
			t.Logf("KEY-WARN-REPRO: keyWarnMeasured=false SKIPPED - lsf.initialize needs a working LSF here: %v",
				err)
			SkipConvey("LSF not usable on this host, cannot exercise lsf.initialize", func() {})

			return
		}

		logged := buf.String()

		t.Logf("KEY-WARN-REPRO: keyWarnMeasured=true keyWarned=%v namesPath=%v log=%q",
			strings.Contains(strings.ToLower(logged), "private key"), strings.Contains(logged, badKey), logged)

		Convey("the unreadable key path produces a warning that names it", func() {
			So(strings.ToLower(logged), ShouldContainSubstring, "private key")
			So(logged, ShouldContainSubstring, badKey)
		})
	})
}

// TestReliable3PriorityFairnessStarvation reproduces reliable3 ISSUE 2a's
// remaining refinement, which is STILL PRESENT: the shared per-limit-group budget
// is allocated FIRST-COME across sibling scheduler groups. buildSchedulerGroups
// scans ready jobs in scheduler-group (map) order, not priority order, so if a
// low-priority sibling is scanned first it consumes the whole shared budget and a
// higher-priority sibling gets nothing.
//
// THIS TEST IS INVERTED and deliberately stays that way: it asserts the BUGGY
// behaviour, so it PASSES while the defect exists. A zero exit here does NOT mean
// an invariant holds - it means the starvation reproduced. developers/wrdev.sh
// priority-fairness-check prints a banner saying exactly that, because the one
// thing worse than an inverted reproducer is one whose exit code is mistaken for a
// green invariant. Flipping it to permanently red was considered and rejected: a
// gate that can only ever fail stops being read.
//
// When the defect IS fixed, this test goes red and the wrdev.sh mode says so and
// asks for it to be converted into a regression gate (high-priority sibling gets
// its share of the budget). The outstanding item is recorded in
// .docs/reliable4/prod-validation-260827.md.
//
// Deterministic: feed the low-priority sibling's ready jobs first, then the
// high-priority sibling's. Scale knobs: WR_OP_LIMIT and WR_PF_READY (extra ready
// per group beyond the limit).
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

		t.Logf("PRIORITY-FAIRNESS-REPRO: limit=%d readyPerGroup=%d low(pri0).count=%d "+
			"high(pri250).count=%d high.skipped=%d",
			limit, readyPerGroup, groups[lowGrp].count, groups[highGrp].count, groups[highGrp].skipped)

		Convey("the higher-priority sibling is starved (the first-come allocation bug is STILL present)", func() {
			So(groups[lowGrp].count, ShouldEqual, limit)
			So(groups[highGrp].count, ShouldEqual, 0)
			So(groups[highGrp].skipped, ShouldBeGreaterThan, 0)
		})
	})
}
