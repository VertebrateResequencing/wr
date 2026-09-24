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

	// currentDBSchemaVersion is the version a new database is created at.
	currentDBSchemaVersion = dbSchemaVersionNoCompleteStd

	dbSchemaVersionBytes = 8
)

//nolint:gochecknoglobals // bucket names are shared BoltDB keys.
var (
	bucketMeta           = []byte("meta")
	metaKeySchemaVersion = []byte("schemaVersion")
)

var errBadDBSchemaVersion = errors.New("malformed database schema version")

// putDBSchemaVersion records version as tx's database's schema version.
func putDBSchemaVersion(tx *bolt.Tx, version uint64) error {
	b, err := tx.CreateBucketIfNotExists(bucketMeta)
	if err != nil {
		return fmt.Errorf("create bucket %s: %w", bucketMeta, err)
	}

	return b.Put(metaKeySchemaVersion, binary.BigEndian.AppendUint64(nil, version))
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
