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

/*
Package limiter provides a way of limiting the number of something that belongs
to one or more limit groups. It can be used concurrently.

You first create a Limiter and make SetLimit() calls to define the limit of
each group. Then when you want to have something in one or more of those groups,
you call Increment(). If limits have not been reached, it returns true. When
your "something" is done, Decrement().

    import "github.com/VertebrateResequencing/wr/limiter"

    l := limiter.New()
    l.SetLimit("l1", 3)
    l.SetLimit("l2", 2)

    if l.Increment([]string{"l1", "l2"}) { // true
        // do something that can only be done if neither l1 nor l2 have reached
        // their limit, then afterwards:
        l.Decrement([]string{"l1", "l2"})
    }

    l.Increment([]string{"l2"}) // true
    l.Increment([]string{"l2"}) // true
    l.Increment([]string{"l2"}) // false
    l.Increment([]string{"l1", "l2"}) // false
    l.Decrement([]string{"l1", "l2"}) // error, "l1" not incremented yet
    l.Decrement([]string{"l2"})
    l.Increment([]string{"l1", "l2"}) // true

    l.Increment([]string{"l3"}) // true for anything with no SetLimit()
    l.Decrement([]string{"l3"}) // ignored, no error
*/
package limiter
