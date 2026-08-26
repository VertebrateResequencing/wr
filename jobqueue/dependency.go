/*******************************************************************************
 * Copyright (c) 2017-2018, 2026 Genome Research Ltd.
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

// This file contains the dependency related code.

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"

	bolt "go.etcd.io/bbolt"
)

// dependencyResolutionChunkSize is how many jobs resolveDependencies resolves
// per bolt read transaction.
//
// One transaction for the whole pass would be fewer transactions still, but that
// is not safe: a bolt read transaction holds mmaplock.RLock() for its whole
// life, and a writer whose allocation reaches the end of the current mapping
// must take mmaplock.Lock() to grow it - while holding db.rwlock, so every other
// write queues behind it. Measured against bbolt v1.4.3 on a 134 MB file: with a
// read transaction open, a growing write blocked for the remaining life of that
// transaction, and so did an unrelated tiny write. The stall is therefore
// database-wide, reaching add, archive, bury and release, and through them the
// RPC handlers that hold the queue mutex - which is the production signature
// this work exists to remove (jtouch 16m38.97s, add 16m33.76s, 14 getin
// completing in the same second; .docs/reliable4/prod-restart-260825.md Finding
// 4). One transaction for a whole recovery would set that stall's upper bound to
// the whole recovery, and production measured recoveries of 21 minutes and
// 42m56s.
//
// Chunking bounds the exposure to one chunk and still gets essentially all of
// the win: the 150,472 jobs production recovered cost ~151 transactions instead
// of ~450,000, within 0.03% of the whole-pass ideal. It also leaves the snapshot
// semantics closer to the per-job resolution this replaced (a snapshot per job)
// than a pass-wide snapshot would; each job's dependencies resolve independently
// of every other job's, so a chunk boundary cannot change an answer.
//
// It is a package var (not user-configurable) purely so tests can lower it.
//
//nolint:gochecknoglobals // internal tuning knob; a var only so tests can vary it
var dependencyResolutionChunkSize = 1000

// depReader provides the database reads that dependency resolution needs. *db
// implements it by opening a bolt read transaction per read, which is what a
// single job's resolution on a control path wants; txDepReader implements it
// against one caller-supplied read transaction, so resolving a chunk of jobs
// costs one transaction instead of one or more per job.
type depReader interface {
	depGroupEverSeen(depGroup string) (bool, error)
	depGroupsEverSeen(depGroups []string) (map[string]bool, error)
	checkIfLive(jobKey string) (bool, error)
}

// txDepReader is a depReader that answers every read from one bolt read
// transaction.
type txDepReader struct {
	tx *bolt.Tx
}

func (r txDepReader) depGroupEverSeen(depGroup string) (bool, error) {
	return depGroupEverSeenTx(r.tx, depGroup), nil
}

func (r txDepReader) depGroupsEverSeen(depGroups []string) (map[string]bool, error) {
	return depGroupsEverSeenTx(r.tx, depGroups), nil
}

func (r txDepReader) checkIfLive(jobKey string) (bool, error) {
	return checkIfLiveTx(r.tx, jobKey), nil
}

// resolvedJob is a job together with the dependency keys resolved for it. They
// travel as one value rather than as index-parallel slices so that no later
// filtering or reordering step can hand a job another job's dependencies, which
// would run jobs in the wrong order with no error to say so.
type resolvedJob struct {
	job  *Job
	deps []string
}

// resolveDependencyChunk resolves the dependencies of the given jobs inside a
// single bolt read transaction, and returns nothing at all if ctx is cancelled
// part way through (bolt rolls a View back when its function returns an error,
// so a cancel cannot leave the transaction open).
//
// Only work that reads the database belongs in here. It does also call
// groups.hasMembers, which takes one of depGroupMembers' shard mutexes for a
// single O(1) map read, and job.setWaitingForDepGroups, which takes that Job's
// own mutex; both are safe because recoverIncompleteJobs decodes a fresh *Job
// per database record, so these pointers are private to the recovery goroutine
// and the locks are uncontended. Nothing that can block on another goroutine -
// the scheduler, the queue, a client write - may be done in here, since the
// transaction would be held across it (DEVELOPERS.md rule 1).
func (db *db) resolveDependencyChunk(
	ctx context.Context, jobs []*Job, groups depGroupState, cache *seenDepGroupCache,
) ([]resolvedJob, error) {
	resolved := make([]resolvedJob, 0, len(jobs))

	err := db.bolt.View(func(tx *bolt.Tx) error {
		reader := cache.depReader(txDepReader{tx: tx})

		for _, job := range jobs {
			if errc := ctx.Err(); errc != nil {
				return errc
			}

			keys, waitingForDepGroups, errd := job.Dependencies.dependencyKeys(reader, groups)
			if errd != nil {
				return errd
			}

			job.setWaitingForDepGroups(waitingForDepGroups)

			resolved = append(resolved, resolvedJob{job: job, deps: keys})
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return resolved, nil
}

// depGroupState answers whether a dep group has a live member job, without a
// database read.
type depGroupState interface {
	hasMembers(depGroup string) bool
}

// newSeenDepGroupCache returns an empty cache that counts the reads it makes
// into gets.
func newSeenDepGroupCache(gets *atomic.Uint64) *seenDepGroupCache {
	return &seenDepGroupCache{seen: make(map[string]bool), gets: gets}
}

// cachedSeenDepReader is the depReader seenDepGroupCache.depReader returns: the
// dep-group half of the reads come from the cache's memo, everything else
// straight from the wrapped reader. It holds the memo and the counter rather than
// the cache itself, so that it and seenDepGroupCache do not name each other.
type cachedSeenDepReader struct {
	depReader
	seen map[string]bool
	gets *atomic.Uint64
}

func (r cachedSeenDepReader) depGroupEverSeen(depGroup string) (bool, error) {
	if seen, cached := r.seen[depGroup]; cached {
		return seen, nil
	}

	seen, err := r.depReader.depGroupEverSeen(depGroup)
	if err != nil {
		return false, err
	}

	r.gets.Add(1)
	r.seen[depGroup] = seen

	return seen, nil
}

// depGroupsEverSeen answers each group from the cache, asking the wrapped reader
// only about the ones the pass has not asked about yet. It asks one group at a
// time because the wrapped reader is a txDepReader sharing the chunk's already
// open read transaction, where a get is a bucket read and not a transaction of
// its own.
func (r cachedSeenDepReader) depGroupsEverSeen(depGroups []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(depGroups))

	for _, depGroup := range depGroups {
		everSeen, err := r.depGroupEverSeen(depGroup)
		if err != nil {
			return nil, err
		}

		seen[depGroup] = everSeen
	}

	return seen, nil
}

// seenDepGroupCache memoises "was this dep group ever seen" for the length of
// one resolution pass, so a never-seen group named by 150,000 live jobs costs
// one bucketDepGroups get rather than 150,000. bucketDepGroups is only ever
// added to, and nothing is served during recovery, so a cached answer cannot go
// stale within a pass.
//
// resolveDependencies walks its chunks one at a time, so the memo is only ever
// used by the goroutine running the pass and needs no lock of its own; the
// counter is an atomic so a test can read it from another goroutine.
type seenDepGroupCache struct {
	seen map[string]bool
	gets *atomic.Uint64
}

// depReader wraps reader so that depGroupsEverSeen is answered from the cache,
// asking reader only about groups the pass has not asked about yet. The wrapper
// is rebuilt per chunk (each chunk has its own transaction) while the memo it
// shares lives for the whole pass.
func (c *seenDepGroupCache) depReader(reader depReader) depReader {
	return cachedSeenDepReader{depReader: reader, seen: c.seen, gets: c.gets}
}

// collectDepGroupKey applies the dep-group half of the resolution rule: a group
// with a live member blocks; a group with none that has been seen is satisfied
// and contributes nothing; a group with none that has never been seen blocks and
// is reported as waited for.
func (d *Dependency) collectDepGroupKey(
	liveMembers, seen, jobKeys, waitingForDepGroups map[string]bool,
) {
	if !liveMembers[d.DepGroup] && seen[d.DepGroup] {
		return
	}

	jobKeys[depGroupDependencyKey(d.DepGroup)] = true

	if !liveMembers[d.DepGroup] {
		waitingForDepGroups[d.DepGroup] = true
	}
}

// collectEssenceJobKey adds this essence dependency's job key to jobKeys if that
// job is still live; a job that is not live has nothing left to wait for.
func (d *Dependency) collectEssenceJobKey(reader depReader, jobKeys map[string]bool) error {
	if d.Essence == nil {
		return nil
	}

	keys, _, err := d.incompleteEssenceJobKeys(reader)
	if err != nil {
		return err
	}

	collectStrings(keys, jobKeys)

	return nil
}

func collectStrings(values []string, set map[string]bool) {
	for _, value := range values {
		set[value] = true
	}
}

func (d *Dependency) incompleteEssenceJobKeys(reader depReader) ([]string, []string, error) {
	jobKey := d.Essence.Key()

	live, err := reader.checkIfLive(jobKey)
	if err != nil {
		return []string{}, []string{}, err
	}

	if live {
		return []string{jobKey}, nil, nil
	}

	return []string{}, nil, nil
}

// Dependencies is a slice of *Dependency, for use in Job.Dependencies. It
// describes the jobs that must be complete before the Job you associate this
// with will start.
type Dependencies []*Dependency

// UnmarshalJSON accepts the public REST command dependency shape
// [{"cmd":"...","cwd":"..."}] as well as the historical struct-shaped JSON.
func (d *Dependencies) UnmarshalJSON(data []byte) error {
	var deps []dependencyViaJSON
	if err := json.Unmarshal(data, &deps); err != nil {
		return err
	}

	converted := make(Dependencies, 0, len(deps))
	for _, dep := range deps {
		converted = appendDependencyViaJSON(converted, dep)
	}

	*d = converted

	return nil
}

type dependencyViaJSON struct {
	Essence       *JobEssence `json:"essence"`
	LegacyEssence *JobEssence `json:"Essence"`
	Cmd           string      `json:"cmd"`
	Cwd           string      `json:"cwd"`
	DepGroup      string      `json:"dep_group"`
	LegacyDepGrp  string      `json:"DepGroup"`
}

func appendDependencyViaJSON(deps Dependencies, dep dependencyViaJSON) Dependencies {
	if dep.DepGroup != "" {
		return append(deps, NewDepGroupDependency(dep.DepGroup))
	}

	if dep.LegacyDepGrp != "" {
		return append(deps, NewDepGroupDependency(dep.LegacyDepGrp))
	}

	if dep.Cmd != "" || dep.Cwd != "" {
		return append(deps, NewEssenceDependency(dep.Cmd, dep.Cwd))
	}

	if dep.Essence != nil {
		return append(deps, &Dependency{Essence: dep.Essence})
	}

	if dep.LegacyEssence != nil {
		return append(deps, &Dependency{Essence: dep.LegacyEssence})
	}

	return deps
}

// dependencyKeys returns the queue dependency keys for these Dependencies - one
// depgroup:G key per unsatisfied declared group, one job key per live essence
// dependency - plus the declared groups that have never been seen.
//
// A dep group edge stays at the granularity the user declared it: one opaque key
// for the whole group, never one key per member job. Whether the group still has
// a live member is answered from groups, in memory. Only a group with no live
// member needs the database, to tell "seen, so all its jobs are done and the
// edge is satisfied" from "never seen, so the edge blocks and is reported as
// waited for"; a job whose groups all have live members therefore opens no read
// transaction for the seen check.
//
// The returned slice is built fresh for every call, and must be: Item's
// Dependencies() returns the live backing slice and ChangedKey() mutates it in
// place, so two items may never share one.
func (d Dependencies) dependencyKeys(
	reader depReader, groups depGroupState,
) ([]string, []string, error) {
	liveMembers, memberless := d.depGroupMembership(groups)

	seen, err := reader.depGroupsEverSeen(memberless)
	if err != nil {
		return []string{}, []string{}, err
	}

	jobKeys := make(map[string]bool)
	waitingForDepGroups := make(map[string]bool)

	for _, dep := range d {
		if dep.DepGroup != "" {
			dep.collectDepGroupKey(liveMembers, seen, jobKeys, waitingForDepGroups)

			continue
		}

		if errd := dep.collectEssenceJobKey(reader, jobKeys); errd != nil {
			return []string{}, []string{}, errd
		}
	}

	return sortedStringSet(jobKeys), sortedStringSet(waitingForDepGroups), nil
}

func sortedStringSet(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}

	slices.Sort(values)

	return values
}

// depGroupMembership answers, for each declared dep group, whether it has a live
// member job, and also lists the ones that do not - the only groups whose "was
// it ever seen" answer matters. Each group is asked about exactly once, so a
// membership that changes part way through a resolution cannot make one group
// look both blocked and never seen.
func (d Dependencies) depGroupMembership(groups depGroupState) (map[string]bool, []string) {
	liveMembers := make(map[string]bool)

	var memberless []string

	for _, dep := range d {
		if dep.DepGroup == "" {
			continue
		}

		if _, asked := liveMembers[dep.DepGroup]; asked {
			continue
		}

		liveMembers[dep.DepGroup] = groups.hasMembers(dep.DepGroup)

		if !liveMembers[dep.DepGroup] {
			memberless = append(memberless, dep.DepGroup)
		}
	}

	return liveMembers, memberless
}

// DepGroups returns all the DepGroups of our constituent Dependency structs.
func (d Dependencies) DepGroups() []string {
	var depGroups []string

	for _, dep := range d {
		if dep.DepGroup != "" {
			depGroups = append(depGroups, dep.DepGroup)
		}
	}

	return depGroups
}

// Stringify converts our constituent Dependency structs in to a slice of
// strings, each of which could be JobEssence or DepGroup based.
func (d Dependencies) Stringify() []string {
	var strings []string

	for _, dep := range d {
		if dep.DepGroup != "" {
			strings = append(strings, dep.DepGroup)
		} else if dep.Essence != nil {
			strings = append(strings, dep.Essence.Stringify())
		}
	}

	return strings
}

// Dependency is a struct that describes a Job purely in terms of a JobEssence,
// or in terms of a Job's DepGroup, for use in Dependencies. If DepGroup is
// specified, then Essence is ignored.
type Dependency struct {
	Essence  *JobEssence
	DepGroup string
}

// NewEssenceDependency makes it a little easier to make a new *Dependency based
// on Cmd+Cwd, for use in NewDependencies(). Leave cwd as an empty string if the
// job you are describing does not have CwdMatters true.
func NewEssenceDependency(cmd string, cwd string) *Dependency {
	return &Dependency{
		Essence: &JobEssence{Cmd: cmd, Cwd: cwd},
	}
}

// NewDepGroupDependency makes it a little easier to make a new *Dependency
// based on a dep group, for use in NewDependencies().
func NewDepGroupDependency(depgroup string) *Dependency {
	return &Dependency{
		DepGroup: depgroup,
	}
}

// resolveDependencies resolves the dependencies of every one of the given jobs,
// dependencyResolutionChunkSize jobs per bolt read transaction, returning each
// job paired with its dependency keys in the order given, and setting each job's
// WaitingForDepGroups.
//
// Resolving one job at a time costs at least one read transaction per job, plus
// one per dependency: prior-state recovery of the 150,472 live jobs production
// held on 2026-08-25 spent hundreds of thousands of them on a brand-new, wholly
// cold 7 GB database, single-threaded, before it could enqueue anything. The
// reads are the same reads, so every answer is unchanged; only the number of
// transactions is (.docs/bugfixes/260825-2.md).
//
// ctx is checked before each job, so a cancelled recovery ends the transaction
// it is in and never opens the next chunk's.
//
// One seenDepGroupCache is built for the whole pass and handed to every chunk, so
// the "ever seen" answer for a group is read once however many jobs name it:
// without it the check is per job, and the 150,472 live jobs production recovered
// naming one never-seen group would cost 150,472 gets.
func (db *db) resolveDependencies(
	ctx context.Context, jobs []*Job, groups depGroupState,
) ([]resolvedJob, error) {
	resolved := make([]resolvedJob, 0, len(jobs))
	cache := newSeenDepGroupCache(&db.depGroupSeenGets)

	// max() because slices.Chunk() panics on a chunk size below 1, and
	// dependencyResolutionChunkSize is a var that a test can set (as
	// numRPCReaders is).
	for chunk := range slices.Chunk(jobs, max(1, dependencyResolutionChunkSize)) {
		chunkResolved, err := db.resolveDependencyChunk(ctx, chunk, groups, cache)
		if err != nil {
			return nil, err
		}

		resolved = append(resolved, chunkResolved...)
	}

	return resolved, nil
}
