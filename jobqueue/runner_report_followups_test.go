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

// This file covers .docs/bugfixes/260930-runner-report-followups.md: reports
// from a job's owning runner that arrive after the manager has already given up
// on its run.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// TestOldOwnerReportAfterNewReservation proves that an old owner's release or
// bury, accepted as the owner's, leaves alone a new run that reserved the job
// before the report acted on it.
func TestOldOwnerReportAfterNewReservation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a started job the manager released as lost, which the user suspended and resumed", t, func() {
		f := newReleaseAfterLostFixture(ctx, t, 3, definitelyDeadPid(t), "")
		defer f.stop(ctx)

		setServerJobRunnerPid(f.server, f.job.Key(), definitelyDeadPid(t))
		So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)
		So(f.waitForManagerRelease(), ShouldBeTrue)

		suspended, err := f.user.Suspend(f.essence())
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		resumed, err := f.user.Resume(f.essence())
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)
		So(f.itemState(), ShouldEqual, queue.ItemStateReady)

		newRunner := f.connect()
		defer disconnect(newRunner)

		// the new runner reserves the job after the old owner's report has been
		// accepted and before it acts.
		var once sync.Once

		var newReservation *Job

		var reserveErr error

		releaseReportAcceptedHook = func(key string) {
			if key != f.job.Key() {
				return
			}

			once.Do(func() {
				newReservation, reserveErr = newRunner.ReserveScheduled(2*time.Second, "")
			})
		}
		defer func() { releaseReportAcceptedHook = nil }()

		assertNewRunLeftAlone := func(err error) {
			So(reserveErr, ShouldBeNil)
			So(newReservation, ShouldNotBeNil)
			So(newReservation.Key(), ShouldEqual, f.job.Key())

			So(f.itemState(), ShouldEqual, queue.ItemStateRun)

			var jqerr Error

			So(errors.As(err, &jqerr), ShouldBeTrue)
			So(jqerr.Err, ShouldEqual, ErrMustReserve)

			item, errg := f.server.q.Get(f.job.Key())
			So(errg, ShouldBeNil)

			job, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			job.RLock()
			defer job.RUnlock()

			So(job.ReservedBy, ShouldEqual, newRunner.clientid)
			So(job.State, ShouldEqual, JobStateReserved)
			So(job.UntilBuried, ShouldEqual, f.retries)
		}

		Convey("the old owner's bury gets ErrMustReserve and the new run is not buried", func() {
			assertNewRunLeftAlone(f.runner.Bury(f.job, releaseAfterLostEndState(), FailReasonExit))
		})

		Convey("the old owner's release gets ErrMustReserve and the new run is not released", func() {
			assertNewRunLeftAlone(f.runner.releaseAfterAttempt(f.job, releaseAfterLostEndState(), FailReasonExit))
		})
	})
}

// TestOldOwnerReportDuringNewReservation proves that an old owner's release,
// bury or archive leaves alone a new run whose reservation has moved the job's
// item to the run sub-queue but not yet given the job to its new runner.
func TestOldOwnerReportDuringNewReservation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a started job the manager released as lost, which the user suspended and resumed", t, func() {
		f := newReleaseAfterLostFixture(ctx, t, 3, definitelyDeadPid(t), "")
		defer f.stop(ctx)

		setServerJobRunnerPid(f.server, f.job.Key(), definitelyDeadPid(t))
		So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)
		So(f.waitForManagerRelease(), ShouldBeTrue)

		suspended, err := f.user.Suspend(f.essence())
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		resumed, err := f.user.Resume(f.essence())
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)
		So(f.itemState(), ShouldEqual, queue.ItemStateReady)

		newRunner := f.connect()
		defer disconnect(newRunner)

		// the old owner reports while the new reservation is part way through.
		assertNewRunLeftAlone := func(report func() error) {
			var once sync.Once

			var reportErr error

			reported := false

			reservationQueuedHook = func(key string) {
				if key != f.job.Key() {
					return
				}

				once.Do(func() {
					reportErr = report()
					reported = true
				})
			}
			defer func() { reservationQueuedHook = nil }()

			newReservation, errr := newRunner.ReserveScheduled(2*time.Second, "")
			So(errr, ShouldBeNil)
			So(newReservation, ShouldNotBeNil)
			So(newReservation.Key(), ShouldEqual, f.job.Key())
			So(reported, ShouldBeTrue)

			So(f.itemState(), ShouldEqual, queue.ItemStateRun)

			var jqerr Error

			So(errors.As(reportErr, &jqerr), ShouldBeTrue)
			So(jqerr.Err, ShouldEqual, ErrMustReserve)

			item, errg := f.server.q.Get(f.job.Key())
			So(errg, ShouldBeNil)

			job, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			job.RLock()
			defer job.RUnlock()

			So(job.ReservedBy, ShouldEqual, newRunner.clientid)
			So(job.State, ShouldEqual, JobStateReserved)
			So(job.UntilBuried, ShouldEqual, f.retries)
		}

		Convey("the old owner's bury gets ErrMustReserve and the new run is not buried", func() {
			assertNewRunLeftAlone(func() error {
				return f.runner.Bury(f.job, releaseAfterLostEndState(), FailReasonExit)
			})
		})

		Convey("the old owner's release gets ErrMustReserve and the new run is not released", func() {
			assertNewRunLeftAlone(func() error {
				return f.runner.releaseAfterAttempt(f.job, releaseAfterLostEndState(), FailReasonExit)
			})
		})

		Convey("the old owner's archive gets ErrMustReserve and the new run is not archived", func() {
			assertNewRunLeftAlone(func() error {
				return f.runner.Archive(f.job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			})

			complete, errc := f.server.db.checkIfComplete(f.job.Key())
			So(errc, ShouldBeNil)
			So(complete, ShouldBeFalse)
		})
	})
}
