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
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	treeBase = "base"
	treeHead = "head"
	pkgLabel = "pkg: "

	// ciHeader is the second column of a table's unit row,
	// ",<unit>,CI,<unit>,CI,vs base,P", which follows its tree row,
	// ",base,,head,,,", or ",head," when only one tree reports the unit.
	ciHeader = "CI"

	// columnsPerTree is how many columns each tree has in a row: its value
	// and its confidence interval.
	columnsPerTree = 2
)

var errRowOutsideTable = errors.New("benchmark row before any table header")

// figure is one benchmark's figure in one unit: the centre benchstat gives each
// tree, and the p-value of its comparison of them.
type figure struct {
	pkg, bench, unit string
	base, head       float64
	hasBase, hasHead bool
	p                float64
	pText            string
	compared         bool
}

// key identifies the figure's benchmark across units.
func (f figure) key() string {
	return f.pkg + "\x00" + f.bench
}

// readP reads the p-value from the row's last column, "p=0.002 n=6", which
// benchstat writes only when both trees have the figure.
func (f *figure) readP(rec []string) error {
	pCol := 6
	if !f.hasBase || !f.hasHead || len(rec) <= pCol {
		return nil
	}

	text, ok := strings.CutPrefix(rec[pCol], "p=")
	if !ok {
		return nil
	}

	text, _, _ = strings.Cut(text, " ")

	p, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("%s %s p-value: %w", f.bench, f.unit, err)
	}

	f.p, f.pText, f.compared = p, text, true

	return nil
}

// parseBenchstatCSV returns every figure in the CSV benchstat -format csv
// writes, in the order it writes them.
func parseBenchstatCSV(r io.Reader) ([]figure, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	var (
		figures []figure
		table   csvTable
	)

	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return figures, nil
		}

		if err != nil {
			return nil, err
		}

		f, isFigure, err := table.read(rec)
		if err != nil {
			return nil, err
		}

		if isFigure {
			figures = append(figures, f)
		}
	}
}

// csvTable tracks which table of benchstat's CSV output the reader is in.
type csvTable struct {
	pkg   string
	trees []string
	unit  string
}

// read takes in one CSV record, returning the figure it holds if it is a
// benchmark row.
func (t *csvTable) read(rec []string) (figure, bool, error) {
	if t.readHeader(rec) {
		return figure{}, false, nil
	}

	if t.unit == "" || len(t.trees) == 0 {
		return figure{}, false, fmt.Errorf("%w: %q", errRowOutsideTable, strings.Join(rec, ","))
	}

	f, err := t.figure(rec)

	return f, err == nil, err
}

// readHeader takes in rec if it is not a benchmark row: a "key: value" line, a
// tree row, a unit row or a geomean row. It says if it was.
func (t *csvTable) readHeader(rec []string) bool {
	switch {
	case len(rec) == 1:
		if pkg, ok := strings.CutPrefix(rec[0], pkgLabel); ok {
			t.pkg = pkg
		}
	case rec[0] == "geomean":
	case rec[0] != "":
		return false
	case len(rec) > 2 && rec[2] == ciHeader:
		t.unit = rec[1]
	default:
		t.trees = treeNames(rec)
	}

	return true
}

// treeNames returns the tree named over each value column of a tree row.
func treeNames(rec []string) []string {
	var trees []string

	for i := 1; i < len(rec); i += columnsPerTree {
		if rec[i] != "" {
			trees = append(trees, rec[i])
		}
	}

	return trees
}

// figure reads a benchmark row of the current table.
func (t *csvTable) figure(rec []string) (figure, error) {
	f := figure{pkg: t.pkg, bench: rec[0], unit: t.unit}

	for i, tree := range t.trees {
		col := 1 + columnsPerTree*i
		if col >= len(rec) || rec[col] == "" {
			continue
		}

		v, err := strconv.ParseFloat(rec[col], 64)
		if err != nil {
			return f, fmt.Errorf("%s %s %s: %w", f.bench, f.unit, tree, err)
		}

		switch tree {
		case treeBase:
			f.base, f.hasBase = v, true
		case treeHead:
			f.head, f.hasHead = v, true
		}
	}

	return f, f.readP(rec)
}
