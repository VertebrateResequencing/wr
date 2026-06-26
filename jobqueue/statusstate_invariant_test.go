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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

// errRecomputeCompleteJobs wraps a server-response error string returned while
// recomputing ground-truth completed-job counts.
var errRecomputeCompleteJobs = errors.New("recompute completed jobs failed")

// TestStatusStateTransitionInvariant drives a real Server through every
// job-state transition path - including the TTR-induced running->lost reclaim
// and the touch/recover lost->running path that both bypass the queue change
// callback - and after each transition asserts the two-projection invariant:
//
//	(a) statusState's absolute counts equal the counts recomputed from ground
//	    truth (live queue + completed DB, per RepGroup), and
//	(b) a subscriber to the affected job receives the corresponding per-job
//	    update for exactly the subscription-relevant transitions.
//
// Both projections are emissions of the same transition event, so neither may
// silently drift from the other.
func TestStatusStateTransitionInvariant(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Both status projections stay consistent across every transition", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)

		// long default TTR so lifecycle jobs never go lost on their own; the
		// dedicated lost subtest lowers it (via SetItemTTR) just for its job. A
		// fast host check that never retries keeps the lost path deterministic.
		serverConfig.Timings.ItemTTR = time.Hour
		serverConfig.Timings.LostJobCheckTimeout = 50 * time.Millisecond
		serverConfig.Timings.LostJobCheckRetryTime = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("normal new->ready->running->complete plus bury/kick/delete", func() {
			repGroup := "invariant-lifecycle"
			ids, erra := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 2), envVars, true)
			So(erra, ShouldBeNil)
			So(ids, ShouldHaveLength, 2)

			// a Go key subscription is the `wr add --sync` path: it accepts
			// terminal/lost/live updates but not bare state changes.
			sub, errs := jq.SubscribeToJobKeys(ctx, ids)
			So(errs, ShouldBeNil)

			defer sub.Unsubscribe()

			// after add, jobs are ready
			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)

			// reserve + start the first job: ready -> running. This is a state
			// change, which a key subscription does not receive; only the count
			// projection records it. The web UI details push (asserted in its own
			// subtest) is the subscriber that observes state changes.
			job, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(jq.Started(job, os.Getpid()), ShouldBeNil)

			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)
			assertNoSubscriptionUpdate(sub)

			// complete it: running -> complete (terminal; key sub receives it)
			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			expectSubscriptionUpdate(sub, job.Key(), JobStateComplete)
			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)

			// reserve + start the second job (state change), then bury it
			// (terminal; key sub receives the buried update)
			job2, errr2 := jq.Reserve(2 * time.Second)
			So(errr2, ShouldBeNil)
			So(job2, ShouldNotBeNil)
			So(jq.Started(job2, os.Getpid()), ShouldBeNil)

			assertCountsMatchGroundTruth(ctx, server)

			So(jq.Bury(job2, &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()}, "invariant bury"), ShouldBeNil)

			expectSubscriptionUpdate(sub, job2.Key(), JobStateBuried)
			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)

			// kick it back to ready: buried -> ready (state change; not delivered
			// to the key sub, but the count projection must record it)
			kicked, errk := jq.Kick([]*JobEssence{{JobKey: job2.Key()}})
			So(errk, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)

			// delete it: ready -> deleted (removed from queue)
			deleted, errd := jq.Delete([]*JobEssence{{JobKey: job2.Key()}})
			So(errd, ShouldBeNil)
			So(deleted, ShouldEqual, 1)

			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)
		})

		Convey("web UI details subscriber receives the running state-change push", func() {
			repGroup := "invariant-statechange"
			ids, erra := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 1), envVars, true)
			So(erra, ShouldBeNil)
			So(ids, ShouldHaveLength, 1)

			// the status-details websocket is the web UI subscriber; it has
			// stateChanges enabled, so it observes the ready->running transition
			// that a key subscription filters out.
			ws, cleanup := openDetailsStateSubscription(ctx, server, token, repGroup, JobStateReady)
			defer cleanup()

			job, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(jq.Started(job, os.Getpid()), ShouldBeNil)

			// the details subscriber receives a running push for the job
			status, ok := readJStatusMatching(ws, func(status JStatus) bool {
				return status.Key == job.Key() && status.IsPushUpdate && status.State == JobStateRunning
			})
			So(ok, ShouldBeTrue)
			So(status.RepGroup, ShouldEqual, repGroup)

			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)

			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)
		})

		Convey("TTR-induced running->lost reclaim then touch/recover lost->running", func() {
			repGroup := "invariant-lost"

			// lower the TTR so this job becomes lost shortly after it starts.
			// subscriptionLostItemTTR has enough headroom that the reserve ->
			// Started setup below completes before the TTR can reclaim the job
			// (the TTR clock starts at Reserve), so jstart can't race it under the
			// race detector on a loaded CI runner, while the job still goes lost
			// promptly (the pollUntil below returns as soon as it does).
			server.SetItemTTR(subscriptionLostItemTTR)

			ids, erra := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 1), envVars, true)
			So(erra, ShouldBeNil)
			So(ids, ShouldHaveLength, 1)

			sub, errs := jq.SubscribeToJobKeys(ctx, ids)
			So(errs, ShouldBeNil)

			defer sub.Unsubscribe()

			job, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(jq.Started(job, os.Getpid()), ShouldBeNil)

			assertCountsMatchGroundTruth(ctx, server)

			// wait for the TTR callback to fire and mark the job lost. The count
			// projection (running -> lost) happens inside that callback (the
			// change callback is bypassed), so poll the ground-truth invariant.
			So(pollUntil(func() bool {
				return jobIsLost(ctx, server, job.Key())
			}), ShouldBeTrue)

			// a lost update is a Lost-kind update, which a key subscription does
			// receive (the TTR path enqueues it explicitly, bypassing the normal
			// state-change gate).
			expectSubscriptionUpdate(sub, job.Key(), JobStateLost)
			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)

			// touch/recover the job: lost -> running. This goes through the
			// serverCLI touch path, which also bypasses the change callback. We
			// supply a live snapshot so the path's subscription emission fires
			// (which is gated on a snapshot being present, not on the normal
			// subscriptionUpdateState gate), letting us assert both projections.
			killCalled, errt := jq.touch(job, &JobEndState{
				Cwd:     liveJTouchActualCwd,
				PeakRAM: 321,
				CPUtime: 4 * time.Second,
			})
			So(errt, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			// the live recovery update reports the job running again
			update := receiveSubscriptionUpdate(sub, 3*time.Second)
			So(update, ShouldNotBeNil)

			if update != nil {
				So(update.Key, ShouldEqual, job.Key())
				So(update.State, ShouldEqual, JobStateRunning)
			}

			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)

			// finish the recovered job so the test leaves a clean queue. The
			// short TTR may re-lose it first, so tolerate an intervening update.
			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			expectSubscriptionUpdateEventually(sub, job.Key(), JobStateComplete)
			assertCountsMatchGroundTruth(ctx, server)
			assertAggregateMatchesLive(server)
		})
	})
}

// assertCountsMatchGroundTruth polls until statusState's per-RepGroup counts
// (excluding the derived aggregate) equal a fresh recompute from ground truth,
// then asserts equality. Polling absorbs the brief asynchrony between a queue
// transition and the change-callback applying it; the timeout bounds the wait.
func assertCountsMatchGroundTruth(ctx context.Context, s *Server) {
	var snapshot, truth map[string]map[JobState]int

	deadline := time.After(2 * time.Second)

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		var err error

		truth, err = recomputeStatusStateCounts(ctx, s)
		So(err, ShouldBeNil)

		snapshot = statusStateWithoutAggregate(s)

		if countsEqual(snapshot, truth) {
			break
		}

		select {
		case <-deadline:
			// fall through to the assertion below for a readable diff
			So(snapshot, ShouldResemble, truth)

			return
		case <-ticker.C:
		}
	}

	So(snapshot, ShouldResemble, truth)
}

// recomputeStatusStateCounts derives the authoritative per-RepGroup absolute
// state counts from ground truth, using exactly the same accounting the server
// itself uses: live jobs come from the in-memory queue (with Lost folded into
// the reserved->running merge by statusStateCounts and itemStateToJobState),
// and completed jobs come from the on-disk completed bucket per RepGroup. This
// is the same data the `current` status snapshot and seedStatusStateFromCompletedDB
// are built from, so statusState's counts must always equal it. The returned map
// omits the statusAllRepGroups aggregate, which statusState derives internally,
// and the deleted state, which statusState intentionally accumulates for the web
// UI bar but which has no ground-truth backing (deleted jobs are removed from
// both the live queue and the completed DB). It returns an error rather than
// asserting, because it is called in a tight poll loop.
func recomputeStatusStateCounts(ctx context.Context, s *Server) (map[string]map[JobState]int, error) {
	counts := make(map[string]map[JobState]int)

	// live jobs, grouped per RepGroup
	live := make(map[string][]*Job)
	for _, job := range s.getAllQueueJobs(ctx, false) {
		live[job.RepGroup] = append(live[job.RepGroup], job)
	}

	for repGroup, jobs := range live {
		counts[repGroup] = statusStateCounts(jobs)
	}

	// completed jobs, per RepGroup, added on top of any live counts
	repGroups, err := s.db.retrieveRepGroups()
	if err != nil {
		return nil, err
	}

	for _, repGroup := range repGroups {
		complete, srerr, _ := s.getCompleteJobsByRepGroup(repGroup)
		if srerr != "" {
			return nil, fmt.Errorf("%w: %s", errRecomputeCompleteJobs, srerr)
		}

		if len(complete) == 0 {
			continue
		}

		if counts[repGroup] == nil {
			counts[repGroup] = make(map[JobState]int)
		}

		for state, n := range statusStateCounts(complete) {
			counts[repGroup][state] += n
		}
	}

	cleanGroundTruthCounts(counts)

	return counts, nil
}

// cleanGroundTruthCounts drops the deleted state (a UI-only accumulator with no
// ground truth) and any non-positive or empty entries, so the result can be
// compared against statusState's cleaned, deleted-excluded snapshot form.
func cleanGroundTruthCounts(counts map[string]map[JobState]int) {
	for repGroup, stateCounts := range counts {
		delete(stateCounts, JobStateDeleted)

		for state, n := range stateCounts {
			if n <= 0 {
				delete(stateCounts, state)
			}
		}

		if len(stateCounts) == 0 {
			delete(counts, repGroup)
		}
	}
}

// statusStateWithoutAggregate returns statusState's snapshot with the derived
// statusAllRepGroups aggregate and the deleted UI accumulator removed, so it can
// be compared against a per-RepGroup ground-truth recompute.
func statusStateWithoutAggregate(s *Server) map[string]map[JobState]int {
	snapshot := s.statusState.snapshot()
	delete(snapshot, statusAllRepGroups)

	for repGroup, stateCounts := range snapshot {
		delete(stateCounts, JobStateDeleted)

		if len(stateCounts) == 0 {
			delete(snapshot, repGroup)
		}
	}

	return snapshot
}

func countsEqual(a, b map[string]map[JobState]int) bool {
	if len(a) != len(b) {
		return false
	}

	for repGroup, aStates := range a {
		bStates, ok := b[repGroup]
		if !ok || len(aStates) != len(bStates) {
			return false
		}

		for state, n := range aStates {
			if bStates[state] != n {
				return false
			}
		}
	}

	return true
}

// assertAggregateMatchesLive asserts the statusAllRepGroups aggregate equals the
// sum of every RepGroup's live (non-terminal) state counts, which is what the
// "+all+" bar shows. This guards the aggregate half of the count projection.
func assertAggregateMatchesLive(s *Server) {
	snapshot := s.statusState.snapshot()
	want := make(map[JobState]int)

	for repGroup, stateCounts := range snapshot {
		if repGroup == statusAllRepGroups {
			continue
		}

		for state, n := range stateCounts {
			if state == JobStateComplete || state == JobStateDeleted {
				continue
			}

			want[state] += n
		}
	}

	for state, n := range want {
		if n <= 0 {
			delete(want, state)
		}
	}

	got := snapshot[statusAllRepGroups]
	if got == nil {
		got = make(map[JobState]int)
	}

	So(got, ShouldResemble, want)
}

// assertNoSubscriptionUpdate asserts that no per-job update arrives on the
// subscription within a short window. Used to prove a state-change transition
// is correctly NOT delivered to a key subscription (which filters bare state
// changes), so the refactor does not start emitting updates that didn't exist.
func assertNoSubscriptionUpdate(sub *Subscription) {
	update := receiveSubscriptionUpdate(sub, liveSubscriptionNoUpdateTimeout)
	So(update, ShouldBeNil)
}

// expectSubscriptionUpdate waits for the next per-job update on the subscription
// and asserts it reports the expected key and state. It fails (rather than
// hangs) on timeout, proving the subscription projection fired.
func expectSubscriptionUpdate(sub *Subscription, key string, state JobState) {
	update := receiveSubscriptionUpdate(sub, 3*time.Second)
	So(update, ShouldNotBeNil)

	if update == nil {
		return
	}

	So(update.Key, ShouldEqual, key)
	So(update.State, ShouldEqual, state)
}

// openDetailsStateSubscription opens a status-details websocket subscribed to
// the given RepGroup at the given state. This is the web UI subscriber (it has
// stateChanges enabled), so it receives per-job pushes for state-change
// transitions that a Go key subscription filters out. It returns the connection
// and a cleanup function. Unlike openStatusDetailsSubscription it does not
// require the job to already be running at connect time.
func openDetailsStateSubscription(
	ctx context.Context,
	server *Server,
	token []byte,
	repGroup string,
	state JobState,
) (*websocket.Conn, func()) {
	testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
	header := http.Header{}
	header.Add("Authorization", "Bearer "+string(token))

	ws, err := drainWebSocket(wsURL, header)
	So(err, ShouldBeNil)

	err = ws.WriteJSON(jstatusReq{
		Request:  jstatusRequestDetails,
		RepGroup: repGroup,
		State:    state,
	})
	So(err, ShouldBeNil)

	drainSetupUntilDetails(ws)

	return ws, func() {
		_ = ws.Close()
		testServer.Close()
	}
}

// jobIsLost reports whether the live job with the given key is currently in the
// lost state, per the same accounting the status UI uses.
func jobIsLost(ctx context.Context, s *Server, key string) bool {
	item, err := s.q.Get(key)
	if err != nil || item == nil {
		return false
	}

	job := s.itemToJob(ctx, item, false, false)

	return job != nil && job.State == JobStateLost
}

// expectSubscriptionUpdateEventually waits for a per-job update reporting the
// target state for the given key, tolerating (and skipping) any intervening
// updates for the same key. It fails on timeout, proving the projection fired.
// Used when an independent transition (e.g. a short TTR re-losing a recovered
// job) may inject an extra update before the awaited one.
func expectSubscriptionUpdateEventually(sub *Subscription, key string, state JobState) {
	deadline := time.After(3 * time.Second)

	for {
		select {
		case update, ok := <-sub.Updates():
			if !ok {
				So("subscription closed before expected update", ShouldBeBlank)

				return
			}

			if update.Key == key && update.State == state {
				return
			}
		case <-deadline:
			So("timed out waiting for subscription update", ShouldBeBlank)

			return
		}
	}
}
