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
	"io"
	"os"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestParseImports(t *testing.T) {
	Convey("Parse handles A4 import statements", t, func() {
		Convey("single-name local includes populate names and source", func() {
			wf, err := Parse(strings.NewReader("include { foo } from './modules/foo'"))

			So(err, ShouldBeNil)
			So(wf.Imports, ShouldHaveLength, 1)
			So(wf.Imports[0].Names, ShouldResemble, []string{"foo"})
			So(wf.Imports[0].Source, ShouldEqual, "./modules/foo")
		})

		Convey("multiple names separated by semicolons are preserved", func() {
			wf, err := Parse(strings.NewReader("include { foo ; bar } from './lib'"))

			So(err, ShouldBeNil)
			So(wf.Imports, ShouldHaveLength, 1)
			So(wf.Imports[0].Names, ShouldResemble, []string{"foo", "bar"})
		})

		Convey("aliases are recorded for remote includes", func() {
			wf, err := Parse(strings.NewReader("include { foo as myFoo } from 'nf-core/modules'"))

			So(err, ShouldBeNil)
			So(wf.Imports, ShouldHaveLength, 1)
			So(wf.Imports[0].Names, ShouldResemble, []string{"foo"})
			So(wf.Imports[0].Alias["foo"], ShouldEqual, "myFoo")
			So(wf.Imports[0].Source, ShouldEqual, "nf-core/modules")
		})

		Convey("missing module source returns a targeted error", func() {
			_, err := Parse(strings.NewReader("include { foo } from"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "missing module source")
		})
	})
}

func TestParseLexerHardening(t *testing.T) {
	Convey("Parse handles A6 lexer hardening", t, func() {
		Convey("shebang lines are ignored before a process", func() {
			wf, err := Parse(strings.NewReader("#!/usr/bin/env nextflow\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("single-line block comments are ignored", func() {
			wf, err := Parse(strings.NewReader("/* block comment */\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("multi-line block comments are ignored", func() {
			wf, err := Parse(strings.NewReader("/* multi\nline\ncomment */\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("nextflow enable flags are skipped", func() {
			wf, err := Parse(strings.NewReader("nextflow.enable.dsl = 2\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("nextflow preview flags are skipped", func() {
			wf, err := Parse(strings.NewReader("nextflow.preview.recursion = true\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("unterminated block comments return a targeted error", func() {
			_, err := Parse(strings.NewReader("/* unclosed block comment"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unterminated comment")
		})
	})
}

func TestParseTupleDeclarations(t *testing.T) {
	Convey("Parse handles A1 tuple input and output declarations", t, func() {
		Convey("tuple inputs capture val and path elements", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\ntuple val(id), path(reads)\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Kind, ShouldEqual, "tuple")
			So(wf.Processes[0].Input[0].Elements, ShouldHaveLength, 2)
			So(wf.Processes[0].Input[0].Elements[0].Kind, ShouldEqual, "val")
			So(wf.Processes[0].Input[0].Elements[0].Name, ShouldEqual, "id")
			So(wf.Processes[0].Input[0].Elements[1].Kind, ShouldEqual, "path")
			So(wf.Processes[0].Input[0].Elements[1].Name, ShouldEqual, "reads")
		})

		Convey("tuple outputs capture string path expressions", func() {
			wf, err := Parse(strings.NewReader("process foo {\noutput:\ntuple val(id), path(\"${id}.bam\")\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Kind, ShouldEqual, "tuple")
			So(wf.Processes[0].Output[0].Elements, ShouldHaveLength, 2)
			So(wf.Processes[0].Output[0].Elements[0].Kind, ShouldEqual, "val")
			So(wf.Processes[0].Output[0].Elements[0].Name, ShouldEqual, "id")
			So(wf.Processes[0].Output[0].Elements[1].Kind, ShouldEqual, "path")
			stringExpr, ok := wf.Processes[0].Output[0].Elements[1].Expr.(StringExpr)
			So(ok, ShouldBeTrue)
			So(stringExpr.Value, ShouldEqual, "${id}.bam")
		})

		Convey("tuple inputs accept arbitrary element counts", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\ntuple val(id), path(r1), path(r2)\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Elements, ShouldHaveLength, 3)
		})

		Convey("emit qualifiers are stored on simple output declarations", func() {
			wf, err := Parse(strings.NewReader("process foo {\noutput:\npath '*.bam', emit: bam\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Emit, ShouldEqual, "bam")
		})

		Convey("tuple outputs preserve line-level emit qualifiers", func() {
			wf, err := Parse(strings.NewReader("process foo {\noutput:\ntuple val(id), path('*.bam'), emit: aligned\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Emit, ShouldEqual, "aligned")
			So(wf.Processes[0].Output[0].Elements, ShouldHaveLength, 2)
		})

		Convey("optional qualifiers are stored on simple output declarations", func() {
			wf, err := Parse(strings.NewReader("process foo {\noutput:\npath 'out.txt', optional: true\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Optional, ShouldBeTrue)
		})

		Convey("env inputs capture the variable name", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\nenv(MY_VAR)\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Kind, ShouldEqual, "env")
			So(wf.Processes[0].Input[0].Name, ShouldEqual, "MY_VAR")
		})

		Convey("stdin inputs are recognised", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\nstdin\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Kind, ShouldEqual, "stdin")
		})

		Convey("tuple inputs accept arity qualifiers on path elements without error", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\ntuple val(meta), path('reads/*', arity: '1..*')\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Kind, ShouldEqual, "tuple")
			So(wf.Processes[0].Input[0].Elements, ShouldHaveLength, 2)
			So(wf.Processes[0].Input[0].Elements[1].Kind, ShouldEqual, "path")
		})
	})
}

func TestParseTopLevelOutputBlocks(t *testing.T) {
	Convey("Parse handles A7 top-level output blocks", t, func() {
		Convey("top-level output blocks are skipped before later definitions", func() {
			wf, err := Parse(strings.NewReader("output { samples { path 'fastq' } }\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("empty top-level output blocks parse without producing processes", func() {
			wf, err := Parse(strings.NewReader("output { }"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 0)
		})

		Convey("nested top-level output blocks are skipped with balanced braces", func() {
			wf, err := Parse(strings.NewReader("output { deeply { nested { block { } } } }"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 0)
		})

		Convey("top-level assignments named output still parse as channel assignments", func() {
			wf, err := Parse(strings.NewReader("output = Channel.of(1,2,3)\nworkflow { foo(output) }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)

			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "of")
			So(factory.Args, ShouldHaveLength, 3)
		})
	})
}

func TestParseFunctionDefinitions(t *testing.T) {
	Convey("Parse handles A3 top-level function definitions", t, func() {
		Convey("functions with parameters are stored on the workflow AST", func() {
			wf, err := Parse(strings.NewReader("def greet(name) { return \"hello ${name}\" }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Name, ShouldEqual, "greet")
			So(wf.Functions[0].Params, ShouldResemble, []string{"name"})
		})

		Convey("functions and processes can coexist at top level", func() {
			wf, err := Parse(strings.NewReader("def add(a, b) { a + b }\nprocess foo {\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Name, ShouldEqual, "add")
			So(wf.Functions[0].Params, ShouldResemble, []string{"a", "b"})
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("functions without parameters record an empty parameter list", func() {
			wf, err := Parse(strings.NewReader("def noParams() { 42 }"))

			So(err, ShouldBeNil)
			So(wf.Functions, ShouldHaveLength, 1)
			So(wf.Functions[0].Name, ShouldEqual, "noParams")
			So(wf.Functions[0].Params, ShouldResemble, []string{})
		})
	})
}

func TestParseComparisonAndLogicalExpressions(t *testing.T) {
	Convey("parseExprTokens handles D2 comparison and logical operators", t, func() {
		Convey("comparison operators parse as binary expressions", func() {
			cases := []struct {
				name   string
				source string
				op     string
				left   Expr
				right  Expr
			}{
				{name: "equal", source: "1 == 1", op: "==", left: IntExpr{Value: 1}, right: IntExpr{Value: 1}},
				{name: "not equal", source: "1 != 2", op: "!=", left: IntExpr{Value: 1}, right: IntExpr{Value: 2}},
				{name: "greater equal", source: "3 >= 3", op: ">=", left: IntExpr{Value: 3}, right: IntExpr{Value: 3}},
				{name: "less equal", source: "2 <= 3", op: "<=", left: IntExpr{Value: 2}, right: IntExpr{Value: 3}},
			}

			for _, testCase := range cases {
				tokens, err := lex(testCase.source)

				So(err, ShouldBeNil)
				expr, err := parseExprTokens(tokens[:len(tokens)-1])

				So(err, ShouldBeNil)
				So(expr, ShouldResemble, BinaryExpr{Left: testCase.left, Op: testCase.op, Right: testCase.right})
			}
		})

		Convey("logical operators respect precedence and unary negation", func() {
			tokens, err := lex("1 == 1 && 2 > 1 || !false")

			So(err, ShouldBeNil)
			expr, err := parseExprTokens(tokens[:len(tokens)-1])

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, BinaryExpr{
				Left: BinaryExpr{
					Left:  BinaryExpr{Left: IntExpr{Value: 1}, Op: "==", Right: IntExpr{Value: 1}},
					Op:    "&&",
					Right: BinaryExpr{Left: IntExpr{Value: 2}, Op: ">", Right: IntExpr{Value: 1}},
				},
				Op:    "||",
				Right: UnaryExpr{Op: "!", Operand: BoolExpr{Value: false}},
			})
		})

		Convey("unary minus parses as a UnaryExpr", func() {
			tokens, err := lex("-1")

			So(err, ShouldBeNil)
			expr, err := parseExprTokens(tokens[:len(tokens)-1])

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, UnaryExpr{Op: "-", Operand: IntExpr{Value: 1}})
		})

		Convey("parsed process directives keep comparison and logical ASTs", func() {
			wf, err := Parse(strings.NewReader("process foo {\nwhen: !false && 1 == 1\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].When, ShouldEqual, "!false && 1 == 1")
		})
	})
}

func TestParseD6CastExpressions(t *testing.T) {
	Convey("Parse handles D6 cast expressions", t, func() {
		Convey("string literals cast to Integer are parsed into CastExpr", func() {
			wf, err := Parse(strings.NewReader("process foo {\ncpus '42' as Integer\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			castExpr, ok := wf.Processes[0].Directives["cpus"].(CastExpr)
			So(ok, ShouldBeTrue)
			So(castExpr.TypeName, ShouldEqual, "Integer")
			operand, ok := castExpr.Operand.(StringExpr)
			So(ok, ShouldBeTrue)
			So(operand.Value, ShouldEqual, "42")
		})

		Convey("integer literals cast to String are parsed into CastExpr", func() {
			wf, err := Parse(strings.NewReader("process foo {\ncpus 42 as String\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			castExpr, ok := wf.Processes[0].Directives["cpus"].(CastExpr)
			So(ok, ShouldBeTrue)
			So(castExpr.TypeName, ShouldEqual, "String")
			operand, ok := castExpr.Operand.(IntExpr)
			So(ok, ShouldBeTrue)
			So(operand.Value, ShouldEqual, 42)
		})
	})
}

func TestParseD1TernaryAndElvisExpressions(t *testing.T) {
	Convey("Parse handles D1 ternary and elvis operators", t, func() {
		Convey("ternary expressions produce a TernaryExpr with a condition", func() {
			expr, err := parseTestExpr("task.attempt > 1 ? '16 GB' : '8 GB'")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, TernaryExpr{
				Cond:  BinaryExpr{Left: VarExpr{Root: "task", Path: "attempt"}, Op: ">", Right: IntExpr{Value: 1}},
				True:  StringExpr{Value: "16 GB"},
				False: StringExpr{Value: "8 GB"},
			})
		})

		Convey("elvis expressions produce a TernaryExpr without a condition", func() {
			expr, err := parseTestExpr("x ?: 'default'")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, TernaryExpr{
				True:  VarExpr{Root: "x"},
				False: StringExpr{Value: "default"},
			})
		})
	})
}

func TestParseD3MethodCalls(t *testing.T) {
	Convey("Parse handles D3 method calls on strings and lists", t, func() {
		Convey("receiver method calls parse into MethodCallExpr", func() {
			expr, err := parseTestExpr("'  hello  '.trim()")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, MethodCallExpr{
				Receiver: StringExpr{Value: "  hello  "},
				Method:   "trim",
				Args:     []Expr{},
			})
		})

		Convey("method calls with arguments keep parsed argument expressions", func() {
			expr, err := parseTestExpr("'hello'.replace('l', 'r')")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, MethodCallExpr{
				Receiver: StringExpr{Value: "hello"},
				Method:   "replace",
				Args: []Expr{
					StringExpr{Value: "l"},
					StringExpr{Value: "r"},
				},
			})
		})

		Convey("method chaining nests MethodCallExpr receivers", func() {
			expr, err := parseTestExpr("'hello'.trim().toUpperCase()")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, MethodCallExpr{
				Receiver: MethodCallExpr{
					Receiver: StringExpr{Value: "hello"},
					Method:   "trim",
					Args:     []Expr{},
				},
				Method: "toUpperCase",
				Args:   []Expr{},
			})
		})

		Convey("list receivers remain valid method-call receivers", func() {
			expr, err := parseTestExpr("[1, 2, 3].size()")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, MethodCallExpr{
				Receiver: ListExpr{Elements: []Expr{
					IntExpr{Value: 1},
					IntExpr{Value: 2},
					IntExpr{Value: 3},
				}},
				Method: "size",
				Args:   []Expr{},
			})
		})

		Convey("trailing closure syntax is preserved as a deferred collect argument", func() {
			expr, err := parseTestExpr("[1, 2, 3].collect { it * 2 }")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, MethodCallExpr{
				Receiver: ListExpr{Elements: []Expr{
					IntExpr{Value: 1},
					IntExpr{Value: 2},
					IntExpr{Value: 3},
				}},
				Method: "collect",
				Args:   []Expr{ClosureExpr{Params: []string{}, Body: "it * 2"}},
			})
		})
	})
}

func TestParseD5Expressions(t *testing.T) {
	Convey("Parse handles D5 null literal and null-safe navigation", t, func() {
		Convey("null literals produce a NullExpr", func() {
			expr, err := parseTestExpr("null")

			So(err, ShouldBeNil)
			So(expr, ShouldHaveSameTypeAs, NullExpr{})
		})

		Convey("task references remain dotted variable expressions", func() {
			expr, err := parseTestExpr("task.attempt")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, VarExpr{Root: "task", Path: "attempt"})
		})

		Convey("null-safe property access produces a NullSafeExpr", func() {
			expr, err := parseTestExpr("x?.property")

			So(err, ShouldBeNil)
			So(expr, ShouldResemble, NullSafeExpr{Receiver: VarExpr{Root: "x"}, Property: "property"})
		})
	})
}

func TestParseD4Expressions(t *testing.T) {
	Convey("Parse handles D4 list and map literals", t, func() {
		Convey("list literals produce a ListExpr", func() {
			expr, err := parseTestExpr("[1, 2, 3]")

			So(err, ShouldBeNil)
			listExpr, ok := expr.(ListExpr)
			So(ok, ShouldBeTrue)
			So(listExpr.Elements, ShouldHaveLength, 3)
			So(listExpr.Elements[0], ShouldResemble, IntExpr{Value: 1})
			So(listExpr.Elements[1], ShouldResemble, IntExpr{Value: 2})
			So(listExpr.Elements[2], ShouldResemble, IntExpr{Value: 3})
		})

		Convey("map literals produce a MapExpr", func() {
			expr, err := parseTestExpr("[a: 1, b: 2]")

			So(err, ShouldBeNil)
			mapExpr, ok := expr.(MapExpr)
			So(ok, ShouldBeTrue)
			So(mapExpr.Keys, ShouldResemble, []Expr{
				StringExpr{Value: "a"},
				StringExpr{Value: "b"},
			})
			So(mapExpr.Values, ShouldResemble, []Expr{
				IntExpr{Value: 1},
				IntExpr{Value: 2},
			})
		})

		Convey("empty map literals are distinguished from empty lists", func() {
			expr, err := parseTestExpr("[:]")

			So(err, ShouldBeNil)
			_, ok := expr.(MapExpr)
			So(ok, ShouldBeTrue)
		})

		Convey("subscript access produces an IndexExpr", func() {
			expr, err := parseTestExpr("list[0]")

			So(err, ShouldBeNil)
			indexExpr, ok := expr.(IndexExpr)
			So(ok, ShouldBeTrue)
			So(indexExpr.Receiver, ShouldResemble, VarExpr{Root: "list"})
			So(indexExpr.Index, ShouldResemble, IntExpr{Value: 0})
		})
	})
}

func parseTestExpr(input string) (Expr, error) {
	tokens, err := lex(input)
	if err != nil {
		return nil, err
	}

	return parseExprTokens(tokens[:len(tokens)-1])
}

func TestParseProcessDefinitions(t *testing.T) {
	Convey("Parse handles A1 process definitions", t, func() {
		Convey("basic process with input, output, and script", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput: val x\noutput: path 'out.txt'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Script, ShouldEqual, "echo hello")
		})

		Convey("label directives populate Labels", func() {
			wf, err := Parse(strings.NewReader("process foo {\nlabel 'big_mem'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Labels, ShouldResemble, []string{"big_mem"})
		})

		Convey("multiple label directives append in order", func() {
			wf, err := Parse(strings.NewReader("process foo {\nlabel 'big_mem'\nlabel 'long_time'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Labels, ShouldResemble, []string{"big_mem", "long_time"})
		})

		Convey("processes without label directives keep an empty Labels slice", func() {
			wf, err := Parse(strings.NewReader("process foo {\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Labels, ShouldResemble, []string{})
		})

		Convey("cpus, memory, and time directives are normalised", func() {
			wf, err := Parse(strings.NewReader("process foo {\ncpus 4\nmemory '8 GB'\ntime '2.h'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(intExprValue(wf.Processes[0].Directives["cpus"]), ShouldEqual, 4)
			So(intExprValue(wf.Processes[0].Directives["memory"]), ShouldEqual, 8192)
			So(intExprValue(wf.Processes[0].Directives["time"]), ShouldEqual, 120)
		})
		Convey("container directive populates the container field", func() {
			wf, err := Parse(strings.NewReader("process foo {\ncontainer 'ubuntu:22.04'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Container, ShouldEqual, "ubuntu:22.04")
		})

		Convey("maxForks populates MaxForks", func() {
			wf, err := Parse(strings.NewReader("process foo {\nmaxForks 5\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].MaxForks, ShouldEqual, 5)
		})

		Convey("errorStrategy and maxRetries populate retry settings", func() {
			wf, err := Parse(strings.NewReader("process foo {\nerrorStrategy 'retry'\nmaxRetries 3\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].ErrorStrat, ShouldEqual, "retry")
			So(wf.Processes[0].MaxRetries, ShouldEqual, 3)
		})

		Convey("publishDir stores a target path", func() {
			wf, err := Parse(strings.NewReader("process foo {\npublishDir '/results'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].PublishDir, ShouldHaveLength, 1)
			So(wf.Processes[0].PublishDir[0].Path, ShouldEqual, "/results")
			So(wf.Processes[0].PublishDir[0].Mode, ShouldEqual, "copy")
		})

		Convey("publishDir stores pattern options", func() {
			wf, err := Parse(strings.NewReader("process foo {\npublishDir '/results', pattern: '*.bam'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].PublishDir, ShouldHaveLength, 1)
			So(wf.Processes[0].PublishDir[0].Pattern, ShouldEqual, "*.bam")
		})

		Convey("env directive stores key-value pairs", func() {
			wf, err := Parse(strings.NewReader("process foo {\nenv MY_VAR: 'value'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Env["MY_VAR"], ShouldEqual, "value")
		})

		Convey("empty input yields no processes", func() {
			wf, err := Parse(strings.NewReader("\n\t// nothing here\n"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 0)
		})

		Convey("leading shebang lines are ignored", func() {
			wf, err := Parse(strings.NewReader("#!/usr/bin/env nextflow\n# generated by remote entrypoint\nprocess foo {\nscript: 'echo hello'\n}\nworkflow { foo() }\n"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "foo")
		})

		Convey("syntax errors include the line number", func() {
			_, err := Parse(strings.NewReader("process foo {\nscript: 'echo hello'\n"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "line 2")
		})

		Convey("disk directives are normalised to GB integers", func() {
			wf, err := Parse(strings.NewReader("process foo {\ndisk '10 GB'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(intExprValue(wf.Processes[0].Directives["disk"]), ShouldEqual, 10)
		})

		Convey("tag directives populate Tag", func() {
			wf, err := Parse(strings.NewReader("process foo {\ntag \"sample_${id}\"\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Tag, ShouldEqual, "sample_${id}")
		})

		Convey("beforeScript directives populate BeforeScript", func() {
			wf, err := Parse(strings.NewReader("process foo {\nbeforeScript 'module load samtools'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].BeforeScript, ShouldEqual, "module load samtools")
		})

		Convey("afterScript directives populate AfterScript", func() {
			wf, err := Parse(strings.NewReader("process foo {\nafterScript 'cleanup.sh'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].AfterScript, ShouldEqual, "cleanup.sh")
		})

		Convey("module directives populate Module", func() {
			wf, err := Parse(strings.NewReader("process foo {\nmodule 'samtools/1.17'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Module, ShouldEqual, "samtools/1.17")
		})

		Convey("cache directives populate Cache", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\ncache 'lenient'\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Cache, ShouldEqual, "lenient")
			So(stderr, ShouldContainSubstring, "unsupported directive \"cache\"")
		})

		Convey("scratch directives are stored instead of ignored", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nscratch true\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Directives["scratch"], ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "unsupported directive \"scratch\"")
		})

		Convey("storeDir directives are stored", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nstoreDir '/cache/outputs'\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Directives["storeDir"], ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "unsupported directive \"storeDir\"")
		})

		Convey("queue directives are stored", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nqueue 'long'\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Directives["queue"], ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "unsupported directive \"queue\"")
		})

		Convey("debug directives are stored", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\ndebug true\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Directives["debug"], ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "unsupported directive \"debug\"")
		})

		Convey("clusterOptions directives are stored", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nclusterOptions '--mem=8G'\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Directives["clusterOptions"], ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "unsupported directive \"clusterOptions\"")
		})

		Convey("executor directives are stored", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nexecutor 'slurm'\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Directives["executor"], ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "unsupported directive \"executor\"")
		})

		Convey("secret directives are stored", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nsecret 'MY_TOKEN'\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Directives["secret"], ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "unsupported directive \"secret\"")
		})

		Convey("legacy DSL1 into syntax is rejected", func() {
			_, err := Parse(strings.NewReader("process foo {\noutput: stdout into result\nscript: 'echo hello'\n}"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "DSL 1")
		})

		Convey("unknown directives are ignored", func() {
			wf, err := Parse(strings.NewReader("process foo {\nunknownDirective true\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			_, ok := wf.Processes[0].Directives["unknownDirective"]
			So(ok, ShouldBeFalse)
		})
	})
}

func TestParseWorkflowBlocks(t *testing.T) {
	Convey("Parse handles A2 workflow blocks", t, func() {
		Convey("named workflows capture take, main, and emit sections", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\ntake:\nreads_ch\nmain:\nBWA(reads_ch)\nemit:\nbam = BWA.out.bam\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Name, ShouldEqual, "ALIGN")
			So(wf.SubWFs[0].Body, ShouldNotBeNil)
			So(wf.SubWFs[0].Body.Take, ShouldResemble, []string{"reads_ch"})
			So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls[0].Target, ShouldEqual, "BWA")
			So(wf.SubWFs[0].Body.Emit, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Emit[0].Name, ShouldEqual, "bam")
			So(wf.SubWFs[0].Body.Emit[0].Expr, ShouldEqual, "BWA.out.bam")
		})

		Convey("take sections can list multiple channels", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\ntake:\nch1\nch2\nmain:\nBWA(ch1, ch2)\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Take, ShouldResemble, []string{"ch1", "ch2"})
		})

		Convey("main sections populate calls without take or emit", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\nmain:\nBWA(reads_ch)\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Take, ShouldBeEmpty)
			So(wf.SubWFs[0].Body.Emit, ShouldBeEmpty)
			So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls[0].Target, ShouldEqual, "BWA")
		})

		Convey("emit sections accept bare channel names", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\nmain:\nBWA(reads_ch)\nemit:\nresult_ch\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Emit, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Emit[0].Name, ShouldEqual, "result_ch")
			So(wf.SubWFs[0].Body.Emit[0].Expr, ShouldEqual, "")
		})

		Convey("publish sections are accepted without affecting main calls", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\nmain:\nBWA(reads_ch)\npublish:\nbam = 'results'\nemit:\nresult_ch\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls[0].Target, ShouldEqual, "BWA")
			So(wf.SubWFs[0].Body.Emit, ShouldHaveLength, 1)
		})

		Convey("publish sections accept colon-style property lines without opening new sections", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\nmain:\nBWA(reads_ch)\npublish:\nenabled: true\npath: 'results'\nmode: 'copy'\nemit:\nresult_ch\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls[0].Target, ShouldEqual, "BWA")
			So(wf.SubWFs[0].Body.Emit, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Emit[0].Name, ShouldEqual, "result_ch")
		})

		Convey("publish sections skip inline closure values without terminating the workflow block", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\nmain:\nBWA(reads_ch)\npublish:\nsaveAs: { filename -> \"${filename}.bam\" }\nemit:\nresult_ch\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls[0].Target, ShouldEqual, "BWA")
			So(wf.SubWFs[0].Body.Emit, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Emit[0].Name, ShouldEqual, "result_ch")
		})

		Convey("publish inline closures still allow later top-level content to parse", func() {
			wf, err := Parse(strings.NewReader("workflow ALIGN {\nmain:\nBWA(reads_ch)\npublish:\nsaveAs: { filename -> \"${filename}.bam\" }\nemit:\nresult_ch\n}\nprocess done {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Emit, ShouldHaveLength, 1)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "done")
		})

		Convey("entry workflow records ordered calls", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch) ; bar(foo.out) }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 2)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "foo")
			So(wf.EntryWF.Calls[1].Target, ShouldEqual, "bar")

			ref, ok := wf.EntryWF.Calls[1].Args[0].(ChanRef)
			So(ok, ShouldBeTrue)
			So(ref.Name, ShouldEqual, "foo.out")
		})

		Convey("named workflow becomes a subworkflow", func() {
			wf, err := Parse(strings.NewReader("workflow named { foo(x) }"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Name, ShouldEqual, "named")
			So(wf.SubWFs[0].Body, ShouldNotBeNil)
			So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls[0].Target, ShouldEqual, "foo")
		})

		Convey("channel assignments resolve call arguments to the assigned factory", func() {
			wf, err := Parse(strings.NewReader("ch = Channel.of(1,2,3)\nworkflow { foo(ch) }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)

			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "of")
			So(factory.Args, ShouldHaveLength, 3)
			So(intExprValue(factory.Args[0]), ShouldEqual, 1)
			So(intExprValue(factory.Args[1]), ShouldEqual, 2)
			So(intExprValue(factory.Args[2]), ShouldEqual, 3)
		})

		Convey("workflow-body channel assignments resolve later call arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { ch = Channel.of(1,2,3); foo(ch) }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)

			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "of")
			So(factory.Args, ShouldHaveLength, 3)
			So(intExprValue(factory.Args[0]), ShouldEqual, 1)
			So(intExprValue(factory.Args[1]), ShouldEqual, 2)
			So(intExprValue(factory.Args[2]), ShouldEqual, 3)
		})

		Convey("workflow-body channel assignments are isolated per workflow block", func() {
			wf, err := Parse(strings.NewReader("shared = Channel.of(9)\nworkflow named { local = Channel.of(1); foo(local, shared) }\nworkflow { bar(local, shared) }"))

			So(err, ShouldBeNil)
			So(wf.SubWFs, ShouldHaveLength, 1)
			So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)

			localFactory, ok := wf.SubWFs[0].Body.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(localFactory.Name, ShouldEqual, "of")
			So(localFactory.Args, ShouldHaveLength, 1)
			So(intExprValue(localFactory.Args[0]), ShouldEqual, 1)

			sharedFactory, ok := wf.SubWFs[0].Body.Calls[0].Args[1].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(sharedFactory.Name, ShouldEqual, "of")
			So(sharedFactory.Args, ShouldHaveLength, 1)
			So(intExprValue(sharedFactory.Args[0]), ShouldEqual, 9)

			localRef, ok := wf.EntryWF.Calls[0].Args[0].(ChanRef)
			So(ok, ShouldBeTrue)
			So(localRef.Name, ShouldEqual, "local")

			entrySharedFactory, ok := wf.EntryWF.Calls[0].Args[1].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(entrySharedFactory.Name, ShouldEqual, "of")
			So(entrySharedFactory.Args, ShouldHaveLength, 1)
			So(intExprValue(entrySharedFactory.Args[0]), ShouldEqual, 9)
		})

		Convey("workflow call arguments can be inline channel factories", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.fromPath('/data/*.fq')) }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)

			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "fromPath")
			So(factory.Args, ShouldHaveLength, 1)
			glob, ok := factory.Args[0].(StringExpr)
			So(ok, ShouldBeTrue)
			So(glob.Value, ShouldEqual, "/data/*.fq")
		})

		Convey("pipe expressions are preserved as channel expressions", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.of(1, 2) | flatten) }"))

			So(err, ShouldBeNil)
			pipeExpr, ok := wf.EntryWF.Calls[0].Args[0].(PipeExpr)
			So(ok, ShouldBeTrue)
			So(pipeExpr.Stages, ShouldHaveLength, 2)
			first, ok := pipeExpr.Stages[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(first.Name, ShouldEqual, "of")
			second, ok := pipeExpr.Stages[1].(ChanRef)
			So(ok, ShouldBeTrue)
			So(second.Name, ShouldEqual, "flatten")
		})

		Convey("workflow pipelines with bare process stages are desugared into calls", func() {
			wf, err := Parse(strings.NewReader("#!/usr/bin/env nextflow\n\nprocess sayHello {\n    input:\n    val x\n\n    output:\n  \n stdout\n\n    script:\n    \"\"\"\n    echo '${x} world!'\n    \"\"\"\n}\n\nworkflow {\n   \nChannel.of('Bonjour', 'Ciao', 'Hello', 'Hola') | sayHello | view\n}\n"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "sayHello")

			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "of")
			So(factory.Args, ShouldHaveLength, 4)
			So(factory.Args[0].(StringExpr).Value, ShouldEqual, "Bonjour")
			So(factory.Args[3].(StringExpr).Value, ShouldEqual, "Hola")
		})

		Convey("workflow if blocks capture conditions and calls", func() {
			wf, err := Parse(strings.NewReader("workflow {\nif (params.aligner == 'bwa') {\nBWA(reads)\n}\n}\n"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Conditions, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].Condition, ShouldContainSubstring, "params.aligner == 'bwa'")
			So(wf.EntryWF.Conditions[0].Body, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].Body[0].Target, ShouldEqual, "BWA")
		})

		Convey("workflow if else blocks capture both branches", func() {
			wf, err := Parse(strings.NewReader("workflow {\nif (x) {\nA(ch)\n} else {\nB(ch)\n}\n}\n"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Conditions, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].Body, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].Body[0].Target, ShouldEqual, "A")
			So(wf.EntryWF.Conditions[0].ElseBody, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].ElseBody[0].Target, ShouldEqual, "B")
		})

		Convey("workflow else if chains are captured separately from else bodies", func() {
			wf, err := Parse(strings.NewReader("workflow {\nif (x) {\nA(ch)\n} else if (y) {\nB(ch)\n} else {\nC(ch)\n}\n}\n"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Conditions, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].ElseIf, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].ElseIf[0].Condition, ShouldEqual, "y")
			So(wf.EntryWF.Conditions[0].ElseIf[0].Body, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].ElseIf[0].Body[0].Target, ShouldEqual, "B")
			So(wf.EntryWF.Conditions[0].ElseBody, ShouldHaveLength, 1)
			So(wf.EntryWF.Conditions[0].ElseBody[0].Target, ShouldEqual, "C")
		})

		Convey("workflow blocks without conditionals keep an empty Conditions slice", func() {
			wf, err := Parse(strings.NewReader("workflow {\nA(ch)\n}\n"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Conditions, ShouldResemble, []*IfBlock{})
		})

		Convey("workflow.onComplete blocks are skipped without affecting process parsing", func() {
			wf, err := Parse(strings.NewReader("workflow.onComplete { println 'Done' }\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("workflow.onError blocks are skipped without affecting process parsing", func() {
			wf, err := Parse(strings.NewReader("workflow.onError { println 'Failed' }\nprocess foo {\nscript: 'echo hi'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Name, ShouldEqual, "foo")
		})

		Convey("workflow lifecycle handlers alone parse without producing processes", func() {
			wf, err := Parse(strings.NewReader("workflow.onComplete { }\nworkflow.onError { }"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 0)
		})
	})
}

func TestParseChannelFactoriesAndOperators(t *testing.T) {
	Convey("Parse handles A3 channel factories and operators", t, func() {
		Convey("closures capture explicit and implicit parameters", func() {
			testCases := []struct {
				name   string
				source string
				params []string
				body   string
			}{
				{name: "single explicit param", source: "workflow { foo(ch.map { item -> item.id }) }", params: []string{"item"}, body: "item.id"},
				{name: "multiple explicit params", source: "workflow { foo(ch.filter { a, b -> a > b }) }", params: []string{"a", "b"}, body: "a > b"},
				{name: "implicit it", source: "workflow { foo(ch.map { it * 2 }) }", params: []string{}, body: "it * 2"},
				{name: "explicit empty params", source: "workflow { foo(ch.map { -> 42 }) }", params: []string{}, body: "42"},
			}

			for _, testCase := range testCases {
				wf, err := Parse(strings.NewReader(testCase.source))

				So(err, ShouldBeNil)
				chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
				So(chain.Operators, ShouldHaveLength, 1)
				So(chain.Operators[0].ClosureExpr, ShouldNotBeNil)
				So(chain.Operators[0].ClosureExpr.Params, ShouldResemble, testCase.params)
				So(chain.Operators[0].ClosureExpr.Body, ShouldEqual, testCase.body)
			}
		})

		Convey("Channel.of chained to map parses as a factory with one operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.of(1,2,3).map { it * 2 }) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			factory, ok := chain.Source.(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "of")
			So(factory.Args, ShouldHaveLength, 3)
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "map")
			So(chain.Operators[0].Closure, ShouldEqual, "it * 2")
		})

		Convey("Channel.fromFilePairs parses its glob argument", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.fromFilePairs('/data/*_{1,2}.fq')) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "fromFilePairs")
			So(factory.Args, ShouldHaveLength, 1)
			So(factory.Args[0], ShouldHaveSameTypeAs, StringExpr{})
			So(factory.Args[0].(StringExpr).Value, ShouldEqual, "/data/*_{1,2}.fq")
		})

		Convey("Channel.empty parses as a zero-arg factory", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.empty()) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "empty")
			So(factory.Args, ShouldHaveLength, 0)
		})

		Convey("Channel.value parses a string argument", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.value('hello')) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "value")
			So(factory.Args, ShouldHaveLength, 1)
			So(factory.Args[0].(StringExpr).Value, ShouldEqual, "hello")
		})

		Convey("Channel.fromList parses a list literal argument", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.fromList([1,2,3])) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "fromList")
			So(factory.Args, ShouldHaveLength, 1)

			listExpr, ok := factory.Args[0].(ListExpr)
			So(ok, ShouldBeTrue)
			So(listExpr.Elements, ShouldHaveLength, 3)
			So(listExpr.Elements[0], ShouldResemble, IntExpr{Value: 1})
			So(listExpr.Elements[1], ShouldResemble, IntExpr{Value: 2})
			So(listExpr.Elements[2], ShouldResemble, IntExpr{Value: 3})
		})

		Convey("Channel.from parses variadic literal arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.from(1,2,3)) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "from")
			So(factory.Args, ShouldHaveLength, 3)
			So(intExprValue(factory.Args[0]), ShouldEqual, 1)
			So(intExprValue(factory.Args[1]), ShouldEqual, 2)
			So(intExprValue(factory.Args[2]), ShouldEqual, 3)
		})

		Convey("Channel.fromSRA parses as a recognized factory", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.fromSRA('SRR1234')) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "fromSRA")
			So(factory.Args, ShouldHaveLength, 1)
			So(factory.Args[0].(StringExpr).Value, ShouldEqual, "SRR1234")
		})

		Convey("Channel.topic parses as a recognized factory", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.topic('myTopic')) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "topic")
			So(factory.Args, ShouldHaveLength, 1)
			So(factory.Args[0].(StringExpr).Value, ShouldEqual, "myTopic")
		})

		Convey("Channel.watchPath parses its glob argument", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.watchPath('/data/*.fq')) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "watchPath")
			So(factory.Args, ShouldHaveLength, 1)
			So(factory.Args[0], ShouldHaveSameTypeAs, StringExpr{})
			So(factory.Args[0].(StringExpr).Value, ShouldEqual, "/data/*.fq")
		})

		Convey("Channel.interval parses an integer argument", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.interval(100)) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "interval")
			So(factory.Args, ShouldHaveLength, 1)
			So(intExprValue(factory.Args[0]), ShouldEqual, 100)
		})

		Convey("Channel.fromLineage parses as a recognized factory", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.fromLineage('query')) }"))

			So(err, ShouldBeNil)
			factory, ok := wf.EntryWF.Calls[0].Args[0].(ChannelFactory)
			So(ok, ShouldBeTrue)
			So(factory.Name, ShouldEqual, "fromLineage")
			So(factory.Args, ShouldHaveLength, 1)
			So(factory.Args[0].(StringExpr).Value, ShouldEqual, "query")
		})

		Convey("filter and collect chains preserve both operators", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.filter { it > 5 }.collect()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			ref, ok := chain.Source.(ChanRef)
			So(ok, ShouldBeTrue)
			So(ref.Name, ShouldEqual, "ch")
			So(chain.Operators, ShouldHaveLength, 2)
			So(chain.Operators[0].Name, ShouldEqual, "filter")
			So(chain.Operators[0].Closure, ShouldEqual, "it > 5")
			So(chain.Operators[1].Name, ShouldEqual, "collect")
		})

		Convey("groupTuple parses as a supported operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.groupTuple()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "groupTuple")
		})

		Convey("join keeps channel arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.join(other)) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "join")
			So(chain.Operators[0].Channels, ShouldHaveLength, 1)
			joined, ok := chain.Operators[0].Channels[0].(ChanRef)
			So(ok, ShouldBeTrue)
			So(joined.Name, ShouldEqual, "other")
		})

		Convey("mix keeps multiple channel arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.mix(a, b)) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "mix")
			So(chain.Operators[0].Channels, ShouldHaveLength, 2)
			So(chain.Operators[0].Channels[0].(ChanRef).Name, ShouldEqual, "a")
			So(chain.Operators[0].Channels[1].(ChanRef).Name, ShouldEqual, "b")
		})

		Convey("first parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.first()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "first")
		})

		Convey("flatMap parses a closure operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.flatMap { it.split(',') }) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "flatMap")
			So(chain.Operators[0].Closure, ShouldEqual, "it.split(',')")
		})

		Convey("last parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.last()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "last")
		})

		Convey("take parses an integer argument", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.take(3)) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "take")
			So(chain.Operators[0].Args, ShouldHaveLength, 1)
			So(intExprValue(chain.Operators[0].Args[0]), ShouldEqual, 3)
		})
	})
}

func TestParsePhase4B1ChannelOperators(t *testing.T) {
	Convey("Parse handles Phase 4 B1 additional channel operators", t, func() {
		Convey("new operators parse with expected arguments, channels, and closures", func() {
			testCases := []struct {
				name            string
				source          string
				expectName      string
				expectArgs      int
				expectChannels  int
				expectClosure   string
				expectFirstInt  int
				expectFirstChan string
			}{
				{name: "cross", source: "workflow { foo(ch.cross(other)) }", expectName: "cross", expectChannels: 1, expectFirstChan: "other"},
				{name: "splitJson", source: "workflow { foo(ch.splitJson()) }", expectName: "splitJson"},
				{name: "splitText", source: "workflow { foo(ch.splitText(by: 1000)) }", expectName: "splitText", expectArgs: 1},
				{name: "buffer", source: "workflow { foo(ch.buffer(size: 3)) }", expectName: "buffer", expectArgs: 1},
				{name: "collate", source: "workflow { foo(ch.collate(5)) }", expectName: "collate", expectArgs: 1, expectFirstInt: 5},
				{name: "until", source: "workflow { foo(ch.until { it == 'DONE' }) }", expectName: "until", expectClosure: "it == 'DONE'"},
				{name: "subscribe", source: "workflow { foo(ch.subscribe { println it }) }", expectName: "subscribe", expectClosure: "println it"},
				{name: "sum", source: "workflow { foo(ch.sum()) }", expectName: "sum"},
				{name: "min", source: "workflow { foo(ch.min()) }", expectName: "min"},
				{name: "max", source: "workflow { foo(ch.max()) }", expectName: "max"},
				{name: "randomSample", source: "workflow { foo(ch.randomSample(10)) }", expectName: "randomSample", expectArgs: 1, expectFirstInt: 10},
				{name: "merge", source: "workflow { foo(ch.merge(other)) }", expectName: "merge", expectChannels: 1, expectFirstChan: "other"},
				{name: "toInteger", source: "workflow { foo(ch.toInteger()) }", expectName: "toInteger"},
			}

			for _, testCase := range testCases {
				wf, err := Parse(strings.NewReader(testCase.source))

				So(err, ShouldBeNil)
				chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
				So(chain.Operators, ShouldHaveLength, 1)
				So(chain.Operators[0].Name, ShouldEqual, testCase.expectName)
				So(chain.Operators[0].Args, ShouldHaveLength, testCase.expectArgs)
				So(chain.Operators[0].Channels, ShouldHaveLength, testCase.expectChannels)
				So(chain.Operators[0].Closure, ShouldEqual, testCase.expectClosure)

				if testCase.expectFirstInt != 0 {
					So(intExprValue(chain.Operators[0].Args[0]), ShouldEqual, testCase.expectFirstInt)
				}

				if testCase.expectFirstChan != "" {
					channel, ok := chain.Operators[0].Channels[0].(ChanRef)
					So(ok, ShouldBeTrue)
					So(channel.Name, ShouldEqual, testCase.expectFirstChan)
				}
			}
		})

		Convey("deprecated count operators parse and emit warnings", func() {
			for _, operatorName := range []string{"countFasta", "countFastq", "countJson", "countLines"} {
				var (
					wf     *Workflow
					err    error
					stderr string
				)

				stderr = captureParseStderr(func() {
					wf, err = Parse(strings.NewReader("workflow { foo(ch." + operatorName + "()) }"))
				})

				So(err, ShouldBeNil)
				chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
				So(chain.Operators, ShouldHaveLength, 1)
				So(chain.Operators[0].Name, ShouldEqual, operatorName)
				So(stderr, ShouldContainSubstring, "deprecated")
				So(stderr, ShouldContainSubstring, operatorName)
			}
		})
	})
}

func TestParseJ1DeprecatedConstructs(t *testing.T) {
	Convey("Parse handles J1 deprecated and DSL1-only constructs", t, func() {
		Convey("Channel.create reports a DSL1-only parse error", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.create()) }"))

			So(wf, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "DSL1-only construct")
			So(err.Error(), ShouldContainSubstring, "Channel.create")
		})

		Convey("merge emits a deprecation warning while still parsing", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("workflow { foo(ch.merge(other)) }"))
			})

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "merge")
			So(stderr, ShouldContainSubstring, "deprecated")
			So(stderr, ShouldContainSubstring, "merge")
		})

		Convey("toInteger emits a deprecation warning while still parsing", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("workflow { foo(ch.toInteger()) }"))
			})

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "toInteger")
			So(stderr, ShouldContainSubstring, "deprecated")
			So(stderr, ShouldContainSubstring, "toInteger")
		})

		Convey("DSL1-style set channel assignment reports a DSL1-only parse error", func() {
			wf, err := Parse(strings.NewReader("workflow { set { item } }"))

			So(wf, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "DSL1-only construct")
			So(err.Error(), ShouldContainSubstring, "set")
		})
	})
}

func TestParseHighPriorityOperators(t *testing.T) {
	Convey("Parse handles E1 high-priority operators", t, func() {
		Convey("branch parses as a closure operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.branch { foo: it > 5 }) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "branch")
			So(chain.Operators[0].Closure, ShouldEqual, "foo: it > 5")
		})

		Convey("combine keeps one channel argument", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.combine(other)) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "combine")
			So(chain.Operators[0].Channels, ShouldHaveLength, 1)
			So(chain.Operators[0].Channels[0].(ChanRef).Name, ShouldEqual, "other")
		})

		Convey("concat keeps multiple channel arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.concat(a, b)) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "concat")
			So(chain.Operators[0].Channels, ShouldHaveLength, 2)
			So(chain.Operators[0].Channels[0].(ChanRef).Name, ShouldEqual, "a")
			So(chain.Operators[0].Channels[1].(ChanRef).Name, ShouldEqual, "b")
		})

		Convey("set parses as a closure operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.set { result }) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "set")
			So(chain.Operators[0].Closure, ShouldEqual, "result")
		})

		Convey("view parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.view()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "view")
		})

		Convey("ifEmpty parses regular arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.ifEmpty('default')) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "ifEmpty")
			So(chain.Operators[0].Args, ShouldHaveLength, 1)
			So(chain.Operators[0].Args[0].(StringExpr).Value, ShouldEqual, "default")
		})

		Convey("splitCsv parses named arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.splitCsv(header: true)) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "splitCsv")
			So(chain.Operators[0].Args, ShouldHaveLength, 1)
		})

		Convey("transpose parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.transpose()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "transpose")
		})

		Convey("flatten parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.flatten()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "flatten")
		})

		Convey("reduce parses as a closure operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.reduce { a, b -> a + b }) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "reduce")
			So(chain.Operators[0].Closure, ShouldEqual, "a, b - > a + b")
		})

		Convey("collectFile parses named arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.collectFile(name: 'output.txt')) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "collectFile")
			So(chain.Operators[0].Args, ShouldHaveLength, 1)
		})

		Convey("tap parses as a closure operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.tap { branch_ch }) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "tap")
			So(chain.Operators[0].Closure, ShouldEqual, "branch_ch")
		})

		Convey("dump parses named arguments", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.dump(tag: 'debug')) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "dump")
			So(chain.Operators[0].Args, ShouldHaveLength, 1)
		})

		Convey("multiMap parses as a closure operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.multiMap { it -> foo: it; bar: it }) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "multiMap")
			So(chain.Operators[0].Closure, ShouldEqual, "it - > foo: it; bar: it")
		})

		Convey("unique parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.unique()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "unique")
		})

		Convey("toList parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.toList()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "toList")
		})

		Convey("count parses as a zero-arg operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.count()) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "count")
		})

		Convey("unsupported operators still return a named error", func() {
			_, err := Parse(strings.NewReader("workflow { foo(ch.bogus()) }"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unsupported operator: bogus")
		})

		Convey("remaining listed operators are accepted", func() {
			for _, source := range []string{
				"workflow { foo(ch.splitFasta()) }",
				"workflow { foo(ch.splitFastq()) }",
				"workflow { foo(ch.distinct()) }",
				"workflow { foo(ch.toSortedList()) }",
			} {
				wf, err := Parse(strings.NewReader(source))

				So(err, ShouldBeNil)
				So(wf.EntryWF.Calls, ShouldHaveLength, 1)
			}
		})

		Convey("tap also accepts channel arguments in parentheses", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(ch.tap(side)) }"))

			So(err, ShouldBeNil)
			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators[0].Name, ShouldEqual, "tap")
			So(chain.Operators[0].Channels, ShouldHaveLength, 1)
			So(chain.Operators[0].Channels[0].(ChanRef).Name, ShouldEqual, "side")
		})
	})
}

func mustChainExpr(expr ChanExpr) ChannelChain {
	chain, ok := expr.(ChannelChain)
	So(ok, ShouldBeTrue)

	return chain
}

func intExprValue(expr any) int {
	intExpr, ok := expr.(IntExpr)
	So(ok, ShouldBeTrue)

	return intExpr.Value
}

func TestParseAdditionalProcessSections(t *testing.T) {
	Convey("Parse handles A4 additional process sections", t, func() {
		Convey("stub sections are stored as raw text", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nstub: 'echo stub'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Stub, ShouldEqual, "echo stub")
			So(stderr, ShouldContainSubstring, "unsupported process section \"stub\"")
		})

		Convey("script and stub sections can coexist", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nscript: 'echo real'\nstub: 'echo stub'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Script, ShouldEqual, "echo real")
			So(wf.Processes[0].Stub, ShouldEqual, "echo stub")
			So(stderr, ShouldContainSubstring, "unsupported process section \"stub\"")
		})

		Convey("exec sections are stored as raw text", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nexec: \"println 'hello'\"\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Exec, ShouldEqual, "println 'hello'")
			So(stderr, ShouldContainSubstring, "unsupported process section \"exec\"")
		})

		Convey("shell sections are stored as raw text", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nshell: 'echo !{var}'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Shell, ShouldEqual, "echo !{var}")
			So(stderr, ShouldContainSubstring, "unsupported process section \"shell\"")
		})

		Convey("when sections are stored as raw text", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nwhen: params.run_step\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].When, ShouldEqual, "params.run_step")
			So(stderr, ShouldContainSubstring, "unsupported process section \"when\"")
		})

		Convey("bare section bodies preserve raw text and realistic punctuation", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\nstub: touch stub.txt && echo ${params.prefix}\nexec: println params.run_step ? 'go' : 'stop'\nshell: echo !{sample_id} && touch out.txt\nwhen: params.run_step && meta.id != 'skip'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Stub, ShouldEqual, "touch stub.txt && echo ${params.prefix}")
			So(wf.Processes[0].Exec, ShouldEqual, "println params.run_step ? 'go' : 'stop'")
			So(wf.Processes[0].Shell, ShouldEqual, "echo !{sample_id} && touch out.txt")
			So(wf.Processes[0].When, ShouldEqual, "params.run_step && meta.id != 'skip'")
			So(stderr, ShouldContainSubstring, "unsupported process section \"stub\"")
			So(stderr, ShouldContainSubstring, "unsupported process section \"exec\"")
			So(stderr, ShouldContainSubstring, "unsupported process section \"shell\"")
			So(stderr, ShouldContainSubstring, "unsupported process section \"when\"")
		})

		Convey("input, output, script, stub, and when sections all parse together", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\ninput:\nval sample\noutput:\npath 'out.txt'\nscript:\n'echo hello'\nstub:\ntouch stub.txt && echo ${params.prefix}\nwhen:\nparams.run_step\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Script, ShouldEqual, "echo hello")
			So(wf.Processes[0].Stub, ShouldEqual, "touch stub.txt && echo ${params.prefix}")
			So(wf.Processes[0].When, ShouldEqual, "params.run_step")
			So(stderr, ShouldContainSubstring, "unsupported process section \"stub\"")
			So(stderr, ShouldContainSubstring, "unsupported process section \"when\"")
		})
	})
}

func TestParseAdditionalIOTypesAndQualifiers(t *testing.T) {
	Convey("Parse handles A5 additional I/O types and qualifiers", t, func() {
		Convey("stdout outputs are recognised", func() {
			wf, err := Parse(strings.NewReader("process foo {\noutput:\nstdout\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Kind, ShouldEqual, "stdout")
			So(wf.Processes[0].Output[0].Name, ShouldEqual, "")
		})

		Convey("env outputs capture the variable name", func() {
			wf, err := Parse(strings.NewReader("process foo {\noutput:\nenv(MY_VAR)\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Kind, ShouldEqual, "env")
			So(wf.Processes[0].Output[0].Name, ShouldEqual, "MY_VAR")
		})

		Convey("eval outputs parse and emit a warning because translation is unsupported", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\noutput:\neval(\"hostname\")\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Kind, ShouldEqual, "eval")
			So(stderr, ShouldContainSubstring, "unsupported output type \"eval\"")
		})

		Convey("topic qualifiers are accepted and warned as non-translatable", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\noutput:\npath '*.bam', topic: 'aligned'\nscript: 'echo hello'\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Kind, ShouldEqual, "path")
			So(stderr, ShouldContainSubstring, "unsupported output qualifier \"topic\"")
		})

		Convey("emit and optional qualifiers are parsed on the same output line", func() {
			wf, err := Parse(strings.NewReader("process foo {\noutput:\npath 'out.txt', emit: result, optional: true\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Kind, ShouldEqual, "path")
			So(wf.Processes[0].Output[0].Emit, ShouldEqual, "result")
			So(wf.Processes[0].Output[0].Optional, ShouldBeTrue)
		})
	})
}

func captureParseStderr(run func()) string {
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stderr = writer
	run()
	_ = writer.Close()
	os.Stderr = original
	output, err := io.ReadAll(reader)
	if err != nil {
		panic(err)
	}
	_ = reader.Close()

	return strings.TrimSpace(string(output))
}
