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
	"fmt"
	"strings"
	"testing"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
	. "github.com/smartystreets/goconvey/convey"
)

// commandLen is the length of the command in repeatedCommand's records.
const commandLen = 1024

func TestBlockCodecs(t *testing.T) {
	recs := repeatedCommand(64)
	frames := appendFrames(recs...)

	Convey("Given 64 frames of a repeated 1KiB command, when blocked with each codec and scanned, all 64 "+
		"records come back byte-equal and the block is smaller than 1/4 of the input", t, func() {
		for _, codec := range []byte{CodecS2, CodecZstd} {
			block, err := AppendBlock(nil, codec, frames)
			So(err, ShouldBeNil)

			s := scanAll(block)
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, recs)
			So(s.end, ShouldEqual, len(block))
			So(len(block), ShouldBeLessThan, len(frames)/4)
		}
	})

	Convey("Given blocks whose codec byte is 9 but whose bytes are good s2 or zstd, a scan stops at the block "+
		"with no error", t, func() {
		for _, compressed := range [][]byte{
			s2.Encode(nil, appendFrames("b")),
			referenceZstd(t, zstd.SpeedFastest).EncodeAll(appendFrames("b"), nil),
		} {
			s := scanAll(appendRawBlock(appendFrames("a"), 9, compressed))
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, []string{"a"})
			So(s.end, ShouldEqual, 9)
		}
	})

	Convey("Codec 1 is s2: an s2 block's payload is byte 1 then the frames as s2 decodes them", t, func() {
		block, err := AppendBlock(nil, CodecS2, frames)
		So(err, ShouldBeNil)
		So(block[FrameHeader], ShouldEqual, 1)

		plain, err := s2.Decode(nil, block[FrameHeader+1:])
		So(err, ShouldBeNil)
		So(plain, ShouldResemble, frames)
	})

	Convey("Codec 2 is zstd at SpeedFastest with no dictionary", t, func() {
		block, err := AppendBlock(nil, CodecZstd, frames)
		So(err, ShouldBeNil)
		So(block[FrameHeader], ShouldEqual, 2)

		compressed := block[FrameHeader+1:]

		var h zstd.Header

		So(h.Decode(compressed), ShouldBeNil)
		So(h.DictionaryID, ShouldEqual, 0)

		fastest := referenceZstd(t, zstd.SpeedFastest).EncodeAll(frames, nil)
		So(compressed, ShouldResemble, fastest)

		Convey("which this input tells apart from every other level and from a dictionary", func() {
			for _, level := range []zstd.EncoderLevel{zstd.SpeedDefault, zstd.SpeedBetterCompression,
				zstd.SpeedBestCompression} {
				So(referenceZstd(t, level).EncodeAll(frames, nil), ShouldNotResemble, fastest)
			}

			So(referenceZstd(t, zstd.SpeedFastest, zstd.WithEncoderDictRaw(0, []byte(recs[0]))).
				EncodeAll(frames, nil), ShouldNotResemble, fastest)
		})
	})
}

// repeatedCommand returns n records of the same 1KiB command: one of distinct
// arguments, so that only its repetition, not its own bytes, compresses well.
func repeatedCommand(n int) []string {
	var cmd strings.Builder

	for i := 0; cmd.Len() < commandLen; i++ {
		fmt.Fprintf(&cmd, "bwa mem -t 8 ref.fa s%d_R1.fq s%d_R2.fq | samtools sort -o s%d.bam; ", i, i, i)
	}

	rec := cmd.String()[:commandLen]
	recs := make([]string, n)

	for i := range recs {
		recs[i] = rec
	}

	return recs
}

// referenceZstd returns a zstd encoder at level, with the frame options a
// block's zstd uses (no checksum, as the block's CRC covers its payload; a
// frame even for empty input) and any extra opts.
func referenceZstd(t *testing.T, level zstd.EncoderLevel, opts ...zstd.EOption) *zstd.Encoder {
	t.Helper()

	opts = append([]zstd.EOption{zstd.WithEncoderLevel(level), zstd.WithEncoderCRC(false),
		zstd.WithZeroFrames(true)}, opts...)

	enc, err := zstd.NewWriter(nil, opts...)
	So(err, ShouldBeNil)

	return enc
}
