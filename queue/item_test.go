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
	. "github.com/smartystreets/goconvey/convey"
	"testing"
	"time"
)

func TestItem(t *testing.T) {
	Convey("Given an item with a non-zero delay and ttr", t, func() {
		item := newItem("item1", "", "data", 255, 100*time.Millisecond, 100*time.Millisecond)
		So(item.state, ShouldEqual, ItemStateDelay)
		So(item.readyAt, ShouldHappenOnOrBetween, time.Now().Add(90*time.Millisecond), time.Now().Add(100*time.Millisecond))

		Convey("Whilst delayed, if restarted the Remaining stat is available", func() {
			item.restart()
			stats := item.Stats()
			So(stats.State, ShouldEqual, ItemStateDelay)
			So(stats.Remaining.Nanoseconds(), ShouldBeBetweenOrEqual, 90000000, 100000000)
			So(stats.Age.Nanoseconds(), ShouldBeBetweenOrEqual, 0, time.Since(item.creation))
		})

		Convey("It won't be ready until after delay", func() {
			So(item.isready(), ShouldBeFalse)
			<-time.After(110 * time.Millisecond)
			So(item.isready(), ShouldBeTrue)
		})

		Convey("It won't be releasable at all", func() {
			So(item.releasable(), ShouldBeFalse)
			<-time.After(110 * time.Millisecond)
			So(item.releasable(), ShouldBeFalse)
		})

		Convey("After being touched it will be releasable after ttr", func() {
			item.touch()
			So(item.releasable(), ShouldBeFalse)
			<-time.After(110 * time.Millisecond)
			So(item.releasable(), ShouldBeTrue)
		})

		Convey("Switching from delay to ready updates properties", func() {
			item.switchDelayReady()
			So(item.queueIndexes[0], ShouldEqual, -1)
			So(item.readyAt, ShouldBeZeroValue)
			So(item.state, ShouldEqual, ItemStateReady)
		})

		Convey("Switching from ready to run updates properties; without a touch it still doesn't release", func() {
			item.switchReadyRun()
			So(item.queueIndexes[1], ShouldEqual, -1)
			So(item.reserves, ShouldEqual, 1)
			So(item.state, ShouldEqual, ItemStateRun)
			So(item.releasable(), ShouldBeFalse)
			<-time.After(110 * time.Millisecond)
			So(item.releasable(), ShouldBeFalse)

			Convey("If touched, the Remaining stat is available", func() {
				item.touch()
				stats := item.Stats()
				So(stats.State, ShouldEqual, ItemStateRun)
				So(stats.Remaining.Nanoseconds(), ShouldBeBetweenOrEqual, 90000000, 100000000)
				So(stats.Age.Nanoseconds(), ShouldBeBetweenOrEqual, 110000, time.Since(item.creation))
			})
		})

		Convey("Switching from run to ready updates properties", func() {
			item.switchRunReady()
			So(item.queueIndexes[2], ShouldEqual, -1)
			So(item.releaseAt, ShouldBeZeroValue)
			So(item.timeouts, ShouldEqual, 1)
			So(item.releases, ShouldBeZeroValue)
			So(item.state, ShouldEqual, ItemStateReady)
		})

		Convey("Switching from run to delay updates properties", func() {
			item.switchRunDelay()
			So(item.releases, ShouldEqual, 1)
			So(item.state, ShouldEqual, ItemStateDelay)

			Convey("restart updates when the item will be ready", func() {
				<-time.After(50 * time.Millisecond)
				item.restart()
				So(item.readyAt, ShouldHappenOnOrBetween, time.Now().Add(90*time.Millisecond), time.Now().Add(100*time.Millisecond))
				So(item.isready(), ShouldBeFalse)
				<-time.After(60 * time.Millisecond)
				So(item.isready(), ShouldBeFalse)
				<-time.After(60 * time.Millisecond)
				So(item.isready(), ShouldBeTrue)
			})
		})

		Convey("Switching from run to bury updates properties", func() {
			item.switchRunBury()
			So(item.queueIndexes[2], ShouldEqual, -1)
			So(item.releaseAt, ShouldBeZeroValue)
			So(item.state, ShouldEqual, ItemStateBury)

			Convey("Once, the Remaining stat is 0", func() {
				item.touch()
				stats := item.Stats()
				So(stats.State, ShouldEqual, ItemStateBury)
				So(stats.Remaining.Nanoseconds(), ShouldEqual, 0)
				So(stats.Age.Nanoseconds(), ShouldBeBetweenOrEqual, 0, time.Since(item.creation))
			})
		})

		Convey("Switching from bury to ready updates properties", func() {
			item.switchBuryReady()
			So(item.queueIndexes[3], ShouldEqual, -1)
			So(item.state, ShouldEqual, ItemStateReady)
		})

		Convey("Properties can be easily cleared out following removal", func() {
			item.removalCleanup()
			So(item.queueIndexes[2], ShouldEqual, -1)
			So(item.queueIndexes[1], ShouldEqual, -1)
			So(item.queueIndexes[0], ShouldEqual, -1)
			So(item.queueIndexes[3], ShouldEqual, -1)
			So(item.releaseAt, ShouldBeZeroValue)
			So(item.readyAt, ShouldBeZeroValue)
			So(item.state, ShouldEqual, ItemStateRemoved)
		})
	})
}
