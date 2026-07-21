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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
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

func TestDBHighPeakMemoryRecommendation(t *testing.T) {
	Convey("A high-memory non-RAM failure seeds recommendations but honours override always", t, func() {
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
			reqGroup     = "high-peak-signal"
			requestedRAM = 16
			peakRAM      = 543
		)

		now := time.Now()
		job := testDBJob("python3 -c alloc", "rg-high-peak")
		job.ReqGroup = reqGroup
		job.Requirements.RAM = requestedRAM
		job.Requirements.Time = time.Minute
		job.State = JobStateDelayed
		job.Exited = true
		job.Exitcode = -1
		job.FailReason = FailReasonSignal
		job.PeakRAM = peakRAM
		job.StartTime = now.Add(-time.Second)
		job.EndTime = now

		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{job}, false)
		So(err, ShouldBeNil)

		testDB.updateJobAfterExit(ctx, job, nil, nil, false)
		testDB.waitForJobExitUpdates()

		recRAM, err := testDB.recommendedReqGroupMemory(reqGroup)
		So(err, ShouldBeNil)
		So(recRAM, ShouldEqual, 600)

		server := &Server{db: testDB}
		recommendedReq := server.recommendedReqForGroup(reqGroup, make(map[string]*jqs.Requirements))
		So(recommendedReq, ShouldNotBeNil)
		So(recommendedReq.RAM, ShouldEqual, 600)

		newRetry := func(override uint8) *Job {
			retry := testDBJob("python3 -c alloc", "rg-high-peak")
			retry.ReqGroup = reqGroup
			retry.Override = override
			retry.Requirements.RAM = requestedRAM
			retry.Requirements.Time = time.Minute
			retry.State = JobStateDelayed
			retry.FailReason = FailReasonSignal
			retry.PeakRAM = peakRAM

			return retry
		}

		systemRetry := newRetry(jobOverridePreferSystemReqs)
		updateJobRequirementsForRetry(systemRetry, systemRetry.Override, recommendedReq)
		So(systemRetry.Requirements.RAM, ShouldEqual, 600)
		So(systemRetry.RequirementsOrig.RAM, ShouldEqual, requestedRAM)
		So(systemRetry.FailReason, ShouldEqual, FailReasonSignal)

		alwaysRetry := newRetry(jobOverrideAlwaysUseJobReqs)
		updateJobRequirementsForRetry(alwaysRetry, alwaysRetry.Override, recommendedReq)
		So(alwaysRetry.Requirements.RAM, ShouldEqual, requestedRAM)
		So(alwaysRetry.RequirementsOrig.RAM, ShouldEqual, requestedRAM)
		So(alwaysRetry.FailReason, ShouldEqual, FailReasonSignal)
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

func TestDBUpgradeProgress(t *testing.T) {
	Convey("Opening an old DB logs upgrade progress and clears the status sidecar", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		logs := clog.ToBufferAtLevel("info")

		defer clog.ToDefault()

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		parent := testDBJob("echo parent", "upgrade-parent")
		parent.DepGroups = []string{"upgrade-parent-dg"}

		child := testDBJob("echo child", "upgrade-child")
		child.Dependencies = Dependencies{NewDepGroupDependency("upgrade-parent-dg")}

		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{parent, child}, false)
		So(err, ShouldBeNil)

		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			if errd := tx.DeleteBucket(bucketDepGroups); errd != nil {
				return errd
			}

			return tx.DeleteBucket(bucketJobLookupEntries)
		})
		So(err, ShouldBeNil)
		So(testDB.close(ctx), ShouldBeNil)

		logs.Reset()

		testDB, _, err = initDB(ctx, dbFile, dbBackup, internal.Development, false, false)

		So(err, ShouldBeNil)
		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		output := logs.String()
		So(output, ShouldContainSubstring, "database upgrade started")
		So(output, ShouldContainSubstring, "database upgrade step started")
		So(output, ShouldContainSubstring, "rebuilding database dependency-group index")
		So(output, ShouldContainSubstring, "database upgrade step complete")
		So(output, ShouldContainSubstring, "rebuilding database job lookup index")
		So(output, ShouldContainSubstring, "committing database upgrade")
		So(output, ShouldContainSubstring, "database upgrade complete")
		So(output, ShouldContainSubstring, "processed=")
		So(output, ShouldContainSubstring, "took=")

		_, _, err = internal.ReadDBUpgradeStatus(dbFile)
		So(os.IsNotExist(err), ShouldBeTrue)
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

			wantNanos := wantEndTimeBytes(endTime)

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

			So(bytes.Equal(entries[0], endTimeIndexKey(wantEndTimeBytes(t2), []byte(job.Key()))), ShouldBeTrue)
			So(string(lookupEntryJobKey(entries[0])), ShouldEqual, job.Key())

			So(bytes.Equal(entries[0], endTimeIndexKey(wantEndTimeBytes(t1), []byte(job.Key()))), ShouldBeFalse)
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

			So(bytes.Equal(entries[0], endTimeIndexKey(wantEndTimeBytes(endTime), []byte(job.Key()))), ShouldBeTrue)
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

		Convey("With the end-time index bucket absent, retrieveCompleteJobsRecent returns empty, no error, no panic", func() {
			So(testDB.bolt.Update(func(tx *bolt.Tx) error {
				return tx.DeleteBucket(bucketEndTimeToKey)
			}), ShouldBeNil)

			So(func() {
				jobs, errr := testDB.retrieveCompleteJobsRecent(time.Now().Add(-time.Hour))
				So(errr, ShouldBeNil)
				So(jobs, ShouldBeEmpty)
			}, ShouldNotPanic)
		})

		Convey("With a job archived at now, a cutoff before 1970 returns it (huge window)", func() {
			endTime := time.Now().Add(-time.Minute).Truncate(time.Nanosecond)
			job := testDBArchivedJob("echo ancient", "rg-ancient", endTime)

			So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

			Convey("retrieveCompleteJobsRecent(pre-1970) returns the job, not an empty slice", func() {
				preEpoch := time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC)
				So(preEpoch.UnixNano() < 0, ShouldBeTrue)

				jobs, errr := testDB.retrieveCompleteJobsRecent(preEpoch)
				So(errr, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)
				So(jobs[0].Key(), ShouldEqual, job.Key())
				So(jobs[0].EndTime.UnixNano(), ShouldEqual, endTime.UnixNano())
			})
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

			So(bytes.Equal(entries[0], endTimeIndexKey(wantEndTimeBytes(t2), []byte(job.Key()))), ShouldBeTrue)
		})
	})
}

// TestDBMapFreelistOpen covers D1 acceptance test 1: a fresh db opened by
// initDB and an existing db reopened by initDB both open without error, and the
// map freelist option actually takes effect (the option only affects freelist
// representation, so behaviour is otherwise unchanged).
func TestDBMapFreelistOpen(t *testing.T) {
	ctx := context.Background()

	Convey("initDB opens a fresh db with the map freelist", t, func() {
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)
		So(testDB.bolt.FreelistType, ShouldEqual, bolt.FreelistMapType)

		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{testDBJob("echo freelist", "rg-freelist")}, false)
		So(err, ShouldBeNil)
		So(testDB.close(ctx), ShouldBeNil)

		Convey("and reopening the existing db also uses the map freelist without error", func() {
			testDB, _, err = initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
			So(err, ShouldBeNil)
			So(testDB.bolt.FreelistType, ShouldEqual, bolt.FreelistMapType)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			jobs, errr := testDB.recoverIncompleteJobs()
			So(errr, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].Cmd, ShouldEqual, "echo freelist")
		})
	})
}

// TestDBMapFreelistRoundTrip covers D1 acceptance test 2: a db written and
// reopened from disk round-trips both stored (live) and archived jobs
// identically under the map freelist.
func TestDBMapFreelistRoundTrip(t *testing.T) {
	ctx := context.Background()

	Convey("Given a db with live and archived jobs written to an on-disk file", t, func() {
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		liveA := testDBJob("echo live a", "rg-live-a")
		liveB := testDBJob("echo live b", "rg-live-b")

		_, _, _, err = testDB.storeNewJobs(ctx, []*Job{liveA, liveB}, false)
		So(err, ShouldBeNil)

		endTime := time.Now().Add(-30 * time.Minute).Truncate(time.Nanosecond)
		archived := testDBArchivedJob("echo archived", "rg-archived", endTime)
		So(testDB.archiveJob(ctx, archived.Key(), archived), ShouldBeNil)

		So(testDB.close(ctx), ShouldBeNil)

		Convey("reopening reads the same stored and archived jobs back", func() {
			testDB, _, err = initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
			So(err, ShouldBeNil)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			live, errr := testDB.recoverIncompleteJobs()
			So(errr, ShouldBeNil)
			So(len(live), ShouldEqual, 2)

			byKey := make(map[string]*Job, len(live))
			for _, job := range live {
				byKey[job.Key()] = job
			}

			So(byKey[liveA.Key()], ShouldNotBeNil)
			So(byKey[liveA.Key()].Cmd, ShouldEqual, liveA.Cmd)
			So(byKey[liveA.Key()].RepGroup, ShouldEqual, liveA.RepGroup)
			So(byKey[liveB.Key()], ShouldNotBeNil)
			So(byKey[liveB.Key()].Cmd, ShouldEqual, liveB.Cmd)
			So(byKey[liveB.Key()].RepGroup, ShouldEqual, liveB.RepGroup)

			complete, errr := testDB.retrieveCompleteJobsByKeys([]string{archived.Key()})
			So(errr, ShouldBeNil)
			So(len(complete), ShouldEqual, 1)
			So(complete[0].Key(), ShouldEqual, archived.Key())
			So(complete[0].Cmd, ShouldEqual, archived.Cmd)
			So(complete[0].State, ShouldEqual, JobStateComplete)
			So(complete[0].EndTime.UnixNano(), ShouldEqual, endTime.UnixNano())
		})
	})
}

// TestDBCompactRoundTrip covers D2 acceptance test 1: a stopped-manager db file
// with churn (free pages) compacts, via the exported CompactDBFile, to a valid
// db whose top-level buckets and every job / lookup / counter round-trip
// identically to the original, and whose output size is <= the input.
func TestDBCompactRoundTrip(t *testing.T) {
	ctx := context.Background()

	Convey("Given an on-disk db churned to leave free pages", t, func() {
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")
		dbBackup := filepath.Join(tmpdir, "queue.db.bak")

		testDB, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
		So(err, ShouldBeNil)

		const perGroup = 60

		repGroups := []string{"rg-a", "rg-b", "rg-c"}

		var archivedKeys, deleteKeys []string

		endTime := time.Now().Add(-time.Hour).Truncate(time.Nanosecond)

		for _, rg := range repGroups {
			batch := make([]*Job, 0, perGroup)
			for i := range perGroup {
				batch = append(batch, testDBJob(fmt.Sprintf("echo %s %d", rg, i), rg))
			}

			_, _, _, errs := testDB.storeNewJobs(ctx, batch, false)
			So(errs, ShouldBeNil)

			for i, job := range batch {
				switch i % 3 {
				case 0:
					aj := testDBArchivedJob(job.Cmd, rg, endTime)
					So(testDB.archiveJob(ctx, aj.Key(), aj), ShouldBeNil)
					archivedKeys = append(archivedKeys, aj.Key())
				case 1:
					deleteKeys = append(deleteKeys, job.Key())
				}
			}
		}

		So(testDB.deleteLiveJobs(ctx, deleteKeys), ShouldBeNil)

		wantBuckets := dbTopLevelBuckets(t, testDB)
		wantLive := liveJobCmdsByKey(t, testDB)
		wantRTK := dbBucketKeys(t, testDB, bucketRTK)
		wantComplete := dbBucketKeys(t, testDB, bucketJobsComplete)

		wantArchived, erra := testDB.retrieveCompleteJobsByKeys(archivedKeys)
		So(erra, ShouldBeNil)
		So(len(wantArchived), ShouldEqual, len(archivedKeys))

		wantArchivedSummary := archivedJobSummaries(wantArchived)

		So(testDB.close(ctx), ShouldBeNil)

		Convey("CompactDBFile shrinks it and preserves every bucket/job/lookup", func() {
			beforeSize, afterSize, errcmp := CompactDBFile(dbFile)
			So(errcmp, ShouldBeNil)
			So(beforeSize, ShouldBeGreaterThan, 0)
			So(afterSize, ShouldBeLessThanOrEqualTo, beforeSize)

			reDB, _, errr := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
			So(errr, ShouldBeNil)

			defer func() { So(reDB.close(ctx), ShouldBeNil) }()

			So(dbTopLevelBuckets(t, reDB), ShouldResemble, wantBuckets)
			So(liveJobCmdsByKey(t, reDB), ShouldResemble, wantLive)
			So(dbBucketKeys(t, reDB, bucketRTK), ShouldResemble, wantRTK)
			So(dbBucketKeys(t, reDB, bucketJobsComplete), ShouldResemble, wantComplete)

			gotArchived, errga := reDB.retrieveCompleteJobsByKeys(archivedKeys)
			So(errga, ShouldBeNil)
			So(archivedJobSummaries(gotArchived), ShouldResemble, wantArchivedSummary)
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

// dbTopLevelBuckets returns the names of every top-level bucket in the db, in
// BoltDB's key order (so two calls on equivalent DBs compare directly).
func dbTopLevelBuckets(t *testing.T, testDB *db) []string {
	t.Helper()

	var names []string

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			names = append(names, string(name))

			return nil
		})
	})
	So(err, ShouldBeNil)

	return names
}

// liveJobCmdsByKey decodes every live job and maps its Key to its Cmd.
func liveJobCmdsByKey(t *testing.T, testDB *db) map[string]string {
	t.Helper()

	jobs, err := testDB.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	byKey := make(map[string]string, len(jobs))
	for _, job := range jobs {
		byKey[job.Key()] = job.Cmd
	}

	return byKey
}

// dbBucketKeys returns every key in the named top-level bucket, in BoltDB's key
// order (empty if the bucket is absent).
func dbBucketKeys(t *testing.T, testDB *db, bucket []byte) []string {
	t.Helper()

	var keys []string

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil
		}

		return b.ForEach(func(k, _ []byte) error {
			keys = append(keys, string(k))

			return nil
		})
	})
	So(err, ShouldBeNil)

	return keys
}

// archivedJobSummaries maps each job's Key to a compact fingerprint of the
// fields that must survive a compaction round-trip, so two sets compare directly.
func archivedJobSummaries(jobs []*Job) map[string]string {
	summaries := make(map[string]string, len(jobs))
	for _, job := range jobs {
		summaries[job.Key()] = fmt.Sprintf("%s|%s|%v|%d",
			job.Cmd, job.RepGroup, job.State, job.EndTime.UnixNano())
	}

	return summaries
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

// wantEndTimeBytes builds the 8-byte big-endian end-time prefix the index uses
// for tm, clamping a non-positive time to all-zero bytes (mirroring the
// production endTimeToBytes guard). Computed independently of the production
// helper so the tests still verify the production encoding rather than tautology.
func wantEndTimeBytes(tm time.Time) []byte {
	b := make([]byte, endTimeBytes)
	if nanos := tm.UnixNano(); nanos > 0 {
		binary.BigEndian.PutUint64(b, uint64(nanos))
	}

	return b
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

func TestDBReverseLookupRebuildOrder(t *testing.T) {
	Convey("Reverse lookup rebuild prepares destination-key ordered writes", t, func() {
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

		jobKeyA := []byte("0000000000000000000000000000000a")
		jobKeyB := []byte("8000000000000000000000000000000b")
		jobKeyC := []byte("ffffffffffffffffffffffffffffffff")

		var (
			entries   reverseLookupEntries
			processed int
		)

		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			if errd := replaceLookupRebuildTestBucket(tx, bucketRTK, []byte("rg-z"+dbDelimiter+string(jobKeyC))); errd != nil {
				return errd
			}

			if errd := replaceLookupRebuildTestBucket(tx, bucketDTK, []byte("dg-z"+dbDelimiter+string(jobKeyB))); errd != nil {
				return errd
			}

			if errd := replaceLookupRebuildTestBucket(tx, bucketRDTK, []byte("rdg-z"+dbDelimiter+string(jobKeyA))); errd != nil {
				return errd
			}

			entries, processed, err = collectReverseLookupRebuildEntries(tx, nil)

			return err
		})
		So(err, ShouldBeNil)
		So(processed, ShouldEqual, 3)
		So(entries, ShouldHaveLength, 3)

		for i := 1; i < len(entries); i++ {
			So(bytes.Compare(entries[i-1], entries[i]), ShouldBeLessThanOrEqualTo, 0)
		}

		So(bytes.HasPrefix(entries[0], reverseLookupEntryPrefix(jobKeyA)), ShouldBeTrue)
		So(bytes.HasPrefix(entries[1], reverseLookupEntryPrefix(jobKeyB)), ShouldBeTrue)
		So(bytes.HasPrefix(entries[2], reverseLookupEntryPrefix(jobKeyC)), ShouldBeTrue)
	})

	Convey("Duplicate reverse lookup rebuild entries are compacted before write", t, func() {
		entries := reverseLookupEntries{
			[]byte("a"),
			[]byte("a"),
			[]byte("b"),
			[]byte("b"),
			[]byte("c"),
		}

		entries = compactSortedReverseLookupEntries(entries)

		So(entries, ShouldResemble, reverseLookupEntries{
			[]byte("a"),
			[]byte("b"),
			[]byte("c"),
		})
	})
}

func TestDBReverseLookupRebuildProgress(t *testing.T) {
	Convey("Reverse lookup rebuild progress reports cumulative source entries without fake totals", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")

		testDB, _, err := initDB(
			ctx,
			dbFile,
			filepath.Join(tmpdir, "queue.db.bak"),
			internal.Development,
			false,
			false,
		)
		So(err, ShouldBeNil)

		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		logs := clog.ToBufferAtLevel("info")

		defer clog.ToDefault()

		progress := newDBUpgradeReporter(ctx, dbFile)
		defer progress.finish(nil)

		progress.startPhase("rebuild job lookup index", "rebuilding database job lookup index")

		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			keys := make([][]byte, dbUpgradeProgressEntries)
			for i := range dbUpgradeProgressEntries {
				jobKey := fmt.Sprintf("%032d", i)
				keys[i] = fmt.Appendf(nil, "rg-%05d%s%s", i, dbDelimiter, jobKey)
			}

			if errd := replaceLookupRebuildTestBucket(tx, bucketRTK, keys...); errd != nil {
				return errd
			}

			for i := range dbUpgradeProgressEntries {
				jobKey := fmt.Sprintf("%032d", dbUpgradeProgressEntries+i)
				keys[i] = fmt.Appendf(nil, "dg-%05d%s%s", i, dbDelimiter, jobKey)
			}

			if errd := replaceLookupRebuildTestBucket(tx, bucketDTK, keys...); errd != nil {
				return errd
			}

			return rebuildJobLookupEntries(tx, progress)
		})
		So(err, ShouldBeNil)

		output := logs.String()
		So(output, ShouldContainSubstring,
			"rebuilding database job lookup index (10000 source entries processed so far; currently reading repgroupToKey)")
		So(output, ShouldContainSubstring,
			"rebuilding database job lookup index (20000 source entries processed so far; currently reading depgroupToKey)")
		So(output, ShouldNotContainSubstring, "10000 total")
		So(output, ShouldNotContainSubstring, "20000 total")
		So(output, ShouldNotContainSubstring, "entries processed, 10000 total")
	})
}

func replaceLookupRebuildTestBucket(tx *bolt.Tx, bucket []byte, keys ...[]byte) error {
	if tx.Bucket(bucket) != nil {
		if err := tx.DeleteBucket(bucket); err != nil {
			return err
		}
	}

	b, err := tx.CreateBucket(bucket)
	if err != nil {
		return err
	}

	for _, key := range keys {
		if err = b.Put(key, nil); err != nil {
			return err
		}
	}

	return nil
}
