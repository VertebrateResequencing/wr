# Hot-path speed across versions (2026-10-01)

These are `make speed` measurements of four trees:

| Tree | Commit |
|---|---|
| v0.37.2 | `b2f0ff97` |
| v0.38.0 | `499b350e` |
| develop | `e8751718`, with #650-#652 and #656-#658 |
| #655 head | `31d5ad10` (`fix-dep-group-rerun-gaps`) |

This report lists the regressions and does not fix them. Each one needs its
own bugfix item. The one develop → #655 regression was fixed in #655 before it
merged (see below).

## Method

- Every comparison is one `developers/speed.sh` session on this 8-CPU host,
  run under `nice -n 19` with nothing else heavy running. A session compares
  two adjacent trees, base then head:
  1. v0.37.2 → v0.38.0
  2. v0.38.0 → develop
  3. develop → #655
- In each session, the base and head rounds alternate. Benchmarks ran 6
  rounds at `-benchtime 1s`. `report-storm` (20000 jobs, 500 runners) and
  `dep-granularity-check` (default 30000-waiter fixture) also ran 6 rounds.
- benchstat compared each pair. "~" means not significant at p < 0.05.
  Deltas are head relative to base. A tree's value comes from the first
  session it was measured in.
- The big-DB scenarios ran once per tree:
  - `add-storm` on `/nfs/hgi/wr/sb10-bigdb/prod.db`
  - `archive-rate` on `pristine10`
  - `archive-ceiling` on `pristine6`

  With one round, benchstat cannot call any of their differences
  significant.
- To confirm a suspect from those single runs, another session repeated
  that scenario 4 to 12 rounds.
- Every scenario used this branch's `developers/wrdev.sh`, pointed at each
  tree with `WRDEV_REPO`. It built that tree's `wr` and ran that tree's
  `reliability_repro` tests.

### What was copied into older trees

All copies are test files and were made only in the temporary worktrees under
`/nfs/hgi/wr/sb10-bigdb/speed/trees/`:

- All four trees got `queue/queue_bench_test.go` (`BenchmarkQueueLifecycle`),
  which this branch adds.
- v0.38.0 got develop's `jobqueue/status_summary_decode_test.go`, for
  `BenchmarkRepGroupStatusDetails`. It compiles unchanged.
- v0.37.2 got:
  - develop's `behaviours_bench_test.go` (`BenchmarkJobCleanupDepth*`).
  - `BenchmarkJobKey` without the `ContainerImageUser` field, which v0.37.2
    lacks.
  - `BenchmarkRepGroupStatusDetails` without its `decodes/op` counter.
  - develop's report-storm, add-storm, archive-rate and archive-ceiling tests,
    plus the helpers they need: `envIntDefault`, `reliable4GiB`, the `wsf*`
    helpers, and `slowRequestThreshold` at its 10s default. `archiveJob`
    calls take the extra `ctx` argument they need in v0.37.2.
- v0.37.2 has no generator for the dep-granularity fixture, so its
  `dep-granularity-check` ran on a fixture that develop built, with the same
  30000/3000/6300 shape (`WRDEV_DEPGRAN_DB`).
- `BenchmarkArchiveSpacedArrivals` (needs `newArchiveTxRecorder`) and
  `BenchmarkReadyBacklogSnapshot` (needs `Server.racScanWork`) cannot be built
  against v0.37.2, so they have no v0.37.2 value.
- `BenchmarkArchiveJobs` gained a transaction recorder in #555, so part of
  its v0.37.2 → v0.38.0 B/op and allocs/op change may come from the
  benchmark rather than the server.

## Benchmarks

The table shows sec/op for every benchmark. Other units appear only where some
step changed significantly.

| Benchmark | unit | v0.37.2 | v0.38.0 | develop | #655 | v0.37.2→v0.38.0 | v0.38.0→develop | develop→#655 |
|---|---|---|---|---|---|---|---|---|
| JobCleanup | sec/op | 298 µs | 347 µs | 352 µs | 349 µs | +16.55% (p=0.002) | ~ | ~ |
| JobCleanupDepth1 | sec/op | 3.05 ms | 4.03 ms | 3.97 ms | 3.94 ms | +31.98% (p=0.002) | ~ | ~ |
| JobCleanupDepth8 | sec/op | 3.19 ms | 4.25 ms | 4.26 ms | 4.26 ms | +33.41% (p=0.002) | ~ | ~ |
| AddJobs | sec/op | 144 ms | 179 ms | 177 ms | 175 ms | +23.99% (p=0.002) | ~ | ~ |
| UpdateJobState | sec/op | 68.6 ms | 59.8 ms | 60.7 ms | 57.7 ms | -12.88% (p=0.002) | ~ | ~ |
| ArchiveJobs | sec/op | 707 ms | 274 ms | 283 ms | 266 ms | -61.30% (p=0.002) | ~ | ~ |
| ArchiveSpacedArrivals | sec/op | - | 2.47 s | 2.47 s | 2.47 s | n/a | ~ | ~ |
| ModifyLiveJobsReverseLookup | sec/op | 11.6 ms | 11.6 ms | 11.6 ms | 11.5 ms | ~ | ~ | ~ |
| JobKey | sec/op | 11 µs | 29.1 ns | 29.4 ns | 29.3 ns | -99.74% (p=0.002) | ~ | ~ |
| ReadyBacklogSnapshot | sec/op | - | 1.73 ms | 1.66 ms | 1.68 ms | n/a | ~ | ~ |
| RepGroupStatusDetails/1000 | sec/op | 17.6 ms | 18 ms | 3.21 ms | 3.28 ms | ~ | -82.74% (p=0.002) | ~ |
| RepGroupStatusDetails/10000 | sec/op | 178 ms | 191 ms | 40.3 ms | 39.5 ms | ~ | -78.80% (p=0.002) | ~ |
| QueueLifecycle/groups=1 | sec/op | 2.8 ms | 2.78 ms | 2.78 ms | 2.81 ms | ~ | ~ | ~ |
| QueueLifecycle/groups=100 | sec/op | 1.92 ms | 1.94 ms | 1.96 ms | 1.94 ms | ~ | ~ | ~ |
| LimiterIncDec | sec/op | 1.94 µs | 3.06 µs | 3.19 µs | 3.08 µs | +57.95% (p=0.002) | ~ | ~ |
| LimiterCapacity | sec/op | 1.7 µs | 2.87 µs | 3.02 µs | 2.92 µs | +68.33% (p=0.002) | ~ | ~ |
| JobCleanup | B/op | 6.46 KiB | 11.2 KiB | 11.2 KiB | 11.1 KiB | +72.70% (p=0.002) | ~ | ~ |
| JobCleanupDepth1 | B/op | 17.6 KiB | 127 KiB | 127 KiB | 127 KiB | +621.69% (p=0.002) | ~ | ~ |
| JobCleanupDepth8 | B/op | 18.7 KiB | 139 KiB | 139 KiB | 139 KiB | +644.32% (p=0.002) | ~ | ~ |
| AddJobs | B/op | 27.1 MiB | 31.2 MiB | 30.2 MiB | 30.2 MiB | +15.19% (p=0.002) | ~ | ~ |
| UpdateJobState | B/op | 13.9 MiB | 22 MiB | 21.9 MiB | 21.7 MiB | +58.48% (p=0.002) | ~ | ~ |
| ArchiveJobs | B/op | 27.9 MiB | 41.7 MiB | 41.4 MiB | 42.6 MiB | +49.68% (p=0.002) | ~ | ~ |
| ModifyLiveJobsReverseLookup | B/op | 126 KiB | 99.1 KiB | 99.3 KiB | 99.2 KiB | -21.28% (p=0.002) | ~ | ~ |
| JobKey | B/op | 21.4 KiB | 0 B | 0 B | 0 B | -100.00% (p=0.002) | ~ | ~ |
| RepGroupStatusDetails/1000 | B/op | 22.6 MiB | 22.7 MiB | 290 KiB | 290 KiB | +0.71% (p=0.002) | -98.75% (p=0.002) | ~ |
| RepGroupStatusDetails/10000 | B/op | 226 MiB | 228 MiB | 3.06 MiB | 3.06 MiB | +0.71% (p=0.002) | -98.66% (p=0.002) | ~ |
| LimiterIncDec | B/op | 544 B | 1.09 KiB | 1.09 KiB | 1.09 KiB | +105.88% (p=0.002) | ~ | ~ |
| LimiterCapacity | B/op | 632 B | 1.21 KiB | 1.21 KiB | 1.21 KiB | +96.20% (p=0.002) | ~ | ~ |
| JobCleanup | allocs/op | 135 | 203 | 203 | 203 | +50.37% (p=0.002) | ~ | ~ |
| JobCleanupDepth1 | allocs/op | 540 | 2194 | 2194 | 2194 | +306.30% (p=0.002) | ~ | ~ |
| JobCleanupDepth8 | allocs/op | 596 | 2376 | 2376 | 2376 | +298.66% (p=0.002) | ~ | ~ |
| AddJobs | allocs/op | 141.5k | 149.0k | 145.7k | 145.7k | +5.30% (p=0.002) | ~ | ~ |
| UpdateJobState | allocs/op | 136.7k | 128.6k | 127.8k | 128.8k | -5.88% (p=0.002) | ~ | ~ |
| ArchiveJobs | allocs/op | 288.6k | 325.8k | 325.1k | 329.5k | +12.88% (p=0.002) | ~ | ~ |
| ModifyLiveJobsReverseLookup | allocs/op | 711 | 595 | 597 | 597 | -16.32% (p=0.002) | ~ | ~ |
| JobKey | allocs/op | 4 | 0 | 0 | 0 | -100.00% (p=0.002) | ~ | ~ |
| RepGroupStatusDetails/1000 | allocs/op | 23.8k | 23.8k | 16.8k | 16.8k | ~ | -29.58% (p=0.002) | ~ |
| RepGroupStatusDetails/10000 | allocs/op | 270.8k | 270.9k | 200.8k | 200.8k | ~ | -25.85% (p=0.002) | ~ |
| LimiterIncDec | allocs/op | 9 | 16 | 16 | 16 | +77.78% (p=0.002) | ~ | ~ |
| LimiterCapacity | allocs/op | 11 | 19 | 19 | 19 | +72.73% (p=0.002) | ~ | ~ |
| AddJobs | bolt_writes/job | 1.191 | 1.135 | 1.073 | 1.073 | -4.70% (p=0.002) | ~ | ~ |
| UpdateJobState | bolt_writes/job | 0.889 | 0.852 | 0.833 | 0.856 | -4.18% (p=0.002) | ~ | ~ |
| ArchiveJobs | bolt_writes/job | 2.665 | 3.158 | 3.148 | 3.226 | +18.52% (p=0.002) | ~ | ~ |

## Scenarios

`report-storm` and `dep-granularity-check` show medians of 6 rounds.

| Scenario | metric | v0.37.2 | v0.38.0 | develop | #655 | v0.37.2→v0.38.0 | v0.38.0→develop | develop→#655 |
|---|---|---|---|---|---|---|---|---|
| report-storm | jobs/s | 931 | 923 | 831 | 916 | -0.9% (p=0.006) | ~ | ~ |
| report-storm | max archive ms | 102 | 83 | 92 | 97 | ~ | ~ | ~ |
| dep-granularity-check | peak RSS | >16 GB, aborted | 293 MiB | 285 MiB | 303 MiB | see below | ~ | +5.5% (p=0.032) |
| dep-granularity-check | recovery s | not reached | 0.172 | 0.194 | 0.152 | see below | +12.8% (p=0.013), not confirmed | -11.4% (p=0.032) |
| dep-granularity-check | add s | not reached | 0.473 | 0.501 | 0.556 | see below | ~ | **+22.9% (p=0.009), confirmed** |

The big-DB scenarios ran one round per tree:

| Scenario | metric | v0.37.2 | v0.38.0 | develop | #655 |
|---|---|---|---|---|---|
| add-storm (700 clients) | adds/s | 13.2 | 263.6 | 271.6 | 265.0 |
| add-storm | p50 / p99 / max ms | 47010 / 60001 / 60090 | 135 / 5526 / 6327 | 134 / 5527 / 6190 | 176 / 5110 / 6154 |
| add-storm | txns/add | 0.95 | 0.15 | 0.15 | 0.12 |
| archive-rate (660 archivers) | archives/s | 17.6 | 173.2 | 173.1 | 173.7 |
| archive-rate | mean / p99 / max ms | 39232 / 53946 / 54194 | 14 / 187 / 1459 | 16 / 311 / 1912 | 3 / 12 / 76 |
| archive-ceiling (20 → 1143) | high archives/s | 27.0 | 468.0 | 479.8 | 471.5 |
| archive-ceiling | throughput factor | 3.53 | 54.0 | 55.3 | 54.2 |
| archive-ceiling | p99 / max ms | 81417 / 81733 | 1611 / 2444 | 969 / 1452 | 1397 / 1946 |

v0.37.2 fails all three gates. It shows production's pre-0.38.0 symptoms: a
47s median add, a 39s mean archive, and a 3.5x throughput factor for 57x the
concurrency. On the 30000-waiter dep-granularity fixture, v0.37.2's manager
grew to the 16 GB abort ceiling in all 3 rounds before it reached recovery.
v0.38.0 peaked at 0.3 GB.

## Regressions over 10% and significant

### v0.37.2 → v0.38.0

Each cause comes from measuring every first-parent commit between the two tags
that touches `jobqueue/db.go` or `jobqueue/behaviours.go`, with 3 rounds each
(`timeline.txt`). Two further steps were narrowed by bisecting the commits that
touch neither file.

1. **Limiter `Increment`/`Decrement`: +58-68% sec/op, about 2x B/op, +75%
   allocs/op**, about +1.1 µs a call.
   - Cause: #555 (`4e5739fc`). Allocs go from 9/11 at its parent `9091b514`
     to 16/19. #602 changes nothing.
   - #555 stopped the limiter holding its mutex over the
     `SetLimitCallback` DB read: `withResolvedGroups` builds a resolved-group
     map and a closure on every call. That is the trade DEVELOPERS.md rule 1
     asks for. A fix would have to keep the lock-free callback and cut the
     per-call allocations.
2. **Job cleanup (`JobCleanup*`): +17-33% sec/op, 1.7-7x B/op, 1.5-4x
   allocs/op.**
   - Cause: #575 (`db108fa8`, "Stop a job's cleanup deleting nested jobs'
     work and live mounts"). Depth1 goes from 540 allocs and 2.99 ms at
     `a16e906d` to 2190 allocs and 4.05 ms. Nothing else moves it.
3. **`AddJobs`: +24% sec/op**, 146 → 174 ms for 3000 jobs added in one call.
   - Cause: #555 (`4e5739fc`). The jump happens there and nowhere else.
   - #555 moved adds onto the single coalescing writer. Under concurrency,
     the same change took add-storm from 13 to 264 adds/s and its median
     from 47s to 135 ms. So this is the cost to one large serial add of
     handing off to the writer, not a loss of throughput.
4. **`AddJobs` +15-27% B/op, `UpdateJobState` +58% B/op, `ArchiveJobs` part
   of its +50% B/op**, with no time cost.
   - Cause: #590 (`0cc79218`, "Run containerised commands as you, not as
     the image's user"). From its parent `4028ed4c`:
     - `AddJobs` 24.2 → 30.8 MiB
     - `UpdateJobState` 14.4 → 22.4 MiB
     - `ArchiveJobs` 35.0 → 42.3 MiB
   - Each is about 2.7 KiB more per job and op. #590 adds
     `Job.ContainerImageUser` and changes `containerImageKey`. Which of
     those allocates is not yet known.
   - The rest of `ArchiveJobs`' B/op rise, 27.9 → 35.4 MiB, lands in #555,
     which also added the benchmark's transaction recorder.
5. **`ArchiveJobs`: +18.5% bolt writes/job, +17.6% pages/job, +12.9%
   allocs/op.**
   - Cause: #555 (`4e5739fc`): 2.665 → 3.166 writes/job.
   - The same change cut `ArchiveJobs` time by 61%, and archive-rate and
     archive-ceiling throughput by 10-17x. The extra pages are what the
     single archive writer costs.

### v0.38.0 → develop

None confirmed.

- `dep-granularity-check` recovery was +12.8% (p=0.013) at 6 rounds. A
  12-round re-run showed 172 → 182 ms, p=0.535, not significant.
- archive-rate's single-run p99 was 187 ms on v0.38.0 against 311 ms on
  develop. A 4-round interleaved re-run showed no difference: mean 4.5 ms on
  both, p99 20.5 → 21.5 ms (p=0.83), and max 547 → 263 ms. The single-run gap
  was noise.

### develop → #655

1. **`dep-granularity-check` add: +12-23%.** One `wr add` of a member into a
   3000-member dep group with 30000 waiters takes about 60-90 ms longer.
   - 6 rounds: 452 → 556 ms (+22.9%, p=0.009).
   - 12 rounds: 479 → 537 ms (+12.1%, p<0.001).
   - Cause: `f4eb134a` ("Keep a dependent's re-run when the manager crashes
     mid-add"). Against its parent `4b743a37`, 12 rounds showed 470 →
     558 ms (+18.7%, p=0.002).
   - That commit moves new-job stores into `storeNewJobsGuarded`, which
     looks up and puts back archived dependents inside the add transaction
     (`putBackArchivedDependentsWith`, `putBackArchivedDependentsTx`).
   - Peak RSS +5.5% (p=0.032) is under the 10% line.
   - **Fixed in #655 before it merged** (item 12 of
     `.docs/bugfixes/260930-dep-group-rerun-gaps.md`): the add now puts back
     only the dependents an archive noted on its guard since the add read
     them. `BenchmarkAddDepGroupMember`, added for this and now part of
     `make speed`, was 334 ms on `f4eb134a`'s parent `4b743a37`, 410 ms on
     `31d5ad10` (+22.8%, p<0.001) and 340 ms with the fix (p=0.38, not
     significant), 8 interleaved rounds each at `-benchtime 10x`.

## Improvements

- `ArchiveJobs` -61% in v0.38.0 (#555).
- `JobKey` -99.7% in v0.38.0 (#641 memoises it).
- `UpdateJobState` -13% in v0.38.0.
- `RepGroupStatusDetails` -80% time and -99% bytes in develop (#650).
- Every big-DB scenario got 10x to 20x better from v0.37.2 to v0.38.0.

## Evidence

Everything is under `/nfs/hgi/wr/sb10-bigdb/speed/`:

- Benchmark and quick-scenario sessions, each holding `benchstat.txt`,
  `benchstat.csv`, raw `base.txt`/`head.txt` and the scenario logs:
  - `run-1790895596` (v0.37.2 → v0.38.0)
  - `run-1790896643` (v0.38.0 → develop)
  - `run-1790897969` (develop → #655)
- Confirmation runs:
  - `run-1790899216` (dep-granularity develop → #655, 12 rounds)
  - `run-1790900138` (dep-granularity v0.38.0 → develop, 12 rounds)
  - `run-1790900774` (`f4eb134a^` → `f4eb134a`, 12 rounds)
  - `run-1790907646` (archive-rate v0.38.0 → develop, 4 rounds)
- v0.37.2 dep-granularity on develop's fixture: `run-1790902480`.
- Big-DB single runs: `run-1790903186` to `run-1790907171`, one per scenario
  and tree.
- The worktrees with their copied test files: `trees/`.
- The per-commit timeline for v0.37.2..v0.38.0: `timeline.txt`.
