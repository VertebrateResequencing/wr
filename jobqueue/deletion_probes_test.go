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

// This file is where adversarial probes of wr's deletion guards land. Each ROW
// of the tables below names one route by which a Job's two directory fields,
// its mount configuration, its own Cmd or a concurrent run of it could aim a
// deletion at something wr did not create, and asks the production entry points
// about it.
//
// A row asserts two things, in this order: that a file of the user's is still
// there, and - where the guard gives a distinguishable error - that the refusal
// carried that error's class, so a change which starts refusing for the wrong
// reason is caught too. Survival is asserted first because Convey's FailureHalts
// stops a leaf at its first failed So, and a deletion is the failure that
// matters.
//
// New probe attempts belong here, as a row in whichever table fits, rather than
// as a function of their own. A route already pinned by workspace_test.go or
// behaviours_test.go belongs there, not here.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// fixture strings shared by the probe tables.
const (
	probeCmd = "probe"

	// probeMineName is a file the user created, which no probe may lose, and
	// probeRemoteName one behind a live mount or in an un-uploaded cache, which
	// no probe may lose either.
	probeMineName   = "MINE.txt"
	probeRemoteName = "REMOTE_DATA"

	// probeMuxfysDir is named the way muxfys names the cache dir it chooses for
	// itself inside whatever CacheBase it was given.
	probeMuxfysDir = muxfysCachePrefix + "_cache123"

	// probeBareLeaf and probeOtherLeaf name the two decoy dirs of probeWorld,
	// and probeDigits is what os.MkdirTemp appends to a name it chooses.
	probeBareLeaf  = "bare prefix"
	probeOtherLeaf = "user's own"
	probeDigits    = "0123456789"

	// probeSiblingDigits turns a workspace name into the name os.MkdirTemp would
	// have given ANOTHER run of the same Job.
	probeSiblingDigits = "999"
)

// probeWorld is the filesystem a (Cwd, ActualCwd) probe runs against: the
// workspace mkHashedDir really built for a Job, a file of the user's own in
// every directory a deletion aimed at the wrong place could land in, and another
// run of the SAME Job beside it.
type probeWorld struct {
	// root is the parent of cwd, cwd is the Job's own Cwd, base is the
	// <AppName>_cwd component below it, and hashed is the deepest hashed level,
	// ie. the workspace's own parent.
	root   string
	cwd    string
	base   string
	hashed string

	workSpace string
	actualCwd string

	// tmpDir is the dir wr made beside the working directory to be the Job's
	// TMPDIR, which a licensed sweep takes with the rest of the workspace.
	tmpDir string

	// decoys are directories of the USER's, beside the Job's own workspace and
	// named the two ways a name os.MkdirTemp chose can be mis-recognised: the
	// bare prefix wr would have used had it not needed a unique dir, and a name
	// of the user's own at the same depth. Whoever submits the Job picks the Cmd
	// that Key() hashes, so they can put a directory at either.
	decoys map[string]string

	// output is this run's own file, which cleanup IS entitled to delete.
	output string

	// sibling is another run of the same Job, differing only in the digits
	// os.MkdirTemp chose, and siblingFile its live output.
	sibling     string
	siblingFile string

	// mine are the user's files, in every place a deletion could stray to.
	mine []string
}

// decoy is the working directory a probe reports inside one of the world's decoy
// dirs, which is a real dir of the user's at the depth wr creates at.
func (w probeWorld) decoy(name string) string {
	return filepath.Join(w.hashed, w.decoys[name], createdCwdName)
}

// seedProbeWorld gives job the workspace mkHashedDir really creates below a
// fresh Cwd, and surrounds it with the user's own files.
func seedProbeWorld(t *testing.T, job *Job) probeWorld {
	t.Helper()

	w := probeWorld{root: t.TempDir()}
	w.cwd = filepath.Join(w.root, "project")
	So(os.MkdirAll(w.cwd, os.ModePerm), ShouldBeNil)

	job.Cwd = w.cwd
	w.actualCwd, w.workSpace, w.tmpDir = realWorkSpace(job)
	w.base = filepath.Join(w.cwd, AppName+createdCwdBaseSuffix)
	w.hashed = filepath.Dir(w.workSpace)
	w.decoys = map[string]string{
		probeBareLeaf:  strings.TrimRight(filepath.Base(w.workSpace), probeDigits),
		probeOtherLeaf: "zzzz1234",
	}

	for _, leaf := range w.decoys {
		w.mine = append(w.mine, writeFileIn(filepath.Join(w.hashed, leaf, createdCwdName), probeMineName))
	}

	for _, dir := range []string{
		w.root, w.cwd, filepath.Join(w.cwd, "my_scripts"),
		w.base, filepath.Join(w.base, "not_a_hash"),
		w.hashed, filepath.Join(w.hashed, "someone_elses"),
	} {
		w.mine = append(w.mine, writeFileIn(dir, probeMineName))
	}

	w.sibling = filepath.Join(w.workSpace+probeSiblingDigits, createdCwdName)
	w.siblingFile = writeFileIn(w.sibling, "other_run.txt")
	w.output = writeFileIn(w.actualCwd, "own_output.txt")

	return w
}

// probeCwdRow is one (Cwd, ActualCwd) pair to ask the real cleanup about: the
// path a run really produced, mutated the way a stale, hand-edited,
// display-derived or maliciously-reported value could mutate it.
type probeCwdRow struct {
	name string

	// cwd is the Cwd the Job carries, or nil for the world's own; actualCwd is
	// the value the Job reports as its working directory.
	cwd       func(w probeWorld) string
	actualCwd func(w probeWorld) string

	// wantErr is the error class the refusal must carry, or nil where the pair
	// names something wr really created, or nothing at all.
	wantErr error

	// sweptOwn and sweptSibling say which of the two runs' working directories
	// the pair licenses cleanup to delete. Both false means it licenses nothing.
	sweptOwn     bool
	sweptSibling bool
}

func probeCwdRows() []probeCwdRow {
	own := func(w probeWorld) string { return w.actualCwd }

	return []probeCwdRow{
		// the same directory, spelled every way a value that reached wr through
		// a display, a config file or a shell could spell it. Each is cleaned
		// before anything is compared, so each must reach the same verdict as
		// the control: sweep this Job's own workspace and nothing else.
		{name: "the path the run really used (control)", actualCwd: own, sweptOwn: true},
		{
			name:      "that path with a trailing slash",
			actualCwd: func(w probeWorld) string { return w.actualCwd + "/" },
			sweptOwn:  true,
		},
		{
			name:      "that path with a trailing /.",
			actualCwd: func(w probeWorld) string { return w.actualCwd + "/." },
			sweptOwn:  true,
		},

		// every level of wr's own tree except the working directory itself. Each
		// is at the wrong depth for the path mkHashedDir builds, and treating one
		// as a working directory would hand its parent to the sweep.
		{
			name:      "one level deeper, still called cwd",
			actualCwd: func(w probeWorld) string { return filepath.Join(w.actualCwd, createdCwdName) },
			wantErr:   errNotACreatedCwd,
		},
		{
			name:      "the workspace holding it",
			actualCwd: func(w probeWorld) string { return w.workSpace },
			wantErr:   errNotACreatedCwd,
		},
		{
			name:      "the deepest hashed level",
			actualCwd: func(w probeWorld) string { return w.hashed },
			wantErr:   errNotACreatedCwd,
		},
		{
			name:      "the _cwd base itself",
			actualCwd: func(w probeWorld) string { return w.base },
			wantErr:   errNotACreatedCwd,
		},

		// directories of the user's own, at each level of that tree.
		{
			name:      "a dir of the user's inside the _cwd base",
			actualCwd: func(w probeWorld) string { return filepath.Join(w.base, "not_a_hash") },
			wantErr:   errNotACreatedCwd,
		},
		{
			name:      "a dir of the user's inside the deepest hashed level",
			actualCwd: func(w probeWorld) string { return filepath.Join(w.hashed, "someone_elses") },
			wantErr:   errNotACreatedCwd,
		},

		// the unique leaf os.MkdirTemp named, mutated. Whoever submits the Job
		// picks the Cmd that Key() hashes, so they can put a directory of their
		// own beside the Job's at the prefix that name starts with.
		{
			name:      "a dir of the user's at the leaf's bare prefix",
			actualCwd: func(w probeWorld) string { return w.decoy(probeBareLeaf) },
			wantErr:   errNotACreatedCwd,
		},
		{
			name:      "a dir of the user's at a leaf name of their own",
			actualCwd: func(w probeWorld) string { return w.decoy(probeOtherLeaf) },
			wantErr:   errNotACreatedCwd,
		},

		// ANOTHER run of the same Job, which the path check accepts by design:
		// the key is what it recognises, and two runs of one key differ only in
		// the digits os.MkdirTemp chose. Only one run of a key is live at a time,
		// so this is a documented residual rather than an escape - but it must
		// still stop at the sibling, taking nothing of the user's with it.
		{
			name:         "another run of the same job",
			actualCwd:    func(w probeWorld) string { return w.sibling },
			sweptSibling: true,
		},

		// the Cwd half of the pair. Cwd is not part of a Job's key unless it is
		// CwdMatters, so moving it leaves the reported working directory at the
		// wrong depth below it rather than changing what the key builds.
		{
			name:      "Cwd raised to its own parent",
			cwd:       func(w probeWorld) string { return w.root },
			actualCwd: own,
			wantErr:   errNotACreatedCwd,
		},
		{
			name:      "Cwd lowered to the _cwd base",
			cwd:       func(w probeWorld) string { return w.base },
			actualCwd: own,
			wantErr:   errNotACreatedCwd,
		},
		{
			name:      "Cwd the filesystem root",
			cwd:       func(_ probeWorld) string { return "/" },
			actualCwd: own,
			wantErr:   errNotACreatedCwd,
		},

		// a blank ActualCwd means wr never learned of a working directory for
		// this Job, so there is nothing it may delete and nothing to say.
		{name: "ActualCwd blank", actualCwd: func(_ probeWorld) string { return "" }},

		// the same directory named through a path that leaves Cwd and comes back
		// to it: /proc/self/cwd is the running process's own directory, so this
		// is a spelling only the kernel can resolve.
		{
			name: "ActualCwd named through /proc/self/cwd",
			actualCwd: func(w probeWorld) string {
				rel, err := filepath.Rel(w.cwd, w.actualCwd)
				So(err, ShouldBeNil)

				return filepath.Join(procSelfCwd, rel)
			},
			wantErr: errNotBelowBaseDir,
		},
	}
}

// procSelfCwd is where Linux names a process's own working directory. A Job's
// ActualCwd spelled through it names a directory only the kernel can resolve.
const procSelfCwd = "/proc/self/cwd"

func TestProbeReportedDirectories(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given the workspace wr really made and the user's files all around it", t, func() {
		for _, row := range probeCwdRows() {
			Convey(row.name, func() {
				job := &Job{Cmd: probeCmd, Behaviours: Behaviours{{When: OnExit, Do: CleanupAll}}}
				w := seedProbeWorld(t, job)

				if row.cwd != nil {
					job.Cwd = row.cwd(w)
				}

				job.ActualCwd = row.actualCwd(w)

				err := job.Behaviours.Trigger(false, job)

				soPathsExist(w.mine...)
				soPathsExist(w.cwd)
				soProbeSwept(row.sweptOwn, w.output, w.actualCwd, w.tmpDir)
				soProbeSwept(row.sweptSibling, w.siblingFile, w.sibling)

				soProbeRefused(err, row.wantErr)
			})
		}
	})

	Convey("A Job whose Cwd is itself a symlink sweeps its own workspace and nothing else", t, func() {
		// Job.Cwd is stored exactly as the user typed it, because it feeds
		// Job.Key(), so a symlinked spelling is one wr is GIVEN. Everything below
		// it is then spelled that way too, and a containment proof that resolved
		// the symlink on one side only would refuse every such Job, silently
		// leaking a workspace per run.
		base := t.TempDir()
		real := filepath.Join(base, "real")
		So(os.MkdirAll(real, os.ModePerm), ShouldBeNil)

		link := filepath.Join(base, "cwd_link")
		So(os.Symlink(real, link), ShouldBeNil)

		precious := writeFileIn(filepath.Join(base, "user_scripts"), probeMineName)

		job := &Job{Cmd: probeCmd, Cwd: link}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)
		output := writeFileIn(actualCwd, "own_output.txt")

		err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job)

		soPathsExist(precious, real, link)
		soPathsGone(output, actualCwd, tmpDir, workSpace)

		So(err, ShouldBeNil)
	})
}

// soProbeSwept asserts that a directory and the file in it either went or
// stayed, according to whether the row licensed cleanup to delete it.
func soProbeSwept(swept bool, paths ...string) {
	if swept {
		soPathsGone(paths...)

		return
	}

	soPathsExist(paths...)
}

// soProbeRefused asserts the refusal a row expected: nothing at all, or an error
// of the given class.
func soProbeRefused(err error, wantErr error) {
	if wantErr == nil {
		So(err, ShouldBeNil)

		return
	}

	So(err, ShouldNotBeNil)
	So(errors.Is(err, wantErr), ShouldBeTrue)
}

// probeMountRow is one MountConfig shape, with the directories mounting it
// really puts a live mount point or an un-uploaded cache in.
//
// Cleanup runs BEFORE Job.Unmount (client.go) and a cached writable mount only
// uploads at Unmount, so a directory this table gets wrong is the user's remote
// objects, or the job's own results, deleted before they ever left the machine.
type probeMountRow struct {
	name string
	mc   MountConfig

	// setUp arranges whatever the shape needs on disk beyond the planting
	// below, such as the symlink a mount or a cache is spelled through. It is
	// called once the workspace exists, so the Job's key is already settled.
	setUp func(actualCwd, workSpace string)

	// live, gone and kept are spelled relative to the WORKSPACE - "." being the
	// workspace itself, "cwd" the working directory and "tmp" the job's TMPDIR -
	// and are written out LITERALLY rather than derived with the resolvers the
	// code under test uses, so a row cannot agree with a resolution bug.
	//
	// A file is planted in each of live, and must survive. gone are the paths
	// cleanup and the TMPDIR removal must between them have deleted, and kept
	// those they must have left.
	live []string
	gone []string
	kept []string
}

// probeCachedTargets is a writable cached mount target, for which muxfys chooses
// a cache dir of its own inside whatever CacheBase it was given.
func probeCachedTargets() []MountTarget {
	return []MountTarget{{Path: "bucket/sub", Cache: true, Write: true}}
}

// probeSymlink replaces link with a symlink to target.
func probeSymlink(target, link string) {
	So(os.Symlink(target, link), ShouldBeNil)
}

func probeMountRows() []probeMountRow {
	const (
		output  = createdCwdName + "/own_output.txt"
		scratch = createdTmpName + "/scratch.txt"
		mount   = "mnt"
	)

	return []probeMountRow{
		// the job's TMPDIR is inside the workspace, so a mount or a cache put
		// there has to survive not just the sweep but the SEPARATE removal
		// Execute makes of the tmp dir on every exit, cleanup Behaviour or none.
		{
			name: "a mount point that IS the job's TMPDIR",
			mc:   MountConfig{Mount: "../" + createdTmpName, Targets: probeCachedTargets()},
			live: []string{createdTmpName, probeMuxfysDir},
			kept: []string{".", createdTmpName, scratch},
			gone: []string{createdCwdName, output},
		},
		{
			name: "a mount point inside the job's TMPDIR",
			mc:   MountConfig{Mount: "../" + createdTmpName + "/m", Targets: probeCachedTargets()},
			live: []string{createdTmpName + "/m", probeMuxfysDir},
			kept: []string{".", createdTmpName, scratch},
			gone: []string{createdCwdName, output},
		},
		{
			name: "a muxfys CacheBase inside the job's TMPDIR",
			mc: MountConfig{
				Mount: mount, CacheBase: "../" + createdTmpName, Targets: probeCachedTargets(),
			},
			live: []string{createdCwdName + "/" + mount, createdTmpName + "/" + probeMuxfysDir},
			kept: []string{".", createdCwdName, createdTmpName, scratch},
			gone: []string{output},
		},
		{
			name: "an explicit CacheDir inside the job's TMPDIR",
			mc: MountConfig{Mount: mount, Targets: []MountTarget{{
				Path: "bucket/sub", CacheDir: createdTmpName + "/c", Write: true,
			}}},
			live: []string{createdCwdName + "/" + mount, createdTmpName + "/c"},
			kept: []string{".", createdCwdName, createdTmpName, scratch},
			gone: []string{output},
		},

		// a mount point and a cache location ABOVE the workspace need protecting
		// nowhere, since the sweep only goes inside it - but the upward walk of
		// empty parents does go higher, and has to stop at the first directory
		// that will not go, which is the one holding them.
		{
			name: "a mount point and a cache dir above the workspace",
			mc: MountConfig{Mount: "../../up", Targets: []MountTarget{{
				Path: "bucket/sub", CacheDir: "..", Write: true,
			}}},
			live: []string{"../up", "../bucket/sub"},
			kept: []string{".."},
			gone: []string{".", createdCwdName, createdTmpName, output, scratch},
		},

		// a mount point at the workspace makes everything wr created for the Job
		// the inside of a live mount, TMPDIR included: reclaiming that dir is a
		// separate deletion from the sweep, so it needs its own answer.
		{
			name: "a mount point that IS the workspace, which puts the TMPDIR inside a live mount",
			mc:   MountConfig{Mount: "..", Targets: probeCachedTargets()},
			live: []string{".", createdCwdName, createdTmpName},
			kept: []string{".", createdCwdName, createdTmpName, output, scratch},
		},

		// a symlink INSIDE the tree being swept makes the two spellings of a
		// mount or cache location agree LEXICALLY while naming different entries
		// of it, so only the resolved spelling names the directory the data is
		// physically in - the one a sweep would recurse into.
		{
			name: "a muxfys CacheBase spelled through a symlink inside the working dir",
			mc:   MountConfig{Mount: mount, CacheBase: "clink", Targets: probeCachedTargets()},
			setUp: func(actualCwd, _ string) {
				So(os.MkdirAll(filepath.Join(actualCwd, "realcache"), os.ModePerm), ShouldBeNil)
				probeSymlink("realcache", filepath.Join(actualCwd, "clink"))
			},
			live: []string{
				createdCwdName + "/realcache/" + probeMuxfysDir, createdCwdName + "/" + mount,
			},
			kept: []string{".", createdCwdName, createdCwdName + "/realcache"},
			// the symlink itself is not a dir, so the sweep unlinks it: what had
			// to be recognised is the dir it led to, which is where the cache is.
			gone: []string{output, createdCwdName + "/clink", createdTmpName, scratch},
		},
		{
			name: "an explicit CacheDir spelled through a symlink inside the workspace",
			mc: MountConfig{Mount: mount, Targets: []MountTarget{{
				Path: "bucket/sub", CacheDir: "clink", Write: true,
			}}},
			setUp: func(_, workSpace string) {
				So(os.MkdirAll(filepath.Join(workSpace, "realcache"), os.ModePerm), ShouldBeNil)
				probeSymlink("realcache", filepath.Join(workSpace, "clink"))
			},
			live: []string{"realcache/bucket/sub", createdCwdName + "/" + mount},
			kept: []string{".", createdCwdName, "realcache", "clink"},
			gone: []string{output, createdTmpName, scratch},
		},
	}
}

func TestProbeMountAndCacheShapes(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a Job whose mounts and caches land in the dirs wr reclaims", t, func() {
		for _, row := range probeMountRows() {
			Convey(row.name, func() {
				cwd := t.TempDir()
				precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

				job := &Job{Cwd: cwd, Cmd: probeCmd, MountConfigs: MountConfigs{row.mc}}
				actualCwd, workSpace, tmpDir := realWorkSpace(job)

				writeFileIn(actualCwd, "own_output.txt")
				writeFileIn(tmpDir, "scratch.txt")

				if row.setUp != nil {
					row.setUp(actualCwd, workSpace)
				}

				planted := probePlant(workSpace, row.live)

				snap := job.workSpaceSnapshot()
				cleanErr := snap.cleanupWorkSpace()
				tmpErr := snap.removeTmpDir()

				soPathsExist(planted...)
				soPathsExist(probePaths(workSpace, row.kept)...)
				soPathsExist(precious, cwd)
				soPathsGone(probePaths(workSpace, row.gone)...)

				So(cleanErr, ShouldBeNil)
				So(tmpErr, ShouldBeNil)
			})
		}
	})
}

// probePlant creates each of the given dirs, spelled relative to base, with a
// file in it that must survive, and returns those files' paths.
func probePlant(base string, rels []string) []string {
	planted := make([]string, 0, len(rels))

	for _, rel := range rels {
		planted = append(planted, writeFileIn(filepath.Join(base, rel), probeRemoteName))
	}

	return planted
}

// probePaths turns paths spelled relative to base into absolute ones.
func probePaths(base string, rels []string) []string {
	paths := make([]string, 0, len(rels))

	for _, rel := range rels {
		paths = append(paths, filepath.Join(base, rel))
	}

	return paths
}

// probe counts for the concurrency probes below. They are the smallest that
// still fail when the upward walk stops removing one empty dir at a time.
const (
	probeCleaners = 8
	probeRounds   = 5
)

// probeRun is one run of a Job: the working directory mkHashedDir made for it,
// and the output file only that run may delete.
type probeRun struct {
	actualCwd string
	tmpDir    string
	output    string
}

// startProbeRun makes a run of the Job with the given key below cwd, exactly as
// Client.Execute does, and puts a live output file in its working directory.
func startProbeRun(cwd, key, owner string) (probeRun, error) {
	actualCwd, tmpDir, err := mkHashedDir(cwd, key)
	if err != nil {
		return probeRun{}, err
	}

	output := filepath.Join(actualCwd, "output_"+owner+".txt")

	return probeRun{actualCwd: actualCwd, tmpDir: tmpDir, output: output},
		os.WriteFile(output, []byte("live output of "+owner), 0o600)
}

// cleanUp asks the real cleanup to sweep this run, the way a Job carrying the
// same Cmd and Cwd would.
func (r probeRun) cleanUp(cwd, cmd string) error {
	job := &Job{Cmd: cmd, Cwd: cwd, ActualCwd: r.actualCwd}

	return job.workSpaceSnapshot().cleanupWorkSpace()
}

func TestProbeConcurrentRunsOfOneCwd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// every run of every Job of one Cwd works below the same <AppName>_cwd base,
	// and the runs of one KEY share every hashed level above their own workspace,
	// so a cleanup's upward walk of empty parents is aimed straight at the
	// directories another live run is sitting in.
	Convey("Given a Cwd two Jobs are running in", t, func() {
		cwd := t.TempDir()
		precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

		victim := &Job{Cwd: cwd, Cmd: probeCmd + " victim"}
		victimCwd, victimWorkSpace, victimTmp := realWorkSpace(victim)
		results := writeFileIn(victimCwd, "results.txt")

		cleaner := &Job{Cwd: cwd, Cmd: probeCmd + " cleaner"}
		realWorkSpace(cleaner)

		Convey("many cleanups of one at once delete nothing of the other's", func() {
			var wg sync.WaitGroup

			for range probeCleaners {
				wg.Go(func() {
					//nolint:errcheck // one cleanup racing another may fail; what survived is the assertion
					cleaner.workSpaceSnapshot().cleanupWorkSpace()
				})
			}

			wg.Wait()

			soPathsExist(results, victimCwd, victimTmp, victimWorkSpace, precious, cwd)
		})
	})

	Convey("Given a live run of a Job, and further runs of the SAME key beside it", t, func() {
		// two managers over one filesystem run what is the same Job by key - Cwd
		// is not part of a non-cwd_matters key, and nothing else about a run is
		// either - so their workspaces differ only in the digits os.MkdirTemp
		// chose, below hashed levels they share.
		cwd := t.TempDir()
		precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

		cmd := probeCmd + " shared"
		key := (&Job{Cmd: cmd, Cwd: cwd}).Key()

		live, err := startProbeRun(cwd, key, "live")
		So(err, ShouldBeNil)

		Convey("the live run survives each of them being made and cleaned up", func() {
			for range probeRounds {
				short, serr := startProbeRun(cwd, key, "short")
				So(serr, ShouldBeNil)
				So(short.cleanUp(cwd, cmd), ShouldBeNil)
			}

			soPathsExist(live.output, live.actualCwd, live.tmpDir, precious, cwd)
		})
	})
}

// probeModRow is one `wr mod` shape applied to the Job wr v0.37.0|1 persisted:
// CwdMatters with ActualCwd poisoned to Cwd, still carrying the cleanup
// Behaviour that version stored.
//
// The modification is the one route by which such a Job can end up running in a
// directory wr made while still reporting the user's own Cwd as its working
// directory, which is the state the file loss came from.
type probeModRow struct {
	name string
	mod  func(jm *JobModifier)

	// wantPoison is whether the modification leaves the poisoned ActualCwd in
	// place, and wantCleanup whether it leaves a cleanup Behaviour able to act
	// on it.
	wantPoison  bool
	wantCleanup bool

	// wantErr is what the cleanup the modification permits must refuse with, or
	// nil where it leaves no cleanup to run.
	wantErr error
}

func probeModRows() []probeModRow {
	return []probeModRow{
		{
			// this is the one modification that would leave the Job running in a
			// directory wr makes while still reporting the user's own Cwd as the
			// one it ran in - the state the file loss came from - and it does not:
			// Cwd is part of a CwdMatters Job's key and of no other's, so
			// unsetting CwdMatters changes the key, and a stored working directory
			// the current definition cannot build is discarded. The cleanup this
			// modification does permit is then left with nothing to act on.
			//
			// (What refuses the state itself, should it be reached some other way,
			// is pinned by behaviours_test.go's ActualCwd-equal-to-Cwd cases.)
			name:        "--unset_cwd_matters, which clears the poison because the key changes",
			mod:         func(jm *JobModifier) { jm.SetCwdMatters(false) },
			wantCleanup: true,
		},
		{
			// the Job stays cwd_matters, so it keeps the poison and loses the
			// cleanup: a cleanup can only delete a directory wr made, and a
			// cwd_matters Job has none.
			name:       "--priority, which leaves the poison but drops the cleanup",
			mod:        func(jm *JobModifier) { jm.SetPriority(5) },
			wantPoison: true,
		},
	}
}

func TestProbeV037PoisonedJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given the job wr v0.37.0|1 persisted, in a Cwd of the user's own", t, func() {
		// the Cwd is spelled the way a workspace of wr's is - a *_cwd base, the
		// created depth, a leaf called cwd - so that nothing about its SHAPE is
		// what refuses the deletion. This is the incident's own arrangement: the
		// user's scripts sat above a directory named that way.
		base := t.TempDir()
		scripts := filepath.Join(base, "scripts_ciseqtl")
		precious := writeFileIn(scripts, "05_RunCisEQTL.R")

		cwd := filepath.Join(scripts, AppName+createdCwdBaseSuffix, "a", "b", "c", "abc123", createdCwdName)
		So(os.MkdirAll(cwd, os.ModePerm), ShouldBeNil)

		inputs := writeFileIn(filepath.Join(cwd, "inputs"), "counts.tsv")

		for _, row := range probeModRows() {
			Convey(row.name, func() {
				job := &Job{
					Cmd: probeCmd, Cwd: cwd, CwdMatters: true, ActualCwd: cwd,
					Behaviours: Behaviours{{When: OnExit, Do: CleanupAll}},
				}

				jm := NewJobModifer()
				row.mod(jm)
				jm.applyTo(job)

				err := job.Behaviours.Trigger(false, job)

				soPathsExist(precious, inputs, cwd, scripts)

				So(job.ActualCwd == cwd, ShouldEqual, row.wantPoison)
				So(strings.Contains(job.Behaviours.String(), "cleanup"), ShouldEqual, row.wantCleanup)

				soProbeRefused(err, row.wantErr)
			})
		}
	})
}
