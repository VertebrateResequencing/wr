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

// Tests of the log store's rerun guard when an add and an archive race across
// a held or failed batch (spec C4, tests 9 to 11), at the store: running job d
// depends on dep group g and a guarded add of new member m joins g, with the
// calls the server makes for them.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
)

// logGuardHoldWait is how long a test lets a write that must wait for a held
// batch run before checking it has not returned (spec C4 tests 9 to 11).
const logGuardHoldWait = 100 * time.Millisecond

// archivedResult is an archive's outcome and error.
type archivedResult struct {
	outcome archiveOutcome
	err     error
}

// receiveArchived returns the archive result that arrives on ch within wait,
// or one with errOutageTestPending if none does.
func receiveArchived(ch <-chan archivedResult, wait time.Duration) archivedResult {
	select {
	case r := <-ch:
		return r
	case <-time.After(wait):
		return archivedResult{err: errOutageTestPending}
	}
}

func TestLogStoreRerunGuardHeldBatches(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given running job d depending on dep group g, and a guarded add of new member m held before its "+
		"commit", t, func() {
		hold := newLogSyncFailHold()
		s, _ := openTestLogStore(t, ctx)
		d, m := guardTestJobs(ctx, s)

		guard := s.registerRerunGuard()

		Reset(func() { s.releaseRerunGuard(guard) })

		hold.arm()

		added := make(chan storedResult, 1)

		go func() {
			stored, err := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
			added <- storedResult{stored, err}
		}()

		hold.awaitEntered()

		archived := archiveAsync(s, d)

		So(receiveArchived(archived, logGuardHoldWait).err, ShouldEqual, errOutageTestPending)

		Convey("d's archive waits for the add, then keeps d live, also after reopen", func() {
			hold.Release(nil)

			stored := receiveStored(added)
			So(stored.err, ShouldBeNil)
			So(jobKeys(stored.stored.dependents.jobs), ShouldResemble, []string{d.Key()})

			a := receiveArchived(archived, logSyncHoldWait)
			So(a.err, ShouldBeNil)
			So(a.outcome, ShouldEqual, archiveRemovedLive)
			So(guard.keptLive(d.Key()), ShouldBeTrue)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, d.Key(), true)
				soComplete(st, d.Key())
				soLive(st, m.Key(), true)
			})
		})

		Convey("if the add's batch fails with EIO, the add returns it and d's archive removes d; adding m "+
			"again then puts d back", func() {
			hold.Release(syscall.EIO)

			So(receiveStored(added).err, ShouldWrap, syscall.EIO)

			a := receiveArchived(archived, logSyncHoldWait)
			So(a.err, ShouldBeNil)
			So(a.outcome, ShouldEqual, archiveRemovedLive)
			So(guard.keptLive(d.Key()), ShouldBeFalse)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, d.Key(), false)
				soComplete(st, d.Key())
				soLive(st, m.Key(), false)
			})

			again := testDBJob(m.Cmd, m.RepGroup)
			again.DepGroups = []string{"g"}
			stored, err := s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
			So(err, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 2)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, d.Key(), true)
				soLive(st, m.Key(), true)
			})
		})
	})

	Convey("Given running job d depending on dep group g, whose archive is held before its commit", t, func() {
		hold := newLogSyncFailHold()
		s, _ := openTestLogStore(t, ctx)
		d, m := guardTestJobs(ctx, s)

		hold.arm()

		archived := archiveAsync(s, d)

		hold.awaitEntered()

		Convey("a guarded add of m then waits for it, and puts d back, also after reopen", func() {
			guard := s.registerRerunGuard()

			Reset(func() { s.releaseRerunGuard(guard) })

			added := make(chan storedResult, 1)

			go func() {
				stored, err := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
				added <- storedResult{stored, err}
			}()

			select {
			case <-added:
				So("the add returned while the archive was held", ShouldBeEmpty)
			case <-time.After(logGuardHoldWait):
			}

			soLive(s, m.Key(), false)
			hold.Release(nil)

			a := receiveArchived(archived, logSyncHoldWait)
			So(a.err, ShouldBeNil)
			So(a.outcome, ShouldEqual, archiveRemovedLive)

			stored := receiveStored(added)
			So(stored.err, ShouldBeNil)
			So(stored.stored.dependents.wasPutBack(d.Key()), ShouldBeTrue)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, d.Key(), true)
				soComplete(st, d.Key())
				soLive(st, m.Key(), true)
			})
		})
	})
}

// newLogSyncFailHold installs a logSyncFailHold, released and uninstalled when
// the Convey block ends. Make it before the store, so that the release comes
// before the store's close.
func newLogSyncFailHold() *logSyncFailHold {
	h := &logSyncFailHold{entered: make(chan struct{}), release: make(chan error, 1)}

	Reset(func() { h.Release(nil) })
	Reset(wal.SetTestSyncHook(h.hook))

	return h
}

// guardTestJobs adds running job d, depending on dep group g, to s, and returns
// it, completed and ready to archive, with new member m of g, not yet added.
// A suffix makes d and m differ from those of another call.
func guardTestJobs(ctx context.Context, s *logStore, suffix ...string) (*Job, *Job) {
	d := testDBJob("echo c4 guard dependent"+strings.Join(suffix, ""), "c4guard")
	d.Dependencies = Dependencies{NewDepGroupDependency("g")}
	_, err := s.storeNewJobsGuarded(ctx, []*Job{d}, false, nil)
	So(err, ShouldBeNil)

	d.State = JobStateRunning
	d.StartTime = time.Now()
	So(s.updateJobRunStateDurable(d), ShouldBeNil)

	d.State = JobStateComplete
	d.EndTime = time.Now()

	m := testDBJob("echo c4 guard member"+strings.Join(suffix, ""), "c4guard")
	m.DepGroups = []string{"g"}

	return d, m
}

// archiveAsync archives job in s on another goroutine, returning where its
// result arrives.
func archiveAsync(s *logStore, job *Job) <-chan archivedResult {
	ch := make(chan archivedResult, 1)

	go func() {
		outcome, err := s.archiveCompletion(job.Key(), job)
		ch <- archivedResult{outcome, err}
	}()

	return ch
}

// logSyncFailHold is a wal sync hook that holds the first wal file sync after
// arm until Release, which says what that sync returns.
type logSyncFailHold struct {
	armed       bool
	mu          sync.Mutex
	entered     chan struct{}
	release     chan error
	releaseOnce sync.Once
}

func (h *logSyncFailHold) hook(path string) error {
	h.mu.Lock()
	held := h.armed && strings.HasSuffix(path, walFileExt)

	if held {
		h.armed = false
	}
	h.mu.Unlock()

	if !held {
		return nil
	}

	close(h.entered)

	return <-h.release
}

// arm makes the next wal file sync hold.
func (h *logSyncFailHold) arm() {
	h.mu.Lock()
	h.armed = true
	h.mu.Unlock()
}

// Release lets the held sync return err.
func (h *logSyncFailHold) Release(err error) {
	h.releaseOnce.Do(func() { h.release <- err })
}

// awaitEntered waits for the held sync to start.
func (h *logSyncFailHold) awaitEntered() {
	select {
	case <-h.entered:
	case <-time.After(logSyncHoldWait):
		So("the held sync never started", ShouldBeEmpty)
	}
}

func TestLogStoreGuardedPutBacks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given running job d depending on dep group g, and a guarded add of new member m that reads d live", t,
		func() {
			hold := newAppendedHold()
			s, _ := openTestLogStore(t, ctx)
			d, m := guardTestJobs(ctx, s)

			Reset(func() { dependentsReadHook, logPutBackHook = nil, nil })

			m2 := testDBJob("echo c4 guard second member", "c4guard")
			m2.DepGroups = []string{"g"}

			Convey("once d is archived, a delete of d appended before the add's put back of d, but applied after "+
				"it is appended, applies before it, so d is live, also after reopen", func() {
				guard := s.registerRerunGuard()

				Reset(func() { s.releaseRerunGuard(guard) })

				deleted := make(chan error, 1)

				dependentsReadHook = func() {
					dependentsReadHook = nil

					archiveOrPanic(s, d)

					hold.arm()

					go func() { deleted <- s.deleteLiveJobs(ctx, []string{d.Key()}) }()

					hold.awaitEntered()

					go func() {
						time.Sleep(logStorePlanWait)
						hold.Release()
					}()
				}

				stored, err := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
				So(err, ShouldBeNil)
				So(receiveWithin(deleted, logSyncHoldWait), ShouldBeNil)
				So(stored.dependents.wasPutBack(d.Key()), ShouldBeTrue)

				eachReopen(ctx, &s, func(st *logStore) { soLive(st, d.Key(), true) })
			})

			Convey("once d is archived and the add's put back of d appended but not applied, a put back and a "+
				"delete of d made meanwhile apply after it, so d is not live, also after reopen", func() {
				guard := s.registerRerunGuard()

				Reset(func() { s.releaseRerunGuard(guard) })

				dependentsReadHook = func() {
					dependentsReadHook = nil

					archiveOrPanic(s, d)
				}

				logPutBackHook = func() {
					logPutBackHook = nil

					hold.arm()
				}

				added := make(chan storedResult, 1)

				go func() {
					stored, err := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
					added <- storedResult{stored, err}
				}()

				hold.awaitEntered()

				again := testDBJob(d.Cmd, d.RepGroup)
				again.Dependencies = d.Dependencies
				So(s.storeLiveForRerun([]*Job{again}), ShouldBeNil)

				deleted := make(chan error, 1)
				go func() { deleted <- s.deleteLiveJobs(ctx, []string{d.Key()}) }()

				So(receiveWithin(deleted, logGuardHoldWait), ShouldEqual, errOutageTestPending)
				hold.Release()

				r := receiveStored(added)
				So(r.err, ShouldBeNil)
				So(r.stored.dependents.wasPutBack(d.Key()), ShouldBeTrue)
				So(receiveWithin(deleted, logSyncHoldWait), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, d.Key(), false)
				})
			})

			Convey("once d is archived, an add of another member of g putting d back, appended but not applied, "+
				"and the add's put back of d, made meanwhile, do not both put d back, also after reopen", func() {
				var second <-chan storedResult

				parked := make(chan struct{})

				dependentsReadHook = func() {
					dependentsReadHook = nil

					archiveOrPanic(s, d)

					hold.arm()

					second = addAsync(ctx, s, m2)

					hold.awaitEntered()

					logStoreWaitHook = func() {
						logStoreWaitHook = nil

						close(parked)
					}
				}

				Reset(func() { logStoreWaitHook = nil })

				first := addAsync(ctx, s, m)

				select {
				case <-parked:
				case <-time.After(logSyncHoldWait):
					So("the add never waited for the other add's put back", ShouldBeEmpty)
				}

				hold.Release()

				a, b := receiveStored(first), receiveStored(second)
				So(a.err, ShouldBeNil)
				So(b.err, ShouldBeNil)
				So(putBacksOf(d.Key(), a.stored, b.stored), ShouldEqual, 1)

				eachReopen(ctx, &s, func(st *logStore) { soLive(st, d.Key(), true) })
			})

			Convey("once d is archived, the add's put back of d, appended but not applied, and an add of another "+
				"member of g made meanwhile do not both put d back, also after reopen", func() {
				planned := make(chan struct{})

				dependentsReadHook = func() {
					dependentsReadHook = func() {
						dependentsReadHook = nil

						close(planned)
					}

					archiveOrPanic(s, d)
				}

				logPutBackHook = func() {
					logPutBackHook = nil

					hold.arm()
				}

				first := addAsync(ctx, s, m)

				hold.awaitEntered()

				parked := signalAddWaits()
				second := addAsync(ctx, s, m2)

				select {
				case <-planned:
				case <-parked:
				case <-time.After(logSyncHoldWait):
					So("the second add neither read its dependents nor waited", ShouldBeEmpty)
				}

				hold.Release()

				a, b := receiveStored(first), receiveStored(second)
				So(a.err, ShouldBeNil)
				So(b.err, ShouldBeNil)
				So(putBacksOf(d.Key(), a.stored, b.stored), ShouldEqual, 1)

				eachReopen(ctx, &s, func(st *logStore) { soLive(st, d.Key(), true) })
			})

			Convey("once d is archived and the add's put back of d appended but not applied, a transition of d "+
				"waiting for it and for a put back of d (storeLiveForRerun) made meanwhile still waits for that "+
				"once the add returns, so d ends with the transition's state, also after reopen", func() {
				guard := s.registerRerunGuard()

				Reset(func() { s.releaseRerunGuard(guard) })

				dependentsReadHook = func() {
					dependentsReadHook = nil

					archiveOrPanic(s, d)
				}

				logPutBackHook = func() {
					logPutBackHook = nil

					hold.arm()
				}

				added := make(chan storedResult, 1)

				go func() {
					stored, err := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
					added <- storedResult{stored, err}
				}()

				hold.awaitEntered()

				again, unlockAgain := lockedCopy(d)
				again.Dependencies = d.Dependencies

				Reset(unlockAgain)

				putBack := make(chan error, 1)
				go func() { putBack <- s.storeLiveForRerun([]*Job{again}) }()

				awaitRevivingWrites(s, d.Key(), 2)

				tr := testDBJob(d.Cmd, d.RepGroup)
				tr.Dependencies = d.Dependencies
				tr.State = JobStateRunning
				tr.Attempts = 5
				tr.StartTime = time.Now()

				parked := signalAddWaits()
				transitioned := make(chan error, 1)

				go func() { transitioned <- s.updateJobRunStateDurable(tr) }()

				awaitParked(parked)
				hold.Release()

				So(receiveStored(added).err, ShouldBeNil)
				So(receiveWithin(transitioned, logGuardHoldWait), ShouldEqual, errOutageTestPending)

				unlockAgain()
				So(receiveWithin(putBack, logSyncHoldWait), ShouldBeNil)
				So(receiveWithin(transitioned, logSyncHoldWait), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					So(storedLiveJob(t, st, d.Key()).Attempts, ShouldEqual, 5)
				})
			})
		})
}

// archiveOrPanic archives job in s, panicking on error: for a hook, which runs
// off the test's goroutine.
func archiveOrPanic(s *logStore, job *Job) {
	if _, err := s.archiveCompletion(job.Key(), job); err != nil {
		panic(err)
	}
}

// putBacksOf returns how many of adds report the keyed dependent put back: as
// one to queue, or as put back by a guarded add.
func putBacksOf(key string, adds ...storedNewJobs) int {
	n := 0

	for _, add := range adds {
		if add.dependents.wasPutBack(key) || slices.Contains(jobKeys(add.jobsToQueue), key) {
			n++
		}
	}

	return n
}

func TestLogStoreRevivingPutBacks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given running job d depending on dep group g, a guarded add of new member m that reads d live, and "+
		"a held wal sync", t, func() {
		hold := newLogSyncFailHold()
		s, _ := openTestLogStore(t, ctx)
		d, m := guardTestJobs(ctx, s)

		Reset(func() { dependentsReadHook, logPutBackHook = nil, nil })

		Convey("once d is archived, and a put back of d (storeLiveForRerun) is making its records, the add waits "+
			"for it, and puts d back once its batch fails with EIO, also after reopen", func() {
			guard := s.registerRerunGuard()

			Reset(func() { s.releaseRerunGuard(guard) })

			again, unlockAgain := lockedCopy(d)
			again.Dependencies = d.Dependencies

			Reset(unlockAgain)

			putBack := make(chan error, 1)
			read, goOn := make(chan struct{}), make(chan struct{})

			dependentsReadHook = func() {
				dependentsReadHook = nil

				archiveOrPanic(s, d)

				go func() { putBack <- s.storeLiveForRerun([]*Job{again}) }()

				close(read)
				<-goOn
			}

			added := make(chan storedResult, 1)

			go func() {
				stored, err := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
				added <- storedResult{stored, err}
			}()

			select {
			case <-read:
			case <-time.After(logSyncHoldWait):
				So("the add never read its dependents", ShouldBeEmpty)
			}

			awaitReviving(s, d.Key())

			parked := signalAddWaits()

			close(goOn)
			awaitParked(parked)
			soLive(s, m.Key(), true)

			hold.arm()
			unlockAgain()
			hold.awaitEntered()
			hold.Release(syscall.EIO)

			So(receiveWithin(putBack, logSyncHoldWait), ShouldWrap, syscall.EIO)

			r := receiveStored(added)
			So(r.err, ShouldBeNil)
			So(r.stored.dependents.wasPutBack(d.Key()), ShouldBeTrue)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, d.Key(), true)
				soLive(st, m.Key(), true)
			})
		})

		Convey("once d is archived and the add's put back of d appended, an add of another member of g waits "+
			"for it, and puts d back once the first add's batch fails with EIO, also after reopen", func() {
			m2 := testDBJob("echo c4 guard second member", "c4guard")
			m2.DepGroups = []string{"g"}

			dependentsReadHook = func() {
				dependentsReadHook = nil

				archiveOrPanic(s, d)
			}

			logPutBackHook = func() {
				logPutBackHook = nil

				hold.arm()
			}

			first := addAsync(ctx, s, m)

			hold.awaitEntered()

			parked := signalAddWaits()
			second := addAsync(ctx, s, m2)

			awaitParked(parked)
			hold.Release(syscall.EIO)

			So(receiveStored(first).err, ShouldWrap, syscall.EIO)

			r := receiveStored(second)
			So(r.err, ShouldBeNil)
			So(jobKeys(r.stored.jobsToQueue), ShouldContain, d.Key())

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, d.Key(), true)
				soLive(st, m.Key(), false)
			})
		})
	})

	Convey("Given running jobs x and y depending on dep group g, and a held wal sync", t, func() {
		hold := newLogSyncFailHold()
		s, _ := openTestLogStore(t, ctx)
		x, m1 := guardTestJobs(ctx, s)
		y, m2 := guardTestJobs(ctx, s, "y")

		Reset(func() { dependentsReadHook = nil })

		Convey("an add of m1 that read both live and puts x back, deferring y, which an add of m2 that read x "+
			"live puts back, deferring x, both finish, each putting one back, also after reopen", func() {
			planned, goOn := make(chan struct{}), make(chan struct{})

			var second <-chan storedResult

			dependentsReadHook = func() {
				dependentsReadHook = func() {
					dependentsReadHook = nil

					archiveOrPanic(s, x)
					close(planned)

					select {
					case <-goOn:
					case <-time.After(logSyncHoldWait):
					}
				}

				archiveOrPanic(s, y)

				second = addAsync(ctx, s, m2)

				select {
				case <-planned:
				case <-time.After(logSyncHoldWait):
				}

				hold.arm()
			}

			first := addAsync(ctx, s, m1)

			hold.awaitEntered()
			close(goOn)
			awaitParkedOrWriting(s, nil)
			hold.Release(nil)

			a, b := receiveStored(first), receiveStored(second)
			So(a.err, ShouldBeNil)
			So(b.err, ShouldBeNil)
			So(putBacksOf(x.Key(), a.stored, b.stored), ShouldEqual, 1)
			So(putBacksOf(y.Key(), a.stored, b.stored), ShouldEqual, 1)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, x.Key(), true)
				soLive(st, y.Key(), true)
			})
		})
	})
}

// newLogSyncFailHolds installs a wal sync hook that hands each wal file sync to
// the latest hold that the returned func makes, armed, so a test can hold, then
// fail, one batch's sync after another. The latest is released when the Convey
// block ends. Make it before the store, as newLogSyncFailHold.
func newLogSyncFailHolds() func() *logSyncFailHold {
	var latest atomic.Pointer[logSyncFailHold]

	Reset(func() {
		if h := latest.Load(); h != nil {
			h.Release(nil)
		}
	})
	Reset(wal.SetTestSyncHook(func(path string) error {
		if h := latest.Load(); h != nil {
			return h.hook(path)
		}

		return nil
	}))

	return func() *logSyncFailHold {
		h := &logSyncFailHold{entered: make(chan struct{}), release: make(chan error, 1)}
		h.arm()
		latest.Store(h)

		return h
	}
}

// failNextSync holds the next wal file sync and fails it with EIO.
func failNextSync(hold func() *logSyncFailHold, start func()) {
	h := hold()

	start()
	h.awaitEntered()
	h.Release(syscall.EIO)
}

// revivingAdd is a guarded add of new member m of dep group g whose dependent
// d was archived after the add read it live, while a put-back of d
// (storeLiveForRerun) is making its records.
type revivingAdd struct {
	added   <-chan storedResult
	putBack <-chan error

	// goOn lets the add go on to append its block.
	goOn func()

	// unlockAgain lets the put-back of d go on to append its block.
	unlockAgain func()
}

// startRevivingAdd starts a guarded add of m to s that reads d live; d is then
// archived and a put-back of d starts, holding before it appends. It returns
// once d is reviving, with the add held until goOn.
func startRevivingAdd(ctx context.Context, s *logStore, d, m *Job) revivingAdd {
	guard := s.registerRerunGuard()

	Reset(func() { s.releaseRerunGuard(guard) })

	again, unlockAgain := lockedCopy(d)
	again.Dependencies = d.Dependencies

	Reset(unlockAgain)

	putBack := make(chan error, 1)
	read, goOn := make(chan struct{}), make(chan struct{})
	letGoOn := sync.OnceFunc(func() { close(goOn) })

	Reset(letGoOn)

	dependentsReadHook = func() {
		dependentsReadHook = nil

		archiveOrPanic(s, d)

		go func() { putBack <- s.storeLiveForRerun([]*Job{again}) }()

		close(read)
		<-goOn
	}

	added := make(chan storedResult, 1)

	go func() {
		stored, err := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
		added <- storedResult{stored, err}
	}()

	select {
	case <-read:
	case <-time.After(logSyncHoldWait):
		So("the add never read its dependents", ShouldBeEmpty)
	}

	awaitReviving(s, d.Key())

	return revivingAdd{added: added, putBack: putBack, goOn: letGoOn, unlockAgain: unlockAgain}
}

func TestLogStoreDeferredPutBackFailures(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given running job d depending on dep group g, a guarded add of new member m that reads d live, d "+
		"then archived and a put back of d (storeLiveForRerun) making its records", t, func() {
		hold := newLogSyncFailHolds()
		s, _ := openTestLogStore(t, ctx)
		d, m := guardTestJobs(ctx, s)

		Reset(func() { dependentsReadHook, logPutBackHook = nil, nil })

		ra := startRevivingAdd(ctx, s, d, m)

		Convey("when the add's own block fails with EIO, the add returns that error without waiting for the "+
			"put-back of d, which puts d back, and m is not stored, also after reopen", func() {
			failNextSync(hold, ra.goOn)

			r := receiveStored(ra.added)
			So(r.err, ShouldWrap, syscall.EIO)

			ra.unlockAgain()
			So(receiveWithin(ra.putBack, logSyncHoldWait), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, m.Key(), false)
				soLive(st, d.Key(), true)
			})
		})

		Convey("when the put-back of d fails with EIO, then so does the add's further block putting d back, "+
			"the add returns that error, not reporting d put back, with m stored and d not, also after reopen",
			func() {
				parkDeferringAdd(s, ra, m)

				decided, goOn := holdNextPutBackDecision(func() {})

				Reset(goOn)

				failNextSync(hold, ra.unlockAgain)
				So(receiveWithin(ra.putBack, logSyncHoldWait), ShouldWrap, syscall.EIO)

				awaitClosed(decided, "the add never decided on d again")

				// the wal has opened its next segment, so the next sync held is the further block's
				So(s.updateJobRunStateDurable(m), ShouldBeNil)

				failNextSync(hold, goOn)

				r := receiveStored(ra.added)
				So(r.err, ShouldWrap, syscall.EIO)
				So(r.stored.dependents.wasPutBack(d.Key()), ShouldBeFalse)

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, m.Key(), true)
					soLive(st, d.Key(), false)
				})
			})

		Convey("when the put-back of d fails with EIO, a delete of d made as the add decides on d again waits "+
			"for the add's further block putting d back, so d is not live, also after reopen", func() {
			parkDeferringAdd(s, ra, m)

			deleted := make(chan error, 1)
			decided, goOn := holdNextPutBackDecision(func() {
				go func() { deleted <- s.deleteLiveJobs(ctx, []string{d.Key()}) }()
			})

			Reset(goOn)

			failNextSync(hold, ra.unlockAgain)
			So(receiveWithin(ra.putBack, logSyncHoldWait), ShouldWrap, syscall.EIO)

			awaitClosed(decided, "the add never decided on d again")
			So(receiveWithin(deleted, logGuardHoldWait), ShouldEqual, errOutageTestPending)
			goOn()

			r := receiveStored(ra.added)
			So(r.err, ShouldBeNil)
			So(r.stored.dependents.wasPutBack(d.Key()), ShouldBeTrue)
			So(receiveWithin(deleted, logSyncHoldWait), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, m.Key(), true)
				soLive(st, d.Key(), false)
			})
		})

		Convey("when the put-back of d fails with EIO, and another put-back of d, making its records as the "+
			"add decides again, also fails, the add defers d again and then puts it back, also after reopen",
			func() {
				again2, unlock2 := lockedCopy(d)
				again2.Dependencies = d.Dependencies

				Reset(unlock2)

				parkDeferringAdd(s, ra, m)

				putBack2 := make(chan error, 1)
				decided, goOn := holdNextPutBackDecision(func() {
					go func() { putBack2 <- s.storeLiveForRerun([]*Job{again2}) }()
				})

				Reset(goOn)

				failNextSync(hold, ra.unlockAgain)
				So(receiveWithin(ra.putBack, logSyncHoldWait), ShouldWrap, syscall.EIO)

				awaitClosed(decided, "the add never decided on d again")
				awaitReviving(s, d.Key())
				goOn()

				// the wal has opened its next segment, so the next sync held is the second put-back's
				So(s.updateJobRunStateDurable(m), ShouldBeNil)

				failNextSync(hold, unlock2)
				So(receiveWithin(putBack2, logSyncHoldWait), ShouldWrap, syscall.EIO)

				r := receiveStored(ra.added)
				So(r.err, ShouldBeNil)
				So(r.stored.dependents.wasPutBack(d.Key()), ShouldBeTrue)

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, m.Key(), true)
					soLive(st, d.Key(), true)
				})
			})
	})
}

// holdNextPutBackDecision makes the next put-back decision of a guarded add
// call then wait, closing the returned decided channel once then has
// returned, until go on is called or logSyncHoldWait passes.
func holdNextPutBackDecision(then func()) (decided <-chan struct{}, goOn func()) {
	reached, cont := make(chan struct{}), make(chan struct{})

	logPutBackHook = func() {
		logPutBackHook = nil

		then()
		close(reached)

		select {
		case <-cont:
		case <-time.After(logSyncHoldWait):
		}
	}

	return reached, sync.OnceFunc(func() { close(cont) })
}

// awaitClosed fails with what if ch is not closed within logSyncHoldWait.
func awaitClosed(ch <-chan struct{}, what string) {
	select {
	case <-ch:
	case <-time.After(logSyncHoldWait):
		So(what, ShouldBeEmpty)
	}
}

// parkDeferringAdd lets ra's add go on, deferring d, and returns once its
// block is durable and it waits for the put-back of d.
func parkDeferringAdd(s *logStore, ra revivingAdd, m *Job) {
	parked := signalAddWaits()

	ra.goOn()
	awaitParked(parked)
	soLive(s, m.Key(), true)
}
