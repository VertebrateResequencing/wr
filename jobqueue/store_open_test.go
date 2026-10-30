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

// Tests of the store lock and VERSION (spec C7): a manager that cannot take
// the lock beside dbFile, or that finds a store from a newer wr, changes
// nothing.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// storeOpenLockedTimeout is the managerDBOpenTimeout C7 test 1 runs with.
	storeOpenLockedTimeout = 100 * time.Millisecond

	// storeOpenLockedBound is how soon C7 test 1's refused open must return.
	storeOpenLockedBound = time.Second

	// storeOpenRepGroup is the RepGroup of the jobs these tests store.
	storeOpenRepGroup = "store-open"

	// storeOpenGarbageVersionSize is the size of the garbage VERSION a test
	// writes, far more than an error should quote.
	storeOpenGarbageVersionSize = 4096

	// storeOpenUnwritableDirMode leaves a directory readable but lets nothing
	// be created in it.
	storeOpenUnwritableDirMode os.FileMode = 0o500
)

// logOpenResult is what a bounded openLogStoreLocked call came back with.
type logOpenResult struct {
	s   *logStore
	err error
}

// openLogStoreBounded calls openLogStoreLocked on a goroutine and returns its
// result or, if it has not returned within managerDBOpenTimeout +
// dgbDeadlineSlack, an empty result having reported the hang as a named
// failure. Any store it opens is closed when the Convey block ends.
func openLogStoreBounded(ctx context.Context, cfg storeConfig) logOpenResult {
	results := make(chan logOpenResult, 1)

	go func() {
		s, err := openLogStoreLocked(ctx, cfg)
		results <- logOpenResult{s: s, err: err}
	}()

	select {
	case result := <-results:
		if result.s != nil {
			Reset(func() { So(result.s.close(ctx), ShouldBeNil) })
		}

		return result
	case <-time.After(managerDBOpenTimeout + dgbDeadlineSlack):
		go func() {
			if result := <-results; result.s != nil {
				_ = result.s.close(ctx)
			}
		}()

		So("openLogStoreLocked did not return within the database open timeout", ShouldBeBlank)

		return logOpenResult{}
	}
}

func TestStoreOpenLock(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store held open, with its backup and restore leftovers beside it", t, func() {
		original := managerDBOpenTimeout

		Reset(func() { managerDBOpenTimeout = original })

		managerDBOpenTimeout = storeOpenLockedTimeout

		cfg, dir := logStoreOpenConfig(t)
		closedLogStoreWithBackup(ctx, cfg)

		holder, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		Reset(func() { So(holder.close(ctx), ShouldBeNil) })

		plantRestoreLeftovers(cfg.dbFile)

		Convey("its lock is the file beside the store directory", func() {
			info, errs := os.Stat(cfg.dbFile + ".lock")
			So(errs, ShouldBeNil)
			So(info.Mode().IsRegular(), ShouldBeTrue)

			_, errs = os.Stat(filepath.Join(cfg.dbFile, ".lock"))
			So(errors.Is(errs, os.ErrNotExist), ShouldBeTrue)
		})

		Convey("a second open fails with ErrDBLocked within 1s, touching no file, and the holder still works", func() {
			before := storeDirIdentity(t, dir)

			started := time.Now()
			result := openLogStoreBounded(ctx, cfg)
			elapsed := time.Since(started)

			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, ErrDBLocked)
			So(result.err.Error(), ShouldContainSubstring, cfg.dbFile)
			So(elapsed, ShouldBeLessThan, storeOpenLockedBound)
			So(storeDirIdentity(t, dir), ShouldResemble, before)

			job := testDBJob("echo store open holder", storeOpenRepGroup)

			_, err = holder.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)
			soLogStoreHolds(holder, job)
		})

		Convey("once the holder closes, a second open takes the lock and opens the store", func() {
			So(holder.close(ctx), ShouldBeNil)

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)
			So(result.s, ShouldNotBeNil)
		})
	})

	Convey("Given only the lock held, with no store directory yet", t, func() {
		original := managerDBOpenTimeout

		Reset(func() { managerDBOpenTimeout = original })

		managerDBOpenTimeout = storeOpenLockedTimeout

		cfg, dir := logStoreOpenConfig(t)

		lock, err := lockStore(ctx, cfg.dbFile)
		So(err, ShouldBeNil)

		Reset(func() { So(lock.release(), ShouldBeNil) })

		Convey("an open fails with ErrDBLocked before creating anything", func() {
			before := storeDirIdentity(t, dir)

			result := openLogStoreBounded(ctx, cfg)

			So(result.err, ShouldWrap, ErrDBLocked)
			So(storeDirIdentity(t, dir), ShouldResemble, before)

			_, errs := os.Stat(cfg.dbFile)
			So(errors.Is(errs, os.ErrNotExist), ShouldBeTrue)
		})
	})

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store held open through openStore", storeKindName(kind)), t, func() {
			withStoreLockOpenTimeout()

			dir, cfg := storeLockSeeded(ctx, t, kind)
			holder := openStoreIn(ctx, kind, dir)

			Reset(func() { So(holder.close(ctx), ShouldBeNil) })

			Convey("a second open that would wipe a development store fails with ErrDBLocked, wiping nothing", func() {
				before := storeDirIdentity(t, dir)

				cfg.wipeDevDB = true
				result := openStoreBounded(ctx, cfg)
				closeOpenResult(ctx, result)

				So(result.err, ShouldWrap, ErrDBLocked)
				So(storeDirIdentity(t, dir), ShouldResemble, before)

				soStoreStillWorks(ctx, holder)
			})
		})
	})
}

// logStoreOpenConfig returns the config of a log store at "queue.db" in a new
// temporary directory, with its backup beside it, and that directory.
func logStoreOpenConfig(t *testing.T) (storeConfig, string) {
	t.Helper()

	dir := t.TempDir()

	return storeConfig{
		dbFile:     filepath.Join(dir, "queue.db"),
		dbBkFile:   filepath.Join(dir, testStoreBackupFile),
		deployment: internal.Development,
		kind:       storeKindLog,
	}, dir
}

// closedLogStoreWithBackup creates a closed log store holding one live job,
// with a copy of its directory at the backup path, as a backup directory.
func closedLogStoreWithBackup(ctx context.Context, cfg storeConfig) {
	s, _ := seededLogStore(ctx, cfg)
	So(s.close(ctx), ShouldBeNil)

	So(os.CopyFS(cfg.dbBkFile, os.DirFS(cfg.dbFile)), ShouldBeNil)
}

// seededLogStore opens the log store cfg names through openLogStoreLocked,
// stores one live job in it, and returns it open with that job; the caller
// closes it.
func seededLogStore(ctx context.Context, cfg storeConfig) (*logStore, *Job) {
	s, err := openLogStoreLocked(ctx, cfg)
	So(err, ShouldBeNil)

	job := testDBJob("echo store open", storeOpenRepGroup)

	_, err = s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	return s, job
}

// soLogStoreHolds asserts that job is stored live in s.
func soLogStoreHolds(s *logStore, job *Job) {
	stored, live, err := s.liveJob(job.Key())
	So(err, ShouldBeNil)
	So(live, ShouldBeTrue)
	So(stored.Cmd, ShouldEqual, job.Cmd)
}

func TestStoreOpenVersion(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a new log store", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, job := seededLogStore(ctx, cfg)

		Convey("its VERSION is wr-store 1, and it reopens with its job", func() {
			So(s.close(ctx), ShouldBeNil)

			version, err := os.ReadFile(filepath.Join(cfg.dbFile, "VERSION"))
			So(err, ShouldBeNil)
			So(string(version), ShouldEqual, "wr-store 1\n")

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)
			soLogStoreHolds(result.s, job)
		})

		Convey("with VERSION wr-store 2 and a torn wal tail recovery would truncate", func() {
			So(s.close(ctx), ShouldBeNil)
			So(os.WriteFile(filepath.Join(cfg.dbFile, "VERSION"), []byte("wr-store 2\n"), dbFilePermission),
				ShouldBeNil)

			files, err := walFileNumbers(cfg.dbFile)
			So(err, ShouldBeNil)
			So(files, ShouldNotBeEmpty)

			wal, err := os.OpenFile(walFilePath(cfg.dbFile, files[len(files)-1]), os.O_WRONLY|os.O_APPEND, 0)
			So(err, ShouldBeNil)

			_, err = wal.Write([]byte{0xff, 0xff, 0xff})
			So(err, ShouldBeNil)
			So(wal.Close(), ShouldBeNil)

			Convey("an open is refused as too new, saying to use a newer wr, changing no file", func() {
				before := storeDirIdentity(t, cfg.dbFile)

				result := openLogStoreBounded(ctx, cfg)

				So(result.s, ShouldBeNil)
				So(result.err, ShouldWrap, errStoreVersionTooNew)
				So(result.err.Error(), ShouldContainSubstring, "use a newer wr")
				So(storeDirIdentity(t, cfg.dbFile), ShouldResemble, before)
			})
		})

		Convey("with VERSION not one wr writes, an open is refused as corrupt", func() {
			So(s.close(ctx), ShouldBeNil)

			for _, content := range []string{
				"wr-store 0\n", "wr-store x\n", "bolt 1\n", "", "1\n", "wr-store 1",
			} {
				So(os.WriteFile(filepath.Join(cfg.dbFile, "VERSION"), []byte(content), dbFilePermission),
					ShouldBeNil)

				result := openLogStoreBounded(ctx, cfg)
				So(result.s, ShouldBeNil)
				So(result.err, ShouldWrap, errStoreCorrupt)
			}
		})

		Convey("with a large garbage VERSION, the refusal quotes only the start of it", func() {
			So(s.close(ctx), ShouldBeNil)

			garbage := strings.Repeat("x", storeOpenGarbageVersionSize)
			So(os.WriteFile(filepath.Join(cfg.dbFile, "VERSION"), []byte(garbage), dbFilePermission),
				ShouldBeNil)

			result := openLogStoreBounded(ctx, cfg)
			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, errStoreCorrupt)
			So(result.err.Error(), ShouldContainSubstring, garbage[:storeVersionQuoted])
			So(result.err.Error(), ShouldNotContainSubstring, garbage[:storeVersionQuoted+1])
		})

		Convey("with no VERSION beside its wal files, an open is refused as corrupt", func() {
			So(s.close(ctx), ShouldBeNil)
			So(os.Remove(filepath.Join(cfg.dbFile, "VERSION")), ShouldBeNil)

			result := openLogStoreBounded(ctx, cfg)
			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, errStoreCorrupt)
		})
	})

	Convey("Given an empty store directory, an open creates a store in it", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		So(os.Mkdir(cfg.dbFile, logStoreDirPerm), ShouldBeNil)

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		version, err := os.ReadFile(filepath.Join(cfg.dbFile, "VERSION"))
		So(err, ShouldBeNil)
		So(string(version), ShouldEqual, "wr-store 1\n")
	})

	Convey("Given a store directory holding only a VERSION.tmp, an open creates a store in it", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		So(os.Mkdir(cfg.dbFile, logStoreDirPerm), ShouldBeNil)
		So(os.WriteFile(filepath.Join(cfg.dbFile, "VERSION.tmp"), []byte("wr-st"), dbFilePermission),
			ShouldBeNil)

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)
		So(result.s, ShouldNotBeNil)

		version, err := os.ReadFile(filepath.Join(cfg.dbFile, "VERSION"))
		So(err, ShouldBeNil)
		So(string(version), ShouldEqual, "wr-store 1\n")
	})
}

func TestStoreOpenUnreadable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbUnreadableSkipIfRoot(t)

	ctx := context.Background()

	Convey("Given a log store directory of mode 0, with a backup beside it", t, func() {
		cfg, dir := logStoreOpenConfig(t)
		closedLogStoreWithBackup(ctx, cfg)

		backupBefore := storeDirIdentity(t, cfg.dbBkFile)

		So(os.Chmod(cfg.dbFile, 0), ShouldBeNil)

		Reset(func() { So(os.Chmod(cfg.dbFile, logStoreDirPerm), ShouldBeNil) })

		Convey("an open fails with ErrDBUnreadable, restoring nothing", func() {
			result := openLogStoreBounded(ctx, cfg)

			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, ErrDBUnreadable)
			So(result.err.Error(), ShouldNotContainSubstring, "corrupt")

			entries, err := os.ReadDir(dir)
			So(err, ShouldBeNil)

			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}

			So(names, ShouldResemble, []string{"queue.db", "queue.db.bak", "queue.db.lock"})

			info, err := os.Stat(cfg.dbFile)
			So(err, ShouldBeNil)
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(0))
			So(storeDirIdentity(t, cfg.dbBkFile), ShouldResemble, backupBefore)
		})
	})

	Convey("Given a closed log store whose VERSION file is of mode 0", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		closedLogStoreWithBackup(ctx, cfg)

		versionPath := filepath.Join(cfg.dbFile, storeVersionFile)
		So(os.Chmod(versionPath, 0), ShouldBeNil)

		Reset(func() { So(os.Chmod(versionPath, dbFilePermission), ShouldBeNil) })

		Convey("an open fails with ErrDBUnreadable, not as a store without a VERSION", func() {
			result := openLogStoreBounded(ctx, cfg)

			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, ErrDBUnreadable)
			So(errors.Is(result.err, errStoreCorrupt), ShouldBeFalse)

			info, err := os.Stat(versionPath)
			So(err, ShouldBeNil)
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(0))
		})
	})

	Convey("Given no lock file yet, beside dbFile in a directory nothing can be created in", t, func() {
		cfg, dir := logStoreOpenConfig(t)

		So(os.Chmod(dir, storeOpenUnwritableDirMode), ShouldBeNil)

		Reset(func() { So(os.Chmod(dir, logStoreDirPerm), ShouldBeNil) })

		Convey("an open fails with ErrDBUnreadable, not as corrupt", func() {
			result := openLogStoreBounded(ctx, cfg)

			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, ErrDBUnreadable)
			So(result.err.Error(), ShouldContainSubstring, cfg.dbFile)
			So(result.err.Error(), ShouldNotContainSubstring, "corrupt")

			_, err := os.Stat(cfg.dbFile + ".lock")
			So(errors.Is(err, os.ErrNotExist), ShouldBeTrue)
		})
	})

	Convey("Given a bolt database openStore cannot open", t, func() {
		withStoreLockOpenTimeout()

		// with no GC running, a lock file openStore failed to release stays
		// open, as it would for a while in a real manager, instead of being
		// closed by its finalizer and hiding the leak.
		gcPercent := debug.SetGCPercent(-1)

		Reset(func() { debug.SetGCPercent(gcPercent) })

		dbFile, dbBkFile := dgbSeededDB(ctx, t)
		cfg := storeConfig{
			dbFile:     dbFile,
			dbBkFile:   dbBkFile,
			deployment: internal.Development,
			kind:       storeKindBolt,
		}

		dbUnreadableChmod(dbFile)

		result := openStoreBounded(ctx, cfg)
		closeOpenResult(ctx, result)

		So(result.s, ShouldBeNil)
		So(result.err, ShouldWrap, ErrDBUnreadable)

		Convey("once it is readable again, a retry in the same process takes the lock and opens it", func() {
			So(os.Chmod(dbFile, dbFilePermission), ShouldBeNil)

			retry := openStoreBounded(ctx, cfg)
			closeOpenResult(ctx, retry)

			So(retry.err, ShouldBeNil)
			So(retry.s, ShouldNotBeNil)
		})
	})
}
