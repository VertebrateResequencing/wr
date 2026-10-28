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
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// runStateDuplicateRunnerPid is the runner pid a re-sent start reports.
const runStateDuplicateRunnerPid = 4242

// TestReservationAndStartRecoveredAfterCrash checks that a reservation and a
// start, a duplicate start included, are what a manager killed after them
// recovers.
func TestReservationAndStartRecoveredAfterCrash(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner's manager with a job added", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		f.add(movedOnRetries, nil, "recovered")

		key := (&Job{Cmd: restFormTrue + " movedon recovered", Cwd: testCwd}).Key()

		Convey("a manager killed after the reservation and before Started holds the job in Run for the runner",
			func() {
				So(f.reserve().Key(), ShouldEqual, key)

				f.crashOnto(ctx, f.backup())

				item, err := f.server.q.Get(key)
				So(err, ShouldBeNil)
				So(item.Stats().State, ShouldEqual, queue.ItemStateRun)

				recovered := preStartServerJob(f.server, key)
				recovered.RLock()
				reservedBy := recovered.ReservedBy
				recovered.RUnlock()

				So(reservedBy, ShouldEqual, f.runner.clientid)
			})

		Convey("a duplicate start giving the runner pid of a job recovered without one is recovered with it", func() {
			reserved := f.reserveAndStart()

			// what a job recovered from a record written without a runner pid is.
			inMemory := preStartServerJob(f.server, key)
			inMemory.Lock()
			inMemory.RunnerPid = 0
			inMemory.Unlock()

			So(f.server.db.updateJobRunStateDurable(inMemory), ShouldBeNil)
			So(storedLiveJob(t, f.server.inspector(), key).RunnerPid, ShouldEqual, 0)

			req, err := f.runner.startedRequest(reserved, f.cmdPid, time.Now())
			So(err, ShouldBeNil)

			req.Job.RunnerPid = runStateDuplicateRunnerPid

			_, err = f.runner.request(req)
			So(err, ShouldBeNil)

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			runnerPid, state := recovered.RunnerPid, recovered.State
			recovered.RUnlock()

			So(state, ShouldEqual, JobStateRunning)
			So(runnerPid, ShouldEqual, runStateDuplicateRunnerPid)
		})
	})
}

// TestReservationLeavesWaitingForDepGroupsToRecovery checks that a
// reservation does not store WaitingForDepGroups, and recovery re-derives it,
// so a stale stored value is not what a recovered job waits on.
func TestReservationLeavesWaitingForDepGroupsToRecovery(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job with no dependencies whose stored record says it waits on dep group g", t, func() {
		f := newMovedOnFixture(ctx, t, "")
		defer f.stop(ctx)

		f.add(movedOnRetries, nil, "waits")

		key := (&Job{Cmd: restFormTrue + " movedon waits", Cwd: testCwd}).Key()

		So(rewriteStoredLiveJob(f.server, key, func(job *Job) {
			job.WaitingForDepGroups = []string{"g"}
		}), ShouldBeNil)
		So(storedLiveJob(t, f.server.inspector(), key).WaitingForDepGroups, ShouldResemble, []string{"g"})

		Convey("once it is reserved and the manager crashes, it is recovered waiting on nothing", func() {
			So(f.reserve().Key(), ShouldEqual, key)

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			defer recovered.RUnlock()

			So(recovered.WaitingForDepGroups, ShouldBeEmpty)
		})
	})
}

// TestReservationStoresClearedRerunMark checks that a job recovered out of the
// run sub-queue has its stale mark to run again cleared in memory, and its
// next reservation stores that, so a crash while it then runs does not recover
// it marked, and it runs only once.
func TestReservationStoresClearedRerunMark(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a crash image in which a job is lost with its stored mark to run again", t, func() {
		f := newMovedOnFixture(ctx, t, "")
		defer f.stop(ctx)

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")
		cmd := startDurabilityCmd(marker, stopFile)

		f.add(movedOnRetries, func(job *Job) { job.Cmd = cmd }, "rerun")

		key := (&Job{Cmd: cmd, Cwd: testCwd}).Key()

		So(rewriteStoredLiveJob(f.server, key, func(job *Job) {
			job.State = JobStateLost
			job.Lost = true
			job.RerunAfterRun = true
		}), ShouldBeNil)

		f.crashOnto(ctx, f.backup())

		Convey("once recovered, reserved, started and recovered again, it is running unmarked and runs once", func() {
			reserved := f.reserve()
			So(reserved.Key(), ShouldEqual, key)

			run := exec.CommandContext(ctx, "sh", "-c", cmd)
			So(run.Start(), ShouldBeNil)

			defer func() {
				_ = os.WriteFile(stopFile, nil, 0o600) //nolint:errcheck // best-effort test cleanup
				_ = run.Wait()                         //nolint:errcheck // best-effort test cleanup
			}()

			So(waitForRuns(marker, 1, reserveDurabilityFirstRunWait), ShouldBeTrue)
			So(f.runner.Started(reserved, run.Process.Pid), ShouldBeNil)

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			state, mark := recovered.State, recovered.RerunAfterRun
			recovered.RUnlock()

			So(state, ShouldEqual, JobStateRunning)
			So(mark, ShouldBeFalse)

			killCalled, err := f.runner.Touch(reserved)
			So(err, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			So(os.WriteFile(stopFile, nil, 0o600), ShouldBeNil)
			So(run.Wait(), ShouldBeNil)

			So(f.runner.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			_, err = f.server.q.Get(key)
			So(err, ShouldNotBeNil)

			again, err := f.user.Reserve(time.Second)
			So(err, ShouldBeNil)
			So(again, ShouldBeNil)
			So(runCount(marker), ShouldEqual, 1)
		})
	})
}

// TestReservationKeepsLearnedRequirements checks that requirements a job
// learned from its ReqGroup in memory before its reservation are what recovery
// reads after a crash, though its live record was written before it learned
// them.
func TestReservationKeepsLearnedRequirements(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		reqGroup  = "lr"
		requested = 100
		peak      = 1500
		completed = 10
	)

	ctx := context.Background()

	Convey("Given a manager whose ReqGroup learned from 10 completed 100MB jobs that peaked at 1500MB", t, func() {
		f := newMovedOnFixture(ctx, t, "")
		defer f.stop(ctx)

		learning := func(job *Job) {
			job.ReqGroup = reqGroup
			job.Requirements.RAM = requested
		}

		names := make([]string, completed)
		for i := range names {
			names[i] = "done" + strconv.Itoa(i)
		}

		f.add(movedOnRetries, learning, names...)

		for range completed {
			done := f.reserveAndStart()
			So(f.runner.Archive(done, &JobEndState{
				Exited: true, Exitcode: 0, PeakRAM: peak, EndTime: time.Now(),
			}), ShouldBeNil)
		}

		So(pollUntil(func() bool {
			rec := f.server.db.refreshReqGroupRecs(reqGroup)

			return rec != nil && rec.RAM > requested
		}), ShouldBeTrue)

		Convey("an 11th job reserved after learning them is recovered after a crash with them", func() {
			f.add(movedOnRetries, learning, "learner")

			key := (&Job{Cmd: restFormTrue + " movedon learner", Cwd: testCwd}).Key()

			// the reservation's write is what has to carry them, so the job must
			// have learned them before it.
			So(pollUntil(func() bool { return runStateJobRAM(preStartServerJob(f.server, key)) > requested }),
				ShouldBeTrue)

			So(f.reserve().Key(), ShouldEqual, key)

			learned := runStateJobRAM(preStartServerJob(f.server, key))

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			defer recovered.RUnlock()

			So(recovered.Requirements.RAM, ShouldEqual, learned)
			So(recovered.RequirementsOrig, ShouldNotBeNil)
			So(recovered.RequirementsOrig.RAM, ShouldEqual, requested)
		})
	})
}

// runStateJobRAM returns job's Requirements.RAM, read under its lock, or 0 for
// a nil job.
func runStateJobRAM(job *Job) int {
	if job == nil {
		return 0
	}

	job.RLock()
	defer job.RUnlock()

	return job.Requirements.RAM
}
