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
// Both consumers - the cleanup that deletes below a Job's Cwd, and the `run`
// behaviour that executes a command there - resolve and check those two fields
// here, once, then work through the handles they get back. Neither reads j.Cwd
// or j.ActualCwd again. Each syscall made through a handle is checked against
// the inode lstat'ed on the way down, so a symlink put in place afterwards
// cannot redirect a deletion or a command.
//
// It is not enough that the two agree on a directory: every OTHER JOB of the
// same Cwd has a workspace path of the same shape, so what identifies THIS
// Job's is the path mkHashedDir builds from its own key - which is what
// relIsJobCreatedCwd requires.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/VertebrateResequencing/wr/clog"
)

// procSelfFD is where Linux names a process's own file descriptors, one entry
// per descriptor number; see runDir for why a command is started in one.
const procSelfFD = "/proc/self/fd/"

// procFDPrefix is procSelfFD. It is a var solely so that a test can stand in a
// host that does not name its own descriptors, since on any box that has /proc
// the fallback below is otherwise unreachable. Nothing in wr assigns to it.
var procFDPrefix = procSelfFD //nolint:gochecknoglobals // the only seam onto the no-/proc path

// workSpaceResolveHook, when set, is called at the start of every workspace
// resolution, so that a test can count the resolutions a code path makes. The
// resolution costs a hash of the Job's key and an lstat per path component
// below its Cwd, and paths on which most Jobs need none of that cannot show
// that in their result, only in the work they did. It is nil in production.
var workSpaceResolveHook func() //nolint:gochecknoglobals // the only seam onto work that has no result

// muxfysCachePrefix is what muxfys names the cache directories it chooses for
// itself, inside whichever CacheBase it was given (see its remote.go). They hold
// a writable mount's output until Unmount uploads it, so cleanup keeps them
// unconditionally rather than judging whether Unmount has already run.
const muxfysCachePrefix = ".muxfys"

// jobWorkSpaceSnapshot is the copy of a Job's fields that the workspace
// resolution needs, taken under the Job's lock and used only after releasing it:
// the resolution walks the filesystem, and ActualCwd is written under the lock
// by applyLiveSnapshot while cleanup runs in the same manager process, so
// reading it unlocked is a race that decides which directory gets deleted.
type jobWorkSpaceSnapshot struct {
	cwdMatters bool
	cwd        string
	actualCwd  string
	key        string
	mounts     MountConfigs
}

// cleanupWorkSpace resolves the snapshot's workspace and clears it out, doing
// nothing at all when wr created nothing there it may delete. It is the whole of
// a Job's workspace cleanup, for the cleanup Behaviour and for the runner.
func (s jobWorkSpaceSnapshot) cleanupWorkSpace() error {
	ws, err := s.resolveWorkSpace()
	if err != nil || ws == nil {
		return err
	}
	defer ws.Close()

	return ws.cleanup()
}

// workSpaceSnapshot copies, under the Job's read lock, everything the workspace
// resolution reads from it; nothing downstream looks at the Job again. Copying
// the MountConfigs slice header is enough, since it is only ever replaced
// wholesale - including by Key(), which must not sort it under this read lock.
func (j *Job) workSpaceSnapshot() jobWorkSpaceSnapshot {
	j.RLock()
	defer j.RUnlock()

	return j.workSpaceSnapshotLocked()
}

// workSpaceSnapshotLocked is workSpaceSnapshot for a caller that already holds
// at least the Job's read lock; see pinBehavioursLocked.
func (j *Job) workSpaceSnapshotLocked() jobWorkSpaceSnapshot {
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
// creates. It touches no filesystem, so a Job that cannot have a deletable
// workspace is refused before a single syscall is made.
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

	mounts MountConfigs
}

// paths does the lexical resolution and its checks.
//
// A nil result with a nil error means wr created no directory for this Job, so
// there is nothing it is entitled to delete: a CwdMatters Job runs directly in
// the user's own Cwd, and a blank ActualCwd means the Job never ran. ActualCwd
// should always be blank on a CwdMatters Job, but we don't rely on that, because
// one persisted by wr v0.37.0|1 can have it set to Cwd.
func (s jobWorkSpaceSnapshot) paths() (*workSpacePaths, error) {
	if s.cwdMatters || s.actualCwd == "" {
		return nil, nil //nolint:nilnil // "wr created nothing here" is not a failure
	}

	cwd, err := absJobDir("cwd", s.cwd)
	if err != nil {
		return nil, err
	}

	actualCwd, err := absJobDir("actual cwd", s.actualCwd)
	if err != nil {
		return nil, err
	}

	rel, err := createdCwdRel(cwd, actualCwd, s.key)
	if err != nil {
		return nil, err
	}

	return &workSpacePaths{
		cwd:           cwd,
		rel:           rel,
		actualCwd:     actualCwd,
		workSpace:     filepath.Dir(actualCwd),
		actualCwdName: filepath.Base(actualCwd),
		mounts:        s.mounts,
	}, nil
}

// absJobDir cleans one of a Job's directories, refusing it unless it is already
// absolute.
//
// This code runs in two processes - the runner, and the manager when it declares
// a job lost - and each resolves a relative path against its own directory, so a
// relative Cwd would aim the containment proof, and a `run` behaviour's command,
// at whatever sits beside the MANAGER.
//
// Job.Cwd cannot be normalised at its source, because it feeds Job.Key() and so
// job identity. Refusing here turns a deletion in the wrong tree into a leaked
// workspace and a loud error, which is the right way round.
func absJobDir(what, dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: the job's %s %s is not absolute", errNotBelowBaseDir, what, dir)
	}

	return filepath.Clean(dir), nil
}

// createdCwdRel returns actualCwd relative to cwd, having refused it unless it
// is strictly inside cwd and is the path mkHashedDir builds from THIS Job's key.
//
// The key is the only thing about a reported directory wr can establish without
// trusting whoever reported it, and everything below treats that directory's
// PARENT as a disposable workspace and runs the user's own `run` command inside
// the directory itself. Anything less lets a value naming a directory of the
// user's, or another Job's live working directory, do both.
func createdCwdRel(cwd, actualCwd, key string) (string, error) {
	rel, err := filepath.Rel(cwd, actualCwd)
	if err != nil {
		return "", fmt.Errorf("%w: %s vs %s: %w", errNotBelowBaseDir, actualCwd, cwd, err)
	}

	if !relIsBelow(rel) {
		return "", fmt.Errorf("%w: %s is not inside the job's cwd %s", errNotBelowBaseDir, actualCwd, cwd)
	}

	if !relIsJobCreatedCwd(rel, key) {
		return "", fmt.Errorf("%w: %s", errNotACreatedCwd, actualCwd)
	}

	return rel, nil
}

// jobWorkSpace is the checked account of the disposable directory wr created for
// a Job, and the only thing in wr that may license a deletion below the Job's
// Cwd. Behaviour.cleanup and Job.Unmount's empty-dir tidy-up both work from one,
// so they cannot disagree about which directories are wr's to delete.
//
// The caller must Close it.
type jobWorkSpace struct {
	// paths is the lexical resolution the fields below were checked against.
	paths *workSpacePaths

	// cwdRoot is an open handle on the Job's Cwd, and every deletion is made
	// through it rather than through a re-resolved path string: a relative
	// operation on an os.Root cannot leave that root, so a component replaced by
	// a symlink after the check cannot redirect a deletion out of Cwd. It is the
	// handle the proven field holds, not a second one on the same directory.
	cwdRoot *os.Root

	// proven is the workspace: a real directory strictly inside cwdRoot, with no
	// symlink among the components leading to it.
	proven provenDirs

	// actualCwdInfo is the lstat taken of the working directory during the
	// descent, or nil where its absence was tolerated. Anything that opens the
	// working directory afterwards compares against this to confirm it got the
	// same inode.
	actualCwdInfo os.FileInfo

	// keep is everything the Job's live mounts and caches need to survive,
	// classified once against the paths above.
	keep keptDirs
}

// resolveWorkSpace is the ONE place a Job's Cwd and ActualCwd are turned into a
// licence to delete. It resolves them, checks they name a directory wr built for
// this Job, holds the Cwd open, and resolves and classifies every mount point
// and cache location in the same breath, so that no caller has to - or is able
// to - work any of it out again.
//
// Asked of a SNAPSHOT, not the live Job: applyLiveSnapshot rewrites ActualCwd
// under the Job's lock while cleanup walks. See pinnedBehaviours.
//
// A nil result with a nil error means wr created nothing here that it may
// delete: see paths() for the cases, plus a Job Cwd that has itself already
// gone. An error means the reported directories cannot be shown to be wr's, in
// which case wr deletes nothing at all and says why: leaving a workspace behind
// is recoverable, deleting the wrong directory is not.
//
// The caller must Close a non-nil result.
func (s jobWorkSpaceSnapshot) resolveWorkSpace() (*jobWorkSpace, error) {
	if workSpaceResolveHook != nil {
		workSpaceResolveHook()
	}

	paths, err := s.paths()
	if err != nil || paths == nil {
		return nil, err
	}

	return paths.prove(absenceTolerated)
}

// resolveRunDir is the ONE place a Job's Cwd and ActualCwd decide where a `run`
// Behaviour's command executes. It asks the same resolution cleanup asks, of the
// same snapshot, so the two cannot disagree about which directory is the Job's.
//
// A directory that cannot be shown to be the Job's is refused rather than
// substituted for: the command can do anything the user can, so the behaviour
// fails loudly and runs nothing. A CwdMatters Job runs in its Cwd; see
// cwdRunDir.
//
// The one thing it asks differently is absenceRefused: absence has no legitimate
// meaning for a directory a command is about to be executed in.
//
// It returns the directory HELD OPEN, because exec.Cmd resolves a Dir name once
// more when the command starts; see runDir. The caller must Close the result.
func (s jobWorkSpaceSnapshot) resolveRunDir() (*runDir, error) {
	paths, err := s.paths()
	if err != nil {
		return nil, err
	}

	if paths == nil {
		return s.cwdRunDir()
	}

	ws, err := paths.prove(absenceRefused)
	if err != nil {
		return nil, err
	}

	if ws == nil {
		return nil, fmt.Errorf("%w: the job's cwd %s is not there to run in", errNotBelowBaseDir, paths.cwd)
	}
	defer ws.Close()

	if runResolvedHook != nil {
		runResolvedHook()
	}

	return ws.openRunDir()
}

// cwdRunDir answers resolveRunDir for a Job wr created no working directory for.
//
// A CwdMatters Job's Cmd really did run in the user's own Cwd, so the behaviour
// runs there too, with Cwd still required to be absolute for absJobDir's reason.
//
// Any other Job's Cmd did NOT: it ran in a working directory wr made for it, and
// a blank ActualCwd means only that this process never learned which one (the
// manager learns it from a Touch carrying a live snapshot, which a manager with
// no web port never enables). Running the user's command in their Cwd on that
// basis would execute it somewhere the Job had never been, among everything of
// theirs that lives there, so it is refused.
func (s jobWorkSpaceSnapshot) cwdRunDir() (*runDir, error) {
	if !s.cwdMatters {
		return nil, fmt.Errorf("%w: %s reported no working directory to run in",
			errNotACreatedCwd, s.key)
	}

	// nothing is held open: this is the user's own Cwd, which wr did not create
	// and so never checked.
	dir, err := absJobDir("cwd", s.cwd)
	if err != nil {
		return nil, err
	}

	return &runDir{path: dir}, nil
}

// runDir is the directory a `run` Behaviour's command is to be executed in, with
// an open handle on it where there is one to be had.
//
// exec.Cmd takes a Dir NAME that the child resolves again when the command
// starts, so a racer can replace the proven directory in between - and `run`
// also fires in the MANAGER, for a job declared lost whose Cmd may still be
// alive on a node sharing the filesystem.
//
// So the handle is opened while the directory is being checked, and the command
// is started relative to it: on Linux /proc/self/fd/N, which the child resolves
// after fork() while it still has our descriptor table, so it names the
// directory by descriptor rather than by a path a racer can move. Where that is
// not available the name is used and the window is still open; see execDir.
type runDir struct {
	// held is the open directory, or nil when there is none: a CwdMatters Job's
	// own Cwd, which wr did not create and so never checked.
	held *os.File

	// path is the directory's name, used when there is no handle and as the
	// fallback where a handle cannot be named to exec.
	path string
}

// openRunDir hands back the Job's working directory, held open.
//
// It is opened through the handle on the Job's Cwd, so the lookup cannot leave
// it, and the inode it gets is compared with the one the resolution lstat'ed: an
// os.Root follows a relative symlink that stays inside its root, so opening by
// name alone could still be redirected within Cwd.
func (ws *jobWorkSpace) openRunDir() (*runDir, error) {
	held, err := openVerifiedDirFile(ws.cwdRoot, ws.paths.rel, ws.actualCwdInfo)
	if err != nil {
		return nil, fmt.Errorf("%w: refusing to run in %s: %w", errNotBelowBaseDir, ws.paths.actualCwd, err)
	}

	return &runDir{held: held, path: ws.paths.actualCwd}, nil
}

// execDir is the name to give exec.Cmd's Dir.
//
// It is the held directory's own file descriptor, named through /proc/self/fd,
// whenever that names the directory we are holding: the child chdirs to it
// between fork and exec, while it still has a copy of our descriptor table, so
// it lands in the directory itself rather than in whatever the path resolves to
// by then.
//
// Where the platform will not name the descriptor it is the path, runDir's
// second resolution is open again, and that is SAID rather than left silent. It
// warns rather than refuses because there is nothing else to run the command in,
// and a host without /proc is already outside what wr supports (Execute needs it
// for peak RAM tracking); it warns every time, because the exposure is per
// command.
//
// A runDir holding nothing is a CwdMatters Job's own Cwd, which wr did not
// create and so never checked.
func (r *runDir) execDir() string {
	if r.held == nil {
		return r.path
	}

	fdPath, ok := r.fdName()
	if ok {
		return fdPath
	}

	clog.Warn(context.Background(), "this host does not name a process's own file descriptors, so a run "+
		"behaviour's command must be started at a path that gets resolved again when it starts; whatever "+
		"can replace that path in between decides where the command runs",
		"dir", r.path, "fds", procFDPrefix)

	return r.path
}

// fdName builds /proc/self/fd/N for the directory handle being held, and reports
// whether that path really does resolve to the directory the handle refers to.
//
// The caller gives the path to a child process as cmd.Dir, so the child resolves
// it again after fork(). Where /proc is not mounted, or does not name a process's
// own descriptors, the path is not that directory, so the answer is no and the
// caller falls back to the plain path.
func (r *runDir) fdName() (string, bool) {
	fdPath := procFDPrefix + strconv.Itoa(int(r.held.Fd()))

	held, err := r.held.Stat()
	if err != nil {
		return "", false
	}

	named, err := os.Stat(filepath.Clean(fdPath))
	if err != nil || !os.SameFile(named, held) {
		return "", false
	}

	return fdPath, true
}

// Close releases the handle, which must not happen until the command has
// started: it is what the child chdirs through.
func (r *runDir) Close() {
	if r.held != nil {
		r.held.Close()
	}
}

// absenceRule says what a resolution makes of a working directory that is not
// there. It is a parameter rather than a property of the paths because the two
// consumers of the resolution differ on it, and only on it.
//
// Cleanup tolerates absence: the Job's own Cmd or a previous cleanup may have
// removed the directory, and cleanup runs twice for a lost job, so refusing
// would leak a workspace every second time - affordable because the path was
// proven to be the one wr built for this Job before it was ever missed. A `run`
// behaviour cannot tolerate it: returning the name of a directory that is not
// there leaves whatever creates it next to choose where the command runs.
type absenceRule bool

const (
	absenceTolerated absenceRule = true
	absenceRefused   absenceRule = false
)

// prove opens the Job's Cwd and proves the workspace is a real directory
// strictly inside it, with no symlink among the components leading to it.
//
// There is deliberately no fallback that resolves the symlinks instead: a
// resolved path names a symlink's target rather than the directory itself, and
// the deletions below descend into the workspace and the working directory by
// reading them, which follows a symlinked final component.
func (p *workSpacePaths) prove(absent absenceRule) (*jobWorkSpace, error) {
	cwdRoot, err := openBaseRoot(p.cwd)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // the Cwd has gone, so nothing of ours is inside it
		}

		return nil, fmt.Errorf("%w: could not open the job's cwd %s: %w", errNotBelowBaseDir, p.cwd, err)
	}

	proven, actualCwdInfo, err := p.proveBelow(cwdRoot, absent)
	if err != nil {
		cwdRoot.Close()

		return nil, err
	}

	return &jobWorkSpace{
		paths: p, cwdRoot: cwdRoot, proven: proven, actualCwdInfo: actualCwdInfo, keep: p.keptDirs(),
	}, nil
}

// proveBelow proves both halves of what wr made: the workspace, as a real dir
// strictly inside the open Cwd with no symlink among the components leading to
// it, and the working directory inside it.
//
// A workspace that is not there gets no exemption from proveActualCwd, tempting
// as one looks: what is left for cleanup to do then is walk up removing empty
// parent directories, and those parents are the user's whenever the reported
// path is.
func (p *workSpacePaths) proveBelow(cwdRoot *os.Root, absent absenceRule) (provenDirs, os.FileInfo, error) {
	proven, ok := realDirBelow(cwdRoot, p.workSpace)
	if !ok {
		return provenDirs{}, nil, fmt.Errorf(
			"%w: refusing to use %s, whose parent is not a real dir inside the job's cwd %s",
			errNotBelowBaseDir, p.actualCwd, p.cwd)
	}

	info, err := p.proveActualCwd(cwdRoot, absent)

	return proven, info, err
}

// proveActualCwd proves the Job's working directory is a real directory that is
// really there. It is named relative to the open Cwd, so the lookup cannot be
// talked into leaving it, and an lstat does not follow a symlinked final
// component - the one component the workspace proof has not already covered.
//
// Absence is tolerated only when the caller says it means something; see
// absenceRule.
func (p *workSpacePaths) proveActualCwd(cwdRoot *os.Root, absent absenceRule) (os.FileInfo, error) {
	info, err := cwdRoot.Lstat(p.rel)
	if err == nil && info.IsDir() {
		return info, nil
	}

	if os.IsNotExist(err) && absent == absenceTolerated {
		return nil, nil //nolint:nilnil // there is nothing there to have lstat'ed, and that is allowed here
	}

	return nil, fmt.Errorf("%w: refusing to use %s, which is not a real dir inside the job's cwd %s",
		errNotBelowBaseDir, p.actualCwd, p.cwd)
}

// Close releases the handle on the Job's Cwd, and so also the root proven holds.
func (ws *jobWorkSpace) Close() {
	ws.cwdRoot.Close()
}

// keptDirs is everything cleanup must leave alone inside a Job's workspace: its
// mount points, where deleting through a still-live one would recurse into the
// user's remote file system; and the cache directories muxfys writes a writable
// mount's output to, which are not uploaded until Unmount. Both come from the
// snapshot's MountConfigs rather than from what is mounted now, and are kept
// unconditionally, so a caller may clean up on either side of Job.Unmount. Each is
// classified ONCE, against the proven workspace and working directory.
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
	// tidy-up may walk: MountConfig.Mount may be an absolute path to any
	// directory the user can write to, including an existing one of their own
	// inside their Cwd, and that walk removes empty dirs and their parents.
	mountPoints []string

	// wholeWorkSpace is set when one of the Job's mount points is the workspace
	// itself or a directory above it, which makes everything wr created for the
	// Job the inside of a live mount: the working directory, TMPDIR and any cache
	// beside them are then the user's remote objects, read through a mount
	// Unmount has not got to yet, so nothing there may be deleted.
	//
	// protect() can record nothing about such a mount point, since both halves of
	// it describe something INSIDE the workspace.
	wholeWorkSpace bool

	// muxfysNamesWorkSpaceEntry is set when one of the Job's mounts has a
	// CacheBase that resolves to the workspace itself, the default for a mounting
	// Job. That is the one case where a directory cleanup must keep has a name
	// rather than a path: muxfys chooses the name of the cache dir it makes
	// inside the CacheBase it was given (its remote.go), so wr can only recognise
	// the prefix.
	//
	// It is a fact about the Job's own configuration rather than a rule applied
	// to every Job: applied to a Job with no mounts, and so no muxfys, it would
	// let that Job's Cmd keep its whole workspace alive by creating a
	// ../.muxfyssquat directory.
	muxfysNamesWorkSpaceEntry bool
}

// keptDirs resolves every mount point and cache location the Job has, and
// classifies each one against the workspace and the working directory.
func (p *workSpacePaths) keptDirs() keptDirs {
	keep := keptDirs{workSpaceEntries: make(map[string]bool, len(p.mounts))}

	for _, mount := range p.mountPoints() {
		// mountPoints keeps the LEXICAL answer, and is the one classification
		// here that must: it licenses an upward walk of empty dirs rather than
		// protecting anything, and rmEmptyDirsIn requires a path that is inside
		// the open Cwd with no symlink among its components. A mount recognised
		// only once its symlinks are resolved is not such a path, so a spelling
		// the walk cannot use leaves an emptied dir behind rather than deleting
		// through one.
		if _, ok := relBelowDir(p.workSpace, mount); ok {
			keep.mountPoints = append(keep.mountPoints, mount)
		}

		if dirIsAtOrAbove(mount, p.workSpace) {
			keep.wholeWorkSpace = true
		}

		keep.protect(p, mount)
	}

	for _, mc := range p.mounts {
		keep.protectCaches(p, mc)
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
// makes an absolute mount inside the working directory recognisable at all.
func (p *workSpacePaths) mountPoints() []string {
	points := make([]string, 0, len(p.mounts))

	for _, mc := range p.mounts {
		points = append(points, filepath.Clean(resolveMountPoint(mc.Mount, p.actualCwd, p.actualCwd)))
	}

	return points
}

// protectCaches records every location muxfys may write one MountConfig's cache
// to, resolved exactly as Job.Mount resolves them for a Job with a wr-created
// working directory: a MountConfig.CacheBase relative to the working directory
// and defaulting to the workspace, and a MountTarget.CacheDir relative to the
// workspace (which is the base buildRemoteConfigs hands muxfys).
//
// The CacheBase is recorded as well as any explicit CacheDir, because muxfys
// creates a cache directory of its own inside the CacheBase for every Target
// that gives no CacheDir. A CacheBase that resolves to the workspace itself
// classifies as neither inside the working directory nor as an entry of the
// workspace, so the muxfysCachePrefix rule is what covers the directory muxfys
// puts there, and this is where wr learns that rule has something to cover.
//
// A cache location ABOVE the workspace needs recording nowhere: cleanup only
// deletes inside the workspace, and the only thing that goes higher is the
// upward walk of EMPTY parents, which a directory holding a cache stops.
func (k *keptDirs) protectCaches(p *workSpacePaths, mc MountConfig) {
	base := filepath.Clean(resolveCacheBase(mc.CacheBase, p.actualCwd, p.workSpace))

	k.protect(p, base)

	if rel, ok := relBelowDirResolved(p.workSpace, base); ok && rel == "." {
		k.muxfysNamesWorkSpaceEntry = true
	}

	for _, mt := range mc.Targets {
		if cacheDir := resolveCacheDir(mt.CacheDir, p.workSpace); cacheDir != "" {
			k.protect(p, filepath.Clean(cacheDir))
		}
	}
}

// protect records dir as something cleanup must not delete, in whichever of the
// two sweeps could otherwise reach it.
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
// Something that IS the working directory keeps it whole. For a mount point the
// mount is live, so its contents are the user's remote files; for a CacheBase of
// "." there is no way to delete the job's own output without deleting a cache
// that has yet to be uploaded, so wr deletes neither.
func (k *keptDirs) protectInActualCwd(actualCwd, dir string) {
	rel, ok := relBelowDirResolved(actualCwd, dir)
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

	// a workspace inside one of the Job's live mounts has nothing of wr's in it
	// to delete, and nothing of wr's above it to tidy; see
	// keptDirs.wholeWorkSpace.
	if ws.keep.wholeWorkSpace {
		return nil
	}

	// the descent is made once and its handles kept, so that emptying the
	// workspace and then deleting it and its empty parents cost one metadata
	// lookup per level instead of re-walking Cwd for each of them.
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

// empty opens the workspace as a directory handle of its own, confirms that
// handle is the same inode the descent lstat'ed - so a symlink put in its place
// since then cannot redirect the deletion - and deletes the workspace's contents
// through it, keeping the Job's live mount points and caches.
//
// A workspace that has already gone is not a failure: cleanup runs more than
// once for a lost job and Job.Unmount deletes the emptied workspace in between,
// so erroring here would skip the empty parent dirs cleanup goes on to tidy.
//
// A Job with no mounts gets no fast path around the keep set: that set is
// already empty for such a Job, so the sweep below deletes everything anyway.
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

	if !ws.keep.wholeActualCwd && actualCwd != nil {
		if err = removeActualCwd(wsRoot, ws.paths.actualCwdName, actualCwd, ws.keep.inActualCwd); err != nil {
			return err
		}
	}

	return ws.removeWorkSpaceEntries(wsRoot)
}

// actualCwdNow lstats the working directory again, as a single named entry of the
// workspace handle, and returns the info the deletion is to be made against.
//
// This is not a second decision about whether the workspace may be swept -
// proveActualCwd made that one - but the same check at the moment of use, since
// every syscall re-resolves the name. Gone since then means there is nothing to
// delete; turned into a symlink or a file means the Job's own Cmd replaced it,
// and reading it would delete the target's contents instead.
//
// Being a real dir is not enough, because another directory can be renamed onto
// the name. So identity comes from comparing inodes with ws.actualCwdInfo,
// through the same proveSameDir that `run`'s openVerifiedDirFile uses, and it is
// that earlier info that is handed on rather than this fresh lstat.
//
// A nil ws.actualCwdInfo is the absence proveActualCwd tolerated, so there is no
// inode to compare; what licenses the deletion then is the path, which
// createdCwdRel has already matched against the one mkHashedDir built for THIS
// Job.
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

	if ws.actualCwdInfo == nil {
		return info, nil
	}

	if err = proveSameDir(info, ws.actualCwdInfo, ws.paths.actualCwd); err != nil {
		return nil, fmt.Errorf("refusing to clean up %s inside the job's cwd %s: %w",
			ws.paths.actualCwd, ws.paths.cwd, err)
	}

	return ws.actualCwdInfo, nil
}

// keptEntry says if an entry of the Job's workspace must survive cleanup: an
// entry leading to one of the Job's mount points or cache locations, or a cache
// dir muxfys named for itself where the Job has a mount that puts one there.
//
// The working directory needs no rule of its own: it survives exactly when
// something inside or at it must, and protect() records that as the workspace
// entry leading to it, which is the working directory's own name.
func (ws *jobWorkSpace) keptEntry(name string) bool {
	if ws.keep.workSpaceEntries[name] {
		return true
	}

	return ws.keep.muxfysNamesWorkSpaceEntry && strings.HasPrefix(name, muxfysCachePrefix)
}

// removeWorkSpaceEntries deletes every entry of the workspace that keptEntry
// doesn't claim. What survives is asked of the Job's own keep set and of nothing
// else, so there is no second answer to what cleanup may delete. Each entry is
// named to wsRoot by its own name alone, with no path above it left to resolve,
// so nothing done here can be redirected elsewhere.
func (ws *jobWorkSpace) removeWorkSpaceEntries(wsRoot *os.Root) error {
	entries, err := readDirIn(wsRoot, ".")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if ws.keptEntry(entry.Name()) {
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
// Cwd, and this walk removes empty dirs and then their empty parents. The
// workspace is the only tree wr created.
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
// whether it is dir itself (".") or strictly inside it. Each caller decides for
// itself what "." means to it, so all of them check rel.
//
// Both sides have to be normalised, because a MountConfig.Mount is whatever the
// user typed for `wr add --mounts` while the dirs it is compared against come
// from a Job: filepath.Rel fails when given one absolute path and one relative
// one, and an unrecognised mount point is a mount deleted through while live.
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

// relBelowDirResolved is relBelowDir, asking the FILESYSTEM rather than the two
// strings when the strings disagree: a mount at <symlink-to-Cwd>/<AppName>_cwd is
// the same directory ".." names two levels down, and a lexical comparison
// recognises only one of those two spellings.
//
// It is what the keep set classifies a mount point or cache location with, so
// that every consumer of that set agrees about which directories are the Job's
// live mounts, whichever spelling its MountConfig gave them.
//
// The rel it returns is safe to name to a handle on the UNRESOLVED dir even when
// it came from the resolved comparison: resolving a path does not change WHICH
// DIRECTORY it names, so the components of the rel are entries of dir itself, and
// EvalSymlinks leaves none of them a symlink. An absolute resolved path is not
// safe that way - it is named in a different tree from the handles the deletions
// are made through - which is why nothing here keeps one.
//
// The lexical comparison is made first, so the filesystem is only asked about a
// mount or cache that was not already recognised, and never for a Job that
// configured none.
func relBelowDirResolved(dir, path string) (string, bool) {
	if rel, ok := relBelowDir(dir, path); ok {
		return rel, true
	}

	return relBelowDir(resolvedDir(dir), resolvedDir(path))
}

// dirIsAtOrAbove reports whether dir is other, or a directory above it, in either
// spelling; see relBelowDirResolved.
func dirIsAtOrAbove(dir, other string) bool {
	_, ok := relBelowDirResolved(dir, other)

	return ok
}

// resolvedDir is dir with the symlinks in it resolved, falling back to dir
// itself where they cannot be: a mount point wr is asked about need not exist
// yet.
func resolvedDir(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}

	return resolved
}

// entryLeadingTo returns the name of the entry of dir that path is inside (path
// itself, if it is a direct child). ok is false if path is not strictly inside
// dir - dir itself, which relBelowDir reports as ".", included - or if either
// path could not be made absolute.
//
// The containment is the resolved one, because this is what protects a mount
// point from the workspace sweep and the sweep works entry by entry: an entry
// leading to a live mount has the same NAME whichever way the mount point was
// spelled, since both spellings name the one directory.
func entryLeadingTo(dir, path string) (string, bool) {
	rel, ok := relBelowDirResolved(dir, path)
	if !ok || rel == "." {
		return "", false
	}

	name, _, _ := strings.Cut(rel, string(filepath.Separator))

	return name, true
}
