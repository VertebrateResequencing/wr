/*******************************************************************************
 * Copyright (c) 2019, 2024 Genome Research Ltd.
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
