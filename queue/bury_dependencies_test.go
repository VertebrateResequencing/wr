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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	bsqKey = "buried"
	bsqDep = "dep"
)

// TestBuryStartQueueDependencySatisfiedWhileBuried proves an item added to the
// bury sub-queue with dependencies stays buried when they are satisfied, as a
// live buried item does, and that a kick then makes it ready.
func TestBuryStartQueueDependencySatisfiedWhileBuried(t *testing.T) {
	ctx := context.Background()

	synctestConvey(t, "Satisfying the dependencies of an item added buried leaves it buried", func() {
		queue := New(ctx, "bury deps satisfied queue")
		defer qdestroy(queue)

		_, _, err := queue.AddMany(ctx, []*ItemDef{{
			Key: bsqKey, Data: testData, TTR: time.Minute,
			StartQueue: SubQueueBury, Dependencies: []string{bsqDep},
		}})
		So(err, ShouldBeNil)

		So(queue.SatisfyDependency(ctx, bsqDep), ShouldBeNil)

		item, err := queue.Get(bsqKey)
		So(err, ShouldBeNil)
		So(item.Stats().State, ShouldEqual, ItemStateBury)
		So(queue.Stats().Buried, ShouldEqual, 1)

		So(queue.Kick(ctx, bsqKey), ShouldBeNil)
		So(item.Stats().State, ShouldEqual, ItemStateReady)
	})
}

// TestAddManyBuryStartQueueWithDependencies proves an item AddMany()s to the
// bury sub-queue with dependencies, as a manager recovering a buried job does,
// is buried rather than dependent, and that a kick then makes it wait on them.
func TestAddManyBuryStartQueueWithDependencies(t *testing.T) {
	ctx := context.Background()

	synctestConvey(t, "AddMany with StartQueue bury and dependencies buries the item", func() {
		queue := New(ctx, "bury deps addmany queue")
		defer qdestroy(queue)

		added, dups, err := queue.AddMany(ctx, []*ItemDef{{
			Key: bsqKey, Data: testData, TTR: time.Minute,
			StartQueue: SubQueueBury, Dependencies: []string{bsqDep},
		}})
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(dups, ShouldEqual, 0)

		bsqSoBuriedThenKickWaits(ctx, queue)
	})
}

// TestAddBuryStartQueueWithDependencies is TestAddManyBuryStartQueueWithDependencies
// for Add().
func TestAddBuryStartQueueWithDependencies(t *testing.T) {
	ctx := context.Background()

	synctestConvey(t, "Add with startQueue bury and dependencies buries the item", func() {
		queue := New(ctx, "bury deps add queue")
		defer qdestroy(queue)

		_, err := queue.Add(ctx, bsqKey, "", testData, 0, 0, time.Minute, SubQueueBury, []string{bsqDep})
		So(err, ShouldBeNil)

		bsqSoBuriedThenKickWaits(ctx, queue)
	})
}

// bsqSoBuriedThenKickWaits asserts the item is buried and counted as buried,
// kicks it, and asserts it then waits on its dependency, becoming ready once
// that is satisfied.
func bsqSoBuriedThenKickWaits(ctx context.Context, queue *Queue) {
	item, err := queue.Get(bsqKey)
	So(err, ShouldBeNil)
	So(item.Stats().State, ShouldEqual, ItemStateBury)
	So(queue.Stats().Buried, ShouldEqual, 1)
	So(queue.Stats().Dependant, ShouldEqual, 0)

	So(queue.Kick(ctx, bsqKey), ShouldBeNil)
	So(item.Stats().State, ShouldEqual, ItemStateDependent)
	So(item.UnresolvedDependencies(), ShouldResemble, []string{bsqDep})

	So(queue.SatisfyDependency(ctx, bsqDep), ShouldBeNil)
	So(item.Stats().State, ShouldEqual, ItemStateReady)
}
