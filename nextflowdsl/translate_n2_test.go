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

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestTranslateN2WorkflowPublish(t *testing.T) {
	Convey("Translate handles N2 workflow publish wiring", t, func() {
		newWorkflow := func(outputBlock string, publishes []*WFPublish, call *Call) *Workflow {
			proc := &Process{
				Name:       "produce",
				Directives: map[string]any{},
				Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "out.txt"}}},
				Script:     "echo hi > out.txt",
				Env:        map[string]string{},
				PublishDir: []*PublishDir{},
			}

			entryCall := &Call{Target: "produce"}
			if call != nil {
				entryCall = call
			}

			return &Workflow{
				Processes:   []*Process{proc},
				OutputBlock: outputBlock,
				EntryWF: &WorkflowBlock{
					Calls:   []*Call{entryCall},
					Publish: publishes,
				},
			}
		}

		findJob := func(jobs []*jobqueue.Job, match func(*jobqueue.Job) bool) *jobqueue.Job {
			for _, job := range jobs {
				if job != nil && match(job) {
					return job
				}
			}

			return nil
		}

		Convey("publish assignments add copy commands to final jobs", func() {
			wf := newWorkflow(
				"samples { path 'results/fastq' }",
				[]*WFPublish{{Target: "samples", Source: "produce.out"}},
				nil,
			)

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "cp ")
			So(commands[0], ShouldContainSubstring, "/work/nf-work/r1/produce/out.txt")
			So(commands[0], ShouldContainSubstring, "/work/results/fastq/")
		})

		Convey("index targets create an aggregator job that writes a TSV index", func() {
			wf := newWorkflow(
				"samples { path 'results/fastq'; index { path 'index.csv' } }",
				[]*WFPublish{{Target: "samples", Source: "produce.out"}},
				&Call{Target: "produce", Args: []ChanExpr{ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "sampleA"}, StringExpr{Value: "sampleB"}}}}},
			)
			wf.Processes[0].Input = []*Declaration{{Kind: "val", Name: "sample"}}
			wf.Processes[0].Output = []*Declaration{{Kind: "path", Expr: StringExpr{Value: "${sample}.txt"}}}
			wf.Processes[0].Script = "echo hi > ${sample}.txt"

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 3)

			indexJob := findJob(result.Jobs, func(job *jobqueue.Job) bool {
				return strings.Contains(job.Cmd, "index.csv")
			})
			So(indexJob, ShouldNotBeNil)
			So(indexJob.Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.produce.0", "nf.r1.produce.1"})
			So(indexJob.Cmd, ShouldContainSubstring, "/work/index.csv")
			So(indexJob.Cmd, ShouldContainSubstring, "sampleA.txt")
			So(indexJob.Cmd, ShouldContainSubstring, "sampleB.txt")
			So(indexJob.Cmd, ShouldContainSubstring, "\t")
		})

		Convey("missing output targets warn and skip publish wiring", func() {
			wf := newWorkflow(
				"samples { path 'results/fastq' }",
				[]*WFPublish{{Target: "missing", Source: "produce.out"}},
				nil,
			)

			var result *TranslateResult
			stderr := captureTranslateStderr(func() {
				var err error
				result, err = Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})
				So(err, ShouldBeNil)
			})

			So(result.Jobs, ShouldHaveLength, 1)
			So(onSuccessRunCommands(result.Jobs[0]), ShouldBeNil)
			So(stderr, ShouldContainSubstring, "publish target")
			So(stderr, ShouldContainSubstring, "missing")
			So(stderr, ShouldContainSubstring, "output block")
		})

		Convey("workflows without publish assignments do not add publish jobs", func() {
			wf := newWorkflow("samples { path 'results/fastq' }", nil, nil)

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(onSuccessRunCommands(result.Jobs[0]), ShouldBeNil)
		})
	})
}
