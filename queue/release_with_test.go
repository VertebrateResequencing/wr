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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// runEndRecorder is a ReleaseWith, RequeueWith or BuryWith callback that
// records, on each call, the data it got, the state of the reserved item and
// whether the queue was locked.
type runEndRecorder struct {
	q         *Queue
	item      *Item
	order     *[]string
	calls     int
	data      any
	state     ItemState
	queueFree bool
}

func (r *runEndRecorder) call(data any) {
	*r.order = append(*r.order, rhCbMark)
	r.calls++
	r.data = data
	r.state = r.item.State()

	if r.q.mutex.TryLock() {
		r.queueFree = true
		r.q.mutex.Unlock()
	}
}

// shouldHaveSeenRunningItemLocked asserts the callback was called once, with
// the item's data, while the item was still in the run sub-queue and the queue
// was locked.
func (r *runEndRecorder) shouldHaveSeenRunningItemLocked() {
	So(r.calls, ShouldEqual, 1)
	So(r.data, ShouldEqual, rhData)
	So(r.state, ShouldEqual, ItemStateRun)
	So(r.queueFree, ShouldBeFalse)
}

// TestReleaseWith proves ReleaseWith, RequeueWith and BuryWith call their
// callback once, with the item's data, under the queue lock while the item is
// still in the run sub-queue and before the ReadyHook, and never when the call
// fails.
func TestReleaseWith(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with a hook appending to a shared slice and a reserved item", t, func() {
		q := New(ctx, "release with queue")
		defer qdestroy(q)

		var order []string

		hookCalls := 0

		q.SetReadyHook(func(_ any, reserveGroup string) string {
			order = append(order, rhHookMark)
			hookCalls++

			return reserveGroup
		})

		_, err := q.Add(ctx, rhKey, rhGroup1, rhData, 0, 0, rhTTR, "")
		So(err, ShouldBeNil)

		item, err := q.Reserve(rhGroup1, 0)
		So(err, ShouldBeNil)

		cb := &runEndRecorder{q: q, item: item, order: &order}

		hookCalls = 0
		order = nil

		Convey("ReleaseWith with delay 0 calls the callback before the hook and the item is ready", func() {
			So(q.ReleaseWith(ctx, rhKey, cb.call), ShouldBeNil)

			So(order, ShouldResemble, []string{rhCbMark, rhHookMark})
			cb.shouldHaveSeenRunningItemLocked()
			So(stateOf(q), ShouldEqual, ItemStateReady)
		})

		Convey("ReleaseWith with a 1h delay calls the callback once, the item is delayed and the hook is "+
			"not called", func() {
			So(q.SetDelay(rhKey, time.Hour), ShouldBeNil)

			hookCalls = 0

			So(q.ReleaseWith(ctx, rhKey, cb.call), ShouldBeNil)

			So(cb.calls, ShouldEqual, 1)
			cb.shouldHaveSeenRunningItemLocked()
			So(stateOf(q), ShouldEqual, ItemStateDelay)
			So(hookCalls, ShouldEqual, 0)
		})

		Convey("RequeueWith with nil deps calls the callback before the hook and the item is ready", func() {
			So(q.RequeueWith(ctx, rhKey, nil, cb.call), ShouldBeNil)

			So(order, ShouldResemble, []string{rhCbMark, rhHookMark})
			cb.shouldHaveSeenRunningItemLocked()
			So(stateOf(q), ShouldEqual, ItemStateReady)
		})

		Convey("RequeueWith with deps calls the callback once, the item is dependent and the hook is not "+
			"called", func() {
			So(q.RequeueWith(ctx, rhKey, []string{rhParent}, cb.call), ShouldBeNil)

			So(cb.calls, ShouldEqual, 1)
			cb.shouldHaveSeenRunningItemLocked()
			So(stateOf(q), ShouldEqual, ItemStateDependent)
			So(hookCalls, ShouldEqual, 0)
		})

		Convey("BuryWith calls the callback once, the item is buried and the hook is not called", func() {
			So(q.BuryWith(rhKey, cb.call), ShouldBeNil)

			So(cb.calls, ShouldEqual, 1)
			cb.shouldHaveSeenRunningItemLocked()
			So(stateOf(q), ShouldEqual, ItemStateBury)
			So(hookCalls, ShouldEqual, 0)
		})

		Convey("Once the item is ready again", func() {
			So(q.Release(ctx, rhKey), ShouldBeNil)
			So(stateOf(q), ShouldEqual, ItemStateReady)

			for _, op := range runEndOps() {
				Convey(op.name+" fails with ErrNotRunning without calling the callback", func() {
					err := op.call(ctx, q, rhKey, cb.call)
					shouldBeQueueError(err, ErrNotRunning)
					So(cb.calls, ShouldEqual, 0)
					So(stateOf(q), ShouldEqual, ItemStateReady)
				})
			}
		})

		for _, op := range runEndOps() {
			Convey(op.name+" of a missing key fails with ErrNotFound without calling the callback", func() {
				err := op.call(ctx, q, "missing", cb.call)
				shouldBeQueueError(err, ErrNotFound)
				So(cb.calls, ShouldEqual, 0)
			})

			Convey(op.name+" on a destroyed queue fails with ErrQueueClosed without calling the callback", func() {
				So(q.Destroy(), ShouldBeNil)

				err := op.call(ctx, q, rhKey, cb.call)
				shouldBeQueueError(err, ErrQueueClosed)
				So(cb.calls, ShouldEqual, 0)
			})
		}
	})
}

func stateOf(q *Queue) ItemState {
	item, err := q.Get(rhKey)
	So(err, ShouldBeNil)

	return item.State()
}

func runEndOps() []runEndOp {
	return []runEndOp{
		{"ReleaseWith", func(ctx context.Context, q *Queue, key string, cb func(any)) error {
			return q.ReleaseWith(ctx, key, cb)
		}},
		{"RequeueWith", func(ctx context.Context, q *Queue, key string, cb func(any)) error {
			return q.RequeueWith(ctx, key, nil, cb)
		}},
		{"BuryWith", func(_ context.Context, q *Queue, key string, cb func(any)) error {
			return q.BuryWith(key, cb)
		}},
	}
}

// runEndOp is one of the With calls that ends an item's run.
type runEndOp struct {
	name string
	call func(ctx context.Context, q *Queue, key string, cb func(any)) error
}
