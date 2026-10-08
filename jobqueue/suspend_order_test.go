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

// This file covers a resume racing the suspend of the job it resumes: once the
// suspend has moved the job's item to the suspended sub-queue, a resume can move
// it back before the suspend has updated the job. The suspend must not then set
// that resumed job's State to suspended, or the item is ready while its job, and
// the job's stored record, say it is suspended. It also covers a suspend that
// fails after preparing its write, which must change nothing and let the
// manager stop.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// TestSuspendRacingResumeKeepsResume proves that a job resumed as soon as its
// suspend has moved its item stays ready, in memory, in its stored record and
// after a crash.
func TestSuspendRacingResumeKeepsResume(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a ready job resumed as soon as its suspend has moved its item", t, func() {
		d := dgrStartServer(ctx)
		serverStopped := false

		defer func() {
			if !serverStopped {
				d.stop(ctx)
			}
		}()

		jq := d.connect()
		defer disconnect(jq)

		target := d.job("echo suspend order", "suspend-order")
		dgrAddJobs(jq, []*Job{target})
		key := target.Key()

		// a job parked out of the way, whose durable write flushes the suspend's
		// and resume's.
		other := d.job("echo suspend order other", "suspend-order-other")
		dgrAddJobs(jq, []*Job{other})
		_, err := jq.Suspend([]*JobEssence{{JobKey: other.Key()}})
		So(err, ShouldBeNil)

		otherItem, err := d.server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		resumer := d.connect()
		defer disconnect(resumer)

		var (
			resumed   int
			resumeErr error
		)

		suspendQueuedHook = func(hooked string) {
			if hooked == key {
				resumed, resumeErr = resumer.Resume([]*JobEssence{{JobKey: key}})
			}
		}
		defer func() { suspendQueuedHook = nil }()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		suspendQueuedHook = nil

		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		So(resumeErr, ShouldBeNil)
		So(resumed, ShouldEqual, 1)

		item, err := d.server.q.Get(key)
		So(err, ShouldBeNil)
		So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

		// a durable write queued after the suspend's and resume's commits no
		// earlier than them.
		So(d.server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

		Convey("the job and its stored record say it is ready", func() {
			job, isJob := item.Data().(*Job)
			So(isJob, ShouldBeTrue)

			job.RLock()
			state := job.State
			job.RUnlock()

			So(state, ShouldEqual, JobStateReady)
			So(storedLiveJobState(t, d.server.db, key), ShouldEqual, JobStateReady)
		})

		Convey("a crash recovers it ready", func() {
			serverStopped = true

			rdrCrash(ctx, d, func() {})

			recoveredJQ := d.connect()
			defer disconnect(recoveredJQ)

			recovered, errg := recoveredJQ.GetByEssence(&JobEssence{JobKey: key}, false, false)
			So(errg, ShouldBeNil)
			So(recovered, ShouldNotBeNil)
			So(recovered.State, ShouldEqual, JobStateReady)
		})
	})
}

// TestFailedSuspendLeavesJobAndStops proves that a suspend of a job reserved
// after its write was prepared changes nothing and gives back what the prepare
// took: the job stays reserved in memory and in its stored record, and the
// manager still stops, which a leaked write slot would block forever.
func TestFailedSuspendLeavesJobAndStops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a ready job reserved once its suspend has prepared its write", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		serverStopped := false

		defer func() {
			if !serverStopped {
				server.Stop(ctx, true)
			}
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		target := &Job{
			Cmd: restFormTrue + " suspendfail", Cwd: testCwd, RepGroup: "suspend-fail",
			ReqGroup: "suspend-fail", Requirements: standardReqs,
		}
		other := &Job{
			Cmd: restFormTrue + " suspendfail other", Cwd: testCwd, RepGroup: "suspend-fail-other",
			ReqGroup: "suspend-fail-other", Requirements: standardReqs,
		}
		inserts, _, err := jq.Add([]*Job{target, other}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		key := target.Key()

		// a job parked out of the way, whose durable write flushes the
		// reservation's.
		_, err = jq.Suspend([]*JobEssence{{JobKey: other.Key()}})
		So(err, ShouldBeNil)

		otherItem, err := server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		var (
			reserved   *Job
			reserveErr error
		)

		jobChangeAheadHook = func(hooked string) {
			if hooked == key {
				reserved, reserveErr = runner.Reserve(2 * time.Second)
			}
		}
		defer func() { jobChangeAheadHook = nil }()

		Convey("suspending it suspends nothing, leaves it reserved, and the manager stops", func() {
			suspended, errs := jq.Suspend([]*JobEssence{{JobKey: key}})
			jobChangeAheadHook = nil

			So(errs, ShouldBeNil)
			So(reserveErr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, key)
			So(suspended, ShouldEqual, 0)
			So(item.Stats().State, ShouldEqual, queue.ItemStateRun)

			sjob.RLock()
			state := sjob.State
			sjob.RUnlock()

			So(state, ShouldEqual, JobStateReserved)

			// a durable write queued after the reservation's commits no earlier
			// than it.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)
			So(storedLiveJobState(t, server.db, key), ShouldEqual, JobStateReserved)

			serverStopped = true

			So(stopsPromptly(ctx, server), ShouldBeTrue)
		})
	})
}
