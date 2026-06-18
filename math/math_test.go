/*******************************************************************************
 * Copyright (c) 2020 Genome Research Ltd.
 *
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 ******************************************************************************/

package math

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestMath(t *testing.T) {
	Convey("Round down a floating point number to 3 decimal places", t, func() {
		So(toFixed(123.123456), ShouldEqual, 123.123)
		So(toFixed(234.144444), ShouldEqual, 234.144)
	})

	Convey("Check if a floating point number is smaller than the other", t, func() {
		So(FloatLessThan(123.123456, 123.1245678), ShouldEqual, true)
		So(FloatLessThan(234.144444, 123.123456), ShouldEqual, false)
	})

	Convey("Subtract a floating point number from the other", t, func() {
		So(FloatSubtract(234.144444, 123.123456), ShouldEqual, 111.021)
		So(FloatSubtract(123.123456, 234.144444), ShouldEqual, -111.021)
	})

	Convey("Add a floating point number to other", t, func() {
		So(FloatAdd(234.144444, 123.123456), ShouldEqual, 357.267)
	})
}
