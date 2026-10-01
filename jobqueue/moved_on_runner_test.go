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

// This file covers .docs/bugfixes/260930-moved-on-runner.md: a job the manager
// believes is running, but whose runner has moved on to other jobs because its
// report of the job's end was lost, is released as soon as that runner reserves,
// starts or touches another job, without an ssh check and without killing
// anything. Before, confirm-dead could never declare it dead while the runner
// lived, so it stayed running until the 1h backstop killed the runner and the
// unrelated job it was then running.
//
// The lost report is simulated by the runner never sending it: the manager is
// then in exactly the state a report acknowledged and then lost to a crash
// leaves behind.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	movedOnRepGroup = "moved_on_runner"
	movedOnRetries  = 3

	// movedOnReleaseDelay keeps a released job delayed for the whole test.
	movedOnReleaseDelay = time.Minute

	// movedOnReserveWait bounds each of the runner's reserves.
	movedOnReserveWait = 5 * time.Second

	// movedOnSchedulerID is the LSF element the runner says it runs in.
	movedOnSchedulerID = "1234[5]"
)

// movedOnJob is what the tests check of the manager's job.
type movedOnJob struct {
	State       JobState
	FailReason  string
	UntilBuried uint8
}

// movedOnFixture is a manager with jobs added by a user, and one runner client
// that reserves them one at a time. cmdPid is a live process standing in for
// every command the runner reports started, and cmdExited is closed if it dies.
type movedOnFixture struct {
	t            *testing.T
	server       *Server
	serverConfig ServerConfig
	addr         string
	token        []byte
	connectTime  time.Duration
	reqs         *jqs.Requirements
	group        string
	runner       *Client
	user         *Client
	cmdPid       int
	cmdExited    chan struct{}
}

// newMovedOnFixture starts a manager with the given runner command (none for a
// manager whose clients are Go API users), and a runner client. With a runner
// command the runner reserves as a wr runner under LSF does, by scheduler group
// and naming its scheduler element.
func newMovedOnFixture(ctx context.Context, t *testing.T, runnerCmd string) *movedOnFixture {
	t.Helper()

	_, serverConfig, addr, standardReqs, connectTime := startDurabilityConfig(t)
	serverConfig.Timings.ItemTTR = time.Minute
	serverConfig.Timings.ReleaseDelayMin = movedOnReleaseDelay
	// nothing touches the jobs left running, so a stop need not wait for that.
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond
	serverConfig.RunnerCmd = runnerCmd

	f := &movedOnFixture{
		t: t, serverConfig: serverConfig, addr: addr, connectTime: connectTime, reqs: standardReqs,
	}

	if runnerCmd != "" {
		f.group = schedulerGroupString(reqForScheduler(standardReqs), nil)
	}

	f.startCommand()
	f.serve(ctx)

	return f
}

// startCommand starts the live process whose pid the runner reports for every
// command it starts.
func (f *movedOnFixture) startCommand() {
	cmd := exec.CommandContext(context.Background(), "sleep", "600")
	So(cmd.Start(), ShouldBeNil)

	f.cmdPid = cmd.Process.Pid
	f.cmdExited = make(chan struct{})

	go func() {
		_ = cmd.Wait() //nolint:errcheck // killed by the cleanup below

		close(f.cmdExited)
	}()

	f.t.Cleanup(func() {
		_ = cmd.Process.Kill() //nolint:errcheck // may have exited already
	})
}

// serve starts the manager on the fixture's config and connects a user and a
// runner to it.
func (f *movedOnFixture) serve(ctx context.Context) {
	server, _, token, err := serve(ctx, f.serverConfig)
	So(err, ShouldBeNil)
	So(waitUntilRecovered(server), ShouldBeTrue)

	f.server, f.token = server, token
	f.user = f.connect()
	f.runner = f.connect()

	if f.group != "" {
		f.runner.SetReserveSchedulerID(movedOnSchedulerID)
	}
}

// connect returns a new client of the manager.
func (f *movedOnFixture) connect() *Client {
	jq, err := Connect(f.addr, f.serverConfig.CAFile, f.serverConfig.CertDomain, f.token, f.connectTime)
	So(err, ShouldBeNil)

	return jq
}

// stop disconnects the clients and stops the manager.
func (f *movedOnFixture) stop(ctx context.Context) {
	disconnect(f.user)
	disconnect(f.runner)
	f.server.Stop(ctx, true)
}

// add adds one job per name, all with the given retries and the same
// requirements, so they are in one scheduler group. Each job's extra is applied
// before it is added.
func (f *movedOnFixture) add(retries uint8, extra func(*Job), names ...string) {
	jobs := make([]*Job, 0, len(names))

	for _, name := range names {
		job := &Job{
			Cmd: restFormTrue + " movedon " + name, Cwd: testCwd, RepGroup: movedOnRepGroup,
			ReqGroup: movedOnRepGroup, Requirements: f.reqs.Clone(), Retries: retries,
		}

		if extra != nil {
			extra(job)
		}

		jobs = append(jobs, job)
	}

	inserts, _, err := f.user.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, len(names))
}

// reserve has the runner reserve its next job, as a wr runner does when the
// fixture has a runner command, and as a Go API client does otherwise.
func (f *movedOnFixture) reserve() *Job {
	var (
		job *Job
		err error
	)

	if f.group == "" {
		job, err = f.runner.Reserve(movedOnReserveWait)
	} else {
		job, err = f.runner.ReserveScheduled(movedOnReserveWait, f.group)
	}

	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)

	return job
}

// reserveAndStart reserves the runner's next job and reports it started.
func (f *movedOnFixture) reserveAndStart() *Job {
	job := f.reserve()
	So(f.runner.Started(job, f.cmdPid), ShouldBeNil)

	return job
}

// serverJob returns what the tests check of the manager's job for key, and its
// item's state.
func (f *movedOnFixture) serverJob(key string) (movedOnJob, queue.ItemState) {
	item, err := f.server.q.Get(key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	job.RLock()
	defer job.RUnlock()

	return movedOnJob{State: job.State, FailReason: job.FailReason, UntilBuried: job.UntilBuried}, item.Stats().State
}

// soReleasedAsLost asserts that the manager released the job for key as it
// releases a lost job confirmed dead: with a retry spent, to wait to run again
// or, with none left, buried. A job recovered after a crash has no delay left
// to wait, so it may be ready already.
func (f *movedOnFixture) soReleasedAsLost(key string, retries uint8) {
	job, itemState := f.serverJob(key)

	want := []JobState{JobStateDelayed, JobStateReady}
	wantItem := []queue.ItemState{queue.ItemStateDelay, queue.ItemStateReady}

	if retries == 0 {
		want, wantItem = []JobState{JobStateBuried}, []queue.ItemState{queue.ItemStateBury}
	}

	So(job.State, ShouldBeIn, want)
	So(itemState, ShouldBeIn, wantItem)
	So(job.FailReason, ShouldEqual, FailReasonLost)
	So(job.UntilBuried, ShouldEqual, initialUntilBuried(retries)-1)
}

// soStillRunning asserts that the manager still has the job for key running
// (or reserved, if it was never started).
func (f *movedOnFixture) soStillRunning(key string) {
	job, itemState := f.serverJob(key)
	So(job.State, ShouldBeIn, []JobState{JobStateRunning, JobStateReserved})
	So(itemState, ShouldEqual, queue.ItemStateRun)
}

// soCommandAlive asserts that nothing killed the process standing in for the
// runner's commands.
func (f *movedOnFixture) soCommandAlive() {
	select {
	case <-f.cmdExited:
		So("the command was killed", ShouldBeEmpty)
	default:
	}
}

// scheduledCount is the manager's count of runners wanted for the fixture's
// scheduler group.
func (f *movedOnFixture) scheduledCount() int {
	f.server.psgmutex.RLock()
	group, existed := f.server.previouslyScheduledGroups[f.group]
	f.server.psgmutex.RUnlock()

	if !existed {
		return 0
	}

	return group.getCount()
}

// backup returns an image of the manager's committed database.
func (f *movedOnFixture) backup() *bytes.Buffer {
	image := &bytes.Buffer{}
	So(f.server.BackupDB(image), ShouldBeNil)

	return image
}

// crashOnto stops the manager, replaces its database with image, as a crash
// would leave it, and starts it again. The runner reconnects with the same
// client id, having reserved before, as a runner that outlived the crash does.
func (f *movedOnFixture) crashOnto(ctx context.Context, image *bytes.Buffer) {
	clientID := f.runner.clientid

	f.stop(ctx)

	So(os.WriteFile(f.serverConfig.DBFile, image.Bytes(), 0o600), ShouldBeNil)
	f.serverConfig.dontWipeDevDB = true

	f.serve(ctx)

	f.runner.clientid = clientID
	f.runner.hasReserved = true
}

// TestMovedOnRunner proves that a job whose runner moved on to another job is
// released at once, as a lost job confirmed dead would be, when that runner
// reserves its next job.
func TestMovedOnRunner(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, retries := range []uint8{movedOnRetries, 0} {
		Convey("Given a runner that started a job, whose report of it never arrived", t, func() {
			f := newMovedOnFixture(ctx, t, serverRC)
			defer f.stop(ctx)

			f.add(retries, nil, "first", "second")
			first := f.reserveAndStart()

			Convey("its reserving another job releases the first at once, killing nothing", func() {
				second := f.reserve()

				f.soReleasedAsLost(first.Key(), retries)
				f.soStillRunning(second.Key())
				f.soCommandAlive()
			})
		})
	}

	Convey("Given a runner whose started job the manager has marked lost", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		f.server.SetItemTTR(300 * time.Millisecond)
		f.add(movedOnRetries, nil, "first", "second")
		first := f.reserveAndStart()
		So(waitForJobLost(f.server, first.Key(), 10*time.Second), ShouldBeTrue)

		Convey("its reserving another job releases the lost one at once, killing nothing", func() {
			f.reserve()

			f.soReleasedAsLost(first.Key(), movedOnRetries)
			f.soCommandAlive()
		})
	})
}

// TestMovedOnRunnerLeavesHeldJobs proves that the release only follows a wr
// runner's move to a newer job: a Go API client may hold many jobs at once,
// and a runner's own job, or a stale report of an older one, releases nothing.
func TestMovedOnRunnerLeavesHeldJobs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, runnerCmd := range []string{"", serverRC} {
		Convey("Given a Go API client that reserved and started two jobs, with runner command "+runnerCmd,
			t, func() {
				f := newMovedOnFixture(ctx, t, runnerCmd)
				defer f.stop(ctx)

				// a Go client may reserve by scheduler group, but names no
				// scheduler element.
				f.runner.SetReserveSchedulerID("")

				f.add(movedOnRetries, nil, "first", "second")
				first := f.reserveAndStart()
				second := f.reserveAndStart()

				Convey("touching the second leaves both running", func() {
					_, err := f.runner.Touch(second)
					So(err, ShouldBeNil)

					f.soStillRunning(first.Key())
					f.soStillRunning(second.Key())
				})
			})
	}

	Convey("Given a runner that reserved a job", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		f.add(movedOnRetries, nil, "only")
		job := f.reserve()

		Convey("starting and touching it leaves it running", func() {
			So(f.runner.Started(job, f.cmdPid), ShouldBeNil)

			_, err := f.runner.Touch(job)
			So(err, ShouldBeNil)

			f.soStillRunning(job.Key())
		})
	})
}

// TestMovedOnRunnerAfterRestart proves that the release also follows a manager
// crash, for a runner whose first request to the new manager is about a newer
// job than one the manager recovered as running under it.
func TestMovedOnRunnerAfterRestart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner's started job recovered running after a crash", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		f.add(movedOnRetries, nil, "first", "second")
		first := f.reserveAndStart()
		f.crashOnto(ctx, f.backup())
		f.soStillRunning(first.Key())

		Convey("the runner's reserving another job releases it", func() {
			f.reserve()

			f.soReleasedAsLost(first.Key(), movedOnRetries)
			f.soCommandAlive()
		})
	})

	Convey("Given a runner's two jobs recovered running after a crash, the second only reserved", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		f.add(movedOnRetries, nil, "first", "second")
		first := f.reserveAndStart()
		firstStarted := f.backup()
		second := f.reserve()
		f.crashOnto(ctx, withLiveRecord(t, f.backup(), firstStarted, first.Key()))
		f.soStillRunning(first.Key())
		f.soStillRunning(second.Key())

		Convey("the runner's start of the second releases the first", func() {
			So(f.runner.Started(second, f.cmdPid), ShouldBeNil)

			f.soReleasedAsLost(first.Key(), movedOnRetries)
			f.soStillRunning(second.Key())
			f.soCommandAlive()
		})

		Convey("the runner's touch of the second releases the first", func() {
			_, err := f.runner.Touch(second)
			So(err, ShouldBeNil)

			f.soReleasedAsLost(first.Key(), movedOnRetries)
			f.soStillRunning(second.Key())
		})

		Convey("a stale touch of the first releases neither", func() {
			_, err := f.runner.Touch(first)
			So(err, ShouldBeNil)

			f.soStillRunning(first.Key())
			f.soStillRunning(second.Key())
		})
	})
}

// withLiveRecord returns base with the live record of key replaced by the one
// in from.
func withLiveRecord(t *testing.T, base, from *bytes.Buffer, key string) *bytes.Buffer {
	t.Helper()

	dir := t.TempDir()
	basePath, fromPath := filepath.Join(dir, "base"), filepath.Join(dir, "from")
	So(os.WriteFile(basePath, base.Bytes(), 0o600), ShouldBeNil)
	So(os.WriteFile(fromPath, from.Bytes(), 0o600), ShouldBeNil)

	fromDB, err := bolt.Open(fromPath, 0o600, nil)
	So(err, ShouldBeNil)

	var record []byte

	So(fromDB.View(func(tx *bolt.Tx) error {
		record = bytes.Clone(tx.Bucket(bucketJobsLive).Get([]byte(key)))

		return nil
	}), ShouldBeNil)
	So(fromDB.Close(), ShouldBeNil)
	So(record, ShouldNotBeEmpty)

	baseDB, err := bolt.Open(basePath, 0o600, nil)
	So(err, ShouldBeNil)
	So(baseDB.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobsLive).Put([]byte(key), record)
	}), ShouldBeNil)
	So(baseDB.Close(), ShouldBeNil)

	composed, err := os.ReadFile(basePath)
	So(err, ShouldBeNil)

	return bytes.NewBuffer(composed)
}

// TestMovedOnRunnerLateReport proves that a report the runner makes of the
// released job after all is answered as for any job the manager released
// itself, and gives back no second runner's worth of the scheduler group.
func TestMovedOnRunnerLateReport(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner's job released because the runner reserved another", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		f.add(movedOnRetries, nil, "first", "second")
		first := f.reserveAndStart()
		f.reserve()
		f.soReleasedAsLost(first.Key(), movedOnRetries)

		counted := f.scheduledCount()

		Convey("the runner's late release is acknowledged without spending another retry", func() {
			So(f.runner.releaseAfterAttempt(first, releaseAfterLostEndState(), FailReasonExit), ShouldBeNil)

			job, _ := f.serverJob(first.Key())
			So(job.UntilBuried, ShouldEqual, initialUntilBuried(movedOnRetries)-1)
			So(f.scheduledCount(), ShouldEqual, counted)
		})

		// its scheduler group count is not checked: an archive of a job the
		// manager has released, whatever released it, gives back a runner again
		// (finishArchive), which is not this fix's to change.
		Convey("the runner's late archive completes it", func() {
			So(f.runner.Archive(first, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			_, live := busyExitState(f.server, first.Key())
			So(live, ShouldBeFalse)
			So(f.server.jobAlreadyComplete(first.Key()), ShouldBeTrue)
		})

		Convey("the runner's late bury buries it", func() {
			So(f.runner.Bury(first, releaseAfterLostEndState(), FailReasonExit), ShouldBeNil)

			job, itemState := f.serverJob(first.Key())
			So(job.State, ShouldEqual, JobStateBuried)
			So(itemState, ShouldEqual, queue.ItemStateBury)
			So(f.scheduledCount(), ShouldEqual, counted)
		})
	})
}

// TestMovedOnRunnerRerunAfterRun proves that a released job marked to run again
// once its run ends, because a dep group it depends on gained a member while it
// ran, waits on that member as any other release of it would.
func TestMovedOnRunnerRerunAfterRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner's job depending on a dep group that gained a member while it ran", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		const depGroup = "moved_on_dep_group"

		inDepGroup := func(job *Job) { job.DepGroups = []string{depGroup} }

		f.add(movedOnRetries, inDepGroup, "seed")
		seed := f.reserveAndStart()
		So(f.runner.Archive(seed, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		f.add(movedOnRetries, func(job *Job) {
			job.Dependencies = Dependencies{NewDepGroupDependency(depGroup)}
		}, "waiter")
		waiter := f.reserveAndStart()

		f.add(movedOnRetries, inDepGroup, "member")

		Convey("the runner's reserving the member releases the waiter to wait on it", func() {
			member := f.reserve()
			So(member.Key(), ShouldNotEqual, waiter.Key())

			job, itemState := f.serverJob(waiter.Key())
			So(itemState, ShouldEqual, queue.ItemStateDependent)
			So(job.FailReason, ShouldEqual, FailReasonLost)
			f.soCommandAlive()
		})
	})
}
