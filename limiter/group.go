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

// group struct describes an individual limit group.
type group struct {
	name    string
	limit   uint
	current uint
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
	g.limit = limit
}

// canIncrement tells you if the current count of this group is less than the
// limit.
func (g *group) canIncrement() bool {
	return g.current < g.limit
}

// increment increases the current count of this group. You must call
// canIncrement() first to make sure you won't go over the limit (and hold a
// lock over the 2 calls to avoid a race condition).
func (g *group) increment() {
	g.current++
}

// decrement decreases the current count of this group. Returns true if the
// current count indicates the group is unused.
func (g *group) decrement() bool {
	// (decrementing a uint under 0 makes it a large positive value, so we must
	// check first)
	if g.current == 0 {
		return true
	}
	g.current--
	return g.current < 1
}

// capacity tells you how many more increments you could do on this group before
// breaching the limit.
func (g *group) capacity() int {
	if g.current >= g.limit {
		return 0
	}
	return int(g.limit - g.current)
}
