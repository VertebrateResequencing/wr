// Copyright © 2017, 2018 Genome Research Limited
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
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15"
	. "github.com/smartystreets/goconvey/convey"
)

func TestREST(t *testing.T) {
	if runnermode {
		return
	}

	testLogger := log15.New()
	testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.StderrHandler))

	// load our config to know where our development manager port is supposed to
	// be; we'll use that to test jobqueue
	config := internal.ConfigLoad("development", true, testLogger)
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   "local",
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		DBFile:          config.ManagerDbFile,
		DBFileBackup:    config.ManagerDbFile + "_bk",
		CertFile:        config.ManagerCertFile,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
		Logger:          testLogger,
	}
	addr := "localhost:" + config.ManagerPort
	baseURL := "https://localhost:" + config.ManagerWeb
	jobsEndPoint := baseURL + "/rest/v1/jobs"
	warningsEndPoint := baseURL + "/rest/v1/warnings/"
	serversEndPoint := baseURL + "/rest/v1/servers/"

	ServerInterruptTime = 10 * time.Millisecond
	ServerReserveTicker = 10 * time.Millisecond
	ClientReleaseDelay = 100 * time.Millisecond
	ServerItemTTR = 200 * time.Millisecond
	ClientTouchInterval = 50 * time.Millisecond
	clientConnectTime := 1500 * time.Millisecond

	var server *Server
	var err error
	Convey("Once the jobqueue server is up", t, func() {
		server, _, err = Serve(serverConfig)
		So(err, ShouldBeNil)

		Convey("Initial GET queries return nothing", func() {
			response, err := http.Get(jobsEndPoint)
			So(err, ShouldBeNil)
			responseData, err := ioutil.ReadAll(response.Body)
			So(err, ShouldBeNil)

			var jstati []jstatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 0)
		})

		Convey("You can POST to add jobs to the queue", func() {
			var inputJobs []*JobViaJSON
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 1 && true", RepGrp: "rp1"})
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 2 && true", RepGrp: "rp2", Cwd: "/tmp/foo"})
			pri := 2
			cpus := 2
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 3 && false", CwdMatters: true, RepGrp: "rp1", Memory: "50M", CPUs: &cpus, Time: "2m", Priority: &pri, Env: []string{"foo=bar", "test=case"}})
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)

			response, err := http.Post(jobsEndPoint+"/", "application/json", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			responseData, err := ioutil.ReadAll(response.Body)
			So(err, ShouldBeNil)
			var jstati []jstatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 3)

			So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
			So(jstati[0].State, ShouldEqual, "ready")
			So(jstati[0].CwdBase, ShouldEqual, "/tmp")
			So(jstati[0].RepGroup, ShouldEqual, "rp1")
			So(jstati[0].ExpectedRAM, ShouldEqual, 1000)
			So(jstati[0].ExpectedTime, ShouldEqual, 3600)
			So(jstati[0].Cores, ShouldEqual, 1)
			So(jstati[1].Key, ShouldEqual, "f5c0d6240167a6e0b803e23f74e3a085")
			So(jstati[1].RepGroup, ShouldEqual, "rp2")
			So(jstati[1].CwdBase, ShouldEqual, "/tmp/foo")
			So(jstati[2].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
			So(jstati[2].State, ShouldEqual, "ready")
			So(jstati[2].CwdBase, ShouldEqual, "/tmp")
			So(jstati[2].RepGroup, ShouldEqual, "rp1")
			So(jstati[2].ExpectedRAM, ShouldEqual, 50)
			So(jstati[2].ExpectedTime, ShouldEqual, 120)
			So(jstati[2].Cores, ShouldEqual, 2)

			Convey("You can GET the current status of all jobs", func() {
				response, err := http.Get(jobsEndPoint)
				So(err, ShouldBeNil)
				responseData, err := ioutil.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []jstatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 3)
			})

			Convey("You can GET the status of particular jobs using their ids", func() {
				response, err := http.Get(jobsEndPoint + "/de6d167c58701e55f5b9f9e1e91d7807")
				So(err, ShouldBeNil)
				responseData, err := ioutil.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []jstatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 1)
				So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")

				response, err = http.Get(jobsEndPoint + "/de6d167c58701e55f5b9f9e1e91d7807,db1e7d99becace3306c1c2470331c78e")
				So(err, ShouldBeNil)
				responseData, err = ioutil.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati2 []jstatus
				err = json.Unmarshal(responseData, &jstati2)
				So(err, ShouldBeNil)
				So(len(jstati2), ShouldEqual, 2)
				So(jstati2[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
				So(jstati2[1].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
			})

			Convey("You can GET the status of jobs by RepGroup", func() {
				response, err := http.Get(jobsEndPoint + "/rp1")
				So(err, ShouldBeNil)
				responseData, err := ioutil.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []jstatus
				err = json.Unmarshal(responseData, &jstati)
				So(err, ShouldBeNil)
				So(len(jstati), ShouldEqual, 2)
				keys := make(map[string]bool)
				for _, j := range jstati {
					keys[j.Key] = true
				}
				So(keys, ShouldResemble, map[string]bool{"de6d167c58701e55f5b9f9e1e91d7807": true, "db1e7d99becace3306c1c2470331c78e": true})

				Convey("And you can modify the results by changing limit", func() {
					response, err := http.Get(jobsEndPoint + "/rp1?limit=1")
					So(err, ShouldBeNil)
					responseData, err := ioutil.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati []jstatus
					err = json.Unmarshal(responseData, &jstati)
					So(err, ShouldBeNil)
					So(len(jstati), ShouldEqual, 1)
					So(jstati[0].Similar, ShouldEqual, 1)
				})
			})

			Convey("Once one of the jobs has changed state", func() {
				jq, err := Connect(addr, config.ManagerCAFile, clientConnectTime)
				So(err, ShouldBeNil)
				defer jq.Disconnect()

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

				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateBuried)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 1)

				Convey("You can GET all jobs by state, and get their stdout/err", func() {
					response, err := http.Get(jobsEndPoint + "/?state=ready")
					So(err, ShouldBeNil)
					responseData, err := ioutil.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati []jstatus
					err = json.Unmarshal(responseData, &jstati)
					So(err, ShouldBeNil)
					So(len(jstati), ShouldEqual, 2)
					keys := make(map[string]bool)
					for _, j := range jstati {
						keys[j.Key] = true
					}
					So(keys, ShouldResemble, map[string]bool{"de6d167c58701e55f5b9f9e1e91d7807": true, "f5c0d6240167a6e0b803e23f74e3a085": true})

					response, err = http.Get(jobsEndPoint + "/?state=buried&std=true")
					So(err, ShouldBeNil)
					responseData, err = ioutil.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati2 []jstatus
					err = json.Unmarshal(responseData, &jstati2)
					So(err, ShouldBeNil)
					So(len(jstati2), ShouldEqual, 1)

					So(jstati2[0].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
					So(jstati2[0].CwdBase, ShouldEqual, "/tmp")
					So(jstati2[0].State, ShouldEqual, "buried")
					So(jstati2[0].StdOut, ShouldEqual, "3")

					response, err = http.Get(jobsEndPoint + "/?state=buried&std=false")
					So(err, ShouldBeNil)
					responseData, err = ioutil.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati3 []jstatus
					err = json.Unmarshal(responseData, &jstati3)
					So(err, ShouldBeNil)
					So(len(jstati3), ShouldEqual, 1)

					So(jstati3[0].Key, ShouldEqual, "db1e7d99becace3306c1c2470331c78e")
					So(jstati3[0].CwdBase, ShouldEqual, "/tmp")
					So(jstati3[0].State, ShouldEqual, "buried")
					So(jstati3[0].StdOut, ShouldEqual, "")
				})

				Convey("You can GET all jobs by state and RepGroup", func() {
					response, err := http.Get(jobsEndPoint + "/rp1?state=ready")
					So(err, ShouldBeNil)
					responseData, err := ioutil.ReadAll(response.Body)
					So(err, ShouldBeNil)

					var jstati []jstatus
					err = json.Unmarshal(responseData, &jstati)
					So(err, ShouldBeNil)
					So(len(jstati), ShouldEqual, 1)
					So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
				})
			})
		})

		Convey("You can POST to add a job with a cloud_flavor to the queue", func() {
			var inputJobs []*JobViaJSON
			inputJobs = append(inputJobs, &JobViaJSON{Cmd: "echo 1 && true", RepGrp: "rp1", CloudFlavor: "o1.tiny"})
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)

			response, err := http.Post(jobsEndPoint+"/", "application/json", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			responseData, err := ioutil.ReadAll(response.Body)
			So(err, ShouldBeNil)
			var jstati []jstatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 1)

			So(jstati[0].Key, ShouldEqual, "de6d167c58701e55f5b9f9e1e91d7807")
			So(jstati[0].State, ShouldEqual, "ready")
			So(jstati[0].CwdBase, ShouldEqual, "/tmp")
			So(jstati[0].RepGroup, ShouldEqual, "rp1")
			other := []string{"cloud_flavor:o1.tiny"}
			So(jstati[0].OtherRequests, ShouldResemble, other)

			Convey("You can GET the job and the cloud_flavor is still there", func() {
				response, err := http.Get(jobsEndPoint + "/rp1?state=ready")
				So(err, ShouldBeNil)
				responseData, err := ioutil.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var jstati []jstatus
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
			response, err := http.Post(jobsEndPoint+"/", "application/json", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			So(response.StatusCode, ShouldEqual, 400)
			responseData, err := ioutil.ReadAll(response.Body)
			So(err, ShouldBeNil)
			So(string(responseData), ShouldEqual, "There was a problem interpreting your job: cmd was not specified\n")
		})

		Convey("You can POST with optional parameters to set new job defaults", func() {
			inputJobs := []*JobViaJSON{{Cmd: "echo defaults"}}
			jsonValue, err := json.Marshal(inputJobs)
			So(err, ShouldBeNil)
			bs := fmt.Sprintf("&on_success=%s&on_failure=%s&on_exit=%s", url.QueryEscape(`[{"cleanup":true}]`), url.QueryEscape(`[{"run":"foo"}]`), url.QueryEscape(`[{"cleanup_all":true}]`))
			mountJSON := `[{"Mount":"/tmp/wr_mnt","Targets":[{"Profile":"default","Path":"mybucket/subdir","Write":true}]}]`
			mounts := fmt.Sprintf("&mounts=%s", url.QueryEscape(mountJSON))
			response, err := http.Post(jobsEndPoint+"/?rep_grp=defaultedRepGrp&cwd=/tmp/foo&cpus=2&dep_grps=a,b,c&deps=x,y&change_home=true&memory=3G&time=4m"+bs+mounts, "application/json", bytes.NewBuffer(jsonValue))
			So(err, ShouldBeNil)
			responseData, err := ioutil.ReadAll(response.Body)
			So(err, ShouldBeNil)
			var jstati []jstatus
			err = json.Unmarshal(responseData, &jstati)
			So(err, ShouldBeNil)
			So(len(jstati), ShouldEqual, 1)

			So(jstati[0].Key, ShouldEqual, "b17c665295e0a3fcf2e07c6d7ad6ddd4")
			So(jstati[0].State, ShouldEqual, "ready")
			So(jstati[0].CwdBase, ShouldEqual, "/tmp/foo")
			So(jstati[0].RepGroup, ShouldEqual, "defaultedRepGrp")
			So(jstati[0].Cores, ShouldEqual, 2)
			So(jstati[0].DepGroups, ShouldResemble, []string{"a", "b", "c"})
			So(jstati[0].Dependencies, ShouldResemble, []string{"x", "y"})
			So(jstati[0].HomeChanged, ShouldBeTrue)
			So(jstati[0].ExpectedRAM, ShouldEqual, 3072)
			So(jstati[0].ExpectedTime, ShouldEqual, 240)
			So(jstati[0].Behaviours, ShouldEqual, `{"on_failure":[{"run":"foo"}],"on_success":[{"cleanup":true}],"on_exit":[{"cleanup_all":true}]}`)
			So(jstati[0].Mounts, ShouldEqual, mountJSON)
		})

		Convey("Initial GET queries on the warnings endpoint return nothing", func() {
			response, err := http.Get(warningsEndPoint)
			So(err, ShouldBeNil)
			responseData, err := ioutil.ReadAll(response.Body)
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

				response, err := http.Get(warningsEndPoint)
				So(err, ShouldBeNil)
				responseData, err := ioutil.ReadAll(response.Body)
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
			response, err := http.Get(serversEndPoint)
			So(err, ShouldBeNil)
			responseData, err := ioutil.ReadAll(response.Body)
			So(err, ShouldBeNil)

			var servers []*badServer
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

				response, err := http.Get(serversEndPoint)
				So(err, ShouldBeNil)
				responseData, err := ioutil.ReadAll(response.Body)
				So(err, ShouldBeNil)

				var servers []*badServer
				err = json.Unmarshal(responseData, &servers)
				So(err, ShouldBeNil)
				So(len(servers), ShouldEqual, 1)
				So(servers[0].Name, ShouldEqual, "name")

				req, err := http.NewRequest(http.MethodDelete, serversEndPoint, nil)
				So(err, ShouldBeNil)
				client := http.DefaultClient
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				So(response.StatusCode, ShouldEqual, http.StatusBadRequest)

				req, err = http.NewRequest(http.MethodDelete, serversEndPoint+"?id=serverid1", nil)
				So(err, ShouldBeNil)
				response, err = client.Do(req)
				So(err, ShouldBeNil)
				So(response.StatusCode, ShouldEqual, http.StatusNotModified) // because the fake server doesn't actually exist

				server.bsmutex.RLock()
				So(len(server.badServers), ShouldEqual, 0)
				server.bsmutex.RUnlock()
			})
		})

		Reset(func() {
			server.Stop(true)
		})
	})

	if server != nil {
		server.Stop(true)
	}
}
