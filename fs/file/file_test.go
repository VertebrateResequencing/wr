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

package file

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// fileMode is the mode of the temp file created for testing.
const fileMode os.FileMode = 0600

const (
	// hugeFileBytes is the size of the files used to prove that GetFirstLine
	// does not read a whole file to get its first line: a --monitor_docker
	// glob can match a job's output file, which is arbitrarily large.
	hugeFileBytes = 32 * 1024 * 1024

	// maxFirstLineAllocBytes is the most GetFirstLine may allocate to return
	// one short first line, however large the rest of the file is.
	maxFirstLineAllocBytes = 1024 * 1024

	// maxShownLineBytes is how much of an unexpectedly long line a failed
	// assertion prints.
	maxShownLineBytes = 20
)

func TestFile(t *testing.T) {
	Convey("We can the first line of a file", t, func() {
		tempDir, err := os.MkdirTemp("", "temp_filepath")
		if err != nil {
			log.Fatal(err)
		}

		defer os.RemoveAll(tempDir)

		Convey("when the file exists", func() {
			tempFile := filepath.Join(tempDir, "tempFile.txt")
			err = os.WriteFile(tempFile, []byte("id1"), fileMode)
			So(err, ShouldBeNil)

			id, err := GetFirstLine(tempFile)
			So(err, ShouldBeNil)
			So(id, ShouldEqual, "id1")

			tempFile1 := filepath.Join(tempDir, "tempFile1.txt")
			err = os.WriteFile(tempFile1, []byte("id1\n"), fileMode)
			So(err, ShouldBeNil)

			id, err = GetFirstLine(tempFile1)
			So(err, ShouldBeNil)
			So(id, ShouldNotEqual, "id1\n")
			So(id, ShouldEqual, "id1")
		})

		Convey("not when the file doesn't exist", func() {
			tempNonExistingFile := filepath.Join(tempDir, "tempNonExisting.txt")
			noID, err := GetFirstLine(tempNonExistingFile)
			So(err, ShouldNotBeNil)
			So(noID, ShouldBeEmpty)
		})
	})

	Convey("Given a path to a file check it's content", t, func() {
		empContent, err := ToString("")
		So(err, ShouldNotBeNil)
		So(empContent, ShouldBeEmpty)

		home, herr := os.UserHomeDir()
		So(herr, ShouldEqual, nil)

		filepth := filepath.Join(home, "testing_pathtocontent.text")
		defer os.Remove(filepth)

		file, err := os.Create(filepth)
		So(err, ShouldEqual, nil)

		_, err = file.WriteString("hello")
		So(err, ShouldEqual, nil)

		content, err := ToString(filepth)
		So(content, ShouldEqual, "hello")
		So(err, ShouldEqual, nil)

		content, err = ToString("random.txt")
		So(content, ShouldEqual, "")
		So(err, ShouldNotBeNil)
	})
}

func TestGetFirstLine(t *testing.T) {
	Convey("GetFirstLine returns only the first line of a multi-line file", t, func() {
		path := filepath.Join(t.TempDir(), "lines.txt")
		err := os.WriteFile(path, []byte("id1\nsome other output\n"), fileMode)
		So(err, ShouldBeNil)

		line, err := GetFirstLine(path)
		So(err, ShouldBeNil)
		So(line, ShouldEqual, "id1")
	})

	Convey("GetFirstLine gets a short first line without reading a huge file", t, func() {
		path := filepath.Join(t.TempDir(), "huge.txt")
		So(writeHugeFile(path, "id1\n"), ShouldBeNil)

		var (
			line string
			err  error
		)

		allocated := allocatedBy(func() {
			line, err = GetFirstLine(path)
		})

		So(allocated, ShouldBeLessThan, maxFirstLineAllocBytes)
		So(err, ShouldBeNil)
		So(shorten(line), ShouldEqual, "id1")
	})

	Convey("GetFirstLine returns a first line of exactly maxFirstLineBytes", t, func() {
		path := filepath.Join(t.TempDir(), "atlimit.txt")
		line := strings.Repeat("a", maxFirstLineBytes)
		So(os.WriteFile(path, []byte(line+"\n"), fileMode), ShouldBeNil)

		got, err := GetFirstLine(path)
		So(err, ShouldBeNil)
		So(len(got), ShouldEqual, maxFirstLineBytes)
	})

	Convey("GetFirstLine refuses a huge file that has no newline at all", t, func() {
		path := filepath.Join(t.TempDir(), "nonewline.txt")
		So(writeHugeFile(path, ""), ShouldBeNil)

		var (
			line string
			err  error
		)

		allocated := allocatedBy(func() {
			line, err = GetFirstLine(path)
		})

		So(allocated, ShouldBeLessThan, maxFirstLineAllocBytes)
		So(errors.Is(err, ErrLineTooLong), ShouldBeTrue)
		So(line, ShouldBeEmpty)
	})
}

// writeHugeFile creates a file of hugeFileBytes bytes that starts with the
// given content and is padded out with NUL bytes, which contain no newline.
func writeHugeFile(path, start string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode)
	if err != nil {
		return err
	}

	if _, err = f.WriteString(start); err != nil {
		return err
	}

	if err = f.Truncate(hugeFileBytes); err != nil {
		return err
	}

	return f.Close()
}

// allocatedBy returns how many bytes were allocated on the heap while running
// fn.
func allocatedBy(fn func()) uint64 {
	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	fn()

	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

// shorten truncates s, so that an assertion that fails because GetFirstLine
// returned the whole of a huge file prints something a human can read.
func shorten(s string) string {
	if len(s) > maxShownLineBytes {
		return s[:maxShownLineBytes] + "..."
	}

	return s
}
