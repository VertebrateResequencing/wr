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
	"encoding/binary"
	"fmt"
	"path/filepath"
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

func TestDBEndTimeIndex(t *testing.T) {
	ctx := context.Background()

	Convey("Given a fresh db", t, func() {
		tmpdir := t.TempDir()

		testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
		So(err, ShouldBeNil)

		Convey("Archiving a job records it in the end-time index with the correct bytes", func() {
			endTime := time.Now().Add(-30 * time.Minute).Truncate(time.Nanosecond)
			job := testDBArchivedJob("echo recent", "rg-recent", endTime)

			err = testDB.archiveJob(ctx, job.Key(), job)
			So(err, ShouldBeNil)

			entries := endTimeIndexEntries(t, testDB)
			So(len(entries), ShouldEqual, 1)

			wantNanos := make([]byte, endTimeBytes)
			binary.BigEndian.PutUint64(wantNanos, uint64(endTime.UnixNano()))

			So(bytes.Equal(entries[0], endTimeIndexKey(wantNanos, []byte(job.Key()))), ShouldBeTrue)
			So(string(lookupEntryJobKey(entries[0])), ShouldEqual, job.Key())
			So(bytes.Equal(entries[0][:endTimeBytes], wantNanos), ShouldBeTrue)
		})

		Convey("Re-archiving the same key at a later end time replaces the entry (no stale T1)", func() {
			t1 := time.Now().Add(-2 * time.Hour).Truncate(time.Nanosecond)
			job := testDBArchivedJob("echo rerun", "rg-rerun", t1)

			err = testDB.archiveJob(ctx, job.Key(), job)
			So(err, ShouldBeNil)

			t2 := time.Now().Add(-1 * time.Minute).Truncate(time.Nanosecond)
			job.EndTime = t2

			err = testDB.archiveJob(ctx, job.Key(), job)
			So(err, ShouldBeNil)

			entries := endTimeIndexEntries(t, testDB)
			So(len(entries), ShouldEqual, 1)

			wantT2 := make([]byte, endTimeBytes)
			binary.BigEndian.PutUint64(wantT2, uint64(t2.UnixNano()))

			So(bytes.Equal(entries[0], endTimeIndexKey(wantT2, []byte(job.Key()))), ShouldBeTrue)
			So(string(lookupEntryJobKey(entries[0])), ShouldEqual, job.Key())

			oldNanos := make([]byte, endTimeBytes)
			binary.BigEndian.PutUint64(oldNanos, uint64(t1.UnixNano()))
			So(bytes.Equal(entries[0], endTimeIndexKey(oldNanos, []byte(job.Key()))), ShouldBeFalse)
		})

		Convey("Re-archiving the same key with an unchanged end time is idempotent", func() {
			endTime := time.Now().Add(-15 * time.Minute).Truncate(time.Nanosecond)
			job := testDBArchivedJob("echo same", "rg-same", endTime)

			err = testDB.archiveJob(ctx, job.Key(), job)
			So(err, ShouldBeNil)

			err = testDB.archiveJob(ctx, job.Key(), job)
			So(err, ShouldBeNil)

			entries := endTimeIndexEntries(t, testDB)
			So(len(entries), ShouldEqual, 1)

			wantNanos := make([]byte, endTimeBytes)
			binary.BigEndian.PutUint64(wantNanos, uint64(endTime.UnixNano()))
			So(bytes.Equal(entries[0], endTimeIndexKey(wantNanos, []byte(job.Key()))), ShouldBeTrue)
		})

		Convey("Retrieving from a never-written index returns an empty slice and no error", func() {
			jobs, errr := testDB.retrieveCompleteJobsRecent(time.Now().Add(-time.Hour))
			So(errr, ShouldBeNil)
			So(jobs, ShouldBeEmpty)
		})
	})
}

func TestDBRetrieveCompleteJobsRecent(t *testing.T) {
	ctx := context.Background()

	Convey("Given a fresh db", t, func() {
		tmpdir := t.TempDir()

		testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
		So(err, ShouldBeNil)

		Convey("With three archived jobs ending at now-3h, now-30m and now-1m", func() {
			now := time.Now()
			oldEnd := now.Add(-3 * time.Hour).Truncate(time.Nanosecond)
			midEnd := now.Add(-30 * time.Minute).Truncate(time.Nanosecond)
			newEnd := now.Add(-1 * time.Minute).Truncate(time.Nanosecond)

			oldJob := testDBArchivedJob("echo old", "rg-old", oldEnd)
			midJob := testDBArchivedJob("echo mid", "rg-mid", midEnd)
			newJob := testDBArchivedJob("echo new", "rg-new", newEnd)

			So(oldJob.Key(), ShouldNotEqual, midJob.Key())
			So(midJob.Key(), ShouldNotEqual, newJob.Key())

			for _, job := range []*Job{oldJob, midJob, newJob} {
				So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
			}

			Convey("retrieveCompleteJobsRecent(now-1h) returns the in-window jobs in ascending end-time order", func() {
				jobs, errr := testDB.retrieveCompleteJobsRecent(now.Add(-time.Hour))
				So(errr, ShouldBeNil)
				So(len(jobs), ShouldEqual, 2)
				So(jobs[0].Key(), ShouldEqual, midJob.Key())
				So(jobs[0].EndTime.UnixNano(), ShouldEqual, midEnd.UnixNano())
				So(jobs[1].Key(), ShouldEqual, newJob.Key())
				So(jobs[1].EndTime.UnixNano(), ShouldEqual, newEnd.UnixNano())

				keys := []string{jobs[0].Key(), jobs[1].Key()}
				So(keys, ShouldNotContain, oldJob.Key())
			})
		})

		Convey("With a job archived at T whose key is also live again (re-running)", func() {
			endTime := time.Now().Add(-30 * time.Minute).Truncate(time.Nanosecond)
			job := testDBArchivedJob("echo rerunning", "rg-rerun", endTime)

			So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

			Convey("It is returned before being made live again", func() {
				jobs, errr := testDB.retrieveCompleteJobsRecent(endTime.Add(-time.Minute))
				So(errr, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)
				So(jobs[0].Key(), ShouldEqual, job.Key())
			})

			Convey("retrieveCompleteJobsRecent(T-1m) skips it once the key is live again", func() {
				liveAgain := testDBJob("echo rerunning", "rg-rerun")
				So(liveAgain.Key(), ShouldEqual, job.Key())

				_, _, _, errs := testDB.storeNewJobs(ctx, []*Job{liveAgain}, false)
				So(errs, ShouldBeNil)

				jobs, errr := testDB.retrieveCompleteJobsRecent(endTime.Add(-time.Minute))
				So(errr, ShouldBeNil)
				So(jobs, ShouldBeEmpty)
			})
		})

		Convey("With an empty complete store, retrieveCompleteJobsRecent(now-1h) returns empty and no error", func() {
			jobs, errr := testDB.retrieveCompleteJobsRecent(time.Now().Add(-time.Hour))
			So(errr, ShouldBeNil)
			So(jobs, ShouldBeEmpty)
		})
	})
}

func TestDBEndTimeIndexDurability(t *testing.T) {
	ctx := context.Background()

	Convey("Given a db backed by an on-disk file", t, func() {
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		Convey("An archived job's index entry survives a close/reopen from the same file", func() {
			now := time.Now()
			endTime := now.Add(-30 * time.Minute).Truncate(time.Nanosecond)
			job := testDBArchivedJob("echo durable", "rg-durable", endTime)

			So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
			So(testDB.close(ctx), ShouldBeNil)

			testDB, _, err = initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
			So(err, ShouldBeNil)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			jobs, errr := testDB.retrieveCompleteJobsRecent(now.Add(-time.Hour))
			So(errr, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].Key(), ShouldEqual, job.Key())
			So(jobs[0].EndTime.UnixNano(), ShouldEqual, endTime.UnixNano())
		})

		Convey("A key re-archived at T2 is returned once with its T2 end time after reopen", func() {
			t1 := time.Now().Add(-2 * time.Hour).Truncate(time.Nanosecond)
			job := testDBArchivedJob("echo rerun", "rg-rerun", t1)

			So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

			t2 := time.Now().Add(-1 * time.Minute).Truncate(time.Nanosecond)
			job.EndTime = t2

			So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
			So(testDB.close(ctx), ShouldBeNil)

			testDB, _, err = initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
			So(err, ShouldBeNil)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			jobs, errr := testDB.retrieveCompleteJobsRecent(t1.Add(-time.Minute))
			So(errr, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].Key(), ShouldEqual, job.Key())
			So(jobs[0].EndTime.UnixNano(), ShouldEqual, t2.UnixNano())

			entries := endTimeIndexEntries(t, testDB)
			So(len(entries), ShouldEqual, 1)

			wantT2 := make([]byte, endTimeBytes)
			binary.BigEndian.PutUint64(wantT2, uint64(t2.UnixNano()))
			So(bytes.Equal(entries[0], endTimeIndexKey(wantT2, []byte(job.Key()))), ShouldBeTrue)
		})
	})
}

// testDBArchivedJob builds a job ready to be archived: it has its end state
// (StartTime, EndTime, exit status and peak usage) populated so archiveJob can
// record stats and the end-time index.
func testDBArchivedJob(cmd, repGroup string, endTime time.Time) *Job {
	job := testDBJob(cmd, repGroup)
	job.State = JobStateComplete
	job.Exited = true
	job.StartTime = endTime.Add(-time.Second)
	job.EndTime = endTime
	job.PeakRAM = 10
	job.PeakDisk = 1
	job.CPUtime = time.Second

	return job
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

// endTimeIndexEntries returns every key currently in bucketEndTimeToKey.
func endTimeIndexEntries(t *testing.T, testDB *db) [][]byte {
	t.Helper()

	var entries [][]byte

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketEndTimeToKey).ForEach(func(k, _ []byte) error {
			entries = append(entries, bytes.Clone(k))

			return nil
		})
	})
	So(err, ShouldBeNil)

	return entries
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
