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

package cmd

// CLI-level regression tests for reliable4 FINDING 1: `wr suspend` and
// `wr resume` must not ask the manager for the archived history of the report
// group(s) they are selecting from. A complete job can never be suspended or
// resumed, so every archived job the manager decodes for these two commands is
// work that is 100% discarded - on the production DB that scan cost minutes of
// CPU and took the manager's heap from 0.35GB to 12.1GB, and the operator could
// not un-suspend the queue at all.
//
// The observable proxy for "did the manager scan the history?" is the
// "(out of N matching)" count: complete jobs are only in that count if the CLI
// asked the server for them. Pre-fix these tests reported the archived jobs too;
// the jobqueue package's TestReliable4ControlPathsSkipArchivedHistory pins the
// same fix server-side with a decode counter.

import (
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	historySelectionRepGroup      = "rg-history-selection"
	historySelectionOtherRepGroup = "rg-history-selection-other"
	historySelectionSubStr        = "history-selection"
	historySelectionArchived      = 4
)

func TestSelectedJobsSkipArchivedHistory(t *testing.T) {
	Convey("wr suspend -i selects from live jobs only, ignoring archived history", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			archiveHistoryJobs(jq, reqs, historySelectionRepGroup)

			ready := newQueueCommandJob("echo history live ready", historySelectionRepGroup, reqs)
			addQueueCommandJobs(jq, ready)

			output, err := runSuspendForTest(t, "-i", historySelectionRepGroup)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, ready), ShouldEqual, jobqueue.JobStateSuspended)

			Convey("and wr status -i still shows the archived history", func() {
				assertStatusPlainStateCount(t, jobqueue.JobStateComplete, historySelectionArchived,
					"-i", historySelectionRepGroup, "-o", "plain")
				assertStatusPlainStateCount(t, jobqueue.JobStateSuspended, 1,
					"-i", historySelectionRepGroup, "-o", "plain")
			})
		})
	})

	Convey("wr resume -i selects from live jobs only, ignoring archived history", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			archiveHistoryJobs(jq, reqs, historySelectionRepGroup)

			suspended := newQueueCommandJob("echo history live suspended", historySelectionRepGroup, reqs)
			addQueueCommandJobs(jq, suspended)
			suspendQueueCommandJobs(jq, suspended)

			output, err := runResumeForTest(t, "-i", historySelectionRepGroup)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 1 suspended commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, suspended), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr suspend -i -z selects from live jobs only across matching groups", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			archiveHistoryJobs(jq, reqs, historySelectionRepGroup)
			archiveHistoryJobs(jq, reqs, historySelectionOtherRepGroup)

			one := newQueueCommandJob("echo history sub one", historySelectionRepGroup, reqs)
			two := newQueueCommandJob("echo history sub two", historySelectionOtherRepGroup, reqs)
			addQueueCommandJobs(jq, one, two)

			output, err := runSuspendForTest(t, "-i", historySelectionSubStr, "-z")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 2 queued commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("wr suspend/resume -i report a history-only report group as no match", t, func() {
		// DELIBERATE user-visible change: because these commands no longer ask for the
		// archived history, a report group whose jobs have ALL finished now selects
		// nothing, so both commands die with "no matching jobs found" (exit 1) instead of
		// exiting 0 having printed "Suspended 0 queued commands (out of 4 matching)".
		// That cannot be avoided without reintroducing the unbounded scan, and it makes
		// them consistent with `wr remove -i`, which already dies (its JobStateDeletable
		// filter excludes complete jobs too). Pinned so the change stays deliberate.
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			archiveHistoryJobs(jq, reqs, historySelectionRepGroup)

			output, err := runSuspendForTest(t, "-i", historySelectionRepGroup)
			So(output, ShouldBeEmpty)
			So(err, ShouldEqual, errSelectedJobsNoMatch)

			output, err = runResumeForTest(t, "-i", historySelectionRepGroup)
			So(output, ShouldBeEmpty)
			So(err, ShouldEqual, errSelectedJobsNoMatch)

			Convey("and wr status -i still reports that history", func() {
				assertStatusPlainStateCount(t, jobqueue.JobStateComplete, historySelectionArchived,
					"-i", historySelectionRepGroup, "-o", "plain")
			})
		})
	})

	Convey("wr suspend still counts live but ineligible jobs as matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			archiveHistoryJobs(jq, reqs, historySelectionRepGroup)

			buried := newQueueCommandJob("echo history live buried", historySelectionRepGroup, reqs)
			addQueueCommandJobs(jq, buried)
			buryQueueCommandJob(jq, buried)

			output, err := runSuspendForTest(t, "-i", historySelectionRepGroup)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 0 queued commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, buried), ShouldEqual, jobqueue.JobStateBuried)
		})
	})
}

// archiveHistoryJobs adds historySelectionArchived jobs in repGroup and completes
// them all the real way, so repGroup has an archived history in the database that
// only the database scan can see.
func archiveHistoryJobs(jq *jobqueue.Client, reqs *jqs.Requirements, repGroup string) {
	jobs := make([]*jobqueue.Job, 0, historySelectionArchived)
	for i := range historySelectionArchived {
		jobs = append(jobs, newQueueCommandJob(
			"echo history "+repGroup+" "+strconv.Itoa(i), repGroup, reqs,
		))
	}

	addQueueCommandJobs(jq, jobs...)

	for range historySelectionArchived {
		archiveNextStatusJob(jq, time.Now())
	}
}
