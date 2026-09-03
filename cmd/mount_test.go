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

package cmd

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestMountResolvesToUsersDir(t *testing.T) {
	Convey("Given a user who mounts a bucket with --mounts and no other options", t, func() {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		So(err, ShouldBeNil)

		setMountOptionsForTest(t, "", "cw:mybucket/path")

		Convey("the mount point and cache are in the directory they ran wr mount in", func() {
			t.Chdir(dir)

			configs := resolvedMountConfigs()

			So(configs, ShouldHaveLength, 1)
			So(configs[0].Mount, ShouldEqual, filepath.Join(dir, "mnt"))
			So(configs[0].CacheBase, ShouldEqual, dir)
		})

		Convey("they are in that directory for the daemon too, which runs from /", func() {
			t.Setenv(mountCwdEnvVar, dir)
			t.Chdir("/")

			configs := resolvedMountConfigs()

			So(configs, ShouldHaveLength, 1)
			So(configs[0].Mount, ShouldEqual, filepath.Join(dir, "mnt"))
			So(configs[0].CacheBase, ShouldEqual, dir)
		})
	})

	Convey("Given a user who mounts with a relative --mount_json CacheDir", t, func() {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		So(err, ShouldBeNil)

		setMountOptionsForTest(t, `[{"Mount":"mymnt","Targets":[{"Path":"mybucket","CacheDir":"mycache"}]}]`, "")

		Convey("the mount point and cache dir are in the directory the daemon was started from", func() {
			t.Setenv(mountCwdEnvVar, dir)
			t.Chdir("/")

			configs := resolvedMountConfigs()

			So(configs, ShouldHaveLength, 1)
			So(configs[0].Mount, ShouldEqual, filepath.Join(dir, "mymnt"))
			So(configs[0].Targets, ShouldHaveLength, 1)
			So(configs[0].Targets[0].CacheDir, ShouldEqual, filepath.Join(dir, "mycache"))
		})
	})
}

// setMountOptionsForTest sets the mount command's option variables for the
// duration of the test, restoring them afterwards.
func setMountOptionsForTest(t *testing.T, jsonString, simpleString string) {
	t.Helper()

	oldJSON := mountJSON
	oldSimple := mountSimple

	t.Cleanup(func() {
		mountJSON = oldJSON
		mountSimple = oldSimple
	})

	mountJSON = jsonString
	mountSimple = simpleString
}
