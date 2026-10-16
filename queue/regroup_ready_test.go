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
	"fmt"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const rgKeyReserved = "k3"

const (
	rgOrderItems      = 100
	rgOrderPriorities = 7
)

// TestRegroupReady proves RegroupReady moves only ready items, to the group
// its callback returns, in one hold of the queue's lock.
func TestRegroupReady(t *testing.T) {
	ctx := context.Background()

	Convey("Given a reserved item k3, then ready items k and k2, all of group g1", t, func() {
		q := New(ctx, "regroup ready queue")
		defer qdestroy(q)

		_, err := q.Add(ctx, rgKeyReserved, rhGroup1, rgKeyReserved, 0, 0, rhTTR, "")
		So(err, ShouldBeNil)

		reserved, err := q.Reserve(rhGroup1, 0)
		So(err, ShouldBeNil)
		So(reserved.Key, ShouldEqual, rgKeyReserved)

		for _, key := range []string{rhKey, rhKey2} {
			_, err = q.Add(ctx, key, rhGroup1, key, 0, 0, rhTTR, "")
			So(err, ShouldBeNil)
		}

		var calls []hookCall

		toG2ForK := func(data any, group string) string {
			calls = append(calls, hookCall{data, group})

			if data == rhKey {
				return rhGroup2
			}

			return group
		}

		Convey("it calls back for the ready items only and moves those whose group changes", func() {
			moved, errr := q.RegroupReady([]string{rhKey, rhKey2, rgKeyReserved, "missing"}, toG2ForK)
			So(errr, ShouldBeNil)
			So(moved, ShouldEqual, 1)
			So(calls, ShouldResemble, []hookCall{{rhKey, rhGroup1}, {rhKey2, rhGroup1}})

			So(reserved.ReserveGroup, ShouldEqual, rhGroup1)

			item, errr := q.Reserve(rhGroup1, 0)
			So(errr, ShouldBeNil)
			So(item.Key, ShouldEqual, rhKey2)

			item, errr = q.Reserve(rhGroup2, 0)
			So(errr, ShouldBeNil)
			So(item.Key, ShouldEqual, rhKey)
		})

		Convey("a reserve waiting on the new group gets the moved item", func() {
			got := make(chan string, 1)

			go func() {
				item, errr := q.Reserve(rhGroup2, 2*time.Second)
				if errr != nil {
					got <- ""

					return
				}

				got <- item.Key
			}()

			<-time.After(50 * time.Millisecond)

			_, err = q.RegroupReady([]string{rhKey}, toG2ForK)
			So(err, ShouldBeNil)

			select {
			case key := <-got:
				So(key, ShouldEqual, rhKey)
			case <-time.After(200 * time.Millisecond):
				So("reserve did not return", ShouldBeEmpty)
			}
		})

		Convey("the callback runs with the queue locked", func() {
			returned := make(chan struct{})
			blocked := false

			_, err = q.RegroupReady([]string{rhKey}, func(_ any, group string) string {
				go func() {
					q.HasReady(rhGroup1)
					close(returned)
				}()

				select {
				case <-returned:
				case <-time.After(100 * time.Millisecond):
					blocked = true
				}

				return group
			})
			So(err, ShouldBeNil)
			So(blocked, ShouldBeTrue)
			<-returned
		})

		Convey("a key given twice moves its item once", func() {
			moved, errr := q.RegroupReady([]string{rhKey, rhKey}, func(_ any, group string) string {
				return group + "x"
			})
			So(errr, ShouldBeNil)
			So(moved, ShouldEqual, 1)

			item, errr := q.Reserve(rhGroup1+"x", 0)
			So(errr, ShouldBeNil)
			So(item.Key, ShouldEqual, rhKey)
		})

		Convey("a destroyed queue errors without calling back", func() {
			So(q.Destroy(), ShouldBeNil)

			_, err = q.RegroupReady([]string{rhKey}, toG2ForK)

			var qerr Error
			So(errors.As(err, &qerr), ShouldBeTrue)
			So(qerr.Err, ShouldEqual, ErrQueueClosed)
			So(calls, ShouldBeEmpty)
		})
	})

	Convey("Given 100 ready items of group g1 with mixed priorities", t, func() {
		q := New(ctx, "regroup ready order queue")
		defer qdestroy(q)

		keys := make([]string, 0, rgOrderItems)

		for i := range rgOrderItems {
			key := fmt.Sprintf("k%d", i)
			keys = append(keys, key)

			_, err := q.Add(ctx, key, rhGroup1, key, uint8((i*37)%rgOrderPriorities), 0, rhTTR, "")
			So(err, ShouldBeNil)
		}

		for _, n := range []int{1, 60, rgOrderItems} {
			Convey(fmt.Sprintf("after moving %d of them to g2, both groups reserve in priority order", n), func() {
				toMove := make(map[any]bool, n)
				for _, key := range keys[:n] {
					toMove[key] = true
				}

				moved, err := q.RegroupReady(keys, func(data any, group string) string {
					if toMove[data] {
						return rhGroup2
					}

					return group
				})
				So(err, ShouldBeNil)
				So(moved, ShouldEqual, n)

				g1, g1Ordered := rgReserveAll(q, rhGroup1)
				g2, g2Ordered := rgReserveAll(q, rhGroup2)

				So(g1, ShouldEqual, rgOrderItems-n)
				So(g2, ShouldEqual, n)
				So(g1Ordered, ShouldBeTrue)
				So(g2Ordered, ShouldBeTrue)
			})
		}

		Convey("after moving 10 of them to g2 and then 50 more, which rebuild g2, both groups reserve in "+
			"priority order", func() {
			toG2 := func(any, string) string { return rhGroup2 }

			moved, err := q.RegroupReady(keys[:10], toG2)
			So(err, ShouldBeNil)
			So(moved, ShouldEqual, 10)

			So(manyOf(50, 60), ShouldBeTrue)

			moved, err = q.RegroupReady(keys[10:60], toG2)
			So(err, ShouldBeNil)
			So(moved, ShouldEqual, 50)

			g1, g1Ordered := rgReserveAll(q, rhGroup1)
			g2, g2Ordered := rgReserveAll(q, rhGroup2)

			So(g1, ShouldEqual, rgOrderItems-60)
			So(g2, ShouldEqual, 60)
			So(g1Ordered, ShouldBeTrue)
			So(g2Ordered, ShouldBeTrue)
		})

		Convey("after moving 60 of them to g2 and then 1 more, both groups reserve in priority order", func() {
			toG2 := func(any, string) string { return rhGroup2 }

			moved, err := q.RegroupReady(keys[:60], toG2)
			So(err, ShouldBeNil)
			So(moved, ShouldEqual, 60)

			moved, err = q.RegroupReady(keys[60:61], toG2)
			So(err, ShouldBeNil)
			So(moved, ShouldEqual, 1)

			g1, g1Ordered := rgReserveAll(q, rhGroup1)
			g2, g2Ordered := rgReserveAll(q, rhGroup2)

			So(g1, ShouldEqual, rgOrderItems-61)
			So(g2, ShouldEqual, 61)
			So(g1Ordered, ShouldBeTrue)
			So(g2Ordered, ShouldBeTrue)
		})
	})
}

// rgReserveAll reserves every ready item of group, and returns how many there
// were and whether they came out highest priority first, oldest first within a
// priority.
func rgReserveAll(q *Queue, group string) (int, bool) {
	n, ordered := 0, true

	var last *Item

	for {
		item, err := q.Reserve(group, 0)
		if err != nil {
			return n, ordered
		}

		if last != nil && lessByPriority(item, last) {
			ordered = false
		}

		last = item
		n++
	}
}
