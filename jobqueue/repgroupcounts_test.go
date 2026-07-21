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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

// TestRepGroupCountsLiveAbsolute covers D1 acceptance test 1: a fresh counter
// tracks the live absolute per-RepGroup counts and the statusAllRepGroups
// ("+all+") live aggregate across new->ready->running->complete.
func TestRepGroupCountsLiveAbsolute(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A fresh repGroupCounts tracks live absolute counts and the +all+ aggregate", t, func() {
		c := newRepGroupCounts()
		rg := "rgc-live"

		So(c.wholeMap(), ShouldBeEmpty)

		c.applyTransitions([]countContribution{{to: JobStateNew, repGroup: rg, n: 1}})
		So(c.wholeMap()[rg], ShouldResemble, map[JobState]int{JobStateNew: 1})
		So(c.wholeMap()[statusAllRepGroups], ShouldResemble, map[JobState]int{JobStateNew: 1})

		c.applyTransitions([]countContribution{{from: JobStateNew, to: JobStateReady, repGroup: rg, n: 1}})
		So(c.wholeMap()[rg], ShouldResemble, map[JobState]int{JobStateReady: 1})
		So(c.wholeMap()[statusAllRepGroups], ShouldResemble, map[JobState]int{JobStateReady: 1})

		c.applyTransitions([]countContribution{{from: JobStateReady, to: JobStateRunning, repGroup: rg, n: 1}})
		So(c.wholeMap()[rg], ShouldResemble, map[JobState]int{JobStateRunning: 1})
		So(c.wholeMap()[statusAllRepGroups], ShouldResemble, map[JobState]int{JobStateRunning: 1})

		c.applyTransitions([]countContribution{{from: JobStateRunning, to: JobStateComplete, repGroup: rg, n: 1}})
		So(c.wholeMap()[rg], ShouldResemble, map[JobState]int{JobStateComplete: 1})
		// the +all+ aggregate excludes terminal states, so it drops back to empty.
		So(c.wholeMap()[statusAllRepGroups], ShouldBeEmpty)
	})
}

// TestRepGroupCountsWireFormat covers D1 acceptance test 2: a connected web-UI
// status client receives a jstateAbsolute JSON message that is byte-compatible
// with the pre-change wire format.
func TestRepGroupCountsWireFormat(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A connected status client receives a byte-compatible jstateAbsolute message", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "rgc-wire"
		jobs := subscriptionTestJobs(repGroup, standardReqs, 1)
		added, already, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		ws, testServer := connectStatusWS(ctx, server, token)
		defer testServer.Close()
		defer ws.Close()

		So(ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)

		expected, err := json.Marshal(&jstateAbsolute{
			RepGroup: repGroup,
			Counts:   map[JobState]int{JobStateReady: 1},
		})
		So(err, ShouldBeNil)

		So(readRawJStateAbsolute(ws, repGroup, string(expected), 5*time.Second), ShouldBeTrue)
	})
}

// TestRepGroupCountsSeedOmitsTerminal covers D1 acceptance test 3 (corrected by
// bugfix 260721-1, restoring 260626-2 / 260716-1): a RepGroup whose only jobs are
// terminal (complete) is OMITTED from a fresh subscriber's connect-seed, so a
// page refresh does not re-show completed-only work. The counter itself still
// tracks the terminal count (wholeMap includes it) for the live push path and
// the +all+ aggregate.
func TestRepGroupCountsSeedOmitsTerminal(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The connect-seed omits complete-only RepGroups (terminal-hiding filter), but the counter keeps them",
		t, func() {
			ctx := context.Background()
			serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
			serverConfig.Timings.ItemTTR = time.Hour

			server, _, token, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer server.Stop(ctx, true)

			jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			repGroup := "rgc-terminal-only"
			jobs := subscriptionTestJobs(repGroup, standardReqs, 1)
			added, _, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)

			job, err := jq.Reserve(2 * time.Second)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(jq.Started(job, os.Getpid()), ShouldBeNil)
			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			// the counter still tracks the terminal count (used by the +all+
			// aggregate and the live per-RepGroup push path).
			So(server.repGroupCounts.wholeMap()[repGroup], ShouldResemble, map[JobState]int{
				JobStateComplete: 1,
			})

			// but a freshly-connected client (a page refresh) must NOT be seeded
			// with the complete-only RepGroup.
			ws, testServer := connectStatusWS(ctx, server, token)
			defer testServer.Close()
			defer ws.Close()

			So(ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
			So(readRepGroupAbsentDuring(ws, repGroup, 3*time.Second), ShouldBeTrue)
		})
}

// TestRepGroupCountsSeedFilter is a behavioural unit test on the seed builder: a
// fully-complete (or deleted-only) RepGroup is absent from a fresh subscriber's
// connect seed (a page refresh), while a RepGroup with >=1 live job is present
// with its live + complete counts and no deleted state; and a RepGroup that
// completes WHILE connected stays visible via the live drain path (260625-6).
func TestRepGroupCountsSeedFilter(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A fresh subscriber's seed omits terminal-only RepGroups but keeps live ones", t, func() {
		c := newRepGroupCounts()

		live := "rgc-seed-live"
		completeOnly := "rgc-seed-complete"
		deletedOnly := "rgc-seed-deleted"

		// a live RepGroup with partial progress: one running + two already complete.
		c.applyTransitions([]countContribution{{to: JobStateRunning, repGroup: live, n: 1}})
		c.applyTransitions([]countContribution{{to: JobStateComplete, repGroup: live, n: 2}})
		// a complete-only RepGroup and a deleted-only RepGroup.
		c.applyTransitions([]countContribution{{to: JobStateComplete, repGroup: completeOnly, n: 3}})
		c.applyTransitions([]countContribution{{to: JobStateDeleted, repGroup: deletedOnly, n: 4}})

		sub := c.subscribe()
		seed := c.drain(sub)

		// the live RepGroup is seeded with its live + complete counts, no deleted.
		So(seed[live], ShouldResemble, map[JobState]int{JobStateRunning: 1, JobStateComplete: 2})

		// the complete-only and deleted-only RepGroups are omitted from the seed.
		_, ok := seed[completeOnly]
		So(ok, ShouldBeFalse)
		_, ok = seed[deletedOnly]
		So(ok, ShouldBeFalse)

		// the counter itself still tracks all states, including terminal-only groups.
		So(c.wholeMap()[completeOnly], ShouldResemble, map[JobState]int{JobStateComplete: 3})
		So(c.wholeMap()[deletedOnly], ShouldResemble, map[JobState]int{JobStateDeleted: 4})
		So(c.wholeMap()[live][JobStateComplete], ShouldEqual, 2)
	})

	Convey("A RepGroup that completes while connected stays visible via the live drain (260625-6)", t, func() {
		c := newRepGroupCounts()
		rg := "rgc-seed-inflight"

		c.applyTransitions([]countContribution{{to: JobStateRunning, repGroup: rg, n: 1}})

		sub := c.subscribe()
		So(c.drain(sub)[rg], ShouldResemble, map[JobState]int{JobStateRunning: 1})

		// the job completes WHILE connected; the live drain still delivers the
		// complete count so the RepGroup stays visible for this session.
		c.applyTransitions([]countContribution{{from: JobStateRunning, to: JobStateComplete, repGroup: rg, n: 1}})
		So(c.drain(sub)[rg], ShouldResemble, map[JobState]int{JobStateComplete: 1})
	})
}

// TestRepGroupCountsEmptyAfterRestart covers D1 acceptance test 4: a restarted
// manager on a DB with prior completed jobs has an empty counter until a live
// transition, proving no history scan seeds the counter on startup. The
// authoritative DB scan still reports the completed jobs (proving the DB was
// retained, not wiped).
func TestRepGroupCountsEmptyAfterRestart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A restarted manager's counter is empty (never seeded) despite completed history", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		repGroup := "rgc-restart"
		jobs := subscriptionTestJobs(repGroup, standardReqs, 1)
		added, _, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		job, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)
		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		summaries, err := jq.GetStatusByRepGroupMatch(repGroup, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries[repGroup].Counts[JobStateComplete], ShouldEqual, 1)

		disconnect(jq)
		server.Stop(ctx, true)

		// restart the manager on the same DB.
		serverConfig.dontWipeDevDB = true
		server, _, token, err = serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false

		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err = Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// the authoritative DB scan still finds the completed job (DB retained).
		summaries, err = jq.GetStatusByRepGroupMatch(repGroup, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries[repGroup].Counts[JobStateComplete], ShouldEqual, 1)

		// but the live web-UI counter was never seeded from that history, so a
		// client connecting before any new transition sees no message reviving it.
		ws, testServer := connectStatusWS(ctx, server, token)
		defer testServer.Close()
		defer ws.Close()

		So(ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)
		So(readRepGroupAbsentDuring(ws, repGroup, 3*time.Second), ShouldBeTrue)
	})
}

// connectStatusWS starts an httptest server exposing the jobqueue status
// websocket and returns a dialled client connection plus the test server (which
// the caller must Close).
func connectStatusWS(ctx context.Context, server *Server, token []byte) (*websocket.Conn, *httptest.Server) {
	testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))

	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
	header := http.Header{}
	header.Add("Authorization", "Bearer "+string(token))

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	So(err, ShouldBeNil)

	return ws, testServer
}

// readRawJStateAbsolute reads raw websocket messages until it sees one for the
// given RepGroup whose raw bytes exactly equal the expected JSON (proving
// byte-compatible wire format), or the timeout expires. Messages for other
// RepGroups (and the +all+ aggregate), and intermediate states for this
// RepGroup (e.g. an initial new before ready), are skipped.
func readRawJStateAbsolute(ws *websocket.Conn, repGroup, expected string, timeout time.Duration) bool {
	if err := ws.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}
	defer clearReadDeadlineBestEffort(ws)

	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return false
		}

		var msg jstateAbsolute
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}

		if msg.RepGroup != repGroup {
			continue
		}

		// gorilla's WriteJSON uses a json.Encoder, which appends a trailing
		// newline; json.Marshal (used to build expected) does not, so compare the
		// trimmed wire bytes.
		if strings.TrimSpace(string(raw)) == expected {
			return true
		}
	}
}
