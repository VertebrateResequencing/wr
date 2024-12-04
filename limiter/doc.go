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

You first create a Limiter with a callback that provides the limit of each
group. Then when you want to have something in one or more of those groups, you
call Increment(). If limits have not been reached, it returns true. When your
"something" is done, Decrement().

Your callback is only called once per group while that group is in use: the
limit you provide is stored in memory. But Decrement() removes groups from
memory when the count becomes zero, so that unused groups don't fill up memory.
If a subsequent Increment() uses a group that was removed from memory, your
callback will be called again to find out the limit. It is intended that you
don't store all your limits in memory yourself, but retrieve them from disk.
If you need to change the limit of a group, your callback should start returning
the new limit, and you should call SetLimit() to change the memorised limit, if
any.

	import "github.com/VertebrateResequencing/wr/limiter"

	cb := func(name string) int {
	    if name == "l1" {
	        return 3
	    } else if name == "l2" {
	        return 2
	    }
	    return 0
	}

	l := limiter.New(cb)

	if l.Increment([]string{"l1", "l2"}) { // true
	    // do something that can only be done if neither l1 nor l2 have reached
	    // their limit, then afterwards:
	    l.Decrement([]string{"l1", "l2"})
	}

	l.Increment([]string{"l2"}) // true
	l.Increment([]string{"l2"}) // true
	l.Increment([]string{"l2"}) // false
	l.Increment([]string{"l1", "l2"}) // false
	l.Decrement([]string{"l1", "l2"}) // l1 ignored since never incremented
	l.Increment([]string{"l1", "l2"}) // true

	l.Increment([]string{"l3"}) // true since callback returns 0
	l.Decrement([]string{"l3"}) // ignored
*/
package limiter
