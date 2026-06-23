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
	"math/rand"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestLiveTailSaver(t *testing.T) {
	Convey("A live tail saver flushes a compressed recent tail", t, func() {
		saver := &liveTailSaver{}

		n, err := saver.Write([]byte("one\n"))
		So(err, ShouldBeNil)
		So(n, ShouldEqual, len("one\n"))

		compressed := saver.FlushCompressed()
		So(compressed, ShouldNotBeNil)
		So(len(compressed), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)
		So(decompressLiveTail(compressed), ShouldResemble, []byte("one\n"))
	})

	Convey("A live tail saver returns nil when flushed twice without more writes", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("one\n"))
		So(err, ShouldBeNil)
		So(saver.FlushCompressed(), ShouldNotBeNil)
		So(saver.FlushCompressed(), ShouldBeNil)
	})

	Convey("A live tail saver bounds incompressible output to a compressed suffix", t, func() {
		written := deterministicLiveBytes(liveStdRawTailLimit)
		saver := &liveTailSaver{}

		n, err := saver.Write(written)
		So(err, ShouldBeNil)
		So(n, ShouldEqual, len(written))

		compressed := saver.FlushCompressed()
		So(compressed, ShouldNotBeNil)
		So(len(compressed), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)

		decompressed := decompressLiveTail(compressed)
		So(decompressed, ShouldNotBeEmpty)
		So(bytes.HasSuffix(written, decompressed), ShouldBeTrue)
	})

	Convey("A live tail saver keeps the newest marker and drops old output", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("UNIQUE-PREFIX\n"))
		So(err, ShouldBeNil)
		_, err = saver.Write(deterministicLiveBytes(2 * liveStdRawTailLimit))
		So(err, ShouldBeNil)
		_, err = saver.Write([]byte("UNIQUE-SUFFIX\n"))
		So(err, ShouldBeNil)

		decompressed := decompressLiveTail(saver.FlushCompressed())
		So(string(decompressed), ShouldContainSubstring, "UNIQUE-SUFFIX\n")
		So(string(decompressed), ShouldNotContainSubstring, "UNIQUE-PREFIX\n")
	})

	Convey("A live tail saver resets after each flush", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("old\n"))
		So(err, ShouldBeNil)
		So(saver.FlushCompressed(), ShouldNotBeNil)

		_, err = saver.Write([]byte("new\n"))
		So(err, ShouldBeNil)
		So(decompressLiveTail(saver.FlushCompressed()), ShouldResemble, []byte("new\n"))
	})
}

func decompressLiveTail(compressed []byte) []byte {
	decompressed, err := decompress(compressed)
	So(err, ShouldBeNil)

	return decompressed
}

//nolint:gosec // deterministic test data must be reproducible.
func deterministicLiveBytes(size int) []byte {
	r := rand.New(rand.NewSource(1))

	data := make([]byte, size)
	for i := range data {
		data[i] = byte(r.Intn(256))
	}

	return data
}
