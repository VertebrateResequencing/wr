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
// the CRC Castagnoli. Bit 31 of lenFlags marks a block (a compressed run of
// frames); the low 31 bits are the payload's length, at most 1GiB.
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

// AppendFrame appends rec to dst as a plain frame, returning the extended
// buffer. rec must be at most 1GiB: a longer one could never be scanned back, so
// AppendFrame panics rather than write it.
func AppendFrame(dst, rec []byte) []byte {
	if len(rec) > MaxFrameLen {
		panic(fmt.Sprintf("wal: record of %d bytes is over the %d byte frame limit", len(rec), MaxFrameLen))
	}

	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(rec))) //nolint:gosec // at most MaxFrameLen
	dst = binary.LittleEndian.AppendUint32(dst, crc32.Checksum(rec, castagnoli))

	return append(dst, rec...)
}
