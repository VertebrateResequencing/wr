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

package jobqueue

// Reproducer for prodsim FINDING 2
// (.docs/bugfixes/260927-prodsim-findings.md): while the periodic backup
// holds its read transaction, a write that grows the database past the current
// mmap size has to remap, and bbolt's remap waits for every open read
// transaction. The writer holds the write lock while it waits, and the pending
// remap blocks every new read transaction, so every database read and write in
// the manager stalls until the backup copy finishes. #632 fixes it by mapping
// the DB with headroom; its untagged TestDBBackupRemap is the deterministic
// gate, and this test measures how long the stall is on a chosen filesystem
// (developers/wrdev.sh remap-stall-check). It fails until #632 merges.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	bolt "go.etcd.io/bbolt"
)

// TestProdsimBackupRemapStall reports the worst latency of a trivial database
// read and of a small write while a paced backup copy is running, first when
// the writes stay inside the current mmap and then when one of them crosses
// what would be bbolt's default mapping (with #632, the manager's mapping has
// headroom and they no longer cross it). WR_PRODSIM_REMAP_DIR puts the
// database on a chosen filesystem (eg. NFS); WR_PRODSIM_REMAP_BACKUP_SECS is
// roughly how long the backup copy takes.
func TestProdsimBackupRemapStall(t *testing.T) {
	dir := os.Getenv("WR_PRODSIM_REMAP_DIR")
	if dir == "" {
		dir = t.TempDir()
	}

	backupSecs := 8.0
	if v := os.Getenv("WR_PRODSIM_REMAP_BACKUP_SECS"); v != "" {
		fmt.Sscanf(v, "%g", &backupSecs) //nolint:errcheck
	}

	for _, cross := range []bool{false, true} {
		read, write, backup := prodsimRemapRun(t, dir, cross, backupSecs)
		t.Logf("PRODSIM-REMAP crossesMmap=%v backup=%s maxRead=%s maxWrite=%s", cross,
			backup.Round(time.Millisecond), read.Round(time.Millisecond), write.Round(time.Millisecond))

		if cross && read > backup/2 {
			t.Errorf("a trivial read waited %s behind a %s backup because a write crossed the mmap size",
				read.Round(time.Millisecond), backup.Round(time.Millisecond))
		}
	}
}

func prodsimRemapRun(t *testing.T, dir string, cross bool, backupSecs float64) (maxRead, maxWrite,
	backupTook time.Duration) {
	t.Helper()

	ctx := context.Background()
	dbFile := filepath.Join(dir, fmt.Sprintf("remap-%v.db", cross))

	fillBucket := []byte("prodsimfill")
	removeDB := func() {
		for _, f := range []string{dbFile, dbFile + "_bk"} {
			if errr := os.Remove(f); errr != nil && !os.IsNotExist(errr) {
				t.Logf("could not remove %s: %s", f, errr)
			}
		}
	}

	removeDB()
	t.Cleanup(removeDB)

	// fill to ~100MiB in a first open, so the reopen maps 128MiB and ~28MiB of
	// writes cross it.
	db1, _, err := initDB(ctx, dbFile, dbFile+"_bk", internal.Development, false, false)
	if err != nil {
		t.Fatal(err)
	}

	value := make([]byte, 1<<20)

	for i := range 100 {
		if err = db1.bolt.Update(func(tx *bolt.Tx) error {
			b, errc := tx.CreateBucketIfNotExists(fillBucket)
			if errc != nil {
				return errc
			}

			return b.Put(fmt.Appendf(nil, "fill%04d", i), value)
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err = db1.close(ctx); err != nil {
		t.Fatal(err)
	}

	testDB, _, err := initDB(ctx, dbFile, dbFile+"_bk", internal.Development, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errc := testDB.close(ctx); errc != nil {
			t.Logf("close: %s", errc)
		}
	}()

	// slow the real backup copy to backupSecs, as a multi-GB copy to NFS is.
	info, err := os.Stat(dbFile)
	if err != nil {
		t.Fatalf("stat %s: %s", dbFile, err)
	}

	oldBytes, oldHook := backupCopySyncBytes, backupPaceHook
	backupCopySyncBytes = 1 << 20
	paces := float64(info.Size()) / float64(backupCopySyncBytes)
	backupPaceHook = func() { time.Sleep(time.Duration(backupSecs / paces * float64(time.Second))) }

	defer func() { backupCopySyncBytes, backupPaceHook = oldBytes, oldHook }()

	var (
		wg   sync.WaitGroup
		done atomic.Bool
		mu   sync.Mutex
	)

	record := func(d time.Duration, into *time.Duration) {
		mu.Lock()
		*into = max(*into, d)
		mu.Unlock()
	}

	// any backup initDB made is removed, so the file checked after the copy can
	// only be the copy's own
	if err = os.Remove(dbFile + "_bk"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	wg.Add(1)

	go func() {
		defer wg.Done()

		t0 := time.Now()

		testDB.backupToBackupFile(ctx, false)

		backupTook = time.Since(t0)

		done.Store(true)
	}()

	time.Sleep(500 * time.Millisecond) // the backup's read tx is open

	// a reader doing what every status request's first step does
	wg.Add(1)

	go func() {
		defer wg.Done()

		for !done.Load() {
			t0 := time.Now()

			if _, errr := testDB.retrieveLastCompletionTimeByRepGroup([]string{"rg"}); errr != nil {
				t.Logf("read: %s", errr)
			}

			record(time.Since(t0), &maxRead)
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// writes: without crossing, 40 small records; crossing, 40 new 1MiB
	// values, taking the file past the 128MiB mapping.
	small := make([]byte, 100)

	for i := range 40 {
		key, val := fmt.Appendf(nil, "small%04d", i), small
		if cross {
			key, val = fmt.Appendf(nil, "grow%04d", i), value
		}

		t0 := time.Now()

		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			return tx.Bucket(fillBucket).Put(key, val)
		})
		if err != nil {
			break
		}

		record(time.Since(t0), &maxWrite)
		time.Sleep(20 * time.Millisecond)
	}

	wg.Wait()

	if err != nil {
		t.Fatal(err)
	}

	// backupToBackupFile returns no error, so without this a backup that failed
	// early would look like a short copy and let the check pass falsely. Any
	// earlier backup was removed before the copy began, so an existing file is
	// this copy's (no mtime comparison, which NFS's coarse or skewed clocks
	// would make unreliable).
	bk, errs := os.Stat(dbFile + "_bk")
	if errs != nil || bk.Size() == 0 {
		t.Fatalf("the backup did not produce %s_bk during the copy (stat err %v)", dbFile, errs)
	}

	return maxRead, maxWrite, backupTook
}
