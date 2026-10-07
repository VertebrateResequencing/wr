# Notes for the PR body

- Concurrent adds (item 2.3): If two adds of the same new job race and the
  second add's fresh copy is written after a reservation's run state is
  queued but before it is drained, the run state lands over that fresh copy,
  so until the job's next full write its stored non-run fields are the second
  add's; this window is no wider than today's stale full change, and a test
  (TestAddKeepsHandedOutRunState) pins the outcome.
- Compact on a 0-byte file (item 1.2): `wr manager compact` now fails on an
  empty file (the read-only version check cannot open it), where before it
  would have initialised and compacted it.
- Speed (item 2.2): every full live write now also deletes a run-state key;
  covered by phase 6's make speed / speed-full comparison.
- Recovery cleanup (item 3.2): a failed delete of stale/orphaned run-state
  records at startup is logged as a warning ("recovering: failed to drop
  stale job run-state records") and recovery continues; the spec did not
  cover this case. Leftover records are harmless (every reader re-checks the
  CRC) and the next recovery retries.
- runStateRecovery carries `undecodable` and `dropErr` beyond the spec's
  struct, so decodePriorJobs can log them.

## A2 audit

A reservation and a start now write only `jobRunState`, so every persisted
`Job` field changed in memory since the job's last full write must be in
`jobRunState`, have its own durable write, or be re-derived at recovery.

Changed in memory between the last full write and a reservation or start:

| Field | Where | How it is persisted |
| --- | --- | --- |
| `Requirements`, `RequirementsOrig` | `prepareReadyJob` -> `updateJobRequirementsForRetry` (`applyRecommendedJob*`, `increaseJobRAMAfterHighPeak`, `increaseJobDiskAfterFailure`, `increaseJobTimeAfterFailure`) | In `jobRunState` (A2 test 3) |
| `DelayTime` | `respondWithReservedJob` | In `jobRunState` |
| `State`, `Lost`, `EndTime`, `PeakRAM`, `CPUtime` and the other run fields | queue callbacks, `markJobLost`, touches (`recoverLostTouchedJob`, live status), reservation and start | In `jobRunState` |
| `WaitingForDepGroups` | `setWaitingForDepGroups` (add, `reflectModifiedJobInQueue`, `updateModifiedQueueJobs`, `updateLiveDependents`, `updateDependentUnlessRunning`, `rerunDependencies`) | Not stored by the reservation; recovery re-derives it for every job in `resolveDependencyChunk` (A2 test 7) |
| `UntilBuried` | `applyRelease`; `kickJobs` | The release's exit-op write; the kick's own full change, which A3 test 4 shows survives a reservation queued after it. `kickJobs` does not yet guarantee that order: `s.q.Kick` makes the item reservable before the kick sets `UntilBuried` under `job.Lock` and before it queues its full change. A reservation that locks the job between those steps queues its run state, which can commit before the kick's full change is queued; a crash then recovers the pre-kick `UntilBuried`, 0 for a buried job, so its next failure buries it again instead of retrying. Before this change the reservation's full write stored the kicked value. The same window already causes a double run on develop: a job reserved before the kick takes `job.Lock` has its State set back to ready, the kick's full change supersedes the reservation, and a crash before Started recovers it ready while its runner may run it. A separate kick-ordering fix, merged before this PR and required by it, closes both by setting the fields and queueing the kick's write before the job becomes reservable |
| `RerunAfterRun` | `markRerunAfterRun` (add), `takeRerunAfterRun` (release), `endArchive` (archive) | `storeRunningRerunMarks`, the release's write and the archive's writes respectively |
| `Behaviours` | `dropImpossibleCleanups` | At add before the add's encode, and on every `decodeJob`, so recovery re-applies it |

Set at add before the add's record is encoded, so in it: `EnvKey`,
`RerunAfterRun` and `RunnerReservation` (cleared), `ReadyTime` (cleared),
`UntilBuried`, `BsubID`, `LimitGroups` and `LimitGroupsForDisplay`
(`prepareInputJobs`, `handleUserSpecifiedJobLimitGroups`). Every
`JobModifier` setter (`Cmd`, `Cwd`, `ReqGroup`, `Group`, `Requirements`,
`Override`, `Priority`, `Retries`, `NoRetriesOverWalltime`, `LimitGroups`,
`DepGroups`, `Dependencies`, `Behaviours`, `MountConfigs`, the container
fields, `BsubID`) is written by the modify's full write
(`modifyLiveJobsTx`). `RunnerReservation` is cleared by
`forgetCompletedRunnerHold` only for the complete record.

Set only on copies sent to clients, never stored: `ReadyTime`
(`itemToJobIfAdmitted`), `EnvC` and `EnvCRetrieved` (`jobPopulateStdEnv`),
`Similar` (`addJobToGroup`, `countUndecodedJobs`), `State` and `UntilBuried`
in `refreshJobFromLiveItem`, `RepGroup` (`stampRepGroup`, the web status
handlers) and `resetJobStatusFields`. Nothing in the manager sets `Queue`.
Unexported fields (`schedulerGroup`, `runID`, `incrementedLimitGroups`, ...)
were never persisted.

Changed in memory by recovery:

| Field | Where | How it is persisted |
| --- | --- | --- |
| every `jobRunState` field | `recoverIncompleteJobs` overlay | Read from disk |
| `Behaviours` | `decodeJob` -> `dropImpossibleCleanups` | Re-applied on every decode |
| `WaitingForDepGroups` | `resolveDependencyChunk` | Re-derived at every recovery |
| `RerunAfterRun` cleared | `recoverRerunMark`, for a job recovered outside the run sub-queue | In `jobRunState`, so the next reservation stores it (A2 test 8) |
| `RerunAfterRun` set | `recoverRunningDependent` -> `markRecoveredRerun` | `storeRunningRerunMarks`, unless already stored |
| `schedulerGroup`, `incrementedLimitGroups` | `recoveredItemDef`, `recoverRunningJob` | Unexported, never persisted |

`recoverRunnerHold` reads the job and changes no field of it.

A2 test 1 checks this audit end to end for a reserved and started job: after
reserve and start its live record is byte-equal to the add-time record, and
recovery reads a job whose encoding equals the in-memory job's.
- Kick ordering dependency: done after the rebase onto 72384e4c.
  `TestKickQueuedBeforeReservable` reserves the kicked job from
  `kickQueuedHook`, crash-images, and requires recovery to give the job
  reserved with the kicked `UntilBuried`. It fails (`UntilBuried` 0, want 4)
  with the write queued after that hook. With the write queued after
  `KickWith` returns but before the hook, every test passes: no seam exists
  between `KickWith` returning and that write.
- Follow-up from item 4.1's review (non-blocking): reserveNotDurableLines in
  reserve_durability_test.go could also require `lvl=info`, so a line logged
  at warn level would fail the test. Done in fa1deb3b.
- Follow-ups from item 5.1's review (non-blocking): dbstart drops a job's
  line if a CRC-matching run-state body fails to decode (jobqueue falls back
  to the live record; dbstart could too); `dbstart -schema` with no path
  prints an open error (exit 1) rather than usage (exit 2). Done in 17cc1d66.
- Follow-up from item 5.2's review (non-blocking): statinspect prints a
  doubled prefix for a missing jobslive bucket ("clearlive err: clearlive: no
  jobslive bucket"); drop the inner wrap. Done in 5fb8060d.
- Follow-up from item 5.3's review (non-blocking): CHANGELOG "naming both
  versions" and the compact help's "newer version of wr" mean schema
  versions; consider "naming both schema versions". Done in 7c7c3518.
- Follow-ups from item 5.4's review (non-blocking): add a d1 case with only
  warning lines (expect inside=0 outside=0) to pin "no fallback to
  warnings"; consider printing a failing outsidePct when runs=0. Done in
  288fa2a8 (`outsidePct=nan` when runs=0; case 22's expected output changed).

## Fixtures (item 5.5)

`G=/nfs/hgi/wr/sb10-bigdb/runstate-gate`. Every `wrdev.sh` run below had all
`OS_*` unset, `GOCACHE=/tmp/claude-11346/gocache-runstate-gate`, `nice -n 19`,
ports checked free with `ss -ltn`, and `wrdev.sh build` first. Fixture builds
used `WRDEV_ROOT=$G/fixbuild/root DEV_PORT=51970 DEV_WEB=51971 PROD_PORT=51972
PROD_WEB=51973`; E4 tests 1 and 2 used `WRDEV_ROOT=$G/e4test/root` with ports
51974 to 51977. `dbstart` is `go build ./developers/soak/dbstart` at `$G/dbstart`.

Fixtures, built 2026-10-06 from this tree (`generator 2a0e4f9e` plus the
uncommitted phase 5 changes):

| Fixture | Command | Source size | Size | `dbstart -schema` |
| --- | --- | --- | --- | --- |
| `$G/fixtures/pristine6` | `compact-fixture /nfs/hgi/wr/sb10-bigdb/pristine6 $G/fixtures/pristine6` (99s) | 7,392,931,840 | 3,046,572,032 | `schemaVersion=2` |
| `$G/fixtures/pristine10` | `compact-fixture /nfs/hgi/wr/sb10-bigdb/pristine10 $G/fixtures/pristine10` (135s) | 10,729,893,888 | 4,337,278,976 | `schemaVersion=2` |
| `$G/fixtures/prod.db` | `compact-fixture /nfs/hgi/wr/sb10-bigdb/prod.db $G/fixtures/prod.db` (123s; "removed output stored by older wr versions from 75811 completed jobs") | 7,925,501,952 | 5,111,070,720 | `schemaVersion=2` |
| `$G/fixtures/fix120k.db` | `WRDEV_PRISTINE_DB=$G/fixtures/pristine6 WRDEV_ASL_FIXTURE=$G/fixtures/fix120k.db wrdev.sh add-storm-fixture 120000 5 200` | (base pristine6 above) | 3,352,190,976 | `schemaVersion=2` |

The originals' size and mtime were the same before and after. The
`fix120k.db.aslmanifest`: `aslfixture 2`, `size 3352190976`,
`mtime 1791324308`, `incomplete 120000`, `prefix echo aslfix`,
`blockgroup aslblock`, `repgroup rgaslfix`, `kidrepgroup rgaslfixkid`,
`jobcwd $G/fixtures/fix120k.db.jobcwd`, `base $G/fixtures/pristine6`,
`jobs 120000`, `selfaddpct 5`, `depgroups 200`. Its generator printed
`command audit: 120000 incomplete commands read, 0 of them NOT a form this
generator writes`. Compaction drops each original's freelist, so figures
measured on these copies are not like for like with battery10's.

E4 acceptance tests:

1. `cp jobqueue/testdata/dbcompat/db.golden $G/e4test/src.db`, then
   `wrdev.sh compact-fixture $G/e4test/src.db $G/e4test/dst.db` exited 0;
   `src.db`'s SHA-256 stayed
   `cff1df83216f0ec63fdc080fec698d96bae19e1da7ea2e6a296a7783efd550b9`
   and `dbstart -schema dst.db` printed `schemaVersion=2` (`src.db`:
   `schemaVersion=0`). With `dst.db` copied to
   `$G/e4test/root/.wr-prod_production/db`, `wrdev.sh prod-start local`
   exited 0, `wr status --deployment production -i
   reliable2-dbcompat-complete -o counts` printed `complete: 2`, and
   `wrdev.sh prod-stop` printed `killed our manager pid 1883124`.
2. With `dst.db` present: `wrdev: REFUSING: $G/e4test/dst.db already
   exists`, exit 1. With `src.db.aslmanifest` present: `wrdev: REFUSING:
   $G/e4test/src.db.aslmanifest exists, ...`, exit 1. A before/after listing
   (names, sizes, mtimes, SHA-256) of `$G/e4test` was identical for both. Also
   checked: same file (`dst.db` vs `./dst.db`) refused, exit 1; with the dev
   manager up, `REFUSING: the dev manager (pid 1884740) is up`, exit 1; a
   non-bolt source failed compaction (`invalid database`), exit 1, and its
   `dst` was removed.
3. `WRDEV_PRISTINE_DB=$G/fixtures/fix120k.db wrdev.sh add-storm-lsf` (fixbuild
   root, defaults), run to completion, printed `fixture manifest OK: 120000
   incomplete jobs, safe prefix 'echo aslfix', blocked by aslblock,` and
   exited 0 with `## VERDICT: ... presentAfterRestart=4476/4476
   missingAcked=0 ... overSlow=0 ...` and `PASS: all 4476 acknowledged adds`;
   cleanup reported `wrpiso51972h838732172_* jobs left in LSF: 0`. F2.2 runs
   it again for the gate verdict.
- Follow-ups from item 5.5's review (non-blocking): compact-fixture writes
  its private config before the `wr conf` refusals; doesn't assert
  ManagerHost is localhost; has no trap for SIGINT/timeout cleanup of dst;
  the aslmanifest refusal doesn't follow symlinks; freelist-bound repro texts
  could point at TestReliable4InflateDB. Done in 0b35a4a8.

## Local gates (item 6.1)

Run on cadeae73 (rebased on develop 72384e4c), all `OS_*` unset,
`GOCACHE=/tmp/claude-11346/gocache-runstate` (speed runs:
`/tmp/claude-11346/gocache-runstate-gate`), `GOFLAGS=-p=2`, `nice -n 19`.

1. `make lint`: `0 issues.` PASS. `make test`: 959 passed, 22 skipped, 34
   packages, PASSED. `CGO_ENABLED=1 make race`: 959 passed, 21 skipped, 34
   packages, PASSED. `go vet -tags netgo,reliability_repro ./jobqueue/`: exit
   0, PASS. `go test -tags netgo,reliability_repro --count 1 -run
   TestReliable4InflateDBOpens ./jobqueue/`: 14 assertions, `ok`, PASS.
2. `SPEED_BASE=72384e4c SPEED_DIR=$G/speed-quick DEV_PORT=51990 DEV_WEB=51991
   PROD_PORT=51992 PROD_WEB=51993 make speed` (benchstat copied into
   `$G/speed-quick/tools/` first; ports checked free with `ss -ltn`): `PASS:
   no benchmark or scenario worsened by more than 10% at p<0.05, and every
   scenario met its thresholds` (`$G/speed-quick/run-1791326361`). No time
   or bolt-I/O metric moved significantly except
   `BuildSchedulerGroupsBacklog/priorities=1` sec/op +3.98% (p=0.041), which
   this branch does not touch. Small significant allocation rises from the
   run-state write: `UpdateJobState` B/op +4.09% (p=0.004) and allocs/op
   +2.96% (p=0.002); allocs/op `AddJobs` +2.21%, `ArchiveSpacedArrivals`
   +1.85%, `ModifyLiveJobsReverseLookup` +2.28% (all p=0.002); B/op
   `ModifyLiveJobsReverseLookup` +0.53% (p=0.009). bolt_pages/job
   and bolt_writes/job: no significant change (`UpdateJobState` 0.8149 to
   0.8199, p=0.310). Scenarios (one round each, so threshold-gated only):
   report-storm 743.8 to 746.8 jobs/s; dep-granularity peak RSS 314 to
   300 MiB (raw `peakRssMb`; benchstat's table rescales this unit, showing
   299.5Mi to 286.1Mi).
3. A2 test 5, `make bench BENCH='UpdateJob(RunState|Full)10KB'`:
   `BenchmarkUpdateJobRunState10KB` 0.3030 bolt_pages/job,
   `BenchmarkUpdateJobFull10KB` 4.629 bolt_pages/job (ratio 0.065, bar is
   at most 0.5). PASS.
4. `SPEED_BASE=72384e4c SPEED_DIR=$G/speed-full
   SPEED_BIG_DB=$G/fixtures/pristine6 WR_AS_DB=$G/fixtures/prod.db
   WR_ARCHRATE_DB=$G/fixtures/pristine10 WR_AC_DB=$G/fixtures/pristine6
   DEV_PORT=51994 DEV_WEB=51995 PROD_PORT=51996 PROD_WEB=51997 make
   speed-full` (benchstat already in `$G/speed-full/tools/`; ports checked
   free with `ss -ltn`; head reported `-dirty` only for this uncommitted
   pr-notes.md): `PASS: no benchmark or scenario worsened by more than 10% at
   p<0.05, and every scenario met its thresholds`
   (`$G/speed-full/run-1791333741`). An earlier interrupted run,
   `$G/speed-full/run-1791327200`, is abandoned and left in place; no process
   referenced it. Significant moves: sec/op `ArchiveSpacedArrivals` +0.29%
   (p=0.026); `UpdateJobState` B/op +4.44% and allocs/op +2.97% (both
   p=0.002), the run-state write as in the quick run;
   `ModifyLiveJobsReverseLookup` B/op +0.67% and allocs/op +2.53% (both
   p=0.002). No bolt-I/O metric moved significantly. Scenarios (one round
   each, so threshold-gated only), base to head: report-storm 747.9 to 923.4
   jobs/s; dep-granularity peak RSS 300 to 309 MiB; add-storm 220.40 to
   242.87 adds/s, p99 6585 to 3141 ms; archive-rate 166.6 to 167.5
   archives/s; archive-ceiling 384.63 to 382.99 archives/s
   (throughput-factor 44.18 to 43.91).
5. `.docs/reliable2/harness/statinspect`: `GOPROXY=off GOFLAGS=-mod=mod go mod
   tidy` exit 0, `go.mod` and `go.sum` unchanged (md5 identical);
   `go test -count=1 ./...`: `ok statinspect`. PASS.
6. `developers/soak/testdata/soakgate/run.sh`: `PASS: 28 soakgate cases`,
   exit 0.

## wrdev modes and sweep (item 6.2)

Run on f9380b6c (rebased on develop 72384e4c), all `OS_*` unset,
`GOCACHE=/tmp/claude-11346/gocache-runstate-gate`, `nice -n 19`, after
`wrdev.sh build` into the root below. F2 modes ran in sequence with
`WRDEV_ROOT=$G/f2/root DEV_PORT=51980 DEV_WEB=51981 PROD_PORT=51982
PROD_WEB=51983` (ports checked free with `ss -ltn`).

1. `wrdev.sh crash-recovery`: exit 0, `PASS: re-sent archive accepted
   (complete=1), command ran exactly once`. PASS. (Its `job running;
   marker=...` line also logs a harmless shell error, `cr_count: No such file
   or directory`, when the job has not yet written its marker file; the
   line then reports `marker=0`.)
2. `WRDEV_PRISTINE_DB=$G/fixtures/fix120k.db wrdev.sh add-storm-lsf`: exit
   0, PASS. Manifest `incomplete 120000`; `recovered-job audit:
   120000/120000 incomplete commands read, all of a form the generator
   writes`; `## VERDICT: adders=24 adds=5034 acked=4694 ... missingAcked=0
   neverAddedFound=0 unackedInDb=0 ... killProven=yes ... outage=22s ...
   p50=202ms p99=1283ms ... fixIncomplete=120000 ... fixBuried=0`; `PASS: all
   4694 acknowledged adds (2406 of them before the kill, 8 in flight ...`.
3. `wrdev.sh dep-granularity-check`: exit 0, `PASS: peakRssMb=315, recovery
   in 197ms, 'wr manager status' answered 'starting' inside the window, and
   one more member added in 525ms`. PASS.
4. F2.4, each exit 0, PASS:
   - `WR_ARCHRATE_DB=$G/fixtures/pristine10 wrdev.sh archive-rate`: `PASS: a
     sustained 660-archiver rate on a 4GB-class DB stays at 160ms mean /
     1212ms p99 with the queue never deeper than 285` (30023 archives, 0
     errors, overFloor=0).
   - `WR_AC_DB=$G/fixtures/pristine6 WRDEV_AC_WORK=$G/f2/root wrdev.sh
     archive-ceiling`: `## VERDICT: low=20@8.72/s high=1143@382.97/s
     throughputFactor=43.94x (min 10x) ... overClientFloor=0
     archiveErrors=0 goExit=0`; `PASS: 1143 archivers reached 382.97/s
     against 20 archivers' 8.72/s`.
   - `WR_AS_DB=$G/fixtures/prod.db WRDEV_AS_WORK=$G/f2/root wrdev.sh
     add-storm`: `## VERDICT: low=20@10.18/s high=700@247.65/s
     throughputFactor=24.34x (min 10x) ... highP99=3256ms ... overSlow=0
     overClientFloor=0 clientTimedOut=0 txnsPerAdd=0.16 ... addErrors=0
     goExit=0`; `PASS: 700 clients reached 247.65/s against 20 clients'
     10.18/s`.
   - `WR_WSFREEZE_DB=$G/fixtures/pristine10 wrdev.sh writestorm-freeze`:
     `WSFREEZE: N=100000 archivers=8 ... peakGoroutines(added)=1
     archives=105 maxArchiveLat=2.057s overTTR(1m0s)=0`, `--- PASS`, `ok`.
   - `WRDEV_PRISTINE_DB=$G/fixtures/pristine10 wrdev.sh backup-stall-check`:
     `## VERDICT: maxDelayed=0 badjobDelta=0 maxStatusRPC=260ms`, `NO STALL:
     jobs drained cleanly despite backups (the fix works)`.

Sweep: `SWEEP_DB_DIR=$G/fixtures FIX120K=$G/fixtures/fix120k.db
SWEEP_PORTS="51984 51985 51986 51987" developers/soak/sweep.sh $G/sweep`
(ports checked free with `ss -ltn`), exit 0, no `SWEEP ERROR` lines, no
`SKIPPED-NOFIXTURE` or `SKIPPED-DISK` rows, and no `FAIL`, `--- FAIL` or
`panic:` line in any mode log. All 49 modes rc=0 (mode, rc, seconds from
`$G/sweep/results.tsv`):

```text
build	rc=0	secs=15
status	rc=0	secs=15
flicker-check	rc=0	secs=45
status-seed-overlap	rc=0	secs=30
overprovision-check	rc=0	secs=15
overcount-check	rc=0	secs=15
limit-stall-check	rc=0	secs=15
priority-fairness-check	rc=0	secs=15
backlog-rescan-check	rc=0	secs=15
bkill-hygiene	rc=0	secs=90
runner-started-timeout-check	rc=0	secs=15
ttrmiss-check	rc=0	secs=15
confirm-dead-leak	rc=0	secs=45
report-storm	rc=0	secs=15
report-storm-profile	rc=0	secs=45
remap-stall-check	rc=0	secs=30
freelist-check	rc=0	secs=46
selfconnect-check	rc=0	secs=90
idle-backlog-cpu	rc=0	secs=60
control-rpc-history	rc=0	secs=30
dep-granularity-check	rc=0	secs=45
exec-impossible-retries	rc=0	secs=15
transient-start-retries	rc=0	secs=165
runner-log-bytes	rc=0	secs=15
retention-check	rc=0	secs=135
web-burst	rc=0	secs=181
backup-stall-fast	rc=0	secs=240
writestorm-freeze	rc=0	secs=45
archive-rate	rc=0	secs=210
archive-ceiling	rc=0	secs=406
add-storm	rc=0	secs=345
add-storm-fixture	rc=0	secs=60
add-storm-lsf	rc=0	secs=315
add-storm-lsf-fix120k	rc=0	secs=330
unsuspend-burst	rc=0	secs=211
start	rc=0	secs=15
probe	rc=0	secs=15
churn	rc=0	secs=305
monitor	rc=0	secs=15
stop	rc=0	secs=15
limit-drain	rc=0	secs=1792
backup-stall-check	rc=0	secs=556
report-storm-lsf	rc=0	secs=526
crash-recovery	rc=0	secs=45
prodsim	rc=0	secs=7284
prod-start	rc=0	secs=30
prod-stop	rc=0	secs=15
dump	rc=0	secs=23
clean	rc=0	secs=15
```

## Production-scale soaks (item 6.3)

Two LSF crash soaks, run one after the other on farm22-wrstat01, each from
battery10's `soak-go.sh` with only F3's values changed (copies in
`$G/soak-base/soak-go.sh` and `$G/soak-change/soak-go.sh`). Each tree is a
`git archive | tar -x` export plus `git init`, and each soak's `run.sh` built
its own wr into its own `SOAK_ROOT`, which nothing replaced during the run.
Each `FIXTURE` was a fresh `cp -p` of `$G/fixtures/fix120k.db` made just
before its soak. Ports were checked free with `ss -ltn` and all `OS_*`
variables were unset. Both soaks were analysed with this tree's tools (the
README steps, `dbstart` built from `$G/src-change`, and `soakgate.py`);
outputs are in `$G/analysis/{base,change}/`.

| | Baseline | Change |
| --- | --- | --- |
| tree | develop 72384e4c, `$G/src-base` | a7ebb498, `$G/src-change` |
| run directory | `$G/soak-base/run/prodsim-1791354279` | `$G/soak-change/run/prodsim-1791365645` |
| ran | 07:24-10:27 | 10:34-13:38 |
| ports | 51950-51953, pprof 6250 | 51960-51963, pprof 6260 |
| peak RUN | 4511 | 3920 |
| mean RUN / mean PEND | 3106 / 659 | 2468 / 1340 |
| runs (keys) | 841,581 (824,825) | 731,234 (721,094) |
| stops | 17: 12 crashes (6 injected), 5 clean | 17: 12 crashes (6 injected), 5 clean |
| injected stall | 1791357563 to 1791357770 (207s) | 1791368940 to 1791369146 (206s) |
| manager peak RSS / heap in use | 27,991MB / 9,089MB | 41,591MB / 18,042MB |
| live jobs in the DB at the end | 319,998 | 717,597 |

`soakgate.py --source warning` (baseline):

```text
nondurable source=warning inside=1 outside=31885 runs=841581 outsidePct=3.7887
totals n/a
doubles inside=665 outside=2 acknowledged=0
missing ran=824825 absent=0 excused=251
peakRUN=4511
double portal_dedupe 20261007T074510.6291 outside reserved=1791356052 acknowledged=no
double portal_dedupe 20261007T074510.20444 outside reserved=1791356057 acknowledged=no
```

`soakgate.py --source d1` (change):

```text
nondurable source=d1 inside=612 outside=10159 runs=731234 outsidePct=1.3893
totals ok
doubles inside=606 outside=1 acknowledged=0
missing ran=721094 absent=0 excused=243
peakRUN=3920
double portal_dedupe 20261007T115143.17261 outside reserved=1791372335 acknowledged=no
```

### Verdict under the current F3

The change soak passes F3 as the owner revised it on 2026-10-07 (prompt.md
Notes; spec F3). The verdict under the original criteria is kept as history
below.

1. Scale (reported, not gated): not reached. Peaks were 4,511 (baseline)
   and 3,920 (change) against the 5,500 target. The ramp tops out at a
   portal target of 4,000, and battery10's 6,104 was a one-sample spike at
   the end of its 518s start (A2), with 3 other samples above 5,500. The
   longest start here was 49s in the baseline and 90s in the change soak
   (the failed start below). The farm was busier during the change soak:
   LSF held more of its runners pending (mean PEND 1,340 against 659), and
   `lsfprobe.tsv` reported "not enough processor units" in 37 of 91 probes,
   against 19 of 90 in the baseline. The two soaks therefore did not run at
   comparable scale.
2. Non-durable hand-outs: PASS. `totals ok`, and the change's `outsidePct`
   is 1.3893 against the baseline's approximate 3.7887, a ratio of about
   0.37, under the 0.5 allowed. Of the 10,771 D1 lines, 612 fall in the
   stall window. Most of the rest come in bursts under load: 1,436 in the 3
   minutes after the start that followed the stall crash (11:32:48), 2,235
   in the process started at 11:55:41, and 6,223 in the one started at
   12:11:36, of which 3,640 came in its first 3 minutes. Meanwhile the
   manager logged requests taking more than 10s (for example 2,015 slow
   `jstart` and 738 slow `jarchive` calls in the minute to 12:16:47) and
   archive-fold transactions of up to 7.9s.
3. Double runs: PASS. Outside stalls the change soak had one double run and
   the baseline two. All three were at a scheduled clean stop, and none was
   caused by the change. The clean-stop double run is queued separately as
   item 5 in `delivery-queue.md`.
   - Change soak: the double is not a non-durable hand-out, since no D1 line
     names its key (d1582bc1...). Its first run (node-13-08 pid 2170469,
     runner log `runnerlogs/26.10.07/12-12-10.node-13-08.2081262`) was
     reserved and started at 12:25:35 and exited 0 at 12:26:28. Its runner
     never reported the completion: it logged "gave up waiting for the
     resource checking goroutine to stop" at 12:27:29 and "aborting due to
     signal" (interrupt) at 12:27:47. Meanwhile the scheduled clean stop that
     began at 12:26:38 logged "gave up waiting for runners to exit" at
     12:27:39. The job was not complete in the DB, so a later manager ran it
     again at 12:44:11 on node-11-2-2.
   - Baseline: the 2 outside doubles are also at a clean stop: each run
     exited 0 just as the stop's "kill requested externally" reached its
     runner (07:55:33).
4. Missing jobs: PASS. `absent=0` in both soaks, and `relburycheck.py`
   reports problems 0 in both.
5. `rundepcheck.py`: PASS. Summary `{'OK': 36, 'CHECK': 4}`. All 4 CHECK
   lines (instances 8, 16, 28 and 37) end with `-noend-then-stop-clean`.
   The baseline's 4 CHECK lines (instances 16, 28, 36 and 37) do too.

Other observations: one scheduled start in the change soak (12:31:30) failed
because the manager port was still held by a socket that was not listening
after 1m30s. `watcher.sh` restarted the manager at 12:33:23. Both DBs end at
`schemaVersion=2`. Afterwards no manager, runner, fusestall mount or LSF job
of either soak remained.

The bursts investigation (`$G/analysis/bursts-findings.md`) found that the
change cut what it targeted. The best-effort writer's share of bbolt's
write lock fell from 32% to 4% of lock-holding samples, slow `jstart` calls
fell by 72% and slow `jarchive` calls by 73%. The remaining hand-outs come
from the archive writer and chunked portal adds, which now saturate the
lock between them, so a reservation still waits for whole rounds of those
writers.

### History: verdict under the original F3

Before the owner's 2026-10-07 revision, F3 gated on scale and on an
absolute non-durable bar, and allowed no double run outside stalls. Under
those criteria the change soak failed:

1. Scale: FAIL. Both peaks are below 5,500 (4,511 and 3,920), and
   |4511 - 3920| = 591 > 0.10 x 4511 = 451. The original F3 said such a soak
   is re-run, not counted against the change; the current spec has no such
   rule.
2. Non-durable hand-outs: FAIL. `outsidePct` is 1.3893 against a bar of
   0.0340 (10,159 outside hand-outs in 731,234 runs), about 40 times the
   bar at about soak9's peak RUN (3,920 against 3,872). The slowdowns
   behind the bursts are natural, not injected, so F3 did not excuse them.
3. Double runs: FAIL. `outside=1 acknowledged=0`, traced above.
4. Missing jobs: PASS.
5. `rundepcheck.py`: PASS.

The original verdict judged a re-run at higher scale unlikely to bring
criterion 2 under the bar, because the bursts come from commit latency
under load. The change's outside count was about a third of the baseline's
approximate warning count.
