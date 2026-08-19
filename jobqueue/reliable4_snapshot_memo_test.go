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

// Untagged behavioural regression tests for reliable4 FINDING 3 of the 2026-08-17
// live production profiling run (.docs/reliable4/prod-run-20260817.md): the
// O(backlog) rac pre-pass (Server.snapshotReadyJobs -> Job.schedulerGroupSnapshot)
// recomputed 2 MD5s, a sort and several allocations for EVERY ready job on EVERY
// rac cycle, even though every input of those derived strings is invariant unless
// the job's Requirements/LimitGroups/Cmd/Cwd/mounts/container fields change. A
// 245x A/B on the live manager measured 61,000 limit-blocked ready jobs burning
// 19,640ms of CPU per 25s (41.8% of it in schedulerGroupSnapshot) for zero real
// work, versus 80ms per 25s with a single ready job.
//
// The first test is the shape of that A/B: N ready jobs in a limit-0 group, no
// runners, nothing schedulable, driving repeated rac cycles. Per-cycle derivation
// work must be O(1) in N (memoised), not O(N). The second test is its necessary
// companion: the memo must be INVALIDATED by every way the server legitimately
// changes those inputs, so the derived scheduler group and key never go stale.
//
// Both tests deliberately use flat root Convey blocks rather than nested ones:
// GoConvey re-runs the enclosing block for every nested leaf, which would rebuild
// the 20,000-job backlog several times over and make these slow.

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// memoBacklog is the ready backlog size the reproducer builds. It is large
	// enough that an O(N) per-cycle MD5+sort+allocate pre-pass is unmistakable
	// (both in the derivation counter and in the allocation delta) while still
	// keeping the test to a couple of seconds.
	memoBacklog = 20000

	// memoLimitGroup gives every backlog job a limit group with a limit of ZERO,
	// so all of them are ready-but-blocked: nothing is ever schedulable, no job's
	// requirements are recomputed, and a rac cycle therefore does no legitimate
	// per-job work at all. This is exactly the live-manager A/B's shape.
	memoLimitGroup = "reliable4-memo-backlog:0"

	// memoSteadyCycles is how many further rac cycles are driven over the
	// unchanged backlog after the first (cold) one.
	memoSteadyCycles = 3

	// memoMaxMallocsPerJob bounds the heap allocations a steady-state rac cycle
	// may make per ready job. The memoised pre-pass allocates nothing per job (it
	// reads cached strings under the job's read lock); what remains is the
	// candidates slice and the limit-group bookkeeping of readyJobLimitBlocked
	// (schedGroupToLimitGroups splits the group name), a handful per job. The
	// un-memoised pre-pass adds reqForScheduler, Key()'s buffer+MD5+hex and
	// Stringify's key slice+builder+MD5+two Sprintfs on top, which measured 18
	// allocations per job, comfortably over this bound.
	memoMaxMallocsPerJob = 8

	// memoInvalidationJobs is how many jobs the invalidation test adds: one per
	// legitimate mutation path that has to start from a fresh memo (the limit group
	// job is then reused for the re-normalisation check).
	memoInvalidationJobs = 4

	// memoTestRAM and memoTestTime are the fixed requirements of the backlog
	// jobs, and memoModifiedRAM is a different RAM used to check that a
	// legitimate requirements change invalidates the memo.
	memoTestRAM     = 100
	memoTestTime    = 10 * time.Second
	memoModifiedRAM = 4000

	// memoRecommendedDisk is a learned disk requirement, used to check the
	// jobOverrideAlwaysUseJobReqs path: the backlog jobs (like the great majority
	// of real jobs) never specified a disk, so a learned one is applied to them
	// whatever their override.
	memoRecommendedDisk = 50
)

func TestReliable4SchedulerGroupSnapshotMemoised(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("rac cycles over an unchanged limit-blocked ready backlog do no per-job derivation", t, func() {
		server, allitemdata := memoBacklogServer(t, ctx, memoBacklog)

		// the first cycle derives each ready job's strings once. Every job starts
		// cold (nothing derives at add time, and the paused server runs no cycle
		// of its own), so this is exactly one derivation per ready job.
		cold := memoDerivationsDuring(allitemdata, func() {
			server.buildSchedulerGroups(ctx, server.q, allitemdata, "true")
		})
		So(cold, ShouldEqual, memoBacklog)
		So(int(server.racScanWork.Load()), ShouldEqual, 0) // nothing is schedulable

		// ...and further cycles over the unchanged backlog derive nothing at all.
		// Pre-fix this was memoBacklog per cycle: O(N) MD5+sort+allocate.
		steady := int64(0)

		for range memoSteadyCycles {
			steady += memoDerivationsDuring(allitemdata, func() {
				server.buildSchedulerGroups(ctx, server.q, allitemdata, "true")
			})
		}

		So(steady, ShouldEqual, 0)

		// cumulatively, then, these memoSteadyCycles+1 cycles over memoBacklog
		// ready jobs cost exactly one derivation per job, not one per job per
		// cycle. That is the two assertions above restated as a total, and it is
		// the form that would still hold if a future change let some other cycle
		// run concurrently with these: derivedLocked re-checks under the job's
		// write lock, so no job can derive twice unless something invalidated its
		// memo in between.
		So(memoDerivations(allitemdata), ShouldEqual, memoBacklog)

		// a steady-state cycle also allocates little per ready job
		mallocs := memoMallocsDuring(func() {
			server.buildSchedulerGroups(ctx, server.q, allitemdata, "true")
		})
		So(mallocs/uint64(memoBacklog), ShouldBeLessThan, uint64(memoMaxMallocsPerJob))

		// the memoised strings are byte-identical to a fresh computation, so the
		// persisted-and-parsed scheduler group name format is unchanged
		job, ok := allitemdata[0].(*Job)
		So(ok, ShouldBeTrue)

		snapshot := job.schedulerGroupSnapshot()
		So(snapshot.key, ShouldEqual, job.Key())
		So(snapshot.group, ShouldEqual, schedulerGroupString(reqForScheduler(job.Requirements), job.LimitGroups))
		So(snapshot.group, ShouldContainSubstring, jobSchedLimitGroupSeparator)
	})
}

func TestReliable4SchedulerGroupSnapshotInvalidated(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("every legitimate change to a job's derived scheduler-group inputs invalidates the memo", t, func() {
		server, allitemdata := memoBacklogServer(t, ctx, memoInvalidationJobs)

		reqJob, ok := allitemdata[0].(*Job)
		So(ok, ShouldBeTrue)
		limitJob, ok := allitemdata[1].(*Job)
		So(ok, ShouldBeTrue)
		cmdJob, ok := allitemdata[2].(*Job)
		So(ok, ShouldBeTrue)
		overrideJob, ok := allitemdata[3].(*Job)
		So(ok, ShouldBeTrue)

		// a requirements change, made the way prepareReadyJob makes it for a
		// schedulable job whose ReqGroup has learned different resources
		beforeReq := reqJob.schedulerGroupSnapshot().group

		reqJob.Lock()
		updateJobRequirementsForRetry(reqJob, jobOverridePreferSystemReqs,
			&jqs.Requirements{RAM: memoModifiedRAM, Time: memoTestTime})
		reqJob.Unlock()

		afterReq := reqJob.schedulerGroupSnapshot()
		So(afterReq.group, ShouldNotEqual, beforeReq)
		So(afterReq.group, ShouldEqual, schedulerGroupString(reqForScheduler(reqJob.Requirements), reqJob.LimitGroups))
		So(afterReq.requirements.RAM, ShouldBeGreaterThan, memoTestRAM)

		// the same change for a job that always overrides the learned values
		// (`wr add --override always`): that only overrides the resources the user
		// actually specified, so a learned value for one they did not specify (here
		// disk, unset on the great majority of jobs, as documented at cmd/add.go's
		// "If you choose to override eg. only disk...") is still applied - on the
		// early-return path through updateJobRequirementsForRetry.
		beforeOverride := overrideJob.schedulerGroupSnapshot().group

		overrideJob.Lock()
		overrideJob.Override = jobOverrideAlwaysUseJobReqs
		updateJobRequirementsForRetry(overrideJob, overrideJob.Override,
			&jqs.Requirements{RAM: memoModifiedRAM, Disk: memoRecommendedDisk, Time: memoTestTime})
		overrideJob.Unlock()

		So(overrideJob.Requirements.Disk, ShouldEqual, memoRecommendedDisk) // learned, not specified
		So(overrideJob.Requirements.RAM, ShouldEqual, memoTestRAM)          // specified, so kept

		afterOverride := overrideJob.schedulerGroupSnapshot()
		So(afterOverride.group, ShouldNotEqual, beforeOverride)
		So(afterOverride.group, ShouldEqual,
			schedulerGroupString(reqForScheduler(overrideJob.Requirements), overrideJob.LimitGroups))
		So(afterOverride.requirements.Disk, ShouldEqual, memoRecommendedDisk)

		// a limit group change, made the way a client's `wr modify` makes it
		beforeLimit := limitJob.schedulerGroupSnapshot().group

		limitModifier := NewJobModifer()
		limitModifier.SetLimitGroups([]string{"reliable4-memo-other"})

		_, err := limitModifier.Modify([]*Job{limitJob}, server)
		So(err, ShouldBeNil)

		afterLimit := limitJob.schedulerGroupSnapshot()
		So(afterLimit.group, ShouldNotEqual, beforeLimit)
		So(afterLimit.group, ShouldEqual,
			schedulerGroupString(reqForScheduler(limitJob.Requirements), limitJob.LimitGroups))
		So(afterLimit.group, ShouldContainSubstring, "reliable4-memo-other")

		// a Cmd change, which changes the memoised Key
		beforeCmd := cmdJob.schedulerGroupSnapshot().key

		cmdModifier := NewJobModifer()
		cmdModifier.SetCmd("echo reliable4-memo-modified")

		_, err = cmdModifier.Modify([]*Job{cmdJob}, server)
		So(err, ShouldBeNil)

		afterCmd := cmdJob.schedulerGroupSnapshot()
		So(afterCmd.key, ShouldNotEqual, beforeCmd)
		So(afterCmd.key, ShouldEqual, cmdJob.Key())

		// re-normalising a job's limit groups (what the server does after a limit
		// group modification) also invalidates it
		beforeNormalise := limitJob.schedulerGroupSnapshot().group

		limitJob.Lock()
		limitJob.LimitGroups = []string{"reliable4-memo-normalised"}
		server.handleUserSpecifiedJobLimitGroups(limitJob, make(map[string]*limiter.GroupData))
		limitJob.Unlock()

		afterNormalise := limitJob.schedulerGroupSnapshot()
		So(afterNormalise.group, ShouldNotEqual, beforeNormalise)
		So(afterNormalise.group, ShouldContainSubstring, "reliable4-memo-normalised")
	})
}

// memoBacklogServer starts an isolated PAUSED server (so the only rac cycles that
// ever run are the ones the test drives itself, see below), adds n limit-0 (so
// permanently ready-but-blocked) jobs to it, waits for them all to be in the queue
// and returns the server and the ready item data a rac cycle receives. The server
// and client are torn down when the test ends.
func memoBacklogServer(t *testing.T, ctx context.Context, n int) (*Server, []any) {
	t.Helper()

	serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	t.Cleanup(func() { server.Stop(ctx, true) })

	// pause the server before adding anything, for the rest of the test. This
	// server has no runner command, so its own background rac cycles take
	// buildSchedulerGroups' rc == "" branch, which runs prepareReadyJob for
	// EVERY ready job (a fresh database still yields a non-nil, all-zero
	// recommendation) and therefore INVALIDATES every job's memo. Left running,
	// one of those cycles races the ones the tests drive below, in BOTH
	// directions: its cheap pre-pass can warm memos a driven cold cycle has not
	// reached yet (17,392 derivations instead of 20,000, seen in the wild at
	// host load ~100), and its invalidating half can make jobs that cycle
	// already derived cold again (21,580 instead of 20,000, 5 failures out of 5
	// when such a cycle is triggered deliberately). Pausing is how the server
	// itself makes the queue quiescent for a bulk change (see handleModify), it
	// does not gate Add, and it leaves the measured path - the
	// buildSchedulerGroups calls the tests make directly - byte for byte the
	// same. So every derivation counted below is one a test caused, and the
	// counts are exact rather than merely usually right.
	paused, err := server.Pause()
	So(err, ShouldBeNil)
	So(paused, ShouldBeTrue)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	t.Cleanup(func() { disconnect(jq) })

	added, existed, err := jq.Add(memoBacklogJobs(n), envVars, true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, n)
	So(existed, ShouldEqual, 0)

	So(pollUntil(func() bool {
		server.rpmutex.Lock()
		running := server.racRunning
		server.rpmutex.Unlock()

		return !running && len(server.q.AllItems()) == n
	}), ShouldBeTrue)

	allitemdata := itemData(server.q.AllItems())
	So(len(allitemdata), ShouldEqual, n)

	return server, allitemdata
}

// memoDerivationsDuring returns how many times the given jobs actually computed
// their expensive derived scheduler-group strings while fn ran. It deliberately
// counts only these jobs (see Job.derivations), so that another live server in the
// same test binary deriving for its own jobs cannot perturb the exact-equality
// assertions made on the result.
func memoDerivationsDuring(jobs []any, fn func()) int64 {
	before := memoDerivations(jobs)

	fn()

	return memoDerivations(jobs) - before
}

// memoDerivations returns the total number of expensive derived-scheduler-group
// computations the given jobs have made so far.
func memoDerivations(jobs []any) int64 {
	total := int64(0)

	for _, data := range jobs {
		job, ok := data.(*Job)
		if !ok {
			continue
		}

		job.RLock()
		total += int64(job.derivations)
		job.RUnlock()
	}

	return total
}

// memoMallocsDuring returns how many heap objects were allocated while fn ran.
func memoMallocsDuring(fn func()) uint64 {
	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	fn()

	runtime.ReadMemStats(&after)

	return after.Mallocs - before.Mallocs
}

// BenchmarkReadyBacklogSnapshot records the per-rac-cycle cost of the O(backlog)
// pre-pass over an unchanging limit-blocked ready backlog: one op is one whole
// cycle's worth of Job.schedulerGroupSnapshot calls over memoBacklog ready jobs,
// which is what Server.snapshotReadyJobs does on every rac cycle. A cold cycle is
// run before the timer starts, so every timed op measures a STEADY-STATE cycle -
// the case the live manager was spending 0.79 cores on. The reported allocs/op
// (with -benchmem) is the headline signal: un-memoised it is many allocations per
// ready job (reqForScheduler, Key's buffer+MD5+hex, Stringify's key slice+builder+
// MD5+Sprintfs), memoised it is zero, and ns/op falls by orders of magnitude.
func BenchmarkReadyBacklogSnapshot(b *testing.B) {
	jobs := memoBacklogJobs(memoBacklog)

	// a cold cycle first, so the timed ops measure repeat cycles over an
	// unchanged backlog rather than the one-time derivation.
	groups := memoSnapshotCycle(jobs)
	if groups == 0 {
		b.Fatal("no scheduler groups derived")
	}

	b.ResetTimer()

	for range b.N {
		groups += memoSnapshotCycle(jobs)
	}

	b.StopTimer()

	if groups == 0 {
		b.Fatal("no scheduler groups derived")
	}
}

// memoBacklogJobs builds n jobs that all share one limit group with a limit of
// zero (so they are all ready-but-blocked), each with a distinct Cmd (so each has
// a distinct Key) and a non-empty Requirements.Other, matching the real
// production job shape that makes both MD5 paths of the pre-pass live.
func memoBacklogJobs(n int) []*Job {
	jobs := make([]*Job, 0, n)

	for i := range n {
		jobs = append(jobs, &Job{
			Cmd:      fmt.Sprintf("echo reliable4-memo-backlog %d", i),
			Cwd:      testCwd,
			ReqGroup: "reliable4-memo",
			Requirements: &jqs.Requirements{
				RAM:   memoTestRAM,
				Time:  memoTestTime,
				Cores: 1,
				Other: map[string]string{"scheduler_queues_avoid": "interactive,inference"},
			},
			RepGroup:    "reliable4-memo",
			LimitGroups: []string{memoLimitGroup},
		})
	}

	return jobs
}

// memoSnapshotCycle takes one scheduler-group snapshot of every given job, as the
// rac pre-pass does, returning the total length of the group names it saw (so the
// work cannot be optimised away).
func memoSnapshotCycle(jobs []*Job) int {
	total := 0

	for _, job := range jobs {
		total += len(job.schedulerGroupSnapshot().group)
	}

	return total
}
