# Production validation of the dep-granularity work, 2026-08-27

**STATUS: the change works in production, and a deliberate stress test then
found the NEXT ceiling.** Recovery went from 42m56s to 36.4s and peak RSS from
~181 GB to 7.84 GB, with no OOM and no crash. Raising a job limit from 20 to 2000
(1,143 concurrent runners) held memory fine but drove archive latency to the 60 s
client floor and lost **470 final-state reports in one minute**. So the memory
work is done; **write-path throughput is now the binding constraint**.

Measurement has since ruled out the two explanations that would have made this a
hard limit: **fsync latency is 0.73 ms**, and free pages are only 10.9%, so
neither durability nor freelist size accounts for a 70 ms transaction - and the
same code reaches 172/s on local disk. Roughly 68 ms per transaction remains
unexplained and needs a production profile to decompose.

Two design directions are being explored in `throughput-architecture.md`; a
**regression sweep of 26 wrdev.sh gates found no regression** from this delivery.

Read this first for the current position. Background: the OOM diagnosis is in
`prod-restart-260825.md`, the design in `.docs/dep-granularity/spec.md`, and each
phase file's "Phase N outcome" section carries corrections to that spec rather
than just status.

## What was deployed


`wr v0.37.1-77-gfb5df01` on farm22-ibackup01, started 2026-08-27 07:36:26 by the
operator. Seven commits, each implemented, independently reviewed and committed
separately:

| commit | change |
| --- | --- |
| `b967dbb` | `queue.SatisfyDependency`; a dependency key may name a group, not an item |
| `3390f23` | sharded `depGroupMembers`; `dependencyKeys` replaces member expansion; `bucketDTK` no longer written |
| `93d106b` | recovery rebuilds membership, archive releases it; per-pass seen-group cache |
| `0180128` | add/modify/delete hooks keep membership current |
| `f2ccaee` | publish the listener only once recovery completes, plus the whole startup series |
| `0b24478` | memory, transaction-count and scale gates |
| `fb5df01` | startup-ordering gate; `max(1, chunkSize)` guard; dead status call removed |

Two operator actions matter for reading the numbers below: the DB was **compacted**
before this run (15.03 GB at 45.9% free pages -> 7.3 GB), and `db_predep-granularity`
was kept as a pre-deployment copy.

## Startup, measured


These are the phase lines this delivery added (section E9), at warn so they appear
in an ordinary manager log with no `--debug`:

| phase | elapsed |
| --- | --- |
| opened database | 924 ms |
| decoded live jobs (151,280) | **34.664 s** |
| built dependency-group state (112,486 memberships) | 318 ms |
| resolved prior job dependencies (151,280) | **95 ms** |
| enqueued prior jobs | 286 ms |
| **07:36:26 -> 07:37:02 total** | **~36.4 s** |

The comparison, from `prod-restart-260825.md`: **42m56s** to recover 150,472 jobs,
after which the manager served for four minutes and was OOM-killed.

**Dependency resolution is 95 ms.** That is the same work that previously took
tens of minutes and held 97.55% of a 15.65 GB heap. The 112,486 memberships that
used to be expanded into per-job key lists are now built once, in 318 ms.

## Memory, measured


```
VmHWM:  7844984 kB
VmRSS:  7844984 kB
```

`VmHWM == VmRSS`, so it rose to 7.84 GB and stayed flat - no spike to hide. The
five OOM kills were at 180,537,676 / 181,374,128 / 181,400,708 / 174,834,676 kB
on a 182.7 GB node.

Most of the 7.84 GB is the mmap'd 7.34 GB database, which is file-backed page
cache rather than anonymous memory (the node showed 126 GB in buff/cache and
168 GB available), so the Go heap is a fraction of a gigabyte. The figure to
compare it against is the old **140 GB live heap with a 218 GB GC goal** - above
physical RAM, which is why the kill was arithmetically certain.

## Also confirmed working in production


- **Publication after recovery.** The pid file appeared at 07:36, `client.token`
  and `manager.addr` at 07:37 - the token is now written at publication, so
  nothing outside could reach a half-recovered manager.
- **Default-level startup progress.** Every phase above is visible without
  `--debug`. This was the operator's original ask in `.docs/bugfixes/260825-1.md`,
  after a 15-minute recovery that logged nothing but client errors and read as
  total job loss.
- **`wr manager v0.37.1-77-gfb5df01 started on ...`** is logged *after*
  `prior state recovered`, which is the ordering the `fb5df01` gate now enforces.

## Limit stress test, 07:58-08:08 - the next ceiling, measured


The operator raised a job limit from **20 to 2000** at ~07:58 to stress the new
build. LSF filled it to **1,143 concurrent runners** (wr's own `wrp_*` jobs; the
~22,500 other pending mercury jobs in `bjobs` are unrelated `gb-cram-*` work, and
wr's own pending runners peaked around 840 - **no over-provisioning**, so
\#553/\#554 are still holding).

### Throughput did not scale, then degraded

Completions per minute, counted from `msg="command ran OK"` across all runner
logs in `/nfs/hgi/wr/lsf/runner_logs/26.08.27/`:

| period | limit | concurrent runners | completions/min |
| --- | --- | --- | --- |
| 07:38-07:57 | 20 | ~20 | **~500-550, steady** (448-710, mean ~520) |
| 07:59-08:04 | 2000 | 690 -> 1,143 | 984, 2072, 833, 439, 1101, 640 |

**57x the concurrency bought ~1.6x the throughput, and made it erratic.** The 439
in the 08:02 minute is *below* the old steady rate while running 1,143 runners
instead of 20. That is saturation, not capacity: the runners queue on the single
serialized DB write path rather than doing more work.

### Latency walked up to the client floor and pinned there

Max `slow request` duration per minute (threshold is
`slowRequestThresholdDefault = 10s`, `serverCLI.go:82`):

| minute | slow reqs | max duration |
| --- | --- | --- |
| 07:58 | 63 | 11.0 s |
| 07:59 | 1633 | 29.4 s |
| 08:00 | 2568 | 19.6 s |
| 08:01 | 1593 | 47.7 s |
| 08:02 | 1187 | 50.8 s |
| 08:03 | 1817 | 51.6 s |
| 08:04 | 1557 | **59.9 s** |
| 08:05 | 1688 | 59.6 s |
| 08:06 | 1633 | **59.9 s** |
| 08:07 | 851 | **60.0 s** |

`ClientMinRequestTimeout` is **60 s** (`client.go:120`). A grep for durations
>= 60 s returns **zero**, and that is not reassurance - it is the signature of
the ceiling being enforced from the client side. The server cannot log a longer
duration because the client has already given up. Distribution over 08:04-08:07:
103 requests at >= 59 s, 2,429 at 50-59 s, 1,168 at 30-50 s, 2,029 under 30 s.

### It did break, and here is the evidence

The runner logs carry **470 x `msg="failed to update server with cmd's final
state" err="receive time out"`**, and **all 470 fall in the single minute
08:06** - the moment the plateau reached the floor. They arrive together because
they were all queued behind the same write path and all hit their 60 s deadline
at once.

That is completed work whose report was lost: the command ran fine, the archive
RPC timed out. It is the precursor to the discard-and-rerun churn of
`prod-restart-260825.md` - the manager still believes those jobs are running, so
TTR expiry releases and re-runs them. Against 18,690 total completions, 470 is
**2.5%** of the run's reports lost in one minute.

Manager-side there was exactly one other error in the whole run,
`jobqueue add(): bad request (missing arguments?)` at 08:00:25 - a single
instance, the kind of malformed request that shows up when clients time out
mid-send.

The operator dropped the limit back to 20 on this evidence; runners drained
promptly and nothing crashed.

### What this tells us, and what it does not

**Holds up under 57x the validated load:** memory (no OOM, no growth), the
dependency machinery (no dependency-related error in 18,690 completions), and the
manager's stability (no crash, no restart, no wedge). The dep-granularity work is
not what breaks here.

**The new binding constraint is write-path throughput**, and issue 3 is its
proximate cause: a continuous full 7.3 GB backup copy competing with the archive
path for the same NFS I/O, with archives serialized behind it.

**Acceptance test for the issue-3 work:** sustain `limit 2000` (~1,000+
concurrent runners) with archive latency well clear of 60 s and **zero** "failed
to update server with cmd's final state" in the runner logs. That is a sharper,
production-grounded gate than any synthetic threshold, and it is reproducible on
demand by raising the limit again.

Two caveats on the throughput numbers, so they are not over-read. Completion rate
is counted from runner-side "command ran OK" lines, which record the command
finishing, not the archive succeeding - so the post-change figures include work
whose report was subsequently lost. And the pre-change baseline is a 20-minute
window on one workload mix; it is a solid steady-state figure but not a
controlled A/B.

## wrdev.sh regression sweep - no regression found


26 gates run against the delivered code, at load 119-121 on 8 cores. Every gate
that asserts a fixed invariant passes, with wide margins, including all four that
exercise the rewritten startup path.

**Startup-semantics evidence specifically**, which is where a regression from this
delivery would have shown:

- `crash-recovery` (real LSF, prod mode): manager killed mid-run, restarted on the
  preserved DB while its runner survived; the re-sent archive was **accepted**,
  `complete=1`, `marker=1` (ran exactly once). Publication-after-recovery does not
  break runner reconnect.
- `dep-granularity-check` samples `wr manager status` *inside* the recovery window
  and got `starting` in 45 ms - the harness copes with the new ordering.
- `control-rpc-history` and `unsuspend-burst` both bring prod-mode managers up on
  large DBs through `cmd_prod_start` (a 90 s-bounded `manager start` that greps for
  `started on`) with no timing trouble.
- `limit-drain 20000 2000 30`: **fully drained**, concurrent runners peaked
  1957-2000 without exceeding the limit, and `lost`, `badjob`, `confirmed_dead` and
  `archive_reject` were **all 0**.
- `churn 40000` (real LSF): fully drained 40000/40000, `badjob=0`, `notrun=0`,
  status RPC 29-64 ms.

### Three exit-1 results, all pre-existing, none a regression

Each was A/B'd against a worktree at `a083f1d` and behaves identically there.

- **`overcount-check`** and **`limit-stall-check`** are *inverted* reproducers -
  their own comments say they pass on buggy code. `overcount-check` now reports
  `finalCount=2000 (exceeds limit by 0)`, so its `ShouldBeGreaterThan` fails
  because the bug is fixed. `limit-stall-check`'s `SilentConfirmFailure` half fails
  because the code now *logs* the warning whose absence it asserts. Both are the
  desirable outcome.
- **`priority-fairness-check` exits 0, and that is not good news.** It is also
  inverted, and it still demonstrates the reliable3 2a starvation
  (`low(pri0).count=2000 high(pri250).count=0 high.skipped=2500`). Unchanged by
  this delivery; do not read its zero exit as an invariant holding.

### A harness trap worth knowing

Putting `WRDEV_ROOT` on `/tmp` **silently disables every LSF gate that needs a
real runner**, with no error line saying why. `/tmp` here is local `/dev/vda3`, so
no exec node can read the wr binary: runners reach RUN and die with
`Exited with exit code 127` in under a second, which is invisible between `bjobs`
polls. `crash-recovery` failed twice this way before being re-run on NFS, where it
passed in 34 s. This is the DEVELOPERS.md "build on NFS so exec nodes can run the
runner" rule, and the disk-pressure advice to use `/tmp` collides with it.

### Not measured, and what it would take

- **`report-storm-lsf`** and **`backup-stall-check`** - the only two gates needing
  a multi-GB DB *and* real LSF runners, so both the DB and the binary must sit on
  an exec-visible filesystem. `/tmp` is local, home had under 2 GB free, and
  creating a working area under `/nfs/hgi/wr/` was denied. Needs ~25 GB of
  exec-visible scratch.
- **`limit-drain` at its default 60000** - run at 20000 (10:1 rather than 30:1) to
  keep one shared-farm saturation window to 9 minutes. It did reach the full 2000
  concurrent runners and drained clean; the untested delta is a *longer* saturation
  window, which is where the original prod stall appeared. ~27 min of 2000 farm
  slots would close it.
- **`web-burst`** - manual verdict, fixed 900 s window; the same failure family is
  covered deterministically by `flicker-check` (PASS).
- The four big-DB gates used **`pristine6`** (7.4 GB, freelist 3202 MiB) rather
  than `pristine10`, so a fixture plus its per-gate copy fit in `/tmp`. Per the
  known false-PASS trap it was used only for backup/freelist/write-storm/archive
  work; `control-rpc-history` seeded its own DB.

### Two incidental pre-existing defects found

- `wrdev.sh dump` prints a stale pid, because a `-f` foreground manager writes no
  pid file - so `wrdev.sh stop`/`clean` cannot kill a `dump`-started manager.
  Identical at `a083f1d`.
- `parseBmgroups` (`jobqueue/scheduler/lsf.go:1491`) starts `bmgroup -w` and never
  calls `Wait()`, so every manager leaves a defunct `[bmgroup]` child. Present at
  both commits.
## Remaining issues


### 1. Decode is now the entire startup cost

34.664 s of the 36.4 s window - 95%. It has nothing to do with dependencies any
more; it is reading and decoding 151,280 job records from a 7.3 GB DB.

This is the only lever left if faster startup is wanted, and it would mean a
batched or parallel decode. Note the measurements doc
(`.docs/dep-granularity/startup-window-measurements.md`) bounds the *other*
phases at synthetic scale but explicitly does not bound `initDB` or decode at
production scale - this run is the first production figure for decode.

**Recommendation: leave it.** 36 s is a perfectly good startup, and a parallel
decode touches the recovery path this work has just stabilised.

### 2. A thundering herd at publication

57 `add jobs=10` requests took 10.1 s to 15.7 s each. All of them *started* at
the publication instant (they are logged 07:37:12-07:37:17 with durations of
10-15 s), and the durations climb monotonically, which is queueing rather than
individual slowness.

This is a **direct consequence of the new design**, not a defect in it: clients
that would previously have trickled in and collected `ErrRecovering` now all
arrive at the same moment, because the manager becomes reachable atomically. They
had been waiting anyway, so nothing is lost, and 15.7 s is far under the 60 s
`ClientMinRequestTimeout` floor.

Worth watching rather than fixing: the herd scales with how many clients are
blocked during the window, and the window scales with the job count. If a future
recovery is slower, or more clients are waiting, this could approach the floor -
at which point clients start retrying and the churn dynamics of
`prod-restart-260825.md` become reachable again. `replyJobs=0` on all 57 suggests
they were duplicate adds, so the work itself was cheap; the cost was contention.

### 3. Continuous full-DB backup, causing the only other latency

This is the top *confirmed* latency source at ordinary concurrency. Whether it is
also what caps throughput under load is **not established** - see "Which cause?
Not settled yet" before treating it as the fix for the stress test.

```
db_bk      7,283,757,056  07:42:33   (finished)
db_bk.tmp  3,675,652,096  07:43:32   (already half-written)
```

Back-to-back full 7.3 GB copies - roughly 80 MB/s to NFS, indefinitely, because
the dirty-flag ticker refires as soon as jobs keep completing. The two slow
`jarchive` requests in the log (11.2 s at 07:40:15, 13.7 s at 07:41:25) sit
inside that copy window.

This is the **unfixed "Part C copy-I/O relief"** recorded in the
backup-coordination work: `f51af04` stopped the backup *serialising* the archive
hot path, but the copy itself is still a raw `tx.WriteTo` of the whole file, and
archives pay latency while it runs.

Context for how bad it is now, against `prod-state-260824.md`: **220 slow
jarchives per 28 minutes, 20 of them past the 60 s floor**. This run: 2 in 5
minutes, worst 13.7 s, none near the floor. Most of that improvement is the
operator's compaction (15 GB -> 7.3 GB) rather than code. But it is now the top
remaining latency source, and unlike the old behaviour it is *perpetual* rather
than occasional.

A prototype already exists and was measured: incrementally fsyncing the backup
copy every ~32 MB took a 10 GB DB's freeze from 15.8 s to 0.7 s, size-independent
(`reliable4-backup-stall-reinvestigation`). That is the starting point, not a
blank page.

## Which cause? Not settled yet


An earlier revision of this document treated issue 3 (continuous full-DB backup
copy) as the cause of the stress test's latency. **That was a leap, and it should
not be acted on as fact.** A competing explanation fits the same evidence and
implies different work.

### The observation that raises the doubt

The completion rates look like a hard ceiling, not a gradual degradation:

| limit | runners | completions/s |
| --- | --- | --- |
| 20 | ~20 | **8.7/s** |
| 2000 | 1,143 | **~14/s**, then erratic |

Twenty runners were already at ~60% of the rate that 1,143 runners could reach.
Now compare `prod-state-260824.md` and the `archive-rate` gate's recorded
production figure: **queue ~600 deep, ~12/s, mean block 43000 ms** with 660
concurrent archivers. Nearly the same number - and that gate measures the archive
path **in-process, with no manager and no backup running at all**.

One synchronous bolt write transaction per archive, each fsyncing to NFS at
roughly 70 ms, serialized, *is* about 14/s. If that is the mechanism, relieving
the backup copy barely moves it.

### Direct measurements, 2026-08-27: durability latency is NOT the constraint

Two cheap probes, taken against the live filesystem and the live DB:

```
fsync, 4 KB, 30 samples:
  /nfs/hgi (holds the prod DB)   median 0.73 ms   p90 0.84 ms   max 1.07 ms
  /tmp     (local /dev/vda3)     median 0.26 ms   p90 0.34 ms   max 0.53 ms

boltfree on the live db (8.59 GB, txid 35888):
  free 228908 pages (0.94 GB) = 10.9% of the file
```

A bolt commit costs two fsyncs, so durability is **~1.5 ms** of a **~70 ms**
transaction. The freelist is ~1.8 MB per commit at 228,908 free pages - real, but
not 70 ms either. **So there is no fundamental durability floor at 14/s**, and the
framing "database write speed is inherently limited" does not survive contact with
the numbers: the same code reached 172/s on a 7.4 GB DB on local disk.

Roughly 68 ms per transaction is unaccounted for and **cannot be decomposed from
outside the process**. The leading suspects are NFS *bandwidth* contention with
the continuous 7.3 GB backup (81 MB/s sustained) and scattered dirty-page writes
across a large tree. Note the fsync probe does not refute the bandwidth theory:
fsync latency and write bandwidth are different quantities, and a 4 KB probe
measures the former only.

### Sweep evidence, 2026-08-27: candidates 2 and 3 largely refuted

Two gates from the regression sweep bear directly on this, and both point away
from "the archive path is inherently ~14/s".

| measurement | archivers | rate | mean | DB | filesystem | backups |
| --- | --- | --- | --- | --- | --- | --- |
| `archive-rate` gate | 660 | **172/s** | 41 ms | pristine6, 7.4 GB, 3202 MiB freelist | **local** `/dev/vda3` | on |
| `limit-drain 20000 2000 30` | ~2000 runners | **~67/s** sustained | - | small, fresh | **NFS** (`$HOME`) | off (dev mode) |
| **production, 08:00-08:07** | 1,143 runners | **~14/s** | - | 8.5 GB, growing | **NFS** | **on, continuous** |

- **172/s on a 7.4 GB DB with a 3.2 GB freelist** says the archive code path, the
  write lock and freelist/spill cost are *not* the ceiling. That substantially
  refutes **candidate 3**, and refutes the structural half of **candidate 2**.
- **~67/s across NFS** (2000 runners on 30-second jobs, drained clean with
  `archive_reject=0`) says one fsync per archive over NFS is *not* a 14/s ceiling
  either. That refutes the rest of **candidate 2**.
- Production differs from both by exactly two things: a **continuously copied
  7.3 GB backup**, and a **big DB on NFS**.

So **candidate 1 is now the leading explanation**, with a sub-variant worth keeping
separate: big-DB-on-NFS effects that neither gate covers, since `archive-rate` had
the big DB but not NFS, and `limit-drain` had NFS but not the big DB.

**Be careful how far this is pushed.** No single gate isolates one variable - the
inference is by elimination across two measurements with *different* confounds,
which is weaker than one controlled A/B. `archive-rate`'s fixture copy lived on
local disk, so it says nothing about NFS fsync latency; `limit-drain` used a small
dev-mode DB with backups off, so it says nothing about big-DB or backup effects.
Nothing yet reproduces production's actual combination of big DB **and** NFS
**and** continuous backup. That combination is only available in production, which
is why the next measurement has to be taken there.

### Three candidates, three different fixes

1. **Backup copy I/O competing for the same NFS bandwidth.** Fix: incrementally
   fsync the copy - already prototyped, 15.8 s -> 0.7 s on a 10 GB DB,
   size-independent.
2. **One fsync per archive, serialized on the single write lock.** Fix: coalesce
   multiple archives per commit. Materially harder, because archives are
   synchronous *by design* - the client is waiting for durability, so batching
   trades latency for throughput and needs a correctness argument.
3. **Freelist/spill CPU on a growing DB** (7.3 -> 8.5 GB during this run). Fix:
   compaction cadence. Note `NoFreelistSync` was already tried and **dropped**
   for breaking the fast-startup invariant - do not revisit it without reading
   why.

### What the logs can and cannot settle

The pre-stress evidence supports candidate 1 *at low concurrency*: slow
`jarchive`s at 07:40-07:57 arrived roughly one per few minutes, 10-22 s, clustered
in backup windows. But at 1,143 runners that evidence cannot distinguish "the
backup is stealing I/O" from "the write path was always ~14/s and 1,143 runners
now queue on it". The backup windows also cannot be reconstructed retroactively -
only the current `db_bk.tmp` size and `db_bk` mtime are observable, so there is no
way to go back and check whether latency fell in the gaps between copies.

### How to settle it

The in-process route has now been taken and did not settle it - it eliminated two
candidates rather than confirming one (above). **The remaining question is
production-only**, so measure there:

1. **Restart with `WR_PPROF_ADDR` and raise the limit again.** Recovery is now 36 s,
   so a restart is cheap. Capture CPU + mutex + block profiles during saturation.
   The discriminating question is where the archive goroutines sit: `fdatasync`/NFS
   write, versus freelist/spill CPU, versus waiting on `db.rwlock`.
2. **In the same run, correlate archive latency against backup progress.** Sample
   `db_bk.tmp`'s size every couple of seconds and line the copy windows up against
   the manager log's slow-request timestamps. **If latency falls in the gaps
   between copies, candidate 1 is confirmed directly** - and that needs no profile
   at all, just sampling during the run. This could not be done retroactively for
   the 07:58-08:08 test because only the current `db_bk.tmp` is observable.
3. **The decisive config experiment, if a filesystem is available:** point the
   backup at a *different* filesystem (the `report-storm-lsf` mode already supports
   this idea via `WR_RS_BKDIR`, "back up to a separate filesystem so it can't
   starve the DB's I/O"). If the ceiling moves, candidate 1 is proven and Part C is
   the fix. If it does not, the cause is the big DB on NFS, and the lever is
   compaction cadence rather than copy pacing.

**Observer-effect warning:** pprof sampling on this manager perturbs what it
measures. Use the sampling tiers recorded in the prod-profiling notes rather than
continuous capture, or the profile will report the cost of profiling.

## Things not to "fix"


- **`wr manager status` dies with "could not read token file" for the whole
  startup window on the daemonized path.** Operator decision, 2026-08-27: this is
  expected, not a bug - status is only expected to work once `wr manager start`
  says the manager has started. Making the pid-file branch consult the sidecar
  would undo the entire point of publishing late.
- The green window in `3390f23` is deliberate: ten test functions are red at that
  commit by design. Do not bisect through it expecting a green suite.

## Next steps


### Done

- **The wrdev.sh regression sweep** - 26 gates, no regression; see the section
  above.
- **The in-process route to identifying the cause** - taken via the `archive-rate`
  gate. It eliminated two of the three candidates rather than confirming one.
- **The direct filesystem and freelist probes** - fsync latency and free-page
  count are both ruled out as the explanation (see above).

### Open, in order

1. **Design the throughput fix.** Two approaches are being explored in
   `throughput-architecture.md`: **group commit** (batch archives into shared bolt
   transactions) and **splitting the live set from the archive**. Group commit is
   worth doing regardless of how the remaining ~68 ms decomposes, because batching
   divides *every* per-transaction cost by the batch size - it is robust to the
   open question below.
   **Ruled out by the operator (2026-08-27):** moving the DB to local disk, and
   relying on compaction cadence or a separate backup filesystem. Those may still
   be measured as diagnostics, but nothing is to depend on them.
2. **Profile production when the operator can restart it** (deferred - a restart
   is not available right now). Capture CPU + mutex + block during saturation with
   the limit raised, *and* sample `db_bk.tmp`'s size every couple of seconds so
   archive latency can be correlated against backup copy windows. That answers
   where the ~68 ms goes and whether the split in idea 2 must also move the DB off
   NFS. **This document gets updated with the result.**
3. **Watch the publication herd** (issue 2) across a few more restarts. No code
   needed today; what would change that is a herd approaching the 60 s floor.
4. **Leave the decode cost** (issue 1) unless startup time becomes a complaint.
5. Close out the remaining recorded items in `.docs/bugfixes/260825-1.md`
   (items 2-4), several of which became moot now that recovery completes before
   any client can connect.
6. Two pre-existing defects the sweep turned up, both minor: `wrdev.sh dump`
   leaves a manager that `stop`/`clean` cannot kill (no pid file under `-f`), and
   `parseBmgroups` never `Wait()`s its `bmgroup -w` child, so every manager leaves
   a defunct process.

