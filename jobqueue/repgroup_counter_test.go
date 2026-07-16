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

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// The maintained per-repGroup COMPLETE counter (spec A1) must equal the RAW
// scan (retrieveCompleteJobCountsByRepGroups) by construction after every
// mutation. These tests exercise the four maintenance hooks and assert that
// invariant. The RAW scan is the ground truth.

// newCounterTestDB opens a fresh db in a temp dir for counter tests.
func newCounterTestDB(t *testing.T, ctx context.Context) *db {
	t.Helper()

	tmpdir := t.TempDir()

	testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
	So(err, ShouldBeNil)

	return testDB
}

// counterMatchesRaw asserts the maintained counter equals the RAW scan for the
// given repGroups and returns the maintained map.
func counterMatchesRaw(testDB *db, repGroups ...string) map[string]int {
	maintained, err := testDB.retrieveMaintainedCompleteCounts(repGroups)
	So(err, ShouldBeNil)

	raw, err := testDB.retrieveCompleteJobCountsByRepGroups(repGroups)
	So(err, ShouldBeNil)

	So(maintained, ShouldResemble, raw)

	return maintained
}

// storeCounterJob stores a job in the live bucket via the add path.
func storeCounterJob(ctx context.Context, testDB *db, job *Job) {
	//nolint:dogsled // storeNewJobs' queue/update/added returns are irrelevant here.
	_, _, _, err := testDB.storeNewJobs(ctx, []*Job{job}, false)
	So(err, ShouldBeNil)
}

func TestRepGroupCounterFreshStore(t *testing.T) {
	ctx := context.Background()

	Convey("Given a fresh db with a stored-but-not-archived job in rgA", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		job := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, job)

		Convey("A1.1: the maintained counter for rgA is 0 == RAW scan", func() {
			counts := counterMatchesRaw(testDB, "rgA")
			So(counts["rgA"], ShouldEqual, 0)
		})
	})
}

func TestRepGroupCounterArchive(t *testing.T) {
	ctx := context.Background()

	Convey("Given a stored job in rgA that is then archived", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		job := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, job)
		So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

		Convey("A1.2: the maintained counter for rgA is 1 == RAW scan", func() {
			counts := counterMatchesRaw(testDB, "rgA")
			So(counts["rgA"], ShouldEqual, 1)
		})
	})
}

func TestRepGroupCounterCrossRepGroupAdd(t *testing.T) {
	ctx := context.Background()

	Convey("Given K archived under rgA, when a same-key job is added under rgB", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		jobA := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, jobA)
		So(testDB.archiveJob(ctx, jobA.Key(), jobA), ShouldBeNil)

		jobB := testDBArchivedJob("echo a", "rgB", time.Now())
		So(jobB.Key(), ShouldEqual, jobA.Key())
		storeCounterJob(ctx, testDB, jobB)

		Convey("A1.3: the counter is {rgA:1, rgB:1} == RAW scan", func() {
			counts := counterMatchesRaw(testDB, "rgA", "rgB")
			So(counts["rgA"], ShouldEqual, 1)
			So(counts["rgB"], ShouldEqual, 1)
		})
	})
}

func TestRepGroupCounterKeyChangingModify(t *testing.T) {
	ctx := context.Background()

	Convey("Given K complete and live under two repgroups, when K is modified to a new key", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		jobA := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, jobA)
		So(testDB.archiveJob(ctx, jobA.Key(), jobA), ShouldBeNil)

		jobB := testDBArchivedJob("echo a", "rgB", time.Now())
		storeCounterJob(ctx, testDB, jobB)

		oldKey := jobA.Key()

		modified := testDBArchivedJob("echo a2", "rgB", time.Now())
		So(modified.Key(), ShouldNotEqual, oldKey)

		So(testDB.modifyLiveJobs(ctx, []string{oldKey}, []*Job{modified}), ShouldBeNil)

		Convey("A1.4: the counter is {rgA:0, rgB:0} == RAW scan", func() {
			counts := counterMatchesRaw(testDB, "rgA", "rgB")
			So(counts["rgA"], ShouldEqual, 0)
			So(counts["rgB"], ShouldEqual, 0)
		})
	})
}

func TestRepGroupCounterIdempotentReArchive(t *testing.T) {
	ctx := context.Background()

	Convey("Given K archived under rgA (counter 1), when archiveJob is called again for K", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		job := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, job)
		So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
		So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

		Convey("A1.5: the counter for rgA stays 1 == RAW scan", func() {
			counts := counterMatchesRaw(testDB, "rgA")
			So(counts["rgA"], ShouldEqual, 1)
		})
	})
}

func TestRepGroupCounterPreExistenceReAdd(t *testing.T) {
	ctx := context.Background()

	Convey("Given K archived under rgA, when K is re-added under rgA (RTK entry pre-exists)", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		job := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, job)
		So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

		reAdd := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, reAdd)

		Convey("A1.6: the counter for rgA stays 1 == RAW scan", func() {
			counts := counterMatchesRaw(testDB, "rgA")
			So(counts["rgA"], ShouldEqual, 1)
		})
	})
}

func TestRepGroupCounterRemoveDoesNotDeleteRTK(t *testing.T) {
	ctx := context.Background()

	Convey("Given K archived under rgA and re-added live, when K is removed via deleteLiveJobs", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		job := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, job)
		So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

		reAdd := testDBArchivedJob("echo a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, reAdd)

		So(testDB.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)

		Convey("A1.7: the counter for rgA stays 1 == RAW scan (remove leaves RTK)", func() {
			counts := counterMatchesRaw(testDB, "rgA")
			So(counts["rgA"], ShouldEqual, 1)
		})
	})
}

func TestRepGroupCounterMixedChurn(t *testing.T) {
	ctx := context.Background()

	Convey("Given a churn across three repgroups mixing add/archive/remove/re-add/modify", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		// rgA: add -> archive (single-repgroup complete key).
		jobA := testDBArchivedJob("churn a", "rgA", time.Now())
		storeCounterJob(ctx, testDB, jobA)
		So(testDB.archiveJob(ctx, jobA.Key(), jobA), ShouldBeNil)

		// rgB: add -> archive.
		jobB := testDBArchivedJob("churn b", "rgB", time.Now())
		storeCounterJob(ctx, testDB, jobB)
		So(testDB.archiveJob(ctx, jobB.Key(), jobB), ShouldBeNil)

		// rgC: add a live-only job (never archived).
		jobC := testDBArchivedJob("churn c", "rgC", time.Now())
		storeCounterJob(ctx, testDB, jobC)

		// cross-repgroup: re-add rgA's complete key under rgB.
		crossB := testDBArchivedJob("churn a", "rgB", time.Now())
		So(crossB.Key(), ShouldEqual, jobA.Key())
		storeCounterJob(ctx, testDB, crossB)

		// remove: delete the live-only rgC job (must not touch RTK).
		So(testDB.deleteLiveJobs(ctx, []string{jobC.Key()}), ShouldBeNil)

		// re-add jobC under rgC again (RTK entry pre-exists).
		reC := testDBArchivedJob("churn c", "rgC", time.Now())
		storeCounterJob(ctx, testDB, reC)

		// add a fresh live job to rgA and modify it to a new key.
		liveA := testDBArchivedJob("churn a live", "rgA", time.Now())
		storeCounterJob(ctx, testDB, liveA)

		modA := testDBArchivedJob("churn a live2", "rgA", time.Now())
		So(modA.Key(), ShouldNotEqual, liveA.Key())
		So(testDB.modifyLiveJobs(ctx, []string{liveA.Key()}, []*Job{modA}), ShouldBeNil)

		// re-add rgB's complete key under rgC (second cross-repgroup key) then bury
		// by archiving under rgC.
		crossC := testDBArchivedJob("churn b", "rgC", time.Now())
		So(crossC.Key(), ShouldEqual, jobB.Key())
		storeCounterJob(ctx, testDB, crossC)
		So(testDB.archiveJob(ctx, crossC.Key(), crossC), ShouldBeNil)

		Convey("A1.8: for every repgroup the maintained counter == RAW scan", func() {
			counts := counterMatchesRaw(testDB, "rgA", "rgB", "rgC")
			So(counts["rgA"], ShouldEqual, 1)
			So(counts["rgB"], ShouldEqual, 2)
			So(counts["rgC"], ShouldEqual, 1)
		})
	})
}

// setRepGroupCompleteCounter overwrites bucketRepGroupComplete[repGroup] with an
// exact value, letting a test drive the maintained counter away from the RAW
// scan to prove which one seeding reads.
func setRepGroupCompleteCounter(testDB *db, repGroup string, value uint64) {
	err := testDB.bolt.Update(func(tx *bolt.Tx) error {
		encoded := make([]byte, repGroupCountBytes)
		binary.BigEndian.PutUint64(encoded, value)

		return tx.Bucket(bucketRepGroupComplete).Put([]byte(repGroup), encoded)
	})
	So(err, ShouldBeNil)
}

// a2RepGroup is the live repGroup used by the A2 seeding tests.
const a2RepGroup = "rgBig"

// repGroup names shared by the A3 backfill tests.
const (
	rgA = "rgA"
	rgB = "rgB"
	rgC = "rgC"
)

// A2 (spec.md section A2): seeding reads the maintained counter, not the RAW
// scan. retrieveMaintainedCompleteCounts is O(live repgroups) point reads, so a
// single live job in a huge-history repgroup no longer blocks seeding.

func TestA2SeedingReadsCounterNotScan(t *testing.T) {
	ctx := context.Background()

	Convey("Given rgBig with a RAW scan of 3 but its counter overwritten to a differing 99", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		for i := range 3 {
			job := testDBArchivedJob("echo big "+strconv.Itoa(i), a2RepGroup, time.Now())
			storeCounterJob(ctx, testDB, job)
			So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
		}

		raw, err := testDB.retrieveCompleteJobCountsByRepGroups([]string{a2RepGroup})
		So(err, ShouldBeNil)
		So(raw[a2RepGroup], ShouldEqual, 3)

		setRepGroupCompleteCounter(testDB, a2RepGroup, 99)

		server := &Server{db: testDB, statusState: newStatusState()}
		So(server.statusState.hasRepGroup(a2RepGroup), ShouldBeFalse)

		Convey("A2.1: seeding rgBig for a newly-added job takes the counter value 99, not the RAW scan 3", func() {
			err = server.seedStatusStateForItemDefs([]*queue.ItemDef{{
				Data: &Job{RepGroup: a2RepGroup},
			}})
			So(err, ShouldBeNil)

			snap := server.statusState.snapshot()
			So(snap[a2RepGroup][JobStateComplete], ShouldEqual, 99)

			// the RAW scan is unchanged: proves the divergent value came from the
			// counter, not a re-scan.
			raw, err = testDB.retrieveCompleteJobCountsByRepGroups([]string{a2RepGroup})
			So(err, ShouldBeNil)
			So(raw[a2RepGroup], ShouldEqual, 3)
		})
	})
}

func TestA2RestartSeedsCounter(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	_, serverConfig, _, _, _ := jobqueueTestInit(true) //nolint:dogsled
	serverConfig.dontWipeDevDB = true

	const n = 7

	Convey("Given a db with a live job in rgBig whose maintained counter is 7", t, func() {
		testDB, _, err := initDB(ctx, serverConfig.DBFile, serverConfig.DBFileBackup,
			internal.Development, false, false)
		So(err, ShouldBeNil)

		// a live (never-completed) job kept off the scheduler by a missing
		// dependency, so rgBig becomes live at restart without the job running
		// and changing the complete count.
		job := testDBJob("echo restart live", a2RepGroup)
		job.Dependencies = Dependencies{NewDepGroupDependency("a2-missing-dep")}
		storeCounterJob(ctx, testDB, job)

		setRepGroupCompleteCounter(testDB, a2RepGroup, n)
		So(testDB.close(ctx), ShouldBeNil)

		Convey("A2.2: after the manager becomes responsive, rgBig is seeded to 7 from the counter", func() {
			server, _, _, errs := serve(ctx, serverConfig)
			So(errs, ShouldBeNil)

			defer server.Stop(ctx, true)

			var snap map[string]map[JobState]int
			for range 100 {
				snap = server.statusState.snapshot()
				if _, ok := snap[a2RepGroup]; ok {
					break
				}

				time.Sleep(20 * time.Millisecond)
			}

			So(snap[a2RepGroup][JobStateComplete], ShouldEqual, n)
		})
	})
}

// A3 (spec.md section A3): one-time online background backfill. For each
// repGroup lacking a marker, in one tx SET counter[rg] = the RAW scan computed
// in that same tx, then write the marker; write the sentinel when all done. SET
// (not additive) reconciles with concurrent runtime increments because bolt
// serialises write transactions. Idempotent, crash-resumable; a new DB (no
// repGroups) is a no-op. Ground truth remains the RAW scan.

// archiveCounterJob stores then archives a job under repGroup, so the RAW scan
// (and the A1-maintained counter) count it complete.
func archiveCounterJob(ctx context.Context, testDB *db, cmd, repGroup string) {
	job := testDBArchivedJob(cmd, repGroup, time.Now())
	storeCounterJob(ctx, testDB, job)
	So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
}

// clearCounterBuckets empties bucketRepGroupComplete and bucketRepGroupBackfilled
// to simulate a pre-upgrade DB: archived history present, but no maintained
// counts or backfill markers yet.
func clearCounterBuckets(testDB *db) {
	err := testDB.bolt.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketRepGroupComplete, bucketRepGroupBackfilled} {
			if derr := tx.DeleteBucket(name); derr != nil {
				return derr
			}

			if _, cerr := tx.CreateBucket(name); cerr != nil {
				return cerr
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
}

// repGroupHasMarker reports whether repGroup has a backfill marker.
func repGroupHasMarker(testDB *db, repGroup string) bool {
	var has bool

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		has = tx.Bucket(bucketRepGroupBackfilled).Get([]byte(repGroup)) != nil

		return nil
	})
	So(err, ShouldBeNil)

	return has
}

// backfillSentinelSet reports whether the fully-backfilled sentinel is set.
func backfillSentinelSet(testDB *db) bool {
	var has bool

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		has = tx.Bucket(bucketRepGroupBackfilled).Get(backfillSentinelKey) != nil

		return nil
	})
	So(err, ShouldBeNil)

	return has
}

// maintainedCount returns the single maintained counter value for repGroup.
func maintainedCount(testDB *db, repGroup string) int {
	counts, err := testDB.retrieveMaintainedCompleteCounts([]string{repGroup})
	So(err, ShouldBeNil)

	return counts[repGroup]
}

func TestA3BackfillPreUpgradeDB(t *testing.T) {
	ctx := context.Background()

	Convey("Given a pre-upgrade DB with archived history but empty counter/marker buckets", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		archiveCounterJob(ctx, testDB, "echo a2", rgA)
		archiveCounterJob(ctx, testDB, "echo b1", rgB)
		archiveCounterJob(ctx, testDB, "echo c1", rgC)

		repGroups := []string{rgA, rgB, rgC}

		raw, err := testDB.retrieveCompleteJobCountsByRepGroups(repGroups)
		So(err, ShouldBeNil)
		So(raw, ShouldResemble, map[string]int{rgA: 2, rgB: 1, rgC: 1})

		clearCounterBuckets(testDB)

		// the counters now disagree with the RAW scan (empty => 0), so a genuine
		// backfill is exercised.
		So(maintainedCount(testDB, rgA), ShouldEqual, 0)
		So(backfillSentinelSet(testDB), ShouldBeFalse)

		Convey("A3.1: backfill sets every counter to the RAW scan with markers and the sentinel", func() {
			So(testDB.backfillRepGroupCompleteCounts(ctx), ShouldBeNil)

			counts := counterMatchesRaw(testDB, repGroups...)
			So(counts, ShouldResemble, map[string]int{rgA: 2, rgB: 1, rgC: 1})

			for _, rg := range repGroups {
				So(repGroupHasMarker(testDB, rg), ShouldBeTrue)
			}

			So(backfillSentinelSet(testDB), ShouldBeTrue)
		})
	})
}

func TestA3BackfillNewDBNoOp(t *testing.T) {
	ctx := context.Background()

	Convey("Given a fresh DB with no repGroups", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		Convey("A3: backfill is a harmless no-op that only sets the sentinel", func() {
			So(testDB.backfillRepGroupCompleteCounts(ctx), ShouldBeNil)

			So(backfillSentinelSet(testDB), ShouldBeTrue)
		})
	})
}

func TestA3BackfillInterruptedRerun(t *testing.T) {
	ctx := context.Background()

	Convey("Given a pre-upgrade DB whose backfill was interrupted after only rgA was done", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		archiveCounterJob(ctx, testDB, "echo a2", rgA)
		archiveCounterJob(ctx, testDB, "echo b1", rgB)
		archiveCounterJob(ctx, testDB, "echo c1", rgC)

		repGroups := []string{rgA, rgB, rgC}

		clearCounterBuckets(testDB)

		// simulate a genuine partial backfill: rgA fully processed (correct
		// counter + marker), rgB and rgC not reached before the "crash".
		So(testDB.backfillRepGroupComplete(rgA), ShouldBeNil)
		So(repGroupHasMarker(testDB, rgA), ShouldBeTrue)
		So(maintainedCount(testDB, rgA), ShouldEqual, 2)
		So(repGroupHasMarker(testDB, rgB), ShouldBeFalse)
		So(maintainedCount(testDB, rgB), ShouldEqual, 0)

		Convey("A3.2: re-running backfill completes the unmarked repGroups to the RAW scan", func() {
			So(testDB.backfillRepGroupCompleteCounts(ctx), ShouldBeNil)

			counts := counterMatchesRaw(testDB, repGroups...)
			So(counts, ShouldResemble, map[string]int{rgA: 2, rgB: 1, rgC: 1})

			for _, rg := range repGroups {
				So(repGroupHasMarker(testDB, rg), ShouldBeTrue)
			}

			So(backfillSentinelSet(testDB), ShouldBeTrue)
		})

		Convey("A3.2: a re-run processes ONLY unmarked repGroups (marked rgA is skipped)", func() {
			// corrupt the already-marked rgA counter; a resumable backfill must NOT
			// recompute a marked repGroup, so this wrong value must survive.
			setRepGroupCompleteCounter(testDB, rgA, 42)

			So(testDB.backfillRepGroupCompleteCounts(ctx), ShouldBeNil)

			So(maintainedCount(testDB, rgA), ShouldEqual, 42)
			So(maintainedCount(testDB, rgB), ShouldEqual, 1)
			So(maintainedCount(testDB, rgC), ShouldEqual, 1)
		})
	})
}

func TestA3BackfillConcurrentArchives(t *testing.T) {
	ctx := context.Background()

	Convey("Given a pre-upgrade DB with existing rgC history and empty counter buckets", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		const preArchived = 50

		for i := range preArchived {
			archiveCounterJob(ctx, testDB, "echo pre "+strconv.Itoa(i), rgC)
		}

		clearCounterBuckets(testDB)

		Convey("A3.3: concurrent archives while backfill runs reconcile counter[rgC] to the RAW scan", func() {
			const concurrentArchives = 50

			// So() must run on the Convey goroutine, so the goroutines only collect
			// errors (each writes its own variable, so there is no shared-write race)
			// and we assert after they join.
			var (
				wg          sync.WaitGroup
				backfillErr error
				archiveOK   int
			)

			wg.Add(2)

			go func() {
				defer wg.Done()

				backfillErr = testDB.backfillRepGroupCompleteCounts(ctx)
			}()

			go func() {
				defer wg.Done()

				for i := range concurrentArchives {
					job := testDBArchivedJob("echo conc "+strconv.Itoa(i), rgC, time.Now())

					if _, _, _, err := testDB.storeNewJobs(ctx, []*Job{job}, false); err != nil {
						continue
					}

					if testDB.archiveJob(ctx, job.Key(), job) == nil {
						archiveOK++
					}
				}
			}()

			wg.Wait()

			So(backfillErr, ShouldBeNil)
			So(archiveOK, ShouldEqual, concurrentArchives)

			counts := counterMatchesRaw(testDB, rgC)
			So(counts[rgC], ShouldEqual, preArchived+concurrentArchives)
		})
	})
}

func TestA3BackfillContextCancelled(t *testing.T) {
	ctx := context.Background()

	Convey("Given a pre-upgrade DB and an already-cancelled context", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		archiveCounterJob(ctx, testDB, "echo a1", rgA)

		clearCounterBuckets(testDB)

		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()

		Convey("A3: backfill respects cancellation and stops before processing repGroups", func() {
			err := testDB.backfillRepGroupCompleteCounts(cancelledCtx)
			So(err, ShouldEqual, context.Canceled)

			So(repGroupHasMarker(testDB, rgA), ShouldBeFalse)
			So(maintainedCount(testDB, rgA), ShouldEqual, 0)
		})
	})
}

func TestA3BackfillSentinelShortCircuits(t *testing.T) {
	ctx := context.Background()

	Convey("Given a DB with the fully-backfilled sentinel set but an UNMARKED, corrupted repGroup", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		archiveCounterJob(ctx, testDB, "echo a2", rgA)
		archiveCounterJob(ctx, testDB, "echo b1", rgB)

		// Set the sentinel WITHOUT writing any per-repGroup markers, then corrupt
		// rgA. This isolates the sentinel short-circuit from the per-repGroup
		// marker skip: rgA has no marker, so if backfill actually ran its pass it
		// WOULD repair rgA (the marker-skip does not fire). Only the sentinel
		// short-circuit can leave the corrupted value in place, so asserting it
		// survives proves the short-circuit fired (not merely a marker skip).
		So(testDB.markBackfillSentinel(), ShouldBeNil)
		So(backfillSentinelSet(testDB), ShouldBeTrue)
		So(repGroupHasMarker(testDB, rgA), ShouldBeFalse)
		setRepGroupCompleteCounter(testDB, rgA, 99)

		Convey("A3: a subsequent backfill short-circuits on the sentinel and does NOT repair the corruption", func() {
			So(testDB.backfillRepGroupCompleteCounts(ctx), ShouldBeNil)

			// the sentinel made the whole pass a no-op: the wrong value survives,
			// and rgA still has no marker (backfill never touched it).
			So(maintainedCount(testDB, rgA), ShouldEqual, 99)
			So(repGroupHasMarker(testDB, rgA), ShouldBeFalse)

			Convey("A4: the offline recompute (which ignores the sentinel) DOES repair it", func() {
				drift, err := testDB.recomputeRepGroupCompleteCounts(ctx)
				So(err, ShouldBeNil)
				So(drift, ShouldEqual, 1) // only rgA differed (99 != 2)
				So(maintainedCount(testDB, rgA), ShouldEqual, 2)
				So(counterMatchesRaw(testDB, rgA, rgB), ShouldResemble, map[string]int{rgA: 2, rgB: 1})
			})
		})
	})
}

// A4 (spec.md section A4): offline recompute/repair. Unlike the online backfill,
// recomputeRepGroupCompleteCounts ignores markers and processes EVERY repGroup,
// SETting counter[rg] = the RAW scan computed in the same tx; it returns the
// drift (repGroups whose stored value differed) and is idempotent. The exported
// RecomputeRepGroupCompleteCounts opens the DB file directly with the map
// freelist and is what the offline `wr manager recompute-counts` subcommand
// calls. Ground truth remains the RAW scan.

// newCounterTestDBFile opens a fresh db in a temp dir and returns it with its
// file path, so a test can close it and hand the path to the exported
// RecomputeRepGroupCompleteCounts (which opens the file itself).
func newCounterTestDBFile(t *testing.T, ctx context.Context) (*db, string) {
	t.Helper()

	dbFile := filepath.Join(t.TempDir(), "queue.db")

	testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
	So(err, ShouldBeNil)

	return testDB, dbFile
}

// reopenCounterDBFile reopens the db at dbFile, so a test can assert the state
// the exported RecomputeRepGroupCompleteCounts persisted before it closed.
func reopenCounterDBFile(t *testing.T, ctx context.Context, dbFile string) *db {
	t.Helper()

	testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
	So(err, ShouldBeNil)

	return testDB
}

func TestA4RecomputeCorrectCountersNoOp(t *testing.T) {
	ctx := context.Background()

	Convey("Given a closed db whose maintained counters are already correct", t, func() {
		testDB, dbFile := newCounterTestDBFile(t, ctx)

		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		archiveCounterJob(ctx, testDB, "echo a2", rgA)
		archiveCounterJob(ctx, testDB, "echo b1", rgB)

		// correct by construction (the A1 hooks kept them equal to the RAW scan).
		So(counterMatchesRaw(testDB, rgA, rgB), ShouldResemble, map[string]int{rgA: 2, rgB: 1})
		So(testDB.close(ctx), ShouldBeNil)

		Convey("A4.1: the exported recompute reports drift 0 and leaves counters unchanged", func() {
			drift, err := RecomputeRepGroupCompleteCounts(ctx, dbFile)
			So(err, ShouldBeNil)
			So(drift, ShouldEqual, 0)

			reopened := reopenCounterDBFile(t, ctx, dbFile)
			defer func() { So(reopened.close(ctx), ShouldBeNil) }()

			So(counterMatchesRaw(reopened, rgA, rgB), ShouldResemble, map[string]int{rgA: 2, rgB: 1})
		})
	})
}

func TestA4RecomputeRepairsCorruptedCounters(t *testing.T) {
	ctx := context.Background()

	Convey("Given a closed db whose counters were deliberately corrupted", t, func() {
		testDB, dbFile := newCounterTestDBFile(t, ctx)

		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		archiveCounterJob(ctx, testDB, "echo a2", rgA)
		archiveCounterJob(ctx, testDB, "echo b1", rgB)
		archiveCounterJob(ctx, testDB, "echo c1", rgC)

		// corrupt two of the three repGroups (rgB is left correct), so the drift
		// must equal the number of corrupted repGroups.
		setRepGroupCompleteCounter(testDB, rgA, 99)
		setRepGroupCompleteCounter(testDB, rgC, 0)
		So(testDB.close(ctx), ShouldBeNil)

		Convey("A4.2: recompute repairs every counter to the RAW scan and drift == corrupted count", func() {
			drift, err := RecomputeRepGroupCompleteCounts(ctx, dbFile)
			So(err, ShouldBeNil)
			So(drift, ShouldEqual, 2)

			reopened := reopenCounterDBFile(t, ctx, dbFile)
			defer func() { So(reopened.close(ctx), ShouldBeNil) }()

			So(counterMatchesRaw(reopened, rgA, rgB, rgC),
				ShouldResemble, map[string]int{rgA: 2, rgB: 1, rgC: 1})
		})
	})
}

func TestA4RecomputeNonexistentDBFile(t *testing.T) {
	ctx := context.Background()

	Convey("Given a path to a DB file that does not exist", t, func() {
		dbFile := filepath.Join(t.TempDir(), "nonexistent.db")

		Convey("A4: the exported recompute errors cleanly and does not create the file", func() {
			drift, err := RecomputeRepGroupCompleteCounts(ctx, dbFile)
			So(err, ShouldNotBeNil)
			So(drift, ShouldEqual, 0)

			// it must NOT have created an empty DB at the path.
			_, statErr := os.Stat(dbFile)
			So(os.IsNotExist(statErr), ShouldBeTrue)
		})
	})
}

func TestA4RecomputeIgnoresMarkersAndIsIdempotent(t *testing.T) {
	ctx := context.Background()

	Convey("Given a db where an already-backfilled repGroup's counter is corrupted", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		archiveCounterJob(ctx, testDB, "echo b1", rgB)

		// mark rgA backfilled then corrupt it: unlike backfill, recompute must
		// still repair a marked repGroup (a full repair ignores markers).
		So(testDB.backfillRepGroupComplete(rgA), ShouldBeNil)
		So(repGroupHasMarker(testDB, rgA), ShouldBeTrue)
		setRepGroupCompleteCounter(testDB, rgA, 42)

		Convey("A4: recompute repairs the marked repGroup, sets markers+sentinel, and is idempotent", func() {
			drift, err := testDB.recomputeRepGroupCompleteCounts(ctx)
			So(err, ShouldBeNil)
			So(drift, ShouldEqual, 1) // only rgA differed (42 != 1)

			So(counterMatchesRaw(testDB, rgA, rgB), ShouldResemble, map[string]int{rgA: 1, rgB: 1})
			So(repGroupHasMarker(testDB, rgA), ShouldBeTrue)
			So(repGroupHasMarker(testDB, rgB), ShouldBeTrue)
			So(backfillSentinelSet(testDB), ShouldBeTrue)

			// a second run over now-correct counters is a no-op (drift 0).
			drift, err = testDB.recomputeRepGroupCompleteCounts(ctx)
			So(err, ShouldBeNil)
			So(drift, ShouldEqual, 0)
			So(counterMatchesRaw(testDB, rgA, rgB), ShouldResemble, map[string]int{rgA: 1, rgB: 1})
		})
	})
}

func TestA4RecomputeContextCancelled(t *testing.T) {
	ctx := context.Background()

	Convey("Given a db with a corrupted counter and an already-cancelled context", t, func() {
		testDB := newCounterTestDB(t, ctx)
		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		setRepGroupCompleteCounter(testDB, rgA, 42)

		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()

		Convey("A4: recompute respects cancellation and does not modify the corrupted counter", func() {
			drift, err := testDB.recomputeRepGroupCompleteCounts(cancelledCtx)
			So(err, ShouldEqual, context.Canceled)
			So(drift, ShouldEqual, 0)
			So(maintainedCount(testDB, rgA), ShouldEqual, 42)
		})
	})
}

// A5 (spec.md section A5): crash consistency. The COMPLETE counter is written in
// the SAME bolt tx as the archive (archiveJob), never as a separate flush, so a
// kill -9 that leaves no clean counter shutdown still recovers a counter that
// equals the RAW scan with zero drift: either both the archived job and its
// counter increment committed together, or neither did. These tests realise the
// crash as an in-package close/reopen from the on-disk file (initDB opens a
// fresh bolt handle from the same path), so the assertions read persisted state,
// not an in-memory value that survived only because the process did not really
// die. If the counter were maintained outside the archive tx, a reopen would
// read a stale counter that no longer matched the RAW scan and these tests would
// fail.
//
// The suite has no separate in-process kill -9 harness (serve runs the server
// in-process). The true out-of-process hard-crash exemplar - a --servermode
// server SIGKILLed and restarted with --keepdb - lives in TestJobqueueSignal
// (jobqueue_test.go), NOT here.

func TestA5CrashConsistencyReopen(t *testing.T) {
	ctx := context.Background()

	Convey("Given a churn of archives across three repgroups persisted to a db file", t, func() {
		testDB, dbFile := newCounterTestDBFile(t, ctx)

		// churn archives; the counter is only ever written inside the shared
		// archive+counter bolt tx (no explicit counter flush anywhere).
		archiveCounterJob(ctx, testDB, "echo a1", rgA)
		archiveCounterJob(ctx, testDB, "echo a2", rgA)
		archiveCounterJob(ctx, testDB, "echo a3", rgA)
		archiveCounterJob(ctx, testDB, "echo b1", rgB)
		archiveCounterJob(ctx, testDB, "echo b2", rgB)
		archiveCounterJob(ctx, testDB, "echo c1", rgC)

		expected := map[string]int{rgA: 3, rgB: 2, rgC: 1}

		// correct by construction before the "crash".
		So(counterMatchesRaw(testDB, rgA, rgB, rgC), ShouldResemble, expected)

		// simulate a kill -9 with no clean counter shutdown: just close and
		// reopen a fresh handle from the same file.
		So(testDB.close(ctx), ShouldBeNil)

		Convey("A5.1: after reopening from disk, every counter == RAW scan and recompute drift == 0", func() {
			reopened := reopenCounterDBFile(t, ctx, dbFile)
			defer func() { So(reopened.close(ctx), ShouldBeNil) }()

			// every counter recovered by the shared archive tx equals the RAW scan.
			So(counterMatchesRaw(reopened, rgA, rgB, rgC), ShouldResemble, expected)

			// and a full recompute over the recovered db finds no drift: the
			// persisted counters already match the RAW scan, so no separate
			// counter-flush was needed to survive the crash.
			drift, err := reopened.recomputeRepGroupCompleteCounts(ctx)
			So(err, ShouldBeNil)
			So(drift, ShouldEqual, 0)
		})
	})
}

func TestA5CrashConsistencyMidCompletion(t *testing.T) {
	ctx := context.Background()

	const (
		rgMid = "rgMid"
		batch = 5
	)

	Convey("Given a run hard-stopped mid-completion (some jobs archived, some not) on a db file", t, func() {
		testDB, dbFile := newCounterTestDBFile(t, ctx)

		// the first half of the batch completes (archive commits the counter in
		// the same tx); keep a reference to one committed job to model a runner
		// that reconnects and retries its archive after the crash.
		committed := make([]*Job, 0, batch/2)
		for i := range batch / 2 {
			job := testDBArchivedJob("echo mid pre "+strconv.Itoa(i), rgMid, time.Now())
			storeCounterJob(ctx, testDB, job)
			So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
			committed = append(committed, job)
		}

		So(maintainedCount(testDB, rgMid), ShouldEqual, batch/2)

		// hard stop mid-completion: close before the rest of the batch archives.
		So(testDB.close(ctx), ShouldBeNil)

		Convey("A5.2: after restart no completion is double-counted or lost (counter == RAW scan)", func() {
			restarted := reopenCounterDBFile(t, ctx, dbFile)
			defer func() { So(restarted.close(ctx), ShouldBeNil) }()

			// the pre-crash completions survived exactly (nothing lost).
			So(maintainedCount(restarted, rgMid), ShouldEqual, batch/2)

			// a reconnecting runner retries an already-committed archive: it must
			// be idempotent and not double-count.
			retried := committed[0]
			So(restarted.archiveJob(ctx, retried.Key(), retried), ShouldBeNil)

			// completion continues for the jobs that had not archived pre-crash.
			for i := batch / 2; i < batch; i++ {
				job := testDBArchivedJob("echo mid post "+strconv.Itoa(i), rgMid, time.Now())
				storeCounterJob(ctx, restarted, job)
				So(restarted.archiveJob(ctx, job.Key(), job), ShouldBeNil)
			}

			// exactly `batch` distinct completions: none double-counted, none lost.
			counts := counterMatchesRaw(restarted, rgMid)
			So(counts[rgMid], ShouldEqual, batch)

			drift, err := restarted.recomputeRepGroupCompleteCounts(ctx)
			So(err, ShouldBeNil)
			So(drift, ShouldEqual, 0)
		})
	})
}
