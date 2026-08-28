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
	"strings"

	"github.com/hashicorp/go-multierror"
)

// sentinel errors for behaviour handling.
var (
	errBehaviourInvalidStatus = errors.New("invalid status")
	errBehaviourArgNotStr     = errors.New("arg is not a string")
	errBehaviourArgNotStrSl   = errors.New("arg is not a []string")
)

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

	// it's the parent of ActualCwd that is the unique dir that got created
	// that should be deleted; it contains tmp, cwd and possibly mount cache
	// dirs (that we don't want to delete).
	workSpace := filepath.Dir(j.ActualCwd)

	// only a dir strictly inside Cwd can be a dir that wr created; if ActualCwd
	// says otherwise (eg. it is stale after a `wr mod --cwd`, or was poisoned
	// with Cwd itself), deleting it would destroy directories that belong to the
	// user, so we delete nothing and report why.
	if !dirIsBelow(workSpace, j.Cwd) {
		return fmt.Errorf("%w: refusing to clean up %s, which is not inside the job's cwd %s",
			errNotBelowBaseDir, workSpace, j.Cwd)
	}

	if err := cleanupWorkSpace(j, workSpace); err != nil {
		return err
	}

	// delete any empty parent directories up to Cwd
	return rmEmptyDirs(workSpace, j.Cwd)
}

// cleanupWorkSpace deletes the contents of the Job's workspace dir. If the Job
// used mounts it takes care not to delete the cache dirs or mounted dirs;
// otherwise it just deletes everything in one go.
func cleanupWorkSpace(j *Job, workSpace string) error {
	if len(j.MountConfigs) == 0 {
		return removeAllManaged(workSpace)
	}

	keepDirs, keepActualCwd := mountDirsToKeep(j.MountConfigs)

	if !keepActualCwd {
		if err := removeActualCwd(j.ActualCwd, keepDirs); err != nil {
			return err
		}
	}

	return removeWorkSpaceExtras(workSpace)
}

// mountDirsToKeep works out, from a Job's MountConfigs, which relative dirs to
// keep, and whether the ActualCwd itself must be kept.
func mountDirsToKeep(mcs MountConfigs) (keepDirs []string, keepActualCwd bool) {
	for _, mc := range mcs {
		if mc.Mount == "" {
			return nil, true
		}

		if !filepath.IsAbs(mc.Mount) {
			keepDirs = append(keepDirs, mc.Mount)
		}
	}

	return keepDirs, false
}

// removeWorkSpaceExtras deletes everything inside workSpace except for cwd and
// the cache dirs, incase a job.Cmd did something like `touch ../foo`.
func removeWorkSpaceExtras(workSpace string) error {
	entries, err := os.ReadDir(workSpace)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.Name() == "cwd" || strings.HasPrefix(entry.Name(), ".muxfys") {
			continue
		}

		if err = removeAllManaged(filepath.Join(workSpace, entry.Name())); err != nil {
			return err
		}
	}

	return nil
}

// removeActualCwd deletes the Job's ActualCwd, keeping the given relative dirs
// if any were specified.
func removeActualCwd(actualCwd string, keepDirs []string) error {
	if len(keepDirs) > 0 {
		return removeAllExcept(actualCwd, keepDirs)
	}

	return removeAllManaged(actualCwd)
}

// run simply runs the given command from Job's actual cwd.
func (b *Behaviour) run(j *Job) error {
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
