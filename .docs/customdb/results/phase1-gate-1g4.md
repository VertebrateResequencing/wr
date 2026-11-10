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
