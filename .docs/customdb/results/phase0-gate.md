# Phase 0 gate (item 0g.1)

Gate: no behaviour change (A1 test 3: the existing suite passes with no
test removed or skipped) and `make speed` within 10% of `d1a4b6d7`.

Run on 2026-10-29 on farm22-wrstat01 (8 cores), HEAD `98f03181` on
`customdb-f397b547` against base `d1a4b6d7`. Both parts pass.

Raw outputs are in [phase0-gate/](phase0-gate/).

## No behaviour change

### Method

`make test` ran in detached scratch worktrees of `d1a4b6d7` and `98f03181`
with every `OS_*` variable unset and an empty `HOME`. In both worktrees
`internal/testsuite/runner.go` was patched to copy each lane's
`go test -v` log out before the suite deletes its temp dir
([runner-keep-logs.patch](phase0-gate/runner-keep-logs.patch)). The patch
changes nothing else, and both sides got the same patch.

A script read the logs and listed:

- every top-level `--- PASS` and `--- SKIP` test
  ([base-top.txt](phase0-gate/base-top.txt),
  [head-top.txt](phase0-gate/head-top.txt));
- every GoConvey leaf path, as `TestName :: scope > scope > leaf`, from
  the suite's `GOCONVEY_REPORTER=json` blocks.

The leaf script, run as `python3 -I extract.py <logdir> <outprefix>`:

```python
import sys, os, re, json, glob, collections
logdir, out = sys.argv[1], sys.argv[2]
top = collections.Counter(); sub = collections.Counter(); leaves = set(); paths = set()
O, C = ">->->OPEN-JSON->->->", "<-<-<-CLOSE-JSON<-<-<"
for f in sorted(glob.glob(os.path.join(logdir, "*.log"))):
    txt = open(f, errors="replace").read()
    for m in re.finditer(r'^(\s*)--- (PASS|SKIP|FAIL): (\S+)', txt, re.M):
        (sub if m.group(1) else top)[(m.group(2), m.group(3))] += 1
    cur = None
    events = [(m.start(), 'run', m.group(1)) for m in re.finditer(r'^=== (?:RUN|CONT)\s+(\S+)', txt, re.M)]
    events += [(m.start(), 'blk', m.group(1)) for m in re.finditer(re.escape(O) + r'(.*?)' + re.escape(C), txt, re.S)]
    for _, kind, v in sorted(events):
        if kind == 'run': cur = v.split('/')[0]; continue
        body = v.strip().rstrip(',')
        if not body: continue
        stack = []
        for s in json.loads('[' + body + ']'):
            stack = stack[:s['Depth'] - 1] + [s['Title']]
            paths.add((cur, tuple(stack)))
for (t, p) in paths:
    if not any(q[0] == t and len(q[1]) > len(p) and q[1][:len(p)] == p for q in paths):
        leaves.add(t + " :: " + " > ".join(p))
for ext, data in ((".top", top), (".sub", sub)):
    with open(out + ext, "w") as w:
        for (st, n), c in sorted(data.items()): w.write(f"{st} {n} {c}\n")
with open(out + ".leaves", "w") as w:
    for l in sorted(leaves): w.write(l.replace("\n", " ") + "\n")
```

Sort the `.leaves` files with `LC_ALL=C` before `comm`. A static
`grep '^func (Test|Fuzz|Benchmark|Example)'` over every `*_test.go` covered
the build-tagged tests that `make test` does not run.

### Counts

| | base `d1a4b6d7` | HEAD `98f03181` |
| --- | --- | --- |
| `make test` summary | 1008 passed, 23 skipped, 34 packages, 5m36s | 1047 passed, 27 skipped, 35 packages, 5m43s |
| `jobqueue` | 649 passed, 10 skipped | 686 passed, 14 skipped |
| top-level `--- PASS` / `--- SKIP` | 998 / 8 | 1037 / 12 |
| GoConvey leaf paths | 3289 | 3398 |
| declared test, fuzz, benchmark and example functions | 1088 | 1146 |

The full summaries are in
[base-make-test.txt](phase0-gate/base-make-test.txt) and
[head-make-test.txt](phase0-gate/head-make-test.txt). Every other package's
line is unchanged. HEAD adds `jobqueue/wal` (2 passed).

### Skips

HEAD skips everything the base skips, with the same reasons, plus 4 new
opt-in tests that did not exist at the base:

| Skip at HEAD | Test | Why |
| --- | --- | --- |
| child of TestStoreCrash | `TestStoreCrashChild` | the A4 crash test's re-executed child; runs only under `TestStoreCrash`, which passes |
| WR_STORE_REPLAY is not set | `TestStoreReplay` | A5 replay of a named recording, for the Phase 1 and 2 gates |
| WR_STORE_RECORD_REGEN is not set | `TestStoreRecordRegen` | regenerates the committed recording fixture |
| WR_ADDBENCH_DIR is not set | `TestAddLatency1000` | the add-latency harness; `TestAddLatency1000FailsOverItsLimit` runs it as a child and passes |

No test that ran at the base is skipped at HEAD. The one indented
`--- FAIL: TestAddLatency1000` line at HEAD is that child's expected
failure, which `TestAddLatency1000FailsOverItsLimit` asserts.

### Removed or renamed tests

8 top-level tests at the base no longer exist by name. Each was moved
onto `store` (A2) and renamed, and every leaf survives:

| Base test | HEAD test |
| --- | --- |
| `TestDBCheckIfComplete` | `TestStoreCheckIfComplete` |
| `TestDBHighPeakMemoryRecommendation` | `TestStoreHighPeakMemoryRecommendation` |
| `TestDBLoadDropsImpossibleCleanups` | `TestStoreRecoveryDropsImpossibleCleanups` |
| `TestDepGranularityCorruptDBStillRestores` | `TestStoreCorruptRestoredFromBackup` |
| `TestDepGranularityDBLockWaitsForRelease` | `TestStoreOpenLocked` (a second open waits for the lock) |
| `TestDepGranularityDBLockedLeavesBackupAlone` | `TestStoreOpenLocked` (fails with ErrDBLocked, touches no file, holder still works) |
| `TestDepGranularityDBLockedLeavesFileAlone` | `TestStoreOpenLocked` (same leaf, and the no-backup leaf) |
| `TestDepGranularityUnlockedDBOpensImmediately` | `TestStoreOpenLocked` (an open of a closed store takes less than the open timeout) |

### Moved or split leaves

42 leaf paths at the base are not at HEAD under the same test and
wording ([leaves-base-only.txt](phase0-gate/leaves-base-only.txt)).
9 belong to the 8 renamed tests above. The other 33 moved to another
test, split in two, or had "db"/"database" reworded to "bolt store":

| Base leaves | HEAD |
| --- | --- |
| `TestAlreadyCompleteDuplicateAttribution`: the 2 index-entry leaves | `TestAlreadyCompleteDuplicateIndexAttribution`, same wording |
| `TestDBContainerImageUser` | `TestStoreContainerImageUser` |
| `TestDBDepGroups`: resurrecting a dependent | `TestStoreResurrectedDependentSkipsCompleteJobs` |
| `TestDBEndTimeIndexDurability`: index entry survives a reopen | `TestStoreCompleteJobsRecentDurability`: an archived job is still recent after a close and reopen |
| `TestDBRetrieveCompleteJobsRecent` (5) | `TestStoreRetrieveCompleteJobsRecent` (the same 5, plus one) |
| `TestMovedOnRunnerAfterRestart`: the 3 "second only reserved" leaves | `TestMovedOnRunnerAfterRestartSecondReserved`, same wording |
| `TestReaddQueuedKeepsRecord`: a ready one, without writing to the database | `TestReaddQueuedKeepsRecord` "a ready one" plus `TestReaddQueuedWritesNothing` |
| `TestReleaseDurabilityResend`: the 2 held-off-disk leaves | `TestReleaseDurabilityResendWaitsForWrite`, same wording |
| `TestReliable4RecoveryDependencyPass`: the cancel and the per-job-equivalence leaves | `TestStoreRecoveryDependencyPass`, same wording |
| `TestReliable4RecoveryDependencyState`: restores every job's state "for far fewer transactions" | split: `TestReliable4RecoveryDependencyState` (state) and `TestReliable4RecoveryDependencyTxCost` (fewer read transactions than jobs) |
| `TestReliable4RecoveryDependencyState`: dependencies release the children | same test, "bolt store" wording |
| `TestReqStatCache`: load during an uncommitted new stat key | `TestReqStatCacheUncommittedStat` |
| `TestReserveAndStartWriteRunState`: duplicate start of a job recovered without a pid | `TestReservationAndStartRecoveredAfterCrash`, same wording |
| `TestReserveAndStartWriteRunState`: killed after the reservation, "recovers its run state, holding the job in Run" | split: same test "recovers its run-state record", and `TestReservationAndStartRecoveredAfterCrash` "holds the job in Run for the runner" |
| `TestStoreNewJobsKeepsHandedOutRecord` (8) | same test, "bolt store" wording |
| `TestWarningStormIsAggregated`: the 2 leaves | the reservation leaf stays; the slow-request leaf is `TestSlowRequestWarningIsAggregated` |

HEAD adds 151 leaf paths
([leaves-head-only.txt](phase0-gate/leaves-head-only.txt)): the targets
above plus the new Phase 0 tests (store interface, inspection, stats,
crash, record and replay, `wal` scan).

Build-tagged tests (`reliability_repro` and similar) are not run by
`make test` at either commit. The only declared functions missing at HEAD
are the 8 renamed tests above.

## Speed

`SPEED_DIR=<scratchpad> SPEED_BASE=d1a4b6d7 make speed` from the main
clone at `98f03181`. The host was otherwise quiet (load about 2-3 on 8
cores).

### Run 1: full `make speed`, verdict PASS

Benchmarks, 6 interleaved rounds, medians (full table in
[speed-run1-benchstat.txt](phase0-gate/speed-run1-benchstat.txt)):

| Benchmark | base sec/op | HEAD sec/op | delta |
| --- | --- | --- | --- |
| JobCleanup | 342.5µ | 341.2µ | ~ (p=0.94) |
| JobCleanupDepth1 | 3.346m | 3.396m | ~ (p=0.39) |
| JobCleanupDepth8 | 3.652m | 3.673m | ~ (p=0.70) |
| AddJobs | 129.2m | 129.8m | ~ (p=0.59) |
| UpdateJobState | 55.55m | 55.75m | ~ (p=0.94) |
| ArchiveJobs | 261.3m | 259.4m | ~ (p=0.82) |
| ArchiveSpacedArrivals | 2.470 | 2.469 | ~ (p=0.82) |
| ModifyLiveJobsReverseLookup | 11.60m | 11.57m | ~ (p=1.00) |
| AddDepGroupMember | 370.4m | 373.0m | ~ (p=0.49) |
| JobKey | 28.96n | 29.01n | ~ (p=0.70) |
| BuildSchedulerGroupsBacklog/priorities=1 | 105.9m | 105.1m | ~ (p=0.94) |
| BuildSchedulerGroupsBacklog/priorities=4 | 169.0m | 168.2m | ~ (p=1.00) |
| RACCycleCounted/leavers=0 | 93.93m | 94.65m | ~ (p=0.94) |
| RACCycleCounted/leavers=100 | 101.2m | 103.0m | ~ (p=0.82) |
| ReadyBacklogSnapshot | 1.559m | 1.536m | ~ (p=0.49) |
| RepGroupStatusDetails/1000 | 3.206m | 3.289m | ~ (p=0.39) |
| RepGroupStatusDetails/10000 | 42.70m | 41.33m | ~ (p=0.31) |
| LimiterIncDecUnlimited | 2.021µ | 1.988µ | ~ (p=0.94) |
| LimiterIncDec | 2.150µ | 2.137µ | ~ (p=0.49) |
| LimiterCapacity | 1.878µ | 1.887µ | ~ (p=0.59) |
| QueueLifecycle/groups=1 | 2.727m | 2.734m | ~ (p=0.94) |
| QueueLifecycle/groups=100 | 1.921m | 1.935m | ~ (p=0.94) |
| QueueLifecycle/groups=1,callback=running | 2.851m | 2.789m | ~ (p=0.49) |
| QueueLifecycle/groups=100,callback=running | 2.047m | 2.014m | ~ (p=0.59) |
| jobqueue geomean | 14.90m | 14.91m | +0.09% |

B/op and allocs/op are unchanged. The only significant differences are
allocs/op of +1.21% (RepGroupStatusDetails/1000, p=0.048), +0.74%
(ModifyLiveJobsReverseLookup), +0.01% (AddDepGroupMember) and -0.19%
(ArchiveSpacedArrivals). The bolt pages, writes and transactions per job
are within 0.5% and not significant.

Scenarios, one round each:

| Scenario figure | base | HEAD | delta |
| --- | --- | --- | --- |
| report-storm jobs/s | 3200.0 | 3096.0 | -3.3% |
| report-storm max-archive-ms | 92 | 113 | +22.8% |
| report-storm wall-sec | 6.25 | 6.46 | +3.4% |
| dep-granularity peak-rss-MB | 311 | 318 | +2.3% |
| dep-granularity recovery-sec | 0.200 | 0.171 | -14.5% |
| dep-granularity add-sec | 0.492 | 0.511 | +3.9% |

One round cannot show significance, and report-storm's max-archive-ms is
the single worst archive of a run, so it was rerun.

### Run 2: scenarios only, 6 rounds each, verdict PASS

`SPEED_COUNT=0 SPEED_SCENARIO_COUNT=6`
([speed-run2-benchstat.txt](phase0-gate/speed-run2-benchstat.txt)):

| Scenario figure | base median | HEAD median | delta |
| --- | --- | --- | --- |
| report-storm jobs/s | 3138 ± 3% | 3089 ± 2% | -1.6%, ~ (p=0.26) |
| report-storm max-archive-ms | 94.5 ± 34% | 107.0 ± 57% | +13.2%, ~ (p=0.31) |
| report-storm wall-sec | 6.375 ± 3% | 6.475 ± 3% | +1.6%, ~ (p=0.26) |
| dep-granularity peak-rss | 297.5Mi ± 3% | 305.2Mi ± 5% | +2.6%, ~ (p=0.25) |
| dep-granularity recovery-sec | 0.1955 ± 25% | 0.1950 ± 13% | -0.3%, ~ (p=0.85) |
| dep-granularity add-sec | 0.510 ± 9% | 0.522 ± 8% | +2.4%, ~ (p=0.86) |

The max-archive-ms samples overlap fully (base 73 to 127, HEAD 83 to
168), so it was rerun with more rounds.

### Run 3: report-storm only, 12 rounds, verdict PASS

`SPEED_COUNT=0 SPEED_SCENARIOS=report-storm SPEED_SCENARIO_COUNT=12`
([speed-run3-benchstat.txt](phase0-gate/speed-run3-benchstat.txt)):

| Scenario figure | base median | HEAD median | delta |
| --- | --- | --- | --- |
| report-storm jobs/s | 3077 ± 2% | 3145 ± 2% | +2.2% (better, p=0.031) |
| report-storm max-archive-ms | 107.5 ± 53% | 108.5 ± 47% | +0.9%, ~ (p=0.66) |
| report-storm wall-sec | 6.500 ± 2% | 6.360 ± 2% | -2.2% (better, p=0.031) |

The run-1 and run-2 max-archive-ms gaps were noise: base and HEAD both
range from about 72ms to 176ms. Every measure is within 10%.
