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

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// TestStatusStateLockOrder drives a real queue.Queue under a concurrent storm to
// prove statusState's mutex is a strict leaf: the TTR callback acquires it as
// queue.mutex -> job -> statusState.mu, while a concurrent drainer takes
// statusState.mu alone. There must be no lock-order inversion or deadlock (run
// with -race), and no *Job/*Item pointer or internal map escapes the lock.
func TestStatusStateLockOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("statusState stays a leaf lock under a real-queue storm", t, func() {
		ctx := context.Background()
		q := queue.New(ctx, "statusstate-lockorder")
		ss := newStatusState()

		// changed callback: detached goroutine (queue unlocked) -> job lock ->
		// statusState.mu, exactly like the production server.
		q.SetChangedCallback(func(fromQ, toQ queue.SubQueue, data []any) {
			from := subqueueToJobState[fromQ]
			to := subqueueToJobState[toQ]
			groups := make(map[string]int)

			for _, inter := range data {
				job := inter.(*Job) //nolint:errcheck,forcetypeassert
				job.RLock()
				rg := job.RepGroup
				job.RUnlock()

				groups[rg]++
			}

			for rg, count := range groups {
				ss.applyTransition(from, to, rg, count)
			}
		})

		// TTR callback: runs while holding queue.mutex, takes job lock then
		// statusState.mu (the running->lost transition). This is the path that
		// must never invert against a snapshot taking statusState.mu first.
		q.SetTTRCallback(func(data any) queue.SubQueue {
			job := data.(*Job) //nolint:errcheck,forcetypeassert
			job.Lock()
			rg := job.RepGroup
			job.Unlock()
			ss.applyTransition(JobStateRunning, JobStateLost, rg, 1)

			return queue.SubQueueRun
		})

		var wg sync.WaitGroup

		stop := make(chan struct{})

		// concurrent drainers take statusState.mu alone (leaf).
		for range 4 {
			wg.Go(func() {
				sub := ss.subscribe()
				defer ss.unsubscribe(sub)

				for {
					select {
					case <-stop:
						ss.drain(sub)

						return
					case <-sub.wake:
						drained := ss.drain(sub)
						// touch the copy to ensure it is independent.
						for _, counts := range drained {
							_ = counts[JobStateRunning]
						}
					}
				}
			})
		}

		// concurrent adders + reservers drive transitions and TTR expiry.
		const groups = 8

		var added int64

		for g := range groups {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()

				rg := "rg" + strconv.Itoa(g)

				for i := range 50 {
					select {
					case <-stop:
						return
					default:
					}

					key := rg + "-" + strconv.Itoa(i)
					job := &Job{RepGroup: rg, Cmd: key}

					_, err := q.Add(ctx, key, "", job, 0, 0, 20*time.Millisecond, queue.SubQueueReady)
					if err != nil {
						continue
					}

					atomic.AddInt64(&added, 1)
				}
			}(g)
		}

		// reservers move ready->run, then let some hit TTR (running->lost).
		for range 4 {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}

					item, err := q.Reserve("", time.Millisecond)
					if err != nil || item == nil {
						continue
					}
					// occasionally touch to keep it alive, otherwise let TTR fire.
					if atomic.LoadInt64(&added)%2 == 0 {
						q.Touch(item.Key) //nolint:errcheck
					}
				}
			})
		}

		// run the storm for a bounded window, then stop everything.
		time.Sleep(750 * time.Millisecond)
		close(stop)

		done := make(chan struct{})

		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			So("storm goroutines did not finish - possible deadlock", ShouldBeBlank)
		}

		// The whole point is no deadlock / no inversion under -race; if we got
		// here cleanly the leaf-lock discipline held. Sanity-check we actually
		// exercised the queue and accumulated some state.
		So(atomic.LoadInt64(&added), ShouldBeGreaterThan, 0)
		So(len(ss.snapshot()), ShouldBeGreaterThan, 0)

		err := q.Destroy()
		So(err, ShouldBeNil)
	})
}

// TestStatusStateSeedSemantics covers the fresh-connect seed a refreshed or
// newly-loaded status page receives as its first drain (issue: removed jobs
// reappear after refresh). A RepGroup with live jobs is seeded with its live
// states plus complete, but never deleted; a normally completed RepGroup is
// seeded as complete-only; deleted-only and complete+deleted-only RepGroups are
// omitted. Live updates AFTER subscribe must still deliver complete and deleted
// (the transient red bar and the completes-while-open retain) to already-
// connected clients.
func TestStatusStateSeedSemantics(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("a fresh subscriber's seed only shows live RepGroups and never deleted", t, func() {
		ss := newStatusState()

		// liveRG ends with live + complete + deleted (a removal mid-flight that
		// left some jobs still ready and some already complete).
		ss.applyTransition(JobStateNew, JobStateReady, "liveRG", 5)
		ss.applyTransition(JobStateReady, JobStateComplete, "liveRG", 2)
		ss.applyTransition(JobStateReady, JobStateDeleted, "liveRG", 1)

		// doneRG ends complete-only (all its jobs finished normally).
		ss.applyTransition(JobStateNew, JobStateReady, "doneRG", 3)
		ss.applyTransition(JobStateReady, JobStateComplete, "doneRG", 3)

		// goneRG ends deleted-only (every job was removed before completing) -
		// this is the echo-after-`wr remove -a` shape with no completes.
		ss.applyTransition(JobStateNew, JobStateReady, "goneRG", 4)
		ss.applyTransition(JobStateReady, JobStateDeleted, "goneRG", 4)

		// mixedTerminalRG is the removed-jobs-refresh shape: some jobs completed
		// normally, then the remaining jobs were removed. A refresh must not show
		// that row again as completed or deleted, because part of the RepGroup was
		// explicitly removed.
		ss.applyTransition(JobStateNew, JobStateReady, "mixedTerminalRG", 7)
		ss.applyTransition(JobStateReady, JobStateComplete, "mixedTerminalRG", 3)
		ss.applyTransition(JobStateReady, JobStateDeleted, "mixedTerminalRG", 4)

		sub := ss.subscribe()
		defer ss.unsubscribe(sub)

		seed := ss.drain(sub)
		So(seed, ShouldNotBeNil)

		Convey("a live+complete+deleted RepGroup seeds as live+complete, no deleted", func() {
			So(seed, ShouldContainKey, "liveRG")
			So(seed["liveRG"][JobStateReady], ShouldEqual, 2)
			So(seed["liveRG"][JobStateComplete], ShouldEqual, 2)
			_, hasDeleted := seed["liveRG"][JobStateDeleted]
			So(hasDeleted, ShouldBeFalse)
		})

		Convey("a complete-only RepGroup is seeded as complete", func() {
			So(seed, ShouldContainKey, "doneRG")
			So(seed["doneRG"], ShouldResemble, map[JobState]int{JobStateComplete: 3})
		})

		Convey("a deleted-only RepGroup is omitted from the seed", func() {
			So(seed, ShouldNotContainKey, "goneRG")
		})

		Convey("a complete+deleted-only RepGroup is omitted from the seed", func() {
			So(seed, ShouldNotContainKey, "mixedTerminalRG")
		})

		Convey("the +all+ live aggregate is seeded (and excludes terminal states)", func() {
			So(seed, ShouldContainKey, statusAllRepGroups)
			So(seed[statusAllRepGroups][JobStateReady], ShouldEqual, 2)
			_, hasComplete := seed[statusAllRepGroups][JobStateComplete]
			So(hasComplete, ShouldBeFalse)

			_, hasDeleted := seed[statusAllRepGroups][JobStateDeleted]
			So(hasDeleted, ShouldBeFalse)
		})
	})

	Convey("a transition AFTER subscribe still delivers deleted to that client", t, func() {
		ss := newStatusState()
		ss.applyTransition(JobStateNew, JobStateReady, "rg", 5)

		sub := ss.subscribe()
		defer ss.unsubscribe(sub)

		// drain the seed first (rg is live, so present, no deleted yet).
		seed := ss.drain(sub)
		_, hasDeleted := seed["rg"][JobStateDeleted]
		So(hasDeleted, ShouldBeFalse)

		// now a removal happens while this client is connected: the live red
		// deleted bar MUST be delivered to it.
		ss.applyTransition(JobStateReady, JobStateDeleted, "rg", 2)

		update := ss.drain(sub)
		So(update, ShouldContainKey, "rg")
		So(update["rg"][JobStateDeleted], ShouldEqual, 2)
		So(update["rg"][JobStateReady], ShouldEqual, 3)
	})

	Convey("a RepGroup that completes WHILE connected stays delivered (260625-6 live-retain)", t, func() {
		ss := newStatusState()
		ss.applyTransition(JobStateNew, JobStateReady, "rg", 4)

		sub := ss.subscribe()
		defer ss.unsubscribe(sub)

		// drain the seed (rg is live).
		So(ss.drain(sub), ShouldContainKey, "rg")

		// rg now completes entirely while the page is open; the connected client
		// must still receive the complete count so the row stays visible.
		ss.applyTransition(JobStateReady, JobStateComplete, "rg", 4)

		update := ss.drain(sub)
		So(update, ShouldContainKey, "rg")
		So(update["rg"][JobStateComplete], ShouldEqual, 4)
		So(update["rg"][JobStateReady], ShouldEqual, 0)
	})

	Convey("a transition between subscribe and the first drain is included in the seed", t, func() {
		ss := newStatusState()
		ss.applyTransition(JobStateNew, JobStateReady, "rg", 2)

		sub := ss.subscribe()
		defer ss.unsubscribe(sub)

		// a removal races in before the very first drain; the seed must reflect
		// current state (the client renders whatever the first drain holds), and
		// a transition observed live this way may carry deleted.
		ss.applyTransition(JobStateReady, JobStateDeleted, "rg", 2)

		seed := ss.drain(sub)
		So(seed, ShouldContainKey, "rg")
		So(seed["rg"][JobStateDeleted], ShouldEqual, 2)
		So(seed["rg"][JobStateReady], ShouldEqual, 0)
	})
}

func TestStatusState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("statusState tracks absolute per-RepGroup counts idempotently", t, func() {
		ss := newStatusState()

		Convey("applyTransition maintains absolute counts and the +all+ aggregate", func() {
			ss.applyTransition(JobStateNew, JobStateReady, "rg1", 5)
			ss.applyTransition(JobStateReady, JobStateRunning, "rg1", 2)

			snap := ss.snapshot()
			So(snap["rg1"][JobStateReady], ShouldEqual, 3)
			So(snap["rg1"][JobStateRunning], ShouldEqual, 2)
			So(snap[statusAllRepGroups][JobStateReady], ShouldEqual, 3)
			So(snap[statusAllRepGroups][JobStateRunning], ShouldEqual, 2)

			Convey("complete/deleted leave the +all+ live aggregate", func() {
				ss.applyTransition(JobStateRunning, JobStateComplete, "rg1", 2)

				snap := ss.snapshot()
				So(snap["rg1"][JobStateComplete], ShouldEqual, 2)
				So(snap["rg1"][JobStateRunning], ShouldEqual, 0)
				// +all+ shows only live jobs, so complete is not counted there.
				So(snap[statusAllRepGroups][JobStateComplete], ShouldEqual, 0)
				So(snap[statusAllRepGroups][JobStateRunning], ShouldEqual, 0)
				So(snap[statusAllRepGroups][JobStateReady], ShouldEqual, 3)
			})
		})

		Convey("counts are clamped at zero so a duplicate/lost decrement cannot go negative", func() {
			ss.applyTransition(JobStateNew, JobStateReady, "rg1", 2)
			// two running->complete for only two ready jobs would imply -2 ready;
			// it must clamp at 0, never negative.
			ss.applyTransition(JobStateReady, JobStateComplete, "rg1", 2)
			ss.applyTransition(JobStateReady, JobStateComplete, "rg1", 2)

			snap := ss.snapshot()
			So(snap["rg1"][JobStateReady], ShouldEqual, 0)
			So(snap["rg1"][JobStateComplete], ShouldEqual, 4)
		})

		Convey("applying the same absolute state twice is a no-op (idempotency)", func() {
			ss.applyTransition(JobStateNew, JobStateRunning, "rg1", 3)

			view := make(map[string]map[JobState]int)

			absolute := ss.snapshot()
			for repGroup, counts := range absolute {
				applyAbsolute(view, repGroup, counts)
			}

			first := view["rg1"][JobStateRunning]

			// apply the very same absolute snapshot again.
			for repGroup, counts := range absolute {
				applyAbsolute(view, repGroup, counts)
			}

			second := view["rg1"][JobStateRunning]

			So(first, ShouldEqual, 3)
			So(second, ShouldEqual, 3)
		})

		Convey("skipping an intermediate absolute value still converges (self-heal)", func() {
			view := make(map[string]map[JobState]int)

			// the server progresses through several states ...
			ss.applyTransition(JobStateNew, JobStateReady, "rg1", 10)
			// (client misses this intermediate drain)
			ss.applyTransition(JobStateReady, JobStateRunning, "rg1", 4)
			ss.applyTransition(JobStateReady, JobStateRunning, "rg1", 6)
			ss.applyTransition(JobStateRunning, JobStateComplete, "rg1", 10)

			// the client only ever sees the final absolute state, never the
			// intermediates, yet converges exactly.
			final := ss.snapshot()
			for repGroup, counts := range final {
				applyAbsolute(view, repGroup, counts)
			}

			So(view["rg1"][JobStateReady], ShouldEqual, 0)
			So(view["rg1"][JobStateRunning], ShouldEqual, 0)
			So(view["rg1"][JobStateComplete], ShouldEqual, 10)
			So(view["rg1"][JobStateReady], ShouldEqual, 0)
		})
	})

	Convey("statusState seeds and replays the live current state to subscribers", t, func() {
		ss := newStatusState()
		ss.seed(map[string]map[JobState]int{
			"rgA": {JobStateRunning: 2, JobStateComplete: 4},
			"rgB": {JobStateReady: 1},
		})

		Convey("a new subscriber's first drain is the live current state (complete kept on live groups)", func() {
			sub := ss.subscribe()
			defer ss.unsubscribe(sub)

			full := ss.drain(sub)
			So(full, ShouldNotBeNil)
			// rgA has live running jobs, so it is seeded with its live states
			// plus its already-finished complete count.
			So(full["rgA"][JobStateRunning], ShouldEqual, 2)
			So(full["rgA"][JobStateComplete], ShouldEqual, 4)
			So(full["rgB"][JobStateReady], ShouldEqual, 1)

			Convey("thereafter only changed RepGroups are drained", func() {
				// nothing changed since the full drain.
				So(ss.drain(sub), ShouldBeNil)

				ss.applyTransition(JobStateReady, JobStateRunning, "rgB", 1)

				delta := ss.drain(sub)
				So(delta, ShouldContainKey, "rgB")
				So(delta, ShouldContainKey, statusAllRepGroups)
				So(delta, ShouldNotContainKey, "rgA")
				So(delta["rgB"][JobStateRunning], ShouldEqual, 1)
				So(delta["rgB"][JobStateReady], ShouldEqual, 0)
			})
		})

		// A reconnect is a brand-new subscription, so it gets the same seed a fresh
		// page load gets. A RepGroup that became complete-only while the now-closed
		// connection was up remains visible as completed progress; removed groups
		// are distinguished by their deleted contribution in the seed semantics
		// above.
		Convey("reconnect (a fresh subscription) keeps now-complete groups visible", func() {
			sub1 := ss.subscribe()
			ss.drain(sub1)
			// rgA's last 2 running jobs complete, so rgA is now complete-only.
			ss.applyTransition(JobStateRunning, JobStateComplete, "rgA", 2)
			ss.unsubscribe(sub1)

			// simulate reconnect: brand-new subscriber.
			sub2 := ss.subscribe()
			defer ss.unsubscribe(sub2)

			full := ss.drain(sub2)
			So(full["rgA"], ShouldResemble, map[JobState]int{JobStateComplete: 6})
			So(full["rgB"][JobStateReady], ShouldEqual, 1)
		})
	})

	Convey("drained maps are fresh copies that do not alias internal state", t, func() {
		ss := newStatusState()
		ss.applyTransition(JobStateNew, JobStateRunning, "rg1", 1)

		sub := ss.subscribe()
		defer ss.unsubscribe(sub)

		drained := ss.drain(sub)
		// mutating the drained copy must not corrupt the authoritative state.
		drained["rg1"][JobStateRunning] = 9999
		drained["rg1"][JobStateBuried] = 9999

		snap := ss.snapshot()
		So(snap["rg1"][JobStateRunning], ShouldEqual, 1)
		So(snap["rg1"][JobStateBuried], ShouldEqual, 0)
	})
}

// applyAbsolute mimics the client: it replaces a RepGroup's counts wholesale
// from an absolute message, which is how the idempotent protocol converges.
func applyAbsolute(view map[string]map[JobState]int, repGroup string, counts map[JobState]int) {
	fresh := make(map[JobState]int, len(counts))
	for state, count := range counts {
		if count > 0 {
			fresh[state] = count
		}
	}

	view[repGroup] = fresh
}
