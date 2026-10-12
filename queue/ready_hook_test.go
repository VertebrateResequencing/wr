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

package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	rhKey    = "k"
	rhData   = "d"
	rhGroup1 = "g1"
	rhGroup2 = "g2"
	rhParent = "p"
	rhKey2   = "k2"
	rhGroup3 = "g3"
	rhTTR    = time.Minute

	// rhCbMark and rhHookMark are what a callback and a ReadyHook append to a
	// shared slice, to show the order they were called in.
	rhCbMark   = "cb"
	rhHookMark = "hook"
)

// TestHasReady proves HasReady reports whether a reserve group has an item in
// the ready sub-queue.
func TestHasReady(t *testing.T) {
	ctx := context.Background()

	Convey("Given an empty queue, nothing is ready", t, func() {
		q := New(ctx, "has ready empty queue")
		defer qdestroy(q)

		So(q.HasReady(rhGroup1), ShouldBeFalse)
	})

	Convey("Given a ready item with group g1", t, func() {
		q := New(ctx, "has ready queue")
		defer qdestroy(q)

		_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "")
		So(err, ShouldBeNil)

		Convey("g1 has a ready item and g2 does not", func() {
			So(q.HasReady(rhGroup1), ShouldBeTrue)
			So(q.HasReady(rhGroup2), ShouldBeFalse)
		})

		Convey("Once it is reserved, g1 has none", func() {
			_, err = q.Reserve(rhGroup1, 0)
			So(err, ShouldBeNil)
			So(q.HasReady(rhGroup1), ShouldBeFalse)
		})

		Convey("Once the queue is destroyed, g1 has none", func() {
			So(q.Destroy(), ShouldBeNil)
			So(q.HasReady(rhGroup1), ShouldBeFalse)
		})
	})

	Convey("Given a ready item with group \"\", the empty group has a ready item", t, func() {
		q := New(ctx, "has ready no group queue")
		defer qdestroy(q)

		_, err := q.Add(ctx, rhKey, "", rhData, 0, 0, rhTTR, "")
		So(err, ShouldBeNil)
		So(q.HasReady(""), ShouldBeTrue)
	})
}

func noSetup(context.Context, *Queue) error { return nil }

// hookCall is one recorded call of a ReadyHook.
type hookCall struct {
	data  any
	group string
}

// TestReadyHook proves the ReadyHook is called, under the queue lock, once for
// every item entering the ready sub-queue and never otherwise, and that the item
// is pushed under the reserve group it returns.
func TestReadyHook(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with a recording hook returning g2", t, func() {
		q := New(ctx, "ready hook queue")
		defer qdestroy(q)

		rec := &hookRecorder{}
		q.SetReadyHook(rec.hook)

		Convey("Add calls it once and the item is only reservable in g2", func() {
			item, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "")
			So(err, ShouldBeNil)

			So(rec.itemCalls(), ShouldResemble, []hookCall{{data: rhData, group: rhGroup1}})
			So(rec.count(), ShouldEqual, 1)
			So(item.ReserveGroup, ShouldEqual, rhGroup2)

			_, err = q.Reserve(rhGroup1, 0)
			So(errors.Is(err, ErrNothingReady), ShouldBeTrue)

			reserved, err := q.Reserve(rhGroup2, 0)
			So(err, ShouldBeNil)
			So(reserved.Key, ShouldEqual, rhKey)
		})

		for _, entry := range readyEntries() {
			Convey("An item entering ready by "+entry.name+" calls it once and is pushed under g2", func() {
				So(entry.setup(ctx, q), ShouldBeNil)
				So(rec.itemCalls(), ShouldBeEmpty)

				So(entry.enter(ctx, q), ShouldBeNil)

				item, state := waitForItemState(q, rhKey, ItemStateReady, 2*time.Second)
				So(state, ShouldEqual, ItemStateReady)
				So(rec.itemCalls(), ShouldHaveLength, 1)
				So(item.ReserveGroup, ShouldEqual, rhGroup2)

				reserved, err := q.Reserve(rhGroup2, 0)
				So(err, ShouldBeNil)
				So(reserved.Key, ShouldEqual, rhKey)
			})
		}

		for _, entry := range notReadyEntries() {
			Convey("An item going elsewhere by "+entry.name+" does not call it", func() {
				So(entry.act(ctx, q, rec.reset), ShouldBeNil)

				item, err := q.Get(rhKey)
				So(err, ShouldBeNil)
				So(item.State(), ShouldEqual, entry.want)
				So(rec.count(), ShouldEqual, 0)
			})
		}

		for _, updater := range depUpdaters() {
			for _, held := range []struct {
				startQueue SubQueue
				state      ItemState
			}{{SubQueueBury, ItemStateBury}, {SubQueueSuspended, ItemStateSuspended}} {
				Convey(updater.name+" removing the last dependency of a "+string(held.state)+
					" item leaves it, and another dependant, where they are without calling it", func() {
					deps := []string{rhParent}

					_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, held.startQueue, deps)
					So(err, ShouldBeNil)

					_, err = q.Add(ctx, rhKey2, rhGroup1, "k2 data", 0, 0, rhTTR, "", deps)
					So(err, ShouldBeNil)

					rec.reset()

					So(updater.update(ctx, q, rhGroup1, 0), ShouldBeNil)

					item, err := q.Get(rhKey)
					So(err, ShouldBeNil)
					So(item.State(), ShouldEqual, held.state)
					So(rec.count(), ShouldEqual, 0)

					stats := q.Stats()
					So(stats.Dependant, ShouldEqual, 1)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Buried+stats.Suspended, ShouldEqual, 1)
				})
			}

			Convey(updater.name+" removing the last dependency with a changed delay does not apply the "+
				"caller's group before the hook", func() {
				_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "", []string{rhParent})
				So(err, ShouldBeNil)

				So(updater.update(ctx, q, rhGroup3, time.Hour), ShouldBeNil)
				So(rec.itemCalls(), ShouldResemble, []hookCall{{data: rhData, group: rhGroup1}})
			})

			Convey(updater.name+" removing the last dependency with an unchanged delay applies the "+
				"caller's group before the hook", func() {
				_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "", []string{rhParent})
				So(err, ShouldBeNil)

				So(updater.update(ctx, q, rhGroup3, 0), ShouldBeNil)
				So(rec.itemCalls(), ShouldResemble, []hookCall{{data: rhData, group: rhGroup3}})
			})
		}

		Convey("Reserves block on the group the hook returns, not the item's original group", func() {
			type result struct {
				item *Item
				err  error
				at   time.Time
			}

			reserveIn := func(group string) <-chan result {
				ch := make(chan result, 1)

				go func() {
					item, err := q.Reserve(group, 2*time.Second)
					ch <- result{item, err, time.Now()}
				}()

				return ch
			}

			g1 := reserveIn(rhGroup1)
			g2 := reserveIn(rhGroup2)

			<-time.After(100 * time.Millisecond)

			addedAt := time.Now()
			_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "")
			So(err, ShouldBeNil)

			var g2res result
			select {
			case g2res = <-g2:
			case <-time.After(200 * time.Millisecond):
			}

			So(g2res.err, ShouldBeNil)
			So(g2res.item, ShouldNotBeNil)
			So(g2res.item.Key, ShouldEqual, rhKey)
			So(g2res.at.Sub(addedAt), ShouldBeLessThan, 200*time.Millisecond)

			returnedEarly := false

			select {
			case <-g1:
				returnedEarly = true
			case <-time.After(time.Until(addedAt.Add(500 * time.Millisecond))):
			}

			So(returnedEarly, ShouldBeFalse)

			g1res := <-g1
			So(errors.Is(g1res.err, ErrNothingReady), ShouldBeTrue)
			So(g1res.at.Sub(addedAt), ShouldBeGreaterThanOrEqualTo, 1500*time.Millisecond)
		})
	})

	Convey("Given KickWith and ResumeWith callbacks and a hook that each append to a shared slice", t, func() {
		q := New(ctx, "ready hook order queue")
		defer qdestroy(q)

		var order []string

		q.SetReadyHook(func(_ any, reserveGroup string) string {
			order = append(order, rhHookMark)

			return reserveGroup
		})

		Convey("KickWith calls its callback before the hook", func() {
			_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, SubQueueBury)
			So(err, ShouldBeNil)

			So(q.KickWith(ctx, rhKey, func(any) { order = append(order, rhCbMark) }), ShouldBeNil)
			So(order, ShouldResemble, []string{rhCbMark, rhHookMark})
		})

		Convey("ResumeWith calls its callback before the hook", func() {
			_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, SubQueueSuspended)
			So(err, ShouldBeNil)

			So(q.ResumeWith(ctx, rhKey, func(any, ItemState) { order = append(order, rhCbMark) }), ShouldBeNil)
			So(order, ShouldResemble, []string{rhCbMark, rhHookMark})
		})
	})

	Convey("Given a hook that returns its reserveGroup argument, an added item keeps its group", t, func() {
		q := New(ctx, "ready hook identity queue")
		defer qdestroy(q)

		q.SetReadyHook(func(_ any, reserveGroup string) string { return reserveGroup })

		item, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "")
		So(err, ShouldBeNil)
		So(item.ReserveGroup, ShouldEqual, rhGroup1)

		reserved, err := q.Reserve(rhGroup1, 0)
		So(err, ShouldBeNil)
		So(reserved.Key, ShouldEqual, rhKey)
	})
}

func readyEntries() []readyEntry {
	return []readyEntry{
		{"AddMany with no delay", noSetup, func(ctx context.Context, q *Queue) error {
			_, _, err := q.AddMany(ctx, []*ItemDef{{Key: rhKey, ReserveGroup: rhGroup1, Data: rhData, TTR: rhTTR}})

			return err
		}},
		{"AddWithSize with no delay", noSetup, func(ctx context.Context, q *Queue) error {
			_, err := q.AddWithSize(ctx, rhKey, rhGroup1, rhData, 0, 1, 0, rhTTR, "")

			return err
		}},
		{"Add with a 20ms delay", noSetup, func(ctx context.Context, q *Queue) error {
			_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 20*time.Millisecond, rhTTR, "")

			return err
		}},
		{"Remove of its only parent", func(ctx context.Context, q *Queue) error {
			if _, err := q.Add(ctx, rhParent, "", "parent data", 0, 0, rhTTR, ""); err != nil {
				return err
			}

			return addInto("", []string{rhParent})(ctx, q)
		}, func(ctx context.Context, q *Queue) error {
			return q.Remove(ctx, rhParent)
		}},
		{"SatisfyDependency of its only dependency", addInto("", []string{rhParent}),
			func(ctx context.Context, q *Queue) error {
				return q.SatisfyDependency(ctx, rhParent)
			}},
		{"KickWith of it buried", addInto(SubQueueBury), func(ctx context.Context, q *Queue) error {
			return q.KickWith(ctx, rhKey, nil)
		}},
		{"ResumeWith of it suspended with no dependencies", addInto(SubQueueSuspended),
			func(ctx context.Context, q *Queue) error {
				return q.ResumeWith(ctx, rhKey, nil)
			}},
		{"Release of it reserved with zero delay", addInto(SubQueueRun), func(ctx context.Context, q *Queue) error {
			return q.Release(ctx, rhKey)
		}},
		{"Requeue of it reserved with nil deps", addInto(SubQueueRun), func(ctx context.Context, q *Queue) error {
			return q.Requeue(ctx, rhKey, nil)
		}},
		{"Update removing its last dependency", addInto("", []string{rhParent}),
			func(ctx context.Context, q *Queue) error {
				return q.Update(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, []string{})
			}},
		{"UpdateHolderUnlessRunning removing its last dependency", addInto("", []string{rhParent}),
			func(ctx context.Context, q *Queue) error {
				_, err := q.UpdateHolderUnlessRunning(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR,
					[]string{}, func() {})

				return err
			}},
		{"TTR expiry with a TTR callback returning SubQueueReady", func(ctx context.Context, q *Queue) error {
			q.SetTTRCallback(func(any) SubQueue { return SubQueueReady })

			_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, 20*time.Millisecond, SubQueueRun)

			return err
		}, func(context.Context, *Queue) error { return nil }},
	}
}

func addInto(startQueue SubQueue, deps ...[]string) func(ctx context.Context, q *Queue) error {
	return func(ctx context.Context, q *Queue) error {
		_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, startQueue, deps...)

		return err
	}
}

// waitForItemState polls the item with the given key until it is in want or
// timeout passes, returning the item and the state it was last seen in.
func waitForItemState(q *Queue, key string, want ItemState, timeout time.Duration) (*Item, ItemState) {
	item, err := q.Get(key)
	if err != nil {
		return nil, ""
	}

	deadline := time.Now().Add(timeout)

	for {
		state := item.State()
		if state == want || time.Now().After(deadline) {
			return item, state
		}

		<-time.After(5 * time.Millisecond)
	}
}

func notReadyEntries() []notReadyEntry {
	deps := []string{rhParent}

	return []notReadyEntry{
		{"Add to delay", ItemStateDelay, addNotReady("", time.Hour)},
		{"Add to dependent", ItemStateDependent, addNotReady("", 0, deps)},
		{"Add to run", ItemStateRun, addNotReady(SubQueueRun, 0)},
		{"Add to bury", ItemStateBury, addNotReady(SubQueueBury, 0)},
		{"Add to suspended", ItemStateSuspended, addNotReady(SubQueueSuspended, 0)},
		{"AddMany to delay", ItemStateDelay, addManyNotReady("", time.Hour, nil)},
		{"AddMany to dependent", ItemStateDependent, addManyNotReady("", 0, deps)},
		{"AddMany to run", ItemStateRun, addManyNotReady(SubQueueRun, 0, nil)},
		{"AddMany to bury", ItemStateBury, addManyNotReady(SubQueueBury, 0, nil)},
		{"AddMany to suspended", ItemStateSuspended, addManyNotReady(SubQueueSuspended, 0, nil)},
		{"Suspend", ItemStateSuspended, func(ctx context.Context, q *Queue, reset func()) error {
			if _, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, ""); err != nil {
				return err
			}

			reset()

			return q.Suspend(ctx, rhKey)
		}},
		{"Bury", ItemStateBury, func(ctx context.Context, q *Queue, reset func()) error {
			if _, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, SubQueueRun); err != nil {
				return err
			}

			reset()

			return q.Bury(rhKey)
		}},
		{"Release given a 1h delay after the reserve", ItemStateDelay,
			func(ctx context.Context, q *Queue, reset func()) error {
				if _, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, ""); err != nil {
					return err
				}

				if _, err := q.Reserve(rhGroup2, 0); err != nil {
					return err
				}

				if err := q.SetDelay(rhKey, time.Hour); err != nil {
					return err
				}

				reset()

				return q.Release(ctx, rhKey)
			}},
		{"Requeue with deps", ItemStateDependent, func(ctx context.Context, q *Queue, reset func()) error {
			if _, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, SubQueueRun); err != nil {
				return err
			}

			reset()

			return q.Requeue(ctx, rhKey, deps)
		}},
		{"ResumeWith to dependent", ItemStateDependent, func(ctx context.Context, q *Queue, reset func()) error {
			if _, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, SubQueueSuspended, deps); err != nil {
				return err
			}

			reset()

			return q.ResumeWith(ctx, rhKey, nil)
		}},
		{"KickWith to dependent", ItemStateDependent, func(ctx context.Context, q *Queue, reset func()) error {
			if _, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, SubQueueBury, deps); err != nil {
				return err
			}

			reset()

			return q.KickWith(ctx, rhKey, nil)
		}},
	}
}

func addNotReady(startQueue SubQueue, delay time.Duration,
	deps ...[]string,
) func(ctx context.Context, q *Queue, reset func()) error {
	return func(ctx context.Context, q *Queue, reset func()) error {
		reset()

		_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, delay, rhTTR, startQueue, deps...)

		return err
	}
}

func addManyNotReady(startQueue SubQueue, delay time.Duration,
	deps []string,
) func(ctx context.Context, q *Queue, reset func()) error {
	return func(ctx context.Context, q *Queue, reset func()) error {
		reset()

		_, _, err := q.AddMany(ctx, []*ItemDef{{
			Key: rhKey, ReserveGroup: rhGroup1, Data: rhData, Delay: delay, TTR: rhTTR,
			StartQueue: startQueue, Dependencies: deps,
		}})

		return err
	}
}

func depUpdaters() []depUpdater {
	return []depUpdater{
		{"Update", func(ctx context.Context, q *Queue, group string, delay time.Duration) error {
			return q.Update(ctx, rhKey, group, rhData, 0, delay, rhTTR, []string{})
		}},
		{"UpdateHolderUnlessRunning", func(ctx context.Context, q *Queue, group string, delay time.Duration) error {
			_, err := q.UpdateHolderUnlessRunning(ctx, rhKey, group, rhData, 0, delay, rhTTR, []string{}, func() {})

			return err
		}},
	}
}

// addResult is how many items addUntilStopped added, and the error that stopped
// it early, if any.
type addResult struct {
	added int
	err   error
}

// TestSetReadyAddedCallbackRace proves the ready-added callback can be replaced
// on a live queue while items are added; run it under -race.
func TestSetReadyAddedCallbackRace(t *testing.T) {
	ctx := context.Background()

	Convey("Given a callback replaced while items are added from another goroutine", t, func() {
		q := New(ctx, "rac replace queue")
		defer qdestroy(q)

		const minReplacements = 1000

		var calls atomic.Int64

		cb := func(string, []any) { calls.Add(1) }

		stop := make(chan struct{})
		done := make(chan addResult, 1)

		go addUntilStopped(ctx, q, stop, done)

		// keep replacing it, with nil half the time, while items are added,
		// until a callback goroutine has read it and called it, so that the
		// replacements overlap those reads
		deadline := time.Now().Add(5 * time.Second)

		for i := 0; (i < minReplacements || calls.Load() == 0) && time.Now().Before(deadline); i++ {
			if i%2 == 0 {
				q.SetReadyAddedCallback(cb)
			} else {
				q.SetReadyAddedCallback(nil)
			}
		}

		close(stop)

		res := <-done
		So(res.err, ShouldBeNil)
		So(calls.Load(), ShouldBeGreaterThan, 0)
		So(q.Stats().Ready, ShouldEqual, res.added)
	})
}

// addUntilStopped adds ready items to q until stop is closed, then sends the
// result to done.
func addUntilStopped(ctx context.Context, q *Queue, stop <-chan struct{}, done chan<- addResult) {
	added := 0

	for {
		select {
		case <-stop:
			done <- addResult{added: added}

			return
		default:
		}

		if _, err := q.Add(ctx, fmt.Sprintf("k%d", added), rhGroup1, added, 0, 0, rhTTR, ""); err != nil {
			done <- addResult{added: added, err: err}

			return
		}

		added++
	}
}

// hookRecorder is a ReadyHook that records its calls and returns rhGroup2. It
// is called with the queue locked, sometimes from the queue's own goroutines,
// so it has its own lock for the test to read it.
type hookRecorder struct {
	mu    sync.Mutex
	calls []hookCall
}

func (r *hookRecorder) hook(data any, reserveGroup string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, hookCall{data: data, group: reserveGroup})

	return rhGroup2
}

func (r *hookRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.calls)
}

// itemCalls returns the calls made for the item rhKey, by its data rhData.
func (r *hookRecorder) itemCalls() []hookCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	var calls []hookCall

	for _, c := range r.calls {
		if c.data == rhData {
			calls = append(calls, c)
		}
	}

	return calls
}

func (r *hookRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = nil
}

// readyEntry is one way for the item rhKey (group rhGroup1, data rhData) to
// enter the ready sub-queue: setup leaves it outside ready without calling the
// hook for it, and enter moves it to ready.
type readyEntry struct {
	name  string
	setup func(ctx context.Context, q *Queue) error
	enter func(ctx context.Context, q *Queue) error
}

// notReadyEntry is one way for the item rhKey to go somewhere other than the
// ready sub-queue: act does any setup, then calls reset, then makes the move,
// leaving the item in want.
type notReadyEntry struct {
	name string
	want ItemState
	act  func(ctx context.Context, q *Queue, reset func()) error
}

// depUpdater is one of the Update methods, removing the last dependency of the
// item rhKey (data rhData) and giving it the given group and delay.
type depUpdater struct {
	name   string
	update func(ctx context.Context, q *Queue, group string, delay time.Duration) error
}

// TestReadyAddedCallbackRunning proves the flag stays set from the add that
// starts the ready-added callback, through the recall hand-off, until the
// recalled callback returns.
func TestReadyAddedCallbackRunning(t *testing.T) {
	ctx := context.Background()

	Convey("Given a ready-added callback that blocks until released", t, func() {
		q := New(ctx, "rac running queue")
		defer qdestroy(q)

		calls := make(chan chan struct{}, 4)

		q.SetReadyAddedCallback(func(string, []any) {
			release := make(chan struct{})
			calls <- release

			<-release
		})

		handOff := make(chan bool, 1)
		hook := func(hq *Queue) {
			if hq == q {
				handOff <- hq.ReadyAddedCallbackRunning()
			}
		}

		recallHandOffHook.Store(&hook)
		defer recallHandOffHook.Store(nil)

		_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "")
		So(err, ShouldBeNil)
		So(q.ReadyAddedCallbackRunning(), ShouldBeTrue)

		first, ok := receiveWithin(calls, 2*time.Second)
		So(ok, ShouldBeTrue)
		So(q.ReadyAddedCallbackRunning(), ShouldBeTrue)

		_, err = q.Add(ctx, rhKey2, rhGroup1, rhData, 0, 0, rhTTR, "")
		So(err, ShouldBeNil)
		So(q.ReadyAddedCallbackRunning(), ShouldBeTrue)

		close(first)

		runningAtHandOff, ok, everIdle := pollRunningUntil(q, handOff, 2*time.Second)
		So(ok, ShouldBeTrue)
		So(everIdle, ShouldBeFalse)
		So(runningAtHandOff, ShouldBeTrue)

		recall, ok := receiveWithin(calls, 2*time.Second)
		So(ok, ShouldBeTrue)
		So(q.ReadyAddedCallbackRunning(), ShouldBeTrue)

		close(recall)

		idle := false
		deadline := time.Now().Add(2 * time.Second)

		for time.Now().Before(deadline) {
			if !q.ReadyAddedCallbackRunning() {
				idle = true

				break
			}

			time.Sleep(10 * time.Millisecond)
		}

		So(idle, ShouldBeTrue)
	})
}

// pollRunningUntil reads q.ReadyAddedCallbackRunning() about every millisecond
// until a value arrives on ch, returning that value, false if none arrived
// within timeout, and whether any read was false. The reads happen in their own
// goroutine, so a read that blocks forever cannot stop the timeout.
func pollRunningUntil(q *Queue, ch <-chan bool, timeout time.Duration) (value, ok, everIdle bool) {
	var idle atomic.Bool

	stop := make(chan struct{})
	defer close(stop)

	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if !q.ReadyAddedCallbackRunning() {
					idle.Store(true)
				}
			}
		}
	}()

	value, ok = receiveWithin(ch, timeout)

	return value, ok, idle.Load()
}

// receiveWithin returns the next value from ch, and false if none arrives
// within timeout.
func receiveWithin[T any](ch <-chan T, timeout time.Duration) (T, bool) {
	select {
	case v := <-ch:
		return v, true
	case <-time.After(timeout):
		var zero T

		return zero, false
	}
}
