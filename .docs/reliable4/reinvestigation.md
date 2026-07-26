# reliable4 reinvestigation — the LSF-scale stall PERSISTS after #1/#3

---

## ✅ STATUS as of 2026-07-26 (read this first — reproducer done, root cause confirmed, fix found)

**TL;DR:** the backup stall is now **reproduced from scratch**, its root cause is
**confirmed** (not what UPDATE 2 guessed), and a **simple, validated fix** eliminates
it. Recommendation at the bottom: **/bugfix** (small, localized).

### The faithful from-scratch reproducer (built + committed)
The trigger is the DB's complete-job **record count / churn**, not file size. A
padding DB (few huge values, ~empty freelist) never reproduced even at 8GB.
`jobqueue/reliable4_backup_repro_test.go` (`TestReliable4InflateDB`, build tag
`reliability_repro`) now generates a DB with three production-like properties:
(1) ~2.1M **real** codec-encoded complete-job records + the `endTimeToKey` index;
(2) multi-GB size; (3) a **large persisted freelist** (throwaway bucket filled then
`DeleteBucket`'d as the final write, so its pages persist as free on reopen). Two
harnesses drive it (`developers/wrdev.sh`):
- `backup-stall-fast [archivers] [seconds] [pauseMs]` — FAST, in-process, no LSF:
  opens the big DB via the real `initDB` (prod, backups on) and hammers
  `db.archiveJob`, timing each; a backup that freezes archives past the TTR = churn.
  Uses `WRDEV_PRISTINE_DB` (copied per run). This is the iteration harness.
- `backup-stall-check [dbGB] [N] [limit] [runsec] [records] [freelistGB]` — the
  faithful end-to-end LSF gate (record-dense DB + prod manager + sleep jobs).
Generate a pristine DB once: `WR_INFLATE_DB=... WR_INFLATE_RECORDS=2100000
WR_INFLATE_GB=6 WR_INFLATE_FREELIST_GB=2 go test -tags reliability_repro ./jobqueue/
-run TestReliable4InflateDB -timeout 3600s`.

### Root cause (CONFIRMED — corrects UPDATE 2's "the copy is the problem")
The backup **copy** is a **sequential** `io.CopyN` of `tx.Size()` bytes
(bbolt tx.go `WriteTo`) and, on a 91GB-RAM box, reads mostly from page cache — so
the copy's cost is ~the 6–10GB **write** to the `_bk` file, and is NOT the thing
that scales with record count. The freeze is in the **foreground write path**:
- A goroutine dump DURING a reproduced freeze shows: **all archivers parked in
  `bbolt (*DB).Batch` (chan receive)**, **2 goroutines in `syscall`** (the one
  bbolt write-tx committer + the backup copy), **none in `mmaplock`/`RWMutex`**.
- So it is pure **I/O contention**, not a Go lock and not an mmap remap: the
  backup's big write burst on the shared (NFS) filesystem stalls the single bbolt
  write-tx committer's `write`/`fdatasync`, and every archiver serialises behind
  bbolt's one writer (Batch). The freeze lasts ~the whole copy ⇒ 6.89GB → ~15s here,
  6GB → 108s in production (slower/contended storage). Load-independent, recurs each
  backup interval.
- The per-commit **freelist write** (bbolt rewrites the whole freelist every commit;
  ~4MB on a 3GB freelist) is a real **throughput** tax (see NoFreelistSync below) but
  is NOT the freeze cause.

### Experiments (in-process, 6.89GB DB / 3.1GB freelist, on /nfs/hgi)
| config | archive throughput | max archive latency (freeze) |
|---|---|---|
| baseline | ~140/s | **15.8s** |
| `NoFreelistSync` | ~390/s (2.8×) | 16.9s (freeze remains) |
| throttle copy 100MB/s | ~390/s | 6.0s (reduced, not gone; copy 70s ⇒ staler) |
| **incremental backup `fsync` every 32MB** | ~130/s | **0.70s (freeze GONE)** |
| incremental fsync + NoFreelistSync | ~370/s | 0.59s |

At **10GB** (3M records / 4.7GB freelist): baseline maxLat **10.99s** (freeze) →
incremental `fsync` 32MB maxLat **0.70s (zero churn)**. Identical 0.70s at 6.89GB
and 10GB confirms the archive-latency bound is **size-independent** — it meets the
"cope with 10GB backups without any job churn" bar.

**Backup-duration cost (measured, 6.89GB):** the fix makes each backup copy take
~**36s** vs baseline ~**20s** (~1.8×) — the price of ~220 synchronous `fsync`
round-trips. Bounded, not catastrophic, and archives stay at 0.77s throughout. So
the fix trades a slightly longer, more-continuous backup for zero archive freeze.
(A `sync_file_range(WRITE)`+`fadvise(DONTNEED)` variant would bound dirty pages
without the synchronous round-trips, avoiding the bloat, at the cost of being
Linux-specific — a possible refinement.)

**End-to-end LSF (`backup-stall-check`, 6.89GB, 4000 sleep-20 jobs / limit 2000,
NO --debug):** baseline `maxStatusRPC=751ms`, `badjobDelta=311`; fix
`maxStatusRPC=192ms`, `badjobDelta=768`. The fix **improved manager responsiveness**
(status-RPC 751→192ms = the manager backup freeze is gone, matching the in-process
result), but BOTH runs still showed tail `badjob` churn (fix higher, but it also
ran more jobs concurrently). That residual churn is NOT the manager backup freeze
(the fix fixed that) — it is the SEPARATE client-side / fast-drain churn (real
runners on a busy cluster missing touch/archive TTRs; cf. the earlier
reinvestigation's "runner-starved-touch" component that #1/#3 only partly
addressed). This fast-drain LSF config (drains in ~6min, no --debug so freezes are
invisible in the log) is too noisy to cleanly A/B the backup effect; the in-process
harness isolates it cleanly and is the reliable gate. A cleaner end-to-end check
would sustain saturation (≫limit jobs, longer sleep) and run with --debug so
backup freezes show as real log gaps.

### The fix (validated) — incremental backup fsync
Make the backup copy `f.Sync()` every ~32MB instead of buffering the whole multi-GB
write. This keeps the backup's dirty-page backlog small so it never clogs the
writeback queue the archive `fdatasync` waits on — **without** capping bandwidth
(unlike throttle, so backups stay fresh) and **without** `NoFreelistSync`'s cost.
Crucially the archive `fdatasync` then only ever waits behind ≤32MB of backup write,
so archive latency is **bounded independent of total DB size AND storage speed**
(32MB even on 10×-slower prod storage is sub-second) — this is what robustly kills
the 108s production freeze. Prototyped behind `WR_EXP_BACKUP_SYNC_MB` in
`db.copyBackup` (jobqueue/db.go). Optional add-ons, not required for zero churn:
`NoFreelistSync` (`WR_EXP_NOFREELISTSYNC`, 2.8× archive throughput, but the freelist
is rebuilt by a page-scan on next open — measure that cost); adaptive interval
(`WR_EXP_BACKUP_K`, fewer backups). The archive-DB split (below) is a larger
redesign that would also cut backup I/O **volume/staleness**, but is NOT needed to
stop churn.

### Recommendation
**/bugfix for the backup-freeze churn, now.** Incrementally fsync the backup copy
(default interval ≈16–32MB; `db.copyBackup`). It is small, localized, unit-testable
(assert byte-identical copy + `Sync` called per interval), and changes nothing about
DB durability, consistency, open behaviour or the freelist. It demonstrably removes
the backup-induced manager freeze at 10GB (in-process 11s→0.7s; LSF status-RPC
751→192ms). Drop the temporary `WR_EXP_*` scaffolding; keep the interval a named
constant. Optional companions (separate, independent): `NoFreelistSync` (2.8× archive
throughput — but measure its open-scan cost first) and an adaptive backup interval.
Consider the `sync_file_range` variant to avoid the ~1.8× backup-duration bloat.

**Also worth a /spec-writer: the archive-DB split** (the user's idea). It is the
only approach that makes the FREQUENT backup **cheap** (small live DB) rather than
pacing a whole multi-GB copy every ~30–60s — so it removes the churn AND the backup
I/O **volume/staleness** cost that even the fixed full-copy still pays. Bigger change
(splits jobscomplete/endTimeToKey by age across a live+archive DB, spans reads over
both, promotes an ancient job back to live on re-run, adds a background ager +
migration). Not required to STOP churn, but the better long-term design.

**Note (separate issue):** the LSF end-to-end still shows tail `badjob` churn with
the backup fix applied. That is NOT the backup freeze (fixed) — it is client-side /
saturation TTR-miss churn (real runners on a busy cluster), the same family the
earlier reinvestigation's #1/#3 only partly addressed. It needs its own follow-up
and is out of scope for the backup-stall fix.

### Validated
- 10GB confirmed: baseline freeze 10.99s → incremental-fsync 0.70s, zero churn.
- End-to-end LSF `backup-stall-check` (real churn counters, baseline vs fix) run as
  final corroboration.

### Still TODO (for the /bugfix and follow-up)
- Implement the fix as default in `db.copyBackup` (sync interval ≈16–32MB, keep it
  a named constant), drop the temporary `WR_EXP_*` scaffolding, add a unit test
  (assert the copy is byte-identical AND `Sync` is called every interval, e.g. via a
  countable writer / the `slowBackups` hook).
- Tune the sync interval (16 vs 32 vs 64MB) if desired; 32MB already gives sub-second.
- Measure `NoFreelistSync` open-scan cost on 6–10GB before deciding to also adopt it
  (it is an optional throughput win, not part of the churn fix).
- Turn OFF prod `--debug`. Isolated test dir: `/nfs/hgi/wr` (home is full);
  pristine DBs at `/nfs/hgi/wr/sb10-bigdb/pristine{6,10}`.

---

## (HISTORICAL — superseded by the STATUS block above; UPDATE 2's "backup copy" root cause was corrected) earlier RESUME HERE

**Where we are:** #1 (rac scan bound) and #3 (Started-timeout kill) shipped in PR
#555 and are correct but NOT sufficient. Production still stalls at LSF scale.
The stall is an **archive-RPC-timeout / false-lost churn**: a job COMPLETES but
its `archive` RPC times out (proven by a runner log: "receive time out" then
"jarchive: bad job"), so the manager marks it lost → confirmed dead → reruns it.
The manager **periodically FREEZES** (proven: a 108s and a 22s log gap; status_rpc
spikes) and during those freezes RPCs time out. It is **load-independent** (hits
at ~250 jobs and at zero load) and **DB-size dependent**.

**The leading hypothesis (the user's) is the periodic DB backup** —
`db.backupToBackupFile` does `db.bolt.View(tx.CopyFile(...))` of the WHOLE
multi-GB DB every `minimumTimeBetweenBackups = 30s` (jobqueue/db.go). Dev-manager
runs never back up, which is why every dev-mode reproducer drained cleanly.

**IMPORTANT UNRESOLVED CAVEAT — do not assume "backup" is proven:** the portable
reproducer inflates a big *padding* DB and runs it prod-mode with backups on. A
**4GB and an 8GB padding DB both DRAINED CLEANLY** (no churn) — yet a copy of the
REAL 6GB production DB (2,110,982 complete-job records) DID reproduce the churn +
a 22s freeze. So the trigger is tied to the DB's **complete-job CONTENT / record
count**, NOT merely file size or backup-copy duration. It is therefore STILL OPEN
whether the freeze is (a) the backup behaving differently on a record-dense DB,
or (b) the **complete-job scans** on the big DB (e.g. `getJobsRecent` →
`retrieveCompleteJobsRecent`, or the web status feed / seeding) freezing the
manager. Both correlate with DB record count, so they were not yet isolated.

**NEXT STEPS (for the fresh agent):**
1. Make the reproducer FAITHFUL: replace the padding inflater with one that writes
   ~2M REAL complete-job records into `jobscomplete` (wr's encoding), so the DB is
   record-dense like production. Re-run `developers/wrdev.sh backup-stall-check`
   and confirm it reproduces the churn/freeze (it currently does NOT with padding).
2. ISOLATE backup vs scan: with a record-dense DB, compare (i) backups on vs a
   build with backups effectively disabled, and (ii) idle vs with the web status
   feed active. A goroutine dump (`wrdev.sh dump` + SIGQUIT) DURING a freeze on
   the reproduced stall will show exactly what holds the lock / what's running.
3. Only then `/bugfix` the confirmed root (if backup: adaptive backup interval,
   unit-testable via the existing `slowBackups`/`slowBackupTestDelay` hooks; if
   scan: bound/paginate the complete-job scan or serve counts from an index).

**Reproducer + tooling (committed on this branch):**
- `developers/wrdev.sh backup-stall-check [dbGB] [N] [limit] [runsec]` — portable,
  self-generates the DB; `WRDEV_ROOT` is where the DB+backup live (needs ~2x dbGB).
  Currently uses a PADDING DB (does NOT reproduce — see caveat above; make it
  record-dense per NEXT STEP 1).
- `developers/wrdev.sh limit-drain [...]` — dev-mode drain check (drains cleanly;
  useful as a no-backup control).
- `jobqueue/reliable4_backup_repro_test.go` (build tag `reliability_repro`) —
  `TestReliable4InflateDB` (padding inflater; needs the record-dense rewrite).
- `jobqueue/scheduler/lsf.go` — a small committed improvement: `submitToQueue` now
  surfaces bsub's STDERR on failure (was discarded; hid the real LSF reason for the
  post-restart `bsub ... exit status 255` runner-scheduling failures). Unrelated
  to the stall; keep it. NOTE it has no dedicated test yet.

**Separate production issue (not the stall):** after a restart, runner bsubs
failed (`exit 255`) — wr auto-picked restricted `*-inference` queues AND `normal`
bsubs failed (LSF pending-limit?). The user's `bqueues` wrapper (hides inference
queues) only helps if it's on the MANAGER's non-interactive `bash -c` PATH and
filters `bqueues -l`. The bsub-stderr change above will reveal the real reason.

**Isolated test dir used:** `WRDEV_ROOT=/nfs/hgi/wr/sb10-wrdev` (home is 100%
full; lustre has a quota). Clean it up when done (`rm -rf`).

---

Date: 2026-07-25 (evening). The reliable4 build (PR #555, HEAD `d500a95`) was
deployed to **production** (mercury, farm22-ibackup01, **with `--debug` on**).
The portal workload (`portal_20260724T115039_{dedupe,compress}`, one limit group
`results_portal` limit=2000) **still stalls**: `compress` gets no new completions
with >500 running; `dedupe` keeps flapping lost. So #1 (rac scan bound) and #3
(don't kill on transient Started) were necessary but **not sufficient**.

## Production evidence (manager log 21:20–21:37, after a restart+recovery)

- **Backlog not draining.** ready `items` sits at **~99–100k** the whole window
  and even grows; net completions ≈ 0.
- **rac is NOT the bottleneck anymore.** rac cycles are **~0s** each (bug #1
  works) and the summed runner request per cycle is capped at **exactly 2000**
  (= limit; §2a/over-provision hold). So neither rac cost nor over-provisioning
  is causing this.
- **Steady false-lost churn.** "killed a job after confirming it was dead"
  ramps to and plateaus at **~750/min**; `jarchive` bad-job + must-reserve spike
  to **~1210/min** — completed work rejected because the job was already
  lost→confirmed-dead→released→**rerun** (double-run), so the original runner's
  archive hits `bad job`/`must reserve`.
- **Reconnect is NOT broken.** `wrdev.sh crash-recovery` PASSES on the exact
  HEAD (d500a95): a job survives a manager restart, re-adopts, completes exactly
  once, never shows "lost contact". The stuck-lost `wrstat-ui-summarise` job is a
  *recovered* job whose process is genuinely alive (so it is NOT in the
  confirmed-dead kills); it flaps "lost" under saturation and self-resolves when
  a touch finally lands (only 14 lost→running re-adoptions vs 5674 kills in the
  window because the manager is saturated). Same stall, not a reconnect bug.

## Root-cause hypothesis (to be CONFIRMED by reproduction + goroutine dump)

The dominant driver is the **touch-during-run TTR-miss → false-lost loop**, which
#1/#3 do not address:

1. A runner reserves+starts a job; under load the manager does not service its
   `touch` within the 60s TTR.
2. TTR expiry → job marked **lost**; if the process already exited (short job)
   `confirmJobDead` confirms dead → `releaseJob` → **rerun**.
3. The original runner's later `archive` is rejected (`bad job`/`must reserve`)
   → work discarded → backlog never drains. Positive feedback (reruns re-enter
   the backlog).

Why are touches missed when rac is now cheap? Prime suspects, in order:
- **The single global `queue.mutex`** (queue/queue.go): every reserve/touch/
  archive from ~2000 churning runners + the rac ready-snapshot serialize on it.
  reliable3 flagged this as RESERVED "only if measured" — it is now the measured
  bottleneck.
- **`--debug` logging I/O**: production runs with `--debug`; `reserved job` lines
  are ~25KB each (escaped-JSON args), thousands/min → heavy synchronous I/O that
  can stall the RPC goroutines. This is a confound the reproducer MUST isolate
  (run with and without `--debug`).
- The O(N) ready-snapshot (`readyItemData` + `snapshotReadyJobs`) still iterates
  all ~100k ready items per rac under a read lock; cheap per item but O(N) and it
  contends with writers on `queue.mutex`.

## Why the earlier validation missed it

`wrdev.sh churn 40000` used **no limit group** — jobs drained freely up to
fair-share, so it never created a large-backlog-behind-a-small-limit at
saturation, and it ran **without `--debug`**. It passed without reproducing the
real failure. The new reproducer (below) fixes both gaps.

## Plan

1. **Faithful reproducer** (`wrdev.sh` LSF-scale): N≫limit SHORT jobs in ONE
   limit group (small limit), monitor drain + churn (kills/badjob/archive-reject)
   + control-RPC latency; assert full drain. Must STALL on current code. Run with
   and without `--debug` to isolate the logging confound. This is the gate.
2. **Goroutine dump** (`wrdev.sh dump` + SIGQUIT) on the reproduced stall to
   confirm the true bottleneck (mutex waiters vs logging vs ssh) before fixing.
3. **/bugfix** the confirmed root until the reproducer fully drains
   (badjob≈0, no runaway confirmed-dead kills).

## Reproduction results (2026-07-25 late) — the stall does NOT reproduce synthetically

New `wrdev.sh limit-drain [N] [limit] [runsec] [padKB]` (WRDEV_ROOT moved to
`/nfs/hgi/wr/sb10-wrdev`, 334G, because 25KB cmds overflow the 50G home + a
lustre group quota). Runs, all on an isolated dev manager:

| config | result |
|---|---|
| 60k / limit 2000 / sleep 30 / no pad / **debug OFF**, hit **2000** runners | **DRAINS** linearly, lost=0, badjob=0, confirmed_dead=0, status_rpc 76–296ms |
| 20k / 2000 / sleep 30 / pad 25KB / **debug ON** | **DRAINS**, zero churn |
| 60k / 2000 / **sleep 3** (~high reserve rate) / pad 25KB / **debug ON**, ramped to ~1300 runners | **DRAINS**, lost=0, badjob=0, confirmed_dead=0, status_rpc ~140ms |

So the reliable4 code drains a production-SHAPED workload (big backlog behind a
2000 limit, one limit group across 100 mem→sibling groups, 25KB cmd lines,
`--debug`) with **zero false-lost churn and responsive status** — #1/#3 + the
reliable3 fixes hold under heavy synthetic load. **The production stall was NOT
reproduced.**

### What the synthetic runs DON'T replicate (candidate real triggers)
1. **Sustained full 2000 concurrency.** LSF fair-share throttled my ramp to
   ~1300; production held a fixed 2000, and a restart instantly reconnected ~2000.
2. **The at-scale restart/recovery cascade.** Production stalled *immediately
   after a manager restart+recovery* of a large in-flight workload (the 44k
   reserves/min burst at 21:21 is the reconnect+reschedule storm). My dev runs
   never restart (and the dev manager wipes its DB on restart, so recovery must
   be tested prod-mode). **This is the strongest untested, most faithful
   hypothesis.**
3. **Real compute-node load starving the RUNNER process.** portal_builder is
   CPU/lustre-heavy; on an oversubscribed node the wr *runner* can be starved and
   send its touch/archive RPCs late → 60s TTR miss → false-lost. My idle `sleep`
   runners always communicate on time, so they never go lost. The manager only
   ever sees RPCs, so a *manager* overload isn't required — a *client-side* delay
   suffices. Not a manager bug per se; would need TTR/touch-priority resilience.

### The stall mechanism deduced from the production numbers
Steady state was ~750 reserves/min **and** ~750 confirmed-dead kills/min with
2000 "running" and complete frozen: the 2000 limit slots are full of
**lost-phantom jobs**, so throughput is capped by the **confirm-dead (ssh)
reclaim rate**, and jobs cycle reserve→run→lost→kill→reserve without ever
completing (their touch/archive never lands within TTR). `ttrCallback` parks a
lost job in SubQueueRun WITHOUT freeing its limit slot (deliberate, while the
runner may still be alive) — so once phantoms fill the slots, net completions ≈
the confirm rate ≈ 0. Confirming this needs the false-lost to be triggered,
which requires one of the un-replicated conditions above.

### Decision
Blindly `/bugfix`ing without a reproduced failure would violate "know the fix is
good enough". Next step: build the **at-scale prod-mode restart/recovery
reproduction** (reach ~2000 running, kill+restart, watch for the phantom-slot
cascade) and/or clarify the production environment (node oversubscription; job
durations; is the stall always post-restart?). Then fix against that gate.

## UPDATE 2 (2026-07-25 ~23:xx) — the real root is the DB BACKUP, not scheduling

Fresh production run (manager v0.37.1-13-gd500a95-dirty, started 23:14, backups
ON) reproduced the churn at only ~250 jobs and even at zero load. A killed
COMPRESS job's runner log is the smoking gun:

```
23:23:59 started executing (zopfli -i1000 ...)
23:24:59 failed to update server with cmd's final state  err="receive time out"  <- command DONE, ARCHIVE RPC timed out
23:25:14 reconnected; jarchive ... err="bad job (not in queue or correct sub-queue)"  <- job already lost+released
```

So a job COMPLETES successfully but its **archive RPC times out**; the manager
(no touch/archive within the 60s TTR) marks it lost -> confirms dead (process
already exited) -> releases/reruns -> the good result is discarded. That is the
churn, and it is an **archive-processing stall**, not a scheduling one.

**Root cause = the periodic DB backup.** `db.backupToBackupFile` runs a
`db.bolt.View(tx.CopyFile(...))` that copies the ENTIRE bolt DB file, with
`minimumTimeBetweenBackups = 30s`. On the ~6GB production DB this copy (a) does
~12GB of NFS I/O and (b) holds the bolt read-tx's `mmaplock.RLock` for the whole
copy, so any archive write that must grow/remap the DB blocks on `mmaplock` until
the copy finishes. Either way, archive/touch DB writes stall past the TTR ->
"receive time out" -> false-lost -> confirmed-dead -> rerun. It:
- scales with DB SIZE (bad now at 6GB; "no completed backup for ~20min"),
- is LOAD-INDEPENDENT (hits at 250 jobs and at zero load — the summarise jobs),
- RECURS every ~30s (why the running jobs never get a stable window to reconnect),
- and is exactly why every earlier `limit-drain` reproduction DRAINED CLEANLY:
  they ran on the **dev manager, which does not back up** (backups need
  production deployment or the unexported forceBackups). That was the missing
  ingredient the whole time.

Also seen: 362 "failed to get a killed lost job" = a race where a job is
confirmed-dead-killed while its still-alive runner concurrently archives it.

**Reproduction (CONFIRMED 2026-07-26 00:0x):** on the isolated prod-mode manager
with the safe 6GB DB (jobscomplete=2,110,982 records, jobslive cleared), running
8000 sleep-30 jobs behind a 2000 limit, the manager backs up the 6GB DB
back-to-back (db_bk completed 00:01:37; db_bk.tmp mid-write 00:04:43). During
backups the manager FREEZES (22s log gap ending 00:03:49; status_rpc spikes
62ms->257ms->1695ms) and the sleep jobs — which cannot fail — CHURN: delayed
spiked to 995, badjob climbed 1982->7215. The identical workload on the DEV
manager (no backups) had ZERO churn.

**CORRECTION (see RESUME HERE at top):** this used the REAL 6GB DB. A later,
portable reproducer using a same/larger-size PADDING DB (4GB and 8GB) DRAINED
CLEANLY with backups on. So file size / backup-copy duration ALONE is not the
trigger — it is tied to the real DB's ~2.1M complete-job RECORDS. Whether the
freeze is the backup-of-a-record-heavy-DB or the complete-job SCANS is STILL
OPEN. Do not treat "DB backup" as proven; isolate it first (RESUME NEXT STEPS).

Reproduction (details):** isolated PROD-mode manager (backups on) started
on a SAFE copy of the real 6GB DB — `.tmp/db` copied to `/nfs/hgi/wr/sb10-wrdev`
and its `jobslive` bucket cleared (via a bbolt tool in `.tmp/dbhack`) so recovery
runs nothing, while `jobscomplete` keeps the file ~6GB so backups stay slow. Then
add sleep-30 jobs behind a 2000 limit and watch archives time out / jobs go lost
coinciding with each backup. (`wrdev.sh limit-drain` only exercises the dev
manager; the prod-mode + big-DB harness is `scratchpad/repro_bigdb.sh`.)

**Fix directions (once reproduced):** the every-30s full-file CopyFile of a
multi-GB DB is the problem. Options: make backup frequency adaptive to DB size /
last-backup DURATION (never back up more often than it takes to copy, plus a
floor); avoid blocking foreground writes (throttle/ionice the copy, back up to a
different spindle, or use an approach that can't hold `mmaplock` against a
remap); and/or pre-grow the DB so archive writes don't remap during a backup.
This is `/bugfix`-able against the prod-mode + big-DB reproduction.

## Immediate operational note

Production is still running with `--debug`. Regardless of the code fix, this
should be turned off — at ~100k backlog it is a large I/O amplifier and confounds
diagnosis.
