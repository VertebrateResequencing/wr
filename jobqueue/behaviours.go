/*******************************************************************************
 * Copyright (c) 2017-2019, 2021, 2024 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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

// This file contains the implementation of Job behaviours.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/go-multierror"
)

// sentinel errors for behaviour handling.
var (
	errNotACreatedCwd         = errors.New("actual cwd is not a working directory wr created")
	errBehaviourInvalidStatus = errors.New("invalid status")
	errBehaviourArgNotStr     = errors.New("arg is not a string")
	errBehaviourArgNotStrSl   = errors.New("arg is not a []string")
)

// cleanupProvenHook, when set, is called in the moment between the cleanup path
// proving which directory it is entitled to delete and it deleting anything.
// That moment is the race this code is built to survive, and a test cannot
// otherwise get into it reliably. It is nil in production.
var cleanupProvenHook func() //nolint:gochecknoglobals

// BehaviourTrigger is supplied to a Behaviour to define under what circumstance
// that Behaviour will trigger.
type BehaviourTrigger uint8

const (
	// OnExit is a BehaviourTrigger for Behaviours that should trigger when a
	// Job's Cmd is executed and finishes running. These behaviours will trigger
	// after OnSucess and OnFailure triggers, which makes OnExit different to
	// specifying OnSuccess|OnFailure.
	OnExit BehaviourTrigger = 1 << iota

	// OnSuccess is a BehaviourTrigger for Behaviours that should trigger when a
	// Job's Cmd is executed and exits 0.
	OnSuccess

	// OnFailure is a BehaviourTrigger for Behaviours that should trigger when a
	// Job's Cmd is executed and exits non-0.
	OnFailure
)

// BehaviourAction is supplied to a Behaviour to define what should happen when
// that behaviour triggers. (It's a uint8 type as opposed to an actual func to
// save space since we need to store these on every Job; do not treat as a flag
// and OR multiple actions together!)
type BehaviourAction uint8

const (
	// CleanupAll is a BehaviourAction that will delete any directories that
	// were created by a Job due to CwdMatters being false. Note that if the
	// Job's Cmd created output files within the actual cwd, these would get
	// deleted along with everything else. It takes no arguments.
	CleanupAll BehaviourAction = 1 << iota

	// Cleanup is a BehaviourAction that behaves exactly as CleanupAll in the
	// case that no output files have been specified on the Job. If some have,
	// everything except those files gets deleted. It takes no arguments.
	// (NB: since output file specification has not yet been implemented, this
	// is currently identical to CleanupAll.)
	Cleanup

	// Run is a BehaviourAction that runs a given command (supplied as a single
	// string Arg to the Behaviour) in the Job's actual cwd.
	Run

	// CopyToManager is a BehaviourAction that copies the given files (specified
	// as a slice of string paths Arg to the Behaviour) from the Job's actual
	// cwd to a configured location on the machine that the jobqueue server is
	// running on. *** not yet implemented!
	CopyToManager

	// Nothing is a BehaviourAction that does nothing. It allows you to define
	// a Behaviour that will do nothing, distinguishable from a nil Behaviour,
	// for situations where you want to store a desire to change another
	// Behaviour to turn it off.
	Nothing

	// Remove is a BehaviourAction that requests the Job is removed from the
	// queue after being buried. Useful when working with another workflow
	// management system that keeps track of jobs itself and may try to add
	// failed jobs again, in which case they mustn't be in the queue.
	//
	// Unlike other behaviours, the action doesn't occur when Trigger()ed, but
	// rather RemoveRequested() should be called after a Job is buried to ask if
	// it should be removed.
	Remove
)

// ModifyBehaviours converts a BehavioursViaJSON supplied for one trigger when
// modifying a job to real Behaviours. Unlike Behaviours(), an explicitly
// supplied empty set becomes the Nothing behaviour, ie. it turns that trigger
// off: a modification that mentions a trigger replaces all of that trigger's
// behaviours, so leaving a trigger untouched is done by not mentioning it at
// all, not by supplying nothing for it.
func ModifyBehaviours(bjs BehavioursViaJSON, when BehaviourTrigger) Behaviours {
	if len(bjs) == 0 {
		bjs = BehavioursViaJSON{{Nothing: true}}
	}

	return bjs.Behaviours(when)
}

// Behaviour describes something that should happen in response to a Job's Cmd
// exiting a certain way.
type Behaviour struct {
	When BehaviourTrigger
	Do   BehaviourAction
	Arg  any // the arg needed by your chosen action
}

// Trigger will carry out our BehaviourAction if the supplied status matches our
// BehaviourTrigger.
func (b *Behaviour) Trigger(status BehaviourTrigger, j *Job) error {
	if b.When&status == 0 {
		return nil
	}

	switch b.Do {
	case CleanupAll:
		return b.cleanup(j, true)
	case Cleanup:
		return b.cleanup(j, false)
	case Run:
		return b.run(j)
	case CopyToManager:
		return b.copyToManager(j)
	case Remove, Nothing:
		return nil
	}

	return fmt.Errorf("%w %d", errBehaviourInvalidStatus, status)
}

// fillBVJM converts to a bvjMapping. Supply an empty or existing one and this
// will add to it.
func (b *Behaviour) fillBVJM(bvjm *bvjMapping) {
	bvj, ok := b.toBehaviourViaJSON()
	if !ok {
		return
	}

	switch b.When {
	case OnFailure:
		bvjm.OnFailure = append(bvjm.OnFailure, bvj)
	case OnSuccess:
		bvjm.OnSuccess = append(bvjm.OnSuccess, bvj)
	case OnFailure | OnSuccess:
		bvjm.OnFS = append(bvjm.OnFS, bvj)
	case OnExit:
		bvjm.OnExit = append(bvjm.OnExit, bvj)
	default:
		return
	}
}

// toBehaviourViaJSON builds the BehaviourViaJSON for our Do action. The bool is
// false if our action does not map to one (in which case nothing should be
// added to a bvjMapping).
func (b *Behaviour) toBehaviourViaJSON() (BehaviourViaJSON, bool) {
	switch b.Do {
	case Run:
		return BehaviourViaJSON{Run: b.argString()}, true
	case CopyToManager:
		return BehaviourViaJSON{CopyToManager: b.argStringSlice()}, true
	case Cleanup:
		return BehaviourViaJSON{Cleanup: true}, true
	case CleanupAll:
		return BehaviourViaJSON{CleanupAll: true}, true
	case Remove:
		return BehaviourViaJSON{Remove: true}, true
	case Nothing:
		return BehaviourViaJSON{Nothing: true}, true
	default:
		return BehaviourViaJSON{}, false
	}
}

// argString returns our Arg as a string, or "!invalid!" if it isn't one.
func (b *Behaviour) argString() string {
	if cmd, wasStr := b.Arg.(string); wasStr {
		return cmd
	}

	return "!invalid!"
}

// argStringSlice returns our Arg as a []string, or []string{"!invalid!"} if it
// isn't one.
func (b *Behaviour) argStringSlice() []string {
	if files, wasStrSlice := b.Arg.([]string); wasStrSlice {
		return files
	}

	return []string{"!invalid!"}
}

// String provides a nice string representation of a Behaviour for user
// interface display purposes. It is in the form of a JSON string that can be
// converted back to a Behaviour via a BehaviourViaJSON.
func (b *Behaviour) String() string {
	bvjm := &bvjMapping{}
	b.fillBVJM(bvjm)

	// because of automatic HTML escaping, we can't just use json.Marshal(bvjm)

	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)

	err := encoder.Encode(bvjm)
	if err != nil {
		panic(fmt.Sprintf("Encoding a bvjm failed: %s", err))
	}

	return strings.TrimSpace(buffer.String())
}

// cleanup wipes out the Job's unique dir as aggressively as possible, along
// with all empty parent dirs up to Cwd. (The all arg would, when false, keep
// files designated as outputs, but that designation is *** not yet implemented,
// so for now we always wipe everything.)
func (b *Behaviour) cleanup(j *Job, _ bool) error {
	if j.CwdMatters || j.ActualCwd == "" {
		// a CwdMatters job runs directly in the user's own Cwd, so wr created no
		// dir for it and cleanup is documented (cmd/add.go) as having no effect.
		// ActualCwd should always be blank for such a Job, but we don't rely on
		// that: a Job persisted by wr v0.37.0|1 can have it set to Cwd, and then
		// deleting the parent of ActualCwd would destroy the user's own data.
		// For any other Job, a blank ActualCwd just means it never ran, so
		// again there's nothing wr created to delete.
		return nil
	}

	if !filepath.IsAbs(j.Cwd) || !filepath.IsAbs(j.ActualCwd) {
		// a relative path means nothing without knowing which process resolves
		// it, and cleanup runs in TWO of them: the runner, and the manager when
		// it declares a job lost (killLostJobAndTriggerBehaviours). Everything
		// below resolves with filepath.Abs, i.e. against whichever process is
		// cleaning up - so a relative Cwd made every containment proof hold
		// against the MANAGER's directory instead, and deletion landed on
		// whatever happened to sit at the same relative path beside it.
		//
		// Job.Cwd is stored exactly as the user typed it and cannot be
		// normalised at the source, because it feeds Job.Key() and so job
		// identity. Refusing here turns a deletion in the wrong tree into a
		// leaked workspace and a loud error, which is the right way round.
		return fmt.Errorf("%w: %s and %s must both be absolute", errNotBelowBaseDir, j.Cwd, j.ActualCwd)
	}

	// every deletion below is made relative to this handle on the Job's Cwd,
	// which is the boundary all of them must stay inside. Proving a path is
	// inside Cwd and then deleting the path leaves a window in which a proven
	// directory component can be replaced with a symlink; deleting through the
	// handle does not, because the operating system, not us, refuses a name
	// that resolves outside it at the moment of the deletion.
	cwdRoot, err := openBaseRoot(j.Cwd)
	if err != nil {
		if os.IsNotExist(err) {
			// the Job's Cwd has already gone, so there is nothing of ours left
			// inside it to delete.
			return nil
		}

		return fmt.Errorf("%w: could not open the job's cwd %s: %w", errNotBelowBaseDir, j.Cwd, err)
	}
	defer cwdRoot.Close()

	return cleanupBelowCwd(j, cwdRoot)
}

// cleanupBelowCwd does Behaviour.cleanup's deletions, all of them relative to
// an open handle on the Job's Cwd.
func cleanupBelowCwd(j *Job, cwdRoot *os.Root) error {
	workSpace, actualCwdName, err := provenWorkSpace(j, cwdRoot)
	if err != nil {
		return err
	}

	if !relIsCreatedCwd(filepath.Join(workSpace.rel, actualCwdName)) {
		// how far below Cwd the reported directory sits, and what it is called,
		// are the only things about it wr can check without trusting whoever
		// reported it. Everything below treats its PARENT as a disposable
		// workspace, so a value naming a directory of the user's would have wr
		// sweep that directory whole - which is the incident this fix is named
		// for, arriving through a different field.
		return fmt.Errorf("%w: %s", errNotACreatedCwd, j.ActualCwd)
	}

	if cleanupProvenHook != nil {
		cleanupProvenHook()
	}

	// the descent to the workspace is made once and its handles kept, so that
	// emptying the workspace and then deleting it and its empty parents cost
	// one metadata lookup per level instead of re-walking Cwd for each of them.
	chain, err := workSpace.openChain()
	if err != nil {
		return err
	}
	defer chain.closeAll()

	if err := emptyWorkSpace(j, chain, actualCwdName); err != nil {
		return err
	}

	// delete the emptied workspace and any empty parent directories up to Cwd
	return chain.removeUpward()
}

// provenWorkSpace proves the unique dir wr created for this Job: the parent of
// its ActualCwd, which holds tmp, cwd and possibly mount cache dirs. It returns
// that dir, proven and bound to cwdRoot, plus the name ActualCwd has within it.
//
// wr made ActualCwd itself, as a real dir strictly inside Cwd, so its parent is
// a real dir strictly inside Cwd too. If either is anything else now (eg. it is
// stale after a `wr mod --cwd`, was poisoned with Cwd itself, is an unclean path
// that cleans out of Cwd, or the Job's own Cmd replaced a dir below Cwd with a
// symlink), we cannot tell which directory wr is entitled to delete, so we
// delete nothing and report why. Leaving a workspace behind is recoverable;
// deleting the wrong directory is what this whole guard exists to prevent.
//
// A resolved proof is not good enough here, because it names a symlink's target
// rather than the dir itself: the deletions below descend into the workspace and
// into ActualCwd by reading them, which follows a symlinked final component.
func provenWorkSpace(j *Job, cwdRoot *os.Root) (provenDirs, string, error) {
	absActualCwd, err := filepath.Abs(j.ActualCwd)
	if err != nil {
		return provenDirs{}, "", err
	}

	workSpace, ok := realDirBelow(cwdRoot, filepath.Dir(absActualCwd))
	if !ok {
		return provenDirs{}, "", fmt.Errorf(
			"%w: refusing to clean up %s, whose parent is not a real dir inside the job's cwd %s",
			errNotBelowBaseDir, j.ActualCwd, j.Cwd)
	}

	return workSpace, filepath.Base(absActualCwd), nil
}

// emptyWorkSpace opens the proven workspace as a root of its own, proves it is
// the dir that was proven rather than one a symlink has since substituted, and
// deletes its contents through it.
//
// A workspace that has already gone is not a failure. The same Job's cleanup
// runs more than once - the runner triggers it, and for a lost job the server
// triggers it again - and Job.Unmount deletes the emptied workspace in between,
// so the second run finds nothing to delete. Erroring here would skip the empty
// parent dirs that Behaviour.cleanup goes on to tidy.
func emptyWorkSpace(j *Job, chain dirChain, actualCwdName string) error {
	wsRoot, err := chain.openLeaf()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}
	defer wsRoot.Close()

	return cleanupWorkSpace(j, wsRoot, chain.leaf, actualCwdName)
}

// cleanupWorkSpace deletes the contents of the Job's workspace dir, all of it
// relative to wsRoot, the proven handle on that dir. If the Job used mounts it
// takes care not to delete the cache dirs or mounted dirs; otherwise it just
// deletes everything in one go.
func cleanupWorkSpace(j *Job, wsRoot *os.Root, workSpace, actualCwdName string) error {
	// a missing working dir is NOT tolerated here, though a missing workspace is
	// (emptyWorkSpace returns before this). The name check in cleanupBelowCwd
	// looks only at the last component, so appending "/cwd" to any directory of
	// the user's inside Cwd passes it - and nothing else stopped it, because
	// the directory it named did not have to exist. Requiring it to be there,
	// and to be a real dir, means the reported path has to name something wr
	// actually made rather than merely end in the right word.
	//
	// The cost is that a workspace whose cwd was already removed, by a cleanup
	// that failed partway, is now left alone instead of being swept. That is a
	// leaked directory rather than a deleted one, which is the right way round.
	actualCwdInfo, err := provenActualCwd(j, wsRoot, actualCwdName)
	if err != nil {
		return err
	}

	if len(j.MountConfigs) == 0 {
		return removeWorkSpaceEntries(wsRoot, nil)
	}

	keepDirs, keepActualCwd := j.mountDirsToKeep()

	if !keepActualCwd && actualCwdInfo != nil {
		if err := removeActualCwd(wsRoot, actualCwdName, actualCwdInfo, keepDirs); err != nil {
			return err
		}
	}

	return removeWorkSpaceExtras(wsRoot, mountedWorkSpaceEntries(j, workSpace))
}

// provenActualCwd proves that the Job's ActualCwd, which must be the named entry
// of the workspace, is a real directory rather than a symlink or a file: the
// deletions read it, and a read follows a symlinked final component and would
// delete the target's contents instead.
//
// Naming a single entry of an already proven handle is what makes this
// sufficient: there is no path left for a swap elsewhere to redirect. An
// ActualCwd that has already gone gives an os.IsNotExist error, which is not a
// failure, just nothing to delete there.
func provenActualCwd(j *Job, wsRoot *os.Root, actualCwdName string) (os.FileInfo, error) {
	info, err := wsRoot.Lstat(actualCwdName)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%w: refusing to clean up %s, which is not a real dir inside the job's cwd %s",
			errNotBelowBaseDir, j.ActualCwd, j.Cwd)
	}

	return info, nil
}

// mountDirsToKeep works out which of the Job's mount points lie inside its
// ActualCwd, as paths relative to it, and whether one lands on ActualCwd itself
// (in which case ActualCwd must be kept whole).
//
// It works from mountPoints(), which resolves every MountConfig.Mount to an
// absolute path, rather than from the raw Mount strings. Mount may legitimately
// be given as an absolute path - the docs allow "any directory you're able to
// write to" - and an absolute one can still land inside ActualCwd. Reading the
// raw strings protected only the relative mounts, so an absolute mount inside
// ActualCwd was deleted through while it was still live, which is how mounted
// remote content could be reached.
//
// Mount points outside ActualCwd are deliberately not returned here: they are
// protected by removeWorkSpaceExtras instead, which keeps the workspace entry
// leading to each of them.
func (j *Job) mountDirsToKeep() (keepDirs []string, keepActualCwd bool) {
	for _, mount := range j.mountPoints() {
		rel, ok := relBelowDir(j.ActualCwd, mount)
		if !ok {
			continue
		}

		if rel == "." {
			return nil, true
		}

		keepDirs = append(keepDirs, rel)
	}

	return keepDirs, false
}

// removeWorkSpaceEntries deletes every entry of the workspace that keep doesn't
// claim. A nil keep deletes them all, which is what a Job with no mounts wants.
//
// Each entry is named to wsRoot by its own name alone, with no path above it
// left to resolve, so nothing done here can be redirected elsewhere.
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

// mountedWorkSpaceEntries returns, for each of the Job's mount points that lies
// inside workSpace, the name of the workSpace entry you would have to go
// through to reach it. Those entries must be left alone: deleting one would
// delete the mount point, or the dirs above a deeper one along with it.
//
// cmd/add.go documents cleanup as ignoring mounted dirs, and the mounts are
// still live when cleanup runs (Job.Unmount comes after it in client.go), so a
// recursive delete - which has no mount awareness - would recurse through a
// mount point into the user's remote file system.
//
// Mount points below the ActualCwd, and outside workSpace entirely, need
// nothing here: the former are kept by removeActualCwd, and the latter are
// somewhere cleanup never deletes.
func mountedWorkSpaceEntries(j *Job, workSpace string) map[string]bool {
	entries := make(map[string]bool, len(j.MountConfigs))

	for _, mount := range j.mountPoints() {
		if name, ok := entryLeadingTo(workSpace, mount); ok {
			entries[name] = true
		}
	}

	return entries
}

// entryLeadingTo returns the name of the entry of dir that path is inside (path
// itself, if it is a direct child). ok is false if path is not strictly inside
// dir, or if either path could not be made absolute.
// relBelowDir returns path relative to dir, with BOTH made absolute first, and
// whether it is dir itself (".") or strictly inside it.
//
// Both sides have to be normalised because Job.Cwd is stored exactly as the
// user typed it and is never cleaned - it feeds Job.Key(), so normalising it at
// the source would change job identity - and a relative Cwd makes the ActualCwd
// built from it relative too, while an absolute MountConfig.Mount stays
// absolute. filepath.Rel fails when given one of each, and a mount point that
// failed to be recognised is a mount deleted through while it is still live.
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

func entryLeadingTo(dir, path string) (string, bool) {
	rel, ok := relBelowDir(dir, path)
	if !ok || rel == "." {
		return "", false
	}

	name, _, _ := strings.Cut(rel, string(filepath.Separator))

	return name, true
}

// removeWorkSpaceExtras deletes everything inside the workspace except for cwd,
// the cache dirs, and the given entries leading to the Job's mount points, incase
// a job.Cmd did something like `touch ../foo`.
func removeWorkSpaceExtras(wsRoot *os.Root, keepEntries map[string]bool) error {
	return removeWorkSpaceEntries(wsRoot, func(name string) bool {
		return keptWorkSpaceEntry(name, keepEntries)
	})
}

// keptWorkSpaceEntry says if an entry of a Job's workspace must survive
// cleanup: its ActualCwd, one of the muxfys mount cache dirs, or an entry
// leading to one of its mount points.
func keptWorkSpaceEntry(name string, keepEntries map[string]bool) bool {
	return name == createdCwdName || strings.HasPrefix(name, ".muxfys") || keepEntries[name]
}

// removeActualCwd deletes the Job's ActualCwd, keeping the given relative dirs
// if any were specified.
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

// run simply runs the given command from Job's actual cwd.
func (b *Behaviour) run(j *Job) error {
	// this falls back to Cwd unconditionally, unlike Job.workingDir(), which
	// does so only when CwdMatters: cmd.Dir must name a directory that exists,
	// and Cwd is the only one we know does. Don't unify the two: workingDir()
	// feeds what we display and offer to ssh to, where claiming a
	// non-CwdMatters job with no reported ActualCwd is in Cwd would be a lie.
	actualCwd := j.ActualCwd
	if actualCwd == "" {
		actualCwd = j.Cwd
	}

	bc, wasStr := b.Arg.(string)
	if !wasStr {
		return fmt.Errorf("%w: arg %s is type %T", errBehaviourArgNotStr, b.Arg, b.Arg)
	}

	if strings.Contains(bc, " | ") {
		bc = "set -o pipefail; " + bc
	}
	// *** hardcoding bash here, when we could in theory have client.Execute()
	// pass shell in? And yes, we're allowing user to run absolutely any command
	// they like, but that is the very nature of this app. This runs as them,
	// so can do whatever they can do...
	cmd := exec.CommandContext(context.Background(), "/bin/bash", "-c", bc)
	cmd.Dir = actualCwd

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run behaviour failed: %w\n%s", err, string(out))
	}

	return err
}

// copyToManager copies the files specified in the Arg slice to the configured
// location on the manager's machine.
func (b *Behaviour) copyToManager(*Job) error {
	_, wasStrSlice := b.Arg.([]string)
	if !wasStrSlice {
		return fmt.Errorf("%w: arg %s is type %T", errBehaviourArgNotStrSl, b.Arg, b.Arg)
	}

	// *** not yet implemented

	return nil
}

// Behaviours are a slice of Behaviour.
type Behaviours []*Behaviour

// Trigger calls Trigger on each constituent Behaviour, first all those for
// OnSuccess if success = true or OnFailure otherwise, then those for OnExit.
func (bs Behaviours) Trigger(success bool, j *Job) error {
	if len(bs) == 0 {
		return nil
	}

	var status BehaviourTrigger
	if success {
		status = OnSuccess
	} else {
		status = OnFailure
	}

	var merr *multierror.Error

	for _, b := range bs {
		err := b.Trigger(status, j)
		if err != nil {
			merr = multierror.Append(merr, err)
		}
	}

	status = OnExit
	for _, b := range bs {
		err := b.Trigger(status, j)
		if err != nil {
			merr = multierror.Append(merr, err)
		}
	}

	return merr.ErrorOrNil()
}

// RemovalRequested tells you if one of the behaviours is Remove.
func (bs Behaviours) RemovalRequested() bool {
	for _, b := range bs {
		if b.Do == Remove {
			return true
		}
	}

	return false
}

// withoutCleanups returns bs with every Cleanup and CleanupAll Behaviour
// removed, regardless of trigger, and nil if that leaves none.
//
// Those actions can only ever delete the wr-created working directory of a job
// with CwdMatters false (see Behaviour.cleanup), so on a cwd_matters job they
// are documented no-ops that must not be stored: keeping one would have wr
// advertise a deletion that will never happen, and it was such a stored no-op
// that deleted the wrong directory when ActualCwd got poisoned with Cwd.
func (bs Behaviours) withoutCleanups() Behaviours {
	kept := slices.DeleteFunc(slices.Clone(bs), func(b *Behaviour) bool {
		return b.Do == Cleanup || b.Do == CleanupAll
	})

	if len(kept) == 0 {
		return nil
	}

	return kept
}

// String provides a nice string representation of Behaviours for user
// interface display purposes. It takes the form of a JSON string that can
// be converted back to Behaviours using a BehavioursViaJSON for each key. The
// keys are "on_failure", "on_success", "on_failure|success" and "on_exit".
func (bs Behaviours) String() string {
	if len(bs) == 0 {
		return ""
	}

	bvjm := &bvjMapping{}
	for _, b := range bs {
		b.fillBVJM(bvjm)
	}

	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)

	err := encoder.Encode(bvjm)
	if err != nil {
		panic(fmt.Sprintf("Encoding a bvjm failed: %s", err))
	}

	return strings.TrimSpace(buffer.String())
}

// BehaviourViaJSON makes up BehavioursViaJSON. Each of these should only
// specify one of its properties.
type BehaviourViaJSON struct {
	Run           string   `json:"run,omitempty"`
	CopyToManager []string `json:"copy_to_manager,omitempty"`
	Cleanup       bool     `json:"cleanup,omitempty"`
	CleanupAll    bool     `json:"cleanup_all,omitempty"`
	Remove        bool     `json:"remove,omitempty"`
	Nothing       bool     `json:"nothing,omitempty"`
}

// Behaviour converts the friendly BehaviourViaJSON struct to real Behaviour.
func (bj BehaviourViaJSON) Behaviour(when BehaviourTrigger) *Behaviour {
	var (
		do  BehaviourAction
		arg any
	)

	switch {
	case bj.Run != "":
		do = Run
		arg = bj.Run
	case len(bj.CopyToManager) > 0:
		do = CopyToManager
		arg = bj.CopyToManager
	case bj.Cleanup:
		do = Cleanup
	case bj.CleanupAll:
		do = CleanupAll
	case bj.Remove:
		do = Remove
	default:
		do = Nothing
	}

	return &Behaviour{
		When: when,
		Do:   do,
		Arg:  arg,
	}
}

// BehavioursViaJSON is a slice of BehaviourViaJSON. It is a convenience to
// allow users to specify behaviours in a more natural way if they're trying to
// describe them in a JSON string. You'd have one of these per BehaviourTrigger.
type BehavioursViaJSON []BehaviourViaJSON

// Behaviours converts a BehavioursViaJSON to real Behaviours.
func (bjs BehavioursViaJSON) Behaviours(when BehaviourTrigger) Behaviours {
	bs := make(Behaviours, 0, len(bjs))
	for _, bj := range bjs {
		bs = append(bs, bj.Behaviour(when))
	}

	return bs
}

// bvjMapping struct is used by Behaviour*.String() to do its JSON conversion.
type bvjMapping struct {
	OnFailure BehavioursViaJSON `json:"on_failure,omitempty"`
	OnSuccess BehavioursViaJSON `json:"on_success,omitempty"`
	OnFS      BehavioursViaJSON `json:"on_failure|success,omitempty"`
	OnExit    BehavioursViaJSON `json:"on_exit,omitempty"`
}
