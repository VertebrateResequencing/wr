/*******************************************************************************
 * Copyright (c) 2025-2026 Genome Research Ltd.
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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

const webStatusAllRepGroups = statusAllRepGroups

func TestCaster(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A caster sends updates to active members", t, func() {
		caster := newCaster()

		receiver := caster.Join()
		defer receiver.Close()

		caster.Send("status")

		select {
		case msg := <-receiver.In:
			So(msg, ShouldEqual, "status")
		case <-time.After(time.Second):
			So("timed out waiting for caster update", ShouldBeBlank)
		}
	})

	Convey("Closing a caster member cancels a pending send", t, func() {
		caster := newCaster()
		receiver := caster.Join()

		if cap(receiver.In) > 0 {
			receiver.In <- "queued"
		}

		sent := make(chan struct{})

		go func() {
			caster.Send("closed")
			close(sent)
		}()

		select {
		case <-sent:
		case <-time.After(time.Second):
			So("timed out waiting for caster send", ShouldBeBlank)
		}

		receiver.Close()

		if cap(receiver.In) > 0 {
			select {
			case msg := <-receiver.In:
				So(msg, ShouldEqual, "queued")
			default:
				So("buffered caster update was missing", ShouldBeBlank)
			}
		}

		select {
		case msg := <-receiver.In:
			So(fmt.Sprintf("received update after close: %v", msg), ShouldBeBlank)
		case <-time.After(100 * time.Millisecond):
		}
	})
}

type expectedJStateCount struct {
	repGroup string
	state    JobState
	count    int
}

func TestServerWebISuspendedStatus(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("The status web interface shows suspended jobs", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "rg-web"
		ready := &Job{
			Cmd:          "echo web ready",
			Cwd:          testCwd,
			ReqGroup:     "web-group",
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}
		suspended := &Job{
			Cmd:          "echo web suspended",
			Cwd:          testCwd,
			ReqGroup:     "web-group",
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}

		inserts, already, err := jq.Add([]*Job{ready, suspended}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)
		So(already, ShouldEqual, 0)

		changed, err := jq.Suspend([]*JobEssence{suspended.ToEssense()})
		So(err, ShouldBeNil)
		So(changed, ShouldEqual, 1)

		Convey("The static page labels suspended state filters and details", func() {
			handler := webInterfaceStatic(ctx, server)

			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/status.html", nil)
			r.Header.Set("Authorization", "Bearer "+string(token))

			handler(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusOK)
			body := w.Body.String()
			So(body, ShouldContainSubstring, "selectedState() === 'suspended'")
			So(body, ShouldContainSubstring, "counts.suspended")
			So(body, ShouldContainSubstring, "showRepgroupSuspended")
			So(body, ShouldContainSubstring, "State == 'delayed' || State == 'dependent' || State == 'suspended'")
			So(body, ShouldContainSubstring, `<span class="prop-value">suspended</span>`)
			So(body, ShouldContainSubstring, "confirmResume")
			So(body, ShouldNotContainSubstring, "suspended - use wr resume to make it schedulable again")

			w = httptest.NewRecorder()
			r = httptest.NewRequestWithContext(ctx, http.MethodGet, "/js/wr/action-handlers.js", nil)

			handler(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusOK)
			So(w.Body.String(), ShouldContainSubstring, "Resume Suspended Commands")
		})

		Convey("The websocket returns suspended current counts, details, and resumes a single suspended job", func() {
			testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
			defer testServer.Close()

			wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
			header := http.Header{}
			header.Add("Authorization", "Bearer "+string(token))

			ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
			So(err, ShouldBeNil)

			defer ws.Close()

			err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
			So(err, ShouldBeNil)

			So(readJStateCounts(ws, []expectedJStateCount{
				{repGroup: webStatusAllRepGroups, state: JobStateSuspended, count: 1},
				{repGroup: repGroup, state: JobStateReady, count: 1},
				{repGroup: repGroup, state: JobStateSuspended, count: 1},
			}, 3*time.Second), ShouldBeTrue)

			err = ws.WriteJSON(jstatusReq{
				Request:  jstatusRequestDetails,
				RepGroup: repGroup,
				State:    JobStateSuspended,
			})
			So(err, ShouldBeNil)

			status, ok := readJStatusMatching(ws, func(s JStatus) bool {
				return s.RepGroup == repGroup && s.State == JobStateSuspended && s.Key == suspended.Key()
			})
			So(ok, ShouldBeTrue)
			So(status.Cmd, ShouldEqual, suspended.Cmd)

			err = ws.WriteJSON(jstatusReq{
				Request: jstatusRequestResume,
				Key:     suspended.Key(),
			})
			So(err, ShouldBeNil)
			So(waitForJobState(jq, repGroup, JobStateReady, 2), ShouldEqual, 2)
		})
	})
}

func readJStateCounts(ws *websocket.Conn, expected []expectedJStateCount, timeout time.Duration) bool {
	remaining := make(map[expectedJStateCount]struct{}, len(expected))
	for _, count := range expected {
		remaining[count] = struct{}{}
	}

	if err := ws.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}
	defer clearReadDeadlineBestEffort(ws)

	for len(remaining) > 0 {
		var count jstateCount
		if err := ws.ReadJSON(&count); err != nil {
			return false
		}

		delete(remaining, expectedJStateCount{
			repGroup: count.RepGroup,
			state:    count.ToState,
			count:    count.Count,
		})
	}

	return true
}

func TestStatusCurrentSnapshotsAreAuthoritative(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Status current websocket responses delimit authoritative snapshots", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "status-current-snapshot"
		jobs := []*Job{
			{
				Cmd:          "echo status current snapshot 1",
				Cwd:          testCwd,
				ReqGroup:     repGroup,
				Requirements: standardReqs,
				RepGroup:     repGroup,
			},
			{
				Cmd:          "echo status current snapshot 2",
				Cwd:          testCwd,
				ReqGroup:     repGroup,
				Requirements: standardReqs,
				RepGroup:     repGroup,
			},
		}
		added, already, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 2)
		So(already, ShouldEqual, 0)

		testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
		defer testServer.Close()

		wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
		header := http.Header{}
		header.Add("Authorization", "Bearer "+string(token))

		ws, err := drainWebSocket(wsURL, header)
		So(err, ShouldBeNil)

		defer ws.Close()

		err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
		So(err, ShouldBeNil)

		snapshot, ok := readJStateSnapshot(ws, 3*time.Second)
		So(ok, ShouldBeTrue)
		So(snapshot.id, ShouldNotEqual, 0)
		So(snapshot.counts[expectedJStateCount{
			repGroup: webStatusAllRepGroups,
			state:    JobStateReady,
			count:    2,
		}], ShouldBeTrue)
		So(snapshot.counts[expectedJStateCount{
			repGroup: repGroup,
			state:    JobStateReady,
			count:    2,
		}], ShouldBeTrue)

		err = ws.Close()
		So(err, ShouldBeNil)

		ws, err = drainWebSocket(wsURL, header)
		So(err, ShouldBeNil)

		defer ws.Close()

		err = ws.WriteJSON(jstatusReq{
			Request:  jstatusRequestRemove,
			RepGroup: repGroup,
		})
		So(err, ShouldBeNil)

		So(pollUntil(func() bool {
			remaining, errr := jq.GetByRepGroup(repGroup, false, 0, "", false, false)

			return errr == nil && len(remaining) == 0
		}), ShouldBeTrue)

		err = ws.Close()
		So(err, ShouldBeNil)

		ws, err = drainWebSocket(wsURL, header)
		So(err, ShouldBeNil)

		defer ws.Close()

		err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
		So(err, ShouldBeNil)

		emptySnapshot, ok := readJStateSnapshot(ws, 3*time.Second)
		So(ok, ShouldBeTrue)
		So(emptySnapshot.id, ShouldNotEqual, 0)
		So(emptySnapshot.id, ShouldNotEqual, snapshot.id)
		So(len(emptySnapshot.counts), ShouldEqual, 0)
	})
}

func readJStateSnapshot(ws *websocket.Conn, timeout time.Duration) (jstateSnapshot, bool) {
	snapshot := jstateSnapshot{
		counts: make(map[expectedJStateCount]bool),
	}

	if err := ws.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return snapshot, false
	}
	defer clearReadDeadlineBestEffort(ws)

	for {
		var count jstateCount
		if err := ws.ReadJSON(&count); err != nil {
			return snapshot, false
		}

		if count.SnapshotID == 0 {
			continue
		}

		if snapshot.id == 0 {
			snapshot.id = count.SnapshotID
		}

		if count.SnapshotID != snapshot.id {
			continue
		}

		if count.SnapshotDone {
			return snapshot, true
		}

		if count.Count == 0 || count.ToState == "" {
			continue
		}

		snapshot.counts[expectedJStateCount{
			repGroup: count.RepGroup,
			state:    count.ToState,
			count:    count.Count,
		}] = true
	}
}

type jstateSnapshot struct {
	id     uint64
	counts map[expectedJStateCount]bool
}

func TestServerWebI(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Once the jobqueue server is up", t, func() {
		serverConfig.Timings.ItemTTR = 100 * time.Second
		serverConfig.Timings.TouchInterval = 50 * time.Second
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		var jobs []*Job
		jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "group1",
			Requirements: standardReqs, RepGroup: "rg1"})
		jobs = append(jobs, &Job{Cmd: "echo 2", Cwd: "/tmp", ReqGroup: "group1",
			Requirements: standardReqs, RepGroup: "rg1"})
		jobs = append(jobs, &Job{Cmd: "echo 3", Cwd: "/tmp", ReqGroup: "group2",
			Requirements: standardReqs, RepGroup: "rg2"})
		jobs = append(jobs, &Job{Cmd: "echo 4 && false", Cwd: "/tmp", ReqGroup: "group2",
			Requirements: standardReqs, RepGroup: "rg2"})
		inserts, already, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 4)
		So(already, ShouldEqual, 0)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Cmd, ShouldEqual, "echo 1")

		job, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Cmd, ShouldEqual, "echo 2")
		err = jq.Execute(ctx, job, config.RunnerExecShell)
		So(err, ShouldBeNil)

		job, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Cmd, ShouldEqual, "echo 3")
		err = jq.Execute(ctx, job, config.RunnerExecShell)
		So(err, ShouldBeNil)

		job, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Cmd, ShouldEqual, "echo 4 && false")
		err = jq.Execute(ctx, job, config.RunnerExecShell)
		So(err, ShouldNotBeNil)
		So(job.State, ShouldEqual, JobStateBuried)

		Convey("The webInterfaceStatic handler works", func() {
			handler := webInterfaceStatic(ctx, server)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/status.html", nil)
			r.Header.Set("Authorization", "Bearer "+string(token))
			handler(w, r)
			resp := w.Result()
			So(resp.StatusCode, ShouldEqual, http.StatusOK)
			So(resp.Header.Get("Content-Type"), ShouldEqual, "text/html; charset=utf-8")

			body, err := io.ReadAll(resp.Body)
			So(err, ShouldBeNil)
			So(resp.Body.Close(), ShouldBeNil)
			So(string(body), ShouldContainSubstring, "Waiting for dep groups not yet seen")
			So(string(body), ShouldContainSubstring, "WaitingForDepGroups")
			So(string(body), ShouldContainSubstring, "foreach: WaitingForDepGroups")
			So(string(body), ShouldContainSubstring, "text: $data")
			So(string(body), ShouldNotContainSubstring, "html: WaitingForDepGroups")

			w = httptest.NewRecorder()
			r = httptest.NewRequest(http.MethodGet, "/nonexistent.html", nil)
			r.Header.Set("Authorization", "Bearer "+string(token))
			handler(w, r)
			resp = w.Result()
			So(resp.StatusCode, ShouldEqual, http.StatusNotFound)
			So(resp.Body.Close(), ShouldBeNil)

			fileTypes := map[string]string{
				"static/js/test.js":      "text/javascript; charset=utf-8",
				"static/css/test.css":    "text/css; charset=utf-8",
				"static/fonts/test.woff": "application/font-woff",
				"favicon.ico":            "image/x-icon",
			}

			for path, expectedContentType := range fileTypes {
				So(getContentTypeForPath(path), ShouldEqual, expectedContentType)
			}
		})

		Convey("The websocket handler connects and sends job status", func() {
			testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
			defer testServer.Close()

			wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
			header := http.Header{}
			header.Add("Authorization", "Bearer "+string(token))

			ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
			So(err, ShouldBeNil)

			defer ws.Close()

			executeReservedJobs := func(expectedCmds ...string) {
				expected := make(map[string]bool, len(expectedCmds))
				for _, cmd := range expectedCmds {
					expected[cmd] = true
				}

				for range expectedCmds {
					job, errr := jq.Reserve(50 * time.Millisecond)
					So(errr, ShouldBeNil)
					So(job, ShouldNotBeNil)

					if job == nil {
						return
					}

					So(expected, ShouldContainKey, job.Cmd)
					delete(expected, job.Cmd)

					errr = jq.Execute(ctx, job, config.RunnerExecShell)
					So(errr, ShouldBeNil)
				}

				So(len(expected), ShouldEqual, 0)
			}

			Convey("The websocket handler responds to current requests", func() {
				err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)

				receivedJobs := make(map[string]bool)
				receivedGroups := make(map[string]bool)
				receivedFromNews := 0
				receivedToBuried := 0
				receivedToComplete := 0
				receivedToRunning := 0

				for range 5 {
					var stateCount jstateCount
					err = ws.ReadJSON(&stateCount)
					So(err, ShouldBeNil)

					if stateCount.FromState == JobStateNew {
						receivedFromNews += stateCount.Count
					}

					switch stateCount.ToState { //nolint:exhaustive
					case JobStateBuried:
						receivedToBuried += stateCount.Count
					case JobStateComplete:
						receivedToComplete += stateCount.Count
					case JobStateRunning:
						receivedToRunning += stateCount.Count
					}

					if stateCount.RepGroup == webStatusAllRepGroups {
						receivedJobs[stateCount.RepGroup] = true
					} else {
						receivedGroups[stateCount.RepGroup] = true
					}
				}

				So(receivedJobs, ShouldContainKey, webStatusAllRepGroups)
				So(receivedGroups, ShouldContainKey, "rg1")
				So(receivedGroups, ShouldContainKey, "rg2")
				So(receivedFromNews, ShouldEqual, 5)
				So(receivedToBuried, ShouldBeGreaterThanOrEqualTo, 1)
				So(receivedToRunning, ShouldBeGreaterThanOrEqualTo, 1)
				So(receivedToComplete, ShouldBeGreaterThanOrEqualTo, 1)
			})

			Convey("The websocket handler responds to details requests", func() {
				err = ws.WriteJSON(jstatusReq{
					Request:  jstatusRequestDetails,
					RepGroup: "rg1",
					State:    JobStateComplete,
				})
				So(err, ShouldBeNil)

				status, ok := readJStatusMatching(ws, func(s JStatus) bool {
					return s.RepGroup == "rg1" && s.State == JobStateComplete
				})
				So(ok, ShouldBeTrue)
				So(status.RepGroup, ShouldEqual, "rg1")
				So(status.State, ShouldEqual, JobStateComplete)
				So(status.Cmd, ShouldEqual, "echo 2")

				go func() {
					<-time.After(100 * time.Millisecond)
					ws.WriteJSON(jstatusReq{ //nolint:errcheck
						Request:  jstatusRequestDetails,
						RepGroup: "rg1",
						State:    JobStateReserved,
					})
				}()

				status2, ok2 := readJStatusMatching(ws, func(s JStatus) bool {
					return s.RepGroup == "rg1" && s.State == JobStateReserved
				})
				So(ok2, ShouldBeTrue)
				So(status2.Cmd, ShouldEqual, "echo 1")
				So(status2.RepGroup, ShouldEqual, "rg1")
			})

			Convey("The websocket and REST details expose editable status fields", func() {
				statusJob := &Job{
					Cmd:                   "echo web status fields",
					Cwd:                   "/tmp",
					CwdMatters:            true,
					ChangeHome:            true,
					ReqGroup:              "web-req",
					Requirements:          standardReqs,
					RepGroup:              "web-status-fields",
					Override:              2,
					Priority:              11,
					Retries:               5,
					NoRetriesOverWalltime: 3 * time.Minute,
				}
				err = statusJob.EnvAddOverride([]string{"WEB_ONLY=old"})
				So(err, ShouldBeNil)

				inserts, already, erra := jq.Add([]*Job{statusJob}, envVars, true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				key := statusJob.Key()

				Convey("via websocket details", func() {
					err = ws.WriteJSON(jstatusReq{
						Request:  jstatusRequestDetails,
						RepGroup: "web-status-fields",
						State:    JobStateReady,
					})
					So(err, ShouldBeNil)

					status, ok := readJStatusMatching(ws, func(s JStatus) bool {
						return s.Key == key
					})
					So(ok, ShouldBeTrue)
					assertEditableStatusFields(status)
				})

				Convey("via REST GET by key", func() {
					handler := restJobs(ctx, server)
					w := httptest.NewRecorder()
					r := httptest.NewRequestWithContext(ctx, http.MethodGet, restJobsEndpoint+key, nil)
					r.Header.Set("Authorization", "Bearer "+string(token))

					handler(w, r)

					resp := w.Result()
					defer resp.Body.Close()

					So(resp.StatusCode, ShouldEqual, http.StatusOK)

					var statuses []JStatus

					err = json.NewDecoder(resp.Body).Decode(&statuses)
					So(err, ShouldBeNil)
					So(len(statuses), ShouldEqual, 1)
					assertEditableStatusFields(statuses[0])
				})
			})

			Convey("The websocket handler sends never-seen dependency group waits for details", func() {
				waiting := &Job{
					Cmd:          "echo web waiting dep",
					Cwd:          "/tmp",
					ReqGroup:     "web_group",
					Requirements: standardReqs,
					RepGroup:     "web-waiting",
					Dependencies: Dependencies{NewDepGroupDependency(futureDepGroup)},
				}

				inserts, already, erra := jq.Add([]*Job{waiting}, envVars, true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				ws, err = drainWebSocket(wsURL, header)
				So(err, ShouldBeNil)

				err = ws.WriteJSON(jstatusReq{
					Request:  jstatusRequestDetails,
					RepGroup: waiting.RepGroup,
					State:    JobStateDependent,
				})
				So(err, ShouldBeNil)

				status, ok := readJStatusMatching(ws, func(s JStatus) bool {
					return s.Key == waiting.Key()
				})
				So(ok, ShouldBeTrue)
				So(status.RepGroup, ShouldEqual, waiting.RepGroup)
				So(status.State, ShouldEqual, JobStateDependent)
				So(status.WaitingForDepGroups, ShouldResemble, []string{futureDepGroup})
			})

			Convey("The websocket handler deals with paginated details requests", func() {
				numPaginationJobs := 12
				limit := 5
				paginationJobs := make([]*Job, numPaginationJobs)

				for i := range numPaginationJobs {
					paginationJobs[i] = &Job{
						Cmd:          fmt.Sprintf("echo pagination_job_%d && false", i),
						Cwd:          "/tmp",
						ReqGroup:     "pg_group",
						Requirements: standardReqs,
						RepGroup:     "pg_repgroup",
					}
				}

				inserts, _, erra := jq.Add(paginationJobs, envVars, true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 12)

				for range numPaginationJobs {
					job, errr := jq.Reserve(50 * time.Millisecond)
					So(errr, ShouldBeNil)
					So(strings.HasPrefix(job.Cmd, "echo pagination_job_"), ShouldBeTrue)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exitcode, ShouldEqual, 1)
					So(job.FailReason, ShouldEqual, FailReasonExit)
				}

				buriedJobs, errg := jq.GetByRepGroup("pg_repgroup", false, 0, JobStateBuried, false, false)
				So(errg, ShouldBeNil)
				So(len(buriedJobs), ShouldEqual, numPaginationJobs)

				ws, err = drainWebSocket(wsURL, header)
				So(err, ShouldBeNil)

				testStatusesReceived := func(ws *websocket.Conn, expectedNum, offset, exitCode int) {
					// The status websocket also carries unsolicited count
					// broadcasts (which decode into a JStatus with an empty Key),
					// so for each expected job read until the next real job status
					// rather than asserting on whatever message arrives next.
					for i := range expectedNum {
						status, ok := readJStatusMatching(ws, func(s JStatus) bool { return s.Key != "" })
						So(ok, ShouldBeTrue)
						So(status.RepGroup, ShouldEqual, "pg_repgroup")
						So(status.State, ShouldEqual, JobStateBuried)
						So(status.Exitcode, ShouldEqual, exitCode)
						So(status.FailReason, ShouldEqual, FailReasonExit)
						So(status.Cmd, ShouldEqual, fmt.Sprintf("echo pagination_job_%d && false", i+offset))
					}

					So(testNoMoreMessages(ws), ShouldBeTrue)
				}

				Convey("It returns the first page of jobs", func() {
					err = ws.WriteJSON(jstatusReq{
						Request:    jstatusRequestDetails,
						RepGroup:   "pg_repgroup",
						State:      JobStateBuried,
						Exitcode:   1,
						FailReason: FailReasonExit,
						Limit:      limit,
						Offset:     0,
					})
					So(err, ShouldBeNil)

					testStatusesReceived(ws, limit, 0, 1)
				})

				Convey("It returns the second page of jobs", func() {
					err = ws.WriteJSON(jstatusReq{
						Request:    jstatusRequestDetails,
						RepGroup:   "pg_repgroup",
						State:      JobStateBuried,
						Exitcode:   1,
						FailReason: FailReasonExit,
						Limit:      limit,
						Offset:     limit,
					})
					So(err, ShouldBeNil)

					testStatusesReceived(ws, limit, limit, 1)
				})

				Convey("It returns a partial page when reaching the end", func() {
					err = ws.WriteJSON(jstatusReq{
						Request:    jstatusRequestDetails,
						RepGroup:   "pg_repgroup",
						State:      JobStateBuried,
						Exitcode:   1,
						FailReason: FailReasonExit,
						Limit:      limit,
						Offset:     limit * 2,
					})
					So(err, ShouldBeNil)

					testStatusesReceived(ws, 2, limit*2, 1)
				})

				Convey("It returns no jobs when offset is beyond available results", func() {
					err = ws.WriteJSON(jstatusReq{
						Request:    jstatusRequestDetails,
						RepGroup:   "pg_repgroup",
						State:      JobStateBuried,
						Exitcode:   1,
						FailReason: FailReasonExit,
						Limit:      limit,
						Offset:     limit * 4,
					})
					So(err, ShouldBeNil)

					testStatusesReceived(ws, 0, limit*4, 1)
				})

				Convey("It returns all jobs when limit is 0", func() {
					err = ws.WriteJSON(jstatusReq{
						Request:    jstatusRequestDetails,
						RepGroup:   "pg_repgroup",
						State:      JobStateBuried,
						Exitcode:   1,
						FailReason: FailReasonExit,
						Limit:      0,
						Offset:     0,
					})
					So(err, ShouldBeNil)

					testStatusesReceived(ws, numPaginationJobs, 0, 1)
				})

				Convey("It returns jobs from multiple repgroups with Search=true and limit=0", func() {
					searchJobs := []*Job{
						{Cmd: "echo search1", Cwd: "/tmp", ReqGroup: "search_group",
							Requirements: standardReqs, RepGroup: "search_rgA"},
						{Cmd: "echo search2", Cwd: "/tmp", ReqGroup: "search_group",
							Requirements: standardReqs, RepGroup: "seach_rgB"}, //nolint:misspell
						{Cmd: "echo search3", Cwd: "/tmp", ReqGroup: "search_group",
							Requirements: standardReqs, RepGroup: "search_rgC"},
					}
					inserts, _, erra := jq.Add(searchJobs, envVars, true)
					So(erra, ShouldBeNil)
					So(inserts, ShouldEqual, 3)

					ws, err = drainWebSocket(wsURL, header)
					So(err, ShouldBeNil)

					err = ws.WriteJSON(jstatusReq{
						Request:  jstatusRequestDetails,
						RepGroup: "search_rg",
						Search:   true,
					})
					So(err, ShouldBeNil)

					var statuses []JStatus

					timeout := time.After(500 * time.Millisecond)
					err = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
					So(err, ShouldBeNil)

					defer clearReadDeadlineBestEffort(ws)

				collectLoop:
					for {
						select {
						case <-timeout:
							break collectLoop
						default:
							var status JStatus

							errr := ws.ReadJSON(&status)
							if errr != nil {
								break collectLoop
							}

							if status.Key == "" {
								// skip interleaved count broadcasts; only the
								// search's job results have a Key.
								continue
							}

							statuses = append(statuses, status)
						}
					}

					So(len(statuses), ShouldEqual, 2)

					repGroups := map[string]bool{}
					cmds := map[string]bool{}

					for _, s := range statuses {
						repGroups[s.RepGroup] = true
						cmds[s.Cmd] = true

						So(s.State, ShouldEqual, JobStateReady)
					}

					So(repGroups, ShouldContainKey, "search_rgA")
					So(repGroups, ShouldContainKey, "search_rgC")
					So(repGroups, ShouldNotContainKey, "seach_rgB") //nolint:misspell
					So(cmds, ShouldContainKey, "echo search1")
					So(cmds, ShouldContainKey, "echo search3")
				})

				Convey("It handles negative offset gracefully", func() {
					err = ws.WriteJSON(jstatusReq{
						Request:    jstatusRequestDetails,
						RepGroup:   "pg_repgroup",
						State:      JobStateBuried,
						Exitcode:   1,
						FailReason: FailReasonExit,
						Limit:      limit,
						Offset:     -1,
					})
					So(err, ShouldBeNil)

					testStatusesReceived(ws, limit, 0, 1)
				})

				Convey("It filters correctly with multiple criteria", func() {
					var differentJob []*Job

					differentJob = append(differentJob, &Job{
						Cmd:          "echo different_exitcode && exit 2",
						Cwd:          "/tmp",
						ReqGroup:     "pg_group",
						Requirements: standardReqs,
						RepGroup:     "pg_repgroup",
					})

					inserts, _, erra := jq.Add(differentJob, envVars, true)
					So(erra, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, errr := jq.Reserve(50 * time.Millisecond)
					So(errr, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "echo different_exitcode && exit 2")

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exitcode, ShouldEqual, 2)

					ws, err = drainWebSocket(wsURL, header)
					So(err, ShouldBeNil)

					err = ws.WriteJSON(jstatusReq{
						Request:    jstatusRequestDetails,
						RepGroup:   "pg_repgroup",
						State:      JobStateBuried,
						Exitcode:   2,
						FailReason: FailReasonExit,
						Limit:      5,
						Offset:     0,
					})
					So(err, ShouldBeNil)

					// skip any interleaved count broadcast (empty Key) and read
					// the real job status the request asked for.
					status, ok := readJStatusMatching(ws, func(s JStatus) bool { return s.Key != "" })
					So(ok, ShouldBeTrue)
					So(status.RepGroup, ShouldEqual, "pg_repgroup")
					So(status.State, ShouldEqual, JobStateBuried)
					So(status.Exitcode, ShouldEqual, 2)
					So(status.Cmd, ShouldEqual, "echo different_exitcode && exit 2")

					So(testNoMoreMessages(ws), ShouldBeTrue)
				})
			})

			Convey("The websocket handler responds to key requests", func() {
				var jobKey string

				completeJobs, errg := jq.GetByRepGroup("rg1", false, 0, JobStateComplete, false, false)
				So(errg, ShouldBeNil)
				So(len(completeJobs), ShouldEqual, 1)
				jobKey = completeJobs[0].Key()

				err = ws.WriteJSON(jstatusReq{Key: jobKey})
				So(err, ShouldBeNil)

				status, ok := readJStatusMatching(ws, func(s JStatus) bool { return s.Key == jobKey })
				So(ok, ShouldBeTrue)
				So(status.Key, ShouldEqual, jobKey)
				So(status.State, ShouldEqual, JobStateComplete)
			})

			Convey("The websocket handler can retry buried jobs", func() {
				buriedJobs, errg := jq.GetByRepGroup("rg2", false, 0, JobStateBuried, false, false)
				So(errg, ShouldBeNil)
				So(len(buriedJobs), ShouldEqual, 1)
				So(buriedJobs[0].Cmd, ShouldEqual, "echo 4 && false")

				err = ws.WriteJSON(jstatusReq{
					Request:    "retry",
					RepGroup:   "rg2",
					Exitcode:   buriedJobs[0].Exitcode,
					FailReason: buriedJobs[0].FailReason,
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					kicked, errr := jq.GetByRepGroup("rg2", false, 0, JobStateReady, false, false)

					return errr == nil && len(kicked) == 1
				}), ShouldBeTrue)

				kickedJobs, errg := jq.GetByRepGroup("rg2", false, 0, JobStateReady, false, false)
				So(errg, ShouldBeNil)
				So(len(kickedJobs), ShouldEqual, 1)
				So(kickedJobs[0].Cmd, ShouldEqual, "echo 4 && false")
			})

			Convey("The websocket handler can rerun completed jobs", func() {
				completeJobs, errg := jq.GetByRepGroup("rg1", false, 0, JobStateComplete, false, false)
				So(errg, ShouldBeNil)
				So(len(completeJobs), ShouldEqual, 1)
				So(completeJobs[0].Cmd, ShouldEqual, "echo 2")
				So(completeJobs[0].Exited, ShouldBeTrue)
				So(completeJobs[0].Attempts, ShouldEqual, 1)

				err = ws.WriteJSON(jstatusReq{
					Request:  jstatusRequestRerun,
					Key:      completeJobs[0].Key(),
					RepGroup: completeJobs[0].RepGroup,
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					rerunJobs, errr := jq.GetByRepGroup("rg1", false, 0, JobStateReady, false, false)
					if errr != nil || len(rerunJobs) != 1 {
						return false
					}

					return rerunJobs[0].Cmd == "echo 2"
				}), ShouldBeTrue)

				rerunJobs, errg := jq.GetByRepGroup("rg1", false, 0, JobStateReady, false, false)
				So(errg, ShouldBeNil)
				So(len(rerunJobs), ShouldEqual, 1)
				So(rerunJobs[0].Key(), ShouldEqual, completeJobs[0].Key())
				So(rerunJobs[0].Exited, ShouldBeFalse)
				So(rerunJobs[0].Attempts, ShouldEqual, 0)
				So(rerunJobs[0].StartTime.IsZero(), ShouldBeTrue)
				So(rerunJobs[0].EndTime.IsZero(), ShouldBeTrue)
				So(rerunJobs[0].PeakRAM, ShouldEqual, 0)
				So(rerunJobs[0].PeakDisk, ShouldEqual, 0)
				So(rerunJobs[0].FailReason, ShouldBeBlank)
			})

			Convey("The websocket handler can rerun completed jobs by key in the requested RepGroup", func() {
				firstGroup := "rerun_key_rg1"
				secondGroup := "rerun_key_rg2"
				cmd := "echo webi rerun duplicate key"

				inserts, already, erra := jq.Add([]*Job{{
					Cmd:          cmd,
					Cwd:          "/tmp",
					ReqGroup:     "rerun_key_group",
					Requirements: standardReqs,
					RepGroup:     firstGroup,
				}}, envVars, true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				executeReservedJobs(cmd)

				inserts, already, erra = jq.Add([]*Job{{
					Cmd:          cmd,
					Cwd:          "/tmp",
					ReqGroup:     "rerun_key_group",
					Requirements: standardReqs,
					RepGroup:     secondGroup,
				}}, envVars, false)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				executeReservedJobs(cmd)

				firstGroupJobs, errg := jq.GetByRepGroup(firstGroup, false, 0, JobStateComplete, false, false)
				So(errg, ShouldBeNil)
				So(len(firstGroupJobs), ShouldEqual, 1)
				So(firstGroupJobs[0].RepGroup, ShouldEqual, firstGroup)

				err = ws.WriteJSON(jstatusReq{
					Request:  jstatusRequestRerun,
					Key:      firstGroupJobs[0].Key(),
					RepGroup: firstGroupJobs[0].RepGroup,
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					rerunJobs, errr := jq.GetByRepGroup(firstGroup, false, 0, JobStateReady, false, false)
					if errr != nil || len(rerunJobs) != 1 {
						return false
					}

					return rerunJobs[0].Cmd == cmd
				}), ShouldBeTrue)

				firstGroupReady, errg := jq.GetByRepGroup(firstGroup, false, 0, JobStateReady, false, false)
				So(errg, ShouldBeNil)
				So(len(firstGroupReady), ShouldEqual, 1)
				So(firstGroupReady[0].RepGroup, ShouldEqual, firstGroup)

				secondGroupReady, errg := jq.GetByRepGroup(secondGroup, false, 0, JobStateReady, false, false)
				So(errg, ShouldBeNil)
				So(len(secondGroupReady), ShouldEqual, 0)
			})

			Convey("The websocket handler can rerun all matching completed jobs", func() {
				repGroup := "rerun_all_rg"
				otherRepGroup := "rerun_all_other_rg"
				reqGroup := "rerun_all_group"
				jobsToRerun := []*Job{
					{Cmd: "echo webi rerun all 1", Cwd: "/tmp", ReqGroup: reqGroup,
						Requirements: standardReqs, RepGroup: repGroup},
					{Cmd: "echo webi rerun all 2", Cwd: "/tmp", ReqGroup: reqGroup,
						Requirements: standardReqs, RepGroup: repGroup},
					{Cmd: "echo webi rerun all other", Cwd: "/tmp", ReqGroup: reqGroup,
						Requirements: standardReqs, RepGroup: otherRepGroup},
				}

				inserts, already, erra := jq.Add(jobsToRerun, envVars, true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 3)
				So(already, ShouldEqual, 0)

				executeReservedJobs(
					"echo webi rerun all 1",
					"echo webi rerun all 2",
					"echo webi rerun all other",
				)

				completeJobs, errg := jq.GetByRepGroup(repGroup, false, 0, JobStateComplete, false, false)
				So(errg, ShouldBeNil)
				So(len(completeJobs), ShouldEqual, 2)
				So(completeJobs[0].Exited, ShouldBeTrue)
				So(completeJobs[0].Attempts, ShouldEqual, 1)

				err = ws.WriteJSON(jstatusReq{
					Request:    jstatusRequestRerun,
					RepGroup:   repGroup,
					State:      JobStateComplete,
					Exitcode:   completeJobs[0].Exitcode,
					FailReason: completeJobs[0].FailReason,
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					rerunJobs, errr := jq.GetByRepGroup(repGroup, false, 0, JobStateReady, false, false)

					return errr == nil && len(rerunJobs) == 2
				}), ShouldBeTrue)

				rerunJobs, errg := jq.GetByRepGroup(repGroup, false, 0, JobStateReady, false, false)
				So(errg, ShouldBeNil)
				So(len(rerunJobs), ShouldEqual, 2)

				rerunKeys := make(map[string]bool, len(rerunJobs))
				for _, job := range rerunJobs {
					rerunKeys[job.Key()] = true
					So(job.RepGroup, ShouldEqual, repGroup)
					So(job.Exited, ShouldBeFalse)
					So(job.Attempts, ShouldEqual, 0)
					So(job.StartTime.IsZero(), ShouldBeTrue)
					So(job.EndTime.IsZero(), ShouldBeTrue)
					So(job.PeakRAM, ShouldEqual, 0)
					So(job.PeakDisk, ShouldEqual, 0)
					So(job.FailReason, ShouldBeBlank)
				}

				for _, job := range completeJobs {
					So(rerunKeys, ShouldContainKey, job.Key())
				}

				otherReady, errg := jq.GetByRepGroup(otherRepGroup, false, 0, JobStateReady, false, false)
				So(errg, ShouldBeNil)
				So(len(otherReady), ShouldEqual, 0)
			})

			Convey("The websocket handler can remove jobs", func() {
				removeJobs := []*Job{{Cmd: "echo remove", Cwd: "/tmp",
					ReqGroup: "group3", Requirements: standardReqs, RepGroup: "rg3"}}
				inserts, _, erra := jq.Add(removeJobs, envVars, true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 1)

				jobs, errg := jq.GetByRepGroup("rg3", false, 0, "", false, false)
				So(errg, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)

				err = ws.WriteJSON(jstatusReq{
					Request:  jstatusRequestRemove,
					RepGroup: "rg3",
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					remaining, errr := jq.GetByRepGroup("rg3", false, 0, "", false, false)

					return errr == nil && len(remaining) == 0
				}), ShouldBeTrue)

				jobs, err = jq.GetByRepGroup("rg3", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 0)
			})

			Convey("The websocket handler supports multiple concurrent clients", func() {
				ws2, _, errw := websocket.DefaultDialer.Dial(wsURL, header)
				So(errw, ShouldBeNil)

				defer ws2.Close()

				ws3, _, errw := websocket.DefaultDialer.Dial(wsURL, header)
				So(errw, ShouldBeNil)

				defer ws3.Close()

				var broadcastJobs []*Job
				broadcastJobs = append(broadcastJobs, &Job{Cmd: "echo broadcast", Cwd: "/tmp",
					ReqGroup: "group4", Requirements: standardReqs, RepGroup: "rg4"})
				inserts, _, erra := jq.Add(broadcastJobs, envVars, true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 1)

				err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)
				err = ws2.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)
				err = ws3.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)

				var wg sync.WaitGroup

				wg.Add(3)

				r1ch := make(chan jstateCount, 1)
				r2ch := make(chan jstateCount, 1)
				r3ch := make(chan jstateCount, 1)

				go func() {
					defer wg.Done()

					var sc jstateCount

					ws.ReadJSON(&sc) //nolint:errcheck
					r1ch <- sc
				}()

				go func() {
					defer wg.Done()

					var sc jstateCount

					ws2.ReadJSON(&sc) //nolint:errcheck
					r2ch <- sc
				}()

				go func() {
					defer wg.Done()

					var sc jstateCount

					ws3.ReadJSON(&sc) //nolint:errcheck
					r3ch <- sc
				}()

				wg.Wait()

				sc1 := <-r1ch
				So(sc1, ShouldNotBeNil)
				So(sc1.RepGroup, ShouldNotBeBlank)

				sc2 := <-r2ch
				So(sc2, ShouldNotBeNil)
				So(sc2.RepGroup, ShouldNotBeBlank)

				sc3 := <-r3ch
				So(sc3, ShouldNotBeNil)
				So(sc3.RepGroup, ShouldNotBeBlank)

				job, errr := jq.Reserve(50 * time.Millisecond)
				So(errr, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "echo broadcast")

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)

				ws2.Close()

				err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)

				var sc jstateCount

				// Read the count response with a deadline instead of pre-sleeping
				// a fixed time: the explicit "current" request above guarantees a
				// jstateCount is sent, and the deadline tolerates a slow response
				// under heavy parallel-test load.
				So(ws.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)
				defer clearReadDeadlineBestEffort(ws)

				err = ws.ReadJSON(&sc)
				So(err, ShouldBeNil)

				err = ws3.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)

				So(ws3.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)
				defer clearReadDeadlineBestEffort(ws3)

				err = ws3.ReadJSON(&sc)
				So(err, ShouldBeNil)
			})

			Convey("The websocket handler correctly processes scheduler messages", func() {
				testMsg := "Test scheduler issue"

				si := &schedulerIssue{
					Msg:       testMsg,
					FirstDate: time.Now().Unix(),
					LastDate:  time.Now().Unix(),
					Count:     1,
				}

				server.simutex.Lock()
				server.schedIssues[testMsg] = si
				server.simutex.Unlock()

				server.schedCaster.Send(si)

				err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)

				foundMessage := false

				// The scheduler-issue broadcast is lossy (the caster drops to any subscriber
				// whose buffer is full), so under heavy parallel-test load the "current" we
				// requested above can be dropped before this ws reads it. Re-request
				// "current" periodically (it re-broadcasts the issues) while reading until we
				// see our message, bounded by a read deadline; skip other message shapes.
				resendStop := make(chan struct{})
				resendDone := make(chan struct{})

				go func() {
					defer close(resendDone)

					ticker := time.NewTicker(time.Second)
					defer ticker.Stop()

					for {
						select {
						case <-resendStop:
							return
						case <-ticker.C:
							if werr := writeCurrentJStatusWithDeadline(ws); werr != nil {
								return
							}
						}
					}
				}()

				So(ws.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)
				defer clearReadDeadlineBestEffort(ws)

				for {
					_, data, errr := ws.ReadMessage()
					if errr != nil {
						break // read deadline exceeded or connection closed
					}

					var msg schedulerIssue
					if json.Unmarshal(data, &msg) != nil {
						continue // a different message shape on the stream; skip it
					}

					if msg.Msg == testMsg {
						foundMessage = true

						So(msg.Count, ShouldEqual, 1)
						So(msg.FirstDate, ShouldBeLessThanOrEqualTo, time.Now().Unix())
						So(msg.LastDate, ShouldEqual, msg.FirstDate)

						break
					}
				}

				close(resendStop)
				<-resendDone

				clearReadDeadlineBestEffort(ws)
				So(foundMessage, ShouldBeTrue)

				err = ws.WriteJSON(jstatusReq{
					Request: "dismissMsg",
					Msg:     testMsg,
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					server.simutex.RLock()
					_, exists := server.schedIssues[testMsg]
					server.simutex.RUnlock()

					return !exists
				}), ShouldBeTrue)

				anotherMsg := "Another test issue"
				anotherSi := &schedulerIssue{
					Msg:       anotherMsg,
					FirstDate: time.Now().Unix(),
					LastDate:  time.Now().Unix(),
					Count:     1,
				}

				server.simutex.Lock()
				server.schedIssues[anotherMsg] = anotherSi
				server.simutex.Unlock()

				err = ws.WriteJSON(jstatusReq{
					Request: "dismissMsgs",
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					server.simutex.RLock()
					count := len(server.schedIssues)
					server.simutex.RUnlock()

					return count == 0
				}), ShouldBeTrue)
			})

			Convey("The websocket handler handles bad server notifications", func() {
				testServer := &cloud.Server{
					ID:   "test-server-id",
					Name: "test-server",
					IP:   "192.168.1.1",
				}
				testServer.GoneBad("Test server problem")

				// Manually call the bad server callback
				server.bsmutex.Lock()
				server.badServers[testServer.ID] = testServer
				server.bsmutex.Unlock()

				server.badServerCaster.Send(cloudServerToBadServer(testServer))

				err = ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)

				foundBadServer := false

				// The bad-server broadcast is lossy (the caster drops to any
				// subscriber whose buffer is full), so under heavy parallel-test
				// load the "current" we requested above can be dropped before this
				// ws reads it. Re-request "current" periodically (it re-broadcasts
				// the bad servers) while reading until we see our server, bounded
				// by a read deadline; skip other message shapes.
				resendStop := make(chan struct{})
				resendDone := make(chan struct{})

				go func() {
					defer close(resendDone)

					ticker := time.NewTicker(time.Second)
					defer ticker.Stop()

					for {
						select {
						case <-resendStop:
							return
						case <-ticker.C:
							if werr := writeCurrentJStatusWithDeadline(ws); werr != nil {
								return
							}
						}
					}
				}()

				So(ws.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)
				defer clearReadDeadlineBestEffort(ws)

				for {
					var msg BadServer

					errr := ws.ReadJSON(&msg)
					if errr != nil {
						break // read deadline exceeded or connection closed
					}

					if msg.ID == testServer.ID {
						foundBadServer = true

						So(msg.Name, ShouldEqual, "test-server")
						So(msg.IP, ShouldEqual, "192.168.1.1")
						So(msg.IsBad, ShouldBeTrue)
						So(msg.Problem, ShouldEqual, "Test server problem")

						break
					}
				}

				close(resendStop)
				<-resendDone

				clearReadDeadlineBestEffort(ws)

				So(foundBadServer, ShouldBeTrue)

				err = ws.WriteJSON(jstatusReq{
					Request:  "confirmBadServer",
					ServerID: testServer.ID,
				})
				So(err, ShouldBeNil)

				So(pollUntil(func() bool {
					server.bsmutex.RLock()
					_, stillBad := server.badServers[testServer.ID]
					server.bsmutex.RUnlock()

					return !stillBad
				}), ShouldBeTrue)

				server.bsmutex.RLock()
				_, exists := server.badServers[testServer.ID]
				server.bsmutex.RUnlock()
				So(exists, ShouldBeFalse)
			})
		})

		Reset(func() {
			server.Stop(ctx, true)
		})
	})
}

func assertEditableStatusFields(status JStatus) {
	So(status.ReqGroup, ShouldEqual, "web-req")
	So(status.Override, ShouldEqual, 2)
	So(status.Priority, ShouldEqual, 11)
	So(status.Retries, ShouldEqual, 5)
	So(status.NoRetryOverWalltime, ShouldEqual, 180)
	So(status.CwdMatters, ShouldBeTrue)
	So(status.HomeChanged, ShouldBeTrue)
	So(status.EnvOverrides, ShouldResemble, []string{"WEB_ONLY=old"})
}

func TestStatusDetailsLiveCompatibility(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Status details preserve compatibility without active live data", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
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

		ws, err := drainWebSocket(wsURL, header)
		So(err, ShouldBeNil)

		defer ws.Close()

		addRunningJob := func(repGroup, cmd string) *Job {
			ids, erra := jq.AddAndReturnIDs([]*Job{{
				Cmd:          cmd,
				Cwd:          testCwd,
				ReqGroup:     repGroup,
				Requirements: standardReqs,
				RepGroup:     repGroup,
			}}, envVars, true)
			So(erra, ShouldBeNil)
			So(ids, ShouldHaveLength, 1)

			job, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)

			if job == nil {
				return &Job{}
			}

			So(job.Key(), ShouldEqual, ids[0])
			So(jq.Started(job, os.Getpid()), ShouldBeNil)

			return job
		}

		requestDetails := func(repGroup string, state JobState, key string) JStatus {
			errw := ws.WriteJSON(jstatusReq{
				Request:  jstatusRequestDetails,
				RepGroup: repGroup,
				State:    state,
			})
			So(errw, ShouldBeNil)

			status, ok := readJStatusMatching(ws, func(s JStatus) bool { return s.Key == key })
			So(ok, ShouldBeTrue)

			return status
		}

		noLiveJob := addRunningJob("status-details-no-live", "echo status details no live")
		noLiveStatus := requestDetails(noLiveJob.RepGroup, JobStateRunning, noLiveJob.Key())

		So(noLiveStatus.PeakRAM, ShouldEqual, 0)
		So(noLiveStatus.CPUtime, ShouldEqual, 0)
		So(noLiveStatus.StdOut, ShouldEqual, "")
		So(noLiveStatus.StdErr, ShouldEqual, "")
		So(noLiveStatus.SSHCommand, ShouldEqual, "")
		So(noLiveStatus.Started, ShouldNotBeNil)
		So(noLiveStatus.Ended, ShouldBeNil)
		So(noLiveStatus.Walltime, ShouldBeGreaterThanOrEqualTo, 0)

		completeJob := addRunningJob("status-details-live-archive", "echo status details live archive")
		completeJob.ActualCwd = liveJTouchActualCwd
		completeJob.PeakRAM = 321
		completeJob.CPUtime = 4 * time.Second
		completeJob.StdOutC = compressStd([]byte("live\n"))
		completeJob.StdErrC = compressStd([]byte("stale\n"))

		killCalled, errt := jq.Touch(completeJob)
		So(errt, ShouldBeNil)
		So(killCalled, ShouldBeFalse)

		So(jq.Archive(completeJob, &JobEndState{
			Exited:   true,
			Exitcode: 0,
			PeakRAM:  654,
			CPUtime:  8 * time.Second,
			EndTime:  time.Now(),
			Stdout:   compressStd([]byte("final\n")),
			Stderr:   compressStd([]byte("done\n")),
		}), ShouldBeNil)

		completeStatus := requestDetails(completeJob.RepGroup, JobStateComplete, completeJob.Key())

		So(completeStatus.StdOut, ShouldEqual, "final\n")
		So(completeStatus.StdErr, ShouldEqual, "done\n")
		So(completeStatus.Exited, ShouldBeTrue)
		So(completeStatus.PeakRAM, ShouldEqual, 654)
		So(completeStatus.CPUtime, ShouldEqual, 8)
		So(completeStatus.SSHCommand, ShouldEqual, "")
	})
}

func TestStatusDetailsLiveFields(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Status details include running job live fields and SSH command", t, func() {
		ctx := context.Background()

		server, jq, runner, token, standardReqs := startSubscriptionIntegration(ctx, t)
		defer server.Stop(ctx, true)
		defer disconnect(jq)
		defer disconnect(runner)

		job := addAndStartLiveSubscriptionJob(server, jq, runner, standardReqs, "status-details-c1-live")
		killCalled, err := runner.touch(job, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte("out\n")),
			Stderr:  compressStd([]byte("err\n")),
		})
		So(err, ShouldBeNil)
		So(killCalled, ShouldBeFalse)

		testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
		defer testServer.Close()

		wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
		header := http.Header{}
		header.Add("Authorization", "Bearer "+string(token))

		ws, err := drainWebSocket(wsURL, header)
		So(err, ShouldBeNil)

		defer ws.Close()

		err = ws.WriteJSON(jstatusReq{
			Request:  jstatusRequestDetails,
			RepGroup: job.RepGroup,
			State:    JobStateRunning,
		})
		So(err, ShouldBeNil)

		status, ok := readJStatusMatching(ws, func(status JStatus) bool {
			return status.Key == job.Key() && !status.IsPushUpdate
		})
		So(ok, ShouldBeTrue)
		So(status.State, ShouldEqual, JobStateRunning)
		So(status.PeakRAM, ShouldEqual, 321)
		So(status.CPUtime, ShouldEqual, 4)
		So(status.StdOut, ShouldEqual, "out\n")
		So(status.StdErr, ShouldEqual, "err\n")
		So(status.SSHCommand, ShouldEqual,
			"ssh -- cloud_user@10.0.0.8 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'")
	})
}

func TestStatusDetailsLivePushUpdates(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Status details websocket pushes running job live fields and SSH command", t, func() {
		ctx := context.Background()

		server, jq, runner, token, standardReqs := startSubscriptionIntegration(ctx, t)
		defer server.Stop(ctx, true)
		defer disconnect(jq)
		defer disconnect(runner)

		job := addAndStartLiveSubscriptionJob(server, jq, runner, standardReqs, "status-details-c2-live")

		ws, cleanup := openStatusDetailsSubscription(ctx, server, token, job.RepGroup, job.Key())
		defer cleanup()

		killCalled, err := runner.touch(job, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte("out\n")),
			Stderr:  compressStd([]byte("err\n")),
		})
		So(err, ShouldBeNil)
		So(killCalled, ShouldBeFalse)

		status, ok := readJStatusMatching(ws, func(status JStatus) bool {
			return status.Key == job.Key() && status.IsPushUpdate && status.PeakRAM == 321
		})
		So(ok, ShouldBeTrue)
		So(status.State, ShouldEqual, JobStateRunning)
		So(status.CPUtime, ShouldEqual, 4)
		So(status.StdOut, ShouldEqual, "out\n")
		So(status.StdErr, ShouldEqual, "err\n")
		So(status.SSHCommand, ShouldEqual,
			"ssh -- cloud_user@10.0.0.8 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'")
	})

	Convey("Status details preserve live output when a later heartbeat updates only resources", t, func() {
		ctx := context.Background()

		server, jq, runner, token, standardReqs := startSubscriptionIntegration(ctx, t)
		defer server.Stop(ctx, true)
		defer disconnect(jq)
		defer disconnect(runner)

		job := addAndStartLiveSubscriptionJob(server, jq, runner, standardReqs, "status-details-c3-live")

		ws, cleanup := openStatusDetailsSubscription(ctx, server, token, job.RepGroup, job.Key())
		defer cleanup()

		killCalled, err := runner.touch(job, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte("progress 1\n")),
			Stderr:  compressStd([]byte("warning 1\n")),
		})
		So(err, ShouldBeNil)
		So(killCalled, ShouldBeFalse)

		_, ok := readJStatusMatching(ws, func(status JStatus) bool {
			return status.Key == job.Key() && status.IsPushUpdate && status.StdOut == "progress 1\n"
		})
		So(ok, ShouldBeTrue)

		killCalled, err = runner.touch(job, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 654,
			CPUtime: 7 * time.Second,
		})
		So(err, ShouldBeNil)
		So(killCalled, ShouldBeFalse)

		pushStatus, ok := readJStatusMatching(ws, func(status JStatus) bool {
			return status.Key == job.Key() && status.IsPushUpdate && status.PeakRAM == 654
		})
		So(ok, ShouldBeTrue)
		So(pushStatus.State, ShouldEqual, JobStateRunning)
		So(pushStatus.CPUtime, ShouldEqual, 7)
		So(pushStatus.StdOut, ShouldEqual, "progress 1\n")
		So(pushStatus.StdErr, ShouldEqual, "warning 1\n")

		err = ws.WriteJSON(jstatusReq{
			Request:  jstatusRequestDetails,
			RepGroup: job.RepGroup,
			State:    JobStateRunning,
		})
		So(err, ShouldBeNil)

		requestedStatus, ok := readJStatusMatching(ws, func(status JStatus) bool {
			return status.Key == job.Key() && !status.IsPushUpdate && status.PeakRAM == 654
		})
		So(ok, ShouldBeTrue)
		So(requestedStatus.CPUtime, ShouldEqual, 7)
		So(requestedStatus.StdOut, ShouldEqual, "progress 1\n")
		So(requestedStatus.StdErr, ShouldEqual, "warning 1\n")
	})
}

func TestStatusWSDetailsSubscriptionRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A details subscription queues updates that race initial status delivery", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "status-ws-details-race"
		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd:          "echo status ws details race",
			Cwd:          testCwd,
			ReqGroup:     repGroup,
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
		defer testServer.Close()

		wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
		header := http.Header{}
		header.Add("Authorization", "Bearer "+string(token))

		ws, err := drainWebSocket(wsURL, header)
		So(err, ShouldBeNil)

		defer ws.Close()

		hookEntered := make(chan struct{})
		releaseInitialStatus := make(chan struct{})

		var hookOnce sync.Once

		server.statusWSDetailsHook = func() {
			hookOnce.Do(func() {
				close(hookEntered)
				<-releaseInitialStatus
			})
		}

		defer func() {
			server.statusWSDetailsHook = nil
		}()

		err = ws.WriteJSON(jstatusReq{
			Request:  jstatusRequestDetails,
			RepGroup: repGroup,
			State:    JobStateReady,
		})
		So(err, ShouldBeNil)

		select {
		case <-hookEntered:
		case <-time.After(time.Second):
			So("timed out waiting for details hook", ShouldBeBlank)

			return
		}

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		close(releaseInitialStatus)

		So(ws.SetReadDeadline(time.Now().Add(2*time.Second)), ShouldBeNil)
		defer clearReadDeadlineBestEffort(ws)

		initialStatus, err := readUntilStatus(ws)
		So(err, ShouldBeNil)
		So(initialStatus.Key, ShouldEqual, ids[0])
		So(initialStatus.State, ShouldEqual, JobStateReady)
		So(initialStatus.IsPushUpdate, ShouldBeFalse)

		pushStatus, err := readUntilStatus(ws)
		So(err, ShouldBeNil)
		So(pushStatus.Key, ShouldEqual, ids[0])
		So(pushStatus.State, ShouldEqual, JobStateRunning)
		So(pushStatus.IsPushUpdate, ShouldBeTrue)
	})
}

func clearReadDeadlineBestEffort(ws *websocket.Conn) {
	if err := ws.SetReadDeadline(time.Time{}); err != nil {
		return
	}
}

// readJStatusMatching reads JStatus messages from ws until one satisfies match
// (or a generous deadline elapses), returning it and true, or a zero status and
// false on timeout. The status websocket also carries unsolicited count and
// state-change broadcast messages (a count decodes into a JStatus with an empty
// Key), so a request's response has to be picked out rather than assuming it is
// the very next message read - otherwise the read races those broadcasts under
// load and sees the wrong message.
func readJStatusMatching(ws *websocket.Conn, match func(JStatus) bool) (JStatus, bool) {
	if err := ws.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return JStatus{}, false
	}
	defer clearReadDeadlineBestEffort(ws)

	for {
		var status JStatus

		if err := ws.ReadJSON(&status); err != nil {
			return JStatus{}, false
		}

		if match(status) {
			return status, true
		}
	}
}

func drainWebSocket(wsURL string, header http.Header) (*websocket.Conn, error) {
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return nil, err
	}

	err = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if err != nil {
		return nil, err
	}

	for {
		var msg any

		errr := ws.ReadJSON(&msg)
		if errr != nil {
			break
		}
	}

	_ = ws.Close()

	ws, _, err = websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return nil, err
	}

	err = ws.SetReadDeadline(time.Now().Add(2 * time.Second))

	return ws, err
}

func testNoMoreMessages(ws *websocket.Conn) bool {
	var msg any

	err := ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if err != nil {
		return false
	}
	defer clearReadDeadlineBestEffort(ws)

	err = ws.ReadJSON(&msg)

	return err != nil
}

func readUntilStatus(ws *websocket.Conn) (*JStatus, error) {
	for {
		var msg map[string]any

		err := ws.ReadJSON(&msg)
		if err != nil {
			return nil, err
		}

		_, hasKey := msg["Key"]
		_, hasState := msg["State"]

		if !hasKey || !hasState {
			continue
		}

		statusJSON, err := json.Marshal(msg)
		if err != nil {
			return nil, err
		}

		var status JStatus

		err = json.Unmarshal(statusJSON, &status)

		return &status, err
	}
}

func limitedDrain(ws *websocket.Conn, count int) {
	for range count {
		var msg any

		ws.ReadJSON(&msg) //nolint:errcheck
	}
}

func TestJobSubscriptions(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Once the jobqueue server is up with jobs added", t, func() {
		serverConfig.Timings.ItemTTR = 100 * time.Second
		serverConfig.Timings.TouchInterval = 50 * time.Second
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		var repGroupJobs []*Job
		repGroupJobs = append(repGroupJobs, &Job{Cmd: "echo sub_test_1", Cwd: "/tmp", ReqGroup: "sub_group1",
			Requirements: standardReqs, RepGroup: "sub_rg1"})
		repGroupJobs = append(repGroupJobs, &Job{Cmd: "echo sub_test_2", Cwd: "/tmp", ReqGroup: "sub_group2",
			Requirements: standardReqs, RepGroup: "sub_rg2"})

		inserts, already, err := jq.Add(repGroupJobs, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)
		So(already, ShouldEqual, 0)

		testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
		defer testServer.Close()

		wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
		header := http.Header{}
		header.Add("Authorization", "Bearer "+string(token))

		Convey("Multiple clients can connect and subscribe to different job updates", func() {
			ws1, err := drainWebSocket(wsURL, header)
			So(err, ShouldBeNil)
			defer ws1.Close()

			ws2, err := drainWebSocket(wsURL, header)
			So(err, ShouldBeNil)
			defer ws2.Close()

			ws3, err := drainWebSocket(wsURL, header)
			So(err, ShouldBeNil)
			defer ws3.Close()

			rg1Jobs, err := jq.GetByRepGroup("sub_rg1", false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(rg1Jobs), ShouldEqual, 1)

			rg2Jobs, err := jq.GetByRepGroup("sub_rg2", false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(rg2Jobs), ShouldEqual, 1)

			err = ws1.WriteJSON(jstatusReq{
				Request:  jstatusRequestDetails,
				RepGroup: "sub_rg1",
				State:    JobStateReady,
			})
			So(err, ShouldBeNil)

			err = ws2.WriteJSON(jstatusReq{
				Request:  jstatusRequestDetails,
				RepGroup: "sub_rg2",
				State:    JobStateReady,
			})
			So(err, ShouldBeNil)

			err = ws3.WriteJSON(jstatusReq{
				Request: jstatusRequestCurrent,
			})
			So(err, ShouldBeNil)

			limitedDrain(ws1, 1)
			limitedDrain(ws2, 1)

			Convey("Only subscribed clients receive detailed push updates", func() {
				job, errr := jq.Reserve(50 * time.Millisecond)
				So(errr, ShouldBeNil)
				So(job.RepGroup, ShouldEqual, "sub_rg1")

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)

				status1, errr := readUntilStatus(ws1)
				So(errr, ShouldBeNil)
				So(status1.IsPushUpdate, ShouldBeTrue)
				So(status1.RepGroup, ShouldEqual, "sub_rg1")
				So(status1.State, ShouldEqual, JobStateRunning)

				status1, errr = readUntilStatus(ws1)
				So(errr, ShouldBeNil)
				So(status1.IsPushUpdate, ShouldBeTrue)
				So(status1.RepGroup, ShouldEqual, "sub_rg1")
				So(status1.State, ShouldEqual, JobStateComplete)

				_, err = readUntilStatus(ws1)
				So(err, ShouldNotBeNil)

				var msg any
				err = ws3.ReadJSON(&msg)
				So(err, ShouldBeNil)

				mapMsg, isMap := msg.(map[string]any)
				So(isMap, ShouldBeTrue)

				_, hasIsPushUpdate := mapMsg["IsPushUpdate"]
				So(hasIsPushUpdate, ShouldBeFalse)

				ws2, err = drainWebSocket(wsURL, header)
				So(err, ShouldBeNil)
				defer ws2.Close()

				err = ws2.WriteJSON(jstatusReq{
					Request:  jstatusRequestDetails,
					RepGroup: "sub_rg2",
					State:    JobStateReady,
				})
				So(err, ShouldBeNil)

				limitedDrain(ws2, 1)

				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.RepGroup, ShouldEqual, "sub_rg2")

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)

				status2, errr := readUntilStatus(ws2)
				So(errr, ShouldBeNil)
				So(status2.IsPushUpdate, ShouldBeTrue)
				So(status2.RepGroup, ShouldEqual, "sub_rg2")
				So(status2.State, ShouldEqual, JobStateRunning)

				status2, errr = readUntilStatus(ws2)
				So(errr, ShouldBeNil)
				So(status2.IsPushUpdate, ShouldBeTrue)
				So(status2.RepGroup, ShouldEqual, "sub_rg2")
				So(status2.State, ShouldEqual, JobStateComplete)

				_, err = readUntilStatus(ws2)
				So(err, ShouldNotBeNil)
			})

			Convey("Clients can unsubscribe to stop receiving updates", func() {
				ws1, err = drainWebSocket(wsURL, header)
				So(err, ShouldBeNil)
				defer ws1.Close()

				err = ws1.WriteJSON(jstatusReq{
					Request:  jstatusRequestDetails,
					RepGroup: "sub_rg1",
					State:    JobStateReady,
				})
				So(err, ShouldBeNil)

				limitedDrain(ws1, 3)

				err = ws1.WriteJSON(jstatusReq{
					Request: jstatusRequestUnsubscribe,
				})
				So(err, ShouldBeNil)

				job, errr := jq.Reserve(50 * time.Millisecond)
				So(errr, ShouldBeNil)
				So(job.RepGroup, ShouldEqual, "sub_rg1")

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)

				So(testNoMoreMessages(ws1), ShouldBeTrue)
			})

			Convey("Subscriptions are cleaned up when connections close", func() {
				ws4, err := drainWebSocket(wsURL, header)
				So(err, ShouldBeNil)

				cleanupIDs, err := jq.AddAndReturnIDs([]*Job{{
					Cmd:          "echo sub_cleanup",
					Cwd:          "/tmp",
					ReqGroup:     "sub_cleanup",
					Requirements: standardReqs,
					RepGroup:     "sub_cleanup",
				}}, envVars, true)
				So(err, ShouldBeNil)
				So(cleanupIDs, ShouldHaveLength, 1)

				jobKey := cleanupIDs[0]

				err = ws4.WriteJSON(jstatusReq{
					Key: jobKey,
				})
				So(err, ShouldBeNil)

				status, err := readUntilStatus(ws4)
				So(err, ShouldBeNil)
				So(status.Key, ShouldEqual, jobKey)

				subscriptionID, sub, ok := statusSubscriptionForKeyBecomes(server, jobKey, time.Second)
				So(ok, ShouldBeTrue)
				So(subscriptionID, ShouldNotBeBlank)

				ws4.Close()
				So(serverClientSubscriptionRemoved(server, subscriptionID, 5*time.Second), ShouldBeTrue)
				So(serverSubscriptionClosed(sub, time.Second), ShouldBeTrue)
			})
		})
	})
}

func statusSubscriptionForKeyBecomes(
	server *Server,
	key string,
	timeout time.Duration,
) (string, *serverSubscription, bool) {
	deadline := time.After(timeout)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if id, sub, ok := statusSubscriptionForKey(server, key); ok {
			return id, sub, true
		}

		select {
		case <-deadline:
			return "", nil, false
		case <-ticker.C:
		}
	}
}

func statusSubscriptionForKey(server *Server, key string) (string, *serverSubscription, bool) {
	server.csmutex.RLock()
	defer server.csmutex.RUnlock()

	for id, sub := range server.clientSubscriptions {
		if !sub.stateChanges || !sub.matchesKey(key) {
			continue
		}

		return id, sub, true
	}

	return "", nil, false
}

func serverClientSubscriptionRemoved(server *Server, id string, timeout time.Duration) bool {
	deadline := time.After(timeout)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, exists := server.clientSubscription(id); !exists {
			return true
		}

		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}

func serverSubscriptionClosed(sub *serverSubscription, timeout time.Duration) bool {
	if sub == nil {
		return false
	}

	select {
	case <-sub.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func writeCurrentJStatusWithDeadline(ws *websocket.Conn) error {
	if err := ws.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	defer clearWriteDeadlineBestEffort(ws)

	return ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
}

func clearWriteDeadlineBestEffort(ws *websocket.Conn) {
	if err := ws.SetWriteDeadline(time.Time{}); err != nil {
		return
	}
}

func TestWebUIModificationStaticContract(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The status page exposes Modify actions and modal fields", t, func() {
		statusHTML := readStaticText("static/status.html")
		actionHandlers := readStaticText("static/js/wr/action-handlers.js")
		viewModel := readStaticText("static/js/wr/status-viewmodel.js")

		So(statusHTML, ShouldContainSubstring, "jobCanModify")
		So(statusHTML, ShouldContainSubstring, "showModifyJob")
		So(statusHTML, ShouldContainSubstring, "submit: function() { $root.submitModifyJob(); return false; }")
		So(actionHandlers, ShouldContainSubstring, "showModifyJob")
		So(viewModel, ShouldContainSubstring, "jobCanModify")

		for _, field := range []string{
			"cmd",
			"cwd",
			"cwd_matters",
			"change_home",
			"req_grp",
			"memory",
			"time",
			"cpus",
			"disk",
			"priority",
			"retries",
			"override",
			"no_retry_over_walltime",
			"limit_grps",
			"modules",
			"deps",
			"cmd_deps",
			"on_failure",
			"on_success",
			"on_exit",
			"other",
			"mounts",
			"monitor_docker",
			"with_docker",
			"with_singularity",
			"container_mounts",
			"env",
		} {
			So(statusHTML, ShouldContainSubstring, `data-modify-field="`+field+`"`)
		}
	})

	Convey("The browser modify module builds requests and applies responses", t, func() {
		runNodeWebUITest(t, `
import assert from 'node:assert/strict';
import {
  createModifyForm,
  createModifyPayload,
  createModifyRequest,
  jobCanModify,
  replaceModifiedJobs,
  trimTrailingNewline
} from './jobqueue/static/js/wr/modify-job.js';

const oldKey = '11111111111111111111111111111111';
const newKey = '22222222222222222222222222222222';
const oldJob = {
  Key: oldKey,
  State: 'ready',
  Cmd: 'echo web old',
  CwdBase: '/tmp/web-old',
  CwdMatters: false,
  HomeChanged: false,
  ReqGroup: 'web-old',
  ExpectedRAM: 64,
  ExpectedTime: 60,
  Cores: 1,
  RequestedDisk: 0,
  Priority: 1,
  Retries: 1,
  Override: 0,
  NoRetryOverWalltime: 0,
  LimitGroups: ['old:1'],
  Modules: ['oldmod'],
  Dependencies: ['old-dep', 'echo dep [/tmp/dep-old]'],
  Behaviours: '{"on_exit":[{"nothing":true}]}',
  OtherRequests: ['scheduler_queue:old'],
  Mounts: '[{"Mount":"oldmnt","Targets":[{"Profile":"old","Path":"old/data"}]}]',
  MonitorDocker: 'old-docker',
  WithDocker: '',
  WithSingularity: 'old.sif',
  ContainerMounts: '/old:/old',
  Env: [],
  EnvOverrides: []
};

const form = createModifyForm(oldJob);
assert.equal(form.cmd, 'echo web old');
assert.equal(form.cwd, '/tmp/web-old');
assert.equal(form.cwdMatters, false);
assert.equal(form.changeHome, false);
assert.equal(form.reqGrp, 'web-old');
assert.equal(form.memory, '64M');
assert.equal(form.time, '1m');
assert.equal(form.cpus, '1');
assert.equal(form.disk, '0');
assert.equal(form.priority, '1');
assert.equal(form.retries, '1');
assert.equal(form.override, '0');
assert.equal(form.noRetryOverWalltime, '');
assert.equal(form.limitGrps, 'old:1');
assert.equal(form.modules, 'oldmod');
assert.equal(form.deps, 'old-dep');
assert.deepEqual(JSON.parse(form.cmdDeps), [{cmd: 'echo dep', cwd: '/tmp/dep-old'}]);
assert.equal(form.onFailure, '');
assert.equal(form.onSuccess, '');
assert.deepEqual(JSON.parse(form.onExit), [{nothing: true}]);
assert.equal(form.other, 'scheduler_queue:old');
assert.deepEqual(JSON.parse(form.mounts), [{Mount: 'oldmnt', Targets: [{Profile: 'old', Path: 'old/data'}]}]);
assert.equal(form.monitorDocker, 'old-docker');
assert.equal(form.withDocker, '');
assert.equal(form.withSingularity, 'old.sif');
assert.equal(form.containerMounts, '/old:/old');

const dayDurationForm = createModifyForm({
  ...oldJob,
  ExpectedTime: 86400,
  NoRetryOverWalltime: 172800
});
assert.equal(dayDurationForm.time, '24h');
assert.equal(dayDurationForm.noRetryOverWalltime, '48h');
const dayDurationPayload = createModifyPayload(dayDurationForm);
assert.equal(dayDurationPayload.time, '24h');
assert.equal(dayDurationPayload.no_retry_over_walltime, '48h');

Object.assign(form, {
  cmd: 'echo web new',
  cwd: '/tmp/web-new',
  cwdMatters: true,
  changeHome: true,
  reqGrp: 'web-new',
  memory: '128M',
  time: '3m',
  cpus: '2',
  disk: '5',
  priority: '12',
  retries: '6',
  override: '2',
  noRetryOverWalltime: '10m',
  limitGrps: 'new:2',
  modules: 'mod-a\nmod-b',
  deps: 'dep-a',
  cmdDeps: '[{"cmd":"echo dep","cwd":"/tmp/dep"}]',
  onFailure: '[{"cleanup":true}]',
  onSuccess: '[{"remove":true}]',
  onExit: '[{"nothing":true}]',
  other: 'cloud_os:Ubuntu 22\nscheduler_queue:short',
  mounts: '[{"Mount":"mnt","Targets":[{"Profile":"p","Path":"bucket/data"}]}]',
  monitorDocker: 'dock-new',
  withDocker: 'ubuntu:22.04',
  withSingularity: '',
  containerMounts: '/data:/data'
});

const expectedPayload = {
  cmd: 'echo web new',
  cwd: '/tmp/web-new',
  cwd_matters: true,
  change_home: true,
  req_grp: 'web-new',
  memory: '128M',
  time: '3m',
  cpus: 2,
  disk: 5,
  priority: 12,
  retries: 6,
  override: 2,
  no_retry_over_walltime: '10m',
  limit_grps: ['new:2'],
  modules: ['mod-a', 'mod-b'],
  deps: ['dep-a'],
  cmd_deps: [{cmd: 'echo dep', cwd: '/tmp/dep'}],
  on_failure: [{cleanup: true}],
  on_success: [{remove: true}],
  on_exit: [{nothing: true}],
  other: {cloud_os: 'Ubuntu 22', scheduler_queue: 'short'},
  mounts: [{Mount: 'mnt', Targets: [{Profile: 'p', Path: 'bucket/data'}]}],
  monitor_docker: 'dock-new',
  with_docker: 'ubuntu:22.04',
  with_singularity: '',
  container_mounts: '/data:/data'
};

assert.deepEqual(createModifyPayload(form), expectedPayload);
assert.throws(
  () => createModifyPayload({...form, cmdDeps: '{}'}),
  /cmd_deps must be a JSON array/
);
assert.throws(
  () => createModifyPayload({...form, mounts: '{}'}),
  /mounts must be a JSON array/
);

const request = createModifyRequest(form, 'web-token');
assert.equal(request.url, '/rest/v1/jobs/' + oldKey);
assert.equal(request.options.method, 'PATCH');
assert.equal(request.options.headers.Authorization, 'Bearer web-token');
assert.deepEqual(JSON.parse(request.options.body), expectedPayload);

const returnedJob = {
  ...oldJob,
  Key: newKey,
  Cmd: 'echo web new',
  CwdBase: '/tmp/web-new',
  CwdMatters: true,
  HomeChanged: true,
  ReqGroup: 'web-new',
  ExpectedRAM: 128,
  ExpectedTime: 180,
  Cores: 2,
  RequestedDisk: 5,
  Priority: 12,
  Retries: 6,
  Override: 2,
  NoRetryOverWalltime: 600,
  LimitGroups: ['new:2'],
  Modules: ['mod-a', 'mod-b'],
  Dependencies: ['dep-a', 'echo dep [/tmp/dep]'],
  Behaviours: '{"on_failure":[{"cleanup":true}],"on_success":[{"remove":true}],"on_exit":[{"nothing":true}]}',
  OtherRequests: ['cloud_os:Ubuntu 22', 'scheduler_queue:short'],
  Mounts: '[{"Mount":"mnt","Targets":[{"Profile":"p","Path":"bucket/data"}]}]',
  MonitorDocker: 'dock-new',
  WithDocker: 'ubuntu:22.04',
  WithSingularity: '',
  ContainerMounts: '/data:/data'
};
const replaced = replaceModifiedJobs([oldJob], {modified: {[newKey]: oldKey}, jobs: [returnedJob]});
assert.equal(replaced.length, 1);
assert.equal(replaced[0].Key, newKey);
assert.equal(replaced[0].Cmd, 'echo web new');
assert.equal(replaced[0].CwdBase, '/tmp/web-new');
assert.equal(replaced[0].CwdMatters, true);
assert.equal(replaced[0].HomeChanged, true);
assert.equal(replaced[0].ReqGroup, 'web-new');
assert.equal(replaced[0].ExpectedRAM, 128);
assert.equal(replaced[0].ExpectedTime, 180);
assert.equal(replaced[0].RequestedDisk, 5);
assert.equal(replaced[0].NoRetryOverWalltime, 600);
assert.equal(replaced.some(job => job.Key === oldKey), false);

for (const state of ['delayed', 'ready', 'dependent', 'buried']) {
  assert.equal(jobCanModify({State: state}), true);
}
for (const state of ['reserved', 'running', 'lost', 'complete']) {
  assert.equal(jobCanModify({State: state}), false);
}
assert.equal(trimTrailingNewline('no editable jobs matched\n'), 'no editable jobs matched');
`)
	})

	Convey("The env editor uses only overrides and preserves inherited env", t, func() {
		runNodeWebUITest(t, `
import assert from 'node:assert/strict';
import { createModifyForm, createModifyPayload, replaceModifiedJobs } from './jobqueue/static/js/wr/modify-job.js';

const key = '33333333333333333333333333333333';
const job = {
  Key: key,
  State: 'ready',
  Cmd: 'echo env',
  CwdBase: '/tmp',
  ReqGroup: 'env',
  ExpectedRAM: 1,
  ExpectedTime: 60,
  Cores: 1,
  RequestedDisk: 0,
  Priority: 1,
  Retries: 1,
  Override: 0,
  LimitGroups: [],
  Modules: [],
  Dependencies: [],
  OtherRequests: [],
  Env: ['PATH=/bin', 'INHERITED=base', 'WEB_ONLY=old'],
  EnvOverrides: ['WEB_ONLY=old']
};

const form = createModifyForm(job);
assert.equal(form.env, 'WEB_ONLY=old');
assert.equal(form.env.includes('PATH=/bin'), false);
assert.equal(form.env.includes('INHERITED=base'), false);

form.env = 'WEB_ONLY=new';
const changedPayload = createModifyPayload(form);
assert.deepEqual(changedPayload.env, ['WEB_ONLY=new']);
assert.equal(JSON.stringify(changedPayload).includes('PATH=/bin'), false);
assert.equal(JSON.stringify(changedPayload).includes('INHERITED=base'), false);

let replaced = replaceModifiedJobs([job], {
  modified: {[key]: key},
  jobs: [{...job, Env: [], EnvOverrides: ['WEB_ONLY=new']}]
});
assert.deepEqual(replaced[0].Env, ['PATH=/bin', 'INHERITED=base', 'WEB_ONLY=new']);

form.env = '';
const clearedPayload = createModifyPayload(form);
assert.deepEqual(clearedPayload.env, []);

replaced = replaceModifiedJobs([job], {
  modified: {[key]: key},
  jobs: [{...job, Env: [], EnvOverrides: []}]
});
assert.deepEqual(replaced[0].EnvOverrides, []);
assert.deepEqual(replaced[0].Env, ['PATH=/bin', 'INHERITED=base']);
`)
	})

	Convey("The browser modify module reports failed edits without changing rows", t, func() {
		runNodeWebUITest(t, `
import assert from 'node:assert/strict';
import { replaceModifiedJobs, trimTrailingNewline } from './jobqueue/static/js/wr/modify-job.js';

const row = {Key: '44444444444444444444444444444444', State: 'ready', Priority: 1};
const priorityError = 'priority value (300) is not in the range 0..255';
assert.equal(trimTrailingNewline(priorityError + '\n'), priorityError);
assert.equal(trimTrailingNewline('no editable jobs matched\n'), 'no editable jobs matched');
assert.deepEqual(replaceModifiedJobs([row], {modified: {}, jobs: []}), [row]);
`)
	})

	Convey("The real modal handler submits PATCHes and keeps validation errors visible", t, func() {
		runNodeModalHandlerTest(t, `
import assert from 'node:assert/strict';
import { showModifyJob, submitModifyJob } from './jobqueue/static/js/wr/modal-handlers.js';

globalThis.ko = {
  observable(initial) {
    let value = initial;
    return function observable(next) {
      if (arguments.length > 0) {
        value = next;
        return observable;
      }

      return value;
    };
  }
};

function viewModelWith(job) {
  return {
    token: 'web-token',
    detailsOA: ko.observable([job]),
    modifyJobModalVisible: ko.observable(false),
    modifyJobForm: ko.observable(),
    modifyJobError: ko.observable(''),
    modifyJobSubmitting: ko.observable(false)
  };
}

const oldKey = '55555555555555555555555555555555';
const newKey = '66666666666666666666666666666666';
const oldJob = {
  Key: oldKey,
  State: 'ready',
  Cmd: 'echo modal old',
  CwdBase: '/tmp/modal-old',
  CwdMatters: false,
  HomeChanged: false,
  ReqGroup: 'modal-old',
  ExpectedRAM: 64,
  ExpectedTime: 60,
  Cores: 1,
  RequestedDisk: 0,
  Priority: 1,
  Retries: 1,
  Override: 0,
  NoRetryOverWalltime: 0,
  LimitGroups: [],
  Modules: [],
  Dependencies: [],
  Behaviours: '',
  OtherRequests: [],
  Mounts: '',
  MonitorDocker: '',
  WithDocker: '',
  WithSingularity: '',
  ContainerMounts: '',
  Env: ['PATH=/bin'],
  EnvOverrides: [],
  Walltime: 0
};

const successVM = viewModelWith(oldJob);
showModifyJob(successVM, oldJob);
assert.equal(successVM.modifyJobModalVisible(), true);
successVM.modifyJobForm().cmd('echo modal new');

const fetchCalls = [];
globalThis.fetch = async (url, options) => {
  fetchCalls.push({url, options});

  return {
    ok: true,
    json: async () => ({
      modified: {[newKey]: oldKey},
      jobs: [{...oldJob, Key: newKey, Cmd: 'echo modal new', Env: ['PATH=/bin'], Walltime: 0}]
    })
  };
};

assert.equal(await submitModifyJob(successVM), true);
assert.equal(successVM.modifyJobModalVisible(), false);
assert.equal(fetchCalls.length, 1);
assert.equal(fetchCalls[0].url, '/rest/v1/jobs/' + oldKey);
assert.equal(fetchCalls[0].options.method, 'PATCH');
assert.equal(fetchCalls[0].options.headers.Authorization, 'Bearer web-token');
assert.equal(JSON.parse(fetchCalls[0].options.body).cmd, 'echo modal new');
assert.equal(successVM.detailsOA().length, 1);
assert.equal(successVM.detailsOA()[0].Key, newKey);
assert.equal(successVM.detailsOA()[0].Cmd, 'echo modal new');

const errorVM = viewModelWith(oldJob);
showModifyJob(errorVM, oldJob);
errorVM.modifyJobForm().priority('300');
const priorityError = 'priority value (300) is not in the range 0..255';
globalThis.fetch = async () => ({
  ok: false,
  text: async () => priorityError + '\n'
});

assert.equal(await submitModifyJob(errorVM), false);
assert.equal(errorVM.modifyJobModalVisible(), true);
assert.equal(errorVM.modifyJobError(), priorityError);
assert.equal(errorVM.detailsOA().length, 1);
assert.equal(errorVM.detailsOA()[0].Key, oldKey);
assert.equal(errorVM.detailsOA()[0].Priority, 1);

globalThis.fetch = async () => ({
  ok: false,
  text: async () => 'no editable jobs matched\n'
});

assert.equal(await submitModifyJob(errorVM), false);
assert.equal(errorVM.modifyJobModalVisible(), true);
assert.equal(errorVM.modifyJobError(), 'no editable jobs matched');
assert.equal(errorVM.detailsOA()[0].Key, oldKey);
assert.equal(errorVM.detailsOA()[0].Priority, 1);
`)
	})
}

func TestStatusPageLiveIntrospectionAssets(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Status page assets render live introspection without an embedded terminal", t, func() {
		html := readTextAsset(t, "static/status.html")
		handler := readTextAsset(t, "static/js/wr/websocket-handler.js")
		utility := readTextAsset(t, "static/js/wr/utility.js")
		css := readTextAsset(t, "static/css/wr-0.36.0.css")

		So(html, ShouldContainSubstring, "!Exited && (State == 'running' || State == 'reserved') && PeakRAM > 0")
		So(html, ShouldContainSubstring, "text: window.wrUtils.mbIEC(PeakRAM)")
		So(html, ShouldContainSubstring, "!Exited && (State == 'running' || State == 'reserved') && CPUtime > 0")
		So(html, ShouldContainSubstring, "text: 'CPU: ' + window.wrUtils.toDuration(CPUtime)")
		So(html, ShouldContainSubstring, "ko if: StdOut")
		So(html, ShouldContainSubstring, "ko if: StdErr")
		So(html, ShouldContainSubstring, "ko if: SSHCommand")
		So(html, ShouldContainSubstring, "data-clipboard-text': SSHCommand")
		So(html, ShouldContainSubstring, "ssh-command-text")
		So(html, ShouldNotContainSubstring, "xterm")
		So(html, ShouldNotContainSubstring, "web-terminal")

		So(handler, ShouldContainSubstring, "mergeJobDetailsPushUpdate")
		So(handler, ShouldContainSubstring, "viewModel.detailsOA.splice(index, 1, merged)")
		So(utility, ShouldContainSubstring, "copyTextToClipboard")
		So(utility, ShouldContainSubstring, "navigator.clipboard.writeText")
		So(css, ShouldContainSubstring, ".ssh-command-control")
		So(css, ShouldContainSubstring, ".ssh-command-text")
	})
}

func readStaticText(path string) string {
	data, err := staticFS.ReadFile(path)
	So(err, ShouldBeNil)

	return string(data)
}

func TestStatusPageLivePushUpdateBehaviour(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to exercise the status page JavaScript")
	}

	Convey("Status page job details push updates replace visible live data", t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		//nolint:gosec // The Node script is a constant test harness.
		cmd := exec.CommandContext(ctx, "node", "-e", statusPageLivePushUpdateScript())
		output, err := cmd.CombinedOutput()
		So(string(output), ShouldBeBlank)
		So(err, ShouldBeNil)
	})
}

func statusPageLivePushUpdateScript() string {
	return `
const fs = require('fs');
const vm = require('vm');

let source = fs.readFileSync('static/js/wr/websocket-handler.js', 'utf8');
source = source
    .replace(/^import .*;\n/gm, '')
    .replace(/export function /g, 'function ');

const context = {
    console,
    createRepGroupTracker() {
        return {};
    },
    setupLiveWalltime(job, walltime) {
        job.LiveWalltime = () => walltime;
    }
};
context.globalThis = context;
vm.createContext(context);
vm.runInContext(source + '\nglobalThis.handleJobDetailsMessage = handleJobDetailsMessage;', context,
    { filename: 'websocket-handler.js' });

function observableArray(initial) {
    const values = initial.slice();
    function observable(next) {
        if (arguments.length > 0) {
            values.splice(0, values.length, ...next);
            return values;
        }

        return values;
    }
    observable.push = value => values.push(value);
    observable.splice = (...args) => values.splice(...args);
    return observable;
}

function assert(condition, message) {
    if (!condition) {
        throw new Error(message);
    }
}

const sshCommand = "ssh -- ubuntu@10.0.0.8 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'";
const existing = {
    Key: 'k',
    RepGroup: 'rg1',
    State: 'running',
    Exited: false,
    PeakRAM: 0,
    CPUtime: 0,
    StdOut: '',
    StdErr: '',
    SSHCommand: '',
    Walltime: 12,
    Started: 123,
    Cmd: 'sleep 60',
    ExpectedRAM: 100,
    ExpectedTime: 60,
    RequestedDisk: 0,
    Cores: 1,
    Attempts: 1
};
const viewModel = {
    detailsRepgroup: 'rg1',
    detailsOA: observableArray([existing]),
    isSearchMode: () => false,
    repGroups: [{ id: 'rg1' }],
    newJobsInfo: {}
};

context.handleJobDetailsMessage(viewModel, {
    Key: 'k',
    RepGroup: 'rg1',
    State: 'running',
    IsPushUpdate: true,
    PeakRAM: 321,
    CPUtime: 4,
    StdOut: 'alpha-out\n',
    StdErr: 'alpha-err\n',
    SSHCommand: sshCommand
});

let job = viewModel.detailsOA()[0];
assert(viewModel.detailsOA().length === 1, 'push update must replace the existing detail row');
assert(job.PeakRAM === 321, 'first push PeakRAM was not applied');
assert(job.CPUtime === 4, 'first push CPUtime was not applied');
assert(job.StdOut === 'alpha-out\n', 'first push stdout was not applied');
assert(job.StdErr === 'alpha-err\n', 'first push stderr was not applied');
assert(job.SSHCommand === sshCommand, 'first push SSH command was not applied');
assert(job.Cmd === 'sleep 60', 'push update should preserve existing command text');
assert(job.LiveWalltime() === 12, 'push update should preserve live walltime fallback');

context.handleJobDetailsMessage(viewModel, {
    Key: 'k',
    RepGroup: 'rg1',
    State: 'reserved',
    IsPushUpdate: true,
    PeakRAM: 222,
    CPUtime: 5,
    Cmd: '',
    ExpectedRAM: 0,
    ExpectedTime: 0,
    RequestedDisk: 0,
    Cores: 0,
    Attempts: 0
});

job = viewModel.detailsOA()[0];
assert(job.State === 'reserved', 'reserved push State was not applied');
assert(job.PeakRAM === 222, 'reserved push PeakRAM was not applied');
assert(job.CPUtime === 5, 'reserved push CPUtime was not applied');
assert(job.Cmd === 'sleep 60', 'reserved push should preserve existing command text');
assert(job.ExpectedRAM === 100, 'reserved push should preserve expected RAM');
assert(job.ExpectedTime === 60, 'reserved push should preserve expected time');
assert(job.Cores === 1, 'reserved push should preserve cores');
assert(job.Attempts === 1, 'reserved push should preserve attempts');

context.handleJobDetailsMessage(viewModel, {
    Key: 'k',
    RepGroup: 'rg1',
    State: 'running',
    IsPushUpdate: true,
    PeakRAM: 654,
    CPUtime: 8,
    StdOut: 'beta-out\n',
    StdErr: 'beta-err\n',
    SSHCommand: sshCommand
});

job = viewModel.detailsOA()[0];
assert(viewModel.detailsOA().length === 1, 'second push update must keep one detail row');
assert(job.PeakRAM === 654, 'second push PeakRAM was not applied');
assert(job.CPUtime === 8, 'second push CPUtime was not applied');
assert(job.StdOut === 'beta-out\n', 'second push stdout was not applied');
assert(job.StdErr === 'beta-err\n', 'second push stderr was not applied');
assert(!job.StdOut.includes('alpha-out'), 'old stdout should not remain visible');
assert(!job.StdErr.includes('alpha-err'), 'old stderr should not remain visible');
`
}

func readTextAsset(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	return string(data)
}

func runNodeWebUITest(t *testing.T, source string) {
	t.Helper()

	repoRoot := repoRootForWebUITest(t)
	moduleURL := fileURL(filepath.Join(repoRoot, "jobqueue/static/js/wr/modify-job.js"))
	source = strings.ReplaceAll(source, "'./jobqueue/static/js/wr/modify-job.js'", fmt.Sprintf("%q", moduleURL))

	runNodeScript(t, repoRoot, source)
}

func runNodeModalHandlerTest(t *testing.T, source string) {
	t.Helper()

	repoRoot := repoRootForWebUITest(t)
	dir := t.TempDir()

	utility := `export function capitalizeFirstLetter(value) {
  return String(value || '').charAt(0).toUpperCase() + String(value || '').slice(1);
}

export function setupLiveWalltime(job, walltime) {
  job.LiveWalltime = function () {
    return walltime || 0;
  };
}
`
	utilityPath := filepath.Join(dir, "utility.js")
	err := os.WriteFile(utilityPath, []byte(utility), 0600)
	So(err, ShouldBeNil)

	sourcePath := filepath.Join(repoRoot, "jobqueue/static/js/wr/modal-handlers.js")
	modalSource, err := os.ReadFile(sourcePath)
	So(err, ShouldBeNil)

	rewritten := strings.ReplaceAll(string(modalSource), "'/js/wr/modify-job.js'",
		fmt.Sprintf("%q", fileURL(filepath.Join(repoRoot, "jobqueue/static/js/wr/modify-job.js"))))
	rewritten = strings.ReplaceAll(rewritten, "'/js/wr/utility.js'", fmt.Sprintf("%q", fileURL(utilityPath)))

	modalPath := filepath.Join(dir, "modal-handlers.js")
	// #nosec G703 -- modalPath is generated inside t.TempDir for a test-only module.
	err = os.WriteFile(modalPath, []byte(rewritten), 0600)
	So(err, ShouldBeNil)

	source = strings.ReplaceAll(source, "'./jobqueue/static/js/wr/modal-handlers.js'",
		fmt.Sprintf("%q", fileURL(modalPath)))

	runNodeScript(t, repoRoot, source)
}

func runNodeScript(t *testing.T, repoRoot, source string) {
	t.Helper()

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH; skipping browser module contract test")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "web_ui_modify_test.mjs")
	err := os.WriteFile(script, []byte(source), 0600)
	So(err, ShouldBeNil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	So(string(output), ShouldEqual, "")
	So(err, ShouldBeNil)
}

func fileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}

func repoRootForWebUITest(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	So(err, ShouldBeNil)

	return filepath.Dir(wd)
}
