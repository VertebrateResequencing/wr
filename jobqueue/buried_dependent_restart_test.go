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

// This file covers a buried job whose dep group gains an incomplete member: per
// .docs/dep-granularity/spec.md (B2), it stays buried with the new dependencies
// and only a kick makes it dependent, and a manager restart, clean or not, must
// not change that.

import (
	"context"
	"os"
	"testing"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	bdrGroup      = "buried-dependent-restart-group"
	bdrFirstName  = "first"
	bdrSecondName = "second"
	bdrWaiterName = "waiter"
)

// TestBuriedDependentRestart proves a buried job whose dep group gained an
// incomplete member is recovered buried after a manager restart, and that a kick
// then makes it wait on the new member.
func TestBuriedDependentRestart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job whose dep group gains a member after it was buried", t, func() {
		d := dgrStartServer(ctx)
		jq := d.connect()

		defer d.stop(ctx)
		defer disconnect(jq)

		member := dgaMemberJob(d, bdrGroup, bdrFirstName)
		waiter := dgaWaiterJob(d, bdrGroup, bdrWaiterName)

		dgrAddJobs(jq, []*Job{member})
		dgrAddJobs(jq, []*Job{waiter})
		dgaExecuteReserved(ctx, d, jq, member.Key())

		bdrReserveAndBury(d, jq, waiter.Key())

		second := dgaMemberJob(d, bdrGroup, bdrSecondName)
		dgrAddJobs(jq, []*Job{second})
		So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateBury)

		Convey("a clean restart leaves it buried, and a kick makes it wait on the new member", func() {
			d.restart(ctx)

			bdrSoBuriedThenKickWaits(ctx, d, waiter.Key(), second.Key())
		})

		Convey("a crash leaves it buried, and a kick makes it wait on the new member", func() {
			bdrCrash(ctx, d)

			bdrSoBuriedThenKickWaits(ctx, d, waiter.Key(), second.Key())
		})

		Convey("with the new member buried too, a clean restart leaves it buried", func() {
			bdrReserveAndBury(d, jq, second.Key())

			d.restart(ctx)

			runner := d.connect()
			defer disconnect(runner)

			bdrSoBuried(d, runner, waiter.Key(), 2)
		})
	})

	Convey("Given a job buried after its dep group gained a member while it was running", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		dgrAddJobs(jq, []*Job{second})

		So(jq.Bury(waiter, rdrFailedEnd(), "failed"), ShouldBeNil)
		So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateBury)

		Convey("a clean restart leaves it buried, and a kick makes it wait on the new member", func() {
			d.restart(ctx)

			bdrSoBuriedThenKickWaits(ctx, d, waiter.Key(), second.Key())
		})

		Convey("a crash leaves it buried, and a kick makes it wait on the new member", func() {
			bdrCrash(ctx, d)

			bdrSoBuriedThenKickWaits(ctx, d, waiter.Key(), second.Key())
		})
	})
}

// bdrReserveAndBury reserves the next ready job, which must be the one keyed,
// reports it started and buries it as a failure.
func bdrReserveAndBury(d *dgrServer, jq *Client, key string) {
	job, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)
	So(job.Key(), ShouldEqual, key)
	So(jq.Started(job, os.Getpid()), ShouldBeNil)
	So(jq.Bury(job, rdrFailedEnd(), "failed"), ShouldBeNil)
	So(dgaItemState(d.server, key), ShouldEqual, queue.ItemStateBury)
}

// bdrSoBuriedThenKickWaits asserts the recovered waiter is buried, kicks it and
// asserts it then waits on the new member, becoming ready once that completes.
func bdrSoBuriedThenKickWaits(ctx context.Context, d *dgrServer, waiterKey, secondKey string) {
	jq := d.connect()
	defer disconnect(jq)

	bdrSoBuried(d, jq, waiterKey, 1)

	kicked, err := jq.Kick([]*JobEssence{{JobKey: waiterKey}})
	So(err, ShouldBeNil)
	So(kicked, ShouldEqual, 1)
	So(dgaItemState(d.server, waiterKey), ShouldEqual, queue.ItemStateDependent)

	dgaExecuteReserved(ctx, d, jq, secondKey)
	So(dgaItemState(d.server, waiterKey), ShouldEqual, queue.ItemStateReady)
}

// bdrCrash crashes the manager once the bury it has accepted is committed, as a
// kill some time after the bury would find it.
func bdrCrash(ctx context.Context, d *dgrServer) {
	rdrCrash(ctx, d, d.server.db.waitForJobExitUpdates)
}

// bdrSoBuried asserts the keyed job is buried as its client sees it and as its
// queue item is held, and that the queue holds that many buried items.
func bdrSoBuried(d *dgrServer, jq *Client, key string, buried int) {
	got, err := jq.GetByEssence(&JobEssence{JobKey: key}, false, false)
	So(err, ShouldBeNil)
	So(got, ShouldNotBeNil)
	So(got.State, ShouldEqual, JobStateBuried)
	So(dgaItemState(d.server, key), ShouldEqual, queue.ItemStateBury)
	So(d.server.q.Stats().Buried, ShouldEqual, buried)
}
