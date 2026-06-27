/*******************************************************************************
 * Copyright (c) 2016-2018 Genome Research Ltd.
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
	"fmt"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// sliceQueue is the common behaviour of the simple slice-backed sub-queues
// (buryQueue and depQueue), used to share their identical test logic.
type sliceQueue interface {
	push(item *Item)
	pop() *Item
	remove(item *Item)
	len() int
	empty()
}

// testSliceQueue runs the shared push/pop/remove/empty behaviour checks against
// a slice-backed sub-queue created by newQueue.
func testSliceQueue(t *testing.T, newQueue func() sliceQueue) {
	t.Helper()

	Convey("Once 10 items have been pushed to the queue", t, func() {
		queue := newQueue()
		items := make(map[string]*Item)

		for i := range 10 {
			key := fmt.Sprintf("key_%d", i)
			items[key] = newItem(key, "", "data", 0, 0*time.Second, 0*time.Second)
			queue.push(items[key])
		}

		So(queue.len(), ShouldEqual, 10)

		Convey("Removing an item works", func() {
			removeItem := items["key_2"]
			queue.remove(removeItem)
			So(queue.len(), ShouldEqual, 9)

			for {
				item := queue.pop()
				if item == nil {
					break
				}

				So(item.Key, ShouldNotEqual, "key_2")
			}

			So(queue.len(), ShouldEqual, 0)
		})

		Convey("Changing an item works", func() {
			exampleItem := items["key_9"]
			exampleItem.Key = "newKey"
			newItem := queue.pop()
			So(newItem.Key, ShouldEqual, "newKey")
		})

		Convey("Removing all items works", func() {
			queue.empty()
			So(queue.len(), ShouldEqual, 0)
		})
	})

	Convey("Once a single item has been pushed to the queue", t, func() {
		queue := newQueue()
		items := make(map[string]*Item)

		for i := range 1 {
			key := fmt.Sprintf("key_%d", i)
			items[key] = newItem(key, "", "data", 0, 0*time.Second, 0*time.Second)
			queue.push(items[key])
		}

		So(queue.len(), ShouldEqual, 1)

		Convey("Removing the item works", func() {
			removeItem := items["key_0"]
			queue.remove(removeItem)
			So(queue.len(), ShouldEqual, 0)
		})
	})
}

func TestBuryQueue(t *testing.T) {
	testSliceQueue(t, func() sliceQueue { return newBuryQueue() })
}
