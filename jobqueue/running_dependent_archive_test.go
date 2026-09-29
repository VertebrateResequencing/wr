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

// This file covers the ways the archive of a successful run of a job can race
// what running_dependent.go does to make that job run again
// (.docs/bugfixes/260929-running-dependent-rerun.md), and the way two archives of
// one completion race an add that makes the job run again.

import (
	"context"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

// TestRunningDependentRerunArchiveRaces proves the archive of a job's successful
// run keeps its say over the job's stored record and queue item against a mark
// that it must run again arriving or being stored at any moment, and against an
// add that makes it run again while another archive of the same completion is in
// flight.
func TestRunningDependentRerunArchiveRaces(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	success := func() *JobEndState { return &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()} }

	Convey("Given a running job marked to run again because its dep group gained a member", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		dgrAddJobs(jq, []*Job{second})

		serverJob := rdaServerJob(d, waiter.Key())

		Convey("a late store of the mark, while or after its archive is written, leaves the archive's record", func() {
			key, repGroup, schedGroup, srerr := markJobComplete(serverJob, success(), d.server.limiter, jq.clientid)
			So(srerr, ShouldBeEmpty)

			outcome, err := d.server.db.archiveCompletion(key, serverJob)
			So(err, ShouldBeNil)
			So(outcome, ShouldEqual, archiveKeptLive)

			d.server.storeRerunMarks(ctx, rerunMarks{running: []*Job{serverJob}})
			rdaSoLiveRecordUnmarked(d, key)

			_, srerr, qerr := d.server.finishArchive(ctx, serverJob, key, repGroup, schedGroup, outcome, err)
			So(srerr, ShouldBeEmpty)
			So(qerr, ShouldBeEmpty)

			d.server.storeRerunMarks(ctx, rerunMarks{running: []*Job{serverJob}})
			rdaSoLiveRecordUnmarked(d, key)

			d.restart(ctx)

			runner := d.connect()
			defer disconnect(runner)

			rdaSoRunsOnceMore(ctx, d, runner, waiter.Key(), second.Key())
		})

		Convey("a mark left in its stored record once it is not running is not acted on after a crash", func() {
			So(jq.Archive(waiter, success()), ShouldBeNil)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			rdrCrash(ctx, d, func() { rdrSetStoredMark(d, waiter.Key(), true) })

			runner := d.connect()
			defer disconnect(runner)

			rdaSoRunsOnceMore(ctx, d, runner, waiter.Key(), second.Key())
		})
	})

	Convey("Given a running job that another job depends on directly", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() { dependentsReadHook = nil }()

		child := d.job("echo rda child", dgaWaiterRepGroup)
		child.Dependencies = Dependencies{NewEssenceDependency(waiter.Cmd, "")}
		dgrAddJobs(jq, []*Job{child})
		So(dgaItemState(d.server, child.Key()), ShouldEqual, queue.ItemStateDependent)

		serverJob := rdaServerJob(d, waiter.Key())

		Convey("if it is marked to run again after its archive's write, the dependent waits for it to run again", func() {
			key, repGroup, schedGroup, srerr := markJobComplete(serverJob, success(), d.server.limiter, jq.clientid)
			So(srerr, ShouldBeEmpty)

			var (
				outcome    archiveOutcome
				archiveErr error
			)

			dependentsReadHook = func() {
				dependentsReadHook = nil

				outcome, archiveErr = d.server.db.archiveCompletion(key, serverJob)
			}

			second := dgaMemberJob(d, rdrGroup, rdrSecondName)
			dgrAddJobs(jq, []*Job{second})
			So(archiveErr, ShouldBeNil)
			So(outcome, ShouldEqual, archiveRemovedLive)

			_, srerr, qerr := d.server.finishArchive(ctx, serverJob, key, repGroup, schedGroup, outcome, archiveErr)
			So(srerr, ShouldBeEmpty)
			So(qerr, ShouldBeEmpty)

			rdrSoCompleteRecorded(d, waiter.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
			So(dgaItemState(d.server, child.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			dgaExecuteReserved(ctx, d, jq, waiter.Key())
			So(dgaItemState(d.server, child.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})

	Convey("Given a running job with two archives of its one successful completion in flight", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		serverJob := rdaServerJob(d, waiter.Key())
		end := success()

		key, repGroup, schedGroup, srerr := markJobComplete(serverJob, end, d.server.limiter, jq.clientid)
		So(srerr, ShouldBeEmpty)

		_, _, _, srerr = markJobComplete(serverJob, end, d.server.limiter, jq.clientid)
		So(srerr, ShouldBeEmpty)

		_, srerr, qerr := d.server.archiveCompletedJob(ctx, serverJob, key, repGroup, schedGroup)
		So(srerr, ShouldBeEmpty)
		So(qerr, ShouldBeEmpty)

		_, errg := d.server.q.Get(waiter.Key())
		So(errg, ShouldNotBeNil)

		Convey("an add that makes it run again once the first finishes is not undone by the second", func() {
			second := dgaMemberJob(d, rdrGroup, rdrSecondName)

			inserts, _, erra := jq.Add([]*Job{second}, envVars, true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			_, srerr, qerr = d.server.archiveCompletedJob(ctx, serverJob, key, repGroup, schedGroup)
			So(srerr, ShouldBeEmpty)
			So(qerr, ShouldBeEmpty)

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			live, errl := d.server.db.checkIfLive(waiter.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			dgaExecuteReserved(ctx, d, jq, waiter.Key())

			_, errg = d.server.q.Get(waiter.Key())
			So(errg, ShouldNotBeNil)
		})
	})
}

// rdaServerJob returns the manager's in-memory job of the keyed queue item.
func rdaServerJob(d *dgrServer, key string) *Job {
	item, err := d.server.q.Get(key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	return job
}

// rdaSoLiveRecordUnmarked asserts the job's live record has neither the mark
// that it must run again nor a reservation.
func rdaSoLiveRecordUnmarked(d *dgrServer, key string) {
	encoded := d.server.db.retrieve(context.Background(), bucketJobsLive, key)
	So(encoded, ShouldNotBeEmpty)

	live, err := d.server.db.decodeJob(encoded)
	So(err, ShouldBeNil)
	So(live.RerunAfterRun, ShouldBeFalse)
	So(live.ReservedBy, ShouldEqual, uuid.UUID{})
}

// rdaSoRunsOnceMore asserts the waiter is waiting on second, and that once second
// completes it runs just once more.
func rdaSoRunsOnceMore(ctx context.Context, d *dgrServer, runner *Client, waiterKey, secondKey string) {
	So(dgaItemState(d.server, waiterKey), ShouldEqual, queue.ItemStateDependent)

	dgaExecuteReserved(ctx, d, runner, secondKey)
	So(dgaItemState(d.server, waiterKey), ShouldEqual, queue.ItemStateReady)

	rdrExecuteAsRunner(ctx, d, runner, waiterKey)

	_, errg := d.server.q.Get(waiterKey)
	So(errg, ShouldNotBeNil)
}
