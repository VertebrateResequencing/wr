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

// This file holds the fast, deterministic regression guard for reliable4
// FINDING 7 (the web status page showed 274 running with 4 actually running,
// and only a page refresh corrected it), plus the status-websocket recording
// harness shared with the bigger, build-tagged reproducer in
// reliable4_seedoverlap_test.go.
//
// The defect: setupUpdateListener joins s.statusCaster as its first act, so a
// status client is on the live delta feed before its "current" request can even
// arrive; sendCurrentStatusCounts then answers by snapshotting the queue, so
// every transition emitted between the join and the snapshot is reported TWICE -
// once as its own from->to delta and once by the seed, which already shows the
// job in its destination state. Deltas are anonymous counts, not job identities,
// so the client's occupancy model cannot spot the duplicate and one unit of
// occupancy moves permanently from the source bucket to the destination one.
//
// The fix: the server brackets the seed with jstatusSeedBoundary markers, under
// the connection's write mutex so nothing interleaves them, and the client
// resets to the seed on the "begin" marker, discarding everything it received
// before it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	seedOverlapQuiet   = 1500 * time.Millisecond
	seedOverlapMaxWait = 60 * time.Second

	// seedOverlapMarker prefixes every measurement line so
	// `developers/wrdev.sh status-seed-overlap` can parse them (and hard-FAIL
	// when they are absent).
	seedOverlapMarker = "SEED-OVERLAP-REPRO"

	// seedBoundaryRepGroup is the rep group the fast guard below uses.
	seedBoundaryRepGroup = "rg-seed-boundary"
)

// seedCountsDependentCmd and seedCountsDelayedCmd name the two jobs
// TestReliable4StatusSeedCounts parks in `dependent` and `delay`, so the one it
// reserves on purpose can be identified.
const (
	seedCountsDependentCmd = "rg-seed-counts-dependent"
	seedCountsDelayedCmd   = "rg-seed-counts-delayed"
)

// statusWSMessage is every field the status websocket sends on the count-related
// paths: a jstateCount delta, or a jstatusSeedBoundary marker. One struct
// decodes both because the wire is JSON, so a field the sender omitted is simply
// left zero - which is also why adding the marker cannot upset an older decoder.
type statusWSMessage struct {
	RepGroup     string
	FromState    JobState
	ToState      JobState
	Count        int
	SeedBoundary string
}

// isDelta reports whether this is a state-count delta (a live one or a seed
// count) rather than a boundary marker.
func (m statusWSMessage) isDelta() bool {
	return m.RepGroup != ""
}

// seedBoundaries returns the indexes of the seed boundary markers in a recorded
// stream, plus the indexes of any live delta that arrived inside a bracket -
// which the connection's write mutex must make impossible, since that
// atomicity is what lets the client discard the pre-seed stream wholesale.
func seedBoundaries(msgs []statusWSMessage) (begins, ends, inside []int) {
	depth := 0

	for i, msg := range msgs {
		switch {
		case msg.SeedBoundary == seedBoundaryBegin:
			begins = append(begins, i)
			depth++
		case msg.SeedBoundary == seedBoundaryEnd:
			ends = append(ends, i)
			depth--
		case depth > 0 && msg.isDelta() && msg.FromState != JobStateNew:
			inside = append(inside, i)
		}
	}

	return begins, ends, inside
}

// liveTransitionCount returns the total Count of live (non-seed) deltas for the
// given rep group and from->to pair.
func liveTransitionCount(msgs []statusWSMessage, repGroup string, from, to JobState) int {
	total := 0

	for _, m := range msgs {
		if m.RepGroup == repGroup && m.FromState == from && m.ToState == to {
			total += m.Count
		}
	}

	return total
}

// seedMessagesFor returns the total Count of scan-on-connect seed messages
// (FromState "new") for the given rep group and to-state.
func seedMessagesFor(msgs []statusWSMessage, repGroup string, to JobState) int {
	total := 0

	for _, m := range msgs {
		if m.RepGroup == repGroup && m.FromState == JobStateNew && m.ToState == to {
			total += m.Count
		}
	}

	return total
}

// wsRecorder reads every message a status websocket sends and keeps the RAW
// payloads in arrival order, which is what a browser applies. They are kept
// verbatim rather than decoded so a message type the recorder does not model (a
// seed boundary marker) survives the recording intact, and the replay judges the
// shipped client's real message routing.
type wsRecorder struct {
	sync.Mutex

	ws   *websocket.Conn
	raw  [][]byte
	done chan struct{}
}

func newWSRecorder(ws *websocket.Conn) *wsRecorder {
	r := &wsRecorder{ws: ws, done: make(chan struct{})}

	go r.read()

	return r
}

// dialStatusWS starts a status page websocket handler for server and dials it,
// returning a recorder of everything it sends. The dial is what makes
// setupUpdateListener join the never-drop status caster, so every delta from
// here on is queued for this client - before any "current" request exists.
func dialStatusWS(ctx context.Context, t *testing.T, server *Server, token []byte) *wsRecorder {
	t.Helper()

	testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
	t.Cleanup(testServer.Close)

	header := http.Header{}
	header.Add("Authorization", "Bearer "+string(token))

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(testServer.URL, "http"), header)
	So(err, ShouldBeNil)

	t.Cleanup(func() {
		ws.Close()
	})

	return newWSRecorder(ws)
}

func (r *wsRecorder) read() {
	defer close(r.done)

	for {
		_, payload, err := r.ws.ReadMessage()
		if err != nil {
			return
		}

		kept := make([]byte, len(payload))
		copy(kept, payload)

		r.Lock()
		r.raw = append(r.raw, kept)
		r.Unlock()
	}
}

func (r *wsRecorder) len() int {
	r.Lock()
	defer r.Unlock()

	return len(r.raw)
}

// rawSnapshot returns the raw payloads recorded so far, in arrival order.
func (r *wsRecorder) rawSnapshot() []json.RawMessage {
	r.Lock()
	defer r.Unlock()

	out := make([]json.RawMessage, len(r.raw))
	for i, payload := range r.raw {
		out[i] = json.RawMessage(payload)
	}

	return out
}

// snapshot returns the decoded messages recorded so far, in arrival order.
func (r *wsRecorder) snapshot() []statusWSMessage {
	raws := r.rawSnapshot()
	msgs := make([]statusWSMessage, 0, len(raws))

	for _, payload := range raws {
		var msg statusWSMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		msgs = append(msgs, msg)
	}

	return msgs
}

// waitForMessages waits until at least n messages have been recorded.
func (r *wsRecorder) waitForMessages(n int) bool {
	deadline := time.Now().Add(seedOverlapMaxWait)
	for time.Now().Before(deadline) {
		if r.len() >= n {
			return true
		}

		time.Sleep(20 * time.Millisecond)
	}

	return false
}

// waitQuiet waits until no new message has arrived for seedOverlapQuiet, so the
// whole delta backlog has been delivered. The client never reconnects.
func (r *wsRecorder) waitQuiet() {
	deadline := time.Now().Add(seedOverlapMaxWait)
	last, lastChange := r.len(), time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)

		if now := r.len(); now != last {
			last, lastChange = now, time.Now()

			continue
		}

		if time.Since(lastChange) >= seedOverlapQuiet {
			return
		}
	}
}

// TestReliable4StatusSeedBoundary is the fast, deterministic regression guard
// for reliable4 FINDING 7. It forces the causal structure the browser hits by
// chance - join the delta feed, run transitions, only THEN ask for the seed -
// and asserts both halves of the fix: the server brackets the seed with markers
// that no delta interleaves, and the shipped client's displayed counts equal the
// truth on a connection that NEVER reconnects.
func TestReliable4StatusSeedBoundary(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to replay the stream through the real status page client")
	}

	const (
		backlog  = 60
		preStart = 5
		overlapN = 10
		leftover = 2
	)

	ctx := context.Background()

	Convey("The status-count seed is bracketed, and what predates it is not counted twice", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		added, _, err := jq.Add(subscriptionTestJobs(seedBoundaryRepGroup, standardReqs, backlog), envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, backlog)

		// jobs started before any status client exists: the control set, reported
		// only by the seed.
		before := startSeedOverlapJobs(t, jq, preStart)
		So(len(before), ShouldEqual, preStart)

		recorder := dialStatusWS(ctx, t, server, token)

		// prove the caster member is live before running the straddling
		// transitions: one started job must produce deltas with no "current"
		// request in sight.
		canary := startSeedOverlapJobs(t, jq, 1)
		So(len(canary), ShouldEqual, 1)
		So(recorder.waitForMessages(1), ShouldBeTrue)

		// the straddling set: started after the join, before the seed snapshot.
		overlap := startSeedOverlapJobs(t, jq, overlapN)
		So(len(overlap), ShouldEqual, overlapN)

		// let every one of their deltas be delivered, so this measures the seed
		// overlap itself rather than the delta feed's write lag.
		recorder.waitQuiet()
		preSeedDeltas := recorder.len()

		running := append(append(before, canary...), overlap...)
		So(len(running), ShouldEqual, preStart+1+overlapN)

		// now ask for the seed, exactly as the browser does in onopen.
		So(recorder.ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
		recorder.waitQuiet()

		// the mass exit: everything but `leftover` archives, mirroring the prod
		// limit->0 drop that made the pre-existing offset glaring.
		for _, job := range running[:len(running)-leftover] {
			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		}

		recorder.waitQuiet()

		msgs := recorder.snapshot()

		// the server's half of the fix: exactly one bracket, in order, with no
		// live delta inside it.
		begins, ends, inside := seedBoundaries(msgs)
		So(len(begins), ShouldEqual, 1)
		So(len(ends), ShouldEqual, 1)
		So(begins[0], ShouldBeLessThan, ends[0])
		So(inside, ShouldBeEmpty)

		// the overlap window really was exercised: the straddling transitions'
		// deltas all arrived BEFORE the bracket, and the seed inside it reports
		// those same transitions over again.
		So(preSeedDeltas, ShouldBeGreaterThan, 0)
		So(begins[0], ShouldEqual, preSeedDeltas)
		So(liveTransitionCount(msgs, seedBoundaryRepGroup, JobStateReady, JobStateRunning),
			ShouldBeGreaterThanOrEqualTo, overlapN+1)
		So(seedMessagesFor(msgs, seedBoundaryRepGroup, JobStateRunning), ShouldEqual, preStart+1+overlapN)

		// and nothing was dropped: every mass-exit transition reached this client
		// exactly once.
		So(liveTransitionCount(msgs, seedBoundaryRepGroup, JobStateRunning, JobStateComplete),
			ShouldEqual, preStart+1+overlapN-leftover)

		// the client's half: what the shipped websocket-handler.js would show,
		// with NO reconnect, against the truth from the same call the seed uses.
		truth := statusStateCounts(server.getJobsCurrent(ctx, seedBoundaryRepGroup,
			RepGroupMatchExact, 0, "", false, false, false))
		So(truth[JobStateRunning], ShouldEqual, leftover)
		So(truth[JobStateReady], ShouldEqual, backlog-preStart-1-overlapN)

		replay := replayThroughRealClient(t, t.TempDir(), recorder.rawSnapshot(), "", false)
		So(replay.begins, ShouldEqual, 1)
		So(replay.ends, ShouldEqual, 1)
		So(replay.interleaved, ShouldEqual, 0)

		printReproLine(fmt.Sprintf("\n  %s guard true_running=%d shown_running=%d true_ready=%d shown_ready=%d",
			seedOverlapMarker, truth[JobStateRunning], replay.shown[seedBoundaryRepGroup]["running"],
			truth[JobStateReady], replay.shown[seedBoundaryRepGroup]["ready"]))

		So(replay.shown[seedBoundaryRepGroup]["running"], ShouldEqual, truth[JobStateRunning])
		So(replay.shown[seedBoundaryRepGroup]["ready"], ShouldEqual, truth[JobStateReady])
		So(replay.shown[statusAllRepGroups]["running"], ShouldEqual, truth[JobStateRunning])
		So(replay.shown[statusAllRepGroups]["ready"], ShouldEqual, truth[JobStateReady])
	})
}

// startSeedOverlapJobs reserves and starts n jobs, returning them so they can be
// archived later.
func startSeedOverlapJobs(t *testing.T, jq *Client, n int) []*Job {
	t.Helper()

	started := make([]*Job, 0, n)

	for range n {
		job, err := jq.Reserve(5 * time.Second)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		started = append(started, job)
	}

	return started
}

// replayThroughRealClient writes the recorded stream to dir and replays it
// through the real websocket-handler.js, returning what the status bars would
// show. handler names the handler file to drive (empty means the shipped one)
// and ignoreBoundaries strips the seed boundary markers first, which is what a
// status page that predates them sees - so one recording can be replayed both
// ways to measure what the boundary bought.
func replayThroughRealClient(t *testing.T, dir string, msgs []json.RawMessage,
	handler string, ignoreBoundaries bool,
) replayResult {
	t.Helper()

	streamFile := filepath.Join(dir, "stream.json")
	if ignoreBoundaries {
		streamFile = filepath.Join(dir, "stream-blind.json")
	}

	encoded, err := json.Marshal(msgs)
	So(err, ShouldBeNil)
	So(os.WriteFile(streamFile, encoded, 0o600), ShouldBeNil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := []string{
		filepath.Join("jobqueue", "testdata", "status-count-reconcile", "replay-stream.mjs"),
		streamFile,
	}
	if handler != "" {
		args = append(args, handler)
	}

	if ignoreBoundaries {
		args = append(args, "--ignore-boundaries")
	}

	cmd := exec.CommandContext(ctx, "node", args...)
	cmd.Dir = repoRootForWebUITest(t)

	out, err := cmd.CombinedOutput()
	So(err, ShouldBeNil)

	return parseReplayOutput(string(out))
}

// parseReplayOutput turns replay-stream.mjs's two output lines into a
// replayResult.
func parseReplayOutput(out string) replayResult {
	result := replayResult{shown: make(map[string]map[string]int)}
	payload := ""

	for _, line := range strings.Split(out, "\n") {
		if after, ok := strings.CutPrefix(line, "RECONSTRUCTED "); ok {
			payload = after
		}

		if after, ok := strings.CutPrefix(line, "SEEDBRACKET "); ok {
			_, err := fmt.Sscanf(after, "begin=%d end=%d interleaved=%d",
				&result.begins, &result.ends, &result.interleaved)
			So(err, ShouldBeNil)
		}
	}

	So(payload, ShouldNotBeBlank)
	So(json.Unmarshal([]byte(payload), &result.shown), ShouldBeNil)

	return result
}

// printReproLine writes one measurement line into the GoConvey report. The
// print is the whole point of the line, so its byte count and (always nil)
// error are deliberately discarded.
func printReproLine(line string) {
	//nolint:errcheck // the print is the point; its byte count and nil error are noise
	Println(line)
}

// TestReliable4StatusSeedCounts pins the equivalence the cheap seed walk rests
// on: statusSeedCounts must return exactly what counting the materialised jobs
// returned, for the "+all+" aggregate and for every RepGroup, across a queue
// holding jobs in a spread of states. Only the cost is different - it does not
// clone every field of every job - and the cost is what bounds the residual the
// seed boundary cannot close.
func TestReliable4StatusSeedCounts(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("statusSeedCounts counts exactly what the materialising walk counted", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		// a released job's backoff is set when it is reserved, so this keeps the
		// delayed job below in `delay` for the whole test instead of letting it
		// return to `ready` between the two walks this test compares.
		serverConfig.Timings.ReleaseDelayMin = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// two rep groups, so the per-RepGroup breakdown is exercised.
		added, _, err := jq.Add(subscriptionTestJobs("rg-seed-counts-a", standardReqs, 12), envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 12)

		added, _, err = jq.Add(subscriptionTestJobs("rg-seed-counts-b", standardReqs, 5), envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 5)

		// two more states the bars show, as jobs that cannot leave them: one waiting
		// on a dep group nothing will ever satisfy, and one reserved and released
		// with a retry left, which backs off into `delay`. Both reach their state
		// through the same itemStateToJobState call the materialising walk uses, so
		// this is belt and braces rather than a separate risk - it just widens the
		// spread the map comparison below is taken over.
		added, _, err = jq.Add([]*Job{
			{
				Cmd:          "echo " + seedCountsDependentCmd,
				Cwd:          testCwd,
				ReqGroup:     seedCountsDependentCmd,
				Requirements: standardReqs,
				RepGroup:     "rg-seed-counts-a",
				Dependencies: Dependencies{{DepGroup: "rg-seed-counts-never-satisfied"}},
			},
			{
				Cmd:          "echo " + seedCountsDelayedCmd,
				Cwd:          testCwd,
				ReqGroup:     seedCountsDelayedCmd,
				Requirements: standardReqs,
				RepGroup:     "rg-seed-counts-a",
				Retries:      1,
				// the highest priority ready job is reserved first, so the release
				// below is certainly this job's and not an arbitrary one's. The Cmd
				// assertion holds it to that.
				Priority: 255,
			},
		}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 2)

		delayed, err := jq.Reserve(5 * time.Second)
		So(err, ShouldBeNil)
		So(delayed, ShouldNotBeNil)
		So(delayed.Cmd, ShouldEqual, "echo "+seedCountsDelayedCmd)
		So(jq.Started(delayed, os.Getpid()), ShouldBeNil)
		So(jq.Release(delayed, nil, "seed counts test"), ShouldBeNil)

		// spread the rest of the jobs over the other states the status bars show:
		// running (from started), reserved (started == false, which the display
		// merges into running), buried and still-ready.
		running := startSeedOverlapJobs(t, jq, 3)
		So(len(running), ShouldEqual, 3)

		reserved, err := jq.Reserve(5 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		buried := startSeedOverlapJobs(t, jq, 2)
		for _, job := range buried {
			So(jq.Bury(job, nil, "seed counts test"), ShouldBeNil)
		}

		// these jobs carry no Retries, so a release buries too.
		released := startSeedOverlapJobs(t, jq, 1)
		So(jq.Release(released[0], nil, "seed counts test"), ShouldBeNil)

		// the reference: what the seed used to be built from.
		jobs := server.getJobsCurrent(ctx, "", RepGroupMatchExact, 0, "", false, false, false)
		wantAll := statusStateCounts(jobs)

		rgJobs := make(map[string][]*Job)
		for _, job := range jobs {
			rgJobs[job.RepGroup] = append(rgJobs[job.RepGroup], job)
		}

		wantPerRepGroup := make(map[string]map[JobState]int)
		for repGroup, group := range rgJobs {
			wantPerRepGroup[repGroup] = statusStateCounts(group)
		}

		gotAll, gotPerRepGroup := server.statusSeedCounts()

		// the states really are spread, so the equivalence is not vacuous - and
		// reserved really is merged into running for display (3 started plus the
		// one reserved-but-not-started).
		So(len(wantAll), ShouldBeGreaterThanOrEqualTo, 3)
		So(wantAll[JobStateRunning], ShouldEqual, 4)
		So(wantAll[JobStateBuried], ShouldEqual, 3)
		So(wantAll[JobStateReady], ShouldBeGreaterThan, 0)
		So(wantAll[JobStateDependent], ShouldEqual, 1)
		So(wantAll[JobStateDelayed], ShouldEqual, 1)
		So(gotAll, ShouldResemble, wantAll)
		So(gotPerRepGroup, ShouldResemble, wantPerRepGroup)
		So(len(gotPerRepGroup), ShouldEqual, 2)
	})
}

// replayResult is what the real status page client makes of a recorded stream:
// the bucket counts each status bar would show, keyed by tracker ("+all+" and
// each rep group), plus the seed bracket the recording contained.
type replayResult struct {
	shown       map[string]map[string]int
	begins      int
	ends        int
	interleaved int
}
