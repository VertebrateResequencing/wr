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

		Convey("list literals evaluate to []any", func() {
			result, err := EvalExpr(ListExpr{Elements: []Expr{
				IntExpr{Value: 1},
				IntExpr{Value: 2},
				IntExpr{Value: 3},
			}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{1, 2, 3})
		})

		Convey("string list literals evaluate to []any", func() {
			result, err := EvalExpr(ListExpr{Elements: []Expr{
				StringExpr{Value: "a"},
				StringExpr{Value: "b"},
			}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{"a", "b"})
		})

		Convey("map literals evaluate to map[string]any", func() {
			result, err := EvalExpr(MapExpr{
				Keys:   []Expr{StringExpr{Value: "key"}},
				Values: []Expr{StringExpr{Value: "value"}},
			}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, map[string]any{"key": "value"})
		})

		Convey("multi-entry map literals evaluate to map[string]any", func() {
			result, err := EvalExpr(MapExpr{
				Keys:   []Expr{StringExpr{Value: "a"}, StringExpr{Value: "b"}},
				Values: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}},
			}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, map[string]any{"a": 1, "b": 2})
		})

		Convey("empty list literals evaluate to empty []any", func() {
			result, err := EvalExpr(ListExpr{}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{})
		})

		Convey("empty map literals evaluate to empty map[string]any", func() {
			result, err := EvalExpr(MapExpr{}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, map[string]any{})
		})

		Convey("list subscript access resolves indexed values", func() {
			result, err := EvalExpr(IndexExpr{
				Receiver: VarExpr{Root: "list"},
				Index:    IntExpr{Value: 0},
			}, map[string]any{"list": []any{10, 20, 30}})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 10)
		})

		Convey("map subscript access resolves keyed values", func() {
			result, err := EvalExpr(IndexExpr{
				Receiver: VarExpr{Root: "map"},
				Index:    StringExpr{Value: "key"},
			}, map[string]any{"map": map[string]any{"key": "val"}})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "val")
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

func TestEvalExprD2ComparisonAndLogicalOperators(t *testing.T) {
	Convey("EvalExpr handles D2 comparison and logical operators", t, func() {
		Convey("comparison operators evaluate integers and strings", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "1 == 1", expr: BinaryExpr{Left: IntExpr{Value: 1}, Op: "==", Right: IntExpr{Value: 1}}, expected: true},
				{name: "1 != 2", expr: BinaryExpr{Left: IntExpr{Value: 1}, Op: "!=", Right: IntExpr{Value: 2}}, expected: true},
				{name: "3 >= 3", expr: BinaryExpr{Left: IntExpr{Value: 3}, Op: ">=", Right: IntExpr{Value: 3}}, expected: true},
				{name: "2 <= 3", expr: BinaryExpr{Left: IntExpr{Value: 2}, Op: "<=", Right: IntExpr{Value: 3}}, expected: true},
				{name: "'hello' == 'hello'", expr: BinaryExpr{Left: StringExpr{Value: "hello"}, Op: "==", Right: StringExpr{Value: "hello"}}, expected: true},
				{name: "'a' != 'b'", expr: BinaryExpr{Left: StringExpr{Value: "a"}, Op: "!=", Right: StringExpr{Value: "b"}}, expected: true},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})

		Convey("logical operators and unary negation evaluate booleans", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "true && false", expr: BinaryExpr{Left: BoolExpr{Value: true}, Op: "&&", Right: BoolExpr{Value: false}}, expected: false},
				{name: "true || false", expr: BinaryExpr{Left: BoolExpr{Value: true}, Op: "||", Right: BoolExpr{Value: false}}, expected: true},
				{name: "!true", expr: UnaryExpr{Op: "!", Operand: BoolExpr{Value: true}}, expected: false},
				{name: "!false", expr: UnaryExpr{Op: "!", Operand: BoolExpr{Value: false}}, expected: true},
				{name: "1 == 1 && 2 > 1", expr: BinaryExpr{
					Left:  BinaryExpr{Left: IntExpr{Value: 1}, Op: "==", Right: IntExpr{Value: 1}},
					Op:    "&&",
					Right: BinaryExpr{Left: IntExpr{Value: 2}, Op: ">", Right: IntExpr{Value: 1}},
				}, expected: true},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})
	})
}

func TestEvalExprD1TernaryAndElvisOperators(t *testing.T) {
	Convey("EvalExpr handles D1 ternary and elvis operators", t, func() {
		Convey("ternary expressions evaluate the matching branch", func() {
			cases := []struct {
				name     string
				vars     map[string]any
				expected any
			}{
				{name: "attempt one chooses false branch", vars: map[string]any{"task": map[string]any{"attempt": 1}}, expected: "8 GB"},
				{name: "attempt two chooses true branch", vars: map[string]any{"task": map[string]any{"attempt": 2}}, expected: "16 GB"},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(TernaryExpr{
					Cond:  BinaryExpr{Left: VarExpr{Root: "task", Path: "attempt"}, Op: ">", Right: IntExpr{Value: 1}},
					True:  StringExpr{Value: "16 GB"},
					False: StringExpr{Value: "8 GB"},
				}, testCase.vars)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})

		Convey("elvis expressions use Groovy truthiness for fallback", func() {
			cases := []struct {
				name     string
				vars     map[string]any
				expected any
			}{
				{name: "non-empty string stays", vars: map[string]any{"x": "hello"}, expected: "hello"},
				{name: "nil falls back", vars: map[string]any{"x": nil}, expected: "default"},
				{name: "empty string falls back", vars: map[string]any{"x": ""}, expected: "default"},
				{name: "zero falls back", vars: map[string]any{"x": 0}, expected: "default"},
				{name: "false falls back", vars: map[string]any{"x": false}, expected: "default"},
				{name: "empty list falls back", vars: map[string]any{"x": []any{}}, expected: "default"},
				{name: "empty map falls back", vars: map[string]any{"x": map[string]any{}}, expected: "default"},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(TernaryExpr{
					True:  VarExpr{Root: "x"},
					False: StringExpr{Value: "default"},
				}, testCase.vars)

				So(err, ShouldBeNil)
				So(result, ShouldResemble, testCase.expected)
			}
		})
	})
}

func TestEvalExprD6CastExpressions(t *testing.T) {
	Convey("EvalExpr handles D6 cast expressions", t, func() {
		Convey("string literals cast to Integer", func() {
			result, err := EvalExpr(CastExpr{
				Operand:  StringExpr{Value: "42"},
				TypeName: "Integer",
			}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 42)
		})

		Convey("integers cast to String", func() {
			result, err := EvalExpr(CastExpr{
				Operand:  IntExpr{Value: 42},
				TypeName: "String",
			}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "42")
		})

		Convey("variables cast to Integer", func() {
			result, err := EvalExpr(CastExpr{
				Operand:  VarExpr{Root: "x"},
				TypeName: "Integer",
			}, map[string]any{"x": "10"})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 10)
		})

		Convey("unsupported cast targets evaluate to UnsupportedExpr", func() {
			result, err := EvalExpr(CastExpr{
				Operand:  StringExpr{Value: "42"},
				TypeName: "Duration",
			}, nil)

			So(err, ShouldBeNil)
			unsupported, ok := result.(UnsupportedExpr)
			So(ok, ShouldBeTrue)
			So(unsupported.Text, ShouldContainSubstring, "as Duration")
		})
	})
}

func TestEvalExprD5NullAndTaskReferences(t *testing.T) {
	Convey("EvalExpr handles D5 null literal and task references", t, func() {
		Convey("null literals evaluate to nil", func() {
			result, err := EvalExpr(NullExpr{}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldBeNil)
		})

		Convey("null equality evaluates true", func() {
			result, err := EvalExpr(BinaryExpr{Left: NullExpr{}, Op: "==", Right: NullExpr{}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, true)
		})

		Convey("task.attempt resolves from vars", func() {
			result, err := EvalExpr(VarExpr{Root: "task", Path: "attempt"}, map[string]any{
				"task": map[string]any{"attempt": 1},
			})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 1)
		})

		Convey("task.attempt participates in arithmetic", func() {
			result, err := EvalExpr(BinaryExpr{
				Left:  VarExpr{Root: "task", Path: "attempt"},
				Op:    "*",
				Right: IntExpr{Value: 2},
			}, map[string]any{
				"task": map[string]any{"attempt": 3},
			})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 6)
		})

		Convey("null-safe property access returns nil for nil receivers", func() {
			result, err := EvalExpr(NullSafeExpr{Receiver: VarExpr{Root: "x"}, Property: "property"}, map[string]any{
				"x": nil,
			})

			So(err, ShouldBeNil)
			So(result, ShouldBeNil)
		})

		Convey("null-safe property access resolves properties on present receivers", func() {
			result, err := EvalExpr(NullSafeExpr{Receiver: VarExpr{Root: "x"}, Property: "property"}, map[string]any{
				"x": map[string]any{"property": "val"},
			})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "val")
		})
	})
}

func TestEvalExprD3MethodCalls(t *testing.T) {
	Convey("EvalExpr handles D3 method calls on strings and lists", t, func() {
		Convey("string methods evaluate to the expected values", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "trim", expr: MethodCallExpr{Receiver: StringExpr{Value: "  hello  "}, Method: "trim", Args: []Expr{}}, expected: "hello"},
				{name: "size", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "size", Args: []Expr{}}, expected: 5},
				{name: "toInteger", expr: MethodCallExpr{Receiver: StringExpr{Value: "42"}, Method: "toInteger", Args: []Expr{}}, expected: 42},
				{name: "toLowerCase", expr: MethodCallExpr{Receiver: StringExpr{Value: "Hello"}, Method: "toLowerCase", Args: []Expr{}}, expected: "hello"},
				{name: "toUpperCase", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "toUpperCase", Args: []Expr{}}, expected: "HELLO"},
				{name: "contains", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello world"}, Method: "contains", Args: []Expr{StringExpr{Value: "world"}}}, expected: true},
				{name: "startsWith", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "startsWith", Args: []Expr{StringExpr{Value: "hel"}}}, expected: true},
				{name: "endsWith", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "endsWith", Args: []Expr{StringExpr{Value: "llo"}}}, expected: true},
				{name: "replace", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "replace", Args: []Expr{StringExpr{Value: "l"}, StringExpr{Value: "r"}}}, expected: "herro"},
				{name: "split", expr: MethodCallExpr{Receiver: StringExpr{Value: "a,b,c"}, Method: "split", Args: []Expr{StringExpr{Value: ","}}}, expected: []any{"a", "b", "c"}},
				{name: "substring start", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "substring", Args: []Expr{IntExpr{Value: 1}}}, expected: "ello"},
				{name: "substring start end", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "substring", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 3}}}, expected: "el"},
				{name: "method chaining", expr: MethodCallExpr{Receiver: MethodCallExpr{Receiver: StringExpr{Value: "  hello  "}, Method: "trim", Args: []Expr{}}, Method: "toUpperCase", Args: []Expr{}}, expected: "HELLO"},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldResemble, testCase.expected)
			}
		})

		Convey("list methods evaluate to the expected values", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "size", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}, Method: "size", Args: []Expr{}}, expected: 3},
				{name: "isEmpty", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{}}, Method: "isEmpty", Args: []Expr{}}, expected: true},
				{name: "first", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}, Method: "first", Args: []Expr{}}, expected: 1},
				{name: "last", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}, Method: "last", Args: []Expr{}}, expected: 3},
				{name: "flatten", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{
					ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}}},
					ListExpr{Elements: []Expr{IntExpr{Value: 3}, ListExpr{Elements: []Expr{IntExpr{Value: 4}, IntExpr{Value: 5}}}}},
				}}, Method: "flatten", Args: []Expr{}}, expected: []any{1, 2, 3, 4, 5}},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldResemble, testCase.expected)
			}
		})

		Convey("collect applies simple closures to each list item", func() {
			result, err := EvalExpr(MethodCallExpr{
				Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
				Method:   "collect",
				Args:     []Expr{ClosureExpr{Body: "it * 2"}},
			}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{2, 4, 6})
		})

		Convey("parsed trailing-closure collect calls evaluate end to end", func() {
			expr, err := parseTestExpr("[1, 2, 3].collect { it * 2 }")

			So(err, ShouldBeNil)

			result, err := EvalExpr(expr, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{2, 4, 6})
		})
	})
}
