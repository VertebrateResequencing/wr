// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// The code in this file is heavily based on:
// https://github.com/gastonsimone/go-dojo/tree/master/mergeint (unspecified
// copyright and license).
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package minfys

/*
This file implements the interval-related code used by NewCachedFile to
track which byte intervals have already been downloaded.

Generally more efficient algorithms exist for merging or querying sets of
intervals, eg. http://www.geeksforgeeks.org/merging-intervals/ describes the
commonly used sort approach, as well as a reverse sort variant. You can also
build an interval (range) tree or similar.

However, for this particular use case, we expect that most of the time we
will only have between 1 and 3 prior intervals if we merge every time, and we
expect that we will merge the majority of the time.

In this case, the simple nested loop algorithm used here is significantly faster
than the other options (2x faster than sorting, 10x faster than a tree).

    var ivs Intervals
    for _, data := range inputs {
        iv := NewInterval(int64(data.start), int64(data.length))
        newIvs := ivs.Difference(iv)
        // do something with the new ivs
        ivs = ivs.Merge(iv)
    }
*/

// Interval struct is used to describe something with a start and end. End must
// be greater than start.
type Interval struct {
	Start int64
	End   int64
}

// NewInterval is a convenience for creating a new Interval when you have a
// length instead of an end.
func NewInterval(start, length int64) Interval {
	return Interval{start, start + length - 1}
}

// Length returns the length of this interval.
func (i *Interval) Length() int64 {
	return i.End - i.Start + 1
}

// Overlaps returns true if this interval overlaps with the supplied one.
func (i *Interval) Overlaps(j Interval) bool {
	// https://nedbatchelder.com/blog/201310/range_overlap_in_two_compares.html
	return i.End >= j.Start && j.End >= i.Start
}

// OverlapsOrAdjacent returns true if this interval overlaps with or is adjacent
// to the supplied one.
func (i *Interval) OverlapsOrAdjacent(j Interval) bool {
	return i.End+1 >= j.Start && j.End+1 >= i.Start
}

// Difference returns the portions of j that do NOT overlap with this interval.
// If j starts before this interval, left will be defined. If j ends after this
// interval, right will be defined. If this interval fully contains j, or if j
// does not overlap with this interval at all, left and right will be nil (and
// overlapped will be false in the latter case).
func (i *Interval) Difference(j Interval) (left *Interval, right *Interval, overlapped bool) {
	if !i.Overlaps(j) {
		return
	}

	overlapped = true
	if j.Start < i.Start {
		left = &Interval{j.Start, i.Start - 1}
	}
	if j.End > i.End {
		right = &Interval{i.End + 1, j.End}
	}
	return
}

// Merge merges the supplied interval with this interval if they overlap or are
// adjacent. Returns true if a merge actually occurred.
func (i *Interval) Merge(j Interval) bool {
	if !i.OverlapsOrAdjacent(j) {
		return false
	}

	if j.Start < i.Start {
		i.Start = j.Start
	}
	if j.End > i.End {
		i.End = j.End
	}
	return true
}

// Intervals type is a slice of Interval.
type Intervals []Interval

// Difference returns any portions of iv that do not overlap with any of our
// intervals. Assumes that all of our intervals have been Merge()d in.
func (ivs Intervals) Difference(iv Interval) (diffs Intervals) {
	diffs = append(diffs, iv)
	for _, prior := range ivs {
		for i := 0; i < len(diffs); {
			if left, right, overlapped := prior.Difference(diffs[i]); overlapped {
				if len(diffs) == 1 {
					diffs = nil
				} else {
					diffs = append(diffs[:i], diffs[i+1:]...)
				}

				if left != nil {
					diffs = append(diffs, *left)
				}
				if right != nil {
					diffs = append(diffs, *right)
				}
			} else {
				i++
			}
		}
		if len(diffs) == 0 {
			break
		}
	}

	return
}

// Merge adds another interval to this slice of intervals, merging with any
// prior intervals if it overlaps with or is adjacent to them. Returns the new
// slice of intervals, which have the property of not overlapping with or being
// adjacent to each other.
func (ivs Intervals) Merge(iv Interval) Intervals {
	ivs = append(ivs, iv)

	merged := make(Intervals, 0, len(ivs))
	for _, iv := range ivs {
		for i := 0; i < len(merged); {
			if iv.Merge(merged[i]) {
				// remove position i from merged set
				merged = append(merged[:i], merged[i+1:]...)
			} else {
				i++
			}
		}
		merged = append(merged, iv)
	}

	return merged
}

// Truncate removes all intervals that start after the given position, and
// truncates any intervals that overlap with the position.
func (ivs Intervals) Truncate(pos int64) Intervals {
	if pos == 0 {
		return Intervals{}
	}

	for i := 0; i < len(ivs); {
		if ivs[i].End > pos {
			ivs[i].End = pos
		}
		if ivs[i].Start > pos {
			ivs = append(ivs[:i], ivs[i+1:]...)
		} else {
			i++
		}
	}

	return ivs
}
