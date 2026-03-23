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

func TestEvalExprM4NumberMethods(t *testing.T) {
	Convey("EvalExpr handles M4 number methods", t, func() {
		cases := []struct {
			name     string
			receiver any
			method   string
			args     []Expr
			expected any
		}{
			{name: "abs returns positive value for negative ints", receiver: -5, method: "abs", expected: 5},
			{name: "round rounds 3.7 up", receiver: float64(3.7), method: "round", expected: 4},
			{name: "round rounds 3.2 down", receiver: float64(3.2), method: "round", expected: 3},
			{
				name:     "intdiv performs integer division",
				receiver: 7,
				method:   "intdiv",
				args:     []Expr{IntExpr{Value: 2}},
				expected: 3,
			},
			{name: "toInteger truncates floats", receiver: float64(3.14), method: "toInteger", expected: 3},
			{name: "toDouble converts ints to float64", receiver: 42, method: "toDouble", expected: float64(42.0)},
			{name: "abs preserves zero", receiver: 0, method: "abs", expected: 0},
			{name: "toLong converts int64 receivers", receiver: int64(42), method: "toLong", expected: int64(42)},
			{
				name:     "toBigDecimal converts float64 receivers",
				receiver: float64(3.14),
				method:   "toBigDecimal",
				expected: float64(3.14),
			},
		}

		for _, testCase := range cases {
			result, err := EvalExpr(MethodCallExpr{
				Receiver: VarExpr{Root: "value"},
				Method:   testCase.method,
				Args:     testCase.args,
			}, map[string]any{"value": testCase.receiver})

			So(err, ShouldBeNil)
			So(result, ShouldResemble, testCase.expected)
		}
	})
}
