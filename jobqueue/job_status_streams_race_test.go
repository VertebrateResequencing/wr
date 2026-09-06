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
	"sync"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// statusStreamsRaceJobs is how many separate Jobs the race tests below
	// contend on. A fresh Job per round gives the race detector fresh addresses
	// to watch, which finds an unsynchronised access far more reliably than
	// hammering one address for longer does.
	statusStreamsRaceJobs = 20

	// statusStreamsRaceCalls is how many times each of the two contending
	// goroutines calls into the production code per Job.
	statusStreamsRaceCalls = 300
)

// TestJobStatusStreamsStdRace covers the overlap the manager creates by design:
// the queue's change-callback goroutine calls ToStatus() on the queue-held *Job
// (jobtransition.go, after waitForJobStartTime deliberately waits for the
// runner's Started RPC), while that same runner's touches reach
// applyLiveSnapshot, which writes StdOutC and StdErrC on the same *Job under its
// write lock.
//
// ToStatus() gathers the streams BEFORE it takes the Job's read lock, so the
// accessors it calls must take that lock themselves. The race detector is the
// verdict here: remove the locking from StdOut()/StdErr() and this goes red.
func TestJobStatusStreamsStdRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given the head and tail of a running Cmd's output", t, func() {
		const std = "some stdout that is long enough to be worth compressing"

		compressed, err := compress([]byte(std))
		So(err, ShouldBeNil)

		Convey("ToStatus() on a Job a runner is touching does not race with the touch", func() {
			var status JStatus

			for range statusStreamsRaceJobs {
				job := newStatusStreamsRaceJob()
				snapshot := &JobEndState{Cwd: job.Cwd, Stdout: compressed, Stderr: compressed}

				status, err = statusRacedAgainst(job, func() { applyLiveSnapshot(job, snapshot) })
			}

			So(err, ShouldBeNil)
			So(status.StdOut, ShouldEqual, std)
			So(status.StdErr, ShouldEqual, std)
		})
	})
}

// TestJobStatusStreamsEnvRace is TestJobStatusStreamsStdRace for EnvC and
// EnvCRetrieved, which ToStatus() reads through Env() in the same unlocked
// gather, and which the web interface's rerun of a completed job clears under
// the Job's write lock.
func TestJobStatusStreamsEnvRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a completed Job with a stored environment", t, func() {
		envC, err := compressEnv([]string{"WR_RACE_ONE=1", "WR_RACE_TWO=2"})
		So(err, ShouldBeNil)

		Convey("ToStatus() on it does not race with the web interface rerunning it", func() {
			var status JStatus

			for range statusStreamsRaceJobs {
				job := newStatusStreamsRaceJob()
				job.State = JobStateComplete
				job.EnvC = envC
				job.EnvCRetrieved = true

				status, err = statusRacedAgainst(job, func() { resetCompletedJobForRerun(job) })
			}

			So(err, ShouldBeNil)
			So(status.Env, ShouldBeEmpty)
		})
	})
}

// newStatusStreamsRaceJob makes a running Job that ToStatus() can be called on.
func newStatusStreamsRaceJob() *Job {
	return &Job{
		Cmd:          "sleep 1",
		Cwd:          "/tmp",
		RepGroup:     "status_streams_race",
		Requirements: &scheduler.Requirements{RAM: 1, Time: 1, Cores: 1},
		State:        JobStateRunning,
	}
}

// statusRacedAgainst calls job.ToStatus() on one goroutine while write runs on
// another, both statusStreamsRaceCalls times against the one *Job, then returns
// the status of the settled Job.
func statusRacedAgainst(job *Job, write func()) (JStatus, error) {
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		for range statusStreamsRaceCalls {
			if _, err := job.ToStatus(); err != nil {
				return
			}
		}
	}()

	go func() {
		defer wg.Done()

		for range statusStreamsRaceCalls {
			write()
		}
	}()

	wg.Wait()

	return job.ToStatus()
}
