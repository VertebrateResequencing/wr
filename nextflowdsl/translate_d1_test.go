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
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestTranslateD1(t *testing.T) {
	parseWorkflow := func(source string) *Workflow {
		wf, err := Parse(strings.NewReader(source))
		So(err, ShouldBeNil)

		return wf
	}

	translateWorkflow := func(wf *Workflow, tc TranslateConfig) (*TranslateResult, string) {
		var (
			result *TranslateResult
			err    error
		)

		stderr := captureTranslateD1Stderr(func() {
			result, err = Translate(wf, nil, tc)
		})

		So(err, ShouldBeNil)

		return result, stderr
	}

	Convey("Translate handles D1 accelerator directive mapping", t, func() {
		Convey("accelerator counts map to lsf GPU requirements", func() {
			wf := parseWorkflow("process GPU {\naccelerator 1\nscript: 'echo hi'\n}\nworkflow {\nGPU()\n}\n")

			result, stderr := translateWorkflow(wf, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Scheduler: "lsf"})

			So(stderr, ShouldEqual, "")
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Other["scheduler_misc"], ShouldContainSubstring, "select[ngpus>0] rusage[ngpus_physical=1]")
		})

		Convey("accelerator type metadata leaves an informational warning and still maps gpu counts for lsf", func() {
			wf := parseWorkflow("process GPU {\naccelerator 2, type: 'nvidia-tesla-v100'\nscript: 'echo hi'\n}\nworkflow {\nGPU()\n}\n")

			result, stderr := translateWorkflow(wf, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Scheduler: "lsf"})

			So(stderr, ShouldContainSubstring, "accelerator type")
			So(stderr, ShouldContainSubstring, "nvidia-tesla-v100")
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Other["scheduler_misc"], ShouldContainSubstring, "ngpus_physical=2")
		})

		Convey("non-lsf schedulers leave accelerator untranslated and emit a warning", func() {
			wf := parseWorkflow("process GPU {\naccelerator 1\nscript: 'echo hi'\n}\nworkflow {\nGPU()\n}\n")

			result, stderr := translateWorkflow(wf, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work"})

			So(stderr, ShouldContainSubstring, "accelerator directive is only applied for lsf scheduling")
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Other["scheduler_misc"], ShouldEqual, "")
		})

		Convey("accelerator requirements merge with existing clusterOptions for lsf", func() {
			wf := parseWorkflow("process GPU {\naccelerator 1\nclusterOptions '--account=mylab'\nscript: 'echo hi'\n}\nworkflow {\nGPU()\n}\n")

			result, stderr := translateWorkflow(wf, TranslateConfig{RunID: "r1", WorkflowName: "wf", Cwd: "/work", Scheduler: "lsf"})

			So(stderr, ShouldEqual, "")
			So(result.Jobs, ShouldHaveLength, 1)
			So(result.Jobs[0].Requirements.Other["scheduler_misc"], ShouldContainSubstring, "--account=mylab")
			So(result.Jobs[0].Requirements.Other["scheduler_misc"], ShouldContainSubstring, "ngpus_physical=1")
		})
	})
}

func captureTranslateD1Stderr(run func()) string {
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
