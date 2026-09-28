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

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	callCols        = 6
	secsPerHour     = 3600
	quarters        = 4
	lastQuarter     = 3
	halves          = 2
	p50             = 50
	p95             = 95
	p100            = 100
	minSlopePoints  = 3
	scanBufSize     = 1 << 20
	scanMaxLineSize = 1 << 24
)

// opStat is one actor/op's latencies (ms) over the run and in its first and
// last quarters, and its errors.
type opStat struct {
	all, first, last []float64
	errs             int
	lastErr          string
	n                float64
}

// report writes a summary of a run directory to w: per-call latency (whole
// run and first vs last quarter, so growth shows), errors, and the health
// samples' start, peak and end with a per-hour slope over the second half.
func report(w io.Writer, dir string) error {
	calls, err := readTSV(filepath.Join(dir, "calls.tsv"))
	if err != nil {
		return err
	}

	samples, err := readTSV(filepath.Join(dir, "samples.tsv"))
	if err != nil {
		return err
	}

	end := 0.0
	for _, r := range calls {
		end = max(end, num(r[0]))
	}

	reportCalls(w, callStats(calls, end), end)

	if len(samples) > 0 {
		reportHealth(w, samples, end)
	}

	return nil
}

// callStats groups calls.tsv rows by actor/op; the web users are one group.
func callStats(calls [][]string, end float64) map[string]*opStat {
	ops := map[string]*opStat{}

	for _, r := range calls {
		if len(r) < callCols {
			continue
		}

		key := r[1] + "/" + r[2]
		if strings.HasPrefix(r[1], "web") {
			key = "web*/" + r[2]
		}

		st := ops[key]
		if st == nil {
			st = &opStat{}
			ops[key] = st
		}

		st.add(r, end)
	}

	return ops
}

// add counts one calls.tsv row of a run that lasted end seconds.
func (st *opStat) add(r []string, end float64) {
	st.n += num(r[4])

	if r[5] != "" {
		st.errs++
		st.lastErr = r[5]

		return
	}

	ms, t := num(r[3]), num(r[0])
	st.all = append(st.all, ms)

	switch {
	case t < end/quarters:
		st.first = append(st.first, ms)
	case t > end*lastQuarter/quarters:
		st.last = append(st.last, ms)
	}
}

func reportCalls(w io.Writer, ops map[string]*opStat, end float64) {
	keys := make([]string, 0, len(ops))
	for k := range ops {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	fmt.Fprintf(w, "== client calls over %.1f h (ms; q1 = first quarter of the run, q4 = last)\n", end/secsPerHour)
	fmt.Fprintf(w, "%-38s %6s %5s %8s %8s %9s %9s %9s %9s\n", "actor/op", "n", "errs", "p50", "p95", "max",
		"q1p95", "q4p95", "meanN")

	for _, k := range keys {
		st := ops[k]
		cnt := len(st.all) + st.errs
		fmt.Fprintf(w, "%-38s %6d %5d %8.0f %8.0f %9.0f %9.0f %9.0f %9.0f\n", k, cnt, st.errs, pct(st.all, p50),
			pct(st.all, p95), pct(st.all, p100), pct(st.first, p95), pct(st.last, p95), st.n/float64(max(cnt, 1)))
	}

	fmt.Fprintln(w, "\n== last error per op")

	for _, k := range keys {
		if ops[k].errs > 0 {
			fmt.Fprintf(w, "%-38s %s\n", k, ops[k].lastErr)
		}
	}
}

// healthCols are the samples.tsv columns reportHealth summarises.
func healthCols() []struct {
	name string
	idx  int
} {
	return []struct {
		name string
		idx  int
	}{{"rss_mb", 2}, {"hwm_mb", 3}, {"threads", 4}, {"fds", 5}, {"goroutines", 6}, {"heap_inuse_mb", 7},
		{"heap_objects", 8}, {"db_mb", 9}, {"log_mb", 11}, {"ping_ms", 12}, {"load1", 14}}
}

func reportHealth(w io.Writer, samples [][]string, end float64) {
	fmt.Fprintln(w, "\n== manager health (first / peak / last; slope per hour over the second half)")

	for _, c := range healthCols() {
		first, peak, last, perHour := columnTrend(samples, c.idx, end)
		fmt.Fprintf(w, "%-14s %12.0f %12.0f %12.0f   %+12.1f/h\n", c.name, first, peak, last, perHour)
	}
}

// columnTrend returns a samples.tsv column's first, peak and last known
// values, and its slope per hour over the second half of the run.
func columnTrend(samples [][]string, idx int, end float64) (first, peak, last, perHour float64) {
	first, peak, last = unknown, unknown, unknown

	var xs, ys []float64

	for _, r := range samples {
		v := float64(unknown)
		if len(r) > idx {
			v = num(r[idx])
		}

		if v < 0 {
			continue
		}

		if first < 0 {
			first = v
		}

		peak, last = max(peak, v), v

		if t := num(r[0]); t > end/halves {
			xs, ys = append(xs, t/secsPerHour), append(ys, v)
		}
	}

	return first, peak, last, slope(xs, ys)
}

func readTSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() {
		if errc := f.Close(); errc != nil {
			fmt.Fprintln(os.Stderr, "prodsim:", errc)
		}
	}()

	var rows [][]string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, scanBufSize), scanMaxLineSize)

	for first := true; sc.Scan(); first = false {
		if !first {
			rows = append(rows, strings.Split(sc.Text(), "\t"))
		}
	}

	return rows, sc.Err()
}

// num parses a TSV field, returning -1 for anything that is not a number.
func num(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return unknown
	}

	return v
}

// pct returns the p-th percentile of vs, or -1 if vs is empty.
func pct(vs []float64, p float64) float64 {
	if len(vs) == 0 {
		return unknown
	}

	c := append([]float64(nil), vs...)
	sort.Float64s(c)

	return c[int(float64(len(c)-1)*p/p100)]
}

// slope returns the least-squares slope of ys against xs, or 0 with too few
// points.
func slope(xs, ys []float64) float64 {
	n := float64(len(xs))
	if n < minSlopePoints {
		return 0
	}

	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}

	d := n*sxx - sx*sx
	if d == 0 {
		return 0
	}

	return (n*sxy - sx*sy) / d
}
