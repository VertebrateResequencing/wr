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
	"slices"
	"testing"

	"github.com/klauspost/compress/s2"
)

// fuzzMaxRecords bounds how many records FuzzScan builds a valid log from.
const fuzzMaxRecords = 256

func FuzzScan(f *testing.F) {
	f.Add([]byte{}, uint32(0), byte(0xff))
	f.Add(AppendFrame(nil, []byte("one\nframe")), uint32(2), byte(0x01))

	block, err := AppendBlock(nil, CodecS2, appendFrames(threeRecs...))
	if err != nil {
		f.Fatal(err)
	}

	f.Add(block, uint32(3), byte(0x80))
	f.Add(appendRawBlock(nil, 9, s2.Encode(nil, appendFrames("a", "bb"))), uint32(8), byte(0x08))

	maxLen := binary.LittleEndian.AppendUint32(nil, 0x7fffffff)
	maxLen = binary.LittleEndian.AppendUint32(maxLen, 0)
	f.Add(append(maxLen, "\nsome\npayload"...), uint32(0), byte(0x7f))

	f.Fuzz(func(t *testing.T, data []byte, pos uint32, xor byte) {
		end, err := Scan(bytes.NewReader(data), func(Ref, []byte) error { return nil })
		if err != nil || end < 0 || end > int64(len(data)) {
			t.Fatalf("scan of %d input bytes: end %d, err %v", len(data), end, err)
		}

		recs := fuzzRecords(data)
		log := fuzzLog(t, recs)

		if got := scanAll(log); got.err != nil || !slices.Equal(got.recs, recs) || got.end != int64(len(log)) {
			t.Fatalf("valid log of %d records scanned as %d records, end %d of %d, err %v",
				len(recs), len(got.recs), got.end, len(log), got.err)
		}

		if len(log) == 0 {
			return
		}

		if xor == 0 {
			xor = 0xff
		}

		log[int(pos)%len(log)] ^= xor

		got := scanAll(log)

		prefix := len(got.recs) <= len(recs) && slices.Equal(got.recs, recs[:len(got.recs)])
		if got.err != nil || got.end > int64(len(log)) || !prefix {
			t.Fatalf("log of %d records with byte %d xor %#x scanned as %q, end %d, err %v",
				len(recs), int(pos)%len(log), xor, got.recs, got.end, got.err)
		}
	})
}

// fuzzRecords splits data into the records of a valid log at its newlines,
// skipping empty ones, since records are never empty.
func fuzzRecords(data []byte) []string {
	var recs []string

	for rec := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(recs) == fuzzMaxRecords {
			break
		}

		if len(rec) > 0 {
			recs = append(recs, string(rec))
		}
	}

	return recs
}

// fuzzLog frames the first third of recs as plain frames, the second as an s2
// block and the last as a zstd block.
func fuzzLog(t *testing.T, recs []string) []byte {
	t.Helper()

	third, twoThirds := len(recs)/3, 2*len(recs)/3
	log := appendFrames(recs[:third]...)

	for _, part := range []struct {
		codec byte
		recs  []string
	}{{CodecS2, recs[third:twoThirds]}, {CodecZstd, recs[twoThirds:]}} {
		if len(part.recs) == 0 {
			continue
		}

		var err error

		log, err = AppendBlock(log, part.codec, appendFrames(part.recs...))
		if err != nil {
			t.Fatalf("AppendBlock: %v", err)
		}
	}

	return log
}
