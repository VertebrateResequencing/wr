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
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
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

// foldLoadHangTimeout bounds the wait for a learned-requirements load made
// while a fold's transaction is open, which must not wait for it.
const foldLoadHangTimeout = 10 * time.Second

// foldTestWait is how long a test waits for the folder goroutine to fold
// unprompted: a few of its intervals, as it folds at least once in each.
const foldTestWait = 5 * foldInterval

// foldUnpromptedLimit is the most time TestStoreFoldUnprompted allows the
// folder to fold an archive in: the spec's second between folds, and as long
// again for the fold, fixed, so that it pins the spec's interval rather than
// whatever foldInterval is.
const foldUnpromptedLimit = 2 * time.Second

// foldTestReqGroup is the ReqGroup of the jobs whose samples D1's tests fold.
const foldTestReqGroup = "q"

// foldTestSecRound and foldTestMBRound are D1 acceptance test 3's rounding of
// learned times and RAM, chosen so that neither rounds the other's samples to
// the same value.
const (
	foldTestSecRound = 7
	foldTestMBRound  = 300
)

const (
	// foldBlockTime is how long D1 acceptance test 5's hook blocks the folder.
	foldBlockTime = 2 * time.Second

	// foldBlockedArchives is how many archives D1 acceptance test 5 makes while
	// the folder is blocked, and foldArchiveLimit the most each may take.
	foldBlockedArchives = 100
	foldArchiveLimit    = 500 * time.Millisecond
)

// foldWorkloadBase is the time D1 acceptance test 7's workloads run their jobs
// from, and ask for completions since.
var foldWorkloadBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) //nolint:gochecknoglobals

// foldWorkloads are D1 acceptance test 7's scripted workloads, each making its
// steps in s with foldWorkloadProbe after each.
var foldWorkloads = []struct { //nolint:gochecknoglobals
	name string
	run  func(ctx context.Context, s store)
}{
	{"archive then storeLiveForRerun", foldWorkloadPutBack},
	{"a keep-live archive", foldWorkloadKeepLive},
	{"archive, resurrection by a new member and archive again", foldWorkloadResurrect},
	{"archive, put back, deleteLiveJobs and re-add", foldWorkloadDeleteReadd},
	{"archive and re-add under another rep group", foldWorkloadReaddElsewhere},
}

var _ store = (*logReplayStore)(nil)

// errFoldTestFailure is the error TestStoreFoldFailureRetried's hook fails
// folds with.
var errFoldTestFailure = errors.New("injected fold failure")

// foldTestFailures is how many folds TestStoreFoldFailureRetried fails.
const foldTestFailures = 3

const (
	// foldTestChunkRecords is TestStoreFoldChunks' chunk size, and
	// foldTestChunkedJobs how many archives it folds, each 2 records.
	foldTestChunkRecords = 10
	foldTestChunkedJobs  = 25
)

// foldSpecReqGroups are the req groups TestStoreFoldSpecPrecedence's job is
// added under, in turn.
var foldSpecReqGroups = []string{"specq1", "specq2", "specq3"} //nolint:gochecknoglobals

// foldModifyReqGroup is the req group TestStoreFoldSpecPrecedence modifies its
// job into.
const foldModifyReqGroup = "specmq"

// foldReplacedSamples is how many learned samples
// TestStoreFoldReplacedAfterFold's two archives leave: a RAM and a run-time
// sample each.
const foldReplacedSamples = 4

const (
	// foldRacedArchives is how many jobs TestStoreFoldWhileArchiving
	// archives, and foldRacedWriters how many writers archive them.
	foldRacedArchives = 400
	foldRacedWriters  = 16
)

const (
	// foldManyCompletes is how many jobs TestStoreFoldManyCompletesEachKeyTwice
	// archives in one fold: more than the 12 a sort orders by insertion,
	// which keeps equal keys in order whether or not the sort is stable.
	foldManyCompletes = 20

	// foldManyCompletesSamples is how many learned samples
	// TestStoreFoldManyCompletesEachKeyTwice's archives leave: a RAM sample
	// each, all distinct, a run-time sample of each of their 2 run times, and
	// one disk sample, as none used disk.
	foldManyCompletesSamples = 2*foldManyCompletes + 2 + 1
)

// foldElsewhereRepGroups are the rep groups TestStoreFoldReplacedElsewhere's
// job runs under, in turn, each with the req group of foldSpecReqGroups at
// its index.
var foldElsewhereRepGroups = []string{"foldrg1", "foldrg2", "foldrg3"} //nolint:gochecknoglobals

// foldBatchedJobs is how many jobs TestStoreFoldBatches folds the modifies,
// completions and rglookups of in one transaction: more than foldApplyBatch,
// so that a fold drops each from the in-memory state in more than one batch.
const foldBatchedJobs = foldApplyBatch + foldApplyBatch/8

// foldBatchedRepGroups are the rep groups TestStoreFoldBatches' jobs are
// added under, then modified into.
var foldBatchedRepGroups = []string{"foldbatchedA", "foldbatchedB"} //nolint:gochecknoglobals

// foldLookedUpRepGroup is the rep group of TestStoreFoldLookedUpRepGroups's
// adds.
const foldLookedUpRepGroup = "foldlookedup"

const (
	// foldQueriedJobs is how many jobs of foldQueriedRepGroup
	// TestStoreFoldRepGroupQueriesDuringModifies queries, while it modifies
	// one of them and folds the modify, foldQueriedRounds times.
	foldQueriedJobs     = 8
	foldQueriedRounds   = 64
	foldQueriedRepGroup = "foldqueried"
)

// foldLookedUpPadding is how many jobs TestStoreFoldLookedUpRepGroups adds
// to a wal file before an add of foldLookedUpRepGroup, so that the add is at a
// later offset in it than a later add's in the next file.
const foldLookedUpPadding = 8

func TestStoreFoldIdleNoWriterLine(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given an idle log store reporting every 50ms", t, func() {
		old := archiveFoldReportInterval
		archiveFoldReportInterval = storeStatsTestInterval

		Reset(func() { archiveFoldReportInterval = old })

		logPath, logCtx := managerLogContext(t, ctx)
		s, _ := openTestLogStore(t, logCtx)

		Convey("a fold that only moves the folded position writes no db writer line", func() {
			So(s.foldNow(), ShouldBeNil)

			s.mu.RLock()
			So(s.state.folded.file, ShouldBeGreaterThan, 0)
			s.mu.RUnlock()

			time.Sleep(storeStatsIdleIntervals * storeStatsTestInterval)
			So(s.close(ctx), ShouldBeNil)
			So(logLinesContaining(readLogFile(t, logPath), dbWriterLogMatch), ShouldBeEmpty)
		})
	})
}

func TestLogStoreCloseStopsLearnedLoads(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a background load of learned requirements blocked", t, func() {
		loads := countReqStatLoads()

		Reset(func() { reqStatLoadHook = nil })

		s, _ := openTestLogStore(t, ctx)

		started, release := blockReqStatLoads(loads, foldTestReqGroup)
		defer release()

		s.refreshReqGroupRecsLater(foldTestReqGroup)

		select {
		case <-started:
		case <-time.After(foldTestWait):
			So("the background load never started", ShouldBeBlank)
		}

		Convey("close waits for the load, and none starts once it has closed", func() {
			closed := make(chan error, 1)

			go func() { closed <- s.close(ctx) }()

			select {
			case <-closed:
				So("close returned during the load", ShouldBeBlank)
			case <-time.After(200 * time.Millisecond):
			}

			release()

			select {
			case err := <-closed:
				So(err, ShouldBeNil)
			case <-time.After(foldTestWait):
				So("close did not return once the load finished", ShouldBeBlank)
			}

			s.reqStats.invalidate(foldTestReqGroup)
			loads.reset()

			s.refreshReqGroupRecsLater(foldTestReqGroup)
			s.reqStats.wg.Wait()

			So(loads.count(foldTestReqGroup), ShouldEqual, 0)
		})
	})
}

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

// learnedStepsStore is what D1 acceptance test 3's steps ask of a store.
type learnedStepsStore interface {
	historyArchiver
	storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
		guard *rerunGuard) (storedNewJobs, error)
	updateJobAfterExitDurable(job *Job, stdo, stde []byte, forceStorage bool) error
	updateJobAfterChangeDurable(job *Job) error
	modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error
	refreshReqGroupRecs(reqGroup string) *jqs.Requirements
	reqStatChanges() uint32
	setRecRounding(secRound, mbRound int)
	count(kind recordKind) (int, error)
	flush() error
}

// openBoltStepsStore returns a new bbolt store as a learnedStepsStore.
func openBoltStepsStore(t *testing.T, ctx context.Context) learnedStepsStore {
	t.Helper()

	bdb, _ := openTestStore(t, ctx, storeKindBolt)
	steps, ok := bdb.(learnedStepsStore)
	So(ok, ShouldBeTrue)

	return steps
}

func TestStoreFoldLearned(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given completions, releases and a bury in req group q, each exit then kicked, modified and delayed", t,
		func() {
			bdb, _ := openTestStore(t, ctx, storeKindBolt)
			boltSteps, ok := bdb.(learnedStepsStore)
			So(ok, ShouldBeTrue)

			bolt := learnedSteps(ctx, boltSteps, func(job *Job) { bdb.queueJobChangeAhead(ctx, job, nil, delayChange) })

			resume := pauseLogFolder()
			s, dir := openTestLogStore(t, ctx)

			Convey("once the folder is flushed, the log store has learned what bbolt learned, from exactly the "+
				"4 exits' samples, and invalidated its cache to learn it", func() {
				logged := learnedSteps(ctx, s, func(job *Job) {
					delayChange(job)
					So(s.updateJobAfterChangeDurable(job), ShouldBeNil)
				})

				So(bolt.rec.RAM, ShouldEqual, 900)
				So(bolt.rec.Time, ShouldEqual, 3605*time.Second)
				So(bolt.changes, ShouldBeGreaterThan, 0)
				So(logged, ShouldResemble, bolt)

				exitStats := 0
				for _, frame := range walFrames(dir) {
					exitStats += countRecords(frame.recs, logRecExitStat)
				}

				So(exitStats, ShouldEqual, 4)

				Convey("and after reopen it learns the same from what it folded", func() {
					resume()

					reopened := reopenLogStore(ctx, s)
					reopened.setRecRounding(foldTestSecRound, foldTestMBRound)
					rec := reopened.refreshReqGroupRecs(foldTestReqGroup)
					So(rec, ShouldNotBeNil)
					So(*rec, ShouldResemble, bolt.rec)
					So(inspectedCount(reopened, kindStat), ShouldEqual, bolt.samples)
				})
			})
		})
}

// learnedSteps makes D1 acceptance test 3's steps in s, with bestEffort making
// a best-effort state change of a job, flushes s and returns what it learned.
func learnedSteps(ctx context.Context, s learnedStepsStore, bestEffort func(job *Job)) learnedAnswers {
	jobs := testLogJobs("learned", "learned", 7)
	for _, job := range jobs {
		job.ReqGroup = foldTestReqGroup
	}

	jobs[6].Requirements.RAM = 100

	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	s.refreshReqGroupRecs(foldTestReqGroup)
	before := s.reqStatChanges()
	end := time.Now().Truncate(time.Second)

	for i, peakRAM := range []int{100, 100, 200} {
		jobs[i].State, jobs[i].Exited, jobs[i].PeakRAM = JobStateComplete, true, peakRAM
		jobs[i].StartTime, jobs[i].EndTime = end.Add(-time.Duration(i+1)*time.Minute), end
		_, err = s.archiveCompletion(jobs[i].Key(), jobs[i])
		So(err, ShouldBeNil)
	}

	for _, exit := range []struct {
		job      *Job
		state    JobState
		reason   string
		peakRAM  int
		peakDisk int64
	}{
		{jobs[3], JobStateDelayed, FailReasonRAM, 300, 0},
		{jobs[4], JobStateDelayed, FailReasonTime, 0, 0},
		{jobs[5], JobStateBuried, FailReasonDisk, 0, 50},
		{jobs[6], JobStateDelayed, FailReasonExit, 900, 0},
	} {
		job := exit.job
		job.State, job.Exited, job.Exitcode, job.FailReason = exit.state, true, 1, exit.reason
		job.PeakRAM, job.PeakDisk, job.StartTime, job.EndTime = exit.peakRAM, exit.peakDisk, end.Add(-time.Hour), end
		So(s.updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false), ShouldBeNil)

		job.State = JobStateReady
		So(s.updateJobAfterChangeDurable(job), ShouldBeNil)
	}

	moved := modifiedCopy(jobs[3], "echo learned modified")
	moved.ReqGroup, moved.FailReason, moved.PeakRAM = foldTestReqGroup, jobs[3].FailReason, jobs[3].PeakRAM
	So(s.modifyLiveJobs(ctx, []string{jobs[3].Key()}, []*Job{moved}), ShouldBeNil)

	bestEffort(jobs[4])
	So(s.flush(), ShouldBeNil)

	rec := s.refreshReqGroupRecs(foldTestReqGroup)
	So(rec, ShouldNotBeNil)

	samples, err := s.count(kindStat)
	So(err, ShouldBeNil)

	return learnedAnswers{changes: s.reqStatChanges() - before, rec: *rec, samples: samples}
}

// pauseLogFolder stops the folder goroutine of every log store folding, until
// the returned func or the end of the Convey block resumes it; foldNow still
// folds. Call it before opening the store, so that its folder never runs
// unpaused.
func pauseLogFolder() func() {
	paused := func() bool { return false }
	logFoldHook.Store(&paused)

	resume := func() { logFoldHook.CompareAndSwap(&paused, nil) }

	Reset(resume)

	return resume
}

// delayChange is the change of D1 acceptance test 3's delay change.
func delayChange(job *Job) {
	job.State = JobStateDelayed
}

func TestStoreFoldSpecPrecedence(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job archived under req group q1, added again under q2, archived again and added again under "+
		"q3, all in one fold interval", t, func() {
		fromBolt := readdSteps(ctx, openBoltStepsStore(t, ctx), func() {})

		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		Convey("a fold gives each archive's sample to the req group it ran under, as bbolt does", func() {
			logged := readdSteps(ctx, s, func() { So(s.foldNow(), ShouldBeNil) })

			So(fromBolt[foldSpecReqGroups[0]].RAM, ShouldBeGreaterThan, 0)
			So(fromBolt[foldSpecReqGroups[1]].RAM, ShouldBeGreaterThan, fromBolt[foldSpecReqGroups[0]].RAM)
			So(logged, ShouldResemble, fromBolt)
		})

		Convey("a fold in chunks of 1 record, so that each spec record is read in an earlier chunk than its "+
			"complete record, gives each sample to the same req group", func() {
			chunks := foldInChunksOfOne()

			logged := readdSteps(ctx, s, func() { So(s.foldNow(), ShouldBeNil) })

			So(chunks.Load(), ShouldBeGreaterThanOrEqualTo, len(foldSpecReqGroups)*2-1)
			So(logged, ShouldResemble, fromBolt)
		})
	})

	Convey("Given a job added under req group q1, modified keeping its key into req group mq, archived, added "+
		"again under q2 and archived again, all in one fold interval", t, func() {
		fromBolt := modifySpecSteps(ctx, openBoltStepsStore(t, ctx), func() {})

		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		Convey("a fold gives the first archive's sample to mq, from the modify's spec record, the latest of "+
			"its key read before it, as bbolt does", func() {
			logged := modifySpecSteps(ctx, s, func() { So(s.foldNow(), ShouldBeNil) })

			So(fromBolt[foldModifyReqGroup].RAM, ShouldEqual, foldTestMBRound)
			So(fromBolt[foldSpecReqGroups[0]].RAM, ShouldEqual, 0)
			So(logged, ShouldResemble, fromBolt)
		})

		Convey("so does a fold in chunks of 1 record", func() {
			foldInChunksOfOne()

			logged := modifySpecSteps(ctx, s, func() { So(s.foldNow(), ShouldBeNil) })

			So(logged, ShouldResemble, fromBolt)
		})
	})

	Convey("Given a job added under req group q1, archived twice and kept live each time, then modified "+
		"keeping its key into req group mq and archived again, all in one fold interval", t, func() {
		fromBolt := keptLiveSpecSteps(ctx, openBoltStepsStore(t, ctx), func() {})

		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		Convey("a fold gives both kept-live archives' samples to q1, the second from the spec record read "+
			"before the first, as bbolt does", func() {
			logged := keptLiveSpecSteps(ctx, s, func() { So(s.foldNow(), ShouldBeNil) })

			So(fromBolt[foldSpecReqGroups[0]].RAM, ShouldEqual, 2*foldTestMBRound)
			So(logged, ShouldResemble, fromBolt)
		})
	})
}

// foldInChunksOfOne makes folds read 1 record a chunk, so each frame is a
// chunk of its own, until the end of the Convey block, and returns how many
// fold transactions have committed since.
func foldInChunksOfOne() *atomic.Int64 {
	logFoldChunkRecords.Store(1)

	var chunks atomic.Int64

	committed := func() { chunks.Add(1) }
	logFoldCommittedHook.Store(&committed)

	Reset(func() {
		logFoldChunkRecords.Store(0)
		logFoldCommittedHook.Store(nil)
	})

	return &chunks
}

// readdSteps makes TestStoreFoldSpecPrecedence's steps in s, calls flush, and
// returns what s learned for each of foldSpecReqGroups.
func readdSteps(ctx context.Context, s learnedStepsStore, flush func()) map[string]jqs.Requirements {
	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	end := time.Now().Truncate(time.Second)

	for i, reqGroup := range foldSpecReqGroups {
		job := testDBJob("echo fold spec precedence", historyTestRepGroup)
		job.ReqGroup = reqGroup
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		if i == len(foldSpecReqGroups)-1 {
			break
		}

		job.State, job.Exited, job.PeakRAM = JobStateComplete, true, (i+1)*foldTestMBRound
		job.StartTime, job.EndTime = end.Add(-time.Duration(i+1)*time.Hour), end
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)
	}

	flush()

	learned := make(map[string]jqs.Requirements)

	for _, reqGroup := range foldSpecReqGroups {
		if rec := s.refreshReqGroupRecs(reqGroup); rec != nil {
			learned[reqGroup] = *rec
		}
	}

	return learned
}

// modifySpecSteps makes TestStoreFoldSpecPrecedence's modify steps in s:
// a job added under foldSpecReqGroups[0], modified keeping its key into
// foldModifyReqGroup and archived, then added again under
// foldSpecReqGroups[1] and archived again. It then calls flush, and returns
// what s learned for each of those req groups.
func modifySpecSteps(ctx context.Context, s learnedStepsStore, flush func()) map[string]jqs.Requirements {
	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	end := time.Now().Truncate(time.Second)

	job := testDBJob("echo fold modify spec", historyTestRepGroup)
	job.ReqGroup = foldSpecReqGroups[0]
	_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	modified := modifiedCopy(job, job.Cmd)
	modified.ReqGroup = foldModifyReqGroup
	So(modified.Key(), ShouldEqual, job.Key())
	So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{modified}), ShouldBeNil)

	modified.State, modified.Exited, modified.PeakRAM = JobStateComplete, true, foldTestMBRound
	modified.StartTime, modified.EndTime = end.Add(-time.Hour), end
	_, err = s.archiveCompletion(modified.Key(), modified)
	So(err, ShouldBeNil)

	again := testDBJob(job.Cmd, historyTestRepGroup)
	again.ReqGroup = foldSpecReqGroups[1]
	_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
	So(err, ShouldBeNil)

	again.State, again.Exited, again.PeakRAM = JobStateComplete, true, 2*foldTestMBRound
	again.StartTime, again.EndTime = end.Add(-time.Hour), end
	_, err = s.archiveCompletion(again.Key(), again)
	So(err, ShouldBeNil)

	flush()

	return learnedFor(s, foldSpecReqGroups[0], foldModifyReqGroup, foldSpecReqGroups[1])
}

// keptLiveSpecSteps makes TestStoreFoldSpecPrecedence's kept-live steps in s:
// a job added under foldSpecReqGroups[0] and archived twice, kept live each
// time, then modified keeping its key into foldModifyReqGroup and archived
// again. It then calls flush, and returns what s learned for each of those
// req groups.
func keptLiveSpecSteps(ctx context.Context, s learnedStepsStore, flush func()) map[string]jqs.Requirements {
	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	end := time.Now().Truncate(time.Second)

	job := testDBJob("echo fold kept live spec", historyTestRepGroup)
	job.ReqGroup = foldSpecReqGroups[0]
	_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	for i := range 2 {
		// each run ends at a time of its own, so each archive records a
		// completion of its own (see Job.archivedEndTime)
		ended := end.Add(time.Duration(i-2) * time.Minute)

		job.RerunAfterRun = true
		job.State, job.Exited, job.PeakRAM = JobStateComplete, true, (i+1)*foldTestMBRound
		job.StartTime, job.EndTime = ended.Add(-time.Hour), ended
		outcome, erra := s.archiveCompletion(job.Key(), job)
		So(erra, ShouldBeNil)
		So(outcome, ShouldEqual, archiveKeptLive)
	}

	modified := modifiedCopy(job, job.Cmd)
	modified.ReqGroup = foldModifyReqGroup
	So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{modified}), ShouldBeNil)

	modified.State, modified.Exited, modified.PeakRAM = JobStateComplete, true, 3*foldTestMBRound
	modified.StartTime, modified.EndTime = end.Add(-time.Hour), end
	outcome, err := s.archiveCompletion(modified.Key(), modified)
	So(err, ShouldBeNil)
	So(outcome, ShouldEqual, archiveRemovedLive)

	flush()

	return learnedFor(s, foldSpecReqGroups[0], foldModifyReqGroup)
}

// learnedFor returns what s learned for each of reqGroups.
func learnedFor(s learnedStepsStore, reqGroups ...string) map[string]jqs.Requirements {
	learned := make(map[string]jqs.Requirements)

	for _, reqGroup := range reqGroups {
		if rec := s.refreshReqGroupRecs(reqGroup); rec != nil {
			learned[reqGroup] = *rec
		}
	}

	return learned
}

func TestStoreFoldReplacedAfterFold(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job of req group q added and folded, archived, added again under q and archived again, "+
		"and the same steps in bbolt", t, func() {
		boltSamples, fromBolt := replacedAfterFoldSteps(ctx, openBoltStepsStore(t, ctx), func() {})

		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		Convey("a fold of both archives, the first replaced in the overlay and its spec record folded past, "+
			"keeps both archives' samples, as bbolt does", func() {
			samples, logged := replacedAfterFoldSteps(ctx, s, func() { So(s.foldNow(), ShouldBeNil) })

			So(boltSamples, ShouldEqual, foldReplacedSamples)
			So(samples, ShouldEqual, boltSamples)
			So(logged, ShouldResemble, fromBolt)
		})
	})
}

// replacedAfterFoldSteps makes TestStoreFoldReplacedAfterFold's steps in s,
// calling fold after the first add and at the end, and returns how many
// learned samples s then holds and what it learned for foldTestReqGroup.
func replacedAfterFoldSteps(ctx context.Context, s learnedStepsStore, fold func()) (int, jqs.Requirements) {
	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	end := time.Now().Truncate(time.Second)

	job := testDBJob("echo fold replaced after fold", historyTestRepGroup)
	job.ReqGroup = foldTestReqGroup
	_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	fold()

	for i, j := range []*Job{job, testDBJob(job.Cmd, historyTestRepGroup)} {
		if i > 0 {
			j.ReqGroup = foldTestReqGroup
			_, err = s.storeNewJobsGuarded(ctx, []*Job{j}, false, nil)
			So(err, ShouldBeNil)
		}

		j.State, j.Exited, j.PeakRAM = JobStateComplete, true, (2*i+1)*foldTestMBRound
		j.StartTime, j.EndTime = end.Add(-time.Hour), end
		_, err = s.archiveCompletion(j.Key(), j)
		So(err, ShouldBeNil)
	}

	fold()

	samples, err := s.count(kindStat)
	So(err, ShouldBeNil)

	rec := s.refreshReqGroupRecs(foldTestReqGroup)
	So(rec, ShouldNotBeNil)

	return samples, *rec
}

func TestStoreFoldManyCompletesEachKeyTwice(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given 20 jobs of req group q added and each archived twice, kept live the first time, and the "+
		"same steps in bbolt", t, func() {
		fromBolt := manyCompletesSteps(ctx, openBoltStepsStore(t, ctx))

		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		Convey("one fold of their 40 completions keeps both samples of each job, as bbolt does",
			func() {
				So(fromBolt, ShouldEqual, foldManyCompletesSamples)
				So(manyCompletesSteps(ctx, s), ShouldEqual, fromBolt)
			})
	})
}

// manyCompletesSteps makes TestStoreFoldManyCompletesEachKeyTwice's steps in
// s, archiving each job twice, then flushes s and returns how many learned
// samples it holds.
func manyCompletesSteps(ctx context.Context, s learnedStepsStore) int {
	end := time.Now().Truncate(time.Second)

	jobs := testLogJobs("fold many completes", historyTestRepGroup, foldManyCompletes)
	for _, job := range jobs {
		job.ReqGroup = foldTestReqGroup
	}

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	for i, job := range jobs {
		for run := range 2 {
			job.RerunAfterRun = run == 0
			job.State, job.Exited, job.PeakRAM = JobStateComplete, true, 1000+i*10+run*5000
			job.StartTime, job.EndTime = end.Add(-time.Hour), end.Add(time.Duration(run-2)*time.Minute)
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)
		}
	}

	So(s.flush(), ShouldBeNil)

	samples, err := s.count(kindStat)
	So(err, ShouldBeNil)

	return samples
}

func TestStoreFoldReplacedElsewhere(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, modify := range []bool{false, true} {
		again := "added again"
		if modify {
			again = "kept live and modified keeping its key"
		}

		Convey("Given a job of rep group rg1 and req group q1 added and folded, then archived, "+again+" into "+
			"rep group rg2 and req group q2, archived, "+again+" into rg3 and q3 and archived, and the same "+
			"steps in bbolt", t, func() {
			bs, ok := openBoltStepsStore(t, ctx).(replacedElsewhereStore)
			So(ok, ShouldBeTrue)

			noFold := func() {}
			replacedElsewhereSteps(ctx, bs, modify, noFold, noFold, noFold)
			fromBolt := replacedElsewhereProbe(bs)

			So(fromBolt.has, ShouldResemble, []bool{true, true, true})
			So(fromBolt.times, ShouldHaveLength, len(foldElsewhereRepGroups))

			for i, reqGroup := range foldSpecReqGroups {
				So(fromBolt.learned[reqGroup].RAM, ShouldBeGreaterThanOrEqualTo, (i+1)*foldTestMBRound)
			}

			pauseLogFolder()

			s, _ := openTestLogStore(t, ctx)
			fold := func() { So(s.foldNow(), ShouldBeNil) }

			// answersAsBolt asserts that st gives bbolt's answers, with nothing
			// left unfolded
			answersAsBolt := func(st *logStore) {
				So(replacedElsewhereProbe(st), ShouldResemble, fromBolt)
				So(overlayEmpty(st), ShouldBeTrue)
			}

			Convey("one fold of the archives, the first replaced in the overlay and its spec record folded "+
				"past, gives each archive's end time and sample to the rep and req group it ran under, as "+
				"bbolt does, also after reopen", func() {
				replacedElsewhereSteps(ctx, s, modify, fold, noFold, fold)

				eachReopen(ctx, &s, answersAsBolt)
			})

			Convey("so do two folds, the first to the log's durable end as it was after the first archive, "+
				"made once later archives have replaced it, as a fold that started between them would, which "+
				"drops that archive's replaced entry from memory and keeps the current one", func() {
				var noted historyPos

				firstArchived := func() {
					file, durEnd := s.w.Durable()
					noted = historyPos{file: file, off: uint64(durEnd)} //nolint:gosec // an offset
				}

				twoFolds := func() {
					f, err := s.readFold(noted)
					So(err, ShouldBeNil)
					So(f.complete, ShouldHaveLength, 1)
					So(s.foldHistory(f), ShouldBeNil)

					first := f.complete[0]

					s.mu.RLock()
					firstReplaced := slices.ContainsFunc(s.state.replaced[first.key],
						func(h *logHistory) bool { return h.seq == first.seq })
					current := s.state.overlay[first.key]
					s.mu.RUnlock()

					So(firstReplaced, ShouldBeFalse)
					So(current, ShouldNotBeNil)
					So(current.seq, ShouldBeGreaterThan, first.seq)

					fold()
				}

				replacedElsewhereSteps(ctx, s, modify, fold, firstArchived, twoFolds)

				eachReopen(ctx, &s, answersAsBolt)
			})

			Convey("so does a reopen before any fold of them, whose replay rebuilds the overlay, then a fold",
				func() {
					replacedElsewhereSteps(ctx, s, modify, fold, noFold, noFold)

					s = reopenLogStore(ctx, s)
					So(s.foldNow(), ShouldBeNil)

					eachReopen(ctx, &s, answersAsBolt)
				})
		})
	}
}

// replacedElsewhereSteps makes TestStoreFoldReplacedElsewhere's steps in s: a
// job of the first of foldElsewhereRepGroups and of foldSpecReqGroups added,
// then fold called, then the job archived, firstArchived called, and then,
// for each other pair of them in turn, the job, if modify, modified keeping
// its key, else added again, into that pair and archived again. With modify,
// the job is archived twice under the first pair, with no spec record
// between, and each archive but the last keeps it live. Each archive ends at a time
// of its own, fixed so that both stores' steps give the same answers, with a
// peak RAM of its own. Last, lastFold is called.
func replacedElsewhereSteps(ctx context.Context, s replacedElsewhereStore, modify bool,
	fold, firstArchived, lastFold func()) {
	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	last := len(foldElsewhereRepGroups) - 1

	job := testDBJob("echo fold replaced elsewhere", foldElsewhereRepGroups[0])
	job.ReqGroup = foldSpecReqGroups[0]
	_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	fold()

	for i, rg := range foldElsewhereRepGroups {
		if i > 0 {
			job = replacedElsewhereNext(ctx, s, modify, job, rg, foldSpecReqGroups[i])
		}

		runs := 1
		if modify && i == 0 {
			runs = 2
		}

		for run := range runs {
			want := archiveRemovedLive
			if modify && i < last {
				job.RerunAfterRun, want = true, archiveKeptLive
			}

			job.State, job.Exited, job.PeakRAM = JobStateComplete, true, (i+1)*foldTestMBRound+run*foldTestMBRound/2
			job.StartTime = foldWorkloadBase
			job.EndTime = foldWorkloadBase.Add(time.Duration(i+1)*time.Hour + time.Duration(run)*time.Minute)
			outcome, erra := s.archiveCompletion(job.Key(), job)
			So(erra, ShouldBeNil)
			So(outcome, ShouldEqual, want)

			if i == 0 && run == 0 {
				firstArchived()
			}
		}
	}

	lastFold()
}

// replacedElsewhereNext returns job, archived, made a job of rep group rg and
// req group reqGroup with the same key in s: modified into them if modify,
// else added again under them.
func replacedElsewhereNext(ctx context.Context, s replacedElsewhereStore, modify bool, job *Job,
	rg, reqGroup string) *Job {
	next := testDBJob(job.Cmd, rg)
	next.ReqGroup = reqGroup

	if modify {
		So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{next}), ShouldBeNil)
	} else {
		stored, err := s.storeNewJobsGuarded(ctx, []*Job{next}, false, nil)
		So(err, ShouldBeNil)
		So(stored.jobsToQueue, ShouldHaveLength, 1)
	}

	So(next.Key(), ShouldEqual, job.Key())

	return next
}

// replacedElsewhereProbe returns s's replacedElsewhereAnswers.
func replacedElsewhereProbe(s replacedElsewhereStore) replacedElsewhereAnswers {
	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	times, err := s.retrieveLastCompletionTimeByRepGroup(foldElsewhereRepGroups)
	So(err, ShouldBeNil)

	samples, err := s.count(kindStat)
	So(err, ShouldBeNil)

	a := replacedElsewhereAnswers{times: times, learned: learnedFor(s, foldSpecReqGroups...), samples: samples}

	for _, rg := range foldElsewhereRepGroups {
		a.has = append(a.has, s.repGroupHasHistory(rg))
	}

	return a
}

// overlayEmpty reports whether s's overlay holds no history entry, current or
// replaced, no rglookup, by rep group or by key, and no rep group's end time
// or rep group not yet folded.
func overlayEmpty(s *logStore) bool {
	entries, lookups := overlaySize(s)

	s.mu.RLock()
	ends, byKey, rgs := len(s.state.overlayEnds), len(s.state.keyRepGroups), len(s.state.lookedUpRGs)
	replaced := len(s.state.replaced)
	s.mu.RUnlock()

	return entries == 0 && lookups == 0 && byKey == 0 && ends == 0 && rgs == 0 && replaced == 0
}

// overlaySize returns how many history entries and rglookups s's overlay holds.
func overlaySize(s *logStore) (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lookups := 0
	for _, keys := range s.state.repGroupKeys {
		lookups += len(keys)
	}

	return len(s.state.overlay), lookups
}

// replacedElsewhereStore is what TestStoreFoldReplacedElsewhere asks of a
// store.
type replacedElsewhereStore interface {
	learnedStepsStore
	retrieveLastCompletionTimeByRepGroup(repGroups []string) (map[string]time.Time, error)
	repGroupHasHistory(repGroup string) bool
}

// replacedElsewhereAnswers is what TestStoreFoldReplacedElsewhere asks a
// store: the last completion time of each of foldElsewhereRepGroups, whether
// each has history, what it learned for each of foldSpecReqGroups, and how
// many learned samples it holds.
type replacedElsewhereAnswers struct {
	times   map[string]time.Time
	has     []bool
	learned map[string]jqs.Requirements
	samples int
}

// learnedAnswers is what a store learned from D1 acceptance test 3's steps:
// how much its reqStatChanges stamp rose, its requirements for
// foldTestReqGroup, and how many samples it holds.
type learnedAnswers struct {
	changes uint32
	rec     jqs.Requirements
	samples int
}

// logReplayStore is a log store as the store a recording is replayed against.
// If flushEach, each write call is followed by a fold to the durable end of the
// log.
type logReplayStore struct {
	*logStore

	flushEach bool
}

// flushed folds, if r flushes each write, then returns err.
func (r *logReplayStore) flushed(err error) error {
	if r.flushEach {
		So(r.foldNow(), ShouldBeNil)
	}

	return err
}

func (r *logReplayStore) storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
	guard *rerunGuard,
) (storedNewJobs, error) {
	stored, err := r.logStore.storeNewJobsGuarded(ctx, jobs, ignoreAdded, guard)

	return stored, r.flushed(err)
}

func (r *logReplayStore) archiveCompletion(key string, job *Job) (archiveOutcome, error) {
	outcome, err := r.logStore.archiveCompletion(key, job)

	return outcome, r.flushed(err)
}

func (r *logReplayStore) storeLiveForRerun(jobs []*Job) error {
	return r.flushed(r.logStore.storeLiveForRerun(jobs))
}

func (r *logReplayStore) storeRunningRerunMarks(jobs []*Job) error {
	return r.flushed(r.logStore.storeRunningRerunMarks(jobs))
}

func (r *logReplayStore) deleteLiveJobs(ctx context.Context, keys []string) error {
	return r.flushed(r.logStore.deleteLiveJobs(ctx, keys))
}

func (r *logReplayStore) modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error {
	return r.flushed(r.logStore.modifyLiveJobs(ctx, oldKeys, jobs))
}

func (r *logReplayStore) updateJobRunStateDurable(job *Job) error {
	return r.flushed(r.logStore.updateJobRunStateDurable(job))
}

func (r *logReplayStore) updateJobAfterExitDurable(job *Job, stdo, stde []byte, forceStorage bool) error {
	return r.flushed(r.logStore.updateJobAfterExitDurable(job, stdo, stde, forceStorage))
}

func TestStoreFoldWhileArchiving(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store, its folder running, with 400 jobs added and folded", t, func() {
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("fold while archiving", historyTestRepGroup, foldRacedArchives)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		Convey("16 writers archiving them while folds run back to back leave every archive folded, once each",
			func() {
				done := make(chan struct{})
				folded := make(chan error, 1)

				go func() {
					for {
						select {
						case <-done:
							folded <- nil

							return
						default:
						}

						if errf := s.foldNow(); errf != nil {
							folded <- errf

							return
						}
					}
				}()

				errs := make([]error, len(jobs))

				var wg sync.WaitGroup

				end := time.Now().Truncate(time.Second)

				for w := range foldRacedWriters {
					wg.Go(func() {
						for i := w; i < len(jobs); i += foldRacedWriters {
							job := jobs[i]
							job.State, job.Exited, job.StartTime, job.EndTime = JobStateComplete, true, end, end
							_, errs[i] = s.archiveCompletion(job.Key(), job)
						}
					})
				}

				wg.Wait()
				close(done)

				So(errors.Join(errs...), ShouldBeNil)

				select {
				case errf := <-folded:
					So(errf, ShouldBeNil)
				case <-time.After(foldTestWait):
					So("the folds never stopped", ShouldBeBlank)
				}

				So(s.foldNow(), ShouldBeNil)
				So(historyBoltCompletes(s), ShouldEqual, foldRacedArchives)
				So(overlayEmpty(s), ShouldBeTrue)

				byRG, errr := s.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
				So(errr, ShouldBeNil)
				So(jobKeys(byRG), ShouldResemble, jobKeys(sortedByKey(jobs)))
			})
	})
}

// historyBoltCompletes returns how many keys s's history.bolt holds a
// history entry of.
func historyBoltCompletes(s *logStore) int {
	index := make(map[logKey]uint64)

	_, _, err := s.history.load(index)
	So(err, ShouldBeNil)

	return len(index)
}

// sortedByKey returns jobs sorted by key.
func sortedByKey(jobs []*Job) []*Job {
	sorted := slices.Clone(jobs)
	slices.SortFunc(sorted, func(a, b *Job) int { return strings.Compare(a.Key(), b.Key()) })

	return sorted
}

func TestStoreFoldBatches(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, folds of any number of records a chunk, and "+
		strconv.Itoa(foldBatchedJobs)+" jobs of rep group A added and folded, then modified keeping their keys "+
		"into rep group B and archived", t, func() {
		pauseLogFolder()

		logFoldChunkRecords.Store(math.MaxInt64)

		var txs atomic.Int64

		committed := func() { txs.Add(1) }
		logFoldCommittedHook.Store(&committed)

		Reset(func() {
			logFoldChunkRecords.Store(0)
			logFoldCommittedHook.Store(nil)
		})

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("fold batches", foldBatchedRepGroups[0], foldBatchedJobs)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		modified := make([]*Job, len(jobs))
		for i, job := range jobs {
			modified[i] = modifiedCopy(job, job.Cmd)
			modified[i].RepGroup = foldBatchedRepGroups[1]
		}

		So(s.modifyLiveJobs(ctx, jobKeys(jobs), modified), ShouldBeNil)
		So(archiveConcurrently(s, modified, time.Now().Truncate(time.Second)), ShouldBeNil)

		Convey("one fold transaction of their modifies, completions and rglookups, each more than a batch of "+
			"its in-memory changes, leaves the overlay and the keys a modify dropped empty, the in-memory state "+
			"the one a reopen rebuilds, and rep group B with every job", func() {
			txs.Store(0)
			So(s.foldNow(), ShouldBeNil)
			So(txs.Load(), ShouldEqual, 1)
			So(overlayEmpty(s), ShouldBeTrue)

			s.mu.RLock()
			So(s.state.unlooked, ShouldBeEmpty)
			s.mu.RUnlock()

			soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))

			byRG, errr := s.retrieveCompleteJobsByRepGroup(foldBatchedRepGroups[1])
			So(errr, ShouldBeNil)
			So(jobKeys(byRG), ShouldResemble, jobKeys(sortedByKey(modified)))
		})
	})
}

// soStateLikeReplay asserts that s's in-memory state is the one a reopen of a
// copy in crashed of its files in dir rebuilds, and returns the reopened
// store, closed when the Convey block ends.
func soStateLikeReplay(ctx context.Context, s *logStore, dir, crashed string) *logStore {
	So(copyCrashStore(dir, crashed), ShouldBeNil)

	reopened := openLogStoreIn(ctx, crashed)

	Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

	s.mu.RLock()
	memory := comparableLogState(&s.state)
	s.mu.RUnlock()

	So(comparableLogState(&reopened.state), ShouldResemble, memory)
	So(overlayEndAnswers(reopened), ShouldResemble, overlayEndAnswers(s))

	return reopened
}

// archiveConcurrently archives each of jobs in s, as completed at end, from
// foldRacedWriters writers, so that their syncs share batches, returning
// their errors joined.
func archiveConcurrently(s *logStore, jobs []*Job, end time.Time) error {
	errs := make([]error, len(jobs))

	var wg sync.WaitGroup

	for w := range foldRacedWriters {
		wg.Go(func() {
			for i := w; i < len(jobs); i += foldRacedWriters {
				job := jobs[i]
				job.State, job.Exited, job.StartTime, job.EndTime = JobStateComplete, true, end, end
				_, errs[i] = s.archiveCompletion(job.Key(), job)
			}
		})
	}

	wg.Wait()

	return errors.Join(errs...)
}

func TestLogStoreHistoryOverlay(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and rep group rg never seen before", t, func() {
		resume := pauseLogFolder()
		s, _ := openTestLogStore(t, ctx)

		before := time.Now().Add(-time.Minute)
		job := testDBJob("echo history overlay", historyTestRepGroup)
		other := testDBJob("echo history overlay other", historyUnarchivedRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job, other}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second).Add(-time.Second)
		archiveTestJob(s, job, end.Add(-time.Minute), end)

		Convey("when a job of rg is archived, every history query answers with it, before and after reopen", func() {
			entries, lookups := overlaySize(s)
			So(entries, ShouldEqual, 1)
			So(lookups, ShouldEqual, 2)

			checkArchivedAnswers(s, job, before, end)
			checkArchivedAnswers(reopenLogStore(ctx, s), job, before, end)
		})

		Convey("when the folder resumes and runs, the same answers are given and the overlay is empty, also after "+
			"reopen", func() {
			resume()
			So(pollUntilFor(foldTestWait, func() bool { return overlayEmpty(s) }), ShouldBeTrue)

			checkArchivedAnswers(s, job, before, end)

			reopened := reopenLogStore(ctx, s)
			So(overlayEmpty(reopened), ShouldBeTrue)
			checkArchivedAnswers(reopened, job, before, end)
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

func TestStoreFoldIdempotent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with 3 jobs archived and its folder paused", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("idempotent", historyTestRepGroup, 3)
		for _, job := range jobs {
			job.ReqGroup = foldTestReqGroup
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		for _, job := range jobs {
			archiveTestJob(s, job, end.Add(-time.Minute), end)
		}

		Convey("when a fold is killed after its history.bolt transaction, before anything else, and the store "+
			"reopened and folded again, nothing is folded twice", func() {
			killed := filepath.Join(t.TempDir(), "killed")

			kill := func() { So(copyCrashStore(dir, killed), ShouldBeNil) }
			logFoldCommittedHook.Store(&kill)

			Reset(func() { logFoldCommittedHook.Store(nil) })

			So(s.foldNow(), ShouldBeNil)
			logFoldCommittedHook.Store(nil)

			complete := inspectedCount(s, kindComplete)
			samples := inspectedCount(s, kindStat)

			So(complete, ShouldEqual, 3)
			So(samples, ShouldBeGreaterThan, 0)

			s.mu.RLock()
			folded := s.state.folded
			s.mu.RUnlock()

			reopened := openLogStoreIn(ctx, killed)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			check := func() {
				So(inspectedCount(reopened, kindComplete), ShouldEqual, complete)
				So(inspectedCount(reopened, kindStat), ShouldEqual, samples)
				So(overlayEmpty(reopened), ShouldBeTrue)

				byRG, errr := reopened.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
				So(errr, ShouldBeNil)
				So(jobKeys(byRG), ShouldResemble, jobKeys(sortedByKey(jobs)))
			}

			reopened.mu.RLock()
			So(reopened.state.folded, ShouldResemble, folded)
			reopened.mu.RUnlock()

			check()
			So(reopened.foldNow(), ShouldBeNil)
			check()
		})
	})
}

func TestStoreFoldPastRotation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, folding in chunks of 1 record, with 3 jobs added and "+
		"folded, its wal rotated, then 2 more added and folded, so the folded position is in the second file",
		t, func() {
			pauseLogFolder()
			foldInChunksOfOne()

			s, _ := openTestLogStore(t, ctx)

			_, err := s.storeNewJobsGuarded(ctx, testLogJobs("past rotation a", historyUnarchivedRepGroup, 3),
				false, nil)
			So(err, ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)

			_, err = s.w.Rotate()
			So(err, ShouldBeNil)

			_, err = s.storeNewJobsGuarded(ctx, testLogJobs("past rotation b", historyUnarchivedRepGroup, 2),
				false, nil)
			So(err, ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)

			Convey("a fold of 2 jobs then added and archived returns, having folded them and left the overlay "+
				"empty", func() {
				jobs := testLogJobs("past rotation c", historyTestRepGroup, 2)
				_, err = s.storeNewJobsGuarded(ctx, jobs, false, nil)
				So(err, ShouldBeNil)

				end := time.Now().Truncate(time.Second)
				for _, job := range jobs {
					archiveTestJob(s, job, end.Add(-time.Minute), end)
				}

				folded := make(chan error, 1)

				go func() { folded <- s.foldNow() }()

				select {
				case err = <-folded:
					So(err, ShouldBeNil)
				case <-time.After(foldTestWait):
					So("the fold did not return", ShouldBeBlank)
				}

				So(overlayEmpty(s), ShouldBeTrue)

				byRG, errr := s.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
				So(errr, ShouldBeNil)
				So(jobKeys(byRG), ShouldResemble, jobKeys(sortedByKey(jobs)))
			})
		})
}

func TestStoreFoldNotOnReplyPath(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store reporting every 50ms whose folder a hook blocks for 2s inside its first fold's "+
		"history.bolt transaction", t, func() {
		old := archiveFoldReportInterval
		archiveFoldReportInterval = storeStatsTestInterval

		Reset(func() { archiveFoldReportInterval = old })

		entered := make(chan struct{})

		var (
			once         sync.Once
			blockedUntil time.Time
		)

		block := func() error { //nolint:unparam // the hook's type; this one never fails a fold
			once.Do(func() {
				blockedUntil = time.Now().Add(foldBlockTime)

				close(entered)
				time.Sleep(foldBlockTime)
			})

			return nil
		}

		logFoldTxHook.Store(&block)

		Reset(func() { logFoldTxHook.Store(nil) })

		logPath, logCtx := managerLogContext(t, ctx)
		s, _ := openTestLogStore(t, logCtx)

		jobs := testLogJobs("blocked fold", historyTestRepGroup, foldBlockedArchives)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		Convey("100 archives made meanwhile each return within 500ms, and the folder then folds them and "+
			"reports as writer=fold", func() {
			select {
			case <-entered:
			case <-time.After(foldTestWait):
				So("the folder never ran", ShouldBeBlank)
			}

			// concurrently, as a manager's are, so that the log's group
			// commit makes them in a few syncs whatever a sync costs here
			took := make([]time.Duration, len(jobs))
			errs := make([]error, len(jobs))

			var wg sync.WaitGroup

			end := time.Now().Truncate(time.Second)
			for i, job := range jobs {
				job.State, job.Exited, job.StartTime, job.EndTime = JobStateComplete, true, end, end

				wg.Go(func() {
					started := time.Now()
					_, errs[i] = s.archiveCompletion(job.Key(), job)
					took[i] = time.Since(started)
				})
			}

			wg.Wait()

			So(time.Now().Before(blockedUntil), ShouldBeTrue)
			So(errors.Join(errs...), ShouldBeNil)
			So(slices.Max(took), ShouldBeLessThan, foldArchiveLimit)

			var lines []string

			// the adds' rglookups and the archives' completions
			folded := 2 * foldBlockedArchives

			So(pollUntilFor(foldBlockTime+foldTestWait, func() bool {
				lines = storeStatsWriterLines(readLogFile(t, logPath), foldWriterName)

				return storeStatsSumOf(lines, "items") >= folded && overlayEmpty(s)
			}), ShouldBeTrue)
		})
	})
}

// storeStatsSumOf is storeStatsSum of lines without failing on a line it cannot
// parse, for polling.
func storeStatsSumOf(lines []string, key string) int {
	sum := 0

	for _, line := range lines {
		n, err := strconv.Atoi(logLineValue(line, key))
		if err == nil {
			sum += n
		}
	}

	return sum
}

func TestStoreFoldPutBack(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a job archived and then put back live", t, func() {
		resume := pauseLogFolder()
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold put back", historyTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second).Add(-time.Second)
		archiveTestJob(s, job, end.Add(-time.Minute), end)

		job.State = JobStateReady
		So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)

		check := func(st *logStore) {
			live, errl := st.checkIfLive(job.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)

			complete, errc := st.checkIfComplete(job.Key())
			So(errc, ShouldBeNil)
			So(complete, ShouldBeTrue)

			times, errt := st.retrieveLastCompletionTimeByRepGroup([]string{historyTestRepGroup})
			So(errt, ShouldBeNil)
			So(times, ShouldResemble, map[string]time.Time{historyTestRepGroup: end.UTC()})
			So(st.repGroupHasHistory(historyTestRepGroup), ShouldBeTrue)

			byRG, errr := st.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
			So(errr, ShouldBeNil)
			So(byRG, ShouldBeEmpty)

			recent, errn := st.retrieveCompleteJobsRecent(end.Add(-time.Hour))
			So(errn, ShouldBeNil)
			So(recent, ShouldBeEmpty)

			status, errs := st.retrieveCompleteJobStatusByRepGroup(historyTestRepGroup, true)
			So(errs, ShouldBeNil)
			So(status.Counts[JobStateComplete], ShouldEqual, 0)
		}

		Convey("before and after the folder resumes and folds it, and after reopen, it is live and complete, "+
			"has history and is omitted where live keys are skipped", func() {
			So(overlayEmpty(s), ShouldBeFalse)
			check(s)

			resume()
			So(pollUntilFor(foldTestWait, func() bool { return overlayEmpty(s) }), ShouldBeTrue)
			check(s)

			reopened := reopenLogStore(ctx, s)
			So(overlayEmpty(reopened), ShouldBeTrue)
			check(reopened)
		})
	})
}

func TestStoreFoldUnprompted(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose learned requirements for q are loaded and current", t, func() {
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("unprompted", historyTestRepGroup, 2)
		for _, job := range jobs {
			job.ReqGroup = foldTestReqGroup
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		So(s.refreshReqGroupRecs(foldTestReqGroup).RAM, ShouldEqual, 0)

		learns := func(peakRAM int) {
			So(pollUntilFor(foldUnpromptedLimit, func() bool {
				_, current := s.cachedReqGroupRecs(foldTestReqGroup)

				return !current && overlayEmpty(s)
			}), ShouldBeTrue)

			So(s.refreshReqGroupRecs(foldTestReqGroup).RAM, ShouldEqual, peakRAM)
		}

		Convey("the folder, never asked to, keeps folding each archive and invalidating the cache for its new "+
			"sample", func() {
			end := time.Now().Truncate(time.Second)

			for i, peakRAM := range []int{300, 1200} {
				jobs[i].State, jobs[i].Exited, jobs[i].StartTime, jobs[i].EndTime = JobStateComplete, true, end, end
				jobs[i].PeakRAM = peakRAM

				_, err = s.archiveCompletion(jobs[i].Key(), jobs[i])
				So(err, ShouldBeNil)

				learns(peakRAM)
			}
		})
	})
}

func TestStoreFoldUncommittedSample(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with no learned samples, a load while a fold's new sample is not yet committed "+
		"is not current once it commits", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo uncommitted sample", historyTestRepGroup)
		job.ReqGroup = foldTestReqGroup
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		So(s.refreshReqGroupRecs(foldTestReqGroup).RAM, ShouldEqual, 0)

		end := time.Now().Truncate(time.Second)
		job.State, job.Exited, job.StartTime, job.EndTime, job.PeakRAM = JobStateComplete, true, end, end, 300
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)

		loaded := make(chan *jqs.Requirements, 1)

		onFoldTx(func() {
			go func() { loaded <- s.refreshReqGroupRecs(foldTestReqGroup) }()

			select {
			case rec := <-loaded:
				So(rec.RAM, ShouldEqual, 0)
			case <-time.After(foldLoadHangTimeout):
				So("the load blocked on the fold's open transaction", ShouldBeBlank)
			}
		})

		So(s.foldNow(), ShouldBeNil)

		_, current := s.cachedReqGroupRecs(foldTestReqGroup)
		So(current, ShouldBeFalse)
		So(s.refreshReqGroupRecs(foldTestReqGroup).RAM, ShouldEqual, 300)
	})
}

// onFoldTx makes fn run inside each fold's history.bolt transaction, until
// the Convey block ends.
func onFoldTx(fn func()) {
	hook := func() error { //nolint:unparam // the hook's type; this one never fails a fold
		fn()

		return nil
	}

	logFoldTxHook.Store(&hook)

	Reset(func() { logFoldTxHook.Store(nil) })
}

func TestStoreFoldNeverMovesBack(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a job archived and folded", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo never moves back", historyTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end, end)
		So(s.foldNow(), ShouldBeNil)

		s.mu.RLock()
		folded := s.state.folded
		s.mu.RUnlock()

		Convey("a fold to an earlier position moves the folded position back neither in memory nor in "+
			"history.bolt", func() {
			So(s.foldHistory(&historyFold{to: historyPos{file: folded.file, off: 1}}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				st.mu.RLock()
				So(st.state.folded, ShouldResemble, folded)
				st.mu.RUnlock()

				So(overlayEmpty(st), ShouldBeTrue)
			})
		})
	})
}

func TestStoreFoldReplay(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, workload := range foldWorkloads {
		Convey("Given a recording against bbolt of "+workload.name+", with every history query and a re-add "+
			"after each step", t, func() {
			path := filepath.Join(t.TempDir(), "workload.rec")

			recording := openRecordingStoreIn(t, ctx, storeKindBolt, path)
			workload.run(ctx, recording)
			So(recording.close(ctx), ShouldBeNil)

			pauseLogFolder()

			s, _ := openTestLogStore(t, ctx)

			for _, flushEach := range []bool{false, true} {
				Convey(fmt.Sprintf("when replayed against the log store with the folder flushed after every "+
					"write call %t, nothing mismatches", flushEach), func() {
					replayed := &logReplayStore{logStore: s, flushEach: flushEach}

					stats, err := replayRecording(ctx, path, replayed)
					So(err, ShouldBeNil)
					So(stats.calls, ShouldBeGreaterThan, 0)
					So(stats.skipped, ShouldEqual, 0)
					So(mismatchedMethods(stats), ShouldBeEmpty)

					entries, _ := overlaySize(s)
					So(entries == 0, ShouldEqual, flushEach)
				})
			}
		})
	}
}

func TestStoreFoldBeforeApply(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and a job of rep group rg", t, func() {
		hold := newAppendedHold()

		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold before apply", historyTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second)

		// archiveFoldedFirst archives job with its write held after it is
		// appended, folds once the write is durable, before it applies, then
		// lets it apply, and checks the completion answers as it would have.
		archiveFoldedFirst := func() {
			file, off := s.w.Durable()
			before := historyPos{file: file, off: uint64(off)} //nolint:gosec // an offset

			job.State, job.Exited, job.StartTime, job.EndTime = JobStateComplete, true, end.Add(-time.Minute), end

			hold.arm()

			archived := make(chan error, 1)

			go func() {
				_, erra := s.archiveCompletion(job.Key(), job)
				archived <- erra
			}()

			hold.awaitEntered()
			So(pollUntilFor(logSyncHoldWait, func() bool {
				file, off = s.w.Durable()

				return historyPos{file: file, off: uint64(off)}.after(before) //nolint:gosec // an offset
			}), ShouldBeTrue)

			So(s.foldNow(), ShouldBeNil)

			complete, errc := s.checkIfComplete(job.Key())
			So(errc, ShouldBeNil)
			So(complete, ShouldBeTrue)

			got, found, errc := s.completeJob(job.Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(got.Cmd, ShouldEqual, job.Cmd)
			So(got.RepGroup, ShouldEqual, historyTestRepGroup)

			hold.Release()

			select {
			case err = <-archived:
				So(err, ShouldBeNil)
			case <-time.After(logSyncHoldWait):
				So("the held archive never returned", ShouldBeBlank)
			}

			eachReopen(ctx, &s, func(st *logStore) {
				So(overlayEmpty(st), ShouldBeTrue)
				soLive(st, job.Key(), false)
				checkArchivedAnswers(st, job, end.Add(-time.Hour), end)
			})
		}

		Convey("a fold of its archive before the archive applies pairs the complete record with its live spec, "+
			"its spec record being folded past already", func() {
			So(s.foldNow(), ShouldBeNil)

			archiveFoldedFirst()
		})

		Convey("a fold of its archive after it was deleted, before the archive applies, pairs the complete record "+
			"with the spec record the archive wrote before it", func() {
			So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)

			archiveFoldedFirst()
		})

		Convey("a fold of its archive before the archive applies, reading its add and then a modify keeping its "+
			"key into req group mq, pairs the complete record with the modify's spec, the latest read", func() {
			modified := modifiedCopy(job, job.Cmd)
			modified.ReqGroup = foldModifyReqGroup
			So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{modified}), ShouldBeNil)

			job = modified

			archiveFoldedFirst()

			got, found, errc := s.completeJob(job.Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(got.ReqGroup, ShouldEqual, foldModifyReqGroup)
		})

		Convey("a fold that overtook an archive moves the folded position before its first batch of in-memory "+
			"changes, and the archive, applying between them, leaves the job complete throughout", func() {
			So(s.foldNow(), ShouldBeNil)

			file, off := s.w.Durable()
			before := historyPos{file: file, off: uint64(off)} //nolint:gosec // an offset

			job.State, job.Exited, job.StartTime, job.EndTime = JobStateComplete, true, end.Add(-time.Minute), end

			hold.arm()

			archived := make(chan error, 1)

			go func() {
				_, erra := s.archiveCompletion(job.Key(), job)
				archived <- erra
			}()

			hold.awaitEntered()
			So(pollUntilFor(logSyncHoldWait, func() bool {
				file, off = s.w.Durable()

				return historyPos{file: file, off: uint64(off)}.after(before) //nolint:gosec // an offset
			}), ShouldBeTrue)

			var once sync.Once

			onFoldBatch(func() {
				once.Do(func() {
					hold.Release()

					select {
					case erra := <-archived:
						So(erra, ShouldBeNil)
					case <-time.After(logSyncHoldWait):
						So("the held archive never returned", ShouldBeBlank)
					}

					soLive(s, job.Key(), false)
				})

				s.mu.RLock()
				So(s.state.folded.after(before), ShouldBeTrue)
				s.mu.RUnlock()

				complete, errc := s.checkIfComplete(job.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeTrue)
			})

			So(s.foldNow(), ShouldBeNil)
			logFoldBatchHook.Store(nil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(overlayEmpty(st), ShouldBeTrue)
				checkArchivedAnswers(st, job, end.Add(-time.Hour), end)
			})
		})
	})
}

// onFoldBatch makes fn run after each batch of each fold's changes to the
// in-memory state, until the Convey block ends.
func onFoldBatch(fn func()) {
	logFoldBatchHook.Store(&fn)

	Reset(func() { logFoldBatchHook.Store(nil) })
}

func TestStoreFoldLookupAfterModify(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and a job of rep group A archived", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold lookup after modify", "foldrgA")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end, end)

		Convey("when, while a fold of its rglookup commits, it is put back, modified keeping its key and rep group "+
			"and deleted, rep group A still finds it, before and after that modify folds, and after reopen", func() {
			var once sync.Once

			onFoldTx(func() {
				once.Do(func() {
					job.State = JobStateReady
					So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)

					same := modifiedCopy(job, job.Cmd)
					same.Priority = 7
					So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{same}), ShouldBeNil)
					So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)
				})
			})

			findsIt := func(st *logStore) {
				byRG, errr := st.retrieveCompleteJobsByRepGroup("foldrgA")
				So(errr, ShouldBeNil)
				So(jobKeys(byRG), ShouldResemble, []string{job.Key()})
			}

			So(s.foldNow(), ShouldBeNil)
			logFoldTxHook.Store(nil)
			findsIt(s)

			So(s.foldNow(), ShouldBeNil)
			So(overlayEmpty(s), ShouldBeTrue)

			s.mu.RLock()
			So(s.state.unlooked, ShouldBeEmpty)
			s.mu.RUnlock()

			eachReopen(ctx, &s, findsIt)
		})
	})
}

func TestStoreFoldRepGroupQueriesDuringModifies(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and "+strconv.Itoa(foldQueriedJobs)+" jobs of rep group A "+
		"added and folded", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("fold queried", foldQueriedRepGroup, foldQueriedJobs)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		Convey("rep group A's queries, made while its jobs are modified keeping their keys and rep group, "+
			"each modify then folded, each find every job", func() {
			stop := make(chan struct{})
			queried := make(chan error, 1)

			var queries, misses atomic.Int64

			go func() {
				for {
					select {
					case <-stop:
						queried <- nil

						return
					default:
					}

					budget := newArchivedBytesBudget()
					if errq := s.spendArchivedBytesByRepGroup(foldQueriedRepGroup, budget); errq != nil {
						queried <- errq

						return
					}

					queries.Add(1)

					if budget.priced != foldQueriedJobs {
						misses.Add(1)
					}
				}
			}()

			for round := range foldQueriedRounds {
				job := jobs[round%len(jobs)]
				same := modifiedCopy(job, job.Cmd)
				same.Priority = uint8(round % math.MaxUint8)

				So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{same}), ShouldBeNil)
				So(s.foldNow(), ShouldBeNil)
			}

			close(stop)
			So(receiveWithin(queried, logSyncHoldWait), ShouldBeNil)
			So(queries.Load(), ShouldBeGreaterThan, 0)
			So(misses.Load(), ShouldEqual, 0)

			s.mu.RLock()
			So(s.state.unlooked, ShouldBeEmpty)
			s.mu.RUnlock()
		})
	})
}

func TestStoreFoldLookedUpRepGroups(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and folds of one frame a chunk", t, func() {
		pauseLogFolder()
		foldInChunksOfOne()

		s, dir := openTestLogStore(t, ctx)

		first := testDBJob("echo fold looked up first", foldLookedUpRepGroup)
		second := testDBJob("echo fold looked up second", foldLookedUpRepGroup)

		Convey("when two adds of a rep group apply and a fold folds the first's frame alone, the rep group is "+
			"still one with an rglookup not folded, as a replay of the log from the folded position finds it",
			func() {
				for _, job := range []*Job{first, second} {
					_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
					So(err, ShouldBeNil)
				}

				foldFirstFrame(s, first, second)
				soLookedUpLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			})

		Convey("when an add of a rep group takes its seqs, then a later add of it is appended and applies "+
			"before it, and a fold folds the later add's frame alone, the rep group is still one with an "+
			"rglookup not folded, as a replay of the log from the folded position finds it", func() {
			dependent := testDBJob("echo fold looked up dependent", "foldlookeddeps")
			dependent.Dependencies = Dependencies{NewDepGroupDependency("foldlookedg")}
			archiveNew(ctx, s, dependent)
			So(s.foldNow(), ShouldBeNil)

			first.DepGroups = []string{"foldlookedg"}

			// The first add reaches this hook once it has taken its seqs,
			// before it appends, as it then plans putting dependent back.
			logPutBackHook = func() {
				logPutBackHook = nil

				_, err := s.storeNewJobsGuarded(ctx, []*Job{second}, false, nil)
				So(err, ShouldBeNil)
			}

			Reset(func() { logPutBackHook = nil })

			_, err := s.storeNewJobsGuarded(ctx, []*Job{first}, false, nil)
			So(err, ShouldBeNil)

			foldFirstFrame(s, second, first)
			soLookedUpLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
		})

		Convey("when an add of a rep group is appended, then a later add of it is appended and applies before "+
			"it, and a fold folds the earlier add's frame alone, the rep group is still one with an rglookup "+
			"not folded, as a replay of the log from the folded position finds it", func() {
			hold := newAppendedHold()
			hold.arm()

			added := make(chan error, 1)

			go func() {
				_, erra := s.storeNewJobsGuarded(ctx, []*Job{first}, false, nil)
				added <- erra
			}()

			hold.awaitEntered()

			_, err := s.storeNewJobsGuarded(ctx, []*Job{second}, false, nil)
			So(err, ShouldBeNil)

			hold.Release()
			So(receiveWithin(added, logSyncHoldWait), ShouldBeNil)

			foldFirstFrame(s, first, second)
			soLookedUpLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
		})

		Convey("when an add of a rep group is appended late in one wal file and a later add of it early in "+
			"the next, and a fold folds the earlier add's frame alone, the rep group is still one with an "+
			"rglookup not folded, as a replay of the log from the folded position finds it", func() {
			_, err := s.storeNewJobsGuarded(ctx, testLogJobs("fold looked up padding", "foldlookedpad",
				foldLookedUpPadding), false, nil)
			So(err, ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)

			_, err = s.storeNewJobsGuarded(ctx, []*Job{first}, false, nil)
			So(err, ShouldBeNil)

			_, err = s.w.Rotate()
			So(err, ShouldBeNil)

			_, err = s.storeNewJobsGuarded(ctx, []*Job{second}, false, nil)
			So(err, ShouldBeNil)

			foldFirstFrame(s, first, second)
			soLookedUpLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
		})
	})
}

// foldFirstFrame folds the first frame of s's log not yet folded, by failing
// the fold's transaction after it, and asserts that it was that of the add of
// folded, and not of unfolded, both of foldLookedUpRepGroup, and that the rep
// group is still one with an rglookup not folded.
func foldFirstFrame(s *logStore, folded, unfolded *Job) {
	var txs atomic.Int64

	failLater := func() error {
		if txs.Add(1) > 1 {
			return errFoldTestFailure
		}

		return nil
	}

	logFoldTxHook.Store(&failLater)

	Reset(func() { logFoldTxHook.Store(nil) })

	So(s.foldNow(), ShouldWrap, errFoldTestFailure)
	logFoldTxHook.Store(nil)

	s.mu.RLock()
	keys := maps.Clone(s.state.repGroupKeys[foldLookedUpRepGroup])
	lookedUp := maps.Clone(s.state.lookedUpRGs)
	s.mu.RUnlock()

	foldedKey, err := parseLogKey(folded.Key())
	So(err, ShouldBeNil)

	unfoldedKey, err := parseLogKey(unfolded.Key())
	So(err, ShouldBeNil)

	So(keys, ShouldNotContainKey, foldedKey)
	So(keys, ShouldContainKey, unfoldedKey)
	So(lookedUp, ShouldContainKey, foldLookedUpRepGroup)
}

// soLookedUpLikeReplay asserts that s's in-memory state, folded part way, is
// the one a reopen of a copy in crashed of its files in dir rebuilds, and that
// both list foldLookedUpRepGroup.
func soLookedUpLikeReplay(ctx context.Context, s *logStore, dir, crashed string) {
	reopened := soStateLikeReplay(ctx, s, dir, crashed)

	for _, st := range []*logStore{s, reopened} {
		rgs, err := st.retrieveRepGroups()
		So(err, ShouldBeNil)
		So(rgs, ShouldContain, foldLookedUpRepGroup)
	}
}

func TestStoreFoldAfterReadd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a job of rep group A archived and then added again under "+
		"rep group B", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold after readd", "foldrgA")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		// so that its spec record is folded past, and the fold of its archive
		// finds the spec it completed with only in the overlay.
		So(s.foldNow(), ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end, end)

		_, err = s.storeNewJobsGuarded(ctx, []*Job{testDBJob(job.Cmd, "foldrgB")}, false, nil)
		So(err, ShouldBeNil)

		Convey("a fold pairs the completion with the spec it completed with, of rep group A, not the live one, "+
			"also after reopen", func() {
			So(s.foldNow(), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				byKeys, errk := st.retrieveCompleteJobsByKeys([]string{job.Key()})
				So(errk, ShouldBeNil)
				So(byKeys, ShouldHaveLength, 1)
				So(byKeys[0].RepGroup, ShouldEqual, "foldrgA")

				times, errt := st.retrieveLastCompletionTimeByRepGroup([]string{"foldrgA", "foldrgB"})
				So(errt, ShouldBeNil)
				So(times, ShouldResemble, map[string]time.Time{"foldrgA": end.UTC()})
			})
		})
	})
}

func TestStoreFoldTwoModifies(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and a job of rep group A archived, put back and modified "+
		"keeping its key", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold two modifies", "foldrgA")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end, end)

		job.State = JobStateReady
		So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)

		same := modifiedCopy(job, job.Cmd)
		same.Priority = 7
		So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{same}), ShouldBeNil)

		Convey("when, while that modify folds, it is modified again into rep group B and deleted, rep group A "+
			"no longer finds it and B does, before, while and after the second modify folds, and after reopen",
			func() {
				var once sync.Once

				onFoldTx(func() {
					once.Do(func() {
						inB := modifiedCopy(same, same.Cmd)
						inB.RepGroup = "foldrgB"
						So(s.modifyLiveJobs(ctx, []string{same.Key()}, []*Job{inB}), ShouldBeNil)
						So(s.deleteLiveJobs(ctx, []string{same.Key()}), ShouldBeNil)
					})
				})

				movedToB := func(st *logStore) {
					inA, errr := st.retrieveCompleteJobsByRepGroup("foldrgA")
					So(errr, ShouldBeNil)
					So(inA, ShouldBeEmpty)

					inB, errr := st.retrieveCompleteJobsByRepGroup("foldrgB")
					So(errr, ShouldBeNil)
					So(jobKeys(inB), ShouldResemble, []string{job.Key()})
				}

				So(s.foldNow(), ShouldBeNil)
				logFoldTxHook.Store(nil)
				movedToB(s)

				batches := 0

				onFoldBatch(func() {
					batches++

					movedToB(s)
				})

				So(s.foldNow(), ShouldBeNil)
				So(batches, ShouldBeGreaterThan, 1)
				logFoldBatchHook.Store(nil)

				eachReopen(ctx, &s, movedToB)
			})
	})
}

func TestStoreFoldFailureRetried(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose first 3 folds fail, and the 3 after its first success", t, func() {
		var (
			mu    sync.Mutex
			calls []time.Time
		)

		failing := func() error {
			mu.Lock()
			defer mu.Unlock()

			calls = append(calls, time.Now())
			if (len(calls)-1)%(foldTestFailures+1) < foldTestFailures {
				return errFoldTestFailure
			}

			return nil
		}

		logFoldTxHook.Store(&failing)

		Reset(func() { logFoldTxHook.Store(nil) })

		logPath, logCtx := managerLogContext(t, ctx)
		s, _ := openTestLogStore(t, logCtx)

		job := testDBJob("echo fold failure", historyTestRepGroup)
		second := testDBJob("echo fold failure again", "foldfailrg2")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job, second}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end, end)

		Convey("the folder retries with the log writer's backoff, faster than its interval, until it folds, "+
			"and, its backoff reset by that success, retries the next failures as fast, logging the failure "+
			"once at error and counting only the folds that committed as db writer transactions", func() {
			So(pollUntilFor(foldTestWait, func() bool { return overlayEmpty(s) }), ShouldBeTrue)
			So(s.foldStats.take().txs, ShouldEqual, 1)

			mu.Lock()
			So(calls, ShouldHaveLength, foldTestFailures+1)
			So(calls[foldTestFailures].Sub(calls[0]), ShouldBeLessThan, foldInterval)
			mu.Unlock()

			archiveTestJob(s, second, end, end)

			So(pollUntilFor(foldTestWait, func() bool { return overlayEmpty(s) }), ShouldBeTrue)
			So(s.foldStats.take().txs, ShouldEqual, 1)

			mu.Lock()
			So(calls, ShouldHaveLength, 2*(foldTestFailures+1))
			So(calls[2*foldTestFailures+1].Sub(calls[foldTestFailures+1]), ShouldBeLessThan, foldInterval)
			mu.Unlock()

			failed := logLinesContaining(readLogFile(t, logPath), `msg="`+foldFailedLogMsg+`"`)
			So(failed, ShouldHaveLength, 1)
			So(failed[0], ShouldContainSubstring, "lvl=eror")
			So(failed[0], ShouldContainSubstring, "failures=1")
			So(failed[0], ShouldContainSubstring, errFoldTestFailure.Error())

			checkArchivedAnswers(reopenLogStore(ctx, s), job, end.Add(-time.Hour), end)
		})
	})
}

func TestStoreFoldSkipsAbandonedBatch(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose archive of a job fails its sync, abandoning its wal file's tail", t, func() {
		pauseLogFolder()

		var armed atomic.Bool

		Reset(wal.SetTestSyncHook(func(path string) error {
			if strings.HasSuffix(path, walFileExt) && armed.CompareAndSwap(true, false) {
				return syscall.EIO
			}

			return nil
		}))

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold abandoned", historyTestRepGroup)
		job.ReqGroup = foldTestReqGroup
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		armed.Store(true)

		end := time.Now().Truncate(time.Second)
		job.State, job.Exited, job.StartTime, job.EndTime, job.PeakRAM = JobStateComplete, true, end, end, 300
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldWrap, syscall.EIO)

		later := testDBJob("echo fold after abandoned", historyTestRepGroup)
		_, err = s.storeNewJobsGuarded(ctx, []*Job{later}, false, nil)
		So(err, ShouldBeNil)

		Convey("a fold reads that file only up to its durable end, so folds neither the archive nor its "+
			"sample, before or after reopen", func() {
			file, _ := s.w.Durable()
			So(file, ShouldBeGreaterThan, 1)

			eachReopen(ctx, &s, func(st *logStore) {
				So(st.foldNow(), ShouldBeNil)

				complete, errc := st.checkIfComplete(job.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeFalse)
				So(inspectedCount(st, kindStat), ShouldEqual, 0)
				soLive(st, job.Key(), true)
			})
		})
	})
}

func TestStoreFoldAcrossReopen(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and a job added, folded and reopened, which starts a new wal "+
		"file after the one holding the folded position", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold across reopen", historyTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		s = reopenLogStore(ctx, s)

		s.mu.RLock()
		folded := s.state.folded
		s.mu.RUnlock()

		file, _ := s.w.Durable()
		So(folded.file, ShouldBeGreaterThan, 0)
		So(file, ShouldBeGreaterThan, folded.file)

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end.Add(-time.Minute), end)

		Convey("a fold reads on into the new file, so the archive is in history.bolt and not the overlay, before "+
			"and after another reopen", func() {
			So(s.foldNow(), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(overlayEmpty(st), ShouldBeTrue)
				So(historyBoltCompletes(st), ShouldEqual, 1)
				checkArchivedAnswers(st, job, end.Add(-time.Hour), end)
			})
		})
	})
}

func TestStoreFoldStopsAtItsDurableFile(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a job added, the log's durable end then noted, and the "+
		"store reopened, which starts a new wal file, and the job archived in it", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold stops at its durable file", historyTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		file, durEnd := s.w.Durable()
		noted := historyPos{file: file, off: uint64(durEnd)} //nolint:gosec // an offset

		s = reopenLogStore(ctx, s)

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end, end)

		later, _ := s.w.Durable()
		So(later, ShouldBeGreaterThan, file)

		Convey("a read of the log to the noted end, as a fold's whose durable end was read before the new "+
			"file was started, reads no later file: it ends there with the add's rglookup and no completion",
			func() {
				f, errf := s.readFold(noted)
				So(errf, ShouldBeNil)
				So(f, ShouldNotBeNil)
				So(f.to, ShouldResemble, noted)
				So(f.lookups, ShouldHaveLength, 1)
				So(f.complete, ShouldBeEmpty)
			})
	})
}

func TestStoreFoldDamageBeforeDurable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, folded, and then a job archived whose last frame is "+
		"damaged on disk, before the log's durable end", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo fold damaged", historyTestRepGroup)
		job.ReqGroup = foldTestReqGroup
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		s.mu.RLock()
		folded := s.state.folded
		s.mu.RUnlock()

		end := time.Now().Truncate(time.Second)
		archiveTestJob(s, job, end, end)

		file, durEnd := s.w.Durable()
		So(historyPos{file: file, off: uint64(durEnd)}.after(folded), ShouldBeTrue) //nolint:gosec // an offset

		f, err := os.OpenFile(walFilePath(dir, file), os.O_RDWR, 0)
		So(err, ShouldBeNil)

		last := make([]byte, 1)
		_, err = f.ReadAt(last, durEnd-1)
		So(err, ShouldBeNil)

		_, err = f.WriteAt([]byte{last[0] ^ 0xff}, durEnd-1)
		So(err, ShouldBeNil)
		So(f.Close(), ShouldBeNil)

		Convey("a fold fails with errStoreCorrupt and changes neither the folded position nor history.bolt", func() {
			So(s.foldNow(), ShouldWrap, errStoreCorrupt)

			s.mu.RLock()
			So(s.state.folded, ShouldResemble, folded)
			s.mu.RUnlock()

			So(historyBoltFolded(s), ShouldResemble, folded)
			So(historyBoltCompletes(s), ShouldEqual, 0)

			samples, errs := s.sampleCount()
			So(errs, ShouldBeNil)
			So(samples, ShouldEqual, 0)
		})
	})
}

// historyBoltFolded returns the folded position s's history.bolt holds.
func historyBoltFolded(s *logStore) historyPos {
	pos, _, err := s.history.load(make(map[logKey]uint64))
	So(err, ShouldBeNil)

	return pos
}

func TestStoreFoldChunks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, folds of at most 10 records a chunk, and 25 jobs added, "+
		"folded and then archived one at a time", t, func() {
		pauseLogFolder()
		logFoldChunkRecords.Store(foldTestChunkRecords)

		Reset(func() { logFoldChunkRecords.Store(0) })

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("chunks", historyTestRepGroup, foldTestChunkedJobs)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)
		s.foldStats.take()

		end := time.Now().Truncate(time.Second)
		for _, job := range jobs {
			archiveTestJob(s, job, end.Add(-time.Minute), end)
		}

		all := jobKeys(sortedByKey(jobs))

		// answersWhole asserts that rep group rg's completions are every job,
		// once each, however much is folded.
		answersWhole := func(st *logStore) {
			byRG, errr := st.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
			So(errr, ShouldBeNil)
			So(jobKeys(byRG), ShouldResemble, all)
		}

		Convey("a fold of them commits them in several transactions, the first ending at the first frame after "+
			"10 records and each moving the folded position on, with every answer whole between them, and a "+
			"crash between two leaves a prefix that reopens whole and folds the rest", func() {
			killed := filepath.Join(t.TempDir(), "killed")

			var (
				positions []historyPos
				folded    []int
			)

			committed := func() {
				positions = append(positions, historyBoltFolded(s))
				folded = append(folded, historyBoltCompletes(s))
				answersWhole(s)

				if len(positions) == 2 {
					So(copyCrashStore(dir, killed), ShouldBeNil)
				}
			}

			logFoldCommittedHook.Store(&committed)

			Reset(func() { logFoldCommittedHook.Store(nil) })

			onFoldBatch(func() { answersWhole(s) })

			So(s.foldNow(), ShouldBeNil)
			logFoldCommittedHook.Store(nil)
			logFoldBatchHook.Store(nil)

			So(len(positions), ShouldBeGreaterThanOrEqualTo, foldTestChunkedJobs*2/foldTestChunkRecords)
			So(folded[0], ShouldEqual, foldTestChunkRecords/2)

			movedOn := 0

			for i := 1; i < len(positions); i++ {
				if positions[i].after(positions[i-1]) {
					movedOn++
				}
			}

			So(movedOn, ShouldEqual, len(positions)-1)
			So(s.foldStats.take().txs, ShouldEqual, len(positions))
			So(historyBoltCompletes(s), ShouldEqual, foldTestChunkedJobs)
			So(overlayEmpty(s), ShouldBeTrue)

			reopened := openLogStoreIn(ctx, killed)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			reopened.mu.RLock()
			So(reopened.state.folded, ShouldResemble, positions[1])
			reopened.mu.RUnlock()

			prefix := historyBoltCompletes(reopened)
			So(prefix, ShouldBeGreaterThan, 0)
			So(prefix, ShouldBeLessThan, foldTestChunkedJobs)

			entries, _ := overlaySize(reopened)
			So(entries, ShouldEqual, foldTestChunkedJobs-prefix)
			answersWhole(reopened)

			So(reopened.foldNow(), ShouldBeNil)
			So(historyBoltCompletes(reopened), ShouldEqual, foldTestChunkedJobs)
			So(overlayEmpty(reopened), ShouldBeTrue)
			answersWhole(reopened)
		})
	})
}

func TestStoreFoldStopsAtClose(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store logging to a file, folds of at most 10 records a chunk, and 25 jobs added, "+
		"folded and then archived one at a time with its folder paused", t, func() {
		resume := pauseLogFolder()

		logFoldChunkRecords.Store(foldTestChunkRecords)

		Reset(func() { logFoldChunkRecords.Store(0) })

		logPath, logCtx := managerLogContext(t, ctx)
		s, dir := openTestLogStore(t, logCtx)

		jobs := testLogJobs("fold stops at close", historyTestRepGroup, foldTestChunkedJobs)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		for _, job := range jobs {
			archiveTestJob(s, job, end.Add(-time.Minute), end)
		}

		Convey("a close asked for while the resumed folder reads its first chunk of them returns once that "+
			"chunk commits, logging no failed fold, and a reopen folds the rest", func() {
			var reads, commits atomic.Int64

			entered := make(chan struct{})

			read := func() {
				if reads.Add(1) > 1 {
					return
				}

				close(entered)

				select {
				case <-s.foldStop:
				case <-time.After(foldTestWait):
				}
			}

			committed := func() { commits.Add(1) }

			logFoldReadHook.Store(&read)
			logFoldCommittedHook.Store(&committed)

			Reset(func() {
				logFoldReadHook.Store(nil)
				logFoldCommittedHook.Store(nil)
			})

			resume()

			select {
			case <-entered:
			case <-time.After(foldTestWait):
				So("the folder never folded", ShouldBeBlank)
			}

			So(s.close(ctx), ShouldBeNil)
			So(reads.Load(), ShouldEqual, 1)
			So(commits.Load(), ShouldEqual, 1)
			So(logLinesContaining(readLogFile(t, logPath), `msg="`+foldFailedLogMsg+`"`), ShouldBeEmpty)

			logFoldReadHook.Store(nil)
			logFoldCommittedHook.Store(nil)
			pauseLogFolder()

			reopened := openLogStoreIn(ctx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			So(historyBoltCompletes(reopened), ShouldEqual, foldTestChunkRecords/2)

			So(reopened.foldNow(), ShouldBeNil)
			So(historyBoltCompletes(reopened), ShouldEqual, foldTestChunkedJobs)
			So(overlayEmpty(reopened), ShouldBeTrue)

			byRG, errr := reopened.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
			So(errr, ShouldBeNil)
			So(jobKeys(byRG), ShouldResemble, jobKeys(sortedByKey(jobs)))
		})
	})
}

func TestStoreFoldReportedAtClose(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, logging to a file, and a job added and folded", t, func() {
		pauseLogFolder()

		logPath, logCtx := managerLogContext(t, ctx)
		s, _ := openTestLogStore(t, logCtx)

		_, err := s.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo fold reported at close", historyTestRepGroup)},
			false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		folds := func() []string { return storeStatsWriterLines(readLogFile(t, logPath), foldWriterName) }

		Convey("the fold, made in a report interval that has not ended, is reported as writer=fold at close",
			func() {
				So(folds(), ShouldBeEmpty)
				So(s.close(ctx), ShouldBeNil)

				lines := folds()
				So(lines, ShouldHaveLength, 1)
				So(logLineValue(lines[0], "txs"), ShouldEqual, "1")
				So(logLineValue(lines[0], "items"), ShouldEqual, "1")
			})
	})
}

func TestStoreFoldRefoldAddsNoSample(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a job of req group q1 added and folded, archived and "+
		"folded, then added again under req group q2", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold refold", historyTestRepGroup)
		job.ReqGroup = foldSpecReqGroups[0]
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		s.mu.RLock()
		afterAdd := s.state.folded
		s.mu.RUnlock()

		end := time.Now().Truncate(time.Second)
		job.State, job.Exited, job.StartTime, job.EndTime, job.PeakRAM = JobStateComplete, true, end, end,
			foldTestMBRound
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		again := testDBJob(job.Cmd, historyTestRepGroup)
		again.ReqGroup = foldSpecReqGroups[1]
		_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
		So(err, ShouldBeNil)

		samples, err := s.sampleCount()
		So(err, ShouldBeNil)
		So(samples, ShouldBeGreaterThan, 0)

		Convey("a fold that reads the archive again, as one from before the folded position would, adds no "+
			"sample, to q2 or anywhere", func() {
			s.mu.Lock()
			s.state.folded = afterAdd
			s.mu.Unlock()

			So(s.foldNow(), ShouldBeNil)

			refolded, errs := s.sampleCount()
			So(errs, ShouldBeNil)
			So(refolded, ShouldEqual, samples)
			So(s.refreshReqGroupRecs(foldSpecReqGroups[1]).RAM, ShouldEqual, 0)
			So(s.refreshReqGroupRecs(foldSpecReqGroups[0]).RAM, ShouldEqual, foldTestMBRound)
		})
	})
}

func TestStoreFoldExitStatsObserved(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and a job of req group q added and folded", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo fold exitstat", historyTestRepGroup)
		job.ReqGroup = foldTestReqGroup
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)
		s.foldStats.take()

		Convey("a fold of nothing but its release's exitstat sample is one db writer transaction of 1 item", func() {
			end := time.Now().Truncate(time.Second)
			job.State, job.Exited, job.Exitcode, job.FailReason = JobStateDelayed, true, 1, FailReasonRAM
			job.PeakRAM, job.StartTime, job.EndTime = foldTestMBRound, end.Add(-time.Minute), end
			So(s.updateJobAfterExitDurable(job, nil, nil, false), ShouldBeNil)

			So(s.foldNow(), ShouldBeNil)

			folds := s.foldStats.take()
			So(folds.txs, ShouldEqual, 1)
			So(folds.items, ShouldEqual, 1)
			So(inspectedCount(s, kindStat), ShouldBeGreaterThan, 0)
		})
	})
}

// foldWorkloadPutBack is workload (a): archive then storeLiveForRerun.
func foldWorkloadPutBack(ctx context.Context, s store) {
	job := testDBJob("echo fold workload a", "wa")
	foldWorkloadAdd(ctx, s, job)
	foldWorkloadArchive(ctx, s, job, 0, archiveRemovedLive)

	job.State = JobStateReady
	So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)
	foldWorkloadProbe(ctx, s, job)
}

// foldWorkloadKeepLive is workload (b): a keep-live archive, its rerun mark set
// while running.
func foldWorkloadKeepLive(ctx context.Context, s store) {
	job := testDBJob("echo fold workload b", "wb")
	foldWorkloadAdd(ctx, s, job)
	foldWorkloadStart(s, job, 0)

	job.RerunAfterRun = true
	So(s.storeRunningRerunMarks([]*Job{job}), ShouldBeNil)
	foldWorkloadProbe(ctx, s, job)

	foldWorkloadArchive(ctx, s, job, 0, archiveKeptLive)
}

// foldWorkloadResurrect is workload (c): archive a member of dep group g and a
// job depending on g, add a new member to g, which puts the dependent back,
// and archive it again.
func foldWorkloadResurrect(ctx context.Context, s store) {
	member := testDBJob("echo fold workload c member", "wc")
	member.DepGroups = []string{"g"}
	dependent := testDBJob("echo fold workload c dependent", "wc")
	dependent.Dependencies = Dependencies{NewDepGroupDependency("g")}

	foldWorkloadAdd(ctx, s, member, dependent)
	foldWorkloadArchive(ctx, s, member, 0, archiveRemovedLive)
	foldWorkloadArchive(ctx, s, dependent, 1, archiveRemovedLive, member)

	added := testDBJob("echo fold workload c new member", "wc")
	added.DepGroups = []string{"g"}

	stored, err := s.storeNewJobsGuarded(ctx, []*Job{added}, false, nil)
	So(err, ShouldBeNil)
	So(jobKeys(stored.jobsToQueue), ShouldContain, dependent.Key())
	foldWorkloadProbe(ctx, s, member, dependent, added)

	foldWorkloadArchive(ctx, s, dependent, 2, archiveRemovedLive, member, added)
}

// foldWorkloadDeleteReadd is workload (d): archive, put back, deleteLiveJobs
// and re-add.
func foldWorkloadDeleteReadd(ctx context.Context, s store) {
	job := testDBJob("echo fold workload d", "wd")
	foldWorkloadAdd(ctx, s, job)
	foldWorkloadArchive(ctx, s, job, 0, archiveRemovedLive)

	job.State = JobStateReady
	So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)
	foldWorkloadProbe(ctx, s, job)

	So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)
	foldWorkloadProbe(ctx, s, job)

	_, err := s.storeNewJobsGuarded(ctx, []*Job{foldWorkloadCopy(job)}, false, nil)
	So(err, ShouldBeNil)
	foldWorkloadProbe(ctx, s, job)
}

// foldWorkloadCopy returns a new job with job's spec, as a user adding it
// again makes it.
func foldWorkloadCopy(job *Job) *Job {
	copied := testDBJob(job.Cmd, job.RepGroup)
	copied.DepGroups = slices.Clone(job.DepGroups)
	copied.Dependencies = job.Dependencies

	return copied
}

// foldWorkloadAdd adds jobs to s, then probes.
func foldWorkloadAdd(ctx context.Context, s store, jobs ...*Job) {
	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)
	foldWorkloadProbe(ctx, s, jobs...)
}

// foldWorkloadArchive runs job to completion as the i'th run of its workload,
// asserting the archive's outcome, then probes it and others.
func foldWorkloadArchive(ctx context.Context, s store, job *Job, i int, want archiveOutcome, others ...*Job) {
	foldWorkloadStart(s, job, i)

	job.State, job.Exited = JobStateComplete, true
	job.EndTime = job.StartTime.Add(time.Second)
	job.PeakRAM = 10 * (i + 1)

	outcome, err := s.archiveCompletion(job.Key(), job)
	So(err, ShouldBeNil)
	So(outcome, ShouldEqual, want)

	foldWorkloadProbe(ctx, s, append([]*Job{job}, others...)...)
}

// foldWorkloadStart reserves and starts job, the i'th run of its workload.
func foldWorkloadStart(s store, job *Job, i int) {
	job.State = JobStateReserved
	So(s.updateJobRunStateDurable(job), ShouldBeNil)

	job.State, job.Host = JobStateRunning, "fold-host"
	job.StartTime = foldWorkloadBase.Add(time.Duration(i) * time.Minute)
	So(s.updateJobRunStateDurable(job), ShouldBeNil)
}

// foldWorkloadProbe makes every history query of jobs and their rep groups,
// then re-adds them with ignoreAdded true, as D1 acceptance test 7 does after
// each step.
func foldWorkloadProbe(ctx context.Context, s store, jobs ...*Job) {
	keys := jobKeys(jobs)
	rgs := make([]string, 0, len(jobs))

	for _, job := range jobs {
		rgs = append(rgs, job.RepGroup)

		_, err := s.checkIfComplete(job.Key())
		So(err, ShouldBeNil)
	}

	_, err := s.retrieveCompleteJobsByKeys(keys)
	So(err, ShouldBeNil)

	for _, rg := range slices.Compact(slices.Sorted(slices.Values(rgs))) {
		foldWorkloadProbeRepGroup(s, rg)
	}

	_, err = s.retrieveCompleteJobsRecent(foldWorkloadBase)
	So(err, ShouldBeNil)

	_, err = s.retrieveLastCompletionTimeByRepGroup(rgs)
	So(err, ShouldBeNil)

	_, err = s.retrieveRepGroups()
	So(err, ShouldBeNil)

	readd := make([]*Job, len(jobs))
	for i, job := range jobs {
		readd[i] = foldWorkloadCopy(job)
	}

	_, err = s.storeNewJobsGuarded(ctx, readd, true, nil)
	So(err, ShouldBeNil)
}

// foldWorkloadProbeRepGroup makes every history query of rep group rg.
func foldWorkloadProbeRepGroup(s store, rg string) {
	_, err := s.retrieveCompleteJobsByRepGroup(rg)
	So(err, ShouldBeNil)

	_, err = s.retrieveOldestCompleteJobsByRepGroup(rg, allCompleteJobsQuery(10))
	So(err, ShouldBeNil)

	for _, details := range []bool{false, true} {
		_, err = s.retrieveCompleteJobStatusByRepGroup(rg, details)
		So(err, ShouldBeNil)
	}

	s.repGroupHasHistory(rg)
	So(s.spendArchivedBytesByRepGroup(rg, newArchivedBytesBudget()), ShouldBeNil)
}

func TestStoreFoldFailuresLogged(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, logging to a file", t, func() {
		pauseLogFolder()

		logPath, logCtx := managerLogContext(t, ctx)
		s, _ := openTestLogStore(t, logCtx)

		failed := func() []string {
			return logLinesContaining(readLogFile(t, logPath), `msg="`+foldFailedLogMsg+`"`)
		}

		Convey("failed folds are logged at most once a minute, each line counting the failures since the last",
			func() {
				for range foldTestFailures {
					s.foldFailed(errFoldTestFailure)
				}

				lines := failed()
				So(lines, ShouldHaveLength, 1)
				So(lines[0], ShouldContainSubstring, "failures=1")

				for _, want := range []string{"failures=3", "failures=1"} {
					s.foldFailLogged = time.Now().Add(-foldFailedLogEvery)
					s.foldFailed(errFoldTestFailure)

					lines = failed()
					So(lines[len(lines)-1], ShouldContainSubstring, want)
				}

				So(lines, ShouldHaveLength, 3)
			})
	})
}

// foldWorkloadReaddElsewhere, beyond D1 acceptance test 7's four, archives a
// job and adds it again under another rep group, whose spec the history entry
// must not take, then archives it again.
func foldWorkloadReaddElsewhere(ctx context.Context, s store) {
	job := testDBJob("echo fold workload e", "we")
	foldWorkloadAdd(ctx, s, job)
	foldWorkloadArchive(ctx, s, job, 0, archiveRemovedLive)

	elsewhere := testDBJob(job.Cmd, "we2")
	foldWorkloadAdd(ctx, s, elsewhere)
	foldWorkloadProbe(ctx, s, job, elsewhere)

	foldWorkloadArchive(ctx, s, elsewhere, 1, archiveRemovedLive, job)
}
