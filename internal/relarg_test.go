// Copyright © 2025 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

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
