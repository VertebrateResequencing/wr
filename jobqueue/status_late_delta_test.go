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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

// lostDeltaRepGroup is the rep group of TestStatusLostDeltaAfterSeed's job.
const lostDeltaRepGroup = "rg-lost-delta"

// lateDeltaCase is one queue move whose status-count delta reaches a status
// page only after that page's scan-on-connect seed has already counted it.
type lateDeltaCase struct {
	name     string
	from, to queue.SubQueue
	addFirst bool // add the target job before the held move
	move     func(jq *Client, target *Job)
	want     map[JobState]int
}

// TestStatusLateDeltaAfterSeed covers a move's status-count delta reaching a
// status page after the page's seed. The queue runs its change callback in a
// goroutine, so a move made before a status page asks for its seed can have its
// delta broadcast after the seed's "end" boundary. The seed already shows the
// job in its new state, so the page, which never reconnects, counts the move
// twice. Each case holds the target job's change callback until the recorded
// stream holds the seed's "end", then replays the stream through the shipped
// websocket-handler.js and compares what it shows with the queue.
func TestStatusLateDeltaAfterSeed(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to replay the stream through the real status page client")
	}

	cases := []lateDeltaCase{
		{
			name: "suspend", from: queue.SubQueueReady, to: queue.SubQueueSuspended, addFirst: true,
			move: func(jq *Client, target *Job) {
				changed, err := jq.Suspend([]*JobEssence{target.ToEssense()})
				So(err, ShouldBeNil)
				So(changed, ShouldEqual, 1)
			},
			want: map[JobState]int{JobStateReady: 1, JobStateSuspended: 1},
		},
		{
			name: "reserve", from: queue.SubQueueReady, to: queue.SubQueueRun, addFirst: true,
			move: func(jq *Client, target *Job) {
				job, err := jq.Reserve(5 * time.Second)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, target.Cmd)
			},
			want: map[JobState]int{JobStateReady: 1, JobStateRunning: 1},
		},
		{
			name: "add", from: queue.SubQueueNew, to: queue.SubQueueReady,
			move: func(jq *Client, target *Job) {
				added, _, err := jq.Add([]*Job{target}, envVars, true)
				So(err, ShouldBeNil)
				So(added, ShouldEqual, 1)
			},
			want: map[JobState]int{JobStateReady: 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testStatusLateDelta(t, tc)
		})
	}
}

func testStatusLateDelta(t *testing.T, tc lateDeltaCase) {
	t.Helper()

	ctx := context.Background()
	repGroup := "rg-late-delta-" + tc.name

	Convey("A "+tc.name+" delta broadcast after the seed is not counted twice", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		target := &Job{
			Cmd: "echo late delta target", Cwd: testCwd, ReqGroup: "late-delta",
			Requirements: standardReqs, RepGroup: repGroup, Priority: 255,
		}
		other := &Job{
			Cmd: "echo late delta other", Cwd: testCwd, ReqGroup: "late-delta",
			Requirements: standardReqs, RepGroup: repGroup,
		}

		held, release, emitted := holdChangeCallback(ctx, server, tc, target.Cmd)
		defer close(release)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		setup := []*Job{other}
		if tc.addFirst {
			setup = append(setup, target)
		}

		added, _, err := jq.Add(setup, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, len(setup))

		// the setup add's own delta is emitted before any status page exists.
		So(waitForCount(emitted, 1), ShouldBeTrue)

		tc.move(jq, target)
		So(waitForClose(held), ShouldBeTrue)

		recorder := dialStatusWS(ctx, t, server, token)
		So(recorder.ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
		So(waitForSeedEnds(recorder, 1), ShouldBeTrue)

		release <- struct{}{}

		So(waitForCount(emitted, 2), ShouldBeTrue)
		recorder.waitQuiet()

		all, perRepGroup, _ := server.statusSeedCounts()
		for state, count := range tc.want {
			So(all[state], ShouldEqual, count)
			So(perRepGroup[repGroup][state], ShouldEqual, count)
		}

		replay := replayThroughRealClient(t, t.TempDir(), recorder.rawSnapshot(), "", false)
		So(replay.begins, ShouldEqual, 1)
		So(replay.ends, ShouldEqual, 1)

		want := make(map[string]map[string]int)
		shown := make(map[string]map[string]int)

		for _, tracker := range []string{repGroup, statusAllRepGroups} {
			want[tracker] = make(map[string]int)
			shown[tracker] = make(map[string]int)

			for state, count := range tc.want {
				want[tracker][string(state)] = count
				shown[tracker][string(state)] = replay.shown[tracker][string(state)]
			}
		}

		So(shown, ShouldResemble, want)
	})
}

// waitForCount waits until counter reaches at least n.
func waitForCount(counter *atomic.Int32, n int32) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if counter.Load() >= n {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return false
}

// waitForClose waits until ch is closed.
func waitForClose(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

// waitForSeedEnds waits until the recorder has received n seeds' "end"
// boundaries.
func waitForSeedEnds(recorder *wsRecorder, n int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ends := 0

		for _, msg := range recorder.snapshot() {
			if msg.SeedBoundary == seedBoundaryEnd {
				ends++
			}
		}

		if ends >= n {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return false
}

// holdChangeCallback wraps the server's queue change callback so the callback
// for tc's move of the job with cmd blocks until a value is sent on release.
// held is closed once that callback is blocked; emitted counts every callback
// that has finished emitting its deltas. It must be called before any job is
// added: the queue reads its callback without a lock of its own, and the
// AllItems call publishes the swap through the queue mutex every move holds.
func holdChangeCallback(ctx context.Context, server *Server, tc lateDeltaCase,
	cmd string,
) (chan struct{}, chan struct{}, *atomic.Int32) {
	held := make(chan struct{})
	release := make(chan struct{}, 1)
	emitted := &atomic.Int32{}

	server.q.SetChangedCallback(func(fromQ, toQ queue.SubQueue, data []any, seq uint64) {
		if fromQ == tc.from && toQ == tc.to && changeHoldsCmd(data, cmd) {
			close(held)
			<-release
		}

		server.emitChangeCallbackTransition(ctx, fromQ, toQ, data, seq)
		emitted.Add(1)
	})
	server.q.AllItems()

	return held, release, emitted
}

// changeHoldsCmd reports whether a change callback's data holds the job with
// cmd.
func changeHoldsCmd(data []any, cmd string) bool {
	for _, inter := range data {
		job, ok := inter.(*Job)
		if !ok {
			continue
		}

		job.RLock()
		match := job.Cmd == cmd
		job.RUnlock()

		if match {
			return true
		}
	}

	return false
}

// TestStatusLostDeltaAfterSeed covers the status-count deltas that do not come
// from a queue move, so carry no change sequence: a running job going lost
// when its TTR expires, and a lost job coming back when it is touched. Once a
// status page's seed has recorded a non-zero sequence, the page must still be
// sent both, or it shows a lost job as running, or a recovered job as lost,
// until it is refreshed.
func TestStatusLostDeltaAfterSeed(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to replay the stream through the real status page client")
	}

	ctx := context.Background()

	Convey("A seeded status page shows a job going lost and coming back", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		applySubscriptionTimings(&serverConfig, time.Hour)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		added, _, err := jq.Add([]*Job{{
			Cmd: "echo lost delta", Cwd: testCwd, ReqGroup: "lost-delta",
			Requirements: standardReqs, RepGroup: lostDeltaRepGroup,
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		job, err := jq.Reserve(5 * time.Second)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		recorder := dialStatusWS(ctx, t, server, token)
		So(recorder.ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
		So(waitForSeedEnds(recorder, 1), ShouldBeTrue)

		_, _, seedSeq := server.statusSeedCounts()
		So(seedSeq, ShouldBeGreaterThan, 0)
		So(shownRunningLost(t, recorder), ShouldResemble, runningLost(1, 0))

		expireTTRNow(ctx, server, job.Key())
		So(waitForJobLost(server, job.Key(), 5*time.Second), ShouldBeTrue)
		recorder.waitQuiet()
		So(shownRunningLost(t, recorder), ShouldResemble, runningLost(0, 1))

		So(recorder.ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
		So(waitForSeedEnds(recorder, 2), ShouldBeTrue)
		So(shownRunningLost(t, recorder), ShouldResemble, runningLost(0, 1))

		holdTTR(ctx, server, job.Key(), time.Hour)

		kill, err := jq.Touch(job)
		So(err, ShouldBeNil)
		So(kill, ShouldBeFalse)

		_, lost, _, ok := serverJobState(server, job.Key())
		So(ok, ShouldBeTrue)
		So(lost, ShouldBeFalse)
		recorder.waitQuiet()
		So(shownRunningLost(t, recorder), ShouldResemble, runningLost(1, 0))
	})
}

// shownRunningLost replays everything the recorder has received through the
// shipped status page client and returns the running and lost counts it shows
// for lostDeltaRepGroup, asserting the "+all+" tracker agrees.
func shownRunningLost(t *testing.T, recorder *wsRecorder) map[string]int {
	t.Helper()

	replay := replayThroughRealClient(t, t.TempDir(), recorder.rawSnapshot(), "", false)

	shown := make(map[string]int)
	for _, state := range []JobState{JobStateRunning, JobStateLost} {
		shown[string(state)] = replay.shown[lostDeltaRepGroup][string(state)]
		So(replay.shown[statusAllRepGroups][string(state)], ShouldEqual, shown[string(state)])
	}

	return shown
}

// runningLost returns the running and lost counts a status page should show
// for both the rep group and "+all+", as shownRunningLost reports them.
func runningLost(running, lost int) map[string]int {
	return map[string]int{"running": running, "lost": lost}
}

// expireTTRNow makes the running item with key reach its TTR at once. A shorter
// TTR alone does not wake the queue's TTR processing, so it also touches the
// item.
func expireTTRNow(ctx context.Context, server *Server, key string) {
	holdTTR(ctx, server, key, time.Millisecond)
	So(server.q.Touch(key), ShouldBeNil)
}

// holdTTR gives the item with key a TTR of d, starting now, changing nothing
// else about it.
func holdTTR(ctx context.Context, server *Server, key string, d time.Duration) {
	item, err := server.q.Get(key)
	So(err, ShouldBeNil)

	stats := item.Stats()
	So(server.q.Update(ctx, key, item.ReserveGroup, item.Data(), stats.Priority, stats.Delay, d), ShouldBeNil)
}

// TestReadJStateDeltasUntilSeedBegin covers the delta reader the status tests
// share, for a move whose delta reaches a status page after it joins the delta
// feed but before its seed's "begin" boundary. The seed already counts that
// move, and the status page drops the delta when it resets on "begin", so the
// reader must too or it counts the move twice and never sees the seed's counts.
func TestReadJStateDeltasUntilSeedBegin(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A delta received before the seed's begin boundary is not counted", t, func() {
		const repGroup = "rg-helper-join"

		stream := []any{
			&jstateCount{RepGroup: repGroup, FromState: JobStateReady, ToState: JobStateSuspended, Count: 1},
			&jstateCount{RepGroup: statusAllRepGroups, FromState: JobStateReady, ToState: JobStateSuspended, Count: 1},
			&jstatusSeedBoundary{SeedBoundary: seedBoundaryBegin},
			&jstateCount{RepGroup: statusAllRepGroups, FromState: JobStateNew, ToState: JobStateReady, Count: 1},
			&jstateCount{RepGroup: statusAllRepGroups, FromState: JobStateNew, ToState: JobStateSuspended, Count: 1},
			&jstateCount{RepGroup: repGroup, FromState: JobStateNew, ToState: JobStateReady, Count: 1},
			&jstateCount{RepGroup: repGroup, FromState: JobStateNew, ToState: JobStateSuspended, Count: 1},
			&jstatusSeedBoundary{SeedBoundary: seedBoundaryEnd},
		}

		ws := dialScriptedStatusWS(t, stream)

		So(readJStateCounts(ws, []expectedJStateCount{
			{repGroup: repGroup, state: JobStateReady, count: 1},
			{repGroup: repGroup, state: JobStateSuspended, count: 1},
			{repGroup: statusAllRepGroups, state: JobStateReady, count: 1},
			{repGroup: statusAllRepGroups, state: JobStateSuspended, count: 1},
		}, 2*time.Second), ShouldBeTrue)
	})
}

// dialScriptedStatusWS connects to a websocket server that sends each of msgs
// in order, then holds the connection open until the test ends.
func dialScriptedStatusWS(t *testing.T, msgs []any) *websocket.Conn {
	t.Helper()

	done := make(chan struct{})
	upgrader := websocket.Upgrader{}

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		defer conn.Close()

		for _, msg := range msgs {
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}

		<-done
	}))

	ws, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(testServer.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial scripted status websocket: %s", err)
	}

	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}

	t.Cleanup(func() {
		ws.Close()
		close(done)
		testServer.Close()
	})

	return ws
}
