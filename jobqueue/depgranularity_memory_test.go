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

// Memory proof for dep-group dependency granularity (spec F1). This is the
// property whose absence killed production five times: recovering 150,472 live
// jobs, an -inuse_space profile put 97.55% of a 15.65 GB live heap in
// recoveredItemDef while the decoded jobs themselves were 376 MB (2.40%) of it.
// The bytes were the jobs' dependency KEY LISTS - one key per live member of
// every dep group they waited on - so retention was waiters x members and the
// heap reached 140 GB against a 182.7 GB node.
//
// Two assertions, one exact and one bounded. The exact one is primary because it
// can never flake: the keys retained across all recovered jobs must equal the
// declared edge count, and must not move when the group's membership grows
// tenfold. The bounded one measures the actual retained heap.

import (
	"context"
	"runtime"
	"strconv"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// dgmWaiters is F1's W: the live jobs waiting on the one dep group. It is the
	// same in both fixtures; only the membership varies.
	dgmWaiters = 2000

	// dgmSmallMembers and dgmBigMembers are F1's two M's, an order of magnitude
	// apart. Pre-change every waiter retained one dependency key per live member,
	// so these two fixtures retained 400,000 and 4,000,000 keys; the point of the
	// change is that both now retain dgmWaiters.
	dgmSmallMembers = 200
	dgmBigMembers   = 2000

	// dgmMembersRatio is how much bigger the big fixture's membership is, which
	// is also how much more the pre-change code retained for it.
	dgmMembersRatio = dgmBigMembers / dgmSmallMembers

	// dgmMaxBytesPerJob is F1 test 2's bound on retained heap per resolved job.
	// Retention that still scaled with membership costs kilobytes per job at
	// dgmSmallMembers and tens of kilobytes at dgmBigMembers.
	dgmMaxBytesPerJob = 2 * 1024

	// dgmMembershipGrowth is how much more the big fixture may retain per
	// resolved job than the small one. Retention that scaled with membership
	// would be dgmMembersRatio here, not 1.5.
	dgmMembershipGrowth = 1.5

	dgmGroup          = "depgranularity-memory-group"
	dgmMemberRepGroup = "depgranularity-memory-members"
	dgmWaiterRepGroup = "depgranularity-memory-waiters"
)

// dgmResolution is what one fixture's resolution pass retained.
type dgmResolution struct {
	members int
	jobs    int
	keys    int
	growth  uint64
}

// dgmResolve builds the F1 fixture with the given membership, resolves every one
// of its live jobs the way prior-state recovery does, and reports what the
// resolution retained.
func dgmResolve(t *testing.T, ctx context.Context, members int) dgmResolution {
	t.Helper()

	testDB, server, priorJobs := dgrResolutionFixture(t, ctx, dgmFixtureJobs(members))

	var (
		resolved   []resolvedJob
		resolveErr error
	)

	growth := dgmRetainedGrowth(func() any {
		resolved, resolveErr = testDB.resolveDependencies(ctx, priorJobs, server.depGroups)

		return resolved
	})

	// the fixture has to outlive the measurement as well as its result. server,
	// priorJobs and testDB are all last used inside fn, so without this the
	// collector takes the dep-group state out of the heap between the two reads -
	// one map per member job - and the big fixture's growth reads as a net loss.
	runtime.KeepAlive(server)
	runtime.KeepAlive(priorJobs)
	runtime.KeepAlive(testDB)

	So(resolveErr, ShouldBeNil)
	So(resolved, ShouldHaveLength, len(priorJobs))

	keys := 0
	for _, rj := range resolved {
		keys += len(rj.deps)
	}

	return dgmResolution{members: members, jobs: len(resolved), keys: keys, growth: growth}
}

// bytesPerJob is the retained heap divided by the jobs resolved for it, which is
// the figure F1 test 2 bounds: a per-job cost is what an operator can multiply
// by their live-job count to size a recovery.
func (r dgmResolution) bytesPerJob() float64 {
	if r.jobs == 0 {
		return 0
	}

	return float64(r.growth) / float64(r.jobs)
}

// TestDepGranularityResolutionMemory covers F1 acceptance tests 1 and 2: the
// dependency keys a recovery pass retains, and the heap they take, are both a
// function of how many edges the user declared and not of how many jobs are in
// the group each edge names.
func TestDepGranularityResolutionMemory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given one dep group resolved at two memberships an order of magnitude apart", t, func() {
		small := dgmResolve(t, ctx, dgmSmallMembers)
		big := dgmResolve(t, ctx, dgmBigMembers)

		t.Logf("M=%d: %d jobs, %d keys, %d bytes retained (%.0f bytes/job)",
			small.members, small.jobs, small.keys, small.growth, small.bytesPerJob())
		t.Logf("M=%d: %d jobs, %d keys, %d bytes retained (%.0f bytes/job)",
			big.members, big.jobs, big.keys, big.growth, big.bytesPerJob())

		Convey("The retained key count is the declared edge count at both memberships", func() {
			So(small.keys, ShouldEqual, dgmWaiters)
			So(big.keys, ShouldEqual, dgmWaiters)

			// the two fixtures really do differ in the dimension that used to
			// drive retention, and every live job really was resolved.
			So(big.members, ShouldEqual, dgmMembersRatio*small.members)
			So(small.jobs, ShouldEqual, dgmSmallMembers+dgmWaiters)
			So(big.jobs, ShouldEqual, dgmBigMembers+dgmWaiters)
		})

		Convey("The retained heap per resolved job is bounded and does not grow with membership", func() {
			So(small.bytesPerJob(), ShouldBeLessThan, dgmMaxBytesPerJob)
			So(big.bytesPerJob(), ShouldBeLessThan, dgmMaxBytesPerJob)
			So(big.bytesPerJob(), ShouldBeLessThanOrEqualTo, dgmMembershipGrowth*small.bytesPerJob())

			// a measurement of nothing would satisfy every bound above.
			So(small.growth, ShouldBeGreaterThan, 0)
			So(big.growth, ShouldBeGreaterThan, 0)
		})
	})
}

// TestDepGranularityRecoveredMemberships covers F1 acceptance test 3: what a
// whole serve recovery leaves in memory, rather than what one pass returns. The
// per-group state holds one entry per (group, live member) pair, and the queue
// holds one dependency key per declared edge - the two halves of the target
// representation, measured on the fixture whose membership used to multiply them
// together.
func TestDepGranularityRecoveredMemberships(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database of members and waiters recovered by a full serve", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgmSeedLiveJobs(ctx, t, serverConfig, dgmBigMembers)

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		Convey("It holds one membership per member and one dependency key per waiter", func() {
			So(server.depGroups.memberships(), ShouldEqual, dgmBigMembers)
			So(dgaGroupMembers(server.depGroups, dgmGroup), ShouldHaveLength, dgmBigMembers)

			items := server.q.AllItems()
			So(items, ShouldHaveLength, dgmBigMembers+dgmWaiters)

			total, onlyTheGroupKey := dgmItemDependencies(items, depGroupDependencyKey(dgmGroup))

			So(total, ShouldEqual, dgmWaiters)
			So(onlyTheGroupKey, ShouldEqual, dgmWaiters)
		})
	})
}

// dgmSeedLiveJobs writes the F1 fixture into the server config's database, so a
// real serve recovers it. The members go in before the waiters because
// prepareNewJobs scans the previously stored waiters of every dep group a new job
// belongs to, and would decode all dgmWaiters of them for each member otherwise.
func dgmSeedLiveJobs(ctx context.Context, t *testing.T, config ServerConfig, members int) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	jobs := dgmFixtureJobs(members)

	queued, _, _, err := testDB.storeNewJobs(ctx, jobs[:members], false)
	So(err, ShouldBeNil)
	So(queued, ShouldHaveLength, members)

	queued, _, _, err = testDB.storeNewJobs(ctx, jobs[members:], false)
	So(err, ShouldBeNil)
	So(queued, ShouldHaveLength, dgmWaiters)

	So(testDB.close(ctx), ShouldBeNil)
}

// dgmFixtureJobs returns F1's fixture: members live jobs in the one dep group,
// and dgmWaiters live jobs waiting on that group and belonging to no group of
// their own - so the fixture's whole membership is members, and its whole
// declared edge count is dgmWaiters, whatever members is.
func dgmFixtureJobs(members int) []*Job {
	jobs := make([]*Job, 0, members+dgmWaiters)

	for i := range members {
		job := testDBJob("echo dgm member "+strconv.Itoa(i), dgmMemberRepGroup)
		job.DepGroups = []string{dgmGroup}
		jobs = append(jobs, job)
	}

	for i := range dgmWaiters {
		job := testDBJob("echo dgm waiter "+strconv.Itoa(i), dgmWaiterRepGroup)
		job.Dependencies = Dependencies{NewDepGroupDependency(dgmGroup)}
		jobs = append(jobs, job)
	}

	return jobs
}

// dgmItemDependencies returns the total number of dependency keys across the
// items, and how many of them depend on exactly the one key want.
func dgmItemDependencies(items []*queue.Item, want string) (total, exactlyOne int) {
	for _, item := range items {
		deps := item.Dependencies()
		total += len(deps)

		if len(deps) == 1 && deps[0] == want {
			exactlyOne++
		}
	}

	return total, exactlyOne
}

// dgmRetainedGrowth returns how many heap bytes fn's result is still holding
// after it has run, with that result held live across the second measurement so
// the collector cannot take back the very thing being measured.
//
// It reads HeapAlloc rather than go-conventions' HeapInuse because F1 test 2
// compares two of these figures against each other with a 1.5x bound: after a
// collection HeapAlloc is exactly the live object bytes, whereas HeapInuse counts
// whole spans and so carries fragmentation left by the maps resolution allocates
// and frees per job, which on a few hundred KB of signal is a flake rather than a
// measurement.
func dgmRetainedGrowth(fn func() any) uint64 {
	var before, after runtime.MemStats

	dgmCollect()
	runtime.ReadMemStats(&before)

	kept := fn()

	dgmCollect()
	runtime.ReadMemStats(&after)

	runtime.KeepAlive(kept)

	if after.HeapAlloc <= before.HeapAlloc {
		return 0
	}

	return after.HeapAlloc - before.HeapAlloc
}

// dgmCollect collects twice, because HeapAlloc counts dead objects until they
// have been swept and one runtime.GC() can return with the previous cycle's
// sweep still outstanding. Measured with a single collection, the fixture's own
// garbage was still in the baseline, was swept during the resolution, and the
// growth came out as an underflow-guarded zero on most runs - a measurement of
// nothing, which satisfies every bound F1 test 2 sets.
func dgmCollect() {
	runtime.GC()
	runtime.GC()
}
