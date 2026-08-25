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

// This file pins the operator-visible half of spec.md section B1's "lightweight
// progress reporting": on a DEFAULT `wr manager start` (no --debug) the manager
// log must say that prior-state recovery started, that it finished, and - while
// a long recovery is still running - that it is still working. Production spent
// 15+ minutes recovering 148,393 jobs with nothing in the log but client
// "server is recovering prior state" errors, which read as total job loss (see
// .docs/bugfixes/260825-1.md).
//
// The boundary asserted is therefore the manager LOG FILE, produced under
// production's own handler configuration (see managerLogContext): a line the
// server logs below warn on that context never reaches the file, so asserting
// on a captured buffer at debug level would prove nothing about what an
// operator sees. The expected message substrings are spelled out literally here
// rather than shared with server.go, because those literals are the contract -
// they are what an operator (and .docs/bugfixes/260825-1-red-recovery-
// visibility.sh) greps for.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// recoveryStartLogMatch, recoveryFinishLogMatch and recoveryStillLogMatch are
	// the three recovery lines an operator must be able to find in the manager
	// log at the default log level.
	recoveryStartLogMatch  = `msg="recovering prior state"`
	recoveryFinishLogMatch = `msg="recovering: prior state recovered"`
	recoveryStillLogMatch  = `msg="recovering: still recovering prior state"`

	// recoveryLogHeartbeatInterval is the shrunken heartbeat interval the
	// long-recovery test runs with, so a blocked recovery crosses it quickly.
	recoveryLogHeartbeatInterval = 50 * time.Millisecond

	// warmRestartRecovery is how long production's warm restart takes to recover
	// its ~148k live jobs. The production heartbeat interval has to be longer, or
	// every ordinary restart logs heartbeats.
	warmRestartRecovery = 40 * time.Second
)

// TestReliable4RecoveryVisibleInManagerLog covers the start and finish
// milestones, plus the requirement that the common fast recovery stays quiet: a
// warm restart restores ~148k jobs in ~40s, well inside
// recoveryHeartbeatInterval, and must not add heartbeat lines.
func TestReliable4RecoveryVisibleInManagerLog(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A default manager start logs prior-state recovery starting and finishing", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig = recoveryLogFixtureConfig(t, serverConfig)
		logPath, serverCtx := managerLogContext(t, ctx)

		server, _, _, err := serve(serverCtx, serverConfig)
		So(err, ShouldBeNil)
		So(waitUntilRecovered(server), ShouldBeTrue)

		server.Stop(ctx, true)

		logged := readLogFile(t, logPath)
		total := strconv.Itoa(dbcompatIncompleteCount)

		startLines := logLinesContaining(logged, recoveryStartLogMatch)
		So(len(startLines), ShouldEqual, 1)
		So(startLines[0], ShouldContainSubstring, "total="+total)

		finishLines := logLinesContaining(logged, recoveryFinishLogMatch)
		So(len(finishLines), ShouldEqual, 1)
		So(finishLines[0], ShouldContainSubstring, "restored="+total)
		So(finishLines[0], ShouldContainSubstring, "total="+total)

		So(logLinesContaining(logged, recoveryStillLogMatch), ShouldBeEmpty)
		So(recoveryHeartbeatInterval, ShouldBeGreaterThan, warmRestartRecovery)
	})
}

// TestReliable4LongRecoveryReportsStillWorking covers the part that mattered in
// the incident: a recovery that outlasts the heartbeat interval keeps saying it
// is still working, carrying the elapsed time (recovery enqueues in a single
// batch, so there is no per-job count to report), and stops saying it once
// recovery ends. The recovery is held open at the recoveryPauseHook seam, so
// the window is deterministic rather than timing-dependent.
func TestReliable4LongRecoveryReportsStillWorking(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A recovery that outlasts the heartbeat interval keeps reporting that it is still working", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig = recoveryLogFixtureConfig(t, serverConfig)
		logPath, serverCtx := managerLogContext(t, ctx)

		previousInterval := recoveryHeartbeatInterval
		recoveryHeartbeatInterval = recoveryLogHeartbeatInterval

		defer func() { recoveryHeartbeatInterval = previousInterval }()

		server, _, release := pausedRecoveringFixtureServer(serverCtx, serverConfig)

		defer func() {
			release()
			server.Stop(ctx, true)
		}()

		var heartbeats []string

		So(pollUntil(func() bool {
			heartbeats = logLinesContaining(readLogFile(t, logPath), recoveryStillLogMatch)

			return len(heartbeats) > 0
		}), ShouldBeTrue)

		So(heartbeats[0], ShouldContainSubstring, "total="+strconv.Itoa(dbcompatIncompleteCount))

		elapsed, err := time.ParseDuration(logLineValue(heartbeats[0], "elapsed"))
		So(err, ShouldBeNil)
		So(elapsed, ShouldBeGreaterThan, time.Duration(0))

		release()
		So(waitUntilRecovered(server), ShouldBeTrue)

		// the heartbeat stops with recovery: given several intervals' more chances,
		// with the server still up so a goroutine that outlived recovery would
		// still be ticking, nothing claims to be still recovering after the finish
		// line.
		time.Sleep(4 * recoveryLogHeartbeatInterval)

		logged := readLogFile(t, logPath)
		So(logged, ShouldContainSubstring, recoveryFinishLogMatch)
		So(strings.LastIndex(logged, recoveryStillLogMatch),
			ShouldBeLessThan, strings.Index(logged, recoveryFinishLogMatch))
	})
}

// recoveryLogFixtureConfig points the given config at a fresh copy of the
// committed golden DB, whose jobslive bucket holds dbcompatIncompleteCount
// prior incomplete jobs for recovery to restore.
func recoveryLogFixtureConfig(t *testing.T, serverConfig ServerConfig) ServerConfig {
	t.Helper()

	dbPath := copyFixtureToTempDB(t, serverConfig.DBFile)
	serverConfig.DBFile = dbPath
	serverConfig.DBFileBackup = dbPath + "_bk"
	serverConfig.dontWipeDevDB = true

	return serverConfig
}

// managerLogContext configures logging exactly as `wr manager start` does
// without --debug (cmd.setupManagerLogging): one log file with an info-level
// handler on the global logger and a WARN-level handler on the context handed
// to the jobqueue server. It returns the log file's path and that server
// context. Because a context handler replaces (not augments) the global one,
// anything the server logs below warn on this context is dropped - which is
// exactly the production filtering this test must not be able to bypass.
func managerLogContext(t *testing.T, ctx context.Context) (string, context.Context) {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "manager.log")

	handlers, err := clog.CreateFileHandlersAtLevels(logPath, "info", "warn")
	So(err, ShouldBeNil)

	previous := clog.GetHandler()

	clog.AddHandler(handlers[0])
	t.Cleanup(func() { log15.Root().SetHandler(previous) })

	return logPath, clog.ContextWithLogHandler(ctx, handlers[1])
}

// logLinesContaining returns the log lines containing the given substring.
func logLinesContaining(logged, substr string) []string {
	var lines []string

	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, substr) {
			lines = append(lines, line)
		}
	}

	return lines
}

// readLogFile returns the whole current content of the log file at path. It is
// safe to call while the manager is still writing (log15's stream handler
// appends whole records), so a test can poll for a line that a running server
// has not logged yet.
func readLogFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	return string(content)
}

// logLineValue returns the value logged for the given logfmt key in the given
// log line, or "" if the line does not carry that key.
func logLineValue(line, key string) string {
	_, after, found := strings.Cut(line, " "+key+"=")
	if !found {
		return ""
	}

	value, _, _ := strings.Cut(after, " ")

	return value
}
