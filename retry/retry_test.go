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
	"time"

	"github.com/VertebrateResequencing/wr/backoff"
	bm "github.com/VertebrateResequencing/wr/backoff/mock"
	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
)

var ErrOp = errors.New("op err")

func TestRetry(t *testing.T) {
	ctx := context.Background()
	wait := 1 * time.Millisecond
	backoff := &backoff.Backoff{Min: wait, Max: wait, Factor: 1}
	activity := "doing foo"

	Convey("You can Retry things until they succeed", t, func() {
		count := 0
		op := func() error {
			count++
			if count == 3 {
				return nil
			}

			return ErrOp
		}

		sleeper := &bm.Sleeper{}
		backoff.Sleeper = sleeper
		buff := clog.ToBufferAtLevel("debug")

		defer clog.ToDefault()

		status := Do(ctx, op, &UntilNoError{}, backoff, activity)
		So(status.Retried, ShouldEqual, 2)
		So(status.StoppedBecause, ShouldEqual, BecauseErrorNil)
		So(status.Err, ShouldBeNil)

		msg := "after 2 retries, stopped trying because there was no error"
		So(status.String(), ShouldEqual, msg)
		So(status.Error(), ShouldEqual, msg)
		So(status.Unwrap(), ShouldBeNil)
		So(count, ShouldEqual, 3)
		So(sleeper.Elapsed(), ShouldEqual, 2*time.Millisecond)

		Convey("And the backoffs and final state are logged with context", func() {
			lmsg := buff.String()
			So(lmsg, ShouldContainSubstring, "lvl=dbug")
			So(lmsg, ShouldContainSubstring, "msg=backoff")
			So(lmsg, ShouldContainSubstring, "sleep=1ms")
			So(lmsg, ShouldContainSubstring, "retryset=")
			So(lmsg, ShouldContainSubstring, "retryactivity=\""+activity)
			So(lmsg, ShouldContainSubstring, "retrynum=1")
			So(lmsg, ShouldContainSubstring, "retrynum=2")
			So(lmsg, ShouldContainSubstring, "msg=retried")
			So(lmsg, ShouldContainSubstring, "status=\""+msg)
		})
	})

	Convey("You can Retry things until you give up", t, func() {
		count := 0
		op := func() error {
			count++

			return ErrOp
		}

		sleeper := &bm.Sleeper{}
		backoff.Sleeper = sleeper

		status := Do(ctx, op, &UntilLimit{Max: 2}, backoff, activity)
		So(status.Retried, ShouldEqual, 2)
		So(status.StoppedBecause, ShouldEqual, BecauseLimitReached)
		So(status.Err, ShouldEqual, ErrOp)

		msg := "after 2 retries, stopped trying because limit reached; err: op err"
		So(status.String(), ShouldEqual, msg)
		So(status.Error(), ShouldEqual, msg)
		So(status.Unwrap(), ShouldEqual, ErrOp)
		So(count, ShouldEqual, 3)
		So(sleeper.Elapsed(), ShouldEqual, 2*time.Millisecond)
	})

	Convey("When there are no retries, there are no log messages", t, func() {
		op := func() error {
			return nil
		}

		sleeper := &bm.Sleeper{}
		backoff.Sleeper = sleeper
		buff := clog.ToBufferAtLevel("debug")

		defer clog.ToDefault()

		status := Do(ctx, op, &UntilNoError{}, backoff, activity)
		So(status.Retried, ShouldEqual, 0)
		So(status.StoppedBecause, ShouldEqual, BecauseErrorNil)
		So(status.Err, ShouldBeNil)

		msg := "after 0 retries, stopped trying because there was no error"
		So(status.String(), ShouldEqual, msg)

		So(buff.String(), ShouldBeBlank)
	})
}
