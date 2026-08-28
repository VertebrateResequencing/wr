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
same code reaches 172/s on local disk.

**Then a reproduction attempt moved the question.** With production's own
ingredients - the big DB, on NFS, with the continuous full-DB backup streaming at
its own 80 MB/s - the archive path does **364/s**, not 14/s, and no archive comes
within 12x of the client floor. Mutate it to **one bolt write transaction per
archive** and it reproduces all three production symptoms exactly (21/s, mean
archive 70 s, 1,827 reports past the floor). But **group commit already landed at
`f7e36bc` and is in the deployed `fb5df01`** - so production is running the code
that measures 364/s and behaving like the code that measures 21/s. See "Group
commit is already in production" and the two sections after it; the leading
hypothesis is that something upstream of `db.archiveJob` serialises completions so
the coalescing writer only ever has one archive to fold.

Two design directions are being explored in `throughput-architecture.md`; a
**regression sweep of 26 wrdev.sh gates found no regression** from this delivery,
and the three gates whose verdicts had become misleading are now fixed.

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

### Three exit-1 results, all pre-existing, none a regression - now resolved

Each was A/B'd against a worktree at `a083f1d` and behaved identically there. All
three were *inverted* reproducers: they asserted the presence of a reliable3 bug,
so their exit codes had come to mean the opposite of what a reader assumes. Two
have been converted into regression gates and the third now says what its exit
code means:

- **`overcount-check`** asserted `finalCount > limit`, so it passed on the buggy
  code and started failing when the cap landed (`finalCount=2000`, exceeds by 0).
  It now asserts the invariant - the summed count stays within the limit - and
  also asserts that the arrangement really does over-count *before*
  `capGroupCountsToLimits` trims it, so it cannot pass vacuously.
- **`limit-stall-check`**'s `SilentConfirmFailure` half asserted the *absence* of
  any log. It now asserts both warnings are present and name what an operator has
  to act on (the host and pid; the key path). Its `LimitSlotStall` half always
  asserted correct behaviour - a limit group whose slots are all held schedules
  nothing more - so only its framing changed.
- **`priority-fairness-check` exits 0, and that is not good news.** It still
  demonstrates the reliable3 2a starvation
  (`low(pri0).count=2000 high(pri250).count=0 high.skipped=2500`), so it stays
  inverted, because the defect is open and a permanently-red gate stops being
  read. It now prints a banner saying that its zero exit means the bug reproduced,
  and exits non-zero only when nothing was measured. See the open item below.

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

## The completion path, read - and a reframe (2026-08-27)

A static read of the whole completion path, plus a fuller read of the production
log, **demotes the dep-group hypothesis and produces a much better candidate**.

### `add` is hit harder than `jarchive`, which rules out most suspects

Of 15,042 `slow request` lines: **8,637 are `add`, 6,403 `jarchive`**, co-timed
minute for minute through the saturation window. `add jobs=1` alone: n=5,785,
**p50 43.3 s, p90 65.5 s, max 213 s**, with **1,577 adds at or past 60 s** against
567 jarchives past 59 s.

And the cost is **size-independent**: `add jobs=37651` took 20.4 s while
`add jobs=1` took 43 s at the median; on a quiet manager `add jobs=59489` took
53 s, while under saturation `add jobs=4481` took 3m13s.

`add` never touches `getijForReport`, `q.Get`, `db.archiveJob`, `q.Remove` or
`satisfyEmptiedDepGroups`. **So whatever serialises is shared with `add`**, which
demotes every archive-specific suspect - including `satisfyEmptiedDepGroups`, the
hypothesis this read was commissioned to test.

Corollary worth stating: **the 14/s rate is a consequence of the latency, not an
independent cap.** 1,143 runners x (2.3 s job + ~60 s archive) is about 18/s,
which is production's ~14/s. The only question is why *every* request takes tens
of seconds.

### Leading candidate: bolt's `mmaplock`, and compaction is what exposed it

`db.backupToBackupFile` (`db.go:4246`) wraps the entire 7.3-8.7 GB copy in **one**
`db.bolt.View`. A bbolt read transaction holds `db.mmaplock.RLock()` for its whole
life; `db.allocate` calls `db.mmap` whenever the freelist cannot satisfy a write,
and `db.mmap` takes `db.mmaplock.Lock()`. bbolt's own comment: *"When the mmap is
remapped it will obtain a write lock so all transactions must finish before it can
be remapped."* Go's `RWMutex` then queues every new reader behind that pending
writer.

**Consequence: the whole DB freezes for up to the remainder of the ~100 s copy**,
hitting `add` (`bolt.Batch`) and `jarchive` (`bolt.Update`) equally, for a duration
independent of request size. That fits every number above, including the 213 s
maximum.

**Why no harness reproduced it, and the irony in that:** `pristine6` carries a
3,202 MiB freelist (819,745 free pages), so `db.mmap` is never called and the
freeze cannot occur. Production was **compacted on 2026-08-25** to 10.9% free and
has been **growing** since (8.591 GB at 13:23 to 8.678 GB at 14:26 on 2026-08-27,
`db_bk` rewritten every ~2 minutes). The compaction that cut backup volume by
45.9% also removed the free-page slack that had been letting writes avoid growing
the file. This is the one ingredient the hunt never varied.

**How to kill or confirm it:** re-run `archive-ceiling` against a *compacted* copy,
so the archive path must grow the file while the backup's read transaction is open.
No production access needed.

### Other candidates worth acting on regardless

- **The `add` path demands 3-6 bolt write transactions per request**
  (`storeLimitGroups`, plus 2-5 concurrent `bolt.Batch` goroutines from
  `storeNewJobData`). `bolt.Batch` only coalesces callers arriving before the
  current batch starts, and the archive writer's plain `Update` cannot join a
  Batch at all. At ~1,000 slow adds/min that is ~100 write transactions/s demanded
  against ~14/s available.
- **`decrementGroupCount` runs the LSF scheduler synchronously inside the archive
  RPC** (`server.go:7081` calls `scheduleRunners` directly, *not* in a goroutine -
  contrast `scheduleGroup` at `server.go:5271`, which spawns one precisely
  "because the external scheduler command (eg. bsub) can be slow"). That reaches
  `parseBjobs`, which runs **`bjobs -w` over all the user's LSF jobs with no
  timeout and no context** (`scheduler/lsf.go:2053`). Mercury's LSF had 22,500+
  foreign pending jobs at 08:00. A direct route to individual jarchives crossing
  the floor; it does not explain the `add` latency.
- **`limiter.vivifyGroup` holds the limiter's exclusive mutex across a bolt read
  transaction** (`limiter/limiter.go:212` -> `db.retrieveLimitGroup`) - a rule-1
  violation on a lock every completion needs via `markJobComplete` -> `Decrement`.
- **Two server-wide exclusive locks are already taken once per completion**:
  `queue.mutex` via `q.Remove`, and `s.rpl`. Rule 2's prohibition on *adding* one
  is operating against a path that has two, plus `waitgroup.WaitGroup.mu` twice
  per RPC.
- **`AddMany` holds `queue.mutex` across the whole batch** (`queue.go:848`) - N
  item constructions, map inserts, dependency wiring and heap pushes under one
  hold. Production logged `add jobs=37651` and `add jobs=59489`, so this is a
  genuine stop-the-world, though only ~2 such adds appear in the log.
- **`processTimedOutItems` holds `queue.mutex` across the whole TTR pass**
  (`queue.go:1918`), calling `ttrCallback` -> `markJobLost` -> two
  `statusCaster.Send`s under the write lock, with `ServerItemTTR` = 60 s and 1,143
  running jobs whose touches are queued behind it. Periodic and self-amplifying.

### New instrumentation, landed

`9fbccb4` adds a periodic warn-level `archive fold` line reporting the fold
achieved plus the **wait/transaction split**, so "the writer is starved" and "the
writer is slow" are distinguishable from an ordinary production log:

```
msg="archive fold" txs=94 archives=20458 meanFold=217.64 maxFold=423
  meanWait=342ms maxWait=922ms meanTx=634ms maxTx=923ms meanLock=0s maxLock=0s interval=1m0s
```

Proven to discriminate on production's own filesystem with the backup streaming:
fold **217.64** on this code, versus **1.00** with coalescing mutated away, where
`meanWait` climbs to **1m17.8s** against a `meanTx` of 78 ms. So a production line
is readable against both regimes. A fold of ~1 with a *small* wait means arrivals
are merely sparse - which is why the split had to be on the same line.

## Three mechanisms, separated by experiment (2026-08-27)

A controlled A/B on `/nfs/hgi` (production's own filesystem) plus a 16-minute
read-only sample of the live manager separated **three independent mechanisms**.
Evidence in `/nfs/hgi/wr/sb10-wrdevtest/mmaplock/`.

### Mechanism A: the add path's lock contention - CONFIRMED, and it matches the workload

The operator supplied the missing workload shape: **the portal dedup jobs are fast
and each one `add`s compress jobs, which depend on all the dedup jobs finishing
first.** So adds are interleaved roughly 1:1 with completions, from the runners
themselves, and one huge dep group is where the 112,486 memberships come from.

Reproduced by interleaving one `storeNewJobs` per archive:

| | archives | p50 | max |
| --- | --- | --- | --- |
| archives only | **357/s** | 897 ms | 1,893 ms |
| archives + interleaved adds | **150/s** | 2,497 ms | 5,792 ms |

**Adds halve throughput and multiply latency 2.8x, hitting `add` and `jarchive`
equally, and it is entirely independent of freelist slack** (identical in both
arms). The fold line names the mechanism: `meanLock=1.421s maxLock=3.9s` - the
archive writer spends most of each transaction **waiting for bolt's write lock**,
held by the add path's `bolt.Batch`. The add path costs 3-6 write transactions per
request and the archive writer's plain `Update` cannot join a Batch.

This explains the log ratio directly: 8,637 slow `add` against 6,403 slow
`jarchive` is not two problems, it is one contention with more requests exposed on
the add side.

### Mechanism B: the backup copy's I/O - CONFIRMED as production's *current* latency

16 minutes of 2-second sampling of the live manager (read-only `stat`;
`.wr_production/` untouched):

- **12 copies, each the full 8.99 GB, 28-40 s each, 35% duty cycle**
- `db` stayed at 8,998,133,760 bytes throughout - **no growth, so no remap**
- **10 slow `jarchive`s, 17.4-26.6 s, about one per copy cycle**, 8 of 10
  overlapping a measured copy window

So production's ordinary slow archives today are the copy's I/O - the unfixed
**"Part C copy-I/O relief"**, already prototyped at 15.8 s -> 0.7 s via incremental
fsync. Statistical caveat recorded honestly: with a 20 s request, a 28 s copy and
an 81 s cycle, chance overlap is ~62%, so 8/10 is suggestive rather than decisive;
the strong evidence is the once-per-cycle cadence and durations bounded by the copy.

### Mechanism C: the mmaplock remap freeze - CONFIRMED real, currently dormant

`db.backupToBackupFile` wraps the whole copy in one `db.bolt.View`, holding
`mmaplock.RLock()` for its life. A write that must grow the file calls `db.mmap`,
which wants `mmaplock.Lock()`, and Go's `RWMutex` then queues everything behind it.

The A/B isolated exactly this. Same fixture, same filesystem, same 1,143
archivers; the only difference was freelist slack:

| | Arm A (as-is) | Arm B (same DB, compacted) |
| --- | --- | --- |
| free pages | **821,353** (45.5%) | **7** (0.0%) |
| file growth during run | **0 bytes** | **+966 MiB** |
| mmap steps crossed | **0** | **1** |
| throughput / p99 | 357/s / 1,484 ms | 360/s / 1,491 ms |
| **max archive** | **1,893 ms** | **6,613 ms** |
| watchdog stalls | 0 | **3** |

Throughput and p99 are *identical*; the only difference across 430,000 archives is
Arm B's multi-second outlier, at the exact second the file crossed its mmap
boundary. Per-second samples show **zero archives completing for the copy's
remaining ~5 s**, then 229 draining the instant it finished.

**Goroutine dump taken mid-freeze** (`armB-stall-1.txt`), 1,153 goroutines: 1,143
archivers in `chan receive`, **one** writer in `sync.RWMutex.Lock` under
`bbolt.(*DB).mmap(..., 0xc0000000)` - 3 GiB, i.e. `db.mmaplock.Lock()` - beneath
`archiveTx`; and the holder in `internal/poll.(*FD).Pread` under
`bbolt.(*Tx).WriteTo` <- `copyBackup` <- `backupToBackupFile` <- `bbolt.(*DB).View`.
Mechanism proven, not inferred.

**The freeze equals the copy's remaining time**, so it scales with DB size over
copy bandwidth: ~6 s here (3.2 GB at ~350 MB/s), but production's live copy is
9.0 GB in 28-40 s, so its ceiling today would be ~40 s - and far more during
07:58-08:08, when the copy was slower under load and **the DB grew 7.34 -> 8.54 GB**.

**A self-amplifying coupling worth naming:** an open read transaction pins every
page freed during it (`freelist.ReleasePendingPages` only releases below the oldest
read txid), so **the copy starves the freelist and thereby causes the growth that
triggers the remap.**

### Near-term prediction - worth watching

Production's free pages fell from **271,809 (14:54) to 111,133 (16:13)** with the
file size static: 0.46 GB of slack left, and 0.67 GB of high-water growth to the
next mmap step. **When the next add burst consumes that, remaps resume**, and any
that lands inside a copy freezes `add` and `jarchive` alike for the copy's
remainder. Mechanism C is dormant, not absent.

### Reading a production `archive fold` line

| signature | meaning |
| --- | --- |
| `maxTx` large, `maxLock` ~0, `maxFold` huge | mmap remap freeze (mechanism C) |
| `maxLock` ~ `maxTx` | add-path lock contention (mechanism A) |
| `maxWait` large, `maxTx` small | sparse arrivals - not a problem |

## PRODUCTION PROFILE, 16:33-16:54 (2026-08-27) - mechanism A confirmed

Manager restarted on `v0.37.1-90-g11fb939` (so the `archive fold` instrumentation
from `9fbccb4` was deployed for the first time) with
`GODEBUG=gctrace=1 WR_PPROF_ADDR=0.0.0.0:6060`, `-f`. Portal limit was 1 at
startup, raised to **500** at ~16:41. `WR_PPROF_ADDR` also enables mutex and block
profiling (`server.go:3263`).

**Profile and sample files: `/nfs/hgi/wr/sb10-pprof/prof260827/`**

| file | what |
| --- | --- |
| `base-{mutex,block,goroutine,heap}.pb.gz` | baseline at limit 1, 16:35:49 |
| `base-goroutine.txt` | baseline goroutine listing (69 goroutines) |
| `load-{mutex,block,goroutine}.pb.gz` | under load at limit 500, 16:43:43 |
| `load-cpu.pb.gz` | 30 s CPU profile, 16:43:44 |
| `load-goroutine-full.txt` | full goroutine dump with stacks, under load |
| `samples.csv` | 2 s samples: db size, `db_bk.tmp` size, slow-request counts |

Manager log: `/nfs/hgi/wr/lsf/.wr_production/log`. Foreground/gctrace log:
`/nfs/hgi/wr/sb10-pprof/wr-fg-1787844800.log`.

### Recovery at nearly double the job count

| phase | elapsed |
| --- | --- |
| opened database | 1.189 s |
| decoded live jobs (**296,949**) | **1m12.152s** |
| built dependency-group state (**258,218** memberships) | 892 ms |
| resolved prior job dependencies | **268 ms** |
| enqueued prior jobs | 635 ms |
| **16:33:21 -> 16:34:36 total** | **75 s** |

**296,949 jobs in 75 s**, against the old code's 42m56s for 150,472. Dependency
resolution is 268 ms at 258,218 memberships. Decode is 96% of the window and is
the only remaining startup cost.

### Mechanism A is production's dominant serialiser - three independent measurements

**1. The mutex profile** (`load-mutex.pb.gz`, 5,272 s total delay):

| path | share |
| --- | --- |
| **`bbolt.(*batch).run` -> `batch.trigger` -> `sync.Once.Do`** | **84.93%** (4,478 s) |
| `archiveWriter` -> `drainArchives` -> `archiveTx` | 9.10% (480 s) |
| `bestEffortWriter` -> `drainBestEffort` | 5.59% (295 s) |

`bolt.Batch` is used **only by the add path**; the archive and best-effort writers
both use plain `Update`. So **85% of all mutex delay in the manager is the add path
holding bolt's write lock**, and the archive writer is a victim at 9%. Flat cost is
`sync.(*Mutex).Unlock` under `bbolt.(*DB).Update` -> `Tx.Commit` -> `Tx.close`
(99.63%).

**2. The `archive fold` line**, steady state at limit 500, five consecutive minutes
within a few percent of each other:

```
txs=13 archives=1606 meanFold=123.54 maxFold=164 meanWait=2.477s maxWait=6.46s
  meanTx=5.062s maxTx=7.501s meanLock=4.52s maxLock=7.005s interval=1m0s
```

**`meanLock` / `meanTx` = 89%** - nine tenths of every archive transaction is spent
waiting for a lock someone else holds. And `meanFold` is 123-127 with a max of 164,
so **the writer is batching correctly**; it simply cannot get the lock. That is the
signature the instrumentation was built to identify.

**3. The block profile** (`load-block.pb.gz`, 51.40 hrs total):
`handleAdd` -> `createJobs` **7.20 hrs (14.02%)** against `handleArchive` ->
`archiveCompletedJob` **3.97 hrs (7.72%)** - adds block roughly twice as much as
archives.

**4. Slow-request counts agree**, and more starkly than this morning:

| method | n | mean | max |
| --- | --- | --- | --- |
| `add` | **14,996** | 13.1 s | 20.0 s |
| `jarchive` | 1,494 | 11.1 s | 25.9 s |

A **10:1** ratio, up from this morning's 1.35:1.

### The manager is not CPU-bound

`load-cpu.pb.gz`: 30 s wall, **4,790 ms of samples = 15.97%** of capacity. So
nothing here is a compute limit. Two entries worth recording anyway:

- **`crypto/internal/fips140/bigmod` at 24.63% cumulative** (`montgomeryMul`,
  `addMulVVW1024`) - RSA, i.e. TLS handshakes. The largest single CPU consumer, and
  it suggests client connection churn: each `wr add` from a runner appears to pay a
  handshake. Worth a look independently of the write path.
- **`freelist.(*hashMap).freePageIds` at 12.53%** - freelist work is back up
  (an earlier validation had driven it from 18.6% to 2.47%), consistent with the DB
  having grown and refragmented.

### Throughput peaks well below limit 2000

| limit | concurrent runners | completions/s |
| --- | --- | --- |
| 1 | ~1 | ~2/s (104-153/min) |
| **500** | ~349 rising | **~23-27/s** (1,392-1,632/min) |
| 2000 (this morning) | 1,143 | **~14/s** |

**Throughput roughly halves between limit 500 and limit 2000.** The optimum is at or
below 500, and beyond it more concurrency actively costs throughput - textbook
saturation collapse, and a useful operational fact on its own.

At limit 500 nothing was lost: **0 requests at or over 30 s, 0 at or over 60 s**,
against 470 lost reports at limit 2000. Every request still pays 11-13 s.

### Mechanisms B and C this session

- **B (backup copy I/O): present.** `jarchive` max 25.9 s, in the same 17-27 s band
  measured earlier, and the backup ran at a **9.2% duty cycle** (79 of 860 samples
  had a `db_bk.tmp`), much lower than the 35% seen this morning at higher load.
- **C (mmap remap freeze): not observed.** The DB grew 139 MB during the ramp
  (9.015 -> 9.154 GB between 16:42:23 and 16:43:17) and then went static, so no
  remap was forced while profiling. It remains confirmed in the harness with a
  goroutine dump, and dormant here.

### INDEPENDENT REVIEW of this profile - several of my readings were wrong

A fresh analysis of the same files corrected the section above. **The conclusion
survives; two of the arguments for it do not.** Corrections, in order of
importance:

**1. The 84.93 / 9.10 / 5.59% mutex split is a TRANSACTION-COUNT split, not a
hold-time split.** Go's mutex profile charges the *unlocker* the wait of the one
waiter it hands off to; under a persistently deep FIFO queue that wait is
approximately `queue_depth x mean_hold`, which is the same whoever unlocks. The
`contentions` dimension - which I did not use - reads 89.6 / 6.1 / 4.3%, and the
**mean delay per handoff is nearly equal across all three writers** (3.04 s,
4.80 s, 4.21 s), with the *archive* writer's the largest. Had the add path done
13x more but 13x shorter transactions, the profile would read identically.

So "85% of mutex delay is the add path" carries no hold-time information. The add
path **does** hold roughly 78-85% of the lock, but that comes from independent
evidence: at saturation the holds must sum to the window, the archive writer's
hold is `meanTx - meanLock` (0.42-0.54 s x 36 txs = ~16 s), best-effort ~4 s, and
the residual ~93 s over 1,079 add transactions = **86 ms each**.

**2. "The archive writer is merely a victim" is wrong as stated.** It is the *sole
gate* on completion throughput - `completions/s == meanFold / meanTx` is an
identity - so its service time sets the ceiling. And in aggregate waiter-seconds
the **add path is the main victim** (96.07% of blocked waiter time, versus the
archive writer's 1.86%), because it has 30-45 concurrent waiters while the archive
writer is a single goroutine. The defensible statement is per-goroutine: **the
archive writer is blocked 89-93% of its cycle.**

**3. "Not CPU-bound" was understated by 16x.** 4,790 ms of samples over 30 s is
16% of *one core*; gctrace reports **16 P**, so the manager used 0.16 cores =
**1.0% of the machine**. GC printed 0% throughout, heap flat at ~1.08 GB.

**4. "Stop the add path monopolising the lock" is unsupported as a throughput
fix.** Adds and completions are ~1:1 by construction, both need the same lock, and
the whole budget is ~10 write transactions/s. Giving the archive writer priority
just moves the queue from `jarchive` to `add`. What the data supports instead:

- **Cut the add path's transaction COUNT, not its priority.** 763 goroutines are
  blocked in `bolt.DB.Batch`, 655 of them spawned by `db.launchBatchStore` (one per
  batched store), spread across **28 simultaneous `batch.run` transactions**. The
  add path achieves **2.6 adds per transaction** against the archive writer's
  **125** - about 48x more transactions per unit of user-visible work. Replacing
  `bolt.Batch` with the same explicit drain-everything-pending writer the archives
  already use is the direct analogue.
- **Then attack the 60-90 ms per-transaction floor**, which is ~93% off-CPU. It is
  measured directly at limit 1: `meanTx` 52-74 ms with `meanLock` 0-1 ms and fold
  exactly 1. That is what a bolt write commit costs on this DB whatever the payload.

**5. A second competing consumer that NONE of the instrumentation can see.**
Because bolt is MVCC the backup's `tx.WriteTo` takes **no write lock**, so it is
invisible to the mutex profile, the block profile and `archivefold` alike. From
`samples.csv`: **22 distinct `db_bk.tmp` lifetimes, ~30-31 s each, one starting
every ~80-90 s, present in 26.4% of samples** - about **8.5 GB in 31 s, ~275 MB/s
of competing NFS write bandwidth at a ~26% duty cycle**, against a lock whose hold
is ~93% write and fsync. How much of the 86 ms it costs **cannot be determined
from these profiles**; it needs an A/B with the backup off or throttled.

Secondary effect confirmed: a 31 s read transaction pins pages freed during it, so
writers must allocate fresh ones - the DB grew 8.396 -> 8.573 GB over the run.

**Not a compaction story, though:** the freelist retains ~110k free pages ~= 450 MB
~= 5.4% of the file. Freelist *CPU* is still 54% of the write path
(`Tx.commitFreelist` 0.69 s of `DB.Update`'s 1.27 s), but the volume is small.

**6. `maxFold` = 164 is not a cap - it is a closed-loop artefact.** No size bound
exists anywhere on the drain path (`swapArchives` takes `arPending` wholesale).
The sequence is actually 131, 163, 164 x10, 166, 165, 168 - it tracks `maxTx`.
Because the writer *is* the rate limiter, `archives/s == meanFold/meanTx`, so
arrival rate is proportional to `1/meanTx` and `maxFold ~= arrival_rate x maxTx`,
predicted within ~5% on every line. Nothing is being clipped.

**7. RSA at 24.63% is real churn but irrelevant.** `jobqueue.Client` creates one
mangos socket in `Connect` and reuses it, so **no handshake is paid per request** -
the 745 long-lived pipes confirm it. The churn is per client *process*: ~23-30 new
connections/s against ~23-27 completions/s, i.e. about one per completion, because
each dedup job's own `wr add` is a fresh process. But `handshaker.worker` is
**0.35% of the host**; the entry tops the profile only because the manager does
almost no CPU work at all. An ECDSA server key would be 20-40x cheaper if it ever
mattered.

**8. My slow-request statistics were window-dependent and are tail means.** Over
the whole log: `add` **n=28,806 mean 13.54 s max 24.96 s**; `jarchive` **n=4,403
mean 11.67 s max 35.18 s**. Both are conditioned on >= 10 s (that is the log
threshold), so they are tail means, not means. The untruncated mean is
`meanWait + meanTx` ~= **7.6 s**.

**9. Provenance correction:** all four profile types are cumulative from process
start (16:33:20), and load only began ~16:41:45. So the load profiles cover **~113 s
of actual contention**, not the 474 s between snapshots - any rate divided by 474
is ~4x too low.

**10. The profiled run never reached the 60 s floor.** Max `jarchive` in the whole
window is **35.18 s**. This is a milder regime than the failure being diagnosed
(1,143 runners, latency pinned at 60 s, 470 lost reports), so extrapolating a fix
from it is reasonable but **not proven**.

**Two cross-instrument agreements worth trusting**, both independent of my
reasoning: the mutex profile's bolt-lock total (5,252.7 s) against the block
profile's `beginRWTx` (5,313.8 s) agree to **1.2%**; and the block profile's
`archiveTx` wait (98.97 s) against the fold log's own `meanLock x txs` over the
same window (~100 s) agree to **~1%**. Also: **29 goroutines blocked in
`sync.Mutex.Lock`, all on the same mutex address, all in `bolt.beginRWTx`** - there
is one serialiser, and `queue.mutex` is 290x smaller (18.06 s, 0.34%).

**Also confirmed, and relevant to work in flight:** there are **no `os/exec`,
`bsub`, `bjobs`, `RunCmd`, `limiter` or scheduler frames anywhere** in the 2,715
goroutine dump, and 99.9% of the archive handler's blocking is the archive writer.
So the synchronous LSF call in `decrementGroupCount` was blocking nothing at that
instant - it remains a latent unbounded call worth bounding, but it is not this
bottleneck.

### The measurement that would have prevented the misreading

Add the same four counters `archivefold` gives archives - transactions, calls
folded, lock wait, transaction duration - **to the add path's writer**. The
hold-time split in correction 1 is *derived*, not measured, because nothing
instruments the add path the way `archivefold` instruments archives. It is a small
change and it would settle the central number directly.

### Loose ends - both now settled

`maxFold` = 164 and the RSA CPU entry were both open when this section was first
written; corrections 6 and 7 above settle them. Neither is a defect.

## The two independent analyses: agreed, disputed, added

Two fresh-context analyses examined the same production window by different routes
- one from the **profiles**, one from the **code**. Neither saw the other's output.
This is the reconciliation.

### Agreed by both

- **There is exactly one contended resource: bolt's write lock.** The profile route
  proved it observationally (29 goroutines blocked in `sync.Mutex.Lock`, all on the
  same mutex address, all in `bolt.beginRWTx`; `queue.mutex` 290x smaller at 0.34%).
  The code route reached the same place by tracing every lock on both hot paths.
- **The add path dominates that lock.** Profile route: ~78-85%, *derived* from
  saturation plus the fold log. Code route: **87.7%, computed directly** from the
  fold line (`13 x (meanTx - meanLock) = 7.40 s of 60 s = 12.3%` for archives, so
  87.7% is everything else). Two methods, ~3-10 points apart, same conclusion.
- **The manager is not CPU-bound.** 1.0% of a 16-core host.
- **Reordering or prioritising the writers will not help.** Adds and completions are
  ~1:1 and share one lock; priority moves the queue rather than shortening it. Both
  independently concluded the lever is *transaction count*, not scheduling.
- **The periodic backup is a real competing consumer that the mutex and block
  profiles cannot see**, because bolt is MVCC and `tx.WriteTo` takes no write lock.

### Disputed - and the code route wins on evidence

- **The freelist.** The profile route concluded "not a compaction story… the volume
  is small" (~110k free pages, 5.4% of the file, and freelist *CPU* only matters
  when CPU is scarce, which it is not). The code route **reproduced the opposite**:
  the identical empty write transaction costs **0.63 ms with an empty freelist and
  61.3 ms at production's 111k free pages**, because `Tx.Commit` unconditionally
  rewrites the entire free+pending list - 890 KB, written and fsynced, for zero
  work. A reproduction beats an inference, so this stands as the explanation of the
  52-74 ms floor.
- **Whether the backup's cost is knowable.** The profile route said it cannot be
  determined without an A/B. The code route showed it is closed-form: the copy's
  read transaction pins pending pages, `EstimatedWritePageSize` counts them, and
  every commit during the copy pays. Both are right in their own frame - the profile
  route said "from *these profiles*" - but the practical answer is that two
  `bolt.Stats()` fields on the existing fold line would settle it.

### Added by the profile route alone

- **The methodological catch that mattered most:** Go's mutex profile charges the
  *unlocker* the wait of the waiter it hands off to, so under a deep FIFO queue the
  84.93/9.10/5.59% split is a **transaction-count** split carrying no hold-time
  information. My published reading of it was wrong, and no amount of code reading
  would have caught that.
- **Cross-instrument agreement worth trusting:** mutex-profile bolt-lock total
  5,252.7 s against block-profile `beginRWTx` 5,313.8 s - **1.2% apart**; and
  block-profile archive wait 98.97 s against the fold log's own figure ~100 s -
  **~1%**.
- 501 concurrent in-flight requests, **every one blocked** (239 add, 263 archive);
  745 live mangos connections.
- `maxFold` = 164 is a closed-loop artefact (`maxFold ~= arrival_rate x maxTx`,
  predicted within 5% on every line), not a cap.
- RSA is ~one new connection per completion - real churn, because each `wr add` is a
  fresh process - but 0.35% of the host, and no handshake is paid per *request*.
- **No `os/exec`, `bsub`, `bjobs` or scheduler frames anywhere in 2,715 goroutines**,
  so the synchronous LSF call was blocking nothing at that instant.

### Added by the code route alone

- ~~`bucketEnvs` grows by one never-deleted ~6-11 KB record per add~~ -
  **FALSIFIED by measurement 2026-08-28**: `bucketEnvs` holds 549 keys / 1.31 MB
  against `bucketJobsComplete`'s 2.15M keys / 2.87 GB, i.e. 0.04% of leaf bytes,
  and `Client.Execute` replaces `cmd.Env` from `job.Env()` so the key is not unique
  per add. The DB's growth is just archived jobs accumulating. A redundant write
  transaction on a cache miss was real and is fixed (`72d3d3d`). See
  `throughput-architecture.md`.
- **Three *sequential* write transactions per single-job add** - the latency
  mechanism the transaction count alone could not explain. 3 x 4.6 s queue traversal
  = 13.8 s against a measured 13.54 s tail mean.
- The freelist reproduction above.
- The concrete minimum fix: three edits, no new machinery, ~2.4-4x the ceiling.
- **The ordering hazard** that any add-fold change must argue past: `prepareNewJobs`
  reads before it writes, and adds interact through dep groups.
- A plain bug in `storeBatched` (an extra empty committed transaction per store),
  and the explanation of `wr add 150000` taking 72.5 s (split into ~50 transactions).
- No 6-request concurrency cap: `numRPCReaders` bounds admission only.

### Net position

The diagnosis is settled and neither analysis was redundant. **The floor is the
freelist rewrite; the multiplier is three sequential add transactions; the growth
that feeds both is an env record that is never deleted.** The fix is a set of
cheap, contained edits, not new machinery - and the profile route's contribution
was to stop the wrong fix being built on a misread number.

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

### Group commit is already in production, and the symptom happened anyway

**Correction to this document's own framing.** It says a completed job "currently
costs one bolt write transaction". That has not been true since `f7e36bc`
(2026-08-18), which is an ancestor of the deployed `fb5df01`. `db.archiveJob`
encodes the job *outside* any transaction, enqueues an `archiveOp`, and a single
long-lived `archiveWriter` folds **every** archive pending at that moment into ONE
`db.Update`, replying to each waiter with its own error (bbolt-`Batch`-like
per-caller semantics, including re-running a failing member on its own).
`TestReliable4ArchivesCoalesceIntoOneTransaction` in the main suite pins the
archives-per-transaction ratio.

So `throughput-architecture.md`'s **idea 1 is, for the archive path, already
implemented and already deployed** - and the 07:58-08:08 symptom occurred with it
running. Two things follow:

- the arithmetic "one synchronous transaction per archive at ~70 ms *is* about
  14/s" no longer follows from the code, so it cannot stand as candidate 2's
  mechanism without new evidence;
- the 2026-08-17 block profile that put 100% of `archiveCompletedJob`'s block time
  inside `db.archiveJob` **predates** `f7e36bc`, so it cannot be cited for where
  the 08:00 latency sat.

### In-process reproduction with production's ingredients: the symptom does not appear

`developers/wrdev.sh archive-ceiling` was built to reproduce the *symptom* rather
than any fix's mechanism, and run on `/nfs/hgi` - the filesystem production's own
DB lives on - with the periodic full-DB backup forced on and its streamed bytes
measured. `report-storm` was then run at production's shape to cover the whole
server RPC path rather than `db.archiveJob` alone.

| measurement | concurrency | achieved | worst archive | over the 60 s floor |
| --- | --- | --- | --- | --- |
| `archive-ceiling`, pristine6 (6.89 GiB, 3202 MiB freelist), NFS, backups ON (27,347 MB at 75.8 MB/s), 256-byte Cmds | 20 -> 1,143 | 8.37/s -> **363.9/s** (43.5x for 57.1x) | 1.62 s | 0 |
| same, **25 KB Cmds** (production's `portal_builder` size; 31,362 MB at 86.9 MB/s) | 20 -> 1,143 | 8.37/s -> **334.3/s** (39.9x) | 2.03 s | 0 |
| `report-storm` through the real server RPC path: 80,000-job live set, 1,143 real client runners, same DB on NFS, backups ON | 1,143 | **~350/s**, all 80,000 drained, 0 timeouts, 0 rejections | 5.10 s | 0 |
| **production, 08:00-08:07** | 20 -> 1,143 | 8.7/s -> **~14/s** (1.6x) | >= 59.9 s | **470 reports lost** |

None of these is sufficient to produce the symptom: a 6.89 GiB DB with a 3.2 GB
freelist; NFS; a continuous full-DB backup copying at production's own ~80 MB/s;
1,143 concurrent archivers; production-sized 25 KB records; or a production-sized
live set driven through the real RPC handlers. The in-process write path is
**24-26x faster** than production's and its worst archive is **12-37x clear** of
the client floor. Record size in particular is nearly free here: 100x bigger
records cost 8% of the throughput.

### The mutation that DOES reproduce it: one transaction per archive

The same mode, with `db.archiveJob` mutated to take its own `db.bolt.Update` per
archive instead of enqueuing to the coalescing writer - everything else identical,
same DB, same filesystem, same backup streaming at 83.1 MB/s:

| | low, 20 archivers | high, 1,143 archivers |
| --- | --- | --- |
| achieved | 8.18/s | **21.32/s** (efficiency 0.043) |
| mean archive | 152 ms | **69,995 ms** |
| p99 / max | 650 / 800 ms | 91,158 / 91,432 ms |
| queue depth | 1-6 | **1,108-1,129** |
| over the 60 s floor | 0 | **1,827** |

`throughputFactor` **2.61x for 57.1x the concurrency**. Production measured
**1.6x for 57x**, latency pinned at the floor, 470 reports lost. All three
symptoms, to the digit.

**So production behaves exactly like the code with no group commit, while running
the code with it.** The two are indistinguishable from outside - a queue that
drains at one archive per transaction looks the same however the transaction came
to hold one archive - so there are two candidate explanations, and they need
different work:

1. **The writer is not batching in production, because something UPSTREAM of
   `db.archiveJob` serialises completions**, leaving it one archive to fold at a
   time. The obvious candidate is the queue mutex: `getijForReport` does `s.q.Get`
   on every report, while `satisfyEmptiedDepGroups` takes the queue's write lock
   per completion, once per dep group the completion emptied - and production has
   **112,486 dep-group memberships** and a dependency-heavy `portal_builder`
   workload. The in-process runs above cannot see this: `archive-ceiling` bypasses
   the server entirely, and `report-storm`'s 80,000 jobs have no dep groups at all.
   This is a hypothesis, not a finding.
2. **The 60 s is not inside the archive at all**, and the resemblance is
   coincidence.

Both are answered by the same measurement, which is already the deferred item
below: **a mutex + block profile of the live manager under saturation.** The
question it has to answer has changed, though - not "where do the 68 ms inside the
bolt transaction go", but "how many archives is the coalescing writer folding per
transaction, and what is holding the queue mutex while they wait".

**What else is left, and it is all production-only:** 1,143 *remote* runners over
real TLS connections plus their touch traffic, rather than in-process clients; the
LSF scheduler on the same manager (`bsub`/`bjobs`/`bkill` subprocesses,
`killExcessCmds`, the scheduling callback); the web status feed; production's real
DB contents and key distribution (2.15M complete, 118k live) rather than the
generator's; and whatever else was competing for `/nfs/hgi` and for the farm at
08:00, including ~22,500 other pending mercury jobs.

An incidental find from the same runs: **`wr add` of 150,000 jobs in one request
took 72.5 s and the client gave up at its own 60 s floor** (80,000 took 49.7 s).
Not this symptom, but the same floor, and a real ceiling on bulk add.

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

1. **Do NOT start by building group commit - it is already built and deployed**
   (`f7e36bc`, in `fb5df01`). See "Group commit is already in production" above.
   The next step for `throughput-architecture.md`'s idea 1 is to find out **why it
   is not batching in production**, since one transaction per archive is exactly
   what production's numbers look like and what the mutation above reproduces.
   Idea 2's premise is also weakened by the measurements above: 25 KB records cost
   8% more than 256-byte ones here, and the whole in-process write path is 24x
   faster than production's.
   **Ruled out by the operator (2026-08-27):** moving the DB to local disk, and
   relying on compaction cadence or a separate backup filesystem. Those may still
   be measured as diagnostics, but nothing is to depend on them.
2. **Profile production when the operator can restart it** (deferred - a restart
   is not available right now) - now the critical path, and with a sharper
   question. Capture CPU + **mutex + block** during saturation with the limit
   raised, *and* sample `db_bk.tmp`'s size every couple of seconds so archive
   latency can be correlated against backup copy windows. The question is no
   longer "where do the 68 ms inside the transaction go" but **"how many archives
   is the coalescing writer folding per transaction, and what is holding the queue
   mutex while they wait"** - `satisfyEmptiedDepGroups` per completion against
   112,486 dep-group memberships is the leading suspect.
   **This document gets updated with the result.**
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
7. **Reliable3 issue 2a: priority-unfair limit-group budget allocation. OPEN, and
   reproducible on demand.** The shared per-limit-group budget is handed out
   first-come across sibling scheduler groups, in map order rather than priority
   order, so a low-priority sibling scanned first takes the whole budget and a
   higher-priority sibling gets nothing. `developers/wrdev.sh
   priority-fairness-check` reproduces it deterministically in-process:
   `low(pri0).count=2000 high(pri250).count=0 high.skipped=2500` at a 2000 limit.
   That mode is an INVERTED reproducer - it asserts the bug, so its **exit 0 means
   the bug is still present** - and it prints a banner to that effect. When this is
   fixed, convert that mode into a regression gate (the higher-priority sibling
   must get its share) and delete this item. Not seen to bite production yet, which
   is why it is recorded rather than scheduled: it needs sibling scheduler groups
   of *differing* priority sharing one limit group.

