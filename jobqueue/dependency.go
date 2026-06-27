/*******************************************************************************
 * Copyright (c) 2017-2018 Genome Research Ltd.
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

// This file contains the dependency related code.

import (
	"encoding/json"
	"slices"
	"strings"
)

const neverSeenDepGroupDependencyPrefix = "depgroup-not-seen:"

func (d *Dependency) collectIncompleteJobKeys(
	db *db,
	seenDepGroups map[string]bool,
	jobKeys, waitingForDepGroups map[string]bool,
) error {
	keys, waiting, err := d.incompleteJobKeysWithSeen(db, seenDepGroups)
	if err != nil {
		return err
	}

	collectIncompleteJobKeys(keys, jobKeys, waitingForDepGroups)
	collectStrings(waiting, waitingForDepGroups)

	return nil
}

func collectStrings(values []string, set map[string]bool) {
	for _, value := range values {
		set[value] = true
	}
}

func (d *Dependency) incompleteDepGroupJobKeys(db *db, seenDepGroups map[string]bool) ([]string, []string, error) {
	keys, err := db.retrieveIncompleteJobKeysByDepGroup(d.DepGroup)
	if err != nil {
		return []string{}, []string{}, err
	}

	if len(keys) > 0 {
		return keys, nil, nil
	}

	if seenDepGroups[d.DepGroup] {
		return nil, nil, nil
	}

	return []string{neverSeenDepGroupDependencyKey(d.DepGroup)}, []string{d.DepGroup}, nil
}

func neverSeenDepGroupDependencyKey(depGroup string) string {
	return neverSeenDepGroupDependencyPrefix + depGroup
}

func (d *Dependency) incompleteEssenceJobKeys(db *db) ([]string, []string, error) {
	jobKey := d.Essence.Key()

	live, err := db.checkIfLive(jobKey)
	if err != nil {
		return []string{}, []string{}, err
	}

	if live {
		return []string{jobKey}, nil, nil
	}

	return []string{}, nil, nil
}

// Dependencies is a slice of *Dependency, for use in Job.Dependencies. It
// describes the jobs that must be complete before the Job you associate this
// with will start.
type Dependencies []*Dependency

// UnmarshalJSON accepts the public REST command dependency shape
// [{"cmd":"...","cwd":"..."}] as well as the historical struct-shaped JSON.
func (d *Dependencies) UnmarshalJSON(data []byte) error {
	var deps []dependencyViaJSON
	if err := json.Unmarshal(data, &deps); err != nil {
		return err
	}

	converted := make(Dependencies, 0, len(deps))
	for _, dep := range deps {
		converted = appendDependencyViaJSON(converted, dep)
	}

	*d = converted

	return nil
}

type dependencyViaJSON struct {
	Essence       *JobEssence `json:"essence"`
	LegacyEssence *JobEssence `json:"Essence"`
	Cmd           string      `json:"cmd"`
	Cwd           string      `json:"cwd"`
	DepGroup      string      `json:"dep_group"`
	LegacyDepGrp  string      `json:"DepGroup"`
}

func appendDependencyViaJSON(deps Dependencies, dep dependencyViaJSON) Dependencies {
	if dep.DepGroup != "" {
		return append(deps, NewDepGroupDependency(dep.DepGroup))
	}

	if dep.LegacyDepGrp != "" {
		return append(deps, NewDepGroupDependency(dep.LegacyDepGrp))
	}

	if dep.Cmd != "" || dep.Cwd != "" {
		return append(deps, NewEssenceDependency(dep.Cmd, dep.Cwd))
	}

	if dep.Essence != nil {
		return append(deps, &Dependency{Essence: dep.Essence})
	}

	if dep.LegacyEssence != nil {
		return append(deps, &Dependency{Essence: dep.LegacyEssence})
	}

	return deps
}

// incompleteJobKeys converts the constituent Dependency structs in to internal
// job keys that uniquely identify the jobs we are dependent upon. Note that if
// you have dependencies that are specified with DepGroups, then you should re-
// call this and update every time a new Job is added with one of our
// DepGroups() in its *Job.DepGroups. It will only return keys for jobs that
// are incomplete (they could have been Archive()d in the past if they are now
// being re-run).
func (d Dependencies) incompleteJobKeys(db *db) ([]string, []string, error) {
	seenDepGroups, err := db.depGroupsEverSeen(d.DepGroups())
	if err != nil {
		return []string{}, []string{}, err
	}

	jobKeys, waitingForDepGroups, err := d.incompleteJobKeysByDependency(db, seenDepGroups)
	if err != nil {
		return []string{}, []string{}, err
	}

	return sortedStringSet(jobKeys), sortedStringSet(waitingForDepGroups), nil
}

func sortedStringSet(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}

	slices.Sort(values)

	return values
}

func (d Dependencies) incompleteJobKeysByDependency(
	db *db,
	seenDepGroups map[string]bool,
) (map[string]bool, map[string]bool, error) {
	jobKeys := make(map[string]bool)
	waitingForDepGroups := make(map[string]bool)

	for _, dep := range d {
		if dep.DepGroup == "" {
			keys, waiting, err := dep.incompleteJobKeys(db)
			if err != nil {
				return nil, nil, err
			}

			collectIncompleteJobKeys(keys, jobKeys, waitingForDepGroups)
			collectStrings(waiting, waitingForDepGroups)

			continue
		}

		if err := dep.collectIncompleteJobKeys(db, seenDepGroups, jobKeys, waitingForDepGroups); err != nil {
			return nil, nil, err
		}
	}

	return jobKeys, waitingForDepGroups, nil
}

// DepGroups returns all the DepGroups of our constituent Dependency structs.
func (d Dependencies) DepGroups() []string {
	var depGroups []string

	for _, dep := range d {
		if dep.DepGroup != "" {
			depGroups = append(depGroups, dep.DepGroup)
		}
	}

	return depGroups
}

// Stringify converts our constituent Dependency structs in to a slice of
// strings, each of which could be JobEssence or DepGroup based.
func (d Dependencies) Stringify() []string {
	var strings []string

	for _, dep := range d {
		if dep.DepGroup != "" {
			strings = append(strings, dep.DepGroup)
		} else if dep.Essence != nil {
			strings = append(strings, dep.Essence.Stringify())
		}
	}

	return strings
}

// Dependency is a struct that describes a Job purely in terms of a JobEssence,
// or in terms of a Job's DepGroup, for use in Dependencies. If DepGroup is
// specified, then Essence is ignored.
type Dependency struct {
	Essence  *JobEssence
	DepGroup string
}

// incompleteJobKeys calculates the job keys that this dependency refers to. For
// a Dependency made with Essence, you will get a single key which will be the
// same key you'd get from *Job.key() on a Job made with the same essence.
// For a Dependency made with a DepGroup, you will get the *Job.key()s of all
// the jobs in the queue and database that have that DepGroup in their
// DepGroups. You will only get keys for jobs that are currently in the queue.
func (d *Dependency) incompleteJobKeys(db *db) ([]string, []string, error) {
	seenDepGroups := make(map[string]bool)

	if d.DepGroup != "" {
		seen, err := db.depGroupEverSeen(d.DepGroup)
		if err != nil {
			return []string{}, []string{}, err
		}

		seenDepGroups[d.DepGroup] = seen
	}

	return d.incompleteJobKeysWithSeen(db, seenDepGroups)
}

func (d *Dependency) incompleteJobKeysWithSeen(db *db, seenDepGroups map[string]bool) ([]string, []string, error) {
	if d.DepGroup != "" {
		return d.incompleteDepGroupJobKeys(db, seenDepGroups)
	}

	if d.Essence != nil {
		return d.incompleteEssenceJobKeys(db)
	}

	return []string{}, nil, nil
}

// NewEssenceDependency makes it a little easier to make a new *Dependency based
// on Cmd+Cwd, for use in NewDependencies(). Leave cwd as an empty string if the
// job you are describing does not have CwdMatters true.
func NewEssenceDependency(cmd string, cwd string) *Dependency {
	return &Dependency{
		Essence: &JobEssence{Cmd: cmd, Cwd: cwd},
	}
}

// NewDepGroupDependency makes it a little easier to make a new *Dependency
// based on a dep group, for use in NewDependencies().
func NewDepGroupDependency(depgroup string) *Dependency {
	return &Dependency{
		DepGroup: depgroup,
	}
}

func collectIncompleteJobKeys(keys []string, jobKeys, waitingForDepGroups map[string]bool) {
	for _, key := range keys {
		jobKeys[key] = true
		if depGroup, ok := neverSeenDepGroupFromDependencyKey(key); ok {
			waitingForDepGroups[depGroup] = true
		}
	}
}

func neverSeenDepGroupFromDependencyKey(key string) (string, bool) {
	return strings.CutPrefix(key, neverSeenDepGroupDependencyPrefix)
}
