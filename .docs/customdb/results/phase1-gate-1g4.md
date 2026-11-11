# Phase 1 gate: make speed-full against d1a4b6d7 (item 1g.4)

Gate (spec.md, Implementation Order, Phase 1, Gates; phase1-gate.md item
1g.4): `make speed-full` against `d1a4b6d7`, with no regression above 10%.

Run on 2026-11-10, 03:19 to 05:27 GMT, on farm22-wrstat01 (8 cores, 91GB,
shared with other users; load average 1.2 to 4.1 throughout). HEAD was
`27c10a45` on `customdb-f397b547`, a clean tree with the log store as the
default. Trimmed outputs are in [phase1-gate-1g4/](phase1-gate-1g4/); the
raw run directories are under `/nfs/hgi/wr/sb10-bigdb/customdb/1g4/speed/`.

**Verdict: FAIL.** One defect on the log store's add path causes two
confirmed regressions:

- `BenchmarkAddDepGroupMember` does not complete on HEAD. Base takes 361ms
  for one add; HEAD's first add was still running after 13m23s, when the
  benchmark gave up with `receive time out`.
- In the `dep-granularity-check` scenario (6 interleaved rounds), add-sec
  rises from 567.5ms to 4,090.5ms (+620.79%, p=0.002), and peak RSS from
  306.1MiB to 362.4MiB (+18.38%, p=0.002). Every HEAD round also fails the
  scenario's own threshold (addSec at most 2s).

Every other benchmark and scenario is within 10%, or is better on HEAD.

## Method

`developers/speed.sh` builds the baseline in a git worktree. This run used
no worktree: the baseline was a `git archive d1a4b6d7` export into a local
scratch directory, passed as `SPEED_BASE=<dir>`, which `speed.sh` accepts
as a prepared tree. The fixtures and ports were those reserve-runstate's
phase 6 used for its `make speed-full`. benchstat was the copy installed
earlier at `/nfs/hgi/wr/sb10-bigdb/speed/tools/`, so nothing had to be
fetched. Every `OS_*` variable was unset, and `HOME` was an empty scratch
directory, so modules and the build cache came from scratch:

```bash
F=/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures
(unset $(compgen -v | grep '^OS_'); HOME=<scratch>/home1 \
 SPEED_BASE=<scratch>/base-d1a4b6d7 SPEED_DIR=/nfs/hgi/wr/sb10-bigdb/customdb/1g4/speed \
 SPEED_BIG_DB=$F/pristine6 WR_AS_DB=$F/prod.db WR_ARCHRATE_DB=$F/pristine10 WR_AC_DB=$F/pristine6 \
 DEV_PORT=51994 DEV_WEB=51995 PROD_PORT=51996 PROD_WEB=51997 make speed-full)
```

`SPEED_DIR` is on NFS, so every scenario's work copy lives on NFS, as
wrdev.sh asks for add-storm and archive-ceiling. The fixtures were only
read; `/nfs/hgi/wr/sb10-bigdb/prod.db` was not touched.

| Run | What | Window (GMT) | Result |
| --- | --- | --- | --- |
| 1 | the command above | 03:19-03:36 | aborted: HEAD's `BenchmarkAddDepGroupMember` failed in round 1 |
| 2 | run 1 with `SPEED_BENCH` set to the default set minus `AddDepGroupMember` | 03:44-04:29 | FAIL: `dep-granularity-check` fails its threshold on HEAD |
| 3 | `SPEED_COUNT=0 SPEED_SCENARIOS="report-storm dep-granularity-check" SPEED_SCENARIO_COUNT=6` | 04:30-04:39 | FAIL: dep-granularity add-sec and peak RSS regress |
| 4 | `SPEED_COUNT=0 SPEED_SCENARIOS=add-storm SPEED_SCENARIO_COUNT=4` | 04:43-05:27 | PASS |

Runs 3 and 4 are the load-matched A/B reruns of every scenario figure that
was over 10% worse in run 2's single round: `speed.sh` alternates base and
HEAD within each round. The clock times are from `LOG.txt`.

## Results

### Benchmarks (run 2, 6 interleaved rounds)

Full table: [run2-benchstat.txt](phase1-gate-1g4/run2-benchstat.txt).
No sec/op change is significant:

| Benchmark | base sec/op | HEAD sec/op | delta |
| --- | --- | --- | --- |
| JobCleanup | 336.2µ | 332.6µ | ~ (p=0.94) |
| JobCleanupDepth1 | 3.340m | 3.223m | ~ (p=0.70) |
| JobCleanupDepth8 | 3.526m | 3.516m | ~ (p=0.82) |
| AddJobs | 131.3m | 130.1m | ~ (p=0.31) |
| UpdateJobState | 55.73m | 55.87m | ~ (p=0.59) |
| ArchiveJobs | 262.3m | 260.6m | ~ (p=0.59) |
| ArchiveSpacedArrivals | 2.474 | 2.472 | ~ (p=0.13) |
| ModifyLiveJobsReverseLookup | 11.68m | 11.71m | ~ (p=0.59) |
| JobKey | 28.91n | 28.86n | ~ (p=0.82) |
| BuildSchedulerGroupsBacklog/priorities=1 | 99.22m | 100.01m | ~ (p=0.70) |
| BuildSchedulerGroupsBacklog/priorities=4 | 148.0m | 153.6m | ~ (p=0.39) |
| RACCycleCounted/leavers=0 | 92.73m | 95.17m | ~ (p=0.31) |
| RACCycleCounted/leavers=100 | 100.3m | 101.0m | ~ (p=0.24) |
| ReadyBacklogSnapshot | 1.462m | 1.427m | ~ (p=0.82) |
| RepGroupStatusDetails/1000 | 3.230m | 3.232m | ~ (p=0.94) |
| RepGroupStatusDetails/10000 | 40.56m | 39.78m | ~ (p=0.09) |
| LimiterIncDecUnlimited | 2.019µ | 2.029µ | ~ (p=0.90) |
| LimiterIncDec | 2.208µ | 2.150µ | ~ (p=0.49) |
| LimiterCapacity | 1.896µ | 1.885µ | ~ (p=0.94) |
| QueueLifecycle/groups=1 | 2.760m | 2.757m | ~ (p=0.59) |
| QueueLifecycle/groups=100 | 1.966m | 1.961m | ~ (p=1.00) |
| QueueLifecycle/groups=1,callback=running | 2.922m | 2.849m | ~ (p=0.13) |
| QueueLifecycle/groups=100,callback=running | 2.088m | 2.044m | ~ (p=0.70) |
| **AddDepGroupMember** (run 1, round 1) | **361.1m** | **no result after 13m23s** | **FAIL** |

Some B/op and allocs/op changes are significant, but all are small:
UpdateJobState B/op -2.52%, RepGroupStatusDetails/1000 allocs/op +1.22%
and B/op +0.55%, JobCleanup B/op -0.36%, ArchiveSpacedArrivals allocs/op
+0.15%. No bolt pages, writes or transactions per job changed
significantly.

### Scenarios

| Scenario figure | base | HEAD | delta | rounds |
| --- | --- | --- | --- | --- |
| report-storm jobs/s | 2,124 ± 48% | 2,440 ± 36% | ~ (p=0.31) | 6 (run 3) |
| report-storm max-archive-ms | 174.5 | 108.0 | -38.11% (better, p=0.028) | 6 (run 3) |
| report-storm wall-sec | 11.88 | 8.855 | ~ (p=0.31) | 6 (run 3) |
| **dep-granularity add-sec** | **0.5675** | **4.0905** | **+620.79% (p=0.002)** | 6 (run 3) |
| **dep-granularity peak RSS** | **306.1MiB** | **362.4MiB** | **+18.38% (p=0.002)** | 6 (run 3) |
| dep-granularity recovery-sec | 0.1945 | 0.2140 | ~ (p=0.24) | 6 (run 3) |
| add-storm adds/s | 232.5 | 233.0 | ~ (p=1.00) | 4 (run 4) |
| add-storm p50-ms | 513.5 | 359.5 | ~ (p=0.49) | 4 (run 4) |
| add-storm p99-ms | 3,486 | 3,762 | ~ (p=0.34) | 4 (run 4) |
| add-storm max-ms | 3,990 | 4,184 | ~ (p=0.89) | 4 (run 4) |
| add-storm txns/add | 0.130 | 0.150 | ~ (p=0.26) | 4 (run 4) |
| archive-rate archives/s | 167.0 | 168.5 | +0.9% | 1 (run 2) |
| archive-rate mean-ms | 160 | 125 | -21.9% (better) | 1 (run 2) |
| archive-rate p99-ms | 1,373 | 1,153 | -16.0% (better) | 1 (run 2) |
| archive-rate max-ms | 1,913 | 1,591 | -16.8% (better) | 1 (run 2) |
| archive-ceiling archives/s | 369.2 | 357.9 | -3.1% | 1 (run 2) |
| archive-ceiling throughput-factor | 42.41 | 41.20 | -2.9% | 1 (run 2) |
| archive-ceiling p99-ms | 1,623 | 1,640 | +1.0% | 1 (run 2) |
| archive-ceiling max-ms | 1,863 | 1,832 | -1.7% | 1 (run 2) |

Every scenario met its own thresholds on both trees, except
`dep-granularity-check` on HEAD. In the single round of run 2, three more
figures were over 10% worse on HEAD; the reruns show all three are noise:

- report-storm max-archive-ms, +77% (99 to 175ms). Run 3 has HEAD better
  (-38%), and phase 0 recorded a range of about 72 to 176ms on both trees.
- add-storm txns/add, +162% (0.08 to 0.21). Run 4 shows no significant
  change (0.13 against 0.15, p=0.26). wrdev.sh's add-storm notes say why it
  moves: under deep saturation bbolt coalesces more, so txns/add falls.
  In run 2's high phase, base reached only 0.50 of the offered rate and
  HEAD 0.77. Both trees stay within the 1.00 ceiling.
- dep-granularity peak RSS, +19%. Run 3 confirms it; see the diagnosis
  below.

report-storm's absolute throughput moved between rounds on both trees
alike: about 1,150 (base) and 1,750 (HEAD) jobs/s in rounds 1-3 of run 3,
and about 3,100 and 3,300 in rounds 4-6 and in run 2. The cause is outside
the trees (host or NFS). The interleaving is what makes the rows
comparable.

### What the gate measures on HEAD

On HEAD, most of speed-full still runs on bbolt, so its "within 10%" says
nothing about the log store there:

- add-storm calls `withBoltStore` (since 1f.11), and archive-rate and
  archive-ceiling open bbolt directly through `initDB`.
- `AddJobs`, `UpdateJobState`, `ArchiveJobs`, `ArchiveSpacedArrivals` and
  `ModifyLiveJobsReverseLookup` (`db_bench_test.go`, `db_bolt_test.go`)
  open bbolt directly. The scheduler, status, limiter and queue
  benchmarks touch no store.

Only report-storm, `dep-granularity-check` (the real `wr` manager, which
converts the bbolt fixture to the log store at start) and
`BenchmarkAddDepGroupMember` (through `serve`) exercise the log store. So
this gate did not measure the log store's add, archive and big-DB costs.
The 1g.5 NFS targets and `TestAddLatency1000` (`WR_TEST_STORE=log`) cover
some of that ground. Whether speed-full's big-DB scenarios should move to
the log store is a question for the spec owner.

## Regression diagnosis

### Root cause

The add of one member to a dep group reads every job that depends on the
group. `logStore.planDependents` calls `dependentJobs`, which calls
`dependentJob`, then `liveJobOf`, then `decodeJob` for each dependent
(`jobqueue/store_log.go:2408`, `:2531`, `:2571`, `:1017`, `:1650`).
`decodeJob` reads the job's spec record and state record separately,
through `readRecordBody`, then `readRecord`, then `wal.ReadAt`
(`store_log.go:1597`, `jobqueue/wal/scan.go:99`). When the record lies
inside a block, `wal.ReadAt` reads the whole frame into a new buffer and
`blockRecord` decompresses the whole block (`decodeBlock`). It then
CRC-checks every frame inside (`wholePlainFrames`) to return one record.
Reading k records from one block therefore decompresses it 2k times.

The 30,000 waiters and 3,000 members were added together, so their specs
and states share one block. `logBlockMaxFrames` is `wal.MaxFrameLen / 2`
(`store_log.go:77`), so the store does not split it. The add's cost is
therefore about (2 × dependents) × (block size), which is quadratic in
the batch:

- A SIGQUIT goroutine dump of the hung benchmark
  ([repro-goroutine-dump.txt](phase1-gate-1g4/repro-goroutine-dump.txt))
  shows the add handler in exactly this stack, ending in
  `wal.wholePlainFrames` under `wal.decodeBlock`. The process was at 195%
  CPU, so it was busy, not deadlocked.
- On a scratch copy of HEAD with the waiter count made configurable
  ([scaling-head.txt](phase1-gate-1g4/scaling-head.txt)), one add took
  2.67s with 1,000 waiters, 7.07s with 2,000 and 20.45s with 4,000. Each
  doubling costs about 2.6 to 2.9 times as much. Extrapolated, 30,000
  waiters take about 19 minutes, against base's 361ms.

In `dep-granularity-check` the manager first converts the bbolt fixture,
which puts the live jobs in a checkpoint whose blocks are at most 64KiB
(`checkpointBlockBytes`, `jobqueue/store_checkpoint.go:80`). The same
about 60,000 point reads then each decompress and CRC-check one 64KiB
block, about 4GB of work. That matches the measured 4.1s, against base's
0.57s.

The peak RSS regression comes from the same add. A rerun with a scratch
copy of `wrdev.sh` prints the manager's VmHWM just before `wr add`
([prehwm-rss-ab.txt](phase1-gate-1g4/prehwm-rss-ab.txt), 3 alternating
rounds):

| | before the add | after the add |
| --- | --- | --- |
| base | 221, 223, 222MB | 327, 324, 321MB |
| HEAD | 186, 191, 185MB | 380, 393, 380MB |

HEAD's startup peak, conversion included, is about 35MB lower than
base's. The add then raises HEAD's peak by about 195MB, against base's
100MB: each point read allocates a freshly decompressed block, which
becomes garbage at once.

The only other point reads of this kind are the `history.bolt` overlay's
(`store_history_bolt.go:702`, `:927`), which read history entries the
folder has not yet folded. The fold, recovery and checkpoint reads already
decode each frame once, through `wal.ReadFrame`.

### Proposed fix

Batch the dependents' reads by frame. `dependentJobs` would first collect
the spec and state Refs of all the live dependents, under one `holdRefs`
generation. It would then group the Refs by (file, offset) and read each
frame once with `wal.ReadFrame`, which already decompresses a block once
and gives each record with its Ref, keeping only the records it needs.
Finally it would decode the jobs. This makes the add cost proportional to
the number of blocks plus the number of records, and drops the per-record
garbage. Any other caller that reads many keyed jobs, such as the overlay
history reads, could share the same batch reader.

A bounded cache of recently decoded blocks inside `readRecord` would also
remove the quadratic cost, without changing callers. It would hold up to
one block's plain bytes, which can be hundreds of MB for a large add, so
it needs a byte bound and its own invalidation rules. The batch read
avoids both.

A structural test in the style of `TestDepGranularityAddTransactionCost`
would guard either fix: the number of blocks decoded while adding one
member to a group with N dependents must not grow with N. The
`BenchmarkAddDepGroupMember` and `dep-granularity-check` reruns then
verify the timing.

This fix was not made here. It changes the log store's read path, which
needs its own red test, review and a rerun of this gate, so it goes to
`bugfix`. After it lands, rerun at least `BenchmarkAddDepGroupMember` and
`dep-granularity-check` against `d1a4b6d7`, ideally the whole of
`make speed-full`.

## Files

- [LOG.txt](phase1-gate-1g4/LOG.txt): timeline, load at each step.
- [run1-adddepgroupmember-hang.txt](phase1-gate-1g4/run1-adddepgroupmember-hang.txt):
  run 1's output, HEAD's round-1 benchmarks and the slow-request lines.
- [repro-goroutine-dump.txt](phase1-gate-1g4/repro-goroutine-dump.txt),
  [scaling-head.txt](phase1-gate-1g4/scaling-head.txt): the diagnosis.
- `run2-*`: run 2's log, benchstat, raw base and HEAD results, and each
  scenario's summary and verdict lines.
- `run3-*`: the 6-round report-storm and dep-granularity A/B.
- `run4-*`: the 4-round add-storm A/B.
- [prehwm-rss-ab.txt](phase1-gate-1g4/prehwm-rss-ab.txt): the
  before-and-after-the-add RSS A/B.

## Fix (bugfix 261110-dependents-pointread-a11cc697)

Checklist:
[261110-dependents-pointread-a11cc697.md](../../bugfixes/261110-dependents-pointread-a11cc697.md).
The fix is uncommitted on `customdb-f397b547` above `27c10a45`. It took four
rounds: rounds 2 and 3 cut the add's allocations after the first A/B, and
round 4 follows review round 1, which FAILed on a test that did not pin the
read buffer cap, stale wording here and in the checklist, and three
incidentals (see Fix round 4).

The add now reads its dependents by frame. `dependentJobs` takes the Refs of
each dep group's live dependents under one `holdRefs` hold (`liveRefsOf`).
`liveJobsOf` sorts their spec and state records by Ref, reads each frame
holding them once (`readJobBodies`, `readFrameJobBodies`, `takeJobBodies`),
copies each body once, and decodes each job with `jobOf`. Archived
dependents are read in one history read (`historyJobs`). History reads
(`historyRead`) read an overlay entry's records through a frame cache that
lasts as long as the read (`overlayRecord`), so a query that reads many
entries whose specs share one add's block decompresses that block once; this
was the same defect on the status and history path. Two structural tests
guard both: `TestLogStoreDependentsReadCost` (record file reads for one
one-member add: 1,000 before the fix with 500 waiters, 1 after) and
`TestLogStoreOverlayReadCost` (400 reads before, one per frame after).
The round 1 A/B below was run on round 1's code, which read through the
fold's `readRecordBodies` and recovery's `jobsOf`; rounds 2 and 3 replaced
that path.

### Speed A/B against d1a4b6d7

Run on 2026-11-10, 06:24 to 06:33 GMT, on farm22-wrstat01 (load average
1.1 to 2.3), with `developers/speed.sh quick`, base and HEAD interleaved,
6 rounds of each:

```bash
(unset $(compgen -v | grep '^OS_'); HOME=<scratch> \
 SPEED_BASE=<scratch>/base-d1a4b6d7 SPEED_DIR=/nfs/hgi/wr/sb10-bigdb/customdb/1g4/fix-speed \
 SPEED_BENCH='^BenchmarkAddDepGroupMember$' SPEED_COUNT=6 \
 SPEED_SCENARIOS=dep-granularity-check SPEED_SCENARIO_COUNT=6 \
 SPEED_BIG_DB=/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures/pristine6 \
 DEV_PORT=51994 DEV_WEB=51995 PROD_PORT=51996 PROD_WEB=51997 developers/speed.sh quick)
```

Results: [fix-benchstat.txt](phase1-gate-1g4/fix-benchstat.txt), raw lines
in [fix-raw.txt](phase1-gate-1g4/fix-raw.txt); the run directory is
`/nfs/hgi/wr/sb10-bigdb/customdb/1g4/fix-speed/run-1794291848/`.

| Figure | base | HEAD + fix | delta |
| --- | --- | --- | --- |
| AddDepGroupMember sec/op | 380.1m | 347.3m | -8.63% (p=0.009) |
| AddDepGroupMember B/op | 102.2Mi | 167.1Mi | **+63.41% (p=0.002)** |
| AddDepGroupMember allocs/op | 1,175.7k | 798.3k | -32.10% (p=0.002) |
| dep-granularity add-sec | 508.5m | 392.5m | -22.81% (p=0.002) |
| dep-granularity peak RSS | 302.3Mi | 267.0Mi | -11.67% (p=0.002) |
| dep-granularity recovery-sec | 173.0m | 172.0m | ~ (p=0.73) |

Every HEAD round of `dep-granularity-check` passed its own thresholds.
Time and peak RSS are within the gate, and better than base. `speed.sh`
still fails the run on one figure: `AddDepGroupMember` allocates 63% more
bytes per add than base.

A memory profile of 10 adds on each tree places the extra bytes. Both
trees allocate about 57MB per add decoding the 30,000 waiters' jobs (bbolt's
`decodeJob`; the log store's `jobOf`). bbolt reads the encoded jobs from
its memory map without copying them. The log store reads the waiters'
block from its file and decompresses it (about 15MB per add), then copies
each record's body and indexes it in maps (about 25MB per add). Removing
those copies would need `wal.ReadFrame` to hand over its decompressed
buffer, or pooled buffers; neither was done here. Peak RSS falls rather
than rises because the decompressed block is read once and dropped at once.
Whether this B/op figure must be within 10% of the bbolt base, or is
accepted as the log store's cost of reading by copy, is for the gate owner.

### Fix round 2: fewer copies (B/op still fails)

The coordinator ruled the B/op figure a gate failure, so the dependents' read
was cut down further:

- `wal.ReadFrame` reads and decompresses into buffers from a `sync.Pool`.
  Its contract already said a record is valid only until `fn` returns, and
  every caller copies what it keeps.
- `liveJobsOf` no longer goes through maps. It sorts the records it needs by
  Ref, reads each frame once, and copies each body once, straight from the
  record (`keyedBody`, which does not allocate a parsed record). The copy is
  still needed: the flat decoder's strings alias the bytes they are decoded
  from (`unsafe.String`), so a job kept from a pooled buffer would change
  under it. `TestLogStoreDependentsAcrossFrames` fails if the copy is
  removed.

A second interleaved A/B, same command, 08:07 to 08:17 GMT:
[fix2-benchstat.txt](phase1-gate-1g4/fix2-benchstat.txt),
[fix2-raw.txt](phase1-gate-1g4/fix2-raw.txt), run directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g4/fix-speed/run-1794298057/`.

| Figure | base | HEAD + fix | delta |
| --- | --- | --- | --- |
| AddDepGroupMember sec/op | 383.2m | 313.2m | -18.27% (p=0.002) |
| AddDepGroupMember B/op | 102.2Mi | 142.0Mi | **+38.90% (p=0.002)** |
| AddDepGroupMember allocs/op | 1,175.7k | 696.4k | -40.77% (p=0.002) |
| dep-granularity add-sec | 529.5m | 327.0m | -38.24% (p=0.002) |
| dep-granularity peak RSS | 308.0Mi | 249.4Mi | -19.04% (p=0.002) |
| dep-granularity recovery-sec | 192.0m | 191.5m | ~ (p=0.90) |

`speed.sh` still fails the run on B/op. What is left of the excess is not
the add's reading of its dependents alone:

- **The benchmark's own setup.** Run directly, HEAD's B/op falls as b.N
  grows: 148MB at 3 adds, 122MB at 10 and at 30; base stays at 107MB. At
  `speed.sh`'s 1s benchtime, b.N is 3, so about 26MB per add is a one-off
  cost landing in the timed loop. The profile places it in the log store's
  background folder (`runFolder`, 208MB in a 30-add run), which folds the
  33,000 jobs the benchmark adds before `b.ResetTimer`. bbolt has no such
  background work. Only a change to the benchmark (waiting for the store to
  settle before timing) removes it.
- **The decompressed block.** At 30 adds HEAD is 122MB against base's
  107MB, +14%. Of the add's own excess, about 12MB is the decompressed
  block and 3MB its compressed payload, which bbolt never allocates because
  it reads from its memory map. `sync.Pool` does not keep them across adds,
  since decoding 30,000 jobs runs several GCs per add, and a pool is emptied
  by GC. Keeping them would need a long-lived buffer on the store, sized to
  the largest block it has read. That is a design change, not part of this
  fix.

So B/op cannot get within 10% of base without one of those two changes. The
evidence is above. The decision is the gate owner's: change the benchmark,
add a retained read buffer, or accept the figure.

### Fix round 3: settled benchmark and a kept read buffer (PASS)

The coordinator asked for both changes round 2 named:

- **A kept read buffer.** `wal.ReadFrameInto` reads a frame into
  caller-owned `wal.FrameBuffers`; `wal.ReadFrame` still uses a pool. The log
  store keeps one set of buffers in a single slot (`takeReadBufs`,
  `putReadBufs`). The dependents' read (`readFrameJobBodies`) and the frame
  reads of history queries and the fold (`readFrameRecords`) take the slot
  for one frame and give it back after. A reader that finds the slot taken
  reads into new buffers, so a buffer is never shared while a caller holds
  it. Buffers grown past `readBufsMax` (64MiB) are dropped, not kept.
  `TestLogStoreConcurrentFrameReads` (run with `-race`) has 16 goroutines
  reading the same live jobs and overlay entries at once;
  `TestLogStoreReadBufsBound` checks the cap.
- **A settled benchmark on HEAD.** `BenchmarkAddDepGroupMember` now waits,
  before `b.ResetTimer`, for the log store's folder to fold the 33,000
  setup jobs (`settleStore`, which calls `foldNow` and does nothing on
  bbolt). Without it, that fold runs a second or so after the setup add,
  inside the timed loop, and at `speed.sh`'s b.N of 3 it adds its whole
  cost to the few adds measured. That is setup cost, not the add's.
  d1a4b6d7's benchmark has no such wait because bbolt does no background
  work.

The wait's effect, HEAD with and without it, 6 interleaved rounds at
`-benchtime 1s` ([fix3-settle.bench](phase1-gate-1g4/fix3-settle.bench),
[fix3-nosettle.bench](phase1-gate-1g4/fix3-nosettle.bench)):

| Figure | without the wait | with it | delta |
| --- | --- | --- | --- |
| sec/op | 288.7m ± 6% | 294.2m ± 4% | ~ (p=0.70) |
| B/op | 118.8Mi ± 14% | 110.9Mi ± 0% | -6.67% (p=0.002) |
| allocs/op | 635.7k ± 11% | 602.5k ± 0% | -5.23% (p=0.002) |

The add's own cost does not change; the wait removes the fold's share and
its round-to-round spread.

A third interleaved A/B against d1a4b6d7, same `speed.sh` command and
settings, 09:14 to 09:22 GMT:
[fix3-benchstat.txt](phase1-gate-1g4/fix3-benchstat.txt),
[fix3-raw.txt](phase1-gate-1g4/fix3-raw.txt), run directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g4/fix-speed/run-1794302053/`.

| Figure | base | HEAD + fix | delta |
| --- | --- | --- | --- |
| AddDepGroupMember sec/op | 378.7m | 276.9m | -26.87% (p=0.002) |
| AddDepGroupMember B/op | 102.2Mi | 110.9Mi | +8.54% (p=0.002) |
| AddDepGroupMember allocs/op | 1,175.7k | 602.5k | -48.75% (p=0.002) |
| dep-granularity add-sec | 518.0m | 329.5m | -36.39% (p=0.002) |
| dep-granularity peak RSS | 299.9Mi | 249.9Mi | -16.69% (p=0.002) |
| dep-granularity recovery-sec | 194.5m | 174.0m | ~ (p=0.62) |

**Verdict: PASS.** `speed.sh` reports no figure worse by more than 10% at
p<0.05, and every scenario met its thresholds. B/op is still 8.54% above
bbolt's, within the gate; the rest is the one copy of each record body the
flat decoder needs, since bbolt's decoder copies from its memory map
instead. Only `BenchmarkAddDepGroupMember` and `dep-granularity-check` were
rerun, not the whole of `make speed-full`.

Gates on the final code, each with a scratch `HOME` and every `OS_`
variable unset: `make lint` 0 issues; `make test` 1273 passed;
`WR_TEST_STORE=all make test` 1273 passed; `make race` 1273 passed.

### Fix round 4: review round 1 fixes, and the rest of the evidence

Review round 1 FAILed on these points; round 4 fixed them:

- `TestLogStoreReadBufsBound` now pins the cap. It measures the kept
  buffers' size after one read, then shows they are dropped with
  `readBufsMax` one byte smaller and kept with it equal. Mutants moving the
  cap to `>= 2*readBufsMax`, `>=` and `+1` are each killed.
- The stale wording above and in the checklist is corrected.
- `jobqueue/wal` gained `TestReadFrameInto`: one `FrameBuffers` reused
  across a small block, a plain frame and a larger block.
- `wal.ReadFrame`'s `sync.Pool`, which had no size cap, is dropped:
  `ReadFrame` again reads into new buffers, as it did before the fix. It now
  serves only recovery and checkpoint reads; the reads this fix is about go
  through the store's kept buffers instead. Recovery-sec is unchanged
  against base in this round's A/B (below).
- `historyRead.overlayRecord`'s comment says it keeps every overlay record of
  each frame it reads for the life of the read.

A fourth interleaved A/B against d1a4b6d7, 6 rounds, `speed.sh` quick with
`SPEED_BENCH='^Benchmark(ReadyBacklogSnapshot|KickUnderReserveLoad|ResumeUnderReserveLoad|SuspendUnderReserveLoad|AddManyUnderReserveLoad|AddDepGroupMember)$'`
and `SPEED_SCENARIOS="report-storm dep-granularity-check"`, 11:18 to 13:56
GMT: [fix4-benchstat.txt](phase1-gate-1g4/fix4-benchstat.txt), run directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g4/fix-speed/run-1794309506/`.

| Figure | base | HEAD + fix | delta |
| --- | --- | --- | --- |
| AddDepGroupMember sec/op | 374.4m | 282.7m | -24.49% |
| AddDepGroupMember B/op | 102.2Mi | 110.9Mi | +8.49% |
| report-storm jobs/s | 3,152 | 3,295 | +4.53% |
| report-storm max-archive-ms | 104.0 | 116.0 | ~ (p=0.59) |
| dep-granularity add-sec | 543.5m | 332.0m | -38.91% |
| dep-granularity peak RSS | 309.9Mi | 250.8Mi | -19.08% |
| dep-granularity recovery-sec | 205.0m | 194.0m | ~ (p=0.18) |
| Kick/Resume/Suspend/AddMany UnderReserveLoad sec/op | | | -48% to -87% |
| ReadyBacklogSnapshot sec/op | 1.468m | 1.402m | ~ |

**`speed.sh` verdict: FAIL, on 9 figures, none from this fix**
([fix4-regressions.txt](phase1-gate-1g4/fix4-regressions.txt)): the
reserve latency of the `*UnderReserveLoad` benchmarks (reserve-p50-us +244%
to +678% on Kick, Resume and Suspend; reserve-p99-us +64% to +121% on those
and +18% to +20% on AddMany) and Kick's allocs/op (+11.45%). Those
benchmarks were not part of 1g.4's earlier runs (`speed.sh`'s default set
does not include them), so this is their first comparison against d1a4b6d7.

To place them, `27c10a45` (HEAD before this fix) was compared with the fix
on the same four benchmarks, 6 interleaved rounds at `-benchtime 1s`, 14:00
to 16:47 GMT:
[fix4-attribution-benchstat.txt](phase1-gate-1g4/fix4-attribution-benchstat.txt).
Every sec/op, reserve-p50, reserve-p99, reserve-max and reserves/op figure is
unchanged (no significant difference). Kick's allocs/op is unchanged too.
The fix's only significant differences there are Suspend's B/op (+20.74%,
p=0.041, on spreads of ±14 to ±25%) and allocs/op (+9.75%, p=0.026), both
below base's. So the reserve-latency regressions belong to the log store
as it was at `27c10a45`, not to this fix. They need their own bugfix item;
they are not fixed here. They mirror the large sec/op gain: each op
finishes 4 to 8 times faster, and reserves contend with it more.

Rerun after the fix: `BenchmarkAddDepGroupMember`, `dep-granularity-check`,
report-storm and the `serve()`-based benchmarks above, which are the parts of
`make speed-full` that reach the log store. The rest are unaffected by it:
add-storm runs with `withBoltStore`, archive-rate and archive-ceiling open
bbolt through `initDB`, `AddJobs`, `UpdateJobState`, `ArchiveJobs`,
`ArchiveSpacedArrivals`, `ModifyLiveJobsReverseLookup` and
`RepGroupStatusDetails` open bbolt directly, and `JobCleanup*`,
`BuildSchedulerGroupsBacklog`, `RACCycleCounted`, `JobKey`, the limiter and
the queue benchmarks touch no store.

On round 4's code, `make lint` reports 0 issues, and `make test`,
`WR_TEST_STORE=all make test` and `make race` each pass with 1,274 tests
(scratch `HOME`, every `OS_*` variable unset). Review round 2 passed the
fix.

## Reserve latency fix

Fix round 4's A/B failed `speed.sh` on the reserve latency of the
`serve()`-based `*UnderReserveLoad` benchmarks, and on Kick's allocs/op. The
bugfix checklist is
[261110-reserve-latency-304bdcfa.md](../../bugfixes/261110-reserve-latency-304bdcfa.md).

### Root cause

Profiles and an instrumented copy of the queue (time from taking the
queue's lock to releasing it, per resumed item, and time in the store's
callback under it) on both trees, 1,000 resumes of 130KB-command jobs:

| Tree, load | op | hold per item | store callback | lock held | reserve p50 |
| --- | --- | --- | --- | --- | --- |
| base, no reservers | 68-85ms | 4.5-5.2us | 0.6us | 6-7% | |
| 09c38065, no reservers | 7-10ms | 6.0-7.5us | 3.9-5.0us | 71-82% | |
| base, 8 reservers | 75-97ms | 5.3-6.7us | 0.9-1.2us | 7% | 9-11us |
| 09c38065, 8 reservers | 10-19ms | 6.5-11.8us | 4.3-7.9us | 62-68% | 34-85us |

The mutex profile puts the contention on the queue's lock, between the
resume loop and the reserves; the log store's own locks contribute under
0.1ms in all. No lock is held across I/O: the wal writer holds its mutex
only to queue a frame. A runtime trace (`go tool trace -pprof=sched`) shows
the reserves are not left runnable for long; they wait inside
`sync.Mutex.Lock`. HEAD's reserve-p99 (1.05 to 1.35ms) sits just above Go's
1ms mutex starvation threshold.

Variants of the instrumented 09c38065 copy separate the causes:

| Variant | op | reserve p50 |
| --- | --- | --- |
| a 70us spin per item outside the lock (bbolt's pacing) | 105-112ms | 17-26us |
| that, and no store write in the callback | 88-96ms | 10-15us |
| no store write in the callback, no spin | 5-7ms | 29-49us |
| no store write, a 15us spin (lock held 13% of the time) | 25-29ms | 36-39us |
| a yield after each item | 35-91ms | 10-43us |
| no store write, a 5us spin, a yield after each item | 28-31ms | 3-5us |

So:

1. **The bulk loops starve reserves.** `kickJobs`, `resumeJobs` and
   `suspendJobs` take the queue's lock once per job and go straight on to
   the next job. Unlocking wakes one waiting reserve, which must then be
   scheduled before it can take the lock; the loop takes it again sooner,
   every time, until the reserve has waited 1ms and Go's mutex hands the
   lock over. bbolt's 70us encode per job, outside the lock, left a gap
   long enough for a waiter to get in. The log store has no such encode, so
   the gap fell to a few microseconds. Even with the lock held only 13% of
   the time, p50 stayed at 36-39us; a yield after each item brought it to
   3-5us.
2. **The log store writes under the queue's lock.** Its
   `queueJobChangeAhead`, which the server calls inside the queue's
   callback, encoded the state record and appended it to the wal: about
   3us per call uncontended, all allocation and encoding, and 8 to 13us
   with reserves running, against bbolt's 0.6us (bbolt encodes ahead, in
   `prepareJobChange`). Paced like bbolt, this alone doubled reserve-p50.
3. **AddManyUnderReserveLoad's p99 is a sampling artefact.** One add of
   10,000 jobs, 8 rounds per tree, twice, counting the reserves slower than
   each threshold per op:

   | Tree | reserves per op | over 200us | over 500us | over 1ms | p99 |
   | --- | --- | --- | --- | --- | --- |
   | base | 8,270-15,509 | 13-69 | 10-43 | 8-29 | 74-145us |
   | 09c38065 | 5,770-10,176 | 30-54 | 17-37 | 8-28 | 111-168us |

   The slow reserves per op are about as many as base's. The add takes half
   base's time, so it has fewer reserves in all, and the same slow reserves
   make a higher percentile. The add's own hold of the queue's lock
   (`AddMany`) causes 0.46s of contention over 3 rounds on HEAD against
   0.51s on base.
4. **Kick's allocs/op came from setup.** Kick's own allocations, measured
   around the op alone, are 52-63k on base, 53.5k on 09c38065 and 54.6k
   with this fix once the store has settled. Without settling they were
   67-145k: the log store's folder was still folding the 21,000 jobs the
   benchmark had just added, inside the timed window. That is the setup
   cost fix round 3 removed from `BenchmarkAddDepGroupMember`.

### Fix

- `kickJobs`, `resumeJobs` and `suspendJobs` call `yieldQueueTurn`
  (`runtime.Gosched`) after each job, so a reserve woken by the loop's
  unlock runs on that processor before the loop takes the lock again.
- The log store's `queueJobChangeAhead` now makes the change and takes its
  state record's seq and run state (under the job's read lock and
  `stateMu`, as before) and queues them. One goroutine at a time, started
  by the push that finds none running (`logChangeQueue`), encodes and
  appends the queued records. Seqs rank a key's records, so appending later
  changes nothing about what recovery or the store's state see. A queued
  record counts in the store's `writing`, so `flush` waits for it, and
  `close` waits for the queue to drain before it closes the writer. Every
  other write appends the changes queued before it first (`write`, under the
  queue's `appendMu`), so a write made after a change is durable no earlier
  than the change (added in review round 1, below). The spec's "Write path"
  says so.
- The `*UnderReserveLoad` benchmarks call `settleStore` before timing, as
  `BenchmarkAddDepGroupMember` does since fix round 3. It does nothing on
  bbolt; base's benchmarks are unchanged.

Structural tests, red on 09c38065's code:

- `TestBulkQueueOpsLetReservesIn` runs each bulk op over 200 jobs on one
  processor with GC off and begins a reserve after the first job. It
  requires the reserve to get a job before the op handles a third job.
  Before the fix the reserve ran only after all 200.
- `TestLogStoreJobChangeAppendedLater` (as rewritten in review round 1)
  holds the store's change drain, makes 100 `queueJobChangeAhead` calls,
  and requires each to return with its seq taken and its record queued,
  not appended: 100 queued, where 09c38065 appends each on the caller and
  queues none. It then checks all 100 are durable across a reopen. (Its
  first version counted wal appends during the calls; that count was
  process-wide and flaked.)

### Speed A/B of the four benchmarks against d1a4b6d7

Run on 2026-11-10, 19:15 to 20:47 GMT, on farm22-wrstat01, base and HEAD
interleaved, 6 rounds of each at `-benchtime 1s`:

```bash
(unset $(compgen -v | grep '^OS_'); HOME=<scratch> \
 SPEED_BASE=<scratch>/base-d1a4b6d7 SPEED_DIR=/nfs/hgi/wr/sb10-bigdb/customdb/1g4/rl-speed \
 SPEED_BENCH='^Benchmark(KickUnderReserveLoad|ResumeUnderReserveLoad|SuspendUnderReserveLoad|AddManyUnderReserveLoad)$' \
 SPEED_COUNT=6 SPEED_SCENARIOS=none developers/speed.sh quick)
```

Results: [rl-benchstat.txt](phase1-gate-1g4/rl-benchstat.txt), verdict
[rl-verdict.txt](phase1-gate-1g4/rl-verdict.txt); the run directory is
`/nfs/hgi/wr/sb10-bigdb/customdb/1g4/rl-speed/run-1794338151/`.

| Figure | Kick | Resume | Suspend | AddMany norc | AddMany rc |
| --- | --- | --- | --- | --- | --- |
| sec/op | -47.05% | -53.11% | -58.42% | -48.57% | -48.56% |
| reserve-p50-us | ~ (+1.5%) | ~ (+3.8%) | ~ (-18.1%) | ~ (+7.8%) | ~ (+1.4%) |
| reserve-p99-us | -78.14% | -74.52% | -82.91% | **+10.29% (p=0.009)** | ~ (+5.4%) |
| reserve-max-us | -60.03% | -57.65% | -74.78% | ~ | ~ |
| B/op | -96.71% | -97.67% | -97.85% | -4.73% | -5.11% |
| allocs/op | -10.17% | -8.50% | -13.73% | -53.69% | -49.28% |

Fix round 4's figures for the same benchmarks were reserve-p50 +244% to
+678%, reserve-p99 +64% to +121% (+18% to +20% on AddMany) and Kick
allocs/op +11.45%. Every Kick, Resume and Suspend figure now passes, most of
them far better than base: their reserve-p99 is a quarter of bbolt's.

**`speed.sh` verdict: FAIL, on one figure**, AddManyUnderReserveLoad/norc's
reserve-p99 (+10.29%). The fix as it stood for this run changed nothing
an add runs (only the job-change write and the bulk loops), and this is
the sampling artefact of point 3 above. Counting the add's slow reserves on
this fix's tree against d1a4b6d7, both run at once, 4 rounds of each
sub-benchmark ([rl-addmany-tail.txt](phase1-gate-1g4/rl-addmany-tail.txt)):

| Tree | reserves per op | over 150us | over 500us | over 1ms |
| --- | --- | --- | --- | --- |
| base | 11,263-20,137 | 61-194 (mean 105) | 21-94 (mean 35) | 12-25 (mean 18) |
| this fix | 7,222-10,992 | 56-125 (mean 89) | 17-49 (mean 31) | 14-31 (mean 22) |

Per add, the add holds up about as many reserves as bbolt's (over 1ms, a
mean of 22 against 18 here, and 16 against 17 in review round 2's runs
below): its p99 is higher because the add is twice as fast, so its window
holds 40% fewer reserves, and about the same number of slow ones make a
higher percentile.

Over wall time the picture differs. The add takes half as long, so the
rate of slow reserves while an add runs is about double base's: over 1ms,
about 73 a second against 32 here (55 against 30 in round 2's runs); over
150us, about 297 a second against 184 (279 against 182). Judged per add,
the add costs reserves no more than bbolt's; judged per second of add,
reserves are held up up to twice as often, for half as long. Which view
the gate should take is the gate owner's call; the gate was not changed.

Review asked whether the add path has the pattern fixed above. It does
not:

- The add takes the queue's lock once, in `queue.AddMany`, and prepares
  every item under that one hold; it never releases and re-takes the lock
  per item. The queue package is unchanged since d1a4b6d7.
- Under that hold it calls only server code that is the same on both trees
  (`prepareForReady`, `recomputeJobRequirementsLocked`, `prepareReadyJob`;
  `prepareBeforeQueueing` and `warmReqGroupRecs` run before it). The one
  store call there, `cachedReqGroupRecs`, reads the same `reqStatCache` on
  both stores, with no I/O. The log store writes the add's records before
  the items are queued, outside the lock, as bbolt does.
- Timing the `AddMany` hold in instrumented copies, one add of 10,000 jobs
  with 8 reservers, settled, 8 rounds each: 16.3 to 18.9ms on base, 14.1
  to 21.7ms on this fix (7 of 8 rounds at 14.1 to 16.2ms). Slow reserves
  per op in the same runs: over 150us, 68-150 (mean 104) on base against
  58-110 (mean 81) on this fix; over 1ms, 9-35 (mean 17) against 11-24
  (mean 16).

So nothing on the add path holds the queue's lock longer than base does,
and per add it holds up about as many reserves; the p99 figure, and the
per-add or per-second view of it, are left to the gate owner.

### The settle step's effect

This fix's code with and without the `settleStore` call in the four
benchmarks, 6 interleaved rounds at `-benchtime 1s`, 21:28 to 22:45 GMT,
`speed.sh` with `SPEED_BASE` a copy of this tree without the call:
[rl-settle-benchstat.txt](phase1-gate-1g4/rl-settle-benchstat.txt), run
directory `/nfs/hgi/wr/sb10-bigdb/customdb/1g4/rl-speed/run-1794346126/`.

| Figure | Kick | Resume | Suspend | AddMany norc | AddMany rc |
| --- | --- | --- | --- | --- | --- |
| sec/op | ~ | ~ | -12.86% | ~ | ~ |
| B/op | -62.07% | -70.90% | -74.67% | -0.81% | -0.81% |
| allocs/op | -31.58% (±21% to ±3%) | -42.06% (±38% to ±3%) | -46.16% (±26% to ±1%) | ~ | ~ |
| reserve-p50-us | +21.17% (p=0.026) | +38.85% (p=0.041) | ~ | ~ | ~ |
| reserve-p99-us | ~ | ~ | -22.53% | ~ | ~ |

Without the wait, the fold of the setup's 21,000 adds lands in the timed
window: it adds about 10MB and 40,000 allocations to each kick, resume or
suspend, and its spread (±21% to ±38%) to theirs. The add benchmarks are
unchanged by it, their own add being far larger than the fold's share.
Settling also raises Kick's and Resume's reserve-p50 against the unsettled
runs, to about base's level (against d1a4b6d7, settled: Kick 9.56us
against 9.42us, Resume 12.59us against 12.13us, both not significant);
unsettled, both were below base's. So the wait hides no regression in
reserve latency; it removes setup work that had made HEAD look better
there and worse on allocations.

### Rerun of the rest of 1g.4's log store figures

`BenchmarkAddDepGroupMember`, `report-storm` and `dep-granularity-check`
against d1a4b6d7, 6 interleaved rounds each, 20:48 to 20:58 GMT, the fix
round 4 command with `SPEED_BENCH='^BenchmarkAddDepGroupMember$'`,
`SPEED_SCENARIOS="report-storm dep-granularity-check"` and
`SPEED_SCENARIO_COUNT=6`:
[rl-step6-benchstat.txt](phase1-gate-1g4/rl-step6-benchstat.txt), run
directory `/nfs/hgi/wr/sb10-bigdb/customdb/1g4/rl-speed/run-1794343739/`.

| Figure | base | HEAD + fix | delta |
| --- | --- | --- | --- |
| AddDepGroupMember sec/op | 359.5m | 284.5m | -20.85% |
| AddDepGroupMember B/op | 102.0Mi | 110.9Mi | +8.68% |
| AddDepGroupMember allocs/op | 1,175.7k | 602.5k | -48.75% |
| report-storm jobs/s | 3,106 | 3,268 | ~ (p=0.13) |
| report-storm max-archive-ms | 127.0 | 120.0 | ~ (p=0.97) |
| report-storm wall-sec | 6.44 | 6.12 | ~ (p=0.13) |
| dep-granularity add-sec | 515.0m | 326.5m | -36.60% |
| dep-granularity peak RSS | 304.7Mi | 246.5Mi | -19.09% |
| dep-granularity recovery-sec | 182.5m | 182.0m | ~ (p=0.70) |

**`speed.sh` verdict: PASS**, and every scenario round met its thresholds.

### Gates

On the first round's code, each with a scratch `HOME` and every `OS_` variable
unset, 20:58 to 21:25 GMT: `make lint` reports 0 issues, and `make test`,
`WR_TEST_STORE=all make test` and `make race` each pass with 1,276 tests.
Each of 7 mutants of the fix, each in a plain copy and each compiling, is
killed (see the checklist). The incidental `TestReadFrameInto` gap is
closed in the same checklist.

### Review round 1 fixes, and the A/B on their code

Review round 1 found five things, fixed as follows (details in the
checklist):

- `TestLogStoreJobChangeAppendedLater` could count another store's wal
  appends (`logAppendedHook` is process-wide) and flaked once under load.
  It now holds this store's change drain with a hook, checks that all 100
  changes return with their seqs taken and their records queued, then
  releases the drain and checks the records are durable across a reopen.
  A reservation of one of the jobs made while its change is queued appends
  the queued changes first and takes a later seq, so it is the record kept;
  that checks wal order and seq agree, not that seq outranks wal order,
  which `TestLogStoreStateSeqOrder` covers (two state records of a job
  appended in reverse seq order; the later seq is kept before and after a
  reopen), as do `TestLogStoreReducerOrder` and `TestLogStoreReducerSeqRules`
  for the in-memory reducer. It
  passed `-race -count 100` three times while a `-race -count 50` run of
  the `TestLogStore*`, `TestLogHeld*` and `TestBulkQueueOpsLetReservesIn`
  tests loaded the machine.
- A panic appending a queued change exited the drain without closing it,
  wedging flush, close and later adds of the key. `appendChange` now
  recovers it per record, logs it at Crit with its stack and treats it as
  a failed write (the record is lost, its key unnoted), and the drain goes
  on; `TestLogStoreJobChangeAppendPanic` covers it.
- `settleStore` now calls the store's `flush`, so the change queue is
  drained too (the prepare step's bury, suspend and delay changes), not
  only the fold.
- `queueJobChangeAhead` and `lockedStateFrames` share `takeStateLocked`.
- Comments and the wording above ("about as many", and the per-second view
  of the add's slow reserves).

The gates on those fixes found a defect in the deferred append itself:
`make test` failed `TestResumeAfterChangeWritesResumedJob` once (Expected
"ready", Actual "suspended"). That test relies on a durable write made
after a change committing no earlier than the change, which held while the
change was appended by its caller; with the change queued, the later write
could be appended, and be durable, first. `write` now appends the changes
queued before it first, under the queue's `appendMu`, which the drain also
holds while it appends a batch, so appends again follow the order of calls.
`TestLogStoreWriteAfterQueuedChange` pins it, and
`TestLogStoreCloseWaitsForQueuedChanges` pins `close` waiting for the queue
(the older `TestLogHeldCloseDrains` stopped catching that once writes
append queued changes). The first A/B below was run before this change; the
final A/B after it.

The four benchmarks against d1a4b6d7 again, on this code, 6 interleaved
rounds, 23:26 GMT on 2026-11-10 to 00:54 GMT on 2026-11-11, same command:
[rl-r1-benchstat.txt](phase1-gate-1g4/rl-r1-benchstat.txt), verdict
[rl-r1-verdict.txt](phase1-gate-1g4/rl-r1-verdict.txt), run directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g4/rl-speed/run-1794353174/`.

| Figure | Kick | Resume | Suspend | AddMany norc | AddMany rc |
| --- | --- | --- | --- | --- | --- |
| sec/op | -49.40% | -56.84% | -60.04% | -50.13% | -49.16% |
| reserve-p50-us | ~ (-4.8%) | ~ (-22.0%) | -18.36% | ~ (+15.3%) | ~ (+2.9%) |
| reserve-p99-us | -70.35% | -80.14% | -73.45% | **+17.46% (p=0.004)** | **+16.11% (p=0.026)** |
| reserve-max-us | -63.04% | -66.93% | -78.98% | ~ | ~ |
| B/op | -96.74% | -97.72% | -97.76% | -4.97% | -4.83% |
| allocs/op | -10.24% | ~ (-7.3%) | -9.76% | -54.15% | -48.49% |

**`speed.sh` verdict: FAIL, on the two AddMany reserve-p99 figures** (+17%
and +16%; +10% and +5% in the first A/B). The AddMany figures moved within
their spread (±8% to ±18%).
They are the per-add against per-second question above, left to the gate
owner. Every Kick, Resume and Suspend figure passes.

### Final A/B, on the code with all fixes

The four benchmarks, `BenchmarkAddDepGroupMember`, report-storm and
dep-granularity-check against d1a4b6d7, one `speed.sh` run, 6 interleaved
rounds of each, 01:39 to 03:19 GMT on 2026-11-11 (the fix round 4 command
with `SPEED_BENCH='^Benchmark(KickUnderReserveLoad|ResumeUnderReserveLoad|SuspendUnderReserveLoad|AddManyUnderReserveLoad|AddDepGroupMember)$'`,
`SPEED_SCENARIOS="report-storm dep-granularity-check"` and
`SPEED_SCENARIO_COUNT=6`):
[rl-final-benchstat.txt](phase1-gate-1g4/rl-final-benchstat.txt), run
directory `/nfs/hgi/wr/sb10-bigdb/customdb/1g4/rl-speed/run-1794361157/`.

| Figure | Kick | Resume | Suspend | AddMany norc | AddMany rc |
| --- | --- | --- | --- | --- | --- |
| sec/op | -50.09% | -57.20% | -62.60% | -50.12% | -49.76% |
| reserve-p50-us | ~ (-9.9%) | ~ (-18.7%) | -21.47% | ~ (-1.6%) | ~ (+3.1%) |
| reserve-p99-us | -65.93% | -76.32% | -80.11% | ~ (+9.7%, p=0.065) | ~ (+12.6%, p=0.093) |
| reserve-max-us | -52.53% | -63.10% | -84.00% | ~ | ~ |
| B/op | -96.74% | -97.79% | -97.80% | -5.24% | -4.85% |
| allocs/op | -11.31% | -10.27% | -13.88% | -54.60% | -48.92% |

| Figure | base | HEAD + fix | delta |
| --- | --- | --- | --- |
| AddDepGroupMember sec/op | 380.7m | 290.0m | -23.83% |
| AddDepGroupMember B/op | 102.2Mi | 110.9Mi | +8.48% |
| AddDepGroupMember allocs/op | 1,175.7k | 602.5k | -48.75% |
| report-storm jobs/s | 3,150 | 3,265 | +3.66% |
| report-storm max-archive-ms | 116.5 | 103.5 | ~ |
| dep-granularity add-sec | 526.5m | 347.5m | -34.00% |
| dep-granularity peak RSS | 303.3Mi | 254.2Mi | -16.19% |
| dep-granularity recovery-sec | 195.0m | 192.0m | ~ |

**`speed.sh` verdict: PASS**, and every scenario round met its thresholds.
The AddMany reserve-p99 figures are 10-13% above base in this run but not
significant; across the three A/Bs they were +10.29%/+5.4%, +17.46%/+16.11%
and +9.7%/+12.6%. Since review round 1 an add's write goes through the
changed `write`: it loads the change queue's `queued` count, and would
append any changes queued before it (none are, during the benchmark's add).
That is the only change on the add's path, and the add's hold of the
queue's lock measured in review round 2 is no longer than base's, so this
is still the per-add against per-second question above, for the gate
owner.

The settle step's effect table above was measured with round 0's
`settleStore` (fold only); `settleStore` now also drains the change queue,
which only moves the prepare step's queued changes, a few thousand state
records, out of the timed window.

Gates on the final code, each with a scratch `HOME` and every `OS_`
variable unset, 01:10 to 01:39 GMT on 2026-11-11: `make lint` 0 issues;
`make test`, `WR_TEST_STORE=all make test` and `make race` each pass with
1,279 tests.

### Review round 2 fixes (tests and docs)

Review round 2 found no deadlock, ordering or durability defect, but gaps in
the tests, which are now filled (each with a mutant it kills; see the
checklist):

- `TestLogStoreWriteWaitsForBatchBeingAppended` pins that a write made
  while the drain is part way through a batch of queued changes waits for
  the rest of the batch (`appendMu`, and `queued` falling only after each
  append).
- `TestLogStoreRevivingJobChange` pins that a change of a key a write in
  flight is making live is appended once that write has finished.
- `TestLogStoreDrainHoldsCloseMu` pins that the drain appends holding
  `closeMu`, as the spec's "Write path" requires of every append.
- A change's write that panics after counting itself in `writing` now
  gives the count back, and a `finished` that panics is recovered
  (`TestLogStoreJobChangeWritePanic`, `TestLogStoreJobChangeFinishPanic`).
- The drain-holding test helper releases the drain before the store
  closes, so no change test waits out the hold's 10s cap.

Gates on this code, each with a scratch `HOME` and every `OS_` variable
unset, 05:11 to 05:39 GMT on 2026-11-11: `make lint` reports 0 issues, and
`make test`, `WR_TEST_STORE=all make test` and `make race` each pass with
1,284 tests.

The panic give-back adds a flag to the write path (`writeFramesCounted`),
so the four benchmarks were run once more against d1a4b6d7, 6 interleaved
rounds, 05:39 to 07:12 GMT:
[rl-r2-benchstat.txt](phase1-gate-1g4/rl-r2-benchstat.txt), run directory
`/nfs/hgi/wr/sb10-bigdb/customdb/1g4/rl-speed/run-1794375551/`.

| Figure | Kick | Resume | Suspend | AddMany norc | AddMany rc |
| --- | --- | --- | --- | --- | --- |
| sec/op | -50.10% | -51.22% | -63.17% | -47.27% | -49.59% |
| reserve-p50-us | ~ | ~ | ~ | ~ | ~ |
| reserve-p99-us | -73.98% | -72.81% | -82.54% | ~ (+15.5%) | **+23.37% (p=0.004)** |
| reserve-max-us | -72.63% | -62.95% | -63.04% | ~ | -19.82% |
| allocs/op | -10.08% | ~ | -11.98% | -53.59% | -49.23% |

**`speed.sh` verdict: FAIL, on AddManyUnderReserveLoad/rc reserve-p99**
(+23%). Every Kick, Resume and Suspend figure passes as before. The add's
reserve-p99 (norc/rc) has now been +10%/+5% (FAIL), +17%/+16% (FAIL),
+10%/+13% (PASS) and +16%/+23% (FAIL) across four A/Bs. An add pays only the checks the changed write
path makes (the `queued` load, and the `counted` flag in
`writeFramesCounted`), and its hold of the queue's lock is no longer than
base's, so this is the per-add against per-second question above, left to
the gate owner.

### Review round 3 fixes (tests and docs)

Two more tests pin the change queue's drain:
`TestLogStoreChangeQueuedDuringBatch` (a change queued while the drain appends an earlier batch is appended by
that drain, not stranded) and `TestLogStoreChangeAfterClose` (a change
queued after close is dropped and leaves no key noted as written and
nothing counted in `writing`). Each kills the mutant review round 3 found
surviving (see the checklist). No product code changed in this round but a
comment, so the speed A/B was not rerun.

Gates on this code, scratch `HOME`, every `OS_` variable unset, 07:36 to
08:46 GMT on 2026-11-11: `make lint` 0 issues; `make test` and `make race`
1,286 passed each. `WR_TEST_STORE=all make test` failed once on
`TestClientAddAndWaitCancelledDuringOutage` (Line 252: the cancelled
AddAndWait did not return within 3.8s), a known flake: in a load-matched
A/B, this tree and 09c38065 run side by side 105 times each, it failed 1
time on this tree and 3 times on 09c38065, always at that assertion. The
rerun passed with 1,286 tests.
