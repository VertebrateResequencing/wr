/*******************************************************************************
 * Copyright (c) 2021 Genome Research Ltd.
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

package test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestTestFuncs(t *testing.T) {
	Convey("We can mock the STDIN and write to it", t, func() {
		origStdin, stdinWriter, err := mockStdInRW()
		So(origStdin, ShouldNotBeNil)
		So(stdinWriter, ShouldNotBeNil)
		So(err, ShouldBeNil)

		_, err = stdinWriter.WriteString("test\n")
		So(err, ShouldBeNil)

		var response string
		fmt.Scanf("%s\n", &response)
		So(response, ShouldEqual, "test")

		os.Stdin = origStdin
		stdinWriter.Close()
	})

	Convey("Given a mocked STDIN", t, func() {
		mockedStdIn, err := NewMockStdIn()
		defer mockedStdIn.RestoreStdIn()
		So(mockedStdIn, ShouldNotBeNil)
		So(err, ShouldBeNil)

		Convey("we can write to it", func() {
			err = mockedStdIn.WriteString("test2")

			var response string
			fmt.Scanf("%s\n", &response)
			So(response, ShouldEqual, "test2")
		})

		Convey("and restore it to default", func() {
			mockedStdIn.RestoreStdIn()
			So(mockedStdIn.stdinWriter, ShouldBeNil)
			So(mockedStdIn.origStdin, ShouldNotBeNil)
		})
	})

	Convey("We can mock the STDERR", t, func() {
		origStderr, stderrReader, outCh, err := mockStdErrRW()
		So(origStderr, ShouldNotBeNil)
		So(stderrReader, ShouldNotBeNil)
		So(outCh, ShouldNotBeNil)
		So(err, ShouldBeNil)

		os.Stderr = origStderr
		stderrReader.Close()
	})

	Convey("Given a mocked STDERR", t, func() {
		mockedStdErr, err := NewMockStdErr()
		defer mockedStdErr.RestoreStdErr()
		So(mockedStdErr, ShouldNotBeNil)
		So(err, ShouldBeNil)

		Convey("we can read from it and restore it to default", func() {
			fmt.Fprintf(os.Stderr, "test stderr")
			stdErr, errg := mockedStdErr.GetAndRestoreStdErr()
			So(errg, ShouldBeNil)
			So(stdErr, ShouldContainSubstring, "test stderr")
			So(mockedStdErr.stderrReader, ShouldBeNil)
			So(mockedStdErr.origStderr, ShouldNotBeNil)

			Convey("but not when it is already closed", func() {
				_, errg = mockedStdErr.GetAndRestoreStdErr()
				So(errg, ShouldNotBeNil)
			})
		})

		Convey("reading fails if we close the reader", func() {
			err = mockedStdErr.stderrReader.Close()
			So(err, ShouldBeNil)

			fmt.Fprintf(os.Stderr, "test stderr")
			stdErr, err := mockedStdErr.GetAndRestoreStdErr()
			So(err, ShouldBeNil)
			So(stdErr, ShouldContainSubstring, "file already closed")
			So(stdErr, ShouldNotContainSubstring, "test stderr")
		})
	})

	Convey("FilePathInTempDir returns a non-existent path in an existing tmp dir", t, func() {
		basename := "foo"
		path := FilePathInTempDir(t, basename)
		So(path, ShouldStartWith, os.TempDir())
		So(path, ShouldEndWith, basename)
		_, err := os.Open(filepath.Dir(path))
		So(err, ShouldBeNil)
		_, err = os.Open(path)
		So(err, ShouldNotBeNil)
	})
}
