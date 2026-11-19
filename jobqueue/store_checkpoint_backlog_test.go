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
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// backlogTestLoad is the least time TestLogStoreCheckpointWhileFoldLags
	// adds and archives jobs faster than they fold, until it has seen
	// backlogTestCuts checkpoints or backlogTestMaxLoad has passed: the
	// checkpointer looks every checkpointPoll, so it makes one about that
	// often, less often on a loaded host.
	backlogTestLoad    = 3 * time.Second
	backlogTestMaxLoad = time.Minute
	backlogTestCuts    = 3

	// backlogTestFoldTx is how long each fold transaction takes in it, so
	// that with each holding at most a batch of records the folder folds far
	// fewer records a second than are written.
	backlogTestFoldTx = 500 * time.Millisecond

	// backlogTestEvery is how often checkpoints are due in it.
	backlogTestEvery = 100 * time.Millisecond
)

// checkpointHeldTimeout bounds the wait for a checkpoint checkpointWhileHeld
// started to end once the reader it waited for has released its hold: far
// longer than a checkpoint of a test's few thousand records takes.
const checkpointHeldTimeout = time.Minute

// errCheckpointHeldTimeout is the error of a checkpoint checkpointWhileHeld
// started that did not end within checkpointHeldTimeout.
var errCheckpointHeldTimeout = errors.New("checkpoint did not end")

func TestLogStoreCheckpointWhileFoldLags(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store of small wal files with checkpoints due every 100ms, whose every fold "+
		"transaction holds at most a batch of records and takes 500ms", t, func() {
		smallWALSegments()
		logCheckpointEvery.Store(int64(backlogTestEvery))
		logFoldTxBytes.Store(1)

		slow := func() error { //nolint:unparam // logFoldTxHook's type
			time.Sleep(backlogTestFoldTx)

			return nil
		}

		logFoldTxHook.Store(&slow)

		Reset(func() {
			logCheckpointEvery.Store(0)
			logFoldTxBytes.Store(0)
			logFoldTxHook.Store(nil)
		})

		s, dir := openTestLogStore(t, ctx)

		Convey("while jobs are added and archived faster than they fold, checkpoints keep completing and "+
			"delete the wal below their cuts; a reopen rebuilds the same state, and once everything folds, "+
			"every archived job is complete", func() {
			end := time.Now().Truncate(time.Second)
			cuts := make(map[uint32]bool)

			var all, archived []*Job //nolint:prealloc // how many are added depends on the machine's speed

			began := time.Now()

			loading := func() bool {
				took := time.Since(began)

				return took < backlogTestLoad || (len(cuts) < backlogTestCuts && took < backlogTestMaxLoad)
			}

			var errs []error

			for i := 0; loading() && len(errs) == 0; i++ {
				jobs := testLogJobs(fmt.Sprintf("ckpt lag %d", i), fmt.Sprintf("ckptlag%d", i%3), 50)
				_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
				errs = append(errs, err, archiveConcurrently(s, jobs[:25], end))

				all, archived = append(all, jobs...), append(archived, jobs[:25]...)

				numbers, err := checkpointNumbers(dir)
				errs = append(errs, err)

				for _, n := range numbers {
					cuts[n] = true
				}

				errs = slices.DeleteFunc(errs, func(err error) bool { return err == nil })
			}

			So(errs, ShouldBeEmpty)

			files, err := walFileNumbers(dir)
			So(err, ShouldBeNil)

			seen := slices.Sorted(maps.Keys(cuts))

			So(len(seen), ShouldBeGreaterThanOrEqualTo, backlogTestCuts)

			// The checkpoint before the newest one seen had ended, and so
			// deleted the wal files before its cut, before the newest began.
			So(files[0], ShouldBeGreaterThanOrEqualTo, seen[len(seen)-2])

			pauseLogFolder()
			logFoldTxHook.Store(nil)
			logCheckpointEvery.Store(int64(time.Hour))

			s.foldMu.Lock()
			s.foldMu.Unlock() //nolint:staticcheck // waits for a fold in flight

			So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)

			before := foldCrashAnswers(ctx, s, all)
			reopened := soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			So(foldCrashAnswers(ctx, reopened, all), ShouldEqual, before)
			So(liveViews(reopened), ShouldResemble, liveViews(s))

			So(s.foldNow(), ShouldBeNil)
			So(overlayEmpty(s), ShouldBeTrue)
			So(foldCrashAnswers(ctx, s, all), ShouldEqual, before)

			var incomplete []string

			for _, job := range archived {
				if complete, errc := s.checkIfComplete(job.Key()); errc != nil || !complete {
					incomplete = append(incomplete, job.Key())
				}
			}

			So(incomplete, ShouldBeEmpty)
		})
	})
}

func TestLogStoreCheckpointBacklogUnappliedModify(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a live job of rep group orig whose modify is written after "+
		"a later spec of it and its rglookup under rep group elsewhere, so that the modify does not apply, and "+
		"the job then archived", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo backlog unapplied modify", "orig")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		k, err := parseLogKey(job.Key())
		So(err, ShouldBeNil)

		moved := modifiedCopy(job, "echo backlog unapplied modify moved")
		k2, err := parseLogKey(moved.Key())
		So(err, ShouldBeNil)

		var earlier, later logFrames

		So(s.addModifies(&earlier, []logModify{{old: k, new: k2, job: moved}}), ShouldBeNil)
		So(s.addSpecAndState(&later, job, k), ShouldBeNil)
		s.addRGLookupRecord(&later, "elsewhere", k)
		writeInReverse(s, &earlier, &later)

		archiveTestJob(s, job, time.Now().Add(-time.Minute), time.Now())

		elsewhere := func(st *logStore) []string {
			got, errr := st.retrieveCompleteJobsByRepGroup("elsewhere")
			So(errr, ShouldBeNil)

			keys := make([]string, len(got))
			for i, j := range got {
				keys[i] = j.Key()
			}

			return keys
		}

		So(elsewhere(s), ShouldResemble, []string{job.Key()})

		Convey("a checkpoint carrying the modify in its backlog leaves the job complete under rep group "+
			"elsewhere, in memory and after a reopen", func() {
			checkpointed(s)
			So(elsewhere(s), ShouldResemble, []string{job.Key()})

			reopened := soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			So(elsewhere(reopened), ShouldResemble, []string{job.Key()})
		})
	})
}

func TestLogStoreCheckpointBacklogFoldedModifyNotUnlooked(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a job of rep group orig modified to a new command, then "+
		"its original command added again under rep group back and archived, and a checkpoint carrying "+
		"those records in its backlog", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo backlog folded modify", "orig")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		moved := modifiedCopy(job, "echo backlog folded modify moved")
		So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}), ShouldBeNil)

		back := testDBJob("echo backlog folded modify", "back")
		So(back.Key(), ShouldEqual, job.Key())

		_, err = s.storeNewJobsGuarded(ctx, []*Job{back}, false, nil)
		So(err, ShouldBeNil)

		archiveTestJob(s, back, time.Now().Add(-time.Minute), time.Now())

		checkpointed(s)

		Convey("once history.bolt has folded them, a reopen still answers the job complete under rep group "+
			"back, as the modify it folded hides none of the key's later rglookups", func() {
			So(s.foldNow(), ShouldBeNil)

			reopened := reopenLogStore(ctx, s)

			got, err := reopened.retrieveCompleteJobsByRepGroup("back")
			So(err, ShouldBeNil)
			So(jobKeys(got), ShouldResemble, []string{back.Key()})
		})
	})
}

func TestLogStoreCheckpointBacklogModifyOfNonLiveKey(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a job of rep group orig archived and then modified to a "+
		"new command, a modify of a key that is not live, which the reducer applies", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo backlog non-live modify", "orig")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		archiveTestJob(s, job, time.Now().Add(-time.Minute), time.Now())

		moved := modifiedCopy(job, "echo backlog non-live modify moved")
		So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}), ShouldBeNil)

		jobs := []*Job{job, moved}
		before := foldCrashAnswers(ctx, s, jobs)

		Convey("a checkpoint carrying the modify in its backlog leaves every answer as it was, in memory, "+
			"after a reopen, and once each has folded", func() {
			checkpointed(s)
			So(foldCrashAnswers(ctx, s, jobs), ShouldEqual, before)

			reopened := soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			So(foldCrashAnswers(ctx, reopened, jobs), ShouldEqual, before)

			So(s.foldNow(), ShouldBeNil)
			So(foldCrashAnswers(ctx, s, jobs), ShouldEqual, before)

			So(reopened.foldNow(), ShouldBeNil)
			So(foldCrashAnswers(ctx, reopened, jobs), ShouldEqual, before)
		})
	})
}

func TestLogStoreCheckpointBacklogEmptyOnceFolded(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, 100 jobs added, archived and looked up under another "+
		"rep group, a checkpoint carrying those records in its backlog, and then everything folded", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("backlog empty once folded", "folded", 100)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(archiveConcurrently(s, jobs, time.Now().Truncate(time.Second)), ShouldBeNil)

		others := make([]*Job, len(jobs))
		for i, job := range jobs {
			others[i] = testDBJob(job.Cmd, "foldedother")
		}

		So(s.storeRepGroupLookups(ctx, others), ShouldBeNil)

		for s.writing.Load() != 0 {
			time.Sleep(logStoreFlushPoll)
		}

		checkpointed(s)
		So(checkpointBacklogLen(dir), ShouldBeGreaterThanOrEqualTo, 2*len(jobs))

		So(s.foldNow(), ShouldBeNil)

		// Without the backlog's own positions, a checkpoint would keep the
		// records it carried over from the previous one however far they
		// had folded, and each checkpoint's backlog would grow without bound.
		Convey("the next two checkpoints carry no backlog, as nothing before their cuts is left to fold", func() {
			for range 2 {
				checkpointed(s)
				So(checkpointBacklogLen(dir), ShouldEqual, 0)
			}
		})
	})
}

// checkpointBacklogLen returns how many backlog headers the newest checkpoint
// in dir holds: one per record of its backlog, and one per spec it copies for
// them.
func checkpointBacklogLen(dir string) int {
	_, _, offsets, f, err := newestCheckpoint(dir)
	So(err, ShouldBeNil)
	So(f, ShouldNotBeNil)

	defer f.Close()

	n := 0

	for _, off := range offsets {
		err = wal.ReadFrame(f, off, func(_ wal.Ref, rec []byte) error {
			_, ok, errh := backlogHeaderOf(rec)
			if ok {
				n++
			}

			return errh
		})
		So(err, ShouldBeNil)
	}

	return n
}

func TestLogStoreCheckpointBacklogLikeTheWAL(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given two log stores with their folders paused, each given, unfolded, completions, releases, a bury "+
		"and a modify in req group q, D1 test 7's workloads, a re-add and second archive of a completed job, "+
		"and a re-add of a live one under another rep group", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)
		twin, _ := openTestLogStore(t, ctx)

		end := time.Now().Truncate(time.Second)
		jobs := backlogSteps(ctx, s, end)
		backlogSteps(ctx, twin, end)

		Convey("checkpoints of one, the second merging the first's backlog, each leave the in-memory state a "+
			"reopen rebuilds, and once folded it answers and has learned as the other, folded with no "+
			"checkpoint", func() {
			for range 2 {
				cut := checkpointed(s)
				So(walFilesBelow(dir, cut), ShouldBeEmpty)
				soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			}

			So(s.foldNow(), ShouldBeNil)
			So(overlayEmpty(s), ShouldBeTrue)
			So(twin.foldNow(), ShouldBeNil)

			So(backlogAnswers(ctx, s, jobs), ShouldResemble, backlogAnswers(ctx, twin, jobs))

			want := backlogAnswers(ctx, twin, jobs)

			eachReopen(ctx, &s, func(st *logStore) {
				So(backlogAnswers(ctx, st, jobs), ShouldResemble, want)
			})
		})
	})
}

// backlogSteps makes, in s, never folding: D1 test 3's completions, releases,
// bury and modify in req group q (as learnedSteps, but for its flush); D1 test
// 7's workloads (a) to (d), of put-back, keep-live, resurrecting and
// re-added archives; a job of rep group again archived at end-1m, added again
// and archived at end; and a job of rep group live added again under rep
// group elsewhere. It returns every job, as last modified.
func backlogSteps(ctx context.Context, s *logStore, end time.Time) []*Job {
	jobs := backlogLearnedSteps(ctx, s, end)

	for _, workload := range []func(context.Context, store){foldWorkloadPutBack, foldWorkloadKeepLive,
		foldWorkloadResurrect, foldWorkloadDeleteReadd} {
		workload(ctx, s)
	}

	again := testDBJob("echo backlog again", "again")
	_, err := s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
	So(err, ShouldBeNil)

	archiveTestJob(s, again, end.Add(-2*time.Minute), end.Add(-time.Minute))

	_, err = s.storeNewJobsGuarded(ctx, []*Job{testDBJob(again.Cmd, "again")}, true, nil)
	So(err, ShouldBeNil)

	archiveTestJob(s, again, end.Add(-time.Minute), end)

	live := testDBJob("echo backlog live", "live")
	_, err = s.storeNewJobsGuarded(ctx, []*Job{live}, false, nil)
	So(err, ShouldBeNil)
	So(s.storeRepGroupLookups(ctx, []*Job{testDBJob(live.Cmd, "elsewhere")}), ShouldBeNil)

	for s.writing.Load() != 0 {
		time.Sleep(logStoreFlushPoll)
	}

	return append(jobs, again, live)
}

// backlogLearnedSteps makes D1 test 3's steps in s, as learnedSteps does but
// for its flush, returning the jobs, the modified one as modified.
func backlogLearnedSteps(ctx context.Context, s *logStore, end time.Time) []*Job {
	jobs := testLogJobs("backlog learned", "learned", 7)
	for _, job := range jobs {
		job.ReqGroup = foldTestReqGroup
	}

	jobs[6].Requirements.RAM = 100

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	for i, peakRAM := range []int{100, 100, 200} {
		jobs[i].State, jobs[i].Exited, jobs[i].PeakRAM = JobStateComplete, true, peakRAM
		jobs[i].StartTime, jobs[i].EndTime = end.Add(-time.Duration(i+1)*time.Minute), end
		_, err = s.archiveCompletion(jobs[i].Key(), jobs[i])
		So(err, ShouldBeNil)
	}

	for i, reason := range []string{FailReasonRAM, FailReasonTime, FailReasonDisk, FailReasonExit} {
		job := jobs[3+i]
		job.State, job.Exited, job.Exitcode, job.FailReason = JobStateDelayed, true, 1, reason
		job.PeakRAM, job.PeakDisk, job.StartTime, job.EndTime = 300*(i+1), int64(50*i), end.Add(-time.Hour), end
		So(s.updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false), ShouldBeNil)

		job.State = JobStateReady
		So(s.updateJobAfterChangeDurable(job), ShouldBeNil)
	}

	moved := modifiedCopy(jobs[3], "echo backlog learned modified")
	moved.ReqGroup, moved.FailReason, moved.PeakRAM = foldTestReqGroup, jobs[3].FailReason, jobs[3].PeakRAM
	So(s.modifyLiveJobs(ctx, []string{jobs[3].Key()}, []*Job{moved}), ShouldBeNil)

	return append(jobs, moved)
}

// backlogAnswers describes what s answers about jobs (see foldCrashAnswers),
// whether each rep group has history, and what it learned of req group q.
func backlogAnswers(ctx context.Context, s *logStore, jobs []*Job) string {
	s.setRecRounding(foldTestSecRound, foldTestMBRound)

	rec := s.refreshReqGroupRecs(foldTestReqGroup)
	So(rec, ShouldNotBeNil)

	var b strings.Builder

	b.WriteString(foldCrashAnswers(ctx, s, jobs))
	fmt.Fprintf(&b, "rec=%+v samples=%d\n", *rec, inspectedCount(s, kindStat))

	for _, rg := range []string{"learned", "again", "live", "elsewhere"} {
		fmt.Fprintf(&b, "history[%s]=%v\n", rg, s.repGroupHasHistory(rg))
	}

	return b.String()
}

func TestLogStoreCheckpointBacklogFramesSpanBlocks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, chunk := range []int64{1, 7, 333, 1000} {
		Convey(fmt.Sprintf("Given a log store with its folder paused, 3 adds and archives of 1,000 jobs and one "+
			"rglookup write of all 3,000 under 37 other rep groups, so that frames span the 64KiB blocks of a "+
			"checkpoint's backlog, checkpointed, then folded %d records a chunk", chunk), t, func() {
			pauseLogFolder()

			s, _ := openTestLogStore(t, ctx)
			end := time.Now().Truncate(time.Second)

			var all []*Job

			for b := range 3 {
				jobs := testLogJobs(fmt.Sprintf("span %d", b), "span", 1000)
				_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
				So(err, ShouldBeNil)
				So(archiveConcurrently(s, jobs, end), ShouldBeNil)

				all = append(all, jobs...)
			}

			others := make([]*Job, len(all))
			for i, job := range all {
				others[i] = testDBJob(job.Cmd, fmt.Sprintf("spanother%d", i%37))
			}

			So(s.storeRepGroupLookups(ctx, others), ShouldBeNil)

			for s.writing.Load() != 0 {
				time.Sleep(logStoreFlushPoll)
			}

			checkpointed(s)

			logFoldChunkRecords.Store(chunk)
			Reset(func() { logFoldChunkRecords.Store(0) })

			So(s.foldNow(), ShouldBeNil)

			want := []int{len(all), len(all), 0}
			So(spanAnswers(s, all), ShouldResemble, want)

			Convey("a second checkpoint and a reopen still answer every completion and rglookup", func() {
				checkpointed(s)
				So(spanAnswers(s, all), ShouldResemble, want)

				s = reopenLogStore(ctx, s)
				So(spanAnswers(s, all), ShouldResemble, want)
			})
		})
	}
}

// spanAnswers returns how many of TestLogStoreCheckpointBacklogFramesSpanBlocks'
// jobs s answers as complete under rep group span, how many under its 37
// other rep groups, and how many of all it does not answer complete.
func spanAnswers(s *logStore, all []*Job) []int {
	got, err := s.retrieveCompleteJobsByRepGroup("span")
	So(err, ShouldBeNil)

	others := 0

	for i := range 37 {
		g, errg := s.retrieveCompleteJobsByRepGroup(fmt.Sprintf("spanother%d", i))
		So(errg, ShouldBeNil)

		others += len(g)
	}

	incomplete := 0

	for _, job := range all {
		if complete, errc := s.checkIfComplete(job.Key()); errc != nil || !complete {
			incomplete++
		}
	}

	return []int{len(got), others, incomplete}
}

func TestLogStoreCheckpointDuringFold(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, 2,000 jobs added and archived, and a checkpoint carrying "+
		"their records in its backlog", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)
		end := time.Now().Truncate(time.Second)

		var all []*Job

		for b := range 2 {
			jobs := testLogJobs(fmt.Sprintf("during %d", b), "during", 1000)
			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)
			So(archiveConcurrently(s, jobs, end), ShouldBeNil)

			all = append(all, jobs...)
		}

		for s.writing.Load() != 0 {
			time.Sleep(logStoreFlushPoll)
		}

		first := checkpointed(s)

		var (
			ckptErr error
			ran     bool
		)

		soFoldedWhole := func() {
			So(ran, ShouldBeTrue)
			So(ckptErr, ShouldBeNil)
			So(spanAnswersOf(s, all, "during"), ShouldResemble, []int{len(all), 0})
			So(overlayEmpty(s), ShouldBeTrue)

			reopened := soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			So(spanAnswersOf(reopened, all, "during"), ShouldResemble, []int{len(all), 0})
		}

		Convey("a checkpoint made while the fold reads that backlog, between its blocks, waits for the fold "+
			"before deleting the backlog's checkpoint, so the fold reads it whole", func() {
			var (
				once sync.Once
				done <-chan error
			)

			hook := func() {
				once.Do(func() {
					ran = true
					done = checkpointWhileHeld(s)
				})
			}

			logFoldBacklogBlockHook.Store(&hook)
			Reset(func() { logFoldBacklogBlockHook.Store(nil) })

			So(s.foldNow(), ShouldBeNil)
			So(done, ShouldNotBeNil)

			select {
			case ckptErr = <-done:
			case <-time.After(checkpointHeldTimeout):
				ckptErr = errCheckpointHeldTimeout
			}

			So(s.checkpointFrom(), ShouldBeGreaterThan, first)
			soFoldedWhole()
		})

		Convey("a checkpoint made while a fold has moved the folded position but not yet dropped what it "+
			"folded from the overlay completes, and leaves a state a reopen rebuilds", func() {
			var once sync.Once

			hook := func() {
				once.Do(func() {
					ran = true
					ckptErr = s.checkpoint()
				})
			}

			logFoldBatchHook.Store(&hook)
			Reset(func() { logFoldBatchHook.Store(nil) })

			So(s.foldNow(), ShouldBeNil)
			soFoldedWhole()
		})

		Convey("checkpoints made after every batch of a fold's changes, while it drops what it folded from "+
			"the overlay, all complete, and leave a state a reopen rebuilds", func() {
			var calls, failed int

			hook := func() {
				calls++

				if err := s.checkpoint(); err != nil {
					failed++
					ckptErr = err
				}
			}

			logFoldBatchHook.Store(&hook)
			Reset(func() { logFoldBatchHook.Store(nil) })

			So(s.foldNow(), ShouldBeNil)
			So(calls, ShouldBeGreaterThan, 2)
			So(failed, ShouldEqual, 0)

			ran = true

			soFoldedWhole()
		})
	})
}

// spanAnswersOf returns how many of jobs s answers as complete under rep group
// repGroup, and how many it does not answer complete.
func spanAnswersOf(s *logStore, jobs []*Job, repGroup string) []int {
	got, err := s.retrieveCompleteJobsByRepGroup(repGroup)
	So(err, ShouldBeNil)

	incomplete := 0

	for _, job := range jobs {
		if complete, errc := s.checkIfComplete(job.Key()); errc != nil || !complete {
			incomplete++
		}
	}

	return []int{len(got), incomplete}
}

// checkpointWhileHeld makes a checkpoint of s in another goroutine, called by
// a reader holding Refs (see holdRefs), and returns once that checkpoint has
// ended or is waiting for the reader to release its hold before it deletes
// what that reader may read, returning a channel that receives the
// checkpoint's error once it ends.
func checkpointWhileHeld(s *logStore) <-chan error {
	awaiting := make(chan uint64, 1)

	await := func() {
		s.mu.RLock()
		old := s.refGen - 1
		s.mu.RUnlock()

		select {
		case awaiting <- old:
		default:
		}
	}

	logAwaitReadersHook.Store(&await)
	defer logAwaitReadersHook.Store(nil)

	done := make(chan error, 1)

	go func() { done <- s.checkpoint() }()

	var old uint64

	select {
	case err := <-done:
		return ended(err)
	case old = <-awaiting:
	}

	for s.refHeld[old&1].Load() == 0 {
		select {
		case err := <-done:
			return ended(err)
		case <-time.After(checkpointWaitPoll):
		}
	}

	return done
}

// ended returns a channel holding err.
func ended(err error) <-chan error {
	ch := make(chan error, 1)
	ch <- err

	return ch
}
