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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
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
	// logSyncHoldWait bounds waits on a held wal sync and on the writes it
	// holds up: far longer than any one sync or one small write takes.
	logSyncHoldWait = 10 * time.Second

	// logStorePlanWait is how long a test lets a write that must wait for a
	// held sync run before letting the sync go on: far longer than one small
	// write takes to append and return, so one that has not returned by then
	// is waiting.
	logStorePlanWait = 500 * time.Millisecond
)

// logSyncHold is a wal sync hook that holds the first wal file sync after arm
// until Release.
type logSyncHold struct {
	armed            atomic.Bool
	entered, release chan struct{}
	releaseOnce      sync.Once
}

// newLogSyncHold installs a logSyncHold, released and uninstalled when the
// Convey block ends. Make it before the store, so that the store's writer has
// it and the release comes before the store's close.
func newLogSyncHold() *logSyncHold {
	h := &logSyncHold{entered: make(chan struct{}), release: make(chan struct{})}

	Reset(h.Release)
	Reset(wal.SetTestSyncHook(h.hook))

	return h
}

func (h *logSyncHold) hook(path string) error {
	if strings.HasSuffix(path, walFileExt) && h.armed.CompareAndSwap(true, false) {
		close(h.entered)
		<-h.release
	}

	return nil
}

// arm makes the next wal file sync hold.
func (h *logSyncHold) arm() {
	h.armed.Store(true)
}

// Release lets the held sync go on.
func (h *logSyncHold) Release() {
	h.releaseOnce.Do(func() { close(h.release) })
}

// awaitEntered waits for the held sync to start.
func (h *logSyncHold) awaitEntered() {
	select {
	case <-h.entered:
	case <-time.After(logSyncHoldWait):
		So("the held sync never started", ShouldBeEmpty)
	}
}

func TestLogStoreConcurrentAdds(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a queued job whose reservation's state write is appended but held before it is durable", t,
		func() {
			hold := newLogSyncHold()
			s, _ := openTestLogStore(t, ctx)

			job := testDBJob("echo c2 race", "c2")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			runner := uuid.Must(uuid.NewV4())
			job.State = JobStateRunning
			job.ReservedBy = runner
			job.StartTime = time.Now()

			hold.arm()

			reserved := make(chan error, 1)
			go func() { reserved <- s.updateJobRunStateDurable(job) }()

			hold.awaitEntered()

			Convey("adding the job again meanwhile leaves it running with its ReservedBy, also after reopen", func() {
				parked := signalAddWaits()
				added := addAsync(ctx, s, testDBJob("echo c2 race", "c2"))
				awaitParkedOrWriting(s, parked)
				hold.Release()

				So(receiveWithin(reserved, logSyncHoldWait), ShouldBeNil)
				So(receiveStored(added).err, ShouldBeNil)

				for _, st := range []*logStore{s, reopenLogStore(ctx, s)} {
					got, live, errl := st.liveJob(job.Key())
					So(errl, ShouldBeNil)
					So(live, ShouldBeTrue)
					So(got.State, ShouldEqual, JobStateRunning)
					So(got.ReservedBy, ShouldEqual, runner)
				}
			})
		})

	Convey("Given a running job whose exit's write is appended but held before it is durable", t, func() {
		hold := newLogSyncHold()
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c2 exit race", "c2")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State = JobStateRunning
		job.ReservedBy = uuid.Must(uuid.NewV4())
		job.StartTime = time.Now()
		job.Attempts = 1
		So(s.updateJobRunStateDurable(job), ShouldBeNil)

		job.State = JobStateBuried
		job.Exitcode = 1
		job.EndTime = time.Now()

		hold.arm()

		exited := make(chan error, 1)
		go func() { exited <- s.updateJobAfterExitDurable(job, []byte("out"), []byte("err"), false) }()

		hold.awaitEntered()

		Convey("adding the job again meanwhile waits for the exit before deciding, then leaves the exit's state",
			func() {
				parked := signalAddWaits()
				added := addAsync(ctx, s, testDBJob("echo c2 exit race", "c2"))

				select {
				case <-parked:
				case r := <-added:
					So(r.err, ShouldBeNil)
					So("the add decided without waiting for the held exit", ShouldBeEmpty)
				case <-time.After(logSyncHoldWait):
					So("the add never waited for the held exit", ShouldBeEmpty)
				}

				hold.Release()

				So(receiveWithin(exited, logSyncHoldWait), ShouldBeNil)
				So(receiveStored(added).err, ShouldBeNil)

				for _, st := range []*logStore{s, reopenLogStore(ctx, s)} {
					got, live, errl := st.liveJob(job.Key())
					So(errl, ShouldBeNil)
					So(live, ShouldBeTrue)
					So(got.State, ShouldEqual, JobStateBuried)
					So(got.Exitcode, ShouldEqual, 1)
				}
			})
	})

	Convey("Given an add of a member of dep group g, putting back archived dependent d, held before durable", t,
		func() {
			hold := newLogSyncHold()
			s, _ := openTestLogStore(t, ctx)

			d := testDBJob("echo c2 dependent", "c2")
			d.Dependencies = Dependencies{NewDepGroupDependency("g")}
			_, err := s.storeNewJobsGuarded(ctx, []*Job{d}, false, nil)
			So(err, ShouldBeNil)

			d.State = JobStateComplete
			_, err = s.archiveCompletion(d.Key(), d)
			So(err, ShouldBeNil)

			member := func() *Job {
				m := testDBJob("echo c2 member", "c2")
				m.DepGroups = []string{"g"}

				return m
			}

			hold.arm()

			first := addAsync(ctx, s, member())

			hold.awaitEntered()

			Convey("adding the member again meanwhile waits for it, then finds d live rather than to put back", func() {
				parked := signalAddWaits()
				second := addAsync(ctx, s, member())
				awaitParkedOrWriting(s, parked)
				hold.Release()

				r := receiveStored(first)
				So(r.err, ShouldBeNil)
				So(jobKeys(r.stored.jobsToQueue), ShouldResemble, []string{d.Key(), member().Key()})

				r = receiveStored(second)
				So(r.err, ShouldBeNil)
				So(jobKeys(r.stored.jobsToQueue), ShouldResemble, []string{member().Key()})
				So(jobKeys(r.stored.dependents.jobs), ShouldResemble, []string{d.Key()})
			})
		})

	Convey("Given a job archived under rep group a, and the next wal sync held", t, func() {
		hold := newLogSyncHold()
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c2 lookup", "a")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State = JobStateComplete
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)

		hold.arm()

		Convey("a rep group lookup under b returns once durable, after which the job counts as b's", func() {
			type lookupResult struct {
				err, errc error
				elsewhere *completedElsewhere
			}

			done := make(chan lookupResult, 1)

			go func() {
				underB := testDBJob("echo c2 lookup", "b")
				errl := s.storeRepGroupLookups(ctx, []*Job{underB})

				_, elsewhere, errc := s.completeUnderRepGroup(underB, underB.Key())
				done <- lookupResult{errl, errc, elsewhere}
			}()

			hold.awaitEntered()
			hold.Release()

			select {
			case r := <-done:
				So(r.err, ShouldBeNil)
				So(r.errc, ShouldBeNil)
				So(r.elsewhere, ShouldBeNil)
			case <-time.After(logSyncHoldWait):
				So("the lookup never returned", ShouldBeEmpty)
			}
		})
	})
}

// signalAddWaits installs logStoreWaitHook until the Convey block ends,
// returning a channel signalled each time an add is about to wait.
func signalAddWaits() <-chan struct{} {
	parked := make(chan struct{}, 1)

	logStoreWaitHook = func() {
		select {
		case parked <- struct{}{}:
		default:
		}
	}

	Reset(func() { logStoreWaitHook = nil })

	return parked
}

// addAsync adds jobs to s on another goroutine, returning where its result
// arrives.
func addAsync(ctx context.Context, s *logStore, jobs ...*Job) <-chan storedResult {
	ch := make(chan storedResult, 1)

	go func() {
		stored, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		ch <- storedResult{stored, err}
	}()

	return ch
}

// awaitParkedOrWriting waits until an add is about to wait, as signalled on
// parked, or s has two writes appended and unfinished, the one a test holds and
// another, failing if neither happens within logSyncHoldWait.
func awaitParkedOrWriting(s *logStore, parked <-chan struct{}) {
	const n = 2

	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

	deadline := time.After(logSyncHoldWait)

	for s.writing.Load() < n {
		select {
		case <-parked:
			return
		case <-tick.C:
		case <-deadline:
			So("the add neither waited nor appended", ShouldBeEmpty)

			return
		}
	}
}

// receiveStored returns the add result that arrives on ch, failing if none
// does within logSyncHoldWait.
func receiveStored(ch <-chan storedResult) storedResult {
	select {
	case r := <-ch:
		return r
	case <-time.After(logSyncHoldWait):
		So("the add never returned", ShouldBeEmpty)

		return storedResult{}
	}
}

func TestLogStoreAddRaces(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a queued job, and a re-add of it that has decided to store it afresh but not appended", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c2 decided", "c2")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		runner := uuid.Must(uuid.NewV4())

		var reserveErr error

		dependentsReadHook = func() {
			dependentsReadHook = nil

			job.State = JobStateRunning
			job.ReservedBy = runner
			job.StartTime = time.Now()
			reserveErr = s.updateJobRunStateDurable(job)
		}

		Reset(func() { dependentsReadHook = nil })

		Convey("a reservation durable before the re-add appends outranks it, also after reopen", func() {
			_, err = s.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo c2 decided", "c2")}, false, nil)
			So(err, ShouldBeNil)
			So(reserveErr, ShouldBeNil)

			for _, st := range []*logStore{s, reopenLogStore(ctx, s)} {
				got, live, errl := st.liveJob(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)
				So(got.State, ShouldEqual, JobStateRunning)
				So(got.ReservedBy, ShouldEqual, runner)
			}
		})
	})

	Convey("Given archived dependent d of dep group g, and an add of a member of g that has read d archived", t,
		func() {
			s, _ := openTestLogStore(t, ctx)

			dependent := func() *Job {
				d := testDBJob("echo c2 put back race", "c2")
				d.Dependencies = Dependencies{NewDepGroupDependency("g")}

				return d
			}

			d := dependent()
			_, err := s.storeNewJobsGuarded(ctx, []*Job{d}, false, nil)
			So(err, ShouldBeNil)

			d.State = JobStateComplete
			_, err = s.archiveCompletion(d.Key(), d)
			So(err, ShouldBeNil)

			m := testDBJob("echo c2 put back member", "c2")
			m.DepGroups = []string{"g"}

			runner := uuid.Must(uuid.NewV4())

			var hookErr error

			logPutBackHook = func() {
				logPutBackHook = nil

				readded := dependent()
				if _, hookErr = s.storeNewJobsGuarded(ctx, []*Job{readded}, false, nil); hookErr != nil {
					return
				}

				readded.State = JobStateRunning
				readded.ReservedBy = runner
				readded.StartTime = time.Now()
				hookErr = s.updateJobRunStateDurable(readded)
			}

			Reset(func() { logPutBackHook = nil })

			Convey("d re-added and reserved meanwhile stays running, a live dependent of the add, also after reopen",
				func() {
					stored, erra := s.storeNewJobsGuarded(ctx, []*Job{m}, false, nil)
					So(erra, ShouldBeNil)
					So(hookErr, ShouldBeNil)
					So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{m.Key()})
					So(jobKeys(stored.dependents.jobs), ShouldResemble, []string{d.Key()})

					for _, st := range []*logStore{s, reopenLogStore(ctx, s)} {
						got, live, errl := st.liveJob(d.Key())
						So(errl, ShouldBeNil)
						So(live, ShouldBeTrue)
						So(got.State, ShouldEqual, JobStateRunning)
						So(got.ReservedBy, ShouldEqual, runner)
					}
				})
		})

	Convey("Given archived dependent d of dep group g, and an add of a member of g that has read d archived, "+
		"when d is re-added (not reserved) before the put-back decides", t, func() {
		s, _ := openTestLogStore(t, ctx)

		dependent := func(priority uint8) *Job {
			d := testDBJob("echo c2 put back fresh", "c2")
			d.Dependencies = Dependencies{NewDepGroupDependency("g")}
			d.Priority = priority

			return d
		}

		d := dependent(1)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{d}, false, nil)
		So(err, ShouldBeNil)

		d.State = JobStateComplete
		_, err = s.archiveCompletion(d.Key(), d)
		So(err, ShouldBeNil)

		var hookErr error

		logPutBackHook = func() {
			logPutBackHook = nil
			_, hookErr = s.storeNewJobsGuarded(ctx, []*Job{dependent(9)}, false, nil)
		}

		Reset(func() { logPutBackHook = nil })

		Convey("d is not put back over the re-add, but is a live dependent of the add, keeping the re-add's spec",
			func() {
				m := testDBJob("echo c2 put back fresh member", "c2")
				m.DepGroups = []string{"g"}

				stored, erra := s.storeNewJobsGuarded(ctx, []*Job{m}, false, nil)
				So(erra, ShouldBeNil)
				So(hookErr, ShouldBeNil)
				So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{m.Key()})
				So(jobKeys(stored.dependents.jobs), ShouldResemble, []string{d.Key()})

				for _, st := range []*logStore{s, reopenLogStore(ctx, s)} {
					got, live, errl := st.liveJob(d.Key())
					So(errl, ShouldBeNil)
					So(live, ShouldBeTrue)
					So(got.Priority, ShouldEqual, 9)
				}
			})
	})

	Convey("Given the next wal sync held", t, func() {
		hold := newLogSyncHold()
		s, _ := openTestLogStore(t, ctx)

		hold.arm()

		Convey("storeEnv returns only once its env is durable, and it is then retrievable", func() {
			type envResult struct {
				blob []byte
				err  error
			}

			done := make(chan envResult, 1)

			go func() {
				key, err := s.storeEnv([]byte("held env"))
				done <- envResult{s.retrieveEnv(ctx, key), err}
			}()

			hold.awaitEntered()

			select {
			case <-done:
				So("storeEnv returned while its sync was held", ShouldBeEmpty)
			case <-time.After(logStorePlanWait):
			}

			hold.Release()

			select {
			case r := <-done:
				So(r.err, ShouldBeNil)
				So(string(r.blob), ShouldEqual, "held env")
			case <-time.After(logSyncHoldWait):
				So("storeEnv never returned", ShouldBeEmpty)
			}
		})
	})
}

// storedResult is what an add returned.
type storedResult struct {
	stored storedNewJobs
	err    error
}

// walFrame is one outer frame of a wal file, after its header.
type walFrame struct {
	off     uint64
	isBlock bool
	codec   byte
	recs    []logRecordType
}

// walFrames returns the outer frames of every wal file in dir, in log order.
func walFrames(dir string) []walFrame {
	files, err := walFileNumbers(dir)
	So(err, ShouldBeNil)

	var frames []walFrame

	for _, n := range files {
		data, errr := os.ReadFile(walFilePath(dir, n))
		So(errr, ShouldBeNil)

		header := true

		_, errs := wal.Scan(bytes.NewReader(data), func(ref wal.Ref, rec []byte) error {
			if header {
				header = false

				return nil
			}

			if len(frames) == 0 || frames[len(frames)-1].off != ref.Off || ref.Idx == 0 {
				lenFlags := binary.LittleEndian.Uint32(data[ref.Off:])
				frames = append(frames, walFrame{off: ref.Off, isBlock: lenFlags&(1<<31) != 0})

				if frames[len(frames)-1].isBlock {
					frames[len(frames)-1].codec = data[ref.Off+wal.FrameHeader]
				}
			}

			last := &frames[len(frames)-1]
			last.recs = append(last.recs, logRecordType(rec[0]))

			return nil
		})
		So(errs, ShouldBeNil)
	}

	return frames
}

// specsPerFrame returns how many spec records each of frames holding any has.
func specsPerFrame(frames []walFrame) []int {
	var specs []int

	for _, frame := range frames {
		if n := countRecords(frame.recs, logRecSpec); n > 0 {
			specs = append(specs, n)
		}
	}

	return specs
}

// countRecords returns how many of recs are of typ.
func countRecords(recs []logRecordType, typ logRecordType) int {
	n := 0

	for _, r := range recs {
		if r == typ {
			n++
		}
	}

	return n
}

func TestLogStoreAdd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store", t, func() {
		s, dir := openTestLogStore(t, ctx)

		Convey("1,000 jobs added in one call are one s2 block holding 1,000 spec records, durable on return", func() {
			before := walFrames(dir)

			stored, err := s.storeNewJobsGuarded(ctx, testLogJobs("block", "c2", 1000), false, nil)
			So(err, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 1000)

			frames := walFrames(dir)
			So(frames, ShouldHaveLength, len(before)+1)

			added := frames[len(frames)-1]
			So(added.isBlock, ShouldBeTrue)
			So(added.codec, ShouldEqual, wal.CodecS2)
			So(countRecords(added.recs, logRecSpec), ShouldEqual, 1000)
			So(countRecords(added.recs, logRecState), ShouldEqual, 1000)
			So(inspectedCount(s, kindLive), ShouldEqual, 1000)

			Convey("and after reopen every job is live as it was added", func() {
				reopened := reopenLogStore(ctx, s)
				So(inspectedCount(reopened, kindLive), ShouldEqual, 1000)

				jobs, _, errr := reopened.recoverIncompleteJobs()
				So(errr, ShouldBeNil)
				So(jobs, ShouldHaveLength, 1000)

				job, live, errl := reopened.liveJob(testLogJobs("block", "c2", 1)[0].Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)
				So(job.Cmd, ShouldEqual, "echo block 0")
				So(job.RepGroup, ShouldEqual, "c2")
			})
		})

		Convey("an add whose records exceed the block limit is split into s2 blocks in one batch", func() {
			old := logBlockMaxFrames
			logBlockMaxFrames = 4096

			Reset(func() { logBlockMaxFrames = old })

			before := walFrames(dir)
			batches := s.w.Stats().Batches

			jobs := testLogJobs("split", "c2", 100)
			stored, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 100)
			So(s.w.Stats().Batches, ShouldEqual, batches+1)

			added := walFrames(dir)[len(before):]
			So(len(added), ShouldBeGreaterThan, 1)

			specs := 0

			for _, frame := range added {
				So(frame.isBlock, ShouldBeTrue)
				So(frame.codec, ShouldEqual, wal.CodecS2)

				specs += countRecords(frame.recs, logRecSpec)
			}

			So(specs, ShouldEqual, 100)

			reopened := reopenLogStore(ctx, s)
			So(inspectedCount(reopened, kindLive), ShouldEqual, 100)

			job, live, errl := reopened.liveJob(jobs[99].Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)
			So(job.Cmd, ShouldEqual, jobs[99].Cmd)
		})

		Convey("a job added again while running keeps its running state and ReservedBy, also after reopen", func() {
			job := testDBJob("echo c2 running", "c2")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			runner := uuid.Must(uuid.NewV4())
			job.State = JobStateRunning
			job.ReservedBy = runner
			job.StartTime = time.Now()
			So(s.updateJobRunStateDurable(job), ShouldBeNil)

			again := testDBJob("echo c2 running", "c2")
			stored, err := s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
			So(err, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 1)

			checkRunning := func(store *logStore) {
				got, live, errl := store.liveJob(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)
				So(got.State, ShouldEqual, JobStateRunning)
				So(got.ReservedBy, ShouldEqual, runner)
			}

			checkRunning(s)

			reopened := reopenLogStore(ctx, s)
			checkRunning(reopened)

			Convey("and added again after that reopen, it still keeps them", func() {
				_, err = reopened.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo c2 running", "c2")}, false, nil)
				So(err, ShouldBeNil)

				checkRunning(reopened)
				checkRunning(reopenLogStore(ctx, reopened))
			})
		})

		Convey("a job added again while queued and never handed out is stored afresh", func() {
			job := testDBJob("echo c2 queued", "c2")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			again := testDBJob("echo c2 queued", "c2")
			again.Priority = 7
			_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
			So(err, ShouldBeNil)

			got, live, err := reopenLogStore(ctx, s).liveJob(job.Key())
			So(err, ShouldBeNil)
			So(live, ShouldBeTrue)
			So(got.Priority, ShouldEqual, 7)
		})

		Convey("an env stored twice is stored once, also after reopen", func() {
			env := []byte("PATH=/bin")

			key, err := s.storeEnv(env)
			So(err, ShouldBeNil)

			before := walFrames(dir)

			again, err := s.storeEnv(env)
			So(err, ShouldBeNil)
			So(again, ShouldEqual, key)
			So(walFrames(dir), ShouldHaveLength, len(before))

			reopened := reopenLogStore(ctx, s)
			So(inspectedCount(reopened, kindEnv), ShouldEqual, 1)
			So(string(reopened.retrieveEnv(ctx, key)), ShouldEqual, string(env))
			So(reopened.retrieveEnv(ctx, "nokey"), ShouldBeNil)
		})
	})

	Convey("Given limit group g set to 5 then removed", t, func() {
		steps := func(st interface {
			storeLimitGroups(groups map[string]*limiter.GroupData) (changed, removed []string, err error)
		}) {
			changed, removed, err := st.storeLimitGroups(map[string]*limiter.GroupData{"g": limiter.NewCountGroupData(5)})
			So(err, ShouldBeNil)
			So(changed, ShouldResemble, []string{"g"})
			So(removed, ShouldBeEmpty)

			changed, removed, err = st.storeLimitGroups(map[string]*limiter.GroupData{"g": limiter.NewCountGroupData(-1)})
			So(err, ShouldBeNil)
			So(changed, ShouldBeEmpty)
			So(removed, ShouldResemble, []string{"g"})
		}

		boltDir := t.TempDir()
		boltStore := openStoreIn(ctx, storeKindBolt, boltDir)
		steps(boltStore)
		So(boltStore.close(ctx), ShouldBeNil)

		boltStore = openStoreIn(ctx, storeKindBolt, boltDir)
		boltLimits, err := boltStore.retrieveStoredLimits([]string{"g"})
		So(err, ShouldBeNil)

		boltGroup := boltStore.retrieveLimitGroup(ctx, "g")
		So(boltStore.close(ctx), ShouldBeNil)

		s, _ := openTestLogStore(t, ctx)
		steps(s)

		Convey("after reopen the log store answers as bbolt does for a never-set group", func() {
			reopened := reopenLogStore(ctx, s)

			limits, errr := reopened.retrieveStoredLimits([]string{"g"})
			So(errr, ShouldBeNil)
			So(limits, ShouldResemble, boltLimits)
			So(limits, ShouldResemble, []int64{-1})
			So(reopened.retrieveLimitGroup(ctx, "g"), ShouldResemble, boltGroup)
			So(inspectedCount(reopened, kindLimit), ShouldEqual, 0)
		})

		Convey("a limit set again after removal is stored, and unchanged when set the same", func() {
			changed, _, errs := s.storeLimitGroups(map[string]*limiter.GroupData{"g": limiter.NewCountGroupData(3)})
			So(errs, ShouldBeNil)
			So(changed, ShouldResemble, []string{"g"})

			changed, _, errs = s.storeLimitGroups(map[string]*limiter.GroupData{"g": limiter.NewCountGroupData(3)})
			So(errs, ShouldBeNil)
			So(changed, ShouldBeEmpty)

			limits, errr := reopenLogStore(ctx, s).retrieveStoredLimits([]string{"g", "h"})
			So(errr, ShouldBeNil)
			So(limits, ShouldResemble, []int64{3, -1})
		})
	})
}

func TestLogStoreSplitAdd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given an add of 5 jobs split into one block per job", t, func() {
		old := logBlockMaxFrames
		logBlockMaxFrames = 1

		Reset(func() { logBlockMaxFrames = old })

		s, dir := openTestLogStore(t, ctx)
		before := len(walFrames(dir))

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("torn", "c2", 5), false, nil)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		added := walFrames(dir)[before:]
		So(len(added), ShouldBeGreaterThan, 1)

		files, err := walFileNumbers(dir)
		So(err, ShouldBeNil)
		So(files, ShouldHaveLength, 1)

		Convey("a crash before any of its blocks is durable recovers every job whose spec is, whole", func() {
			for i, frame := range added {
				copyDir := filepath.Join(t.TempDir(), "torn")
				So(copyCrashStore(dir, copyDir), ShouldBeNil)
				So(os.Truncate(walFilePath(copyDir, files[0]), int64(frame.off)), ShouldBeNil) //nolint:gosec // an offset

				specs := 0
				for _, kept := range added[:i] {
					specs += countRecords(kept.recs, logRecSpec)
				}

				reopened, erro := openLogStore(ctx, copyDir)
				So(erro, ShouldBeNil)

				jobs, rsr, errr := reopened.recoverIncompleteJobs()
				So(errr, ShouldBeNil)
				So(jobs, ShouldHaveLength, specs)
				So(rsr.applied, ShouldEqual, specs)
				So(reopened.close(ctx), ShouldBeNil)
			}
		})
	})

	Convey("Given an add of 5 jobs split into one block per job, before any reopen", t, func() {
		old := logBlockMaxFrames
		logBlockMaxFrames = 1

		Reset(func() { logBlockMaxFrames = old })

		s, _ := openTestLogStore(t, ctx)
		jobs := testLogJobs("split live", "c2", 5)

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		Convey("each job reads back through its live records", func() {
			read := 0

			for _, job := range jobs {
				got, live, errl := s.liveJob(job.Key())
				if errl == nil && live && got.Cmd == job.Cmd {
					read++
				}
			}

			So(read, ShouldEqual, len(jobs))
		})
	})

	Convey("Given an add's records, with dependents to put back", t, func() {
		s, _ := openTestLogStore(t, ctx)

		archived := testLogJobs("put back", "c2", 20)
		for _, job := range archived {
			job.Dependencies = Dependencies{NewDepGroupDependency("g")}
		}

		_, err := s.storeNewJobsGuarded(ctx, archived, false, nil)
		So(err, ShouldBeNil)

		for _, job := range archived {
			job.State = JobStateComplete
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)
		}

		members := testLogJobs("member", "c2", 20)
		for _, job := range members {
			job.DepGroups = []string{"g", "h"}
		}

		keys, err := jobLogKeys(members)
		So(err, ShouldBeNil)

		add, claim, err := s.planClaimedAdd(members, keys, false)
		So(err, ShouldBeNil)
		claim.release()
		So(add.jobsToQueue, ShouldHaveLength, 40)

		f := add.frames

		Convey("no block size limit splits a job's records between blocks", func() {
			torn := 0

			for limit := 1; limit <= len(f.frames)+1; limit++ {
				for _, chunk := range f.chunks(limit)[1:] {
					if k := f.ops[chunk[0]].key; k != (logKey{}) && f.ops[chunk[0]-1].key == k {
						torn++
					}
				}
			}

			So(torn, ShouldEqual, 0)
		})
	})
}

func TestLogStoreSplitPutBacks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given 3 archived dependents of dep group g, and a 1 byte block limit", t, func() {
		s, dir := openTestLogStore(t, ctx)

		dependents := testLogJobs("split put back", "c2", 3)
		for _, d := range dependents {
			d.Dependencies = Dependencies{NewDepGroupDependency("g")}
		}

		_, err := s.storeNewJobsGuarded(ctx, dependents, false, nil)
		So(err, ShouldBeNil)

		for _, d := range dependents {
			d.State = JobStateComplete
			_, err = s.archiveCompletion(d.Key(), d)
			So(err, ShouldBeNil)
		}

		old := logBlockMaxFrames
		logBlockMaxFrames = 1

		Reset(func() { logBlockMaxFrames = old })

		before := len(walFrames(dir))

		Convey("an add of a member of g puts each back in a block of its own", func() {
			m := testDBJob("echo split put back member", "c2")
			m.DepGroups = []string{"g"}

			stored, errs := s.storeNewJobsGuarded(ctx, []*Job{m}, false, nil)
			So(errs, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 4)

			So(specsPerFrame(walFrames(dir)[before:]), ShouldResemble, []int{1, 1, 1, 1})
		})

		Convey("putting them back with storeLiveForRerun puts each back in a block of its own", func() {
			So(s.storeLiveForRerun(dependents), ShouldBeNil)

			So(specsPerFrame(walFrames(dir)[before:]), ShouldResemble, []int{1, 1, 1})
		})
	})
}

// testLogJobs returns n distinct jobs in repGroup.
func testLogJobs(prefix, repGroup string, n int) []*Job {
	jobs := make([]*Job, n)

	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo %s %d", prefix, i), repGroup)
	}

	return jobs
}
