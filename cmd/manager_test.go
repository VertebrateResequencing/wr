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

package cmd

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	testDBUpgradeState  = "rebuild job lookup index"
	testDBUpgradeDetail = "rebuilding database job lookup index"
)

func TestManagerDBUpgradeStatusLogMessage(t *testing.T) {
	Convey("manager DB upgrade status logging distinguishes post-upgrade startup", t, func() {
		So(managerDBUpgradeStatusLogMessage(internal.DBUpgradeStatus{
			State:  testDBUpgradeState,
			Detail: testDBUpgradeDetail,
		}), ShouldEqual, "wr manager is upgrading its database: rebuilding database job lookup index")

		So(managerDBUpgradeStatusLogMessage(internal.DBUpgradeStatus{
			State:  internal.DBUpgradePostStartupState,
			Detail: internal.DBUpgradePostStartupDetail,
		}), ShouldEqual, "wr manager is starting after database upgrade: starting manager after database upgrade")
	})
}

func TestGetBadLogLinesScansRotatedManagerLogs(t *testing.T) {
	Convey("getBadLogLines finds bad lines since the latest manager start across rotated logs", t, func() {
		oldConfig := config

		t.Cleanup(func() {
			config = oldConfig
		})

		dir := t.TempDir()
		logPath := filepath.Join(dir, "manager.log")
		config = &internal.Config{ManagerLogFile: logPath}

		err := os.WriteFile(filepath.Join(dir, "manager-2026-01-01T00-00-00.000.log"), []byte(
			"lvl=eror msg=\"old error\"\n",
		), 0o600)
		So(err, ShouldBeNil)

		compressedLog, err := os.Create(filepath.Join(dir, "manager-2026-01-01T00-01-00.000.log.gz"))
		So(err, ShouldBeNil)

		gzipWriter := gzip.NewWriter(compressedLog)
		_, err = gzipWriter.Write([]byte(
			"lvl=info msg=\"wr manager 1.0.0 started on host:1234\"\n" +
				"lvl=eror msg=\"rotated error\"\n",
		))
		So(err, ShouldBeNil)
		So(gzipWriter.Close(), ShouldBeNil)
		So(compressedLog.Close(), ShouldBeNil)

		err = os.WriteFile(filepath.Join(dir, "manager-2026-01-01T00-02-00.000.log"), []byte(
			"lvl=eror msg=\"newer rotated error\"\n",
		), 0o600)
		So(err, ShouldBeNil)

		err = os.WriteFile(logPath, []byte(
			"lvl=info msg=\"ordinary info\"\n"+
				"lvl=crit msg=\"active critical\"\n",
		), 0o600)
		So(err, ShouldBeNil)

		So(getBadLogLines(), ShouldResemble, []string{
			"lvl=eror msg=\"rotated error\"",
			"lvl=eror msg=\"newer rotated error\"",
			"lvl=crit msg=\"active critical\"",
		})
	})
}

func TestGetBadLogLinesHandlesLongLogLines(t *testing.T) {
	Convey("getBadLogLines keeps scanning after a long non-matching log line", t, func() {
		oldConfig := config

		t.Cleanup(func() {
			config = oldConfig
		})

		dir := t.TempDir()
		logPath := filepath.Join(dir, "manager.log")
		config = &internal.Config{ManagerLogFile: logPath}

		err := os.WriteFile(logPath, []byte(
			"lvl=info msg=\""+strings.Repeat("x", 70*1024)+"\"\n"+
				"lvl=eror msg=\"later error\"\n"+
				"lvl=crit msg=\"later critical\"\n",
		), 0o600)
		So(err, ShouldBeNil)

		So(getBadLogLines(), ShouldResemble, []string{
			"lvl=eror msg=\"later error\"",
			"lvl=crit msg=\"later critical\"",
		})
	})
}

// TestManagerCompactRefusesWhileRunning covers D2 acceptance test 2: the
// `wr manager compact` subcommand must refuse (exit non-zero) and leave the
// database untouched while a manager is running. It mirrors
// TestManagerRecomputeCountsRefusesWhileRunning: it drives the real Run func
// against a real in-process manager reachable via connect(), swapping the
// managerCompactExit seam so the non-zero exit is observable without terminating
// the test process, and asserts the compaction call (the only thing that would
// modify the db) is never invoked, so the db is provably untouched.
func TestManagerCompactRefusesWhileRunning(t *testing.T) {
	ctx := context.Background()

	Convey("compact refuses while a manager runs and leaves the db untouched", t, func() {
		oldConfig := config
		oldCAFile := caFile
		oldExit := managerCompactExit
		oldCompact := compactDBFile

		t.Cleanup(func() {
			config = oldConfig
			caFile = oldCAFile
			managerCompactExit = oldExit
			compactDBFile = oldCompact
		})

		testConfig, _, _, _, server, _ := startStatusTestServer(ctx, t)
		defer server.Stop(ctx, true)

		config = testConfig
		caFile = testConfig.ManagerCAFile

		exitCode := -1
		managerCompactExit = func(code int) { exitCode = code }

		compactCalled := false
		compactDBFile = func(string) (int64, int64, error) {
			compactCalled = true

			return 0, 0, nil
		}

		managerCompactCmd.Run(managerCompactCmd, nil)

		Convey("D2.2: it exits non-zero and never touches (compacts) the database", func() {
			So(exitCode, ShouldEqual, 1)
			So(compactCalled, ShouldBeFalse)
		})
	})
}

func TestWaitForManagerStartupDuringDBUpgrade(t *testing.T) {
	Convey("manager startup waits past the initial timeout while a DB upgrade is active", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 200 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		preStart := time.Now().Add(-time.Second)

		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)

		started := time.Now()
		readyAt := started.Add(90 * time.Millisecond)
		statusDone := make(chan struct{})
		statusErr := make(chan error, 1)

		So(internal.WriteDBUpgradeStatus(config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     testDBUpgradeState,
			Detail:    testDBUpgradeDetail,
			StartedAt: preStart,
		}), ShouldBeNil)

		go func() {
			defer close(statusDone)

			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()

			for range ticker.C {
				if time.Now().After(readyAt) {
					return
				}

				err := internal.WriteDBUpgradeStatus(config.ManagerDBFile, internal.DBUpgradeStatus{
					State:     testDBUpgradeState,
					Detail:    testDBUpgradeDetail,
					StartedAt: preStart,
				})
				if err != nil {
					statusErr <- err

					return
				}
			}
		}()

		var reports []string

		jq := waitForManagerStartupWith(preStart, 20*time.Millisecond, func(time.Duration) *jobqueue.Client {
			if time.Now().After(readyAt) {
				return &jobqueue.Client{}
			}

			return nil
		}, func(status internal.DBUpgradeStatus) {
			reports = append(reports, managerDBUpgradeStatusText(status))
		})

		<-statusDone

		select {
		case err := <-statusErr:
			So(err, ShouldBeNil)
		default:
		}

		So(jq, ShouldNotBeNil)
		So(time.Since(started), ShouldBeGreaterThanOrEqualTo, 80*time.Millisecond)
		So(reports, ShouldContain, "rebuilding database job lookup index")
	})

	Convey("manager startup still times out normally without a fresh DB upgrade", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 40 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		preStart := time.Now().Add(-time.Second)

		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)

		stale := preStart.Add(-time.Second)
		writeDBUpgradeStatusForTest(t, config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     testDBUpgradeState,
			Detail:    "stale upgrade",
			PID:       os.Getpid(),
			StartedAt: stale,
			UpdatedAt: stale,
		}, stale)

		started := time.Now()
		jq := waitForManagerStartupWith(preStart, 20*time.Millisecond, func(time.Duration) *jobqueue.Client {
			return nil
		}, func(internal.DBUpgradeStatus) {
			So("stale upgrade should not be reported", ShouldBeBlank)
		})

		So(jq, ShouldBeNil)
		So(time.Since(started), ShouldBeLessThan, 150*time.Millisecond)
	})

	Convey("manager startup connects when a new token has coarse filesystem mtime", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 40 * time.Millisecond

		managerDir := filepath.Join(t.TempDir(), "manager")
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(managerDir, "db"),
			ManagerTokenFile: filepath.Join(managerDir, "client.token"),
		}

		_, err := os.Stat(managerDir)
		So(os.IsNotExist(err), ShouldBeTrue)

		preStart := time.Now()

		So(os.MkdirAll(managerDir, 0o700), ShouldBeNil)
		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)
		So(os.Chtimes(config.ManagerTokenFile, preStart.Truncate(time.Second), preStart.Truncate(time.Second)),
			ShouldBeNil)

		readyAt := time.Now().Add(30 * time.Millisecond)
		attempts := 0

		jq := waitForManagerStartupWith(preStart, 80*time.Millisecond, func(time.Duration) *jobqueue.Client {
			attempts++

			if time.Now().After(readyAt) {
				return &jobqueue.Client{}
			}

			return nil
		}, func(internal.DBUpgradeStatus) {
			So("new-token startup should not report an upgrade", ShouldBeBlank)
		})

		So(jq, ShouldNotBeNil)
		So(attempts, ShouldBeGreaterThan, 0)
	})

	Convey("manager startup waits when a new token is empty", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 40 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		preStart := time.Now()

		So(os.WriteFile(config.ManagerTokenFile, nil, 0o600), ShouldBeNil)

		tokenTime := preStart.Add(time.Second)
		So(os.Chtimes(config.ManagerTokenFile, tokenTime, tokenTime), ShouldBeNil)

		attempts := 0
		started := time.Now()

		jq := waitForManagerStartupWith(preStart, 20*time.Millisecond, func(time.Duration) *jobqueue.Client {
			attempts++

			return &jobqueue.Client{}
		}, func(internal.DBUpgradeStatus) {
			So("empty-token startup should not report an upgrade", ShouldBeBlank)
		})

		So(jq, ShouldBeNil)
		So(attempts, ShouldEqual, 0)
		So(time.Since(started), ShouldBeLessThan, 150*time.Millisecond)
	})

	Convey("manager startup keeps a longer timeout while a DB upgrade status is fresh", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 25 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		startupTimeout := 300 * time.Millisecond
		preStart := time.Now().Add(-10 * time.Millisecond)

		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)

		statusTime := time.Now()
		writeDBUpgradeStatusForTest(t, config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     testDBUpgradeState,
			Detail:    "single fresh upgrade status",
			PID:       os.Getpid(),
			StartedAt: preStart,
			UpdatedAt: statusTime,
		}, statusTime)

		started := time.Now()
		readyAt := started.Add(100 * time.Millisecond)
		So(readyAt.Before(preStart.Add(startupTimeout)), ShouldBeTrue)

		var reports []string

		jq := waitForManagerStartupWith(preStart, startupTimeout, func(time.Duration) *jobqueue.Client {
			if time.Now().After(readyAt) {
				return &jobqueue.Client{}
			}

			return nil
		}, func(status internal.DBUpgradeStatus) {
			reports = append(reports, managerDBUpgradeStatusText(status))
		})

		So(jq, ShouldNotBeNil)
		So(time.Since(started), ShouldBeGreaterThanOrEqualTo, 90*time.Millisecond)
		So(reports, ShouldContain, "single fresh upgrade status")
	})

	Convey("manager startup trusts the DB upgrade status timestamp when filesystem mtime is coarse", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 200 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		preStart := time.Now().Add(-time.Second)

		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)

		statusTime := time.Now()
		writeDBUpgradeStatusForTest(t, config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     testDBUpgradeState,
			Detail:    "fresh upgrade with coarse status mtime",
			PID:       os.Getpid(),
			StartedAt: preStart,
			UpdatedAt: statusTime,
		}, preStart.Truncate(time.Second))

		started := time.Now()
		readyAt := started.Add(60 * time.Millisecond)

		var reports []string

		jq := waitForManagerStartupWith(preStart, 20*time.Millisecond, func(time.Duration) *jobqueue.Client {
			if time.Now().After(readyAt) {
				return &jobqueue.Client{}
			}

			return nil
		}, func(status internal.DBUpgradeStatus) {
			reports = append(reports, managerDBUpgradeStatusText(status))
		})

		So(jq, ShouldNotBeNil)
		So(time.Since(started), ShouldBeGreaterThanOrEqualTo, 50*time.Millisecond)
		So(reports, ShouldContain, "fresh upgrade with coarse status mtime")
	})

	Convey("manager startup keeps waiting during a quiet commit phase while the upgrade process is alive", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 25 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		preStart := time.Now().Add(-10 * time.Millisecond)
		statusTime := time.Now()

		writeDBUpgradeStatusForTest(t, config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     "commit database upgrade",
			Detail:    "committing database upgrade",
			PID:       os.Getpid(),
			StartedAt: preStart,
			UpdatedAt: statusTime,
		}, statusTime)

		started := time.Now()
		readyAt := started.Add(100 * time.Millisecond)

		tokenErr := make(chan error, 1)

		go func() {
			time.Sleep(time.Until(readyAt))

			tokenErr <- os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600)
		}()

		var reports []string

		attempts := 0

		jq := waitForManagerStartupWith(preStart, 20*time.Millisecond, func(time.Duration) *jobqueue.Client {
			attempts++

			if time.Now().After(readyAt) {
				return &jobqueue.Client{}
			}

			return nil
		}, func(status internal.DBUpgradeStatus) {
			reports = append(reports, managerDBUpgradeStatusText(status))
		})

		So(<-tokenErr, ShouldBeNil)
		So(jq, ShouldNotBeNil)
		So(attempts, ShouldBeGreaterThan, 0)
		So(time.Since(started), ShouldBeGreaterThanOrEqualTo, 90*time.Millisecond)
		So(reports, ShouldContain, "committing database upgrade")
	})

	Convey("manager startup ignores a fresh DB upgrade status with an invalid PID", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldFresh := managerDBUpgradeStatusFresh

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerDBUpgradeStatusFresh = oldFresh
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerDBUpgradeStatusFresh = 200 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		preStart := time.Now().Add(-time.Second)

		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)

		statusTime := time.Now()
		writeDBUpgradeStatusForTest(t, config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     testDBUpgradeState,
			Detail:    "fresh upgrade with invalid pid",
			PID:       0,
			StartedAt: statusTime,
			UpdatedAt: statusTime,
		}, statusTime)

		readyAt := time.Now().Add(60 * time.Millisecond)
		reports := 0

		jq := waitForManagerStartupWith(preStart, 20*time.Millisecond, func(time.Duration) *jobqueue.Client {
			if time.Now().After(readyAt) {
				return &jobqueue.Client{}
			}

			return nil
		}, func(internal.DBUpgradeStatus) {
			reports++
		})

		So(jq, ShouldBeNil)
		So(reports, ShouldEqual, 0)
	})
}

func TestWaitForLiveManagerStartup(t *testing.T) {
	Convey("manager start keeps waiting for its live daemon and reports delayed startup", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval
		oldConnect := managerStartupConnectAttempt
		oldReport := managerStartupReportInterval

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
			managerStartupConnectAttempt = oldConnect
			managerStartupReportInterval = oldReport
		})

		managerStartupPollInterval = 5 * time.Millisecond
		managerStartupConnectAttempt = time.Millisecond
		managerStartupReportInterval = 15 * time.Millisecond

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}

		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)

		child := exec.Command("sleep", "30") //nolint:noctx // the test owns and terminates this process
		So(child.Start(), ShouldBeNil)

		processDone := monitorManagerStartupProcess(child.Process)

		t.Cleanup(func() {
			if child.ProcessState != nil {
				return
			}

			if err := child.Process.Kill(); err == nil {
				<-processDone
			}
		})

		preStart := time.Now()
		statusTime := preStart.Add(time.Millisecond)
		writeDBUpgradeStatusForTest(t, config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     testDBUpgradeState,
			Detail:    "progress from another process",
			PID:       os.Getpid(),
			StartedAt: preStart,
			UpdatedAt: statusTime,
		}, statusTime)
		_, active := currentManagerDBUpgradeStatus(preStart, child.Process.Pid)
		So(active, ShouldBeFalse)

		statusTime = statusTime.Add(time.Millisecond)
		writeDBUpgradeStatusForTest(t, config.ManagerDBFile, internal.DBUpgradeStatus{
			State:     testDBUpgradeState,
			Detail:    "startup phase from exact manager process",
			PID:       child.Process.Pid,
			StartedAt: preStart,
			UpdatedAt: statusTime,
		}, statusTime)

		readyAt := preStart.Add(70 * time.Millisecond)

		var reports []string

		upgradeReports := 0

		jq, err := waitForLiveManagerStartupWith(preStart, 20*time.Millisecond, child.Process.Pid, processDone,
			func(time.Duration) *jobqueue.Client {
				if time.Now().After(readyAt) {
					return &jobqueue.Client{}
				}

				return nil
			}, func(internal.DBUpgradeStatus) {
				upgradeReports++
			}, func(elapsed time.Duration, phase string) {
				reports = append(reports, managerStartupWaitingLogMessage(elapsed, phase))
			})

		So(err, ShouldBeNil)
		So(jq, ShouldNotBeNil)
		So(time.Since(preStart), ShouldBeGreaterThanOrEqualTo, 60*time.Millisecond)
		So(upgradeReports, ShouldBeGreaterThan, 0)
		So(len(reports), ShouldBeGreaterThan, 1)
		So(reports[0], ShouldContainSubstring, "wr manager is still starting")
		So(reports[0], ShouldContainSubstring, "waiting for it to become ready")
		So(reports[0], ShouldContainSubstring, "startup phase from exact manager process")
		So(reports[0], ShouldNotContainSubstring, "progress from another process")
	})

	Convey("manager start reports when its daemon exits before becoming ready", t, func() {
		oldConfig := config
		oldPoll := managerStartupPollInterval

		t.Cleanup(func() {
			config = oldConfig
			managerStartupPollInterval = oldPoll
		})

		managerStartupPollInterval = 5 * time.Millisecond
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(t.TempDir(), "db"),
			ManagerTokenFile: filepath.Join(t.TempDir(), "client.token"),
		}

		child := exec.Command("sh", "-c", "exit 7") //nolint:noctx // short-lived test subprocess
		So(child.Start(), ShouldBeNil)

		processDone := monitorManagerStartupProcess(child.Process)
		started := time.Now()

		jq, err := waitForLiveManagerStartupWith(started, 2*time.Second, child.Process.Pid, processDone,
			func(time.Duration) *jobqueue.Client {
				return nil
			}, func(internal.DBUpgradeStatus) {}, func(time.Duration, string) {})

		So(jq, ShouldBeNil)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "manager process")
		So(err.Error(), ShouldContainSubstring, "exited before becoming ready")
		So(err.Error(), ShouldContainSubstring, "exit status 7")
		So(time.Since(started), ShouldBeLessThan, 500*time.Millisecond)
	})

	Convey("quick manager startup remains quiet", t, func() {
		oldConfig := config

		t.Cleanup(func() {
			config = oldConfig
		})

		dir := t.TempDir()
		config = &internal.Config{
			ManagerDBFile:    filepath.Join(dir, "db"),
			ManagerTokenFile: filepath.Join(dir, "client.token"),
		}
		So(os.WriteFile(config.ManagerTokenFile, []byte("token"), 0o600), ShouldBeNil)

		processDone := make(chan error)
		reports := 0

		jq, err := waitForLiveManagerStartupWith(time.Now(), time.Second, 123, processDone,
			func(time.Duration) *jobqueue.Client {
				return &jobqueue.Client{}
			}, func(internal.DBUpgradeStatus) {}, func(time.Duration, string) {
				reports++
			})

		So(err, ShouldBeNil)
		So(jq, ShouldNotBeNil)
		So(reports, ShouldEqual, 0)
	})
}

// TestManagerRecomputeCountsSubcommandRemoved covers E1 acceptance test 1: the
// `wr manager recompute-counts` subcommand no longer exists, so cobra treats it
// as an unknown command (a non-zero "unknown command" error at execution). We
// assert this structurally: the manager command tree registers no such
// subcommand, and cobra's Find does not resolve "recompute-counts" to a real
// subcommand (it falls back to the parent, so an execution would error).
func TestManagerRecomputeCountsSubcommandRemoved(t *testing.T) {
	Convey("the manager command tree has no recompute-counts subcommand", t, func() {
		found := false

		for _, sub := range managerCmd.Commands() {
			if sub.Name() == "recompute-counts" {
				found = true
			}
		}

		Convey("E1.1: recompute-counts is an unknown subcommand", func() {
			So(found, ShouldBeFalse)

			resolved, _, err := managerCmd.Find([]string{"recompute-counts"})
			So(err, ShouldBeNil)
			So(resolved, ShouldNotBeNil)
			So(resolved.Name(), ShouldNotEqual, "recompute-counts")
		})
	})
}

func writeDBUpgradeStatusForTest(t *testing.T, dbFile string, status internal.DBUpgradeStatus, modTime time.Time) {
	t.Helper()

	payload, err := json.Marshal(status)
	So(err, ShouldBeNil)

	path := internal.DBUpgradeStatusPath(dbFile)
	So(os.WriteFile(path, payload, 0o600), ShouldBeNil)
	So(os.Chtimes(path, modTime, modTime), ShouldBeNil)
}
