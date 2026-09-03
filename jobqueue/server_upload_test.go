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

package jobqueue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestServerUploadFile(t *testing.T) {
	ctx := context.Background()

	Convey("Given a server with an upload directory", t, func() {
		s := &Server{uploadDir: t.TempDir()}

		Convey("Uploading to a caller-named path that holds a longer file replaces its content", func() {
			savePath := filepath.Join(t.TempDir(), "config.yml")
			old := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
			err := os.WriteFile(savePath, []byte(old), ownerReadWrite)
			So(err, ShouldBeNil)

			newContent := "short\n"
			returned, err := s.uploadFile(ctx, strings.NewReader(newContent), savePath)

			content, errr := os.ReadFile(savePath)
			So(errr, ShouldBeNil)
			So(string(content), ShouldEqual, newContent)
			So(returned, ShouldEqual, savePath)
			So(err, ShouldBeNil)
		})

		Convey("Uploading to a caller-named path that holds a shorter file replaces its content", func() {
			savePath := filepath.Join(t.TempDir(), "config.yml")
			err := os.WriteFile(savePath, []byte("tiny\n"), ownerReadWrite)
			So(err, ShouldBeNil)

			newContent := "a much longer replacement payload\n"
			returned, err := s.uploadFile(ctx, strings.NewReader(newContent), savePath)

			content, errr := os.ReadFile(savePath)
			So(errr, ShouldBeNil)
			So(string(content), ShouldEqual, newContent)
			So(returned, ShouldEqual, savePath)
			So(err, ShouldBeNil)
		})

		Convey("Uploading with an empty savePath stores the data at an md5-based path", func() {
			content := "md5 named content\n"

			first, err := s.uploadFile(ctx, strings.NewReader(content), "")
			So(err, ShouldBeNil)
			So(first, ShouldStartWith, s.uploadDir)

			stored, errr := os.ReadFile(first)
			So(errr, ShouldBeNil)
			So(string(stored), ShouldEqual, content)

			Convey("And uploading identical content again generates no error and reuses the path", func() {
				second, errs := s.uploadFile(ctx, strings.NewReader(content), "")

				stored, errr := os.ReadFile(second)
				So(errr, ShouldBeNil)
				So(string(stored), ShouldEqual, content)
				So(second, ShouldEqual, first)
				So(errs, ShouldBeNil)

				entries, errg := filepath.Glob(filepath.Join(s.uploadDir, "file_upload*"))
				So(errg, ShouldBeNil)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}
