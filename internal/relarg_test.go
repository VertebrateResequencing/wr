/*******************************************************************************
 * Copyright (c) 2025 Genome Research Ltd.
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

package internal

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestCmdlineHasRelativePaths(t *testing.T) {
	Convey("Given a file in a directory and a subdir", t, func() {
		dir := t.TempDir()
		dirName := filepath.Base(dir)
		pathBase := "file"
		absPath := filepath.Join(dir, pathBase)
		subdir := "subdir"
		subdirPath := filepath.Join(dir, subdir)

		f, err := os.Create(absPath)
		So(err, ShouldBeNil)
		err = f.Close()
		So(err, ShouldBeNil)

		err = os.Mkdir(subdirPath, 0755)
		So(err, ShouldBeNil)

		filesInDir := GetFilesInDir(dir)
		So(filesInDir, ShouldNotBeNil)
		So(len(filesInDir), ShouldEqual, 2)
		So(filesInDir[absPath], ShouldBeTrue)
		So(filesInDir[subdirPath], ShouldBeTrue)
		So(filesInDir[filepath.Join(dir, "nonexistent")], ShouldBeFalse)

		Convey("It is correctly detected as relative or not as part of a command line", func() {
			for _, test := range [...]struct {
				cmdline  string
				expected bool
			}{
				{"", false},
				{"cmd --foo", false},
				{"cmd --foo " + pathBase, true},
				{"cmd --foo " + absPath, false},
				{"cmd $(cat " + pathBase + ")", true},
				{"cmd $(cat " + absPath + ")", false},
				{"cmd foo=" + pathBase, true},
				{"cmd foo=" + absPath, false},
				{"cmd && cat " + pathBase, true},
				{"cmd && cat " + absPath, false},
				{"echo " + pathBase + "; true", true},
				{"echo " + absPath + "; true", false},
				{"echo ./" + pathBase, true},
				{"echo ../" + pathBase, false},
				{"echo ../" + dirName + "/" + pathBase, true},
				{"file " + absPath, false},
				{"cmd *", true},
				{"cmd ./*", true},
				{"cmd " + dirName + "/*", false},
				{"cmd ./" + string(pathBase[0]) + "*", true},
				{"cmd ./x*", false},
				{"cmd ./" + subdir + "/*", true},
				{"cmd ./x/*", false},
				{"cmd " + string(pathBase[0]) + "*", true},
				{"cmd x*", false},
				{"cmd *" + pathBase[1:], true},
				{"cmd *x", false},
				{"cmd ?" + pathBase[1:], true},
				{"cmd ?x", false},
			} {
				isRel := CmdlineHasRelativePaths(filesInDir, dir, test.cmdline)

				if isRel != test.expected {
					t.Logf("\n%s\n", test.cmdline)
				}

				So(isRel, ShouldEqual, test.expected)
			}
		})
	})
}
