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

package jobqueue

// This file holds the dep-granularity measurements that are too big for
// make test, behind the reliability_repro build tag. Two full serve recoveries
// over 10k and 50k live-job databases would materially lengthen the suite, which
// spec F4 item 4 gates at the branch-point baseline.
//
// Spec F3 (phase 6) also lands TestDepGranularityFixture here.

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	// dgscSmall and dgscLarge are the two live-job counts E9 acceptance test 2
	// compares. The 150k production point is measured by hand through this same
	// entry point, by raising these.
	dgscSmall = 10000
	dgscLarge = 50000

	// dgscLinearTolerance is how far from a linear relationship in live-job count
	// the decode and build costs may sit. The assertion is deliberately only "not
	// superlinear by more than 2x" rather than a wall-clock budget: this host runs
	// at a load average well above its core count, so an absolute figure would be
	// a flake while a ratio between two runs on the same host is not.
	dgscLinearTolerance = 2.0

	// dgscRecoveryWait bounds a whole 50k-job recovery. It is a hang detector,
	// not a latency budget.
	dgscRecoveryWait = 30 * time.Minute

	// dgscGroupSize is how many live jobs share a dep group, so the fixture has
	// the shape production has: many groups, each with many live members, and a
	// waiter on the previous group.
	dgscGroupSize = 100

	// dgscRecoveredPoll is how often the recovery wait re-checks the flag.
	dgscRecoveredPoll = 50 * time.Millisecond

	dgscRepGroup = "depgranularity-scale"
)

const (
	// the default shape of the F3 gate's fixture: one dep group with
	// dgfDefaultMembers live members, dgfDefaultWaiters live jobs waiting on it,
	// and dgfDefaultGroups distinct dep groups in total. It is production's shape
	// scaled down (150,472 live jobs, >=250,000 dep-group memberships over 6,299
	// groups) so the pre-fix run fails without exhausting the dev host: the
	// pre-fix code retains one dependency key per (waiter, live member) pair, so
	// this shape retains 90,000,000 of them.
	dgfDefaultWaiters = 30000
	dgfDefaultMembers = 3000
	dgfDefaultGroups  = 6300

	// dgfBigGroup is the dep group the waiters wait on and the members belong to.
	dgfBigGroup = "depgran-big"

	// dgfBlockerGroup is a dep group no job is ever added WITH, so it has never
	// been seen and every dependency on it blocks for ever. Every fixture job
	// carries one, which is what stops the manager under test scheduling any of
	// them: the gate measures a recovery and an add on a shared host, and must
	// never run a job or launch a runner to do it.
	dgfBlockerGroup = "depgran-blocker"

	dgfOtherGroupPrefix = "depgran-other-"
	dgfMemberRepGroup   = "depgran-members"
	dgfOtherRepGroup    = "depgran-others"
	dgfWaiterRepGroup   = "depgran-waiters"

	// dgfStoreBatch is how many jobs are stored per storeNewJobs call, so a
	// 30,000-waiter fixture does not encode the whole batch before writing any of
	// it.
	dgfStoreBatch = 5000
)

// dgscPhaseTimings holds all five phases E9 asks to be measured. The assertion
// is on decode and build, which are the two this work changes the cost of; the
// rest are recorded because the ceiling an operator cares about is their sum.
type dgscPhaseTimings struct {
	initDB  time.Duration
	decode  time.Duration
	build   time.Duration
	resolve time.Duration
	enqueue time.Duration
}

// dgscMeasureStartup builds a database holding count live jobs of one shape,
// starts a server on it, and returns the decode and dependency-group build
// durations the startup phase log lines report.
func dgscMeasureStartup(t *testing.T, count int) dgscPhaseTimings {
	t.Helper()

	ctx, logs := cmdLogSyncCapture(context.Background())
	_, serverConfig, _, _, _ := jobqueueTestInit(true)
	serverConfig.dontWipeDevDB = true

	dgscSeedLiveJobs(ctx, t, serverConfig, count)

	server, _, _, err := serveWithoutPublication(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	// publication is the tail of recovery, so this is also what bounds a whole
	// 50k-job recovery; waitUntilRecovered's own 10s bound is for the make test
	// scale, not this one.
	So(dgscWaitServing(server), ShouldBeTrue)
	So(dgscWaitRecovered(server), ShouldBeTrue)

	logged := logs.String()

	return dgscPhaseTimings{
		initDB:  dgscPhaseElapsed(logged, "recovering: opened database"),
		decode:  dgscPhaseElapsed(logged, "recovering: decoded live jobs"),
		build:   dgscPhaseElapsed(logged, "recovering: built dependency-group state"),
		resolve: dgscPhaseElapsed(logged, "recovering: resolved prior job dependencies"),
		enqueue: dgscPhaseElapsed(logged, "recovering: enqueued prior jobs"),
	}
}

// total is the whole startup window: what the manager is unreachable for.
func (t dgscPhaseTimings) total() time.Duration {
	return t.initDB + t.decode + t.build + t.resolve + t.enqueue
}

// String renders the phases for the recorded measurements.
func (t dgscPhaseTimings) String() string {
	return "initDB " + t.initDB.String() +
		", decode " + t.decode.String() +
		", build " + t.build.String() +
		", resolve " + t.resolve.String() +
		", enqueue " + t.enqueue.String() +
		", total " + t.total().String()
}

// TestDepGranularityStartupScaling covers E9 acceptance test 2: the startup
// window this work introduces is bounded by LIVE jobs, and an operator sizing it
// needs to know it grows with that count linearly rather than quadratically -
// which is the whole point of retiring the per-member dependency expansion.
//
// It measures the decode and the dependency-group state build at two live-job
// counts and records both, asserting only that neither is superlinear by more
// than dgscLinearTolerance.
func TestDepGranularityStartupScaling(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The decode and dependency-group build scale linearly in live-job count", t, func() {
		small := dgscMeasureStartup(t, dgscSmall)
		large := dgscMeasureStartup(t, dgscLarge)

		ratio := float64(dgscLarge) / float64(dgscSmall)

		t.Logf("live jobs %d: %s", dgscSmall, small)
		t.Logf("live jobs %d: %s", dgscLarge, large)

		So(small.decode, ShouldBeGreaterThan, 0)
		So(small.build, ShouldBeGreaterThan, 0)
		So(dgscScaling(small.decode, large.decode, ratio), ShouldBeLessThanOrEqualTo, dgscLinearTolerance)
		So(dgscScaling(small.build, large.build, ratio), ShouldBeLessThanOrEqualTo, dgscLinearTolerance)
	})
}

// dgscScaling is how much worse than linear the growth from small to large was,
// for a jobs ratio of jobsRatio. 1.0 is exactly linear; below 1.0 is sublinear.
func dgscScaling(small, large time.Duration, jobsRatio float64) float64 {
	if small <= 0 {
		return 0
	}

	return (float64(large) / float64(small)) / jobsRatio
}

// dgfShape is the fixture's dimensions and the names it uses, which the gate
// passes in so that the shell and this generator cannot disagree about them.
type dgfShape struct {
	waiters  int
	members  int
	groups   int
	bigGroup string
	blocker  string
}

func dgfShapeFromEnv() dgfShape {
	return dgfShape{
		waiters:  dgfEnvInt("WR_DEPGRAN_WAITERS", dgfDefaultWaiters),
		members:  dgfEnvInt("WR_DEPGRAN_MEMBERS", dgfDefaultMembers),
		groups:   dgfEnvInt("WR_DEPGRAN_GROUPS", dgfDefaultGroups),
		bigGroup: dgfEnvString("WR_DEPGRAN_GROUP", dgfBigGroup),
		blocker:  dgfEnvString("WR_DEPGRAN_BLOCKER", dgfBlockerGroup),
	}
}

// liveJobs is how many live jobs the shape holds: the big group's members, one
// member for each of the other groups, and the waiters.
func (s dgfShape) liveJobs() int {
	return s.members + s.groups - 1 + s.waiters
}

// TestDepGranularityFixture is the DB GENERATOR for the F3 scale gate
// (developers/wrdev.sh dep-granularity-check), and asserts the produced database
// has the shape that gate depends on (F3 acceptance test 4).
//
// It builds the fixture through db.storeNewJobs rather than by writing
// bucketJobsLive directly, so bucketRDTK and bucketDepGroups hold what the real
// add path puts there. A fixture that populated only the live bucket would make
// the pre-fix binary resolve no member keys at all, allocate nothing
// quadratically, and hand the gate a false PASS.
//
// bucketDTK is the exception and is written here directly, as the pre-upgrade
// data it is. After spec A3 storeNewJobs writes no bucketDTK entry, but the
// pre-fix binary resolves a dep-group dependency by cursoring exactly that
// bucket, so a fixture without those entries would make every group look
// satisfied to it - the same false PASS. Every live job in production was added
// by a pre-fix binary, so this is the database shape the upgrade meets anyway.
func TestDepGranularityFixture(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbFile := os.Getenv("WR_DEPGRAN_DB")
	if dbFile == "" {
		t.Skip("set WR_DEPGRAN_DB to the database file to create (see wrdev.sh dep-granularity-check)")
	}

	shape := dgfShapeFromEnv()
	ctx := context.Background()

	Convey("The generated database has the shape the gate depends on", t, func() {
		started := time.Now()
		counts := dgfBuild(ctx, t, dbFile, shape)
		size := dgfFileSize(dbFile)

		// F3 acceptance test 4: the three buckets a dep-group dependency is
		// resolved through, in either tree, are all populated, and the retired one
		// holds the big group's whole membership.
		So(counts.dtkForBigGroup, ShouldEqual, shape.members)
		So(counts.dtk, ShouldEqual, shape.members+shape.groups-1)
		So(counts.rdtk, ShouldBeGreaterThan, 0)
		So(counts.depGroups, ShouldBeGreaterThan, 0)
		So(counts.live, ShouldEqual, shape.liveJobs())

		fmt.Printf("DEPGRAN-FIXTURE waiters=%d members=%d groups=%d liveJobs=%d dtk=%d "+
			"dtkBigGroup=%d rdtk=%d depGroups=%d bytes=%d seconds=%.1f db=%s\n",
			shape.waiters, shape.members, shape.groups, counts.live, counts.dtk,
			counts.dtkForBigGroup, counts.rdtk, counts.depGroups, size,
			time.Since(started).Seconds(), dbFile)
	})
}

// dgfBuild creates the fixture database and returns what it holds.
//
// The big group's members go in before the waiters, and the waiters last of all:
// prepareNewJobs scans the previously stored waiters of every dep group a new
// job belongs to, so waiters added last (belonging to no group of their own)
// trigger no scan, whereas members added last would each decode every waiter.
func dgfBuild(ctx context.Context, t *testing.T, dbFile string, shape dgfShape) dgfCounts {
	t.Helper()

	testDB, _, err := initDB(ctx, dbFile, dbFile+"_bk", internal.Development, false, false)
	So(err, ShouldBeNil)

	memberKeys := dgfStore(ctx, testDB, dgfMemberJobs(shape))
	otherKeys := dgfStore(ctx, testDB, dgfOtherGroupJobs(shape))
	waiterKeys := dgfStore(ctx, testDB, dgfWaiterJobs(shape))

	So(memberKeys, ShouldHaveLength, shape.members)
	So(otherKeys, ShouldHaveLength, shape.groups-1)
	So(waiterKeys, ShouldHaveLength, shape.waiters)

	dgfWriteLegacyLookups(testDB, shape, memberKeys, otherKeys)

	counts := dgfCount(testDB, shape)

	So(testDB.close(ctx), ShouldBeNil)

	return counts
}

// dgfStore stores the jobs in batches, returning their keys in order.
func dgfStore(ctx context.Context, testDB *db, jobs []*Job) []string {
	keys := make([]string, 0, len(jobs))
	stored := 0

	for batch := range slices.Chunk(jobs, dgfStoreBatch) {
		queued, _, _, err := testDB.storeNewJobs(ctx, batch, false)
		So(err, ShouldBeNil)

		stored += len(queued)

		for _, job := range batch {
			keys = append(keys, job.Key())
		}
	}

	So(stored, ShouldEqual, len(jobs))

	return keys
}

// dgfCount reads back what the generated database holds.
func dgfCount(testDB *db, shape dgfShape) dgfCounts {
	var counts dgfCounts

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		counts.live = tx.Bucket(bucketJobsLive).Stats().KeyN
		counts.dtk = tx.Bucket(bucketDTK).Stats().KeyN
		counts.rdtk = tx.Bucket(bucketRDTK).Stats().KeyN
		counts.depGroups = tx.Bucket(bucketDepGroups).Stats().KeyN
		counts.dtkForBigGroup = len(dgLookupKeys(tx, bucketDTK, shape.bigGroup))

		return nil
	})
	So(err, ShouldBeNil)

	return counts
}

func dgfFileSize(dbFile string) int64 {
	info, err := os.Stat(dbFile)
	So(err, ShouldBeNil)

	return info.Size()
}

// dgfMemberJobs returns the big group's live member jobs.
func dgfMemberJobs(shape dgfShape) []*Job {
	jobs := make([]*Job, 0, shape.members)

	for i := range shape.members {
		job := dgfJob("echo depgran member "+strconv.Itoa(i), dgfMemberRepGroup, shape)
		job.DepGroups = []string{shape.bigGroup}
		jobs = append(jobs, job)
	}

	return jobs
}

// dgfOtherGroupJobs returns one live member for each dep group other than the
// big one, so the database holds shape.groups distinct groups the way
// production's held 6,299.
func dgfOtherGroupJobs(shape dgfShape) []*Job {
	jobs := make([]*Job, 0, shape.groups-1)

	for i := range shape.groups - 1 {
		job := dgfJob("echo depgran other "+strconv.Itoa(i), dgfOtherRepGroup, shape)
		job.DepGroups = []string{dgfOtherGroupPrefix + strconv.Itoa(i)}
		jobs = append(jobs, job)
	}

	return jobs
}

// dgfWaiterJobs returns the live jobs waiting on the big group. They belong to
// no dep group of their own, so the whole fixture's membership is the members.
func dgfWaiterJobs(shape dgfShape) []*Job {
	jobs := make([]*Job, 0, shape.waiters)

	for i := range shape.waiters {
		job := dgfJob("echo depgran waiter "+strconv.Itoa(i), dgfWaiterRepGroup, shape)
		job.Dependencies = append(job.Dependencies, NewDepGroupDependency(shape.bigGroup))
		jobs = append(jobs, job)
	}

	return jobs
}

// dgfJob returns a fixture job that can never be scheduled, because it waits on
// a dep group no job is ever added with.
func dgfJob(cmd, repGroup string, shape dgfShape) *Job {
	job := testDBJob(cmd, repGroup)
	job.Dependencies = Dependencies{NewDepGroupDependency(shape.blocker)}

	return job
}

// dgfWriteLegacyLookups writes the bucketDTK entry the pre-change code wrote for
// every (dep group, live member) pair: one depGroup + dbDelimiter + jobKey key,
// the shape db.generateLookupKey produces. See this test's own comment for why
// the fixture writes them itself.
func dgfWriteLegacyLookups(testDB *db, shape dgfShape, memberKeys, otherKeys []string) {
	err := testDB.bolt.Update(func(tx *bolt.Tx) error {
		b, errb := tx.CreateBucketIfNotExists(bucketDTK)
		if errb != nil {
			return errb
		}

		for _, jobKey := range memberKeys {
			if errp := b.Put(testDB.generateLookupKey(shape.bigGroup, []byte(jobKey)), nil); errp != nil {
				return errp
			}
		}

		for i, jobKey := range otherKeys {
			group := dgfOtherGroupPrefix + strconv.Itoa(i)

			if errp := b.Put(testDB.generateLookupKey(group, []byte(jobKey)), nil); errp != nil {
				return errp
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
}

// dgfCounts is what the generated database ended up holding.
type dgfCounts struct {
	live           int
	dtk            int
	dtkForBigGroup int
	rdtk           int
	depGroups      int
}

// dgscWaitServing waits up to dgscRecoveryWait for the server to publish itself.
func dgscWaitServing(server *Server) bool {
	select {
	case <-server.Serving():
		return true
	case <-time.After(dgscRecoveryWait):
		return false
	}
}

// dgscWaitRecovered waits up to dgscRecoveryWait for the recovering flag to
// clear, which publication precedes by a sub-millisecond.
func dgscWaitRecovered(server *Server) bool {
	deadline := time.Now().Add(dgscRecoveryWait)

	for time.Now().Before(deadline) {
		if !server.isRecovering() {
			return true
		}

		<-time.After(dgscRecoveredPoll)
	}

	return !server.isRecovering()
}

// dgscSeedLiveJobs writes count live jobs, in one dep group per hundred with the
// hundred's first job waiting on the previous hundred's group, so the decode and
// the membership build both have real work of the shape production has.
func dgscSeedLiveJobs(ctx context.Context, t *testing.T, config ServerConfig, count int) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	jobs := make([]*Job, count)

	for i := range count {
		job := testDBJob("echo dgsc "+strconv.Itoa(i), dgscRepGroup)
		group := dgscRepGroup + "-" + strconv.Itoa(i/dgscGroupSize)
		job.DepGroups = []string{group}

		if i > 0 && i%dgscGroupSize == 0 {
			prior := dgscRepGroup + "-" + strconv.Itoa(i/dgscGroupSize-1)
			job.Dependencies = Dependencies{NewDepGroupDependency(prior)}
		}

		jobs[i] = job
	}

	jobsToQueue, _, _, err := testDB.storeNewJobs(ctx, jobs, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, count)
	So(testDB.close(ctx), ShouldBeNil)
}

// dgscPhaseElapsed pulls the elapsed duration out of the named phase's log line,
// returning -1 when the line is absent so a comparison fails rather than
// silently reading zero.
func dgscPhaseElapsed(logged, phase string) time.Duration {
	for line := range strings.SplitSeq(logged, "\n") {
		if !strings.Contains(line, phase) {
			continue
		}

		_, after, found := strings.Cut(line, "elapsed=")
		if !found {
			continue
		}

		field, _, _ := strings.Cut(after, " ")

		elapsed, err := time.ParseDuration(strings.Trim(field, `"`))
		if err != nil {
			return -1
		}

		return elapsed
	}

	return -1
}

func dgfEnvInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}

	return value
}

func dgfEnvString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}
