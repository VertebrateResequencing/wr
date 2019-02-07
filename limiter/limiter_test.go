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
	Convey("You can make a new Limiter with some limits set", t, func() {
		l := New()
		So(l, ShouldNotBeNil)

		l.SetLimit("l1", 3)
		l.SetLimit("l2", 2)

		Convey("Increment and Decrement work as expected", func() {
			So(l.Increment([]string{"l1", "l2"}), ShouldBeTrue)
			So(l.Decrement([]string{"l1", "l2"}), ShouldBeNil)

			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeTrue)
			So(l.Increment([]string{"l2"}), ShouldBeFalse)
			So(l.Increment([]string{"l1", "l2"}), ShouldBeFalse)
			err := l.Decrement([]string{"l1", "l2"})
			So(err, ShouldNotBeNil)
			lerr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(lerr.Err, ShouldEqual, ErrNotIncremented)
			So(l.Decrement([]string{"l2"}), ShouldBeNil)
			So(l.Increment([]string{"l1", "l2"}), ShouldBeTrue)

			So(l.Increment([]string{"l3"}), ShouldBeTrue)
			So(l.Decrement([]string{"l3"}), ShouldBeNil)
		})
	})
}
