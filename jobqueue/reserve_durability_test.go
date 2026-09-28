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
	"strings"
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

// TestBestEffortDrainKeepsArrivalOrder proves that when one drain holds both an
// exit op and a change for the same job, the job's live record is whichever
// arrived last. Before, changes were always written first, so a release queued
// before the next reservation overwrote it, and the job recovered to ready.
func TestBestEffortDrainKeepsArrivalOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		exitFirst bool
		wantState JobState
	}{
		{"a release then a reservation leaves the reservation", true, JobStateReserved},
		{"a reservation then a release leaves the release", false, JobStateDelayed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := openReliable4WriteStormDB(t, ctx)
			defer func() { _ = database.close(ctx) }()

			job := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			// each queue call encodes the job as it is at that moment.
			queueRelease := func() {
				job.State = JobStateDelayed
				queueUnkickedBestEffortExit(t, database, job)
			}

			queueReservation := func() {
				job.State = JobStateReserved
				queueUnkickedBestEffortChange(t, database, job)
			}

			if tc.exitFirst {
				queueRelease()
				queueReservation()
			} else {
				queueReservation()
				queueRelease()
			}

			database.drainBestEffort(ctx)

			if got := storedLiveJobState(t, database, job.Key()); got != tc.wantState {
				t.Errorf("live record state is %q, want %q", got, tc.wantState)
			}
		})
	}
}

// queueUnkickedBestEffortExit queues job's exit op exactly as updateJobAfterExit
// does, but WITHOUT kicking the writer goroutine, so the caller's own
// drainBestEffort picks it up in the same batch as anything else queued so.
func queueUnkickedBestEffortExit(t *testing.T, database *db, job *Job) {
	t.Helper()

	database.Lock()
	defer database.Unlock()

	exit, ok := database.snapshotJobExit(context.Background(), job, nil, nil, false)
	if !ok {
		t.Fatal("could not snapshot the job's exit")
	}

	database.updatingAfterJobExit.Add(1)

	database.wgMutex.Lock()
	defer database.wgMutex.Unlock()

	database.beMu.Lock()
	defer database.beMu.Unlock()

	database.enqueueExitLocked(exit)
}

// storedLiveJobState decodes the job stored under key in the live bucket.
func storedLiveJobState(t *testing.T, database *db, key string) JobState {
	t.Helper()

	var state JobState

	err := database.bolt.View(func(tx *bolt.Tx) error {
		job := &Job{}
		if errd := codec.NewDecoderBytes(tx.Bucket(bucketJobsLive).Get([]byte(key)), database.ch).Decode(job); errd != nil {
			return errd
		}

		state = job.State

		return nil
	})
	if err != nil {
		t.Fatalf("could not read the live job: %v", err)
	}

	return state
}
