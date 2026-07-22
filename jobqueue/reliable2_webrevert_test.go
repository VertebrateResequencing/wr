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

// This file pins spec.md sections A1 and A3 (reliable2 phase2): A1 removes the
// absolute per-RepGroup counter (repGroupCounts / jstateAbsolute) while keeping
// the #503 subscription delivery, and A3 restores the v0.36.5 status-bar
// jstateCount delta feed and incomplete-only scan-on-connect in its place.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2WebRevertCounterRemoved covers A1.1: compile-time proof the
// absolute counter is gone. The file repgroupcounts.go must not exist and the
// identifiers repGroupCounts / newRepGroupCounts / jstateAbsolute must not
// appear as symbols in any non-test source file of the package. A truly-removed
// symbol cannot be referenced from compiled code, so this is proven by scanning
// the package's non-test source for any surviving mention. Fail-before: the
// file and symbols exist; pass-after: neither does.
func TestReliable2WebRevertCounterRemoved(t *testing.T) {
	const removedCounterFile = "repgroupcounts.go"

	// identifiers of the absolute per-RepGroup counter A1 removes.
	removedCounterSymbols := []string{"repGroupCounts", "newRepGroupCounts", "jstateAbsolute"}

	Convey("The absolute per-RepGroup counter is entirely removed from the package source", t, func() {
		entries, err := os.ReadDir(".")
		So(err, ShouldBeNil)

		var offending []string

		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}

			So(name, ShouldNotEqual, removedCounterFile)

			data, errr := os.ReadFile(name)
			So(errr, ShouldBeNil)

			src := string(data)
			for _, sym := range removedCounterSymbols {
				if strings.Contains(src, sym) {
					offending = append(offending, name+":"+sym)
				}
			}
		}

		So(offending, ShouldBeEmpty)
	})
}

// TestReliable2WebRevertSubscriptionUnaffected covers A1.2: removing the counter
// leaves the KEEP'd #503 subscription delivery intact. A subscriber to a rep
// group whose only job runs to success (Exitcode==0) still receives a terminal
// aggregate whose job state is JobStateComplete and never JobStateDeleted.
func TestReliable2WebRevertSubscriptionUnaffected(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A rep group subscriber still receives a terminal complete update after the counter removal", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		applySubscriptionTimings(&serverConfig, subscriptionSafeItemTTR)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "rg-web-subscribe-complete"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToRepGroup(ctx, repGroup)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		rjob, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(rjob, ShouldNotBeNil)
		So(jq.Started(rjob, os.Getpid()), ShouldBeNil)
		So(jq.Archive(rjob, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateRepGroupDone)
		So(update.RepGroup, ShouldEqual, repGroup)
		So(update.Complete, ShouldEqual, 1)
		So(update.Total, ShouldEqual, 1)
		So(update.JobStates, ShouldResemble, []JobState{JobStateComplete})
		So(update.JobStates, ShouldNotContain, JobStateDeleted)
	})
}

// TestReliable2WebRevertNoDivergingCounter covers A3.2: after a DB-preserving
// restart with N prior completed jobs in a rep group and no absolute counter, a
// client requesting "current" is sent no complete seed for the terminal-only rep
// group (incomplete-only getJobsCurrent), while the CLI scan still reports the N
// complete jobs. This is the Issue-4 fix: no server-side counter to diverge.
func TestReliable2WebRevertNoDivergingCounter(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("After a DB-preserving restart the terminal-only rep group is absent "+
		"from the live feed while the CLI still counts it", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour
		serverConfig.dontWipeDevDB = true

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		const completeCount = 3

		repGroup := "rg-web-terminal"

		jobs := make([]*Job, 0, completeCount)
		for i := range completeCount {
			jobs = append(jobs, &Job{
				Cmd:          "echo terminal " + repGroup + strings.Repeat("x", i+1),
				Cwd:          testCwd,
				ReqGroup:     "web-terminal-group",
				Requirements: standardReqs,
				RepGroup:     repGroup,
			})
		}

		added, already, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, completeCount)
		So(already, ShouldEqual, 0)

		for range completeCount {
			rjob, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(rjob, ShouldNotBeNil)
			So(jq.Started(rjob, os.Getpid()), ShouldBeNil)
			So(jq.Archive(rjob, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		}

		summaries, err := jq.GetStatusByRepGroupMatch(repGroup, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries[repGroup].Counts[JobStateComplete], ShouldEqual, completeCount)

		disconnect(jq)
		server.Stop(ctx, true)

		// restart preserving the DB: the completed jobs persist, but there is no
		// absolute counter to seed the live feed.
		server2, _, token2, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server2.Stop(ctx, true)

		So(waitUntilRecovered(server2), ShouldBeTrue)

		jq2, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token2, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq2)

		testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server2))
		defer testServer.Close()

		wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
		header := http.Header{}
		header.Add("Authorization", "Bearer "+string(token2))

		wsc, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		So(err, ShouldBeNil)

		defer wsc.Close()

		So(wsc.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)

		// the fresh scan-on-connect is incomplete-only, so the terminal-only rep
		// group is never delivered: the window elapses with no reviving message.
		So(readRepGroupAbsentDuring(wsc, repGroup, 3*time.Second), ShouldBeTrue)

		// the CLI scan is ground truth and still reports the completed jobs.
		summaries2, err := jq2.GetStatusByRepGroupMatch(repGroup, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries2[repGroup].Counts[JobStateComplete], ShouldEqual, completeCount)
	})
}

// deltaCounts accumulates jstateCount deltas into per-RepGroup absolute state,
// mirroring what the browser's status bar does: FromState drops by Count,
// ToState rises by Count. Negative intermediate values are clamped to 0 so an
// out-of-order delta cannot drive a state below zero (v0.36.5 behaviour).
type deltaCounts struct {
	latest map[string]map[JobState]int
}

func newDeltaCounts() *deltaCounts {
	return &deltaCounts{latest: make(map[string]map[JobState]int)}
}

func (d *deltaCounts) apply(msg jstateCount) {
	if msg.RepGroup == "" {
		return
	}

	counts := d.latest[msg.RepGroup]
	if counts == nil {
		counts = make(map[JobState]int)
		d.latest[msg.RepGroup] = counts
	}

	if msg.FromState != "" && msg.FromState != JobStateNew {
		counts[msg.FromState] -= msg.Count
		if counts[msg.FromState] <= 0 {
			delete(counts, msg.FromState)
		}
	}

	if msg.ToState != "" && msg.Count != 0 {
		counts[msg.ToState] += msg.Count
		if counts[msg.ToState] == 0 {
			delete(counts, msg.ToState)
		}
	}
}

func (d *deltaCounts) count(repGroup string, state JobState) int {
	return d.latest[repGroup][state]
}

// TestReliable2WebRevertDeltaFeed covers A3.1: a connected /status_ws client
// watching a job go new->ready->running->complete in a rep group receives
// jstateCount delta messages whose applied deltas leave the rep group's live
// counts matching the run, and "+all+" tracks the live total.
func TestReliable2WebRevertDeltaFeed(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("The status websocket feeds v0.36.5-style jstateCount deltas", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
		defer testServer.Close()

		wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
		header := http.Header{}
		header.Add("Authorization", "Bearer "+string(token))

		ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		So(err, ShouldBeNil)

		defer ws.Close()

		So(ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}), ShouldBeNil)

		repGroup := "rg-web-delta"
		job := &Job{
			Cmd:          "echo web delta",
			Cwd:          testCwd,
			ReqGroup:     "web-delta-group",
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}

		added, already, err := jq.Add([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		// the job becomes ready: rg and +all+ show one ready job via deltas.
		So(readJStateDeltasUntil(ws, 5*time.Second, func(acc *deltaCounts) bool {
			return acc.count(repGroup, JobStateReady) == 1 && acc.count(webStatusAllRepGroups, JobStateReady) == 1
		}), ShouldBeTrue)

		rjob, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(rjob, ShouldNotBeNil)
		So(jq.Started(rjob, os.Getpid()), ShouldBeNil)

		// ready->running delta leaves one running job, none ready.
		So(readJStateDeltasUntil(ws, 5*time.Second, func(acc *deltaCounts) bool {
			return acc.count(repGroup, JobStateRunning) == 1 &&
				acc.count(repGroup, JobStateReady) == 0 &&
				acc.count(webStatusAllRepGroups, JobStateRunning) == 1
		}), ShouldBeTrue)

		So(jq.Archive(rjob, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		// running->complete delta leaves one complete job in the rep group and
		// nothing live, so +all+ drops back to zero running.
		So(readJStateDeltasUntil(ws, 5*time.Second, func(acc *deltaCounts) bool {
			return acc.count(repGroup, JobStateComplete) == 1 &&
				acc.count(repGroup, JobStateRunning) == 0 &&
				acc.count(webStatusAllRepGroups, JobStateRunning) == 0
		}), ShouldBeTrue)
	})
}

// readJStateDeltasUntil reads jstateCount delta messages from the status
// websocket, accumulating them, until the predicate holds against the running
// accumulated state or the timeout expires.
func readJStateDeltasUntil(ws *websocket.Conn, timeout time.Duration,
	until func(acc *deltaCounts) bool) bool {
	acc := newDeltaCounts()

	if err := ws.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}
	defer clearReadDeadlineBestEffort(ws)

	for {
		if until(acc) {
			return true
		}

		var msg jstateCount
		if err := ws.ReadJSON(&msg); err != nil {
			return false
		}

		acc.apply(msg)
	}
}
