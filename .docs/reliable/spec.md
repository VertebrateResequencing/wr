# Fast, Reliable wr Manager at LSF Scale Specification

## Overview

The wr manager becomes unresponsive at LSF scale and gets stuck on `kill -9`
restart. `.docs/reliable/testing.md` attributes both symptoms to the `#547`
web-UI status seeding: `seedStatusStateForItemDefs` ->
`retrieveCompleteJobCountsByRepGroups` (db.go:902) cold-scans the 1.9M-entry
complete bucket on `add` and the startup/recovery path (190 s to add one
job to a big repgroup; 162 s of a 162 s restart). Baseline F0 (commit a19d390)
already fixed false-lost-under-load and must not regress.

This spec removes that scan and makes startup non-blocking, in four
independently mergeable phases (priority order):

1. **Idea 3** - a maintained, persisted per-repgroup COMPLETE-count counter,
   updated inside the existing BoltDB transactions at four mutation hooks so it
   equals the RAW scan by construction. Seeding reads it in O(live repgroups)
   point reads. One-off online background backfill + offline recompute for
   existing DBs; new DBs need no migration.
2. **Idea 2** - reorder `Serve()` so the manager answers clients before
   `loadPriorState`, which runs in a background goroutine behind a `recovering`
   flag, with recovery-window RPC safety and concurrency guards.
3. **Fix 1c** - `local.recover` enumerates processes once per recovery.
4. **Fix 1d** - open BoltDB with the map freelist; offline compaction
   subcommand.

**Non-goals (reserved):** Idea 4 full separate-process projector; Idea 5 full
hot/cold storage split / SQLite; Idea 1 async-seed (dropped). Fix 1f (separate
status listener) is DROPPED: the browser web UI already reads counts from
`statusState` over `/status_ws` (off the mangos socket), so once the counter
makes `statusState`'s complete count fast+correct, the web-UI status page is
already fast and decoupled. No new port, no new status endpoint. No expensive
automatic periodic full-verify of the counter: runtime drift is guarded by the
>= 0 clamp + anomaly log plus the on-demand offline recompute (A4).

## Architecture

### The two complete-count scans (do not confuse them)

- `retrieveCompleteJobCountsByRepGroups` (db.go:902) ->
  `rawCompleteJobCountByRepGroup` (db.go:926): **RAW** count = number of keys
  `K` with `(R,K)` in the RTK bucket AND `K` in the complete bucket. Does NOT
  exclude keys currently re-added to the live bucket. This is what seeding uses
  today and what the counter MUST equal. Keep this function (used by
  backfill/recompute and as test ground truth); only remove it from the
  operational seeding path.
- `retrieveCompleteJobStatusByRepGroup` (db.go:1677) ->
  `addCompleteJobStatusByRepGroup` (db.go:1686, live-excluding `continue` at
  db.go:1697): the CLI's live-EXCLUDING scan. Reached by `wr status`
  (cmd/status.go:1113 `GetByRepGroup` -> "getrs" -> `handleGetByRepGroup`
  serverCLI.go:1449 -> `getStatusByRepGroup` server.go:1060 ->
  `retrieveCompleteJobStatusByRepGroup` db.go:1677). This is GROUND-TRUTH for
  the CLI and stays **UNCHANGED**. Never point `wr status`
  at `statusState` or the counter.

They diverge only when a key is simultaneously in the complete bucket and
re-added to the live bucket: RAW counts it, live-excluding drops it. The counter
matches RAW, matching current seeding semantics (no web-UI regression).

### RTK key format (verified)

RTK keys are `<repGroup> + "_::_" + <jobKey>` (`generateLookupKey` db.go:1390;
`dbDelimiter = "_::_"` db.go:63). `jobKey` is `byteKey(...)`, a 128-bit FarmHash
hex string (job.go:1073, utils.go:145) with no delimiter, and repGroups contain
no delimiter (assumed by every existing RTK scan). So splitting an RTK key into
repGroup/jobKey at the LAST delimiter is safe, as `persistedRepGroupsForJobKey`
(persistedstatus.go:49-51) and `lookupEntryJobKey` (db.go:3116) already do.

### The four counter hooks (Idea 3) - verified sites

The RAW count for `R` changes at exactly these runtime primitives; hook each
INSIDE its existing bolt transaction. Clamp the stored count at >= 0 (a safety
net that must never trigger if the logic is correct; if the clamp ever fires,
i.e. a computed value < 0, log the anomaly so an admin can run the offline
recompute A4):

1. **RTK entry created** for `(R,K)`, entry did not already exist AND `K` in
   complete -> `counter[R]++`. Site: `putLookups` (db.go:2705, `Put` at
   db.go:2708), gated on `bytes.Equal(bucket, bucketRTK)`. Covers the add path
   (`storeNewJobData` db.go:1203 -> `storeLookups` db.go:2695, its own
   `db.bolt.Batch`) AND the modify path (`modifyLiveJobsTx` db.go:2264 ->
   `putAllLookups` db.go:2377). Pre-existence: `lookup.Get(doublet[0]) == nil`
   before the `Put`. This is the default `ignoreComplete=false` re-add / re-add
   under a new repgroup case.
2. **RTK entry deleted** for `(R,K)`, `K` in complete -> `counter[R]--`. Site:
   `deleteLookupEntriesForJobKey` (db.go:2402, `Delete` at db.go:2416) for each
   collected delete whose `d.bucket` equals `bucketRTK`. VERIFIED this is the
   ONLY runtime RTK-deletion site (sole caller `deleteOldLiveJobs` db.go:2298,
   from `modifyLiveJobsTx`, inside `db.bolt.Batch` db.go:2223); normal remove
   path `deleteLiveJobs` (db.go:1501) deletes only from the live bucket and
   explicitly does NOT touch RTK (comment db.go:1518-1519).
3. **Key added to the complete bucket** (archive), not already present -> for
   every `R` with `(R,K)` in RTK, `counter[R]++`. Site: `archiveJobTx`
   (db.go:807, complete `Put` db.go:818; VERIFIED the only production writer of
   `bucketJobsComplete`). Idempotent re-archive guard: capture
   `wasComplete := tx.Bucket(bucketJobsComplete).Get(key) != nil` BEFORE the Put
   and increment only when `!wasComplete` (covers the `#547`/`260708`
   archived-rerun path). Enumerate `R`s for `K` via the reverse index, reusing
   `persistedRepGroupsForJobKey` (persistedstatus.go:38; prefix
   `reverseLookupEntryPrefix(K)` over `bucketJobLookupEntries`, RTK entries
   only). `archiveJob` (db.go:1438) wraps this in `db.bolt.Batch` (db.go:1451).
4. **Key deleted from the complete bucket**: never happens (append-only) -> no
   hook.

**Batch-retry safety (critical):** `archiveJob`, `storeLookups` and
`modifyLiveJobs` use `db.bolt.Batch`, whose fn may run more than once. Every
counter mutation MUST be a read-modify-write of the counter bucket value WITHIN
the tx (read current, apply delta, clamp, `Put`) and MUST NOT touch any
in-memory mirror inside the tx, so a batch re-split cannot double-apply. The
in-memory `statusState` is maintained by the separate `emitJobTransition` path
(jobtransition.go), NOT by these hooks; the counter's only runtime consumer is
seeding.

### New persisted buckets

Create both via `CreateBucketIfNotExists` in `initDB` (db.go:515+):

    // repGroup -> 8-byte big-endian uint64 count
    bucketRepGroupComplete = []byte("repGroupCompleteCount")

    // repGroup -> nil marker; sentinel key "" -> nil = fully backfilled
    bucketRepGroupBackfilled = []byte("repGroupCompleteBackfilled")

### New / changed functions

Idea 3 (jobqueue/db.go, jobqueue/server.go):

    // O(len(repGroups)) point reads of bucketRepGroupComplete; 0 for absent.
    func (db *db) retrieveMaintainedCompleteCounts(
        repGroups []string,
    ) (map[string]int, error)

    // read-modify-write of bucketRepGroupComplete[repGroup], clamped >= 0
    // (log an anomaly if the clamp fires; see A4 offline recompute).
    func adjustRepGroupComplete(tx *bolt.Tx, repGroup string, delta int) error

    // split "<repGroup>_::_<jobKey>" at the last delimiter.
    func splitRTKKey(rtkKey []byte) (repGroup string, jobKey []byte, ok bool)

    // one-time online backfill: for each repGroup from retrieveRepGroups()
    // (db.go:1584) lacking a marker, in ONE bolt.Update tx SET counter[rg] =
    // rawCompleteJobCountByRepGroup(rg) computed in that same tx, then Put its
    // marker. Set the sentinel when all done. Idempotent, crash-resumable. Runs
    // in a background goroutine (see Idea 2). New DB (no repgroups) is a no-op.
    func (db *db) backfillRepGroupCompleteCounts(ctx context.Context) error

    // offline SET-all repair/migration: for every repGroup SET counter[rg] =
    // raw scan (+ marker). Returns the number of repGroups whose stored value
    // differed from the raw scan (drift), for logging. Idempotent.
    func (db *db) recomputeRepGroupCompleteCounts(
        ctx context.Context,
    ) (drift int, err error)

`seedStatusStateForItemDefs` (server.go:921): replace the server.go:936 call
`s.db.retrieveCompleteJobCountsByRepGroups(repGroups)` with
`s.db.retrieveMaintainedCompleteCounts(repGroups)`. Everything else in seeding
(`seedRepGroupComplete` statusstate.go:113, the `statusSeedMutex`, the
`unseededStatusRepGroups` filter) is unchanged.

Idea 2 (jobqueue/server.go, jobqueue/serverCLI.go):

    // NEW const; MUST NOT contain the ErrBadJob (server.go:82) or ErrBadRequest
    // (server.go:81) substrings, so client retry treats it as transient.
    ErrRecovering = "server is recovering prior state, please retry"

    // Server fields (guard with ssmutex, server.go:758):
    recovering       bool
    recoveryTotal    int
    recoveryRestored int
    rrjMu            sync.RWMutex // NEW: guards recoveredRunningJobs

    func (s *Server) isRecovering() bool
    func (s *Server) recoveryProgress() (restored, total int) // observe N/M
    func (s *Server) setRecovering(total int) // recovering=true, total set
    func (s *Server) noteRecovered(n int)     // restored += n (progress)
    func (s *Server) finishRecovering()       // recovering=false

    // NEW Server test hook (model on statusWSDetailsHook server.go:746); called
    // at the top of the background recovery goroutine so a test can block
    // recovery and observe the recovering window. nil in production.
    recoveryPauseHook func()

Fix 1c (jobqueue/scheduler/local.go): add a process-lister seam and a
per-recovery snapshot cache (Section C).

Fix 1d (jobqueue/db.go, cmd/manager.go): map-freelist on the 5 `bolt.Open`
calls (db.go:441, 449, 455, 480, 500); offline subcommands.

### Lock order (unchanged; new leaf only)

Preserve `queue.mutex -> job -> statusState.mu`. The counter lives in bolt txs
(no in-memory lock). `rrjMu` and the recovering-state fields are leaves; do not
take any new lock before `queue.mutex`.

### Quality gates (Makefile / go-conventions)

- `make test` (`go run ./cmd/wr-testsuite test`, CGO_ENABLED=0)
- `make race` (CGO_ENABLED=1)
- `make lint` (`golangci-lint run`); also `golangci-lint run --fix`
- New source files start with the copyright boilerplate (go-conventions).
- GoConvey `So(...)` assertions; tests guard with
  `if runnermode || servermode { return }` and use the `jobqueueTestInit` /
  `serve` (jobqueue_test.go:1396) / `Connect` harness, as
  `lost_detection_test.go` does.

---

## A. Maintained persisted per-repgroup COMPLETE counter (Idea 3)

### A1: Counter buckets and the four maintenance hooks

As the manager, I want the per-repgroup complete count maintained inside the
existing job-write transactions, so no operational path scans history and the
count equals the RAW scan by construction.

Implement `bucketRepGroupComplete`, `adjustRepGroupComplete`, `splitRTKKey`, and
the four hooks (Architecture). Clamp at >= 0.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/repgroup_counter_test.go`

**Acceptance tests** (ground truth = `retrieveCompleteJobCountsByRepGroups`, the
RAW scan; assert the maintained counter equals it after each step):

1. Given a fresh db, when a job with RepGroup "rgA" (key K) is stored via
   `storeNewJobs` (ignoreComplete=false) but not archived, then
   `retrieveMaintainedCompleteCounts(["rgA"])["rgA"]` == 0 == RAW scan.
2. Given that job, when it is archived via `archiveJob`, then counter["rgA"]
   == 1 == RAW scan.
3. Given K archived under "rgA", when a job {same Cmd/Cwd => key K, RepGroup
   "rgB"} is added via `storeNewJobs` (ignoreComplete=false), then counter ==
   {"rgA":1,"rgB":1} == RAW scan (cross-repgroup at add time; RTK now has
   (rgA,K) and (rgB,K), K still in complete).
4. Given step 3, when the live job for K is modified via `modifyLiveJobs`
   (oldKeys=[K], new job changes Cmd so its `Key()` becomes K' != K, RepGroup
   "rgB"), then counter == {"rgA":0,"rgB":0} == RAW scan (the RTK-delete hook
   drops both (rgA,K) and (rgB,K) since K in complete; (rgB,K') created but K'
   not in complete).
5. Given K archived under "rgA" (counter 1), when `archiveJob` is called again
   for the same key K (idempotent re-archive, K already complete), then
   counter["rgA"] stays 1 == RAW scan (the `wasComplete` guard).
6. Given K archived under "rgA", when K is re-added under "rgA" again (same
   repGroup, (rgA,K) already in RTK), then counter["rgA"] stays 1 == RAW scan
   (the pre-existence guard).
7. Given K archived under "rgA", when K is removed via `deleteLiveJobs`, then
   counter["rgA"] stays 1 == RAW scan (remove does not delete RTK).
8. Given a churn across >= 3 repgroups mixing add -> run -> complete -> remove
   -> re-add(new repgroup) -> modify(key-changing) -> bury for both single- and
   cross-repgroup keys, when it completes, then for every repgroup
   `retrieveMaintainedCompleteCounts` == RAW scan ==
   `recomputeRepGroupCompleteCounts` result (drift == 0).

### A2: Seeding reads the counter, not the scan

As the manager, I want startup and `add` seeding to read the counter in
O(live repgroups) point reads, so a single live job in a 700k-history repgroup
no longer blocks the operation for minutes. Change
`seedStatusStateForItemDefs` (server.go:921; scan call at server.go:936) to
call `retrieveMaintainedCompleteCounts`. No change to `wr status` / CLI
transport.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/repgroup_counter_test.go`

**Acceptance tests:**

1. Given a running server whose "rgBig" is not yet in `statusState`, when its
   `bucketRepGroupComplete["rgBig"]` is set to a sentinel value V that DIFFERS
   from the RAW scan (e.g. RAW would be 3, set V = 99), and a new job is then
   added to "rgBig", then `statusState.snapshot()["rgBig"][JobStateComplete]`
   == 99 (proves seeding read the counter, not the raw scan).
2. Given a restart (`serve`) of a db with a live job in "rgBig" whose counter
   is N, when the manager becomes responsive, then
   `statusState.snapshot()["rgBig"][JobStateComplete]` == N, seeded from the
   counter (no raw scan on the readiness path).
3. Regression D: `TestReliableCompletedRepGroupRemovedOnRefresh` stays green - a
   fresh status subscriber's seed still includes a complete-only repgroup (the
   counter feeds `seedRepGroupComplete`; `subscribe`/`liveSeedLocked` semantics
   unchanged).

### A3: One-time online background backfill

As an operator upgrading a large existing DB, I want the counter built in the
background after the manager is responsive, so upgrade never blocks startup and
is crash-resumable.

`backfillRepGroupCompleteCounts` runs in a background goroutine (started by
Serve after readiness; composes with Idea 2). Per-repgroup, in one tx: SET
counter[rg] = raw scan computed in that same tx, then write the marker. SET (not
additive) reconciles with concurrent runtime increments because bolt serialises
write txs: an archive committed before the backfill tx is in the raw scan; one
committed after increments on top. New DBs (no repgroups) are a no-op. During
this one-time window a not-yet-backfilled repgroup's web-UI complete count may
under-report; this is acceptable (the CLI ground-truth path is unaffected).

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/repgroup_counter_test.go`

**Acceptance tests:**

1. Given a db pre-populated with archived history but EMPTY counter/marker
   buckets (a pre-upgrade DB), when `backfillRepGroupCompleteCounts` runs, then
   every repgroup's counter == RAW scan and each has a marker.
2. Given a backfill interrupted after some but not all repgroups have markers,
   when it is re-run, then it processes only the unmarked repgroups and the
   final counters == RAW scan (idempotent, resumable).
3. Given concurrent archives into repgroup "rgC" while backfill processes it
   (drive archives and backfill together), when both finish, then counter["rgC"]
   == RAW scan (SET-in-same-tx reconciliation; run under `-race`).

### A4: Offline recompute/repair subcommand

As an operator, I want an offline command (manager stopped) that SETs all
counters from a full scan, as belt-and-braces repair and the migration tool.

Add `wr manager recompute-counts` (cmd/manager.go, wired like `managerBackupCmd`
cmd/manager.go:526). It refuses to run if the manager is up (pid file / port
check), opens the DB directly with the map-freelist option, calls
`recomputeRepGroupCompleteCounts`, logs the drift count, and closes.

**Package:** `cmd/`, `jobqueue/`
**File:** `cmd/manager.go`, `jobqueue/db.go`
**Test file:** `jobqueue/repgroup_counter_test.go`

**Acceptance tests:**

1. Given a db with correct counters, when `recomputeRepGroupCompleteCounts`
   runs, then drift == 0 and all counters are unchanged (idempotent no-op).
2. Given a db whose counters were deliberately corrupted (set to wrong values),
   when recompute runs, then every counter == RAW scan and drift == number of
   corrupted repgroups.
3. Given the subcommand invoked while a manager is running on that db, then it
   exits non-zero and does not modify the db.

### A5: Crash consistency

As an operator, I want counters to survive `kill -9` mid-completion, because
they are written in the same tx as the archive.

**Package:** `jobqueue/`
**Test file:** `jobqueue/repgroup_counter_test.go`

**Acceptance tests:**

1. Given a churn of archives via `archiveJob`, when the db is reopened without a
   clean counter shutdown (relying on the shared tx), then every counter == RAW
   scan and `recomputeRepGroupCompleteCounts` reports drift == 0.
2. Given the suite's in-process crash-recovery path, when a run is hard-stopped
   mid-completion and restarted, then no completion is double-counted or lost
   (counter == RAW scan). The suite has no separate in-process `kill -9`
   harness (`serve` runs the server in-process), so this crash-recovery path
   is A5 test 1's in-package close/reopen and the property rests on the
   shared archive+counter bolt tx; the true hard-crash exemplar - a
   `--servermode` server SIGKILLed and restarted with `--keepdb` - lives
   out-of-process in TestJobqueueSignal.

---

## B. Non-blocking startup and recovery-window RPC safety (Idea 2)

### B1: Reorder Serve() so recovery runs in the background

As an operator, I want the manager to answer ping/status/add within ~1 s of
start regardless of history/running-jobs, so a `kill -9` restart is never stuck.

Reorder `Serve()` (server.go:2229). Current order: `initDB` (2272) ->
`createQueue` -> `loadPriorState` (2391, BLOCKING) -> web interface (2405,
`<-ready` 2407) -> `persistToken` (2410) -> `serveClients` (2417). New order:
`createQueue` -> web interface + `persistToken` + `serveClients` -> then
`setRecovering(total)` and launch `loadPriorState` in a goroutine that
calls `recoveryPauseHook` (if set), performs recovery, updates progress via
`noteRecovered`, and calls `finishRecovering()` at the end. Web interface stays
before `serveClients` (preserving the existing relative ordering/behaviour;
only `loadPriorState` moves to the background - the ready-added callback that
recovery's enqueue relies on is set in `createQueue`, not `serveWebInterface`).
Recovery keeps the single-batch enqueue (`recoverPriorJobs`
-> `enqueueItems` server.go:2755); dependency ordering is preserved because
AddMany resolves deps within the one batch. `Serve` returns once clients are
served (recovery still running).

Lightweight progress reporting: expose recovering/`restored`/`total` (via
`manager status` and/or a "recovering: N/M restored" log line) so a slow
recovery is not mistaken for a hang.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/nonblocking_startup_test.go`

**Acceptance tests:**

1. Given a db recovered via `serve` with `recoveryPauseHook` set to block on a
   channel, when the hook is blocking (recovery not yet run), then within 2 s:
   `jq.Ping` succeeds; `manager status` responds and `isRecovering() == true`;
   and `jq.Add` of a new job succeeds and that job is reservable. Then release
   the hook.
2. Given a db with M incomplete jobs (>= 3; mix of ready/running/buried) saved
   by a prior server, when a fresh `serve` runs recovery to completion (hook
   released), then `isRecovering()` becomes false and the queue + snapshot equal
   the pre-restart ground truth: exact job count M, `lost == 0`, no duplicated
   keys.
3. Given a client hammering Add/Reserve/status throughout recovery, when
   recovery completes, then final accounting is exact (no lost/dup jobs) and the
   test is `-race` clean.
4. Given `serve` with `recoveryPauseHook` blocking and M jobs to recover, when
   paused at the hook, then `recoveryProgress()` reports `total == M` and
   `restored == 0` (progress observable via `manager status`); after the hook
   is released and `isRecovering()` becomes false, `recoveryProgress()`
   reports `restored == total == M`, and `restored` sampled across the run is
   monotonically non-decreasing and never exceeds `total`.

### B2: Recovery-window RPC safety

As a pre-crash runner reconnecting during the recovery window, I want my
touch/archive for a not-yet-restored job to be retried, not permanently failed,
so recovery timing never causes a new false loss, and a genuine successful
archive in the window is still recorded.

In `getij` (serverCLI.go:1689), when `s.q.Get(key)` returns an error (item not
in queue) AND `s.isRecovering()`, return `ErrRecovering` instead of `ErrBadJob`.
Leave the wrong-sub-queue branch (checkRunning, serverCLI.go:1696) as
`ErrBadJob` (a real state error, not a recovery-timing miss). The client
retries all
non-ErrBadJob/ErrBadRequest errors: `handleFinalStateError` (client.go:2118)
gives up only when the error string contains `ErrBadJob` or `ErrBadRequest`
(client.go:2123), else sleeps `retryWait` and retries within `retryTime`
(`reportFinalState` client.go:2084). `handleTouch` (serverCLI.go:905) already
calls `recordJobContact` first (serverCLI.go:910), so a recovering-window touch
still records contact.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/server.go`
**Test file:** `jobqueue/nonblocking_startup_test.go`

**Acceptance tests:**

1. Given `serve` with recovery blocked by `recoveryPauseHook`, and a job key
   that recovery WILL restore as running (reserved by client C), when C archives
   that key during the window, then the response error is `ErrRecovering`, and
   `strings.Contains(err, ErrBadJob) == false` and
   `strings.Contains(err, ErrBadRequest) == false`.
2. Given the same setup, when the hook is released so recovery restores the job
   and C retries the archive (as `reportFinalState` would), then the archive
   succeeds, the job ends `complete`, and its repgroup counter is incremented
   exactly once (== RAW scan).
3. Given a recovering-window touch for a not-yet-restored running key, when it
   is issued, then the response is `ErrRecovering` (retryable) and
   `recordJobContact` recorded the contact.

### B3: Concurrency guards

As a maintainer, I want the races the trial flagged closed.

Guard `recoveredRunningJobs` with `rrjMu` at both sites: write in
`recoverRunningJob` (server.go:2815) and read in `confirmOrReleaseLostJob`
(server.go:3297). Ensure a client re-adding an identical job that is also being
recovered cannot yield a wrong final state (AddMany dedups by key only):
recovery and add must converge to one queue item per key with a consistent state
and no double-run/double-count.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/nonblocking_startup_test.go`

**Acceptance tests:**

1. Given recovery in progress and a client that re-adds (same Cmd/Cwd => same
   key) a job that is also being recovered, when both complete, then the queue
   holds exactly one item for that key, the job is not run twice, and the
   repgroup counter == RAW scan.
2. Given the full B1-B3 suite run under `-race`, then there are no data races
   (in particular around `recoveredRunningJobs`).

---

## C. Local-scheduler recover-once (Fix 1c)

### C1: Enumerate processes once per recovery

As a local/dev operator, I want `local.recover` to enumerate processes once per
recovery instead of once per running job, so a `kill -9` restart with thousands
of running jobs does not take minutes (testing.md S2: 10k running -> 175 s).

`local.recover` (scheduler/local.go:581) calls `process.Processes()`
(scheduler/local.go:582) on every call, and it is called once per running job
(`recoverRunningJob` server.go:2810 -> `Scheduler.Recover` scheduler.go:428 ->
`local.recover`). Introduce a process-lister seam on the local struct
(scheduler/local.go:152): `processLister func() ([]*process.Process, error)`
defaulting to `process.Processes`. Cache a single enumeration for the duration
of one recovery pass (reuse across `recover` calls; invalidate when
the pass ends / after a short freshness window), so N running jobs cause 1
enumeration. Keep tracking each still-alive matching pid via `recoverPid`
(local.go:617) / `recoveredPids` (local.go:173). LSF unaffected: `lsf.recover`
(lsf.go:1026) is a no-op and does no enumeration.

**Package:** `jobqueue/scheduler/`
**File:** `jobqueue/scheduler/local.go`
**Test file:** `jobqueue/scheduler/recover_test.go`

**Acceptance tests:**

1. Given a local scheduler whose `processLister` is a test double counting
   invocations and returning a fixed process set, when `Recover` is called for
   50 distinct running cmds in one recovery pass, then the lister was invoked
   exactly once and every matching alive pid is tracked (resources added /
   present in `recoveredPids`).
2. Given the LSF scheduler, when `Recover` is called 50 times, then no process
   enumeration occurs (its `recover` is a no-op) and it returns nil.
3. Given two matching processes for one cmd, when `Recover` runs, then only one
   pid is tracked per cmd (existing `recoverPid` de-dup preserved).

---

## D. Map freelist and offline compaction (Fix 1d)

### D1: Open BoltDB with the map freelist

As an operator, I want BoltDB opened with the map freelist on all manager opens,
to bound the freelist-load component of `initDB` on the large churned DB
(testing.md S4).

Pass `&bolt.Options{FreelistType: bolt.FreelistMapType}` (preserving existing
option fields) to all five production `bolt.Open` calls in `initDB`: db.go:441,
449, 455, 480, 500. No startup or online compaction.

**Validation (record, not assert):** measure `initDB` open time with vs without
the map freelist against a copy of the real `.tmp/db`; the benefit shows only at
real fragmentation, not synthetic scale (prompt.md Notes).

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/db_test.go`

**Acceptance tests:**

1. Given a fresh db opened by `initDB` (map freelist) and an existing db
   reopened by `initDB`, then both open without error and existing db tests pass
   (behaviour unchanged; the option only affects freelist representation).
2. Given a db written and reopened, when jobs are stored and archived, then
   reads return the same data as before the change (round-trip preserved).

### D2: Offline compaction subcommand

As an operator, I want to compact the DB while the manager is stopped, because
`bolt.Compact` copies the whole DB (~2x disk) and cannot run cleanly online.

Add `wr manager compact` (cmd/manager.go, wired like `managerBackupCmd`). It
refuses to run if the manager is up (pid/port check), compacts the DB file
to a temp file via `bolt.Compact` (source opened with the map freelist), then
atomically replaces the original, and reports before/after sizes.

**Package:** `cmd/`, `jobqueue/`
**File:** `cmd/manager.go`, `jobqueue/db.go`
**Test file:** `jobqueue/db_test.go`

**Acceptance tests:**

1. Given a stopped-manager db file with churn (free pages), when compaction
   runs, then it produces a valid db whose buckets and every
   job/lookup/counter round-trip identically to the original, and the output
   file size is <= the input.
2. Given compaction invoked while a manager is running on that db, then it exits
   non-zero and leaves the db untouched.

---

## E. Regression guards (must stay green)

As a maintainer, I want the F0 baseline and `#547`/`#548` fixes preserved. These
existing tests must remain green after every phase (re-run after each):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner`,
  `TestLostDetectionRecentContactNotLost` (E / F0).
- `.docs/reliable/harness/reliable_repro_test.go` (dropped into `jobqueue/`;
  `go test -run TestReliable ./jobqueue`): `TestReliableFalseLostRerun` (C),
  `TestReliableCompletedRepGroupRemovedOnRefresh` (D).
- `.docs/reliable/harness/reliable_cause_test.go`:
  `TestReliableFalseLostUnderSaturation` (E stress guard; run from `harness/`,
  not committed - throughput-sensitive under `-race`).

Preserved behaviour (by construction, no code change): the `#533` absolute-state
web-UI semantics (idempotent last-write-wins per-repgroup counts,
`jstateAbsolute`) stay intact because seeding still feeds the same value it does
today - the `counter == RAW scan == current seeding value` invariant
(Architecture) means the absolute complete count the web UI receives is
unchanged.

**Acceptance tests:**

1. After each phase, `TestReliableFalseLostRerun`,
   `TestReliableCompletedRepGroupRemovedOnRefresh`,
   `TestLostDetectionSilentRunner`, `TestLostDetectionRecentContactNotLost` all
   PASS.
2. After each phase, `TestReliableFalseLostUnderSaturation` (run from harness)
   still PASSes (`everLost == 0`).
3. `make test`, `make race`, `make lint` all clean.

### Harness thresholds (record numbers; not asserts) - `.docs/reliable/harness/`

- **B (startup):** `exp_startup_ab.sh` (10k running) and `exp_realdb_seed.sh`
  (real DB copy) restart responsive in <= a few seconds regardless of
  history/running-jobs; cold `add` to a big-history repgroup < 100 ms (was
  190 s).
- **F (responsive):** `exp_reconnect.sh` / `exp_status_load2.sh` - `wr status`
  bounded (~ baseline) independent of connected-runner count.
- **G (throughput):** `exp1.sh` / `exp_drive_ab.sh` throughput not regressed
  (HEAD >= v0.36.5).

Traceability: prompt.md acceptance criterion (c) (status endpoint stays
responsive under heavy runner load) is covered by harness threshold F above
(record numbers, not a deterministic Go test) - Fix 1f is dropped and the web
UI already reads counts over `/status_ws`, off the mangos socket.

---

## Implementation Order

Each phase is independently reviewable/mergeable; re-run Section E after each.

1. **Phase 1 - Idea 3 counter (Section A).** A1 (buckets + four hooks) and A2
   (seeding swap) are the core and must land together (seeding depends on the
   counter). Then A3 (online backfill), A4 (offline recompute subcommand), A5
   (crash-consistency tests). Highest value; removes the O(history) scan from
   add and startup.
2. **Phase 2 - Idea 2 non-blocking startup (Section B).** B1 (reorder +
   recovering flag + progress), B2 (recovery-window RPC safety), B3 (concurrency
   guards). Builds on Phase 1 (cheap recovery makes single-batch background
   recovery fast) and reuses `backfillRepGroupCompleteCounts` as a background
   task.
3. **Phase 3 - Fix 1c local recover-once (Section C).** Independent; scheduler
   package only.
4. **Phase 4 - Fix 1d map freelist + offline compaction (Section D).** D1
   (freelist option) is a one-line-per-site change; D2 (compaction subcommand)
   pairs with A4's offline-subcommand plumbing.

Phases 3 and 4 are independent of 1-2 and of each other; they may be done in
parallel with or after Phases 1-2.

---

## Appendix: Key Decisions

- **Counter target = the RAW scan** (`retrieveCompleteJobCountsByRepGroups`
  db.go:902), never the CLI's live-excluding scan (db.go:1677). This makes the
  counter a drop-in cheap replacement for the value seeding already used, so no
  seed value changes - only its cost. All A-section tests assert against the RAW
  scan.
- **Correct by construction, not repair-later.** The manager runs for
  weeks/months and offline recompute needs downtime, so the four tx-scoped hooks
  keep the counter equal to the RAW scan at runtime. The offline recompute (A4)
  and online backfill (A3) exist for migration and belt-and-braces repair; the
  >= 0 clamp is a safety net that must never trigger.
- **Batch-retry safety** dictates counter mutations be tx-only read-modify-write
  with no in-memory mirror inside the tx (Architecture). The in-memory
  `statusState` stays maintained by the separate `emitJobTransition` chokepoint
  (jobtransition.go), which already handles cross-repgroup completion at runtime
  via `job.statusCompleteRepGroups`; the counter's only runtime consumer is
  seeding.
- **SET, not additive, for backfill/recompute.** Per-repgroup SET in the same tx
  as the raw scan reconciles with concurrent runtime increments because bolt
  serialises write txs; a giant whole-DB overwrite is explicitly avoided.
  Per-repgroup markers make it crash-resumable and idempotent.
- **CLI `wr status` stays ground-truth and unchanged** (Architecture). No new
  port, no status endpoint; Fix 1f is dropped because the browser UI already
  reads `statusState` over `/status_ws`.
- **Recovery-window safety via a retryable error.** `ErrRecovering` is chosen
  over "restore-early" because it is simple, centralised in `getij`, and safe:
  the client already retries any non-ErrBadJob/ErrBadRequest error
  (client.go:2123) within `retryTime`, so a legit archive in the window is
  recorded once recovery restores the job. The wrong-sub-queue branch stays
  `ErrBadJob` (a real error, not a timing miss).
- **Single-batch background recovery is acceptable** because Phase 1 makes
  recovery cheap (seconds, no history scan). Chunked/streaming/dependency-aware
  progressive recovery is out of scope; dependency ordering is preserved within
  the one AddMany batch. Progress reporting prevents mistaking a slow
  recovery for a hang.
- **Fix 1c** caches one process enumeration per recovery pass behind a lister
  seam; LSF's no-op `recover` is unaffected. **Fix 1d** applies the map freelist
  to all 5 `bolt.Open` calls and ships compaction offline only (online/startup
  compaction would re-block readiness and needs ~2x disk on the 6.2 GB NFS DB).
- **Testing strategy:** deterministic in-package GoConvey tests using the
  existing `serve`/`Connect` harness and the RAW scan as ground truth; a Server
  `recoveryPauseHook` (modelled on `statusWSDetailsHook`) makes the
  recovering window observable without timing flakiness; a `processLister` seam
  makes the recover-once count assertable. See go-implementor and go-reviewer
  skills for the TDD loop and review gates.
