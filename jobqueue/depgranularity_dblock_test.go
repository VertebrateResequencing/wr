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

// This file covers spec section E7 where it is bbolt's own: a manager started
// while another one holds the database gets ErrDBLocked, not bolt's bare
// ErrTimeout. The store-neutral parts (the loser touches no file, the holder
// keeps working, an open waits for a briefly held lock, a corrupt database is
// restored from its backup) are in store_lock_test.go.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

const (
	// dgbOpenTimeout is the managerDBOpenTimeout the lock test runs with. The
	// real value is 30s; lowering it is the whole reason managerDBOpenTimeout is
	// a var.
	dgbOpenTimeout = 500 * time.Millisecond
)

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

	holder, err := openManagerBolt(ctx, dbFile)
	So(err, ShouldBeNil)

	return dbFile, dbBkFile, holder
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
