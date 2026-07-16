# Phase 2: Idea 2 - Non-blocking startup and recovery-window RPC safety

Ref: [spec.md](../spec.md) sections B1, B2, B3 (plus one additional integration
acceptance test spanning A3 + B1)

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout. This phase depends on Phase 1 (cheap recovery makes single-batch
background recovery fast, and it reuses `backfillRepGroupCompleteCounts` as a
background task). It is independently mergeable and must keep the Section E
regression guards green (re-run them after the phase).

Items are sequential: they cluster in `jobqueue/server.go` and
`jobqueue/serverCLI.go`, B2 depends on B1's `isRecovering()`, and B3 depends on
the B1 recovering machinery and interacts with B2's retry path. Item 2.4 is an
integration test that needs B1 (this phase) and A3 (Phase 1).

## Items

### Item 2.1: B1 - Reorder Serve() so recovery runs in the background

spec.md section: B1

In `jobqueue/server.go`, reorder `Serve()` (server.go:2229). New order:
`createQueue` -> web interface + `persistToken` + `serveClients` -> then
`setRecovering(total)` and launch `loadPriorState` in a goroutine that calls
`recoveryPauseHook` (if set), performs recovery, updates progress via
`noteRecovered`, and calls `finishRecovering()` at the end. Web interface stays
before `serveClients`; only `loadPriorState` moves to the background. Recovery
keeps the single-batch enqueue (`recoverPriorJobs` -> `enqueueItems`
server.go:2755; AddMany resolves deps within the one batch). `Serve` returns
once clients are served (recovery still running).

Add server fields guarded by `ssmutex` (server.go:758): `recovering bool`,
`recoveryTotal int`, `recoveryRestored int`, and `rrjMu sync.RWMutex` (used in
B3). Methods: `isRecovering()`, `recoveryProgress() (restored, total int)`,
`setRecovering(total)`, `noteRecovered(n)`, `finishRecovering()`. Add the
`recoveryPauseHook func()` test hook (modelled on `statusWSDetailsHook`
server.go:746), called at the top of the background recovery goroutine; nil in
production. Lightweight progress reporting via `manager status` and/or a
"recovering: N/M restored" log line. New lock leaf only (preserve
`queue.mutex -> job -> statusState.mu`).

Tests in `jobqueue/nonblocking_startup_test.go`. Covers all 4 acceptance tests
from B1 (Ping/status/Add answer within 2 s while the hook blocks recovery;
recovery-to-completion reproduces exact ground truth M with lost == 0 and no dup
keys; hammering Add/Reserve/status through recovery keeps accounting exact under
`-race`; `recoveryProgress` reports total == M / restored 0 at the pause and
restored == total == M after, monotonic non-decreasing, never exceeding total).

- [ ] implemented
- [ ] reviewed

### Item 2.2: B2 - Recovery-window RPC safety

spec.md section: B2

Add the const `ErrRecovering = "server is recovering prior state, please retry"`
(server.go); it MUST NOT contain the `ErrBadJob` (server.go:82) or
`ErrBadRequest` (server.go:81) substrings, so client retry treats it as
transient. In `getij` (serverCLI.go:1689), when `s.q.Get(key)` returns an error
(item not in queue) AND `s.isRecovering()`, return `ErrRecovering` instead of
`ErrBadJob`. Leave the wrong-sub-queue branch (`checkRunning`,
serverCLI.go:1696) as `ErrBadJob` (a real state error, not a recovery-timing
miss). Client retry is already in place: `handleFinalStateError`
(client.go:2118) gives up only when the error contains `ErrBadJob` /
`ErrBadRequest` (client.go:2123), else retries within `retryTime`
(`reportFinalState` client.go:2084); `handleTouch` (serverCLI.go:905) already
calls `recordJobContact` first (serverCLI.go:910). Depends on B1's
`isRecovering()`.

Tests in `jobqueue/nonblocking_startup_test.go`. Covers all 3 acceptance tests
from B2 (window archive of a to-be-restored key returns `ErrRecovering` with
neither ErrBadJob nor ErrBadRequest substring; after release + retry the archive
succeeds, job ends complete, repgroup counter incremented exactly once == RAW
scan; a window touch of a not-yet-restored running key returns `ErrRecovering`
and `recordJobContact` recorded the contact).

- [ ] implemented
- [ ] reviewed

### Item 2.3: B3 - Concurrency guards

spec.md section: B3

In `jobqueue/server.go`, guard `recoveredRunningJobs` with `rrjMu` at both
sites: write in `recoverRunningJob` (server.go:2815), read in
`confirmOrReleaseLostJob` (server.go:3297). Ensure a client re-adding an
identical job (same Cmd/Cwd => same key) that is also being recovered converges
to one queue item per key with a consistent state and no double-run/double-count
(AddMany dedups by key only). Depends on B1's recovering machinery and interacts
with B2's retry path.

Tests in `jobqueue/nonblocking_startup_test.go`. Covers both B3 acceptance tests
(recovery + concurrent re-add of the same key -> exactly one queue item, job not
run twice, repgroup counter == RAW scan; the full B1-B3 suite is `-race` clean,
in particular around `recoveredRunningJobs`).

- [ ] implemented
- [ ] reviewed

### Item 2.4: Integration - non-blocking Serve + background backfill

spec.md section: A3 + B1 (additional acceptance test from spec review; not in
the spec's numbered acceptance lists)

Additional integration test exercising Phase 1 A3 and Phase 2 B1 wiring
together, placed here (the later of the two phases). It depends on A3's
`backfillRepGroupCompleteCounts` and its Serve-launched background goroutine
(Phase 1) and on B1's non-blocking `Serve` (this phase). Test in
`jobqueue/nonblocking_startup_test.go`.

1. Given a pre-upgrade / not-yet-backfilled DB (archived complete history
   present, `bucketRepGroupComplete` and `bucketRepGroupBackfilled` EMPTY), when
   the manager is started via `serve`, then it becomes responsive immediately
   (Ping, `manager status`, and `Add` of a new job all answer within ~2 s) while
   `backfillRepGroupCompleteCounts` runs in the BACKGROUND; and after the
   backfill finishes, every repgroup's maintained counter (via
   `retrieveMaintainedCompleteCounts`) converges to the RAW
   `retrieveCompleteJobCountsByRepGroups` scan, and its marker (with the
   sentinel) is set. This proves the non-blocking-startup and
   background-backfill paths compose (responsive-immediately from B1,
   convergence from A3).

- [ ] implemented
- [ ] reviewed

## Regression guards (Section E)

Re-run after this phase; all must stay green (spec.md Section E):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner`,
  `TestLostDetectionRecentContactNotLost`.
- `TestReliableFalseLostRerun`, `TestReliableCompletedRepGroupRemovedOnRefresh`
  (reliable harness dropped into `jobqueue/`;
  `go test -run TestReliable ./jobqueue`).
- `TestReliableFalseLostUnderSaturation` (`everLost == 0`; run from
  `.docs/reliable/harness/`, not committed).
- `make test`, `make race`, `make lint` all clean.
