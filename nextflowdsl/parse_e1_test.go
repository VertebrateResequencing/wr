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

func TestParseL1ChannelOperators(t *testing.T) {
	Convey("Parse handles L1 recurse and times channel operators", t, func() {
		Convey("recurse parses as a supported operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(FOO.out.recurse(10)) }"))

			So(err, ShouldBeNil)

			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "recurse")
		})

		Convey("times parses as a supported operator", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(FOO.out.times(5)) }"))

			So(err, ShouldBeNil)

			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "times")
		})

		Convey("existing supported operators still parse", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(FOO.out.map { it * 2 }) }"))

			So(err, ShouldBeNil)

			chain := mustChainExpr(wf.EntryWF.Calls[0].Args[0])
			So(chain.Operators, ShouldHaveLength, 1)
			So(chain.Operators[0].Name, ShouldEqual, "map")
		})

		Convey("unsupported operators remain rejected", func() {
			_, err := Parse(strings.NewReader("workflow { foo(FOO.out.nonexistent(1)) }"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unsupported operator")
		})
	})
}

func TestParseI1DeprecatedIncludeParams(t *testing.T) {
	Convey("Parse handles I1 deprecated include params clauses", t, func() {
		Convey("addParams clauses are skipped with a deprecation warning", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("include { X } from './mod' addParams(foo: 1)"))
			})

			So(err, ShouldBeNil)
			So(wf.Imports, ShouldHaveLength, 1)
			So(wf.Imports[0].Source, ShouldEqual, "./mod")
			So(wf.Imports[0].Names, ShouldResemble, []string{"X"})
			So(stderr, ShouldContainSubstring, "deprecated")
			So(stderr, ShouldContainSubstring, "addParams")
		})

		Convey("params clauses are skipped with a deprecation warning", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("include { X } from './mod' params(bar: 2)"))
			})

			So(err, ShouldBeNil)
			So(wf.Imports, ShouldHaveLength, 1)
			So(wf.Imports[0].Source, ShouldEqual, "./mod")
			So(stderr, ShouldContainSubstring, "deprecated")
			So(stderr, ShouldContainSubstring, "params")
		})

		Convey("includes without deprecated trailing clauses preserve existing behaviour", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("include { X } from './mod'"))
			})

			So(err, ShouldBeNil)
			So(wf.Imports, ShouldHaveLength, 1)
			So(wf.Imports[0].Source, ShouldEqual, "./mod")
			So(stderr, ShouldEqual, "")
		})

		Convey("parenthesised map arguments in addParams clauses are skipped", func() {
			var (
				wf  *Workflow
				err error
			)

			wf, err = Parse(strings.NewReader("include { X } from './mod' addParams([a: 1, b: 2])"))

			So(err, ShouldBeNil)
			So(wf.Imports, ShouldHaveLength, 1)
			So(wf.Imports[0].Source, ShouldEqual, "./mod")
		})
	})
}

func TestParseG1SkipDSL1CombinedInputOutput(t *testing.T) {
	Convey("Parse handles G1 legacy DSL1 into declarations", t, func() {
		Convey("legacy into declarations warn instead of failing", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\ninput:\nval x into ch\nscript:\n'''\ntrue\n'''\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Input, ShouldHaveLength, 0)
			So(stderr, ShouldContainSubstring, "DSL 1")
			So(stderr, ShouldContainSubstring, "into")
		})

		Convey("valid declarations are preserved when a following into declaration is skipped", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\ninput:\nval x\nval y into ch\nscript:\n'''\ntrue\n'''\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Kind, ShouldEqual, "val")
			So(wf.Processes[0].Input[0].Name, ShouldEqual, "x")
			So(stderr, ShouldContainSubstring, "DSL 1")
		})

		Convey("valid declarations without into do not emit DSL1 warnings", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\ninput:\npath x\nscript:\n'''\ntrue\n'''\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Kind, ShouldEqual, "path")
			So(wf.Processes[0].Input[0].Name, ShouldEqual, "x")
			So(stderr, ShouldEqual, "")
		})

		Convey("valid declarations can still use into as a qualifier value", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("process foo {\noutput:\npath x, emit: into\nscript:\n'''\ntrue\n'''\n}"))
			})

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Output, ShouldHaveLength, 1)
			So(wf.Processes[0].Output[0].Kind, ShouldEqual, "path")
			So(wf.Processes[0].Output[0].Name, ShouldEqual, "x")
			So(wf.Processes[0].Output[0].Emit, ShouldEqual, "into")
			So(stderr, ShouldEqual, "")
		})
	})
}

func TestParseF1DeclarationQualifiers(t *testing.T) {
	Convey("Parse handles F1 path name and stageAs qualifiers", t, func() {
		Convey("simple path inputs store name qualifiers", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\npath x, name: 'reads_*.fq'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes, ShouldHaveLength, 1)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].StageName, ShouldEqual, "reads_*.fq")
		})

		Convey("simple path inputs store stageAs qualifiers", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\npath x, stageAs: 'sample.fq'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].StageAs, ShouldEqual, "sample.fq")
		})

		Convey("tuple path inputs store stageAs qualifiers on the path element", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\ntuple val(a), path(b, stageAs: 'out.txt')\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Elements, ShouldHaveLength, 2)
			So(wf.Processes[0].Input[0].Elements[1].StageAs, ShouldEqual, "out.txt")
		})

		Convey("tuple path inputs store name qualifiers on the path element", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\ntuple val(a), path(b, name: '*.bam')\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Elements, ShouldHaveLength, 2)
			So(wf.Processes[0].Input[0].Elements[1].StageName, ShouldEqual, "*.bam")
		})

		Convey("simple path inputs accept identifier name qualifiers", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\npath x, name: pattern\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].StageName, ShouldEqual, "pattern")
		})

		Convey("name qualifiers coexist with existing emit qualifiers", func() {
			wf, err := Parse(strings.NewReader("process foo {\ninput:\npath x, emit: 'y', name: 'z'\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			So(wf.Processes[0].Input, ShouldHaveLength, 1)
			So(wf.Processes[0].Input[0].Emit, ShouldEqual, "y")
			So(wf.Processes[0].Input[0].StageName, ShouldEqual, "z")
		})
	})
}

func TestParseJ1DeprecatedWorkflowLoops(t *testing.T) {
	Convey("Parse handles J1 deprecated workflow loops", t, func() {
		Convey("for loops in workflow main are skipped with a deprecation warning", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("workflow { main: for (x in [1,2,3]) { FOO(ch) } }"))
			})

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "deprecated")
			So(stderr, ShouldContainSubstring, "for")
		})

		Convey("while loops in workflow main are skipped with a deprecation warning", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("workflow { main: while (x < 3) { BAR(ch) } }"))
			})

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "deprecated")
			So(stderr, ShouldContainSubstring, "while")
		})

		Convey("workflow calls around deprecated loops are preserved while loop bodies are skipped", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			source := "workflow {\nmain:\nFOO(ch)\nfor (x in items) { BAR(ch) }\nBAZ(ch)\n}"
			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader(source))
			})

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 2)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "FOO")
			So(wf.EntryWF.Calls[1].Target, ShouldEqual, "BAZ")
			So(stderr, ShouldContainSubstring, "deprecated")
		})

		Convey("workflow main without deprecated loops keeps existing behaviour", func() {
			var (
				wf     *Workflow
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				wf, err = Parse(strings.NewReader("workflow { main: FOO(ch) }"))
			})

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "FOO")
			So(stderr, ShouldEqual, "")
		})
	})
}

func TestParseK1ParallelOperatorDesugar(t *testing.T) {
	Convey("Parse handles K1 workflow parallel operator desugaring", t, func() {
		Convey("a pipe stage with A & B fans out the same input channel", func() {
			wf, err := Parse(strings.NewReader("workflow { ch | A & B }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 2)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "A")
			So(wf.EntryWF.Calls[1].Target, ShouldEqual, "B")

			for _, call := range wf.EntryWF.Calls {
				So(call.Args, ShouldHaveLength, 1)
				input, ok := call.Args[0].(ChanRef)
				So(ok, ShouldBeTrue)
				So(input.Name, ShouldEqual, "ch")
			}
		})

		Convey("nested A & B & C stages flatten into one call per target", func() {
			wf, err := Parse(strings.NewReader("workflow { ch | A & B & C }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 3)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "A")
			So(wf.EntryWF.Calls[1].Target, ShouldEqual, "B")
			So(wf.EntryWF.Calls[2].Target, ShouldEqual, "C")

			for _, call := range wf.EntryWF.Calls {
				So(call.Args, ShouldHaveLength, 1)
				input, ok := call.Args[0].(ChanRef)
				So(ok, ShouldBeTrue)
				So(input.Name, ShouldEqual, "ch")
			}
		})

		Convey("ordinary pipe chains remain sequential", func() {
			wf, err := Parse(strings.NewReader("workflow { ch | A | B }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 2)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "A")
			So(wf.EntryWF.Calls[1].Target, ShouldEqual, "B")

			firstInput, ok := wf.EntryWF.Calls[0].Args[0].(ChanRef)
			So(ok, ShouldBeTrue)
			So(firstInput.Name, ShouldEqual, "ch")

			secondInput, ok := wf.EntryWF.Calls[1].Args[0].(ChanRef)
			So(ok, ShouldBeTrue)
			So(secondInput.Name, ShouldEqual, "A.out")
		})

		Convey("top-level A & B becomes multiple calls without explicit inputs", func() {
			wf, err := Parse(strings.NewReader("workflow { A & B }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 2)
			So(wf.EntryWF.Calls[0].Target, ShouldEqual, "A")
			So(wf.EntryWF.Calls[0].Args, ShouldBeEmpty)
			So(wf.EntryWF.Calls[1].Target, ShouldEqual, "B")
			So(wf.EntryWF.Calls[1].Args, ShouldBeEmpty)
		})
	})
}

func TestParseH1MultiAssignment(t *testing.T) {
	Convey("Parse handles H1 multi-assignment statements", t, func() {
		Convey("declared multi-assignment binds each variable", func() {
			scope := map[string]any{}

			result, err := evalStatementBody("def (a, b) = [1, 2]", scope)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{1, 2})
			So(scope, ShouldContainKey, "a")
			So(scope, ShouldContainKey, "b")
			So(scope["a"], ShouldEqual, 1)
			So(scope["b"], ShouldEqual, 2)
		})

		Convey("declared multi-assignment pads missing values with nil", func() {
			scope := map[string]any{}

			result, err := evalStatementBody("def (x, y, z) = [10, 20]", scope)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{10, 20})
			So(scope["x"], ShouldEqual, 10)
			So(scope["y"], ShouldEqual, 20)
			So(scope, ShouldContainKey, "z")
			So(scope["z"], ShouldBeNil)
		})

		Convey("existing non-declared multi-assignment remains supported", func() {
			scope := map[string]any{}

			result, err := evalStatementBody("(a, b) = [3, 4]", scope)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, []any{3, 4})
			So(scope["a"], ShouldEqual, 3)
			So(scope["b"], ShouldEqual, 4)
		})
	})
}

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
			source := "def label(x) { switch (x) { case 1: 'one'; break; " +
				"case 2: 'two'; break; default: 'other' } }"
			wf, err := Parse(strings.NewReader(source))

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
			source := "process foo {\nscript:\n\"\"\"\nfor (x in items) {\n" +
				"    println x\n}\n\"\"\"\n}"
			wf, err := Parse(strings.NewReader(source))

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
