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

// This file covers the last item of
// .docs/bugfixes/260930-release-durability.md: the manager marks a started job
// lost and releases it itself, either because it confirmed the job's pids dead
// or because the user killed the lost job, and then the job's owner, a runner
// that was really still alive, reports the job's final state. That report is
// accepted as the owner's, since the job is still reserved by it. A bury ("stop
// means buried") must leave the job buried, and a release must be acknowledged
// with the job's retry spent only once. Either must be answered definitively:
// the runner re-sends an error such as ErrInternalError for its whole retry
// time, about 24h. Each report here is sent once, so a test cannot loop.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	releaseAfterLostRepGroup = "release_after_lost"
	releaseAfterLostTTR      = 300 * time.Millisecond
	releaseAfterLostWait     = 20 * releaseAfterLostTTR

	// releaseAfterLostDelay keeps a released job delayed for the whole test,
	// so no ready-added recount follows its release.
	releaseAfterLostDelay = time.Minute
)

// releaseAfterLostFixture is a manager, the owning runner's client, the job it
// reserved and started, the Retries it was added with, and a second client
// acting as the user.
type releaseAfterLostFixture struct {
	t       *testing.T
	server  *Server
	runner  *Client
	user    *Client
	job     *Job
	retries uint8

	// connect connects another client to the manager.
	connect func() *Client
}

// newReleaseAfterLostFixture starts a manager with a short TTR, prompt lost
// checks, a long release delay and the given runner command, adds a job with
// the given retries, and has a runner reserve it and report it started with
// commandPid.
func newReleaseAfterLostFixture(ctx context.Context, t *testing.T, retries uint8,
	commandPid int, runnerCmd string) *releaseAfterLostFixture {
	t.Helper()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = releaseAfterLostTTR
	serverConfig.Timings.ReleaseDelayMin = releaseAfterLostDelay
	serverConfig.RunnerCmd = runnerCmd

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	server.SetLostJobCheckTimeout(2 * time.Second)
	server.SetLostJobCheckRetryTime(200 * time.Millisecond)

	runner, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	user, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	inserts, _, err := runner.Add([]*Job{{
		Cmd: restFormTrue + " releaseafterlost", Cwd: testCwdPath, RepGroup: releaseAfterLostRepGroup,
		ReqGroup: releaseAfterLostRepGroup, Requirements: standardReqs, Retries: retries,
	}}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	// with a runner command, only a runner for the job's scheduler group can
	// reserve it.
	var schedulerGroup string
	if runnerCmd != "" {
		schedulerGroup = schedulerGroupString(reqForScheduler(standardReqs), nil)
	}

	reserved, err := runner.ReserveScheduled(2*time.Second, schedulerGroup)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(runner.Started(reserved, commandPid), ShouldBeNil)

	connect := func() *Client {
		client, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		return client
	}

	return &releaseAfterLostFixture{
		t: t, server: server, runner: runner, user: user, job: reserved, retries: retries, connect: connect,
	}
}

// stop disconnects the clients and stops the manager.
func (f *releaseAfterLostFixture) stop(ctx context.Context) {
	disconnect(f.user)
	disconnect(f.runner)
	f.server.Stop(ctx, true)
}

// waitForManagerRelease waits until the manager has released the job itself,
// leaving it delayed while the runner still holds the reservation.
func (f *releaseAfterLostFixture) waitForManagerRelease() bool {
	return f.waitForState(JobStateDelayed)
}

// waitForState waits until the manager's job is in the given state.
func (f *releaseAfterLostFixture) waitForState(want JobState) bool {
	deadline := time.Now().Add(releaseAfterLostWait)

	for time.Now().Before(deadline) {
		if state, live := busyExitState(f.server, f.job.Key()); live && state == want {
			return true
		}

		<-time.After(10 * time.Millisecond)
	}

	return false
}

// essence is the job's essence, for the user's commands.
func (f *releaseAfterLostFixture) essence() []*JobEssence {
	return []*JobEssence{{JobKey: f.job.Key()}}
}

// scheduledCount is the manager's count of runners wanted for the job's
// scheduler group.
func (f *releaseAfterLostFixture) scheduledCount() int {
	item, err := f.server.q.Get(f.job.Key())
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	f.server.psgmutex.RLock()
	group, existed := f.server.previouslyScheduledGroups[job.getSchedulerGroup()]
	f.server.psgmutex.RUnlock()

	if !existed {
		return 0
	}

	return group.getCount()
}

// finalJob is the job as a client sees it.
func (f *releaseAfterLostFixture) finalJob() *Job {
	jobs, err := f.user.GetByRepGroup(releaseAfterLostRepGroup, false, 0, "", false, false)
	So(err, ShouldBeNil)
	So(len(jobs), ShouldEqual, 1)

	return jobs[0]
}

// itemState is the state of the job's queue item.
func (f *releaseAfterLostFixture) itemState() queue.ItemState {
	item, err := f.server.q.Get(f.job.Key())
	So(err, ShouldBeNil)

	return item.Stats().State
}

// addReadyJobInSameGroup adds a second job, in another rep group, that shares
// the fixture job's scheduler group.
func (f *releaseAfterLostFixture) addReadyJobInSameGroup() {
	inserts, _, err := f.user.Add([]*Job{{
		Cmd: restFormTrue + " releaseafterlostother", Cwd: testCwdPath, RepGroup: releaseAfterLostRepGroup + "_other",
		ReqGroup: releaseAfterLostRepGroup, Requirements: f.job.Requirements.Clone(),
	}}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)
}

// TestReleaseAfterLost proves that a runner's bury or release of a job the
// manager has already released as lost is answered definitively and applied.
func TestReleaseAfterLost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	// one job has retries to spare after the manager's release spends one, and
	// the other's release by the manager spends its last spare retry.
	for _, retries := range []uint8{3, 1} {
		Convey(fmt.Sprintf("Given a started job with %d retries the manager confirmed dead and released itself",
			retries), t, func() {
			f := newReleaseAfterLostFixture(ctx, t, retries, definitelyDeadPid(t), "")
			defer f.stop(ctx)

			setServerJobRunnerPid(f.server, f.job.Key(), definitelyDeadPid(t))
			So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)
			So(f.waitForManagerRelease(), ShouldBeTrue)

			assertOwnerReportsAfterLostRelease(f)
		})

		Convey(fmt.Sprintf("Given a started lost job with %d retries the user killed, so the manager released it",
			retries), t, func() {
			f := newReleaseAfterLostFixture(ctx, t, retries, os.Getpid(), "")
			defer f.stop(ctx)

			So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)

			killed, err := f.user.Kill([]*JobEssence{{JobKey: f.job.Key()}})
			So(err, ShouldBeNil)
			So(killed, ShouldEqual, 1)
			So(f.waitForManagerRelease(), ShouldBeTrue)

			assertOwnerReportsAfterLostRelease(f)
		})
	}

	Convey("Given a started job with 3 retries the manager released as lost, which the user suspended and resumed",
		t, func() {
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
			So(f.finalJob().State, ShouldEqual, JobStateReady)

			assertOwnerReportsOnWaitingJob(f, f.retries)
		})

	Convey("Given a started lost job with no retries the user killed, so the manager buried it, and then kicked",
		t, func() {
			f := newReleaseAfterLostFixture(ctx, t, 0, os.Getpid(), "")
			defer f.stop(ctx)

			So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)

			killed, err := f.user.Kill(f.essence())
			So(err, ShouldBeNil)
			So(killed, ShouldEqual, 1)
			So(f.waitForState(JobStateBuried), ShouldBeTrue)

			// with a runner command, the manager counts the kicked job as wanting
			// a runner.
			f.server.setRC(serverRC)

			kicked, err := f.user.Kick(f.essence())
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)
			So(f.finalJob().State, ShouldEqual, JobStateReady)
			So(pollUntil(func() bool { return f.scheduledCount() == 1 }), ShouldBeTrue)

			assertOwnerReportsOnWaitingJob(f, initialUntilBuried(0))

			Convey("and once the owner has buried it, no runner is wanted for it", func() {
				So(f.runner.Bury(f.job, releaseAfterLostEndState(), FailReasonExit), ShouldBeNil)
				So(pollUntilFor(5*time.Second, func() bool { return f.scheduledCount() == 0 }), ShouldBeTrue)
			})
		})

	Convey("Given a started job with 3 retries the manager released as lost, left delayed, "+
		"and another job wanting a runner in its scheduler group", t, func() {
		// with a runner command from the start, the job has a scheduler group,
		// and the manager's release of its run gives back that run's runner.
		f := newReleaseAfterLostFixture(ctx, t, 3, definitelyDeadPid(t), serverRC)
		defer f.stop(ctx)

		setServerJobRunnerPid(f.server, f.job.Key(), definitelyDeadPid(t))
		So(waitForJobLost(f.server, f.job.Key(), releaseAfterLostWait), ShouldBeTrue)
		So(f.waitForManagerRelease(), ShouldBeTrue)
		So(f.itemState(), ShouldEqual, queue.ItemStateDelay)
		So(pollUntil(func() bool { return f.scheduledCount() == 0 }), ShouldBeTrue)

		// only the other job, ready, now wants a runner. The delayed job does not
		// move to ready during the test, so nothing recounts the group after a
		// bury of it.
		f.addReadyJobInSameGroup()
		So(pollUntil(func() bool { return f.scheduledCount() == 1 }), ShouldBeTrue)

		Convey("the owner's bury buries it and leaves the other job's runner wanted", func() {
			So(f.runner.Bury(f.job, releaseAfterLostEndState(), FailReasonExit), ShouldBeNil)
			So(f.itemState(), ShouldEqual, queue.ItemStateBury)
			So(f.scheduledCount(), ShouldEqual, 1)
		})

		Convey("a duplicate owner's bury that saw it waiting before the first bury landed leaves "+
			"the other job's runner wanted", func() {
			item, err := f.server.q.Get(f.job.Key())
			So(err, ShouldBeNil)

			job, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			// the steps releaseJob takes for the duplicate, with the first bury
			// landing between its snapshot and its queue change.
			rep := releaseReport{
				endState: releaseAfterLostEndState(), failReason: FailReasonExit,
				forceStorage: true, forceBury: true, durable: true,
			}
			snap := releaseJobSnapshot(job, item, &rep)

			So(f.runner.Bury(f.job, releaseAfterLostEndState(), FailReasonExit), ShouldBeNil)
			So(f.scheduledCount(), ShouldEqual, 1)

			outcome, err := f.server.applyReleaseQueueChange(ctx, f.server.q, item, snap, job, nil)
			So(err, ShouldBeNil)
			So(f.server.finalizeReleasedJob(ctx, job, rep, outcome), ShouldBeNil)

			So(f.itemState(), ShouldEqual, queue.ItemStateBury)
			So(f.scheduledCount(), ShouldEqual, 1)
		})
	})
}

// releaseAfterLostEndState is the end state of a run whose command exited
// non-zero.
func releaseAfterLostEndState() *JobEndState {
	return &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}
}

// assertOwnerReportsAfterLostRelease checks the owning runner's bury and release
// of a job the manager has already released as lost, having spent one retry.
func assertOwnerReportsAfterLostRelease(f *releaseAfterLostFixture) {
	assertOwnerReportsOnWaitingJob(f, f.retries)
}

// assertOwnerReportsOnWaitingJob checks the owning runner's bury and release of
// a job whose run the manager has already given up on, and which is now waiting
// to run again with the given retry budget. The owner's release must not spend
// that budget again.
func assertOwnerReportsOnWaitingJob(f *releaseAfterLostFixture, untilBuried uint8) {
	So(f.finalJob().UntilBuried, ShouldEqual, untilBuried)

	Convey("the owner's bury is acknowledged and the job ends buried", func() {
		err := f.runner.Bury(f.job, releaseAfterLostEndState(), FailReasonExit)
		So(err, ShouldBeNil)

		So(storedLiveJobState(f.t, f.server.db, f.job.Key()), ShouldEqual, JobStateBuried)

		job := f.finalJob()
		So(job.State, ShouldEqual, JobStateBuried)
		So(job.UntilBuried, ShouldEqual, 0)
		So(job.FailReason, ShouldEqual, FailReasonExit)
	})

	Convey("the owner's release is acknowledged and the job is retried, its retry spent once", func() {
		err := f.runner.releaseAfterAttempt(f.job, releaseAfterLostEndState(), FailReasonExit)
		So(err, ShouldBeNil)

		job := f.finalJob()
		So(job.State, ShouldBeIn, []JobState{JobStateDelayed, JobStateReady})
		So(job.UntilBuried, ShouldEqual, untilBuried)
	})
}
