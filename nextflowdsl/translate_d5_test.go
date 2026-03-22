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

func TestTranslateD5(t *testing.T) {
	Convey("Translate handles D5 channel factory resolution", t, func() {
		Convey("Channel.of creates one indexed job per item", func() {
			wf := d5Workflow(ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}})

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 3)
			So(result.Jobs[0].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/0")
			So(result.Jobs[1].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/1")
			So(result.Jobs[2].Cwd, ShouldEqual, "/work/nf-work/r1/ALIGN/2")
			So(result.Jobs[0].DepGroups, ShouldResemble, []string{"nf.r1.ALIGN.0"})
			So(result.Jobs[1].DepGroups, ShouldResemble, []string{"nf.r1.ALIGN.1"})
			So(result.Jobs[2].DepGroups, ShouldResemble, []string{"nf.r1.ALIGN.2"})
		})

		Convey("Channel.fromFilePairs creates one job per pair", func() {
			dataDir := t.TempDir()
			for _, name := range []string{"sample1_1.fq", "sample1_2.fq", "sample2_1.fq", "sample2_2.fq"} {
				err := os.WriteFile(filepath.Join(dataDir, name), []byte(name), 0o600)
				So(err, ShouldBeNil)
			}

			wf := d5Workflow(ChannelFactory{Name: "fromFilePairs", Args: []Expr{StringExpr{Value: filepath.Join(dataDir, "*_{1,2}.fq")}}})

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: dataDir})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 2)
		})

		Convey("Channel.value creates one job whose command contains the resolved value", func() {
			wf := d5Workflow(ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "x"}}})

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Cmd, ShouldContainSubstring, "export reads='x'")
		})

		Convey("Channel.fromPath with no matches creates zero jobs", func() {
			wf := d5Workflow(ChannelFactory{Name: "fromPath", Args: []Expr{StringExpr{Value: filepath.Join(t.TempDir(), "*.fq")}}})

			result, err := Translate(wf, nil, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(err, ShouldBeNil)
			So(result.Jobs, ShouldBeEmpty)
			So(result.Pending, ShouldBeEmpty)
		})
	})
}

func d5Workflow(arg ChanExpr) *Workflow {
	return &Workflow{
		Processes: []*Process{{
			Name:       "ALIGN",
			Directives: map[string]any{},
			Input:      []*Declaration{{Kind: "path", Name: "reads"}},
			Script:     "echo $reads",
			Env:        map[string]string{},
			PublishDir: []*PublishDir{},
		}},
		EntryWF: &WorkflowBlock{Calls: []*Call{{Target: "ALIGN", Args: []ChanExpr{arg}}}},
	}
}
