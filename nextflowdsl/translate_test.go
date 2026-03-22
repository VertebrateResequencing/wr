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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestBuildCommandA1(t *testing.T) {
	Convey("buildCommand wraps process scripts to capture stdout and stderr", t, func() {
		Convey("it wraps a script with no input bindings", func() {
			cmd, err := buildCommand(&Process{Script: "echo hello"}, nil, nil)

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ echo hello; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it wraps a script with input bindings after exporting them", func() {
			cmd, err := buildCommand(&Process{
				Script: "cat $reads",
				Input:  []*Declaration{{Name: "reads"}},
			}, []string{"/data/a.fq"}, nil)

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ export reads='/data/a.fq'\ncat $reads; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it preserves multi-line scripts inside the wrapped group", func() {
			cmd, err := buildCommand(&Process{Script: "line1\nline2"}, nil, nil)

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ line1\nline2; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it substitutes params before wrapping the command", func() {
			cmd, err := buildCommand(&Process{Script: "echo ${params.greeting}"}, nil, map[string]any{"greeting": "hi"})

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ echo hi; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it executes indented multi-line scripts without emitting a stray terminator line", func() {
			cwd, err := os.Getwd()
			So(err, ShouldBeNil)

			tempDir := t.TempDir()
			So(os.Chdir(tempDir), ShouldBeNil)
			defer func() {
				So(os.Chdir(cwd), ShouldBeNil)
			}()

			cmd, err := buildCommand(&Process{Script: "\n    echo hello\n    "}, nil, nil)

			So(err, ShouldBeNil)
			So(exec.Command("bash", "-c", cmd).Run(), ShouldBeNil)

			stdout, err := os.ReadFile(filepath.Join(tempDir, nfStdoutFile))
			So(err, ShouldBeNil)
			So(string(stdout), ShouldEqual, "hello\n")
		})

		Convey("it interpolates process input variables before the shell executes quoted script text", func() {
			cwd, err := os.Getwd()
			So(err, ShouldBeNil)

			tempDir := t.TempDir()
			So(os.Chdir(tempDir), ShouldBeNil)
			defer func() {
				So(os.Chdir(cwd), ShouldBeNil)
			}()

			cmd, err := buildCommand(&Process{
				Script: "echo '${x} world!'",
				Input:  []*Declaration{{Name: "x", Kind: "val"}},
			}, []string{"Bonjour"}, nil)

			So(err, ShouldBeNil)
			So(exec.Command("bash", "-c", cmd).Run(), ShouldBeNil)

			stdout, err := os.ReadFile(filepath.Join(tempDir, nfStdoutFile))
			So(err, ShouldBeNil)
			So(string(stdout), ShouldEqual, "Bonjour world!\n")
		})
	})
}

func TestMatchCompletedOutputPaths(t *testing.T) {
	Convey("matchCompletedOutputPaths only falls back to basenames for simple relative patterns", t, func() {
		completed := []string{
			"/work/task/alpha/result.txt",
			"/work/task/beta/result.txt",
			"/work/task/gamma/other.txt",
		}

		Convey("simple file names still match absolute completed paths by basename", func() {
			matched := matchCompletedOutputPaths("result.txt", completed)

			So(matched, ShouldResemble, []string{
				"/work/task/alpha/result.txt",
				"/work/task/beta/result.txt",
			})
		})

		Convey("patterns with directory components do not match unrelated basenames", func() {
			matched := matchCompletedOutputPaths("alpha/result.txt", completed)

			So(matched, ShouldResemble, []string{"/work/task/alpha/result.txt"})
		})

		Convey("glob patterns with directory components still match on the full cleaned path", func() {
			matched := matchCompletedOutputPaths("/work/task/*/result.txt", completed)

			So(matched, ShouldResemble, []string{
				"/work/task/alpha/result.txt",
				"/work/task/beta/result.txt",
			})
		})
	})
}

func TestTranslateA2(t *testing.T) {
	Convey("Translate adds an on-exit cleanup behaviour for empty capture files", t, func() {
		wf := &Workflow{
			Processes: []*Process{{
				Name:   "ALIGN",
				Script: "echo hello",
			}},
			EntryWF: &WorkflowBlock{
				Calls: []*Call{{Target: "ALIGN"}},
			},
		}

		result, err := Translate(wf, nil, TranslateConfig{
			RunID:        "run123",
			WorkflowName: "main",
			Cwd:          "/tmp/workdir",
		})

		So(err, ShouldBeNil)
		So(result, ShouldNotBeNil)
		So(result.Jobs, ShouldHaveLength, 1)
		So(result.Jobs[0].Behaviours, ShouldHaveLength, 1)
		So(result.Jobs[0].Behaviours[0].When, ShouldEqual, jobqueue.OnExit)
		So(result.Jobs[0].Behaviours[0].Do, ShouldEqual, jobqueue.Run)
		So(result.Jobs[0].Behaviours[0].Arg, ShouldContainSubstring, ".nf-stdout")
		So(result.Jobs[0].Behaviours[0].Arg, ShouldContainSubstring, ".nf-stderr")
		So(result.Jobs[0].Behaviours[0].Arg, ShouldContainSubstring, "grep")
		So(result.Jobs[0].Behaviours[0].Arg, ShouldContainSubstring, "rm")
	})
}

func TestTranslateD5TaskReferences(t *testing.T) {
	Convey("Translate provides D5 task.* bindings for directive evaluation", t, func() {
		Convey("task.attempt defaults to 1 during translation", func() {
			wf := &Workflow{
				Processes: []*Process{{
					Name:       "A",
					Script:     "echo hi",
					Directives: map[string]any{"cpus": BinaryExpr{Left: VarExpr{Root: "task", Path: "attempt"}, Op: "*", Right: IntExpr{Value: 2}}},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Cores, ShouldEqual, 2)
		})

		Convey("task.cpus is available while resolving memory", func() {
			wf := &Workflow{
				Processes: []*Process{{
					Name:   "A",
					Script: "echo hi",
					Directives: map[string]any{
						"cpus":   IntExpr{Value: 4},
						"memory": BinaryExpr{Left: VarExpr{Root: "task", Path: "cpus"}, Op: "*", Right: IntExpr{Value: 1024}},
					},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Cores, ShouldEqual, 4)
			So(result.Jobs[0].Requirements.RAM, ShouldEqual, 4096)
		})

		Convey("task.memory is available while resolving later directives", func() {
			wf := &Workflow{
				Processes: []*Process{{
					Name:   "A",
					Script: "echo hi",
					Directives: map[string]any{
						"memory": IntExpr{Value: 2048},
						"disk":   BinaryExpr{Left: VarExpr{Root: "task", Path: "memory"}, Op: "/", Right: IntExpr{Value: 1024}},
					},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.RAM, ShouldEqual, 2048)
			So(result.Jobs[0].Requirements.Disk, ShouldEqual, 2)
		})
	})
}

func TestTranslateB1(t *testing.T) {
	Convey("Translate binds tuple inputs element-by-element from upstream channel items", t, func() {
		Convey("tuple val/path inputs export each element individually", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "PRODUCE",
						Script: "echo hi",
						Input: []*Declaration{
							{Kind: "val", Name: "id"},
							{Kind: "path", Name: "reads"},
						},
						Output: []*Declaration{
							{Kind: "val", Name: "id"},
							{Kind: "val", Name: "reads"},
						},
					},
					{
						Name:   "CONSUME",
						Script: "echo ${id} ${reads}",
						Input: []*Declaration{{
							Kind: "tuple",
							Elements: []*TupleElement{
								{Kind: "val", Name: "id"},
								{Kind: "path", Name: "reads"},
							},
						}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}, ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "/data/s1.fq"}}}}},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export id='s1'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export reads='/data/s1.fq'")
		})

		Convey("tuple inputs with three elements export all tuple members", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "PRODUCE",
						Script: "echo hi",
						Input: []*Declaration{
							{Kind: "val", Name: "id"},
							{Kind: "path", Name: "r1"},
							{Kind: "path", Name: "r2"},
						},
						Output: []*Declaration{
							{Kind: "val", Name: "id"},
							{Kind: "val", Name: "r1"},
							{Kind: "val", Name: "r2"},
						},
					},
					{
						Name:   "CONSUME",
						Script: "echo ${id} ${r1} ${r2}",
						Input: []*Declaration{{
							Kind: "tuple",
							Elements: []*TupleElement{
								{Kind: "val", Name: "id"},
								{Kind: "path", Name: "r1"},
								{Kind: "path", Name: "r2"},
							},
						}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE", Args: []ChanExpr{
						ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}},
						ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "/data/r1.fq"}}},
						ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "/data/r2.fq"}}},
					}},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export id='s1'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export r1='/data/r1.fq'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export r2='/data/r2.fq'")
		})

		Convey("non-tuple val inputs keep existing export behaviour", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "PRODUCE",
						Script: "echo hi",
						Input:  []*Declaration{{Kind: "val", Name: "x"}},
						Output: []*Declaration{{Kind: "val", Name: "x"}},
					},
					{
						Name:   "CONSUME",
						Script: "echo ${x}",
						Input:  []*Declaration{{Kind: "val", Name: "x"}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}}},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export x='s1'")
		})
	})
}

func TestTranslateB2(t *testing.T) {
	Convey("Translate wires tuple output path elements into downstream translation", t, func() {
		Convey("tuple outputs with resolved path elements expose those paths to downstream tuple inputs", func() {
			proc := &Process{
				Name:   "A",
				Script: "touch ${id}.bam",
				Input:  []*Declaration{{Kind: "val", Name: "id"}},
				Output: []*Declaration{{
					Kind: "tuple",
					Elements: []*TupleElement{
						{Kind: "val", Name: "id"},
						{Kind: "path", Expr: StringExpr{Value: "${id}.bam"}},
					},
				}},
			}

			jobs, stage, err := translateProcessCall(proc, &Call{Target: "A", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}}}, nil, nil, &ProcessDefaults{}, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(stage.outputPaths, ShouldResemble, []string{"/work/nf-work/r1/A/s1.bam"})

			wf := &Workflow{
				Processes: []*Process{
					proc,
					{
						Name:   "B",
						Script: "echo ${id} ${reads}",
						Input: []*Declaration{{
							Kind: "tuple",
							Elements: []*TupleElement{
								{Kind: "val", Name: "id"},
								{Kind: "path", Name: "reads"},
							},
						}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "A", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}}},
					{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export id='s1'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export reads='/work/nf-work/r1/A/s1.bam'")
		})

		Convey("tuple outputs with glob path elements keep downstream stages pending", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "A",
						Script: "touch a.bam b.bam",
						Input:  []*Declaration{{Kind: "val", Name: "id"}},
						Output: []*Declaration{{
							Kind: "tuple",
							Elements: []*TupleElement{
								{Kind: "val", Name: "id"},
								{Kind: "path", Expr: StringExpr{Value: "*.bam"}},
							},
						}},
					},
					{
						Name:   "B",
						Script: "echo ${id} ${reads}",
						Input: []*Declaration{{
							Kind: "tuple",
							Elements: []*TupleElement{
								{Kind: "val", Name: "id"},
								{Kind: "path", Name: "reads"},
							},
						}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "A", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}}},
					{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)
			So(result.Pending[0].AwaitDepGrps, ShouldResemble, []string{"nf.r1.A"})
		})

		Convey("val-only tuple outputs remain static", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "A",
						Script: "echo ${id} ${count}",
						Input: []*Declaration{
							{Kind: "val", Name: "id"},
							{Kind: "val", Name: "count"},
						},
						Output: []*Declaration{{
							Kind: "tuple",
							Elements: []*TupleElement{
								{Kind: "val", Name: "id"},
								{Kind: "val", Name: "count"},
							},
						}},
					},
					{
						Name:   "B",
						Script: "echo ${id} ${count}",
						Input: []*Declaration{{
							Kind: "tuple",
							Elements: []*TupleElement{
								{Kind: "val", Name: "id"},
								{Kind: "val", Name: "count"},
							},
						}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "A", Args: []ChanExpr{
						ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}},
						ChannelFactory{Name: "value", Args: []Expr{IntExpr{Value: 2}}},
					}},
					{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export id='s1'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export count='2'")
		})
	})
}

func TestTranslateB3(t *testing.T) {
	Convey("TranslatePending resolves tuple output path elements from completed paths", t, func() {
		Convey("tuple outputs with concrete path patterns expose resolved files to downstream tuple inputs", func() {
			produce := &Process{
				Name:   "A",
				Script: "touch ${id}.bam",
				Input:  []*Declaration{{Kind: "val", Name: "id"}},
				Output: []*Declaration{{
					Kind: "tuple",
					Elements: []*TupleElement{
						{Kind: "val", Name: "id"},
						{Kind: "path", Expr: StringExpr{Value: "${id}.bam"}},
					},
				}},
			}

			consume := &Process{
				Name:   "B",
				Script: "echo ${reads}",
				Input: []*Declaration{{
					Kind: "tuple",
					Elements: []*TupleElement{
						{Kind: "val", Name: "id"},
						{Kind: "path", Name: "reads"},
					},
				}},
			}

			_, stage, err := translateProcessCall(
				produce,
				&Call{Target: "A", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}}},
				nil,
				nil,
				&ProcessDefaults{},
				nil,
				TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
			)

			So(err, ShouldBeNil)
			stage.repGroup = "nf.wf.r1.A"

			jobs, err := TranslatePending(&PendingStage{
				Process:      consume,
				call:         &Call{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}},
				defaults:     &ProcessDefaults{},
				translated:   map[string]translatedCall{"A": stage},
				awaitRepGrps: []string{"nf.wf.r1.A"},
			}, []CompletedJob{{
				RepGrp:      "nf.wf.r1.A",
				OutputPaths: []string{"/work/s1.bam"},
				ExitCode:    0,
			}}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "export reads='/work/s1.bam'")
		})

		Convey("tuple outputs with glob path patterns expose all resolved files to downstream tuple inputs", func() {
			produce := &Process{
				Name:   "A",
				Script: "touch a.bam b.bam",
				Input:  []*Declaration{{Kind: "val", Name: "id"}},
				Output: []*Declaration{{
					Kind: "tuple",
					Elements: []*TupleElement{
						{Kind: "val", Name: "id"},
						{Kind: "path", Expr: StringExpr{Value: "*.bam"}},
					},
				}},
			}

			consume := &Process{
				Name:   "B",
				Script: "echo ${reads}",
				Input: []*Declaration{{
					Kind: "tuple",
					Elements: []*TupleElement{
						{Kind: "val", Name: "id"},
						{Kind: "path", Name: "reads"},
					},
				}},
			}

			_, stage, err := translateProcessCall(
				produce,
				&Call{Target: "A", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}}},
				nil,
				nil,
				&ProcessDefaults{},
				nil,
				TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
			)

			So(err, ShouldBeNil)
			stage.repGroup = "nf.wf.r1.A"

			jobs, err := TranslatePending(&PendingStage{
				Process:      consume,
				call:         &Call{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}},
				defaults:     &ProcessDefaults{},
				translated:   map[string]translatedCall{"A": stage},
				awaitRepGrps: []string{"nf.wf.r1.A"},
			}, []CompletedJob{{
				RepGrp:      "nf.wf.r1.A",
				OutputPaths: []string{"/work/a.bam", "/work/b.bam"},
				ExitCode:    0,
			}}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/work/a.bam")
			So(jobs[0].Cmd, ShouldContainSubstring, "/work/b.bam")
		})
	})
}

func TestTranslateD6UnsupportedCastDirectiveFallback(t *testing.T) {
	Convey("Translate falls back for directives with unsupported cast targets", t, func() {
		stderr := captureTranslateStderr(func() {
			wf := &Workflow{Processes: []*Process{{
				Name:       "proc",
				Script:     "echo hi",
				Directives: map[string]any{"cpus": CastExpr{Operand: StringExpr{Value: "4"}, TypeName: "Duration"}},
			}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "proc"}}}}

			cfg := &Config{Process: &ProcessDefaults{Cpus: 8}}
			result, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Cores, ShouldEqual, 8)
		})

		So(stderr, ShouldContainSubstring, "falling back for cpus directive with unsupported expression")
		So(stderr, ShouldContainSubstring, "as Duration")
	})
}

func TestTranslateC1(t *testing.T) {
	Convey("emit labels resolve specific process outputs", t, func() {
		Convey("translateProcessCall binds only the matching emit-labelled output for process.out.label", func() {
			produce := &Process{
				Name:   "A",
				Script: "touch sample.bam sample.bai",
				Output: []*Declaration{
					{Kind: "path", Expr: StringExpr{Value: "*.bam"}, Emit: "bam"},
					{Kind: "path", Expr: StringExpr{Value: "*.bai"}, Emit: "idx"},
				},
			}

			consume := &Process{
				Name:   "B",
				Script: "echo ${reads}",
				Input:  []*Declaration{{Kind: "path", Name: "reads"}},
			}

			_, stage, err := translateProcessCall(
				produce,
				&Call{Target: "A"},
				nil,
				nil,
				&ProcessDefaults{},
				nil,
				TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
			)

			So(err, ShouldBeNil)

			jobs, _, err := translateProcessCall(
				consume,
				&Call{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out.bam"}}},
				nil,
				map[string]translatedCall{"A": stage},
				&ProcessDefaults{},
				nil,
				TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
			)

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/work/nf-work/r1/A/*.bam")
			So(jobs[0].Cmd, ShouldNotContainSubstring, "/work/nf-work/r1/A/*.bai")
		})

		Convey("TranslatePending filters completed paths by the referenced emit label", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "A",
						Script: "touch sample.bam sample.bai",
						Output: []*Declaration{
							{Kind: "path", Expr: StringExpr{Value: "*.bam"}, Emit: "bam"},
							{Kind: "path", Expr: StringExpr{Value: "*.bai"}, Emit: "idx"},
						},
					},
					{
						Name:   "B",
						Script: "echo ${reads}",
						Input:  []*Declaration{{Kind: "path", Name: "reads"}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}, {Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out.bam"}}}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)

			jobs, err := TranslatePending(result.Pending[0], []CompletedJob{{
				RepGrp:      "nf.wf.r1.A",
				OutputPaths: []string{"/work/sample.bam", "/work/sample.bai"},
				ExitCode:    0,
			}}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "export reads='/work/sample.bam'")
			So(jobs[0].Cmd, ShouldNotContainSubstring, "/work/sample.bai")
		})

		Convey("process.out preserves full-output behaviour when no emit labels are defined", func() {
			produce := &Process{
				Name:   "A",
				Script: "touch sample.bam sample.bai",
				Output: []*Declaration{
					{Kind: "path", Expr: StringExpr{Value: "*.bam"}},
					{Kind: "path", Expr: StringExpr{Value: "*.bai"}},
				},
			}

			consume := &Process{
				Name:   "B",
				Script: "echo ${reads}",
				Input:  []*Declaration{{Kind: "path", Name: "reads"}},
			}

			_, stage, err := translateProcessCall(
				produce,
				&Call{Target: "A"},
				nil,
				nil,
				&ProcessDefaults{},
				nil,
				TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
			)

			So(err, ShouldBeNil)

			jobs, _, err := translateProcessCall(
				consume,
				&Call{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}},
				nil,
				map[string]translatedCall{"A": stage},
				&ProcessDefaults{},
				nil,
				TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
			)

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/work/nf-work/r1/A/*.bam /work/nf-work/r1/A/*.bai")
		})

		Convey("missing emit labels fall back to full outputs with a warning", func() {
			produce := &Process{
				Name:   "A",
				Script: "touch sample.bam sample.bai",
				Output: []*Declaration{
					{Kind: "path", Expr: StringExpr{Value: "*.bam"}, Emit: "bam"},
					{Kind: "path", Expr: StringExpr{Value: "*.bai"}, Emit: "idx"},
				},
			}

			consume := &Process{
				Name:   "B",
				Script: "echo ${reads}",
				Input:  []*Declaration{{Kind: "path", Name: "reads"}},
			}

			_, stage, err := translateProcessCall(
				produce,
				&Call{Target: "A"},
				nil,
				nil,
				&ProcessDefaults{},
				nil,
				TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
			)

			So(err, ShouldBeNil)

			stderr := captureTranslateStderr(func() {
				jobs, _, callErr := translateProcessCall(
					consume,
					&Call{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out.missing"}}},
					nil,
					map[string]translatedCall{"A": stage},
					&ProcessDefaults{},
					nil,
					TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
				)

				So(callErr, ShouldBeNil)
				So(jobs, ShouldHaveLength, 1)
				So(jobs[0].Cmd, ShouldContainSubstring, "/work/nf-work/r1/A/*.bam /work/nf-work/r1/A/*.bai")
			})

			So(stderr, ShouldContainSubstring, "emit label")
			So(stderr, ShouldContainSubstring, "A.out.missing")
		})
	})
}

func TestTranslate(t *testing.T) {
	Convey("Translate creates a single deterministic job for a basic process call", t, func() {
		wf := &Workflow{
			Processes: []*Process{{
				Name:   "ALIGN",
				Script: "echo hello",
				Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			}},
			EntryWF: &WorkflowBlock{
				Calls: []*Call{{Target: "ALIGN"}},
			},
		}

		result, err := Translate(wf, nil, TranslateConfig{
			RunID:        "run123",
			WorkflowName: "main",
			Cwd:          "/tmp/workdir",
		})
		So(err, ShouldBeNil)
		So(result, ShouldNotBeNil)
		So(result.Pending, ShouldBeEmpty)
		So(result.Jobs, ShouldHaveLength, 1)
		So(result.Jobs[0].Cwd, ShouldEqual, filepath.Clean("/tmp/workdir/nf-work/run123/ALIGN"))
		So(result.Jobs[0].RepGroup, ShouldEqual, "nf.main.run123.ALIGN")
		So(result.Jobs[0].DepGroups, ShouldResemble, []string{"nf.run123.ALIGN"})
	})

	Convey("Translate fans out static channel factories into indexed jobs", t, func() {
		tmpDir := t.TempDir()
		paths := []string{
			filepath.Join(tmpDir, "a.txt"),
			filepath.Join(tmpDir, "b.txt"),
		}
		for _, path := range paths {
			err := os.WriteFile(path, []byte(path), 0o644)
			So(err, ShouldBeNil)
		}

		wf := &Workflow{
			Processes: []*Process{{
				Name:   "ALIGN",
				Script: "echo $reads",
				Input:  []*Declaration{{Kind: "path", Name: "reads"}},
				Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			}},
			EntryWF: &WorkflowBlock{
				Calls: []*Call{{
					Target: "ALIGN",
					Args: []ChanExpr{ChannelFactory{
						Name: "fromPath",
						Args: []Expr{StringExpr{Value: filepath.Join(tmpDir, "*.txt")}},
					}},
				}},
			},
		}

		result, err := Translate(wf, nil, TranslateConfig{
			RunID:        "run123",
			WorkflowName: "main",
			Cwd:          tmpDir,
		})
		So(err, ShouldBeNil)
		So(result.Pending, ShouldBeEmpty)
		So(result.Jobs, ShouldHaveLength, 2)
		So(result.Jobs[0].Cwd, ShouldEqual, filepath.Clean(filepath.Join(tmpDir, "nf-work/run123/ALIGN/0")))
		So(result.Jobs[0].DepGroups, ShouldResemble, []string{"nf.run123.ALIGN.0"})
		So(result.Jobs[1].Cwd, ShouldEqual, filepath.Clean(filepath.Join(tmpDir, "nf-work/run123/ALIGN/1")))
		So(result.Jobs[1].DepGroups, ShouldResemble, []string{"nf.run123.ALIGN.1"})
		So(result.Jobs[0].RepGroup, ShouldEqual, "nf.main.run123.ALIGN")
		So(result.Jobs[1].RepGroup, ShouldEqual, "nf.main.run123.ALIGN")
		So(result.Jobs[0].Cmd, ShouldContainSubstring, "export reads=")
	})

	Convey("Translate omits jobs for empty channel factories", t, func() {
		wf := &Workflow{
			Processes: []*Process{{
				Name:   "ALIGN",
				Script: "echo hello",
			}},
			EntryWF: &WorkflowBlock{
				Calls: []*Call{{
					Target: "ALIGN",
					Args:   []ChanExpr{ChannelFactory{Name: "empty"}},
				}},
			},
		}

		result, err := Translate(wf, nil, TranslateConfig{
			RunID:        "run123",
			WorkflowName: "main",
			Cwd:          "/tmp/workdir",
		})
		So(err, ShouldBeNil)
		So(result.Jobs, ShouldBeEmpty)
		So(result.Pending, ShouldBeEmpty)
	})

	Convey("TranslatePending materializes dynamic downstream stages once outputs are known", t, func() {
		wf := &Workflow{
			Processes: []*Process{
				{
					Name:   "PRODUCE",
					Script: "touch produced.txt",
					Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "produced.txt"}}},
				},
				{
					Name:   "CONSUME",
					Script: "cat $reads",
					Input:  []*Declaration{{Kind: "path", Name: "reads"}},
					Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "consumed.txt"}}},
				},
			},
			EntryWF: &WorkflowBlock{
				Calls: []*Call{
					{Target: "PRODUCE"},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				},
			},
		}

		result, err := Translate(wf, nil, TranslateConfig{
			RunID:        "run123",
			WorkflowName: "main",
			Cwd:          "/tmp/workdir",
		})
		So(err, ShouldBeNil)
		So(result.Jobs, ShouldHaveLength, 1)
		So(result.Pending, ShouldHaveLength, 1)
		So(result.Pending[0].AwaitDepGrps, ShouldResemble, []string{"nf.run123.PRODUCE"})

		jobs, err := TranslatePending(result.Pending[0], []CompletedJob{{
			RepGrp:      "nf.main.run123.PRODUCE",
			OutputPaths: []string{"/tmp/workdir/nf-work/run123/PRODUCE/produced.txt"},
			ExitCode:    0,
		}}, TranslateConfig{Cwd: "/tmp/workdir"})
		So(err, ShouldBeNil)
		So(jobs, ShouldHaveLength, 1)
		So(jobs[0].Dependencies, ShouldNotBeNil)
		So(jobs[0].Cmd, ShouldContainSubstring, "produced.txt")
	})

	Convey("Translate covers D1 acceptance details for resources and behaviors", t, func() {
		Convey("a static A -> B pipeline produces deterministic cwd, deps, and output references", func() {
			wf := &Workflow{
				Processes: []*Process{
					{Name: "A", Script: "echo hello", Output: []*Declaration{{Kind: "val", Name: "out"}}},
					{Name: "B", Script: "echo $reads", Input: []*Declaration{{Kind: "val", Name: "reads"}}},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}, {Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs[0].DepGroups, ShouldResemble, []string{"nf.r1.A"})
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.mywf.r1.A")
			So(result.Jobs[0].ReqGroup, ShouldEqual, "nf.A")
			So(result.Jobs[0].CwdMatters, ShouldBeTrue)
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/A")
			So(result.Jobs[1].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.A"})
			So(result.Jobs[1].RepGroup, ShouldEqual, "nf.mywf.r1.B")
			So(result.Jobs[1].ReqGroup, ShouldEqual, "nf.B")
			So(result.Jobs[1].CwdMatters, ShouldBeTrue)
			So(result.Jobs[1].Cwd, ShouldEqual, "/work/nf-work/r1/B")
			So(result.Jobs[1].Cmd, ShouldNotContainSubstring, "/work/nf-work/r1/A/")
		})

		Convey("literal path outputs remain pending until upstream completion is known", func() {
			wf := &Workflow{
				Processes: []*Process{
					{Name: "A", Script: "echo hello", Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "out.txt"}}}},
					{Name: "B", Script: "cat $reads", Input: []*Declaration{{Kind: "path", Name: "reads"}}},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}, {Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.mywf.r1.A")
			So(result.Pending[0].AwaitDepGrps, ShouldResemble, []string{"nf.r1.A"})
		})

		Convey("resource directives and defaults map to requirements", func() {
			wf := &Workflow{Processes: []*Process{{Name: "A", Script: "echo hi", Directives: map[string]any{"cpus": IntExpr{Value: 4}, "memory": IntExpr{Value: 8192}, "time": IntExpr{Value: 120}, "disk": IntExpr{Value: 10}}}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}}}}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs[0].Requirements.Cores, ShouldEqual, 4)
			So(result.Jobs[0].Requirements.RAM, ShouldEqual, 8192)
			So(result.Jobs[0].Requirements.Time, ShouldEqual, 2*time.Hour)
			So(result.Jobs[0].Requirements.Disk, ShouldEqual, 10)
			So(result.Jobs[0].Override, ShouldEqual, 0)

			defaulted, err := Translate(&Workflow{Processes: []*Process{{Name: "B", Script: "echo hi"}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "B"}}}}, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(defaulted.Jobs[0].Requirements.Cores, ShouldEqual, 1)
			So(defaulted.Jobs[0].Requirements.RAM, ShouldEqual, 128)
			So(defaulted.Jobs[0].Requirements.Time, ShouldEqual, time.Hour)
			So(defaulted.Jobs[0].Requirements.Disk, ShouldEqual, 1)
			So(defaulted.Jobs[0].Override, ShouldEqual, 0)
		})

		Convey("container runtime selection and absence are translated correctly", func() {
			wf := &Workflow{Processes: []*Process{{Name: "A", Script: "echo hi", Container: "ubuntu:22.04"}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}}}}

			singularity, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", ContainerRuntime: "singularity"})
			So(err, ShouldBeNil)
			So(singularity.Jobs[0].WithSingularity, ShouldEqual, "ubuntu:22.04")
			So(singularity.Jobs[0].WithDocker, ShouldEqual, "")

			docker, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", ContainerRuntime: "docker"})
			So(err, ShouldBeNil)
			So(docker.Jobs[0].WithDocker, ShouldEqual, "ubuntu:22.04")

			bare, err := Translate(&Workflow{Processes: []*Process{{Name: "B", Script: "echo hi"}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "B"}}}}, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", ContainerRuntime: "docker"})
			So(err, ShouldBeNil)
			So(bare.Jobs[0].WithDocker, ShouldEqual, "")
			So(bare.Jobs[0].WithSingularity, ShouldEqual, "")
		})

		Convey("maxForks, errorStrategy, env, params substitution, and config defaults are applied", func() {
			stderr := captureTranslateStderr(func() {
				wf := &Workflow{Processes: []*Process{{
					Name:       "proc",
					Script:     "echo ${params.input}",
					Directives: map[string]any{"cpus": UnsupportedExpr{Text: "task.input.size() < 10 ? 1 : 4"}, "memory": IntExpr{Value: 8192}},
					MaxForks:   5,
					ErrorStrat: "finish",
					Env:        map[string]string{"MY_VAR": "hello"},
				}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "proc"}}}}

				cfg := &Config{Process: &ProcessDefaults{Cpus: 2}, Profiles: map[string]*Profile{"big": {Process: &ProcessDefaults{Cpus: 8}}}}
				result, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Params: map[string]any{"input": "/data"}, Profile: "big"})
				So(err, ShouldBeNil)
				So(result.Jobs, ShouldHaveLength, 1)
				So(result.Jobs[0].LimitGroups, ShouldResemble, []string{"proc:5"})
				So(result.Jobs[0].Retries, ShouldEqual, 0)
				So(result.Jobs[0].Requirements.Cores, ShouldEqual, 8)
				So(result.Jobs[0].Requirements.RAM, ShouldEqual, 8192)
				So(result.Jobs[0].Cmd, ShouldContainSubstring, "/data")
				So(result.Jobs[0].EnvOverride, ShouldNotBeEmpty)
				So(result.Jobs[0].Behaviours, ShouldHaveLength, 1)
			})

			So(stderr, ShouldContainSubstring, "unsupported errorStrategy \"finish\"")
			So(stderr, ShouldContainSubstring, "falling back for cpus directive with unsupported expression \"task.input.size() < 10 ? 1 : 4\"")

			retry := &Workflow{Processes: []*Process{{Name: "retry", Script: "echo hi", ErrorStrat: "retry", MaxRetries: 3}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "retry"}}}}
			retryResult, err := Translate(retry, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(retryResult.Jobs[0].Retries, ShouldEqual, 3)

			ignore := &Workflow{Processes: []*Process{{Name: "ignore", Script: "echo hi", ErrorStrat: "ignore"}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "ignore"}}}}
			ignoreResult, err := Translate(ignore, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(ignoreResult.Jobs[0].Retries, ShouldEqual, 0)
			So(ignoreResult.Jobs[0].Behaviours, ShouldHaveLength, 2)
			So(ignoreResult.Jobs[0].Behaviours[0].Do, ShouldEqual, jobqueue.Remove)

			cfgDefaults := &Config{Process: &ProcessDefaults{Cpus: 2}, Profiles: map[string]*Profile{"big": {Process: &ProcessDefaults{Cpus: 8}}}}
			defaultResult, err := Translate(&Workflow{Processes: []*Process{{Name: "defaulted", Script: "echo hi"}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "defaulted"}}}}, cfgDefaults, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(defaultResult.Jobs[0].Requirements.Cores, ShouldEqual, 2)

			profileResult, err := Translate(&Workflow{Processes: []*Process{{Name: "profiled", Script: "echo hi"}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "profiled"}}}}, cfgDefaults, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Profile: "big"})
			So(err, ShouldBeNil)
			So(profileResult.Jobs[0].Requirements.Cores, ShouldEqual, 8)
		})

		Convey("three-step and diamond DAGs wire dependencies correctly", func() {
			sequential := &Workflow{Processes: []*Process{{Name: "A", Script: "echo a", Output: []*Declaration{{Kind: "val", Name: "out"}}}, {Name: "B", Script: "echo $reads", Input: []*Declaration{{Kind: "val", Name: "reads"}}, Output: []*Declaration{{Kind: "val", Name: "out"}}}, {Name: "C", Script: "echo $reads", Input: []*Declaration{{Kind: "val", Name: "reads"}}}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}, {Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}}, {Target: "C", Args: []ChanExpr{ChanRef{Name: "B.out"}}}}}}

			result, err := Translate(sequential, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 3)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.wf.r1.A")
			So(result.Jobs[1].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.A"})
			So(result.Jobs[1].RepGroup, ShouldEqual, "nf.wf.r1.B")
			So(result.Jobs[2].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.B"})
			So(result.Jobs[2].RepGroup, ShouldEqual, "nf.wf.r1.C")

			diamond := &Workflow{Processes: []*Process{{Name: "A", Script: "echo a", Output: []*Declaration{{Kind: "val", Name: "out"}}}, {Name: "B", Script: "echo b", Output: []*Declaration{{Kind: "val", Name: "out"}}}, {Name: "C", Script: "echo $left $right", Input: []*Declaration{{Kind: "val", Name: "left"}, {Kind: "val", Name: "right"}}}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "A"}, {Target: "B"}, {Target: "C", Args: []ChanExpr{ChanRef{Name: "A.out"}, ChanRef{Name: "B.out"}}}}}}

			diamondResult, err := Translate(diamond, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(diamondResult.Jobs, ShouldHaveLength, 3)
			So(diamondResult.Pending, ShouldBeEmpty)
			So(diamondResult.Jobs[0].RepGroup, ShouldEqual, "nf.wf.r1.A")
			So(diamondResult.Jobs[1].RepGroup, ShouldEqual, "nf.wf.r1.B")
			So(diamondResult.Jobs[2].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.A", "nf.r1.B"})
			So(diamondResult.Jobs[2].Cwd, ShouldEqual, "/work/nf-work/r1/C")
		})
	})
}

func captureTranslateStderr(run func()) string {
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
