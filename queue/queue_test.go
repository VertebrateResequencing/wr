// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

package queue

import (
	"fmt"
	. "github.com/smartystreets/goconvey/convey"
	"testing"
	"time"
)

func TestQueue(t *testing.T) {
	Convey("Once 10 items of differing delay and ttr have been added to the queue", t, func() {
		queue := New("myqueue")
		items := make(map[string]*Item)
		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("key_%d", i)
			t := time.Duration((i+1)*100) * time.Millisecond
			item, existed := queue.Add(key, "data", 0, t, t)
			So(existed, ShouldBeFalse)
			items[key] = item
		}

		stats := queue.Stats()
		So(stats.Items, ShouldEqual, 10)
		So(stats.Delayed, ShouldEqual, 10)
		So(stats.Ready, ShouldEqual, 0)
		So(stats.Running, ShouldEqual, 0)
		So(stats.Buried, ShouldEqual, 0)

		Convey("You can get an item back out", func() {
			item, exists := queue.Get("key_0")
			So(exists, ShouldBeTrue)
			So(item.Data, ShouldEqual, "data")
			So(item.creation, ShouldHappenOnOrBefore, items["key_0"].creation)
		})

		Convey("You can't add the same item again", func() {
			item, existed := queue.Add("key_0", "data new", 0, 100*time.Millisecond, 100*time.Millisecond)
			So(existed, ShouldBeTrue)
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
			<-time.After(110 * time.Millisecond)
			stats = queue.Stats()
			So(stats.Delayed, ShouldEqual, 7)
			So(stats.Ready, ShouldEqual, 3)

			Convey("Once ready you should be able to reserve them in the expected order", func() {
				item1 := queue.Reserve()
				So(item1, ShouldNotBeNil)
				So(item1.Key, ShouldEqual, "key_0")
				So(item1.reserves, ShouldEqual, 1)
				item2 := queue.Reserve()
				So(item2, ShouldNotBeNil)
				So(item2.Key, ShouldEqual, "key_1")
				item3 := queue.Reserve()
				So(item3, ShouldNotBeNil)
				So(item3.Key, ShouldEqual, "key_2")
				item4 := queue.Reserve()
				So(item4, ShouldBeNil)

				stats = queue.Stats()
				So(stats.Items, ShouldEqual, 10)
				So(stats.Delayed, ShouldEqual, 7)
				So(stats.Ready, ShouldEqual, 0)
				So(stats.Running, ShouldEqual, 3)

				Convey("Once reserved you can release them", func() {
					So(item1.state, ShouldEqual, "run")
					So(item1.releases, ShouldEqual, 0)
					released := queue.Release(item1.Key)
					So(released, ShouldBeTrue)
					So(item1.state, ShouldEqual, "ready")
					So(item1.releases, ShouldEqual, 1)

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 10)
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 1)
					So(stats.Running, ShouldEqual, 2)

					Convey("You can get read-only item stats at any point", func() {
						itemstats := item1.Stats()
						So(itemstats.State, ShouldEqual, "ready")
						So(itemstats.Releases, ShouldEqual, 1)
					})
				})

				Convey("Or remove them", func() {
					So(item2.state, ShouldEqual, "run")
					removed := queue.Remove(item2.Key)
					So(removed, ShouldBeTrue)
					So(item2.state, ShouldEqual, "removed")

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 9)
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 2)

					Convey("Releasing, touching, burying and kicking fail after removal", func() {
						ok := queue.Release(item2.Key)
						So(ok, ShouldBeFalse)
						ok = queue.Touch(item2.Key)
						So(ok, ShouldBeFalse)
						ok = queue.Bury(item2.Key)
						So(ok, ShouldBeFalse)
						ok = queue.Kick(item2.Key)
						So(ok, ShouldBeFalse)
					})
				})

				Convey("Or bury them", func() {
					So(item3.state, ShouldEqual, "run")
					So(item3.buries, ShouldEqual, 0)
					buried := queue.Bury(item3.Key)
					So(buried, ShouldBeTrue)
					So(item3.state, ShouldEqual, "bury")
					So(item3.buries, ShouldEqual, 1)

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 10)
					So(stats.Delayed, ShouldEqual, 7)
					So(stats.Ready, ShouldEqual, 0)
					So(stats.Running, ShouldEqual, 2)
					So(stats.Buried, ShouldEqual, 1)

					Convey("Once buried you can kick them", func() {
						So(item3.kicks, ShouldEqual, 0)
						kicked := queue.Kick(item3.Key)
						So(kicked, ShouldBeTrue)
						So(item3.state, ShouldEqual, "delay")
						So(item3.kicks, ShouldEqual, 1)

						stats = queue.Stats()
						So(stats.Items, ShouldEqual, 10)
						So(stats.Delayed, ShouldEqual, 8)
						So(stats.Ready, ShouldEqual, 0)
						So(stats.Running, ShouldEqual, 2)
						So(stats.Buried, ShouldEqual, 0)

						Convey("And then they automatically get ready again", func() {
							<-time.After(310 * time.Millisecond)
							So(item3.state, ShouldEqual, "ready")
						})
					})

					Convey("You can also remove them whilst buried", func() {
						ok := queue.Remove("key_2")
						So(ok, ShouldBeTrue)

						stats := queue.Stats()
						So(stats.Items, ShouldEqual, 9)
						So(stats.Delayed, ShouldEqual, 7)
						So(stats.Ready, ShouldEqual, 0)
						So(stats.Running, ShouldEqual, 2)
						So(stats.Buried, ShouldEqual, 0)

						ok = queue.Remove("key_2")
						So(ok, ShouldBeFalse)
					})
				})

				Convey("If not buried you can't kick them", func() {
					kicked := queue.Kick(item3.Key)
					So(kicked, ShouldBeFalse)
					So(item3.state, ShouldEqual, "run")
				})

				Convey("If you do nothing they get auto-released", func() {
					<-time.After(50 * time.Millisecond)
					So(item1.state, ShouldEqual, "run")
					<-time.After(60 * time.Millisecond)
					So(item1.state, ShouldEqual, "ready")
					So(item1.timeouts, ShouldEqual, 1)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 2)
					So(stats.Running, ShouldEqual, 2)
					<-time.After(110 * time.Millisecond)
					So(item2.state, ShouldEqual, "ready")
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 4)
					So(stats.Running, ShouldEqual, 1)
					<-time.After(110 * time.Millisecond)
					So(item3.state, ShouldEqual, "ready")
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 6)
					So(stats.Running, ShouldEqual, 0)

					stats = queue.Stats()
					So(stats.Items, ShouldEqual, 10)
					So(stats.Delayed, ShouldEqual, 4)
				})

				Convey("Though you can prevent auto-release by touching them", func() {
					<-time.After(50 * time.Millisecond)
					So(item1.state, ShouldEqual, "run")
					queue.Touch(item1.Key)
					<-time.After(60 * time.Millisecond)
					So(item1.state, ShouldEqual, "run")
					So(item1.timeouts, ShouldEqual, 0)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 1)
					So(stats.Running, ShouldEqual, 3)
					<-time.After(50 * time.Millisecond)
					So(item1.state, ShouldEqual, "ready")
					So(item1.timeouts, ShouldEqual, 1)
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 2)
					So(stats.Running, ShouldEqual, 2)
					<-time.After(60 * time.Millisecond)
					So(item2.state, ShouldEqual, "ready")
					stats = queue.Stats()
					So(stats.Ready, ShouldEqual, 4)
					So(stats.Running, ShouldEqual, 1)
				})

				Convey("Touching doesn't mess with the correct queue order", func() {
					queue := New("new queue")
					queue.Add("item1", "data", 0, 0*time.Millisecond, 50*time.Millisecond)
					queue.Add("item2", "data", 0, 0*time.Millisecond, 52*time.Millisecond)
					<-time.After(1 * time.Millisecond)
					item1 := queue.Reserve()
					So(item1.Key, ShouldEqual, "item1")
					So(item1.state, ShouldEqual, "run")
					item2 := queue.Reserve()
					So(item2.Key, ShouldEqual, "item2")
					So(item2.state, ShouldEqual, "run")

					<-time.After(25 * time.Millisecond)

					So(queue.runQueue.items[0].Key, ShouldEqual, "item1")
					queue.Touch(item1.Key)
					So(queue.runQueue.items[0].Key, ShouldEqual, "item2")

					<-time.After(30 * time.Millisecond)

					So(item1.state, ShouldEqual, "run")
					So(item2.state, ShouldEqual, "ready")

					<-time.After(25 * time.Millisecond)
					So(item1.state, ShouldEqual, "ready")
				})
			})

			Convey("If not reserved you can't release, touch, bury or kick them", func() {
				ok := queue.Release("key_0")
				So(ok, ShouldBeFalse)
				ok = queue.Touch("key_0")
				So(ok, ShouldBeFalse)
				ok = queue.Bury("key_0")
				So(ok, ShouldBeFalse)
				ok = queue.Kick("key_0")
				So(ok, ShouldBeFalse)
			})

			Convey("But you can remove them when ready", func() {
				ok := queue.Remove("key_0")
				So(ok, ShouldBeTrue)

				stats := queue.Stats()
				So(stats.Items, ShouldEqual, 9)
				So(stats.Delayed, ShouldEqual, 7)
				So(stats.Ready, ShouldEqual, 2)
				So(stats.Running, ShouldEqual, 0)
				So(stats.Buried, ShouldEqual, 0)

				ok = queue.Remove("key_0")
				So(ok, ShouldBeFalse)
			})
		})

		Convey("If not ready you can't reserve them", func() {
			item := queue.Reserve()
			So(item, ShouldBeNil)
		})

		Convey("But you can remove them when not ready", func() {
			ok := queue.Remove("key_0")
			So(ok, ShouldBeTrue)

			stats := queue.Stats()
			So(stats.Items, ShouldEqual, 9)
			So(stats.Delayed, ShouldEqual, 9)
			So(stats.Ready, ShouldEqual, 0)
			So(stats.Running, ShouldEqual, 0)
			So(stats.Buried, ShouldEqual, 0)

			ok = queue.Remove("key_0")
			So(ok, ShouldBeFalse)
		})
	})

	Convey("Once an item been added to the queue", t, func() {
		queue := New("myqueue")
		item, _ := queue.Add("item1", "data", 0, 50*time.Millisecond, 50*time.Millisecond)

		Convey("It can be removed from the queue immediately prior to it getting switched to the ready queue", func() {
			<-time.After(49 * time.Millisecond)
			queue.Remove("item1")
			<-time.After(6 * time.Millisecond)
			So(item.state, ShouldEqual, "removed")

			stats := queue.Stats()
			So(stats.Items, ShouldEqual, 0)
			So(stats.Delayed, ShouldEqual, 0)
			So(stats.Ready, ShouldEqual, 0)

			Convey("Once removed it can't be updated", func() {
				ok := queue.Update("item1", "data", 0, 75*time.Millisecond, 50*time.Millisecond)
				So(ok, ShouldBeFalse)
			})
		})

		Convey("The queue won't fall over if we manage to change the item's readyAt without updating the queue", func() {
			<-time.After(49 * time.Millisecond)
			item.readyAt = time.Now().Add(25 * time.Millisecond)
			<-time.After(6 * time.Millisecond)
			So(item.state, ShouldEqual, "delay")
			<-time.After(25 * time.Millisecond)
			So(item.state, ShouldEqual, "ready")
		})

		Convey("The delay can be updated even in the delay queue", func() {
			<-time.After(25 * time.Millisecond)
			ok := queue.Update("item1", "data", 0, 75*time.Millisecond, 50*time.Millisecond)
			So(ok, ShouldBeTrue)
			<-time.After(30 * time.Millisecond)
			So(item.state, ShouldEqual, "delay")
			<-time.After(50 * time.Millisecond)
			So(item.state, ShouldEqual, "ready")

			Convey("When ready the priority can be updated", func() {
				ok := queue.Update("item1", "data", 1, 75*time.Millisecond, 50*time.Millisecond)
				So(ok, ShouldBeTrue)
				So(item.priority, ShouldEqual, 1)
			})
		})

		Convey("Once reserved", func() {
			<-time.After(55 * time.Millisecond)
			queue.Reserve()

			Convey("It can be removed from the queue immediately prior to it getting switched to the ready queue", func() {
				<-time.After(49 * time.Millisecond)
				queue.Remove("item1")
				<-time.After(6 * time.Millisecond)
				So(item.state, ShouldEqual, "removed")

				stats := queue.Stats()
				So(stats.Items, ShouldEqual, 0)
				So(stats.Delayed, ShouldEqual, 0)
				So(stats.Ready, ShouldEqual, 0)
			})

			Convey("The queue won't fall over if we manage to change the item's releaseAt without updating the queue", func() {
				<-time.After(49 * time.Millisecond)
				item.releaseAt = time.Now().Add(25 * time.Millisecond)
				<-time.After(6 * time.Millisecond)
				So(item.state, ShouldEqual, "run")
				<-time.After(25 * time.Millisecond)
				So(item.state, ShouldEqual, "ready")
			})

			Convey("When running the ttr can be updated", func() {
				<-time.After(25 * time.Millisecond)
				ok := queue.Update("item1", "data", 0, 50*time.Millisecond, 75*time.Millisecond)
				So(ok, ShouldBeTrue)
				<-time.After(30 * time.Millisecond)
				So(item.state, ShouldEqual, "run")
				<-time.After(50 * time.Millisecond)
				So(item.state, ShouldEqual, "ready")
			})
		})
	})
}
