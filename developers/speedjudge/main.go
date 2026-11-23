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

// Command speedjudge reads the CSV benchstat writes comparing a base and a head
// tree, and prints developers/speed.sh's judgement of it: a "REGRESSION" line
// for each figure that fails the gate, then a "WINDOW" block for each
// benchmark judged by the window-aware rule and a "NOTE" for each under-load
// benchmark that could not be. The rule judges its counts of reserves over
// each threshold on a second, tolerant CSV, comparing head's counts over 1.1
// times each threshold with base's (see judge.go). See DEVELOPERS.md
// "Performance gate". It is developer tooling, not part of the shipped binary.
//
//	speedjudge [-threshold 10] [-alpha 0.05] benchstat.csv benchstat-tol.csv
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

const (
	defaultThreshold = 10
	defaultAlpha     = 0.05
	exitUsage        = 2
)

func main() {
	threshold := flag.Float64("threshold", defaultThreshold, "percent worsening that fails the gate")
	alpha := flag.Float64("alpha", defaultAlpha, "significance a worsening needs to fail the gate")

	flag.Parse()

	if flag.NArg() != 2 { //nolint:mnd
		fmt.Fprintln(os.Stderr, "usage: speedjudge [-threshold pct] [-alpha p] benchstat.csv benchstat-tol.csv")
		os.Exit(exitUsage)
	}

	if err := judgeFiles(flag.Arg(0), flag.Arg(1), *threshold, *alpha); err != nil {
		fmt.Fprintln(os.Stderr, "speedjudge:", err)
		os.Exit(1)
	}
}

func judgeFiles(path, tolerantPath string, threshold, alpha float64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	defer f.Close()

	tf, err := os.Open(tolerantPath)
	if err != nil {
		return err
	}

	defer tf.Close()

	return run(f, tf, os.Stdout, threshold, alpha)
}

// run judges the benchstat CSV read from r, and the tolerant one read from
// tolerant, writing the judgement to w.
func run(r, tolerant io.Reader, w io.Writer, threshold, alpha float64) error {
	figures, err := parseBenchstatCSV(r)
	if err != nil {
		return err
	}

	tolerantFigures, err := parseBenchstatCSV(tolerant)
	if err != nil {
		return fmt.Errorf("tolerant CSV: %w", err)
	}

	_, err = io.WriteString(w, judge(figures, tolerantFigures, threshold, alpha))

	return err
}
