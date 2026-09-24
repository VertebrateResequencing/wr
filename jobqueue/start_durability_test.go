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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

// This file covers .docs/bugfixes/260917-start-durability.md: a manager that
// acknowledges a runner's Started() before the job's `running` state is on disk
// re-runs that job on restart, while the first run's command is still alive -
// the simultaneous double run DEVELOPERS.md rule 4 exists to prevent.
//
// The natural flake reproduces 0 times in 1,736 runs (the margin that decides it
// is the /proc sweep the signal test does between seeing its marker and killing
// the manager), so the window is opened deterministically instead: holding the
// single bbolt write lock keeps the queued start write off disk, exactly as a
// manager that dies before its next best-effort drain leaves it. The crash image
// is then bolt's own consistent snapshot of committed state, taken at the instant
// the runner was told its start had been recorded.

const (
	// startDurabilityHoldWrite is how long the start write is kept off disk before
	// the hold is released for a manager that is waiting on it. It only has to
	// outlast the Started() round trip a manager that does NOT wait answers in;
	// that ack is a local RPC and lands in about a millisecond, and a run where it
	// did not is caught by the ackedWhileWriteHeld assertion rather than passing
	// quietly.
	startDurabilityHoldWrite = 250 * time.Millisecond

	// startDurabilityAckWait bounds how long we wait for Started() to return.
	startDurabilityAckWait = 30 * time.Second

	// startDurabilityRerunWait is how long a second run of the command is given
	// to appear before we conclude it did not happen.
	startDurabilityRerunWait = 5 * time.Second

	// startDurabilityPollTime is how often the marker file is re-read.
	startDurabilityPollTime = 20 * time.Millisecond

	// startDurabilityRepGroup names the one job these tests add.
	startDurabilityRepGroup = "start_durability"
)

// errStartNeverReturned stands in for the Started() error when the call did not
// come back at all within startDurabilityAckWait.
var errStartNeverReturned = errors.New("Started() did not return")

// TestStartDurability proves that a job whose start the manager acknowledged is
// not run a second time after the manager crashes and recovers, while the first
// run's command is still alive.
func TestStartDurability(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager that acknowledged a job's start with the start write held off disk", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")
		cmd := startDurabilityCmd(marker, stopFile)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		job := &Job{
			Cmd: cmd, Cwd: testCwd, RepGroup: startDurabilityRepGroup,
			ReqGroup: startDurabilityRepGroup, Requirements: standardReqs,
		}
		inserts, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		// the first run: a runner starts the command before it reports the start,
		// and the command stays alive for the rest of the test.
		first := exec.CommandContext(ctx, config.RunnerExecShell, "-c", cmd) //nolint:gosec // test-authored command
		So(first.Start(), ShouldBeNil)

		// every run of cmd exits once stopFile exists, so this releases both the
		// first run and any second one before waiting for them.
		defer func() {
			_ = os.WriteFile(stopFile, nil, 0o600) //nolint:errcheck // best-effort test cleanup
			_ = first.Wait()                       //nolint:errcheck // best-effort test cleanup
		}()

		So(waitForRuns(marker, 1, startDurabilityRerunWait), ShouldBeTrue)

		// hold the single bbolt write lock, so the start write the manager queues
		// cannot reach disk.
		holdTx, err := server.db.bolt.Begin(true)
		So(err, ShouldBeNil)

		started := make(chan error, 1)

		go func() {
			started <- jq.Started(reserved, first.Process.Pid)
		}()

		// the crash image is the manager's committed on-disk state at the instant it
		// told the runner its start was recorded, so it is taken the moment the ack
		// arrives - BEFORE the hold is released if the ack beats it. Releasing first
		// would let the write land in an image the manager had already answered
		// without, which would turn the unfixed case green and prove nothing. The
		// timer release is only for a manager that waits for its own write: it cannot
		// answer at all until the commit it is waiting for is allowed to happen.
		crashImage := &bytes.Buffer{}

		var ackedWhileWriteHeld bool

		select {
		case err = <-started:
			ackedWhileWriteHeld = true

			So(server.BackupDB(crashImage), ShouldBeNil)
			So(holdTx.Rollback(), ShouldBeNil)
		case <-time.After(startDurabilityHoldWrite):
			So(holdTx.Rollback(), ShouldBeNil)

			select {
			case err = <-started:
			case <-time.After(startDurabilityAckWait):
				err = errStartNeverReturned
			}

			So(err, ShouldBeNil)
			So(server.BackupDB(crashImage), ShouldBeNil)
		}

		So(err, ShouldBeNil)

		t.Logf("start acknowledged while its write was held off disk: %t", ackedWhileWriteHeld)

		server.Stop(ctx, true)
		disconnect(jq)

		So(os.WriteFile(serverConfig.DBFile, crashImage.Bytes(), 0o600), ShouldBeNil)

		serverConfig.dontWipeDevDB = true

		server, _, token, err = serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		Convey("a fresh runner asking the recovered manager for work does not run the command again", func() {
			jq2, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq2)

			// what the recovered manager thinks of the job, read before anyone can
			// change it. Without this a manager that hands out no work for some
			// unrelated reason would satisfy the run count below while leaving the job
			// on the ready queue, which is the bug.
			recovered, errg := jq2.GetByRepGroup(startDurabilityRepGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(recovered), ShouldEqual, 1)

			recoveredState := recovered[0].State

			// exactly what a runner the recovered manager scheduled would do: ask for
			// work and run whatever it is given.
			second, errr := jq2.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)

			if second != nil {
				go func() {
					_ = jq2.Execute(ctx, second, config.RunnerExecShell) //nolint:errcheck // the run count is the assertion
				}()
			}

			// give a second run of the command time to record itself.
			rerun := waitForRuns(marker, 2, startDurabilityRerunWait)

			So(runCount(marker), ShouldEqual, 1)
			So(rerun, ShouldBeFalse)

			// and the one run there was is still alive, so a second one would have
			// been simultaneous.
			So(first.Process.Signal(syscall.Signal(0)), ShouldBeNil)

			// the recovered manager knows the job is running and offers it to nobody.
			So(recoveredState, ShouldEqual, JobStateRunning)
			So(second, ShouldBeNil)

			// last, because it is the backstop rather than the headline: on a box slow
			// enough for the Started() round trip to outlast the hold, the assertions
			// above would pass whatever the manager does, so this is what stops such a
			// run reporting a silent green.
			So(ackedWhileWriteHeld, ShouldBeFalse)
		})
	})
}

// startDurabilityConfig gives a test server its own database, database backup and
// token file under t.TempDir(), so the crash image this test writes over that
// database cannot reach any other test's manager directory.
func startDurabilityConfig(t *testing.T) (internal.Config, ServerConfig, string, *jqs.Requirements, time.Duration) {
	t.Helper()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	dir := t.TempDir()
	serverConfig.DBFile = filepath.Join(dir, "db")
	serverConfig.DBFileBackup = filepath.Join(dir, "db.bk")
	serverConfig.TokenFile = filepath.Join(dir, "token")

	return config, serverConfig, addr, standardReqs, clientConnectTime
}

// startDurabilityCmd is a command that records one line per run in marker and
// then stays alive until stopFile appears, so a second run of it overlaps the
// first.
func startDurabilityCmd(marker, stopFile string) string {
	return fmt.Sprintf("echo run >> %s; while [ ! -e %s ]; do sleep 0.05; done", marker, stopFile)
}

// runCount is how many runs of a startDurabilityCmd have recorded themselves in
// marker.
func runCount(marker string) int {
	content, err := os.ReadFile(marker)
	if err != nil {
		return 0
	}

	return len(strings.Fields(string(content)))
}

// waitForRuns reports whether marker records at least want runs within timeout.
func waitForRuns(marker string, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if runCount(marker) >= want {
			return true
		}

		time.Sleep(startDurabilityPollTime)
	}

	return runCount(marker) >= want
}

// TestStartDurabilityAbortedWriteIsNotCommitted covers the one path that would
// reintroduce the bug this file exists to close. db.bolt.Update rolls a panicking
// transaction back but does NOT recover it - applyArchiveOp exists precisely
// because bbolt.Batch's safelyCall did and Update does not - so a panic inside
// batch.apply unwinds through drainBestEffort's deferred reply carrying no error
// of its own. A waiter told nil there would read a rolled-back transaction as a
// write that reached disk, handleStart would acknowledge the start, and the
// restarted manager would re-run the live command.
//
// The drain runs on THIS goroutine, and the batch is queued without waking the
// writer, because bestEffortWriter's deferred internal.LogPanic calls os.Exit(1):
// a panicking drain over there would take the whole test binary down instead of
// failing one test. It is a plain test for the reason the sibling best-effort
// tests are (goconvey's whole-struct reflection would race the live writer).
func TestStartDurabilityAbortedWriteIsNotCommitted(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	job := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

	// dropping the live bucket leaves applyChanges dereferencing a nil
	// *bolt.Bucket, which is a panic raised inside the transaction body rather
	// than an error returned from it.
	if err := database.bolt.Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket(bucketJobsLive)
	}); err != nil {
		t.Fatalf("could not drop the live bucket: %v", err)
	}

	waiter := queueUnkickedBestEffortChange(t, database, job)

	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Error("the drain did not panic, so this test proves nothing")
			}
		}()

		database.drainBestEffort(ctx)
	}()

	select {
	case err := <-waiter:
		if !errors.Is(err, errBestEffortWriteAborted) {
			t.Errorf("a waiter on a rolled-back drain was told %v, want errBestEffortWriteAborted", err)
		}
	default:
		t.Error("a waiter on a rolled-back drain was never answered")
	}
}

// queueUnkickedBestEffortChange queues job's encoded live-bucket value and a
// waiter for it exactly as launchJobChangeUpdate does, but WITHOUT kicking the
// writer goroutine, so the caller's own drainBestEffort is certain to be what
// picks the batch up.
func queueUnkickedBestEffortChange(t *testing.T, database *db, job *Job) chan error {
	t.Helper()

	var encoded []byte

	if err := codec.NewEncoderBytes(&encoded, database.ch).Encode(job); err != nil {
		t.Fatalf("could not encode the job: %v", err)
	}

	waiter := make(chan error, 1)

	database.RLock()
	defer database.RUnlock()

	database.wgMutex.Lock()
	defer database.wgMutex.Unlock()

	database.beMu.Lock()
	defer database.beMu.Unlock()

	database.beChanges[job.Key()] = encoded
	database.beWGKeys = append(database.beWGKeys, database.wg.Add(1))
	database.beWaiters = append(database.beWaiters, waiter)

	return waiter
}
