// Copyright © 2016-2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package jobqueue

// This file contains the dependency related code.

// Dependencies is a slice of *Dependency, for use in Job.Dependencies. It
// describes the jobs that must be complete before the Job you associate this
// with will start.
type Dependencies []*Dependency

// incompleteJobKeys converts the constituent Dependency structs in to internal
// job keys that uniquely identify the jobs we are dependent upon. Note that if
// you have dependencies that are specified with DepGroups, then you should re-
// call this and update every time a new Job is added with with one of our
// DepGroups() in its *Job.DepGroups. It will only return keys for jobs that
// are incomplete (they could have been Archive()d in the past if they are now
// being re-run).
func (d Dependencies) incompleteJobKeys(db *db) ([]string, error) {
	// we initially store in a map to avoid duplicates
	jobKeys := make(map[string]bool)
	for _, dep := range d {
		keys, err := dep.incompleteJobKeys(db)
		if err != nil {
			return []string{}, err
		}
		for _, key := range keys {
			jobKeys[key] = true
		}
	}

	keys := make([]string, len(jobKeys))
	i := 0
	for key := range jobKeys {
		keys[i] = key
		i++
	}

	return keys, nil
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
func (d *Dependency) incompleteJobKeys(db *db) ([]string, error) {
	if d.DepGroup != "" {
		keys, err := db.retrieveIncompleteJobKeysByDepGroup(d.DepGroup)
		return keys, err
	}
	if d.Essence != nil {
		jobKey := d.Essence.Key()
		live, err := db.checkIfLive(jobKey)
		if err != nil {
			return []string{}, err
		}
		if live {
			return []string{jobKey}, nil
		}
	}
	return []string{}, nil
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
