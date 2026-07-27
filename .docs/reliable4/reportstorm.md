# reliable4 — post-resume "report storm" reproducer + bottleneck pin

Status block (read first)
-------------------------
- **Branch/base:** `reliable4` @ `11910e3` (f4b9b55 busy-exit fix REVERTED: it was
  incomplete and introduced a "release not running" regression — see
  `reinvestigation.md`). PROD is still running the regressed `f4b9b55`.
- **What was done:** built a faithful in-process LOAD reproducer of the prod
  "post-resume report storm" (`jobqueue/reliable4_reportstorm_test.go`, tag
  `reliability_repro`, wired into `developers/wrdev.sh` as `report-storm` and
  `report-storm-profile`), swept it across concurrency / backlog / DB-size, and
  profiled it (CPU + mutex + block pprof) to PIN the serialization point rather
  than guess.
- **Headline:** the in-process path CANNOT reproduce the churn on `11910e3` at any
  tested config — a deliberate NEGATIVE result, not a tuning gap. But the profiler
  PINNED the mechanism, and it overturns the earlier queue-mutex hypothesis.
- **UPDATE 2026-07-27: the churn IS now reproduced FAITHFULLY at LSF scale** via a new
  `wrdev.sh report-storm-lsf` (isolated PROD-mode manager on a 10GB DB copy, backups ON,
  + 50k fast jobs behind reprolimit:2000 → ~2000 real LSF runners). The 10GB backup on
  NFS froze the manager for **122s** (>> the 60s client floor), and archive-reject churn
  climbed to **4089** `jarchive(...): bad job (not in queue or correct sub-queue)` (plus 17
  `jtouch`, 1 `jstart`) — the exact prod signature. See "LSF reproduction" below.
- **Decision:** the mechanism is proven. Proceed to the holistic fix (Layer 1 backup
  starvation is the root; L2 idempotency/accept-when-moved is confirmed necessary since
  the churn is `jarchive bad job` with confirmed_dead=0). `report-storm-lsf` is the gate.

The prod failure (from live logs)
---------------------------------
After the operator resumes ~2000 suspended fast jobs behind a small limit group,
the manager cannot service RPCs fast enough. A runner does reserve → started →
its sub-second command finishes → it tries to report the final state → gets
`err="receive time out"` → reconnects, retries → the retry is rejected
`bad job (not in queue or correct sub-queue)` → `will need to be rerun`. One prod
runner did 99 jobs, 0 completions, 21 reconnects, 22 receive-timeouts; the manager
logged 910 `jstart: bad job` but only 291 `jarchive` total. Net: successful fast
commands are never recorded and re-run forever; `complete` never advances.

The reproducer
--------------
`TestReliable4ReportStorm`: real `serve()` with the **mock scheduler + a non-empty
RunnerCmd** so ALL server-side scheduling / limit / reserve-group machinery runs
exactly as in prod, while the scheduler's launched runners are inert (park), so the
only load is our M goroutines. N jobs across 4 sibling memory groups sharing ONE
count-limited limit group (like prod's `results_portal:2000`). Each of M real
`Connect()`ed "runners" tight-loops `ReserveScheduled → Started(os.Getpid()) →
touch loop → stop touching → Archive`, with a faithful `reportFinalState`-style
retry+reconnect and per-RPC outcome classification (accepted / badjob / must-reserve
/ receive-timeout / other). Optional big-DB confound (`WR_RS_DB`) opens a mutable
copy of a pre-generated big DB with backups on. Knobs: `WR_RS_JOBS/RUNNERS/LIMIT/
SECONDS/TTR_MS/CMD_MS/DB/PROFILE_DIR/STATUS/STATUS_MS`. Includes a CPU+mutex+block
pprof harness and a 200ms in-flight-blocking sampler.

Sweep results (every config: 0 badjob, 0 receive-timeout, 0 must-reserve, 0 churn)
---------------------------------------------------------------------------------
| N / M / DB                         | completed   | maxArchLat | max in-flight | verdict |
|------------------------------------|-------------|-----------|---------------|---------|
| 5000 / 50 / fresh                  | 5000/5000   | 23ms      | –             | no churn |
| 5000 / 200 / fresh                 | 5000/5000   | 68ms      | –             | no churn |
| 5000 / 500 / fresh                 | 5000/5000   | 135ms     | –             | no churn |
| 5000 / 1000 / fresh                | 5000/5000   | 217ms     | 80ms          | no churn |
| 50000 / 1000 / fresh               | 50000/50000 | 269ms     | 242ms         | no churn |
| 50000 / 1000 / fresh + 8 status    | 50000/50000 | 219ms     | 155ms         | no churn; status ≤200ms |
| 5000 / 500 / **10GB**              | 5000/5000   | **5.44s** | 5.34s         | no churn |
| 5000 / 1000 / **10GB**             | 5000/5000   | **9.72s** | 9.60s         | no churn |
| 50000 / 1000 / **10GB**            | 41686/50000 | **16.47s**| 16.46s        | no churn (throughput-capped in 300s) |
| 10000 / 1000 / **10GB** + 4 status | 10000/10000 | 10.01s    | 9.91s         | no churn; status ≤**147ms** |

Fresh-DB latency scales cleanly with contention (23→68→135→217ms for M=50→1000)
but stays ~300× below the 60s floor. The big DB is what produces multi-second
latencies (5–16.5s), and 16.5s was the worst single-RPC latency obtainable.

pprof (M=1000)
--------------
- **CPU:** dominated by TLS-socket `Syscall6` (28% fresh → 38% big-DB) + GC. The only
  app hot path is `Job.schedulerGroupSnapshot` — the O(backlog) rac re-scan — 6.5% at
  5k backlog rising to **18.9%** at 50k (CPU pressure, not a lock).
- **Mutex (the serialization point):** ~90–98% on the single **`queue` mutex** `Unlock`,
  via `handleReserve` (`setItemDelay`→`q.SetDelay` + `reserveWithLimits`→`q.Reserve`,
  ~70%) and `handleArchive`→`archiveCompletedJob`→`q.Remove` (~18%). These ops are
  O(log n), so per-op hold is short → waits stay sub-second.
- **Block:** under the big DB, report RPCs wait in `handleArchive` → bbolt `Batch`
  `sync.Cond.Wait` — i.e. behind the backup-stalled committer.

The pinned mechanism (what actually crosses 60s)
------------------------------------------------
The wall is `ClientMinRequestTimeout = 60s` (client.go:119): after connect, every
report's receive deadline is floored at 60s (`requestTimeout`), so a report only
times out if a single server RPC exceeds 60s. The ONLY thing that gets close is the
**periodic full-file DB backup starving the foreground committer**:

- `db.backupToBackupFile` → `db.bolt.View(copyBackup)` → `tx.WriteTo(w)` streams the
  ENTIRE DB file (6–10GB) to the backup copy. `backupCopyWriter.pace()` forces
  writeback every 8MB (SFR pipeline), which bounds the *dirty-page backlog* but NOT
  the *total I/O*: a full-file copy competes with the committer's `fdatasync` for
  storage bandwidth for its whole duration.
- Backups are triggered after DB-altering ops and throttled to at most once per
  `max(minimumTimeBetweenBackups=30s, lastBackupDuration)` (`finishBackgroundBackup`).
  So under a storm, backups run nearly back-to-back, and commits starve for a large
  fraction of the time.
- In-process on the test node's storage this maxed at **16.5s**. On real prod NFS the
  same stall was observed at **108s at 6GB** (`reinvestigation.md`) — i.e. it exceeds
  the 60s floor, and THAT is what triggers the mass receive-timeouts.

**The earlier queue-mutex hypothesis is refuted as the churn cause.** The queue mutex
IS the top contention point, but its holds are O(log n) → sub-second waits. Sharding
/ finer-grained locking / actor-batching the queue mutex would NOT fix the churn.
Likewise `wr status`/`AllItems` is NOT an O(backlog) stall (≤200ms even on the 10GB
DB; the complete-jobs read is per-rep-group indexed and `AllItems` is a cheap pointer
copy).

Why in-process can't cross 60s (the prod-only amplifiers)
--------------------------------------------------------
1. **Storage:** real prod NFS makes a full-file backup exceed 60s (108s at 6GB); the
   test node's faster storage capped the same stall at 16.5s.
2. **Shared pid:** all in-process runners share one live pid, so `confirmJobDead`
   never confirms dead → a spuriously-lost job parks in Run and its late archive is
   still accepted (current ttrCallback design). In prod, thousands of DISTINCT dying
   runner processes let `confirmJobDead` actually confirm death → the job leaves Run
   → the owner's late archive is then rejected `bad job` → discard + rerun.

So the reproducer is a faithful NEGATIVE CONTROL / regression guard: it will flip to
CHURN the moment any single RPC breaches the 60s floor. It cannot, by itself, prove a
fix against the real churn — that needs the LSF-scale harness (real NFS, real runner
processes): `wrdev.sh limit-drain` with fast jobs, `WRDEV_DEBUG=1`, `padKB~25`.

Secondary but real finding
--------------------------
The chunked-backup fix (`619c09c`) does NOT bound the stall to 0.7s under high
concurrency: 0.7s at 50 archivers (the old backup-stall test) becomes 5–16.5s at
M=500–1000, because hundreds of archivers pile up behind the stalled bbolt committer.
This is the single largest latency source and the closest anything came to 60s.

Three-layer diagnosis (the holistic fix must address all three)
---------------------------------------------------------------
1. **Overload / never-too-busy (the ROOT, proven):** the periodic DB backup must never
   starve foreground commits for more than a couple seconds regardless of DB size,
   storage speed, or archive concurrency. This is what crosses the 60s floor and
   starts the whole spiral. Most self-contained (db.go backup path) and the most
   in-process-validatable (measure the stall drop from 16.5s → <1s). Candidate
   directions: rate-limit/throttle the backup so it always leaves commit headroom;
   defer/yield the backup while archives are flowing; and/or reduce backup cost
   (frequency, or only-on-change). NB the 6–10GB DB size is itself a contributing
   factor (backup cost ∝ size) — completed-job retention may warrant its own look.
2. **Client delivery + server reconciliation (still a real gap at HEAD; f4b9b55
   reverted):** an owner's final-state report must be accepted in EVERY state
   (Run/Delay/Ready/Lost) at EVERY stage (start/touch/archive/release/bury) and be
   IDEMPOTENT — a retry after a timed-out-but-processed archive must return success
   (complete-bucket check), not `bad job` → rerun; a report for an item the manager
   moved (TTR fired) must be accepted by OWNERSHIP, not sub-queue; new-run-wins must
   still be enforced by the ownership check; and the Ready-state release path must not
   throw "not running" (the f4b9b55 regression). This is hard to validate in-process
   (needs the churn) but unit-testable with forced states.
3. **Don't false-lose an actively-reporting runner:** largely handled by the current
   `ttrCallback` (parks lost-in-Run, both-pid `confirmJobDead`), but worth hardening:
   a job whose runner is actively contacting (any received RPC) should not be
   confirmed dead, and `confirmJobDead`/`ProcessNotRunningOnHost` correctness under a
   failing SSH forced-command is operational (see memory).

LSF reproduction (2026-07-27) — the churn, replicated
-----------------------------------------------------
`developers/wrdev.sh report-storm-lsf [jobs] [limit] [runsec]` (defaults 100000 2000 1)
reproduces the prod churn faithfully. It supplies the two prod-only amplifiers the
in-process harness structurally lacked:
  1. real (NFS) storage + a big DB: isolated PROD-mode manager (backups ON) opened on a
     COPY of `/nfs/hgi/wr/sb10-bigdb/pristine10` (10GB);
  2. real LSF runners = thousands of DISTINCT pids.
Shape: N fast `sleep 1` jobs in ONE limit group (reprolimit:2000) across MEM_GROUPS
memory groups, so ~2000 real `wr runner` processes tight-loop reserve→start→touch→archive.

SAFETY: the isolated manager runs on port 51782 as user sb10; its LSF jobs are namespaced
`wrpiso51782_*` via the WR_JOBNAME_TOKEN hack (jobqueue/scheduler/{scheduler,lsf}.go),
which can NEVER match a real production manager's `wrp_*`. Cleanup only ever bkills that
namespace. (Proper multi-deployment fix: .docs/bugfixes/260727-1.md.)

Run (`report-storm-lsf 50000 2000 1`, WRDEV_PRISTINE_DB=pristine10) — churn appeared once
the first big backup froze the committer past 60s:

    t+123s complete=4467  running=1550 delayed=741 lost=7  LSF_RUN=1969 badjob=0     archive_reject=0
    t+153s complete=5486  running=1357 delayed=128 lost=7  LSF_RUN=1973 badjob=1080  archive_reject=1080
    t+184s complete=5903  running=1353 delayed=400 lost=7  LSF_RUN=1900 badjob=1588  archive_reject=1588
    t+276s complete=9061  running=585  delayed=0   lost=7  LSF_RUN=1941 badjob=3513  archive_reject=3495
    t+307s complete=10148 running=1972 delayed=0   lost=7  LSF_RUN=1981 badjob=4107  archive_reject=4089

Manager-log freezes (a gap > 60s crosses the client receive floor):

    GAP 122s ending T09:40:11   <-- 122s committer freeze during a 10GB backup
    GAP  46s ending T09:41:28
    GAP  35s ending T09:43:12
    GAP  30s ending T09:40:42

Exact rejection signature (identical to prod), from the manager log:

    4089  jarchive(<hash>): bad job (not in queue or correct sub-queue)
      17  jtouch(<hash>):   bad job (not in queue or correct sub-queue)
       1  jstart(<hash>):   bad job (not in queue or correct sub-queue)

Interpretation / what the fix must address (now evidence-backed, not hypothesised):
- The 122s freeze = the full-file 10GB backup starving the single bbolt committer on NFS
  (block profile already showed reports waiting in `handleArchive → Batch sync.Cond.Wait`).
  It far exceeds the 60s `ClientMinRequestTimeout`, so EVERY in-flight report times out.
- `confirmed_dead=0` throughout, yet `jarchive ... bad job` dominates ⇒ the churn is NOT the
  confirm-dead/release path here; it is the **idempotency / item-moved path**: the runner's
  archive is blocked behind the frozen committer past 60s → the client reconnects and RETRIES,
  but by then the original archive has committed+removed the job (or the 60s TTR moved the
  item), so the retry gets `bad job` and the client re-runs already-successful work. `jtouch`
  and `jstart` show the SAME across-stage failure the holistic fix must cover.
- `report-storm-lsf` is the GATE: healthy code must drain it with ~0 `jarchive bad job` and no
  freeze > a few seconds. It reproduces at 50k/2000; raise the DB size or `limit` to make the
  freeze longer / the spiral deeper.

Fix strategy / recommendation
-----------------------------
Layer 1 is the proven dominant cause, is self-contained, and is the only layer the
in-process reproducer can measurably validate (stall drop). Layers 2–3 are
defense-in-depth that make a delayed report never discard completed work even if a
stall does occur, and they interact with reliable2/reliable3 boundaries and prior
fixes (regression risk).

- **Fastest prod relief:** /bugfix Layer 1 now (make the backup never starve commits;
  validate the in-process stall drops from 16.5s→<1s). This alone likely stops the
  60s-crossing that starts the spiral.
- **Holistic (user asked for "do them all"):** /spec-writer sequencing L1 (relief) →
  L2 (reconciliation/idempotency across all stages+states, done properly this time) →
  L3 (false-lost hardening), with a validation plan that combines the in-process
  regression guard with LSF-scale `limit-drain` on real NFS.
