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
	"slices"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// TestReserveRefusedForDoomedElement covers the server side of DEVELOPERS.md
// rule 5: a runner whose scheduler element the scheduler has decided to kill as
// excess (so ClaimForReserve refuses it) is given no job, as if none were ready,
// and nothing is left half-reserved, so the job and its limit group slot go to
// the next runner.
func TestReserveRefusedForDoomedElement(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		rg     = "excess_claim_rg"
		doomed = "4242[2]"
		wanted = "4242[1]"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	var (
		claimsMu sync.Mutex
		claims   []string
	)

	serverConfig.SchedulerName = schedulerNameMock
	serverConfig.SchedulerConfig = &jqs.ConfigMock{
		RunnerFunc: func(context.Context, string) {},
		ClaimForReserveFunc: func(schedulerID string) bool {
			claimsMu.Lock()
			defer claimsMu.Unlock()

			claims = append(claims, schedulerID)

			return schedulerID != doomed
		},
	}

	Convey("Given a server whose scheduler refuses claims from a doomed element", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		connect := func(schedulerID string) *Client {
			jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			jq.SetReserveSchedulerID(schedulerID)

			return jq
		}

		jqDoomed := connect(doomed)
		defer disconnect(jqDoomed)

		jqWanted := connect(wanted)
		defer disconnect(jqWanted)

		_, _, err = jqDoomed.Add([]*Job{{
			Cmd: restFormTrue + " excessclaim", Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, LimitGroups: []string{"excessclaim:1"},
		}}, os.Environ(), true)
		So(err, ShouldBeNil)

		Convey("the doomed element's runner gets no job, and the next runner gets it", func() {
			job, errr := jqDoomed.Reserve(time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldBeNil)

			job, errr = jqWanted.Reserve(time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)

			claimsMu.Lock()
			defer claimsMu.Unlock()

			So(slices.Contains(claims, doomed), ShouldBeTrue)
			So(slices.Contains(claims, wanted), ShouldBeTrue)
		})

		Convey("an old runner that sends no scheduler id is not asked about and still gets the job", func() {
			jqOld := connect("")
			defer disconnect(jqOld)

			job, errr := jqOld.Reserve(time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)

			claimsMu.Lock()
			defer claimsMu.Unlock()

			So(slices.Contains(claims, ""), ShouldBeFalse)
		})
	})
}
