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

func TestDelayQueue(t *testing.T) {
	Convey("Once 10 items of differing delay have been pushed to the queue", t, func() {
		queue := newSubQueue(0)
		items := make(map[string]*Item)

		for i := range 10 {
			key := fmt.Sprintf("key_%d", i)
			delay := time.Duration((9 - i + 1)) * time.Second
			items[key] = newItem(key, "", "data", 0, delay, 0*time.Second)
			queue.push(items[key])
		}

		So(queue.Len(), ShouldEqual, 10)

		Convey("Popping them should remove them in delay order", func() {
			exampleItem := items["key_1"]

			for i := range 5 {
				item := queue.pop()
				So(item, ShouldHaveSameTypeAs, exampleItem)
				So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", 9-i))
			}

			So(queue.Len(), ShouldEqual, 5)

			for i := range 5 {
				item := queue.pop()
				So(item, ShouldHaveSameTypeAs, exampleItem)
				So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", 9-i-5))
			}

			So(queue.Len(), ShouldEqual, 0)

			item := queue.pop()
			So(item, ShouldBeNil)
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
			exampleItem.readyAt = time.Now().Add(2500 * time.Millisecond)
			queue.update(exampleItem)
			newItem := queue.pop()
			So(newItem.Key, ShouldEqual, "key_8")
		})

		Convey("Removing all items works", func() {
			queue.empty()
			So(queue.Len(), ShouldEqual, 0)
		})
	})
}
