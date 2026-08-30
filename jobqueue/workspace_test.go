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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// fixture strings shared by the workspace tests.
const (
	testWSMount      = "mnt"
	testWSTargetPath = "s3/path"
	testWSCmd        = "run"
)

// realWorkSpace gives job the working directory mkHashedDir really creates for
// it below job.Cwd, and returns that dir, the workspace holding it, and the tmp
// dir wr makes beside it.
//
// Fixtures built by hand, at paths and depths wr cannot produce, are a large
// part of why holes in this code survived earlier rounds of testing, and the
// path wr builds is now also what proves a workspace is wr's own - so a fixture
// that is merely the right shape is no longer the same test.
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
	// for this Job, rather than merely sitting at the right depth under the
	// right name. It is pinned against mkHashedDir here so the two cannot drift
	// apart: if they did, cleanup would quietly stop tolerating a working
	// directory a Job's own Cmd deleted, rather than fail loudly.
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

		Convey("nor one whose unique dir was not made by MkdirTemp", func() {
			names := strings.Split(rel, string(filepath.Separator))
			names[len(names)-2] = job.Key()[mkHashedLevels-1:]
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeFalse)
		})

		Convey("and deliberately ignores the AppName base component", func() {
			// AppName is a package var: cmd/runner.go sets it to "wr" while the
			// manager leaves it "jobqueue", and cleanup runs in both. Keying on
			// that component would refuse every real workspace server-side.
			names := strings.Split(rel, string(filepath.Separator))
			names[0] = "someone_elses_cwd"
			So(relIsJobCreatedCwd(filepath.Join(names...), job.Key()), ShouldBeTrue)
		})
	})
}

func TestCleanupKeepsMountCaches(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// cmd/add.go promises that cleanup ignores "any mount cache directories, so
	// that nothing on your remote file systems gets deleted". Cleanup runs
	// BEFORE Job.Unmount (client.go), and a cached writable mount only uploads
	// at Unmount, so deleting one of these destroys the job's own output. Where
	// each cache lands is worked out by the same resolveCacheBase/resolveCacheDir
	// that Job.Mount uses to put it there.
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
			job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{Mount: testWSMount}}}
			_, workSpace, _ := realWorkSpace(job)

			abs := filepath.Join(workSpace, "abscache")
			job.MountConfigs[0].Targets = []MountTarget{{Path: testWSTargetPath, Cache: true, CacheDir: abs}}

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

		Convey("A muxfys-named dir is still deleted for a Job that mounts nothing", func() {
			// the name rule exists only to cover the cache dir muxfys names for
			// itself inside a mount's CacheBase. A Job with no mounts has no
			// cache, so a dir its Cmd happened to name that way is just output.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			_, workSpace, _ := realWorkSpace(job)

			decoy := writeFileIn(filepath.Join(workSpace, muxfysCachePrefix+"_decoy"), "junk.txt")

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsGone(decoy, workSpace)
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
	// workspace - which is what requiring the working directory to exist did,
	// until the path itself could prove the workspace was wr's own.
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
		// a proof is about a path string, and every syscall re-resolves it, so
		// the working directory proven to be real can be a symlink by the time
		// the sweep reads it - and a read follows a symlinked final component,
		// deleting the target's contents instead. The hook puts the test in that
		// moment, which is otherwise not reachable reliably.
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
	})
}

func TestCleanupActualCwdRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Cleanup reads a Job's ActualCwd under the Job's lock", t, func() {
		// applyLiveSnapshot writes ActualCwd under the Job's lock every touch
		// interval, and the manager runs cleanup for a lost job at the same
		// time, so an unlocked read here is a data race whose outcome decides
		// which directory gets deleted. -race is what makes this test bite.
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
