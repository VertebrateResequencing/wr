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

	. "github.com/smartystreets/goconvey/convey"
)

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
}
