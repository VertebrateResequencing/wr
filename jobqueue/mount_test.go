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
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// testResolveCwd is the directory the user is in when they configure the mounts
// of the Resolve tests.
const testResolveCwd = "/user/dir"

// testResolveBucket is the remote path of the targets of those mounts.
const testResolveBucket = "bucket/path"

func TestMountConfigsResolve(t *testing.T) {
	if runnermode || servermode {
		return
	}

	for _, tc := range []struct {
		name          string
		config        MountConfig
		wantMount     string
		wantCacheBase string
		wantCacheDirs []string
	}{
		{
			name:          "an unconfigured mount is in the mnt subdirectory of the user's directory",
			config:        MountConfig{Targets: []MountTarget{{Path: testResolveBucket, Cache: true}}},
			wantMount:     "/user/dir/mnt",
			wantCacheBase: "/user/dir",
			wantCacheDirs: []string{""},
		},
		{
			name: "relative paths are relative to the user's directory",
			config: MountConfig{
				Mount:     "mymnt",
				CacheBase: "cache",
				Targets:   []MountTarget{{Path: testResolveBucket, CacheDir: "target1"}},
			},
			wantMount:     "/user/dir/mymnt",
			wantCacheBase: "/user/dir/cache",
			wantCacheDirs: []string{"/user/dir/target1"},
		},
		{
			name: "relative paths can climb out of the user's directory",
			config: MountConfig{
				Mount:     "../shared/mnt",
				CacheBase: "../shared/cache",
				Targets:   []MountTarget{{Path: testResolveBucket, CacheDir: "../shared/target1"}},
			},
			wantMount:     "/user/shared/mnt",
			wantCacheBase: "/user/shared/cache",
			wantCacheDirs: []string{"/user/shared/target1"},
		},
		{
			name: "absolute paths are left alone",
			config: MountConfig{
				Mount:     "/elsewhere/mnt",
				CacheBase: "/elsewhere/cache",
				Targets: []MountTarget{
					{Path: testResolveBucket, CacheDir: "/elsewhere/target1"},
					{Path: testResolveBucket, CacheDir: "/elsewhere/target2"},
				},
			},
			wantMount:     "/elsewhere/mnt",
			wantCacheBase: "/elsewhere/cache",
			wantCacheDirs: []string{"/elsewhere/target1", "/elsewhere/target2"},
		},
		{
			name: "home directory paths are left for the mount to expand",
			config: MountConfig{
				Mount:     "~/mnt",
				CacheBase: "~/cache",
				Targets:   []MountTarget{{Path: testResolveBucket, CacheDir: "~/target1"}},
			},
			wantMount:     "~/mnt",
			wantCacheBase: "~/cache",
			wantCacheDirs: []string{"~/target1"},
		},
	} {
		Convey("Resolve says "+tc.name, t, func() {
			resolved := MountConfigs{tc.config}.Resolve(testResolveCwd)

			So(resolved, ShouldHaveLength, 1)
			So(resolved[0].Mount, ShouldEqual, tc.wantMount)
			So(resolved[0].CacheBase, ShouldEqual, tc.wantCacheBase)
			So(resolvedCacheDirs(resolved[0]), ShouldResemble, tc.wantCacheDirs)
		})
	}

	Convey("Resolve does not alter the MountConfigs it was asked about", t, func() {
		mcs := MountConfigs{{Targets: []MountTarget{{Path: testResolveBucket, CacheDir: "cache"}}}}

		resolved := mcs.Resolve(testResolveCwd)

		So(resolved[0].Mount, ShouldEqual, "/user/dir/mnt")
		So(resolved[0].Targets[0].CacheDir, ShouldEqual, "/user/dir/cache")
		So(mcs[0].Mount, ShouldEqual, "")
		So(mcs[0].CacheBase, ShouldEqual, "")
		So(mcs[0].Targets[0].CacheDir, ShouldEqual, "cache")
	})
}

// resolvedCacheDirs returns the CacheDir of each of the MountConfig's Targets.
func resolvedCacheDirs(mc MountConfig) []string {
	dirs := make([]string, len(mc.Targets))

	for i, mt := range mc.Targets {
		dirs[i] = mt.CacheDir
	}

	return dirs
}
