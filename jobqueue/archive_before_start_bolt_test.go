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
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// This file holds the late-start test that stalls the archive's commit by
// holding bbolt's writer, so is deleted with bbolt (spec F5).

// TestLateStartDuringArchive proves that a start report that reaches the manager
// while its runner's archive is still waiting to commit is refused and leaves
// the completed job alone. A runner from before start reports were settled
// ahead of the final report could send one then: a background retry of a start
// already recorded, whose reply was lost.
func TestLateStartDuringArchive(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner's archive of a started job waiting on a stalled commit", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() { server.Stop(ctx, true) }()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: restFormTrue + " latestart", Cwd: testCwd, RepGroup: archiveBeforeStartRepGroup,
			ReqGroup: archiveBeforeStartRepGroup, Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		startTime := reserved.StartTime

		holdTx, err := server.boltDB().bolt.Begin(true)
		So(err, ShouldBeNil)

		var releaseOnce sync.Once

		release := func() {
			releaseOnce.Do(func() { _ = holdTx.Rollback() }) //nolint:errcheck // rollback of a held tx
		}

		defer release()

		endTime := time.Now()
		archived := make(chan error, 1)

		go func() {
			archived <- jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: endTime})
		}()

		So(waitForServerJob(server, reserved.Key(), archiveBeforeStartWait, func(job *Job) bool {
			return job.archivesPending == 1
		}), ShouldBeTrue)

		// a retry of the runner's start, on a second connection since the first
		// is still waiting on the archive's reply.
		late, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(late)

		late.clientid = jq.clientid
		started := make(chan error, 1)

		go func() { started <- late.Started(reserved, os.Getpid()) }()

		Convey("the start is refused without waiting on the commit, and the job completes as archived", func() {
			var errStart error

			select {
			case errStart = <-started:
			case <-time.After(2 * time.Second):
				release()

				errStart = <-started
			}

			var jqErr Error
			So(errors.As(errStart, &jqErr), ShouldBeTrue)
			So(jqErr.Err, ShouldEqual, ErrBadJob)

			release()
			So(<-archived, ShouldBeNil)

			server.Stop(ctx, true)

			serverConfig.dontWipeDevDB = true
			server, _, _, err = serve(ctx, serverConfig)
			So(err, ShouldBeNil)
			So(waitUntilRecovered(server), ShouldBeTrue)

			jq2, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq2)

			done, errg := jq2.GetByRepGroup(archiveBeforeStartRepGroup, false, 0, "", true, false)
			So(errg, ShouldBeNil)
			So(len(done), ShouldEqual, 1)
			So(done[0].State, ShouldEqual, JobStateComplete)
			So(done[0].EndTime.Equal(endTime), ShouldBeTrue)
			So(done[0].StartTime.Equal(startTime), ShouldBeTrue)
			So(done[0].Attempts, ShouldEqual, 1)

			again, errr := jq2.Reserve(time.Second)
			So(errr, ShouldBeNil)
			So(again, ShouldBeNil)
		})
	})
}
