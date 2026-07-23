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

// This file is the rewritten A1 churn oracle (spec.md section A1, note 1),
// modelling the reliability invariant "an alive job is never re-reserved and its
// owner's successful archive is always accepted". It supersedes the old
// .docs/reliable2/harness/reliable2_churn_test.go red oracle (which asserted the
// buggy discard behaviour of a released+re-reserved job). A job reserved+started
// by runner A with an alive PID that is never touched is parked Lost in
// SubQueueRun by the TTR callback; because it is still owned by A it can never be
// re-reserved by runner B, and A's genuinely-successful archive is accepted -
// exactly v0.36.5's lenient contract. A genuine non-owner archiver is still
// rejected with ErrMustReserve.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2HoldingRunnerArchiveAccepted covers all five A1 acceptance tests.
func TestReliable2HoldingRunnerArchiveAccepted(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 500 * time.Millisecond
		rg  = "reliable2_holding_runner_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A parked-lost alive job is never re-reserved and its owner's archive is accepted", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		connect := func() *Client {
			c, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			return c
		}

		jqA := connect()
		defer disconnect(jqA)

		jqB := connect()
		defer disconnect(jqB)

		job := &Job{
			Cmd: restFormTrue, Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jqA.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		// Runner A reserves and starts the job with our own (alive) PID, so the
		// async dead-confirmation finds the process alive and cannot remove the
		// job mid-test (the determinism trick from TestLostDetectionSilentRunner).
		reservedA, err := jqA.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reservedA, ShouldNotBeNil)
		So(jqA.Started(reservedA, os.Getpid()), ShouldBeNil)
		key := reservedA.Key()

		// Acceptance test 1: never touched, so ~1 TTR after starting the TTR
		// callback marks it Lost but parks it in SubQueueRun (an alive owner is
		// never moved out of Run). Allow a few TTRs so the assertion is not flaky.
		deadline := time.Now().Add(6 * ttr)
		lost := false
		inRun := false

		for time.Now().Before(deadline) {
			item, errg := server.q.Get(key)
			if errg == nil && item != nil {
				inRun = item.Stats().State == queue.ItemStateRun
				if j, ok := item.Data().(*Job); ok {
					j.RLock()
					lost = j.Lost
					j.RUnlock()
				}
			}

			if lost && inRun {
				break
			}

			time.Sleep(20 * time.Millisecond)
		}

		So(inRun, ShouldBeTrue)
		So(lost, ShouldBeTrue)

		// Acceptance test 2: runner B cannot re-reserve the alive-owned job. All
		// 20 Reserve calls must come back empty (no job, no error).
		reservedByB := 0
		reserveErrs := 0

		for range 20 {
			rj, rerr := jqB.Reserve(200 * time.Millisecond)
			if rerr != nil {
				reserveErrs++
			}

			if rj != nil {
				reservedByB++
			}
		}

		So(reserveErrs, ShouldEqual, 0)
		So(reservedByB, ShouldEqual, 0)

		// Acceptance test 3: A still owns the reservation, so its successful
		// archive is accepted even though the job is parked Lost.
		successEnd := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		So(jqA.Archive(reservedA, successEnd), ShouldBeNil)

		// Acceptance test 4: the rep group shows exactly one complete job (the
		// command ran once; B never re-ran it).
		summaries, serr := jqA.GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)
		So(serr, ShouldBeNil)
		So(summaries[rg], ShouldNotBeNil)
		So(summaries[rg].Counts[JobStateComplete], ShouldEqual, 1)

		// Acceptance test 5: a genuine non-owner archiver (a stale runner that
		// does not own the item) is still rejected with ErrMustReserve. Runner A
		// reserves+starts a fresh job (item in Run, owned by A); runner B, which
		// does not own it, tries to archive it and must be refused.
		job2 := &Job{
			Cmd: restFormTrue + " second", Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 3,
		}
		inserts2, _, err := jqA.Add([]*Job{job2}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts2, ShouldEqual, 1)

		reservedA2, err := jqA.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reservedA2, ShouldNotBeNil)
		So(jqA.Started(reservedA2, os.Getpid()), ShouldBeNil)

		staleArchiveErr := jqB.Archive(reservedA2, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		So(staleArchiveErr, ShouldNotBeNil)
		So(staleArchiveErr.Error(), ShouldContainSubstring, ErrMustReserve)
	})
}
