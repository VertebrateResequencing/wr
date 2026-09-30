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
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// This file covers the re-sent half of
// .docs/bugfixes/260930-release-durability.md: a runner re-sends its release or
// bury when the first request timed out on a slow commit, or was answered with
// an error because its write failed. The manager finds the report already
// applied in memory, and must still not acknowledge it before the job's state is
// on disk, since that is when the runner moves on.

// releaseResendRetries is the Retries of a job whose release is re-sent with
// retries still to spare after the first release, alongside the
// releaseDurabilityRetries case where the first release spent the last spare
// one.
const releaseResendRetries = 3

// startedJobForResend adds a job with the given retries, reserves it and reports
// it started with this test process's pid, returning the manager, a client of it
// and the reserved job.
func startedJobForResend(ctx context.Context, t *testing.T, retries uint8) (*Server, *Client, *Job) {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
	serverConfig.Timings.ItemTTR = time.Minute
	serverConfig.Timings.ReleaseDelayMin = time.Millisecond

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	inserts, _, err := jq.Add([]*Job{{
		Cmd: restFormTrue + " releaseresend", Cwd: testCwd, RepGroup: releaseDurabilityRepGroup,
		ReqGroup: releaseDurabilityRepGroup, Requirements: standardReqs, Retries: retries,
	}}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

	return server, jq, reserved
}

// TestReleaseDurabilityResend proves that a re-sent release or bury, which the
// manager has already applied, is acknowledged, and only once a write covering
// the job's state has committed.
func TestReleaseDurabilityResend(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job a runner has released after a failed attempt", t, func() {
		server, jq, job := startedJobForResend(ctx, t, releaseResendRetries)

		defer server.Stop(ctx, true)
		defer disconnect(jq)

		So(jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit), ShouldBeNil)

		Convey("a re-sent release is not acknowledged while writes are held off disk", func() {
			_, acked := reportWithWriteHeld(server, func() error {
				return jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit)
			})

			So(acked, ShouldBeFalse)

			jobs, err := jq.GetByRepGroup(releaseDurabilityRepGroup, false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].UntilBuried, ShouldEqual, releaseResendRetries)
		})
	})

	Convey("Given a job a runner has released after a failed attempt at its last spare retry", t, func() {
		server, jq, job := startedJobForResend(ctx, t, releaseDurabilityRetries)

		defer server.Stop(ctx, true)
		defer disconnect(jq)

		So(jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit), ShouldBeNil)

		Convey("a re-sent release is acknowledged and leaves the job to be retried, its retry spent once", func() {
			So(jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit), ShouldBeNil)

			jobs, err := jq.GetByRepGroup(releaseDurabilityRepGroup, false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldBeIn, []JobState{JobStateDelayed, JobStateReady})
			So(jobs[0].UntilBuried, ShouldEqual, releaseDurabilityRetries)
		})
	})

	Convey("Given a job a runner has buried", t, func() {
		server, jq, job := startedJobForResend(ctx, t, releaseDurabilityRetries)

		defer server.Stop(ctx, true)
		defer disconnect(jq)

		So(jq.Bury(job, failedAttempt(), FailReasonExit), ShouldBeNil)

		Convey("a re-sent bury is acknowledged, but not while writes are held off disk", func() {
			_, acked := reportWithWriteHeld(server, func() error {
				return jq.Bury(job, failedAttempt(), FailReasonExit)
			})

			So(acked, ShouldBeFalse)
		})
	})
}
