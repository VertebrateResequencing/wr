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
	. "github.com/smartystreets/goconvey/convey"
	"testing"
	"time"
)

func TestItem(t *testing.T) {
	Convey("Given an item with a non-zero delay and ttr", t, func() {
		item := newItem("item1", "data", 255, 100*time.Millisecond, 100*time.Millisecond)
		So(item.State, ShouldEqual, "delay")
		So(item.readyAt, ShouldHappenOnOrBetween, time.Now().Add(90*time.Millisecond), time.Now().Add(100*time.Millisecond))

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
			So(item.delayIndex, ShouldEqual, -1)
			So(item.readyAt, ShouldBeZeroValue)
			So(item.State, ShouldEqual, "ready")
		})

		Convey("Switching from ready to run updates properties; without a touch it still doesn't release", func() {
			item.switchReadyRun()
			So(item.readyIndex, ShouldEqual, -1)
			So(item.Reserves, ShouldEqual, 1)
			So(item.State, ShouldEqual, "run")
			So(item.releasable(), ShouldBeFalse)
			<-time.After(110 * time.Millisecond)
			So(item.releasable(), ShouldBeFalse)
		})

		Convey("Switching from run to ready updates properties", func() {
			item.switchRunReady("timeout")
			So(item.ttrIndex, ShouldEqual, -1)
			So(item.releaseAt, ShouldBeZeroValue)
			So(item.Timeouts, ShouldEqual, 1)
			So(item.Releases, ShouldBeZeroValue)
			So(item.State, ShouldEqual, "ready")

			item.switchRunReady("release")
			So(item.Timeouts, ShouldEqual, 1)
			So(item.Releases, ShouldEqual, 1)
		})

		Convey("Switching from run to bury updates properties", func() {
			item.switchRunBury()
			So(item.ttrIndex, ShouldEqual, -1)
			So(item.releaseAt, ShouldBeZeroValue)
			So(item.State, ShouldEqual, "bury")
		})

		Convey("Switching from bury to delay updates properties", func() {
			item.switchBuryDelay()
			So(item.buryIndex, ShouldEqual, -1)
			So(item.State, ShouldEqual, "delay")

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

		Convey("Properties can be easily cleared out following removal", func() {
			item.removalCleanup()
			So(item.ttrIndex, ShouldEqual, -1)
			So(item.readyIndex, ShouldEqual, -1)
			So(item.delayIndex, ShouldEqual, -1)
			So(item.buryIndex, ShouldEqual, -1)
			So(item.releaseAt, ShouldBeZeroValue)
			So(item.readyAt, ShouldBeZeroValue)
			So(item.State, ShouldEqual, "removed")
		})
	})
}
