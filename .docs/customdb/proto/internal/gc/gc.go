// Package gc is a group-commit writer over one or more files. Callers queue
// appends (or fixed-offset overwrites) and get back a channel that is closed
// once that write, and everything queued before it, is on disk. One goroutine
// takes everything queued so far, writes it, fdatasyncs every file it touched
// (in parallel) and wakes the waiters: one sync per batch, however many
// writers are waiting, and no lock held across the sync.
//
// Records are framed as [len u32][crc32c u32][payload], so a reader can find
// a torn tail after a crash and stop there (see Scan).
package gc

import (
	"bufio"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// FrameHeader is the bytes added to every appended record.
const FrameHeader = 8

var crcTable = crc32.MakeTable(crc32.Castagnoli) //nolint:gochecknoglobals

// ErrClosed is returned for writes after Close.
var ErrClosed = errors.New("gc: closed")

type pwrite struct {
	off int64
	b   []byte
}

type fileBuf struct {
	app []byte
	pw  []pwrite
}

type batch struct {
	bufs []fileBuf
	done chan struct{}
	err  error
}

// Writer group-commits to a fixed set of files.
type Writer struct {
	files  []*os.File
	sizes  []int64
	mu     sync.Mutex
	cur    *batch
	spare  *batch
	kick   chan struct{}
	closed bool
	wg     sync.WaitGroup
	NoSync bool // write() only: survives a process crash, not a host crash

	Batches, Syncs, Bytes atomic.Int64
	SyncNanos             atomic.Int64
}

// New starts a writer over files, appending after their current ends.
func New(files []*os.File) *Writer {
	w := &Writer{files: files, sizes: make([]int64, len(files)), kick: make(chan struct{}, 1)}

	for i, f := range files {
		if st, err := f.Stat(); err == nil {
			w.sizes[i] = st.Size()
		}
	}

	w.cur, w.spare = w.newBatch(), w.newBatch()
	w.wg.Add(1)

	go w.loop()

	return w
}

func (w *Writer) newBatch() *batch {
	return &batch{bufs: make([]fileBuf, len(w.files)), done: make(chan struct{})}
}

// Append frames rec onto file i and returns the batch's durability channel
// and error getter.
func (w *Writer) Append(i int, rec []byte) (<-chan struct{}, func() error) {
	w.mu.Lock()

	if w.closed {
		w.mu.Unlock()

		return nil, func() error { return ErrClosed }
	}

	b := w.cur
	fb := &b.bufs[i]
	fb.app = binary.LittleEndian.AppendUint32(fb.app, uint32(len(rec)))
	fb.app = binary.LittleEndian.AppendUint32(fb.app, crc32.Checksum(rec, crcTable))
	fb.app = append(fb.app, rec...)
	w.mu.Unlock()
	w.signal()

	return b.done, func() error { return b.err }
}

// AppendMulti frames one record per (file, rec) pair into the same batch, so
// they become durable together (but are NOT atomic across files on a crash).
func (w *Writer) AppendMulti(idx []int, recs [][]byte) (<-chan struct{}, func() error) {
	w.mu.Lock()
	b := w.cur

	for n, i := range idx {
		fb := &b.bufs[i]
		fb.app = binary.LittleEndian.AppendUint32(fb.app, uint32(len(recs[n])))
		fb.app = binary.LittleEndian.AppendUint32(fb.app, crc32.Checksum(recs[n], crcTable))
		fb.app = append(fb.app, recs[n]...)
	}

	w.mu.Unlock()
	w.signal()

	return b.done, func() error { return b.err }
}

// WriteAt queues an overwrite of file i at off (for slot files). rec is
// copied.
func (w *Writer) WriteAt(i int, off int64, rec []byte) (<-chan struct{}, func() error) {
	w.mu.Lock()
	b := w.cur
	b.bufs[i].pw = append(b.bufs[i].pw, pwrite{off, append([]byte(nil), rec...)})
	w.mu.Unlock()
	w.signal()

	return b.done, func() error { return b.err }
}

// Size returns the logical end of file i including queued appends.
func (w *Writer) Size(i int) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.sizes[i] + int64(len(w.cur.bufs[i].app))
}

// Wait blocks until done is closed and returns its error.
func Wait(done <-chan struct{}, errf func() error) error {
	if done != nil {
		<-done
	}

	return errf()
}

// WaitWithin is Wait with a deadline, like ReserveWriteWait.
func WaitWithin(done <-chan struct{}, errf func() error, d time.Duration) (bool, error) {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-done:
		return true, errf()
	case <-t.C:
		return false, nil
	}
}

func (w *Writer) signal() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

func (w *Writer) loop() {
	defer w.wg.Done()

	for range w.kick {
		w.mu.Lock()
		b := w.cur
		empty := true

		for i := range b.bufs {
			if len(b.bufs[i].app) > 0 || len(b.bufs[i].pw) > 0 {
				empty = false
			}

			w.sizes[i] += int64(len(b.bufs[i].app))
		}

		closing := w.closed

		if !empty {
			w.cur = w.spare
			w.cur.done = make(chan struct{})
			w.cur.err = nil
		}
		w.mu.Unlock()

		if !empty {
			b.err = w.flush(b)
			close(b.done)

			for i := range b.bufs {
				b.bufs[i].app = b.bufs[i].app[:0]
				b.bufs[i].pw = b.bufs[i].pw[:0]
			}

			w.mu.Lock()
			w.spare = b
			w.mu.Unlock()
		}

		if closing && empty {
			return
		}
	}
}

func (w *Writer) flush(b *batch) error {
	w.Batches.Add(1)

	var (
		wg   sync.WaitGroup
		errs = make([]error, len(b.bufs))
	)

	start := time.Now()

	for i := range b.bufs {
		fb := &b.bufs[i]
		if len(fb.app) == 0 && len(fb.pw) == 0 {
			continue
		}

		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = w.flushFile(i, fb)
		}()
	}

	wg.Wait()
	w.SyncNanos.Add(int64(time.Since(start)))

	return errors.Join(errs...)
}

func (w *Writer) flushFile(i int, fb *fileBuf) error {
	f := w.files[i]

	if len(fb.app) > 0 {
		if _, err := f.Write(fb.app); err != nil {
			return err
		}

		w.Bytes.Add(int64(len(fb.app)))
	}

	for _, p := range fb.pw {
		if _, err := f.WriteAt(p.b, p.off); err != nil {
			return err
		}

		w.Bytes.Add(int64(len(p.b)))
	}

	if w.NoSync {
		return nil
	}

	w.Syncs.Add(1)

	return unix.Fdatasync(int(f.Fd()))
}

// Close flushes what is queued and stops the writer.
func (w *Writer) Close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.signal()

	done := make(chan struct{})

	go func() {
		w.wg.Wait()
		close(done)
	}()

	for {
		select {
		case <-done:
			return
		case <-time.After(10 * time.Millisecond):
			w.signal()
		}
	}
}

// Scan calls fn for every intact framed record in r from the start, stopping
// at the first torn or corrupt frame. It returns the offset just after the
// last good record, which is where a recovering writer truncates to.
func Scan(r io.Reader, fn func(off int64, rec []byte) error) (int64, error) {
	br := newBufReader(r)

	var off int64

	hdr := make([]byte, FrameHeader)
	rec := make([]byte, 0, 64<<10)

	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			return off, nil //nolint:nilerr // EOF or torn header: end of good data
		}

		n := binary.LittleEndian.Uint32(hdr)
		if n > 1<<30 {
			return off, nil
		}

		if cap(rec) < int(n) {
			rec = make([]byte, n)
		}

		rec = rec[:n]
		if _, err := io.ReadFull(br, rec); err != nil {
			return off, nil //nolint:nilerr // torn body
		}

		if crc32.Checksum(rec, crcTable) != binary.LittleEndian.Uint32(hdr[4:]) {
			return off, nil
		}

		if err := fn(off+FrameHeader, rec); err != nil {
			return off, err
		}

		off += FrameHeader + int64(n)
	}
}

func newBufReader(r io.Reader) io.Reader { return bufio.NewReaderSize(r, 4<<20) }
