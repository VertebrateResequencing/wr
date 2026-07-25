//go:build reliability_repro

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

// This file provides a portable, from-scratch big-DB generator used by the
// reliable4 backup-stall reproducer (developers/wrdev.sh backup-stall-check).
//
// The production stall was traced to the periodic DB backup: db.backupToBackupFile
// copies the ENTIRE bolt DB file every minimumTimeBetweenBackups (30s). On a
// multi-GB DB each copy does GBs of I/O and holds the bolt read-tx mmaplock for
// its whole duration, so archive/touch DB writes stall past the TTR -> "receive
// time out" -> jobs falsely lost -> confirmed-dead -> rerun churn. It reproduces
// ONLY when backups run (production deployment) on a LARGE DB - which is why the
// dev-manager reproducers all drained cleanly.
//
// To reproduce on any machine without needing a real production DB, this helper
// inflates a fresh, valid bolt DB to a target size by filling a throwaway
// "reliable4padding" bucket with 1MiB values. wr's initDB opens it fine and
// creates its own buckets; the padding just makes the file big so each backup's
// CopyFile is slow. Run it (via the wrdev command) with:
//
//	WR_INFLATE_DB=/path/to/db WR_INFLATE_GB=6 \
//	  go test -tags reliability_repro ./jobqueue/ -run TestReliable4InflateDB
//
// Set WRDEV_ROOT (the wrdev harness) to a disk with room for the DB + its backup
// (2x the size); the DB path is whatever you pass in WR_INFLATE_DB.

package jobqueue

import (
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// reliable4PaddingBucket is a throwaway bucket used only to inflate the DB file
// size; wr never reads it.
var reliable4PaddingBucket = []byte("reliable4padding") //nolint:gochecknoglobals

func TestReliable4InflateDB(t *testing.T) {
	if runnermode || servermode {
		return
	}

	path := os.Getenv("WR_INFLATE_DB")
	gb, _ := strconv.Atoi(os.Getenv("WR_INFLATE_GB"))

	if path == "" || gb <= 0 {
		t.Skip("set WR_INFLATE_DB (path) and WR_INFLATE_GB (>0) to inflate a big bolt DB")
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("could not open/create bolt DB at %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	const (
		mib          = 1 << 20
		valsPerTx    = 64 // 64MiB per transaction, so no single huge tx
		reportEveryN = 512
	)

	val := make([]byte, mib)
	targetMiB := gb * 1024

	var written int
	for written < targetMiB {
		if errU := db.Update(func(tx *bolt.Tx) error {
			b, errc := tx.CreateBucketIfNotExists(reliable4PaddingBucket)
			if errc != nil {
				return errc
			}

			for i := 0; i < valsPerTx && written < targetMiB; i++ {
				var key [8]byte
				binary.BigEndian.PutUint64(key[:], uint64(written)) //nolint:gosec // loop-bounded, fits

				if errp := b.Put(key[:], val); errp != nil {
					return errp
				}

				written++
			}

			return nil
		}); errU != nil {
			t.Fatalf("failed writing padding at %dMiB: %v", written, errU)
		}

		if written%reportEveryN == 0 {
			t.Logf("INFLATE-DB: %d/%d MiB written to %s", written, targetMiB, path)
		}
	}

	if fi, errs := os.Stat(path); errs == nil {
		t.Logf("INFLATE-DB: done, %s is now %d bytes (~%dGiB)", path, fi.Size(), fi.Size()>>30)
	}
}
