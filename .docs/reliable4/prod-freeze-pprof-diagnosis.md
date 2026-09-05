# Prod-freeze ROOT CAUSE + repro/fix plan — live pprof capture (2026-07-28)

## STATUS: root cause FOUND and MEASURED on the real prod manager.
## The freeze is a single-writer DB-commit collapse under an unbounded burst of
## best-effort job-state writes, NOT the DB backup (the prior lead is corrected
## below). NEXT AGENT: start at "PLAN FOR THE NEXT AGENT" near the end — build the
## failing reproducer first (TDD via /bugfix), then Fix 1.

## How captured (this is the real prod manager, not a synthetic harness)

The HGI prod manager (`wr manager v0.37.1-36-g447c6df`, functionally == ffd180d;
the extra commit is docs-only) was restarted by the operator in the FOREGROUND
with `GODEBUG=gctrace=1 WR_PPROF_ADDR=0.0.0.0:6060 wr manager start -f` (unclean
kill -9 of the old daemon first, so the token survived and runners reconnected).
`WR_PPROF_ADDR` starts a dedicated net/http/pprof endpoint on its own goroutine
(answers even while the committer/queue is frozen) AND auto-enables mutex
(fraction 5) + block (rate 10us) profiling. Captured from the dev host over the
network (0.0.0.0 bind). The freeze was triggered by un-suspending the
results_portal batch (the suspend-not-sticking bug released far more than the
~2000 cap). Artifacts under `/nfs/hgi/wr/sb10-pprof/` (goroutine debug=2 dumps,
live mutex/block/heap/cpu, `wr-fg-*.log` with gctrace, and the capture scripts).

## The mechanism (all numbers are measured from the dumps)

1. **Trigger.** Un-suspending a large batch flips ~100k jobs' state near
   simultaneously (and the suspend/limit controls don't hold, so it's the whole
   backlog, not the 2000 cap).

2. **Unbounded write-goroutine spawn.** Every job change/exit persists
   best-effort to bolt:
   - `updateJobAfterChange` (db.go:2386) -> `launchJobChangeUpdate` (db.go:2417)
     -> `go func(){ db.bolt.Batch(...) }` — **one new goroutine per change**, no
     bound, no backpressure (the RPC handler returns immediately after spawning).
   - `updateJobAfterExit` -> `launchJobExitUpdate` (db.go:2276) — same pattern.
   Captured at freeze onset: **119,698 goroutines**, **114,459 blocked in
   `bbolt.(*DB).Batch`** = 81,849 `launchJobChangeUpdate` + 9,076
   `launchJobExitUpdate` + 1,996 `archiveJob`.

3. **bbolt batch-coalescing collapses -> thousands of tiny fsync'd txns.** bbolt
   detaches `db.batch` the instant a batch starts running and arms a fresh 10ms
   timer per new batch, so under this arrival pattern batches never approach
   `MaxBatchSize=10000` (ServerDBBatchSize) — they stay tiny. Measured:
   **3,004 `batch.run` goroutines** queued on the single write lock
   (`bbolt.(*DB).beginRWTx -> sync.Mutex.Lock`, bbolt db.go:824 = `db.rwlock`)
   for ~90k writes ⇒ **~30 writes per transaction ⇒ ~3,004 separate write txns**,
   each paying full `beginRWTx`(freePages) + spill + freelist-sync + fdatasync
   cost, serialized through one writer.

4. **Each commit is CPU-bound in the freelist/spill, on the 7.9GB DB.** The
   rwlock holder's captured stack:
   ```
   freelist.(*shared).Free  <- node.spill <- Bucket.spill <- Tx.Commit
     <- DB.Update <- batch.run   [runnable]
   ```
   i.e. burning CPU in freelist/spill, **not** in `fdatasync`, **not** in
   `mmap`/`mmaplock`. bbolt uses `FreelistMapType` and syncs the freelist on
   every commit (no `NoFreelistSync`); on a 7.9GB DB with a churn-bloated
   freelist, this is expensive, and it is paid ~3,004 times.

5. **The timeout-critical path is the collateral damage.** `archiveJob`
   (db.go:1675) commits **synchronously** (db.bolt.Batch, db.go:1689), and the
   block profile shows `dispatchClientRequest -> dispatchMethod -> handleArchive
   -> archiveCompletedJob` **blocked** behind the storm. Waiters reached
   `bwmax = 14 minutes` (>> the 60s ClientMinRequestTimeout floor) -> runner
   `receive time out` -> job lost -> re-reserved -> original archive rejected
   (ErrMustReserve) -> work discarded -> retry.

6. **Self-amplifying, does NOT self-recover.** After the operator suspended,
   the goroutine count *grew* 119k -> **438,395** (change 251,327 + exit 137,412
   + archive 7,596 + ssh 31,875). Compounding sources, all measured:
   - **suspend/re-suspend itself fires a change-update per job** — repeatedly
     re-suspending ~100k jobs pumps the change-update backlog directly.
   - **lost -> confirm-dead -> release -> retry -> lost** feedback keeps
     generating state changes even with external load off.
   - **confirm-dead SSH connections exploded 892 -> ~5,300** (31,875 goroutines):
     one persistent cached SSH client per lost-job host.
   Recovery only began once the operator STOPPED re-suspending: drained
   ~10-14k goroutines/min, RPC/web UI responsive again. Confirms load-driven
   collapse, not a deadlock.

## Independent corroboration

- **Mutex profile:** 99.8% of contended-mutex time is `sync.Mutex.Unlock` under
  `bbolt.(*DB).Update -> Tx.Commit -> batch.run` (= `db.rwlock`), ~525 hrs
  cumulative — the single write lock IS the contention point.
- **Block profile:** ~95% channel-receive (the ~90k `Batch` callers on
  `<-errCh`) + `sync.Mutex.Lock` (the rwlock waiters); explicitly shows the
  archive RPC path blocked.
- **gctrace (fg log):** heap sawtooth ~1.4->2.0GB early (live <1GB, later ~2GB),
  STW pauses 0.15-8ms, stacks 62->253MB across 438k goroutines. Healthy.

## Ruled OUT by this capture

- **GC / allocation spike** — heap and STW are healthy; "GC forced" lines during
  the freeze show allocation actually *dropped* (everything parked on channels).
- **mmap-remap lock contention** (a pre-capture hypothesis) — 0 `mmap`/`mmaplock`
  frames in any dump. Readers do not block the writer here.
- **Backup starving fsync directly** (the PRIOR memory/report lead) — the
  committer is CPU-bound in `freelist.Free`/`spill`, NOT in `fdatasync`, and the
  freeze is fully present in samples where `in_backup=0`. The concurrent backup
  is at most a *secondary* amplifier (its long read-tx pins freed pages, bloating
  the freelist and making Free/spill slower) — it is not the trigger and does not
  hold a lock the writer needs. **Part C (backup copy-I/O relief) is not the fix.**
- **CPU-profile red herring:** a live 20s CPU profile was dominated by `runtime`
  stack-unwind/print (`unwinder/pcvalue/step/gwrite/printlock`). That is the
  OBSERVER EFFECT of our own `goroutine?debug=2` walking 100k+ stacks every 5s
  (fg log had panic=0), NOT the manager's hot path. When profiling this again,
  do NOT take debug=2 dumps at >~50k goroutines more often than ~every 20-30s.

================================================================================
## PLAN FOR THE NEXT AGENT
================================================================================

### Key code sites
- `jobqueue/db.go:2386` `updateJobAfterChange` -> `:2417` `launchJobChangeUpdate`
  (the `go func(){ db.bolt.Batch(...) }` best-effort change writer; note the
  archive-vs-change race guard at db.go:2425-2431 — a change that says "started"
  must NOT re-add a job the archive already removed from bucketJobsLive; any
  rewrite MUST preserve this "only Put if key still present" check).
- `jobqueue/db.go:2276` `launchJobExitUpdate` (best-effort exit writer,
  `db.bolt.Batch(exit.update)`).
- `jobqueue/db.go:1675` `archiveJob` -> `:1689` synchronous `db.bolt.Batch`
  (the timeout-critical path that gets starved).
- `jobqueue/db.go:611-628` `bolt.Open(... FreelistType: FreelistMapType)` (no
  `NoFreelistSync`); `jobqueue/db.go:844` `setBatchTuning`; defaults
  `ServerDBBatchDelay=10ms`, `ServerDBBatchSize=10000` (server.go:204/212).
- Backup (secondary): `db.go:3086` backupTicker -> `:3137` backgroundBackup ->
  `:3240` backupToBackupFile -> `:495` copyBackup (paced tx.WriteTo).
- confirm-dead / SSH cache (compounding): `confirmJobDead` /
  `confirmOrReleaseLostJob`, `ServerConfirmDeadConcurrency=16` (server.go:187) —
  concurrency is bounded but the per-host connection CACHE grows (892->~5,300).

### REPRO PLAN (build the failing test FIRST — TDD)
The failure signature to assert: a single large simultaneous state-change burst
makes in-flight DB-write goroutines explode and produces thousands of tiny bolt
write-txns, so the synchronous archive/final-state path exceeds the client
timeout. Fix must keep in-flight writes bounded, collapse to few large txns, and
keep archive latency < ClientMinRequestTimeout.

- **A. In-process (primary, fast; jobqueue/*_test.go, build tag like the existing
  `reliability_repro` `TestReliable4*`).** Start a server; add ~50-100k harmless
  jobs behind a limit group set to 0 (all ready-but-blocked). Instrument a counter
  of in-flight best-effort write goroutines (or count goroutines whose stack hits
  `db.bolt.Batch`). Release the burst (raise the limit / un-suspend) so all flip
  state at once. While the burst runs, issue a normal final-state/archive for one
  job and measure its latency. Assert (current code FAILS): in-flight write
  goroutines >> a few thousand AND archive latency crosses the timeout. After Fix
  1: in-flight writes bounded (e.g. < a few thousand), transaction count small,
  archive latency < timeout. To make per-commit cost realistic, either run
  against the prod.db copy (below) or pre-inflate the live bucket + freelist.
- **B. High-fidelity (wrdev.sh, farm/NFS scale).** New `wrdev.sh` mode
  `unsuspend-burst`: isolated PROD-mode manager on the real prod.db copy
  (`/nfs/hgi/wr/sb10-bigdb/prod.db`) on team166 + `WR_PPROF_ADDR`, stage a large
  suspended/limit-blocked batch, release it, and run the existing classifier
  `/nfs/hgi/wr/sb10-pprof/_capture_load.sh` (goroutine debug=2 every 5-20s ->
  bw / in_commit / in_backup / bwmax) to confirm the signature and, post-fix, its
  absence. This is the faithful check that the freelist/spill amplifier is tamed.
  Reuse the exact prod trigger; earlier synthetic repros failed only because they
  drove *steady* ~2000-concurrency, never a simultaneous ~100k state-change burst
  on the real freelist-bloated DB.

### FIX PLAN (ordered)
1. **PRIMARY — bounded, coalescing, dedup-by-key single writer for best-effort
   change/exit updates.** Replace the per-call `go db.bolt.Batch(...)` in
   `launchJobChangeUpdate`/`launchJobExitUpdate` with an enqueue onto a *bounded*
   structure keyed by `Job.Key()` holding the latest encoded value; a single
   long-lived writer goroutine drains it, folding all pending keys into ONE bolt
   write-tx per cycle (dedup => each churning job written once, not N times; few
   fsyncs instead of thousands). Bounded => backpressure (block or coalesce, never
   spawn unbounded goroutines). MUST preserve: best-effort semantics, latest-state
   -wins, and the archive-vs-change guard (only Put if key still in bucketJobsLive,
   db.go:2425-2431). This single change removes both the goroutine explosion and
   the coalescing collapse.
2. **Protect the timeout-critical path.** Ensure `archiveJob`/final-state commits
   are not starved by best-effort writes. With Fix 1 the shared writer is no
   longer swamped; if needed, give archive its own priority/lane so a burst can
   never delay it past the timeout.
3. **bbolt `NoFreelistSync=true`** at open — stop writing the churn-bloated
   freelist on every commit (big per-commit win on the 7.9GB DB). Validate the
   slower first-open (freelist rebuild) is acceptable for prod restart.
4. **Make suspend cheap + recovery-safe** (this is what turned a test into an
   incident): re-suspend must not persist a change-update per job on every call;
   and fix suspend-not-restored-on-recovery (#531) so a restart does not re-storm.
5. **Bound/evict the confirm-dead SSH connection cache** (892 -> ~5,300 under a
   lost-job storm) to cap goroutines/fds during churn.

### Validation gate for the fix
Repro A must flip fail->pass; Repro B on prod.db copy must show the freeze
signature gone (bw stays low/drains fast, no bwmax growth, archive latency <
timeout) under the un-suspend burst.

### Operational note (returning the live prod manager to normal)
Any restart re-storms while #531 is unfixed. Safe sequence: while the recovering
manager is responsive, set `results_portal` limit -> 0 (persists in bucketLGs),
THEN Ctrl-C the foreground manager (keeps token) and restart daemonized without
`WR_PPROF_ADDR`; raise the limit gradually once the code fix is deployed.
