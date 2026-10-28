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

import (
	"bytes"
	"os"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestBackupCopyWriterChunking checks that backupCopyWriter.Write honours its
// syncEvery bound even when a single incoming write is larger than syncEvery: it
// must pace once per syncEvery-sized interval, not once for the whole burst. Before
// the chunking fix a single large write jumped sinceSync past syncEvery and zeroed
// it, so the copy could accumulate an unbounded dirty-page backlog behind one late
// pace, weakening the bound on a concurrent foreground fdatasync's latency.
func TestBackupCopyWriterChunking(t *testing.T) {
	const (
		syncEvery = 4
		intervals = 3
	)

	Convey("Given a backupCopyWriter with a tiny syncEvery and a pace counter", t, func() {
		origHook := backupPaceHook
		defer func() { backupPaceHook = origHook }()

		var paceCount int

		backupPaceHook = func() { paceCount++ }

		f, err := os.CreateTemp(t.TempDir(), "backupchunk")
		So(err, ShouldBeNil)

		defer func() { So(f.Close(), ShouldBeNil) }()

		w := &backupCopyWriter{f: f, syncEvery: syncEvery}

		Convey("A single Write spanning several intervals paces once per interval", func() {
			p := bytes.Repeat([]byte("z"), syncEvery*intervals)

			n, errw := w.Write(p)
			So(errw, ShouldBeNil)
			So(n, ShouldEqual, len(p))

			// one pace per completed interval, not a single pace for the whole burst
			// (the unchunked writer gave 1 pace here regardless of how far the write
			// overshot syncEvery).
			So(paceCount, ShouldEqual, intervals)

			// the copy's bytes are exactly what we asked to write.
			got, errr := os.ReadFile(f.Name())
			So(errr, ShouldBeNil)
			So(got, ShouldResemble, p)
		})
	})
}
