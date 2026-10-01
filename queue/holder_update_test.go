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

// TestUpdateHolderUnlessRunning proves an update of an item by the data it held
// changes nothing once another item holding other data has replaced it.
func TestUpdateHolderUnlessRunning(t *testing.T) {
	ctx := context.Background()

	Convey("Given an item that was removed and replaced under its key by one with other data", t, func() {
		q := New(ctx, "holder_update")

		defer func() { So(q.Destroy(), ShouldBeNil) }()

		old, replacement := &struct{ name string }{"old"}, &struct{ name string }{"replacement"}

		_, err := q.Add(ctx, key1, "", old, 0, 0, time.Hour, "")
		So(err, ShouldBeNil)
		So(q.Remove(ctx, key1), ShouldBeNil)

		_, err = q.Add(ctx, key1, "", replacement, 0, 0, time.Hour, "")
		So(err, ShouldBeNil)

		Convey("an update by the old data is refused with ErrDataChanged, leaving the replacement alone", func() {
			running, erru := q.UpdateHolderUnlessRunning(ctx, key1, "", old, 0, 0, time.Hour,
				[]string{depG1}, func() { panic("called for an item that does not hold the data") })
			So(running, ShouldBeFalse)

			var qerr Error

			So(errors.As(erru, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrDataChanged)

			item, errg := q.Get(key1)
			So(errg, ShouldBeNil)
			So(item.Data(), ShouldEqual, replacement)
			So(item.Stats().State, ShouldEqual, ItemStateReady)
			So(item.UnresolvedDependencies(), ShouldBeEmpty)
		})

		Convey("an update by the replacement's data is applied", func() {
			running, erru := q.UpdateHolderUnlessRunning(ctx, key1, "", replacement, 0, 0, time.Hour,
				[]string{depG1}, func() { panic("called for a waiting item") })
			So(erru, ShouldBeNil)
			So(running, ShouldBeFalse)

			item, errg := q.Get(key1)
			So(errg, ShouldBeNil)
			So(item.Data(), ShouldEqual, replacement)
			So(item.Stats().State, ShouldEqual, ItemStateDependent)
		})

		Convey("a running replacement is not called back for by the old data", func() {
			reserved, errr := q.Reserve("", 0)
			So(errr, ShouldBeNil)
			So(reserved.Data(), ShouldEqual, replacement)

			running, erru := q.UpdateHolderUnlessRunning(ctx, key1, "", old, 0, 0, time.Hour, nil,
				func() { panic("called for an item that does not hold the data") })
			So(running, ShouldBeFalse)
			So(erru, ShouldNotBeNil)

			called := 0

			running, erru = q.UpdateHolderUnlessRunning(ctx, key1, "", replacement, 0, 0, time.Hour, nil,
				func() { called++ })
			So(erru, ShouldBeNil)
			So(running, ShouldBeTrue)
			So(called, ShouldEqual, 1)
		})
	})
}
