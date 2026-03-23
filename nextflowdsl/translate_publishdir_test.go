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
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestTranslateD2PublishDir(t *testing.T) {
	Convey("Translate handles D2 publishDir translation", t, func() {
		newPublishWorkflow := func() *Workflow {
			proc := newPublishProcess()

			return &Workflow{
				Processes: []*Process{proc},
				EntryWF:   &WorkflowBlock{Calls: []*Call{{Target: "publish"}}},
			}
		}

		Convey("default publishDir copies declared outputs to an absolute directory", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "/results", Mode: "copy"}}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "cp ")
			So(commands[0], ShouldContainSubstring, "out.txt")
			So(commands[0], ShouldContainSubstring, "/results/")
		})

		Convey("publishDir pattern is preserved in the copy command", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "/results", Pattern: "*.bam", Mode: "copy"}}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "*.bam")
			So(commands[0], ShouldNotContainSubstring, "out.txt")
		})

		Convey("relative publishDir paths resolve against the workflow file directory", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "results", Mode: "copy"}}

			result, err := Translate(wf, nil, TranslateConfig{
				RunID:        "r1",
				WorkflowName: "mywf",
				WorkflowPath: "/workflow/main.nf",
				Cwd:          "/work",
			})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "/workflow/results/")
		})

		Convey("processes without publishDir do not gain on-success run behaviours", func() {
			wf := newPublishWorkflow()

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(onSuccessRunCommands(result.Jobs[0]), ShouldBeNil)
		})

		Convey("multiple publishDir directives create multiple on-success run behaviours", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "/results/a", Mode: "copy"}, {Path: "/results/b", Mode: "copy"}}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 2)
			So(commands[0], ShouldContainSubstring, "/results/a/")
			So(commands[1], ShouldContainSubstring, "/results/b/")
		})

		Convey("link mode uses ln rather than cp", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "/results", Mode: "link"}}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "ln ")
			So(commands[0], ShouldNotContainSubstring, "cp ")
		})

		Convey("move mode uses mv rather than cp", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "/results", Mode: "move"}}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "mv ")
			So(commands[0], ShouldNotContainSubstring, "cp ")
		})

		Convey("publishDir paths support params substitution", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "${params.outdir}", Mode: "copy"}}

			result, err := Translate(wf, nil, TranslateConfig{
				RunID:        "r1",
				WorkflowName: "mywf",
				Cwd:          "/work",
				Params:       map[string]any{"outdir": "/results"},
			})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "/results/")
		})

		Convey("patternless publishDir copies dynamically resolved output paths", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].Output = []*Declaration{{Kind: "path", Expr: StringExpr{Value: "${params.sample}.txt"}}}
			wf.Processes[0].Script = "echo hi > ${params.sample}.txt"
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "/results", Mode: "copy"}}

			result, err := Translate(wf, nil, TranslateConfig{
				RunID:        "r1",
				WorkflowName: "mywf",
				Cwd:          "/work",
				Params:       map[string]any{"sample": "sampleA"},
			})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "/work/nf-work/r1/publish/sampleA.txt")
			So(commands[0], ShouldContainSubstring, "/results/")
		})

		Convey("tuple path outputs are also published", func() {
			wf := newPublishWorkflow()
			wf.Processes[0].Output = []*Declaration{{
				Kind: "tuple",
				Elements: []*TupleElement{
					{Kind: "val", Name: "sample"},
					{Kind: "path", Expr: StringExpr{Value: "out.txt"}},
					{Kind: "file", Expr: StringExpr{Value: "out.bai"}},
				},
			}}
			wf.Processes[0].PublishDir = []*PublishDir{{Path: "/results", Mode: "copy"}}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)

			commands := onSuccessRunCommands(result.Jobs[0])
			So(commands, ShouldHaveLength, 1)
			So(commands[0], ShouldContainSubstring, "out.txt")
			So(commands[0], ShouldContainSubstring, "out.bai")
		})
	})
}

func newPublishProcess() *Process {
	return &Process{
		Name:       "publish",
		Directives: map[string]any{},
		Output:     []*Declaration{{Kind: "path", Expr: StringExpr{Value: "out.txt"}}},
		Script:     "echo hi > out.txt",
		Env:        map[string]string{},
		PublishDir: []*PublishDir{},
	}
}

func onSuccessRunCommands(job *jobqueue.Job) []string {
	if job == nil {
		return nil
	}

	commands := make([]string, 0, len(job.Behaviours))
	for _, behaviour := range job.Behaviours {
		if behaviour == nil || behaviour.When != jobqueue.OnSuccess || behaviour.Do != jobqueue.Run {
			continue
		}

		command, ok := behaviour.Arg.(string)
		if !ok {
			continue
		}

		commands = append(commands, command)
	}

	if len(commands) == 0 {
		return nil
	}

	return commands
}
