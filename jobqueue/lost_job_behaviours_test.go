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

// This file guards the moment a lost job's behaviours run in the MANAGER.
//
// Two things have to be true of that moment, and each was got wrong on its own.
//
// The behaviours must act on the run that was LOST. killJob releases a lost job
// back to ready before it returns, so a runner can reserve the RETRY while the
// behaviours of the lost run are still to come, and the retry's first Touch
// writes its new working directory onto the very same *Job. Same job, same key,
// so nothing the workspace resolution proves about a path can tell the two runs
// apart. Only pinning the state when the job is declared lost can.
//
// And they must act only on a run that really was killed. The manager's
// dead-check is an ssh round trip bounded by ServerLostJobCheckTimeout - fifteen
// seconds by default - and in it the job can recover on a touch, or be released
// and started again. Deciding on one run and acting on another swept the working
// directory of a job that was still RUNNING.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// lostRunTTR is the ItemTTR the fixture's manager runs with: long enough that
// the assertions after the kill decision are made well inside one TTR, short
// enough not to dominate the test's run time.
const lostRunTTR = 1 * time.Second

// lostRunSettleTime is how long a test will wait for the manager's behaviours to
// finish once it has been told the kill released the run.
const lostRunSettleTime = 20 * time.Second

// lostRun is a real manager with one real job in it, reserved, started with a
// pid that is really dead, and given the workspace a real mkHashedDir made for
// it - so that letting its TTR expire drives the whole lost-job sequence:
// ttrCallback -> markJobLost (which pins) -> confirmJobDead (which confirms,
// because the pid is gone) -> killLostJobAndTriggerBehaviours.
type lostRun struct {
	server *Server
	client *Client

	// key and live are the job's key and the manager's own *Job for it, which is
	// what every run of the job shares and what the retry writes itself onto.
	key  string
	live *Job

	// cwd is the job's Cwd, and ranIn is the file the `run` behaviour reports its
	// pwd to. It is outside the workspace so that the cleanup behaviour cannot
	// take the evidence away with it, and OnFailure runs before OnExit, so it is
	// written before the sweep.
	cwd   string
	ranIn string

	// lostCwd, lostTmp and lostOut are the working directory, TMPDIR and output
	// of the run that gets lost.
	lostCwd string
	lostTmp string
	lostOut string

	// killed carries what the manager's kill decided: true only if it really
	// released the run the behaviours were pinned for.
	killed chan bool
}

// newLostRun builds the fixture. The caller must defer stop().
func newLostRun(ctx context.Context, t *testing.T, rg string) *lostRun {
	t.Helper()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = lostRunTTR

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	l := &lostRun{server: server, client: jq, cwd: t.TempDir(), killed: make(chan bool, 1)}
	l.ranIn = filepath.Join(l.cwd, "ran_in.txt")

	job := &Job{
		Cmd: restFormTrue, Cwd: l.cwd, RepGroup: rg, ReqGroup: rg,
		Requirements: standardReqs, Retries: 3,
		Behaviours: Behaviours{
			{When: OnFailure, Do: Run, Arg: "pwd > " + l.ranIn},
			{When: OnExit, Do: CleanupAll},
		},
	}

	_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)

	l.key = reserved.Key()
	l.live = l.liveJob()

	// started with a pid that has already exited, so the manager's dead-check
	// really does confirm this run dead, through the real scheduler.
	So(jq.Started(reserved, exitedPid()), ShouldBeNil)

	// the lost run's workspace, made by the real mkHashedDir and reported to the
	// manager the way a Touch reports it, with output in it.
	l.lostCwd, l.lostTmp, err = mkHashedDir(l.cwd, l.key)
	So(err, ShouldBeNil)

	l.lostOut = writeFileIn(l.lostCwd, "abandoned.txt")
	applyLiveSnapshot(l.live, &JobEndState{Cwd: l.lostCwd})

	lostJobKilledHook = func(released bool) {
		select {
		case l.killed <- released:
		default:
		}
	}

	return l
}

func (l *lostRun) stop(ctx context.Context) {
	lostJobDeadCheckedHook = nil
	lostJobKilledHook = nil

	disconnect(l.client)
	l.server.Stop(ctx, true)
}

// liveJob is the manager's own *Job for the fixture's job.
func (l *lostRun) liveJob() *Job {
	item, err := l.server.q.Get(l.key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	return job
}

// inTheDeadCheckWindow runs f where the manager's dead-check returns: after the
// job was declared lost and its behaviours pinned, and before the kill. That is
// the window everything here is about.
func (l *lostRun) inTheDeadCheckWindow(f func()) {
	lostJobDeadCheckedHook = func() {
		lostJobDeadCheckedHook = nil

		f()
	}
}

// waitForKillDecision returns what the manager's kill decided, failing the test
// if the TTR never drove it that far.
func (l *lostRun) waitForKillDecision() bool {
	select {
	case released := <-l.killed:
		return released
	case <-time.After(lostRunSettleTime):
		So("the manager never reached its kill decision", ShouldBeBlank)

		return false
	}
}

// jobStateAndKillCalled reports the live job's state and whether the manager has
// marked it to kill itself on its next touch.
func (l *lostRun) jobStateAndKillCalled() (JobState, bool) {
	l.live.RLock()
	defer l.live.RUnlock()

	return l.live.State, l.live.killCalled
}

// exitedPid returns the pid of a process that has run and been reaped, so that
// the scheduler's `ps` really finds nothing.
func exitedPid() int {
	cmd := exec.Command("/bin/true")
	So(cmd.Run(), ShouldBeNil)

	return cmd.Process.Pid
}

// makeRetryWorkspace makes a second workspace for the same key, with partial
// output in it, and reports it onto the live *Job the way the retry's first
// Touch does.
//
// It makes no Convey assertion of its own, because it runs on the MANAGER's
// goroutine rather than the test's, and returns everything for the test to
// assert on afterwards.
func (l *lostRun) makeRetryWorkspace() (actualCwd, tmpDir, output string, err error) {
	actualCwd, tmpDir, err = mkHashedDir(l.cwd, l.key)
	if err != nil {
		return "", "", "", err
	}

	output = filepath.Join(actualCwd, "partial.txt")
	if err = os.WriteFile(output, []byte("precious\n"), 0o600); err != nil {
		return "", "", "", err
	}

	applyLiveSnapshot(l.live, &JobEndState{Cwd: actualCwd})

	return actualCwd, tmpDir, output, nil
}

// soGoneWithin waits up to lostRunSettleTime for path to be deleted.
func soGoneWithin(path string) {
	deadline := time.Now().Add(lostRunSettleTime)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	soPathsGone(path)
}

func TestLostJobBehavioursActOnTheLostRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job whose retry is reserved before its behaviours run", t, func() {
		l := newLostRun(ctx, t, "lost_job_behaviours")

		defer l.stop(ctx)

		// the retry: a second workspace of the SAME key, since it is the same
		// job, reported onto the same *Job by its first Touch in the window
		// killLostRun opens by releasing the lost job back to ready.
		var (
			retryCwd, retryTmp, retryOutput string
			retryErr                        error
		)

		lostJobKilledHook = func(released bool) {
			retryCwd, retryTmp, retryOutput, retryErr = l.makeRetryWorkspace()

			select {
			case l.killed <- released:
			default:
			}
		}

		Convey("the behaviours act on the run that was lost, not on the live retry", func() {
			So(l.waitForKillDecision(), ShouldBeTrue)
			So(retryErr, ShouldBeNil)
			So(retryCwd, ShouldNotBeBlank)
			So(retryCwd, ShouldNotEqual, l.lostCwd)

			soGoneWithin(l.lostCwd)

			// the retry is running in these; the survival is asserted before
			// anything else, since it is the data loss that matters.
			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))

			// and the workspace that was really abandoned is the one that goes,
			// rather than being leaked while the live one is swept.
			soPathsGone(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))

			ran, errr := os.ReadFile(l.ranIn)
			So(errr, ShouldBeNil)
			So(strings.TrimSpace(string(ran)), ShouldEqual, l.lostCwd)
		})
	})
}

func TestLostJobBehavioursSpareARecoveredJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job that recovers on a touch inside the dead-check window", t, func() {
		l := newLostRun(ctx, t, "lost_job_recovered")

		defer l.stop(ctx)

		// the blip that lost the job clears, and the runner's next Touch takes
		// the job back off lost. Its Cmd is still running, in the very working
		// directory the manager is about to sweep.
		l.inTheDeadCheckWindow(func() {
			l.server.recoverLostTouchedJob(l.live)
		})

		Convey("the manager neither kills it nor runs its behaviours", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			// the job is still running, in a working directory that is all still
			// there, and the manager has not marked it to kill itself either.
			soPathsExist(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))
			soPathsGone(l.ranIn)

			state, killCalled := l.jobStateAndKillCalled()
			So(state, ShouldEqual, JobStateRunning)
			So(killCalled, ShouldBeFalse)
		})
	})
}

func TestLostJobBehavioursSpareARetryStartedInTheWindow(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job whose retry is started inside the dead-check window", t, func() {
		l := newLostRun(ctx, t, "lost_job_retry_in_window")

		defer l.stop(ctx)

		var (
			retryCwd, retryTmp, retryOutput string
			retryErr                        error
		)

		// the job is released and reserved again while the manager is off asking
		// whether the old run is dead - a retryable failure, or a `wr kill`. The
		// retry's Started takes the job off lost (applyJobStart) and its first
		// Touch writes its own working directory onto this same *Job.
		l.inTheDeadCheckWindow(func() {
			l.server.applyJobStart(l.live, &Job{Pid: os.Getpid(), Host: "localhost"})

			retryCwd, retryTmp, retryOutput, retryErr = l.makeRetryWorkspace()
		})

		Convey("the manager acts on neither run", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)
			So(retryErr, ShouldBeNil)
			So(retryCwd, ShouldNotEqual, l.lostCwd)

			// the retry is running in these.
			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))
			soPathsGone(l.ranIn)

			state, killCalled := l.jobStateAndKillCalled()
			So(state, ShouldEqual, JobStateRunning)
			So(killCalled, ShouldBeFalse)
		})
	})
}

func TestLostJobBehavioursSpareASecondLostRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job whose retry is itself lost inside the dead-check window", t, func() {
		l := newLostRun(ctx, t, "lost_job_second_loss")

		defer l.stop(ctx)

		var (
			retryCwd, retryTmp, retryOutput string
			retryErr                        error
		)

		// the worst version of the window: the job is started again as a second
		// run, reports its own working directory, and that run is then declared
		// lost too. Lost is true and the key is the same, so only the pinned
		// ActualCwd tells the manager that the death it confirmed was the FIRST
		// run's and says nothing about this one - which has its own confirmation
		// on the way.
		l.inTheDeadCheckWindow(func() {
			l.server.applyJobStart(l.live, &Job{Pid: os.Getpid(), Host: "localhost"})

			retryCwd, retryTmp, retryOutput, retryErr = l.makeRetryWorkspace()
			if retryErr != nil {
				return
			}

			l.live.Lock()
			l.live.Lost = true
			l.live.Unlock()
		})

		Convey("the manager sweeps neither run", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)
			So(retryErr, ShouldBeNil)
			So(retryCwd, ShouldNotEqual, l.lostCwd)

			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))
			soPathsExist(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))
			soPathsGone(l.ranIn)
		})
	})
}

func TestPinBehavioursIsLocked(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Pinning a lost job's behaviours reads its state under the Job's lock", t, func() {
		// the pin is taken in the manager, on the queue's live *Job, while a
		// runner's touches are writing ActualCwd onto it under that same lock
		// (applyLiveSnapshot). An unlocked read here is a data race whose
		// outcome decides which directory the behaviours delete and run in.
		// -race is what makes this test bite.
		const (
			concurrentRounds          = 50
			concurrentWriterAndPinner = 2
		)

		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd, Behaviours: Behaviours{{When: OnExit, Do: CleanupAll}}}
		actualCwd, _, _ := realWorkSpace(job)

		var wg sync.WaitGroup

		wg.Add(concurrentWriterAndPinner)

		go func() {
			defer wg.Done()

			for range concurrentRounds {
				applyLiveSnapshot(job, &JobEndState{Cwd: actualCwd})
			}
		}()

		go func() {
			defer wg.Done()

			for range concurrentRounds {
				_ = job.pinBehaviours()
			}
		}()

		wg.Wait()

		soPathsExist(cwd)
	})
}
