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
	"path/filepath"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	// dgRetiredGroup is the dep group the retired-index tests put their parent
	// job in, and their child job's dependency names.
	dgRetiredGroup = "retired-index-dep-group"
)

// TestDepGroupRetiredIndex covers the retirement of bucketDTK: it stops being
// written, but is left in place so that a database written before the change
// still reads, still rebuilds bucketDepGroups if that is ever lost, and still
// lets a key-changing modify tidy up its pre-upgrade entries.
func TestDepGroupRetiredIndex(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Storing a job in a dep group no longer writes the retired dep group index", t, func() {
		testDB := dgTestDB(t, ctx)
		parent, child := dgStoreRetiredPair(ctx, testDB)

		err := testDB.bolt.View(func(tx *bolt.Tx) error {
			So(dgLookupKeys(tx, bucketDTK, dgRetiredGroup), ShouldHaveLength, 0)
			So(tx.Bucket(bucketDepGroups).Get([]byte(dgRetiredGroup)), ShouldNotBeNil)

			// the reverse dep group index, which the add path reads to resurrect
			// waiters, is unchanged in shape.
			So(dgLookupKeys(tx, bucketRDTK, dgRetiredGroup), ShouldResemble,
				[]string{dgRetiredGroup + dbDelimiter + child.Key()})

			return nil
		})
		So(err, ShouldBeNil)
		So(parent.Key(), ShouldNotEqual, child.Key())
	})

	Convey("A database holding pre-upgrade dep group entries opens unchanged and still resolves", t, func() {
		testDB, parent, child, legacyKey := dgReopenedLegacyDB(t, ctx, false)

		So(testDB.upgradedOnOpen(), ShouldBeFalse)

		err := testDB.bolt.View(func(tx *bolt.Tx) error {
			So(dgLookupKeys(tx, bucketDTK, dgRetiredGroup), ShouldResemble, []string{legacyKey})

			return nil
		})
		So(err, ShouldBeNil)

		groups := newDepGroupMembers()
		groups.add(parent.DepGroups, parent.Key())

		deps, waiting, err := child.Dependencies.dependencyKeys(testDB, groups)
		So(err, ShouldBeNil)
		So(deps, ShouldResemble, []string{depGroupDependencyKey(dgRetiredGroup)})
		So(waiting, ShouldResemble, []string{})

		deps, waiting, err = parent.Dependencies.dependencyKeys(testDB, groups)
		So(err, ShouldBeNil)
		So(deps, ShouldResemble, []string{})
		So(waiting, ShouldResemble, []string{})
	})

	Convey("A pre-upgrade dep group entry does not break a key-changing modify", t, func() {
		testDB, parent, _, _ := dgReopenedLegacyDB(t, ctx, true)

		oldKey := parent.Key()

		err := testDB.bolt.View(func(tx *bolt.Tx) error {
			So(countReverseLookupEntriesByJobKey(tx, oldKey), ShouldEqual, 2)

			return nil
		})
		So(err, ShouldBeNil)

		modified := testDBJob("echo retired parent modified", "retired-parent-modified")
		modified.DepGroups = []string{dgRetiredGroup}

		// deleting bucketDTK rather than leaving it unwritten is what would make
		// this return ErrBucketNotFound, on every job that predates the change.
		So(testDB.modifyLiveJobs(ctx, []string{oldKey}, []*Job{modified}), ShouldBeNil)

		err = testDB.bolt.View(func(tx *bolt.Tx) error {
			So(countReverseLookupEntriesByJobKey(tx, oldKey), ShouldEqual, 0)
			So(countLookupEntriesByJobKey(tx, oldKey), ShouldEqual, 0)

			return nil
		})
		So(err, ShouldBeNil)
	})
}

// dgLookupKeys returns the keys in the given lookup bucket that belong to the
// given lookup prefix.
func dgLookupKeys(tx *bolt.Tx, bucket []byte, prefix string) []string {
	b := tx.Bucket(bucket)
	So(b, ShouldNotBeNil)

	seek := []byte(prefix + dbDelimiter)
	keys := make([]string, 0, b.Stats().KeyN)

	c := b.Cursor()
	for k, _ := c.Seek(seek); bytes.HasPrefix(k, seek); k, _ = c.Next() {
		keys = append(keys, string(k))
	}

	return keys
}

// dgReopenedLegacyDB builds a database the pre-change code could have written -
// a parent job in a dep group, a child depending on it, and the parent's
// bucketDTK membership entry, optionally with the reverse lookup entry that
// names that bucket - then closes it and hands back the reopened database, its
// two jobs and the legacy entry's key.
func dgReopenedLegacyDB(t *testing.T, ctx context.Context, withReverse bool) (*db, *Job, *Job, string) {
	t.Helper()

	tmpdir := t.TempDir()
	dbFile := filepath.Join(tmpdir, "queue.db")
	dbBackup := filepath.Join(tmpdir, "queue.db.bak")

	written, _, err := initDB(ctx, dbFile, dbBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	parent, child := dgStoreRetiredPair(ctx, written)
	legacyKey := dgWriteLegacyDepGroupEntry(written, parent, withReverse)
	So(written.close(ctx), ShouldBeNil)

	return dgOpenDB(t, ctx, dbFile, dbBackup), parent, child, legacyKey
}

// dgStoreRetiredPair stores a parent job carrying dgRetiredGroup and a child job
// depending on it.
func dgStoreRetiredPair(ctx context.Context, testDB *db) (*Job, *Job) {
	parent := testDBJob("echo retired parent", "retired-parent")
	parent.DepGroups = []string{dgRetiredGroup}
	child := testDBJob("echo retired child", "retired-child")
	child.Dependencies = Dependencies{NewDepGroupDependency(dgRetiredGroup)}

	queued, _, _, err := testDB.storeNewJobs(ctx, []*Job{parent, child}, false)
	So(err, ShouldBeNil)
	So(queued, ShouldHaveLength, 2)

	return parent, child
}

// dgWriteLegacyDepGroupEntry writes the bucketDTK entry the pre-change code
// wrote for job's dep group membership, optionally with the reverse lookup entry
// that names that bucket, and returns the entry's key. Those entries are
// pre-upgrade data by definition, so writing them directly is the truer fixture.
func dgWriteLegacyDepGroupEntry(testDB *db, job *Job, withReverse bool) string {
	jobKey := []byte(job.Key())
	lookupKey := testDB.generateLookupKey(dgRetiredGroup, jobKey)

	err := testDB.bolt.Update(func(tx *bolt.Tx) error {
		// a database that predates the retirement has the bucket, whatever a
		// later binary would do with it.
		b, errb := tx.CreateBucketIfNotExists(bucketDTK)
		if errb != nil {
			return errb
		}

		if errp := b.Put(lookupKey, nil); errp != nil {
			return errp
		}

		if !withReverse {
			return nil
		}

		return tx.Bucket(bucketJobLookupEntries).Put(
			reverseLookupEntryKey(jobKey, bucketDTK, lookupKey), nil)
	})
	So(err, ShouldBeNil)

	return string(lookupKey)
}
