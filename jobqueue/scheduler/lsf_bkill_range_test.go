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

// When bkill is given consecutive elements of one array it can report them as
// one range, e.g. "Job <408347[1-2:1]>: Job has already finished" (seen on
// farm22, LSF 10.1). LSF's documented index list syntax is
// job_ID[index_list], where index_list is a comma-separated list of
// start[-end[:step]] entries, so killSummary.account must credit every element
// such a line covers.

import (
	"errors"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

var errBkillRangeExit = errors.New("exit status 255")

const (
	bkillRangeEl1 = "7[1]"
	bkillRangeEl2 = "7[2]"
)

func TestBkillRangeOutput(t *testing.T) {
	Convey("killSummary.account credits elements bkill reports", t, func() {
		var k killSummary

		Convey("as a range with a step, as prod's LSF did", func() {
			k.account([]string{"408347[1]", "408347[2]"},
				"Job <408347[1-2:1]>: Job has already finished\n", errBkillRangeExit)

			So(k.alreadyGone, ShouldEqual, 2)
			So(k.unaccounted, ShouldEqual, 0)
		})

		Convey("as a range with a step greater than 1, skipping the elements it steps over", func() {
			k.account([]string{bkillRangeEl1, bkillRangeEl2, "7[3]", "7[4]", "7[5]"},
				"Job <7[1-5:2]>: Job has already finished\n", errBkillRangeExit)

			So(k.alreadyGone, ShouldEqual, 3)
			So(k.unaccounted, ShouldEqual, 2)
		})

		Convey("as a range without a step", func() {
			k.account([]string{"7[4]", "7[5]", "7[6]"},
				"Job <7[4-6]> is being terminated\n", errBkillRangeExit)

			So(k.killed, ShouldEqual, 3)
			So(k.unaccounted, ShouldEqual, 0)
		})

		Convey("as a comma list mixing single elements and ranges", func() {
			k.account([]string{bkillRangeEl1, "7[3]", "7[7]", "7[8]", "7[9]", "7[10]"},
				"Job <7[1,3,7-9:1]>: No matching job found\n", errBkillRangeExit)

			So(k.alreadyGone, ShouldEqual, 5)
			So(k.unaccounted, ShouldEqual, 1)
		})

		Convey("as a range wider than the elements asked about, counting only those asked about", func() {
			k.account([]string{"7[500]", "7[501]"},
				"Job <7[1-1000000000:1]>: Job has already finished\n", errBkillRangeExit)

			So(k.alreadyGone, ShouldEqual, 2)
			So(k.unaccounted, ShouldEqual, 0)
		})

		Convey("as a range reaching the largest index, but only for its own job", func() {
			k.account([]string{"7[500]", "17[500]", "70[500]"},
				"Job <7[0-9223372036854775807]>: Job has already finished\n", errBkillRangeExit)

			So(k.alreadyGone, ShouldEqual, 1)
			So(k.unaccounted, ShouldEqual, 2)
		})

		Convey("as single elements and whole jobs, as before", func() {
			k.account([]string{bkillRangeEl1, "8"},
				"Job <7[1]> is being terminated\nJob <8>: Job has already finished\n", errBkillRangeExit)

			So(k.killed, ShouldEqual, 1)
			So(k.alreadyGone, ShouldEqual, 1)
			So(k.unaccounted, ShouldEqual, 0)
		})

		Convey("but not elements of a different job, nor any element twice", func() {
			k.account([]string{bkillRangeEl1, bkillRangeEl2},
				"Job <70[1-2:1]>: Job has already finished\nJob <7[1-2:1]> is being terminated\n"+
					"Job <7[1-2:1]>: Job has already finished\n", errBkillRangeExit)

			So(k.killed, ShouldEqual, 2)
			So(k.alreadyGone, ShouldEqual, 0)
			So(k.unaccounted, ShouldEqual, 0)
		})

		Convey("but not from a malformed index list", func() {
			k.account([]string{bkillRangeEl1, bkillRangeEl2},
				"Job <7[2-1:1]>: Job has already finished\nJob <7[1-2:0]>: Job has already finished\n"+
					"Job <7[a-b]>: Job has already finished\n", errBkillRangeExit)

			So(k.alreadyGone, ShouldEqual, 0)
			So(k.unaccounted, ShouldEqual, 2)
		})
	})
}
