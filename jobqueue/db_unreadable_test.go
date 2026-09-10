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

// This file covers a database file the manager cannot open: initDB must never
// unlink it and copy an older backup over it, because a database that cannot be
// read has not been shown to be damaged, and everything recorded since that
// backup is destroyed by the attempt to "fix" it.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"syscall"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// dbUnreadableMode is what a careful administrator sets on the database
	// before copying or inspecting it: still readable, but nothing can change
	// under them. bolt opens O_RDWR, so this is the mode that denies it.
	dbUnreadableMode fs.FileMode = 0o400

	// dbUnreadableDirMode is the mode of the directory a test puts where the
	// database file should be, to make bolt's open fail with EISDIR.
	dbUnreadableDirMode fs.FileMode = 0o700

	// dbUnreadableRepGroup names the job stored after the backup was taken, so a
	// test can tell "the database" from "the older backup" by what it holds.
	dbUnreadableRepGroup = "db-unreadable"
)

// TestDBUnreadableDirectoryWhereTheDatabaseShouldBeSurvives proves the refusal
// covers more than a permission error, using the one class that is
// deterministic and needs no privileges: an open of a directory. A directory
// where the database file should be says nothing about any database's contents,
// yet the restore path used to remove it and write the backup in its place.
func TestDBUnreadableDirectoryWhereTheDatabaseShouldBeSurvives(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("initDB refuses a directory where the database file should be, and leaves it there", t, func() {
		dbFile, dbBkFile := dgbSeededDB(ctx, t)
		So(os.Remove(dbFile), ShouldBeNil)
		So(os.Mkdir(dbFile, dbUnreadableDirMode), ShouldBeNil)

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)
		So(returned, ShouldBeTrue)

		openedDB := result.db != nil

		dgbClose(ctx, result)

		So(openedDB, ShouldBeFalse)
		So(errors.Is(result.err, ErrDBUnreadable), ShouldBeTrue)
		So(errors.Is(result.err, syscall.EISDIR), ShouldBeTrue)
		So(result.err.Error(), ShouldNotContainSubstring, "corrupt")

		info, err := os.Stat(dbFile)
		So(err, ShouldBeNil)
		So(info.IsDir(), ShouldBeTrue)
	})
}

// TestDBUnreadableDBIsNotReplacedFromBackup proves that making the manager's
// database read-only does not lose the jobs recorded since the last backup. The
// database holds two jobs and its backup only the first, so a restore is
// visible as the second job disappearing - along with the file's inode, since
// the restore unlinks the original.
func TestDBUnreadableDBIsNotReplacedFromBackup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbUnreadableSkipIfRoot(t)

	ctx := context.Background()

	Convey("Given a database with a job the backup does not have, made read-only", t, func() {
		dbFile, dbBkFile := dbUnreadableSeededDB(ctx, t)
		sizeBefore, modBefore, inoBefore := dgbFileIdentity(t, dbFile)

		dbUnreadableChmod(dbFile)

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)
		So(returned, ShouldBeTrue)

		// whatever initDB opened has to be let go before the assertions below
		// can read the file themselves.
		openedDB := result.db != nil

		dgbClose(ctx, result)

		Convey("initDB refuses to open it, saying it is unreadable and not that it is corrupt", func() {
			So(result.msg, ShouldNotContainSubstring, "corrupt")
			So(openedDB, ShouldBeFalse)
			So(errors.Is(result.err, ErrDBUnreadable), ShouldBeTrue)
			So(errors.Is(result.err, fs.ErrPermission), ShouldBeTrue)
			So(result.err.Error(), ShouldContainSubstring, dbFile)
			So(result.err.Error(), ShouldContainSubstring, "permission denied")
			So(result.err.Error(), ShouldContainSubstring,
				"left untouched, so resolve the reported problem and start wr again")
			So(result.err.Error(), ShouldNotContainSubstring, "corrupt")
		})

		Convey("initDB does not unlink or rewrite the file", func() {
			sizeAfter, modAfter, inoAfter := dgbFileIdentity(t, dbFile)
			So(sizeAfter, ShouldEqual, sizeBefore)
			So(modAfter.Equal(modBefore), ShouldBeTrue)
			So(inoAfter, ShouldEqual, inoBefore)
		})

		Convey("every job the database held is still there afterwards", func() {
			So(os.Chmod(dbFile, dbFilePermission), ShouldBeNil)
			So(dbUnreadableRepGroups(ctx, t, dbFile, dbBkFile), ShouldResemble,
				[]string{dbUnreadableRepGroup, dgbRepGroup})
		})
	})
}

// TestDBUnreadableDBWithoutBackupStillReportsUnreadable proves the refusal does
// not depend on a backup existing, which is what places it before the restore
// block rather than inside it: with no db_bk the unfixed code fell through and
// returned bolt's bare EACCES, which no caller can tell from a damaged file.
func TestDBUnreadableDBWithoutBackupStillReportsUnreadable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbUnreadableSkipIfRoot(t)

	ctx := context.Background()

	Convey("initDB reports an unreadable database as unreadable with no backup to fall back on", t, func() {
		dbFile, dbBkFile := dgbSeededDB(ctx, t)
		So(os.Remove(dbBkFile), ShouldBeNil)

		dbUnreadableChmod(dbFile)

		result, returned := dgbInitDB(ctx, dbFile, dbBkFile)
		So(returned, ShouldBeTrue)

		openedDB := result.db != nil

		dgbClose(ctx, result)

		So(openedDB, ShouldBeFalse)
		So(errors.Is(result.err, ErrDBUnreadable), ShouldBeTrue)
		So(result.err.Error(), ShouldNotContainSubstring, "corrupt")
	})
}

// dbUnreadableSkipIfRoot skips the test when the process can open any file
// whatever its mode, since then there is no way to produce the failure being
// covered.
func dbUnreadableSkipIfRoot(t *testing.T) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root bypasses file modes, so a database the manager cannot open cannot be simulated")
	}
}

// dbUnreadableSeededDB creates a database holding one live job with a backup of
// it, then adds a second job that only the database has. That second job stands
// for everything recorded since the last backup: what a restore destroys.
func dbUnreadableSeededDB(ctx context.Context, t *testing.T) (string, string) {
	t.Helper()

	dbFile, dbBkFile := dgbSeededDB(ctx, t)

	opened, _, err := initDB(ctx, dbFile, dbBkFile, internal.Development, false, false)
	So(err, ShouldBeNil)

	jobsToQueue, _, _, err := opened.storeNewJobs(ctx, []*Job{testDBJob("echo new", dbUnreadableRepGroup)}, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, 1)
	So(opened.close(ctx), ShouldBeNil)

	return dbFile, dbBkFile
}

// dbUnreadableChmod makes path unopenable for writing, and checks that it
// really is, so the test cannot silently pass by never producing the error it
// is about.
func dbUnreadableChmod(path string) {
	So(os.Chmod(path, dbUnreadableMode), ShouldBeNil)

	f, err := os.OpenFile(path, os.O_RDWR, dbUnreadableMode)
	if err == nil {
		So(f.Close(), ShouldBeNil)
	}

	So(errors.Is(err, fs.ErrPermission), ShouldBeTrue)
}

// dbUnreadableRepGroups returns the sorted RepGroups of every live job the
// database at dbFile still holds, so a test can say exactly which jobs survived.
func dbUnreadableRepGroups(ctx context.Context, t *testing.T, dbFile, dbBkFile string) []string {
	t.Helper()

	opened, _, err := initDB(ctx, dbFile, dbBkFile, internal.Development, false, false)
	So(err, ShouldBeNil)

	defer func() { So(opened.close(ctx), ShouldBeNil) }()

	jobs, err := opened.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	groups := make([]string, len(jobs))
	for i, job := range jobs {
		groups[i] = job.RepGroup
	}

	slices.Sort(groups)

	return groups
}
