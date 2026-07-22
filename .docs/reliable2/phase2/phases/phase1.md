# Phase 1: Counter-only web front-end revert (section A)

Ref: [spec.md](../spec.md) sections A1, A2, A3, A4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout: each acceptance test must fail before and pass after its change;
the transition/web tests run under `-race`. Build/test with `-tags netgo`; unset
ALL `OS_*` env vars for `make test` / `make race` (keeps them fast); GoConvey
`So()` assertions; copyright headers on new files.

This phase is FIRST because it rewrites the transition (`jobtransition.go`) and
queue (`queue/queue.go`) files every later phase touches, and it de-serialises
dispatch by removing the two per-transition serialisers - the exclusive
`repGroupCounts.mu` (A1) and the serial drainer (A2) - which is the precondition
for the responsiveness Phases 2-4 assume. Depends on nothing. This is the one
ACCEPTED user-facing change in the spec: web status-bar counts revert to v0.36.5
flicker/overcount quality; every other change is internal-only.

The restored concurrent `go queue.changedCb(...)` fan-out (A2) now also drives
the RETAINED #503 subscription delivery concurrently, so the N1b concurrency
invariants (spec "Concurrency invariants") MUST be preserved:

1. DECISIVE: actually remove the `repGroupCounts.applyTransitions` call from the
   per-transition path (A1); its replacement `statusCaster.Send` takes only a
   shared `RLock` + a per-web-client mutex + a non-blocking buffered send, never
   a server-wide exclusive lock.
2. Keep the `hasAnyClientSubscriptions()` early-out at the top of
   `enqueueChangeCallbackSubscriptions`.
3. Keep `csmutex` an `RWMutex`; per-transition access stays `RLock`; the only
   `Lock` writers are subscribe/unsubscribe/shutdown - never add a
   per-transition `Lock`.
4. Never hold `csmutex` (or any server-wide lock) across the actual client
   delivery - delivery stays a buffered channel send after the lock is released.
5. Use `statusCaster.Send` for the web-bar counts; do NOT reintroduce any single
   server-wide exclusive mutex on the per-transition path.

Verify these under `-race` in this phase; the under-load verification
(control-op responsiveness while a fleet churns) is part of the Tier-B real-LSF
validation (Phase 5).

Sequencing: A2 is in the `queue/` package (`queue/queue.go`,
`queue/queue_test.go`) and is independent of the `jobqueue` items, so it MAY be
implemented in parallel (Item 1.1). Within `jobqueue`, A3 (Item 1.2) is
sequenced BEFORE A1 (Item 1.3): A3 adds `statusCaster` + the `jstateCount` type
and swaps the web listener, so the package still compiles when A1 removes the
old `repGroupCounts`/`jstateAbsolute` counter and reroutes `counts` into
`statusCaster.Send`. A1 and A3 both edit `server.go`, so they are NOT parallel.
A4 (Item 1.4) is a regression guard on A3's restored scan-on-connect.

## Items

### Item 1.1: A2 - Restore v0.36.5 concurrent change-callback dispatch

spec.md section: A2

In `queue/queue.go` restore v0.36.5's fan-out and delete the #547 serial
drainer:

- Restore `changed()` to v0.36.5's body: if `changedCb != nil`, build `data`
  and `go queue.changedCb(from, to, data)` (11fe092:queue/queue.go:325-334).
  Restore `SetChangedCallback` to a plain setter.
- DELETE the serial-drainer machinery: `runChangedCallbacks` (queue.go:262),
  `finishChangedCallbacks` (:283), `nextChangedCallback` (:297), the
  `changedCbPending`/`changedCbRunning` fields (:415/:419), and
  `changedNotification` if unused. Keep `changedCbMutex` only if still needed to
  guard the `changedCb` setter (a mutex-guarded setter is acceptable; v0.36.5
  did not lock).

Accepted consequence (N1/N1b): transition-callback ORDERING is no longer
guaranteed; web-UI count/update ordering reverts to v0.36.5 quality. This is why
the ordering-pinning tests are removed.

This item is independent of the `jobqueue` items (separate package, separate
test file) and MAY be implemented in parallel with Items 1.2-1.4.

Tests in `queue/queue_test.go` (edit). Covers A2's 2 behavioural acceptance
tests plus its mandated deletion: (1) with a `changedCb` set, a transitioning
batch runs the callback on a goroutine distinct from the caller (the transition
method returns without waiting) - concurrent dispatch; (2) two overlapping
transition batches may run their callbacks concurrently (a blocking callback
does not block the other) - the v0.36.5 fan-out; (3) DELETE the three
serial-drainer ordering/Goexit tests
(`TestQueueChangedCallbacksPreserveTransitionOrder`,
`TestQueueChangedCallbacksFollowConcurrentTransitionOrder`,
`TestQueueChangedCallbacksContinueAfterGoexit`) - no remaining test may assert
transition-order preservation.

- [x] implemented
- [x] reviewed

### Item 1.2: A3 - Restore the v0.36.5 status-bar delta feed + scan-on-connect

spec.md section: A3

Restore v0.36.5's `jstateCount` delta feed by REUSING the existing
`caster`/`setupUpdateListener` (NOT bcast), so the package still compiles when
Item 1.3 removes the old counter. This item is sequenced BEFORE A1 and is NOT
parallel with it (both edit `server.go`).

Server (`jobqueue/server.go`):

- Restore the `jstateCount` type (spec "Types": `RepGroup`, `FromState`,
  `ToState`, `Count`; `"+all+"` aggregates all live jobs).
- Add a `statusCaster *caster` field; init `newCaster()` in `serve` (beside
  `badServerCaster`/`schedCaster`, server.go:2488/2492), `Broadcasting(0)` at
  startup and `Close()` at shutdown (mirror server.go:2628/5590).
- The transition chokepoint emits one
  `statusCaster.Send(&jstateCount{RepGroup, FromState, ToState, Count})` per
  grouped contribution (from A1's `counts`, including lost-from-lost and
  touch-recovery lost->running).

Server (`jobqueue/serverWebI.go`):

- In `webInterfaceStatusWS` (serverWebI.go:394) replace the
  `setupStatusStateUpdateListener` call with `s.setupUpdateListener(ctx, conn,
  stopper, storedName, s.statusCaster, "status updater")` (identical pattern to
  serverWebI.go:433/436). DELETE `setupStatusStateUpdateListener`,
  `sendStatusStateUpdates` (serverWebI.go:924-993), and
  `statusStateSendThrottle`.
- Restore the v0.36.5 scan-on-connect: on the `{Request:"current"}` message
  (`readStatusWSRequests`), send `webInterfaceStatusSendGroupStateCount` for
  `"+all+"` from `getJobsCurrent(...)` (incomplete jobs only) and, on a
  per-RepGroup request, that group's `getJobsCurrent` +
  `getCompleteJobsByRepGroup` (11fe092:serverWebI.go:253-277, :510-528). Restore
  `webInterfaceStatusSendGroupStateCount`.

Web JS (`jobqueue/static/js/wr/websocket-handler.js`, EDIT ONLY the status-bar
branch; N1a):

- Change the message dispatch (line ~180): replace the
  `json.hasOwnProperty('Counts')` -> `handleAbsoluteStateMessage` branch with a
  `json.hasOwnProperty('FromState')` -> delta handler that decrements
  `FromState` and increments `ToState` by `Count` on the RepGroup tracker (and
  the `"+all+"` inflight tracker), v0.36.5-style. Remove
  `handleAbsoluteStateMessage`/`setTrackerCounts` absolute-replace logic.
- KEEP unchanged: the `State` (IsPushUpdate live job-detail pushes), `IP`
  (bad-server), and `Msg` (scheduler) branches; the reconnect fresh-state reset
  (`resetLiveCounts`, line ~148); the `{Request:"current"}` send on open; the
  `inflight-tracking.js` import; and `modify-job.js`. Do NOT wholesale-restore
  v0.36.5's `websocket-handler.js`.

Browser guard (A3.3, belt-and-suspenders on the server-side Go tests): after
the JS status-bar edit, `make browser-test` MUST keep the count-display fixture
`jobqueue/testdata/repgroup-bar-flicker/` green. It currently drives the
absolute `Counts` protocol, so update `screenshot.mjs` only as strictly needed
to drive/reflect the reverted `FromState` delta count DISPLAY, never weakening
its bar-rendering assertions.

Tests in `jobqueue/serverWebI_test.go` (edit: `jstateAbsolute` ->
`jstateCount`) and the new `jobqueue/reliable2_webrevert_test.go`. Covers all
three A3 acceptance tests (map to Issue 4): (1) a `/status_ws` client watching
a job go new->ready->running->complete in rep group `rg` receives
`jstateCount` messages whose applied deltas leave `rg`'s live counts matching
the run and `"+all+"` tracking the live total; (2) after a DB-preserving restart
with N prior completed jobs in `rg` and no absolute counter, a client
requesting `"current"` before any new transition is sent no `complete` seed for
the terminal-only `rg` (incomplete-only `getJobsCurrent`), while the CLI
`GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)` still
returns `Counts[JobStateComplete] == N` - the Issue-4 fix (no diverging
counter); (3) the browser guard above (A3.3) - `make browser-test` keeps
`repgroup-bar-flicker` green after the JS status-bar edit.

- [x] implemented
- [x] reviewed

### Item 1.3: A1 - Remove the per-RepGroup counter; split the chokepoint

spec.md section: A1

Remove the diverging absolute counter and reroute the transition chokepoint to
drive only the (restored, Item 1.2) delta feed and the KEEP'd #503 subscription
delivery. Sequenced AFTER A3 so the package compiles once the old counter is
gone.

Remove:

- `jobqueue/repgroupcounts.go` (whole file): `repGroupCounts`,
  `repGroupCountsSubscriber`, `newRepGroupCounts`, `applyTransitions`,
  `applyToRepGroupLocked`, `markDirtyLocked`, `wholeMap(Locked)`,
  `liveSeedLocked`, `rgcSeedCountCopy`, `rgcHasLiveJob`, `subscribe`,
  `unsubscribe`, `drain`, and the exclusive `mu`.
- The `s.repGroupCounts` field (server.go) and its `newRepGroupCounts()` init
  (server.go:2487); the `jstateAbsolute` type (server.go:504).
- In `emitJobTransition` (jobtransition.go:75-81): the
  `s.repGroupCounts.applyTransitions(counts)` call (N1b requirement 1). Keep the
  `emitSubscriptions()` half. The `counts []countContribution` still flow but
  now drive `statusCaster.Send` deltas (Item 1.2), not the counter.
- The counter reference on the touch-recovery path (serverCLI.go:943-955,
  `recoverLostTouchedJob`) and the lost-transition path (server.go:3400-3407):
  keep the subscription enqueue; route the count as a `jstateCount` delta.

`changeCallbackCounts`/`contributionsFromGrouped`/`countContributionKey` MAY be
kept as the delta-grouping helper (they already group per (from,to,repGroup) and
tally lost jobs) or inlined; behaviour is pinned by A3's tests, not structure.

KEEP unchanged: #503 subscription delivery
(`enqueueChangeCallbackSubscriptions`, `server_subscription.go`,
`subscription.go`), `hasAnyClientSubscriptions`, live
RAM/CPU/STDOUT (`emitLiveTouchSnapshot`), reconnect/resync, `wr add --sync`. The
N1b invariants 1-5 (Instructions) MUST hold.

Tests in the new `jobqueue/reliable2_webrevert_test.go` (shared with A3); DELETE
`jobqueue/repgroupcounts_test.go`. Covers both A1 acceptance tests (map to Issue
4): (1) compile-time proof the counter is gone - `repGroupCounts`,
`newRepGroupCounts`, `jstateAbsolute`, `repgroupcounts.go` are not symbols; (2)
a subscriber to a rep group whose job runs to success (`Exitcode==0`) still
receives a terminal `JobUpdate` with to-state `JobStateComplete` and never
`JobStateDeleted` (KEEP #503 delivery unaffected by removing the counter).

- [x] implemented
- [x] reviewed

### Item 1.4: A4 - Terminal-hiding-on-refresh retained

spec.md section: A4

Retained essentially for free by A3's restored scan-on-connect: v0.36.5 seeds
`"+all+"` from `getJobsCurrent` (incomplete jobs only, server.go ~4823), so a
completed-only rep group is naturally excluded from a fresh connection's seed; a
rep group that COMPLETES while connected stays visible via the live
running->complete delta. The spec REQUIRES this property and pins it with a
test; the mechanism moves from the removed `liveSeedLocked`/`repGroupCounts` to
the restored incomplete-only scan. This is a regression guard on Item 1.2 - no
new production code beyond A3.

Browser guard (A4.3, belt-and-suspenders on the server-side Go tests): after
the JS status-bar edit, `make browser-test` MUST keep the terminal-hiding /
refresh fixtures `jobqueue/testdata/removed-jobs-refresh/` and
`jobqueue/testdata/completed-repgroup-visibility/` green. Update their
`screenshot.mjs` only as strictly needed to reflect the reverted delta count
DISPLAY, never weakening their terminal-hiding / refresh assertions (a
refreshed connection still shows nothing for a terminal-only rep group; a rep
group that completes while connected stays visible).

Tests in `jobqueue/serverWebI_test.go`. Covers all three A4 acceptance tests:
(1) a terminal-only rep group (its only jobs are complete) yields no live seed
when a `/status_ws` client connects and requests `"current"` (terminal-hiding
on refresh, preserving 260626-2/260716-1/260721-1); (2) a rep group with live
jobs that then complete WHILE a client is connected yields the
running->complete `jstateCount` deltas and the rep group stays visible; (3) the
browser guard above (A4.3) - `make browser-test` keeps `removed-jobs-refresh`
and `completed-repgroup-visibility` green after the JS status-bar edit.

- [x] implemented
- [x] reviewed

## Regression guards (KEEP surfaces, section E1)

Re-run after this phase; all must stay green under `-race` (spec.md section E1):

- Background recovery window tests (`recoverInBackground`/`isRecovering`/
  `ErrRecovering`/`rescheduleReadyAfterRecovery`).
- `jobqueue/subscription_test.go` (#503), `jobqueue/live_jtouch_test.go`
  (live RAM/CPU/STDOUT incl. ssh-to-host), the `JobUpdateResync` reconnect/
  resync tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, the
  `wr add --sync` client test.
- `jobqueue/reliable2_keep_test.go`, `jobqueue/reliable2_completion_test.go`,
  `jobqueue/reliable2_lost_test.go`, `jobqueue/reliable2_dbcompat_test.go`.
- `make test`, `make race`, `make lint` all clean (with all `OS_*` env vars
  unset).
