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

const (
	kwBuried = "kick with buried"
	kwReady  = "kick with ready"
)

// TestKickWith proves KickWith runs its callback on a buried item before the
// item can be reserved, and not at all when the kick fails.
func TestKickWith(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with a buried item and a ready item", t, func() {
		queue := New(ctx, "kick with queue")
		defer qdestroy(queue)

		_, _, err := queue.AddMany(ctx, []*ItemDef{
			{Key: kwBuried, Data: "buried data", TTR: time.Minute, StartQueue: SubQueueBury},
			{Key: kwReady, Data: "ready data", TTR: time.Minute},
		})
		So(err, ShouldBeNil)

		buried, err := queue.Get(kwBuried)
		So(err, ShouldBeNil)

		var (
			calls       []any
			kickedState ItemState
		)

		reserved := []string{}
		done := make(chan struct{})

		kicking := func(data any) {
			calls = append(calls, data)

			kickedState = buried.Stats().State

			// the queue is locked for the callback, so a reserve made now only
			// gets in once the kick has moved the item.
			go func() {
				defer close(done)

				for {
					item, errr := queue.Reserve("", 0)
					if errr != nil {
						return
					}

					reserved = append(reserved, item.Key)
				}
			}()

			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
			}
		}

		Convey("KickWith of the buried item calls back with its data before it can be reserved", func() {
			So(queue.KickWith(ctx, kwBuried, kicking), ShouldBeNil)
			<-done
			So(calls, ShouldResemble, []any{"buried data"})
			So(kickedState, ShouldEqual, ItemStateBury)
			So(reserved, ShouldHaveLength, 2)
			So(reserved, ShouldContain, kwBuried)
		})

		Convey("KickWith of an item that is not buried fails without calling back", func() {
			errk := queue.KickWith(ctx, kwReady, kicking)

			var qerr Error
			So(errors.As(errk, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotBuried)
			So(calls, ShouldBeEmpty)
		})

		Convey("KickWith of a missing item fails without calling back", func() {
			errk := queue.KickWith(ctx, "missing", kicking)

			var qerr Error
			So(errors.As(errk, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotFound)
			So(calls, ShouldBeEmpty)
		})
	})
}
