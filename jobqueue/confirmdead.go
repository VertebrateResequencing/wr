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

// This file implements the confirm-dead coordinator: when jobs are marked lost,
// their death is confirmed by ssh'ing to the exec host to `ps` the command (and
// runner) pid. When a whole exec node dies, all of its jobs go lost together, and
// confirming each independently would dial one ssh connection PER CHECK (getHost
// -> dial -> close), i.e. ~2 connections per lost job. The coordinator instead
// COLLECTS the pending checks, GROUPS them by host, and processes each host's
// batch over ONE reused connection (scheduler.ProcessesNotRunningOnHost), bounded
// by concurrent HOSTS -- collapsing a dead node's ~2K dials into ~1. It preserves
// the both-pid liveness verdict and the retry/backstop cadence of the old
// per-check confirmJobDead path exactly (see .docs/reliable4/freeze-fix-plan.md
// Fix 5 / .docs/bugfixes/260729-3.md).

import (
	"context"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
)

// confirmDeadCoalesceWindow is how long a freshly-started per-host worker waits
// for more checks to accumulate before its first ssh round. When a whole exec
// node dies its jobs go lost together within a sub-millisecond burst, so this
// short pause lets them all be checked over ONE grouped connection instead of the
// worker draining the first arrival(s) and dialling again for each that trickles
// in. It is far below the (seconds-scale) lost-job retry time and TTR, so it never
// meaningfully delays a genuinely-dead job's reclamation, and it only applies on
// the lost-job failure path.
const confirmDeadCoalesceWindow = 50 * time.Millisecond

// confirmDeadCoordinator groups lost jobs' confirm-dead ssh checks by host so all
// of a dead host's pid checks share one ssh connection. A per-host worker
// goroutine drains that host's queued checks in batches, and the number of hosts
// being ssh-checked at once is bounded by the Server's confirmDeadLimiter.
type confirmDeadCoordinator struct {
	server  *Server
	mu      sync.Mutex
	pending map[string][]lostJobDetails // host -> checks awaiting a worker
	active  map[string]bool             // host -> a worker goroutine is draining it
}

// newConfirmDeadCoordinator returns a coordinator that drives server's
// confirm-dead checks. server must be fully constructed (the coordinator uses its
// scheduler, confirmDeadLimiter, timings and stopClientHandling).
func newConfirmDeadCoordinator(server *Server) *confirmDeadCoordinator {
	return &confirmDeadCoordinator{
		server:  server,
		pending: make(map[string][]lostJobDetails),
		active:  make(map[string]bool),
	}
}

// enqueue submits a lost job's confirm-dead check, grouping it with any other
// checks pending for the same host. The first check for an idle host starts a
// per-host worker goroutine; further checks for that host join its queue and are
// picked up by that worker in the same or the next ssh round. It is safe to call
// concurrently from the many goroutines markJobLost spawns.
func (c *confirmDeadCoordinator) enqueue(ctx context.Context, d lostJobDetails) {
	c.mu.Lock()
	c.pending[d.host] = append(c.pending[d.host], d)

	startWorker := !c.active[d.host]
	if startWorker {
		c.active[d.host] = true
	}
	c.mu.Unlock()

	if startWorker {
		go c.runHostWorker(ctx, d.host)
	}
}

// runHostWorker drains one host's pending checks, processing them in grouped ssh
// batches until none remain. Re-reading the queue after each batch means checks
// that arrived during an ssh round are still grouped (into the next batch). It
// holds the host "active" for its whole lifetime; hostWorkerFinished (deferred)
// closes the enqueue handshake so there is never a lost check nor more than one
// worker per host, even if a batch panics.
func (c *confirmDeadCoordinator) runHostWorker(ctx context.Context, host string) {
	defer c.hostWorkerFinished(ctx, host)

	// let a burst of same-host checks (a whole node dying at once) accumulate
	// before the first ssh round, so they group onto one connection.
	if !c.coalesce() {
		return
	}

	for {
		c.mu.Lock()
		batch := c.pending[host]
		delete(c.pending, host)
		c.mu.Unlock()

		if len(batch) == 0 {
			return
		}

		c.processBatch(ctx, host, batch)
	}
}

// coalesce waits confirmDeadCoalesceWindow, returning true, or false at once if
// the server is shutting down (so a worker started as the server stops does no
// work and exits promptly).
func (c *confirmDeadCoordinator) coalesce() bool {
	timer := time.NewTimer(confirmDeadCoalesceWindow)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-c.server.stopClientHandling:
		return false
	}
}

// stopping reports (without blocking) whether the server has begun shutting down,
// i.e. stopClientHandling has been closed.
func (c *confirmDeadCoordinator) stopping() bool {
	select {
	case <-c.server.stopClientHandling:
		return true
	default:
		return false
	}
}

// hostWorkerFinished closes out a host worker. It first recovers and logs any
// panic from batch processing, so one host's exceptional failure cannot crash the
// manager. Then, atomically with the empty check, it either marks the host
// inactive or -- if checks raced in during the tiny stop/panic window, leaving
// pending non-empty -- restarts a worker so those checks are never orphaned.
func (c *confirmDeadCoordinator) hostWorkerFinished(ctx context.Context, host string) {
	if r := recover(); r != nil {
		clog.Error(ctx, "jobqueue confirm-dead host worker panicked", "host", host, "err", r)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// on shutdown, abandon any straggler checks rather than restart a worker
	// (which would immediately coalesce-exit and could spin).
	if c.stopping() {
		delete(c.pending, host)
		delete(c.active, host)

		return
	}

	if len(c.pending[host]) == 0 {
		delete(c.active, host)

		return
	}

	go c.runHostWorker(ctx, host)
}

// processBatch runs one grouped ssh round for a host and applies each job's
// verdict. The ssh round (checkHost) is bounded by the per-host confirmDeadLimiter
// and abandoned cleanly on server stop.
func (c *confirmDeadCoordinator) processBatch(ctx context.Context, host string, batch []lostJobDetails) {
	notRunning := c.checkHost(ctx, host, batch)

	for _, d := range batch {
		c.applyVerdict(ctx, d, notRunning)
	}
}

// checkHost performs the grouped ssh round for a batch, returning a map from pid
// to not-running (nil when there is nothing to check). It holds one slot in the
// per-host confirmDeadLimiter for the duration of the ssh work, so the number of
// hosts ssh-checked at once stays bounded; a server stop received while waiting
// for a slot abandons the round (returning nil, so the jobs are simply retried),
// keeping Stop() unblocked. The whole round shares one host connection and the
// batch's check timeout.
func (c *confirmDeadCoordinator) checkHost(ctx context.Context, host string, batch []lostJobDetails) map[int]bool {
	pids := batchPids(batch)
	if len(pids) == 0 {
		return nil
	}

	s := c.server

	select {
	case s.confirmDeadLimiter <- struct{}{}:
	case <-s.stopClientHandling:
		return nil
	}

	defer func() { <-s.confirmDeadLimiter }()

	ctx, cancel := context.WithTimeout(ctx, batch[0].checkTimeout)
	defer cancel()

	return s.scheduler.ProcessesNotRunningOnHost(ctx, host, pids)
}

// pidsPerCheck is the most pids a single confirm-dead check contributes to a
// batch: the job's command pid and, when reported, its runner pid. It only sizes
// the batchPids working slices.
const pidsPerCheck = 2

// batchPids returns the distinct, positive pids a batch of confirm-dead checks
// must test on the host: each job's command pid, plus its runner pid when one was
// reported (> 0). A non-positive pid is skipped (a job with no command pid is
// never confirmed dead). Deduplicated so a pid shared by several checks is tested
// only once over the shared connection.
func batchPids(batch []lostJobDetails) []int {
	seen := make(map[int]bool, len(batch)*pidsPerCheck)
	pids := make([]int, 0, len(batch)*pidsPerCheck)

	add := func(pid int) {
		if pid > 0 && !seen[pid] {
			seen[pid] = true

			pids = append(pids, pid)
		}
	}

	for _, d := range batch {
		add(d.pid)
		add(d.runnerPid)
	}

	return pids
}

// applyVerdict acts on one job's confirm-dead result. Confirmed dead: kill the job
// and trigger its behaviours (off the worker goroutine, exactly as the old path
// did). Not confirmed dead: retry after the job's checkRetryTime, honouring the
// wedged-runner backstop and re-enqueuing so retries group by host too.
func (c *confirmDeadCoordinator) applyVerdict(ctx context.Context, d lostJobDetails, notRunning map[int]bool) {
	if jobConfirmedDead(d, notRunning) {
		go c.server.killLostJobAndTriggerBehaviours(ctx, d)

		return
	}

	go c.retryAfter(ctx, d)
}

// jobConfirmedDead reports whether a lost job's check result means the job is
// really dead, preserving confirmJobDead's exact both-pid semantics: the job is
// dead only if it has a command pid (!= 0) that is not running AND -- when a
// runner pid was reported (> 0) -- the runner is also not running. A live runner
// (which may still archive a completed job) is never declared dead, so its success
// is never re-run underneath it; a job with no command pid (0) is never dead. With
// no runner pid (old records) it falls back to the command-pid-only verdict.
func jobConfirmedDead(d lostJobDetails, notRunning map[int]bool) bool {
	if d.pid == 0 || !notRunning[d.pid] {
		return false
	}

	if d.runnerPid > 0 {
		return notRunning[d.runnerPid]
	}

	return true
}

// retryAfter re-checks a not-yet-confirmed-dead job after checkRetryTime, mirroring
// the old confirmJobDeadAndKillAfterRetryTime: after the wait it refreshes the
// job's current pids and how long it has been parked Lost, force-kills a wedged
// runner once that exceeds LostRunnerBackstop (so the re-check then finds both
// pids gone and re-runs the job via the normal path), and re-enqueues the check so
// the retry groups by host too. It stops cleanly on server shutdown, and drops the
// retry if the job is no longer a parked-Lost running job.
func (c *confirmDeadCoordinator) retryAfter(ctx context.Context, d lostJobDetails) {
	s := c.server

	timer := time.NewTimer(d.checkRetryTime)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-s.stopClientHandling:
		return
	}

	retry, ok := s.lostJobRetryCheck(d.key)
	if !ok {
		return
	}

	if backstop := s.timings.LostRunnerBackstop; backstop > 0 && retry.lostFor > backstop {
		s.backstopKillWedgedRunner(ctx, retry)
	}

	c.enqueue(ctx, lostJobDetails{
		key:            retry.key,
		host:           retry.host,
		pid:            retry.pid,
		runnerPid:      retry.runnerPid,
		checkTimeout:   retry.checkTimeout,
		checkRetryTime: d.checkRetryTime,
		pin:            retry.pin,
	})
}
