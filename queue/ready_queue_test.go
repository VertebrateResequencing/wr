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

func TestReadyQueue(t *testing.T) {
	Convey("Once 10 items of equal priority have been pushed to the queue", t, func() {
		queue := newSubQueue(1)
		items := make(map[string]*Item)
		for i := range 10 {
			key := fmt.Sprintf("key_%d", i)
			items[key] = newItem(key, "", "data", 0, 0*time.Second, 0*time.Second)
			queue.push(items[key])
		}

		So(queue.Len(), ShouldEqual, 10)

		Convey("Popping them should remove them in fifo order", func() {
			exampleItem := items["key_1"]

			for i := range 5 {
				item := queue.pop()
				So(item, ShouldHaveSameTypeAs, exampleItem)
				So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", i))
			}
			So(queue.Len(), ShouldEqual, 5)
			for i := range 5 {
				item := queue.pop()
				So(item, ShouldHaveSameTypeAs, exampleItem)
				So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", i+5))
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
			exampleItem := items["key_5"]
			exampleItem.priority = 1
			queue.update(exampleItem)
			newItem := queue.pop()
			So(newItem.Key, ShouldEqual, "key_5")
		})

		Convey("Removing all items works", func() {
			queue.empty()
			So(queue.Len(), ShouldEqual, 0)
		})
	})

	Convey("Once 10 items of differing priority have been pushed to the queue", t, func() {
		queue := newSubQueue(1)
		items := make(map[string]*Item)
		for i := range 10 {
			key := fmt.Sprintf("key_%d", i)
			p := i
			if i == 4 {
				p = 5
			}
			items[key] = newItem(key, "", "data", uint8(p), 0*time.Second, 0*time.Second)
			queue.push(items[key])
		}

		So(queue.Len(), ShouldEqual, 10)

		Convey("Popping them should remove them in priority and then fifo order", func() {
			for i := range 10 {
				item := queue.pop()
				p := 9 - i
				if i == 4 {
					p--
				} else if i == 5 {
					p++
				}
				So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", p))
			}
			So(queue.Len(), ShouldEqual, 0)
		})
	})

	Convey("Once 10 items of equal priority and 2 different ReserveGroups have been pushed to the queue", t, func() {
		queue := newSubQueue(1)
		items := make(map[string]*Item)
		for i := range 5 {
			key := fmt.Sprintf("key_%d", i)
			items[key] = newItem(key, "group1", "data", 0, 0*time.Second, 0*time.Second)
			queue.push(items[key])
		}
		for i := 5; i < 10; i++ {
			key := fmt.Sprintf("key_%d", i)
			items[key] = newItem(key, "group2", "data", 0, 0*time.Second, 0*time.Second)
			queue.push(items[key])
		}

		So(queue.len(), ShouldEqual, 10)

		Convey("Pop on an unspecified group does nothing", func() {
			item := queue.pop()
			So(item, ShouldBeNil)
		})

		Convey("Popping from a given group should remove them in fifo order", func() {
			exampleItem := items["key_1"]

			So(queue.len("group1"), ShouldEqual, 5)
			for i := range 5 {
				item := queue.pop("group1")
				So(item, ShouldHaveSameTypeAs, exampleItem)
				So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", i))
			}
			So(queue.len("group1"), ShouldEqual, 0)
			item := queue.pop("group1")
			So(item, ShouldBeNil)

			So(queue.len("group2"), ShouldEqual, 5)
			for i := range 5 {
				itemp := queue.pop("group2")
				So(itemp, ShouldHaveSameTypeAs, exampleItem)
				So(itemp.Key, ShouldEqual, fmt.Sprintf("key_%d", i+5))
			}
			So(queue.len("group2"), ShouldEqual, 0)

			item = queue.pop("group2")
			So(item, ShouldBeNil)
		})

		Convey("Removing an item works", func() {
			removeItem := items["key_2"]
			queue.remove(removeItem)
			So(queue.len(), ShouldEqual, 9)

			for {
				item := queue.pop("group1")
				if item == nil {
					break
				}
				So(item.Key, ShouldNotEqual, "key_2")
			}
			So(queue.len("group1"), ShouldEqual, 0)
		})

		Convey("Updating an item works", func() {
			exampleItem := items["key_0"]
			exampleItem.ReserveGroup = "group2"
			queue.update(exampleItem, "group1")
			newItem := queue.pop("group2")
			So(newItem.Key, ShouldEqual, "key_0")
		})

		Convey("Removing all items works", func() {
			queue.empty()
			So(queue.len(), ShouldEqual, 0)
		})
	})
}

// func BenchmarkReadyQueue(b *testing.B) {
//     readyQueue := newReadyQueue()
//     b.ResetTimer()
//     k := 1
//     for i := 0; i < b.N; i++ {
//         k++
//         p := uint8(rand.Intn(255))
//         item := newItem(fmt.Sprintf("%d.%d", k, p), "data", p, 0*time.Second, 0*time.Second)
//         readyQueue.push(item)
//     }
// }
