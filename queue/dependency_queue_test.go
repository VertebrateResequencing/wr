// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package queue

import (
	"fmt"
	. "github.com/smartystreets/goconvey/convey"
	"testing"
	"time"
)

func TestDependencyQueue(t *testing.T) {
	Convey("Once 10 items have been pushed to the queue", t, func() {
		queue := newDependencyQueue()
		items := make(map[string]*Item)
		for i := 0; i < 10; i++ {
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
		queue := newDependencyQueue()
		items := make(map[string]*Item)
		for i := 0; i < 1; i++ {
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
