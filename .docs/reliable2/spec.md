# Reliable Job Execution Restore (Option R) Specification

## Overview

Reliable job running is the top priority; web-UI aggregate count accuracy is
secondary and may regress to v0.36.5 quality. The post-v0.36.5 web-UI-accuracy
work (`#533` absolute-state counts + the `#547`/`#548`/`#550` fallout patches)
broke reliable execution: successful commands are discarded and re-run under
load, succeeded jobs are broadcast `deleted`, and large-DB startup stalled. This
spec surgically removes that machinery and reverts the completion/lost path to
v0.36.5 semantics, while keeping every genuinely-useful post-v0.36.5 feature.

Required outcomes: (1) a command that exits 0 is always recorded `complete` and
never discarded, even after a transient loss under load; (2) the web UI never
shows a succeeded job as `deleted`; (3) manager startup on a large real DB is
responsive with no history scan; (4) `wr status` stays no worse than v0.36.5;
(5) all KEEP features keep working; (6) the reworked build opens a
current-code-upgraded DB without error or data loss.

Baseline = branch `reliable2` (`develop` + `#547`/`#548`/`#550`). The
`queues_avoid` client fix (`.docs/bugfixes/260720-1.md`) is already landed -
keep it, do not respec it. Grounding evidence: `.docs/reliable2/testing.md` and
`choice.md` ("Option R"); reference behaviour is v0.36.5's `SetTTRCallback`,
`jtouch`, `jarchive` (`git show v0.36.5:jobqueue/{server,serverCLI}.go`).

The change is internal-only except two deliberately-accepted user-facing
regressions: web-UI aggregate count accuracy drops to v0.36.5 quality, and the
`wr manager recompute-counts` subcommand is removed.

## Architecture

### Packages / files touched

- `jobqueue/serverCLI.go` - archive/touch handlers, `getij`, remove
  `canCompleteFromQueueState`.
- `jobqueue/server.go` - `ttrCallback`, remove contact-grace + counter-backfill
  + `seedStatusState*`; keep the recovery window.
- `jobqueue/jobtransition.go` - un-wrap `emitJobTransition`; remove
  `changeCallbackToState` and the `#533` count-contribution helpers.
- `jobqueue/statusstate.go` - DELETE; replaced by a slim counter (new file
  `jobqueue/repgroupcounts.go`).
- `jobqueue/persistedstatus.go` - DELETE (feeds only the removed count
  accuracy).
- `jobqueue/serverWebI.go` - web-UI status listener reads the slim counter.
- `jobqueue/db.go` - delete per-RepGroup complete-counter machinery + its
  `Recompute*`; add `putJobStats` guard; keep the recent-index + all
  authoritative data.
- `jobqueue/job.go` - drop the two unexported `#533` count fields; keep a
  decode-compatible exported `Job`.
- `cmd/manager.go` - delete the `recompute-counts` subcommand.
- `cmd/status.go` - unchanged behaviour (its fast path already scans; verify).
- `jobqueue/static/js/wr/websocket-handler.js` - UNCHANGED (wire format
  preserved).

### Exact DELETIONS (scope: unambiguous)

Code:

- `statusState` type and every method (`newStatusState`, `seed`,
  `seedRepGroupComplete`, `hasRepGroup`, `liveSeedLocked`, `applyTransition(s)`,
  `applyTransitionLocked`, `applyToRepGroupLocked`, `markDirtyLocked`,
  `subscribe`, `unsubscribe`, `drain`, `snapshot`, subscriber types) - whole
  file `statusstate.go`; the `s.statusState` field (`server.go:759`) and its
  init (`server.go:2642`).
- `seedStatusStateForItemDefs` (`server.go:1012`) and its call in `enqueueItems`
  (`server.go:3977`).
- `changeCallbackToState` (`jobtransition.go:171`) and the `#533` count-accuracy
  helpers `changeCallbackCounts` historical branches:
  `groupArchivedRerunContributions`, `groupHistoricalCompletionContributions`,
  and the `statusFromComplete`/`statusCompleteRepGroups` handling within them.
- `persistedstatus.go` (whole file) + the `Job` fields `statusFromComplete`,
  `statusCompleteRepGroups` (`job.go:418-419`) + all
  `markPersistedJobStatusGroups` calls (`serverCLI.go:1107`, `server.go:3016`,
  `server.go:4105`).
- Per-RepGroup complete-counter machinery in `db.go`: `bucketRepGroupComplete`
  (`repGroupCompleteCount`), `bucketRepGroupBackfilled`
  (`repGroupCompleteBackfilled`), `backfillSentinelKey`,
  `adjustRepGroupComplete` + `adjustRepGroupCompleteForRTKKey` + their call
  sites (`db.go:897`, `:1279`, `:1605`), `retrieveMaintainedCompleteCounts`,
  `backfillRepGroupCompleteCounts`, `setRepGroupCompleteFromRawScan`,
  `fullyBackfilled`, `markBackfillSentinel`,
  `recomputeRepGroupComplete(Counts)`, `RecomputeRepGroupCompleteCounts`,
  `ensureRecomputeBuckets`, and the two `CreateBucketIfNotExists` calls for
  those buckets (`db.go:660,665`). Also delete `startCounterBackfill`
  (`server.go:1129`) and its call (`server.go:2718`).
- `#550` F0 runner-contact grace: `recordJobContact` (`server.go:1070`),
  `contactedWithin` (`server.go:1084`), `forgetJobContacts` (`server.go:1102`),
  the `lastContact` map + `lastContactMu` (`server.go:785,806`) and its init
  (`server.go:2651`), the `handleTouch` `recordJobContact` call
  (`serverCLI.go:910`), the `emitChangeCallbackTransition` `forgetJobContacts`
  block (`jobtransition.go:214-216`), and the `ttrCallback` `contactedWithin`
  guard (`server.go:3535-3541`).
- `canCompleteFromQueueState` (`serverCLI.go:1064`).
- `recompute-counts` subcommand: `managerRecomputeCountsCmd`,
  `managerRecomputeExit`, `recomputeCounts` var (`cmd/manager.go:562-611`), and
  its `AddCommand` (`cmd/manager.go:1198`).

Tests (note 6 + note 7):

- DELETE whole: `statusstate_invariant_test.go`, `repgroup_counter_test.go`,
  `nonblocking_startup_test.go`, `status_count_test.go`, `statusstate_test.go`.
- `lost_detection_test.go`: DELETE `TestLostDetectionRecentContactNotLost` (pins
  F0 grace); KEEP `TestLostDetectionSilentRunner`.
- `server_startup_test.go`: DELETE
  `TestServeStartsQuicklyWithLargeCompletedHistory` + helpers
  `completedOnlyHistoryStartupDuration`,
  `prepareCompletedOnlyHistory`, `waitForStartupStatusCounts` (reads
  `statusState.snapshot`) + consts `startupActiveHistoryRepGroup`,
  `startupSmall/LargeCompletedHistorySize`, `startupHistoryStartupScaleLimit`.
  KEEP `TestServeReportsPostUpgradeStartupUntilTokenReady` and
  `TestServeDoesNotReportPostUpgradeStartupForBrandNewDB` (general DB-upgrade
  progress reporting, independent of the removed seeding).
- `.docs/reliable2/harness/reliable2_churn_test.go` (a `package jobqueue` test
  file under `.docs/`): DELETE the whole file (or at minimum its
  `TestReliable2DoubleReservationDiscardsSuccess`) - the rewritten A1 oracle in
  `jobqueue/reliable2_completion_test.go` SUPERSEDES it, so no stale
  test of the old/broken discard behaviour remains and no duplicate `package
  jobqueue` test file lingers under `.docs/` (acceptance #1; see A1).

### Exact KEEP surfaces (do not remove or break)

- `#503` subscriptions: `enqueueSubscriptionUpdate`, `subscription.go`,
  `server_subscription.go`, the change-callback subscription delivery
  (`enqueueChangeCallbackSubscriptions`), `hasAnyClientSubscriptions`.
- `#530`/`#534` live introspection: `emitLiveTouchSnapshot`,
  `applyLiveSnapshot`, `jobUpdateFromLiveJob`, ssh-to-host detail
  (`live_jtouch_test.go`).
- Reconnect/resync: `JobUpdateResync` (`subscription.go:69,347`).
- Web+REST actions: rerun completed, modify incomplete, suspend/resume, the
  `suspended` state, `wr status --suspended`.
- `wr add --sync` non-polling wait (`cmd/add.go:waitForSynchronousJob` over
  subscriptions) and other orthogonal fixes (memory-misreport reason, bulk-add
  dependent-dedup, `--rerun` with incomplete deps, cloud quota-leak, key-gen
  speedups, `wr status --recent`, `-o table`, log rotation).
- Background prior-state recovery window (note 8): `startPriorStateRecovery`,
  `recoverInBackground`, `isRecovering`/`finishRecovering`, `setRecovering`, the
  retryable `ErrRecovering` returned by `getij` (`serverCLI.go:1701`),
  `rescheduleReadyAfterRecovery`, the no-overcommit-during-recovery scheduling
  gate (`readyAddedCallback` `!s.isRecovering()`).
- The recent-completed index `endTimeToKey`/`repgroupEndTime` and its
  maintenance (`updateRGEndTime`, `retrieveCompleteJobsRecent`,
  `scanCompleteJobsRecent`) - see DB-compatibility note below.

### Error handling

Reuse existing `Err*` string constants (`server.go:79-98`): `ErrBadJob` (not in
queue / wrong sub-queue), `ErrMustReserve` (caller is not the owner),
`ErrBadRequest` (bad end-state), `ErrRecovering` (retryable, recovery window),
`ErrDBError`, `ErrInternalError`. No new error types.

### DB compatibility (additive upgrade; no schema-version gate)

The DB upgrade is additive and non-destructive (`CreateBucketIfNotExists`, no
in-DB version marker, no `DeleteBucket` of authoritative data, indices rebuilt
from job buckets). The reworked build MUST:

- open a current-code-upgraded DB without error; retain a decode-compatible
  `Job` (the ugorji binc codec tolerates field diffs; the two post-v0.36.5
  exported fields are `WaitingForDepGroups`, `LimitGroupsForDisplay`);
- NOT assert absence of the now-dead buckets `repGroupCompleteCount`,
  `repGroupCompleteBackfilled` - leave them as harmless dead data (cleaning them
  is optional, not required; do not add a schema-version gate);
- NOT re-run the one-time index rebuilds (`bucketRTK`, `bucketJobLookupEntries`,
  dep-group index) - they are already populated on an upgraded DB.

RESOLVED CONFLICT (flagged): prompt/`choice.md` list `endTimeToKey` and
`repgroupEndTime` among "dead buckets", but the code shows they back `wr status
--recent` (`retrieveCompleteJobsRecent` seeks `bucketEndTimeToKey`;
`updateRGEndTime` writes `bucketRGEndTime` on archive) - a KEEP feature. They
are therefore RETAINED and still maintained; only `repGroupCompleteCount` and
`repGroupCompleteBackfilled` are genuinely dead. This is the single point where
two authoritative statements conflicted; resolved in favour of the KEEP list +
code evidence (removing the index would break `--recent`).

---

## A. Completion path revert (PRIMARY correctness fix)

Prompt: sec 1, note 1. Acceptance: #1.

### A1: A holding runner's successful archive is always accepted

As a runner that finished its command successfully, I want my `complete` result
accepted while I still own the reservation, so that work is never discarded and
re-run under load.

Restore v0.36.5's lenient `jarchive`: accept a successful archive (`Exited &&
Exitcode==0 && !StartTime.IsZero() && !EndTime.IsZero()`) from the client that
still owns the item while the queue item is in `SubQueueRun` - with NO
`job.State` gate and NO attempt-epoch field (out of scope, note 1).

Changes:

- `handleArchive` (`serverCLI.go:1013`): call `getij(cr, true)` (item must be in
  `queue.ItemStateRun`; preserves the `ErrRecovering` retry path and the owner
  check that returns `ErrMustReserve`).
- `markJobComplete` (`serverCLI.go:1033`): DELETE the
  `canCompleteFromQueueState(item.Stats().State)` gate. Keep the owner check
  (`ReservedBy == cr.ClientID` -> else `ErrMustReserve`) and the end-state check
  (`canCompleteFromEndState` -> else `ErrBadRequest`). On success set
  `State=JobStateComplete`, `FailReason=""`, `Lost=false`, apply the end state,
  and return key/repGroup/schedulerGroup.
- A `Lost`-but-parked-in-`Run` job (its `State` may still be `Running`) whose
  owner archives success is accepted, because the item is in `Run` and the owner
  matches - exactly v0.36.5's contract.

**Package:** `jobqueue/` **File:** `jobqueue/serverCLI.go` **Test file:**
`jobqueue/reliable2_completion_test.go` (new)

**Acceptance tests (map to #1; run under `-race`):**

Rewrite the churn oracle per note 1 as the "an alive job is never re-reserved"
model (replacing `.docs/reliable2/harness/reliable2_churn_test.go`'s old
buggy-assert oracle). Because the rewritten oracle lives in
`jobqueue/reliable2_completion_test.go` (it needs unexported helpers) and
SUPERSEDES that harness test, DELETE
`.docs/reliable2/harness/reliable2_churn_test.go` (or at least its
`TestReliable2DoubleReservationDiscardsSuccess`): no stale test asserting the
old/broken behaviour may remain and no duplicate `package jobqueue` test file
may linger under `.docs/` (acceptance #1). Use existing package helpers
`jobqueueTestInit(true)`,
`serve`, `Connect`, `disconnect`, `restFormTrue`, `testCwdPath`. Set
`serverConfig.Timings.ItemTTR = 500ms`. Start runner A's job with
`jq.Started(reserved, os.Getpid())` so the async dead-confirmation finds the PID
alive and cannot remove the job mid-test (the determinism trick used by
`TestLostDetectionSilentRunner`).

1. Given a job reserved+started by runner A (PID = `os.Getpid()`) and never
   touched, when its TTR (500ms) expires, then within a few TTRs the server-side
   item is still in `SubQueueRun` (`server.q.Get(key)` -> `item.Stats().State ==
   queue.ItemStateRun`) and the job has `Lost == true`.
2. Given that parked-lost job, when runner B calls `Reserve(200ms)` up to 20
   times, then every call returns `nil` (B cannot re-reserve an alive-owned
   job).
3. Given A still owns the reservation, when A calls `Archive(reserved,
   &JobEndState{Exited:true, Exitcode:0, EndTime:time.Now()})`, then the
   returned error is `nil`.
4. Given A's archive succeeded, when the rep group is queried
   (`GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)`), then
   `summaries[rg].Counts[JobStateComplete] == 1` and the command ran exactly
   once (no re-run by B).
5. Given a client that no longer owns the item (a genuine stale archiver), when
   it archives, then the error is `ErrMustReserve` (a pure revert still rejects
   a non-owner - this guards against the opposite over-acceptance).

---

## B. TTR / lost path revert

Prompt: sec 1, note 7. Acceptance: #6.

### B1: Pure v0.36.5 `ttrCallback`; F0 contact grace removed

As the manager, I want a TTR-expired but still-running job parked in
`SubQueueRun` with a `Lost` flag (recoverable by a late touch) and a genuinely
silent runner still detected dead, so that alive work is never re-run and dead
work is still reclaimed - without any contact-based grace.

Changes:

- `ttrCallback` (`server.go:3515`): DELETE the `if s.contactedWithin(job.Key(),
  ttr)` block (`server.go:3535-3541`). Retain the rest: zero-start/exited ->
  `SubQueueDelay`; already-`Lost` -> parked `SubQueueRun` (no
  re-mark/re-confirm); otherwise set `Lost=true`, `FailReason=FailReasonLost`,
  `EndTime=now`, defer `markJobLost`, return `SubQueueRun`. Delete all F0
  symbols listed in Architecture.
- A late `touch` still clears `Lost` and resets the TTR: `handleTouch`
  (`serverCLI.go:905`) minus its `recordJobContact` call; `touchJob` ->
  `recoverLostTouchedJob` clears `Lost`/`EndTime` and records the lost->running
  transition through the retained chokepoint. An on-time touch resets the TTR
  via `q.Touch`, so `ttrCallback` never fires for it.
- Genuine death still handled by `markJobLost` -> `confirmOrReleaseLostJob` ->
  `confirmJobDeadAndKill` (unchanged), re-running a confirmed-dead job within ~1
  TTR.

Safety (note 7): a spuriously-set `Lost` under saturation is now benign - a flag
the next touch clears - because a `Lost` job is parked in `Run`, never
re-reserved while its runner is alive, and its owner's success is always
accepted (A1). Do NOT re-introduce any contact-based grace.

**Package:** `jobqueue/` **File:** `jobqueue/server.go`, `jobqueue/serverCLI.go`
**Test file:** `jobqueue/lost_detection_test.go` (edit),
`jobqueue/reliable2_lost_test.go` (new)

**Acceptance tests (map to #6):**

1. (KEEP `TestLostDetectionSilentRunner`) Given a job reserved+started with PID
   = `os.Getpid()` and never touched, `ItemTTR = 500ms`, when up to `6*ttr`
   elapses, then the server-side job has `Lost == true` (silent runner still
   detected).
2. (new) Given a job reserved+started, `ItemTTR = 500ms`, when the runner calls
   `Touch` every ~250ms (within the TTR) for `>= 4` TTRs, then at every sample
   the job has `Lost == false` and `FailReason == ""` (an alive, on-time-touched
   job is never lost) and the item stays in `SubQueueRun`.
3. (new) Given a job marked `Lost` (as in test 1) whose runner then sends one
   `Touch`, when the touch is processed, then the job has `Lost == false` and
   its TTR is reset (recovery), and the item is still in `SubQueueRun`.

---

## C. Remove `#533` aggregate count machinery + `deleted` projection

Prompt: sec 2, note 2. Acceptance: #2, #3.

### C1: Un-wrap the transition chokepoint; derive to-state from the job

As a subscriber, I want the per-job update to carry the job's real state, so
that a succeeded job is reported `complete` and never `deleted`.

Changes:

- `emitJobTransition` (`jobtransition.go:74`): keep the `emitSubscriptions()`
  half; replace `s.statusState.applyTransitions(counts)` with an update to the
  slim counter (D1). Signature unchanged: `func (s *Server)
  emitJobTransition(counts []countContribution, emitSubscriptions func())`.
- `emitChangeCallbackTransition` (`jobtransition.go:204`): DELETE
  `changeCallbackToState`; derive each job's to-state directly from its own
  `job.State` at emission time (per-job, inside
  `enqueueChangeCallbackSubscriptions`). Simplify `changeCallbackCounts` to a
  plain per-RepGroup `from->to` increment (v0.36.5 quality); drop the
  historical-completion/rerun contribution helpers and the `statusFromComplete`
  branch. Remove the `forgetJobContacts` block.
- The subscription layer, its gating (`subscriptionUpdateState`, per-subscriber
  filtering, idle fast-path), and `enqueueSubscriptionUpdate` are UNCHANGED
  (KEEP) - only the to-state source changes.

Invariants (note 2): a job whose command succeeded is ALWAYS reported
`complete`, never `deleted`; a genuine user delete/remove of an INCOMPLETE job
is still reported `deleted`.

Implementor note: deriving `deleted` for a genuinely removed incomplete job
relies on that job's `State` reading as `JobStateDeleted` at the moment the
subscription/broadcast update is emitted, so the removal path must set/observe
that state before emission (test C1.2 pins this).

**Package:** `jobqueue/` **File:** `jobqueue/jobtransition.go` **Test file:**
`jobqueue/reliable2_deleted_test.go` (new)

**Acceptance tests (map to #2):**

1. Given a client subscribed to a rep group's job updates, when the job is
   reserved, started and archived successfully (`Exitcode==0`), then the
   subscriber receives a terminal update with to-state `JobStateComplete` and
   NEVER an update with `JobStateDeleted` for that key.
2. Given a subscriber and an INCOMPLETE job (added, never run), when the job is
   deleted/removed by the user, then the subscriber receives a `JobStateDeleted`
   update (v0.36.5-quality display retained).
3. Given the churn scenario of A1 (parked-lost then owner-archived success),
   when the job leaves the queue as `complete`, then no `deleted` broadcast is
   emitted for that key.

### C2: Fast startup - no history scan, no seedStatusState

As an operator, I want the manager responsive within a few seconds on a large
completed-history DB, so that startup never stalls on a status scan.

Changes: with `seedStatusStateForItemDefs` and `startCounterBackfill` removed
(Architecture), startup no longer seeds any status counts. The retained
background recovery (note 8, section H) is unaffected. The slim counter (D1)
starts empty and is only populated by live transitions.

**Package:** `jobqueue/` **File:** `jobqueue/server.go` **Test file:**
`jobqueue/reliable2_startup_test.go` (new; replaces the deleted seeding startup
test)

**Acceptance tests (map to #3):**

1. Given a DB pre-populated with a large completed-only history (e.g. 25k vs
   250k archived jobs via the existing
   `testDBArchivedJob`/`storeNewJobs`/`archiveJob` helpers), when `serve` is
   started and readiness awaited, then startup time does NOT scale with history
   size: `largeElapsed < 4 * smallElapsed`, and the absolute elapsed is within a
   few seconds.
2. Given the same startup, when it completes, then no per-RepGroup complete
   counter was seeded (there is no `statusState`/seed call; assert structurally
   that the web counter's whole map is empty of pre-seeded `complete` counts
   until a live transition occurs).

---

## D. Web-UI count feed - slim absolute per-RepGroup counter

Prompt: sec 3, note 3.

### D1: Slim live counter emitting the unchanged `jstateAbsolute`

As the web UI, I want per-RepGroup status bars fed by a slim absolute counter
that emits the existing `jstateAbsolute` message, so that the frontend and wire
format are unchanged while startup seeding stays removed.

New type (recommended shape; behaviour pinned by tests) in new file
`jobqueue/repgroupcounts.go`:

```go
// repGroupCounts holds slim absolute per-RepGroup job-state counts for the web
// UI status bars. Special group statusAllRepGroups ("+all+") aggregates live
// states across all RepGroups. Maintained live from transitions only; never
// seeded from history.
type repGroupCounts struct {
    mu       sync.Mutex
    counts   map[string]map[JobState]int
    // minimal per-listener dirty/wake tracking for the websocket push
}

func newRepGroupCounts() *repGroupCounts
func (c *repGroupCounts) applyTransitions(transitions []countContribution)
func (c *repGroupCounts) wholeMap() map[string]map[JobState]int // deep copy
```

Requirements:

- Replaces `s.statusState` (field renamed, e.g. `s.repGroupCounts`), initialised
  empty in `serve`.
- `applyTransitions` applies each `countContribution` (`from`, `to`, `repGroup`,
  `n`) to the absolute map, maintaining the `"+all+"` live aggregate; called
  from `emitJobTransition` (C1). Same lock discipline as before (strict-leaf
  mutex, taken last; never before queue/job/subscription locks).
- Web-UI listener (`serverWebI.go:setupStatusStateUpdateListener`,
  `sendStatusStateUpdates`): on (re)connect push `wholeMap()` (the whole current
  in-memory map, INCLUDING terminal states - do NOT replicate the removed
  `liveSeedLocked` terminal-hiding filter); thereafter push per-RepGroup
  `jstateAbsolute{RepGroup, Counts}` on change, throttled as today. The
  `jstateAbsolute` struct (`server.go:504`) and `websocket-handler.js` are
  UNCHANGED. Do NOT revert to v0.36.5's `statusCaster`/`jstateCount` delta
  broadcasting - the slim counter emits the existing absolute `jstateAbsolute`
  message.
- NEVER seeded by a history scan; a manager restart yields an initially-empty
  counter that fills from live transitions (v0.36.5-quality accuracy: flicker /
  overcount under high update rates accepted).

**Package:** `jobqueue/` **File:** `jobqueue/repgroupcounts.go`,
`jobqueue/serverWebI.go` **Test file:** `jobqueue/repgroupcounts_test.go` (new)

**Acceptance tests (map to sec 3 / note 3):**

1. Given a fresh server (empty counter), when a job in rep group `rg` goes
   new->ready->running->complete, then the counter's `wholeMap()[rg]` reflects
   the live absolute counts at each step and `[statusAllRepGroups]` tracks the
   live total.
2. Given a connected web-UI status client, when a transition occurs, then it
   receives a `jstateAbsolute{RepGroup, Counts}` JSON message whose fields and
   shape are byte-compatible with the pre-change format (unchanged wire).
3. Given RepGroups whose only jobs are terminal (complete), when a client
   connects, then the connect-seed (`wholeMap()`) INCLUDES those RepGroups'
   counts (no terminal-hiding filter).
4. Given a restarted manager on a DB with prior completed jobs, when a client
   connects before any new transition, then the counter is empty (never seeded)
   - proving no history scan on startup.

### D2: CLI `wr status` count path stays a scan (unchanged)

As a CLI user, I want `wr status -o counts`/`-o summary` accurate, so it must
not regress below v0.36.5.

The server handler `getStatusByRepGroup` (`server.go:1312`) already computes
counts by scanning the live queue + complete bucket
(`retrieveCompleteJobStatusByRepGroup`, a raw RTK-prefix scan) and does NOT
consume `statusState` or the removed maintained counter. Requirement: leave this
path as a scan; do NOT route the CLI fast-count path to the slim web-UI counter
(a never-seeded counter would under-report `complete` as 0 after restart -
strictly worse than v0.36.5). `cmd/status.go` behaviour is unchanged.

**Test file:** covered by existing `getStatusByRepGroup` tests remaining green;
add one focused assertion in `reliable2_startup_test.go`:

1. Given a restarted manager on a DB with N archived jobs in `rg` (and the web
   counter empty), when `GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil,
   true, false)` is called, then `Counts[JobStateComplete] == N` (the scan is
   accurate independent of the slim counter).

---

## E. Remove `wr manager recompute-counts`

Prompt: note 5. Accepted user-facing removal.

### E1: Subcommand and backing function deleted

Delete `managerRecomputeCountsCmd`, `managerRecomputeExit`, the
`recomputeCounts` var (`cmd/manager.go:562-611`), the `AddCommand`
(`cmd/manager.go:1198`), and `jobqueue.RecomputeRepGroupCompleteCounts` (with
the DB machinery in section Architecture).

**Package:** `cmd/`, `jobqueue/` **File:** `cmd/manager.go`, `jobqueue/db.go`
**Test file:** delete the recompute-counts CLI test(s) if present (they pin the
removed command).

**Acceptance tests:**

1. Given the built `wr` binary, when `wr manager recompute-counts` is run, then
   it is an unknown subcommand (cobra error / non-zero), i.e. the command no
   longer exists.
2. Given the `jobqueue` package, when it is built, then
   `RecomputeRepGroupCompleteCounts` is not a symbol (compile-time; the deletion
   is complete).

---

## F. DB compatibility with current-code-upgraded databases

Prompt: sec 4, note 4. Acceptance: #5.

### F1: Open a current-upgraded DB with the reworked build

As an operator upgrading in place, I want the reworked build to open a DB
already upgraded by current code without error or data loss.

Realise with a SMALL committed binary fixture DB, produced once by current
(`reliable2`) code, containing: the now-dead buckets (`repGroupCompleteCount`,
`repGroupCompleteBackfilled`), the retained buckets, jobs with the two
post-v0.36.5 fields (`WaitingForDepGroups`, `LimitGroupsForDisplay`) set,
populated indices (`bucketRTK`, `bucketJobLookupEntries`, `endTimeToKey`,
`repgroupEndTime`, dep-group index), and a mix of complete + incomplete jobs (a
handful, not the multi-million-job farm artefact).

Fixture location: `jobqueue/testdata/dbcompat/db.golden`.

Regeneration procedure (document verbatim in `jobqueue/testdata/README.md`):

1. Check out a `reliable2` commit that STILL maintains the counters (the parent
   of this change, or any pre-removal build) - the fixture must contain the dead
   buckets, which only pre-change code writes.
2. Build/run the committed generator `jobqueue/testdata/dbcompat/gen.go` (a
   `//go:build ignore` program): open a fresh BoltDB via the `jobqueue` DB open
   path; add ~4 jobs across two rep groups, at least two carrying non-empty
   `WaitingForDepGroups` and `LimitGroupsForDisplay`; reserve+start+archive two
   of them (so `jobscomplete`, `endTimeToKey`, `repgroupEndTime`, and
   `repGroupCompleteCount` populate and the backfill sentinel is written); leave
   the rest incomplete in `jobslive`. Close cleanly.
3. Copy the resulting bolt file to `jobqueue/testdata/dbcompat/db.golden` and
   `git add` it (binary). Keep it small.

The test copies the fixture into `t.TempDir()` (BoltDB needs exclusive
read-write open), then opens it with the reworked build.

**Package:** `jobqueue/` **File:** `jobqueue/testdata/dbcompat/db.golden`
(fixture), `jobqueue/testdata/dbcompat/gen.go` (generator),
`jobqueue/testdata/README.md` (procedure) **Test file:**
`jobqueue/reliable2_dbcompat_test.go` (new)

**Acceptance tests (map to #5):**

1. Given the committed fixture copied into `t.TempDir()`, when the reworked
   `serve` opens it, then it returns no error and does not crash (no panic on
   the dead buckets, no decode error on the two new `Job` fields).
2. Given the opened DB, when the complete rep group is queried
   (`GetStatusByRepGroupMatch(..., includeComplete=true)`), then the known
   complete jobs are returned as `JobStateComplete` with the expected count.
3. Given the opened DB, when recovery finishes, then the known incomplete jobs
   are recovered and become reservable/runnable (served by the retained recovery
   path, section H).
4. Given the fixture's index buckets, when the DB is opened, then the one-time
   index rebuilds do NOT re-run: read `bucketRTK` and `bucketJobLookupEntries`
   key counts before and after open and assert they are unchanged (an
   already-populated index is not rebuilt), and assert no post-upgrade rebuild
   was reported for the already-indexed buckets.

---

## G. Orthogonal fix - `db.putJobStats` guard

Prompt: note 9. The one deliberately-included fix beyond the revert.

### G1: Do not store a corrupt duration stat

As the scheduler's time-learning, I want no negative/overflow duration recorded,
so that future time recommendations are not corrupted.

Change `putJobStats` (`db.go:2017`): guard the runtime stat - do NOT store a
`bucketJobSecs` value when `job.EndTime.IsZero()` OR when the computed duration
(`job.EndTime.Sub(job.StartTime)`) is `<= 0`. Still store RAM/disk stats as
today. No one-time repair of existing entries (guard only).

**Package:** `jobqueue/` **File:** `jobqueue/db.go` **Test file:**
`jobqueue/db_test.go` (add focused test)

**Acceptance tests (map to note 9):**

1. Given a job with `EndTime` zero (`time.Time{}`) and a valid `ReqGroup`, when
   `putJobStats` runs, then the `bucketJobSecs` bucket has NO entry for that
   `ReqGroup` (no `~MinInt64` value stored), while RAM/disk entries are stored.
2. Given a job with `EndTime` before `StartTime` (non-positive duration), when
   `putJobStats` runs, then no `bucketJobSecs` entry is stored for it.
3. Given a job with a valid positive duration (`EndTime` after `StartTime`),
   when `putJobStats` runs, then a `bucketJobSecs` entry equal to
   `ceil(seconds)` is stored (happy path unaffected).

---

## H. KEEP-feature regression coverage

Prompt: KEEP list, note 8. Acceptance: #4.

These features are verified independent of `statusState`; the acceptance is that
their existing tests remain green after the removal, plus the focused checks
below. Do NOT weaken any existing KEEP test.

### H1: Subscriptions, live data, resync, actions, add --sync

**Existing anchors that MUST stay green** (do not delete/weaken):
`subscription_test.go` (`#503` per-job subscriptions), `live_jtouch_test.go`
(`#530`/`#534` live RAM/CPU/STDOUT incl. ssh-to-host), the `JobUpdateResync`
reconnect/resync tests, `suspend_resume_test.go` + `wr status --suspended`,
`modify_validation_test.go`, `serverWebI_test.go` (rerun/modify/suspend/resume),
and the `wr add --sync` client test.

**Acceptance tests (map to #4):**

1. Given a subscriber, when a job progresses new->running->complete, then it
   still receives per-job `JobUpdate`s AND a live RAM/CPU/STDOUT snapshot from a
   touch (`emitLiveTouchSnapshot`), unchanged by the removal.
2. Given a subscriber that reconnects mid-run, when it re-subscribes, then it
   receives a `JobUpdateResync` and catches up.
3. Given a completed job, when Rerun is invoked (web/REST), then it re-runs;
   given an incomplete job, modify and suspend/resume still work and `wr status
   --suspended` lists suspended jobs.
4. Given `wr add --sync` for a command that completes, then the client returns
   on completion via the subscription (non-polling), unchanged.

### H2: Background prior-state recovery window retained (note 8)

As a runner reconnecting mid-recovery, I want a retryable error and correct
re-accounting, so that recovery timing never causes a false loss or overcommit.

KEEP `recoverInBackground`, `isRecovering`/`finishRecovering`/`setRecovering`,
the `ErrRecovering` returned by `getij` (`serverCLI.go:1701`),
`rescheduleReadyAfterRecovery`, and the `!s.isRecovering()` scheduling gate.
Only the `seedStatusState`/counter-backfill work is removed from startup, not
this.

**Test file:** existing recovery tests remain green; add to
`reliable2_dbcompat_test.go`:

1. Given a job whose key is not yet restored during the recovery window, when a
   reconnecting runner calls a `j*` method for it, then `getij` returns
   `ErrRecovering` (retryable), not `ErrBadJob`.
2. Given prior incomplete jobs in a DB, when `serve` starts, then they are
   recovered and become reservable after recovery finishes (acceptance #5's
   "incomplete jobs recover and run" is served here).

---

## I. Scale / throughput validation (#7) - VALIDATION, not a unit test

Prompt: acceptance #7, Constraints. Documented as a farm/saturation validation
step, NOT a GoConvey unit test (the in-process oracle A1 is necessary but not
sufficient - see `testing.md`).

Before shipping, validate at the churn-triggering scale:

- Steady-state throughput >= current (metric M7).
- A scale run at ~6-7k concurrent runners (the `portal_builder` workload on the
  farm, per `testing.md`'s harness, or an in-process saturation harness that
  lowers the reader threshold) shows: successful commands recorded `complete`
  (M1 ~100%, M2 = 0 `jarchive: bad job` for exit-0 jobs), no `deleted` broadcast
  for succeeded jobs (M4), bounded heavy `wr status` latency (M5), and clean
  completion + responsive status matching v0.36.5.

Record the result in `.docs/reliable2/` before merge. This is a gate, not code.

---

## Implementation Order

Sequenced by the prompt's priority (completion/lost revert first, then machinery
removal, then count feed, then DB-compat + orthogonal guard). Each phase builds
on tested foundations from prior phases.

1. **Completion + lost revert (sections A, B).** PRIMARY correctness. Revert
   `handleArchive`/`markJobComplete` to lenient v0.36.5 acceptance (delete
   `canCompleteFromQueueState`); revert `ttrCallback` and delete the F0
   contact-grace symbols. Land the rewritten churn oracle (A1) and lost tests
   (B1) under `-race`, and edit `lost_detection_test.go`. Depends on nothing;
   unblocks everything. (This alone fixes "lost" once C removes the discard's
   projection sibling.)

2. **Remove `#533` count machinery + `deleted` projection (section C).** Delete
   `statusState`, `changeCallbackToState`, `seedStatusState*`,
   `persistedstatus.go` + the two `Job` count fields, and the
   `startCounterBackfill`/seeding call sites; un-wrap `emitJobTransition` to
   keep only `emitSubscriptions` and derive the to-state from `job.State`;
   delete the note-6 test files and the seeding startup test. Depends on nothing
   in phase 1 but shares the transition files; sequence after 1 to keep the
   correctness revert isolated. Provides fast startup (C2) and
   no-false-`deleted` (C1).

3. **Web-UI slim counter (section D).** Add `repGroupCounts`, wire it into
   `emitJobTransition` and the web-UI status listener (connect-seed = whole map,
   `jstateAbsolute` unchanged); confirm the CLI count path stays a scan (D2).
   Depends on phase 2 (the transition un-wrap and `statusState` removal).

4. **Remove `recompute-counts` (section E).** Delete the subcommand + backing
   function + the remaining DB counter machinery. Depends on phase 2/3 (the
   counter is no longer referenced).

5. **DB compatibility (section F) + recovery-window checks (H2).** Add the
   committed fixture + generator + README procedure and the compat/recovery
   tests. Depends on phases 1-4 being in place (opens a DB with the reworked
   build).

6. **`putJobStats` guard (section G).** Orthogonal; can land any time after
   phase 1 but grouped last as an independent fix with its focused test.

7. **KEEP regression sweep (section H1) + scale validation (section I).** Run
   the full KEEP anchor suite green under `-race`; then perform the
   farm/saturation validation gate before merge.

Phases 1-2 are the reliability core and should be reviewed together for the
completion/lost/deleted invariants. Phases 3-6 are more separable.

---

## Appendix: Key Decisions

- **Pure v0.36.5 revert, no attempt-epoch (note 1).** The reliability fix is
  structural: an alive owner is never re-reserved (B) and its success is always
  accepted (A). No new `Job` field; `idea1.md`'s monotonic epoch is out of
  scope.
- **`deleted` vs `complete` from `job.State` (note 2).** Deleting
  `changeCallbackToState` and sourcing the subscription/broadcast to-state from
  the real job state is the structure closest to v0.36.5 (which has no
  projection). Reconciles sec 2 (delete the projection) with KEEP (`#503`
  subscriptions still deliver a correct to-state).
- **Slim counter, frontend unchanged (note 3).** A single absolute per-RepGroup
  counter emitting the existing `jstateAbsolute`; never seeded; connect-seed
  pushes the whole map (no terminal-hiding). CLI counts stay a scan (a
  never-seeded counter would under-report post-restart). Count accuracy =
  v0.36.5 quality (accepted).
- **DB compat = additive (note, sec 4).** No schema-version gate; dead
  `repGroupComplete*` buckets left harmless; decode-compatible `Job`; no re-run
  of index rebuilds. `endTimeToKey`/`repgroupEndTime` are RETAINED (they back
  `wr status --recent`, a KEEP feature) - the single reconciled conflict.
- **Two accepted user-facing regressions.** Web-UI aggregate count accuracy to
  v0.36.5 quality, and removal of `wr manager recompute-counts` (note 5). Both
  are sanctioned exceptions to the internal-only rule.
- **Testing strategy.** GoConvey `So()` assertions only; `t.TempDir()` for DB
  fixtures; the churn oracle and lost/recovery tests run under `-race`; scale is
  a documented validation gate, not a unit test. New files carry the standard
  copyright header. Run tests with OpenStack env unset: `env -u OS_AUTH_URL -u
  OS_USERNAME ... CGO_ENABLED=1 go test -tags netgo -race -count 1 ./jobqueue
  -run <TestFunc>`; lint with `golangci-lint run --fix`.
- **Implementor / reviewer.** Follow `go-implementor` (TDD: write the failing
  acceptance test first, then implement) and `go-reviewer` (verify every
  acceptance test above has a corresponding GoConvey test that genuinely fails
  before the change and passes after), both referencing `go-conventions`.

