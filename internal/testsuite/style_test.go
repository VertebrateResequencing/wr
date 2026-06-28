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

package testsuite

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestStyle(t *testing.T) {
	Convey("A rich style wraps text in the expected ANSI codes", t, func() {
		sty := newStyle(true)

		So(sty.green("ok"), ShouldEqual, ansiGreen+"ok"+ansiReset)
		So(sty.yellow("ok"), ShouldEqual, ansiYellow+"ok"+ansiReset)
		So(sty.red("ok"), ShouldEqual, ansiRed+"ok"+ansiReset)
		So(sty.cyan("ok"), ShouldEqual, ansiCyan+"ok"+ansiReset)
		So(sty.bold("ok"), ShouldEqual, ansiBold+"ok"+ansiReset)
		So(sty.dim("ok"), ShouldEqual, ansiDim+"ok"+ansiReset)
		So(sty.boldGreen("ok"), ShouldEqual, ansiBold+ansiGreen+"ok"+ansiReset)
		So(sty.boldRed("ok"), ShouldEqual, ansiBold+ansiRed+"ok"+ansiReset)
	})

	Convey("A plain style returns text unchanged", t, func() {
		sty := newStyle(false)

		So(sty.green("ok"), ShouldEqual, "ok")
		So(sty.yellow("ok"), ShouldEqual, "ok")
		So(sty.red("ok"), ShouldEqual, "ok")
		So(sty.cyan("ok"), ShouldEqual, "ok")
		So(sty.bold("ok"), ShouldEqual, "ok")
		So(sty.dim("ok"), ShouldEqual, "ok")
		So(sty.boldGreen("ok"), ShouldEqual, "ok")
		So(sty.boldRed("ok"), ShouldEqual, "ok")
	})

	Convey("Glyphs are Unicode when rich and ASCII when plain", t, func() {
		rich := newStyle(true)
		plain := newStyle(false)

		So(rich.pass(), ShouldEqual, "✓")
		So(plain.pass(), ShouldEqual, "")

		So(rich.skip(), ShouldEqual, "◦")
		So(plain.skip(), ShouldEqual, "")

		So(rich.skipArrow(), ShouldEqual, "↳")
		So(plain.skipArrow(), ShouldEqual, "-")

		So(rich.bullet(), ShouldEqual, "·")
		So(plain.bullet(), ShouldEqual, "·")

		So(rich.rule(), ShouldEqual, "─")
		So(plain.rule(), ShouldEqual, "-")

		So(rich.fail(), ShouldEqual, "✗")
		So(plain.fail(), ShouldEqual, "")

		So(rich.times(), ShouldEqual, "×")
		So(plain.times(), ShouldEqual, "x")
	})
}
