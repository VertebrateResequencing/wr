/*******************************************************************************
 * TEMP reliability-v2 reproduction (not for merge). Drop into jobqueue/ and run:
 *   env -u OS_AUTH_URL -u OS_USERNAME ... CGO_ENABLED=1 go test -tags netgo \
 *       -race -run 'TestReliable2' -count=1 ./jobqueue
 *
 * Targets the failure the REAL client (portal_builder) hits at LSF scale that
 * the v1 work (#548/#550) does NOT fix: under manager saturation a still-alive
 * running job is flipped out of SubQueueRun (TTR expiry on a backlogged touch)
 * and RE-RESERVED by another runner before the original runner's *successful*
 * archive arrives, so that successful work is DISCARDED and the command is
 * re-run (near-zero forward progress; "jobs end up lost"; the non-complete
 * removal is broadcast to the web UI as "deleted").
 *
 * #548's TestReliableFalseLostRerun covers the SINGLE-runner lost-then-archive
 * case (no re-reservation) and passes on current code. This adds the missing
 * piece: a RE-RESERVATION between the loss and the original's archive.
 ******************************************************************************/

package jobqueue

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2DoubleReservationDiscardsSuccess is the deterministic minimal
// repro. Runner A reserves+starts a job and (as happens under saturation) the
// manager loses it; it is released and RE-RESERVED by runner B; runner A then
// reports a successful completion for the work it actually did. On current code
// A's success is rejected (ErrMustReserve/ErrBadJob) and the command is re-run
// by B - the work A did is thrown away. A correct fix must not discard A's
// successful work (accept it, or never have re-reserved an alive job).
func TestReliable2DoubleReservationDiscardsSuccess(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = 500 * time.Millisecond
	serverConfig.Timings.ReleaseDelayMin = time.Millisecond // make a released job re-reservable ~immediately

	const rg = "reliable2_double_reservation_rg"

	Convey("A re-reserved job must not discard the original runner's successful work", t, func() {
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

		// Runner A reserves and starts the job (and in reality keeps running it).
		reservedA, err := jqA.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reservedA, ShouldNotBeNil)
		So(jqA.Started(reservedA, 111), ShouldBeNil)

		// The saturated manager loses A and releases the job back to the queue.
		lostEnd := &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()}
		So(jqA.Release(reservedA, lostEnd, FailReasonLost), ShouldBeNil)

		// Runner B re-reserves the SAME job and starts re-running it (wasted work).
		var reservedB *Job
		for range 50 {
			reservedB, err = jqB.Reserve(200 * time.Millisecond)
			if err == nil && reservedB != nil {
				break
			}
		}

		So(reservedB, ShouldNotBeNil)
		So(reservedB.Key(), ShouldEqual, reservedA.Key())
		So(jqB.Started(reservedB, 222), ShouldBeNil)

		// Runner A's command actually SUCCEEDED; it now reports completion.
		successEnd := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		archiveErrA := jqA.Archive(reservedA, successEnd)

		summaries, serr := jqA.GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)
		So(serr, ShouldBeNil)

		t.Logf("RESULT archiveErrA=%v counts=%v (A's successful work discarded => job re-run by B)",
			archiveErrA, summaries[rg].Counts)

		// DOCUMENTS THE BUG on current code: A's genuine success is rejected, so
		// the command is needlessly re-run. A correct fix should make this pass
		// with archiveErrA == nil (A's work recorded complete) OR ensure the job
		// was never re-reserved while A was alive. Flip these asserts once fixed.
		So(archiveErrA, ShouldNotBeNil)
		So(summaries[rg].Counts[JobStateComplete], ShouldEqual, 0)
	})
}
