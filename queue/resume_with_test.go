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
	rwSuspended = "resume with suspended"
	rwWaiting   = "resume with waiting"
	rwDep       = "resume with dep"
	rwReady     = "resume with ready"
)

// TestResumeWith proves ResumeWith runs its callback on a suspended item, with
// the state the item is moving to, before the item can be reserved, and not at
// all when the resume fails.
func TestResumeWith(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with a suspended item, a suspended item with an unresolved dependency, and a ready item", t,
		func() {
			queue := New(ctx, "resume with queue")
			defer qdestroy(queue)

			_, _, err := queue.AddMany(ctx, []*ItemDef{
				{Key: rwSuspended, Data: "suspended data", TTR: time.Minute, StartQueue: SubQueueSuspended},
				{
					Key: rwWaiting, Data: "waiting data", TTR: time.Minute, StartQueue: SubQueueSuspended,
					Dependencies: []string{rwDep},
				},
				{Key: rwDep, Data: "dep data", TTR: time.Minute, StartQueue: SubQueueBury},
				{Key: rwReady, Data: "ready data", TTR: time.Minute},
			})
			So(err, ShouldBeNil)

			var (
				calls        []any
				tos          []ItemState
				resumedState ItemState
			)

			resuming := func(key string) func(data any, to ItemState) {
				item, errg := queue.Get(key)
				So(errg, ShouldBeNil)

				return func(data any, to ItemState) {
					calls = append(calls, data)
					tos = append(tos, to)
					resumedState = item.Stats().State
				}
			}

			Convey("ResumeWith of the suspended item calls back with its data before it can be reserved", func() {
				reserved := []string{}
				done := make(chan struct{})

				callback := resuming(rwSuspended)
				racing := func(data any, to ItemState) {
					callback(data, to)

					// the queue is locked for the callback, so a reserve made now
					// only gets in once the resume has moved the item.
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

				So(queue.ResumeWith(ctx, rwSuspended, racing), ShouldBeNil)
				<-done
				So(calls, ShouldResemble, []any{"suspended data"})
				So(tos, ShouldResemble, []ItemState{ItemStateReady})
				So(resumedState, ShouldEqual, ItemStateSuspended)
				So(reserved, ShouldHaveLength, 2)
				So(reserved, ShouldContain, rwSuspended)
			})

			Convey("ResumeWith of a suspended item with an unresolved dependency calls back that it becomes dependent",
				func() {
					So(queue.ResumeWith(ctx, rwWaiting, resuming(rwWaiting)), ShouldBeNil)
					So(calls, ShouldResemble, []any{"waiting data"})
					So(tos, ShouldResemble, []ItemState{ItemStateDependent})
					So(resumedState, ShouldEqual, ItemStateSuspended)

					item, errg := queue.Get(rwWaiting)
					So(errg, ShouldBeNil)
					So(item.Stats().State, ShouldEqual, ItemStateDependent)
				})

			Convey("ResumeWith of an item that is not suspended fails without calling back", func() {
				errr := queue.ResumeWith(ctx, rwReady, resuming(rwReady))

				var qerr Error
				So(errors.As(errr, &qerr), ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotSuspended)
				So(calls, ShouldBeEmpty)
			})

			Convey("ResumeWith of a missing item fails without calling back", func() {
				errr := queue.ResumeWith(ctx, "missing", func(data any, to ItemState) {
					calls = append(calls, data)
				})

				var qerr Error
				So(errors.As(errr, &qerr), ShouldBeTrue)
				So(qerr.Err, ShouldEqual, ErrNotSuspended)
				So(calls, ShouldBeEmpty)
			})
		})
}
