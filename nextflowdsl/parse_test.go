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
			So(chain.Operators[0].Closure, ShouldContainSubstring, "split")
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
