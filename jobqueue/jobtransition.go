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

// This file contains the single chokepoint through which every job-state
// transition updates both status projections, so neither can be forgotten.

import (
	"context"
	"slices"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/queue"
)

// countContribution is one (from -> to, n jobs in repGroup) increment applied to
// the absolute status counts. A transition may produce several (e.g. the change
// callback splits lost jobs into their own contribution).
type countContribution struct {
	from     JobState
	to       JobState
	repGroup string
	n        int
}

// emitJobTransition is the single chokepoint through which every job-state
// transition updates BOTH status projections, so a future transition path
// cannot update one and silently forget the other (which would drift the web UI
// bar counts, or make a `wr add --sync`/details subscriber miss an update).
//
// It first applies every count contribution to the authoritative absolute
// statusState (the per-RepGroup web UI bar counts; always updated, on every
// transition), then runs emitSubscriptions to deliver the per-job subscription
// updates. emitSubscriptions stays a caller-supplied closure because the two
// delivery mechanisms remain SEPARATE and the per-job projection is
// deliberately asymmetric: it is gated by subscriptionUpdateState and per-
// subscriber filtering (the change-callback path), or carries a pre-built
// update that bypasses that gate (the TTR lost update, the touch live update).
// Centralising the EMISSION here does not change WHICH updates are sent. Pass a
// nil closure for a transition with no per-job update.
//
// Lock discipline (concurrency-critical; a prior attempt deadlocked here): this
// method introduces NO new lock. applyTransition (which takes the strict-leaf
// statusState.mu) and emitSubscriptions (which takes the subscription/csmutex
// locks) are invoked strictly SEQUENTIALLY and are never nested, and this method
// holds no lock across them. Callers that run inside the queue change/TTR
// callbacks still hold queue.mutex, preserving the established acquisition order
// queue.mutex -> job -> statusState.mu (and queue.mutex -> subscription locks);
// neither statusState.mu nor any subscription lock is ever taken before the
// queue lock.
func (s *Server) emitJobTransition(counts []countContribution, emitSubscriptions func()) {
	s.statusState.applyTransitions(counts)

	if emitSubscriptions != nil {
		emitSubscriptions()
	}
}

// changeCallbackCounts builds the absolute-count contributions for a change-
// callback transition, one per RepGroup. Lost jobs transition from the lost
// state, not the running state, so they get their own contribution; the
// statusAllRepGroups aggregate is maintained inside applyTransition.
func changeCallbackCounts(from, to JobState, data []any) []countContribution {
	grouped := make(map[countContributionKey]int)

	for _, inter := range data {
		job := inter.(*Job) //nolint:errcheck,forcetypeassert
		job.Lock()
		repGroup := job.RepGroup
		lost := job.Lost
		statusFromComplete := from == JobStateNew && job.statusFromComplete
		completeRepGroups := slices.Clone(job.statusCompleteRepGroups)

		if statusFromComplete {
			job.statusFromComplete = false
		}

		job.Unlock()

		transitionFrom := from
		if from == JobStateRunning && lost {
			transitionFrom = JobStateLost
		}

		switch {
		case statusFromComplete:
			groupArchivedRerunContributions(grouped, to, repGroup, completeRepGroups)
		default:
			grouped[countContributionKey{transitionFrom, to, repGroup}]++
			groupHistoricalCompletionContributions(grouped, to, repGroup, completeRepGroups)
		}
	}

	counts := make([]countContribution, 0, len(grouped))
	for contribution, count := range grouped {
		counts = append(counts, countContribution{
			from: contribution.from, to: contribution.to, repGroup: contribution.repGroup, n: count,
		})
	}

	return counts
}

func groupArchivedRerunContributions(grouped map[countContributionKey]int, to JobState,
	repGroup string, completeRepGroups []string,
) {
	currentMoved := false

	for _, completedRepGroup := range completeRepGroups {
		if completedRepGroup == repGroup {
			grouped[countContributionKey{JobStateComplete, to, repGroup}]++
			currentMoved = true

			continue
		}

		grouped[countContributionKey{from: JobStateComplete, repGroup: completedRepGroup}]++
	}

	if !currentMoved {
		grouped[countContributionKey{to: to, repGroup: repGroup}]++
	}
}

func groupHistoricalCompletionContributions(grouped map[countContributionKey]int, to JobState,
	repGroup string, completeRepGroups []string,
) {
	if to != JobStateComplete {
		return
	}

	for _, completedRepGroup := range completeRepGroups {
		if completedRepGroup != repGroup {
			grouped[countContributionKey{to: JobStateComplete, repGroup: completedRepGroup}]++
		}
	}
}

type countContributionKey struct {
	from     JobState
	to       JobState
	repGroup string
}

// changeCallbackToState resolves the destination JobState for a change-callback
// event. Items removed from the queue are either deleted or completed, so that
// case is disambiguated by inspecting the jobs' own state.
func changeCallbackToState(toQ queue.SubQueue, data []any) JobState {
	if toQ != queue.SubQueueRemoved {
		return subqueueToJobState[toQ]
	}

	for _, inter := range data {
		job := inter.(*Job) //nolint:errcheck,forcetypeassert
		job.RLock()
		jState := job.State
		job.RUnlock()

		if jState == JobStateComplete {
			return JobStateComplete
		}
	}

	return JobStateDeleted
}

// jobKeyAndRepGroup reads a job's key and RepGroup under its read lock.
func jobKeyAndRepGroup(job *Job) (string, string) {
	job.RLock()
	defer job.RUnlock()

	return job.Key(), job.RepGroup
}

// emitChangeCallbackTransition is the queue change-callback's transition
// emission. It disambiguates the from/to JobStates, then routes both
// projections through emitJobTransition: the absolute per-RepGroup counts
// (with lost jobs tallied from the lost state, not running) and the per-job
// subscription updates (gated by subscriptionUpdateState and per-subscriber
// filtering, exactly as before).
func (s *Server) emitChangeCallbackTransition(ctx context.Context, fromQ, toQ queue.SubQueue, data []any) {
	from := subqueueToJobState[fromQ]
	to := changeCallbackToState(toQ, data)
	state, emit := subscriptionUpdateState(from, to)
	includeKeyStateChange := from == JobStateSuspended || to == JobStateSuspended

	s.emitJobTransition(changeCallbackCounts(from, to, data), func() {
		if !emit {
			return
		}

		s.enqueueChangeCallbackSubscriptions(ctx, data, to, state, includeKeyStateChange)
	})
}

// enqueueChangeCallbackSubscriptions delivers the per-job subscription updates
// for a subscription-relevant change-callback transition from a single per-job
// status loop, applying the same per-subscriber gating as before: at least one
// client must want the update (hasClientSubscriptionsForJobUpdate), and running
// jobs wait briefly for their start time before the status snapshot. Each job is
// converted to a status exactly once, and that single status feeds the
// subscription update (it is never separately written to the browser here).
//
// Idle fast-path: when there are no JobUpdate client subscriptions at all (the
// common case - no `wr add --sync` key subscription and no repGroup subscription
// for these jobs), it returns before the per-job loop after a single
// csmutex.RLock, skipping the per-job allocations and contended RLocks that would
// otherwise deliver nothing. This activates regardless of whether a status web
// UI is connected, because the web UI does not rely on this per-job JobUpdate
// delivery. This is purely the per-job subscription delivery; the absolute
// per-RepGroup status counts have already been applied by emitJobTransition
// before this closure runs, so a web UI client that connects LATER still gets
// correct seed counts. The early csmutex.RLock is in the same async
// change-callback context as the existing per-job
// hasClientSubscriptionsForJobUpdate RLock, so it adds no new lock and no new
// nesting (the order queue.mutex -> subscription locks is preserved).
func (s *Server) enqueueChangeCallbackSubscriptions(
	ctx context.Context, data []any, to, state JobState, includeKeyStateChange bool,
) {
	if !s.hasAnyClientSubscriptions() {
		return
	}

	for _, inter := range data {
		job := inter.(*Job) //nolint:errcheck,forcetypeassert

		jobKey, repGroup := jobKeyAndRepGroup(job)
		if !s.hasClientSubscriptionsForJobUpdate(jobKey, repGroup, state, includeKeyStateChange) {
			continue
		}

		if to == JobStateRunning {
			waitForJobStartTime(job)
		}

		status, err := job.ToStatus()
		if err != nil {
			clog.Warn(ctx, "failed to convert job to status", "err", err)

			continue
		}

		status.IsPushUpdate = true

		started, ended := jobUpdateTimes(job)
		s.enqueueSubscriptionUpdate(jobUpdateFromStatus(status, state, started, ended), includeKeyStateChange)
	}
}
