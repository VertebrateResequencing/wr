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
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"slices"
)

const (
	// scanBufferSize is the size of Scan's read buffer.
	scanBufferSize = 1 << 20

	// payloadReadChunk is the most of a payload Scan reads before it has seen
	// that many bytes arrive, so that a damaged length field cannot make it
	// allocate up to MaxFrameLen for a payload that is not there.
	payloadReadChunk = 1 << 20
)

// ErrBadRef is the error of ReadAt given a Ref that names no undamaged record.
var ErrBadRef = errors.New("wal: ref names no undamaged record")

// Scan reads frames from r, calling fn with each record and its Ref, until the
// first damage: a short header or payload, a CRC mismatch, or a length of 0 or
// over 1GiB. A block is all or nothing: its records are given only once it has
// passed its CRC, decompressed and been found to hold nothing but whole, plain,
// undamaged frames; otherwise it is damage. Damage is the end of the log, not
// an error.
//
// It returns end, the offset just after the last frame whose records fn was
// all given, and an error only if reading r failed or fn returned one, which
// stops the scan with end before the frame fn was given the record of.
//
// A plain frame's record has Ref{Off: its offset}; a block's records have the
// block's offset and their index in it. ref.File is 0; callers set it. rec is
// only valid until fn returns.
func Scan(r io.Reader, fn func(ref Ref, rec []byte) error) (int64, error) {
	br := bufio.NewReaderSize(r, scanBufferSize)

	var (
		payload, plain []byte
		end            int64
	)

	for {
		var (
			isBlock, ok bool
			err         error
		)

		payload, isBlock, ok, err = readFrame(br, payload)
		if !ok || err != nil {
			return end, err
		}

		plain, ok, err = giveRecords(payload, isBlock, plain, Ref{Off: uint64(end)}, fn)
		if !ok || err != nil {
			return end, err
		}

		end += FrameHeader + int64(len(payload))
	}
}

// ReadAt returns the record ref names in f, a log file: the plain frame at
// ref.Off, whose Idx must be 0, or record ref.Idx of the block at ref.Off.
// ref.File is not used; the caller picks f by it. It returns ErrBadRef if there
// is no undamaged frame at ref.Off or no record ref.Idx in it, and the error of
// reading f otherwise. The record is the caller's to keep.
func ReadAt(f io.ReaderAt, ref Ref) ([]byte, error) {
	if ref.Off > math.MaxInt64-FrameHeader-MaxFrameLen {
		return nil, fmt.Errorf("%w: %+v", ErrBadRef, ref)
	}

	payload, isBlock, ok, err := readFrame(io.NewSectionReader(f, int64(ref.Off), FrameHeader+MaxFrameLen), nil)
	if err != nil {
		return nil, fmt.Errorf("wal: read %+v: %w", ref, err)
	}

	if !ok {
		return nil, fmt.Errorf("%w: %+v", ErrBadRef, ref)
	}

	if !isBlock {
		if ref.Idx != 0 {
			return nil, fmt.Errorf("%w: %+v", ErrBadRef, ref)
		}

		return payload, nil
	}

	return blockRecord(payload, ref)
}

// readFrame reads the next frame from r, returning its payload in buf, whether
// it is a block, and whether it read one whole and undamaged. The error is only
// from reading r.
func readFrame(r io.Reader, buf []byte) ([]byte, bool, bool, error) {
	var header [FrameHeader]byte

	if _, err := io.ReadFull(r, header[:]); err != nil {
		return buf, false, false, damageIsEnd(err)
	}

	lenFlags := binary.LittleEndian.Uint32(header[:4])
	isBlock := lenFlags&blockFlag != 0

	n := lenFlags &^ blockFlag
	// The boundary at exactly MaxFrameLen is untested: proving it needs a 1GiB
	// payload, too much memory for the test suite.
	if n == 0 || n > MaxFrameLen {
		return buf, false, false, nil
	}

	payload, err := readPayload(r, buf[:0], int(n))
	if err != nil {
		return payload, false, false, damageIsEnd(err)
	}

	want := binary.LittleEndian.Uint32(header[4:])
	if isBlock {
		return payload, true, blockCRC(lenFlags, payload) == want, nil
	}

	return payload, false, crc32.Checksum(payload, castagnoli) == want, nil
}

// damageIsEnd returns nil for the errors a log cut short gives, which end it,
// and err otherwise.
func damageIsEnd(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}

	return err
}

// readPayload reads n bytes from r into buf, which it returns, growing it no
// faster than bytes arrive.
func readPayload(r io.Reader, buf []byte, n int) ([]byte, error) {
	for len(buf) < n {
		start := len(buf)
		chunk := min(n-start, max(start, payloadReadChunk))
		buf = slices.Grow(buf, chunk)[:start+chunk]

		if _, err := io.ReadFull(r, buf[start:]); err != nil {
			return buf, err
		}
	}

	return buf, nil
}

// blockRecord returns record ref.Idx of the block whose payload this is.
func blockRecord(payload []byte, ref Ref) ([]byte, error) {
	plain, ok := decodeBlock(nil, payload)
	if !ok {
		return nil, fmt.Errorf("%w: %+v", ErrBadRef, ref)
	}

	for idx := uint32(0); len(plain) > 0; idx++ {
		n := FrameHeader + int(binary.LittleEndian.Uint32(plain))
		if idx == ref.Idx {
			// A copy, so a kept record does not pin the whole decoded block.
			return slices.Clone(plain[FrameHeader:n]), nil
		}

		plain = plain[n:]
	}

	return nil, fmt.Errorf("%w: %+v", ErrBadRef, ref)
}

// giveRecords calls fn with the record of a plain frame's payload, or, if the
// block whose payload it is undamaged, with each of the block's records,
// reporting whether it was. plain is storage to decompress a block into, which
// it returns for reuse. The error is only from fn.
func giveRecords(payload []byte, isBlock bool, plain []byte, ref Ref,
	fn func(ref Ref, rec []byte) error) ([]byte, bool, error) {
	if !isBlock {
		return plain, true, fn(ref, payload)
	}

	plain, ok := decodeBlock(plain, payload)
	if !ok {
		return plain, false, nil
	}

	for b := plain; len(b) > 0; ref.Idx++ {
		n := FrameHeader + int(binary.LittleEndian.Uint32(b))
		if err := fn(ref, b[FrameHeader:n]); err != nil {
			return plain, false, err
		}

		b = b[n:]
	}

	return plain, true, nil
}

// decodeBlock decompresses a block's payload into plain's storage, returning
// it, and whether the block is undamaged: that it decompressed into nothing
// but whole, plain, undamaged, non-empty frames, at least one.
func decodeBlock(plain, payload []byte) ([]byte, bool) {
	// payload is never empty: readFrame treats a length of 0 as damage.
	plain, err := decompress(plain, payload[0], payload[1:])

	return plain, err == nil && len(plain) > 0 && wholePlainFrames(plain)
}

// wholePlainFrames reports whether b is nothing but whole, plain, non-empty
// frames whose CRCs match.
func wholePlainFrames(b []byte) bool {
	for len(b) > 0 {
		if len(b) < FrameHeader {
			return false
		}

		lenFlags := binary.LittleEndian.Uint32(b)
		// A nested block's bit 31 makes its length over len(b), which a
		// decompressed block keeps to MaxFrameLen, so it fails this too.
		if lenFlags == 0 || int64(lenFlags) > int64(len(b)-FrameHeader) {
			return false
		}

		rec := b[FrameHeader : FrameHeader+int(lenFlags)]
		if crc32.Checksum(rec, castagnoli) != binary.LittleEndian.Uint32(b[4:]) {
			return false
		}

		b = b[FrameHeader+len(rec):]
	}

	return true
}
