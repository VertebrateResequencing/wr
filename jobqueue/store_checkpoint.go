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

// This file holds the log store's checkpoints (spec "Checkpoints", C6): every
// checkpointInterval, or checkpointLogBytes of wal, the log is cut, the
// previous checkpoint is merged with the wal files before the cut into a new
// one, every in-memory Ref below the cut is swapped for its copy in the new
// checkpoint, and the files the new checkpoint makes redundant are deleted, so
// that recovery time and the log's size stay bounded and every live spec stays
// addressable through a Ref.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
)

const (
	// checkpointFileExt is the extension of a checkpoint's file name,
	// NNNNNNNN.ckpt, NNNNNNNN being its walFrom.
	checkpointFileExt = ".ckpt"

	// checkpointRefFlag is set in the file number of a Ref of a record in a
	// checkpoint, whose number is that checkpoint's walFrom, so that it is
	// never taken for wal file walFrom, which has the same number.
	checkpointRefFlag uint32 = 1 << 31

	// checkpointInterval is the most time between the starts of two
	// checkpoints, and checkpointLogBytes the most wal written between them.
	checkpointInterval = 5 * time.Minute
	checkpointLogBytes = 256 << 20

	// checkpointPoll is how often the checkpointer checks whether a
	// checkpoint is due.
	checkpointPoll = time.Second

	// checkpointBlockBytes is the most uncompressed bytes of records a
	// checkpoint's block holds, unless one record alone is larger.
	checkpointBlockBytes = 64 << 10

	// checkpointReaders is how many goroutines decode a checkpoint's blocks
	// at recovery.
	checkpointReaders = 8

	// checkpointSwapBatch is how many keys' Refs a checkpoint swaps per hold
	// of the store's lock.
	checkpointSwapBatch = 1024

	// checkpointFailedLogMsg is logged, at most once per
	// checkpointFailedLogEvery, while checkpoints fail.
	checkpointFailedLogMsg   = "checkpoint failed"
	checkpointFailedLogEvery = time.Minute

	// checkpointHeaderLen is the length of a checkpoint's header record: its
	// magic, walFrom, histFrom's file and offset, max seq, live jobs and
	// records.
	checkpointHeaderLen = 8 + 4 + 4 + 8 + 8 + 8 + 8

	// checkpointTrailerTail is the length of the end of a checkpoint's
	// trailer record after its block offsets, each checkpointOffsetLen
	// bytes: their count and the magic.
	checkpointTrailerTail = 4 + 8
	checkpointOffsetLen   = 8

	// checkpointBlocksAhead is how many blocks a recovery decodes ahead of
	// the one it applies next.
	checkpointBlocksAhead = 2 * checkpointReaders
)

var (
	// checkpointHeaderMagic starts a checkpoint's header record, and
	// checkpointTrailerMagic ends its trailer record.
	checkpointHeaderMagic  = []byte("WRCKPTHD") //nolint:gochecknoglobals // a constant
	checkpointTrailerMagic = []byte("WRCKPT01") //nolint:gochecknoglobals // a constant

	// errCheckpointInvalid is the error of reading a checkpoint file with no
	// valid trailer or header.
	errCheckpointInvalid = errors.New("checkpoint has no valid trailer")

	// errCheckpointRefsUnswapped is a checkpoint's error when an in-memory Ref
	// below its cut has no copy in it, so the files below the cut are kept.
	errCheckpointRefsUnswapped = errors.New("in-memory Refs below the cut have no copy in the checkpoint")
)

// Test seams, nil or 0 outside tests.
var (
	// logCheckpointFoldedHook is called by each checkpoint once the folded
	// position has reached its cut (spec "Checkpoints", step 2).
	logCheckpointFoldedHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logCheckpointRenameHook is called by each checkpoint once its .tmp file
	// is durable, before it is renamed; an error it returns ends the
	// checkpoint there, as a crash would.
	logCheckpointRenameHook atomic.Pointer[func() error] //nolint:gochecknoglobals // a test seam

	// logCheckpointWriteHook is called by each checkpoint as it writes its
	// .tmp file, once it has written its blocks; an error it returns fails
	// the write.
	logCheckpointWriteHook atomic.Pointer[func() error] //nolint:gochecknoglobals // a test seam

	// logCheckpointSendHook is called by each recovery's decoder of a
	// checkpoint's block i, once it has decoded it, with send, which hands
	// the block to be applied, in place of handing it itself.
	logCheckpointSendHook atomic.Pointer[func(i int, send func())] //nolint:gochecknoglobals // a test seam

	// logFoldSpecsHook is called by each fold once it has taken the spec Refs
	// of its complete records, before it reads them.
	logFoldSpecsHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logCheckpointEvery and logCheckpointBytes, if above 0, replace
	// checkpointInterval (in nanoseconds) and checkpointLogBytes.
	logCheckpointEvery atomic.Int64 //nolint:gochecknoglobals // a test seam
	logCheckpointBytes atomic.Int64 //nolint:gochecknoglobals // a test seam
)

// callCheckpointWriteHook returns the error of logCheckpointWriteHook, if it
// is set.
func callCheckpointWriteHook() error {
	if hook := logCheckpointWriteHook.Load(); hook != nil {
		return (*hook)()
	}

	return nil
}

// checkpointHeader is a checkpoint's header record: the cut it was made at,
// the durable folded position when it was merged, the highest seq it read, how
// many live jobs it holds and how many records.
type checkpointHeader struct {
	walFrom  uint32
	histFrom historyPos
	maxSeq   uint64
	live     uint64
	records  uint64
}

// parseCheckpointHeader returns the header rec holds.
func parseCheckpointHeader(rec []byte) (checkpointHeader, error) {
	if len(rec) != checkpointHeaderLen || !bytes.HasPrefix(rec, checkpointHeaderMagic) {
		return checkpointHeader{}, errCheckpointInvalid
	}

	b := rec[len(checkpointHeaderMagic):]

	return checkpointHeader{
		walFrom:  binary.LittleEndian.Uint32(b),
		histFrom: historyPos{file: binary.LittleEndian.Uint32(b[4:]), off: binary.LittleEndian.Uint64(b[8:])},
		maxSeq:   binary.LittleEndian.Uint64(b[16:]),
		live:     binary.LittleEndian.Uint64(b[24:]),
		records:  binary.LittleEndian.Uint64(b[32:]),
	}, nil
}

// readCheckpointMeta returns the header and block offsets of the checkpoint in
// f, or errCheckpointInvalid if its trailer or header is not whole and valid.
func readCheckpointMeta(f *os.File) (checkpointHeader, []uint64, error) {
	offsets, err := readCheckpointTrailer(f)
	if err != nil {
		return checkpointHeader{}, nil, err
	}

	rec, err := wal.ReadAt(f, wal.Ref{})
	if err != nil {
		return checkpointHeader{}, nil, errors.Join(errCheckpointInvalid, err)
	}

	header, err := parseCheckpointHeader(rec)

	return header, offsets, err
}

// openCheckpoint opens checkpoint n in dir and returns its header and block
// offsets, or errCheckpointInvalid if it has no valid trailer or names another
// walFrom.
func openCheckpoint(dir string, n uint32) (checkpointHeader, []uint64, *os.File, error) {
	f, err := os.Open(checkpointPath(dir, n))
	if err != nil {
		return checkpointHeader{}, nil, nil, err
	}

	header, offsets, err := readCheckpointMeta(f)
	if err == nil && header.walFrom != n {
		err = errCheckpointInvalid
	}

	if err != nil {
		return checkpointHeader{}, nil, nil, errors.Join(err, f.Close())
	}

	return header, offsets, f, nil
}

// newestCheckpoint opens the newest checkpoint in dir with a valid trailer,
// returning its number, header and block offsets, or a nil file if dir has no
// checkpoint. Checkpoint files with none valid is errStoreCorrupt.
func newestCheckpoint(dir string) (uint32, checkpointHeader, []uint64, *os.File, error) {
	numbers, err := checkpointNumbers(dir)
	if err != nil {
		return 0, checkpointHeader{}, nil, nil, err
	}

	for _, n := range slices.Backward(numbers) {
		header, offsets, f, errm := openCheckpoint(dir, n)
		if !errors.Is(errm, errCheckpointInvalid) {
			return n, header, offsets, f, errm
		}
	}

	if len(numbers) > 0 {
		err = fmt.Errorf("%w: no checkpoint in %s has a valid trailer", errStoreCorrupt, dir)
	}

	return 0, checkpointHeader{}, nil, nil, err
}

// append appends h's record to dst.
func (h *checkpointHeader) append(dst []byte) []byte {
	dst = append(dst, checkpointHeaderMagic...)
	dst = binary.LittleEndian.AppendUint32(dst, h.walFrom)
	dst = binary.LittleEndian.AppendUint32(dst, h.histFrom.file)
	dst = binary.LittleEndian.AppendUint64(dst, h.histFrom.off)
	dst = binary.LittleEndian.AppendUint64(dst, h.maxSeq)
	dst = binary.LittleEndian.AppendUint64(dst, h.live)

	return binary.LittleEndian.AppendUint64(dst, h.records)
}

// checkpointNumbers returns the numbers of the checkpoint files in dir,
// ascending.
func checkpointNumbers(dir string) ([]uint32, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var numbers []uint32

	for _, entry := range entries {
		if n, ok := checkpointNumber(entry.Name()); ok {
			numbers = append(numbers, n)
		}
	}

	slices.Sort(numbers)

	return numbers, nil
}

// checkpointNumber returns the number of the checkpoint file named name, and
// whether name is a checkpoint's.
func checkpointNumber(name string) (uint32, bool) {
	hex, ok := strings.CutSuffix(name, checkpointFileExt)
	if !ok || len(hex) != walFileDigits {
		return 0, false
	}

	n, err := strconv.ParseUint(hex, 16, 32)

	return uint32(n), err == nil
}

// removeTmpFiles deletes every file being written in dir, as one left by a
// crash is never used (spec "Recovery", step 1).
func removeTmpFiles(dir string) error {
	tmps, err := filepath.Glob(filepath.Join(dir, "*"+storeTmpExt))
	if err != nil {
		return err
	}

	var errs []error

	for _, tmp := range tmps {
		errs = append(errs, os.Remove(tmp))
	}

	return errors.Join(errs...)
}

// previousMaxSeq returns the max seq of checkpoint prev's header, or 0 if prev
// is 0, there being no previous checkpoint.
func previousMaxSeq(dir string, prev uint32) (uint64, error) {
	if prev == 0 {
		return 0, nil
	}

	header, _, f, err := openCheckpoint(dir, prev)
	if err != nil {
		return 0, err
	}

	return header.maxSeq, f.Close()
}

// checkpointWriter writes a checkpoint's records into zstd blocks of at most
// checkpointBlockBytes uncompressed, noting the new Ref of each record it
// copies.
type checkpointWriter struct {
	w       *bufio.Writer
	refFile uint32
	off     uint64
	frames  []byte
	n       uint32
	block   []byte
	offsets []uint64
	swap    map[wal.Ref]wal.Ref
}

// add adds rec to the block being built, which it first writes if rec would
// take it over checkpointBlockBytes, and returns rec's Ref.
func (w *checkpointWriter) add(rec []byte) (wal.Ref, error) {
	if w.n > 0 && len(w.frames)+wal.FrameHeader+len(rec) > checkpointBlockBytes {
		if err := w.flush(); err != nil {
			return wal.Ref{}, err
		}
	}

	ref := wal.Ref{File: w.refFile, Off: w.off, Idx: w.n}
	w.frames = wal.AppendFrame(w.frames, rec)
	w.n++

	return ref, nil
}

// copy is add of rec, a record at old, noting its new Ref as old's swap.
func (w *checkpointWriter) copy(old wal.Ref, rec []byte) error {
	ref, err := w.add(rec)
	w.swap[old] = ref

	return err
}

// flush writes the block being built, if it holds any record.
func (w *checkpointWriter) flush() error {
	if w.n == 0 {
		return nil
	}

	block, err := wal.AppendBlock(w.block[:0], wal.CodecZstd, w.frames)
	if err != nil {
		return err
	}

	if _, err = w.w.Write(block); err != nil {
		return err
	}

	w.block = block
	w.offsets = append(w.offsets, w.off)
	w.off += uint64(len(block))
	w.frames, w.n = w.frames[:0], 0

	return nil
}

// frame writes rec as a plain frame, the header's or trailer's.
func (w *checkpointWriter) frame(rec []byte) error {
	frame := wal.AppendFrame(nil, rec)
	_, err := w.w.Write(frame)
	w.off += uint64(len(frame))

	return err
}

// addAll adds each of recs.
func (w *checkpointWriter) addAll(recs [][]byte) error {
	for _, rec := range recs {
		if _, err := w.add(rec); err != nil {
			return err
		}
	}

	return nil
}

// checkpointMerge is one checkpoint's reading of the previous checkpoint and
// the wal files before its cut.
type checkpointMerge struct {
	s       *logStore
	walFrom uint32
	prev    uint32 // the previous checkpoint's walFrom, or 0
	files   []uint32
	limits  []int64
	state   logState
}

// each calls fn with every record of the previous checkpoint, then of the wal
// files before the cut, in order, until fn returns an error. It stops with
// errDBClosed once close has asked the checkpointer to stop.
func (m *checkpointMerge) each(fn func(ref wal.Ref, rec []byte) error) error {
	if err := m.eachInPrevious(fn); err != nil {
		return err
	}

	for i, n := range m.files {
		if m.s.checkpointStopping() {
			return errDBClosed
		}

		if err := m.eachInWAL(n, m.limits[i], fn); err != nil {
			return err
		}
	}

	return nil
}

// eachInPrevious calls fn with every record of the previous checkpoint, if any,
// until fn returns an error.
func (m *checkpointMerge) eachInPrevious(fn func(ref wal.Ref, rec []byte) error) error {
	if m.prev == 0 {
		return nil
	}

	_, offsets, f, err := openCheckpoint(m.s.dir, m.prev)
	if err != nil {
		return err
	}

	err = eachCheckpointRecord(f, offsets, m.prev|checkpointRefFlag, fn)

	return errors.Join(err, f.Close())
}

// eachCheckpointRecord calls fn with each record of the blocks at offsets in
// checkpoint f, whose Refs have file number refFile, until fn returns an error.
func eachCheckpointRecord(f *os.File, offsets []uint64, refFile uint32, fn func(ref wal.Ref, rec []byte) error) error {
	for _, off := range offsets {
		err := wal.ReadFrame(f, off, func(ref wal.Ref, rec []byte) error {
			ref.File = refFile

			return fn(ref, rec)
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// eachInWAL calls fn with every record of wal file n before limit, but its
// header.
func (m *checkpointMerge) eachInWAL(n uint32, limit int64, fn func(ref wal.Ref, rec []byte) error) error {
	if limit == 0 {
		return nil
	}

	f, err := os.Open(walFilePath(m.s.dir, n))
	if err != nil {
		return err
	}

	defer f.Close()

	end, err := wal.Scan(io.NewSectionReader(f, 0, limit), func(ref wal.Ref, rec []byte) error {
		if ref.Off == 0 {
			return nil
		}

		ref.File = n

		return fn(ref, rec)
	})
	if err == nil && end != limit {
		err = fmt.Errorf("%w: wal file %08x is damaged at %d, before its durable end %d", errStoreCorrupt, n, end, limit)
	}

	return err
}

// reduce applies every record m reads to m's state with the recovery reducer.
// It decodes no spec or run state: what a checkpoint holds depends only on
// each record's type, seq, key and Ref.
func (m *checkpointMerge) reduce() error {
	return m.each(func(ref wal.Ref, rec []byte) error {
		parsed, err := parseLogRecord(rec)
		if err != nil {
			return fmt.Errorf("%w: %+v: %w", errStoreCorrupt, ref, err)
		}

		op := logOp{typ: parsed.typ, seq: parsed.seq, key: parsed.key, newKey: parsed.newKey, ref: ref,
			stream: parsed.stream, hasStd: len(parsed.body) > 0, limit: parsed.limit, removed: parsed.removed,
			name: strings.Clone(parsed.name)}

		if parsed.typ == logRecEnv {
			op.blob = slices.Clone(parsed.body)
		}

		m.state.apply(&op)

		return nil
	})
}

// wanted returns the Refs of the records of m's live jobs a checkpoint copies:
// each one's spec, state and non-empty std.
func (m *checkpointMerge) wanted() map[wal.Ref]bool {
	want := make(map[wal.Ref]bool, 2*len(m.state.live)) //nolint:mnd // a spec and a state each

	for _, e := range m.state.live {
		want[e.spec] = true

		if e.stateSeq > 0 {
			want[e.state] = true
		}

		for stream, has := range e.hasStd {
			if has {
				want[e.std[stream]] = true
			}
		}
	}

	return want
}

// write writes m's checkpoint to f and syncs it: its header, a block per
// checkpointBlockBytes of records, and its trailer.
func (m *checkpointMerge) write(f *os.File, folded historyPos) (map[wal.Ref]wal.Ref, error) {
	want, unreferenced := m.wanted(), m.unreferenced()
	cw := &checkpointWriter{w: bufio.NewWriter(f), refFile: m.walFrom | checkpointRefFlag,
		swap: make(map[wal.Ref]wal.Ref, len(want))}

	header := checkpointHeader{walFrom: m.walFrom, histFrom: folded, maxSeq: m.state.maxSeq,
		live: uint64(len(m.state.live)), records: uint64(len(want) + len(unreferenced))}

	steps := []func() error{
		func() error { return cw.frame(header.append(nil)) },
		func() error { return m.copyWanted(cw, want) },
		func() error { return cw.addAll(unreferenced) },
		cw.flush,
		callCheckpointWriteHook,
		func() error { return cw.frame(checkpointTrailer(cw.offsets)) },
		cw.w.Flush,
		f.Sync,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return cw.swap, nil
}

// checkpointTrailer returns a checkpoint's trailer record: its block offsets,
// their count and the magic.
func checkpointTrailer(offsets []uint64) []byte {
	rec := make([]byte, 0, checkpointOffsetLen*len(offsets)+checkpointTrailerTail)

	for _, off := range offsets {
		rec = binary.LittleEndian.AppendUint64(rec, off)
	}

	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(offsets))) //nolint:gosec // a count

	return append(rec, checkpointTrailerMagic...)
}

// copyWanted adds to cw each record m reads whose Ref is in want.
func (m *checkpointMerge) copyWanted(cw *checkpointWriter, want map[wal.Ref]bool) error {
	return m.each(func(ref wal.Ref, rec []byte) error {
		if !want[ref] {
			return nil
		}

		return cw.copy(ref, rec)
	})
}

// unreferenced returns the records of m's state no Ref names, sorted: a std
// record clearing each cleared std stream of a live job (see stdClears), and an
// env, limit and depgroup record of each.
func (m *checkpointMerge) unreferenced() [][]byte {
	recs := m.stdClears()

	for key, blob := range m.state.envs {
		recs = append(recs, appendEnvRecord(nil, 0, key, blob))
	}

	for group, limit := range m.state.limits {
		recs = append(recs, appendLimitRecord(nil, 0, group, limit, false))
	}

	for name := range m.state.depGroupsSeen {
		recs = append(recs, appendDepGroupRecord(nil, 0, name))
	}

	slices.SortFunc(recs, bytes.Compare)

	return recs
}

// stdClears returns a std record clearing each cleared std stream of m's live
// jobs, at the seq that cleared it, so that an earlier std record appended
// after the cut is still ignored.
func (m *checkpointMerge) stdClears() [][]byte {
	var recs [][]byte

	for k, e := range m.state.live {
		for stream, seq := range e.stdSeq {
			if seq > 0 && !e.hasStd[stream] {
				recs = append(recs, appendStdRecord(nil, seq, k, uint8(stream), nil))
			}
		}
	}

	return recs
}

// findFiles sets m's files to the wal files from the previous checkpoint's
// walFrom (else the first) to the cut, and their limits to the end recovery
// gives each.
func (m *checkpointMerge) findFiles() error {
	files, err := walFileNumbers(m.s.dir)
	if err != nil {
		return err
	}

	for i, n := range files {
		if n >= m.walFrom || n < m.prev {
			continue
		}

		limit, errl := m.limit(files, i)
		if errl != nil {
			return errl
		}

		m.files, m.limits = append(m.files, n), append(m.limits, limit)
	}

	return nil
}

// limit returns the end recovery gives wal file files[i], which is before the
// cut, so a later file's header bounds it.
func (m *checkpointMerge) limit(files []uint32, i int) (int64, error) {
	limit, err := m.s.walValidEnd(files, i)
	if err == nil && limit < 0 {
		err = fmt.Errorf("%w: wal file %08x has no recorded end, before the cut %08x",
			errStoreCorrupt, files[i], m.walFrom)
	}

	return limit, err
}

// newCheckpointMerge returns the merge for a checkpoint cut at walFrom: of the
// previous checkpoint, if any, and the wal files from its walFrom (else the
// first) to the cut, each to the end recovery gives it.
func (s *logStore) newCheckpointMerge(walFrom uint32) (*checkpointMerge, error) {
	m := &checkpointMerge{s: s, walFrom: walFrom, prev: s.ckptFrom, state: newLogState()}

	// Every complete, rglookup and modify record before the cut is folded
	// (step 2), so they change only the live plane, as at recovery.
	m.state.folded = historyPos{file: walFrom}

	var err error

	// The previous checkpoint's highest seq may be of a record it does not
	// hold, such as a delete's.
	if m.state.maxSeq, err = previousMaxSeq(s.dir, m.prev); err != nil {
		return nil, err
	}

	if err = m.findFiles(); err != nil {
		return nil, err
	}

	if err = checkWALSequence(append(slices.Clone(m.files), walFrom)); err != nil {
		return nil, err
	}

	if m.prev != 0 && (len(m.files) == 0 || m.files[0] != m.prev) {
		return nil, fmt.Errorf("%w: wal file %08x is missing", errStoreCorrupt, m.prev)
	}

	return m, nil
}

// checkpointDue is when the checkpointer makes its next checkpoint: once
// checkpointInterval has passed since the last started (or the store opened),
// or checkpointLogBytes of wal have been written since, and never sooner after
// the last ended than it took.
type checkpointDue struct {
	started, ended time.Time
	took           time.Duration
	bytes          uint64
}

// now reports whether a checkpoint is due, written bytes of wal having been
// written in all.
func (d *checkpointDue) now(written uint64) bool {
	every, logBytes := checkpointInterval, uint64(checkpointLogBytes)

	if n := logCheckpointEvery.Load(); n > 0 {
		every = time.Duration(n)
	}

	if n := logCheckpointBytes.Load(); n > 0 {
		logBytes = uint64(n)
	}

	if time.Since(d.ended) < d.took {
		return false
	}

	return time.Since(d.started) >= every || written-d.bytes >= logBytes
}

// run makes a checkpoint of s if one is due, noting when it started and ended
// and how much wal had been written when it started.
func (d *checkpointDue) run(s *logStore) error {
	written := s.w.Stats().Bytes
	if !d.now(written) {
		return nil
	}

	d.started, d.bytes = time.Now(), written
	err := s.checkpoint()
	d.ended = time.Now()
	d.took = d.ended.Sub(d.started)

	return err
}

// runCheckpointer makes a checkpoint whenever one is due, until close stops
// it.
func (s *logStore) runCheckpointer() {
	defer internal.LogPanic(s.ctx, "jobqueue log store checkpointer", false)
	defer close(s.ckptExited)

	ticker := time.NewTicker(checkpointPoll)
	defer ticker.Stop()

	due := checkpointDue{started: time.Now(), bytes: s.w.Stats().Bytes}

	var failed checkpointFailures

	for {
		select {
		case <-s.ckptStop:
			return
		case <-ticker.C:
		}

		if err := due.run(s); err != nil && !errors.Is(err, errDBClosed) {
			failed.note(s.ctx, err)
		}
	}
}

// checkpointFailures counts the checkpoints that failed since one was last
// logged, and when that was.
type checkpointFailures struct {
	n      int
	logged time.Time
}

// note counts a checkpoint that failed with err, logging
// checkpointFailedLogMsg, with how many failed, if none was logged in the last
// checkpointFailedLogEvery.
func (c *checkpointFailures) note(ctx context.Context, err error) {
	c.n++

	if time.Since(c.logged) < checkpointFailedLogEvery {
		return
	}

	c.logged = time.Now()

	clog.Error(ctx, checkpointFailedLogMsg, "err", err, "failures", c.n)

	c.n = 0
}

// readCheckpointTrailer returns the block offsets the trailer of checkpoint f,
// its last frame, holds, or errCheckpointInvalid if it has none whole and
// valid.
func readCheckpointTrailer(f *os.File) ([]uint64, error) {
	start, n, err := checkpointTrailerAt(f)
	if err != nil {
		return nil, err
	}

	trailer, err := wal.ReadAt(f, wal.Ref{Off: uint64(start)}) //nolint:gosec // not negative
	if err != nil || int64(len(trailer)) != checkpointOffsetLen*n+checkpointTrailerTail {
		return nil, errors.Join(errCheckpointInvalid, err)
	}

	offsets := make([]uint64, n)
	for i := range offsets {
		offsets[i] = binary.LittleEndian.Uint64(trailer[checkpointOffsetLen*i:])
	}

	return offsets, nil
}

// checkpointTrailerAt returns where checkpoint f's trailer frame starts and how
// many block offsets it holds, from the count and magic that end it, or
// errCheckpointInvalid if f does not end in them.
func checkpointTrailerAt(f *os.File) (int64, int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}

	size := info.Size()
	if size < wal.FrameHeader+checkpointTrailerTail {
		return 0, 0, errCheckpointInvalid
	}

	var tail [checkpointTrailerTail]byte
	if _, err = f.ReadAt(tail[:], size-checkpointTrailerTail); err != nil {
		return 0, 0, err
	}

	n := int64(binary.LittleEndian.Uint32(tail[:]))
	start := size - wal.FrameHeader - checkpointTrailerTail - checkpointOffsetLen*n

	if !bytes.Equal(tail[4:], checkpointTrailerMagic) || start < 0 {
		return 0, 0, errCheckpointInvalid
	}

	return start, n, nil
}

// recordFilePath returns the path of the file holding the records whose Refs
// have file number file in dir: a checkpoint's, if checkpointRefFlag is set,
// else a wal file's.
func recordFilePath(dir string, file uint32) string {
	if file&checkpointRefFlag != 0 {
		return checkpointPath(dir, file&^checkpointRefFlag)
	}

	return walFilePath(dir, file)
}

// checkpointPath returns the path of checkpoint n, the one whose walFrom is n,
// in dir.
func checkpointPath(dir string, n uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%0*x%s", walFileDigits, n, checkpointFileExt))
}

// refBelow reports whether ref is a record's Ref in a file a checkpoint cut at
// walFrom makes redundant: a wal file before walFrom or an older checkpoint.
func refBelow(ref wal.Ref, walFrom uint32) bool {
	return ref != (wal.Ref{}) && ref.File&^checkpointRefFlag < walFrom
}

// swappedHistory returns h, or a copy of it with its Refs passed through
// swapped if that changes them.
func swappedHistory(h *logHistory, swapped func(wal.Ref) wal.Ref) *logHistory {
	spec, complete := swapped(h.spec), swapped(h.complete)
	if spec == h.spec && complete == h.complete {
		return h
	}

	copied := *h
	copied.spec, copied.complete = spec, complete

	return &copied
}

// feedCheckpointBlocks sends each of n blocks' indexes in turn to blocks, once
// ahead has room for it, until stop is closed, then closes blocks.
func feedCheckpointBlocks(n int, blocks chan<- int, ahead chan<- struct{}, stop <-chan struct{}) {
	defer close(blocks)

	for i := range n {
		select {
		case ahead <- struct{}{}:
		case <-stop:
			return
		}

		blocks <- i
	}
}

// publishCheckpoint renames the durable checkpoint file tmp to path, in dir,
// and makes the rename durable. A failed rename removes tmp.
func publishCheckpoint(dir, tmp, path string) error {
	if hook := logCheckpointRenameHook.Load(); hook != nil {
		if err := (*hook)(); err != nil {
			return err
		}
	}

	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}

	return syncDir(dir)
}

// cutAndFold makes a checkpoint's cut and folds to it (spec "Checkpoints",
// steps 1 and 2): it rotates the writer, waits until every write with records
// before the cut has applied, then folds to the writer's durable end, which
// the rotation took past the cut. It returns the cut and the folded position.
func (s *logStore) cutAndFold() (uint32, historyPos, error) {
	sealed, err := s.w.Rotate()
	if errors.Is(err, wal.ErrClosed) {
		return 0, historyPos{}, errDBClosed
	}

	if err != nil {
		return 0, historyPos{}, err
	}

	s.awaitAppends()

	if err = s.foldNow(); err != nil {
		return 0, historyPos{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return sealed + 1, s.state.folded, nil
}

// swapRefs passes e's Refs in use through swapped: its spec, its state if it
// has one and each std stream's, which a cleared stream has none of.
func (e *logLive) swapRefs(swapped func(wal.Ref) wal.Ref) {
	e.spec = swapped(e.spec)

	if e.stateSeq > 0 {
		e.state = swapped(e.state)
	}

	for stream, ref := range e.std {
		e.std[stream] = swapped(ref)
	}
}

// loadCheckpoint applies the records of the newest checkpoint in the store's
// directory with a valid trailer to the store's state (spec "Recovery", step
// 2), decoding its blocks with checkpointReaders goroutines, and returns its
// walFrom, or 0 if there is no checkpoint. Checkpoint files with none valid is
// errStoreCorrupt.
func (s *logStore) loadCheckpoint() (uint32, error) {
	n, header, offsets, f, err := newestCheckpoint(s.dir)
	if err != nil || f == nil {
		return 0, err
	}

	err = s.applyCheckpoint(f, offsets, n|checkpointRefFlag)
	if err = errors.Join(err, f.Close()); err != nil {
		return 0, fmt.Errorf("%w: checkpoint %08x: %w", errStoreCorrupt, n, err)
	}

	s.state.maxSeq = max(s.state.maxSeq, header.maxSeq)

	return n, nil
}

// checkpointOps is the ops of a checkpoint's block i, or the error decoding it.
type checkpointOps struct {
	i   int
	ops []logOp
	err error
}

// applyCheckpoint applies the records of checkpoint f's blocks at offsets,
// whose Refs have file number refFile, to the store's state, in order: a block
// may hold a key's state record and an earlier one its spec, which the state
// record needs to find live. Up to checkpointReaders goroutines decode blocks
// at once, at most twice as many blocks ahead of the one applied next.
func (s *logStore) applyCheckpoint(f *os.File, offsets []uint64, refFile uint32) error {
	blocks := make(chan int)
	decoded := make(chan checkpointOps)
	ahead := make(chan struct{}, checkpointBlocksAhead)
	stop := make(chan struct{})

	go func() {
		var wg sync.WaitGroup

		for range min(checkpointReaders, len(offsets)) {
			wg.Go(func() { s.decodeCheckpointBlocks(f, offsets, refFile, blocks, decoded) })
		}

		feedCheckpointBlocks(len(offsets), blocks, ahead, stop)
		wg.Wait()
		close(decoded)
	}()

	err := s.applyCheckpointBlocks(decoded, ahead)

	close(stop)

	for range decoded {
		// Drain the decoders, so they end.
	}

	return err
}

// decodeCheckpointBlocks sends to decoded the ops of each block of checkpoint f
// whose index blocks gives, until blocks is closed.
func (s *logStore) decodeCheckpointBlocks(f *os.File, offsets []uint64, refFile uint32, blocks <-chan int,
	decoded chan<- checkpointOps) {
	var r logReplayer

	for i := range blocks {
		block := s.decodeCheckpointBlock(&r, f, i, offsets[i], refFile)
		send := func() { decoded <- block }

		if hook := logCheckpointSendHook.Load(); hook != nil {
			(*hook)(i, send)

			continue
		}

		send()
	}
}

// decodeCheckpointBlock returns the ops of the records of the checkpoint block
// i at off in f.
func (s *logStore) decodeCheckpointBlock(r *logReplayer, f *os.File, i int, off uint64,
	refFile uint32) checkpointOps {
	block := checkpointOps{i: i}

	block.err = wal.ReadFrame(f, off, func(ref wal.Ref, rec []byte) error {
		ref.File = refFile

		op, err := r.op(rec, ref, s.ch)
		block.ops = append(block.ops, op)

		return err
	})

	return block
}

// applyCheckpointBlocks applies the ops of each block decoded gives, in block
// order, freeing a place in ahead as it applies each, until decoded is closed
// or a block failed to decode.
func (s *logStore) applyCheckpointBlocks(decoded <-chan checkpointOps, ahead <-chan struct{}) error {
	waiting := make(map[int]checkpointOps)
	next := 0

	for block := range decoded {
		if block.err != nil {
			return block.err
		}

		waiting[block.i] = block

		for ready, ok := waiting[next]; ok; ready, ok = waiting[next] {
			for i := range ready.ops {
				s.state.apply(&ready.ops[i])
			}

			delete(waiting, next)
			next++

			<-ahead
		}
	}

	return nil
}

// checkpoint makes a checkpoint (spec "Checkpoints"): it rotates the writer,
// so the new file is the cut walFrom; folds the log to the cut; merges the
// previous checkpoint and the wal files before the cut into a new one, renamed
// into place once durable; swaps every in-memory Ref below the cut for its copy
// in it; then, once no reader holds a Ref it swapped, deletes the older
// checkpoint and the wal files before both the cut and the folded position.
// Checkpoints are made one at a time. A failed fold fails the checkpoint, so it
// is skipped while the folder is failing.
func (s *logStore) checkpoint() error {
	s.ckptMu.Lock()
	defer s.ckptMu.Unlock()

	walFrom, folded, err := s.cutAndFold()
	if err != nil {
		return err
	}

	callHook(&logCheckpointFoldedHook)

	swap, err := s.writeCheckpoint(walFrom, folded)
	if err != nil {
		return err
	}

	s.ckptFrom = walFrom

	if unswapped := s.swapRefs(swap, walFrom); unswapped > 0 {
		return fmt.Errorf("%w: %d", errCheckpointRefsUnswapped, unswapped)
	}

	s.awaitRefReaders()

	return s.deleteRedundant(walFrom, folded)
}

// writeCheckpoint writes the checkpoint cut at walFrom, its folded position
// folded, merging the previous checkpoint with the wal files before the cut
// (spec "Checkpoints", steps 3 and 4 up to the rename), and returns the new Ref
// of each record it copied, by its old Ref.
func (s *logStore) writeCheckpoint(walFrom uint32, folded historyPos) (map[wal.Ref]wal.Ref, error) {
	m, err := s.newCheckpointMerge(walFrom)
	if err != nil {
		return nil, err
	}

	if err = m.reduce(); err != nil {
		return nil, err
	}

	path := checkpointPath(s.dir, walFrom)
	tmp := path + storeTmpExt

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, dbFilePermission)
	if err != nil {
		return nil, err
	}

	swap, err := m.write(f, folded)
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, errors.Join(err, os.Remove(tmp))
	}

	return swap, publishCheckpoint(s.dir, tmp, path)
}

// swapRefs swaps every Ref of the in-memory state below walFrom for its copy in
// swap (spec "Checkpoints", step 4): each live key's spec, state and std, and
// the spec of each overlay entry and replaced entry. It looks at every key live
// in memory, not only those live at the cut, so that a Ref the checkpoint has
// no copy of is found even if memory differs from what the checkpoint replayed.
// A key made live after it collects the keys has Refs after the cut, as every
// write with records before the cut has applied (cutAndFold). It works through
// the keys in batches, each under the store's lock, so that a reader holding a
// Ref it copied before the swap has its hold in the generation before it.
// Overlay and replaced entries are replaced, never changed, as queries copy
// them. It returns how many Refs below walFrom it found no copy of, so that the
// files they name must be kept.
func (s *logStore) swapRefs(swap map[wal.Ref]wal.Ref, walFrom uint32) int {
	s.mu.RLock()
	keys := slices.Concat(slices.Collect(maps.Keys(s.state.live)), slices.Collect(maps.Keys(s.state.overlay)),
		slices.Collect(maps.Keys(s.state.replaced)))
	s.mu.RUnlock()

	slices.SortFunc(keys, func(a, b logKey) int { return bytes.Compare(a[:], b[:]) })
	keys = slices.Compact(keys)

	unswapped := 0

	for batch := range slices.Chunk(keys, checkpointSwapBatch) {
		s.mu.Lock()

		for _, k := range batch {
			unswapped += s.state.swapRefs(k, swap, walFrom)
		}

		s.mu.Unlock()
	}

	return unswapped
}

// swapRefs is logStore.swapRefs of key k.
func (st *logState) swapRefs(k logKey, swap map[wal.Ref]wal.Ref, walFrom uint32) int {
	unswapped := 0

	swapped := func(ref wal.Ref) wal.Ref {
		if !refBelow(ref, walFrom) {
			return ref
		}

		to, ok := swap[ref]
		if !ok {
			unswapped++

			return ref
		}

		return to
	}

	if e := st.live[k]; e != nil {
		e.swapRefs(swapped)
	}

	if h := st.overlay[k]; h != nil {
		st.overlay[k] = swappedHistory(h, swapped)
	}

	for i, h := range st.replaced[k] {
		st.replaced[k][i] = swappedHistory(h, swapped)
	}

	return unswapped
}

// deleteRedundant deletes the checkpoints before walFrom and the wal files
// before both walFrom and folded's file (spec "Recovery", wal deletion rule),
// closing any of them open for reading. No reader holds them: the caller waited
// for every reader that might.
func (s *logStore) deleteRedundant(walFrom uint32, folded historyPos) error {
	files, err := walFileNumbers(s.dir)
	if err != nil {
		return err
	}

	ckpts, err := checkpointNumbers(s.dir)
	if err != nil {
		return err
	}

	var errs []error

	for _, n := range files {
		if n < min(walFrom, folded.file) {
			errs = append(errs, s.removeRecordFile(n))
		}
	}

	for _, n := range ckpts {
		if n < walFrom {
			errs = append(errs, s.removeRecordFile(n|checkpointRefFlag))
		}
	}

	return errors.Join(errs...)
}

// removeRecordFile closes the file of Refs with file number file, if it is open
// for reading, and deletes it.
func (s *logStore) removeRecordFile(file uint32) error {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()

	var err error

	if f := s.files[file]; f != nil {
		err = f.Close()

		delete(s.files, file)
	}

	return errors.Join(err, os.Remove(recordFilePath(s.dir, file)))
}

// startCheckpointer starts the checkpointer goroutine, which close stops.
func (s *logStore) startCheckpointer() {
	s.ckptStop = make(chan struct{})
	s.ckptExited = make(chan struct{})

	go s.runCheckpointer()
}

// checkpointStopping reports whether close has asked the checkpointer to stop.
func (s *logStore) checkpointStopping() bool {
	select {
	case <-s.ckptStop:
		return true
	default:
		return false
	}
}

// stopCheckpointer asks the checkpointer to stop, which a checkpoint it is
// making notices between the files it reads, so that close never waits for a
// whole merge. The caller then waits for ckptExited.
func (s *logStore) stopCheckpointer() {
	close(s.ckptStop)
}
