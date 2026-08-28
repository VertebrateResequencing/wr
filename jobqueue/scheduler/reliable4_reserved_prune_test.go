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

package scheduler

// This is the .docs/bugfixes/260827-2.md item 6 regression test. wr remembers
// which LSF array elements it has handed a job reservation to so that
// killExcessCmds never bkills one of them (DEVELOPERS.md rule 5), and countCmds
// prunes that set against a full `bjobs -w` snapshot to keep it bounded over a
// long-lived manager. It used to prune from the snapshot even when parseBjobs
// had returned an error, so a bjobs that failed - or that bjobsExecTimeout cut
// short mid-list - had wr forget elements LSF still holds and its reservations
// still cover.
//
// Both directions are asserted, since "never prune" would satisfy the first on
// its own while reintroducing the unbounded growth pruning exists to stop.

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// reservedReportedElement is the LSF element id the fake `bjobs -w` list
	// reports (job 9876543, array index 1, as fakeBjobsListBody prints it).
	reservedReportedElement = "9876543[1]"

	// reservedGoneElement is an LSF element id wr holds a reservation for that
	// the fake `bjobs -w` list does not report.
	reservedGoneElement = "9876543[2]"
)

// TestReliable4ReservedPruneOnlyWhenComplete covers 260827-2 item 6: the
// reserved-element set may only be pruned against a bjobs snapshot that is
// complete.
func TestReliable4ReservedPruneOnlyWhenComplete(t *testing.T) {
	Convey("Given an lsf holding reservations for two LSF elements", t, func() {
		dir := t.TempDir()
		s := newFakeLSFScheduler(t, dir, filepath.Join(dir, "jargs"), fakeLSFDelays{bjobsListJobs: 1})

		s.reserved(reservedReportedElement)
		s.reserved(reservedGoneElement)

		ctx, _ := captureLogCtx()

		Convey("a full scan whose `bjobs -w` failed leaves the reservations intact", func() {
			// a list call that exits non-zero: whatever LSF holds, this snapshot
			// is not a picture of it.
			writeFakeExe(t, s.bjobsExe, "#!/bin/bash\nexit 1\n")

			count, err := s.countCmds(ctx, jobNamePrefix(s.config.Deployment), true)

			So(err, ShouldNotBeNil)
			So(count, ShouldEqual, 0)
			So(s.snapshotReserved(), ShouldResemble, map[string]bool{
				reservedReportedElement: true,
				reservedGoneElement:     true,
			})
		})

		Convey("a full scan whose `bjobs -w` succeeded still prunes what LSF no longer reports", func() {
			count, err := s.countCmds(ctx, jobNamePrefix(s.config.Deployment), true)

			So(err, ShouldBeNil)
			So(count, ShouldEqual, 1)
			So(s.snapshotReserved(), ShouldResemble, map[string]bool{reservedReportedElement: true})
		})
	})
}
