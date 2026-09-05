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

// Untagged, fast behavioural regression test for checklist 260726-4: a running
// job recovered after a DB-preserving restart whose runner never reconnects
// (died/bkilled during the downtime) must be confirmed dead and reclaimed like
// any other lost job, rather than stay "lost" forever holding its slot. This
// deliberately changes the #550 permanent recovered-protection (which bypassed
// both the dead-check and the backstop); confirmJobDead's both-pid liveness
// check (checklist 260726-3) now provides the only protection needed, and still
// keeps an alive-runner recovered job parked (its unrecorded success safe) -
// covered by the second assertion here and by TestReliable2ReleaseCrashRecovery.

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable4RecoveredRunnerDead proves that removing the permanent
// recovered-protection lets a recovered running job with a genuinely dead runner
// be reclaimed, while the both-pid liveness check still parks one whose runner is
// alive.
func TestReliable4RecoveredRunnerDead(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 1 * time.Second
		rg  = "reliable4_recovered_rg"
	)

	ctx := context.Background()

	Convey("Given a reserved+started job recovered as running after a DB-preserving restart", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)

		// server1 gets a long TTR so it never marks the job lost during the (fast)
		// setup; only the restarted server gets the short TTR that drives the
		// recovered job lost. This keeps the on-disk job cleanly Running (not Lost)
		// across the restart, so recovery behaves as it would in production.
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " recovereddead", Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 3,
		}
		inserts, _, erra := jq.Add([]*Job{job}, os.Environ(), true)
		So(erra, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		key := reserved.Key()

		// the manager crashes and comes back with the DB preserved: the still-running
		// job is recovered into the Run sub-queue. The restarted server gets the short
		// TTR so the recovered job is marked lost promptly.
		server.Stop(ctx, true)

		serverConfig.Timings.ItemTTR = ttr
		server = restartSubscriptionTestServer(ctx, serverConfig)
		So(waitUntilRecovered(server), ShouldBeTrue)

		// make the lost/confirm-dead path fire promptly so the test is fast.
		server.SetLostJobCheckTimeout(2 * time.Second)
		server.SetLostJobCheckRetryTime(200 * time.Millisecond)

		// a recovered running job carries the scheduler group it will be reserved
		// under (recoveredItemDef recomputes it), so once reclaimed it is reservable
		// by a runner of that group - which is what a real re-run does. Reserve the
		// same way here.
		recItem, errg := server.q.Get(key)
		So(errg, ShouldBeNil)
		So(recItem, ShouldNotBeNil)

		group := recItem.ReserveGroup

		reserveByGroup := func() *Job {
			runner, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			if errc != nil {
				return nil
			}

			defer disconnect(runner)

			j, errj := runner.ReserveScheduled(200*time.Millisecond, group)
			if errj != nil {
				return nil
			}

			return j
		}

		Convey("With its command AND runner both dead, it is confirmed dead and reclaimed after its TTR", func() {
			// the runner died / was bkilled during the downtime: overwrite the
			// recovered job's command pid AND runner pid with definitely-dead pids on
			// this (reachable) host, so confirmJobDead's both-pid check confirms death.
			So(setServerJobPid(server, key, definitelyDeadPid(t)), ShouldBeTrue)
			setServerJobRunnerPid(server, key, definitelyDeadPid(t))

			// its TTR lapses (no runner left to touch it) and it is marked Lost.
			So(waitForJobLost(server, key, 20*ttr), ShouldBeTrue)

			// post-fix: with both pids dead the job is confirmed dead and released for
			// retry, so a runner of its group can reserve it again (its slot is
			// reclaimed). pre-fix: the permanent recovered-protection makes
			// confirmOrReleaseLostJob skip the dead-check entirely, so the job stays
			// parked Lost in Run forever and this reserve never succeeds.
			var reReserved *Job

			deadline := time.Now().Add(20 * ttr)
			for time.Now().Before(deadline) {
				if j := reserveByGroup(); j != nil {
					reReserved = j

					break
				}

				time.Sleep(50 * time.Millisecond)
			}

			So(reReserved, ShouldNotBeNil)
			So(reReserved.Key(), ShouldEqual, key)
		})

		Convey("With its runner still alive, it is NOT re-reserved after its TTR (both-pid guard)", func() {
			// the command finished but the runner is still alive (slow/starved to
			// archive): dead command pid, but the runner pid is this live test process.
			// The both-pid check must refuse to confirm the job dead, so its unrecorded
			// success stays safe - the removed recovered-protection is subsumed by this
			// guard, not lost.
			So(setServerJobPid(server, key, definitelyDeadPid(t)), ShouldBeTrue)
			setServerJobRunnerPid(server, key, os.Getpid())

			So(waitForJobLost(server, key, 20*ttr), ShouldBeTrue)

			// give the confirm/retry path ample time to (wrongly) reclaim it, then
			// confirm it never did: the job stays parked Lost in Run, not reservable.
			reclaimed := false

			for range 5 {
				if j := reserveByGroup(); j != nil {
					reclaimed = true

					break
				}

				time.Sleep(200 * time.Millisecond)
			}

			So(reclaimed, ShouldBeFalse)
		})
	})
}
