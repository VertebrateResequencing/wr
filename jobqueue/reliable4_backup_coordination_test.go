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

// This file is the fast, in-process behavioural regression test for the
// reliable4 DB-backup coordination fix (bugfix 260727-2). It asserts that the
// periodic DB backup no longer serialises the archive/exit hot path:
//
//   - Part A: the archive path (db.archiveJob) must NOT acquire the exclusive db
//     write-lock. Pre-fix it called db.backgroundBackup(ctx) on every archive,
//     which takes db.Lock, so any transient db.Lock holder (e.g. an exit-update
//     stuck acquiring the backup's wgMutex during a backup) stalled every
//     concurrent archiver. The report-storm dump caught 1828 archivers blocked
//     exactly there.
//   - Part B: a periodic backup must NOT hold db.wgMutex across db.wg.Wait().
//     Pre-fix backupToBackupFile drained in-flight async writes under wgMutex, so
//     every exit-path write (which must take wgMutex to register its background
//     batch) blocked for the backup's drain. The fix keeps that wait only on
//     close()'s final backup.
//
// Both sub-tests are deterministic and must be RED before the fix and GREEN
// after. They complement the LSF-scale developers/wrdev.sh report-storm-lsf gate.

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	coordArchivers       = 50
	coordArchiveWait     = 2 * time.Second
	coordBackupReachWait = 200 * time.Millisecond
	coordExitProceedWait = time.Second
)

// coordCompletedJob builds a fresh completed job with a key unique to n, so
// archiving it adds a new complete-bucket record (as production archives do).
func coordCompletedJob(n int) *Job {
	now := time.Now()

	return &Job{
		Cmd:      fmt.Sprintf("reliable4coord %d", n),
		Cwd:      defaultUploadDir,
		RepGroup: "reliable4coord",
		ReqGroup: "reliable4coord",
		Requirements: &scheduler.Requirements{
			RAM: 100, Time: time.Hour, Cores: 1, Disk: 1,
		},
		State:     JobStateComplete,
		Exited:    true,
		Host:      "reliable4-host",
		StartTime: now.Add(-time.Minute),
		EndTime:   now,
	}
}

// coordWaitTimeout reports whether wg completed within d.
func coordWaitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// coordOpenBackupDB opens a fresh, backups-enabled db in a temp dir (the real
// initDB path the manager uses), returning it and registering its close.
func coordOpenBackupDB(t *testing.T, ctx context.Context) *db {
	t.Helper()

	tmpdir := t.TempDir()

	database, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, true)
	So(err, ShouldBeNil)

	// NB: don't assert on `database` itself (e.g. ShouldNotBeNil) - goconvey
	// fmt-reflects the whole struct, which races the backup ticker goroutine
	// that initDB has already started. A plain nil guard avoids that.
	if database == nil {
		t.Fatal("initDB returned a nil db")
	}

	return database
}

func TestReliable4BackupCoordination(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("The archive hot path does not serialise behind the db write-lock", t, func() {
		database := coordOpenBackupDB(t, ctx)
		defer func() { So(database.close(ctx), ShouldBeNil) }()

		// Hold the db write-lock for the duration of the archive attempt. This
		// stands in for the transient db.Lock hold that occurs during a backup
		// (an exit-update stuck on the backup's wgMutex holds db.Lock). Pre-fix,
		// db.archiveJob acquires db.Lock via db.backgroundBackup, so it stalls
		// here; the fix replaces that with a lock-free atomic dirty flag.
		var (
			wg   sync.WaitGroup
			errs atomic.Int64
		)

		database.Lock()

		for i := range coordArchivers {
			wg.Add(1)

			go func(n int) {
				defer wg.Done()

				job := coordCompletedJob(n)
				if err := database.archiveJob(ctx, job.Key(), job); err != nil {
					errs.Add(1)
				}
			}(i)
		}

		completed := coordWaitTimeout(&wg, coordArchiveWait)

		database.Unlock()
		wg.Wait()

		So(errs.Load(), ShouldEqual, 0)
		So(completed, ShouldBeTrue)
	})

	Convey("A periodic backup does not hold wgMutex across wg.Wait", t, func() {
		database := coordOpenBackupDB(t, ctx)
		defer func() { So(database.close(ctx), ShouldBeNil) }()

		// Register an in-flight async-write we control, so a periodic backup's
		// pre-copy wg.Wait would block on it. Pre-fix, backupToBackupFile holds
		// wgMutex across that wait, so any exit-path write blocks; the fix drops
		// the wait (and the wgMutex hold) from the periodic path.
		database.wgMutex.Lock()
		wgk := database.wg.Add(1)
		database.wgMutex.Unlock()

		database.backgroundBackup(ctx)
		time.Sleep(coordBackupReachWait)

		var wg sync.WaitGroup

		wg.Add(1)

		go func() {
			defer wg.Done()

			database.updateJobAfterChange(ctx, coordCompletedJob(0))
		}()

		proceeded := coordWaitTimeout(&wg, coordExitProceedWait)

		database.wg.Done(wgk)
		wg.Wait()

		So(proceeded, ShouldBeTrue)
	})
}
