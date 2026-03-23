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

	. "github.com/smartystreets/goconvey/convey"
)

func TestParseOutputBlockN1(t *testing.T) {
	parseTargets := func(source string) ([]*OutputTarget, string, error) {
		wf, err := Parse(strings.NewReader(source))
		So(err, ShouldBeNil)

		var targets []*OutputTarget
		stderr := captureTranslateStderr(func() {
			targets, err = ParseOutputBlock(wf.OutputBlock)
		})

		return targets, stderr, err
	}

	Convey("ParseOutputBlock handles N1 output block targets", t, func() {
		Convey("static target paths populate Name and Path", func() {
			targets, stderr, err := parseTargets("output { samples { path 'results/fastq' } }")

			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")
			So(targets, ShouldHaveLength, 1)
			So(targets[0].Name, ShouldEqual, "samples")
			So(targets[0].Path, ShouldEqual, "results/fastq")
			So(targets[0].IndexPath, ShouldEqual, "")
		})

		Convey("nested index blocks populate IndexPath", func() {
			targets, stderr, err := parseTargets("output { samples { path 'fastq'; index { path 'index.csv' } } }")

			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")
			So(targets, ShouldHaveLength, 1)
			So(targets[0].Name, ShouldEqual, "samples")
			So(targets[0].Path, ShouldEqual, "fastq")
			So(targets[0].IndexPath, ShouldEqual, "index.csv")
		})

		Convey("empty output blocks return no targets", func() {
			targets, stderr, err := parseTargets("output { }")

			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")
			So(targets, ShouldHaveLength, 0)
		})

		Convey("multiple named targets are all returned", func() {
			targets, stderr, err := parseTargets("output { a { path 'x' }; b { path 'y' } }")

			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")
			So(targets, ShouldHaveLength, 2)
			So(targets[0].Name, ShouldEqual, "a")
			So(targets[0].Path, ShouldEqual, "x")
			So(targets[1].Name, ShouldEqual, "b")
			So(targets[1].Path, ShouldEqual, "y")
		})

		Convey("closure-valued paths warn and skip the target", func() {
			targets, stderr, err := parseTargets("output { a { path { meta -> \"${meta.id}\" } } }")

			So(err, ShouldBeNil)
			So(targets, ShouldHaveLength, 0)
			So(stderr, ShouldContainSubstring, "closure-valued path")
			So(stderr, ShouldContainSubstring, "skipping target")
		})
	})
}
