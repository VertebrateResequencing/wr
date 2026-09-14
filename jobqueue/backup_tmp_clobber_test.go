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

// This file pins the fix for bugfix 260910-3: `wr manager backup -p mydb.db`
// used to stage the backup at the FIXED path `mydb.db.tmp`, so a file the user
// already had there was truncated and then either renamed away over the backup
// (on success) or removed (on failure). The path is one a user types on a
// command line, in a directory full of their own files, and nothing warns them.

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// backupTmpUserContent is what the user's own file at <backup path>.tmp
	// holds, so a survivor can be told apart from a file wr overwrote with a
	// database.
	backupTmpUserContent = "a file of the user's that has nothing to do with wr\n"

	// backupTmpUserMode makes the user's file unwritable, which is how the
	// failure path is reached honestly: `os.WriteFile` on an existing 0400 file
	// fails with EACCES, and the pre-fix cleanup then removed it.
	backupTmpUserMode = 0o400

	// backupTmpWritableMode is an ordinary mode for the user's file, so the
	// pre-fix write succeeds and the file is renamed away instead.
	backupTmpWritableMode = 0o600

	// backupTmpDirMode is the mode of the directory that stands in the way of
	// the publishing rename.
	backupTmpDirMode = 0o700

	// backupTmpHostileUmask masks off the owner's write bit as well as all of
	// group and other, so os.CreateTemp on its own would produce a 0400 file.
	// Under any ordinary umask (0002, 0022) CreateTemp's 0600 arrives intact,
	// which is why only a umask like this one can show that stageBackup sets
	// the mode itself.
	backupTmpHostileUmask = 0o277

	// backupTmpStagedContent stands in for the database bytes the server would
	// send; staging only cares that there are some.
	backupTmpStagedContent = "the bytes a backup would be made of"
)

// TestBackupDBSparesUnrelatedTmpFile proves BackupDB destroys nothing the user
// already has beside the path they asked for, whether the backup succeeds or
// fails, and that the backup itself still lands complete and readable.
func TestBackupDBSparesUnrelatedTmpFile(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server, a client and a job", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		backupTmpAddJob(jq, standardReqs)

		dir := t.TempDir()
		backupPath := filepath.Join(dir, "mydb.db")
		userPath := backupPath + ".tmp"

		Convey("A backup beside a writable file of the user's leaves that file alone", func() {
			So(os.WriteFile(userPath, []byte(backupTmpUserContent), backupTmpWritableMode), ShouldBeNil)

			errb := jq.BackupDB(backupPath)

			// the survival of the user's file is asserted before the backup's
			// own outcome, because GoConvey halts a block at its first failure
			// and the destroyed file is the thing this test exists to catch.
			backupTmpAssertUserFileIntact(userPath, backupTmpWritableMode)

			So(errb, ShouldBeNil)
			assertNonEmptyFile(backupPath)
			assertBoltLiveJobs(backupPath, 1)
		})

		Convey("A backup beside a read-only file of the user's leaves that file alone", func() {
			So(os.WriteFile(userPath, []byte(backupTmpUserContent), backupTmpUserMode), ShouldBeNil)

			errb := jq.BackupDB(backupPath)

			backupTmpAssertUserFileIntact(userPath, backupTmpUserMode)

			So(errb, ShouldBeNil)
			assertNonEmptyFile(backupPath)
			assertBoltLiveJobs(backupPath, 1)
		})

		Convey("A backup lands atomically at the asked-for path, leaving no staging file behind", func() {
			So(jq.BackupDB(backupPath), ShouldBeNil)

			info, errs := os.Stat(backupPath)
			So(errs, ShouldBeNil)
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(dbFilePermission))

			So(backupTmpDirEntries(dir), ShouldResemble, []string{"mydb.db"})
		})

		Convey("A backup that cannot be published leaves no staging file behind either", func() {
			// a directory at the asked-for path cannot be renamed over, which
			// fails the backup after the staging file has been written.
			So(os.Mkdir(backupPath, backupTmpDirMode), ShouldBeNil)
			So(os.WriteFile(filepath.Join(backupPath, "occupied"),
				[]byte(backupTmpUserContent), backupTmpWritableMode), ShouldBeNil)

			So(jq.BackupDB(backupPath), ShouldNotBeNil)

			So(backupTmpDirEntries(dir), ShouldResemble, []string{"mydb.db"})
		})
	})
}

// backupTmpAddJob adds one job through jq, so the backup taken afterwards is a
// real database with a countable live job in it.
func backupTmpAddJob(jq *Client, reqs *jqs.Requirements) {
	inserts, already, err := jq.Add([]*Job{{
		Cmd:          "echo backup-tmp-clobber",
		Cwd:          defaultUploadDir,
		ReqGroup:     "backup_tmp_clobber",
		RepGroup:     "backup_tmp_clobber",
		Requirements: reqs,
	}}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)
	So(already, ShouldEqual, 0)
}

// backupTmpAssertUserFileIntact asserts the user's file still exists with the
// content and mode they gave it.
func backupTmpAssertUserFileIntact(path string, mode os.FileMode) {
	info, err := os.Stat(path)
	So(err, ShouldBeNil)

	if err != nil {
		return
	}

	So(info.Mode().Perm(), ShouldEqual, mode)

	content, err := os.ReadFile(path)
	So(err, ShouldBeNil)
	So(string(content), ShouldEqual, backupTmpUserContent)
}

// backupTmpDirEntries returns the names of everything in dir, so a test can say
// what wr left behind.
func backupTmpDirEntries(dir string) []string {
	entries, err := os.ReadDir(dir)
	So(err, ShouldBeNil)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

// TestBackupStagedModeSurvivesHostileUmask proves a staged backup gets the mode
// a database is meant to have even when the process umask would strip it. That
// staged file is the backup: BackupDB renames it over the path the user asked
// for, so its mode is the mode the user is left holding.
func TestBackupStagedModeSurvivesHostileUmask(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A backup staged under a umask that would strip the owner's write bit still has dbFilePermission", t, func() {
		// the temp dir has to exist before the umask changes, or the umask
		// would make the dir itself unwritable and nothing could be staged in
		// it.
		dir := t.TempDir()

		tmpPath := backupTmpStageUnderUmask(filepath.Join(dir, "mydb.db"), backupTmpHostileUmask)

		info, err := os.Stat(tmpPath)
		So(err, ShouldBeNil)
		So(info.Mode().Perm(), ShouldEqual, os.FileMode(dbFilePermission))
	})
}

// backupTmpStageUnderUmask stages a backup for path with the process umask set
// to umask, and returns the staged file's name.
//
// syscall.Umask is process-wide and every test in a package shares one process,
// so the umask is restored as the helper returns (deferred, so a failed
// assertion inside cannot leak it) rather than at the end of the test. No test
// in this package calls t.Parallel(), so no other test body can run inside this
// window, but background goroutines outliving earlier tests could still create
// files in it, and one call is as narrow as the window gets.
func backupTmpStageUnderUmask(path string, umask int) string {
	previous := syscall.Umask(umask)
	defer syscall.Umask(previous)

	tmpPath, err := stageBackup(path, []byte(backupTmpStagedContent))
	So(err, ShouldBeNil)

	return tmpPath
}
