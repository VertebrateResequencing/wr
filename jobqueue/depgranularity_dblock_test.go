//go:build !windows

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

// This file covers spec section E7: a manager started while another one holds
// the database fails cleanly with ErrDBLocked, and never touches the winner's
// database file.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

const (
	// dgbOpenTimeout is the managerDBOpenTimeout the lock tests run with. The
	// real value is 30s; lowering it is the whole reason managerDBOpenTimeout is
	// a var, since three tests each really waiting 30s would add ~90s to
	// make test.
	dgbOpenTimeout = 500 * time.Millisecond

	// dgbDeadlineSlack is how much longer than managerDBOpenTimeout each test
	// gives initDB before declaring it hung. It is a hang detector, not a
	// latency budget: without Options.Timeout bbolt retries the flock every 50ms
	// for as long as the lock is held, so an unbounded initDB would otherwise
	// block until go test's 10-minute panic took the whole package down with a
	// stack dump instead of a named failure.
	dgbDeadlineSlack = 5 * time.Second

	dgbRepGroup = "depgranularity-dblock"
)

// dgbInitDBResult is what a bounded initDB call came back with.
type dgbInitDBResult struct {
	db  *db
	msg string
	err error
}

// dgbInitDB calls initDB on a goroutine and returns either its result or, if it
// has not returned within managerDBOpenTimeout + dgbDeadlineSlack, a nil result
// having reported the hang as a named failure. The caller closes any *db it gets
// back, with dgbClose.
//
// The bound is what makes a run without Options.Timeout report a per-test
// "initDB did not return" instead of blocking until go test's 10-minute panic
// takes the whole package down with a stack dump. On that branch a second
// goroutine closes whatever the blocked call eventually opens, so it lets the
// file go rather than holding it for the rest of the package run.
func dgbInitDB(ctx context.Context, dbFile, dbBkFile string) (dgbInitDBResult, bool) {
	results := make(chan dgbInitDBResult, 1)

	go func() {
		opened, msg, err := initDB(ctx, dbFile, dbBkFile, internal.Development, false, false)
		results <- dgbInitDBResult{db: opened, msg: msg, err: err}
	}()

	select {
	case result := <-results:
		return result, true
	case <-time.After(managerDBOpenTimeout + dgbDeadlineSlack):
		go func() { dgbClose(ctx, <-results) }()

		So("initDB did not return within the database open timeout", ShouldBeBlank)

		return dgbInitDBResult{}, false
	}
}

// TestDepGranularityDBLockedLeavesFileAlone covers E7 acceptance test 1: a
// second manager started while the first holds the database gets ErrDBLocked
// within the bounded wait, and the winner's file is untouched - same size, same
// modification time and, crucially, the same inode, because the hazard being
// avoided is the restore path unlinking the live file and coming up on a stale
// backup.
func TestDepGranularityDBLockedLeavesFileAlone(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("initDB on a database another manager holds fails without touching the file", t, func() {
		defer dgbWithShortOpenTimeout()()

		dbFile, dbBkFile, holder := dgbLockedDB(ctx, t)

		defer func() { So(holder.Close(), ShouldBeNil) }()

		sizeBefore, modBefore, inoBefore := dgbFileIdentity(t, dbFile)

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)
		So(returned, ShouldBeTrue)
		So(result.db == nil, ShouldBeTrue)
		So(errors.Is(result.err, ErrDBLocked), ShouldBeTrue)
		So(result.err.Error(), ShouldContainSubstring, dbFile)

		sizeAfter, modAfter, inoAfter := dgbFileIdentity(t, dbFile)
		So(sizeAfter, ShouldEqual, sizeBefore)
		So(modAfter.Equal(modBefore), ShouldBeTrue)
		So(inoAfter, ShouldEqual, inoBefore)
	})
}

// dgbWithShortOpenTimeout lowers managerDBOpenTimeout for the duration of the
// test, returning the func that restores it.
func dgbWithShortOpenTimeout() func() {
	original := managerDBOpenTimeout
	managerDBOpenTimeout = dgbOpenTimeout

	return func() { managerDBOpenTimeout = original }
}

// dgbLockedDB creates a database holding one live job, then reopens it with a
// raw bolt.Open the test holds for the rest of the test, standing in for the
// winning manager. It returns the db file path, the backup path, and the holder.
//
// Releasing the holder is deferred by the caller, not left to the end of the
// test body, so a goroutine still blocked in initDB is released even when the
// test fails.
func dgbLockedDB(ctx context.Context, t *testing.T) (string, string, *bolt.DB) {
	t.Helper()

	dbFile, dbBkFile := dgbSeededDB(ctx, t)

	holder, err := openManagerBolt(dbFile)
	So(err, ShouldBeNil)

	return dbFile, dbBkFile, holder
}

// dgbSeededDB creates a fresh database in t.TempDir() holding one live job, plus
// a valid backup of it, and returns both paths with nothing holding either.
func dgbSeededDB(ctx context.Context, t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	dbFile := filepath.Join(dir, "db")
	dbBkFile := dbFile + "_bk"

	seed, _, err := initDB(ctx, dbFile, dbBkFile, internal.Development, false, false)
	So(err, ShouldBeNil)

	jobsToQueue, _, _, err := seed.storeNewJobs(ctx, []*Job{testDBJob("echo dgb", dgbRepGroup)}, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, 1)
	So(seed.close(ctx), ShouldBeNil)
	So(copyFile(dbFile, dbBkFile), ShouldBeNil)

	return dbFile, dbBkFile
}

// dgbFileIdentity returns the size, modification time and inode of path, so a
// test can prove initDB left the winner's database file completely alone: not
// truncated, not rewritten, and above all not unlinked and replaced.
func dgbFileIdentity(t *testing.T, path string) (int64, time.Time, uint64) {
	t.Helper()

	info, err := os.Stat(path)
	So(err, ShouldBeNil)

	stat, ok := info.Sys().(*syscall.Stat_t)
	So(ok, ShouldBeTrue)

	return info.Size(), info.ModTime(), stat.Ino
}

// TestDepGranularityDBLockedLeavesBackupAlone covers E7 acceptance test 2: the
// loser also leaves the backup alone, and the winner keeps working. Reading and
// writing through the holder afterwards is what proves it still owns a live
// database rather than a deleted inode.
func TestDepGranularityDBLockedLeavesBackupAlone(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("initDB on a held database leaves the backup alone and the holder working", t, func() {
		defer dgbWithShortOpenTimeout()()

		dbFile, dbBkFile, holder := dgbLockedDB(ctx, t)

		defer func() { So(holder.Close(), ShouldBeNil) }()

		sizeBefore, modBefore, inoBefore := dgbFileIdentity(t, dbBkFile)

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)
		So(returned, ShouldBeTrue)
		So(errors.Is(result.err, ErrDBLocked), ShouldBeTrue)

		sizeAfter, modAfter, inoAfter := dgbFileIdentity(t, dbBkFile)
		So(sizeAfter, ShouldEqual, sizeBefore)
		So(modAfter.Equal(modBefore), ShouldBeTrue)
		So(inoAfter, ShouldEqual, inoBefore)

		So(dgbHolderStillWorks(holder), ShouldBeTrue)
	})
}

// dgbHolderStillWorks writes and reads back a key through the holder, reporting
// whether both succeeded.
func dgbHolderStillWorks(holder *bolt.DB) bool {
	key := []byte("dgb-holder-check")

	if err := holder.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobsLive).Put(key, []byte("ok"))
	}); err != nil {
		return false
	}

	var read []byte

	if err := holder.View(func(tx *bolt.Tx) error {
		read = tx.Bucket(bucketJobsLive).Get(key)

		return nil
	}); err != nil {
		return false
	}

	return string(read) == "ok"
}

// TestDepGranularityDBLockedWithoutBackup covers E7 acceptance test 3: the
// sentinel does not depend on a backup existing. With no db_bk the pre-change
// code would fall through the restore block and return bolt's bare ErrTimeout,
// which no caller can tell from a corrupt file.
func TestDepGranularityDBLockedWithoutBackup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("initDB on a held database with no backup still reports ErrDBLocked", t, func() {
		defer dgbWithShortOpenTimeout()()

		dbFile, dbBkFile, holder := dgbLockedDB(ctx, t)

		defer func() { So(holder.Close(), ShouldBeNil) }()

		So(os.Remove(dbBkFile), ShouldBeNil)

		sizeBefore, modBefore, inoBefore := dgbFileIdentity(t, dbFile)

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)
		So(returned, ShouldBeTrue)
		So(errors.Is(result.err, ErrDBLocked), ShouldBeTrue)
		So(errors.Is(result.err, berrors.ErrTimeout), ShouldBeFalse)

		sizeAfter, modAfter, inoAfter := dgbFileIdentity(t, dbFile)
		So(sizeAfter, ShouldEqual, sizeBefore)
		So(modAfter.Equal(modBefore), ShouldBeTrue)
		So(inoAfter, ShouldEqual, inoBefore)
	})
}

// TestDepGranularityCorruptDBStillRestores covers E7 acceptance test 4: bounding
// the open must not disturb the restore-from-backup path, which is what makes
// the ErrDBLocked short-circuit's placement (before that block, not after)
// load-bearing rather than cosmetic.
func TestDepGranularityCorruptDBStillRestores(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("initDB still recreates a corrupt database from its backup", t, func() {
		defer dgbWithShortOpenTimeout()()

		dbFile, dbBkFile := dgbSeededDB(ctx, t)

		So(os.WriteFile(dbFile, []byte("this is not a bolt database"), dbFilePermission), ShouldBeNil)

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)

		defer dgbClose(ctx, result)

		So(returned, ShouldBeTrue)
		So(result.err, ShouldBeNil)
		So(result.db != nil, ShouldBeTrue)
		So(result.msg, ShouldContainSubstring, "recreated corrupt (?) db file")

		jobs, err := result.db.recoverIncompleteJobs()
		So(err, ShouldBeNil)
		So(jobs, ShouldHaveLength, 1)
		So(jobs[0].RepGroup, ShouldEqual, dgbRepGroup)
	})
}

// TestDepGranularityUnlockedDBOpensImmediately covers E7 acceptance test 5: the
// bound is a timeout, not a sleep.
func TestDepGranularityUnlockedDBOpensImmediately(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("initDB on an unlocked database opens it with no measurable delay", t, func() {
		defer dgbWithShortOpenTimeout()()

		dbFile, dbBkFile := dgbSeededDB(ctx, t)

		started := time.Now()

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)
		elapsed := time.Since(started)

		defer dgbClose(ctx, result)

		So(returned, ShouldBeTrue)
		So(result.err, ShouldBeNil)
		So(elapsed, ShouldBeLessThan, dgbOpenTimeout)
	})
}

// TestDepGranularityDBLockWaitsForRelease covers E7 acceptance test 6: the bound
// is a WAIT, so a restart that overlaps a prompt shutdown still opens the
// database rather than failing fast. That is the whole reason the timeout is 30s
// in production and not a few seconds.
func TestDepGranularityDBLockWaitsForRelease(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("initDB waits for a briefly-held database rather than failing fast", t, func() {
		defer dgbWithShortOpenTimeout()()

		dbFile, dbBkFile, holder := dgbLockedDB(ctx, t)

		released := make(chan struct{})

		go func() {
			<-time.After(dgbOpenTimeout / 5)

			_ = holder.Close()

			close(released)
		}()

		defer func() { <-released }()

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)

		defer dgbClose(ctx, result)

		So(returned, ShouldBeTrue)
		So(result.err, ShouldBeNil)
		So(result.db != nil, ShouldBeTrue)
	})
}

// dgbClose closes the database a dgbInitDB result holds, if it holds one.
func dgbClose(ctx context.Context, result dgbInitDBResult) {
	if result.db != nil {
		_ = result.db.close(ctx)
	}
}
