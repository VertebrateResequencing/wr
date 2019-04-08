// Copyright © 2019 Genome Research Limited
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

package limiter

// This file contains the implementation of the main struct in the limiter
// package, the Limiter.

import (
	"sync"
)

// SetLimitCallback is provided to New(). Your function should take the name of
// a group and return the current limit for that group. If the group doesn't
// exist or has no limit, return -1. The idea is that you retrieve the limit for
// a group from some on-disk database, so you don't have to have all group
// limits in memory. (Limiter itself will clear out unused groups from its own
// memory.)
type SetLimitCallback func(name string) int

// Limiter struct is used to limit usage of groups.
type Limiter struct {
	cb     SetLimitCallback
	groups map[string]*group
	mu     sync.Mutex
}

// New creates a new Limiter.
func New(cb SetLimitCallback) *Limiter {
	return &Limiter{
		cb:     cb,
		groups: make(map[string]*group),
	}
}

// SetLimit creates or updates a group with the given limit.
func (l *Limiter) SetLimit(name string, limit uint) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if g, set := l.groups[name]; set {
		g.setLimit(limit)
	} else {
		l.groups[name] = newGroup(name, limit)
	}
}

// RemoveLimit removes the given group from memory. If your callback also begins
// returning -1 for this group, the group effectively becomes unlimited.
func (l *Limiter) RemoveLimit(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.groups, name)
}

// Increment sees if it would be possible to increment the count of every
// supplied group, without making any of them go over their limit.
//
// If this is the first time we're seeing a group name, or a Decrement() call
// has made us forget about that group, the callback provided to New() will be
// called with the name, and the returned value will be used to create a new
// group with that limit and initial count of 0 (which will become 1 if this
// returns true). Groups with a limit of 0 will not be able to be Increment()ed.
//
// If possible, the group counts are actually incremented and this returns
// true. If not possible, no group counts are altered and this returns false.
func (l *Limiter) Increment(groups []string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	var gs []*group
	for _, name := range groups {
		group := l.vivifyGroup(name)
		if group != nil {
			// contrary to the strict wording of the docs above, we increment
			// everything, and then decrement them if 1 fails to increment.
			if group.increment() {
				gs = append(gs, group)
			} else {
				for _, group := range gs {
					group.decrement()
				}
				return false
			}
		}
	}

	return true
}

// vivifyGroup either returns a stored group or creates a new one based on the
// results of calling the SetLimitCallback. You must have the mu.Lock() before
// calling this. Can return nil if the callback doesn't know about this group
// and returns a -1 limit.
func (l *Limiter) vivifyGroup(name string) *group {
	group, exists := l.groups[name]
	if !exists {
		if limit := l.cb(name); limit >= 0 {
			group = newGroup(name, uint(limit))
			l.groups[name] = group
		}
	}
	return group
}

// Decrement decrements the count of every supplied group.
//
// To save memory, if a group reaches a count of 0, it is forgotten.
//
// If a group isn't known about (because it was never previously Increment()ed,
// or was previously Decrement()ed to 0 and forgotten about), it is silently
// ignored.
func (l *Limiter) Decrement(groups []string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, name := range groups {
		if group, exists := l.groups[name]; exists {
			if group.decrement() {
				if !group.canDecrement() {
					delete(l.groups, group.name)
				}
			}
		}
	}
}

// GetLowestLimit tells you the lowest limit currently set amongst the given
// groups. If none have a limit set, returns -1.
func (l *Limiter) GetLowestLimit(groups []string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	lowest := -1
	for _, name := range groups {
		group := l.vivifyGroup(name)
		if group != nil && (lowest == -1 || int(group.limit) < lowest) {
			lowest = int(group.limit)
		}
	}
	return lowest
}
