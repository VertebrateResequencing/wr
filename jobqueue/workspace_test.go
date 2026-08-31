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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
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
)

// realWorkSpace gives job the working directory mkHashedDir really creates for it
// below job.Cwd, and returns that dir, the workspace holding it, and the tmp dir
// wr makes beside it. The path wr builds is what proves a workspace is wr's own,
// so a hand-made fixture of merely the right shape does not reach the same
// guards.
func realWorkSpace(job *Job) (actualCwd, workSpace, tmpDir string) {
	actualCwd, tmpDir, err := mkHashedDir(job.Cwd, job.Key())
	So(err, ShouldBeNil)

	job.ActualCwd = actualCwd

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

		actualCwd, _, err := mkHashedDir(cwd, job.Key())
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

// keyGrindMax bounds jobWithKeyPrefix's search, which is expected to take about
// 16^len(prefix) tries.
const keyGrindMax = 1 << 20

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
