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

// The log store's claims, which order the writes of a key's spec, rglookup or
// removal, and its reviving keys, which order a transition or exit of a key
// after a write making it live (spec "Write path", Claims and Reviving).

import (
	"errors"
	"maps"
	"slices"
)

// errLogKeyClaimed is the error of an add planning to put back a dependent
// whose key another write has claimed.
var errLogKeyClaimed = errors.New("dependent's key claimed by another write")

// logClaim is the keys one add, delete, modify, archive, put-back or rep group
// lookup has claimed: no other write of a spec, lookup or removal of one of
// them decides or takes its seqs until the claim is released, once the write
// has applied. Every such write claims its keys from before it decides until
// it has applied, except a guarded add's put-backs, which decide, take their
// seqs and append under guardMu, as removals do. So a removal never applies
// before a lower-seq spec, complete or rglookup record of its key, though the
// reducer keeps no tombstone (spec "Records").
//
// No wait deadlocks. A write waiting for a claim holds none (claimKeys), and an
// add finding a dependent's key claimed releases its claim and plans again
// (planClaimedAdd). Every other wait made holding a claim is for a write that
// finishes without it: a state record, an archive or a guarded add already
// appended, or a write making a key live. Of those last, an add's claimed
// put-back can meet only a guarded add's put-back, which, once noted, appends
// waiting only for brief locks; a guarded add defers such a key rather than
// waiting, and waits for it only once its own write has finished
// (putBackDeferred), with none of its keys noted; and a transition or exit
// waits holding no lock and is not yet in stateWrites, so nothing waits for it.
// A guarded add's write unnotes its keys when it finishes, not when its claim
// is released, as it may then wait for writes making deferred keys live; every
// other reviving write releases its claim, so unnotes them, as soon as it has
// finished. So no such wait outlasts the write it waits for.
type logClaim struct {
	s    *logStore
	keys map[logKey]struct{}
	mine chan struct{}

	// revived unnote the keys noted in reviving for the claim's write.
	revived []func()
}

// noteReviving notes each of keys in reviving, as being made live by c's
// write, until unnoteReviving. The write calls it before taking its seqs.
func (c *logClaim) noteReviving(keys ...logKey) {
	c.s.stateMu.Lock()
	defer c.s.stateMu.Unlock()

	for _, k := range keys {
		c.noteRevivingLocked(k)
	}
}

// noteRevivingLocked is noteReviving of k for a caller holding stateMu.
func (c *logClaim) noteRevivingLocked(k logKey) {
	c.revived = append(c.revived, c.s.notePending(c.s.reviving, k))
}

// tryClaim adds k to c and returns true, unless another write has it claimed.
// It never waits, so a write already holding a claim never waits for another.
func (c *logClaim) tryClaim(k logKey) bool {
	c.s.claimMu.Lock()
	defer c.s.claimMu.Unlock()

	if other := c.s.claims[k]; other != nil && other != c.mine {
		return false
	}

	c.s.claims[k] = c.mine
	c.keys[k] = struct{}{}

	return true
}

// unnoteReviving unnotes the keys noted in reviving for c's write. A guarded
// add's write calls it once it has finished, as its after func, so a
// transition or put-back waiting for one goes on as soon as it has applied or
// failed, not once the add's deferred put-backs are made and c is released;
// release calls it for any other write.
func (c *logClaim) unnoteReviving() {
	for _, unnote := range c.revived {
		unnote()
	}

	c.revived = nil
}

// release unnotes any keys c's write was making live and unclaims c's keys.
func (c *logClaim) release() {
	c.unnoteReviving()

	c.s.claimMu.Lock()
	for k := range c.keys {
		delete(c.s.claims, k)
	}
	c.s.claimMu.Unlock()

	close(c.mine)
}

// planClaimedAdd claims keys, those of jobs, and plans their add, claiming
// too the key of each archived dependent it puts back. If another write has
// one of those claimed, it releases its claim and plans again once it can
// claim that key with the rest, so it never waits holding a claim.
func (s *logStore) planClaimedAdd(jobs []*Job, keys []logKey, ignoreAdded bool) (logAdd, *logClaim, error) {
	claim := s.claimKeySet(keys...)

	for {
		add, err := s.planAdd(jobs, ignoreAdded, claim)
		if !errors.Is(err, errLogKeyClaimed) {
			return add, claim, err
		}

		more := maps.Clone(claim.keys)
		more[add.claimed] = struct{}{}

		claim.release()
		claim = s.claimKeys(more)
	}
}

// claimKeySet is claimKeys of keys.
func (s *logStore) claimKeySet(keys ...logKey) *logClaim {
	set := make(map[logKey]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}

	return s.claimKeys(set)
}

// claimKeys claims keys, once no other write in flight has any of them,
// waiting for each such write with no key claimed.
func (s *logStore) claimKeys(keys map[logKey]struct{}) *logClaim {
	c := &logClaim{s: s, keys: maps.Clone(keys), mine: make(chan struct{})}

	for wait := s.tryClaimKeys(c.keys, c.mine); wait != nil; wait = s.tryClaimKeys(c.keys, c.mine) {
		if logStoreWaitHook != nil {
			logStoreWaitHook()
		}

		<-wait
	}

	return c
}

// logPutBacks is what a guarded add's put-backs did: the keys put back, and,
// to each key it deferred, as another write in flight was making it live, that
// write's idle channel (see putBack).
type logPutBacks struct {
	put      map[string]bool
	deferred map[string]chan struct{}
}

// putBackDeferred makes the put-backs a guarded add deferred: once each write
// that was making one live has finished, it decides on them again, as
// appendGuardedAdd does, appending any put-backs in a further block, until none
// is deferred, and returns once those are durable. So a key whose reviving
// write fails is still put back, as in the bbolt store, whose transactions are
// serial. It adds the keys of each further block, once durable, to pb.put, the
// map the add reports as its put-backs (storeNewJobsGuarded), so a block that
// fails adds none. The caller's own write has finished, so its keys are no
// longer noted in reviving, and it waits holding no lock: a write it waits for
// needs nothing of it (see logClaim).
func (s *logStore) putBackDeferred(pb logPutBacks, claim *logClaim) error {
	for deferred := pb.deferred; len(deferred) > 0; {
		awaitIdle(deferred)

		keys := slices.Sorted(maps.Keys(deferred))

		var f logFrames

		w, more, err := s.appendAdd(&f, nil, func() []string { return keys }, claim)
		if err == nil && w != nil {
			err = w.wait()
		}

		if err != nil {
			return err
		}

		maps.Copy(pb.put, more.put)
		deferred = more.deferred
	}

	return nil
}

// awaitIdle returns once each of waits, the idle channels of writes in flight,
// is closed.
func awaitIdle(waits map[string]chan struct{}) {
	for _, wait := range waits {
		if logStoreWaitHook != nil {
			logStoreWaitHook()
		}

		<-wait
	}
}

// logPending counts a key's writes of one kind in flight; idle is closed once
// there are none.
type logPending struct {
	n    int
	idle chan struct{}
}

// notePending notes one more write of k in flight in pending, a map stateMu
// guards, returning the func that unnotes it. The caller holds stateMu.
func (s *logStore) notePending(pending map[logKey]*logPending, k logKey) func() {
	p := pending[k]
	if p == nil {
		p = &logPending{idle: make(chan struct{})}
		pending[k] = p
	}

	p.n++

	return func() {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()

		if p.n--; p.n == 0 {
			delete(pending, k)
			close(p.idle)
		}
	}
}

// lockUnlessReviving takes job's read lock, then stateMu, once no write in
// flight is making k live. It waits holding neither, so the write it waits for
// can take job's read lock to make its records, and an add never waits for it
// in awaitStateWrites: it is not yet in stateWrites.
func (s *logStore) lockUnlessReviving(job *Job, k logKey) {
	for {
		job.RLock()
		s.stateMu.Lock()

		p := s.reviving[k]
		if p == nil {
			return
		}

		s.stateMu.Unlock()
		job.RUnlock()

		if logStoreWaitHook != nil {
			logStoreWaitHook()
		}

		<-p.idle
	}
}

// claimAndPutBack claims job's key for add, then is addPutBack of job to add's
// frames. If a guarded add's put-back of the key is in flight, it waits for
// that, holding its claim, then decides again: having the key claimed, no
// other write can be making it live, and a guarded add's put-back appends,
// once noted, waiting for nothing, so is durable or failed without this add.
func (s *logStore) claimAndPutBack(add *logAdd, job *Job) (bool, error) {
	k, err := parseLogKey(job.Key())
	if err != nil {
		return false, err
	}

	if !add.claim.tryClaim(k) {
		add.claimed = k

		return false, errLogKeyClaimed
	}

	for {
		put, wait, err := s.addPutBack(&add.frames, job, k, add.claim)
		if wait == nil {
			return put, err
		}

		if logStoreWaitHook != nil {
			logStoreWaitHook()
		}

		<-wait
	}
}

// tryClaimKeys claims keys for the write whose claim's channel is mine and
// returns nil, unless another write has one of them claimed, when it returns
// that write's channel.
func (s *logStore) tryClaimKeys(keys map[logKey]struct{}, mine chan struct{}) chan struct{} {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()

	for k := range keys {
		if other := s.claims[k]; other != nil {
			return other
		}
	}

	for k := range keys {
		s.claims[k] = mine
	}

	return nil
}
