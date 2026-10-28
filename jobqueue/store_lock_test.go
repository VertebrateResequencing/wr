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

// Tests of opening a store another manager holds, or whose files are corrupt,
// through openStore, so that they run on every store kind (see forEachStore).
// A manager that loses the lock must change nothing: not the winner's store,
// not its backup, and not the leftovers of a restore beside them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// storeLockOpenTimeout is the managerDBOpenTimeout these tests run with,
	// so a test of the lock waits well under a second rather than 30s.
	storeLockOpenTimeout = 500 * time.Millisecond

	// storeLockRepGroup is the RepGroup of the job storeLockSeeded stores.
	storeLockRepGroup = "store-lock"

	// storeLockCorruptBytes is how many random bytes replace a corrupted store.
	storeLockCorruptBytes = 4096

	// storeLockCorruptSeed seeds those bytes, so a failure reproduces.
	storeLockCorruptSeed = 7
)

// storeOpenResult is what a bounded openStore call came back with.
type storeOpenResult struct {
	s   store
	msg string
	err error
}

// openStoreBounded calls openStore on a goroutine and returns its result or,
// if it has not returned within managerDBOpenTimeout + dgbDeadlineSlack, an
// empty result having reported the hang as a named failure. The caller closes
// any store it gets back, with closeOpenResult. On a hang a second goroutine
// closes whatever the blocked call eventually opens.
func openStoreBounded(ctx context.Context, cfg storeConfig) storeOpenResult {
	results := make(chan storeOpenResult, 1)

	go func() {
		s, msg, err := openStore(ctx, cfg)
		results <- storeOpenResult{s: s, msg: msg, err: err}
	}()

	select {
	case result := <-results:
		return result
	case <-time.After(managerDBOpenTimeout + dgbDeadlineSlack):
		go func() { closeOpenResult(ctx, <-results) }()

		So("openStore did not return within the database open timeout", ShouldBeBlank)

		return storeOpenResult{}
	}
}

func TestStoreOpenLocked(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store held open, with a backup and restore leftovers beside it",
			storeKindName(kind)), t, func() {
			withStoreLockOpenTimeout()

			dir, cfg := storeLockSeeded(ctx, t, kind)
			holder := openStoreIn(ctx, kind, dir)

			Reset(func() { So(holder.close(ctx), ShouldBeNil) })

			plantRestoreLeftovers(cfg.dbFile)

			Convey("a second open fails with ErrDBLocked, touches no file, and the holder still works", func() {
				before := storeDirIdentity(t, dir)

				result := openStoreBounded(ctx, cfg)
				closeOpenResult(ctx, result)

				So(result.s, ShouldBeNil)
				So(errors.Is(result.err, ErrDBLocked), ShouldBeTrue)
				So(result.err.Error(), ShouldContainSubstring, cfg.dbFile)
				So(storeDirIdentity(t, dir), ShouldResemble, before)

				soStoreStillWorks(ctx, holder)
			})

			Convey("with no backup, a second open still fails with ErrDBLocked, touching no file", func() {
				So(os.RemoveAll(cfg.dbBkFile), ShouldBeNil)

				before := storeDirIdentity(t, dir)

				result := openStoreBounded(ctx, cfg)
				closeOpenResult(ctx, result)

				So(errors.Is(result.err, ErrDBLocked), ShouldBeTrue)
				So(storeDirIdentity(t, dir), ShouldResemble, before)
			})
		})

		Convey(fmt.Sprintf("Given a %s store held open only briefly", storeKindName(kind)), t, func() {
			withStoreLockOpenTimeout()

			dir, cfg := storeLockSeeded(ctx, t, kind)
			holder := openStoreIn(ctx, kind, dir)

			released := make(chan error, 1)

			go func() {
				<-time.After(storeLockOpenTimeout / 5)

				released <- holder.close(ctx)
			}()

			Convey("a second open waits for the lock to be released, then opens the store", func() {
				result := openStoreBounded(ctx, cfg)
				closeOpenResult(ctx, result)

				select {
				case err := <-released:
					So(err, ShouldBeNil)
				case <-time.After(storeLockOpenTimeout + dgbDeadlineSlack):
					So("the holder did not close", ShouldBeBlank)
				}

				So(result.err, ShouldBeNil)
				So(result.s, ShouldNotBeNil)
			})
		})

		Convey(fmt.Sprintf("Given a closed %s store, an open takes less than the open timeout",
			storeKindName(kind)), t, func() {
			withStoreLockOpenTimeout()

			_, cfg := storeLockSeeded(ctx, t, kind)

			started := time.Now()
			result := openStoreBounded(ctx, cfg)
			elapsed := time.Since(started)

			closeOpenResult(ctx, result)

			So(result.err, ShouldBeNil)
			So(elapsed, ShouldBeLessThan, storeLockOpenTimeout)
		})
	})
}

// withStoreLockOpenTimeout lowers managerDBOpenTimeout to storeLockOpenTimeout
// until the Convey block ends.
func withStoreLockOpenTimeout() {
	original := managerDBOpenTimeout

	Reset(func() { managerDBOpenTimeout = original })

	managerDBOpenTimeout = storeLockOpenTimeout
}

// storeLockSeeded creates a closed store of the given kind in a new temporary
// directory, holding one live job, with a backup of it at the backup path. It
// returns the directory and the config that opens the store.
func storeLockSeeded(ctx context.Context, t *testing.T, kind storeKind) (string, storeConfig) {
	t.Helper()

	dir := t.TempDir()
	cfg := storeConfig{
		dbFile:     filepath.Join(dir, "queue.db"),
		dbBkFile:   filepath.Join(dir, testStoreBackupFile),
		deployment: internal.Development,
		kind:       kind,
	}

	s := openStoreIn(ctx, kind, dir)

	_, err := s.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo store lock", storeLockRepGroup)}, false, nil)
	So(err, ShouldBeNil)

	backup, err := os.Create(cfg.dbBkFile)
	So(err, ShouldBeNil)
	So(s.backup(backup), ShouldBeNil)
	So(backup.Close(), ShouldBeNil)
	So(s.close(ctx), ShouldBeNil)

	return dir, cfg
}

// plantRestoreLeftovers puts what a restore leaves beside dbFile there: a
// ".restoring" directory with a file in it, and a ".corrupt" file. Planted
// while another manager holds the store, they stand for its restore in
// progress, so a manager that loses the lock must leave them alone.
func plantRestoreLeftovers(dbFile string) {
	restoring := dbFile + ".restoring"

	So(os.Mkdir(restoring, dbUnreadableDirMode), ShouldBeNil)
	So(os.WriteFile(filepath.Join(restoring, "VERSION"), []byte("wr-store 1\n"), dbFilePermission), ShouldBeNil)
	So(os.WriteFile(dbFile+".corrupt", []byte("corrupt"), dbFilePermission), ShouldBeNil)
}

// storeDirIdentity returns, for every file and directory under dir, its type,
// inode, size, modification time and, for a regular file, the sha256 of its
// content, so a test can prove an open changed nothing there: no file
// rewritten, removed, added, or unlinked and replaced.
func storeDirIdentity(t *testing.T, dir string) map[string]string {
	t.Helper()

	identity := make(map[string]string)

	root, err := os.OpenRoot(dir)
	So(err, ShouldBeNil)

	defer func() { So(root.Close(), ShouldBeNil) }()

	fsys := root.FS()

	err = fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("no stat for %s", path) //nolint:err113
		}

		id := fmt.Sprintf("%s ino=%d size=%d mod=%d", info.Mode().Type(), stat.Ino, info.Size(),
			info.ModTime().UnixNano())

		if info.Mode().IsRegular() {
			content, errr := fs.ReadFile(fsys, path)
			if errr != nil {
				return errr
			}

			sum := sha256.Sum256(content)
			id += " sha256=" + hex.EncodeToString(sum[:])
		}

		identity[path] = id

		return nil
	})
	So(err, ShouldBeNil)
	So(identity, ShouldNotBeEmpty)

	return identity
}

// soStoreStillWorks adds a job through s and asserts that it is stored live,
// which a store that lost its files to another manager's open could not do.
func soStoreStillWorks(ctx context.Context, s store) {
	job := testDBJob("echo store lock holder", storeLockRepGroup)

	_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	inspector := inspectorOf(s)
	So(inspector.flush(), ShouldBeNil)

	stored, live, err := inspector.liveJob(job.Key())
	So(err, ShouldBeNil)
	So(live, ShouldBeTrue)
	So(stored.Cmd, ShouldEqual, job.Cmd)
}

func TestStoreCorruptRestoredFromBackup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store replaced by random bytes, and a backup of it", storeKindName(kind)),
			t, func() {
				withStoreLockOpenTimeout()

				_, cfg := storeLockSeeded(ctx, t, kind)

				So(os.RemoveAll(cfg.dbFile), ShouldBeNil)

				garbage := make([]byte, storeLockCorruptBytes)

				_, err := rand.NewChaCha8([32]byte{storeLockCorruptSeed}).Read(garbage)
				So(err, ShouldBeNil)

				So(os.WriteFile(cfg.dbFile, garbage, dbFilePermission), ShouldBeNil)

				Convey("opening it restores the backup, saying the db file was corrupt", func() {
					result := openStoreBounded(ctx, cfg)

					defer closeOpenResult(ctx, result)

					So(result.err, ShouldBeNil)
					So(result.s, ShouldNotBeNil)
					So(result.msg, ShouldStartWith, "recreated corrupt (?) db file")

					jobs, _, err := result.s.recoverIncompleteJobs()
					So(err, ShouldBeNil)
					So(jobs, ShouldHaveLength, 1)
					So(jobs[0].RepGroup, ShouldEqual, storeLockRepGroup)
				})
			})
	})
}

// closeOpenResult closes the store an openStoreBounded result holds, if any.
func closeOpenResult(ctx context.Context, result storeOpenResult) {
	if result.s != nil {
		_ = result.s.close(ctx)
	}
}
