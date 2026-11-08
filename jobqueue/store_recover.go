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
// "Recovery", steps 3 to 6), from the walFrom of the checkpoint it loaded
// (store_checkpoint.go).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	// walFoldedRewoundLogMsg is logged at warn when recovery finds the log's
	// valid end before history.bolt's folded position, and moves it back.
	walFoldedRewoundLogMsg = "recovery moved history's folded position back to the log's damaged end"

	// walFromAbsentLogMsg is logged at warn when recovery finds no wal file
	// from the checkpoint's walFrom on, with history.bolt's folded position at
	// that file's start: an earlier recovery removed the file as torn and moved
	// the folded position back to it, and the store closed before writing it
	// again.
	walFromAbsentLogMsg = "recovery found the checkpoint's walFrom wal file absent after a torn header was removed"
)

var (
	// errStoreCorrupt is the error of opening a store whose log is damaged
	// before its durable end.
	errStoreCorrupt = errors.New("store is corrupt")

	// errReplayStop ends a wal file's scan at its valid end.
	errReplayStop = errors.New("end of valid wal")

	// errWALFromMissing is walFilesFrom's error, wrapping errStoreCorrupt, when
	// the checkpoint's walFrom wal file is not there.
	errWALFromMissing = fmt.Errorf("%w: the checkpoint's walFrom wal file is missing", errStoreCorrupt)
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
// damage if limit is -1), then truncates anything after its valid end, which
// it returns. It reports whether the file is a last file with a torn header,
// which the caller removes once the folded position is not past it (see
// replayFromCheckpoint). Before it truncates a file or reports it torn it notes
// the end it leaves (0 for torn) durably (see noteRecoveryCut), so the first
// incremental backup tick makes the backup's copy of it match (spec E4).
func (s *logStore) replayFile(ctx context.Context, n uint32, limit int64, last bool) (bool, int64, error) {
	f, err := os.OpenFile(walFilePath(s.dir, n), os.O_RDWR, 0)
	if err != nil {
		return false, 0, err
	}

	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, 0, err
	}

	replay := walFileReplay{s: s, n: n, limit: limit, header: true}

	end, err := replay.scan(f)
	if err != nil {
		return false, 0, err
	}

	torn, err := settleWALEnd(ctx, f, info.Size(), end, limit, last,
		func(at int64) error { return s.noteRecoveryCut(n, at) })

	return torn, end, err
}

// walFilePath returns the path of wal file n in dir.
func walFilePath(dir string, n uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%0*x%s", walFileDigits, n, walFileExt))
}

// settleWALEnd deals with what follows the valid end of wal file f, of size
// bytes, whose records were applied up to end: damage before limit is
// errStoreCorrupt; a file with no recorded end (limit -1) and no whole header
// is torn, which it reports for the caller to remove, if it is the last file,
// and errStoreCorrupt otherwise; anything else after end is truncated. It calls
// cut with the end it leaves the file at, 0 for a torn one, before it
// truncates it or reports it torn.
func settleWALEnd(ctx context.Context, f *os.File, size, end, limit int64, last bool,
	cut func(at int64) error,
) (bool, error) {
	if err := walEndCorrupt(f.Name(), end, limit, last); err != nil {
		return false, err
	}

	switch {
	case limit < 0 && end == 0:
		if err := checkTornHeader(f, size); err != nil {
			return false, err
		}

		clog.Warn(ctx, walTornHeaderLogMsg, "file", f.Name(), "bytes", size)

		return true, cut(0)
	case size > end:
		return false, truncateWALTail(ctx, f, size, end, cut)
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

// checkTornHeader returns errStoreCorrupt for f, a last wal file of size bytes
// with no whole header, if any byte after a header's length is not zero. A
// header is written and made durable before any record, so a torn one is
// followed by nothing, or by zeros a crash left; a header failing its CRC
// followed by a non-zero byte is damage, not a header that was never durable.
func checkTornHeader(f *os.File, size int64) error {
	const headerSize = wal.FrameHeader + walHeaderLen

	if size <= headerSize {
		return nil
	}

	data, err := io.ReadAll(io.NewSectionReader(f, headerSize, size-headerSize))
	if err != nil {
		return err
	}

	if slices.ContainsFunc(data, func(b byte) bool { return b != 0 }) {
		return fmt.Errorf("%w: wal file %s starts with a damaged header, followed by %d bytes", errStoreCorrupt,
			f.Name(), size-headerSize)
	}

	return nil
}

// truncateWALTail truncates f, of size bytes, at end, its valid end, having
// first called cut with end.
func truncateWALTail(ctx context.Context, f *os.File, size, end int64, cut func(at int64) error) error {
	clog.Warn(ctx, walTailTruncatedLogMsg, "file", f.Name(), "bytes", size-end)

	if err := cut(end); err != nil {
		return err
	}

	if err := f.Truncate(end); err != nil {
		return err
	}

	return walTruncated()
}

// walTruncated calls logWALTruncatedHook, if set, returning its error.
func walTruncated() error {
	if hook := logWALTruncatedHook.Load(); hook != nil {
		return (*hook)()
	}

	return nil
}

// walFilesFrom returns the numbers of the wal files in dir numbered from on,
// ascending, checking that none is missing, from included if it is not 0.
func walFilesFrom(dir string, from uint32) ([]uint32, error) {
	files, err := walFileNumbers(dir)
	if err != nil {
		return nil, err
	}

	files = slices.DeleteFunc(files, func(n uint32) bool { return n < from })

	if from != 0 && (len(files) == 0 || files[0] != from) {
		return files, fmt.Errorf("%w: wal file %08x", errWALFromMissing, from)
	}

	return files, checkWALSequence(files)
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

// refFrames returns the offset of each frame holding one of refs' records, by
// file number.
func refFrames(refs map[wal.Ref]bool) map[uint32]map[uint64]bool {
	frames := make(map[uint32]map[uint64]bool)

	for ref := range refs {
		if frames[ref.File] == nil {
			frames[ref.File] = make(map[uint64]bool)
		}

		frames[ref.File][ref.Off] = true
	}

	return frames
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
// order. It reads each wal file holding one of their records through once,
// rather than each record through its Ref, since a block's records can only be
// read by decompressing the whole block; a checkpoint's blocks it reads one by
// one, each once. A record whose file is missing is errStoreCorrupt.
func (s *logStore) liveJobs() ([]*Job, error) {
	keys, refs, gen := s.liveRefs()
	defer s.releaseRefs(gen)

	want := make(map[wal.Ref]bool, 2*len(refs)) //nolint:mnd // a spec and a state each

	for _, r := range refs {
		want[r[0]], want[r[1]] = true, true
	}

	found, err := s.collectRecords(want)
	if err != nil {
		return nil, err
	}

	return s.jobsOf(keys, refs, found)
}

// liveRefs returns, in key order, every live key and the Refs of its spec and
// state records, with the hold (see holdRefs) the caller releases once it has
// read them.
func (s *logStore) liveRefs() ([]logKey, [][2]wal.Ref, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := slices.SortedFunc(maps.Keys(s.state.live), func(a, b logKey) int { return bytes.Compare(a[:], b[:]) })
	refs := make([][2]wal.Ref, len(keys))

	for i, k := range keys {
		e := s.state.live[k]
		refs[i] = [2]wal.Ref{e.spec, e.state}
	}

	return keys, refs, s.holdRefs()
}

// collectRecords returns the body after the key of each record whose Ref is in
// want.
func (s *logStore) collectRecords(want map[wal.Ref]bool) (map[wal.Ref][]byte, error) {
	found := make(map[wal.Ref][]byte, len(want))
	frames := refFrames(want)

	keep := func(file uint32) func(ref wal.Ref, rec []byte) error {
		return func(ref wal.Ref, rec []byte) error {
			ref.File = file
			if !want[ref] {
				return nil
			}

			parsed, err := parseLogRecord(rec)
			if err != nil {
				return err
			}

			found[ref] = slices.Clone(parsed.body)

			return nil
		}
	}

	for _, file := range slices.Sorted(maps.Keys(frames)) {
		if err := s.collectFileRecords(file, slices.Sorted(maps.Keys(frames[file])), keep(file)); err != nil {
			return nil, err
		}
	}

	return found, nil
}

// collectFileRecords calls fn with the records of the file of Refs with file
// number file: each of a checkpoint's frames at offs, or the whole of a wal
// file. A missing file is errStoreCorrupt.
func (s *logStore) collectFileRecords(file uint32, offs []uint64, fn func(ref wal.Ref, rec []byte) error) error {
	f, err := s.segmentFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %w", errStoreCorrupt, err)
	}

	if err != nil {
		return err
	}

	if file&checkpointRefFlag == 0 {
		_, err = wal.Scan(io.NewSectionReader(f, 0, math.MaxInt64), fn)

		return err
	}

	for _, off := range offs {
		if err = wal.ReadFrame(f, off, fn); err != nil {
			return err
		}
	}

	return nil
}

// jobsOf returns the job of each of keys, from the bodies in found of the spec
// and state records at its refs.
func (s *logStore) jobsOf(keys []logKey, refs [][2]wal.Ref, found map[wal.Ref][]byte) ([]*Job, error) {
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
//
// With a checkpoint, from is its walFrom: files before it, which the
// checkpoint holds the live state of, are not replayed, and file from must be
// there.
//
// It also returns the log's valid end: the last file it kept and that file's
// valid end, or the start of the first file if that is the one torn, and the
// number of a last file with a torn header, which the caller removes, or 0.
//
// A missing walFrom is errStoreCorrupt, unless no file from it on is there
// and history.bolt's folded position is at its start: an earlier recovery
// removed it as torn, having first moved the folded position back to it, and
// the store closed before its writer made the file again (spec C5). The log
// then ends at walFrom's start, and it is the next file.
func (s *logStore) replay(ctx context.Context, from uint32) (uint32, historyPos, uint32, error) {
	files, err := walFilesFrom(s.dir, from)
	if s.walFromAbsent(err, files, from) {
		clog.Warn(ctx, walFromAbsentLogMsg, "file", walFilePath(s.dir, from))

		return from, historyPos{file: from}, 0, nil
	}

	if err != nil || len(files) == 0 {
		return 1, historyPos{}, 0, err
	}

	logEnd := historyPos{file: files[0]}

	for i, n := range files {
		torn, end, errf := s.replayAt(ctx, files, i)
		if errf != nil {
			return 0, historyPos{}, 0, errf
		}

		if torn {
			return n, logEnd, n, nil
		}

		logEnd = historyPos{file: n, off: uint64(end)} //nolint:gosec // an offset
	}

	return files[len(files)-1] + 1, logEnd, 0, nil
}

// walFromAbsent reports whether err, walFilesFrom's of files from walFrom
// from, is the walFrom file found missing with no file after it and
// history.bolt's folded position at its start (see replay).
func (s *logStore) walFromAbsent(err error, files []uint32, from uint32) bool {
	return errors.Is(err, errWALFromMissing) && len(files) == 0 && s.state.folded == historyPos{file: from}
}

// replayAt is replayFile of files[i], valid up to the end a later file's
// header gives it (see walValidEnd). Every file is first checked to start with
// a header or a torn one (spec C5), even one that limit 0 skips as spent, whose
// replay stops before reading its header: records with no header are
// errStoreCorrupt, not a spent file's tail to truncate.
func (s *logStore) replayAt(ctx context.Context, files []uint32, i int) (bool, int64, error) {
	limit, err := s.walValidEnd(files, i)
	if err != nil {
		return false, 0, err
	}

	if _, _, _, err = s.walHeader(files[i]); err != nil {
		return false, 0, err
	}

	return s.replayFile(ctx, files[i], limit, i == len(files)-1)
}
