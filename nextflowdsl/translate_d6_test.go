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

func TestTranslateD6(t *testing.T) {
	Convey("Translate handles D6 channel operator effects", t, func() {
		Convey("collect collapses indexed upstream jobs into one downstream job with all dependencies", func() {
			wf := &Workflow{
				Processes: []*Process{
					d6Process("upstream", []*Declaration{{Kind: "val", Name: "x"}}, []*Declaration{{Kind: "val", Name: "out"}}, "echo $x"),
					d6Process("merge", []*Declaration{{Kind: "val", Name: "reads"}}, nil, "echo $reads"),
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "upstream", Args: []ChanExpr{ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}}},
					{Target: "merge", Args: []ChanExpr{ChannelChain{Source: ChanRef{Name: "upstream.out"}, Operators: []ChannelOperator{{Name: "collect"}}}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			mergeJobs := jobsWithReqGroup(result.Jobs, "nf.merge")
			So(mergeJobs, ShouldHaveLength, 1)
			So(mergeJobs[0].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.upstream.0", "nf.r1.upstream.1", "nf.r1.upstream.2"})
			So(mergeJobs[0].Cmd, ShouldContainSubstring, "export reads='1 2 3'")
		})

		Convey("first keeps only the first upstream item", func() {
			wf := &Workflow{
				Processes: []*Process{
					d6Process("upstream", []*Declaration{{Kind: "val", Name: "x"}}, []*Declaration{{Kind: "val", Name: "out"}}, "echo $x"),
					d6Process("peek", []*Declaration{{Kind: "val", Name: "reads"}}, nil, "echo $reads"),
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "upstream", Args: []ChanExpr{ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}}}},
					{Target: "peek", Args: []ChanExpr{ChannelChain{Source: ChanRef{Name: "upstream.out"}, Operators: []ChannelOperator{{Name: "first"}}}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			peekJobs := jobsWithReqGroup(result.Jobs, "nf.peek")
			So(peekJobs, ShouldHaveLength, 1)
			So(peekJobs[0].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.upstream.0"})
			So(peekJobs[0].Cmd, ShouldContainSubstring, "export reads='1'")
		})

		Convey("groupTuple creates one downstream job per group key", func() {
			call := &Call{Target: "foo", Args: []ChanExpr{ChannelChain{Source: ChanRef{Name: "pairs"}, Operators: []ChannelOperator{{Name: "groupTuple"}}}}}
			translated := map[string]translatedCall{
				"pairs": {
					items: []channelItem{
						{value: []any{"a", 1}, depGroups: []string{"nf.r1.upstream.0"}},
						{value: []any{"a", 2}, depGroups: []string{"nf.r1.upstream.1"}},
						{value: []any{"a", 3}, depGroups: []string{"nf.r1.upstream.2"}},
						{value: []any{"b", 4}, depGroups: []string{"nf.r1.upstream.3"}},
						{value: []any{"b", 5}, depGroups: []string{"nf.r1.upstream.4"}},
						{value: []any{"b", 6}, depGroups: []string{"nf.r1.upstream.5"}},
					},
				},
			}

			jobs, _, err := translateProcessCall(d6Process("foo", []*Declaration{{Kind: "val", Name: "group"}}, nil, "echo $group"), call, nil, translated, &ProcessDefaults{}, nil, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 2)
			So(jobs[0].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.upstream.0", "nf.r1.upstream.1", "nf.r1.upstream.2"})
			So(jobs[1].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.upstream.3", "nf.r1.upstream.4", "nf.r1.upstream.5"})
		})

		Convey("filter reduces downstream job count to matching items", func() {
			wf := &Workflow{
				Processes: []*Process{d6Process("foo", []*Declaration{{Kind: "val", Name: "x"}}, nil, "echo $x")},
				EntryWF: &WorkflowBlock{Calls: []*Call{{
					Target: "foo",
					Args: []ChanExpr{ChannelChain{
						Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}, IntExpr{Value: 4}, IntExpr{Value: 5}}},
						Operators: []ChannelOperator{{Name: "filter", Closure: "it > 3"}},
					}},
				}}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			fooJobs := jobsWithReqGroup(result.Jobs, "nf.foo")
			So(fooJobs, ShouldHaveLength, 2)
			So(fooJobs[0].Cmd, ShouldContainSubstring, "export x='4'")
			So(fooJobs[1].Cmd, ShouldContainSubstring, "export x='5'")
		})

		Convey("mix merges both input channels into downstream fanout", func() {
			call := &Call{Target: "bar", Args: []ChanExpr{ChannelChain{Source: ChanRef{Name: "ch1"}, Operators: []ChannelOperator{{Name: "mix", Channels: []ChanExpr{ChanRef{Name: "ch2"}}}}}}}
			translated := map[string]translatedCall{
				"ch1": {items: []channelItem{{value: 1, depGroups: []string{"nf.r1.left.0"}}, {value: 2, depGroups: []string{"nf.r1.left.1"}}}},
				"ch2": {items: []channelItem{{value: 3, depGroups: []string{"nf.r1.right.0"}}, {value: 4, depGroups: []string{"nf.r1.right.1"}}, {value: 5, depGroups: []string{"nf.r1.right.2"}}}},
			}

			jobs, _, err := translateProcessCall(d6Process("bar", []*Declaration{{Kind: "val", Name: "x"}}, nil, "echo $x"), call, nil, translated, &ProcessDefaults{}, nil, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 5)
			So(jobs[0].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.left.0"})
			So(jobs[4].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.right.2"})
		})

		Convey("mix keeps distinct dependencies in a diamond DAG", func() {
			wf := &Workflow{
				Processes: []*Process{
					d6Process("A", nil, []*Declaration{{Kind: "val", Name: "out"}}, "echo A"),
					d6Process("B", nil, []*Declaration{{Kind: "val", Name: "out"}}, "echo B"),
					d6Process("C", []*Declaration{{Kind: "val", Name: "reads"}}, nil, "echo $reads"),
				},
				EntryWF: &WorkflowBlock{Calls: []*Call{
					{Target: "A"},
					{Target: "B"},
					{Target: "C", Args: []ChanExpr{ChannelChain{Source: ChanRef{Name: "A.out"}, Operators: []ChannelOperator{{Name: "mix", Channels: []ChanExpr{ChanRef{Name: "B.out"}}}}}}},
				}},
			}

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			cJobs := jobsWithReqGroup(result.Jobs, "nf.C")
			So(cJobs, ShouldHaveLength, 2)
			So(cJobs[0].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.A"})
			So(cJobs[1].Dependencies.DepGroups(), ShouldResemble, []string{"nf.r1.B"})
		})
	})
}

func d6Process(name string, input []*Declaration, output []*Declaration, script string) *Process {
	return &Process{
		Name:       name,
		Directives: map[string]any{},
		Input:      input,
		Output:     output,
		Script:     script,
		Env:        map[string]string{},
		PublishDir: []*PublishDir{},
	}
}

func jobsWithReqGroup(jobs []*jobqueue.Job, reqGroup string) []*jobqueue.Job {
	filtered := []*jobqueue.Job{}
	for _, job := range jobs {
		if job.ReqGroup == reqGroup {
			filtered = append(filtered, job)
		}
	}

	return filtered
}
