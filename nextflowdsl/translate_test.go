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
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestBuildCommandA1(t *testing.T) {
	Convey("buildCommand wraps process scripts to capture stdout and stderr", t, func() {
		Convey("it wraps a script with no input bindings", func() {
			cmd, err := buildCommand(&Process{Script: "echo hello"}, nil, nil, "/work", "/work")

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ echo hello; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it wraps a script with input bindings after exporting them", func() {
			cmd, err := buildCommand(&Process{
				Script: "cat $reads",
				Input:  []*Declaration{{Name: "reads"}},
			}, []string{"/data/a.fq"}, nil, "/work", "/work")

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ export reads='/data/a.fq'\ncat $reads; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it preserves multi-line scripts inside the wrapped group", func() {
			cmd, err := buildCommand(&Process{Script: "line1\nline2"}, nil, nil, "/work", "/work")

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ line1\nline2; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it substitutes params before wrapping the command", func() {
			cmd, err := buildCommand(&Process{Script: "echo ${params.greeting}"}, nil, map[string]any{"greeting": "hi"}, "/work", "/work")

			So(err, ShouldBeNil)
			So(cmd, ShouldEqual, "{ echo hi; } > .nf-stdout 2> .nf-stderr")
		})

		Convey("it appends the first eval output capture line", func() {
			cmd, err := buildCommand(&Process{
				Script: "echo hello",
				Output: []*Declaration{{Kind: "eval", Expr: StringExpr{Value: "hostname"}}},
			}, nil, nil, "/work", "/work")

			So(err, ShouldBeNil)
			So(cmd, ShouldContainSubstring, "echo hello\n__nf_eval_0=$(hostname)")
		})

		Convey("it appends later eval outputs with incremented variable names", func() {
			cmd, err := buildCommand(&Process{
				Script: "echo hello",
				Output: []*Declaration{
					{Kind: "eval", Expr: StringExpr{Value: "hostname"}},
					{Kind: "eval", Expr: StringExpr{Value: "wc -l < input.txt"}},
				},
			}, nil, nil, "/work", "/work")

			So(err, ShouldBeNil)
			So(cmd, ShouldContainSubstring, "__nf_eval_1=$(wc -l < input.txt)")
		})

		Convey("it executes indented multi-line scripts without emitting a stray terminator line", func() {
			cwd, err := os.Getwd()
			So(err, ShouldBeNil)

			tempDir := t.TempDir()
			So(os.Chdir(tempDir), ShouldBeNil)
			defer func() {
				So(os.Chdir(cwd), ShouldBeNil)
			}()

			cmd, err := buildCommand(&Process{Script: "\n    echo hello\n    "}, nil, nil, tempDir, tempDir)

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
			}, []string{"Bonjour"}, nil, tempDir, tempDir)

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

func TestTranslateA2DirectiveWrappers(t *testing.T) {
	Convey("Translate applies A2 scratch, storeDir, conda, and spack directive wrappers", t, func() {
		translateJob := func(proc *Process) *jobqueue.Job {
			wf := &Workflow{
				Processes: []*Process{proc},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: proc.Name}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)

			return result.Jobs[0]
		}

		Convey("scratch true creates a temp directory, runs there, and copies outputs back", func() {
			job := translateJob(&Process{
				Name:       "ALIGN",
				Script:     "touch result.txt",
				Directives: map[string]any{"scratch": BoolExpr{Value: true}},
				Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			})

			So(job.Cmd, ShouldContainSubstring, "nf_scratch_dir=$(mktemp -d)")
			So(job.Cmd, ShouldContainSubstring, "cd \"$nf_scratch_dir\"")
			So(job.Cmd, ShouldContainSubstring, "touch result.txt")
			So(job.Cmd, ShouldContainSubstring, "cp -rf -- \"$nf_scratch_dir/result.txt\" \"$nf_orig_dir\"")
			So(job.Cmd, ShouldNotContainSubstring, "shopt -s nullglob")
		})

		Convey("scratch with a literal path uses that directory", func() {
			job := translateJob(&Process{
				Name:       "ALIGN",
				Script:     "touch result.txt",
				Directives: map[string]any{"scratch": StringExpr{Value: "/tmp/work"}},
				Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			})

			So(job.Cmd, ShouldContainSubstring, "nf_scratch_dir='/tmp/work'")
			So(job.Cmd, ShouldContainSubstring, "mkdir -p \"$nf_scratch_dir\"")
		})

		Convey("scratch false leaves the command unwrapped", func() {
			plain := translateJob(&Process{Name: "ALIGN", Script: "echo hello"})
			withFalse := translateJob(&Process{
				Name:       "ALIGN",
				Script:     "echo hello",
				Directives: map[string]any{"scratch": BoolExpr{Value: false}},
			})

			So(withFalse.Cmd, ShouldEqual, plain.Cmd)
		})

		Convey("storeDir checks for cached outputs and skips execution when present", func() {
			job := translateJob(&Process{
				Name:       "CACHE",
				Script:     "touch result.txt",
				Directives: map[string]any{"storeDir": StringExpr{Value: "/data/cache"}},
				Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			})

			So(job.Cmd, ShouldContainSubstring, "if [ -e '/data/cache/result.txt' ]; then")
			So(job.Cmd, ShouldContainSubstring, "cp -rf -- '/data/cache/result.txt' '/work/nf-work/r1/CACHE/'")
			So(job.Cmd, ShouldNotContainSubstring, "compgen -G")
		})

		Convey("storeDir cache hits do not require conda or spack setup", func() {
			job := translateJob(&Process{
				Name:   "CACHEENV",
				Script: "touch result.txt",
				Directives: map[string]any{
					"storeDir": StringExpr{Value: "/data/cache"},
					"conda":    StringExpr{Value: "samtools=1.17"},
					"spack":    StringExpr{Value: "samtools@1.17"},
				},
				Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			})

			So(job.Cmd, ShouldContainSubstring, "if [ -e '/data/cache/result.txt' ]; then")
			So(job.Cmd, ShouldContainSubstring, "else\nconda activate samtools=1.17 && spack load samtools@1.17 &&")
			So(job.Cmd, ShouldNotContainSubstring, "conda activate samtools=1.17 && if [ -e '/data/cache/result.txt' ]; then")
		})

		Convey("scratch and storeDir glob wrappers avoid bash-only builtins", func() {
			job := translateJob(&Process{
				Name:   "CACHE",
				Script: `touch a.txt b.txt`,
				Directives: map[string]any{
					"scratch":  BoolExpr{Value: true},
					"storeDir": StringExpr{Value: "/data/cache"},
				},
				Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "*.txt"}}},
			})

			So(job.Cmd, ShouldContainSubstring, `set -- $nf_scratch_dir/*.txt`)
			So(job.Cmd, ShouldContainSubstring, `set -- /data/cache/*.txt`)
			So(job.Cmd, ShouldNotContainSubstring, "shopt -s nullglob")
			So(job.Cmd, ShouldNotContainSubstring, "compgen -G")
		})

		Convey("storeDir copies outputs into the cache after a normal run", func() {
			job := translateJob(&Process{
				Name:       "CACHE",
				Script:     "touch result.txt",
				Directives: map[string]any{"storeDir": StringExpr{Value: "/data/cache"}},
				Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			})

			So(job.Cmd, ShouldContainSubstring, "touch result.txt")
			So(job.Cmd, ShouldContainSubstring, "cp -rf -- '/work/nf-work/r1/CACHE/result.txt' '/data/cache/'")
		})

		Convey("relative storeDir paths resolve from the workflow launch directory", func() {
			job := translateJob(&Process{
				Name:       "CACHE",
				Script:     "touch result.txt",
				Directives: map[string]any{"storeDir": StringExpr{Value: "cache/results"}},
				Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			})

			So(job.Cmd, ShouldContainSubstring, "mkdir -p '/work/cache/results/'")
			So(job.Cmd, ShouldContainSubstring, "if [ -e '/work/cache/results/result.txt' ]; then")
			So(job.Cmd, ShouldContainSubstring, "cp -rf -- '/work/nf-work/r1/CACHE/result.txt' '/work/cache/results/'")
			So(job.Cmd, ShouldNotContainSubstring, "/work/nf-work/r1/CACHE/cache/results")
		})

		Convey("conda prepends environment activation", func() {
			job := translateJob(&Process{
				Name:       "CONDA",
				Script:     "echo hello",
				Directives: map[string]any{"conda": StringExpr{Value: "samtools=1.17"}},
			})

			So(job.Cmd, ShouldStartWith, "conda activate samtools=1.17 &&")
		})

		Convey("spack prepends environment loading", func() {
			job := translateJob(&Process{
				Name:       "SPACK",
				Script:     "echo hello",
				Directives: map[string]any{"spack": StringExpr{Value: "samtools@1.17"}},
			})

			So(job.Cmd, ShouldStartWith, "spack load samtools@1.17 &&")
		})

		Convey("conda precedes the scratch wrapper when both directives are present", func() {
			job := translateJob(&Process{
				Name:   "COMBINED",
				Script: "touch result.txt",
				Directives: map[string]any{
					"conda":   StringExpr{Value: "samtools=1.17"},
					"scratch": BoolExpr{Value: true},
				},
				Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
			})

			condaIndex := strings.Index(job.Cmd, "conda activate samtools=1.17")
			scratchIndex := strings.Index(job.Cmd, "nf_scratch_dir=$(mktemp -d)")
			So(condaIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(scratchIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(condaIndex, ShouldBeLessThan, scratchIndex)
		})
	})
}

func TestTranslateN1DynamicDirectiveClosures(t *testing.T) {
	Convey("Translate evaluates N1 dynamic directive closures with task defaults", t, func() {
		translateSingleJob := func(proc *Process, params map[string]any, cfg TranslateConfig) *jobqueue.Job {
			cfg.Params = params

			wf := &Workflow{
				Processes: []*Process{proc},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: proc.Name}}},
			}

			result, err := Translate(wf, nil, cfg)
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)

			return result.Jobs[0]
		}

		baseConfig := TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"}

		Convey("memory closures use the default task.attempt value", func() {
			job := translateSingleJob(&Process{
				Name:   "A",
				Script: "echo hi",
				Directives: map[string]any{
					"memory": ClosureExpr{Body: "2048 * task.attempt"},
				},
			}, nil, baseConfig)

			So(job.Requirements.RAM, ShouldEqual, 2048)
		})

		Convey("cpus closures fall back to params defaults through elvis evaluation", func() {
			job := translateSingleJob(&Process{
				Name:   "A",
				Script: "echo hi",
				Directives: map[string]any{
					"cpus": ClosureExpr{Body: "params.cpus ?: 4"},
				},
			}, map[string]any{}, baseConfig)

			So(job.Requirements.Cores, ShouldEqual, 4)
		})

		Convey("errorStrategy closures resolve against the default task.exitStatus", func() {
			strategy, err := resolveDirectiveString(
				"errorStrategy",
				ClosureExpr{Body: "task.exitStatus in [137, 140] ? 'retry' : 'terminate'"},
				map[string]any{},
				"",
				defaultDirectiveTask(),
			)

			So(err, ShouldBeNil)
			So(strategy, ShouldEqual, "terminate")
		})

		Convey("non-closure directives preserve existing integer resolution", func() {
			job := translateSingleJob(&Process{
				Name:   "A",
				Script: "echo hi",
				Directives: map[string]any{
					"cpus": IntExpr{Value: 4},
				},
			}, nil, baseConfig)

			So(job.Requirements.Cores, ShouldEqual, 4)
		})

		Convey("container closures interpolate task defaults during translation", func() {
			job := translateSingleJob(&Process{
				Name:   "A",
				Script: "echo hi",
				Directives: map[string]any{
					"container": ClosureExpr{Body: `"image:${task.attempt}"`},
				},
			}, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", ContainerRuntime: "docker"})

			So(job.WithDocker, ShouldEqual, "image:1")
		})

		Convey("cpus closures can reference the default task.cpus value", func() {
			job := translateSingleJob(&Process{
				Name:   "A",
				Script: "echo hi",
				Directives: map[string]any{
					"cpus": ClosureExpr{Body: "task.cpus * 2"},
				},
			}, nil, baseConfig)

			So(job.Requirements.Cores, ShouldEqual, 2)
		})
	})
}

func TestTranslateTupleOutputPathBinding(t *testing.T) {
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

			jobs, stage, err := translateProcessCall(proc, &Call{Target: "A", Args: []ChanExpr{ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "s1"}}}}}, nil, nil, &ProcessDefaults{}, nil, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

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

func TestTranslateI2(t *testing.T) {
	Convey("Translate creates workflow lifecycle hook jobs for I2", t, func() {
		findJobBySuffix := func(jobs []*jobqueue.Job, suffix string) *jobqueue.Job {
			for _, job := range jobs {
				if job != nil && strings.HasSuffix(job.RepGroup, suffix) {
					return job
				}
			}

			return nil
		}

		wf := &Workflow{
			Processes: []*Process{
				{Name: "A", Script: "echo a"},
				{Name: "B", Script: "echo b"},
			},
			SubWFs: []*SubWorkflow{{
				Name: "FLOW",
				Body: &WorkflowBlock{
					Calls:      []*Call{{Target: "A"}, {Target: "B"}},
					OnComplete: "println 'workflow done'",
					OnError:    "println 'workflow failed'",
				},
			}},
			EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "FLOW"}}},
		}

		result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

		So(err, ShouldBeNil)
		So(result.Pending, ShouldBeEmpty)
		So(result.Jobs, ShouldHaveLength, 4)

		onCompleteJob := findJobBySuffix(result.Jobs, ".FLOW.onComplete")
		So(onCompleteJob, ShouldNotBeNil)
		So(onCompleteJob.Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.FLOW.A", "nf.r1.FLOW.B"})
		So(onCompleteJob.Cmd, ShouldContainSubstring, "echo 'workflow done'")

		onErrorJob := findJobBySuffix(result.Jobs, ".FLOW.onError")
		So(onErrorJob, ShouldNotBeNil)
		So(onErrorJob.LimitGroups, ShouldBeEmpty)
		So(onErrorJob.Cmd, ShouldContainSubstring, "wr status -i 'nf.wf.r1.FLOW.A' -o plain")
		So(onErrorJob.Cmd, ShouldContainSubstring, "wr status -i 'nf.wf.r1.FLOW.B' -o plain")
		So(onErrorJob.Cmd, ShouldContainSubstring, `$2 == "buried" || $2 == "lost"`)
		So(onErrorJob.Cmd, ShouldContainSubstring, `$2 != "buried" && $2 != "complete" && $2 != "lost"`)
		So(onErrorJob.Cmd, ShouldContainSubstring, "echo 'workflow failed'")
		So(onErrorJob.Cmd, ShouldContainSubstring, "wr add")
		So(onErrorJob.Cmd, ShouldContainSubstring, `next_limit=$(date -d '+1 minute' '+datetime < %Y-%m-%d %H:%M:%S')`)
		So(onErrorJob.Cmd, ShouldNotContainSubstring, "wr status --running")

		Convey("parent workflow onError watches all rep groups from called subworkflows", func() {
			wf := &Workflow{
				Processes: []*Process{
					{Name: "A", Script: "echo a", Output: []*Declaration{{Kind: "val", Name: "out"}}},
					{Name: "B", Script: "echo b", Input: []*Declaration{{Kind: "val", Name: "in"}}, Output: []*Declaration{{Kind: "val", Name: "out"}}},
				},
				SubWFs: []*SubWorkflow{
					{
						Name: "CHILD",
						Body: &WorkflowBlock{
							Calls: []*Call{
								{Target: "A"},
								{Target: "B", Args: []ChanExpr{ChanRef{Name: "A.out"}}},
							},
							Emit: []*WFEmit{{Name: "out", Expr: "B.out"}},
						},
					},
					{
						Name: "PARENT",
						Body: &WorkflowBlock{
							Calls:   []*Call{{Target: "CHILD"}},
							OnError: "println 'parent failed'",
						},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "PARENT"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)

			onErrorJob := findJobBySuffix(result.Jobs, ".PARENT.onError")
			So(onErrorJob, ShouldNotBeNil)
			So(onErrorJob.Cmd, ShouldContainSubstring, "wr status -i 'nf.wf.r1.PARENT.CHILD.A' -o plain")
			So(onErrorJob.Cmd, ShouldContainSubstring, "wr status -i 'nf.wf.r1.PARENT.CHILD.B' -o plain")
			So(onErrorJob.Cmd, ShouldContainSubstring, "echo 'parent failed'")
		})
	})
}

func TestTranslateA3(t *testing.T) {
	Convey("Translate applies A3 and L1 selector defaults in specificity order", t, func() {
		translateJob := func(proc *Process, cfg *Config) *jobqueue.Job {
			wf := &Workflow{
				Processes: []*Process{proc},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: proc.Name}}},
			}

			result, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)

			return result.Jobs[0]
		}

		Convey("matching withLabel overrides generic defaults", func() {
			job := translateJob(&Process{Name: "ALIGN", Labels: []string{"big_mem"}, Script: "echo hi"}, &Config{
				Process: &ProcessDefaults{Cpus: 1},
				Selectors: []*ProcessSelector{{
					Kind:     "withLabel",
					Pattern:  "big_mem",
					Settings: &ProcessDefaults{Cpus: 8},
				}},
			})

			So(job.Requirements.Cores, ShouldEqual, 8)
		})

		Convey("withName overrides withLabel", func() {
			job := translateJob(&Process{Name: "ALIGN", Labels: []string{"big_mem"}, Script: "echo hi"}, &Config{
				Selectors: []*ProcessSelector{
					{Kind: "withLabel", Pattern: "big_mem", Settings: &ProcessDefaults{Cpus: 8}},
					{Kind: "withName", Pattern: "ALIGN", Settings: &ProcessDefaults{Cpus: 16}},
				},
			})

			So(job.Requirements.Cores, ShouldEqual, 16)
		})

		Convey("process-level directives override withName", func() {
			job := translateJob(&Process{
				Name:       "ALIGN",
				Labels:     []string{"big_mem"},
				Script:     "echo hi",
				Directives: map[string]any{"cpus": IntExpr{Value: 32}},
			}, &Config{
				Selectors: []*ProcessSelector{{Kind: "withName", Pattern: "ALIGN", Settings: &ProcessDefaults{Cpus: 16}}},
			})

			So(job.Requirements.Cores, ShouldEqual, 32)
		})

		Convey("withName supports glob matching", func() {
			job := translateJob(&Process{Name: "ALIGN_BWA", Script: "echo hi"}, &Config{
				Selectors: []*ProcessSelector{{Kind: "withName", Pattern: "ALIGN*", Settings: &ProcessDefaults{Cpus: 4}}},
			})

			So(job.Requirements.Cores, ShouldEqual, 4)
		})

		Convey("withName supports regex matching", func() {
			job := translateJob(&Process{Name: "ALIGN_BWA", Script: "echo hi"}, &Config{
				Selectors: []*ProcessSelector{{Kind: "withName", Pattern: "~ALIGN.*", Settings: &ProcessDefaults{Cpus: 4}}},
			})

			So(job.Requirements.Cores, ShouldEqual, 4)
		})

		Convey("generic defaults apply when selectors do not match", func() {
			job := translateJob(&Process{Name: "FOO", Labels: []string{"small"}, Script: "echo hi"}, &Config{
				Process:   &ProcessDefaults{Cpus: 2},
				Selectors: []*ProcessSelector{{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Cpus: 16}}},
			})

			So(job.Requirements.Cores, ShouldEqual, 2)
		})

		Convey("last matching selector wins within the same specificity", func() {
			job := translateJob(&Process{Name: "FOO", Labels: []string{"big"}, Script: "echo hi"}, &Config{
				Selectors: []*ProcessSelector{
					{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Cpus: 8}},
					{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Cpus: 12}},
				},
			})

			So(job.Requirements.Cores, ShouldEqual, 12)
		})

		Convey("selectors merge field-by-field with process directives", func() {
			job := translateJob(&Process{
				Name:       "FOO",
				Labels:     []string{"big"},
				Script:     "echo hi",
				Directives: map[string]any{"cpus": IntExpr{Value: 4}},
			}, &Config{
				Selectors: []*ProcessSelector{{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Memory: 65536}}},
			})

			So(job.Requirements.Cores, ShouldEqual, 4)
			So(job.Requirements.RAM, ShouldEqual, 65536)
		})

		Convey("nested selectors match when both conditions match", func() {
			job := translateJob(&Process{Name: "ALIGN", Labels: []string{"big"}, Script: "echo hi"}, &Config{
				Process: &ProcessDefaults{Cpus: 2},
				Selectors: []*ProcessSelector{{
					Kind:    "withLabel",
					Pattern: "big",
					Inner: &ProcessSelector{
						Kind:     "withName",
						Pattern:  "ALIGN",
						Settings: &ProcessDefaults{Cpus: 32},
					},
				}},
			})

			So(job.Requirements.Cores, ShouldEqual, 32)
		})

		Convey("nested selectors do not match when the inner selector misses", func() {
			job := translateJob(&Process{Name: "SORT", Labels: []string{"big"}, Script: "echo hi"}, &Config{
				Process: &ProcessDefaults{Cpus: 2},
				Selectors: []*ProcessSelector{{
					Kind:    "withLabel",
					Pattern: "big",
					Inner: &ProcessSelector{
						Kind:     "withName",
						Pattern:  "ALIGN",
						Settings: &ProcessDefaults{Cpus: 32},
					},
				}},
			})

			So(job.Requirements.Cores, ShouldEqual, 2)
		})

		Convey("selected profiles append their selectors after global selectors", func() {
			job := translateJob(&Process{Name: "ALIGN", Labels: []string{"big"}, Script: "echo hi"}, &Config{
				Selectors: []*ProcessSelector{{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Cpus: 8}}},
				Profiles: map[string]*Profile{
					"test": {
						Selectors: []*ProcessSelector{{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Cpus: 16}}},
					},
				},
			})

			result, err := Translate(&Workflow{
				Processes: []*Process{{Name: "ALIGN", Labels: []string{"big"}, Script: "echo hi"}},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "ALIGN"}}},
			}, &Config{
				Selectors: []*ProcessSelector{{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Cpus: 8}}},
				Profiles: map[string]*Profile{
					"test": {
						Selectors: []*ProcessSelector{{Kind: "withLabel", Pattern: "big", Settings: &ProcessDefaults{Cpus: 16}}},
					},
				},
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Profile: "test"})
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Cores, ShouldEqual, 16)

			So(job.Requirements.Cores, ShouldEqual, 8)
		})
	})
}

func TestTranslateF2(t *testing.T) {
	Convey("Translate wraps process commands with beforeScript and afterScript", t, func() {
		translateJob := func(proc *Process, tc TranslateConfig) *jobqueue.Job {
			wf := &Workflow{
				Processes: []*Process{proc},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: proc.Name}}},
			}

			result, err := Translate(wf, nil, tc)
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)

			return result.Jobs[0]
		}

		Convey("beforeScript runs before the process script", func() {
			job := translateJob(&Process{
				Name:         "ALIGN",
				BeforeScript: "module load samtools",
				Script:       "samtools sort input.bam",
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			beforeIndex := strings.Index(job.Cmd, "module load samtools")
			scriptIndex := strings.Index(job.Cmd, "samtools sort input.bam")
			So(beforeIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(scriptIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(beforeIndex, ShouldBeLessThan, scriptIndex)
		})

		Convey("afterScript runs after the process script", func() {
			job := translateJob(&Process{
				Name:        "RUN",
				Script:      "run.sh",
				AfterScript: "cleanup.sh",
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			scriptIndex := strings.Index(job.Cmd, "run.sh")
			afterIndex := strings.Index(job.Cmd, "cleanup.sh")
			So(scriptIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(afterIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(scriptIndex, ShouldBeLessThan, afterIndex)
		})

		Convey("beforeScript and afterScript wrap the script as one containerized command block", func() {
			job := translateJob(&Process{
				Name:         "WRAP",
				Container:    "ubuntu:22.04",
				BeforeScript: "setup.sh",
				Script:       "main.sh",
				AfterScript:  "teardown.sh",
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", ContainerRuntime: "docker"})

			beforeIndex := strings.Index(job.Cmd, "setup.sh")
			scriptIndex := strings.Index(job.Cmd, "main.sh")
			afterIndex := strings.Index(job.Cmd, "teardown.sh")
			So(beforeIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(scriptIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(afterIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(beforeIndex, ShouldBeLessThan, scriptIndex)
			So(scriptIndex, ShouldBeLessThan, afterIndex)
			So(job.Cmd, ShouldStartWith, "{ ")
			So(job.Cmd, ShouldEndWith, " > .nf-stdout 2> .nf-stderr")
			So(job.WithDocker, ShouldEqual, "ubuntu:22.04")
		})

		Convey("existing command behaviour is preserved when neither directive is set", func() {
			job := translateJob(&Process{
				Name:   "PLAIN",
				Script: "echo hello",
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(job.Cmd, ShouldEqual, "{ echo hello; } > .nf-stdout 2> .nf-stderr")
		})
	})
}

func TestTranslateF3(t *testing.T) {
	Convey("Translate prepends module directives to process commands", t, func() {
		translateJob := func(proc *Process, tc TranslateConfig) *jobqueue.Job {
			wf := &Workflow{
				Processes: []*Process{proc},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: proc.Name}}},
			}

			result, err := Translate(wf, nil, tc)
			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)

			return result.Jobs[0]
		}

		Convey("single module directives are loaded before beforeScript and the process script", func() {
			job := translateJob(&Process{
				Name:         "ALIGN",
				Module:       "samtools/1.17",
				BeforeScript: "setup.sh",
				Script:       "samtools sort input.bam",
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(job.Cmd, ShouldStartWith, "{ module load samtools/1.17\n")

			moduleIndex := strings.Index(job.Cmd, "module load samtools/1.17")
			beforeIndex := strings.Index(job.Cmd, "setup.sh")
			scriptIndex := strings.Index(job.Cmd, "samtools sort input.bam")
			So(moduleIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(beforeIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(scriptIndex, ShouldBeGreaterThanOrEqualTo, 0)
			So(moduleIndex, ShouldBeLessThan, beforeIndex)
			So(beforeIndex, ShouldBeLessThan, scriptIndex)
		})

		Convey("colon-separated module directives expand to one module load per line in declaration order", func() {
			job := translateJob(&Process{
				Name:   "ALIGN",
				Module: "samtools/1.17:bwa/0.7.17",
				Script: "bwa mem ref.fa reads.fq",
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(job.Cmd, ShouldStartWith, "{ module load samtools/1.17\nmodule load bwa/0.7.17\n")
			So(job.Cmd, ShouldContainSubstring, "module load samtools/1.17\nmodule load bwa/0.7.17\nbwa mem ref.fa reads.fq")
		})

		Convey("commands without a module directive do not gain module load lines", func() {
			job := translateJob(&Process{
				Name:   "PLAIN",
				Script: "echo hello",
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(job.Cmd, ShouldNotContainSubstring, "module load ")
		})
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
	Convey("Translate expands each inputs as a Cartesian product", t, func() {
		Convey("regular inputs crossed with one each input create N x M jobs with unique CWDs and a shared dep group", func() {
			wf := &Workflow{
				Processes: []*Process{{
					Name:   "ALIGN",
					Script: "echo ${id} ${mode}",
					Input: []*Declaration{
						{Kind: "val", Name: "id"},
						{Kind: "val", Name: "mode", Each: true},
					},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{
					Target: "ALIGN",
					Args: []ChanExpr{
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "A"}, StringExpr{Value: "B"}}},
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "x"}, StringExpr{Value: "y"}, StringExpr{Value: "z"}}},
					},
				}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 6)
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/0_0")
			So(result.Jobs[1].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/0_1")
			So(result.Jobs[2].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/0_2")
			So(result.Jobs[3].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/1_0")
			So(result.Jobs[4].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/1_1")
			So(result.Jobs[5].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/1_2")
			for _, job := range result.Jobs {
				So(job.DepGroups, ShouldResemble, []string{"nf.r1.ALIGN"})
			}
		})

		Convey("job commands export every regular and each-input combination", func() {
			wf := &Workflow{
				Processes: []*Process{{
					Name:   "ALIGN",
					Script: "echo ${id} ${mode}",
					Input: []*Declaration{
						{Kind: "val", Name: "id"},
						{Kind: "val", Name: "mode", Each: true},
					},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{
					Target: "ALIGN",
					Args: []ChanExpr{
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "A"}, StringExpr{Value: "B"}}},
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "x"}, StringExpr{Value: "y"}, StringExpr{Value: "z"}}},
					},
				}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 6)
			So(result.Jobs[0].Cmd, ShouldContainSubstring, "export id='A'")
			So(result.Jobs[0].Cmd, ShouldContainSubstring, "export mode='x'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export id='A'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export mode='y'")
			So(result.Jobs[2].Cmd, ShouldContainSubstring, "export id='A'")
			So(result.Jobs[2].Cmd, ShouldContainSubstring, "export mode='z'")
			So(result.Jobs[3].Cmd, ShouldContainSubstring, "export id='B'")
			So(result.Jobs[3].Cmd, ShouldContainSubstring, "export mode='x'")
			So(result.Jobs[4].Cmd, ShouldContainSubstring, "export id='B'")
			So(result.Jobs[4].Cmd, ShouldContainSubstring, "export mode='y'")
			So(result.Jobs[5].Cmd, ShouldContainSubstring, "export id='B'")
			So(result.Jobs[5].Cmd, ShouldContainSubstring, "export mode='z'")
		})

		Convey("processes without each inputs preserve existing zip-style behaviour", func() {
			wf := &Workflow{
				Processes: []*Process{{
					Name:   "ALIGN",
					Script: "echo ${id} ${mode}",
					Input: []*Declaration{
						{Kind: "val", Name: "id"},
						{Kind: "val", Name: "mode"},
					},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{
					Target: "ALIGN",
					Args: []ChanExpr{
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "A"}, StringExpr{Value: "B"}}},
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "x"}, StringExpr{Value: "y"}}},
					},
				}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/0")
			So(result.Jobs[0].DepGroups, ShouldResemble, []string{"nf.r1.ALIGN.0"})
			So(result.Jobs[0].Cmd, ShouldContainSubstring, "export id='A'")
			So(result.Jobs[0].Cmd, ShouldContainSubstring, "export mode='x'")
			So(result.Jobs[1].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/1")
			So(result.Jobs[1].DepGroups, ShouldResemble, []string{"nf.r1.ALIGN.1"})
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export id='B'")
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export mode='y'")
		})

		Convey("multiple each inputs multiply together for each regular input item", func() {
			wf := &Workflow{
				Processes: []*Process{{
					Name:   "ALIGN",
					Script: "echo ${id} ${m1} ${m2}",
					Input: []*Declaration{
						{Kind: "val", Name: "id"},
						{Kind: "val", Name: "m1", Each: true},
						{Kind: "val", Name: "m2", Each: true},
					},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{
					Target: "ALIGN",
					Args: []ChanExpr{
						ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "A"}}},
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "x"}, StringExpr{Value: "y"}}},
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "1"}, StringExpr{Value: "2"}, StringExpr{Value: "3"}}},
					},
				}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 6)
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/0_0")
			So(result.Jobs[5].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/0_5")
			So(result.Jobs[0].Cmd, ShouldContainSubstring, "export m1='x'")
			So(result.Jobs[0].Cmd, ShouldContainSubstring, "export m2='1'")
			So(result.Jobs[5].Cmd, ShouldContainSubstring, "export m1='y'")
			So(result.Jobs[5].Cmd, ShouldContainSubstring, "export m2='3'")
			for _, job := range result.Jobs {
				So(job.DepGroups, ShouldResemble, []string{"nf.r1.ALIGN"})
			}
		})
	})
}

func TestTranslateO1(t *testing.T) {
	Convey("Translate applies O1 cross-product CWD and dependency structure", t, func() {
		newCrossProductWorkflow := func() *Workflow {
			return &Workflow{
				Processes: []*Process{{
					Name:   "FOO",
					Script: "echo ${id} ${mode}",
					Input: []*Declaration{
						{Kind: "val", Name: "id"},
						{Kind: "val", Name: "mode", Each: true},
					},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{
					Target: "FOO",
					Args: []ChanExpr{
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "A"}, StringExpr{Value: "B"}}},
						ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "x"}, StringExpr{Value: "y"}}},
					},
				}}},
			}
		}

		Convey("cross-product jobs use the {regIdx}_{eachIdx} CWD suffix", func() {
			result, err := Translate(newCrossProductWorkflow(), nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 4)
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/FOO/0_0")
			So(result.Jobs[1].Cwd, ShouldEqual, "/work/nf-work/r1/FOO/0_1")
			So(result.Jobs[2].Cwd, ShouldEqual, "/work/nf-work/r1/FOO/1_0")
			So(result.Jobs[3].Cwd, ShouldEqual, "/work/nf-work/r1/FOO/1_1")
		})

		Convey("all cross-product jobs share the same dep group", func() {
			result, err := Translate(newCrossProductWorkflow(), nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 4)
			for _, job := range result.Jobs {
				So(job.DepGroups, ShouldResemble, []string{"nf.r1.FOO"})
			}
		})

		Convey("downstream jobs depend on the shared cross-product dep group", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "FOO",
						Script: "echo ${id}_${mode}",
						Input: []*Declaration{
							{Kind: "val", Name: "id"},
							{Kind: "val", Name: "mode", Each: true},
						},
						Output: []*Declaration{{Kind: "val", Name: "pair", Expr: StringExpr{Value: "${id}_${mode}"}}},
					},
					{
						Name:   "BAR",
						Script: "echo ${pair}",
						Input:  []*Declaration{{Kind: "val", Name: "pair"}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{
						Target: "FOO",
						Args: []ChanExpr{
							ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "A"}, StringExpr{Value: "B"}}},
							ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "x"}, StringExpr{Value: "y"}}},
						},
					},
					{Target: "BAR", Args: []ChanExpr{ChanRef{Name: "FOO.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 8)
			for _, job := range result.Jobs[4:] {
				So(job.Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.FOO"})
			}
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
		Convey("Translate handles C1 eval outputs", func() {
			Convey("processes with path and eval outputs translate both without errors", func() {
				proc := &Process{
					Name:   "A",
					Script: "touch sample.txt",
					Output: []*Declaration{
						{Kind: "path", Expr: StringExpr{Value: "sample.txt"}},
						{Kind: "eval", Expr: StringExpr{Value: "hostname"}},
					},
				}

				jobs, stage, err := translateProcessCall(
					proc,
					&Call{Target: "A"},
					nil,
					nil,
					&ProcessDefaults{},
					nil,
					nil,
					TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"},
				)

				So(err, ShouldBeNil)
				So(jobs, ShouldHaveLength, 1)
				So(jobs[0].Cmd, ShouldContainSubstring, "touch sample.txt\n__nf_eval_0=$(hostname)")
				So(stage.outputPaths, ShouldResemble, []string{"/work/nf-work/r1/A/sample.txt"})
			})
		})

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

		Convey("TranslatePending preserves branch named-output routing for downstream references", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "A",
						Script: "touch sample.small.txt sample.big.txt",
						Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "*.txt"}}},
					},
					{
						Name:   "B",
						Script: "echo ${reads}",
						Input:  []*Declaration{{Kind: "path", Name: "reads"}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "A"},
					{Target: "B", Args: []ChanExpr{NamedChannelRef{
						Source: ChannelChain{Source: ChanRef{Name: "A.out"}, Operators: []ChannelOperator{{
							Name:        "branch",
							Closure:     "small: it == '/work/sample.small.txt'; big: true",
							ClosureExpr: &ClosureExpr{Body: "small: it == '/work/sample.small.txt'; big: true"},
						}}},
						Label: "small",
					}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)

			jobs, err := TranslatePending(result.Pending[0], []CompletedJob{
				{
					RepGrp:      "nf.wf.r1.A",
					OutputPaths: []string{"/work/sample.small.txt"},
					ExitCode:    0,
				},
				{
					RepGrp:      "nf.wf.r1.A",
					OutputPaths: []string{"/work/sample.big.txt"},
					ExitCode:    0,
				},
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "export reads='/work/sample.small.txt'")
			So(jobs[0].Cmd, ShouldNotContainSubstring, "/work/sample.big.txt")
		})

		Convey("TranslatePending preserves multiMap named-output routing for downstream references", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:   "A",
						Script: "touch sample.txt",
						Output: []*Declaration{{Kind: "path", Expr: StringExpr{Value: "sample.txt"}}},
					},
					{
						Name:   "B",
						Script: "echo ${reads}",
						Input:  []*Declaration{{Kind: "path", Name: "reads"}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "A"},
					{Target: "B", Args: []ChanExpr{NamedChannelRef{
						Source: ChannelChain{Source: ChanRef{Name: "A.out"}, Operators: []ChannelOperator{{
							Name:        "multiMap",
							Closure:     "it -> foo: it; bar: it",
							ClosureExpr: &ClosureExpr{Params: []string{"it"}, Body: "foo: it; bar: it"},
						}}},
						Label: "foo",
					}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)

			jobs, err := TranslatePending(result.Pending[0], []CompletedJob{{
				RepGrp:      "nf.wf.r1.A",
				OutputPaths: []string{"/work/sample.txt"},
				ExitCode:    0,
			}}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "export reads='/work/sample.txt'")
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

		Convey("config env is merged into job env overrides", func() {
			Convey("config env applies when the process has no env directive", func() {
				wf := &Workflow{Processes: []*Process{{Name: "proc", Script: "echo hi"}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "proc"}}}}
				cfg := &Config{Env: map[string]string{"FOO": "bar"}}

				result, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
				So(err, ShouldBeNil)
				So(translatedJobEnv(result.Jobs[0])["FOO"], ShouldEqual, "bar")
			})

			Convey("process env overrides config env for duplicate keys", func() {
				wf := &Workflow{Processes: []*Process{{
					Name:   "proc",
					Script: "echo hi",
					Env:    map[string]string{"FOO": "local"},
				}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "proc"}}}}
				cfg := &Config{Env: map[string]string{"FOO": "global"}}

				result, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
				So(err, ShouldBeNil)
				So(translatedJobEnv(result.Jobs[0])["FOO"], ShouldEqual, "local")
			})

			Convey("config env still contributes non-overlapping keys", func() {
				wf := &Workflow{Processes: []*Process{{
					Name:   "proc",
					Script: "echo hi",
					Env:    map[string]string{"A": "override"},
				}}, EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "proc"}}}}
				cfg := &Config{Env: map[string]string{"A": "1", "B": "2"}}

				result, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
				So(err, ShouldBeNil)
				env := translatedJobEnv(result.Jobs[0])
				So(env["A"], ShouldEqual, "override")
				So(env["B"], ShouldEqual, "2")
			})

			Convey("workflow param defaults feed translation without overriding config, profile, or explicit params", func() {
				wf := &Workflow{
					Processes: []*Process{{Name: "proc", Script: "echo ${params.input} ${params.nested.value}"}},
					EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "proc"}}},
					ParamBlock: []*ParamDecl{
						{Name: "input", Default: StringExpr{Value: "wf-default"}},
						{Name: "nested.value", Default: StringExpr{Value: "wf-nested"}},
					},
				}

				defaultResult, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
				So(err, ShouldBeNil)
				So(defaultResult.Jobs[0].Cmd, ShouldContainSubstring, "echo wf-default wf-nested")

				cfg := &Config{
					Params: map[string]any{"input": "config-default", "nested": map[string]any{"value": "config-nested"}},
					Profiles: map[string]*Profile{
						"prod": {Params: map[string]any{"input": "profile-default", "nested": map[string]any{"value": "profile-nested"}}},
					},
				}

				profileResult, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Profile: "prod"})
				So(err, ShouldBeNil)
				So(profileResult.Jobs[0].Cmd, ShouldContainSubstring, "echo profile-default profile-nested")

				explicitResult, err := Translate(wf, cfg, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Profile: "prod", Params: map[string]any{"input": "cli-default", "nested": map[string]any{"value": "cli-nested"}}})
				So(err, ShouldBeNil)
				So(explicitResult.Jobs[0].Cmd, ShouldContainSubstring, "echo cli-default cli-nested")
			})

			Convey("workflow param defaults evaluate safe File constructors", func() {
				wf := &Workflow{
					Processes: []*Process{{Name: "proc", Script: "echo ${params.outdir}"}},
					EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "proc"}}},
					ParamBlock: []*ParamDecl{
						{Name: "base", Default: StringExpr{Value: "/tmp/results"}},
						{Name: "outdir", Default: NewExpr{ClassName: "File", Args: []Expr{ParamsExpr{Path: "base"}, StringExpr{Value: "final"}}}},
					},
				}

				stderr := captureTranslateStderr(func() {
					result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
					So(err, ShouldBeNil)
					So(result.Jobs[0].Cmd, ShouldContainSubstring, "echo /tmp/results/final")
				})

				So(stderr, ShouldEqual, "")
			})

				Convey("workflow param defaults evaluate supported Date constructors", func() {
				wf := &Workflow{
					Processes: []*Process{{Name: "proc", Script: "echo ${params.generated}"}},
					EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "proc"}}},
					ParamBlock: []*ParamDecl{
						{Name: "generated", Default: NewExpr{ClassName: "Date", Args: []Expr{}}},
					},
				}

				var result *TranslateResult
				stderr := captureTranslateStderr(func() {
					var err error
					result, err = Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
					So(err, ShouldBeNil)
				})

				So(stderr, ShouldEqual, "")
				So(result.Jobs[0].Cmd, ShouldNotContainSubstring, "new Date()")
				matches, err := regexp.MatchString(`echo [0-9]{4}-[0-9]{2}-[0-9]{2}T`, result.Jobs[0].Cmd)
				So(err, ShouldBeNil)
				So(matches, ShouldBeTrue)
			})
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

		Convey("unknown selected profiles fail fast", func() {
			wf := &Workflow{
				Processes: []*Process{{Name: "proc", Script: "echo hi"}},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "proc"}}},
			}

			_, err := Translate(wf, &Config{}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Profile: "missing"})

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unknown config profile \"missing\"")
			So(err.Error(), ShouldContainSubstring, "config does not define any profiles")
		})

		Convey("unknown selected profiles list available names", func() {
			wf := &Workflow{
				Processes: []*Process{{Name: "proc", Script: "echo hi"}},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "proc"}}},
			}

			_, err := Translate(wf, &Config{Profiles: map[string]*Profile{"alpha": {}, "beta": {}}}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Profile: "missing"})

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unknown config profile \"missing\"")
			So(err.Error(), ShouldContainSubstring, "available profiles: alpha, beta")
		})
	})
}

func TestTranslateE2(t *testing.T) {
	conditionalWorkflow := func(condition string) *Workflow {
		return &Workflow{
			Processes: []*Process{
				{Name: "BWA", Script: "echo bwa"},
				{Name: "BOWTIE", Script: "echo bowtie"},
			},
			EntryWF: &WorkflowBlock{Conditions: []*IfBlock{{
				Condition: condition,
				Body:      []*Call{{Target: "BWA"}},
				ElseIf:    []*IfBlock{},
				ElseBody:  []*Call{{Target: "BOWTIE"}},
			}}},
		}
	}

	Convey("Translate evaluates workflow conditional blocks against resolved params", t, func() {
		Convey("statically true conditions emit only the if branch", func() {
			result, err := Translate(conditionalWorkflow("params.aligner == 'bwa'"), nil, TranslateConfig{
				RunID:        "r1",
				WorkflowName: "wf",
				Cwd:          "/work",
				Params:       map[string]any{"aligner": "bwa"},
			})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.wf.r1.BWA")
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/BWA")
		})

		Convey("statically false conditions emit only the else branch", func() {
			result, err := Translate(conditionalWorkflow("params.aligner == 'bwa'"), nil, TranslateConfig{
				RunID:        "r1",
				WorkflowName: "wf",
				Cwd:          "/work",
				Params:       map[string]any{"aligner": "bowtie"},
			})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.wf.r1.BOWTIE")
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/BOWTIE")
		})

		Convey("unevaluable conditions emit both branches with a warning and separate cwd scopes", func() {
			wf := &Workflow{
				Processes: []*Process{
					{Name: "A", Script: "echo a"},
					{Name: "B", Script: "echo b"},
				},
				EntryWF: &WorkflowBlock{Conditions: []*IfBlock{{
					Condition: "complexExpr()",
					Body:      []*Call{{Target: "A"}},
					ElseIf:    []*IfBlock{},
					ElseBody:  []*Call{{Target: "B"}},
				}}},
			}

			var result *TranslateResult
			stderr := captureTranslateStderr(func() {
				var err error
				result, err = Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
				So(err, ShouldBeNil)
			})

			So(stderr, ShouldContainSubstring, "unable to evaluate workflow condition")
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.wf.r1.if_0.A")
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/if_0/A")
			So(result.Jobs[1].RepGroup, ShouldEqual, "nf.wf.r1.else_0.B")
			So(result.Jobs[1].Cwd, ShouldEqual, "/work/nf-work/r1/else_0/B")
		})

		Convey("workflow blocks without conditions preserve existing translation behaviour", func() {
			wf := &Workflow{
				Processes: []*Process{{Name: "A", Script: "echo hi"}},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "A"}}, Conditions: []*IfBlock{}},
			}

			var result *TranslateResult
			stderr := captureTranslateStderr(func() {
				var err error
				result, err = Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
				So(err, ShouldBeNil)
			})

			So(stderr, ShouldEqual, "")
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.wf.r1.A")
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/A")
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

func TestTranslateA3DirectiveTranslationDetails(t *testing.T) {
	translateResult := func(proc *Process, tc TranslateConfig, args ...ChanExpr) *TranslateResult {
		wf := &Workflow{
			Processes: []*Process{proc},
			EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: proc.Name, Args: args}}},
		}

		result, err := Translate(wf, nil, tc)
		So(err, ShouldBeNil)

		return result
	}

	findJobBySuffix := func(jobs []*jobqueue.Job, suffix string) *jobqueue.Job {
		for _, job := range jobs {
			if job != nil && strings.HasSuffix(job.RepGroup, suffix) {
				return job
			}
		}

		return nil
	}

	commandLine := func(job *jobqueue.Job) string {
		cmd, cleanup, err := job.CmdLine(context.Background())
		So(err, ShouldBeNil)
		cleanup()

		return cmd
	}

	baseCfg := TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"}

	Convey("Translate covers A3 directive translation details", t, func() {
		Convey("clusterOptions map to scheduler_misc requirements", func() {
			for _, testCase := range []struct {
				name     string
				value    string
				expected string
			}{
				{name: "simple account flag", value: "--account=mylab", expected: "--account=mylab"},
				{name: "multiple scheduler flags", value: "-q priority --exclusive", expected: "-q priority --exclusive"},
			} {
				testCase := testCase
				Convey(testCase.name, func() {
					result := translateResult(&Process{
						Name:       "proc",
						Script:     "echo hi",
						Directives: map[string]any{"clusterOptions": StringExpr{Value: testCase.value}},
					}, baseCfg)

					So(result.Jobs, ShouldHaveLength, 1)
					So(result.Jobs[0].Requirements.Other["scheduler_misc"], ShouldEqual, testCase.expected)
				})
			}
		})

		Convey("queue maps to scheduler_queue requirements", func() {
			for _, testCase := range []struct {
				name     string
				value    string
				expected string
			}{
				{name: "single queue", value: "long", expected: "long"},
				{name: "multiple queue names", value: "gpu,highpri", expected: "gpu,highpri"},
			} {
				testCase := testCase
				Convey(testCase.name, func() {
					result := translateResult(&Process{
						Name:       "proc",
						Script:     "echo hi",
						Directives: map[string]any{"queue": StringExpr{Value: testCase.value}},
					}, baseCfg)

					So(result.Jobs, ShouldHaveLength, 1)
					So(result.Jobs[0].Requirements.Other["scheduler_queue"], ShouldEqual, testCase.expected)
				})
			}
		})

		Convey("queue and clusterOptions can both populate scheduler requirements", func() {
			result := translateResult(&Process{
				Name:   "proc",
				Script: "echo hi",
				Directives: map[string]any{
					"queue":          StringExpr{Value: "long"},
					"clusterOptions": StringExpr{Value: "--account=mylab"},
				},
			}, baseCfg)

			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Other["scheduler_queue"], ShouldEqual, "long")
			So(result.Jobs[0].Requirements.Other["scheduler_misc"], ShouldEqual, "--account=mylab")
		})

		Convey("shell directives populate RunnerExecShell in the job environment", func() {
			Convey("shell list values are joined with spaces", func() {
				result := translateResult(&Process{
					Name:   "proc",
					Script: "echo hi",
					Directives: map[string]any{
						"shell": ListExpr{Elements: []Expr{
							StringExpr{Value: "/bin/bash"},
							StringExpr{Value: "-euo"},
							StringExpr{Value: "pipefail"},
						}},
					},
				}, baseCfg)

				So(translatedJobEnv(result.Jobs[0])["RunnerExecShell"], ShouldEqual, "/bin/bash -euo pipefail")
			})

			Convey("shell string values are preserved as-is", func() {
				result := translateResult(&Process{
					Name:       "proc",
					Script:     "echo hi",
					Directives: map[string]any{"shell": StringExpr{Value: "/bin/zsh"}},
				}, baseCfg)

				So(translatedJobEnv(result.Jobs[0])["RunnerExecShell"], ShouldEqual, "/bin/zsh")
			})
		})

		Convey("containerOptions are inserted into the effective container invocation", func() {
			Convey("docker options are added before the image", func() {
				result := translateResult(&Process{
					Name:       "proc",
					Script:     "echo hi",
					Container:  "ubuntu:latest",
					Directives: map[string]any{"containerOptions": StringExpr{Value: "--gpus all"}},
				}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", ContainerRuntime: "docker"})

				So(result.Jobs, ShouldHaveLength, 1)
				cmdLine := commandLine(result.Jobs[0])
				So(cmdLine, ShouldContainSubstring, "docker run --rm")
				So(cmdLine, ShouldContainSubstring, "-i --gpus all ubuntu:latest /bin/sh")
			})

			Convey("singularity options are added before the image", func() {
				result := translateResult(&Process{
					Name:       "proc",
					Script:     "echo hi",
					Container:  "ubuntu:latest",
					Directives: map[string]any{"containerOptions": StringExpr{Value: "--bind /data"}},
				}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", ContainerRuntime: "singularity"})

				So(result.Jobs, ShouldHaveLength, 1)
				So(commandLine(result.Jobs[0]), ShouldContainSubstring, "singularity shell --bind /data ubuntu:latest")
			})
		})

		Convey("maxErrors creates a polling job that monitors buried jobs and stops resubmitting once terminal", func() {
			result := translateResult(&Process{
				Name:   "proc",
				Script: "echo $sample",
				Input:  []*Declaration{{Kind: "val", Name: "sample"}},
				Directives: map[string]any{
					"maxErrors": IntExpr{Value: 3},
				},
			}, baseCfg, ChannelFactory{Name: "of", Args: []Expr{
				StringExpr{Value: "s0"},
				StringExpr{Value: "s1"},
				StringExpr{Value: "s2"},
				StringExpr{Value: "s3"},
				StringExpr{Value: "s4"},
				StringExpr{Value: "s5"},
				StringExpr{Value: "s6"},
				StringExpr{Value: "s7"},
				StringExpr{Value: "s8"},
				StringExpr{Value: "s9"},
			}})

			So(result.Jobs, ShouldHaveLength, 11)
			poller := findJobBySuffix(result.Jobs, ".proc.maxErrors")
			So(poller, ShouldNotBeNil)
			So(poller.LimitGroups, ShouldBeEmpty)
			So(poller.Cmd, ShouldContainSubstring, "wr status -i 'nf.wf.r1.proc' -o plain")
			So(poller.Cmd, ShouldContainSubstring, `$2 == "buried"`)
			So(poller.Cmd, ShouldContainSubstring, `$2 != "buried" && $2 != "complete" && $2 != "lost"`)
			So(poller.Cmd, ShouldContainSubstring, "[ \"$buried_count\" -gt 3 ]")
			So(poller.Cmd, ShouldContainSubstring, `case "$state" in`)
			So(poller.Cmd, ShouldContainSubstring, `wr kill -i "$job_id" -y`)
			So(poller.Cmd, ShouldContainSubstring, `wr kill --confirmdead -i "$job_id" -y`)
			So(poller.Cmd, ShouldContainSubstring, `wr remove -i "$job_id" -y`)
			So(poller.Cmd, ShouldNotContainSubstring, "--confirm-dead")
			So(poller.Cmd, ShouldContainSubstring, "wr add")
			So(poller.Cmd, ShouldContainSubstring, `next_limit=$(date -d '+1 minute' '+datetime < %Y-%m-%d %H:%M:%S')`)
			So(poller.Cmd, ShouldContainSubstring, "[ \"$active_count\" -gt 0 ]")
		})
	})
}

func translatedJobEnv(job *jobqueue.Job) map[string]string {
	job.EnvCRetrieved = true
	env, err := job.Env()
	if err != nil {
		panic(err)
	}

	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		values[key] = value
	}

	return values
}
