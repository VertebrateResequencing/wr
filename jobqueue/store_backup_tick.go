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

// This file holds the log store's incremental backups (spec E4, Phase 1 form):
// ticks that copy to the backup only what is new since the last, history.bolt
// whole whenever its folded position moved, the newest checkpoint once, and
// wal bytes as parts, never shipping a byte twice.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// backupPartOffDigits is how many hex digits the start offset in a wal
	// part's name, NNNNNNNN.wal.OOOOOOOOOOOOOOOO, has.
	backupPartOffDigits = 16

	// backupFailedLogMsg is logged at error, at most once per
	// backupFailedLogEvery, while incremental backup ticks fail.
	backupFailedLogMsg   = "incremental backup failed"
	backupFailedLogEvery = time.Minute

	// backupCheckpointKeptLogMsg is logged at warn by a tick that keeps the
	// backup's checkpoint because the newest local one's histFrom is past the
	// history it shipped.
	backupCheckpointKeptLogMsg = "incremental backup kept its checkpoint: the newest one's histFrom is past " +
		"the history it holds"

	// backupStartedAgainLogMsg is logged at warn by a store's first tick when
	// its local wal files do not continue the backup's, so the backup is made
	// again as a first one (see startBackupAgain).
	backupStartedAgainLogMsg = "incremental backup started again as a first backup: the store no longer has " +
		"the wal files that would continue it"

	// recoveryCutsFile names the file in a store directory in which recovery
	// notes, durably and before it cuts a wal file, the end it leaves it at, so
	// that the first incremental backup tick after, in this run or a later one,
	// makes the backup's copy of the file match (see matchRecoveryCuts). That
	// tick deletes it.
	recoveryCutsFile = "backup-cuts"
)

// logBackupEvery, if above 0, replaces minimumTimeBetweenBackups (in
// nanoseconds) as the least time from the end of one incremental backup tick
// to the start of the next. A test seam, 0 outside tests.
var logBackupEvery atomic.Int64 //nolint:gochecknoglobals // a test seam

// logBackupTickHook, a test seam nil outside tests, is called by the ticker each
// time its timer fires, before it makes that tick.
var logBackupTickHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

// errBackupAhead is a tick's error for a backup holding a checkpoint or wal
// file this store has not reached, so not made from this store's files.
var errBackupAhead = errors.New("the backup holds a checkpoint or wal file this store has not reached, so it " +
	"was not made from this store; it has been left untouched: move it aside to let backups resume")

// readRecoveryCuts returns the cuts recoveryCutsFile in dir notes, by wal file
// number; none if it is not there.
func readRecoveryCuts(dir string) (map[uint32]int64, error) {
	cuts := make(map[uint32]int64)
	path := filepath.Join(dir, recoveryCutsFile)

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cuts, nil
	}

	if err != nil {
		return nil, err
	}

	for line := range strings.Lines(string(data)) {
		var (
			n   uint32
			end int64
		)

		if _, err = fmt.Sscanf(line, "%x %d\n", &n, &end); err != nil {
			return nil, fmt.Errorf("%s holds %q, not a wal file number and offset: %w; the backup may no "+
				"longer match this store, so move the backup aside (if it has one), then delete %s to let it "+
				"open", path, line, err, path)
		}

		cuts[n] = end
	}

	return cuts, nil
}

// backupPart is a part of a wal file in a backup: its name, the offset in the
// wal file it starts at, and its size. A whole wal file, as a copy of a store
// directory holds, is a part at offset 0.
type backupPart struct {
	name      string
	off, size int64
}

// end returns the offset in the wal file just after p.
func (p backupPart) end() int64 {
	return p.off + p.size
}

// prepareBackup reads what the backup holds before the store's first tick. A
// one-file bbolt backup is first moved aside (see backupTarget.moveAside), as
// E1 keeps a converted bbolt file. A backup that holds what this store has not
// reached is errBackupAhead, decided before anything in it is changed. Then
// leftover .tmp files are deleted, and so are parts that follow a gap or hold
// bytes past where recovery cut their wal file, so that each wal file's parts
// are a prefix of its local file. A backup the store's local wal files do not
// continue, as after a run without backups deleted the files it needs, is made
// again as a first backup (see startBackupAgain). A backup with no VERSION gets
// it last of all (see shipVersion).
func (s *logStore) prepareBackup() error {
	b := s.inc
	b.ckpts, b.parts = nil, make(map[uint32][]backupPart)

	entries, err := s.backupEntries()
	if err != nil {
		return err
	}

	tmps := b.noteEntries(entries)

	if err = s.checkBackupNotAhead(); err != nil {
		return err
	}

	if err = b.removeAll(tmps); err != nil {
		return err
	}

	if err = b.keepAllTiling(); err != nil {
		return err
	}

	return s.matchLocalFiles()
}

// shipPart ships the bytes of wal file n from what the backup holds of it to
// end, if there are any, as a new part. A file of which the backup holds no
// part and whose end is 0, one a failed open spent (spec B2), gets an empty
// part, so that a restore makes the empty file recovery skips as spent, not a
// store missing that file number.
func (s *logStore) shipPart(n uint32, end int64) error {
	b := s.inc

	from := b.shipped(n)
	if end < from || (end == from && len(b.parts[n]) > 0) {
		return nil
	}

	name := backupPartName(n, from)

	if err := b.target.put(name, copyFileTo(walFilePath(s.dir, n), from, end-from)); err != nil {
		return err
	}

	b.parts[n] = append(b.parts[n], backupPart{name: name, off: from, size: end - from})
	b.shippedWAL = true

	return nil
}

// backupPartName returns the name of the part of wal file n starting at off.
func backupPartName(n uint32, off int64) string {
	return fmt.Sprintf("%0*x%s.%0*x", walFileDigits, n, walFileExt, backupPartOffDigits, off)
}

// copyFileTo returns a func that writes size bytes of the local file at path
// from off (all from off if size is -1) to w.
func copyFileTo(path string, off, size int64) func(w io.Writer) error {
	return func(w io.Writer) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}

		n := size
		if n < 0 {
			n, err = sizeAfter(f, off)
		}

		if err == nil {
			_, err = io.CopyN(w, io.NewSectionReader(f, off, n), n)
		}

		return errors.Join(err, f.Close())
	}
}

// sizeAfter returns how many bytes of f follow off.
func sizeAfter(f *os.File, off int64) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}

	return info.Size() - off, nil
}

// cutsAll reports whether recovery cut wal file n before every one of parts,
// the backup's parts of it, starts.
func (s *logStore) cutsAll(n uint32, parts []backupPart) bool {
	cut, ok := s.recoveryCuts[n]

	return ok && !slices.ContainsFunc(parts, func(p backupPart) bool { return p.off < cut })
}

// replaceTornParts replaces the backup's parts of wal file n, which recovery
// cut to nothing, with the local file's durable header: it deletes every part
// after the first, in descending offset order, then puts the header under the
// first part's name, replacing it in one rename. So a crash at any point
// leaves the backup a prefix of one of the two files, never none of n, which a
// backup checkpoint's walFrom may be, and a restore needs, as file numbers
// never skip one. n is either a file removed as torn that the writer reuses,
// or a spent one (spec B2) truncated to empty, whose durable end is 0, so whose
// header put is empty, as the part shipPart ships of a spent file is. Only the
// header is put: the backup's history.bolt may have folded into the old n, and
// a restore with it would take the reused n's records below its folded
// position as folded, losing their completions. So the tick ships the rest of
// n, as a new part, only after it has shipped history.bolt.
func (s *logStore) replaceTornParts(files []uint32, n uint32) error {
	b := s.inc
	parts := b.parts[n]

	end, err := s.durableEndOf(files, n)
	if err != nil {
		return err
	}

	// Defensive, unreachable today: the writer reuses n and makes its header
	// durable before Durable names it, a later header bounds a spent n, and
	// cutToMatch skips n below the first local file, so end is known. Without
	// this check an unknown end (-1) would ship the whole local file,
	// non-durable bytes included, as copyFileTo reads a negative size as "to
	// the end".
	if end < 0 {
		return b.removePartsFrom(n, 0)
	}

	left, err := b.removeParts(parts[1:])
	b.parts[n] = append(parts[:1], left...)

	if err != nil {
		return err
	}

	end = min(end, int64(wal.FrameHeader+walHeaderLen))
	first := backupPart{name: parts[0].name, size: end}

	if err = b.target.put(first.name, copyFileTo(walFilePath(s.dir, n), 0, end)); err != nil {
		return err
	}

	b.parts[n] = []backupPart{first}

	return nil
}

// incBackup makes a log store's incremental backups (spec E4) to target:
// periodic ones, in production deployment or with forceBackups, or only those
// a test forces.
type incBackup struct {
	target   backupTarget
	periodic bool
	stop     chan struct{}
	exited   chan struct{}

	// tickMu makes ticks one at a time, and guards what the rest say the
	// backup holds: whether a tick has read that (prepared), whether this
	// store has shipped wal bytes to it (shippedWAL), whether it lacks
	// VERSION (versionPending) and the least walFrom of a checkpoint it must
	// hold before it gets it (versionFrom), its checkpoints, the folded
	// position of the history.bolt copy it holds (if histShipped), and the
	// parts of each wal file, which tile it from 0. sealedEnds caches the
	// durable end of each sealed local wal file. A store reads what the
	// backup holds only before it ships anything, so shippedWAL is false
	// whenever that read checks it; it keeps the check right should the
	// backup be read again later, as the tests' retargetBackup does.
	tickMu         sync.Mutex
	prepared       bool
	shippedWAL     bool
	versionPending bool
	versionFrom    uint32
	ckpts          []uint32
	histShipped    bool
	histFolded     historyPos
	parts          map[uint32][]backupPart
	sealedEnds     map[uint32]int64

	// mu, under which nothing else is taken, guards gating, whether the store
	// keeps what the backup may need, held, the local checkpoint a tick holds,
	// and done, the local wal files the backup needs no more bytes of.
	mu     sync.Mutex
	gating bool
	held   uint32
	done   map[uint32]bool

	// logMu guards errTime, when noteFailure last logged.
	logMu   sync.Mutex
	errTime time.Time
}

// newIncBackup returns the incremental backups of the store cfg describes:
// nil if it has no dbBkFile, or an S3 one in development deployment without
// forceBackups, which never makes or restores from backups there.
func newIncBackup(cfg storeConfig) (*incBackup, error) {
	periodic := cfg.deployment == internal.Production || cfg.forceBackups

	if cfg.dbBkFile == "" || (internal.InS3(cfg.dbBkFile) && !periodic) {
		return nil, nil //nolint:nilnil // no backups is not an error
	}

	target, err := backupTargetAt(cfg.dbBkFile, cfg.deployment)
	if err != nil {
		return nil, err
	}

	if s3, ok := target.(s3Target); ok {
		s3.stage = cfg.dbFile
		target = s3
	}

	return &incBackup{
		target:     target,
		periodic:   periodic,
		gating:     periodic,
		parts:      make(map[uint32][]backupPart),
		sealedEnds: make(map[uint32]int64),
		done:       make(map[uint32]bool),
	}, nil
}

// removeAll deletes the files of the backup named names.
func (b *incBackup) removeAll(names []string) error {
	for _, name := range names {
		if err := b.target.remove(name); err != nil {
			return err
		}
	}

	return nil
}

// mayDelete reports whether the store may delete wal file n: always, unless
// the backup may still need bytes of it. A nil b makes no backups.
func (b *incBackup) mayDelete(n uint32) bool {
	if b == nil {
		return true
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return !b.gating || b.done[n]
}

// holds reports whether a tick holds checkpoint n, which the store must keep.
func (b *incBackup) holds(n uint32) bool {
	if b == nil {
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.held != 0 && b.held == n
}

// setHeld makes n the checkpoint a tick holds, 0 for none.
func (b *incBackup) setHeld(n uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.held = n
}

// markDone notes that the backup needs no more bytes of wal file n.
func (b *incBackup) markDone(n uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.done[n] = true
}

// startGating makes the store keep what the backup may need from now on.
func (b *incBackup) startGating() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.gating = true
}

// noteFailure logs err, if not nil, at error, at most once per
// backupFailedLogEvery.
func (b *incBackup) noteFailure(ctx context.Context, err error) {
	if err == nil {
		return
	}

	b.logMu.Lock()
	defer b.logMu.Unlock()

	if !b.errTime.IsZero() && time.Since(b.errTime) < backupFailedLogEvery {
		return
	}

	b.errTime = time.Now()

	clog.Error(ctx, backupFailedLogMsg, "err", err)
}

// shipped returns how many bytes of wal file n from 0 the backup holds.
func (b *incBackup) shipped(n uint32) int64 {
	parts := b.parts[n]
	if len(parts) == 0 {
		return 0
	}

	return parts[len(parts)-1].end()
}

// newestCheckpoint returns the walFrom of the newest checkpoint in the backup,
// 0 if it has none.
func (b *incBackup) newestCheckpoint() uint32 {
	if len(b.ckpts) == 0 {
		return 0
	}

	return slices.Max(b.ckpts)
}

// neededFrom returns the first wal file the backup needs: the one below both
// its checkpoint's walFrom and its history's folded position's file, or 0,
// all of them, if it has no checkpoint or history yet.
func (b *incBackup) neededFrom() uint32 {
	if !b.histShipped || len(b.ckpts) == 0 {
		return 0
	}

	return min(b.newestCheckpoint(), b.histFolded.file)
}

// removeParts deletes parts, in descending offset order so that what is left
// of a wal file always tiles it from 0, returning those that are left.
func (b *incBackup) removeParts(parts []backupPart) ([]backupPart, error) {
	for i := len(parts) - 1; i >= 0; i-- {
		if err := b.target.remove(parts[i].name); err != nil {
			return parts[:i+1], err
		}
	}

	return nil, nil
}

// forgetBelow forgets what b noted of the local wal files below n. The caller
// holds tickMu.
func (b *incBackup) forgetBelow(n uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	maps.DeleteFunc(b.done, func(file uint32, _ bool) bool { return file < n })
	maps.DeleteFunc(b.sealedEnds, func(file uint32, _ int64) bool { return file < n })
}

// noteEntries notes the backup's checkpoints, the parts of each wal file and
// whether it lacks VERSION, changing nothing. It returns the names of the
// leftover .tmp files it holds.
func (b *incBackup) noteEntries(entries []backupEntry) []string {
	var tmps []string

	b.versionPending = true

	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.name, storeTmpExt):
			tmps = append(tmps, entry.name)
		case entry.name == storeVersionFile:
			b.versionPending = false
		default:
			b.noteEntry(entry)
		}
	}

	return tmps
}

// keepAllTiling is keepTiling of every wal file's parts.
func (b *incBackup) keepAllTiling() error {
	for n, parts := range b.parts {
		if err := b.keepTiling(n, sortedParts(parts)); err != nil {
			return err
		}
	}

	return nil
}

// sortedParts returns parts sorted by offset, a whole wal file before a part
// at the same offset.
func sortedParts(parts []backupPart) []backupPart {
	slices.SortFunc(parts, func(a, b backupPart) int {
		if c := cmp.Compare(a.off, b.off); c != 0 {
			return c
		}

		return cmp.Compare(len(a.name), len(b.name))
	})

	return parts
}

// noteEntry notes entry, at the backup's top level, if it is a checkpoint or
// a wal part.
func (b *incBackup) noteEntry(entry backupEntry) {
	if strings.Contains(entry.name, "/") {
		return
	}

	if n, ok := checkpointNumber(entry.name); ok {
		b.ckpts = append(b.ckpts, n)
	}

	if n, off, ok := parseBackupPart(entry.name); ok {
		b.parts[n] = append(b.parts[n], backupPart{name: entry.name, off: off, size: entry.size})
	}
}

// parseBackupPart returns the wal file number and start offset of the part
// named name, NNNNNNNN.wal.OOOOOOOOOOOOOOOO, or of the whole wal file
// NNNNNNNN.wal, at offset 0, and whether name is either.
func parseBackupPart(name string) (uint32, int64, bool) {
	hex, rest, found := strings.Cut(name, walFileExt)
	if !found || len(hex) != walFileDigits {
		return 0, 0, false
	}

	n, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, false
	}

	off, ok := parsePartOffset(rest)

	return uint32(n), off, ok
}

// keepTiling makes parts, wal file n's in the backup sorted by offset, the
// ones that tile it from 0, deleting the rest.
func (b *incBackup) keepTiling(n uint32, parts []backupPart) error {
	var end int64

	tiled := 0

	for _, p := range parts {
		if p.off != end {
			break
		}

		end = p.end()
		tiled++
	}

	left, err := b.removeParts(parts[tiled:])
	b.parts[n] = append(parts[:tiled], left...)

	if len(b.parts[n]) == 0 {
		delete(b.parts, n)
	}

	return err
}

// removePartsFrom deletes the parts of wal file n in the backup that start at
// or after end.
func (b *incBackup) removePartsFrom(n uint32, end int64) error {
	parts := b.parts[n]
	keep := 0

	for keep < len(parts) && parts[keep].off < end {
		keep++
	}

	left, err := b.removeParts(parts[keep:])
	b.parts[n] = append(parts[:keep], left...)

	return err
}

// wholeFrom reports whether the backup holds a checkpoint whose walFrom is at
// or past first, and no part of a wal file before first.
func (b *incBackup) wholeFrom(first uint32) bool {
	return len(b.ckpts) > 0 && b.newestCheckpoint() >= first && !b.holdsPartsBefore(first)
}

// needsNoneBefore reports whether the backup needs no wal file before first:
// its newest checkpoint's walFrom is at or past first, or it holds neither a
// checkpoint nor a part of a wal file before first.
func (b *incBackup) needsNoneBefore(first uint32) bool {
	if len(b.ckpts) > 0 {
		return b.newestCheckpoint() >= first
	}

	return !b.holdsPartsBefore(first)
}

// holdsPartsBefore reports whether the backup holds a part of a wal file
// before first.
func (b *incBackup) holdsPartsBefore(first uint32) bool {
	for n := range b.parts {
		if n < first {
			return true
		}
	}

	return false
}

// parsePartOffset returns the start offset that rest, what follows
// NNNNNNNN.wal in a part's name, gives: 0 for nothing, as a whole wal file has,
// else the offset in ".OOOOOOOOOOOOOOOO", and whether it is either.
func parsePartOffset(rest string) (int64, bool) {
	if rest == "" {
		return 0, true
	}

	offHex, ok := strings.CutPrefix(rest, ".")
	if !ok || len(offHex) != backupPartOffDigits {
		return 0, false
	}

	off, err := strconv.ParseInt(offHex, 16, 64)

	return off, err == nil
}

// backupTickInterval returns the least time from the end of one tick to the
// start of the next, which waits as long as the last tick took if that is
// longer (see runBackupTicker).
func backupTickInterval() time.Duration {
	if every := logBackupEvery.Load(); every > 0 {
		return time.Duration(every)
	}

	return minimumTimeBetweenBackups
}

// matchLocalFiles makes the backup match the store's local wal files: it
// starts the backup again as a first backup if they do not continue it (see
// startAgainIfNotContinued), then makes its parts match recovery's cuts, then
// forgets those.
func (s *logStore) matchLocalFiles() error {
	if err := s.startAgainIfNotContinued(); err != nil {
		return err
	}

	if err := s.matchRecoveryCuts(); err != nil {
		return err
	}

	return s.forgetRecoveryCuts()
}

// backupEntries returns the files of the backup, after moving a one-file
// bbolt backup aside (see backupTarget.moveAside); none if it has none.
func (s *logStore) backupEntries() ([]backupEntry, error) {
	b := s.inc

	form, err := b.target.form()
	if err != nil {
		return nil, err
	}

	switch form {
	case backupFile:
		return nil, b.target.moveAside(s.dir)
	case backupStore:
		return b.target.list()
	default:
		return nil, nil
	}
}

// startBackupTicker starts the goroutine making periodic incremental backup
// ticks, which close stops.
func (s *logStore) startBackupTicker() {
	if s.inc == nil || !s.inc.periodic {
		return
	}

	s.inc.stop = make(chan struct{})
	s.inc.exited = make(chan struct{})

	go s.runBackupTicker()
}

// runBackupTicker makes a tick every backupTickInterval, or as often as ticks
// take if that is longer, until close stops it.
func (s *logStore) runBackupTicker() {
	defer internal.LogPanic(s.ctx, "jobqueue log store backup ticker", false)
	defer close(s.inc.exited)

	wait := backupTickInterval()

	for {
		timer := time.NewTimer(wait)

		select {
		case <-s.inc.stop:
			timer.Stop()

			return
		case <-timer.C:
		}

		callHook(&logBackupTickHook)

		wait = max(backupTickInterval(), s.periodicBackupTick())
	}
}

// periodicBackupTick makes a tick, logging its failure (see noteFailure), and
// returns how long it took.
func (s *logStore) periodicBackupTick() time.Duration {
	start := time.Now()

	s.inc.noteFailure(s.ctx, s.backupTick())

	return time.Since(start)
}

// finishBackups, for close once the writer has made its last commit, stops
// the ticker, waiting for a tick in progress, then makes a final tick, logging
// its failure at error whenever a periodic one last logged.
func (s *logStore) finishBackups() {
	if s.inc == nil || !s.inc.periodic {
		return
	}

	close(s.inc.stop)
	<-s.inc.exited

	if err := s.backupTick(); err != nil {
		clog.Error(s.ctx, backupFailedLogMsg, "err", err)
	}
}

// backupTick makes one incremental backup tick (spec E4). The first of a store
// reads what the backup holds (see prepareBackup). Then it holds the newest
// local checkpoint C, ships history.bolt if its folded position moved, then
// the durable wal bytes not yet shipped as parts, then C, if new, deleting
// the backup's other checkpoints, then deletes the parts of the wal files the
// backup no longer needs, then ships VERSION if the backup lacks it and is
// whole (see shipVersion). Last it deletes the local files the store kept for
// the backup that it now has.
//
// The wal bytes go before C so that a crash between never leaves a backup whose
// newest checkpoint's walFrom file is missing.
func (s *logStore) backupTick() error {
	b := s.inc

	b.tickMu.Lock()
	defer b.tickMu.Unlock()

	b.startGating()

	if !b.prepared {
		if err := s.prepareBackup(); err != nil {
			return err
		}

		b.prepared = true
	}

	ckpt := s.holdNewestCheckpoint()
	err := s.shipBackup(ckpt)

	b.setHeld(0)

	if err != nil {
		return err
	}

	return s.deleteShipped()
}

// holdNewestCheckpoint returns the walFrom of the newest local checkpoint, 0
// if there is none, which the store then keeps until the tick releases it.
func (s *logStore) holdNewestCheckpoint() uint32 {
	s.ckptMu.Lock()
	defer s.ckptMu.Unlock()

	s.inc.setHeld(s.ckptFrom)

	return s.ckptFrom
}

// shipBackup is backupTick's shipping, of checkpoint ckpt.
func (s *logStore) shipBackup(ckpt uint32) error {
	if err := s.shipHistory(); err != nil {
		return err
	}

	if err := s.shipWAL(); err != nil {
		return err
	}

	if err := s.shipCheckpoint(ckpt); err != nil {
		return err
	}

	if err := s.dropUnneededParts(); err != nil {
		return err
	}

	return s.shipVersion(ckpt)
}

// shipVersion ships VERSION to a backup lacking it once the backup is whole:
// it holds a checkpoint whose walFrom is at or past versionFrom, and no part of
// a wal file before that, or the store has none (ckpt, the one the tick holds,
// is 0), so the wal files it holds from the first on have every record. As
// restore (E2) refuses a backup with no VERSION, a store's first backup that a
// crash cuts short, or that no checkpoint could yet be shipped to, is never
// restored as a store missing what the checkpoint holds.
func (s *logStore) shipVersion(ckpt uint32) error {
	b := s.inc
	if !b.versionPending || (ckpt != 0 && !b.wholeFrom(b.versionFrom)) {
		return nil
	}

	if err := b.target.put(storeVersionFile, copyFileTo(filepath.Join(s.dir, storeVersionFile), 0, -1)); err != nil {
		return err
	}

	b.versionPending, b.versionFrom = false, 0

	return nil
}

// startAgainIfNotContinued makes the backup again as a first backup (see
// startBackupAgain) if the store's local wal files do not continue it (see
// backupContinued).
func (s *logStore) startAgainIfNotContinued() error {
	files, err := walFileNumbers(s.dir)
	if err != nil || len(files) == 0 {
		return err
	}

	continued, err := s.backupContinued(files[0])
	if err != nil || continued {
		return err
	}

	return s.startBackupAgain(files[0])
}

// backupContinued reports whether the store's local wal files, from first on,
// continue the backup's: it needs no wal file before first (see
// needsNoneBefore), or it holds wal file first-1 ending just where first's
// header says that file ended. A run without backups can leave neither, by
// deleting files the backup still needs, or by cutting the last of them and
// deleting it once its checkpoint is past it.
func (s *logStore) backupContinued(first uint32) (bool, error) {
	b := s.inc

	if b.needsNoneBefore(first) {
		return true, nil
	}

	if len(b.parts[first-1]) == 0 {
		return false, nil
	}

	prevFile, prevEnd, whole, err := s.walHeader(first)
	if err != nil {
		return false, err
	}

	return whole && prevFile == first-1 && prevEnd == b.shipped(first-1), nil
}

// startBackupAgain makes the backup's next ticks make it again as a first
// backup from first, the store's first local wal file: it deletes VERSION,
// durably, before a tick ships anything to it, so that restore refuses the
// backup, saying to move it aside, until a tick has shipped history.bolt
// again, the wal files from first on and a checkpoint whose walFrom is at or
// past first, and has deleted the backup's older checkpoints and its parts of
// the wal files before first (see shipVersion). It suits any backup the
// store's local wal files no longer continue, whatever deleted them.
func (s *logStore) startBackupAgain(first uint32) error {
	b := s.inc

	clog.Warn(s.ctx, backupStartedAgainLogMsg, "backup", b.target.describe(), "first_wal_file", first)

	if err := b.target.remove(storeVersionFile); err != nil {
		return err
	}

	if err := b.target.sync(); err != nil {
		return err
	}

	b.versionPending, b.versionFrom, b.histShipped = true, first, false

	return nil
}

// deleteShipped deletes the files the store kept for the backup that the
// backup now has and the newest checkpoint that could delete files (see
// delFrom) made redundant, then forgets what it noted of the wal files below
// them. One it failed to delete is noted again by the next tick, as a file the
// backup no longer needs.
func (s *logStore) deleteShipped() error {
	s.ckptMu.Lock()
	defer s.ckptMu.Unlock()

	if s.delFrom == 0 {
		return nil
	}

	err := s.deleteRedundant(s.delFrom, s.delFolded)
	s.inc.forgetBelow(min(s.delFrom, s.delFolded.file))

	return err
}

// matchRecoveryCuts makes the backup's copy of each wal file recovery cut end
// where recovery left the local file (spec E4), then makes the deletes that
// took durable, before the caller forgets the cuts. A cut of a file below the
// store's first local wal file is skipped: the store deleted the file once its
// checkpoint and history were past it, and the number is never written again.
// The backup's parts of it are then either ones it no longer needs once the
// tick ships history.bolt, as its checkpoint's walFrom is past the file, or
// ones the store's local wal files do not continue, so prepareBackup has
// already started it again as a first backup (see startBackupAgain), which
// deletes them before it gets VERSION.
func (s *logStore) matchRecoveryCuts() error {
	files, err := walFileNumbers(s.dir)
	if err != nil {
		return err
	}

	changed := false

	for _, n := range slices.Sorted(maps.Keys(s.recoveryCuts)) {
		if !s.cutToMatch(files, n) {
			continue
		}

		changed = true

		if err = s.matchRecoveryCut(files, n, s.recoveryCuts[n]); err != nil {
			return err
		}
	}

	if !changed {
		return nil
	}

	return s.inc.target.sync()
}

// cutToMatch reports whether the backup's copy of wal file n must change to
// match recovery's cut of it: it holds bytes past the cut, and n is not below
// files[0], the store's first local wal file.
func (s *logStore) cutToMatch(files []uint32, n uint32) bool {
	return s.inc.shipped(n) > s.recoveryCuts[n] && (len(files) == 0 || n >= files[0])
}

// matchRecoveryCut makes the backup's copy of wal file n end at end, where
// recovery cut it: for 0, a file removed as torn whose number the writer
// reuses, or a spent one truncated to empty, it replaces the parts with the
// local file's durable header (see replaceTornParts); otherwise it deletes the
// parts after end, and ships again the local bytes of a part that spans it,
// under its own name.
func (s *logStore) matchRecoveryCut(files []uint32, n uint32, end int64) error {
	if end == 0 {
		return s.replaceTornParts(files, n)
	}

	if err := s.inc.removePartsFrom(n, end); err != nil {
		return err
	}

	return s.reshipSpanning(n, end)
}

// durableEndOf returns the durable end of local wal file n, files[i] (see
// shipWAL), or -1 if it is not known: n is not a local file, or is past the
// durable log, or is sealed and no later file has a whole header yet.
func (s *logStore) durableEndOf(files []uint32, n uint32) (int64, error) {
	durFile, durEnd := s.w.Durable()

	i := slices.Index(files, n)

	switch {
	case i < 0 || n > durFile:
		return -1, nil
	case n == durFile:
		return durEnd, nil
	default:
		return s.sealedEnd(files, i)
	}
}

// forgetRecoveryCuts deletes recoveryCutsFile, durably, once the backup
// matches each cut it notes, and forgets them.
func (s *logStore) forgetRecoveryCuts() error {
	if len(s.recoveryCuts) == 0 {
		return nil
	}

	if err := os.Remove(filepath.Join(s.dir, recoveryCutsFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	clear(s.recoveryCuts)

	return syncDir(s.dir)
}

// noteRecoveryCut notes that recovery is about to leave wal file n ending at
// end (0 for one it removes as torn), adding it, durably, to the cuts
// recoveryCutsFile holds, unless that already holds a cut of n at or before
// end.
func (s *logStore) noteRecoveryCut(n uint32, end int64) error {
	if old, ok := s.recoveryCuts[n]; ok && old <= end {
		return nil
	}

	s.recoveryCuts[n] = end

	var content []byte

	for _, file := range slices.Sorted(maps.Keys(s.recoveryCuts)) {
		content = fmt.Appendf(content, "%0*x %d\n", walFileDigits, file, s.recoveryCuts[file])
	}

	return writeFileDurably(filepath.Join(s.dir, recoveryCutsFile), content)
}

// reshipSpanning ships again, under its own name, the last part of wal file n
// in the backup if it ends past end, with the local bytes up to end.
func (s *logStore) reshipSpanning(n uint32, end int64) error {
	b := s.inc

	parts := b.parts[n]
	if len(parts) == 0 {
		delete(b.parts, n)

		return nil
	}

	last := &parts[len(parts)-1]
	if last.end() <= end {
		return nil
	}

	if err := b.target.put(last.name, copyFileTo(walFilePath(s.dir, n), last.off, end-last.off)); err != nil {
		return err
	}

	last.size = end - last.off

	return nil
}

// checkBackupNotAhead returns errBackupAhead if the backup holds a checkpoint
// after the store's newest, or parts of a wal file after the one it is
// writing, or, before this store has shipped wal bytes, from the one its
// writer started with on. A wal file whose every part starts at or after
// where recovery cut it does not count, as matchRecoveryCuts deletes them: a
// torn file's number the writer reuses is one the store has written.
func (s *logStore) checkBackupNotAhead() error {
	b := s.inc

	s.ckptMu.Lock()
	ckptFrom := s.ckptFrom
	s.ckptMu.Unlock()

	if newest := b.newestCheckpoint(); newest > ckptFrom {
		return fmt.Errorf("%w: %s holds checkpoint %08x, after this store's %08x", errBackupAhead,
			b.target.describe(), newest, ckptFrom)
	}

	durFile, _ := s.w.Durable()

	for n, parts := range b.parts {
		if s.cutsAll(n, parts) {
			continue
		}

		if n > durFile || (!b.shippedWAL && n >= s.openNext) {
			return fmt.Errorf("%w: %s holds wal file %08x, which this store has not written", errBackupAhead,
				b.target.describe(), n)
		}
	}

	return nil
}

// shipHistory ships history.bolt whole, under one read transaction that also
// reads its folded position, F_b, unless that is the position of the copy the
// backup already holds.
func (s *logStore) shipHistory() error {
	b := s.inc

	pos, err := s.history.foldedPos()
	if err != nil {
		return err
	}

	if b.histShipped && pos == b.histFolded {
		return nil
	}

	var folded historyPos

	err = b.target.put(historyBoltFile, func(w io.Writer) error {
		var errc error

		folded, errc = s.history.copyTo(w)

		return errc
	})
	if err != nil {
		return err
	}

	b.histShipped, b.histFolded = true, folded

	return nil
}

// shipWAL ships, for each local wal file the backup needs, the durable bytes
// it does not yet hold, as one new part, and notes each sealed file the
// backup then needs no more of, as it does a file below the ones it needs.
func (s *logStore) shipWAL() error {
	b := s.inc
	durFile, durEnd := s.w.Durable()

	files, err := walFileNumbers(s.dir)
	if err != nil {
		return err
	}

	need := b.neededFrom()

	for i, n := range files {
		switch {
		case n > durFile:
			return nil
		case n < need:
			b.markDone(n)
		case n == durFile:
			err = s.shipPart(n, durEnd)
		default:
			err = s.shipSealed(files, i)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// shipSealed ships what the backup lacks of sealed wal file files[i], up to
// its durable end, once that is known, noting that it then needs no more.
func (s *logStore) shipSealed(files []uint32, i int) error {
	end, err := s.sealedEnd(files, i)
	if err != nil || end < 0 {
		return err
	}

	if err = s.shipPart(files[i], end); err != nil {
		return err
	}

	s.inc.markDone(files[i])

	return nil
}

// sealedEnd returns the durable end of sealed wal file files[i] that the
// first later file with a whole header records (see walValidEnd), or -1 if
// none does yet.
func (s *logStore) sealedEnd(files []uint32, i int) (int64, error) {
	b := s.inc
	if end, ok := b.sealedEnds[files[i]]; ok {
		return end, nil
	}

	end, err := s.walValidEnd(files, i)
	if err != nil || end < 0 {
		return end, err
	}

	b.sealedEnds[files[i]] = end

	return end, nil
}

// shipCheckpoint ships checkpoint ckpt, held, if it is newer than the
// backup's and its histFrom is at or below the shipped history's folded
// position, else keeps the backup's, logging at warn if that is why. Then it
// deletes every other checkpoint from the backup.
func (s *logStore) shipCheckpoint(ckpt uint32) error {
	b := s.inc

	if ckpt != 0 && ckpt > b.newestCheckpoint() {
		if err := s.shipNewCheckpoint(ckpt); err != nil {
			return err
		}
	}

	newest := b.newestCheckpoint()

	for _, n := range slices.Clone(b.ckpts) {
		if n == newest {
			continue
		}

		if err := b.target.remove(filepath.Base(checkpointPath("", n))); err != nil {
			return err
		}

		b.ckpts = slices.DeleteFunc(b.ckpts, func(c uint32) bool { return c == n })
	}

	return nil
}

// shipNewCheckpoint ships checkpoint ckpt if its histFrom is at or below the
// shipped history's folded position, else logs at warn.
func (s *logStore) shipNewCheckpoint(ckpt uint32) error {
	b := s.inc

	header, _, f, err := openCheckpoint(s.dir, ckpt)
	if err != nil {
		return err
	}

	if err = f.Close(); err != nil {
		return err
	}

	if !b.histShipped || header.histFrom.after(b.histFolded) {
		clog.Warn(s.ctx, backupCheckpointKeptLogMsg, "checkpoint", ckpt, "hist_from_file", header.histFrom.file,
			"hist_from_offset", header.histFrom.off, "folded_file", b.histFolded.file,
			"folded_offset", b.histFolded.off)

		return nil
	}

	path := checkpointPath(s.dir, ckpt)

	if err = b.target.put(filepath.Base(path), copyFileTo(path, 0, -1)); err != nil {
		return err
	}

	b.ckpts = append(b.ckpts, ckpt)

	return nil
}

// dropUnneededParts deletes from the backup every part of each wal file below
// both its checkpoint's walFrom and its history's folded position's file.
func (s *logStore) dropUnneededParts() error {
	b := s.inc
	need := b.neededFrom()

	for _, n := range slices.Sorted(maps.Keys(b.parts)) {
		if n >= need {
			break
		}

		left, err := b.removeParts(b.parts[n])
		if err != nil {
			b.parts[n] = left

			return err
		}

		delete(b.parts, n)
	}

	return nil
}
