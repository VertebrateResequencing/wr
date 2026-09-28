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

// Timing probes behind the cold-start and freelist fixes (.docs/bugfixes/
// 260928-1.md). They need a big database on NFS, so they only run when pointed
// at a WRITABLE COPY of one (they commit to it): WR_DBSTART_DB.
//
//   CGO_ENABLED=1 WR_DBSTART_DB=/nfs/.../copy.db go test -tags "netgo reliability_repro" \
//     ./jobqueue -run 'TestDBStartProbe' -v -timeout 2h
//
// Every open takes bbolt's flock, which on NFS drops the client's cached pages
// for the file, so each run below starts cold whatever ran before it.

import (
	"context"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

var dbStartProbeBucket = []byte("dbstartprobe")

// TestDBStartProbeRecovery times opening the manager database and decoding its
// live bucket as recovery does, with each way of warming the file after the
// open's flock.
func TestDBStartProbeRecovery(t *testing.T) {
	path := dbStartProbePath(t)

	modes := []string{"none", "seq8", "concurrent8", "fadvise", "fadviseloop", "populate", "prefetch"}
	if v := os.Getenv("WR_DBSTART_MODES"); v != "" {
		modes = strings.Split(v, ",")
	}

	for _, mode := range modes {
		open, warm, decode, jobs := dbStartProbeRecoveryRun(t, path, mode)
		t.Logf("DBSTART-RECOVERY mode=%s open=%s warm=%s decode=%s jobs=%d total=%s", mode,
			open.Round(time.Millisecond), warm.Round(time.Millisecond), decode.Round(time.Millisecond), jobs,
			(open + warm + decode).Round(time.Millisecond))
	}
}

// TestDBStartProbeCommitCost times, and counts the bytes allocated by, one-key
// commits to the database with each freelist option.
func TestDBStartProbeCommitCost(t *testing.T) {
	path := dbStartProbePath(t)

	for _, mode := range []string{"map", "array", "map-nofreelistsync"} {
		opts := &bolt.Options{FreelistType: bolt.FreelistMapType, Timeout: 10 * time.Second}

		switch mode {
		case "array":
			opts.FreelistType = bolt.FreelistArrayType
		case "map-nofreelistsync":
			opts.NoFreelistSync = true
		}

		t0 := time.Now()

		bdb, err := bolt.Open(path, dbFilePermission, opts)
		if err != nil {
			t.Fatal(err)
		}

		open := time.Since(t0)
		median, allocs := dbStartProbeCommits(t, bdb)
		st := bdb.Stats()

		if err = bdb.Close(); err != nil {
			t.Fatal(err)
		}

		t.Logf("DBSTART-COMMIT mode=%s open=%s free_pages=%d median_commit=%s alloc_per_commit=%dKB",
			mode, open.Round(time.Millisecond), st.FreePageN, median.Round(10*time.Microsecond), allocs>>10)
	}
}

// TestDBStartProbeNoFreelistSyncOpen times the opens that must rebuild an
// unsynced freelist by walking the database: cold, with MAP_POPULATE, and with
// the file read in parallel while bbolt walks it.
func TestDBStartProbeNoFreelistSyncOpen(t *testing.T) {
	path := dbStartProbePath(t)

	convert, err := bolt.Open(path, dbFilePermission, &bolt.Options{FreelistType: bolt.FreelistMapType,
		NoFreelistSync: true, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	if err = convert.Update(func(tx *bolt.Tx) error {
		_, errc := tx.CreateBucketIfNotExists(dbStartProbeBucket)

		return errc
	}); err != nil {
		t.Fatal(err)
	}

	if err = convert.Close(); err != nil {
		t.Fatal(err)
	}

	modes := []string{"populate", "concurrent8", "none"}
	if os.Getenv("WR_DBSTART_SKIP_COLD_WALK") != "" {
		modes = modes[:2]
	}

	for _, mode := range modes {
		opts := &bolt.Options{FreelistType: bolt.FreelistMapType, NoFreelistSync: true, Timeout: 10 * time.Second}

		ctx, cancel := context.WithCancel(context.Background())

		var wg sync.WaitGroup

		switch mode {
		case "populate":
			opts.MmapFlags = syscall.MAP_POPULATE
		case "concurrent8":
			opts.OpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
				f, erro := os.OpenFile(name, flag, perm)
				if erro == nil {
					wg.Go(func() {
						// give bbolt's flock time to drop the cache before we fill it
						select {
						case <-ctx.Done():
							return
						case <-time.After(50 * time.Millisecond):
						}

						dbStartProbeRead(t, name, 8)
					})
				}

				return f, erro
			}
		}

		if os.Getenv("WR_DBSTART_EVICT") != "" {
			dbStartProbeEvict(t, path)
		}

		t0 := time.Now()

		bdb, erro := bolt.Open(path, dbFilePermission, opts)
		if erro != nil {
			t.Fatal(erro)
		}

		open := time.Since(t0)

		cancel()
		wg.Wait()

		t.Logf("DBSTART-NOFREELISTSYNC mode=%s open=%s free_pages=%d", mode, open.Round(time.Millisecond),
			bdb.Stats().FreePageN)

		bdb.Close() //nolint:errcheck
	}
}

// TestDBStartProbeGrowLive copies WR_DBSTART_GROW_LIVE complete jobs into the
// live bucket, 500 per transaction, so the copy's live bucket is the size of a
// soak's (the aslfixture has 20k live jobs; the soak restarted with 57k-121k).
func TestDBStartProbeGrowLive(t *testing.T) {
	path := dbStartProbePath(t)

	n, err := strconv.Atoi(os.Getenv("WR_DBSTART_GROW_LIVE"))
	if err != nil {
		t.Skip("set WR_DBSTART_GROW_LIVE to the number of jobs to add")
	}

	bdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{FreelistType: bolt.FreelistMapType,
		Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	var after []byte

	for added := 0; added < n; {
		err = bdb.Update(func(tx *bolt.Tx) error {
			live := tx.Bucket(bucketJobsLive)
			c := tx.Bucket(bucketJobsComplete).Cursor()

			k, v := c.First()
			if after != nil {
				k, v = c.Seek(after)
				if k != nil && string(k) == string(after) {
					k, v = c.Next()
				}
			}

			for i := 0; i < 500 && k != nil && added < n; k, v = c.Next() {
				if errp := live.Put(k, v); errp != nil {
					return errp
				}

				after = append(after[:0], k...)
				added++
				i++
			}

			if k == nil {
				added = n
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	var live int

	bdb.View(func(tx *bolt.Tx) error { //nolint:errcheck
		live = tx.Bucket(bucketJobsLive).Stats().KeyN

		return nil
	})

	t.Logf("DBSTART-GROW live=%d free_pages=%d", live, bdb.Stats().FreePageN)
}

// TestDBStartProbeCompact times `wr manager compact`'s CompactDBFileStats on the
// database (which it replaces with the compacted copy).
func TestDBStartProbeCompact(t *testing.T) {
	path := dbStartProbePath(t)
	if os.Getenv("WR_DBSTART_COMPACT") == "" {
		t.Skip("set WR_DBSTART_COMPACT to compact WR_DBSTART_DB")
	}

	t0 := time.Now()

	stats, err := CompactDBFileStats(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("DBSTART-COMPACT took=%s before=%dMB after=%dMB", time.Since(t0).Round(time.Millisecond),
		stats.BeforeSize>>20, stats.AfterSize>>20)
}

func dbStartProbePath(t *testing.T) string {
	t.Helper()

	path := os.Getenv("WR_DBSTART_DB")
	if path == "" {
		t.Skip("set WR_DBSTART_DB to a writable copy of a big manager database")
	}

	return path
}

// dbStartProbeCommits does 50 one-key commits and returns their median time, and
// the mean bytes allocated per commit.
func dbStartProbeCommits(t *testing.T, bdb *bolt.DB) (time.Duration, uint64) {
	t.Helper()

	const n = 50

	times := make([]time.Duration, 0, n)

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	for i := range n {
		t0 := time.Now()

		if err := bdb.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists(dbStartProbeBucket)
			if err != nil {
				return err
			}

			return b.Put([]byte("tick"), []byte{byte(i)})
		}); err != nil {
			t.Fatal(err)
		}

		times = append(times, time.Since(t0))
	}

	runtime.ReadMemStats(&after)

	sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })

	return times[n/2], (after.TotalAlloc - before.TotalAlloc) / n
}

func dbStartProbeRecoveryRun(t *testing.T, path, mode string) (time.Duration, time.Duration, time.Duration, int) {
	t.Helper()

	opts := &bolt.Options{FreelistType: bolt.FreelistMapType, Timeout: 10 * time.Second}
	if mode == "populate" {
		opts.MmapFlags = syscall.MAP_POPULATE
	}

	if os.Getenv("WR_DBSTART_EVICT") != "" {
		dbStartProbeEvict(t, path)
	}

	t0 := time.Now()

	var (
		bdb *bolt.DB
		err error
	)

	if strings.HasPrefix(mode, "prefetch") {
		bdb, err = openManagerBolt(context.Background(), path)
	} else {
		bdb, err = bolt.Open(path, dbFilePermission, opts)
	}

	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	open := time.Since(t0)
	t1 := time.Now()

	var wg sync.WaitGroup

	switch mode {
	case "seq8":
		dbStartProbeRead(t, path, 8)
	case "concurrent8":
		wg.Go(func() { dbStartProbeRead(t, path, 8) })
	case "fadvise":
		dbStartProbeFadvise(t, path, 0)
	case "fadviseloop":
		dbStartProbeFadvise(t, path, 128<<10)
	}

	warm := time.Since(t1)
	t2 := time.Now()
	d := &db{bolt: bdb, ch: new(codec.BincHandle)}

	var jobs []*Job
	if mode == "parallel16" {
		jobs = dbStartProbeParallelDecode(t, d, 16)
	} else {
		jobs, err = d.recoverIncompleteJobs()
		if err != nil {
			t.Fatal(err)
		}
	}

	decode := time.Since(t2)

	wg.Wait()

	// as a clean close does, so the next open reads the freelist
	if mode == "prefetch-sync" {
		d.syncFreelist(context.Background())
	}

	return open, warm, decode, len(jobs)
}

// dbStartProbeEvict drops the file's clean pages from the page cache, so a probe
// of a local-disk copy starts cold.
func dbStartProbeEvict(t *testing.T, path string) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED); err != nil {
		t.Fatal(err)
	}
}

// dbStartProbeRead reads the whole file with streams parallel sequential readers.
func dbStartProbeRead(t *testing.T, path string, streams int) {
	t.Helper()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	chunk := (fi.Size() + int64(streams) - 1) / int64(streams)

	var wg sync.WaitGroup

	for i := range streams {
		wg.Go(func() {
			f, erro := os.Open(path)
			if erro != nil {
				return
			}
			defer f.Close()

			io.Copy(io.Discard, io.NewSectionReader(f, int64(i)*chunk, chunk)) //nolint:errcheck
		})
	}

	wg.Wait()
}

// dbStartProbeFadvise asks for the whole file with FADV_WILLNEED, in one call if
// chunk is 0, else chunk bytes per call.
func dbStartProbeFadvise(t *testing.T, path string, chunk int64) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if chunk == 0 {
		chunk = fi.Size()
	}

	for off := int64(0); off < fi.Size(); off += chunk {
		if err = unix.Fadvise(int(f.Fd()), off, chunk, unix.FADV_WILLNEED); err != nil {
			t.Fatal(err)
		}
	}
}

// dbStartProbeParallelDecode decodes the live bucket in n key ranges at once.
func dbStartProbeParallelDecode(t *testing.T, d *db, n int) []*Job {
	t.Helper()

	parts := make([][]*Job, n)

	err := d.bolt.View(func(tx *bolt.Tx) error {
		var wg sync.WaitGroup

		for i := range n {
			wg.Go(func() {
				c := tx.Bucket(bucketJobsLive).Cursor()
				start := []byte{byte(i * 256 / n)}
				end := byte((i + 1) * 256 / n)

				k, v := c.Seek(start)
				if i == 0 {
					k, v = c.First()
				}

				for ; k != nil && (i == n-1 || k[0] < end); k, v = c.Next() {
					if v == nil {
						continue
					}

					job, errd := d.decodeJob(v)
					if errd != nil {
						return
					}

					parts[i] = append(parts[i], job)
				}
			})
		}

		wg.Wait()

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var jobs []*Job
	for _, p := range parts {
		jobs = append(jobs, p...)
	}

	return jobs
}
