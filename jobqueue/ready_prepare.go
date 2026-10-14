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
	"context"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
)

// FailBump* are the bits of Job.FailBumpsCleared.
const (
	FailBumpRAM uint8 = 1 << iota
	FailBumpDisk
	FailBumpTime
)

// runReqs returns a copy of the RAM, Disk, DiskSet and Time of req.
func runReqs(req *scheduler.Requirements) *scheduler.Requirements {
	return &scheduler.Requirements{RAM: req.RAM, Disk: req.Disk, DiskSet: req.DiskSet, Time: req.Time}
}

// recomputedReqs is the part of a job's Requirements that
// updateJobRequirementsForRetry may change; valid is false for nil
// Requirements.
type recomputedReqs struct {
	ram     int
	disk    int
	diskSet bool
	time    time.Duration
	valid   bool
}

// recomputedReqsOf returns req's recomputedReqs.
func recomputedReqsOf(req *scheduler.Requirements) recomputedReqs {
	if req == nil {
		return recomputedReqs{}
	}

	return recomputedReqs{ram: req.RAM, disk: req.Disk, diskSet: req.DiskSet, time: req.Time, valid: true}
}

// updateJobRequirementsForRetry recomputes job's requirements: the learned
// values in recommendedReq under jobOverride, then any increase its pending
// failure calls for. It is the one recompute, made when the job fails, when an
// owner's report replaces a lost release's end state, and by the RAC for ready
// jobs.
//
// A job with a pending failure starts from its failed run's requirements
// (FailedRunReqs), or from the user's value (ModifiedReqs) for a resource a
// wr mod set since, so the result is the same however often it runs: learning
// is judged against what the run reserved, never against an earlier
// recompute's increase. The caller holds job's lock.
//
// It also invalidates the job's memoised derived scheduler-group strings, of
// which Requirements is an input, if it changed their RAM, disk or time (the
// only inputs it changes), in a defer, so that no return can skip it. The queue's
// ready hook calls it for every item entering ready under the queue's lock, so
// when the numbers come out the same the memo is kept for the hook to use.
func updateJobRequirementsForRetry(job *Job, jobOverride uint8, recommendedReq *scheduler.Requirements) {
	defer job.invalidateDerivedIfChangedLocked(recomputedReqsOf(job.Requirements))

	if job.RequirementsOrig == nil {
		job.RequirementsOrig = &scheduler.Requirements{
			RAM:     job.Requirements.RAM,
			Time:    job.Requirements.Time,
			Disk:    job.Requirements.Disk,
			DiskSet: job.Requirements.DiskSet,
		}
	}

	resetRequirementsForRecompute(job)

	// as today, this runs for jobOverrideAlwaysUseJobReqs too: it then fills
	// in only resources the user did not specify (see cmd/add.go's "If you
	// choose to override eg. only disk...").
	applyRecommendedJobRequirements(job, jobOverride, recommendedReq)

	if jobOverride == jobOverrideAlwaysUseJobReqs {
		return
	}

	applyFailureBumpsLocked(job)
}

// resetRequirementsForRecompute sets the RAM, disk and time of a job with a
// pending failure back to what its failed run reserved, or to the user's value
// for a resource whose FailBump* bit is set. A resource with neither (a record
// from an older manager) keeps its current value.
//
// A FailReasonResource job is not reset: buryImpossibleItem buries it without
// recording a run end, so its FailedRunReqs may be from an earlier failure.
func resetRequirementsForRecompute(job *Job) {
	if job.FailReason == "" || job.FailReason == FailReasonResource {
		return
	}

	if base := job.failBumpBase(FailBumpRAM); base != nil {
		job.Requirements.RAM = base.RAM
	}

	// Disk only: DiskSet is left as it is.
	if base := job.failBumpBase(FailBumpDisk); base != nil {
		job.Requirements.Disk = base.Disk
	}

	if base := job.failBumpBase(FailBumpTime); base != nil {
		job.Requirements.Time = base.Time
	}
}

// applyFailureBumpsLocked raises job's requirements for the failure its
// FailReason, PeakRAM, PeakDisk, StartTime and EndTime record, and does
// nothing for jobOverrideAlwaysUseJobReqs. A resource whose bit is set in
// FailBumpsCleared is skipped only while its value is still the user's value
// in ModifiedReqs; if learning replaced it, its increase applies as today. RAM
// is raised when FailReason is FailReasonRAM, or when
// shouldIncreaseJobRAMAfterHighPeak says so. Raise-only. Only
// updateJobRequirementsForRetry calls it, after its reset, which is what makes
// the whole recompute idempotent. The caller holds job's lock.
func applyFailureBumpsLocked(job *Job) {
	if job.Override == jobOverrideAlwaysUseJobReqs {
		return
	}

	kept := job.userKeptFailBumps()

	if kept&FailBumpRAM == 0 && shouldIncreaseJobRAMAfterHighPeak(job) {
		increaseJobRAMAfterHighPeak(job)
	}

	increaseDiskOrTimeAfterFailure(job, kept)
}

// increaseDiskOrTimeAfterFailure raises job's disk or time for a
// FailReasonDisk or FailReasonTime failure, unless the resource's bit is set
// in kept.
func increaseDiskOrTimeAfterFailure(job *Job, kept uint8) {
	switch {
	case job.FailReason == FailReasonDisk && kept&FailBumpDisk == 0:
		increaseJobDiskAfterFailure(job)
	case job.FailReason == FailReasonTime && kept&FailBumpTime == 0:
		increaseJobTimeAfterFailure(job)
	}
}

// invalidateDerivedIfChangedLocked invalidates j's memoised derived strings
// unless j's recomputedReqs still equal before. The caller holds j's write
// lock.
func (j *Job) invalidateDerivedIfChangedLocked(before recomputedReqs) {
	if before.valid && recomputedReqsOf(j.Requirements) == before {
		return
	}

	j.invalidateDerivedLocked()
}

// regroupJob gives job the scheduler group its current requirements call for
// and moves its queue item to that reserve group, if the group changed. It
// returns the snapshot it took and whether it regrouped. The caller must not
// hold the job's lock: the lock order is queue, then job.
func regroupJob(ctx context.Context, q *queue.Queue, job *Job) (schedulerGroupSnapshot, bool) {
	snapshot := job.schedulerGroupSnapshot()
	if snapshot.previousGroup == snapshot.group {
		return snapshot, false
	}

	job.setSchedulerGroup(snapshot.group)
	warnUnexpectedSetReserveGroupError(ctx, q.SetReserveGroup(snapshot.key, snapshot.group))

	return snapshot, true
}

// prepareBeforeQueueing does for each of jobs, whose items are about to be
// queued ready, what the queue's ready hook (prepareForReady) will do, but
// without the queue's lock: it recomputes the job's requirements from the
// cached learned values and, when a runner command is set, derives its
// scheduler group. The hook then normally finds nothing to change and the group
// memoised, so the queue's lock is held for less per item. The hook still
// prepares every item; this only moves the work. The caller holds no job's
// lock.
func (s *Server) prepareBeforeQueueing(jobs []*Job) {
	rc := s.runnerCommand()

	for _, job := range jobs {
		job.Lock()

		updateJobRequirementsForRetry(job, job.Override, s.currentReqGroupRecs(job.ReqGroup))

		if rc != "" {
			job.derivedLocked()
		}

		job.Unlock()
	}
}

// userKeptFailBumps returns the FailBump* bits of the resources whose
// requirement is still the value a user's wr mod set, so their failure's
// increase is not applied.
func (j *Job) userKeptFailBumps() uint8 {
	m := j.ModifiedReqs
	if m == nil {
		return 0
	}

	var kept uint8

	if m.RAM == j.Requirements.RAM {
		kept |= FailBumpRAM
	}

	if m.Disk == j.Requirements.Disk {
		kept |= FailBumpDisk
	}

	if m.Time == j.Requirements.Time {
		kept |= FailBumpTime
	}

	return kept & j.FailBumpsCleared
}

// lastRunReqs returns the requirements of the job's last run: those its failed
// run reserved (FailedRunReqs) while a failure is pending, since Requirements
// already holds that failure's increase, otherwise Requirements. It may be nil.
//
// While a run is in progress (runInProgress), that run's are Requirements. A
// reservation clears FailReason, so it can then only be FailReasonLost, which
// marking the run lost sets without recording a run end, and which a touch
// recovering it leaves set: its FailedRunReqs are still an earlier run's.
func (j *Job) lastRunReqs() *scheduler.Requirements {
	if j.FailReason != "" && !j.runInProgress() && j.FailedRunReqs != nil {
		return j.FailedRunReqs
	}

	return j.Requirements
}

// runInProgress says whether j's State is one a job normally has only while
// its queue item is in the run sub-queue, so a run is in progress:
//
//   - JobStateReserved: reserved, not yet reported started, or so shown by the
//     web interface for any item in run.
//   - JobStateRunning: reported started.
//   - JobStateLost: in run but lost, whether or not it started. A client copy
//     (copyJobForClient, which does not copy Lost) and the web interface's
//     jobs show a lost job so.
//
// Every release sets State anew, but a few paths leave one of these on a job
// that is not in run. The web interface's write of State can overwrite a
// release's (.docs/reserve-runstate/delivery-queue.md row 16), and recovery
// can put a lost State that write persisted into ready: a failed job then
// reports its Requirements, the next run's size, not its failed run's.
// Recovery also puts an old client's reservation (Pid 0) into ready with
// State reserved, which is harmless, since the reservation cleared
// FailReason.
func (j *Job) runInProgress() bool {
	switch j.State {
	case JobStateReserved, JobStateRunning, JobStateLost:
		return true
	default:
		return false
	}
}

// failBumpBase returns the requirements a recompute starts the given
// FailBump* resource from: the user's ModifiedReqs if its bit is set, else
// FailedRunReqs. Either may be nil.
func (j *Job) failBumpBase(bit uint8) *scheduler.Requirements {
	if j.FailBumpsCleared&bit != 0 {
		return j.ModifiedReqs
	}

	return j.FailedRunReqs
}

// currentReqGroupRecs returns the cached learned requirements of reqGroup,
// starting a background load if they are not current. It does no database I/O,
// so it may be called under the queue's and a job's lock.
func (s *Server) currentReqGroupRecs(reqGroup string) *scheduler.Requirements {
	rec, current := s.db.cachedReqGroupRecs(reqGroup)
	if !current {
		s.db.refreshReqGroupRecsLater(reqGroup)
	}

	return rec
}

// recomputeJobRequirements locks job and recomputes its requirements from the
// cached learned values, as after an owner's report replaces the end state of
// a lost release.
func (s *Server) recomputeJobRequirements(job *Job) {
	job.Lock()
	defer job.Unlock()

	updateJobRequirementsForRetry(job, job.Override, s.currentReqGroupRecs(job.ReqGroup))
}

// prepareForReady is the queue's ReadyHook: it prepares each job as its item
// enters ready, before any reserve can take it. It recomputes the job's
// requirements from the cached learned values and its pending failure,
// unconditionally (prepareReadyJob's guard is for the RAC's cost, not
// correctness), and, when a runner command is set, gives the job the scheduler
// group they call for and returns it as the item's reserve group. With no
// runner command it returns reserveGroup unchanged, since a group-less Reserve
// pops only group "" items. Other data is returned unchanged.
//
// It runs under the queue's lock, so it takes only the job's lock and, through
// currentReqGroupRecs, the learned-requirement cache's; it does no database
// I/O, takes no Server mutex (runnerCommand is atomic), does not count
// racScanWork and never calls the queue. It must not call
// schedulerGroupSnapshot, which takes the job lock it holds.
func (s *Server) prepareForReady(data any, reserveGroup string) string {
	job, ok := data.(*Job)
	if !ok {
		return reserveGroup
	}

	rc := s.runnerCommand()

	job.Lock()
	defer job.Unlock()

	updateJobRequirementsForRetry(job, job.Override, s.currentReqGroupRecs(job.ReqGroup))

	if rc == "" {
		return reserveGroup
	}

	job.schedulerGroup = job.derivedLocked().group

	return job.schedulerGroup
}

// prepareReadyJobAgain gives a job whose requirements changed while its item
// may be ready the reserve group they call for, as prepareReadyJob does, and
// has the RAC recount, since only runners of that group can take the item now.
// The caller must not hold the job's lock: the lock order is queue, then job.
func (s *Server) prepareReadyJobAgain(ctx context.Context, job *Job) {
	q := s.queueIfPresent()
	if q == nil || s.runnerCommand() == "" {
		return
	}

	item, err := q.Get(job.Key())
	if err != nil || item.Stats().State != queue.ItemStateReady {
		return
	}

	if _, regrouped := regroupJob(ctx, q, job); regrouped {
		s.triggerReadyAddedCallback(ctx)
	}
}

// prepareModifiedJobs prepares each of the given jobs, which a user just
// modified, whose item is ready: it recomputes the job's requirements, so the
// modification takes effect at once, and gives it the reserve group they call
// for. A modify calls it before resuming the server, so no reserve can take a
// modified ready job at its old size or from its old group. The caller must not
// hold any of the jobs' locks.
func (s *Server) prepareModifiedJobs(ctx context.Context, jobs []*Job) {
	q := s.queueIfPresent()
	if q == nil {
		return
	}

	for _, job := range jobs {
		item, err := q.Get(job.Key())
		if err != nil || item.Stats().State != queue.ItemStateReady {
			continue
		}

		s.recomputeJobRequirements(job)
		s.prepareReadyJobAgain(ctx, job)
	}
}

// userModifiedReqs sets the given FailBump* bit and returns ModifiedReqs, made if
// nil, for a user's wr mod to record the value it set. The caller holds the
// job's lock.
func (j *Job) userModifiedReqs(bit uint8) *scheduler.Requirements {
	j.FailBumpsCleared |= bit

	if j.ModifiedReqs == nil {
		j.ModifiedReqs = &scheduler.Requirements{}
	}

	return j.ModifiedReqs
}
