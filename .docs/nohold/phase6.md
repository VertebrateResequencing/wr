# Phase 6: CHANGELOG and gates

Ref: [spec.md](spec.md) sections F1, G1, G2, G3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

The items run in sequence: F1, G1, G2, then G3, the last step before the
PR is ready. Each run is compared with the same run on the PR's
`origin/develop` merge-base, never concurrently with it, and every run
has all `OS_*` unset. Fix any failure in the story it belongs to.

## Items

### Item 6.1: F1 - CHANGELOG entries

spec.md section: F1

Add a `## [Unreleased]` section directly above
`## [0.39.0] - 2026-10-12` in `CHANGELOG.md`, followed by the two blank
lines the file puts before each version heading, with exactly the entries
F1 gives, under their headings and in their order. Covering the 1 acceptance
test from F1.

F1's text is in place verbatim, with two additions beyond F1:

- A second "Added" bullet for the `JStatus` fields `RunRAM`, `RunTime` and
  `RunDisk` from item 3.2. `wr status -o json`, the REST API
  (`jobsToStatuses`) and the web status page all emit `JStatus`, and F1's
  Go API bullet does not cover these fields. The bullet goes after F1's, so
  F1's own bullet stays first.
- A last bullet under Fixed, "Everyone", for `wr status`'s "note: command
  used too much RAM" and the web status page's over-limit highlights of
  RAM, time and disk. Before this branch both compared with
  `Requirements`: `copyJobForClient` never copies `RequirementsOrig`, so
  `wr status` fell back to `Requirements.RAM`, and the web page used
  `ExpectedRAM`, `ExpectedTime` and `RequestedDisk`. The RAC rewrites
  `Requirements` for a ready failed job, raising it after a high peak or
  replacing it with a learned value, so the note and highlights could go
  missing or appear falsely. Both now compare with what the last run
  reserved (`FailedRunReqs` and `RunRAM`, `RunTime`, `RunDisk`). The
  coordinator ruled it user-visible and asked for it to be listed. It goes
  last because it arises only for a failed command waiting to run again.

F1 itself changed: its Go API bullet said `FailBumpsCleared`, `ModifiedReqs`
and `FailedRunReqs` also appear in JSON output. They do not: JSON output is
`[]JStatus`, which lacks them, and only Go clients get them, through
`copyJobForClient`. The orchestrator, delegated by the owner, ruled that the
docs must match the code and that adding the fields to `JStatus` is out of
scope, so the clause is dropped from `CHANGELOG.md`, from F1 in `spec.md`,
and from the matching note after the `Job` fields in `spec.md`.

`make lint` runs only golangci-lint, so the formatting was checked against
0.39.0 by hand.

Considered and not added: item 5.4's scheduler double count was introduced
and fixed on this branch, so no release had it.

The re-review found that the Go API bullet lacked
`queue.Queue.GetRunningDataAndLeftReady`, added in phase 5. It is now in
that bullet in `CHANGELOG.md` and in F1 in `spec.md`. The last Fixed bullet
keeps "waiting to run again": the base compared with `Requirements`, which
it raised only in `prepareReadyJob`, so it was wrong only for a ready job
and right for a delayed or buried one.

The re-review also found a regression from item 3.2. `Job.lastRunReqs`
returned `FailedRunReqs` whenever `FailReason` was set, but `ttrCallback`
sets `FailReasonLost` on a running retry without recording a run end, and a
touch (`recoverLostTouchedJob`) clears `Lost` but leaves `FailReason`. A
retry at the raised RAM that went lost, or lost and then recovered, so
showed the failed run's RAM, time and disk as `RunRAM`, `RunTime` and
`RunDisk`, and the web page flagged use between the two sizes as over the
limit. The same holds for a retry reserved but never started: `ttrCallback`
marks it lost while its `State` is still reserved, and a touch leaves it
reserved with `FailReasonLost`. And every client copy of a lost job
(`copyJobForClient`, which does not copy `Lost`) has `State` lost, as do the
web interface's jobs (`reqToRepGroupJobs`, `reqToKeyJobs`), so the REST API,
`wr status -o json` and the web page read `lastRunReqs` from a lost `State`.
`lastRunReqs` now uses `FailedRunReqs` only when no run is in progress,
which `Job.runInProgress` decides from `State`: reserved, running or lost,
the states a job normally has only while its item is in the run sub-queue,
since every release sets `State` anew. Three paths break that. The web
interface's write of `State` can overwrite a release's
(`.docs/reserve-runstate/delivery-queue.md` row 16), and recovery can put a
lost `State` that write persisted into ready, so a failed job then shows its
next run's size. Recovery also puts an old client's reservation (Pid 0) into
ready with `State` reserved, which is harmless, since the reservation
cleared `FailReason`. A reservation clears `FailReason`, so while a run is
in progress its `FailReason` can only be this run's `FailReasonLost`.
Excluding `FailReasonLost` instead would be wrong: a lost run confirmed dead
goes through `applyRunEndLocked`, which sets `FailedRunReqs` to that run's
size while the recompute may move `Requirements` to a learned value.

Clearing `FailedRunReqs` at reservation was not chosen. `jobRunState` does
not carry it, so after a restart the live record's older value would come
back, and the fix would need a run-state format change. The other readers
were checked: `resetRequirementsForRecompute` and `failBumpBase` run only
for jobs with no run in progress, `statusRequiredRAMForProblem` requires
`Exited`, which a reservation clears, and `newJobExitData` runs only after
`applyRunEndLocked` (or for a job it already released), so it never sees an
earlier run's value. The `FailedRunReqs` and `statusRequiredRAMForProblem`
comments now say so.

Tests: `TestNoHoldStatusPageOverLimit` gains a lost retry (`RunRAM` is the
retry's RAM while lost, and after a touch recovers it, and the page does not
flag a peak between the sizes), red on 2e95d03c with `RunRAM` 10 for 1200. A
second leaf does the same for a retry reserved but never started. Both
leaves also read the client copies, from `getJobsByKeys` (the REST API and
web page) and from `GetByEssence` (`wr status -o json`), asserting `State`
lost and `RunRAM` 1200 while lost, and `RunRAM` 1200 after the touch. Those
reads were red, with `RunRAM` 10, under the earlier reserved-or-running
gate. A third leaf has a run released as lost after its group learned more
RAM, and its `RunRAM` stays the lost run's 10. A fourth leaf has the retry
at the raised RAM complete, and asserts `RunRAM` 1200 on the live job and on
both client copies, the server one read from the complete bucket. It pins
the `FailReason` gate, since `FailedRunReqs` is never cleared and an archive
clears only `FailReason`. Clearing `FailedRunReqs` at archive was not
chosen: it would change the exported field in complete records for no gain,
as the gate already covers it. `TestNoHoldHighPeakBumps` gains a lost and
recovered retry failing within its RAM, which records no high-peak stat. The
third leaf and the high-peak test pass on 2e95d03c and guard the
alternatives.

Mutants, each killed: dropping reserved from `runInProgress` (the reserved
leaf fails while lost), dropping running (the started leaf fails while
lost), dropping lost (both leaves' client copies fail), gating on `!j.Lost`
(fails after the touch), excluding `FailReasonLost` (fails the learned-RAM
leaf), and `applyRunEndLocked` keeping an earlier `FailedRunReqs` (fails the
high-peak test). Dropping `j.FailReason != ""` from `lastRunReqs` is killed
by the completed-retry leaf (`RunRAM` 10 for 1200).

- [x] implemented
- [x] reviewed

### Item 6.1b: kicked high-peak job reserved at unbumped RAM (flake investigation)

A review run of `go test -race -count 3 -run
'TestNoHold|TestReleaseAfterLost|TestHighPeak' ./jobqueue/` failed once in
`TestNoHoldHighPeakBumps`'s first leaf: a buried job that exceeded its RAM
was kicked, and the reserve got RAM 100 (the learned value) instead of the
bumped 1200. This is a product bug, not a flaky test.

Root cause: a RAC cycle prepares the jobs of its ready snapshot
(`prepareReadyJob`), but with the reserve hold gone a reserve can take a job
between that snapshot and its recompute. The reserve's
`resetJobForReservation` clears `FailReason` and `PeakRAM`, so the recompute
then finds no pending failure and sets the reserved job's RAM to the learned
100. The reserve copies the job for its runner only after
`persistReservation`'s durable write. A RAC recompute in that window hands
the runner the unbumped size, which is what the flake caught. One landing
later still leaves the manager recording 100 as the run's RAM, and
`lastRunReqs`, status and the next failure's `FailedRunReqs` all read that.
Before this branch the hold kept reserves out while a RAC ran.

Fix: `prepareReadyJob` recomputes through `recomputeIfStillReady`, which
skips the recompute when the job's queue item is no longer ready. It reads the
item's state under the job's lock. A reserve moves the item out of ready
before its reset takes that lock, so no reset can come between the check and
the recompute. A job with no item is recomputed as before:
`TestServerSchedulerQueuesAvoidPreserved` and the RAC benchmarks pass
unqueued jobs.

Hypotheses ruled out by the deterministic red: the test asserting before the
bury was applied (the bury's 1200 is asserted and passed), learned-stat
refresh timing and `ReqStatRefreshInterval` (the red needs no load), and the
ready hook's own recompute (the item had 1200 when reserved).

Red: a new `TestNoHoldHighPeakBumps` leaf holds the RAC that the kick triggers
at its gate, reserves the job (1200), opens the gate, and asserts the
manager's job and its client copy still have 1200. On 8b527918 the manager's
job has 100.

Load-matched loop: the first leaf alone, built with `-race`, run as 16
processes of `-count 125` with 8 CPU burners on 8 CPUs. On 8b527918, 50
failures in 6000 runs (24, 15 and 11 per 2000), all "Actual 100". With the
fix, 0 in 4000. The full test under the same load failed 1 in 320 on
8b527918. The loop's few "the server's publication gave up" startup failures
(1 on 8b527918, 1 with the fix) come from its 16 processes contending for
picked ports, and are unrelated.

Mutants: dropping the state check fails the new leaf (100). Skipping jobs with
no item fails `TestServerSchedulerQueuesAvoidPreserved`. Reading the state
before taking the job's lock survives: it reopens a window of a few
instructions that no test can hit without a seam there, and the comment
records why the order matters.

`BenchmarkBuildSchedulerGroupsBacklog`, interleaved, shows no slowdown (fix
94.3ms and 160.0ms against 101.0ms and 168.3ms, n=12). The `wrdev.sh`
speed comparison was not run.

- [x] implemented
- [x] reviewed

### Item 6.2: G1 - Local gates

spec.md section: G1

Run `make lint`, `make test` and `CGO_ENABLED=1 make race`; `make speed`,
re-running alone any row marked worse
(`SPEED_BENCH='^Name$' SPEED_COUNT=10 make speed`); and the G1 test 3
comparison of `Benchmark(Kick|Resume|Suspend|AddMany)UnderReserveLoad`.
For that comparison, copy `jobqueue/kick_resume_bench_test.go` into a
worktree of the base, since the base lacks the new
`BenchmarkAddManyUnderReserveLoad`, and pass that worktree as
`SPEED_BASE=<dir>`. Each passes when no row is more than 10% worse at
p < 0.05. Summarise the `make speed` verdict in the PR body as
DEVELOPERS.md section 5 asks: PASS or FAIL, the baseline, and every row
benchstat marks as changed. Explain any row that stays more than 10%
worse, giving the per-item and 118k-job recovery cost if a CPU profile
shows the hook's own cost is the cause; the owner decides whether to
accept it. Covering all 3 G1 gates. Depends on item 6.1.

- [ ] implemented
- [ ] reviewed

### Item 6.3: G2 - Real-LSF scenarios

spec.md section: G2

Run `wrdev.sh add-storm-lsf`, `wrdev.sh report-storm-lsf` and
`wrdev.sh limit-drain` (defaults) on the base and on this tree, one at a
time. Both storm modes require `WRDEV_PRISTINE_DB` and run on a copy of
it. Use the reserve-runstate gate's compacted fixtures in
`/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures/`: `fix120k.db`, which has
the `.aslmanifest` that `add-storm-lsf` needs, for `add-storm-lsf`, and
`pristine10`, as `developers/soak/sweep.sh` uses, for `report-storm-lsf`.
Each must pass by its own criteria on this tree:

- `add-storm-lsf` prints its `PASS: all <n> acknowledged adds` line.
- `report-storm-lsf` prints `FULLY DRAINED (complete=<n> buried=<b>)` and
  exits 0, rather than printing `CHURN/STALL REPRODUCED` and exiting 1.
- `limit-drain` drains every job.

Record both trees' jobs completed, wall time, lost and confirmed-dead
counts in the PR body. For `report-storm-lsf` also record its final
`## VERDICT: badjobDelta=<n> maxDelayed=<n> maxLost=<n> maxStatusRPC=<n>ms`
line and its longest manager-log `GAP` (a gap over 60s crosses the
client's receive floor). Explain any figure more than 10% worse than the
base's, any `badjobDelta` or `maxLost` above the base's, and any gap over
60s on this tree. Depends on item 6.2.

- [ ] implemented
- [ ] reviewed

### Item 6.4: G3 - prodsim soak

spec.md section: G3

Run two `developers/soak` soaks at production concurrency, base then this
tree, each with its own binary for the whole run, with the reserve-runstate
F3 staged ramp (`RAMP0=600`,
`RAMP="0:600 8:1200 16:2100 25:3000 40:3600 60:4000"`, `HOURS=3`) and its
crash and stall injectors. The stall injector needs FUSE: set
`USE_FUSE=1 FUSE_ON_AT=3 FUSE_OFF_AT=4 STALL_AFTER_MIN=7 STALL_SECS=180`,
as F3's launcher (`/nfs/hgi/wr/sb10-bigdb/battery10/soak-go.sh`) did.
With the default `USE_FUSE=0`, `run.sh` skips `fusestall`, `hook.sh` and
`stall.sh`, and `crashon.sh`'s `stall` trigger refuses to run (see
`developers/soak/README.md`). Check all 4 G3 pass criteria with `soakgate.py`
and `latency.py`: no double run or lost job attributable to the change
(tracing each one `soakgate.py` lists), steady ends per minute at least
90% of the base's, steady end-to-acknowledgement p50 and p99 no more than
10% worse, and peak RUN for both against the 5,500 target. Then
record both run directories, the figures and the verdict in the PR body.
Depends on item 6.3.

- [ ] implemented
- [ ] reviewed
