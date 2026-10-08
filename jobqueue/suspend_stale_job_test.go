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

// This file covers a suspend that looked its job up before an add replaced the
// job its queue item holds. The suspend must suspend, and write, the job the
// item holds when it suspends it, not the one it found.

import (
	"context"
	"testing"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const suspendStaleNewRepGroup = "suspend-stale-job-new"

// TestSuspendOfReplacedJobSuspendsItemsJob proves that a suspend of a ready job
// whose item's job is replaced, by an add that re-runs it, while the suspend is
// under way leaves the replacement suspended and stored: a waiter brought back
// complete by a dep-group re-run, and made ready again, is re-run by an add
// under a new rep group between the suspend looking up its job and suspending
// its item.
func TestSuspendOfReplacedJobSuspendsItemsJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a ready complete waiter that an add re-runs while it is being suspended", t, func() {
		d := dgrStartServer(ctx)
		serverStopped := false

		defer func() {
			if !serverStopped {
				d.stop(ctx)
			}
		}()

		jq := d.connect()
		defer disconnect(jq)

		const group = "suspend-stale-job-group"

		first := dgaMemberJob(d, group, "suspend stale first")
		waiter := dgaWaiterJob(d, group, "suspend stale waiter")
		key := waiter.Key()

		dgrAddJobs(jq, []*Job{first})
		dgrAddJobs(jq, []*Job{waiter})
		dgaExecuteReserved(ctx, d, jq, first.Key())
		dgaExecuteReserved(ctx, d, jq, key)

		// a new member of the waiter's group brings it back from the complete
		// bucket, still complete, to wait for that member; once the member
		// completes it is ready, its job still complete.
		second := dgaMemberJob(d, group, "suspend stale second")
		_, _, err := jq.Add([]*Job{second}, envVars, true)
		So(err, ShouldBeNil)
		So(dgaItemState(d.server, key), ShouldEqual, queue.ItemStateDependent)
		dgaExecuteReserved(ctx, d, jq, second.Key())
		So(dgaItemState(d.server, key), ShouldEqual, queue.ItemStateReady)

		item, err := d.server.q.Get(key)
		So(err, ShouldBeNil)

		looked, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		looked.RLock()
		lookedState := looked.State
		looked.RUnlock()
		So(lookedState, ShouldEqual, JobStateComplete)

		// a job parked out of the way, whose durable write flushes the
		// suspend's.
		other := d.job("echo suspend stale other", suspendStaleNewRepGroup+"-other")
		dgrAddJobs(jq, []*Job{other})
		_, err = jq.Suspend([]*JobEssence{{JobKey: other.Key()}})
		So(err, ShouldBeNil)

		otherItem, err := d.server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		adder := d.connect()
		defer disconnect(adder)

		rerun := dgaWaiterJob(d, group, "suspend stale waiter")
		rerun.RepGroup = suspendStaleNewRepGroup
		So(rerun.Key(), ShouldEqual, key)

		var (
			addErr   error
			addCount int
		)

		// the add lands after the suspend has looked up the item's job, and
		// encoded its write, and before it suspends the item, while that job is
		// still complete.
		var added bool

		jobChangeAheadHook = func(hooked string) {
			if hooked == key && !added {
				added = true
				addCount, _, addErr = adder.Add([]*Job{rerun}, envVars, false)
			}
		}
		defer func() { jobChangeAheadHook = nil }()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		jobChangeAheadHook = nil

		So(added, ShouldBeTrue)

		So(err, ShouldBeNil)
		So(addErr, ShouldBeNil)
		So(addCount, ShouldEqual, 1)
		So(suspended, ShouldEqual, 1)

		item, err = d.server.q.Get(key)
		So(err, ShouldBeNil)
		So(item.Stats().State, ShouldEqual, queue.ItemStateSuspended)

		live, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)
		// compared directly: ShouldNotPointTo formats the jobs, reading them
		// without their locks.
		So(live != looked, ShouldBeTrue)

		// a durable write queued after the suspend's commits no earlier than it.
		So(d.server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

		Convey("the job the item holds is the one suspended, and is stored", func() {
			live.RLock()
			state := live.State
			live.RUnlock()

			So(state, ShouldEqual, JobStateSuspended)

			stored := storedLiveJob(t, d.server.db, key)
			So(stored.State, ShouldEqual, JobStateSuspended)
			So(stored.RepGroup, ShouldEqual, suspendStaleNewRepGroup)
		})

		Convey("a crash recovers the re-added job suspended under its new rep group", func() {
			serverStopped = true

			rdrCrash(ctx, d, func() {})

			recoveredJQ := d.connect()
			defer disconnect(recoveredJQ)

			recovered, errg := recoveredJQ.GetByEssence(&JobEssence{JobKey: key}, false, false)
			So(errg, ShouldBeNil)
			So(recovered, ShouldNotBeNil)
			So(recovered.RepGroup, ShouldEqual, suspendStaleNewRepGroup)
			So(recovered.State, ShouldEqual, JobStateSuspended)
		})
	})
}
