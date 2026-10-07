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

// This file holds the database's schema version: what an existing database is
// known to hold, so a one-time clean-up can tell whether it has been done.

import (
	"encoding/binary"
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// dbSchemaVersion values. A database records the version its contents are known
// to satisfy. A database with no recorded version is version 0: it may hold
// anything older wr versions wrote.
//
// To add a version, add a constant here that says what the database is then
// known to hold, make it currentDBSchemaVersion, and teach `wr manager compact`
// (compactBolt) to bring an older database up to it.
const (
	// dbSchemaVersionNoCompleteStd means no bucketJobsComplete record holds
	// StdOutC or StdErrC. wr v0.37.0 to v0.37.2 kept a successful job's output
	// there; `wr manager compact` strips it from an older database.
	dbSchemaVersionNoCompleteStd uint64 = 1

	// dbSchemaVersionRunState means the database may hold bucketJobRunState
	// records, which a manager that does not overlay them would ignore.
	dbSchemaVersionRunState uint64 = 2

	// currentDBSchemaVersion is the version a new database is created at, and
	// the newest version this wr supports.
	currentDBSchemaVersion = dbSchemaVersionRunState

	dbSchemaVersionBytes = 8
)

//nolint:gochecknoglobals // bucket names are shared BoltDB keys.
var (
	bucketMeta           = []byte("meta")
	metaKeySchemaVersion = []byte("schemaVersion")

	// bucketJobRunState holds a job's run-state record, keyed as in
	// bucketJobsLive.
	bucketJobRunState = []byte("jobRunState")
)

var (
	errBadDBSchemaVersion = errors.New("malformed database schema version")
	errDBSchemaTooNew     = errors.New("database schema version is newer than this wr supports")
	errDBNeedsCompact     = errors.New("database must be compacted before this wr can use it")
)

// putDBSchemaVersion records version as tx's database's schema version.
func putDBSchemaVersion(tx *bolt.Tx, version uint64) error {
	b, err := tx.CreateBucketIfNotExists(bucketMeta)
	if err != nil {
		return fmt.Errorf("create bucket %s: %w", bucketMeta, err)
	}

	return b.Put(metaKeySchemaVersion, binary.BigEndian.AppendUint64(nil, version))
}

// checkOpenedDBSchemaVersion returns the schema version of bdb, the manager's
// just-opened database at dbFile, or an error if this wr must not use it: its
// stamp is malformed, it is newer than this wr supports, or it existed before
// this open (existed), is unversioned and has a bucketJobsLive bucket, so it
// must be compacted first. It writes nothing.
func checkOpenedDBSchemaVersion(bdb *bolt.DB, dbFile string, existed bool) (uint64, error) {
	version, err := dbFileSchemaVersion(bdb)
	if err != nil {
		return 0, err
	}

	if err = checkDBSchemaVersion(dbFile, version); err != nil {
		return 0, err
	}

	if !existed || version > 0 {
		return version, nil
	}

	var hasLive bool

	if err = bdb.View(func(tx *bolt.Tx) error {
		hasLive = tx.Bucket(bucketJobsLive) != nil

		return nil
	}); err != nil {
		return 0, err
	}

	if hasLive {
		return 0, fmt.Errorf("%w: %s was created by wr 0.37.2 or earlier and has never been compacted; "+
			"with the manager stopped, run `wr manager compact`, then start the manager again",
			errDBNeedsCompact, dbFile)
	}

	return version, nil
}

// readOnlyDBFileSchemaVersion opens path read-only (so the open writes
// nothing, even to a file whose freelist was never synced) with Timeout
// offlineDBOpenTimeout, so it fails rather than blocks on a file a running
// manager holds, returns its schema version and closes it.
func readOnlyDBFileSchemaVersion(path string) (version uint64, err error) {
	bdb, err := bolt.Open(path, dbFilePermission,
		&bolt.Options{ReadOnly: true, FreelistType: bolt.FreelistMapType, Timeout: offlineDBOpenTimeout})
	if err != nil {
		return 0, err
	}

	defer func() {
		if errc := bdb.Close(); errc != nil && err == nil {
			err = errc
		}
	}()

	return dbFileSchemaVersion(bdb)
}

// dbFileSchemaVersion returns the schema version recorded in bdb, or 0 if none
// is recorded.
func dbFileSchemaVersion(bdb *bolt.DB) (uint64, error) {
	var version uint64

	err := bdb.View(func(tx *bolt.Tx) error {
		var errv error

		version, errv = readDBSchemaVersion(tx)

		return errv
	})

	return version, err
}

// readDBSchemaVersion returns the schema version recorded in tx's database, or 0
// if none is recorded.
func readDBSchemaVersion(tx *bolt.Tx) (uint64, error) {
	b := tx.Bucket(bucketMeta)
	if b == nil {
		return 0, nil
	}

	v := b.Get(metaKeySchemaVersion)
	if v == nil {
		return 0, nil
	}

	if len(v) != dbSchemaVersionBytes {
		return 0, fmt.Errorf("%w: %d bytes", errBadDBSchemaVersion, len(v))
	}

	return binary.BigEndian.Uint64(v), nil
}

// checkDBSchemaVersion returns errDBSchemaTooNew, naming dbFile, if version is
// newer than currentDBSchemaVersion; otherwise nil.
func checkDBSchemaVersion(dbFile string, version uint64) error {
	if version <= currentDBSchemaVersion {
		return nil
	}

	return fmt.Errorf("%w: %s has schema version %d, this wr supports up to %d; use a newer wr",
		errDBSchemaTooNew, dbFile, version, currentDBSchemaVersion)
}
