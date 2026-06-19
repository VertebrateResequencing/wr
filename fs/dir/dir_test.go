/*******************************************************************************
 * Copyright (c) 2021 Genome Research Ltd.
 *
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 ******************************************************************************/

package dir

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
)

func TestDir(t *testing.T) {
	Convey("We can get the present working directory", t, func() {
		ctx := context.Background()
		origPWD := GetPWD(ctx)
		So(origPWD, ShouldNotBeEmpty)

		tempDir := os.TempDir()
		err := os.Chdir(tempDir)
		So(err, ShouldBeNil)

		defer func() {
			err = os.Chdir(origPWD)
			So(err, ShouldBeNil)
		}()

		pwd := GetPWD(ctx)
		So(strings.TrimSuffix(pwd, "/"), ShouldEndWith, strings.TrimSuffix(tempDir, "/"))

		Convey("unless it doesn't exist", func() {
			dir := filepath.Join(tempDir, "non")
			err = os.MkdirAll(dir, fs.ModePerm)
			So(err, ShouldBeNil)

			err := os.Chdir(dir)
			So(err, ShouldBeNil)

			err = os.RemoveAll(dir)
			So(err, ShouldBeNil)

			buff := clog.ToBufferAtLevel("fatal")

			defer clog.ToDefault()

			os.Setenv("WR_FATAL_EXIT_TEST", "1")

			defer os.Unsetenv("WR_FATAL_EXIT_TEST")

			_ = GetPWD(ctx)

			bufferStr := buff.String()

			if bufferStr == "" {
				pwd, err = os.Getwd()
				So(err, ShouldBeNil)
				So(pwd, ShouldNotEqual, dir)
				SkipConvey("can't test GetPWD failure on a system that never fails pwd", func() {})

				return
			}

			So(bufferStr, ShouldContainSubstring, "fatal=true")
			So(bufferStr, ShouldNotContainSubstring, "caller=clog")
			So(bufferStr, ShouldContainSubstring, "stack=")
			So(bufferStr, ShouldContainSubstring, "no such file or directory")
		})
	})

	Convey("We can get the home directory", t, func() {
		ctx := context.Background()

		origHome := os.Getenv("HOME")
		home := GetHome(ctx)
		So(home, ShouldEqual, origHome)

		Convey("but not when HOME env is set to empty", func() {
			os.Setenv("HOME", "")
			defer os.Setenv("HOME", origHome)

			buff := clog.ToBufferAtLevel("fatal")

			defer clog.ToDefault()

			os.Setenv("WR_FATAL_EXIT_TEST", "1")

			defer os.Unsetenv("WR_FATAL_EXIT_TEST")

			_ = GetHome(ctx)

			bufferStr := buff.String()
			So(bufferStr, ShouldContainSubstring, "fatal=true")
			So(bufferStr, ShouldNotContainSubstring, "caller=clog")
			So(bufferStr, ShouldContainSubstring, "stack=")
			So(bufferStr, ShouldContainSubstring, "could not find home dir")
		})
	})
}
