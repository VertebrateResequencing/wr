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

// This file replays the log store's wal files into memory when it opens (spec
// "Recovery", steps 3 to 6, without checkpoints yet).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/ugorji/go/codec"
)

const (
	// walFileExt is the extension of a wal file's name, NNNNNNNN.wal.
	walFileExt = ".wal"

	// walFileDigits is how many hex digits a wal file's number has.
	walFileDigits = 8

	// walTailTruncatedLogMsg is logged at warn for each wal file whose damaged
	// or abandoned tail recovery truncates.
	walTailTruncatedLogMsg = "recovery truncated a wal file's damaged tail"

	// walTornHeaderLogMsg is logged at warn when recovery removes a last wal
	// file whose header was never made durable.
	walTornHeaderLogMsg = "recovery removed a wal file with a torn header"
)

var (
	// errStoreCorrupt is the error of opening a store whose log is damaged
	// before its durable end.
	errStoreCorrupt = errors.New("store is corrupt")

	// errReplayStop ends a wal file's scan at its valid end.
	errReplayStop = errors.New("end of valid wal")
)

// walHeaderLen is the length of a wal file's header record.
const walHeaderLen = 8 + 4 + 8

// walLenFlagsLen is the length of a frame's lenFlags, its first field.
const walLenFlagsLen = 4

// logReplayer parses records into the ops that apply them, reusing a Job to
// decode spec records into.
type logReplayer struct {
	job Job
	rs  logRunState
}

// op returns the op applying rec, at ref. The op aliases nothing of rec.
func (r *logReplayer) op(rec []byte, ref wal.Ref, ch codec.Handle) (logOp, error) {
	parsed, err := parseLogRecord(rec)
	if err != nil {
		return logOp{}, err
	}

	op := logOp{typ: parsed.typ, seq: parsed.seq, key: parsed.key, newKey: parsed.newKey, ref: ref, stream: parsed.stream,
		hasStd: len(parsed.body) > 0, limit: parsed.limit, removed: parsed.removed, name: strings.Clone(parsed.name)}

	switch parsed.typ {
	case logRecSpec:
		err = r.specIndex(&op, parsed.body, ch)
	case logRecState, logRecComplete:
		err = r.runState(&op, parsed.body)
	case logRecEnv:
		op.blob = slices.Clone(parsed.body)
	default:
	}

	return op, err
}

// runState sets what op, a state or complete record's, keeps of its run state,
// from the record's body, as addState sets it.
func (r *logReplayer) runState(op *logOp, body []byte) error {
	if err := decodeFlatRunState(body, &r.rs); err != nil {
		return err
	}

	op.handedOut = runStateHandedOut(r.rs.State, r.rs.Attempts, r.rs.StartTime)
	op.endTime = r.rs.EndTime

	return nil
}

// specIndex sets op's index from a spec record's body.
func (r *logReplayer) specIndex(op *logOp, body []byte, ch codec.Handle) error {
	r.job = Job{}

	if err := decodeFlatSpec(body, &r.job, ch); err != nil {
		return err
	}

	op.index = newLogSpecIndex(&r.job).cloned()

	return nil
}

// walFileReplay applies the records of wal file n up to limit (if not -1) or
// its first damage.
type walFileReplay struct {
	s        *logStore
	n        uint32
	limit    int64
	header   bool // its header, which replayAt checked, is yet to be skipped
	replayer logReplayer
}

// scan applies f's records, returning the end of the last whole frame applied.
func (r *walFileReplay) scan(f *os.File) (int64, error) {
	end, err := wal.Scan(f, r.record)
	if errors.Is(err, errReplayStop) {
		return end, nil
	}

	return end, err
}

// record is scan's wal.Scan func: it skips the header, then applies each
// record before limit.
func (r *walFileReplay) record(ref wal.Ref, rec []byte) error {
	if r.limit >= 0 && int64(ref.Off) >= r.limit { //nolint:gosec // an offset
		return errReplayStop
	}

	if r.header {
		r.header = false

		return nil
	}

	ref.File = r.n

	op, err := r.replayer.op(rec, ref, r.s.ch)
	if err != nil {
		return fmt.Errorf("%w: wal file %08x at %d: %w", errStoreCorrupt, r.n, ref.Off, err)
	}

	r.s.state.apply(&op)

	return nil
}

// replayFile applies the records of wal file n up to limit (or its first
// damage if limit is -1), then truncates anything after its valid end. It
// reports whether it removed the file, a last file with a torn header.
func (s *logStore) replayFile(ctx context.Context, n uint32, limit int64, last bool) (bool, error) {
	path := walFilePath(s.dir, n)

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}

	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, err
	}

	replay := walFileReplay{s: s, n: n, limit: limit, header: true}

	end, err := replay.scan(f)
	if err != nil {
		return false, err
	}

	return settleWALEnd(ctx, f, info.Size(), end, limit, last)
}

// walFilePath returns the path of wal file n in dir.
func walFilePath(dir string, n uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%0*x%s", walFileDigits, n, walFileExt))
}

// settleWALEnd deals with what follows the valid end of wal file f, of size
// bytes, whose records were applied up to end: damage before limit is
// errStoreCorrupt; a file with no recorded end (limit -1) and no whole header
// is removed, which it reports, if it is the last file, and errStoreCorrupt
// otherwise; anything else after end is truncated.
func settleWALEnd(ctx context.Context, f *os.File, size, end, limit int64, last bool) (bool, error) {
	if err := walEndCorrupt(f.Name(), end, limit, last); err != nil {
		return false, err
	}

	switch {
	case limit < 0 && end == 0:
		clog.Warn(ctx, walTornHeaderLogMsg, "file", f.Name(), "bytes", size)

		return true, os.Remove(f.Name())
	case size > end:
		clog.Warn(ctx, walTailTruncatedLogMsg, "file", f.Name(), "bytes", size-end)

		return false, f.Truncate(end)
	default:
		return false, nil
	}
}

// walEndCorrupt returns errStoreCorrupt if wal file name, whose records were
// applied up to end, is damaged before limit, or, with no recorded end (limit
// -1) and no whole header, is not the last file.
func walEndCorrupt(name string, end, limit int64, last bool) error {
	switch {
	case limit >= 0 && end < limit:
		return fmt.Errorf("%w: wal file %s is damaged at %d, before its durable end %d",
			errStoreCorrupt, name, end, limit)
	case limit < 0 && end == 0 && !last:
		return fmt.Errorf("%w: wal file %s has no header", errStoreCorrupt, name)
	default:
		return nil
	}
}

// walFileNumbers returns the numbers of the wal files in dir, ascending.
func walFileNumbers(dir string) ([]uint32, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var numbers []uint32

	for _, entry := range entries {
		hex, ok := strings.CutSuffix(entry.Name(), walFileExt)
		if !ok || len(hex) != walFileDigits {
			continue
		}

		n, errp := strconv.ParseUint(hex, 16, 32)
		if errp == nil {
			numbers = append(numbers, uint32(n))
		}
	}

	slices.Sort(numbers)

	return numbers, nil
}

// checkWALSequence returns errStoreCorrupt if files, ascending wal file
// numbers, skip a number. The writer never skips one (spec B2), so a missing
// file is one lost.
func checkWALSequence(files []uint32) error {
	for i := 1; i < len(files); i++ {
		if files[i] != files[i-1]+1 {
			return fmt.Errorf("%w: wal file %08x is missing", errStoreCorrupt, files[i-1]+1)
		}
	}

	return nil
}

// readWALPrefix returns the first n bytes of the file at path, or all of it if
// it is shorter.
func readWALPrefix(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer f.Close()

	buf := make([]byte, n)

	read, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}

	return buf[:read], nil
}

// walLengthNotHeader reports whether prefix, the start of a wal file, holds the
// length field of a first frame that is not a header: one neither a header's
// length nor zero, as a torn header's is.
func walLengthNotHeader(prefix []byte) bool {
	if len(prefix) < walLenFlagsLen {
		return false
	}

	lenFlags := binary.LittleEndian.Uint32(prefix)

	return lenFlags != 0 && lenFlags != walHeaderLen
}

// walValidEnd returns the durable end of wal file files[i] that the first
// later file with a whole header records: the end it gives if it names
// files[i], or 0 if it names an earlier file, skipping files[i] as one a failed
// open spent (spec B2), which holds nothing durable. A later header naming a
// file after files[i] is errStoreCorrupt. If no later file has a whole header,
// files[i]'s end is unknown (-1), so it is valid up to its first damage, as a
// last file is. Usually files[i] was sealed whole before the next was started;
// but in the residual window (spec B2), where files[i] was abandoned and no
// later open made a header durable, it may hold a failed batch's records,
// which are then replayed.
func (s *logStore) walValidEnd(files []uint32, i int) (int64, error) {
	prev := files[i]

	for _, n := range files[i+1:] {
		prevFile, prevEnd, whole, err := s.walHeader(n)

		switch {
		case err != nil:
			return 0, err
		case !whole:
			continue
		case prevFile == prev:
			return prevEnd, nil
		case prevFile < prev:
			return 0, nil
		default:
			return 0, fmt.Errorf("%w: wal file %08x has no header naming %08x", errStoreCorrupt, n, prev)
		}
	}

	return -1, nil
}

// walHeader returns the previous file and its durable end that wal file n's
// header records, and whether n starts with a whole frame at all. A frame that
// is not a header is errStoreCorrupt, whether whole or not: a torn header's
// length field is a header's or zero, so any other length means a record that
// is not a header.
func (s *logStore) walHeader(n uint32) (uint32, int64, bool, error) {
	prefix, err := readWALPrefix(walFilePath(s.dir, n), wal.FrameHeader+walHeaderLen)
	if err != nil {
		return 0, 0, false, err
	}

	if walLengthNotHeader(prefix) {
		return 0, 0, true, fmt.Errorf("%w: wal file %08x does not start with a header", errStoreCorrupt, n)
	}

	var header []byte

	_, err = wal.Scan(bytes.NewReader(prefix), func(_ wal.Ref, rec []byte) error {
		header = slices.Clone(rec)

		return errReplayStop
	})
	if err != nil && !errors.Is(err, errReplayStop) {
		return 0, 0, false, err
	}

	if header == nil {
		return 0, 0, false, nil
	}

	prevFile, prevEnd, ok := wal.ParseHeader(header)
	if !ok {
		return 0, 0, true, fmt.Errorf("%w: wal file %08x does not start with a header", errStoreCorrupt, n)
	}

	return prevFile, int64(prevEnd), true, nil //nolint:gosec // a file size
}

// liveJobs returns every live job, as its latest durable spec and state, in key
// order. It reads the wal files through once, rather than each job's records
// through their Refs, since a block's records can only be read by
// decompressing the whole block.
func (s *logStore) liveJobs() ([]*Job, error) {
	want := s.liveRefs()

	found := make(map[wal.Ref][]byte, len(want))

	files, err := walFileNumbers(s.dir)
	if err != nil {
		return nil, err
	}

	for _, n := range files {
		if err = s.collectRecords(n, want, found); err != nil {
			return nil, err
		}
	}

	return s.jobsOf(found)
}

// liveRefs returns the Refs of every live key's spec and state records.
func (s *logStore) liveRefs() map[wal.Ref]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	want := make(map[wal.Ref]bool, 2*len(s.state.live)) //nolint:mnd // a spec and a state each

	for _, e := range s.state.live {
		want[e.spec], want[e.state] = true, true
	}

	return want
}

// collectRecords copies into found the body after the key of each record of
// wal file n whose Ref is in want.
func (s *logStore) collectRecords(n uint32, want map[wal.Ref]bool, found map[wal.Ref][]byte) error {
	f, err := s.segmentFile(n)
	if err != nil {
		return err
	}

	_, err = wal.Scan(io.NewSectionReader(f, 0, math.MaxInt64), func(ref wal.Ref, rec []byte) error {
		ref.File = n
		if !want[ref] {
			return nil
		}

		parsed, errp := parseLogRecord(rec)
		if errp != nil {
			return errp
		}

		found[ref] = slices.Clone(parsed.body)

		return nil
	})

	return err
}

// jobsOf returns, in key order, the job of each live key, from its spec and
// state record bodies in found.
func (s *logStore) jobsOf(found map[wal.Ref][]byte) ([]*Job, error) {
	s.mu.RLock()
	keys := slices.SortedFunc(maps.Keys(s.state.live), func(a, b logKey) int { return bytes.Compare(a[:], b[:]) })
	refs := make([][2]wal.Ref, len(keys))

	for i, k := range keys {
		e := s.state.live[k]
		refs[i] = [2]wal.Ref{e.spec, e.state}
	}

	s.mu.RUnlock()

	jobs := make([]*Job, 0, len(keys))

	for i, k := range keys {
		spec, state := found[refs[i][0]], found[refs[i][1]]
		if spec == nil || state == nil {
			return nil, fmt.Errorf("%w: live job %s has no record", errStoreCorrupt, k)
		}

		job, err := s.jobOf(spec, state)
		if err != nil {
			return nil, err
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

// replay applies every record of the valid part of each wal file in the
// store's directory to its state, truncating damaged or abandoned tails, and
// returns the number the next wal file must have.
//
// A file followed by another is valid up to the durable end the first later
// file with a whole header gives it (none, if that header skips it as spent);
// anything after is the tail of an abandoned file and is truncated, while
// damage before it is errStoreCorrupt. The last file is valid
// up to its first damage, its torn tail truncated, or, if its header itself was
// never made durable, it holds nothing durable and is removed.
func (s *logStore) replay(ctx context.Context) (uint32, error) {
	files, err := walFileNumbers(s.dir)
	if err != nil || len(files) == 0 {
		return 1, err
	}

	if err = checkWALSequence(files); err != nil {
		return 0, err
	}

	for i, n := range files {
		removed, errf := s.replayAt(ctx, files, i)
		if errf != nil {
			return 0, errf
		}

		if removed {
			return n, nil
		}
	}

	return files[len(files)-1] + 1, nil
}

// replayAt is replayFile of files[i], valid up to the end a later file's
// header gives it (see walValidEnd). Every file is first checked to start with
// a header or a torn one (spec C5), even one that limit 0 skips as spent, whose
// replay stops before reading its header: records with no header are
// errStoreCorrupt, not a spent file's tail to truncate.
func (s *logStore) replayAt(ctx context.Context, files []uint32, i int) (bool, error) {
	limit, err := s.walValidEnd(files, i)
	if err != nil {
		return false, err
	}

	if _, _, _, err = s.walHeader(files[i]); err != nil {
		return false, err
	}

	return s.replayFile(ctx, files[i], limit, i == len(files)-1)
}
