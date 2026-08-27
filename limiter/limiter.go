/*******************************************************************************
 * Copyright (c) 2019-2021, 2024-2025 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

package limiter

// This file contains the implementation of the main struct in the limiter
// package, the Limiter.

import (
	"context"
	"sync"
	"time"
)

// SetLimitCallback is provided to New(). Your function should take the name of
// a group and return the current limit for that group. If the group doesn't
// exist or has no limit, return -1. The idea is that you retrieve the limit for
// a group from some on-disk database, so you don't have to have all group
// limits in memory. (Limiter itself will clear out unused groups from its own
// memory.)
//
// Your function is never called while the Limiter holds its own lock, so it is
// allowed to be slow (which a database read can be), but it can therefore be
// called concurrently, and more than once for the same group.
type SetLimitCallback func(context.Context, string) *GroupData

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
func (l *Limiter) SetLimit(name string, data GroupData) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if g, set := l.groups[name]; set {
		g.setLimit(data.limit)
	} else {
		l.groups[name] = newGroup(name, data)
	}
}

// GetLimit tells you the limit currently set for the given group. If the group
// doesn't exist, returns -1.
func (l *Limiter) GetLimit(ctx context.Context, name string) *GroupData {
	data := NewCountGroupData(-1)

	l.withResolvedGroups(ctx, []string{name}, func(resolved map[string]*GroupData) {
		if group := l.vivifyGroup(name, resolved); group != nil {
			data = &group.GroupData
		}
	})

	return data
}

// GetLimits tells you the current limit of all currently set groups.
func (l *Limiter) GetLimits() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()

	limits := make(map[string]int, len(l.groups))

	for name, group := range l.groups {
		if group.IsCount() {
			limits[name] = int(group.limit)
		}
	}

	return limits
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
//
// If an optional wait duration is supplied, will wait for up to the given wait
// period for an increment of every group to be possible.
func (l *Limiter) Increment(ctx context.Context, groups []string, wait ...time.Duration) bool {
	wantWait := len(wait) == 1

	incremented, ch := l.attemptIncrement(ctx, groups, wantWait)
	if incremented {
		return true
	}

	if !wantWait {
		return false
	}

	limit := time.After(wait[0])

	for {
		select {
		case <-ch:
			incremented, ch = l.attemptIncrement(ctx, groups, true)
			if incremented {
				return true
			}

			continue
		case <-limit:
			return false
		}
	}
}

// attemptIncrement increments all the groups if possible, returning true. If
// not possible and registerOnFail is true, it registers a fresh decrement
// notification channel for the groups (under the same lock as the failed
// check) and returns it so the caller can wait on it; otherwise it returns a
// nil channel.
func (l *Limiter) attemptIncrement(ctx context.Context, groups []string, registerOnFail bool) (bool, chan bool) {
	var (
		incremented bool
		ch          chan bool
	)

	l.withResolvedGroups(ctx, groups, func(resolved map[string]*GroupData) {
		if l.checkGroups(groups, resolved) {
			l.incrementGroups(groups, resolved)

			incremented = true

			return
		}

		if registerOnFail {
			ch = make(chan bool, len(groups))
			l.registerGroupNotifications(groups, ch, resolved)
		}
	})

	return incremented, ch
}

// withResolvedGroups calls fn while holding mu, having first made sure that
// every name in groups is either already in memory or has had its limit
// resolved by the SetLimitCallback, passing fn those resolutions for
// vivifyGroup() to use. fn can therefore do everything it needs to under a
// single uninterrupted lock hold, without anything under that lock calling the
// callback.
//
// The callback is only ever called with mu released, because it typically reads
// an on-disk database, which can stall (see DEVELOPERS.md rule 1): mu is on the
// path of every Decrement(), so a stalled lookup of one group must not freeze
// the completion of jobs in every other group.
//
// Since a group can be forgotten (by Decrement() reaching 0, or RemoveLimit())
// while mu is released, this loops until it gets the lock with nothing left to
// resolve; a name that has gone missing again is resolved rather than treated
// as unlimited. It terminates because each iteration resolves at least one name
// that has not been resolved before, and never resolves a name twice.
func (l *Limiter) withResolvedGroups(ctx context.Context, groups []string, fn func(map[string]*GroupData)) {
	var resolved map[string]*GroupData

	for {
		unresolved := l.runWithGroups(groups, resolved, fn)
		if unresolved == nil {
			return
		}

		resolved = l.resolveGroups(ctx, unresolved, resolved)
	}
}

// runWithGroups takes mu and, if every name in groups is either in memory or
// already resolved, calls fn and returns nil. Otherwise it calls nothing and
// returns the names that still need to be resolved with mu released.
func (l *Limiter) runWithGroups(groups []string, resolved map[string]*GroupData,
	fn func(map[string]*GroupData),
) []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var unresolved []string

	for _, name := range groups {
		if _, exists := l.groups[name]; exists {
			continue
		}

		if _, done := resolved[name]; !done {
			unresolved = append(unresolved, name)
		}
	}

	if unresolved != nil {
		return unresolved
	}

	fn(resolved)

	return nil
}

// resolveGroups calls the SetLimitCallback for each unresolved name, adding
// what it learns to resolved (creating it if nil) and returning it. You must
// NOT hold mu when calling this.
func (l *Limiter) resolveGroups(ctx context.Context, unresolved []string,
	resolved map[string]*GroupData,
) map[string]*GroupData {
	if resolved == nil {
		resolved = make(map[string]*GroupData, len(unresolved))
	}

	for _, name := range unresolved {
		if _, done := resolved[name]; !done {
			resolved[name] = l.cb(ctx, name)
		}
	}

	return resolved
}

// checkGroups checks all the groups to see if they can be incremented. You must
// hold the mu.lock before calling this, and until after calling
// incrementGroups() if this returns true.
func (l *Limiter) checkGroups(groups []string, resolved map[string]*GroupData) bool {
	for _, name := range groups {
		group := l.vivifyGroup(name, resolved)
		if group != nil {
			if !group.canIncrement() {
				return false
			}
		}
	}

	return true
}

// incrementGroups increments all the groups without checking them. You must
// hold the mu.lock before calling this (and check first).
func (l *Limiter) incrementGroups(groups []string, resolved map[string]*GroupData) {
	for _, name := range groups {
		group := l.vivifyGroup(name, resolved)
		if group != nil {
			group.increment()
		}
	}
}

// vivifyGroup either returns a stored group or creates a new one based on the
// limit that withResolvedGroups() has already got from the SetLimitCallback.
// You must have the mu.Lock() before calling this. Can return nil if the
// callback didn't know about this group and returned a -1 limit.
//
// A group that is already in memory is returned as-is and never replaced with
// the resolved data: overwriting it would reset its current count to 0 and so
// break the limit it exists to enforce.
func (l *Limiter) vivifyGroup(name string, resolved map[string]*GroupData) *group {
	group, exists := l.groups[name]
	if exists {
		return group
	}

	if limit := resolved[name]; limit.IsValid() {
		group = newGroup(name, *limit)
		l.groups[name] = group
	}

	return group
}

// registerGroupNotifications passes the channel to each group to be notified of
// decrement() calls on them.
func (l *Limiter) registerGroupNotifications(groups []string, ch chan bool, resolved map[string]*GroupData) {
	for _, name := range groups {
		group := l.vivifyGroup(name, resolved)
		if group != nil {
			group.notifyDecrement(ch)
		}
	}
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
				delete(l.groups, group.name)
			}
		}
	}
}

// GetLowestLimit tells you the lowest limit currently set amongst the given
// groups. If none have a limit set, returns -1.
func (l *Limiter) GetLowestLimit(ctx context.Context, groups []string) int {
	lowest := -1

	l.withResolvedGroups(ctx, groups, func(resolved map[string]*GroupData) {
		for _, name := range groups {
			group := l.vivifyGroup(name, resolved)
			if group != nil && (lowest == -1 || int(group.limit) < lowest) {
				lowest = int(group.limit)
			}
		}
	})

	return lowest
}

// GetRemainingCapacity tells you how many times you could Increment() the given
// groups. If none have a limit set, returns -1.
func (l *Limiter) GetRemainingCapacity(ctx context.Context, groups []string) int {
	lowest := -1

	l.withResolvedGroups(ctx, groups, func(resolved map[string]*GroupData) {
		for _, name := range groups {
			group := l.vivifyGroup(name, resolved)
			if group == nil {
				continue
			}

			if capacity := group.capacity(); lowest == -1 || capacity < lowest {
				lowest = capacity
			}
		}
	})

	return lowest
}
