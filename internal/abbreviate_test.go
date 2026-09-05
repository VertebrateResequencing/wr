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

package internal

import (
	"strconv"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// abbreviateTestOverhead is a generous allowance for the fixed "[...]
// (truncated; N bytes total)" suffix Abbreviate appends, used to bound the
// result without pinning the exact wording.
const abbreviateTestOverhead = 64

func TestAbbreviate(t *testing.T) {
	Convey("Abbreviate leaves a short string alone", t, func() {
		So(Abbreviate(""), ShouldEqual, "")
		So(Abbreviate("echo foo"), ShouldEqual, "echo foo")

		exact := strings.Repeat("x", AbbreviateMax)
		So(Abbreviate(exact), ShouldEqual, exact)
		So(len(Abbreviate(exact)), ShouldEqual, AbbreviateMax)
	})

	Convey("Abbreviate bounds a huge string and reports its real length", t, func() {
		huge := strings.Repeat("x", AbbreviateMax*1000)

		got := Abbreviate(huge)

		// the bound is the point: a 1.3MB command line must not reach a log line.
		So(len(got), ShouldBeLessThan, AbbreviateMax+abbreviateTestOverhead)
		So(got, ShouldStartWith, huge[:AbbreviateMax])
		So(got, ShouldContainSubstring, strconv.Itoa(len(huge)))

		Convey("and the input is not modified", func() {
			So(len(huge), ShouldEqual, AbbreviateMax*1000)
			So(huge, ShouldEqual, strings.Repeat("x", AbbreviateMax*1000))
		})
	})

	Convey("Abbreviate is one byte past the limit at the boundary", t, func() {
		overByOne := strings.Repeat("y", AbbreviateMax+1)

		got := Abbreviate(overByOne)

		So(got, ShouldNotEqual, overByOne)
		So(got, ShouldContainSubstring, strconv.Itoa(AbbreviateMax+1))
	})
}
