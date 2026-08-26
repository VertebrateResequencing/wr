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

// Recovery-side tests for dep-group dependency granularity (spec C1).
// Prior-state recovery rebuilds the per-group live-member state from the jobs it
// has already decoded, and then resolves every job's dependencies against it: one
// opaque depgroup:G key per declared group however many members it has, one bolt
// read transaction per chunk of jobs, and one bucketDepGroups get per distinct
// group with no live member however many jobs name it.
//
// The fixture databases are built by a first server, which is stopped so the live
// bucket it leaves behind is what the second server recovers. Nothing runs any of
// those jobs behind the tests' backs: jobqueueTestInit sets no ServerConfig
// RunnerCmd, and the readyAddedCallback dispatches runners only when one is set,
// so every job that runs here is one the test's own client reserved and executed.
// That is what makes the recovered queue state, and the release of a group's
// waiters by its last member's archive, assertable without polling.

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// dgrMembers and dgrWaiters are C1 test 1's M and W: the dep group has 200
	// live member jobs, and 500 live jobs wait on it.
	dgrMembers = 200
	dgrWaiters = 500

	// dgrChainJobs is C1 test 6's job count, over dgrChains chains of dep groups
	// in which every job belongs to one group and depends on the previous one.
	dgrChainJobs = 3000
	dgrChains    = 3

	// dgrPassJobs and dgrPassChunkSize are C1 test 7's: 100 recovered jobs at a
	// chunk size of 10, which is 10 chunks - more than one, and far fewer than one
	// per job.
	dgrPassJobs      = 100
	dgrPassChunkSize = 10
	dgrPassChunks    = dgrPassJobs / dgrPassChunkSize

	// dgrSeenJobs is C1 test 8's: 500 recovered jobs all naming the same
	// never-seen dep group, spanning 50 chunks at dgrPassChunkSize.
	dgrSeenJobs   = 500
	dgrSeenChunks = dgrSeenJobs / dgrPassChunkSize

	// dgrGroup is the dep group whose members and waiters C1 tests 1 to 3 use,
	// dgrGoneGroup one whose only member has completed, and dgrNeverSeenGroup one
	// no job has ever been added with.
	dgrGroup          = "depgranularity-recovery-group"
	dgrGoneGroup      = "depgranularity-recovery-gone-group"
	dgrNeverSeenGroup = "depgranularity-recovery-never-seen-group"

	dgrMemberRepGroup  = "depgranularity-recovery-members"
	dgrWaiterRepGroup  = "depgranularity-recovery-waiters"
	dgrCarrierRepGroup = "depgranularity-recovery-carrier"
	dgrChainRepGroup   = "depgranularity-recovery-chain"

	// dgrReserveWait is how long a reserve of an already-ready job is given. It is
	// generous because it costs nothing on the success path and this node runs at
	// a load average well above its core count.
	dgrReserveWait = 30 * time.Second
)

// dgrServer is a development server with its own ports, manager directory and
// database, and what a client needs to talk to it.
type dgrServer struct {
	server       *Server
	serverConfig ServerConfig
	config       internal.Config
	addr         string
	reqs         *jqs.Requirements
	connectTime  time.Duration
	token        []byte
}

// dgrStartServer starts a server with an empty database.
func dgrStartServer(ctx context.Context) *dgrServer {
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(true)

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	return &dgrServer{
		server: server, serverConfig: serverConfig, config: config,
		addr: addr, reqs: reqs, connectTime: connectTime, token: token,
	}
}

// restart stops the server and replaces it with one that keeps and recovers its
// database, returning once prior-state recovery has finished.
func (d *dgrServer) restart(ctx context.Context) {
	d.server.Stop(ctx, true)

	d.serverConfig.dontWipeDevDB = true

	server, _, token, err := serve(ctx, d.serverConfig)
	So(err, ShouldBeNil)

	d.server, d.token = server, token

	So(waitUntilRecovered(d.server), ShouldBeTrue)
}

// runReady reserves and executes count jobs, returning how many of the rep
// group's jobs ran to completion. Nothing else runs them: this server has no
// RunnerCmd, so it dispatches no runners of its own.
func (d *dgrServer) runReady(ctx context.Context, jq *Client, repGroup string, count int) int {
	ran := 0

	for range count {
		job, err := jq.Reserve(dgrReserveWait)
		if err != nil || job == nil || job.RepGroup != repGroup {
			continue
		}

		if jq.Execute(ctx, job, d.config.RunnerExecShell) != nil {
			continue
		}

		ran++
	}

	return ran
}

func (d *dgrServer) stop(ctx context.Context) {
	d.server.Stop(ctx, true)
}

func (d *dgrServer) connect() *Client {
	jq, err := Connect(d.addr, d.config.ManagerCAFile, d.config.ManagerCertDomain, d.token, d.connectTime)
	So(err, ShouldBeNil)

	return jq
}

// job returns a quick job this server can run.
func (d *dgrServer) job(cmd, repGroup string) *Job {
	return &Job{
		Cmd: cmd, Cwd: testCwd, ReqGroup: reqGroupFake, Requirements: d.reqs,
		RepGroup: repGroup,
	}
}

// groupMembers returns the dgrMembers jobs that make up dgrGroup.
func (d *dgrServer) groupMembers() []*Job {
	jobs := make([]*Job, 0, dgrMembers)

	for i := range dgrMembers {
		job := d.job("echo dgr member "+strconv.Itoa(i), dgrMemberRepGroup)
		job.DepGroups = []string{dgrGroup}
		jobs = append(jobs, job)
	}

	return jobs
}

// groupWaiters returns the dgrWaiters jobs that depend on dgrGroup and belong to
// no group of their own, so the fixture's whole membership is dgrMembers.
func (d *dgrServer) groupWaiters() []*Job {
	jobs := make([]*Job, 0, dgrWaiters)

	for i := range dgrWaiters {
		job := d.job("echo dgr waiter "+strconv.Itoa(i), dgrWaiterRepGroup)
		job.Dependencies = Dependencies{NewDepGroupDependency(dgrGroup)}
		jobs = append(jobs, job)
	}

	return jobs
}

// waiterOn returns a single job depending on the named dep group.
func (d *dgrServer) waiterOn(depGroup string) *Job {
	job := d.job("echo dgr waiter on "+depGroup, dgrWaiterRepGroup)
	job.Dependencies = Dependencies{NewDepGroupDependency(depGroup)}

	return job
}

// chainJobs returns count jobs split into chains chains, in which each job
// belongs to its own dep group and depends on the group of the job before it; the
// head of each chain depends on headDep instead.
func (d *dgrServer) chainJobs(count, chains int, headDep string) []*Job {
	jobs := make([]*Job, 0, count)

	for i := range count {
		job := d.job("echo dgr chain "+strconv.Itoa(i), dgrChainRepGroup)
		dgrChainDeps(job, i, chains, headDep)
		jobs = append(jobs, job)
	}

	return jobs
}

// dgrChainDeps makes job the i'th link of one of chains dep-group chains: it
// belongs to a group of its own, and depends on the group of the job chains
// places before it, or on headDep if it is a chain head.
func dgrChainDeps(job *Job, i, chains int, headDep string) {
	job.DepGroups = []string{dgrChainGroup(i)}

	if i < chains {
		job.Dependencies = Dependencies{NewDepGroupDependency(headDep)}

		return
	}

	job.Dependencies = Dependencies{NewDepGroupDependency(dgrChainGroup(i - chains))}
}

// TestDepGranularityRecoveryDepGroupWaiters covers C1 acceptance tests 1, 2 and
// 3 against one fixture: a dep group with dgrMembers live members and dgrWaiters
// live jobs waiting on it. The three share a fixture because building it costs
// two server startups and 700 job adds, and because they are three views of the
// same recovered state.
func TestDepGranularityRecoveryDepGroupWaiters(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped server's database holding a dep group's members and their waiters", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()
		memberKeys := dgrAddJobs(jq, d.groupMembers())
		waiterKeys := dgrAddJobs(jq, d.groupWaiters())
		disconnect(jq)

		So(memberKeys, ShouldHaveLength, dgrMembers)
		So(waiterKeys, ShouldHaveLength, dgrWaiters)

		d.restart(ctx)

		Convey("Recovery resolves each waiter to the one group key and releases them all when the members complete", func() {
			// C1 test 1: one dependency each, the group's own key, so the total is
			// W and not W*M.
			exactlyOne, total := dgrItemDeps(d.server, waiterKeys, depGroupDependencyKey(dgrGroup))
			So(exactlyOne, ShouldEqual, dgrWaiters)
			So(total, ShouldEqual, dgrWaiters)

			// C1 test 2: one (group, member) pair per member, independent of W.
			So(d.server.depGroups.memberships(), ShouldEqual, dgrMembers)

			// C1 test 3: the partition recovery produced, then the release.
			So(d.server.q.Stats().Dependant, ShouldEqual, dgrWaiters)
			So(d.server.q.Stats().Ready, ShouldEqual, dgrMembers)

			jq = d.connect()

			defer disconnect(jq)

			So(d.runReady(ctx, jq, dgrMemberRepGroup, dgrMembers), ShouldEqual, dgrMembers)

			// archiving the last member emptied the group, and that is the only
			// thing that could have released its waiters.
			So(d.server.depGroups.memberships(), ShouldEqual, 0)
			So(d.server.q.Stats().Dependant, ShouldEqual, 0)
			So(d.server.q.Stats().Ready, ShouldEqual, dgrWaiters)
		})
	})
}

// dgrAddJobs adds the jobs, asserting they were all new, and returns their keys.
func dgrAddJobs(jq *Client, jobs []*Job) []string {
	inserts, already, err := jq.Add(jobs, envVars, true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, len(jobs))
	So(already, ShouldEqual, 0)

	keys := make([]string, 0, len(jobs))
	for _, job := range jobs {
		keys = append(keys, job.Key())
	}

	return keys
}

// dgrItemDeps returns how many of the given queue items have exactly the one
// dependency want, and the total number of dependencies across all of them.
func dgrItemDeps(s *Server, keys []string, want string) (exactlyOne, total int) {
	for _, key := range keys {
		item, err := s.q.Get(key)
		if err != nil {
			continue
		}

		deps := item.Dependencies()
		total += len(deps)

		if len(deps) == 1 && deps[0] == want {
			exactlyOne++
		}
	}

	return exactlyOne, total
}

// TestDepGranularityRecoverySatisfiedAndNeverSeen covers C1 acceptance tests 4
// and 5: the two dep-group cases with no live member, which are the ones the
// database still has to be asked about.
func TestDepGranularityRecoverySatisfiedAndNeverSeen(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped server whose only live job waits on a memberless dep group", t, func() {
		Convey("A group that was seen and has no live member leaves that job ready", func() {
			d := dgrStartServer(ctx)

			defer d.stop(ctx)

			jq := d.connect()

			// completing the group's only member is what makes the group seen with
			// no live member, which is the state under test.
			carrier := d.job("echo dgr carrier", dgrCarrierRepGroup)
			carrier.DepGroups = []string{dgrGoneGroup}
			dgrAddJobs(jq, []*Job{carrier})

			reserved, err := jq.Reserve(dgrReserveWait)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, carrier.Key())
			execute(ctx, jq, reserved, d.config.RunnerExecShell)

			dgrAddJobs(jq, []*Job{d.waiterOn(dgrGoneGroup)})
			disconnect(jq)

			d.restart(ctx)

			jq = d.connect()

			defer disconnect(jq)

			waiter := dgrOnlyJob(jq, dgrWaiterRepGroup)
			So(waiter.State, ShouldEqual, JobStateReady)
			So(waiter.WaitingForDepGroups, ShouldBeEmpty)
		})

		Convey("A group no job was ever added with leaves that job dependent and waiting for it", func() {
			d := dgrStartServer(ctx)

			defer d.stop(ctx)

			jq := d.connect()
			dgrAddJobs(jq, []*Job{d.waiterOn(dgrNeverSeenGroup)})
			disconnect(jq)

			d.restart(ctx)

			jq = d.connect()

			defer disconnect(jq)

			waiter := dgrOnlyJob(jq, dgrWaiterRepGroup)
			So(waiter.State, ShouldEqual, JobStateDependent)
			So(waiter.WaitingForDepGroups, ShouldResemble, []string{dgrNeverSeenGroup})
		})
	})
}

// dgrOnlyJob returns the rep group's single live job.
func dgrOnlyJob(jq *Client, repGroup string) *Job {
	jobs, err := jq.GetByRepGroup(repGroup, false, 0, "", false, false)
	So(err, ShouldBeNil)
	So(jobs, ShouldHaveLength, 1)

	return jobs[0]
}

// TestDepGranularityRecoveryChainScale covers C1 acceptance test 6: dgrChainJobs
// live jobs in which every job both belongs to and depends on a chain of dep
// groups recover into exactly the sub-queues they were in before the restart,
// with every key present exactly once.
//
// The reference is the first server's own queue, which the add path maintaining
// the group state (D1) makes the right one: both queues are built from the same
// declared groups, one by the add path and one by recovery, so they have to
// agree. The fixture's own counts are asserted as well, because a comparison
// between two queues can be satisfied by both being wrong in the same way.
func TestDepGranularityRecoveryChainScale(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped server's database holding chains of dep-grouped jobs", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		carrier := d.job("echo dgr chain carrier", dgrCarrierRepGroup)
		carrier.DepGroups = []string{dgrGoneGroup}
		dgrAddJobs(jq, []*Job{carrier})

		reserved, err := jq.Reserve(dgrReserveWait)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		execute(ctx, jq, reserved, d.config.RunnerExecShell)

		chainKeys := dgrAddJobs(jq, d.chainJobs(dgrChainJobs, dgrChains, dgrGoneGroup))
		disconnect(jq)

		So(chainKeys, ShouldHaveLength, dgrChainJobs)

		beforeRestart := d.server.q.Stats()
		beforeMemberships := d.server.depGroups.memberships()

		d.restart(ctx)

		Convey("Recovery restores every job into the sub-queue it was in, losing and duplicating none", func() {
			stats := d.server.q.Stats()
			So(stats, ShouldResemble, beforeRestart)

			So(stats.Items, ShouldEqual, dgrChainJobs)
			So(stats.Ready, ShouldEqual, dgrChains)
			So(stats.Dependant, ShouldEqual, dgrChainJobs-dgrChains)
			So(stats.Running, ShouldEqual, 0)
			So(stats.Buried, ShouldEqual, 0)
			So(stats.Suspended, ShouldEqual, 0)

			So(dgrMissingItems(d.server, chainKeys), ShouldEqual, 0)
			So(d.server.depGroups.memberships(), ShouldEqual, beforeMemberships)
			So(d.server.depGroups.memberships(), ShouldEqual, dgrChainJobs)
		})
	})
}

// dgrMissingItems returns how many of the given keys the recovered queue does not
// hold, which is also how many it duplicated: the queue is keyed, so a key
// restored twice displaces another only if the fixture itself repeated it, and the
// fixture's keys are all distinct.
func dgrMissingItems(s *Server, keys []string) int {
	missing := 0

	for _, key := range keys {
		if _, err := s.q.Get(key); err != nil {
			missing++
		}
	}

	return missing
}

// TestDepGranularityRecoveryPassTransactions covers C1 acceptance test 7: the
// recovery dependency pass costs one bolt read transaction per chunk of jobs -
// not one for the whole pass, which would hold bolt's mmaplock for a whole
// recovery, and not one per job, which is the bug it replaced.
func TestDepGranularityRecoveryPassTransactions(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database of recovered chain jobs and a small chunk size", t, func() {
		testDB, server, priorJobs := dgrResolutionFixture(t, ctx, dgrChainDBJobs(dgrPassJobs, 1, dgrNeverSeenGroup))
		So(priorJobs, ShouldHaveLength, dgrPassJobs)

		defer setDependencyResolutionChunkSize(dgrPassChunkSize)()

		Convey("The pass costs exactly one read transaction per chunk", func() {
			before := testDB.bolt.Stats().TxN

			resolved, err := testDB.resolveDependencies(ctx, priorJobs, server.depGroups)
			So(err, ShouldBeNil)
			So(resolved, ShouldHaveLength, dgrPassJobs)

			So(testDB.bolt.Stats().TxN-before, ShouldEqual, dgrPassChunks)
			So(dgrPassChunks, ShouldBeGreaterThan, 1)
			So(dgrPassChunks, ShouldBeLessThan, dgrPassJobs)
		})
	})
}

// TestDepGranularityRecoverySharedSeenCache covers C1 acceptance test 8: the one
// seenDepGroupCache the pass builds means a never-seen dep group named by every
// recovered job is read from bucketDepGroups once, not once per job and not once
// per chunk.
func TestDepGranularityRecoverySharedSeenCache(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database of recovered jobs all waiting on the same never-seen dep group", t, func() {
		testDB, server, priorJobs := dgrResolutionFixture(t, ctx, dgrNeverSeenDBJobs(dgrSeenJobs))
		So(priorJobs, ShouldHaveLength, dgrSeenJobs)

		defer setDependencyResolutionChunkSize(dgrPassChunkSize)()

		Convey("The group is read once for the whole pass, and every job still blocks on it", func() {
			So(dgrSeenChunks, ShouldBeGreaterThan, 1)

			before := testDB.depGroupSeenGets.Load()

			resolved, err := testDB.resolveDependencies(ctx, priorJobs, server.depGroups)
			So(err, ShouldBeNil)
			So(resolved, ShouldHaveLength, dgrSeenJobs)

			// one get for the whole pass: without the shared cache it would be one
			// per job (dgrSeenJobs), and without the cache outliving each chunk's
			// reader it would be one per chunk (dgrSeenChunks).
			So(testDB.depGroupSeenGets.Load()-before, ShouldEqual, 1)

			blocked, waiting := 0, 0
			wantDeps := []string{depGroupDependencyKey(dgrNeverSeenGroup)}
			wantWaiting := []string{dgrNeverSeenGroup}

			for _, rj := range resolved {
				if slices.Equal(rj.deps, wantDeps) {
					blocked++
				}

				if slices.Equal(rj.job.WaitingForDepGroups, wantWaiting) {
					waiting++
				}
			}

			So(blocked, ShouldEqual, dgrSeenJobs)
			So(waiting, ShouldEqual, dgrSeenJobs)
		})
	})
}

// dgrResolutionFixture stores the given jobs in a fresh database's live bucket,
// reads them back the way prior-state recovery does, and registers their dep
// group membership on a server that holds nothing else - which is all
// resolveDependencies needs, and keeps the transaction and get counts free of a
// running server's other reads.
func dgrResolutionFixture(t *testing.T, ctx context.Context, jobs []*Job) (*db, *Server, []*Job) {
	t.Helper()

	testDB := dgTestDB(t, ctx)

	queued, _, _, err := testDB.storeNewJobs(ctx, jobs, false)
	So(err, ShouldBeNil)
	So(queued, ShouldHaveLength, len(jobs))

	priorJobs, err := testDB.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	server := &Server{depGroups: newDepGroupMembers()}
	server.registerDepGroupMembers(priorJobs)

	return testDB, server, priorJobs
}

// dgrNeverSeenDBJobs returns count database-level jobs that all depend on
// dgrNeverSeenGroup and belong to no dep group, so the group has no live member
// and has never been seen.
func dgrNeverSeenDBJobs(count int) []*Job {
	jobs := make([]*Job, 0, count)

	for i := range count {
		job := testDBJob("echo dgr db never seen "+strconv.Itoa(i), dgrWaiterRepGroup)
		job.Dependencies = Dependencies{NewDepGroupDependency(dgrNeverSeenGroup)}
		jobs = append(jobs, job)
	}

	return jobs
}

// dgrChainDBJobs returns count database-level jobs forming chains dep-group
// chains, as dgrServer.chainJobs does for a live server.
func dgrChainDBJobs(count, chains int, headDep string) []*Job {
	jobs := make([]*Job, 0, count)

	for i := range count {
		job := testDBJob("echo dgr db chain "+strconv.Itoa(i), dgrChainRepGroup)
		dgrChainDeps(job, i, chains, headDep)
		jobs = append(jobs, job)
	}

	return jobs
}

func dgrChainGroup(i int) string {
	return "depgranularity-recovery-chain-" + strconv.Itoa(i)
}
