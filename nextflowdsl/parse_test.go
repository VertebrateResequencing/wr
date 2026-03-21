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

		Convey("legacy DSL1 into syntax is rejected", func() {
			_, err := Parse(strings.NewReader("process foo {\noutput: stdout into result\nscript: 'echo hello'\n}"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "DSL 1")
		})

		Convey("unknown directives are ignored", func() {
			wf, err := Parse(strings.NewReader("process foo {\nscratch true\nscript: 'echo hello'\n}"))

			So(err, ShouldBeNil)
			_, ok := wf.Processes[0].Directives["scratch"]
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
	})
}

func TestParseChannelFactoriesAndOperators(t *testing.T) {
	Convey("Parse handles A3 channel factories and operators", t, func() {
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

		Convey("unsupported operators return a named error", func() {
			_, err := Parse(strings.NewReader("workflow { foo(ch.buffer(size: 3)) }"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unsupported operator: buffer")
		})
	})
}

func mustChainExpr(expr ChanExpr) ChannelChain {
	chain, ok := expr.(ChannelChain)
	So(ok, ShouldBeTrue)

	return chain
}

func intExprValue(expr Expr) int {
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
