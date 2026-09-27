//go:build reliability_repro

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

// This file keeps the deliberately-red probe rows that found the key-blind
// residual written up in .docs/cwd_matters_cleanup/readme.md, "Measured
// residuals": two runs of one Job.Key() can configure their mounts and caches
// differently, so a finished run whose ActualCwd names a live run's workspace
// can delete that run's un-uploaded writable-mount output. It is gated behind
// the `reliability_repro` build tag so it is NOT part of `make test`; run it
// with:
//
//	go test -tags reliability_repro ./jobqueue/ -run TestProbeMountConfigsOneKeyCovers -count=1
//
// Row 1 PASSES: its live remote is a real FUSE mount, and the mount-boundary
// guard stops the sweep crossing into it. Rows 2-4 are EXPECTED TO FAIL, on
// REMOTE_DATA deleted under <ws>/s3.example.com/bucket, <ws>/tmp and
// <ws>/.muxfys_cache*: content awaiting upload is a plain directory, so no
// boundary guard sees it, and the stale run's keep set does not name it. They
// fail only because each row forces the path collision by hand, removing a
// workspace and recreating the same path through mkCwdAndTmp, and #578's
// per-run UUID mint makes that collision unreachable in production. They are
// kept as the measurement of what a collision from any other cause would cost.
//
// The fixtures and helpers are shared with deletion_probes_test.go,
// workspace_test.go and behaviours_test.go.

package jobqueue

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// probeKeyBlindRow is a PAIR of MountConfigs that Job.Key() cannot tell apart,
// together with what one run configured that way stands to lose to the cleanup
// of another.
//
// The ONE thing the workspace resolution establishes about a reported working
// directory is the key that built its path (relIsJobCreatedCwd). So if a
// finished run's own ActualCwd ever names a LIVE run's workspace byte for byte,
// the "another run of the same job" row of deletion_probes_test.go accepts it,
// and nothing but the stale run's own keep set stands between the sweep and the
// live run's data. One job by key sounds as if it must mount the same remote at
// the same place, so that keep set would name it.
//
// These rows are the counterexample to that reasoning. MountConfigs.Key()
// (mount.go) reads only Mount, Target.Profile and Target.Path, and normalises an
// EMPTY Mount to "mnt" - while resolveMountPoint (job.go) gives an empty Mount
// the working DIRECTORY of a Job wr made one for. Nothing that decides where a
// cache lands - CacheBase, Target.CacheDir, Cache, Write - is read at all. So
// one key covers mount configs whose live mounts and un-uploaded output are in
// different places, and the keep set the stale run computes does not name the
// live run's.
//
// KNOWN FAILING (rows 2-4): each is a deletion wr really makes, of a writable
// mount's output before Unmount uploaded it, once the collision is forced.
// Fixing it belongs in production code, not here.
type probeKeyBlindRow struct {
	name string

	// live is the MountConfig of the run whose data is on disk now, and stale
	// that of the finished run of the same key whose ActualCwd names the live
	// run's workspace.
	live  MountConfig
	stale MountConfig

	// mount, when set, is where the live run's remote is really FUSE mounted,
	// spelled relative to the WORKSPACE, as probeFuseRow spells its own. A row
	// that leaves it unset needs no mount, because what it stands to lose is
	// cache content waiting for Unmount rather than the remote itself.
	mount string

	// cache are the directories, spelled relative to the workspace, that hold
	// the live run's un-uploaded writable output. A file is planted in each, and
	// must survive.
	cache []string
}

func probeKeyBlindRows() []probeKeyBlindRow {
	// what muxfys names the entries of a writable S3 target's cache dir: the
	// endpoint the Profile resolved to, then the bucket, then the path (its
	// s3.go LocalPath). wr cannot enumerate those, which is why a CacheDir that
	// IS the workspace keeps the whole workspace.
	const s3CacheTree = "s3.example.com/bucket"

	return []probeKeyBlindRow{
		{
			// `wr add --mounts cw:bucket/path` gives no Mount at all, which for a
			// Job wr made a working directory for means mount ON that directory;
			// `wr add --mount_json` with a Mount of "mnt" is the same job by key.
			// So the stale run's keep set names <cwd>/mnt while every entry of
			// the live run's working directory is the user's remote data.
			name:  "the live run took the default mount point and the stale run named mnt",
			live:  MountConfig{Targets: probeCachedTargets()},
			stale: MountConfig{Mount: testWSMount, Targets: probeCachedTargets()},
			mount: createdCwdName,
		},
		{
			// a CacheDir that IS the workspace makes the workspace root where a
			// writable mount's output waits for Unmount, under names only the
			// remote knows, so keptDirs keeps the whole workspace for it. The
			// stale run configured no CacheDir, and CacheDir is not part of the
			// key, so its keep set keeps nothing of the sort.
			name: "the live run's cache dir IS its workspace and the stale run named none",
			live: MountConfig{Mount: testWSMount, Targets: []MountTarget{{
				Path: testWSTargetPath, CacheDir: ".", Write: true,
			}}},
			stale: MountConfig{Mount: testWSMount, Targets: []MountTarget{{
				Path: testWSTargetPath, Write: true,
			}}},
			cache: []string{s3CacheTree},
		},
		{
			// a cache dir inside the job's TMPDIR has to survive the SEPARATE
			// removal Execute makes of that dir on every exit, which asks the
			// same keep set. The stale run configured no cache, so nothing
			// claims the tmp entry for it.
			name: "the live run cached in its TMPDIR and the stale run named no cache",
			live: MountConfig{Mount: testWSMount, Targets: []MountTarget{{
				Path: testWSTargetPath, CacheDir: createdTmpName, Write: true,
			}}},
			stale: MountConfig{Mount: testWSMount, Targets: []MountTarget{{
				Path: testWSTargetPath, Write: true,
			}}},
			cache: []string{createdTmpName},
		},
		{
			// with no CacheBase given, muxfys chooses a cache dir of its own
			// inside the workspace, which the muxfysCachePrefix rule covers - but
			// only for a Job whose own configuration puts one there. The stale
			// run pointed its CacheBase elsewhere, and CacheBase is not part of
			// the key either, so that rule is off for it.
			name:  "the live run cached in the default base and the stale run cached elsewhere",
			live:  MountConfig{Mount: testWSMount, Targets: probeCachedTargets()},
			stale: MountConfig{Mount: testWSMount, CacheBase: "../elsewhere", Targets: probeCachedTargets()},
			cache: []string{probeMuxfysDir},
		},
	}
}

func TestProbeMountConfigsOneKeyCovers(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a live and a finished run of one key, configured differently where the key cannot look", t, func() {
		for _, row := range probeKeyBlindRows() {
			Convey(row.name, func() {
				if row.mount != "" && !probeCanMountFuse() {
					SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

					return
				}

				cwd := t.TempDir()
				precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

				live := &Job{Cwd: cwd, Cmd: probeCmd, MountConfigs: MountConfigs{row.live}}
				stale := &Job{Cwd: cwd, Cmd: probeCmd, MountConfigs: MountConfigs{row.stale}}

				// the premise: the two configs are one Job, so the path the
				// stale run reports is one the live run's key builds too.
				So(stale.Key(), ShouldEqual, live.Key())

				// force the collision #578's per-run mint makes unreachable: the
				// stale run's workspace goes, and the live run is handed that same
				// path, so the ActualCwd the stale run reports is the live run's.
				finished, workSpace, _ := realWorkSpace(stale)

				So(os.RemoveAll(workSpace), ShouldBeNil)
				So(os.MkdirAll(workSpace, os.ModePerm), ShouldBeNil)

				liveCwd, _, err := mkCwdAndTmp(workSpace)
				So(err, ShouldBeNil)
				So(liveCwd, ShouldEqual, finished)

				live.ActualCwd = liveCwd

				planted := probePlant(workSpace, row.cache)

				var remote string

				if row.mount != "" {
					var mounted bool

					remote, mounted = probeMountRemote(t, filepath.Join(workSpace, row.mount))
					if !mounted {
						return
					}
				}

				snap := stale.workSpaceSnapshot()
				cleanErr := snap.cleanupWorkSpace()
				tmpErr := snap.removeTmpDir()

				soPathsExist(planted...)

				if remote != "" {
					soPathsExist(remote)
					soPathsExist(filepath.Join(workSpace, row.mount, probeRemoteName))
				}

				soPathsExist(precious, cwd)

				So(cleanErr, ShouldBeNil)
				So(tmpErr, ShouldBeNil)
			})
		}
	})
}
