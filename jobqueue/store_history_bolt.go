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

// This file is the log store's Phase 1 history (spec "Folder and history"):
// history.bolt in the store directory, holding today's history buckets plus
// meta and completeSeq, and the in-memory overlay of completions not yet
// folded into it. Every history query answers from both, filtering live keys
// exactly where the bbolt store's does.
//
// A query copies the overlay before it opens its history.bolt read
// transaction. The folder commits a fold to history.bolt before it drops what
// it folded from the overlay, so a completion is in the copy, the
// transaction, or both, and never in neither. Where both hold a key, the
// copy's entry is used, so the query answers for that key as of its copy,
// though a newer completion may since have been folded. Copying the whole
// overlay is O(overlay), which the folder keeps to about a second of
// completions.

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	// historyBoltFile names history.bolt in the store directory.
	historyBoltFile = "history.bolt"

	// foldTxBytes is about how many bytes of pages of its own records (its
	// freelist aside) one transaction of a fold that keeps up may write before
	// it commits them and makes the rest in another (see historyBolt.fold,
	// foldTxSize, foldTxLimit): 2MB, which committed in about 40ms on NFS.
	foldTxBytes = 2 << 20

	// foldLookupBatch is how many rglookups a fold puts between its looks at
	// its transaction's estimated size (see foldTxSize).
	foldLookupBatch = 64

	// completeKeysPut and completeSeqsPut are about how many copies of a
	// completion's key, and of an 8-byte seq or time, putComplete puts: its
	// key in jobscomplete, endTimeToKey and completeSeq, plus its rep group
	// end time's; and the seq and end times beside them.
	completeKeysPut = 4
	completeSeqsPut = 3

	// foldTxMinCompletions is the fewest completions a fold transaction
	// records, if it has that many, however big its estimated size, so that
	// no estimate can make a fold commit for each record.
	foldTxMinCompletions = 16

	// historyPosLen is the length of the folded position meta holds: its wal
	// file and offset.
	historyPosLen = 4 + 8

	// historySeqLen is the length of a seq in meta and completeSeq.
	historySeqLen = 8
)

var (
	// bucketHistoryMeta holds historyMetaFolded and historyMetaMaxSeq.
	bucketHistoryMeta = []byte("meta") //nolint:gochecknoglobals

	// bucketCompleteSeq maps each folded key to its last folded complete
	// record's seq.
	bucketCompleteSeq = []byte("completeSeq") //nolint:gochecknoglobals

	// bucketKeyRGs is repgroupToKey's reverse, key then rep group, so that a
	// fold of a modify finds every rglookup of its old key to delete.
	bucketKeyRGs = []byte("keyRepgroups") //nolint:gochecknoglobals

	// historyMetaFolded is meta's key of the folded position, and
	// historyMetaMaxSeq of the highest folded complete record's seq.
	historyMetaFolded = []byte("folded") //nolint:gochecknoglobals
	historyMetaMaxSeq = []byte("maxSeq") //nolint:gochecknoglobals

	// historyBoltBuckets are the buckets history.bolt has.
	historyBoltBuckets = [][]byte{ //nolint:gochecknoglobals
		bucketJobsComplete, bucketRTK, bucketRGs, bucketDTK, bucketRDTK, bucketDepGroups,
		bucketEndTimeToKey, bucketRGEndTime, bucketJobRAM, bucketJobDisk, bucketJobSecs,
		bucketHistoryMeta, bucketCompleteSeq, bucketKeyRGs,
	}

	// logFoldTxBytes, if above 0, replaces foldTxBytes.
	logFoldTxBytes atomic.Int64 //nolint:gochecknoglobals // a test seam
)

// foldApplyBatch is how many of a fold's records foldHistory drops from the
// in-memory state per hold of the store's lock, so that a fold of one large
// block, which no chunk splits (see foldNow), never holds it for long.
const foldApplyBatch = 1024

// historyPos is a position in the log: a wal file and an offset in it. The
// folded position is the end of the log the folder has folded.
type historyPos struct {
	file uint32
	off  uint64
}

// historyFoldedPos returns the folded position meta holds.
func historyFoldedPos(meta *bolt.Bucket) historyPos {
	v := meta.Get(historyMetaFolded)
	if len(v) != historyPosLen {
		return historyPos{}
	}

	return historyPos{file: binary.BigEndian.Uint32(v), off: binary.BigEndian.Uint64(v[4:])}
}

// covers reports whether ref's record is before p, so folded if p is the
// folded position.
func (p historyPos) covers(ref wal.Ref) bool {
	return ref.File < p.file || (ref.File == p.file && ref.Off < p.off)
}

// after reports whether p is later in the log than q.
func (p historyPos) after(q historyPos) bool {
	return p.file > q.file || (p.file == q.file && p.off > q.off)
}

// historyFolded is one complete record a fold commits: its key, its seq and
// the job of the history entry it makes, which nothing else holds.
type historyFolded struct {
	key logKey
	seq uint64
	job *Job
}

// historyLookup is one rglookup record a fold commits, or, if dropped, one
// modify record, which drops every rglookup of its old key, key. seq is the
// record's.
type historyLookup struct {
	repGroup string
	key      logKey
	seq      uint64
	dropped  bool
}

// historyLookupChanges is the net effect on history.bolt of a fold's rglookup
// and modify records (see netHistoryLookups): the keys whose rglookups to
// drop, before the puts; the rglookups to put, in repgroupToKey's key order;
// and the rep groups to note as ever added.
type historyLookupChanges struct {
	drops     []logKey
	puts      []historyLookupPut
	repGroups [][]byte

	// complete is the fold's complete records, in key order, which its
	// transactions record after its rglookups (see historyBolt.fold).
	complete []historyFolded
}

// historyLookupPut is one rglookup's keys in repgroupToKey and keyRepgroups.
type historyLookupPut struct {
	lookup, reverse []byte
}

// newHistoryLookupChanges returns the net effect of lookups.
func newHistoryLookupChanges(lookups []historyLookup) historyLookupChanges {
	puts, drops := netHistoryLookups(lookups)

	c := historyLookupChanges{drops: drops, puts: make([]historyLookupPut, len(puts)),
		repGroups: lookupRepGroups(lookups)}

	for i, l := range puts {
		key := l.key.String()
		c.puts[i] = historyLookupPut{
			lookup:  []byte(l.repGroup + dbDelimiter + key),
			reverse: []byte(key + dbDelimiter + l.repGroup),
		}
	}

	slices.SortFunc(c.puts, func(a, b historyLookupPut) int { return bytes.Compare(a.lookup, b.lookup) })

	return c
}

// netHistoryLookups returns what lookups, in order, leave: the rglookups put
// and not dropped after, and the keys dropped, whose rglookups history.bolt
// already holds go, in key order.
func netHistoryLookups(lookups []historyLookup) ([]historyLookup, []logKey) {
	put := make(map[logKey]map[string]bool)
	dropped := make(map[logKey]bool)

	for _, l := range lookups {
		if l.dropped {
			delete(put, l.key)
			dropped[l.key] = true

			continue
		}

		if put[l.key] == nil {
			put[l.key] = make(map[string]bool)
		}

		put[l.key][l.repGroup] = true
	}

	var puts []historyLookup

	for k, rgs := range put {
		for rg := range rgs {
			puts = append(puts, historyLookup{repGroup: rg, key: k})
		}
	}

	return puts, slices.SortedFunc(maps.Keys(dropped), func(a, b logKey) int { return bytes.Compare(a[:], b[:]) })
}

// lookupRepGroups returns the rep group of each of lookups' rglookups, once
// each, whether or not a later one dropped it.
func lookupRepGroups(lookups []historyLookup) [][]byte {
	seen := make(map[string]bool)

	var repGroups [][]byte

	for _, l := range lookups {
		if !l.dropped && !seen[l.repGroup] {
			seen[l.repGroup] = true
			repGroups = append(repGroups, []byte(l.repGroup))
		}
	}

	return repGroups
}

// historyFold is what one fold commits to history.bolt: complete records,
// each with the job of its history entry, rglookup and modify records in log
// order, the learned-requirement samples of exitstat records, and the folded
// position they bring history.bolt to.
type historyFold struct {
	complete []historyFolded
	lookups  []historyLookup
	samples  []jobExitData
	to       historyPos
}

// items returns how many records f folds.
func (f *historyFold) items() int {
	return len(f.complete) + len(f.lookups) + len(f.samples)
}

// foldHistory commits f to history.bolt, then drops from the overlay what it
// folded and moves the state's folded position to f's, so that a record f
// folded before its done func applied it is applied as folded too. The folder
// calls it for each fold, contiguously from the folded position.
//
// It changes the in-memory state in batches, each under the store's lock,
// in an order that keeps every query's answer whole between them, as each
// batch drops only what history.bolt now holds: first the folded position
// and the history index, together, as a done func f overtook then applies to
// the live plane only, leaving the index to this fold; then the keys a modify
// dropped the folded rglookups of, which history.bolt no longer holds, before
// any rglookup they would hide once their own is dropped; then the overlay's
// completions and rglookups. Each of history.bolt's transactions of f writes
// about foldTxMaxBytes (see historyBolt.fold).
func (s *logStore) foldHistory(f *historyFold) error {
	return s.foldHistoryUpTo(f, foldTxMaxBytes())
}

// foldHistoryUpTo is foldHistory with each of history.bolt's transactions of
// f writing about maxBytes.
func (s *logStore) foldHistoryUpTo(f *historyFold, maxBytes int) error {
	if err := s.history.fold(f, s.reqStats, &s.foldStats, maxBytes); err != nil {
		return err
	}

	callHook(&logFoldCommittedHook)

	s.moveFolded(f)
	s.dropFolded(f)

	return nil
}

// moveFolded moves the state's folded position to f's, and raises the history
// index to f's complete records, for foldHistory.
func (s *logStore) moveFolded(f *historyFold) {
	s.mu.Lock()

	if f.to.after(s.state.folded) {
		s.state.folded = f.to
	}

	for i := range f.complete {
		c := &f.complete[i]
		s.state.history[c.key] = max(s.state.history[c.key], c.seq)
	}

	s.mu.Unlock()
	callHook(&logFoldBatchHook)
}

// dropFolded drops from the in-memory state what f folded, for foldHistory, in
// batches: the keys a modify dropped first, then completions and rglookups.
func (s *logStore) dropFolded(f *historyFold) {
	s.inFoldBatches(len(f.lookups), func(i int) {
		if l := f.lookups[i]; l.dropped {
			s.state.relook(l.key, l.seq)
		}
	})

	s.inFoldBatches(len(f.complete), func(i int) {
		c := &f.complete[i]
		s.state.unfold(c.key, c.seq)
		s.state.unfoldEnd(c.job.RepGroup, c.job.EndTime)
	})

	s.inFoldBatches(len(f.lookups), func(i int) {
		if l := f.lookups[i]; !l.dropped {
			s.state.unlookUp(l.repGroup, l.key, l.seq)
			s.state.unlookUpRG(l.repGroup)
		}
	})
}

// historyBolt is history.bolt.
type historyBolt struct {
	bolt *bolt.DB
	ch   codec.Handle

	// writeScale is the bytes of pages the last fold transaction wrote, but
	// for its freelist, per byte its foldTxSize estimated, 0 before any; the
	// folder, one fold at a time, alone reads and sets it.
	writeScale float64
}

// openHistoryBolt opens, creating if need be, history.bolt in dir, with every
// bucket it has. The caller holds the store's lock.
//
// It maps the file with managerInitialMmapSize's headroom, as openManagerBolt
// does the bbolt store, since a backup copies history.bolt whole under one
// read transaction (spec E4, E5): a fold commit growing the file past its
// mapping would remap, which waits for that transaction while blocking every
// new history read until the copy ends. With the headroom that happens only
// once history.bolt has doubled in size since the store opened. If the
// process may not map that much address space it falls back to bbolt's
// default mapping.
func openHistoryBolt(dir string, ch codec.Handle) (*historyBolt, error) {
	bdb, err := openBoltWithHeadroom(filepath.Join(dir, historyBoltFile),
		bolt.Options{Timeout: managerDBOpenTimeout, FreelistType: bolt.FreelistMapType})
	if err != nil {
		return nil, err
	}

	err = bdb.Update(func(tx *bolt.Tx) error {
		for _, name := range historyBoltBuckets {
			if _, errc := tx.CreateBucketIfNotExists(name); errc != nil {
				return errc
			}
		}

		return nil
	})
	if err != nil {
		return nil, errors.Join(err, bdb.Close())
	}

	return &historyBolt{bolt: bdb, ch: ch}, nil
}

// load returns the folded position and the highest folded complete record's
// seq, and adds each folded key's seq to index.
func (h *historyBolt) load(index map[logKey]uint64) (historyPos, uint64, error) {
	var (
		pos    historyPos
		maxSeq uint64
	)

	err := h.bolt.View(func(tx *bolt.Tx) error {
		pos = historyFoldedPos(tx.Bucket(bucketHistoryMeta))
		maxSeq = historyMaxSeq(tx)

		return loadCompleteSeqs(tx, index)
	})

	return pos, maxSeq, err
}

// historyMaxSeq returns meta's highest folded complete record's seq.
func historyMaxSeq(tx *bolt.Tx) uint64 {
	v := tx.Bucket(bucketHistoryMeta).Get(historyMetaMaxSeq)
	if len(v) != historySeqLen {
		return 0
	}

	return binary.BigEndian.Uint64(v)
}

// loadCompleteSeqs adds each key in completeSeq to index with its seq.
func loadCompleteSeqs(tx *bolt.Tx, index map[logKey]uint64) error {
	return tx.Bucket(bucketCompleteSeq).ForEach(func(key, seq []byte) error {
		k, err := parseLogKey(string(key))
		if err == nil && len(seq) != historySeqLen {
			err = fmt.Errorf("%w: %s has a %d byte seq", errLogRecordMalformed, key, len(seq))
		}

		if err != nil {
			return fmt.Errorf("%w: %s %s: %w", errStoreCorrupt, historyBoltFile, bucketCompleteSeq, err)
		}

		index[k] = binary.BigEndian.Uint64(seq)

		return nil
	})
}

// fold commits f to history.bolt: each rglookup into repgroupToKey, repgroups
// and keyRepgroups, and each modify's dropping of its old key's
// rglookups, in log order; each complete record whose seq is above its key's
// completeSeq as today's archive records a completion (jobscomplete,
// endTimeToKey, repgroupEndTime); the learned-requirement samples of each
// complete record it records (as putJobStats) and exitstat record (as
// updateFailStat), through stats, which invalidates a ReqGroup given a new
// sample once the transaction commits; and the folded position and highest
// folded seq into meta. A complete record at or below its key's completeSeq is already folded,
// so folding it again changes nothing and adds no sample, and samples are kept
// per distinct value, so an exitstat sample folded again adds none. Each
// transaction is observed in writer, with the records it folded, once it
// commits, unless it folds no record and only moves the folded position, as
// the first fold after an open does, so an idle store logs no "db writer" line
// (spec A3); a failed one is not, as the fold that retries it is.
//
// It commits f in one transaction unless its rglookups, then its completions,
// would write more than maxBytes of history.bolt's pages of their own, as
// foldTxSize estimates, or more than its freelist if that is bigger (see
// putSome): then it commits the drops and what it has put and recorded so far
// as soon as the estimate reaches it, and makes the rest in further
// transactions, the last of which commits the rest of f and moves the folded
// position. The folder passes foldTxLimit of its lag as maxBytes, so a fold
// that falls behind makes bigger transactions, cheaper per record. Each rglookup and completion puts keys in
// random places in history.bolt's trees: in a 1.3-2.3GB history.bolt, a chunk
// of 2,000-4,000 rglookups (an add of many jobs is one block, which a chunk
// never splits) wrote 7-15MB in 1,500-2,600 runs of pages, and a chunk of
// several hundred completions took 1-4s, and on NFS such a commit held the
// log's syncs, and so reservations, 60-350ms behind it
// (.docs/bugfixes/261111-reserve-tail-nfs-4766eb70.md). A small
// history.bolt's folds write far fewer pages, so are seldom split. Each
// transaction before the last is idempotent (completeSeq makes a completion
// recorded again a no-op) and only adds to or takes from history.bolt what
// the overlay still holds of the same records, so a crash before the last
// leaves every history query's answer as it was, but for learned-requirement
// samples, which go with their completions, and the fold made again after it
// makes the rest; recovery clears a live job's std for a complete record
// history.bolt already holds past the folded position as for one before it
// (logState.applyComplete).
func (h *historyBolt) fold(f *historyFold, stats *reqStatCache, writer *writerStats, maxBytes int) error {
	c := newHistoryLookupChanges(f.lookups)
	c.complete = slices.Clone(f.complete)
	slices.SortStableFunc(c.complete, func(a, b historyFolded) int { return bytes.Compare(a.key[:], b.key[:]) })

	items := f.items()

	for {
		asked := time.Now()

		put, recorded, begun, err := h.foldSomeTx(f, c, maxBytes, stats)
		if err != nil {
			return err
		}

		if put == len(c.puts) && recorded == len(c.complete) {
			if items > 0 {
				writer.observe(items, asked, begun)
			}

			return nil
		}

		writer.observe(put+recorded, asked, begun)

		c.drops, c.puts, c.complete, items = nil, c.puts[put:], c.complete[recorded:], items-put-recorded
	}
}

// foldSomeTx makes one of fold's transactions: c's drops, its puts and then
// its completions, until the transaction's estimated size reaches maxBytes
// (see putSome), then, if that was all of them, the
// rest of f. It returns how many puts and completions it made and when its
// body began.
func (h *historyBolt) foldSomeTx(f *historyFold, c historyLookupChanges, maxBytes int,
	stats *reqStatCache,
) (int, int, time.Time, error) {
	var (
		begun         time.Time
		put, recorded int
	)

	err := h.bolt.Update(func(tx *bolt.Tx) error {
		begun = time.Now()

		var err error

		put, recorded, err = h.putSome(tx, c, maxBytes, stats)
		switch {
		case err != nil:
			return err
		case put < len(c.puts) || recorded < len(c.complete):
			return callTxHook()
		default:
			return h.foldTx(tx, f, c.repGroups, stats)
		}
	})

	return put, recorded, begun, err
}

// putSome makes, in tx, c's drops and its puts (see putHistoryLookups) and,
// if it made all of them, its completions (see recordCompletions), until tx's
// foldTxSize reaches maxBytes, or the bytes every commit writes for
// history.bolt's freelist if more, returning how many puts and completions it
// made. Once tx commits, the bytes it wrote but for its freelist set the next
// one's scale. A transaction writing less of its own than its freelist would
// cost more in the freelist than it saves the log's syncs.
func (h *historyBolt) putSome(tx *bolt.Tx, c historyLookupChanges, maxBytes int,
	stats *reqStatCache,
) (int, int, error) {
	pageSize := tx.DB().Info().PageSize
	size := &foldTxSize{tx: tx, pageSize: pageSize, max: max(maxBytes, h.freelistWrite(pageSize)),
		scale: h.writeScale}

	tx.OnCommit(func() { h.noteWrite(tx, size) })

	put, err := putHistoryLookups(tx, c, size)
	if err != nil || put < len(c.puts) {
		return put, 0, err
	}

	recorded, err := h.recordCompletions(tx, c.complete, size, stats)

	return put, recorded, err
}

// foldTxMaxBytes returns foldTxBytes, or logFoldTxBytes if a test set it.
func foldTxMaxBytes() int {
	if n := logFoldTxBytes.Load(); n > 0 {
		return int(n)
	}

	return foldTxBytes
}

// callTxHook returns what logFoldTxHook returns, or nil if it is not set.
func callTxHook() error {
	if hook := logFoldTxHook.Load(); hook != nil {
		return (*hook)()
	}

	return nil
}

// putHistoryLookups deletes every rglookup of each of c's drops, then puts c's
// rglookups in key order, since bbolt inserts into a node it has not yet split
// by moving every later entry: so putting in random order costs time quadratic
// in the number put. Once size is full, it stops, at a multiple of
// foldLookupBatch, and returns how many it put.
func putHistoryLookups(tx *bolt.Tx, c historyLookupChanges, size *foldTxSize) (int, error) {
	for _, k := range c.drops {
		if err := dropHistoryLookups(tx, k); err != nil {
			return 0, err
		}
	}

	keys := make([][]byte, 0, foldLookupBatch)

	for i := 0; i < len(c.puts); i += foldLookupBatch {
		if i > 0 && size.full() {
			return i, nil
		}

		written, err := putHistoryLookupBatch(tx, c.puts[i:min(i+foldLookupBatch, len(c.puts))], keys)
		if err != nil {
			return i, err
		}

		size.written += written
	}

	return len(c.puts), nil
}

// putHistoryLookupBatch puts batch's rglookups in tx, in repgroupToKey in
// their order and in keyRepgroups in key order, using keys' space, and
// returns the bytes of keys it put.
func putHistoryLookupBatch(tx *bolt.Tx, batch []historyLookupPut, keys [][]byte) (int, error) {
	lookups := tx.Bucket(bucketRTK)
	keys = keys[:0]
	written := 0

	for _, p := range batch {
		if err := lookups.Put(p.lookup, nil); err != nil {
			return written, err
		}

		keys = append(keys, p.reverse)
		written += len(p.lookup) + len(p.reverse)
	}

	return written, putSortedKeys(tx.Bucket(bucketKeyRGs), keys)
}

// foldTxSize estimates how many bytes of its own one of a fold's
// transactions will write when it commits, its freelist aside: a page for
// each node it has changed (bbolt's node count), plus the bytes of the keys
// and values it has put, which a page count misses, scaled by how many bytes
// fold transactions wrote, but for their freelists, per byte so estimated
// (see noteWrite). The scale covers what neither term sees: a node holding
// values larger than a page is written as a run of pages, existing values
// with it, and a node grown past a page splits into several.
type foldTxSize struct {
	tx       *bolt.Tx
	pageSize int
	max      int
	written  int
	scale    float64
}

// estimate returns the transaction's unscaled estimated size.
func (z *foldTxSize) estimate() int {
	stats := z.tx.Stats()

	return int(stats.GetNodeCount())*z.pageSize + z.written
}

// full reports whether the transaction's estimated size, scaled, has reached
// its max.
func (z *foldTxSize) full() bool {
	scale := z.scale
	if scale == 0 {
		scale = 1
	}

	return float64(z.estimate())*scale >= float64(z.max)
}

// noteWrite sets writeScale to the bytes of pages tx, a fold transaction
// estimated by size, wrote as it committed, but for its freelist (see
// freelistWrite), per byte estimated. The freelist is left out because its
// size has nothing to do with the records a transaction holds: counted, a
// large freelist would scale every estimate up until each transaction folded
// a few records.
func (h *historyBolt) noteWrite(tx *bolt.Tx, size *foldTxSize) {
	estimate := size.estimate()
	stats := tx.Stats()
	written := stats.GetPageAlloc() - int64(h.freelistWrite(size.pageSize))

	if estimate <= 0 || written <= 0 {
		return
	}

	h.writeScale = float64(written) / float64(estimate)
}

// foldTx makes the rest of fold's changes of f in tx, once its rglookups are
// put and its completions recorded: the rep groups ever added, repGroups; its
// samples; and the folded position, with the highest seq of f's complete
// records.
func (h *historyBolt) foldTx(tx *bolt.Tx, f *historyFold, repGroups [][]byte, stats *reqStatCache) error {
	if err := putSortedKeys(tx.Bucket(bucketRGs), repGroups); err != nil {
		return err
	}

	err := putFailStats(tx, f.samples, stats)
	if err == nil {
		err = callTxHook()
	}

	if err != nil {
		return err
	}

	maxSeq := historyMaxSeq(tx)
	for i := range f.complete {
		maxSeq = max(maxSeq, f.complete[i].seq)
	}

	return putHistoryMeta(tx, f.to, maxSeq)
}

// putFailStats records the learned-requirement sample of each of samples, an
// exitstat record's, as the bbolt store's exits do (updateFailStat).
func putFailStats(tx *bolt.Tx, samples []jobExitData, stats *reqStatCache) error {
	for i := range samples {
		if err := samples[i].updateFailStat(tx, stats); err != nil {
			return err
		}
	}

	return nil
}

// putHistoryMeta records the folded position, unless meta holds a later one,
// which a fold never moves it back from (only rewind does), and the highest
// folded seq in meta.
func putHistoryMeta(tx *bolt.Tx, to historyPos, maxSeq uint64) error {
	meta := tx.Bucket(bucketHistoryMeta)

	if !historyFoldedPos(meta).after(to) {
		pos := binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint32(nil, to.file), to.off)
		if err := meta.Put(historyMetaFolded, pos); err != nil {
			return err
		}
	}

	return meta.Put(historyMetaMaxSeq, binary.BigEndian.AppendUint64(nil, maxSeq))
}

// rewind durably moves the folded position back to to, which recovery does
// when damage has left the log ending before it (see logStore.rewindFoldedTo).
func (h *historyBolt) rewind(to historyPos) error {
	return h.bolt.Update(func(tx *bolt.Tx) error {
		pos := binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint32(nil, to.file), to.off)

		return tx.Bucket(bucketHistoryMeta).Put(historyMetaFolded, pos)
	})
}

// recordCompletions records each of complete's jobs, in their order (key
// order, for the reason putHistoryLookups puts in key order; the completeSeq
// rule makes the order of one key's records not matter), as putComplete, and
// the learned-requirement samples of each it records (as putJobStats), until
// size is full, once it has taken foldTxMinCompletions, and returns how many
// it took. Recording one is idempotent, by
// completeSeq, so a fold made again records only those its earlier attempt
// did not commit.
func (h *historyBolt) recordCompletions(tx *bolt.Tx, complete []historyFolded, size *foldTxSize,
	stats *reqStatCache,
) (int, error) {
	for i := range complete {
		if i >= foldTxMinCompletions && size.full() {
			return i, nil
		}

		written, err := h.recordCompletion(tx, &complete[i], stats)
		if err != nil {
			return i, err
		}

		size.written += written
	}

	return len(complete), nil
}

// freelistWrite returns about how many bytes of pages each commit of
// history.bolt writes for its freelist, as of the last commit: bbolt writes
// the whole freelist, in pageSize pages, unless NoFreelistSync is set.
func (h *historyBolt) freelistWrite(pageSize int) int {
	if h.bolt.NoFreelistSync {
		return 0
	}

	stats := h.bolt.Stats()

	return (stats.FreelistInuse/pageSize + 1) * pageSize
}

// recordCompletion records c's job as putComplete does, and, if it was not
// already folded, its learned-requirement samples through stats, returning
// about how many bytes of keys and values it put.
func (h *historyBolt) recordCompletion(tx *bolt.Tx, c *historyFolded, stats *reqStatCache) (int, error) {
	written, err := h.putComplete(tx, c)
	if err == nil && written > 0 {
		err = putJobStats(tx, stats, c.job)
	}

	return written, err
}

// putComplete records c's job as its key's history entry and returns about
// how many bytes of keys and values that put: its encoded job under its key in
// jobscomplete, its key in endTimeToKey, its rep group's end time and its seq
// in completeSeq. It returns 0 if completeSeq shows it is already folded.
func (h *historyBolt) putComplete(tx *bolt.Tx, c *historyFolded) (int, error) {
	key := []byte(c.key.String())
	seqs := tx.Bucket(bucketCompleteSeq)

	if v := seqs.Get(key); len(v) == historySeqLen && binary.BigEndian.Uint64(v) >= c.seq {
		return 0, nil
	}

	var encoded []byte
	if err := codec.NewEncoderBytes(&encoded, h.ch).Encode(c.job); err != nil {
		return 0, err
	}

	if err := updateEndTimeIndex(tx, key, c.job, h.ch); err != nil {
		return 0, err
	}

	if err := tx.Bucket(bucketJobsComplete).Put(key, encoded); err != nil {
		return 0, err
	}

	if err := updateRGEndTime(tx.Bucket(bucketRGEndTime), c.job); err != nil {
		return 0, err
	}

	written := len(encoded) + completeKeysPut*len(key) + completeSeqsPut*historySeqLen

	return written, seqs.Put(key, binary.BigEndian.AppendUint64(nil, c.seq))
}

// close closes history.bolt.
func (h *historyBolt) close() error {
	return h.bolt.Close()
}

// backup writes history.bolt to tw as one tar member, under a read
// transaction that also reads its folded position, which it returns.
func (h *historyBolt) backup(tw *tar.Writer) (historyPos, error) {
	var folded historyPos

	err := h.bolt.View(func(tx *bolt.Tx) error {
		folded = historyFoldedPos(tx.Bucket(bucketHistoryMeta))

		if err := tw.WriteHeader(backupTarHeader(historyBoltFile, tx.Size())); err != nil {
			return err
		}

		_, err := tx.WriteTo(tw)

		return err
	})

	return folded, err
}

// copyTo writes history.bolt to w, under a read transaction that also reads
// its folded position, which it returns with the bytes written.
func (h *historyBolt) copyTo(w io.Writer) (historyPos, int64, error) {
	var (
		folded historyPos
		size   int64
	)

	err := h.bolt.View(func(tx *bolt.Tx) error {
		folded = historyFoldedPos(tx.Bucket(bucketHistoryMeta))

		var err error

		size, err = tx.WriteTo(w)

		return err
	})

	return folded, size, err
}

// foldedPos returns the folded position history.bolt holds.
func (h *historyBolt) foldedPos() (historyPos, error) {
	var folded historyPos

	err := h.bolt.View(func(tx *bolt.Tx) error {
		folded = historyFoldedPos(tx.Bucket(bucketHistoryMeta))

		return nil
	})

	return folded, err
}

// historyRecent is an archived job retrieveCompleteJobsRecent returns, with
// its endTimeToKey key, which orders them.
type historyRecent struct {
	order []byte
	job   *Job
}

// retrieveCompleteJobsRecent returns the archived jobs that are not live and
// ended at or after cutoff, in ascending end time order (then key order), as
// the bbolt store's endTimeToKey walk gives them.
func (s *logStore) retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error) {
	var recent []historyRecent

	err := s.viewHistory("", func(r *historyRead) error {
		var err error

		if recent, err = r.foldedRecent(cutoff); err != nil {
			return err
		}

		more, err := r.overlayRecent(cutoff)
		recent = append(recent, more...)

		return err
	})

	slices.SortFunc(recent, func(a, b historyRecent) int { return bytes.Compare(a.order, b.order) })

	jobs := make([]*Job, len(recent))
	for i := range recent {
		jobs[i] = recent[i].job
	}

	return jobs, err
}

// historyRead is one history query's view: a copy of the overlay, of the
// overlay's rglookups of repGroup, and of the keys whose rglookups in
// history.bolt a modify dropped (unlooked), taken before tx, its history.bolt
// read transaction.
type historyRead struct {
	s        *logStore
	overlay  map[logKey]logHistory
	repGroup string
	lookups  map[logKey]uint64
	unlooked map[logKey]uint64
	tx       *bolt.Tx

	// records holds the records of overlay entries the read has read, by Ref,
	// and unread the index of each of the others by its frame's Ref (see
	// overlayRecord).
	records map[wal.Ref][]byte
	unread  map[wal.Ref]map[uint32]bool
}

// view calls fn with r in a history.bolt read transaction.
func (r *historyRead) view(fn func(r *historyRead) error) error {
	return r.s.history.bolt.View(func(tx *bolt.Tx) error {
		r.tx = tx

		return fn(r)
	})
}

// repGroupKeys returns, in key order as bbolt's repgroupToKey walk gives
// them, the keys looked up under r's rep group, skipping those history.bolt
// holds of a key a modify not yet folded dropped.
func (r *historyRead) repGroupKeys() ([]logKey, error) {
	keys := make(map[logKey]struct{}, len(r.lookups))
	for k := range r.lookups {
		keys[k] = struct{}{}
	}

	prefix := []byte(r.repGroup + dbDelimiter)
	cursor := r.tx.Bucket(bucketRTK).Cursor()

	for k, _ := cursor.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
		key, err := parseLogKey(string(k[len(prefix):]))
		if err != nil {
			return nil, err
		}

		if _, dropped := r.unlooked[key]; !dropped {
			keys[key] = struct{}{}
		}
	}

	return slices.SortedFunc(maps.Keys(keys), func(a, b logKey) int { return bytes.Compare(a[:], b[:]) }), nil
}

// folded returns k's complete record in history.bolt, or nil.
func (r *historyRead) folded(k logKey) []byte {
	return r.tx.Bucket(bucketJobsComplete).Get([]byte(k.String()))
}

// job returns k's history entry's job, from the overlay, else history.bolt,
// or nil if it has none, counting each it decodes in s.archivedDecodes.
func (r *historyRead) job(k logKey) (*Job, error) {
	if h, ok := r.overlay[k]; ok {
		r.s.archivedDecodes.Add(1)

		return r.overlayJob(h)
	}

	encoded := r.folded(k)
	if len(encoded) == 0 {
		return nil, nil //nolint:nilnil // no history entry is not an error
	}

	r.s.archivedDecodes.Add(1)

	return decodeStoredJob(encoded, r.s.ch)
}

// archivedJob is job, but nil for a key that is live (being re-run), as the
// bbolt store's decodeArchivedJob.
func (r *historyRead) archivedJob(k logKey) (*Job, error) {
	if r.s.liveKey(k) {
		return nil, nil //nolint:nilnil // a live key is skipped, not an error
	}

	return r.job(k)
}

// selectOldest offers each archived job of r's rep group that is not live to
// query's selectors, counting them in counted, and returns the oldest-started
// each can use, oldest-started first.
func (r *historyRead) selectOldest(query completeJobsQuery, counted map[string]int) ([]archivedJobCandidate, error) {
	keys, err := r.repGroupKeys()
	if err != nil {
		return nil, err
	}

	scan := &archivedScan{
		query:     query,
		counted:   counted,
		selectors: make(map[string]*oldestArchivedJobs),
		decoder:   codec.NewDecoderBytes(nil, r.s.ch),
	}

	for _, k := range keys {
		if err = r.offerArchived(scan, k); err != nil {
			return nil, err
		}
	}

	return mergeOldestArchivedJobs(scan.selectors), nil
}

// decodeSelected decodes the selected candidates into page.jobs, counting how
// many fell in each group. Each was selected in this read, so has an entry.
func (r *historyRead) decodeSelected(selected []archivedJobCandidate, page *completeJobsPage) error {
	page.jobs = make([]*Job, 0, len(selected))

	for i := range selected {
		k, err := parseLogKey(string(selected[i].key))
		if err != nil {
			return err
		}

		job, err := r.job(k)
		if err != nil {
			return err
		}

		page.jobs = append(page.jobs, job)
		page.fetched[selected[i].group]++
	}

	return nil
}

// offerArchived offers k's history entry to scan, unless k is live or has no
// entry.
func (r *historyRead) offerArchived(scan *archivedScan, k logKey) error {
	if r.s.liveKey(k) {
		return nil
	}

	key := []byte(k.String())

	if _, ok := r.overlay[k]; !ok {
		if encoded := r.folded(k); len(encoded) > 0 {
			return scan.consider(key, encoded)
		}

		return nil
	}

	job, err := r.job(k)
	if err != nil {
		return err
	}

	scan.facets = facetsOf(job)
	scan.offer(key)

	return nil
}

// addStatus adds k's history entry to summary, unless k is live or has no
// entry.
func (r *historyRead) addStatus(summary *RepGroupStatus, k logKey, includeDetails bool,
	decoder *codec.Decoder,
) error {
	if r.s.liveKey(k) {
		return nil
	}

	if _, ok := r.overlay[k]; ok {
		return r.addOverlayStatus(summary, k, includeDetails)
	}

	encoded := r.folded(k)

	switch {
	case len(encoded) == 0:
		return nil
	case !includeDetails:
		summary.AddState(JobStateComplete, 1)

		return nil
	}

	return addFoldedUsage(summary, encoded, decoder)
}

// addFoldedUsage adds the usage of a folded complete record to summary,
// decoding its usage fields alone.
func addFoldedUsage(summary *RepGroupStatus, encoded []byte, decoder *codec.Decoder) error {
	var usage completeJobUsage

	decoder.ResetBytes(encoded)

	if err := decoder.Decode(&usage); err != nil {
		return err
	}

	summary.addCompleteUsage(&usage)

	return nil
}

// addOverlayStatus is addStatus for k's overlay entry.
func (r *historyRead) addOverlayStatus(summary *RepGroupStatus, k logKey, includeDetails bool) error {
	if !includeDetails {
		summary.AddState(JobStateComplete, 1)

		return nil
	}

	job, err := r.job(k)
	if err != nil {
		return err
	}

	summary.AddCompleteJob(job)

	return nil
}

// foldedRecent returns the folded archived jobs that ended at or after cutoff,
// skipping those live and those r's overlay copy has an entry of, which
// overlayRecent gives instead.
func (r *historyRead) foldedRecent(cutoff time.Time) ([]historyRecent, error) {
	var recent []historyRecent

	cursor := r.tx.Bucket(bucketEndTimeToKey).Cursor()

	for ik, _ := cursor.Seek(endTimeSeekKey(cutoff)); ik != nil; ik, _ = cursor.Next() {
		k, err := parseLogKey(string(lookupEntryJobKey(ik)))
		if err != nil {
			return nil, err
		}

		if _, newer := r.overlay[k]; newer {
			continue
		}

		job, err := r.archivedJob(k)
		if err != nil {
			return nil, err
		}

		if job != nil {
			recent = append(recent, historyRecent{order: slices.Clone(ik), job: job})
		}
	}

	return recent, nil
}

// overlayRecent returns the overlay's archived jobs that ended at or after
// cutoff and are not live.
func (r *historyRead) overlayRecent(cutoff time.Time) ([]historyRecent, error) {
	var recent []historyRecent

	seek := endTimeSeekKey(cutoff)

	for k, h := range r.overlay {
		end := endTimeToBytes(h.endTime.UnixNano())
		if bytes.Compare(end, seek) < 0 {
			continue
		}

		job, err := r.archivedJob(k)
		if err != nil {
			return nil, err
		}

		if job != nil {
			recent = append(recent, historyRecent{order: endTimeIndexKey(end, []byte(k.String())), job: job})
		}
	}

	return recent, nil
}

// archivedBytes returns how many bytes spendArchivedBytesByRepGroup charges for
// k.
func (r *historyRead) archivedBytes(k logKey) (int, error) {
	h, ok := r.overlay[k]
	if !ok {
		return len(r.folded(k)), nil
	}

	spec, err := r.overlayRecord(h.spec)
	if err != nil {
		return 0, err
	}

	complete, err := r.overlayRecord(h.complete)

	return len(spec) + len(complete), err
}

// overlayJob returns the job made of overlay entry h's spec and complete
// records.
func (r *historyRead) overlayJob(h logHistory) (*Job, error) {
	spec, err := r.overlayRecord(h.spec)
	if err != nil {
		return nil, err
	}

	complete, err := r.overlayRecord(h.complete)
	if err != nil {
		return nil, err
	}

	specBody, err := keyedBody(spec, h.spec, logRecSpec)
	if err != nil {
		return nil, err
	}

	completeBody, err := keyedBody(complete, h.complete, logRecComplete)
	if err != nil {
		return nil, err
	}

	return r.s.jobOf(specBody, completeBody)
}

// overlayRecord returns the record at ref, the spec or complete record of one
// of r's overlay entries. The first time it needs one of a frame's records, it
// reads the frame once for all the overlay entries' records it holds, since a
// block's records can only be read by decompressing the whole block: reading
// each through its Ref would cost a block per record, quadratic in the jobs
// one add stored in one block when a query reads many of them. It keeps a copy
// of every overlay entry's record in each frame it reads, whether or not the
// query asks for it, for as long as r lives, so a query's memory grows with
// the overlay records sharing frames with those it reads, at most the whole
// overlay.
func (r *historyRead) overlayRecord(ref wal.Ref) ([]byte, error) {
	if rec, ok := r.records[ref]; ok {
		return rec, nil
	}

	if r.records == nil {
		r.indexOverlay()
	}

	frame := wal.Ref{File: ref.File, Off: ref.Off}
	idxs := r.unread[frame]
	delete(r.unread, frame)

	err := r.s.readFrameRecords(frame, idxs, func(got wal.Ref, rec []byte) error {
		r.records[got] = slices.Clone(rec)

		return nil
	})
	if err != nil {
		return nil, err
	}

	rec, ok := r.records[ref]
	if !ok {
		return nil, fmt.Errorf("%w: %+v", wal.ErrBadRef, ref)
	}

	return rec, nil
}

// indexOverlay notes, as unread, the index of the spec and complete records of
// each of r's overlay entries by its frame's Ref.
func (r *historyRead) indexOverlay() {
	r.records = make(map[wal.Ref][]byte)
	r.unread, _ = frameIdxs(func(yield func(wal.Ref) bool) {
		for _, h := range r.overlay {
			if !yield(h.spec) || !yield(h.complete) {
				return
			}
		}
	})
}

// completedElsewhere is the bbolt store's checkIfCompleteUnderRepGroupTx for
// k, whose history entry no overlay rglookup links to repGroup: nil if
// history.bolt's repgroupToKey links it (and no modify not yet folded dropped
// that), or its entry's rep group is repGroup; else that rep group and the
// entry's EndTime.
func (r *historyRead) completedElsewhere(k logKey, repGroup string) (*completedElsewhere, error) {
	if r.completedUnder(k, repGroup) {
		return nil, nil //nolint:nilnil // completed under repGroup
	}

	job, err := r.job(k)
	if err != nil || job == nil || job.RepGroup == repGroup {
		return nil, err
	}

	return &completedElsewhere{repGroup: job.RepGroup, endTime: job.EndTime}, nil
}

// completedUnder reports whether history.bolt's repgroupToKey links k to
// repGroup, unless a modify not yet folded dropped that, or k's overlay entry
// is of repGroup.
func (r *historyRead) completedUnder(k logKey, repGroup string) bool {
	_, dropped := r.unlooked[k]
	if !dropped && r.tx.Bucket(bucketRTK).Get([]byte(repGroup+dbDelimiter+k.String())) != nil {
		return true
	}

	h, ok := r.overlay[k]

	return ok && h.repGroup == repGroup
}

// historyJobs returns the history entry's job of each of keys that has one, by
// key, in one read, so that the overlay entries' frames are each read once (see
// overlayRecord).
func (s *logStore) historyJobs(keys []logKey) (map[logKey]*Job, error) {
	jobs := make(map[logKey]*Job, len(keys))

	err := s.viewHistoryKeys(keys, func(r *historyRead) error {
		for _, k := range keys {
			job, err := r.job(k)
			if err != nil {
				return err
			}

			if job != nil {
				jobs[k] = job
			}
		}

		return nil
	})

	return jobs, err
}

// historyJob returns the keyed job's history entry's job and true, or false if
// it has none.
func (s *logStore) historyJob(k logKey) (*Job, bool, error) {
	jobs, err := s.historyJobs([]logKey{k})
	job := jobs[k]

	return job, job != nil, err
}

// retrieveCompleteJobsByKeys returns the history entry's job of each of keys
// that has one, live or not, in keys' order. A key that is not a job's key, or
// whose entry cannot be decoded, is skipped, as the bbolt store skips it.
func (s *logStore) retrieveCompleteJobsByKeys(keys []string) ([]*Job, error) {
	parsed := make([]logKey, 0, len(keys))

	for _, key := range keys {
		if k, err := parseLogKey(key); err == nil {
			parsed = append(parsed, k)
		}
	}

	var jobs []*Job

	err := s.viewHistoryKeys(parsed, func(r *historyRead) error {
		for _, k := range parsed {
			if job, errj := r.job(k); errj == nil && job != nil {
				jobs = append(jobs, job)
			}
		}

		return nil
	})

	return jobs, err
}

// viewHistoryKeys calls fn with a historyRead of the overlay entries, and
// unlooked keys, of keys alone.
func (s *logStore) viewHistoryKeys(keys []logKey, fn func(r *historyRead) error) error {
	r := &historyRead{s: s, overlay: make(map[logKey]logHistory), unlooked: make(map[logKey]uint64)}

	s.mu.RLock()

	for _, k := range keys {
		if h := s.state.overlay[k]; h != nil {
			r.overlay[k] = *h
		}

		if seq, ok := s.state.unlooked[k]; ok {
			r.unlooked[k] = seq
		}
	}

	gen := s.holdRefs()
	s.mu.RUnlock()

	defer s.releaseRefs(gen)

	return r.view(fn)
}

// retrieveCompleteJobsByRepGroup returns the history entry's job of each key
// looked up under repgroup that is not live, in key order.
func (s *logStore) retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error) {
	var jobs []*Job

	err := s.viewRepGroup(repgroup, func(r *historyRead, k logKey) error {
		job, err := r.archivedJob(k)
		if job != nil {
			jobs = append(jobs, job)
		}

		return err
	})

	return jobs, err
}

// retrieveOldestCompleteJobsByRepGroup is retrieveCompleteJobsByRepGroup with
// query's limits pushed down, as the bbolt store's: it counts every archived
// job query keeps and decodes only the oldest-started it can use. Folded
// records are read for their facets alone.
func (s *logStore) retrieveOldestCompleteJobsByRepGroup(repgroup string,
	query completeJobsQuery,
) (*completeJobsPage, error) {
	page := &completeJobsPage{fetched: make(map[string]int), counted: make(map[string]int)}

	err := s.viewHistory(repgroup, func(r *historyRead) error {
		selected, err := r.selectOldest(query, page.counted)
		if err != nil {
			return err
		}

		return r.decodeSelected(selected, page)
	})

	return page, err
}

// retrieveCompleteJobStatusByRepGroup returns a status summary of the archived
// jobs of repgroup that are not live: their count, or, with includeDetails,
// their usage too, read from folded records' usage fields alone.
func (s *logStore) retrieveCompleteJobStatusByRepGroup(repgroup string,
	includeDetails bool,
) (*RepGroupStatus, error) {
	summary := NewRepGroupStatus()

	decoder := codec.NewDecoderBytes(nil, s.ch)

	err := s.viewRepGroup(repgroup, func(r *historyRead, k logKey) error {
		return r.addStatus(summary, k, includeDetails, decoder)
	})

	return summary, err
}

// spendArchivedBytesByRepGroup charges budget for every key looked up under
// repgroup, live or not, as the bbolt store's does: a folded record's encoded
// length, an overlay entry's spec and complete records' lengths, and nothing,
// though it is still counted, for a key with no history entry. It decodes
// nothing.
func (s *logStore) spendArchivedBytesByRepGroup(repgroup string, budget *archivedBytesBudget) error {
	return s.viewRepGroup(repgroup, func(r *historyRead, k logKey) error {
		n, err := r.archivedBytes(k)
		if err != nil {
			return err
		}

		return budget.spendLen(n)
	})
}

// viewRepGroup calls fn, in a historyRead made for repgroup, with each key
// looked up under repgroup, in key order, until fn returns an error.
func (s *logStore) viewRepGroup(repgroup string, fn func(r *historyRead, k logKey) error) error {
	return s.viewHistory(repgroup, func(r *historyRead) error {
		keys, err := r.repGroupKeys()
		if err != nil {
			return err
		}

		for _, k := range keys {
			if err = fn(r, k); err != nil {
				return err
			}
		}

		return nil
	})
}

// viewHistory calls fn with a historyRead of the whole overlay, whose lookups
// are repGroup's.
func (s *logStore) viewHistory(repGroup string, fn func(r *historyRead) error) error {
	r := &historyRead{s: s, repGroup: repGroup}

	s.mu.RLock()
	r.overlay = make(map[logKey]logHistory, len(s.state.overlay))

	for k, h := range s.state.overlay {
		r.overlay[k] = *h
	}

	r.lookups = maps.Clone(s.state.repGroupKeys[repGroup])
	r.unlooked = maps.Clone(s.state.unlooked)
	gen := s.holdRefs()
	s.mu.RUnlock()

	defer s.releaseRefs(gen)

	return r.view(fn)
}

// openBoltWithHeadroom opens the bbolt file at path with opts, mapped with
// managerInitialMmapSize's headroom for the file's size, or with bbolt's
// default mapping if the process may not map that much (see openHistoryBolt
// and openManagerBolt).
func openBoltWithHeadroom(path string, opts bolt.Options) (*bolt.DB, error) {
	var fileSize int64
	if info, err := os.Stat(path); err == nil { //nolint:gosec // G703: path is one of the manager's own db files
		fileSize = info.Size()
	}

	opts.InitialMmapSize = managerInitialMmapSize(fileSize)

	bdb, err := bolt.Open(path, dbFilePermission, &opts)
	if err == nil || opts.InitialMmapSize == 0 || !errors.Is(err, syscall.ENOMEM) {
		return bdb, err
	}

	opts.InitialMmapSize = 0

	return bolt.Open(path, dbFilePermission, &opts)
}

// putSortedKeys puts each of keys in b with no value, in key order.
func putSortedKeys(b *bolt.Bucket, keys [][]byte) error {
	slices.SortFunc(keys, bytes.Compare)

	for _, k := range keys {
		if err := b.Put(k, nil); err != nil {
			return err
		}
	}

	return nil
}

// dropHistoryLookups deletes every rglookup of k, as the bbolt store's modify
// deletes its old key's (deleteLookupEntriesForJobKey). repgroups keeps the rep
// groups, which are ever added.
func dropHistoryLookups(tx *bolt.Tx, k logKey) error {
	key := k.String()
	prefix := []byte(key + dbDelimiter)
	reverse := tx.Bucket(bucketKeyRGs)

	var found [][]byte //nolint:prealloc // how many there are is not known before the walk

	cursor := reverse.Cursor()
	for rk, _ := cursor.Seek(prefix); bytes.HasPrefix(rk, prefix); rk, _ = cursor.Next() {
		found = append(found, slices.Clone(rk))
	}

	lookups := tx.Bucket(bucketRTK)

	for _, rk := range found {
		if err := lookups.Delete([]byte(string(rk[len(prefix):]) + dbDelimiter + key)); err != nil {
			return err
		}

		if err := reverse.Delete(rk); err != nil {
			return err
		}
	}

	return nil
}

// inFoldBatches calls fn with each index below n, foldApplyBatch at a time
// under the store's lock.
func (s *logStore) inFoldBatches(n int, fn func(i int)) {
	for start := 0; start < n; start += foldApplyBatch {
		s.mu.Lock()

		for i := start; i < min(start+foldApplyBatch, n); i++ {
			fn(i)
		}

		s.mu.Unlock()
		callHook(&logFoldBatchHook)
	}
}

// sampleCount returns how many learned-requirement samples history.bolt holds.
func (s *logStore) sampleCount() (int, error) {
	n := 0

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketJobRAM, bucketJobDisk, bucketJobSecs} {
			n += tx.Bucket(name).Stats().KeyN
		}

		return nil
	})

	return n, err
}

// retrieveLastCompletionTimeByRepGroup returns, as UTC instants to the second,
// the latest EndTime of a completion under each of repGroups that has one,
// live or not.
func (s *logStore) retrieveLastCompletionTimeByRepGroup(repGroups []string) (map[string]time.Time, error) {
	latest := s.overlayLastCompletions(repGroups)

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		ends := tx.Bucket(bucketRGEndTime)

		for _, rg := range repGroups {
			if v := ends.Get([]byte(rg)); len(v) == rgEndTimeBytes {
				noteLatest(latest, rg, int64(binary.BigEndian.Uint64(v))) //nolint:gosec // a unix time
			}
		}

		return nil
	})

	times := make(map[string]time.Time, len(latest))
	for rg, end := range latest {
		times[rg] = time.Unix(end, 0).UTC()
	}

	return times, err
}

// overlayLastCompletions returns overlayEnds of each of repGroups that has
// one.
func (s *logStore) overlayLastCompletions(repGroups []string) map[string]int64 {
	latest := make(map[string]int64, len(repGroups))

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, rg := range repGroups {
		if end, ok := s.state.overlayEnds[rg]; ok {
			latest[rg] = end
		}
	}

	return latest
}

// repGroupHasHistory says whether any job has completed under repGroup.
func (s *logStore) repGroupHasHistory(repGroup string) bool {
	s.mu.RLock()
	_, inOverlay := s.state.overlayEnds[repGroup]
	s.mu.RUnlock()

	if inOverlay {
		return true
	}

	has := false

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		has = len(tx.Bucket(bucketRGEndTime).Get([]byte(repGroup))) == rgEndTimeBytes

		return nil
	})

	return err == nil && has
}

// retrieveRepGroups returns, sorted, the rep group of every rglookup record:
// every rep group a job has ever been added under.
func (s *logStore) retrieveRepGroups() ([]string, error) {
	s.mu.RLock()
	rgs := slices.Collect(maps.Keys(s.state.lookedUpRGs))
	s.mu.RUnlock()

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRGs).ForEach(func(k, _ []byte) error {
			rgs = append(rgs, string(k))

			return nil
		})
	})

	slices.Sort(rgs)

	return slices.Compact(rgs), err
}
