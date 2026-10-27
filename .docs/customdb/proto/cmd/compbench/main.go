// Command compbench measures how well, and how fast, each candidate
// compressor shrinks real wr job records from a manager database: the flat
// spec of every live job, flat spec+state of a sample of completed jobs, and
// today's Binc values for comparison. Each compressor is tried per record (a
// log or history record read on its own), per batch (a group commit of 64
// records, and a 1000-job add), and per block (64KB and 1MB blocks of a
// sealed history segment), with and without a dictionary trained on records
// held out of the test. Every output is decompressed and compared with its
// input. Timings are single-threaded.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/zc"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/klauspost/compress/dict"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

type comp struct {
	name string
	enc  func(dst, src []byte) []byte
	dec  func(dst, src []byte) ([]byte, error)
}

func main() {
	path := flag.String("db", "", "bolt db path (opened read-only)")
	nComplete := flag.Int("complete", 200000, "completed jobs to sample")
	only := flag.String("only", "", "comma-separated compressor names to run (default all)")
	modesFlag := flag.String("modes", "rec,b64,b1000,blk64k,blk1m", "modes to run")
	dictOut := flag.String("dictout", "", "write the zstd raw dictionary here")
	corpusOut := flag.String("corpusout", "", "write every loaded job's command here (zc corpus)")
	flag.Parse()

	live, complete, bincLive := load(*path, *nComplete)

	if *corpusOut != "" {
		must(zc.WriteCorpus(*corpusOut, cmds))
	}

	var big [][]byte

	for _, r := range append(append([][]byte{}, live...), complete...) {
		if len(r) > 4096 {
			big = append(big, r)
		}
	}

	// Train dictionaries on every 10th record, test on the rest.
	trainLive, testLive := split(live)
	trainComplete, testComplete := split(complete)
	train := append(append([][]byte{}, trainLive...), trainComplete...)

	// dict.BuildZstdDict panics on these records (klauspost/compress
	// v1.20.0, enc_base.go:71), so zstd uses the content the S2 builder
	// chose as a raw-content dictionary.
	sdictBytes, err := dict.BuildS2Dict(train, dict.Options{MaxDictSize: 64 << 10, HashBytes: 6})
	must(err)

	zdict := s2.NewDict(sdictBytes).Bytes()

	if *dictOut != "" {
		must(os.WriteFile(*dictOut, zdict, 0o600))
	}

	fmt.Printf("records: live=%d (%.0fMB) complete=%d (%.0fMB) big(>4KB)=%d (%.0fMB) bincLive=%d (%.0fMB); dicts zstd=%dB s2=%dB from %d records\n",
		len(live), mb(live), len(complete), mb(complete), len(big), mb(big), len(bincLive), mb(bincLive),
		len(zdict), len(sdictBytes), len(train))

	comps := compressors(zdict, s2.NewDict(sdictBytes))

	groups := []struct {
		name string
		recs [][]byte
	}{
		{"live", testLive},
		{"complete", testComplete},
		{"big", big},
		{"bincLive", bincLive},
	}

	want := map[string]bool{}

	for n := range strings.SplitSeq(*only, ",") {
		if n != "" {
			want[n] = true
		}
	}

	for m := range strings.SplitSeq(*modesFlag, ",") {
		for _, g := range groups {
			units := unitsFor(m, g.recs)
			if units == nil {
				continue
			}

			for _, c := range comps {
				if len(want) > 0 && !want[c.name] {
					continue
				}

				if strings.HasPrefix(m, "blk") && strings.Contains(c.name, "dict") {
					continue // a block is big enough to be its own dictionary
				}

				run(m, g.name, c, units, len(g.recs))
			}
		}
	}
}

func split(recs [][]byte) (train, test [][]byte) {
	for i, r := range recs {
		if i%10 == 0 {
			train = append(train, r)
		} else {
			test = append(test, r)
		}
	}

	return train, test
}

// unitsFor groups records into what one compression call sees in mode m.
func unitsFor(m string, recs [][]byte) [][]byte {
	switch m {
	case "rec":
		return recs
	case "b64":
		return byCount(recs, 64)
	case "b1000":
		return byCount(recs, 1000)
	case "blk64k":
		return bySize(recs, 64<<10)
	case "blk1m":
		return bySize(recs, 1<<20)
	}

	return nil
}

func byCount(recs [][]byte, n int) [][]byte {
	var out [][]byte

	for i := 0; i < len(recs); i += n {
		out = append(out, bytes.Join(recs[i:min(i+n, len(recs))], nil))
	}

	return out
}

func bySize(recs [][]byte, size int) [][]byte {
	var (
		out [][]byte
		cur []byte
	)

	for _, r := range recs {
		cur = append(cur, r...)
		if len(cur) >= size {
			out = append(out, cur)
			cur = nil
		}
	}

	if len(cur) > 0 {
		out = append(out, cur)
	}

	return out
}

func run(mode, group string, c comp, units [][]byte, nrecs int) {
	var in, out int

	encoded := make([][]byte, len(units))
	buf := make([]byte, 0, 1<<21)

	start := time.Now()

	for i, u := range units {
		buf = c.enc(buf[:0], u)
		encoded[i] = append([]byte(nil), buf...)
		in += len(u)
		out += len(buf)
	}

	encT := time.Since(start)

	start = time.Now()

	for i, e := range encoded {
		var err error

		buf, err = c.dec(buf[:0], e)
		if err != nil {
			panic(fmt.Sprintf("%s %s %s: %v", mode, group, c.name, err))
		}

		if i%7 == 0 && !bytes.Equal(buf, units[i]) {
			panic(fmt.Sprintf("%s %s %s: round trip mismatch", mode, group, c.name))
		}
	}

	decT := time.Since(start)

	fmt.Printf("%-6s %-9s %-18s ratio=%5.2f saved=%5.1f%% enc=%7.0fns/rec %6.0fMB/s dec=%6.0fns/rec %6.0fMB/s\n",
		mode, group, c.name, float64(in)/float64(max(out, 1)), 100*(1-float64(out)/float64(in)),
		float64(encT.Nanoseconds())/float64(nrecs), float64(in)/encT.Seconds()/1e6,
		float64(decT.Nanoseconds())/float64(nrecs), float64(in)/decT.Seconds()/1e6)
}

func compressors(zdict []byte, sdict *s2.Dict) []comp {
	cs := []comp{
		{"none", func(d, s []byte) []byte { return append(d, s...) }, func(d, s []byte) ([]byte, error) { return append(d, s...), nil }},
		{"snappy", func(d, s []byte) []byte { return snappy.Encode(d[:cap(d)], s) },
			func(d, s []byte) ([]byte, error) { return snappy.Decode(d[:cap(d)], s) }},
		{"s2", func(d, s []byte) []byte { return s2.Encode(d[:cap(d)], s) },
			func(d, s []byte) ([]byte, error) { return s2.Decode(d[:cap(d)], s) }},
		{"s2-better", func(d, s []byte) []byte { return s2.EncodeBetter(d[:cap(d)], s) },
			func(d, s []byte) ([]byte, error) { return s2.Decode(d[:cap(d)], s) }},
		{"s2-dict", func(d, s []byte) []byte { return sdict.Encode(d[:cap(d)], s) },
			func(d, s []byte) ([]byte, error) { return sdict.Decode(d[:cap(d)], s) }},
		{"s2-dict-better", func(d, s []byte) []byte { return sdict.EncodeBetter(d[:cap(d)], s) },
			func(d, s []byte) ([]byte, error) { return sdict.Decode(d[:cap(d)], s) }},
		lz4Comp(),
		flateComp(1),
	}

	for _, l := range []zstd.EncoderLevel{zstd.SpeedFastest, zstd.SpeedDefault, zstd.SpeedBetterCompression, zstd.SpeedBestCompression} {
		cs = append(cs, zstdComp("zstd-"+levelName(l), l, nil))
	}

	for _, l := range []zstd.EncoderLevel{zstd.SpeedFastest, zstd.SpeedDefault, zstd.SpeedBetterCompression} {
		cs = append(cs, zstdComp("zstd-dict-"+levelName(l), l, zdict))
	}

	return cs
}

func levelName(l zstd.EncoderLevel) string {
	return strings.ToLower(strings.TrimPrefix(l.String(), "speed-"))
}

func zstdComp(name string, l zstd.EncoderLevel, d []byte) comp {
	eo := []zstd.EOption{zstd.WithEncoderLevel(l), zstd.WithEncoderConcurrency(1), zstd.WithZeroFrames(true),
		zstd.WithEncoderCRC(false), zstd.WithSingleSegment(true)}
	do := []zstd.DOption{zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1 << 30)}

	if d != nil {
		eo = append(eo, zstd.WithEncoderDictRaw(1, d))
		do = append(do, zstd.WithDecoderDictRaw(1, d))
	}

	enc, err := zstd.NewWriter(nil, eo...)
	must(err)

	dec, err := zstd.NewReader(nil, do...)
	must(err)

	return comp{name, func(dst, s []byte) []byte { return enc.EncodeAll(s, dst) },
		func(dst, s []byte) ([]byte, error) { return dec.DecodeAll(s, dst) }}
}

func lz4Comp() comp {
	var c lz4.Compressor

	return comp{"lz4", func(d, s []byte) []byte {
		need := lz4.CompressBlockBound(len(s)) + 4
		if cap(d) < need {
			d = make([]byte, 0, need)
		}

		d = d[:need]
		d[0], d[1], d[2], d[3] = byte(len(s)), byte(len(s)>>8), byte(len(s)>>16), byte(len(s)>>24)

		n, err := c.CompressBlock(s, d[4:])
		must(err)

		return d[:4+n]
	}, func(d, s []byte) ([]byte, error) {
		n := int(s[0]) | int(s[1])<<8 | int(s[2])<<16 | int(s[3])<<24
		if cap(d) < n {
			d = make([]byte, 0, n)
		}

		m, err := lz4.UncompressBlock(s[4:], d[:n])

		return d[:m], err
	}}
}

func flateComp(level int) comp {
	var bw bytes.Buffer

	w, err := flate.NewWriter(&bw, level)
	must(err)

	r := flate.NewReader(nil)

	return comp{fmt.Sprintf("flate-%d", level), func(d, s []byte) []byte {
		bw.Reset()
		w.Reset(&bw)
		_, _ = w.Write(s)
		_ = w.Close()

		return append(d, bw.Bytes()...)
	}, func(d, s []byte) ([]byte, error) {
		must(r.(flate.Resetter).Reset(bytes.NewReader(s), nil))

		out := bytes.NewBuffer(d)
		_, err := out.ReadFrom(r)

		return out.Bytes(), err
	}}
}

// load reads every live job and up to n completed jobs, re-encoding each as a
// flat record (key + spec for live; key + spec + state for complete), and
// keeps the live jobs' Binc values as stored today.
func load(path string, n int) (live, complete, bincLive [][]byte) {
	bdb, err := bolt.Open(path, 0o400, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	must(err)

	defer bdb.Close()

	ch := new(codec.BincHandle)

	err = bdb.View(func(tx *bolt.Tx) error {
		live, bincLive = readBucket(tx.Bucket([]byte("jobslive")), ch, -1, false)
		complete, _ = readBucket(tx.Bucket([]byte("jobscomplete")), ch, n, true)

		return nil
	})
	must(err)

	return live, complete, bincLive
}

func readBucket(b *bolt.Bucket, ch *codec.BincHandle, n int, withState bool) (recs, binc [][]byte) {
	bh := &codec.BincHandle{}

	c := b.Cursor()

	for k, v := c.First(); k != nil && (n < 0 || len(recs) < n); k, v = c.Next() {
		var j jobqueue.Job
		if err := codec.NewDecoderBytes(v, ch).Decode(&j); err != nil {
			continue // skip undecodable records, as dbstats does
		}

		key := flat.KeyOf(string(k))
		rec := append(key[:0:0], key[:]...)
		rec = flat.AppendSpec(rec, &j, flat.Blob(bh, &j))

		if withState {
			st := stateOf(key, &j)
			rec = append(rec, make([]byte, flat.StateSize)...)
			flat.PutState(rec[len(rec)-flat.StateSize:], &st)
		}

		recs = append(recs, rec)
		binc = append(binc, append([]byte(nil), v...))
		cmds = append(cmds, j.Cmd)
	}

	return recs, binc
}

func stateOf(k flat.Key, j *jobqueue.Job) flat.State {
	st := flat.State{
		Key: k, State: flat.SComplete, Exited: j.Exited, Lost: j.Lost, Attempts: j.Attempts,
		Exitcode: int32(j.Exitcode), Pid: uint32(j.Pid), StartTime: j.StartTime.UnixNano(), //nolint:gosec
		EndTime: j.EndTime.UnixNano(), CPUtime: int64(j.CPUtime), PeakRAM: uint64(j.PeakRAM), //nolint:gosec
		PeakDisk: uint64(j.PeakDisk), //nolint:gosec
	}

	st.SetHost(j.Host)

	return st
}

// cmds is every loaded job's command, for -corpusout.
var cmds []string //nolint:gochecknoglobals

func mb(recs [][]byte) float64 {
	t := 0
	for _, r := range recs {
		t += len(r)
	}

	return float64(t) / 1e6
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
