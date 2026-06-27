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
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

func TestDBBatchTuning(t *testing.T) {
	Convey("A freshly opened db uses bbolt's default batch tuning", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()

		testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		So(testDB.bolt.MaxBatchDelay, ShouldEqual, 10*time.Millisecond)
		So(testDB.bolt.MaxBatchSize, ShouldEqual, 1000)

		Convey("setBatchTuning applies positive values to the live database", func() {
			testDB.setBatchTuning(40*time.Millisecond, 5000)

			So(testDB.bolt.MaxBatchDelay, ShouldEqual, 40*time.Millisecond)
			So(testDB.bolt.MaxBatchSize, ShouldEqual, 5000)
		})

		Convey("setBatchTuning leaves the database untouched for non-positive values", func() {
			testDB.setBatchTuning(0, 0)

			So(testDB.bolt.MaxBatchDelay, ShouldEqual, 10*time.Millisecond)
			So(testDB.bolt.MaxBatchSize, ShouldEqual, 1000)
		})
	})
}

func TestDBReverseLookupIndex(t *testing.T) {
	Convey("Opening an old DB rebuilds reverse lookup entries used by modify", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		parent := testDBJob("echo parent", "old-parent")
		parent.DepGroups = []string{"old-parent-dg"}

		child := testDBJob("echo child", "old-child")
		child.Dependencies = Dependencies{NewDepGroupDependency("old-parent-dg")}

		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{parent, child}, false)
		So(err, ShouldBeNil)

		parentOldKey := parent.Key()
		childOldKey := child.Key()

		var (
			parentLookups int
			childLookups  int
		)

		err = testDB.bolt.View(func(tx *bolt.Tx) error {
			parentLookups = countLookupEntriesByJobKey(tx, parentOldKey)
			childLookups = countLookupEntriesByJobKey(tx, childOldKey)

			return nil
		})
		So(err, ShouldBeNil)
		So(parentLookups, ShouldEqual, 2)
		So(childLookups, ShouldEqual, 2)

		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			return tx.DeleteBucket(bucketJobLookupEntries)
		})
		So(err, ShouldBeNil)
		So(testDB.close(ctx), ShouldBeNil)

		testDB, _, err = initDB(ctx, dbFile, dbBackup, internal.Development, false, false)

		So(err, ShouldBeNil)
		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		err = testDB.bolt.View(func(tx *bolt.Tx) error {
			So(tx.Bucket(bucketJobLookupEntries), ShouldNotBeNil)
			So(countReverseLookupEntriesByJobKey(tx, parentOldKey), ShouldEqual, parentLookups)
			So(countReverseLookupEntriesByJobKey(tx, childOldKey), ShouldEqual, childLookups)

			return nil
		})
		So(err, ShouldBeNil)

		modifiedParent := testDBJob("echo parent modified", "new-parent")
		modifiedParent.DepGroups = []string{"new-parent-dg"}
		newParentKey := modifiedParent.Key()

		err = testDB.modifyLiveJobs(ctx, []string{parentOldKey}, []*Job{modifiedParent})
		So(err, ShouldBeNil)

		oldDepKeys, err := testDB.retrieveIncompleteJobKeysByDepGroup("old-parent-dg")
		So(err, ShouldBeNil)
		So(oldDepKeys, ShouldHaveLength, 0)

		newDepKeys, err := testDB.retrieveIncompleteJobKeysByDepGroup("new-parent-dg")
		So(err, ShouldBeNil)
		So(newDepKeys, ShouldContain, newParentKey)

		err = testDB.bolt.View(func(tx *bolt.Tx) error {
			So(countLookupEntriesByJobKey(tx, parentOldKey), ShouldEqual, 0)
			So(countReverseLookupEntriesByJobKey(tx, parentOldKey), ShouldEqual, 0)
			So(countLookupEntriesByJobKey(tx, newParentKey), ShouldEqual, 2)
			So(countReverseLookupEntriesByJobKey(tx, newParentKey), ShouldEqual, 2)

			return nil
		})
		So(err, ShouldBeNil)
	})
}

func TestDBDepGroups(t *testing.T) {
	Convey("Preparing new jobs records each seen dep group once per batch", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()

		testDB, _, err := initDB(
			ctx,
			filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"),
			internal.Development,
			false,
			false,
		)
		So(err, ShouldBeNil)

		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		const (
			sharedDepGroup = "shared"
			otherDepGroup  = "other"
		)

		first := testDBJob("echo first", "first")
		first.DepGroups = []string{sharedDepGroup, sharedDepGroup}
		second := testDBJob("echo second", "second")
		second.DepGroups = []string{sharedDepGroup, otherDepGroup}

		_, _, _, depGroupsSeen, _, _, _, _, _, err := testDB.prepareNewJobs([]*Job{first, second}, false)
		So(err, ShouldBeNil)
		So(depGroupsSeen, ShouldHaveLength, 2)

		seen := make(map[string]bool, len(depGroupsSeen))
		for _, lookup := range depGroupsSeen {
			seen[string(lookup[0])] = true
		}

		So(seen[sharedDepGroup], ShouldBeTrue)
		So(seen[otherDepGroup], ShouldBeTrue)
	})

	Convey("Opening an old DB rebuilds seen dep groups from historical dep group lookups", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		legacy := testDBJob("echo legacy", "legacy")
		legacy.DepGroups = []string{"legacy"}

		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{legacy}, false)
		So(err, ShouldBeNil)

		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			return tx.DeleteBucket(bucketDepGroups)
		})
		So(err, ShouldBeNil)
		So(testDB.close(ctx), ShouldBeNil)

		testDB, _, err = initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		seen, err := testDB.depGroupEverSeen("legacy")
		So(err, ShouldBeNil)
		So(seen, ShouldBeTrue)

		seen, err = testDB.depGroupEverSeen("absent")
		So(err, ShouldBeNil)
		So(seen, ShouldBeFalse)
	})
}

func TestDBStartWriteRecovery(t *testing.T) {
	Convey("Given a db with a stored, not-yet-started job and a started job", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		notStarted := testDBJob("echo not-started", "recover")
		started := testDBJob("echo started", "recover")

		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{notStarted, started}, false)
		So(err, ShouldBeNil)

		startedKey := started.Key()
		notStartedKey := notStarted.Key()

		Convey("After a started job is persisted and the db is closed and reopened (clean restart)", func() {
			markJobStarted(started, 4321, "host-a")
			testDB.updateJobAfterStart(ctx, started)

			// close() must flush the still-pending start-write so a job that is
			// still running when the manager stops is recoverable.
			So(testDB.close(ctx), ShouldBeNil)

			testDB, err = reopenDB(ctx, dbFile, dbBackup)
			So(err, ShouldBeNil)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			recovered, errr := testDB.recoverIncompleteJobs()
			So(errr, ShouldBeNil)

			byKey := jobsByKey(recovered)
			So(byKey, ShouldContainKey, startedKey)
			So(byKey, ShouldContainKey, notStartedKey)

			// recovered RUNNING => put on the run queue (not re-run); its pid/host
			// are what recoverRunningJob re-monitors.
			Convey("the started job is recovered as RUNNING with its pid/host", func() {
				So(byKey[startedKey].State, ShouldEqual, JobStateRunning)
				So(byKey[startedKey].Pid, ShouldEqual, 4321)
				So(byKey[startedKey].Host, ShouldEqual, "host-a")
			})

			// recovered non-running => runnable, so it still runs at least once.
			Convey("the never-started job is recovered in a runnable (non-running) state", func() {
				So(byKey[notStartedKey].State, ShouldNotEqual, JobStateRunning)
			})
		})

		Convey("The running-state becomes durable even without closing, within the flush window", func() {
			markJobStarted(started, 9876, "host-b")
			testDB.updateJobAfterStart(ctx, started)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			// poll the live bucket via the recovery path until the background
			// flusher has made the running-state durable (deterministic on the
			// success path: a generous bound only matters under load).
			recoveredRunning := pollForRecoveredState(testDB, startedKey, JobStateRunning, 30*time.Second)
			So(recoveredRunning, ShouldBeTrue)
		})
	})
}

func TestDBShortJobNotResurrected(t *testing.T) {
	Convey("Given a stored job whose start-write is deferred and then archived before flushing", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		// widen ONLY the start-write flusher's debounce (not bbolt's MaxBatchDelay,
		// which would also slow archiveJob's own batch) so the start-write is still
		// pending (not yet flushed) when we archive, exercising the cancel path.
		setFlusherDelay(testDB, 30*time.Second)

		short := testDBJob("echo short", "short")
		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{short}, false)
		So(err, ShouldBeNil)

		key := short.Key()

		present, err := bucketHasKey(testDB, bucketJobsLive, key)
		So(err, ShouldBeNil)
		So(present, ShouldBeTrue)

		Convey("When the job starts and is then immediately archived (completes)", func() {
			markJobStarted(short, 111, "host-c")
			testDB.updateJobAfterStart(ctx, short)

			completeJob(short)
			So(testDB.archiveJob(ctx, key, short), ShouldBeNil)

			Convey("the job ends up archived and absent from the live bucket, even after a clean close", func() {
				// the pending start-write must have been cancelled, so nothing
				// can re-add the job to the live bucket.
				So(pendingStartWriteCount(testDB), ShouldEqual, 0)

				// close forces a final flush; the bjl.Get guard means even a raced
				// flush would not resurrect the archived job.
				So(testDB.close(ctx), ShouldBeNil)

				testDB, err = reopenDB(ctx, dbFile, dbBackup)
				So(err, ShouldBeNil)

				defer func() { So(testDB.close(ctx), ShouldBeNil) }()

				inLive, errl := bucketHasKey(testDB, bucketJobsLive, key)
				So(errl, ShouldBeNil)
				So(inLive, ShouldBeFalse)

				inComplete, errc := bucketHasKey(testDB, bucketJobsComplete, key)
				So(errc, ShouldBeNil)
				So(inComplete, ShouldBeTrue)

				recovered, errr := testDB.recoverIncompleteJobs()
				So(errr, ShouldBeNil)
				So(jobsByKey(recovered), ShouldNotContainKey, key)
			})
		})
	})
}

func TestDBStartWriteCommitCoalescing(t *testing.T) {
	Convey("Short jobs that complete before their start-write flushes incur no redundant start-write commit", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()

		testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		// a long flusher debounce guarantees no start-write flush happens during
		// the test, so any start commit we observe would be a redundant one. We
		// widen only the flusher (not bbolt's MaxBatchDelay) so archiveJob's own
		// batch is unaffected.
		setFlusherDelay(testDB, 60*time.Second)

		const n = 200

		jobs := make([]*Job, n)
		for i := range n {
			jobs[i] = testDBJob(fmt.Sprintf("echo coalesce %d", i), "coalesce")
		}

		_, _, _, err = testDB.storeNewJobs(ctx, jobs, false)
		So(err, ShouldBeNil)

		// let the add-time storage and its background backup settle.
		testDB.wg.Wait(30 * time.Second)

		writesBeforeStarts := boltWrites(testDB)

		// start every job (defers the start-writes into the pending map).
		for i, job := range jobs {
			markJobStarted(job, 1000+i, "host")
			testDB.updateJobAfterStart(ctx, job)
		}

		So(pendingStartWriteCount(testDB), ShouldEqual, n)

		// no start commit should have happened yet (writes deferred, window long).
		So(boltWrites(testDB), ShouldEqual, writesBeforeStarts)

		// now complete (archive) every job before the window elapses; this should
		// cancel all the pending start-writes.
		for i, job := range jobs {
			completeJob(job)
			So(testDB.archiveJob(ctx, jobs[i].Key(), job), ShouldBeNil)
		}

		So(pendingStartWriteCount(testDB), ShouldEqual, 0)

		// force the flusher to run; with all start-writes cancelled it writes
		// nothing, so there is no start-write commit on top of the archives.
		writesBeforeFlush := boltWrites(testDB)
		testDB.flushPendingStartWrites(ctx)
		So(boltWrites(testDB), ShouldEqual, writesBeforeFlush)

		// and the live bucket is empty: every job is archived, none resurrected.
		liveN, errc := liveBucketCount(testDB)
		So(errc, ShouldBeNil)
		So(liveN, ShouldEqual, 0)
	})
}

// setFlusherDelay overrides the start-write flusher's debounce window without
// touching bbolt's MaxBatchDelay (so other bolt.Batch callers stay fast). Used
// by tests to keep start-writes pending long enough to exercise the cancel
// path deterministically.
func setFlusherDelay(testDB *db, delay time.Duration) {
	testDB.Lock()
	testDB.batchDelay = delay
	testDB.Unlock()
}

// TestDBStartWriteConcurrency hammers the start-write path concurrently with
// archiving and a clean close, the way the live server reaches these methods
// from many simultaneous client-request handlers. It is primarily a -race
// target for the shared pendingStartWrites map and the single flusher, but it
// also asserts the persisted-state invariants that must survive the churn:
// archived jobs are never in the live bucket, and still-running jobs are
// durable for recovery (so they are recovered as RUNNING, not re-run).
func TestDBStartWriteConcurrency(t *testing.T) {
	Convey("Concurrent starts, archives and a close keep persisted state correct", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		const (
			workers      = 16
			archivedJobs = 400 // started then archived (short jobs)
			runningJobs  = 100 // started but left running at shutdown
		)

		archived := make([]*Job, archivedJobs)
		for i := range archivedJobs {
			archived[i] = testDBJob(fmt.Sprintf("echo short %d", i), "concurrent")
		}

		running := make([]*Job, runningJobs)
		for i := range runningJobs {
			running[i] = testDBJob(fmt.Sprintf("echo long %d", i), "concurrent")
		}

		all := append(append([]*Job{}, archived...), running...)
		_, _, _, err = testDB.storeNewJobs(ctx, all, false)
		So(err, ShouldBeNil)

		// drive start (+archive for the short ones) concurrently across workers.
		archiveErrs := runStartWriteWorkers(ctx, testDB, workers, archived, running)
		So(archiveErrs, ShouldEqual, 0)

		// close flushes the still-pending start-writes of the running jobs.
		So(testDB.close(ctx), ShouldBeNil)

		testDB, err = reopenDB(ctx, dbFile, dbBackup)
		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		recovered, errr := testDB.recoverIncompleteJobs()
		So(errr, ShouldBeNil)

		byKey := jobsByKey(recovered)

		Convey("every archived (short) job is absent from the live bucket", func() {
			missing := 0

			for _, job := range archived {
				if _, ok := byKey[job.Key()]; !ok {
					missing++
				}
			}

			So(missing, ShouldEqual, archivedJobs)
		})

		Convey("every still-running job is recovered as RUNNING (durable, so not re-run)", func() {
			recoveredRunning := 0

			for _, job := range running {
				if rj, ok := byKey[job.Key()]; ok && rj.State == JobStateRunning {
					recoveredRunning++
				}
			}

			So(recoveredRunning, ShouldEqual, runningJobs)
		})
	})
}

func BenchmarkModifyLiveJobsReverseLookup(b *testing.B) {
	ctx := context.Background()
	tmpdir := b.TempDir()

	testDB, _, err := initDB(
		ctx,
		filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"),
		internal.Development,
		false,
		false,
	)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if closeErr := testDB.close(ctx); closeErr != nil {
			b.Fatal(closeErr)
		}
	})

	const seedJobs = 5000

	jobs := make([]*Job, 0, seedJobs)
	for i := range seedJobs {
		job := testDBJob(fmt.Sprintf("echo seed %d", i), "seed")
		job.DepGroups = []string{fmt.Sprintf("seed-dg-%d", i)}
		job.Dependencies = Dependencies{NewDepGroupDependency(fmt.Sprintf("seed-parent-%d", i))}
		jobs = append(jobs, job)
	}

	if _, _, _, err = testDB.storeNewJobs(ctx, jobs, false); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()

	for i := range b.N {
		b.StopTimer()

		target := testDBJob(fmt.Sprintf("echo target %d", i), "target")
		target.DepGroups = []string{fmt.Sprintf("target-dg-%d", i)}

		target.Dependencies = Dependencies{NewDepGroupDependency(fmt.Sprintf("target-parent-%d", i))}
		if _, _, _, err = testDB.storeNewJobs(ctx, []*Job{target}, false); err != nil {
			b.Fatal(err)
		}

		modified := testDBJob(fmt.Sprintf("echo modified target %d", i), "target")
		modified.DepGroups = []string{fmt.Sprintf("modified-target-dg-%d", i)}
		modified.Dependencies = Dependencies{NewDepGroupDependency(fmt.Sprintf("modified-target-parent-%d", i))}
		oldKey := target.Key()

		b.StartTimer()

		if err = testDB.modifyLiveJobs(ctx, []string{oldKey}, []*Job{modified}); err != nil {
			b.Fatal(err)
		}
	}
}

func testDBJob(cmd, repGroup string) *Job {
	return &Job{
		Cmd:      cmd,
		Cwd:      testCwd,
		ReqGroup: "db_test",
		Requirements: &jqs.Requirements{
			RAM:   10,
			Time:  time.Second,
			Cores: 1,
		},
		RepGroup: repGroup,
	}
}

// runStartWriteWorkers reports each short job started then archived, and each
// running job started (and left running), spread across the given number of
// worker goroutines so the start-write/archive paths and the flusher run
// concurrently. It returns the number of archive errors encountered.
func runStartWriteWorkers(ctx context.Context, testDB *db, workers int, archived, running []*Job) int {
	type unit struct {
		job     *Job
		archive bool
	}

	work := make(chan unit)

	var (
		wg        sync.WaitGroup
		archiveEr atomic.Int64
	)

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for u := range work {
				markJobStarted(u.job, 1, "host")
				testDB.updateJobAfterStart(ctx, u.job)

				if !u.archive {
					continue
				}

				completeJob(u.job)

				if err := testDB.archiveJob(ctx, u.job.Key(), u.job); err != nil {
					archiveEr.Add(1)
				}
			}
		}()
	}

	units := make([]unit, 0, len(archived)+len(running))
	for _, job := range archived {
		units = append(units, unit{job: job, archive: true})
	}

	for _, job := range running {
		units = append(units, unit{job: job, archive: false})
	}

	for _, u := range units {
		work <- u
	}

	close(work)
	wg.Wait()

	return int(archiveEr.Load())
}

// bucketHasKey reports whether the given key is present in the named bucket.
func bucketHasKey(testDB *db, bucket []byte, key string) (bool, error) {
	present := false

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		present = tx.Bucket(bucket).Get([]byte(key)) != nil

		return nil
	})

	return present, err
}

// markJobStarted sets the fields handleStart sets when a runner reports its
// command has started running.
func markJobStarted(job *Job, pid int, host string) {
	job.Lock()
	defer job.Unlock()

	job.State = JobStateRunning
	job.Pid = pid
	job.Host = host
	job.StartTime = time.Now()
}

func countLookupEntriesByJobKey(tx *bolt.Tx, jobKey string) int {
	suffix := []byte(dbDelimiter + jobKey)
	count := 0

	for _, bucket := range indexedLookupBuckets() {
		b := tx.Bucket(bucket)
		if b == nil {
			continue
		}

		err := b.ForEach(func(k, _ []byte) error {
			if bytes.HasSuffix(k, suffix) {
				count++
			}

			return nil
		})
		if err != nil {
			continue
		}
	}

	return count
}

func countReverseLookupEntriesByJobKey(tx *bolt.Tx, jobKey string) int {
	b := tx.Bucket(bucketJobLookupEntries)
	if b == nil {
		return 0
	}

	prefix := reverseLookupEntryPrefix([]byte(jobKey))
	count := 0

	c := b.Cursor()
	for k, _ := c.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		count++
	}

	return count
}

// pendingStartWriteCount returns how many start-writes are currently pending a
// flush.
func pendingStartWriteCount(testDB *db) int {
	testDB.startWriteMu.Lock()
	defer testDB.startWriteMu.Unlock()

	return len(testDB.pendingStartWrites)
}

// completeJob sets the fields a successfully exited job has before archiveJob.
func completeJob(job *Job) {
	job.Lock()
	defer job.Unlock()

	job.State = JobStateComplete
	job.Exited = true
	job.Exitcode = 0

	if job.StartTime.IsZero() {
		job.StartTime = time.Now()
	}

	job.EndTime = job.StartTime.Add(time.Second)
}

// reopenDB closes nothing; it opens an existing db file the same way the server
// does on restart, so recovery paths can be exercised.
func reopenDB(ctx context.Context, dbFile, dbBackup string) (*db, error) {
	reopened, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)

	return reopened, err
}

// liveBucketCount returns the number of entries in the live bucket.
func liveBucketCount(testDB *db) (int, error) {
	count := 0

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(bucketJobsLive).Stats().KeyN

		return nil
	})

	return count, err
}

// pollForRecoveredState polls the recovery path until the job with the given
// key is recovered in the wanted state, or the timeout elapses. It is
// deterministic on the success path: the bound only matters under heavy load.
func pollForRecoveredState(testDB *db, key string, want JobState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jobs, err := testDB.recoverIncompleteJobs()
		if err == nil {
			if job, ok := jobsByKey(jobs)[key]; ok && job.State == want {
				return true
			}
		}

		time.Sleep(time.Millisecond)
	}

	return false
}

// jobsByKey indexes recovered jobs by their Key() for assertion.
func jobsByKey(jobs []*Job) map[string]*Job {
	byKey := make(map[string]*Job, len(jobs))
	for _, job := range jobs {
		byKey[job.Key()] = job
	}

	return byKey
}
