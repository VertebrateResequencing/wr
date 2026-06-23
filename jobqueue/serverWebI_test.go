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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

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

			w = httptest.NewRecorder()
			r = httptest.NewRequest(http.MethodGet, "/nonexistent.html", nil)
			r.Header.Set("Authorization", "Bearer "+string(token))
			handler(w, r)
			resp = w.Result()
			So(resp.StatusCode, ShouldEqual, http.StatusNotFound)

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

					if stateCount.RepGroup == "+all+" {
						receivedJobs[stateCount.RepGroup] = true
					} else {
						receivedGroups[stateCount.RepGroup] = true
					}
				}

				So(receivedJobs, ShouldContainKey, "+all+")
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
					// Read with a deadline rather than pre-sleeping a fixed time:
					// each ReadJSON below blocks until the next expected status
					// arrives (or the deadline lapses), which is robust to the
					// broadcast being slow under heavy parallel-test load.
					So(ws.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)

					for i := range expectedNum {
						var status JStatus

						err = ws.ReadJSON(&status)
						So(err, ShouldBeNil)
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

					// Read with a deadline instead of a fixed pre-sleep: the
					// single matching status is awaited up to the deadline, which
					// tolerates a slow broadcast under heavy parallel-test load.
					So(ws.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)

					var status JStatus

					err = ws.ReadJSON(&status)
					So(err, ShouldBeNil)
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
					Request:  "remove",
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

				err = ws.ReadJSON(&sc)
				So(err, ShouldBeNil)

				err = ws3.WriteJSON(jstatusReq{Request: jstatusRequestCurrent})
				So(err, ShouldBeNil)

				So(ws3.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)

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
							if werr := ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}); werr != nil {
								return
							}
						}
					}
				}()

				So(ws.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)

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

				So(ws.SetReadDeadline(time.Time{}), ShouldBeNil)
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
							if werr := ws.WriteJSON(jstatusReq{Request: jstatusRequestCurrent}); werr != nil {
								return
							}
						}
					}
				}()

				So(ws.SetReadDeadline(time.Now().Add(30*time.Second)), ShouldBeNil)

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

				So(ws.SetReadDeadline(time.Time{}), ShouldBeNil)

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
