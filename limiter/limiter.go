/*******************************************************************************
 * Copyright (c) 2019-2021, 2024-2026 Genome Research Ltd.
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

// resolutionBufSize is how many groups a call can resolve using a buffer on
// its own stack; calls with more groups than this allocate one.
const resolutionBufSize = 8

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

// resolution is what lockWithResolvedGroups() learnt about one of the groups
// it was given: the group itself if it was in memory when the lock was taken,
// otherwise (if done) what the SetLimitCallback said its limit is.
type resolution struct {
	group  *group
	data   *GroupData
	wanted bool
	done   bool
}

// Limiter struct is used to limit usage of groups.
type Limiter struct {
	cb     SetLimitCallback
	groups map[string]*group
	mu     sync.Mutex

	// limitChanges counts SetLimit() and RemoveLimit() calls, so that
	// lockWithResolvedGroups() can tell if a limit changed while it was looking
	// limits up.
	limitChanges uint64
}

// New creates a new Limiter.
func New(cb SetLimitCallback) *Limiter {
	return &Limiter{
		cb:     cb,
		groups: make(map[string]*group),
	}
}

// SetLimit creates or updates a group with the given limit. A group that is
// already counted, with or without a limit, keeps its count.
func (l *Limiter) SetLimit(name string, data GroupData) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.limitChanges++

	if g, set := l.groups[name]; set {
		g.setLimit(data.limit)
	} else {
		l.groups[name] = newGroup(name, data)
	}
}

// GetLimit tells you the limit currently set for the given group. If the group
// doesn't exist, returns -1.
func (l *Limiter) GetLimit(ctx context.Context, name string) *GroupData {
	var buf [resolutionBufSize]resolution

	resolved := l.lockWithResolvedGroups(ctx, []string{name}, buf[:])
	defer l.mu.Unlock()

	if group := l.vivifyGroup(name, &resolved[0]); group != nil && group.hasLimit() {
		return &group.GroupData
	}

	return NewCountGroupData(-1)
}

// GetLimits tells you the current limit of all currently set groups.
func (l *Limiter) GetLimits() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()

	limits := make(map[string]int, len(l.groups))

	for name, group := range l.groups {
		if group.IsCount() && group.hasLimit() {
			limits[name] = int(group.limit)
		}
	}

	return limits
}

// RemoveLimit removes the limit of the given group. If your callback also
// begins returning -1 for this group, the group becomes unlimited.
//
// A simple run limit group that is in use stays in memory without a limit,
// still counting its use, so that a limit set again with SetLimit() applies to
// the use already counted. Otherwise the group is removed from memory.
func (l *Limiter) RemoveLimit(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.limitChanges++

	if g, set := l.groups[name]; set && g.IsCount() && g.current > 0 {
		g.removeLimit()

		return
	}

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
// A group the callback knows no limit for can always be Increment()ed, but is
// still counted, so that a limit later given to it with SetLimit() applies to
// the count it already has.
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
	var buf [resolutionBufSize]resolution

	resolved := l.lockWithResolvedGroups(ctx, groups, buf[:])
	defer l.mu.Unlock()

	if l.checkGroups(groups, resolved) {
		l.incrementGroups(groups, resolved)

		return true, nil
	}

	if !registerOnFail {
		return false, nil
	}

	ch := make(chan bool, len(groups))
	l.registerGroupNotifications(groups, ch, resolved)

	return false, ch
}

// lockWithResolvedGroups takes mu and returns still holding it (so you must
// unlock it), having first made sure that every name in groups is either
// already in memory or has had its limit resolved by the SetLimitCallback. The
// returned slice holds those resolutions in the same order as groups, for
// vivifyGroup() to use. You can therefore do everything you need to under a
// single uninterrupted lock hold, without anything under that lock calling the
// callback.
//
// buf, which must be zeroed, is used to hold the resolutions if it is long
// enough, so that callers can pass a fresh array on their own stack and the
// common case of a few groups does not allocate.
//
// The callback is only ever called with mu released, because it typically reads
// an on-disk database, which can stall (see DEVELOPERS.md rule 1): mu is on the
// path of every Decrement(), so a stalled lookup of one group must not freeze
// the completion of jobs in every other group.
//
// Since a group can be forgotten (by Decrement() reaching 0, or RemoveLimit())
// while mu is released, this loops until it gets the lock with nothing left to
// resolve; a name that has gone missing again is resolved rather than treated
// as unlimited.
//
// A limit can also be changed while mu is released, after the callback has read
// the old one: a RemoveLimit() of a group not in memory has nothing to remove,
// so creating the group from the old limit would enforce it until the limit was
// next changed. So if any limit changed while the callback was being called,
// every resolution is discarded and done again. (Any change counts, not only one
// of these groups, so that nothing is kept per group name; limits change
// rarely, so the extra lookups are rare.)
//
// It terminates because, unless a limit changed, each iteration resolves at
// least one entry of groups that has not been resolved before, and never
// resolves an entry twice; it only repeats lookups while limits keep changing
// during them.
func (l *Limiter) lockWithResolvedGroups(ctx context.Context, groups []string,
	buf []resolution,
) []resolution {
	resolved := buf
	if len(groups) > len(buf) {
		resolved = make([]resolution, len(groups))
	}

	resolved = resolved[:len(groups)]

	l.mu.Lock()

	for l.markUnresolved(groups, resolved) {
		changes := l.limitChanges

		l.mu.Unlock()
		l.resolveGroups(ctx, groups, resolved)
		l.mu.Lock()

		if l.limitChanges != changes {
			clear(resolved)
		}
	}

	return resolved
}

// markUnresolved notes each name in groups that is in memory, and marks as
// wanted the resolution of each that is neither in memory nor already
// resolved, returning true if there were any of the latter. You must hold mu
// when calling this.
func (l *Limiter) markUnresolved(groups []string, resolved []resolution) bool {
	missing := false

	for i, name := range groups {
		resolved[i].group = l.groups[name]

		if resolved[i].group != nil || resolved[i].done {
			continue
		}

		resolved[i].wanted = true
		missing = true
	}

	return missing
}

// resolveGroups calls the SetLimitCallback for each wanted resolution. You must
// NOT hold mu when calling this.
func (l *Limiter) resolveGroups(ctx context.Context, groups []string, resolved []resolution) {
	for i := range resolved {
		if resolved[i].wanted {
			resolved[i] = resolution{data: l.cb(ctx, groups[i]), done: true}
		}
	}
}

// checkGroups checks all the groups to see if they can be incremented. You must
// hold the mu.lock before calling this, and until after calling
// incrementGroups() if this returns true.
func (l *Limiter) checkGroups(groups []string, resolved []resolution) bool {
	for i, name := range groups {
		group := l.vivifyGroup(name, &resolved[i])
		if group != nil {
			if !group.canIncrement() {
				return false
			}
		}
	}

	return true
}

// incrementGroups increments all the groups without checking them, creating
// an unlimited group for each that has no limit. You must hold the mu.lock
// before calling this (and check first).
//
// Unlimited groups are only created here, never by vivifyGroup(), so that only
// groups something is counted against stay in memory: Decrement() forgets them
// again when their count returns to 0.
func (l *Limiter) incrementGroups(groups []string, resolved []resolution) {
	for i, name := range groups {
		group := l.vivifyGroup(name, &resolved[i])
		if group == nil {
			group = newUnlimitedGroup(name)
			l.groups[name] = group
			resolved[i].group = group
		}

		group.increment()
	}
}

// vivifyGroup either returns a stored group or creates a new one based on the
// limit that lockWithResolvedGroups() has already got from the
// SetLimitCallback, noting it in r for subsequent calls under the same lock
// hold. You must have held mu since the lockWithResolvedGroups() call that
// returned r. Can return nil if the callback didn't know about this group and
// returned a -1 limit, and the group is not already counted in memory.
//
// A group that is already in memory is returned as-is and never replaced with
// the resolved data: overwriting it would reset its current count to 0 and so
// break the limit it exists to enforce. (It can be in memory without r knowing
// if groups named it more than once.)
func (l *Limiter) vivifyGroup(name string, r *resolution) *group {
	if r.group != nil {
		return r.group
	}

	group, exists := l.groups[name]
	if !exists && r.data.IsValid() {
		group = newGroup(name, *r.data)
		l.groups[name] = group
	}

	r.group = group

	return group
}

// registerGroupNotifications passes the channel to each group to be notified of
// decrement() calls on them.
func (l *Limiter) registerGroupNotifications(groups []string, ch chan bool, resolved []resolution) {
	for i, name := range groups {
		group := l.vivifyGroup(name, &resolved[i])
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
// ignored. A group without a limit is counted like any other.
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
	var buf [resolutionBufSize]resolution

	resolved := l.lockWithResolvedGroups(ctx, groups, buf[:])
	defer l.mu.Unlock()

	lowest := -1

	for i, name := range groups {
		group := l.vivifyGroup(name, &resolved[i])
		if group != nil && group.hasLimit() && (lowest == -1 || int(group.limit) < lowest) {
			lowest = int(group.limit)
		}
	}

	return lowest
}

// GetRemainingCapacity tells you how many times you could Increment() the given
// groups. If none have a limit set, returns -1.
func (l *Limiter) GetRemainingCapacity(ctx context.Context, groups []string) int {
	var buf [resolutionBufSize]resolution

	resolved := l.lockWithResolvedGroups(ctx, groups, buf[:])
	defer l.mu.Unlock()

	lowest := -1

	for i, name := range groups {
		group := l.vivifyGroup(name, &resolved[i])
		if group == nil || !group.hasLimit() {
			continue
		}

		if capacity := group.capacity(); lowest == -1 || capacity < lowest {
			lowest = capacity
		}
	}

	return lowest
}
