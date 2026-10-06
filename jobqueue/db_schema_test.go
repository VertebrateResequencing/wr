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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

func TestDBSchemaVersionCheckOnOpen(t *testing.T) {
	ctx := context.Background()

	Convey("Given no database file, initDB creates it at the current version with a run-state bucket", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")

		So(openAndCloseTestDB(ctx, dbFile), ShouldBeNil)

		version, hasMeta := testDBSchemaVersion(t, dbFile)
		So(hasMeta, ShouldBeTrue)
		So(version, ShouldEqual, uint64(2))
		So(testDBHasBucket(t, dbFile, bucketJobRunState), ShouldBeTrue)
	})

	Convey("Given a database stamped 1 holding 2 live jobs, initDB opens it, recovery reads both and it becomes 2", t,
		func() {
			dbFile := filepath.Join(t.TempDir(), "queue.db")
			testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
			So(err, ShouldBeNil)

			live := []*Job{testDBJob("echo schema 1", "rg-schema"), testDBJob("echo schema 2", "rg-schema")}
			_, _, _, err = testDB.storeNewJobs(ctx, live, false)
			So(err, ShouldBeNil)
			So(testDB.close(ctx), ShouldBeNil)

			stampTestDB(t, dbFile, dbSchemaVersionNoCompleteStd)

			reDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
			So(err, ShouldBeNil)

			jobs, err := reDB.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(reDB.close(ctx), ShouldBeNil)
			So(len(jobs), ShouldEqual, len(live))

			version, _ := testDBSchemaVersion(t, dbFile)
			So(version, ShouldEqual, uint64(2))
		})

	Convey("Given a database stamped 2, initDB keeps it at 2", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")
		So(openAndCloseTestDB(ctx, dbFile), ShouldBeNil)
		stampTestDB(t, dbFile, dbSchemaVersionRunState)

		So(openAndCloseTestDB(ctx, dbFile), ShouldBeNil)

		version, _ := testDBSchemaVersion(t, dbFile)
		So(version, ShouldEqual, uint64(2))
	})

	Convey("Given a database stamped 3, initDB refuses it without changing it", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")
		So(openAndCloseTestDB(ctx, dbFile), ShouldBeNil)
		stampTestDB(t, dbFile, 3)

		before := fileSHA256(t, dbFile)

		err := openAndCloseTestDB(ctx, dbFile)
		So(errors.Is(err, errDBSchemaTooNew), ShouldBeTrue)
		So(err.Error(), ShouldContainSubstring, "schema version 3")
		So(err.Error(), ShouldContainSubstring, "supports up to 2")
		So(err.Error(), ShouldContainSubstring, dbFile)
		So(fileSHA256(t, dbFile), ShouldEqual, before)
	})

	Convey("Given an existing unversioned database with a jobslive bucket, initDB refuses it without changing it", t,
		func() {
			dbFile := filepath.Join(t.TempDir(), "queue.db")
			So(openAndCloseTestDB(ctx, dbFile), ShouldBeNil)
			unstampDB(t, dbFile)

			before := fileSHA256(t, dbFile)

			err := openAndCloseTestDB(ctx, dbFile)
			So(errors.Is(err, errDBNeedsCompact), ShouldBeTrue)
			So(err.Error(), ShouldContainSubstring, "wr manager compact")
			So(err.Error(), ShouldContainSubstring, dbFile)
			So(fileSHA256(t, dbFile), ShouldEqual, before)
		})

	Convey("Given an existing bbolt file with no buckets, initDB treats it as new and stamps it 2", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")
		bdb, err := bolt.Open(dbFile, dbFilePermission, &bolt.Options{Timeout: time.Second})
		So(err, ShouldBeNil)
		So(bdb.Close(), ShouldBeNil)

		So(openAndCloseTestDB(ctx, dbFile), ShouldBeNil)

		version, _ := testDBSchemaVersion(t, dbFile)
		So(version, ShouldEqual, uint64(2))
	})

	Convey("Given a missing database and an unversioned backup with a jobslive bucket", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, "queue.db")
		bkFile := filepath.Join(dir, "queue.db.bak")
		So(openAndCloseTestDB(ctx, bkFile), ShouldBeNil)
		unstampDB(t, bkFile)

		Convey("initDB restores it, refuses it, and a compaction makes it openable at 2", func() {
			_, _, err := initDB(ctx, dbFile, bkFile, internal.Development, false, false)
			So(errors.Is(err, errDBNeedsCompact), ShouldBeTrue)

			_, err = os.Stat(dbFile)
			So(err, ShouldBeNil)

			_, err = CompactDBFileStats(dbFile)
			So(err, ShouldBeNil)

			testDB, _, err := initDB(ctx, dbFile, bkFile, internal.Development, false, false)
			So(err, ShouldBeNil)
			So(testDB.close(ctx), ShouldBeNil)

			version, _ := testDBSchemaVersion(t, dbFile)
			So(version, ShouldEqual, uint64(2))
		})
	})

	Convey("Given a database whose stamp is 3 bytes long, initDB refuses it without changing it", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")
		So(openAndCloseTestDB(ctx, dbFile), ShouldBeNil)
		So(updateRawBolt(t, dbFile, func(tx *bolt.Tx) error {
			return tx.Bucket(bucketMeta).Put(metaKeySchemaVersion, []byte("bad"))
		}), ShouldBeNil)

		before := fileSHA256(t, dbFile)

		err := openAndCloseTestDB(ctx, dbFile)
		So(errors.Is(err, errBadDBSchemaVersion), ShouldBeTrue)
		So(fileSHA256(t, dbFile), ShouldEqual, before)
	})
}

// openAndCloseTestDB opens dbFile with initDB, as a development manager would,
// and closes it, returning initDB's error.
func openAndCloseTestDB(ctx context.Context, dbFile string) error {
	testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
	if err != nil {
		return err
	}

	return testDB.close(ctx)
}

// testDBHasBucket reports whether the closed BoltDB file at path has a
// top-level bucket called name.
func testDBHasBucket(t *testing.T, path string, name []byte) bool {
	t.Helper()

	bdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	So(err, ShouldBeNil)

	defer func() { So(bdb.Close(), ShouldBeNil) }()

	var has bool

	So(bdb.View(func(tx *bolt.Tx) error {
		has = tx.Bucket(name) != nil

		return nil
	}), ShouldBeNil)

	return has
}

// stampTestDB records version as the schema version of the closed BoltDB file
// at path.
func stampTestDB(t *testing.T, path string, version uint64) {
	t.Helper()

	So(updateRawBolt(t, path, func(tx *bolt.Tx) error {
		return putDBSchemaVersion(tx, version)
	}), ShouldBeNil)
}

func TestReadOnlyDBFileSchemaVersion(t *testing.T) {
	Convey("readOnlyDBFileSchemaVersion reads a stamp without changing a never-synced-freelist file", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")

		bdb, err := bolt.Open(dbFile, dbFilePermission,
			&bolt.Options{FreelistType: bolt.FreelistMapType, NoFreelistSync: true, Timeout: time.Second})
		So(err, ShouldBeNil)
		So(bdb.Update(func(tx *bolt.Tx) error {
			return putDBSchemaVersion(tx, 3)
		}), ShouldBeNil)
		So(bdb.Close(), ShouldBeNil)

		before := fileSHA256(t, dbFile)

		version, err := readOnlyDBFileSchemaVersion(dbFile)
		So(err, ShouldBeNil)
		So(version, ShouldEqual, uint64(3))
		So(fileSHA256(t, dbFile), ShouldEqual, before)

		So(checkDBSchemaVersion(dbFile, version), ShouldWrap, errDBSchemaTooNew)
		So(checkDBSchemaVersion(dbFile, currentDBSchemaVersion), ShouldBeNil)
	})
}

// fileSHA256 returns the hex SHA-256 of the file at path.
func fileSHA256(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}
