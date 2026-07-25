# reliable4 reinvestigation — the LSF-scale stall PERSISTS after #1/#3

---

## ⛳ RESUME HERE (read this first — status as of 2026-07-26 early)

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
