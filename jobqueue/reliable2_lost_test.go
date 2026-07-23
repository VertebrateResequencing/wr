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

// This file covers spec.md section B1 acceptance tests 2 and 3 (the pure
// v0.36.5 ttrCallback with the #550 F0 contact grace removed). Acceptance test 1
// (a genuinely silent runner is still detected Lost) is covered by the retained
// TestLostDetectionSilentRunner in lost_detection_test.go. Here we pin the two
// live-runner directions: an on-time-touched job is never lost (its on-time
// touch resets the TTR via q.Touch so ttrCallback never fires), and a job that
// did go Lost recovers to Lost==false with its TTR reset on a single late touch,
// staying parked in SubQueueRun throughout.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// serverJobState reads the server-side queue item's sub-queue state and the
// job's Lost flag / FailReason under lock. ok is false if the item is not in the
// queue.
func serverJobState(server *Server, key string) (inRun, lost bool, failReason string, ok bool) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return false, false, "", false
	}

	inRun = item.Stats().State == queue.ItemStateRun

	j, isJob := item.Data().(*Job)
	if !isJob {
		return inRun, false, "", false
	}

	j.RLock()
	lost = j.Lost
	failReason = j.FailReason
	j.RUnlock()

	return inRun, lost, failReason, true
}

// TestReliable2OnTimeTouchedJobNeverLost covers B1 acceptance test 2: a runner
// that touches within the TTR keeps its job alive (never Lost, never with a
// FailReason) and parked in SubQueueRun across several TTRs.
func TestReliable2OnTimeTouchedJobNeverLost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 500 * time.Millisecond
		rg  = "reliable2_on_time_touch_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A job touched on time is never marked Lost and stays in SubQueueRun", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " ontime", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 30,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
		key := reserved.Key()

		// Touch every ~250ms (well within the 500ms TTR) for >= 4 TTRs (2s),
		// sampling the server-side state after each touch. A single bad sample
		// fails the test, but we count instead of asserting in the tight loop.
		samples := 0
		badLost := 0
		badReason := 0
		badRun := 0
		deadline := time.Now().Add(4 * ttr)

		for time.Now().Before(deadline) {
			killed, errt := jq.Touch(reserved)
			So(errt, ShouldBeNil)
			So(killed, ShouldBeFalse)

			inRun, lost, failReason, ok := serverJobState(server, key)
			samples++

			if !ok || !inRun {
				badRun++
			}

			if lost {
				badLost++
			}

			if failReason != "" {
				badReason++
			}

			time.Sleep(ttr / 2)
		}

		So(samples, ShouldBeGreaterThanOrEqualTo, 4)
		So(badRun, ShouldEqual, 0)
		So(badLost, ShouldEqual, 0)
		So(badReason, ShouldEqual, 0)
	})
}

// TestReliable2LostJobRecoversOnTouch covers B1 acceptance test 3: a job that
// went Lost (silent past its TTR) recovers to Lost==false with its TTR reset on
// a single late touch, and the item stays in SubQueueRun.
func TestReliable2LostJobRecoversOnTouch(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 500 * time.Millisecond
		rg  = "reliable2_lost_recovery_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A Lost job recovers (Lost==false, TTR reset) on a single late touch", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " lostrecover", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 30,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		// Started with our own (alive) pid so the async dead-confirmation cannot
		// remove the job before we recover it (as in TestLostDetectionSilentRunner).
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
		key := reserved.Key()

		// Deliberately never touch until the TTR-driven Lost transition fires;
		// allow a few TTRs so the wait is not timing-flaky.
		deadline := time.Now().Add(6 * ttr)
		lost := false

		for time.Now().Before(deadline) {
			_, l, _, ok := serverJobState(server, key)
			if ok {
				lost = l
			}

			if lost {
				break
			}

			time.Sleep(20 * time.Millisecond)
		}

		So(lost, ShouldBeTrue)

		// A single late touch must recover it: kill has not been called, so Touch
		// returns killed==false, and the server clears Lost and resets the TTR.
		killed, errt := jq.Touch(reserved)
		So(errt, ShouldBeNil)
		So(killed, ShouldBeFalse)

		inRun, lostAfter, _, ok := serverJobState(server, key)
		So(ok, ShouldBeTrue)
		So(inRun, ShouldBeTrue)
		So(lostAfter, ShouldBeFalse)

		// The TTR was reset by the touch: the job stays not-Lost for at least a
		// further ~half TTR (it would not have, had the deadline not restarted).
		time.Sleep(ttr / 2)

		inRun2, lost2, _, ok2 := serverJobState(server, key)
		So(ok2, ShouldBeTrue)
		So(inRun2, ShouldBeTrue)
		So(lost2, ShouldBeFalse)
	})
}
