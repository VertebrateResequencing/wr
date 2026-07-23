# Spec input: restore reliable job execution by removing the web-UI status-count machinery (#533) and reverting the completion/lost path to v0.36.5 semantics — while keeping every post-0.36.5 feature

## Goal & priority (non-negotiable)

Reliable running of jobs is the top priority; web-UI *count accuracy* is
explicitly secondary and may regress to v0.36.5 quality. The post-0.36.5
web-UI-accuracy work (`#533` absolute-state counts and the `#547`/`#548`/`#550`
attempts to fix its fallout) broke reliable job execution and has never fully
fixed it. This spec **surgically removes that machinery and reverts the
completion/lost path to v0.36.5's behaviour**, while **keeping all the genuinely
useful features** added since v0.36.5.

Required outcomes:
- A command that runs and exits 0 is **always** recorded `complete` and **never
  discarded/re-run**, even if the manager briefly lost track of it under load.
- The web UI **never** shows a job as `deleted` when it actually completed.
- Manager startup on a large real database is quick (no history scan).
- `wr status` stays responsive (no worse than v0.36.5).
- **All listed features keep working** (see KEEP).
- The reworked build **opens databases already upgraded by current code**
  without error or data loss (see DB compatibility).

## What is already done (baseline = branch `reliable2`)

- `reliable2` = `develop` + `#547`/`#548`/`#550`.
- The `queues_avoid` client-side aliasing/race bug is already fixed
  (`.docs/bugfixes/260720-1.md`); keep it.
- Root cause, evidence, the v0.36.5 diff, and the decision are in
  `.docs/reliable2/testing.md` and `.docs/reliable2/choice.md` (this spec
  implements choice.md "Option R"). Reproduction harness in
  `.docs/reliable2/harness/`.

## The desired solution (implement in this priority order)

### 1. Revert the completion & lost path to v0.36.5 semantics — PRIMARY correctness fix
- Replace the strict `canCompleteFromQueueState` (`#548`) with v0.36.5's lenient
  archive acceptance: a successful (`Exited && Exitcode==0`) archive from the
  runner that holds the reservation is accepted while the queue item is in the
  **Run** sub-queue and owned by that client — **no `job.State` gate**. A
  successful result is never discarded.
- Restore v0.36.5's TTR/lost handling: a TTR-expired but still-running job is
  marked with a `Lost` **flag** and **kept in `SubQueueRun`**; a late touch
  clears the flag and resets the TTR (recovery); an alive job is **never moved
  out of `Run`** and never re-reserved. Genuinely dead jobs are still detected
  and re-run within ~1 TTR (as v0.36.5 did via `confirmJobDeadAndKill`).
- This supersedes `#548` and `#550`'s F0 (they were patches for the regressions
  being removed). The reference implementation is v0.36.5's
  `SetTTRCallback`, `jtouch`, and `jarchive` handlers (see Notes).

### 2. Remove `#533`'s aggregate status-count machinery + its dependent patches
- Delete `statusState` and its uses (`applyTransitions`, `seedRepGroupComplete`,
  `hasRepGroup`, `subscribe`/`unsubscribe`, `drain`), `changeCallbackToState`
  (the sole source of the `deleted` broadcast), and `seedStatusState` (the sole
  source of the startup scan/stall).
- Delete the `#547`/`#550` machinery that existed **only** to serve
  `statusState`: the startup seeding-avoidance, the persisted per-repgroup
  completion counters (`repGroupCompleteCount`, `repGroupCompleteBackfilled`)
  and their backfill/recompute, and the non-blocking-startup logic that existed
  to hide the seeding cost (startup is quick again once seeding is gone).
- **Un-wrap** the transition→subscription delivery: `emitJobTransition` currently
  does `statusState.applyTransitions(counts)` then `emitSubscriptions()`. Keep
  the `emitSubscriptions()` half (feature delivery); drop the `applyTransitions`
  half and the `changeCallbackToState` decision.

### 3. Feed the web-UI count display the v0.36.5 way (or accept its behaviour)
- The web-UI status *bars/counts* must still function. Restore v0.36.5's
  transition-count broadcasting (`statusCaster` `jstateCount`) **or** derive the
  display counts from the retained per-job subscription updates. Count accuracy
  may regress to v0.36.5 quality (some flicker/overcount under high update
  rates) — this is accepted. The CLI `wr status` heavy path is unaffected (it
  already scans the complete bucket + live queue, not `statusState`); only the
  fast `wr status -o counts` path is affected — revert it to a scan, or keep a
  single slim counter if cheap.

### 4. Database compatibility with current-code-upgraded databases (first-class)
- The reworked build MUST open a database previously upgraded by current
  (`0.37.x`/`reliable2`) code without error and without losing or re-running
  work. The upgrade is additive (no schema-version gate, no destructive bucket
  deletion), so: do **not** assert the removed buckets are absent — leave
  `repGroupCompleteCount` and `repGroupCompleteBackfilled` as harmless dead
  buckets (optionally clean them in a one-time migration, not required).
  **Correction (verified in code during authoring):** `endTimeToKey` and
  `repgroupEndTime` are **NOT** dead — they back `wr status --recent` (a KEEP
  feature): `retrieveCompleteJobsRecent` seeks `bucketEndTimeToKey` and
  `updateRGEndTime` writes `bucketRGEndTime` on archive. **Retain them and keep
  maintaining them.** Retain a decode-compatible `Job`; do not re-run the
  one-time index rebuilds (their buckets are already populated).

## KEEP — must remain fully working (do not remove or break)

- `#503` job subscriptions (`enqueueSubscriptionUpdate` and the subscription
  layer) — verified independent of `statusState`.
- `#530`/`#534` live RAM/CPU/STDOUT introspection (`emitLiveTouchSnapshot`),
  including the ssh-to-host detail.
- Web UI reconnect/resync (`JobUpdateResync`).
- Web + REST actions: Rerun completed jobs, modify incomplete jobs,
  suspend/resume (and the `suspended` state / `wr status --suspended`).
- `wr add --sync` non-polling wait (client pkg) and other orthogonal fixes:
  memory-misreport reason, bulk-add dependent-dedup, `--rerun` with incomplete
  deps, cloud/OpenStack quota-leak fix, hot-path job-key-gen speedups,
  `wr status --recent`, `-o table`, log rotation.

## Acceptance criteria (TDD targets)

1. **No discarded success:** the churn oracle
   (`.docs/reliable2/harness/reliable2_churn_test.go`,
   `TestReliable2DoubleReservationDiscardsSuccess`) is flipped to assert the
   fixed behaviour and passes — a holding runner's successful archive is
   recorded `complete` even after TTR loss / re-reservation; run under `-race`.
2. **No false "deleted":** a job whose command succeeded is broadcast/queryable
   as `complete`, never `deleted`, to a subscriber (new test).
3. **Fast startup:** starting on a large real-DB copy is responsive in ≤ a few
   seconds with no history scan (measure; no `seedStatusState`).
4. **Features intact:** tests that a subscriber still receives per-job updates +
   live RAM/CPU/STDOUT; reconnect/resync still catches up; Rerun/modify/
   suspend/resume still work; `wr add --sync` still returns on completion.
5. **DB compatibility:** open a DB previously upgraded by current code (a copy of
   the real `.tmp/db`) with the reworked build — incomplete jobs recover and
   run, complete jobs are queryable, no crash, no re-run of index rebuilds.
6. **Lost detection preserved:** a genuinely silent runner is still marked lost
   and its job re-run within ~1 TTR; an alive, on-time-touched job is never lost.
7. **No throughput/scale regression:** steady-state throughput ≥ current; a
   scale run at the churn-triggering concurrency (~6–7k runners) shows clean
   completion and responsive status (matching v0.36.5) — validate on the farm.

## Reserved (out of scope now; revisit only if needed)

- Idea 2 (concurrent reader / separate status listener) — optional future
  headroom only if the status stall still bites at extreme connection counts
  after this rework.
- Re-introducing accurate aggregate counts via a cheaper, reliability-safe
  mechanism (future, if the v0.36.5 count quality proves inadequate).

## Constraints

- Internal-only behaviour change **except** the deliberately-accepted regression
  of web-UI aggregate count accuracy to v0.36.5 quality (see
  `[[speedups-internal-only]]`).
- Do not break any KEEP feature; do not delete authoritative DB data; do not add
  a DB schema-version gate.
- Validate at the churn-triggering scale before shipping (the in-process oracle
  is necessary but not sufficient — see testing.md; farm fair-share permitting,
  or an in-process saturation harness).

## Notes (authoritative clarifications, verified this investigation)

- **Commit attribution:** `enqueueSubscriptionUpdate` ← `#503`;
  `emitLiveTouchSnapshot` ← `#534`/`#530`; `statusState`,
  `changeCallbackToState`, `seedStatusState` ← `#533`; strict
  `canCompleteFromQueueState` ← `#548`.
- **Feature/machinery independence (verified):** per-job subscriptions and live
  updates do not consume `statusState`; the code even notes subscriptions are
  "tracked separately… NOT covered by statusState". `emitJobTransition` runs the
  count update and the subscription delivery as two separable operations.
- **v0.36.5 reference behaviour (the target for the completion/lost path):**
  `SetTTRCallback` sets a `Lost` flag and returns `SubQueueRun` (parks the job,
  no release); `jtouch` clears `Lost` and resets TTR; `jarchive` accepts on
  `getij(checkRunning=true)` + `item.Stats().State==Run` + owner + `Exitcode==0`,
  with **no `job.State` check**. See `git show v0.36.5:jobqueue/serverCLI.go`
  and `:jobqueue/server.go` (`SetTTRCallback`).
- **Why this fixes all three symptoms:** `deleted` comes only from
  `changeCallbackToState` (removed); the startup stall only from `seedStatusState`
  (removed); discarded success only from the strict completion gate (reverted).
  The status stall returns to v0.36.5's acceptable single-reader profile once the
  per-transition count work is gone and touches are cheap.
- **DB upgrade is safe to work over:** additive `CreateBucketIfNotExists`, no
  in-DB version marker, no destructive `DeleteBucket`, indices rebuilt from
  authoritative job data; `Job` grew by 2 fields since v0.36.5 and the ugorji
  binc codec tolerates field diffs on decode. Rollback note (not a forward
  blocker): the reworked build won't maintain `repGroupCompleteCount`, so a
  later roll-**forward** to current code should run its `recompute-counts`
  repair to refresh `statusState`.
- The `queues_avoid` fix (`260720-1`) is separate and already landed — keep it.

## Notes — clarifications resolved during spec-writing (2026-07-20)

These decisions refine the requirements above and are authoritative where they
add detail. (Distinct from the investigation notes above.)

1. **Completion-acceptance model — pure v0.36.5 revert, no attempt-epoch.**
   Implement the completion path exactly as v0.36.5: accept a successful archive
   (`Exited && Exitcode==0`) from the client that still **owns** the reservation
   while the queue item is in `SubQueueRun` — no `job.State` gate and no new
   attempt-epoch `Job` field. Do **not** add the monotonic epoch discussed in
   `idea1.md`/`choice.md`; it is out of scope for this spec.
   - A pure revert (like both v0.36.5 and current code) still rejects an archive
     from a client that no longer owns the item, so the churn oracle
     (`reliable2_churn_test.go`,
     `TestReliable2DoubleReservationDiscardsSuccess`) must be **rewritten to the
     "an alive job is never re-reserved" model**, not merely flipped: force a TTR
     expiry while runner A is still alive and holding, assert the job stays parked
     in `SubQueueRun` with the `Lost` flag and that runner B **cannot** re-reserve
     it, and assert that A's later successful archive is **accepted and recorded
     `complete`** because A still owns the reservation. Run under `-race`. (The
     old "re-reserve, then A's late archive is discarded" script does not reflect
     the v0.36.5 contract; the real fix is that a live job is never re-reserved in
     the first place.)

2. **`deleted` vs `complete` — match v0.36.5 by deriving state from the job.**
   Remove the `changeCallbackToState` projection (as §2 requires), and replace its
   to-state role by **deriving the subscription/broadcast to-state directly from
   the job's actual state** at the point the update is emitted. v0.36.5 has no
   `changeCallbackToState` (it is `#533`), so "derive from the real job state" is
   the structure closest to v0.36.5.
   - Hard invariants: a job whose command succeeded is **always** reported
     `complete` and **never** `deleted` (acceptance #2); a genuine user
     delete/remove of an **incomplete** job is still reported `deleted`
     (v0.36.5-quality display, retained).
   - This reconciles the apparent tension between §2 ("delete
     `changeCallbackToState`") and KEEP (`#503` subscriptions): the projection
     function goes away, but the retained subscription layer still receives a
     correct to-state sourced from `job.State`.

3. **Web-UI counts — one slim absolute per-RepGroup counter; frontend unchanged.**
   Feed the web-UI status bars/counts from a single slim **absolute** per-RepGroup
   live counter that continues to emit the **existing `jstateAbsolute`** message,
   so `static/js/wr/websocket-handler.js` and the wire format stay **unchanged**.
   Do **not** revert to v0.36.5's `statusCaster`/`jstateCount` delta broadcasting.
   Startup seeding stays removed: the slim counter is maintained live from
   transitions and is **never** seeded by a history scan. Count accuracy may
   regress to v0.36.5 quality (flicker/overcount under high update rates) —
   accepted.
   - **Web-UI connect-seed:** on a browser (re)connect, push the counter's whole
     current in-memory map (do **not** replicate the removed `liveSeedLocked`
     terminal-only-hiding filter — that filter was `#533` machinery, so omitting it
     is ≤ v0.36.5 quality).
   - **CLI (revised):** `wr status -o counts` reverts to the heavy bucket+queue
     **scan** (accurate, v0.36.5 behaviour — satisfies the goal "`wr status` stays
     responsive, no worse than v0.36.5"). The slim counter is **web-UI only**; do
     **not** route the CLI fast-count path to it, because a never-seeded counter
     would under-report `complete` as `0` after a manager restart (strictly worse
     than v0.36.5). The heavy `wr status` path is unchanged.

4. **DB-compatibility test — committed small binary fixture.**
   Realize acceptance #5 with a **small binary fixture DB committed to the repo**
   (e.g. under `jobqueue/testdata/`), produced once by current (`reliable2`) code
   so it contains the now-dead buckets (`repGroupCompleteCount`,
   `repGroupCompleteBackfilled`) **and** the retained
   `endTimeToKey`/`repgroupEndTime` end-time index buckets (which back
   `wr status --recent` — keep maintaining them, not dead), jobs carrying
   the 2 post-v0.36.5 `Job` fields, populated indices, and a mix of complete and
   incomplete jobs. The test opens this fixture with the reworked build and
   asserts: incomplete jobs recover and run, complete jobs are queryable as
   `complete`, no crash, and the one-time index rebuilds do **not** re-run. Keep
   the fixture small (a handful of jobs), not the multi-million-job farm artifact.
   The spec must document how the fixture was generated so it can be regenerated.

5. **`wr manager recompute-counts` — removed.**
   Remove the `recompute-counts` subcommand and its backing
   `RecomputeRepGroupCompleteCounts`. This is a deliberate, accepted **user-facing
   removal** — the second sanctioned exception to "internal-only", alongside the
   count-accuracy regression. Its rollback/repair purpose belongs to the
   roll-forward target described in the rollback note, not to this build.

6. **Removed-machinery tests — deleted wholesale.**
   Delete the test files pinned to the removed machinery
   (`statusstate_invariant_test.go`, `repgroup_counter_test.go`,
   `nonblocking_startup_test.go`, `status_count_test.go`, `statusstate_test.go`,
   and the seeding tests in `server_startup_test.go`). Their guarantees (count
   idempotency, backfill correctness, non-blocking seeding) are abandoned together
   with the machinery they exercise; the new acceptance tests above replace the
   coverage that still matters.

7. **TTR/lost path — pure v0.36.5 `ttrCallback`; remove the `#550` F0 contact grace.**
   Implement the lost path exactly as v0.36.5: on TTR expiry set the `Lost` flag and
   **park the job in `SubQueueRun`** (no release, no re-reservation while alive); the
   next `touch` clears `Lost` and resets the TTR (recovery). **Remove** the `#550`
   F0 runner-contact grace entirely — `recordJobContact`, `contactedWithin`,
   `lastContact`, `forgetJobContacts`, and the `ttrCallback` `contactedWithin` guard
   (server.go:~3537). This is safe under this revert: because a `Lost` job is parked
   in `Run` and never re-reserved while its runner is alive, and a holding runner's
   successful archive is always accepted (note 1), a spuriously-set `Lost` flag under
   saturation is **benign** (a flag cleared by the next touch), not a cause of re-run
   or discarded success — the danger F0 addressed existed only in the now-removed
   strict-completion world.
   - **Test-list impact:** `TestLostDetectionRecentContactNotLost`
     (`jobqueue/lost_detection_test.go`) pins the F0 machinery and must be **deleted**
     as part of this F0 removal (in addition to the note-6 statusState-machinery
     deletions).
   - **Acceptance #6 re-reading:** "an alive, on-time-touched job is never lost" means
     a job whose `touch` arrives within the TTR keeps resetting its TTR and is never
     flagged; "lost detection preserved" means a genuinely silent runner is still
     flagged `Lost` and its job re-run within ~1 TTR via `confirmJobDeadAndKill`. Do
     **not** re-introduce a contact-based grace to satisfy #6.

8. **Prior-state recovery window — KEEP (independent reliability feature).**
   Removing "non-blocking startup" (§2) means removing **only** the `seedStatusState`
   work that the non-blocking wrapper existed to hide — **not** the broader background
   prior-state recovery. KEEP `recoverInBackground`, `isRecovering` /
   `finishRecovering`, the retryable `ErrRecovering` that `getij` returns to runners
   reconnecting mid-recovery (serverCLI.go:~1701), and the no-overcommit-during-
   recovery scheduling gate (`rescheduleReadyAfterRecovery`). These are an independent
   reliability feature (graceful retry for reconnecting runners) and belong on the
   KEEP list. Acceptance #5 ("incomplete jobs recover and run") is served by this
   retained recovery path.

9. **`db.putJobStats` robustness — in scope, minimal guard.**
   Add a minimal guard in `db.putJobStats` (db.go:~2026): **do not store a stat** when
   the job's `EndTime` is zero, or when the computed duration is non-positive
   (currently such a job stores `~MinInt64` seconds and corrupts future scheduler
   time-recommendations). No one-time repair of existing entries is required (guard
   only). Add a focused test. This is the one deliberately-included orthogonal fix
   beyond the revert.
