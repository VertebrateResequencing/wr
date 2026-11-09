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

// A timing probe of the log store's startup on a prod-sized store (the 1d.1
// incidental on the history key index, resolved in 1f.8). It needs a big
// database on NFS, so it only runs when pointed at a WRITABLE COPY of one,
// which its first run converts to a log store in place (spec E1):
//
//   CGO_ENABLED=1 WR_STORE_PROBE_DB=/nfs/.../copy.db go test -tags "netgo reliability_repro" \
//     ./jobqueue -run 'TestLogStoreStartupProbe' -v -timeout 4h
//
// Later runs time the opens alone. Before each open it times loading the
// history key index alone, as recovery does. Each open takes history.bolt's flock, which
// on NFS drops the client's cached pages for it, so each open reads it cold.

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/ugorji/go/codec"
)

const storeProbeDefaultOpens = 3

// TestLogStoreStartupProbe converts WR_STORE_PROBE_DB if it is still a bbolt
// file, then opens the store WR_STORE_PROBE_OPENS times (default 3), logging
// each open's time, the heap it holds once open, and the sizes of the history
// key index and the live jobs.
func TestLogStoreStartupProbe(t *testing.T) {
	if runnermode || servermode {
		return
	}

	path := os.Getenv("WR_STORE_PROBE_DB")
	if path == "" {
		t.Skip("set WR_STORE_PROBE_DB to a writable copy of a big manager database")
	}

	opens := storeProbeDefaultOpens

	if v := os.Getenv("WR_STORE_PROBE_OPENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatal(err)
		}

		opens = n
	}

	ctx := context.Background()
	cfg := storeConfig{dbFile: path, dbBkFile: path + ".probe_bk", deployment: internal.Development,
		kind: storeKindLog}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if !info.IsDir() {
		elapsed, heap, history, live := storeProbeOpen(ctx, t, cfg)
		t.Logf("STORE-PROBE convert=%s heapMB=%d historyKeys=%d live=%d", elapsed.Round(time.Millisecond),
			heap>>20, history, live)
	}

	for i := range opens {
		elapsed, heap, keys := storeProbeIndex(t, path)
		t.Logf("STORE-PROBE index=%d took=%s heapMB=%d historyKeys=%d", i, elapsed.Round(time.Millisecond),
			heap>>20, keys)

		elapsed, heap, history, live := storeProbeOpen(ctx, t, cfg)
		t.Logf("STORE-PROBE open=%d took=%s heapMB=%d historyKeys=%d live=%d", i, elapsed.Round(time.Millisecond),
			heap>>20, history, live)
	}
}

// storeProbeIndex returns how long opening the history.bolt of the store in
// dir and loading its history key index, as recovery does, took, the heap the
// index holds, and its number of keys.
func storeProbeIndex(t *testing.T, dir string) (time.Duration, uint64, int) {
	t.Helper()

	before := storeProbeHeap()
	started := time.Now()

	history, err := openHistoryBolt(dir, new(codec.BincHandle))
	if err != nil {
		t.Fatal(err)
	}

	index := make(map[logKey]uint64)
	if _, _, err = history.load(index); err != nil {
		t.Fatal(err)
	}

	elapsed := time.Since(started)

	if err = history.close(); err != nil {
		t.Fatal(err)
	}

	var heap uint64
	if after := storeProbeHeap(); after > before {
		heap = after - before
	}

	runtime.KeepAlive(index)

	return elapsed, heap, len(index)
}

// storeProbeOpen opens and closes the store of cfg, returning how long the
// open took, the heap in use while it was open beyond that before it, and the
// number of keys in its history key index and of its live jobs.
func storeProbeOpen(ctx context.Context, t *testing.T, cfg storeConfig) (time.Duration, uint64, int, int) {
	t.Helper()

	before := storeProbeHeap()
	started := time.Now()

	s, _, err := openStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	elapsed := time.Since(started)

	ls, ok := s.(*logStore)
	if !ok {
		t.Fatalf("opened a %T, not a log store", s)
	}

	var heap uint64
	if after := storeProbeHeap(); after > before {
		heap = after - before
	}

	ls.mu.RLock()
	history, live := len(ls.state.history), len(ls.state.live)
	ls.mu.RUnlock()

	if err = s.close(ctx); err != nil {
		t.Fatal(err)
	}

	return elapsed, heap, history, live
}

// storeProbeHeap returns the heap in use after a GC.
func storeProbeHeap() uint64 {
	runtime.GC()

	var m runtime.MemStats

	runtime.ReadMemStats(&m)

	return m.HeapInuse
}
