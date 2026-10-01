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
	"errors"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestUpdateUnlessRunningAndRequeue covers the operations that let a caller
// hold a dependency change back from a running item until its run ends.
func TestUpdateUnlessRunningAndRequeue(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with one running item and one ready item", t, func() {
		q := New(ctx, "running_update")

		defer func() { So(q.Destroy(), ShouldBeNil) }()

		_, err := q.Add(ctx, key1, "", "running data", 0, 0, time.Hour, "")
		So(err, ShouldBeNil)

		reserved, err := q.Reserve("", 0)
		So(err, ShouldBeNil)
		So(reserved.Key, ShouldEqual, key1)

		_, err = q.Add(ctx, key2, "", "ready data", 0, 0, time.Hour, "")
		So(err, ShouldBeNil)

		Convey("UpdateUnlessRunning leaves the running item alone and calls back", func() {
			called := 0

			running, erru := q.UpdateUnlessRunning(ctx, key1, "", "new data", 0, 0, time.Hour,
				[]string{depG1}, func() { called++ })
			So(erru, ShouldBeNil)
			So(running, ShouldBeTrue)
			So(called, ShouldEqual, 1)

			item, errg := q.Get(key1)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, ItemStateRun)
			So(item.Data(), ShouldEqual, "running data")
			So(item.UnresolvedDependencies(), ShouldBeEmpty)
			So(q.Touch(key1), ShouldBeNil)
		})

		Convey("UpdateUnlessRunning updates a waiting item without calling back", func() {
			running, erru := q.UpdateUnlessRunning(ctx, key2, "", "new data", 0, 0, time.Hour,
				[]string{depG1}, func() { panic("called for a waiting item") })
			So(erru, ShouldBeNil)
			So(running, ShouldBeFalse)

			item, errg := q.Get(key2)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, ItemStateDependent)
			So(item.Data(), ShouldEqual, "new data")
		})

		Convey("UpdateUnlessRunning of a missing item is an ErrNotFound", func() {
			_, erru := q.UpdateUnlessRunning(ctx, key3, "", "", 0, 0, time.Hour, nil, func() {})
			So(erru, ShouldNotBeNil)

			var qerr Error

			So(errors.As(erru, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotFound)
		})

		Convey("Requeue with dependencies makes the running item wait on them", func() {
			So(q.Requeue(ctx, key1, []string{depG1}), ShouldBeNil)

			item, errg := q.Get(key1)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, ItemStateDependent)

			So(q.SatisfyDependency(ctx, depG1), ShouldBeNil)
			So(item.Stats().State, ShouldEqual, ItemStateReady)
		})

		Convey("Requeue without dependencies makes the running item ready again", func() {
			So(q.Requeue(ctx, key1, nil), ShouldBeNil)

			item, errg := q.Get(key1)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, ItemStateReady)
			So(item.Data(), ShouldEqual, "running data")
		})

		Convey("Requeue leaves the item's dependants waiting on it", func() {
			_, erra := q.Add(ctx, key3, "", "dependant", 0, 0, time.Hour, "", []string{key1})
			So(erra, ShouldBeNil)

			So(q.Requeue(ctx, key1, nil), ShouldBeNil)

			dependant, errg := q.Get(key3)
			So(errg, ShouldBeNil)
			So(dependant.Stats().State, ShouldEqual, ItemStateDependent)
		})

		Convey("RemoveUnless leaves the item, and its dependants waiting on it, if keep says so", func() {
			_, erra := q.Add(ctx, key3, "", "dependant", 0, 0, time.Hour, "", []string{key1})
			So(erra, ShouldBeNil)

			removed, errr := q.RemoveUnless(ctx, key1, func(data any) bool { return data == "running data" })
			So(errr, ShouldBeNil)
			So(removed, ShouldBeFalse)

			item, errg := q.Get(key1)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, ItemStateRun)

			dependant, errg := q.Get(key3)
			So(errg, ShouldBeNil)
			So(dependant.Stats().State, ShouldEqual, ItemStateDependent)

			removed, errr = q.RemoveUnless(ctx, key1, func(any) bool { return false })
			So(errr, ShouldBeNil)
			So(removed, ShouldBeTrue)

			_, errg = q.Get(key1)
			So(errg, ShouldNotBeNil)
			So(dependant.Stats().State, ShouldEqual, ItemStateReady)
		})

		Convey("RemoveUnless of a missing item is an ErrNotFound", func() {
			removed, errr := q.RemoveUnless(ctx, key3, func(any) bool { return false })
			So(removed, ShouldBeFalse)
			So(errr, ShouldNotBeNil)

			var qerr Error

			So(errors.As(errr, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotFound)
		})

		Convey("RemoveUnlessState tells keep the state of the item it may remove", func() {
			var seen []ItemState

			keepRecording := func(_ any, state ItemState) bool {
				seen = append(seen, state)

				return true
			}

			removed, errr := q.RemoveUnlessState(ctx, key1, keepRecording)
			So(errr, ShouldBeNil)
			So(removed, ShouldBeFalse)

			removed, errr = q.RemoveUnlessState(ctx, key2, keepRecording)
			So(errr, ShouldBeNil)
			So(removed, ShouldBeFalse)
			So(seen, ShouldResemble, []ItemState{ItemStateRun, ItemStateReady})

			removed, errr = q.RemoveUnlessState(ctx, key2, func(any, ItemState) bool { return false })
			So(errr, ShouldBeNil)
			So(removed, ShouldBeTrue)

			_, errg := q.Get(key2)
			So(errg, ShouldNotBeNil)
		})

		Convey("Requeue of an item that is not running is an ErrNotRunning", func() {
			erru := q.Requeue(ctx, key2, nil)
			So(erru, ShouldNotBeNil)

			var qerr Error

			So(errors.As(erru, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotRunning)
		})
	})
}
