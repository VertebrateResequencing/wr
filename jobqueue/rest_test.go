/*******************************************************************************
 * Copyright (c) 2017-2019, 2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

func TestRESTJobModificationEndpoint(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

	Convey("Once the REST modification endpoint is up", t, func() {
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		handler := restJobs(ctx, server)
		bearer := "Bearer " + string(token)

		const restBulkCmdGroup = "rest-bulk-cmd"

		addJob := func(job *Job) string {
			inserts, already, erra := jq.Add([]*Job{job}, envVars, true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			return job.Key()
		}

		patchJob := func(target, body, authHeader string) (int, string, JobModifyResponse) {
			w := httptest.NewRecorder()

			r := httptest.NewRequestWithContext(ctx, http.MethodPatch, target, strings.NewReader(body))
			if authHeader != "" {
				r.Header.Set("Authorization", authHeader)
			}

			r.Header.Set("Content-Type", "application/json")

			handler(w, r)

			resp := w.Result()
			defer resp.Body.Close()

			responseData, errr := io.ReadAll(resp.Body)
			So(errr, ShouldBeNil)

			var decoded JobModifyResponse
			if resp.StatusCode == http.StatusOK {
				errr = json.Unmarshal(responseData, &decoded)
				So(errr, ShouldBeNil)
			}

			return resp.StatusCode, string(responseData), decoded
		}

		getJobStatuses := func(key string, getEnv bool) []JStatus {
			target := restJobsEndpoint + key
			if getEnv {
				target += "?env=true"
			}

			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			r.Header.Set("Authorization", bearer)

			handler(w, r)

			resp := w.Result()
			defer resp.Body.Close()

			So(resp.StatusCode, ShouldEqual, http.StatusOK)

			var statuses []JStatus

			errr := json.NewDecoder(resp.Body).Decode(&statuses)
			So(errr, ShouldBeNil)

			return statuses
		}

		reserveOnly := func(key string) *Job {
			job, errr := jq.Reserve(50 * time.Millisecond)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Key(), ShouldEqual, key)

			return job
		}

		Convey("PATCH modifies one ready job by key and returns fresh status rows", func() {
			job := &Job{
				Cmd:          "echo rest old",
				Cwd:          testCwd,
				ReqGroup:     "rest-old",
				Requirements: &jqs.Requirements{RAM: 50, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
				Priority:     1,
				Retries:      1,
				Override:     0,
				LimitGroups:  []string{"old:1"},
				RepGroup:     "rest-a1-single",
			}
			err = job.EnvAddOverride([]string{"REST_MOD=old"})
			So(err, ShouldBeNil)

			oldKey := addJob(job)
			body := `{
				"cmd": "echo rest new",
				"cwd": "/tmp/rest-new",
				"cwd_matters": true,
				"change_home": true,
				"req_grp": "rest-new",
				"memory": "64M",
				"time": "2m",
				"cpus": 0.5,
				"disk": 2,
				"priority": 7,
				"retries": 4,
				"override": 2,
				"limit_grps": ["new:2"],
				"modules": ["module-a"],
				"env": ["REST_MOD=new", "REST_EXTRA=1"],
				"other": {"scheduler_queue": "short"},
				"on_exit": [{"nothing": true}]
			}`

			status, _, decoded := patchJob(restJobsEndpoint+oldKey, body, bearer)
			So(status, ShouldEqual, http.StatusOK)
			So(len(decoded.Modified), ShouldEqual, 1)
			So(len(decoded.Jobs), ShouldEqual, 1)

			newKey := decoded.Jobs[0].Key
			So(newKey, ShouldNotEqual, "")
			So(newKey, ShouldNotEqual, oldKey)
			So(decoded.Modified[newKey], ShouldEqual, oldKey)

			stored := getJobStatuses(newKey, true)
			So(len(stored), ShouldEqual, 1)
			So(stored[0].Cmd, ShouldEqual, "echo rest new")
			So(stored[0].CwdBase, ShouldEqual, "/tmp/rest-new")
			So(stored[0].ReqGroup, ShouldEqual, "rest-new")
			So(stored[0].CwdMatters, ShouldBeTrue)
			So(stored[0].HomeChanged, ShouldBeTrue)
			So(stored[0].ExpectedRAM, ShouldEqual, 64)
			So(stored[0].ExpectedTime, ShouldEqual, 120)
			So(stored[0].Cores, ShouldEqual, 0.5)
			So(stored[0].RequestedDisk, ShouldEqual, 2)
			So(stored[0].Priority, ShouldEqual, 7)
			So(stored[0].Retries, ShouldEqual, 4)
			So(stored[0].Override, ShouldEqual, 2)
			So(stored[0].LimitGroups, ShouldResemble, []string{"new:2"})
			So(stored[0].Modules, ShouldResemble, []string{"module-a"})
			So(stored[0].Env, ShouldContain, "REST_MOD=new")
			So(stored[0].Env, ShouldContain, "REST_EXTRA=1")
			So(stored[0].OtherRequests, ShouldContain, "scheduler_queue:short")
			So(stored[0].Behaviours, ShouldEqual, `{"on_exit":[{"nothing":true}]}`)

			storedWithoutEnv := getJobStatuses(newKey, false)
			So(len(storedWithoutEnv), ShouldEqual, 1)
			So(decoded.Jobs[0], ShouldResemble, storedWithoutEnv[0])

			oldStatuses := getJobStatuses(oldKey, false)
			So(len(oldStatuses), ShouldEqual, 0)
		})

		Convey("PATCH accepts the token query parameter without bearer auth", func() {
			key := addJob(&Job{
				Cmd:          "echo rest token auth",
				Cwd:          testCwd,
				ReqGroup:     "rest-token",
				Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
				Priority:     1,
				RepGroup:     "rest-a1-token",
			})

			status, _, decoded := patchJob(restJobsEndpoint+key+"?token="+string(token), `{"priority":9}`, "")
			So(status, ShouldEqual, http.StatusOK)
			So(len(decoded.Jobs), ShouldEqual, 1)
			So(decoded.Jobs[0].Priority, ShouldEqual, 9)
			So(decoded.Modified[decoded.Jobs[0].Key], ShouldEqual, key)

			stored := getJobStatuses(key, false)
			So(len(stored), ShouldEqual, 1)
			So(stored[0].Priority, ShouldEqual, 9)
		})

		Convey("PATCH accepts public command dependency JSON", func() {
			key := addJob(&Job{
				Cmd:          "echo rest cmd deps",
				Cwd:          testCwd,
				ReqGroup:     "rest-cmd-deps",
				Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
				RepGroup:     "rest-a1-cmd-deps",
			})

			body := `{"deps":["dep-a"],"cmd_deps":[{"cmd":"echo dep","cwd":"/tmp/dep"}]}`
			status, _, decoded := patchJob(restJobsEndpoint+key, body, bearer)
			So(status, ShouldEqual, http.StatusOK)
			So(len(decoded.Jobs), ShouldEqual, 1)
			So(decoded.Jobs[0].Dependencies, ShouldContain, "dep-a")
			So(decoded.Jobs[0].Dependencies, ShouldContain, "echo dep [/tmp/dep]")

			stored := getJobStatuses(key, false)
			So(len(stored), ShouldEqual, 1)
			So(stored[0].Dependencies, ShouldContain, "dep-a")
			So(stored[0].Dependencies, ShouldContain, "echo dep [/tmp/dep]")
		})

		Convey("PATCH rejects requests without token or bearer auth", func() {
			key := addJob(&Job{
				Cmd:          "echo rest no auth",
				Cwd:          testCwd,
				ReqGroup:     "rest-no-auth",
				Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
				RepGroup:     "rest-a1-no-auth",
			})

			status, _, _ := patchJob(restJobsEndpoint+key, `{"priority":9}`, "")
			So(status, ShouldEqual, http.StatusUnauthorized)
		})

		Convey("PATCH modifies multiple editable RepGroup jobs and leaves running jobs unchanged", func() {
			runningKey := addJob(&Job{
				Cmd:          "echo rest bulk running",
				Cwd:          testCwd,
				ReqGroup:     "rest-bulk-running",
				Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
				Priority:     3,
				LimitGroups:  []string{"running:1"},
				RepGroup:     "rest-bulk",
			})
			runningJob := reserveOnly(runningKey)
			err = jq.Started(runningJob, os.Getpid())
			So(err, ShouldBeNil)

			for i := range 3 {
				addJob(&Job{
					Cmd:          fmt.Sprintf("echo rest bulk %d", i),
					Cwd:          testCwd,
					ReqGroup:     "rest-bulk-editable",
					Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
					Priority:     1,
					LimitGroups:  []string{"oldbulk:2"},
					RepGroup:     "rest-bulk",
				})
			}

			status, _, decoded := patchJob(restJobsEndpoint+"rest-bulk", `{"priority":8,"limit_grps":["bulk:1"]}`, bearer)
			So(status, ShouldEqual, http.StatusOK)
			So(len(decoded.Modified), ShouldEqual, 3)
			So(len(decoded.Jobs), ShouldEqual, 3)

			for _, job := range decoded.Jobs {
				So(job.Priority, ShouldEqual, 8)
				So(job.LimitGroups, ShouldResemble, []string{"bulk:1"})
				So(decoded.Modified[job.Key], ShouldEqual, job.Key)
				So(job.Key, ShouldNotEqual, runningKey)
			}

			runningStatuses := getJobStatuses(runningKey, false)
			So(len(runningStatuses), ShouldEqual, 1)
			So(runningStatuses[0].State, ShouldEqual, JobStateRunning)
			So(runningStatuses[0].Priority, ShouldEqual, 3)
			So(runningStatuses[0].LimitGroups, ShouldResemble, []string{"running:1"})
		})

		Convey("PATCH rejects command changes when a RepGroup matches multiple editable jobs", func() {
			keyA := addJob(&Job{
				Cmd:          "echo rest bulk cmd a",
				Cwd:          testCwd,
				ReqGroup:     restBulkCmdGroup,
				Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
				RepGroup:     restBulkCmdGroup,
			})
			keyB := addJob(&Job{
				Cmd:          "echo rest bulk cmd b",
				Cwd:          testCwd,
				ReqGroup:     restBulkCmdGroup,
				Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 1, Disk: 0, Other: make(map[string]string)},
				RepGroup:     restBulkCmdGroup,
			})

			status, body, _ := patchJob(restJobsEndpoint+restBulkCmdGroup, `{"cmd":"echo same"}`, bearer)
			So(status, ShouldEqual, http.StatusBadRequest)
			So(body, ShouldEqual, "cmd can only be modified for one job\n")

			storedA := getJobStatuses(keyA, false)
			So(len(storedA), ShouldEqual, 1)
			So(storedA[0].Cmd, ShouldEqual, "echo rest bulk cmd a")

			storedB := getJobStatuses(keyB, false)
			So(len(storedB), ShouldEqual, 1)
			So(storedB[0].Cmd, ShouldEqual, "echo rest bulk cmd b")
		})
	})
}

func TestRESTJobModificationValidation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	const restA2ReqGroup = "rest-a2"

	Convey("Once the REST modification server is up", t, func() {
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		handler := restJobs(ctx, server)
		bearer := "Bearer " + string(token)

		addJob := func(job *Job) string {
			inserts, already, erra := jq.Add([]*Job{job}, envVars, true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			return job.Key()
		}

		patchJob := func(id, body string) (int, string, JobModifyResponse) {
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(ctx, http.MethodPatch, restJobsEndpoint+id, strings.NewReader(body))
			r.Header.Set("Authorization", bearer)
			r.Header.Set("Content-Type", "application/json")

			handler(w, r)

			resp := w.Result()
			defer resp.Body.Close()

			responseData, errr := io.ReadAll(resp.Body)
			So(errr, ShouldBeNil)

			var decoded JobModifyResponse
			if resp.StatusCode == http.StatusOK {
				errr = json.Unmarshal(responseData, &decoded)
				So(errr, ShouldBeNil)
			}

			return resp.StatusCode, string(responseData), decoded
		}

		getJobStatus := func(key string, getEnv bool) JStatus {
			target := restJobsEndpoint + key
			if getEnv {
				target += "?env=true"
			}

			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			r.Header.Set("Authorization", bearer)

			handler(w, r)

			resp := w.Result()
			defer resp.Body.Close()

			So(resp.StatusCode, ShouldEqual, http.StatusOK)

			var statuses []JStatus

			errr := json.NewDecoder(resp.Body).Decode(&statuses)
			So(errr, ShouldBeNil)
			So(len(statuses), ShouldEqual, 1)

			return statuses[0]
		}

		reserveOnly := func(key string) *Job {
			job, errr := jq.Reserve(50 * time.Millisecond)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Key(), ShouldEqual, key)

			return job
		}

		Convey("PATCH modifies delayed jobs and preserves their state", func() {
			key := addJob(&Job{
				Cmd: "echo rest delayed", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-delayed", Priority: 1, Retries: 3,
			})
			job := reserveOnly(key)
			err = jq.Release(job, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, FailReasonExit)
			So(err, ShouldBeNil)
			So(waitUntilJobState(jq, &JobEssence{JobKey: key}, JobStateDelayed, 5).State, ShouldEqual, JobStateDelayed)

			status, _, decoded := patchJob(key, `{"priority":9}`)
			So(status, ShouldEqual, http.StatusOK)
			So(len(decoded.Jobs), ShouldEqual, 1)
			So(decoded.Jobs[0].State, ShouldEqual, JobStateDelayed)
			So(decoded.Jobs[0].Priority, ShouldEqual, 9)
			So(getJobStatus(key, false).State, ShouldEqual, JobStateDelayed)
			So(getJobStatus(key, false).Priority, ShouldEqual, 9)
			time.Sleep(50 * time.Millisecond)
			So(getJobStatus(key, false).State, ShouldEqual, JobStateDelayed)
		})

		Convey("PATCH modifies dependent jobs and preserves their state", func() {
			addJob(&Job{
				Cmd: "echo rest dep parent", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-dep-parent", DepGroups: []string{"rest-a2-parent"},
			})
			key := addJob(&Job{
				Cmd: "echo rest dep child", Cwd: testCwd, ReqGroup: "dep-old",
				Requirements: standardReqs, RepGroup: "rest-a2-dependent",
				Dependencies: Dependencies{NewDepGroupDependency("rest-a2-parent")},
			})
			So(getJobStatus(key, false).State, ShouldEqual, JobStateDependent)

			status, _, decoded := patchJob(key, `{"req_grp":"dep-new"}`)
			So(status, ShouldEqual, http.StatusOK)
			So(len(decoded.Jobs), ShouldEqual, 1)
			So(decoded.Jobs[0].State, ShouldEqual, JobStateDependent)
			So(decoded.Jobs[0].ReqGroup, ShouldEqual, "dep-new")

			stored := getJobStatus(key, false)
			So(stored.State, ShouldEqual, JobStateDependent)
			So(stored.ReqGroup, ShouldEqual, "dep-new")
		})

		Convey("PATCH modifies buried jobs and preserves their state", func() {
			key := addJob(&Job{
				Cmd: "echo rest buried", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-buried", Retries: 1,
			})
			job := reserveOnly(key)
			err = jq.Bury(job, nil, "rest buried")
			So(err, ShouldBeNil)
			So(waitUntilJobState(jq, &JobEssence{JobKey: key}, JobStateBuried, 5).State, ShouldEqual, JobStateBuried)

			status, _, decoded := patchJob(key, `{"retries":3}`)
			So(status, ShouldEqual, http.StatusOK)
			So(len(decoded.Jobs), ShouldEqual, 1)
			So(decoded.Jobs[0].State, ShouldEqual, JobStateBuried)
			So(decoded.Jobs[0].Retries, ShouldEqual, 3)
			So(getJobStatus(key, false).State, ShouldEqual, JobStateBuried)
			So(getJobStatus(key, false).Retries, ShouldEqual, 3)
		})

		Convey("PATCH rejects priority values outside 0..255", func() {
			key := addJob(&Job{
				Cmd: "echo rest bad priority", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-bad-priority", Priority: 1,
			})

			status, body, _ := patchJob(key, `{"priority":256}`)
			So(status, ShouldEqual, http.StatusBadRequest)
			So(body, ShouldContainSubstring, "priority value (256) is not in the range 0..255")
			So(getJobStatus(key, false).Priority, ShouldEqual, 1)
		})

		Convey("PATCH rejects an empty command", func() {
			key := addJob(&Job{
				Cmd: "echo rest empty cmd", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-empty-cmd",
			})

			status, body, _ := patchJob(key, `{"cmd":""}`)
			So(status, ShouldEqual, http.StatusBadRequest)
			So(body, ShouldEqual, "cmd cannot be empty\n")
			So(getJobStatus(key, false).Cmd, ShouldEqual, "echo rest empty cmd")
		})

		Convey("PATCH reports no-retry walltime parse errors with the field name", func() {
			key := addJob(&Job{
				Cmd: "echo rest bad no retry", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-bad-no-retry",
			})

			status, body, _ := patchJob(key, `{"no_retry_over_walltime":"notaduration"}`)
			So(status, ShouldEqual, http.StatusBadRequest)
			So(body, ShouldContainSubstring, "no_retry_over_walltime value (notaduration) was not specified correctly")
		})

		Convey("PATCH rejects running jobs without changing them", func() {
			key := addJob(&Job{
				Cmd: "echo rest running", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-running", Priority: 1,
			})
			job := reserveOnly(key)
			err = jq.Started(job, os.Getpid())
			So(err, ShouldBeNil)
			So(getJobStatus(key, false).State, ShouldEqual, JobStateRunning)

			status, body, _ := patchJob(key, `{"priority":9}`)
			So(status, ShouldEqual, http.StatusConflict)
			So(body, ShouldEqual, "no editable jobs matched\n")
			So(getJobStatus(key, false).Priority, ShouldEqual, 1)
		})

		Convey("PATCH rejects complete jobs without creating ready jobs", func() {
			key := addJob(&Job{
				Cmd: "echo rest complete", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-complete", Retries: 1,
			})
			job := reserveOnly(key)
			err = jq.Started(job, os.Getpid())
			So(err, ShouldBeNil)
			err = jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			So(err, ShouldBeNil)
			So(getJobStatus(key, false).State, ShouldEqual, JobStateComplete)

			status, body, _ := patchJob(key, `{"retries":9}`)
			So(status, ShouldEqual, http.StatusConflict)
			So(body, ShouldEqual, "no editable jobs matched\n")

			jobs, errg := jq.GetByRepGroup("rest-a2-complete", false, 0, JobStateReady, false, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 0)
		})

		Convey("PATCH rejects reserved jobs without changing them", func() {
			key := addJob(&Job{
				Cmd: "echo rest reserved", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-reserved", Priority: 1,
			})
			reserveOnly(key)
			So(getJobStatus(key, false).State, ShouldEqual, JobStateReserved)

			status, body, _ := patchJob(key, `{"priority":9}`)
			So(status, ShouldEqual, http.StatusConflict)
			So(body, ShouldEqual, "no editable jobs matched\n")
			So(getJobStatus(key, false).Priority, ShouldEqual, 1)
		})

		Convey("PATCH reports no editable jobs when queue state changes before modification", func() {
			key := addJob(&Job{
				Cmd: "echo rest stale editable", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-stale-editable", Priority: 1,
			})
			reserveOnly(key)

			modifier := NewJobModifer()
			modifier.SetPriority(9)

			modified, errm := server.modifyJobsByKeys(ctx, []string{key}, modifier)
			So(errm, ShouldBeNil)
			So(len(modified), ShouldEqual, 0)
			So(server.restModifyEmptyResultError([]string{key}), ShouldEqual, errRESTModifyNoEditable)
		})

		Convey("PATCH rejects lost jobs without changing them", func() {
			key := addJob(&Job{
				Cmd: "echo rest lost", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-lost", Priority: 1,
			})
			job := reserveOnly(key)
			err = jq.Started(job, os.Getpid())
			So(err, ShouldBeNil)

			item, errq := server.q.Get(key)
			So(errq, ShouldBeNil)

			serverJob, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)
			serverJob.Lock()
			serverJob.Lost = true
			serverJob.Unlock()
			So(getJobStatus(key, false).State, ShouldEqual, JobStateLost)

			status, body, _ := patchJob(key, `{"priority":9}`)
			So(status, ShouldEqual, http.StatusConflict)
			So(body, ShouldEqual, "no editable jobs matched\n")
			So(getJobStatus(key, false).Priority, ShouldEqual, 1)
		})

		Convey("PATCH returns not found for unknown jobs", func() {
			status, body, _ := patchJob("0123456789abcdef0123456789abcdef", `{"priority":9}`)
			So(status, ShouldEqual, http.StatusNotFound)
			So(body, ShouldEqual, "job not found\n")

			jobs, errg := jq.GetByRepGroup("0123456789abcdef0123456789abcdef", false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 0)
		})

		Convey("PATCH clears job-specific env overrides", func() {
			job := &Job{
				Cmd: "echo rest clear env", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-clear-env",
			}
			err = job.EnvAddOverride([]string{"REST_CLEAR=1"})
			So(err, ShouldBeNil)

			key := addJob(job)

			status, _, _ := patchJob(key, `{"env":[]}`)
			So(status, ShouldEqual, http.StatusOK)

			stored := getJobStatus(key, true)
			So(stored.Env, ShouldNotContain, "REST_CLEAR=1")
			So(stored.EnvOverrides, ShouldBeEmpty)
		})

		Convey("PATCH reports duplicate-key command edits without changing either job", func() {
			keyA := addJob(&Job{
				Cmd: "echo rest duplicate a", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-duplicate-a",
			})
			keyB := addJob(&Job{
				Cmd: "echo rest duplicate b", Cwd: testCwd, ReqGroup: restA2ReqGroup,
				Requirements: standardReqs, RepGroup: "rest-a2-duplicate-b",
			})

			status, body, _ := patchJob(keyA, `{"cmd":"echo rest duplicate b"}`)
			So(status, ShouldEqual, http.StatusConflict)
			So(body, ShouldEqual, "no jobs were modified\n")
			So(getJobStatus(keyA, false).Cmd, ShouldEqual, "echo rest duplicate a")
			So(getJobStatus(keyB, false).Cmd, ShouldEqual, "echo rest duplicate b")
		})
	})
}

func TestRESTWaitingDepGroups(t *testing.T) {
	ctx := context.Background()

	if runnermode {
		return
	}

	Convey("REST status exposes and filters never-seen dependency group waits", t, func() {
		config, serverConfig, addr, reqs, clientConnectTime := jobqueueTestInit(true)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		waiting := &Job{
			Cmd:          "echo rest waiting dep",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			RepGroup:     "rest-waiting",
			DepGroups:    []string{testCarrierDepGroup},
			Dependencies: Dependencies{NewDepGroupDependency(futureDepGroup)},
		}
		liveDependent := &Job{
			Cmd:          "echo rest live dependent",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			RepGroup:     "rest-live",
			Dependencies: Dependencies{NewDepGroupDependency(testLiveDepGroup)},
		}
		liveCarrier := &Job{
			Cmd:          "echo rest live carrier",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			RepGroup:     "rest-live",
			DepGroups:    []string{testLiveDepGroup},
		}

		inserts, already, err := jq.Add([]*Job{waiting, liveDependent, liveCarrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 3)
		So(already, ShouldEqual, 0)

		client := newRESTTestHTTPClient(t, config.ManagerCAFile, config.ManagerCertDomain)
		bearer := "Bearer " + string(token)
		jobsEndpoint := "https://" + config.ManagerCertDomain + ":" + config.ManagerWeb + "/rest/v1/jobs"

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, jobsEndpoint+"/"+waiting.RepGroup, nil)
		So(err, ShouldBeNil)
		req.Header.Add("Authorization", bearer)

		resp, err := client.Do(req)
		So(err, ShouldBeNil)

		defer resp.Body.Close()

		So(resp.StatusCode, ShouldEqual, http.StatusOK)

		responseData, err := io.ReadAll(resp.Body)
		So(err, ShouldBeNil)

		var rawStatuses []map[string]json.RawMessage

		err = json.Unmarshal(responseData, &rawStatuses)
		So(err, ShouldBeNil)
		So(rawStatuses, ShouldHaveLength, 1)

		var state string
		So(json.Unmarshal(rawStatuses[0]["State"], &state), ShouldBeNil)
		So(state, ShouldEqual, string(JobStateDependent))

		var depGroups []string
		So(json.Unmarshal(rawStatuses[0]["DepGroups"], &depGroups), ShouldBeNil)
		So(depGroups, ShouldResemble, []string{testCarrierDepGroup})

		var waitingGroups []string
		So(json.Unmarshal(rawStatuses[0]["WaitingForDepGroups"], &waitingGroups), ShouldBeNil)
		So(waitingGroups, ShouldResemble, []string{futureDepGroup})

		_, hasLowerState := rawStatuses[0]["state"]
		_, hasSnakeDepGroups := rawStatuses[0]["dep_groups"]
		_, hasSnakeWaitingGroups := rawStatuses[0]["waiting_for_dep_groups"]

		So(hasLowerState, ShouldBeFalse)
		So(hasSnakeDepGroups, ShouldBeFalse)
		So(hasSnakeWaitingGroups, ShouldBeFalse)

		req, err = http.NewRequestWithContext(ctx, http.MethodGet, jobsEndpoint+"?waiting_deps=true", nil)
		So(err, ShouldBeNil)
		req.Header.Add("Authorization", bearer)

		filterResp, err := client.Do(req)
		So(err, ShouldBeNil)

		defer filterResp.Body.Close()

		So(filterResp.StatusCode, ShouldEqual, http.StatusOK)

		filteredData, err := io.ReadAll(filterResp.Body)
		So(err, ShouldBeNil)

		var filtered []JStatus

		err = json.Unmarshal(filteredData, &filtered)
		So(err, ShouldBeNil)
		So(filtered, ShouldHaveLength, 1)
		So(filtered[0].Key, ShouldEqual, waiting.Key())
		So(filtered[0].WaitingForDepGroups, ShouldResemble, []string{futureDepGroup})
	})
}

func newRESTTestHTTPClient(t *testing.T, caFile, serverName string) *http.Client {
	t.Helper()

	tlsConfig := &tls.Config{ServerName: serverName}
	caCert, err := os.ReadFile(caFile)
	So(err, ShouldBeNil)

	certPool := x509.NewCertPool()
	So(certPool.AppendCertsFromPEM(caCert), ShouldBeTrue)
	tlsConfig.RootCAs = certPool

	return &http.Client{Transport: &http.Transport{
		Proxy:           nil,
		TLSClientConfig: tlsConfig,
	}}
}

// waitForRESTJobState polls the REST job endpoint at url until it returns
// exactly one job in the wanted state (or pollUntil's deadline elapses),
// returning the last-decoded statuses and whether the state was reached. It
// lets the buried/lost checks wait for the server's view to settle instead of
// sleeping a fixed time that races the server under load.
func waitForRESTJobState(ctx context.Context, httpClient *http.Client, url, bearer string,
	wanted JobState,
) ([]JStatus, bool) {
	const pollAttemptTimeout = 5 * time.Second

	var jstati []JStatus

	ok := pollUntil(func() bool {
		attemptCtx, cancel := context.WithTimeout(ctx, pollAttemptTimeout)
		defer cancel()

		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}

		req.Header.Add("Authorization", bearer)

		resp, err := httpClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return false
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}

		jstati = nil

		return json.Unmarshal(body, &jstati) == nil && len(jstati) == 1 && jstati[0].State == wanted
	})

	return jstati, ok
}

func TestREST(t *testing.T) {
	ctx := context.Background()

	if runnermode {
		return
	}

	testLogger := log15.New()
	testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.StderrHandler))

	dir, errt := os.MkdirTemp("", "wr_rest_tests")
	if errt != nil {
		log.Fatalf("could not create tempdir: %s\n", errt)
	}
	defer os.RemoveAll(dir)
	uploadsDir := filepath.Join(dir, "uploads")

	// load our config to know where our development manager port is supposed to
	// be; we'll use that to test jobqueue
	config := internal.ConfigLoadFromParentDir(ctx, internal.Development)
	isolateTestConfig(config)
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   "local",
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		UploadDir:       uploadsDir,
		DBFile:          config.ManagerDBFile,
		DBFileBackup:    config.ManagerDBFile + "_bk",
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
		Logger:          testLogger,
	}
	addr := "localhost:" + config.ManagerPort
	baseURL := "https://" + config.ManagerCertDomain + ":" + config.ManagerWeb
	jobsEndPoint := baseURL + "/rest/v1/jobs"
	uploadEndPoint := baseURL + "/rest/v1/upload"
	warningsEndPoint := baseURL + "/rest/v1/warnings/"
	serversEndPoint := baseURL + "/rest/v1/servers/"

	setDomainIP(config.ManagerCertDomain)

	serverConfig.Timings.InterruptTime = 10 * time.Millisecond
	serverConfig.Timings.ReleaseDelayMin = 100 * time.Millisecond
	serverConfig.Timings.ItemTTR = 200 * time.Millisecond
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond
	clientConnectTime := 1500 * time.Millisecond

	var server *Server
	var token []byte
	Convey("Once the jobqueue server is up", t, func() {
		server, _, token, errt = Serve(ctx, serverConfig)
		So(errt, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)
		defer disconnect(jq)

		bearer := "Bearer " + string(token)

		tlsConfig := &tls.Config{ServerName: config.ManagerCertDomain}
		caCert, errr := os.ReadFile(config.ManagerCAFile)
		if errr == nil {
			certPool := x509.NewCertPool()
			certPool.AppendCertsFromPEM(caCert)
			tlsConfig.RootCAs = certPool
		}
		var noProxyTransport http.RoundTripper = &http.Transport{
			Proxy:           nil,
			TLSClientConfig: tlsConfig,
		}

		client := &http.Client{Transport: noProxyTransport}

		Convey("You must be authorised to access all the endpoints", func() {
			req, err := http.NewRequest(http.MethodGet, jobsEndPoint, nil)
			So(err, ShouldBeNil)
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			So(response.StatusCode, ShouldEqual, http.StatusUnauthorized)

			req, err = http.NewRequest(http.MethodGet, warningsEndPoint, nil)
			So(err, ShouldBeNil)
			response, err = client.Do(req)
			So(err, ShouldBeNil)
			So(response.StatusCode, ShouldEqual, http.StatusUnauthorized)

			req, err = http.NewRequest(http.MethodGet, serversEndPoint, nil)
			So(err, ShouldBeNil)
			response, err = client.Do(req)
			So(err, ShouldBeNil)
			So(response.StatusCode, ShouldEqual, http.StatusUnauthorized)
		})

		Convey("Initial GET queries return nothing", func() {
			req, err := http.NewRequest(http.MethodGet, jobsEndPoint, nil)
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			responseData, err := io.ReadAll(response.Body)
			So(err, ShouldBeNil)

			var jstati []JStatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 0)
		})

		Convey("You can POST to add jobs to the queue", func() {
			var inputJobs []*JobViaJSON
			pri := 2
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 1 && true", RepGrp: "rp1", Retries: &pri, NoRetriesOverWalltime: "5m"})
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 2 && true", RepGrp: "rp2", Cwd: "/tmp/foo"})
			cpus := float64(2)
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 3 && false", CwdMatters: true, RepGrp: "rp1", Memory: "50M", CPUs: &cpus, Time: "2m", Priority: &pri, Env: []string{"foo=bar", "test=case"}})
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)

			req, err := http.NewRequest(http.MethodPost, jobsEndPoint+"/", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			req.Header.Add("Content-Type", "application/json")
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			responseData, err := io.ReadAll(response.Body)
			So(err, ShouldBeNil)
			var jstati []JStatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 3)

			So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
			So(jstati[0].State, ShouldEqual, JobStateReady)
			So(jstati[0].CwdBase, ShouldEqual, "/tmp")
			So(jstati[0].RepGroup, ShouldEqual, "rp1")
			So(jstati[0].ExpectedRAM, ShouldEqual, 1000)
			So(jstati[0].ExpectedTime, ShouldEqual, 3600)
			So(jstati[0].Cores, ShouldEqual, 0)
			So(jstati[1].Key, ShouldEqual, "f5c0d6240167a6e0b803e23f74e3a085")
			So(jstati[1].RepGroup, ShouldEqual, "rp2")
			So(jstati[1].CwdBase, ShouldEqual, "/tmp/foo")
			So(jstati[2].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
			So(jstati[2].State, ShouldEqual, JobStateReady)
			So(jstati[2].CwdBase, ShouldEqual, "/tmp")
			So(jstati[2].RepGroup, ShouldEqual, "rp1")
			So(jstati[2].ExpectedRAM, ShouldEqual, 50)
			So(jstati[2].ExpectedTime, ShouldEqual, 120)
			So(jstati[2].Cores, ShouldEqual, 2)
			So(jstati[2].Started, ShouldBeNil)
			So(jstati[2].Ended, ShouldBeNil)

			job, err := jq.GetByEssence(&JobEssence{Cmd: "echo 1 && true"}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Retries, ShouldEqual, 2)
			So(job.NoRetriesOverWalltime, ShouldEqual, 5*time.Minute)
			job, err = jq.GetByEssence(&JobEssence{Cmd: "echo 3 && false", Cwd: "/tmp"}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Retries, ShouldEqual, 0)
			So(job.NoRetriesOverWalltime, ShouldEqual, 0)

			Convey("You can GET the current status of all jobs", func() {
				req, err := http.NewRequest(http.MethodGet, jobsEndPoint, nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				responseData, err = io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []JStatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 3)
			})

			Convey("You can GET the status of particular jobs using their ids", func() {
				req, err := http.NewRequest(http.MethodGet, jobsEndPoint+"/de6d167c58701e55f5b9f9e1e91d7807", nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				responseData, err = io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []JStatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 1)
				So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")

				req, err = http.NewRequest(http.MethodGet, jobsEndPoint+"/de6d167c58701e55f5b9f9e1e91d7807,db1e7d99becace3306c1c2470331c78e", nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				responseData, err = io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati2 []JStatus
				err = json.Unmarshal(responseData, &jstati2)
				So(err, ShouldBeNil)
				So(len(jstati2), ShouldEqual, 2)
				So(jstati2[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
				So(jstati2[1].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
			})

			Convey("You can GET the status of jobs by RepGroup", func() {
				req, err := http.NewRequest(http.MethodGet, jobsEndPoint+"/rp1", nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				responseData, err = io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []JStatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 2)
				keys := make(map[string]bool)
				for _, j := range jstati {
					keys[j.Key] = true
				}
				So(keys, ShouldResemble, map[string]bool{"de6d167c58701e55f5b9f9e1e91d7807": true, "db1e7d99becace3306c1c2470331c78e": true})

				Convey("And you can modify the results by changing limit", func() {
					req, err := http.NewRequest(http.MethodGet, jobsEndPoint+"/rp1?limit=1", nil)
					So(err, ShouldBeNil)
					req.Header.Add("Authorization", bearer)
					response, err = client.Do(req)
					So(err, ShouldBeNil)
					responseData, err = io.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati []JStatus
					err = json.Unmarshal(responseData, &jstati)
					So(err, ShouldBeNil)
					So(len(jstati), ShouldEqual, 1)
					So(jstati[0].Similar, ShouldEqual, 1)
				})
			})

			Convey("You can DELETE jobs by RepGroup", func() {
				req, err := http.NewRequest(http.MethodDelete, jobsEndPoint+"/rp1", nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				responseData, err = io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				So(response.Status, ShouldEqual, "400 Bad Request")
				So(string(responseData), ShouldEqual, "state must be supplied as one of running|lost|deletable\n")

				req, err = http.NewRequest(http.MethodDelete, jobsEndPoint+"/rp1?state=deletable", nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				responseData, err = io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []JStatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 2)
				keys := make(map[string]bool)
				for _, j := range jstati {
					keys[j.Key] = true
					So(j.State, ShouldEqual, JobStateDeleted)
				}
				So(keys, ShouldResemble, map[string]bool{"de6d167c58701e55f5b9f9e1e91d7807": true, "db1e7d99becace3306c1c2470331c78e": true})
			})

			Convey("Once one of the jobs has changed state", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "echo 3 && false")
				So(job.State, ShouldEqual, JobStateReserved)
				So(job.Attempts, ShouldEqual, 0)
				So(job.UntilBuried, ShouldEqual, 1)
				env, err := job.Env()
				So(err, ShouldBeNil)
				So(env, ShouldContain, "foo=bar")
				So(env, ShouldContain, "test=case")

				Convey("You can DELETE running jobs to bury them", func() {
					err = jq.Started(job, 1)
					So(err, ShouldBeNil)

					req, err = http.NewRequest(http.MethodGet, jobsEndPoint+"/db1e7d99becace3306c1c2470331c78e", nil)
					So(err, ShouldBeNil)
					req.Header.Add("Authorization", bearer)
					response, err = client.Do(req)
					So(err, ShouldBeNil)
					responseData, err = io.ReadAll(response.Body)
					So(err, ShouldBeNil)

					jstati = []JStatus{}
					err = json.Unmarshal(responseData, &jstati)
					So(err, ShouldBeNil)
					So(len(jstati), ShouldEqual, 1)
					So(jstati[0].Started, ShouldNotBeNil)
					So(jstati[0].Ended, ShouldBeNil)

					req, errr := http.NewRequest(http.MethodDelete, jobsEndPoint+"/rp1?state=running", nil)
					So(errr, ShouldBeNil)
					req.Header.Add("Authorization", bearer)
					response, err = client.Do(req)
					So(err, ShouldBeNil)
					responseData, err = io.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati []JStatus
					err = json.Unmarshal(responseData, &jstati)
					So(err, ShouldBeNil)
					So(len(jstati), ShouldEqual, 1)
					So(jstati[0].State, ShouldEqual, JobStateRunning)

					buried, ok := waitForRESTJobState(ctx, client,
						jobsEndPoint+"/db1e7d99becace3306c1c2470331c78e", bearer, JobStateBuried)
					So(ok, ShouldBeTrue)
					So(len(buried), ShouldEqual, 1)
					So(buried[0].State, ShouldEqual, JobStateBuried)
					So(buried[0].Started, ShouldNotBeNil)
					So(buried[0].Ended, ShouldBeNil)
				})

				Convey("You can DELETE lost jobs to bury them", func() {
					err = jq.Started(job, 1)
					So(err, ShouldBeNil)

					// the job is never touched, so it becomes lost once its short
					// TTR expires; poll for that state (a read-only GET) rather
					// than waiting a fixed margin over the TTR, which races the
					// server's timer under load. The DELETE below is then issued
					// exactly once, against a confirmed-lost job.
					_, lostOK := waitForRESTJobState(ctx, client,
						jobsEndPoint+"/db1e7d99becace3306c1c2470331c78e", bearer, JobStateLost)
					So(lostOK, ShouldBeTrue)

					req, errr := http.NewRequest(http.MethodDelete, jobsEndPoint+"/rp1?state=lost", nil)
					So(errr, ShouldBeNil)
					req.Header.Add("Authorization", bearer)
					response, err = client.Do(req)
					So(err, ShouldBeNil)
					responseData, err = io.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati []JStatus
					err = json.Unmarshal(responseData, &jstati)
					So(err, ShouldBeNil)
					So(len(jstati), ShouldEqual, 1)
					So(jstati[0].State, ShouldEqual, JobStateLost)

					buried, buriedOK := waitForRESTJobState(ctx, client,
						jobsEndPoint+"/db1e7d99becace3306c1c2470331c78e", bearer, JobStateBuried)
					So(buriedOK, ShouldBeTrue)
					So(len(buried), ShouldEqual, 1)
					So(buried[0].State, ShouldEqual, JobStateBuried)
				})

				Convey("Once executed...", func() {
					t := time.Now()
					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)

					Convey("You can GET all jobs by state, and get their stdout/err", func() {
						req, err := http.NewRequest(http.MethodGet, jobsEndPoint+"/?state=ready", nil)
						So(err, ShouldBeNil)
						req.Header.Add("Authorization", bearer)
						response, err := client.Do(req)
						So(err, ShouldBeNil)
						responseData, err := io.ReadAll(response.Body)
						So(err, ShouldBeNil)

						var jstati []JStatus
						err = json.Unmarshal(responseData, &jstati)
						So(err, ShouldBeNil)
						So(len(jstati), ShouldEqual, 2)
						keys := make(map[string]bool)
						for _, j := range jstati {
							keys[j.Key] = true
						}
						So(keys, ShouldResemble, map[string]bool{"de6d167c58701e55f5b9f9e1e91d7807": true, "f5c0d6240167a6e0b803e23f74e3a085": true})

						req, err = http.NewRequest(http.MethodGet, jobsEndPoint+"/?state=buried&std=true", nil)
						So(err, ShouldBeNil)
						req.Header.Add("Authorization", bearer)
						response, err = client.Do(req)
						So(err, ShouldBeNil)
						responseData, err = io.ReadAll(response.Body)
						So(err, ShouldBeNil)

						var jstati2 []JStatus
						err = json.Unmarshal(responseData, &jstati2)
						So(err, ShouldBeNil)
						So(len(jstati2), ShouldEqual, 1)

						So(jstati2[0].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
						So(jstati2[0].CwdBase, ShouldEqual, "/tmp")
						So(jstati2[0].State, ShouldEqual, JobStateBuried)
						So(jstati2[0].StdOut, ShouldEqual, "3")
						So(jstati2[0].Started, ShouldNotBeNil)
						So(*jstati2[0].Started, ShouldBeGreaterThanOrEqualTo, t.Unix())
						So(jstati2[0].Ended, ShouldNotBeNil)
						So(*jstati2[0].Ended, ShouldBeGreaterThanOrEqualTo, t.Unix())

						req, err = http.NewRequest(http.MethodGet, jobsEndPoint+"/?state=buried&std=false", nil)
						So(err, ShouldBeNil)
						req.Header.Add("Authorization", bearer)
						response, err = client.Do(req)
						So(err, ShouldBeNil)
						responseData, err = io.ReadAll(response.Body)
						So(err, ShouldBeNil)

						var jstati3 []JStatus
						err = json.Unmarshal(responseData, &jstati3)
						So(err, ShouldBeNil)
						So(len(jstati3), ShouldEqual, 1)

						So(jstati3[0].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
						So(jstati3[0].CwdBase, ShouldEqual, "/tmp")
						So(jstati3[0].State, ShouldEqual, JobStateBuried)
						So(jstati3[0].StdOut, ShouldEqual, "")
					})

					Convey("You can GET all jobs by state and RepGroup", func() {
						req, err := http.NewRequest(http.MethodGet, jobsEndPoint+"/rp1?state=ready", nil)
						So(err, ShouldBeNil)
						req.Header.Add("Authorization", bearer)
						response, err := client.Do(req)
						So(err, ShouldBeNil)
						responseData, err := io.ReadAll(response.Body)
						So(err, ShouldBeNil)

						var jstati []JStatus
						err = json.Unmarshal(responseData, &jstati)
						So(err, ShouldBeNil)
						So(len(jstati), ShouldEqual, 1)
						So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
					})
				})
			})
		})

		Convey("You can POST to add a job with a cloud_flavor to the queue", func() {
			var inputJobs []*JobViaJSON
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 1 && true", RepGrp: "rp1", CloudFlavor: "o1.tiny"})
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)

			req, err := http.NewRequest(http.MethodPost, jobsEndPoint+"/", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			req.Header.Add("Content-Type", "application/json")
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			responseData, err := io.ReadAll(response.Body)
			So(err, ShouldBeNil)
			var jstati []JStatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 1)

			So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
			So(jstati[0].State, ShouldEqual, JobStateReady)
			So(jstati[0].CwdBase, ShouldEqual, "/tmp")
			So(jstati[0].RepGroup, ShouldEqual, "rp1")
			other := []string{"cloud_flavor:o1.tiny"}
			So(jstati[0].OtherRequests, ShouldResemble, other)

			Convey("You can GET the job and the cloud_flavor is still there", func() {
				req, err := http.NewRequest(http.MethodGet, jobsEndPoint+"/rp1?state=ready", nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err := client.Do(req)
				So(err, ShouldBeNil)
				responseData, err := io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []JStatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 1)
				So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
				So(jstati[0].OtherRequests, ShouldResemble, other)
			})
		})

		Convey("You must supply certain properties when adding jobs", func() {
			inputJobs := []*JobViaJSON{{RepGrp: "foo"}}
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)
			req, err := http.NewRequest(http.MethodPost, jobsEndPoint+"/", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			req.Header.Add("Content-Type", "application/json")
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			So(response.StatusCode, ShouldEqual, 400)
			responseData, err := io.ReadAll(response.Body)
			So(err, ShouldBeNil)
			So(string(responseData), ShouldEqual, "there was a problem interpreting your job: cmd was not specified\n")
		})

		Convey("You can POST with optional parameters to set new job defaults", func() {
			inputJobs := []*JobViaJSON{{Cmd: "echo defaults"}}
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)
			bs := fmt.Sprintf("&on_success=%s&on_failure=%s&on_exit=%s", url.QueryEscape(`[{"cleanup":true}]`), url.QueryEscape(`[{"run":"foo"}]`), url.QueryEscape(`[{"cleanup_all":true}]`))
			mountJSON := `[{"Mount":"/tmp/wr_mnt","Targets":[{"Profile":"default","Path":"mybucket/subdir","Write":true}]}]`
			mounts := fmt.Sprintf("&mounts=%s", url.QueryEscape(mountJSON))
			req, err := http.NewRequest(http.MethodPost, jobsEndPoint+"/?rep_grp=defaultedRepGrp&cwd=/tmp/foo&cpus=2&dep_grps=a,b,c&deps=x,y&change_home=true&memory=3G&time=4m&no_retry_over_walltime=5m"+bs+mounts, bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			req.Header.Add("Content-Type", "application/json")
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			responseData, err := io.ReadAll(response.Body)
			So(err, ShouldBeNil)
			var jstati []JStatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 1)

			So(jstati[0].Key, ShouldEqual, "b17c665295e0a3fcf2e07c6d7ad6ddd4")
			So(jstati[0].State, ShouldEqual, JobStateDependent)
			So(jstati[0].CwdBase, ShouldEqual, "/tmp/foo")
			So(jstati[0].RepGroup, ShouldEqual, "defaultedRepGrp")
			So(jstati[0].Cores, ShouldEqual, 2)
			So(jstati[0].DepGroups, ShouldResemble, []string{"a", "b", "c"})
			So(jstati[0].Dependencies, ShouldResemble, []string{"x", "y"})
			So(jstati[0].WaitingForDepGroups, ShouldResemble, []string{"x", "y"})
			So(jstati[0].HomeChanged, ShouldBeTrue)
			So(jstati[0].ExpectedRAM, ShouldEqual, 3072)
			So(jstati[0].ExpectedTime, ShouldEqual, 240)
			So(jstati[0].Behaviours, ShouldEqual, `{"on_failure":[{"run":"foo"}],"on_success":[{"cleanup":true}],"on_exit":[{"cleanup_all":true}]}`)
			So(jstati[0].Mounts, ShouldEqual, mountJSON)

			job, err := jq.GetByEssence(&JobEssence{JobKey: "b17c665295e0a3fcf2e07c6d7ad6ddd4"}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Retries, ShouldEqual, 0)
			So(job.NoRetriesOverWalltime, ShouldEqual, 5*time.Minute)
		})

		Convey("Trying to POST a job with a non-existent cloud_script fails", func() {
			cloudScript := filepath.Join(dir, "cloud.script")
			uploadedScript := filepath.Join(dir, "cloud.script.uploaded")

			scriptContent := []byte("echo 1\n")
			err := os.WriteFile(cloudScript, scriptContent, 0o600)
			So(err, ShouldBeNil)

			_, err = os.Stat(uploadedScript)
			So(err, ShouldNotBeNil)

			var inputJobs []*JobViaJSON
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 1 && true", RepGrp: "rp1", CloudScript: uploadedScript})
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)

			req, err := http.NewRequest(http.MethodPost, jobsEndPoint+"/", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			req.Header.Add("Content-Type", "application/json")
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			So(response.StatusCode, ShouldEqual, http.StatusBadRequest)

			Convey("But it works after uploading the script", func() {
				file, err := os.Open(cloudScript)
				So(err, ShouldBeNil)
				req, err := http.NewRequest(http.MethodPut, uploadEndPoint+"/?path="+uploadedScript, file)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err := client.Do(req)
				So(err, ShouldBeNil)
				responseData, err := io.ReadAll(response.Body)
				So(err, ShouldBeNil)
				file.Close()

				_, err = os.Stat(uploadedScript)
				So(err, ShouldBeNil)
				content, err := os.ReadFile(uploadedScript)
				So(err, ShouldBeNil)
				So(content, ShouldResemble, scriptContent)

				answer := make(map[string]string)
				err = json.Unmarshal(responseData, &answer)
				So(err, ShouldBeNil)
				So(answer["path"], ShouldEqual, uploadedScript)

				req, err = http.NewRequest(http.MethodPost, jobsEndPoint+"/", bytes.NewBuffer(jsonValue))
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				req.Header.Add("Content-Type", "application/json")
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				So(response.StatusCode, ShouldEqual, http.StatusCreated)

				Convey("You can also upload without specifying an upload path", func() {
					md5Path := filepath.Join(uploadsDir, "3", "3", "4", "d5669e1fb34a7d0583a9773e1b237")

					file, err := os.Open(cloudScript)
					So(err, ShouldBeNil)
					req, err := http.NewRequest(http.MethodPut, uploadEndPoint+"/", file)
					So(err, ShouldBeNil)
					req.Header.Add("Authorization", bearer)
					response, err := client.Do(req)
					So(err, ShouldBeNil)
					responseData, err := io.ReadAll(response.Body)
					So(err, ShouldBeNil)
					file.Close()

					info, err := os.Stat(md5Path)
					So(err, ShouldBeNil)
					content, err := os.ReadFile(md5Path)
					So(err, ShouldBeNil)
					So(content, ShouldResemble, scriptContent)

					answer := make(map[string]string)
					err = json.Unmarshal(responseData, &answer)
					So(err, ShouldBeNil)
					So(answer["path"], ShouldEqual, md5Path)

					// and trying a second time succeeds, but doesn't change the
					// original upload
					file, err = os.Open(cloudScript)
					So(err, ShouldBeNil)
					req, err = http.NewRequest(http.MethodPut, uploadEndPoint+"/", file)
					So(err, ShouldBeNil)
					req.Header.Add("Authorization", bearer)
					response, err = client.Do(req)
					So(err, ShouldBeNil)
					responseData, err = io.ReadAll(response.Body)
					So(err, ShouldBeNil)
					file.Close()

					answer = make(map[string]string)
					err = json.Unmarshal(responseData, &answer)
					So(err, ShouldBeNil)
					So(answer["path"], ShouldEqual, md5Path)

					info2, err := os.Stat(md5Path)
					So(err, ShouldBeNil)
					So(info2.ModTime(), ShouldEqual, info.ModTime())
				})
			})
		})

		Convey("Initial GET queries on the warnings endpoint return nothing", func() {
			req, err := http.NewRequest(http.MethodGet, warningsEndPoint, nil)
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			responseData, err := io.ReadAll(response.Body)
			So(err, ShouldBeNil)

			var sis []*schedulerIssue
			err = json.Unmarshal(responseData, &sis)
			So(err, ShouldBeNil)
			So(len(sis), ShouldEqual, 0)

			Convey("After adding some warnings, you can retrieve them, which also dismisses them", func() {
				server.simutex.Lock()
				server.schedIssues["msg1"] = &schedulerIssue{
					Msg:       "msg1",
					FirstDate: time.Now().Unix(),
					LastDate:  time.Now().Unix(),
					Count:     1,
				}
				server.schedIssues["msg2"] = &schedulerIssue{
					Msg:       "msg2",
					FirstDate: time.Now().Unix(),
					LastDate:  time.Now().Unix(),
					Count:     2,
				}
				So(len(server.schedIssues), ShouldEqual, 2)
				server.simutex.Unlock()

				req, err := http.NewRequest(http.MethodGet, warningsEndPoint, nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err := client.Do(req)
				So(err, ShouldBeNil)
				responseData, err := io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var sis []*schedulerIssue
				err = json.Unmarshal(responseData, &sis)
				So(err, ShouldBeNil)
				So(len(sis), ShouldEqual, 2)

				server.simutex.RLock()
				So(len(server.schedIssues), ShouldEqual, 0)
				server.simutex.RUnlock()
			})
		})

		Convey("Initial GET queries on the warnings and servers endpoints return nothing", func() {
			req, err := http.NewRequest(http.MethodGet, serversEndPoint, nil)
			So(err, ShouldBeNil)
			req.Header.Add("Authorization", bearer)
			response, err := client.Do(req)
			So(err, ShouldBeNil)
			responseData, err := io.ReadAll(response.Body)
			So(err, ShouldBeNil)

			var servers []*BadServer
			err = json.Unmarshal(responseData, &servers)
			So(err, ShouldBeNil)
			So(len(servers), ShouldEqual, 0)

			Convey("After adding some bad servers, you can get and delete them", func() {
				cloudServer := &cloud.Server{
					ID:   "serverid1",
					Name: "name",
					IP:   "192.168.0.1",
				}
				cloudServer.GoneBad()
				server.bsmutex.Lock()
				server.badServers["serverid1"] = cloudServer
				So(len(server.badServers), ShouldEqual, 1)
				server.bsmutex.Unlock()

				req, err := http.NewRequest(http.MethodGet, serversEndPoint, nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err := client.Do(req)
				So(err, ShouldBeNil)
				responseData, err := io.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var servers []*BadServer
				err = json.Unmarshal(responseData, &servers)
				So(err, ShouldBeNil)
				So(len(servers), ShouldEqual, 1)
				So(servers[0].Name, ShouldEqual, "name")

				req, err = http.NewRequest(http.MethodDelete, serversEndPoint, nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				So(response.StatusCode, ShouldEqual, http.StatusBadRequest)

				req, err = http.NewRequest(http.MethodDelete, serversEndPoint+"?id=serverid1", nil)
				So(err, ShouldBeNil)
				req.Header.Add("Authorization", bearer)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				So(response.StatusCode, ShouldEqual, http.StatusNotModified) // because the fake server doesn't actually exist

				server.bsmutex.RLock()
				So(len(server.badServers), ShouldEqual, 0)
				server.bsmutex.RUnlock()
			})
		})

		Convey("The Go client can retrieve the scheduler alerts shown by the web UI", func() {
			server.simutex.Lock()
			server.schedIssues["client scheduler message"] = &schedulerIssue{
				Msg:       "client scheduler message",
				FirstDate: 1710000000,
				LastDate:  1710000060,
				Count:     3,
			}
			server.simutex.Unlock()

			cloudServer := &cloud.Server{
				ID:   "serverid-client-alert",
				Name: "alert-server",
				IP:   "192.168.0.7",
			}
			cloudServer.GoneBad("boot failed")

			server.bsmutex.Lock()
			server.badServers[cloudServer.ID] = cloudServer
			server.bsmutex.Unlock()

			alerts, err := jq.GetSchedulerAlerts()
			So(err, ShouldBeNil)
			So(alerts, ShouldNotBeNil)
			So(alerts.Issues, ShouldHaveLength, 1)
			So(alerts.Issues[0].Msg, ShouldEqual, "client scheduler message")
			So(alerts.Issues[0].FirstDate, ShouldEqual, 1710000000)
			So(alerts.Issues[0].LastDate, ShouldEqual, 1710000060)
			So(alerts.Issues[0].Count, ShouldEqual, 3)
			So(alerts.BadServers, ShouldHaveLength, 1)
			So(alerts.BadServers[0].ID, ShouldEqual, "serverid-client-alert")
			So(alerts.BadServers[0].Name, ShouldEqual, "alert-server")
			So(alerts.BadServers[0].IP, ShouldEqual, "192.168.0.7")
			So(alerts.BadServers[0].Problem, ShouldEqual, "boot failed")

			server.simutex.RLock()
			So(server.schedIssues, ShouldHaveLength, 0)
			server.simutex.RUnlock()

			server.bsmutex.RLock()
			So(server.badServers, ShouldHaveLength, 1)
			server.bsmutex.RUnlock()
		})

		Reset(func() {
			server.Stop(ctx, true)
		})
	})

	if server != nil {
		server.Stop(ctx, true)
	}
}
