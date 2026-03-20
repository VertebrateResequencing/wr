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

func TestParams(t *testing.T) {
	Convey("params helpers handle B2 parameter substitution", t, func() {
		Convey("SubstituteParams replaces interpolated params references", func() {
			result, err := SubstituteParams("cat ${params.input}/file.txt", map[string]any{"input": "/data"})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "cat /data/file.txt")
		})

		Convey("SubstituteParams resolves nested dot-separated params references", func() {
			result, err := SubstituteParams("${params.input.file}", map[string]any{"input": map[string]any{"file": "x.fq"}})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "x.fq")
		})

		Convey("SubstituteParams errors when a referenced param is missing", func() {
			_, err := SubstituteParams("${params.missing}", map[string]any{"a": "1"})

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "params.missing")
		})

		Convey("LoadParams reads JSON params files", func() {
			path := filepath.Join(t.TempDir(), "params.json")
			err := os.WriteFile(path, []byte(`{"x":1}`), 0o600)

			So(err, ShouldBeNil)

			params, loadErr := LoadParams(path)

			So(loadErr, ShouldBeNil)
			So(params, ShouldResemble, map[string]any{"x": 1})
		})

		Convey("LoadParams reads YAML params files", func() {
			path := filepath.Join(t.TempDir(), "params.yaml")
			err := os.WriteFile(path, []byte("x: 1\n"), 0o600)

			So(err, ShouldBeNil)

			params, loadErr := LoadParams(path)

			So(loadErr, ShouldBeNil)
			So(params, ShouldResemble, map[string]any{"x": 1})
		})

		Convey("MergeParams lets file params override config params", func() {
			merged := MergeParams(map[string]any{"a": "1"}, map[string]any{"a": "2"})

			So(merged, ShouldResemble, map[string]any{"a": "2"})
		})

		Convey("SubstituteParams replaces bare params references", func() {
			result, err := SubstituteParams("cat params.input/file.txt", map[string]any{"input": "/data"})

			So(err, ShouldBeNil)
			So(result, ShouldEqual, "cat /data/file.txt")
		})

		Convey("MergeParams applies later sources in order through CLI params", func() {
			merged := MergeParams(
				map[string]any{"input": "/cfg"},
				map[string]any{"input": "/file"},
				map[string]any{"input": "/cli"},
			)

			So(merged["input"], ShouldEqual, "/cli")
		})
	})
}
