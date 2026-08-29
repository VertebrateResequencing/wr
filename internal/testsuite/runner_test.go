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

package testsuite

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// envSuiteTempChild makes TestSuiteTempChild do its work instead of skipping,
// and suiteTempChildReport prefixes its report of the dir it made.
const (
	envSuiteTempChild    = "WR_TEST_SUITE_TEMP_CHILD"
	suiteTempChildReport = "SUITETEMP="
	plantedDirMode       = 0o700
)

func TestIsTerminal(t *testing.T) {
	Convey("isTerminal is false for /dev/null, a character device that is not a TTY", t, func() {
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		So(err, ShouldBeNil)

		defer func() {
			So(devNull.Close(), ShouldBeNil)
		}()

		So(isTerminal(devNull), ShouldBeFalse)
	})

	Convey("isTerminal is false for a non-*os.File writer", t, func() {
		So(isTerminal(&bytes.Buffer{}), ShouldBeFalse)
	})
}

// TestSuiteTempChild is the child half of TestSuiteTempReaping: it makes the
// temp dir a run makes for itself, reports it, and exits leaving it behind, as
// a killed run does.
func TestSuiteTempChild(t *testing.T) {
	if os.Getenv(envSuiteTempChild) == "" {
		t.Skip("child of TestSuiteTempReaping")
	}

	dir, err := os.MkdirTemp("", tempPrefix(ModeTest))
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println(suiteTempChildReport + dir) //nolint:forbidigo
}

func TestSuiteTempReaping(t *testing.T) {
	Convey("A run removes its own temp dir and reaps one a killed run left behind", t, func() {
		dead := killedRunSuiteTemp(t)

		live, err := os.MkdirTemp("", tempPrefix(ModeRace))
		So(err, ShouldBeNil)

		defer func() {
			So(os.RemoveAll(live), ShouldBeNil)
		}()

		So(RunPlan(t.Context(), io.Discard, io.Discard, t.TempDir(),
			Plan{Mode: ModeTest, Module: "example.com/m"}), ShouldBeNil)

		_, err = os.Stat(dead)
		So(os.IsNotExist(err), ShouldBeTrue)

		_, err = os.Stat(live)
		So(err, ShouldBeNil)

		// scoped to this process, because RunPlan legitimately reaps every
		// other dead owner's dir too, and /tmp is at its dirtiest - full of
		// exactly those - right after the killed run this fix is about.
		So(suiteTempDirsOf(tempPrefixTest, os.Getpid()), ShouldBeEmpty)
	})

	Convey("A dir whose name does not canonically encode a pid is left alone", t, func() {
		pid := strconv.Itoa(exitedPid(t))
		planted := plantDirs(t, tempPrefixTest+"+"+pid+".x", tempPrefixTest+"0"+pid+".x")

		reapDeadSuiteTemps()

		for _, path := range planted {
			_, err := os.Stat(path)
			So(err, ShouldBeNil)
		}
	})
}

func TestSuiteLeavesForeignJobDirsAlone(t *testing.T) {
	Convey("A run leaves the working dir of a job someone else submitted alone", t, func() {
		foreign := plantJobqueueCwdDir(t)

		So(RunPlan(t.Context(), io.Discard, io.Discard, t.TempDir(),
			Plan{Mode: ModeTest, Module: "example.com/m"}), ShouldBeNil)

		_, err := os.Stat(foreign)
		So(err, ShouldBeNil)
	})
}

// plantJobqueueCwdDir makes one dir under /tmp/jobqueue_cwd for the duration of
// the test, standing in for the working dir of a live job someone else on this
// host submitted with --cwd /tmp: jobqueue's mkHashedDir builds every such job
// a dir under exactly that path, holding its stdout, stderr, .wr_ metadata and
// the cwd the command runs in.
//
// It makes the shared parent the way jobqueue does, world-accessible so users
// coexist under it, and only when it is missing, and removes only what it made.
func plantJobqueueCwdDir(t *testing.T) string {
	t.Helper()

	parent := filepath.Join("/tmp", "jobqueue_cwd")

	_, err := os.Stat(parent)
	madeParent := os.IsNotExist(err)

	So(os.MkdirAll(parent, os.ModePerm), ShouldBeNil)

	dir := filepath.Join(parent, "wrtest-"+strconv.Itoa(os.Getpid())+"-foreign")
	So(os.Mkdir(dir, plantedDirMode), ShouldBeNil)

	// no assertion here: this runs after the Convey block that called us has
	// ended, where So has nowhere to report to. os.Remove leaves the parent
	// alone unless it is empty, so a real job's dir in it is never at risk.
	t.Cleanup(func() {
		os.RemoveAll(dir)

		if madeParent {
			os.Remove(parent)
		}
	})

	return dir
}

// killedRunSuiteTemp returns a temp dir made, and left behind, by a run of this
// binary that is no longer around - what a killed suite leaves in os.TempDir().
func killedRunSuiteTemp(t *testing.T) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestSuiteTempChild$") //nolint:gosec

	cmd.Env = append(os.Environ(), envSuiteTempChild+"=1")

	out, err := cmd.CombinedOutput()
	So(err, ShouldBeNil)

	for line := range strings.SplitSeq(string(out), "\n") {
		if reported, found := strings.CutPrefix(strings.TrimSpace(line), suiteTempChildReport); found {
			return reported
		}
	}

	So(string(out), ShouldContainSubstring, suiteTempChildReport)

	return ""
}

// suiteTempDirsOf returns the temp dirs in os.TempDir() that pid made for the
// given suite mode prefix.
func suiteTempDirsOf(prefix string, pid int) []string {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), prefix+strconv.Itoa(pid)+".*"))
	if err != nil {
		return nil
	}

	return matches
}

// exitedPid returns the pid of a process that has exited, so a temp dir named
// after it is one the reaper would remove if it could read the name.
func exitedPid(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^$") //nolint:gosec
	So(cmd.Run(), ShouldBeNil)

	return cmd.Process.Pid
}

// plantDirs makes the named dirs in os.TempDir() for the duration of the test.
func plantDirs(t *testing.T, names ...string) []string {
	t.Helper()

	paths := make([]string, 0, len(names))

	for _, name := range names {
		path := filepath.Join(os.TempDir(), name)
		So(os.Mkdir(path, plantedDirMode), ShouldBeNil)

		paths = append(paths, path)
	}

	// no assertion here: this runs after the Convey block that called us has
	// ended, where So has nowhere to report to.
	t.Cleanup(func() {
		for _, path := range paths {
			os.RemoveAll(path)
		}
	})

	return paths
}
