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

// Untagged, fast behavioural regression tests for the reliable4 TTR-miss
// archive-reject churn fix (checklist 260726-3): the runner reports its OWN pid
// (RunnerPid) as well as the command's child pid, and the manager confirms a lost
// job dead only if BOTH are gone. So a job whose command finished (dead command
// pid) but whose runner is still alive (slow/starved to archive) is NOT re-run,
// and its late successful archive is accepted rather than rejected as ErrBadJob.
// A rare wedged-but-alive runner's slot is still reclaimed by a backstop that
// force-kills the runner after LostRunnerBackstop. Unlike the build-tagged
// reliable4_ttrmiss_test.go these run at small scale under `make test`, so the
// guarantee is covered by the normal test suite.

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable4RunnerPidLiveness is the primary behavioural regression: a job
// whose COMMAND pid is dead but whose RUNNER pid is alive, with its touches lapsed
// past the TTR (so it is marked Lost), must NOT be confirmed dead / re-run, and
// its late Archive() must be ACCEPTED (the success preserved). Pre-fix
// (command-pid-only confirmJobDead) the job is confirmed dead and re-run, so it
// becomes re-reservable and the late archive is rejected as ErrBadJob.
func TestReliable4RunnerPidLiveness(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 300 * time.Millisecond
		rg  = "reliable4_runnerpid_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A live runner's completed job is not re-run and its late archive is accepted", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		// make the lost/confirm-dead path fire promptly so the test is fast.
		server.SetLostJobCheckTimeout(2 * time.Second)
		server.SetLostJobCheckRetryTime(200 * time.Millisecond)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " runnerpid", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 3,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		key := reserved.Key()

		// the command finished: report a definitely-dead command pid. Started reports
		// this test process's own pid as RunnerPid (os.Getpid()), which is alive.
		deadPid := definitelyDeadPid(t)
		So(jq.Started(reserved, deadPid), ShouldBeNil)

		// deliberately stop touching so the TTR lapses and the job is marked Lost.
		So(waitForJobLost(server, key, 20*ttr), ShouldBeTrue)

		// because the runner process is still alive, the job must NOT be confirmed
		// dead / re-run while parked Lost, so a fresh client cannot reserve it.
		reReserved, _ := countReReserves(addr, config.ManagerCAFile, config.ManagerCertDomain,
			token, clientConnectTime, 5)
		So(reReserved, ShouldEqual, 0)

		// the runner's late successful archive must be ACCEPTED (success preserved),
		// not rejected as ErrBadJob (which is what happens once a job has been re-run).
		aerr := jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		So(aerr, ShouldBeNil)
	})
}

// TestReliable4LostRunnerBackstop is the backstop regression: a job parked Lost
// with an ALIVE (wedged) runner pid is NOT re-run while the runner lives (Fix C
// parks it), and once lostFor exceeds the (short, test-set) LostRunnerBackstop the
// server force-kills the runner (KillProcessOnHost) and the job is reclaimed /
// re-run. Pre-backstop (LostRunnerBackstop effectively never reached) the wedged
// runner's job would stay parked forever, holding its slot.
func TestReliable4LostRunnerBackstop(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr      = 300 * time.Millisecond
		backstop = 2 * time.Second
		rg       = "reliable4_backstop_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr
	serverConfig.Timings.LostRunnerBackstop = backstop

	Convey("A wedged-but-alive runner's parked-Lost job is reclaimed only after the backstop kill", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		server.SetLostJobCheckTimeout(2 * time.Second)
		server.SetLostJobCheckRetryTime(200 * time.Millisecond)

		// a live, killable stand-in for the wedged runner process; reaped so a kill
		// truly ends it (no lingering zombie that ps would still report as alive).
		child := exec.CommandContext(ctx, "sleep", "120")
		So(child.Start(), ShouldBeNil)

		childPid := child.Process.Pid

		go func() { _ = child.Wait() }() //nolint:errcheck // just reaping the stand-in after it is killed

		defer func() { _ = child.Process.Kill() }() //nolint:errcheck // best-effort cleanup if the backstop did not fire

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " backstop", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 3,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		key := reserved.Key()

		// command finished (dead command pid); the runner is the live child and is
		// wedged (this test never archives on its behalf).
		So(jq.Started(reserved, definitelyDeadPid(t)), ShouldBeNil)
		setServerJobRunnerPid(server, key, childPid)

		So(waitForJobLost(server, key, 20*ttr), ShouldBeTrue)

		// while the runner (child) is alive and before the backstop, the job is parked
		// Lost-in-Run and must NOT be re-reservable (Fix C must not re-run it yet).
		reReserved, _ := countReReserves(addr, config.ManagerCAFile, config.ManagerCertDomain,
			token, clientConnectTime, 3)
		So(reReserved, ShouldEqual, 0)

		// after the backstop the runner is force-killed, death is confirmed, and the
		// job is re-run -> a fresh client can reserve it again.
		var got *Job

		deadline := time.Now().Add(backstop + 10*time.Second)
		for time.Now().Before(deadline) {
			jq2, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			if errc == nil {
				j, jrerr := jq2.Reserve(200 * time.Millisecond)
				disconnect(jq2)

				if jrerr == nil && j != nil {
					got = j

					break
				}
			}

			time.Sleep(100 * time.Millisecond)
		}

		So(got, ShouldNotBeNil)
		So(got.Key(), ShouldEqual, key)
		So(processAliveLocally(childPid), ShouldBeFalse)
	})
}

// setServerJobRunnerPid overwrites the server-side job's recorded RunnerPid under
// lock (white-box; the test is in package jobqueue), to model a dead or specific
// runner process.
func setServerJobRunnerPid(server *Server, key string, pid int) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return
	}

	job.Lock()
	job.RunnerPid = pid
	job.Unlock()
}

// processAliveLocally reports whether pid is a live (non-zombie) process on this host.
func processAliveLocally(pid int) bool {
	ctx := context.Background()
	//nolint:errcheck,gosec // non-zero exit for a dead pid is expected; pid is a formatted int, not tainted
	out, _ := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(out))

	return state != "" && !strings.HasPrefix(state, "Z")
}
