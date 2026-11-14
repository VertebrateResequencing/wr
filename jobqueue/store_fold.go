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

// This file is the log store's folder (spec "Folder and history"): one
// background goroutine, never on a reply path, that at least once a second
// reads the log from the folded position F up to the writer's durable end and
// folds into history.bolt every complete record (with its spec), rglookup and
// modify record, and the learned-requirement samples of complete and exitstat
// records. It also holds the log store's learned requirements, which it
// invalidates as it folds new samples.

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"strings"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
)

const (
	// foldFailedLogMsg is logged, at most once per foldFailedLogEvery, while
	// folds fail.
	foldFailedLogMsg = "history fold failed"

	// foldFailedLogEvery is the least time between foldFailedLogMsg lines.
	foldFailedLogEvery = time.Minute

	// foldWriterName is the writer= value of the folder's "db writer" line.
	foldWriterName = "fold"

	// foldInterval is the most time between the end of one fold and the start
	// of the next, while folds succeed.
	foldInterval = time.Second

	// foldLagStep is the fold lag past which a fold's transactions may grow
	// (see foldTxLimit), in proportion to it. The folder trails the log by a
	// few seconds while it keeps up, and a portal burst's backlog kept
	// records waiting up to about 50s, which the bound must not grow for:
	// transactions of 15-20MB, as a 5s step let a burst's fold make, held the
	// log's syncs 170-230ms (gate soak G5,
	// .docs/customdb/results/phase1-gate-1g5.md). Past that, a fold that
	// keeps records waiting longer still is behind for good, and must fold
	// more records a second than small transactions can.
	foldLagStep = time.Minute

	// foldChunkRecords is how many log records one fold transaction reads
	// before it ends, at the next frame (see foldNow), so that a backlog's fold
	// holds its records in memory, the store's lock and history.bolt's commit a
	// chunk at a time. It is about 2,000 archives (a complete and a delete
	// record each): with 64 writers archiving as fast as they can, a fold of
	// 4,096 took about 170ms and left the slowest archive near what it is with
	// no folder (35ms against up to 26ms), where 16,384 made it 70ms and 65,536
	// 170ms, as each commit's writes delay the log's syncs, while 1,024 bought
	// no more throughput for four times the commits.
	foldChunkRecords = 4096
)

// errFoldChunkFull stops a fold's reading of the log at a frame once it has
// read its chunk's records.
var errFoldChunkFull = errors.New("fold chunk full")

// Test seams, nil outside tests. They are atomic, as the folder goroutines of
// stores a test is not looking at read them too.
var (
	// logFoldHook is called by the folder goroutine before each fold; false
	// skips that fold, so a hook can pause the folder, or block it.
	logFoldHook atomic.Pointer[func() bool] //nolint:gochecknoglobals // a test seam

	// logFoldReadHook is called by each fold once it has read its chunk of the
	// log, before it folds it.
	logFoldReadHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logFoldTxHook is called inside each fold's history.bolt transaction,
	// once it has made every change but before it commits; an error it
	// returns fails the fold.
	logFoldTxHook atomic.Pointer[func() error] //nolint:gochecknoglobals // a test seam

	// logFoldCommittedHook is called by each fold once its history.bolt
	// transaction has committed, before it changes anything in memory.
	logFoldCommittedHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logFoldBatchHook is called by each fold after each batch of its changes
	// to the in-memory state, with the store's lock released (see
	// foldHistory).
	logFoldBatchHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logFoldChunkRecords, if above 0, replaces foldChunkRecords.
	logFoldChunkRecords atomic.Int64 //nolint:gochecknoglobals // a test seam
)

// foldComplete is a complete record a fold read: its key, seq and body after
// the key, and the latest spec record of its key its chunk read before it, if
// any.
type foldComplete struct {
	key      logKey
	seq      uint64
	body     []byte
	readSpec wal.Ref
	hasSpec  bool
}

// specOf returns the spec record of c's history entry: the one the reducer
// paired with c, if c applied as its key's overlay entry, whether or not a
// later complete record of its key has replaced it since (logState.replaced);
// else, as c has not applied yet, the one it will pair c with, which no write
// of the key's spec can change first, as each claims the key until it has
// applied (spec "Write path", claims): the latest of its key read before it in
// its chunk, else its key's live spec, else its overlay entry's.
//
// The chunk's own spec records suffice. Every earlier write of c's key has
// applied (claims), so if the key is live its live spec is the one; if it is
// not, c's write is an archive of a key not live, a single write the log
// makes as one block, which holds the key's spec record too and which no
// chunk splits.
func (st *logState) specOf(c *foldComplete) (wal.Ref, bool) {
	if h := st.appliedAs(c); h != nil {
		return h.spec, true
	}

	switch e, h := st.live[c.key], st.overlay[c.key]; {
	case c.hasSpec:
		return c.readSpec, true
	case e != nil:
		return e.spec, true
	case h != nil:
		return h.spec, true
	default:
		return wal.Ref{}, false
	}
}

// appliedAs returns the overlay entry c applied as, its key's current one or
// one a later one replaced, or nil if c has not applied as one.
func (st *logState) appliedAs(c *foldComplete) *logHistory {
	if h := st.overlay[c.key]; h != nil && h.seq == c.seq {
		return h
	}

	for _, h := range st.replaced[c.key] {
		if h.seq == c.seq {
			return h
		}
	}

	return nil
}

// foldRead is one fold's reading of the log, a chunk of foldNow's: the
// records it folds, in log order, the latest spec record of each key it has
// read, how many records it has read, and the most it reads before ending at a
// frame.
type foldRead struct {
	s         *logStore
	completes []foldComplete
	lookups   []historyLookup
	samples   []jobExitData
	specs     map[logKey]wal.Ref
	records   int
	chunk     int
}

// newFoldRead returns an empty foldRead of s.
func newFoldRead(s *logStore) *foldRead {
	chunk := foldChunkRecords
	if n := logFoldChunkRecords.Load(); n > 0 {
		chunk = int(n)
	}

	return &foldRead{s: s, specs: make(map[logKey]wal.Ref), chunk: chunk}
}

// readFile reads wal file files[i] from from, if it is from's file, or its
// start, to durEnd, if it is durFile, or else to its valid end. If it reaches
// a frame once r has read its chunk's records, it stops there, before that
// frame, and returns where.
func (r *foldRead) readFile(files []uint32, i int, from historyPos, durFile uint32,
	durEnd int64) (*historyPos, error) {
	n := files[i]

	limit, err := r.s.foldLimit(files, i, durFile, durEnd)
	if err != nil {
		return nil, err
	}

	var start int64
	if n == from.file {
		start = int64(from.off) //nolint:gosec // an offset
	}

	if limit <= start {
		return nil, nil //nolint:nilnil // nothing to read is not an error
	}

	return r.scanFile(n, start, limit)
}

// scanFile reads wal file n from start to limit, for readFile.
func (r *foldRead) scanFile(n uint32, start, limit int64) (*historyPos, error) {
	f, err := r.s.segmentFile(n)
	if err != nil {
		return nil, err
	}

	end, err := wal.Scan(io.NewSectionReader(f, start, limit-start), func(ref wal.Ref, rec []byte) error {
		ref.File, ref.Off = n, ref.Off+uint64(start) //nolint:gosec // an offset

		return r.record(ref, rec)
	})

	switch {
	case errors.Is(err, errFoldChunkFull):
		return &historyPos{file: n, off: uint64(start + end)}, nil //nolint:gosec // an offset
	case err == nil && end != limit-start:
		err = fmt.Errorf("%w: wal file %08x is damaged at %d, before its durable end %d",
			errStoreCorrupt, n, start+end, limit)
	}

	return nil, err
}

// record notes rec, at ref, if a fold folds it or needs it: a spec record, as
// the spec of a complete record of its key after it, and the records the fold
// folds. A wal file's header, its first frame, is skipped. It returns
// errFoldChunkFull at the first record of a frame after a header once r has
// read its chunk's records, so a chunk never ends inside a block.
func (r *foldRead) record(ref wal.Ref, rec []byte) error {
	if ref.Off == 0 {
		return nil
	}

	if ref.Idx == 0 && r.records >= r.chunk {
		return errFoldChunkFull
	}

	r.records++

	parsed, err := parseLogRecord(rec)
	if err == nil {
		err = r.note(ref, &parsed)
	}

	if err != nil {
		return fmt.Errorf("%w: wal file %08x at %d: %w", errStoreCorrupt, ref.File, ref.Off, err)
	}

	return nil
}

// note notes parsed, the record at ref, if a fold folds it or needs it.
func (r *foldRead) note(ref wal.Ref, parsed *logRecord) error {
	switch parsed.typ {
	case logRecSpec:
		r.specs[parsed.key] = ref
	case logRecComplete:
		readSpec, hasSpec := r.specs[parsed.key]
		r.completes = append(r.completes, foldComplete{key: parsed.key, seq: parsed.seq,
			body: append([]byte(nil), parsed.body...), readSpec: readSpec, hasSpec: hasSpec})
	case logRecRGLookup:
		r.lookups = append(r.lookups, historyLookup{repGroup: strings.Clone(parsed.name), key: parsed.key,
			seq: parsed.seq})
	case logRecModify:
		r.lookups = append(r.lookups, historyLookup{key: parsed.key, seq: parsed.seq, dropped: true})
	case logRecExitStat:
		exit, err := parseExitStat(parsed.body)
		if err != nil {
			return err
		}

		r.samples = append(r.samples, exit)
	default:
	}

	return nil
}

// fold returns the fold of what r read, to position to: each complete record
// with the job of its history entry, made of its spec (see specOf) and its
// body.
func (r *foldRead) fold(to historyPos) (*historyFold, error) {
	f := &historyFold{lookups: r.lookups, samples: r.samples, to: to}

	specRefs, gen := r.specRefs()

	callHook(&logFoldSpecsHook)

	bodies, err := r.s.readRecordBodies(maps.Values(specRefs))

	r.s.releaseRefs(gen)

	if err != nil {
		return nil, err
	}

	for i, c := range r.completes {
		ref, ok := specRefs[i]
		if !ok {
			continue
		}

		job, errj := r.s.jobOf(bodies[ref], c.body)
		if errj != nil {
			return nil, errj
		}

		f.complete = append(f.complete, historyFolded{key: c.key, seq: c.seq, job: job})
	}

	return f, nil
}

// specRefs returns the Ref of the spec of each of r's complete records, by
// index, leaving out one with none, which the reducer drops too (see
// applyComplete), and the hold (see holdRefs) to release once they are read, so
// that no checkpoint deletes a file they name meanwhile.
func (r *foldRead) specRefs() (map[int]wal.Ref, uint64) {
	refs := make(map[int]wal.Ref, len(r.completes))

	r.s.mu.RLock()
	defer r.s.mu.RUnlock()

	for i := range r.completes {
		if ref, ok := r.s.state.specOf(&r.completes[i]); ok {
			refs[i] = ref
		}
	}

	return refs, r.s.holdRefs()
}

// readFiles reads, as readFile does, each of files between positions from and
// to, returning where it stopped: to, or the end of its chunk.
func (r *foldRead) readFiles(files []uint32, from, to historyPos) (historyPos, error) {
	for i, n := range files {
		if n < from.file || n > to.file {
			continue
		}

		stop, err := r.readFile(files, i, from, to.file, int64(to.off)) //nolint:gosec // an offset
		if err != nil {
			return to, err
		}

		if stop != nil {
			return *stop, nil
		}
	}

	return to, nil
}

// callHook calls the func h holds, if any.
func callHook(h *atomic.Pointer[func()]) {
	if fn := h.Load(); fn != nil {
		(*fn)()
	}
}

// frameIdxs returns the index of each of refs' records by the Ref of its frame
// (its Idx 0), and how many refs there are.
func frameIdxs(refs iter.Seq[wal.Ref]) (map[wal.Ref]map[uint32]bool, int) {
	frames := make(map[wal.Ref]map[uint32]bool)
	n := 0

	for ref := range refs {
		n++

		frame := wal.Ref{File: ref.File, Off: ref.Off}
		if frames[frame] == nil {
			frames[frame] = make(map[uint32]bool)
		}

		frames[frame][ref.Idx] = true
	}

	return frames, n
}

// foldLimit returns where a fold reads wal file files[i] to: durEnd, if it is
// durFile, the end of the durable log, else the end recovery gives it.
func (s *logStore) foldLimit(files []uint32, i int, durFile uint32, durEnd int64) (int64, error) {
	if files[i] == durFile {
		return durEnd, nil
	}

	limit, err := s.walValidEnd(files, i)
	if err == nil && limit < 0 {
		err = fmt.Errorf("%w: wal file %08x has no recorded end, before the durable %08x",
			errStoreCorrupt, files[i], durFile)
	}

	return limit, err
}

// startFolder starts the folder goroutine, which close stops.
func (s *logStore) startFolder() {
	s.foldStop = make(chan struct{})
	s.foldExited = make(chan struct{})

	go s.runFolder()
}

// runFolder folds every foldInterval until stopped, retrying a failed fold
// with the log writer's backoff instead.
func (s *logStore) runFolder() {
	defer internal.LogPanic(s.ctx, "jobqueue log store folder", false)
	defer close(s.foldExited)

	timer := time.NewTimer(foldInterval)
	defer timer.Stop()

	failures := 0

	for {
		select {
		case <-s.foldStop:
			return
		case <-timer.C:
		}

		timer.Reset(s.foldRound(&failures))
	}
}

// foldRound is one of the folder goroutine's rounds: a fold, unless the test
// hook skips it. It returns how long to wait before the next round: the
// backoff after *failures consecutive failed folds, which it counts, else
// foldInterval. A fold that close stopped is not a failure.
func (s *logStore) foldRound(failures *int) time.Duration {
	if hook := logFoldHook.Load(); hook != nil && !(*hook)() {
		return foldInterval
	}

	err := s.foldNow()

	switch {
	case errors.Is(err, errDBClosed):
		return foldInterval
	case err != nil:
		*failures++

		s.foldFailed(err)

		return wal.RetryDelay(*failures)
	default:
		*failures = 0

		return foldInterval
	}
}

// foldFailed logs foldFailedLogMsg at most once per foldFailedLogEvery, with
// how many folds failed since the last. Only the folder goroutine calls it.
func (s *logStore) foldFailed(err error) {
	failures := s.foldFailures.Add(1)

	if time.Since(s.foldFailLogged) < foldFailedLogEvery {
		return
	}

	s.foldFailLogged = time.Now()
	s.foldFailures.Add(-failures)

	clog.Error(s.ctx, foldFailedLogMsg, "err", err, "failures", failures)
}

// stopFolder stops the folder goroutine and makes foldNow return errDBClosed,
// so nothing folds once history.bolt is to close.
func (s *logStore) stopFolder() {
	close(s.foldStop)
	<-s.foldExited

	s.foldMu.Lock()
	s.foldClosed = true
	s.foldMu.Unlock()
}

// foldNow folds the log from the folded position to the writer's durable end
// as it is when foldNow starts, so that writes meanwhile never keep it from
// returning, as the folder does, and returns once that fold is done. Folds are
// made one at a time, each from where the last ended. A fold of more than
// foldChunkRecords records is made in chunks, each ending at a frame and
// committed, with the folded position it moves to, in a transaction of its
// own, so that it holds the store's lock, and its records in memory, for a
// chunk at a time, and a crash between chunks leaves history.bolt holding
// the log up to its folded position. Once close has asked the folder to stop,
// it returns errDBClosed after the chunk it is folding, so that close never
// waits for a backlog's fold: history.bolt then holds the log up to the
// folded position, as after a crash between chunks, and the next open folds
// the rest.
func (s *logStore) foldNow() error {
	s.foldMu.Lock()
	defer s.foldMu.Unlock()

	if s.foldClosed {
		return errDBClosed
	}

	began := time.Now()
	if s.foldLagFrom.IsZero() {
		s.foldLagFrom = began
	}

	durFile, durEnd := s.w.Durable()
	to := historyPos{file: durFile, off: uint64(durEnd)} //nolint:gosec // an offset

	for {
		done, err := s.foldChunk(to, began)
		if err != nil || done {
			return err
		}

		select {
		case <-s.foldStop:
			return errDBClosed
		default:
		}
	}
}

// foldChunk folds the log's next chunk towards to (see foldNow), and reports
// true, having noted that a fold that began at began reached its target, if
// there was none. Its history.bolt transactions write about foldTxLimit of
// how long its oldest record may have waited: since foldLagFrom.
func (s *logStore) foldChunk(to historyPos, began time.Time) (bool, error) {
	f, err := s.readFold(to)
	if err != nil {
		return false, err
	}

	if f == nil {
		s.foldLagFrom = began

		return true, nil
	}

	lag := time.Since(s.foldLagFrom)
	s.foldStats.observeLag(lag)

	return false, s.foldHistoryUpTo(f, foldTxLimit(lag))
}

// foldTxLimit returns how many bytes of pages one transaction of a fold may
// write (see historyBolt.fold) when its oldest record may have waited lag to
// be folded: foldTxMaxBytes, and once lag passes foldLagStep, that many times
// lag/foldLagStep. Small transactions keep each commit from holding the log's
// syncs long, but each commit costs about the same however few records it
// holds, so they fold fewer records a second; a fold falling behind is let
// make them bigger, as unsplit folds of a backlog's 4,096-record chunks were,
// so that a backlog folds as cheaply per record as before the bound
// (.docs/bugfixes/261111-reserve-tail-nfs-4766eb70.md).
func foldTxLimit(lag time.Duration) int {
	limit := foldTxMaxBytes()
	if lag > foldLagStep {
		limit = int(float64(limit) * float64(lag) / float64(foldLagStep))
	}

	return limit
}

// readFold reads the log from the folded position F to to, the writer's
// durable end, into the fold of it, or of its first chunk (see foldNow), or
// returns nil if F is there already. A wal file followed by another is read
// only up to the end recovery gives it (see walValidEnd), so an abandoned batch
// is never folded.
func (s *logStore) readFold(to historyPos) (*historyFold, error) {
	s.mu.RLock()
	from := s.state.folded
	s.mu.RUnlock()

	if !to.after(from) {
		return nil, nil //nolint:nilnil // nothing to fold is not an error
	}

	files, err := walFileNumbers(s.dir)
	if err != nil {
		return nil, err
	}

	r := newFoldRead(s)

	end, err := r.readFiles(files, from, to)
	if err != nil {
		return nil, err
	}

	callHook(&logFoldReadHook)

	return r.fold(end)
}

// readRecordBodies returns the body after the key of each record refs name,
// reading each frame once, so a block holding several is decompressed once.
// The caller holds refs (see holdRefs).
func (s *logStore) readRecordBodies(refs iter.Seq[wal.Ref]) (map[wal.Ref][]byte, error) {
	frames, n := frameIdxs(refs)
	bodies := make(map[wal.Ref][]byte, n)

	keep := func(ref wal.Ref, rec []byte) error {
		parsed, err := parseLogRecord(rec)
		if err != nil {
			return err
		}

		bodies[ref] = append([]byte(nil), parsed.body...)

		return nil
	}

	for frame, idxs := range frames {
		if err := s.readFrameRecords(frame, idxs, keep); err != nil {
			return nil, err
		}
	}

	return bodies, nil
}

// readFrameRecords calls keep with each record of the frame at frame whose
// index is in idxs, and its Ref, reading the frame once. rec is only valid
// until keep returns.
func (s *logStore) readFrameRecords(frame wal.Ref, idxs map[uint32]bool,
	keep func(ref wal.Ref, rec []byte) error,
) error {
	f, err := s.segmentFile(frame.File)
	if err != nil {
		return err
	}

	bufs := s.takeReadBufs()
	defer s.putReadBufs(bufs)

	return wal.ReadFrameInto(f, frame.Off, bufs, func(ref wal.Ref, rec []byte) error {
		if !idxs[ref.Idx] {
			return nil
		}

		ref.File = frame.File

		return keep(ref, rec)
	})
}

// cachedReqGroupRecs returns reqGroup's last loaded learned requirements (nil
// if never loaded) and whether they are current. It does no I/O.
func (s *logStore) cachedReqGroupRecs(reqGroup string) (*scheduler.Requirements, bool) {
	return s.reqStats.cached(reqGroup)
}

// refreshReqGroupRecs loads reqGroup's learned requirements if they are not
// current (the folder invalidates them as it folds new samples), the last load
// started at least the refresh interval ago, and no load is in flight; it
// returns the cached value afterwards.
func (s *logStore) refreshReqGroupRecs(reqGroup string) *scheduler.Requirements {
	return s.reqStats.refresh(reqGroup, s.loadReqGroupRecs)
}

// refreshReqGroupRecsLater is refreshReqGroupRecs in the background, started
// only when a load is due, and not once the store has started closing. As the
// bbolt store's, it takes no context.
//
//nolint:contextcheck,nolintlint // see above
func (s *logStore) refreshReqGroupRecsLater(reqGroup string) {
	s.reqStats.refreshLater(reqGroup, s.loadReqGroupRecs)
}

// warmReqGroupRecs calls refreshReqGroupRecs once for each distinct ReqGroup of
// jobs.
//
//nolint:contextcheck,nolintlint // as refreshReqGroupRecsLater
func (s *logStore) warmReqGroupRecs(jobs []*Job) {
	s.reqStats.warm(jobs, s.loadReqGroupRecs)
}

// reqStatChanges returns the learned-requirements cache's change stamp.
func (s *logStore) reqStatChanges() uint32 {
	return s.reqStats.changes.Load()
}

// setReqStatRefreshInterval sets the least time between loads of one ReqGroup's
// learned requirements.
func (s *logStore) setReqStatRefreshInterval(d time.Duration) {
	s.reqStats.interval = d
}

// setRecRounding sets the rounding, in seconds and MBs, of recommended reserve
// times and memory/disk.
func (s *logStore) setRecRounding(secRound, mbRound int) {
	s.recSecRound = secRound
	s.recMBRound = mbRound
}

// loadReqGroupRecs reads reqGroup's learned requirements from the samples the
// folder has folded into history.bolt.
func (s *logStore) loadReqGroupRecs(reqGroup string) (*scheduler.Requirements, error) {
	return loadLearnedReqs(s.history.bolt, reqGroup, recommendationRound(s.recSecRound, RecSecRound),
		recommendationRound(s.recMBRound, RecMBRound))
}
