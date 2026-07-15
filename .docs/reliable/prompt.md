# Spec input: make the wr manager fast and reliable at LSF scale, with the web UI unable to affect operations

This is the input to the **spec-writer** workflow. It describes the desired
solution, distilled from the investigation in `.docs/reliable/` (see `testing.md`
for the reproductions/measurements and `idea1.md`–`idea5.md` for the trialled
options and their measured results). Implement as TDD against the acceptance tests
below.

## Goal & priority (non-negotiable)

The **actual running of jobs must be fast and reliable** on an LSF cluster with
tens of thousands of jobs, thousands running at once, and the manager DB on NFS.
**The web-UI status machinery must never be able to slow or block operations.**
Web-UI accuracy is secondary and may lag briefly; it must never sit on an
operational critical path (`add`, `reserve`, `touch`, `archive`, startup-readiness).

## What is already done (baseline = reliable branch HEAD)

- **F0 (commit a19d390) — DONE, do not regress.** Fixed false-lost-under-load:
  touch arrival is recorded before contended processing (`recordJobContact`),
  `ttrCallback` won't mark a running job Lost if it was `contactedWithin(ItemTTR)`,
  per-touch web-UI work is gated behind `hasAnyClientSubscriptions()`, and
  genuinely-silent runners are still marked Lost within ~1 TTR. Guarded by
  `jobqueue/lost_detection_test.go` + the independent stress test
  `.docs/reliable/harness/reliable_cause_test.go`.
- `#547`/`#548` already fixed the false-lost *consequence* (late archive accepted)
  and the "removed-on-refresh" web-UI seed. Those must stay fixed.

## The desired solution (a combination; implement in this priority order)

Each was trialled with a working spike; the measured result is in the cited
`ideaN.md`. This is a combination because no single idea covers everything, and
the trials showed a clear, evidence-backed stack.

### 1. Maintained persisted per-repgroup completion counters (Idea 3) — PRIMARY, highest value
Remove the O(history) `statusState` seeding scan
(`seedStatusStateForItemDefs` → `retrieveCompleteJobCountsByRepGroups`) from BOTH
the `add` path and the startup/recovery path — it is the root cause of the reported
**190 s cold `add`** and **162 s stuck restart** (testing.md S3, S8-context).
- Maintain a persisted per-repgroup "complete" count, incremented **inside
  `archiveJob`'s existing BoltDB transaction** (so it is crash-consistent — trial
  proved `mismatches=0` after a kill-9 mid-completion).
- Seeding becomes O(live repgroups) point reads; the cold scan never runs on an
  operational path again (trial: 64 ms → **0 ms** on both add and restart).
- **Must** increment for **every** repgroup a completed key belongs to (not just
  `job.RepGroup`) — cross-repgroup keys otherwise under-count (the trial spike only
  did `job.RepGroup`; this is the known gap to close). Reproduce the exact
  semantics of the current scan (distinct completed keys, incl. the `#547`/`260708`
  archived-rerun path — guard against double-count on idempotent re-archive).
- Provide a one-off **offline recompute** at DB upgrade to build the counter from
  existing history, plus a verify/drift check. New DBs need no migration.
- Complete-only scope is sufficient (all other states are live, recovered from the
  live bucket). See `idea3.md` "Trial results".

### 2. Non-blocking startup (Idea 2)
Reorder `Serve()` so `persistToken` + `serveClients` + the web interface come
BEFORE `loadPriorState`, which runs in a background goroutine (with a `recovering`
flag). Trial: restart time-to-responsive **12.6 s → 0.34 s** on NFS, independent of
recovery cost — the strongest structural guarantee a `kill -9` restart is never
"stuck", for any future cause too.
- With Idea 3, background recovery is cheap, so the minimal reorder is likely
  enough. Handle the concurrency the trial flagged: guard `recoveredRunningJobs`
  with a lock; ensure a client re-adding a job that is also being recovered cannot
  produce a wrong final state (AddMany dedups by key only); keep dependency
  ordering correct. (Optional, if reserve-ramp matters: chunk the recovery
  decode+enqueue so recovered jobs become reservable progressively rather than all
  at the end — the minimal reorder does not stream.) See `idea2.md` "Trial results".

### 3. Serve status off the runner-traffic path (Fix 1f + "Idea-4-lite")
Keep `wr status` and the web UI from competing with runner RPCs on the single
`RecvMsg` mangos socket (testing.md S6):
- Make `wr status` / the web UI use the cheap `statusState`/counts path, not the
  heavy `GetByRepGroup`→`AllItems()` path, by default.
- Serve status from a **separate listener/goroutine** that reads only `statusState`
  (a leaf lock) — trial proved it stays **flat ~0.5 ms under 7000 connections /
  195% CPU** while the mangos path doubles. This is ~70 LOC in-process; the **full
  separate-process projector (Idea 4) is NOT warranted** (the browser UI is already
  off the mangos socket; F0 already gated the expensive shared work). See
  `idea4.md` "Trial results".

### 4. Local-scheduler recovery fix (Fix 1c)
`local.recover` calls `process.Processes()` (full `/proc` scan) **per running job**
(testing.md S2: 10k running → 175 s recovery). Enumerate processes **once** per
recovery. LSF is unaffected (its `Recover` is a no-op), but this fixes the
`kill -9` loop for local/dev deployments.

### 5. Cheaper `initDB` (Fix 1d)
Open BoltDB with `Options{FreelistType: FreelistMapType}` on the 5 `bolt.Open`
calls, and add periodic/offline `bolt.Compact`. Bounds the freelist-load component
of `initDB` (testing.md S4: ~1 s NFS / up to 12.6 s cold-local on the real 6.2 GB
DB) and the archive-decay component (S5). ~a day's work, near-zero risk. **Validate
the map-freelist benefit against the real `.tmp/db`, not synthetic scale** (it only
shows at real fragmentation). See `idea5.md` "Trial results".

## Reserved (out of scope now; revisit only if needed after 1–5 land)

- **Idea 5 full hot/cold storage split** (or SQLite): large/multi-week, high blast
  radius (archive & dependency read paths). After 1–5, the residual is *efficiency*
  (~1 s NFS `initDB`, gradual archive taper), not the reported reliability
  failures. Revisit only if the real DB still shows a biting `initDB`/decay on NFS.
- **Idea 4 full separate-process projector:** the maximal "web UI can never touch
  ops, ever" guarantee; keep in reserve for future web-UI expansion.
- **Idea 1 async-seed: dropped** — it only *defers* the scan (with a double-count
  risk); Idea 3 *removes* it. (Its Fix 1c/1d/1f parts are folded in above.)

## Acceptance criteria (TDD targets — see `testing.md` "Acceptance criteria")

Deterministic Go tests (must all be green; the first three already pass on the F0
baseline and must stay green):
- `TestReliableFalseLostRerun`, `TestReliableCompletedRepGroupRemovedOnRefresh`
  (C/D — #548), `TestLostDetectionSilentRunner`, `TestLostDetectionRecentContactNotLost`
  (E — F0), and the independent stress guard
  `harness/reliable_cause_test.go::TestReliableFalseLostUnderSaturation`.
- **New tests required by this work:** (a) counter equals a fresh recompute ==
  ground truth after add→run→complete→remove→re-add churn, **including
  cross-repgroup keys** and the archived-rerun path, and stays consistent after a
  `kill -9` mid-completion; (b) startup is responsive (status/ping/add answer) while
  recovery is still in progress, and final job accounting is exact (no lost/dup);
  (c) the status endpoint stays responsive while the manager is under heavy runner
  load.

Harness thresholds (record numbers; `.docs/reliable/harness/`):
- **B:** `exp_startup_ab.sh` (10k running) and `exp_realdb_seed.sh` (real DB copy)
  restart responsive in **≤ a few seconds** regardless of history/running-jobs;
  cold `add` to a big-history repgroup **< 100 ms** (was 190 s).
- **F:** `exp_reconnect.sh`/`exp_status_load2.sh` — `wr status` bounded (≈ baseline)
  independent of connected-runner count.
- **G:** `exp1.sh`/`exp_drive_ab.sh` throughput **not regressed** (HEAD ≥ v0.36.5).

## Constraints

- Web-UI/status/counter work must add only O(1)-ish cost to operational paths;
  nothing on `add`/`reserve`/`touch`/`archive`/startup-readiness may scan history
  or block on a slow client.
- Correctness: counts must match ground truth (incl. cross-repgroup + crash
  consistency); preserve `#533` absolute-state web-UI semantics and the
  `#547`/`#548` behaviours; respect lock order (queue.mutex → job → statusState.mu;
  new leaf locks only).
- Gates: `make lint`, `make test`, `make race` clean.
- Keep the independent harness tests in `.docs/reliable/harness/` as regression
  guards for F0 and the reproductions; the spec's own tests live in-package.

## Notes (clarifications resolved)

- **Scope / packaging (Q1):** Deliver ONE spec covering the full recommended
  stack — Idea 3 (counters), Idea 2 (non-blocking startup), Fix 1f (decoupled
  status), Fix 1c (local-scheduler recover-once), Fix 1d (map freelist +
  compaction) — organised as separate implementation **phases in that priority
  order**, each independently reviewable/mergeable. F0 (a19d390) is the done
  baseline. Idea 4-full (separate-process projector) and Idea 5-full (hot/cold
  storage split / SQLite) are explicitly **out of scope** (reserved for
  future/extreme scale).
- **Counter recompute / migration (Q2):** The one-off recompute for existing DBs
  must **not block startup** — run it as a background task after the manager is
  responsive (consistent with Idea 2). Operations start immediately; the web-UI
  "complete" counts may under-report only during that one-time post-upgrade
  window. The recompute must be crash-safe / re-runnable and must reconcile
  correctly with archive increments happening concurrently (compute from a
  consistent read-tx snapshot and add to the counter, rather than overwrite).
  Runtime drift is prevented by construction: the counter is incremented in the
  **same bolt transaction** as the archive write. Do **not** add an expensive
  automatic periodic full-verify — the recompute is the repair tool (re-runnable);
  log any detected anomaly for an admin-triggered recompute.
- **Recovery mechanism (Q3):** Idea 2 uses the **minimal reorder** (respond first;
  `loadPriorState` in a background goroutine). Recovered jobs becoming reservable
  in a single batch at the end of background recovery is acceptable **because
  Idea 3 makes recovery cheap** (seconds, no history scan). Chunked/streaming/
  dependency-aware progressive recovery is **out of scope** (revisit only if
  recovery is ever slow). **Do** include lightweight recovery **progress
  reporting** (manager status / log shows "recovering: N/M restored") so a slow
  recovery is never mistaken for "hung". Guard the concurrency the trial flagged:
  protect `recoveredRunningJobs`; ensure a client re-adding a job that is also
  being recovered cannot yield a wrong final state; preserve dependency ordering.
- **Status listener transport / user-facing behaviour (Q4/Q5) — Option B (no new
  port, no user-facing change):** Do NOT change any `wr status` output modes or
  defaults — `-o details` stays exactly as it is on the existing path. The manager
  keeps its **2 ports** (mangos client/runner port + web/REST port); **no 3rd
  port**. Decouple only by serving the cheap counts/summary + the web-UI status
  from `statusState` via the **existing web/REST port** (a counts/summary endpoint
  the web UI uses, and that cheap CLI invocations can use), so the web-UI status
  page is fully isolated from runner traffic. The default `wr status -o details`
  CLI path remains on the mangos socket unchanged — an acceptable residual
  (occasional human command; mild ~2× degradation under extreme load after F0). A
  dedicated second read-socket (Option C, which would need a 3rd port) is out of
  scope.
