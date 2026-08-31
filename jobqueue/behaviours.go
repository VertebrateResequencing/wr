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
	"os/exec"
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

// cleanupProvenHook, when set, is called between the cleanup path proving which
// directory it is entitled to delete and it deleting anything, so that a test can
// swap something in during that window. It is nil in production.
var cleanupProvenHook func() //nolint:gochecknoglobals

// runResolvedHook and runProvenHook, when set, are called in the two windows a
// `run` Behaviour has to survive, so that a test can swap something in during
// them; both are nil in production.
//
// runResolvedHook is called once the directory the command will run in has been
// proven and before it is opened, since the open resolves the proven path again.
// runProvenHook is called once it is open and before the command starts, which is
// when exec.Cmd resolves the Dir name it was given.
//
//nolint:gochecknoglobals // test hooks into two moments that cannot be reached otherwise
var (
	runResolvedHook func()
	runProvenHook   func()
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
// BehaviourTrigger, against the Job as it is now.
func (b *Behaviour) Trigger(status BehaviourTrigger, j *Job) error {
	return b.trigger(status, j.workSpaceSnapshot())
}

// trigger is Trigger against a snapshot of the Job's state taken once, rather
// than against the Job itself; see Behaviours.trigger for why the snapshot is
// shared by a whole set.
func (b *Behaviour) trigger(status BehaviourTrigger, ws jobWorkSpaceSnapshot) error {
	if b.When&status == 0 {
		return nil
	}

	switch b.Do {
	case CleanupAll:
		return b.cleanup(ws, true)
	case Cleanup:
		return b.cleanup(ws, false)
	case Run:
		return b.run(ws)
	case CopyToManager:
		return b.copyToManager()
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
//
// Which dirs those are, and which of them the Job's live mounts and caches need
// kept, is decided entirely by resolveWorkSpace. A nil workspace means wr created
// nothing here that it may delete.
func (b *Behaviour) cleanup(snap jobWorkSpaceSnapshot, _ bool) error {
	ws, err := snap.resolveWorkSpace()
	if err != nil || ws == nil {
		return err
	}
	defer ws.Close()

	return ws.cleanup()
}

// run runs the given command in the directory the Job's Cmd ran in.
//
// Which directory that is comes from resolveRunDir - the same resolution that
// decides what cleanup may delete, since it is the same question about the same
// two fields. A Job whose directory cannot be shown to be its own runs nothing at
// all: the command is the user's and can do anything they can.
//
// The directory comes back held open, and cmd.Dir names that handle where the
// platform allows it to be named, so the command starts in the directory that was
// proven rather than in whatever its path resolves to by then; see runDir.
func (b *Behaviour) run(snap jobWorkSpaceSnapshot) error {
	bc, wasStr := b.Arg.(string)
	if !wasStr {
		return fmt.Errorf("%w: arg %s is type %T", errBehaviourArgNotStr, b.Arg, b.Arg)
	}

	dir, err := snap.resolveRunDir()
	if err != nil {
		return fmt.Errorf("run behaviour refused: %w", err)
	}
	defer dir.Close()

	if runProvenHook != nil {
		runProvenHook()
	}

	if strings.Contains(bc, " | ") {
		bc = "set -o pipefail; " + bc
	}
	// *** hardcoding bash here, when we could in theory have client.Execute()
	// pass shell in? And yes, we're allowing user to run absolutely any command
	// they like, but that is the very nature of this app. This runs as them,
	// so can do whatever they can do...
	cmd := exec.CommandContext(context.Background(), "/bin/bash", "-c", bc)
	cmd.Dir = dir.execDir()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run behaviour failed: %w\n%s", err, string(out))
	}

	return err
}

// copyToManager copies the files specified in the Arg slice to the configured
// location on the manager's machine.
func (b *Behaviour) copyToManager() error {
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
//
// The whole set shares ONE snapshot of the Job, rather than each behaviour
// reading the Job again: a `--on_failure cleanup --on_exit run` pair is one
// decision about one directory, and re-reading between them lets the two act on
// different ones. Nothing downstream reads the Job again while it walks the
// filesystem deciding what it may delete.
func (bs Behaviours) Trigger(success bool, j *Job) error {
	// answered before the snapshot, not after, because most Jobs have no
	// behaviours at all and this is on the path of every Job that runs: the
	// snapshot costs the Job's lock and the hash Key() makes of its Cmd and
	// mounts.
	if len(bs) == 0 {
		return nil
	}

	ws := j.workSpaceSnapshot()

	var status BehaviourTrigger
	if success {
		status = OnSuccess
	} else {
		status = OnFailure
	}

	var merr *multierror.Error

	for _, b := range bs {
		err := b.trigger(status, ws)
		if err != nil {
			merr = multierror.Append(merr, err)
		}
	}

	status = OnExit
	for _, b := range bs {
		err := b.trigger(status, ws)
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
// with CwdMatters false (see Behaviour.cleanup), so on a cwd_matters job they are
// no-ops that must not be stored: keeping one would have wr advertise a deletion
// that will never happen.
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
