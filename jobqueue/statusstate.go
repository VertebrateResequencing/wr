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

// statusSubscriber represents one connected status web UI client. Each client
// has its own dirty set so that clients connecting at different times each
// receive the full current state followed by only the RepGroups that change
// thereafter. The wake channel is a buffered, edge-triggered signal: applying a
// transition signals every subscriber without ever blocking the queue-mutation
// path.
type statusSubscriber struct {
	dirty map[string]struct{}
	wake  chan struct{}
}

// statusState holds the authoritative, absolute per-RepGroup job-state counts
// that the status web UI displays, plus the special statusAllRepGroups ("+all+")
// aggregate of all live (incomplete) jobs across every RepGroup.
//
// The status web UI used to be fed non-idempotent count deltas ("+n in this
// state, -n in that state") over a lossy channel; a single dropped or duplicated
// delta corrupted the displayed counts (causing the flicker and overcount bugs
// of issue 260625-7). statusState instead holds the current absolute count for
// each (RepGroup, JobState) and sends those absolute values to clients. Absolute
// state is idempotent: applying the same value twice is a no-op, and a dropped
// intermediate value is harmless because the next value overwrites it wholesale.
// This makes coalescing under load correct by construction and removes the need
// for any sequence numbers, snapshot generations, or delta-recovery machinery.
//
// Lock discipline: mu is a strict LEAF. While it is held, no other lock may be
// acquired, and no other lock holder may take mu before the queue lock. Callers
// that mutate the queue (the change callback and the TTR callback) acquire mu
// last in the order queue.mutex -> job -> statusState.mu, so mu can never be
// involved in a lock-order inversion (see issue 260625-7 attempt 5).
type statusState struct {
	mu     sync.Mutex
	counts map[string]map[JobState]int
	subs   map[*statusSubscriber]struct{}
}

// newStatusState creates an empty statusState.
func newStatusState() *statusState {
	return &statusState{
		counts: make(map[string]map[JobState]int),
		subs:   make(map[*statusSubscriber]struct{}),
	}
}

// seed sets the authoritative counts from a full scan of current and completed
// jobs. It is called once at server startup before any client connects. The
// provided map is copied; the caller may reuse it afterwards.
func (s *statusState) seed(counts map[string]map[JobState]int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.counts = make(map[string]map[JobState]int, len(counts))
	for repGroup, stateCounts := range counts {
		s.counts[repGroup] = cleanCountCopy(stateCounts)
	}
}

// cleanCountCopy returns a fresh copy of a state-count map with any non-positive
// entries dropped, so callers and the wire never see negative or zero clutter. A
// nil input yields a non-nil empty map.
func cleanCountCopy(stateCounts map[JobState]int) map[JobState]int {
	out := make(map[JobState]int, len(stateCounts))

	maps.Copy(out, stateCounts)

	for state, count := range out {
		if count <= 0 {
			delete(out, state)
		}
	}

	return out
}

// applyTransition records that n jobs in the given RepGroup moved from one
// JobState to another, updating both the RepGroup's absolute counts and the
// statusAllRepGroups aggregate, then marks the RepGroup (and the aggregate)
// dirty for every subscriber and signals them.
//
// from or to may be the empty JobState to mean "no source"/"no destination"
// (e.g. brand new jobs have no from state). Counts are clamped at zero so a lost
// or duplicated decrement can never drive a count negative. The aggregate never
// holds the terminal complete/deleted states, matching what the UI shows for
// "+all+" (live jobs only).
func (s *statusState) applyTransition(from, to JobState, repGroup string, n int) {
	if n <= 0 || repGroup == "" || repGroup == statusAllRepGroups {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.applyToRepGroupLocked(repGroup, from, to, n)
	s.applyToRepGroupLocked(statusAllRepGroups, aggregateState(from), aggregateState(to), n)

	s.markDirtyLocked(repGroup)
	s.markDirtyLocked(statusAllRepGroups)
}

// aggregateState maps a per-RepGroup state to the state it contributes to the
// statusAllRepGroups live aggregate. Terminal states (complete, deleted) and the
// empty state contribute nothing, because "+all+" counts only live jobs.
func aggregateState(state JobState) JobState {
	switch state {
	case JobStateComplete, JobStateDeleted:
		return ""
	default:
		return state
	}
}

// applyToRepGroupLocked applies a single transition to one RepGroup's counts.
// Must be called with mu held.
func (s *statusState) applyToRepGroupLocked(repGroup string, from, to JobState, n int) {
	stateCounts, ok := s.counts[repGroup]
	if !ok {
		stateCounts = make(map[JobState]int)
		s.counts[repGroup] = stateCounts
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
func (s *statusState) markDirtyLocked(repGroup string) {
	for sub := range s.subs {
		sub.dirty[repGroup] = struct{}{}
		signalWake(sub.wake)
	}
}

// signal performs a non-blocking send on an edge-triggered, buffered(1) wake
// channel.
func signalWake(wake chan struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// subscribe registers a new subscriber, seeding its dirty set with every
// RepGroup currently known so that the subscriber's first drain returns the full
// current state. Registration happens under mu so no concurrent transition is
// missed: a transition either runs before subscribe (its result is in the seeded
// full state) or after (it marks the new subscriber dirty).
func (s *statusState) subscribe() *statusSubscriber {
	sub := &statusSubscriber{
		dirty: make(map[string]struct{}),
		wake:  make(chan struct{}, 1),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for repGroup := range s.counts {
		sub.dirty[repGroup] = struct{}{}
	}

	s.subs[sub] = struct{}{}

	// ensure the first drain fires even if there are no RepGroups yet, so a
	// freshly-connected client gets an (empty) initial state promptly.
	signalWake(sub.wake)

	return sub
}

// unsubscribe removes a subscriber.
func (s *statusState) unsubscribe(sub *statusSubscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.subs, sub)
}

// drain returns the absolute counts for every RepGroup currently dirty for the
// subscriber, clearing its dirty set. The returned counts are fresh copies, so
// no internal map ever escapes the lock. A RepGroup that is dirty but no longer
// present yields an empty count map (all states zero), which the client applies
// as "this RepGroup has no jobs" without removing the row.
func (s *statusState) drain(sub *statusSubscriber) map[string]map[JobState]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(sub.dirty) == 0 {
		return nil
	}

	out := make(map[string]map[JobState]int, len(sub.dirty))
	for repGroup := range sub.dirty {
		out[repGroup] = cleanCountCopy(s.counts[repGroup])
	}

	clear(sub.dirty)

	return out
}

// snapshot returns a fresh copy of every RepGroup's absolute counts. Used by
// tests and by reconnect/seed verification; no internal map escapes the lock.
func (s *statusState) snapshot() map[string]map[JobState]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]map[JobState]int, len(s.counts))
	for repGroup, stateCounts := range s.counts {
		out[repGroup] = cleanCountCopy(stateCounts)
	}

	return out
}
