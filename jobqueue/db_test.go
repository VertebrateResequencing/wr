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
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

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
