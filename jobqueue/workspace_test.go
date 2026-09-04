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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	gofuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	. "github.com/smartystreets/goconvey/convey"
)

// osLinux is the only GOOS that names a process's own file descriptors through
// /proc, which is what pins the directory a `run` behaviour starts in.
const osLinux = "linux"

// fixture strings shared by the workspace tests.
const (
	testWSMount      = "mnt"
	testWSTargetPath = "s3/path"
	testWSCmd        = "run"
	testWSRemote     = "REMOTE_OBJECT"
)

// keyGrindMax bounds jobWithKeyPrefix's search, which is expected to take about
// 16^len(prefix) tries.
const keyGrindMax = 1 << 20

// TestCleanupCrossesNoMountBoundaryAtTheWorkingDir drives the sweep whose device
// number comes from the working directory itself: removeAllExcept, reached by
// removeActualCwd when the Job's keep set is non-empty.
//
// The SWEPT directory is where the boundary question is blind, because every
// entry of a mount root is on the mount's own device: judged against the
// directory they are entries of, nothing inside a mount root ever looks like a
// crossing, and the sweep walks the user's mounted filesystem believing it is
// walking the workspace wr made. So the swept directory has to be judged against
// the directory ABOVE it, which is the one level where the device does change.
func TestCleanupCrossesNoMountBoundaryAtTheWorkingDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// the mount the Job's own Cmd raised is AT the working directory rather than
	// an entry of it, and the working directory is where removeAllExcept takes
	// the device it judges every entry against.
	//
	// Both sweeps are driven, because only one of them takes its device from the
	// working directory: with no mounts the keep set is empty, removeActualCwd
	// hands the whole directory to removeAllGuarded, and the device that judges
	// it is the WORKSPACE's - from which the mount is a device change and IS
	// caught, even before this fix.
	for _, mounts := range nestedSweepCases() {
		Convey("Given a Job whose Cmd raised a live mount over the working directory wr gave it", t, func() {
			if !canMountFuse() {
				SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

				return
			}

			cwd := t.TempDir()

			job := &Job{
				Cwd:          cwd,
				Cmd:          "sshfs remote:/data . && analyse",
				MountConfigs: mounts,
			}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)
			scratch := writeFileIn(tmpDir, "scratch.txt")

			// the Job's keep set is built from its MountConfigs LEXICALLY and kept
			// unconditionally (keptDirs), so the mount point inside the working
			// directory does not have to be there for the keep set to name it -
			// which is what sends the sweep down the removeAllExcept branch. It
			// being absent is also what lets fusermount 2 mount over the working
			// directory at all; see mountLoopback on the "nonempty" option.
			remote, mounted := mountLoopbackOver(t, actualCwd)
			if !mounted {
				SkipConvey("this host refused an unprivileged FUSE mount", func() {})

				return
			}

			Convey(fmt.Sprintf("cleanup with %d mounts deletes nothing through it, and still deletes what it made",
				len(mounts)), func() {
				err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)

				soPathsExist(remote, filepath.Join(actualCwd, testWSRemote), actualCwd, workSpace)
				soPathsGone(scratch, tmpDir)

				So(err, ShouldBeNil)
			})
		})
	}
}

// TestCleanupCrossesNoMountBoundaryOverAPopulatedWorkingDir is the test above
// with the ordering wr really produces: wr raises its own configured mount
// INSIDE the working directory before the Job's Cmd runs, so that directory is
// not empty when the Cmd mounts over it.
func TestCleanupCrossesNoMountBoundaryOverAPopulatedWorkingDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a working dir that held wr's own mount point when the Cmd mounted over it", t, func() {
		if !canMountFuse() {
			SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

			return
		}

		cwd := t.TempDir()
		job := &Job{
			Cwd:          cwd,
			Cmd:          "sshfs remote:/data . && analyse",
			MountConfigs: MountConfigs{{Mount: testWSMount}},
		}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)
		scratch := writeFileIn(tmpDir, "scratch.txt")

		So(os.MkdirAll(filepath.Join(actualCwd, testWSMount), os.ModePerm), ShouldBeNil)

		backing := t.TempDir()
		remote := writeFileIn(backing, testWSRemote)

		if !mountLoopback(t, actualCwd, backing, "nonempty") {
			SkipConvey("this host refused an unprivileged FUSE mount over a non-empty dir", func() {})

			return
		}

		Convey("cleanup deletes nothing through it, and still deletes what it made", func() {
			err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)

			soPathsExist(remote, filepath.Join(actualCwd, testWSRemote), actualCwd, workSpace)
			soPathsGone(scratch, tmpDir)

			So(err, ShouldBeNil)
		})
	})
}

// TestCleanupCrossesNoMountBoundaryAtTheWorkSpace drives the sweep of the
// workspace's own entries - removeWorkSpaceEntries - by making the WORKSPACE the
// mount root. It takes its device from the workspace's own Lstat("."), so it is
// blind there for the same reason, and it needs no keep set at all to be
// reached. Reclaiming the TMPDIR is blind in the same place, and is reached from
// Execute rather than from a cleanup Behaviour; see
// TestJobTmpDirRemovalCrossesNoMountBoundary.
func TestCleanupCrossesNoMountBoundaryAtTheWorkSpace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// withCwdEntry says whether the mounted remote happens to hold an entry
	// called cwd, which is what the working directory resolves to through the
	// mount. With one, the working-directory sweep runs against it as well; with
	// none, proveActualCwd's tolerated absence leaves actualCwdInfo nil and only
	// removeWorkSpaceEntries runs - and that alone is enough to wipe the remote's
	// top level.
	//
	// The mounts decide which sweep that working directory then gets, and only
	// the keep-set one reaches inside it: from the working directory's own device
	// - the mount's - nothing below is a crossing, so what refuses it is the
	// workspace being a mount root, asked one level higher up.
	for _, withCwdEntry := range []bool{false, true} {
		for _, mounts := range nestedSweepCases() {
			Convey("Given a Job whose Cmd raised a live mount over the workspace wr gave it", t, func() {
				if !canMountFuse() {
					SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

					return
				}

				cwd := t.TempDir()
				job := &Job{Cwd: cwd, Cmd: "sshfs remote:/data .. && analyse", MountConfigs: mounts}
				_, workSpace, _ := realWorkSpace(job)

				backing := t.TempDir()
				remote := writeFileIn(backing, testWSRemote)
				remoteDeep := remote

				if withCwdEntry {
					remoteDeep = writeFileIn(filepath.Join(backing, createdCwdName), "remote_output.txt")
				}

				// the workspace is never empty when the Job's own Cmd mounts over
				// it: wr's cwd, wr's tmp and the record of the run using it are all
				// in it. So raising the mount needs "nonempty"; see mountLoopback
				// on that option.
				if !mountLoopback(t, workSpace, backing, "nonempty") {
					SkipConvey("this host refused an unprivileged FUSE mount over a non-empty dir", func() {})

					return
				}

				Convey(fmt.Sprintf("cleanup with a cwd entry present=%v and %d mounts deletes nothing through it",
					withCwdEntry, len(mounts)), func() {
					err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)

					soPathsExist(remoteDeep, remote, workSpace)

					So(err, ShouldBeNil)
				})
			})
		}
	}
}

// TestJobTmpDirRemovalCrossesNoMountBoundary drives the sweep no cleanup
// Behaviour reaches: removeTmp, which Execute asks for on every exit through
// removeJobTmpDir, including for the great majority of Jobs that have no cleanup
// Behaviour at all and so never sweep anything else.
//
// It takes its device from the workspace's own Lstat("."), so with a mount the
// Job's own Cmd raised over the workspace, the tmp it deletes is whatever the
// mounted filesystem happens to call tmp.
func TestJobTmpDirRemovalCrossesNoMountBoundary(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a Job whose Cmd raised a live mount over the workspace holding its TMPDIR", t, func() {
		if !canMountFuse() {
			SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

			return
		}

		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: "sshfs remote:/data .. && analyse"}
		_, workSpace, _ := realWorkSpace(job)

		backing := t.TempDir()
		remote := writeFileIn(backing, testWSRemote)

		// what the TMPDIR wr made resolves to through the mount is the mounted
		// filesystem's own tmp, so that is what removeTmp deletes.
		remoteTmp := writeFileIn(filepath.Join(backing, createdTmpName), "remote_output.txt")

		// the workspace is never empty when the Job's own Cmd mounts over it:
		// wr's cwd, wr's tmp and the record of the run using it are all in it. So
		// raising the mount needs "nonempty"; see mountLoopback on that option.
		if !mountLoopback(t, workSpace, backing, "nonempty") {
			SkipConvey("this host refused an unprivileged FUSE mount over a non-empty dir", func() {})

			return
		}

		buff := clog.ToBufferAtLevel("warn")

		Reset(clog.ToDefault)

		Convey("reclaiming the TMPDIR deletes nothing through the mount", func() {
			removeJobTmpDir(context.Background(), job)

			soPathsExist(remoteTmp, remote, filepath.Join(workSpace, createdTmpName), workSpace)
			So(buff.String(), ShouldBeEmpty)
		})
	})
}

// realWorkSpace gives job the working directory mkHashedDir really creates for it
// below job.Cwd, and returns that dir, the workspace holding it, and the tmp dir
// wr makes beside it. The path wr builds is what proves a workspace is wr's own,
// so a hand-made fixture of merely the right shape does not reach the same
// guards.
func realWorkSpace(job *Job) (actualCwd, workSpace, tmpDir string) {
	actualCwd, tmpDir, wsToken, err := mkHashedDir(job.Cwd, job.Key())
	So(err, ShouldBeNil)

	// the token is reported with the path because production reports the pair:
	// mkCwdAndTmp records the run's identity inside the workspace, and cleanup
	// refuses a workspace whose record it cannot be shown to have written.
	job.ActualCwd = actualCwd
	job.ActualCwdToken = wsToken

	return actualCwd, filepath.Dir(actualCwd), tmpDir
}

// writeFileIn creates dir and a file within it, and returns the file's path.
func writeFileIn(dir, name string) string {
	So(os.MkdirAll(dir, os.ModePerm), ShouldBeNil)

	path := filepath.Join(dir, name)
	So(os.WriteFile(path, []byte("precious\n"), 0o600), ShouldBeNil)

	return path
}

func TestRelIsJobCreatedCwd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// relIsJobCreatedCwd is what proves a workspace really is the one wr built
	// for this Job, rather than merely sitting at the right depth under the right
	// name. It is pinned against mkHashedDir here so the two cannot drift apart:
	// if they did, cleanup would quietly stop working rather than fail loudly.
	Convey("relIsJobCreatedCwd recognises exactly what mkHashedDir creates", t, func() {
		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: "echo created"}

		actualCwd, _, _, err := mkHashedDir(cwd, job.Key())
		So(err, ShouldBeNil)

		rel, err := filepath.Rel(cwd, actualCwd)
		So(err, ShouldBeNil)
		So(relIsJobCreatedCwd(rel, job.Key()), ShouldBeTrue)

		Convey("but not another Job's working dir", func() {
			So(relIsJobCreatedCwd(rel, (&Job{Cmd: "echo different"}).Key()), ShouldBeFalse)
		})

		Convey("nor a hand-made path of the right depth and name", func() {
			So(relIsJobCreatedCwd(filepath.Join("a", "b", "c", "d", "e", createdCwdName), job.Key()),
				ShouldBeFalse)
		})

		Convey("nor the same path with anything appended or removed", func() {
			// every component this reads is at a fixed index, so it answers only
			// about paths of exactly the depth mkHashedDir builds at. A deeper path
			// whose LEAF is still called cwd is the one that matters: the job's own
			// Cmd can make it inside the dir wr made, and treating it as a working
			// directory would sweep that dir as a workspace.
			names := strings.Split(rel, string(filepath.Separator))
			deeper := slices.Concat(names[:len(names)-1], []string{"extra", createdCwdName})

			So(relIsJobCreatedCwd(filepath.Join(deeper...), job.Key()), ShouldBeFalse)
			So(relIsJobCreatedCwd(filepath.Join(rel, "deeper"), job.Key()), ShouldBeFalse)
			So(relIsJobCreatedCwd(filepath.Dir(rel), job.Key()), ShouldBeFalse)
		})

		Convey("nor one whose hashed dirs are not the ones the key builds", func() {
			// k0-k2 are the first three characters of the key, and the only
			// components of the path that both get checked and have to exist on
			// disk, so they are the ones an attacker grinds a Cmd to match.
			names := strings.Split(rel, string(filepath.Separator))
			names[2] = "z"
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)
		})

		Convey("nor one whose unique dir was not made by MkdirTemp", func() {
			// what MkdirTemp adds to the prefix is a non-empty run of DIGITS,
			// and both halves of that have to be required. The bare prefix is
			// the dir wr would have made had it not needed a unique one...
			names := strings.Split(rel, string(filepath.Separator))
			leaf := job.Key()[mkHashedLevels-1:]

			names[len(names)-2] = leaf
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)

			// ...and a suffix of anything else is a name something other than
			// MkdirTemp chose. Whoever submits the Job picks the Cmd that Key()
			// hashes, so they can put a directory of their own beside the job's at
			// exactly this prefix, and accepting it would hand its PARENT to the
			// sweep as this Job's workspace.
			names[len(names)-2] = leaf + "-mydata"
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)

			names[len(names)-2] = leaf + "12x"
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)
		})

		Convey("nor one whose leaf is not the dir mkCwdAndTmp makes", func() {
			names := strings.Split(rel, string(filepath.Separator))
			names[len(names)-1] = "tmp"
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)
		})

		Convey("and requires the base component to be named the way wr names one", func() {
			// AppName is a package var: cmd/runner.go sets it to "wr" while the
			// manager leaves it "jobqueue", and cleanup runs in both, so the
			// component must not be compared against what THIS process would build
			// - that would refuse every runner-made workspace server-side. The
			// SUFFIX is checked instead, and the hashed dirs below it are rebuilt
			// from whatever the component actually is.
			names := strings.Split(rel, string(filepath.Separator))
			names[0] = "someone_elses" + createdCwdBaseSuffix
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeTrue)

			names[0] = "results"
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)

			// the name has to END with the suffix, since that is how wr builds
			// it. A directory of the user's that merely contains it - the
			// renamed leftovers of an old workspace, say - is one of theirs.
			names[0] = "wr" + createdCwdBaseSuffix + ".old"
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)
		})
	})
}

func TestOpenVerifiedDirFile(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// the path was proven with an lstat and the open resolves it again, so between
	// the two a component of it - the working directory included - can be swapped
	// for a symlink to somewhere else inside the Job's Cwd, which an os.Root
	// follows. Only proving that what was opened is the directory that was checked
	// catches that.
	Convey("Given a directory that has been checked", t, func() {
		base := t.TempDir()
		root, err := os.OpenRoot(base)
		So(err, ShouldBeNil)

		Reset(func() { root.Close() })

		proven := filepath.Join(base, "proven")
		So(os.MkdirAll(proven, os.ModePerm), ShouldBeNil)

		info, err := root.Lstat("proven")
		So(err, ShouldBeNil)

		Convey("opening it by name gives a handle on it", func() {
			f, errr := openVerifiedDirFile(root, "proven", info)
			So(errr, ShouldBeNil)

			defer f.Close()

			opened, errr := f.Stat()
			So(errr, ShouldBeNil)
			So(os.SameFile(opened, info), ShouldBeTrue)
		})

		Convey("but not once the name leads somewhere else inside the same root", func() {
			elsewhere := filepath.Join(base, "elsewhere")
			So(os.MkdirAll(elsewhere, os.ModePerm), ShouldBeNil)
			So(os.Rename(proven, filepath.Join(base, "moved")), ShouldBeNil)
			So(os.Symlink("elsewhere", filepath.Join(base, "proven")), ShouldBeNil)

			f, errr := openVerifiedDirFile(root, "proven", info)
			So(errr, ShouldNotBeNil)
			So(errors.Is(errr, errNotBelowBaseDir), ShouldBeTrue)
			So(f, ShouldBeNil)
		})

		Convey("and nothing at all when there is nothing to prove it against", func() {
			f, errr := openVerifiedDirFile(root, "proven", nil)
			So(errr, ShouldNotBeNil)
			So(f, ShouldBeNil)
		})
	})
}

func TestCleanupKeepsMountCaches(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// cmd/add.go promises that cleanup ignores "any mount cache directories, so
	// that nothing on your remote file systems gets deleted". Cleanup runs BEFORE
	// Job.Unmount (client.go), and a cached writable mount only uploads at
	// Unmount, so deleting one of these destroys the job's own output.
	Convey("Given a Job whose mount caches land in the dirs cleanup sweeps", t, func() {
		cwd := t.TempDir()
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		Convey("An explicit relative MountTarget.CacheDir in the workspace survives", func() {
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:   testWSMount,
				Targets: []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true, CacheDir: "mycache"}},
			}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			cached := writeFileIn(filepath.Join(workSpace, "mycache"), "unuploaded.txt")
			output := writeFileIn(actualCwd, "out.txt")

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(cached, cwd)
			soPathsGone(output, tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("An explicit absolute MountTarget.CacheDir in the workspace survives", func() {
			// the CacheDir is filled in after the workspace exists, since it has
			// to name a path inside it. Only Mount, Target.Profile and Target.Path
			// reach MountConfigs.Key(), so the workspace wr built stays the one
			// this Job's key describes.
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:   testWSMount,
				Targets: []MountTarget{{Path: testWSTargetPath, Cache: true}},
			}}}
			_, workSpace, _ := realWorkSpace(job)

			abs := filepath.Join(workSpace, "abscache")
			job.MountConfigs[0].Targets[0].CacheDir = abs

			cached := writeFileIn(abs, "unuploaded.txt")

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(cached, cwd)
		})

		Convey("A relative MountConfig.CacheBase inside the working dir survives", func() {
			// this one lands inside ActualCwd, which cleanup otherwise deletes
			// whole, and muxfys puts its own cache dir inside it.
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:     testWSMount,
				CacheBase: "cachebase",
				Targets:   []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}},
			}}}
			actualCwd, _, tmpDir := realWorkSpace(job)

			cached := writeFileIn(filepath.Join(actualCwd, "cachebase", muxfysCachePrefix+"_cache123"),
				"unuploaded.txt")
			output := writeFileIn(actualCwd, "out.txt")

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(cached, cwd)
			soPathsGone(output, tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("The default cache dir muxfys names for itself in the workspace survives", func() {
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:   testWSMount,
				Targets: []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}},
			}}}
			_, workSpace, tmpDir := realWorkSpace(job)

			cached := writeFileIn(filepath.Join(workSpace, muxfysCachePrefix+"_cache456"), "unuploaded.txt")

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(cached, cwd)
			soPathsGone(tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("A MountConfig.CacheBase naming the working dir itself keeps it whole", func() {
			// there is then no way to delete the job's output without deleting
			// the cache that has not been uploaded yet, so wr deletes neither.
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:     testWSMount,
				CacheBase: ".",
				Targets:   []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}},
			}}}
			actualCwd, _, tmpDir := realWorkSpace(job)

			cached := writeFileIn(filepath.Join(actualCwd, muxfysCachePrefix+"_cache789"), "unuploaded.txt")
			output := writeFileIn(actualCwd, "out.txt")

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(cached, output, cwd)
			soPathsGone(tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("Nothing is kept for a mount that configures no cache of its own", func() {
			// the keep-set is derived from the Job's own configuration, not from
			// what the workspace happens to contain: keeping too much would leak
			// a directory of every job that ever named one.
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:   testWSMount,
				Targets: []MountTarget{{Path: testWSTargetPath}},
			}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			notACache := writeFileIn(filepath.Join(workSpace, "mycache"), "junk.txt")
			output := writeFileIn(actualCwd, "out.txt")

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(cwd)
			soPathsGone(notACache, filepath.Join(workSpace, "mycache"), output, tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("A Job that mounts nothing keeps nothing named like a muxfys cache", func() {
			// the name rule is the one thing cleanup keeps by NAME rather than by
			// path, because muxfys chooses the name of the cache dir it makes
			// inside the CacheBase it was given. A Job with no mounts has no
			// muxfys and so no such dir to protect: applying the rule to one
			// anyway lets its own Cmd keep the whole workspace alive by creating
			// ../.muxfyssquat.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			squat := writeFileIn(filepath.Join(workSpace, muxfysCachePrefix+"squat"), "junk.txt")
			output := writeFileIn(actualCwd, "out.txt")

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsGone(squat, workSpace, filepath.Join(cwd, AppName+createdCwdBaseSuffix),
				output, actualCwd, tmpDir)
			soPathsExist(cwd)
		})

		Convey("Nor does a mounting Job whose cache base is not the workspace", func() {
			// muxfys names its own dir inside the CacheBase it was given, so a
			// CacheBase elsewhere puts nothing of muxfys's naming in the
			// workspace, and keeping one anyway is the same leak.
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:     testWSMount,
				CacheBase: "cachebase",
				Targets:   []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}},
			}}}
			_, workSpace, tmpDir := realWorkSpace(job)

			squat := writeFileIn(filepath.Join(workSpace, muxfysCachePrefix+"squat"), "junk.txt")

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsGone(squat, tmpDir)
			soPathsExist(cwd)
		})

		Convey("A dir merely named like the configured cache is still deleted", func() {
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{
				Mount:   testWSMount,
				Targets: []MountTarget{{Path: testWSTargetPath, Cache: true, CacheDir: "mycache"}},
			}}}
			_, workSpace, _ := realWorkSpace(job)

			cached := writeFileIn(filepath.Join(workSpace, "mycache"), "unuploaded.txt")
			lookalike := writeFileIn(filepath.Join(workSpace, "mycache2"), "junk.txt")

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(cached)
			soPathsGone(lookalike, filepath.Join(workSpace, "mycache2"))
		})
	})
}

func TestCleanupKeepsCacheAtWorkSpace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// a MountTarget.CacheDir is resolved against the workspace (job.go's
	// resolveCacheDir), so "." - or any spelling that reaches it, lexically or
	// through a symlink - puts muxfys's cache on the workspace root itself,
	// beside the working directory and tmp. A writable cached mount only uploads
	// at Unmount, so a cleanup that sweeps such a cache destroys the job's own
	// output before it ever reaches S3: Client.Execute triggers the behaviours
	// (client.go) before it unmounts, and the manager's lost-job cleanup sweeps
	// while the runner may still be alive with the mount live.
	Convey("Given a Job with a cleanup Behaviour whose mount cache lands on the workspace", t, func() {
		cwd := t.TempDir()

		cachingJob := func(cacheDir string) *Job {
			return &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{
				Mount: testWSMount,
				Targets: []MountTarget{{
					Path: testWSTargetPath, Cache: true, Write: true, CacheDir: cacheDir,
				}},
			}}, Behaviours: Behaviours{{When: OnSuccess, Do: Cleanup}}}
		}

		// muxfys caches an S3 target's data at <CacheDir>/<host>/<bucket>/<path>
		// (its s3.go LocalPath), so the entries of a cache sitting on the
		// workspace root are named after the remote and the endpoint the
		// profile resolves to, not after anything wr can predict.
		soCacheSurvivesCleanup := func(job *Job) {
			_, workSpace, _ := realWorkSpace(job)

			cached := writeFileIn(filepath.Join(workSpace, "s3.example.com", "mybucket"),
				"unuploaded.txt")

			err := job.TriggerBehaviours(true)

			soPathsExist(cached, cwd)

			So(err, ShouldBeNil)
		}

		Convey("A CacheDir of \".\" survives, since it is the workspace itself", func() {
			soCacheSurvivesCleanup(cachingJob("."))
		})

		Convey("So does one spelled \"./\"", func() {
			soCacheSurvivesCleanup(cachingJob("./"))
		})

		Convey("So does one spelled \"sub/..\"", func() {
			soCacheSurvivesCleanup(cachingJob("sub/.."))
		})

		Convey("So does one that reaches the workspace through a symlink", func() {
			// the classification has to ask the FILESYSTEM as well as the
			// strings: "link" is lexically an ENTRY of the workspace, so a
			// purely lexical answer keeps the symlink and sweeps the directory
			// the cache is physically in - which is the workspace itself.
			job := cachingJob("link")
			_, workSpace, _ := realWorkSpace(job)

			So(os.Symlink(workSpace, filepath.Join(workSpace, "link")), ShouldBeNil)

			cached := writeFileIn(filepath.Join(workSpace, "link", "s3.example.com", "mybucket"),
				"unuploaded.txt")
			physical := filepath.Join(workSpace, "s3.example.com", "mybucket", "unuploaded.txt")

			err := job.TriggerBehaviours(true)

			soPathsExist(physical, cached, cwd)

			So(err, ShouldBeNil)
		})

		Convey("A CacheDir below the workspace is kept while the rest is still swept", func() {
			// the keep set may only widen, but not to the point of keeping
			// everything: an ordinary cache dir is named, so cleanup knows
			// exactly which entry to leave and deletes the others.
			job := cachingJob("mycache")
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			cached := writeFileIn(filepath.Join(workSpace, "mycache"), "unuploaded.txt")
			output := writeFileIn(actualCwd, "out.txt")

			err := job.TriggerBehaviours(true)

			soPathsExist(cached, cwd)
			soPathsGone(output, tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("A Job that mounts nothing still has its whole workspace swept", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd,
				Behaviours: Behaviours{{When: OnSuccess, Do: Cleanup}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			output := writeFileIn(actualCwd, "out.txt")

			err := job.TriggerBehaviours(true)

			soPathsGone(output, actualCwd, tmpDir, workSpace)
			soPathsExist(cwd)

			So(err, ShouldBeNil)
		})
	})
}

// mountLoopback really FUSE mounts backing over mountPoint, with the given mount
// options, and takes the mount down when the test ends. ok is false where the
// host refused the mount, for the reason mountLoopbackOver gives.
//
// go-fuse's loopback filesystem is what makes the mount real, and it is the
// cheapest one an unprivileged process can raise. What is behind it is an
// ordinary directory, so what survived can be asserted about directly; muxfys
// would need an object store to talk to, and the deletion under test does not
// care which filesystem is mounted, only that a mount gets crossed.
//
// The one option any of this needs is "nonempty", for a mount point that is not
// empty: fusermount 2 refuses that without it, while libfuse 3 dropped the check
// and permits it by default. It decides only whether the mount is PERMITTED,
// never what the sweep does once it is up.
func mountLoopback(t *testing.T, mountPoint, backing string, options ...string) bool {
	t.Helper()

	root, err := gofuse.NewLoopbackRoot(backing)
	So(err, ShouldBeNil)
	So(os.MkdirAll(mountPoint, os.ModePerm), ShouldBeNil)

	server, err := gofuse.Mount(mountPoint, root, &gofuse.Options{
		MountOptions: fuse.MountOptions{Options: options},
	})
	if err != nil {
		t.Logf("this host refused a loopback FUSE mount at %s: %s", mountPoint, err)

		return false
	}

	t.Cleanup(func() {
		if uerr := server.Unmount(); uerr != nil {
			t.Logf("could not unmount the loopback at %s: %s", mountPoint, uerr)
		}
	})

	return true
}

// jobKeptDirs is the keep set the real resolution classifies for job: everything
// cleanup must leave alone inside the workspace wr made for it. It is asked of
// the production resolution, so a test can pin the classification itself rather
// than only what a sweep happened to leave behind.
func jobKeptDirs(job *Job) keptDirs {
	ws, err := job.workSpaceSnapshot().resolveWorkSpace()
	So(err, ShouldBeNil)
	So(ws, ShouldNotBeNil)

	defer ws.Close()

	return ws.keep
}

func TestCleanupKeepsSymlinkSpelledMounts(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// MountConfig.Mount is an absolute path of the user's, while the workspace it
	// is compared against is spelled the way the Job's own Cwd is, so one
	// directory can reach the classification under two names. Cleanup runs while
	// the mount is still live (Job.Unmount comes after it in client.go), so a
	// spelling the keep set does not recognise is a live mount read through and
	// deleted, taking the user's remote objects with it.
	Convey("Given a Job whose mount point is inside its workspace, spelled through a symlink", t, func() {
		cwd := t.TempDir()
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		// the Mount string is settled before the Job's key, and so before the
		// name of the workspace: only the symlink's TARGET names the workspace.
		link := filepath.Join(cwd, "mount_link")

		Convey("a mount inside the working directory is classified as kept, and kept", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: link}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			mount := filepath.Join(actualCwd, testWSMount)
			remote := writeFileIn(mount, "remote.txt")
			output := writeFileIn(actualCwd, "out.txt")

			So(os.Symlink(mount, link), ShouldBeNil)

			keep := jobKeptDirs(job)
			So(keep.inActualCwd, ShouldResemble, []string{testWSMount})
			So(keep.workSpaceEntries[createdCwdName], ShouldBeTrue)

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(remote, mount, actualCwd, workSpace, cwd)
			soPathsGone(output, tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("a mount beside the working directory is classified as kept, and kept", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: link}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			mount := filepath.Join(workSpace, "shared")
			remote := writeFileIn(mount, "remote.txt")
			So(os.Symlink(mount, link), ShouldBeNil)

			keep := jobKeptDirs(job)
			So(keep.workSpaceEntries["shared"], ShouldBeTrue)

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(remote, mount, workSpace, cwd)
			soPathsGone(actualCwd, tmpDir)

			So(err, ShouldBeNil)
		})

		Convey("a cache base that resolves to the workspace is recognised, so muxfys' own dir survives", func() {
			// a writable mount's cache is not uploaded until Job.Unmount, which
			// comes after cleanup, and muxfys names the dir it makes inside the
			// CacheBase it was given - so the spelling of the CacheBase decides
			// whether the job's own un-uploaded output survives its cleanup.
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{
				Mount:     testWSMount,
				CacheBase: link,
				Targets:   []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}},
			}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			So(os.Symlink(workSpace, link), ShouldBeNil)

			cached := writeFileIn(filepath.Join(workSpace, muxfysCachePrefix+"_cache456"), "unuploaded.txt")
			output := writeFileIn(actualCwd, "out.txt")

			So(jobKeptDirs(job).muxfysNamesWorkSpaceEntry, ShouldBeTrue)

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(cached, workSpace, cwd)
			soPathsGone(output, tmpDir)

			So(err, ShouldBeNil)
		})
	})

	// the symlink can also be INSIDE the workspace, and then both spellings name
	// something inside it: the classification agrees lexically and never asks the
	// filesystem, so the name it records is the symlink's rather than that of the
	// directory the mount is physically in - and the sweep deletes that directory,
	// through the live mount, while leaving the link it kept dangling.
	Convey("Given a Job whose mount point is spelled through a symlink inside its workspace", t, func() {
		cwd := t.TempDir()
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		Convey("the dir a mount beside the working directory is really in is kept", func() {
			// a relative Mount is resolved against the working directory, so this
			// names <workSpace>/link/x without needing the workspace's name.
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: "../link/x"}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			realDir := filepath.Join(workSpace, "real")
			mount := filepath.Join(realDir, "x")
			remote := writeFileIn(mount, "remote.txt")

			So(os.Symlink(realDir, filepath.Join(workSpace, "link")), ShouldBeNil)

			keep := jobKeptDirs(job)
			err := cleanup.Trigger(OnExit, job)

			soPathsExist(remote, mount, realDir, workSpace, cwd)
			soPathsGone(actualCwd, tmpDir)

			So(keep.workSpaceEntries["real"], ShouldBeTrue)
			So(err, ShouldBeNil)
		})

		Convey("the dir a mount inside the working directory is really in is kept", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: "link/x"}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			realDir := filepath.Join(actualCwd, "real")
			mount := filepath.Join(realDir, "x")
			remote := writeFileIn(mount, "remote.txt")
			output := writeFileIn(actualCwd, "out.txt")
			So(os.Symlink(realDir, filepath.Join(actualCwd, "link")), ShouldBeNil)

			keep := jobKeptDirs(job)
			err := cleanup.Trigger(OnExit, job)

			soPathsExist(remote, mount, realDir, actualCwd, workSpace, cwd)
			soPathsGone(output, tmpDir)

			So(keep.inActualCwd, ShouldContain, filepath.Join("real", "x"))
			So(err, ShouldBeNil)
		})

		Convey("a cache base that resolves to the workspace is recognised, so muxfys' own dir survives", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{
				Mount:     testWSMount,
				CacheBase: "../wslink",
				Targets:   []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}},
			}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			So(os.Symlink(workSpace, filepath.Join(workSpace, "wslink")), ShouldBeNil)

			cached := writeFileIn(filepath.Join(workSpace, muxfysCachePrefix+"_cache456"), "unuploaded.txt")
			output := writeFileIn(actualCwd, "out.txt")

			keep := jobKeptDirs(job)
			err := cleanup.Trigger(OnExit, job)

			soPathsExist(cached, workSpace, cwd)
			soPathsGone(output, tmpDir)

			So(keep.muxfysNamesWorkSpaceEntry, ShouldBeTrue)
			So(err, ShouldBeNil)
		})

		Convey("and so is the same dir when the symlink to it is outside the workspace", func() {
			// the spelling the earlier fix established, pinned against the same
			// fixture: here the two spellings disagree lexically, which is what
			// makes the resolved comparison happen at all.
			link := filepath.Join(cwd, "mount_link")
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: link}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			realDir := filepath.Join(workSpace, "real")
			mount := filepath.Join(realDir, "x")
			remote := writeFileIn(mount, "remote.txt")
			So(os.Symlink(mount, link), ShouldBeNil)

			keep := jobKeptDirs(job)
			err := cleanup.Trigger(OnExit, job)

			soPathsExist(remote, mount, realDir, workSpace, cwd)
			soPathsGone(actualCwd, tmpDir)

			So(keep.workSpaceEntries["real"], ShouldBeTrue)
			So(err, ShouldBeNil)
		})
	})

	// the same two spellings the other way round. Job.Cwd is stored exactly as
	// the user typed it, because it feeds Job.Key(), so the symlinked spelling is
	// the one wr is GIVEN rather than one it chose - and then it is the workspace
	// side of the comparison that has to be resolved for the mount to be
	// recognised at all.
	Convey("Given a Job whose Cwd is a symlinked spelling and whose mount names the real path", t, func() {
		base := t.TempDir()
		realCwd := filepath.Join(base, "real")
		So(os.MkdirAll(realCwd, os.ModePerm), ShouldBeNil)

		cwd := filepath.Join(base, "cwd_link")
		So(os.Symlink(realCwd, cwd), ShouldBeNil)

		link := filepath.Join(base, "mount_link")
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		Convey("the mount inside its working directory is classified as kept, and kept", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: link}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			realActualCwd, err := filepath.EvalSymlinks(actualCwd)
			So(err, ShouldBeNil)
			So(realActualCwd, ShouldNotEqual, actualCwd)

			mount := filepath.Join(realActualCwd, testWSMount)
			remote := writeFileIn(mount, "remote.txt")
			output := writeFileIn(actualCwd, "out.txt")

			So(os.Symlink(mount, link), ShouldBeNil)

			keep := jobKeptDirs(job)
			So(keep.inActualCwd, ShouldResemble, []string{testWSMount})
			So(keep.workSpaceEntries[createdCwdName], ShouldBeTrue)

			err = cleanup.Trigger(OnExit, job)

			soPathsExist(remote, mount, actualCwd, workSpace, cwd)
			soPathsGone(output, tmpDir)

			So(err, ShouldBeNil)
		})
	})
}

func TestCleanupUnlinksSymlinkSpelledKeeps(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// relsBelowDirResolved records a mount point in EVERY spelling that puts it
	// inside the working directory, so a mount named through a symlink there
	// reaches the keep set twice: once as the LINK, and once as the directory the
	// link leads to. Only the resolved one is the mount, and it is kept under its
	// OWN name, which makes the link a redundant SECOND name for something already
	// protected: keeping it protects nothing - unlinking a symlink cannot touch
	// what it points at - and leaves a name in a workspace the Job was supposed to
	// have reclaimed.
	Convey("Given a Job whose mount point is a symlink to a dir inside its working directory", t, func() {
		cwd := t.TempDir()
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		// a relative Mount is resolved against the working directory, so this
		// names <actualCwd>/link without needing the workspace's name, and the
		// kernel puts the mount itself at <actualCwd>/real.
		job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: "link"}}}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)

		realDir := filepath.Join(actualCwd, "real")
		remote := writeFileIn(realDir, testWSRemote)
		output := writeFileIn(actualCwd, "out.txt")

		link := filepath.Join(actualCwd, "link")
		So(os.Symlink("real", link), ShouldBeNil)

		Convey("cleanup keeps the dir the mount is really in, and unlinks the symlink", func() {
			keep := jobKeptDirs(job)

			err := cleanup.Trigger(OnExit, job)

			soPathsExist(remote, realDir, actualCwd, workSpace, cwd)

			// the link is lstat'ed rather than handed to soPathsGone, because a
			// surviving link whose target had ALSO been deleted would satisfy the
			// os.Stat that helper makes.
			_, linkErr := os.Lstat(link)
			So(os.IsNotExist(linkErr), ShouldBeTrue)

			soPathsGone(output, tmpDir)

			// both spellings are in the keep set, which is why the sweep meets the
			// link at all.
			So(keep.inActualCwd, ShouldContain, "link")
			So(keep.inActualCwd, ShouldContain, "real")
			So(err, ShouldBeNil)
		})
	})
}

func TestCleanupIsIdempotent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// cleanup runs more than once for the same Job: the runner triggers it, and
	// for a lost job the server triggers it again. It must therefore be able to
	// finish the job the first run left, rather than refuse and leak the
	// workspace, which is what requiring the working directory to exist would do.
	Convey("Given a workspace wr really created for a Job", t, func() {
		cwd := t.TempDir()
		precious := writeFileIn(cwd, "05_RunCisEQTL.R")
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		Convey("Cleanup finishes the sweep when the Job's Cmd deleted its own working dir", func() {
			job := &Job{Cwd: cwd, Cmd: "rm -fr $PWD"}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			So(os.RemoveAll(actualCwd), ShouldBeNil)

			err := cleanup.Trigger(OnExit, job)

			soPathsGone(tmpDir, workSpace, filepath.Join(cwd, AppName+"_cwd"))
			soPathsExist(precious, cwd)

			So(err, ShouldBeNil)
		})

		Convey("Cleanup run twice around an unmount clears the whole workspace", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: "../shared"}}}
			_, workSpace, tmpDir := realWorkSpace(job)

			shared := filepath.Join(workSpace, "shared")
			remote := writeFileIn(shared, "remote.txt")

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			// the mount is still live during the first cleanup, so its content
			// survives; the workspace cannot go until Unmount has taken it away.
			soPathsExist(remote, shared, workSpace)
			soPathsGone(tmpDir)

			So(os.RemoveAll(shared), ShouldBeNil)

			err := cleanup.Trigger(OnExit, job)

			soPathsGone(workSpace, filepath.Join(cwd, AppName+"_cwd"))
			soPathsExist(precious, cwd)

			So(err, ShouldBeNil)
		})

		Convey("Cleanup does nothing, silently, when the Job's Cwd has itself gone", func() {
			job := &Job{Cwd: filepath.Join(cwd, "gone"), Cmd: testWSCmd}
			So(os.MkdirAll(job.Cwd, os.ModePerm), ShouldBeNil)

			realWorkSpace(job)

			So(os.RemoveAll(job.Cwd), ShouldBeNil)

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(precious, cwd)
		})
	})
}

func TestCleanupWorkingDirSwappedAfterProof(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Cleanup refuses a working dir swapped for a symlink after it was proven", t, func() {
		// a proof is about a path string, and every syscall re-resolves it, so the
		// working directory proven to be real can be a symlink by the time the
		// sweep reads it - and a read follows a symlinked final component, deleting
		// the target's contents instead. The hook puts the test in that moment.
		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: testWSMount}}}
		actualCwd, _, _ := realWorkSpace(job)
		So(os.MkdirAll(filepath.Join(actualCwd, testWSMount), os.ModePerm), ShouldBeNil)

		userDir := filepath.Join(cwd, "userdata")
		notes := writeFileIn(userDir, "notes.txt")

		swapWorkingDir := func(j *Job) error {
			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				So(os.RemoveAll(j.ActualCwd), ShouldBeNil)
				So(os.Symlink(userDir, j.ActualCwd), ShouldBeNil)
			}

			return (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, j)
		}

		Reset(func() { cleanupProvenHook = nil })

		Convey("when a live mount inside it would otherwise be kept", func() {
			err := swapWorkingDir(job)

			soPathsExist(notes, userDir, actualCwd, cwd)

			So(err, ShouldNotBeNil)
		})

		Convey("and when the Job has no mounts, so nothing else looks at it", func() {
			// with nothing to keep, the sweep would simply unlink the link the
			// Cmd left behind and report success, which is wr quietly tidying up
			// after something it cannot account for.
			plain := &Job{Cwd: cwd, Cmd: testWSCmd + " plain"}
			plainCwd, _, _ := realWorkSpace(plain)

			err := swapWorkingDir(plain)

			soPathsExist(notes, userDir, plainCwd, cwd)

			So(err, ShouldNotBeNil)
		})

		Convey("and when the proof tolerated the working dir already being absent", func() {
			// absence is the one case with no identity to check the name against:
			// there was nothing there to lstat, and the licence to delete is the
			// path being provably this Job's own. So what KIND of thing has
			// appeared at that name by the time the sweep looks is the only
			// question left, and asking it is what stops wr unlinking a symlink of
			// the user's and reporting success.
			absent := &Job{Cwd: cwd, Cmd: testWSCmd + " absent"}
			absentCwd, _, _ := realWorkSpace(absent)
			So(os.RemoveAll(absentCwd), ShouldBeNil)

			err := swapWorkingDir(absent)

			soPathsExist(notes, userDir, absentCwd, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})
	})
}

func TestCleanupWorkingDirSwappedForADirAfterProof(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Cleanup refuses a working dir swapped for another dir after it was proven", t, func() {
		// what kind of thing sits at a name is not which thing it is: a DIRECTORY
		// renamed onto the working directory's name after the proof answers every
		// question about kind identically, so only comparing it against the lstat
		// the proof took tells the two apart. That is the comparison `run` makes
		// through openVerifiedDirFile, and cleanup has to make the same one or the
		// two consumers disagree about which directory is the Job's.
		cwd := t.TempDir()

		userTree := filepath.Join(cwd, "scripts")
		writeFileIn(userTree, "analyse.sh")
		writeFileIn(filepath.Join(userTree, "lib"), "helpers.sh")

		swapWorkingDirForDir := func(j *Job) error {
			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				So(os.Rename(j.ActualCwd, j.ActualCwd+".moved"), ShouldBeNil)
				So(os.Rename(userTree, j.ActualCwd), ShouldBeNil)
			}

			return (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, j)
		}

		soUserTreeSurvives := func(at string) {
			soPathsExist(filepath.Join(at, "analyse.sh"), filepath.Join(at, "lib", "helpers.sh"), cwd)
		}

		Reset(func() { cleanupProvenHook = nil })

		Convey("when the Job has no mounts, so the swept dir is removed whole", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			actualCwd, _, _ := realWorkSpace(job)

			err := swapWorkingDirForDir(job)

			// survival first, so a lost race shows up as the deletion it is,
			// not as a missing error value.
			soUserTreeSurvives(actualCwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("and when a live mount inside it would otherwise keep some of it", func() {
			// the keep set takes the other branch of removeActualCwd, which opens
			// the working directory and deletes all but the kept dirs through the
			// handle. Verifying that open against a fresh look at the name would
			// prove only that the name did not change again.
			job := &Job{Cwd: cwd, Cmd: testWSCmd + " mounted", MountConfigs: MountConfigs{{Mount: testWSMount}}}
			actualCwd, _, _ := realWorkSpace(job)
			So(os.MkdirAll(filepath.Join(actualCwd, testWSMount), os.ModePerm), ShouldBeNil)

			err := swapWorkingDirForDir(job)

			soUserTreeSurvives(actualCwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})
	})
}

func TestCleanupActualCwdRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Cleanup reads a Job's ActualCwd under the Job's lock", t, func() {
		// applyLiveSnapshot writes ActualCwd under the Job's lock every touch
		// interval, and the manager runs cleanup for a lost job at the same time,
		// so an unlocked read here is a data race whose outcome decides which
		// directory gets deleted. -race is what makes this test bite.
		const (
			concurrentRounds           = 50
			concurrentWriterAndCleaner = 2
		)

		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd}
		actualCwd, _, _ := realWorkSpace(job)

		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		var wg sync.WaitGroup

		wg.Add(concurrentWriterAndCleaner)

		go func() {
			defer wg.Done()

			for range concurrentRounds {
				applyLiveSnapshot(job, &JobEndState{Cwd: actualCwd})
			}
		}()

		go func() {
			defer wg.Done()

			for range concurrentRounds {
				//nolint:errcheck // a cleanup racing another may fail; -race is the assertion
				cleanup.Trigger(OnExit, job)
			}
		}()

		wg.Wait()

		soPathsExist(cwd)
	})
}

func TestCleanupEmptyParentWalk(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// the upward walk that tidies empty parent directories is bounded by the
	// containment proof, and containment alone is not enough: an absent workspace
	// must still be proven to be one wr created, or the walk unlinks the user's
	// own empty directories up to Cwd.
	Convey("Given an empty output tree of the user's own, at the depth wr creates at", t, func() {
		// the tree is named every way a workspace of wr's is named - a *_cwd
		// base, a leaf called cwd, at the created depth - so that what refuses
		// it is the identity check rather than any of the shape rules.
		cwd := t.TempDir()
		deep := filepath.Join(cwd, "results"+createdCwdBaseSuffix, "2024", "runA", "sampleB")
		So(os.MkdirAll(deep, os.ModePerm), ShouldBeNil)

		cleanupAll := &Behaviour{When: OnExit, Do: CleanupAll}

		Convey("cleanup deletes none of it for an ActualCwd wr never created", func() {
			job := &Job{Cwd: cwd, Cmd: "echo eat", ActualCwd: filepath.Join(deep, "absent", createdCwdName)}

			err := cleanupAll.Trigger(OnExit, job)

			soPathsExist(deep, filepath.Dir(deep), cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("cleanup still tidies the empty parents of a workspace wr did create", func() {
			// the absence of a workspace wr can prove it built is the ordinary
			// state of a second cleanup, so tolerating that must survive the fix.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			_, workSpace, _ := realWorkSpace(job)

			So(os.RemoveAll(workSpace), ShouldBeNil)

			err := cleanupAll.Trigger(OnExit, job)

			soPathsGone(filepath.Join(cwd, AppName+"_cwd"))
			soPathsExist(deep, cwd)

			So(err, ShouldBeNil)
		})
	})
}

func TestCleanupWorkSpaceReappearsAfterProof(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Cleanup tidies the empty parents of a workspace that came back after the proof", t, func() {
		// the proof finding every parent but no workspace is the ordinary state of
		// the second cleanup of a lost job, and what is left to do then is tidy
		// the empty parent dirs wr created. There is no lstat from the proof to
		// prove that a workspace which has since reappeared is the one wr made, so
		// it is not opened at all - and it not being opened must not cost the
		// parents their tidy-up.
		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd}
		_, workSpace, _ := realWorkSpace(job)

		So(os.RemoveAll(workSpace), ShouldBeNil)

		cleanupProvenHook = func() {
			cleanupProvenHook = nil

			So(os.MkdirAll(workSpace, os.ModePerm), ShouldBeNil)
		}

		Reset(func() { cleanupProvenHook = nil })

		err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)

		// the tidy-up first, so a refusal to go on shows up as the untidied tree
		// it is rather than only as an error value.
		soPathsGone(filepath.Join(cwd, AppName+createdCwdBaseSuffix))
		soPathsExist(cwd)

		So(err, ShouldBeNil)
	})
}

// jobWithKeyPrefix returns a Job whose Key() starts with prefix, found the way
// an attacker would: Key() hashes the Cmd (and the mounts and image), all of
// which come from whoever submitted the job, so a few leading characters of it
// can simply be ground out.
func jobWithKeyPrefix(cwd, prefix string) *Job {
	var job *Job

	for i := range keyGrindMax {
		candidate := &Job{Cwd: cwd, Cmd: fmt.Sprintf("echo %d", i)}
		if strings.HasPrefix(candidate.Key(), prefix) {
			job = candidate

			break
		}
	}

	So(job, ShouldNotBeNil)

	return job
}

func TestWorkSpaceBaseComponent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// every directory wr has ever created for a Job sits below a base component
	// named <AppName>_cwd (mkHashedDir), and nothing else about the path a
	// runner reports is checked against something wr chose. Without that
	// component pinned, a path of the right depth through directories of the
	// user's own passes for a workspace.
	Convey("Given an empty output tree of the user's whose names match a job key's hashed dirs", t, func() {
		// the hashed dirs are the first three characters of the key, so an
		// attacker greps the user's Cwd for a three-deep chain and grinds Cmd
		// until the key matches it - about 4096 tries, or one in 4096 by chance.
		cwd := t.TempDir()
		hashed := []string{"1", "2", "3"}
		job := jobWithKeyPrefix(cwd, strings.Join(hashed, ""))
		results := filepath.Join(cwd, "results")
		tree := filepath.Join(append([]string{results}, hashed...)...)
		So(os.MkdirAll(tree, os.ModePerm), ShouldBeNil)

		// the unique dir below those is the only component carrying the key's
		// real entropy, and it is exactly the one whose ABSENCE the origin proof
		// licenses, so the attacker never has to produce it.
		job.ActualCwd = filepath.Join(tree, job.Key()[mkHashedLevels-1:]+"0", createdCwdName)

		Convey("cleanup unlinks none of it", func() {
			err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job)

			soPathsExist(tree, results, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})
	})

	Convey("Given a directory of the user's own at the depth and under the name wr creates at", t, func() {
		cwd := t.TempDir()
		sample := filepath.Join(cwd, "results", "2024", "runA", "sampleB")
		scripts := filepath.Join(sample, "scripts")
		fabricated := filepath.Join(scripts, createdCwdName)
		So(os.MkdirAll(fabricated, os.ModePerm), ShouldBeNil)

		analyse := writeFileIn(scripts, "analyse.sh")
		notes := writeFileIn(fabricated, "notes.txt")

		job := &Job{Cwd: cwd, Cmd: testWSCmd, ActualCwd: fabricated}

		Convey("cleanup sweeps none of it", func() {
			err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job)

			soPathsExist(notes, analyse, fabricated, scripts, sample, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("a run behaviour executes nothing in it", func() {
			err := runBehaviour().Trigger(OnExit, job)

			soPathsGone(filepath.Join(fabricated, testRunMarker))
			soPathsExist(notes, analyse, fabricated, scripts, sample, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})
	})
}

func TestWorkSpaceOfAnotherJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// every Job of a Cwd works below the SAME <AppName>_cwd base component, so
	// the base check says nothing about which of them a reported path belongs
	// to: a sibling's workspace has exactly the shape wr's own does, because it
	// is one. Only the path mkHashedDir builds from a Job's own key tells the
	// two apart.
	Convey("Given two Jobs sharing a Cwd, each with the workspace wr really made", t, func() {
		cwd := t.TempDir()

		victim := &Job{Cwd: cwd, Cmd: "echo victim"}
		victimCwd, victimWorkSpace, victimTmp := realWorkSpace(victim)
		results := writeFileIn(victimCwd, "results.txt")

		attacker := &Job{Cwd: cwd, Cmd: "echo attacker"}
		realWorkSpace(attacker)

		// the runner reports ActualCwd over the wire, so the attacker's Job can
		// carry the victim's working directory while the victim is still
		// running in it.
		applyLiveSnapshot(attacker, &JobEndState{Cwd: victimCwd})

		Convey("cleanup of one deletes nothing of the other's", func() {
			err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, attacker)

			soPathsExist(results, victimCwd, victimTmp, victimWorkSpace)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("a run behaviour of one executes nothing in the other's", func() {
			err := runBehaviour().Trigger(OnExit, attacker)

			soPathsGone(filepath.Join(victimCwd, testRunMarker))
			soPathsExist(results, victimCwd, victimTmp, victimWorkSpace)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})
	})
}

// nestedSweepCases are the two sweeps a Job's working directory can get,
// chosen by whether the Job has mounts: with none, the whole directory is handed
// to one deletion, and with some, its contents are swept entry by entry around
// the Job's keep set. Both have to keep a nested Job's workspace.
func nestedSweepCases() []MountConfigs {
	return []MountConfigs{nil, {{Mount: testWSMount}}}
}

func TestCleanupOfNestedJobWorkSpace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// a Job that adds jobs - the documented `wr add --bsubs` pattern for
	// nextflow and cellranger - hands its children a Cwd of os.Getwd(), which IS
	// the working directory wr gave IT (Execute sets the Cmd's Dir to it), so wr
	// builds their workspaces INSIDE the tree the parent's own default
	// `--on_exit [{"cleanup":true}]` is licensed to wipe. The children are still
	// running there when the parent exits.
	for _, mounts := range nestedSweepCases() {
		Convey("Given a Job whose working directory holds the workspace wr made for a nested Job", t, func() {
			cwd := t.TempDir()

			parent := &Job{Cwd: cwd, Cmd: "wr add --bsubs nextflow", MountConfigs: mounts}
			parentCwd, parentWorkSpace, parentTmp := realWorkSpace(parent)
			parentOutput := writeFileIn(parentCwd, "parent_output.txt")

			child := &Job{Cwd: parentCwd, Cmd: "still running child"}
			childCwd, childWorkSpace, childTmp := realWorkSpace(child)
			childOutput := writeFileIn(childCwd, "child_output.txt")
			childScratch := writeFileIn(childTmp, "child_scratch.txt")

			Convey(fmt.Sprintf("cleanup with %d mounts keeps all of it, and still deletes its own",
				len(mounts)), func() {
				err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, parent)

				soPathsExist(childOutput, childScratch, childCwd, childTmp, childWorkSpace)
				soPathsGone(parentOutput, parentTmp)

				// the parent's own working directory and the workspace holding
				// it are what the child's tree is inside, so they survive with
				// it; see nestedWorkSpaceBase on why that leak is the right way
				// round.
				soPathsExist(parentCwd, parentWorkSpace)

				So(err, ShouldBeNil)
			})
		})
	}
}

func TestCleanupCrossesNoMountBoundary(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// the keep set is built from the Job's own MountConfigs, so it knows nothing
	// of a mount raised by the Job's own Cmd (sshfs, s3fs) or by a nested wr,
	// and a sweep that recurses through one unlinks the user's remote objects.
	//
	// Both sweeps of the working directory are driven, because they get the
	// device the boundary is judged against from different places: the
	// mount-free sweep lstats each directory as it descends into it, while the
	// sweep around a keep set starts from the lstat of the working directory
	// itself, and the mount here is a direct entry of that directory.
	for _, mounts := range nestedSweepCases() {
		Convey("Given a Job whose Cmd raised a live mount wr did not configure", t, func() {
			if !canMountFuse() {
				SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

				return
			}

			cwd := t.TempDir()

			job := &Job{
				Cwd:          cwd,
				Cmd:          "sshfs remote:/data unconfigured && analyse",
				MountConfigs: mounts,
			}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)
			output := writeFileIn(actualCwd, "own_output.txt")
			scratch := writeFileIn(tmpDir, "scratch.txt")

			mountPoint := filepath.Join(actualCwd, "unconfigured")

			remote, mounted := mountLoopbackOver(t, mountPoint)
			if !mounted {
				SkipConvey("this host refused an unprivileged FUSE mount", func() {})

				return
			}

			Convey(fmt.Sprintf("cleanup with %d mounts deletes nothing through it, and still deletes what it made",
				len(mounts)), func() {
				err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)

				soPathsExist(remote, filepath.Join(mountPoint, testWSRemote), mountPoint, actualCwd, workSpace)
				soPathsGone(output, scratch, tmpDir)

				So(err, ShouldBeNil)
			})
		})
	}
}

// canMountFuse is a best-effort check that this host has what a FUSE mount
// needs: an openable /dev/fuse, and a fusermount binary on PATH. A host with
// both can still refuse the mount, so mountLoopbackOver reports that for real.
func canMountFuse() bool {
	dev, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if err != nil {
		return false
	}

	defer dev.Close()

	for _, bin := range []string{"fusermount3", "fusermount"} {
		if _, err = exec.LookPath(bin); err == nil {
			return true
		}
	}

	return false
}

// mountLoopbackOver plants a file of the user's remote data in a directory
// OUTSIDE the Job's Cwd and really FUSE mounts that directory over mountPoint,
// returning the planted file's path. The mount is taken down when the test ends.
//
// ok is false where the host refused the mount, which is the only answer that
// settles whether this environment can run the test: a host can have /dev/fuse
// and a fusermount binary and still deny an unprivileged mount, so a failed
// mount is a skip and never a test failure.
//
// A test that needs to choose the NAMES behind the mount plants its own backing
// directory and calls mountLoopback itself.
func mountLoopbackOver(t *testing.T, mountPoint string) (string, bool) {
	t.Helper()

	backing := t.TempDir()
	remote := writeFileIn(backing, testWSRemote)

	if !mountLoopback(t, mountPoint, backing) {
		return "", false
	}

	return remote, true
}

func TestRunDirWithoutProcFD(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// where the platform will not name a process's own file descriptors, the
	// working directory can only be given to exec.Cmd as a NAME, which the child
	// resolves a second time when the command starts. The warning is what stops a
	// deployment sitting on the losing side of that window silently.
	Convey("Given a host that does not name its own file descriptors", t, func() {
		missing := filepath.Join(t.TempDir(), "no-proc-here", "fd") + string(filepath.Separator)
		procFDPrefix = missing

		Reset(func() { procFDPrefix = procSelfFD })

		buff := clog.ToBufferAtLevel("warn")

		Reset(clog.ToDefault)

		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd}
		actualCwd, _, _ := realWorkSpace(job)

		Convey("a run behaviour still runs in the right directory, and says it could not pin it", func() {
			So(runBehaviour().Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(filepath.Join(actualCwd, testRunMarker))

			logged := buff.String()
			So(logged, ShouldContainSubstring, "does not name a process's own file descriptors")
			So(logged, ShouldContainSubstring, actualCwd)
		})
	})

	// procSelfFDInfo names one file per descriptor too, but they are the
	// descriptors' metadata rather than what they point at. It stands in here for
	// a host that answers for its own descriptors with something that is not the
	// directory we hold - which is the only way to reach the same-dir proof on a
	// box whose /proc is the real one.
	const procSelfFDInfo = "/proc/self/fdinfo/"

	Convey("Given a host that names something else where its descriptors should be", t, func() {
		if runtime.GOOS != osLinux {
			SkipConvey("only linux has /proc/self/fdinfo", func() {})

			return
		}

		procFDPrefix = procSelfFDInfo

		Reset(func() { procFDPrefix = procSelfFD })

		buff := clog.ToBufferAtLevel("warn")

		Reset(clog.ToDefault)

		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd}
		actualCwd, _, _ := realWorkSpace(job)

		Convey("the name is not used, and the fallback is warned about", func() {
			So(runBehaviour().Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(filepath.Join(actualCwd, testRunMarker))
			So(buff.String(), ShouldContainSubstring, "does not name a process's own file descriptors")
		})
	})

	Convey("Given a host that does name them, nothing is warned about", t, func() {
		if runtime.GOOS != osLinux {
			SkipConvey("only linux names descriptors through /proc", func() {})

			return
		}

		buff := clog.ToBufferAtLevel("warn")

		Reset(clog.ToDefault)

		cwd := t.TempDir()

		Convey("for a Job wr made a working directory for", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			realWorkSpace(job)

			So(runBehaviour().Trigger(OnExit, job), ShouldBeNil)
			So(buff.String(), ShouldBeEmpty)
		})

		Convey("nor for a CwdMatters Job, which runs in its own Cwd", func() {
			// there is no handle to pin here and there never was one to lose:
			// Cwd is the directory no proof was made about. Warning about it
			// would cry wolf for every CwdMatters Job, which is the only kind
			// that runs there - see cwdRunDir.
			job := &Job{Cwd: cwd, Cmd: testWSCmd, CwdMatters: true}

			So(runBehaviour().Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(filepath.Join(cwd, testRunMarker))
			So(buff.String(), ShouldBeEmpty)
		})
	})
}

// unorderedMounts returns two MountConfigs that are NOT in Mount order, which is
// the order MountConfigs.Key() reads them in - so asking for the key is what
// used to reorder them. A fresh list each call, since a shared one would be
// reordered by the first asking and prove nothing about the second.
func unorderedMounts() MountConfigs {
	return MountConfigs{
		{Mount: "zeta", Targets: []MountTarget{{Path: testWSTargetPath + "/z"}}},
		{Mount: "alpha", Targets: []MountTarget{{Path: testWSTargetPath + "/a"}}},
	}
}

func TestJobKeyConcurrentWithCleanup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Asking a Job for its key while cleanup asks too is not a write", t, func() {
		// Job.Key() is asked for by readers holding at most a READ lock:
		// workSpaceSnapshot under the Job's RLock, and the REST and CLI handlers,
		// jobtransition, ToEssense and the client under no lock at all. If
		// MountConfigs.Key() sorted the slice it was asked about, two of them at
		// once would write the same backing array, leaving the Job holding a
		// config list with one entry lost and another duplicated - after which a
		// writable mount is never mounted and the Cmd's results are written into a
		// plain directory that cleanup deletes.
		//
		// Each round gets its own Job, because such a write only happens while the
		// slice is still in the order it was configured in.
		const (
			concurrentRounds  = 20
			concurrentReaders = 3
		)

		cwd := t.TempDir()
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}
		wrongKeys := 0
		mutated := 0

		for round := range concurrentRounds {
			cmd := fmt.Sprintf("%s %d", testWSCmd, round)

			// the workspace is built for a Job of its own, so that the Job the
			// readers share has never been asked for its key: the state a Job is in
			// when the manager has just decoded it from the database or taken it
			// off the wire.
			built := &Job{Cwd: cwd, Cmd: cmd, MountConfigs: unorderedMounts()}
			actualCwd, _, _ := realWorkSpace(built)

			job := &Job{Cwd: cwd, Cmd: cmd, MountConfigs: unorderedMounts(), ActualCwd: actualCwd}
			wanted := unorderedMounts()
			key := built.Key()

			keys := make([]string, concurrentReaders)

			var wg sync.WaitGroup

			wg.Add(concurrentReaders + 1)

			for reader := range concurrentReaders {
				go func() {
					defer wg.Done()

					keys[reader] = job.Key()
				}()
			}

			go func() {
				defer wg.Done()

				//nolint:errcheck // the key and the config list are the assertions
				cleanup.Trigger(OnExit, job)
			}()

			wg.Wait()

			for _, got := range keys {
				if got != key {
					wrongKeys++
				}
			}

			if !reflect.DeepEqual(job.MountConfigs, wanted) {
				mutated++
			}
		}

		So(wrongKeys, ShouldEqual, 0)
		So(mutated, ShouldEqual, 0)
	})
}
