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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	gofuse "github.com/hanwen/go-fuse/v2/fs"
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

	// probeBareLeaf and probeOtherLeaf name the two decoy dirs of probeWorld.
	probeBareLeaf  = "bare prefix"
	probeOtherLeaf = "user's own"

	// probeLinkName is the name of a symlink a mount config spells a mount point
	// or a cache location through, and probeRealDir the dir it leads to.
	probeLinkName = "link"
	probeRealDir  = "real"

	// probeArabicDigit is the digit three written in Arabic-Indic numerals: a
	// character unicode calls a digit and os.MkdirTemp never writes.
	probeArabicDigit = "\u0663"

	// probeRelativeDir is a relative path, which a Job's directories may not be.
	probeRelativeDir = "relative/cwd"
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
	// named the two ways the unique name wr minted can be mis-recognised: the
	// bare prefix wr would have used had it not needed a unique dir, ie. that
	// prefix with an EMPTY suffix, and a name of the user's own at the same
	// depth. Whoever submits the Job picks the Cmd that Key() hashes, so they
	// can put a directory at either.
	decoys map[string]string

	// output is this run's own file, which cleanup IS entitled to delete.
	output string

	// sibling is the working directory mkHashedDir made for ANOTHER run of the
	// same Job, so it differs from this run's only in the unique suffix wr chose
	// for it, and siblingFile is its live output.
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

	// the prefix comes from calculateHashedDir, the helper both mkHashedDir and
	// the guard ask, so the bare-prefix decoy is the prefix with an EMPTY suffix
	// however wr spells the suffix it adds. Deriving it from the workspace name
	// instead cannot: the prefix is hex, so trimming the suffix off by character
	// class eats into it.
	hashed, bare := calculateHashedDir(w.base, job.Key())
	So(hashed, ShouldEqual, w.hashed)

	w.decoys = map[string]string{
		probeBareLeaf:  bare,
		probeOtherLeaf: "zzzz1234",
	}

	// the real workspace is that same prefix plus the unique suffix wr chose, so
	// the two names differ in the suffix ALONE - which is what makes the row
	// fed by this decoy a test of the empty suffix rather than of a name that
	// merely resembles the prefix.
	So(filepath.Base(w.workSpace), ShouldStartWith, w.decoys[probeBareLeaf])
	So(filepath.Base(w.workSpace), ShouldNotEqual, w.decoys[probeBareLeaf])

	for _, name := range w.decoys {
		w.mine = append(w.mine, writeFileIn(filepath.Join(w.hashed, name, createdCwdName), probeMineName))
	}

	for _, dir := range []string{
		w.root, w.cwd, filepath.Join(w.cwd, "my_scripts"),
		w.base, filepath.Join(w.base, "not_a_hash"),
		w.hashed, filepath.Join(w.hashed, "someone_elses"),
	} {
		w.mine = append(w.mine, writeFileIn(dir, probeMineName))
	}

	// the sibling is minted by asking production for a second workspace of the
	// SAME key, which is exactly what a second run does, rather than by building
	// a name of the shape one is expected to have.
	sibling, _, err := mkHashedDir(job.Cwd, job.Key())
	So(err, ShouldBeNil)
	So(sibling, ShouldNotEqual, w.actualCwd)

	w.sibling = sibling
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
		{
			name:      "that path with a doubled separator in it",
			actualCwd: func(w probeWorld) string { return w.workSpace + "//" + createdCwdName },
			sweptOwn:  true,
		},
		{
			name: "that path spelled through a component of its own and ..",
			actualCwd: func(w probeWorld) string {
				return filepath.Join(w.workSpace, "x", "..", createdCwdName)
			},
			sweptOwn: true,
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
			// the tmp dir wr made beside the working directory sits at the right
			// depth below the same workspace, so only its NAME tells the two
			// apart - and treating it as a working directory would hand the
			// workspace to the sweep from a Job that reported the wrong sister.
			name:      "the tmp dir wr made beside it",
			actualCwd: func(w probeWorld) string { return filepath.Join(w.workSpace, createdTmpName) },
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
		{
			// the digits os.MkdirTemp appends are ASCII, and the check spells
			// that out as a range rather than asking unicode: a name whose
			// suffix is a digit in another script is a name the user chose.
			name: "the same leaf with a digit of another script appended",
			actualCwd: func(w probeWorld) string {
				return filepath.Join(w.hashed, filepath.Base(w.workSpace)+probeArabicDigit, createdCwdName)
			},
			wantErr: errNotACreatedCwd,
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
		{
			// Cwd is stored exactly as it was typed, because it feeds Job.Key(),
			// so an unclean spelling of the right directory is one wr is GIVEN
			// and must reach the control's verdict.
			name:      "Cwd with a trailing separator",
			cwd:       func(w probeWorld) string { return w.cwd + "/" },
			actualCwd: own,
			sweptOwn:  true,
		},
		{
			// this code runs in the runner AND in the manager, which have
			// different working directories, so a relative Cwd names a
			// different tree in each. Refusing it costs a leaked workspace.
			name:      "Cwd given as a relative path",
			cwd:       func(_ probeWorld) string { return probeRelativeDir },
			actualCwd: own,
			wantErr:   errNotBelowBaseDir,
		},
		{
			name: "ActualCwd given as a relative path",
			actualCwd: func(w probeWorld) string {
				rel, err := filepath.Rel(w.cwd, w.actualCwd)
				So(err, ShouldBeNil)

				return rel
			},
			wantErr: errNotBelowBaseDir,
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
		realCwd := filepath.Join(base, "real")
		So(os.MkdirAll(realCwd, os.ModePerm), ShouldBeNil)

		link := filepath.Join(base, "cwd_link")
		So(os.Symlink(realCwd, link), ShouldBeNil)

		precious := writeFileIn(filepath.Join(base, "user_scripts"), probeMineName)

		job := &Job{Cmd: probeCmd, Cwd: link}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)
		output := writeFileIn(actualCwd, "own_output.txt")

		err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job)

		soPathsExist(precious, realCwd, link)
		soPathsGone(output, actualCwd, tmpDir, workSpace)

		So(err, ShouldBeNil)
	})

	Convey("A Job refuses the live workspace of another whose key shares its hashed levels", t, func() {
		// the hashed levels are the first three characters of the key, so two
		// Jobs whose keys agree there share EVERY directory above their own
		// workspace: the *_cwd base and all mkHashedLevels-1 of them. Only the
		// MkdirTemp leaf differs, and that leaf is named for the REST of the
		// key - so this is the closest two different Jobs of one Cwd can get,
		// and the one place the leaf check is the only thing left standing.
		//
		// Three characters is a 4096-way space that whoever submits a Job can
		// grind a Cmd through, which is why the check does not stop there.
		cwd := t.TempDir()
		precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

		victim := &Job{Cwd: cwd, Cmd: probeCmd + " victim"}

		cleaner := probeJobSharingHashedLevels(cwd, victim)
		So(cleaner, ShouldNotBeNil)

		victimCwd, victimWorkSpace, victimTmp := realWorkSpace(victim)
		results := writeFileIn(victimCwd, "results.txt")

		cleanerCwd, _, _ := realWorkSpace(cleaner)
		So(filepath.Dir(filepath.Dir(cleanerCwd)), ShouldEqual, filepath.Dir(victimWorkSpace))

		cleaner.ActualCwd = victimCwd

		err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, cleaner)

		soPathsExist(precious, cwd)
		soPathsExist(results, victimCwd, victimTmp, victimWorkSpace)

		soProbeRefused(err, errNotACreatedCwd)
	})
}

// probeJobSharingHashedLevels finds a Job of the given Cwd whose key starts with
// the same characters as other's - so mkHashedDir puts its workspace below the
// very same hashed levels - but which is not the same Job.
//
// The Cmd is ground out rather than written down because the key is a hash of
// it: a literal would stop sharing the levels the moment anything else about
// what Key() reads changed.
func probeJobSharingHashedLevels(cwd string, other *Job) *Job {
	const probeGrindLimit = 1 << 20

	prefix := other.Key()[:mkHashedLevels-1]

	for i := range probeGrindLimit {
		job := &Job{Cwd: cwd, Cmd: probeCmd + " sharer " + strconv.Itoa(i)}
		if job.Key()[:mkHashedLevels-1] == prefix && job.Key() != other.Key() {
			return job
		}
	}

	return nil
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

// probeCleanupActionRow is one of the two BehaviourActions that sweep a Job's
// workspace. Behaviour.trigger has an arm of its own for each, and the doc on
// CleanupAll reserves it the reach Cleanup gives up once output files can be
// designated - so the two arms are one place a future divergence would land,
// and the poisoned database records of the original file loss carried
// cleanup_all.
//
// Each row asks the same two questions of one action, so an arm that stopped
// going through the workspace licence shows up as the answer changing for one
// action and not the other.
type probeCleanupActionRow struct {
	name string
	do   BehaviourAction
}

func probeCleanupActionRows() []probeCleanupActionRow {
	return []probeCleanupActionRow{
		{name: "cleanup", do: Cleanup},
		{name: "cleanup_all", do: CleanupAll},
	}
}

func TestProbeCleanupActionsAgree(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given the workspace wr really made and the user's files all around it", t, func() {
		for _, row := range probeCleanupActionRows() {
			Convey("an on_exit "+row.name, func() {
				job := &Job{Cmd: probeCmd, Behaviours: Behaviours{{When: OnExit, Do: row.do}}}
				w := seedProbeWorld(t, job)

				Convey("sweeps this run's own workspace and nothing else", func() {
					err := job.Behaviours.Trigger(false, job)

					soPathsExist(w.mine...)
					soPathsExist(w.cwd, w.siblingFile, w.sibling)
					soPathsGone(w.output, w.actualCwd, w.tmpDir)

					So(err, ShouldBeNil)
				})

				Convey("refuses a dir of the user's at the workspace leaf's bare prefix", func() {
					job.ActualCwd = w.decoy(probeBareLeaf)

					err := job.Behaviours.Trigger(false, job)

					soPathsExist(w.mine...)
					soPathsExist(w.cwd, w.output, w.actualCwd, w.tmpDir)

					soProbeRefused(err, errNotACreatedCwd)
				})
			})
		}
	})

	Convey("A hard link into the workspace costs the user a name, not their file", t, func() {
		// every deletion in the sweep is an unlinkat of one name, so a second
		// name for a file of the user's - which their own Cmd can make, and
		// which no check here could tell from an ordinary output file - loses
		// that name and leaves the file. There is no route by which the sweep
		// truncates or empties what it unlinks.
		cwd := t.TempDir()
		precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

		job := &Job{Cwd: cwd, Cmd: probeCmd, Behaviours: Behaviours{{When: OnExit, Do: CleanupAll}}}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)

		for _, dir := range []string{actualCwd, workSpace, tmpDir} {
			So(os.Link(precious, filepath.Join(dir, "another_name.txt")), ShouldBeNil)
		}

		err := job.Behaviours.Trigger(false, job)

		soPathsExist(precious, cwd)
		soPathsGone(workSpace)

		So(err, ShouldBeNil)
	})
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
	return []MountTarget{{Path: testWSTargetPath, Cache: true, Write: true}}
}

// probeSymlink replaces link with a symlink to target.
func probeSymlink(target, link string) {
	So(os.Symlink(target, link), ShouldBeNil)
}

func probeMountRows() []probeMountRow {
	const (
		output   = createdCwdName + "/own_output.txt"
		scratch  = createdTmpName + "/scratch.txt"
		mountDir = testWSMount
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
				Mount: mountDir, CacheBase: "../" + createdTmpName, Targets: probeCachedTargets(),
			},
			live: []string{createdCwdName + "/" + mountDir, createdTmpName + "/" + probeMuxfysDir},
			kept: []string{".", createdCwdName, createdTmpName, scratch},
			gone: []string{output},
		},
		{
			name: "an explicit CacheDir inside the job's TMPDIR",
			mc: MountConfig{Mount: mountDir, Targets: []MountTarget{{
				Path: testWSTargetPath, CacheDir: createdTmpName + "/c", Write: true,
			}}},
			live: []string{createdCwdName + "/" + mountDir, createdTmpName + "/c"},
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
				Path: testWSTargetPath, CacheDir: "..", Write: true,
			}}},
			live: []string{"../up", "../" + testWSTargetPath},
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
		{
			// a mount point ABOVE the workspace puts the workspace inside the
			// live mount just as surely, and the rule is about being at or above
			// rather than about being the workspace itself: everything wr made
			// for the Job is then the user's remote data, read through a mount
			// Unmount has not got to yet.
			name: "a mount point at the hashed level above the workspace",
			mc:   MountConfig{Mount: "../..", Targets: probeCachedTargets()},
			live: []string{".", createdCwdName, createdTmpName},
			kept: []string{".", createdCwdName, createdTmpName, output, scratch},
		},

		// a symlink INSIDE the tree being swept makes the two spellings of a
		// mount or cache location agree LEXICALLY while naming different entries
		// of it, so only the resolved spelling names the directory the data is
		// physically in - the one a sweep would recurse into.
		{
			name: "a muxfys CacheBase spelled through a symlink inside the working dir",
			mc:   MountConfig{Mount: mountDir, CacheBase: probeLinkName, Targets: probeCachedTargets()},
			setUp: func(actualCwd, _ string) {
				So(os.MkdirAll(filepath.Join(actualCwd, probeRealDir), os.ModePerm), ShouldBeNil)
				probeSymlink(probeRealDir, filepath.Join(actualCwd, probeLinkName))
			},
			live: []string{
				createdCwdName + "/" + probeRealDir + "/" + probeMuxfysDir, createdCwdName + "/" + mountDir,
			},
			kept: []string{".", createdCwdName, createdCwdName + "/" + probeRealDir},
			// the symlink itself is not a dir, so the sweep unlinks it: what had
			// to be recognised is the dir it led to, which is where the cache is.
			gone: []string{output, createdCwdName + "/" + probeLinkName, createdTmpName, scratch},
		},
		{
			name: "an explicit CacheDir spelled through a symlink inside the workspace",
			mc: MountConfig{Mount: mountDir, Targets: []MountTarget{{
				Path: testWSTargetPath, CacheDir: probeLinkName, Write: true,
			}}},
			setUp: func(_, workSpace string) {
				So(os.MkdirAll(filepath.Join(workSpace, probeRealDir), os.ModePerm), ShouldBeNil)
				probeSymlink(probeRealDir, filepath.Join(workSpace, probeLinkName))
			},
			live: []string{probeRealDir + "/" + testWSTargetPath, createdCwdName + "/" + mountDir},
			kept: []string{".", createdCwdName, probeRealDir, probeLinkName},
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

// probeFuseRow is one MountConfig shape whose mount point holds a REAL FUSE
// mount of the user's remote data, rather than a plain directory standing in for
// one.
//
// A plain directory cannot show what the keep set is FOR. Every deletion below
// is made through an os.Root, which resolves each path beneath the directory it
// holds - but RESOLVE_BENEATH does not imply RESOLVE_NO_XDEV, so an
// os.Root.RemoveAll of an entry that happens to be a live mount crosses into it
// and unlinks what is behind. For a muxfys mount that is the user's objects in
// their object store, and for a writable one their job's own results before
// Unmount ever uploaded them. Nothing about the workspace's shape stops it; the
// keep set is the only thing that does.
type probeFuseRow struct {
	name string
	mc   MountConfig

	// setUp arranges whatever the shape needs on disk beyond the mount itself,
	// as probeMountRow's does and for the same reason.
	setUp func(actualCwd, workSpace string)

	// mount is where the remote is really mounted, own are the files this run
	// left OUTSIDE that mount, and kept and gone are the paths cleanup and the
	// TMPDIR removal must between them have left and deleted. All of them are
	// spelled relative to the WORKSPACE and written out literally, as
	// probeMountRow spells its own.
	//
	// The own files are written after the mount is raised, and nothing wr made
	// under a mount point is named at all, because a mount point has to be empty
	// to mount over: in a real run the working directory and the TMPDIR are made
	// empty and the mounts go up before the Job's Cmd writes anything, so what a
	// run leaves inside one of its own mounts is remote data, not local.
	mount string
	own   []string
	kept  []string
	gone  []string
}

func probeFuseRows() []probeFuseRow {
	const (
		output   = createdCwdName + "/own_output.txt"
		scratch  = createdTmpName + "/scratch.txt"
		cwdMount = createdCwdName + "/" + testWSMount
		realDir  = createdCwdName + "/" + probeRealDir
	)

	return []probeFuseRow{
		// one row per rule of the keep set that can be the only thing between
		// the sweep and a live mount, each over a directory wr itself made.
		{
			// keptDirs.inActualCwd, honoured by removeAllExcept.
			name:  "a live mount inside the working directory",
			mc:    MountConfig{Mount: testWSMount, Targets: probeCachedTargets()},
			mount: cwdMount,
			own:   []string{output, scratch},
			kept:  []string{".", createdCwdName, cwdMount},
			gone:  []string{output, createdTmpName, scratch},
		},
		{
			// keptDirs.wholeActualCwd: the working directory IS the mount, so
			// everything in it is the user's remote data and none of it may go.
			name:  "a live mount that IS the working directory",
			mc:    MountConfig{Mount: ".", Targets: probeCachedTargets()},
			mount: createdCwdName,
			own:   []string{scratch},
			kept:  []string{".", createdCwdName},
			gone:  []string{createdTmpName, scratch},
		},
		{
			// keptDirs.workSpaceEntries, honoured by BOTH sweeps: the tmp dir is
			// reclaimed on every exit, cleanup Behaviour or none, so a mount put
			// there must survive a deletion the workspace sweep does not make.
			name:  "a live mount that IS the job's TMPDIR",
			mc:    MountConfig{Mount: "../" + createdTmpName, Targets: probeCachedTargets()},
			mount: createdTmpName,
			own:   []string{output},
			kept:  []string{".", createdTmpName},
			gone:  []string{createdCwdName, output},
		},
		{
			// relsBelowDirResolved: only the RESOLVED spelling names the
			// directory the mount is physically in, and a keep set given the
			// lexical one alone would protect the symlink and delete the mount.
			name: "a live mount spelled through a symlink inside the working directory",
			mc:   MountConfig{Mount: probeLinkName + "/" + testWSMount, Targets: probeCachedTargets()},
			setUp: func(actualCwd, _ string) {
				So(os.MkdirAll(filepath.Join(actualCwd, probeRealDir), os.ModePerm), ShouldBeNil)
				probeSymlink(probeRealDir, filepath.Join(actualCwd, probeLinkName))
			},
			mount: realDir + "/" + testWSMount,
			own:   []string{output, scratch},
			kept:  []string{".", createdCwdName, realDir, realDir + "/" + testWSMount},
			// the symlink itself is not a dir, so the sweep unlinks it: what had
			// to be recognised is the dir it led to, which is where the mount is.
			gone: []string{output, createdCwdName + "/" + probeLinkName, createdTmpName, scratch},
		},
		{
			// removeAllExcept's keep-before-check ordering. A dir that must
			// survive AND is on the way to a deeper dir that must survive is both
			// an exception and a dir to recurse into, and only keeping it whole is
			// safe: recursing into a live mount to reach something deeper reads it
			// through the mount and deletes every entry of the user's remote data
			// that is not the deeper thing itself.
			name: "a live mount inside the working directory with a cache dir configured inside it",
			mc: MountConfig{Mount: testWSMount, Targets: []MountTarget{{
				Path: testWSTargetPath, CacheDir: createdCwdName + "/" + testWSMount + "/c", Write: true,
			}}},
			mount: cwdMount,
			own:   []string{output, scratch},
			kept:  []string{".", createdCwdName, cwdMount},
			gone:  []string{output, createdTmpName, scratch},
		},
		{
			// keptDirs.wholeWorkSpace, honoured by BOTH sweeps. A CacheDir that IS
			// the workspace makes the workspace root where the live mount's output
			// waits for Unmount to upload it, under names only the remote knows, so
			// nothing wr made there is wr's to delete: not the job's own results,
			// and not the TMPDIR the runner reclaims on every exit.
			//
			// A mount point at the workspace cannot reach this rule the same way: a
			// real mount needs an empty mount point, and mkCwdAndTmp has already
			// filled the workspace by the time a run mounts.
			name: "a live mount whose cache dir IS the workspace, so nothing there may go",
			mc: MountConfig{Mount: testWSMount, Targets: []MountTarget{{
				Path: testWSTargetPath, CacheDir: ".", Write: true,
			}}},
			mount: cwdMount,
			own:   []string{output, scratch},
			kept: []string{
				".", createdCwdName, createdTmpName, cwdMount, output, scratch,
			},
		},
	}
}

func TestProbeLiveFuseMounts(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a Job whose mount point holds a live mount of the user's remote data", t, func() {
		if !probeCanMountFuse() {
			SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

			return
		}

		for _, row := range probeFuseRows() {
			Convey(row.name, func() {
				cwd := t.TempDir()
				precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

				job := &Job{Cwd: cwd, Cmd: probeCmd, MountConfigs: MountConfigs{row.mc}}
				actualCwd, workSpace, _ := realWorkSpace(job)

				if row.setUp != nil {
					row.setUp(actualCwd, workSpace)
				}

				remote := probeMountRemote(t, filepath.Join(workSpace, row.mount))
				probeOwnFiles(workSpace, row.own)

				snap := job.workSpaceSnapshot()
				cleanErr := snap.cleanupWorkSpace()
				tmpErr := snap.removeTmpDir()

				soPathsExist(remote)
				soPathsExist(filepath.Join(workSpace, row.mount, probeRemoteName))
				soPathsExist(probePaths(workSpace, row.kept)...)
				soPathsExist(precious, cwd)
				soPathsGone(probePaths(workSpace, row.gone)...)

				So(cleanErr, ShouldBeNil)
				So(tmpErr, ShouldBeNil)
			})
		}

		Convey("the manager's own route keeps the mount of the run it pinned", func() {
			// the manager reaches the same deletion code as the runner, but
			// through a PIN taken when it declared the job lost, and by the time
			// that pin triggers the very same *Job can be a live retry, with the
			// retry's own working directory written onto it by a Touch. So both
			// halves of what the sweep may do are named by the pin: the workspace
			// it deletes, and the live mount of the user's remote data inside that
			// workspace which it must not delete through.
			cwd := t.TempDir()
			precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

			job := &Job{
				Cwd: cwd, Cmd: probeCmd,
				MountConfigs: MountConfigs{{Mount: testWSMount, Targets: probeCachedTargets()}},
				Behaviours:   Behaviours{{When: OnExit, Do: CleanupAll}},
			}
			lostCwd, _, lostTmp := realWorkSpace(job)

			remote := probeMountRemote(t, filepath.Join(lostCwd, testWSMount))

			pin := job.pinBehaviours()

			abandoned := writeFileIn(lostCwd, "abandoned.txt")

			retry, err := startProbeRun(cwd, job.Key(), "retry")
			So(err, ShouldBeNil)

			job.Lock()
			job.ActualCwd = retry.actualCwd
			job.Unlock()

			err = pin.trigger()

			soPathsExist(remote)
			soPathsExist(filepath.Join(lostCwd, testWSMount, probeRemoteName))
			soPathsExist(retry.output, retry.actualCwd, retry.tmpDir)
			soPathsExist(precious, cwd)
			soPathsGone(abandoned, lostTmp)

			So(err, ShouldBeNil)
		})

		Convey("a run handed a workspace name an earlier run finished with keeps its mount", func() {
			// os.MkdirTemp's digits are not pinned to a run, so the name one
			// manager's run finished with is free for another manager's run of the
			// SAME key to be handed later, and a stale ActualCwd then names a live
			// workspace byte for byte. Nothing about the path can tell the two
			// apart - the residual the "another run of the same job" row records -
			// but the two are one job by key, so they mount the same remote at the
			// same place, and the stale run's own keep set still names it. The
			// local workspace goes; the user's remote data does not.
			cwd := t.TempDir()
			precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

			job := &Job{
				Cwd: cwd, Cmd: probeCmd,
				MountConfigs: MountConfigs{{Mount: testWSMount, Targets: probeCachedTargets()}},
			}
			finished, workSpace, _ := realWorkSpace(job)

			So(os.RemoveAll(workSpace), ShouldBeNil)
			So(os.MkdirAll(workSpace, os.ModePerm), ShouldBeNil)

			reusedCwd, reusedTmp, err := mkCwdAndTmp(workSpace)
			So(err, ShouldBeNil)
			So(reusedCwd, ShouldEqual, finished)

			remote := probeMountRemote(t, filepath.Join(reusedCwd, testWSMount))
			live := writeFileIn(reusedCwd, "live_output.txt")

			snap := job.workSpaceSnapshot()
			cleanErr := snap.cleanupWorkSpace()
			tmpErr := snap.removeTmpDir()

			soPathsExist(remote)
			soPathsExist(filepath.Join(reusedCwd, testWSMount, probeRemoteName))
			soPathsExist(precious, cwd, workSpace, reusedCwd)
			soPathsGone(live, reusedTmp)

			So(cleanErr, ShouldBeNil)
			So(tmpErr, ShouldBeNil)
		})
	})
}

// probeOwnFiles writes the files a run left outside its own mounts, each spelled
// relative to base, as probeFuseRow.own spells them.
func probeOwnFiles(base string, rels []string) {
	for _, rel := range rels {
		path := filepath.Join(base, rel)
		writeFileIn(filepath.Dir(path), filepath.Base(path))
	}
}

// probeCanMountFuse reports whether this host lets an unprivileged process raise
// a FUSE mount, which the rows above need and a host with no /dev/fuse or no
// setuid fusermount cannot give them.
func probeCanMountFuse() bool {
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

// probeMountRemote plants a file of the user's remote data in a directory
// OUTSIDE the Job's Cwd and really FUSE mounts that directory over mountPoint,
// returning the planted file's path. The mount is taken down when the test ends.
//
// go-fuse's loopback filesystem is what makes the mount real, and it is the
// cheapest one an unprivileged process can raise. What is behind it is an
// ordinary directory, so what survived can be asserted about directly; muxfys
// would need an object store to talk to, and the deletion under test does not
// care which filesystem is mounted, only that a mount gets crossed.
func probeMountRemote(t *testing.T, mountPoint string) string {
	t.Helper()

	backing := t.TempDir()
	remote := writeFileIn(backing, probeRemoteName)

	root, err := gofuse.NewLoopbackRoot(backing)
	So(err, ShouldBeNil)
	So(os.MkdirAll(mountPoint, os.ModePerm), ShouldBeNil)

	server, err := gofuse.Mount(mountPoint, root, &gofuse.Options{})
	So(err, ShouldBeNil)

	t.Cleanup(func() {
		if uerr := server.Unmount(); uerr != nil {
			t.Logf("could not unmount the probe's remote at %s: %s", mountPoint, uerr)
		}
	})

	return remote
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

// probeWindowRow is one thing that can happen to the directories a cleanup has
// already proven, in the window between the proof and the sweep, when ANOTHER of
// the user's own jobs is working below the same Cwd - the normal condition at
// scale.
//
// Every level above the workspace is shared with every other run of the same
// key, and the workspace's own name is unique only among the workspaces that
// EXIST: os.MkdirTemp hands the name of a workspace that has gone to the next
// run that asks. So each row leaves a path that still names something, just not
// the thing that was proven, and only the inodes the descent lstat'ed - or the
// upward walk's refusal to remove anything but an empty directory - tell the two
// apart.
type probeWindowRow struct {
	name string

	// inWindow is called between the proof and the sweep, with the Job whose
	// cleanup is running, its own workspace and the deepest hashed level above
	// it, and returns the paths that must ALL still be there afterwards.
	inWindow func(job *Job, workSpace, hashed string) []string

	// wantErr is the error class the refusal must carry, or nil where the row
	// leaves the cleanup something it may legitimately finish.
	wantErr error
}

func probeWindowRows() []probeWindowRow {
	return []probeWindowRow{
		// the workspace itself, replaced. What was proven is gone, and the
		// deletion is aimed at whatever holds its name now.
		{
			// this is the name-reuse residual, arriving one moment too late to
			// be one: a stale ActualCwd naming a workspace since handed out
			// again is indistinguishable by path (the "another run of the same
			// job" row), but a workspace this cleanup PROVED and then lost is
			// told apart by the inode of the dir that was checked - PROVIDED
			// the replacement got an inode of its own, which is why the other
			// run is minted while this workspace still holds its own, and moved
			// onto its name only afterwards. Freeing the name first would not
			// guarantee it: ext4 hands a freed directory inode straight back to
			// the next mkdir, and a replacement given the proven inode
			// satisfies proveSameDir, so the sweep proceeds and takes the live
			// run's output with it. That case is a recorded residual, not
			// something this row can hold production to - see "Measured
			// residuals" in .docs/cwd_matters_cleanup/readme.md, "os.SameFile
			// is not an identity check across delete and recreate".
			name: "another run of the same key is handed this workspace's name",
			inWindow: func(job *Job, workSpace, _ string) []string {
				checked, err := os.Lstat(workSpace)
				So(err, ShouldBeNil)

				otherCwd, otherTmp, err := mkHashedDir(job.Cwd, job.Key())
				So(err, ShouldBeNil)

				So(os.RemoveAll(workSpace), ShouldBeNil)
				So(os.Rename(filepath.Dir(otherCwd), workSpace), ShouldBeNil)

				now, err := os.Lstat(workSpace)
				So(err, ShouldBeNil)
				So(os.SameFile(now, checked), ShouldBeFalse)

				reusedCwd := filepath.Join(workSpace, filepath.Base(otherCwd))
				reusedTmp := filepath.Join(workSpace, filepath.Base(otherTmp))

				return []string{
					writeFileIn(reusedCwd, "live_output.txt"), reusedCwd, reusedTmp, workSpace,
				}
			},
			wantErr: errNotBelowBaseDir,
		},
		{
			// the same window with a directory of the user's in it instead,
			// which the hashed levels being inside their own Cwd puts within
			// reach of anything running as them. Nothing about its SHAPE is
			// what refuses it: it sits at the path mkHashedDir built for this
			// very Job, so the sweep would empty it entirely, keeping nothing.
			name: "a directory of the user's is renamed onto the workspace's name",
			inWindow: func(job *Job, workSpace, _ string) []string {
				userTree := filepath.Join(job.Cwd, "scripts")
				writeFileIn(userTree, "analyse.sh")
				writeFileIn(filepath.Join(userTree, "lib"), "helpers.sh")

				So(os.RemoveAll(workSpace), ShouldBeNil)
				So(os.Rename(userTree, workSpace), ShouldBeNil)

				return []string{
					filepath.Join(workSpace, "analyse.sh"),
					filepath.Join(workSpace, "lib", "helpers.sh"), workSpace,
				}
			},
			wantErr: errNotBelowBaseDir,
		},

		// the hashed level ABOVE the workspace, taken and put back. This is the
		// ordering two runs of one key really produce: one run's cleanup walks
		// up removing the empty levels it shares with every other run, and the
		// next run's mkHashedDir creates them again. The cleanup has nothing
		// left to delete and must say so quietly, since a workspace that has
		// gone is the ordinary state of a second cleanup - but the handles it
		// still holds are on the levels it descended through, not on the ones
		// that replaced them, and the only thing its upward walk may remove is
		// an empty directory.
		{
			name: "the hashed level above the workspace is recreated by another run of the same key",
			inWindow: func(job *Job, _, hashed string) []string {
				So(os.RemoveAll(hashed), ShouldBeNil)

				other, err := startProbeRun(job.Cwd, job.Key(), "other")
				So(err, ShouldBeNil)

				return []string{other.output, other.actualCwd, other.tmpDir, hashed}
			},
		},
		{
			name: "the hashed level above the workspace is recreated holding a tree of the user's",
			inWindow: func(_ *Job, _, hashed string) []string {
				So(os.RemoveAll(hashed), ShouldBeNil)

				return []string{writeFileIn(filepath.Join(hashed, "someone_elses"), probeMineName), hashed}
			},
		},
	}
}

func TestProbeWorkSpaceChangedInTheWindow(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a cleanup that has proven its workspace, and another run of the same key below the Cwd", t, func() {
		for _, row := range probeWindowRows() {
			Convey(row.name, func() {
				cwd := t.TempDir()
				precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

				job := &Job{Cmd: probeCmd, Cwd: cwd, Behaviours: Behaviours{{When: OnExit, Do: CleanupAll}}}
				_, workSpace, _ := realWorkSpace(job)

				var survivors []string

				cleanupProvenHook = func() {
					cleanupProvenHook = nil

					survivors = row.inWindow(job, workSpace, filepath.Dir(workSpace))
				}

				Reset(func() { cleanupProvenHook = nil })

				err := job.Behaviours.Trigger(false, job)

				soPathsExist(survivors...)
				soPathsExist(precious, cwd)

				soProbeRefused(err, row.wantErr)
			})
		}
	})
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

	Convey("Given a workspace name the next run of the same key was handed", t, func() {
		// os.MkdirTemp's digits are unique only among the workspaces that
		// EXIST, so once a run's workspace has gone its name is free for the
		// next run of the same key to be given - and the finished run's OWN
		// ActualCwd, which nobody reported and which its own mkHashedDir
		// returned, then names a LIVE run's working directory byte for byte.
		//
		// Nothing about the path can tell the two apart, since they are one job
		// by key and the key is the whole of what the check recognises. For
		// cleanup that residual is recorded by the "another run of the same
		// job" row. `run` shares the same resolution, so it inherits the same
		// residual - and what it does with the directory is the user's own
		// command, which can do anything they can.
		cwd := t.TempDir()
		precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

		job := &Job{Cmd: probeCmd, Cwd: cwd}
		finished, workSpace, _ := realWorkSpace(job)

		So(os.RemoveAll(workSpace), ShouldBeNil)
		So(os.MkdirAll(workSpace, os.ModePerm), ShouldBeNil)

		reusedCwd, reusedTmp, err := mkCwdAndTmp(workSpace)
		So(err, ShouldBeNil)
		So(reusedCwd, ShouldEqual, finished)

		live := writeFileIn(reusedCwd, "live_output.txt")

		Convey("a run behaviour of the finished run executes in the live run's working directory", func() {
			err = (&Behaviour{When: OnExit, Do: Run, Arg: "rm -rf ./*"}).Trigger(OnExit, job)

			// nothing of the USER's goes, and the deletion stops at the working
			// directory the command was pointed at: it is the live run's own
			// output that this residual costs.
			soPathsExist(precious, cwd, workSpace, reusedCwd, reusedTmp)
			soPathsGone(live)

			So(err, ShouldBeNil)
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

func TestProbeWorkSpaceOnAnotherFilesystem(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a workspace wr built on a filesystem mounted inside the Job's Cwd", t, func() {
		// every deletion is made through an os.Root, and RESOLVE_BENEATH does
		// not imply RESOLVE_NO_XDEV, so nothing in wr asks whether a path
		// crosses a mount boundary. A Cwd with fast scratch mounted somewhere
		// below it is ordinary on the shared filesystems jobs run on, and the
		// hashed levels are exactly where such a mount would be: the sweep then
		// works entirely on the other filesystem, and the upward walk arrives at
		// the mount point itself.
		if !probeCanMountFuse() {
			SkipConvey("this host will not let an unprivileged process raise a FUSE mount", func() {})

			return
		}

		cwd := t.TempDir()
		precious := writeFileIn(filepath.Join(cwd, "user_scripts"), probeMineName)

		job := &Job{Cwd: cwd, Cmd: probeCmd, Behaviours: Behaviours{{When: OnExit, Do: CleanupAll}}}

		// the deepest hashed level is where mkHashedDir will put this Job's
		// workspace, so mounting there puts the whole workspace across the
		// boundary before the workspace exists.
		hashed, _ := calculateHashedDir(filepath.Join(cwd, AppName+createdCwdBaseSuffix), job.Key())
		scratch := probeMountRemote(t, hashed)

		actualCwd, workSpace, tmpDir := realWorkSpace(job)
		output := writeFileIn(actualCwd, "own_output.txt")

		err := job.Behaviours.Trigger(false, job)

		// the file of the user's on the OTHER filesystem is beside the
		// workspace, so only the upward walk could reach it - and that walk
		// removes empty directories, which the mount point is not.
		soPathsExist(scratch, precious, cwd, hashed)
		soPathsGone(output, actualCwd, tmpDir, workSpace)

		So(err, ShouldBeNil)
	})
}
