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
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

// This file covers .docs/bugfixes/260928-reserve-durability.md: a runner starts
// its command on the strength of its reservation alone, before its Started()
// reaches the manager, and the manager wrote nothing at reservation. A manager
// that crashed between handing out a job and durably recording its start came
// back reading the job's Add-time record, put it on the ready queue with a
// ReservedBy that was not the live runner's, and so rejected that runner's
// start, touch ("bad job") and archive ("you must Reserve() a Job") while
// handing the job to a fresh runner: the double run the production-shaped soak
// prodsim-1790539118 recorded after its 00:07:53 crash-restart.
//
// The crash image is bolt's consistent snapshot of committed state taken the
// instant Reserve() returns, so nothing here depends on a timer: a manager that
// answers a reservation before it is on disk is snapshotted without it.

const (
	// reserveDurabilityRepGroup names the one job these tests add.
	reserveDurabilityRepGroup = "reserve_durability"

	// reserveDurabilityFirstRunWait bounds how long the pre-crash run of the
	// command is given to record itself.
	reserveDurabilityFirstRunWait = 30 * time.Second

	// reserveDurabilityHost is the runner host given to recoversIntoRun.
	reserveDurabilityHost = "host"
)

// reserveDurabilityCrash is what reserveDurabilityCrashAndRecover leaves behind:
// a recovered manager, the job the pre-crash runner was handed, and a client
// that is that runner reconnected (the same client id, as quickReconnect keeps).
type reserveDurabilityCrash struct {
	server       *Server
	serverConfig ServerConfig
	addr         string
	token        []byte
	connectTime  time.Duration
	reserved     *Job
	runner       *Client
}

// reserveDurabilityCrashAndRecover adds cmd, reserves it, snapshots the manager's
// committed state the moment the reservation is handed out, calls startRun (the
// runner starting its command while the manager is going down), crashes the
// manager onto that snapshot, and restarts it with the given TTR.
func reserveDurabilityCrashAndRecover(ctx context.Context, t *testing.T, cmd string,
	ttrAfter time.Duration, startRun func(),
) *reserveDurabilityCrash {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

	// a long TTR before the crash, so nothing marks the job lost during setup.
	serverConfig.Timings.ItemTTR = time.Minute

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	job := &Job{
		Cmd: cmd, Cwd: testCwd, RepGroup: reserveDurabilityRepGroup,
		ReqGroup: reserveDurabilityRepGroup, Requirements: standardReqs, Retries: 3,
	}
	inserts, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)

	crashImage := &bytes.Buffer{}
	So(server.BackupDB(crashImage), ShouldBeNil)

	startRun()

	clientID := jq.clientid

	server.Stop(ctx, true)
	disconnect(jq)

	So(os.WriteFile(serverConfig.DBFile, crashImage.Bytes(), 0o600), ShouldBeNil)

	serverConfig.dontWipeDevDB = true
	serverConfig.Timings.ItemTTR = ttrAfter

	server, _, token, err = serve(ctx, serverConfig)
	So(err, ShouldBeNil)
	So(waitUntilRecovered(server), ShouldBeTrue)

	runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	runner.clientid = clientID

	return &reserveDurabilityCrash{
		server: server, serverConfig: serverConfig, addr: addr, token: token,
		connectTime: clientConnectTime, reserved: reserved, runner: runner,
	}
}

// connect returns a fresh client of the recovered manager: a different runner.
func (c *reserveDurabilityCrash) connect() *Client {
	jq, err := Connect(c.addr, c.serverConfig.CAFile, c.serverConfig.CertDomain, c.token, c.connectTime)
	So(err, ShouldBeNil)

	return jq
}

// TestReserveDurability proves that a job handed to a runner just before the
// manager crashed is neither handed to another runner by the recovered manager
// nor refused to its own runner, which is still running it.
func TestReserveDurability(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager that crashed after handing out a job, while its runner started the command", t, func() {
		config, _, _, _, _ := jobqueueTestInit(false)

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")
		cmd := startDurabilityCmd(marker, stopFile)

		var first *exec.Cmd

		crash := reserveDurabilityCrashAndRecover(ctx, t, cmd, time.Minute, func() {
			first = exec.CommandContext(ctx, config.RunnerExecShell, "-c", cmd) //nolint:gosec // test-authored command
			So(first.Start(), ShouldBeNil)
			So(waitForRuns(marker, 1, reserveDurabilityFirstRunWait), ShouldBeTrue)
		})

		defer crash.server.Stop(ctx, true)
		defer disconnect(crash.runner)

		defer func() {
			_ = os.WriteFile(stopFile, nil, 0o600) //nolint:errcheck // best-effort test cleanup
			_ = first.Wait()                       //nolint:errcheck // best-effort test cleanup
		}()

		Convey("a fresh runner is not given the job, and the original runner's reports are accepted", func() {
			jq2 := crash.connect()
			defer disconnect(jq2)

			recovered, errg := jq2.GetByRepGroup(reserveDurabilityRepGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(recovered), ShouldEqual, 1)

			recoveredState := recovered[0].State

			// what a runner the recovered manager scheduled would do.
			second, errr := jq2.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)

			if second != nil {
				go func() {
					_ = jq2.Execute(ctx, second, config.RunnerExecShell) //nolint:errcheck // the run count is the assertion
				}()
			}

			rerun := waitForRuns(marker, 2, startDurabilityRerunWait)

			So(runCount(marker), ShouldEqual, 1)
			So(rerun, ShouldBeFalse)
			So(first.Process.Signal(syscall.Signal(0)), ShouldBeNil)
			So(second, ShouldBeNil)
			So(recoveredState, ShouldEqual, JobStateReserved)

			// the original runner reconnects and carries on: its (retried) start,
			// its touches and finally its archive are all accepted.
			So(crash.runner.Started(crash.reserved, first.Process.Pid), ShouldBeNil)

			killCalled, errt := crash.runner.Touch(crash.reserved)
			So(errt, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			So(os.WriteFile(stopFile, nil, 0o600), ShouldBeNil)
			So(first.Wait(), ShouldBeNil)

			So(crash.runner.Archive(crash.reserved, &JobEndState{
				Exited: true, Exitcode: 0, EndTime: time.Now(),
			}), ShouldBeNil)

			done, errg := jq2.GetByRepGroup(reserveDurabilityRepGroup, false, 0, "", true, false)
			So(errg, ShouldBeNil)
			So(len(done), ShouldEqual, 1)
			So(done[0].State, ShouldEqual, JobStateComplete)
			So(runCount(marker), ShouldEqual, 1)
		})
	})
}

// TestReserveDurabilityDeadRunner proves the job is not parked for ever when the
// runner it was handed to died with the manager: once its TTR lapses and its
// runner is confirmed dead, it is re-run.
func TestReserveDurabilityDeadRunner(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const ttr = time.Second

	ctx := context.Background()

	Convey("Given a manager that crashed after handing out a job whose runner then died", t, func() {
		crash := reserveDurabilityCrashAndRecover(ctx, t, restFormTrue+" reservedurable", ttr, func() {})

		defer crash.server.Stop(ctx, true)
		defer disconnect(crash.runner)

		crash.server.SetLostJobCheckTimeout(2 * time.Second)
		crash.server.SetLostJobCheckRetryTime(200 * time.Millisecond)

		key := crash.reserved.Key()

		Convey("it is confirmed dead after its TTR and handed to a new runner", func() {
			// the reservation recorded the runner's own pid; make it a dead one.
			So(setServerJobPid(crash.server, key, definitelyDeadPid(t)), ShouldBeTrue)
			So(waitForJobLost(crash.server, key, 20*ttr), ShouldBeTrue)

			item, errg := crash.server.q.Get(key)
			So(errg, ShouldBeNil)

			group := item.ReserveGroup

			jq2 := crash.connect()
			defer disconnect(jq2)

			var reReserved *Job

			deadline := time.Now().Add(20 * ttr)
			for time.Now().Before(deadline) && reReserved == nil {
				reReserved, _ = jq2.ReserveScheduled(200*time.Millisecond, group) //nolint:errcheck // retried until the deadline
			}

			So(reReserved, ShouldNotBeNil)
			So(reReserved.Key(), ShouldEqual, key)
		})
	})
}

// TestRecoversIntoRun pins which recovered jobs go back into the run sub-queue:
// a reservation with no runner pid could never be confirmed dead there, so it is
// recovered onto the ready queue as before rather than parked for ever.
func TestRecoversIntoRun(t *testing.T) {
	Convey("recoversIntoRun puts running jobs and pid-bearing reservations into Run", t, func() {
		So(recoversIntoRun(&Job{State: JobStateRunning}), ShouldBeTrue)
		So(recoversIntoRun(&Job{State: JobStateReserved, Host: reserveDurabilityHost, Pid: 1}), ShouldBeTrue)
		So(recoversIntoRun(&Job{State: JobStateReserved, Host: reserveDurabilityHost}), ShouldBeFalse)
		So(recoversIntoRun(&Job{State: JobStateReserved, Pid: 1}), ShouldBeFalse)
		So(recoversIntoRun(&Job{State: JobStateReady}), ShouldBeFalse)
		So(recoversIntoRun(&Job{State: JobStateDelayed, Host: reserveDurabilityHost, Pid: 1}), ShouldBeFalse)
	})
}

// TestReserveDurabilityStalledWrite proves a reservation whose write cannot reach
// disk is still answered within ReserveWriteWait, well inside the client's request
// timeout, so the runner gets the job instead of timing out and leaving it
// reserved to nobody; and that the job then runs once.
func TestReserveDurabilityStalledWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		bound  = 300 * time.Millisecond
		margin = 2 * time.Second

		// how long the write is stalled: long past bound+margin, so a manager
		// that waits for the commit answers too late.
		stall = 5 * time.Second
	)

	ctx := context.Background()

	Convey("Given a manager whose reservation write is stalled past ReserveWriteWait", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ReserveWriteWait = bound

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		marker := filepath.Join(t.TempDir(), "runs")

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo run >> " + marker, Cwd: testCwd, RepGroup: reserveDurabilityRepGroup,
			ReqGroup: reserveDurabilityRepGroup, Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		logs := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		// holding bolt's single write transaction stalls every best-effort drain.
		holdTx, err := server.db.bolt.Begin(true)
		So(err, ShouldBeNil)

		released := make(chan struct{})
		timer := time.AfterFunc(stall, func() {
			_ = holdTx.Rollback() //nolint:errcheck // releasing the stall

			close(released)
		})

		start := time.Now()
		reserved, errr := jq.Reserve(2 * time.Second)
		elapsed := time.Since(start)

		if timer.Stop() {
			So(holdTx.Rollback(), ShouldBeNil)
		} else {
			<-released
		}

		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(elapsed, ShouldBeGreaterThanOrEqualTo, bound)
		So(elapsed, ShouldBeLessThan, bound+margin)
		So(strings.Contains(logs.String(), "reservation not yet recorded on disk"), ShouldBeTrue)

		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)
		So(runCount(marker), ShouldEqual, 1)

		again, errr := jq.Reserve(200 * time.Millisecond)
		So(errr, ShouldBeNil)
		So(again, ShouldBeNil)
	})
}

// TestBestEffortDrainKeepsArrivalOrder proves that when one drain holds a job's
// exit ops, full change and run state, recovery sees whichever arrived last, as
// it did when every write was a full record, and that an exit op whose live
// write is superseded still stores its std. Before, changes were always written
// first, so a release queued before the next reservation overwrote it, and the
// job recovered to ready. A reservation or start is queued either as a full
// change or as a run state; a release is an exit op; a kick and a suspend are
// full changes.
func TestBestEffortDrainKeepsArrivalOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, tc := range drainOrderCases() {
		t.Run(tc.name, func(t *testing.T) {
			database := openReliable4WriteStormDB(t, ctx)
			defer func() { _ = database.close(ctx) }()

			job := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]
			key := job.Key()
			ops := newDrainOrderOps(database, job)

			if len(tc.prior) > 0 {
				ops.queue(t, tc.prior)
				database.drainBestEffort(ctx)

				if runStateBucketValue(t, database, bucketJobRunState, key) == nil {
					t.Fatal("the earlier drain committed no run-state record")
				}
			}

			if tc.archived {
				deleteDrainOrderLiveRecord(t, database, key)
			}

			ops.queue(t, tc.order)
			database.drainBestEffort(ctx)

			tc.check(t, database, ops, job)
		})
	}
}

// drainOrderCase is one TestBestEffortDrainKeepsArrivalOrder case: ops
// committed by an earlier drain, whether the job's live record is then deleted,
// and the ops queued for the one drain under test, in arrival order.
type drainOrderCase struct {
	name     string
	prior    []string
	archived bool
	order    []string
	want     drainOrderWant
}

// drainOrderWant is what a drainOrderCase must leave: the recovered job's State,
// whether a run-state record exists, and any further check of the recovered job.
type drainOrderWant struct {
	state    JobState
	runState bool
	more     func(t *testing.T, ops *drainOrderOps, recovered *Job)
}

// The ops of TestBestEffortDrainKeepsArrivalOrder.
const (
	drainRelease         = "release"
	drainReservation     = "reservation"
	drainFullReservation = "full reservation"
	drainKick            = "kick"
	drainStart           = "start"
	drainSuspend         = "suspend"
)

// drainOrderStdE is the stderr a drainRelease stores.
const drainOrderStdE = "failed run's stderr"

// drainOrderCases returns the cases of TestBestEffortDrainKeepsArrivalOrder,
// spec.md A3 tests 1-9 and 12-14.
func drainOrderCases() []drainOrderCase {
	reservedExitcode := func(t *testing.T, _ *drainOrderOps, recovered *Job) {
		t.Helper()

		if recovered.Exitcode != -1 {
			t.Errorf("recovered Exitcode is %d, want -1", recovered.Exitcode)
		}
	}

	kicked := func(t *testing.T, _ *drainOrderOps, recovered *Job) {
		t.Helper()

		if recovered.UntilBuried != 3 {
			t.Errorf("recovered UntilBuried is %d, want 3", recovered.UntilBuried)
		}
	}

	return []drainOrderCase{
		{name: "a release then a reservation leaves the reservation",
			order: []string{drainRelease, drainReservation},
			want: drainOrderWant{state: JobStateReserved, runState: true,
				more: func(t *testing.T, ops *drainOrderOps, recovered *Job) {
					t.Helper()
					reservedExitcode(t, ops, recovered)

					live := runStateBucketValue(t, ops.database, bucketJobsLive, recovered.Key())
					if !bytes.Equal(live, ops.releaseEncoded) {
						t.Error("the live record is not the release's encoding")
					}
				}}},
		{name: "a reservation then a release leaves the release",
			order: []string{drainReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a release, a reservation and a release leaves the last release",
			order: []string{drainRelease, drainReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a kick then a reservation leaves the reservation over the kick",
			order: []string{drainKick, drainReservation},
			want: drainOrderWant{state: JobStateReserved, runState: true,
				more: func(t *testing.T, ops *drainOrderOps, recovered *Job) {
					t.Helper()
					kicked(t, ops, recovered)

					live := runStateBucketValue(t, ops.database, bucketJobsLive, recovered.Key())
					if !bytes.Equal(live, ops.kickEncoded) {
						t.Error("the live record is not the kick's encoding")
					}
				}}},
		{name: "a reservation then a kick leaves the kick",
			order: []string{drainReservation, drainKick},
			want:  drainOrderWant{state: JobStateReady, more: kicked}},
		{name: "a reservation then a start leaves the start",
			order: []string{drainReservation, drainStart},
			want: drainOrderWant{state: JobStateRunning, runState: true,
				more: func(t *testing.T, _ *drainOrderOps, recovered *Job) {
					t.Helper()

					if recovered.Pid != 7 {
						t.Errorf("recovered Pid is %d, want 7", recovered.Pid)
					}
				}}},
		{name: "a full change after a committed run state supersedes it",
			prior: []string{drainReservation}, order: []string{drainSuspend},
			want: drainOrderWant{state: JobStateSuspended}},
		{name: "a release after a committed run state supersedes it",
			prior: []string{drainReservation}, order: []string{drainRelease},
			want: drainOrderWant{state: JobStateDelayed}},
		{name: "a reservation of an archived job writes nothing",
			archived: true, order: []string{drainReservation}},
		{name: "a kick then a release leaves the release",
			order: []string{drainKick, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a release then a kick leaves the kick",
			order: []string{drainRelease, drainKick},
			want:  drainOrderWant{state: JobStateReady, more: kicked}},
		{name: "a release then a full reservation leaves the reservation",
			order: []string{drainRelease, drainFullReservation},
			want:  drainOrderWant{state: JobStateReserved}},
		{name: "a full reservation then a release leaves the release",
			order: []string{drainFullReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a release, a full reservation and a release leaves the last release",
			order: []string{drainRelease, drainFullReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
	}
}

// check asserts what the drains of tc left for job: its run-state record,
// stored stderr and recovered state, or for an archived job that neither of its
// records exists. The run-state record is checked before recovery, which drops
// a stale one.
func (tc drainOrderCase) check(t *testing.T, database *db, ops *drainOrderOps, job *Job) {
	t.Helper()

	key := job.Key()
	runState := runStateBucketValue(t, database, bucketJobRunState, key)

	if tc.archived {
		if live := runStateBucketValue(t, database, bucketJobsLive, key); live != nil || runState != nil {
			t.Errorf("an archived job has a live record (%t) or a run-state record (%t)", live != nil, runState != nil)
		}

		return
	}

	if got := runState != nil; got != tc.want.runState {
		t.Errorf("run-state record present is %t, want %t", got, tc.want.runState)
	}

	if slices.Contains(tc.order, drainRelease) {
		if got := storedStdE(t, database, key); string(got) != drainOrderStdE {
			t.Errorf("stored stderr is %q, want %q", got, drainOrderStdE)
		}
	}

	if got := storedLiveJobState(t, database, key); got != tc.want.state {
		t.Errorf("stored live state is %q, want %q", got, tc.want.state)
	}

	recovered := recoveredJob(t, database, key)
	if recovered.State != tc.want.state {
		t.Errorf("recovered state is %q, want %q", recovered.State, tc.want.state)
	}

	if tc.want.more != nil {
		tc.want.more(t, ops, recovered)
	}
}

// deleteDrainOrderLiveRecord deletes key's live record, as an archive does.
func deleteDrainOrderLiveRecord(t *testing.T, database *db, key string) {
	t.Helper()

	if err := database.bolt.Update(func(tx *bolt.Tx) error {
		return deleteLiveRecord(tx, []byte(key))
	}); err != nil {
		t.Fatalf("could not delete the live record: %v", err)
	}
}

// recoveredJob returns the job with key that recoverIncompleteJobs recovers.
func recoveredJob(t *testing.T, database *db, key string) *Job {
	t.Helper()

	recovered, _, err := database.recoverIncompleteJobs()
	if err != nil {
		t.Fatalf("recoverIncompleteJobs failed: %v", err)
	}

	for _, job := range recovered {
		if job.Key() == key {
			return job
		}
	}

	t.Fatalf("job %s was not recovered", key)

	return nil
}

// drainOrderOps queues TestBestEffortDrainKeepsArrivalOrder's ops of one job,
// each encoding the job as it is at that moment, without kicking the writer.
type drainOrderOps struct {
	database       *db
	job            *Job
	kickEncoded    []byte
	releaseEncoded []byte
}

// newDrainOrderOps returns a drainOrderOps for job in database.
func newDrainOrderOps(database *db, job *Job) *drainOrderOps {
	return &drainOrderOps{database: database, job: job}
}

// queue queues each of order's ops in turn.
func (o *drainOrderOps) queue(t *testing.T, order []string) {
	t.Helper()

	job := o.job

	for _, op := range order {
		switch op {
		case drainRelease:
			job.State = JobStateDelayed
			job.Exitcode = 1
			queueUnkickedBestEffortExit(t, o.database, job, []byte(drainOrderStdE))
			o.releaseEncoded = o.encode(t)
		case drainReservation, drainFullReservation:
			job.State = JobStateReserved
			job.Exitcode = -1

			o.queueReservation(t, op == drainFullReservation)
		case drainKick:
			job.State = JobStateReady
			job.UntilBuried = 3
			queueUnkickedBestEffortChange(t, o.database, job)
			o.kickEncoded = o.encode(t)
		case drainStart:
			job.State = JobStateRunning
			job.Pid = 7
			queueUnkickedBestEffortRunState(t, o.database, job)
		case drainSuspend:
			job.State = JobStateSuspended
			queueUnkickedBestEffortChange(t, o.database, job)
		default:
			t.Fatalf("unknown op %q", op)
		}
	}
}

// queueReservation queues the job's reservation as a full change if full, or
// else as a run state.
func (o *drainOrderOps) queueReservation(t *testing.T, full bool) {
	t.Helper()

	if full {
		queueUnkickedBestEffortChange(t, o.database, o.job)

		return
	}

	queueUnkickedBestEffortRunState(t, o.database, o.job)
}

// encode returns the job encoded as queueUnkickedBestEffortChange encodes it.
func (o *drainOrderOps) encode(t *testing.T) []byte {
	t.Helper()

	var encoded []byte

	if err := codec.NewEncoderBytes(&encoded, o.database.ch).Encode(o.job); err != nil {
		t.Fatalf("could not encode the job: %v", err)
	}

	return encoded
}

// TestBestEffortChangeKeepsEncodeOrder proves that of two concurrent writes of
// one job, the one that encoded the newer state is what recovery sees, whether
// the newer write, the reservation, is a full record or a run state, and when
// the older write is the run state and a full change overtakes it. A kick that
// encoded the job as ready, and was then overtaken by the job's reservation,
// used to queue after that reservation and win the coalescing. The
// reservation's waiter was told its write had committed while the ready record
// replaced it, so a crash before Started recovered the job to run a second time.
func TestBestEffortChangeKeepsEncodeOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	kick := encodeOrderWrite{write: (*db).updateJobAfterChangeDurable}
	reserve := func(write func(database *db, job *Job) error) encodeOrderWrite {
		return encodeOrderWrite{change: func(job *Job) { job.State = JobStateReserved }, write: write}
	}

	for _, tc := range []struct {
		name         string
		older, newer encodeOrderWrite
	}{
		{"a full reservation", kick, reserve((*db).updateJobAfterChangeDurable)},
		{"a run-state reservation", kick, reserve((*db).updateJobRunStateDurable)},
		{"a run-state reservation with a bounded wait", kick, reserve(func(database *db, job *Job) error {
			return database.updateJobRunStateDurableWithin(job, serverReserveWriteWait())
		})},
		{"a full change overtaking a run-state reservation", reserve((*db).updateJobRunStateDurable),
			encodeOrderWrite{change: func(job *Job) {
				job.State = JobStateReady
				job.UntilBuried = 3
			}, write: (*db).updateJobAfterChangeDurable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testBestEffortChangeKeepsEncodeOrder(t, tc.older, tc.newer)
		})
	}
}

// encodeOrderWrite is one of TestBestEffortChangeKeepsEncodeOrder's writes: a
// change made to the job under its lock, if any, then the write of it.
type encodeOrderWrite struct {
	change func(job *Job)
	write  func(database *db, job *Job) error
}

// apply makes w's change to job, then writes it.
func (w encodeOrderWrite) apply(database *db, job *Job) error {
	if w.change != nil {
		job.Lock()
		w.change(job)
		job.Unlock()
	}

	return w.write(database, job)
}

// testBestEffortChangeKeepsEncodeOrder is TestBestEffortChangeKeepsEncodeOrder
// with older paused between encoding the job and queueing its write while newer
// changes and writes the job.
func testBestEffortChangeKeepsEncodeOrder(t *testing.T, older, newer encodeOrderWrite) {
	t.Helper()

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	job := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

	var paused atomic.Bool

	encoded, resume := make(chan struct{}), make(chan struct{})

	jobChangeEncodedHook = func() {
		if paused.CompareAndSwap(false, true) {
			close(encoded)
			<-resume
		}
	}
	defer func() { jobChangeEncodedHook = nil }()

	olderErr, newerErr := make(chan error, 1), make(chan error, 1)

	go func() { olderErr <- older.apply(database, job) }()

	<-encoded

	go func() { newerErr <- newer.apply(database, job) }()

	// if writes queue in the order they encoded, the newer write cannot finish
	// while the older is paused, so this only waits long enough for it to try.
	var errs []error

	select {
	case err := <-newerErr:
		t.Log("the newer write committed while the older was still unqueued")

		errs = append(errs, err)
	case <-time.After(time.Second):
	}

	close(resume)

	errs = append(errs, <-olderErr)
	if len(errs) == 1 {
		errs = append(errs, <-newerErr)
	}

	for _, err := range errs {
		if err != nil {
			t.Fatalf("durable write failed: %v", err)
		}
	}

	job.RLock()
	wantState, wantUntilBuried := job.State, job.UntilBuried
	job.RUnlock()

	if got := storedLiveJobState(t, database, job.Key()); got != wantState {
		t.Errorf("live record state is %q, want %q", got, wantState)
	}

	recovered := recoveredJob(t, database, job.Key())
	if recovered.State != wantState {
		t.Errorf("recovered state is %q, want %q", recovered.State, wantState)
	}

	if recovered.UntilBuried != wantUntilBuried {
		t.Errorf("recovered UntilBuried is %d, want %d", recovered.UntilBuried, wantUntilBuried)
	}
}

// storedStdE returns the stderr stored for key.
func storedStdE(t *testing.T, database *db, key string) []byte {
	t.Helper()

	var stde []byte

	err := database.bolt.View(func(tx *bolt.Tx) error {
		stde = bytes.Clone(tx.Bucket(bucketStdE).Get([]byte(key)))

		return nil
	})
	if err != nil {
		t.Fatalf("could not read the job's stderr: %v", err)
	}

	return stde
}

// queueUnkickedBestEffortExit queues job's exit op exactly as updateJobAfterExit
// does, but WITHOUT kicking the writer goroutine, so the caller's own
// drainBestEffort picks it up in the same batch as anything else queued so.
func queueUnkickedBestEffortExit(t *testing.T, database *db, job *Job, stde []byte) {
	t.Helper()

	database.Lock()
	defer database.Unlock()

	job.RLock()
	exit, err := database.snapshotJobExitLocked(job, nil, stde, false)
	job.RUnlock()

	if err != nil {
		t.Fatalf("could not snapshot the job's exit: %s", err)
	}

	database.updatingAfterJobExit.Add(1)

	database.wgMutex.Lock()
	defer database.wgMutex.Unlock()

	database.beMu.Lock()
	defer database.beMu.Unlock()

	database.enqueueExitLocked(exit, nil)
}

// storedLiveJobState decodes the state of the job stored under key in the live
// bucket, with a matching run-state record applied, as recovery would.
func storedLiveJobState(t *testing.T, database *db, key string) JobState {
	t.Helper()

	return storedLiveJob(t, database, key).State
}

// storedLiveJob decodes the job stored under key in the live bucket and applies
// key's run-state record if it matches, as recovery would.
func storedLiveJob(t *testing.T, database *db, key string) *Job {
	t.Helper()

	var job *Job

	err := database.bolt.View(func(tx *bolt.Tx) error {
		var errs error

		job, errs = storedLiveJobTx(tx, database, key)

		return errs
	})
	if err != nil {
		t.Fatalf("could not read the live job: %v", err)
	}

	return job
}

// storedLiveJobTx is storedLiveJob in tx: the job stored under key in the live
// bucket, with key's run-state record applied if it matches. A database without
// a run-state bucket has no record to apply.
func storedLiveJobTx(tx *bolt.Tx, database *db, key string) (*Job, error) {
	live := tx.Bucket(bucketJobsLive).Get([]byte(key))

	job := &Job{}
	if err := codec.NewDecoderBytes(live, database.ch).Decode(job); err != nil {
		return nil, err
	}

	var record []byte
	if bucket := tx.Bucket(bucketJobRunState); bucket != nil {
		record = bucket.Get([]byte(key))
	}

	runState, matches, err := database.decodeMatchingRunState(live, record)
	if matches && err == nil {
		runState.applyTo(job)
	}

	return job, err
}

// rewriteStoredLiveJob decodes the job stored under key through its run-state
// record, lets change alter it, and writes it back as key's full live record,
// deleting the run-state record in the same transaction, as putLiveRecord does
// for every full write.
func rewriteStoredLiveJob(database *db, key string, change func(*Job)) error {
	return database.bolt.Update(func(tx *bolt.Tx) error {
		job, err := storedLiveJobTx(tx, database, key)
		if err != nil {
			return err
		}

		change(job)

		encoded, err := database.encode(job)
		if err != nil {
			return err
		}

		return putLiveRecord(tx, []byte(key), encoded)
	})
}
