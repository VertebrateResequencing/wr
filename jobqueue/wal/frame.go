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

// Package wal is the customdb store's log format: framed, CRC-checked records
// that a scan reads back up to the first damage.
//
// A frame is [u32 lenFlags][u32 crc32c(payload)][payload], little endian, with
// the CRC Castagnoli. Bit 31 of lenFlags marks a block; the low 31 bits are the
// payload's length, at most 1GiB. A block's payload is [u8 codec][compressed
// bytes], whose decompressed bytes are a sequence of plain frames, and its CRC
// covers lenFlags then the payload. Records are never empty: a frame of length
// 0, plain or inside a block, is damage, so a zero-filled tail ends a log.
package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const (
	// FrameHeader is the size of a frame's header: its lenFlags and its CRC.
	FrameHeader = 8

	// MaxFrameLen is the longest payload a frame may have: a longer record
	// cannot be framed.
	MaxFrameLen = 1 << 30

	// blockFlag is the bit of lenFlags that marks a block.
	blockFlag = 1 << 31
)

// castagnoli is the CRC table frames are checked with.
var castagnoli = crc32.MakeTable(crc32.Castagnoli) //nolint:gochecknoglobals // a constant table

// Ref addresses one record: the file number, the offset of its outer frame, and
// its index inside that frame (0 for a plain frame). Lazy specs (design D3) read
// a spec through a Ref without a format change.
type Ref struct {
	File uint32
	Off  uint64
	Idx  uint32
}

// AppendBlock appends frames, a sequence of plain frames made with AppendFrame,
// to dst as one block compressed with codec (CodecS2 or CodecZstd), returning
// the extended buffer. A scan gives a block's records all or none.
//
// It returns ErrEmptyBlock for no frames, ErrUnknownCodec for another codec,
// and ErrBlockTooLarge if frames or the block's payload is over MaxFrameLen, in
// each case with dst unchanged. It does not check frames: a block of damaged
// frames scans as damage.
func AppendBlock(dst []byte, codec byte, frames []byte) ([]byte, error) {
	if len(frames) == 0 {
		return dst, ErrEmptyBlock
	}

	if len(frames) > MaxFrameLen {
		return dst, fmt.Errorf("%w: %d bytes of frames", ErrBlockTooLarge, len(frames))
	}

	start := len(dst)
	dst = append(dst, make([]byte, FrameHeader)...)
	dst = append(dst, codec)

	dst, err := compress(dst, codec, frames)
	if err != nil {
		return dst[:start], err
	}

	payload := dst[start+FrameHeader:]
	if len(payload) > MaxFrameLen {
		return dst[:start], fmt.Errorf("%w: %d byte payload", ErrBlockTooLarge, len(payload))
	}

	lenFlags := blockFlag | uint32(len(payload)) //nolint:gosec // at most MaxFrameLen
	binary.LittleEndian.PutUint32(dst[start:], lenFlags)
	binary.LittleEndian.PutUint32(dst[start+4:], blockCRC(lenFlags, payload))

	return dst, nil
}

// blockCRC is the CRC of a block with the given lenFlags and payload. Unlike a
// plain frame's, it covers lenFlags as well as the payload: were it the
// payload's alone, a block whose bit 31 is lost would pass as a plain frame of
// its compressed bytes, and a plain frame whose bit 31 is set could pass as a
// block.
func blockCRC(lenFlags uint32, payload []byte) uint32 {
	var lf [4]byte

	binary.LittleEndian.PutUint32(lf[:], lenFlags)

	return crc32.Update(crc32.Checksum(lf[:], castagnoli), castagnoli, payload)
}

// AppendFrame appends rec to dst as a plain frame, returning the extended
// buffer. rec must be 1 byte to 1GiB: an empty or longer one could never be
// scanned back, so AppendFrame panics rather than write it.
func AppendFrame(dst, rec []byte) []byte {
	checkRecord(rec)

	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(rec))) //nolint:gosec // at most MaxFrameLen
	dst = binary.LittleEndian.AppendUint32(dst, crc32.Checksum(rec, castagnoli))

	return append(dst, rec...)
}

// checkRecord panics unless rec is 1 byte to 1GiB, the records a frame can
// hold.
func checkRecord(rec []byte) {
	if len(rec) == 0 {
		panic("wal: an empty record cannot be framed")
	}

	if len(rec) > MaxFrameLen {
		panic(fmt.Sprintf("wal: record of %d bytes is over the %d byte frame limit", len(rec), MaxFrameLen))
	}
}
