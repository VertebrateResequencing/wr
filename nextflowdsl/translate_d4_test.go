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
	"os"
	"path/filepath"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestTranslateD4(t *testing.T) {
	Convey("Translate handles D4 dynamic workflow detection", t, func() {
		Convey("glob path outputs are classified as pending inputs", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:       "PRODUCE",
						Directives: map[string]Expr{},
						Script:     "touch a.txt b.txt",
						Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "*.txt"}}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "CONSUME",
						Directives: map[string]Expr{},
						Script:     "cat $reads",
						Input:      []*Declaration{{Kind: "path", Name: "reads"}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE"},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)
			So(result.Pending[0].AwaitDepGrps, ShouldResemble, []string{"nf.r1.PRODUCE"})
		})

		Convey("literal path outputs are also classified as pending inputs", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:       "PRODUCE",
						Directives: map[string]Expr{},
						Script:     "touch result.txt",
						Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "result.txt"}}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "CONSUME",
						Directives: map[string]Expr{},
						Script:     "cat $reads",
						Input:      []*Declaration{{Kind: "path", Name: "reads"}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE"},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)
			So(result.Pending[0].AwaitDepGrps, ShouldResemble, []string{"nf.r1.PRODUCE"})
		})

		Convey("val outputs remain static and emit downstream jobs immediately", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:       "PRODUCE",
						Directives: map[string]Expr{},
						Script:     "echo x",
						Output:     []*Declaration{{Kind: "val", Name: "token"}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "CONSUME",
						Directives: map[string]Expr{},
						Script:     "echo $token",
						Input:      []*Declaration{{Kind: "val", Name: "token"}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE"},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[1].Cmd, ShouldNotContainSubstring, "/work/nf-work/r1/PRODUCE/")
		})

		Convey("val outputs propagate known input values into downstream commands", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:       "PRODUCE",
						Directives: map[string]Expr{},
						Input:      []*Declaration{{Kind: "val", Name: "token"}},
						Script:     "echo $token",
						Output:     []*Declaration{{Kind: "val", Name: "token"}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "CONSUME",
						Directives: map[string]Expr{},
						Script:     "echo $token",
						Input:      []*Declaration{{Kind: "val", Name: "token"}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE", Args: []ChanExpr{ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "hello"}}}}},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Pending, ShouldBeEmpty)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Jobs[1].Cmd, ShouldContainSubstring, "export token='hello'")
		})

		Convey("TranslatePending preserves per-item fanout for multiple completed jobs sharing a rep group", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:       "PRODUCE",
						Directives: map[string]Expr{},
						Input:      []*Declaration{{Kind: "val", Name: "token"}},
						Script:     "touch produced.txt",
						Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "produced.txt"}}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "CONSUME",
						Directives: map[string]Expr{},
						Input:      []*Declaration{{Kind: "path", Name: "reads"}},
						Script:     "cat $reads",
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE", Args: []ChanExpr{ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "a"}, StringExpr{Value: "b"}}}}},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Pending, ShouldHaveLength, 1)

			jobs, err := TranslatePending(result.Pending[0], []CompletedJob{
				{RepGrp: "nf.wf.r1.PRODUCE", OutputPaths: []string{"/work/nf-work/r1/PRODUCE/0/produced.txt"}, ExitCode: 0},
				{RepGrp: "nf.wf.r1.PRODUCE", OutputPaths: []string{"/work/nf-work/r1/PRODUCE/1/produced.txt"}, ExitCode: 0},
			}, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 2)
			So(jobs[0].Cmd, ShouldContainSubstring, "/work/nf-work/r1/PRODUCE/0/produced.txt")
			So(jobs[1].Cmd, ShouldContainSubstring, "/work/nf-work/r1/PRODUCE/1/produced.txt")
		})

		Convey("CompletedJobsForPending waits for every awaited dep group and expands concrete output paths", func() {
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:       "PRODUCE",
						Directives: map[string]Expr{},
						Input:      []*Declaration{{Kind: "val", Name: "token"}},
						Script:     "touch produced.txt",
						Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "produced.txt"}}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "CONSUME",
						Directives: map[string]Expr{},
						Input:      []*Declaration{{Kind: "path", Name: "reads"}},
						Script:     "cat $reads",
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE", Args: []ChanExpr{ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "a"}, StringExpr{Value: "b"}}}}},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			baseDir := t.TempDir()
			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: baseDir})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Pending, ShouldHaveLength, 1)

			writeOutput := func(job *jobqueue.Job) string {
				So(os.MkdirAll(job.Cwd, 0o755), ShouldBeNil)
				outputPath := filepath.Join(job.Cwd, "produced.txt")
				So(os.WriteFile(outputPath, []byte("ok"), 0o644), ShouldBeNil)

				return outputPath
			}

			firstPath := writeOutput(result.Jobs[0])

			completed, ready, err := CompletedJobsForPending(result.Pending[0], []*jobqueue.Job{result.Jobs[0]})
			So(err, ShouldBeNil)
			So(ready, ShouldBeFalse)
			So(completed, ShouldBeNil)

			secondPath := writeOutput(result.Jobs[1])

			completed, ready, err = CompletedJobsForPending(result.Pending[0], []*jobqueue.Job{result.Jobs[0], result.Jobs[1]})
			So(err, ShouldBeNil)
			So(ready, ShouldBeTrue)
			So(completed, ShouldHaveLength, 2)
			So(completed[0].OutputPaths, ShouldResemble, []string{firstPath})
			So(completed[1].OutputPaths, ShouldResemble, []string{secondPath})
		})

		Convey("CompletedJobsForPending preserves absolute output paths outside the job cwd", func() {
			absDir := t.TempDir()
			absOutput := filepath.Join(absDir, "out.txt")
			wf := &Workflow{
				Processes: []*Process{
					{
						Name:       "PRODUCE",
						Directives: map[string]Expr{},
						Script:     "touch " + absOutput,
						Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: absOutput}}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "CONSUME",
						Directives: map[string]Expr{},
						Input:      []*Declaration{{Kind: "path", Name: "reads"}},
						Script:     "cat $reads",
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "PRODUCE"},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Pending, ShouldHaveLength, 1)
			So(os.WriteFile(absOutput, []byte("ok"), 0o644), ShouldBeNil)

			completed, ready, err := CompletedJobsForPending(result.Pending[0], []*jobqueue.Job{result.Jobs[0]})

			So(err, ShouldBeNil)
			So(ready, ShouldBeTrue)
			So(completed, ShouldHaveLength, 1)
			So(completed[0].OutputPaths, ShouldResemble, []string{absOutput})
		})
	})
}
