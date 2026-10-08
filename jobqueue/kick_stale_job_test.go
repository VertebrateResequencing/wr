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

package jobqueue

// This file covers a kick whose caller looked the buried job up before an add
// replaced the job its queue item holds. The kick must kick, and write, the job
// the item holds when it kicks it, not the one its caller found.

import (
	"context"
	"testing"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const kickStaleNewRepGroup = "kick-stale-job-new"

// TestKickOfReplacedJobKicksItemsJob proves that a kick of a buried job whose
// item's job was replaced, after the kick's caller looked it up, by an add that
// re-runs it, leaves the replacement kicked and stored: a waiter brought back
// complete by a dep-group re-run and buried as impossible is re-run by an add
// under a new rep group while a kick of it is under way.
func TestKickOfReplacedJobKicksItemsJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried complete waiter that an add re-runs while it is being kicked", t, func() {
		d := dgrStartServer(ctx)
		serverStopped := false

		defer func() {
			if !serverStopped {
				d.stop(ctx)
			}
		}()

		jq := d.connect()
		defer disconnect(jq)

		const group = "kick-stale-job-group"

		first := dgaMemberJob(d, group, "kick stale first")
		waiter := dgaWaiterJob(d, group, "kick stale waiter")
		key := waiter.Key()

		dgrAddJobs(jq, []*Job{first})
		dgrAddJobs(jq, []*Job{waiter})
		dgaExecuteReserved(ctx, d, jq, first.Key())
		dgaExecuteReserved(ctx, d, jq, key)

		// a new member of the waiter's group brings it back from the complete
		// bucket, still complete, to wait for that member.
		second := dgaMemberJob(d, group, "kick stale second")
		_, _, err := jq.Add([]*Job{second}, envVars, true)
		So(err, ShouldBeNil)
		So(dgaItemState(d.server, key), ShouldEqual, queue.ItemStateDependent)
		dgaExecuteReserved(ctx, d, jq, second.Key())
		So(dgaItemState(d.server, key), ShouldEqual, queue.ItemStateReady)

		// scheduling finds its requirements impossible and buries it.
		item, err := d.server.q.Get(key)
		So(err, ShouldBeNil)

		reserved, err := d.server.q.Reserve(item.ReserveGroup, 0)
		So(err, ShouldBeNil)
		So(reserved.Key, ShouldEqual, key)
		d.server.buryImpossibleItem(ctx, reserved)
		So(dgaItemState(d.server, key), ShouldEqual, queue.ItemStateBury)

		looked, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		looked.RLock()
		lookedState := looked.State
		looked.RUnlock()
		So(lookedState, ShouldEqual, JobStateComplete)

		// a job parked out of the way, whose durable write flushes the kick's.
		other := d.job("echo kick stale other", kickStaleNewRepGroup+"-other")
		dgrAddJobs(jq, []*Job{other})
		_, err = jq.Suspend([]*JobEssence{{JobKey: other.Key()}})
		So(err, ShouldBeNil)

		otherItem, err := d.server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		adder := d.connect()
		defer disconnect(adder)

		rerun := dgaWaiterJob(d, group, "kick stale waiter")
		rerun.RepGroup = kickStaleNewRepGroup
		So(rerun.Key(), ShouldEqual, key)

		var (
			addErr   error
			addCount int
		)

		// the add lands after the kick's caller has looked the job up and before
		// the kick takes the item out of the bury sub-queue.
		jobChangeAheadHook = func(hooked string) {
			if hooked == key {
				addCount, _, addErr = adder.Add([]*Job{rerun}, envVars, false)
			}
		}
		defer func() { jobChangeAheadHook = nil }()

		kicked, err := jq.Kick([]*JobEssence{{JobKey: key}})
		jobChangeAheadHook = nil

		So(err, ShouldBeNil)
		So(addErr, ShouldBeNil)
		So(addCount, ShouldEqual, 1)
		So(kicked, ShouldEqual, 1)

		item, err = d.server.q.Get(key)
		So(err, ShouldBeNil)
		So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

		live, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)
		// compared directly: ShouldNotPointTo formats the jobs, reading them
		// without their locks.
		So(live != looked, ShouldBeTrue)

		// a durable write queued after the kick's commits no earlier than it.
		So(d.server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

		Convey("the job the item holds is the one kicked", func() {
			live.RLock()
			state := live.State
			live.RUnlock()

			So(state, ShouldEqual, JobStateReady)
			So(storedLiveJobState(t, d.server.db, key), ShouldEqual, JobStateReady)

			live.RLock()
			liveAhead := live.changesAhead
			live.RUnlock()

			looked.RLock()
			lookedAhead := looked.changesAhead
			looked.RUnlock()

			So(liveAhead, ShouldEqual, 0)
			So(lookedAhead, ShouldEqual, 0)
		})

		Convey("a crash recovers the re-added job ready under its new rep group", func() {
			serverStopped = true

			rdrCrash(ctx, d, func() {})

			recoveredJQ := d.connect()
			defer disconnect(recoveredJQ)

			recovered, errg := recoveredJQ.GetByEssence(&JobEssence{JobKey: key}, false, false)
			So(errg, ShouldBeNil)
			So(recovered, ShouldNotBeNil)
			So(recovered.RepGroup, ShouldEqual, kickStaleNewRepGroup)
			So(recovered.State, ShouldEqual, JobStateReady)
		})
	})
}
