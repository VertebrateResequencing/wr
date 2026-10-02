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

// This file orders an archive's cleanup of a job's in-memory state against an
// add that brings the same job back to run again
// (.docs/bugfixes/260930-dep-group-rerun-gaps.md item 7).
//
// Once an archive has removed a job's queue item, it drops the job's dep group
// memberships (satisfying any group that leaves empty) and its rep group lookup.
// An add can bring the job back in between: storeNewJobs resurrecting a complete
// dependent, updateLiveDependents finding a dependent it read archived, or a
// re-add of the job itself. The add registers the job's memberships before it
// queues it, since jobs in the same batch resolve their dependencies against
// them, and records its rep group once it is queued. An archive cleanup applied
// after that registration dropped what the add registered, so the job, back and
// incomplete, released the waiters of its groups and could not be found by its
// rep group.
//
// So an add holds the keys of the jobs it may bring back, in bringBacks, from
// before it registers any membership until it has queued them and recorded their
// rep groups, and the archive decides what to clean up under the same key's
// shard lock:
//
//   - while an add holds the key, the decision is deferred to the last such add
//     to finish, so it is made once every bring-back of the key in flight has
//     queued the job or failed to;
//   - otherwise, a queue item under the key can only be one queued since the
//     archive removed its own, by an add that registered its memberships and rep
//     group, so the memberships are left alone, as is the rep group the queued
//     job has;
//   - otherwise, nothing has brought the job back, and an add that does so later
//     registers after the cleanup.
//
// Either way no membership an add registered for a job it has queued is ever
// dropped, and no group is satisfied while a member is back.
//
// A modify that changes a job's key registers its queue item, rep group and
// memberships under the new key, so it holds the new keys too. Modify refuses a
// new key that is queued or complete, and a job in the window is complete, but
// that check is made before the rekey, and a job with the new key can be added,
// run and archived in between.
//
// A bringBacks shard lock is taken with no other lock held, never two at once,
// and the cleanup it guards takes the queue mutex, a job's lock, membership
// shard locks and the rep group lookup's lock, so it is above them all in the
// lock order.

import (
	"context"
	"slices"
	"sync"
)

// jobKeys returns the keys of the jobs in each of jobLists.
func jobKeys(jobLists ...[]*Job) []string {
	var keys []string

	for _, jobs := range jobLists {
		for _, job := range jobs {
			keys = append(keys, job.Key())
		}
	}

	return keys
}

// rekeyedKeys returns the new keys of a modify's new->old key mapping that
// differ from the old.
func rekeyedKeys(modified map[string]string) []string {
	var keys []string

	for newKey, oldKey := range modified {
		if newKey != oldKey {
			keys = append(keys, newKey)
		}
	}

	return keys
}

// bringBackShard is one shard of bringBacks.
type bringBackShard struct {
	mu sync.Mutex

	// holds counts the adds holding each key.
	holds map[string]int

	// deferred holds, for each key an archive has left to its adds to clean up,
	// the rep groups of the jobs archived.
	deferred map[string][]string
}

// releaseLocked drops one add's hold on key. If that was the last, it returns
// the rep groups of any archive cleanup of key deferred to it, and whether there
// was one. The shard must be held.
func (b *bringBackShard) releaseLocked(key string) ([]string, bool) {
	if b.holds[key] > 1 {
		b.holds[key]--

		return nil, false
	}

	delete(b.holds, key)

	repGroups, deferred := b.deferred[key]
	delete(b.deferred, key)

	// a map keeps its buckets after its keys are deleted, so one large add would
	// otherwise pin memory for every key it ever held for the manager's life.
	if len(b.holds) == 0 {
		b.holds = nil
	}

	if len(b.deferred) == 0 {
		b.deferred = nil
	}

	return repGroups, deferred
}

// bringBacks are the keys of the jobs adds may be bringing back to run again, and
// the archive cleanups waiting on them. The zero value is ready to use.
type bringBacks struct {
	shards [depGroupShards]bringBackShard
}

// shard returns the shard of key.
func (b *bringBacks) shard(key string) *bringBackShard {
	return &b.shards[depGroupShardIndex(key)]
}

// holdBringBacks notes that an add may bring back the jobs with these keys. Call
// it before the add registers any of their memberships, and releaseBringBacks
// with the same keys once it has queued them or given up.
func (s *Server) holdBringBacks(keys []string) {
	for _, key := range keys {
		shard := s.bringBacks.shard(key)

		shard.mu.Lock()

		if shard.holds == nil {
			shard.holds = make(map[string]int)
		}

		shard.holds[key]++

		shard.mu.Unlock()
	}
}

// releaseBringBacks undoes holdBringBacks, carrying out any archive cleanup of a
// key left to the last add holding it.
func (s *Server) releaseBringBacks(ctx context.Context, keys []string) {
	for _, key := range keys {
		shard := s.bringBacks.shard(key)

		shard.mu.Lock()

		if repGroups, deferred := shard.releaseLocked(key); deferred {
			s.cleanUpArchivedLocked(ctx, key, repGroups)
		}

		shard.mu.Unlock()
	}
}

// cleanUpArchived drops the in-memory state of an archived job whose queue item
// has been removed, its dep group memberships and its rep group's lookup of it,
// unless an add has brought it back (see bringback.go).
func (s *Server) cleanUpArchived(ctx context.Context, key, repGroup string) {
	shard := s.bringBacks.shard(key)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if shard.holds[key] > 0 {
		if shard.deferred == nil {
			shard.deferred = make(map[string][]string)
		}

		shard.deferred[key] = append(shard.deferred[key], repGroup)

		return
	}

	s.cleanUpArchivedLocked(ctx, key, []string{repGroup})
}

// cleanUpArchivedLocked is cleanUpArchived with key's shard held and no add
// holding key. A job queued under key now was brought back, so keeps its
// memberships and its own rep group's lookup of it.
func (s *Server) cleanUpArchivedLocked(ctx context.Context, key string, repGroups []string) {
	if job := s.queuedJob(key); job != nil {
		job.RLock()
		queuedRepGroup := job.RepGroup
		job.RUnlock()

		s.deleteRepGroupKeys(key, slices.DeleteFunc(repGroups, func(repGroup string) bool {
			return repGroup == queuedRepGroup
		}))

		return
	}

	s.releaseDepGroupMembership(ctx, key)
	s.deleteRepGroupKeys(key, repGroups)
}

// deleteRepGroupKeys drops key from the lookups of repGroups.
func (s *Server) deleteRepGroupKeys(key string, repGroups []string) {
	s.rpl.Lock()
	defer s.rpl.Unlock()

	for _, repGroup := range repGroups {
		s.rpl.Delete(repGroup, key)
	}
}
