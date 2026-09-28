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
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	bolt "go.etcd.io/bbolt"
)

// managerDBPrefetchStreams is how many sequential readers prefetchFile reads a
// file with. On NFS one stream managed about 380MB/s and 8 about 900MB/s.
const managerDBPrefetchStreams = 8

// prefetchChunkBytes is how much each prefetchFile reader reads at a time, and
// so how soon it notices its context is done.
const prefetchChunkBytes = 1 << 20

//nolint:gochecknoglobals // internal tuning knob and seam; vars only so tests can vary them
var (
	// managerDBPrefetchTimeout bounds how long openBoltPrefetched reads for, so a
	// database too big to read quickly costs at most this much more at startup
	// than it did before prefetching. At NFS's 900MB/s it covers a 50GB file,
	// against production's 11GB.
	managerDBPrefetchTimeout = time.Minute

	// managerDBOpenDropsCache says if opening the bbolt database at path makes
	// the OS drop the file's cached pages.
	managerDBOpenDropsCache = fileOnNFS
)

// boltMeta offsets: a meta page is a 16-byte page header followed by the meta:
// magic, version, pageSize and flags (uint32s), the root bucket (two uint64s),
// then the freelist, pgid and txid (uint64s), little-endian on the platforms wr
// supports.
const (
	boltMetaOffset     = 16
	boltPageSizeOffset = boltMetaOffset + 8
	boltFreelistOffset = boltMetaOffset + 32
	boltTxidOffset     = boltFreelistOffset + 16
	boltMetaEnd        = boltTxidOffset + 8

	// boltMagic is the magic number bbolt writes to both of its meta pages.
	boltMagic = 0xED0CDAED
)

// prefetchResult is what prefetchFile read of a file of size bytes.
type prefetchResult struct {
	read int64
	size int64
	err  error
}

// prefetchFile reads the whole of the file at path, in managerDBPrefetchStreams
// parallel sequential streams, discarding what it reads, so that its pages are
// in the page cache for the reads that follow. It stops early when ctx is done.
func prefetchFile(ctx context.Context, path string) prefetchResult {
	info, err := os.Stat(path) //nolint:gosec // G703: path is the manager's own db file
	if err != nil {
		return prefetchResult{err: err}
	}

	size := info.Size()
	span := (size + managerDBPrefetchStreams - 1) / managerDBPrefetchStreams

	var (
		read atomic.Int64
		wg   sync.WaitGroup
		errs = make([]error, managerDBPrefetchStreams)
	)

	for i := range managerDBPrefetchStreams {
		start := int64(i) * span
		end := min(start+span, size)

		if start >= end {
			break
		}

		wg.Go(func() {
			errs[i] = prefetchRange(ctx, path, start, end, &read)
		})
	}

	wg.Wait()

	return prefetchResult{read: read.Load(), size: size, err: errors.Join(errs...)}
}

// log reports the prefetch of path, which took elapsed, at warn: like the other
// startup phases, it is where an operator sizing the startup window looks.
func (r prefetchResult) log(ctx context.Context, path string, elapsed time.Duration) {
	clog.Warn(ctx, "prefetched database", "path", path, "bytes", r.read, "size", r.size,
		"complete", r.read == r.size && r.err == nil, "elapsed", elapsed.Round(time.Millisecond), "err", r.err)
}

// openBoltPrefetched returns open(), which should open the bbolt database at
// path. If opening it drops the file's cached pages (managerDBOpenDropsCache), or
// the file's freelist was not synced, it also reads the whole file while open()
// runs and waits for that read to finish (for at most managerDBPrefetchTimeout)
// before returning.
//
// The first is for NFS, where the flock bbolt takes at open makes the client
// drop the file's cached pages, so everything after it reads the database
// through the mmap one synchronous page fault (0.3-0.4ms) at a time: recovering
// 121k live jobs took 185s. Parallel sequential reads fetch the whole file at
// about 900MB/s instead, after which recovery decodes 120k jobs in about a
// second. Local filesystems keep their cache across a flock, and reading a cold
// local file whole is no faster than recovery's own faults on it, so a synced
// local file is just opened.
//
// The second is because when the freelist was not synced (the manager did not
// close cleanly; see openManagerBolt), bbolt rebuilds it inside the open by
// walking every page of the database. Cold, that took 5m on NFS and 2m50s on
// local disk for a 7.4GB file, and 11.5s and 22s with this read running beside
// it. That is also why the read starts before open() rather than after it
// returns. What it reads before the flock is dropped with the rest of an NFS
// client's cache, but the flock is one round trip after the open begins, so that
// is a few MB at most.
func openBoltPrefetched(ctx context.Context, path string, open func() (*bolt.DB, error)) (*bolt.DB, error) {
	if !managerDBOpenDropsCache(path) && boltFreelistSynced(path) {
		return open()
	}

	started := time.Now()

	pctx, cancel := context.WithTimeout(ctx, managerDBPrefetchTimeout)
	defer cancel()

	prefetched := make(chan prefetchResult, 1)

	go func() {
		prefetched <- prefetchFile(pctx, path)
	}()

	bdb, err := open()
	if err != nil {
		cancel()
	}

	result := <-prefetched

	if err != nil {
		return nil, err
	}

	result.log(ctx, path, time.Since(started))

	return bdb, nil
}

// boltFreelistSynced reads the newer of the two meta pages of the bbolt file at
// path, and says if it records a freelist page (bbolt stores
// 0xffffffffffffffff there when commits did not sync the freelist). It says true
// for anything it cannot read as a bbolt file, including a missing or new one,
// since then there is no freelist to rebuild.
func boltFreelistSynced(path string) bool {
	metas, ok := readBoltMetas(path)
	if !ok {
		return true
	}

	var newest []byte

	for _, meta := range metas {
		if binary.LittleEndian.Uint32(meta[boltMetaOffset:]) != boltMagic {
			continue
		}

		if newest == nil || boltMetaTxid(meta) > boltMetaTxid(newest) {
			newest = meta
		}
	}

	return newest == nil || binary.LittleEndian.Uint64(newest[boltFreelistOffset:]) != ^uint64(0)
}

// readBoltMetas returns the start of both meta pages of the bbolt file at path,
// up to the end of their txid, and false if it could not read them.
func readBoltMetas(path string) ([2][]byte, bool) {
	var metas [2][]byte

	f, err := os.Open(path) //nolint:gosec // G304: path is the manager's own db file
	if err != nil {
		return metas, false
	}
	defer f.Close()

	metas[0] = make([]byte, boltMetaEnd)
	if _, err = f.ReadAt(metas[0], 0); err != nil {
		return metas, false
	}

	metas[1] = make([]byte, boltMetaEnd)
	pageSize := int64(binary.LittleEndian.Uint32(metas[0][boltPageSizeOffset:]))

	if _, err = f.ReadAt(metas[1], pageSize); err != nil {
		return metas, false
	}

	return metas, true
}

// boltMetaTxid returns the txid of a meta page read by readBoltMetas.
func boltMetaTxid(meta []byte) uint64 {
	return binary.LittleEndian.Uint64(meta[boltTxidOffset:])
}

// prefetchRange reads bytes [start, end) of the file at path, adding what it
// reads to read, until it reaches end or ctx is done.
func prefetchRange(ctx context.Context, path string, start, end int64, read *atomic.Int64) error {
	f, err := os.Open(path) //nolint:gosec // G304: path is the manager's own db file
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, prefetchChunkBytes)

	for off := start; off < end && ctx.Err() == nil; {
		n, errr := f.ReadAt(buf[:min(int64(len(buf)), end-off)], off)
		off += int64(n)
		read.Add(int64(n))

		if errr != nil {
			if errors.Is(errr, io.EOF) {
				return nil
			}

			return errr
		}
	}

	return nil
}
