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

// This file releases a job whose wr runner has moved on to another job
// (.docs/bugfixes/260930-moved-on-runner.md).
//
// A job the manager holds as running can be one its runner has already given
// up: the runner's report of the job's end was lost, for example to a manager
// crash, or was refused, and the runner went on to other jobs. Confirm-dead
// never declares such a job dead, because the runner's pid lives on, so the job
// stayed running until LostRunnerBackstop killed the runner, and with it the
// unrelated job it was then running. A manager restart restarted that clock.
//
// A `wr runner` reserves and runs jobs strictly one at a time over one client,
// and settles each job's final report (retrying it for up to ClientRetryTime)
// before it reserves the next; a runner that had any trouble reporting stops
// reserving altogether (ErrStopReserving). So once runner client X reserves,
// starts or touches job J2, any other job X reserved before J2 and that is
// still in the run sub-queue under X is a job X has dropped. It is released
// then, as a lost job confirmed dead is (lostJobReleaseReport, its pinned
// behaviours triggered), with no ssh check and nothing killed. Confirm-dead
// stays as the fallback for a runner that really died.
//
// A Go API client may legitimately hold several jobs under one client id, so
// only a reservation made by a wr runner counts. The request says so without
// any change to the protocol: it asks for a scheduler group
// (Client.ReserveScheduled) of a manager with a runner command, and it names
// the scheduler element the runner runs in (Client.SetReserveSchedulerID),
// which only `wr runner` sets, and only under LSF. The scheduler group alone is
// not enough: Go clients, this package's own tests among them, call
// ReserveScheduled and hold several jobs. A runner outside LSF names no
// element, so its dropped jobs are left to confirm-dead, as before.
//
// Such a reservation records a Job.RunnerReservation, a number larger than that
// of any earlier one, stored with the reservation so the inference also works
// on the first request a runner makes of a restarted manager: a touch carries
// neither the scheduler group nor the element.
//
// The number, not just being held by X, is what says J2 is newer: only jobs
// with a smaller one are released. The one a request is about is never
// released, nor is anything by a request about an older job, such as a touch
// or start report of J1 still in flight when the runner reserved J2.
//
// So that no touch has to look through every running job, runnerHolds indexes
// each runner client's runs in the run sub-queue by job key. It is filled at
// reservation and from the jobs recovery puts into Run, and a run leaves it
// when it is released (releaseJob, which every release and bury goes through),
// when its success is recorded (handleArchive), when its job is reserved again,
// or when it is taken here to be released. Its mutex is a leaf: nothing else is
// locked while it is held.

import (
	"context"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
)

// heldRun is one runner run of a job: the job's key and its RunnerReservation.
type heldRun struct {
	key         string
	reservation uint64
}

// releaseMovedOnRun releases the given run of a job, which its runner client
// has moved on from, exactly as killLostJobAndTriggerBehaviours releases a lost
// run confirmed dead, provided the job is still in the run sub-queue on that
// run and its success is not being recorded.
func (s *Server) releaseMovedOnRun(ctx context.Context, client uuid.UUID, run heldRun) {
	job, pin, ok := s.movedOnRunJob(client, run)
	if !ok {
		return
	}

	if err := s.releaseJob(ctx, job, lostJobReleaseReport()); err != nil {
		clog.Warn(ctx, "failed to release a job whose runner has moved on to another job", "key", run.key, "err", err)

		return
	}

	clog.Info(ctx, "released a job whose runner has moved on to another job without reporting it", "key", run.key)

	go s.triggerLostRunBehaviours(ctx, lostJobDetails{key: run.key, pin: pin})
}

// movedOnRunJob returns the job of the given run, and its behaviours pinned to
// that run, if it is still in the run sub-queue on that run of client's, not
// exited, and not having its success archived.
func (s *Server) movedOnRunJob(client uuid.UUID, run heldRun) (*Job, pinnedBehaviours, bool) {
	job := s.runSubQueueJob(run.key)
	if job == nil {
		return nil, pinnedBehaviours{}, false
	}

	job.RLock()
	defer job.RUnlock()

	if !job.isUnfinishedRunnerRunLocked(client, run.reservation) {
		return nil, pinnedBehaviours{}, false
	}

	return job, job.pinBehavioursLocked(), true
}

// runnerHolds indexes the runs that wr runner clients hold in the run
// sub-queue, and hands out the RunnerReservation numbers that order them.
type runnerHolds struct {
	mu       sync.Mutex
	byClient map[uuid.UUID]map[string]uint64
	last     uint64
}

// newRunnerHolds returns an empty runnerHolds.
func newRunnerHolds() *runnerHolds {
	return &runnerHolds{byClient: make(map[uuid.UUID]map[string]uint64)}
}

// next returns a RunnerReservation larger than any this manager has handed out
// or recovered. It is the time in nanoseconds when that is larger, so that it
// is also larger than those of a manager that ran before this one.
func (h *runnerHolds) next() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := uint64(time.Now().UnixNano())
	if n <= h.last {
		n = h.last + 1
	}

	h.last = n

	return n
}

// hold records that client holds a run of the keyed job with the given
// reservation. A reservation of 0, which was not a runner's, is ignored.
func (h *runnerHolds) hold(client uuid.UUID, key string, reservation uint64) {
	if reservation == 0 {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	runs := h.byClient[client]
	if runs == nil {
		runs = make(map[string]uint64)
		h.byClient[client] = runs
	}

	runs[key] = reservation
	h.last = max(h.last, reservation)
}

// forget removes client's run of the keyed job, if it is the one with the given
// reservation.
func (h *runnerHolds) forget(client uuid.UUID, key string, reservation uint64) {
	if reservation == 0 {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	runs := h.byClient[client]
	if runs[key] != reservation {
		return
	}

	delete(runs, key)

	if len(runs) == 0 {
		delete(h.byClient, client)
	}
}

// takeOlder removes and returns client's runs of jobs other than the keyed one
// that were reserved before the given reservation.
func (h *runnerHolds) takeOlder(client uuid.UUID, key string, reservation uint64) []heldRun {
	h.mu.Lock()
	defer h.mu.Unlock()

	runs := h.byClient[client]

	var older []heldRun

	for k, r := range runs {
		if k == key || r >= reservation {
			continue
		}

		older = append(older, heldRun{key: k, reservation: r})
		delete(runs, k)
	}

	if len(runs) == 0 {
		delete(h.byClient, client)
	}

	return older
}

// runnerReservation returns the RunnerReservation for a job being reserved by
// cr: a new one if cr is a wr runner's reservation (see the top of this file),
// otherwise 0.
func (s *Server) runnerReservation(cr *clientRequest) uint64 {
	if cr.SchedulerGroup == "" || cr.SchedulerID == "" || s.runnerCommand() == "" {
		return 0
	}

	return s.runnerHolds.next()
}

// recoverRunnerHold indexes the runner run of a job recovery put into Run.
func (s *Server) recoverRunnerHold(job *Job) {
	job.RLock()
	client, reservation := job.ReservedBy, job.RunnerReservation
	job.RUnlock()

	s.runnerHolds.hold(client, job.Key(), reservation)
}

// forgetCompletedRunnerHold removes from the index the run of a job whose
// success its runner has just reported, and clears its RunnerReservation, so
// that the job's complete record does not keep it.
func (s *Server) forgetCompletedRunnerHold(job *Job) {
	job.Lock()
	client, reservation := job.ReservedBy, job.RunnerReservation
	job.RunnerReservation = 0
	job.Unlock()

	s.runnerHolds.forget(client, job.Key(), reservation)
}

// releaseRunsMovedOnFrom releases every job client reserved as a wr runner
// before the keyed job's reservation, which client holds. A reservation of 0
// was not a runner's, and releases nothing.
//
// It runs in the request, and holds no lock while it releases: a release waits
// on no write (it is not durable, as no manager release is), and the behaviours
// it triggers run in the background.
func (s *Server) releaseRunsMovedOnFrom(ctx context.Context, client uuid.UUID, key string, reservation uint64) {
	if reservation == 0 {
		return
	}

	for _, run := range s.runnerHolds.takeOlder(client, key, reservation) {
		s.releaseMovedOnRun(ctx, client, run)
	}
}

// runSubQueueJob returns the keyed job if its item is in the run sub-queue, or
// nil.
func (s *Server) runSubQueueJob(key string) *Job {
	q := s.queueIfPresent()
	if q == nil {
		return nil
	}

	item, err := q.Get(key)
	if err != nil || item.Stats().State != queue.ItemStateRun {
		return nil
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return nil
	}

	return job
}

// isUnfinishedRunnerRunLocked reports whether this Job is on client's runner
// run with the given reservation, and that run has not ended: it has not exited
// and its success is not being archived. The caller must hold at least the
// Job's read lock.
func (j *Job) isUnfinishedRunnerRunLocked(client uuid.UUID, reservation uint64) bool {
	return j.ReservedBy == client && j.RunnerReservation == reservation &&
		!j.Exited && !j.archivePendingLocked()
}

// releaseRunsMovedOnFromJob releases every job client reserved as a wr runner
// before job, which client has just started or touched and so holds.
func (s *Server) releaseRunsMovedOnFromJob(ctx context.Context, client uuid.UUID, job *Job) {
	job.RLock()
	reservation := job.RunnerReservation
	job.RUnlock()

	s.releaseRunsMovedOnFrom(ctx, client, job.Key(), reservation)
}
