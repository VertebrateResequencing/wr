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
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// TestAlreadyCompleteDuplicateIndexAttribution covers how an already-complete
// duplicate is attributed when bbolt's repgroup->key index disagrees with the
// archived record, which only a hand-edited index can set up.
func TestAlreadyCompleteDuplicateIndexAttribution(t *testing.T) {
	ctx := context.Background()

	const (
		firstRepGroup = "attributionA"
		otherRepGroup = "attributionB"
		doneCmd       = "echo attributed"
	)

	completedAt := time.Date(2026, 8, 28, 9, 30, 0, 0, time.UTC)

	Convey("Given a command that completed under one report group", t, func() {
		testDB := testDBForAttribution(ctx, t)

		seedCompletedJob(ctx, testDB, doneCmd, firstRepGroup, completedAt)

		Convey("A same-report-group duplicate is attributed without reading the archived record", func() {
			// the archived record says the command completed under
			// firstRepGroup, but the index also links its key to
			// otherRepGroup, as it does for a command that was added under
			// both. Adding under otherRepGroup must then report no other report
			// group at all: naming firstRepGroup here could only come from
			// reading the record, which this path must not do.
			So(indexKeyUnderRepGroup(testDB, doneCmd, firstRepGroup, otherRepGroup), ShouldBeNil)

			indexedDups, erri := addedDuplicates(ctx, testDB, []*Job{testDBJob(doneCmd, otherRepGroup)})
			So(erri, ShouldBeNil)
			So(indexedDups, ShouldResemble, DuplicateBreakdown{Complete: 1, CompleteSameRepGroup: 1})
			So(indexedDups.OtherRepGroups, ShouldBeEmpty)
		})

		Convey("A duplicate with no index entry is attributed by the record's own report group", func() {
			// a database old enough, or built by hand, to have no
			// repgroup->key entry for the record. Re-adding under the report
			// group the record itself names must not report that report group
			// as another one: the operator just typed it.
			So(forgetIndexKey(testDB, doneCmd, firstRepGroup), ShouldBeNil)

			unindexedDups, erru := addedDuplicates(ctx, testDB,
				[]*Job{testDBJob(doneCmd, firstRepGroup)})
			So(erru, ShouldBeNil)
			So(unindexedDups, ShouldResemble, DuplicateBreakdown{Complete: 1, CompleteSameRepGroup: 1})
		})
	})
}

// testDBForAttribution returns a new empty database that is closed when the
// test finishes.
func testDBForAttribution(ctx context.Context, t *testing.T) *db {
	t.Helper()

	tmpdir := t.TempDir()

	testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
	So(err, ShouldBeNil)

	Reset(func() {
		So(testDB.close(ctx), ShouldBeNil)
	})

	return testDB
}

// indexKeyUnderRepGroup adds the repGroup->key index entry that storing cmd as a
// job of repGroup would have added, without storing or archiving any record for
// it.
func indexKeyUnderRepGroup(testDB *db, cmd, jobRepGroup, repGroup string) error {
	key := []byte(testDBJob(cmd, jobRepGroup).Key())

	return testDB.bolt.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRTK).Put(testDB.generateLookupKey(repGroup, key), nil)
	})
}

// forgetIndexKey removes the repGroup->key index entry for cmd stored as a job
// of repGroup, leaving its archived record in place, as a database predating
// that index has it.
func forgetIndexKey(testDB *db, cmd, repGroup string) error {
	key := []byte(testDBJob(cmd, repGroup).Key())

	return testDB.bolt.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRTK).Delete(testDB.generateLookupKey(repGroup, key))
	})
}
