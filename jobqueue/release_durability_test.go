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
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// This file covers .docs/bugfixes/260930-release-durability.md: a runner whose
// command failed releases (or buries) the job and, once that is acknowledged,
// moves on to other work. The manager acknowledged before the release was on
// disk, so a crash straight after left the durable start record: recovery
// parked the job in Run as "running", and because the runner's pid was still
// alive (it had moved on, not died) the job was never confirmed dead and never
// re-ran, as soak6 recorded for job ccfca92b after its 22:52:19 crash.
//
// As in start_durability_test.go, the manager's single bolt write lock is held
// while the release is made, and the crash image is taken the instant the
// release is acknowledged: a manager that answers before its write commits is
// snapshotted without it, whatever the timing of the best-effort writer.

const (
	// releaseDurabilityRepGroup names the one job these tests add.
	releaseDurabilityRepGroup = "release_durability"

	// releaseDurabilityTTR is the recovered manager's TTR, short so that a job
	// parked in Run is checked for loss well within the test's waits.
	releaseDurabilityTTR = time.Second

	// releaseDurabilityReserveWait bounds how long a fresh runner is given to
	// reserve the released job from the recovered manager.
	releaseDurabilityReserveWait = 10 * time.Second

	// releaseDurabilityRetries is the released job's Retries.
	releaseDurabilityRetries = 1

	// releaseDurabilityExitCode is the failed attempt's exit code.
	releaseDurabilityExitCode = 3
)

// errReportNeverReturned stands in for the release or bury error when the call
// did not come back at all within startDurabilityAckWait.
var errReportNeverReturned = errors.New("release or bury did not return")

// releaseDurabilityCrash is what releaseDurabilityCrashAndRecover leaves behind:
// a recovered manager, whether the pre-crash manager acknowledged the report
// while its write was held off disk, and a client of the recovered manager.
type releaseDurabilityCrash struct {
	server              *Server
	jq                  *Client
	key                 string
	ackedWhileWriteHeld bool
}

// releaseDurabilityCrashAndRecover adds a job, reserves it, reports it started
// with this test process's (live) pid, then calls report (the runner's release
// or bury after a failed attempt) with the bolt write lock held. It snapshots
// committed state the moment report returns, crashes the manager onto that
// snapshot, and restarts it with a short TTR. The runner's pid stays alive
// throughout, as the soak's runner did when it moved on to other jobs.
func releaseDurabilityCrashAndRecover(ctx context.Context, t *testing.T,
	report func(jq *Client, job *Job) error,
) *releaseDurabilityCrash {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
	serverConfig.Timings.ItemTTR = time.Minute
	serverConfig.Timings.ReleaseDelayMin = time.Millisecond

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	inserts, _, err := jq.Add([]*Job{{
		Cmd: restFormTrue + " releasedurable", Cwd: testCwd, RepGroup: releaseDurabilityRepGroup,
		ReqGroup: releaseDurabilityRepGroup, Requirements: standardReqs, Retries: releaseDurabilityRetries,
	}}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

	crashImage, acked := reportWithWriteHeld(server, func() error { return report(jq, reserved) })

	server.Stop(ctx, true)
	disconnect(jq)

	So(os.WriteFile(serverConfig.DBFile, crashImage.Bytes(), 0o600), ShouldBeNil)

	serverConfig.dontWipeDevDB = true
	serverConfig.Timings.ItemTTR = releaseDurabilityTTR

	server, _, token, err = serve(ctx, serverConfig)
	So(err, ShouldBeNil)
	So(waitUntilRecovered(server), ShouldBeTrue)

	server.SetLostJobCheckTimeout(2 * time.Second)
	server.SetLostJobCheckRetryTime(200 * time.Millisecond)

	jq2, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	return &releaseDurabilityCrash{server: server, jq: jq2, key: reserved.Key(), ackedWhileWriteHeld: acked}
}

// reportWithWriteHeld calls report with the manager's bolt write lock held and
// returns the committed DB image as of the moment report returned, and whether
// it returned while the lock was still held. The lock is let go after
// startDurabilityHoldWrite for a manager that waits for its own write.
func reportWithWriteHeld(server *Server, report func() error) (*bytes.Buffer, bool) {
	holdTx, err := server.db.bolt.Begin(true)
	So(err, ShouldBeNil)

	done := make(chan error, 1)

	go func() {
		done <- report()
	}()

	crashImage := &bytes.Buffer{}

	select {
	case err = <-done:
		So(server.BackupDB(crashImage), ShouldBeNil)
		So(holdTx.Rollback(), ShouldBeNil)
		So(err, ShouldBeNil)

		return crashImage, true
	case <-time.After(startDurabilityHoldWrite):
		So(holdTx.Rollback(), ShouldBeNil)
	}

	select {
	case err = <-done:
	case <-time.After(startDurabilityAckWait):
		err = errReportNeverReturned
	}

	So(err, ShouldBeNil)
	So(server.BackupDB(crashImage), ShouldBeNil)

	return crashImage, false
}

// recoveredJob returns the recovered manager's view of the one job.
func (c *releaseDurabilityCrash) recoveredJob() *Job {
	jobs, err := c.jq.GetByRepGroup(releaseDurabilityRepGroup, false, 0, "", false, false)
	So(err, ShouldBeNil)
	So(len(jobs), ShouldEqual, 1)

	return jobs[0]
}

// failedAttempt is the end state of a run whose command exited non-zero.
func failedAttempt() *JobEndState {
	return &JobEndState{Exited: true, Exitcode: releaseDurabilityExitCode, EndTime: time.Now()}
}

// TestReleaseDurability proves that a job released after a failed attempt, just
// before the manager crashed, recovers as released with its retry spent and is
// run again, even though the runner that released it is still alive.
func TestReleaseDurability(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager that crashed right after acknowledging a release after a failed attempt", t, func() {
		crash := releaseDurabilityCrashAndRecover(ctx, t, func(jq *Client, job *Job) error {
			return jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit)
		})

		defer crash.server.Stop(ctx, true)
		defer disconnect(crash.jq)

		t.Logf("release acknowledged while its write was held off disk: %t", crash.ackedWhileWriteHeld)

		Convey("the job is not running, has spent a retry, and a fresh runner reserves it", func() {
			// read before anyone can change it: reserving moves the job on.
			recovered := crash.recoveredJob()

			// what a runner the recovered manager scheduled would do.
			var again *Job

			deadline := time.Now().Add(releaseDurabilityReserveWait)
			for time.Now().Before(deadline) && again == nil {
				again, _ = crash.jq.Reserve(200 * time.Millisecond) //nolint:errcheck // retried until the deadline
			}

			t.Logf("recovered state %s; fresh runner reserved it within %s: %t",
				recovered.State, releaseDurabilityReserveWait, again != nil)

			So(recovered.State, ShouldBeIn, []JobState{JobStateDelayed, JobStateReady})
			So(recovered.UntilBuried, ShouldEqual, releaseDurabilityRetries)
			So(recovered.Exitcode, ShouldEqual, releaseDurabilityExitCode)
			So(again, ShouldNotBeNil)
			So(again.Key(), ShouldEqual, crash.key)

			// the backstop: on a box slow enough for the release round trip to outlast
			// the hold, the image includes the write whatever the manager does.
			So(crash.ackedWhileWriteHeld, ShouldBeFalse)
		})
	})
}

// TestReleaseDurabilityBury proves that a job buried just before the manager
// crashed recovers buried, not running, while its runner is still alive.
func TestReleaseDurabilityBury(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager that crashed right after acknowledging a bury", t, func() {
		crash := releaseDurabilityCrashAndRecover(ctx, t, func(jq *Client, job *Job) error {
			return jq.Bury(job, failedAttempt(), FailReasonExit)
		})

		defer crash.server.Stop(ctx, true)
		defer disconnect(crash.jq)

		t.Logf("bury acknowledged while its write was held off disk: %t", crash.ackedWhileWriteHeld)

		Convey("the job is recovered buried", func() {
			So(crash.recoveredJob().State, ShouldEqual, JobStateBuried)
			So(crash.ackedWhileWriteHeld, ShouldBeFalse)
		})
	})
}
