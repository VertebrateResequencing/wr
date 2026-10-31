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

// Tests of the log store's recovery (spec C5).

import (
	"context"
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
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

// errRecoverRun is the error of a random operation that did not do what the
// server relies on.
var errRecoverRun = errors.New("random operation went wrong")

const (
	// recoverRandomOps is how many random operations C5 test 1 makes.
	recoverRandomOps = 500

	// recoverMemoryJobs and recoverMemoryJobSize are C5 test 6's live jobs:
	// this many, each with a spec of about this many bytes.
	recoverMemoryJobs    = 100000
	recoverMemoryJobSize = 1024

	// recoverMemoryAddChunk is how many of those jobs one add stores.
	recoverMemoryAddChunk = 10000

	// recoverMemoryLimit is the most the store's own heap may grow by in
	// recovering them.
	recoverMemoryLimit = 40 << 20

	// recoverExtraBytes is how many bytes C5 test 4 puts past file 1's durable
	// end.
	recoverExtraBytes = 50
)

// recoverRun makes random operations on a log store, keeping each job live
// after them as the server would hold it, and the first error an operation got.
type recoverRun struct {
	ctx    context.Context //nolint:containedctx // the test's
	s      *logStore
	rng    *rand.Rand
	prefix string
	live   map[string]*Job
	added  int
	made   map[string]int
	err    error
}

// recoverOps are the operations step chooses from, by name.
//
//nolint:gochecknoglobals // a constant table
var recoverOps = map[string]func(r *recoverRun) bool{
	"reserves": (*recoverRun).reserve,
	"starts":   (*recoverRun).start,
	"archives": (*recoverRun).archive,
	"releases": (*recoverRun).release,
	"deletes":  (*recoverRun).remove,
	"modifies": (*recoverRun).modify,
	"kicks":    (*recoverRun).kick,
}

func TestLogStoreTornTail(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store whose last wal file has 10 garbage bytes appended", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)
		s, dir := openTestLogStore(t, logCtx)

		job := testDBJob("echo torn", "torn")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(dir)
		So(err, ShouldBeNil)

		last := walFilePath(dir, files[len(files)-1])
		info, err := os.Stat(last)
		So(err, ShouldBeNil)

		f, err := os.OpenFile(last, os.O_APPEND|os.O_WRONLY, 0)
		So(err, ShouldBeNil)
		_, err = f.WriteString("0123456789")
		So(err, ShouldBeNil)
		So(f.Close(), ShouldBeNil)

		Convey("it reopens with its jobs, the tail truncated and logged", func() {
			reopened := openLogStoreIn(logCtx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			after, errs := os.Stat(last)
			So(errs, ShouldBeNil)
			So(after.Size(), ShouldEqual, info.Size())

			live, errl := reopened.checkIfLive(job.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)

			lines := logLinesContaining(readLogFile(t, logPath), walTailTruncatedLogMsg)
			So(lines, ShouldHaveLength, 1)
			So(logLineValue(lines[0], "bytes"), ShouldEqual, "10")

			Convey("and a further reopen keeps every write", func() {
				more := testDBJob("echo torn more", "torn")
				_, err = reopened.storeNewJobsGuarded(ctx, []*Job{more}, false, nil)
				So(err, ShouldBeNil)

				again := reopenLogStore(ctx, reopened)
				So(inspectedCount(again, kindLive), ShouldEqual, 2)
			})
		})
	})
}

// TestLogStoreRecoverAbandonedTail covers spec C5 test 4: bytes past the
// durable end a later header gives an abandoned file, its failed batch's
// included, are truncated and not replayed.
func TestLogStoreRecoverAbandonedTail(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store holding job a, whose add of b fails its sync, then whose add of c succeeds", t, func() {
		var failing atomic.Bool

		Reset(wal.SetTestSyncHook(func(path string) error {
			if strings.HasSuffix(path, walFileExt) && failing.CompareAndSwap(true, false) {
				return syscall.EIO
			}

			return nil
		}))

		s, dir := openTestLogStore(t, ctx)

		a, b, c := testDBJob("echo abandoned a", "c5"), testDBJob("echo abandoned b", "c5"),
			testDBJob("echo abandoned c", "c5")

		_, err := s.storeNewJobsGuarded(ctx, []*Job{a}, false, nil)
		So(err, ShouldBeNil)

		file1 := walFilePath(dir, 1)
		info, err := os.Stat(file1)
		So(err, ShouldBeNil)

		failing.Store(true)

		_, err = s.storeNewJobsGuarded(ctx, []*Job{b}, false, nil)
		So(err, ShouldWrap, syscall.EIO)

		_, err = s.storeNewJobsGuarded(ctx, []*Job{c}, false, nil)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		prevFile, prevEnd, whole, err := s.walHeader(2)
		So(err, ShouldBeNil)
		So(whole, ShouldBeTrue)
		So(prevFile, ShouldEqual, 1)
		So(prevEnd, ShouldEqual, info.Size())

		f, err := os.OpenFile(file1, os.O_APPEND|os.O_WRONLY, 0)
		So(err, ShouldBeNil)
		_, err = f.Write(make([]byte, recoverExtraBytes))
		So(err, ShouldBeNil)
		So(f.Close(), ShouldBeNil)

		after, err := os.Stat(file1)
		So(err, ShouldBeNil)
		So(after.Size(), ShouldBeGreaterThanOrEqualTo, prevEnd+recoverExtraBytes)

		Convey("when 50 bytes are put past file 1's durable end, reopening truncates file 1 to it, keeps a "+
			"and c and does not replay b", func() {
			reopened := openLogStoreIn(ctx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			truncated, errs := os.Stat(file1)
			So(errs, ShouldBeNil)
			So(truncated.Size(), ShouldEqual, prevEnd)

			for job, want := range map[*Job]bool{a: true, b: false, c: true} {
				live, errl := reopened.checkIfLive(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldEqual, want)
			}
		})
	})
}

// TestLogStoreRecoverMemory covers spec C5 test 6: recovering 100,000 live
// 1KiB jobs grows the store's own heap by less than 40MB.
func TestLogStoreRecoverMemory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store holding 100,000 live 1KiB jobs", t, func() {
		s, dir := openTestLogStore(t, ctx)

		pad := strings.Repeat("x", recoverMemoryJobSize)

		for chunk := range recoverMemoryJobs / recoverMemoryAddChunk {
			jobs := make([]*Job, recoverMemoryAddChunk)
			for i := range jobs {
				jobs[i] = testDBJob(fmt.Sprintf("echo %d %s", chunk*recoverMemoryAddChunk+i, pad), "c5memory")
			}

			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)
		}

		So(s.close(ctx), ShouldBeNil)

		Convey("recovering them grows the store's own heap by less than 40MB", func() {
			var reopened *logStore

			Reset(func() {
				if reopened != nil {
					So(reopened.close(ctx), ShouldBeNil)
				}
			})

			growth := dgmRetainedGrowth(func() any {
				reopened = openLogStoreIn(ctx, dir)

				jobs, _, err := reopened.recoverIncompleteJobs()
				So(err, ShouldBeNil)
				So(jobs, ShouldHaveLength, recoverMemoryJobs)

				return reopened
			})

			t.Logf("store heap growth recovering %d jobs: %d bytes", recoverMemoryJobs, growth)
			So(growth, ShouldBeLessThan, recoverMemoryLimit)
		})
	})
}

// TestLogStoreRecoverFailedBatch covers spec C5 test 7, without "after the
// folder has run" (item 1d.1): an add, an archive and an exit whose shared
// batch fails leave no trace, in memory or after reopen.
func TestLogStoreRecoverFailedBatch(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given live k2 running and k3 running after an earlier exit 1 with std x and y", t, func() {
		var (
			armed    atomic.Bool
			appended atomic.Int32
		)

		logAppendedHook = func() { appended.Add(1) }

		Reset(func() { logAppendedHook = nil })

		Reset(wal.SetTestSyncHook(func(path string) error {
			if !strings.HasSuffix(path, walFileExt) || !armed.CompareAndSwap(true, false) {
				return nil
			}

			if !pollUntilFor(logSyncHoldWait, func() bool { return appended.Load() >= 3 }) {
				return nil
			}

			return syscall.EIO
		}))

		s, _ := openTestLogStore(t, ctx)

		k1, k2, k3 := testDBJob("echo failed batch k1", "c5"), testDBJob("echo failed batch k2", "c5"),
			testDBJob("echo failed batch k3", "c5")

		_, err := s.storeNewJobsGuarded(ctx, []*Job{k2, k3}, false, nil)
		So(err, ShouldBeNil)

		for _, job := range []*Job{k2, k3} {
			job.State, job.Host, job.Pid, job.Attempts = JobStateRunning, "c5host", 1, 1
			So(s.updateJobRunStateDurable(job), ShouldBeNil)
		}

		k3.State, k3.Exited, k3.Exitcode = JobStateDelayed, true, 1
		So(s.updateJobAfterExitDurable(k3, []byte("x"), []byte("y"), false), ShouldBeNil)

		k3.State, k3.Exited, k3.Exitcode, k3.Attempts = JobStateRunning, false, 0, 2
		So(s.updateJobRunStateDurable(k3), ShouldBeNil)

		Convey("when an add of k1, the archive of k2 and k3's exit 1 with std o and e share a batch whose "+
			"sync fails, each returns an error and none has an effect, also after reopen", func() {
			appended.Store(0)
			armed.Store(true)

			k2.State, k2.Exited, k2.EndTime = JobStateComplete, true, time.Now()
			k3.State, k3.Exited, k3.Exitcode = JobStateDelayed, true, 1

			errs := make(chan error, 3)

			go func() {
				_, erra := s.storeNewJobsGuarded(ctx, []*Job{k1}, false, nil)
				errs <- erra
			}()

			go func() {
				_, erra := s.archiveCompletion(k2.Key(), k2)
				errs <- erra
			}()

			go func() {
				errs <- s.updateJobAfterExitDurable(k3, []byte("o"), []byte("e"), false)
			}()

			for range 3 {
				select {
				case erra := <-errs:
					So(erra, ShouldWrap, syscall.EIO)
				case <-time.After(logSyncHoldWait):
					So("a write never returned", ShouldBeEmpty)
				}
			}

			So(armed.Load(), ShouldBeFalse)

			assertUnchanged := func(st *logStore) {
				live, errl := st.checkIfLive(k2.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)

				complete, errc := st.checkIfComplete(k2.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeFalse)

				stdo, stde := st.retrieveJobStd(ctx, k3.Key())
				So(string(stdo), ShouldEqual, "x")
				So(string(stde), ShouldEqual, "y")
			}

			live, errl := s.checkIfLive(k1.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeFalse)
			assertUnchanged(s)

			stored, err := s.storeNewJobsGuarded(ctx, []*Job{k1}, false, nil)
			So(err, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 1)
			So(stored.jobsToQueue[0].Key(), ShouldEqual, k1.Key())

			live, errl = s.checkIfLive(k1.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)

			reopened := reopenLogStore(ctx, s)

			live, errl = reopened.checkIfLive(k1.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)
			assertUnchanged(reopened)
		})
	})
}

// TestLogStoreRecoverReopenedRelease covers spec C5 test 8, without its
// checkpoint (item 1d.2) and converted store (item 1f.10): a release made after
// a reopen outranks the reservation recovered by it at the next reopen.
func TestLogStoreRecoverReopenedRelease(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given live job k reserved, with Attempts 0", t, func() {
		s, _ := openTestLogStore(t, ctx)

		k := testDBJob("echo reopened release", "c5")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{k}, false, nil)
		So(err, ShouldBeNil)

		k.State, k.ReservedBy = JobStateReserved, uuid.Must(uuid.NewV4())
		So(s.updateJobRunStateDurable(k), ShouldBeNil)

		Convey("when the store is reopened, k released with exit 1 and the store reopened again, k is to "+
			"be ready with Attempts 1", func() {
			reopened := reopenLogStore(ctx, s)

			recovered, found, errl := reopened.liveJob(k.Key())
			So(errl, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(recovered.State, ShouldEqual, JobStateReserved)
			So(recovered.Attempts, ShouldEqual, 0)

			recovered.State, recovered.Attempts, recovered.Exited, recovered.Exitcode =
				JobStateDelayed, 1, true, 1
			So(reopened.updateJobAfterExitDurable(recovered, nil, nil, false), ShouldBeNil)

			again := reopenLogStore(ctx, reopened)

			jobs, _, errr := again.recoverIncompleteJobs()
			So(errr, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].State, ShouldEqual, JobStateDelayed)
			So(recoversIntoRun(jobs[0]), ShouldBeFalse)
			So(jobs[0].Attempts, ShouldEqual, 1)
		})
	})
}

// step makes one random operation, or an add if none of the jobs live can take
// the one chosen.
func (r *recoverRun) step() {
	names := slices.Sorted(maps.Keys(recoverOps))

	choice := r.rng.IntN(len(names) + 1)
	if choice < len(names) && recoverOps[names[choice]](r) {
		r.count(names[choice])

		return
	}

	r.add()
}

// count notes that an operation of name was made.
func (r *recoverRun) count(name string) {
	r.made[name]++
}

// check keeps err if it is the first.
func (r *recoverRun) check(err error) {
	if r.err == nil {
		r.err = err
	}
}

// pick returns a random live job in one of states, or nil.
func (r *recoverRun) pick(states ...JobState) *Job {
	var keys []string

	for key, job := range r.live {
		if slices.Contains(states, job.State) {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return nil
	}

	slices.Sort(keys)

	return r.live[keys[r.rng.IntN(len(keys))]]
}

// add stores a new ready job.
func (r *recoverRun) add() {
	r.added++
	job := testDBJob(fmt.Sprintf("%s add %d", r.prefix, r.added), "c5")
	job.State = JobStateReady
	job.Priority = uint8(r.rng.IntN(256)) //nolint:gosec // in range

	_, err := r.s.storeNewJobsGuarded(r.ctx, []*Job{job}, false, nil)
	r.check(err)

	r.live[job.Key()] = job
	r.count("adds")
}

// reserve reserves a ready or delayed job for a new runner.
func (r *recoverRun) reserve() bool {
	job := r.pick(JobStateReady, JobStateDelayed)
	if job == nil {
		return false
	}

	var id uuid.UUID
	for i := range id {
		id[i] = byte(r.rng.IntN(256)) //nolint:gosec // in range
	}

	job.State, job.ReservedBy = JobStateReserved, id

	r.check(r.s.updateJobRunStateDurable(job))

	return true
}

// start starts a reserved job, as applyJobStart does.
func (r *recoverRun) start() bool {
	job := r.pick(JobStateReserved)
	if job == nil {
		return false
	}

	job.State, job.Host, job.Pid = JobStateRunning, "c5host", 100+r.rng.IntN(1000)
	job.StartTime, job.EndTime = time.Now(), time.Time{}
	job.Attempts++

	r.check(r.s.updateJobRunStateDurable(job))

	return true
}

// archive completes a running job.
func (r *recoverRun) archive() bool {
	job := r.pick(JobStateRunning)
	if job == nil {
		return false
	}

	job.State, job.Exited, job.Exitcode, job.EndTime = JobStateComplete, true, 0, time.Now()

	outcome, err := r.s.archiveCompletion(job.Key(), job)
	r.check(err)

	if outcome != archiveRemovedLive {
		r.check(fmt.Errorf("%w: archive of %s had outcome %d", errRecoverRun, job.Key(), outcome))
	}

	delete(r.live, job.Key())

	return true
}

// release ends a running job's run with exit 1, delaying it to run again or
// burying it, as applyReleaseLocked does.
func (r *recoverRun) release() bool {
	job := r.pick(JobStateRunning)
	if job == nil {
		return false
	}

	job.Exited, job.Exitcode, job.EndTime = true, 1, time.Now()
	job.State = JobStateDelayed

	if r.rng.IntN(2) == 0 {
		job.State, job.UntilBuried = JobStateBuried, 0
	}

	r.check(r.s.updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false))

	return true
}

// kick unburies a buried job, as kickJobs does.
func (r *recoverRun) kick() bool {
	job := r.pick(JobStateBuried)
	if job == nil {
		return false
	}

	kickChange(job)

	r.check(r.s.updateJobAfterChangeDurable(job))

	return true
}

// remove deletes a job that is not running.
func (r *recoverRun) remove() bool {
	job := r.pick(JobStateReady, JobStateDelayed, JobStateBuried)
	if job == nil {
		return false
	}

	r.check(r.s.deleteLiveJobs(r.ctx, []string{job.Key()}))

	delete(r.live, job.Key())

	return true
}

// modify changes a job that is not running: its command, which changes its key,
// or its priority and RAM, which do not.
func (r *recoverRun) modify() bool {
	job := r.pick(JobStateReady, JobStateDelayed, JobStateBuried)
	if job == nil {
		return false
	}

	cmd := job.Cmd

	if r.rng.IntN(2) == 0 {
		r.added++
		cmd = fmt.Sprintf("%s modified %d", r.prefix, r.added)
	}

	modified := testDBJob(cmd, job.RepGroup)
	modified.State, modified.Attempts, modified.ReservedBy = job.State, job.Attempts, job.ReservedBy
	modified.UntilBuried, modified.Exited, modified.Exitcode = job.UntilBuried, job.Exited, job.Exitcode
	modified.Priority = uint8(r.rng.IntN(256)) //nolint:gosec // in range
	modified.Requirements.RAM = 1 + r.rng.IntN(1000)

	r.check(r.s.modifyLiveJobs(r.ctx, []string{job.Key()}, []*Job{modified}))

	delete(r.live, job.Key())
	r.live[modified.Key()] = modified

	return true
}

// TestLogStoreRecoverRandomOps covers spec C5 test 1, without its checkpoints
// (item 1d.2): a store reopened after random operations gives back the live
// jobs the operations left, closed or not, and rebuilds the same in-memory
// state its done funcs had built.
func TestLogStoreRecoverRandomOps(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, goroutines := range []int{1, 4} {
		Convey(fmt.Sprintf("Given 500 random adds, reserves, starts, archives, releases, deletes, modifies and "+
			"kicks on a log store from %d goroutine(s), and the jobs they leave live as the server holds them",
			goroutines), t, func() {
			s, dir := openTestLogStore(t, ctx)

			runs := make([]*recoverRun, goroutines)
			for i := range runs {
				runs[i] = &recoverRun{
					ctx: ctx, s: s, prefix: fmt.Sprintf("echo c5 run %d", i),
					rng:  rand.New(rand.NewPCG(5, uint64(i))), //nolint:gosec // a fixed seed, for a reproducible run
					live: make(map[string]*Job), made: make(map[string]int),
				}
			}

			var wg sync.WaitGroup

			for _, run := range runs {
				wg.Go(func() {
					for range recoverRandomOps / goroutines {
						run.step()
					}
				})
			}

			wg.Wait()

			want := make(map[string]recoverView)

			for _, run := range runs {
				So(run.err, ShouldBeNil)

				for _, name := range append(slices.Sorted(maps.Keys(recoverOps)), "adds") {
					So(run.made[name], ShouldBeGreaterThan, 0)
				}

				maps.Copy(want, recoverViews(slices.Collect(maps.Values(run.live))))
			}

			crashed := filepath.Join(t.TempDir(), "crashed")
			So(os.CopyFS(crashed, os.DirFS(dir)), ShouldBeNil)
			So(s.close(ctx), ShouldBeNil)

			for name, reopenDir := range map[string]string{"closed": dir, "not closed": crashed} {
				Convey("when the "+name+" store is reopened, its live jobs and in-memory state are the same",
					func() {
						reopened := openLogStoreIn(ctx, reopenDir)

						Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

						So(comparableLogState(&reopened.state), ShouldResemble, comparableLogState(&s.state))

						jobs, rsr, err := reopened.recoverIncompleteJobs()
						So(err, ShouldBeNil)
						So(rsr.applied, ShouldEqual, len(want))
						So(recoverViews(jobs), ShouldResemble, want)
					})
			}
		})
	}
}

// recoverViews returns the view of each of jobs, by key.
func recoverViews(jobs []*Job) map[string]recoverView {
	views := make(map[string]recoverView, len(jobs))

	for _, job := range jobs {
		views[job.Key()] = recoverView{
			State: job.State, Attempts: job.Attempts, ReservedBy: job.ReservedBy, Cmd: job.Cmd,
			RepGroup: job.RepGroup, Priority: job.Priority, RAM: job.Requirements.RAM,
		}
	}

	return views
}

// comparableLogState returns a copy of st in which what is equal compares
// equal: a live key's empty dep group list is nil, and an overlay entry's
// EndTime has no monotonic clock reading.
func comparableLogState(st *logState) logState {
	c := *st
	c.live = make(map[logKey]*logLive, len(st.live))

	for k, e := range st.live {
		copied := *e
		if len(copied.index.depDeps) == 0 {
			copied.index.depDeps = nil
		}

		c.live[k] = &copied
	}

	c.overlay = make(map[logKey]*logHistory, len(st.overlay))

	for k, h := range st.overlay {
		copied := *h
		copied.endTime = time.Unix(0, h.endTime.UnixNano())
		c.overlay[k] = &copied
	}

	return c
}

// TestLogStoreRecoverDamage covers spec C5 tests 3 (without its backup, item
// 1f.10) and 5: damage within a wal file that is not the last, or a missing
// wal file, is corrupt.
func TestLogStoreRecoverDamage(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store with jobs in each of wal files 1, 2 and 3", t, func() {
		s, dir := openTestLogStore(t, ctx)

		for n := range 3 {
			if n > 0 {
				_, err := s.w.Rotate()
				So(err, ShouldBeNil)
			}

			_, err := s.storeNewJobsGuarded(ctx, testLogJobs(fmt.Sprintf("damage %d", n), "c5", 3), false, nil)
			So(err, ShouldBeNil)
		}

		So(s.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(dir)
		So(err, ShouldBeNil)
		So(files, ShouldResemble, []uint32{1, 2, 3})

		Convey("with a byte flipped in file 1, the store lock is released and open's error wraps "+
			"errStoreCorrupt", func() {
			path := walFilePath(dir, 1)

			data, errr := os.ReadFile(path)
			So(errr, ShouldBeNil)

			off := wal.FrameHeader + walHeaderLen + (len(data)-wal.FrameHeader-walHeaderLen)/2
			data[off] ^= 0x01
			So(os.WriteFile(path, data, 0o600), ShouldBeNil)

			cfg := storeConfig{dbFile: dir}

			_, erro := openLogStoreLocked(ctx, cfg)
			So(erro, ShouldWrap, errStoreCorrupt)

			lock, errl := lockStore(ctx, cfg.dbFile)
			So(errl, ShouldBeNil)
			So(lock.release(), ShouldBeNil)
		})

		Convey("with a whole frame appended to file 3 holding a record of unknown type, open's error wraps "+
			"errStoreCorrupt", func() {
			rec := make([]byte, logRecordHeader)
			rec[0] = 99

			f, erro := os.OpenFile(walFilePath(dir, 3), os.O_WRONLY|os.O_APPEND, 0)
			So(erro, ShouldBeNil)

			_, errw := f.Write(wal.AppendFrame(nil, rec))
			So(errw, ShouldBeNil)
			So(f.Close(), ShouldBeNil)

			_, erro = openLogStore(ctx, dir)
			So(erro, ShouldWrap, errStoreCorrupt)
		})

		Convey("with file 2 deleted, open's error wraps errStoreCorrupt", func() {
			So(os.Remove(walFilePath(dir, 2)), ShouldBeNil)

			_, erro := openLogStore(ctx, dir)
			So(erro, ShouldWrap, errStoreCorrupt)
		})
	})
}

// recoverView is what C5 test 1 compares of a live job: its key, State,
// Attempts, ReservedBy and spec fields.
type recoverView struct {
	State      JobState
	Attempts   uint32
	ReservedBy uuid.UUID
	Cmd        string
	RepGroup   string
	Priority   uint8
	RAM        int
}
