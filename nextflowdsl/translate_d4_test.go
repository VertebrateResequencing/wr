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
	"strconv"
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

			completed, ready, err := CompletedJobsForPending(result.Pending[0], []*jobqueue.Job{result.Jobs[0]}, nil)
			So(err, ShouldBeNil)
			So(ready, ShouldBeFalse)
			So(completed, ShouldBeNil)

			secondPath := writeOutput(result.Jobs[1])

			completed, ready, err = CompletedJobsForPending(result.Pending[0], []*jobqueue.Job{result.Jobs[0], result.Jobs[1]}, nil)
			So(err, ShouldBeNil)
			So(ready, ShouldBeTrue)
			So(completed, ShouldHaveLength, 2)
			So(completed[0].OutputPaths, ShouldResemble, []string{firstPath})
			So(completed[1].OutputPaths, ShouldResemble, []string{secondPath})
		})

		Convey("CompletedJobsForPending preserves numeric fanout ordering for 10 or more indexed jobs", func() {
			values := make([]Expr, 0, 12)
			for i := range 12 {
				values = append(values, StringExpr{Value: strconv.Itoa(i)})
			}

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
					{Target: "PRODUCE", Args: []ChanExpr{ChannelFactory{Name: "of", Args: values}}},
					{Target: "CONSUME", Args: []ChanExpr{ChanRef{Name: "PRODUCE.out"}}},
				}},
			}

			baseDir := t.TempDir()
			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: baseDir})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 12)
			So(result.Pending, ShouldHaveLength, 1)

			for _, job := range result.Jobs {
				So(os.MkdirAll(job.Cwd, 0o755), ShouldBeNil)
				So(os.WriteFile(filepath.Join(job.Cwd, "produced.txt"), []byte("ok"), 0o644), ShouldBeNil)
			}

			completed, ready, err := CompletedJobsForPending(result.Pending[0], result.Jobs, nil)
			So(err, ShouldBeNil)
			So(ready, ShouldBeTrue)
			So(completed, ShouldHaveLength, 12)

			jobs, err := TranslatePending(result.Pending[0], completed, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: baseDir})
			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 12)

			for i, job := range jobs {
				expectedPath := filepath.Join(baseDir, "nf-work", "r1", "PRODUCE", strconv.Itoa(i), "produced.txt")
				expectedDep := "nf.r1.PRODUCE." + strconv.Itoa(i)
				So(job.Cmd, ShouldContainSubstring, expectedPath)
				So(job.Dependencies.DepGroups(), ShouldResemble, []string{expectedDep})
			}
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

			completed, ready, err := CompletedJobsForPending(result.Pending[0], []*jobqueue.Job{result.Jobs[0]}, nil)

			So(err, ShouldBeNil)
			So(ready, ShouldBeTrue)
			So(completed, ShouldHaveLength, 1)
			So(completed[0].OutputPaths, ShouldResemble, []string{absOutput})
		})

		Convey("downstream pending stages wait until all jobs in an upstream pending rep group finish", func() {
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
						Script:     "cp $reads consumed.txt",
						Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "consumed.txt"}}},
						Env:        map[string]string{},
						PublishDir: []*PublishDir{},
					},
					{
						Name:       "REPORT",
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
					{Target: "REPORT", Args: []ChanExpr{ChanRef{Name: "CONSUME.out"}}},
				}},
			}

			baseDir := t.TempDir()
			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: baseDir})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
			So(result.Pending, ShouldHaveLength, 2)

			writeOutput := func(job *jobqueue.Job, name string) string {
				So(os.MkdirAll(job.Cwd, 0o755), ShouldBeNil)
				outputPath := filepath.Join(job.Cwd, name)
				So(os.WriteFile(outputPath, []byte("ok"), 0o644), ShouldBeNil)

				return outputPath
			}

			cloneJob := func(job *jobqueue.Job) *jobqueue.Job {
				return &jobqueue.Job{
					Cmd:          job.Cmd,
					Cwd:          job.Cwd,
					CwdMatters:   job.CwdMatters,
					RepGroup:     job.RepGroup,
					ReqGroup:     job.ReqGroup,
					Override:     job.Override,
					DepGroups:    append([]string{}, job.DepGroups...),
					Dependencies: append(jobqueue.Dependencies{}, job.Dependencies...),
					Behaviours:   append(jobqueue.Behaviours{}, job.Behaviours...),
					State:        job.State,
					Exitcode:     job.Exitcode,
				}
			}

			produceOutputs := []CompletedJob{}
			for _, job := range result.Jobs {
				produceOutputs = append(produceOutputs, CompletedJob{
					RepGrp:      job.RepGroup,
					OutputPaths: []string{writeOutput(job, "produced.txt")},
					ExitCode:    0,
				})
			}

			consumeJobs, err := TranslatePending(result.Pending[0], produceOutputs, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: baseDir})
			So(err, ShouldBeNil)
			So(consumeJobs, ShouldHaveLength, 2)

			consumeComplete := []*jobqueue.Job{cloneJob(consumeJobs[0])}
			consumeComplete[0].State = jobqueue.JobStateComplete
			consumeComplete[0].Exitcode = 0
			writeOutput(consumeComplete[0], "consumed.txt")

			consumeIncomplete := []*jobqueue.Job{cloneJob(consumeJobs[1])}
			consumeIncomplete[0].State = jobqueue.JobStateRunning

			completed, ready, err := CompletedJobsForPending(result.Pending[1], consumeComplete, consumeIncomplete)
			So(err, ShouldBeNil)
			So(ready, ShouldBeFalse)
			So(completed, ShouldBeNil)

			consumeComplete = append(consumeComplete, cloneJob(consumeJobs[1]))
			consumeComplete[1].State = jobqueue.JobStateComplete
			consumeComplete[1].Exitcode = 0
			writeOutput(consumeComplete[1], "consumed.txt")

			completed, ready, err = CompletedJobsForPending(result.Pending[1], consumeComplete, nil)
			So(err, ShouldBeNil)
			So(ready, ShouldBeTrue)
			So(completed, ShouldHaveLength, 2)
		})
	})
}
