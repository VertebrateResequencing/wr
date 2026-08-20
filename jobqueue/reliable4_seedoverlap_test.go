//go:build reliability_repro

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

// This file is a FAST, deterministic, in-process reproducer for reliable4
// FINDING 7: the web status page showed 274 running jobs when only 4 were
// actually running, and only a page refresh corrected it
// (.docs/reliable4/prod-run-20260817.md, "FINDING 7").
//
// WHAT IT PROVES (the question the investigation had to answer first: is a
// running->X delta DROPPED, or NEVER EMITTED?). Neither. Every delta is emitted
// once and the status caster never drops (newCaster(true) queues unboundedly).
// The count is wrong because ONE TRANSITION IS COUNTED TWICE:
//
//   1. webInterfaceStatusWS starts setupUpdateListener, whose first act is
//      s.statusCaster.Join() - so the client is on the live delta feed BEFORE
//      its "current" request can even arrive over the network.
//   2. sendCurrentStatusCounts then answers "current" by SNAPSHOTTING the queue
//      (getJobsCurrent) and sending one new-><state> seed message per state.
//   3. The snapshot and the delta feed are not a consistent cut, so any
//      transition emitted after (1) and before the snapshot in (2) is reported
//      TWICE: once by its own from->to delta and once by the seed, which
//      already shows the job in its destination state.
//
// The client's occupancy model (.docs/flicker/, websocket-handler.js) is
// order-independent but cannot detect the duplicate, because deltas are
// anonymous counts, not job identities: the duplicate ready->running parks in
// pending[ready][running] and is then satisfied by the seed's (large) ready
// occupancy, permanently moving one unit of occupancy from ready to running.
// Total occupancy stays right, which is why the existing regression harness
// (jobqueue/testdata/status-count-reconcile/reconcile-harness.mjs, scenario B)
// scores this stream as "converged": it measures the TOTAL, not the per-bucket
// distribution.
//
// The prod mass exit (limit -> 0, several hundred runners exiting at once) is
// NOT the cause: it is what makes the pre-existing offset glaring, because the
// true running count falls to a handful while the offset stays.
//
// This reproducer forces the (1)-before-(2) interleaving deterministically -
// dial the status websocket, prove the caster member is live, run the
// transitions, and only THEN send "current" - which is exactly the real causal
// structure, with the browser's "RTT + scan duration" window made explicit
// instead of left to chance. It then replays the RECORDED wire stream through
// the REAL client logic via
// jobqueue/testdata/status-count-reconcile/replay-stream.mjs, so the reported
// "the web UI would show N running" is the shipped client's own answer.
//
// It was RED before the seed boundary landed, which is what it was written to
// prove. It is GREEN now, and its job from here on is to stay that way: it fails
// if the boundary is removed, or if the seed is bracketed anywhere other than
// around the whole seed walk. It stays build-tagged out of `make test` because
// it needs node to replay the recorded stream through the real status page
// client, and because its scale knobs make it far heavier than a unit test - the
// cheap always-built guard for the same boundary is
// TestReliable4StatusSeedBoundary in reliable4_seedboundary_test.go. Run it
// with:
//
//	go test -tags reliability_repro ./jobqueue/ -run TestReliable4StatusSeedOverlap -v
//
// or via `developers/wrdev.sh status-seed-overlap`.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// seedOverlapRepGroup is the rep group both shapes below use. The recording
// harness itself (wsRecorder, replayThroughRealClient, startSeedOverlapJobs, the
// scale knobs) lives in reliable4_seedboundary_test.go, which is part of the
// normal build, so the fast guard and these bigger shapes share one harness.
const seedOverlapRepGroup = "rg-seed-overlap"

// TestReliable4StatusSeedOverlap reproduces FINDING 7: a transition emitted
// after a status client joins the delta feed but before the scan-on-connect
// snapshot is counted twice, permanently inflating the `running` bar for a
// client that never reconnects.
func TestReliable4StatusSeedOverlap(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to replay the stream through the real status page client")
	}

	var (
		backlog  = seedOverlapEnvInt("WR_SO_BACKLOG", 400)
		preStart = seedOverlapEnvInt("WR_SO_PRESTART", 40)
		overlapN = seedOverlapEnvInt("WR_SO_OVERLAP", 120)
		leftover = seedOverlapEnvInt("WR_SO_LEFTOVER", 4)
	)

	ctx := context.Background()

	Convey("A transition straddling the scan-on-connect seed is counted twice", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jobs := subscriptionTestJobs(seedOverlapRepGroup, standardReqs, backlog)
		added, already, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, backlog)
		So(already, ShouldEqual, 0)

		// (a) start some jobs BEFORE any status client exists: the control set,
		// visible only in the seed.
		before := startSeedOverlapJobs(t, jq, preStart)
		So(len(before), ShouldEqual, preStart)

		// (b) connect the status page. setupUpdateListener's first act is
		// statusCaster.Join(), so from here on every delta is queued for this
		// client - and we have NOT asked for the seed yet.
		recorder := dialStatusWS(ctx, t, server, token)

		// prove the caster member is live before we run the overlap transitions:
		// one started job must produce deltas without any "current" request.
		canary := startSeedOverlapJobs(t, jq, 1)
		So(len(canary), ShouldEqual, 1)
		So(recorder.waitForMessages(1), ShouldBeTrue)

		// (c) the OVERLAP set: started after the Join, before the seed snapshot.
		overlap := startSeedOverlapJobs(t, jq, overlapN)
		So(len(overlap), ShouldEqual, overlapN)

		running := append(append(before, canary...), overlap...)
		So(len(running), ShouldEqual, preStart+1+overlapN)

		// let every one of their deltas be delivered first, so this shape measures
		// the seed overlap itself rather than the delta feed's write lag.
		recorder.waitQuiet()
		preSeedDeltas := recorder.len()
		So(preSeedDeltas, ShouldBeGreaterThan, 0)

		// (d) now ask for the seed, exactly as the browser does in onopen.
		So(recorder.ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
		recorder.waitQuiet()

		// (e) the mass exit: everything but `leftover` archives at once,
		// which is what made prod's offset glaring rather than what caused it.
		for _, job := range running[:len(running)-leftover] {
			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		}

		recorder.waitQuiet()

		msgs := recorder.snapshot()
		So(len(msgs), ShouldBeGreaterThan, 0)

		// the mechanism, independent of any client: the seed reported every
		// started job as running, AND the live feed reported the same
		// ready->running transitions for the overlap set. One transition, two
		// reports.
		seededRunning := seedMessagesFor(msgs, seedOverlapRepGroup, JobStateRunning)
		liveStarts := liveTransitionCount(msgs, seedOverlapRepGroup, JobStateReady, JobStateRunning)
		liveExits := liveTransitionCount(msgs, seedOverlapRepGroup, JobStateRunning, JobStateComplete)

		printReproLine(fmt.Sprintf("\n  %s seed_new_to_running=%d live_ready_to_running=%d overlap_set=%d",
			seedOverlapMarker, seededRunning, liveStarts, overlapN+1))

		So(seededRunning, ShouldEqual, preStart+1+overlapN)
		So(liveStarts, ShouldBeGreaterThanOrEqualTo, overlapN+1)

		// NOT a dropped and NOT a missing exit delta: every one of the mass
		// exit's running->complete transitions reached this client, exactly once.
		So(liveExits, ShouldEqual, preStart+1+overlapN-leftover)

		// truth, from the same server call the seed uses.
		truth := statusStateCounts(server.getJobsCurrent(ctx, seedOverlapRepGroup,
			RepGroupMatchExact, 0, "", false, false, false))
		So(truth[JobStateRunning], ShouldEqual, leftover)

		// what the shipped client reconstructs from the recorded wire stream,
		// with NO reconnect.
		replay := replayThroughRealClient(t, t.TempDir(), recorder.rawSnapshot(), "", false)
		shown := replay.shown
		So(replay.interleaved, ShouldEqual, 0)

		printReproLine(fmt.Sprintf("  %s live_running_to_complete=%d mass_exit_size=%d (nothing dropped)",
			seedOverlapMarker, liveExits, preStart+1+overlapN-leftover))
		printReproLine(fmt.Sprintf("  %s forced true_running=%d shown_running=%d true_ready=%d shown_ready=%d",
			seedOverlapMarker, truth[JobStateRunning], shown[seedOverlapRepGroup]["running"],
			truth[JobStateReady], shown[seedOverlapRepGroup]["ready"]))
		printReproLine(fmt.Sprintf("  %s all_bar=%v rg_bar=%v",
			seedOverlapMarker, shown[statusAllRepGroups], shown[seedOverlapRepGroup]))

		// what the boundary buys: both bars are exact. Without it, both over-count
		// running by the size of the overlap set and under-count ready by the same
		// amount, which is what these three assertions failed on before the fix and
		// what they fail on again if it is removed.
		So(shown[seedOverlapRepGroup]["running"], ShouldEqual, truth[JobStateRunning])
		So(shown[statusAllRepGroups]["running"], ShouldEqual, truth[JobStateRunning])
		So(shown[seedOverlapRepGroup]["ready"], ShouldEqual, truth[JobStateReady])
	})
}

// TestReliable4StatusSeedOverlapNaturalRace dials the status page and sends
// "current" immediately, exactly as the browser's onopen does, while jobs keep
// starting. Post-fix it is the RESIDUAL MEASUREMENT rather than the fix's proof:
// in-process the pre-snapshot part of the connect window (the request hop, and
// any delta the caster had not yet written) is microseconds, so what is left is
// the seed walk itself, and the seed boundary cannot close that. It therefore
// reports both what the shipped client shows and what a boundary-blind client
// would show on the SAME recording, times the seed walk, and asserts only that
// the bracket is there, that nothing interleaves it, and that the boundary never
// makes the residual worse. The discriminating shape is the forced one above;
// prod's window was mostly the part the boundary does close, since the browser
// was remote and the manager CPU-bound.
func TestReliable4StatusSeedOverlapNaturalRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to replay the stream through the real status page client")
	}

	var (
		backlog  = seedOverlapEnvInt("WR_SO_NAT_BACKLOG", 20000)
		leftover = seedOverlapEnvInt("WR_SO_LEFTOVER", 4)
		rampFor  = time.Duration(seedOverlapEnvInt("WR_SO_NAT_RAMPSEC", 3)) * time.Second
	)

	ctx := context.Background()

	Convey("The browser's own connect sequence double-counts whatever straddles the scan", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		added, _, err := jq.Add(subscriptionTestJobs(seedOverlapRepGroup, standardReqs, backlog), envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, backlog)

		// the ramp: jobs keep entering `running` for the whole window, as they do
		// while a load run fills the farm.
		var (
			mu      sync.Mutex
			started []*Job
			stop    = make(chan struct{})
			rampEnd = make(chan struct{})
		)

		go func() {
			defer close(rampEnd)

			for {
				select {
				case <-stop:
					return
				default:
				}

				job, errr := jq.Reserve(time.Second)
				if errr != nil || job == nil {
					return
				}

				if jq.Started(job, os.Getpid()) != nil {
					return
				}

				mu.Lock()

				started = append(started, job)

				mu.Unlock()
			}
		}()

		time.Sleep(200 * time.Millisecond)

		recorder := dialStatusWS(ctx, t, server, token)

		// exactly what websocket-handler.js does in onopen, with no pause.
		So(recorder.ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)

		time.Sleep(rampFor)
		close(stop)
		<-rampEnd

		mu.Lock()
		running := started
		mu.Unlock()

		So(len(running), ShouldBeGreaterThan, leftover)

		recorder.waitQuiet()

		for _, job := range running[:len(running)-leftover] {
			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		}

		recorder.waitQuiet()

		// the bracket is there, once, and nothing interleaved it, under real
		// concurrency on a queue of this size.
		msgs := recorder.snapshot()
		begins, ends, inside := seedBoundaries(msgs)
		So(len(begins), ShouldEqual, 1)
		So(len(ends), ShouldEqual, 1)
		So(len(inside), ShouldEqual, 0)

		truth := statusStateCounts(server.getJobsCurrent(ctx, seedOverlapRepGroup,
			RepGroupMatchExact, 0, "", false, false, false))
		So(truth[JobStateRunning], ShouldEqual, leftover)

		// quantify the residual the boundary CANNOT close: the seed's queue walk is
		// not a point in time, so a transition that happens while it is in progress
		// can be both counted by the walk and reported by a delta written after the
		// closing boundary. The window is that walk's own duration - timed here on
		// this very queue, next to the materialising walk the seed used to do - and
		// the error it can produce is the window times the transition rate.
		seedStart := time.Now()
		_, perRG := server.statusSeedCounts()
		seedMS := float64(time.Since(seedStart).Nanoseconds()) / 1e6
		So(len(perRG), ShouldBeGreaterThan, 0)

		jobsStart := time.Now()
		walked := server.getJobsCurrent(ctx, "", RepGroupMatchExact, 0, "", false, false, false)
		jobsMS := float64(time.Since(jobsStart).Nanoseconds()) / 1e6
		rate := float64(len(running)) / rampFor.Seconds()

		// the SAME recording, replayed twice: once as the shipped client reads it,
		// once with the boundary markers stripped, which is exactly what a status
		// page that predates them sees. The difference is what the boundary bought,
		// measured within this one run rather than against a remembered number.
		dir := t.TempDir()
		aware := replayThroughRealClient(t, dir, recorder.rawSnapshot(), "", false)
		blind := replayThroughRealClient(t, dir, recorder.rawSnapshot(), "", true)
		shown := aware.shown
		awareErr := aware.shown[statusAllRepGroups]["running"] - truth[JobStateRunning]
		blindErr := blind.shown[statusAllRepGroups]["running"] - truth[JobStateRunning]

		printReproLine(fmt.Sprintf("\n  %s natural ramp_started=%d true_running=%d shown_running=%d all_bar=%v",
			seedOverlapMarker, len(running), truth[JobStateRunning],
			shown[statusAllRepGroups]["running"], shown[statusAllRepGroups]))
		printReproLine(fmt.Sprintf("  %s natural bracket=%d/%d queue=%d seedwalk_ms=%.1f jobswalk_ms=%.1f "+
			"starts_per_s=%.0f residual_predicted=%.1f",
			seedOverlapMarker, len(begins), len(ends), len(walked), seedMS, jobsMS,
			rate, seedMS/1000*rate))
		printReproLine(fmt.Sprintf("  %s natural overcount_boundary_aware=%d overcount_boundary_blind=%d",
			seedOverlapMarker, awareErr, blindErr))

		// the blind replay must actually be wrong, or there is nothing to compare.
		So(blindErr, ShouldBeGreaterThan, 0)

		// nothing may be LOST (DEVELOPERS.md rule 3: a delta feed must never drop).
		// The boundary discards what it received before the seed, so it is only safe
		// because the seed already accounts for every one of those transitions - if
		// the bracket were placed after the queue walk instead of before it, a
		// transition the walk had already passed would be discarded AND absent from
		// the seed, and this would go negative.
		So(awareErr, ShouldBeGreaterThanOrEqualTo, 0)

		// and the boundary must never make the residual worse. It cannot make it
		// zero: in-process the pre-snapshot part of the window (the request hop and
		// any delta the caster had not written yet) is microseconds, so essentially
		// the whole of this shape's error is the seed walk itself - the residual the
		// boundary cannot close without locking the queue across the walk
		// (DEVELOPERS.md rule 1). Closing THAT is what the forced shape above shows,
		// and what prod's real window - a browser over the network to a CPU-bound
		// manager - is mostly made of.
		So(awareErr, ShouldBeLessThanOrEqualTo, blindErr)

	})
}

// seedOverlapEnvInt reads a scale knob from the environment, so
// `developers/wrdev.sh status-seed-overlap` can resize the reproducer without
// recompiling. See reliable4_seedoverlap_test.go for what each knob sizes.
func seedOverlapEnvInt(name string, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}

	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return def
	}

	return v
}
