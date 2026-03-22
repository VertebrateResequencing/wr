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

package nextflowdsl

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestParseE1SkippableStatements(t *testing.T) {
	Convey("Parse handles E1 skippable statement types", t, func() {
		Convey("functions with assert and return parse without error", func() {
			wf, err := Parse(strings.NewReader("def check(x) { assert x > 0 : 'must be positive'\nreturn x }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Body, ShouldContainSubstring, "assert x > 0")
			So(wf.Functions[0].Body, ShouldContainSubstring, "return x")
		})

		Convey("functions with try-catch parse without error", func() {
			wf, err := Parse(strings.NewReader("def safe(x) { try { risky(x) } catch (Exception e) { log.warn(e) } }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Body, ShouldContainSubstring, "try")
			So(wf.Functions[0].Body, ShouldContainSubstring, "catch (Exception e)")
		})

		Convey("functions with for-in loops parse without error", func() {
			wf, err := Parse(strings.NewReader("def loop(items) { for (x in items) { println x } }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Body, ShouldContainSubstring, "for (x in items)")
		})

		Convey("functions with while loops parse without error", func() {
			wf, err := Parse(strings.NewReader("def wait(n) { while (n > 0) { n-- } }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Body, ShouldContainSubstring, "while (n > 0)")
		})

		Convey("functions with switch statements parse without error", func() {
			wf, err := Parse(strings.NewReader("def label(x) { switch (x) { case 1: 'one'; break; case 2: 'two'; break; default: 'other' } }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Body, ShouldContainSubstring, "switch (x)")
			So(wf.Functions[0].Body, ShouldContainSubstring, "default: 'other'")
		})

		Convey("functions with throw statements parse without error", func() {
			wf, err := Parse(strings.NewReader("def fail() { throw new RuntimeException('fail') }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Body, ShouldContainSubstring, "throw new RuntimeException('fail')")
		})

		Convey("functions with bare returns parse without error", func() {
			wf, err := Parse(strings.NewReader("def answer() { return 42 }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Body, ShouldContainSubstring, "return 42")
		})

		Convey("process scripts containing for-in loops keep raw script text", func() {
			wf, err := Parse(strings.NewReader("process foo {\nscript:\n\"\"\"\nfor (x in items) {\n    println x\n}\n\"\"\"\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Script, ShouldContainSubstring, "for (x in items) {")
			So(wf.Processes[0].Script, ShouldContainSubstring, "println x")
		})

		Convey("closures with inline returns parse without error", func() {
			expr, err := parseE1TestExpr("{ item -> if (item == null) return null; item.trim() }")

			So(err, ShouldBeNil)
			closure, ok := expr.(ClosureExpr)
			So(ok, ShouldBeTrue)
			So(closure.Params, ShouldResemble, []string{"item"})
			So(closure.Body, ShouldContainSubstring, "if (item == null) return null")
			So(closure.Body, ShouldContainSubstring, "item.trim()")
		})
	})
}

func parseE1TestExpr(input string) (Expr, error) {
	tokens, err := lex(input)
	if err != nil {
		return nil, err
	}

	return parseExprTokens(tokens[:len(tokens)-1])
}
