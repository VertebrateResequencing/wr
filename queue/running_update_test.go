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

// errBackNotReady is what releaseToReady returns if its released item does not
// become ready again.
var errBackNotReady = errors.New("released item did not become ready again")

const (
	// leftReadyRun, leftReadyBury and leftReadyBack name the items
	// TestGetRunningDataAndLeftReady's callback leaves running, buries, and
	// returns to ready; leftReadyRunData and leftReadyBackData are the data of
	// the first and the last.
	leftReadyRun      = "run"
	leftReadyBury     = "bury"
	leftReadyBack     = "back"
	leftReadyRunData  = "run data"
	leftReadyBackData = "back data"
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

// leftReadyResult is what a ready-added callback in TestGetRunningDataAndLeftReady
// reports.
type leftReadyResult struct {
	running []any
	left    []any
	err     error
}

// TestGetRunningDataAndLeftReady covers the call that gets the running items'
// data and those of the items that left ready during a ready-added callback.
func TestGetRunningDataAndLeftReady(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with five ready items in their own reserve groups", t, func() {
		q := New(ctx, "running_left_ready")

		defer func() { So(q.Destroy(), ShouldBeNil) }()

		for _, name := range []string{leftReadyRun, leftReadyBury, "remove", "untouched", leftReadyBack} {
			_, err := q.Add(ctx, name, name, name+" data", 0, 0, time.Hour, "")
			So(err, ShouldBeNil)
		}

		Convey("outside a ready-added callback, including after one has finished, nothing is reported "+
			"as having left ready", func() {
			q.SetReadyAddedCallback(func(string, []any) {})
			q.TriggerReadyAddedCallback(ctx)
			So(pollQueue(func() bool { return !q.ReadyAddedCallbackRunning() }), ShouldBeTrue)

			_, err := q.Reserve(leftReadyRun, 0)
			So(err, ShouldBeNil)

			running, left := q.GetRunningDataAndLeftReady()
			So(running, ShouldResemble, []any{leftReadyRunData})
			So(left, ShouldBeEmpty)
		})

		Convey("a ready-added callback is told of the items that left ready during it and are not "+
			"back, and the next callback is told only of its own", func() {
			results := make(chan leftReadyResult, 10)
			calls := 0

			q.SetReadyAddedCallback(func(_ string, _ []any) {
				calls++
				if calls > 1 {
					running, left := q.GetRunningDataAndLeftReady()
					results <- leftReadyResult{running: running, left: left}

					return
				}

				results <- leftReadyResult{err: leaveReadyInCallback(ctx, q)}

				running, left := q.GetRunningDataAndLeftReady()
				results <- leftReadyResult{running: running, left: left}
			})
			q.TriggerReadyAddedCallback(ctx)

			So((<-results).err, ShouldBeNil)

			first := <-results
			So(first.running, ShouldResemble, []any{leftReadyRunData})
			So(first.left, ShouldHaveLength, 3)
			So(first.left, ShouldContain, leftReadyRunData)
			So(first.left, ShouldContain, "bury data")
			So(first.left, ShouldContain, "remove data")

			// the release back to ready may already have asked for the next
			// callback; if not, this does.
			q.TriggerReadyAddedCallback(ctx)

			second := <-results
			So(second.running, ShouldResemble, []any{leftReadyRunData})
			So(second.left, ShouldBeEmpty)
		})

		Convey("an item that leaves ready twice during a ready-added callback is reported once", func() {
			results := make(chan leftReadyResult, 10)
			calls := 0

			q.SetReadyAddedCallback(func(_ string, _ []any) {
				calls++
				if calls > 1 {
					return
				}

				results <- leftReadyResult{err: leaveReadyTwiceInCallback(ctx, q)}

				running, left := q.GetRunningDataAndLeftReady()
				results <- leftReadyResult{running: running, left: left}
			})
			q.TriggerReadyAddedCallback(ctx)

			So((<-results).err, ShouldBeNil)

			result := <-results
			So(result.running, ShouldResemble, []any{leftReadyBackData})
			So(result.left, ShouldResemble, []any{leftReadyBackData})
		})
	})
}

// pollQueue reports whether check returns true within 5 seconds.
func pollQueue(check func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if check() {
			return true
		}

		time.Sleep(10 * time.Millisecond)
	}

	return false
}

// leaveReadyInCallback takes the "run" item to run, the "bury" item to buried,
// removes the "remove" item, and takes the "back" item to run and releases it,
// waiting until it is ready again.
func leaveReadyInCallback(ctx context.Context, q *Queue) error {
	for _, name := range []string{leftReadyRun, leftReadyBury, leftReadyBack} {
		if _, err := q.Reserve(name, 0); err != nil {
			return err
		}
	}

	if err := q.Bury(leftReadyBury); err != nil {
		return err
	}

	if err := q.Remove(ctx, "remove"); err != nil {
		return err
	}

	return releaseToReady(ctx, q, leftReadyBack)
}

// releaseToReady releases the running item key and waits until it is ready
// again.
func releaseToReady(ctx context.Context, q *Queue, key string) error {
	if err := q.Release(ctx, key); err != nil {
		return err
	}

	backReady := pollQueue(func() bool {
		item, err := q.Get(key)

		return err == nil && item.State() == ItemStateReady
	})
	if !backReady {
		return errBackNotReady
	}

	return nil
}

// leaveReadyTwiceInCallback takes the "back" item to run, releases it, waits
// until it is ready again, and takes it to run again.
func leaveReadyTwiceInCallback(ctx context.Context, q *Queue) error {
	if _, err := q.Reserve(leftReadyBack, 0); err != nil {
		return err
	}

	if err := releaseToReady(ctx, q, leftReadyBack); err != nil {
		return err
	}

	_, err := q.Reserve(leftReadyBack, 0)

	return err
}
