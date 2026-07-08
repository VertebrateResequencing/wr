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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const testDBUpgradeState = "rebuild job lookup index"

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
			Detail:    "rebuilding database job lookup index",
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
					Detail:    "rebuilding database job lookup index",
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

func writeDBUpgradeStatusForTest(t *testing.T, dbFile string, status internal.DBUpgradeStatus, modTime time.Time) {
	t.Helper()

	payload, err := json.Marshal(status)
	So(err, ShouldBeNil)

	path := internal.DBUpgradeStatusPath(dbFile)
	So(os.WriteFile(path, payload, 0o600), ShouldBeNil)
	So(os.Chtimes(path, modTime, modTime), ShouldBeNil)
}
