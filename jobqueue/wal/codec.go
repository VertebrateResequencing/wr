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
	"slices"
	"sync"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
)

const (
	// CodecS2 is the codec byte of a block compressed with s2.
	CodecS2 byte = 1

	// CodecZstd is the codec byte of a block compressed with zstd.
	CodecZstd byte = 2
)

var (
	// ErrUnknownCodec is the error of AppendBlock given a codec it does not
	// know.
	ErrUnknownCodec = errors.New("wal: unknown block codec")

	// ErrBlockTooLarge is the error of AppendBlock given frames that, or whose
	// compression, is over MaxFrameLen, so could not be scanned back.
	ErrBlockTooLarge = errors.New("wal: block over the frame limit")

	// ErrEmptyBlock is the error of AppendBlock given no frames: a block of no
	// records would scan as damage.
	ErrEmptyBlock = errors.New("wal: block of no frames")

	// errDecodedTooLarge is the error of decompressing a block that would
	// expand beyond MaxFrameLen, which AppendBlock never writes.
	errDecodedTooLarge = errors.New("wal: block decompresses over the frame limit")
)

// zstdCodec holds the one zstd encoder and decoder, made on first use. Both
// are safe for concurrent EncodeAll and DecodeAll calls.
var zstdCodec = sync.OnceValues(newZstd) //nolint:gochecknoglobals // shared, stateless use

// decompress returns src, compressed with codec, decompressed into dst's
// storage. It refuses to expand beyond MaxFrameLen.
func decompress(dst []byte, codec byte, src []byte) ([]byte, error) {
	switch codec {
	case CodecS2:
		n, err := s2.DecodedLen(src)
		if err != nil {
			return dst, err
		}

		// The boundary at exactly MaxFrameLen is untested: proving it needs a
		// 1GiB decoded block, too much memory for the test suite.
		if n > MaxFrameLen {
			return dst, errDecodedTooLarge
		}

		return s2.Decode(slices.Grow(dst[:0], n)[:n], src)
	case CodecZstd:
		z, err := zstdCodec()
		if err != nil {
			return dst, err
		}

		return z.dec.DecodeAll(src, dst[:0])
	default:
		return dst, fmt.Errorf("%w: %d", ErrUnknownCodec, codec)
	}
}

// zstdPair is a zstd encoder and decoder.
type zstdPair struct {
	enc *zstd.Encoder
	dec *zstd.Decoder
}

// newZstd makes the zstd encoder, at its fastest level with no dictionary, and
// a decoder that refuses to expand a block beyond MaxFrameLen.
func newZstd() (zstdPair, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithEncoderCRC(false), zstd.WithZeroFrames(true))
	if err != nil {
		return zstdPair{}, fmt.Errorf("wal: zstd encoder: %w", err)
	}

	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(MaxFrameLen))
	if err != nil {
		return zstdPair{}, fmt.Errorf("wal: zstd decoder: %w", err)
	}

	return zstdPair{enc: enc, dec: dec}, nil
}

// compress appends src compressed with codec to dst.
func compress(dst []byte, codec byte, src []byte) ([]byte, error) {
	switch codec {
	case CodecS2:
		return s2Append(dst, src), nil
	case CodecZstd:
		z, err := zstdCodec()
		if err != nil {
			return dst, err
		}

		return z.enc.EncodeAll(src, dst), nil
	default:
		return dst, fmt.Errorf("%w: %d", ErrUnknownCodec, codec)
	}
}

// s2Append appends src compressed with s2 to dst.
func s2Append(dst, src []byte) []byte {
	n, need := len(dst), s2.MaxEncodedLen(len(src))
	dst = slices.Grow(dst, need)

	return dst[:n+len(s2.Encode(dst[n:n+need], src))]
}
