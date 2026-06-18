/*******************************************************************************
 * Copyright (c) 2020 Genome Research Ltd.
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

package retry

import (
	"context"
	"errors"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

var ErrNormal = errors.New("normal")

func TestUntil(t *testing.T) {
	Convey("UntilLimit stops after the specified limit", t, func() {
		var _ Until = (*UntilLimit)(nil)
		u := &UntilLimit{Max: 2}
		So(u.ShouldStop(0, ErrNormal), ShouldEqual, doNotStop)
		So(u.ShouldStop(0, nil), ShouldEqual, doNotStop)
		So(u.ShouldStop(1, ErrNormal), ShouldEqual, doNotStop)
		So(u.ShouldStop(2, ErrNormal), ShouldEqual, BecauseLimitReached)
		So(u.ShouldStop(3, ErrNormal), ShouldEqual, BecauseLimitReached)
	})

	Convey("UntilNoError stops after getting no error", t, func() {
		var _ Until = (*UntilNoError)(nil)
		u := &UntilNoError{}
		So(u.ShouldStop(0, ErrNormal), ShouldEqual, doNotStop)
		So(u.ShouldStop(1, ErrNormal), ShouldEqual, doNotStop)
		So(u.ShouldStop(0, nil), ShouldEqual, BecauseErrorNil)
		So(u.ShouldStop(1, nil), ShouldEqual, BecauseErrorNil)
	})

	Convey("untilContext stops after the context is done", t, func() {
		var _ Until = (*untilContext)(nil)
		ctx, cancel := context.WithCancel(context.Background())
		u := &untilContext{Context: ctx}
		So(u.ShouldStop(1, nil), ShouldEqual, doNotStop)
		cancel()
		So(u.ShouldStop(1, nil), ShouldEqual, BecauseContextClosed)
	})

	Convey("You can combine multiple Untils", t, func() {
		var _ Until = (*Untils)(nil)
		u := Untils{&UntilLimit{Max: 2}, &UntilNoError{}}
		So(u.ShouldStop(0, ErrNormal), ShouldEqual, doNotStop)
		So(u.ShouldStop(2, ErrNormal), ShouldEqual, BecauseLimitReached)
		So(u.ShouldStop(0, nil), ShouldEqual, BecauseErrorNil)
		So(u.ShouldStop(2, nil), ShouldEqual, BecauseLimitReached)

		ctx, cancel := context.WithCancel(context.Background())
		u = Untils{u, &untilContext{Context: ctx}}
		cancel()
		So(u.ShouldStop(1, ErrNormal), ShouldEqual, BecauseContextClosed)
	})
}
