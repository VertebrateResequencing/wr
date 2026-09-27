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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// remapTestWait bounds how long TestDBBackupRemap lets a database operation
// take while the backup copy is held open. Unblocked, each operation takes
// milliseconds; blocked behind the copy, it would take until the copy is
// released, which the test only does after this wait.
const remapTestWait = 5 * time.Second

var remapTestBucket = []byte("remapTest") //nolint:gochecknoglobals

const (
	envMmapLimitChild   = "WR_TEST_MMAP_LIMIT_CHILD"
	mmapLimitChildOK    = "opened under RLIMIT_AS"
	mmapLimitChildSlack = 1 << 30
)

func TestManagerInitialMmapSize(t *testing.T) {
	if math.MaxInt < managerMmapMaxSize {
		t.Skip("no headroom is mapped on 32-bit platforms")
	}

	Convey("managerInitialMmapSize maps a manager db with room to grow", t, func() {
		const gib = int64(1) << 30

		Convey("at least managerMmapMinHeadroom beyond a small or new file", func() {
			So(managerInitialMmapSize(0), ShouldEqual, int64(managerMmapMinHeadroom))
			So(managerInitialMmapSize(100<<20), ShouldEqual, int64(100<<20+managerMmapMinHeadroom))
		})

		Convey("twice the size of a multi-GB file", func() {
			So(managerInitialMmapSize(11*gib), ShouldEqual, 22*gib)
		})

		Convey("capped at managerMmapMaxSize, but never below the file's size", func() {
			So(managerInitialMmapSize(600*gib), ShouldEqual, int64(managerMmapMaxSize))
			So(managerInitialMmapSize(2*managerMmapMaxSize), ShouldEqual, int64(2*managerMmapMaxSize))
		})
	})
}

// TestOpenManagerBoltUnderAddressLimit runs a child that limits its own address
// space to less than managerInitialMmapSize's headroom, as ulimit -v would, and
// checks it can still open, write and read a manager db with bbolt's default
// mapping.
func TestOpenManagerBoltUnderAddressLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads the process's address space size from /proc")
	}

	Convey("openManagerBolt falls back to the default mapping when it may not map the headroom", t, func() {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestOpenManagerBoltLimitChild$", //nolint:gosec
			"-test.v")
		cmd.Env = append(os.Environ(), envMmapLimitChild+"="+filepath.Join(t.TempDir(), "queue.db"))

		out, err := cmd.CombinedOutput()
		So(err, ShouldBeNil)
		So(string(out), ShouldContainSubstring, mmapLimitChildOK)
	})
}

// TestOpenManagerBoltLimitChild is the child of
// TestOpenManagerBoltUnderAddressLimit.
func TestOpenManagerBoltLimitChild(t *testing.T) {
	path := os.Getenv(envMmapLimitChild)
	if path == "" {
		t.Skip("child of TestOpenManagerBoltUnderAddressLimit")
	}

	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Fatal(err)
	}

	pages, err := strconv.ParseUint(strings.Fields(string(statm))[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}

	limit := pages*uint64(os.Getpagesize()) + mmapLimitChildSlack //nolint:gosec // page size is positive

	// Only the soft limit is lowered, and never above the hard one, since an
	// unprivileged process cannot raise its hard limit.
	var rlim syscall.Rlimit
	if err = syscall.Getrlimit(syscall.RLIMIT_AS, &rlim); err != nil {
		t.Fatal(err)
	}

	rlim.Cur = min(limit, rlim.Max)
	if err = syscall.Setrlimit(syscall.RLIMIT_AS, &rlim); err != nil {
		t.Fatal(err)
	}

	bdb, err := openManagerBolt(path)
	if err != nil {
		t.Fatal(err)
	}

	err = bdb.Update(func(tx *bolt.Tx) error {
		b, errc := tx.CreateBucketIfNotExists(remapTestBucket)
		if errc != nil {
			return errc
		}

		return b.Put([]byte("k"), []byte("v"))
	})
	if err == nil {
		err = bdb.View(func(tx *bolt.Tx) error {
			if string(tx.Bucket(remapTestBucket).Get([]byte("k"))) != "v" {
				return os.ErrNotExist
			}

			return nil
		})
	}

	if errc := bdb.Close(); err == nil {
		err = errc
	}

	if err != nil {
		t.Fatal(err)
	}

	t.Log(mmapLimitChildOK)
}

// TestDBBackupRemap checks that a write growing the database past bbolt's
// current memory mapping, and every read after it, do not wait for a running
// backup copy's read transaction to finish. bbolt remaps to grow, and a remap
// waits for every open read transaction while holding the write lock and
// blocking new reads, so before the fix the whole database stalled for the rest
// of the copy (prodsim finding 2).
func TestDBBackupRemap(t *testing.T) {
	if math.MaxInt < managerMmapMaxSize {
		t.Skip("no headroom is mapped on 32-bit platforms, so a backup can still stall a remap there")
	}

	ctx := context.Background()

	Convey("Given a small db whose backup copy is held open mid-copy", t, func() {
		tmpdir := t.TempDir()
		dbFile := filepath.Join(tmpdir, "queue.db")

		testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		info, err := os.Stat(dbFile)
		So(err, ShouldBeNil)

		// bbolt's default mapping of a file this small is at most twice its
		// size, so this value must extend the database past it.
		growValue := make([]byte, 2*info.Size()+1<<20)

		origBytes, origHook := backupCopySyncBytes, backupPaceHook

		defer func() { backupCopySyncBytes, backupPaceHook = origBytes, origHook }()

		inCopy := make(chan struct{})
		release := make(chan struct{})

		var holdOnce, releaseOnce sync.Once

		releaseCopy := func() { releaseOnce.Do(func() { close(release) }) }

		backupCopySyncBytes = int64(os.Getpagesize())
		backupPaceHook = func() {
			holdOnce.Do(func() {
				close(inCopy)
				<-release
			})
		}

		backupDone := make(chan struct{})

		go func() {
			testDB.backupToBackupFile(ctx, false)
			close(backupDone)
		}()

		defer func() {
			releaseCopy()
			<-backupDone
		}()

		var copyHeld bool

		select {
		case <-inCopy:
			copyHeld = true
		case <-time.After(remapTestWait):
		}

		So(copyHeld, ShouldBeTrue)

		Convey("a write growing the db past its mapping, and a read after it, complete during the copy, "+
			"and the backup is a consistent snapshot",
			func() {
				writeDone := make(chan error, 1)

				go func() {
					writeDone <- testDB.bolt.Update(func(tx *bolt.Tx) error {
						b, errc := tx.CreateBucketIfNotExists(remapTestBucket)
						if errc != nil {
							return errc
						}

						return b.Put([]byte("grow"), growValue)
					})
				}()

				wrote := waitForRemapTestOp(writeDone)

				readDone := make(chan error, 1)

				go func() {
					readDone <- testDB.bolt.View(func(tx *bolt.Tx) error {
						tx.Bucket(remapTestBucket)

						return nil
					})
				}()

				read := waitForRemapTestOp(readDone)

				releaseCopy()
				<-backupDone

				So(wrote, ShouldBeTrue)
				So(read, ShouldBeTrue)

				// the backup's consistency is unchanged: it is the snapshot from
				// before the write.
				bk, errb := bolt.Open(testDB.backupPath, dbFilePermission,
					&bolt.Options{ReadOnly: true, Timeout: time.Second})
				So(errb, ShouldBeNil)

				var hasGrowBucket bool

				So(bk.View(func(tx *bolt.Tx) error {
					hasGrowBucket = tx.Bucket(remapTestBucket) != nil

					return nil
				}), ShouldBeNil)
				So(bk.Close(), ShouldBeNil)
				So(hasGrowBucket, ShouldBeFalse)
			})
	})
}

// waitForRemapTestOp reports whether done delivered a nil error within
// remapTestWait.
func waitForRemapTestOp(done <-chan error) bool {
	select {
	case err := <-done:
		return err == nil
	case <-time.After(remapTestWait):
		return false
	}
}
