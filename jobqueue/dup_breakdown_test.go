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
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

func TestAlreadyCompleteDuplicateAttribution(t *testing.T) {
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

		Convey("Adding it again under that same report group is attributed to it", func() {
			_, _, sameDups, errs := testDB.storeNewJobs(ctx, []*Job{testDBJob(doneCmd, firstRepGroup)}, true)
			So(errs, ShouldBeNil)
			So(sameDups.Total(), ShouldEqual, 1)
			So(sameDups.Complete, ShouldEqual, 1)
			So(sameDups.CompleteSameRepGroup, ShouldEqual, 1)
			So(sameDups.CompleteOtherRepGroups(), ShouldEqual, 0)
			So(sameDups.OtherRepGroups, ShouldBeEmpty)
		})

		Convey("Adding it again under a different report group names the one it completed under", func() {
			_, _, otherDups, erro := testDB.storeNewJobs(ctx, []*Job{testDBJob(doneCmd, otherRepGroup)}, true)
			So(erro, ShouldBeNil)
			So(otherDups.Complete, ShouldEqual, 1)
			So(otherDups.CompleteSameRepGroup, ShouldEqual, 0)
			So(otherDups.CompleteOtherRepGroups(), ShouldEqual, 1)
			So(dupRepGroupSummaries(otherDups.OtherRepGroups), ShouldResemble,
				[]string{firstRepGroup + " x1 completed 2026-08-28"})
		})

		Convey("Each input job of one add is attributed by its own report group", func() {
			_, _, mixedDups, errm := testDB.storeNewJobs(ctx, []*Job{
				testDBJob(doneCmd, firstRepGroup),
				testDBJob(doneCmd, otherRepGroup),
			}, true)
			So(errm, ShouldBeNil)
			So(mixedDups.Complete, ShouldEqual, 2)
			So(mixedDups.CompleteSameRepGroup, ShouldEqual, 1)
			So(dupRepGroupSummaries(mixedDups.OtherRepGroups), ShouldResemble,
				[]string{firstRepGroup + " x1 completed 2026-08-28"})
		})

		Convey("A command repeated within one add counts once per input job", func() {
			_, _, repeatDups, errr := testDB.storeNewJobs(ctx, []*Job{
				testDBJob(doneCmd, firstRepGroup),
				testDBJob(doneCmd, firstRepGroup),
			}, true)
			So(errr, ShouldBeNil)
			So(repeatDups, ShouldResemble, DuplicateBreakdown{Complete: 2, CompleteSameRepGroup: 2})
			So(repeatDups.Total(), ShouldEqual, 2)
		})

		Convey("A command that never completed is not a duplicate", func() {
			_, _, freshDups, errf := testDB.storeNewJobs(ctx, []*Job{testDBJob("echo fresh", firstRepGroup)}, true)
			So(errf, ShouldBeNil)
			So(freshDups, ShouldResemble, DuplicateBreakdown{})
		})

		Convey("A same-report-group duplicate is attributed without reading the archived record", func() {
			// the archived record says the command completed under
			// firstRepGroup, but the index also links its key to
			// otherRepGroup, as it does for a command that was added under
			// both. Adding under otherRepGroup must then report no other report
			// group at all: naming firstRepGroup here could only come from
			// reading the record, which this path must not do.
			So(indexKeyUnderRepGroup(testDB, doneCmd, firstRepGroup, otherRepGroup), ShouldBeNil)

			_, _, indexedDups, erri := testDB.storeNewJobs(ctx, []*Job{testDBJob(doneCmd, otherRepGroup)}, true)
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

			_, _, unindexedDups, erru := testDB.storeNewJobs(ctx,
				[]*Job{testDBJob(doneCmd, firstRepGroup)}, true)
			So(erru, ShouldBeNil)
			So(unindexedDups, ShouldResemble, DuplicateBreakdown{Complete: 1, CompleteSameRepGroup: 1})
		})
	})

	Convey("Given commands that completed under 2 other report groups", t, func() {
		testDB := testDBForAttribution(ctx, t)

		const (
			busierRepGroup  = "attributionBusy"
			quieterRepGroup = "attributionQuiet"
			newRepGroup     = "attributionNew"
			busyCmd         = "echo busy 1"
			busierCmd       = "echo busy 2"
			quietCmd        = "echo quiet 1"
		)

		newest := completedAt.Add(48 * time.Hour)

		seedCompletedJob(ctx, testDB, busyCmd, busierRepGroup, completedAt)
		seedCompletedJob(ctx, testDB, busierCmd, busierRepGroup, newest)
		seedCompletedJob(ctx, testDB, quietCmd, quieterRepGroup, completedAt)

		Convey("They are grouped per report group, biggest count first, newest end time each", func() {
			_, _, dups, err := testDB.storeNewJobs(ctx, []*Job{
				testDBJob(quietCmd, newRepGroup),
				testDBJob(busyCmd, newRepGroup),
				testDBJob(busierCmd, newRepGroup),
			}, true)
			So(err, ShouldBeNil)
			So(dups.Complete, ShouldEqual, 3)
			So(dups.CompleteOtherRepGroups(), ShouldEqual, 3)
			So(dupRepGroupSummaries(dups.OtherRepGroups), ShouldResemble, []string{
				busierRepGroup + " x2 completed 2026-08-30",
				quieterRepGroup + " x1 completed 2026-08-28",
			})
		})

		Convey("Equal counts are ordered by report group, so the report is deterministic", func() {
			_, _, dups, err := testDB.storeNewJobs(ctx, []*Job{
				testDBJob(quietCmd, newRepGroup),
				testDBJob(busierCmd, newRepGroup),
			}, true)
			So(err, ShouldBeNil)
			So(dupRepGroupSummaries(dups.OtherRepGroups), ShouldResemble, []string{
				busierRepGroup + " x1 completed 2026-08-30",
				quieterRepGroup + " x1 completed 2026-08-28",
			})
		})
	})

	Convey("Given a command that completed without recording an end time", t, func() {
		testDB := testDBForAttribution(ctx, t)

		seedCompletedJob(ctx, testDB, doneCmd, firstRepGroup, time.Time{})

		Convey("The report group it completed under is named with no end time", func() {
			_, _, dups, err := testDB.storeNewJobs(ctx, []*Job{testDBJob(doneCmd, otherRepGroup)}, true)
			So(err, ShouldBeNil)
			So(dups, ShouldResemble, DuplicateBreakdown{
				Complete:       1,
				OtherRepGroups: []DuplicateRepGroup{{RepGroup: firstRepGroup, Count: 1}},
			})
			So(dups.OtherRepGroups[0].LastCompleted.IsZero(), ShouldBeTrue)
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

// seedCompletedJob stores cmd as a job of repGroup and then archives it as
// having completed at endTime, so that a later add of the same command finds it
// already complete.
func seedCompletedJob(ctx context.Context, testDB *db, cmd, repGroup string, endTime time.Time) {
	job := testDBArchivedJob(cmd, repGroup, endTime)

	_, _, dups, err := testDB.storeNewJobs(ctx, []*Job{job}, true)
	So(err, ShouldBeNil)
	So(dups, ShouldResemble, DuplicateBreakdown{})
	So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
}

// dupRepGroupSummaries renders report groups in the order they would be reported
// in, with the count and completion date an operator would be shown. Stored
// times come back with only their zone offset, not its name, so they cannot be
// compared to a constructed time.Time directly.
func dupRepGroupSummaries(groups []DuplicateRepGroup) []string {
	summaries := make([]string, 0, len(groups))

	for _, group := range groups {
		summary := fmt.Sprintf("%s x%d", group.RepGroup, group.Count)

		if !group.LastCompleted.IsZero() {
			summary += " completed " + group.LastCompleted.Format(time.DateOnly)
		}

		summaries = append(summaries, summary)
	}

	return summaries
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

func TestAddDuplicatesReporting(t *testing.T) {
	breakdown := DuplicateBreakdown{Queued: 1, Complete: 2, CompleteSameRepGroup: 2}

	Convey("Given an add answered by a manager that breaks its duplicates down", t, func() {
		dups := NewAddDuplicates(3, breakdown)

		Convey("Both the total and the breakdown are reported", func() {
			So(dups.Total(), ShouldEqual, 3)

			reported, ok := dups.Breakdown()
			So(ok, ShouldBeTrue)
			So(reported, ShouldResemble, breakdown)
		})
	})

	Convey("Given an add answered by a manager predating the breakdown", t, func() {
		resp := decodePreBreakdownAddResponse(3)

		Convey("Its duplicate total is still reported, and no breakdown is", func() {
			dups := NewAddDuplicates(resp.Existed, resp.Duplicates)
			So(dups.Total(), ShouldEqual, 3)

			_, ok := dups.Breakdown()
			So(ok, ShouldBeFalse)
		})
	})

	Convey("Given an add answered by an old manager that had no duplicates", t, func() {
		resp := decodePreBreakdownAddResponse(0)

		Convey("No breakdown is reported, since that manager sent none", func() {
			dups := NewAddDuplicates(resp.Existed, resp.Duplicates)
			So(dups.Total(), ShouldEqual, 0)

			_, ok := dups.Breakdown()
			So(ok, ShouldBeFalse)
		})
	})

	Convey("Given an add whose breakdown does not account for its total", t, func() {
		dups := NewAddDuplicates(3, DuplicateBreakdown{Complete: 2})

		Convey("The manager's own total is reported, and no breakdown is", func() {
			So(dups.Total(), ShouldEqual, 3)

			_, ok := dups.Breakdown()
			So(ok, ShouldBeFalse)
		})
	})
}

// decodePreBreakdownAddResponse returns the serverResponse a current client
// decodes when a manager from before duplicates were broken down answers its
// add with the given number of duplicates. There is no version handshake, so
// what makes that manager old is simply that its response has no Duplicates in
// it at all.
func decodePreBreakdownAddResponse(existed int) *serverResponse {
	type preBreakdownResponse struct {
		Err         string
		Added       int
		Existed     int
		AddedIDs    []string
		AddWarnings AddWarnings
	}

	var encoded []byte

	ch := new(codec.BincHandle)
	So(codec.NewEncoderBytes(&encoded, ch).Encode(&preBreakdownResponse{Existed: existed}), ShouldBeNil)

	resp := &serverResponse{}
	So(codec.NewDecoderBytes(encoded, ch).Decode(resp), ShouldBeNil)
	So(resp.Existed, ShouldEqual, existed)
	So(resp.Duplicates, ShouldResemble, DuplicateBreakdown{})

	return resp
}
