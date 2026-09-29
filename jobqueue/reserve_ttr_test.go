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
	bolt "go.etcd.io/bbolt"
)

const (
	// reserveTTR is the item TTR of the manager in TestReservationTTRStartsAtHandOut,
	// the one TestJobqueueSignal's daemon uses.
	reserveTTR = 200 * time.Millisecond

	// reserveWriteStall is how long that test holds up the manager's database
	// writer, and so the reservation's write, which is well past reserveTTR and
	// well inside the ReserveWriteWait the reservation waits for.
	reserveWriteStall = 5 * reserveTTR
)

// TestReservationTTRStartsAtHandOut guards that a reservation whose write to
// disk is slow is not declared lost before its runner has even been given it.
// The TTR of a reserved item runs from the moment the queue reserves it, and
// the manager then waits up to ReserveWriteWait for the reservation to reach
// disk before handing it out. A wait longer than the TTR used to mark the job
// lost, and start confirming its runner dead, during that wait, so the runner
// was handed a job already in the lost state.
func TestReservationTTRStartsAtHandOut(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager with a short TTR whose database writer stalls during a reservation", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = reserveTTR

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: "echo reserve ttr", Cwd: testCwd, RepGroup: "reserve_ttr",
			ReqGroup: "reserve_ttr", Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		stalled := make(chan struct{})
		released := make(chan error, 1)

		go func() {
			released <- server.db.bolt.Update(func(*bolt.Tx) error {
				close(stalled)
				<-time.After(reserveWriteStall)

				return nil
			})
		}()

		<-stalled

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(<-released, ShouldBeNil)

		Convey("the runner is handed a reserved job, and its TTR starts then", func() {
			So(reserved, ShouldNotBeNil)
			So(reserved.State, ShouldEqual, JobStateReserved)

			inRun, lost, _, ok := serverJobState(server, reserved.Key())
			So(ok, ShouldBeTrue)
			So(inRun, ShouldBeTrue)
			So(lost, ShouldBeFalse)

			// and a runner that then never touches it is still caught
			So(waitForJobLost(server, reserved.Key(), 20*reserveTTR), ShouldBeTrue)
		})
	})
}
