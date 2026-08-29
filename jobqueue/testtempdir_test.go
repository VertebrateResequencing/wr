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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

// testTempDirPrefix prefixes every temp dir this package's tests create. The
// creating pid is part of the name so that a dir left behind by a run that was
// killed instead of exiting through TestMain can be reaped by a later run; see
// reapDeadTestTempDirs for what that keys on, and for what it therefore does
// not protect.
const testTempDirPrefix = "wrtest-"

// envTempDirChild makes TestTempDirChild (the child half of
// TestTestBinaryTempDirs) do its work instead of skipping.
const envTempDirChild = "WR_TEST_TEMPDIR_CHILD"

// tempDirChildReport prefixes the child's report of the dir it created.
const tempDirChildReport = "TEMPDIR="

// testTempDirMode is the permission a test's own planted temp dir gets.
const testTempDirMode = 0o700

//nolint:gochecknoglobals // TestMain removes the temp dirs this process created.
var testTempDirs struct {
	sync.Mutex
	paths []string
}

// TestTempDirChild is the child half of TestTestBinaryTempDirs: it creates one
// isolated test config, reports the temp dir that made, and does nothing else,
// so the parent can check that dir is gone once this binary has exited.
func TestTempDirChild(t *testing.T) {
	if os.Getenv(envTempDirChild) == "" {
		t.Skip("child of TestTestBinaryTempDirs")
	}

	config := &internal.Config{}
	isolateTestConfig(config)

	fmt.Println(tempDirChildReport + filepath.Dir(config.ManagerDir)) //nolint:forbidigo
}

// TestMain removes every temp dir this test binary created, once all its tests
// have finished. The dirs deliberately outlive the test that created them (a
// test's --servermode/--runnermode subprocesses keep using the manager dir
// inside them, and can briefly outlive their test function), but they must not
// outlive the binary: leaving them for "the Makefile (or the OS)" added ~600
// dirs (~2.5GB) to /tmp per suite run permanently, which filled this host's
// 127GB /tmp and then failed a make race run mid-suite with "no space left on
// device".
//
// On the way in it also reaps what earlier runs left behind when they never
// reached here; reapDeadTestTempDirs states the guarantee that reaping does and
// does not give.
//
// It first lets dispatchSubprocessMode() handle a --runnermode or --servermode
// child, which exits the process. That has to come before the reap and the
// cleanup: a subprocess child must neither reap another run's dirs nor delete
// the dirs its parent is still using.
//
// Mounts must go first, and not for tidiness: reapDeadTestTempDirs' RemoveAll
// recurses INTO a mount point it finds in a dir it is removing, so if a killed
// binary's child still holds that mount's fuse fd, the RemoveAll blocks in
// request_wait_answer and wedges the reaper here - before m.Run(), where no
// -test.timeout watchdog exists yet, so the run hangs for good with no panic.
func TestMain(m *testing.M) {
	dispatchSubprocessMode()

	reapDeadTestMounts()
	reapDeadTestTempDirs()

	code := m.Run()

	for _, path := range takeTestTempDirs() {
		if err := os.RemoveAll(path); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to remove %s: %s\n", path, err)

			if code == 0 {
				code = 1
			}
		}
	}

	os.Exit(code)
}

func TestTestBinaryTempDirs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A test binary removes the temp dirs it created once it exits", t, func() {
		dir := runTempDirChild(t)
		So(dir, ShouldStartWith, filepath.Join(os.TempDir(), testTempDirPrefix))

		_, err := os.Stat(dir)
		So(os.IsNotExist(err), ShouldBeTrue)
	})

	Convey("A killed run's temp dirs get reaped, but a live run's do not", t, func() {
		exited := exitedProcessPid(t)

		dead, err := os.MkdirTemp("", testTempDirPattern(exited, "reaptest"))
		So(err, ShouldBeNil)

		live, err := newTestTempDir("reaptest")
		So(err, ShouldBeNil)

		// a name that only looks like the exited pid's must not borrow its
		// deadness, or a planted dir could aim the reaper wherever it liked.
		uncanonical := filepath.Join(os.TempDir(),
			testTempDirPrefix+"+"+strconv.Itoa(exited)+"-reaptest-planted")
		So(os.Mkdir(uncanonical, testTempDirMode), ShouldBeNil)

		defer func() {
			So(os.RemoveAll(uncanonical), ShouldBeNil)
		}()

		reapDeadTestTempDirs()

		_, err = os.Stat(dead)
		So(os.IsNotExist(err), ShouldBeTrue)

		_, err = os.Stat(live)
		So(err, ShouldBeNil)

		_, err = os.Stat(uncanonical)
		So(err, ShouldBeNil)
	})
}

// runTempDirChild runs this test binary as a child that creates one isolated
// test config, and returns the temp dir the child reported creating.
func runTempDirChild(t *testing.T) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestTempDirChild$") //nolint:gosec

	cmd.Env = append(os.Environ(), envTempDirChild+"=1")

	out, err := cmd.CombinedOutput()
	So(err, ShouldBeNil)

	for line := range strings.SplitSeq(string(out), "\n") {
		if reported, found := strings.CutPrefix(strings.TrimSpace(line), tempDirChildReport); found {
			return reported
		}
	}

	So(string(out), ShouldContainSubstring, tempDirChildReport)

	return ""
}

// exitedProcessPid returns the pid of a process that has exited, so a temp dir
// named after it is one the reaper must remove.
func exitedProcessPid(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^$") //nolint:gosec
	So(cmd.Run(), ShouldBeNil)

	return cmd.Process.Pid
}

// newTestTempDir creates a temp dir that TestMain removes when this test binary
// exits. The label appears in the dir name, so leftovers say what made them.
func newTestTempDir(label string) (string, error) {
	dir, err := os.MkdirTemp("", testTempDirPattern(os.Getpid(), label))
	if err != nil {
		return "", err
	}

	trackTestTempDir(dir)

	return dir, nil
}

// testTempDirPattern returns the os.MkdirTemp pattern for a temp dir created by
// pid for label.
func testTempDirPattern(pid int, label string) string {
	return testTempDirPrefix + strconv.Itoa(pid) + "-" + label + "-*"
}

// trackTestTempDir registers path for removal when this test binary exits.
func trackTestTempDir(path string) {
	testTempDirs.Lock()
	defer testTempDirs.Unlock()

	testTempDirs.paths = append(testTempDirs.paths, path)
}

// reapDeadTestTempDirs removes temp dirs that an earlier run of this test
// binary left in os.TempDir() because it never reached TestMain (eg. it was
// killed by a `timeout` wrapper).
//
// It keys on one thing: whether the pid encoded in the dir's name - the pid of
// the process that CREATED the dir - is still alive. That leaves a
// concurrently running lane's dirs, and a hung test binary's own dirs, alone,
// which is all a parallel `make test`/`make race` needs.
//
// It is NOT a check that no live process is using the dir, and a dir's user
// need not be its creator: a --servermode/--runnermode child runs out of the
// manager dir inside the dir its parent created, so if a test binary is killed
// while such a child is still running, a later run's reaper deletes that dir
// out from under the live child. That is wanted: the child is already orphaned
// from any test that could observe it, and nothing else clears the dirs this
// glob matches - TestMain never ran for that killed run, and `make clean`
// removes only /tmp/wr.
//
// Dirs from a PRE-FIX binary have no dash and no encoded pid
// (/tmp/wrtest<random>, /tmp/wr_self_test<random>), so they match neither the
// glob below nor `make clean`. Nothing automated will ever clear those; this
// host's were removed by hand.
func reapDeadTestTempDirs() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), testTempDirPrefix+"*-*"))
	if err != nil {
		return
	}

	for _, path := range matches {
		if pid, ok := testTempDirPid(path); ok && !pidExists(pid) {
			_ = os.RemoveAll(path)
		}
	}
}

// testTempDirPid returns the pid encoded in a newTestTempDir path. The pid must
// be written canonically, so that a planted "wrtest-+123-x" or "wrtest-0123-x"
// cannot borrow the liveness of pid 123.
func testTempDirPid(path string) (int, bool) {
	name := strings.TrimPrefix(filepath.Base(path), testTempDirPrefix)

	pidStr, _, found := strings.Cut(name, "-")
	if !found {
		return 0, false
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != pidStr {
		return 0, false
	}

	return pid, true
}

// pidExists says whether pid is a process on this host, erring towards yes so
// that an unreadable /proc never costs us someone else's temp dir.
func pidExists(pid int) bool {
	exists, err := process.PidExists(int32(pid)) //nolint:gosec // a pid always fits in an int32

	return err != nil || exists
}

// takeTestTempDirs returns the registered paths and forgets them.
func takeTestTempDirs() []string {
	testTempDirs.Lock()
	defer testTempDirs.Unlock()

	paths := testTempDirs.paths
	testTempDirs.paths = nil

	return paths
}
