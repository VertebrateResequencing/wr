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

// Probes for the second prodsim round: every bbolt commit writes the whole
// freelist (the manager opens its database without NoFreelistSync), and the
// map freelist rebuilds a sorted slice of every free page id to do it, so a
// commit's cost grows with the number of free pages, however small the
// transaction.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// TestProdsimFreelistStats reports the freelist of an existing database
// (WR_PRODSIM_FREELIST_DB, opened read-only), eg. the aslfixture.
func TestProdsimFreelistStats(t *testing.T) {
	path := os.Getenv("WR_PRODSIM_FREELIST_DB")
	if path == "" {
		t.Skip("set WR_PRODSIM_FREELIST_DB")
	}

	t0 := time.Now()

	bdb, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, FreelistType: bolt.FreelistMapType,
		Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	st := bdb.Stats()
	t.Logf("PRODSIM-FREELIST db=%s size=%dMB pagesize=%d free_pages=%d pending_pages=%d free_alloc=%dMB "+
		"freelist_inuse=%dMB open=%s", path, fi.Size()>>20, bdb.Info().PageSize, st.FreePageN, st.PendingPageN,
		st.FreeAlloc>>20, st.FreelistInuse>>20, time.Since(t0).Round(time.Millisecond))
}

const prodsimFreelistBucket = "fl"

// TestProdsimFreelistCommitCost times a one-key commit against databases whose
// freelist holds a growing number of free pages (0, 64Ki, 256Ki, 1Mi).
// WR_PRODSIM_FREELIST_DIR chooses the filesystem (default a temp dir);
// WR_PRODSIM_FREELIST_MAX_PAGES stops the steps early, since making 1Mi free
// pages writes 4GiB.
func TestProdsimFreelistCommitCost(t *testing.T) {
	dir := os.Getenv("WR_PRODSIM_FREELIST_DIR")
	if dir == "" {
		dir = t.TempDir()
	}

	maxPages := 1 << 20
	if v := os.Getenv("WR_PRODSIM_FREELIST_MAX_PAGES"); v != "" {
		fmt.Sscanf(v, "%d", &maxPages) //nolint:errcheck
	}

	var base, baseM time.Duration

	for _, freePages := range []int{0, 64 << 10, 256 << 10, 1 << 20} {
		if freePages > maxPages {
			break
		}

		median, fl, reopen := prodsimFreelistRun(t, filepath.Join(dir, fmt.Sprintf("fl%d.db", freePages)),
			freePages, false)
		if freePages == 0 {
			base = median
		}

		t.Logf("PRODSIM-FREELIST-COST free_pages=%d freelist_bytes=%dKB median_commit=%s reopen=%s",
			freePages, fl>>10, median.Round(10*time.Microsecond), reopen.Round(time.Millisecond))

		// the same, with the freelist not written at each commit (bbolt then
		// rebuilds it by scanning the whole database at open)
		mNS, _, reopenNS := prodsimFreelistRun(t, filepath.Join(dir, fmt.Sprintf("flns%d.db", freePages)),
			freePages, true)
		t.Logf("PRODSIM-FREELIST-COST NoFreelistSync free_pages=%d median_commit=%s reopen=%s",
			freePages, mNS.Round(10*time.Microsecond), reopenNS.Round(time.Millisecond))

		// and as the manager opens its database (openManagerBolt), which is what
		// the gate is about: its commits must not cost more as the freelist grows
		mM, _, reopenM := prodsimFreelistRunWith(t, filepath.Join(dir, fmt.Sprintf("flm%d.db", freePages)),
			freePages, func(path string) (*bolt.DB, error) { return openManagerBolt(context.Background(), path) })
		if freePages == 0 {
			baseM = mM
		}

		t.Logf("PRODSIM-FREELIST-COST manager free_pages=%d median_commit=%s reopen=%s",
			freePages, mM.Round(10*time.Microsecond), reopenM.Round(time.Millisecond))

		if freePages >= 256<<10 && mM > 10*baseM+5*time.Millisecond {
			t.Errorf("a manager one-key commit took %s with %d free pages, vs %s with none (raw bbolt: %s vs %s)",
				mM, freePages, baseM, median, base)
		}
	}
}

// prodsimFreelistRun makes a database with about freePages free pages (by
// writing 1MiB values and deleting them) and returns the median time of 50
// one-key commits, the freelist's size in bytes and how long reopening the
// database took.
func prodsimFreelistRun(t *testing.T, path string, freePages int,
	noFreelistSync bool) (time.Duration, int, time.Duration) {
	t.Helper()

	opts := &bolt.Options{FreelistType: bolt.FreelistMapType, NoFreelistSync: noFreelistSync}

	return prodsimFreelistRunWith(t, path, freePages, func(path string) (*bolt.DB, error) {
		return bolt.Open(path, 0o600, opts)
	})
}

// prodsimFreelistRunWith is prodsimFreelistRun with the database opened by
// open.
func prodsimFreelistRunWith(t *testing.T, path string, freePages int,
	open func(string) (*bolt.DB, error)) (time.Duration, int, time.Duration) {
	t.Helper()

	defer os.Remove(path)

	bdb, err := open(path)
	if err != nil {
		t.Fatal(err)
	}

	const valBytes = 1 << 20

	val := make([]byte, valBytes)
	// fragment the free space: write in pairs and delete only one of each pair,
	// so the freelist is many spans, as a long-lived database's is.
	pairs := freePages * bdb.Info().PageSize / valBytes

	for i := 0; i < pairs; i += 64 {
		err = bdb.Update(func(tx *bolt.Tx) error {
			b, errc := tx.CreateBucketIfNotExists([]byte(prodsimFreelistBucket))
			if errc != nil {
				return errc
			}

			for j := i; j < min(i+64, pairs); j++ {
				if errp := b.Put(fmt.Appendf(nil, "del%09d", j), val); errp != nil {
					return errp
				}

				if errp := b.Put(fmt.Appendf(nil, "keep%09d", j), val[:4096]); errp != nil {
					return errp
				}
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	err = bdb.Update(func(tx *bolt.Tx) error {
		b, errc := tx.CreateBucketIfNotExists([]byte(prodsimFreelistBucket))
		if errc != nil {
			return errc
		}

		for j := range pairs {
			if errd := b.Delete(fmt.Appendf(nil, "del%09d", j)); errd != nil {
				return errd
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// two empty commits so the deleted pages move from pending to free
	for range 2 {
		if err = bdb.Update(func(tx *bolt.Tx) error {
			return tx.Bucket([]byte(prodsimFreelistBucket)).Put([]byte("tick"), []byte("x"))
		}); err != nil {
			t.Fatal(err)
		}
	}

	times := make([]time.Duration, 0, 50)

	for i := range 50 {
		t0 := time.Now()

		if err = bdb.Update(func(tx *bolt.Tx) error {
			return tx.Bucket([]byte(prodsimFreelistBucket)).Put([]byte("tick"), fmt.Appendf(nil, "%d", i))
		}); err != nil {
			t.Fatal(err)
		}

		times = append(times, time.Since(t0))
	}

	sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })

	st := bdb.Stats()

	if err = bdb.Close(); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()

	bdb, err = open(path)
	if err != nil {
		t.Fatal(err)
	}

	reopen := time.Since(t0)

	bdb.Close()

	return times[len(times)/2], (st.FreePageN + st.PendingPageN) * 8, reopen
}

// TestProdsimNoFreelistSyncReopen measures what NoFreelistSync would cost at
// startup: bbolt then rebuilds the freelist at open by walking every page. It
// converts WR_PRODSIM_FREELIST_COPY (a WRITABLE COPY of a big database, which
// it modifies) to an unsynced freelist with one commit, then times two opens.
// On NFS each open's flock drops the page cache, so both walk it cold.
func TestProdsimNoFreelistSyncReopen(t *testing.T) {
	path := os.Getenv("WR_PRODSIM_FREELIST_COPY")
	if path == "" {
		t.Skip("set WR_PRODSIM_FREELIST_COPY to a writable copy of a database")
	}

	opts := &bolt.Options{FreelistType: bolt.FreelistMapType, NoFreelistSync: true, Timeout: 10 * time.Second}

	for i := range 3 {
		t0 := time.Now()

		bdb, err := bolt.Open(path, 0o600, opts)
		if err != nil {
			t.Fatal(err)
		}

		open := time.Since(t0)
		st := bdb.Stats()

		if i == 0 {
			if err = bdb.Update(func(tx *bolt.Tx) error {
				b, errc := tx.CreateBucketIfNotExists([]byte(prodsimFreelistBucket))
				if errc != nil {
					return errc
				}

				return b.Put([]byte("tick"), []byte("x"))
			}); err != nil {
				t.Fatal(err)
			}
		}

		bdb.Close()

		t.Logf("PRODSIM-NOFREELISTSYNC open=%d took=%s free_pages=%d", i, open.Round(time.Millisecond), st.FreePageN)
	}
}
