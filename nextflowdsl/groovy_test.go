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

func TestEvalExpr(t *testing.T) {
	Convey("EvalExpr handles B3 expressions", t, func() {
		Convey("integer literals evaluate to ints", func() {
			result, err := EvalExpr(IntExpr{Value: 4}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 4)
		})

		Convey("params references resolve from vars", func() {
			result, err := EvalExpr(ParamsExpr{Path: "cpus"}, map[string]any{
				"params": map[string]any{"cpus": 8},
			})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 8)
		})

		Convey("double-quoted strings interpolate variables", func() {
			result, err := EvalExpr(StringExpr{Value: "hello ${name}"}, map[string]any{
				"name": "world",
			})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "hello world")
		})

		Convey("basic arithmetic evaluates integer expressions", func() {
			result, err := EvalExpr(BinaryExpr{
				Left:  IntExpr{Value: 2},
				Op:    "+",
				Right: IntExpr{Value: 3},
			}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 5)
		})

		Convey("task references participate in arithmetic", func() {
			result, err := EvalExpr(BinaryExpr{
				Left:  VarExpr{Root: "task", Path: "cpus"},
				Op:    "*",
				Right: IntExpr{Value: 2},
			}, map[string]any{
				"task": map[string]any{"cpus": 4},
			})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 8)
		})

		Convey("complex closures return an unsupported-expression error", func() {
			_, err := EvalExpr(UnsupportedExpr{Text: "task.input.size() < 10 ? 1 : 4"}, nil)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unsupported expression")
		})

		Convey("unsupported-expression errors include the original text", func() {
			_, err := EvalExpr(UnsupportedExpr{Text: "task.input.size() < 10 ? 1 : 4"}, nil)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "task.input.size() < 10 ? 1 : 4")
		})

		Convey("parsed directive arithmetic expressions evaluate end to end", func() {
			wf, err := Parse(strings.NewReader("process foo {\ncpus 2 + 3\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)

			result, evalErr := EvalExpr(wf.Processes[0].Directives["cpus"], nil)

			So(evalErr, ShouldBeNil)
			So(result, ShouldEqual, 5)
		})

		Convey("parsed directive task references evaluate end to end", func() {
			wf, err := Parse(strings.NewReader("process foo {\ncpus task.cpus * 2\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)

			result, evalErr := EvalExpr(wf.Processes[0].Directives["cpus"], map[string]any{
				"task": map[string]any{"cpus": 4},
			})

			So(evalErr, ShouldBeNil)
			So(result, ShouldEqual, 8)
		})
	})
}
