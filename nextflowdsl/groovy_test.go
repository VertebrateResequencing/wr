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

func TestEvalExprG1EnumValues(t *testing.T) {
	Convey("EvalExpr handles G1 enum values", t, func() {
		wf, err := Parse(strings.NewReader("enum Day { MONDAY, TUESDAY, WEDNESDAY }"))

		So(err, ShouldBeNil)

		result, evalErr := EvalExpr(VarExpr{Root: "Day", Path: "MONDAY"}, bindWorkflowEnumValues(nil, wf))

		So(evalErr, ShouldBeNil)
		So(result, ShouldEqual, "MONDAY")
	})
}

func TestEvalExprNewExpr(t *testing.T) {
	Convey("EvalExpr handles constructor expressions used by translation", t, func() {
		Convey("File constructors evaluate to cleaned path strings", func() {
			result, err := EvalExpr(NewExpr{ClassName: "File", Args: []Expr{StringExpr{Value: "test.txt"}}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "test.txt")

			result, err = EvalExpr(NewExpr{ClassName: "java.io.File", Args: []Expr{StringExpr{Value: "/tmp"}, StringExpr{Value: "out.txt"}}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "/tmp/out.txt")
		})

		Convey("unsupported constructors return an unsupported expression for caller fallback", func() {
			result, err := EvalExpr(NewExpr{ClassName: "Date", Args: []Expr{}}, nil)

			So(err, ShouldBeNil)
			unsupported, ok := result.(UnsupportedExpr)
			So(ok, ShouldBeTrue)
			So(unsupported.Text, ShouldEqual, "new Date()")
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

		Convey("unary minus negates integer operands", func() {
			result, err := EvalExpr(UnaryExpr{Op: "-", Operand: IntExpr{Value: 7}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, -7)
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

func TestEvalExprD3CommonOperators(t *testing.T) {
	Convey("EvalExpr handles D3 common operators", t, func() {
		Convey("modulus and exponentiation evaluate integer expressions", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "10 % 3", expr: BinaryExpr{Left: IntExpr{Value: 10}, Op: "%", Right: IntExpr{Value: 3}}, expected: 1},
				{name: "2 ** 10", expr: BinaryExpr{Left: IntExpr{Value: 2}, Op: "**", Right: IntExpr{Value: 10}}, expected: 1024},
				{name: "2 ** 0", expr: BinaryExpr{Left: IntExpr{Value: 2}, Op: "**", Right: IntExpr{Value: 0}}, expected: 1},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})

		Convey("modulus by zero returns an error", func() {
			_, err := EvalExpr(BinaryExpr{Left: IntExpr{Value: 10}, Op: "%", Right: IntExpr{Value: 0}}, nil)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "division by zero")
		})

		Convey("membership operators evaluate against lists", func() {
			cases := []struct {
				name     string
				expr     Expr
				vars     map[string]any
				expected any
			}{
				{name: "a in list", expr: InExpr{Left: StringExpr{Value: "a"}, Right: VarExpr{Root: "letters"}}, vars: map[string]any{"letters": []any{"a", "b", "c"}}, expected: true},
				{name: "d in list", expr: InExpr{Left: StringExpr{Value: "d"}, Right: VarExpr{Root: "letters"}}, vars: map[string]any{"letters": []any{"a", "b", "c"}}, expected: false},
				{name: "x !in list", expr: InExpr{Left: StringExpr{Value: "x"}, Right: VarExpr{Root: "letters"}, Negated: true}, vars: map[string]any{"letters": []any{"a", "b"}}, expected: true},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, testCase.vars)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})

		Convey("regex operators evaluate search and full matches", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "find digits", expr: RegexExpr{Left: StringExpr{Value: "hello123"}, Right: SlashyStringExpr{Value: "[0-9]+"}}, expected: true},
				{name: "find anchored digits fails", expr: RegexExpr{Left: StringExpr{Value: "hello"}, Right: SlashyStringExpr{Value: "^[0-9]+$"}}, expected: false},
				{name: "full digits match", expr: RegexExpr{Left: StringExpr{Value: "12345"}, Right: SlashyStringExpr{Value: "^[0-9]+$"}, Full: true}, expected: true},
				{name: "full digits mismatch", expr: RegexExpr{Left: StringExpr{Value: "abc123"}, Right: SlashyStringExpr{Value: "^[0-9]+$"}, Full: true}, expected: false},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})

		Convey("range expressions evaluate to integer lists", func() {
			inclusive, err := EvalExpr(RangeExpr{Start: IntExpr{Value: 1}, End: IntExpr{Value: 5}}, nil)

			So(err, ShouldBeNil)
			So(inclusive, ShouldResemble, []any{1, 2, 3, 4, 5})

			exclusive, err := EvalExpr(RangeExpr{Start: IntExpr{Value: 0}, End: IntExpr{Value: 3}, Exclusive: true}, nil)

			So(err, ShouldBeNil)
			So(exclusive, ShouldResemble, []any{0, 1, 2})
		})

		Convey("spread-dot resolves properties across list items", func() {
			result, err := EvalExpr(SpreadExpr{Receiver: VarExpr{Root: "items"}, Property: "name"}, map[string]any{
				"items": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}},
			})

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{"a", "b"})
		})

		Convey("bitwise and shift operators evaluate integer expressions", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "and", expr: BinaryExpr{Left: IntExpr{Value: 0xFF}, Op: "&", Right: IntExpr{Value: 0x0F}}, expected: 15},
				{name: "xor", expr: BinaryExpr{Left: IntExpr{Value: 0xFF}, Op: "^", Right: IntExpr{Value: 0x0F}}, expected: 240},
				{name: "or", expr: BinaryExpr{Left: IntExpr{Value: 0x01}, Op: "|", Right: IntExpr{Value: 0x10}}, expected: 17},
				{name: "bitwise not", expr: UnaryExpr{Op: "~", Operand: IntExpr{Value: 0}}, expected: -1},
				{name: "left shift", expr: BinaryExpr{Left: IntExpr{Value: 1}, Op: "<<", Right: IntExpr{Value: 4}}, expected: 16},
				{name: "right shift", expr: BinaryExpr{Left: IntExpr{Value: 256}, Op: ">>", Right: IntExpr{Value: 2}}, expected: 64},
				{name: "unsigned right shift", expr: BinaryExpr{Left: IntExpr{Value: -1}, Op: ">>>", Right: IntExpr{Value: 24}}, expected: 255},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})

		Convey("spaceship operator returns ordering values", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "3 compared to 5", expr: BinaryExpr{Left: IntExpr{Value: 3}, Op: "<=>", Right: IntExpr{Value: 5}}, expected: -1},
				{name: "5 compared to 5", expr: BinaryExpr{Left: IntExpr{Value: 5}, Op: "<=>", Right: IntExpr{Value: 5}}, expected: 0},
				{name: "7 compared to 2", expr: BinaryExpr{Left: IntExpr{Value: 7}, Op: "<=>", Right: IntExpr{Value: 2}}, expected: 1},
				{name: "abc compared to def", expr: BinaryExpr{Left: StringExpr{Value: "abc"}, Op: "<=>", Right: StringExpr{Value: "def"}}, expected: -1},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
			}
		})

		Convey("instanceof operators map Groovy names to Go runtime types", func() {
			cases := []struct {
				name     string
				expr     Expr
				expected any
			}{
				{name: "hello instanceof String", expr: BinaryExpr{Left: StringExpr{Value: "hello"}, Op: "instanceof", Right: VarExpr{Root: "String"}}, expected: true},
				{name: "42 instanceof Integer", expr: BinaryExpr{Left: IntExpr{Value: 42}, Op: "instanceof", Right: VarExpr{Root: "Integer"}}, expected: true},
				{name: "list instanceof List", expr: BinaryExpr{Left: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}}}, Op: "instanceof", Right: VarExpr{Root: "List"}}, expected: true},
				{name: "hello instanceof Integer", expr: BinaryExpr{Left: StringExpr{Value: "hello"}, Op: "instanceof", Right: VarExpr{Root: "Integer"}}, expected: false},
				{name: "42 !instanceof String", expr: BinaryExpr{Left: IntExpr{Value: 42}, Op: "!instanceof", Right: VarExpr{Root: "String"}}, expected: true},
			}

			for _, testCase := range cases {
				result, err := EvalExpr(testCase.expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldEqual, testCase.expected)
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
				{name: "replaceAll", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello123"}, Method: "replaceAll", Args: []Expr{StringExpr{Value: "[0-9]"}, StringExpr{Value: ""}}}, expected: "hello"},
				{name: "matches", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello"}, Method: "matches", Args: []Expr{StringExpr{Value: "[a-z]+"}}}, expected: true},
				{name: "matches requires full-string match", expr: MethodCallExpr{Receiver: StringExpr{Value: "hello123"}, Method: "matches", Args: []Expr{StringExpr{Value: "[a-z]+"}}}, expected: false},
				{name: "split", expr: MethodCallExpr{Receiver: StringExpr{Value: "a,b,c"}, Method: "split", Args: []Expr{StringExpr{Value: ","}}}, expected: []any{"a", "b", "c"}},
				{name: "tokenize", expr: MethodCallExpr{Receiver: StringExpr{Value: "a b c"}, Method: "tokenize", Args: []Expr{StringExpr{Value: " "}}}, expected: []any{"a", "b", "c"}},
				{name: "multiply", expr: MethodCallExpr{Receiver: StringExpr{Value: "abc"}, Method: "multiply", Args: []Expr{IntExpr{Value: 3}}}, expected: "abcabcabc"},
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
				{name: "unique", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}, IntExpr{Value: 2}, IntExpr{Value: 1}}}, Method: "unique", Args: []Expr{}}, expected: []any{1, 2, 3}},
				{name: "sort", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 3}, IntExpr{Value: 1}, IntExpr{Value: 2}}}, Method: "sort", Args: []Expr{}}, expected: []any{1, 2, 3}},
				{name: "plus", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}}}, Method: "plus", Args: []Expr{ListExpr{Elements: []Expr{IntExpr{Value: 3}, IntExpr{Value: 4}}}}}, expected: []any{1, 2, 3, 4}},
				{name: "minus", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}, Method: "minus", Args: []Expr{ListExpr{Elements: []Expr{IntExpr{Value: 2}}}}}, expected: []any{1, 3}},
				{name: "join", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{StringExpr{Value: "a"}, StringExpr{Value: "b"}, StringExpr{Value: "c"}}}, Method: "join", Args: []Expr{StringExpr{Value: "-"}}}, expected: "a-b-c"},
				{name: "take", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}, Method: "take", Args: []Expr{IntExpr{Value: 2}}}, expected: []any{1, 2}},
				{name: "drop", expr: MethodCallExpr{Receiver: ListExpr{Elements: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}, Method: "drop", Args: []Expr{IntExpr{Value: 1}}}, expected: []any{2, 3}},
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

		Convey("D4 closure-backed list methods evaluate end to end", func() {
			cases := []struct {
				name     string
				input    string
				expected any
			}{
				{name: "any", input: "[1, 2, 3].any { it > 2 }", expected: true},
				{name: "every", input: "[1, 2, 3].every { it > 0 }", expected: true},
				{name: "findAll", input: "[1, 2, 3].findAll { it > 1 }", expected: []any{2, 3}},
				{name: "find", input: "[1, 2, 3].find { it > 1 }", expected: 2},
			}

			for _, testCase := range cases {
				expr, err := parseTestExpr(testCase.input)

				So(err, ShouldBeNil)

				result, err := EvalExpr(expr, nil)

				So(err, ShouldBeNil)
				So(result, ShouldResemble, testCase.expected)
			}
		})
	})
}

func TestEvalExprE2StatementBodies(t *testing.T) {
	Convey("EvalExpr handles E2 statement evaluation in closures and functions", t, func() {
		evalClosure := func(source string, value any) (any, error) {
			expr, err := parseTestExpr(source)
			So(err, ShouldBeNil)

			closure, ok := expr.(ClosureExpr)
			So(ok, ShouldBeTrue)

			return evalSimpleClosure(closure, value, nil)
		}

		Convey("findAll closures honor guarded return statements", func() {
			expr, err := parseTestExpr("[1, null, 3].findAll { x -> if (x == null) return false; return x > 0 }")

			So(err, ShouldBeNil)

			result, err := EvalExpr(expr, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{1, 3})
		})

		Convey("collect closures honor guarded return statements", func() {
			expr, err := parseTestExpr("[null, 'a', null, 'b'].collect { x -> if (x == null) return 'N/A'; return x.toUpperCase() }")

			So(err, ShouldBeNil)

			result, err := EvalExpr(expr, nil)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{"N/A", "A", "N/A", "B"})
		})

		Convey("function bodies evaluate for-in accumulation loops", func() {
			wf, err := Parse(strings.NewReader("def total(items) { def sum = 0; for (x in items) { sum += x }; return sum }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)

			result, err := evalSimpleFuncDef(wf.Functions[0], []any{[]any{1, 2, 3}}, nil)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 6)
		})

		Convey("try catch closures evaluate the catch branch on parse failure", func() {
			result, err := evalClosure("{ x -> try { Integer.parseInt(x) } catch (Exception e) { -1 } }", "abc")

			So(err, ShouldBeNil)
			So(result, ShouldEqual, -1)
		})

		Convey("try catch closures return the try value when no error occurs", func() {
			result, err := evalClosure("{ x -> try { Integer.parseInt(x) } catch (Exception e) { -1 } }", "42")

			So(err, ShouldBeNil)
			So(result, ShouldEqual, 42)
		})

		Convey("switch closures return the first matching equality case", func() {
			result, err := evalClosure("{ x -> switch(x) { case 1: 'one'; break; case 2: 'two'; break; default: 'other' } }", 1)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "one")
		})

		Convey("switch closures fall back to default when no case matches", func() {
			result, err := evalClosure("{ x -> switch(x) { case 1: 'one'; break; case 2: 'two'; break; default: 'other' } }", 99)

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "other")
		})

		Convey("switch closures match regex pattern cases", func() {
			result, err := evalClosure("{ x -> switch(x) { case ~/^[A-Z]/: 'upper'; default: 'lower' } }", "Hello")

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "upper")
		})
	})
}
