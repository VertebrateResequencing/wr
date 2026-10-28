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
	"errors"
	"fmt"
	"time"

	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

// inspectFlushWait bounds how long a bbolt flush waits for the write
// transactions it waits on, as close() does.
const inspectFlushWait = dbRunningTransactionsWaitTime

var (
	errInspectFlushTimeout  = errors.New("queued writes were not durable")
	errInspectMissingBucket = errors.New("store has no bucket for that record kind")
	errInspectNoBackups     = errors.New("store has no backup path")
	errInspectUnknownKind   = errors.New("unknown record kind")
)

//nolint:gochecknoinits // registers this store kind's file opener
func init() {
	storeFileOpeners = append(storeFileOpeners, openBoltFileInspector)
}

// openBoltFileInspector is the storeFileOpeners entry for a bbolt database.
func openBoltFileInspector(path string) (storeInspector, func() error, error) {
	boltdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, nil, err
	}

	return &db{bolt: boltdb, ch: new(codec.BincHandle)}, boltdb.Close, nil
}

// inspectBuckets returns the buckets that hold records of kind.
func inspectBuckets(kind recordKind) ([][]byte, error) {
	switch kind {
	case kindLive:
		return [][]byte{bucketJobsLive}, nil
	case kindComplete:
		return [][]byte{bucketJobsComplete}, nil
	case kindStdOut:
		return [][]byte{bucketStdO}, nil
	case kindStdErr:
		return [][]byte{bucketStdE}, nil
	case kindEnv:
		return [][]byte{bucketEnvs}, nil
	case kindLimit:
		return [][]byte{bucketLGs}, nil
	case kindStat:
		return [][]byte{bucketJobRAM, bucketJobDisk, bucketJobSecs}, nil
	default:
		return nil, fmt.Errorf("%w: %d", errInspectUnknownKind, kind)
	}
}

// count is storeInspector.count: the number of keys in the buckets holding
// kind.
func (db *db) count(kind recordKind) (int, error) {
	buckets, err := inspectBuckets(kind)
	if err != nil {
		return 0, err
	}

	n := 0

	err = db.bolt.View(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			bucket := tx.Bucket(name)
			if bucket == nil {
				return fmt.Errorf("%w: %s", errInspectMissingBucket, name)
			}

			n += bucket.Stats().KeyN
		}

		return nil
	})

	return n, err
}

// liveJob is storeInspector.liveJob: key's live record with its matching
// run-state record applied, as recovery reads it.
func (db *db) liveJob(key string) (*Job, bool, error) {
	var job *Job

	err := db.bolt.View(func(tx *bolt.Tx) error {
		var errl error

		job, errl = db.liveJobTx(tx, key)

		return errl
	})

	return job, job != nil, err
}

// liveJobTx is liveJob in tx, returning a nil job if key has no live record. A
// database without a run-state bucket has no record to apply.
func (db *db) liveJobTx(tx *bolt.Tx, key string) (*Job, error) {
	live := tx.Bucket(bucketJobsLive).Get([]byte(key))
	if live == nil {
		return nil, nil //nolint:nilnil // no live record is not an error
	}

	job, err := db.decodeJob(live)
	if err != nil {
		return nil, err
	}

	var record []byte
	if bucket := tx.Bucket(bucketJobRunState); bucket != nil {
		record = bucket.Get([]byte(key))
	}

	runState, matches, err := db.decodeMatchingRunState(live, record)
	if err != nil {
		return nil, err
	}

	if matches {
		runState.applyTo(job)
	}

	return job, nil
}

// completeJob is storeInspector.completeJob: key's record in the complete
// bucket.
func (db *db) completeJob(key string) (*Job, bool, error) {
	var job *Job

	err := db.bolt.View(func(tx *bolt.Tx) error {
		encoded := tx.Bucket(bucketJobsComplete).Get([]byte(key))
		if encoded == nil {
			return nil
		}

		var errd error

		job, errd = db.decodeJob(encoded)

		return errd
	})

	return job, job != nil, err
}

// forceBackup is storeInspector.forceBackup: it copies the database to a
// temporary file and moves it over the backup, as a periodic backup does, even
// if periodic backups are disabled.
func (db *db) forceBackup() error {
	if db.backupPath == "" {
		return errInspectNoBackups
	}

	tmp := db.backupPathTmp + ".forced"

	if err := db.copyBackup(tmp); err != nil {
		return err
	}

	db.handleSucceededBackup(context.Background(), tmp)

	return nil
}

// flush is storeInspector.flush: it waits for every expected job exit update
// and every tracked asynchronous write transaction, as close() does, failing
// if the writes are not done within inspectFlushWait. Neither wait gives up by
// itself (db.wg.Wait only logs at that point), so they run on their own
// goroutine, which ends once the writes do.
func (db *db) flush() error {
	done := make(chan struct{})

	go func() {
		defer close(done)

		db.waitForJobExitUpdates()

		db.wgMutex.Lock()
		defer db.wgMutex.Unlock()

		db.wg.Wait(inspectFlushWait)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(inspectFlushWait):
		return fmt.Errorf("%w after %s", errInspectFlushTimeout, inspectFlushWait)
	}
}
