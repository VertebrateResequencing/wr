# Phase 2: Remove count machinery + deleted projection; add slim counter (C, D)

Ref: [spec.md](../spec.md) sections D1, C1, C2, D2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout; the transition/deleted tests run under `-race`. This phase
deletes the `#533` absolute-count machinery and the `deleted` projection, and
replaces `statusState` with the slim `repGroupCounts` counter so a succeeded job
is reported `complete` and never `deleted`, and startup no longer scans history.
Sequence after Phase 1 (this phase shares the transition files with the Phase 1
F0 removal; keeping them separate isolates the correctness revert). Phases 1 and
2 are the reliability core and should be reviewed together for the
completion/lost/deleted invariants.

CONSTRAINT (C2/D1 type co-location): the section-C rewrite of `emitJobTransition`
calls the slim counter's `applyTransitions`, and story C2's structural startup
test references `repGroupCounts.wholeMap()`. That type is introduced by story D1.
Rewriting section C while leaving D1's counter type to a later phase would NOT
compile. Therefore D1 (Item 2.1) is sequenced FIRST within this phase, ahead of
C1 (Item 2.2) and C2 (Item 2.3), and all three land together. Items are
sequential: they cluster in `jobqueue/repgroupcounts.go` (new),
`jobqueue/jobtransition.go`, `jobqueue/server.go`, and `jobqueue/serverWebI.go`.

## Items

### Item 2.1: D1 - Slim live counter emitting the unchanged jstateAbsolute

spec.md section: D1

Add the slim absolute per-RepGroup counter in the new file
`jobqueue/repgroupcounts.go` (recommended shape; behaviour pinned by tests):
`repGroupCounts` with `mu sync.Mutex`, `counts map[string]map[JobState]int`, and
minimal per-listener dirty/wake tracking; `newRepGroupCounts()`,
`applyTransitions([]countContribution)`, `wholeMap() map[string]map[JobState]int`
(deep copy). Maintain the `statusAllRepGroups` ("+all+", serverWebI.go:56) live
aggregate. Same lock discipline as before (strict-leaf mutex, taken last; never
before queue/job/subscription locks).

- Replace the `s.statusState` field (server.go:759) with `s.repGroupCounts`,
  initialised empty in `Serve` (was the `newStatusState()` init at
  server.go:2642).
- Wire the web-UI listener `setupStatusStateUpdateListener` (serverWebI.go:931) /
  `sendStatusStateUpdates` (serverWebI.go:962): on (re)connect push `wholeMap()`,
  the whole current in-memory map INCLUDING terminal states (do NOT replicate the
  removed `liveSeedLocked` terminal-hiding filter); thereafter push per-RepGroup
  `jstateAbsolute{RepGroup, Counts}` on change, throttled as today. The
  `jstateAbsolute` struct (server.go:504) and
  `static/js/wr/websocket-handler.js` are UNCHANGED. Do NOT revert to v0.36.5's
  `statusCaster`/`jstateCount` delta broadcasting. NEVER seed the counter from a
  history scan; a restart yields an empty counter that fills from live
  transitions (v0.36.5-quality accuracy accepted).

This item introduces the `repGroupCounts` type (and `wholeMap()`) consumed by
Items 2.2 and 2.3 in this phase; it MUST land before them (constraint above).

Tests in the new file `jobqueue/repgroupcounts_test.go`. Covers all 4 acceptance
tests from D1 (live absolute counts and `[statusAllRepGroups]` total across
new->ready->running->complete; a connected client receives a byte-compatible
`jstateAbsolute` message; connect-seed via `wholeMap()` INCLUDES terminal-only
RepGroups; a restarted manager's counter is empty until a live transition).

- [x] implemented
- [x] reviewed

### Item 2.2: C1 - Un-wrap the transition chokepoint; derive to-state from the job

spec.md section: C1

Un-wrap the transition chokepoint in `jobqueue/jobtransition.go` and delete the
`#533` count machinery:

- `emitJobTransition` (jobtransition.go:74): keep the `emitSubscriptions()` half;
  replace `s.statusState.applyTransitions(counts)` with
  `s.repGroupCounts.applyTransitions(counts)` (Item 2.1). Signature unchanged.
- `emitChangeCallbackTransition` (jobtransition.go:204): DELETE
  `changeCallbackToState` (jobtransition.go:171); derive each job's to-state
  directly from its own `job.State` at emission time (per-job, inside
  `enqueueChangeCallbackSubscriptions`, jobtransition.go:248). Simplify
  `changeCallbackCounts` to a plain per-RepGroup `from->to` increment (v0.36.5
  quality); drop `groupArchivedRerunContributions` (jobtransition.go:127),
  `groupHistoricalCompletionContributions` (jobtransition.go:148), and the
  `statusFromComplete` branch (jobtransition.go:94-113). (The `forgetJobContacts`
  block was already removed in Phase 1.) The subscription layer, its gating, and
  `enqueueSubscriptionUpdate` are UNCHANGED (KEEP) - only the to-state source
  changes.
- Implementor note (test C1.2): deriving `deleted` for a genuinely removed
  INCOMPLETE job relies on that job's `State` reading `JobStateDeleted` at the
  moment the subscription/broadcast update is emitted, so the removal path must
  set/observe that state before emission.

Perform the section-C DELETIONS (spec Architecture):

- Whole file `jobqueue/statusstate.go` (`statusState` type and every method).
  The `s.statusState` field/init was already replaced by `s.repGroupCounts` in
  Item 2.1.
- `seedStatusStateForItemDefs` (server.go:1012) and its call in `enqueueItems`
  (server.go:3977).
- Whole file `jobqueue/persistedstatus.go` + the `Job` fields
  `statusFromComplete`, `statusCompleteRepGroups` (job.go:418-419) + all
  `markPersistedJobStatusGroups` calls (serverCLI.go:1107, server.go:3016,
  server.go:4105).
- `startCounterBackfill` (server.go:1129) and its `Serve` call (server.go:2718),
  required for C2's fast startup. This (with the `seedStatusStateForItemDefs`
  removal above) may orphan the db.go read/backfill helpers they fed:
  `startCounterBackfill` fed `backfillRepGroupCompleteCounts`;
  `seedStatusStateForItemDefs` fed `retrieveMaintainedCompleteCounts` (plus
  their private helpers). The remaining db.go per-RepGroup counter machinery
  (the runtime write-side `adjustRepGroupComplete` + call sites, the buckets,
  and the recompute path) is removed in Phase 3 (section E). If deleting the
  launchers here leaves any db.go helper that `make lint` flags as unused,
  remove that orphaned helper in this phase (it is part of the same dead unit);
  defer any still-referenced machinery to Phase 3.
- Delete the note-6 test files whole: `statusstate_invariant_test.go`,
  `repgroup_counter_test.go`, `nonblocking_startup_test.go`,
  `status_count_test.go`, `statusstate_test.go`. In `server_startup_test.go`
  delete `TestServeStartsQuicklyWithLargeCompletedHistory` and its helpers
  (`completedOnlyHistoryStartupDuration`, `prepareCompletedOnlyHistory`,
  `waitForStartupStatusCounts`) and consts; KEEP
  `TestServeReportsPostUpgradeStartupUntilTokenReady` and
  `TestServeDoesNotReportPostUpgradeStartupForBrandNewDB`.

Invariants (spec note 2): a job whose command succeeded is ALWAYS reported
`complete`, never `deleted`; a genuine user delete/remove of an INCOMPLETE job is
still reported `deleted`.

Tests in the new file `jobqueue/reliable2_deleted_test.go`. Covers all 3
acceptance tests from C1 (a reserved-started-archived-success job yields a
terminal `JobStateComplete` update and NEVER a `JobStateDeleted` update for that
key; a deleted INCOMPLETE job yields a `JobStateDeleted` update; the A1
parked-lost-then-owner-archived-success churn emits no `deleted` broadcast for
that key).

- [x] implemented
- [x] reviewed

### Item 2.3: C2 - Fast startup: no history scan, no seedStatusState

spec.md section: C2

With `seedStatusStateForItemDefs` and `startCounterBackfill` removed (Item 2.2),
startup no longer seeds any status counts; the retained background recovery
(section H2) is unaffected; the slim counter (Item 2.1) starts empty and is only
populated by live transitions.

Tests in the new file `jobqueue/reliable2_startup_test.go` (replaces the deleted
seeding startup test). Acceptance test 2 references `repGroupCounts.wholeMap()`
from Item 2.1 - that type MUST exist in this phase for C2 to compile, which is
why D1 is sequenced ahead of C2. Covers both C2 acceptance tests: (1) a large
completed-only history (e.g. 25k vs 250k archived jobs via the existing
`testDBArchivedJob`/`storeNewJobs`/`archiveJob` helpers) does NOT make startup
scale with history size (`largeElapsed < 4 * smallElapsed`, absolute within a few
seconds); (2) no per-RepGroup complete counter is seeded - assert structurally
that the web counter's `wholeMap()` is empty of pre-seeded `complete` counts
until a live transition occurs.

- [x] implemented
- [x] reviewed

### Item 2.4: D2 - CLI wr status count path stays a scan (unchanged)

spec.md section: D2

`getStatusByRepGroup` (server.go:1312) already computes counts by scanning the
live queue + complete bucket (`retrieveCompleteJobStatusByRepGroup`, a raw
RTK-prefix scan) and does NOT consume `statusState` or the removed maintained
counter. Requirement: leave this path as a scan; do NOT route the CLI fast-count
path to the slim web-UI counter (a never-seeded counter would under-report
`complete` as 0 after restart - strictly worse than v0.36.5). `cmd/status.go`
behaviour is unchanged.

Add one focused assertion in `jobqueue/reliable2_startup_test.go`. Covers the 1
D2 acceptance test (after restart on a DB with N archived jobs in `rg`, with the
web counter empty, `GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true,
false)` returns `Counts[JobStateComplete] == N`).

- [x] implemented
- [x] reviewed

## Regression guards (KEEP surfaces, section H)

Re-run after this phase; all must stay green (spec.md section H1, plus
`TestLostDetectionSilentRunner` per section B):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner` (KEEP).
- `jobqueue/subscription_test.go` (`#503`), `jobqueue/live_jtouch_test.go`
  (`#530`/`#534`, incl. ssh-to-host), the `JobUpdateResync` reconnect/resync
  tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, and the
  `wr add --sync` client test.
- `make test`, `make race`, `make lint` all clean.
