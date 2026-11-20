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

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

// This file covers item B of
// .docs/bugfixes/261119-phase1-gate-defects-4f8ea64c.md: the 1g.3 crash soak's
// relb.r2.rel.113. A runner's release after a failed attempt was applied and on
// disk, but the manager was killed before it replied. After the restart another
// runner ran the job's last attempt and buried it, and then the first runner's
// re-sent release was answered "bad job". A re-send of a release the store has
// already applied must be acknowledged, and change nothing; a release that was
// never applied for its sender is refused as before.

const (
	// releaseResendRepGroup names the one job these tests add.
	releaseResendRepGroup = "release_resend_applied"

	// releaseResendReserveWait bounds how long a runner is given to reserve
	// the released job, which waits ReleaseDelayMin (1ms) in delay first.
	releaseResendReserveWait = 10 * time.Second
)

// releaseResendFixture is a manager on which runner A's run of its one job has
// been ended, perhaps restarted since, with a client of it for each of the
// runners involved.
type releaseResendFixture struct {
	ctx    context.Context //nolint:containedctx // test fixture
	server *Server
	key    string

	// first is runner A, under its own client ID, with its job and the end
	// state its release of it gives, which resend sends again.
	first      *Client
	firstJob   *Job
	firstEnded *JobEndState

	// other is a client of the manager under a fresh client ID.
	other *Client

	connect func() *Client
}

// releaseResendRun ends runner A's run of a releaseResendFixture's job.
type releaseResendRun func(f *releaseResendFixture)

// releasedByRunner has runner A release its run after a failed attempt.
func releasedByRunner(f *releaseResendFixture) {
	f.firstEnded = failedAttempt()
	So(f.first.releaseAfterAttempt(f.firstJob, f.firstEnded, FailReasonExit), ShouldBeNil)
}

// releasedByManager has the manager release runner A's run itself, as a lost
// run confirmed dead; A's release of it is then never applied unless A sends
// it.
func releasedByManager(f *releaseResendFixture) {
	f.firstEnded = failedAttempt()
	So(f.server.releaseJob(f.ctx, f.server.runSubQueueJob(f.key), lostJobReleaseReport()), ShouldBeNil)
}

// reservedAgainThenReleasedByManager returns a releaseResendRun that has runner
// A, released times in turn, each after a failed attempt and then a reserve and
// start of the same job again, and then has the manager release A's last run
// itself, as a lost run confirmed dead.
func reservedAgainThenReleasedByManager(released int) releaseResendRun {
	return func(f *releaseResendFixture) {
		for range released {
			releasedByRunner(f)

			f.firstJob = f.reserveAndStart(f.first)
		}

		releasedByManager(f)
	}
}

// newReleaseResendFixture adds a job with the given Retries to a manager using
// the given store, has runner A reserve and start it, and then has endRun end
// A's run. With restart, it then stops the manager and starts it again on the
// same store, and reconnects A under its old client ID.
func newReleaseResendFixture(t *testing.T, kind storeKind, retries uint8, restart bool,
	endRun releaseResendRun) *releaseResendFixture {
	t.Helper()

	ctx := context.Background()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
	serverConfig.storeKind = kind
	serverConfig.Timings.ItemTTR = time.Minute
	serverConfig.Timings.ReleaseDelayMin = time.Millisecond

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	connect := func() *Client {
		jq, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		return jq
	}

	added := &Job{
		Cmd: restFormTrue + " releaseresendapplied", Cwd: testCwd, RepGroup: releaseResendRepGroup,
		ReqGroup: releaseResendRepGroup, Requirements: standardReqs, Retries: retries,
	}

	f := &releaseResendFixture{ctx: ctx, server: server, key: added.Key(), first: connect(), connect: connect}

	inserts, _, err := f.first.Add([]*Job{added}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	f.firstJob = f.reserveAndStart(f.first)

	endRun(f)

	if restart {
		firstID := f.first.clientid

		server.Stop(ctx, true)
		disconnect(f.first)

		serverConfig.dontWipeDevDB = true

		f.server, _, token, err = serve(ctx, serverConfig)
		So(err, ShouldBeNil)
		So(waitUntilRecovered(f.server), ShouldBeTrue)

		f.first = connect()
		f.first.clientid = firstID
	}

	f.other = connect()

	return f
}

// stop stops the manager and disconnects every client.
func (f *releaseResendFixture) stop() {
	disconnect(f.first)
	disconnect(f.other)
	f.server.Stop(f.ctx, true)
}

// resend has runner A send its release again, exactly as it sent it the first
// time.
func (f *releaseResendFixture) resend() error {
	return f.first.releaseAfterAttempt(f.firstJob, f.firstEnded, FailReasonExit)
}

// state returns the manager's view of the job.
func (f *releaseResendFixture) state() *Job {
	jobs, err := f.other.GetByRepGroup(releaseResendRepGroup, false, 0, "", false, false)
	So(err, ShouldBeNil)
	So(jobs, ShouldHaveLength, 1)

	return jobs[0]
}

// reserveAgain has a new runner, B, reserve the job and report it started,
// returning B and its job.
func (f *releaseResendFixture) reserveAgain() (*Client, *Job) {
	runner := f.connect()

	return runner, f.reserveAndStart(runner)
}

// reserveAndStart has runner reserve the job, which may first wait out a
// release's delay, and report it started, returning its job.
func (f *releaseResendFixture) reserveAndStart(runner *Client) *Job {
	var job *Job

	deadline := time.Now().Add(releaseResendReserveWait)
	for time.Now().Before(deadline) && job == nil {
		job, _ = runner.Reserve(200 * time.Millisecond) //nolint:errcheck // retried until the deadline
	}

	So(job, ShouldNotBeNil)
	So(job.Key(), ShouldEqual, f.key)
	So(runner.Started(job, os.Getpid()), ShouldBeNil)

	return job
}

// TestReleaseResendAppliedKeepsNewest proves that a job remembers the last
// releasedByKept runners whose releases it applied, across a restart: their
// re-sends are acknowledged, and an older runner's is refused.
func TestReleaseResendAppliedKeepsNewest(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		runners   = releasedByKept + 2
		byManager = runners / 2
	)

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store holding a job released by %d runners in turn, then restarted",
			storeKindName(kind), runners), t, func() {
			ctx := context.Background()

			_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
			serverConfig.storeKind = kind
			serverConfig.Timings.ItemTTR = time.Minute
			serverConfig.Timings.ReleaseDelayMin = time.Millisecond

			server, _, token, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			connect := func() *Client {
				jq, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
				So(errc, ShouldBeNil)

				return jq
			}

			added := &Job{
				Cmd: restFormTrue + " releaseresendkept", Cwd: testCwd, RepGroup: releaseResendRepGroup,
				ReqGroup: releaseResendRepGroup, Requirements: standardReqs, Retries: runners - 1,
			}

			adder := connect()
			inserts, _, err := adder.Add([]*Job{added}, os.Environ(), true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			disconnect(adder)

			ids := make([]uuid.UUID, runners)
			jobs := make([]*Job, runners)
			ended := make([]*JobEndState, runners)

			f := &releaseResendFixture{ctx: ctx, server: server, key: added.Key(), connect: connect}

			// every run but the one at byManager is released by its runner; that
			// one is released by the manager, as a lost run confirmed dead.
			for i := range runners {
				runner, job := f.reserveAgain()
				ids[i], jobs[i], ended[i] = runner.clientid, job, failedAttempt()

				if i != byManager {
					So(runner.releaseAfterAttempt(job, ended[i], FailReasonExit), ShouldBeNil)
				} else {
					So(server.releaseJob(ctx, server.runSubQueueJob(f.key), lostJobReleaseReport()), ShouldBeNil)
				}

				disconnect(runner)
			}

			server.Stop(ctx, true)

			serverConfig.dontWipeDevDB = true

			server, _, token, err = serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer func() { server.Stop(ctx, true) }()

			So(waitUntilRecovered(server), ShouldBeTrue)

			resend := func(i int) error {
				runner := connect()
				defer disconnect(runner)

				runner.clientid = ids[i]

				return runner.releaseAfterAttempt(jobs[i], ended[i], FailReasonExit)
			}

			Convey("the newest runners' re-sends are acknowledged, the oldest's and the never-applied one's "+
				"refused, and the job is unchanged", func() {
				acked := 0

				for i := 1; i < runners; i++ {
					if i != byManager && resend(i) == nil {
						acked++
					}
				}

				So(acked, ShouldEqual, releasedByKept)
				soRefusedWith(resend(0), ErrBadJob)
				soRefusedWith(resend(byManager), ErrBadJob)

				f.other = connect()
				defer disconnect(f.other)

				job := f.state()
				So(job.State, ShouldEqual, JobStateBuried)
				So(job.UntilBuried, ShouldEqual, 0)
				So(job.Attempts, ShouldEqual, runners)
			})
		})
	})
}

// errNotResent stands in for the re-send's error until it is made.
var errNotResent = errors.New("not re-sent")

// soRefusedWith asserts err is a jobqueue Error whose Err is one of want.
func soRefusedWith(err error, want ...string) {
	jqErr, ok := errors.AsType[Error](err)
	So(ok, ShouldBeTrue)
	So(jqErr.Err, ShouldBeIn, want)
}

// TestReleaseResendApplied proves that runner A's re-sent release, which a
// restarted manager's store already applied, is acknowledged whether the job is
// still waiting to run again, is running for another runner, or has been
// buried by that runner, and that the re-send spends no retry, counts no
// attempt and leaves the other runner's run alone.
func TestReleaseResendApplied(t *testing.T) {
	if runnermode || servermode {
		return
	}

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store whose manager restarted after applying runner A's release",
			storeKindName(kind)), t, func() {
			f := newReleaseResendFixture(t, kind, releaseDurabilityRetries, true, releasedByRunner)
			defer f.stop()

			Convey("A's re-send is acknowledged while the job waits to run again, and changes nothing", func() {
				So(f.resend(), ShouldBeNil)

				job := f.state()
				So(job.State, ShouldBeIn, []JobState{JobStateDelayed, JobStateReady})
				So(job.UntilBuried, ShouldEqual, releaseDurabilityRetries)
				So(job.Attempts, ShouldEqual, 1)
			})

			Convey("once runner B is running the job, A's re-send is acknowledged and B's run is left alone", func() {
				runnerB, jobB := f.reserveAgain()
				defer disconnect(runnerB)

				So(f.resend(), ShouldBeNil)

				job := f.state()
				So(job.State, ShouldEqual, JobStateRunning)
				So(job.UntilBuried, ShouldEqual, releaseDurabilityRetries)
				So(job.Attempts, ShouldEqual, 2)

				So(runnerB.releaseAfterAttempt(jobB, failedAttempt(), FailReasonExit), ShouldBeNil)
				So(f.state().State, ShouldEqual, JobStateBuried)
			})

			Convey("once runner B has buried the job after its last attempt, A's re-send is acknowledged", func() {
				runnerB, jobB := f.reserveAgain()
				defer disconnect(runnerB)

				So(runnerB.releaseAfterAttempt(jobB, failedAttempt(), FailReasonExit), ShouldBeNil)

				So(f.resend(), ShouldBeNil)

				job := f.state()
				So(job.State, ShouldEqual, JobStateBuried)
				So(job.UntilBuried, ShouldEqual, 0)
				So(job.Attempts, ShouldEqual, 2)
			})

			Convey("A's re-send while runner B's reservation is part way through is acknowledged, "+
				"and B's run is left alone", func() {
				runnerB := f.connect()
				defer disconnect(runnerB)

				var (
					once      sync.Once
					resendErr = errNotResent
				)

				reservationQueuedHook = func(key string) {
					if key == f.key {
						once.Do(func() { resendErr = f.resend() })
					}
				}
				defer func() { reservationQueuedHook = nil }()

				var jobB *Job

				deadline := time.Now().Add(releaseResendReserveWait)
				for time.Now().Before(deadline) && jobB == nil {
					jobB, _ = runnerB.Reserve(200 * time.Millisecond) //nolint:errcheck // retried until the deadline
				}

				So(jobB, ShouldNotBeNil)
				So(resendErr, ShouldBeNil)

				job := f.state()
				So(job.State, ShouldEqual, JobStateReserved)
				So(job.UntilBuried, ShouldEqual, releaseDurabilityRetries)

				So(runnerB.Started(jobB, os.Getpid()), ShouldBeNil)
				So(runnerB.releaseAfterAttempt(jobB, failedAttempt(), FailReasonExit), ShouldBeNil)
				So(f.state().State, ShouldEqual, JobStateBuried)
			})

			Convey("a release from a client that never held the job is refused", func() {
				soRefusedWith(f.other.releaseAfterAttempt(f.firstJob, failedAttempt(), FailReasonExit),
					ErrMustReserve)

				runnerB, jobB := f.reserveAgain()
				defer disconnect(runnerB)

				stranger := f.connect()
				defer disconnect(stranger)

				soRefusedWith(stranger.releaseAfterAttempt(f.firstJob, failedAttempt(), FailReasonExit),
					ErrMustReserve)

				So(runnerB.releaseAfterAttempt(jobB, failedAttempt(), FailReasonExit), ShouldBeNil)

				soRefusedWith(stranger.releaseAfterAttempt(f.firstJob, failedAttempt(), FailReasonExit),
					ErrBadJob)
				So(f.state().State, ShouldEqual, JobStateBuried)
			})
		})

		Convey(fmt.Sprintf("Given a %s store whose manager released runner A's run itself, then restarted",
			storeKindName(kind)), t, func() {
			f := newReleaseResendFixture(t, kind, releaseDurabilityRetries, true, releasedByManager)
			defer f.stop()

			Convey("A's release, which was never applied, is refused once the job has moved on", func() {
				runnerB, jobB := f.reserveAgain()
				defer disconnect(runnerB)

				soRefusedWith(f.resend(), ErrMustReserve)
				So(f.state().State, ShouldEqual, JobStateRunning)

				So(runnerB.releaseAfterAttempt(jobB, failedAttempt(), FailReasonExit), ShouldBeNil)

				soRefusedWith(f.resend(), ErrBadJob)

				job := f.state()
				So(job.State, ShouldEqual, JobStateBuried)
				So(job.Attempts, ShouldEqual, 2)
			})

			Convey("A's report, applied over the manager's release, is acknowledged when re-sent "+
				"once the job has moved on", func() {
				So(f.resend(), ShouldBeNil)

				runnerB, jobB := f.reserveAgain()
				defer disconnect(runnerB)

				So(f.resend(), ShouldBeNil)
				So(runnerB.releaseAfterAttempt(jobB, failedAttempt(), FailReasonExit), ShouldBeNil)
				So(f.resend(), ShouldBeNil)

				job := f.state()
				So(job.State, ShouldEqual, JobStateBuried)
				So(job.Attempts, ShouldEqual, 2)
			})
		})
	})
}

// TestReleaseResendAppliedOlderBury proves that a re-sent bury of a job buried
// by a manager that kept no ReleasedBy, as one before it did, is still
// acknowledged while the job stays buried and reserved by its runner, and that
// another client's bury of it is refused.
func TestReleaseResendAppliedOlderBury(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store's job buried by its runner, whose ReleasedBy is then forgotten",
			storeKindName(kind)), t, func() {
			server, jq, job := startedJobForResend(ctx, t, releaseDurabilityRetries,
				func(c *ServerConfig) { c.storeKind = kind })

			defer server.Stop(ctx, true)
			defer disconnect(jq)

			ended := failedAttempt()
			So(jq.Bury(job, ended, FailReasonExit), ShouldBeNil)

			item, err := server.q.Get(job.Key())
			So(err, ShouldBeNil)

			stored, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			stored.Lock()
			So(stored.ReleasedBy, ShouldResemble, []uuid.UUID{jq.clientid})
			stored.ReleasedBy = nil
			stored.Unlock()

			Convey("its runner's re-sent bury is acknowledged, and another client's refused", func() {
				So(jq.Bury(job, ended, FailReasonExit), ShouldBeNil)

				jq.clientid = uuid.Must(uuid.NewV4())
				soRefusedWith(jq.Bury(job, failedAttempt(), FailReasonExit), ErrBadJob)
			})
		})
	})
}

// TestReleaseResendAppliedNotFromAdd proves that a client cannot add a job
// naming runners whose reports it applied: a release from one it named is
// refused.
func TestReleaseResendAppliedNotFromAdd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store, and a job added naming a runner in its ReleasedBy",
			storeKindName(kind)), t, func() {
			_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
			serverConfig.storeKind = kind

			server, _, token, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer server.Stop(ctx, true)

			jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			job := &Job{
				Cmd: restFormTrue + " releaseresendadded", Cwd: testCwd, RepGroup: releaseResendRepGroup,
				ReqGroup: releaseResendRepGroup, Requirements: standardReqs,
				ReleasedBy: []uuid.UUID{jq.clientid},
			}

			inserts, _, err := jq.Add([]*Job{job}, os.Environ(), true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)

			Convey("that runner's release of it, never reserved, is refused", func() {
				soRefusedWith(jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit), ErrMustReserve)
			})
		})
	})
}

// TestReleaseResendAppliedReservedAgain proves that a runner that had its
// release of a job applied, then reserved the same job again under the same
// client ID, is refused as an old owner once the manager has released that
// second run itself, without its report, and another runner has taken the job
// on, whether or not the manager restarted in between. It also proves what a
// re-send of the first, applied, release does while the runner holds its second
// reservation.
func TestReleaseResendAppliedReservedAgain(t *testing.T) {
	if runnermode || servermode {
		return
	}

	forEachStore(t, func(kind storeKind) {
		for _, c := range []struct {
			released int
			restart  bool
		}{{1, false}, {1, true}, {2, false}} {
			Convey(fmt.Sprintf("Given a %s store's job runner A released %d times, each time reserving it again, "+
				"and whose last run the manager released itself (restarted after: %t)",
				storeKindName(kind), c.released, c.restart), t, func() {
				f := newReleaseResendFixture(t, kind, releaseResendRetries, c.restart,
					reservedAgainThenReleasedByManager(c.released))
				defer f.stop()

				Convey("A's release of its second run is refused once runner B holds or has buried the job", func() {
					runnerB, jobB := f.reserveAgain()
					defer disconnect(runnerB)

					soRefusedWith(f.resend(), ErrMustReserve)
					So(f.state().State, ShouldEqual, JobStateRunning)

					So(runnerB.Bury(jobB, failedAttempt(), FailReasonExit), ShouldBeNil)

					soRefusedWith(f.resend(), ErrBadJob)

					job := f.state()
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Attempts, ShouldEqual, c.released+2)
				})
			})
		}

		Convey(fmt.Sprintf("Given a %s store's job runner A released and then reserved again",
			storeKindName(kind)), t, func() {
			f := newReleaseResendFixture(t, kind, releaseResendRetries, true, releasedByRunner)
			defer f.stop()

			f.reserveAndStart(f.first)

			Convey("a re-send of A's first release, which the manager cannot tell from a release of the run "+
				"A now holds, releases that run", func() {
				So(f.resend(), ShouldBeNil)

				job := f.state()
				So(job.State, ShouldBeIn, []JobState{JobStateDelayed, JobStateReady})
				So(job.UntilBuried, ShouldEqual, releaseResendRetries-1)
				So(job.Attempts, ShouldEqual, 2)
			})
		})
	})
}

// TestReleaseResendAppliedNotAfterLostBury proves that a runner's release or
// bury of a job whose run the manager released itself, as lost, burying it, is
// refused: the manager never applied that runner's report.
func TestReleaseResendAppliedNotAfterLostBury(t *testing.T) {
	if runnermode || servermode {
		return
	}

	forEachStore(t, func(kind storeKind) {
		for _, restart := range []bool{false, true} {
			Convey(fmt.Sprintf("Given a %s store's job with no retries whose run the manager released and buried "+
				"(restarted after: %t)", storeKindName(kind), restart), t, func() {
				f := newReleaseResendFixture(t, kind, 0, restart, releasedByManager)
				defer f.stop()

				So(f.state().State, ShouldEqual, JobStateBuried)

				Convey("its runner's release and bury are refused, and the job stays buried as lost", func() {
					soRefusedWith(f.resend(), ErrBadJob)
					soRefusedWith(f.first.Bury(f.firstJob, f.firstEnded, FailReasonExit), ErrBadJob)

					job := f.state()
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.FailReason, ShouldEqual, FailReasonLost)
				})
			})
		}
	})
}

// TestReleaseResendAppliedNotAfterDelete proves that runner A's re-sent
// release of a job deleted since its release was applied is refused: there is
// no job left whose release could be acknowledged.
func TestReleaseResendAppliedNotAfterDelete(t *testing.T) {
	if runnermode || servermode {
		return
	}

	forEachStore(t, func(kind storeKind) {
		for _, restart := range []bool{false, true} {
			Convey(fmt.Sprintf("Given a %s store's job runner A released, then deleted (restarted before the "+
				"delete: %t)", storeKindName(kind), restart), t, func() {
				f := newReleaseResendFixture(t, kind, releaseResendRetries, restart, releasedByRunner)
				defer f.stop()

				deleted, err := f.other.Delete([]*JobEssence{{JobKey: f.key}})
				So(err, ShouldBeNil)
				So(deleted, ShouldEqual, 1)

				Convey("A's re-send is refused", func() {
					soRefusedWith(f.resend(), ErrBadJob)
				})
			})
		}
	})
}

// releasedByRunnerThenRepeatedly has runner A release its run, and then has one
// other runner reserve, start and release the job releasedByKept times in turn.
func releasedByRunnerThenRepeatedly(f *releaseResendFixture) {
	releasedByRunner(f)

	runner := f.connect()
	defer disconnect(runner)

	for range releasedByKept {
		job := f.reserveAndStart(runner)
		So(runner.releaseAfterAttempt(job, failedAttempt(), FailReasonExit), ShouldBeNil)
	}
}

// TestReleaseResendAppliedRepeatReleaser proves that a runner releasing the
// same job many times takes one place among the releasedByKept runners a job
// remembers, so it does not push out a runner whose release was applied before.
func TestReleaseResendAppliedRepeatReleaser(t *testing.T) {
	if runnermode || servermode {
		return
	}

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store's job runner A released, then runner B released %d times, "+
			"then restarted", storeKindName(kind), releasedByKept), t, func() {
			f := newReleaseResendFixture(t, kind, releasedByKept+1, true, releasedByRunnerThenRepeatedly)
			defer f.stop()

			Convey("A's re-send is still acknowledged", func() {
				So(f.resend(), ShouldBeNil)
			})
		})
	})
}

// TestReleaseResendAppliedNotKeptComplete proves that a job's complete record
// does not name the runners whose releases of it were applied, as kept in the
// store and as clients are sent it, and that a re-send of such a release is
// still acknowledged once the job is complete.
func TestReleaseResendAppliedNotKeptComplete(t *testing.T) {
	if runnermode || servermode {
		return
	}

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store's job runner A released, which runner B then ran to success",
			storeKindName(kind)), t, func() {
			f := newReleaseResendFixture(t, kind, releaseResendRetries, false, releasedByRunner)
			defer f.stop()

			runnerB, jobB := f.reserveAgain()
			defer disconnect(runnerB)

			So(runnerB.Archive(jobB, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			Convey("neither its stored complete record nor the job clients are sent names A, "+
				"and A's re-send is acknowledged", func() {
				stored, err := f.server.db.retrieveCompleteJobsByKeys([]string{f.key})
				So(err, ShouldBeNil)
				So(stored, ShouldHaveLength, 1)
				So(stored[0].ReleasedBy, ShouldBeNil)

				jobs, err := f.other.GetByRepGroup(releaseResendRepGroup, false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(jobs, ShouldHaveLength, 1)
				So(jobs[0].State, ShouldEqual, JobStateComplete)
				So(jobs[0].ReleasedBy, ShouldBeNil)

				So(f.resend(), ShouldBeNil)
			})
		})
	})
}

// errReportWriteFailed is the write error reportWriteStore gives.
var errReportWriteFailed = errors.New("report write failed")

// What reportWriteStore notes a write as: one of the whole job, or one of its
// run state only.
const (
	reportWroteJob      = "job"
	reportWroteRunState = "run state"
)

// reportWriteStore is a store for a Server with no other store, holding no
// complete jobs, that notes which of a job's durable writes a re-sent report's
// acknowledgement asked for, and fails them if fail is set. Any other call
// panics, on the nil store it embeds.
type reportWriteStore struct {
	store

	fail   bool
	writes []string
}

// checkIfComplete reports that no job is complete.
func (*reportWriteStore) checkIfComplete(string) (bool, error) {
	return false, nil
}

// updateJobAfterChangeDurable notes a write of the whole job.
func (r *reportWriteStore) updateJobAfterChangeDurable(*Job) error {
	return r.write(reportWroteJob)
}

// updateJobRunStateDurable notes a write of the job's run state only.
func (r *reportWriteStore) updateJobRunStateDurable(*Job) error {
	return r.write(reportWroteRunState)
}

// write notes a write of what, and returns errReportWriteFailed if fail is set.
func (r *reportWriteStore) write(what string) error {
	r.writes = append(r.writes, what)

	if r.fail {
		return errReportWriteFailed
	}

	return nil
}

// TestReleaseResendAppliedWrite proves that a re-sent release the manager
// already applied is acknowledged only once a write of the whole job has
// succeeded, whether the job is buried or being run by another runner: a write
// of a running job's run state alone would not carry the applied release, had
// its own write failed. A failed write is answered ErrInternalError, which the
// runner re-sends, and not acknowledged.
func TestReleaseResendAppliedWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, state := range []queue.SubQueue{queue.SubQueueBury, queue.SubQueueRun} {
		Convey(fmt.Sprintf("Given a job in sub-queue %s held by runner B, whose release by runner A was applied",
			state), t, func() {
			runnerA, runnerB := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			db := &reportWriteStore{}
			server := &Server{q: queue.New(ctx, "release-resend-write"), db: db, up: true}

			job := &Job{
				Cmd: "release resend write", ReservedBy: runnerB, ReleasedBy: []uuid.UUID{runnerA},
				State: JobStateRunning,
			}
			if state == queue.SubQueueBury {
				job.State = JobStateBuried
			}

			_, err := server.q.Add(ctx, job.Key(), "", job, 0, 0, time.Minute, state)
			So(err, ShouldBeNil)

			resend := func() string {
				_, srerr, _ := server.handleRelease(ctx, &clientRequest{
					ClientID: runnerA, Job: &Job{Cmd: job.Cmd}, JobEndState: failedAttempt(),
				}, false, "release resend write")

				return srerr
			}

			Convey("A's re-send is acknowledged after a write of the whole job", func() {
				So(resend(), ShouldBeEmpty)
				So(db.writes, ShouldResemble, []string{reportWroteJob})
			})

			Convey("A's re-send is answered ErrInternalError, not acknowledged, if that write fails", func() {
				db.fail = true

				So(resend(), ShouldEqual, ErrInternalError)
				So(db.writes, ShouldResemble, []string{reportWroteJob})
			})
		})
	}
}

// TestReleaseResendAppliedManagerReleaseMidReservation proves that a manager's
// release of a run part way through being reserved by runner B does not take
// runner A, the job's previous runner, whose release was applied, out of
// ReleasedBy: the release did not end a run of A's, though ReservedBy still
// names A until B's reservation resets the job.
func TestReleaseResendAppliedManagerReleaseMidReservation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store whose manager restarted after applying runner A's release",
			storeKindName(kind)), t, func() {
			f := newReleaseResendFixture(t, kind, releaseResendRetries, true, releasedByRunner)
			defer f.stop()

			runnerB := f.connect()
			defer disconnect(runnerB)

			var once sync.Once

			released := make(chan error, 1)

			reservationQueuedHook = func(key string) {
				if key == f.key {
					once.Do(func() {
						released <- f.server.releaseJob(f.ctx, f.server.runSubQueueJob(key), lostJobReleaseReport())
					})
				}
			}
			defer func() { reservationQueuedHook = nil }()

			var (
				releaseErr error
				hooked     bool
			)

			deadline := time.Now().Add(releaseResendReserveWait)
			for time.Now().Before(deadline) && !hooked {
				runnerB.Reserve(200 * time.Millisecond) //nolint:errcheck // retried until the hook has run

				select {
				case releaseErr = <-released:
					hooked = true
				default:
				}
			}

			Convey("A's re-send is still acknowledged", func() {
				So(hooked, ShouldBeTrue)
				So(releaseErr, ShouldBeNil)
				So(f.resend(), ShouldBeNil)
			})
		})
	})
}
