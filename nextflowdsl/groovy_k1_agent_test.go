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
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestEvalExprK1ConstructorsAgent(t *testing.T) {
	Convey("EvalExpr handles K1 constructor expressions", t, func() {
		Convey("new File absolute path", func() {
			result, err := EvalExpr(NewExpr{ClassName: "File", Args: []Expr{StringExpr{Value: "/data/test.txt"}}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "/data/test.txt")
		})

		Convey("new File relative path", func() {
			result, err := EvalExpr(NewExpr{ClassName: "File", Args: []Expr{StringExpr{Value: "relative/path.txt"}}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "relative/path.txt")
		})

		Convey("new URL string", func() {
			result, err := EvalExpr(NewExpr{ClassName: "URL", Args: []Expr{StringExpr{Value: "https://example.com"}}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "https://example.com")
		})

		Convey("new Date non-empty string", func() {
			result, err := EvalExpr(NewExpr{ClassName: "Date", Args: []Expr{}}, nil)

			So(err, ShouldBeNil)
			value, ok := result.(string)
			So(ok, ShouldBeTrue)
			So(value, ShouldNotBeBlank)
		})

		Convey("new BigDecimal parses float64", func() {
			result, err := EvalExpr(NewExpr{ClassName: "BigDecimal", Args: []Expr{StringExpr{Value: "3.14"}}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 3.14)
		})

		Convey("new BigInteger parses int64", func() {
			result, err := EvalExpr(NewExpr{ClassName: "BigInteger", Args: []Expr{StringExpr{Value: "42"}}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, int64(42))
		})

		Convey("new ArrayList copies slice input", func() {
			original := []any{1, 2, 3}

			result, err := EvalExpr(NewExpr{ClassName: "ArrayList", Args: []Expr{VarExpr{Root: "items"}}}, map[string]any{"items": original})

			So(err, ShouldBeNil)
			values, ok := result.([]any)
			So(ok, ShouldBeTrue)
			So(values, ShouldResemble, []any{1, 2, 3})

			values[0] = 99
			So(original, ShouldResemble, []any{1, 2, 3})
		})

		Convey("new HashMap copies map input", func() {
			original := map[string]any{"a": 1, "b": 2}

			result, err := EvalExpr(NewExpr{ClassName: "HashMap", Args: []Expr{VarExpr{Root: "mapping"}}}, map[string]any{"mapping": original})

			So(err, ShouldBeNil)
			values, ok := result.(map[string]any)
			So(ok, ShouldBeTrue)
			So(values, ShouldResemble, map[string]any{"a": 1, "b": 2})

			values["a"] = 99
			So(original, ShouldResemble, map[string]any{"a": 1, "b": 2})
		})

		Convey("new LinkedHashMap copies map input", func() {
			original := map[string]any{"x": "y"}

			result, err := EvalExpr(NewExpr{ClassName: "LinkedHashMap", Args: []Expr{VarExpr{Root: "mapping"}}}, map[string]any{"mapping": original})

			So(err, ShouldBeNil)
			values, ok := result.(map[string]any)
			So(ok, ShouldBeTrue)
			So(values, ShouldResemble, map[string]any{"x": "y"})

			values["x"] = "z"
			So(original, ShouldResemble, map[string]any{"x": "y"})
		})

		Convey("new Random returns numeric seed", func() {
			result, err := EvalExpr(NewExpr{ClassName: "Random", Args: []Expr{}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, int64(0))
		})

		Convey("unknown constructor remains unsupported", func() {
			result, err := EvalExpr(NewExpr{ClassName: "UnknownClass", Args: []Expr{}}, nil)

			So(err, ShouldBeNil)
			unsupported, ok := result.(UnsupportedExpr)
			So(ok, ShouldBeTrue)
			So(unsupported.Text, ShouldEqual, "new UnknownClass()")
		})
	})
}