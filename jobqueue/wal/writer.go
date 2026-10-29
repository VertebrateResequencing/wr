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

package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// headerMagic starts every segment's header record.
	headerMagic = "WRWALHDR"

	// headerLen is the length of a header record: its magic, the previous
	// file's number (u32) and that file's durable end (u64).
	headerLen = len(headerMagic) + 4 + 8

	// headerEnd is the offset of a segment's first record, just after its
	// header frame.
	headerEnd = int64(FrameHeader + headerLen)

	// walRetryBase is the backoff after the second consecutive failed batch;
	// it doubles per further one, up to walRetryCap.
	walRetryBase = 100 * time.Millisecond

	// walRetryCap caps the writer's backoff between failing batches.
	walRetryCap = 5 * time.Second

	// segmentPerm is the permission segments are created with.
	segmentPerm = 0o600
)

// ErrClosed is the error of an append made after Close.
var ErrClosed = errors.New("wal: writer closed")

// Test seams, set through SetTestSyncHook and SetTestRetryCap. Each writer
// copies them when made, so changing them never races a running writer.
var (
	testSyncHook atomic.Pointer[func(path string) error] //nolint:gochecknoglobals // a test seam
	testRetryCap atomic.Int64                            //nolint:gochecknoglobals // a test seam
)

// SetTestSyncHook is for tests only. It makes h, until restore is called, be
// called before each sync made by writers made meanwhile, after NewWriter
// returns, with the path being synced: a data sync of a segment, an fsync of
// the segment a rotation seals, or an fsync of the directory. An error h
// returns is the sync's, without syncing; h may also block to hold the sync. A
// nil h removes the hook.
func SetTestSyncHook(h func(path string) error) (restore func()) {
	var p *func(path string) error
	if h != nil {
		p = &h
	}

	old := testSyncHook.Swap(p)

	return func() { testSyncHook.Store(old) }
}

// SetTestRetryCap is for tests only. It caps the backoff between failing
// batches of writers made until restore is called at d instead of 5s; a d of 0
// or less restores the default.
func SetTestRetryCap(d time.Duration) (restore func()) {
	old := testRetryCap.Swap(int64(d))

	return func() { testRetryCap.Store(old) }
}

// ParseHeader returns the previous file's number and durable end from rec, a
// segment's header record, and whether rec is one. Every segment's first record
// is its header.
func ParseHeader(rec []byte) (prevFile uint32, prevEnd uint64, ok bool) {
	if len(rec) != headerLen || string(rec[:len(headerMagic)]) != headerMagic {
		return 0, 0, false
	}

	rest := rec[len(headerMagic):]

	return binary.LittleEndian.Uint32(rest), binary.LittleEndian.Uint64(rest[4:]), true
}

// batch is the records queued for one segment between two of the writer's
// takes, made durable together by one write and one sync.
type batch struct {
	file    uint32
	off     int64
	buf     []byte
	records int
	dones   []func(error)
	done    chan struct{}
	err     error
}

// add appends data to the batch, framing it first if plain, as n records.
func (b *batch) add(data []byte, plain bool, n int, done func(error)) {
	if plain {
		b.buf = AppendFrame(b.buf, data)
	} else {
		b.buf = append(b.buf, data...)
	}

	b.records += n
	if done != nil {
		b.dones = append(b.dones, done)
	}
}

// finish gives the batch its result: it calls its done funcs in Ref order,
// then wakes its waiters.
func (b *batch) finish(err error) {
	b.err = err

	for _, done := range b.dones {
		done(err)
	}

	close(b.done)
}

// Commit is the durability of an appended record: done once the batch holding
// it is durable or has failed.
type Commit struct{ b *batch }

// closedCommit calls done, if not nil, with ErrClosed and returns a Commit that
// is done with it.
func closedCommit(done func(error)) Commit {
	b := &batch{done: make(chan struct{})}
	if done != nil {
		b.dones = []func(error){done}
	}

	b.finish(ErrClosed)

	return Commit{b}
}

// Wait blocks until the record is durable, returning nil, or its batch has
// failed, returning the error.
func (c Commit) Wait() error {
	<-c.b.done

	return c.b.err
}

// WaitWithin is Wait for at most d: it returns done false and a nil error if the
// record is neither durable nor failed by then. A d of 0 or less only checks.
func (c Commit) WaitWithin(d time.Duration) (bool, error) {
	select {
	case <-c.b.done:
		return true, c.b.err
	default:
	}

	if d <= 0 {
		return false, nil
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-c.b.done:
		return true, c.b.err
	case <-t.C:
		return false, nil
	}
}

// WriterStats are a Writer's totals since it was made.
type WriterStats struct {
	// Batches is how many batches were made durable.
	Batches uint64

	// Records is how many records were made durable, counting each of a
	// block's.
	Records uint64

	// Bytes is how many bytes of records were made durable.
	Bytes uint64

	// Syncs is how many batch fdatasyncs were made, failed or not.
	Syncs uint64

	// SyncTime is the time those syncs took.
	SyncTime time.Duration
}

// Writer appends records to numbered segment files in one directory. One
// goroutine takes everything queued, writes it with one write() and makes it
// durable with one fdatasync, so concurrent appends share syncs. No lock is
// held across a sync.
//
// A failed write or sync abandons its segment: nothing more is written to it,
// since a page cache after a failed fsync cannot be trusted. The batch, and
// every batch queued behind it with Refs in that segment, get the error, and
// the writer carries on in the next file number, whose header records the
// abandoned file's durable end. After a second consecutive failure it waits a
// backoff before going on, and it never gives up.
type Writer struct {
	dir         string
	segmentSize int64
	onError     func(error)
	syncHook    func(path string) error
	retryCap    time.Duration
	kick        chan struct{}
	closing     chan struct{}
	exited      chan struct{}

	mu      sync.Mutex
	pending []*batch
	qFile   uint32
	qEnd    int64
	durFile uint32
	durEnd  int64
	closed  bool

	// Only the writer goroutine (or NewWriter before starting it) uses these.
	f        *os.File // the open segment; nil once abandoned
	file     uint32   // the segment last created or tried
	end      int64    // its durable end
	failures int      // consecutive failed batches
	closeErr error

	batches, records, bytes, syncs, syncNanos atomic.Uint64
}

// NewWriter creates segment file in dir, writes its header and makes it
// durable, then starts the writer goroutine. The header names the
// highest-numbered segment below file in dir and its size, which recovery has
// truncated to its durable end. Segments rotate before a record that would take
// one past segmentSize, unless it would be the segment's first. onError, if not
// nil, is called with the error of every failed batch.
func NewWriter(dir string, file uint32, segmentSize int64, onError func(error)) (*Writer, error) {
	prevFile, prevEnd, err := previousSegment(dir, file)
	if err != nil {
		return nil, err
	}

	w := &Writer{
		dir:         dir,
		segmentSize: segmentSize,
		onError:     onError,
		retryCap:    retryCap(),
		kick:        make(chan struct{}, 1),
		closing:     make(chan struct{}),
		exited:      make(chan struct{}),
		file:        prevFile,
		end:         prevEnd,
		qFile:       file,
		qEnd:        headerEnd,
	}

	if err = w.openSegment(file); err != nil {
		return nil, err
	}

	if h := testSyncHook.Load(); h != nil {
		w.syncHook = *h
	}

	go w.run()

	return w, nil
}

// Append queues rec, which must be 1 byte to 1GiB (else it panics), as a plain
// frame, returning its Ref and its batch's Commit. done, if not nil, is called
// by the writer goroutine with the batch's result after its sync and before
// Wait returns, in Ref order with the batch's other done funcs; it must not
// block. After Close, the Commit gives ErrClosed, the Ref is zero, and done is
// called with ErrClosed on the caller's goroutine before Append returns.
func (w *Writer) Append(rec []byte, done func(error)) (Ref, Commit) {
	checkRecord(rec)

	return w.add(rec, true, 1, done)
}

// AppendBlock is Append for block, one whole block frame made by AppendBlock
// holding n records; it panics if block is not one. The block's Ref has Idx 0;
// its records have its File and Off and Idx 0 to n-1. As with Append, after
// Close done is called with ErrClosed on the caller's goroutine before
// AppendBlock returns.
func (w *Writer) AppendBlock(block []byte, n int, done func(error)) (Ref, Commit) {
	checkBlock(block)

	return w.add(block, false, n, done)
}

// checkBlock panics unless block is one whole block frame, so a malformed one
// can never be written into the middle of the log.
func checkBlock(block []byte) {
	if len(block) <= FrameHeader {
		panic(fmt.Sprintf("wal: a block of %d bytes is not a block frame", len(block)))
	}

	lenFlags := binary.LittleEndian.Uint32(block)
	if lenFlags != blockFlag|uint32(len(block)-FrameHeader) { //nolint:gosec // compared, not trusted
		panic(fmt.Sprintf("wal: a %d byte block has lenFlags %#x", len(block), lenFlags))
	}
}

// add queues data, framing it first if plain, holding n records, under the
// mutex, so Ref order is log order.
func (w *Writer) add(data []byte, plain bool, n int, done func(error)) (Ref, Commit) {
	size := int64(len(data))
	if plain {
		size += FrameHeader
	}

	w.mu.Lock()

	if w.closed {
		w.mu.Unlock()

		return Ref{}, closedCommit(done)
	}

	if w.qEnd > headerEnd && w.qEnd+size > w.segmentSize {
		w.qFile++
		w.qEnd = headerEnd
	}

	b := w.openBatch()
	ref := Ref{File: w.qFile, Off: uint64(w.qEnd)} //nolint:gosec // never negative

	b.add(data, plain, n, done)

	w.qEnd += size
	w.mu.Unlock()

	w.signal()

	return ref, Commit{b}
}

// openBatch returns the batch the next record in segment qFile joins, starting
// a new one if the last queued is for an earlier segment or none is queued. The
// caller holds the mutex.
func (w *Writer) openBatch() *batch {
	if n := len(w.pending); n > 0 && w.pending[n-1].file == w.qFile {
		return w.pending[n-1]
	}

	b := &batch{file: w.qFile, off: w.qEnd, done: make(chan struct{})}
	w.pending = append(w.pending, b)

	return b
}

// signal wakes the writer goroutine if it is waiting.
func (w *Writer) signal() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Rotate seals the current segment: records appended after it go to the next
// file number, which it creates and makes durable, along with any records
// queued for it meanwhile, before it returns the sealed file's number. Its
// error is that of opening the new segment or syncing those records, or
// ErrClosed after Close. After a failure has abandoned the current segment,
// the sealed number may be that of a file never created: the one the next
// records were to start, which is still below the new segment's.
func (w *Writer) Rotate() (uint32, error) {
	w.mu.Lock()

	if w.closed {
		w.mu.Unlock()

		return 0, ErrClosed
	}

	sealed := w.qFile
	w.qFile++
	w.qEnd = headerEnd
	c := Commit{w.openBatch()}
	w.mu.Unlock()

	w.signal()

	if err := c.Wait(); err != nil {
		return 0, err
	}

	return sealed, nil
}

// Durable returns the end of the durable log: the segment of the last durable
// batch (or the last segment opened) and the offset just after it.
func (w *Writer) Durable() (uint32, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.durFile, w.durEnd
}

// Stats returns the writer's totals.
func (w *Writer) Stats() WriterStats {
	return WriterStats{
		Batches:  w.batches.Load(),
		Records:  w.records.Load(),
		Bytes:    w.bytes.Load(),
		Syncs:    w.syncs.Load(),
		SyncTime: time.Duration(w.syncNanos.Load()), //nolint:gosec // a sum of durations
	}
}

// Close refuses later appends (ErrClosed), then syncs every batch already
// queued, calls its done funcs and closes its channels before it returns. Its
// error is that of closing the open segment; batches' errors go to their
// commits. Closing again does nothing.
func (w *Writer) Close() error {
	w.mu.Lock()
	already := w.closed
	w.closed = true
	w.mu.Unlock()

	if already {
		<-w.exited

		return nil
	}

	close(w.closing)
	<-w.exited

	return w.closeErr
}

// run is the writer goroutine: it commits everything queued, in order, until
// Close and the queue is empty.
func (w *Writer) run() {
	defer close(w.exited)

	for {
		batches, closed := w.take()
		if len(batches) > 0 {
			w.commitAll(batches)

			continue
		}

		if closed {
			w.closeSegment()

			return
		}

		// Closing only wakes the loop: it must go round to take again, never
		// return here, or an append queued just before Close is lost.
		select {
		case <-w.kick:
		case <-w.closing:
		}
	}
}

// closeSegment closes the open segment, if any, keeping the error for Close.
func (w *Writer) closeSegment() {
	if w.f != nil {
		w.closeErr = w.f.Close()
	}
}

// take returns every queued batch and whether the writer is closed.
func (w *Writer) take() ([]*batch, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	batches := w.pending
	w.pending = nil

	return batches, w.closed
}

// commitAll makes batches durable in order, failing a batch, and every batch
// for its segment, on the first error in that segment.
func (w *Writer) commitAll(batches []*batch) {
	for len(batches) > 0 {
		b := batches[0]
		batches = batches[1:]

		err := w.commit(b)
		if err == nil {
			w.failures = 0

			b.finish(nil)

			continue
		}

		for _, failed := range w.abandon(b) {
			if w.onError != nil {
				w.onError(err)
			}

			failed.finish(err)
		}

		w.failures++
		w.backoff()
	}
}

// commit writes b to its segment, opening the segment first if b is its first
// batch, and syncs it.
func (w *Writer) commit(b *batch) error {
	if b.file != w.file {
		if err := w.openSegment(b.file); err != nil {
			return err
		}
	}

	if len(b.buf) == 0 {
		return nil
	}

	// Every earlier batch for this segment succeeded, or b would have failed
	// with it, so b.off is the segment's durable end.
	if _, err := w.f.WriteAt(b.buf, b.off); err != nil {
		return fmt.Errorf("wal: write segment %08x: %w", b.file, err)
	}

	start := time.Now()
	err := w.fdatasync(w.f)

	w.syncs.Add(1)
	w.syncNanos.Add(uint64(time.Since(start))) //nolint:gosec // a positive duration

	if err != nil {
		return fmt.Errorf("wal: sync segment %08x: %w", b.file, err)
	}

	w.end = b.off + int64(len(b.buf))
	w.setDurable(b.file, w.end)
	w.batches.Add(1)
	w.records.Add(uint64(b.records)) //nolint:gosec // a count
	w.bytes.Add(uint64(len(b.buf)))

	return nil
}

// openSegment creates segment n with a header naming the segment last created
// or tried and its durable end, then syncs it, the segment it replaces and the
// directory, so n and everything before it are durable before any record is
// written to it. On failure no segment is open and n is abandoned, with no
// durable end.
func (w *Writer) openSegment(n uint32) error {
	old, prevFile, prevEnd := w.f, w.file, w.end
	w.f, w.file, w.end = nil, n, 0

	if old != nil {
		// Its data is synced, and nothing more is written to it.
		defer old.Close()
	}

	f, err := os.OpenFile(segmentPath(w.dir, n), os.O_WRONLY|os.O_CREATE|os.O_EXCL, segmentPerm)
	if err != nil {
		return fmt.Errorf("wal: create segment: %w", err)
	}

	if err = w.startSegment(f, old, prevFile, prevEnd); err != nil {
		_ = f.Close() // abandoned: its error changes nothing

		return fmt.Errorf("wal: start segment %08x: %w", n, err)
	}

	w.f, w.end = f, headerEnd
	w.setDurable(n, headerEnd)

	return nil
}

// startSegment writes f's header and syncs it, old if not nil, and the
// directory.
func (w *Writer) startSegment(f, old *os.File, prevFile uint32, prevEnd int64) error {
	header := AppendFrame(nil, appendHeader(nil, prevFile, uint64(prevEnd))) //nolint:gosec // a file size

	if _, err := f.Write(header); err != nil {
		return err
	}

	if err := w.fdatasync(f); err != nil {
		return err
	}

	if old != nil {
		if err := w.sync(old.Name(), old.Sync); err != nil {
			return err
		}
	}

	d, err := os.Open(w.dir)
	if err != nil {
		return err
	}

	defer d.Close()

	return w.sync(d.Name(), d.Sync)
}

// appendHeader appends a header record naming prevFile and its durable end
// prevEnd to dst.
func appendHeader(dst []byte, prevFile uint32, prevEnd uint64) []byte {
	dst = append(dst, headerMagic...)
	dst = binary.LittleEndian.AppendUint32(dst, prevFile)

	return binary.LittleEndian.AppendUint64(dst, prevEnd)
}

// fdatasync makes f's data durable, after the sync hook.
func (w *Writer) fdatasync(f *os.File) error {
	return w.sync(f.Name(), func() error { return unix.Fdatasync(int(f.Fd())) })
}

// sync calls the sync hook with path, then, if it returned no error, fn.
func (w *Writer) sync(path string, fn func() error) error {
	if w.syncHook != nil {
		if err := w.syncHook(path); err != nil {
			return err
		}
	}

	return fn()
}

// setDurable records file and end as the end of the durable log.
func (w *Writer) setDurable(file uint32, end int64) {
	w.mu.Lock()
	w.durFile, w.durEnd = file, end
	w.mu.Unlock()
}

// abandon closes b's segment for good after b failed in it, and returns b and
// every batch queued for that segment since b was taken, which must fail with
// it: their Refs are in the abandoned segment. Records appended from now on go
// to the next file number.
func (w *Writer) abandon(b *batch) []*batch {
	if w.f != nil {
		_ = w.f.Close() // abandoned: its error changes nothing
		w.f = nil
	}

	failed := []*batch{b}

	w.mu.Lock()
	defer w.mu.Unlock()

	kept := w.pending[:0]

	for _, p := range w.pending {
		if p.file == b.file {
			failed = append(failed, p)
		} else {
			kept = append(kept, p)
		}
	}

	w.pending = kept

	if w.qFile == b.file {
		w.qFile++
		w.qEnd = headerEnd
	}

	return failed
}

// backoff waits retryDelay before the writer takes its next batch. It does not
// wait once the writer is closing.
func (w *Writer) backoff() {
	d := retryDelay(w.failures, w.retryCap)
	if d == 0 {
		return
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
	case <-w.closing:
	}
}

// retryDelay returns the backoff after failures consecutive failed batches:
// none after the first, then 100ms doubling per further failure, capped at
// limit. Doubling stops at the cap, so a long outage never overflows it.
func retryDelay(failures int, limit time.Duration) time.Duration {
	if failures < 2 { //nolint:mnd // the first failure is retried at once
		return 0
	}

	d := walRetryBase
	for range failures - 2 {
		if d >= limit {
			break
		}

		d *= 2
	}

	return min(d, limit)
}

// retryCap returns the backoff cap for a new writer: walRetryCap, unless a test
// set another.
func retryCap() time.Duration {
	if d := time.Duration(testRetryCap.Load()); d > 0 {
		return d
	}

	return walRetryCap
}
