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
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/minio/minio-go/v7"
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

// errStoreNotLog is returned by openLogStoreLocked if it opens another kind of
// store.
var errStoreNotLog = errors.New("opened store is not a log store")

const (
	// storeRestoreRepGroup is the RepGroup of the jobs the restore tests
	// store.
	storeRestoreRepGroup = "store-restore"

	// storeRestoreJobs is how many jobs seedRestoreJobs adds, and
	// storeRestoreArchived how many of them it archives.
	storeRestoreJobs     = 4
	storeRestoreArchived = 2

	// storeRestoreMarker is a file planted in a corrupt store, so a test can
	// tell none of its files remains.
	storeRestoreMarker = "corrupt-marker"

	// storeRestoreGarbageSize is the size of the random bytes a test puts at
	// dbFile, and storeRestoreGarbageSeed seeds them.
	storeRestoreGarbageSize = 4096
	storeRestoreGarbageSeed = 14

	// storeRestoreProdKey and storeRestoreDevKey are the keys a production
	// and a development manager keep storeWipeS3Backup's backup under.
	storeRestoreProdKey = storeWipeS3ProdKey
	storeRestoreDevKey  = storeWipeS3Key
)

// errRestoreTestCrash is what a restore test's hook ends a restore with, as a
// crash at that point would.
var errRestoreTestCrash = errors.New("restore test crash")

// restoreCrashPoints are the points of a restore (spec E2 test 9) a test
// crashes it at, the call of the restore hook there that crashes, and whether
// the crash leaves the original db file set aside as ".corrupt".
var restoreCrashPoints = []struct { //nolint:gochecknoglobals // a test table
	name     string
	point    restorePoint
	nth      int
	setAside bool
}{
	{"mid-copy", restoreAtCopied, 2, false},
	{"after recovering .restoring", restoreAtRecovered, 1, false},
	{"after the rename to .corrupt", restoreAtSetAside, 1, true},
	{"after the rename to the db file", restoreAtReplaced, 1, true},
}

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

// openLogStoreLocked opens the log store cfg names, under its lock, through
// openStoreKind, so unwrapped.
func openLogStoreLocked(ctx context.Context, cfg storeConfig) (*logStore, error) {
	s, _, err := openStoreKind(ctx, cfg)
	if err != nil {
		return nil, err
	}

	ls, ok := s.(*logStore)
	if !ok {
		return nil, errors.Join(fmt.Errorf("%w: %T", errStoreNotLog, s), s.close(ctx))
	}

	return ls, nil
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

// storeSnapshot is a store's live and complete jobs of storeRestoreRepGroup,
// each as its key and Cmd, sorted.
type storeSnapshot struct {
	live, complete []string
}

// snapshotOf returns s's storeSnapshot.
func snapshotOf(s store) storeSnapshot {
	live, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	complete, err := s.retrieveCompleteJobsByRepGroup(storeRestoreRepGroup)
	So(err, ShouldBeNil)

	described := func(jobs []*Job) []string {
		var out []string

		for _, job := range jobs {
			if job.RepGroup == storeRestoreRepGroup {
				out = append(out, job.Key()+" "+job.Cmd)
			}
		}

		slices.Sort(out)

		return out
	}

	return storeSnapshot{live: described(live), complete: described(complete)}
}

// boltRestoreBackup creates a closed bbolt database in a new temporary
// directory, holding seedRestoreJobs' jobs, and returns its path and snapshot.
func boltRestoreBackup(ctx context.Context, t *testing.T) (string, storeSnapshot) {
	t.Helper()

	dir := t.TempDir()
	s := openStoreIn(ctx, storeKindBolt, dir)

	seedRestoreJobs(ctx, s, "bolt backup")

	snapshot := snapshotOf(s)
	So(s.close(ctx), ShouldBeNil)

	return filepath.Join(dir, "queue.db"), snapshot
}

func TestStoreOpenRestore(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a backup directory and a self-contained backup of it", t, func() {
		f := newRestoreFixture(ctx, t)

		Convey("with no db file and the backup directory as its backup, an open restores it", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			f.cfg.dbBkFile = f.backupDir
			before := storeDirIdentity(t, f.backupDir)

			result := openRestoring(ctx, f.cfg)
			soRestored(result, f.backup)
			So(result.msg, ShouldEqual, "recreated missing db file "+f.cfg.dbFile+" from backup file "+f.backupDir)
			So(storeDirIdentity(t, f.backupDir), ShouldResemble, before)
			soNoRestoreLeftovers(f.cfg.dbFile)
		})

		Convey("with no db file and the self-contained backup as its backup, an open restores it", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			f.cfg.dbBkFile = f.backupTar
			before := sha256Of(f.backupTar)

			result := openRestoring(ctx, f.cfg)
			soRestored(result, f.backup)
			So(result.msg, ShouldStartWith, "recreated missing db file")
			So(sha256Of(f.backupTar), ShouldEqual, before)
		})

		Convey("with the self-contained backup as its db file, and a different backup, an open restores the "+
			"db file into a store directory there", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			So(os.Rename(f.backupTar, f.cfg.dbFile), ShouldBeNil)

			other, _ := boltRestoreBackup(ctx, t)
			f.cfg.dbBkFile = other

			result := openRestoring(ctx, f.cfg)
			soRestored(result, f.backup)
			So(result.msg, ShouldEqual, "restored db directory "+f.cfg.dbFile+" from self-contained backup file")

			info, err := os.Stat(f.cfg.dbFile)
			So(err, ShouldBeNil)
			So(info.IsDir(), ShouldBeTrue)
			soNoRestoreLeftovers(f.cfg.dbFile)

			Convey("and the next open recovers that store without restoring", func() {
				closeOpenResult(ctx, result)

				again := openRestoring(ctx, f.cfg)
				soRestored(again, f.backup)
				So(again.msg, ShouldBeBlank)
			})
		})

		for _, point := range restoreCrashPoints {
			Convey("with the self-contained backup as its db file, no other backup, and a crash "+point.name+
				" restoring it, the next open leaves a store equal to the backup and no restore leftovers", func() {
				So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
				tarSum := sha256Of(f.backupTar)
				So(os.Rename(f.backupTar, f.cfg.dbFile), ShouldBeNil)
				f.cfg.dbBkFile = filepath.Join(f.dir, "absent")

				crashRestoreAt(point.point, point.nth)

				crashed := openRestoring(ctx, f.cfg)
				So(crashed.err, ShouldWrap, errRestoreTestCrash)

				if point.setAside {
					So(sha256Of(f.cfg.dbFile+storeCorruptSuffix), ShouldEqual, tarSum)
				}

				logRestoreHook.Store(nil)

				result := openRestoring(ctx, f.cfg)
				soRestored(result, f.backup)
				soNoRestoreLeftovers(f.cfg.dbFile)
			})
		}

		Convey("with the self-contained backup set aside as .corrupt by an interrupted restore, and a db file "+
			"created since by a bbolt-kind open, an open fails naming both, removing neither", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			tarSum := sha256Of(f.backupTar)
			corrupt := f.cfg.dbFile + storeCorruptSuffix
			So(os.Rename(f.backupTar, corrupt), ShouldBeNil)
			f.cfg.dbBkFile = filepath.Join(f.dir, "absent")

			boltCfg := f.cfg
			boltCfg.kind = storeKindBolt
			created := openRestoring(ctx, boltCfg)
			So(created.err, ShouldBeNil)
			closeOpenResult(ctx, created)

			dbSum := sha256Of(f.cfg.dbFile)
			restoring := f.cfg.dbFile + storeRestoringSuffix
			So(os.Mkdir(restoring, logStoreDirPerm), ShouldBeNil)

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, errCorruptBesideDBFile)
			So(result.err.Error(), ShouldContainSubstring, f.cfg.dbFile+" ")
			So(result.err.Error(), ShouldContainSubstring, corrupt)
			So(sha256Of(corrupt), ShouldEqual, tarSum)
			So(sha256Of(f.cfg.dbFile), ShouldEqual, dbSum)
			So(restoring, shouldExist)
		})

		for name, makeDBFile := range map[string]func(dbFile string) error{
			"a named pipe": func(dbFile string) error { return syscall.Mkfifo(dbFile, dbFilePermission) },
			"a symlink to a directory": func(dbFile string) error {
				target := filepath.Join(f.dir, "elsewhere")
				if err := os.Mkdir(target, logStoreDirPerm); err != nil {
					return err
				}

				return os.Symlink(target, dbFile)
			},
		} {
			Convey("with the self-contained backup set aside as .corrupt by an interrupted restore, and "+name+
				" as its db file since, an open fails naming both, removing neither", func() {
				So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
				tarSum := sha256Of(f.backupTar)
				corrupt := f.cfg.dbFile + storeCorruptSuffix
				So(os.Rename(f.backupTar, corrupt), ShouldBeNil)
				f.cfg.dbBkFile = filepath.Join(f.dir, "absent")
				So(makeDBFile(f.cfg.dbFile), ShouldBeNil)

				before, err := os.Lstat(f.cfg.dbFile)
				So(err, ShouldBeNil)

				result := openRestoring(ctx, f.cfg)
				So(result.err, ShouldWrap, errCorruptBesideDBFile)
				So(sha256Of(corrupt), ShouldEqual, tarSum)

				after, err := os.Lstat(f.cfg.dbFile)
				So(err, ShouldBeNil)
				So(after.Mode(), ShouldEqual, before.Mode())
			})
		}

		Convey("with a damaged self-contained backup as its db file, an open fails, leaving it as it was", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)

			data, err := os.ReadFile(f.backupTar)
			So(err, ShouldBeNil)
			So(os.WriteFile(f.cfg.dbFile, data[:len(data)/2], dbFilePermission), ShouldBeNil)

			before := sha256Of(f.cfg.dbFile)

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldNotBeNil)
			So(sha256Of(f.cfg.dbFile), ShouldEqual, before)
			soNoRestoreLeftovers(f.cfg.dbFile)
		})

		Convey("with its store corrupt", func() {
			f.corrupt()

			corruptIdentity := storeDirIdentity(t, f.cfg.dbFile)

			Convey("and the backup directory as its backup, an open restores it, keeping no file of the "+
				"corrupt store", func() {
				f.cfg.dbBkFile = f.backupDir

				result := openRestoring(ctx, f.cfg)
				soRestored(result, f.backup)
				So(result.msg, ShouldStartWith, "recreated corrupt (?) db file "+f.cfg.dbFile+" from backup file "+
					f.backupDir+" (error with original db file was: ")
				So(result.msg, ShouldContainSubstring, errStoreCorrupt.Error())
				soNoCorruptFileRemains(f.dir)
				soNoRestoreLeftovers(f.cfg.dbFile)
			})

			Convey("and a backup directory whose checkpoint trailer is damaged, an open fails, leaving the "+
				"corrupt store as it was", func() {
				damageCheckpointTrailers(f.backupDir)
				f.cfg.dbBkFile = f.backupDir

				result := openRestoring(ctx, f.cfg)
				So(result.s, ShouldBeNil)
				So(result.err, ShouldWrap, errStoreCorrupt)
				So(result.msg, ShouldStartWith, "tried to recreate corrupt (?) db file "+f.cfg.dbFile)
				So(storeDirIdentity(t, f.cfg.dbFile), ShouldResemble, corruptIdentity)
				soNoRestoreLeftovers(f.cfg.dbFile)
			})

			Convey("and an empty backup directory, an open fails rather than restoring an empty store, leaving "+
				"it as it was", func() {
				f.cfg.dbBkFile = filepath.Join(f.dir, "empty")
				So(os.Mkdir(f.cfg.dbBkFile, logStoreDirPerm), ShouldBeNil)

				result := openRestoring(ctx, f.cfg)
				So(result.err, ShouldWrap, errNotABackup)
				So(result.err, ShouldWrap, errStoreCorrupt)
				So(storeDirIdentity(t, f.cfg.dbFile), ShouldResemble, corruptIdentity)
				soNoRestoreLeftovers(f.cfg.dbFile)
			})

			Convey("and no backup, an open fails, leaving it as it was", func() {
				f.cfg.dbBkFile = filepath.Join(f.dir, "absent")

				result := openRestoring(ctx, f.cfg)
				So(result.err, ShouldWrap, errStoreCorrupt)
				So(storeDirIdentity(t, f.cfg.dbFile), ShouldResemble, corruptIdentity)
			})

			for _, point := range restoreCrashPoints {
				Convey("and a crash "+point.name+", the next open leaves a store equal to the backup and no "+
					"restore leftovers", func() {
					f.cfg.dbBkFile = f.backupDir

					crashRestoreAt(point.point, point.nth)

					crashed := openRestoring(ctx, f.cfg)
					So(crashed.err, ShouldWrap, errRestoreTestCrash)

					if point.setAside {
						So(storeDirIdentity(t, f.cfg.dbFile+storeCorruptSuffix), ShouldResemble, corruptIdentity)
					}

					logRestoreHook.Store(nil)

					result := openRestoring(ctx, f.cfg)
					soRestored(result, f.backup)
					soNoRestoreLeftovers(f.cfg.dbFile)
					soNoCorruptFileRemains(f.dir)
				})
			}

			Convey("and a crash mid-copy leaves .restoring, which the next open removes before restoring", func() {
				f.cfg.dbBkFile = f.backupDir

				crashRestoreAt(restoreAtCopied, 1)

				crashed := openRestoring(ctx, f.cfg)
				So(crashed.err, ShouldWrap, errRestoreTestCrash)
				So(f.cfg.dbFile+storeRestoringSuffix, shouldExist)
				So(storeDirIdentity(t, f.cfg.dbFile), ShouldResemble, corruptIdentity)
			})

			Convey("and an old bbolt backup, an open converts the backup into the restored store, leaving the "+
				"backup as it was", func() {
				backup, want := boltRestoreBackup(ctx, t)
				f.cfg.dbBkFile = backup
				before := sha256Of(backup)

				result := openRestoring(ctx, f.cfg)
				soRestored(result, want)
				So(result.msg, ShouldStartWith, "recreated corrupt (?) db file")
				So(sha256Of(backup), ShouldEqual, before)
				soNoCorruptFileRemains(f.dir)
			})

			Convey("and an old bbolt backup larger than the free space, an open fails with errNotEnoughSpace, "+
				"leaving the corrupt store as it was", func() {
				backup, _ := boltRestoreBackup(ctx, t)
				f.cfg.dbBkFile = backup

				info, err := os.Stat(backup)
				So(err, ShouldBeNil)
				reportFreeSpace(uint64(info.Size()) - 1) //nolint:gosec // a file size

				result := openRestoring(ctx, f.cfg)
				So(result.err, ShouldWrap, errNotEnoughSpace)
				So(storeDirIdentity(t, f.cfg.dbFile), ShouldResemble, corruptIdentity)
				So(f.cfg.dbFile+storeRestoringSuffix, shouldNotExist)
			})
		})

		Convey("with 4KiB of random bytes as its db file", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			writeGarbage(f.cfg.dbFile)

			garbage := sha256Of(f.cfg.dbFile)

			Convey("and the backup directory as its backup, an open restores it, without taking the db file "+
				"for bbolt", func() {
				f.cfg.dbBkFile = f.backupDir

				result := openRestoring(ctx, f.cfg)
				soRestored(result, f.backup)
				So(result.msg, ShouldStartWith, "recreated corrupt (?) db file")
				So(result.msg, ShouldContainSubstring, "is not a store directory, bbolt file or self-contained backup")
				So(f.cfg.dbFile+storeBoltPreInfix+"dev", shouldNotExist)
			})

			Convey("and no backup, an open fails, leaving the db file as it was", func() {
				result := openRestoring(ctx, f.cfg)
				So(result.err, ShouldWrap, errStoreCorrupt)
				So(result.err.Error(), ShouldContainSubstring, "is not a store directory, bbolt file or "+
					"self-contained backup")
				So(sha256Of(f.cfg.dbFile), ShouldEqual, garbage)
			})
		})

		Convey("with no db file and an old bbolt backup, an open converts the backup into the store", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)

			backup, want := boltRestoreBackup(ctx, t)
			f.cfg.dbBkFile = backup
			before := sha256Of(backup)

			result := openRestoring(ctx, f.cfg)
			soRestored(result, want)
			So(result.msg, ShouldStartWith, "recreated missing db file")
			So(sha256Of(backup), ShouldEqual, before)
		})

		Convey("with a named pipe as its db file and the backup directory as its backup, an open fails, "+
			"leaving the pipe as it was", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			So(syscall.Mkfifo(f.cfg.dbFile, dbFilePermission), ShouldBeNil)
			f.cfg.dbBkFile = f.backupDir

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, errNotStoreOrFile)

			info, err := os.Lstat(f.cfg.dbFile)
			So(err, ShouldBeNil)
			So(info.Mode().Type(), ShouldEqual, fs.ModeNamedPipe)
			soNoRestoreLeftovers(f.cfg.dbFile)
		})

		Convey("with no db file and a named pipe as its backup, an open fails with errNotABackup, creating no "+
			"store", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			f.cfg.dbBkFile = filepath.Join(f.dir, "pipe")
			So(syscall.Mkfifo(f.cfg.dbBkFile, dbFilePermission), ShouldBeNil)

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, errNotABackup)
			So(f.cfg.dbFile, shouldNotExist)
		})

		for name, hdrs := range map[string][]*tar.Header{
			"a member named in a directory": {{Name: "dir/" + storeVersionFile, Typeflag: tar.TypeReg, Mode: 0o600}},
			"a symlink member": {{
				Name: storeVersionFile, Typeflag: tar.TypeSymlink, Linkname: f.backupDir + "/" + storeVersionFile,
			}},
			"two members of one name": {
				{Name: storeVersionFile, Typeflag: tar.TypeReg, Mode: 0o600},
				{Name: storeVersionFile, Typeflag: tar.TypeReg, Mode: 0o600},
			},
		} {
			Convey("with no db file and a tar holding "+name+" as its backup, an open fails with "+
				"errNotSelfContainedBackup, creating no store", func() {
				So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
				f.cfg.dbBkFile = filepath.Join(f.dir, "bad.tar")
				writeTarOf(f.cfg.dbBkFile, hdrs...)

				result := openRestoring(ctx, f.cfg)
				So(result.err, ShouldWrap, errNotSelfContainedBackup)
				So(f.cfg.dbFile, shouldNotExist)
				soNoRestoreLeftovers(f.cfg.dbFile)
			})
		}

		Convey("with no db file and no backup, an open creates an empty store, saying so", func() {
			So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)
			f.cfg.dbBkFile = filepath.Join(f.dir, "absent")

			result := openRestoring(ctx, f.cfg)
			soRestored(result, storeSnapshot{})
			So(result.msg, ShouldEqual, "created new empty db file "+f.cfg.dbFile)
		})
	})
}

// newRestoreFixture makes a restoreFixture in a new temporary directory.
func newRestoreFixture(ctx context.Context, t *testing.T) restoreFixture {
	t.Helper()

	dir := t.TempDir()
	f := restoreFixture{
		cfg: storeConfig{
			dbFile: filepath.Join(dir, "queue.db"), deployment: internal.Development, kind: storeKindLog,
		},
		dir:       dir,
		backupDir: filepath.Join(dir, "backup.dir"),
		backupTar: filepath.Join(dir, "backup.tar"),
	}

	s, err := openLogStoreLocked(ctx, f.cfg)
	So(err, ShouldBeNil)

	seedRestoreJobs(ctx, s, "backed up")
	So(s.checkpoint(), ShouldBeNil)

	f.backup = snapshotOf(s)
	So(f.backup.live, ShouldHaveLength, storeRestoreJobs-storeRestoreArchived)
	So(f.backup.complete, ShouldHaveLength, storeRestoreArchived)

	tarFile, err := os.Create(f.backupTar)
	So(err, ShouldBeNil)
	So(s.backup(tarFile), ShouldBeNil)
	So(tarFile.Close(), ShouldBeNil)
	So(s.close(ctx), ShouldBeNil)

	So(os.CopyFS(f.backupDir, os.DirFS(f.cfg.dbFile)), ShouldBeNil)

	s, err = openLogStoreLocked(ctx, f.cfg)
	So(err, ShouldBeNil)

	seedRestoreJobs(ctx, s, "after backup")
	So(snapshotOf(s), ShouldNotResemble, f.backup)
	So(s.close(ctx), ShouldBeNil)

	return f
}

// seedRestoreJobs adds storeRestoreJobs jobs whose Cmds start with prefix to
// s, archiving storeRestoreArchived of them.
func seedRestoreJobs(ctx context.Context, s store, prefix string) {
	jobs := make([]*Job, storeRestoreJobs)
	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo %s %d", prefix, i), storeRestoreRepGroup)
	}

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)
	So(archiveJobsInStore(s, jobs[:storeRestoreArchived]), ShouldBeNil)
}

// openRestoring opens the store cfg names, bounded, closing any store it gets
// when the Convey block ends.
func openRestoring(ctx context.Context, cfg storeConfig) storeOpenResult {
	result := openStoreBounded(ctx, cfg)

	Reset(func() { closeOpenResult(ctx, result) })

	return result
}

// soNoRestoreLeftovers asserts that neither ".restoring" nor ".corrupt" is
// beside dbFile.
func soNoRestoreLeftovers(dbFile string) {
	So(dbFile+storeRestoringSuffix, shouldNotExist)
	So(dbFile+storeCorruptSuffix, shouldNotExist)
}

// crashRestoreAt makes the nth restore hook call at point fail with
// errRestoreTestCrash until the Convey block ends.
func crashRestoreAt(point restorePoint, nth int) {
	calls := 0

	setRestoreHook(func(at restorePoint) error {
		if at != point {
			return nil
		}

		calls++
		if calls == nth {
			return errRestoreTestCrash
		}

		return nil
	})
}

// setRestoreHook makes fn logRestoreHook until the Convey block ends.
func setRestoreHook(fn func(at restorePoint) error) {
	Reset(func() { logRestoreHook.Store(nil) })

	logRestoreHook.Store(&fn)
}

// soNoCorruptFileRemains asserts that no file planted in a corrupt store as
// storeRestoreMarker remains anywhere under dir.
func soNoCorruptFileRemains(dir string) {
	var found []string

	So(filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Name() == storeRestoreMarker {
			found = append(found, path)
		}

		return err
	}), ShouldBeNil)
	So(found, ShouldBeEmpty)
}

// damageCheckpointTrailers flips the last byte of every checkpoint in dir.
func damageCheckpointTrailers(dir string) {
	ckpts, err := filepath.Glob(filepath.Join(dir, "*"+checkpointFileExt))
	So(err, ShouldBeNil)
	So(ckpts, ShouldNotBeEmpty)

	for _, path := range ckpts {
		data, errr := os.ReadFile(path)
		So(errr, ShouldBeNil)

		data[len(data)-1] ^= 0xff
		So(os.WriteFile(path, data, dbFilePermission), ShouldBeNil)
	}
}

// reportFreeSpace makes freeSpace report free bytes until the Convey block
// ends.
func reportFreeSpace(free uint64) {
	hook := func(string) (uint64, error) { return free, nil } //nolint:unparam // the hook's type

	Reset(func() { storeFreeSpaceHook.Store(nil) })

	storeFreeSpaceHook.Store(&hook)
}

// writeGarbage writes storeRestoreGarbageSize seeded random bytes to path.
func writeGarbage(path string) {
	garbage := make([]byte, storeRestoreGarbageSize)

	_, err := rand.NewChaCha8([32]byte{storeRestoreGarbageSeed}).Read(garbage)
	So(err, ShouldBeNil)
	So(os.WriteFile(path, garbage, dbFilePermission), ShouldBeNil)
}

// writeTarOf writes a tar to path holding the members hdrs, in order, the
// content of each regular file being "wr-store 1\n".
func writeTarOf(path string, hdrs ...*tar.Header) {
	content := []byte(storeVersionPrefix + "1\n")

	out, err := os.Create(path)
	So(err, ShouldBeNil)

	tw := tar.NewWriter(out)

	for _, hdr := range hdrs {
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(content))
		}

		So(tw.WriteHeader(hdr), ShouldBeNil)

		if hdr.Typeflag == tar.TypeReg {
			_, err = tw.Write(content)
			So(err, ShouldBeNil)
		}
	}

	So(tw.Close(), ShouldBeNil)
	So(out.Close(), ShouldBeNil)
}

func TestStoreOpenRestoreS3(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a production log store with no db file, and its backup directory's files in a fake S3 "+
		"bucket under the backup's key", t, func() {
		f := newRestoreFixture(ctx, t)
		So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)

		f.cfg.deployment = internal.Production
		f.cfg.dbBkFile = storeWipeS3Backup

		fake := withFakeS3Objects()
		keys := putStoreBackupObjects(fake, f.backupDir, storeRestoreProdKey)
		fake.put(storeRestoreDevKey+"/VERSION", storeRestoreProdKey+"x/VERSION")

		Convey("an open downloads every object under the key and opens the store", func() {
			result := openRestoring(ctx, f.cfg)
			soRestored(result, f.backup)
			So(result.msg, ShouldEqual, "recreated missing db file "+f.cfg.dbFile+" from s3 backup file "+
				storeRestoreProdKey)
			So(fake.downloaded(), ShouldResemble, keys)
			soNoRestoreLeftovers(f.cfg.dbFile)
		})

		Convey("with an old bbolt backup also as the object at the key itself, an open restores the store "+
			"backup under the key", func() {
			bolt, _ := boltRestoreBackup(ctx, t)
			putFileObject(fake, bolt, storeRestoreProdKey)

			result := openRestoring(ctx, f.cfg)
			soRestored(result, f.backup)
			So(fake.downloaded(), ShouldResemble, keys)
		})

		Convey("a development manager with forced backups downloads only the development key's objects", func() {
			devKeys := putStoreBackupObjects(fake, f.backupDir, storeRestoreDevKey)
			f.cfg.deployment = internal.Development
			f.cfg.forceBackups = true

			result := openRestoring(ctx, f.cfg)
			soRestored(result, f.backup)
			So(result.msg, ShouldEndWith, "from s3 backup file "+storeRestoreDevKey)
			So(fake.downloaded(), ShouldResemble, devKeys)
		})

		Convey("a development manager without forced backups does not reach S3, and creates an empty store",
			func() {
				f.cfg.deployment = internal.Development

				result := openRestoring(ctx, f.cfg)
				soRestored(result, storeSnapshot{})
				So(fake.openCount(), ShouldEqual, 0)
			})

		Convey("with an object under the key whose name leads out of it, an open fails with errNotABackup, "+
			"writing nothing outside .restoring", func() {
			fake.putData(storeRestoreProdKey+"/../escaped", []byte("escaped"))

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, errNotABackup)
			So(filepath.Join(f.dir, "escaped"), shouldNotExist)
			So(f.cfg.dbFile, shouldNotExist)
			soNoRestoreLeftovers(f.cfg.dbFile)
		})

		for name, failure := range map[string]error{
			"fails part way":        errStoreWipeDelete,
			"finds the object gone": minio.ErrorResponse{Code: "NoSuchKey", StatusCode: http.StatusNotFound},
		} {
			Convey("when a download "+name+", the open fails, leaving nothing it downloaded", func() {
				fake.failDownloading(failure, keys[len(keys)-1])

				result := openRestoring(ctx, f.cfg)
				So(result.err, ShouldNotBeNil)
				So(f.cfg.dbFile, shouldNotExist)
				So(dirEntryNames(f.dir), ShouldResemble,
					[]string{filepath.Base(f.backupDir), filepath.Base(f.backupTar), "queue.db" + storeLockSuffix})
			})
		}
	})

	Convey("Given a production log store with no db file, and an old bbolt backup as one object in a fake S3 "+
		"bucket at the backup's key", t, func() {
		f := newRestoreFixture(ctx, t)
		So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)

		f.cfg.deployment = internal.Production
		f.cfg.dbBkFile = storeWipeS3Backup

		backup, want := boltRestoreBackup(ctx, t)

		fake := withFakeS3Objects()
		putFileObject(fake, backup, storeRestoreProdKey)

		Convey("an open converts it into the store, keeping no download", func() {
			result := openRestoring(ctx, f.cfg)
			soRestored(result, want)
			So(result.msg, ShouldStartWith, "recreated missing db file")
			So(filepath.Join(f.cfg.dbFile, backupDownloadName), shouldNotExist)
			So(fake.downloaded(), ShouldResemble, []string{storeRestoreProdKey})
		})
	})
}

// putStoreBackupObjects puts every file of the store backup directory dir into
// fake as an object under key + "/", and returns their keys, sorted.
func putStoreBackupObjects(fake *fakeS3Objects, dir, key string) []string {
	var keys []string

	fsys := os.DirFS(dir)

	So(fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		data, errr := fs.ReadFile(fsys, name)
		if errr != nil {
			return errr
		}

		keys = append(keys, key+"/"+name)
		fake.putData(keys[len(keys)-1], data)

		return nil
	}), ShouldBeNil)

	slices.Sort(keys)

	return keys
}

// putFileObject puts the file at path into fake as the object at key.
func putFileObject(fake *fakeS3Objects, path, key string) {
	data, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	fake.putData(key, data)
}

func TestStoreOpenRestoreBoltDBFile(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store's backup directory and a bbolt backup", t, func() {
		f := newRestoreFixture(ctx, t)
		So(os.RemoveAll(f.cfg.dbFile), ShouldBeNil)

		boltBackup, boltWant := boltRestoreBackup(ctx, t)

		soRestoredWithoutConverting := func(want storeSnapshot) {
			result := openRestoring(ctx, f.cfg)
			soRestored(result, want)
			So(result.msg, ShouldStartWith, "recreated corrupt (?) db file")

			for _, path := range []string{f.cfg.dbFile + storeConvertingSuffix, f.cfg.dbFile + storeBoltPreInfix + "dev"} {
				So(path, shouldNotExist)
			}

			closeOpenResult(ctx, result)

			again := openRestoring(ctx, f.cfg)
			soRestored(again, want)
			So(again.msg, ShouldBeBlank)
			So(again.s.upgradedOnOpen(), ShouldBeFalse)
		}

		Convey("with a bbolt db file whose meta page checksums are both damaged", func() {
			writeProgressFixture(f.cfg.dbFile)
			damageBoltMetaPages(f.cfg.dbFile)

			Convey("an open restores the bbolt backup", func() {
				f.cfg.dbBkFile = boltBackup

				soRestoredWithoutConverting(boltWant)
			})

			Convey("an open restores the backup directory", func() {
				f.cfg.dbBkFile = f.backupDir

				soRestoredWithoutConverting(f.backup)
			})
		})

		Convey("with a bbolt db file whose meta pages are valid but a branch page is zeroed, an open restores "+
			"the backup directory", func() {
			writeProgressFixture(f.cfg.dbFile)
			zeroBoltBranchPage(f.cfg.dbFile)

			f.cfg.dbBkFile = f.backupDir

			soRestoredWithoutConverting(f.backup)
		})

		Convey("with a bbolt db file whose first meta page is zeroed, which bbolt still opens, an open converts "+
			"it rather than restoring over it", func() {
			copyTestFile(boltBackup, f.cfg.dbFile)

			db, err := os.OpenFile(f.cfg.dbFile, os.O_RDWR, 0)
			So(err, ShouldBeNil)
			_, err = db.WriteAt(make([]byte, os.Getpagesize()), 0)
			So(err, ShouldBeNil)
			So(db.Close(), ShouldBeNil)

			f.cfg.dbBkFile = f.backupDir

			result := openRestoring(ctx, f.cfg)
			soRestored(result, boltWant)
			So(result.msg, ShouldBeBlank)
			So(f.cfg.dbFile+storeBoltPreInfix+"dev", shouldExist)
		})

		Convey("with a bbolt db file that is not corrupt but cannot be converted, an open fails, restoring "+
			"nothing", func() {
			copyTestFile(boltBackup, f.cfg.dbFile)
			putBoltValue(f.cfg.dbFile, bucketJobsLive, convertNeverKey, []byte("not a job"))

			f.cfg.dbBkFile = f.backupDir
			before := sha256Of(f.cfg.dbFile)

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, errBoltUnconvertible)
			So(sha256Of(f.cfg.dbFile), ShouldEqual, before)
			soNoRestoreLeftovers(f.cfg.dbFile)
		})

		Convey("with an interrupted conversion whose SOURCE names no pre name, an open fails, restoring "+
			"nothing", func() {
			copyTestFile(boltBackup, f.cfg.dbFile)

			converting := f.cfg.dbFile + storeConvertingSuffix
			So(os.Mkdir(converting, logStoreDirPerm), ShouldBeNil)
			So(os.WriteFile(filepath.Join(converting, storeConvertSourceFile), []byte("../elsewhere"),
				dbFilePermission), ShouldBeNil)

			f.cfg.dbBkFile = f.backupDir
			before := sha256Of(f.cfg.dbFile)

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, errBadConvertSource)
			So(sha256Of(f.cfg.dbFile), ShouldEqual, before)
			soNoRestoreLeftovers(f.cfg.dbFile)
		})

		Convey("with a conversion that fails after setting the bbolt file aside, an open fails, restoring "+
			"nothing though no db file is left, and the next open finishes the conversion", func() {
			copyTestFile(boltBackup, f.cfg.dbFile)
			f.cfg.dbBkFile = f.backupDir

			crashConvertAt(convertAtRenamed, 1)

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, errConvertTestCrash)
			So(f.cfg.dbFile, shouldNotExist)
			soNoRestoreLeftovers(f.cfg.dbFile)

			logConvertHook.Store(nil)

			again := openRestoring(ctx, f.cfg)
			soRestored(again, boltWant)
			So(again.msg, ShouldBeBlank)
		})

		conveyUnlessRoot("with a corrupt bbolt db file whose abandoned conversion cannot be removed, an open "+
			"fails without claiming corruption, restoring nothing and keeping SOURCE", func() {
			writeProgressFixture(f.cfg.dbFile)
			zeroBoltBranchPage(f.cfg.dbFile)

			damaged := sha256Of(f.cfg.dbFile)
			converting := f.cfg.dbFile + storeConvertingSuffix

			Reset(func() {
				if _, err := os.Stat(converting); err == nil {
					So(os.Chmod(converting, logStoreDirPerm), ShouldBeNil)
				}
			})

			setConvertHook(func(at convertPoint) error {
				if at == convertAtRestored {
					return os.Chmod(converting, storeOpenUnwritableDirMode)
				}

				return nil
			})

			f.cfg.dbBkFile = f.backupDir

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, fs.ErrPermission)
			So(errors.Is(result.err, errStoreCorrupt), ShouldBeFalse)
			So(sha256Of(f.cfg.dbFile), ShouldEqual, damaged)
			So(filepath.Join(converting, storeConvertSourceFile), shouldExist)
			soNoRestoreLeftovers(f.cfg.dbFile)

			Convey("and once it can be removed, the next open restores the backup", func() {
				logConvertHook.Store(nil)
				So(os.Chmod(converting, logStoreDirPerm), ShouldBeNil)

				soRestoredWithoutConverting(f.backup)
			})
		})

		Convey("with a db file that cannot be read, an open fails with ErrDBUnreadable, restoring nothing", func() {
			So(os.Symlink("/proc/self/mem", f.cfg.dbFile), ShouldBeNil)

			f.cfg.dbBkFile = f.backupDir

			result := openRestoring(ctx, f.cfg)
			So(result.err, ShouldWrap, ErrDBUnreadable)

			target, err := os.Readlink(f.cfg.dbFile)
			So(err, ShouldBeNil)
			So(target, ShouldEqual, "/proc/self/mem")
			soNoRestoreLeftovers(f.cfg.dbFile)
		})
	})
}

// soRestored asserts that result opened a store holding want.
func soRestored(result storeOpenResult, want storeSnapshot) {
	So(result.err, ShouldBeNil)
	So(result.s, ShouldNotBeNil)
	So(snapshotOf(result.s), ShouldResemble, want)
}

// restoreFixture is a closed log store at cfg.dbFile, in dir, that has moved on
// since its backups were made: a copy of its directory at backupDir and its
// self-contained backup at backupTar, both holding backup. cfg names no
// backup; a test picks one.
type restoreFixture struct {
	cfg                  storeConfig
	dir                  string
	backupDir, backupTar string
	backup               storeSnapshot
}

// corrupt makes the fixture's store one recovery refuses as corrupt, by
// removing its VERSION, and plants storeRestoreMarker in it.
func (f restoreFixture) corrupt() {
	So(os.Remove(filepath.Join(f.cfg.dbFile, storeVersionFile)), ShouldBeNil)
	So(os.WriteFile(filepath.Join(f.cfg.dbFile, storeRestoreMarker), []byte("corrupt"), dbFilePermission),
		ShouldBeNil)
}
