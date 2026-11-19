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
	"crypto/md5" //nolint:gosec // job keys are MD5s
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

// TestStoreFoldBigHistoryThroughput measures how many items a second the
// history.bolt folder commits into a large history.bolt, such as a copy of a
// crash soak's (WR_FOLD_BENCH_DIR names the directory holding it; it is
// written to, so it must be a copy): a minute of the 1g.3 soak's offered fold
// work, WR_FOLD_BENCH_LOOKUPS rglookups (19,600) and WR_FOLD_BENCH_COMPLETES
// completions (4,900), in foldChunkRecords chunks of new random keys, each
// folded as the folder folds one whose records have just been written. With
// WR_FOLD_BENCH_EVICT set, history.bolt's pages are first dropped from the
// page cache, as a host short of memory does. It is skipped unless
// WR_FOLD_BENCH_DIR is set.
func TestStoreFoldBigHistoryThroughput(t *testing.T) {
	dir := os.Getenv("WR_FOLD_BENCH_DIR")
	if dir == "" {
		t.Skip("WR_FOLD_BENCH_DIR not set")
	}

	lookups, completes := envInt(t, "WR_FOLD_BENCH_LOOKUPS", 19600), envInt(t, "WR_FOLD_BENCH_COMPLETES", 4900)

	if n := envInt(t, "WR_FOLD_BENCH_TXBYTES", 0); n > 0 {
		logFoldTxBytes.Store(int64(n))

		defer logFoldTxBytes.Store(0)
	}

	if os.Getenv("WR_FOLD_BENCH_EVICT") != "" {
		evictFile(t, filepath.Join(dir, historyBoltFile))
	}

	h, err := openHistoryBolt(dir, new(codec.BincHandle))
	if err != nil {
		t.Fatal(err)
	}

	defer h.close()

	if os.Getenv("WR_FOLD_BENCH_BUCKETS") != "" {
		logHistoryBuckets(t, h)
	}

	stats := newReqStatCache()
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1)) //nolint:gosec // test keys

	for round := range envInt(t, "WR_FOLD_BENCH_ROUNDS", 1) {
		foldBigHistoryRound(t, h, round, bigHistoryFolds(rng, lookups, completes), stats)
	}
}

// envInt returns the int env var name holds, or def if it is not set.
func envInt(t *testing.T, name string, def int) int {
	t.Helper()

	v := os.Getenv(name)
	if v == "" {
		return def
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatal(err)
	}

	return n
}

// evictFile drops path's pages from the page cache.
func evictFile(t *testing.T, path string) {
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

// logHistoryBuckets logs the key count and pages of each of h's buckets.
func logHistoryBuckets(t *testing.T, h *historyBolt) {
	t.Helper()

	err := h.bolt.View(func(tx *bolt.Tx) error {
		for _, name := range historyBoltBuckets {
			st := tx.Bucket(name).Stats()
			t.Logf("bucket %s: keys %d depth %d branch pages %d leaf pages %d (%d overflow) leaf in use %dMB",
				name, st.KeyN, st.Depth, st.BranchPageN, st.LeafPageN, st.LeafOverflowN, st.LeafInuse>>20)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// foldBigHistoryRound folds into h, as round round, logging how fast,
// with the page faults and blocks written meanwhile and bbolt's stats.
func foldBigHistoryRound(t *testing.T, h *historyBolt, round int, folds []*historyFold, stats *reqStatCache) {
	t.Helper()

	var (
		writer        writerStats
		before, after syscall.Rusage
		items         int
	)

	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
		t.Fatal(err)
	}

	statsBefore := h.bolt.Stats().TxStats
	began := time.Now()

	for _, f := range folds {
		items += f.items()

		if err := errors.Join(h.mergeSomeReverse(foldTxMaxBytes()), h.fold(f, stats, &writer,
			foldTxMaxBytes())); err != nil {
			t.Fatal(err)
		}
	}

	took := time.Since(began)

	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &after); err != nil {
		t.Fatal(err)
	}

	statsAfter, st := h.bolt.Stats().TxStats, h.bolt.Stats()

	t.Logf("round %d: folded %d items in %d folds, %d transactions, in %s: %.0f items/s, %.0f items/min; "+
		"major faults %d, minor faults %d, blocks out %d", round, items, len(folds), writer.txs.Load(), took,
		float64(items)/took.Seconds(), float64(items)/took.Minutes(), after.Majflt-before.Majflt,
		after.Minflt-before.Minflt, after.Oublock-before.Oublock)
	t.Logf("round %d: freelist in use %dKB, free pages %d; spill %s, write %s (%d writes), pages allocated %dMB, "+
		"nodes %d, splits %d", round, st.FreelistInuse>>10, st.FreePageN,
		statsAfter.GetSpillTime()-statsBefore.GetSpillTime(), statsAfter.GetWriteTime()-statsBefore.GetWriteTime(),
		statsAfter.GetWrite()-statsBefore.GetWrite(), (statsAfter.GetPageAlloc()-statsBefore.GetPageAlloc())>>20,
		statsAfter.GetNodeCount()-statsBefore.GetNodeCount(), statsAfter.GetSplit()-statsBefore.GetSplit())
}

// bigHistoryFolds returns the folds of lookups rglookups and completes
// completions of new random keys, interleaved as adds and archives are, in
// chunks of foldChunkRecords records, each rglookup under one of 30 rep groups
// and each completion of a job with a command like the soak's.
func bigHistoryFolds(rng *rand.Rand, lookups, completes int) []*historyFold {
	var (
		folds []*historyFold
		f     = &historyFold{}
		seq   = uint64(1 << 40)
	)

	total := lookups + completes
	end := time.Now()

	for i := range total {
		seq++

		key := logKey(md5.Sum([]byte(strconv.FormatUint(rng.Uint64(), 16)))) //nolint:gosec // a test key

		if i%(total/max(completes, 1)+1) == 0 && len(f.complete) < completes {
			job := testDBJob(fmt.Sprintf("prodsim.sh portal %d %s", rng.Uint64(), key), "prodsim.portal.burst")
			job.StartTime, job.EndTime, job.PeakRAM = end.Add(-time.Minute), end, 100
			f.complete = append(f.complete, historyFolded{key: key, seq: seq, job: job})
		} else {
			f.lookups = append(f.lookups, historyLookup{repGroup: fmt.Sprintf("prodsim.portal.%d", rng.IntN(30)),
				key: key, seq: seq})
		}

		if f.items() >= foldChunkRecords {
			folds = append(folds, f)
			f = &historyFold{}
		}
	}

	if f.items() > 0 {
		folds = append(folds, f)
	}

	return folds
}
