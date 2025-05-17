// Copyright © 2025 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package jobqueue

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/gorilla/websocket"
	. "github.com/smartystreets/goconvey/convey"
)

func TestServerWebI(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	defer func() {
		os.RemoveAll(filepath.Join(os.TempDir(), AppName+"_cwd"))
	}()

	Convey("Once the jobqueue server is up", t, func() {
		ServerItemTTR = 100 * time.Second
		ClientTouchInterval = 50 * time.Second
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
		// err = jq.Execute(ctx, job, config.RunnerExecShell)
		// So(err, ShouldBeNil)

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

			Convey("The websocket handler responds to current requests", func() {
				err = ws.WriteJSON(jstatusReq{Request: "current"})
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
				So(receivedToBuried, ShouldEqual, 2)
				So(receivedToRunning, ShouldEqual, 2)
				So(receivedToComplete, ShouldEqual, 1)
			})

			Convey("The websocket handler responds to details requests", func() {
				err = ws.WriteJSON(jstatusReq{
					Request:  "details",
					RepGroup: "rg1",
					State:    JobStateComplete,
				})
				So(err, ShouldBeNil)

				var status JStatus
				err = ws.ReadJSON(&status)
				So(err, ShouldBeNil)
				So(status.RepGroup, ShouldEqual, "rg1")
				So(status.State, ShouldEqual, JobStateComplete)
				So(status.Cmd, ShouldEqual, "echo 2")

				go func() {
					<-time.After(100 * time.Millisecond)
					ws.WriteJSON(jstatusReq{ //nolint:errcheck
						Request:  "details",
						RepGroup: "rg1",
						State:    JobStateReserved,
					})
				}()

				var status2 JStatus
				err = ws.ReadJSON(&status2)
				So(err, ShouldBeNil)
				So(status2.Cmd, ShouldEqual, "echo 1")
				So(status2.RepGroup, ShouldEqual, "rg1")

			})

			Convey("The websocket handler responds to key requests", func() {
				var jobKey string

				completeJobs, errg := jq.GetByRepGroup("rg1", false, 0, JobStateComplete, false, false)
				So(errg, ShouldBeNil)
				So(len(completeJobs), ShouldEqual, 1)
				jobKey = completeJobs[0].Key()

				err = ws.WriteJSON(jstatusReq{Key: jobKey})
				So(err, ShouldBeNil)

				var status JStatus
				err = ws.ReadJSON(&status)
				So(err, ShouldBeNil)
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

				<-time.After(100 * time.Millisecond) // wait for kick to process

				kickedJobs, errg := jq.GetByRepGroup("rg2", false, 0, JobStateReady, false, false)
				So(errg, ShouldBeNil)
				So(len(kickedJobs), ShouldEqual, 1)
				So(kickedJobs[0].Cmd, ShouldEqual, "echo 4 && false")
			})

			Convey("The websocket handler can remove jobs", func() {
				var removeJobs []*Job
				removeJobs = append(removeJobs, &Job{Cmd: "echo remove", Cwd: "/tmp",
					ReqGroup: "group3", Requirements: standardReqs, RepGroup: "rg3"})
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

				<-time.After(100 * time.Millisecond) // wait for deletion to process

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

				err = ws.WriteJSON(jstatusReq{Request: "current"})
				So(err, ShouldBeNil)
				err = ws2.WriteJSON(jstatusReq{Request: "current"})
				So(err, ShouldBeNil)
				err = ws3.WriteJSON(jstatusReq{Request: "current"})
				So(err, ShouldBeNil)

				var wg sync.WaitGroup

				wg.Add(3)

				r1ch := make(chan jstateCount, 1)
				r2ch := make(chan jstateCount, 1)
				r3ch := make(chan jstateCount, 1)

				go func() {
					defer wg.Done()

					var sc jstateCount

					ws.ReadJSON(&sc)
					r1ch <- sc
				}()

				go func() {
					defer wg.Done()

					var sc jstateCount

					ws2.ReadJSON(&sc)
					r2ch <- sc
				}()

				go func() {
					defer wg.Done()

					var sc jstateCount

					ws3.ReadJSON(&sc)
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

				<-time.After(100 * time.Millisecond)

				ws2.Close()

				err = ws.WriteJSON(jstatusReq{Request: "current"})
				So(err, ShouldBeNil)

				var sc jstateCount

				err = ws.ReadJSON(&sc)
				So(err, ShouldBeNil)

				err = ws3.WriteJSON(jstatusReq{Request: "current"})
				So(err, ShouldBeNil)

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

				err = ws.WriteJSON(jstatusReq{Request: "current"})
				So(err, ShouldBeNil)

				foundMessage := false

				for range 10 {
					var msg schedulerIssue

					errr := ws.ReadJSON(&msg)
					if errr != nil {
						continue
					}

					if msg.Msg == testMsg {
						foundMessage = true

						So(msg.Count, ShouldEqual, 1)
						So(msg.FirstDate, ShouldBeLessThanOrEqualTo, time.Now().Unix())
						So(msg.LastDate, ShouldEqual, msg.FirstDate)

						break
					}
				}

				So(foundMessage, ShouldBeTrue)

				err = ws.WriteJSON(jstatusReq{
					Request: "dismissMsg",
					Msg:     testMsg,
				})
				So(err, ShouldBeNil)

				<-time.After(100 * time.Millisecond)

				server.simutex.RLock()
				_, exists := server.schedIssues[testMsg]
				server.simutex.RUnlock()
				So(exists, ShouldBeFalse)

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

				<-time.After(100 * time.Millisecond)

				server.simutex.RLock()
				count := len(server.schedIssues)
				server.simutex.RUnlock()
				So(count, ShouldEqual, 0)
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

				<-time.After(100 * time.Millisecond)

				err = ws.WriteJSON(jstatusReq{Request: "current"})
				So(err, ShouldBeNil)

				foundBadServer := false

				for range 10 {
					var msg BadServer

					errr := ws.ReadJSON(&msg)
					if errr != nil {
						continue
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

				So(foundBadServer, ShouldBeTrue)

				err = ws.WriteJSON(jstatusReq{
					Request:  "confirmBadServer",
					ServerID: testServer.ID,
				})
				So(err, ShouldBeNil)

				<-time.After(100 * time.Millisecond)
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
