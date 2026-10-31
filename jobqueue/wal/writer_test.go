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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// testSegmentSize is big enough that tests not about rotation stay in one
	// file.
	testSegmentSize = 1 << 20

	// testTimeout bounds every wait for something the writer must do soon, so
	// a writer that never does it fails the test instead of hanging it.
	testTimeout = 10 * time.Second
)

func TestNewWriterUnlistableDir(t *testing.T) {
	Convey("NewWriter in a directory it can write to but not list returns an error", t, func() {
		dir := t.TempDir()

		Reset(func() { os.Chmod(dir, 0o700) }) //nolint:errcheck // restores the TempDir for cleanup

		So(os.Chmod(dir, 0o300), ShouldBeNil)

		w, err := NewWriter(dir, 1, testSegmentSize, nil)
		So(err, ShouldNotBeNil)
		So(w, ShouldBeNil)
	})
}

func TestNewWriterMissingDir(t *testing.T) {
	Convey("NewWriter in a directory that does not exist returns an error", t, func() {
		w, err := NewWriter(filepath.Join(t.TempDir(), "missing"), 1, testSegmentSize, nil)
		So(err, ShouldNotBeNil)
		So(w, ShouldBeNil)
	})
}

func TestParseHeader(t *testing.T) {
	Convey("ParseHeader recognises only a header record", t, func() {
		prevFile, prevEnd, ok := ParseHeader(appendHeader(nil, 7, 1<<40))
		So(ok, ShouldBeTrue)
		So(prevFile, ShouldEqual, 7)
		So(prevEnd, ShouldEqual, uint64(1<<40))

		hdr := appendHeader(nil, 7, 1<<40)

		_, _, ok = ParseHeader(hdr[:len(hdr)-1])
		So(ok, ShouldBeFalse)

		_, _, ok = ParseHeader(append(hdr, 0))
		So(ok, ShouldBeFalse)

		hdr[0] = 'X'
		_, _, ok = ParseHeader(hdr)
		So(ok, ShouldBeFalse)

		_, _, ok = ParseHeader([]byte("WRWALHDR"))
		So(ok, ShouldBeFalse)
	})
}

func TestRetryDelay(t *testing.T) {
	Convey("retryDelay is none after the first failure, then 100ms doubling, capped, without overflow", t, func() {
		const ms = time.Millisecond

		for _, c := range []struct {
			failures int
			limit    time.Duration
			want     time.Duration
		}{
			{0, 300 * ms, 0}, {1, 300 * ms, 0}, {2, 300 * ms, 100 * ms}, {3, 300 * ms, 200 * ms},
			{4, 300 * ms, 300 * ms}, {5, 300 * ms, 300 * ms}, {37, 300 * ms, 300 * ms},
			{64, 300 * ms, 300 * ms}, {1000, 300 * ms, 300 * ms},
			{0, 5 * time.Second, 0}, {1, 5 * time.Second, 0}, {2, 5 * time.Second, 100 * ms},
			{3, 5 * time.Second, 200 * ms}, {4, 5 * time.Second, 400 * ms}, {5, 5 * time.Second, 800 * ms},
			{37, 5 * time.Second, 5 * time.Second}, {64, 5 * time.Second, 5 * time.Second},
			{1000, 5 * time.Second, 5 * time.Second},
		} {
			So(retryDelay(c.failures, c.limit), ShouldEqual, c.want)
		}
	})
}

// syncHold is a sync hook that blocks its first call until released.
type syncHold struct {
	entered, release chan struct{}
	calls            atomic.Int32
	releaseOnce      sync.Once
	firstErr         error
}

// newSyncHold returns a hold whose first sync returns firstErr once released,
// installed with setSyncHook and released when the Convey block ends, so a
// failing test never leaves a writer blocked.
func newSyncHold(firstErr error) *syncHold {
	h := &syncHold{entered: make(chan struct{}), release: make(chan struct{}), firstErr: firstErr}

	Reset(h.Release)
	setSyncHook(h.hook)

	return h
}

func (h *syncHold) hook(string) error {
	if h.calls.Add(1) != 1 {
		return nil
	}

	close(h.entered)
	<-h.release

	return h.firstErr
}

// Release lets the held sync go on.
func (h *syncHold) Release() {
	h.releaseOnce.Do(func() { close(h.release) })
}

// awaitEntered waits for the held sync to start.
func (h *syncHold) awaitEntered() {
	select {
	case <-h.entered:
	case <-time.After(testTimeout):
		So("the held sync never started", ShouldBeEmpty)
	}
}

func TestWriterSyncHold(t *testing.T) {
	Convey("Given a sync hook holding the first sync", t, func() {
		hold := newSyncHold(nil)
		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		Convey("an appended record is not durable while held, and is once released", func() {
			_, c := w.Append([]byte("a"), nil)

			done, err := c.WaitWithin(50 * time.Millisecond)
			So(done, ShouldBeFalse)
			So(err, ShouldBeNil)

			hold.awaitEntered()

			done, err = c.WaitWithin(0)
			So(done, ShouldBeFalse)
			So(err, ShouldBeNil)

			file, off := w.Durable()
			So(file, ShouldEqual, 1)
			So(off, ShouldEqual, headerEnd)

			hold.Release()
			So(c.Wait(), ShouldBeNil)

			done, err = c.WaitWithin(0)
			So(done, ShouldBeTrue)
			So(err, ShouldBeNil)

			file, off = w.Durable()
			So(file, ShouldEqual, 1)
			So(off, ShouldEqual, fileSize(segment(dir, 1)))
			So(off, ShouldEqual, headerEnd+FrameHeader+1)
		})
	})
}

// newTestWriter returns a writer of file 1 in dir, closed when the Convey
// block ends.
func newTestWriter(dir string, segmentSize int64, onError func(error)) *Writer {
	w, err := NewWriter(dir, 1, segmentSize, onError)
	So(err, ShouldBeNil)

	Reset(func() { w.Close() })

	return w
}

// fileSize returns the size of the file at path.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	So(err, ShouldBeNil)

	return info.Size()
}

// segment returns the path of segment n in dir.
func segment(dir string, n uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%08x.wal", n))
}

func TestWriterSyncError(t *testing.T) {
	Convey("Given a sync hook returning EIO once", t, func() {
		var (
			calls       atomic.Int32
			mu          sync.Mutex
			syncedAfter []string
		)

		setSyncHook(func(path string) error {
			if calls.Add(1) == 1 {
				return syscall.EIO
			}

			mu.Lock()
			defer mu.Unlock()

			syncedAfter = append(syncedAfter, path)

			return nil
		})

		var onErrors atomic.Int32

		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, func(error) { onErrors.Add(1) })
		sizeBefore := fileSize(segment(dir, 1))

		Convey("record a fails, then b in a later batch is durable in file 2, whose header names file 1's "+
			"durable end, and nothing more is written to file 1", func() {
			results := newResults()

			refA, cA := w.Append([]byte("a"), results.done(0))
			So(refA.File, ShouldEqual, 1)

			errA := waitFor(cA)
			So(errA, ShouldWrap, syscall.EIO)
			So(results.calls(0), ShouldResemble, []error{errA})
			So(onErrors.Load(), ShouldEqual, 1)

			sizeAfterA := fileSize(segment(dir, 1))
			So(sizeAfterA, ShouldEqual, sizeBefore+FrameHeader+1)

			refB, cB := w.Append([]byte("b"), results.done(1))
			So(waitFor(cB), ShouldBeNil)
			So(results.calls(1), ShouldResemble, []error{nil})
			So(refB, ShouldResemble, Ref{File: 2, Off: uint64(headerEnd)})

			So(w.Close(), ShouldBeNil)
			So(fileSize(segment(dir, 1)), ShouldEqual, sizeAfterA)

			mu.Lock()
			So(syncedAfter, ShouldNotContain, segment(dir, 1))
			So(syncedAfter, ShouldContain, segment(dir, 2))
			mu.Unlock()

			s := scanSegment(dir, 2)
			So(s.isHeader, ShouldBeTrue)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, sizeBefore)
			So(s.recs, ShouldResemble, []string{"b"})
			So(s.refs, ShouldResemble, []Ref{refB})
			So(onErrors.Load(), ShouldEqual, 1)

			file, off := w.Durable()
			So(file, ShouldEqual, 2)
			So(off, ShouldEqual, fileSize(segment(dir, 2)))
		})
	})

	Convey("Given a sync hook holding the first sync then returning EIO", t, func() {
		hold := newSyncHold(syscall.EIO)

		var onErrors atomic.Int32

		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, func(error) { onErrors.Add(1) })
		sizeBefore := fileSize(segment(dir, 1))

		Convey("a record queued behind the failing batch, with a Ref in the abandoned file, fails too and is "+
			"never written, and the next record goes to file 2", func() {
			results := newResults()

			_, cA := w.Append([]byte("a"), results.done(0))

			hold.awaitEntered()

			refB, cB := w.Append([]byte("b"), results.done(1))
			So(refB.File, ShouldEqual, 1)

			hold.Release()

			errA, errB := waitFor(cA), waitFor(cB)
			So(errA, ShouldWrap, syscall.EIO)
			So(errB, ShouldWrap, syscall.EIO)
			So(results.calls(0), ShouldResemble, []error{errA})
			So(results.calls(1), ShouldResemble, []error{errB})
			So(onErrors.Load(), ShouldEqual, 2)

			refC, cC := w.Append([]byte("c"), nil)
			So(waitFor(cC), ShouldBeNil)
			So(refC, ShouldResemble, Ref{File: 2, Off: uint64(headerEnd)})

			So(fileSize(segment(dir, 1)), ShouldEqual, sizeBefore+FrameHeader+1)

			s := scanSegment(dir, 2)
			So(s.prevEnd, ShouldEqual, sizeBefore)
			So(s.recs, ShouldResemble, []string{"c"})
		})
	})
}

// setSyncHook makes hook govern the syncs of writers made after it, until the
// enclosing Convey block ends.
func setSyncHook(hook func(path string) error) {
	Reset(SetTestSyncHook(hook))
}

func newResults() *results {
	return &results{errs: make(map[int][]error)}
}

// waitFor returns c's result, failing the test if it takes over testTimeout.
func waitFor(c Commit) error {
	done, err := c.WaitWithin(testTimeout)
	So(done, ShouldBeTrue)

	return err
}

// scanSegment scans segment n of dir, splitting off its header, with each
// record's Ref naming n. It requires the whole file to scan.
func scanSegment(dir string, n uint32) segmentScan {
	data, err := os.ReadFile(segment(dir, n))
	So(err, ShouldBeNil)

	s := segmentScan{scanned: scanAll(data)}
	So(s.err, ShouldBeNil)
	So(s.end, ShouldEqual, len(data))
	So(s.recs, ShouldNotBeEmpty)

	s.prevFile, s.prevEnd, s.isHeader = ParseHeader([]byte(s.recs[0]))
	So(s.refs[0], ShouldResemble, Ref{})

	s.recs, s.refs = s.recs[1:], s.refs[1:]
	for i := range s.refs {
		s.refs[i].File = n
	}

	return s
}

func TestWriterClose(t *testing.T) {
	Convey("Given a sync hook holding the first sync", t, func() {
		hold := newSyncHold(nil)
		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		Convey("Close while held returns nil after the release, every queued record durable and its done "+
			"func called with nil; later appends get ErrClosed", func() {
			const n = 50

			results := newResults()
			recs := make([]string, n)
			refs := make([]Ref, n)

			// The first record's batch holds the sync, so the other 49 are
			// still queued when Close is called.
			for i := range n {
				recs[i] = fmt.Sprintf("record %d", i)
				refs[i], _ = w.Append([]byte(recs[i]), results.done(i))

				if i == 0 {
					hold.awaitEntered()
				}
			}

			closed := make(chan error, 1)

			go func() { closed <- w.Close() }()

			select {
			case <-closed:
				So("Close returned while a sync was held", ShouldBeEmpty)
			case <-time.After(50 * time.Millisecond):
			}

			hold.Release()

			select {
			case err := <-closed:
				So(err, ShouldBeNil)
			case <-time.After(testTimeout):
				So("Close never returned", ShouldBeEmpty)
			}

			notNil := 0

			for i := range n {
				if got := results.calls(i); len(got) != 1 || got[0] != nil {
					notNil++
				}
			}

			So(notNil, ShouldEqual, 0)

			late := newResults()
			ref, c := w.Append([]byte("late"), late.done(0))
			So(late.calls(0), ShouldResemble, []error{ErrClosed})
			So(waitFor(c), ShouldEqual, ErrClosed)
			So(ref, ShouldResemble, Ref{})

			_, c = w.AppendBlock(mustBlock("late"), 1, nil)
			So(waitFor(c), ShouldEqual, ErrClosed)

			_, err := w.Rotate()
			So(err, ShouldEqual, ErrClosed)

			So(w.Close(), ShouldBeNil)

			s := scanSegment(dir, 1)
			So(s.recs, ShouldResemble, recs)
			So(s.refs, ShouldResemble, refs)
		})
	})
}

// mustBlock returns recs as one s2 block.
func mustBlock(recs ...string) []byte {
	block, err := AppendBlock(nil, CodecS2, appendFrames(recs...))
	So(err, ShouldBeNil)

	return block
}

func TestWriterRotatesBySize(t *testing.T) {
	Convey("Given segments smaller than a record, each record goes at the start of the segment it is "+
		"appended to, leaving none empty", t, func() {
		dir := t.TempDir()
		w := newTestWriter(dir, headerEnd+10, nil)

		for i := range 2 {
			ref, c := w.Append(fmt.Appendf(nil, "%099d", i), nil)
			So(waitFor(c), ShouldBeNil)
			So(ref, ShouldResemble, Ref{File: uint32(i + 1), Off: uint64(headerEnd)})
		}

		So(segmentFiles(dir), ShouldResemble, []uint32{1, 2})
	})

	Convey("Given segments with room for a 20 byte record and a 10 byte one, and a sync hook holding the "+
		"first sync then returning EIO", t, func() {
		hold := newSyncHold(syscall.EIO)
		dir := t.TempDir()
		w := newTestWriter(dir, headerEnd+FrameHeader+20+FrameHeader+10-2, nil)

		Convey("a failure in file 1 does not fail a batch already queued for file 2, and later records "+
			"follow it in file 2", func() {
			_, cA := w.Append([]byte("aaaaaaaaaaaaaaaaaaaa"), nil)

			hold.awaitEntered()

			refB, cB := w.Append([]byte("bbbbbbbbbb"), nil)
			So(refB, ShouldResemble, Ref{File: 2, Off: uint64(headerEnd)})

			hold.Release()
			So(waitFor(cA), ShouldWrap, syscall.EIO)
			So(waitFor(cB), ShouldBeNil)

			refC, cC := w.Append([]byte("cccccccccc"), nil)
			So(waitFor(cC), ShouldBeNil)
			So(refC, ShouldResemble, Ref{File: 2, Off: uint64(headerEnd) + FrameHeader + 10})

			s := scanSegment(dir, 2)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, headerEnd)
			So(s.recs, ShouldResemble, []string{"bbbbbbbbbb", "cccccccccc"})
		})
	})

	Convey("Given segments sized to hold exactly 3 records of 10 bytes", t, func() {
		dir := t.TempDir()
		w := newTestWriter(dir, headerEnd+3*(FrameHeader+10), nil)

		Convey("3 records fill file 1 to that size and a 4th starts file 2", func() {
			refs := make([]Ref, 0, 4)

			for i := range 4 {
				ref, c := w.Append(fmt.Appendf(nil, "%010d", i), nil)
				So(waitFor(c), ShouldBeNil)

				refs = append(refs, ref)
			}

			So(refs[2].File, ShouldEqual, 1)
			So(refs[3], ShouldResemble, Ref{File: 2, Off: uint64(headerEnd)})
			So(fileSize(segment(dir, 1)), ShouldEqual, headerEnd+3*(FrameHeader+10))
		})
	})

	Convey("Given a writer with 4KiB segments", t, func() {
		const (
			n       = 100
			recSize = 100
		)

		dir := t.TempDir()
		w := newTestWriter(dir, 4<<10, nil)

		Convey("100 records of 100 bytes fill several files, each record whole in the file its Ref names", func() {
			refs := make([]Ref, n)
			commits := make([]Commit, n)
			recs := make([]string, n)

			for i := range n {
				recs[i] = fmt.Sprintf("%-*d", recSize, i)
				refs[i], commits[i] = w.Append([]byte(recs[i]), nil)
			}

			failed := 0

			for _, c := range commits {
				if waitFor(c) != nil {
					failed++
				}
			}

			So(failed, ShouldEqual, 0)

			files := segmentFiles(dir)
			So(len(files), ShouldBeGreaterThanOrEqualTo, 2)
			So(files[0], ShouldEqual, 1)
			So(files[len(files)-1], ShouldEqual, len(files))

			var (
				scannedRefs []Ref
				scannedRecs []string
			)

			for i, file := range files {
				So(fileSize(segment(dir, file)), ShouldBeLessThanOrEqualTo, 4<<10)

				s := scanSegment(dir, file)
				So(s.isHeader, ShouldBeTrue)

				if i > 0 {
					So(s.prevFile, ShouldEqual, files[i-1])
					So(s.prevEnd, ShouldEqual, fileSize(segment(dir, files[i-1])))
				}

				scannedRefs = append(scannedRefs, s.refs...)
				scannedRecs = append(scannedRecs, s.recs...)
			}

			So(scannedRecs, ShouldResemble, recs)
			So(scannedRefs, ShouldResemble, refs)
		})
	})
}

// segmentFiles returns the numbers of the segments in dir, in order.
func segmentFiles(dir string) []uint32 {
	paths, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	So(err, ShouldBeNil)

	nums := make([]uint32, 0, len(paths))

	for _, path := range paths {
		var n uint32

		_, err = fmt.Sscanf(filepath.Base(path), "%08x.wal", &n)
		So(err, ShouldBeNil)

		nums = append(nums, n)
	}

	slices.Sort(nums)

	return nums
}

func TestWriterFailureWithinATake(t *testing.T) {
	Convey("Given one 10 byte record per segment and a sync hook holding the first sync, then failing the "+
		"second", t, func() {
		var calls atomic.Int32

		entered, release := make(chan struct{}), make(chan struct{})

		Reset(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})
		setSyncHook(func(string) error {
			switch calls.Add(1) {
			case 1:
				close(entered)
				<-release
			case 2:
				return syscall.EIO
			}

			return nil
		})

		dir := t.TempDir()
		w := newTestWriter(dir, headerEnd+FrameHeader+10, nil)

		Convey("a failure opening file 2 fails only its batch; file 3's batch in the same take is durable", func() {
			_, cX := w.Append([]byte("xxxxxxxxxx"), nil)

			select {
			case <-entered:
			case <-time.After(testTimeout):
				So("the held sync never started", ShouldBeEmpty)
			}

			refA, cA := w.Append([]byte("aaaaaaaaaa"), nil)
			refB, cB := w.Append([]byte("bbbbbbbbbb"), nil)

			So(refA.File, ShouldEqual, 2)
			So(refB.File, ShouldEqual, 3)

			close(release)

			So(waitFor(cX), ShouldBeNil)
			So(waitFor(cA), ShouldWrap, syscall.EIO)
			So(waitFor(cB), ShouldBeNil)
			So(recordAt(dir, refB), ShouldEqual, "bbbbbbbbbb")

			// file 2's failed start left nothing durable, so file 2 was
			// created again, holding no record, before file 3.
			s2 := scanSegment(dir, 2)
			So(s2.prevFile, ShouldEqual, 1)
			So(s2.prevEnd, ShouldEqual, fileSize(segment(dir, 1)))
			So(s2.recs, ShouldBeEmpty)

			s := scanSegment(dir, 3)
			So(s.prevFile, ShouldEqual, 2)
			So(s.prevEnd, ShouldEqual, headerEnd)
		})
	})

	Convey("Given segments with room for two 10 byte records and a sync hook holding the first sync then "+
		"returning EIO", t, func() {
		hold := newSyncHold(syscall.EIO)

		var onErrors atomic.Int32

		dir := t.TempDir()
		w := newTestWriter(dir, headerEnd+2*(FrameHeader+10), func(error) { onErrors.Add(1) })

		Convey("when file 1 fails, its queued batch gets the sync error and file 2's queued batch is durable",
			func() {
				_, cX := w.Append([]byte("xxxxxxxxxx"), nil)

				hold.awaitEntered()

				refY, cY := w.Append([]byte("yyyyyyyyyy"), nil)
				refZ, cZ := w.Append([]byte("zzzzzzzzzz"), nil)

				So(refY.File, ShouldEqual, 1)
				So(refZ.File, ShouldEqual, 2)

				hold.Release()

				So(waitFor(cX), ShouldWrap, syscall.EIO)
				So(waitFor(cY), ShouldWrap, syscall.EIO)
				So(waitFor(cZ), ShouldBeNil)
				So(onErrors.Load(), ShouldEqual, 2)
				So(recordAt(dir, refZ), ShouldEqual, "zzzzzzzzzz")
			})
	})
}

// recordAt returns the record at ref in dir's segments.
func recordAt(dir string, ref Ref) string {
	f, err := os.Open(segment(dir, ref.File))
	So(err, ShouldBeNil)

	defer f.Close()

	rec, err := ReadAt(f, ref)
	So(err, ShouldBeNil)

	return string(rec)
}

func TestWriterDoneOrder(t *testing.T) {
	Convey("Given a sync hook holding the first sync", t, func() {
		hold := newSyncHold(nil)
		w := newTestWriter(t.TempDir(), testSegmentSize, nil)

		Convey("the done funcs of a batch run in Ref order", func() {
			_, _ = w.Append([]byte("x"), nil)

			hold.awaitEntered()

			var (
				mu    sync.Mutex
				order []int
				last  Commit
			)

			for i := range 5 {
				_, last = w.Append([]byte("r"), func(error) {
					mu.Lock()
					defer mu.Unlock()

					order = append(order, i)
				})
			}

			hold.Release()
			So(waitFor(last), ShouldBeNil)

			mu.Lock()
			defer mu.Unlock()

			So(order, ShouldResemble, []int{0, 1, 2, 3, 4})
		})
	})
}

func TestWriterAppendBlocks(t *testing.T) {
	Convey("Given a writer whose first sync is held", t, func() {
		hold := newSyncHold(nil)
		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		_, first := w.Append([]byte("first"), nil)

		hold.awaitEntered()

		Convey("blocks appended together share one batch, each record at its Ref", func() {
			res := newResults()

			refs, c := w.AppendBlocks([][]byte{mustBlock("a", "b"), mustBlock("c")}, []int{2, 1},
				[]func(error){res.done(0), res.done(1)})

			hold.Release()
			So(waitFor(first), ShouldBeNil)
			So(waitFor(c), ShouldBeNil)

			So(res.calls(0), ShouldResemble, []error{nil})
			So(res.calls(1), ShouldResemble, []error{nil})
			So(w.Stats().Batches, ShouldEqual, 2)
			So(w.Stats().Records, ShouldEqual, 4)

			So(recordAt(dir, Ref{File: refs[0].File, Off: refs[0].Off, Idx: 1}), ShouldEqual, "b")
			So(recordAt(dir, refs[1]), ShouldEqual, "c")
		})

		Convey("after Close, each done gets ErrClosed", func() {
			hold.Release()
			So(w.Close(), ShouldBeNil)

			res := newResults()

			_, c := w.AppendBlocks([][]byte{mustBlock("a"), mustBlock("b")}, []int{1, 1},
				[]func(error){res.done(0), res.done(1)})
			So(c.Wait(), ShouldEqual, ErrClosed)
			So(res.calls(0), ShouldResemble, []error{ErrClosed})
			So(res.calls(1), ShouldResemble, []error{ErrClosed})
		})
	})

	Convey("Given a durable record, in segments with room after it for block a but not also block b", t, func() {
		dir := t.TempDir()
		a, b := mustBlock("a"), mustBlock("b")
		w := newTestWriter(dir, headerEnd+FrameHeader+5+int64(len(a)+len(b))-1, nil)

		_, c := w.Append([]byte("first"), nil)
		So(waitFor(c), ShouldBeNil)

		Convey("a and b appended together both go in segment 2, so no rotation separates them", func() {
			refs, cb := w.AppendBlocks([][]byte{a, b}, []int{1, 1}, []func(error){nil, nil})
			So(waitFor(cb), ShouldBeNil)

			So(refs, ShouldResemble, []Ref{
				{File: 2, Off: uint64(headerEnd)},
				{File: 2, Off: uint64(headerEnd) + uint64(len(a))},
			})
			So(recordAt(dir, refs[0]), ShouldEqual, "a")
			So(recordAt(dir, refs[1]), ShouldEqual, "b")
		})

		Convey("blocks that together exceed a whole segment still all go in segment 2", func() {
			big := mustBlock(strings.Repeat("x", 4*len(a)))

			refs, cb := w.AppendBlocks([][]byte{a, big, b}, []int{1, 1, 1}, []func(error){nil, nil, nil})
			So(waitFor(cb), ShouldBeNil)

			for i, rec := range []string{"a", strings.Repeat("x", 4*len(a)), "b"} {
				So(refs[i].File, ShouldEqual, 2)
				So(recordAt(dir, refs[i]), ShouldEqual, rec)
			}

			_, cn := w.Append([]byte("next"), nil)
			So(waitFor(cn), ShouldBeNil)
			So(segmentFiles(dir), ShouldResemble, []uint32{1, 2, 3})
		})
	})
}

// syncRecorder is a sync hook that records the paths synced.
type syncRecorder struct {
	mu     sync.Mutex
	synced []string
}

// newSyncRecorder returns a recorder installed with setSyncHook.
func newSyncRecorder() *syncRecorder {
	r := &syncRecorder{}
	setSyncHook(r.hook)

	return r
}

func (r *syncRecorder) hook(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.synced = append(r.synced, path)

	return nil
}

// reset forgets the paths synced so far.
func (r *syncRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.synced = nil
}

// paths returns the paths synced since the last reset, in order.
func (r *syncRecorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.synced)
}

func TestWriterRotate(t *testing.T) {
	Convey("Given a writer that has made record a durable", t, func() {
		synced := newSyncRecorder()
		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		refA, c := w.Append([]byte("a"), nil)
		So(waitFor(c), ShouldBeNil)
		So(refA, ShouldResemble, Ref{File: 1, Off: uint64(headerEnd)})

		Convey("Rotate seals file 1, makes file 2 durable at once, and later records go to file 2", func() {
			synced.reset()

			sealed, err := w.Rotate()

			So(synced.paths(), ShouldResemble, []string{segment(dir, 2), segment(dir, 1), dir})

			So(err, ShouldBeNil)
			So(sealed, ShouldEqual, 1)

			file, off := w.Durable()
			So(file, ShouldEqual, 2)
			So(off, ShouldEqual, headerEnd)

			s := scanSegment(dir, 2)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, fileSize(segment(dir, 1)))
			So(s.recs, ShouldBeEmpty)

			refB, c := w.Append([]byte("b"), nil)
			So(waitFor(c), ShouldBeNil)
			So(refB, ShouldResemble, Ref{File: 2, Off: uint64(headerEnd)})
			So(recordAt(dir, refA), ShouldEqual, "a")
			So(recordAt(dir, refB), ShouldEqual, "b")
		})
	})

	Convey("Given a sync hook failing the sync that would open the rotated file", t, func() {
		var calls atomic.Int32

		setSyncHook(func(string) error {
			if calls.Add(1) == 1 {
				return syscall.EIO
			}

			return nil
		})

		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		Convey("Rotate returns the error, and the next record goes to the file after, with file 2 made again "+
			"holding no record, so every header names the file before", func() {
			_, err := w.Rotate()
			So(err, ShouldWrap, syscall.EIO)

			ref, c := w.Append([]byte("a"), nil)
			So(waitFor(c), ShouldBeNil)
			So(ref, ShouldResemble, Ref{File: 3, Off: uint64(headerEnd)})

			s2 := scanSegment(dir, 2)
			So(s2.prevFile, ShouldEqual, 1)
			So(s2.prevEnd, ShouldEqual, fileSize(segment(dir, 1)))
			So(s2.recs, ShouldBeEmpty)

			s := scanSegment(dir, 3)
			So(s.prevFile, ShouldEqual, 2)
			So(s.prevEnd, ShouldEqual, headerEnd)
			So(s.recs, ShouldResemble, []string{"a"})
		})
	})
}

// segmentScan is what a scan of one segment saw.
type segmentScan struct {
	prevFile uint32
	prevEnd  uint64
	isHeader bool
	scanned
}

func TestNewWriterAfterSegmentZero(t *testing.T) {
	Convey("Given a directory holding only segment 0, a writer of file 1 names it and its size", t, func() {
		dir := t.TempDir()

		So(os.WriteFile(segment(dir, 0), make([]byte, 40), 0o600), ShouldBeNil)

		w, err := NewWriter(dir, 1, testSegmentSize, nil)
		So(err, ShouldBeNil)
		So(w.Close(), ShouldBeNil)

		s := scanSegment(dir, 1)
		So(s.prevFile, ShouldEqual, 0)
		So(s.prevEnd, ShouldEqual, 40)
	})
}

func TestWriterGroupCommit(t *testing.T) {
	Convey("Given a writer whose first sync waits until 1,000 goroutines have appended", t, func() {
		const n = 1000

		appended := make(chan struct{})

		setSyncHook(func(string) error {
			<-appended

			return nil
		})

		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		Convey("each goroutine's record is durable at its Ref, its done func having run first, in few syncs",
			func() {
				var (
					appends, waits sync.WaitGroup
					refs           [n]Ref
					errs           [n]error
					doneFirst      [n]bool
					doneRan        [n]atomic.Bool
				)

				appends.Add(n)

				for i := range n {
					waits.Go(func() {
						ref, c := w.Append(fmt.Appendf(nil, "record %d", i), func(error) { doneRan[i].Store(true) })

						appends.Done()

						refs[i], errs[i] = ref, c.Wait()
						doneFirst[i] = doneRan[i].Load()
					})
				}

				appends.Wait()
				close(appended)
				waits.Wait()

				So(slices.DeleteFunc(errs[:], func(err error) bool { return err == nil }), ShouldBeEmpty)
				So(slices.Index(doneFirst[:], false), ShouldEqual, -1)

				stats := w.Stats()
				So(stats.Records, ShouldEqual, n)
				So(stats.Syncs, ShouldBeLessThan, n)
				So(stats.Syncs, ShouldBeGreaterThan, 0)
				So(stats.Batches, ShouldEqual, stats.Syncs)
				So(stats.SyncTime, ShouldBeGreaterThan, 0)
				So(stats.Bytes, ShouldEqual, fileSize(segment(dir, 1))-headerEnd)

				s := scanSegment(dir, 1)
				So(s.isHeader, ShouldBeTrue)
				So(s.prevFile, ShouldEqual, 0)
				So(s.prevEnd, ShouldEqual, 0)
				So(s.recs, ShouldHaveLength, n)

				atRef := make(map[Ref]string, n)
				for i, ref := range s.refs {
					atRef[ref] = s.recs[i]
				}

				misplaced := 0

				for i, ref := range refs {
					if atRef[ref] != fmt.Sprintf("record %d", i) {
						misplaced++
					}
				}

				So(misplaced, ShouldEqual, 0)
			})
	})
}

func TestWriterRetries(t *testing.T) {
	Convey("Given a sync hook returning EIO for every sync, and walRetryCap 200ms", t, func() {
		Reset(SetTestRetryCap(200 * time.Millisecond))

		var (
			failing atomic.Bool
			retried atomic.Int64
		)

		failing.Store(true)
		setSyncHook(func(string) error {
			if failing.Load() {
				return syscall.EIO
			}

			retried.CompareAndSwap(0, time.Now().UnixNano())

			return nil
		})

		var onErrors atomic.Int32

		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, func(error) { onErrors.Add(1) })

		Convey("5 records appended 100ms apart each fail within 1s, then with the hook cleared the next is "+
			"durable in a file numbered above every earlier one", func() {
			const n = 5

			results := newResults()
			waitErrs := make([]error, 0, n+1)

			var lastRef Ref

			for i := range n {
				start := time.Now()
				ref, c := w.Append(fmt.Appendf(nil, "r%d", i), results.done(i))
				lastRef = ref

				done, err := c.WaitWithin(time.Second)
				So(done, ShouldBeTrue)
				So(err, ShouldWrap, syscall.EIO)

				waitErrs = append(waitErrs, err)

				time.Sleep(time.Until(start.Add(100 * time.Millisecond)))
			}

			So(onErrors.Load(), ShouldEqual, n)

			failing.Store(false)

			appended := time.Now()
			ref, c := w.Append([]byte("ok"), results.done(n))

			// The writer must try a sync within 1s of the append: its waits
			// are capped at 200ms. How long that sync then takes is the
			// disk's, seconds under others' fsync load, so durability only
			// has the hang guard.
			err := waitFor(c)
			So(err, ShouldBeNil)
			So(retried.Load(), ShouldBeGreaterThan, 0)
			So(time.Unix(0, retried.Load()).Sub(appended), ShouldBeLessThan, time.Second)

			waitErrs = append(waitErrs, err)

			So(ref.File, ShouldBeGreaterThan, lastRef.File)
			So(segmentFiles(dir)[len(segmentFiles(dir))-1], ShouldEqual, ref.File)
			So(recordAt(dir, ref), ShouldEqual, "ok")
			So(scanSegment(dir, ref.File).recs, ShouldResemble, []string{"ok"})

			mismatched := 0

			for i, want := range waitErrs {
				if got := results.calls(i); len(got) != 1 || !errors.Is(got[0], want) {
					mismatched++
				}
			}

			So(mismatched, ShouldEqual, 0)
			So(onErrors.Load(), ShouldEqual, n)
		})
	})
}

func TestWriterAppendBlock(t *testing.T) {
	Convey("Given a writer with a durable record", t, func() {
		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		_, c := w.Append([]byte("a"), nil)
		So(waitFor(c), ShouldBeNil)

		Convey("a pre-built block of 3 records is durable at its Ref, its records at indexes 0 to 2", func() {
			block := mustBlock("x", "yy", "zzz")

			ref, c := w.AppendBlock(block, 3, nil)
			So(waitFor(c), ShouldBeNil)
			So(ref, ShouldResemble, Ref{File: 1, Off: uint64(headerEnd) + FrameHeader + 1})
			So(w.Stats().Records, ShouldEqual, 4)

			s := scanSegment(dir, 1)
			So(s.recs, ShouldResemble, []string{"a", "x", "yy", "zzz"})
			So(s.refs[1:], ShouldResemble, []Ref{ref, {1, ref.Off, 1}, {1, ref.Off, 2}})
			So(fileSize(segment(dir, 1)), ShouldEqual, headerEnd+FrameHeader+1+int64(len(block)))
		})

		Convey("a block that is not one whole block frame is refused with a panic, and the writer goes on", func() {
			block := mustBlock("x")

			So(func() { w.AppendBlock(block[:len(block)-1], 1, nil) }, ShouldPanic)
			So(func() { w.AppendBlock(appendFrames("x"), 1, nil) }, ShouldPanic)
			So(func() { w.AppendBlock(nil, 1, nil) }, ShouldPanic)
			So(func() { w.AppendBlock([]byte{0, 0, 0, 0x80, 0, 0, 0, 0}, 1, nil) }, ShouldPanic)
			So(func() { w.Append(nil, nil) }, ShouldPanic)

			ref, c := w.Append([]byte("b"), nil)
			So(waitFor(c), ShouldBeNil)
			So(recordAt(dir, ref), ShouldEqual, "b")
		})
	})
}

func TestNewWriter(t *testing.T) {
	Convey("Given a directory holding segments 2 and 3, and a checkpoint numbered 4", t, func() {
		dir := t.TempDir()

		So(os.WriteFile(segment(dir, 2), make([]byte, 50), 0o600), ShouldBeNil)
		So(os.WriteFile(segment(dir, 3), make([]byte, 70), 0o600), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "00000004.ckpt"), make([]byte, 90), 0o600), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "000000004.wal"), make([]byte, 90), 0o600), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "00000004.wal.tmp"), make([]byte, 90), 0o600), ShouldBeNil)

		Convey("a writer of file 5 starts it with a durable header naming segment 3 and its size", func() {
			w, err := NewWriter(dir, 5, testSegmentSize, nil)
			So(err, ShouldBeNil)

			file, off := w.Durable()
			So(file, ShouldEqual, 5)
			So(off, ShouldEqual, headerEnd)
			So(w.Close(), ShouldBeNil)

			s := scanSegment(dir, 5)
			So(s.isHeader, ShouldBeTrue)
			So(s.prevFile, ShouldEqual, 3)
			So(s.prevEnd, ShouldEqual, 70)
			So(s.recs, ShouldBeEmpty)
		})

		Convey("a writer of file 1 starts it with a header naming no previous segment", func() {
			w, err := NewWriter(dir, 1, testSegmentSize, nil)
			So(err, ShouldBeNil)
			So(w.Close(), ShouldBeNil)

			s := scanSegment(dir, 1)
			So(s.prevFile, ShouldEqual, 0)
			So(s.prevEnd, ShouldEqual, 0)
		})

		Convey("a writer of an existing segment is refused, leaving it unchanged", func() {
			_, err := NewWriter(dir, 3, testSegmentSize, nil)
			So(err, ShouldNotBeNil)
			So(fileSize(segment(dir, 3)), ShouldEqual, 70)
		})
	})
}

func TestWriterReopensAfterAbandon(t *testing.T) {
	Convey("Given a writer whose first batch sync fails with EIO", t, func() {
		var failed atomic.Bool

		setSyncHook(func(string) error {
			if failed.CompareAndSwap(false, true) {
				return syscall.EIO
			}

			return nil
		})

		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		Convey("with no later append, the next segment bounds the abandoned one by Close", func() {
			_, c := w.Append([]byte("lost"), nil)
			So(waitFor(c), ShouldWrap, syscall.EIO)
			So(w.Close(), ShouldBeNil)

			So(segmentFiles(dir), ShouldResemble, []uint32{1, 2})

			s := scanSegment(dir, 2)
			So(s.isHeader, ShouldBeTrue)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, headerEnd)
			So(s.recs, ShouldBeEmpty)
			So(fileSize(segment(dir, 1)), ShouldBeGreaterThan, headerEnd)
		})
	})

	Convey("Given a writer whose syncs all fail until allowed, with a long retry cap", t, func() {
		var (
			allowed     atomic.Bool
			reopenTries atomic.Int32
		)

		Reset(SetTestRetryCap(time.Minute))

		dir := t.TempDir()

		setSyncHook(func(path string) error {
			if allowed.Load() {
				return nil
			}

			if path == segment(dir, 2) {
				reopenTries.Add(1)
			}

			return syscall.EIO
		})

		var errs atomic.Int32

		w := newTestWriter(dir, testSegmentSize, func(error) { errs.Add(1) })

		start := time.Now()

		_, c := w.Append([]byte("lost"), nil)
		So(waitFor(c), ShouldWrap, syscall.EIO)

		Convey("idle tries reuse segment 2's number, backing off between them, call no onError, and Close "+
			"makes a last try", func() {
			deadline := time.Now().Add(testTimeout)
			for reopenTries.Load() < 4 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}

			tries := reopenTries.Load()
			So(tries, ShouldBeGreaterThanOrEqualTo, 4)
			So(tries, ShouldBeLessThanOrEqualTo, maxReopenTries(time.Since(start), time.Minute))
			So(segmentFiles(dir), ShouldResemble, []uint32{1})
			So(errs.Load(), ShouldEqual, 1)

			allowed.Store(true)
			So(w.Close(), ShouldBeNil)

			So(segmentFiles(dir), ShouldResemble, []uint32{1, 2})

			s := scanSegment(dir, 2)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, headerEnd)
		})

		Convey("once syncs work again, the next record goes in segment 2 after the abandoned one", func() {
			allowed.Store(true)

			ref, c2 := w.Append([]byte("kept"), nil)
			So(waitFor(c2), ShouldBeNil)
			So(ref.File, ShouldEqual, 2)
			So(recordAt(dir, ref), ShouldEqual, "kept")

			s := scanSegment(dir, 2)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, headerEnd)
		})
	})

	Convey("Given a writer that rotates twice while a failure has left no segment open", t, func() {
		Reset(SetTestRetryCap(time.Minute))

		dir := t.TempDir()

		var allowed atomic.Bool

		setSyncHook(func(string) error {
			if allowed.Load() {
				return nil
			}

			return syscall.EIO
		})

		w := newTestWriter(dir, testSegmentSize, nil)

		_, c := w.Append([]byte("lost"), nil)
		So(waitFor(c), ShouldWrap, syscall.EIO)

		allowed.Store(true)

		_, err := w.Rotate()
		So(err, ShouldBeNil)

		ref, c2 := w.Append([]byte("kept"), nil)
		So(waitFor(c2), ShouldBeNil)

		Convey("every segment is created, each header naming the one before", func() {
			files := segmentFiles(dir)
			So(files[0], ShouldEqual, 1)
			So(files[len(files)-1], ShouldEqual, ref.File)

			for i, n := range files {
				So(n, ShouldEqual, uint32(i)+1)

				if i > 0 {
					So(scanSegment(dir, n).prevFile, ShouldEqual, n-1)
				}
			}
		})
	})
}

// maxReopenTries is the most idle reopen tries a writer can have made within
// elapsed of its first batch failing, with backoff cap limit: one at once, then
// each a retryDelay after the last.
func maxReopenTries(elapsed, limit time.Duration) int32 {
	tries := int32(1)

	for wait := retryDelay(2, limit); wait <= elapsed; wait += retryDelay(int(tries)+1, limit) {
		tries++
	}

	return tries
}

func TestWriterSpendsAnExistingNumber(t *testing.T) {
	Convey("Given a writer of file 1 in a directory already holding a file 2", t, func() {
		dir := t.TempDir()
		w := newTestWriter(dir, testSegmentSize, nil)

		So(os.WriteFile(segment(dir, 2), []byte("stray"), 0o600), ShouldBeNil)

		Convey("Rotate fails, and the next record goes to file 3, whose header skips spent file 2 to name file 1 "+
			"and its durable end", func() {
			_, err := w.Rotate()
			So(err, ShouldWrap, fs.ErrExist)

			ref, c := w.Append([]byte("a"), nil)
			So(waitFor(c), ShouldBeNil)
			So(ref, ShouldResemble, Ref{File: 3, Off: uint64(headerEnd)})

			s := scanSegment(dir, 3)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, headerEnd)
			So(s.recs, ShouldResemble, []string{"a"})
		})
	})

	Convey("Given a writer with record a durable in file 1, whose next batch fails, then whose idle open of "+
		"file 2 fails with its header not durable and file 2 left behind", t, func() {
		dir := t.TempDir()

		var failed, spent atomic.Bool

		setSyncHook(func(path string) error {
			switch {
			case path == segment(dir, 1) && failed.Load() && !spent.Load():
				return syscall.EIO
			case path == segment(dir, 2) && spent.CompareAndSwap(false, true):
				// tear the header, and leave something that cannot be removed
				return errors.Join(syscall.EIO, os.Remove(path), os.Mkdir(path, 0o700),
					os.WriteFile(filepath.Join(path, "stray"), nil, 0o600))
			default:
				return nil
			}
		})

		w := newTestWriter(dir, testSegmentSize, nil)

		refA, c := w.Append([]byte("a"), nil)
		So(waitFor(c), ShouldBeNil)

		failed.Store(true)

		_, c = w.Append([]byte("lost"), nil)
		So(waitFor(c), ShouldWrap, syscall.EIO)

		Convey("file 3's header skips spent file 2 to name file 1 and a's end, which no later header "+
			"would otherwise record", func() {
			So(w.Close(), ShouldBeNil)
			So(spent.Load(), ShouldBeTrue)
			So(segmentFiles(dir), ShouldResemble, []uint32{1, 2, 3})

			s := scanSegment(dir, 3)
			So(s.prevFile, ShouldEqual, 1)
			So(s.prevEnd, ShouldEqual, refA.Off+FrameHeader+1)
			So(fileSize(segment(dir, 1)), ShouldBeGreaterThan, s.prevEnd)
		})
	})
}

func TestWriterReusesAGoneNumber(t *testing.T) {
	Convey("Given a writer of file 1 whose open of file 2 fails with file 2 already gone when it removes it, "+
		"as when a retransmitted remove's success is lost", t, func() {
		dir := t.TempDir()

		var gone atomic.Bool

		setSyncHook(func(path string) error {
			if path == segment(dir, 2) && gone.CompareAndSwap(false, true) {
				return errors.Join(syscall.EIO, os.Remove(path))
			}

			return nil
		})

		w := newTestWriter(dir, testSegmentSize, nil)

		_, err := w.Rotate()
		So(err, ShouldWrap, syscall.EIO)
		So(gone.Load(), ShouldBeTrue)

		Convey("number 2 is not spent: file 2 is opened again, naming file 1, and the next record goes to "+
			"file 3, naming file 2, so no number is skipped", func() {
			ref, c := w.Append([]byte("a"), nil)
			So(waitFor(c), ShouldBeNil)
			So(ref, ShouldResemble, Ref{File: 3, Off: uint64(headerEnd)})
			So(w.Close(), ShouldBeNil)
			So(segmentFiles(dir), ShouldResemble, []uint32{1, 2, 3})

			reopened := scanSegment(dir, 2)
			So(reopened.prevFile, ShouldEqual, 1)
			So(reopened.recs, ShouldBeEmpty)

			s := scanSegment(dir, 3)
			So(s.prevFile, ShouldEqual, 2)
			So(s.recs, ShouldResemble, []string{"a"})
		})
	})
}

// results records the error each done func was called with, by record index.
type results struct {
	mu   sync.Mutex
	errs map[int][]error
}

// done returns the done func of record i.
func (r *results) done(i int) func(error) {
	return func(err error) {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.errs[i] = append(r.errs[i], err)
	}
}

// calls returns the errors record i's done func was called with.
func (r *results) calls(i int) []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.errs[i])
}

// failTimes records when a writer failed each batch. Its onError is the
// writer's, which the writer calls before it starts the backoff after that
// failure, so the time between two of them includes the whole backoff however
// late the appender wakes.
type failTimes struct {
	mu sync.Mutex
	at []time.Time
}

// onError records a failure now.
func (f *failTimes) onError(error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.at = append(f.at, time.Now())
}

// since returns the failure times recorded from the ith on.
func (f *failTimes) since(i int) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.at[i:])
}

// count returns how many failures were recorded.
func (f *failTimes) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.at)
}

func TestWriterIdleReopenSpendsANumber(t *testing.T) {
	Convey("Given a writer whose first batch fails, then whose idle open of file 2 fails leaving a file 2 behind, "+
		"while a record is queued for file 2", t, func() {
		dir := t.TempDir()
		entered, release := make(chan struct{}), make(chan struct{})

		var (
			failed      atomic.Bool
			releaseOnce sync.Once
		)

		Reset(func() { releaseOnce.Do(func() { close(release) }) })

		setSyncHook(func(path string) error {
			if failed.CompareAndSwap(false, true) {
				return syscall.EIO
			}

			if path != segment(dir, 2) {
				return nil
			}

			close(entered)
			<-release

			// leave something at file 2's path that cannot be removed
			if err := os.Remove(path); err != nil {
				return err
			}

			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}

			if err := os.WriteFile(filepath.Join(path, "stray"), nil, 0o600); err != nil {
				return err
			}

			return syscall.EIO
		})

		w := newTestWriter(dir, testSegmentSize, nil)

		_, c := w.Append([]byte("lost"), nil)
		So(waitFor(c), ShouldWrap, syscall.EIO)

		select {
		case <-entered:
		case <-time.After(testTimeout):
			So("the idle open of file 2 never synced", ShouldBeEmpty)
		}

		ref, queued := w.Append([]byte("queued"), nil)
		So(ref.File, ShouldEqual, 2)

		releaseOnce.Do(func() { close(release) })

		Convey("the queued record fails with the open's error", func() {
			err := waitFor(queued)
			So(err, ShouldWrap, syscall.EIO)
		})
	})
}

func TestWriterBackoff(t *testing.T) {
	Convey("Given a sync hook returning EIO for every sync, and walRetryCap 150ms", t, func() {
		Reset(SetTestRetryCap(150 * time.Millisecond))

		setSyncHook(func(string) error { return syscall.EIO })

		failed := &failTimes{}
		w := newTestWriter(t.TempDir(), testSegmentSize, failed.onError)

		Convey("each failed batch is followed by at least retryDelay before the next is taken", func() {
			gaps := failGaps(w, failed, 6)

			var late time.Duration

			for i, gap := range gaps {
				So(gap, ShouldBeGreaterThanOrEqualTo, retryDelay(i, 150*time.Millisecond))

				if i >= 3 {
					late += gap
				}
			}

			// 450ms with the 150ms cap; 1.4s if it were ignored for the
			// default 5s. The bound is loose so load cannot fail it.
			So(late, ShouldBeLessThan, time.Second)
		})
	})

	Convey("Given a sync hook failing every sync until cleared, and walRetryCap 1 minute", t, func() {
		Reset(SetTestRetryCap(time.Minute))

		var failing atomic.Bool

		failing.Store(true)
		setSyncHook(func(string) error {
			if failing.Load() {
				return syscall.EIO
			}

			return nil
		})

		failed := &failTimes{}
		w := newTestWriter(t.TempDir(), testSegmentSize, failed.onError)

		// After 6 failures the writer waits 1.6s before its next batch.
		failGaps(w, failed, 6)

		Convey("Close does not wait out the backoff", func() {
			start := time.Now()

			So(w.Close(), ShouldBeNil)
			So(time.Since(start), ShouldBeLessThan, 800*time.Millisecond)
		})

		Convey("a success resets the backoff", func() {
			failing.Store(false)

			_, c := w.Append([]byte("ok"), nil)
			So(waitFor(c), ShouldBeNil)

			failing.Store(true)

			// Reset, the failures after it wait 0 then 100ms; carried on from
			// 6 they would wait 3.2s then 6.4s.
			gaps := failGaps(w, failed, 3)
			So(gaps[1]+gaps[2], ShouldBeLessThan, 2*time.Second)
			So(gaps[2], ShouldBeGreaterThanOrEqualTo, retryDelay(2, time.Minute))
		})
	})
}

// failGaps appends n records that must each fail, returning the time from each
// failure (or the call, for the first) to the next, as failed, w's onError
// recorder, timed them.
func failGaps(w *Writer, failed *failTimes, n int) []time.Duration {
	first := failed.count()
	last := time.Now()

	for i := range n {
		_, c := w.Append(fmt.Appendf(nil, "r%d", i), nil)
		So(waitFor(c), ShouldWrap, syscall.EIO)
	}

	times := failed.since(first)
	So(times, ShouldHaveLength, n)

	gaps := make([]time.Duration, 0, n)

	for _, at := range times {
		gaps = append(gaps, at.Sub(last))
		last = at
	}

	return gaps
}

func TestWriterTestSeams(t *testing.T) {
	Convey("Given a sync hook set and then restored, a writer made after it syncs unhooked", t, func() {
		restore := SetTestSyncHook(func(string) error { return syscall.EIO })
		Reset(restore)
		restore()

		w := newTestWriter(t.TempDir(), testSegmentSize, nil)

		_, c := w.Append([]byte("a"), nil)
		So(waitFor(c), ShouldBeNil)
	})

	Convey("Given a retry cap set and then restored, a writer made after it backs off 100ms after its "+
		"second consecutive failure", t, func() {
		restore := SetTestRetryCap(time.Millisecond)
		Reset(restore)
		restore()

		setSyncHook(func(string) error { return syscall.EIO })

		failed := &failTimes{}
		w := newTestWriter(t.TempDir(), testSegmentSize, failed.onError)

		gaps := failGaps(w, failed, 3)
		So(gaps[2], ShouldBeGreaterThanOrEqualTo, walRetryBase)
	})
}

func TestWriterCloseRace(t *testing.T) {
	Convey("Appends and Rotates racing Close each resolve, durable and scanned or ErrClosed, with done "+
		"called once with the same result", t, func() {
		const (
			rounds     = 20
			goroutines = 8
			perG       = 40
		)

		bad, doneBad, lost := 0, 0, 0

		for round := range rounds {
			dir := t.TempDir()

			w, err := NewWriter(dir, 1, 2048, nil)
			So(err, ShouldBeNil)

			var (
				wg           sync.WaitGroup
				nBad, nDone  atomic.Int32
				durable      sync.Map
				durableCount atomic.Int32
			)

			for g := range goroutines {
				wg.Go(func() {
					for i := range perG {
						rec := fmt.Sprintf("r%d-g%d-%d", round, g, i)

						var (
							calls atomic.Int32
							got   atomic.Pointer[error]
						)

						_, c := w.Append([]byte(rec), func(err error) {
							calls.Add(1)
							got.Store(&err)
						})

						if i%7 == 0 {
							if _, rerr := w.Rotate(); rerr != nil && !errors.Is(rerr, ErrClosed) {
								nBad.Add(1)
							}
						}

						done, err := c.WaitWithin(testTimeout)
						if !done {
							nBad.Add(1)

							continue
						}

						if p := got.Load(); calls.Load() != 1 || p == nil || !errors.Is(*p, err) {
							nDone.Add(1)
						}

						switch {
						case err == nil:
							durable.Store(rec, true)
							durableCount.Add(1)
						case !errors.Is(err, ErrClosed):
							nBad.Add(1)
						}
					}
				})
			}

			wg.Go(func() { _ = w.Close() })
			wg.Wait()

			bad += int(nBad.Load())
			doneBad += int(nDone.Load())

			found := 0

			for _, file := range segmentFiles(dir) {
				data, rerr := os.ReadFile(segment(dir, file))
				So(rerr, ShouldBeNil)

				for _, rec := range scanAll(data).recs {
					if _, ok := durable.Load(rec); ok {
						found++
					}
				}
			}

			lost += int(durableCount.Load()) - found
		}

		So(bad, ShouldEqual, 0)
		So(doneBad, ShouldEqual, 0)
		So(lost, ShouldEqual, 0)
	})
}

func TestWriterTakeMaxSync(t *testing.T) {
	Convey("Given a writer whose first batch sync takes at least 300ms and later ones are quick", t, func() {
		const slow = 300 * time.Millisecond

		var calls atomic.Int32

		setSyncHook(func(string) error {
			if calls.Add(1) == 1 {
				time.Sleep(slow)
			}

			return nil
		})

		w := newTestWriter(t.TempDir(), testSegmentSize, nil)

		Convey("TakeMaxSync gives the longest sync since the last take, then starts afresh", func() {
			_, c := w.Append([]byte("slow"), nil)
			So(waitFor(c), ShouldBeNil)

			_, c = w.Append([]byte("quick"), nil)
			So(waitFor(c), ShouldBeNil)

			first := w.TakeMaxSync()
			So(first, ShouldBeGreaterThanOrEqualTo, slow)
			So(first, ShouldBeLessThanOrEqualTo, w.Stats().SyncTime)
			So(w.TakeMaxSync(), ShouldEqual, 0)

			_, c = w.Append([]byte("later"), nil)
			So(waitFor(c), ShouldBeNil)

			later := w.TakeMaxSync()
			So(later, ShouldBeGreaterThan, 0)
			So(later, ShouldBeLessThan, slow)
		})
	})
}
