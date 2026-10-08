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
	swReady   = "suspend with ready"
	swDelay   = "suspend with delay"
	swWaiting = "suspend with waiting"
	swDep     = "suspend with dep"
	swRunning = "suspend with running"
)

// TestSuspendWith proves SuspendWith runs its callback on a suspendable item
// before the item can be resumed, and not at all when the suspend fails.
func TestSuspendWith(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with a ready, a delayed, a dependent, a buried and a running item", t, func() {
		queue := New(ctx, "suspend with queue")
		defer qdestroy(queue)

		_, _, err := queue.AddMany(ctx, []*ItemDef{
			{Key: swReady, Data: "ready to suspend", TTR: time.Minute},
			{Key: swDelay, Data: "delayed to suspend", TTR: time.Minute, Delay: time.Hour},
			{Key: swWaiting, Data: "waiting to suspend", TTR: time.Minute, Dependencies: []string{swDep}},
			{Key: swDep, Data: "dep data", TTR: time.Minute, StartQueue: SubQueueBury},
			{Key: swRunning, Data: "running data", TTR: time.Minute, StartQueue: SubQueueRun},
		})
		So(err, ShouldBeNil)

		var (
			calls       []any
			calledState []ItemState
		)

		suspending := func(key string) func(data any) {
			item, errg := queue.Get(key)
			So(errg, ShouldBeNil)

			return func(data any) {
				calls = append(calls, data)
				calledState = append(calledState, item.Stats().State)
			}
		}

		Convey("SuspendWith of the ready item calls back with its data before it can be resumed", func() {
			var resumeErr error

			done := make(chan struct{})

			callback := suspending(swReady)
			racing := func(data any) {
				callback(data)

				// the queue is locked for the callback, so a resume made now only
				// gets in once the suspend has moved the item.
				go func() {
					defer close(done)

					resumeErr = queue.Resume(ctx, swReady)
				}()

				select {
				case <-done:
				case <-time.After(200 * time.Millisecond):
				}
			}

			So(queue.SuspendWith(ctx, swReady, racing), ShouldBeNil)
			<-done
			So(calls, ShouldResemble, []any{"ready to suspend"})
			So(calledState, ShouldResemble, []ItemState{ItemStateReady})
			So(resumeErr, ShouldBeNil)

			item, errg := queue.Get(swReady)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, ItemStateReady)
		})

		Convey("SuspendWith of a delayed or dependent item calls back before suspending it", func() {
			So(queue.SuspendWith(ctx, swDelay, suspending(swDelay)), ShouldBeNil)
			So(queue.SuspendWith(ctx, swWaiting, suspending(swWaiting)), ShouldBeNil)
			So(calls, ShouldResemble, []any{"delayed to suspend", "waiting to suspend"})
			So(calledState, ShouldResemble, []ItemState{ItemStateDelay, ItemStateDependent})

			for _, key := range []string{swDelay, swWaiting} {
				item, errg := queue.Get(key)
				So(errg, ShouldBeNil)
				So(item.Stats().State, ShouldEqual, ItemStateSuspended)
			}
		})

		Convey("SuspendWith of an item that is not suspendable fails without calling back", func() {
			for _, key := range []string{swDep, swRunning} {
				errs := queue.SuspendWith(ctx, key, suspending(key))

				var qerr Error
				So(errors.As(errs, &qerr), ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotSuspendable)
			}

			So(queue.SuspendWith(ctx, swReady, suspending(swReady)), ShouldBeNil)
			errs := queue.SuspendWith(ctx, swReady, suspending(swReady))

			var qerr Error
			So(errors.As(errs, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotSuspendable)
			So(calls, ShouldHaveLength, 1)
		})

		Convey("SuspendWith of a missing item fails without calling back", func() {
			errs := queue.SuspendWith(ctx, "missing", func(data any) {
				calls = append(calls, data)
			})

			var qerr Error
			So(errors.As(errs, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrNotSuspendable)
			So(calls, ShouldBeEmpty)
		})
	})
}
