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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	managerStopTestDeployment = "stale-pid-test"

	// managerWord and startWord are the subcommand words of a manager start argv.
	managerWord = "manager"
	startWord   = "start"
)

func TestIsManagerArgs(t *testing.T) {
	Convey("isManagerArgs recognises only a manager start for the given deployment", t, func() {
		const dep = "production"

		for _, args := range [][]string{
			{"/usr/bin/wr", managerWord, startWord, deploymentFlag, dep},
			{"/opt/wr-renamed", managerWord, startWord, "-s", "local", deploymentFlag, dep},
			{"wr", managerWord, deploymentFlag, dep, startWord},
			{"wr", managerWord, startWord, deploymentFlag + "=" + dep},
		} {
			So(isManagerArgs(args, dep), ShouldBeTrue)
		}

		for _, args := range [][]string{
			nil,
			{"sleep", "60"},
			{"wr", managerWord, startWord, deploymentFlag, "development"},
			{"wr", managerWord, "stop", deploymentFlag, dep},
			{"wr", managerWord, startWord},
			{"wr", managerWord, startWord, deploymentFlag},
			{managerWord, startWord, deploymentFlag, dep},
			{"bash", "-c", "wr manager start --deployment " + dep},
		} {
			So(isManagerArgs(args, dep), ShouldBeFalse)
		}
	})

	Convey("a daemonized manager's argv names its resolved deployment, whatever --deployment it was given", t, func() {
		const dep = "production"

		// an unknown --deployment value resolves to the default deployment
		args := daemonArgs([]string{"wr", managerWord, startWord, deploymentFlag, "prod"}, dep, "-c", "/abs/path")

		So(isManagerArgs(args, dep), ShouldBeTrue)
		So(args[len(args)-2:], ShouldResemble, []string{"-c", "/abs/path"})
	})
}

// managerStopTestProcess is a child process the test started and reaps in the
// background, so a signal that kills it is observable as exited rather than
// hidden behind a zombie that still answers signal 0.
type managerStopTestProcess struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error
}

// startManagerStopTestProcess starts name with args in its own process group
// and arranges for the whole group to be killed when the test ends.
func startManagerStopTestProcess(t *testing.T, name string, args ...string) *managerStopTestProcess {
	t.Helper()

	cmd := exec.Command(name, args...) //nolint:noctx // killed in t.Cleanup
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	So(cmd.Start(), ShouldBeNil)

	p := &managerStopTestProcess{cmd: cmd, exited: make(chan struct{})}

	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()

	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck

		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
		}
	})

	return p
}

// exitedWithin reports whether the process exited within d, and if so the
// signal that terminated it (0 if it was not killed by a signal).
func (p *managerStopTestProcess) exitedWithin(d time.Duration) (bool, syscall.Signal) {
	select {
	case <-p.exited:
		var exitErr *exec.ExitError
		if errors.As(p.waitErr, &exitErr) {
			// Signal() is -1 for a process that exited rather than being killed
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signal() > 0 {
				return true, ws.Signal()
			}
		}

		return true, 0
	case <-time.After(d):
		return false, 0
	}
}

func TestNonPositivePidsAreNeverSignalled(t *testing.T) {
	// kill(0, sig) signals our own process group and kill(-n, sig) signals
	// process group n, so a pid of 0 or less must never reach kill. A child in
	// its own group stands in for the group a negative pid would hit.
	Convey("stopdaemon, killProcess and daemonStillRunning refuse a non-positive pid", t, func() {
		group := startManagerStopTestProcess(t, "sleep", "60")
		pgid := -group.cmd.Process.Pid

		So(stopdaemon(pgid, "test"), ShouldBeFalse)
		So(killProcess(pgid), ShouldNotBeNil)

		exited, _ := group.exitedWithin(200 * time.Millisecond)
		So(exited, ShouldBeFalse)

		for _, pid := range []int{0, -1, pgid} {
			So(daemonStillRunning(pid, nil), ShouldBeFalse)
		}
	})
}

func TestManagerStatusStalePidFile(t *testing.T) {
	Convey("wr manager status reports stopped, not non-responsive, for a stale pid file", t, func() {
		unrelated := startManagerStopTestProcess(t, "sleep", "60")
		setManagerStopTestConfig(t, unrelated.cmd.Process.Pid)

		config.ManagerDBFile = filepath.Join(config.ManagerDir, "db")

		logged := clog.ToBufferAtLevel("warn")
		originalCmdExit := cmdExit

		cmdExit = func(code int) {
			panic(commandExitPanic{code: code})
		}

		defer func() {
			cmdExit = originalCmdExit

			clog.ToDefault()
		}()

		exitCode := 0

		out := captureStdout(func() {
			exitCode = recoverCommandExit(func() {
				managerStatusCmd.Run(managerStatusCmd, nil)
			})
		})

		So(exitCode, ShouldEqual, 0)
		So(logged.String(), ShouldNotContainSubstring, "non-responsive")
		So(out, ShouldEqual, "stopped\n")
	})
}

// setManagerStopTestConfig points config at a fresh deployment directory with
// no manager running, writing pid into its pid file.
func setManagerStopTestConfig(t *testing.T, pid int) {
	t.Helper()

	setManagerStopTestConfigPidFile(t, strconv.Itoa(pid))
}

// setManagerStopTestConfigPidFile is setManagerStopTestConfig with the pid
// file's raw content.
func setManagerStopTestConfigPidFile(t *testing.T, content string) {
	t.Helper()

	oldConfig, oldCAFile := config, caFile

	t.Cleanup(func() {
		config, caFile = oldConfig, oldCAFile
	})

	dir := t.TempDir()
	config = &internal.Config{
		Deployment:       managerStopTestDeployment,
		ManagerDir:       dir,
		ManagerPidFile:   filepath.Join(dir, "pid"),
		ManagerTokenFile: filepath.Join(dir, "client.token"),
		ManagerCAFile:    filepath.Join(dir, "ca.pem"),
		ManagerHost:      "localhost",
		ManagerPort:      closedLocalPort(),
	}
	caFile = config.ManagerCAFile

	So(os.WriteFile(config.ManagerPidFile, []byte(content), 0o600), ShouldBeNil)
}

// closedLocalPort returns a localhost port nothing is listening on.
func closedLocalPort() string {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "localhost:0")
	So(err, ShouldBeNil)

	addr, ok := l.Addr().(*net.TCPAddr)
	So(ok, ShouldBeTrue)
	So(l.Close(), ShouldBeNil)

	return strconv.Itoa(addr.Port)
}

func TestManagerStopStalePidFile(t *testing.T) {
	Convey("wr manager stop does not signal an unrelated process named by a stale pid file", t, func() {
		unrelated := startManagerStopTestProcess(t, "sleep", "60")
		setManagerStopTestConfig(t, unrelated.cmd.Process.Pid)

		exitCode, logged := runManagerStopForTest()

		exited, _ := unrelated.exitedWithin(200 * time.Millisecond)
		So(exited, ShouldBeFalse)
		So(exitCode, ShouldEqual, 1)
		So(logged, ShouldContainSubstring, "stale")
		So(logged, ShouldContainSubstring, "does not seem to be running")
	})

	Convey("wr manager stop still terminates a non-responsive manager named by the pid file", t, func() {
		// sh keeps its own argv, so this process looks exactly like a daemonized
		// `wr manager start --deployment <d>` that is not listening on its port.
		manager := startManagerStopTestProcess(t, "sh", "-c", "sleep 60 & wait",
			managerWord, startWord, deploymentFlag, managerStopTestDeployment)
		setManagerStopTestConfig(t, manager.cmd.Process.Pid)

		exitCode, logged := runManagerStopForTest()

		exited, sig := manager.exitedWithin(5 * time.Second)
		So(exited, ShouldBeTrue)
		So(sig, ShouldEqual, syscall.SIGTERM)
		So(exitCode, ShouldEqual, 0)
		So(logged, ShouldNotContainSubstring, "stale")
	})
}

// runManagerStopForTest drives managerStopCmd's real Run, returning the exit
// code it asked for (0 if none) and everything it logged at warn or above.
func runManagerStopForTest() (int, string) {
	logged := clog.ToBufferAtLevel("warn")
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

func TestDaemonStillRunning(t *testing.T) {
	Convey("daemonStillRunning treats a pid whose argv changed as stopped", t, func() {
		p := startManagerStopTestProcess(t, "sleep", "60")
		pid := p.cmd.Process.Pid
		identity := processArgs(pid)

		So(identity, ShouldResemble, []string{"sleep", "60"})
		So(daemonStillRunning(pid, identity), ShouldBeTrue)
		So(daemonStillRunning(pid, nil), ShouldBeTrue)
		So(daemonStillRunning(pid, []string{"wr", managerWord, startWord}), ShouldBeFalse)

		So(syscall.Kill(pid, syscall.SIGKILL), ShouldBeNil)

		exited, _ := p.exitedWithin(5 * time.Second)
		So(exited, ShouldBeTrue)
		So(daemonStillRunning(pid, identity), ShouldBeFalse)
	})
}

func TestManagerStopInvalidPidFile(t *testing.T) {
	for _, content := range []string{"0", "-1", "garbage"} {
		Convey("wr manager stop signals nothing for a pid file containing "+content, t, func() {
			setManagerStopTestConfigPidFile(t, content)

			exitCode, logged := runManagerStopForTest()

			So(exitCode, ShouldEqual, 1)
			So(logged, ShouldContainSubstring, "so it was not signalled")
			So(logged, ShouldContainSubstring, "does not seem to be running")
		})
	}
}
