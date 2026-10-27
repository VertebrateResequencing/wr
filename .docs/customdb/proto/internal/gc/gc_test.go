package gc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/s2"
)

func s2enc(dst, src []byte) []byte { return append(dst, s2.Encode(nil, src)...) }

func s2dec(dst, src []byte) ([]byte, error) {
	out, err := s2.Decode(nil, src)

	return append(dst, out...), err
}

// TestCompressedBatchesScanToAPrefix writes compressed batches, then requires
// a full scan to return every record in order and a scan of the file cut at
// every offset to return a prefix of whole batches.
func TestCompressedBatchesScanToAPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	w := New([]*os.File{f})
	w.NoSync = true
	w.Compress = s2enc

	var want [][]byte

	for b := range 20 {
		var recs [][]byte

		for r := range 1 + b%5 {
			recs = append(recs, []byte(fmt.Sprintf("batch %d record %d %s", b, r, bytes.Repeat([]byte("x"), b*7))))
		}

		if err := Wait(w.AppendMulti(make([]int, len(recs)), recs)); err != nil {
			t.Fatal(err)
		}

		want = append(want, recs...)
	}

	w.Close()
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	scan := func(b []byte) [][]byte {
		var got [][]byte

		if _, err := ScanWith(bytes.NewReader(b), s2dec, func(_ int64, rec []byte) error {
			got = append(got, append([]byte(nil), rec...))

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		return got
	}

	if got := scan(data); len(got) != len(want) {
		t.Fatalf("full scan got %d records, want %d", len(got), len(want))
	}

	if got := scan(data); !bytes.Equal(bytes.Join(got, nil), bytes.Join(want, nil)) {
		t.Fatal("full scan records differ")
	}

	for cut := range len(data) {
		got := scan(data[:cut])
		for i := range got {
			if !bytes.Equal(got[i], want[i]) {
				t.Fatalf("cut %d: record %d differs", cut, i)
			}
		}
	}

	if _, err := ScanWith(bytes.NewReader(data), nil, func(int64, []byte) error {
		t.Fatal("a compressed batch was returned without a decompressor")

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
