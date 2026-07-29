# Reliable4 prod-freeze — reproducers built + fix plan (2026-07-29)

Companion to `prod-freeze-pprof-diagnosis.md` (the root-cause write-up). This doc
records the REPRODUCERS that were built and confirmed for that diagnosis, and the
per-fix routing (which go via `/bugfix`, which are documented follow-ups). Read
the diagnosis first for the mechanism; this doc is the "how we reproduce it and
how we fix it" layer.

## TL;DR

The freeze's engine — an **unbounded `go db.bolt.Batch` per job-state change**
(`launchJobChangeUpdate`/`launchJobExitUpdate`) — is reproduced two ways, both
CONFIRMED:

- **Fast, deterministic, main-suite (RED now):**
  `jobqueue/reliable4_writestorm_test.go` —
  `TestReliable4WriteStormGoroutineExplosion` + `…ExitExplosion`. A 5,000-job
  state-change burst holds **5,006** concurrent DB-write goroutines (bound 512).
  0.4s each. Flips GREEN under the bounded single-writer fix (verified with a
  throwaway spike: added 5006 → 2).
- **Faithful scale (wrdev.sh):** `developers/wrdev.sh unsuspend-burst` — a single
  `wr resume` of 60k limit-blocked jobs on a big freelist DB made `bw` (goroutines
  blocked in `bbolt.(*DB).Batch`) explode to **57,053** (prod measured 114,459),
  draining over 32s. This is the authoritative pre/post-fix A/B gate.

## The reproducers

### A. Fast in-process (the TDD RED test) — `jobqueue/reliable4_writestorm_test.go`

Plain main-suite tests (no build tag), so `make test` runs them.

```
go test -run 'TestReliable4WriteStorm' ./jobqueue/ -v -count=1
```

How it works: open a backups-off db via the real `initDB`; seed N live jobs;
**hold the single bbolt write transaction open** so every best-effort commit
blocks in `beginRWTx` (standing in for prod's slow freelist/spill commits that let
the goroutines pile up); fire the N-job change (or exit) burst from a helper
goroutine; sample the PEAK goroutine count; then roll back and drain. The burst
runs concurrently and the lock is released once the count settles, so a fix that
applies backpressure cannot hang the test — an enqueue adds DATA, not goroutines,
so any bounded design measures O(1) added goroutines.

- **Current code (RED):** `added ≈ 5006` (one goroutine per change, all stuck in
  `db.bolt.Batch`).
- **After Fix 1 (GREEN):** `added ≤ 512`.
- Also asserts **latest-wins persistence** (reads the live bucket back via
  `recoverIncompleteJobs`), so a "fix" that bounds goroutines by dropping writes
  fails too.

### B. Faithful scale — `developers/wrdev.sh unsuspend-burst [jobs] [pprofPort]`

```
WRDEV_ROOT=/nfs/hgi/wr/<roomy-dir> WRDEV_PRISTINE_DB=/nfs/hgi/wr/sb10-bigdb/prod.db \
  developers/wrdev.sh unsuspend-burst 100000 6062
```

Stages N jobs in ONE limit group set to **0** (`canIncrement` = `current < limit`
= `0 < 0` = false, so they are ready-but-blocked and **never run** — zero LSF
load, farm-safe), on an isolated PROD-mode manager (backups + `WR_PPROF_ADDR` on)
opened on a COPY of a big freelist-bloated DB. Mass-suspends to stage, then the
BURST: a single `wr resume` un-suspends all N at once. An embedded goroutine
classifier (the prod `_capture_load.sh` logic) reports the freeze signature every
3s. **VERDICT** asserts on peak `bw`.

Confirmed run (pristine6, 7GB, N=60000): `bw` → **57,053**, drained over 32s.

Two things that run learned:
- **Sustained >60s freeze needs the real prod.db.** pristine6's synthetic freelist
  commits fast enough that 57k goroutines drained in 32s with `bwmax=0` and status
  RPC < 1.4s. The 7.9GB churn-bloated `prod.db` (CPU-bound `freelist.Free`/`spill`)
  is what keeps the committer stuck > 60s. The **bw explosion is the
  DB-independent primary signature** and the clean A/B gate; the >60s freeze is the
  amplified consequence. NB `prod.db` recovers its own ~118k live jobs on startup —
  use `-s local` (the mode already sets this); those recovered jobs make the burst
  even more faithful.
- **Disk:** put `WRDEV_ROOT` on a roomy FS (needs ~2× the DB for copy+backup).
  Goroutine dumps are classified-then-deleted so they can't fill the disk.

## Fix routing

| # | Fix | Reproducer | Route |
|---|-----|-----------|-------|
| 1 | **Bounded, coalescing, dedup-by-key single writer** for best-effort change/exit updates | A (RED) + B (gate) | **/bugfix** |
| 2 | Keep the synchronous archive path off the best-effort lane | falls out of Fix 1; B shows it | with Fix 1 |
| 3 | bbolt `NoFreelistSync=true` at open | — | TRIED, **DROPPED** (breaks C2 startup invariant; marginal after Fix 1) |
| 4b | Recovery-gate `jsuspend`/`jresume`/`getsetlg` | — (design decision) | documented follow-up |
| 5 | Confirm-dead SSH leak → host-grouped open/check/close coordinator | END-TO-END on LSF here (wrdev.sh) + a fast unit test via a `Host.Close()`/batch seam | **/bugfix** |

Fix 4a (redundant re-suspend) and 4c (limit not held) from the diagnosis are NOT
separate bugs: re-suspend/re-resume no-ops are already guarded at the queue layer
(`queue.Suspend`→`ErrNotSuspendable` before any DB write), and the limit IS held
for the *running* count — mass-resume floods only the uncapped *ready* count,
which is exactly the write storm that Fix 1 addresses.

## Fix 1 — implementation constraints (learned from a throwaway spike)

The spike (single writer goroutine draining a channel) flipped the fast test
GREEN, confirming the shape works. It also surfaced the traps a naive fix hits —
the implementer MUST handle these:

1. **Coalesce for real: fold ALL currently-pending ops into ONE `db.Update`.** A
   single writer that calls `db.bolt.Batch` once per op pays `MaxBatchDelay`
   (10ms) EACH time (bbolt only coalesces *concurrent* callers), so 5,000 ops took
   ~50s. The writer must drain everything queued and apply it in one write tx.
   This is also what kills the tiny-txn collapse (prod's ~3,004 fsync'd txns).
2. **Dedup by `Job.Key()` with latest-wins.** Hold the latest encoded value per
   key; each churning job is written once per drain cycle, not N times.
3. **Preserve the archive-vs-change guard** (`db.go:2425`): only `Put` if the key
   is still present in `bucketJobsLive` (a change that says "started" must not
   re-add a job the archive already removed).
4. **The exit lane is not just a live-bucket Put.** `jobExitData.update` also
   rewrites std buckets and fail-stats, and `launchJobExitUpdate` decrements
   `db.updatingAfterJobExit` under `db.Lock` afterwards. Route it through the same
   writer but keep its per-item side effects and that decrement.
5. **Do not apply backpressure while holding `db.wgMutex` or `db.Lock`.**
   `updateJobAfterChange` holds `wgMutex` and `updateJobAfterExit` holds the
   exclusive `db.Lock` across the enqueue; if the enqueue blocks there and the
   writer needs `db.Lock` (the exit decrement), it deadlocks. Enqueue must not
   block under those locks (bounded-but-coalescing, or acquire capacity before
   taking them).
6. **`db.wg` accounting + `close()` drain.** Keep in-flight writes tracked by
   `db.wg` (backup coordination waits on it) and drain the queue on `close()` (the
   spike leaked its writer — not acceptable for the real fix).
7. **`backupDirty`** must still be set after a successful drain.

Acceptance: `TestReliable4WriteStorm*` GREEN; `make test`/`make race` green; the
existing `BenchmarkUpdateJobState` bolt_writes/job not regressed (ideally lower);
and the reviewer re-runs `wrdev.sh unsuspend-burst` (ideally on prod.db) and sees
peak `bw` stay low (no explosion), no `bwmax` growth, status RPC responsive.

## Fix 3 — `NoFreelistSync` — TRIED AND DROPPED (2026-07-29)

The idea: add `NoFreelistSync: true` to the `bolt.Open` options so commits stop
writing the churn-bloated freelist. It was implemented and tested, but **dropped**:
`NoFreelistSync` makes bbolt rebuild the freelist by scanning the whole DB on the
first write-tx after open (O(db size)), which reintroduces history-proportional
startup cost and reproducibly breaks the reliable2 `TestReliable2FastStartupNoHistoryScan`
acceptance test (DEVELOPERS.md rule 6 — startup must not scale with completed-job
count; a real past outage). Stash-controlled: startup went 1.0x → 5.8–8.2x for
250k jobs, extrapolating to many seconds per restart on the 7.9GB prod DB.

Crucially, its benefit is now marginal: **Fix 1 already coalesced the write path
from thousands of tiny commits to a few**, so the per-commit freelist-write cost
this targeted is largely gone. Trading it for a multi-second restart-scan
regression is a bad deal. See `.docs/bugfixes/260729-1.md` bug #2. A possible
future follow-up (needs its own spec/tests): `NoFreelistSync` at runtime +
persist the freelist on GRACEFUL shutdown, so only an ungraceful crash pays the
open scan.

## Fix 5 — confirm-dead SSH connection cache (cloud package)

Precise map (from a code sweep):
- `cloud/cloud.go:278` `Provider.servers map[string]*Server` — grows one entry per
  distinct host; **never pruned** (no `delete` anywhere; `DestroyServer` deletes
  only from `resources.Servers`).
- `cloud/server.go:178` `Server.sshClients []*ssh.Client` — appended in
  `SSHClient` (`server.go:708`); closed ONLY by `Server.Destroy()`→`closeSSHClients`
  (`server.go:336`/`1670`). `RunCmd` closes only the *session* (`server.go:979`).
- Confirm-dead reaches it on OpenStack via `confirmJobDead` (`server.go:4907`) →
  `ProcessNotRunningOnHost` (`scheduler.go:625`) → `getHost`
  (`openstack.go:2153`) → `GetServerByName` → cached `*Server`. On **LSF**
  (`lsf.go:1470`) `getHost` dials a FRESH `*cloud.Server` per call whose client is
  never closed — a per-check leak.
- Concurrency IS bounded (`ServerConfirmDeadConcurrency=16`, `server.go:4922`); the
  CACHE is not. Prod grew 892 → ~5,300.

Correction (2026-07-29): on LSF it is not a "cache" at all — it is a pure LEAK.
`lsf.getHost` returns a FRESH `cloud.NewServer` per call; `RunCmd` dials a new
`ssh.Client` and closes only the session, never the client; the throwaway Server is
never `Destroy()`ed. And `confirmJobDead` calls `ProcessNotRunningOnHost` TWICE per
job (command pid + runner pid, `server.go:4928`/`4937`), so it leaks ~2 ssh
connections (+ their goroutines) per lost job. The 10-session multiplex and the
`Provider.servers` map only apply to the OpenStack path (not HGI prod, which is
`-s lsf`).

Fix (shape agreed 2026-07-29): a confirm-dead coordinator that COLLECTS pending
checks, GROUPS them by host, opens ONE connection per host, runs that host's pid
checks over it, then CLOSES it. Closing fixes the leak; one-connection-per-host
collapses ~2N dials into ~1 per host (lost jobs cluster on the dead node). Needs a
`Host.Close()` on the scheduler interface (optionally a batch
`ProcessesNotRunningOnHost(host, pids)`); bound by concurrent HOSTS (replacing the
per-check semaphore); preserve the retry cadence + per-job kill. Open/close per
batch is the leak-free v1; a TTL/LRU connection cache is a later optimisation; a
single multi-pid `ps` is gated on mercury's forced-command.

Reproducers: (1) END-TO-END ON LSF, here — earlier notes wrongly said this was
impossible. A `wrdev.sh` mode that submits jobs, induces lost→confirm-dead, and
measures the manager's leaked ssh-client goroutines / open fds (goroutine dump,
`/proc/<pid>/fd`); post-fix the count stays bounded. This is the authoritative
gate. (2) A FAST in-process unit test needs a seam: the `mock` scheduler does no
SSH and `cloud.Server.SSHClient` dials `ssh.Dial` directly — the `Host.Close()` +
batch method above give the mock that seam. Secondary amplifier of the recovery
storm — goroutine/fd growth, NOT the freeze trigger (bug #1 already fixed that).

## Fix 4b — recovery-gating (documented follow-up, design decision)

> SUPERSEDED (2026-07-29) by the "serve clients only when fully ready" design —
> see `.docs/serve-when-ready/prompt.md` (input for `/spec-writer`). Not serving
> clients until recovery is complete closes this gap by construction (no window in
> which a control RPC can be silently lost), and also fixes the empty/half web-UI
> and the completed-jobs-missing-until-refresh symptoms. Prefer that over the
> per-RPC gating described below.

`jsuspend`/`jresume`/`getsetlg` (`serverCLI.go:1696`/`1707`/`1740`) call
`suspendJobs`/`resumeJobs`/`getSetLimitGroup` directly and are NOT gated by
`isRecovering()` (unlike the runner report paths `getij`/`getijForReport`, which
return `ErrRecovering`). During the recovery window a resume for a not-yet-restored
key finds no queue item and is silently counted as 0-affected — the operator's
resume is lost (jobs stay suspended after recovery). Suspended state itself IS
preserved across restart (`recoverJobToItemDef` → `SubQueueSuspended`, not
scheduled), so a restart does NOT auto-re-storm. Deciding whether these control
RPCs should return retryable `ErrRecovering`, defer, or be re-applied post-recovery
is a design call — hence a documented follow-up, not a pre-judged RED test. Model
any test on `reliable2_dbcompat_test.go:169` (the runner-path `ErrRecovering`
pattern) + the `recoveryPauseHookForTest` seam (`server.go:248`).
