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
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// deleteAfterStopGrace is how long the test gives a Stop that does not wait for
// the delete to come back on its own. Past its database close, such a Stop has
// nothing left that takes this long.
const deleteAfterStopGrace = 2 * time.Second

// TestStopWaitsForARemoveOnFailureDelete holds the goroutine that removes a
// buried remove-on-failure job at deleteOnFailureHook while the manager stops.
// A Stop that did not wait for it used to finish, nil the queue, and leave the
// released goroutine to dereference it, crashing the process.
func TestStopWaitsForARemoveOnFailureDelete(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a buried remove-on-failure job whose delete has not run yet", t, func() {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})

		deleteOnFailureHook = func() {
			select {
			case entered <- struct{}{}:
			default:
			}

			<-release
		}

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		stopStarted := false

		defer func() {
			if !stopStarted {
				server.Stop(ctx, true)
			}

			deleteOnFailureHook = nil
		}()

		released := false
		releaseDelete := func() {
			if !released {
				released = true

				close(release)
			}
		}

		defer releaseDelete()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo delete after stop", Cwd: testCwd, RepGroup: "delete_after_stop", ReqGroup: "delete_after_stop",
			Requirements: standardReqs, Behaviours: Behaviours{&Behaviour{When: OnFailure, Do: Remove}},
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		job, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)
		So(jq.Bury(job, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "failed"), ShouldBeNil)

		So(pollUntil(func() bool {
			select {
			case <-entered:
				return true
			default:
				return false
			}
		}), ShouldBeTrue)

		disconnect(jq)

		Convey("stopping the manager waits for the delete and then shuts down cleanly", func() {
			stopped := make(chan struct{})
			stopStarted = true

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			So(pollUntil(func() bool {
				server.db.RLock()
				defer server.db.RUnlock()

				return server.db.closed
			}), ShouldBeTrue)

			stoppedBeforeRelease := false

			select {
			case <-stopped:
				stoppedBeforeRelease = true
			case <-time.After(deleteAfterStopGrace):
			}

			releaseDelete()

			stoppedAfterRelease := false

			select {
			case <-stopped:
				stoppedAfterRelease = true
			case <-time.After(30 * time.Second):
			}

			So(stoppedBeforeRelease, ShouldBeFalse)
			So(stoppedAfterRelease, ShouldBeTrue)
		})
	})
}
