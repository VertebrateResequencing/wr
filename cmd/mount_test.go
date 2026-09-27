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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/sevlyar/go-daemon"
	. "github.com/smartystreets/goconvey/convey"
)

var errTestSecondMount = errors.New("second mount failed")

// mountTestSignalDelay is how long a fake mount waits for a signal it sent
// itself to be delivered.
const mountTestSignalDelay = 100 * time.Millisecond

// mountTestFirst is the Mount of the first of two test mount configs.
const mountTestFirst = "first"

// fakeMountedFS records whether it was unmounted.
type fakeMountedFS struct {
	unmounted int
}

func (f *fakeMountedFS) Unmount(_ ...bool) error {
	f.unmounted++

	return nil
}

func TestMountFailureUnmountsEarlierMounts(t *testing.T) {
	Convey("Given two mount configs where the second fails to mount", t, func() {
		const failingMount = "second"

		configs := jobqueue.MountConfigs{{Mount: mountTestFirst}, {Mount: failingMount}}
		first := &fakeMountedFS{}

		mount := func(mc jobqueue.MountConfig) (mountedFS, error) {
			if mc.Mount == failingMount {
				return nil, errTestSecondMount
			}

			return first, nil
		}

		logged := clog.ToBufferAtLevel("error")
		originalCmdExit := cmdExit
		cmdExit = func(code int) {
			panic(commandExitPanic{code: code})
		}

		defer func() {
			cmdExit = originalCmdExit

			clog.ToDefault()
		}()

		exitCode := recoverCommandExit(func() {
			mountAndWait(configs, mount)
		})

		Convey("wr mount exits non-zero with the error, having unmounted the first", func() {
			So(exitCode, ShouldEqual, 1)
			So(logged.String(), ShouldContainSubstring, errTestSecondMount.Error())
			So(first.unmounted, ShouldEqual, 1)
		})
	})
}

func TestMountSignalMidwayUnmountsEarlierMounts(t *testing.T) {
	Convey("Given two mount configs and a SIGTERM that arrives while the first is mounting", t, func() {
		configs := jobqueue.MountConfigs{{Mount: mountTestFirst}, {Mount: "second"}}
		first := &fakeMountedFS{}

		mount := func(mc jobqueue.MountConfig) (mountedFS, error) {
			if mc.Mount == mountTestFirst {
				So(syscall.Kill(os.Getpid(), syscall.SIGTERM), ShouldBeNil)

				// give the signal time to be delivered before this mount ends
				time.Sleep(mountTestSignalDelay)
			}

			return first, nil
		}

		mountAndWait(configs, mount)

		Convey("wr mount unmounts what it mounted and returns, instead of dying with it live", func() {
			So(first.unmounted, ShouldEqual, 1)
		})
	})
}

func TestMountResolvesToUsersDir(t *testing.T) {
	Convey("Given a user who mounts a bucket with --mounts and no other options", t, func() {
		dir := dirForTest(t)
		otherDir := dirForTest(t)

		setMountOptionsForTest(t, "", "cw:mybucket/path")

		Convey("the mount point and cache are in the directory they ran wr mount in", func() {
			setMountParentForTest(t, dir, "")

			configs := resolvedMountConfigs()

			So(configs, ShouldHaveLength, 1)
			So(configs[0].Mount, ShouldEqual, filepath.Join(dir, "mnt"))
			So(configs[0].CacheBase, ShouldEqual, dir)
		})

		Convey("a WR_MOUNT_CWD of their own does not move them elsewhere", func() {
			setMountParentForTest(t, dir, otherDir)

			configs := resolvedMountConfigs()

			So(configs, ShouldHaveLength, 1)
			So(configs[0].Mount, ShouldEqual, filepath.Join(dir, "mnt"))
			So(configs[0].CacheBase, ShouldEqual, dir)
		})

		Convey("nor does it reach the daemon they start", func() {
			setMountParentForTest(t, dir, otherDir)

			So(mountCwdEnvValues(mountDaemonEnv(mountCwd())), ShouldResemble, []string{dir})
		})

		Convey("they are in that directory for the daemon too, which runs from /", func() {
			setMountDaemonForTest(t, dir)

			configs := resolvedMountConfigs()

			So(configs, ShouldHaveLength, 1)
			So(configs[0].Mount, ShouldEqual, filepath.Join(dir, "mnt"))
			So(configs[0].CacheBase, ShouldEqual, dir)
		})
	})

	Convey("Given a user who mounts with a relative --mount_json CacheDir", t, func() {
		dir := dirForTest(t)

		setMountOptionsForTest(t, `[{"Mount":"mymnt","Targets":[{"Path":"mybucket","CacheDir":"mycache"}]}]`, "")

		Convey("the mount point and cache dir are in the directory the daemon was started from", func() {
			setMountDaemonForTest(t, dir)

			configs := resolvedMountConfigs()

			So(configs, ShouldHaveLength, 1)
			So(configs[0].Mount, ShouldEqual, filepath.Join(dir, "mymnt"))
			So(configs[0].Targets, ShouldHaveLength, 1)
			So(configs[0].Targets[0].CacheDir, ShouldEqual, filepath.Join(dir, "mycache"))
		})
	})
}

// dirForTest returns a symlink-free directory that only exists for the duration
// of the test, since the paths a mount resolves to are compared as the strings
// they are.
func dirForTest(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	So(err, ShouldBeNil)

	return dir
}

// setMountOptionsForTest sets the mount command's option variables for the
// duration of the test, restoring them afterwards.
func setMountOptionsForTest(t *testing.T, jsonString, simpleString string) {
	t.Helper()

	oldJSON := mountJSON
	oldSimple := mountSimple

	t.Cleanup(func() {
		mountJSON = oldJSON
		mountSimple = oldSimple
	})

	mountJSON = jsonString
	mountSimple = simpleString
}

// setMountParentForTest puts this process in the situation of the wr mount the
// user typed: running in cwd, with envCwd as the value of mountCwdEnvVar in its
// environment, which only a daemonized mount may believe.
func setMountParentForTest(t *testing.T, cwd, envCwd string) {
	t.Helper()

	setMountProcessForTest(t, cwd, envCwd, "")
}

// mountCwdEnvValues returns the value of every mountCwdEnvVar entry in the
// given environment, in the order the child process would see them: with a
// duplicated name, os.Getenv answers with the first.
func mountCwdEnvValues(env []string) []string {
	values := make([]string, 0, 1)

	for _, nameValue := range env {
		if value, ok := strings.CutPrefix(nameValue, mountCwdEnvVar+"="); ok {
			values = append(values, value)
		}
	}

	return values
}

// setMountDaemonForTest puts this process in the situation of a daemonized
// mount: re-executed by go-daemon with its mark set, running from "/", and told
// the user's directory in mountCwdEnvVar.
func setMountDaemonForTest(t *testing.T, envCwd string) {
	t.Helper()

	setMountProcessForTest(t, "/", envCwd, daemon.MARK_VALUE)
}

// setMountProcessForTest gives this process the working directory,
// mountCwdEnvVar value and go-daemon mark of the wr mount process being
// modelled. Each of the three is always set, since t.Chdir and t.Setenv only
// undo themselves when the test as a whole ends, not when a Convey leaf does.
func setMountProcessForTest(t *testing.T, cwd, envCwd, daemonMark string) {
	t.Helper()

	t.Setenv(daemon.MARK_NAME, daemonMark)
	t.Setenv(mountCwdEnvVar, envCwd)
	t.Chdir(cwd)
}
