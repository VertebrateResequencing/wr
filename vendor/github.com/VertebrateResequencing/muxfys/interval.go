// Copyright © 2017 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// The code in this file was initially based on:
// https://github.com/gastonsimone/go-dojo/tree/master/mergeint (unspecified
// copyright and license).
//
//  This file is part of muxfys.
//
//  muxfys is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  muxfys is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with muxfys. If not, see <http://www.gnu.org/licenses/>.

package muxfys

/*
This file implements the interval-related code used by CacheTracker.

Standard algorithms exist for merging or querying sets of intervals, eg.
http://www.geeksforgeeks.org/merging-intervals/ describes the commonly used sort
approach, as well as a reverse sort variant. You can also build an interval
(range) tree or similar.

However, for this particular use case, we expect that most of the time we
will only have between 1 and 3 prior intervals if we merge every time, and we
expect that we will merge the majority of the time.

In this case, a simple nested loop algorithm is 2x faster than blindly sorting
everything each time, and 10x faster than using a tree. The key is to minimise
the number of comparisons that get made; the implementation actually used here
is to simply keep things sorted as they are merged in, and then do as few
comparisons as we can in other methods.

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

// Merge adds another interval to this slice of intervals, merging with any
// prior intervals if it overlaps with or is adjacent to them. Returns the new
// slice of intervals, which have the property of not overlapping with or being
// adjacent to each other. They are also sorted by Start if Merge() was used to
// add all of them.
func (ivs Intervals) Merge(iv Interval) Intervals {
	if len(ivs) == 0 {
		return Intervals{iv}
	}

	merged := make(Intervals, 0, len(ivs))
	for i, prior := range ivs {
		if iv.Merge(prior) {
			if i == len(ivs)-1 {
				merged = append(merged, iv)
			}
			continue
		} else if iv.Start < prior.Start-1 {
			merged = append(merged, iv)
			merged = append(merged, ivs[i:]...)
			break
		} else if i == len(ivs)-1 {
			merged = append(merged, ivs[i:]...)
			merged = append(merged, iv)
			break
		}
		merged = append(merged, prior)
	}

	return merged
}

// Difference returns any portions of iv that do not overlap with any of our
// intervals. Assumes that all of our intervals have been Merge()d in.
func (ivs Intervals) Difference(iv Interval) Intervals {
	if len(ivs) == 0 {
		return Intervals{iv}
	}

	diffs := make(Intervals, 0, len(ivs))
	for i, prior := range ivs {
		if iv.Overlaps(prior) {
			if iv.Start < prior.Start {
				diffs = append(diffs, Interval{iv.Start, prior.Start - 1})
			}
			if iv.End > prior.End {
				iv.Start = prior.End + 1
				if i == len(ivs)-1 {
					diffs = append(diffs, iv)
					break
				}
			} else {
				break
			}
		} else if iv.Start < prior.Start || i == len(ivs)-1 {
			diffs = append(diffs, iv)
			break
		}
	}

	return diffs
}

// Truncate removes all intervals that start after the given position, and
// truncates any intervals that overlap with the position. Assumes that all of
// our intervals have been Merge()d in.
func (ivs Intervals) Truncate(pos int64) Intervals {
	if pos == 0 {
		return Intervals{}
	}

	for i, iv := range ivs {
		if iv.Start > pos {
			ivs = ivs[:i]
			break
		}
		if iv.End > pos {
			ivs = ivs[:i+1]
			ivs[i].End = pos
			break
		}
	}

	return ivs
}
