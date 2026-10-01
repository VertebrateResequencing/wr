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

// This file is how a job that is running when a dep group it depends on gains
// a member is run again, the way a complete job that depends on that group is
// (.docs/bugfixes/260929-running-dependent-rerun.md).
//
// Once an add has stored its new jobs, it applies their dependencies to each
// live dependent it read (updateLiveDependents):
//
//   - one still queued and not running gets them, as it always has;
//   - one running is left alone, so its touches and final report are accepted
//     as normal. Instead it is marked Job.RerunAfterRun, while the queue is
//     locked and the item is seen to be running, so the run cannot end between
//     the check and the mark, and the mark is stored before the add replies. The
//     job is encoded for that inside the write's transaction, which writes
//     nothing once a successful completion of the run is being archived: the
//     archive's transaction then owns the live record, and one encoded earlier
//     could otherwise land after it;
//   - one that has been archived and has left the queue since the add read it
//     is queued again, as an archived dependent the add read would be, having
//     been put back in the live bucket as described below. Either way, the
//     archive's dropping of its dep group memberships and rep group lookup is
//     ordered against the add registering them again (see bringback.go).
//
// A dependent the add reads complete, and puts back in the live bucket to run
// again, may still have its item in the run queue, its archive having written
// but not yet removed the item. Queueing it again would find that item and count
// it a duplicate, and the archive would then remove the item, so it is marked
// instead, the same way as a running one (rerunArchivingItems).
//
// What reaches disk must not depend on the add getting that far, since a
// manager that crashes after the add's write, before it replies, would
// otherwise recover such a dependent complete, never to run again, and the
// client's retry of the add finds the new jobs already there. So from before it
// writes anything until it has done the above, the add guards the live
// dependents it read that are still queued (rerunGuard), and between them the
// add's write and a dependent's archive, whichever of their transactions comes
// second, keep it live:
//
//   - the add's write, in the transaction that stores its jobs in the live
//     bucket, puts back there, as a job to run again, any of them it finds
//     archived (db.putBackArchivedDependentsTx);
//   - an archive, in its own transaction, keeps a guarded job live to run again
//     if the add's first job is already in the live bucket (Job.archiveOutcome),
//     the job's item leaving the queue as normal for the add to queue it again.
//
// Neither needs the job's live record to carry anything, so no later rewrite of
// that record from memory, such as its start, can undo it, and a dependent that
// was queued rather than running when the add read it, but ran and completed
// before the add got to it, is covered too. The add's first job is the smallest
// key it stores, so an add big enough to be stored in several transactions
// (storeBatched) stores it, and puts back its archived dependents, in its first
// live bucket transaction. An add that fails after its write, before it queues
// such a job again, leaves it live but out of the queue. A retry of the add then
// reads it as a live dependent that has left the queue, and queues it without
// storing it again (archivedToRerun); without a retry it stays out of the queue
// until a restart, which runs it after the new member, as the write asks.
//
// How a marked run ends decides what the mark does:
//
//   - A successful archive whose transaction finds the mark records the
//     completion in the complete bucket and, in the same transaction, keeps the
//     job in the live bucket; the item then goes back to dependent (or ready)
//     with the job's dependencies as they are now. If the mark arrives after the
//     archive's transaction deleted the live record, while the item is still in
//     the run queue, the mark is stored with a live record of its own, and the
//     archive, deciding under the queue's lock, leaves the item where it is and
//     sends it back the same way, so jobs that depend on it keep waiting.
//   - A release or a lost job confirmed dead is released as normal, except that
//     it waits on the new member first (dependent rather than delayed).
//   - A bury buries it as normal, with the new dependencies recorded, as an add
//     does to a job that was already buried: a kick then makes it dependent.
//
// A crash after the add's write but before it stored a mark leaves the job
// running with an unresolved dep group dependency, since the new member cannot
// have run yet, and recovery marks such a job again (recoverRunningDependent).
// Recovery clears the mark on any job it does not recover as running, since the
// mark only means anything while the job runs. A dependent whose run was
// archived in that window was kept live, as above, so it is recovered waiting on
// the new member, with its completion recorded.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	bolt "go.etcd.io/bbolt"
)

// maxDependentUpdateAttempts bounds how many times updateDependentUnlessRunning
// finds the item it is updating holding a different job than it read. Each
// retry needs the key's item to have been archived away and queued afresh again
// in the meantime.
const maxDependentUpdateAttempts = 3

// isDepGroupDependencyKey reports whether a queue dependency key stands for a
// dep group rather than a job.
func isDepGroupDependencyKey(key string) bool {
	return strings.HasPrefix(key, depGroupDependencyPrefix)
}

// rerunMarks are the jobs just marked to run again once their runs end: those
// still running, and those whose successful completion is being archived.
type rerunMarks struct {
	running, archiving []*Job
}

// add notes that job was marked; archiving is what markRerunAfterRun returned.
func (m *rerunMarks) add(job *Job, archiving bool) {
	if archiving {
		m.archiving = append(m.archiving, job)
	} else {
		m.running = append(m.running, job)
	}
}

// updateLiveDependents applies an add's gathered dependency updates to the jobs
// already in the queue, not to the copies the add decoded from the database,
// and does what running_dependent.go describes for those that are running or
// have left the queue, returning the item definitions of the latter to queue.
func (s *Server) updateLiveDependents(ctx context.Context, updates []jobDependencyUpdate,
	wasPutBack func(key string) bool) ([]*queue.ItemDef, error) {
	var (
		marks rerunMarks
		gone  []string
	)

	defer func() { s.storeRerunMarks(ctx, marks) }()

	for _, update := range updates {
		key := update.job.Key()

		job := s.queuedJob(key)
		if job == nil {
			gone = append(gone, key)

			continue
		}

		job.setWaitingForDepGroups(update.waitingForDepGroups)

		err := s.updateDependentUnlessRunning(ctx, job, update.deps, &marks)
		if queueErrorIs(err, queue.ErrNotFound) {
			gone = append(gone, key)
		} else if err != nil {
			return nil, err
		}
	}

	return s.resurrectArchivedDependents(ctx, gone, wasPutBack)
}

// applyRerunDependencies gives the queue item of job, whose run has ended but
// which must run again, deps to wait on, marking it again should it be running
// once more.
func (s *Server) applyRerunDependencies(ctx context.Context, job *Job, deps []string) {
	var marks rerunMarks

	if err := s.updateDependentUnlessRunning(ctx, job, deps, &marks); err != nil {
		clog.Warn(ctx, "failed to make a job wait on its new dependencies", "key", job.Key(), "err", err)
	}

	s.storeRerunMarks(ctx, marks)
}

// rerunArchivingItems marks to run again, as updateLiveDependents marks a
// running dependent, the in-memory job of each item an add would queue whose key
// is still queued by a successful completion being archived: the add read the
// job complete after the archive's write, and its own write put the job back in
// the live bucket, so the mark has the archive keep the item and send it back to
// wait on its dependencies. It returns the item definitions of the others, and
// how many it marked. A mark made while the job is still being archived is not
// stored again, since the add's write already stored the job live.
func (s *Server) rerunArchivingItems(ctx context.Context, itemdefs []*queue.ItemDef) ([]*queue.ItemDef,
	int, error) {
	var (
		marks     rerunMarks
		remaining []*queue.ItemDef
		marked    int
	)

	defer func() {
		marks.archiving = nil
		s.storeRerunMarks(ctx, marks)
	}()

	for _, itemdef := range itemdefs {
		ok, err := s.rerunArchivingItem(ctx, itemdef, &marks)
		if err != nil {
			return nil, marked, err
		}

		if !ok {
			remaining = append(remaining, itemdef)

			continue
		}

		marked++
	}

	return remaining, marked, nil
}

// updateHolderUnlessRunning is one attempt of updateDependentUnlessRunning,
// returning a queue.ErrDataChanged error if the item no longer holds job.
func (s *Server) updateHolderUnlessRunning(ctx context.Context, job *Job, deps []string, marks *rerunMarks) error {
	var marked, archiving bool

	if dependentReadHook != nil {
		dependentReadHook(job.Key())
	}

	_, err := s.q.UpdateHolderUnlessRunning(ctx, job.Key(), job.getSchedulerGroup(), job, job.Priority,
		0*time.Second, s.itemTTRDuration(), deps, func() { marked, archiving = true, job.markRerunAfterRun() })

	if marked {
		marks.add(job, archiving)
	}

	return err
}

// rerunArchivingItem is rerunArchivingItems for one item definition, reporting
// whether it marked the job its key is queued by.
func (s *Server) rerunArchivingItem(ctx context.Context, itemdef *queue.ItemDef, marks *rerunMarks) (bool, error) {
	job := s.queuedJob(itemdef.Key)
	if job == nil || job == itemdef.Data || !job.archivePending() {
		return false, nil
	}

	err := s.updateDependentUnlessRunning(ctx, job, s.rerunDependencies(ctx, job), marks)
	if queueErrorIs(err, queue.ErrNotFound) {
		return false, nil
	}

	return err == nil, err
}

// updateDependentUnlessRunning gives the queue item holding job, an in-memory
// job, deps to wait on, unless it is running, in which case the job is instead
// marked to run again once its run ends, and added to marks.
//
// The item may have stopped holding job since it was read, its archive having
// removed it and another add having queued a fresh copy of the job under the
// key. Putting job on that item would lose the copy, and a mark made on job
// would not reach its run, so the update is made to the copy instead, with its
// dependencies as they are now: the copy may have been given dependencies worked
// out before the member the caller is adding was registered.
func (s *Server) updateDependentUnlessRunning(ctx context.Context, job *Job, deps []string,
	marks *rerunMarks) error {
	key := job.Key()

	var err error

	for attempt := range maxDependentUpdateAttempts {
		err = s.updateHolderUnlessRunning(ctx, job, deps, marks)
		if !queueErrorIs(err, queue.ErrDataChanged) || attempt == maxDependentUpdateAttempts-1 {
			break
		}

		job = s.queuedJob(key)
		if job == nil {
			return queue.Error{Queue: s.q.Name, Op: "Update", Item: key, Err: queue.ErrNotFound}
		}

		// unlike rerunDependencies, a failure to work out the holder's
		// dependencies fails the update, as one when the add gathered its updates
		// fails the add, rather than leaving the holder to run on none of them
		var waitingForDepGroups []string

		deps, waitingForDepGroups, err = job.Dependencies.dependencyKeys(s.db, s.depGroups)
		if err != nil {
			return err
		}

		job.setWaitingForDepGroups(waitingForDepGroups)
	}

	return err
}

// storeRerunMarks durably writes the marks, so they survive a restart. A running
// job's record is rewritten only while it is still running: once a successful
// completion of it is being archived, the archive's transaction finds the mark
// and keeps it live (see storeRunningRerunMarks). A job being archived gets a
// live record whether or not its archive has deleted the one it had. A failure
// is only logged: the mark is still acted on by this manager, and a restart while
// the job is still running marks it again as long as the member it waits on is
// still live.
func (s *Server) storeRerunMarks(ctx context.Context, marks rerunMarks) {
	if err := s.db.storeRunningRerunMarks(marks.running); err != nil {
		clog.Warn(ctx, "failed to store that running jobs must run again", "err", err)
	}

	if len(marks.archiving) == 0 {
		return
	}

	if err := s.db.storeLiveForRerun(marks.archiving); err != nil {
		clog.Warn(ctx, "failed to store that completing jobs must run again", "err", err)
	}
}

// rerunGuard is an add's hold on the live dependents it read, from before it
// writes anything until it has given them its dependencies (see
// running_dependent.go). An archive of one of them in that time keeps it live to
// run again if the add's write has committed, which the archive's transaction
// tells by member, the first job the add stores in the live bucket, being there.
type rerunGuard struct {
	member []byte
	jobs   []*Job

	mu   sync.Mutex
	kept map[string]bool
}

// release takes g off the jobs guardDependents put it on.
func (g *rerunGuard) release() {
	for _, job := range g.jobs {
		job.Lock()

		job.rerunGuards = slices.DeleteFunc(job.rerunGuards, func(h *rerunGuard) bool { return h == g })
		if len(job.rerunGuards) == 0 {
			job.rerunGuards = nil
		}

		job.Unlock()
	}
}

// keeps reports whether g's add has stored its jobs, as seen in a transaction
// whose live bucket is live, and if so notes that the keyed job's archive keeps
// it live for the add to queue again.
func (g *rerunGuard) keeps(live *bolt.Bucket, key string) bool {
	if live.Get(g.member) == nil {
		return false
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.kept == nil {
		g.kept = make(map[string]bool)
	}

	g.kept[key] = true

	return true
}

// keptLive reports whether an archive kept the keyed job live because of g.
func (g *rerunGuard) keptLive(key string) bool {
	if g == nil {
		return false
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	return g.kept[key]
}

// guardDependents returns the storeNewJobsGuarded callback that puts g on the
// in-memory jobs of the dependents it is given that are still queued.
func (s *Server) guardDependents(g *rerunGuard) func(dependents []*Job, member []byte) {
	return func(dependents []*Job, member []byte) {
		g.member = member

		for _, dependent := range dependents {
			job := s.queuedJob(dependent.Key())
			if job == nil {
				continue
			}

			job.Lock()
			job.rerunGuards = append(job.rerunGuards, g)
			job.Unlock()

			g.jobs = append(g.jobs, job)
		}
	}
}

// markRerunAfterRun marks the job to run again once its run ends. The caller
// must have seen the job's item running with the queue locked, and still hold
// that lock. It reports whether a successful completion of the run is already
// being archived, in which case the mark must be stored with a live record of
// its own (see storeRerunMarks).
func (j *Job) markRerunAfterRun() (archiving bool) {
	j.Lock()
	defer j.Unlock()

	j.RerunAfterRun = true

	return j.archivePendingLocked()
}

// rerunAfterRun reports whether the job is marked to run again once its run
// ends.
func (j *Job) rerunAfterRun() bool {
	j.RLock()
	defer j.RUnlock()

	return j.RerunAfterRun
}

// takeRerunAfterRun clears and returns RerunAfterRun.
func (j *Job) takeRerunAfterRun() bool {
	j.Lock()
	defer j.Unlock()

	rerun := j.RerunAfterRun
	j.RerunAfterRun = false

	return rerun
}

// completionArchived returns the EndTime of the job's successful completion, and
// whether an archive of that completion has already been written (see
// Job.archivedEndTime).
func (j *Job) completionArchived() (time.Time, bool) {
	j.RLock()
	defer j.RUnlock()

	return j.EndTime, !j.archivedEndTime.IsZero() && j.archivedEndTime.Equal(j.EndTime)
}

// endArchive releases the hold on the run queue that one accepted successful
// completion took (see archivePendingLocked), once its archive has finished. It
// reports whether the job must now run again: this was the last archive of the
// completion to finish, and the job is marked. The mark and the job's
// reservation are then forgotten, so no report of the ended run can be applied
// to it again. Leaving that to the last archive keeps the mark for the
// transactions of any archive of the same completion still in flight.
func (j *Job) endArchive() bool {
	j.Lock()
	defer j.Unlock()

	j.archivesPending--

	if j.archivesPending > 0 || !j.RerunAfterRun {
		return false
	}

	j.RerunAfterRun = false
	j.ReservedBy = uuid.UUID{}

	return true
}

// archiveOutcome returns what the archive of the job's successful completion,
// in a transaction whose live bucket is live, must do with its live record. A
// job marked to run again is kept live, and its item stays in the queue
// (archiveKeptLive). So is a job an add guarding it has stored its jobs for,
// making it a dependent whose run ended before the add could give it its
// dependencies; but its item is removed like that of any archived job
// (archiveRemovedLive), since the add, which will find it gone, queues it again,
// as it would one whose archive came before its write (see rerunGuard.keeps).
// Any other job leaves the live bucket, and keepLive is false.
func (j *Job) archiveOutcome(live *bolt.Bucket) (outcome archiveOutcome, keepLive bool) {
	j.RLock()
	defer j.RUnlock()

	if j.RerunAfterRun {
		return archiveKeptLive, true
	}

	if len(j.rerunGuards) == 0 {
		return archiveRemovedLive, false
	}

	key := j.Key()

	for _, g := range j.rerunGuards {
		if g.keeps(live, key) {
			return archiveRemovedLive, true
		}
	}

	return archiveRemovedLive, false
}

// queuedJob returns the in-memory job of the keyed queue item, or nil if there is
// no such item.
func (s *Server) queuedJob(key string) *Job {
	item, err := s.q.Get(key)
	if err != nil {
		return nil
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return nil
	}

	return job
}

// resurrectArchivedDependents puts back in the live bucket, and returns item
// definitions to queue, the jobs with the given keys that an add read as live
// dependents but that have since been archived and left the queue. Their runs
// ended before the add could give them its new dependencies, so they run again,
// like the archived dependents the add read. Those wasPutBack reports on have
// already been put back in the live bucket because of this add, as have those
// live but not queued, left so by an add that failed or has yet to queue them
// (see archivedToRerun). Any other live again, or no longer complete, has been
// dealt with by something else.
func (s *Server) resurrectArchivedDependents(ctx context.Context, keys []string,
	wasPutBack func(key string) bool) ([]*queue.ItemDef, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	jobs, notLive, err := s.archivedNotQueued(keys, wasPutBack)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}

	if err = s.storeLiveForRerunIfAny(notLive); err != nil {
		return nil, err
	}

	s.updateDepGroupMembershipForNewJobs(ctx, jobs)

	itemdefs := make([]*queue.ItemDef, 0, len(jobs))
	for _, job := range jobs {
		itemdefs = append(itemdefs, s.rerunItemDef(job, s.rerunDependencies(ctx, job)))
	}

	return itemdefs, nil
}

// archivePending is archivePendingLocked, taking the job's read lock.
func (j *Job) archivePending() bool {
	j.RLock()
	defer j.RUnlock()

	return j.archivePendingLocked()
}

// storeLiveForRerunIfAny is db.storeLiveForRerun, unless there are no jobs.
func (s *Server) storeLiveForRerunIfAny(jobs []*Job) error {
	if len(jobs) == 0 {
		return nil
	}

	return s.db.storeLiveForRerun(jobs)
}

// archivedNotQueued returns the archived jobs with the given keys that are not
// queued (see archivedToRerun), decoded from the complete bucket and no longer
// reserved by anyone, and separately those of them not already live.
func (s *Server) archivedNotQueued(keys []string, wasPutBack func(key string) bool) (jobs, notLive []*Job,
	err error) {
	archived, err := s.db.retrieveCompleteJobsByKeys(keys)
	if err != nil {
		return nil, nil, err
	}

	jobs = make([]*Job, 0, len(archived))

	for _, job := range archived {
		rerun, alreadyLive, errr := s.archivedToRerun(job, wasPutBack)
		if errr != nil {
			return nil, nil, errr
		}

		if !rerun {
			continue
		}

		if !alreadyLive {
			notLive = append(notLive, job)
		}

		job.ReservedBy = uuid.UUID{}
		jobs = append(jobs, job)
	}

	return jobs, notLive, nil
}

// archivedToRerun reports whether archivedNotQueued returns the archived job,
// and whether it is already in the live bucket, so needs no storing: wasPutBack
// reports on it, or it is live but not queued. The latter was put back live, or
// kept live, by an add (maybe this one's earlier try) that failed before
// queueing it, or that has yet to queue it, in which case queueing it here
// finds a duplicate there or in the other add.
func (s *Server) archivedToRerun(job *Job, wasPutBack func(key string) bool) (rerun, alreadyLive bool, err error) {
	key := job.Key()
	if wasPutBack(key) {
		return true, true, nil
	}

	live, err := s.db.checkIfLive(key)
	if err != nil || !live {
		return err == nil, false, err
	}

	return s.queuedJob(key) == nil, true, nil
}

// rerunDependencies returns the dependencies a job about to run again must wait
// on now, recording on it any dep groups it waits for that have never been seen.
// If they cannot be worked out, that is logged and it waits on nothing.
func (s *Server) rerunDependencies(ctx context.Context, job *Job) []string {
	deps, waitingForDepGroups, err := job.Dependencies.dependencyKeys(s.db, s.depGroups)
	if err != nil {
		clog.Error(ctx, "failed to get the dependencies of a job to run again", "key", job.Key(), "err", err)

		return nil
	}

	job.setWaitingForDepGroups(waitingForDepGroups)

	return deps
}

// rerunItemDef returns the definition of a new queue item for a job to run again
// once deps are satisfied.
func (s *Server) rerunItemDef(job *Job, deps []string) *queue.ItemDef {
	return &queue.ItemDef{
		Key: job.Key(), ReserveGroup: job.getSchedulerGroup(), Data: job, Priority: job.Priority,
		TTR: s.itemTTRDuration(), Dependencies: deps,
	}
}

// requeueRerun sends the item of a job whose archive endArchive said must run
// again back to wait on its dependencies as they are now. The item is still in
// the queue, since an archive does not remove the item of a marked job (see
// finishArchive); should a TTR expiry have moved it on from the run sub-queue,
// it is given those dependencies where it is. The job is still live, so it keeps
// its dep group memberships and rep group lookup.
func (s *Server) requeueRerun(ctx context.Context, job *Job, key, sgroup string) {
	deps := s.rerunDependencies(ctx, job)

	err := s.q.Requeue(ctx, key, deps)
	if queueErrorIs(err, queue.ErrNotRunning) {
		err = nil

		s.applyRerunDependencies(ctx, job, deps)
	}

	if err != nil {
		clog.Warn(ctx, "failed to requeue a completed job to run again", "key", key, "err", err)
	}

	clog.Debug(ctx, "completed job, which will run again", "key", key, "cmd", job.loggableCmd())
	s.decrementGroupCount(ctx, sgroup, 1)
}

// recoverRerunMark gives a recovered job the mark that it must run again once its
// run ends if, and only if, it still needs it: a job recovered into the run
// sub-queue as recoverRunningDependent says, and no other. The mark only means
// anything while the job runs, so one stored with a job that is not is stale.
func (s *Server) recoverRerunMark(ctx context.Context, job *Job, itemdef *queue.ItemDef, intoRun bool) {
	if intoRun {
		s.recoverRunningDependent(ctx, job, itemdef)

		return
	}

	job.Lock()
	job.RerunAfterRun = false
	job.Unlock()
}

// recoverRunningDependent keeps a recovered running job running, with no
// dependencies on its queue item, when it has unresolved dependencies: the queue
// would otherwise put it in the dependent sub-queue and reject its runner's
// reports, running it twice.
//
// If one of those is a dep group dependency, a dep group it depends on gained a
// member after its run started, so in case the manager stopped before the add
// stored its mark, it is marked here (and the mark stored) to run again once its
// run ends. That is the case whichever way the group gained the member: added,
// as running_dependent.go describes, or modified into the group (which, without
// a restart, reruns no dependent of the group, running or complete). An
// unresolved command dependency marks nothing: a command dependency added again
// reruns no dependent of it, running or complete, so a restart does not either.
func (s *Server) recoverRunningDependent(ctx context.Context, job *Job, itemdef *queue.ItemDef) {
	deps := itemdef.Dependencies
	if len(deps) == 0 {
		return
	}

	itemdef.Dependencies = nil

	if !slices.ContainsFunc(deps, isDepGroupDependencyKey) {
		return
	}

	if job.markRecoveredRerun() {
		return
	}

	if err := s.db.storeRunningRerunMarks([]*Job{job}); err != nil {
		clog.Warn(ctx, "failed to store that a recovered running job must run again", "key", job.Key(), "err", err)
	}
}

// markRecoveredRerun marks a recovered running job to run again once its run
// ends, reporting whether its stored record already had the mark.
func (j *Job) markRecoveredRerun() (stored bool) {
	j.Lock()
	defer j.Unlock()

	stored = j.RerunAfterRun
	j.RerunAfterRun = true

	return stored
}

// applyReleaseQueueChangeForRerun is applyReleaseQueueChange for a job that may
// be marked to run again because a dep group it depends on gained a member
// during this run: such a job waits on that member instead of being released
// straight back.
func (s *Server) applyReleaseQueueChangeForRerun(ctx context.Context, q *queue.Queue, item *queue.Item,
	snap releaseSnapshot, job *Job) (releaseOutcome, error) {
	var rerunDeps []string

	rerun := job.takeRerunAfterRun()
	if rerun {
		rerunDeps = s.rerunDependencies(ctx, job)
	}

	outcome, errq := s.applyReleaseQueueChange(ctx, q, item, snap, job, rerunDeps)

	// a mark that was not applied above, or that arrived after it was taken but
	// before the run ended, is applied now the job is no longer running.
	if job.takeRerunAfterRun() || (rerun && (errq != nil || outcome == releaseAlreadyDone)) {
		s.applyRerunDependencies(ctx, job, s.rerunDependencies(ctx, job))
	}

	return outcome, errq
}
