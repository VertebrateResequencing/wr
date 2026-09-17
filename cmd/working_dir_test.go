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
	"syscall"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

// permissiveUmask is a umask that clears nothing, so the mode a directory ends
// up with is entirely the mode the code asked for. It is what makes the
// difference between os.ModePerm and an owner-only mode visible: under an
// ordinary umask of 0022 the bug this pins showed as a merely group and world
// READABLE directory, and under 0002 as a group WRITABLE one.
const permissiveUmask = 0

const (
	// ownerOnlyPerm is the mode a working directory wr creates has to have. It
	// is written out rather than taken from managerDirPerm on purpose: a test
	// that compares the code's constant with itself passes whatever that
	// constant is changed to, which is exactly the mistake this file exists to
	// catch.
	ownerOnlyPerm os.FileMode = 0o700

	// sharedDirPerm is the mode an older wr left the working directory with
	// when started under permissiveUmask, and closedSharedDirPerm is what
	// clearing just the other-user write bits off it leaves: nobody else can
	// delete, replace or plant a file, and every other bit is as it was.
	sharedDirPerm       os.FileMode = 0o777
	closedSharedDirPerm os.FileMode = 0o755

	// colleagueReadableDirPerm is a mode somebody set on purpose, to let a
	// group member read the two public certificates. It has no write bits to
	// clear, so it must survive untouched.
	colleagueReadableDirPerm os.FileMode = 0o750

	// stickySharedDirPerm is sharedDirPerm plus the sticky bit, which stops
	// others REMOVING a file they do not own but still lets them create one.
	// Clearing the write bits must keep the sticky bit.
	stickySharedDirPerm       = os.ModeSticky | sharedDirPerm
	closedStickySharedDirPerm = os.ModeSticky | closedSharedDirPerm

	// sharedFilePerm is the mode of a plain file left where the working
	// directory should be. It has write bits to clear, so it would be
	// chmodded if wr did not check that it has a directory.
	sharedFilePerm os.FileMode = 0o666
)

// TestCreateWorkingDirIsOwnerOnly proves the manager's working directory is
// created for its owner alone, and that one somebody else can write to is
// closed. It holds the database, the client token and the TLS key, and
// unlinking a file needs write permission on the DIRECTORY rather than on the
// file, so a shared directory lets any other local user delete or replace
// those however tightly the files themselves are locked down.
func TestCreateWorkingDirIsOwnerOnly(t *testing.T) {
	Convey("A working directory created under a umask that clears nothing is still owner-only", t, func() {
		// the temp dir has to exist before the umask changes, so that the
		// mode asserted on is the one the code chose and not one inherited
		// from a parent made under the same hostile umask.
		dir := filepath.Join(t.TempDir(), ".wr_development")

		mode, logged := createWorkingDirUnderUmask(t, dir)

		So(mode, ShouldEqual, ownerOnlyPerm)
		So(logged, ShouldBeBlank)
	})

	Convey("An existing working directory another user can write to has just those bits taken off it", t, func() {
		dir := makeDirWithMode(t, sharedDirPerm)

		mode, logged := createWorkingDirUnderUmask(t, dir)

		So(mode, ShouldEqual, closedSharedDirPerm)
		So(logged, ShouldContainSubstring, "from drwxrwxrwx to drwxr-xr-x")
	})

	Convey("An existing working directory only its owner can write to is left exactly as it is", t, func() {
		dir := makeDirWithMode(t, colleagueReadableDirPerm)

		mode, logged := createWorkingDirUnderUmask(t, dir)

		So(mode, ShouldEqual, colleagueReadableDirPerm)
		So(logged, ShouldBeBlank)
	})

	Convey("Closing an existing working directory keeps its sticky bit", t, func() {
		dir := makeDirWithMode(t, stickySharedDirPerm)

		mode, _ := createWorkingDirUnderUmask(t, dir)

		So(mode, ShouldEqual, closedStickySharedDirPerm)
	})
}

// TestCreateWorkingDirRefusesNonDirectories proves wr does not chmod something
// that is not a directory at all. It dies on such a path moments later either
// way, but a plain file sitting where the working directory should be is
// somebody else's file, and changing its mode on the way past is a side effect
// nobody asked for.
func TestCreateWorkingDirRefusesNonDirectories(t *testing.T) {
	Convey("A plain file where the working directory should be keeps its mode", t, func() {
		path := filepath.Join(t.TempDir(), ".wr_development")
		So(os.WriteFile(path, []byte("not a directory"), sharedFilePerm), ShouldBeNil)
		So(os.Chmod(path, sharedFilePerm), ShouldBeNil)

		mode, logged := createWorkingDirUnderUmask(t, path)

		So(mode, ShouldEqual, sharedFilePerm)
		So(logged, ShouldBeBlank)
	})
}

// TestCreateWorkingDirRefusesSymlinkedDir proves wr will not chmod through a
// symlink standing where its working directory should be. os.Chmod follows
// one, so following it would aim the chmod at a target somebody else chose;
// /tmp is used as that target because it is world-writable, owned by root and
// so must come back untouched.
func TestCreateWorkingDirRefusesSymlinkedDir(t *testing.T) {
	Convey("A working directory that is a symlink is warned about rather than chmodded", t, func() {
		before := modeOf("/tmp")

		dir := filepath.Join(t.TempDir(), ".wr_development")
		So(os.Symlink("/tmp", dir), ShouldBeNil)

		_, logged := createWorkingDirUnderUmask(t, dir)

		So(modeOf("/tmp"), ShouldEqual, before)
		So(logged, ShouldContainSubstring, "is a symlink, so wr will not change its mode")
		So(logged, ShouldContainSubstring, "chmod g-w,o-w "+dir)
	})
}

func TestOpenWorkingDirKeepsHandleAfterPathSwap(t *testing.T) {
	Convey("A handle opened before a path swap still names the original directory", t, func() {
		parent := t.TempDir()
		dir := filepath.Join(parent, "manager")
		victim := filepath.Join(parent, "victim")

		So(os.Mkdir(dir, sharedDirPerm), ShouldBeNil)
		So(os.Mkdir(victim, sharedDirPerm), ShouldBeNil)
		So(os.Chmod(dir, sharedDirPerm), ShouldBeNil)
		So(os.Chmod(victim, sharedDirPerm), ShouldBeNil)

		file, err := openWorkingDir(dir)
		So(err, ShouldBeNil)

		defer file.Close()

		So(os.Rename(dir, dir+".real"), ShouldBeNil)
		So(os.Symlink(victim, dir), ShouldBeNil)
		So(file.Chmod(closedSharedDirPerm), ShouldBeNil)

		So(modeOf(victim), ShouldEqual, sharedDirPerm)
		So(modeOf(dir+".real"), ShouldEqual, closedSharedDirPerm)
	})
}

func TestOpenWorkingDirClassifiesSymlink(t *testing.T) {
	Convey("A symlink opened after the initial directory check is classified as a symlink", t, func() {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		dir := filepath.Join(parent, "manager")

		So(os.Mkdir(target, sharedDirPerm), ShouldBeNil)
		So(os.Symlink("target", dir), ShouldBeNil)

		_, err := openWorkingDir(dir)

		So(errors.Is(err, errDirIsSymlink), ShouldBeTrue)
	})
}

func TestOpenWorkingDirClassifiesRejectedFinalSymlink(t *testing.T) {
	Convey("A final symlink rejected by the root is classified as a symlink", t, func() {
		parent := t.TempDir()
		dir := filepath.Join(parent, "manager")

		So(os.Symlink(filepath.Join(t.TempDir(), "outside"), dir), ShouldBeNil)

		_, err := openWorkingDir(dir)

		So(errors.Is(err, errDirIsSymlink), ShouldBeTrue)
	})
}

// createWorkingDirUnderUmask points config at dir and calls createWorkingDir
// with the process umask set to permissiveUmask, returning the mode dir then
// has - sticky bit included, which os.FileMode.Perm() would hide - and
// everything the call logged at info level or above.
//
// syscall.Umask is process-wide and every test in a package shares one process,
// so the umask is restored as the helper returns (deferred, so a failed
// assertion inside cannot leak it) rather than at the end of the test. No test
// in this package calls t.Parallel(), so no other test body can run inside this
// window, but background goroutines outliving earlier tests could still create
// files in it, and one call is as narrow as the window gets.
func createWorkingDirUnderUmask(t *testing.T, dir string) (os.FileMode, string) {
	t.Helper()

	originalConfig, originalCmdExit := config, cmdExit
	config = &internal.Config{ManagerDir: dir}
	cmdExit = func(code int) {
		panic(commandExitPanic{code: code})
	}

	logged := clog.ToBufferAtLevel("info")

	defer func() {
		config, cmdExit = originalConfig, originalCmdExit

		clog.ToDefault()
	}()

	previous := syscall.Umask(permissiveUmask)
	defer syscall.Umask(previous)

	So(recoverCommandExit(createWorkingDir), ShouldEqual, 0)

	return modeOf(dir), logged.String()
}

// modeOf returns the mode of path, sticky bit included.
func modeOf(path string) os.FileMode {
	fi, err := os.Stat(path)
	So(err, ShouldBeNil)

	return fi.Mode() &^ os.ModeDir
}

// makeDirWithMode makes a working directory with exactly mode, chmodding after
// the create because the ambient umask would otherwise clear bits from it.
func makeDirWithMode(t *testing.T, mode os.FileMode) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), ".wr_development")
	So(os.Mkdir(dir, mode), ShouldBeNil)
	So(os.Chmod(dir, mode), ShouldBeNil)

	return dir
}
