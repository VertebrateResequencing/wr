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

// This file contains the implementation of the group stuct.

import (
	"sync"
)

// group struct describes an individual limit group.
type group struct {
	name    string
	limit   uint
	current uint
	sync.RWMutex
}

// newGroup creates a new group.
func newGroup(name string, limit uint) *group {
	return &group{
		name:  name,
		limit: limit,
	}
}

// setLimit updates the group's limit.
func (g *group) setLimit(limit uint) {
	g.Lock()
	defer g.Unlock()
	g.limit = limit
}

// canIncrement tells you if the current count of this group is less than the
// limit.
func (g *group) canIncrement() bool {
	g.RLock()
	defer g.RUnlock()
	return g.current < g.limit
}

// increment increases the count of this group, but errors if you try to
// increment beyond the limit (check canIncrement() first, but you will have to
// guard against the race condition).
func (g *group) increment() error {
	g.Lock()
	defer g.Unlock()
	if g.current >= g.limit {
		return Error{Group: g.name, Op: "increment", Err: ErrAtLimit}
	}
	g.current++
	return nil
}

// canDecrement tells you if the current count of this group is greater than 0.
func (g *group) canDecrement() bool {
	g.RLock()
	defer g.RUnlock()
	return g.current > 0
}

// decrement decreases the count of this group, but errors if you try to
// decrement less than zero (check canDecrement() first, but you will have to
// guard against the race condition).
func (g *group) decrement() error {
	g.Lock()
	defer g.Unlock()
	if g.current <= 0 {
		return Error{Group: g.name, Op: "decrement", Err: ErrNotIncremented}
	}
	g.current--
	return nil
}
