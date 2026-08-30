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

// This file contains jobWorkSpace: the single point at which a Job's Cwd and
// ActualCwd become an account of what wr created and may therefore delete.
//
// Every previous bug in this area was the same bug. Safety was re-derived from
// the raw j.Cwd / j.ActualCwd strings at eight independent call sites, and each
// derived them slightly differently: some made them absolute and some did not,
// some proved the components were real directories and some did not, some
// required the working directory to exist and some did not. Every round of
// probing found another site that differed from its neighbours, and the
// disagreement always resolved in favour of deleting something of the user's.
//
// So the rule this file exists to enforce is: resolve and prove ONCE, then pass
// the proven value. Nothing that can delete reads j.Cwd or j.ActualCwd again.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// muxfysCachePrefix is what muxfys names the cache directories it chooses for
// itself, inside whichever CacheBase it was given (see its remote.go). They hold
// a writable mount's output until Unmount uploads it, and cleanup runs before
// Unmount, so deleting one destroys the job's own results.
const muxfysCachePrefix = ".muxfys"

// jobWorkSpaceSnapshot is the copy of a Job's fields that the workspace
// resolution needs, taken under the Job's lock and used only after releasing it.
//
// It exists for two reasons. The resolution walks the filesystem, and
// DEVELOPERS.md section 2 forbids holding a lock across slow work. And
// ActualCwd is written under the Job's lock by applyLiveSnapshot while cleanup
// runs in the same manager process, so reading it unlocked is a data race - one
// whose outcome decides which directory gets deleted.
type jobWorkSpaceSnapshot struct {
	cwdMatters bool
	cwd        string
	actualCwd  string
	key        string
	mounts     MountConfigs
}

// workSpaceSnapshot copies, under the Job's read lock, everything the workspace
// resolution reads from it. Nothing downstream looks at the Job again.
//
// Only field copies and Key()'s hash of them happen here, so the lock is held
// for no longer than the copy itself. MountConfigs is only ever replaced
// wholesale, never mutated in place, so copying the slice header is enough.
func (j *Job) workSpaceSnapshot() jobWorkSpaceSnapshot {
	j.RLock()
	defer j.RUnlock()

	return jobWorkSpaceSnapshot{
		cwdMatters: j.CwdMatters,
		cwd:        j.Cwd,
		actualCwd:  j.ActualCwd,
		key:        j.Key(),
		mounts:     j.MountConfigs,
	}
}

// workSpacePaths is the lexical half of the resolution: the Job's directories,
// made absolute and cleaned, and checked for the shape wr gives the ones it
// creates. It touches no filesystem, so a Job that cannot possibly have a
// deletable workspace is refused before a single syscall is made.
type workSpacePaths struct {
	// cwd is the Job's Cwd: the boundary every deletion must stay inside.
	cwd string

	// rel is actualCwd as a path relative to cwd, so that it can be named to an
	// open handle on cwd rather than resolved afresh from the top.
	rel string

	// actualCwd is the working directory wr created for the Job, workSpace is
	// its parent - the disposable directory that also holds tmp and any cache
	// dirs - and actualCwdName is what actualCwd is called within it.
	actualCwd     string
	workSpace     string
	actualCwdName string

	// createdForThisJob is true when the paths are exactly the ones mkHashedDir
	// builds from this Job's key. That is proof of origin, where depth and name
	// alone are only a coincidence filter, and it is what lets cleanup tolerate
	// a working directory the Job's own Cmd deleted.
	createdForThisJob bool

	// mounts is the Job's MountConfigs, from which the mount points and cache
	// locations that must survive cleanup are resolved.
	mounts MountConfigs
}

// paths does the lexical resolution and its checks.
//
// A nil result with a nil error means wr created no directory for this Job, so
// there is nothing it is entitled to delete: a CwdMatters Job runs directly in
// the user's own Cwd (cleanup is documented in cmd/add.go as having no effect on
// one), and a blank ActualCwd means the Job never ran. ActualCwd should always
// be blank on a CwdMatters Job, but we don't rely on that: one persisted by wr
// v0.37.0|1 can have it set to Cwd, and deleting the parent of that is the
// incident this whole file exists for.
func (s jobWorkSpaceSnapshot) paths() (*workSpacePaths, error) {
	if s.cwdMatters || s.actualCwd == "" {
		return nil, nil //nolint:nilnil // "wr created nothing here" is not a failure
	}

	cwd, actualCwd, err := absJobDirs(s.cwd, s.actualCwd)
	if err != nil {
		return nil, err
	}

	rel, err := createdCwdRel(cwd, actualCwd)
	if err != nil {
		return nil, err
	}

	return &workSpacePaths{
		cwd:               cwd,
		rel:               rel,
		actualCwd:         actualCwd,
		workSpace:         filepath.Dir(actualCwd),
		actualCwdName:     filepath.Base(actualCwd),
		createdForThisJob: relIsJobCreatedCwd(rel, s.key),
		mounts:            s.mounts,
	}, nil
}

// absJobDirs cleans a Job's Cwd and ActualCwd, refusing either unless it is
// already absolute.
//
// A relative path means nothing without knowing which process resolves it, and
// cleanup runs in TWO of them: the runner, and the manager when it declares a
// job lost (killLostJobAndTriggerBehaviours). Everything below resolves with
// filepath.Abs, ie. against whichever process is cleaning up - so a relative Cwd
// made every containment proof hold against the MANAGER's directory instead, and
// the deletion landed on whatever happened to sit at the same relative path
// beside it.
//
// Job.Cwd is stored exactly as the user typed it and cannot be normalised at the
// source, because it feeds Job.Key() and so job identity. Refusing here turns a
// deletion in the wrong tree into a leaked workspace and a loud error, which is
// the right way round.
func absJobDirs(cwd, actualCwd string) (string, string, error) {
	if !filepath.IsAbs(cwd) || !filepath.IsAbs(actualCwd) {
		return "", "", fmt.Errorf("%w: %s and %s must both be absolute", errNotBelowBaseDir, cwd, actualCwd)
	}

	return filepath.Clean(cwd), filepath.Clean(actualCwd), nil
}

// createdCwdRel returns actualCwd relative to cwd, having refused it unless it
// is strictly inside cwd and has the shape of a working directory wr creates.
//
// How far below Cwd the reported directory sits, and what it is called, are the
// only things about it wr can check without trusting whoever reported it.
// Everything below treats its PARENT as a disposable workspace, so a value
// naming a directory of the user's would have wr sweep that directory whole -
// which is the incident this fix is named for, arriving through a different
// field.
func createdCwdRel(cwd, actualCwd string) (string, error) {
	rel, err := filepath.Rel(cwd, actualCwd)
	if err != nil {
		return "", fmt.Errorf("%w: %s vs %s: %w", errNotBelowBaseDir, actualCwd, cwd, err)
	}

	if !relIsBelow(rel) {
		return "", fmt.Errorf("%w: %s is not inside the job's cwd %s", errNotBelowBaseDir, actualCwd, cwd)
	}

	if !relIsCreatedCwd(rel) {
		return "", fmt.Errorf("%w: %s", errNotACreatedCwd, actualCwd)
	}

	return rel, nil
}

// jobWorkSpace is the proven account of the disposable directory wr created for
// a Job, and the only thing in wr that may license a deletion below the Job's
// Cwd. Behaviour.cleanup and Job.Unmount's empty-dir tidy-up both work from one,
// so they cannot disagree about which directories are wr's to delete - which is
// what the two of them did in three separate rounds of this bug.
//
// The caller must Close it.
type jobWorkSpace struct {
	// paths is the lexical resolution every field below was proven against.
	paths *workSpacePaths

	// cwdRoot is an open handle on the Job's Cwd, and every deletion is made
	// with a path relative to it. That is what closes the gap between proving a
	// path may be deleted and deleting it: a relative operation on a root cannot
	// leave that root, so a directory component swapped for a symlink after the
	// proof can no longer redirect a deletion out of Cwd.
	cwdRoot *os.Root

	// proven is the workspace, proven to be a real directory strictly inside
	// cwdRoot with no symlink among the components leading to it.
	proven provenDirs

	// keep is everything the Job's live mounts and caches need to survive,
	// classified once against the paths above.
	keep keptDirs
}

// resolveWorkSpace is the ONE place a Job's Cwd and ActualCwd are read for a
// destructive purpose. It resolves them, proves what it can about them, and
// resolves and classifies every mount point and cache location in the same
// breath, so that no caller has to - or is able to - work any of it out again.
//
// A nil result with a nil error means wr created nothing here that it may
// delete: see paths() for the cases, plus a Job Cwd that has itself already
// gone, which leaves nothing of ours inside it. An error means the reported
// directories cannot be shown to be wr's, in which case wr deletes nothing at
// all and says why: leaving a workspace behind is recoverable, deleting the
// wrong directory is not.
//
// The caller must Close a non-nil result.
func (j *Job) resolveWorkSpace() (*jobWorkSpace, error) {
	paths, err := j.workSpaceSnapshot().paths()
	if err != nil || paths == nil {
		return nil, err
	}

	return paths.prove()
}

// resolvedWorkSpaceOrNone is resolveWorkSpace for a caller that has nowhere to
// report a refusal to. It makes the same decision - a workspace wr cannot prove
// is one wr touches nothing in - and simply says "none" instead of why.
//
// Only Job.Unmount's tidy-up uses it, because an error returned there fails the
// job itself, and a tidy-up that found nothing of ours to tidy is not a failed
// unmount. Behaviour.cleanup reports the same refusals loudly, since there they
// mean a workspace was leaked.
func (j *Job) resolvedWorkSpaceOrNone() *jobWorkSpace {
	ws, err := j.resolveWorkSpace()
	if err != nil {
		return nil
	}

	return ws
}

// prove opens the Job's Cwd and proves the workspace is a real directory
// strictly inside it, with no symlink among the components leading to it.
//
// A resolved proof would not be good enough: it names a symlink's target rather
// than the directory itself, and the deletions below descend into the workspace
// and into the working directory by reading them, which follows a symlinked
// final component.
func (p *workSpacePaths) prove() (*jobWorkSpace, error) {
	cwdRoot, err := openBaseRoot(p.cwd)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // the Cwd has gone, so nothing of ours is inside it
		}

		return nil, fmt.Errorf("%w: could not open the job's cwd %s: %w", errNotBelowBaseDir, p.cwd, err)
	}

	proven, err := p.proveBelow(cwdRoot)
	if err != nil {
		cwdRoot.Close()

		return nil, err
	}

	return &jobWorkSpace{paths: p, cwdRoot: cwdRoot, proven: proven, keep: p.keptDirs()}, nil
}

// proveBelow proves both halves of what wr made: the workspace, as a real dir
// strictly inside the open Cwd with no symlink among the components leading to
// it, and the working directory inside it.
//
// A workspace that is not there at all used to be proven trivially and skip
// proveActualCwd, on the reasoning that nothing is inside it to identify and
// nothing is inside it to delete. That was wrong about the second half. What is
// left for cleanup to do is walk up removing empty parent directories, and those
// parents are the user's whenever the reported path is: the walk unlinked up to
// five levels of a user's own empty output tree - `results/2024/runA/sampleB`
// under an ActualCwd of `.../sampleB/absent/cwd` - and stopped only where a
// level happened to be non-empty.
//
// So absence gets no exemption of its own. proveActualCwd runs on both branches,
// and its rule - absence is tolerated only for a workspace the paths prove wr
// built for THIS Job - is the same rule either way, which is what keeps the
// second cleanup of a real Job working while a fabricated path is refused.
func (p *workSpacePaths) proveBelow(cwdRoot *os.Root) (provenDirs, error) {
	proven, ok := realDirBelow(cwdRoot, p.workSpace)
	if !ok {
		return provenDirs{}, fmt.Errorf(
			"%w: refusing to clean up %s, whose parent is not a real dir inside the job's cwd %s",
			errNotBelowBaseDir, p.actualCwd, p.cwd)
	}

	return proven, p.proveActualCwd(cwdRoot)
}

// proveActualCwd proves the Job's working directory is a real directory that is
// really there. It is named relative to the open Cwd, so the lookup cannot be
// talked into leaving it, and an lstat does not follow a symlinked final
// component - which is the one component above it that the workspace proof has
// not already covered.
//
// A working directory that is not there is tolerated only when the paths prove
// the workspace is the one wr built for this Job, in which case its absence just
// means the Job's own Cmd removed it, or a previous cleanup did. That keeps
// cleanup idempotent for every workspace wr actually made, which matters because
// it runs twice for a lost job.
//
// Without that proof, absence is refused. The depth-and-name check reads only
// the shape of the path, so appending "/cwd" to a directory of the user's at the
// right depth satisfies it, and its PARENT would then be swept as a workspace,
// or walked for empty dirs by Unmount's tidy-up. A directory that is not there
// is not one wr created, and requiring it to be there is all that stands between
// that user directory and a recursive delete - or, when the workspace is missing
// too, between the user's empty directories and the upward walk.
func (p *workSpacePaths) proveActualCwd(cwdRoot *os.Root) error {
	info, err := cwdRoot.Lstat(p.rel)
	if err == nil && info.IsDir() {
		return nil
	}

	if os.IsNotExist(err) && p.createdForThisJob {
		return nil
	}

	return fmt.Errorf("%w: refusing to clean up %s, which is not a real dir inside the job's cwd %s",
		errNotBelowBaseDir, p.actualCwd, p.cwd)
}

// Close releases the handle on the Job's Cwd.
func (ws *jobWorkSpace) Close() {
	ws.cwdRoot.Close()
}

// keptDirs is everything cleanup must leave alone inside a Job's workspace: its
// mount points, which are still live when cleanup runs (Job.Unmount comes after
// it in client.go) so that deleting through one would recurse into the user's
// remote file system; and the cache directories muxfys writes a writable mount's
// output to, which are not uploaded until that Unmount.
//
// Each is classified ONCE, against the proven workspace and working directory,
// in the one place that knows both. mountDirsToKeep and entryLeadingTo used to
// classify separately, from raw strings, with only one of them normalising - and
// the one guarding the more dangerous deletion was the one that did not.
type keptDirs struct {
	// wholeActualCwd is set when something that must survive IS the working
	// directory, so that none of its contents may be touched.
	wholeActualCwd bool

	// inActualCwd are the paths that must survive that lie strictly inside the
	// working directory, relative to it.
	inActualCwd []string

	// workSpaceEntries are the names of the workspace's own entries that lead to
	// something that must survive. Deleting one would delete a mount point, or
	// the directories above a deeper one along with it.
	workSpaceEntries map[string]bool

	// mountPoints are the Job's mount points that lie at or inside the
	// workspace, absolute. They are the only directories Unmount's empty-dir
	// tidy-up may walk: MountConfig.Mount may be an absolute path to "any
	// directory you're able to write to", so a Job can name an existing
	// directory of the user's inside their own Cwd, and that walk removes empty
	// dirs and then their empty parents.
	mountPoints []string
}

// keptDirs resolves every mount point and cache location the Job has, and
// classifies each one against the workspace and the working directory.
func (p *workSpacePaths) keptDirs() keptDirs {
	keep := keptDirs{workSpaceEntries: make(map[string]bool, len(p.mounts))}

	mounts := p.mountPoints()

	for _, mount := range mounts {
		if _, ok := relBelowDir(p.workSpace, mount); ok {
			keep.mountPoints = append(keep.mountPoints, mount)
		}

		keep.protect(p, mount)
	}

	for _, cache := range p.cacheDirs() {
		keep.protect(p, cache)
	}

	return keep
}

// mountPoints resolves each MountConfig.Mount the way Job.Mount does for a Job
// that has a wr-created working directory: an unspecified mount lands on the
// working directory itself, an absolute one is taken as given, and a relative
// one is resolved against the working directory (so it can climb out of it, eg.
// "../shared" for a mount shared between the Jobs of a Cwd).
//
// Working from the resolved points rather than the raw Mount strings is what
// makes an absolute mount inside the working directory recognisable at all; the
// raw strings protected only the relative ones.
func (p *workSpacePaths) mountPoints() []string {
	points := make([]string, 0, len(p.mounts))

	for _, mc := range p.mounts {
		points = append(points, filepath.Clean(resolveMountPoint(mc.Mount, p.actualCwd, p.actualCwd)))
	}

	return points
}

// cacheDirs resolves every location muxfys may write a mount's cache to, exactly
// as Job.Mount resolves them for a Job with a wr-created working directory: a
// MountConfig.CacheBase relative to the working directory and defaulting to the
// workspace, and a MountTarget.CacheDir relative to the workspace.
//
// Both are returned, not just the explicit CacheDir, because muxfys creates its
// own cache directory inside the CacheBase when no CacheDir is given. A
// CacheBase that resolves to the workspace itself yields the workspace, which
// classifies as neither inside the working directory nor as an entry of the
// workspace, and is therefore covered by the muxfysCachePrefix rule instead - the
// one place a name, rather than a path, is what identifies a cache dir.
func (p *workSpacePaths) cacheDirs() []string {
	dirs := make([]string, 0, len(p.mounts))

	for _, mc := range p.mounts {
		dirs = append(dirs, filepath.Clean(resolveCacheBase(mc.CacheBase, p.actualCwd, p.workSpace)))

		for _, mt := range mc.Targets {
			if cacheDir := resolveCacheDir(mt.CacheDir, p.workSpace); cacheDir != "" {
				dirs = append(dirs, filepath.Clean(cacheDir))
			}
		}
	}

	return dirs
}

// protect records dir as something cleanup must not delete, in whichever of the
// two sweeps could otherwise reach it. A dir that neither sweep can reach is
// somewhere cleanup never deletes, so it needs nothing.
//
// Both sweeps are told, not one or the other: anything at or inside the working
// directory is also inside the workspace, so it names the working directory as
// the workspace entry leading to it, and that is what stops the workspace sweep
// deleting the working directory out from under the first sweep's exceptions.
func (k *keptDirs) protect(p *workSpacePaths, dir string) {
	k.protectInActualCwd(p.actualCwd, dir)

	if name, ok := entryLeadingTo(p.workSpace, dir); ok {
		k.workSpaceEntries[name] = true
	}
}

// protectInActualCwd records dir against the working directory: as the whole of
// it when dir names it, and otherwise as a path within it.
//
// Something that IS the working directory keeps it whole. That is obvious for a
// mount point - the mount is live, so its contents are the user's remote files -
// and it is the deliberate choice for a CacheBase of ".", which names the working
// directory as the place muxfys puts its cache: there is then no way to delete
// the job's own output without deleting the cache that has yet to be uploaded, so
// wr deletes neither.
func (k *keptDirs) protectInActualCwd(actualCwd, dir string) {
	rel, ok := relBelowDir(actualCwd, dir)
	if !ok {
		return
	}

	if rel == "." {
		k.wholeActualCwd = true

		return
	}

	k.inActualCwd = append(k.inActualCwd, rel)
}

// cleanup wipes out the Job's working directory and the workspace holding it, as
// aggressively as the Job's live mounts and caches allow, and then deletes the
// emptied workspace and any empty parent directories up to the Job's Cwd.
func (ws *jobWorkSpace) cleanup() error {
	if cleanupProvenHook != nil {
		cleanupProvenHook()
	}

	// the descent to the workspace is made once and its handles kept, so that
	// emptying the workspace and then deleting it and its empty parents cost one
	// metadata lookup per level instead of re-walking Cwd for each of them.
	chain, err := ws.proven.openChain()
	if err != nil {
		return err
	}
	defer chain.closeAll()

	if err = ws.empty(chain); err != nil {
		return err
	}

	return chain.removeUpward()
}

// empty opens the proven workspace as a root of its own, proves it is the dir
// that was proven rather than one a symlink has since substituted, and deletes
// its contents through it.
//
// A workspace that has already gone is not a failure, and there is nothing there
// to identify or to sweep. The same Job's cleanup runs more than once - the
// runner triggers it, and for a lost job the server triggers it again - and
// Job.Unmount deletes the emptied workspace in between, so the second run finds
// nothing. Erroring here would skip the empty parent dirs cleanup goes on to
// tidy, which is the only thing left for it to do.
func (ws *jobWorkSpace) empty(chain dirChain) error {
	wsRoot, err := chain.openLeaf()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}
	defer wsRoot.Close()

	actualCwd, err := ws.actualCwdNow(wsRoot)
	if err != nil {
		return err
	}

	if len(ws.paths.mounts) == 0 {
		return removeWorkSpaceEntries(wsRoot, nil)
	}

	return ws.removeExcept(wsRoot, actualCwd)
}

// actualCwdNow takes a fresh look at the working directory, as a single named
// entry of the already proven workspace handle, so that the directory the
// deletion opens is proven to be the one it looked at - there is no path left
// for a swap elsewhere to redirect.
//
// This is not a second decision about whether the workspace may be swept; the
// resolution made that decision, once, and proveActualCwd holds it. It is the
// same rule applied again at the moment of use, because a proof is about a path
// string and every syscall re-resolves it. A working directory that has gone
// since the proof is simply nothing to delete; one that has become a symlink or
// a file has been swapped by the Job's own Cmd, and reading it would delete the
// target's contents instead.
func (ws *jobWorkSpace) actualCwdNow(wsRoot *os.Root) (os.FileInfo, error) {
	info, err := wsRoot.Lstat(ws.paths.actualCwdName)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // gone since the proof, so nothing to delete
		}

		return nil, err
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%w: refusing to clean up %s, which is not a real dir inside the job's cwd %s",
			errNotBelowBaseDir, ws.paths.actualCwd, ws.paths.cwd)
	}

	return info, nil
}

// removeExcept deletes the contents of a mounting Job's workspace, keeping the
// dirs its live mounts and caches need.
func (ws *jobWorkSpace) removeExcept(wsRoot *os.Root, actualCwd os.FileInfo) error {
	if !ws.keep.wholeActualCwd && actualCwd != nil {
		err := removeActualCwd(wsRoot, ws.paths.actualCwdName, actualCwd, ws.keep.inActualCwd)
		if err != nil {
			return err
		}
	}

	return removeWorkSpaceEntries(wsRoot, ws.keptEntry)
}

// keptEntry says if an entry of the Job's workspace must survive cleanup: a
// cache dir muxfys named for itself, or an entry leading to one of the Job's
// mount points or cache locations.
//
// The working directory needs no rule of its own. It survives exactly when
// something inside it or at it must survive, and protect() records that same
// thing as the workspace entry leading to it - which is the working directory's
// own name. A separate rule here would be a second way of saying it, and a
// second way of saying it is what every round of this bug was made of.
func (ws *jobWorkSpace) keptEntry(name string) bool {
	return strings.HasPrefix(name, muxfysCachePrefix) || ws.keep.workSpaceEntries[name]
}

// removeWorkSpaceEntries deletes every entry of the workspace that keep doesn't
// claim. A nil keep deletes them all, which is what a Job with no mounts wants.
//
// Each entry is named to wsRoot by its own name alone, with no path above it left
// to resolve, so nothing done here can be redirected elsewhere.
func removeWorkSpaceEntries(wsRoot *os.Root, keep func(name string) bool) error {
	entries, err := readDirIn(wsRoot, ".")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if keep != nil && keep(entry.Name()) {
			continue
		}

		if err = wsRoot.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}

	return nil
}

// removeActualCwd deletes the Job's working directory, keeping the given relative
// dirs if any were specified.
func removeActualCwd(wsRoot *os.Root, actualCwdName string, actualCwdInfo os.FileInfo, keepDirs []string) error {
	if len(keepDirs) == 0 {
		return wsRoot.RemoveAll(actualCwdName)
	}

	actualCwdRoot, err := openVerifiedDir(wsRoot, actualCwdName, actualCwdInfo)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}
	defer actualCwdRoot.Close()

	return removeAllExcept(actualCwdRoot, keepDirs)
}

// rmEmptyMountDirs deletes any empty directories between the Job's mount points
// and its Cwd, for the mount points that lie inside the workspace wr made. It
// returns the error from the last deletion attempted, matching the original
// Unmount behaviour.
//
// Only the workspace is walked. Being inside Cwd is NOT enough: an absolute
// MountConfig.Mount can name an existing directory of the user's inside their own
// Cwd, and this walk deleted that directory and the one above it when it was let
// loose on every mount point. The workspace is the only tree wr created, so it is
// the only one with anything of ours to tidy in.
func (ws *jobWorkSpace) rmEmptyMountDirs() error {
	var err error

	for _, mount := range ws.keep.mountPoints {
		if rmErr := rmEmptyDirsIn(ws.cwdRoot, mount); rmErr != nil {
			err = rmErr
		}
	}

	return err
}

// relBelowDir returns path relative to dir, with BOTH made absolute first, and
// whether it is dir itself (".") or strictly inside it.
//
// Both sides have to be normalised, because a MountConfig.Mount is whatever the
// user typed for `wr add --mounts` and can be anything, while the dirs it is
// compared against come from a Job. filepath.Rel fails when given one absolute
// path and one relative one, and a mount point that failed to be recognised is a
// mount deleted through while it is still live.
func relBelowDir(dir, path string) (string, bool) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}

	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return "", false
	}

	return rel, rel == "." || relIsBelow(rel)
}

// entryLeadingTo returns the name of the entry of dir that path is inside (path
// itself, if it is a direct child). ok is false if path is not strictly inside
// dir, or if either path could not be made absolute.
func entryLeadingTo(dir, path string) (string, bool) {
	rel, ok := relBelowDir(dir, path)
	if !ok || rel == "." {
		return "", false
	}

	name, _, _ := strings.Cut(rel, string(filepath.Separator))

	return name, true
}
