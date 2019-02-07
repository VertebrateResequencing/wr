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

// Limiter struct is used to limit usage of groups.
type Limiter struct {
	groups map[string]*group
	mu     sync.RWMutex
}

// New creates a new Limiter.
func New() *Limiter {
	return &Limiter{
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

// Increment sees if it would be possible to increment the count of every
// supplied group, without making any of them go over their limit.
//
// If a group name is supplied that hasn't previously been defined via
// SetLimit(), it is treated as if it had an infinite limit.
//
// If possible, the group counts are actually incremented and this returns
// true. If not possible, no group counts are altered and this returns false.
func (l *Limiter) Increment(groups []string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var gs []*group
	for _, name := range groups {
		if group, exists := l.groups[name]; exists {
			if group.canIncrement() {
				gs = append(gs, group)
			} else {
				return false
			}
		}
	}

	for _, group := range gs {
		group.increment() // *** currently ignoring "impossible" error
	}

	return true
}

// Decrement decrements the count of every supplied group.
//
// If a decrement of a group would make the count negative, an error is returned
// and no group counts will have been altered.
func (l *Limiter) Decrement(groups []string) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var gs []*group
	for _, name := range groups {
		if group, exists := l.groups[name]; exists {
			if group.canDecrement() {
				gs = append(gs, group)
			} else {
				return Error{Group: name, Op: "Decrement", Err: ErrNotIncremented}
			}
		}
	}

	for _, group := range gs {
		group.decrement() // *** currently ignoring "impossible" error
	}

	return nil
}
