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
	"os"
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

// TestOwnerEndStateAfterLostRelease proves that the owning runner's report of
// how its command ended is recorded, in memory and on disk, for a job the
// manager had already released as lost, which only recorded that it lost
// contact.
func TestOwnerEndStateAfterLostRelease(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a started job with 3 retries the manager confirmed dead and released itself", t, func() {
		f := newReleaseAfterLostFixture(ctx, t, 3, definitelyDeadPid(t), "")
		defer f.stop(ctx)

		setServerJobRunnerPid(f.server, f.job.Key(), definitelyDeadPid(t))
		So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)
		So(f.waitForManagerRelease(), ShouldBeTrue)

		assertOwnerEndStateAfterLostRelease(t, f)
	})

	Convey("Given a started lost job with 3 retries the user killed, so the manager released it", t, func() {
		f := newReleaseAfterLostFixture(ctx, t, 3, os.Getpid(), "")
		defer f.stop(ctx)

		So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)

		killed, err := f.user.Kill(f.essence())
		So(err, ShouldBeNil)
		So(killed, ShouldEqual, 1)
		So(f.waitForManagerRelease(), ShouldBeTrue)

		assertOwnerEndStateAfterLostRelease(t, f)
	})
}

// assertOwnerEndStateAfterLostRelease asserts that the owner's release or bury
// of f's job, which the manager has released as lost, records the owner's end
// state and fail reason, and that a re-send of the owner's release is not taken
// for a new one.
func assertOwnerEndStateAfterLostRelease(t *testing.T, f *releaseAfterLostFixture) {
	t.Helper()

	lost := f.finalJob()
	So(lost.Exitcode, ShouldEqual, -1)
	So(lost.FailReason, ShouldEqual, FailReasonLost)

	ownerEnd := &JobEndState{Exited: true, Exitcode: 3, PeakRAM: 1234, EndTime: time.Now()}

	assertOwnerEndRecorded := func(state JobState, untilBuried uint8) {
		for _, job := range []*Job{f.finalJob(), storedLiveJob(t, f.server.db, f.job.Key())} {
			So(job.State, ShouldBeIn, []JobState{state, JobStateReady})
			So(job.Exitcode, ShouldEqual, 3)
			So(job.PeakRAM, ShouldEqual, 1234)
			So(job.FailReason, ShouldEqual, FailReasonExit)
			So(job.UntilBuried, ShouldEqual, untilBuried)
		}
	}

	Convey("the owner's release records its exit code and peak RAM without spending another retry", func() {
		So(f.runner.releaseAfterAttempt(f.job, ownerEnd, FailReasonExit), ShouldBeNil)
		assertOwnerEndRecorded(JobStateDelayed, f.retries)

		Convey("and a re-send of it changes nothing", func() {
			resent := &JobEndState{Exited: true, Exitcode: 5, PeakRAM: 99, EndTime: time.Now()}
			So(f.runner.releaseAfterAttempt(f.job, resent, FailReasonExit), ShouldBeNil)
			assertOwnerEndRecorded(JobStateDelayed, f.retries)
		})
	})

	Convey("the owner's bury records its exit code and peak RAM", func() {
		So(f.runner.Bury(f.job, ownerEnd, FailReasonExit), ShouldBeNil)
		assertOwnerEndRecorded(JobStateBuried, 0)
	})
}

// TestOwnerBuryOfRequeuedDependent proves that the owning runner's bury of a
// job the manager released as lost into the dependent sub-queue, to wait on a
// new member of its dep group, leaves it buried: stop means buried.
func TestOwnerBuryOfRequeuedDependent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost running job the manager released to wait on a new member of its dep group", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		dgrAddJobs(jq, []*Job{second})

		item, err := d.server.q.Get(waiter.Key())
		So(err, ShouldBeNil)

		serverJob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		serverJob.Lock()
		serverJob.Lost = true
		serverJob.Unlock()

		killed, err := jq.Kill([]*JobEssence{waiter.ToEssense()})
		So(err, ShouldBeNil)
		So(killed, ShouldEqual, 1)
		So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

		Convey("the owner's bury is acknowledged and buries it, and a kick makes it wait on the new member", func() {
			So(jq.Bury(waiter, rdrFailedEnd(), FailReasonExit), ShouldBeNil)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateBury)
			So(storedLiveJobState(t, d.server.db, waiter.Key()), ShouldEqual, JobStateBuried)

			kicked, errk := jq.Kick([]*JobEssence{waiter.ToEssense()})
			So(errk, ShouldBeNil)
			So(kicked, ShouldEqual, 1)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
		})

		Convey("the owner's release is acknowledged and it still waits on the new member", func() {
			So(jq.releaseAfterAttempt(waiter, rdrFailedEnd(), FailReasonExit), ShouldBeNil)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
		})
	})
}

// TestResentReleaseAfterKick proves that a runner's re-sent release of a job
// its first release buried, which the user has since kicked, does not spend a
// retry from the kicked budget.
func TestResentReleaseAfterKick(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a started job with no retries its runner released, so it was buried, and the user kicked", t, func() {
		f := newReleaseAfterLostFixture(ctx, t, 0, os.Getpid(), "")
		defer f.stop(ctx)

		So(f.runner.releaseAfterAttempt(f.job, releaseAfterLostEndState(), FailReasonExit), ShouldBeNil)
		So(f.itemState(), ShouldEqual, queue.ItemStateBury)

		kicked, err := f.user.Kick(f.essence())
		So(err, ShouldBeNil)
		So(kicked, ShouldEqual, 1)
		So(f.itemState(), ShouldEqual, queue.ItemStateReady)

		budget := f.finalJob().UntilBuried
		So(budget, ShouldEqual, initialUntilBuried(0))

		Convey("a re-send of that release is acknowledged and leaves the kicked budget alone", func() {
			So(f.runner.releaseAfterAttempt(f.job, releaseAfterLostEndState(), FailReasonExit), ShouldBeNil)

			job := f.finalJob()
			So(job.State, ShouldEqual, JobStateReady)
			So(job.UntilBuried, ShouldEqual, budget)
		})
	})
}
