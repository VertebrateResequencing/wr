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

// Regression test for prodsim finding 3: opening the status web page must not
// decode every live RepGroup's archived history just to count it. The seed only
// shows how many complete jobs each RepGroup has, which the count-only
// retrieveCompleteJobStatusByRepGroup answers from keys alone. The cost is
// asserted as a count of full archived decodes (db.archivedDecodes), not as a
// timing bound.

import (
	"context"
	"strconv"
	"testing"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	statusSeedDecodeRepGroup = "status-seed-decode"
	statusSeedDecodeArchived = 500
)

// TestStatusSeedCountsWithoutDecodingHistory pins that the page-open seed
// reports a RepGroup's complete count without decoding any archived job, and
// that the counts it sends are the ones the full decode produced.
func TestStatusSeedCountsWithoutDecodingHistory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live RepGroup with archived history", t, func() {
		config, serverConfig, addr, reqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true
		seedArchivedRepGroupHistory(ctx, serverConfig, statusSeedDecodeArchived, statusSeedDecodeRepGroup)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		defer disconnect(jq)

		addStatusSeedDecodeLiveJobs(jq, reqs)

		Convey("opening the status page seeds its counts without decoding that history", func() {
			recorder := dialStatusWS(ctx, t, server, token)

			before := server.db.archivedDecodes.Load()

			So(recorder.ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
			recorder.waitQuiet()

			decodes := server.db.archivedDecodes.Load() - before

			msgs := recorder.snapshot()
			begins, ends, _ := seedBoundaries(msgs)
			So(len(begins), ShouldEqual, 1)
			So(len(ends), ShouldEqual, 1)

			So(decodes, ShouldEqual, 0)

			complete, srerr, _ := server.getCompleteJobsByRepGroup(statusSeedDecodeRepGroup)
			So(srerr, ShouldBeEmpty)

			want := statusStateCounts(complete)
			want[JobStateDependent] += historyScanLive

			got := make(map[JobState]int)

			for _, m := range msgs[begins[0]:ends[0]] {
				if m.RepGroup == statusSeedDecodeRepGroup && m.FromState == JobStateNew {
					got[m.ToState] += m.Count
				}
			}

			So(got, ShouldResemble, want)
			So(got[JobStateComplete], ShouldEqual, statusSeedDecodeArchived)
		})
	})
}

// addStatusSeedDecodeLiveJobs adds historyScanLive jobs to the RepGroup that
// depend on a dep group that never exists, so they stay dependent and the
// RepGroup's live counts cannot change while the seed is read.
func addStatusSeedDecodeLiveJobs(jq *Client, reqs *jqs.Requirements) {
	live := make([]*Job, 0, historyScanLive)
	for i := range historyScanLive {
		live = append(live, &Job{
			Cmd:          "echo status seed decode live " + strconv.Itoa(i),
			Cwd:          testCwd,
			ReqGroup:     historyScanReqGroup,
			Requirements: reqs,
			RepGroup:     statusSeedDecodeRepGroup,
			Dependencies: Dependencies{NewDepGroupDependency("status-seed-decode-never")},
		})
	}

	added, _, err := jq.Add(live, envVars, true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, historyScanLive)
}
