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

// This file holds a checkpoint's fold backlog (spec "Checkpoints", step 3):
// the complete, rglookup, modify and exitstat records from the folded
// position to the cut, which the folder has yet to fold, each after a header
// naming the position in the wal it was written at, and the spec each
// complete record applied with. So a checkpoint never waits for the folder,
// the wal files before its cut are deleted however far the folder trails,
// recovery rebuilds the overlay from the backlog, and the folder folds the
// backlog from the checkpoint as it would have folded those wal files.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
)

const (
	// backlogApplied marks a modify record the reducer applied, as a live
	// spec later than it makes the reducer ignore one (see applyModify).
	backlogApplied uint8 = 1 << 0

	// backlogSpecCopy marks a spec record a backlog holds only as the spec
	// of a complete record after it: it applies to nothing.
	backlogSpecCopy uint8 = 1 << 1

	// backlogHasSpec marks a complete record the reducer applied as an
	// overlay entry, with the Ref and rep group of the spec it applied with.
	backlogHasSpec uint8 = 1 << 2

	// walRefLen is the length of a Ref a backlog header holds.
	walRefLen = 4 + 8 + 4

	// backlogReadBatch is how many backlog records a checkpoint reads at once
	// to copy them, so that it holds a bounded number in memory.
	backlogReadBatch = 4096
)

// logFoldBacklogBlockHook is called by a fold reading a checkpoint's backlog
// after each block it reads to its end. A test seam, nil outside tests.
var logFoldBacklogBlockHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

// errBacklogMalformed is the error of parsing a backlog header that is not one
// a checkpoint writes.
var errBacklogMalformed = errors.New("malformed checkpoint backlog header")

// backlogHeader is the header before each record of a checkpoint's backlog:
// the position of the record in the wal it was written to, its flags and, for
// a complete record with backlogHasSpec, the Ref of its spec in the
// checkpoint and that spec's rep group.
type backlogHeader struct {
	pos      wal.Ref
	flags    uint8
	spec     wal.Ref
	repGroup string
}

// parseBacklogHeader returns the backlog header body, a backlog header
// record's body, holds. Its rep group aliases nothing.
func parseBacklogHeader(body []byte) (backlogHeader, error) {
	if len(body) < walRefLen+1 {
		return backlogHeader{}, errBacklogMalformed
	}

	h := backlogHeader{pos: parseWALRef(body), flags: body[walRefLen]}
	rest := body[walRefLen+1:]

	if h.flags&backlogHasSpec == 0 {
		if len(rest) != 0 {
			return backlogHeader{}, errBacklogMalformed
		}

		return h, nil
	}

	if len(rest) < walRefLen {
		return backlogHeader{}, errBacklogMalformed
	}

	h.spec = parseWALRef(rest)

	fr := flatReader{b: rest[walRefLen:]}
	h.repGroup = strings.Clone(fr.str())

	if fr.end() != nil {
		return backlogHeader{}, errBacklogMalformed
	}

	return h, nil
}

// backlogHeaderOf returns the backlog header rec holds, and whether rec is a
// backlog header record.
func backlogHeaderOf(rec []byte) (*backlogHeader, bool, error) {
	if len(rec) < logRecordHeader || logRecordType(rec[0]) != logRecBacklog {
		return nil, false, nil
	}

	h, err := parseBacklogHeader(rec[logRecordHeader:])

	return &h, true, err
}

// backlogEntry is one record of a checkpoint's backlog, as its merge noted it:
// its position in the wal it was written to, its Ref, and whether the reducer
// applied it, if it is a modify.
type backlogEntry struct {
	pos, ref wal.Ref
	applied  bool
}

// writeBacklogRecord adds the record of backlog entry e after its header,
// and first the spec of its overlay entry in entries, if it has one and cw
// does not hold it yet, noting the new Ref of each it copies in cw's swap.
// recs holds the records, by Ref.
func (m *checkpointMerge) writeBacklogRecord(cw *checkpointWriter, e backlogEntry, recs map[wal.Ref][]byte,
	entries map[wal.Ref]*logHistory) error {
	h := backlogHeader{pos: e.pos}
	if e.applied {
		h.flags |= backlogApplied
	}

	if entry := entries[e.ref]; entry != nil {
		spec, err := m.backlogSpec(cw, e.pos, entry, recs[entry.spec])
		if err != nil {
			return err
		}

		h.flags |= backlogHasSpec
		h.spec, h.repGroup = spec.ref, spec.repGroup
	}

	ref, err := cw.addPair(appendBacklogRecord(nil, &h), recs[e.ref])
	if err == nil {
		cw.swap[e.ref] = ref
	}

	return err
}

// backlogSpecRef is the copy in a checkpoint of an overlay entry's spec and
// its rep group.
type backlogSpecRef struct {
	ref      wal.Ref
	repGroup string
}

// backlogSpec returns the copy in cw of entry's spec, rec, adding one after a
// header at pos if cw holds none yet, and the spec's rep group.
func (m *checkpointMerge) backlogSpec(cw *checkpointWriter, pos wal.Ref, entry *logHistory,
	rec []byte) (backlogSpecRef, error) {
	var r logReplayer

	op, err := r.op(rec, entry.spec, m.s.ch)
	if err != nil {
		return backlogSpecRef{}, fmt.Errorf("%w: spec %+v: %w", errStoreCorrupt, entry.spec, err)
	}

	if ref, ok := cw.swap[entry.spec]; ok {
		return backlogSpecRef{ref: ref, repGroup: op.index.repGroup}, nil
	}

	ref, err := cw.addPair(appendBacklogRecord(nil, &backlogHeader{pos: pos, flags: backlogSpecCopy}), rec)
	cw.swap[entry.spec] = ref

	return backlogSpecRef{ref: ref, repGroup: op.index.repGroup}, err
}

// appendBacklogRecord appends a backlog header record holding h to dst.
func appendBacklogRecord(dst []byte, h *backlogHeader) []byte {
	dst = appendRecordHeader(dst, logRecBacklog, 0)
	dst = appendWALRef(dst, h.pos)
	dst = append(dst, h.flags)

	if h.flags&backlogHasSpec != 0 {
		dst = appendWALRef(dst, h.spec)
		dst = appendFlatStrings(dst, h.repGroup)
	}

	return dst
}

// appendWALRef appends ref's file, offset and index to dst.
func appendWALRef(dst []byte, ref wal.Ref) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, ref.File)
	dst = binary.LittleEndian.AppendUint64(dst, ref.Off)

	return binary.LittleEndian.AppendUint32(dst, ref.Idx)
}

// backlogRecord notes rec, at ref in a checkpoint, a backlog record with
// header h, unless r has read its chunk's records and rec is of another frame
// in the wal than the last it noted: then it sets *stop to that frame's
// position and returns errFoldChunkFull.
func (r *foldRead) backlogRecord(ref wal.Ref, rec []byte, h *backlogHeader, stop **historyPos) error {
	frame := historyPos{file: h.pos.File, off: h.pos.Off}

	if r.records >= r.chunk && frame != r.lastFrame {
		*stop = &frame

		return errFoldChunkFull
	}

	r.records++
	r.lastFrame = frame

	parsed, err := parseLogRecord(rec)
	if err == nil {
		err = r.note(ref, &parsed)
	}

	if err != nil {
		return fmt.Errorf("%w: checkpoint %08x at %d: %w", errStoreCorrupt, ref.File&^checkpointRefFlag, ref.Off,
			err)
	}

	if parsed.typ == logRecComplete && h.flags&backlogHasSpec != 0 {
		c := &r.completes[len(r.completes)-1]
		c.readSpec, c.hasSpec = h.spec, true
	}

	return nil
}

// backlogBlockRead is readBacklogBlock's reading of a block: the header of the
// record it reads next, and where it stopped.
type backlogBlockRead struct {
	r       *foldRead
	refFile uint32
	from    historyPos
	head    *backlogHeader
	stop    *historyPos
}

// record is readBacklogBlock's wal.ReadFrame func.
func (b *backlogBlockRead) record(ref wal.Ref, rec []byte) error {
	h, ok, err := backlogHeaderOf(rec)

	switch {
	case err != nil:
		return err
	case ok:
		b.head = h

		return nil
	case b.head == nil:
		b.stop = &historyPos{file: b.refFile &^ checkpointRefFlag}

		return errFoldChunkFull
	}

	h, b.head = b.head, nil
	ref.File = b.refFile

	if h.flags&backlogSpecCopy != 0 || b.from.covers(h.pos) {
		return nil
	}

	return b.r.backlogRecord(ref, rec, h, &b.stop)
}

// readBacklogBlock notes, as record notes the wal's records, each record of
// the block at off in checkpoint f, whose Refs have file number refFile, that
// is not before from and not a spec copy, with its spec if it has one. It
// returns where it stopped if it reached a record of another frame in the wal
// than the last it noted once r has read its chunk's records, or if the block
// is not a backlog block, the cut.
func (r *foldRead) readBacklogBlock(f *os.File, off uint64, refFile uint32, from historyPos) (*historyPos,
	error) {
	b := backlogBlockRead{r: r, refFile: refFile, from: from}

	err := wal.ReadFrame(f, off, b.record)
	if errors.Is(err, errFoldChunkFull) {
		err = nil
	}

	return b.stop, err
}

// foldBacklog is the folder's note of the newest checkpoint's backlog: its
// cut, its block offsets and the index of its first backlog block (len of
// offsets if it has none).
type foldBacklog struct {
	ckpt    uint32
	offsets []uint64
	first   int
}

// startBlock returns the index of the backlog block to read from position
// from: the last whose first record is before from, else the first. A frame
// in the wal can span several blocks, each after the first starting with it,
// and from, where a fold chunk stopped, is the start of a frame, so a block
// starting at from may hold none of that frame's first records.
func (b *foldBacklog) startBlock(f *os.File, from historyPos) (int, error) {
	notBefore, err := searchBlocks(len(b.offsets)-b.first, func(i int) (bool, error) {
		pos, _, errf := firstBacklogPos(f, b.offsets[b.first+i])

		return !from.after(historyPos{file: pos.File, off: pos.Off}), errf
	})

	return b.first + max(notBefore-1, 0), err
}

// searchBlocks returns the smallest index below n for which pred is true, n
// if none is, pred being false and then true over the indexes.
func searchBlocks(n int, pred func(i int) (bool, error)) (int, error) {
	lo, hi := 0, n

	for lo < hi {
		mid := (lo + hi) / 2 //nolint:mnd // halfway

		ok, err := pred(mid)
		if err != nil {
			return 0, err
		}

		if ok {
			hi = mid
		} else {
			lo = mid + 1
		}
	}

	return lo, nil
}

// firstBacklogPos returns the position its header names of the first record of
// the block at off in checkpoint f, and whether the block is a backlog block,
// which starts with a header.
func firstBacklogPos(f *os.File, off uint64) (wal.Ref, bool, error) {
	var (
		pos     wal.Ref
		backlog bool
	)

	err := wal.ReadFrame(f, off, func(ref wal.Ref, rec []byte) error {
		if ref.Idx > 0 {
			return nil
		}

		h, ok, errh := backlogHeaderOf(rec)
		if ok {
			pos, backlog = h.pos, true
		}

		return errh
	})

	return pos, backlog, err
}

// backlogOf returns the folder's note of the backlog of checkpoint ckpt, open
// as f, finding its first backlog block once per checkpoint. Only the folder,
// holding foldMu, calls it.
func (s *logStore) backlogOf(f *os.File, ckpt uint32) (*foldBacklog, error) {
	if s.foldBacklog != nil && s.foldBacklog.ckpt == ckpt {
		return s.foldBacklog, nil
	}

	offsets, err := readCheckpointTrailer(f)
	if err != nil {
		return nil, err
	}

	b := &foldBacklog{ckpt: ckpt, offsets: offsets}

	b.first, err = searchBlocks(len(offsets), func(i int) (bool, error) {
		_, backlog, errf := firstBacklogPos(f, offsets[i])

		return backlog, errf
	})
	if err != nil {
		return nil, err
	}

	s.foldBacklog = b

	return b, nil
}

// parseWALRef returns the Ref at the start of b.
func parseWALRef(b []byte) wal.Ref {
	return wal.Ref{File: binary.LittleEndian.Uint32(b), Off: binary.LittleEndian.Uint64(b[4:]),
		Idx: binary.LittleEndian.Uint32(b[12:])}
}

// folds reports whether the folder folds records of typ.
func folds(typ logRecordType) bool {
	switch typ {
	case logRecComplete, logRecRGLookup, logRecModify, logRecExitStat:
		return true
	default:
		return false
	}
}

// derefPos returns *p, or the zero position if p is nil.
func derefPos(p *historyPos) historyPos {
	if p == nil {
		return historyPos{}
	}

	return *p
}

// refsOf returns the Ref ref gives of each of items.
func refsOf[T any](items []T, ref func(T) wal.Ref) iter.Seq[wal.Ref] {
	return func(yield func(wal.Ref) bool) {
		for _, item := range items {
			if !yield(ref(item)) {
				return
			}
		}
	}
}

// keepBody sets r's body to body, for a record type with no key.
func (r *logRecord) keepBody(body []byte) error {
	r.body = body

	return nil
}

// applyBacklog applies op, a record of a checkpoint's backlog, to the overlay,
// as the reducer applies the record at its position in the wal, which is
// before the checkpoint's cut: the checkpoint's live records already hold what
// it did to the live plane. A complete record not before the folded position
// and not outranked in the history index becomes its key's overlay entry with
// the spec it applied with, if it applied as one; an rglookup is added to the
// overlay's; and a modify the reducer applied drops the overlay's rglookups of
// its old key, which queries skip in history.bolt too until it is folded.
func (st *logState) applyBacklog(op *logOp) {
	at := *op
	at.ref = op.backlog.pos

	switch op.typ {
	case logRecComplete:
		st.applyBacklogComplete(op, &at)
	case logRecRGLookup:
		st.applyRGLookup(&at)
	case logRecModify:
		if op.backlog.flags&backlogApplied == 0 {
			return
		}

		st.dropLookups(op.key)

		if !st.folded.covers(at.ref) {
			st.unlooked[op.key] = max(st.unlooked[op.key], op.seq)
		}
	default:
	}
}

// applyBacklogComplete is applyBacklog of a complete record, op, at at's Ref
// in the wal.
func (st *logState) applyBacklogComplete(op, at *logOp) {
	if op.backlog.flags&backlogHasSpec == 0 || st.completeOutranked(at) {
		return
	}

	st.putOverlay(op.key, &logHistory{seq: op.seq, complete: op.ref, spec: op.backlog.spec,
		repGroup: op.backlog.repGroup, endTime: op.endTime})
	st.history[op.key] = op.seq
}

// noteBacklog notes op, a record m reads at ref, as a record of its backlog if
// the folder folds its type and its position is not before the folded position
// the backlog starts at. Before op applies, it notes whether a modify read
// from the wal applies, as applyModify decides.
func (m *checkpointMerge) noteBacklog(op *logOp, ref wal.Ref) {
	if !folds(op.typ) {
		return
	}

	e := backlogEntry{pos: ref, ref: ref}

	switch {
	case op.backlog != nil:
		e.pos, e.applied = op.backlog.pos, op.backlog.flags&backlogApplied != 0
	case op.typ == logRecModify:
		live := m.state.live[op.key]
		e.applied = live == nil || op.seq > live.specSeq
	}

	if !m.histFrom.covers(e.pos) {
		m.backlog = append(m.backlog, e)
	}
}

// writeBacklog adds m's backlog to cw, each record after its header, reading
// them, and the spec of each complete record that applied as an overlay entry,
// backlogReadBatch at a time. A spec cw already holds, as a live job's, is not
// copied again, so that every Ref in memory has one copy to swap to, which a
// reopen finds too.
func (m *checkpointMerge) writeBacklog(cw *checkpointWriter) error {
	for batch := range slices.Chunk(m.backlog, backlogReadBatch) {
		if err := m.writeBacklogBatch(cw, batch); err != nil {
			return err
		}
	}

	return nil
}

// writeBacklogBatch is writeBacklog of batch.
func (m *checkpointMerge) writeBacklogBatch(cw *checkpointWriter, batch []backlogEntry) error {
	recs, err := m.s.readRecords(refsOf(batch, func(e backlogEntry) wal.Ref { return e.ref }))
	if err != nil {
		return err
	}

	specs, err := m.backlogSpecs(batch, recs)
	if err != nil {
		return err
	}

	for _, e := range batch {
		if err = m.writeBacklogRecord(cw, e, recs, specs); err != nil {
			return err
		}
	}

	return nil
}

// backlogSpecs returns the overlay entry of each complete record of batch, by
// its Ref, that applied as one, and reads the spec records of those entries
// into recs.
func (m *checkpointMerge) backlogSpecs(batch []backlogEntry, recs map[wal.Ref][]byte) (map[wal.Ref]*logHistory,
	error) {
	entries := make(map[wal.Ref]*logHistory)

	for _, e := range batch {
		h, err := m.appliedEntry(recs[e.ref])
		if err != nil {
			return nil, corruptAt(e.ref, err)
		}

		if h != nil {
			entries[e.ref] = h
		}
	}

	specs, err := m.s.readRecords(refsOf(slices.Collect(maps.Values(entries)),
		func(h *logHistory) wal.Ref { return h.spec }))
	maps.Copy(recs, specs)

	return entries, err
}

// appliedEntry returns the overlay entry rec applied as in m's state, if it is
// a complete record that applied as one, else nil.
func (m *checkpointMerge) appliedEntry(rec []byte) (*logHistory, error) {
	parsed, err := parseLogRecord(rec)
	if err != nil || parsed.typ != logRecComplete {
		return nil, err
	}

	return m.state.appliedAs(&foldComplete{key: parsed.key, seq: parsed.seq}), nil
}

// addPair adds head and rec, in that order, to the same block, writing the
// block being built first if they would take it over checkpointBlockBytes,
// and returns rec's Ref.
func (w *checkpointWriter) addPair(head, rec []byte) (wal.Ref, error) {
	if w.n > 0 && len(w.frames)+2*wal.FrameHeader+len(head)+len(rec) > checkpointBlockBytes {
		if err := w.flush(); err != nil {
			return wal.Ref{}, err
		}
	}

	w.frames = wal.AppendFrame(w.frames, head)
	w.n++

	ref := wal.Ref{File: w.refFile, Off: w.off, Idx: w.n}
	w.frames = wal.AppendFrame(w.frames, rec)
	w.n++

	return ref, nil
}

// readRecords returns each record refs name, whole and aliasing nothing,
// reading each frame once. The caller keeps the files they are in.
func (s *logStore) readRecords(refs iter.Seq[wal.Ref]) (map[wal.Ref][]byte, error) {
	frames, n := frameIdxs(refs)
	recs := make(map[wal.Ref][]byte, n)

	keep := func(ref wal.Ref, rec []byte) error {
		recs[ref] = slices.Clone(rec)

		return nil
	}

	for frame, idxs := range frames {
		if err := s.readFrameRecords(frame, idxs, keep); err != nil {
			return nil, err
		}
	}

	return recs, nil
}

// readBacklog reads, as readFiles reads the wal, the backlog of checkpoint
// ckpt from position from, returning where it stopped: at the first frame
// after its chunk's records, or the cut, once it has read the whole backlog.
// The caller holds a hold on ckpt's Refs (see holdRefs).
func (r *foldRead) readBacklog(ckpt uint32, from historyPos) (historyPos, error) {
	f, err := r.s.segmentFile(ckpt | checkpointRefFlag)
	if err != nil {
		return historyPos{}, err
	}

	b, err := r.s.backlogOf(f, ckpt)
	if err != nil {
		return historyPos{}, err
	}

	start, err := b.startBlock(f, from)
	if err != nil {
		return historyPos{}, err
	}

	for _, off := range b.offsets[start:] {
		stop, errb := r.readBacklogBlock(f, off, ckpt|checkpointRefFlag, from)
		if errb != nil || stop != nil {
			return derefPos(stop), errb
		}

		callHook(&logFoldBacklogBlockHook)
	}

	return historyPos{file: ckpt}, nil
}
