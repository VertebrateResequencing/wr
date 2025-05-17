// Copyright © 2025 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
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

package plist

import (
	"strconv"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestPList(t *testing.T) {
	Convey("Given a set of keys with duplicates and a PList", t, func() {
		keys := []string{"2", "1", "3", "1", "2", "3"}
		p := New[string]()

		Convey("You can add all the keys to the PList", func() {
			for _, key := range keys {
				p.Add(key)
			}

			Convey("But the PList contains only the unique keys", func() {
				So(p.Len(), ShouldEqual, 3)
				So(p.Values(), ShouldResemble, []string{"2", "1", "3"})
			})

			Convey("Then remove them", func() {
				p.Delete("1")
				So(p.Len(), ShouldEqual, 2)
				So(p.Values(), ShouldResemble, []string{"2", "3"})

				p.Delete("2")
				p.Delete("3")
				So(p.Len(), ShouldEqual, 0)
				So(p.Values(), ShouldResemble, []string{})
			})
		})
	})

	Convey("Given a PList with many items you can paginate over them", t, func() {
		numKeys := 16
		p := New[string]()

		for i := 1; i <= numKeys; i++ {
			p.Add(strconv.Itoa(i))
		}

		So(p.Len(), ShouldEqual, numKeys)

		pageSize := 5
		offset := 0

		vals, remaining := p.Page(offset, pageSize)
		So(vals, ShouldResemble, []string{"1", "2", "3", "4", "5"})
		So(remaining, ShouldEqual, numKeys-pageSize)

		offset += len(vals)
		vals, remaining = p.Page(offset, pageSize)
		So(vals, ShouldResemble, []string{"6", "7", "8", "9", "10"})
		So(remaining, ShouldEqual, numKeys-pageSize*2)

		offset += len(vals)
		vals, remaining = p.Page(offset, pageSize)
		So(vals, ShouldResemble, []string{"11", "12", "13", "14", "15"})
		So(remaining, ShouldEqual, numKeys-pageSize*3)

		offset += len(vals)
		vals, remaining = p.Page(offset, pageSize)
		So(vals, ShouldResemble, []string{"16"})
		So(remaining, ShouldEqual, 0)

		offset += len(vals)
		vals, remaining = p.Page(offset, pageSize)
		So(vals, ShouldResemble, []string{})
		So(remaining, ShouldEqual, 0)
	})

	Convey("Page properly handles edge cases", t, func() {
		p := New[string]()

		for i := 1; i <= 10; i++ {
			p.Add(strconv.Itoa(i))
		}

		Convey("Empty PList returns empty page with no remaining items", func() {
			emptyP := New[string]()
			vals, remaining := emptyP.Page(0, 5)
			So(vals, ShouldResemble, []string{})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Negative offset returns empty page with no remaining items", func() {
			vals, remaining := p.Page(-1, 5)
			So(vals, ShouldResemble, []string{})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Offset greater than length returns empty page with no remaining items", func() {
			vals, remaining := p.Page(20, 5)
			So(vals, ShouldResemble, []string{})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Zero page size returns empty page with no remaining items", func() {
			vals, remaining := p.Page(0, 0)
			So(vals, ShouldResemble, []string{})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Negative page size returns empty page with no remaining items", func() {
			vals, remaining := p.Page(0, -5)
			So(vals, ShouldResemble, []string{})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Page size larger than total elements returns all elements with no remaining items", func() {
			vals, remaining := p.Page(0, 15)
			So(vals, ShouldResemble, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Exact last page returns expected elements with no remaining items", func() {
			vals, remaining := p.Page(5, 5)
			So(vals, ShouldResemble, []string{"6", "7", "8", "9", "10"})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Offset equal to length returns empty page with no remaining items", func() {
			vals, remaining := p.Page(10, 5)
			So(vals, ShouldResemble, []string{})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Partial page at the end returns remaining elements with no remaining items", func() {
			vals, remaining := p.Page(8, 5)
			So(vals, ShouldResemble, []string{"9", "10"})
			So(remaining, ShouldEqual, 0)
		})

		Convey("Single element PList returns that element with no remaining items", func() {
			singleP := New[string]()
			singleP.Add("solo")
			vals, remaining := singleP.Page(0, 5)
			So(vals, ShouldResemble, []string{"solo"})
			So(remaining, ShouldEqual, 0)
		})
	})
}
