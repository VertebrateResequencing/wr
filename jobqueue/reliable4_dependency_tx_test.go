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

// Transaction-cost regression tests for dependency resolution
// (.docs/bugfixes/260825-2.md). Prior-state recovery resolves the dependencies
// of every live job, and production paid one or more bolt read transactions per
// job for it: 150,472 of them for jobs with no dependencies at all, against a
// brand-new (wholly cold) 7 GB database on NFS.
//
// The cost is measured with bbolt's own read-transaction counter, Stats().TxN,
// by the tests in reliable4_dependency_tx_bolt_test.go, so they assert the real
// number of transactions
// rather than a proxy for it; a wall-clock gate cannot see the difference on
// warm local disk. Alongside each count is an assertion that the resolved keys
// and waited-for dep groups are exactly what they were before the change, for
// every kind of dependency.
//
// The recovery pass is bounded both ways: it must not cost a transaction per job
// (the bug), and it must not resolve the whole pass in one transaction either,
// since a read transaction holds bolt's mmaplock for its whole life and a
// growing write cannot proceed until it ends. Hence the chunk-count assertions
// at a deliberately small dependencyResolutionChunkSize.

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// depTxLiveGroup is a dep group with live members, depTxGoneGroup one that
	// has been seen but whose only job is no longer live, and
	// depTxNeverSeenGroup one no job has ever been added with.
	depTxLiveGroup      = "deptx-live-group"
	depTxGoneGroup      = "deptx-gone-group"
	depTxNeverSeenGroup = "deptx-never-seen-group"

	depTxLiveCmd = "echo deptx essence target"
	depTxGoneCmd = "echo deptx gone"

	// depTxResolutions is how many jobs each transaction count is measured over,
	// chosen so a per-job transaction is unmistakable in the count.
	depTxResolutions = 100

	// depTxReadingJobs and depTxReadFreeJobs partition depTxRecoveryJobs'
	// depTxResolutions jobs by whether resolving one costs a bolt read
	// transaction. Of the seven kinds it cycles through, five have to ask the
	// database something - the "ever seen" check for a dep group with no live
	// member, or the checkIfLive for an essence dependency - which is 70 jobs. The
	// other 30 have no dependencies at all, or name only depTxLiveGroup, and a dep
	// group with a live member is answered from memory.
	depTxReadingJobs  = 70
	depTxReadFreeJobs = 30

	// depTxChunkSize is the chunk size the multi-chunk tests drive
	// dependencyResolutionChunkSize down to; it divides depTxResolutions into
	// more than one chunk, which the production default (1000) would not.
	depTxChunkSize = 10

	// depTxProdChunkSize is the chunk size production recovers at, and
	// depTxProdPassJobs a job count that spans more than one chunk of it, so that
	// "exactly ceil(N/1000)" is a real division rather than the single chunk
	// depTxResolutions jobs would make of it.
	depTxProdChunkSize  = 1000
	depTxProdPassJobs   = 2500
	depTxProdPassChunks = 3

	// the end-to-end restart fixture: depTxE2EParents jobs carrying
	// depTxE2EGroup, depTxE2EChildren jobs depending on it, and one job waiting
	// on depTxE2EFutureGroup, which no job is ever added with.
	depTxE2ERepGroup    = "reliable4_deptx_e2e"
	depTxE2EGroup       = "reliable4-deptx-e2e-parents"
	depTxE2EFutureGroup = "reliable4-deptx-e2e-future"
	depTxE2EParents     = 3
	depTxE2EChildren    = 20

	// depTxBigGroup is a dep group grown from depTxBigMembers to
	// depTxBigGrownMembers live members, to show the resolved key count does not
	// scale with membership.
	depTxBigGroup        = "deptx-big-group"
	depTxBigMembers      = 500
	depTxBigGrownMembers = 5000
)

// depTxFixture is a database holding the live jobs, dep groups and dep group
// history that the dependency-resolution tests resolve against.
type depTxFixture struct {
	// s is the store holding the fixture, and db that store when it is a bbolt
	// *db opened by newDepTxFixture, for the tests that count its
	// transactions.
	s  store
	db *db

	// liveGroupKeys are the keys of the live jobs in depTxLiveGroup, and are what
	// groups records as that group's members. They are no longer an expected
	// resolution result: a dep group resolves to its own one key, never to its
	// members'.
	liveGroupKeys []string

	// liveKey is the key of a live job with no dep group, for essence
	// dependencies.
	liveKey string

	// groups is the per-group live-member state the fixture's live jobs
	// represent, which dependency resolution answers "has this dep group a live
	// member?" from.
	groups *depGroupMembers
}

// newDepTxFixture builds the fixture in a bbolt database. The caller must close
// fixture.db.
func newDepTxFixture(t *testing.T, ctx context.Context) *depTxFixture {
	t.Helper()

	tmpdir := t.TempDir()

	testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
	So(err, ShouldBeNil)

	fixture := seedDepTxFixture(ctx, testDB)
	fixture.db = testDB

	return fixture
}

// newDepTxStoreFixture builds the fixture in a fresh store of the given kind,
// closed when the Convey block ends.
func newDepTxStoreFixture(t *testing.T, ctx context.Context, kind storeKind) *depTxFixture {
	t.Helper()

	s, _ := openTestStore(t, ctx, kind)

	return seedDepTxFixture(ctx, s)
}

// seedDepTxFixture stores the fixture's jobs in s and returns the fixture.
func seedDepTxFixture(ctx context.Context, s store) *depTxFixture {
	first := testDBJob("echo deptx first", "deptx")
	first.DepGroups = []string{depTxLiveGroup}
	second := testDBJob("echo deptx second", "deptx")
	second.DepGroups = []string{depTxLiveGroup}
	gone := testDBJob(depTxGoneCmd, "deptx")
	gone.DepGroups = []string{depTxGoneGroup}
	live := testDBJob(depTxLiveCmd, "deptx")

	stored, err := s.storeNewJobsGuarded(ctx, []*Job{first, second, gone, live}, false, nil)
	So(err, ShouldBeNil)
	So(stored.jobsToQueue, ShouldHaveLength, 4)

	// gone leaves depTxGoneGroup seen, with a historical depgroupToKey entry but
	// no live member - the state of a dep group whose jobs have all been
	// archived.
	So(s.deleteLiveJobs(ctx, []string{gone.Key()}), ShouldBeNil)

	liveGroupKeys := []string{first.Key(), second.Key()}
	slices.Sort(liveGroupKeys)

	// gone was deleted from the live bucket, so depTxGoneGroup has no live
	// member and only first and second are members of anything.
	groups := newDepGroupMembers()

	for _, key := range liveGroupKeys {
		groups.add([]string{depTxLiveGroup}, key)
	}

	return &depTxFixture{s: s, liveGroupKeys: liveGroupKeys, liveKey: live.Key(), groups: groups}
}

// soResolves resolves deps against the fixture, asserting no error.
func (f *depTxFixture) soResolves(deps Dependencies) ([]string, []string) {
	keys, waiting, err := deps.dependencyKeys(f.db, f.groups)
	So(err, ShouldBeNil)

	return keys, waiting
}

// TestReliable4DependencyResolutionUnchanged pins the resolved dependency keys
// and waited-for dep groups for every kind of dependency, so a change to what
// dependency resolution costs cannot quietly change what it answers.
//
// One answer is deliberately different from the one this test originally pinned:
// a dep group now resolves to its own single opaque key instead of one key per
// live member job. Everything else - which kinds block, which are satisfied, and
// which groups are reported as waited for - is the same partition as before.
func TestReliable4DependencyResolutionUnchanged(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database holding live jobs in dep groups", t, func() {
		fixture := newDepTxFixture(t, ctx)

		defer func() {
			So(fixture.db.close(ctx), ShouldBeNil)
		}()

		Convey("No dependencies resolves to no keys and no waited-for groups", func() {
			var deps Dependencies

			keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{})
			So(waiting, ShouldResemble, []string{})
		})

		Convey("A dep group with live members resolves to that group's own key", func() {
			deps := Dependencies{NewDepGroupDependency(depTxLiveGroup)}

			keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{depGroupDependencyKey(depTxLiveGroup)})
			So(waiting, ShouldResemble, []string{})

			// the group has more than one live member, so this is the granularity
			// change and not just a one-member coincidence.
			So(fixture.liveGroupKeys, ShouldHaveLength, 2)
		})

		Convey("A seen dep group with no live members resolves to no keys", func() {
			deps := Dependencies{NewDepGroupDependency(depTxGoneGroup)}

			keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{})
			So(waiting, ShouldResemble, []string{})
		})

		Convey("A never seen dep group resolves to that group's key and is waited for", func() {
			deps := Dependencies{NewDepGroupDependency(depTxNeverSeenGroup)}

			keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{depGroupDependencyKey(depTxNeverSeenGroup)})
			So(waiting, ShouldResemble, []string{depTxNeverSeenGroup})
		})

		Convey("An essence dependency on a live job resolves to that job's key", func() {
			deps := Dependencies{NewEssenceDependency(depTxLiveCmd, "")}

			keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{fixture.liveKey})
			So(waiting, ShouldResemble, []string{})
		})

		Convey("An essence dependency on a job that is not live resolves to no keys", func() {
			deps := Dependencies{NewEssenceDependency(depTxGoneCmd, "")}

			keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{})
			So(waiting, ShouldResemble, []string{})
		})

		Convey("Mixed dependencies resolve to the sorted union of their keys", func() {
			deps := Dependencies{
				NewDepGroupDependency(depTxLiveGroup),
				NewDepGroupDependency(depTxNeverSeenGroup),
				NewEssenceDependency(depTxLiveCmd, ""),
			}

			want := []string{
				depGroupDependencyKey(depTxLiveGroup),
				depGroupDependencyKey(depTxNeverSeenGroup),
				fixture.liveKey,
			}
			slices.Sort(want)

			keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, want)
			So(waiting, ShouldResemble, []string{depTxNeverSeenGroup})
		})
	})
}

// TestStoreRecoveryDependencyPass proves that resolveDependencies, the
// dependency pass of prior-state recovery, resolves every job exactly as
// per-job resolution does, and that a cancelled context ends the pass with
// context.Canceled before it marks any job as waiting on a dep group.
// TestReliable4RecoveryDependencyPass pins what the pass costs bbolt.
func TestStoreRecoveryDependencyPass(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store of live jobs and a recovery's worth of jobs to resolve",
			storeKindName(kind)), t, func() {
			fixture := newDepTxStoreFixture(t, ctx, kind)
			jobs := depTxRecoveryJobs(depTxResolutions)

			Convey("The pass resolves every job exactly as per-job resolution does", func() {
				depTxSoResolvesAsPerJob(ctx, fixture, jobs)
			})

			Convey("A cancelled context ends the pass instead of resolving every job", func() {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				resolved, err := fixture.s.resolveDependencies(cancelled, jobs, fixture.groups)
				So(err, ShouldWrap, context.Canceled)
				So(resolved, ShouldBeNil)

				waiting := 0

				for _, job := range jobs {
					if len(job.WaitingForDepGroups) > 0 {
						waiting++
					}
				}

				So(waiting, ShouldEqual, 0)
			})
		})
	})
}

// depTxRecoveryJobs returns count jobs covering every kind of dependency, as
// prior-state recovery hands them to the dependency pass.
func depTxRecoveryJobs(count int) []*Job {
	kinds := []Dependencies{
		nil,
		{NewDepGroupDependency(depTxLiveGroup)},
		{NewDepGroupDependency(depTxGoneGroup)},
		{NewDepGroupDependency(depTxNeverSeenGroup)},
		{NewEssenceDependency(depTxLiveCmd, "")},
		{NewEssenceDependency(depTxGoneCmd, "")},
		{NewDepGroupDependency(depTxLiveGroup), NewEssenceDependency(depTxLiveCmd, "")},
	}

	jobs := make([]*Job, 0, count)

	for i := range count {
		job := testDBJob("echo deptx recovered "+strconv.Itoa(i), "deptx")
		job.Dependencies = kinds[i%len(kinds)]

		jobs = append(jobs, job)
	}

	return jobs
}

// depTxSoResolvesAsPerJob resolves the jobs one at a time, then as a pass at the
// current chunk size, and asserts the pass paired every job with exactly the
// keys and waited-for dep groups per-job resolution gives it - so neither the
// pass nor a chunk boundary within it can change an answer.
func depTxSoResolvesAsPerJob(ctx context.Context, fixture *depTxFixture, jobs []*Job) {
	wantKeys := make([][]string, len(jobs))
	wantWaiting := make([][]string, len(jobs))
	failed := 0

	for i, job := range jobs {
		keys, waiting, err := job.Dependencies.dependencyKeys(fixture.s, fixture.groups)
		if err != nil {
			failed++
		}

		wantKeys[i], wantWaiting[i] = keys, waiting
	}

	resolved, err := fixture.s.resolveDependencies(ctx, jobs, fixture.groups)

	So(failed, ShouldEqual, 0)
	So(err, ShouldBeNil)
	So(resolved, ShouldHaveLength, len(jobs))

	differed, withKeys, withWaiting := 0, 0, 0

	for i, rj := range resolved {
		if rj.job != jobs[i] {
			differed++
		}

		if !slices.Equal(rj.deps, wantKeys[i]) {
			differed++
		}

		// setWaitingForDepGroups records an empty set as nil.
		want := wantWaiting[i]
		if len(want) == 0 {
			want = nil
		}

		if !slices.Equal(rj.job.WaitingForDepGroups, want) {
			differed++
		}

		if len(rj.deps) > 0 {
			withKeys++
		}

		if len(rj.job.WaitingForDepGroups) > 0 {
			withWaiting++
		}
	}

	So(differed, ShouldEqual, 0)

	// the comparison is not vacuous: the jobs include ones that resolve to keys
	// and ones left waiting on a never seen dep group.
	So(withKeys, ShouldBeGreaterThan, 0)
	So(withWaiting, ShouldBeGreaterThan, 0)
}

// setDependencyResolutionChunkSize sets the dependencyResolutionChunkSize
// package var to n and returns a function that restores it.
func setDependencyResolutionChunkSize(n int) func() {
	prev := dependencyResolutionChunkSize
	dependencyResolutionChunkSize = n

	return func() { dependencyResolutionChunkSize = prev }
}

// TestDepGranularityDependencyKeys pins what keeping a dep-group dependency at
// the granularity the user declared it buys: a group with live members resolves
// to the group's one key, and the resolved key count does not grow with the
// group's membership. TestDepGranularityLiveGroupTxCost pins that such a group is
// answered from memory for no bbolt read at all.
//
// What each kind of dependency resolves to is pinned once, by
// TestReliable4DependencyResolutionUnchanged, and a job with no dependencies
// costing no read transaction by TestReliable4DependencyFreeTxCost.
func TestDepGranularityDependencyKeys(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database and the per-group state its live jobs represent", t, func() {
		fixture := newDepTxFixture(t, ctx)

		defer func() {
			So(fixture.db.close(ctx), ShouldBeNil)
		}()

		Convey("A dep group with live members resolves to that group's one key", func() {
			deps, waiting := fixture.soResolves(Dependencies{NewDepGroupDependency(depTxLiveGroup)})
			So(deps, ShouldResemble, []string{depGroupDependencyKey(depTxLiveGroup)})
			So(waiting, ShouldResemble, []string{})
		})

		Convey("The resolved key count does not grow with the group's membership", func() {
			depTxGrowGroup(fixture, 0, depTxBigMembers)
			So(fixture.groups.memberships(), ShouldEqual, len(fixture.liveGroupKeys)+depTxBigMembers)

			deps, _ := fixture.soResolves(Dependencies{NewDepGroupDependency(depTxBigGroup)})
			So(deps, ShouldHaveLength, 1)

			depTxGrowGroup(fixture, depTxBigMembers, depTxBigGrownMembers)
			So(fixture.groups.memberships(), ShouldEqual, len(fixture.liveGroupKeys)+depTxBigGrownMembers)

			deps, _ = fixture.soResolves(Dependencies{NewDepGroupDependency(depTxBigGroup)})
			So(deps, ShouldHaveLength, 1)
		})
	})
}

// depTxGrowGroup records synthetic live members from through to-1 in
// depTxBigGroup.
func depTxGrowGroup(fixture *depTxFixture, from, to int) {
	for i := from; i < to; i++ {
		fixture.groups.add([]string{depTxBigGroup}, "deptx-big-member-"+strconv.Itoa(i))
	}
}

// TestReliable4RecoveryDependencyState proves that the queue state a
// DB-preserving restart recovers dependent jobs into is exactly the state they
// were in before it. The dependency keys recovery resolved are then exercised
// for real: completing the parents must release the children, while the child
// waiting on a never seen dep group stays dependent.
// TestReliable4RecoveryDependencyTxCost pins that bbolt's recovery costs fewer
// read transactions than there are jobs.
//
// Its state strings are what WaitingForDepGroups means, which the dep-group
// granularity change does not touch: never-seen groups only.
//
// Both its Connect calls stand as they are once the manager's externally
// observable surface is published only at the end of recovery. The first is
// against the first server, which the serve helper waits on; the second already
// follows waitUntilRecovered, and publication happens before recovery is marked
// finished, so there is nothing left to race.
func TestReliable4RecoveryDependencyState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a stopped server whose %s store holds ready, dependent and waiting jobs",
			storeKindName(kind)), t, func() {
			config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
			serverConfig.storeKind = kind

			beforeRestart := depTxStoppedServer(ctx, config, serverConfig, addr, standardReqs, clientConnectTime)

			serverConfig.dontWipeDevDB = true
			server, _, token, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer func() {
				server.Stop(ctx, true)
			}()

			So(waitUntilRecovered(server), ShouldBeTrue)

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			Convey("Recovery restores every job's state and waited-for dep groups", func() {
				afterRestart, errs := depTxJobStates(jq)
				So(errs, ShouldBeNil)
				So(afterRestart, ShouldResemble, beforeRestart)

				// the states really are a mix, so the comparison is not just
				// agreeing that everything is ready.
				So(beforeRestart["echo deptx e2e parent 0"], ShouldEqual, string(JobStateReady)+" waiting:")
				So(beforeRestart["echo deptx e2e child 0"], ShouldEqual, string(JobStateDependent)+" waiting:")
				So(beforeRestart["echo deptx e2e waiter"],
					ShouldEqual, string(JobStateDependent)+" waiting:"+depTxE2EFutureGroup)
			})

			Convey("The recovered dependencies release the children once the parents complete", func() {
				depTxSoReleasesChildren(ctx, jq, config.RunnerExecShell)
			})
		})
	})
}

// depTxStoppedServer serves serverConfig, adds depTxE2EJobs to it and stops it,
// returning the jobs' states as depTxJobStates reported them before the stop.
func depTxStoppedServer(ctx context.Context, config internal.Config, serverConfig ServerConfig, addr string,
	standardReqs *jqs.Requirements, clientConnectTime time.Duration,
) map[string]string {
	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	defer disconnect(jq)

	jobs := depTxE2EJobs(standardReqs)

	inserts, _, err := jq.Add(jobs, envVars, true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, len(jobs))

	states, err := depTxJobStates(jq)
	So(err, ShouldBeNil)
	So(states, ShouldHaveLength, len(jobs))

	return states
}

// depTxE2EJobs returns the jobs a DB-preserving restart will recover: parents
// that carry a dep group, children that depend on it, and one child left waiting
// on a dep group no job has ever been added with.
func depTxE2EJobs(reqs *jqs.Requirements) []*Job {
	job := func(cmd string) *Job {
		return &Job{
			Cmd: cmd, Cwd: testCwd, ReqGroup: reqGroupFake, Requirements: reqs,
			RepGroup: depTxE2ERepGroup,
		}
	}

	jobs := make([]*Job, 0, depTxE2EParents+depTxE2EChildren+1)

	for i := range depTxE2EParents {
		parent := job("echo deptx e2e parent " + strconv.Itoa(i))
		parent.DepGroups = []string{depTxE2EGroup}
		jobs = append(jobs, parent)
	}

	for i := range depTxE2EChildren {
		child := job("echo deptx e2e child " + strconv.Itoa(i))
		child.Dependencies = Dependencies{NewDepGroupDependency(depTxE2EGroup)}
		jobs = append(jobs, child)
	}

	waiter := job("echo deptx e2e waiter")
	waiter.Dependencies = Dependencies{NewDepGroupDependency(depTxE2EFutureGroup)}

	return append(jobs, waiter)
}

// depTxSoReleasesChildren completes the recovered parents and asserts that the
// children become ready while the waiter stays dependent.
func depTxSoReleasesChildren(ctx context.Context, jq *Client, shell string) {
	// the parents are the only ready jobs, so each reserve gets one of them; a
	// manager-spawned runner may win one first, in which case it completes that
	// parent instead of us.
	for range depTxE2EParents {
		reserved, errr := jq.Reserve(time.Second)
		So(errr, ShouldBeNil)

		if reserved == nil {
			continue
		}

		So(reserved.DepGroups, ShouldResemble, []string{depTxE2EGroup})
		execute(ctx, jq, reserved, shell)
	}

	ready := func() bool {
		states, errs := depTxJobStates(jq)
		if errs != nil {
			return false
		}

		for i := range depTxE2EChildren {
			if states["echo deptx e2e child "+strconv.Itoa(i)] != string(JobStateReady)+" waiting:" {
				return false
			}
		}

		return true
	}

	So(pollUntil(ready), ShouldBeTrue)

	states, errs := depTxJobStates(jq)
	So(errs, ShouldBeNil)
	So(states["echo deptx e2e waiter"],
		ShouldEqual, string(JobStateDependent)+" waiting:"+depTxE2EFutureGroup)
}

// depTxJobStates returns each of the rep group's jobs' state and waited-for dep
// groups, keyed by command, as the queue currently reports them, along with any
// error from asking.
//
// It returns the error instead of asserting on it because it is called from a
// pollUntil condition, which polls every 20ms for up to 30s: a So() in there
// fires ~1500 assertions on a single failure, which go-conventions bans. Every
// caller asserts once, outside the poll.
func depTxJobStates(jq *Client) (map[string]string, error) {
	jobs, err := jq.GetByRepGroup(depTxE2ERepGroup, false, 0, "", false, false)
	if err != nil {
		return nil, err
	}

	states := make(map[string]string, len(jobs))
	for _, job := range jobs {
		states[job.Cmd] = string(job.State) + " waiting:" + strings.Join(job.WaitingForDepGroups, ",")
	}

	return states, nil
}
