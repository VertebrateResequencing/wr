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
	"hash/crc32"
	"io"
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

// Scan reads frames from r, calling fn with each record and its Ref, until the
// first damage: a short header or payload, a CRC mismatch, or a length over
// 1GiB. Damage is the end of the log, not an error. It returns end, the offset
// just after the last frame fn was given, and an error only if reading r failed
// or fn returned one, which stops the scan before that frame is counted in end.
//
// ref.File is 0; callers set it. rec is only valid until fn returns.
//
// A block ends the scan here as damage would, until block decoding is added
// (spec B1).
func Scan(r io.Reader, fn func(ref Ref, rec []byte) error) (int64, error) {
	br := bufio.NewReaderSize(r, scanBufferSize)

	var (
		rec []byte
		end int64
	)

	for {
		var (
			ok  bool
			err error
		)

		rec, ok, err = readFrame(br, rec)
		if !ok || err != nil {
			return end, err
		}

		if err = fn(Ref{Off: uint64(end)}, rec); err != nil {
			return end, err
		}

		end += FrameHeader + int64(len(rec))
	}
}

// readFrame reads the next frame from r, returning its record in buf, and
// whether it read one whole and undamaged. The error is only from reading r.
func readFrame(r io.Reader, buf []byte) ([]byte, bool, error) {
	var header [FrameHeader]byte

	if _, err := io.ReadFull(r, header[:]); err != nil {
		return buf, false, damageIsEnd(err)
	}

	lenFlags := binary.LittleEndian.Uint32(header[:4])
	if lenFlags&blockFlag != 0 || lenFlags > MaxFrameLen {
		return buf, false, nil
	}

	rec, err := readPayload(r, buf[:0], int(lenFlags))
	if err != nil {
		return rec, false, damageIsEnd(err)
	}

	return rec, crc32.Checksum(rec, castagnoli) == binary.LittleEndian.Uint32(header[4:]), nil
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
