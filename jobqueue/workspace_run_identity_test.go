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

// This file covers the run identity mkCwdAndTmp records in a workspace, and what
// cleanup does with it: the path a run reports is built from the Job's key, which
// every run of that Job shares, so the record is the only thing that says WHICH
// run's workspace is on disk. See workSpacePaths.proveWSToken.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gofuse "github.com/hanwen/go-fuse/v2/fs"
	. "github.com/smartystreets/goconvey/convey"
)

// fixture strings for the run identity tests.
const (
	runIDCmd = "identity"

	runIDPreciousName = "MINE.txt"
	runIDRemoteName   = "REMOTE_DATA"

	runIDMuxfysDir = muxfysCachePrefix + "_cache123"
)

// keyBlindRow is a PAIR of MountConfigs that Job.Key() cannot tell apart,
// together with what one run configured that way stands to lose to the cleanup of
// another run of the same key.
//
// The ONE thing about a reported working directory that the path can establish is
// the key that built it (relIsJobCreatedCwd), and two runs of one key differ only
// in the digits os.MkdirTemp chose - digits handed out again to the next run of
// that key once a workspace has gone. So a finished run's own ActualCwd can name a
// LIVE run's workspace byte for byte.
//
// That was once accepted as a residual on the grounds that two runs of one key
// mount the same remote at the same place, so the stale run's own keep set still
// names it. These rows are the counterexample: MountConfigs.Key() (mount.go) reads
// only Mount, Target.Profile and Target.Path, and normalises an EMPTY Mount to
// "mnt" - while resolveMountPoint (job.go) gives an empty Mount the working
// DIRECTORY of a Job wr made one for. Nothing that decides where a cache lands -
// CacheBase, Target.CacheDir, Cache, Write - is read at all. So one key covers
// mount configs whose live mounts and un-uploaded output are in DIFFERENT places,
// and the keep set the stale run computes does not name the live run's.
//
// Every row was a deletion wr really made, of the user's remote objects or of a
// writable mount's output before Unmount uploaded it, returning nil. What stops
// each of them now is the run identity recorded in the workspace, which is not
// derived from the path and so is not shared by two runs of one key.
type keyBlindRow struct {
	name string

	// live is the MountConfig of the run whose data is on disk now, and stale
	// that of the finished run of the same key whose ActualCwd names the live
	// run's workspace.
	live  MountConfig
	stale MountConfig

	// mount, when set, is where the live run's remote is really FUSE mounted,
	// spelled relative to the WORKSPACE. A row that leaves it unset needs no
	// mount, because what it stands to lose is cache content waiting for Unmount
	// rather than the remote itself.
	mount string

	// cache are the directories, spelled relative to the workspace, that hold
	// the live run's un-uploaded writable output. A file is planted in each, and
	// must survive.
	cache []string
}

func keyBlindRows() []keyBlindRow {
	// what muxfys names the entries of a writable S3 target's cache dir: the
	// endpoint the Profile resolved to, then the bucket, then the path (its
	// s3.go LocalPath). wr cannot enumerate those, which is why a CacheDir that
	// IS the workspace keeps the whole workspace.
	const s3CacheTree = "s3.example.com/bucket"

	return []keyBlindRow{
		defaultMountPointRow(),
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
			live:  MountConfig{Mount: testWSMount, Targets: runIDCachedTargets()},
			stale: MountConfig{Mount: testWSMount, CacheBase: "../elsewhere", Targets: runIDCachedTargets()},
			cache: []string{runIDMuxfysDir},
		},
	}
}

// defaultMountPointRow is the row of the commonest mounting job, named because
// two tests use it: `wr add --mounts cw:bucket/path` gives no Mount at all,
// which for a Job wr made a working directory for means mount ON that directory,
// while `wr add --mount_json` with a Mount of "mnt" is the same job by key. So
// the stale run's keep set names <cwd>/mnt while every entry of the live run's
// working directory is the user's remote data.
func defaultMountPointRow() keyBlindRow {
	return keyBlindRow{
		name:  "the live run took the default mount point and the stale run named mnt",
		live:  MountConfig{Targets: runIDCachedTargets()},
		stale: MountConfig{Mount: testWSMount, Targets: runIDCachedTargets()},
		mount: createdCwdName,
	}
}

// runIDCachedTargets is a writable cached mount target, for which muxfys chooses
// a cache dir of its own inside whatever CacheBase it was given.
func runIDCachedTargets() []MountTarget {
	return []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}}
}

func TestCleanupOfAnotherRunsWorkSpace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a live and a finished run of one key, configured differently where the key cannot look", t, func() {
		for _, row := range keyBlindRows() {
			Convey(row.name, func() {
				scene, ok := setUpKeyBlindScene(t, row)
				if !ok {
					return
				}

				snap := scene.stale.workSpaceSnapshot()
				cleanErr := snap.cleanupWorkSpace()
				tmpErr := snap.removeTmpDir()

				soPathsExist(scene.survivors...)
				soRefusedAnotherRunsWorkSpace(cleanErr, tmpErr)

				// the refusal is about WHICH run, not about the workspace: the
				// run that really created it still resolves it, so this guard
				// has not simply turned every cleanup off.
				ws, err := scene.live.workSpaceSnapshot().resolveWorkSpace()
				So(err, ShouldBeNil)
				So(ws, ShouldNotBeNil)
				ws.Close()
			})
		}

		Convey("the live run's own cleanup, which keeps its workspace, keeps the record too", func() {
			// defaultMountPointRow again, with the live run's OWN on_exit
			// cleanup going first. Client.Execute fires the behaviours BEFORE
			// Job.Unmount, so
			// the commonest mounting job - `wr add --mounts cw:bucket/path
			// --on_exit cleanup`, whose mount point IS its working directory -
			// sweeps its own workspace while that mount is still up and its
			// writable output is still un-uploaded. That sweep KEEPS the
			// workspace, since the mount is in it, so a record swept with the
			// rest of it would be gone for the remainder of the workspace's
			// life, and the stale run's cleanup would delete the live run's
			// remote data through the live mount exactly as it did before.
			scene, ok := setUpKeyBlindScene(t, defaultMountPointRow())
			if !ok {
				return
			}

			liveSnap := scene.live.workSpaceSnapshot()
			So(liveSnap.cleanupWorkSpace(), ShouldBeNil)
			So(liveSnap.removeTmpDir(), ShouldBeNil)

			staleSnap := scene.stale.workSpaceSnapshot()
			cleanErr := staleSnap.cleanupWorkSpace()
			tmpErr := staleSnap.removeTmpDir()

			soPathsExist(scene.survivors...)
			soRefusedAnotherRunsWorkSpace(cleanErr, tmpErr)
		})
	})
}

// keyBlindScene is a keyBlindRow set up on disk: the live run of that key, whose
// data is really there; the finished run of the same key whose ActualCwd names
// the live run's workspace byte for byte; and everything that must still be there
// when the finished run's cleanup has had its say.
type keyBlindScene struct {
	live  *Job
	stale *Job

	// survivors is every path the live run stands to lose: the file planted in
	// each of the row's cache dirs, the remote data both behind the row's mount
	// and read through it, and a file of the user's outside the workspace
	// altogether.
	survivors []string
}

// setUpKeyBlindScene builds row's pair of runs, gives them the one workspace, and
// plants everything that must survive. ok is false when the row needs a FUSE
// mount this host will not give it, in which case the caller must return.
func setUpKeyBlindScene(t *testing.T, row keyBlindRow) (keyBlindScene, bool) {
	t.Helper()

	if row.mount != "" && !canMountFuse() {
		SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

		return keyBlindScene{}, false
	}

	cwd := t.TempDir()
	precious := writeFileIn(filepath.Join(cwd, "user_scripts"), runIDPreciousName)

	live := &Job{Cwd: cwd, Cmd: runIDCmd, MountConfigs: MountConfigs{row.live}}
	stale := &Job{Cwd: cwd, Cmd: runIDCmd, MountConfigs: MountConfigs{row.stale}}

	// the premise: the two configs are one Job, so the path the stale run
	// reports is one the live run's key builds too.
	So(stale.Key(), ShouldEqual, live.Key())

	reusedCwd, workSpace, liveToken := reuseWorkSpaceName(stale)
	live.ActualCwd, live.ActualCwdToken = reusedCwd, liveToken

	survivors := runIDPlant(workSpace, row.cache)

	if row.mount != "" {
		mountPoint := filepath.Join(workSpace, row.mount)
		survivors = append(survivors,
			mountRemoteOver(t, mountPoint), filepath.Join(mountPoint, runIDRemoteName))
	}

	return keyBlindScene{live: live, stale: stale, survivors: append(survivors, precious, cwd)}, true
}

// reuseWorkSpaceName arranges the name reuse os.MkdirTemp really allows: stale's
// own run finishes and its workspace goes, so the next run of the same key to ask
// is handed that same name. The workspace is recreated through mkCwdAndTmp, the
// production creation path, so what is recorded in it is the LIVE run's own
// identity, while stale still reports the one its own creation gave it.
//
// It returns the working directory and the workspace inside it - byte for byte the
// ones stale reports - and the identity the live run has them under.
func reuseWorkSpaceName(stale *Job) (reusedCwd, workSpace, liveToken string) {
	finished, workSpace, _ := realWorkSpace(stale)

	So(os.RemoveAll(workSpace), ShouldBeNil)
	So(os.MkdirAll(workSpace, os.ModePerm), ShouldBeNil)

	var err error

	reusedCwd, _, liveToken, err = mkCwdAndTmp(workSpace)
	So(err, ShouldBeNil)
	So(reusedCwd, ShouldEqual, finished)

	// the two really are different runs: were they to share an identity, every
	// refusal below would be proving nothing.
	So(liveToken, ShouldNotBeBlank)
	So(stale.ActualCwdToken, ShouldNotBeBlank)
	So(liveToken, ShouldNotEqual, stale.ActualCwdToken)

	return reusedCwd, workSpace, liveToken
}

// runIDPlant creates each of the given dirs, spelled relative to base, with a
// file in it that must survive, and returns those files' paths.
func runIDPlant(base string, rels []string) []string {
	planted := make([]string, 0, len(rels))

	for _, rel := range rels {
		planted = append(planted, writeFileIn(filepath.Join(base, rel), runIDRemoteName))
	}

	return planted
}

// mountRemoteOver plants a file of the user's remote data in a directory OUTSIDE
// the Job's Cwd and really FUSE mounts that directory over mountPoint, returning
// the planted file's path. The mount is taken down when the test ends.
//
// go-fuse's loopback filesystem is what makes the mount real, and it is the
// cheapest one an unprivileged process can raise. What is behind it is an
// ordinary directory, so what survived can be asserted about directly; muxfys
// would need an object store to talk to, and the deletion under test does not
// care which filesystem is mounted, only that a mount gets crossed - os.Root
// bounds a deletion with RESOLVE_BENEATH, which does not imply RESOLVE_NO_XDEV.
func mountRemoteOver(t *testing.T, mountPoint string) string {
	t.Helper()

	backing := t.TempDir()
	remote := writeFileIn(backing, runIDRemoteName)

	root, err := gofuse.NewLoopbackRoot(backing)
	So(err, ShouldBeNil)
	So(os.MkdirAll(mountPoint, os.ModePerm), ShouldBeNil)

	server, err := gofuse.Mount(mountPoint, root, &gofuse.Options{})
	So(err, ShouldBeNil)

	t.Cleanup(func() {
		if uerr := server.Unmount(); uerr != nil {
			t.Logf("could not unmount the remote at %s: %s", mountPoint, uerr)
		}
	})

	return remote
}

func TestWorkSpaceRunRecord(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given the workspace one run of a job really created", t, func() {
		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: runIDCmd}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)

		output := writeFileIn(actualCwd, "out.txt")
		record := filepath.Join(workSpace, createdWSTokenName)

		Convey("the run it records sweeps it, record and all", func() {
			// the sweep KEEPS the record - it has to, so that a sweep which
			// keeps the workspace leaves no live workspace unidentified - and
			// the emptied workspace is still reclaimed, because the one removal
			// entitled to delete the record takes the directory with it
			// (removeDirHoldingOnlyWSToken). A second cleanup of the same run then reads
			// an absent record, which the pre-upgrade allowance covers.
			So(job.workSpaceSnapshot().cleanupWorkSpace(), ShouldBeNil)

			soPathsGone(output, record, actualCwd, tmpDir, workSpace)
			soPathsExist(cwd)
		})

		Convey("a run that reports the path with no identity of its own is refused", func() {
			// the record being there proves a wr that writes records created the
			// workspace, so a report with no identity did not come from the run
			// that made it. Allowing it would put every same-key residual back:
			// the reporting run would only have to be one that never learned its
			// own token to have another run's live workspace swept.
			job.ActualCwdToken = ""

			snap := job.workSpaceSnapshot()

			cleanErr, tmpErr := snap.cleanupWorkSpace(), snap.removeTmpDir()

			soPathsExist(output, record, actualCwd, tmpDir, workSpace)
			soRefusedAnotherRunsWorkSpace(cleanErr, tmpErr)
		})

		Convey("a record that is not a regular file is refused rather than read", func() {
			// the record's own name is the one component of its path that the
			// workspace proof does not cover, and an os.Root follows a relative
			// symlink that stays inside its root. So whoever can write in the
			// workspace can leave a link there pointing at a file holding the
			// identity the DELETER reports, and be swept as if it were its own -
			// unless a record that is not a plain file is refused unread.
			reporting := "the-reporting-run"
			planted := filepath.Join(cwd, "planted_token")
			So(os.WriteFile(planted, []byte(reporting), 0o600), ShouldBeNil)

			// the link is spelled relative and stays inside the Job's Cwd,
			// which is the only kind an os.Root will follow at all.
			via, err := filepath.Rel(workSpace, planted)
			So(err, ShouldBeNil)

			So(os.Remove(record), ShouldBeNil)
			So(os.Symlink(via, record), ShouldBeNil)

			job.ActualCwdToken = reporting

			snap := job.workSpaceSnapshot()

			cleanErr, tmpErr := snap.cleanupWorkSpace(), snap.removeTmpDir()

			soPathsExist(output, actualCwd, tmpDir, workSpace)
			soRefusedAnotherRunsWorkSpace(cleanErr, tmpErr)
		})

		Convey("an empty record is not no record, and is refused too", func() {
			// a record that is THERE, however unreadable, says a wr that writes
			// records made this workspace, so a deleter still has to show it is
			// the run recorded - and nothing can be shown against a record that
			// says nothing. Absence is the only state that has to be allowed.
			So(os.WriteFile(record, nil, 0o600), ShouldBeNil)

			job.ActualCwdToken = ""

			snap := job.workSpaceSnapshot()

			cleanErr, tmpErr := snap.cleanupWorkSpace(), snap.removeTmpDir()

			soPathsExist(output, record, actualCwd, tmpDir, workSpace)
			soRefusedAnotherRunsWorkSpace(cleanErr, tmpErr)
		})

		Convey("a workspace with no record at all is swept, as a pre-upgrade wr's has none", func() {
			// every workspace wr made before it recorded runs has no record, and
			// refusing those would leak all of them for ever. It is also the state
			// after this same run's own first sweep; see absenceRule.
			So(os.Remove(record), ShouldBeNil)

			job.ActualCwdToken = ""

			So(job.workSpaceSnapshot().cleanupWorkSpace(), ShouldBeNil)

			soPathsGone(output, actualCwd, tmpDir, workSpace)
			soPathsExist(cwd)
		})
	})
}

// TestWorkSpaceRecordAfterUnmount covers the OTHER place a workspace directory
// is removed: the empty-dir tidy-up Job.Unmount makes from each mount point once
// the mounts are down (rmEmptyMountDirs). For the commonest mounting job the
// mount point IS the working directory, so that walk starts BELOW the workspace
// and climbs THROUGH it - and what it finds there is the record cleanup keeps.
// Unless a directory holding nothing but the record counts as empty for a parent
// as well as for a leaf, every such job leaks its workspace and every hashed
// level above it.
func TestWorkSpaceRecordAfterUnmount(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a mounting job whose mount point is its working directory", t, func() {
		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: runIDCmd, MountConfigs: MountConfigs{
			{Targets: []MountTarget{{Path: testWSTargetPath}}},
		}}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)

		record := filepath.Join(workSpace, createdWSTokenName)
		hashedBase := filepath.Join(cwd, AppName+createdCwdBaseSuffix)

		Convey("its own cleanup keeps the mount point and the record, and Unmount reclaims both", func() {
			// nothing is really mounted: what the walk may climb comes from the
			// Job's MountConfigs rather than from what is mounted now, which is
			// what lets a caller clean up on either side of Unmount.
			So(job.workSpaceSnapshot().cleanupWorkSpace(), ShouldBeNil)
			So(job.workSpaceSnapshot().removeTmpDir(), ShouldBeNil)

			soPathsExist(record, actualCwd, workSpace)
			soPathsGone(tmpDir)

			logs, err := job.Unmount()
			So(err, ShouldBeNil)
			So(logs, ShouldBeBlank)

			soPathsGone(record, actualCwd, workSpace, hashedBase)
			soPathsExist(cwd)
		})
	})
}

// TestWorkSpaceRecordThroughItsOwnRemoval covers the one removal in wr entitled
// to delete a run record, which can only do it by unlinking the record and then
// removing the directory it was in - two steps, because no syscall removes a
// directory and its last entry at once, and rmdir will not take a directory the
// record is still in.
//
// So there is a window in which the record is gone and the directory it
// identified is not, and the run that owns that directory can be in the middle
// of building its workspace: the leaf os.MkdirTemp handed it is empty until
// writeWSToken runs, which is exactly the state a stale cleanup's proof reads as
// "no record, this may be mine".
func TestWorkSpaceRecordThroughItsOwnRemoval(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a stale cleanup that resolved a workspace before the live run recorded itself", t, func() {
		cwd := t.TempDir()
		stale := &Job{Cwd: cwd, Cmd: runIDCmd}
		_, workSpace, _ := realWorkSpace(stale)

		// the name reuse os.MkdirTemp allows, caught one step earlier than
		// reuseWorkSpaceName catches it: the live run of the same key has been
		// handed the name back and os.MkdirTemp has returned, so the leaf is
		// there and EMPTY, with writeWSToken still to run.
		So(os.RemoveAll(workSpace), ShouldBeNil)
		So(os.MkdirAll(workSpace, os.ModePerm), ShouldBeNil)

		record := filepath.Join(workSpace, createdWSTokenName)
		liveCwd := filepath.Join(workSpace, createdCwdName)
		hashedBase := filepath.Join(cwd, AppName+createdCwdBaseSuffix)

		Reset(func() {
			cleanupProvenHook = nil
			wsTokenUnlinkedHook = nil
		})

		Convey("the record it unlinks is put back when the live run keeps the directory", func() {
			var liveToken string

			// the live run's own mkCwdAndTmp, interleaved with the stale
			// cleanup at the two moments that matter: the record goes down
			// after the proof has read the leaf as unrecorded, and the working
			// directory goes down while the record is unlinked, which is what
			// stops the directory going and so leaves it live.
			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				var err error

				liveToken, err = writeWSToken(workSpace)
				So(err, ShouldBeNil)
			}

			wsTokenUnlinkedHook = func() {
				wsTokenUnlinkedHook = nil

				So(os.Mkdir(liveCwd, os.ModePerm), ShouldBeNil)
			}

			So(stale.workSpaceSnapshot().cleanupWorkSpace(), ShouldBeNil)

			soPathsExist(record, liveCwd, workSpace)

			// the record must still be the LIVE run's own, byte for byte:
			// without it the workspace is unrecorded for the rest of its life
			// and every same-key deletion is available against it again, this
			// time with wr's own hand having removed the protection.
			content, err := os.ReadFile(record)
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, liveToken)
		})

		Convey("the directory going under it inside the window is not a failure", func() {
			// the two cleanups of a lost job run in different processes, so the
			// directory this one has just unlinked the record from can be taken
			// by the other one while the record is down. There is then no live
			// workspace left to record and nothing to restore the record into -
			// the same interleaving removeLeaf already treats as ordinary when
			// its own Remove is the call that finds the directory gone. It must
			// not fail the cleanup, and through it (Client.Execute folds a
			// behaviour's error into the Job's own) a Job whose Cmd succeeded.
			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				_, err := writeWSToken(workSpace)
				So(err, ShouldBeNil)
			}

			wsTokenUnlinkedHook = func() {
				wsTokenUnlinkedHook = nil

				So(os.RemoveAll(workSpace), ShouldBeNil)
			}

			So(stale.workSpaceSnapshot().cleanupWorkSpace(), ShouldBeNil)

			// gone counts as removed, as it does at the leaf, so the hashed levels
			// above the workspace are still tidied rather than left behind.
			soPathsGone(record, workSpace, hashedBase)
			soPathsExist(cwd)
		})

		Convey("a record written by someone else inside the window is left alone", func() {
			// the compound race O_EXCL is there for: another cleanup removes the
			// emptied workspace while this one holds the record down, os.MkdirTemp
			// hands that same leaf name to a new run of the key, and that run
			// records ITSELF in it. The bytes at that name are then the new run's
			// protection, and are not this stale copy's to overwrite.
			const foreign = "6f1c0b1e-0000-4000-8000-000000000001"

			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				_, err := writeWSToken(workSpace)
				So(err, ShouldBeNil)
			}

			wsTokenUnlinkedHook = func() {
				wsTokenUnlinkedHook = nil

				So(os.WriteFile(record, []byte(foreign), wsTokenPerm), ShouldBeNil)
			}

			err := stale.workSpaceSnapshot().cleanupWorkSpace()

			content, readErr := os.ReadFile(record)
			So(readErr, ShouldBeNil)
			So(string(content), ShouldEqual, foreign)

			soPathsExist(workSpace)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "now records another run")
		})

		Convey("a record too long to be one of wr's is not unlinked at all", func() {
			// putting a record back means holding its bytes, and the record
			// lives where the Job's own Cmd can write, so it is as long as that
			// Cmd made it. Nothing longer than the cap can be a record wr wrote,
			// and truncating it to the cap would be wr rewriting a file of the
			// user's, so such a directory is left behind whole instead.
			oversized := strings.Repeat("x", wsTokenMaxBytes+1)

			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				So(os.WriteFile(record, []byte(oversized), wsTokenPerm), ShouldBeNil)
			}

			So(stale.workSpaceSnapshot().cleanupWorkSpace(), ShouldBeNil)

			soPathsExist(record, workSpace)

			content, err := os.ReadFile(record)
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, oversized)
		})
	})
}

// soRefusedAnotherRunsWorkSpace asserts that each of the given errors is the
// refusal a workspace recording another run must produce: loud, and naming why,
// since a leaked workspace is recoverable and a deleted one is not.
func soRefusedAnotherRunsWorkSpace(errs ...error) {
	for _, err := range errs {
		So(err, ShouldNotBeNil)
		So(errors.Is(err, errNotThisRunsWorkSpace), ShouldBeTrue)
	}
}
