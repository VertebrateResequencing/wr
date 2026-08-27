FEATURE: make the manager's write path fast enough to sustain high job limits on fast-completing jobs — by stopping the add path monopolising bolt's write lock, and stopping the periodic backup freezing the whole database.

Suggested spec location: a new `.docs/write-path/` directory (matching the repo's `.docs/dep-granularity/`, `.docs/reliable/`, `.docs/reliable2/` convention of prompt.md + spec.md + phases/).

## READ THESE FIRST — the investigation is already done, do not redo it

- `.docs/reliable4/prod-validation-260827.md` — especially "Three mechanisms, separated by experiment", "The completion path, read - and a reframe", and "Limit stress test". This is the authority for every measurement below.
- `.docs/reliable4/throughput-architecture.md` — especially "RESOLVED: what the fixes are (2026-08-27)", which names fixes A and C as this spec's scope. Note its CORRECTION block: group commit for the archive path is ALREADY implemented and deployed (`f7e36bc`'s coalescing `archiveWriter`), so this spec must not reinvent it.
- Repo root `DEVELOPERS.md` — binding. Rules 1 (no lock held across I/O), 2 (no new server-wide exclusive lock on the transition path) and 6 (no history scan on startup or a control path) all bear on this.
- `developers/wrdev.sh` — read before specifying anything that touches scheduling, startup or the gates. `archive-ceiling`, `archive-rate`, `writestorm-freeze`, `backup-stall-fast` and `unsuspend-burst` are the existing harnesses in this area.

## The problem, measured on live production 2026-08-27

Production completed **~14 jobs/s** regardless of how many runners were pointed at it. Raising a job limit from 20 to 2000 gave 1,143 concurrent runners, and: archive RPC latency walked to the 60 s `ClientMinRequestTimeout` floor and pinned there; **470 completion reports were lost in a single minute** (`failed to update server with cmd's final state`, `err="receive time out"`); throughput rose only ~1.6x for 57x the concurrency. Slow `add`s outnumbered slow `jarchive`s **8,637 to 6,403**, with `add jobs=1` at p50 43.3 s and max 213 s, size-independently.

Ruled out by measurement: fsync latency (0.73 ms), freelist size (10.9% free), the archive code path itself (364/s in isolation on production's own filesystem with the backup streaming), and NFS per se (~67/s with a small DB).

## SCOPE: two coupled mechanisms

**Fix A — the add path monopolises bolt's write lock.** Interleaving one `storeNewJobs` per archive takes throughput from **357/s to 150/s** and p50 from 897 ms to 2,497 ms, hitting `add` and `jarchive` equally, independent of freelist slack. The `archive fold` line reads `meanLock=1.421s maxLock=3.9s`: the archive writer spends most of each transaction *waiting for the write lock*, held by the add path's `bolt.Batch`. The add path costs 3-6 write transactions per request (`storeLimitGroups`, plus 2-5 concurrent `bolt.Batch` goroutines from `storeNewJobData` via `storeLookups`/`storeEncodedJobs`), and the archive writer's plain `Update` cannot join a `Batch`. This matches the real workload: the portal's dedup jobs complete fast and each one ADDS compress jobs that depend on all dedup jobs finishing, so adds are interleaved ~1:1 with completions and one huge dep group holds 112,486 memberships.

**Fix C — a growing file freezes behind the backup's read transaction.** `db.backupToBackupFile` (`jobqueue/db.go:4246`) wraps the whole copy in ONE `db.bolt.View`, holding `mmaplock.RLock()` for its life. A write that must grow the file calls `db.mmap`, which takes `mmaplock.Lock()`, and Go's `RWMutex` then queues every subsequent reader behind it — freezing the whole DB for the copy's REMAINING duration. Confirmed by A/B on freelist slack alone (identical throughput and p99 across 430,000 archives; the only difference was Arm B's multi-second stall at the exact second the file crossed its mmap boundary, with zero archives completing for the copy's remaining ~5 s) and by a mid-freeze goroutine dump showing one writer in `bbolt.(*DB).mmap` -> `db.mmaplock.Lock()` beneath `archiveTx` while the holder sat in `Tx.WriteTo` <- `copyBackup` <- `bbolt.(*DB).View`. Freeze duration = DB size / copy bandwidth: ~6 s in the harness, ~40 s at production's current copy speed, worse under load.

**Currently dormant and predicted to return:** production's free pages fell from 271,809 to 111,133 in 80 minutes with the file size static — ~0.46 GB of slack before writes must grow the file again.

## Why A and C must be specified together

They are coupled through the freelist, in both directions:
- An open read transaction pins every page freed during it (`freelist.ReleasePendingPages` only releases below the oldest read txid), so **the copy starves the freelist and thereby causes the growth that triggers the remap**.
- **Fix A raises throughput and therefore grows the file faster, which makes fix C fire more often.** A without C could make things worse.

## Also in scope: the backup's data volume

A third mechanism is confirmed but is NOT simply fixable, and the spec should settle what to do about it. Production shows ~10 slow `jarchive`s of 17-27 s per 16 minutes, about one per backup copy cycle, with 12 full 8.99 GB copies at a **35% duty cycle** and the DB not growing — so this is the copy's I/O, distinct from the remap freeze.

**Do not propose "add incremental fsync": it is already there.** `copyBackup` (`jobqueue/db.go:830`) already streams through a `backupCopyWriter` that paces writeback every `backupCopySyncBytes` (8 MiB) using `sync_file_range` on Linux with a full-fsync fallback, and it was already recorded as insufficient under high archiver concurrency. What remains is **copy less often** (a recovery-point-objective tradeoff that is the operator's decision, so ASK) or **copy less data** (incremental/segment backup, a real design question).

## Constraints the spec must honour

1. **Durability contract unchanged.** Both `add` and `jarchive` must remain durable before their reply. The existing coalescing `archiveWriter` is the model: each waiter gets its own error, and waits for the transaction containing its own write. Sharing a writer must not let one caller's bad record fail everyone.
2. **No user-visible behaviour change** — `wr status`, `waitingForDepGroups`, the REST contract (`.docs/issue-197/spec.md` is a binding written contract), and the web payload.
3. **Backup consistency.** A bolt backup needs one consistent snapshot, so the copy cannot simply be chunked into several short read transactions. Any change here must state why the result is still restorable.
4. **Do not regress the recent deliveries.** `.docs/dep-granularity/` (seven commits, prod-validated), `f7e36bc` (archive coalescing), `f51af04` (backup no longer serialises the archive hot path), `ed763b0` (bounded coalescing best-effort writer), `f4b9b55` (ownership-gated, idempotent final-state reports). Read each phase file's "Phase N outcome" section for corrections those deliveries made to their own specs.
5. **Ruled out by the operator, not to be relied on:** moving the DB to local disk; depending on compaction cadence; moving the backup to a different filesystem. Any of these may be *measured* as a diagnostic but nothing may depend on them.
6. **The observability already exists and should be used, not duplicated:** `9fbccb4` added a periodic warn-level `archive fold` line (`txs`, `archives`, `meanFold`, `maxFold`, `meanWait`, `maxWait`, `meanTx`, `maxTx`, `meanLock`, `maxLock`) whose three signatures distinguish remap freeze from lock contention from sparse arrivals.

## Acceptance evidence the spec should demand

The production acceptance test is already written down and should be the spec's headline gate: **sustain limit 2000 (~1,000+ concurrent runners) with archive latency clear of the 60 s floor and ZERO `failed to update server with cmd's final state` in the runner logs.**

In-process, the gates must be proven to FAIL pre-fix — this repo has produced false-PASS gates five times, and `.docs/reliable4/next-steps-260819.md` documents the required pattern. `wrdev.sh archive-ceiling` already exists and PASSES on current code (44x throughput factor), so it is a regression guard, not a red gate; a new or extended gate must reproduce the interleaved-add collapse (357 -> 150/s) and, for fix C, the compacted-DB remap freeze. Note bbolt's own `db.bolt.Stats().TxN` needs no new instrumentation, and inert counters are an established convention here (`db.archivedDecodes` `5c75a15`, `Job.derivations` `8087866`, `db.archiveTxObserver` `f7e36bc`, `db.depGroupSeenGets`, `db.arFold` `9fbccb4`).

Quality gates: `make lint` 0 issues; `unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' '); timeout 3000 make test` with a baseline of **486 passed / 9 skipped / 29 packages** at commit `11fb939`; `go vet -tags netgo` and `-tags 'netgo reliability_repro'`; `make race`.

## HOST SAFETY for anyone running anything

A **live production manager is running** on farm22-ibackup01 with files under `/nfs/hgi/wr/lsf/.wr_production/` — never touch that directory (reading its log is fine), never kill a manager not started by the test, no real LSF jobs. `/nfs/hgi` IS authorised for heavy test I/O (~193 GB free; writable: `/nfs/hgi/wr/sb10-wrdevtest`, `sb10-wrdev`, `sb10-valgate`, `sb10-pprof`), and fixtures at `/nfs/hgi/wr/sb10-bigdb/` (`pristine6`, `pristine10`, `prod.db`) are read-only — copy out, never write in. Note `pristine10`'s synthetic records are invisible to history paths, so it gives false PASSes on history-shaped gates. Production shares the filesystem and is latency-sensitive: prefer few careful runs to many.
