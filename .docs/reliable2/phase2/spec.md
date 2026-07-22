# reliable2 Phase 2 Specification

## Overview

Phase 1 ("Option R", merged into branch `reliable2`) fixed false-lost /
false-deleted and slow startup, but a real `portal_builder` deployment and a
fresh isolated-LSF reinvestigation (`.docs/reliable2/phase2/repro.md`,
`ideas.md`) surfaced remaining failures that would not have happened on v0.36.5:
completion churn from double-reserved jobs (repro Issue B), a failed-job release
livelock that pins runners for 24h (Issue B2), control/status RPC
unresponsiveness under a large runner fleet (Issues 1-3), and a web-vs-CLI
completed-count divergence (Issue 4). This spec implements the chosen fixes.

Reliable job execution is the top priority; web-UI count accuracy is explicitly
secondary and reverts to v0.36.5 quality. Three internal changes plus one
deliberately-accepted user-facing regression (the web count revert):

1. **Counter-only web front-end revert** - remove the absolute per-RepGroup
   counter machinery (`repGroupCounts`, `jstateAbsolute`) and restore v0.36.5's
   concurrent change-callback dispatch and `statusCaster`/`jstateCount` delta
   feed. Fixes Issue 4 and de-serialises the transition hot path.
2. **Idea 5 - concurrent RPC readers** - admit RPCs with N concurrent
   `RecvMsg()` reader goroutines so control/status commands cannot queue behind
   the runner fleet. Fixes Issues 1-3.
3. **Idea 2 - double-reservation prevention** - (a) make the
   reserved-not-started reclaim liveness-confirmed instead of
   `StartTime`-based; (b) never `bkill` an LSF array element wr has handed a
   reservation to. Fixes Issue B.
4. **Idea 1 - release-livelock give-up** - a live manager that authoritatively
   reports the reservation is gone returns `ErrBadJob` so the runner abandons
   the dead reservation, while keeping the 24h retry for manager-crash recovery.
   Fixes Issue B2.

All except the web revert are internal-only (no user-facing behaviour change
beyond the bug fix). The reworked build MUST open a DB already upgraded by
current code without error or data loss.

## Architecture

### Priority vs implementation order

The list above is the user's priority order. The build order (see
Implementation Order) is revert -> readers -> double-reservation -> livelock,
because the revert de-serialises dispatch and rewrites the transition/queue
files everything else touches, and the livelock give-up depends on the KEEP'd
recovery window (unchanged) already being in place.

### Files touched

- `jobqueue/repgroupcounts.go` - DELETE (whole file; the absolute counter).
- `jobqueue/jobtransition.go` - drop the counter half of `emitJobTransition`;
  repurpose count contributions to drive the restored delta feed.
- `jobqueue/server.go` - restore `jstateCount` type + `statusCaster *caster`;
  split `ttrCallback`; spin N reader goroutines in `serveClients`; delete
  `jstateAbsolute`, the `s.repGroupCounts` field + init.
- `jobqueue/serverCLI.go` - `handleRelease` uses `getij(cr, true)`; record
  host/pid at reserve in `respondWithReservedJob` (stop zeroing them in
  `resetJobForReservation`); replace counter emission on touch-recovery.
- `jobqueue/serverWebI.go` - replace `setupStatusStateUpdateListener`/
  `sendStatusStateUpdates` with the reused
  `setupUpdateListener(... statusCaster ...)` + v0.36.5 scan-on-connect
  (`case "current"`).
- `jobqueue/client.go` - send host + `os.Getpid()` in the reserve request.
- `queue/queue.go` - restore v0.36.5's concurrent `go queue.changedCb(...)`;
  delete the serial-drainer machinery.
- `jobqueue/scheduler/scheduler.go` - add a `reserved`/`Reserved` scheduler
  method (reserved-element tracking).
- `jobqueue/scheduler/lsf.go` - track reserved array elements; never `bkill` a
  tracked element in `killExcessCmds`/`killCollector`.
- `cmd/runner.go` - compute and pass the runner's LSF element id at reserve.
- `jobqueue/static/js/wr/websocket-handler.js` - EDIT ONLY the status-bar count
  branch (`Counts` -> `FromState` delta consumer). Do NOT literally restore
  v0.36.5's file (that would delete KEEP front-ends).

### Types

Restore v0.36.5's delta message (currently absent) alongside the retained
`caster`/`setupUpdateListener` infrastructure already used by
`badServerCaster`/`schedCaster`:

```go
// jstateCount is a from->to state-count delta sent to the status web page:
// the count in FromState drops by Count, the count in ToState rises by Count.
type jstateCount struct {
    RepGroup  string // "+all+" aggregates all live jobs across RepGroups
    FromState JobState
    ToState   JobState
    Count     int
}
```

`clientRequest` (client.go:213) gains three fields carrying the reserving
runner's own identity and scheduler element id (binc-tolerant additive fields;
an old runner sends the zero values -> pid 0 -> old-client fallback):

```go
Host          string // runner host, sent in a reserve request (Idea 2a)
Pid           int    // runner os.Getpid(), sent in a reserve request (Idea 2a)
SchedulerID   string // scheduler element id, e.g. LSF "jobid[index]" (Idea 2b)
```

Scheduler reserved-element tracking (new method on the existing `scheduleri`
interface + public `*Scheduler` wrapper; non-LSF impls no-op):

```go
// Reserved records that a scheduler element (opaque, scheduler-specific id,
// e.g. LSF "jobid[index]") has been handed a wr job reservation, so it must not
// be killed as excess.
func (s *Scheduler) Reserved(schedulerID string)
```

### Error handling

Reuse existing `Err*` string constants (server.go): `ErrBadJob` (not in queue /
wrong sub-queue), `ErrMustReserve` (caller not owner), `ErrRecovering`
(retryable, recovery window), `ErrInternalError`. No new error types. The client
give-up set stays exactly `ErrBadJob`/`ErrBadRequest`
(`handleFinalStateError`, client.go:2123); connection errors are neither, so
crash-recovery retries continue.

### DB compatibility

No schema change. The reworked build opens a current-code-upgraded DB
unchanged. The removed counter never persisted a bucket, so there is no dead
bucket to tolerate beyond those already handled in phase 1. The three new
`clientRequest` fields are wire-only (never stored). Existing phase-1 DB-compat
coverage stays green; no new fixture needed.

### Concurrency invariants (N1b - the revert MUST preserve these)

The restored concurrent `go queue.changedCb(...)` fan-out runs every transition
callback on its own goroutine, which now also drives the RETAINED #503
subscription delivery concurrently. This was verified safe. The revert MUST:

1. **DECISIVE:** actually remove the `repGroupCounts.applyTransitions` call from
   the per-transition path. That call took one EXCLUSIVE `repGroupCounts.mu`
   across the batch on every transition (repgroupcounts.go:86-106); leaving
   it re-serialises the restored concurrent dispatch regardless of #503. Its
   replacement, `statusCaster.Send`, takes only a shared `RLock` to snapshot
   members plus a tiny per-web-client mutex and a non-blocking buffered
   send (server.go:654-673) - no server-wide exclusive lock on the hot path.
2. Keep the `hasAnyClientSubscriptions()` early-out at the top of
   `enqueueChangeCallbackSubscriptions` (jobtransition.go:200-202): one
   `csmutex.RLock` + `len()`, then return when no client subscribes.
3. Keep `csmutex` an `RWMutex`; per-transition access stays `RLock`. The only
   `csmutex.Lock` writers are subscribe/unsubscribe/shutdown
   (server_subscription.go:338/351/467); never add a per-transition `Lock`.
4. Never hold `csmutex` (or any server-wide lock) across the actual client
   delivery - delivery stays a buffered channel send after the lock is released
   (server_subscription.go:504-521).
5. Use `statusCaster.Send` for the web-bar counts; do NOT reintroduce any single
   server-wide exclusive mutex on the per-transition path.

Verify BOTH under `-race` AND under load (control-op responsiveness while a
fleet churns), not `-race` alone.

### Constraints

- Ideas 1/2/5 internal-only; the web revert is the one accepted user-facing
  change (counts revert to v0.36.5 flicker/overcount quality).
- go-conventions: copyright headers on new files, GoConvey `So()` assertions,
  `t.TempDir()`. TDD: each behavioural test fails before, passes after.
- Build/test with `-tags netgo`; unset ALL `OS_*` env vars for `make test` /
  `make race` (keeps them fast). Tier-A tests run under `-race`.
- Tier-B isolated-LSF validation uses the DEVELOPMENT deployment (ports
  51780/51781) only, never production; be a good farm citizen (considerate
  scale, force jobs to an appropriate queue, expect fair-share to cap
  concurrency); `bkill` all `wrd_*` after; kill the dev pid directly (verify it
  is the dev binary) because `wr manager stop` hangs under load.

---

## A. Counter-only web front-end revert

Prompt sec 1, notes N1/N1a/N1b. Fixes repro Issue 4; de-serialises the hot path
contributing to Issues 1-3. Priority 1.

### A1: Remove the absolute per-RepGroup counter; split the chokepoint

As a maintainer, I want the diverging absolute counter gone and the transition
chokepoint to drive only the (restored) delta feed and the KEEP'd #503
subscription delivery, so there is no accumulating counter to drift and no
per-transition exclusive serialiser.

Remove:
- `jobqueue/repgroupcounts.go` (whole file): `repGroupCounts`,
  `repGroupCountsSubscriber`, `newRepGroupCounts`, `applyTransitions`,
  `applyToRepGroupLocked`, `markDirtyLocked`, `wholeMap(Locked)`,
  `liveSeedLocked`, `rgcSeedCountCopy`, `rgcHasLiveJob`, `subscribe`,
  `unsubscribe`, `drain`, and the exclusive `mu`.
- `s.repGroupCounts` field (server.go) and its `newRepGroupCounts()` init
  (server.go:2487).
- `jstateAbsolute` type (server.go:504).
- In `emitJobTransition` (jobtransition.go:75-81): the
  `s.repGroupCounts.applyTransitions(counts)` call (N1b requirement 1). Keep the
  `emitSubscriptions()` half. The `counts []countContribution` still flow, but
  now drive `statusCaster.Send` deltas (A3) rather than the counter.
- The counter reference on the touch-recovery path (serverCLI.go:943-955,
  `recoverLostTouchedJob`) and the lost-transition path (server.go:3400-3407):
  keep the subscription enqueue; route the count as a `jstateCount` delta (A3).

`changeCallbackCounts`/`contributionsFromGrouped`/`countContributionKey` MAY be
kept as the delta-grouping helper (they already group per (from,to,repGroup) and
tally lost jobs from the lost state, which maps 1:1 to `jstateCount`), or
inlined; behaviour is pinned by A3 tests, not structure.

KEEP unchanged: #503 subscription delivery
(`enqueueChangeCallbackSubscriptions`, `server_subscription.go`,
`subscription.go`), `hasAnyClientSubscriptions`, live RAM/CPU/STDOUT
(`emitLiveTouchSnapshot`), reconnect/resync, `wr add --sync`.

**Package:** `jobqueue/` **File:** `jobqueue/jobtransition.go`,
`jobqueue/server.go`, `jobqueue/serverCLI.go` **Test file:**
`jobqueue/reliable2_webrevert_test.go` (new); delete
`jobqueue/repgroupcounts_test.go`.

**Acceptance tests (map to Issue 4):**

1. Given the built `jobqueue` package, when it compiles, then `repGroupCounts`,
   `newRepGroupCounts`, `jstateAbsolute`, `repgroupcounts.go` are not symbols
   (compile-time proof the counter is gone). Fail-before: they exist.
2. Given a subscriber to a rep group and a job run to success
   (`Exitcode==0`), when it completes, then the subscriber still receives a
   terminal `JobUpdate` with to-state `JobStateComplete` and never
   `JobStateDeleted` (KEEP #503 delivery unaffected by removing the counter).
   Fail-before: n/a (regression guard); pass-after: holds.

### A2: Restore v0.36.5 concurrent change-callback dispatch

As the manager, I want each transition batch dispatched on its own goroutine (as
v0.36.5 did) rather than through a single serial drainer, so transition
callbacks (and the retained #503 delivery) run concurrently and one exclusive
per-transition serialiser is gone.

In `queue/queue.go`:
- Restore `changed()` to v0.36.5's body: if `changedCb != nil`, build `data` and
  `go queue.changedCb(from, to, data)` (11fe092:queue/queue.go:325-334). Restore
  `SetChangedCallback` to a plain setter.
- DELETE the serial-drainer machinery introduced by #547: `runChangedCallbacks`
  (queue.go:262), `finishChangedCallbacks` (:283), `nextChangedCallback` (:297),
  the `changedCbPending`/`changedCbRunning` fields (:415/:419), and
  `changedNotification` if unused. Keep `changedCbMutex` only if still needed
  to guard the `changedCb` setter (v0.36.5 did not lock; a mutex-guarded setter
  is acceptable).

Accepted consequence (N1/N1b): transition-callback ORDERING is no longer
guaranteed (v0.36.5 behaviour). Web-UI count/update ordering reverts to v0.36.5
quality. This is why the ordering-pinning tests below are removed.

**Package:** `queue/` **File:** `queue/queue.go` **Test file:**
`queue/queue_test.go` (edit).

**Acceptance tests:**

1. Given a queue with a `changedCb` set, when a batch of items transitions, then
   the callback runs on a goroutine distinct from the caller (the transition
   method returns without waiting for the callback body) - proving concurrent
   dispatch. Fail-before: n/a; pass-after: holds.
2. Given two overlapping transition batches, when their callbacks run, then they
   may execute concurrently (a callback that blocks does not block the other) -
   the v0.36.5 fan-out. Fail-before: the serial drainer forced sequential order;
   pass-after: concurrent.
3. DELETE the serial-drainer ordering / Goexit tests (queue_test.go) reverted
   away: `TestQueueChangedCallbacksPreserveTransitionOrder` (:2013),
   `TestQueueChangedCallbacksFollowConcurrentTransitionOrder` (:2051), and
   `TestQueueChangedCallbacksContinueAfterGoexit` (:2421). No remaining test may
   assert transition-order preservation.

### A3: Restore the v0.36.5 status-bar delta feed + scan-on-connect

As the web UI, I want per-RepGroup status bars fed by v0.36.5's `jstateCount`
delta broadcast with an incomplete-only scan-on-connect, so counts work the
v0.36.5 way with no accumulating server-side map.

Server (reuse the existing `caster`/`setupUpdateListener`, NOT bcast):
- Add `statusCaster *caster` field, init `newCaster()` in `serve` (beside
  `badServerCaster`/`schedCaster`, server.go:2488/2492), `Broadcasting(0)` at
  startup and `Close()` at shutdown (mirror server.go:2628/5590).
- The transition chokepoint emits one
  `statusCaster.Send(&jstateCount{RepGroup, FromState, ToState, Count})` per
  grouped contribution (from A1's counts, including lost-from-lost and
  touch-recovery lost->running).
- In `webInterfaceStatusWS` (serverWebI.go:394), replace the
  `setupStatusStateUpdateListener` call with
  `s.setupUpdateListener(ctx, conn, stopper, storedName, s.statusCaster, "status
  updater")` (identical pattern to serverWebI.go:433/436). DELETE
  `setupStatusStateUpdateListener` and `sendStatusStateUpdates`
  (serverWebI.go:924-993) and `statusStateSendThrottle`.
- Restore the v0.36.5 scan-on-connect: on the `{Request:"current"}` message
  (`readStatusWSRequests`), send `webInterfaceStatusSendGroupStateCount` for
  `"+all+"` from `getJobsCurrent(...)` (incomplete jobs only) and, on a
  per-RepGroup request, that group's `getJobsCurrent` +
  `getCompleteJobsByRepGroup` (11fe092:serverWebI.go:253-277, :510-528). Restore
  `webInterfaceStatusSendGroupStateCount`.

Web JS (`websocket-handler.js`, EDIT ONLY the status-bar branch; N1a):
- Change the message dispatch (line 180): replace the
  `json.hasOwnProperty('Counts')` -> `handleAbsoluteStateMessage` branch with a
  `json.hasOwnProperty('FromState')` -> delta handler that decrements
  `FromState` and increments `ToState` by `Count` on the RepGroup tracker (and
  the `"+all+"` inflight tracker), v0.36.5-style. Remove
  `handleAbsoluteStateMessage`/`setTrackerCounts` absolute-replace logic.
- KEEP unchanged: the `State` (IsPushUpdate live job-detail pushes), `IP`
  (bad-server), and `Msg` (scheduler) branches; the reconnect fresh-state reset
  (`resetLiveCounts`, line 148); the `{Request:"current"}` send on open; the
  `inflight-tracking.js` import; and `modify-job.js`. Do NOT wholesale-restore
  v0.36.5's `websocket-handler.js`.

**Package:** `jobqueue/` **File:** `jobqueue/server.go`,
`jobqueue/serverWebI.go`, `jobqueue/static/js/wr/websocket-handler.js`
**Test file:** `jobqueue/serverWebI_test.go` (edit: `jstateAbsolute` ->
`jstateCount`), `jobqueue/reliable2_webrevert_test.go`.

**Acceptance tests (map to Issue 4):**

1. Given a connected `/status_ws` client and a job in rep group `rg` going
   new->ready->running->complete, when the transitions occur, then the client
   receives `jstateCount` messages (fields `RepGroup`, `FromState`, `ToState`,
   `Count`) whose applied deltas leave `rg`'s live counts matching the run, and
   `"+all+"` tracks the live total. Fail-before: the client received
   `jstateAbsolute{RepGroup,Counts}`; pass-after: `jstateCount` deltas.
2. Given a manager restarted preserving a DB with N prior completed jobs in `rg`
   and no absolute counter, when a client connects and requests `"current"`
   before any new transition, then it is sent no `complete` seed for the
   terminal-only `rg` (v0.36.5 scan-on-connect uses incomplete-only
   `getJobsCurrent`), while the CLI scan
   `GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)`
   still returns
   `Counts[JobStateComplete] == N`. This is the Issue-4 fix: no server-side
   absolute counter to diverge; CLI stays a scan. Fail-before: the counter
   reported `rg` complete as 0 while the CLI reported N (the divergence);
   pass-after: there is no diverging counter and the terminal-only `rg`
   is simply absent from the live feed, matching v0.36.5.
3. (Browser guard, belt-and-suspenders on A3.1/A3.2) Given the phase-1 JS
   status-bar edit (absolute `Counts` consumer -> `FromState` delta consumer),
   when `make browser-test` runs, then the count-display browser fixture
   `repgroup-bar-flicker` stays green; update it only as strictly needed to
   reflect the reverted v0.36.5-quality delta count DISPLAY, never weakening
   its bar-rendering assertions. The server-side Go tests (A3.1/A3.2) remain
   the primary guard.

### A4: Terminal-hiding-on-refresh retained

As a web user, I want a page refresh to NOT re-show a completed-only rep group
in the status bars, so the phase-1 terminal-hiding fix is not lost by the
revert.

This is retained essentially for free: v0.36.5's scan-on-connect seeds `"+all+"`
from `getJobsCurrent` (incomplete jobs only, server.go ~4823), so a
completed-only rep group is naturally excluded from a fresh connection's seed. A
rep group that COMPLETES while connected stays visible via the live
running->complete delta (260625-6). The spec REQUIRES this property
and pins it with a test; the mechanism moves from the removed
`liveSeedLocked`/`repGroupCounts` to the restored incomplete-only scan.

**Package:** `jobqueue/` **File:** `jobqueue/serverWebI.go` **Test file:**
`jobqueue/serverWebI_test.go`.

**Acceptance tests:**

1. Given a rep group whose only jobs are complete (terminal-only), when a
   `/status_ws` client connects and requests `"current"`, then it is sent no
   live seed for that rep group (terminal-hiding on refresh). Fail-before: n/a
   (regression guard preserving 260626-2/260716-1/260721-1); pass-after: holds.
2. Given a rep group with live jobs that then complete WHILE a client is
   connected, when the completions occur, then the client receives the
   running->complete `jstateCount` deltas and the rep group stays visible
   (260625-6).
3. (Browser guard, belt-and-suspenders on A4.1/A4.2) Given the phase-1 JS
   status-bar edit, when `make browser-test` runs, then the terminal-hiding /
   refresh browser fixtures `removed-jobs-refresh` and
   `completed-repgroup-visibility` stay green; update them only as strictly
   needed to reflect the reverted v0.36.5-quality count DISPLAY, never
   weakening their terminal-hiding / refresh assertions (a refreshed connection
   still shows nothing for a terminal-only rep group; a rep group completing
   while connected stays visible). The server-side Go tests (A4.1/A4.2) remain
   the primary guard.

---

## B. Concurrent RPC readers (Idea 5)

Prompt sec 4, note N4. Fixes repro Issues 1-3. Priority 2 in build order (after
the revert de-serialises dispatch).

### B1: N concurrent RecvMsg readers on the existing socket

As an operator, I want status/control RPCs (`wr status`, `wr limit`,
`wr suspend`) admitted without queuing behind reserve/touch/archive traffic, so
they stay responsive under a churning fleet.

Today a single reader admits one RPC at a time: `serveClients` loops
`receiveClientMessage` -> `sock.RecvMsg()` (server.go:2656/2671), dispatching
each to its own goroutine. Change admission to N concurrent readers on the SAME
`xrep` mangos socket (server.go:2396). No new port, no wire/protocol change;
stay wire-compatible with existing runners and CLIs.

Design (proven safe against mangos v3.4.2):
- `xrep.socket.OpenContext()` returns `protocol.ErrProtoOp`, so mangos Contexts
  are unavailable on this raw REP socket; the design is raw concurrent
  `RecvMsg()`, not Contexts.
- Concurrent `RecvMsg()` is safe: it snapshots channel refs under a brief lock
  then does a `select`-receive on the shared `recvQ` channel; Go channel
  receives fan out one message per receiver, so N reader goroutines admit
  distinct messages without cross-talk.
- Reply routing is unaffected: each received message carries its pipe-ID header
  (set by the pipe receiver); `reply` -> `s.sock.SendMsg(m)` (serverCLI.go:1977)
  uses that header, and `SendMsg` already runs concurrently today (per-goroutine
  dispatch). Which reader admitted a request does not change which client gets
  the reply.
- Launch `numRPCReaders` goroutines running the `serveClients` loop (server.go:
  2538) sharing `stopClientHandling`. `clientHandlingDone` must close only after
  ALL readers exit (e.g. a `sync.WaitGroup`), so `waitForClientHandling`
  (server.go:1144) still blocks until serving has fully stopped.
- `numRPCReaders`: a small fixed constant (e.g. 4-8), documented; a package var
  so tests can lower/raise it. Not user-configurable (internal-only).

If concurrent handling on this socket proves genuinely unsafe (a documented wr
foot-gun), surface it as a blocker per agent-conduct rather than switching
designs silently.

**Package:** `jobqueue/` **File:** `jobqueue/server.go` **Test file:**
`jobqueue/reliable2_readers_test.go` (new).

**Acceptance tests (map to Issues 1-3):**

1. (Safety, `-race`) Given a server with `numRPCReaders > 1` and M concurrent
   clients each issuing a distinct request-reply RPC round-trip, when they all
   run, then every client receives exactly its own correct reply (no misrouted,
   dropped, or duplicated reply) and the `-race` detector reports no data race.
   Fail-before: n/a (new capability); this is the N4 HARD REQUIREMENT proof.
2. (Admission fairness, supporting) Given a server saturated by many concurrent
   goroutines issuing reserve/touch RPCs in a tight loop, when a control RPC
   (e.g. `GetStatusByRepGroupMatch` or a limit/suspend call) is issued, then it
   returns within a bounded time (low seconds, no 60s timeout) with
   `numRPCReaders > 1`. Fail-before (single reader, `numRPCReaders = 1`): the
   control RPC is starved. Note: this in-process test SUPPORTS but is not the
   sole evidence; the headline responsiveness claim is Tier B (real LSF at
   scale).

---

## C. Double-reservation prevention (Idea 2)

Prompt sec 3, notes N2/N3. Fixes repro Issue B (the churn root). Priority 3 in
build order.

### C1: Report and record host+pid at reserve

As the manager, I want a reserved job to carry the reserving runner's host and
pid immediately (before `Started`), so the reserved-not-started reclaim (C2) can
confirm death via the scheduler independently of the backlogged RPC stream.

- Client: `Reserve`/`ReserveScheduled` (client.go:865/891) set `Host` =
  `os.Hostname()` and `Pid` = `os.Getpid()` on the reserve `clientRequest`. This
  is the runner's OWN pid (overwritten by the command's pid at `Started`,
  applyJobStart, serverCLI.go:893). Do NOT piggyback on the touch stream (that
  stream is exactly what B decouples). No new RPC.
- Server: in `respondWithReservedJob` (serverCLI.go:813), record `cr.Host` and
  `cr.Pid` onto the reserved job. In `resetJobForReservation` (serverCLI.go:842-
  844) STOP zeroing `Host`/`Pid` (leave `StartTime` zeroed - it is still set at
  `Started`). Old client (no host+pid) -> pid stays 0.

**Package:** `jobqueue/` **File:** `jobqueue/client.go`,
`jobqueue/serverCLI.go` **Test file:** `jobqueue/reliable2_reserve_test.go`
(new).

**Acceptance tests:**

1. Given a client that reserves a job, when the reservation is returned, then
   the server-side job has `Host` == the client's host and `Pid` ==
   `os.Getpid()` of the reserving process, BEFORE any `Started` call.
   Fail-before: `resetJobForReservation` zeroed them; pass-after: recorded.
2. Given a reserve request carrying no host+pid (old-client shape), when it is
   handled, then the job's `Pid` is 0 and reservation still succeeds (backward
   compatible).

### C2: Liveness-confirmed reclaim of reserved-not-started jobs

As the manager, I want a reserved-not-started job whose TTR expires to be
parked in Run and requeued only after its runner is confirmed dead (never on a
`StartTime.IsZero()` proxy), so a live-but-backlogged runner's job is never
re-reserved, while a genuinely dead runner's job is still reclaimed.

In `ttrCallback` (server.go:3349-3357) split the
`if job.StartTime.IsZero() || job.Exited` branch:
- `job.Exited` (a released/finished item awaiting delay): unchanged -> return
  `queue.SubQueueDelay`.
- reserved-not-started (`StartTime.IsZero() && !job.Exited`): treat like the
  started path - set `Lost=true`, `FailReason=FailReasonLost`, `EndTime=now`,
  return `queue.SubQueueRun` (parked, un-reservable), and defer `markJobLost`
  (server.go:3375). `markJobLost` snapshots `job.Host`/`job.Pid` (server.go:
  3387-3388) and `confirmOrReleaseLostJob` -> `confirmJobDead` (server.go:4180)
  -> `ProcessNotRunningOnHost` confirms death, then `killJob` requeues.
- The already-`Lost` early return (server.go:3361) is unchanged (a parked job is
  not re-marked/re-confirmed).

Old-client fallback (pid == 0): `confirmJobDead` returns false for pid 0
(server.go:4182), so the job is NOT confirmed dead and stays PARKED in Run -
never blindly re-reserved and never reverted to the old `StartTime`-based
requeue. A stuck parked job recovers when its `Started`/`Touch` finally drains
(applyJobStart / recoverLostTouchedJob clear `Lost`).

**Package:** `jobqueue/` **File:** `jobqueue/server.go` **Test file:**
`jobqueue/reliable2_reserve_test.go`.

**Acceptance tests (map to Issue B; run under `-race`; use the Option-R
determinism style - `ItemTTR = 500ms`):**

1. (Alive owner not re-reserved) Given a job reserved (host + `os.Getpid()`
   recorded, C1) but NEVER started, when its TTR expires, then within a few TTRs
   the item is still in `SubQueueRun` (`server.q.Get(key)` state ==
   `queue.ItemStateRun`) and `job.Lost == true`; and when a second client calls
   `Reserve(200ms)` up to 20 times, every call returns `nil` (cannot re-reserve
   the alive-owned job). Fail-before: the `StartTime.IsZero()` branch sent it to
   delay->ready and the second client re-reserved it; pass-after: parked, not
   re-reserved.
2. (Confirmed-dead reclaimed, no hole) Given a reserved-not-started job whose
   recorded pid is a definitely-dead pid on a reachable host, when its TTR
   expires and death is confirmed, then the job is requeued and becomes
   reservable again (re-run), i.e. no stuck-in-Run hole. Fail-before: n/a
   (guards that the fix did not lose the reclaim); pass-after: reclaimed.
3. (Old-client fallback parks) Given a reserved-not-started job with `Pid == 0`,
   when its TTR expires, then it is parked in `SubQueueRun` and a second
   client's repeated `Reserve` returns `nil` (never blindly re-reserved).
   Pass-after: parked.

### C3: Never bkill a reserved LSF array element

As the LSF scheduler, I want `killExcessCmds` to never `bkill` an array element
wr has already handed a job reservation to, so a PEND->RUN element that just
reserved+started a job is not killed mid-job (repro: 38,302 of ~40k elements
bkilled). Robust to `bjobs` status lag: protection does not depend on `bjobs`
having caught up to RUN.

Correlation: an LSF runner knows its element id from `LSB_JOBID` +
`LSB_JOBINDEX` (cmd/runner.go:134-136); the killable id
`killableID`/`killCollector.consider` builds is exactly `jobid[index]`
(lsf.go:1208-1218). So:
- Runner: compute `SchedulerID` = `LSB_JOBID` (with `[LSB_JOBINDEX]` appended
  when set), empty for non-LSF; send it in the reserve `clientRequest` (C1).
- Server: in `respondWithReservedJob`, when `cr.SchedulerID != ""`, call
  `s.scheduler.Reserved(cr.SchedulerID)`.
- Scheduler: `Reserved` -> `scheduleri.reserved` records the id in a
  concurrency-safe set (LSF impl); non-LSF impls no-op.
- `lsf`: `killCollector`/`killExcessCmds` (lsf.go:1162-1204) MUST skip any
  element whose `killableID` is in the reserved set (never append it to
  `toKill`), even when its `bjobs` `STAT != RUN`.
- Bound the set: prune reserved ids no longer present in the LSF (e.g. drop ids
  absent from a full `bjobs` snapshot, since `parseBjobs` excludes exited
  elements) so it does not grow unboundedly over a long-lived manager.

BOUNDARY (N3): this spec owns ONLY the never-bkill-a-reserved-element
protection. Reducing the VOLUME of over-submission (array cap / uncapped `bsub`)
belongs entirely to bugfix 260722-1; coordinate, do not duplicate. Do NOT
implement the rejected "re-check RUN before bkill" approach.

**Package:** `jobqueue/scheduler/` **File:** `jobqueue/scheduler/lsf.go`,
`jobqueue/scheduler/scheduler.go` **Test file:**
`jobqueue/scheduler/scheduler_lsf_test.go` (edit; pure-function test, no real
LSF).

**Acceptance tests (map to Issue B):**

1. Given a `killCollector` with `maxAllowed` exceeded and a reserved element id
   `12345[7]` recorded, when it considers elements including `12345[7]` with
   `STAT == "PEND"` (non-RUN, normally killable), then `12345[7]` is NOT in
   `toKill`, while an unreserved non-RUN excess element IS. Fail-before: the
   reserved element was killed as excess; pass-after: protected.
2. Given the reserved set with an id whose element no longer appears in a
   subsequent `bjobs` snapshot, when the prune runs, then that id is removed
   from the set (bounded memory). Pass-after: pruned.
3. Given a non-LSF scheduler, when `Reserved(id)` is called, then it is a no-op
   and does not error (interface method safe for all schedulers).

---

## D. Failed-job release livelock give-up (Idea 1)

Prompt sec 2, note (crash-recovery constraint). Fixes repro Issue B2. Priority 4
in build order (depends on the KEEP'd recovery window, unchanged).

### D1: Not-in-Run release from a live manager returns ErrBadJob

As a runner whose failed command's reservation is gone (the double-reservation
loser), I want the manager to tell me to give up promptly, so I abandon the dead
reservation and reserve my next job instead of looping for 24h.

Change `handleRelease` (serverCLI.go:1116-1134): call `getij(cr, true)` instead
of `getij(cr, false)`, mirroring `handleArchive` (serverCLI.go:1013). Then a
release whose item is not in the Run sub-queue returns `ErrBadJob`
(getij, serverCLI.go:1694-1697), landing in the client's give-up set
(`handleFinalStateError`, client.go:2123). A legitimate release (item in Run,
owner matches) is unchanged (srerr == "" -> `releaseJob` proceeds). This covers
both `jrelease` and `jbury` (serverCLI.go:1614-1617).

Distinction preserved:
- Manager up, item gone (superseded) -> `ErrBadJob` -> give up promptly (no
  24h/15s loop).
- Manager unreachable (crash) -> a connection error, NOT `ErrBadJob` -> keep
  retrying (D2).
- During the recovery window, a not-yet-restored item -> `getij` returns
  `ErrRecovering` (retryable), so a genuine unrecorded outcome still lands once
  recovery restores the item.

**Package:** `jobqueue/` **File:** `jobqueue/serverCLI.go` **Test file:**
`jobqueue/reliable2_release_test.go` (new).

**Acceptance tests (map to Issue B2; run under `-race`):**

1. Given a live manager and a job key whose item is NOT in the Run sub-queue
   (removed by a winning runner), when a client sends `jrelease` (a failed
   command's release), then the server responds `ErrBadJob`. Fail-before:
   `getij(cr, false)` -> `releaseJob` -> `ErrNotRunning` -> `ErrInternalError`;
   pass-after: `ErrBadJob`.
2. Given the client `reportFinalState` loop, when `applyFinalState` returns an
   `ErrBadJob` release error, then `handleFinalStateError` returns
   `giveUp == true` and the loop exits promptly (no 24h retry, no 15s reconnect
   storm), so the runner proceeds to its next reserve. Fail-before:
   `ErrInternalError` returned `giveUp == false` (24h loop); pass-after:
   gives up.
3. Given a legitimate release of a job the client owns whose item IS in Run
   (normal non-zero-exit release-for-retry), when it releases, then the error is
   `nil` (or the job is buried after retries) - the fix does not break normal
   releases. Regression guard.

### D2: Crash-recovery success still recorded; retryTime stays 24h

As a runner whose command SUCCEEDED while the manager was crashed, I want to
record that success when the manager restarts within `retryTime`, so an
expensive command is not needlessly re-run.

No code change to `retryTime` (stays 24h, `ClientRetryTime`) or to the KEEP'd
recovery window (`recoverInBackground`/`isRecovering`/`ErrRecovering`/
`rescheduleReadyAfterRecovery`; `confirmOrReleaseLostJob` permanently protects
recovered jobs via `recoveredRunningJobs`, server.go:3430-3433). On restart the
still-owned running job is recovered into Run, so the re-sent archive succeeds.
This is what makes D1's give-up safe: only a genuinely superseded reservation
(item gone, manager up, not recovering) receives `ErrBadJob`.

**Package:** `jobqueue/` **File:** (no change; behaviour test) **Test file:**
`jobqueue/reliable2_release_test.go`.

**Acceptance tests (map to Issue B2 crash-recovery; run under `-race`):**

1. Given a job reserved+started (PID `os.Getpid()`) and a genuine success being
   reported, when the manager is stopped mid-report and restarted preserving the
   DB within `retryTime`, then after recovery the re-sent archive is accepted,
   the job is recorded `JobStateComplete`, and the command is NOT re-run
   (`GetStatusByRepGroupMatch` shows `Counts[JobStateComplete] == 1`).
   Fail-before: n/a (guards that D1's give-up did not discard a genuine
   unrecorded success); pass-after: recorded complete.
2. Given `ClientRetryTime`, when the build is inspected, then it is 24h
   (unchanged). Guard.

---

## E. KEEP, out-of-scope, and two-tier validation

### E1: KEEP - must remain fully working

Do NOT remove or weaken. Existing anchor tests stay green:
- Background recovery window (`recoverInBackground`/`isRecovering`/
  `ErrRecovering`/`rescheduleReadyAfterRecovery`) - makes D safe.
- #503 per-job subscription delivery, live RAM/CPU/STDOUT introspection
  (`emitLiveTouchSnapshot`), reconnect/resync, `wr add --sync` non-polling wait.
- Web JS KEEP: `IsPushUpdate` live pushes (`State` branch), reconnect
  fresh-state, `modify-job.js`, `inflight-tracking.js`.
- Phase-1 Option R gains: no false-lost of on-time jobs, no false-`deleted`
  broadcast, fast large-DB startup; `queues_avoid` client fix
  (`.docs/bugfixes/260720-1.md`); `putJobStats` zero/negative-duration guard.
- v0.36.5 completion leniency (an alive owner's success is never discarded).

**Acceptance:** the existing KEEP anchor suites (`subscription_test.go`,
`live_jtouch_test.go`, resync/suspend/modify tests, `reliable2_keep_test.go`,
recovery tests, `reliable2_completion_test.go`, `reliable2_lost_test.go`,
`reliable2_dbcompat_test.go`) remain green under `-race` after all changes.

### E2: Out of scope (do NOT implement here)

- Ideas 4 and 6 - mooted by the counter-only revert (N1b). No absolute counter
  remains to fix (Idea 6); de-serialising the drainer is achieved BY the revert
  (A2), not as a separate idea (Idea 4).
- Idea 3 - a diagnostic step, folded into Idea 2.
- The uncapped LSF `bsub` array hang - separate bugfix 260722-1. C3 touches the
  same over-submission behaviour but owns ONLY the never-bkill-reserved-element
  protection; the array cap belongs to 260722-1.

### E3: Two-tier validation

**Tier A - committed regression tests (TDD; run in `make test`/`make race`).**
Enumerated, each failing before and passing after:
- A: counter machinery gone + CLI counts stay correct across a DB-preserving
  restart (A1.1, A3.2); change-callbacks dispatch concurrently, no serial
  drainer (A2.1-2); the phase-1 JS status-bar edit keeps the browser-test
  fixtures `repgroup-bar-flicker`, `removed-jobs-refresh`,
  `completed-repgroup-visibility` green via `make browser-test` (A3.3, A4.3),
  belt-and-suspenders on the server-side Go tests.
- B: N concurrent readers are `-race`-safe with correct per-client reply routing
  (B1.1); bounded control-RPC latency under in-process load (B1.2, supporting).
- C: reserved-not-started alive owner not re-reserved (C2.1); confirmed-dead
  reserved-not-started reclaimed, no hole (C2.2); old-client parks (C2.3);
  reserved LSF element never bkilled (C3.1).
- D: live-manager not-in-Run failed release -> client gives up promptly
  (D1.1-2); manager stopped mid genuine-success, restarted within `retryTime`
  -> recorded complete, not re-run (D2.1); `retryTime` == 24h (D2.2).

Keep `jobqueue/reliable2_scale_test.go` (`//go:build reliability`) but add a
header comment documenting that it UNDER-reproduces (it uses `os.Getpid()` live
processes and a TTR above the backlog, so it passed M2=0 while real LSF churned)
and is a non-authoritative support test, never the sole evidence.

**Tier B - real-LSF end-to-end reproductions (NOT committed tests; a REQUIRED
real-LSF gate before merge).** It SHOULD be run by the implementing agent at
the END of the work when the session can reach real LSF at scale on the
isolated dev deployment; a human runs it only as a fallback when the agent
genuinely cannot (no real-LSF access, or fair-share cannot permit a
representative run). It must never be skipped or claimed done on Tier A alone
(the in-process harness was shown insufficient) and must be actually executed,
never simulated. Re-run the documented procedures in
`.docs/reliable2/phase2/repro.md` on the isolated dev manager and record results
in a NEW `.docs/reliable2/phase2/validation.md`:
- Issue B/B2 churn: multi-group `true`/`false` jobs -> near-zero
  `jarchive: bad job` / `jrelease: not running`, each command runs once, forward
  progress ~100%.
- Issues 1-3 responsiveness: `wr status` (details), `wr limit`, `wr suspend`
  stay responsive (no 60s timeouts) while a few thousand runners churn instant
  jobs.
- Issue 4: build `.docs/reliable2/phase2/wsprobe/`, complete jobs in a rep
  group, restart preserving the DB, add live jobs, confirm the web `/status_ws`
  view agrees with the CLI/DB the v0.36.5 way (or that the drifting
  absolute-counter endpoint is gone).
- Idea 1 crash-recovery: kill the manager mid genuine-success report, restart
  within `retryTime`, confirm the job is `complete` and not re-run.

**"Done" bar (N5/N6):** completing Tier A + the gated in-process harness is the
CODING done and may be completed autonomously. Tier B is a REQUIRED real-LSF
gate before merge - the overall work is NOT done on Tier A alone. The
implementing agent SHOULD run Tier B itself at the END when it can reach the
isolated dev farm at scale; only when it genuinely cannot (no real-LSF access,
or fair-share cannot permit a representative run) does a human run it as a
fallback. Either way Tier B must be actually executed, never skipped or
simulated, and recorded in `validation.md`.

---

## Implementation Order

Sequenced so each phase builds on tested foundations; justified where dependent.

1. **Web front-end counter-only revert (A1-A4).** FIRST: it rewrites the
   transition (`jobtransition.go`) and queue (`queue/queue.go`) files everything
   else touches, and it de-serialises dispatch (removes the exclusive
   `repGroupCounts.mu` and the serial drainer), which is a precondition for the
   responsiveness the later phases assume. Land: remove `repGroupCounts` +
   `jstateAbsolute` + `emitJobTransition`'s counter half; restore concurrent
   `go changedCb` and delete the drainer + its three ordering tests; restore
   `jstateCount`/`statusCaster`/scan-on-connect and edit only the JS status-bar
   branch; verify #503 invariants under `-race`. Depends on nothing.

2. **Concurrent RPC readers (B1).** After A because A removes the per-transition
   exclusive serialiser; adding readers then meaningfully raises admission
   throughput. Prove `-race`-safe concurrent `RecvMsg` with correct reply
   routing and wait-for-all-readers shutdown. Depends on A (hot path
   de-serialised).

3. **Double-reservation prevention (C1-C3).** After B so reserved-not-started
   liveness signal (C2) is not itself starved by the single-reader backlog. C1
   (host+pid at reserve) is groundwork for both C2 (confirm-dead reclaim) and
   C3 (the runner also sends its scheduler element id on the same request). C2
   reuses the unchanged `markJobLost`/`confirmOrReleaseLostJob` machinery; C3 is
   isolated in the scheduler package. Depends on C1; benefits from B.

4. **Release-livelock give-up (D1-D2).** LAST: D1 is a one-line handler change,
   but it is only SAFE once the KEEP'd recovery window (unchanged) is confirmed
   intact (D2) and once C reduces how often a live manager even sees a
   not-in-Run release. Depends on the recovery window (present) and is validated
   after C so the crash-recovery test runs against the finished reserve/lost
   path.

5. **KEEP regression sweep + Tier B (E1, E3).** Run all KEEP anchor suites green
   under `-race`; then perform the real-LSF Tier-B validation and record
   `validation.md`. Tier B is a real-LSF gate (run by the implementing agent at
   the end when able, else a human as a fallback; actually executed, never
   simulated), not code.

Phases 1-2 are the responsiveness core; phases 3-4 are the reliability core.
Each phase's Tier-A tests must fail before and pass after that phase's change.

---

## Appendix: Key Decisions

- **Counter-only, surgical (N1/N1a).** Remove only the absolute-count machinery;
  restore v0.36.5's concurrent dispatch and delta feed by REUSING the existing
  `caster`/`setupUpdateListener` (no bcast dependency). Keep every other web
  front-end. Edit only the JS status-bar count branch. Ideas 4/6 stay moot.
- **The decisive de-serialiser (N1b).** The single exclusive `repGroupCounts.mu`
  held across every transition batch was the per-transition serialiser;
  `statusCaster.Send` (shared RLock + per-web-client mutex + non-blocking send)
  is not. Removing the `applyTransitions` call is THE requirement; the retained
  #503 path is RLock-only, has a zero-subscriber early-out, and delivers after
  the lock is released. Verify under `-race` AND under load.
- **Concurrent readers, raw RecvMsg (N4).** mangos v3.4.2 xrep has no Contexts
  (`OpenContext` -> `ErrProtoOp`); raw concurrent `RecvMsg` is safe because it
  fans out a shared channel and pipe-ID headers route replies. No new port, no
  wire change. Prove `-race`-safe; surface as a blocker if it proves unsafe.
- **Host+pid at reserve, not on the touch stream (N2).** The reserve request is
  the one signal independent of the backlogged touch/reserve stream that B
  decouples. Reclaim reuses the started-path confirm-dead machinery; pid 0
  (old client) parks safely and is never blindly re-reserved.
- **Track reserved elements, not re-check RUN (N3).** wr knows which element it
  reserved to (runner's `LSB_JOBID[LSB_JOBINDEX]` == `killableID` form), so
  protection is robust to `bjobs` status lag. Volume reduction stays in
  260722-1.
- **Give up on ErrBadJob, keep 24h retry (Idea 1).** `handleRelease` ->
  `getij(cr, true)` mirrors `handleArchive`; only a live manager's authoritative
  "gone" gives up. Connection errors and `ErrRecovering` still retry, so a
  crash-recovered genuine success is recorded, not discarded.
- **Testing (N5/N6).** GoConvey `So()` only; `-race` for
  concurrency/reliability tests; Option-R determinism style
  (`os.Getpid()` alive owner, definitely-dead pid, short `ItemTTR`). The
  `reliability`-tagged scale test is retained as a documented non-authoritative
  support test. Tier B (real LSF, recorded in `validation.md`) is a required
  gate before merge, run by the implementing agent at the end when able and by
  a human only as a fallback; actually executed, never simulated.
- **Implementor/reviewer.** Follow `go-implementor` (TDD: failing acceptance
  test first, then implement) and `go-reviewer` (verify every acceptance test
  has a GoConvey test that genuinely fails before and passes after), both
  referencing `go-conventions`. Unset `OS_*` env vars when running the suite.
