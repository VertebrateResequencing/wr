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
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/muxfys/v5"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// muxFysFSType is how the kernel names a muxfys mount in the mount table.
	muxFysFSType = "fuse.MuxFys"

	// mountInfoPath lists this host's mounts with, unlike /proc/mounts, the
	// device number we need to find a mount's fuse connection.
	mountInfoPath = "/proc/self/mountinfo"

	// mountInfoDevField and mountInfoPointField are the major:minor and mount
	// point columns of a mountInfoPath line, counting from its start (the
	// columns after the " - " separator are counted separately, because the
	// variable number of optional fields sits just before it).
	mountInfoDevField   = 2
	mountInfoPointField = 4

	// fuseConnectionsDir holds one directory per live fuse connection, named
	// after the device minor number of the mount that made it.
	fuseConnectionsDir = "/sys/fs/fuse/connections"

	// fuseAbortFileMode is only ever used if the abort file is missing, in
	// which case there is no connection to abort and the write fails anyway.
	fuseAbortFileMode = 0o200

	// mountReleaseTimeout bounds every wait for a mount to go away, and
	// processWaitTimeout every wait for a process to reach a state. awaitPoll
	// is how often either is checked.
	mountReleaseTimeout = 30 * time.Second
	processWaitTimeout  = 30 * time.Second
	awaitPoll           = 100 * time.Millisecond

	// mountAbortGrace is how long a lazily unmounted MuxFys gets to finish by
	// itself before its connection is aborted. It is short because a connection
	// that outlives its detached mount has something blocked on it, and that
	// something is already past the point where waiting helps it.
	mountAbortGrace = 2 * time.Second

	// mountTestTimeout bounds a mount test, well under the suite's 40m
	// -test.timeout: a wedged mount would otherwise hang the whole run for 40
	// minutes and still leave the mount behind, where this fails the run in
	// minutes and releases the mount so the wedged goroutine can move on.
	mountTestTimeout = 10 * time.Minute

	// envMuxFysMountChild makes TestMuxFysMountChild do its work instead of
	// skipping, and envMuxFysMountWedge makes it wedge itself in its own mount.
	envMuxFysMountChild = "WR_TEST_MUXFYS_CHILD"
	envMuxFysMountWedge = "WR_TEST_MUXFYS_WEDGE"

	// fuseWaitWchan is what /proc reports a thread blocked in a fuse request as
	// waiting on.
	fuseWaitWchan = "request_wait_answer"

	// muxFysChildReport prefixes the child's report of the mount it made.
	muxFysChildReport = "MUXFYSMOUNT="
)

// errEmptyRemote is what every emptyRemote operation other than listing fails
// with.
var errEmptyRemote = errors.New("not in an empty remote")

// muxFysMount is one MuxFys mount in this host's mount table.
type muxFysMount struct {
	// point is the absolute path the mount is at.
	point string

	// connection is the mount's device minor number, which is also the name of
	// its directory in fuseConnectionsDir.
	connection string
}

// parseMuxFysMount returns the mount one mountInfoPath line describes, if that
// line is a MuxFys mount.
func parseMuxFysMount(line string) (muxFysMount, bool) {
	before, after, found := strings.Cut(line, " - ")
	if !found {
		return muxFysMount{}, false
	}

	if fsType := strings.Fields(after); len(fsType) == 0 || fsType[0] != muxFysFSType {
		return muxFysMount{}, false
	}

	fields := strings.Fields(before)
	if len(fields) <= mountInfoPointField {
		return muxFysMount{}, false
	}

	major, minor, found := strings.Cut(fields[mountInfoDevField], ":")
	if !found || major != "0" { // a fuse mount is always on an anonymous device
		return muxFysMount{}, false
	}

	return muxFysMount{point: unescapeMountPoint(fields[mountInfoPointField]), connection: minor}, true
}

// awaitNoMuxFysMountsUnder waits up to timeout for there to be no MuxFys mounts
// under dir, returning any still there when it gives up.
func awaitNoMuxFysMountsUnder(dir string, timeout time.Duration) []muxFysMount {
	var left []muxFysMount

	awaitTrue(timeout, func() bool {
		left = muxFysMountsUnder(dir)

		return len(left) == 0
	})

	return left
}

// awaitTrue polls ok every awaitPoll until it is true or timeout passes,
// saying which happened.
func awaitTrue(timeout time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(timeout)

	for {
		if ok() {
			return true
		}

		if time.Now().After(deadline) {
			return false
		}

		time.Sleep(awaitPoll)
	}
}

// muxFysMountsUnder returns the MuxFys mounts inside dir.
func muxFysMountsUnder(dir string) []muxFysMount {
	prefix := dir + string(filepath.Separator)

	var under []muxFysMount

	for _, mount := range muxFysMounts() {
		if strings.HasPrefix(mount.point, prefix) {
			under = append(under, mount)
		}
	}

	return under
}

// releaseMuxFysMount detaches mount from the mount namespace and, if that does
// not finish the job, aborts its fuse connection.
//
// The unmount has to be lazy because a plain one cannot detach a mount that
// something is still blocked in: fusermount -u on those returns "Device or
// resource busy". The abort is needed because a lazy unmount does not end an
// in-flight fuse request, so a process stuck in one stays in uninterruptible
// sleep, with its SIGKILL queued and undeliverable, until the connection goes.
func releaseMuxFysMount(mount muxFysMount) {
	ino, known := fuseConnectionIno(mount.connection)

	lazyUnmount(mount.point)

	if awaitMuxFysMountGone(mount, mountAbortGrace) || !known {
		return
	}

	abortFuseConnection(mount.connection, ino)
}

// fuseConnectionIno returns the inode of the numbered fuse connection's
// directory, if that directory exists and belongs to this user.
func fuseConnectionIno(connection string) (uint64, bool) {
	info, err := os.Stat(filepath.Join(fuseConnectionsDir, connection))
	if err != nil {
		return 0, false
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return 0, false
	}

	return stat.Ino, true
}

// lazyUnmount detaches the mount at point from the mount namespace, giving up
// if fusermount itself wedges.
//
// The wait is in a goroutine on purpose: exec.CommandContext kills its process
// when the context expires but still Waits for it, and a fusermount stuck in
// uninterruptible sleep can never be reaped - which would hang TestMain before
// m.Run() has armed any -test.timeout watchdog, the exact failure this file
// exists to stop.
func lazyUnmount(point string) {
	cmd := exec.Command("fusermount", "-uz", point) //nolint:noctx // see the goroutine below

	if cmd.Start() != nil {
		return
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		cmd.Wait() //nolint:errcheck // a refused unmount is what aborting the connection is for
	}()

	select {
	case <-done:
	case <-time.After(mountReleaseTimeout):
		cmd.Process.Kill() //nolint:errcheck // the goroutine is left to reap it if it ever can
	}
}

// abortFuseConnection aborts the numbered fuse connection, which fails
// everything blocked in a request on it so a queued SIGKILL can be delivered.
//
// It writes only if the connection directory is still the same inode
// releaseMuxFysMount saw before unmounting. Connection numbers are reused, so
// the number alone does not identify a connection: another user's is already
// unreachable (fuse gives the directory the connecting user's uid, and abort is
// mode 0200), but a reallocation to another of this user's mounts within
// mountAbortGrace is possible in principle, and aborting that one would be
// exactly the sort of collateral damage this file is meant to avoid.
func abortFuseConnection(connection string, ino uint64) {
	current, known := fuseConnectionIno(connection)
	if !known || current != ino {
		return
	}

	//nolint:errcheck // there is nothing further to try if the abort itself fails
	os.WriteFile(filepath.Join(fuseConnectionsDir, connection, "abort"), []byte("1"), fuseAbortFileMode)
}

// awaitMuxFysMountGone waits up to timeout for mount to leave the mount table
// and its fuse connection to close, saying whether both happened.
func awaitMuxFysMountGone(mount muxFysMount, timeout time.Duration) bool {
	return awaitTrue(timeout, func() bool {
		return !muxFysMountExists(mount.point) && !fuseConnectionExists(mount.connection)
	})
}

// fuseConnectionExists says whether the numbered fuse connection is still open.
func fuseConnectionExists(connection string) bool {
	_, err := os.Stat(filepath.Join(fuseConnectionsDir, connection))

	return err == nil
}

// muxFysMountExists says whether point is currently a MuxFys mount.
func muxFysMountExists(point string) bool {
	return slices.ContainsFunc(muxFysMounts(), func(mount muxFysMount) bool {
		return mount.point == point
	})
}

// muxFysMounts returns every MuxFys mount in this host's mount table, including
// other users'; callers must narrow that down themselves.
func muxFysMounts() []muxFysMount {
	content, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil
	}

	var mounts []muxFysMount

	for line := range strings.SplitSeq(string(content), "\n") {
		if mount, ok := parseMuxFysMount(line); ok {
			mounts = append(mounts, mount)
		}
	}

	return mounts
}

// emptyRemote is a muxfys.RemoteAccessor for a remote with nothing in it, so a
// test can make a real MuxFys mount without needing an object store.
type emptyRemote struct{}

func (emptyRemote) DownloadFile(_, _ string) error { return errEmptyRemote }

func (emptyRemote) UploadFile(_, _, _ string) error { return errEmptyRemote }

func (emptyRemote) UploadData(_ io.Reader, _ string) error { return errEmptyRemote }

func (emptyRemote) CopyFile(_, _ string) error { return errEmptyRemote }

func (emptyRemote) DeleteFile(_ string) error { return errEmptyRemote }

func (emptyRemote) DeleteIncompleteUpload(_ string) error { return errEmptyRemote }

func (emptyRemote) ErrorIsNoQuota(error) bool { return false }

func (emptyRemote) Target() string { return "empty" }

func (emptyRemote) RemotePath(relPath string) string { return relPath }

func (emptyRemote) ListEntries(_ string) ([]muxfys.RemoteAttr, error) {
	return nil, nil
}

func (emptyRemote) OpenFile(_ string, _ int64) (io.ReadCloser, error) {
	return nil, errEmptyRemote
}

func (emptyRemote) Seek(_ string, _ io.ReadCloser, _ int64) (io.ReadCloser, error) {
	return nil, errEmptyRemote
}

func (emptyRemote) ErrorIsNotExists(err error) bool {
	return errors.Is(err, errEmptyRemote)
}

func (emptyRemote) LocalPath(baseDir, remotePath string) string {
	return filepath.Join(baseDir, remotePath)
}

// mountMuxFys mounts a MuxFys at a "cwd" inside dir - where a job with a
// MountConfig mounts one - and returns the mount point. With blocking set, the
// remote never answers, so reading the mount blocks in the kernel for good.
func mountMuxFys(dir string, blocking bool) (string, *muxfys.MuxFys, error) {
	point := filepath.Join(dir, "cwd")

	fs, err := muxfys.New(&muxfys.Config{Mount: point, CacheBase: dir})
	if err != nil {
		return "", nil, err
	}

	var accessor muxfys.RemoteAccessor = emptyRemote{}
	if blocking {
		accessor = blockingRemote{}
	}

	if err = fs.Mount(&muxfys.RemoteConfig{Accessor: accessor}); err != nil {
		return "", nil, err
	}

	return point, fs, nil
}

// blockingRemote is an emptyRemote that never answers a listing, so a read of a
// mount using it blocks inside the kernel's fuse request wait - where the
// D-state processes a killed mount test leaves behind were found.
type blockingRemote struct{ emptyRemote }

func (blockingRemote) ListEntries(_ string) ([]muxfys.RemoteAttr, error) {
	select {}
}

// childMuxFysMount is a MuxFys mount made by a child of this test binary, still
// running so that its mount is nobody else's to reap.
type childMuxFysMount struct {
	point string
	pid   int
	cmd   *exec.Cmd
}

// startChildMuxFysMount runs this test binary as a child that makes one MuxFys
// mount and waits, and returns it still running. With wedge set the child will
// be unable to die once killed; see TestMuxFysMountChild.
func startChildMuxFysMount(t *testing.T, wedge bool) childMuxFysMount {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestMuxFysMountChild$") //nolint:gosec

	cmd.Env = append(os.Environ(), envMuxFysMountChild+"=1")

	if wedge {
		cmd.Env = append(cmd.Env, envMuxFysMountWedge+"=1")
	}

	stdout, err := cmd.StdoutPipe()
	So(err, ShouldBeNil)
	So(cmd.Start(), ShouldBeNil)

	point := scanMuxFysChildReport(stdout)

	// this runs whatever the assertions do, so a broken reaper cannot leave a
	// wedged process on the host.
	t.Cleanup(func() {
		releaseMuxFysMountsUnder(filepath.Dir(point))
		os.RemoveAll(filepath.Dir(point))
	})

	return childMuxFysMount{point: point, pid: cmd.Process.Pid, cmd: cmd}
}

// killAndSettle kills the child with its mount still up - what a timeout
// wrapper does to a run - and waits for it to settle into being gone or wedged.
func (c childMuxFysMount) killAndSettle() {
	So(c.cmd.Process.Kill(), ShouldBeNil)

	// a wedged child never dies, so its Wait can never be the one we block on.
	go func() {
		c.cmd.Wait() //nolint:errcheck // the child was killed, so this only reaps it
	}()

	So(awaitPidDoneFor(c.pid, processWaitTimeout), ShouldBeTrue)
}

// awaitPidDoneFor waits up to timeout for pid to settle into one of the two
// states a killed process reaches - gone, or wedged with an undeliverable
// SIGKILL - saying whether it did.
//
// Which of the two it lands in is not asserted, because it is not ours to
// decide: any other test binary starting in the meantime reaps a wedged
// process's mount, which frees it. Both states are ones the reaper must handle,
// and both are pidDoneFor.
func awaitPidDoneFor(pid int, timeout time.Duration) bool {
	return awaitTrue(timeout, func() bool {
		return pidDoneFor(pid)
	})
}

func TestFuseMountReaping(t *testing.T) {
	if runnermode || servermode {
		return
	}

	live, err := mountMuxFysForTest(t)
	if err != nil {
		SkipConvey("Without a usable fuse mount we can't test mount reaping: "+err.Error(), t, func() {})

		return
	}

	Convey("A killed run's MuxFys mount gets released, but a live run's does not", t, func() {
		child := startChildMuxFysMount(t, false)

		// asserted while the child still lives: the moment it dies its mount is
		// reapable by any other test binary's TestMain, of which a make test
		// run starts dozens, so "the orphan is still there" is not ours to
		// claim after the kill.
		So(muxFysMountExists(child.point), ShouldBeTrue)
		So(muxFysMountExists(live), ShouldBeTrue)

		child.killAndSettle()

		reapDeadTestMounts()

		So(muxFysMountExists(child.point), ShouldBeFalse)
		So(muxFysMountExists(live), ShouldBeTrue)
	})

	Convey("Reaping frees a killed run that kill -9 left wedged in its own mount", t, func() {
		child := startChildMuxFysMount(t, true)
		So(muxFysMountExists(child.point), ShouldBeTrue)

		// a thread already blocked in a fuse request is what makes the kill
		// below leave the process wedged rather than dead, so this is the
		// premise of the whole case, and it holds only while the child lives.
		So(pidInFuseWait(child.pid), ShouldBeTrue)

		child.killAndSettle()

		reapDeadTestMounts()

		So(muxFysMountExists(child.point), ShouldBeFalse)
		So(awaitPidGone(child.pid, processWaitTimeout), ShouldBeTrue)
		So(muxFysMountExists(live), ShouldBeTrue)
	})
}

// mountMuxFysForTest makes a MuxFys mount owned by this live test process, and
// unmounts it when the test ends.
func mountMuxFysForTest(t *testing.T) (string, error) {
	t.Helper()

	dir, err := newTestTempDir("mounts")
	if err != nil {
		return "", err
	}

	point, fs, err := mountMuxFys(dir, false)
	if err != nil {
		return "", err
	}

	t.Cleanup(func() {
		fs.Unmount() //nolint:errcheck // the reaper is the backstop if this fails
	})

	return point, nil
}

// reapDeadTestMounts releases every MuxFys mount an earlier run of this test
// binary left behind because it was killed instead of exiting through TestMain.
//
// Nothing inside a process can defend against its own SIGKILL, so this is the
// only thing that fixes the damage a killed mount test does: the mount stays in
// the host's mount table with its fuse connection open, and anything left
// blocked in a fuse request on it sits in uninterruptible sleep where a further
// kill -9 cannot reach it. On a shared login node those survive indefinitely -
// this host had three such mounts and processes aged 3, 8 and 30 days.
//
// Three things must be true at once for a mount to be touched, so it can never
// reach another user's mount, a non-MuxFys mount, anything outside os.TempDir()
// or a live run's mount: the filesystem type is fuse.MuxFys; the mount point is
// inside a newTestTempDir directory in os.TempDir(); and the pid that directory
// name encodes - the pid of the process that CREATED it, per
// reapDeadTestTempDirs - is done for, per pidDoneFor.
//
// A mount made by a PRE-FIX binary is under a t.TempDir() directory, which
// encodes no pid, so it matches nothing here; this host's were released by hand.
//
// Freeing a wedged process here does not also clear its temp dirs on this run:
// it is still a zombie when reapDeadTestTempDirs runs microseconds later, so
// its pid still "exists" and its dirs go on the run after this one.
func reapDeadTestMounts() {
	for _, mount := range muxFysMounts() {
		if pid, ok := testTempDirMountPid(mount.point); ok && pidDoneFor(pid) {
			releaseMuxFysMount(mount)
		}
	}
}

// testTempDirMountPid returns the pid encoded in the newTestTempDir directory
// that point is inside, if it is inside one.
func testTempDirMountPid(point string) (int, bool) {
	rel, err := filepath.Rel(os.TempDir(), point)
	if err != nil {
		return 0, false
	}

	dir, _, nested := strings.Cut(rel, string(filepath.Separator))
	if !nested || !strings.HasPrefix(dir, testTempDirPrefix) {
		return 0, false
	}

	return testTempDirPid(dir)
}

// pidDoneFor says whether pid is a process nothing need respect any more:
// either it is gone, or it is wedged in uninterruptible sleep with a SIGKILL it
// can never receive.
//
// That second case is not an edge case, it is the observed one. A mount test
// killed mid-mount leaves its own binary blocked in a fuse request that its own
// (now dead) daemon goroutines will never answer, so the SIGKILL that killed it
// stays queued in ShdPnd, `kill -9` does nothing at all, and the pid lives on
// for as long as the machine does - this host had such processes at 3, 8 and 30
// days old. Keying only on "the pid is gone" would leave exactly those alone,
// and releasing their mount is the one thing that lets them finish dying.
//
// A pending SIGKILL is what makes this safe to act on: it cannot be caught,
// blocked or ignored, so a process that has one is already dead in every sense
// but the bookkeeping. It also means an operator whose `kill -9` appeared to do
// nothing has, in fact, put the process in scope for the next run to clear.
func pidDoneFor(pid int) bool {
	if !pidExists(pid) {
		return true
	}

	// every thread is checked because the wedged one is usually not the group
	// leader, and the leader has by then exited, so the process-wide status
	// reports only "zombie".
	return slices.ContainsFunc(procThreadStatuses(pid), func(status map[string]string) bool {
		return strings.HasPrefix(status["State"], "D") && sigKillPending(status["ShdPnd"])
	})
}

// procThreadStatuses reads the /proc status of every thread of a process.
func procThreadStatuses(pid int) []map[string]string {
	paths, err := filepath.Glob(filepath.Join("/proc", strconv.Itoa(pid), "task", "*", "status"))
	if err != nil {
		return nil
	}

	statuses := make([]map[string]string, 0, len(paths))

	for _, path := range paths {
		if status := procStatus(path); status != nil {
			statuses = append(statuses, status)
		}
	}

	return statuses
}

// procStatus reads a /proc status file into its field names and values.
func procStatus(path string) map[string]string {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	status := make(map[string]string)

	for line := range strings.SplitSeq(string(content), "\n") {
		if name, value, found := strings.Cut(line, ":"); found {
			status[name] = strings.TrimSpace(value)
		}
	}

	return status
}

// sigKillPending says whether a hex signal mask from procStatus has SIGKILL in
// it. Signal n is bit n-1.
func sigKillPending(mask string) bool {
	bits, err := strconv.ParseUint(mask, 16, 64)

	return err == nil && bits&(1<<(syscall.SIGKILL-1)) != 0
}

// pidInFuseWait says whether any thread of pid is blocked in a fuse request.
// While that is true the process cannot be killed, which is what makes a
// wedge reproducible: kill it now and it is stuck until the connection ends.
func pidInFuseWait(pid int) bool {
	tasks, err := filepath.Glob(filepath.Join("/proc", strconv.Itoa(pid), "task", "*", "wchan"))
	if err != nil {
		return false
	}

	return slices.ContainsFunc(tasks, func(task string) bool {
		wchan, errr := os.ReadFile(task)

		return errr == nil && string(wchan) == fuseWaitWchan
	})
}

// awaitPidGone waits up to timeout for pid to stop existing, saying whether it
// did.
func awaitPidGone(pid int, timeout time.Duration) bool {
	return awaitTrue(timeout, func() bool {
		return !pidExists(pid)
	})
}

// TestMuxFysMountChild is the child half of TestFuseMountReaping: it makes one
// MuxFys mount inside a temp dir named after its own pid, reports it, and waits
// to be killed with the mount still up.
//
// With envMuxFysMountWedge set it also reads its own mount, from a remote that
// never answers, and waits until that read is in flight before reporting. Being
// killed then leaves it in uninterruptible sleep with an undeliverable SIGKILL,
// which is what a killed TestJobqueueWithMounts leaves behind.
func TestMuxFysMountChild(t *testing.T) {
	if os.Getenv(envMuxFysMountChild) == "" {
		t.Skip("child of TestFuseMountReaping")
	}

	dir, err := newTestTempDir("mounts")
	if err != nil {
		t.Fatal(err)
	}

	wedge := os.Getenv(envMuxFysMountWedge) != ""

	point, _, err := mountMuxFys(dir, wedge)
	if err != nil {
		t.Fatal(err)
	}

	if wedge {
		go func() {
			os.ReadDir(point) //nolint:errcheck // this never returns; blocking is the point
		}()

		if !awaitTrue(processWaitTimeout, func() bool { return pidInFuseWait(os.Getpid()) }) {
			t.Fatal("no thread of this process ended up waiting on its own mount")
		}
	}

	fmt.Println(muxFysChildReport + point) //nolint:forbidigo

	time.Sleep(mountTestTimeout)
}

// scanMuxFysChildReport reads the child's output until it reports its mount.
func scanMuxFysChildReport(stdout io.Reader) string {
	scanner := bufio.NewScanner(stdout)

	for scanner.Scan() {
		if reported, found := strings.CutPrefix(scanner.Text(), muxFysChildReport); found {
			return reported
		}
	}

	So(scanner.Err(), ShouldBeNil)
	So("child never reported a mount", ShouldBeBlank)

	return ""
}

// failMountTestOnTimeout fails t, and releases the mounts under dir, if t is
// still running mountTestTimeout from now.
//
// Releasing the mounts is what actually unwedges it: a goroutine blocked in a
// fuse request served by its own process cannot be interrupted, so nothing
// short of ending the connection gets it moving again, and until it moves the
// test can neither fail nor clean up after itself.
func failMountTestOnTimeout(t *testing.T, dir string) {
	t.Helper()

	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		select {
		case <-done:
		case <-time.After(mountTestTimeout):
			t.Errorf("mount test still running after %s; releasing its mounts under %s", mountTestTimeout, dir)
			releaseMuxFysMountsUnder(dir)
		}
	}()

	t.Cleanup(func() {
		close(done)
		<-stopped
	})
}

// releaseMuxFysMountsUnder waits a bounded time for the MuxFys mounts under dir
// to go away by themselves, then releases whatever is left, returning the mount
// points it had to release.
//
// This is the in-process half of the fix, and it only covers a test that is
// still running: a mount test that ends with a mount up would leave it on the
// host for as long as the test binary lives, and for good if that binary is
// then killed. It cannot help a binary that is killed with a mount up; only
// reapDeadTestMounts, in a later run, can.
func releaseMuxFysMountsUnder(dir string) []string {
	left := awaitNoMuxFysMountsUnder(dir, mountReleaseTimeout)
	points := make([]string, 0, len(left))

	for _, mount := range left {
		releaseMuxFysMount(mount)

		points = append(points, mount.point)
	}

	return points
}

// unescapeMountPoint undoes the octal escaping mountInfoPath applies to the
// characters that would otherwise break its own column format.
func unescapeMountPoint(point string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(point)
}
