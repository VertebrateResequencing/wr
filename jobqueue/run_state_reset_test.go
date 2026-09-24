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
	"maps"
	"os"
	"reflect"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	runStateResetStdOut = "RUNSTATERESETPREVIOUSSTDOUT"
	runStateResetStdErr = "RUNSTATERESETPREVIOUSSTDERR"
)

// TestReservedRetryShowsNothingOfThePreviousRun drives a real manager: a job's
// first run fails after its touches recorded output, and a new runner then
// reserves the retry. Until that retry reports its own Started, a client asking
// about it must see a reserved job with nothing of the failed run: not its
// host's IP, not why it failed, and not its output.
func TestReservedRetryShowsNothingOfThePreviousRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const rg = "run_state_reset_rg"

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ReleaseDelayMin = 10 * time.Millisecond
	// nothing here touches the retry's reservation, which must not go lost
	// while the test reads it, however loaded the machine.
	serverConfig.Timings.ItemTTR = time.Hour

	Convey("Given a job whose first run failed after touching its output", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		statusPage := dialStatusWS(ctx, t, server, token)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo run state reset", Cwd: testCwd, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 1,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		first, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(first, ShouldNotBeNil)

		So(jq.Started(first, os.Getpid()), ShouldBeNil)

		first.StdOutC = compressStd([]byte(runStateResetStdOut))
		first.StdErrC = compressStd([]byte(runStateResetStdErr))

		_, err = jq.Touch(first)
		So(err, ShouldBeNil)

		So(jq.Release(first, &JobEndState{
			Exited:   true,
			Exitcode: 1,
			PeakRAM:  10,
			CPUtime:  time.Second,
			EndTime:  time.Now(),
			Stdout:   compressStd([]byte(runStateResetStdOut)),
			Stderr:   compressStd([]byte(runStateResetStdErr)),
		}, FailReasonExit), ShouldBeNil)

		failed, err := jq.GetByEssence(first.ToEssense(), true, false)
		So(err, ShouldBeNil)
		So(failed, ShouldNotBeNil)
		So(failed.State, ShouldEqual, JobStateDelayed)
		So(failed.HostIP, ShouldNotBeBlank)
		So(failed.FailReason, ShouldEqual, FailReasonExit)
		So(failed.CPUtime, ShouldEqual, time.Second)
		soJobStd(failed, runStateResetStdOut, runStateResetStdErr)

		Convey("a new runner's reservation of the retry reports none of that run", func() {
			jq2, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq2)

			var retry *Job

			So(pollUntil(func() bool {
				retry, err = jq2.Reserve(50 * time.Millisecond)

				return err == nil && retry != nil
			}), ShouldBeTrue)
			So(retry.Key(), ShouldEqual, first.Key())

			soNothingOfThePreviousRun(retry)

			reserved, errg := jq.GetByEssence(first.ToEssense(), true, false)
			So(errg, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.State, ShouldEqual, JobStateReserved)
			soNothingOfThePreviousRun(reserved)
			soJobStd(reserved, "", "")

			status, errs := reserved.ToStatus()
			So(errs, ShouldBeNil)
			So(status.State, ShouldEqual, JobStateReserved)
			So(status.HostIP, ShouldBeBlank)
			So(status.FailReason, ShouldBeBlank)
			So(status.StdOut, ShouldBeBlank)
			So(status.StdErr, ShouldBeBlank)

			// the manager's own copy is what the next Started's duplicate-start
			// guard and a touch's live update read the state from, so it must say
			// reserved too, not what the failed run left.
			So(liveJobState(server, first.Key()), ShouldEqual, JobStateReserved)

			// the status bars' delta feed saw the whole ready, running, delayed,
			// ready, reserved cycle, and its deltas still net out to what a page
			// loaded now would be seeded with: the one job, counted as running.
			// They are netted rather than applied in arrival order, because the
			// change callbacks that send them may deliver two adjacent transitions
			// in either order (which the browser reconciles).
			all, perRepGroup := server.statusSeedCounts()
			So(perRepGroup[rg], ShouldResemble, map[JobState]int{JobStateRunning: 1})

			var net map[string]map[JobState]int

			converged := pollUntilFor(5*time.Second, func() bool {
				net = netDeltas(statusPage)

				return reflect.DeepEqual(net[rg], perRepGroup[rg]) &&
					reflect.DeepEqual(net[webStatusAllRepGroups], all)
			})

			So(net[rg], ShouldResemble, perRepGroup[rg])
			So(net[webStatusAllRepGroups], ShouldResemble, all)
			So(converged, ShouldBeTrue)
		})
	})
}

// soJobStd asserts job's decompressed stdout and stderr.
func soJobStd(job *Job, stdout, stderr string) {
	gotOut, err := job.StdOut()
	So(err, ShouldBeNil)
	So(gotOut, ShouldEqual, stdout)

	gotErr, err := job.StdErr()
	So(err, ShouldBeNil)
	So(gotErr, ShouldEqual, stderr)
}

// soNothingOfThePreviousRun asserts that job carries none of the fields a
// finished run leaves behind.
func soNothingOfThePreviousRun(job *Job) {
	So(job.HostIP, ShouldBeBlank)
	So(job.HostID, ShouldBeBlank)
	So(job.FailReason, ShouldBeBlank)
	So(job.Exited, ShouldBeFalse)
	So(job.StartTime.IsZero(), ShouldBeTrue)
	So(job.EndTime.IsZero(), ShouldBeTrue)
	So(job.CPUtime, ShouldEqual, 0)
	So(job.PeakRAM, ShouldEqual, 0)
	So(job.PeakDisk, ShouldEqual, 0)
}

// liveJobState is the State of the manager's own *Job for key.
func liveJobState(server *Server, key string) JobState {
	item, err := server.q.Get(key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	job.RLock()
	defer job.RUnlock()

	return job.State
}

// netDeltas sums every jstateCount delta the status page has been sent so far
// into per-RepGroup counts, leaving out states that net to zero.
func netDeltas(statusPage *wsRecorder) map[string]map[JobState]int {
	net := make(map[string]map[JobState]int)

	for _, msg := range statusPage.snapshot() {
		if !msg.isDelta() {
			continue
		}

		counts := net[msg.RepGroup]
		if counts == nil {
			counts = make(map[JobState]int)
			net[msg.RepGroup] = counts
		}

		if msg.FromState != JobStateNew {
			counts[msg.FromState] -= msg.Count
		}

		counts[msg.ToState] += msg.Count
	}

	for _, counts := range net {
		maps.DeleteFunc(counts, func(_ JobState, n int) bool { return n == 0 })
	}

	return net
}
