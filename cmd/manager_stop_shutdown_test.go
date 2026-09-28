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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal/testcerts"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// These env vars configure the helper manager process that
// TestManagerStopHelperProcess runs when re-executed by
// startShuttingDownManager.
const (
	stopHelperEnvDir  = "WR_STOPTEST_HELPER_DIR"
	stopHelperEnvPort = "WR_STOPTEST_HELPER_PORT"
	stopHelperEnvWait = "WR_STOPTEST_HELPER_RUNNER_WAIT"

	stopHelperReadyFile = "ready"
	stopHelperLogFile   = "helper.log"
	stopHelperDomain    = "localhost"

	stopHelperRunnerCmd = "fakerunner %s %s %s %s %d %d"

	sleepCmd = "sleep"
)

var errStopHelperNotReady = errors.New("helper manager did not become ready")

// TestManagerStopHelperProcess is not a test: it is the body of the helper
// process startShuttingDownManager starts, a real jobqueue server that behaves
// like a manager that cannot finish shutting down, because its only runner
// never exits. It does nothing unless stopHelperEnvDir is set.
func TestManagerStopHelperProcess(t *testing.T) {
	dir := os.Getenv(stopHelperEnvDir)
	if dir == "" {
		return
	}

	runnerWait, err := time.ParseDuration(os.Getenv(stopHelperEnvWait))
	if err != nil {
		os.Exit(2)
	}

	os.Exit(runStopHelperManager(dir, os.Getenv(stopHelperEnvPort), runnerWait))
}

// runStopHelperManager serves a manager from dir on port whose runner never
// exits, and which waits runnerWait for it when shutting down. It signals
// readiness by creating the ready file, and blocks until the server stops. It
// returns the exit code for the helper process.
func runStopHelperManager(dir, port string, runnerWait time.Duration) int {
	ctx := context.Background()

	if err := clog.ToFileAtLevel(filepath.Join(dir, stopHelperLogFile), "info"); err != nil {
		return 2
	}

	runnerStarted := make(chan struct{}, 1)

	server, _, token, err := jobqueue.Serve(ctx, jobqueue.ServerConfig{
		Port:          port,
		SchedulerName: "mock",
		SchedulerConfig: &jqs.ConfigMock{
			RunnerFunc: func(context.Context, string) {
				runnerStarted <- struct{}{}

				select {} // a runner that never exits, even when told to
			},
		},
		RunnerCmd:  stopHelperRunnerCmd,
		DBFile:     filepath.Join(dir, "db"),
		TokenFile:  filepath.Join(dir, "client.token"),
		CAFile:     filepath.Join(dir, "ca.pem"),
		CertFile:   filepath.Join(dir, "cert.pem"),
		KeyFile:    filepath.Join(dir, "key.pem"),
		CertDomain: stopHelperDomain,
		Deployment: managerStopTestDeployment,
		Timings: jobqueue.ServerTimings{
			TouchInterval:      100 * time.Millisecond,
			ShutdownSocketWait: time.Millisecond,
			ShutdownRunnerWait: runnerWait,
		},
	})
	if err != nil {
		clog.Error(ctx, "helper manager failed to start", "err", err)

		return 2
	}

	<-server.Serving()

	if !addStopHelperJob(port, token, filepath.Join(dir, "ca.pem")) {
		return 2
	}

	<-runnerStarted

	if err = os.WriteFile(filepath.Join(dir, stopHelperReadyFile), nil, 0o600); err != nil {
		return 2
	}

	if err = server.Block(); err != nil {
		clog.Info(ctx, "helper manager stopped", "err", err)
	}

	return 0
}

// addStopHelperJob adds one job to the helper manager, so that it schedules a
// runner.
func addStopHelperJob(port string, token []byte, caFile string) bool {
	jq, err := jobqueue.Connect("localhost:"+port, caFile, stopHelperDomain, token, 10*time.Second)
	if err != nil {
		return false
	}

	defer jq.Disconnect() //nolint:errcheck

	_, _, err = jq.Add([]*jobqueue.Job{{
		Cmd: "sleep 3600", Cwd: "/tmp", RepGroup: "stoptest", ReqGroup: "stoptest",
		Requirements: &jqs.Requirements{RAM: 10, Time: time.Hour, Cores: 1, Other: map[string]string{}},
	}}, os.Environ(), true)

	return err == nil
}

func TestManagerStopWhileShuttingDown(t *testing.T) {
	Convey("wr manager stop of a manager still shutting down after the give-up time", t, func() {
		manager, dir := startShuttingDownManager(t, time.Hour)

		origGiveup := daemonStopGiveup
		daemonStopGiveup = 2 * time.Second

		Reset(func() { daemonStopGiveup = origGiveup })

		exitCode, logged := runManagerStopCapturingInfo()

		Convey("reports failure, keeps the token, and leaves the manager to finish", func() {
			So(logged, ShouldNotContainSubstring, "gracefully shut down")
			So(exitCode, ShouldEqual, 1)
			So(logged, ShouldContainSubstring, "still running")
			So(logged, ShouldContainSubstring, "kept")

			_, err := os.Stat(config.ManagerTokenFile)
			So(err, ShouldBeNil)

			exited, _ := manager.exitedWithin(time.Second)
			So(exited, ShouldBeFalse)

			helperLog, err := os.ReadFile(filepath.Join(dir, stopHelperLogFile))
			So(err, ShouldBeNil)
			So(string(helperLog), ShouldNotContainSubstring, "already shutting down")
		})

		Convey("a second SIGTERM, as from running wr manager stop again, does not kill it mid-shutdown", func() {
			So(syscall.Kill(manager.cmd.Process.Pid, syscall.SIGTERM), ShouldBeNil)

			exited, _ := manager.exitedWithin(time.Second)
			So(exited, ShouldBeFalse)

			So(pollUntilTrue(func() bool {
				helperLog, err := os.ReadFile(filepath.Join(dir, stopHelperLogFile))

				return err == nil && strings.Contains(string(helperLog), "already shutting down")
			}), ShouldBeTrue)
		})
	})
}

// startShuttingDownManager starts a real manager in a helper process, looking
// to `wr manager stop` like a daemonized `wr manager start` for the test
// deployment, with its pid in the pid file config names. Its only runner never
// exits, and its shutdown waits runnerWait for that runner. It returns the
// process and the directory holding its token and log.
func startShuttingDownManager(t *testing.T, runnerWait time.Duration) (*managerStopTestProcess, string) {
	t.Helper()

	setManagerStopTestConfigPidFile(t, "")

	dir := config.ManagerDir
	config.ManagerCertFile = filepath.Join(dir, "cert.pem")
	config.ManagerKeyFile = filepath.Join(dir, "key.pem")
	config.ManagerCertDomain = stopHelperDomain
	config.ManagerDBFile = filepath.Join(dir, "db")

	So(testcerts.Write(config.ManagerCAFile, config.ManagerCertFile, config.ManagerKeyFile,
		stopHelperDomain), ShouldBeNil)

	t.Setenv(stopHelperEnvDir, dir)
	t.Setenv(stopHelperEnvPort, config.ManagerPort)
	t.Setenv(stopHelperEnvWait, runnerWait.String())

	manager := startManagerStopTestProcess(t, os.Args[0], "-test.run=^TestManagerStopHelperProcess$", "--",
		managerWord, startWord, deploymentFlag, managerStopTestDeployment)

	So(os.WriteFile(config.ManagerPidFile, []byte(strconv.Itoa(manager.cmd.Process.Pid)), 0o600),
		ShouldBeNil)

	So(waitForStopHelper(dir, manager), ShouldBeNil)

	return manager, dir
}

// waitForStopHelper waits for the helper manager to be ready.
func waitForStopHelper(dir string, manager *managerStopTestProcess) error {
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, stopHelperReadyFile)); err == nil {
			return nil
		}

		if exited, _ := manager.exitedWithin(50 * time.Millisecond); exited {
			break
		}
	}

	return errStopHelperNotReady
}

// runManagerStopCapturingInfo is runManagerStopForTest, but also captures what
// was logged at info level, which is where success is reported.
func runManagerStopCapturingInfo() (int, string) {
	logged := clog.ToBufferAtLevel("info")
	originalCmdExit := cmdExit

	cmdExit = func(code int) {
		panic(commandExitPanic{code: code})
	}

	defer func() {
		cmdExit = originalCmdExit

		clog.ToDefault()
	}()

	exitCode := recoverCommandExit(func() {
		managerStopCmd.Run(managerStopCmd, nil)
	})

	return exitCode, logged.String()
}

func TestDaemonStillRunningUnreadableArgv(t *testing.T) {
	Convey("an unreaped zombie, whose argv can no longer be read, counts as stopped", t, func() {
		cmd := exec.Command(sleepCmd, "60") //nolint:noctx // killed below
		So(cmd.Start(), ShouldBeNil)

		pid := cmd.Process.Pid
		identity := processArgs(pid)
		So(identity, ShouldResemble, []string{sleepCmd, "60"})
		So(isZombie(pid), ShouldBeFalse)

		So(syscall.Kill(pid, syscall.SIGKILL), ShouldBeNil)

		// not reaped yet, so the pid still answers signal 0
		So(pollUntilTrue(func() bool { return isZombie(pid) }), ShouldBeTrue)
		So(processArgs(pid), ShouldBeNil)
		So(daemonStillRunning(pid, identity), ShouldBeFalse)

		So(cmd.Wait(), ShouldNotBeNil)
	})
}

func TestManagerStopWithARunnerThatNeverExits(t *testing.T) {
	Convey("wr manager stop of a manager whose runner never exits still stops it cleanly", t, func() {
		manager, dir := startShuttingDownManager(t, time.Second)

		exitCode, logged := runManagerStopCapturingInfo()

		So(exitCode, ShouldEqual, 0)
		So(logged, ShouldContainSubstring, "gracefully shut down")

		exited, sig := manager.exitedWithin(5 * time.Second)
		So(exited, ShouldBeTrue)
		So(sig, ShouldEqual, 0)

		_, err := os.Stat(config.ManagerTokenFile)
		So(os.IsNotExist(err), ShouldBeTrue)

		helperLog, err := os.ReadFile(filepath.Join(dir, stopHelperLogFile))
		So(err, ShouldBeNil)
		So(string(helperLog), ShouldContainSubstring, "gave up waiting for runners to exit")
	})
}

// pollUntilTrue polls cond until it is true, for up to 10s, reporting whether it
// became true.
func pollUntilTrue(cond func() bool) bool {
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if cond() {
			return true
		}

		time.Sleep(20 * time.Millisecond)
	}

	return cond()
}
