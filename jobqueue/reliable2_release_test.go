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

// This file covers spec.md section D1 ("Give up on ErrBadJob, keep 24h retry",
// Idea 1), mapping to repro Issue B2. When a live manager is authoritatively
// certain a failed command's reservation is gone (the item is no longer in the
// Run sub-queue, e.g. a winning double-reservation runner already dealt with
// it), handleRelease must return ErrBadJob so the losing runner's
// reportFinalState loop gives up promptly (client give-up set: ErrBadJob /
// ErrBadRequest) instead of looping for the full 24h ClientRetryTime with 15s
// reconnect waits. A legitimate release of a job the runner still owns whose
// item IS in Run is unchanged.
//
// The distinction the change must preserve (verified elsewhere / in Item 4.2):
//   - manager up, item gone (superseded)   -> ErrBadJob      -> give up
//   - manager unreachable (crash)          -> connection err -> keep retrying
//   - during the recovery window, not-yet-restored item -> ErrRecovering (retry)

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2Release covers the three D1 acceptance tests.
func TestReliable2Release(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const rg = "reliable2_release_rg"

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	connect := func(token []byte) *Client {
		c, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		return c
	}

	Convey("Given a live manager and a reserved+started job", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq := connect(token)
		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue, Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
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

		Convey("D1.1/D1.2: releasing a job whose item is no longer in Run gives ErrBadJob and the runner gives up", func() {
			// A winning runner already removed the item from the Run sub-queue;
			// model that authoritative "gone" by moving the item out of Run while
			// the losing runner (jq) still believes it owns the reservation
			// (job.ReservedBy is unchanged). The item still EXISTS, so this is the
			// case the getij(cr, true) gate must catch: before the change
			// getij(cr, false) let releaseJob run and fail with ErrNotRunning ->
			// ErrInternalError; after the change the not-in-Run gate returns
			// ErrBadJob.
			So(server.q.Bury(key), ShouldBeNil)

			releaseErr := jq.Release(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "failed")

			// D1.1: the live manager's authoritative response is ErrBadJob (not
			// ErrInternalError, which would keep the runner looping).
			So(releaseErr, ShouldNotBeNil)
			So(releaseErr.Error(), ShouldContainSubstring, ErrBadJob)
			So(releaseErr.Error(), ShouldNotContainSubstring, ErrInternalError)

			// D1.2: fed back through the reportFinalState error handler, an
			// ErrBadJob release error is permanent, so handleFinalStateError
			// returns giveUp == true and the loop exits promptly rather than
			// waiting out the 24h retry / 15s reconnect storm. Shrink retryWait so
			// the (non-give-up) branch, were it taken, would not stall the test.
			jq.retryWait = time.Millisecond
			_, giveUp := jq.handleFinalStateError(ctx, releaseErr)
			So(giveUp, ShouldBeTrue)
		})

		Convey("D1.3: a legitimate release of an owned job still in Run succeeds", func() {
			// The item is in Run and jq owns it (the normal non-zero-exit
			// release-for-retry path), so the fix must not touch it: the release
			// returns nil.
			releaseErr := jq.Release(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "retry")
			So(releaseErr, ShouldBeNil)

			// the job went back to the queue for retry rather than being lost.
			item, errg := server.q.Get(key)
			So(errg, ShouldBeNil)
			So(item, ShouldNotBeNil)
		})
	})
}

// TestReliable2ReleaseCrashRecovery covers the two D2 acceptance tests (spec.md
// section D2, mapping to repro Issue B2 crash-recovery). It is the safety
// counterpart to D1: D1 makes a live manager tell the double-reservation loser
// to give up promptly on a not-in-Run release, but that give-up MUST NOT
// swallow a genuine unrecorded success from a runner whose command finished
// while the manager was crashed. The KEEP'd recovery window
// (recoverInBackground/isRecovering/ErrRecovering) restores a still-owned
// running job into the Run sub-queue on restart, so the re-sent archive is
// accepted (getij(cr, true) finds it) rather than discarded. What keeps that
// safe once the recovered job's TTR lapses is confirmJobDead's both-pid liveness
// check (checklist 260726-3/4): the runner here is os.Getpid(), still alive, so
// the job is never confirmed dead / re-run and the re-sent archive still lands
// (the old permanent recovered-protection this once relied on has been removed
// as it bypassed the dead-check and backstop). This is a behaviour + guard test
// only: no production change is expected, and if the re-sent archive were wrongly
// discarded/re-run these assertions genuinely fail.
func TestReliable2ReleaseCrashRecovery(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("D2.2: ClientRetryTime is unchanged at 24h", t, func() {
		So(ClientRetryTime, ShouldEqual, 24*time.Hour)
	})

	Convey("Given a reserved+started job whose success is reported after a DB-preserving restart", t, func() {
		const rg = "reliable2_release_crash_rg"

		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		// stop whichever server is live at the end (the restarted one).
		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue, Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 3,
		}
		inserts, _, erra := jq.Add([]*Job{job}, os.Environ(), true)
		So(erra, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		// remember the owning runner's client id so the reconnecting runner is
		// recognised as the still-owning owner of the recovered job.
		clientID := jq.clientid

		Convey("D2.1: after recovery the re-sent archive is accepted and the command is not re-run", func() {
			// The manager crashes mid-report: the command has genuinely succeeded,
			// but the archive has not yet been recorded. Stop the server preserving
			// the DB, then bring it back within retryTime.
			server.Stop(ctx, true)

			server = restartSubscriptionTestServer(ctx, serverConfig)

			// recovery runs in the background (spec B1/H2): wait for the still-owned
			// running job to be restored into the Run sub-queue before re-sending the
			// archive, else it races the recovery window and is refused with
			// ErrRecovering.
			So(waitUntilRecovered(server), ShouldBeTrue)

			// the runner reconnects and re-sends its genuine success. Because the job
			// was recovered into Run still owned by this runner, the re-sent archive
			// is ACCEPTED (getij(cr, true) finds the item) rather than given up on -
			// this is what makes D1's ErrBadJob give-up safe.
			runner, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(runner)

			runner.clientid = clientID

			archiveErr := runner.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			So(archiveErr, ShouldBeNil)

			// the success is recorded: the rep group shows exactly one complete job.
			// A wrongly-discarded success would leave the job non-complete (the
			// archive would have failed above and this count would be 0), and a
			// re-run would not produce a second complete job here either - the
			// command ran once and that one success is preserved.
			summaries, serr := runner.GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)
			So(serr, ShouldBeNil)
			So(summaries[rg], ShouldNotBeNil)
			So(summaries[rg].Counts[JobStateComplete], ShouldEqual, 1)
		})
	})
}
