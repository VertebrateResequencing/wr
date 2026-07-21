/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
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

import (
	"maps"
	"sync"
)

// repGroupCountsSubscriber represents one connected status web UI client. On its
// first drain it receives the seed (the fresh-connect filtered view built by
// liveSeedLocked: RepGroups with >=1 live job, their live + complete counts, no
// deleted, and terminal-only RepGroups omitted), captured atomically at subscribe
// time; thereafter it receives only the RepGroups that change, tracked in its
// dirty set. The wake channel is a buffered, edge-triggered signal so applying a
// transition never blocks the queue-mutation path.
type repGroupCountsSubscriber struct {
	seed  map[string]map[JobState]int
	dirty map[string]struct{}
	wake  chan struct{}
}

// repGroupCounts holds slim absolute per-RepGroup job-state counts for the web
// UI status bars. The special group statusAllRepGroups ("+all+") aggregates the
// live (incomplete) states across all RepGroups. It is maintained live from
// queue transitions only and is NEVER seeded from history: a manager restart
// yields an empty counter that fills from live transitions, so web-UI aggregate
// accuracy is v0.36.5 quality (flicker / overcount under high update rates
// accepted) but startup never scans completed history.
//
// wholeMap returns the whole current in-memory map INCLUDING terminal
// (complete/deleted) states and terminal-only RepGroups; it backs the +all+ live
// aggregate and the live per-RepGroup push path (drain of dirty RepGroups). The
// fresh-connect SEED delivered to a newly-subscribing client is instead the
// filtered view built by liveSeedLocked (RepGroups with >=1 live job only, no
// deleted, terminal-only RepGroups omitted), so a page refresh does not re-show
// completed-only work (bugfix 260721-1, restoring 260626-2 / 260716-1).
//
// Lock discipline: mu is a strict LEAF. While it is held, no other lock may be
// acquired. Callers that mutate the queue (the change callback and the TTR
// callback) acquire mu LAST in the order queue.mutex -> job -> repGroupCounts.mu,
// so mu can never be involved in a lock-order inversion.
type repGroupCounts struct {
	mu     sync.Mutex
	counts map[string]map[JobState]int
	subs   map[*repGroupCountsSubscriber]struct{}
}

// newRepGroupCounts creates an empty counter.
func newRepGroupCounts() *repGroupCounts {
	return &repGroupCounts{
		counts: make(map[string]map[JobState]int),
		subs:   make(map[*repGroupCountsSubscriber]struct{}),
	}
}

// applyTransitions atomically applies all count contributions emitted by one
// queue change to the absolute map and the statusAllRepGroups live aggregate,
// then marks the affected RepGroups dirty for every subscriber and signals them.
// Holding the leaf lock across the whole batch prevents a subscriber observing
// only half of a multi-contribution move (e.g. a resurrected job whose RepGroup
// changed).
func (c *repGroupCounts) applyTransitions(transitions []countContribution) {
	if len(transitions) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, transition := range transitions {
		if !rgcValidContribution(transition.repGroup, transition.n) {
			continue
		}

		c.applyToRepGroupLocked(transition.repGroup, transition.from, transition.to, transition.n)
		c.applyToRepGroupLocked(statusAllRepGroups, rgcAggregateState(transition.from),
			rgcAggregateState(transition.to), transition.n)

		c.markDirtyLocked(transition.repGroup)
		c.markDirtyLocked(statusAllRepGroups)
	}
}

// rgcValidContribution reports whether a contribution should be applied: a
// positive count for a real RepGroup that is not the reserved aggregate name.
func rgcValidContribution(repGroup string, n int) bool {
	return n > 0 && repGroup != "" && repGroup != statusAllRepGroups
}

// rgcAggregateState maps a per-RepGroup state to the state it contributes to the
// statusAllRepGroups live aggregate. Terminal states (complete, deleted) and the
// empty state contribute nothing, because "+all+" counts only live jobs.
func rgcAggregateState(state JobState) JobState {
	switch state {
	case JobStateComplete, JobStateDeleted:
		return ""
	default:
		return state
	}
}

// applyToRepGroupLocked applies a single from->to move of n jobs to one
// RepGroup's counts. Counts are clamped at zero so a lost or duplicated
// decrement can never drive a count negative. Must be called with mu held.
func (c *repGroupCounts) applyToRepGroupLocked(repGroup string, from, to JobState, n int) {
	stateCounts, ok := c.counts[repGroup]
	if !ok {
		stateCounts = make(map[JobState]int)
		c.counts[repGroup] = stateCounts
	}

	if from != "" {
		stateCounts[from] -= n
		if stateCounts[from] <= 0 {
			delete(stateCounts, from)
		}
	}

	if to != "" {
		stateCounts[to] += n
	}
}

// markDirtyLocked marks a RepGroup dirty for every subscriber and signals them.
// Must be called with mu held. The wake signal is non-blocking.
func (c *repGroupCounts) markDirtyLocked(repGroup string) {
	for sub := range c.subs {
		sub.dirty[repGroup] = struct{}{}
		rgcSignalWake(sub.wake)
	}
}

// rgcSignalWake performs a non-blocking send on an edge-triggered, buffered(1)
// wake channel.
func rgcSignalWake(wake chan struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// wholeMap returns a deep copy of every RepGroup's absolute counts, INCLUDING
// terminal states and terminal-only RepGroups (no filtering), with non-positive
// entries dropped. No internal map escapes the lock.
func (c *repGroupCounts) wholeMap() map[string]map[JobState]int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.wholeMapLocked()
}

// wholeMapLocked builds the deep copy of the whole map. Must be called with mu
// held.
func (c *repGroupCounts) wholeMapLocked() map[string]map[JobState]int {
	out := make(map[string]map[JobState]int, len(c.counts))
	for repGroup, stateCounts := range c.counts {
		out[repGroup] = rgcCleanCopy(stateCounts)
	}

	return out
}

// rgcCleanCopy returns a fresh copy of a state-count map with any non-positive
// entries dropped, so callers and the wire never see negative or zero clutter. A
// nil input yields a non-nil empty map.
func rgcCleanCopy(stateCounts map[JobState]int) map[JobState]int {
	out := make(map[JobState]int, len(stateCounts))

	maps.Copy(out, stateCounts)

	for state, count := range out {
		if count <= 0 {
			delete(out, state)
		}
	}

	return out
}

// liveSeedLocked returns the fresh-connect seed: a copy of every RepGroup that
// has at least one live (non-terminal) job, with the terminal deleted state
// excluded from each (complete is kept so a partly-finished RepGroup shows its
// progress). The statusAllRepGroups aggregate already holds only live states, so
// it is included whenever it is non-empty. Complete-only, deleted-only and
// complete+deleted-only RepGroups are dropped entirely, so a page refresh does
// not re-show terminal-only work (bugfix 260721-1, restoring 260626-2 /
// 260716-1). The counter itself still tracks terminal states (see wholeMap) for
// the +all+ aggregate and the live per-RepGroup push path; only this
// fresh-connect seed is filtered. Must be called with mu held; no internal map
// escapes the lock.
func (c *repGroupCounts) liveSeedLocked() map[string]map[JobState]int {
	seed := make(map[string]map[JobState]int, len(c.counts))

	for repGroup, stateCounts := range c.counts {
		if repGroup == statusAllRepGroups {
			if live := rgcCleanCopy(stateCounts); len(live) > 0 {
				seed[repGroup] = live
			}

			continue
		}

		if counts := rgcSeedCountCopy(stateCounts); len(counts) > 0 {
			seed[repGroup] = counts
		}
	}

	return seed
}

// rgcSeedCountCopy returns the fresh-connect seed counts for one non-aggregate
// RepGroup. A RepGroup is seeded only if it has at least one live (non-terminal)
// job; such groups keep every positive non-deleted state, including complete
// progress. Complete-only, deleted-only and complete+deleted-only groups all
// return nil so a fresh load (page refresh) does not re-show terminal-only work.
func rgcSeedCountCopy(stateCounts map[JobState]int) map[JobState]int {
	if !rgcHasLiveJob(stateCounts) {
		return nil
	}

	out := make(map[JobState]int, len(stateCounts))

	for state, count := range stateCounts {
		if count > 0 && state != JobStateDeleted {
			out[state] = count
		}
	}

	return out
}

// subscribe registers a new subscriber, capturing the seed it must receive as
// its first drain: the fresh-connect filtered view (liveSeedLocked) that omits
// terminal-only RepGroups and the deleted state, so a page refresh does not
// re-show completed-only work. Registration happens under mu so no concurrent
// transition is missed: a transition either runs before subscribe (its result is
// in the captured seed) or after (it marks the new subscriber dirty and is
// delivered live, including deleted/complete, keeping in-session RepGroups
// visible per 260625-6).
func (c *repGroupCounts) subscribe() *repGroupCountsSubscriber {
	sub := &repGroupCountsSubscriber{
		dirty: make(map[string]struct{}),
		wake:  make(chan struct{}, 1),
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	sub.seed = c.liveSeedLocked()
	c.subs[sub] = struct{}{}

	// ensure the first drain fires even if there are no RepGroups yet, so a
	// freshly-connected client gets an (empty) initial state promptly.
	rgcSignalWake(sub.wake)

	return sub
}

// unsubscribe removes a subscriber.
func (c *repGroupCounts) unsubscribe(sub *repGroupCountsSubscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.subs, sub)
}

// drain returns the absolute counts a subscriber needs to apply since its last
// drain. The first drain after subscribe returns the whole-map seed merged with
// any RepGroup that transitioned between subscribe and now; subsequent drains
// return only the currently-dirty RepGroups with their full current counts. A
// dirty entry overrides any seed entry for the same RepGroup, as it is newer.
// The returned counts are fresh copies, so no internal map escapes the lock.
func (c *repGroupCounts) drain(sub *repGroupCountsSubscriber) map[string]map[JobState]int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if sub.seed == nil && len(sub.dirty) == 0 {
		return nil
	}

	out := sub.seed
	if out == nil {
		out = make(map[string]map[JobState]int, len(sub.dirty))
	}

	sub.seed = nil

	for repGroup := range sub.dirty {
		out[repGroup] = rgcCleanCopy(c.counts[repGroup])
	}

	clear(sub.dirty)

	return out
}

// rgcHasLiveJob reports whether the state counts include at least one job in a
// non-terminal state (anything other than complete or deleted) with a positive
// count.
func rgcHasLiveJob(stateCounts map[JobState]int) bool {
	for state, count := range stateCounts {
		if count > 0 && state != JobStateComplete && state != JobStateDeleted {
			return true
		}
	}

	return false
}
