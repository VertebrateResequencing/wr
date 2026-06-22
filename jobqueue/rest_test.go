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
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

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

					<-time.After(300 * time.Millisecond)

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
					So(jstati[0].State, ShouldEqual, JobStateBuried)
					So(jstati[0].Started, ShouldNotBeNil)
					So(jstati[0].Ended, ShouldBeNil)
				})

				Convey("You can DELETE lost jobs to bury them", func() {
					err = jq.Started(job, 1)
					So(err, ShouldBeNil)

					<-time.After(300 * time.Millisecond)

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

					<-time.After(300 * time.Millisecond)

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
					So(jstati[0].State, ShouldEqual, JobStateBuried)
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
			So(jstati[0].State, ShouldEqual, JobStateReady)
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
