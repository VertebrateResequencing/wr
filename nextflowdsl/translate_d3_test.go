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

	. "github.com/smartystreets/goconvey/convey"
)

func TestTranslateD3(t *testing.T) {
	Convey("Translate handles D3 subworkflow translation", t, func() {
		Convey("top-level processes keep their original identifiers after scoped translation support is added", func() {
			wf := &Workflow{
				Processes: []*Process{
					d3Process("trim", "echo trimmed > out.txt", nil, []*Declaration{{Kind: "path", Expr: StringExpr{Value: "out.txt"}}}),
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "trim"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.mywf.r1.trim")
			So(result.Jobs[0].DepGroups, ShouldContain, "nf.r1.trim")
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/trim")
		})

		Convey("subworkflow processes are inlined with scoped rep groups, dep groups, and cwd", func() {
			wf := &Workflow{
				Processes: []*Process{
					d3Process("trim", "echo trimmed > out.txt", nil, []*Declaration{{Kind: "path", Expr: StringExpr{Value: "out.txt"}}}),
				},
				SubWFs: []*SubWorkflow{{
					Name: "prep",
					Body: &WorkflowBlock{Calls: []*Call{{Target: "trim"}}},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "prep"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].RepGroup, ShouldEqual, "nf.mywf.r1.prep.trim")
			So(result.Jobs[0].ReqGroup, ShouldEqual, "nf.trim")
			So(result.Jobs[0].DepGroups, ShouldContain, "nf.r1.prep.trim")
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/prep/trim")
		})

		Convey("subworkflow and process name collisions return a rep group collision error", func() {
			wf := &Workflow{
				Processes: []*Process{
					d3Process("foo", "echo foo", nil, nil),
					d3Process("trim", "echo trim", nil, nil),
				},
				SubWFs: []*SubWorkflow{{
					Name: "foo",
					Body: &WorkflowBlock{Calls: []*Call{{Target: "trim"}}},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "foo"}}},
			}

			_, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "rep_grp collision")
		})

		Convey("reused processes in different subworkflows get distinct scoped jobs and dependencies", func() {
			wf := &Workflow{
				Processes: []*Process{
					d3Process("sort", "echo sorted", nil, []*Declaration{{Kind: "val", Name: "sorted"}}),
					d3Process("merge", "echo \"$input\"", []*Declaration{{Kind: "val", Name: "input"}}, nil),
				},
				SubWFs: []*SubWorkflow{
					{
						Name: "align",
						Body: &WorkflowBlock{Calls: []*Call{{Target: "sort"}}},
					},
					{
						Name: "qc",
						Body: &WorkflowBlock{Calls: []*Call{
							{Target: "sort"},
							{Target: "merge", Args: []ChanExpr{ChanRef{Name: "align.sort.out"}}},
						}},
					},
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "align"}, {Target: "qc"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 3)

			alignSort := result.Jobs[0]
			qcSort := result.Jobs[1]
			qcMerge := result.Jobs[2]

			So(alignSort.ReqGroup, ShouldEqual, "nf.sort")
			So(alignSort.DepGroups, ShouldContain, "nf.r1.align.sort")
			So(alignSort.Cwd, ShouldEqual, "/work/nf-work/r1/align/sort")
			So(qcSort.ReqGroup, ShouldEqual, "nf.sort")
			So(qcSort.DepGroups, ShouldContain, "nf.r1.qc.sort")
			So(qcSort.Cwd, ShouldEqual, "/work/nf-work/r1/qc/sort")
			So(qcMerge.ReqGroup, ShouldEqual, "nf.merge")
			So(qcMerge.Dependencies.DepGroups(), ShouldContain, "nf.r1.align.sort")
		})

		Convey("subworkflow-local references shadow top-level stages with the same name", func() {
			wf := &Workflow{
				Processes: []*Process{
					d3Process("sort", "echo top", nil, []*Declaration{{Kind: "val", Name: "out"}}),
					d3Process("merge", "echo $input", []*Declaration{{Kind: "val", Name: "input"}}, nil),
				},
				SubWFs: []*SubWorkflow{{
					Name: "qc",
					Body: &WorkflowBlock{Calls: []*Call{
						{Target: "sort"},
						{Target: "merge", Args: []ChanExpr{ChanRef{Name: "sort.out"}}},
					}},
				}},
				EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "sort"}, {Target: "qc"}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "mywf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 3)
			So(result.Jobs[2].RepGroup, ShouldEqual, "nf.mywf.r1.qc.merge")
			So(result.Jobs[2].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.qc.sort"})
		})
	})
}

func d3Process(name, script string, input []*Declaration, output []*Declaration) *Process {
	return &Process{
		Name:       name,
		Directives: map[string]Expr{},
		Input:      input,
		Output:     output,
		Script:     script,
		Env:        map[string]string{},
		PublishDir: []*PublishDir{},
	}
}
