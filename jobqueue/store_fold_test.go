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

// Spec D1's tests: history queries on the log store answer from history.bolt
// plus the overlay of completions not yet folded into it.

import (
	"context"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// historyTestRepGroup is the rep group D1's tests archive a job of.
	historyTestRepGroup = "rg"

	// historyUnarchivedRepGroup is a rep group whose jobs are added but never
	// archived.
	historyUnarchivedRepGroup = "unarchived"
)

// allCompleteGroup is the one group allCompleteJobsQuery puts every job in.
const allCompleteGroup = "all"

// historyArchiver is the archive method of every store.
type historyArchiver interface {
	archiveCompletion(key string, job *Job) (archiveOutcome, error)
}

// archiveTestJob archives job, as completed successfully between start and
// end, in s, asserting the archive removed it from the live plane or kept it
// live as marked.
func archiveTestJob(s historyArchiver, job *Job, start, end time.Time) {
	job.Lock()
	job.State = JobStateComplete
	job.Exited = true
	job.Exitcode = 0
	job.StartTime = start
	job.EndTime = end
	job.PeakRAM = 42
	kept := job.RerunAfterRun
	job.Unlock()

	outcome, err := s.archiveCompletion(job.Key(), job)
	So(err, ShouldBeNil)

	if kept {
		So(outcome, ShouldEqual, archiveKeptLive)
	} else {
		So(outcome, ShouldEqual, archiveRemovedLive)
	}
}

// historyQuerier is the history queries of spec D1's acceptance test 1, which
// every store answers.
type historyQuerier interface {
	checkIfComplete(key string) (bool, error)
	retrieveCompleteJobsByKeys(keys []string) ([]*Job, error)
	retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error)
	retrieveOldestCompleteJobsByRepGroup(repgroup string, query completeJobsQuery) (*completeJobsPage, error)
	retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error)
	retrieveLastCompletionTimeByRepGroup(repGroups []string) (map[string]time.Time, error)
	retrieveCompleteJobStatusByRepGroup(repgroup string, includeDetails bool) (*RepGroupStatus, error)
	repGroupHasHistory(repGroup string) bool
	retrieveRepGroups() ([]string, error)
}

// checkArchivedAnswers asserts D1 acceptance test 1's nine answers for job,
// the only job of historyTestRepGroup archived and not live, which ended at
// end, after before.
func checkArchivedAnswers(st historyQuerier, job *Job, before, end time.Time) {
	key := job.Key()

	complete, err := st.checkIfComplete(key)
	So(err, ShouldBeNil)
	So(complete, ShouldBeTrue)

	byKeys, err := st.retrieveCompleteJobsByKeys([]string{key})
	So(err, ShouldBeNil)
	So(jobKeys(byKeys), ShouldResemble, []string{key})
	So(byKeys[0].Cmd, ShouldEqual, job.Cmd)
	So(byKeys[0].State, ShouldEqual, JobStateComplete)

	byRG, err := st.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
	So(err, ShouldBeNil)
	So(jobKeys(byRG), ShouldResemble, []string{key})

	page, err := st.retrieveOldestCompleteJobsByRepGroup(historyTestRepGroup, allCompleteJobsQuery(10))
	So(err, ShouldBeNil)
	So(jobKeys(page.jobs), ShouldResemble, []string{key})
	So(page.counted, ShouldResemble, map[string]int{allCompleteGroup: 1})
	So(page.fetched, ShouldResemble, map[string]int{allCompleteGroup: 1})

	recent, err := st.retrieveCompleteJobsRecent(before)
	So(jobKeys(recent), ShouldContain, key)
	So(err, ShouldBeNil)

	times, err := st.retrieveLastCompletionTimeByRepGroup([]string{historyTestRepGroup, "never"})
	So(err, ShouldBeNil)
	So(times, ShouldResemble, map[string]time.Time{historyTestRepGroup: end.UTC()})

	status, err := st.retrieveCompleteJobStatusByRepGroup(historyTestRepGroup, false)
	So(err, ShouldBeNil)
	So(status.Counts[JobStateComplete], ShouldEqual, 1)

	So(st.repGroupHasHistory(historyTestRepGroup), ShouldBeTrue)
	So(st.repGroupHasHistory("never"), ShouldBeFalse)

	rgs, err := st.retrieveRepGroups()
	So(err, ShouldBeNil)
	So(rgs, ShouldContain, historyTestRepGroup)
}

// allCompleteJobsQuery is a completeJobsQuery keeping every job in one group,
// of which it can use limit.
func allCompleteJobsQuery(limit int) completeJobsQuery {
	return completeJobsQuery{
		group: func(*archivedJobFacets) (string, bool) { return allCompleteGroup, true },
		limit: func(string) int { return limit },
	}
}

func TestLogStoreHistoryOverlay(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with no folder running and rep group rg never seen before", t, func() {
		s, _ := openTestLogStore(t, ctx)

		before := time.Now().Add(-time.Minute)
		job := testDBJob("echo history overlay", historyTestRepGroup)
		other := testDBJob("echo history overlay other", historyUnarchivedRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job, other}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second).Add(-time.Second)
		archiveTestJob(s, job, end.Add(-time.Minute), end)

		Convey("when a job of rg is archived, every history query answers with it, before and after reopen", func() {
			checkArchivedAnswers(s, job, before, end)
			checkArchivedAnswers(reopenLogStore(ctx, s), job, before, end)
		})

		Convey("a job of rg archived but kept live is complete but omitted where today's queries skip live keys", func() {
			kept := testDBJob("echo history overlay kept", historyTestRepGroup)
			_, err = s.storeNewJobsGuarded(ctx, []*Job{kept}, false, nil)
			So(err, ShouldBeNil)

			kept.RerunAfterRun = true
			archiveTestJob(s, kept, end.Add(-time.Hour), end.Add(time.Second))

			check := func(st *logStore) {
				live, errl := st.checkIfLive(kept.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)

				checkArchivedAnswers(st, job, before, end.Add(time.Second))

				complete, errc := st.checkIfComplete(kept.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeTrue)

				byKeys, errk := st.retrieveCompleteJobsByKeys([]string{kept.Key()})
				So(errk, ShouldBeNil)
				So(jobKeys(byKeys), ShouldResemble, []string{kept.Key()})
			}

			check(s)
			check(reopenLogStore(ctx, s))
		})

		Convey("rep groups with no completion have no history", func() {
			So(s.repGroupHasHistory(historyUnarchivedRepGroup), ShouldBeFalse)

			times, errt := s.retrieveLastCompletionTimeByRepGroup([]string{historyUnarchivedRepGroup})
			So(errt, ShouldBeNil)
			So(times, ShouldBeEmpty)

			byRG, errr := s.retrieveCompleteJobsByRepGroup(historyUnarchivedRepGroup)
			So(errr, ShouldBeNil)
			So(byRG, ShouldBeEmpty)

			recent, errn := s.retrieveCompleteJobsRecent(end.Add(time.Second))
			So(errn, ShouldBeNil)
			So(recent, ShouldBeEmpty)
		})
	})
}

func TestLogStoreOverlayReducer(t *testing.T) {
	Convey("Given a key archived and then deleted, the reducer's overlay", t, func() {
		st := newLogState()
		k := logKey{1}
		first, second := time.Unix(100, 0), time.Unix(200, 0)
		spec := wal.Ref{File: 1, Off: 1}

		for _, op := range []logOp{
			{typ: logRecSpec, seq: 1, key: k, ref: spec, index: logSpecIndex{repGroup: "a"}},
			{typ: logRecComplete, seq: 2, key: k, ref: wal.Ref{File: 1, Off: 2}, endTime: first},
			{typ: logRecDelete, seq: 3, key: k},
		} {
			st.apply(&op)
		}

		Convey("takes a later complete record of it with the spec it had, as an archive racing the delete makes", func() {
			later := wal.Ref{File: 1, Off: 4}
			st.apply(&logOp{typ: logRecComplete, seq: 4, key: k, ref: later, endTime: second})

			So(st.history[k], ShouldEqual, 4)
			So(*st.overlay[k], ShouldResemble, logHistory{seq: 4, complete: later, spec: spec, repGroup: "a",
				endTime: second})
			So(st.overlayEnds, ShouldResemble, map[string]int64{"a": 200})
		})

		Convey("ignores an earlier complete record of it", func() {
			st.apply(&logOp{typ: logRecComplete, seq: 2, key: k, ref: wal.Ref{File: 1, Off: 9}, endTime: second})

			So(st.history[k], ShouldEqual, 2)
			So(st.overlay[k].complete, ShouldResemble, wal.Ref{File: 1, Off: 2})
			So(st.overlayEnds, ShouldResemble, map[string]int64{"a": 100})
		})

		Convey("drops a complete record of a key neither live nor in it", func() {
			other := logKey{2}
			st.apply(&logOp{typ: logRecComplete, seq: 5, key: other, ref: wal.Ref{File: 1, Off: 5}, endTime: second})

			So(st.history, ShouldNotContainKey, other)
			So(st.overlay, ShouldNotContainKey, other)
		})
	})
}
