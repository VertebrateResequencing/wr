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

// This file covers spec.md section C1 (note 2): the per-job subscription /
// broadcast to-state is derived from the job's own real State, so a job whose
// command succeeded is ALWAYS reported complete and never deleted, while a
// genuine user delete/remove of an INCOMPLETE job is still reported deleted.
// The status-details websocket subscription (stateChanges = true) is the
// subscriber that observes non-terminal state changes such as deleted, so it is
// used here to prove both the presence of deleted for a real delete and its
// absence for a succeeded (incl. parked-lost then owner-archived) job.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

func TestReliable2DeletedProjection(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A successfully archived job is reported complete and never deleted", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const rg = "reliable2-c1-complete"

		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd: "echo reliable2 c1 complete", Cwd: testCwd,
			ReqGroup: rg, Requirements: standardReqs, RepGroup: rg,
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		key := ids[0]

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		ws, cleanup := reliable2SubscribeDetails(ctx, server, token, rg, key, JobStateRunning)
		defer cleanup()

		So(jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		sawComplete, sawDeleted := reliable2CollectPushedStates(ws, key, 3*time.Second)
		So(sawComplete, ShouldBeTrue)
		So(sawDeleted, ShouldBeFalse)
	})

	Convey("A deleted incomplete job is reported deleted", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const rg = "reliable2-c1-deleted"

		job := &Job{
			Cmd: "echo reliable2 c1 deleted", Cwd: testCwd,
			ReqGroup: rg, Requirements: standardReqs, RepGroup: rg,
		}
		ids, err := jq.AddAndReturnIDs([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		key := ids[0]

		// the job is never reserved/run, so it is INCOMPLETE (ready); subscribe to
		// its details while ready, then delete it as a user would.
		ws, cleanup := reliable2SubscribeDetails(ctx, server, token, rg, key, JobStateReady)
		defer cleanup()

		removed, err := jq.Delete([]*JobEssence{job.ToEssense()})
		So(err, ShouldBeNil)
		So(removed, ShouldEqual, 1)

		deleted, ok := readJStatusMatching(ws, func(status JStatus) bool {
			return status.Key == key && status.IsPushUpdate && status.State == JobStateDeleted
		})
		So(ok, ShouldBeTrue)
		So(deleted.State, ShouldEqual, JobStateDeleted)
	})

	Convey("A parked-lost job whose owner archives success emits no deleted broadcast", t, func() {
		const ttr = 500 * time.Millisecond

		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = ttr

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const rg = "reliable2-c1-churn"

		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd: "echo reliable2 c1 churn", Cwd: testCwd,
			ReqGroup: rg, Requirements: standardReqs, RepGroup: rg, Retries: 3,
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		key := ids[0]

		// reserve+start with our own alive PID so the async dead-confirmation cannot
		// remove the job mid-test (the determinism trick from TestLostDetectionSilentRunner).
		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		// subscribe to the running job's details before the TTR parks it lost.
		ws, cleanup := reliable2SubscribeDetails(ctx, server, token, rg, key, JobStateRunning)
		defer cleanup()

		// never touched, so within a few TTRs the item is parked Lost in SubQueueRun.
		deadline := time.Now().Add(6 * ttr)
		lost := false

		for time.Now().Before(deadline) {
			item, errg := server.q.Get(key)
			if errg == nil && item != nil {
				So(item.Stats().State, ShouldEqual, queue.ItemStateRun)

				if j, ok := item.Data().(*Job); ok {
					j.RLock()
					lost = j.Lost
					j.RUnlock()
				}
			}

			if lost {
				break
			}

			time.Sleep(20 * time.Millisecond)
		}

		So(lost, ShouldBeTrue)

		// the still-owning runner archives success: it must be accepted and the job
		// must leave the queue as complete, never broadcast deleted.
		So(jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		sawComplete, sawDeleted := reliable2CollectPushedStates(ws, key, 3*time.Second)
		So(sawComplete, ShouldBeTrue)
		So(sawDeleted, ShouldBeFalse)
	})
}

// reliable2SubscribeDetails opens a status-details websocket subscription for
// the given rep group (as the web UI status details view does), reads the
// initial (non-push) status for key to confirm the subscription is active on
// that key, and returns the connection plus a cleanup func. The details
// subscription has stateChanges = true, so it observes non-terminal state
// changes (running, lost, deleted) as well as terminal ones.
func reliable2SubscribeDetails(
	ctx context.Context, server *Server, token []byte, repGroup, key string, state JobState,
) (*websocket.Conn, func()) {
	testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
	header := http.Header{}
	header.Add("Authorization", "Bearer "+string(token))

	ws, err := drainWebSocket(wsURL, header)
	So(err, ShouldBeNil)

	So(ws.WriteJSON(jstatusReq{
		Request:  jstatusRequestDetails,
		RepGroup: repGroup,
		State:    state,
	}), ShouldBeNil)

	_, ok := readJStatusMatching(ws, func(status JStatus) bool {
		return status.Key == key && !status.IsPushUpdate
	})
	So(ok, ShouldBeTrue)

	return ws, func() {
		_ = ws.Close()
		testServer.Close()
	}
}

// reliable2CollectPushedStates reads pushed JStatus updates for key until it
// observes a terminal complete state (sawComplete) or a deleted state
// (sawDeleted), or until timeout. A deleted broadcast for a succeeded job is the
// exact regression this guards against, so it returns as soon as either is seen.
func reliable2CollectPushedStates(
	ws *websocket.Conn, key string, timeout time.Duration,
) (sawComplete, sawDeleted bool) {
	if err := ws.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false, false
	}
	defer clearReadDeadlineBestEffort(ws)

	for {
		var status JStatus

		if err := ws.ReadJSON(&status); err != nil {
			return sawComplete, sawDeleted
		}

		if status.Key != key || !status.IsPushUpdate {
			continue
		}

		switch status.State {
		case JobStateComplete:
			return true, sawDeleted
		case JobStateDeleted:
			return sawComplete, true
		default:
		}
	}
}
