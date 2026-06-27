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

func TestRunQueue(t *testing.T) {
	Convey("Once 10 items of differing ttr have been pushed to the queue", t, func() {
		queue := newSubQueue(2)
		items := make(map[string]*Item)

		for i := range 10 {
			key := fmt.Sprintf("key_%d", i)
			ttr := time.Duration((9 - i + 1)) * time.Second
			items[key] = newItem(key, "", "data", 0, 0*time.Second, ttr)
			items[key].touch()
			queue.push(items[key])
		}

		So(queue.Len(), ShouldEqual, 10)

		Convey("Popping them should remove them in ttr order", func() {
			assertPopsInOrder(queue, items)
		})

		Convey("Removing an item works", func() {
			removeItem := items["key_2"]
			queue.remove(removeItem)
			So(queue.Len(), ShouldEqual, 9)

			for {
				item := queue.pop()
				if item == nil {
					break
				}

				So(item.Key, ShouldNotEqual, "key_2")
			}

			So(queue.Len(), ShouldEqual, 0)
		})

		Convey("Updating an item works", func() {
			exampleItem := items["key_9"]
			exampleItem.releaseAt = time.Now().Add(2500 * time.Millisecond)
			queue.update(exampleItem)
			newItem := queue.pop()
			So(newItem.Key, ShouldEqual, "key_8")
		})

		Convey("Removing all items works", func() {
			queue.empty()
			So(queue.Len(), ShouldEqual, 0)
		})

		Convey("Getting the next item that would be popped without actually popping it works", func() {
			item := queue.items[0]
			So(item.Key, ShouldEqual, "key_9")
			So(queue.Len(), ShouldEqual, 10)

			queue := newSubQueue(2)

			for i := range 10 {
				key := fmt.Sprintf("key_%d", i)
				ttr := time.Duration((9 - i + 1)) * time.Second
				item := newItem(key, "", "data", 0, 0*time.Second, ttr)
				item.touch()
				queue.push(item)

				item = queue.items[0]
				So(item.Key, ShouldEqual, key)
				So(queue.Len(), ShouldEqual, i+1)
			}

			queue = newSubQueue(2)

			for i := range 10 {
				key := fmt.Sprintf("key_%d", i)
				ttr := time.Duration(i+1) * time.Second
				item := newItem(key, "", "data", 0, 0*time.Second, ttr)
				item.touch()
				queue.push(item)

				item = queue.items[0]
				So(item.Key, ShouldEqual, "key_0")
				So(queue.Len(), ShouldEqual, i+1)
			}
		})
	})
}
