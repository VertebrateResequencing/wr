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

package scheduler

// This file parses the element ids LSF's bkill reports on, which for an array
// job can name several elements at once.

import (
	"strconv"
	"strings"
)

// lsfIndexRange is one start[-end[:step]] entry of an LSF array index list.
type lsfIndexRange struct {
	start, end, step int
}

// size is how many indices the range covers.
func (r lsfIndexRange) size() int {
	return (r.end-r.start)/r.step + 1
}

// covers reports whether index i is one of the range's indices.
func (r lsfIndexRange) covers(i int) bool {
	return i >= r.start && i <= r.end && (i-r.start)%r.step == 0
}

// takeReportedElements removes from unexplained every element that the id a bkill
// output line reports on covers, returning how many it removed. The id is either
// an element id exactly as wr passed it ("123[4]", or "123" for a non-array job),
// or LSF's collapsed form for several elements of one array, which bkill emits
// when given consecutive elements ("Job <123[1-2:1]>: Job has already
// finished"). LSF documents that form as job_ID[index_list], where index_list is
// a comma-separated list of start[-end[:step]] entries. An id whose index list
// cannot be parsed covers nothing.
func takeReportedElements(id string, unexplained map[string]bool) int {
	if unexplained[id] {
		delete(unexplained, id)

		return 1
	}

	jobID, ranges, ok := parseLSFArrayID(id)
	if !ok {
		return 0
	}

	taken := 0
	for _, r := range ranges {
		taken += takeRange(jobID, r, unexplained)
	}

	return taken
}

// parseLSFArrayID splits an LSF id of the form job_ID[index_list] into its job id
// and index ranges.
func parseLSFArrayID(id string) (string, []lsfIndexRange, bool) {
	jobID, rest, found := strings.Cut(id, "[")
	if !found || jobID == "" {
		return "", nil, false
	}

	list, found := strings.CutSuffix(rest, "]")
	if !found {
		return "", nil, false
	}

	var ranges []lsfIndexRange

	for entry := range strings.SplitSeq(list, ",") {
		r, ok := parseLSFIndexRange(entry)
		if !ok {
			return "", nil, false
		}

		ranges = append(ranges, r)
	}

	return jobID, ranges, true
}

// parseLSFIndexRange parses one start[-end[:step]] entry of an LSF index list.
func parseLSFIndexRange(entry string) (lsfIndexRange, bool) {
	bounds, step, hasStep := strings.Cut(entry, ":")
	start, end, hasEnd := strings.Cut(bounds, "-")

	if !hasEnd {
		end = start
	}

	if !hasStep {
		step = "1"
	}

	n, ok := atois(start, end, step)
	if !ok {
		return lsfIndexRange{}, false
	}

	r := lsfIndexRange{start: n[0], end: n[1], step: n[2]}

	return r, r.start >= 0 && r.end >= r.start && r.step >= 1
}

// atois converts each of strs to an int, reporting false if any is not one.
func atois(strs ...string) ([]int, bool) {
	ints := make([]int, len(strs))

	for i, str := range strs {
		n, err := strconv.Atoi(str)
		if err != nil {
			return nil, false
		}

		ints[i] = n
	}

	return ints, true
}

// takeRange removes from unexplained the elements of jobID that r covers,
// returning how many it removed. It does whichever is less work: looking up each
// of r's indices, or checking each unexplained element against r, so a huge range
// costs no more than the elements still unexplained.
func takeRange(jobID string, r lsfIndexRange, unexplained map[string]bool) int {
	if r.size() <= len(unexplained) {
		taken := 0

		for n := range r.size() {
			element := jobID + "[" + strconv.Itoa(r.start+n*r.step) + "]"
			if unexplained[element] {
				delete(unexplained, element)

				taken++
			}
		}

		return taken
	}

	taken := 0
	prefix := jobID + "["

	for element := range unexplained {
		if elementIndexIn(element, prefix, r) {
			delete(unexplained, element)

			taken++
		}
	}

	return taken
}

// elementIndexIn reports whether element is prefix followed by an index r covers
// and a closing bracket.
func elementIndexIn(element, prefix string, r lsfIndexRange) bool {
	rest, found := strings.CutPrefix(element, prefix)
	if !found {
		return false
	}

	indexStr, found := strings.CutSuffix(rest, "]")
	if !found {
		return false
	}

	i, err := strconv.Atoi(indexStr)

	return err == nil && r.covers(i)
}
