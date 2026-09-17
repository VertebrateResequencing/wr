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
	// startDurabilityHoldWrite is how long the start write is held off disk. It
	// only has to outlast the Started() round trip an unfixed manager answered
	// immediately, so that the crash image below is taken with the write still
	// pending; a manager that waits for its own write answers just after it.
	startDurabilityHoldWrite = 250 * time.Millisecond

	// startDurabilityAckWait bounds how long we wait for Started() to return.
	startDurabilityAckWait = 30 * time.Second

	// startDurabilityRerunWait is how long a second run of the command is given
	// to appear before we conclude it did not happen.
	startDurabilityRerunWait = 5 * time.Second

	// startDurabilityPollTime is how often the marker file is re-read.
	startDurabilityPollTime = 20 * time.Millisecond
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
			Cmd: cmd, Cwd: testCwd, RepGroup: "start_durability", ReqGroup: "start_durability",
			Requirements: standardReqs,
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
		// cannot reach disk, then release it after the grace period so a manager
		// that waits for its own write can still answer.
		holdTx, err := server.db.bolt.Begin(true)
		So(err, ShouldBeNil)

		go func() {
			time.Sleep(startDurabilityHoldWrite)

			_ = holdTx.Rollback() //nolint:errcheck // releasing the lock is the only point
		}()

		started := make(chan error, 1)

		go func() {
			started <- jq.Started(reserved, first.Process.Pid)
		}()

		select {
		case err = <-started:
		case <-time.After(startDurabilityAckWait):
			err = errStartNeverReturned
		}

		So(err, ShouldBeNil)

		// the crash image: the manager's committed on-disk state at the instant the
		// runner was told its start had been recorded.
		crashImage := &bytes.Buffer{}
		So(server.BackupDB(crashImage), ShouldBeNil)

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
