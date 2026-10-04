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

	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

// TestZeroClientIDReportsRefused checks that the manager refuses a runner
// report carrying the zero client ID. A job that was never reserved has the
// zero ReservedBy, so without the refusal a hand-made request with no client ID
// passes the "you reserved this job" check and can start, touch, archive,
// release or bury a job that never ran.
func TestZeroClientIDReportsRefused(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const rg = "zero_client_id_rg"

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	connect := func(token []byte) *Client {
		c, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		return c
	}

	Convey("Given a live manager with a job that was never reserved", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq := connect(token)
		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " zero client id", Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 3,
		}
		inserts, _, erra := jq.Add([]*Job{job}, os.Environ(), true)
		So(erra, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := job.Key()

		jobNow := func() *Job {
			got, errg := jq.GetByEssence(&JobEssence{JobKey: key}, false, false)
			So(errg, ShouldBeNil)
			So(got, ShouldNotBeNil)

			return got
		}

		before := jobNow()
		So(before.State, ShouldEqual, JobStateReady)

		zero := connect(token)
		defer disconnect(zero)

		zero.clientid = uuid.Nil

		endState := &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}

		for _, cr := range []*clientRequest{
			{Method: requestMethodStart, Keys: []string{key}, Job: &Job{
				Host: localhost, Pid: os.Getpid(), RunnerPid: os.Getpid(), StartTime: time.Now(),
			}},
			{Method: requestMethodTouch, Keys: []string{key}},
			{
				Method: archiveBeforeStartMethod, Keys: []string{key},
				JobEndState: &JobEndState{Exited: true, EndTime: time.Now()},
			},
			{Method: "jrelease", Keys: []string{key}, JobEndState: endState, FailReason: FailReasonExit},
			{Method: "jbury", Keys: []string{key}, JobEndState: endState, FailReason: FailReasonExit},
		} {
			Convey("a "+cr.Method+" request with the zero client ID is refused and leaves the job alone", func() {
				// an archive is also refused for a job with no start, so give the
				// job the start a previous run leaves behind when it goes back to
				// the ready queue to run again with no reserver, as a rerun does.
				startTime := time.Now().Add(-time.Minute)
				setServerJobStartTime(server, key, startTime)

				_, errr := zero.request(cr)
				So(errr, ShouldNotBeNil)
				So(errr.Error(), ShouldContainSubstring, ErrBadRequest)

				after := jobNow()
				So(after.State, ShouldEqual, before.State)
				So(after.Attempts, ShouldEqual, before.Attempts)
				So(after.UntilBuried, ShouldEqual, before.UntilBuried)
				So(after.FailReason, ShouldEqual, before.FailReason)
				So(after.StartTime.Equal(startTime), ShouldBeTrue)
			})
		}

		Convey("the client that reserves it can still release it, then bury it", func() {
			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, key)
			So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
			So(jq.Release(reserved, endState, FailReasonExit), ShouldBeNil)
			So(jobNow().State, ShouldEqual, JobStateDelayed)

			So(jq.Bury(reserved, endState, FailReasonExit), ShouldBeNil)
			So(jobNow().State, ShouldEqual, JobStateBuried)
		})
	})
}

// setServerJobStartTime sets the start time of the server's copy of the keyed
// job.
func setServerJobStartTime(server *Server, key string, startTime time.Time) {
	item, err := server.q.Get(key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	job.Lock()
	job.StartTime = startTime
	job.Unlock()
}
