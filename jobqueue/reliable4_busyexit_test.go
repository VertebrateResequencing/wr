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

// Holistic reproducer for the "exiting job goes to the wrong queue/state when
// the manager is busy" failure class (live prod 2026-07-26: ~2000 resumed
// compress jobs whose zopfli commands SUCCEEDED had their archives rejected as
// ErrBadJob and were re-run, because a busy manager either processed-then-timed-
// out the archive or moved the job out of the run sub-queue before the runner's
// final-state report landed).
//
// The unifying property under test: once a job's command has exited and its
// legitimate reservation-holder reports the final state, that state MUST be
// applied (archive->complete, release->delayed/buried) and MUST NOT be discarded
// or cause a re-run - regardless of what speculative bookkeeping the (busy)
// manager did to the queue item in the meantime - UNLESS a genuinely new owner
// has taken the job over (new-run-wins), in which case the stale report is
// rejected and the old runner must abandon (not re-run).
//
// These run untagged under `make test`. Scenarios that FAIL on the current code
// (S1, S3, S5) are the bug; S2 and S4 are the guardrails that must stay correct.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// forceServerRelease moves the keyed job out of the Run sub-queue into Delay via
// the real server release path (as the lost-kill / double-reservation paths do),
// WITHOUT changing job.ReservedBy - modelling "the busy manager speculatively
// moved this job while its original runner was still alive and about to report".
func forceServerRelease(server *Server, key string) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return
	}

	//nolint:errcheck // best-effort white-box state forcing in a test helper
	server.releaseJob(context.Background(), job, releaseReport{
		endState:   &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()},
		failReason: FailReasonLost,
	})
}

// waitForItemSubQueue polls the keyed item's live sub-queue state until it
// matches want (returning true) or timeout elapses (false). It lets a test pin
// the item to a specific sub-queue (e.g. ready, after a speculative release's
// delay elapses) before acting, so the scenario is deterministic rather than
// racing the release-delay timer.
func waitForItemSubQueue(server *Server, key string, want queue.ItemState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if item, err := server.q.Get(key); err == nil && item != nil && item.Stats().State == want {
			return true
		}

		<-time.After(2 * time.Millisecond)
	}

	return false
}

// busyExitState returns the keyed job's server-side State and whether the item
// is still in the live queue (false once archived/removed).
func busyExitState(server *Server, key string) (JobState, bool) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return "", false
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return "", false
	}

	job.RLock()
	defer job.RUnlock()

	return job.State, true
}

func TestReliable4BusyExitStates(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const ttr = 300 * time.Millisecond

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	reserveStarted := func(jq *Client, rg string) *Job {
		job := &Job{
			Cmd: restFormTrue + " " + rg, Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 3,
		}
		_, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		return reserved
	}

	Convey("Given a busy manager and exiting jobs", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		server.SetLostJobCheckTimeout(2 * time.Second)
		server.SetLostJobCheckRetryTime(200 * time.Millisecond)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("S1: a successful archive is accepted even after the job was moved to Delay (owner unchanged)", func() {
			reserved := reserveStarted(jq, "busyexit_s1")
			key := reserved.Key()

			// the busy manager speculatively released the job to Delay.
			forceServerRelease(server, key)
			state, live := busyExitState(server, key)
			So(live, ShouldBeTrue)
			So(state, ShouldEqual, JobStateDelayed)

			// the original owner's successful archive must still complete the job.
			aerr := jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			So(aerr, ShouldBeNil)

			_, live = busyExitState(server, key)
			So(live, ShouldBeFalse) // removed from live queue == archived/complete
		})

		Convey("S2 (guardrail): a successful archive is accepted while the job is Lost-in-Run", func() {
			reserved := reserveStarted(jq, "busyexit_s2")
			key := reserved.Key()

			setServerJobPid(server, key, definitelyDeadPid(t))
			So(waitForJobLost(server, key, 20*ttr), ShouldBeTrue)

			aerr := jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			So(aerr, ShouldBeNil)

			_, live := busyExitState(server, key)
			So(live, ShouldBeFalse)
		})

		Convey("S3: a retried archive of an already-completed job is idempotent (not ErrBadJob->re-run)", func() {
			reserved := reserveStarted(jq, "busyexit_s3")

			aerr := jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			So(aerr, ShouldBeNil)

			// the first archive's response was "lost" to a busy manager; the runner
			// retries the SAME archive. It must succeed idempotently, not be rejected
			// (which the client treats as a permanent error and re-runs the job).
			aerr2 := jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			So(aerr2, ShouldBeNil)
		})

		Convey("S4 (guardrail): new-run-wins - a stale owner's archive is rejected once a new owner reserved", func() {
			reserved := reserveStarted(jq, "busyexit_s4")
			key := reserved.Key()

			// the job was released and a genuinely new runner reserved+started it.
			forceServerRelease(server, key)

			jq2, err2 := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err2, ShouldBeNil)

			defer disconnect(jq2)

			reserved2, errr2 := jq2.Reserve(2 * time.Second)
			So(errr2, ShouldBeNil)
			So(reserved2, ShouldNotBeNil)
			So(reserved2.Key(), ShouldEqual, key)
			So(jq2.Started(reserved2, os.Getpid()), ShouldBeNil)

			// the OLD owner's late archive must be REJECTED (new-run-wins), and must
			// not complete the job out from under the new owner.
			aerr := jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			So(aerr, ShouldNotBeNil)

			// the stale owner must NOT have completed/removed the job out from under
			// the new owner: it is still live and not Complete (new-run-wins).
			state, live := busyExitState(server, key)
			So(live, ShouldBeTrue)
			So(state, ShouldNotEqual, JobStateComplete)
		})

		Convey("S5: a failure release is applied after the item reached the ready sub-queue (owner unchanged)", func() {
			reserved := reserveStarted(jq, "busyexit_s5")
			key := reserved.Key()

			// the busy manager speculatively released the job; wait for the release
			// delay to elapse so its item has left Delay for the ready sub-queue -
			// the state a slow/frozen manager leaves it in by the time the runner's
			// late report lands. The original owner still holds the reservation, so
			// this exercises the applyReleaseQueueChange ready-item idempotent path
			// (without it the release falls through to q.Release -> "not running").
			forceServerRelease(server, key)
			So(waitForItemSubQueue(server, key, queue.ItemStateReady, 2*time.Second), ShouldBeTrue)

			// the original owner reports the command FAILED; the release must be
			// accepted (job stays delayed for retry), not rejected as ErrBadJob or
			// looped on an internal "not running" error.
			rerr := jq.Release(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "failed")
			So(rerr, ShouldBeNil)

			state, live := busyExitState(server, key)
			So(live, ShouldBeTrue)
			So(state, ShouldEqual, JobStateDelayed)
		})
	})
}
