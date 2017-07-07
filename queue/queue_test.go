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
	"math/rand"
	"sort"
	"testing"
	"time"
)

type changedStruct struct {
	from  SubQueue
	to    SubQueue
	count int
}

func TestQueue(t *testing.T) {
	Convey("Once 10 items of differing delay and ttr have been added to the queue", t, func() {
		queue := New("myqueue")

		readyAddedTestEnable := false
		readyAddedChan := make(chan int, 1)
		queue.SetReadyAddedCallback(func(queuename string, allitemdata []interface{}) {
			if readyAddedTestEnable {
				readyAddedChan <- len(allitemdata)
			}
		})

		changedTestEnable := false
		changedChan := make(chan *changedStruct, 1)
		queue.SetChangedCallback(func(from, to SubQueue, data []interface{}) {
			if changedTestEnable {
				changedChan <- &changedStruct{from, to, len(data)}
			}
		})

		items := make(map[string]*Item)
		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("key_%d", i)
			t := time.Duration((i+1)*100) * time.Millisecond
			item, err := queue.Add(key, "", "data", 0, t, t)
			So(err, ShouldBeNil)
			items[key] = item
		}

		stats := queue.Stats()
		So(stats.Items, ShouldEqual, 10)
		So(stats.Delayed, ShouldEqual, 10)
		So(stats.Ready, ShouldEqual, 0)
		So(stats.Running, ShouldEqual, 0)
		So(stats.Buried, ShouldEqual, 0)

		Convey("You can get an item back out", func() {
			item, err := queue.Get("key_0")
			So(err, ShouldBeNil)
			So(item.Data, ShouldEqual, "data")
			So(item.creation, ShouldHappenOnOrBefore, items["key_0"].creation)
		})

		Convey("You can't get an non-existent item", func() {
			item, err := queue.Get("key_fake")
			So(err, ShouldNotBeNil)
			So(item, ShouldBeNil)
			qerr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotFound)
		})

		Convey("You can get all items back out", func() {
			items := queue.AllItems()
			So(len(items), ShouldEqual, 10)
		})

		Convey("When nothing is running, GetRunningData returns nothing", func() {
			data := queue.GetRunningData()
			So(len(data), ShouldEqual, 0)
		})

		Convey("You can't add the same item again", func() {
			item, err := queue.Add("key_0", "", "data new", 0, 100*time.Millisecond, 100*time.Millisecond)
			qerr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrAlreadyExists)
			So(err.Error(), ShouldEqual, "queue(myqueue) Add(key_0): already exists")
			So(item.Data, ShouldEqual, "data")
			So(item.creation, ShouldHappenOnOrBefore, items["key_0"].creation)
		})

		Convey("They should start delayed and gradually become ready", func() {
			<-time.After(110 * time.Millisecond)
			stats = queue.Stats()
			So(stats.Delayed, ShouldEqual, 9)
			So(stats.Ready, ShouldEqual, 1)
			<-time.After(110 * time.Millisecond)
			stats = queue.Stats()
			So(stats.Delayed, ShouldEqual, 8)
			So(stats.Ready, ShouldEqual, 2)
			readyAddedTestEnable = true
			changedTestEnable = true
			<-time.After(110 * time.Millisecond)
			stats = queue.Stats()
			So(stats.Delayed, ShouldEqual, 7)
			So(stats.Ready, ShouldEqual, 3)
			So(<-readyAddedChan, ShouldEqual, 3)
			So(checkChanged(changedChan, SubQueueDelay, SubQueueReady, 1), ShouldBeTrue)
			readyAddedTestEnable = false
			changedTestEnable = false

			readyAddedTestEnable = true
			queue.TriggerReadyAddedCallback()
			So(<-readyAddedChan, ShouldEqual, 3)
			readyAddedTestEnable = false

			Convey("Once ready you should be able to reserve them in the expected order", func() {
				item1, err := queue.Reserve()
				So(err, ShouldBeNil)
				So(item1, ShouldNotBeNil)
				So(item1.Key, ShouldEqual, "key_0")
				So(item1.reserves, ShouldEqual, 1)
				item2, err := queue.Reserve()
				So(err, ShouldBeNil)
				So(item2, ShouldNotBeNil)
				So(item2.Key, ShouldEqual, "key_1")
				changedTestEnable = true
				item3, err := queue.Reserve()
				So(err, ShouldBeNil)
				So(item3, ShouldNotBeNil)
				So(item3.Key, ShouldEqual, "key_2")
				So(checkChanged(changedChan, SubQueueReady, SubQueueRun, 1), ShouldBeTrue)
				changedTestEnable = false
				item4, err := queue.Reserve()
				So(err, ShouldNotBeNil)
				So(item4, ShouldBeNil)
				qerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNothingReady)

				stats = queue.Stats()
				So(stats.Items, ShouldEqual, 10)
				So(stats.Delayed, ShouldEqual, 7)
				So(stats.Ready, ShouldEqual, 0)
				So(stats.Running, ShouldEqual, 3)

				Convey("Once reserved, you can get the data with GetRunningData", func() {
					data := queue.GetRunningData()
					So(len(data), ShouldEqual, 3)
				})

				Convey("Once reserved you can release them", func() {
					So(item1.state, ShouldEqual, ItemStateRun)
					So(item1.releases, ShouldEqual, 0)
					changedTestEnable = true
					err := queue.Release(item1.Key)
					So(err, ShouldBeNil)
					So(item1.state, ShouldEqual, ItemStateDelay)
					So(item1.releases, ShouldEqual, 1)
					So(checkChanged(changedChan, SubQueueRun, SubQueueDelay, 1), ShouldBeTrue)
					changedTestEnable = false

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 10)
					So(stats.Delayed, ShouldEqual, 8)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 2)

					Convey("You can get read-only item stats at any point", func() {
						itemstats := item1.Stats()
						So(itemstats.State, ShouldEqual, ItemStateDelay)
						So(itemstats.Releases, ShouldEqual, 1)
					})

					Convey("Once released, items become ready after their delay", func() {
						<-time.After(50 * time.Millisecond)
						So(item1.state, ShouldEqual, ItemStateDelay)
						changedTestEnable = true
						<-time.After(60 * time.Millisecond)
						So(item1.state, ShouldEqual, ItemStateReady)
						stats = queue.Stats()
						So(stats.Delayed, ShouldEqual, 6)
						So(stats.Ready, ShouldEqual, 2)
						So(checkChanged(changedChan, SubQueueDelay, SubQueueReady, 1), ShouldBeTrue)
						changedTestEnable = false

						Convey("Once reserved, the delay can be altered and this affects the next release", func() {
							item1, err := queue.Reserve()
							So(err, ShouldBeNil)
							So(item1, ShouldNotBeNil)
							So(item1.Key, ShouldEqual, "key_0")

							err = queue.SetDelay(item1.Key, 20*time.Millisecond)
							So(err, ShouldBeNil)
							err = queue.Release(item1.Key)
							So(err, ShouldBeNil)
							So(item1.state, ShouldEqual, ItemStateDelay)
							So(item1.releases, ShouldEqual, 2)

							<-time.After(10 * time.Millisecond)
							So(item1.state, ShouldEqual, ItemStateDelay)
							<-time.After(20 * time.Millisecond)
							So(item1.state, ShouldEqual, ItemStateReady)
						})

						Convey("Once reserved and released, the delay can be altered", func() {
							item1, err := queue.Reserve()
							So(err, ShouldBeNil)
							So(item1, ShouldNotBeNil)
							So(item1.Key, ShouldEqual, "key_0")
							err = queue.Release(item1.Key)
							So(err, ShouldBeNil)
							So(item1.state, ShouldEqual, ItemStateDelay)
							So(item1.releases, ShouldEqual, 2)

							err = queue.SetDelay(item1.Key, 30*time.Millisecond)
							So(err, ShouldBeNil)

							<-time.After(20 * time.Millisecond)
							So(item1.state, ShouldEqual, ItemStateDelay)
							<-time.After(20 * time.Millisecond)
							So(item1.state, ShouldEqual, ItemStateReady)
						})
					})
				})

				Convey("Or remove them", func() {
					So(item2.state, ShouldEqual, ItemStateRun)
					changedTestEnable = true
					err := queue.Remove(item2.Key)
					So(err, ShouldBeNil)
					So(item2.state, ShouldEqual, ItemStateRemoved)
					So(checkChanged(changedChan, SubQueueRun, SubQueueRemoved, 1), ShouldBeTrue)
					changedTestEnable = false

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 9)
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 2)

					Convey("Releasing, touching, burying, kicking and updating fail after removal", func() {
						err := queue.Release(item2.Key)
						So(err, ShouldNotBeNil)
						qerr, ok := err.(Error)
						So(ok, ShouldBeTrue)
						So(qerr.Err, ShouldEqual, ErrNotFound)
						err = queue.Touch(item2.Key)
						So(err, ShouldNotBeNil)
						qerr, ok = err.(Error)
						So(ok, ShouldBeTrue)
						So(qerr.Err, ShouldEqual, ErrNotFound)
						err = queue.Bury(item2.Key)
						So(err, ShouldNotBeNil)
						qerr, ok = err.(Error)
						So(ok, ShouldBeTrue)
						So(qerr.Err, ShouldEqual, ErrNotFound)
						err = queue.Kick(item2.Key)
						So(err, ShouldNotBeNil)
						qerr, ok = err.(Error)
						So(ok, ShouldBeTrue)
						So(qerr.Err, ShouldEqual, ErrNotFound)
						err = queue.Update(item2.Key, "", item2.Data, 0, 0*time.Second, 0*time.Second)
						So(err, ShouldNotBeNil)
						qerr, ok = err.(Error)
						So(ok, ShouldBeTrue)
						So(qerr.Err, ShouldEqual, ErrNotFound)
						err = queue.SetDelay(item2.Key, 0*time.Second)
						So(err, ShouldNotBeNil)
						qerr, ok = err.(Error)
						So(ok, ShouldBeTrue)
						So(qerr.Err, ShouldEqual, ErrNotFound)
					})
				})

				Convey("Or bury them", func() {
					So(item3.state, ShouldEqual, ItemStateRun)
					So(item3.buries, ShouldEqual, 0)
					changedTestEnable = true
					err := queue.Bury(item3.Key)
					So(err, ShouldBeNil)
					So(item3.state, ShouldEqual, ItemStateBury)
					So(item3.buries, ShouldEqual, 1)
					So(checkChanged(changedChan, SubQueueRun, SubQueueBury, 1), ShouldBeTrue)
					changedTestEnable = false

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 10)
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 2)
					So(stats.Buried, ShouldEqual, 1)

					Convey("Once buried you can kick them", func() {
						So(item3.kicks, ShouldEqual, 0)
						changedTestEnable = true
						err := queue.Kick(item3.Key)
						So(err, ShouldBeNil)
						So(item3.state, ShouldEqual, ItemStateReady)
						So(item3.kicks, ShouldEqual, 1)
						So(checkChanged(changedChan, SubQueueBury, SubQueueReady, 1), ShouldBeTrue)
						changedTestEnable = false

						stats = queue.Stats()
						So(stats.Items, ShouldEqual, 10)
						So(stats.Delayed, ShouldEqual, 7)
						So(stats.Ready, ShouldEqual, 1)
						So(stats.Running, ShouldEqual, 2)
						So(stats.Buried, ShouldEqual, 0)
					})

					Convey("You can also remove them whilst buried", func() {
						changedTestEnable = true
						err := queue.Remove("key_2")
						So(err, ShouldBeNil)
						So(checkChanged(changedChan, SubQueueBury, SubQueueRemoved, 1), ShouldBeTrue)
						changedTestEnable = false

						stats := queue.Stats()
						So(stats.Items, ShouldEqual, 9)
						So(stats.Delayed, ShouldEqual, 7)
						So(stats.Ready, ShouldEqual, 0)
						So(stats.Running, ShouldEqual, 2)
						So(stats.Buried, ShouldEqual, 0)

						err = queue.Remove("key_2")
						So(err, ShouldNotBeNil)
						qerr, ok := err.(Error)
						So(ok, ShouldBeTrue)
						So(qerr.Err, ShouldEqual, ErrNotFound)
					})
				})

				Convey("If not buried you can't kick them", func() {
					err := queue.Kick(item3.Key)
					So(err, ShouldNotBeNil)
					qerr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrNotBuried)
					So(item3.state, ShouldEqual, ItemStateRun)
				})

				Convey("If you do nothing they get auto-released to the ready queue", func() {
					<-time.After(50 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateRun)
					changedTestEnable = true
					<-time.After(60 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateReady)
					So(item1.timeouts, ShouldEqual, 1)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 2)
					So(stats.Running, ShouldEqual, 2)
					So(checkChanged(changedChan, SubQueueRun, SubQueueReady, 1), ShouldBeTrue)
					changedTestEnable = false
					<-time.After(110 * time.Millisecond)
					So(item2.state, ShouldEqual, ItemStateReady)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 4)
					So(stats.Running, ShouldEqual, 1)
					<-time.After(110 * time.Millisecond)
					So(item3.state, ShouldEqual, ItemStateReady)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 6)
					So(stats.Running, ShouldEqual, 0)

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 10)
					So(stats.Delayed, ShouldEqual, 4)
				})

				Convey("When they hit their ttr you can choose to delay them", func() {
					queue.SetTTRCallback(func(data interface{}) SubQueue {
						return SubQueueDelay
					})
					defer queue.SetTTRCallback(defaultTTRCallback)

					<-time.After(50 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateRun)
					stats = queue.Stats()
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 3)
					changedTestEnable = true
					<-time.After(60 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateDelay)
					So(item1.timeouts, ShouldEqual, 1)
					stats = queue.Stats()
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 1)
					So(stats.Running, ShouldEqual, 2)
					So(checkChanged(changedChan, SubQueueRun, SubQueueDelay, 1), ShouldBeTrue)
					changedTestEnable = false
				})

				Convey("When they hit their ttr you can choose to bury them", func() {
					queue.SetTTRCallback(func(data interface{}) SubQueue {
						return SubQueueBury
					})
					defer queue.SetTTRCallback(defaultTTRCallback)

					<-time.After(50 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateRun)
					stats = queue.Stats()
					So(stats.Buried, ShouldEqual, 0)
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 3)
					changedTestEnable = true
					<-time.After(60 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateBury)
					So(item1.timeouts, ShouldEqual, 1)
					stats = queue.Stats()
					So(stats.Buried, ShouldEqual, 1)
					So(stats.Delayed, ShouldEqual, 6)
					So(stats.Ready, ShouldEqual, 1)
					So(stats.Running, ShouldEqual, 2)
					So(checkChanged(changedChan, SubQueueRun, SubQueueBury, 1), ShouldBeTrue)
					changedTestEnable = false
				})

				Convey("Though you can prevent auto-release by touching them", func() {
					<-time.After(50 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateRun)
					err := queue.Touch(item1.Key)
					So(err, ShouldBeNil)
					<-time.After(60 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateRun)
					So(item1.timeouts, ShouldEqual, 0)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 1)
					So(stats.Running, ShouldEqual, 3)
					<-time.After(45 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateReady)
					So(item1.timeouts, ShouldEqual, 1)
					stats = queue.Stats()
					// if the total elapsed time since the items were added to the queue goes over 500ms, we can get an extra 'Ready' item
					So(stats.Ready, ShouldBeBetweenOrEqual, 2, 3)
					So(stats.Running, ShouldEqual, 2)
					<-time.After(60 * time.Millisecond)
					So(item2.state, ShouldEqual, ItemStateReady)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 4)
					So(stats.Running, ShouldEqual, 1)

					err = queue.Touch("item_fake")
					So(err, ShouldNotBeNil)
					qerr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrNotFound)
				})

				Convey("Touching doesn't mess with the correct queue order", func() {
					queue := New("new queue")
					queue.Add("item1", "", "data", 0, 0*time.Millisecond, 50*time.Millisecond)
					queue.Add("item2", "", "data", 0, 0*time.Millisecond, 52*time.Millisecond)
					<-time.After(1 * time.Millisecond)
					item1, _ := queue.Reserve()
					So(item1.Key, ShouldEqual, "item1")
					So(item1.state, ShouldEqual, ItemStateRun)
					item2, _ := queue.Reserve()
					So(item2.Key, ShouldEqual, "item2")
					So(item2.state, ShouldEqual, ItemStateRun)

					<-time.After(25 * time.Millisecond)

					So(queue.runQueue.items[0].Key, ShouldEqual, "item1")
					queue.Touch(item1.Key)
					So(queue.runQueue.items[0].Key, ShouldEqual, "item2")

					<-time.After(30 * time.Millisecond)

					So(item1.state, ShouldEqual, ItemStateRun)
					So(item2.state, ShouldEqual, ItemStateReady)

					<-time.After(25 * time.Millisecond)
					So(item1.state, ShouldEqual, ItemStateReady)
				})

				Convey("Finally, you can destroy the queue, which doesn't let you do much else after", func() {
					err := queue.Destroy()
					So(err, ShouldBeNil)
					stats := queue.Stats()
					So(stats.Items, ShouldEqual, 0)
					So(stats.Delayed, ShouldEqual, 0)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 0)
					So(stats.Buried, ShouldEqual, 0)

					_, err = queue.Add("fake", "", "data", 0, 0*time.Second, 0*time.Second)
					So(err, ShouldNotBeNil)
					qerr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					_, err = queue.Get("fake")
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					err = queue.Update("fake", "", "data", 0, 0*time.Second, 0*time.Second)
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					_, err = queue.Reserve()
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					_, err = queue.Reserve("")
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					err = queue.Touch("fake")
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					err = queue.Release("fake")
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					err = queue.Bury("fake")
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					err = queue.Kick("fake")
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					err = queue.Remove("fake")
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
					err = queue.SetDelay("fake", 0*time.Second)
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)

					err = queue.Destroy()
					So(err, ShouldNotBeNil)
					qerr, ok = err.(Error)
					So(ok, ShouldBeTrue)
					So(qerr.Err, ShouldEqual, ErrQueueClosed)
				})
			})

			Convey("If not reserved you can't release, touch, bury or kick them", func() {
				err := queue.Release("key_0")
				So(err, ShouldNotBeNil)
				qerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotRunning)
				err = queue.Touch("key_0")
				So(err, ShouldNotBeNil)
				qerr, ok = err.(Error)
				So(ok, ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotRunning)
				err = queue.Bury("key_0")
				So(err, ShouldNotBeNil)
				qerr, ok = err.(Error)
				So(ok, ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotRunning)
				err = queue.Kick("key_0")
				So(err, ShouldNotBeNil)
				qerr, ok = err.(Error)
				So(ok, ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotBuried)
			})

			Convey("But you can remove them when ready", func() {
				err := queue.Remove("key_0")
				So(err, ShouldBeNil)

				stats := queue.Stats()
				So(stats.Items, ShouldEqual, 9)
				So(stats.Delayed, ShouldEqual, 7)
				So(stats.Ready, ShouldEqual, 2)
				So(stats.Running, ShouldEqual, 0)
				So(stats.Buried, ShouldEqual, 0)

				err = queue.Remove("key_0")
				So(err, ShouldNotBeNil)
				qerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotFound)
			})
		})

		Convey("If not ready you can't reserve them", func() {
			item, err := queue.Reserve()
			So(err, ShouldNotBeNil)
			So(item, ShouldBeNil)
			qerr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNothingReady)
		})

		Convey("But you can remove them when not ready", func() {
			err := queue.Remove("key_0")
			So(err, ShouldBeNil)

			stats := queue.Stats()
			So(stats.Items, ShouldEqual, 9)
			So(stats.Delayed, ShouldEqual, 9)
			So(stats.Ready, ShouldEqual, 0)
			So(stats.Running, ShouldEqual, 0)
			So(stats.Buried, ShouldEqual, 0)

			err = queue.Remove("key_0")
			So(err, ShouldNotBeNil)
			qerr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotFound)
		})
	})

	Convey("Once an item been added to the queue", t, func() {
		queue := New("myqueue")
		item, _ := queue.Add("item1", "", "data", 0, 50*time.Millisecond, 50*time.Millisecond)

		Convey("It can be removed from the queue immediately prior to it getting switched to the ready queue", func() {
			<-time.After(49 * time.Millisecond)
			queue.Remove("item1")
			<-time.After(6 * time.Millisecond)
			So(item.state, ShouldEqual, ItemStateRemoved)

			stats := queue.Stats()
			So(stats.Items, ShouldEqual, 0)
			So(stats.Delayed, ShouldEqual, 0)
			So(stats.Ready, ShouldEqual, 0)

			Convey("Once removed it can't be updated", func() {
				err := queue.Update("item1", "", "data", 0, 75*time.Millisecond, 50*time.Millisecond)
				So(err, ShouldNotBeNil)
				qerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotFound)
			})
		})

		Convey("The queue won't fall over if we manage to change the item's readyAt without updating the queue", func() {
			<-time.After(45 * time.Millisecond)
			item.readyAt = time.Now().Add(25 * time.Millisecond)
			<-time.After(10 * time.Millisecond)
			So(item.state, ShouldEqual, ItemStateDelay)
			<-time.After(25 * time.Millisecond)
			So(item.state, ShouldEqual, ItemStateReady)
		})

		Convey("The delay can be updated even in the delay queue", func() {
			<-time.After(25 * time.Millisecond)
			err := queue.Update("item1", "", "data", 0, 75*time.Millisecond, 50*time.Millisecond)
			So(err, ShouldBeNil)
			<-time.After(30 * time.Millisecond)
			So(item.state, ShouldEqual, ItemStateDelay)
			<-time.After(50 * time.Millisecond)
			So(item.state, ShouldEqual, ItemStateReady)

			Convey("When ready the priority can be updated", func() {
				err := queue.Update("item1", "", "data", 1, 75*time.Millisecond, 50*time.Millisecond)
				So(err, ShouldBeNil)
				So(item.priority, ShouldEqual, 1)
			})

			Convey("When ready the ReserveGroup can be changed with Update()", func() {
				err := queue.Update("item1", "newGroup", "data", 0, 75*time.Millisecond, 50*time.Millisecond)
				So(err, ShouldBeNil)
				So(item.ReserveGroup, ShouldEqual, "newGroup")
			})

			Convey("When ready the ReserveGroup can be changed with SetReserveGroup()", func() {
				gotItem, err := queue.Reserve("newGroup")
				So(err, ShouldNotBeNil)
				So(gotItem, ShouldBeNil)

				err = queue.SetReserveGroup("item1", "newGroup")
				So(err, ShouldBeNil)
				So(item.ReserveGroup, ShouldEqual, "newGroup")

				gotItem, err = queue.Reserve("newGroup")
				So(err, ShouldBeNil)
				So(gotItem, ShouldNotBeNil)
				So(gotItem.Key, ShouldEqual, "item1")
			})
		})

		Convey("Once reserved", func() {
			<-time.After(55 * time.Millisecond)
			queue.Reserve()

			Convey("It can be removed from the queue immediately prior to it getting switched to the ready queue", func() {
				<-time.After(49 * time.Millisecond)
				queue.Remove("item1")
				<-time.After(6 * time.Millisecond)
				So(item.state, ShouldEqual, ItemStateRemoved)

				stats := queue.Stats()
				So(stats.Items, ShouldEqual, 0)
				So(stats.Delayed, ShouldEqual, 0)
				So(stats.Ready, ShouldEqual, 0)
			})

			Convey("The queue won't fall over if we manage to change the item's releaseAt without updating the queue", func() {
				So(item.state, ShouldEqual, ItemStateRun)
				<-time.After(49 * time.Millisecond)
				// the state should still be run at this point, but due to
				// timing vagueries it might not be; be more forgiving to
				// following tests by testing against current state instead of
				// explicit 'run'
				currentState := item.state
				item.releaseAt = time.Now().Add(25 * time.Millisecond)
				<-time.After(6 * time.Millisecond)
				So(item.state, ShouldEqual, currentState)
				<-time.After(25 * time.Millisecond)
				So(item.state, ShouldEqual, ItemStateReady)
			})

			Convey("When running the ttr can be updated", func() {
				<-time.After(25 * time.Millisecond)
				err := queue.Update("item1", "", "data", 0, 50*time.Millisecond, 75*time.Millisecond)
				So(err, ShouldBeNil)
				<-time.After(30 * time.Millisecond)
				So(item.state, ShouldEqual, ItemStateRun)
				<-time.After(50 * time.Millisecond)
				So(item.state, ShouldEqual, ItemStateReady)
			})
		})
	})

	Convey("Once a thousand items with no delay have been added to the queue", t, func() {
		queue := New("1000 queue")
		type testdata struct {
			ID int
		}
		var dataids []int
		for i := 0; i < 1000; i++ {
			key := fmt.Sprintf("key_%d", i)
			dataid := rand.Intn(999)
			dataids = append(dataids, dataid)
			_, err := queue.Add(key, "", &testdata{ID: dataid}, 0, 0*time.Second, 30*time.Second)
			So(err, ShouldBeNil)
		}

		Convey("They are immediately all ready", func() {
			stats := queue.Stats()
			So(stats.Items, ShouldEqual, 1000)
			So(stats.Delayed, ShouldEqual, 0)
			So(stats.Ready, ShouldEqual, 1000)
			So(stats.Running, ShouldEqual, 0)
			So(stats.Buried, ShouldEqual, 0)

			Convey("And can all be reserved", func() {
				for i := 0; i < 1000; i++ {
					item, err := queue.Reserve()
					So(err, ShouldBeNil)
					So(item, ShouldNotBeNil)
					So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", i))
				}

				Convey("And when released are immediately ready", func() {
					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 1000)
					So(stats.Delayed, ShouldEqual, 0)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 1000)
					err := queue.Release("key_0")
					So(err, ShouldBeNil)
					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 1000)
					So(stats.Delayed, ShouldEqual, 0)
					So(stats.Ready, ShouldEqual, 1)
					So(stats.Running, ShouldEqual, 999)
				})
			})
		})

		Convey("You can change the group of items with SetReserveGroup()", func() {
			item, err := queue.Reserve("1001")
			So(err, ShouldNotBeNil)
			So(item, ShouldBeNil)

			err = queue.SetReserveGroup("key_1", "1001")
			So(err, ShouldBeNil)

			err = queue.SetReserveGroup("key_2", "1001")
			So(err, ShouldBeNil)

			item, err = queue.Reserve("1001")
			So(err, ShouldBeNil)
			So(item, ShouldNotBeNil)
			So(item.Key, ShouldEqual, "key_1")
			So(item.ReserveGroup, ShouldEqual, "1001")

			item, err = queue.Reserve("1001")
			So(err, ShouldBeNil)
			So(item, ShouldNotBeNil)
			So(item.Key, ShouldEqual, "key_2")
			So(item.ReserveGroup, ShouldEqual, "1001")

			item, err = queue.Reserve("1001")
			So(err, ShouldNotBeNil)
			So(item, ShouldBeNil)
		})
	})

	Convey("Once a thousand items with no delay and differing ReserveGroups have been added to the queue", t, func() {
		queue := New("1000 queue")
		type testdata struct {
			ID int
		}
		var dataids []int
		for i := 0; i < 1000; i++ {
			key := fmt.Sprintf("key_%d", i)
			dataid := rand.Intn(999)
			dataids = append(dataids, dataid)
			_, err := queue.Add(key, fmt.Sprintf("%d", dataid), &testdata{ID: dataid}, 0, 0*time.Second, 30*time.Second)
			So(err, ShouldBeNil)
		}

		Convey("They are immediately all ready", func() {
			stats := queue.Stats()
			So(stats.Items, ShouldEqual, 1000)
			So(stats.Delayed, ShouldEqual, 0)
			So(stats.Ready, ShouldEqual, 1000)
			So(stats.Running, ShouldEqual, 0)
			So(stats.Buried, ShouldEqual, 0)
		})

		Convey("They can be reserved by specifying a group", func() {
			item, err := queue.Reserve("1001")
			So(err, ShouldNotBeNil)
			So(item, ShouldBeNil)
			qerr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNothingReady)

			sort.Ints(dataids)
			for _, dataid := range dataids {
				item, err := queue.Reserve(fmt.Sprintf("%d", dataid))
				So(err, ShouldBeNil)
				So(item, ShouldNotBeNil)
				So(item.Data.(*testdata).ID, ShouldEqual, dataid)
			}
		})

		Convey("You can change a group with SetReserveGroup()", func() {
			item, err := queue.Reserve("1001")
			So(err, ShouldNotBeNil)
			So(item, ShouldBeNil)

			err = queue.SetReserveGroup("key_1", "1001")
			So(err, ShouldBeNil)

			item, err = queue.Reserve("1001")
			So(err, ShouldBeNil)
			So(item, ShouldNotBeNil)
			So(item.Key, ShouldEqual, "key_1")
			So(item.ReserveGroup, ShouldEqual, "1001")
		})
	})

	Convey("Once a thousand items with a small delay have been added to the queue", t, func() {
		queue := New("1000 queue")
		t := time.Now()
		for i := 0; i < 1000; i++ {
			key := fmt.Sprintf("key_%d", i)
			_, err := queue.Add(key, "", "data", 0, 100*time.Millisecond, 30*time.Second)
			So(err, ShouldBeNil)
		}
		e := time.Since(t)

		stats := queue.Stats()
		So(stats.Items, ShouldEqual, 1000)
		if e < 100*time.Millisecond {
			So(stats.Delayed, ShouldEqual, 1000)
			So(stats.Ready, ShouldEqual, 0)
		}
		So(stats.Running, ShouldEqual, 0)
		So(stats.Buried, ShouldEqual, 0)

		<-time.After(110 * time.Millisecond)

		Convey("They are all ready after that delay", func() {
			stats := queue.Stats()
			So(stats.Items, ShouldEqual, 1000)
			So(stats.Delayed, ShouldEqual, 0)
			So(stats.Ready, ShouldEqual, 1000)
			So(stats.Running, ShouldEqual, 0)
			So(stats.Buried, ShouldEqual, 0)

			Convey("And can all be reserved", func() {
				for i := 0; i < 1000; i++ {
					item, err := queue.Reserve()
					So(err, ShouldBeNil)
					So(item, ShouldNotBeNil)
					So(item.Key, ShouldEqual, fmt.Sprintf("key_%d", i))
				}
			})
		})
	})

	Convey("You can add many items to the queue in one go", t, func() {
		q := New("myqueue")

		item, err := q.Reserve("")
		So(err, ShouldNotBeNil)
		So(item, ShouldBeNil)
		qerr, ok := err.(Error)
		So(ok, ShouldBeTrue)
		So(qerr.Err, ShouldEqual, ErrNothingReady)

		var itemdefs []*ItemDef
		for i := 0; i < 10; i++ {
			itemdefs = append(itemdefs, &ItemDef{
				Key:      fmt.Sprintf("key_%d", i),
				Data:     "data",
				Priority: 0,
				Delay:    100 * time.Millisecond,
				TTR:      1 * time.Minute,
			})
		}

		added, dups, err := q.AddMany(itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 10)
		So(dups, ShouldEqual, 0)

		added, dups, err = q.AddMany(itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 0)
		So(dups, ShouldEqual, 10)

		for i := 10; i < 20; i++ {
			itemdefs = append(itemdefs, &ItemDef{
				Key:      fmt.Sprintf("key_%d", i),
				Data:     "data",
				Priority: 0,
				Delay:    50 * time.Millisecond,
				TTR:      1 * time.Minute,
			})
		}

		added, dups, err = q.AddMany(itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 10)
		So(dups, ShouldEqual, 10)

		itemdefs = append(itemdefs, &ItemDef{
			Key:      fmt.Sprintf("key_%d", 99),
			Data:     "data",
			Priority: 0,
			Delay:    0 * time.Millisecond,
			TTR:      1 * time.Minute,
		})
		added, dups, err = q.AddMany(itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(dups, ShouldEqual, 20)

		Convey("It doesn't work if the queue is closed", func() {
			q.Destroy()
			_, _, err = q.AddMany(itemdefs)
			So(err, ShouldNotBeNil)
			qerr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrQueueClosed)
		})
	})

	Convey("Once some items with dependencies have been added to the queue", t, func() {
		// https://i-msdn.sec.s-msft.com/dynimg/IC332764.gif
		queue := New("dep queue")
		_, err := queue.Add("key_1", "", "1", 0, 0*time.Second, 30*time.Second)
		So(err, ShouldBeNil)
		_, err = queue.Add("key_2", "", "2", 0, 0*time.Second, 30*time.Second)
		So(err, ShouldBeNil)
		_, err = queue.Add("key_3", "", "3", 0, 0*time.Second, 30*time.Second)
		So(err, ShouldBeNil)
		_, err = queue.Add("key_4", "", "4", 0, 0*time.Second, 30*time.Second, []string{"key_1"})
		So(err, ShouldBeNil)
		_, err = queue.Add("key_5", "five", "5", 0, 0*time.Second, 30*time.Second, []string{"key_1", "key_2", "key_3"})
		So(err, ShouldBeNil)
		_, err = queue.Add("key_6", "", "6", 0, 0*time.Second, 30*time.Second, []string{"key_3", "key_4"})
		So(err, ShouldBeNil)
		fivesixdep, err := queue.Add("key_7", "", "7", 0, 0*time.Second, 30*time.Second, []string{"key_5", "key_6"})
		So(err, ShouldBeNil)
		_, err = queue.Add("key_8", "", "8", 0, 0*time.Second, 30*time.Second, []string{"key_5"})
		So(err, ShouldBeNil)

		So(fivesixdep.Dependencies(), ShouldResemble, []string{"key_5", "key_6"})

		Convey("Only the non-dependent items are immediately ready", func() {
			depTestFunc(queue)
		})

		Convey("HasDependents works", func() {
			hasDeps, err := queue.HasDependents("key_8")
			So(err, ShouldBeNil)
			So(hasDeps, ShouldBeFalse)
			hasDeps, err = queue.HasDependents("key_2")
			So(err, ShouldBeNil)
			So(hasDeps, ShouldBeTrue)

			err = queue.Remove("key_5")
			So(err, ShouldBeNil)

			hasDeps, err = queue.HasDependents("key_2")
			So(err, ShouldBeNil)
			So(hasDeps, ShouldBeFalse)
		})

		Convey("You can update dependencies", func() {
			four, err := queue.Get("key_4")
			So(err, ShouldBeNil)
			fourStats := four.Stats()
			So(four.Dependencies(), ShouldResemble, []string{"key_1"})
			So(fourStats.State, ShouldEqual, ItemStateDependent)
			hasDeps, err := queue.HasDependents("key_1")
			So(err, ShouldBeNil)
			So(hasDeps, ShouldBeTrue)

			err = queue.Update("key_4", "", four.Data, fourStats.Priority, fourStats.Delay, fourStats.TTR, []string{})
			So(err, ShouldBeNil)

			So(four.Dependencies(), ShouldResemble, []string{})
			So(four.Stats().State, ShouldEqual, ItemStateReady)
			hasDeps, err = queue.HasDependents("key_1")
			So(err, ShouldBeNil)
			So(hasDeps, ShouldBeTrue)

			five, err := queue.Get("key_5")
			So(err, ShouldBeNil)
			fiveStats := five.Stats()
			So(five.Dependencies(), ShouldResemble, []string{"key_1", "key_2", "key_3"})
			So(fiveStats.State, ShouldEqual, ItemStateDependent)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_2", "key_3"})
			So(err, ShouldBeNil)

			So(five.Dependencies(), ShouldResemble, []string{"key_2", "key_3"})
			hasDeps, err = queue.HasDependents("key_1")
			So(err, ShouldBeNil)
			So(hasDeps, ShouldBeFalse)

			err = queue.Remove("key_2")
			So(err, ShouldBeNil)
			err = queue.Remove("key_3")
			So(err, ShouldBeNil)
			<-time.After(6 * time.Millisecond)

			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateReady)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_2", "key_1", "key_3"})
			So(err, ShouldBeNil)

			So(five.Dependencies(), ShouldResemble, []string{"key_2", "key_1", "key_3"})
			hasDeps, err = queue.HasDependents("key_1")
			So(err, ShouldBeNil)
			So(hasDeps, ShouldBeTrue)
			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateDependent)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_2", "key_3"})
			So(err, ShouldBeNil)

			So(five.Stats().State, ShouldEqual, ItemStateReady)

			five, err = queue.Reserve("five")
			So(err, ShouldBeNil)
			So(five, ShouldNotBeNil)
			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateRun)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_2", "key_1", "key_3"})
			So(err, ShouldBeNil)

			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateDependent)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_2", "key_3"})
			So(err, ShouldBeNil)

			So(five.Stats().State, ShouldEqual, ItemStateReady)

			five, err = queue.Reserve("five")
			So(err, ShouldBeNil)
			So(five, ShouldNotBeNil)
			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateRun)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, 1*time.Second, fiveStats.TTR, []string{"key_2", "key_3"})
			So(err, ShouldBeNil)

			queue.Release(five.Key)
			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateDelay)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_2", "key_1", "key_3"})
			So(err, ShouldBeNil)

			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateDependent)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_2", "key_3"})
			So(err, ShouldBeNil)

			So(five.Stats().State, ShouldEqual, ItemStateReady)

			five, err = queue.Reserve("five")
			So(err, ShouldBeNil)
			So(five, ShouldNotBeNil)
			So(five.Stats().State, ShouldEqual, ItemStateRun)

			queue.Bury(five.Key)
			fiveStats = five.Stats()
			So(fiveStats.State, ShouldEqual, ItemStateBury)

			err = queue.Update("key_5", "five", five.Data, fiveStats.Priority, fiveStats.Delay, fiveStats.TTR, []string{"key_1"})
			So(err, ShouldBeNil)

			So(five.Stats().State, ShouldEqual, ItemStateBury)

			queue.Kick(five.Key)
			So(five.Stats().State, ShouldEqual, ItemStateDependent)

			err = queue.Remove("key_1")
			So(err, ShouldBeNil)
			<-time.After(6 * time.Millisecond)

			So(five.Stats().State, ShouldEqual, ItemStateReady)
		})

		Convey("You can add dependencies on non-exist items and resolve them later", func() {
			ten, err := queue.Add("key_10", "", "10", 0, 0*time.Second, 30*time.Second, []string{"key_9"})
			So(err, ShouldBeNil)
			So(ten.Stats().State, ShouldEqual, ItemStateDependent)

			_, err = queue.Add("key_9", "", "9", 0, 0*time.Second, 30*time.Second, []string{})
			So(err, ShouldBeNil)
			So(ten.Stats().State, ShouldEqual, ItemStateDependent)

			err = queue.Remove("key_9")
			So(err, ShouldBeNil)
			<-time.After(6 * time.Millisecond)

			So(ten.Stats().State, ShouldEqual, ItemStateReady)
		})
	})

	Convey("Once some items with dependencies have been added to the queue en-masse", t, func() {
		// same setup as in previous test
		queue := New("dep many queue")

		var itemdefs []*ItemDef
		itemdefs = append(itemdefs, &ItemDef{
			Key:  "key_1",
			Data: "1",
			TTR:  30 * time.Second,
		})
		itemdefs = append(itemdefs, &ItemDef{
			Key:  "key_2",
			Data: "2",
			TTR:  30 * time.Second,
		})
		itemdefs = append(itemdefs, &ItemDef{"key_3", "", "3", 0, 0 * time.Second, 30 * time.Second, []string{}})
		itemdefs = append(itemdefs, &ItemDef{"key_4", "", "4", 0, 0 * time.Second, 30 * time.Second, []string{"key_1"}})
		itemdefs = append(itemdefs, &ItemDef{"key_5", "", "5", 0, 0 * time.Second, 30 * time.Second, []string{"key_2", "key_3"}})
		itemdefs = append(itemdefs, &ItemDef{"key_6", "", "6", 0, 0 * time.Second, 30 * time.Second, []string{"key_3", "key_4"}})
		itemdefs = append(itemdefs, &ItemDef{"key_7", "", "7", 0, 0 * time.Second, 30 * time.Second, []string{"key_5", "key_6"}})
		itemdefs = append(itemdefs, &ItemDef{"key_8", "", "8", 0, 0 * time.Second, 30 * time.Second, []string{"key_5"}})

		added, dups, err := queue.AddMany(itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 8)
		So(dups, ShouldEqual, 0)

		item7, err := queue.Get("key_7")
		So(err, ShouldBeNil)
		So(item7.Dependencies(), ShouldResemble, []string{"key_5", "key_6"})

		Convey("Only the non-dependent items are immediately ready", func() {
			depTestFunc(queue)
		})
	})
}

func checkChanged(changedChan chan *changedStruct, from, to SubQueue, count int) (ok bool) {
	loops := 0
	for cs := range changedChan {
		loops++
		if loops > 10 {
			break
		}
		if cs.from != from {
			continue
		}
		if cs.to != to {
			continue
		}
		if cs.count == count {
			ok = true
			break
		}
	}
	return
}

func depTestFunc(queue *Queue) {
	stats := queue.Stats()
	So(stats.Items, ShouldEqual, 8)
	So(stats.Delayed, ShouldEqual, 0)
	So(stats.Ready, ShouldEqual, 3)
	So(stats.Running, ShouldEqual, 0)
	So(stats.Buried, ShouldEqual, 0)
	So(stats.Dependant, ShouldEqual, 5)

	Convey("Once parent items are removed, dependent items become ready", func() {
		item, err := queue.Get("key_4")
		So(item.Stats().State, ShouldEqual, ItemStateDependent)

		err = queue.Remove("key_1")
		So(err, ShouldBeNil)
		<-time.After(6 * time.Millisecond)

		So(item.Stats().State, ShouldEqual, ItemStateReady)

		stats := queue.Stats()
		So(stats.Items, ShouldEqual, 7)
		So(stats.Delayed, ShouldEqual, 0)
		So(stats.Ready, ShouldEqual, 3)
		So(stats.Running, ShouldEqual, 0)
		So(stats.Buried, ShouldEqual, 0)
		So(stats.Dependant, ShouldEqual, 4)

		item, err = queue.Get("key_6")
		So(item.Stats().State, ShouldEqual, ItemStateDependent)

		err = queue.Remove("key_3")
		So(err, ShouldBeNil)
		<-time.After(6 * time.Millisecond)

		So(item.Stats().State, ShouldEqual, ItemStateDependent)

		stats = queue.Stats()
		So(stats.Items, ShouldEqual, 6)
		So(stats.Ready, ShouldEqual, 2)
		So(stats.Dependant, ShouldEqual, 4)

		err = queue.Remove("key_4")
		So(err, ShouldBeNil)
		<-time.After(6 * time.Millisecond)

		So(item.Stats().State, ShouldEqual, ItemStateReady)

		stats = queue.Stats()
		So(stats.Items, ShouldEqual, 5)
		So(stats.Ready, ShouldEqual, 2)
		So(stats.Dependant, ShouldEqual, 3)

		err = queue.Remove("key_6")
		So(err, ShouldBeNil)
		<-time.After(6 * time.Millisecond)

		stats = queue.Stats()
		So(stats.Items, ShouldEqual, 4)
		So(stats.Ready, ShouldEqual, 1)
		So(stats.Dependant, ShouldEqual, 3)

		item, err = queue.Get("key_5")
		So(item.Stats().State, ShouldEqual, ItemStateDependent)

		err = queue.Remove("key_2")
		So(err, ShouldBeNil)
		<-time.After(6 * time.Millisecond)

		So(item.Stats().State, ShouldEqual, ItemStateReady)

		stats = queue.Stats()
		So(stats.Items, ShouldEqual, 3)
		So(stats.Ready, ShouldEqual, 1)
		So(stats.Dependant, ShouldEqual, 2)

		item7, err := queue.Get("key_7")
		So(item7.Stats().State, ShouldEqual, ItemStateDependent)
		item8, err := queue.Get("key_8")
		So(item8.Stats().State, ShouldEqual, ItemStateDependent)

		err = queue.Remove("key_5")
		So(err, ShouldBeNil)
		<-time.After(6 * time.Millisecond)

		So(item7.Stats().State, ShouldEqual, ItemStateReady)
		So(item8.Stats().State, ShouldEqual, ItemStateReady)
		So(item7.Dependencies(), ShouldResemble, []string{"key_5", "key_6"})
	})
}
