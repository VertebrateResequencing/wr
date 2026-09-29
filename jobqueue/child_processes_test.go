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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

// childLookupTimeout is how long the tests below give a lookup of a process's
// children, which reads a handful of small files when it works as it should.
const childLookupTimeout = 5 * time.Second

// lookupResult is what a lookup run by withinTimeout found.
type lookupResult struct {
	pids []int32
	err  error
}

// withinTimeout runs fn and returns its results, and whether it returned within
// childLookupTimeout.
func withinTimeout(fn func() ([]int32, error)) (lookupResult, bool) {
	done := make(chan lookupResult, 1)

	go func() {
		pids, err := fn()
		done <- lookupResult{pids: pids, err: err}
	}()

	select {
	case r := <-done:
		return r, true
	case <-time.After(childLookupTimeout):
		return lookupResult{}, false
	}
}

// TestChildProcessLookupReadsOnlyTheParent: a runner looks up its command's
// children before it kills the command, and every second while it runs. That
// must not read the /proc entry of every process on the node. On a busy node,
// on a runner starved of CPU, reading them all took so long that the manager's
// stop gave up on the kill before it reached the command, which then ran to
// the end and was run again.
//
// Here, one unrelated process's stat can never be read (it is a FIFO nobody
// writes to), standing in for a scan of the whole process table that takes
// longer than anyone waits.
func TestChildProcessLookupReadsOnlyTheParent(t *testing.T) {
	Convey("Given a process with a child, in a /proc where one unrelated entry blocks reads", t, func() {
		parent := startSleeper(t)
		child := startSleeper(t)

		proc := t.TempDir()
		t.Setenv("HOST_PROC", proc)

		writeFakeProcEntry(t, proc, parent, os.Getpid(), child)
		writeFakeProcEntry(t, proc, child, parent)
		writeFakeThreadSelf(t, proc)

		blocker := filepath.Join(proc, "1", "stat")
		So(os.MkdirAll(filepath.Dir(blocker), 0o700), ShouldBeNil)
		So(syscall.Mkfifo(blocker, 0o600), ShouldBeNil)

		// let any read still stuck on the FIFO see EOF, so it does not outlive
		// the test
		defer releaseFIFO(blocker)

		Convey("getChildProcesses finds the child without reading the unrelated entry", func() {
			r, ok := withinTimeout(func() ([]int32, error) {
				children, err := getChildProcesses(int32(parent)) //nolint:gosec // an OS pid always fits in an int32.

				return processPids(children), err
			})
			So(ok, ShouldBeTrue)
			So(r.err, ShouldBeNil)
			So(r.pids, ShouldResemble, []int32{int32(child)}) //nolint:gosec // an OS pid always fits in an int32.
		})

		Convey("sumChildrenMemory measures the child without reading the unrelated entry", func() {
			r, ok := withinTimeout(func() ([]int32, error) {
				_, err := sumChildrenMemory(parent)

				return nil, err
			})
			So(ok, ShouldBeTrue)
			So(r.err, ShouldBeNil)
		})
	})
}

// startSleeper starts a sleep that is killed at the end of the test, and
// returns its pid.
func startSleeper(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	So(cmd.Start(), ShouldBeNil)

	t.Cleanup(func() {
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
	})

	return cmd.Process.Pid
}

// writeFakeProcEntry gives pid, whose parent is ppid, a stat under proc, and,
// if any children are given, a thread that lists them as its children.
func writeFakeProcEntry(t *testing.T, proc string, pid, ppid int, children ...int) {
	t.Helper()

	dir := filepath.Join(proc, strconv.Itoa(pid))
	So(os.MkdirAll(filepath.Join(dir, "task", strconv.Itoa(pid)), 0o700), ShouldBeNil)

	stat := fmt.Sprintf("%d (sleep) S %d %d %d 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 100 0 0\n", pid, ppid, pid, pid)
	So(os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600), ShouldBeNil)

	if len(children) == 0 {
		return
	}

	fields := make([]string, 0, len(children))
	for _, child := range children {
		fields = append(fields, strconv.Itoa(child))
	}

	list := strings.Join(fields, " ") + " "
	So(os.WriteFile(filepath.Join(dir, "task", strconv.Itoa(pid), "children"), []byte(list), 0o600), ShouldBeNil)
}

// writeFakeThreadSelf makes proc look like the /proc of a kernel that lists
// each thread's children.
func writeFakeThreadSelf(t *testing.T, proc string) {
	t.Helper()

	dir := filepath.Join(proc, "thread-self")
	So(os.MkdirAll(dir, 0o700), ShouldBeNil)
	So(os.WriteFile(filepath.Join(dir, "children"), nil, 0o600), ShouldBeNil)
}

// releaseFIFO opens the FIFO at path for writing without blocking, then closes
// it, so that a reader blocked on it sees EOF.
func releaseFIFO(path string) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}

	syscall.Close(fd)
}

// processPids returns the sorted pids of procs.
func processPids(procs []*process.Process) []int32 {
	pids := make([]int32, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, p.Pid)
	}

	slices.Sort(pids)

	return pids
}

// TestChildProcessLookupWithoutThreadChildren: a kernel built without the
// per-thread children lists still has each process's children found, the slow
// way.
func TestChildProcessLookupWithoutThreadChildren(t *testing.T) {
	Convey("Given a /proc whose kernel does not list each thread's children", t, func() {
		parent := startSleeper(t)
		child := startSleeper(t)

		proc := t.TempDir()
		t.Setenv("HOST_PROC", proc)

		writeFakeProcEntry(t, proc, parent, os.Getpid())
		writeFakeProcEntry(t, proc, child, parent)

		Convey("getChildProcesses still finds the child, from every process's stat", func() {
			children, err := getChildProcesses(int32(parent)) //nolint:gosec // an OS pid always fits in an int32.
			So(err, ShouldBeNil)
			So(processPids(children), ShouldResemble, []int32{int32(child)}) //nolint:gosec // an OS pid always fits in an int32.
		})
	})
}

// TestChildProcessLookupOfRealTree: in the real /proc, a command's children and
// their children are all found.
func TestChildProcessLookupOfRealTree(t *testing.T) {
	Convey("Given a real process tree, getChildProcesses finds its children and grandchildren", t, func() {
		dir := t.TempDir()
		inner := filepath.Join(dir, "inner")
		grandchild := filepath.Join(dir, "grandchild")

		script := fmt.Sprintf(`sh -c 'sleep 30 & echo $! > %s; wait' & echo $! > %s; wait`, grandchild, inner)
		cmd := exec.CommandContext(context.Background(), "sh", "-c", script)
		So(cmd.Start(), ShouldBeNil)

		t.Cleanup(func() {
			pid := int32(cmd.Process.Pid) //nolint:gosec // an OS pid always fits in an int32.
			if children, err := getChildProcesses(pid); err == nil {
				for _, c := range children {
					c.Kill() //nolint:errcheck
				}
			}

			cmd.Process.Kill() //nolint:errcheck
			cmd.Wait()         //nolint:errcheck
		})

		want := []int32{readPidFile(inner), readPidFile(grandchild)}
		slices.Sort(want)

		children, err := getChildProcesses(int32(cmd.Process.Pid)) //nolint:gosec // an OS pid always fits in an int32.
		So(err, ShouldBeNil)
		So(processPids(children), ShouldResemble, want)
	})
}

// readPidFile waits for path to hold a pid, and returns it.
func readPidFile(path string) int32 {
	deadline := time.Now().Add(childLookupTimeout)

	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, errp := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 32); errp == nil {
				return int32(pid)
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	return 0
}
