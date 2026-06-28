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

package cmd

import (
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestParseRecentDuration(t *testing.T) {
	Convey("parseRecentDuration accepts d and w convenience units", t, func() {
		Convey("1d is 24 hours", func() {
			d, err := parseRecentDuration("1d")
			So(err, ShouldBeNil)
			So(d, ShouldEqual, 24*time.Hour)
		})

		Convey("2w is 14 days", func() {
			d, err := parseRecentDuration("2w")
			So(err, ShouldBeNil)
			So(d, ShouldEqual, 14*24*time.Hour)
		})

		Convey("0.5d is 12 hours", func() {
			d, err := parseRecentDuration("0.5d")
			So(err, ShouldBeNil)
			So(d, ShouldEqual, 12*time.Hour)
		})
	})

	Convey("parseRecentDuration accepts standard Go duration units", t, func() {
		Convey("90m is 90 minutes", func() {
			d, err := parseRecentDuration("90m")
			So(err, ShouldBeNil)
			So(d, ShouldEqual, 90*time.Minute)
		})

		Convey("36h is 36 hours", func() {
			d, err := parseRecentDuration("36h")
			So(err, ShouldBeNil)
			So(d, ShouldEqual, 36*time.Hour)
		})
	})

	Convey("parseRecentDuration rejects bad input", t, func() {
		Convey("an empty string errors mentioning --recent", func() {
			_, err := parseRecentDuration("")
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "--recent")
		})

		Convey("an unparseable value errors", func() {
			_, err := parseRecentDuration("banana")
			So(err, ShouldNotBeNil)
		})

		Convey("a combined unit value errors (single trailing unit only)", func() {
			_, err := parseRecentDuration("1d12h")
			So(err, ShouldNotBeNil)
		})

		Convey("a zero or negative window errors", func() {
			_, err := parseRecentDuration("0s")
			So(err, ShouldNotBeNil)

			_, err = parseRecentDuration("-1h")
			So(err, ShouldNotBeNil)
		})
	})

	Convey("parseRecentDuration errors mention --recent and the units", t, func() {
		for _, s := range []string{"", "banana", "1d12h", "0s", "-1h"} {
			_, err := parseRecentDuration(s)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "--recent")
			So(strings.Contains(err.Error(), "days") &&
				strings.Contains(err.Error(), "weeks"), ShouldBeTrue)
		}
	})
}
