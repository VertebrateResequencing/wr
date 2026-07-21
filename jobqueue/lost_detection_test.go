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
// and the false-lost coverage in reliable2_lost_test.go, e.g.
// TestReliable2OnTimeTouchedJobNeverLost) must NOT disable lost detection. A
// running job whose runner goes silent (never touches) must still be marked Lost
// within a bounded time.

import (
	"context"
	"os"
	"testing"
	"time"

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
