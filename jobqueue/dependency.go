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
	"strings"

	bolt "go.etcd.io/bbolt"
)

const neverSeenDepGroupDependencyPrefix = "depgroup-not-seen:"

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
	retrieveIncompleteJobKeysByDepGroup(depGroup string) ([]string, error)
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

func (r txDepReader) retrieveIncompleteJobKeysByDepGroup(depGroup string) ([]string, error) {
	return retrieveIncompleteJobKeysByDepGroupTx(r.tx, depGroup), nil
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
// job.setWaitingForDepGroups, which takes that Job's own mutex; that is safe
// because recoverIncompleteJobs decodes a fresh *Job per database record, so
// these pointers are private to the recovery goroutine and the lock is
// uncontended. Nothing that can block on another goroutine - the scheduler, the
// queue, a client write - may be done in here, since the transaction would be
// held across it (DEVELOPERS.md rule 1).
func (db *db) resolveDependencyChunk(ctx context.Context, jobs []*Job) ([]resolvedJob, error) {
	resolved := make([]resolvedJob, 0, len(jobs))

	err := db.bolt.View(func(tx *bolt.Tx) error {
		reader := txDepReader{tx: tx}

		for _, job := range jobs {
			if errc := ctx.Err(); errc != nil {
				return errc
			}

			keys, waitingForDepGroups, errd := job.Dependencies.incompleteJobKeys(reader)
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

func (d *Dependency) collectIncompleteJobKeys(
	reader depReader,
	seenDepGroups map[string]bool,
	jobKeys, waitingForDepGroups map[string]bool,
) error {
	keys, waiting, err := d.incompleteJobKeysWithSeen(reader, seenDepGroups)
	if err != nil {
		return err
	}

	collectIncompleteJobKeys(keys, jobKeys, waitingForDepGroups)
	collectStrings(waiting, waitingForDepGroups)

	return nil
}

func collectStrings(values []string, set map[string]bool) {
	for _, value := range values {
		set[value] = true
	}
}

func (d *Dependency) incompleteDepGroupJobKeys(
	reader depReader,
	seenDepGroups map[string]bool,
) ([]string, []string, error) {
	keys, err := reader.retrieveIncompleteJobKeysByDepGroup(d.DepGroup)
	if err != nil {
		return []string{}, []string{}, err
	}

	if len(keys) > 0 {
		return keys, nil, nil
	}

	if seenDepGroups[d.DepGroup] {
		return nil, nil, nil
	}

	return []string{neverSeenDepGroupDependencyKey(d.DepGroup)}, []string{d.DepGroup}, nil
}

func neverSeenDepGroupDependencyKey(depGroup string) string {
	return neverSeenDepGroupDependencyPrefix + depGroup
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

// incompleteJobKeys converts the constituent Dependency structs in to internal
// job keys that uniquely identify the jobs we are dependent upon. Note that if
// you have dependencies that are specified with DepGroups, then you should re-
// call this and update every time a new Job is added with one of our
// DepGroups() in its *Job.DepGroups. It will only return keys for jobs that
// are incomplete (they could have been Archive()d in the past if they are now
// being re-run).
func (d Dependencies) incompleteJobKeys(reader depReader) ([]string, []string, error) {
	seenDepGroups, err := reader.depGroupsEverSeen(d.DepGroups())
	if err != nil {
		return []string{}, []string{}, err
	}

	jobKeys, waitingForDepGroups, err := d.incompleteJobKeysByDependency(reader, seenDepGroups)
	if err != nil {
		return []string{}, []string{}, err
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

func (d Dependencies) incompleteJobKeysByDependency(
	reader depReader,
	seenDepGroups map[string]bool,
) (map[string]bool, map[string]bool, error) {
	jobKeys := make(map[string]bool)
	waitingForDepGroups := make(map[string]bool)

	for _, dep := range d {
		if dep.DepGroup == "" {
			keys, waiting, err := dep.incompleteJobKeys(reader)
			if err != nil {
				return nil, nil, err
			}

			collectIncompleteJobKeys(keys, jobKeys, waitingForDepGroups)
			collectStrings(waiting, waitingForDepGroups)

			continue
		}

		if err := dep.collectIncompleteJobKeys(reader, seenDepGroups, jobKeys, waitingForDepGroups); err != nil {
			return nil, nil, err
		}
	}

	return jobKeys, waitingForDepGroups, nil
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

// incompleteJobKeys calculates the job keys that this dependency refers to. For
// a Dependency made with Essence, you will get a single key which will be the
// same key you'd get from *Job.key() on a Job made with the same essence.
// For a Dependency made with a DepGroup, you will get the *Job.key()s of all
// the jobs in the queue and database that have that DepGroup in their
// DepGroups. You will only get keys for jobs that are currently in the queue.
func (d *Dependency) incompleteJobKeys(reader depReader) ([]string, []string, error) {
	seenDepGroups := make(map[string]bool)

	if d.DepGroup != "" {
		seen, err := reader.depGroupEverSeen(d.DepGroup)
		if err != nil {
			return []string{}, []string{}, err
		}

		seenDepGroups[d.DepGroup] = seen
	}

	return d.incompleteJobKeysWithSeen(reader, seenDepGroups)
}

func (d *Dependency) incompleteJobKeysWithSeen(
	reader depReader,
	seenDepGroups map[string]bool,
) ([]string, []string, error) {
	if d.DepGroup != "" {
		return d.incompleteDepGroupJobKeys(reader, seenDepGroups)
	}

	if d.Essence != nil {
		return d.incompleteEssenceJobKeys(reader)
	}

	return []string{}, nil, nil
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

func collectIncompleteJobKeys(keys []string, jobKeys, waitingForDepGroups map[string]bool) {
	for _, key := range keys {
		jobKeys[key] = true
		if depGroup, ok := neverSeenDepGroupFromDependencyKey(key); ok {
			waitingForDepGroups[depGroup] = true
		}
	}
}

func neverSeenDepGroupFromDependencyKey(key string) (string, bool) {
	return strings.CutPrefix(key, neverSeenDepGroupDependencyPrefix)
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
func (db *db) resolveDependencies(ctx context.Context, jobs []*Job) ([]resolvedJob, error) {
	resolved := make([]resolvedJob, 0, len(jobs))

	// max() because slices.Chunk() panics on a chunk size below 1, and
	// dependencyResolutionChunkSize is a var that a test can set (as
	// numRPCReaders is).
	for chunk := range slices.Chunk(jobs, max(1, dependencyResolutionChunkSize)) {
		chunkResolved, err := db.resolveDependencyChunk(ctx, chunk)
		if err != nil {
			return nil, err
		}

		resolved = append(resolved, chunkResolved...)
	}

	return resolved, nil
}
