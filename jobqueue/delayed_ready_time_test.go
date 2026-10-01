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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDelayedJobReportsWhenItWillBeReady(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	// long enough that the job is still delayed when we look at it
	serverConfig.Timings.ReleaseDelayMin = time.Minute

	// a runner that finds it has not enough time left to run a job releases it
	// with no exit state, so the job's EndTime stays as its reservation zeroed
	// it: when it will become ready can only come from the queue.
	Convey("Given a job a runner reserved and then released with no exit state", t, func() {
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd:          "echo delayed ready time",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(3),
			RepGroup:     "delayed_ready_time",
		}

		inserts, _, err := jq.Add([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		got, err := jq.GetByEssence(job.ToEssense(), false, false)
		So(err, ShouldBeNil)
		So(got.State, ShouldEqual, JobStateReady)
		So(got.ReadyTime.IsZero(), ShouldBeTrue)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		releasedFrom := time.Now()

		So(jq.Release(reserved, nil, "not enough time to run"), ShouldBeNil)

		releasedBy := time.Now()

		Convey("a client sees when the queue will make it ready", func() {
			got, err = jq.GetByEssence(job.ToEssense(), false, false)
			So(err, ShouldBeNil)
			So(got.State, ShouldEqual, JobStateDelayed)
			So(got.EndTime.IsZero(), ShouldBeTrue)
			So(got.DelayTime, ShouldBeGreaterThanOrEqualTo, time.Minute)
			So(got.ReadyTime, ShouldHappenOnOrBetween,
				releasedFrom.Add(got.DelayTime), releasedBy.Add(got.DelayTime))
		})
	})
}
