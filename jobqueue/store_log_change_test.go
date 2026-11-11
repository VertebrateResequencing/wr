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
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// logChangeTestJobs is how many jobs TestLogStoreJobChangeAppendedLater
	// changes.
	logChangeTestJobs = 100

	// closeWaitTestPoll is how long TestLogStoreCloseWaitsForQueuedChanges
	// watches for close to refuse writes while the drain is held.
	closeWaitTestPoll = time.Second

	// changeAppendHoldMax bounds how long a test holds the append of one queued
	// change.
	changeAppendHoldMax = 2 * time.Second

	// changeFlushWait bounds how long a test waits for queued changes to be
	// appended or dropped.
	changeFlushWait = 3 * time.Second

	// changeAppendHoldFor is how long TestLogStoreWriteWaitsForBatchBeingAppended
	// holds the append of its second queued change before letting it go on.
	changeAppendHoldFor = 500 * time.Millisecond
)

// buryChange is a job change that buries the job.
func buryChange(job *Job) { job.State = JobStateBuried }

// TestLogStoreJobChangeAppendedLater proves that queueJobChangeAhead, which the
// server calls with its queue locked, takes each change's seq there but leaves
// encoding and appending its state record to the store's change queue, so a
// reserve waiting on the queue does not wait for that, and that the records
// are durable all the same.
func TestLogStoreJobChangeAppendedLater(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store holding live jobs, whose change queue's drain is held", t, func() {
		s, _, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, logChangeTestJobs)

		Convey("each change returns with its seq taken and its record queued, not appended; a reservation "+
			"made while they are queued appends them first and takes a later seq, so it is the one kept; "+
			"all are durable across a reopen", func() {
			seqBefore := s.seq.Load()

			for _, job := range jobs {
				s.queueJobChangeAhead(ctx, job, nil, buryChange)
			}

			So(s.seq.Load()-seqBefore, ShouldEqual, logChangeTestJobs)
			So(queuedChanges(s), ShouldEqual, logChangeTestJobs)

			reserved := jobs[0]
			reserved.Lock()
			reserved.State = JobStateReserved
			reserved.Unlock()

			commit, err := s.reserveRunState(reserved)
			So(err, ShouldBeNil)

			done, err := commit.WaitWithin(reserveTestWait)
			So(err, ShouldBeNil)
			So(done, ShouldBeTrue)

			release()
			So(s.flush(), ShouldBeNil)
			So(queuedChanges(s), ShouldEqual, 0)

			eachReopen(ctx, &s, func(st *logStore) {
				soLiveState(st, reserved.Key(), JobStateReserved)

				buried := 0

				for _, job := range jobs[1:] {
					got, live, errl := st.liveJob(job.Key())
					if errl == nil && live && got.State == JobStateBuried {
						buried++
					}
				}

				So(buried, ShouldEqual, logChangeTestJobs-1)
			})
		})
	})
}

// TestLogStoreJobChangeAppendPanic proves that a panic appending a queued job
// change loses only that change: the drain goes on to the others, flush and
// close return, and the change's key is no longer noted as being written, so a
// later add of it does not wait for it.
func TestLogStoreJobChangeAppendPanic(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store holding live jobs, whose first queued change panics as it is appended", t, func() {
		s, _, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 3)

		var panicked atomic.Bool

		setChangeAppendHook(s, func() {
			if panicked.CompareAndSwap(false, true) {
				panic("test panic appending a job change")
			}
		})

		for _, job := range jobs {
			s.queueJobChangeAhead(ctx, job, nil, buryChange)
		}

		release()

		Convey("flush returns, the other changes are durable, no key is left noted as written, a later add "+
			"of the lost change's job and close return", func() {
			flushed := make(chan error, 1)

			go func() { flushed <- s.flush() }()

			So(receiveWithin(flushed, reserveTestWait), ShouldBeNil)
			So(panicked.Load(), ShouldBeTrue)
			So(notedStateWrites(s), ShouldEqual, 0)

			for _, job := range jobs[1:] {
				soLiveState(s, job.Key(), JobStateBuried)
			}

			added := make(chan error, 1)

			go func() {
				_, erra := s.storeNewJobsGuarded(ctx, []*Job{testDBJob(jobs[0].Cmd, jobs[0].RepGroup)}, false, nil)
				added <- erra
			}()

			So(receiveWithin(added, reserveTestWait), ShouldBeNil)

			closed := make(chan error, 1)

			go func() { closed <- s.close(ctx) }()

			So(receiveWithin(closed, reserveTestWait), ShouldBeNil)
		})
	})
}

// TestLogStoreJobChangeWritePanic proves that a panic once a queued change's
// write has counted itself in writing, and before it finishes, gives that count
// back, so flush and close still return.
func TestLogStoreJobChangeWritePanic(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store whose queued change's write panics once it has appended", t, func() {
		s, _, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 1)

		var (
			armed    atomic.Bool
			panicked atomic.Bool
		)

		setChangeAppendHook(s, func() { armed.Store(true) })

		logAppendedHook = func() {
			if armed.CompareAndSwap(true, false) {
				panicked.Store(true)
				panic("test panic in a job change's write")
			}
		}

		Reset(func() { logAppendedHook = nil })

		s.queueJobChangeAhead(ctx, jobs[0], nil, buryChange)
		release()

		Convey("flush and close return, and no key is left noted as written", func() {
			flushed := make(chan error, 1)

			go func() { flushed <- s.flush() }()

			So(receiveWithin(flushed, reserveTestWait), ShouldBeNil)
			So(panicked.Load(), ShouldBeTrue)
			So(notedStateWrites(s), ShouldEqual, 0)

			closed := make(chan error, 1)

			go func() { closed <- s.close(ctx) }()

			So(receiveWithin(closed, reserveTestWait), ShouldBeNil)
		})
	})
}

// TestLogStoreWriteAfterQueuedChange proves that a write made after a job
// change was queued appends the change first, so when the write is durable
// the change is too, even if the change queue's drain has not run.
func TestLogStoreWriteAfterQueuedChange(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store holding live jobs, whose change queue's drain is held", t, func() {
		s, _, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 2)

		Convey("a durable write of one job made after a change of the other was queued leaves that change "+
			"durable when it returns", func() {
			changed, written := jobs[0], jobs[1]

			s.queueJobChangeAhead(ctx, changed, nil, buryChange)
			So(queuedChanges(s), ShouldEqual, 1)

			So(s.updateJobAfterChangeDurable(written), ShouldBeNil)
			So(queuedChanges(s), ShouldEqual, 0)
			soLiveState(s, changed.Key(), JobStateBuried)

			release()
		})
	})
}

// TestLogStoreWriteWaitsForBatchBeingAppended proves that a write made while
// the drain is part way through appending a batch of queued changes waits for
// the rest of the batch, so it is durable no earlier than any change queued
// before it.
func TestLogStoreWriteWaitsForBatchBeingAppended(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store whose drain has taken two queued changes and is appending the second", t, func() {
		s, _, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 3)

		var calls atomic.Int32

		letGo := make(chan struct{})

		setChangeAppendHook(s, func() {
			if calls.Add(1) == 2 {
				select {
				case <-letGo:
				case <-time.After(changeAppendHoldMax):
				}
			}
		})

		s.queueJobChangeAhead(ctx, jobs[0], nil, buryChange)
		s.queueJobChangeAhead(ctx, jobs[1], nil, buryChange)
		release()

		So(pollUntilFor(reserveTestWait, func() bool { return calls.Load() == 2 }), ShouldBeTrue)

		Convey("a durable write of a third job leaves the second change durable when it returns", func() {
			time.AfterFunc(changeAppendHoldFor, func() { close(letGo) })

			So(s.updateJobAfterChangeDurable(jobs[2]), ShouldBeNil)
			soLiveState(s, jobs[1].Key(), JobStateBuried)
		})
	})
}

// TestLogStoreCloseWaitsForQueuedChanges proves that close waits for the job
// changes queued before it to be appended before it refuses writes, so they
// are durable after a reopen.
func TestLogStoreCloseWaitsForQueuedChanges(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store with a job change queued, whose change queue's drain is held", t, func() {
		s, dir, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 1)

		s.queueJobChangeAhead(ctx, jobs[0], nil, buryChange)

		Convey("close does not refuse writes until the drain is released, and the change survives a reopen", func() {
			closed := make(chan error, 1)

			go func() { closed <- s.close(ctx) }()

			refusing := pollUntilFor(closeWaitTestPoll, func() bool {
				s.closeMu.RLock()
				defer s.closeMu.RUnlock()

				return s.closed
			})
			So(refusing, ShouldBeFalse)

			release()
			So(receiveWithin(closed, reserveTestWait), ShouldBeNil)

			reopened := openLogStoreIn(ctx, dir)

			defer func() { So(reopened.close(ctx), ShouldBeNil) }()

			soLiveState(reopened, jobs[0].Key(), JobStateBuried)
		})
	})
}

// TestLogStoreDrainHoldsCloseMu proves that the drain appends each queued
// change holding closeMu's read lock, as every append must (spec "Write
// path"), so close cannot refuse writes while one is being appended.
func TestLogStoreDrainHoldsCloseMu(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store with job changes queued, whose change queue's drain is held", t, func() {
		s, _, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 2)

		var appended, unlocked atomic.Int32

		setChangeAppendHook(s, func() {
			appended.Add(1)

			if s.closeMu.TryLock() {
				unlocked.Add(1)
				s.closeMu.Unlock()
			}
		})

		for _, job := range jobs {
			s.queueJobChangeAhead(ctx, job, nil, buryChange)
		}

		Convey("once released, the drain appends each holding closeMu", func() {
			release()
			So(s.flush(), ShouldBeNil)
			So(appended.Load(), ShouldEqual, len(jobs))
			So(unlocked.Load(), ShouldEqual, 0)
		})
	})
}

// TestLogStoreRevivingJobChange proves that a change of a job whose key a
// write in flight is making live is appended once that write has finished,
// not dropped.
func TestLogStoreRevivingJobChange(t *testing.T) {
	ctx := context.Background()

	Convey("Given an archived job being put back live by a write held once it has appended", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)

		x := testDBJob("echo change reviving", "rg-change-reviving")
		archiveNew(ctx, s, x)

		again := testDBJob(x.Cmd, x.RepGroup)
		putBack := make(chan error, 1)

		hold.arm()

		go func() { putBack <- s.storeLiveForRerun([]*Job{again}) }()

		hold.awaitEntered()

		k, err := parseLogKey(x.Key())
		So(err, ShouldBeNil)

		s.stateMu.Lock()
		_, reviving := s.reviving[k]
		s.stateMu.Unlock()

		So(reviving, ShouldBeTrue)

		Convey("a change of it, made meanwhile, is durable once the put-back has finished", func() {
			s.queueJobChangeAhead(ctx, again, nil, buryChange)

			hold.Release()
			So(receiveWithin(putBack, logSyncHoldWait), ShouldBeNil)

			So(pollUntilFor(reserveTestWait, func() bool {
				got, live, errl := s.liveJob(x.Key())

				return errl == nil && live && got.State == JobStateBuried
			}), ShouldBeTrue)

			eachReopen(ctx, &s, func(st *logStore) { soLiveState(st, x.Key(), JobStateBuried) })
		})
	})
}

// TestLogStoreJobChangeFinishPanic proves that a queued change whose append
// panics, and whose finish then panics too, does not take the process down
// with it: the drain still ends and flush returns.
func TestLogStoreJobChangeFinishPanic(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store with a queued change whose append and finish both panic", t, func() {
		s, _, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 1)

		k, err := parseLogKey(jobs[0].Key())
		So(err, ShouldBeNil)

		setChangeAppendHook(s, func() { panic("test panic appending a job change") })

		var finishes atomic.Int32

		s.changes.push(s, logQueuedChange{k: k, seq: s.nextSeq(), finished: func() {
			finishes.Add(1)
			panic("test panic finishing a job change")
		}})

		Convey("once released, the drain ends, having tried to finish it, and flush returns", func() {
			release()

			flushed := make(chan error, 1)

			go func() { flushed <- s.flush() }()

			So(receiveWithin(flushed, reserveTestWait), ShouldBeNil)
			So(finishes.Load(), ShouldEqual, 1)
		})
	})
}

// openDrainHeldLogStore opens a log store in a new directory, as
// openTestLogStore does, whose change queue's drain waits, before it takes any
// record, until the returned func is called, or for at most reserveTestWait.
// The Convey block's end calls that func before it closes the store, so close
// never waits for a held drain. Drains of other stores are not held.
func openDrainHeldLogStore(t *testing.T, ctx context.Context) (*logStore, string, func()) {
	t.Helper()

	var (
		held     atomic.Pointer[logStore]
		released atomic.Bool
	)

	release := make(chan struct{})
	releaseOnce := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}

	hook := func(st *logStore) {
		if st == held.Load() {
			select {
			case <-release:
			case <-time.After(reserveTestWait):
			}
		}
	}
	logChangeDrainHook.Store(&hook)

	// registered before openTestLogStore registers the store's close, so it
	// runs first.
	Reset(func() {
		releaseOnce()
		logChangeDrainHook.Store(nil)
	})

	s, dir := openTestLogStore(t, ctx)
	held.Store(s)

	return s, dir, releaseOnce
}

// storeChangeTestJobs stores n live ready jobs in s and returns them.
func storeChangeTestJobs(ctx context.Context, s *logStore, n int) []*Job {
	jobs := make([]*Job, n)
	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo change %d", i), "rg-change")
		jobs[i].State = JobStateReady
	}

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)
	So(s.flush(), ShouldBeNil)

	return jobs
}

// setChangeAppendHook sets logChangeAppendHook to call fn with each record of
// s's change queue as it is appended, until the Convey block's end.
func setChangeAppendHook(s *logStore, fn func()) {
	hook := func(st *logStore) {
		if st == s {
			fn()
		}
	}
	logChangeAppendHook.Store(&hook)

	Reset(func() { logChangeAppendHook.Store(nil) })
}

// queuedChanges returns how many records s's change queue holds.
func queuedChanges(s *logStore) int {
	s.changes.mu.Lock()
	defer s.changes.mu.Unlock()

	return len(s.changes.pending)
}

// notedStateWrites returns how many keys s notes as having state records being
// written.
func notedStateWrites(s *logStore) int {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	return len(s.stateWrites)
}

// soLiveState asserts that the keyed job is live in st with the given state.
func soLiveState(st *logStore, key string, want JobState) {
	got, live, err := st.liveJob(key)
	So(err, ShouldBeNil)
	So(live, ShouldBeTrue)
	So(got.State, ShouldEqual, want)
}

// TestLogStoreChangeQueuedDuringBatch proves that a job change queued while
// the drain is appending an earlier batch is appended by that drain too, not
// left queued with no drain running.
func TestLogStoreChangeQueuedDuringBatch(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store whose drain is appending a first queued change", t, func() {
		s, dir, release := openDrainHeldLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 2)

		entered, letGo := make(chan struct{}), make(chan struct{})

		var once atomic.Bool

		setChangeAppendHook(s, func() {
			if once.CompareAndSwap(false, true) {
				close(entered)

				select {
				case <-letGo:
				case <-time.After(changeAppendHoldMax):
				}
			}
		})

		s.queueJobChangeAhead(ctx, jobs[0], nil, buryChange)
		release()

		select {
		case <-entered:
		case <-time.After(reserveTestWait):
			So("the drain never appended the first change", ShouldBeEmpty)
		}

		Convey("a second change queued meanwhile is appended: flush returns, and it survives close and "+
			"a reopen", func() {
			s.queueJobChangeAhead(ctx, jobs[1], nil, buryChange)
			close(letGo)

			flushed := make(chan error, 1)

			go func() { flushed <- s.flush() }()

			So(receiveWithin(flushed, changeFlushWait), ShouldBeNil)
			So(s.close(ctx), ShouldBeNil)

			reopened := openLogStoreIn(ctx, dir)

			defer func() { So(reopened.close(ctx), ShouldBeNil) }()

			soLiveState(reopened, jobs[1].Key(), JobStateBuried)
		})
	})
}

// TestLogStoreChangeAfterClose proves that a job change queued after close is
// dropped as an append after close is: its key is no longer noted as being
// written, so a later add of it is not left waiting, and it gives back its
// count in writing.
func TestLogStoreChangeAfterClose(t *testing.T) {
	ctx := context.Background()

	Convey("Given a closed log store", t, func() {
		s, _ := openTestLogStore(t, ctx)
		jobs := storeChangeTestJobs(ctx, s, 1)
		So(s.close(ctx), ShouldBeNil)

		Convey("a change queued now is dropped, leaving no key noted as written and nothing counted as "+
			"writing", func() {
			s.queueJobChangeAhead(ctx, jobs[0], nil, buryChange)

			So(pollUntilFor(changeFlushWait, func() bool {
				return notedStateWrites(s) == 0 && s.writing.Load() == 0
			}), ShouldBeTrue)
		})
	})
}
