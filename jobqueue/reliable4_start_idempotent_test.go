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

// TestReliable4StartIdempotent covers reliable4 issue #2: a DUPLICATE Started()
// report for the SAME start (same host+pid, as retryStartReport produces when it
// re-sends after a reply was lost) must NOT re-increment the server-side
// job.Attempts, which would prematurely erode the retry budget (UntilBuried) and
// bury the job one real attempt early. A genuinely NEW start (different pid) must
// still increment Attempts.
func TestReliable4StartIdempotent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a live manager and a reserved job", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " startidempotent", Cwd: testCwdPath,
			RepGroup: "reliable4_start_idempotent", ReqGroup: "reliable4_start_idempotent",
			Requirements: standardReqs, Retries: 3,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		key := reserved.Key()

		attempts, ok := serverJobAttempts(server, key)
		So(ok, ShouldBeTrue)
		So(attempts, ShouldEqual, 0)

		Convey("A duplicate Started() report for the same host+pid increments Attempts only once", func() {
			pid := os.Getpid()
			So(jq.Started(reserved, pid), ShouldBeNil)

			attempts, ok := serverJobAttempts(server, key)
			So(ok, ShouldBeTrue)
			So(attempts, ShouldEqual, 1)

			// a re-sent report of the SAME start (same host+pid) is an idempotent
			// ack: it must not inflate Attempts.
			So(jq.Started(reserved, pid), ShouldBeNil)

			attempts, ok = serverJobAttempts(server, key)
			So(ok, ShouldBeTrue)
			So(attempts, ShouldEqual, 1)

			Convey("but a genuinely new start (different pid) does start a new attempt", func() {
				So(jq.Started(reserved, pid+1), ShouldBeNil)

				attempts, ok := serverJobAttempts(server, key)
				So(ok, ShouldBeTrue)
				So(attempts, ShouldEqual, 2)
			})
		})

		Convey("A duplicate Started() report clears a spurious Lost without a new attempt", func() {
			pid := os.Getpid()
			So(jq.Started(reserved, pid), ShouldBeNil)

			attempts, ok := serverJobAttempts(server, key)
			So(ok, ShouldBeTrue)
			So(attempts, ShouldEqual, 1)
			So(serverJobLost(server, key), ShouldBeFalse)

			// simulate a spurious TTR-driven markJobLost setting Lost=true on the
			// still-alive, still-Running job while its Started() reply was in flight.
			So(setServerJobLost(server, key), ShouldBeTrue)
			So(serverJobLost(server, key), ShouldBeTrue)

			// the retryStartReport re-send of the SAME start (same host+pid) is fresh
			// proof of liveness: it must un-lose the job WITHOUT starting a new attempt.
			So(jq.Started(reserved, pid), ShouldBeNil)

			attempts, ok = serverJobAttempts(server, key)
			So(ok, ShouldBeTrue)
			So(attempts, ShouldEqual, 1)
			So(serverJobLost(server, key), ShouldBeFalse)
		})
	})
}

// TestReliable4StartIdempotentRunnerPid covers the PR #555 follow-up to the
// idempotent-ack branch of applyJobStart: a DUPLICATE Started() for the same
// host+pid must ALSO adopt a FIRST-SEEN runner pid. A job that went Running with
// RunnerPid==0 (recovered from the DB, or first-started by an older runner that
// reported no runner pid) whose current runner now re-sends Started() carrying a
// real RunnerPid must record it, so confirmJobDead keeps the both-pid liveness
// protection instead of falling back to the command-pid-only verdict. The
// deliberately-preserved idempotent fields (Attempts/StartTime) must NOT change,
// and an already-set non-zero RunnerPid must never be clobbered or overwritten.
func TestReliable4StartIdempotentRunnerPid(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		pid       = 424242
		host      = "runnerpid.example"
		runnerPid = 515151
	)

	server := &Server{}
	startTime := time.Now().Add(-time.Minute)

	Convey("A duplicate Started() ack adopts a first-seen runner pid without a new attempt", t, func() {
		job := &Job{
			State: JobStateRunning, Pid: pid, Host: host, RunnerPid: 0,
			Attempts: 1, StartTime: startTime, Lost: true,
		}
		crJob := &Job{Pid: pid, Host: host, RunnerPid: runnerPid}

		So(server.applyJobStart(job, crJob), ShouldBeTrue)

		// the newly-reported runner pid is recorded (restoring both-pid liveness)...
		So(job.RunnerPid, ShouldEqual, runnerPid)
		// ...the spurious Lost is cleared (fresh proof the runner is alive)...
		So(job.Lost, ShouldBeFalse)
		// ...but the idempotent guarantees are preserved: no new attempt, same start.
		So(job.Attempts, ShouldEqual, uint32(1))
		So(job.StartTime.Equal(startTime), ShouldBeTrue)
	})

	Convey("A duplicate Started() ack without a runner pid does not clobber an existing one", t, func() {
		job := &Job{
			State: JobStateRunning, Pid: pid, Host: host, RunnerPid: runnerPid,
			Attempts: 1, StartTime: startTime,
		}
		crJob := &Job{Pid: pid, Host: host, RunnerPid: 0}

		So(server.applyJobStart(job, crJob), ShouldBeTrue)

		So(job.RunnerPid, ShouldEqual, runnerPid)
		So(job.Attempts, ShouldEqual, uint32(1))
	})

	Convey("A duplicate Started() ack never overwrites an already-set runner pid", t, func() {
		job := &Job{
			State: JobStateRunning, Pid: pid, Host: host, RunnerPid: runnerPid,
			Attempts: 1, StartTime: startTime,
		}
		crJob := &Job{Pid: pid, Host: host, RunnerPid: runnerPid + 1}

		So(server.applyJobStart(job, crJob), ShouldBeTrue)

		So(job.RunnerPid, ShouldEqual, runnerPid)
	})
}

// serverJobAttempts reads the server-side job.Attempts for the given key under
// lock, returning ok=false if the item is not in the queue.
func serverJobAttempts(server *Server, key string) (int, bool) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return 0, false
	}

	j, isJob := item.Data().(*Job)
	if !isJob {
		return 0, false
	}

	j.RLock()
	attempts := int(j.Attempts)
	j.RUnlock()

	return attempts, true
}

// serverJobLost reads the server-side job.Lost for the given key under lock,
// returning false if the item is not in the queue.
func serverJobLost(server *Server, key string) bool {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return false
	}

	j, isJob := item.Data().(*Job)
	if !isJob {
		return false
	}

	j.RLock()
	lost := j.Lost
	j.RUnlock()

	return lost
}

// setServerJobLost sets the server-side job.Lost=true for the given key under
// lock, modelling a spurious TTR-driven markJobLost on a still-Running job. It
// returns false if the item is not in the queue.
func setServerJobLost(server *Server, key string) bool {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return false
	}

	j, isJob := item.Data().(*Job)
	if !isJob {
		return false
	}

	j.Lock()
	j.Lost = true
	j.Unlock()

	return true
}
