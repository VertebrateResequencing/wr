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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// TestKillBeforeCmdStartIsHonoured: a kill requested while a reserved job's
// Execute is still preparing (before cmd.Start) reaches the runner on its first
// touch. The command must then never be started, and the job must end buried as
// killed rather than run to completion.
func TestKillBeforeCmdStartIsHonoured(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond

	Convey("Given a live manager and a reserved job that has been killed", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		release := make(chan struct{})
		t.Setenv("DOCKER_HOST", gatedDockerSocket(t, release))
		t.Setenv("DOCKER_API_VERSION", dockerTestAPIVersion)

		cwd := filepath.Join(t.TempDir(), "job")
		So(os.MkdirAll(cwd, 0o700), ShouldBeNil)

		marker := filepath.Join(t.TempDir(), "ran")

		const repGroup = "kill_before_start"

		job := &Job{
			Cmd: "touch " + marker + " && sleep 3", Cwd: cwd, CwdMatters: true,
			RepGroup: repGroup, ReqGroup: repGroup, MonitorDocker: "?",
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
		}

		added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		killed, err := jq.Kill([]*JobEssence{{JobKey: job.Key()}})
		So(err, ShouldBeNil)
		So(killed, ShouldEqual, 1)

		releaseAfterFirstTouchReply(jq, release)

		Convey("Execute does not start the command and the job is buried as killed", func() {
			execErr := jq.Execute(ctx, reserved, "/bin/sh")
			So(execErr, ShouldNotBeNil)
			So(execErr.Error(), ShouldContainSubstring, FailReasonKilled)

			_, errs := os.Stat(marker)
			So(os.IsNotExist(errs), ShouldBeTrue)

			jobs, errg := jq.GetByRepGroup(repGroup, false, 0, "", true, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonKilled)
			So(jobs[0].Exited, ShouldBeFalse)
			So(jobs[0].Exitcode, ShouldEqual, -1)

			stderr, errs := jobs[0].StdErr()
			So(errs, ShouldBeNil)
			So(stderr, ShouldContainSubstring, "was not started")
			So(stderr, ShouldContainSubstring, FailReasonKilled)
		})
	})
}

// gatedDockerSocket serves a fake docker API on a unix socket. Its first
// container listing (the one Execute makes, before cmd.Start, to set up docker
// monitoring) is held until release is closed; every other request is answered
// at once. It stands in for any slow pre-start step of Execute (mounting, docker
// setup, or a CPU-starved runner) and lets the test choose where Execute is.
func gatedDockerSocket(t *testing.T, release <-chan struct{}) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "docker.sock")

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(context.Background(), "unix", sock)
	So(err, ShouldBeNil)

	var once sync.Once

	server := &http.Server{
		ReadHeaderTimeout: dockerTestBound,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/containers/json") {
				http.Error(w, `{"message":"not implemented by this fake"}`, http.StatusNotFound)

				return
			}

			once.Do(func() { <-release })

			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]")) //nolint:errcheck
		}),
	}

	go server.Serve(listener) //nolint:errcheck

	t.Cleanup(func() { _ = server.Close() })

	return "unix://" + sock
}

// releaseAfterFirstTouchReply closes release once jq's first touch, including
// the decoding of its reply, has finished: touch holds teMutex from before
// liveTouchHook until its reply is decoded.
func releaseAfterFirstTouchReply(jq *Client, release chan<- struct{}) {
	touched := make(chan struct{})

	var touchOnce sync.Once

	jq.liveTouchHook = func(*JobEndState) { touchOnce.Do(func() { close(touched) }) }

	go func() {
		<-touched

		for !jq.teMutex.TryLock() {
			time.Sleep(time.Millisecond)
		}

		jq.teMutex.Unlock()
		time.Sleep(200 * time.Millisecond)
		close(release)
	}()
}
