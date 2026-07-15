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

// This file guards the other half of the lost-contact contract: the fix that
// stops a still-touched running job being falsely marked Lost (see ttrCallback
// and TestReliableFalseLostUnderSaturation) must NOT disable lost detection. A
// running job whose runner goes silent (never touches) must still be marked Lost
// within a bounded time.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestLostDetectionSilentRunner(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 500 * time.Millisecond
		rg  = "lost_detection_silent_runner"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A running job whose runner stops touching it is still marked Lost", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " silent", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 30,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		// Started with our own (alive) pid so the async dead-confirmation cannot
		// remove the job before we observe the Lost flag; we are asserting the
		// TTR-driven Lost transition itself, not the subsequent kill.
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
		key := reserved.Key()

		// we deliberately never touch it. Detection happens ~1 TTR after the last
		// contact; allow a generous few TTRs so the assertion is not timing-flaky.
		deadline := time.Now().Add(6 * ttr)
		lost := false

		for time.Now().Before(deadline) {
			item, errg := server.q.Get(key)
			if errg == nil && item != nil {
				if j, ok := item.Data().(*Job); ok {
					j.RLock()
					lost = j.Lost
					j.RUnlock()
				}
			}

			if lost {
				break
			}

			time.Sleep(20 * time.Millisecond)
		}

		t.Logf("RESULT lost=%v after up to %v", lost, 6*ttr)

		So(lost, ShouldBeTrue)
	})
}

// TestLostDetectionRecentContactNotLost pins down both directions of the
// lost-contact decision at its source by calling ttrCallback directly, so it is
// fully deterministic and does not depend on when the background TTR sweeper
// happens to fire. A running job whose runner contacted the manager within the
// TTR is kept running even though its TTR lapsed (the touch was merely processed
// late under load); a running job with no such contact is marked Lost. The first
// case fails on pre-fix code, which had no contact check and always marked the
// job Lost once its TTR fired.
func TestLostDetectionRecentContactNotLost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const rg = "lost_detection_recent_contact"

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	// We call ttrCallback ourselves to keep this test timing-independent, so the
	// background TTR sweeper must NOT also fire on our jobs and race those direct
	// calls; a long ItemTTR keeps the sweeper asleep for the whole test. ItemTTR
	// only sets contactedWithin's window here, and each case turns on our explicit
	// recordJobContact or its deliberate absence, so the long value does not
	// weaken either assertion.
	serverConfig.Timings.ItemTTR = time.Hour

	Convey("ttrCallback keeps a recently-contacted running job but loses a silent one", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// reserveStarted adds, reserves and starts a distinctly-named job with our
		// own (alive) pid, returning the server-side *Job (the exact object
		// ttrCallback locks) and its key. Using os.Getpid() means the async
		// dead-confirmation for a job we mark Lost finds the process alive and does
		// nothing, so it cannot race our assertions (as in TestLostDetectionSilentRunner).
		// The two cases below use separate jobs so they are independent within this
		// one server, without restarting it (which would recover the first job).
		reserveStarted := func(name string) (*Job, string) {
			job := &Job{
				Cmd: restFormTrue + " " + name, Cwd: testCwdPath, RepGroup: rg,
				ReqGroup: rg, Requirements: standardReqs, Retries: 30,
			}
			_, _, errAdd := jq.Add([]*Job{job}, os.Environ(), true)
			So(errAdd, ShouldBeNil)

			reserved, errRes := jq.Reserve(2 * time.Second)
			So(errRes, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

			key := reserved.Key()
			item, errGet := server.q.Get(key)
			So(errGet, ShouldBeNil)
			So(item, ShouldNotBeNil)

			qjob, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			return qjob, key
		}

		// Case A (the fix): a runner that contacted us within the TTR is kept
		// running, not marked Lost, even though its TTR fired. handleTouch records
		// the contact via recordJobContact as early as possible, before any queue
		// lock; the recent contact proves the runner is alive and merely slow. This
		// case fails on pre-fix code, which lacked the contactedWithin check and
		// always marked the job Lost.
		recentJob, recentKey := reserveStarted("recent")
		server.recordJobContact(recentKey)

		recentSub := server.ttrCallback(ctx, recentJob)
		So(recentSub, ShouldEqual, queue.SubQueueRun)

		recentJob.RLock()
		recentLost := recentJob.Lost
		recentReason := recentJob.FailReason
		recentJob.RUnlock()

		So(recentLost, ShouldBeFalse)
		So(recentReason, ShouldBeBlank)

		// Case B (lost detection still works): a running job with no recent contact
		// is marked Lost. We deliberately never recordJobContact for this job, so
		// contactedWithin is false and the TTR firing means the runner really has
		// gone silent. This guards the negative direction and passes both with and
		// without the fix.
		silentJob, _ := reserveStarted("silent")

		silentSub := server.ttrCallback(ctx, silentJob)
		So(silentSub, ShouldEqual, queue.SubQueueRun)

		silentJob.RLock()
		silentLost := silentJob.Lost
		silentReason := silentJob.FailReason
		silentJob.RUnlock()

		So(silentLost, ShouldBeTrue)
		So(silentReason, ShouldEqual, FailReasonLost)
	})
}
