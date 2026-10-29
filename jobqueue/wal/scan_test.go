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
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"testing/iotest"

	"github.com/klauspost/compress/s2"
	. "github.com/smartystreets/goconvey/convey"
)

// errStop is the error TestScan's fn stops a scan with.
var errStop = errors.New("stop")

// threeRecs are the records of several tests' logs.
var threeRecs = []string{"a", "bb", "ccc"} //nolint:gochecknoglobals // read-only test fixture

// errBoom is the error of a reader that fails.
var errBoom = errors.New("boom")

// scanned is what scanAll saw.
type scanned struct {
	refs []Ref
	recs []string
	end  int64
	err  error
}

// scanAll scans log, keeping a copy of each record.
func scanAll(log []byte) scanned {
	return scanReader(bytes.NewReader(log))
}

// scanReader scans r, keeping a copy of each record.
func scanReader(r io.Reader) scanned {
	var s scanned

	s.end, s.err = Scan(r, func(ref Ref, rec []byte) error {
		s.refs = append(s.refs, ref)
		s.recs = append(s.recs, string(rec))

		return nil
	})

	return s
}

func TestScan(t *testing.T) {
	Convey("Given frames for records a, bb and ccc", t, func() {
		log := appendFrames(threeRecs...)

		Convey("a scan sees all 3 in order at their offsets, and ends after the last", func() {
			s := scanAll(log)
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, threeRecs)
			So(s.refs, ShouldResemble, []Ref{{0, 0, 0}, {0, 9, 0}, {0, 19, 0}})
			So(s.end, ShouldEqual, 30)
		})

		Convey("a scan of the log cut at every offset sees the whole frames before the cut", func() {
			wholeBefore := func(cut int) int {
				switch {
				case cut >= 30:
					return 3
				case cut >= 19:
					return 2
				case cut >= 9:
					return 1
				default:
					return 0
				}
			}

			wrong := 0

			for cut := range len(log) {
				s := scanAll(log[:cut])
				if s.err != nil || len(s.recs) != wholeBefore(cut) {
					wrong++
				}
			}

			So(wrong, ShouldEqual, 0)
		})

		Convey("a flipped payload byte ends the scan before that frame, with no error", func() {
			log[FrameHeader+9+1] ^= 0xff

			s := scanAll(log)
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, []string{"a"})
			So(s.end, ShouldEqual, 9)
		})

		Convey("an error from fn stops the scan and is returned, with end before that frame", func() {
			seen := 0

			end, err := Scan(bytes.NewReader(log), func(Ref, []byte) error {
				seen++
				if seen == 2 {
					return errStop
				}

				return nil
			})
			So(err, ShouldEqual, errStop)
			So(seen, ShouldEqual, 2)
			So(end, ShouldEqual, 9)
		})
	})

	Convey("Given a frame whose length has bit 31 clear and value 2^30 + 1, with that whole payload and its "+
		"CRC, a scan stops there with no error, allocating far less than the payload", t, func() {
		const n = 1<<30 + 1

		zeros := io.LimitReader(zeroReader{}, n)

		crc := crc32.New(castagnoli)
		_, err := io.Copy(crc, zeros)
		So(err, ShouldBeNil)

		head := appendFrames("a")
		head = binary.LittleEndian.AppendUint32(head, n)
		head = binary.LittleEndian.AppendUint32(head, crc.Sum32())

		var s scanned

		alloc := allocatedBy(func() {
			s = scanReader(io.MultiReader(bytes.NewReader(head), io.LimitReader(zeroReader{}, n)))
		})

		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"a"})
		So(s.end, ShouldEqual, 9)
		So(alloc, ShouldBeLessThan, 8<<20)
	})

	Convey("Given a reader that fails after a good frame, a scan returns its error with end after the frame", t,
		func() {
			s := scanReader(io.MultiReader(bytes.NewReader(appendFrames("a")), iotest.ErrReader(errBoom)))
			So(s.err, ShouldWrap, errBoom)
			So(s.recs, ShouldResemble, []string{"a"})
			So(s.end, ShouldEqual, 9)
		})

	Convey("Given the bytes of frames of a and bb as written by this format's first version, a scan reads them "+
		"and AppendFrame writes them", t, func() {
		log := mustHex("010000003043d0c16102000000af8145d66262")

		So(scanAll(log).recs, ShouldResemble, []string{"a", "bb"})
		So(appendFrames("a", "bb"), ShouldResemble, log)
	})
}

// appendFrames frames each record onto one log.
func appendFrames(recs ...string) []byte {
	var log []byte

	for _, rec := range recs {
		log = AppendFrame(log, []byte(rec))
	}

	return log
}

// allocatedBy returns how many bytes f allocated.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	f()

	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

// mustHex decodes a hex string a test holds.
func mustHex(h string) []byte {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}

	return b
}

func TestScanEmptyFrames(t *testing.T) {
	Convey("Given 8 zero bytes, a scan sees no records and ends at 0", t, func() {
		s := scanAll(make([]byte, FrameHeader))
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldBeEmpty)
		So(s.end, ShouldEqual, 0)
	})

	Convey("Given good frames then a zero-filled tail, a scan ends at the last good frame", t, func() {
		log := appendFrames("a", "bb")
		good := len(log)
		log = append(log, make([]byte, 4096)...)

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"a", "bb"})
		So(s.end, ShouldEqual, good)
	})

	Convey("Given a block holding an empty frame among good ones, a scan gives none of its records", t, func() {
		frames := appendFrames("a")
		frames = append(frames, make([]byte, FrameHeader)...)
		frames = AppendFrame(frames, []byte("bb"))

		log := appendFrames("x")

		log, err := AppendBlock(log, CodecS2, frames)
		So(err, ShouldBeNil)

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"x"})
		So(s.end, ShouldEqual, 9)
	})

	Convey("Given a block that decompresses to nothing, a scan stops at the block", t, func() {
		log := appendRawBlock(appendFrames("x"), CodecS2, s2.Encode(nil, nil))

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"x"})
		So(s.end, ShouldEqual, 9)
	})

	Convey("AppendFrame refuses a record over 1GiB, which could not be scanned back", t, func() {
		rec := overLimit(t)

		So(func() { AppendFrame(nil, rec) }, ShouldPanic)
	})

	Convey("AppendFrame refuses an empty record, which would scan as damage", t, func() {
		So(func() { AppendFrame(nil, nil) }, ShouldPanic)
	})

	Convey("AppendBlock refuses empty frames, which would scan as damage", t, func() {
		_, err := AppendBlock(nil, CodecS2, nil)
		So(err, ShouldWrap, ErrEmptyBlock)
	})
}

// appendRawBlock appends a block frame holding the codec byte then compressed,
// as-is, with a correct CRC, so that only its codec or contents are damaged.
func appendRawBlock(dst []byte, codec byte, compressed []byte) []byte {
	payload := append([]byte{codec}, compressed...)
	lenFlags := blockFlag | uint32(len(payload)) //nolint:gosec // small

	dst = binary.LittleEndian.AppendUint32(dst, lenFlags)
	dst = binary.LittleEndian.AppendUint32(dst, blockCRC(lenFlags, payload))

	return append(dst, payload...)
}

// overLimit returns MaxFrameLen+1 bytes that are never touched, mapped for
// the rest of the test, so a refusal of them costs no memory.
func overLimit(t *testing.T) []byte {
	t.Helper()

	b, err := syscall.Mmap(-1, 0, MaxFrameLen+1, syscall.PROT_READ, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatalf("mmap: %v", err)
	}

	t.Cleanup(func() {
		if err := syscall.Munmap(b); err != nil {
			t.Errorf("munmap: %v", err)
		}
	})

	return b
}

func TestScanBlocks(t *testing.T) {
	Convey("Given an s2 block of 64 records followed by a plain record", t, func() {
		recs := numberedRecords(65)

		log, err := AppendBlock(nil, CodecS2, appendFrames(recs[:64]...))
		So(err, ShouldBeNil)

		blockEnd := int64(len(log))
		log = AppendFrame(log, []byte(recs[64]))

		Convey("a whole scan sees all 65 in order, the block's records at the block's offset", func() {
			s := scanAll(log)
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, recs)
			So(s.refs[0], ShouldResemble, Ref{0, 0, 0})
			So(s.refs[63], ShouldResemble, Ref{0, 0, 63})
			So(s.refs[64], ShouldResemble, Ref{0, uint64(blockEnd), 0}) //nolint:gosec // small
			So(s.end, ShouldEqual, len(log))
		})

		Convey("a scan of the log cut at every offset sees 0, 64 or 65 records, never a partial block, "+
			"and ends after the last whole frame", func() {
			wrong := 0

			for cut := range len(log) + 1 {
				s := scanAll(log[:cut])

				wantN, wantEnd := 0, int64(0)

				switch {
				case cut == len(log):
					wantN, wantEnd = 65, int64(len(log))
				case int64(cut) >= blockEnd:
					wantN, wantEnd = 64, blockEnd
				}

				if s.err != nil || s.end != wantEnd || !slices.Equal(s.recs, recs[:wantN]) {
					wrong++
				}
			}

			So(wrong, ShouldEqual, 0)
		})

		Convey("a block whose bit 31 is cleared is not read as a plain record", func() {
			log[3] &^= 0x80

			s := scanAll(log)
			So(s.err, ShouldBeNil)
			So(s.recs, ShouldBeEmpty)
			So(s.end, ShouldEqual, 0)
		})

		Convey("an error from fn inside the block stops the scan with end at the block's start", func() {
			seen := 0

			end, err := Scan(bytes.NewReader(log), func(Ref, []byte) error {
				seen++
				if seen == 10 {
					return errStop
				}

				return nil
			})
			So(err, ShouldEqual, errStop)
			So(end, ShouldEqual, 0)
		})
	})

	Convey("Given a plain frame whose bit 31 is set, a scan does not read it as a block", t, func() {
		inner := appendFrames("a")
		payload := append([]byte{CodecS2}, s2.Encode(nil, inner)...)
		log := AppendFrame(nil, payload)
		log[3] |= 0x80

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldBeEmpty)
		So(s.end, ShouldEqual, 0)
	})

	Convey("Given a zstd block of 3 records, a scan sees all 3", t, func() {
		log, err := AppendBlock(nil, CodecZstd, appendFrames(threeRecs...))
		So(err, ShouldBeNil)

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, threeRecs)
		So(s.refs, ShouldResemble, []Ref{{0, 0, 0}, {0, 0, 1}, {0, 0, 2}})
		So(s.end, ShouldEqual, len(log))
	})

	Convey("Given a block whose frames end in a torn frame, a scan gives none of its records", t, func() {
		frames := appendFrames(threeRecs...)

		log, err := AppendBlock(nil, CodecS2, frames[:len(frames)-1])
		So(err, ShouldBeNil)

		seen := 0
		end, err := Scan(bytes.NewReader(log), func(Ref, []byte) error {
			seen++

			return nil
		})
		So(err, ShouldBeNil)
		So(seen, ShouldEqual, 0)
		So(end, ShouldEqual, 0)
	})

	Convey("Given a block whose frames hold a CRC-damaged frame, a scan gives none of its records", t, func() {
		frames := appendFrames(threeRecs...)
		frames[len(frames)-1] ^= 0xff

		log, err := AppendBlock(nil, CodecS2, frames)
		So(err, ShouldBeNil)

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldBeEmpty)
		So(s.end, ShouldEqual, 0)
	})

	Convey("Given a block nested inside a block, a scan gives none of the outer block's records", t, func() {
		nested, err := AppendBlock(appendFrames("a"), CodecS2, appendFrames("b"))
		So(err, ShouldBeNil)

		log, err := AppendBlock(nil, CodecS2, nested)
		So(err, ShouldBeNil)

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldBeEmpty)
		So(s.end, ShouldEqual, 0)
	})

	Convey("Given s2 blocks of a frame then 1 to 7 bytes too few for a frame header, a scan gives none of "+
		"their records", t, func() {
		wrong := 0

		for extra := 1; extra < FrameHeader; extra++ {
			frames := append(appendFrames("a"), make([]byte, extra)...)

			s := scanAll(appendRawBlock(nil, CodecS2, s2.Encode(nil, frames)))
			if s.err != nil || len(s.recs) != 0 || s.end != 0 {
				wrong++
			}
		}

		So(wrong, ShouldEqual, 0)
	})

	Convey("Given blocks that claim to decompress to 2^30 + 1 bytes, a scan stops at each with no error, "+
		"allocating far less than that", t, func() {
		s2Bomb := binary.AppendUvarint(nil, 1<<30+1)
		s2Bomb = append(s2Bomb, 0, 0, 0, 0)

		// A zstd frame header with an 8-byte content size and a window
		// descriptor (so not single-segment), then an empty raw last block.
		zstdBomb := []byte{0x28, 0xb5, 0x2f, 0xfd, 0xc0, 0x00}
		zstdBomb = binary.LittleEndian.AppendUint64(zstdBomb, 1<<30+1)
		zstdBomb = append(zstdBomb, 0x01, 0x00, 0x00)

		for _, bomb := range []struct {
			codec      byte
			compressed []byte
		}{{CodecS2, s2Bomb}, {CodecZstd, zstdBomb}} {
			log := appendRawBlock(appendFrames("x"), bomb.codec, bomb.compressed)

			var s scanned

			alloc := allocatedBy(func() { s = scanAll(log) })

			So(s.err, ShouldBeNil)
			So(s.recs, ShouldResemble, []string{"x"})
			So(s.end, ShouldEqual, 9)
			So(alloc, ShouldBeLessThan, 64<<20)
		}
	})

	Convey("Given a block whose compressed bytes do not decode, a scan stops at the block with no error", t, func() {
		log := appendFrames("a")
		log = appendRawBlock(log, CodecZstd, []byte("not zstd"))

		s := scanAll(log)
		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"a"})
		So(s.end, ShouldEqual, 9)
	})
}

// numberedRecords returns n distinct records.
func numberedRecords(n int) []string {
	recs := make([]string, n)
	for i := range n {
		recs[i] = fmt.Sprintf("record %d of a block", i)
	}

	return recs
}

func TestAppendBlock(t *testing.T) {
	Convey("AppendBlock refuses an unknown codec, returning dst as it was", t, func() {
		in := appendFrames("a")

		out, err := AppendBlock(slices.Clone(in), 9, appendFrames("b"))
		So(err, ShouldWrap, ErrUnknownCodec)
		So(out, ShouldResemble, in)
	})

	Convey("AppendBlock refuses frames over the frame limit, which could not be scanned back", t, func() {
		in := appendFrames("a")

		out, err := AppendBlock(slices.Clone(in), CodecS2, overLimit(t))
		So(err, ShouldWrap, ErrBlockTooLarge)
		So(out, ShouldResemble, in)
	})

	Convey("Given the bytes of blocks of a, bb and ccc as written by this format's first version", t, func() {
		s2Block := mustHex("21000080b1737407011e74010000003043d0c16102000000af8145d6626203000000cdf5866a636363")
		zstdBlock := mustHex("280000802fd9b7270228b52ffd0000f10000010000003043d0c16102000000af8145d6626203000000" +
			"cdf5866a636363")

		Convey("a scan reads both", func() {
			So(scanAll(s2Block).recs, ShouldResemble, threeRecs)
			So(scanAll(zstdBlock).recs, ShouldResemble, threeRecs)
		})

		Convey("AppendBlock writes the s2 one", func() {
			out, err := AppendBlock(nil, CodecS2, appendFrames(threeRecs...))
			So(err, ShouldBeNil)
			So(out, ShouldResemble, s2Block)
		})
	})

	Convey("AppendBlock appends to dst, leaving what was there", t, func() {
		log, err := AppendBlock(appendFrames("a"), CodecZstd, appendFrames("b"))
		So(err, ShouldBeNil)
		So(scanAll(log).recs, ShouldResemble, []string{"a", "b"})
	})
}

func TestScanDamagedLength(t *testing.T) {
	Convey("Given a frame claiming a 1GiB payload followed by only 64 bytes, a scan stops there with no "+
		"error, allocating far less than the claimed length", t, func() {
		log := appendFrames("a")
		log = binary.LittleEndian.AppendUint32(log, MaxFrameLen)
		log = binary.LittleEndian.AppendUint32(log, 0)
		log = append(log, make([]byte, 64)...)

		var s scanned

		alloc := allocatedBy(func() { s = scanAll(log) })

		So(s.err, ShouldBeNil)
		So(s.recs, ShouldResemble, []string{"a"})
		So(s.end, ShouldEqual, 9)
		So(alloc, ShouldBeLessThan, 8<<20)
	})
}

func TestScanBigRecords(t *testing.T) {
	Convey("Given a 3MiB record as a plain frame and inside an s2 block, both over the payload read chunk", t,
		func() {
			big := bigRecord(3 << 20)
			log := appendFrames("a")
			bigOff := uint64(len(log))
			log = AppendFrame(log, big)
			blockOff := uint64(len(log))

			log, err := AppendBlock(log, CodecS2, AppendFrame(appendFrames("b"), big))
			So(err, ShouldBeNil)

			Convey("a scan returns both byte-equal and ends after the block", func() {
				s := scanAll(log)
				So(s.err, ShouldBeNil)
				So(s.recs, ShouldHaveLength, 4)
				So(s.recs[1] == string(big), ShouldBeTrue)
				So(s.recs[3] == string(big), ShouldBeTrue)
				So(s.refs[1], ShouldResemble, Ref{Off: bigOff})
				So(s.refs[3], ShouldResemble, Ref{Off: blockOff, Idx: 1})
				So(s.end, ShouldEqual, len(log))
			})

			Convey("ReadAt returns both byte-equal", func() {
				for _, ref := range []Ref{{Off: bigOff}, {Off: blockOff, Idx: 1}} {
					rec, err := ReadAt(bytes.NewReader(log), ref)
					So(err, ShouldBeNil)
					So(bytes.Equal(rec, big), ShouldBeTrue)
				}
			})
		})
}

// bigRecord returns a record of n bytes that do not repeat in a short cycle,
// so that it is read in several chunks and s2 cannot shrink it much.
func bigRecord(n int) []byte {
	rec := make([]byte, n)
	x := uint32(2463534242)

	for i := range rec {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		rec[i] = byte(x) //nolint:gosec // keeping the low byte is the point
	}

	return rec
}

// zeroReader reads endless zero bytes.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)

	return len(p), nil
}

// failingReaderAt is an io.ReaderAt whose reads fail with errBoom.
type failingReaderAt struct{}

func (failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, errBoom }

func TestReadAt(t *testing.T) {
	Convey("Given a log of a plain frame, an s2 block, a zstd block and a plain frame", t, func() {
		log := appendFrames("a")
		s2Off := uint64(len(log))

		log, err := AppendBlock(log, CodecS2, appendFrames("b", "cc"))
		So(err, ShouldBeNil)

		zstdOff := uint64(len(log))

		log, err = AppendBlock(log, CodecZstd, appendFrames("d", "ee", "fff"))
		So(err, ShouldBeNil)

		lastOff := uint64(len(log))
		log = AppendFrame(log, []byte("g"))
		f := bytes.NewReader(log)

		Convey("ReadAt reads each record by its Ref, whatever its File", func() {
			for ref, want := range map[Ref]string{
				{Off: 0}:                        "a",
				{Off: s2Off}:                    "b",
				{Off: s2Off, Idx: 1}:            "cc",
				{Off: zstdOff}:                  "d",
				{Off: zstdOff, Idx: 2}:          "fff",
				{File: 7, Off: lastOff}:         "g",
				{File: 7, Off: zstdOff, Idx: 1}: "ee",
			} {
				rec, err := ReadAt(f, ref)
				So(err, ShouldBeNil)
				So(string(rec), ShouldEqual, want)
			}
		})

		Convey("ReadAt refuses a Ref that names no record", func() {
			for _, ref := range []Ref{
				{Off: 0, Idx: 1},
				{Off: s2Off, Idx: 2},
				{Off: zstdOff, Idx: 3},
				{Off: 1},
				{Off: lastOff + 1},
				{Off: uint64(len(log))},
				{Off: 1 << 63},
			} {
				_, err := ReadAt(f, ref)
				So(err, ShouldWrap, ErrBadRef)
			}
		})

		Convey("ReadAt refuses a damaged plain frame or block", func() {
			log[len(log)-1] ^= 0xff
			log[zstdOff+FrameHeader+2] ^= 0xff

			_, err := ReadAt(f, Ref{Off: lastOff})
			So(err, ShouldWrap, ErrBadRef)

			_, err = ReadAt(f, Ref{Off: zstdOff})
			So(err, ShouldWrap, ErrBadRef)

			rec, err := ReadAt(f, Ref{Off: s2Off, Idx: 1})
			So(err, ShouldBeNil)
			So(string(rec), ShouldEqual, "cc")
		})

		Convey("ReadAt refuses a block whose CRC is good but whose contents are not", func() {
			bad := appendRawBlock(nil, CodecS2, s2.Encode(nil, appendFrames("a")[:FrameHeader]))

			_, err := ReadAt(bytes.NewReader(bad), Ref{})
			So(err, ShouldWrap, ErrBadRef)

			bad = appendRawBlock(nil, 9, s2.Encode(nil, appendFrames("a")))

			_, err = ReadAt(bytes.NewReader(bad), Ref{})
			So(err, ShouldWrap, ErrBadRef)
		})

		Convey("ReadAt of a frame claiming a 1GiB payload followed by only 64 bytes refuses it, allocating far "+
			"less than the claimed length", func() {
			damaged := binary.LittleEndian.AppendUint32(nil, MaxFrameLen)
			damaged = binary.LittleEndian.AppendUint32(damaged, 0)
			damaged = append(damaged, make([]byte, 64)...)

			var err error

			alloc := allocatedBy(func() { _, err = ReadAt(bytes.NewReader(damaged), Ref{}) })

			So(err, ShouldWrap, ErrBadRef)
			So(alloc, ShouldBeLessThan, 8<<20)
		})

		Convey("ReadAt returns the error of a failing reader", func() {
			_, err := ReadAt(failingReaderAt{}, Ref{Off: s2Off})
			So(err, ShouldWrap, errBoom)
		})
	})
}
