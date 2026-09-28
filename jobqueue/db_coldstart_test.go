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

// Tests for the manager database's cold start on NFS and its per-commit
// freelist cost (.docs/bugfixes/260928-db-cold-start-and-freelist.md).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

var coldStartScratchBucket = []byte("coldStartScratch") //nolint:gochecknoglobals

// coldStartFreePages is how many free pages makeFreePages leaves: enough that
// writing the freelist at a commit takes over 30 pages.
const coldStartFreePages = 16 << 10

func TestPrefetchFile(t *testing.T) {
	Convey("prefetchFile reads every byte of a file, whatever its size", t, func() {
		dir := t.TempDir()

		for _, size := range []int{0, 1, managerDBPrefetchStreams - 1, 3*prefetchChunkBytes + 7} {
			path := filepath.Join(dir, strconv.Itoa(size))
			So(os.WriteFile(path, make([]byte, size), 0o600), ShouldBeNil)

			result := prefetchFile(context.Background(), path)
			So(result.err, ShouldBeNil)
			So(result.read, ShouldEqual, size)
			So(result.size, ShouldEqual, size)
		}
	})

	Convey("prefetchFile stops when its context is done", t, func() {
		path := filepath.Join(t.TempDir(), "f")
		So(os.WriteFile(path, make([]byte, 4*prefetchChunkBytes), 0o600), ShouldBeNil)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result := prefetchFile(ctx, path)
		So(result.err, ShouldBeNil)
		So(result.read, ShouldEqual, 0)
	})
}

func TestManagerDBCommitFreelistCost(t *testing.T) {
	Convey("A commit to the manager's database does work unrelated to how many pages are free", t, func() {
		ctx := context.Background()
		dbFile := filepath.Join(t.TempDir(), "queue.db")

		testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		makeFreePages(testDB.bolt)
		So(testDB.bolt.Stats().FreePageN, ShouldBeGreaterThanOrEqualTo, coldStartFreePages)

		stats := testDB.bolt.Stats()
		before := stats.TxStats.GetPageAlloc()

		err = testDB.bolt.Update(func(tx *bolt.Tx) error {
			return tx.Bucket(coldStartScratchBucket).Put([]byte("tick"), []byte("tock"))
		})
		So(err, ShouldBeNil)

		// the one-key commit dirties the bucket's leaf and the root; writing the
		// freelist of 16Ki free pages would allocate another 33.
		stats = testDB.bolt.Stats()
		allocated := stats.TxStats.GetPageAlloc() - before
		So(allocated, ShouldBeLessThanOrEqualTo, int64(4*testDB.bolt.Info().PageSize))
	})
}

func TestManagerDBFreelistCrashSafety(t *testing.T) {
	Convey("Given a manager database with jobs and free pages", t, func() {
		ctx := context.Background()
		dbFile := filepath.Join(t.TempDir(), "queue.db")

		testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
		So(err, ShouldBeNil)

		jobs := make([]*Job, 0, 100)
		for i := range 100 {
			jobs = append(jobs, testDBJob(fmt.Sprintf("echo %d", i), "coldstart"))
		}

		_, _, _, err = testDB.storeNewJobs(ctx, jobs, false)
		So(err, ShouldBeNil)

		makeFreePages(testDB.bolt)

		st := testDB.bolt.Stats()
		free := st.FreePageN + st.PendingPageN

		Convey("a manager that dies without closing it leaves it whole, with the same free pages", func() {
			// bolt.Close commits nothing, so the file is as a killed manager
			// leaves it: its last commit and nothing more.
			So(testDB.bolt.Close(), ShouldBeNil)
			So(boltFreelistSynced(dbFile), ShouldBeFalse)

			// only stops the dead manager's writers: its bolt is already closed
			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			logs := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			reDB, _, errr := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
			So(errr, ShouldBeNil)

			// bbolt rebuilds the freelist by reading every page, so the file is
			// read ahead of it wherever it is
			So(logs.String(), ShouldContainSubstring, `msg="prefetched database"`)

			defer func() { So(reDB.close(ctx), ShouldBeNil) }()

			So(reDB.bolt.Stats().FreePageN, ShouldEqual, free)
			So(boltCheckErrors(reDB.bolt), ShouldBeEmpty)

			recovered, errr := reDB.recoverIncompleteJobs()
			So(errr, ShouldBeNil)
			So(len(recovered), ShouldEqual, len(jobs))

			Convey("and writing to it again reuses no page still in use", func() {
				makeFreePages(reDB.bolt)
				So(boltCheckErrors(reDB.bolt), ShouldBeEmpty)

				recovered, errr = reDB.recoverIncompleteJobs()
				So(errr, ShouldBeNil)
				So(len(recovered), ShouldEqual, len(jobs))
			})
		})

		Convey("closing it cleanly records its freelist, so the next open need not rebuild it", func() {
			So(testDB.close(ctx), ShouldBeNil)
			So(boltFreelistSynced(dbFile), ShouldBeTrue)

			reDB, _, errr := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
			So(errr, ShouldBeNil)

			defer func() { So(reDB.close(ctx), ShouldBeNil) }()

			// less the pages the freelist itself was written to
			freelistPages := free*8/reDB.bolt.Info().PageSize + 2
			So(reDB.bolt.Stats().FreePageN, ShouldBeBetweenOrEqual, free-freelistPages, free)
			So(boltCheckErrors(reDB.bolt), ShouldBeEmpty)
		})
	})
}

func TestManagerDBPrefetch(t *testing.T) {
	Convey("Given a closed manager database", t, func() {
		ctx := context.Background()
		dbFile := filepath.Join(t.TempDir(), "queue.db")

		testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
		So(err, ShouldBeNil)

		makeFreePages(testDB.bolt)
		So(testDB.close(ctx), ShouldBeNil)

		info, err := os.Stat(dbFile)
		So(err, ShouldBeNil)

		logs := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		origDrops := managerDBOpenDropsCache
		origTimeout := managerDBPrefetchTimeout

		defer func() {
			managerDBOpenDropsCache = origDrops
			managerDBPrefetchTimeout = origTimeout
		}()

		open := func() {
			reDB, _, errr := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
			So(errr, ShouldBeNil)
			So(reDB.close(ctx), ShouldBeNil)
		}

		Convey("opening it where the open drops the file's cached pages reads the whole file", func() {
			managerDBOpenDropsCache = func(string) bool { return true }

			open()

			So(logs.String(), ShouldContainSubstring, `msg="prefetched database"`)
			So(logs.String(), ShouldContainSubstring, "bytes="+strconv.FormatInt(info.Size(), 10)+" ")
		})

		Convey("the read gives up at its deadline, and the open still succeeds", func() {
			managerDBOpenDropsCache = func(string) bool { return true }
			managerDBPrefetchTimeout = time.Nanosecond

			open()

			So(logs.String(), ShouldContainSubstring, `msg="prefetched database"`)
			So(logs.String(), ShouldContainSubstring, "complete=false")
		})

		Convey("opening it where the open keeps the file's cached pages reads nothing extra", func() {
			managerDBOpenDropsCache = func(string) bool { return false }

			open()

			So(logs.String(), ShouldNotContainSubstring, "prefetched database")
		})
	})
}

// makeFreePages writes and then deletes large values in coldStartScratchBucket,
// leaving at least coldStartFreePages free pages (and the bucket in place).
func makeFreePages(bdb *bolt.DB) {
	const valBytes = 64 << 10

	n := coldStartFreePages*bdb.Info().PageSize/valBytes + 1
	val := make([]byte, valBytes)

	err := bdb.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(coldStartScratchBucket)
		if err != nil {
			return err
		}

		for i := range n {
			if err = b.Put([]byte(strconv.Itoa(i)), val); err != nil {
				return err
			}
		}

		return nil
	})
	So(err, ShouldBeNil)

	err = bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(coldStartScratchBucket)
		for i := range n {
			if err = b.Delete([]byte(strconv.Itoa(i))); err != nil {
				return err
			}
		}

		return nil
	})
	So(err, ShouldBeNil)

	// the deleted pages are pending until two later commits release them
	for range 2 {
		So(bdb.Update(func(tx *bolt.Tx) error {
			return tx.Bucket(coldStartScratchBucket).Put([]byte("tick"), nil)
		}), ShouldBeNil)
	}
}

// boltCheckErrors returns the consistency errors bbolt's Check finds in bdb.
func boltCheckErrors(bdb *bolt.DB) []string {
	var errs []string

	err := bdb.View(func(tx *bolt.Tx) error {
		for errc := range tx.Check() {
			errs = append(errs, errc.Error())
		}

		return nil
	})
	So(err, ShouldBeNil)

	return errs
}
