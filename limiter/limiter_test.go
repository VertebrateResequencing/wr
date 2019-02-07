// Copyright © 2019 Genome Research Limited
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

package limiter

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestLimiter(t *testing.T) {
	Convey("You can make a new Limiter with a limit defining callback", t, func() {
		limits := make(map[string]uint)
		limits["l1"] = 3
		limits["l2"] = 2
		cb := func(name string) uint {
			if limit, exists := limits[name]; exists {
				return limit
			}
			return 0
		}

		l := New(cb)
		So(l, ShouldNotBeNil)

		Convey("Increment and Decrement work as expected", func() {
			So(l.Increment([]string{"l1", "l2"}), ShouldBeTrue)
			l.Decrement([]string{"l1", "l2"})

			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeFalse)
			So(l.Increment([]string{"l1", "l2"}), ShouldBeFalse)
			l.Decrement([]string{"l1", "l2"})
			So(l.Increment([]string{"l1", "l2"}), ShouldBeTrue)
			l.Decrement([]string{"l2"})
			So(l.Increment([]string{"l1", "l2"}), ShouldBeTrue)

			So(l.Increment([]string{"l3"}), ShouldBeTrue)
			l.Decrement([]string{"l3"})
		})

		Convey("You can change limits with SetLimit(), and Decrement() forgets about unused groups", func() {
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeFalse)
			l.SetLimit("l2", 3)
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeFalse)
			l.Decrement([]string{"l2"})
			l.Decrement([]string{"l2"})
			l.Decrement([]string{"l2"})
			// at this point l2 should have been forgotten about, which means
			// we forgot we set the limit to 3
			l.Decrement([]string{"l2"}) // doesn't panic or something
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeFalse)
			l.Decrement([]string{"l2"})
			l.Decrement([]string{"l2"})
			limits["l2"] = 3
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeFalse)
		})
	})
}
