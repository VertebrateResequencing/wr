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
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	retentionItems       = 200
	retentionPayloadSize = 1 << 10
	retentionGCRounds    = 100
	retentionGCPause     = 10 * time.Millisecond
)

// retentionPayload stands in for a job: item data big enough to matter.
type retentionPayload struct {
	cmd []byte
}

// newTrackedPayload returns item data whose collection by the GC increments
// collected.
func newTrackedPayload(collected *atomic.Int64) *retentionPayload {
	p := &retentionPayload{cmd: make([]byte, retentionPayloadSize)}
	runtime.AddCleanup(p, func(c *atomic.Int64) { c.Add(1) }, collected)

	return p
}

func TestSliceQueuePopsLeaveItemsCollectable(t *testing.T) {
	queues := map[string]func() sliceQueue{
		"bury":       func() sliceQueue { return newBuryQueue() },
		"dependency": func() sliceQueue { return newDependencyQueue() },
		"suspended":  func() sliceQueue { return newSuspendedQueue() },
	}

	for name, newQueue := range queues {
		Convey("Items popped from the "+name+" sub-queue can be collected", t, func() {
			sq := newQueue()
			collected := &atomic.Int64{}

			for i := range retentionItems {
				key := retentionKey(i)
				sq.push(newItem(key, "", newTrackedPayload(collected), 0, 0, 0))
			}

			popped := 0

			for sq.pop() != nil {
				popped++
			}

			So(popped, ShouldEqual, retentionItems)
			So(collectedAfterGC(collected, retentionItems), ShouldEqual, retentionItems)

			runtime.KeepAlive(sq)
		})
	}
}

// retentionKey returns the key of the i-th item added by a retention test.
func retentionKey(i int) string {
	return fmt.Sprintf("k%d", i)
}

// collectedAfterGC runs the GC until want payloads have been collected or it
// gives up, and returns how many were collected.
func collectedAfterGC(collected *atomic.Int64, want int64) int64 {
	for range retentionGCRounds {
		runtime.GC()

		if collected.Load() >= want {
			break
		}

		time.Sleep(retentionGCPause)
	}

	return collected.Load()
}

// addTrackedItems adds retentionItems items to q in the given reserve group and
// start sub-queue, with optional dependencies, and returns the counter their
// data's collection increments.
func addTrackedItems(ctx context.Context, q *Queue, group string, delay time.Duration, start SubQueue,
	deps ...[]string,
) *atomic.Int64 {
	collected := &atomic.Int64{}

	So(forEachKey(func(key string) error {
		_, err := q.Add(ctx, key, group, newTrackedPayload(collected), 0, delay, time.Hour, start, deps...)

		return err
	}), ShouldEqual, 0)

	return collected
}

// forEachKey runs fn on the key of every item added by addTrackedItems, and
// returns how many calls failed.
func forEachKey(fn func(key string) error) int {
	failures := 0

	for i := range retentionItems {
		if err := fn(retentionKey(i)); err != nil {
			failures++
		}
	}

	return failures
}

func TestRemovedItemsAreCollectable(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue", t, func() {
		q := New(ctx, "retention")

		defer func() {
			So(q.Destroy(), ShouldBeNil)
		}()

		assertAllCollected := func(collected *atomic.Int64) {
			So(q.Stats().Items, ShouldEqual, 0)
			So(collectedAfterGC(collected, retentionItems), ShouldEqual, retentionItems)
			So(readyGroupCount(q), ShouldEqual, 0)
		}

		Convey("items removed straight from the ready sub-queue can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueReady)

			So(forEachKey(func(key string) error { return q.Remove(ctx, key) }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("items reserved and then removed can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueReady)

			So(forEachKey(func(string) error { return reserveAndRemove(ctx, q, "grp") }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("items reserved from a group that keeps a live item can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueReady)

			_, err := q.Add(ctx, "live", "grp", "live", 0, 0, time.Hour, SubQueueReady)
			So(err, ShouldBeNil)

			So(forEachKey(func(string) error { return reserveAndRemove(ctx, q, "grp") }), ShouldEqual, 0)
			So(collectedAfterGC(collected, retentionItems), ShouldEqual, retentionItems)
			So(q.Stats().Ready, ShouldEqual, 1)
		})

		Convey("items removed from the delay sub-queue can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", time.Hour, SubQueueReady)
			So(q.Stats().Delayed, ShouldEqual, retentionItems)

			So(forEachKey(func(key string) error { return q.Remove(ctx, key) }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("items reserved, released to the delay sub-queue and removed can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueReady)

			So(forEachKey(func(string) error {
				item, err := q.Reserve("grp", 0)
				if err != nil {
					return err
				}

				if err = q.SetDelay(item.Key, time.Hour); err != nil {
					return err
				}

				return q.Release(ctx, item.Key)
			}), ShouldEqual, 0)
			So(q.Stats().Delayed, ShouldEqual, retentionItems)

			So(forEachKey(func(key string) error { return q.Remove(ctx, key) }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("items reserved, buried and removed can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueReady)

			So(forEachKey(func(string) error {
				item, err := q.Reserve("grp", 0)
				if err != nil {
					return err
				}

				return q.Bury(item.Key)
			}), ShouldEqual, 0)
			So(q.Stats().Buried, ShouldEqual, retentionItems)

			So(forEachKey(func(key string) error { return q.Remove(ctx, key) }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("items buried, kicked, reserved and removed can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueBury)

			So(forEachKey(func(key string) error { return q.Kick(ctx, key) }), ShouldEqual, 0)
			So(forEachKey(func(string) error { return reserveAndRemove(ctx, q, "grp") }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("dependent items that are removed can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueReady, []string{"absent-parent"})
			So(q.Stats().Dependant, ShouldEqual, retentionItems)

			So(forEachKey(func(key string) error { return q.Remove(ctx, key) }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("suspended items that are removed can be collected", func() {
			collected := addTrackedItems(ctx, q, "grp", 0, SubQueueReady)

			So(forEachKey(func(key string) error { return q.Suspend(ctx, key) }), ShouldEqual, 0)
			So(q.Stats().Suspended, ShouldEqual, retentionItems)

			So(forEachKey(func(key string) error { return q.Remove(ctx, key) }), ShouldEqual, 0)
			assertAllCollected(collected)
		})

		Convey("items moved to a new reserve group, reserved and removed can be collected", func() {
			collected := addTrackedItems(ctx, q, "old", 0, SubQueueReady)

			So(forEachKey(func(key string) error {
				item, err := q.Get(key)
				if err != nil {
					return err
				}

				return q.Update(ctx, key, "new", item.Data(), 0, 0, time.Hour)
			}), ShouldEqual, 0)
			So(readyGroupCount(q), ShouldEqual, 1)

			So(forEachKey(func(string) error { return reserveAndRemove(ctx, q, "new") }), ShouldEqual, 0)
			assertAllCollected(collected)
		})
	})
}

func TestEmptiedReadyGroupsAreDropped(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue that has seen many reserve groups come and go", t, func() {
		q := New(ctx, "groups")

		defer func() {
			So(q.Destroy(), ShouldBeNil)
		}()

		const groups = 50

		failures := 0

		for g := range groups {
			group := fmt.Sprintf("grp%d", g)

			if _, err := q.Add(ctx, group, group, "data", 0, 0, time.Hour, SubQueueReady); err != nil {
				failures++
			}

			if err := reserveAndRemove(ctx, q, group); err != nil {
				failures++
			}
		}

		So(failures, ShouldEqual, 0)

		Convey("the ready sub-queue holds nothing for them", func() {
			So(readyGroupCount(q), ShouldEqual, 0)
		})

		Convey("a dropped group can be used again", func() {
			_, err := q.Add(ctx, "again", "grp0", "data", 0, 0, time.Hour, SubQueueReady)
			So(err, ShouldBeNil)
			So(q.readyQueue.len("grp0"), ShouldEqual, 1)

			item, err := q.Reserve("grp0", 0)
			So(err, ShouldBeNil)
			So(item.Key, ShouldEqual, "again")
			So(readyGroupCount(q), ShouldEqual, 0)
		})

		Convey("a reserve that waits on a dropped group still gets the next item added to it", func() {
			got := make(chan *Item, 1)

			go func() {
				item, err := q.Reserve("grp1", 5*time.Second)
				if err != nil {
					got <- nil

					return
				}

				got <- item
			}()

			<-time.After(50 * time.Millisecond)

			_, err := q.Add(ctx, "late", "grp1", "data", 0, 0, time.Hour, SubQueueReady)
			So(err, ShouldBeNil)

			item := <-got
			So(item, ShouldNotBeNil)
			So(item.Key, ShouldEqual, "late")
		})
	})
}

// reserveAndRemove reserves the next item in group and removes it.
func reserveAndRemove(ctx context.Context, q *Queue, group string) error {
	item, err := q.Reserve(group, 0)
	if err != nil {
		return err
	}

	return q.Remove(ctx, item.Key)
}

// readyGroupCount returns how many reserve groups the ready sub-queue holds a
// slice for.
func readyGroupCount(q *Queue) int {
	q.readyQueue.mutex.RLock()
	defer q.readyQueue.mutex.RUnlock()

	return len(q.readyQueue.groupedItems)
}
