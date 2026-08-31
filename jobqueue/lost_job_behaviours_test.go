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

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// lostRunTTR is the ItemTTR the fixture's manager runs with: short enough not
	// to dominate the test's run time, long enough that a slow box still reaches
	// the first expiry the way a fast one does.
	lostRunTTR = 1 * time.Second

	// lostRunSettleTime is how long a test waits for something it expects the
	// manager to do.
	lostRunSettleTime = 20 * time.Second

	// noBehaviourWindow is how long a test watches a resumed manager it expects
	// to run no behaviour at all.
	noBehaviourWindow = 2 * time.Second

	// retryOutputName is what a cwd_matters retry calls the file it is part way
	// through writing. It ends in .tmp because the lost run's `run` behaviour
	// deletes *.tmp in the directory it is given, which is the ordinary shape of
	// an --on_failure cleanup command and what makes the wrong directory fatal.
	retryOutputName = "retry_output.tmp"
)

// lostRunOpts says which ordinary manager and job the fixture is to be. Each
// combination is a case in which the reported ActualCwd is blank or stale for
// the whole of a run, so that pinning it identifies no run at all.
type lostRunOpts struct {
	// cwdMatters makes the job a --cwd_matters one, which runs directly in the
	// user's own Cwd and so never has a working directory of wr's - its
	// ActualCwd is permanently blank (Job.setActualCwd).
	cwdMatters bool

	// webless makes the fixture behave as a manager with no web port does: it
	// never enables the live touch snapshots (liveJTouchEnabled), so it learns
	// nothing about a run after its Started.
	webless bool
}

// lostRun is a real manager with one real job in it, reserved, started with a
// pid that is really dead, and given the workspace a real mkHashedDir made for
// it - so that letting its TTR expire drives the whole lost-job sequence:
// ttrCallback -> markJobLost (which pins) -> confirmJobDead (which confirms,
// because the pid is gone) -> killLostJobAndTriggerBehaviours.
//
// The manager runs that sequence on its own goroutine, and the test does every
// piece of work itself, on its own. Nothing is shared between the two but the
// channels below and the *Job's own locked fields: a hook that wrote the test's
// variables was a data race, and so was assigning the hooks after Serve had
// already started the goroutines that read them - which is why they are assigned
// BEFORE it, when starting those goroutines is still what orders the two.
type lostRun struct {
	server *Server
	client *Client
	opts   lostRunOpts

	// key and live are the job's key and the manager's own *Job for it, which is
	// what every run of the job shares and what a retry writes itself onto.
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

	// deadChecked and proceed hand the test the window the dead-check occupies:
	// after the job was declared lost and its behaviours pinned, and before the
	// kill. That is where a job recovers, or is released and started again.
	//
	// killed and resume hand it the moment after the kill decision, which is
	// where a runner reserves the retry.
	//
	// All four are unbuffered, so the manager is PARKED at each moment for as
	// long as the test wants it to be. A test that asserts nothing was deleted
	// has to make that assertion at a moment when nothing could yet have deleted
	// it, and "signal and carry on" is not such a moment: it passes against a
	// manager that goes on to sweep the directory a millisecond later.
	deadChecked chan struct{}
	proceed     chan struct{}
	killed      chan bool
	resume      chan struct{}

	// behavioursRan carries the name of the first behaviour moment the manager
	// reached, if it reached one. Parking the manager proves nothing happened
	// BEFORE the test looked; this is what proves nothing happens after it is
	// let go, which is a different claim and needs its own evidence.
	behavioursRan chan string

	// done releases a parked manager whatever the test did or did not do, so
	// that a failed test, or a SECOND lost cycle the test never asked for, ends
	// with the fixture rather than blocking on it.
	done chan struct{}

	proceedOnce sync.Once
	resumeOnce  sync.Once
	doneOnce    sync.Once
}

// newLostRun builds the fixture. The caller must defer stop().
func newLostRun(ctx context.Context, t *testing.T, rg string, opts ...lostRunOpts) *lostRun {
	t.Helper()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = lostRunTTR

	l := &lostRun{
		opts:        firstLostRunOpts(opts),
		cwd:         t.TempDir(),
		deadChecked: make(chan struct{}), proceed: make(chan struct{}),
		killed: make(chan bool), resume: make(chan struct{}),
		behavioursRan: make(chan string, 2), done: make(chan struct{}),
	}
	l.ranIn = filepath.Join(l.cwd, "ran_in.txt")

	l.installHooks()

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	l.server, l.client = server, jq
	l.startLostRun(jq, rg, standardReqs)

	return l
}

// firstLostRunOpts is the fixture's options, defaulting to the ordinary manager
// running an ordinary job.
func firstLostRunOpts(opts []lostRunOpts) lostRunOpts {
	if len(opts) == 0 {
		return lostRunOpts{}
	}

	return opts[0]
}

// installHooks points the manager's four test seams at this fixture. It runs
// before Serve, because that is what orders it against the goroutines that read
// them; the hooks are left in place afterwards, since taking them down again
// would be the same unordered write in reverse. A stopped fixture's hooks do
// nothing at all - see done.
func (l *lostRun) installHooks() {
	lostJobDeadCheckedHook = func() { l.handOver(l.deadChecked, l.proceed) }
	lostJobKilledHook = l.killedHook
	runResolvedHook = func() { l.behaviourRan("run") }
	cleanupProvenHook = func() { l.behaviourRan("cleanup") }
}

// startLostRun adds the job, reserves it, makes the workspace of the run that is
// about to be lost, and starts it with a dead pid.
//
// It does those last two in the order a runner does them: Client.Execute
// resolves the working directory (resolveWorkingDir) before it calls Started, so
// the run's own Started is the FIRST thing that can tell the manager where the
// run is working, and on a manager with no web port it is the only thing.
func (l *lostRun) startLostRun(jq *Client, rg string, reqs *scheduler.Requirements) {
	job := &Job{
		Cmd: restFormTrue, Cwd: l.cwd, CwdMatters: l.opts.cwdMatters, RepGroup: rg, ReqGroup: rg,
		Requirements: reqs, Retries: 3,
		Behaviours: Behaviours{
			{When: OnFailure, Do: Run, Arg: "pwd > " + l.ranIn + "; rm -f *" + filepath.Ext(retryOutputName)},
			{When: OnExit, Do: CleanupAll},
		},
	}

	_, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)

	l.key = reserved.Key()
	l.live = l.liveJob()

	l.makeLostWorkSpace(reserved)

	// started with a pid that has already exited, so the manager's dead-check
	// really does confirm this run dead, through the real scheduler.
	So(jq.Started(reserved, exitedPid()), ShouldBeNil)

	if !l.opts.cwdMatters && !l.opts.webless {
		applyLiveSnapshot(l.live, &JobEndState{Cwd: l.lostCwd})
	}
}

// makeLostWorkSpace makes the lost run's workspace with the real mkHashedDir,
// puts output in it, and records it on the runner's own Job exactly as
// Client.resolveWorkingDir does - which is what its Started then reports.
//
// A cwd_matters job gets none of that: its Cmd runs in the user's own Cwd, so wr
// creates no working directory for it and its ActualCwd stays blank for the
// whole of every run.
func (l *lostRun) makeLostWorkSpace(reserved *Job) {
	if l.opts.cwdMatters {
		return
	}

	var err error

	l.lostCwd, l.lostTmp, err = mkHashedDir(l.cwd, l.key)
	So(err, ShouldBeNil)

	l.lostOut = writeFileIn(l.lostCwd, "abandoned.txt")

	reserved.Lock()
	reserved.setActualCwd(l.lostCwd)
	reserved.Unlock()
}

// handOver parks the manager, telling the test it has reached one of its two
// moments, until the test gives it back. A stopped fixture does neither.
func (l *lostRun) handOver(reached, given chan struct{}) {
	select {
	case reached <- struct{}{}:
	case <-l.done:
		return
	}

	select {
	case <-given:
	case <-l.done:
	}
}

// killedHook is handOver for the moment that carries the kill decision with it.
func (l *lostRun) killedHook(released bool) {
	select {
	case l.killed <- released:
	case <-l.done:
		return
	}

	select {
	case <-l.resume:
	case <-l.done:
	}
}

// behaviourRan records that the manager reached one of the two moments a
// behaviour cannot get past without acting.
func (l *lostRun) behaviourRan(which string) {
	select {
	case l.behavioursRan <- which:
	default:
	}
}

func (l *lostRun) stop(ctx context.Context) {
	l.doneOnce.Do(func() { close(l.done) })

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

// waitForDeadCheckWindow blocks until the manager has declared the job lost,
// pinned its behaviours and confirmed the run dead, and is about to kill it.
// Whatever the test does next happens in the window that confirmation opened.
func (l *lostRun) waitForDeadCheckWindow() {
	select {
	case <-l.deadChecked:
	case <-time.After(lostRunSettleTime):
		So("the job was never declared lost and confirmed dead", ShouldBeBlank)
	}
}

// proceedManager lets the manager out of the dead-check window.
func (l *lostRun) proceedManager() {
	l.proceedOnce.Do(func() { close(l.proceed) })
}

// waitForKillDecision returns what the manager's kill decided. The manager stays
// parked at that decision until resumeManager.
func (l *lostRun) waitForKillDecision() bool {
	select {
	case released := <-l.killed:
		return released
	case <-time.After(lostRunSettleTime):
		So("the manager never reached its kill decision", ShouldBeBlank)

		return false
	}
}

// resumeManager lets the manager past its kill decision.
func (l *lostRun) resumeManager() {
	l.resumeOnce.Do(func() { close(l.resume) })
}

// soNoBehaviourRuns lets the manager past its kill decision and asserts that it
// runs no behaviour at all afterwards.
//
// The wait is bounded rather than signalled because what is being asserted is a
// negative: a manager that was going to act does so as soon as it is let go, so
// a window several orders of magnitude wider than that is evidence, while no
// completion signal could tell "did not act" from "has not acted yet".
func (l *lostRun) soNoBehaviourRuns() {
	l.resumeManager()

	select {
	case which := <-l.behavioursRan:
		So(which, ShouldBeBlank)
	case <-time.After(noBehaviourWindow):
	}
}

// killCalledOnLiveJob says whether the manager has marked the job to kill itself
// on its next touch.
func (l *lostRun) killCalledOnLiveJob() bool {
	l.live.RLock()
	defer l.live.RUnlock()

	return l.live.killCalled
}

// jobStateAndKillCalled reports the live job's state and whether the manager has
// marked it to kill itself on its next touch.
func (l *lostRun) jobStateAndKillCalled() (JobState, bool) {
	l.live.RLock()
	defer l.live.RUnlock()

	return l.live.State, l.live.killCalled
}

// makeRetryWorkspace makes a second workspace for the same key, with partial
// output in it, and reports it onto the live *Job the way the retry's first
// Touch does. It is a second workspace of the SAME key, since it is the same
// job: that is what nothing about a path can tell apart.
func (l *lostRun) makeRetryWorkspace() (actualCwd, tmpDir, output string) {
	l.reserveRetry()

	actualCwd, tmpDir, err := mkHashedDir(l.cwd, l.key)
	So(err, ShouldBeNil)
	So(actualCwd, ShouldNotEqual, l.lostCwd)

	output = writeFileIn(actualCwd, "partial.txt")

	applyLiveSnapshot(l.live, &JobEndState{Cwd: actualCwd})

	return actualCwd, tmpDir, output
}

// startRetryInWindow starts the job again as a second run, the way a runner
// does: it reserves the job (which is where the manager mints the run and clears
// the last one off the shared *Job), makes whatever that run works in, then its
// Started tells the manager, and only a manager with a web port goes on to learn
// the directory again from the run's touches.
//
// A cwd_matters retry works in the shared Cwd, so what it has part way through
// is a file beside the user's other ones rather than a directory of wr's.
//
// touched says whether the retry got as far as its first live Touch. A run's
// touches are ClientTouchInterval apart - 15 seconds by default - so a run that
// dies inside that interval, which is the commonest way a node kills a job, is
// one no touch was ever received for.
func (l *lostRun) startRetryInWindow(touched bool) (actualCwd, tmpDir, output string) {
	l.reserveRetry()

	if l.opts.cwdMatters {
		l.startRetry("", touched)

		return "", "", writeFileIn(l.cwd, retryOutputName)
	}

	actualCwd, tmpDir, err := mkHashedDir(l.cwd, l.key)
	So(err, ShouldBeNil)
	So(actualCwd, ShouldNotEqual, l.lostCwd)

	output = writeFileIn(actualCwd, retryOutputName)

	l.startRetry(actualCwd, touched)

	return actualCwd, tmpDir, output
}

// reserveRetry is the manager's own reserve-time reset of the shared *Job: what
// respondWithReservedJob does to it the moment a runner takes the job on again.
//
// A retry cannot exist without one, and it is where the run BEGINS: everything
// the retry's runner does that another run's decision could destroy - making its
// working directory, mounting, starting the Cmd - it does after this and before
// its Started reaches the manager.
func (l *lostRun) reserveRetry() {
	l.server.resetJobForReservation(l.live, newTestClientID())
}

// startRetry is the retry's own Started, carrying the working directory it has
// already made, plus the touch snapshot a manager with a web port gets from a
// run that lived long enough to touch.
func (l *lostRun) startRetry(actualCwd string, touched bool) {
	So(l.server.applyJobStart(l.live, &Job{Pid: os.Getpid(), Host: localhost, ActualCwd: actualCwd}),
		ShouldBeTrue)

	if actualCwd != "" && touched && !l.opts.webless {
		applyLiveSnapshot(l.live, &JobEndState{Cwd: actualCwd})
	}
}

// killAndReserveTheJob is `wr kill` on a lost job, followed by a runner taking
// the job on again - both through the real manager, so the item really does go
// out of the run sub-queue and really does come back into it.
//
// killJob is the documented way to deal with a job wr has lost contact with, and
// its own doc says what it does: it RELEASES the lost job. That opens a window
// that closes as soon as a runner reserves the retry, and it opens it while the
// confirmation of the lost run is still to come back.
func (l *lostRun) killAndReserveTheJob(ctx context.Context) *Job {
	killed, err := l.server.killJob(ctx, l.key)
	So(err, ShouldBeNil)
	So(killed, ShouldBeTrue)

	reserved, err := l.client.Reserve(lostRunSettleTime)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, l.key)

	return reserved
}

// getOnWithTheRun is everything a runner does with a reservation before its
// Started reaches the manager: Client.Execute resolves the working directory,
// sets up mounts and calls cmd.Start(), and only then reports. So a Cmd is
// already executing, in a directory the manager has not been told about, for the
// whole of the window this models.
func (l *lostRun) getOnWithTheRun(reserved *Job) (actualCwd, tmpDir, output string) {
	actualCwd, tmpDir, err := mkHashedDir(l.cwd, l.key)
	So(err, ShouldBeNil)
	So(actualCwd, ShouldNotEqual, l.lostCwd)

	output = writeFileIn(actualCwd, retryOutputName)

	reserved.Lock()
	reserved.setActualCwd(actualCwd)
	reserved.Unlock()

	return actualCwd, tmpDir, output
}

// reportItStarted is the run's own Started, which is the first thing the manager
// hears about it.
func (l *lostRun) reportItStarted(reserved *Job, pid int) {
	So(l.client.Started(reserved, pid), ShouldBeNil)
}

// runnerDied replaces the host and pid the manager recorded for the reservation
// (respondWithReservedJob records the reserving runner's own) with a process
// that has already exited. That is what a runner killed by its node leaves
// behind: a reservation held by nothing, and no Started ever coming.
func (l *lostRun) runnerDied() {
	pid := exitedPid()

	l.live.Lock()
	l.live.Host = localhost
	l.live.Pid = pid
	l.live.Unlock()
}

// reportedState is the state the manager reports for the job, which is what `wr
// status` shows: reserved while a runner holds it and has yet to report its
// Started, running once it has, and lost only while the manager has lost contact
// with the run that holds it.
func (l *lostRun) reportedState(ctx context.Context) JobState {
	item, err := l.server.q.Get(l.key)
	So(err, ShouldBeNil)

	return l.server.itemToJob(ctx, item, false, false).State
}

// soLeavesRunQueueWithin waits up to lostRunSettleTime for the job to stop
// holding a reservation, which is what a job that was killed, buried or released
// for another try has done and a job parked lost for ever has not.
func (l *lostRun) soLeavesRunQueueWithin() {
	deadline := time.Now().Add(lostRunSettleTime)

	for time.Now().Before(deadline) {
		item, err := l.server.q.Get(l.key)
		So(err, ShouldBeNil)

		if item.Stats().State != queue.ItemStateRun {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	item, err := l.server.q.Get(l.key)
	So(err, ShouldBeNil)
	So(item.Stats().State, ShouldNotEqual, queue.ItemStateRun)
}

// markRetryLost marks the live *Job lost, as a second TTR expiry does for a
// retry that goes silent in its turn.
func (l *lostRun) markRetryLost() {
	l.live.Lock()
	l.live.Lost = true
	l.live.Unlock()
}

// exitedPid returns the pid of a process that has run and been reaped, so that
// the scheduler's `ps` really finds nothing.
func exitedPid() int {
	cmd := exec.CommandContext(context.Background(), "/bin/true")
	So(cmd.Run(), ShouldBeNil)

	return cmd.Process.Pid
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

		l.waitForDeadCheckWindow()
		l.proceedManager()

		So(l.waitForKillDecision(), ShouldBeTrue)

		// the retry, reported onto the same *Job in the window killLostRun opens
		// by releasing the lost job back to ready.
		retryCwd, retryTmp, retryOutput := l.makeRetryWorkspace()

		Convey("the behaviours act on the run that was lost, not on the live retry", func() {
			l.resumeManager()
			soGoneWithin(l.lostCwd)

			// the retry is running in these; the survival is asserted before
			// anything else, since it is the data loss that matters.
			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))

			// and the workspace that was really abandoned is the one that goes,
			// rather than being leaked while the live one is swept.
			soPathsGone(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))

			ran, err := os.ReadFile(l.ranIn)
			So(err, ShouldBeNil)
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
		// directory the manager is about to sweep - and the touch loop really
		// does run that late, past the behaviours and the Unmount upload.
		l.waitForDeadCheckWindow()
		l.server.recoverLostTouchedJob(l.live)
		l.proceedManager()

		Convey("the manager neither kills it nor runs its behaviours", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			// the job is still running, in a working directory that is all still
			// there, and the manager has not marked it to kill itself either.
			soPathsExist(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))
			soPathsGone(l.ranIn)

			state, killCalled := l.jobStateAndKillCalled()
			So(state, ShouldEqual, JobStateRunning)
			So(killCalled, ShouldBeFalse)

			l.soNoBehaviourRuns()
			soPathsExist(l.lostOut, l.lostCwd, l.lostTmp)
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

		// the job is released and reserved again while the manager is off asking
		// whether the old run is dead - a retryable failure, or a `wr kill`.
		l.waitForDeadCheckWindow()
		retryCwd, retryTmp, retryOutput := l.startRetryInWindow(true)
		l.proceedManager()

		Convey("the manager acts on neither run", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			// the retry is running in these.
			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))
			soPathsGone(l.ranIn)

			state, killCalled := l.jobStateAndKillCalled()
			So(state, ShouldEqual, JobStateRunning)
			So(killCalled, ShouldBeFalse)

			l.soNoBehaviourRuns()
			soPathsExist(retryOutput, retryCwd, retryTmp)
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

		// the worst version of the window: the job is started again as a second
		// run, reports its own working directory, and that run is then declared
		// lost too. Lost is true and the key is the same, so only the pinned
		// ActualCwd tells the manager that the death it confirmed was the FIRST
		// run's and says nothing about this one - which has its own confirmation
		// on the way.
		l.waitForDeadCheckWindow()
		retryCwd, retryTmp, retryOutput := l.startRetryInWindow(true)
		l.markRetryLost()
		l.proceedManager()

		Convey("the manager sweeps neither run", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))
			soPathsExist(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))
			soPathsGone(l.ranIn)

			l.soNoBehaviourRuns()
			soPathsExist(retryOutput, retryCwd, l.lostOut, l.lostCwd)
		})
	})
}

func TestLostJobRetryCheckPinsTheLostRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job the manager could not confirm dead the first time", t, func() {
		l := newLostRun(ctx, t, "lost_job_retry_check")

		defer l.stop(ctx)

		// the retry path re-asks whether the job is still lost after half an
		// hour, and then reopens the very window the pin exists to survive by
		// calling confirmJobDead again. Its answer is therefore only worth
		// having if it pins the run it just checked.
		l.waitForDeadCheckWindow()
		retry, checked := l.server.lostJobRetryCheck(l.key)
		l.proceedManager()

		Convey("its retry check pins the run it found lost", func() {
			So(l.waitForKillDecision(), ShouldBeTrue)
			So(checked, ShouldBeTrue)
			So(retry.key, ShouldEqual, l.key)
			So(retry.pin.workSpace.key, ShouldEqual, l.key)
			So(retry.pin.workSpace.actualCwd, ShouldEqual, l.lostCwd)
			So(retry.pin.behaviours, ShouldHaveLength, 2)
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

func TestLostCwdMattersJobSparesItsSecondRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost cwd_matters job whose retry is itself lost in the dead-check window", t, func() {
		l := newLostRun(ctx, t, "lost_job_cwd_matters", lostRunOpts{cwdMatters: true})

		defer l.stop(ctx)

		// a cwd_matters job's ActualCwd is blank for the whole of every run, so
		// pinning it pins nothing: the run that was lost and the run that is
		// live report the same blank directory, and only something the manager
		// mints per run can tell them apart.
		l.waitForDeadCheckWindow()
		_, _, retryOutput := l.startRetryInWindow(true)
		l.markRetryLost()
		l.proceedManager()

		Convey("the manager neither releases the live run nor runs the lost run's command", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			// the live retry is part way through writing this, in the Cwd the
			// lost run's `run` command would have been executed in.
			soPathsExist(retryOutput)
			soPathsGone(l.ranIn)

			state, killCalled := l.jobStateAndKillCalled()
			So(state, ShouldEqual, JobStateRunning)
			So(killCalled, ShouldBeFalse)

			l.soNoBehaviourRuns()
			soPathsExist(retryOutput)
		})
	})
}

func TestLostJobOnWeblessManagerSparesItsSecondRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a webless manager's lost job whose retry is itself lost in the window", t, func() {
		l := newLostRun(ctx, t, "lost_job_webless", lostRunOpts{webless: true})

		defer l.stop(ctx)

		l.waitForDeadCheckWindow()
		retryCwd, retryTmp, retryOutput := l.startRetryInWindow(true)
		l.markRetryLost()
		l.proceedManager()

		Convey("the manager sweeps neither run", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))
			soPathsExist(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))
			soPathsGone(l.ranIn)

			state, killCalled := l.jobStateAndKillCalled()
			So(state, ShouldEqual, JobStateRunning)
			So(killCalled, ShouldBeFalse)

			l.soNoBehaviourRuns()
			soPathsExist(retryOutput, retryCwd, l.lostOut, l.lostCwd)
		})
	})
}

func TestLostJobSparesASecondRunThatNeverTouched(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job whose retry is lost before its first touch", t, func() {
		l := newLostRun(ctx, t, "lost_job_untouched_retry")

		defer l.stop(ctx)

		// the ordinary manager, and the ordinary way a node kills a job: the
		// retry dies inside its first touch interval, so no touch of its own
		// ever reaches the manager.
		l.waitForDeadCheckWindow()
		retryCwd, retryTmp, retryOutput := l.startRetryInWindow(false)
		l.markRetryLost()
		l.proceedManager()

		Convey("the manager sweeps neither run", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))
			soPathsExist(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))
			soPathsGone(l.ranIn)

			state, killCalled := l.jobStateAndKillCalled()
			So(state, ShouldEqual, JobStateRunning)
			So(killCalled, ShouldBeFalse)

			l.soNoBehaviourRuns()
			soPathsExist(retryOutput, retryCwd, l.lostOut, l.lostCwd)
		})
	})
}

func TestLostJobOnWeblessManagerCleansItsWorkSpace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a webless manager's lost job that no touch was ever received for", t, func() {
		l := newLostRun(ctx, t, "lost_job_webless_cleanup", lostRunOpts{webless: true})

		defer l.stop(ctx)

		l.waitForDeadCheckWindow()
		l.proceedManager()

		Convey("its workspace is still cleaned up and its command still runs in it", func() {
			So(l.waitForKillDecision(), ShouldBeTrue)

			l.resumeManager()
			soGoneWithin(l.lostCwd)
			soPathsGone(l.lostOut, l.lostCwd, l.lostTmp, filepath.Dir(l.lostCwd))

			ran, err := os.ReadFile(l.ranIn)
			So(err, ShouldBeNil)
			So(strings.TrimSpace(string(ran)), ShouldEqual, l.lostCwd)
		})
	})
}

func TestKillingALostJobSparesTheRunThatReplacesIt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job the user kills while its death is still being confirmed", t, func() {
		l := newLostRun(ctx, t, "kill_lost_job_retry")

		defer l.stop(ctx)

		// this is the un-gated release: `wr kill` marks the job and releases it,
		// and a runner has the retry reserved, its working directory made and its
		// Cmd EXECUTING before the confirmation of the lost run comes back - and
		// before the manager has heard a single word from it. Once its Started
		// has been reported the job is off lost, so this window, and only this
		// window, is where a decision about the run before it can land.
		l.waitForDeadCheckWindow()

		reserved := l.killAndReserveTheJob(ctx)
		retryCwd, retryTmp, retryOutput := l.getOnWithTheRun(reserved)

		l.proceedManager()

		Convey("the confirmation is not carried out on the retry", func() {
			So(l.waitForKillDecision(), ShouldBeFalse)

			// killCalled is the half that ends a Cmd already running: it turns
			// the retry's next touch into a self-kill.
			So(l.reportedState(ctx), ShouldEqual, JobStateReserved)
			So(l.killCalledOnLiveJob(), ShouldBeFalse)

			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))

			l.soNoBehaviourRuns()
			soPathsExist(retryOutput, retryCwd, retryTmp)
			soPathsGone(l.ranIn)

			// and the retry still holds the reservation, so it gets to report
			// its Started - which a job released out from under it cannot.
			l.reportItStarted(reserved, os.Getpid())
			So(l.reportedState(ctx), ShouldEqual, JobStateRunning)
		})
	})
}

func TestKilledLostJobsReplacementIsStillWatched(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a killed lost job whose replacement run dies in its turn", t, func() {
		l := newLostRun(ctx, t, "kill_lost_job_no_hang")

		defer l.stop(ctx)

		l.waitForDeadCheckWindow()

		// the retry is started with a pid that has already exited, so it goes
		// silent the way a run killed by its node does.
		reserved := l.killAndReserveTheJob(ctx)
		retryCwd, _, _ := l.getOnWithTheRun(reserved)
		l.reportItStarted(reserved, exitedPid())

		l.proceedManager()
		So(l.waitForKillDecision(), ShouldBeFalse)
		l.resumeManager()

		Convey("the manager declares that run lost on its own account", func() {
			// carrying the killed run's Lost flag into the retry parks the job
			// for ever: ttrCallback refuses to re-mark an already-lost job, so
			// nothing is ever confirmed, nothing killed, and the job is neither
			// retried nor buried. Reaching a second dead-check at all is the
			// evidence that the retry was watched as a run of its own.
			l.waitForDeadCheckWindow()
			So(l.waitForKillDecision(), ShouldBeTrue)

			// and the run that really was abandoned is the one swept.
			soGoneWithin(retryCwd)
			soPathsExist(l.lostCwd)
		})
	})
}

func TestKilledLostJobsReservationIsARunOfItsOwn(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job killed and then taken on by a runner that dies before Started", t, func() {
		l := newLostRun(ctx, t, "kill_lost_job_reserved")

		defer l.stop(ctx)

		l.waitForDeadCheckWindow()

		// the reservation IS the new run, and everything its runner does before
		// its Started - the working directory, the mounts, the Cmd itself - it
		// does inside this window. So the manager has to see a run of its own
		// here, rather than the run it lost.
		l.killAndReserveTheJob(ctx)
		So(l.reportedState(ctx), ShouldEqual, JobStateReserved)

		// and this runner is killed by its node before it ever calls Started, so
		// silence is the only thing the manager will ever hear about the run.
		l.runnerDied()

		l.proceedManager()
		So(l.waitForKillDecision(), ShouldBeFalse)
		l.resumeManager()

		Convey("the manager declares that run lost on its own account and ends it", func() {
			// carrying the killed run's Lost flag into this one parks the job for
			// ever: ttrCallback refuses to re-mark an already-lost job, so no
			// confirmation is ever started for the run that is really happening,
			// and it is neither retried nor buried.
			l.waitForDeadCheckWindow()
			So(l.waitForKillDecision(), ShouldBeTrue)

			l.soLeavesRunQueueWithin()
		})
	})
}

// startedRun is a real manager with one real job in it, reserved and started
// through the real client, with the default TTR so that nothing expires under a
// test. It is the fixture for the checks about what a START does, rather than
// about a loss.
type startedRun struct {
	server *Server
	item   *queue.Item
	live   *Job

	// key is the job's key and actualCwd the working directory its first run
	// made and reported, the way Client.Execute does: resolveWorkingDir before
	// Started.
	key       string
	actualCwd string
}

// newStartedRun builds the fixture. The manager and client are torn down with
// the test.
func newStartedRun(ctx context.Context, t *testing.T, rg, cwd string, bs Behaviours) *startedRun {
	t.Helper()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	t.Cleanup(func() { server.Stop(ctx, true) })

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	t.Cleanup(func() { disconnect(jq) })

	job := &Job{
		Cmd: restFormTrue, Cwd: cwd, RepGroup: rg, ReqGroup: rg,
		Requirements: standardReqs, Retries: 3, Behaviours: bs,
	}

	_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)

	r := &startedRun{server: server, key: reserved.Key()}

	r.actualCwd, _, err = mkHashedDir(cwd, r.key)
	So(err, ShouldBeNil)

	reserved.Lock()
	reserved.setActualCwd(r.actualCwd)
	reserved.Unlock()

	So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

	r.item, err = server.q.Get(r.key)
	So(err, ShouldBeNil)

	live, ok := r.item.Data().(*Job)
	So(ok, ShouldBeTrue)

	r.live = live

	return r
}

// liveActualCwd is the working directory the manager currently believes the job
// is running in.
func (r *startedRun) liveActualCwd() string {
	r.live.RLock()
	defer r.live.RUnlock()

	return r.live.ActualCwd
}

// liveHostID is the scheduler's name for the machine the manager currently
// believes the job is running on.
func (r *startedRun) liveHostID() string {
	r.live.RLock()
	defer r.live.RUnlock()

	return r.live.HostID
}

// ranOnCloudServer gives the job the HostID a run on a cloud server gets at its
// Started. The local scheduler names no host, so this is the only way to have
// one to be stale about.
func (r *startedRun) ranOnCloudServer(id string) {
	r.live.Lock()
	r.live.HostID = id
	r.live.Unlock()
}

// markLost marks the manager's own *Job lost, as a TTR expiry does.
func (r *startedRun) markLost() {
	r.live.Lock()
	r.live.Lost = true
	r.live.Unlock()
}

// reserveAgain is the manager's own reserve-time reset of the shared *Job, which
// is what a runner taking the job on again does to it - and where the run it is
// taking on begins.
func (r *startedRun) reserveAgain() {
	r.server.resetJobForReservation(r.live, newTestClientID())
}

// newTestClientID is the id of a runner other than the one that had the job
// before.
func newTestClientID() uuid.UUID {
	clientID, err := uuid.NewV4()
	So(err, ShouldBeNil)

	return clientID
}

func TestKilledLostJobSparesItsSecondRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job the user had already killed, whose retry is lost in its turn", t, func() {
		r := newStartedRun(ctx, t, "killed_lost_run", t.TempDir(), nil)

		// a `wr kill`ed run goes silent, is declared lost, and has its details
		// pinned. This is the one lost-job path with no dead-check in it at all:
		// it simply waits ttrReleaseWait and releases.
		r.live.Lock()
		r.live.killCalled = true
		r.live.Lost = true
		r.live.Unlock()

		pin := r.live.pinBehaviours()

		// but that wait is long enough for the job to be released, reserved and
		// started again, and lost again. Same key, same *Job, and Lost is true
		// once more, so what tells the two runs apart is what the manager minted
		// for the second reservation.
		r.reserveAgain()
		So(r.server.applyJobStart(r.live, &Job{Pid: os.Getpid(), Host: localhost}), ShouldBeTrue)
		r.markLost()

		Convey("the release lands on neither run", func() {
			r.server.confirmOrReleaseLostJob(ctx, lostJobDetails{key: r.key, killCalled: true, pin: pin})

			So(r.item.Stats().State, ShouldEqual, queue.ItemStateRun)
		})
	})
}

func TestReservedRunDoesNotClaimThePreviousRunsHost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job whose first run was on a cloud server", t, func() {
		r := newStartedRun(ctx, t, "reserve_clears_host", t.TempDir(), nil)

		r.ranOnCloudServer("the-server-of-the-run-that-is-over")

		Convey("a fresh reservation stops it naming that server", func() {
			r.reserveAgain()

			// killJobsOnBadServers kills every running or lost job whose HostID
			// is a server the cloud scheduler has condemned, and it does so
			// through the same un-gated release `wr kill` uses. A run that has
			// yet to report where it is must not answer for where the run before
			// it was.
			So(r.liveHostID(), ShouldBeBlank)
		})
	})
}

func TestASlowStartedRunIsNotStillLost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a reservation declared lost before its Started arrived", t, func() {
		r := newStartedRun(ctx, t, "slow_start_recovers", t.TempDir(), nil)

		// a runner whose reserve-to-Started stretch outlasts the TTR - S3 mounts
		// retry for seconds, and a saturated socket delays the report itself - is
		// declared lost while its Cmd is starting, and pinned.
		r.reserveAgain()
		r.markLost()

		pin := r.live.pinBehaviours()

		Convey("its Started recovers it, so the confirmation of that loss is refused", func() {
			So(r.server.applyJobStart(r.live, &Job{Pid: os.Getpid(), Host: localhost}), ShouldBeTrue)

			// the run this pin names is the very run that has just reported in:
			// its reservation minted the token and its Started did not change it.
			// So taking the job off lost is the only thing left standing between
			// a confirmation of that loss and a Cmd that is running.
			released, err := r.server.killLostRun(ctx, pin)
			So(err, ShouldBeNil)
			So(released, ShouldBeFalse)
			So(r.item.Stats().State, ShouldEqual, queue.ItemStateRun)
		})
	})
}

func TestReservedRunDoesNotInheritThePreviousRunsWorkingDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job whose first run made and reported a working directory", t, func() {
		cwd := t.TempDir()
		ranIn := filepath.Join(cwd, "ran_in.txt")
		r := newStartedRun(ctx, t, "reserve_clears_cwd", cwd, Behaviours{{When: OnFailure, Do: Run, Arg: "pwd > " + ranIn}})

		So(r.liveActualCwd(), ShouldEqual, r.actualCwd)

		Convey("a fresh reservation stops it claiming that directory, before the new run reports anything", func() {
			r.reserveAgain()

			So(r.liveActualCwd(), ShouldBeBlank)

			// the retry's runner is now making its working directory, mounting
			// remote filesystems and starting the Cmd, and its Started reaches
			// the manager only after all of it. A TTR expiry anywhere in there
			// pins THIS run, and ActualCwd is what the pinned cleanup deletes and
			// what a pinned `run` executes in - so what it must not carry is an
			// OLDER workspace of the same job.
			r.markLost()

			pin := r.live.pinBehaviours()
			So(pin.workSpace.actualCwd, ShouldBeBlank)
			So(pin.trigger(false), ShouldNotBeNil)
			soPathsGone(ranIn)
			soPathsExist(r.actualCwd)

			// and a Started that reports no directory of its own - an older
			// runner, or a cwd_matters job carrying the ActualCwd that wr
			// v0.37.0|1 stored on one - does not put the old one back either.
			So(r.server.applyJobStart(r.live, &Job{Pid: os.Getpid(), Host: localhost}), ShouldBeTrue)
			So(r.liveActualCwd(), ShouldBeBlank)
		})
	})
}

func TestMintedRunTokenIsNeverTheRecoveredOne(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A run this manager never started is not the run any token it mints identifies", t, func() {
		// a running job recovered from the database after a crash carries the
		// zero token, because this manager minted nothing for it. Every token it
		// does mint has to be distinct from that, or the first run it starts
		// would answer to a pin taken of the recovered one.
		server := &Server{}
		recovered := &Job{Lost: true}

		So(recovered.isLostRunLocked(server.mintRunToken()), ShouldBeFalse)
		So(recovered.isLostRunLocked(0), ShouldBeTrue)
	})
}
