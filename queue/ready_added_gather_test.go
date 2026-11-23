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
	"runtime"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReadyAddedGatherLetsReservesThrough covers a reserve made while a
// ready-added callback's ready item data is being gathered: the gather reads
// every ready item's data, so if it held the queue's lock while doing so, every
// reserve would wait for it, longer the more items are ready.
func TestReadyAddedGatherLetsReservesThrough(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with two ready items, one of whose data is busy", t, func() {
		q := New(ctx, "ready_added_gather")

		// while busy's write lock is held, busy.Data() waits. free releases it
		// before the queue is destroyed, as Destroy waits for the gather's read
		// lock of the queue.
		var busy *Item

		locked := false
		free := func() {
			if locked {
				locked = false

				busy.mutex.Unlock()
			}
		}

		Reset(func() {
			free()
			So(q.Destroy(), ShouldBeNil)
		})

		// the busy item is in a reserve group of its own, so the reserve below
		// does not take it.
		_, err := q.Add(ctx, key1, "busy", key1+" data", 0, 0, time.Hour, "")
		So(err, ShouldBeNil)

		_, err = q.Add(ctx, key2, "", key2+" data", 0, 0, time.Hour, "")
		So(err, ShouldBeNil)

		busy, err = q.Get(key1)
		So(err, ShouldBeNil)

		busy.mutex.Lock()

		locked = true

		type gatheredData struct {
			data []any
			left []any
		}

		gathered := make(chan gatheredData, 1)

		q.SetReadyAddedCallback(func(_ string, data []any) {
			_, left := q.GetRunningDataAndLeftReady()
			gathered <- gatheredData{data: data, left: left}
		})
		q.TriggerReadyAddedCallback(ctx)

		Convey("a reserve made while the gather waits on that data is not held up by it", func() {
			So(pollQueue(gatheringReadyItemData), ShouldBeTrue)

			reserved := make(chan error, 1)

			go func() {
				item, errr := q.Reserve("", 0)
				if errr == nil && item.Key != key2 {
					errr = ErrNotFound
				}

				reserved <- errr
			}()

			var reserveErr error

			select {
			case reserveErr = <-reserved:
			case <-time.After(5 * time.Second):
				free()

				select {
				case <-reserved:
				case <-time.After(5 * time.Second):
				}

				So("reserve waited for the gather", ShouldBeEmpty)
			}

			So(reserveErr, ShouldBeNil)

			free()

			select {
			case g := <-gathered:
				So(g.data, ShouldHaveLength, 2)
				So(g.data, ShouldContain, key1+" data")
				So(g.data, ShouldContain, key2+" data")
				So(g.left, ShouldResemble, []any{key2 + " data"})
			case <-time.After(5 * time.Second):
				So("the ready-added callback was not called", ShouldBeEmpty)
			}
		})
	})
}

// gatheringReadyItemData reports whether a ready-added callback goroutine is
// reading an item's data.
func gatheringReadyItemData() bool {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)

	for g := range strings.SplitSeq(string(buf[:n]), "\n\n") {
		if strings.Contains(g, ".runReadyAddedCb(") && strings.Contains(g, ".(*Item).Data(") {
			return true
		}
	}

	return false
}
