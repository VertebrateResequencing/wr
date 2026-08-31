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
	actualCwd, tmpDir, err := mkHashedDir(l.cwd, l.key)
	So(err, ShouldBeNil)
	So(actualCwd, ShouldNotEqual, l.lostCwd)

	output = writeFileIn(actualCwd, "partial.txt")

	applyLiveSnapshot(l.live, &JobEndState{Cwd: actualCwd})

	return actualCwd, tmpDir, output
}

// startRetryInWindow starts the job again as a second run, the way a runner
// does: it makes whatever that run works in, then its Started tells the manager
// (applyJobStart takes the job off lost), and only a manager with a web port
// goes on to learn the directory again from the run's touches.
//
// A cwd_matters retry works in the shared Cwd, so what it has part way through
// is a file beside the user's other ones rather than a directory of wr's.
//
// touched says whether the retry got as far as its first live Touch. A run's
// touches are ClientTouchInterval apart - 15 seconds by default - so a run that
// dies inside that interval, which is the commonest way a node kills a job, is
// one no touch was ever received for.
func (l *lostRun) startRetryInWindow(touched bool) (actualCwd, tmpDir, output string) {
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

func TestKilledLostJobSparesItsSecondRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a lost job the user had already killed, whose retry is lost in its turn", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		rg := "killed_lost_run"
		job := &Job{
			Cmd: restFormTrue, Cwd: t.TempDir(), RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 3,
		}

		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		key := reserved.Key()

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		live, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		// a `wr kill`ed run goes silent, is declared lost, and has its details
		// pinned. This is the one lost-job path with no dead-check in it at all:
		// it simply waits ttrReleaseWait and releases.
		live.Lock()
		live.killCalled = true
		live.Lost = true
		live.Unlock()

		pin := live.pinBehaviours()

		// but that wait is long enough for the job to be released, started
		// again, and lost again. Same key, same *Job, and Lost is true once
		// more, so what tells the two runs apart is what the manager minted for
		// each start.
		So(server.applyJobStart(live, &Job{Pid: os.Getpid(), Host: localhost}), ShouldBeTrue)

		live.Lock()
		live.Lost = true
		live.Unlock()

		Convey("the release lands on neither run", func() {
			server.confirmOrReleaseLostJob(ctx, lostJobDetails{key: key, killCalled: true, pin: pin})

			So(item.Stats().State, ShouldEqual, queue.ItemStateRun)
		})
	})
}
