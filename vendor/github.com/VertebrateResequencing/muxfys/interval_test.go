// Copyright © 2017, 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
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

import (
	"math"
	"math/rand"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestIntervals(t *testing.T) {
	Convey("You can create intervals a number of different ways", t, func() {
		oneThree := NewInterval(int64(1), 3)
		So(oneThree.Start, ShouldEqual, 1)
		So(oneThree.End, ShouldEqual, 3)
		twoSix := NewInterval(int64(2), 5)
		So(twoSix.Start, ShouldEqual, 2)
		So(twoSix.End, ShouldEqual, 6)
		eightTen := Interval{8, 10}
		So(eightTen.Start, ShouldEqual, 8)
		So(eightTen.End, ShouldEqual, 10)
		fifteenEighteen := Interval{15, 18}
		fiveTen := Interval{5, 10}
		tenEighteen := Interval{10, 18}
		fourSix := Interval{4, 6}
		sevenTen := Interval{Start: 7, End: 10}
		elevenEighteen := Interval{Start: 11, End: 18}
		twentyThirty := Interval{20, 30}
		oneSix := Interval{1, 6}
		oneEighteen := Interval{1, 18}
		fourtyFifty := Interval{40, 50}

		Convey("Length works", func() {
			So(oneThree.Length(), ShouldEqual, 3)
			So(twoSix.Length(), ShouldEqual, 5)
			So(fifteenEighteen.Length(), ShouldEqual, 4)
		})

		Convey("Merging in order works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(twoSix)
			So(newIvs, ShouldResemble, Intervals{Interval{4, 6}})
			ivs = ivs.Merge(twoSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(eightTen)
			So(newIvs, ShouldResemble, Intervals{eightTen})
			ivs = ivs.Merge(eightTen)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(fifteenEighteen)
			So(newIvs, ShouldResemble, Intervals{fifteenEighteen})
			ivs = ivs.Merge(fifteenEighteen)
			So(len(ivs), ShouldEqual, 3)

			expected := Intervals{oneSix, eightTen, fifteenEighteen}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Merging out of order works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(twoSix)
			So(newIvs, ShouldResemble, Intervals{twoSix})
			ivs = ivs.Merge(twoSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(fifteenEighteen)
			So(newIvs, ShouldResemble, Intervals{fifteenEighteen})
			ivs = ivs.Merge(fifteenEighteen)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(eightTen)
			So(newIvs, ShouldResemble, Intervals{eightTen})
			ivs = ivs.Merge(eightTen)
			So(len(ivs), ShouldEqual, 3)

			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{Interval{1, 1}})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 3)

			expected := Intervals{oneSix, eightTen, fifteenEighteen}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Merging where everything merges together works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(twoSix)
			So(newIvs, ShouldResemble, Intervals{Interval{4, 6}})
			ivs = ivs.Merge(twoSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(fiveTen)
			So(newIvs, ShouldResemble, Intervals{Interval{7, 10}})
			ivs = ivs.Merge(fiveTen)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(tenEighteen)
			So(newIvs, ShouldResemble, Intervals{Interval{11, 18}})
			ivs = ivs.Merge(tenEighteen)
			So(len(ivs), ShouldEqual, 1)

			expected := Intervals{oneEighteen}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Merging unsorted where everything merges together works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(twoSix)
			So(newIvs, ShouldResemble, Intervals{twoSix})
			ivs = ivs.Merge(twoSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{Interval{1, 1}})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(tenEighteen)
			So(newIvs, ShouldResemble, Intervals{tenEighteen})
			ivs = ivs.Merge(tenEighteen)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(fiveTen)
			So(newIvs, ShouldResemble, Intervals{Interval{7, 9}})
			ivs = ivs.Merge(fiveTen)
			So(len(ivs), ShouldEqual, 1)

			expected := Intervals{oneEighteen}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Merging where nothing merges together works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(sevenTen)
			So(newIvs, ShouldResemble, Intervals{sevenTen})
			ivs = ivs.Merge(sevenTen)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(twentyThirty)
			So(newIvs, ShouldResemble, Intervals{twentyThirty})
			ivs = ivs.Merge(twentyThirty)
			So(len(ivs), ShouldEqual, 3)

			expected := Intervals{oneThree, sevenTen, twentyThirty}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Merging adjacent intervals works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(fourSix)
			So(newIvs, ShouldResemble, Intervals{fourSix})
			ivs = ivs.Merge(fourSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(sevenTen)
			So(newIvs, ShouldResemble, Intervals{sevenTen})
			ivs = ivs.Merge(sevenTen)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(elevenEighteen)
			So(newIvs, ShouldResemble, Intervals{elevenEighteen})
			ivs = ivs.Merge(elevenEighteen)
			So(len(ivs), ShouldEqual, 1)

			expected := Intervals{oneEighteen}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Merging unsorted adjacent intervals works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(fourSix)
			So(newIvs, ShouldResemble, Intervals{fourSix})
			ivs = ivs.Merge(fourSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(elevenEighteen)
			So(newIvs, ShouldResemble, Intervals{elevenEighteen})
			ivs = ivs.Merge(elevenEighteen)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(sevenTen)
			So(newIvs, ShouldResemble, Intervals{sevenTen})
			ivs = ivs.Merge(sevenTen)
			So(len(ivs), ShouldEqual, 1)

			expected := Intervals{oneEighteen}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Merging subsumable intervals works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(fourSix)
			So(newIvs, ShouldResemble, Intervals{fourSix})
			ivs = ivs.Merge(fourSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(twoSix)
			So(newIvs, ShouldBeEmpty)
			ivs = ivs.Merge(twoSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldBeEmpty)
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			oneSeven := Interval{1, 7}

			newIvs = ivs.Difference(oneSix)
			So(newIvs, ShouldBeEmpty)
			ivs = ivs.Merge(oneSix)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(oneSeven)
			So(newIvs, ShouldResemble, Intervals{Interval{7, 7}})
			ivs = ivs.Merge(oneSeven)
			So(len(ivs), ShouldEqual, 1)

			expected := Intervals{oneSeven}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Difference works across multiple intervals", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(sevenTen)
			So(newIvs, ShouldResemble, Intervals{sevenTen})
			ivs = ivs.Merge(sevenTen)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(fifteenEighteen)
			So(newIvs, ShouldResemble, Intervals{fifteenEighteen})
			ivs = ivs.Merge(fifteenEighteen)
			So(len(ivs), ShouldEqual, 3)

			newIvs = ivs.Difference(twentyThirty)
			So(newIvs, ShouldResemble, Intervals{twentyThirty})
			ivs = ivs.Merge(twentyThirty)
			So(len(ivs), ShouldEqual, 4)

			newIvs = ivs.Difference(fourtyFifty)
			So(newIvs, ShouldResemble, Intervals{fourtyFifty})
			ivs = ivs.Merge(fourtyFifty)
			So(len(ivs), ShouldEqual, 5)

			fiveTwentyFive := Interval{5, 25}
			newIvs = ivs.Difference(fiveTwentyFive)
			So(newIvs, ShouldResemble, Intervals{Interval{5, 6}, Interval{11, 14}, Interval{19, 19}})
			ivs = ivs.Merge(fiveTwentyFive)
			So(len(ivs), ShouldEqual, 3)

			expected := Intervals{oneThree, Interval{5, 30}, fourtyFifty}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Difference works across multiple intervals that it subsumes", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(sevenTen)
			So(newIvs, ShouldResemble, Intervals{sevenTen})
			ivs = ivs.Merge(sevenTen)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(fifteenEighteen)
			So(newIvs, ShouldResemble, Intervals{fifteenEighteen})
			ivs = ivs.Merge(fifteenEighteen)
			So(len(ivs), ShouldEqual, 3)

			newIvs = ivs.Difference(twentyThirty)
			So(newIvs, ShouldResemble, Intervals{twentyThirty})
			ivs = ivs.Merge(twentyThirty)
			So(len(ivs), ShouldEqual, 4)

			newIvs = ivs.Difference(fourtyFifty)
			So(newIvs, ShouldResemble, Intervals{fourtyFifty})
			ivs = ivs.Merge(fourtyFifty)
			So(len(ivs), ShouldEqual, 5)

			fiveThirtyTwo := Interval{5, 32}
			newIvs = ivs.Difference(fiveThirtyTwo)
			So(newIvs, ShouldResemble, Intervals{Interval{5, 6}, Interval{11, 14}, Interval{19, 19}, Interval{31, 32}})
			ivs = ivs.Merge(fiveThirtyTwo)
			So(len(ivs), ShouldEqual, 3)

			expected := Intervals{oneThree, fiveThirtyTwo, fourtyFifty}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Difference works across multiple out-of-order intervals that it subsumes", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(sevenTen)
			So(newIvs, ShouldResemble, Intervals{sevenTen})
			ivs = ivs.Merge(sevenTen)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(twentyThirty)
			So(newIvs, ShouldResemble, Intervals{twentyThirty})
			ivs = ivs.Merge(twentyThirty)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 3)

			newIvs = ivs.Difference(fourtyFifty)
			So(newIvs, ShouldResemble, Intervals{fourtyFifty})
			ivs = ivs.Merge(fourtyFifty)
			So(len(ivs), ShouldEqual, 4)

			newIvs = ivs.Difference(fifteenEighteen)
			So(newIvs, ShouldResemble, Intervals{fifteenEighteen})
			ivs = ivs.Merge(fifteenEighteen)
			So(len(ivs), ShouldEqual, 5)

			fiveThirtyTwo := Interval{5, 32}
			newIvs = ivs.Difference(fiveThirtyTwo)
			So(newIvs, ShouldResemble, Intervals{Interval{5, 6}, Interval{11, 14}, Interval{19, 19}, Interval{31, 32}})
			ivs = ivs.Merge(fiveThirtyTwo)
			So(len(ivs), ShouldEqual, 3)

			expected := Intervals{oneThree, fiveThirtyTwo, fourtyFifty}
			So(ivs, ShouldResemble, expected)
		})

		Convey("Difference works when the interval overlaps and extends past the last interval in the set", func() {
			var ivs, newIvs Intervals
			ivs = ivs.Merge(Interval{0, 147455})
			So(len(ivs), ShouldEqual, 1)

			ivs = ivs.Merge(Interval{348160, 356351})
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(Interval{352256, 368639})
			So(newIvs, ShouldResemble, Intervals{Interval{356352, 368639}})
			ivs = ivs.Merge(Interval{352256, 368639})
			So(len(ivs), ShouldEqual, 2)
		})

		Convey("Truncate works", func() {
			var ivs, newIvs Intervals
			newIvs = ivs.Difference(sevenTen)
			So(newIvs, ShouldResemble, Intervals{sevenTen})
			ivs = ivs.Merge(sevenTen)
			So(len(ivs), ShouldEqual, 1)

			newIvs = ivs.Difference(twentyThirty)
			So(newIvs, ShouldResemble, Intervals{twentyThirty})
			ivs = ivs.Merge(twentyThirty)
			So(len(ivs), ShouldEqual, 2)

			newIvs = ivs.Difference(oneThree)
			So(newIvs, ShouldResemble, Intervals{oneThree})
			ivs = ivs.Merge(oneThree)
			So(len(ivs), ShouldEqual, 3)

			newIvs = ivs.Difference(fourtyFifty)
			So(newIvs, ShouldResemble, Intervals{fourtyFifty})
			ivs = ivs.Merge(fourtyFifty)
			So(len(ivs), ShouldEqual, 4)

			newIvs = ivs.Difference(fifteenEighteen)
			So(newIvs, ShouldResemble, Intervals{fifteenEighteen})
			ivs = ivs.Merge(fifteenEighteen)
			So(len(ivs), ShouldEqual, 5)

			ivs = ivs.Truncate(17)

			expected := Intervals{oneThree, sevenTen, Interval{15, 17}}
			So(ivs, ShouldResemble, expected)

			ivs = ivs.Truncate(13)

			expected = Intervals{oneThree, sevenTen}
			So(ivs, ShouldResemble, expected)

			ivs = ivs.Truncate(0)
			So(ivs, ShouldResemble, Intervals{})
		})
	})

	Convey("Merging many intervals is fast", t, func() {
		// we will simulate reading a 1000000000 byte file 10000 bytes at a
		// time. First we read the second half of the file, then we read the
		// whole file. Within each half most of the reads are sequential, but a
		// handful of them are swapped out of order, as happens in reality (for
		// some unknown reason)
		fileSize := 1000000000 - 1
		halfSize := (fileSize / 2) + 1
		readSize := 10000
		var inputs []int
		var exepectedNew []bool
		for i := halfSize; i < fileSize; i += readSize {
			inputs = append(inputs, i)
			exepectedNew = append(exepectedNew, true)
		}
		for i := 0; i < halfSize; i += readSize {
			inputs = append(inputs, i)
			exepectedNew = append(exepectedNew, true)
		}
		for i := halfSize; i < fileSize; i += readSize {
			inputs = append(inputs, i)
			exepectedNew = append(exepectedNew, false)
		}

		// swap 10% of intervals with their neighbours
		toSwap := int(math.Ceil((float64(len(inputs)) / 100.0) * 10.0))
		doneSwaps := 0
		swapped := make(map[int]bool)
		for {
			swap := rand.Intn(len(inputs))
			if _, done := swapped[swap]; done {
				continue
			}
			inputs[swap], inputs[swap+1] = inputs[swap+1], inputs[swap]
			exepectedNew[swap], exepectedNew[swap+1] = exepectedNew[swap+1], exepectedNew[swap]
			swapped[swap] = true
			swapped[swap+1] = true
			doneSwaps++
			if doneSwaps == toSwap {
				break
			}
		}

		var ivs Intervals
		errors := 0
		t := time.Now()
		for i, input := range inputs {
			iv := NewInterval(int64(input), int64(readSize))
			newIvs := ivs.Difference(iv)
			if (len(newIvs) == 1) != exepectedNew[i] {
				errors++
			}
			ivs = ivs.Merge(iv)
		}
		// fmt.Printf("\ntook %s\n", time.Since(t))
		So(errors, ShouldEqual, 0)
		So(len(ivs), ShouldEqual, 1)
		So(time.Since(t).Seconds(), ShouldBeLessThan, 1) // 30ms on my machine
	})
}
