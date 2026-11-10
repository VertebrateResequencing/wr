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
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// depReadFewWaiters and depReadManyWaiters are the sizes of the two dep
	// groups whose one-member adds TestLogStoreDependentsReadCost compares.
	depReadFewWaiters  = 10
	depReadManyWaiters = 500

	// depReadSlack is how many more record file reads the larger group's add
	// may make than the smaller's, which reads its waiters' one block once:
	// the background folder's reads of the wal file, one per round a second
	// apart, of which an add this small spans at most two.
	depReadSlack = 2

	// overlayReadJobs is how many jobs, stored in one add, then archived one by
	// one, TestLogStoreOverlayReadCost queries.
	overlayReadJobs = 200

	// overlayFoldWait bounds the wait for the folder to start its first round,
	// which it does a foldInterval after the store opens.
	overlayFoldWait = 10 * foldInterval
)

func TestLogStoreDependentsAcrossFrames(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given the waiters on a dep group stored in three adds, each its own block", t, func() {
		s, _ := openTestLogStore(t, ctx)

		want := make(map[string]string)

		for add := range 3 {
			jobs := testLogJobs("across frames "+strconv.Itoa(add), "acrossframes", depReadFewWaiters)
			for _, job := range jobs {
				job.Dependencies = Dependencies{NewDepGroupDependency("acrossframes")}
				want[job.Key()] = job.Cmd
			}

			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)
		}

		Convey("a one-member add finds every waiter as it was stored", func() {
			member := testDBJob("echo across frames member", "acrossframes")
			member.DepGroups = []string{"acrossframes"}

			stored, err := s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
			So(err, ShouldBeNil)

			got := make(map[string]string, len(stored.dependents.jobs))
			for _, job := range stored.dependents.jobs {
				got[job.Key()] = job.Cmd
			}

			So(got, ShouldResemble, want)
		})
	})
}

func TestLogStoreRecordBodyType(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job's spec record", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo record type", "recordtype")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		s.mu.RLock()
		ref := s.state.live[testLogKey(job)].spec
		s.mu.RUnlock()

		rec, err := s.readRecord(ref)
		So(err, ShouldBeNil)

		Convey("its body is given as a spec, but not as a run state", func() {
			body, err := keyedBody(rec, ref, logRecSpec)
			So(err, ShouldBeNil)
			So(body, ShouldNotBeEmpty)

			_, err = keyedBody(rec, ref, logRecState)
			So(err, ShouldWrap, errLogRecordMalformed)
		})
	})
}

func TestLogStoreDependentsReadCost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group with few waiters and one with many, each added in one add", t, func() {
		few, fewDeps := depReadMemberAdd(t, ctx, "few", depReadFewWaiters)
		many, manyDeps := depReadMemberAdd(t, ctx, "many", depReadManyWaiters)

		Convey("a one-member add finds every waiter, reading no more frames for the larger group", func() {
			So(fewDeps, ShouldEqual, depReadFewWaiters)
			So(manyDeps, ShouldEqual, depReadManyWaiters)
			So(few, ShouldBeGreaterThan, 0)
			So(many, ShouldBeLessThanOrEqualTo, few+depReadSlack)
		})
	})
}

// depReadMemberAdd stores waiters jobs depending on dep group g in one add, to
// a log store of its own, then adds one member of g, returning how many record
// file reads that add made and how many live dependents it found.
func depReadMemberAdd(t *testing.T, ctx context.Context, g string, waiters int) (int, int) {
	t.Helper()

	s, _ := openTestLogStore(t, ctx)

	jobs := make([]*Job, waiters)

	for i := range jobs {
		jobs[i] = testDBJob("echo waiter "+strconv.Itoa(i), "depread")
		jobs[i].Dependencies = Dependencies{NewDepGroupDependency(g)}
	}

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	member := testDBJob("echo member", "depread")
	member.DepGroups = []string{g}

	var stored storedNewJobs

	reads := countRecordFileReads(func() {
		stored, err = s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
	})

	So(err, ShouldBeNil)

	return reads, len(stored.dependents.jobs)
}

func TestLogStoreOverlayReadCost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given jobs stored in one add, archived one by one and not yet folded", t, func() {
		folding := holdFirstFold()
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("overlay read", "overlayread", overlayReadJobs)
		for _, job := range jobs {
			job.Dependencies = Dependencies{NewDepGroupDependency("overlayread")}
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		for _, job := range jobs {
			job.State, job.Exited, job.EndTime = JobStateComplete, true, time.Now()
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)
		}

		select {
		case <-folding:
		case <-time.After(overlayFoldWait):
			So("the folder never started a round", ShouldBeEmpty)
		}

		frames, recordBytes := overlayFrames(s)
		So(frames, ShouldBeLessThan, 2*overlayReadJobs)

		Convey("a query of their rep group reads each frame holding their records once", func() {
			var got []*Job

			reads := countRecordFileReads(func() {
				got, err = s.retrieveCompleteJobsByRepGroup("overlayread")
			})

			So(err, ShouldBeNil)
			So(got, ShouldHaveLength, overlayReadJobs)
			So(reads, ShouldEqual, frames)
		})

		Convey("adding a member of the dep group they wait on reads each frame once, putting them all back", func() {
			member := testDBJob("echo overlay read member", "overlayread")
			member.DepGroups = []string{"overlayread"}

			var stored storedNewJobs

			reads := countRecordFileReads(func() {
				stored, err = s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
			})

			So(err, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, overlayReadJobs+1)
			So(reads, ShouldEqual, frames)
		})

		Convey("pricing their rep group's archived bytes reads each frame once, and prices their records", func() {
			budget := newArchivedBytesBudget()

			reads := countRecordFileReads(func() {
				err = s.spendArchivedBytesByRepGroup("overlayread", budget)
			})

			So(err, ShouldBeNil)
			So(reads, ShouldEqual, frames)
			So(budget.priced, ShouldEqual, overlayReadJobs)
			So(maxArchivedBytes-budget.remaining, ShouldEqual, recordBytes)
		})
	})
}

func TestLogStoreHistoryJobBadRecord(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given an archived job not yet folded whose overlay entry's spec Ref names its complete record", t,
		func() {
			folding := holdFirstFold()
			s, _ := openTestLogStore(t, ctx)

			job := testDBJob("echo bad overlay record", "badoverlay")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			job.State, job.Exited, job.EndTime = JobStateComplete, true, time.Now()
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			select {
			case <-folding:
			case <-time.After(overlayFoldWait):
				So("the folder never started a round", ShouldBeEmpty)
			}

			k := testLogKey(job)

			s.mu.Lock()
			bad := *s.state.overlay[k]
			bad.spec = bad.complete
			s.state.overlay[k] = &bad
			s.mu.Unlock()

			Convey("reading its history entry is an error", func() {
				_, _, err := s.historyJob(k)
				So(err, ShouldWrap, errLogRecordMalformed)
			})
		})
}

func TestLogStoreConcurrentFrameReads(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given live waiters stored in one add, and jobs of another add archived and not yet folded", t, func() {
		folding := holdFirstFold()
		s, _ := openTestLogStore(t, ctx)

		waiters := testLogJobs("concurrent waiter", "concurrentlive", overlayReadJobs)
		waiterKeys := make([]logKey, len(waiters))

		for i, job := range waiters {
			job.Dependencies = Dependencies{NewDepGroupDependency("concurrent")}
			waiterKeys[i] = testLogKey(job)
		}

		_, err := s.storeNewJobsGuarded(ctx, waiters, false, nil)
		So(err, ShouldBeNil)

		archived := testLogJobs("concurrent archived", "concurrentarchived", overlayReadJobs)
		_, err = s.storeNewJobsGuarded(ctx, archived, false, nil)
		So(err, ShouldBeNil)

		for _, job := range archived {
			job.State, job.Exited, job.EndTime = JobStateComplete, true, time.Now()
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)
		}

		select {
		case <-folding:
		case <-time.After(overlayFoldWait):
			So("the folder never started a round", ShouldBeEmpty)
		}

		Convey("reading both at once from many goroutines gives every job as it was stored", func() {
			const readers = 8

			wrong := make(chan string, 2*readers)

			var wg sync.WaitGroup

			for range readers {
				wg.Go(func() { wrong <- concurrentLiveRead(s, waiterKeys, waiters) })
				wg.Go(func() { wrong <- concurrentOverlayRead(s, archived) })
			}

			wg.Wait()
			close(wrong)

			var problems []string

			for p := range wrong {
				if p != "" {
					problems = append(problems, p)
				}
			}

			So(problems, ShouldBeEmpty)
		})
	})
}

// holdFirstFold holds the folder's first round once it has read the log, before
// it folds anything, until the Convey block ends, returning a channel closed
// once it is held. Make it before the store, so the release comes before the
// store's close, which waits for the folder.
func holdFirstFold() <-chan struct{} {
	held, release := make(chan struct{}), make(chan struct{})

	var once sync.Once

	hold := func() {
		once.Do(func() {
			close(held)
			<-release
		})
	}

	logFoldReadHook.Store(&hold)
	Reset(func() {
		close(release)
		logFoldReadHook.Store(nil)
	})

	return held
}

// overlayFrames returns how many frames hold the spec and complete records of
// s's overlay entries, and those records' total length, each read through its
// Ref.
func overlayFrames(s *logStore) (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	frames := make(map[wal.Ref]bool)
	n := 0

	for _, h := range s.state.overlay {
		for _, ref := range []wal.Ref{h.spec, h.complete} {
			frames[wal.Ref{File: ref.File, Off: ref.Off}] = true

			rec, err := s.readRecord(ref)
			So(err, ShouldBeNil)

			n += len(rec)
		}
	}

	return len(frames), n
}

// countRecordFileReads returns how many reads of a record file fn makes.
func countRecordFileReads(fn func()) int {
	var reads atomic.Int64

	count := func() { reads.Add(1) }

	logSegmentFileHook.Store(&count)
	defer logSegmentFileHook.Store(nil)

	fn()

	return int(reads.Load())
}

func TestLogStoreReadBufsBound(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given waiters on a dep group stored in one add, read as an add's dependents", t, func() {
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("bound waiter", "bound", overlayReadJobs)
		keys := make([]logKey, len(jobs))

		for i, job := range jobs {
			keys[i] = testLogKey(job)
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		kept := func() int {
			s.readBufsMu.Lock()
			defer s.readBufsMu.Unlock()

			if s.readBufs == nil {
				return 0
			}

			return s.readBufs.Size()
		}

		Convey("the store keeps the buffers it read their block into, up to readBufsMax and no more", func() {
			_, err = s.liveJobsOf(keys)
			So(err, ShouldBeNil)

			size := kept()
			So(size, ShouldBeGreaterThan, 0)

			old := readBufsMax

			Reset(func() { readBufsMax = old })

			readBufsMax = size - 1

			_, err = s.liveJobsOf(keys)
			So(err, ShouldBeNil)
			So(kept(), ShouldEqual, 0)

			readBufsMax = size

			_, err = s.liveJobsOf(keys)
			So(err, ShouldBeNil)
			So(kept(), ShouldEqual, size)
		})
	})
}

// testLogKey returns job's key as a logKey.
func testLogKey(job *Job) logKey {
	k, err := parseLogKey(job.Key())
	So(err, ShouldBeNil)

	return k
}

// concurrentLiveRead reads keys' live jobs, returning what was wrong with
// them compared to jobs, or "".
func concurrentLiveRead(s *logStore, keys []logKey, jobs []*Job) string {
	got, err := s.liveJobsOf(keys)
	if err != nil {
		return err.Error()
	}

	for i, job := range jobs {
		if got[i] == nil || got[i].Cmd != job.Cmd {
			return "live job " + job.Key() + " read wrong"
		}
	}

	return ""
}

// concurrentOverlayRead reads jobs' rep group's archived jobs, returning what
// was wrong with them, or "".
func concurrentOverlayRead(s *logStore, jobs []*Job) string {
	got, err := s.retrieveCompleteJobsByRepGroup(jobs[0].RepGroup)
	if err != nil {
		return err.Error()
	}

	want := make(map[string]string, len(jobs))
	for _, job := range jobs {
		want[job.Key()] = job.Cmd
	}

	for _, job := range got {
		if want[job.Key()] != job.Cmd {
			return "archived job " + job.Key() + " read wrong"
		}

		delete(want, job.Key())
	}

	if len(want) > 0 {
		return strconv.Itoa(len(want)) + " archived jobs not read"
	}

	return ""
}
