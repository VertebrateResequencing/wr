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
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	. "github.com/smartystreets/goconvey/convey"
)

// TestStopWaitsForABadServerConfirmation holds the goroutine that confirms a
// bad cloud server dead at confirmServerDeadHook while the manager stops. A
// Stop that did not wait for it used to finish, nil the queue, and leave the
// released goroutine to read it when looking for the server's jobs, crashing
// the process.
func TestStopWaitsForABadServerConfirmation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	_, serverConfig, _, _, _ := jobqueueTestInit(true) //nolint:dogsled // only the manager is needed

	Convey("Given a manager with a bad server it will confirm dead", t, func() {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})

		confirmServerDeadHook = func() {
			select {
			case entered <- struct{}{}:
			default:
			}

			<-release
		}

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		stopped := make(chan struct{})
		stopStarted := false

		defer func() {
			if stopStarted {
				<-stopped
			} else {
				server.Stop(ctx, true)
			}

			confirmServerDeadHook = nil
		}()

		released := false
		releaseConfirm := func() {
			if !released {
				released = true

				close(release)
			}
		}

		defer releaseConfirm()

		bad := &cloud.Server{ID: "confirm-dead-after-stop", Name: "bad", IP: "192.168.0.1"}
		bad.GoneBad("gone bad for the test")

		startStop := func() {
			stopStarted = true

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()
		}

		Convey("stopping while its confirmation is pending waits for it and shuts down cleanly", func() {
			server.handleBadServerUpdate(ctx, bad, time.Millisecond)

			So(pollUntil(func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			}), ShouldBeTrue)

			startStop()

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

			releaseConfirm()

			stoppedAfterRelease := false

			select {
			case <-stopped:
				stoppedAfterRelease = true
			case <-time.After(30 * time.Second):
			}

			// give an untracked confirmation time to read the queue shutdown
			// let go of
			<-time.After(100 * time.Millisecond)

			So(stoppedBeforeRelease, ShouldBeFalse)
			So(stoppedAfterRelease, ShouldBeTrue)
		})

		Convey("a confirmation that is still waiting does not hold up Stop", func() {
			releaseConfirm()
			server.handleBadServerUpdate(ctx, bad, time.Hour)

			So(pollUntil(func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			}), ShouldBeTrue)

			startStop()

			stoppedPromptly := false

			select {
			case <-stopped:
				stoppedPromptly = true
			case <-time.After(30 * time.Second):
			}

			So(stoppedPromptly, ShouldBeTrue)
		})
	})
}

// TestBadServerIsConfirmedDeadWhileRunning guards the normal path that
// TestStopWaitsForABadServerConfirmation's shutdown handling must not break: a
// server that stays bad for autoConfirmDead is still destroyed and forgotten.
func TestBadServerIsConfirmedDeadWhileRunning(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	_, serverConfig, _, _, _ := jobqueueTestInit(true) //nolint:dogsled // only the manager is needed

	Convey("Given a running manager told about a bad server", t, func() {
		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		bad := &cloud.Server{ID: "confirm-dead-while-running", Name: "bad", IP: "192.168.0.2"}
		bad.GoneBad("gone bad for the test")

		server.handleBadServerUpdate(ctx, bad, 50*time.Millisecond)

		Convey("it is destroyed and forgotten once it has been bad for autoConfirmDead", func() {
			So(pollUntil(func() bool {
				server.bsmutex.RLock()
				_, stillBad := server.badServers[bad.ID]
				server.bsmutex.RUnlock()

				return !stillBad && bad.Destroyed()
			}), ShouldBeTrue)
		})
	})
}
