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

// Tests of the log store's checkpoints (spec C6).

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// ckptTestSegmentSize is the wal segment size of tests wanting a log of
	// several files.
	ckptTestSegmentSize = 64 << 10

	// ckptTestWait is how long a test lets a checkpoint run before it asserts
	// that the checkpoint is still waiting.
	ckptTestWait = 200 * time.Millisecond

	// ckptTestTimeout bounds every wait for a checkpoint or a fold a test
	// started, so a missed signal fails the test instead of hanging it.
	ckptTestTimeout = 30 * time.Second

	// ckptTestGrownJobs is how many jobs growHistory folds into the history.
	ckptTestGrownJobs = 3000
)

var (
	// errCkptTestKilled is the error of a rename hook standing in for a
	// crash.
	errCkptTestKilled = errors.New("checkpoint killed")

	// errCkptTestWrite is the error of a write hook failing a checkpoint's
	// write.
	errCkptTestWrite = errors.New("checkpoint write failed")

	// errCkptTestFold is the error of a fold hook failing every fold.
	errCkptTestFold = errors.New("fold failed")

	// errCkptTestRead is a reader's error when it read other records than
	// those it was to.
	errCkptTestRead = errors.New("read other records than expected")
)

func TestLogStoreRecoverMissingWAL(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given an open log store whose live jobs' records, one's std among them, are in wal file 1 of 2", t,
		func() {
			pauseLogFolder()

			s, dir := openTestLogStore(t, ctx)

			jobs := testLogJobs("c5 missing", "c5", 20)
			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)

			jobs[0].State, jobs[0].Exited, jobs[0].Exitcode, jobs[0].Attempts = JobStateBuried, true, 1, 1
			So(s.updateJobAfterExitDurable(jobs[0], []byte("o"), []byte("e"), false), ShouldBeNil)

			s = reopenLogStore(ctx, s)

			files, err := walFileNumbers(dir)
			So(err, ShouldBeNil)
			So(files, ShouldHaveLength, 2)

			Convey("deleting wal file 1 makes recovering the live jobs fail with errStoreCorrupt", func() {
				So(os.Remove(walFilePath(dir, files[0])), ShouldBeNil)

				_, _, errr := s.recoverIncompleteJobs()
				So(errr, ShouldWrap, errStoreCorrupt)
			})

			Convey("deleting wal file 1 makes reading the job's std fail", func() {
				So(os.Remove(walFilePath(dir, files[0])), ShouldBeNil)

				k, errk := parseLogKey(jobs[0].Key())
				So(errk, ShouldBeNil)

				_, errr := s.readStd(k)
				So(errr, ShouldWrap, os.ErrNotExist)
			})

			Convey("the recovered live jobs are in key order", func() {
				recovered, _, errr := s.recoverIncompleteJobs()
				So(errr, ShouldBeNil)
				So(jobKeys(recovered), ShouldResemble, jobKeys(sortedByKey(jobs)))
			})
		})
}

func TestLogStoreReplayDiffersOnlyInAnswers(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		var once sync.Once

		duringFold := func(fn func()) {
			hook := func() { once.Do(fn) }
			logFoldReadHook.Store(&hook)

			Reset(func() { logFoldReadHook.Store(nil) })

			So(s.foldNow(), ShouldBeNil)
		}

		Convey("a completion ending at 200 folded while one of its rep group ending at 100 applied after the "+
			"fold read the log leaves memory without the overlay's latest EndTime a replay has, with the same "+
			"answers", func() {
			end := time.Now().Truncate(time.Second)
			jobs := testLogJobs("c6 ends", "c6ends", 2)
			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)

			archive := func(job *Job, at time.Time) {
				job.State, job.Exited, job.EndTime = JobStateComplete, true, at
				_, erra := s.archiveCompletion(job.Key(), job)
				So(erra, ShouldBeNil)
			}

			archive(jobs[0], end.Add(200*time.Second))
			duringFold(func() { archive(jobs[1], end.Add(100*time.Second)) })

			s.mu.RLock()
			_, inMemory := s.state.overlayEnds["c6ends"]
			s.mu.RUnlock()

			So(inMemory, ShouldBeFalse)

			reopened := soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			So(reopened.state.overlayEnds, ShouldResemble, map[string]int64{"c6ends": end.Add(100 * time.Second).Unix()})
		})

		Convey("a key looked up under rep groups a then b, then a again after a fold read its first lookup, "+
			"has them in another order in memory than a replay gives, with the same answers", func() {
			job := testDBJob("echo c6 lookups", "a")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			lookUp := func(rg string) {
				moved := testDBJob(job.Cmd, rg)
				So(s.storeRepGroupLookups(ctx, []*Job{moved}), ShouldBeNil)
			}

			duringFold(func() {
				lookUp("b")
				lookUp("a")
			})

			k, err := parseLogKey(job.Key())
			So(err, ShouldBeNil)

			s.mu.RLock()
			inMemory := slices.Clone(s.state.keyRepGroups[k])
			s.mu.RUnlock()

			So(inMemory, ShouldResemble, []string{"a", "b"})

			reopened := soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
			So(reopened.state.keyRepGroups[k], ShouldResemble, []string{"b", "a"})
		})
	})
}

func TestLogStoreCheckpointWaitsForAppends(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job whose state change is appended before a checkpoint's cut but held before it has "+
		"its Refs", t, func() {
		hold := newAppendedHold()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo c6 held append", "c6")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		first, err := walFileNumbers(dir)
		So(err, ShouldBeNil)

		job.State, job.UntilBuried = JobStateBuried, 0

		hold.arm()

		changed := make(chan error, 1)

		go func() { changed <- s.updateJobAfterChangeDurable(job) }()

		hold.awaitEntered()

		Convey("the checkpoint waits for it to apply, so swaps its Ref, and the change is read after the wal "+
			"file holding it is gone, also after reopen", func() {
			checkpointDone := make(chan error, 1)

			go func() { checkpointDone <- s.checkpoint() }()

			select {
			case errc := <-checkpointDone:
				So(errc, ShouldBeNil)
				So("the checkpoint ended before the held write had its Refs", ShouldBeEmpty)
			case <-time.After(ckptTestWait):
			}

			hold.Release()

			for _, done := range []chan error{changed, checkpointDone} {
				select {
				case errd := <-done:
					So(errd, ShouldBeNil)
				case <-time.After(ckptTestTimeout):
					So("the change or checkpoint never ended", ShouldBeEmpty)
				}
			}

			_, errs := os.Stat(walFilePath(dir, first[0]))
			So(errs, ShouldWrap, os.ErrNotExist)

			eachReopen(ctx, &s, func(st *logStore) {
				So(storedLiveJobState(t, st, job.Key()), ShouldEqual, JobStateBuried)
			})
		})
	})
}

// miscountTrailer makes the checkpoint at path end in a trailer count 2 above
// its trailer frame's, placed so that the frame it points at is that trailer
// frame, whole but 16 bytes shorter than such a count needs.
func miscountTrailer(path string) {
	b, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	count := binary.LittleEndian.Uint32(b[len(b)-checkpointTrailerTail:])

	tail := make([]byte, 4, 4+checkpointTrailerTail)
	tail = binary.LittleEndian.AppendUint32(tail, count+2)
	tail = append(tail, checkpointTrailerMagic...)

	So(os.WriteFile(path, append(b, tail...), dbFilePermission), ShouldBeNil)
}

// checkpointTestReader is a read of records through Refs, named for the
// reader of the store that takes their hold. A history read is of the archived
// job, whose records it reads through Refs only until a fold takes it out of
// the overlay.
type checkpointTestReader struct {
	name    string
	history bool
	read    func() error
}

func TestLogStoreCheckpointSwapsBeforeNewHolds(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and a live job with std, its records in a wal file a "+
		"checkpoint is to drop", t, func() {
		pauseLogFolder()

		// Made before the store, so that a failed assertion releases the
		// reader before the store's close.
		release := make(chan struct{})

		var released sync.Once

		releaseReader := func() { released.Do(func() { close(release) }) }

		Reset(releaseReader)

		s, dir := openTestLogStore(t, ctx)

		readers := checkpointTestReaders(ctx, s)

		first, err := walFileNumbers(dir)
		So(err, ShouldBeNil)

		// The history readers read the archived job from history.bolt once
		// the checkpoint has folded, so through no Ref.
		readers = slices.DeleteFunc(readers, func(r checkpointTestReader) bool { return r.history })

		for _, reader := range readers {
			Convey("a reader through "+reader.name+" taking its hold once the checkpoint has started a new "+
				"generation of holds copies swapped Refs, so reads its records after the checkpoint deleted "+
				"their wal file", func() {
				entered := make(chan struct{})
				readDone := make(chan error, 1)

				var reading, started atomic.Bool

				held := func() {
					if reading.CompareAndSwap(false, true) {
						close(entered)
						<-release
					}
				}

				read := func() {
					if !started.CompareAndSwap(false, true) {
						return
					}

					logSegmentFileHook.Store(&held)

					go func() { readDone <- reader.read() }()

					select {
					case <-entered:
					case <-time.After(ckptTestTimeout):
					}
				}

				logAwaitReadersHook.Store(&read)

				Reset(func() {
					logAwaitReadersHook.Store(nil)
					logSegmentFileHook.Store(nil)
				})

				checkpointDone := make(chan error, 1)

				go func() { checkpointDone <- s.checkpoint() }()

				select {
				case errc := <-checkpointDone:
					So(errc, ShouldBeNil)
				case <-time.After(ckptTestTimeout):
					So("the checkpoint never ended", ShouldBeEmpty)
				}

				So(reading.Load(), ShouldBeTrue)

				_, errs := os.Stat(walFilePath(dir, first[0]))
				So(errs, ShouldWrap, os.ErrNotExist)

				releaseReader()

				select {
				case errd := <-readDone:
					So(errd, ShouldBeNil)
				case <-time.After(ckptTestTimeout):
					So("the read never ended", ShouldBeEmpty)
				}
			})
		}
	})
}

// checkpointTestReaders adds to s a live job, buried with std "o" and "e", and
// an archived job, complete in the overlay, and returns a read of them through
// each reader of s that takes a hold of the Refs it copies.
func checkpointTestReaders(ctx context.Context, s *logStore) []checkpointTestReader {
	live := testDBJob("echo c6 reader live", "c6readlive")
	archived := testDBJob("echo c6 reader archived", "c6readarchived")
	_, err := s.storeNewJobsGuarded(ctx, []*Job{live, archived}, false, nil)
	So(err, ShouldBeNil)

	live.State, live.Exited, live.Exitcode, live.Attempts = JobStateBuried, true, 1, 1
	So(s.updateJobAfterExitDurable(live, []byte("o"), []byte("e"), false), ShouldBeNil)

	archived.State, archived.Exited, archived.EndTime = JobStateComplete, true, time.Now()
	_, err = s.archiveCompletion(archived.Key(), archived)
	So(err, ShouldBeNil)

	k, err := parseLogKey(live.Key())
	So(err, ShouldBeNil)

	wantOne := func(jobs []*Job, err error) error {
		if err == nil && len(jobs) != 1 {
			err = fmt.Errorf("%w: %d jobs", errCkptTestRead, len(jobs))
		}

		return err
	}

	return []checkpointTestReader{
		{"readStd", false, func() error {
			std, err := s.readStd(k)
			if err == nil && (string(std[stdOut]) != "o" || string(std[stdErr]) != "e") {
				err = fmt.Errorf("%w: std %q", errCkptTestRead, std)
			}

			return err
		}},
		{"liveJobOf", false, func() error {
			job, live, err := s.liveJobOf(k)
			if err == nil && (!live || job.State != JobStateBuried) {
				err = fmt.Errorf("%w: live %t", errCkptTestRead, live)
			}

			return err
		}},
		{"viewHistory", true, func() error { return wantOne(s.retrieveCompleteJobsByRepGroup(archived.RepGroup)) }},
		{"viewHistoryKeys", true, func() error { return wantOne(s.retrieveCompleteJobsByKeys([]string{archived.Key()})) }},
		{"liveRefs", false, func() error { return wantOne(s.liveJobs()) }},
	}
}

func TestLogStoreCheckpointFailureLogged(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A checkpoint failure noted a minute after one was logged is logged with the count of failures since "+
		"that log", t, func() {
		logCtx, logged := cmdLogSyncCapture(ctx)

		var failures checkpointFailures

		for range 3 {
			failures.note(logCtx, errCkptTestKilled)
		}

		failures.logged = failures.logged.Add(-checkpointFailedLogEvery)
		failures.note(logCtx, errCkptTestKilled)

		failed := logLinesContaining(logged.String(), `msg="`+checkpointFailedLogMsg+`"`)
		So(failed, ShouldHaveLength, 2)
		So(failed[0], ShouldContainSubstring, "failures=1")
		So(failed[1], ShouldContainSubstring, "failures=3")
	})

	Convey("Given a log store due a checkpoint every 100ms, whose checkpoints fail", t, func() {
		var attempts atomic.Int32

		failing := func() error {
			attempts.Add(1)

			return errCkptTestKilled
		}

		logCheckpointRenameHook.Store(&failing)
		logCheckpointEvery.Store(int64(100 * time.Millisecond))

		Reset(func() {
			logCheckpointRenameHook.Store(nil)
			logCheckpointEvery.Store(0)
		})

		logCtx, logged := cmdLogSyncCapture(ctx)
		s, _ := openTestLogStore(t, logCtx)

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("c6 failing", "c6", 5), false, nil)
		So(err, ShouldBeNil)

		Convey("the checkpointer logs the failure once a minute at most, at error, with the failure count", func() {
			So(pollUntilFor(ckptTestTimeout, func() bool { return attempts.Load() >= 2 }), ShouldBeTrue)

			failed := logLinesContaining(logged.String(), `msg="`+checkpointFailedLogMsg+`"`)
			So(failed, ShouldHaveLength, 1)
			So(failed[0], ShouldContainSubstring, "lvl=eror")
			So(failed[0], ShouldContainSubstring, "failures=1")
			So(failed[0], ShouldContainSubstring, errCkptTestKilled.Error())
			So(s.checkpointFrom(), ShouldEqual, 0)
		})
	})
}

func TestLogStoreCheckpointDue(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A checkpoint whose interval has passed is not due sooner after the last ended than the last took",
		t, func() {
			d := checkpointDue{started: time.Now().Add(-time.Hour), ended: time.Now(), took: time.Hour}
			So(d.now(0), ShouldBeFalse)

			d.ended = d.started
			So(d.now(0), ShouldBeTrue)
		})

	Convey("Given a log store due a checkpoint every 32KiB of wal, with more than that written", t, func() {
		logCheckpointEvery.Store(int64(time.Hour))
		logCheckpointBytes.Store(32 << 10)

		Reset(func() {
			logCheckpointEvery.Store(0)
			logCheckpointBytes.Store(0)
		})

		s, _ := openTestLogStore(t, ctx)

		for i := 0; s.w.Stats().Bytes < 2*32<<10; i++ {
			_, err := s.storeNewJobsGuarded(ctx, testLogJobs(fmt.Sprintf("c6 due %d", i), "c6", 50), false, nil)
			So(err, ShouldBeNil)
		}

		Convey("a checkpoint made then counts the wal written from its start, so the next is not due yet", func() {
			var d checkpointDue

			So(d.run(s), ShouldBeNil)
			So(s.checkpointFrom(), ShouldBeGreaterThan, 0)

			time.Sleep(d.took)

			So(d.now(s.w.Stats().Bytes), ShouldBeFalse)
		})
	})

	Convey("Given a log store just opened, with its checkpoints due at the defaults", t, func() {
		var started atomic.Int32

		s, _ := openTestLogStore(t, ctx)

		starting := func() { started.Add(1) }
		logCheckpointFoldedHook.Store(checkpointFoldedHookFor(s, starting))

		Reset(func() { logCheckpointFoldedHook.Store(nil) })

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("c6 due at open", "c6", 20), false, nil)
		So(err, ShouldBeNil)

		Convey("its checkpointer makes no checkpoint at its first polls, the interval counting from the open",
			func() {
				time.Sleep(2*checkpointPoll + ckptTestWait)

				So(started.Load(), ShouldEqual, 0)
				So(s.checkpointFrom(), ShouldEqual, 0)
			})
	})
}

func TestLogStoreCheckpointUnswappedKeepsFiles(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store checkpointed once, whose memory, unlike its log, still has a deleted job live "+
		"when its next checkpoint swaps its Refs", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("c6 unswapped", "c6", 2)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		first := checkpointed(s)
		files := walFilesAtOrAfter(dir, first)

		k, err := parseLogKey(jobs[0].Key())
		So(err, ShouldBeNil)

		s.mu.RLock()
		stale := *s.state.live[k]
		s.mu.RUnlock()

		So(s.deleteLiveJobs(ctx, []string{jobs[0].Key()}), ShouldBeNil)

		diverged := func() error { //nolint:unparam // a logCheckpointRenameHook, which can fail
			s.mu.Lock()
			s.state.live[k] = &stale
			s.mu.Unlock()

			return nil
		}

		logCheckpointRenameHook.Store(&diverged)

		Reset(func() {
			logCheckpointRenameHook.Store(nil)

			s.mu.Lock()
			delete(s.state.live, k)
			s.mu.Unlock()
		})

		Convey("the checkpoint fails, finding Refs below its cut it has no copy of, and deletes no wal file "+
			"and not the older checkpoint", func() {
			So(s.checkpoint(), ShouldWrap, errCheckpointRefsUnswapped)

			So(walFilesAtOrAfter(dir, first)[:len(files)], ShouldResemble, files)
			So(checkpointFiles(dir)[0], ShouldEqual, first)

			s.mu.RLock()
			spec := s.state.live[k].spec
			s.mu.RUnlock()

			So(spec, ShouldResemble, stale.spec)
		})
	})
}

func TestLogStoreCheckpointSkippedWhileFoldFails(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, 40 jobs added and 20 of them archived, whose folds fail",
		t, func() {
			pauseLogFolder()

			s, dir := openTestLogStore(t, ctx)

			jobs := testLogJobs("c6 fold fails", "c6", 40)
			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)
			So(archiveConcurrently(s, jobs[:20], time.Now()), ShouldBeNil)

			want := liveViews(s)

			failing := func() error { return errCkptTestFold }

			Reset(func() { logFoldTxHook.Store(nil) })

			logFoldTxHook.Store(&failing)

			Convey("a checkpoint is skipped, and once folds work again a reopen gives the same live set and "+
				"every completion", func() {
				So(s.checkpoint(), ShouldWrap, errCkptTestFold)
				So(s.checkpointFrom(), ShouldEqual, 0)
				So(checkpointFiles(dir), ShouldBeEmpty)

				logFoldTxHook.Store(nil)

				reopened := reopenLogStore(ctx, s)
				So(liveViews(reopened), ShouldResemble, want)

				var incomplete []string

				for _, job := range jobs[:20] {
					if complete, errc := reopened.checkIfComplete(job.Key()); errc != nil || !complete {
						incomplete = append(incomplete, job.Key())
					}
				}

				So(incomplete, ShouldBeEmpty)
			})
		})
}

func TestLogStoreCheckpointClosesDeletedFiles(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store of several wal files whose live jobs were read through their Refs, then "+
		"checkpointed, then read again", t, func() {
		smallWALSegments()

		s, _ := openTestLogStore(t, ctx)

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("c6 closes", "c6", 200), false, nil)
		So(err, ShouldBeNil)

		liveViews(s)
		first := checkpointed(s)
		liveViews(s)

		Convey("a second checkpoint leaves no wal file or checkpoint below its cut open for reading", func() {
			second := checkpointed(s)
			So(second, ShouldBeGreaterThan, first)

			s.filesMu.Lock()
			open := slices.Collect(maps.Keys(s.files))
			s.filesMu.Unlock()

			below := slices.DeleteFunc(open, func(n uint32) bool { return n&^checkpointRefFlag >= second })
			So(below, ShouldBeEmpty)
		})
	})
}

func TestLogStoreCheckpointDropsWAL(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store of several wal files with 2,000 jobs added and 1,000 of them archived and folded",
		t, func() {
			smallWALSegments()

			s, dir := openTestLogStore(t, ctx)

			jobs := testLogJobs("c6 drops", "c6", 2000)
			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)
			So(archiveConcurrently(s, jobs[:1000], time.Now()), ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)

			before := liveViews(s)
			So(before, ShouldHaveLength, 1000)
			So(len(walFilesBelow(dir, 1<<31)), ShouldBeGreaterThan, 2)

			Convey("a checkpoint leaves no wal file below its cut, and a reopen gives the same live set and "+
				"resumes the seq counter past every seq taken", func() {
				taken := s.seq.Load()

				cut := checkpointed(s)
				So(walFilesBelow(dir, cut), ShouldBeEmpty)
				So(liveViews(s), ShouldResemble, before)

				reopened := reopenLogStore(ctx, s)
				So(reopened.checkpointFrom(), ShouldEqual, cut)
				So(liveViews(reopened), ShouldResemble, before)
				So(reopened.seq.Load(), ShouldBeGreaterThanOrEqualTo, taken)

				var incomplete []string

				for _, job := range jobs[:1000] {
					if complete, errc := reopened.checkIfComplete(job.Key()); errc != nil || !complete {
						incomplete = append(incomplete, job.Key())
					}
				}

				So(incomplete, ShouldBeEmpty)

				Convey("and a second checkpoint replaces the first, leaving one checkpoint file, and a reopen "+
					"resumes the seq counter past every seq taken, though the records of the highest are gone",
					func() {
						seq := reopened.seq.Load()

						next := checkpointed(reopened)
						So(next, ShouldBeGreaterThan, cut)
						So(walFilesBelow(dir, next), ShouldBeEmpty)
						So(checkpointFiles(dir), ShouldResemble, []uint32{next})

						again := reopenLogStore(ctx, reopened)
						So(liveViews(again), ShouldResemble, before)
						So(again.seq.Load(), ShouldBeGreaterThanOrEqualTo, seq)
					})
			})
		})
}

func TestLogStoreCheckpointKilledBeforeRename(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store checkpointed once, then changed, whose next checkpoint is killed after writing "+
		"its .ckpt.tmp but before renaming it", t, func() {
		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("c6 killed", "c6", 20)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		first := checkpointed(s)

		So(s.deleteLiveJobs(ctx, []string{jobs[0].Key()}), ShouldBeNil)

		more := testLogJobs("c6 killed more", "c6", 5)
		_, err = s.storeNewJobsGuarded(ctx, more, false, nil)
		So(err, ShouldBeNil)

		want := liveViews(s)
		So(want, ShouldHaveLength, 24)

		kill := func() error { return errCkptTestKilled }
		logCheckpointRenameHook.Store(&kill)

		Reset(func() { logCheckpointRenameHook.Store(nil) })

		So(s.checkpoint(), ShouldWrap, errCkptTestKilled)

		tmps, errg := filepath.Glob(filepath.Join(dir, "*"+checkpointFileExt+storeTmpExt))
		So(errg, ShouldBeNil)
		So(tmps, ShouldHaveLength, 1)

		Convey("a reopen uses the previous checkpoint, the .tmp is gone and the live set is the model's", func() {
			logCheckpointRenameHook.Store(nil)

			reopened := reopenLogStore(ctx, s)
			So(reopened.checkpointFrom(), ShouldEqual, first)

			_, errs := os.Stat(tmps[0])
			So(errs, ShouldWrap, os.ErrNotExist)

			So(liveViews(reopened), ShouldResemble, want)
			soLive(reopened, jobs[0].Key(), false)
		})
	})
}

func TestLogStoreCheckpointRenameFails(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store checkpointed once, then changed, whose next checkpoint's rename fails as a "+
		"directory is in the way", t, func() {
		s, dir := openTestLogStore(t, ctx)

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("c6 rename fails", "c6", 20), false, nil)
		So(err, ShouldBeNil)

		first := checkpointed(s)

		_, err = s.storeNewJobsGuarded(ctx, testLogJobs("c6 rename fails more", "c6", 5), false, nil)
		So(err, ShouldBeNil)

		pattern := filepath.Join(dir, "*"+checkpointFileExt+storeTmpExt)

		blocking := func() error {
			tmps, errg := filepath.Glob(pattern)

			for _, tmp := range tmps {
				errg = errors.Join(errg, os.MkdirAll(filepath.Join(strings.TrimSuffix(tmp, storeTmpExt), "in the way"),
					logStoreDirPerm))
			}

			return errg
		}

		logCheckpointRenameHook.Store(&blocking)

		Reset(func() { logCheckpointRenameHook.Store(nil) })

		Convey("the checkpoint fails, removing its .ckpt.tmp and keeping the previous checkpoint", func() {
			So(s.checkpoint(), ShouldNotBeNil)
			So(s.checkpointFrom(), ShouldEqual, first)

			tmps, errg := filepath.Glob(pattern)
			So(errg, ShouldBeNil)
			So(tmps, ShouldBeEmpty)
		})
	})
}

func TestLogStoreCheckpointRefs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given 1,000 live jobs, 100 of them with std, and a checkpoint that drops every earlier wal file", t,
		func() {
			smallWALSegments()

			s, dir := openTestLogStore(t, ctx)

			jobs := testLogJobs("c6 refs", "c6", 1000)
			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)

			exits := make([]error, 0, 100)

			for i, job := range jobs[:100] {
				job.State, job.Exited, job.Exitcode, job.Attempts = JobStateBuried, true, 1, 1
				exits = append(exits, s.updateJobAfterExitDurable(job, fmt.Appendf(nil, "o%d", i),
					fmt.Appendf(nil, "e%d", i), false))
			}

			So(errors.Join(exits...), ShouldBeNil)

			recovered, _, err := s.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(recovered, ShouldHaveLength, len(jobs))

			cut := checkpointed(s)
			So(walFilesBelow(dir, cut), ShouldBeEmpty)
			soCheckpointBlocks(dir, cut)

			Convey("each live entry's spec, last state and std Ref, read with wal.ReadAt, decodes to that job's",
				func() {
					byKey := make(map[logKey]*Job, len(recovered))
					errs := make([]error, 0, len(recovered))

					for _, job := range recovered {
						k, errk := parseLogKey(job.Key())
						errs = append(errs, errk)

						byKey[k] = job
					}

					So(errors.Join(errs...), ShouldBeNil)
					So(byKey, ShouldHaveLength, len(jobs))

					s.mu.RLock()
					entries := maps.Clone(s.state.live)
					s.mu.RUnlock()

					So(entries, ShouldHaveLength, len(jobs))

					var mismatches []error

					specs, states, stds := 0, 0, 0

					for k, e := range entries {
						job := byKey[k]
						if job == nil {
							mismatches = append(mismatches, fmt.Errorf("%w: no job recovered for %x", errCkptTestRead, k))

							continue
						}

						mismatches = append(mismatches, specMismatch(s, e.spec, k, job), stateMismatch(s, e.state, k, job))
						specs++
						states++

						for stream, has := range e.hasStd {
							if has {
								mismatches = append(mismatches, stdMismatch(s, e.std[stream], k, uint8(stream), job))
								stds++
							}
						}
					}

					So(errors.Join(mismatches...), ShouldBeNil)
					So(specs, ShouldEqual, 1000)
					So(states, ShouldEqual, 1000)
					So(stds, ShouldEqual, 200)
				})
		})
}

func TestLogStoreCheckpointWhileWriting(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given 4 goroutines making random operations continuously on a log store of small wal files", t, func() {
		smallWALSegments()

		s, dir := openTestLogStore(t, ctx)

		runs := make([]*recoverRun, 4)
		for i := range runs {
			runs[i] = &recoverRun{
				ctx: ctx, s: s, prefix: fmt.Sprintf("echo c6 writing %d", i),
				rng:  rand.New(rand.NewPCG(6, uint64(i))), //nolint:gosec // a fixed seed, for a reproducible run
				live: make(map[string]*Job), made: make(map[string]int),
			}
		}

		var (
			stop atomic.Bool
			wg   sync.WaitGroup
		)

		for _, run := range runs {
			wg.Go(func() {
				for !stop.Load() {
					run.step()
				}
			})
		}

		Convey("during 3 checkpoints, a reopen gives the live set of the model", func() {
			cuts := make([]uint32, 0, 3)

			for range 3 {
				time.Sleep(ckptTestWait)

				cuts = append(cuts, checkpointed(s))
			}

			stop.Store(true)
			wg.Wait()

			want := make(map[string]recoverView)

			for _, run := range runs {
				So(run.err, ShouldBeNil)

				maps.Copy(want, recoverViews(slices.Collect(maps.Values(run.live))))
			}

			So(cuts[0], ShouldBeLessThan, cuts[1])
			So(cuts[1], ShouldBeLessThan, cuts[2])
			So(walFilesBelow(dir, cuts[2]), ShouldBeEmpty)

			reopened := reopenLogStore(ctx, s)
			So(reopened.checkpointFrom(), ShouldEqual, cuts[2])
			So(liveViews(reopened), ShouldResemble, want)
		})
	})
}

func TestLogStoreCheckpointKeepsStdClear(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job marked to run again, with an exit 1 block with std snapshotted but not "+
		"appended, whose archive keeps it live and clears its std", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c6 std clear", "c6")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State, job.ReservedBy, job.StartTime = JobStateRunning, uuid.Must(uuid.NewV4()), time.Now()
		So(s.updateJobRunStateDurable(job), ShouldBeNil)

		job.RerunAfterRun = true
		So(s.storeRunningRerunMarks([]*Job{job}), ShouldBeNil)

		job.State, job.Exited, job.Exitcode = JobStateBuried, true, 1
		held := holdExitWrite(s, job, "o", "e")

		job.State, job.Exitcode, job.EndTime = JobStateComplete, 0, time.Now()
		outcome, err := s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)
		So(outcome, ShouldEqual, archiveKeptLive)

		Convey("the exit block appended after a checkpoint recreates no std, also after reopen", func() {
			checkpointed(s)
			So(held.write(s), ShouldBeNil)

			soStd(ctx, s, job.Key(), "", "")
			So(inspectedCount(s, kindStdOut), ShouldEqual, 0)

			reopened := reopenLogStore(ctx, s)
			soStd(ctx, reopened, job.Key(), "", "")
			So(inspectedCount(reopened, kindStdOut), ShouldEqual, 0)
		})
	})
}

func TestLogStoreCheckpointReplacedSpecs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and job k added in a wal file a checkpoint is to drop", t,
		func() {
			pauseLogFolder()

			s, dir := openTestLogStore(t, ctx)

			k := testDBJob("echo c6 replaced", "c6replaced")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{k}, false, nil)
			So(err, ShouldBeNil)

			end := time.Now().Truncate(time.Second)

			Convey("archiving k, re-adding it and archiving it again after the checkpoint's cut but before its "+
				"Refs are swapped, then folding both archives once its wal file is gone, folds both", func() {
				archived := func() {
					for i := range 2 {
						job := testDBJob("echo c6 replaced", "c6replaced")
						if i == 0 {
							job = k
						} else {
							_, erra := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
							So(erra, ShouldBeNil)
						}

						job.State, job.Exited, job.EndTime = JobStateComplete, true, end.Add(time.Duration(i)*time.Second)
						_, erra := s.archiveCompletion(job.Key(), job)
						So(erra, ShouldBeNil)
					}

					s.mu.RLock()
					replaced := len(s.state.replaced)
					s.mu.RUnlock()

					So(replaced, ShouldEqual, 1)
				}

				logCheckpointFoldedHook.Store(checkpointFoldedHookFor(s, archived))

				Reset(func() { logCheckpointFoldedHook.Store(nil) })

				cut := checkpointed(s)
				So(walFilesBelow(dir, cut), ShouldBeEmpty)

				So(s.foldNow(), ShouldBeNil)
				So(overlayEmpty(s), ShouldBeTrue)

				eachReopen(ctx, &s, func(st *logStore) { soCompletedAt(st, k, end.Add(time.Second)) })
			})

			Convey("archiving k, without re-adding it, after the checkpoint's cut but before its Refs are "+
				"swapped, so only its overlay entry has its spec Ref, then folding once its wal file is gone, "+
				"folds it", func() {
				// It runs on the checkpoint's goroutine, so keeps its error
				// for the test's goroutine to assert.
				var archiveErr error

				archived := func() {
					k.State, k.Exited, k.EndTime = JobStateComplete, true, end
					_, archiveErr = s.archiveCompletion(k.Key(), k)
				}

				logCheckpointFoldedHook.Store(checkpointFoldedHookFor(s, archived))

				Reset(func() { logCheckpointFoldedHook.Store(nil) })

				cut := checkpointed(s)
				So(walFilesBelow(dir, cut), ShouldBeEmpty)

				So(archiveErr, ShouldBeNil)

				So(s.foldNow(), ShouldBeNil)
				So(overlayEmpty(s), ShouldBeTrue)

				eachReopen(ctx, &s, func(st *logStore) { soCompletedAt(st, k, end) })
			})
		})
}

func TestLogStoreCheckpointWaitsForFoldReads(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, job k added in a wal file a checkpoint is to drop, "+
		"and k archived after the checkpoint's cut, then folding until the fold has taken its spec Refs", t,
		func() {
			pauseLogFolder()

			// Made before the store, so that a failed assertion releases the
			// fold before the store's close waits for it.
			folding, release := make(chan struct{}), make(chan struct{})
			foldDone := make(chan error, 1)

			var once, released sync.Once

			releaseFold := func() { released.Do(func() { close(release) }) }

			Reset(releaseFold)

			s, dir := openTestLogStore(t, ctx)

			k := testDBJob("echo c6 fold reads", "c6reads")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{k}, false, nil)
			So(err, ShouldBeNil)

			first, errw := walFileNumbers(dir)
			So(errw, ShouldBeNil)

			end := time.Now().Truncate(time.Second)

			specsTaken := func() {
				once.Do(func() {
					close(folding)
					<-release
				})
			}

			// It runs on the checkpoint's goroutine, so keeps its error for
			// the test's goroutine to assert.
			var archiveErr error

			archivedThenFolding := func() {
				k.State, k.Exited, k.EndTime = JobStateComplete, true, end
				if _, archiveErr = s.archiveCompletion(k.Key(), k); archiveErr != nil {
					return
				}

				logFoldSpecsHook.Store(&specsTaken)

				go func() { foldDone <- s.foldNow() }()

				select {
				case <-folding:
				case <-time.After(ckptTestTimeout):
				}
			}

			logCheckpointFoldedHook.Store(checkpointFoldedHookFor(s, archivedThenFolding))

			Reset(func() {
				logCheckpointFoldedHook.Store(nil)
				logFoldSpecsHook.Store(nil)
			})

			Convey("the checkpoint keeps k's wal file until that fold has read it, and the fold folds k's "+
				"completion", func() {
				checkpointDone := make(chan error, 1)

				go func() { checkpointDone <- s.checkpoint() }()

				select {
				case <-folding:
				case <-time.After(ckptTestTimeout):
					So("the fold never took its spec Refs", ShouldBeEmpty)
				}

				So(archiveErr, ShouldBeNil)

				select {
				case errc := <-checkpointDone:
					So(errc, ShouldBeNil)
					So("the checkpoint ended while the fold held its Refs", ShouldBeEmpty)
				case <-time.After(ckptTestWait):
				}

				_, errs := os.Stat(walFilePath(dir, first[0]))
				So(errs, ShouldBeNil)

				releaseFold()

				for _, done := range []chan error{foldDone, checkpointDone} {
					select {
					case errd := <-done:
						So(errd, ShouldBeNil)
					case <-time.After(ckptTestTimeout):
						So("the fold or checkpoint never ended", ShouldBeEmpty)
					}
				}

				So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)
				So(overlayEmpty(s), ShouldBeTrue)

				eachReopen(ctx, &s, func(st *logStore) { soCompletedAt(st, k, end) })
			})
		})
}

func TestLogStoreCheckpointRotatesDuringFold(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and 10 jobs archived", t, func() {
		pauseLogFolder()

		// Made before the store, so that a failed assertion releases the fold
		// before the store's close waits for it.
		reading, release := make(chan struct{}), make(chan struct{})

		var once, released sync.Once

		releaseFold := func() { released.Do(func() { close(release) }) }

		Reset(releaseFold)

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("c6 rotates", "c6rotates", 10)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second)
		So(archiveConcurrently(s, jobs, end), ShouldBeNil)

		read := func() {
			once.Do(func() {
				close(reading)
				<-release
			})
		}

		logFoldReadHook.Store(&read)

		Reset(func() { logFoldReadHook.Store(nil) })

		Convey("a checkpoint rotating the log while a fold of them is reading waits for that fold, which "+
			"folds them all", func() {
			foldDone := make(chan error, 1)

			go func() { foldDone <- s.foldNow() }()

			select {
			case <-reading:
			case <-time.After(ckptTestTimeout):
				So("the fold never read", ShouldBeEmpty)
			}

			files, errw := walFileNumbers(dir)
			So(errw, ShouldBeNil)

			checkpointDone := make(chan error, 1)

			go func() { checkpointDone <- s.checkpoint() }()

			awaitWALFiles(dir, len(files)+1)

			releaseFold()

			for _, done := range []chan error{foldDone, checkpointDone} {
				select {
				case errd := <-done:
					So(errd, ShouldBeNil)
				case <-time.After(ckptTestTimeout):
					So("the fold or checkpoint never ended", ShouldBeEmpty)
				}
			}

			So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)
			So(overlayEmpty(s), ShouldBeTrue)

			eachReopen(ctx, &s, func(st *logStore) {
				for _, job := range jobs {
					soCompletedAt(st, job, end)
				}
			})
		})
	})
}

// awaitWALFiles waits until dir has n wal files.
func awaitWALFiles(dir string, n int) {
	deadline := time.Now().Add(ckptTestTimeout)

	files, err := walFileNumbers(dir)
	for err == nil && len(files) < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)

		files, err = walFileNumbers(dir)
	}

	So(err, ShouldBeNil)
	So(len(files), ShouldBeGreaterThanOrEqualTo, n)
}

func TestLogStoreCheckpointUnprompted(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given checkpoints due every 100ms, or every 4KiB of wal", t, func() {
		for _, due := range []struct {
			name         string
			every, bytes int64
		}{
			{"every 100ms", int64(100 * time.Millisecond), 0},
			{"every 4KiB of wal", int64(time.Hour), 4 << 10},
		} {
			Convey("a log store "+due.name+" checkpoints unprompted", func() {
				logCheckpointEvery.Store(due.every)
				logCheckpointBytes.Store(due.bytes)

				Reset(func() {
					logCheckpointEvery.Store(0)
					logCheckpointBytes.Store(0)
				})

				s, _ := openTestLogStore(t, ctx)

				jobs := testLogJobs("c6 unprompted", "c6", 500)
				_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
				So(err, ShouldBeNil)

				deadline := time.Now().Add(ckptTestTimeout)
				for s.checkpointFrom() == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}

				So(s.checkpointFrom(), ShouldBeGreaterThan, 0)
				So(liveViews(reopenLogStore(ctx, s)), ShouldHaveLength, 500)
			})
		}
	})
}

func TestLogStoreRecoverHistoryBeforeCheckpoint(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store checkpointed, whose history.bolt is replaced by its copy from before the "+
		"checkpoint folded to its cut", t, func() {
		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("c6 history behind", "c6", 3)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		s = reopenLogStore(ctx, s)

		history := filepath.Join(dir, historyBoltFile)
		behind := filepath.Join(t.TempDir(), historyBoltFile)

		So(s.close(ctx), ShouldBeNil)
		So(copyFile(history, behind), ShouldBeNil)

		s = openLogStoreIn(ctx, dir)
		checkpointed(s)
		So(s.close(ctx), ShouldBeNil)
		So(copyFile(behind, history), ShouldBeNil)

		Convey("opening it fails with errStoreCorrupt", func() {
			_, erro := openLogStore(ctx, dir)
			So(erro, ShouldWrap, errStoreCorrupt)
		})
	})

	Convey("Given a closed log store checkpointed, whose history.bolt is replaced by its copy from when it was "+
		"folded to wal file walFrom-1, just before the checkpoint's cut", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("c6 history just behind", "c6", 3)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(archiveConcurrently(s, jobs[:1], time.Now()), ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		s.mu.RLock()
		folded := s.state.folded
		s.mu.RUnlock()

		history := filepath.Join(dir, historyBoltFile)
		behind := filepath.Join(t.TempDir(), historyBoltFile)

		// The folder is paused, so history.bolt has no write in progress.
		So(copyFile(history, behind), ShouldBeNil)

		cut := checkpointed(s)
		So(folded.file, ShouldEqual, cut-1)

		So(s.close(ctx), ShouldBeNil)
		So(copyFile(behind, history), ShouldBeNil)

		Convey("opening it fails with errStoreCorrupt", func() {
			_, erro := openLogStore(ctx, dir)
			So(erro, ShouldWrap, errStoreCorrupt)
		})
	})
}

func TestLogStoreCheckpointClearedStd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job that exited 1 with std", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo c6 cleared std", "c6")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State, job.Exited, job.Exitcode, job.Attempts = JobStateBuried, true, 1, 1
		So(s.updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false), ShouldBeNil)

		soClearedAfterCheckpoint := func() {
			checkpointed(s)
			soStd(ctx, s, job.Key(), "", "")
			soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))
		}

		Convey("when it exits again with no std, after a checkpoint it has none and its memory is a replay's",
			func() {
				job.State, job.Attempts = JobStateDelayed, 2
				So(s.updateJobAfterExitDurable(job, nil, nil, false), ShouldBeNil)

				soClearedAfterCheckpoint()
			})

		Convey("when it runs again and its archive keeps it live, clearing its std, after a checkpoint it has "+
			"none and its memory is a replay's", func() {
			job.State, job.ReservedBy, job.StartTime = JobStateRunning, uuid.Must(uuid.NewV4()), time.Now()
			So(s.updateJobRunStateDurable(job), ShouldBeNil)

			job.RerunAfterRun = true
			So(s.storeRunningRerunMarks([]*Job{job}), ShouldBeNil)

			job.State, job.Exitcode, job.EndTime = JobStateComplete, 0, time.Now()
			outcome, erra := s.archiveCompletion(job.Key(), job)
			So(erra, ShouldBeNil)
			So(outcome, ShouldEqual, archiveKeptLive)

			soClearedAfterCheckpoint()
		})
	})
}

func TestLogStoreCheckpointKeepsUnreferenced(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with 2 envs and 2 limits stored, each in a write of the same size as the next, "+
		"and a dep group no live job has", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		envs := make(map[string]string, 2)

		for i := range 2 {
			env := fmt.Sprintf("c6 env %d", i)
			key, err := s.storeEnv([]byte(env))
			So(err, ShouldBeNil)

			envs[key] = env
		}

		limits := map[string]int64{"c6limit0": 7, "c6limit1": 8}

		for _, group := range slices.Sorted(maps.Keys(limits)) {
			_, _, err := s.storeLimitGroups(map[string]*limiter.GroupData{
				group: limiter.NewCountGroupData(limits[group]),
			})
			So(err, ShouldBeNil)
		}

		job := testDBJob("echo c6 unreferenced", "c6")
		job.DepGroups = []string{"c6dg"}
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)

		Convey("a checkpoint dropping the wal files holding them keeps them, as memory has them, also after "+
			"reopen", func() {
			cut := checkpointed(s)
			So(walFilesBelow(dir, cut), ShouldBeEmpty)

			soStateLikeReplay(ctx, s, dir, filepath.Join(t.TempDir(), "crashed"))

			eachReopen(ctx, &s, func(st *logStore) {
				for key, env := range envs {
					So(string(st.retrieveEnv(ctx, key)), ShouldEqual, env)
				}

				for group, limit := range limits {
					So(st.retrieveLimitGroup(ctx, group).Limit(), ShouldEqual, limit)
				}

				seen, errs := st.depGroupEverSeen("c6dg")
				So(errs, ShouldBeNil)
				So(seen, ShouldBeTrue)
			})
		})
	})
}

func TestLogStoreRecoverCheckpointChoice(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a copy of a log store as a crash after its second checkpoint's rename, before its deletions, "+
		"leaves it", t, func() {
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("c6 choice", "c6", 20)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		first := checkpointed(s)

		So(s.deleteLiveJobs(ctx, []string{jobs[0].Key()}), ShouldBeNil)

		_, err = s.storeNewJobsGuarded(ctx, testLogJobs("c6 choice more", "c6", 5), false, nil)
		So(err, ShouldBeNil)

		want := liveViews(s)
		So(want, ShouldHaveLength, 24)

		crashed := filepath.Join(t.TempDir(), "crashed")
		copied := func() error { return copyCrashStore(s.dir, crashed) }
		logCheckpointRenameHook.Store(&copied)

		Reset(func() { logCheckpointRenameHook.Store(nil) })

		second := checkpointed(s)

		logCheckpointRenameHook.Store(nil)

		newest := checkpointPath(crashed, second)
		So(os.Rename(newest+storeTmpExt, newest), ShouldBeNil)
		So(checkpointFiles(crashed), ShouldResemble, []uint32{first, second})

		soReopensWith := func(n uint32) {
			reopened := openLogStoreIn(ctx, crashed)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			So(reopened.checkpointFrom(), ShouldEqual, n)
			So(liveViews(reopened), ShouldResemble, want)
		}

		Convey("a reopen uses the newest checkpoint and deletes the older one and the wal files before it", func() {
			So(walFilesBelow(crashed, second), ShouldNotBeEmpty)

			soReopensWith(second)

			So(checkpointFiles(crashed), ShouldResemble, []uint32{second})
			So(walFilesBelow(crashed, second), ShouldBeEmpty)
		})

		for _, damage := range []struct {
			name string
			fn   func(path string)
		}{
			{"is torn", truncateLastByte},
			{"has a whole trailer frame that is not as long as its count of block offsets says", miscountTrailer},
			{"names another walFrom", func(path string) {
				So(copyFile(checkpointPath(crashed, first), path), ShouldBeNil)
			}},
		} {
			Convey("a reopen uses the previous checkpoint if the newest "+damage.name, func() {
				damage.fn(newest)
				soReopensWith(first)
			})
		}

		Convey("a reopen fails with errStoreCorrupt if no checkpoint is valid", func() {
			truncateLastByte(newest)
			truncateLastByte(checkpointPath(crashed, first))

			_, erro := openLogStore(ctx, crashed)
			So(erro, ShouldWrap, errStoreCorrupt)
		})

		Convey("a reopen fails with errStoreCorrupt if wal file walFrom of the newest checkpoint is missing", func() {
			So(os.Remove(walFilePath(crashed, second)), ShouldBeNil)

			_, erro := openLogStore(ctx, crashed)
			So(erro, ShouldWrap, errStoreCorrupt)
		})
	})
}

func TestLogStoreCheckpointMergeDamage(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store of small wal files checkpointed once, then written across 3 or more wal files, "+
		"all folded", t, func() {
		pauseLogFolder()
		smallWALSegments()

		s, dir := openTestLogStore(t, ctx)

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("c6 merge", "c6", 20), false, nil)
		So(err, ShouldBeNil)

		first := checkpointed(s)

		So(addUntilWALFiles(ctx, s, first, 3, "c6 merge"), ShouldBeNil)

		So(s.foldNow(), ShouldBeNil)

		files := walFilesAtOrAfter(dir, first)

		// A failed checkpoint has rotated the log, adding a wal file.
		soCorruptKeeping := func(kept []uint32) {
			So(s.checkpoint(), ShouldWrap, errStoreCorrupt)
			So(checkpointFiles(dir), ShouldResemble, []uint32{first})
			So(walFilesAtOrAfter(dir, first)[:len(kept)], ShouldResemble, kept)
		}

		Convey("a checkpoint fails with errStoreCorrupt, deleting nothing, if the previous checkpoint's wal file "+
			"walFrom is missing", func() {
			So(os.Remove(walFilePath(dir, first)), ShouldBeNil)

			soCorruptKeeping(files[1:])
		})

		Convey("a checkpoint fails with errStoreCorrupt, deleting nothing, if a wal file it merges is damaged "+
			"before its durable end", func() {
			path := walFilePath(dir, files[1])

			b, errr := os.ReadFile(path)
			So(errr, ShouldBeNil)

			b[len(b)/2] ^= 0xff
			So(os.WriteFile(path, b, dbFilePermission), ShouldBeNil)

			soCorruptKeeping(files)
		})
	})
}

func TestLogStoreCheckpointCloseStopsMerge(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store of small wal files checkpointed once, then written across 3 or more wal files", t,
		func() {
			pauseLogFolder()
			smallWALSegments()

			logPath, logCtx := managerLogContext(t, ctx)
			s, dir := openTestLogStore(t, logCtx)

			_, err := s.storeNewJobsGuarded(ctx, testLogJobs("c6 close", "c6", 20), false, nil)
			So(err, ShouldBeNil)

			first := checkpointed(s)

			So(addUntilWALFiles(ctx, s, first, 3, "c6 close"), ShouldBeNil)

			want := liveViews(s)

			Convey("closing it once the checkpointer's next checkpoint has folded to its cut stops that "+
				"checkpoint's merge, so no checkpoint is written and no failure logged, and a reopen gives the "+
				"same live set", func() {
				closed := make(chan error, 1)

				var (
					once    sync.Once
					stopped atomic.Bool
				)

				// It runs on the checkpointer's goroutine, so keeps what it saw
				// for the test's goroutine to assert.
				closing := func() {
					once.Do(func() {
						go func() { closed <- s.close(ctx) }()

						stopped.Store(pollUntilFor(ckptTestTimeout, s.checkpointStopping))
					})
				}

				logCheckpointFoldedHook.Store(checkpointFoldedHookFor(s, closing))
				logCheckpointEvery.Store(1)

				Reset(func() {
					logCheckpointFoldedHook.Store(nil)
					logCheckpointEvery.Store(0)
				})

				select {
				case errc := <-closed:
					So(errc, ShouldBeNil)
				case <-time.After(ckptTestTimeout):
					So("the store never closed", ShouldBeEmpty)
				}

				So(stopped.Load(), ShouldBeTrue)
				So(checkpointFiles(dir), ShouldResemble, []uint32{first})
				So(logLinesContaining(readLogFile(t, logPath), checkpointFailedLogMsg), ShouldBeEmpty)

				reopened := openLogStoreIn(ctx, dir)

				Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

				So(reopened.checkpointFrom(), ShouldEqual, first)
				So(liveViews(reopened), ShouldResemble, want)
			})
		})
}

// smallWALSegments makes the log stores opened from now on rotate their wal
// files at ckptTestSegmentSize, until the Convey block ends.
func smallWALSegments() {
	old := walSegmentSize
	walSegmentSize = ckptTestSegmentSize

	Reset(func() { walSegmentSize = old })
}

func TestLogStoreRecoverCheckpointOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a checkpoint holding job k's spec in one block and its later buried state in a later one", t,
		func() {
			pauseLogFolder()

			s, dir := openTestLogStore(t, ctx)

			k := testDBJob("echo c6 order", "c6order")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{k}, false, nil)
			So(err, ShouldBeNil)

			_, err = s.storeNewJobsGuarded(ctx, testLogJobs("c6 order others", "c6", 500), false, nil)
			So(err, ShouldBeNil)

			k.State, k.UntilBuried = JobStateBuried, 0
			So(s.updateJobAfterChangeDurable(k), ShouldBeNil)

			cut := checkpointed(s)

			key, err := parseLogKey(k.Key())
			So(err, ShouldBeNil)

			blocks := soCheckpointBlocks(dir, cut)
			specBlock, stateBlock := blockHolding(blocks, logRecSpec, key), blockHolding(blocks, logRecState, key)
			So(specBlock, ShouldBeLessThan, stateBlock)

			Convey("a reopen whose decoding of the spec's block ends after the state's block was handed on "+
				"applies the blocks in file order, so k is buried", func() {
				var stateSent sync.Once

				sent := make(chan struct{})

				lateSpec := func(i int, send func()) {
					switch i {
					case specBlock:
						select {
						case <-sent:
						case <-time.After(ckptTestTimeout):
						}
					case stateBlock:
						defer stateSent.Do(func() { close(sent) })
					}

					send()
				}

				logCheckpointSendHook.Store(&lateSpec)

				Reset(func() { logCheckpointSendHook.Store(nil) })

				reopened := reopenLogStore(ctx, s)
				So(reopened.checkpointFrom(), ShouldEqual, cut)
				So(storedLiveJobState(t, reopened, k.Key()), ShouldEqual, JobStateBuried)
			})
		})
}

func TestLogStoreRecoverCheckpointBlocks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store whose checkpoint has more blocks than a recovery decodes ahead", t, func() {
		s, dir := openTestLogStore(t, ctx)

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("c6 blocks "+strings.Repeat("x", 1<<10), "c6", 1000),
			false, nil)
		So(err, ShouldBeNil)

		cut := checkpointed(s)
		want := liveViews(s)

		_, offsets, f, err := openCheckpoint(dir, cut)
		So(err, ShouldBeNil)
		So(f.Close(), ShouldBeNil)
		So(len(offsets), ShouldBeGreaterThan, checkpointBlocksAhead+2)

		So(s.close(ctx), ShouldBeNil)

		Convey("a reopen applies them all, giving the same live set", func() {
			reopened, erro := openLogStoreWithin(ctx, dir)
			So(erro, ShouldBeNil)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			So(reopened.checkpointFrom(), ShouldEqual, cut)
			So(liveViews(reopened), ShouldResemble, want)
		})

		for _, damaged := range []struct {
			name  string
			block int
		}{
			// Block 1 fails while the later blocks are still to be fed, so
			// a feeder that ignored the stop would hang the reopen.
			{"block 1", 1},
			{"middle block", len(offsets) / 2},
		} {
			Convey("a reopen fails with errStoreCorrupt if its "+damaged.name+" is damaged", func() {
				path := checkpointPath(dir, cut)

				b, errr := os.ReadFile(path)
				So(errr, ShouldBeNil)

				b[(offsets[damaged.block]+offsets[damaged.block+1])/2] ^= 0xff
				So(os.WriteFile(path, b, dbFilePermission), ShouldBeNil)

				_, erro := openLogStoreWithin(ctx, dir)
				So(erro, ShouldWrap, errStoreCorrupt)
			})
		}
	})
}

func TestLogStoreCheckpointAppendGenerations(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job whose state change loaded the append generation but is held before it notes "+
		"itself in it", t, func() {
		// Made before the store, so that a failed assertion releases the
		// write before the store's close waits for it.
		loaded, resume := make(chan struct{}), make(chan struct{})

		var once, resumed sync.Once

		resumeWrite := func() { resumed.Do(func() { close(resume) }) }

		Reset(resumeWrite)

		hold := newAppendedHold()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo c6 append generations", "c6")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		paused := func() {
			once.Do(func() {
				close(loaded)
				<-resume
			})
		}

		logEnterAppendHook.Store(&paused)

		Reset(func() { logEnterAppendHook.Store(nil) })

		job.State, job.UntilBuried = JobStateBuried, 0

		hold.arm()

		changed := make(chan error, 1)

		go func() { changed <- s.updateJobAfterChangeDurable(job) }()

		select {
		case <-loaded:
		case <-time.After(ckptTestTimeout):
			So("the change never loaded the append generation", ShouldBeEmpty)
		}

		Convey("once a checkpoint has passed its append barrier, the change appends before the next "+
			"checkpoint's cut, which waits for it to apply, and the change is read after its wal file is gone, "+
			"also after reopen", func() {
			So(s.checkpoint(), ShouldBeNil)

			resumeWrite()
			hold.awaitEntered()

			checkpointDone := make(chan error, 1)

			go func() { checkpointDone <- s.checkpoint() }()

			select {
			case errc := <-checkpointDone:
				So(errc, ShouldBeNil)
				So("the checkpoint ended before the held write had its Refs", ShouldBeEmpty)
			case <-time.After(ckptTestWait):
			}

			hold.Release()

			for _, done := range []chan error{changed, checkpointDone} {
				select {
				case errd := <-done:
					So(errd, ShouldBeNil)
				case <-time.After(ckptTestTimeout):
					So("the change or checkpoint never ended", ShouldBeEmpty)
				}
			}

			So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)

			eachReopen(ctx, &s, func(st *logStore) {
				So(storedLiveJobState(t, st, job.Key()), ShouldEqual, JobStateBuried)
			})
		})
	})
}

func TestLogStoreCheckpointWriteFails(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store checkpointed once, then changed, whose next checkpoint's write fails once it has "+
		"written its blocks", t, func() {
		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("c6 write fails", "c6", 20)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		first := checkpointed(s)

		_, err = s.storeNewJobsGuarded(ctx, testLogJobs("c6 write fails more", "c6", 5), false, nil)
		So(err, ShouldBeNil)

		want := liveViews(s)
		files := walFilesAtOrAfter(dir, first)

		failing := func() error { return errCkptTestWrite }
		logCheckpointWriteHook.Store(&failing)

		Reset(func() { logCheckpointWriteHook.Store(nil) })

		Convey("the checkpoint fails, removing its .ckpt.tmp and keeping the previous checkpoint and the wal "+
			"files from its walFrom, and a reopen gives the same live set", func() {
			So(s.checkpoint(), ShouldWrap, errCkptTestWrite)

			tmps, errg := filepath.Glob(filepath.Join(dir, "*"+storeTmpExt))
			So(errg, ShouldBeNil)
			So(tmps, ShouldBeEmpty)
			So(checkpointFiles(dir), ShouldResemble, []uint32{first})
			So(walFilesAtOrAfter(dir, first)[:len(files)], ShouldResemble, files)

			logCheckpointWriteHook.Store(nil)

			reopened := reopenLogStore(ctx, s)
			So(reopened.checkpointFrom(), ShouldEqual, first)
			So(liveViews(reopened), ShouldResemble, want)
		})
	})
}

func TestLogStoreCheckpointWaitsForReaders(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and 3,000 jobs folded into its history, a live job with "+
		"std and an archived job, their records in a wal file a checkpoint is to drop", t, func() {
		pauseLogFolder()

		// Made before the store, so that a failed assertion releases the
		// reader before the store's close.
		release := make(chan struct{})

		var released sync.Once

		releaseReader := func() { released.Do(func() { close(release) }) }

		Reset(releaseReader)

		s, dir := openTestLogStore(t, ctx)
		growHistory(ctx, s)

		readers := checkpointTestReaders(ctx, s)

		first, err := walFileNumbers(dir)
		So(err, ShouldBeNil)

		for _, reader := range readers {
			Convey("a checkpoint waits for a reader through "+reader.name+" holding Refs below its cut, keeping "+
				"their wal file until it has read them", func() {
				entered := make(chan struct{})

				// Not a sync.Once, whose Do would hold the checkpoint's own
				// reads too until the reader's ended.
				var taken atomic.Bool

				held := func() {
					if taken.CompareAndSwap(false, true) {
						close(entered)
						<-release
					}
				}

				logSegmentFileHook.Store(&held)

				Reset(func() { logSegmentFileHook.Store(nil) })

				readDone := make(chan error, 1)

				go func() { readDone <- reader.read() }()

				select {
				case <-entered:
				case <-time.After(ckptTestTimeout):
					So("the reader never read", ShouldBeEmpty)
				}

				checkpointDone := make(chan error, 1)

				go func() { checkpointDone <- s.checkpoint() }()

				select {
				case errc := <-checkpointDone:
					So(errc, ShouldBeNil)
					So("the checkpoint ended while the reader held its Refs", ShouldBeEmpty)
				case <-time.After(ckptTestWait):
				}

				_, errs := os.Stat(walFilePath(dir, first[0]))
				So(errs, ShouldBeNil)

				releaseReader()

				for _, done := range []chan error{readDone, checkpointDone} {
					select {
					case errd := <-done:
						So(errd, ShouldBeNil)
					case <-time.After(ckptTestTimeout):
						So("the read or checkpoint never ended", ShouldBeEmpty)
					}
				}

				So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)
			})
		}
	})
}

// growHistory adds ckptTestGrownJobs jobs to s, archives and folds them, then
// checkpoints, so that history.bolt has grown as a long-running store's has: a
// fold's commit that must remap a small history.bolt waits for every open read
// of it, which would hold a checkpoint whatever readers' holds do.
func growHistory(ctx context.Context, s *logStore) {
	jobs := testLogJobs("c6 grown", "c6grown", ckptTestGrownJobs)
	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)
	So(archiveConcurrently(s, jobs, time.Now()), ShouldBeNil)
	So(s.foldNow(), ShouldBeNil)
	So(overlayEmpty(s), ShouldBeTrue)

	completed := 0

	for _, job := range jobs {
		if complete, errc := s.checkIfComplete(job.Key()); errc == nil && complete {
			completed++
		}
	}

	So(completed, ShouldEqual, ckptTestGrownJobs)

	checkpointed(s)
}

func TestLogStoreReadersHoldWithTheirRefs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused, a live job with std and an archived job, their records "+
		"in a wal file a checkpoint is to drop", t, func() {
		pauseLogFolder()

		s, dir := openTestLogStore(t, ctx)

		readers := checkpointTestReaders(ctx, s)

		for _, reader := range readers {
			Convey("a checkpoint started as a reader through "+reader.name+" takes its hold keeps the wal file "+
				"of the Refs it copied until it has read them", func() {
				checkpointDone := make(chan error, 1)
				ended := make(chan struct{})

				var taken atomic.Bool

				checkpointing := func() {
					if !taken.CompareAndSwap(false, true) {
						return
					}

					go func() {
						checkpointDone <- s.checkpoint()

						close(ended)
					}()

					select {
					case <-ended:
					case <-time.After(ckptTestWait):
					}
				}

				logHoldRefsHook.Store(&checkpointing)

				Reset(func() { logHoldRefsHook.Store(nil) })

				So(reader.read(), ShouldBeNil)
				So(taken.Load(), ShouldBeTrue)

				select {
				case errc := <-checkpointDone:
					So(errc, ShouldBeNil)
				case <-time.After(ckptTestTimeout):
					So("the checkpoint never ended", ShouldBeEmpty)
				}

				So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)
			})
		}
	})

	Convey("Given a log store with its folder paused and job k added in a wal file a checkpoint is to drop", t,
		func() {
			pauseLogFolder()

			s, dir := openTestLogStore(t, ctx)

			k := testDBJob("echo c6 fold holds", "c6foldholds")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{k}, false, nil)
			So(err, ShouldBeNil)

			Convey("a fold of k's archive, started once the checkpoint has folded to its cut, takes its hold "+
				"with the spec Ref it copied, so the checkpoint keeps k's wal file until the fold has read it",
				func() {
					foldDone := make(chan error, 1)
					entered := make(chan struct{})

					var taken, folding atomic.Bool

					holding := func() {
						if taken.CompareAndSwap(false, true) {
							close(entered)
							time.Sleep(ckptTestWait)
						}
					}

					// It runs on the checkpoint's goroutine, so keeps its error
					// for the test's goroutine to assert.
					var archiveErr error

					archivedThenFolding := func() {
						if !folding.CompareAndSwap(false, true) {
							return
						}

						k.State, k.Exited, k.EndTime = JobStateComplete, true, time.Now()
						if _, archiveErr = s.archiveCompletion(k.Key(), k); archiveErr != nil {
							return
						}

						logHoldRefsHook.Store(&holding)

						go func() { foldDone <- s.foldNow() }()

						select {
						case <-entered:
						case <-time.After(ckptTestTimeout):
						}
					}

					logCheckpointFoldedHook.Store(checkpointFoldedHookFor(s, archivedThenFolding))

					Reset(func() {
						logCheckpointFoldedHook.Store(nil)
						logHoldRefsHook.Store(nil)
					})

					checkpointDone := make(chan error, 1)

					go func() { checkpointDone <- s.checkpoint() }()

					for _, done := range []chan error{checkpointDone, foldDone} {
						select {
						case errd := <-done:
							So(errd, ShouldBeNil)
						case <-time.After(ckptTestTimeout):
							So("the checkpoint or fold never ended", ShouldBeEmpty)
						}
					}

					So(archiveErr, ShouldBeNil)
					So(taken.Load(), ShouldBeTrue)
					So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)
					soComplete(s, k.Key())
				})
		})
}

func TestLogStoreCheckpointCutsBeforeAppendBarrier(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job whose state change starts once a checkpoint's append barrier has started a new "+
		"generation, and is held once it has appended, before it has its Refs", t, func() {
		hold := newAppendedHold()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo c6 append after barrier", "c6")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State, job.UntilBuried = JobStateBuried, 0

		changed := make(chan error, 1)

		var started atomic.Bool

		change := func() {
			if !started.CompareAndSwap(false, true) {
				return
			}

			hold.arm()

			go func() { changed <- s.updateJobAfterChangeDurable(job) }()

			select {
			case <-hold.entered:
			case <-time.After(ckptTestTimeout):
			}
		}

		logAwaitAppendsHook.Store(&change)

		Reset(func() { logAwaitAppendsHook.Store(nil) })

		Convey("the change appended after the checkpoint's cut, so is read after the checkpoint dropped the wal "+
			"files below it, also after reopen", func() {
			checkpointDone := make(chan error, 1)

			go func() { checkpointDone <- s.checkpoint() }()

			select {
			case errc := <-checkpointDone:
				So(errc, ShouldBeNil)
			case <-time.After(ckptTestTimeout):
				So("the checkpoint never ended", ShouldBeEmpty)
			}

			hold.awaitEntered()
			hold.Release()

			select {
			case errd := <-changed:
				So(errd, ShouldBeNil)
			case <-time.After(ckptTestTimeout):
				So("the change never ended", ShouldBeEmpty)
			}

			So(walFilesBelow(dir, s.checkpointFrom()), ShouldBeEmpty)

			eachReopen(ctx, &s, func(st *logStore) {
				So(storedLiveJobState(t, st, job.Key()), ShouldEqual, JobStateBuried)
			})
		})
	})
}

// addUntilWALFiles adds batches of 50 jobs, their commands starting prefix, to
// s until its directory has n wal files numbered first or above, returning the
// first error.
func addUntilWALFiles(ctx context.Context, s *logStore, first uint32, n int, prefix string) error {
	for i := 0; ; i++ {
		files, err := walFileNumbers(s.dir)
		if err != nil {
			return err
		}

		if len(slices.DeleteFunc(files, func(f uint32) bool { return f < first })) >= n {
			return nil
		}

		if _, err = s.storeNewJobsGuarded(ctx, testLogJobs(fmt.Sprintf("%s %d", prefix, i), "c6", 50), false,
			nil); err != nil {
			return err
		}
	}
}

// openLogStoreWithin opens the log store in dir as openLogStore does, returning
// context.DeadlineExceeded if that takes longer than ckptTestTimeout, so that a
// recovery that never ends fails the test instead of hanging it. The caller
// closes it.
func openLogStoreWithin(ctx context.Context, dir string) (*logStore, error) {
	type opened struct {
		s   *logStore
		err error
	}

	done := make(chan opened, 1)

	go func() {
		s, err := openLogStore(ctx, dir)
		done <- opened{s, err}
	}()

	select {
	case o := <-done:
		return o.s, o.err
	case <-time.After(ckptTestTimeout):
		return nil, context.DeadlineExceeded
	}
}

// soCheckpointBlocks returns the records of each block of checkpoint n in dir,
// asserting it has 2 blocks or more, each of at most checkpointBlockBytes of
// frames.
func soCheckpointBlocks(dir string, n uint32) [][]logRecord {
	_, offsets, f, err := openCheckpoint(dir, n)
	So(err, ShouldBeNil)

	defer f.Close()

	So(len(offsets), ShouldBeGreaterThanOrEqualTo, 2)

	blocks := make([][]logRecord, len(offsets))

	for i, off := range offsets {
		size := 0

		So(wal.ReadFrame(f, off, func(_ wal.Ref, rec []byte) error {
			size += wal.FrameHeader + len(rec)

			parsed, errp := parseLogRecord(slices.Clone(rec))
			blocks[i] = append(blocks[i], parsed)

			return errp
		}), ShouldBeNil)

		So(size, ShouldBeLessThanOrEqualTo, checkpointBlockBytes)
	}

	return blocks
}

// blockHolding returns the index of the block of blocks holding k's record of
// type typ, or -1 if none does.
func blockHolding(blocks [][]logRecord, typ logRecordType, k logKey) int {
	for i, block := range blocks {
		for _, rec := range block {
			if rec.typ == typ && rec.key == k {
				return i
			}
		}
	}

	return -1
}

// liveViews returns the view of each of s's live jobs, as recovery reads them.
func liveViews(s *logStore) map[string]recoverView {
	jobs, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	return recoverViews(jobs)
}

// walFilesAtOrAfter returns the wal files in dir numbered n or above.
func walFilesAtOrAfter(dir string, n uint32) []uint32 {
	files, err := walFileNumbers(dir)
	So(err, ShouldBeNil)

	return slices.DeleteFunc(files, func(f uint32) bool { return f < n })
}

// checkpointFiles returns the numbers of the checkpoint files in dir, ascending.
func checkpointFiles(dir string) []uint32 {
	numbers, err := checkpointNumbers(dir)
	So(err, ShouldBeNil)

	return numbers
}

// checkpointed makes a checkpoint of s, asserting it succeeded, and returns its
// cut.
func checkpointed(s *logStore) uint32 {
	So(s.checkpoint(), ShouldBeNil)

	cut := s.checkpointFrom()
	So(cut, ShouldBeGreaterThan, 0)

	return cut
}

// checkpointFoldedHookFor returns a logCheckpointFoldedHook that calls fn for
// s's checkpoints only: the hook is global, so a store another test left open,
// whose checkpoints logCheckpointEvery may make due, calls it too.
func checkpointFoldedHookFor(s *logStore, fn func()) *func(*logStore) {
	hook := func(st *logStore) {
		if st == s {
			fn()
		}
	}

	return &hook
}

// walFilesBelow returns the wal files in dir numbered below cut.
func walFilesBelow(dir string, cut uint32) []uint32 {
	files, err := walFileNumbers(dir)
	So(err, ShouldBeNil)

	return slices.DeleteFunc(files, func(n uint32) bool { return n >= cut })
}

// truncateLastByte removes the last byte of the file at path.
func truncateLastByte(path string) {
	info, err := os.Stat(path)
	So(err, ShouldBeNil)
	So(os.Truncate(path, info.Size()-1), ShouldBeNil)
}

// specMismatch returns an error unless the record at ref, in a checkpoint
// file, is k's spec, of job.
func specMismatch(s *logStore, ref wal.Ref, k logKey, job *Job) error {
	parsed, err := checkpointedRecord(s, ref, logRecSpec, k)
	if err != nil {
		return err
	}

	var decoded Job

	if err = decodeFlatSpec(parsed.body, &decoded, s.ch); err != nil {
		return err
	}

	if decoded.Cmd != job.Cmd || decoded.RepGroup != job.RepGroup {
		return fmt.Errorf("%w: spec of %x is %q in %q", errCkptTestRead, k, decoded.Cmd, decoded.RepGroup)
	}

	return nil
}

// stateMismatch returns an error unless the record at ref, in a checkpoint
// file, is k's state record of job's last state.
func stateMismatch(s *logStore, ref wal.Ref, k logKey, job *Job) error {
	parsed, err := checkpointedRecord(s, ref, logRecState, k)
	if err != nil {
		return err
	}

	var rs logRunState

	if err = decodeFlatRunState(parsed.body, &rs); err != nil {
		return err
	}

	if rs.State != job.State || rs.Attempts != job.Attempts {
		return fmt.Errorf("%w: state of %x is %s after %d attempts", errCkptTestRead, k, rs.State, rs.Attempts)
	}

	return nil
}

// stdMismatch returns an error unless the record at ref, in a checkpoint file,
// is k's std record of stream, "o" or "e" then the end of job's Cmd.
func stdMismatch(s *logStore, ref wal.Ref, k logKey, stream uint8, job *Job) error {
	parsed, err := checkpointedRecord(s, ref, logRecStd, k)
	if err != nil {
		return err
	}

	want := "o"
	if stream == stdErr {
		want = "e"
	}

	want += strings.TrimPrefix(job.Cmd, "echo c6 refs ")

	if parsed.stream != stream || string(parsed.body) != want {
		return fmt.Errorf("%w: std of %x is %q in stream %d", errCkptTestRead, k, parsed.body, parsed.stream)
	}

	return nil
}

// checkpointedRecord reads the record ref names in s's directory with
// wal.ReadAt and returns it parsed, or an error unless it is in a checkpoint
// file and is k's record of type typ.
func checkpointedRecord(s *logStore, ref wal.Ref, typ logRecordType, k logKey) (logRecord, error) {
	if ref.File&checkpointRefFlag == 0 {
		return logRecord{}, fmt.Errorf("%w: %x's Ref %v is not in a checkpoint", errCkptTestRead, k, ref)
	}

	f, err := os.Open(recordFilePath(s.dir, ref.File))
	if err != nil {
		return logRecord{}, err
	}

	defer f.Close()

	rec, err := wal.ReadAt(f, ref)
	if err != nil {
		return logRecord{}, err
	}

	parsed, err := parseLogRecord(rec)
	if err == nil && (parsed.typ != typ || parsed.key != k) {
		err = fmt.Errorf("%w: %x's Ref %v holds a record of type %d of %x", errCkptTestRead, k, ref, parsed.typ,
			parsed.key)
	}

	return parsed, err
}

// checkpointFrom returns the walFrom of the newest checkpoint, or 0 if there is
// none.
func (s *logStore) checkpointFrom() uint32 {
	s.ckptMu.Lock()
	defer s.ckptMu.Unlock()

	return s.ckptFrom
}
