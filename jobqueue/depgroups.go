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

// This file contains the per-dep-group live-member state that dependency
// resolution answers "has this dep group a live member job?" from, instead of
// prefix-scanning the database for the group's member job keys.

import (
	"maps"
	"slices"
	"sync"
)

// depGroupDependencyPrefix prefixes a dep group name to form the opaque queue
// dependency key for that group. Job keys are 128-bit FarmHash hex strings, so
// no job key can collide with it.
const depGroupDependencyPrefix = "depgroup:"

const (
	// depGroupShards is how many shards each of depGroupMembers' two maps is
	// split into. The archive, delete, modify and add paths all maintain
	// membership, and none of them may take a server-wide exclusive lock on the
	// per-transition hot path (DEVELOPERS.md rule 2), so they contend only when
	// two operations hash to the same shard.
	depGroupShards = 64

	// the FNV-1a 32-bit basis and prime. FNV is used rather than maphash so that
	// a name's shard is the same in every process, which keeps a failure
	// reproducible.
	fnvOffsetBasis32 = 2166136261
	fnvPrime32       = 16777619
)

// depGroupDependencyKey returns the opaque queue dependency key that stands for
// the whole of depGroup.
func depGroupDependencyKey(depGroup string) string {
	return depGroupDependencyPrefix + depGroup
}

// depGroupShard holds the member job keys of the dep groups that hash to it. A
// group with no live member is absent, so presence is membership.
type depGroupShard struct {
	mu      sync.Mutex
	members map[string]map[string]bool
}

// addLocked records jobKey as a member of depGroup. The shard must be held.
func (s *depGroupShard) addLocked(depGroup, jobKey string) {
	members := s.members[depGroup]
	if members == nil {
		members = make(map[string]bool)
		s.members[depGroup] = members
	}

	members[jobKey] = true
}

// dropLocked drops jobKey from depGroup, reporting whether that left the group
// with no live member. A group with no member is deleted rather than left as an
// empty set, so re-dropping an already dropped key reports nothing. The shard
// must be held.
func (s *depGroupShard) dropLocked(depGroup, jobKey string) bool {
	members := s.members[depGroup]
	if members == nil {
		return false
	}

	delete(members, jobKey)

	if len(members) > 0 {
		return false
	}

	delete(s.members, depGroup)

	return true
}

// groupMutation is everything one operation does to one dep group's member set.
type groupMutation struct {
	join  string   // the job key joining the group, "" for none.
	leave []string // the job keys leaving the group.
}

// groupMutations is the per-group work an operation has to do, keyed by group
// name. Collecting it before touching any group's shard is what lets each shard
// be taken exactly once, and lets a rekey's join and leave for the same group
// happen inside one hold of it.
type groupMutations map[string]*groupMutation

// join records that jobKey becomes a member of depGroup.
func (g groupMutations) join(depGroup, jobKey string) {
	g.forGroup(depGroup).join = jobKey
}

// leave records that jobKey stops being a member of depGroup.
func (g groupMutations) leave(depGroup, jobKey string) {
	mut := g.forGroup(depGroup)
	mut.leave = append(mut.leave, jobKey)
}

func (g groupMutations) forGroup(depGroup string) *groupMutation {
	mut, exists := g[depGroup]
	if !exists {
		mut = &groupMutation{}
		g[depGroup] = mut
	}

	return mut
}

// jobGroupShard holds the dep groups the job keys that hash to it are members
// of. A job in no group is absent.
type jobGroupShard struct {
	mu     sync.Mutex
	groups map[string]map[string]bool
}

// recordLocked records jobKey as a member of depGroup in the job -> groups map.
// The shard must be held.
func (s *jobGroupShard) recordLocked(jobKey, depGroup string) {
	held := s.groups[jobKey]
	if held == nil {
		held = make(map[string]bool)
		s.groups[jobKey] = held
	}

	held[depGroup] = true
}

// retargetLocked makes newGroups jobKey's whole membership in the job -> groups
// map, recording in muts the groups jobKey joins and the ones it leaves. The
// shard must be held.
func (s *jobGroupShard) retargetLocked(jobKey string, newGroups []string, muts groupMutations) {
	wanted := depGroupSet(newGroups)
	held := s.groups[jobKey]

	for depGroup := range wanted {
		if !held[depGroup] {
			muts.join(depGroup, jobKey)
		}
	}

	for depGroup := range held {
		if !wanted[depGroup] {
			muts.leave(depGroup, jobKey)
		}
	}

	s.setLocked(jobKey, wanted)
}

// depGroupSet is the set of the named dep groups, less the empty name, which
// prepareNewJobs does not record as a dep group either.
func depGroupSet(depGroups []string) map[string]bool {
	set := make(map[string]bool, len(depGroups))

	for _, depGroup := range depGroups {
		if depGroup != "" {
			set[depGroup] = true
		}
	}

	return set
}

// releaseLocked forgets jobKey's whole membership, recording in muts the groups
// it leaves. The shard must be held.
func (s *jobGroupShard) releaseLocked(jobKey string, muts groupMutations) {
	for depGroup := range s.groups[jobKey] {
		muts.leave(depGroup, jobKey)
	}

	delete(s.groups, jobKey)
}

// setLocked makes groups jobKey's recorded membership, forgetting jobKey
// entirely if that is nothing. The shard must be held.
func (s *jobGroupShard) setLocked(jobKey string, groups map[string]bool) {
	if len(groups) == 0 {
		delete(s.groups, jobKey)

		return
	}

	s.groups[jobKey] = groups
}

// depGroupMembers holds, for each dep group with at least one live member job,
// the keys of those members, and for each live job the groups it is a member of.
// Both maps are sharded so the archive, delete, modify and add paths never
// contend on one server-wide lock (DEVELOPERS.md rule 2).
//
// Every operation is idempotent, and the emptied groups an operation reports are
// returned rather than acted on: releasing a group's waiters means calling into
// queue, and no shard lock may be held across that.
//
// Lock order, all of it load-bearing:
//
//   - A job-key shard is always taken before a group-name shard, never the
//     reverse. The shard locks are leaves relative to the existing
//     queue.mutex -> job -> statusState.mu order, which is unchanged.
//   - Group-name shards are taken one at a time and released before the next,
//     never two at once. Holding them all in job.DepGroups order deadlocks
//     against a concurrent operation whose group list is ordered differently. No
//     cross-group atomicity is needed, because each group's emptiness is
//     independent of every other group's.
//   - rekey, the one operation that touches two job-key shards, takes them in
//     ascending shard index, and takes the shard once when both keys fall in it.
//     Locking them in call order deadlocks two opposing rekeys (a -> b against
//     b -> a).
type depGroupMembers struct {
	groups [depGroupShards]depGroupShard
	jobs   [depGroupShards]jobGroupShard
}

// newDepGroupMembers returns a depGroupMembers holding no memberships.
func newDepGroupMembers() *depGroupMembers {
	m := &depGroupMembers{}

	for i := range depGroupShards {
		m.groups[i].members = make(map[string]map[string]bool)
		m.jobs[i].groups = make(map[string]map[string]bool)
	}

	return m
}

// hasMembers says whether depGroup has at least one live member job.
func (m *depGroupMembers) hasMembers(depGroup string) bool {
	shard := m.groupShard(depGroup)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	return len(shard.members[depGroup]) > 0
}

// memberships returns the total number of (group, member) pairs held. It is
// inert observability in the style of db.archivedDecodes: nothing but the memory
// gate's tests and a debug log line read it, and it affects no behaviour.
func (m *depGroupMembers) memberships() int {
	total := 0

	for i := range depGroupShards {
		shard := &m.groups[i]

		shard.mu.Lock()

		for _, members := range shard.members {
			total += len(members)
		}

		shard.mu.Unlock()
	}

	return total
}

// add records jobKey as a live member of each of depGroups. Idempotent.
func (m *depGroupMembers) add(depGroups []string, jobKey string) {
	// an empty job key would be recorded in the job -> groups map but skipped by
	// applyOne, whose join is sentinelled on "", leaving the two maps
	// disagreeing. Real job keys are 32-hex, so this cannot happen; the guard is
	// so it cannot start happening either.
	if jobKey == "" {
		return
	}

	shard := m.jobShard(jobKey)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	muts := make(groupMutations)

	for _, depGroup := range depGroups {
		if depGroup == "" || shard.groups[jobKey][depGroup] {
			continue
		}

		shard.recordLocked(jobKey, depGroup)
		muts.join(depGroup, jobKey)
	}

	// adding can empty nothing, so there is nothing to report.
	m.apply(muts)
}

// remove drops jobKey from every group it is a member of, returning the groups
// left with no live member. Idempotent.
func (m *depGroupMembers) remove(jobKey string) []string {
	shard := m.jobShard(jobKey)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	muts := make(groupMutations)
	shard.releaseLocked(jobKey, muts)

	return m.apply(muts)
}

// replace makes newGroups jobKey's membership, returning the groups left with no
// live member. Idempotent.
func (m *depGroupMembers) replace(jobKey string, newGroups []string) []string {
	shard := m.jobShard(jobKey)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	// the add path's second pass calls this for every job in the batch, and a job
	// that declares no group and holds none has nothing to drop, so it is worth
	// not allocating the two maps retargetLocked would: a 150k-job add would
	// otherwise throw 150k of them away.
	if len(newGroups) == 0 && len(shard.groups[jobKey]) == 0 {
		return nil
	}

	muts := make(groupMutations)
	shard.retargetLocked(jobKey, newGroups, muts)

	return m.apply(muts)
}

// rekey is replace across a job key change: newKey becomes a member of
// newGroups, and any membership held under oldKey is then dropped. It returns
// the groups left with no live member. The new key is recorded BEFORE the old
// one is dropped, so a group both keys belong to never transiently empties - the
// ordering matters, because an emptied group releases its waiters. Idempotent;
// oldKey == newKey behaves as replace.
func (m *depGroupMembers) rekey(oldKey, newKey string, newGroups []string) []string {
	if oldKey == newKey {
		return m.replace(newKey, newGroups)
	}

	// ascending shard index, taking the shard once when both keys fall in it: two
	// opposing rekeys, a -> b on one goroutine and b -> a on another, deadlock if
	// each locks its own key's shard first.
	low, high := m.jobShardIndexes(oldKey, newKey)

	m.jobs[low].mu.Lock()
	defer m.jobs[low].mu.Unlock()

	if high != low {
		m.jobs[high].mu.Lock()
		defer m.jobs[high].mu.Unlock()
	}

	muts := make(groupMutations)
	m.jobShard(newKey).retargetLocked(newKey, newGroups, muts)
	m.jobShard(oldKey).releaseLocked(oldKey, muts)

	return m.apply(muts)
}

// jobShardIndexes returns the indexes of the two keys' job-key shards, lowest
// first.
func (m *depGroupMembers) jobShardIndexes(a, b string) (int, int) {
	ai, bi := depGroupShardIndex(a), depGroupShardIndex(b)

	return min(ai, bi), max(ai, bi)
}

// depGroupShardIndex is the shard a group name or job key belongs to.
func depGroupShardIndex(name string) int {
	hash := uint32(fnvOffsetBasis32)

	for i := range len(name) {
		hash ^= uint32(name[i])
		hash *= fnvPrime32
	}

	return int(hash % depGroupShards)
}

// groupShard returns the shard holding depGroup's member set.
func (m *depGroupMembers) groupShard(depGroup string) *depGroupShard {
	return &m.groups[depGroupShardIndex(depGroup)]
}

// jobShard returns the shard holding jobKey's group set.
func (m *depGroupMembers) jobShard(jobKey string) *jobGroupShard {
	return &m.jobs[depGroupShardIndex(jobKey)]
}

// apply carries out muts, one group-name shard at a time and released before the
// next is taken, and returns the names of the groups left with no live member. It
// is called with the job-key shard(s) of the keys involved held, which is the
// only order these locks are ever taken in.
func (m *depGroupMembers) apply(muts groupMutations) []string {
	var emptied []string

	for _, depGroup := range slices.Sorted(maps.Keys(muts)) {
		if m.applyOne(depGroup, muts[depGroup]) {
			emptied = append(emptied, depGroup)
		}
	}

	return emptied
}

// applyOne carries out one group's mutation under a single hold of that group's
// shard, reporting whether the group is left with no live member. The joining
// key goes in before the leaving ones come out, so a group both keys of a rekey
// belong to is never seen as empty by another goroutine - an emptied group
// releases its waiters.
func (m *depGroupMembers) applyOne(depGroup string, mut *groupMutation) bool {
	shard := m.groupShard(depGroup)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if mut.join != "" {
		shard.addLocked(depGroup, mut.join)
	}

	emptied := false

	for _, jobKey := range mut.leave {
		emptied = shard.dropLocked(depGroup, jobKey) || emptied
	}

	return emptied
}
