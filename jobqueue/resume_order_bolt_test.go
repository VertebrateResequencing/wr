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

// The cases of resume_order_test.go that stop the manager while a resume races
// the bbolt best-effort writer's final drain, which they reach through that
// writer's own hooks and state.

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestResumeDuringFinalDrainStops proves that a resume whose change is made
// while the database's writer makes its final drain, after that drain took the
// pending writes, does not hold up the manager's stop: the writer must refuse
// writes before its final drain takes them, not after it has written them.
func TestResumeDuringFinalDrainStops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job whose resume is made during the writer's final drain", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		serverStopping := false

		defer func() {
			if !serverStopping {
				server.Stop(ctx, true)
			}
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " resumedrain", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		So(storedStateWithin(t, server.inspector(), key, JobStateSuspended), ShouldEqual, JobStateSuspended)

		stopped, finalSwap, resumeMade := make(chan struct{}), make(chan struct{}), make(chan struct{})

		var swapOnce sync.Once

		bestEffortSwappedHook = func() {
			select {
			case <-server.boltDB().beStop:
			default:
				return
			}

			swapOnce.Do(func() {
				close(finalSwap)

				select {
				case <-resumeMade:
				case <-time.After(failedChangeStopTimeout):
				}
			})
		}
		defer func() { bestEffortSwappedHook = nil }()

		swapped := false

		jobChangeAheadHook = func(hooked string) {
			if hooked != key {
				return
			}

			serverStopping = true

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			select {
			case <-finalSwap:
				swapped = true
			case <-time.After(failedChangeStopTimeout):
			}
		}
		defer func() { jobChangeAheadHook = nil }()

		Convey("the resume is made, and the manager stops promptly", func() {
			resumed := server.resumeJobs(ctx, []string{key})
			jobChangeAheadHook = nil

			close(resumeMade)

			stoppedInTime := false

			select {
			case <-stopped:
				stoppedInTime = true
			case <-time.After(failedChangeStopTimeout):
			}

			So(serverStopping, ShouldBeTrue)
			So(swapped, ShouldBeTrue)
			So(resumed, ShouldEqual, 1)
			So(stoppedInTime, ShouldBeTrue)
		})
	})
}

// TestResumeAfterWriterStopsStops proves that a resume whose write was prepared
// before the database's writer stopped, and whose change is made after, does not
// hold up the manager's stop.
func TestResumeAfterWriterStopsStops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job whose resume is made after the manager began stopping", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		serverStopping := false

		defer func() {
			if !serverStopping {
				server.Stop(ctx, true)
			}
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " resumestop", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		stopped := make(chan struct{})
		writerStopped := false

		jobChangeAheadHook = func(hooked string) {
			if hooked != key {
				return
			}

			serverStopping = true

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			writerStopped = waitForWriterStopped(server.boltDB(), failedChangeStopTimeout)
		}
		defer func() { jobChangeAheadHook = nil }()

		Convey("the resume is made, and the manager stops promptly", func() {
			resumed := server.resumeJobs(ctx, []string{key})
			jobChangeAheadHook = nil

			stoppedInTime := false

			select {
			case <-stopped:
				stoppedInTime = true
			case <-time.After(failedChangeStopTimeout):
			}

			So(serverStopping, ShouldBeTrue)
			So(writerStopped, ShouldBeTrue)
			So(resumed, ShouldEqual, 1)
			So(stoppedInTime, ShouldBeTrue)
		})
	})
}

// waitForWriterStopped waits up to d for database's best-effort writer to latch
// that it has stopped, and says whether it did.
func waitForWriterStopped(database *db, d time.Duration) bool {
	deadline := time.Now().Add(d)

	for time.Now().Before(deadline) {
		database.beMu.Lock()
		stopped := database.beStopped
		database.beMu.Unlock()

		if stopped {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return false
}
