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
//     is put back in the live bucket, before the add replies, and queued again,
//     as an archived dependent the add read would be.
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
// mark only means anything while the job runs.
// What a crash in that window does lose is a dependent whose run ended in a
// successful archive after the add read it and before the add put it back in
// the live bucket: it is recovered complete, and is not run again.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
)

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
func (s *Server) updateLiveDependents(ctx context.Context, updates []jobDependencyUpdate) ([]*queue.ItemDef, error) {
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

	return s.resurrectArchivedDependents(ctx, gone)
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

// updateDependentUnlessRunning gives the queue item holding job, an in-memory
// job, deps to wait on, unless it is running, in which case the job is instead
// marked to run again once its run ends, and added to marks.
func (s *Server) updateDependentUnlessRunning(ctx context.Context, job *Job, deps []string,
	marks *rerunMarks) error {
	var marked, archiving bool

	_, err := s.q.UpdateUnlessRunning(ctx, job.Key(), job.getSchedulerGroup(), job, job.Priority,
		0*time.Second, s.itemTTRDuration(), deps, func() { marked, archiving = true, job.markRerunAfterRun() })

	if marked {
		marks.add(job, archiving)
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
// like the archived dependents the add read. One live again, or no longer
// complete, has been dealt with by something else.
func (s *Server) resurrectArchivedDependents(ctx context.Context, keys []string) ([]*queue.ItemDef, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	jobs, err := s.archivedNotLive(keys)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}

	if err = s.db.storeLiveForRerun(jobs); err != nil {
		return nil, err
	}

	s.updateDepGroupMembershipForNewJobs(ctx, jobs)

	itemdefs := make([]*queue.ItemDef, 0, len(jobs))
	for _, job := range jobs {
		itemdefs = append(itemdefs, s.rerunItemDef(job, s.rerunDependencies(ctx, job)))
	}

	return itemdefs, nil
}

// archivedNotLive returns the archived jobs with the given keys that are not
// live, decoded from the complete bucket and no longer reserved by anyone.
func (s *Server) archivedNotLive(keys []string) ([]*Job, error) {
	archived, err := s.db.retrieveCompleteJobsByKeys(keys)
	if err != nil {
		return nil, err
	}

	jobs := make([]*Job, 0, len(archived))

	for _, job := range archived {
		live, errl := s.db.checkIfLive(job.Key())
		if errl != nil {
			return nil, errl
		}

		if !live {
			job.ReservedBy = uuid.UUID{}
			jobs = append(jobs, job)
		}
	}

	return jobs, nil
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
	key string, bury bool, currentState JobState, job *Job) (bool, error) {
	var rerunDeps []string

	rerun := job.takeRerunAfterRun()
	if rerun {
		rerunDeps = s.rerunDependencies(ctx, job)
	}

	alreadyDone, errq := s.applyReleaseQueueChange(ctx, q, item, key, bury, currentState, job, rerunDeps)

	// a mark that was not applied above, or that arrived after it was taken but
	// before the run ended, is applied now the job is no longer running.
	if job.takeRerunAfterRun() || (rerun && (errq != nil || alreadyDone)) {
		s.applyRerunDependencies(ctx, job, s.rerunDependencies(ctx, job))
	}

	return alreadyDone, errq
}
