// Package zc holds the compressors the prototype log store can apply to spec
// records, chosen by name, and a corpus of real commands for realistic
// compressibility. Every Codec is safe for concurrent use.
package zc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
)

// Codec compresses one record at a time.
type Codec interface {
	Name() string
	// Encode appends src's compressed form to dst.
	Encode(dst, src []byte) []byte
	// Decode appends src's decompressed form to dst.
	Decode(dst, src []byte) ([]byte, error)
}

// New returns the codec called name: none, s2, zstd (fastest level), or
// zstd-dict (fastest level with the raw-content dictionary in dictFile).
func New(name, dictFile string) (Codec, error) {
	switch name {
	case "", "none":
		return nil, nil //nolint:nilnil // nil codec means no compression
	case "s2":
		return s2Codec{}, nil
	case "zstd":
		return newZstd(name, nil)
	case "zstd-dict":
		d, err := os.ReadFile(dictFile)
		if err != nil {
			return nil, fmt.Errorf("zstd-dict needs -dict: %w", err)
		}

		return newZstd(name, d)
	}

	return nil, errors.New("unknown codec " + name)
}

type s2Codec struct{}

func (s2Codec) Name() string { return "s2" }

func (s2Codec) Encode(dst, src []byte) []byte {
	n := len(dst)
	need := s2.MaxEncodedLen(len(src))
	dst = grow(dst, need)

	return dst[:n+len(s2.Encode(dst[n:n+need], src))]
}

func (s2Codec) Decode(dst, src []byte) ([]byte, error) {
	l, err := s2.DecodedLen(src)
	if err != nil {
		return dst, err
	}

	n := len(dst)
	dst = grow(dst, l)

	out, err := s2.Decode(dst[n:n+l], src)

	return dst[:n+len(out)], err
}

func grow(b []byte, n int) []byte {
	if cap(b)-len(b) < n {
		nb := make([]byte, len(b), len(b)+n)
		copy(nb, b)

		return nb
	}

	return b
}

type zstdCodec struct {
	name string
	enc  *zstd.Encoder
	dec  *zstd.Decoder
}

func newZstd(name string, dict []byte) (*zstdCodec, error) {
	eo := []zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(false),
		zstd.WithZeroFrames(true), zstd.WithSingleSegment(true)}
	do := []zstd.DOption{zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(1 << 30)}

	if dict != nil {
		eo = append(eo, zstd.WithEncoderDictRaw(1, dict))
		do = append(do, zstd.WithDecoderDictRaw(1, dict))
	}

	enc, err := zstd.NewWriter(nil, eo...)
	if err != nil {
		return nil, err
	}

	dec, err := zstd.NewReader(nil, do...)
	if err != nil {
		return nil, err
	}

	return &zstdCodec{name: name, enc: enc, dec: dec}, nil
}

func (z *zstdCodec) Name() string { return z.name }

func (z *zstdCodec) Encode(dst, src []byte) []byte { return z.enc.EncodeAll(src, dst) }

func (z *zstdCodec) Decode(dst, src []byte) ([]byte, error) { return z.dec.DecodeAll(src, dst) }

// WriteCorpus writes cmds as length-prefixed strings.
func WriteCorpus(path string, cmds []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}

	for _, c := range cmds {
		if _, err = f.Write(binary.AppendUvarint(nil, uint64(len(c)))); err == nil {
			_, err = io.WriteString(f, c)
		}

		if err != nil {
			f.Close()

			return err
		}
	}

	return f.Close()
}

// ReadCorpus reads what WriteCorpus wrote.
func ReadCorpus(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cmds []string

	for len(b) > 0 {
		l, n := binary.Uvarint(b)
		if n <= 0 || int(l) > len(b)-n { //nolint:gosec
			return nil, errors.New("corrupt corpus")
		}

		cmds = append(cmds, string(b[n:n+int(l)])) //nolint:gosec
		b = b[n+int(l):]                           //nolint:gosec
	}

	return cmds, nil
}
