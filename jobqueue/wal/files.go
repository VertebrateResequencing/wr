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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// segmentExt is the extension of a log segment's file name, NNNNNNNN.wal.
const segmentExt = ".wal"

// segmentPath returns the path of segment n in dir.
func segmentPath(dir string, n uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%08x%s", n, segmentExt))
}

// previousSegment returns the number and size of the highest-numbered segment
// in dir below file, or zeros if there is none.
func previousSegment(dir string, file uint32) (uint32, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("wal: list segments: %w", err)
	}

	prev, entry := latestSegmentBelow(entries, file)
	if entry == nil {
		return 0, 0, nil
	}

	info, err := entry.Info()
	if err != nil {
		return 0, 0, fmt.Errorf("wal: previous segment: %w", err)
	}

	return prev, info.Size(), nil
}

// latestSegmentBelow returns the number and entry of the highest-numbered
// segment in entries below file, or a nil entry if there is none.
func latestSegmentBelow(entries []os.DirEntry, file uint32) (uint32, os.DirEntry) {
	var (
		prev  uint32
		found os.DirEntry
	)

	for _, entry := range entries {
		n, ok := segmentNumber(entry.Name())
		if ok && n < file && (found == nil || n > prev) {
			prev, found = n, entry
		}
	}

	return prev, found
}

// segmentNumber returns the number of the segment named name, and whether name
// is a segment's.
func segmentNumber(name string) (uint32, bool) {
	hex, ok := strings.CutSuffix(name, segmentExt)
	if !ok || len(hex) != 8 {
		return 0, false
	}

	n, err := strconv.ParseUint(hex, 16, 32)

	return uint32(n), err == nil
}
