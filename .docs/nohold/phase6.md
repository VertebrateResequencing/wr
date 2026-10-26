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

### Item 6.1c: lost-release recompute races a reservation reset

Found by review, not by a failing run. `ackAlreadyReleased` applies an
owner's report of a run the manager already released as lost in two holds
of the job's lock: `replaceLostEndState` sets the owner's `FailReason` and
`PeakRAM`, then `recomputeJobRequirements` recomputes. The item is waiting
to run again, so a reserve can take it between the two. Its
`resetJobForReservation` clears `FailReason` and `PeakRAM`, the recompute
then finds no pending failure, skips `resetRequirementsForRecompute` and
the bump, and sets the reserved run's RAM to the learned value. As in
item 6.1b, the reserve copies the job for its runner only after
`persistReservation`, so the runner can be handed that size, and the
manager records it as the run's either way. Phase 3 (16f8070a) added the
recompute; 6f1b41bb had none. A reserve before `replaceLostEndState` is
harmless: `FailReason` is no longer `FailReasonLost`, so nothing is
replaced.

`finalizeBuriedWaiting` had the same shape: `applyRelease` and the
recompute took the lock separately. Its item is buried, so a kick in the
window makes it ready for a reserve.

Red: a new test-only seam, `lostEndStateAppliedHook`, is called once an
owner's report has been applied in `ackAlreadyReleased` and
`finalizeBuriedWaiting`. Two new `TestNoHoldOwnerReportAfterLost` leaves
seed low learning, lose a run, and send the owner's RAM release or bury
while the item is delayed. From the hook a second runner reserves the job:
for the release, once the delay moves the item to ready; for the bury, after
a kick. On 23f2c40c with the seam, that runner is handed 1200, and once the
report returns the manager's job has RAM 100 in both leaves.

Fix: a new `Server.replaceLostEndState` takes the job's lock once for
`replaceLostEndStateLocked` and, if it replaced the end state,
`recomputeJobRequirementsLocked`. A reserve then either comes first, and
nothing is replaced, or comes after the recompute and takes the bumped size.
`finalizeBuriedWaiting` applies the owner's end state, `applyReleaseLocked`
and the recompute under one hold. The lock order is unchanged: the
recompute takes only the learned-requirement cache's mutex under the job's
lock, as the ready hook does, and does no database I/O and no queue calls.
The unused `Job.applyRelease` wrapper is removed.

Mutants, each in a scratch worktree: splitting the release path back into
two holds, with the hook moved into the gap between applying the report and
the recompute, fails the release leaf (100); the same for the bury path
fails the bury leaf (100); both at once fail both. With the hook left after
both holds, as in the fix, a re-split survives: the seam cannot sit inside
the lock, so the new leaves prove the race and the fix but do not guard
against a later re-split. The comments on `Server.replaceLostEndState` and
`finalizeBuriedWaiting` record why the hold must not be split. Dropping the
release path's recompute fails C1 tests 9, 10 and 13 (10, 10 and 200), and
dropping the bury path's fails C1 test 11 (10).

Still open, and older than this branch: `finalizeBuriedWaiting` runs after
`BuryWaiting` has buried the item. A kick and reserve before its hold can
still have `updateAfterExit` and `applyReleaseLocked` act on the new run,
as 6f1b41bb's `applyRelease` did, and the recompute then bumps that run's
RAM. That needs its own fix, routed separately.

Gates: `make lint` 0 issues; `CGO_ENABLED=1 go test -race -count 3 -run
'TestNoHold|TestHighPeak|TestReleaseAfterLost' ./jobqueue/` passes;
`make test` and `CGO_ENABLED=1 make race` pass (998 passed). Neither path
is hot: both run only for an owner's report of a run already released, so
no benchmark was run.

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

Results (head cb7cc08c, base 6f1b41bb, all `OS_*` unset, a shared 8-CPU
host with load about 1 to 3.6):

- **G1.1: PASS.** `make lint` 0 issues; `make test` 998 passed, 22
  skipped; `CGO_ENABLED=1 make race` 998 passed, 21 skipped.
- **G1.2 `make speed`: PASS.** Baseline 6f1b41bb (the merge-base with
  `origin/develop`), 6 rounds, plus `report-storm` and
  `dep-granularity-check`. No row more than 10% worse at p < 0.05, so no
  isolated re-run was needed. Rows benchstat marks as changed:
  - `ReadyBacklogSnapshot` sec/op -22.65% (p=0.002), better
  - `UpdateJobState` B/op -1.70% (p=0.015)
  - `ArchiveSpacedArrivals` B/op +0.80%, allocs/op +3.20% (p=0.002)
  - `AddDepGroupMember` B/op +3.38%, allocs/op -1.25% (p=0.002)
  - `ArchiveJobs` allocs/op +1.70% (p=0.002)
  - `QueueLifecycle/groups=1` and `groups=100` B/op +0.03% and +0.02%
    (p=0.002)

  The worse allocation and B/op rows (`AddDepGroupMember` B/op +3.38%,
  `ArchiveSpacedArrivals` allocs +3.20%, `ArchiveJobs` allocs +1.70%) are
  well under the 10% gate and were not attributed to a change.

  `RACCycleCounted` and the `QueueLifecycle` `callback=running` variants
  are new on this branch, so the base reports nothing for them. Head:
  `RACCycleCounted/leavers=0` 87.0ms, 54 allocs; `leavers=100` 95.6ms,
  73 allocs (19 more than at `leavers=0`; phase 5 measured 73 at
  `leavers=100` against 55 with its change reverted). Scenarios,
  one round each, so only their own thresholds gate: `report-storm`
  917.9 to 2958.6 jobs/s, max archive 73 to 82ms; `dep-granularity-check`
  peak RSS 280 to 298MiB, recovery 0.236 to 0.215s; every scenario met
  its thresholds. (The full G1.3 run below also ran them, ungated:
  `report-storm` 923.4 to 1679.3 jobs/s, max archive 197 to 194ms;
  `dep-granularity-check` peak RSS 283 to 300MiB, recovery 0.170 to
  0.192s; none missed its thresholds.)
- **G1.3: FAIL, for reserve latency during kick, resume and suspend, and
  AddMany's reserve-max (the accepted hook cost).**
  Base: a scratch worktree of 6f1b41bb with this branch's
  `jobqueue/kick_resume_bench_test.go` copied in. It compiled unchanged.
  The first attempt died in round 2 when the benchmark's server lost its
  picked port to another process (`bind: address already in use`, so "the
  server's publication gave up"), and was re-run whole. Full run (10
  rounds), the rows benchstat marks as changed:

  | Row | Base | Head | Delta (p) |
  |---|---|---|---|
  | AddMany norc reserve-max | 14.13ms | 15.67ms | +10.91% (0.029) |
  | AddMany rc reserve-max | 13.25ms | 18.04ms | +36.21% (0.007) |
  | Resume reserve-p50 | 10.59us | 11.32us | +6.94% (0.009) |
  | Suspend reserve-p50 | 8.09us | 9.77us | +20.70% (0.043) |
  | Kick reserve-p99 | 257.6us | 409.8us | +59.08% (0.000) |
  | Resume reserve-p99 | 398.1us | 582.1us | +46.24% (0.015) |
  | Suspend reserve-p99 | 294.8us | 446.9us | +51.61% (0.023) |
  | Kick B/op | 195.2Mi | 184.9Mi | -5.28% (0.029) |
  | AddMany rc B/op | 159.6Mi | 161.8Mi | +1.36% (0.001) |
  | Kick allocs/op | 129.8k | 93.8k | -27.76% (0.000) |
  | Resume allocs/op | 72.6k | 55.8k | -23.05% (0.015) |
  | Suspend allocs/op | 72.0k | 57.0k | -20.82% (0.000) |
  | AddMany rc allocs/op | 861.6k | 909.1k | +5.51% (0.000) |

  sec/op, reserve-max (except AddMany) and reserves/op are unchanged in
  every benchmark.

  Isolated re-runs, each alone, 10 rounds, same base:

  | Benchmark | Rows still >10% worse | Other changes |
  |---|---|---|
  | Kick | p99 205.5 to 410.1us, +99.54% (p=0.000) | reserves/op -7.20%, allocs -15.91% |
  | Resume | p50 9.88 to 12.28us, +24.24% (p=0.004) | p99 414 to 479us (~, p=0.165); allocs -17.49% |
  | Suspend | p50 8.13 to 10.06us, +23.68% (p=0.004); p99 353.2 to 458.0us, +29.65% (p=0.002) | reserves/op -4.71%, allocs -37.42% |
  | AddMany | norc reserve-max 11.92 to 18.84ms, +58.01% (p=0.001); rc 12.58 to 17.05ms, +35.52% (p=0.001) | rc sec/op +3.41%; allocs norc +3.47%, rc +7.25%; B/op about +2%; p50, p99 unchanged |

  AddMany's reserve-max is the hook's cost that the owner accepted on
  2026-10-14 (item 4.4: about 13 to 17ms, recovery of about 118k live jobs
  about 37 to 55ms once under the queue's lock (0.31 to 0.47us per item);
  those per-item and recovery costs come from item 4.4's profile and were
  not re-profiled after phase 5 or 6.1). Head here is 15.7 to 18.8ms,
  at or below the top of 4.4's after-cut head range (16.5 to 20.5ms). This run's
  norc base (11.9ms) is lower than 4.4's 12.6 to 13.4ms, but the bases
  differ (6f1b41bb here, 16f8070a in 4.4) and reserve-max's spread is
  ±27 to 33%. So it needs no further cuts.

  Kick, Resume and Suspend reserve latency is new: item 4.4 compared them
  against 16f8070a and found them unchanged. Where it comes from, Kick
  alone unless named (the re-runs used `SPEED_SCENARIOS=none`; scratch
  worktrees with the benchmark file copied in):

  - Phase 3 (16f8070a) against head, Kick, Resume and Suspend, 10 rounds:
    every p99 still worse (Kick 244 to 432us, Resume 383 to 590us, Suspend
    295 to 441us), and Resume and Suspend p50 +30% and +20%. So phases 1
    to 3 are not the cause.
  - Phase 4 (a25b6cf6) against head, Kick and Suspend: the run died in
    round 6 on the same port collision. benchstat of its 5 complete rounds:
    Kick p99 246.9 to 624.9us, +153% (p=0.016), and reserve-max +77.40%
    (p=0.008); Suspend p99 396 to 627us
    (p=0.052, n=6+5). So the cause comes after phase 4, the phase that added
    the hook, so the hook alone is not it.
  - Phase 5 (2e95d03c) against head, 8 rounds: no row changed (p99 461.6
    to 597.4us, p=0.505). So 6.1, 6.1b and 6.1c add nothing measurable on
    top of phase 5. Two attempts failed before it: speed run
    `run-1792050604` (08:50) was killed and left no data, then
    `run-1792053931` (09:45) died on a port collision; these figures
    are from the third.
  - Head with 6.1b's production change reverted against head, 8 rounds: no
    row changed (p99 400.9 to 429.6us). 6.1b's per-job `q.Get` in the RAC
    is not the cause.
  - Head with phase 5's leaver recording turned off
    (`subQueue.recordLeavers` never records) against head, 8 rounds: no
    row changed (p99 480.9 to 525.0us). The recording on every reserve's
    pop is not the cause either.

  Profiles of Kick, 30 ops each, phase 4 against head:

  - The CPU profile finds no new work under the queue's lock. The hook barely
    appears under `KickWith` (one 10ms sample on head, none on phase 4;
    these jobs have no runner command), which
    takes 0.24s on phase 4 and 0.28s on head across the 30 ops. `Reserve`
    has the same callees on both, and `GetRunningDataAndLeftReady` does
    not appear.
  - The block profile shows the reservers waiting on the queue's write
    lock 4.75s on phase 4 and 7.25s on head (+53%).
  - The mutex profile charges that wait to the lock's holders: `Reserve`
    5.26s on phase 4 and 7.30s on head, `KickWith` 0.55s and 1.60s.

  So the latency is not the hook's own cost, nor 6.1b or 6.1c. It appears
  between phase 4 and head, most likely with phase 5, though direct phase 4
  against phase 5 comparisons are mixed (G1.3 phase 5 latency, below: no p99
  change in one run, p=0.105 in another, +37% in a third). The CPU these
  benchmarks spend under the queue's lock does not grow, so per-item and
  118k-job recovery costs do not apply. The reservers wait longer for the
  lock, but the profiles do not show which phase 5 change makes them. What
  the PR body must explain: during a bulk kick, resume or suspend, a
  reserve's p99 goes from about 0.2 to 0.4ms to about 0.4 to 0.6ms, and its
  p50 rises about 1 to 2.5us for resume and suspend. In this comparison the
  time to kick, resume or suspend, the slowest reserve and allocations are
  unchanged or better; against a base whose reserves wait on the hold (G1.3
  phase 5 latency, below) the ops take 15 to 33% longer and Kick allocs/op
  are 42% higher. Reserves served during a kick fall 7% (suspend 5%) in the
  isolated re-runs, and 13 to 15% against phases 3 and 4. The owner decides
  whether to accept it or have phase 5 investigated further.

  Raw outputs, outside the repo, are in
  `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-g1/`: `benchstat/`,
  `logs/`, `speed/run-*/` (benchstat and verdict files, no binaries) and
  `prof/` (p4 and head `.cpu`, `.block` and `.mutex`).

#### G1.3 phase 5 latency

Investigated on head cb7cc08c, a shared 8-CPU host. Other users' test
suites ran throughout, so the load varied from 1.5 to 5 and once reached
20. Method: test binaries built from scratch worktrees, each with this
branch's benchmark file copied in, run with `-benchtime 5x` (each sample
pools 5 kicks), interleaved round by round, compared with benchstat. Some
variants were also instrumented, in the scratch copies only. The queue
recorded how long `Reserve`, `KickWith` and `readyItemData` held its lock
during the timed window. The benchmark counted GCs, and "slow" reserves:
those taking over 150us.

- **What a reserve does during a kick on the base.** The benchmark calls
  `reserveItem` directly, so it never calls `handleReserve`'s
  `waitForPendingReserves`. The base's reserves in it therefore never
  waited on the hold, and the hold has no part in its base figures. A
  real base runner's reserve does wait on the hold. A scratch copy of the
  benchmark measured that on the base: 6f1b41bb with
  `s.waitForPendingReserves()` inside the timed reserve, and samples that
  end after the op still counted. Head got the same counting change. 6
  rounds:

  | Benchmark | Base reserves/op | Base reserve p50 | Head reserves/op | Head p50, p99 |
  |---|---|---|---|---|
  | Kick | 0 to 3.2 | about 527ms when any | 5,095 | 6.2us, 264us |
  | Resume | 0 to 3.2 | about 527ms when any | 4,384 | 9.7us, 612us |
  | Suspend | 0 to 1,220 (median 623) | 5.8us | 4,118 | 7.7us, 391us |

  So during a bulk kick or resume, the base serves almost no reserves: they
  wait about half a second for the hold. Head serves about 5,000, with a p99
  under 1ms. G1.3's base rows describe a reserve path no base runner had.
  The cost in that run is that the ops themselves take longer on head (Kick
  +15%, Resume +33%, Suspend +19%, p<0.01): the reserves now really run
  alongside them. Kick allocs/op are 42% higher and Suspend's slowest
  reserve rises from 0.8 to 4.7ms, likely because head serves about 5,000
  (Kick) and 4,118 (Suspend) reserves per op where the base served 0 to 3.2
  (Kick) or 0 to 1,220 (Suspend) (the benchmark counts allocations across
  the whole process). Suspend p50 is +34.45% (p=0.039), 5.8 to 7.7us; two of
  the base's six rounds served no reserves and record 0, which lowers its
  p50 and max medians. G1.3's sec/op shows no such change, because there the
  base's reserves skipped the hold too.
- **Kick: part of it may come from the removal of `kickJobs`' `q.Get`.**
  Phase 4's `kickJobs` called `s.q.Get(key)`, which takes the queue's read
  lock, between each job's encode and its `KickWith`. It used the result
  to decide whether to set the hold. Phase 5 removed it. Results, rounds:
  - p4 against p5 against head, uninstrumented (8 rounds): no latency row
    changed; head B/op -4.37% (p=0.050). Reserve p99 332, 334 (p=1.000) and
    415us (p=0.574). At this sample size the p99 difference does not always
    reach significance.
  - p4 against p5 against head (instrumented, 8 rounds): slow reserves
    271 to 359 (+33%, p=0.003) on p5 and to 377 (+39%) on head. Reserve
    p99 itself did not differ significantly (p4 477 against p5 610us,
    p=0.105, and head 596us, p=0.328); only the slow-reserve count did. Per-hold
    lock times did not change on any tree: `KickWith` p99 about 25us and
    6.3 to 6.7ms per op; `Reserve` p99 about 18us and 30 to 33ms per op.
    (In the 12-round run below, head's total `Reserve` lock hold per op is
    12.5% lower than p4's, p=0.006, in line with it serving about 15%
    fewer reserves; per-hold times are unchanged.) The run's fourth
    variant, p5 with phase 4's whole pre-`KickWith` sequence restored (the
    `q.Get` and an `rpmutex` lock and unlock), brings back p4's
    slow-reserve count: 232.8 against p4's 270.6 (~, p=0.161), reserve p99
    435.4 against 477.3us (~, p=0.574).
    GCs per op did not change (1), and `readyItemData` ran 0 to 0.6
    times per op (median 0.1 to 0.2) alike on every tree, so a rac
    snapshot rarely falls in the window and does not differ between
    trees.
  - p5 with only the `q.Get` restored, against p4 (8 rounds): reserve p99
    288 against 305us (~), slow reserves ~. With only phase 4's
    `rpmutex` lock and unlock restored instead: p99 +51% (p=0.000), the
    same as p5's +37%.
  - p4 against head against head with the `q.Get` restored (12 rounds):
    p99 281, 542 (+93%, p=0.000) and 416us (+48%, p=0.000); slow
    reserves 234, 348 (+49%, p=0.000) and 290 (~). So on head the
    `q.Get` recovers about half of the p99 and about half of the extra
    slow reserves (in an earlier 8-round run, whose first round ran at
    load 11 to 20, restoring it showed no p99 change, p=0.878). The rest
    does not come with a phase 5 change these runs could isolate: head
    and p5 still do not differ.
  - Other variants of head (8 rounds, the first round at load 11 to 20): a
    `q.Get` state check before the encode instead made p50 +169%
    (p=0.000) and slow reserves +126%. A `runtime.Gosched()` before
    `KickWith` cut p99 33% and served 45% more reserves, but made the kick
    19% slower and allocs/op 22% higher.

  So the cause is not lock-held work: per-hold lock times did not change. The
  likeliest explanation, not proven, is that the kicker reaching the queue's
  `sync.RWMutex` write lock straight from its 130KB encode, rather than
  first taking a read lock (which waits behind any reserve holding or
  queued for the write lock), changes how the lock passes between the
  kicker and the 8 reservers. More reserves then wait over 150us. Where
  in the loop the read lock is taken matters, and in opposite directions
  (before the encode is much worse), so this looks like scheduling and
  lock hand-off behaviour rather than a cost the code can trim.
- **Resume and Suspend** were not pinned to a phase 5 change.
  `suspendJobs` and `suspendJob` are identical in phases 4 and 5. Phase 5
  only drops one `setRACPending` (an `rpmutex` lock and unlock) from
  `triggerReadyAddedCallback`, once per suspend batch, and changes the RAC
  the suspend triggers (leaver recording, which the Kick bisect showed has
  no effect). The only Suspend comparison against phase 4
  was inconclusive (p99 p=0.052, 5 rounds). Phase 5 took only
  `rpmutex`'s lock and unlock out of a resume, and on Kick that showed no
  effect.
- **No cut made.** The only change found that recovered some Kick
  latency is putting back a per-job queue read lock before `KickWith`
  that serves no purpose once the hold is gone. On head it recovered
  about half the p99 in one run and none in another,
  the effect is fragile (a read lock elsewhere makes it far worse), and
  it would trade CPU-free scheduling noise for code with no reason to
  exist. Of the alternatives tried, the read lock before the encode is
  far worse, and `Gosched` trades reserve latency for a 19% slower kick
  and 22% more allocations. The real comparison above shows that during
  a bulk kick or resume head serves about 5,000 reserves where the base
  served almost none; during a suspend it serves about 4,100 where the
  base served about 600, at a somewhat higher p50 and max. The owner
  decides whether to accept G1.3's Kick p99 of about 0.2 to 0.3ms rising
  to about 0.4 to 0.5ms, Resume and Suspend p99 of about 0.3 to 0.4ms
  rising to about 0.45 to 0.6ms, and their p50 rising about 1 to 2.5us,
  all measured against a base whose reserves skipped the hold.

  Raw outputs, outside the repo, are in
  `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-g1/`:
  `benchstat/g1.3-p5latency-*.txt`, `benchstat/raw-*`, `g13p5/` (the runs,
  with the runner `ab.sh` and the instrumentation scripts `instr.py` and
  `instr_bench.py`; the faithful-base modification of the benchmark was
  applied inline and not saved as a script), and `prof/` (p4 and head
  `.cpu`, `.block` and `.mutex`).

Owner decision 2026-10-16: accepted. The G1.3 reserve-latency rise
(Kick p99, Resume and Suspend p99, and Resume and Suspend p50) and the
15% to 33% longer kick, resume and suspend ops under real reserve load
are accepted with no cut; the PR body records the likely mechanism and
numbers above. The AddMany reserve-max rise is the
owner-accepted ready-hook cost (2026-10-14). G1 is therefore met. The
faithful-base run's higher Kick allocs/op and Suspend reserve-max come
from the extra reserves served and are covered by the same acceptance.

- [x] implemented
- [x] reviewed

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

G2 summary. Final figures are f04eaa3e against the base 6f1b41bb, run
interleaved on 2026-10-19 (item 6.3e, LSF section). Medians:

- `add-storm-lsf`, 6 runs each: every run printed `PASS`, with 0
  missing acknowledged adds, 0 lost and 0 confirmed dead. Run length is
  fixed: wall 317.5 against 318.5s (316 to 328 against 317 to 323s). Jobs
  completed 22,810 against 17,262 (+32%). Add p50 226 against 184ms
  (+23%, not significant, p=0.11), p90 -25%, p99 -19%, mean -5%. Kids
  completed 112 against 519. The add p50 gap is likely the same
  writer-occupancy cost as the accepted in-process gap (67 against 16ms),
  with archives/min +31% (p=0.004).
- `report-storm-lsf`, 2 runs each: every run printed `FULLY DRAINED
  (complete=100000 buried=0)` with `badjobDelta=0 maxDelayed=0
  maxLost=0` and 0 confirmed dead. Drain 200 against 198s (+1%), wall
  264 against 247s (+7%), `maxStatusRPC` 601 against 848ms (runs 432 and
  770 against 982 and 713ms). Longest `GAP`, the worst of each tree's 2
  runs: 22s against 25s (medians 21.5 against 23s), none over 60s.
- `limit-drain`, 1 run each: both drained all 60,000 jobs with 0 lost, 0
  bad job and 0 confirmed dead. Wall 969 against 978s, drain 936 against
  943s.

Owner-accepted costs: the add p50 gap (2026-10-19, item 6.3e; it covers the
LSF figure through the writer-occupancy attribution above); kid ordering, so
fewer self-added kids complete (2026-10-19, item 6.3 record); in-process archive
latency on NFS from writer occupancy (2026-10-19); the G1.3 reserve-latency rise
(2026-10-16, item 6.2). Not covered by an acceptance, and not significant:
archive `meanLock` +22% at LSF scale (p=0.33; cause not established: no rise
from write turns shows at LSF scale, but in-process it rose with them, partly
because it now includes the turn wait), and the in-process probe's archive p99
+15% (p=0.16; 959f3fd0 is +2%, so if real it comes with write turns). Explained:
a 5 to 12s pause after the first wave of reservations in 5 of the 27 post-6.3b
storm runs on the nohold trees (item 6.3f, from 6.3b's regroup). Open, likely
outside wr: two pauses of 18s and 27s in which every runner on one exec host
stopped (item 6.3f).

Results (2026-10-16; farm22-wrstat01, queue `normal`, `OS_*` unset).
The base is 6f1b41bb, built from a scratch worktree and run that
morning. The branch figures are the rerun at 959f3fd0, after item 6.3b,
run that evening. 6.3b changed only branch code, so the base figures
were reused.

Superseded: these are 959f3fd0 figures. The final G2 figures, on
f04eaa3e, are in item 6.3e's LSF section and the summary above.

Setup:

- Each tree had its own `WRDEV_ROOT` and binary, built before any of
  its runs, with ports 52610 to 52613. Runs went one at a time; scenario
  arguments were the defaults.
- `WRDEV_DEBUG=1` on both trees: the `confirmed_dead` figure needs it
  (without it the scenario prints `n/a`), it makes the manager log dense
  enough for a `GAP` to mean a freeze, and production runs with
  `--debug` too.
- Storm modes ran on their own copies of
  `/nfs/hgi/wr/sb10-bigdb/runstate-gate/fixtures/fix120k.db` and
  `pristine10`; the fixtures' mtimes are unchanged.
- Base outputs, gzipped manager logs and `g2run.sh` are in
  `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-g2/` (with the pre-6.3b
  branch runs). The rerun's are in
  `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-g2-rerun/`: `g2rerun.sh`,
  each run's `.out`, manager log, lost sampler and, for
  `add-storm-lsf`, its adder logs (`*.adderlogs.tgz`). There are also
  the reservation split (`split.sh`, `reserve-split.txt`) and
  `lsf-after-all.txt`.
- Provenance. The branch binary's `go version -m` shows
  `vcs.revision=959f3fd079a0965c5579989140552a35cc047811`,
  `vcs.modified=false` (`branch-wr.buildinfo.txt`, with its sha1
  `ddab837b...`). `wr version` prints nothing for these builds (no
  release ldflags), as each `.out` header shows. For the base only the
  sha1 prefix `b736a5e6c77b` in each `.out` header was saved.
- LSF empty before each run. Each rerun `.out` records `bjobs -w` from
  just before the run: "No unfinished job found" all 4 times. The
  base's checks were made by hand (`bjobs -w`, or an `until` loop on
  the previous run's job prefix) and not saved; its runs' start and end
  times in each `.out` are the only record. The rerun's `add-storm-lsf`
  run 1 CLEANUP printed `jobs left in LSF: 12`, already bkilled; run 2's
  saved `bjobs -w` shows they had gone before it started.
- Definitions. Wall time is the whole `wrdev.sh` invocation. Drain is
  the manager log's first `added jobs` to its last `completed job`. Lost
  is the highest `lost` count seen. Confirmed dead is the count of
  `killed a job after confirming it was dead` in the manager log.

- **`add-storm-lsf`: PASS on both trees, twice each.** Its run length is
  fixed (120s storm, a kill, 120s more), so wall time cannot show
  throughput; jobs completed is the manager log's `completed job` count
  over storm, recovered and kid jobs. Lost is not printed by the
  scenario. The primary evidence that none were lost is the manager
  logs: none contains a line mentioning `lost` or a confirm-dead kill,
  and the rerun's have no `released job` line. A read-only side sampler
  (`wr status -o counts` every 20s, `*.lostsamples`) never saw
  `lost contact` above 0. Its samples were empty while the manager was
  down or starting: 4 and 5 of 17 for the base, 3 and 2 of 16 for the
  rerun.

  | Run | Tree | Wall | Jobs completed | Acked adds (missing) | Add p50 / p99 / max | Lost | Confirmed dead | Bad job |
  |---|---|---|---|---|---|---|---|---|
  | 1 | base | 331s | 17,883 | 4,741 (0) | 198 / 1,153 / 1,798ms | 0 | 0 | 0 |
  | 1 | branch | 319s | 22,675 | 4,444 (0) | 296 / 1,311 / 1,755ms | 0 | 0 | 0 |
  | 2 | base | 327s | 18,475 | 4,921 (0) | 102 / 1,329 / 1,834ms | 0 | 0 | 0 |
  | 2 | branch | 320s | 21,762 | 4,492 (0) | 285 / 1,156 / 1,560ms | 0 | 0 | 0 |

  Both trees printed `PASS: all <n> acknowledged adds`, with no slow
  add (`overSlow=0`, `mgrSlowAdds=0`).

  Fixture reservations at the unlearned 600MB, by manager lifetime.
  `reserved job` lines are split at the second `recovering prior
  state` line, by kind (fixture `aslfix N plain|selfadd`, kid
  `aslfix N kid`) and by the `schedGrp` RAM. Kid reservations are in
  the learned 200 group on every run.

  | Run | Fixture at 600, first lifetime | After the restart | Kid reservations, first lifetime / after the restart |
  |---|---|---|---|
  | base 1 | 55% of 7,500 | 31% of 5,480 | 105 / 369 |
  | branch 1 | 1% of 9,149 | 0% of 9,001 | 0 / 112 |
  | base 2 | 48% of 8,661 | 32% of 4,647 | 0 / 582 |
  | branch 2 | 0.5% of 8,903 | 0% of 8,280 | 0 / 106 |

  So 6.3b does what it intends: almost every fixture job now runs at
  the learned size in both lifetimes. The remaining 97 and 44 are at the
  start, before learning. Before 6.3b, the branch ran 89% and 95% of
  its first-lifetime fixture jobs at 600 (see "Before item 6.3b"
  below).

  Figures more than 10% worse on this tree, each VERDICT field compared
  run for run with the base's:

  - **Kid completions:** `kidComplete` 112 against 474 (-76%) and 106
    against 582 (-82%), out of `kidJobs` 786 and 743 against 568 and
    590. Every kid reservation in the rerun is in
    `200:30:1:0:8cf0c858...~aslblock`, the same reserve group as 18,053
    fixture reservations (run 1). Item 6.3b describes the cause: the
    queue serves a group by priority, size, then creation, so kids
    queue behind about 40,000 older fixture jobs of equal priority. On
    the base, its stale 600 group took part of the fixture remainder
    out of their way. The logs fit this explanation (one shared
    group, and kid reservations fell as the 600 share went to 0), but
    the queue order itself was not traced here. (Later traced in item
    6.3c, "Kids".) More fixture jobs completed
    instead (18,122 and 17,106 against 12,943 and 13,253). The total
    is higher too (22,675 and 21,762 against 17,883 and 18,475).
  - **Add p50:** 296 against 198ms (+50%) and 285 against 102ms (+179%).
    Unlike the pre-6.3b runs, both rerun runs are above the base's
    slower run. The adder logs show it is not one burst. Per 30s
    window, the median was 169 to 422ms (run 1) and 185 to 435ms (run
    2), raised in both lifetimes; the first window was the lowest.
    Cause not found here. (Answered later: item 6.3d places it in
    phase 5, writer occupancy without the hold, and item 6.3e cuts it.)
    Two untested candidates: the extra job throughput
    (+27% and +18% completions) competing on the same manager, and 6.3b's
    per-cycle refresh of blocked ReqGroups. The base's own run-to-run
    spread is 2x (198 then 102ms), and the base's adder logs were
    deleted, so a like-for-like time profile is not available. Even so,
    the medians are under 300ms, against a 5,000ms bound and the 10s
    slow-request threshold.
  - **Add p99 in run 1:** 1,311 against 1,153ms (+14%). In run 2 it is
    13% better, and max is better in both.
  - **`ackedPreKill` in run 2:** 2,359 against 2,649 (-10.9%); in run 1
    2,231 against 2,456 (-9.2%). Both storms ran 120s before the kill,
    so this compares like for like. The adders are think-time paced, so
    slower adds mean fewer of them, the same cause as add p50.
    `adds`, `acked`, `measuredAdds`, `presentAfterRestart` and
    `stormJobsInDb` are 5 to 9% lower for the same reason.
  - **`maxStatusRPC` in run 2:** 242 against 106ms (+128%); in run 1
    167 against 186ms (10% better). Each is the single worst of the
    run's 24 samples, and all are under 250ms. The per-sample medians
    are 51.5 against 57.5ms and 55 against 57ms, lower on this tree.

  Other fields: every safety and zero field matches the base's
  (`missingAcked`, `neverAddedFound`, `unackedInDb`, `overSlow`,
  `mgrSlow*`, `*Buried`). `outage` and `stormComplete` are within 3%.
  `stormCompleteAtKill` is 2,011 against 2,005 and 2,020 against 2,242
  (-9.9%), another effect of fewer pre-kill adds. `notAcked` is 4%
  higher in run 1 (509 against 488) and 10% lower in run 2.
  `fixReadyNow` is 11% and 8% lower because more of the fixture had
  run. `ackedInKillWindow` (this tree against the base: 6 against 7,
  9 against 9) and `inFlightAtKill` (7 against 0, 3 against 3) count
  luck at the kill instant, so they are neither better nor worse. Peak
  LSF RUN is 90 against 97 and 64 against 96 while completing more
  jobs. `maxLSFPend` is 32 against 60 and 60 against 33. Neither is
  explained.
- **`report-storm-lsf`: PASS on both trees.**

  | Tree | Wall | Drain | Completed | Verdict line | Longest GAP |
  |---|---|---|---|---|---|
  | base | 302s | 248s | 100,000 | `badjobDelta=0 maxDelayed=0 maxLost=0 maxStatusRPC=571ms` | 20s |
  | branch | 323s | 253s | 100,000 | `badjobDelta=0 maxDelayed=0 maxLost=0 maxStatusRPC=547ms` | 25s |

  Both printed `FULLY DRAINED (complete=100000 buried=0)` and exited 0,
  with confirmed dead 0. `badjobDelta` and `maxLost` (0) equal the
  base's. No figure is more than 10% worse: wall +7%, drain +2%.
  `maxStatusRPC` is better, and the median status RPC is 276.5 against
  356ms. The branch peaked at 2,000 running, against the base's 1,529.
  Gaps: the base's 20s and 12s were the 100,000-job `wr add` and the
  wait for the first runner. The branch's 6s ends at the first `reserved
  job`. Its 25s is after the drain: from the last `completed job`
  (19:04:19) to an `archive fold` warning (19:04:44), with nothing left
  to run. No gap is over 60s.
- **`limit-drain`: PASS on both trees.**

  | Tree | Wall | Drain | Completed | Lost | Confirmed dead | Bad job | Archive reject | Max status RPC |
  |---|---|---|---|---|---|---|---|---|
  | base | 1,016s | 963s | 60,000 | 0 | 0 | 0 | 0 | 456ms |
  | branch | 968s | 948s | 60,000 | 0 | 0 | 0 | 0 | 146ms |

  Both printed `FULLY DRAINED (complete=60000 buried=0)`. No figure is
  worse; the status RPC medians are 118 against 135ms. The scenario
  stops neither manager, so each was stopped with `wrdev.sh stop`
  (verified pid, its own namespaced jobs; `limit-drain-branch.stop.out`).
  An `until` loop on `bjobs -J` for its job prefix then waited until
  LSF held none, and `lsf-after-all.txt` saves the final empty
  `bjobs -w`.

Every scenario passes on this tree by its own criteria. Nothing is
left running: no LSF job, no manager. The scratch worktrees, the
scratch `GOCACHE`s (the rerun's emptied with `GOCACHE=<dir> go clean
-cache`) and the work roots (DB copies, binaries) were removed.

Before item 6.3b (c39739ff; outputs in `nohold-g2/`, the split in
`nohold-g2-rerun/reserve-split.txt`), the three scenarios also passed.
A reviewer then found, from those manager logs, that the branch ran
89% and 95% of its first-lifetime fixture jobs at the stale 600 group
(base 55% and 48%, falling to about 30% by the kill). After the restart
it ran 0% at 600 but made only 1 and 2 kid reservations, against 369
and 582 on the base. That finding led to item 6.3b. Those runs
completed 26,008 and 32,822 jobs with add p50 274 and 139ms. In the
earlier write-up, `ackedInKillWindow` was quoted in base-then-branch
order. This tree against the base, it was 5 against 7 (run 1) and 11
against 9 (run 2).

**Load-matched add-storm A/B.** Item 6.3c found that add p50 tracks the
archive writer's `meanWait`, and that the base and branch runs above
were hours apart. So `add-storm-lsf` was rerun as interleaved base,
branch, base, branch, base, branch: 6 runs back to back, 19:55 to 20:27
on 2026-10-16.

- **Setup.** As above: defaults, `WRDEV_DEBUG=1`, fixture copies, ports
  52610 to 52613, one run at a time. Each tree had its own root and its
  own binary, built from clean scratch worktrees. `go version -m` shows
  `vcs.revision` 6f1b41bb and 959f3fd0, both with `vcs.modified=false`.
- **Checks.** Every `.out` saves a `bjobs -w` from just before its run:
  "No unfinished job found" all 6 times. Every CLEANUP reported 0 jobs
  left.
- **Measurement.** Add percentiles are over every acknowledged add in
  both trees' adder logs, the kill window included, so max is about
  22s on every run (the outage). The VERDICT p50, which leaves the kill
  window out, agrees within 1ms. `meanWait` is the mean of the run's
  four `archive fold` lines, as 6.3c computed it.
- **Where it is saved.** Outputs, manager logs, adder logs and scripts
  (`g2ab.sh`, `abdrive.sh`, `abstats.sh`, `split.sh`, `ab-summary.txt`)
  are in `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-g2-ab/`.

| Run | Tree | Acked (missing) | Jobs completed | Add p50 / p90 / p99 | meanWait (4 lines) | Mean | Host load | Kids complete | Fixture at 600, lifetime 1 / 2 |
|---|---|---|---|---|---|---|---|---|---|
| 1 | base | 4,666 (0) | 17,456 | 206 / 744 / 1,289ms | 35, 148, 115, 99ms | 99ms | 5.5 to 6.4 | 388 of 565 | 57% / 36% |
| 2 | branch | 5,125 (0) | 25,412 | 146 / 392 / 876ms | 19, 18, 84, 52ms | 43ms | 6.4 to 4.0 | 184 of 880 | 1% / 0% |
| 3 | base | 5,107 (0) | 21,285 | 108 / 433 / 926ms | 15, 32, 63, 72ms | 46ms | 4.0 to 4.6 | 692 of 696 | 60% / 31% |
| 4 | branch | 4,751 (0) | 21,143 | 232 / 500 / 1,252ms | 20, 74, 89, 81ms | 66ms | 4.6 to 5.1 | 101 of 705 | 2% / 0% |
| 5 | base | 5,150 (0) | 22,140 | 125 / 398 / 761ms | 16, 29, 77, 53ms | 44ms | 5.1 to 5.3 | 718 of 721 | 57% / 58% |
| 6 | branch | 4,878 (0) | 22,667 | 213 / 466 / 881ms | 70, 29, 76, 75ms | 62ms | 5.1 to 5.6 | 125 of 766 | 1% / 0% |

All 6 printed `PASS`, with no acknowledged add missing.

Branch add p50 against the base:

- **Adjacent pairs.** Runs 1 and 2: -29%, but the base's `meanWait`
  was 2.3 times the branch's, so they are not matched. Runs 2 and 3:
  +35%, the best-matched pair (43 against 46ms). Runs 3 and 4: +115%;
  runs 4 and 5: +86%; runs 5 and 6: +70%. In each of those, the
  branch's `meanWait` was 62 to 66ms and the base's 44 to 46ms.
- **Matched `meanWait`.** At 43 to 46ms there is branch run 2, at
  146ms, against base runs 3 and 5, at 108 and 125ms: +17% to +35%.
  No base run fell at 62 to 66ms. Interpolating linearly between the
  base's points at 44 to 46ms (108 to 125ms) and 99ms (206ms) gives
  about 140 to 150ms there. Branch runs 4 and 6 (232 and 213ms) are
  then about +45% to +60%. That interpolation rests on 3 points. The
  rerun's runs (95 and 103ms, p50 285 and 296ms) against base run 1 (99ms,
  206ms) give about +40%.

So at matched archive-writer load the branch's add p50 was higher in
every comparison that could be matched, by about 25 to 90ms (+17% to
+60%). Those comparisons are branch run 2 against base runs 3 and 5,
branch runs 4 and 6 against the interpolation, and the rerun against
base run 1. Its p90 and p99 were not consistently worse: run 2's
392 and 876ms were at or below runs 3 and 5's (433 and 398ms; 926 and
761ms). This A/B did not run c39739ff. 6.3c's figures (c39739ff about
35 to 40% above the base at equal `meanWait`) suggest the cost predates
6.3b, but that was not tested here.

Another observation, not a cause: the branch's `meanWait` was higher
in 2 of 3 adjacent pairs, so its archives wait longer too. Runs 4 and
5 completed about the same number of jobs (21,143 and 22,140) but had
`meanWait` 66 and 44ms, and p50 232 and 125ms. So job throughput alone
does not explain it. This A/B does not show what, in the branch, makes
archives and adds wait longer.

Answered later: item 6.3d's bisect places the cost in phase 5 (writer
occupancy once runners no longer wait on the hold), and item 6.3e's write
turns cut it; the final figures are in 6.3e's LSF section.

Kids and sizes match the earlier runs. The branch ran 98 to 99% of its
fixture reservations at 200 in lifetime 1 and all of them in lifetime 2.
The base ran 57 to 60% at 600 before the kill. The branch completed
101 to 184 kids, against 388 to 718 on the base, for the ordering
reason 6.3c traced. Nothing is left running: the final `bjobs -w`
(`lsf-after-all.txt`) is empty. The scratch worktrees, the scratch
`GOCACHE` (emptied with `GOCACHE=<dir> go clean -cache`) and the work
roots were removed.

- [x] implemented
- [x] reviewed

### Item 6.3b: ready jobs keep their unlearned size (add-storm-lsf finding)

Problem. In item 6.3's `add-storm-lsf` runs, this tree's recovered fixture
jobs mostly ran at their unlearned 600MB for the whole first manager
lifetime, while the base moved about half of them to the learned 200MB.
After the restart the reverse held: every fixture job ran at 200 here, but
the kids' reservations dropped to 1 or 2.

Evidence. Manager logs in
`/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-g2/`. `reserved job` lines
were split by manager lifetime (the boundary is the second `recovering
prior state` line) and by kind: fixture (`aslfix N plain` and `selfadd`),
kid (`aslfix N kid`) and storm (`sleep`). Learning (the first fixture
reservation at 200) arrived 6 to 7s after the first reservation on both
trees. The fixture's limit group is `aslblock:30`.

| Run | Fixture at 600, per 30s before the kill | Before the kill | After the restart | Kid reservations after the restart |
|---|---|---|---|---|
| base 1 | 72, 64, 51, 40, 34% | 55% of 7,500 | 31% of 5,480 | 369 |
| branch 1 | 92, 84, 87, 89, 91% | 89% of 12,562 | 0% of 8,449 | 1 |
| base 2 | 70, 51, 36, 33, 28% | 48% of 8,661 | 32% of 4,647 | 582 |
| branch 2 | 94, 96, 94, 94, 92% | 94% of 16,285 | 0% of 11,137 | 2 |

The last bucket is partial. Storm jobs, which are added during the run,
ran at 200 before the kill on both trees (2 to 3% at 600, 17% in base
run 2). Over each whole run, 45% and 42% of the base's fixture runs were
at 600, against 53% and 56% here. In the RAC's `rac saw ready jobs` lines
before the kill:

- The base counted 5,481 and 6,498 fixture jobs into the 200 group. Its
  600 group had a count above 0 in only 11 and 9 cycles.
- This tree counted 1,591 and 986 into the 200 group. Its 600 group had a
  count of 1 to 4 in 98 and 138 cycles.
- On both trees the 600 group's skipped count stayed large: 52,017 and
  50,313 at the base's last pre-kill cycle, 47,396 and 43,745 here. The
  base still had 46,697 and 46,694 skipped in it at its last cycle after
  the restart.

Mechanism: (a), the expected result of removing the hold, not a bug in
the RAC or ready path.

- On both trees the recovered limit-blocked remainder gets a real reserve
  group at the size known when it first became ready (here 600, since the
  fixture database holds no stats). On the base, the first RAC's
  `recordSkippedReadyJob` -> `ensureReserveGroup` sets it from the
  unprepared snapshot. Here the ready hook and `prepareBeforeQueueing` set
  it at recovery. Neither tree's RAC recomputes a limit-blocked job
  (`ensureReserveGroup` does not recompute, by design, and spec D3.2
  keeps that). So runners already submitted for the 600 group keep
  reserving it on both trees.
- What moved the base's jobs to 200 was the hold. Each archive frees a
  slot of `aslblock` and sets `racPending` (`decrementGroupCount` on a
  group with skipped jobs), so the runner's next reserve waits. The RAC
  then has the freed slot as budget. It prepares the front candidates of
  the first group it visits (`readyItemData` walks `groupedItems`, a
  map), which regroups 600-group jobs to 200 before any 600 runner can
  take them.
- Without the hold, the 600 runner reserves the front item at once, so by
  the time the RAC runs, the budget is spent. The few candidates it does
  prepare are the same front items the runners take, and
  `recomputeIfStillReady` (item 6.1b) correctly skips those already
  reserved. They are then counted at 600, which is what keeps this tree's
  600 group asking for runners. So the RAC never catches up while the
  limit group stays saturated. The stale group drains only by running:
  about 45,000 jobs at 84 to 105 per second, 7 to 9 more minutes here,
  had there been no restart.
- After the restart this tree's recovery prepares the remainder at the
  learned 200 (spec D2, correct). The base's recovery does not, so 31 to
  32% of its post-restart fixture runs were still at 600. The kids
  (`--memory 100M`, `-l aslblock`, learned 200) then share
  `200~aslblock` with about 47,000 older fixture jobs of equal priority.
  The queue serves a reserve group by priority, size, then creation, so
  they wait behind them. On the base they were served because the
  remainder sat in the stale 600 group. Over the whole run this tree
  completed more kids (558 and 702, against 474 and 582). Only their
  order relative to equal-priority older jobs changed.

Over-reservation: at 30 concurrent fixture runs, each run at 600 reserves
400MB more than the learned size. Before the kill that is about 10.7 to
11.3GB here, against about 5.6 to 6.6GB on the base, which was falling
to about 3.4 to 4.1GB by the kill. After the restart it is 0 here,
against about 3.7GB on the base. When learning raises a size instead,
stale jobs run under-sized and can fail for RAM before their bump. The
base has the same exposure for its stale remainder, but its hold resized
most jobs just before they ran.

Red/green. `.docs/nohold/learnchange_probe_test.go.txt` holds
`TestNoHoldLearningAfterReady`, which uses only what both trees have. It
learns from real archives (peak 150, learned 200) of jobs added at 600,
with runner command `serverRC`, and counts finished RACs through a
wrapped ready-added callback. It was copied into scratch worktrees of
6f1b41bb and c39739ff and run with `go test -tags netgo -count 3 -run
TestNoHoldLearningAfterReady ./jobqueue`:

1. A schedulable ready job is moved to 200 and its group, and a runner of
   that group reserves it at 200, once a RAC runs after a completion:
   passes on both trees. So the RAC still re-sizes the jobs it prepares,
   through the real archive's cache invalidation, the change-only memo and
   `recomputeIfStillReady`. Mutants in the scratch head copy: skipping
   `prepareReadyJob`'s recompute fails it (600, not 200); never
   invalidating the memo on a change fails it (group `700:30:1:0` against
   `300:30:1:0`).
2. A job blocked behind its full limit group is not re-sized by a RAC
   after learning changes: it stays at 600 on both trees, as spec D3.2
   says. This is not a regression.
3. With the RAC that the slot holder's archive triggers held for 1s, a
   runner of the old group reserving at once gets the blocked job: the
   base returns nil (its reserve waits for the RAC, which has moved the
   job to 200), and this tree returns it at RAM 600 (3 of 3 runs each).
   This is the mechanism above, made deterministic. It is red here only
   because the reserve no longer waits, which is spec E1's contract, so
   it is not a test to make green as written.

Options (not implemented; for the owner):

1. Accept and document it. Jobs waiting behind a busy limit group run at
   the size known when they became ready until the RAC reaches them,
   which under saturation is not before they run. This is a user-visible
   change in reserved memory (DEVELOPERS.md rule 10), so it would need a
   CHANGELOG line.
2. Re-size stale ready jobs when a ReqGroup's learned values change. Each
   job would record the learned RAM, disk and time it was last prepared
   with. The RAC's cheap pre-pass would compare those with the cycle's
   values for its ReqGroup, an O(1) compare per job, and recompute and
   regroup only on a difference, limit-blocked jobs included. Rounded
   learned values change rarely, so the extra work is one recompute and
   one `SetReserveGroup` per stale job per change. Here that is a single
   burst of about 60,000, which a bulk `SetReserveGroups` would make one
   queue-lock hold. It amends spec D3.2 and B3.3 (`racScanWork` would no
   longer be bounded by the schedulable count on a change cycle), so it
   needs the owner's ruling and the reliable4 rac-bound tests updated.
   This is the recommended option.
3. Check at reserve time. If a popped job's ReqGroup values differ from
   those it was prepared with, recompute it, and if its group changes,
   regroup it and give the runner the next item. This puts a cache read
   on every reserve and complicates the reserve path. Not recommended.
4. Regroup a ReqGroup's ready jobs when a background load after an
   invalidation completes. This needs an index from ReqGroup to ready
   jobs, and does the same work as option 2 off the RAC.

No code was changed, so no gates were run. The scratch worktrees and
`GOCACHE` were removed.

Owner decision 2026-10-16: option 2 (fix in the RAC). Record per job
the learned values it was last prepared with; the RAC's cheap pre-pass
recomputes and regroups any ready job, limit-blocked included, whose
values differ from the cycle's, in bulk. Amend spec D3.2 and B3.3 and the
reliable4 rac-bound tests to match, then rerun item 6.3's LSF gates.

Implementation (uncommitted on c39739ff). Spec: Overview, Packages, queue
API, new A4, B3.3, D3 (rewritten, tests 2 to 8 added), F1 and two Key
Decisions amended. CHANGELOG: `RegroupReady` under Added, a Fixed line for
commands waiting behind a full limit group.

Design:

- `updateJobRequirementsForRetry` records `learnedReqsOf(rec)` on the job
  (`preparedLearned`: RAM, disk, and time rounded up to the half hour as
  `reqForScheduler` rounds it; nil is zero), and resets `preparedStamp` to 0
  when that changes. Every recompute path records it, the hook included.
- The cache keeps a change stamp (`reqStatCache.changes`, from 1) that a load
  increments when it changes a ReqGroup's `learnedReqsOf`. The snapshot
  carries the job's `preparedStamp` in the padding beside its priority, so it
  does not grow.
- Each RAC cycle with a runner command reads the stamp (`stamp0`), refreshes
  every ReqGroup it has compared a blocked job of (`newRACBlockedJobs`),
  since with every ready job blocked nothing else loads it, then reads the
  stamp again (`stamp1`). For a blocked job (`recordSkippedReadyJob`): a
  snapshot stamp equal to `stamp1` is skipped, one compare. Otherwise its
  ReqGroup and values are read under its read lock, the cycle's values looked
  up with no job lock held, and if equal the job gets `stamp0`
  (`markLearnedCurrent`). The compared ReqGroups go into a cycle-local set,
  merged into the refreshed set once per cycle. If they
  differ, `refreshSkippedReadyJob` recomputes it through
  `recomputeIfStillReady` (6.1b: a job a reserve took is left alone), counts
  a unit of `racScanWork`, and counts it as skipped in its new group.
- Blocked jobs whose group changed (this replaces `ensureReserveGroup`) are
  moved by `q.RegroupReady`, 1,024 keys per call (`racRegroupChunk`), each
  call one hold of the queue's lock. Its callback (`regroupReadyJob`) sets
  `job.schedulerGroup` to the derived group under the queue's lock, so the
  job and its item never disagree. Items no longer ready are skipped. The
  queue rebuilds a group's heap once when many of its items move
  (`subQueue.regroup`, `manyOf`), instead of a heap fix per item, which took
  one hold for 60,000 items from about 180ms to about 60ms; chunking then
  took the longest hold to the base's level (about 6ms, below).
- Lock order is unchanged: queue, then job, then cache `mu`. No bolt I/O or
  queue call happens under a job lock, the callback takes no Server mutex,
  and nothing waits for the RAC.
- `racRegrouped` (inert) counts the items the bulk regroup moved.

Tests:

- `jobqueue/nohold_learnchange_test.go` (`TestNoHoldLearningAfterReady`,
  from the probe, which is deleted): D3 tests 3 to 6. Red on c39739ff plus
  only the unused `queue.RegroupReady` and the inert `racRegrouped` field:
  D3.3 fails at "blocked job RAM",
  Expected 200, Actual 600; every other leaf passed there. Green after.
  The probe's reserve-during-held-RAC leaf is replaced by D3.3's "reserves
  it at 200 once the slot frees" and D3.5's reserved-after-snapshot leaf.
- `queue/regroup_ready_test.go`: A4, plus a repeated key and priority order
  after moving 1, 60, 100, and 60 then 1 of 100 items.
- `reliable4_rac_bound_test.go`: the "keep their size" leaf now asserts 11
  (5 + 6) on the change cycle, all 6 at learned 500 and out of their old
  group, then 5 on each of the next two. A new leaf: learned time 20s gives
  11, then 25s (same half hour) gives 5.
- `limit_uncounted_test.go` (`TestLimitSetOnRunningGroup`): its jobs now
  use Override 2. Their `echo` completions teach `fake_group` RAM 100, which
  now moves the blocked jobs out of the test's hard-coded RAM 10 group.
- `&Server{}` fixtures that reach the blocked path got
  `db: racNoStatsDB(...)`, a cache with current empty entries, in
  `rac_backlog_bench_test.go`, `rac_limit_groups_test.go`,
  `rac_cycle_groups_test.go`, `reliable3_overprovision_test.go` and
  `reliable3_rac_accounting_test.go`. `BenchmarkBuildSchedulerGroupsBacklog`'s
  fixture sets each job's scheduler group after its cold cycle, because its
  items are not queued, which `ensureReserveGroup` used to cover.

Mutants, in a scratch detached worktree of c39739ff plus this diff, running
`TestNoHoldLearningAfterReady`, `TestReliable4RacBoundedBySchedulable`,
`TestRACParsesLimitGroupsOncePerGroup`, `TestReliable4SchedulerGroupSnapshot*`
and `TestRegroupReady`:

| Mutant | Red |
|---|---|
| never compare learned values | LearningAfterReady, RacBounded |
| compare, but no bulk regroup | LearningAfterReady |
| recompute, but count the old snapshot (no regroup that cycle) | RacBounded |
| never record `preparedLearned` | LearningAfterReady, RacBounded |
| recompute without the still-ready check | LearningAfterReady (D3.5) |
| compare raw learned time | RacBounded (time leaf) |
| no cycle-start refresh of blocked ReqGroups | LearningAfterReady, RacBounded |
| bulk heap rebuild without `heap.Init` | RegroupReady |
| `RegroupReady` regroups non-ready items | RegroupReady |
| never mark a job current (compare every cycle) | survives: cost only |

Per-job instead of bulk regrouping has the same behaviour and is not
testable. `BenchmarkRACLearnedChange` shows the cost.

Speed: interleaved, 6 rounds each, c39739ff vs this diff, the same bench
files in both (`rac_learned_bench_test.go` is new, copied into the base with
its RAM check off, since the base does not resize). Shared host, load about
2.5. Raw files and benchstat output are in the session scratchpad
(`6.3b/bench-*.txt`, `benchstat2.txt`).

| Benchmark | base | this | delta |
|---|---|---|---|
| BuildSchedulerGroupsBacklog/priorities=1 (200k blocked) | 87.8ms | 91.2ms | ~ (p=0.82) |
| BuildSchedulerGroupsBacklog/priorities=4 | 154.8ms | 147.3ms | ~ (p=0.49) |
| RACCycleCounted/leavers=0 (100k schedulable) | 84.5ms | 87.9ms | ~ (p=1.0) |
| RACCycleCounted/leavers=100 | 84.9ms | 90.9ms | +7.1% (p=0.026) |
| ReadyBacklogSnapshot | 1.42ms | 1.39ms | ~ |
| RACLearnedChange (60k blocked, learned RAM changed) | 18.2ms | 229.5ms | new work |
| RACLearnedChange max queue-lock wait | 6.6ms | 78.8ms | new work |

B/op is unchanged on the no-change benchmarks. allocs/op is +3 per cycle,
constant: the cycle-start refresh's slice and the blocked-job state. No
no-change row is over 10%. A change cycle over 60,000 blocked jobs costs
about 230ms of RAC time and one queue-lock hold that kept a concurrent
reader waiting at most about 79ms, once per change in a ReqGroup's rounded
learned values. The first version, which kept each job's ReqGroup and
values in the snapshot (40 more bytes per job), was +7 to +24% on time
(BuildSchedulerGroupsBacklog +14% and +7%, RACCycleCounted +15% and +7%,
ReadyBacklogSnapshot +24%) and +45 to +55% on B/op for the same no-change
rows, so it was replaced.

Review fixes (coordinator review of the above, FAIL on one defect):

- Stamp race (blocking). The first stamp design read the stamp once, after
  the cycle-start refresh. A background `refreshReqGroupRecsLater` load in
  flight when the refresh reached its ReqGroup made that refresh keep the old
  values; the load then finished during the refresh of another ReqGroup and
  bumped the stamp; the cycle compared that ReqGroup's blocked jobs with the
  old values, found them equal and marked them with the new stamp, so they
  were skipped until some other change. Regression test first:
  `TestNoHoldLearnedStampRace` (spec D3.7), 20 trials so the refresh's map
  order meets the race in about half: red on that diff (14 of 20 trials
  stuck at RAM 100), green after, 5 runs under `-race`. Fix: read `stamp0`
  before the refresh and `stamp1` after; skip on `stamp1`, mark with
  `stamp0`. Mutants: marking with `stamp1` (the old code) fails
  `TestNoHoldLearnedStampRace`; reading only `stamp0` fails
  `TestReliable4RacBoundedBySchedulable` (11 on the change cycle becomes 5).
- Hold cut: `regroupSkippedReadyJobs` chunks its keys (`slices.Chunk`,
  1,024). `RegroupReady` pre-sizes `moved`, `oldGroups` and `isMoved` to
  `len(keys)`, and `subQueue.regroup` each leaving set.
- Minors: the spec's cost wording now says no rounded change in any
  ReqGroup, and gives the change=other cost; compared ReqGroups go into a
  cycle-local set instead of taking `racBlockedMu` per job. The refreshed set
  is not pruned: later cycles skip a current job without reading its
  ReqGroup, so they cannot tell a ReqGroup with no blocked jobs left from one
  whose jobs are all current, and pruning would bring back the no-reload case
  the refresh exists for. The first version's figure is corrected to +7 to
  +24% here and in the spec. A4 has a bulk-join leaf (10 then 50 into g2,
  `manyOf(50, 60)`, rebuilding g2 with its existing items); a mutant that
  drops the existing items fails it.

Speed after the review fixes (interleaved, 6 rounds each, c39739ff vs this
diff, same bench files, host load about 2; `6.3b/benchstat3.txt`).
`BenchmarkRACLearnedChange` now has three variants on its 60,000 blocked
jobs: change=ram (all resized), change=other (only another ReqGroup changed,
so all are compared and none resized) and change=none.

| Benchmark | base | this | delta |
|---|---|---|---|
| BuildSchedulerGroupsBacklog/priorities=1 | 94.1ms | 95.4ms | ~ (p=0.70) |
| BuildSchedulerGroupsBacklog/priorities=4 | 152.0ms | 158.6ms | ~ (p=0.24) |
| RACCycleCounted/leavers=0 | 79.4ms | 85.5ms | ~ (p=0.09) |
| RACCycleCounted/leavers=100 | 87.9ms | 93.6ms | ~ (p=0.18) |
| ReadyBacklogSnapshot | 1.40ms | 1.51ms | ~ (p=0.39) |
| RACLearnedChange/change=none | 14.9ms | 13.6ms | -8.3% |
| RACLearnedChange/change=other | 15.0ms | 24.0ms | compares 60k jobs |
| RACLearnedChange/change=ram | 14.7ms | 259.9ms | resizes 60k jobs |
| max queue-lock wait, change=ram | 6.5ms | 6.4ms | ~ (p=0.82) |

B/op is unchanged except change=ram (28.9MB against 4.1MB). allocs/op is +4
per cycle, constant. No no-change row is significantly worse. A change
cycle's longest queue-lock hold is now no worse than the base's, against
about 79ms unchunked; its RAC time rose from about 230ms to about 260ms, off
the queue's lock. A change in any ReqGroup costs the next cycle about 24ms
against 14ms at 60,000 blocked jobs, outside the queue's lock (RACs are
serialised by the queue's callback-running flag, not a Server mutex).

Second review (PASS) follow-ups:

- Docs: no RAC holds `racmutex`; RACs are serialised by the queue's
  callback-running flag. The spec's D3 and this item's Design and speed text
  now say so. "Compared once per change" is now "at most twice": the cycle
  after it, and once more if a load bumped the stamp during that cycle's
  refresh (`stamp0` then differs from the next `stamp1`), in the spec and the
  `learnedChanged` comment. prompt.md's last Note says chunks of 1,024 with
  one hold each.
- `wr mod` of a blocked job's ReqGroup: the job kept its stamp, so if the new
  ReqGroup had equal rounded values and no blocked job seen before, the job was
  skipped until some ReqGroup changed. Red test first:
  `TestNoHoldLearnedReqGroupModified` (spec D3.8) failed 3 of 3 runs at RAM
  600 against 500. Fix: `JobModifier.applyGrouping` resets `preparedStamp`
  when it changes the ReqGroup. Mutant: dropping the reset is the pre-fix code,
  red as above.

Gates (all `OS_*` unset), on the final diff: `make lint` 0 issues; `CGO_ENABLED=1 go test -race -count 2 -run
'TestNoHold|TestReliable4Rac|TestLimit|TestRegroup|TestModify' ./jobqueue/
./queue/` ok (jobqueue 253s, queue 1.4s); `make test` PASSED (1002 passed,
22 skipped, 34 packages, 4m25s);
`cleanorder -min-diff` on every edited Go file. Earlier, after the first
review fixes: `make test` PASSED (1001 passed, 4m3s) and `CGO_ENABLED=1 make
race` PASSED (1001 passed, 5m32s). Item 6.3's LSF gates are to be rerun (not
run here).

- [x] implemented
- [x] reviewed

### Item 6.3c: add latency and kid starvation after 6.3b

Question. Item 6.3's rerun at 959f3fd0 had add p50 296 and 285ms, against
198 and 102ms on the base and 274 and 139ms at c39739ff. Kid completions
fell to 112 and 106. What in 6.3b makes adds slower, and do the kids
really wait behind the fixture?

Verdict: 6.3b's code is not what slowed the adds. Nothing was cut, and no
code was changed. The kids do wait behind the older fixture jobs, as 6.3b
said; the base served them only because of its stale 600 group.

Add latency, from code reading:

- (a) The cycle-start refresh (`newRACBlockedJobs`) does synchronous bolt
  reads on the RAC goroutine: `refreshReqGroupRecs` for each ReqGroup in
  `racBlockedReqGroups`. A read happens only if an archive invalidated the
  entry, no load is in flight, and the last load started at least
  `ReqStatRefreshInterval` (1s) ago. Each read is three `View` cursor scans
  of that ReqGroup's distinct stat values. These reads take no queue, job
  or db lock, and hold the cache `mu` only around the bookkeeping, never
  across the I/O. A `View` does not take bolt's writer lock. Here the
  blocked ReqGroups are `echo` (fixture and kids) and `sleep` (storm). Both
  have schedulable jobs every cycle, so before 6.3b `prepareReadyJob` loaded
  them in the same cycle through the same per-cycle map. In this run the
  refresh adds about no loads.
- (a) Per blocked job, the cost is one stamp compare
  (`snapshot.learnedStamp == b.stamp`). The full compare, recompute and
  regroup run only after a rounded learned value changes. The manager logs
  show that happened once per run: the `600` to `200` move at 18:48:06 in
  run 1 and 18:54:03 in run 2, within 10s of the first reservation. Before
  it the RAC saw only `600:30` groups for `aslblock` and `asllimit`, and
  after it only `200:30` groups (run 1: 596 and 500 `rac saw ready jobs`
  lines). Nothing moved after the restart.
- (b) `RegroupReady` ran for that one cycle only (about 59 chunks of up to
  1,024 items, each hold about 6ms at most; see 6.3b's speed table). There
  is no trigger storm: 607 and 613 RACs, against 643 and 539 at c39739ff
  and 701 and 710 on the base.
- (c) More completions are not the cause either. c39739ff run 2 completed
  32,822 jobs (26,248 fixture) with add p50 139ms. 959f3fd0 completed
  22,675 and 21,762 with 296 and 285ms.

In-process A/B. `nohold_addlat_probe_test.go` (session scratchpad
`63c/`, not committed) was copied unchanged into scratch worktrees of
c39739ff and 959f3fd0. It runs a real server with `serverRC`, 60,000 ready
jobs of ReqGroup `fix` blocked at `blk:0`, and 20,000 `fix` learners at
`lrn:1000`. One goroutine reserves, starts and archives learners, so `fix`
keeps learning and its cache entry keeps being invalidated. 8 clients each
add one `sleep` job at `asllimit:30` every 20ms, and the probe times each
Add for 15s. Mode `stable`: learner peaks are 1 to 50MB, so the rounded
RAM stays at 100 after the first load (the LSF case). Mode `rising`: each
peak is 100MB above the last, so the rounded RAM changes at every load (at
most 1/s), and 959f3fd0 recomputes and regroups all 60,000 on almost every
cycle (the worst case). Runs were interleaved, host load 3 to 5. Raw
output is in `63c/ab1.txt` and `63c/ab-nfs.txt`.

| DB on | Mode | Tree | Add p50 per round | Add p90 | Mean RAC | RAC busy |
|---|---|---|---|---|---|---|
| local /tmp | stable | c39739ff | 6.06, 5.13, 5.39ms | 9.6 to 10.6ms | 27 to 31ms | 5 to 6% |
| local /tmp | stable | 959f3fd0 | 6.08, 5.32, 5.63ms | 10.0 to 11.8ms | 40 to 41ms | 7 to 8% |
| local /tmp | rising | c39739ff | 2.63, 2.97, 2.74ms | 6.0 to 6.6ms | 26 to 30ms | 5 to 6% |
| local /tmp | rising | 959f3fd0 | 2.89, 2.99, 2.58ms | 7.5 to 8.3ms | 149 to 193ms | 23 to 28% |
| NFS (/nfs/hgi) | stable | c39739ff | 8.49, 8.13ms | 13.8 to 16.0ms | 26 to 29ms | 6 to 7% |
| NFS (/nfs/hgi) | stable | 959f3fd0 | 8.19, 7.95ms | 13.9 to 14.6ms | 30 to 37ms | 6 to 8% |
| NFS (/nfs/hgi) | rising | c39739ff | 6.75, 6.57ms | 12.0 to 12.8ms | 28ms | 6% |
| NFS (/nfs/hgi) | rising | 959f3fd0 | 7.67, 7.54ms | 16.0 to 16.3ms | 370 to 377ms | 43 to 44% |

In the LSF case (`stable`), add p50 differs by -4 to +4% between the
trees. The higher mean RAC on 959f3fd0 is its one change cycle, when the
first archive teaches `fix` 100MB, spread over about 30 cycles. An
instrumented copy counted 79,000 jobs recomputed and regrouped once, the
stamp bumped once (to 2), and about 80,000 stamp skips per cycle after
that. Only `rising`, where every cycle regroups 60,000 jobs, costs adds
anything: on NFS, p50 +13 to +15% and p90 +25 to +35%, with the RAC busy
43%. The LSF logs show that case did not happen.

What the LSF add latency does track is the bolt write path. About a
quarter of the rerun's adds took 33 to 160ms (min 37 and 33ms, p10 52
and 55ms): a whole `wr add` process against an uncontended manager. The
rest spread evenly from 150ms to 1s, the shape of waiting on a shared
writer. Each run's add p50 follows the mean of its four `archive fold`
`meanWait` figures, which say how long an archive waited for the archive
writer:

| Run | Add p50 | Archive meanWait, the 4 one-minute lines | Mean |
|---|---|---|---|
| c39739ff run 2 | 139ms | 19, 22, 45, 100ms | 47ms |
| base run 2 | 102ms | 17, 23, 62, 88ms | 48ms |
| c39739ff run 1 | 274ms | 70, 97, 88, 54ms | 77ms |
| base run 1 | 198ms | 49, 38, 83, 144ms | 79ms |
| 959f3fd0 run 2 | 285ms | 71, 60, 117, 133ms | 95ms |
| 959f3fd0 run 1 | 296ms | 77, 132, 113, 89ms | 103ms |

The rerun's meanTx (22 to 38ms) and archives per minute (about 4,000 to
6,100) are like the other runs'. Its archives wait longer and fold less:
meanFold 2.7 to 3.4, against 3.1 to 5.9 on the base, with maxWait 1.0 to
1.5s. In run 1 the per-10s add p50 was 169 then 64ms until 18:48:20 (the
64ms window had 1,592 completions), then 207 to 539ms. Why the archive
writer waited longer that evening was not found. 6.3b does no bolt
writes. Two candidates are untested. One is the backup copy of the 3.3GB
DB, at least every 30s, competing with commit fsyncs on the shared NFS
(Part C, unfixed, the same on every tree). The other is NFS load at that
time of day: the evening `report-storm-lsf`'s meanWait was 521 to 706ms,
against 224 to 453ms on both trees that morning. At equal meanWait both
branch trees run about 35 to 40% above the base (139 against 102ms, 274
against 198ms), so that much may come from the branch itself, before
6.3b. Only an evening base run, with its adder logs kept, can tell these
apart.

Superseded: item 6.3d's bisect puts the cost in phase 5, the hold removal,
which keeps the bolt writer busier. The backup copy and the evening NFS load
candidates were not needed to explain it.

Kids. `queue/subqueue.go` `lessByPriority` orders a reserve group by
priority (higher first), then size (larger first), then `creation`
(older first), then `iid`. jobqueue never sets an item's size, so it is
0, and fixture, kids and storm jobs all have priority 0. `newItem` sets
`creation` to the enqueue time and `iid` from a global counter. All
recovered items share about one creation instant and keep the iid order
recovery enqueued them in, which is key order. So:

- Lifetime 1. The fixture remainder, about 50,000 items, sits in
  `200:30:1:0:8cf0c858...~aslblock` with creation at recovery. Every kid
  is added later into the same group, so it sorts behind all of them. The
  rerun reserved 0 kids in lifetime 1 in both runs. Fixture reservations
  went through the keyspace in order: in run 1, keys `00` to `26` were
  consumed before the kill.
- Lifetime 2. Recovery re-enqueues the remaining fixture and the
  lifetime-1 kids together, ordered by key. The sweep goes on from `26` to
  `43`. The 112 kids reserved are those whose keys are at or below the
  sweep front: 1 to 3 in each two-hex-digit prefix from `00` to `43`, none
  above it. Kids added in lifetime 2 come last again.
- Base. Its recovered remainder stayed in the stale 600 group. The 200
  group held only the fixture items its hold-time RAC had moved there,
  plus the kids, so 200-group runners reached the kids soon (369 and 582
  kid reservations after the restart). c39739ff behaved the same way
  before its kill (557 and 700). After its restart, recovery put
  everything at 200, and it reserved 1 and 2 kids.

So 6.3b changed which jobs share a group with the kids, not the queue
order. Serving the kids before 50,000 older equal-priority jobs was a
side effect of the stale group, not a property of the base. The ordering
is unchanged here. If kid jobs matter more than older work, they need a
higher priority.

No gates were run, because no code changed. The scratch worktrees, the
scratch `GOCACHE` and the NFS probe directory were removed.

- [x] implemented
- [x] reviewed

### Item 6.3d: branch add latency at matched load

Question. Item 6.3's load-matched `add-storm-lsf` A/B had this tree's add
p50 17% to 60% above the base's at equal archive-writer `meanWait`, and
item 6.3c found c39739ff (before 6.3b) about as slow. Which change makes
adds slower, and can it be cut without bringing back the hold?

Verdict: phase 5, the removal of the hold, and the cost comes with it.
No branch code adds work to an add, and none of the per-archive work
changed. The hold made each runner's reserve wait about 0.4 to 0.5s for
the RAC. That kept the single bolt writer below saturation on NFS. Without
the hold, runners take the writer's spare capacity at once, and adds, which
need the same writer, queue behind more archive transactions. Two cuts were
tried in scratch copies. Coalescing archives does nothing. Making the
archive and best-effort writers yield to pending adds helps, but it changes
the order of the archive critical path, so it needs the owner's ruling. No
code was changed on the branch.

Reproduction. `nohold_addstorm_probe_test.go` (`TestNoHoldAddStormProbe`,
not committed) was copied into scratch worktrees of each tree. Each run
copies `fix120k.db` (3.3GB, 120,000 incomplete jobs) to
`/nfs/hgi/wr/sb10-bigdb/devroot`. It starts a real server with backups on
and raises `aslblock` to 30. The mock scheduler's `RunnerFunc` is an
in-process runner: it reserves its scheduler group until nothing is left,
calls `Started`, sleeps 200ms (fixture) or 1s (storm), self-adds a kid for
a `selfadd` job through a new connection, and archives. So the server
decides how many runners each group gets, as LSF does. 24 adders each
connect, add one `sleep` job (`asllimit:30`, 500M, retries 3) and
disconnect, with a 0.5 to 1.5s jittered think time, like `asl_adder`. After
a 20s warm-up the probe measures 60s: client add latency, the archive
writer's fold figures (its report interval set to 1h so the probe owns
them), bolt `TxStats` deltas, the write-transaction count, and whether a
backup copy is in progress. Runs were interleaved, one at a time, on a
shared 8-CPU host at load 1 to 3.7.

Add latency on NFS has two modes: about 5 to 10ms when the writer is free
and 0.2 to 1.5s when it is not. The per-run p50 lands in one mode or the
other, so its percentages overstate the change. The mean is the steadier
figure. Medians over runs, Mann-Whitney U (`stats.py`):

| Comparison (NFS, backups on) | Add p50 | Add mean | Archives/s |
|---|---|---|---|
| base, phase 3, phase 4 (9 runs) against phase 5, c39739ff, 959f3fd0 (9) | 30 against 114ms, p=0.0003 | 180 against 211ms (+17%), p=0.003 | 62.7 against 73.2 (+17%), p=0.007 |
| phase 4 against phase 5 (3 against 3) | 25 against 96ms, p=0.10 | +17%, p=0.20 | +18%, p=0.10 |
| base against 959f3fd0 (20 against 20) | 15.6 against 99.6ms, p<0.0001 | 180 against 222ms (+23%), p=0.0007 | 65.6 against 74.0 (+13%), p=0.066 |
| base against 959f3fd0 with `aslblock:25` (6 against 6) | 14.7 against 72.8ms, p=0.04 | 182 against 240ms (+32%), p=0.065 | 65.2 against 66.2 (+1%) |

Per tree in the 3-round bisect (`ab3-nfs.txt`), add p50: base 70, 16 and
49ms; phase 3 6, 30 and 35ms; phase 4 40, 15 and 25ms; phase 5 114, 96 and
77ms; c39739ff 125, 45 and 138ms; 959f3fd0 110, 116 and 169ms. Phases 1 to
4 (the ready hook, `prepareBeforeQueueing` and the memo included) match
the base. The change comes with phase 5, and 6.3b adds nothing on top.
On local disk (`ab2-local.txt`, 3 rounds) add p50 is 2.0 to 2.9ms on
every tree. 959f3fd0 with `aslblock:20` completes 17% more archives than
the base with a lower add mean (12 to 22 against 21 to 30ms).

Where an add's time goes (profiles of 60s windows, phase 4 against
959f3fd0, `prof/`):

- The block profile puts all of an add's blocked time in
  `storeNewJobStores` waiting for the new-jobs writer's reply (275s and
  298s across about 1,300 adds). No queue, job or cache lock wait shows
  on the add path.
- Add CPU (`handleAdd`) is 0.12s and 0.15s for about 1,300 adds each, about
  0.1ms per add on both trees. The ready hook and the reqstat warm-up do
  not show.
- The mutex profile charges bolt's write lock (`Tx.close`) 50.1s and 56.5s,
  of which the archive writer's transactions hold 46.1s and 51.8s. Its
  best-effort and new-jobs transactions hold 2 to 3s each.

So an add is as fast as the bolt writer lets the new-jobs writer in, and
archive transactions fill that writer.

The per-archive work is the same on both trees, as `ab8-bk.txt` shows for
two rounds. Page allocation is 3,655 and 3,513 KB/s on the base at 76.0
and 71.5 archives/s, against 2,623 and 3,315 KB/s here at 62.3 and 74.4,
about 47KB per archive on both. Spills, splits, rebalances and nodes per
archive are equal too. Writer time per archive (transactions times
`meanTx` over archives) is 8 to 10ms on every tree. What differs is how
the work arrives:

- Reserve p50 in the probe is 380 to 520ms on the base, waiting on the
  hold, and 15 to 40ms here.
- Fold drops without the hold's bursts. On local disk it is 4.1 against
  1.4 archives per transaction, with 4.5 times the archive transactions.
  On NFS, where transactions are longer, it is 5.3 to 7.5 against 4.1 to
  6.6. Non-archive write transactions rise from 25 to 30/s to 34 to 48/s
  (more `Started` best-effort drains).
- The per-second timelines (`tl/`, base against `aslblock:25`) show this
  tree completing 115 to 150 archives per free second against the base's
  80 to 110, at an equal mean. More of its seconds therefore fall in the
  slow mode. Both trees stall for about 1.2s every few seconds, during
  and between backup copies.

Without backups (`ab7-nobk.txt`, and two `ab8-bk.txt` pairs) the trees did
not differ significantly: add mean 166 against 176ms (+6%, p=0.31), p50
121 against 129ms. In those runs the writer was saturated on every tree
(`meanTx` about 95 to 115ms against 35 to 60ms with backups on, as the NFS
slowed down that evening, and possibly because the backup's read keeps the
file's pages cached). So the absence of a gap there may be because neither
tree had spare writer capacity left, not because the backup causes it. With
backups on, adds that complete during a copy (5 to 9% of the window) have
p50 255 to 262ms on the base and 394 to 1,260ms here. Outside a copy it is
5.6 to 7.9ms against 16 to 37ms.

Cuts tried (scratch worktree `nohold-63d-hg`, `hg-gather-addprio.diff`,
both behind env knobs):

1. Coalescing: the archive writer sleeps 20ms and the best-effort writer
   10ms before each drain. Fold rose from 4.6 to 7.2 to about 9, and archive
   transactions halved. Add latency did not change: mean 184 and 177ms
   against 192, 305 and 188ms (`ab9-gather.txt`; its third gather run was
   discarded after an overlapping run overwrote its DB copy). As the
   per-archive figures say, the writer's cost is per archive, not per
   transaction, so folding is not a lever.
2. Add priority: before each transaction, the archive and best-effort
   writers wait (at most 200ms) while adds are pending or their
   transaction is starting. Against 959f3fd0 (`ab11-prio.txt`, 3 rounds):
   add mean 215, 132 and 141ms against 274, 279 and 214ms (-49% on the
   medians, p=0.2), p50 142, 39 and 84ms against 183, 131 and 104ms
   (-36%, p=0.4), with archive p50 326, 47 and 243ms against 362, 41 and
   347ms. The base in the same run had mean 196, 252 and 178ms and p50
   9, 21 and 7ms. So the add mean fell below the base's while archives did
   not get visibly slower. It is not significant at 3 rounds, and it is
   not landed. It reorders the bolt writer for every caller: under a
   real add storm adds are pending almost all the time, so each archive
   transaction could wait up to the cap and slow the archive critical path
   (DEVELOPERS.md section 1). That is a change for the owner to rule on and
   would need `report-storm`, `add-storm` and `add-storm-lsf` evidence. A
   bounded alternation between writers, rather than strict priority, would
   be the safer form.

Per-add and per-archive cost. Server CPU per add is about 0.1ms on both
trees, and per-archive bolt work is the same. The extra add latency is
queueing on the one bolt writer, which this tree keeps busier because its
runners no longer wait about 0.4 to 0.5s per reserve. In-process that is
+23% mean (about +40ms on a 180ms base) with backups on and archive
throughput 13% higher. In the LSF A/B it was +25 to +90ms at p50. The
throughput gain is the hold removal's purpose (spec E1), so the remaining
cost is intrinsic unless the owner wants the writer reordered (cut 2).

No code changed, so no gates were run. Evidence is in
`/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-63d/`: the probe, `ab.sh`,
`sum.py`, `stats.py` and `stats-out.txt`, every run's output
(`ab*.txt`), `tl/`, `prof/` (`.pprof` files and text tops), the cut
diff, and `fuseslow.diff`. That diff added a fixed fsync delay to
`fusestall`, tried once and dropped, because writes through FUSE
saturated the writer (`ab4-fuse-try.txt`). The scratch worktrees, the
scratch `GOCACHE` and the NFS probe directory were removed.

- [x] implemented
- [x] reviewed

### Item 6.3e: adds wait behind archive writes (owner decision 2026-10-19)

Item 6.3d found the slower `wr add` on NFS comes from phase 5: without the
hold archives complete about 13 to 17% faster (6.3d's probe comparisons),
the single bolt writer saturates
more often, and `storeNewJobStores` commits queue behind archive
transactions. The owner chose to fix this rather than accept it (see
prompt.md Notes).

Implement bounded alternation between the bolt writers (jobqueue db.go
new-jobs writer, archive writer, best-effort writer) so a pending add batch
gets the write lock within a bounded wait, and archives in turn are never
deferred more than a bounded amount under a sustained add storm. The
scratch prototype (add-priority with a 200ms cap) is in
/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-63d/hg-gather-addprio.diff;
use it as a starting point only, replacing the unbounded-priority form with
fair alternation.

Requirements:

- A red-first test showing an add waits behind a stream of archive
  transactions (deterministic, using seams, not timing alone), and a test
  that archives are not starved by a continuous add stream.
- The in-process probe (nohold-63d/nohold_addstorm_probe_test.go, ab.sh)
  on NFS with backups on: add mean and p50 at or below the base, archive
  p50/p99 and archives/s not worse than HEAD (959f3fd0) by more than 10%,
  with enough interleaved rounds to be significant (at least 9 each).
- `make test`, `make race` (with OS_* unset) and lint pass; benchmarks for
  the touched writer path.
- CHANGELOG Fixed line if user-visible.
- Then rerun report-storm-lsf, add-storm-lsf and limit-drain on LSF against
  the base, and record results here. Pass: nothing more than 10% worse than
  the base except costs the owner already accepted.

Results (2026-10-19, uncommitted on the branch, LSF scenarios not yet run).
Superseded: the change is committed as f04eaa3e, and the LSF scenarios'
results are in the LSF section below.

Design. `jobqueue/writeturns.go` adds `writeTurns`, which the three
coalescing writers take before each of their bolt write transactions
(`db.updateInTurn`, used by `newJobsTx`, `archiveTx` and
`drainBestEffort`). When a turn ends, the new jobs writer goes next if it is
waiting; otherwise the writer that has waited longest goes. Each writer is a
single goroutine, so the new jobs writer is never waiting when its own turn
ends, and the order alternates by construction, with no timer and no cap to
tune:

- a waiting add commits in the transaction right after the one in flight;
- the archive and best-effort writers each wait behind at most three other
  transactions (the one in flight, the other's and one add's), the same
  bound bolt's first-come mutex gives them today.

The prototype's form (archives wait while adds are pending, up to 200ms) was
not used: under an add storm adds are always pending, so every archive would
wait the full cap. Other bolt write transactions (RAC, deletes, compaction)
take no turn and still queue on bolt's lock. Archive `meanLock` in the fold
summary now includes the turn wait; it rose from 2.9 to 4.4ms to 3.5 to
5.4ms per transaction in the probe.

Tests (`jobqueue/nohold_write_turns_test.go`, `TestNoHoldWriteTurns`):

- An add commits in the transaction right after the archive in flight,
  ahead of a best-effort write and an archive queued before it. The archive
  is held inside its transaction by the `archiveTxObserver` seam; order is
  read from bolt transaction ids. RED on 959f3fd0 in 5 of 5 runs (the add
  committed 2 transactions later, behind the best-effort write and the second
  archive), GREEN after.
- An archive that arrives while 8 adds (one transaction each) are queued
  behind an add in flight commits after at most one more add transaction. It
  passes on 959f3fd0 (bolt's mutex is first-come) and fails with 8 adds
  ahead against a mutant that defers archives while adds are pending (the
  prototype's form without its cap). A mutant that ignores the add
  preference (longest waiting first) fails the first test.

In-process probe A/B (the 6.3d probe, `fix120k.db` on NFS, backups on), 10
interleaved rounds per tree with the order rotated each round, base
6f1b41bb, HEAD 959f3fd0, and HEAD plus this change ("turns"). Medians over
runs, two-sided Mann-Whitney U (`stats.py`, normal approximation; a
permutation p agrees). Latencies in ms:

| Metric | base | HEAD | turns | turns against base | turns against HEAD |
|---|---|---|---|---|---|
| add mean | 157.3 | 190.9 | 134.1 | -15%, p=0.04 | -30%, p=0.0002 |
| add p50 | 16.4 | 99.0 | 67.3 | +311%, p=0.0003 | -32%, p=0.0004 |
| add p99 | 1,024 | 1,060 | 960 | -6%, p=0.34 | -9%, p=0.10 |
| archive p50 | 19.6 | 305.8 | 298.8 | +1,423%, p=0.0002 | -2%, p=0.38 |
| archive p99 | 1,000 | 1,018 | 1,148 | +15%, p=0.16 | +13%, p=0.43 |
| archives/s | 67.6 | 74.5 | 74.4 | +10%, p=0.016 | 0%, p=0.76 |

Against the criteria:

- Add mean at or below the base: met (-15%, p=0.04).
- Add p50 at or below the base: not met (67 against 16ms). Owner accepted
  2026-10-19.
- Archive p50 and archives/s within 10% of HEAD: met.
- Archive p99 within 10% of HEAD: +13% on the medians, not significant
  (p=0.43). The per-run values overlap fully (HEAD 781 to 1,388, turns 770
  to 1,387).
- Archive latency against the base (not a criterion): p50 298.8 against
  19.6ms (+1,423%, p=0.0002), p99 +15% (p=0.16). The p50 rise is writer
  occupancy from the hold removal, not write turns: HEAD 959f3fd0's p50 is
  the same (305.8ms). At LSF scale the archive `meanWait` is only +8%
  against the base (LSF section below). The owner accepted the archive
  median on 2026-10-19 (prompt.md Notes, item 6.3 record). The p99 rise is
  not significant (p=0.16), and it is not writer occupancy: HEAD is +2%
  against the base (p=0.97). If it is real, it comes with write turns
  (+13% against HEAD, p=0.43). No acceptance covers it.

Why no ordering can bring the add p50 back to the base's. The base's add p50
(5 to 55ms per run) is the "writer free" mode 6.3d described: an add finds
no transaction in flight. The time bolt spends writing per second
(`boltWriteTime/s`) is 440 to 652ms on the base, 704 to 759ms on HEAD and
698 to 749ms with turns, because without the hold the same writer completes
10% more archives (6.3d: per-archive write cost is equal on every tree). At
that load a typical add finds a transaction in flight, so even going next it
waits for that transaction's remainder and then its own (`meanTx` 40 to
80ms), which is the 51 to 92ms per-run p50 seen. Reordering cannot create
idle time; only less write work per second can, which means fewer archives
per second, and 6.3d's gather cut (delaying archive drains) did not change
add latency at all. Meeting the p50 criterion would need either giving back
the hold removal's throughput gain or cheaper commits, neither of which is
an ordering change.

Benchmarks (`jobqueue/nohold_write_turns_bench_test.go`, local disk, 6
interleaved runs each, HEAD against turns, medians): the new
`BenchmarkAddDuringWriterStreams` (single-job adds while archives arrive
every 4ms with a 20ms commit cost and best-effort writes every 2ms) went
from 22.9 to 11.9ms per add (-48%), with archives/s 246.9 against 243.6
(-1.4%). The existing writer benchmarks are unchanged within 1%:
`BenchmarkAddJobs` -0.9%, `BenchmarkArchiveJobs` -0.7%,
`BenchmarkArchiveSpacedArrivals` 0.0% (same transactions per job),
`BenchmarkUpdateJobState` -0.4%.

Gates: `make lint` 0 issues. `make test` and `make race` (OS_* unset)
pass (1,003 passed; `make-test.log`, `make-race.log` in the evidence
directory), and the writer tests (`TestNoHoldWriteTurns`, `TestReliable4Add*`,
`TestReliable4Archive*`, `TestReliable4BestEffort*`) pass 3 times under
`-race`.

No CHANGELOG line: against the released base, adds are faster on mean and
tail but slower at the median, and the branch's existing performance entry
already covers the hold removal. Add one if the owner reads the mean as the
user-visible figure.

Not done at the time: report-storm-lsf, add-storm-lsf and limit-drain on LSF
(left for the owner after review). Superseded: they have since run on f04eaa3e;
see the LSF section below. The host was shared with another session's test runs
during the A/B; the rotating interleave spreads that load across trees.

Evidence in `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-63e/`: `ab1.txt`
(every probe run), `ab.sh`, `stats.py` and `stats-out.txt`, `sum.py` and
`sum-out.txt` (writer figures per run), the probe source, `bench.txt`,
`bench.sh`, `benchsum.py`, `bench-summary.txt`, and `turns.diff` (the change
as measured; the branch differs only in one comment).

LSF scenarios (2026-10-19; farm22-wrstat01, queue `normal`, `OS_*` unset).
The branch is f04eaa3e (this item, committed). The base is 6f1b41bb.
959f3fd0 ("pre-6.3e", HEAD before this item) ran in the same session as a
third tree, so the change can be compared with the branch before it under
the same load. Every base figure is new: the base runs of 2026-10-16 were
made under different NFS load (6.3c), so none were reused.

Setup, as in item 6.3:

- Each tree had its own `WRDEV_ROOT` and binary, built from a clean scratch
  detached worktree with a scratch `GOCACHE`, before any run. `go version
  -m` shows `vcs.revision` 6f1b41bb, f04eaa3e and 959f3fd0, each with
  `vcs.modified=false` (`*-wr.buildinfo.txt`). Ports 52610 to 52613,
  `WRDEV_DEBUG=1`, scenario defaults, storm modes on copies of
  `fix120k.db` and `pristine10`. Runs went one at a time. Before each run the
  driver waited until `bjobs` listed no job of mine, and each `.out` saves
  the `bjobs -w` taken just before the run: "No unfinished job found" all
  23 times.
- Order. `add-storm-lsf`: base, branch, base, branch, base, branch, then
  branch, base, pre-6.3e, pre-6.3e, branch, base, base, pre-6.3e, branch
  (12:15 to 13:37). `report-storm-lsf`: branch, base, pre-6.3e, pre-6.3e,
  base, branch (13:37 to 14:03). `limit-drain`: base, then branch (14:03 to
  14:36).
- The host was shared. Other sessions' builds and browser tests drove the
  load average from about 5 to 34 during `add-storm-lsf` runs 2 and 3. Each
  run's start load is in its `.out` and in the table below.
- Measurement is the same as in item 6.3's load-matched A/B. Add
  percentiles cover every acknowledged add in the adder logs. Mean is over
  the adds under 5s, which leaves out the few kill-window adds (about 22s)
  that land in some runs and not others. `meanWait`, `meanLock` and `meanTx`
  are means of the run's `archive fold` lines. Archives per minute is the
  mean `archives=` of those lines. p values are exact two-sided permutation
  tests of Mann-Whitney U (`stats.py`).

`add-storm-lsf`: all 15 runs printed `PASS: all <n> acknowledged adds`, with
`missingAcked=0`, `overSlow=0`, `mgrSlowAdds=0`, outage 21 to 27s, and
`lost contact` 0 in every side sample. Wall is the `wrdev.sh` run's
`wall=` (316 to 328s); the run length is fixed (120s storm, a kill, 120s
more), so wall time cannot show throughput.

| Run | Tree | Load | Wall | Acked | Completed | Add p50 / p90 / p99 | Mean | meanWait | meanLock | Archives/min | Storm done at kill | Kids | Fixture at 600, lifetime 1 / 2 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | base | 6.8 | 323s | 4,640 | 17,150 | 217 / 761 / 1,201 | 311 | 98 | 4.2 | 3,802 | 1,907 | 521 of 547 | 51% / 27% |
| 2 | branch | 7.8 | 328s | 4,586 | 20,082 | 236 / 478 / 1,040 | 279 | 78 | 10.0 | 4,421 | 1,579 | 94 of 695 | 2% / 0% |
| 3 | base | 32.2 | 321s | 4,617 | 20,526 | 172 / 592 / 1,081 | 255 | 59 | 5.2 | 4,574 | 2,218 | 669 of 670 | 56% / 29% |
| 4 | branch | 14.1 | 322s | 4,838 | 22,933 | 223 / 521 / 898 | 261 | 103 | 5.5 | 5,034 | 1,883 | 113 of 788 | 1% / 0% |
| 5 | base | 5.5 | 318s | 4,835 | 17,445 | 184 / 644 / 1,079 | 267 | 82 | 3.2 | 3,900 | 1,911 | 552 of 554 | 55% / 28% |
| 6 | branch | 6.3 | 318s | 4,962 | 22,503 | 172 / 524 / 916 | 233 | 90 | 4.2 | 4,947 | 1,470 | 107 of 789 | 2% / 0% |
| 7 | branch | 3.0 | 317s | 4,861 | 22,686 | 206 / 526 / 913 | 253 | 100 | 4.0 | 5,089 | 2,023 | 111 of 773 | 1% / 0% |
| 8 | base | 4.4 | 318s | 4,729 | 16,904 | 184 / 696 / 1,202 | 283 | 91 | 3.5 | 3,808 | 1,820 | 512 of 535 | 47% / 25% |
| 9 | pre-6.3e | 6.8 | 321s | 4,852 | 35,997 | 129 / 639 / 1,071 | 248 | 57 | 8.5 | 7,594 | 2,340 | 434 of 1,292 | 0% / 0% |
| 10 | pre-6.3e | 5.8 | 318s | 4,415 | 24,579 | 331 / 800 / 1,187 | 378 | 94 | 4.0 | 5,585 | 2,046 | 145 of 855 | 0% / 0% |
| 11 | branch | 3.7 | 317s | 4,767 | 27,075 | 228 / 640 / 990 | 281 | 90 | 3.2 | 6,056 | 1,896 | 190 of 965 | 1% / 0% |
| 12 | base | 4.4 | 317s | 4,760 | 16,704 | 183 / 695 / 1,169 | 284 | 81 | 2.5 | 3,724 | 1,841 | 516 of 518 | 53% / 22% |
| 13 | base | 5.3 | 319s | 4,677 | 17,374 | 215 / 716 / 1,180 | 301 | 95 | 3.2 | 3,925 | 1,988 | 511 of 545 | 44% / 24% |
| 14 | pre-6.3e | 4.4 | 316s | 4,534 | 23,731 | 293 / 699 / 1,153 | 341 | 104 | 3.2 | 5,323 | 2,044 | 121 of 826 | 0% / 0% |
| 15 | branch | 5.4 | 316s | 4,631 | 27,395 | 234 / 716 / 1,084 | 305 | 97 | 3.2 | 6,230 | 1,786 | 195 of 989 | 1% / 0% |

Latencies and `meanWait`/`meanLock` in ms. Medians (base and branch 6 runs
each, pre-6.3e 3):

| Metric | base | branch | pre-6.3e | branch against base | branch against pre-6.3e |
|---|---|---|---|---|---|
| add p50 | 184 | 226 | 293 | +23%, p=0.11 | -23%, p=0.55 |
| add p90 | 696 | 525 | 699 | -25%, p=0.04 | -25%, p=0.17 |
| add p99 | 1,175 | 953 | 1,153 | -19%, p=0.009 | -17%, p=0.048 |
| add mean | 284 | 270 | 341 | -5%, p=0.24 | -21%, p=0.38 |
| jobs completed | 17,262 | 22,810 | 24,579 | +32%, p=0.004 | -7%, p=0.26 |
| archives/min | 3,854 | 5,062 | 5,585 | +31%, p=0.004 | -9%, p=0.26 |
| archive `meanWait` | 86 | 94 | 94 | +8%, p=0.39 | -1%, p=1.0 |
| archive `meanLock` | 3.35 | 4.1 | 4.0 | +22%, p=0.33 | +2%, p=0.95 |
| archive `meanTx` | 30.6 | 26.4 | 27.0 | -14%, p=0.31 | -2%, p=1.0 |
| storm done at kill | 1,909 | 1,835 | 2,046 | -4%, p=0.18 | -10%, p=0.024 |
| storm done | 4,301 | 4,334 | 4,534 | +1%, p=0.82 | -4%, p=0.26 |
| kids done | 519 | 112 | 145 | -78%, p=0.002 | -23%, p=0.26 |
| `maxStatusRPC` | 263 | 198 | 185 | -25%, p=0.06 | +7%, p=0.55 |

Against the pass criterion (nothing more than 10% worse than the base,
except accepted costs):

- Add p50, +23%, not significant (p=0.11): the cost the owner accepted for
  6.3e, likely the same writer-occupancy cost as the accepted in-process gap
  (67 against 16ms), with archives/min +31% (p=0.004). It is at the
  low end of item 6.3's load-matched gap (+17% to +60% at 959f3fd0), and in
  this session 959f3fd0's p50 (293ms) was above both. p90 and p99 are now
  better than the base (-25%, p=0.04, and -19%, p=0.009), and the mean is 5%
  below it (p=0.24). Against 959f3fd0, p90 fell 25% (p=0.17), p99 17%
  (p=0.048) and the mean 21% (p=0.38). Only p99 is significant, from 3
  959f3fd0 runs, and one of those (run 9) is an outlier: 35,997 jobs
  completed against 23,731 and 24,579, with add p50 129ms. So the cut
  against 959f3fd0 is likely but not established on LSF; the in-process
  probe above, with 10 rounds, is the firm evidence for it.
- Archive `meanLock`, +22% (4.1 against 3.35ms, p=0.33). Not significant. It
  is a per-transaction figure, about 0.75ms. No rise from write turns shows
  at LSF scale: 4.1 against 959f3fd0's 4.0ms (runs of 8.5, 4.0 and 3.2ms,
  p=0.95), though 3 runs cannot rule one out. In-process, `meanLock` rose
  with turns (HEAD 2.9 to 4.4ms, turns 3.5 to 5.4ms, base 2.3 to 4.3ms),
  partly because since 6.3e it includes the wait for the write turn as well
  as for bolt's lock (`archivefold.go`; see the design above). The hold
  removal and write turns are both possible causes of the +22% against the
  base; neither is shown. Run 2's 10.0ms
  coincided with the load-34 spike. `meanWait`, an archive's whole wait, is
  +8% (p=0.39), and each minute the branch archives 31% more. No owner
  acceptance names `meanLock`. The nearest is the 2026-10-19 acceptance of
  archive latency from writer occupancy, which cites this `meanWait` (+8%)
  and of which `meanLock` is a part, so it is not explicitly accepted, and
  its cause is not established; it is not significant (p=0.33).
- Kids done, -78% (p=0.002): the ordering 6.3b introduced and 6.3c traced.
  Kids queue behind the older fixture jobs in the shared learned group. It
  is not changed by this item: 959f3fd0 completed 121 to 434 kids, about as
  many. The owner accepted this cost on 2026-10-19 (prompt.md Notes, item
  6.3 record).
- Storm jobs done at the kill, -4% against the base (p=0.18). Two branch runs
  were low. In run 6, the first runner reserved 33s after the first add, against
  1 to 4s in the other 14 runs. The status samples show `LSF_RUN=0 LSF_PEND=0`
  until then, so no runner had been submitted or seen by LSF yet; item 6.3f
  finds this most likely in LSF's submit path. Run 2 had the 18s pause described
  next (see item 6.3f: host stalls).

Against 959f3fd0 the same field is -10% (1,835 against 2,046, p=0.024,
`stats-out.txt`), which this criterion does not judge but which needs
saying. It is not fewer adds: `ackedPreKill` is 2,432 against 2,286 (+6%).
Without runs 2 and 6 the branch's median is 1,890, -8% against 959f3fd0 and
-1% against the base, and its 4 remaining runs completed 77 to 82% of their
pre-kill adds by the kill against 89 to 91% for 959f3fd0's 3 (the base's 6:
75 to 90%). All 4 below all 3 has an exact two-sided p of 2/35 = 0.057. So
about 8% fewer storm jobs finish by the kill than on 959f3fd0, at no cost
against the base. The cause was not found. One untested candidate is the
write-turn wait on each storm job's start and archive commits. If the
`asllimit:30` cap is what bounds them, 8% fewer completions in the same 120s
means each job took about 0.15s longer from reserve to archive.

Other fields, medians from each run's VERDICT line, branch against base:

- `acked`, `ackedPreKill`, `notAcked`, `adds` and `stormComplete` are
  within 6%.
- `inFlightAtKill` 2.5 against 0.5 and `ackedInKillWindow` 11.5 against
  6.5: higher, but these count luck at the kill instant, and the per-run
  ranges overlap (0 to 5 against 0 to 9; 7 to 18 against 4 to 15).
- `maxLSFRun` 85.5 against 104.5 (-18%) and `maxLSFPend` 31.5 against 56
  (ranges 0 to 89 against 16 to 106): fewer LSF runners and fewer pending,
  while completing 32% more jobs. Lower is not worse here.
- `fixReadyNow` 41,814 against 47,664 (-12%): fewer fixture jobs left ready
  at the end because more ran (`fixComplete` 18,174 against 12,333, +47%).
- `kidJobs` 789 against 546 (+44%): more `selfadd` fixture jobs ran, so
  more kids were added.
- Safety fields: `missingAcked`, `neverAddedFound` and `*Buried` are 0 on
  every run. `unackedInDb` is 0 on every run except branch run 15, where it
  is 1: of 447 adds the client was not told about, the manager has 1. The
  scenario counts this for the reader (a reply lost after the commit leaves
  the job in the database) and it is not a failure: `missingAcked=0` and
  the run printed `PASS`.

The 600/200 split is the same as before 6.3e. The branch ran 1 to 2% of its
lifetime-1 fixture reservations at the unlearned 600 and none after the
restart. 959f3fd0 ran none at 600 in either lifetime. The base ran 44 to 56%
at 600 before the kill and 22 to 29% after. Kid reservations were all at 200
on every tree.

Two observations on the nohold trees, neither from this item:

- Pauses with nothing reserved or completed. In branch run 2 (12:22:30 to
  12:22:47) no job was reserved or completed for 18s, while adds kept being
  acknowledged at about 20 per second. Both limit groups were at or near their
  30 (the RAC's lines), so about 60 running jobs, each an `echo` or a `sleep 1`,
  reported nothing for 18s. That minute's `archive fold` line has `maxWait`
  1.34s and `maxTx` 1.35s, and `maxTx` includes the write-turn wait. So no
  archive waited longer than that in the writer or for its turn. The stall was
  upstream: in the runners, or in the RPCs before they reach the writer.
  `report-storm-lsf` branch run 1 has the same signature at its drain tail
  (below). Neither appears in the 959f3fd0 or base runs here, apart from 6 to
  12s pauses straight after the first wave of reservations (item 6.3f later
  found a 5s one on f04eaa3e too): pre-6.3e add-storm run 9 (6s), pre-6.3e
  report-storm run 4 (6s), 959f3fd0's 2026-10-16 A/B run 6 (`nohold-g2-ab`, 7s)
  and its 2026-10-16 `report-storm-lsf` rerun (`nohold-g2-rerun`, 12s). The base
  runs, here and on 2026-10-16, have none (`idle.sh` over every storm-mode
  manager log; `limit-drain` logs have idle windows by design). Within this
  session, 2 of 8 f04eaa3e runs against 0 of 5 for 959f3fd0 cannot tell whether
  write turns are involved, and r2's writer figures argue against it. The cause
  was not found; a goroutine dump or runner-side logs taken during a pause would
  be needed. See item 6.3f: host stalls, in which every runner on one exec host
  stopped.
- Released fixture jobs at the restart. 5 `released job` lines: 2 in branch run
  4, 1 in run 7 and 2 in pre-6.3e run 14. Each is a `selfadd` fixture job
  reserved in the second before the kill. It was released within a second of the
  restart, then reserved again and completed at once, so its command ran twice.
  Its first run was in progress at the kill (the scenario's `inFlightAtKill`
  luck, as in item 6.3), so this is retry behaviour, not churn. (Corrected in
  item 6.3f: the command's `wr add` ran, and failed, during the outage; it was
  not in flight at the kill.) No line mentions `lost`, `bad job` or a
  confirm-dead kill in any run.

`report-storm-lsf`: all 6 runs printed `FULLY DRAINED (complete=100000
buried=0)`, exited 0, and had VERDICT `badjobDelta=0 maxDelayed=0 maxLost=0`
and confirmed dead 0.

| Run | Tree | Load | Wall | Drain | maxStatusRPC (median) | meanWait | meanLock | meanTx | Archives per fold line | Longest GAP |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | branch | 3.4 | 297s | 225s | 432 (268)ms | 437ms | 28ms | 175ms | 10,241, 33,574, 33,148, 23,030 | 22s |
| 2 | base | 2.8 | 261s | 210s | 982 (439)ms | 381ms | 29ms | 235ms | 3,091, 31,980, 35,775, 29,154 | 25s |
| 3 | pre-6.3e | 3.6 | 231s | 179s | 583 (335)ms | 389ms | 24ms | 190ms | 12,512, 36,088, 35,568 | 21s |
| 4 | pre-6.3e | 3.9 | 260s | 187s | 461 (319)ms | 411ms | 23ms | 174ms | 14,132, 33,401, 34,826 | 20s |
| 5 | base | 3.2 | 233s | 185s | 713 (377)ms | 367ms | 25ms | 199ms | 11,481, 35,084, 35,277 | 21s |
| 6 | branch | 9.1 | 231s | 175s | 770 (375)ms | 354ms | 25ms | 168ms | 22,254, 31,122, 35,587 | 21s |

Branch against base, medians: drain 200 against 198s (+1%), wall 264
against 247s (+7%), `meanWait` +6%, `meanLock` -2%, `meanTx` -21%,
`maxStatusRPC` 601 against 848ms. Adjacent pairs: drain +7% (runs 1 and 2)
and -5% (runs 5 and 6). Nothing is more than 10% worse.

Run 1's drain includes a 27s pause at its tail. Pause lengths here count
the whole seconds with no reservation or completion, as `idle.sh` does: the
last completion before it was at 13:41:19 and the next at 13:41:47, so the
seconds 13:41:20 to 13:41:46 were idle. (The add-storm pause's 18s counts
the same way.) Its last 7 jobs (`sleep 1`) were running at 13:41:19, and at
13:41:47 all 7 completed together. It took 30s from the 99,990th
completion to the last, against 0 to 4s in the other 5 runs. This is the
pause signature described under `add-storm-lsf`. Its stuck archives fall in
an interval whose `archive fold` line was never written (the manager was
stopped first), so the writer figures cannot rule the writer in or out here.
Without the pause, run 1's drain would be about 197s. See item 6.3f: host
stalls. The run's 22s GAP
(13:41:19 to 13:41:41) is this pause. Every other GAP (at most 25s) comes at
the 100,000-job add or before the first runner, as in item 6.3. None is
over 60s. Run 1's wall is 36s longer than run 2's. Drain accounts for 15s
of that; the rest is the 4.3GB DB copy and startup before the first add.

Against 959f3fd0, the branch's median drain is 200 against 183s (+9%),
from two runs each. Without run 1's pause it is about 186s. The `archive fold`
lines are about the same.

`limit-drain`: both runs printed `FULLY DRAINED (complete=60000 buried=0)`
with lost, bad job, confirmed dead and archive rejects all 0. Each manager
was stopped with `wrdev.sh stop` (verified pid, its own namespaced jobs;
`*.stop.out`).

| Run | Tree | Load | Wall | Drain | Status RPC median / max | meanWait | meanLock |
|---|---|---|---|---|---|---|---|
| 1 | base | 4.2 | 978s | 943s | 152 / 1,234ms | 48ms | 2ms |
| 2 | branch | 1.8 | 969s | 936s | 118 / 162ms | 16ms | 2ms |

Nothing is worse. 959f3fd0's limit-drain on 2026-10-16 (948s drain, status
RPC median 118ms) matches.

Verdict: every scenario passes on f04eaa3e by its own criteria. Against the
base, the figures more than 10% worse are add p50 (+23%, owner-accepted for
6.3e), the in-process probe's add p50 (+311%, 67 against 16ms, owner-accepted
for 6.3e), kids done (-78%, owner-accepted kid ordering, 2026-10-19), the
in-process probe's archive p50 (+1,423%, owner-accepted archive latency,
2026-10-19), and two that no acceptance names: LSF archive `meanLock` (+22%,
p=0.33, cause not established; it now includes the write-turn wait) and the
in-process archive p99 (+15%, p=0.16, which if real comes with write turns,
since 959f3fd0 is +2%). Neither of those two is significant. Against 959f3fd0,
add p90, p99 and the mean were 17 to 25% lower under the same load (only p99
significant, p=0.048, from 3 runs), with archive throughput and `meanWait`
unchanged within noise, and storm jobs done at the kill 10% lower (p=0.024,
about 8% without runs 2 and 6, cause not found).

Open: the 18s and 27s pauses in 2 of the 8 f04eaa3e runs (`add-storm-lsf` run 2,
`report-storm-lsf` run 1). No verdict field failed, but the second lengthened
its drain from about 197s to 225s. The one with writer figures shows no archive
waiting over 1.35s in the writer or for its turn. No pause like these has been
seen on the base or, beyond the 6 to 12s first-wave pauses, on 959f3fd0 (item
6.3f found a 5s first-wave pause on f04eaa3e). The cause is unknown here. See
item 6.3f: host stalls, in which every runner on one exec host stopped.

Evidence in `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-63e-lsf/`:

- Scripts: `g2.sh` (one run, from `nohold-g2-ab/g2ab.sh`, plus `wrdev.sh
  stop` after `limit-drain`) and `drive.sh`, with the drive logs.
- Per run: `.out`, gzipped manager log and, for `add-storm-lsf`, its
  adder logs and lost samples.
- Analysis: `collect.sh` makes `addstorm-runs.tsv`; `stats.py` makes
  `stats-out.txt`; `abstats.sh` makes `ab-summary.txt`; `rsstats.sh`
  makes `rs-summary.txt` and `ld-summary.txt`. Also `split.sh`,
  `idle.sh`, `stormbacklog.sh` and `lsf-after-all.txt`.

Nothing is left running: the final `bjobs -w` is empty and no manager
from these roots is alive. The scratch worktrees were removed, the scratch
`GOCACHE` was emptied with `GOCACHE=<dir> go clean -cache`, and the work
roots (binaries, DB copies) were removed.

- [x] implemented
- [x] reviewed

### Item 6.3f: reserve pauses in the 6.3e LSF runs

Item 6.3e's LSF reruns left two pauses unexplained on f04eaa3e: 18s with nothing
reserved or completed in `add-storm-lsf` run 2 (12:22:30 to 12:22:47), and 27s
at the drain tail of `report-storm-lsf` run 1 (13:41:20 to 13:41:46, between a
completion at 13:41:19 and the next at 13:41:47). Both count whole idle seconds,
as `idle.sh` does. They also left 2 smaller questions: run 6's first reservation
came 33s after the first add, and 5 `selfadd` fixture jobs were released at the
restart.

Verdict: neither pause comes from write turns, or from the branch. In each
one, the runners that stopped were exactly the runners on one exec host. In
`report-storm-lsf` run 1, runners on the other 75 hosts were not affected,
so the host evidence alone separates a host stall from a manager stall. In
`add-storm-lsf` run 2 every runner alive across the pause was on that one
host, so the host evidence cannot tell the two apart; there the conclusion
rests on the manager-side evidence: adds were still acknowledged, the
archive writer's worst wait was 1.35s, and no request was slow.

Evidence from LSF accounting (`bacct -l`; `hoststall.sh` makes
`hoststall.txt` from the saved `bacct_*.txt.gz`):

- `report-storm-lsf` run 1. 2,000 runners finished between 13:41 and
  13:43. All 108 runners on node-13-16 ended at 13:41:30 or later (55 at
  13:41:55 to 13:41:58, the other 53 at 13:42:02 to 13:42:03), and none of
  the 1,892 runners on the other 75 hosts did. The 7 `sleep 1` jobs reserved
  at 13:41:14 and completed together at 13:41:47 were on node-13-16. 674
  jobs were reserved in that second; the other 667 completed by 13:41:17.
- `add-storm-lsf` run 2. All 57 runners that were alive across the pause
  (dispatched before 12:22:29, finished after 12:22:47) ran on one host,
  node-13-22. LSF had placed the whole fleet there, so the whole fleet
  stopped together. That accounts for 18s with nothing reserved or
  completed while adds, which come from the manager's own host, were still
  acknowledged (346 `added jobs` lines in the pause). With no runner on
  another host, this run alone cannot show that a manager stall would have
  spared other hosts; the manager-side bullet below is what rules the
  manager out.
- The runners on the stalled hosts spent no time in PSUSP, USUSP, SSUSP or
  UNKWN. The first check (`bhist -a -u sb10` over the last 6 event logs)
  was not saved. It was re-run afterwards for the 4 runner arrays involved
  (952017 for report-storm run 1; 940147, 940148 and 940150 for add-storm
  run 2) and saved as `bhist-stallhosts.txt`: all 1,058 sb10 array
  elements show 0s in each of those states. So LSF did not suspend them;
  the host itself stalled.
- Manager side, none of the 6.3e logs has a `slow request` warning, which
  fires once a handler has run 10s past any wait its client asked for. In
  run 2, the `archive fold` line covering the pause has `maxWait` 1.34s and
  `maxTx` 1.35s. A start waits for its best-effort commit with no bound, so
  a stalled writer would have shown as slow `jstart` requests. None did, so
  the inference is that the runners on the stalled host sent nothing; no
  runner-side log shows it directly.

Repro. 8 more runs on f04eaa3e (4 `add-storm-lsf`, 4 `report-storm-lsf`,
alternating, 14:51 to 15:32, `g3.sh`/`drive3.sh`). Each was the 6.3e
method plus `WR_PPROF_ADDR` and `watch.sh`. The watcher took a goroutine
dump (pprof `debug=2`) every 2s and a `bjobs` snapshot every 4s whenever
reserve/complete progress stalled for 3s or more. All 8 passed by their own
verdicts (`PASS`; `FULLY DRAINED (complete=100000 buried=0)`). None had a
mid-run pause: the longest gaps, apart from the kill window and the
first-wave pause below, were 3s, and every report-storm drain tail
(99,990th to last completion) was 0 to 5s. Across every saved storm run
(`nohold-g2`, `nohold-g2-rerun`, `nohold-g2-ab`, `nohold-63e-lsf` and these
8), `idle.sh` finds a mid-run pause of 5s or more in 2 of 16 f04eaa3e runs,
0 of 11 959f3fd0 runs and 0 of 14 base runs (c39739ff's 3 left out). A count
this small cannot tell the trees apart (p = C(16,2)/C(41,2) = 0.15 that both
pauses fall in f04eaa3e's 16 of the 41 runs by chance), and the host
evidence above does not depend on the tree.

The 5 to 12s pauses straight after the first wave of reservations come from item
6.3b, not from 6.3e. `idle.sh` finds 5 of them in the 27 post-6.3b storm runs
(11 on 959f3fd0, 16 on f04eaa3e): 959f3fd0 add-storm run 9 (6s) and report-storm
run 4 (6s) in `nohold-63e-lsf`, 959f3fd0's 2026-10-16 A/B run 6 (`nohold-g2-ab`,
7s) and `report-storm-lsf` rerun (`nohold-g2-rerun`, 12s), and add-storm run 1
here (5s). The jobs learn their size from the first completions, their ready
jobs move from the unlearned scheduler groups to the learned 200MB one, and the
RAC drops the unlearned groups. In `add-storm-lsf` those are the 600MB
`aslblock` and `asllimit` groups (`rac unscheduling uneeded group` at 14:52:25
and 14:52:27 in run 1, and the same 2 lines just before each add-storm pause
above). In `report-storm-lsf` they are the `reprolimit` groups at 600 to
1,490MB: 88 are unscheduled at 13:51:34 in pre-6.3e run 4, and 2 more at
13:51:35. The runners of a dropped group find nothing left in it and exit.
Reservations stop until the 200MB runners connect: in run 1 here they restart at
14:52:32. (During the rerun's 12s, a `bsub` for one of the dropped groups also
failed, with `failed to find the submitted jobs in bjobs`.) The goroutine dump
taken at 14:52:30 (`add-storm-lsf-branch.r1.watch/stall_g_1792417950.txt`) shows
1 connected client pipe and no handler in flight, against 61 pipes in the dump
taken 6s earlier. Nothing was waiting in the manager. The base keeps the
unlearned size on ready jobs, and so did the branch before 6.3b, so neither
drops its unlearned groups in one burst like this (the base unschedules groups
too, but spread out: at most 5 in any second in 6.3e's base add-storm run 1 and
report-storm run 2): no base run and no pre-6.3b branch run (c39739ff,
`nohold-g2`) has a pause of 5s or more. This is a first-wave cost of 6.3b's
design, about one runner start-up.

Smaller questions:

- Run 6's 33s to its first reservation. The RAC asked for 30 runners at
  12:43:17. LSF records the first array as submitted at 12:43:49 and dispatched
  at 12:43:50 (`bacct_as6.txt.gz`). `bacct`'s Submitted time is when mbatchd
  accepted the job, and the manager log has no `bsub` line, so the 32s went
  between the RAC's request and mbatchd's acceptance. That time is most
  likely LSF's submit path: `jobqueue/scheduler`, `cmd/runner.go`,
  `jobqueue/client.go` and `internal/` are unchanged between the base and
  f04eaa3e. But the logs do not show when `bsub` ran, so wr's time before
  the call cannot be separated from LSF's.
- The 5 `released job` lines for `selfadd` jobs. Each job was reserved in the
  second before the kill. Its command runs `wr add` against the manager
  (`wradd.sh`). An add started while the manager is down fails within about 30ms
  with `jobqueue Connect(): could not reach the server` and exits 1; the storm
  adders log exactly this during the outage (run 4's `adder_*.log.err`). The
  `selfadd` command's own output was not kept, so its failed `wr add` is
  inferred, not observed: the job was released rather than archived, which an
  exit 1 explains (the `released job` line carries no exit code), and the
  retry's add was new (no `dups=1` line), so the kid was added once. The runner
  reported the release about 1s after recovery (run 4: recovered 12:35:09,
  released 12:35:10), after other runners' completions and reservations and the
  first RAC cycles. On the base, the 5 `selfadd` jobs in flight at a kill had
  already finished their add, and their archives were accepted after the
  restart. Which case a run gets depends on where in the command the kill lands,
  and the client code is the same on both trees. This is not a bug. One
  correction to item 6.3e: the add ran during the outage, not "in flight at the
  kill" (mangos resends an add that was in flight once it reconnects). The
  command did run twice, as 6.3e says.

No code change, so no test or gate run.

Evidence in `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-63f/`: `g3.sh`,
`drive3.sh` and `drive3.log`; `watch.sh` and each run's `.watch/`
directory (timeline, stall and healthy goroutine dumps, stall `bjobs`);
per-run `.out`, gzipped manager log and adder logs; `idle3.sh` (gaps of 3s
or more); `branch-wr.buildinfo.txt` (`vcs.revision` f04eaa3e,
`vcs.modified=false`); `bacct_*.txt.gz`, `hoststall.sh` and
`hoststall.txt`; `bhist-stallhosts.txt` (the suspension re-check). Nothing
is left running: `bjobs` is empty, no manager from
the run root is alive, the scratch worktrees and `GOCACHE` are removed, and
the work root (binaries, DB copies) is removed.

Open: why node-13-16 and node-13-22 stalled for about 27s and 18s. The cause is
most likely on the host (an NFS or kernel stall on the node, say), and wr cannot
see it. A gate that must not be fooled by it could check `bacct` for runners
clustered on one host before it blames a pause on the manager.

- [x] implemented
- [x] reviewed

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

G3 summary. Criteria 1 and 4 pass. Criteria 2 and 3 (p99) fail as
measured, because the farm gave the branch soak fewer runners. At matched
runner counts, throughput is equal and latency outside the minutes after
each restart is better (details below). The owner decides whether to accept
that, or to rerun the pair at a load both soaks reach.

| | Base | Branch |
| --- | --- | --- |
| tree | 6f1b41bb | 2d5f1899 |
| run directory | `$G3/soak-base/run/prodsim-1792425803` | `$G3/soak-branch/run/prodsim-1792439340` |
| ran (2026-10-19) | 17:03 to 20:09 | 20:49 to 23:54 |
| ports, pprof | 52710 to 52713, 6271 | 52720 to 52723, 6272 |
| runs (keys) | 447,691 (439,154) | 279,458 (276,522) |
| live jobs at the last recovery | 1,540,462 | 1,977,962 |
| peak RUN, mean RUN, mean PEND | 3,175, 1,452, 2,527 | 2,258, 923, 2,556 |
| stops | 17: 12 crashes (6 injected), 5 clean | 17: 12 crashes (6 injected), 5 clean |
| injected stall | 1792429049 to the crashon-stall kill at 1792429140 | 1792442624 to the crashon-stall kill at 1792442715 |
| manager peak RSS, heap in use | 67,133MB, 37,884MB | 77,923MB, 42,716MB |
| manager start, first and longest | 6.9s, 114s | 8.4s, 163s |

`$G3` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-g3`.

Setup. `$G3/soak-go.sh <base|branch>` is battery10's `soak-go.sh` with
only the tree, ports and `SOAK_ROOT` changed: `HOURS=3`, `RESTART_MIN=15`
with the same 11 restart kinds, `USE_FUSE=1 FUSE_ON_AT=3 FUSE_OFF_AT=4
STALL_AFTER_MIN=7 STALL_SECS=180`, `RAMP0=600
RAMP="0:600 8:1200 16:2100 25:3000 40:3600 60:4000"`, `RUNNER_FILELOG=1`,
`WRDEV_PRODSIM_KEEP_DB=1`, and the injectors crashon (stall and 2
bursts), crashafter, relbury r1 and r2, rundep and stopstate.
`FIXTURE` was `runstate-gate/fixtures/fix120k.db`, which prodsim copies.
Each tree's `run.sh` ran from a clean scratch detached worktree of that
tree and built its own `$SOAK_ROOT/wr`. Nothing replaced that binary during
the run. `go version -m` shows `vcs.modified=false` for both:
6f1b41bb has sha1 `d22b975d...` and 2d5f1899 has `13b52b9b...`
(`soak-*/*-wr.buildinfo.txt`, `*.sha1`). The two soaks did not overlap,
and all `OS_*` variables were unset. `$G3/analyse.sh` ran the branch
tree's tools over each soak, with `soakgate.py --source d1` for both,
since the base has the D1 lines too. The outputs are in
`$G3/analysis/{base,branch}/`. The load-matched breakdowns below, and
the scripts that made them, are in `$G3/analysis/extra/`.

`soakgate.py`, base:

```text
nondurable source=d1 inside=173 outside=298 runs=447691 outsidePct=0.0666
totals ok
doubles inside=174 outside=0 acknowledged=0
missing ran=439154 absent=0 excused=197
peakRUN=3175
```

`soakgate.py`, branch:

```text
nondurable source=d1 inside=185 outside=1125 runs=279458 outsidePct=0.4026
totals ok
doubles inside=183 outside=0 acknowledged=0
missing ran=276522 absent=0 excused=155
peakRUN=2258
```

1. No double run or lost job attributable to the change: PASS.
   - Doubles. All 174 doubles in the base and all 183 in the branch are
     inside the injected stall. From `doubles.tsv`, every first run
     started within the stall, before crashon-stall's `kill -9`: base
     1792429056 to 1792429063, branch 1792442633 to 1792442708. Those
     reservations were handed out while the fsync was held (D1), so
     the crash lost them. After the restart, `anyway.py` shows each first
     run's `jstart` rejected as "bad job" or "you must Reserve()", and
     its `jarchive` rejected as "you must Reserve()", so the job was run
     again. None was acknowledged. The cause is the injectors, and the base
     has the same behaviour. `doubles.py` counts 5 of the branch's doubles
     as "crash ended-while-down", but they belong to the same stall.
   - Lost jobs. `absent=0` in both soaks. In the base, `relburycheck.py`
     reports problems 0. In the branch it reports 6 `STUCK relb.r2.*`, all
     false positives of its snapshot timing. The relbury-r2 crash
     (1792443618) was followed by crashafter's crash right after the
     restart (1792443664), so the "+180" snapshot came only about 140s
     after the manager that ran the batch came up. Each "stuck" job's E
     mark is 0.7 to 4s before that snapshot, so its report was still in
     flight. By the +600 snapshot all 400 were buried, rel jobs had run
     twice and bur jobs once, as specified.
   - `rundepcheck.py`: base `{'OK': 35, 'CHECK': 4, 'OK-late-add': 1}`,
     branch `{'OK': 37, 'CHECK': 3}`. Each CHECK is a run with no end that
     a clean stop killed. Branch instances 14 and 16 are labelled
     `stop-crash`, but their runners logged "kill requested externally"
     at 21:53:09. That is the scheduled clean stop, which began at
     1792443188, 1s after the restart that followed crashon-burst
     (`restarts.tsv`: stop `ms=127749` logged at 1792443316). The A.14
     double is one of the stall doubles.
2. Steady ends per minute at least 90% of the base's: FAIL as measured.
   `latency.py` rc0 ends per minute: mean 1,511.1 against 2,402.4 (63%),
   p50 1,583 against 2,026 (78%). Over minutes wholly outside outages,
   the mean is 1,744.8 against 2,745.4 (64%). Load-matched by our LSF RUN
   in that minute, the two trees do the same work per runner: 1.74 to
   1.77 ends per minute per runner on the branch, against 1.69 to 1.90
   on the base, in every RUN band both reached
   (`throughput-*.txt`). The difference is how many runners LSF ran.
   - In the first hour, while LSF dispatched freely, the branch had more
     RUN than the base (963 to 1,756 against 457 to 1,131) and more ends
     per minute (1,879 to 3,007 against 940 to 1,603; `timeline-*.txt`).
   - From about t+60m (21:50 onwards), LSF started only 360 to 640 of
     our runners in most 15-minute periods, with 2,300 to 4,000 of ours
     pending. RUN fell to between 146 and 1,071, and ends per minute
     followed it. The base, three hours earlier, got 359 to 9,140
     runners per 15 minutes.
   - wr asked for the runners: mean PEND was the same in both soaks
     (2,527 and 2,556). It asked for the same sizes: runners by
     scheduler group are 400MB `results_portal` 25,343 against 22,686
     and 1024MB 3,353 against 2,314. LSF's pending reasons are its own
     ("New job is waiting for scheduling" in 73 of 90 probes, "Job slot
     limit reached" in 40 against the base's 23), and at 23:13 the
     `normal` queue held 21,250 pending jobs farm-wide. The order of the
     soaks may also matter: the branch ran straight after the base,
     under the same user's fair-share, and this was not measured.
   - Runners that ran at least one job ran 19.7 jobs each on the branch
     and 25 on the base (`runnerend-*.txt`). Runners did not exit
     earlier on the branch: the exit reasons have the same mix in both
     soaks (no more commands 46% against 43%, reconnect 16% against 18%,
     could not reach the server during an outage 37% against 35%).
3. Steady end-to-acknowledgement p50 and p99 no more than 10% worse:
   p50 PASS, p99 FAIL as measured. `latency.py` steady figures:

   | outcome | base n, p50, p99 | branch n, p50, p99 |
   | --- | --- | --- |
   | archive | 381,180, 1.17s, 5.45s | 233,084, 0.71s, 6.42s (+18%) |
   | release | 3,767, 1.23s, 6.15s | 2,249, 0.68s, 7.72s (+26%) |
   | bury | 87, 0.54s, 6.18s | 75, 0.64s, 3.40s |

   The p99 excess comes from the 5 minutes after each manager start.
   "Steady" counts ends from the moment a start completes, while runners
   still flush reports held through the outage. Leaving out those 5
   minutes, archives on the branch are better: p50 0.49s against 1.18s and
   p99 4.46s against 5.18s. Release p99 is 6.02s against 5.51s (n 1,213
   and 2,325). In the 5 minutes after a start, the branch's p99 over all
   outcomes is 11.37s against 6.05s. Most of that is the restart after
   the stall crash: 23.63s against 13.09s. When that crash came, the
   branch had 1,832 runners running against the base's 1,297
   (`latbins-*.txt`). Binned by RUN, neither tree is consistently worse.
   The branch's 1,000 to 1,499 band (p99 12.73s) holds its run of
   injected crashes from t+58m to t+100m.
4. Peak RUN against the 5,500 target: reported. The base peaked at 3,175
   and the branch at 2,258, both short of the target. LSF capped both;
   the ramp's portal target tops out at 4,000.

Strain points:

- LSF first. In the base, by t+15m LSF held 1,719 of our runners pending
  against a target of 1,200, with 561 running, nearly all "New job is
  waiting for scheduling". It reached 5,873 pending at t+36m, with
  "Affinity ... not enough processor units" and "Job slot limit
  reached" also appearing. In the branch, LSF kept up for the first
  hour (PEND 24 at t+5m), then stopped dispatching from about t+60m.
  `bjobs` stayed fast in both soaks (mean 137 and 119ms, maximum 283 and
  317ms).
- The manager's strain follows the backlog of live jobs, which grows
  because prodsim adds far more than the farm runs. Starts took 6.9s at
  first, rising to 114s in the base and 163s in the branch, whose last
  recovery held 1.98M live jobs against 1.54M. The branch's status calls
  were slower late in the run: `ws_seed` q4 p95 80.8s against 9.9s, and
  `status_portal_summary` p95 37.4s against 18.9s, max 118.8s against
  83.7s. Ping peaked during manager starts (26.0s and 29.8s).
  Final-report send or receive timeouts on runners, around the outages,
  numbered 6,342 in the base and 5,371 in the branch.

Open, not a G3 criterion: non-durable hand-outs outside the stall were
1,125 on the branch against 298 on the base (`outsidePct` 0.40 against
0.07). In both soaks they come in the 2 or 3 minutes after the
restart that followed the stall crash: branch 21:48 to 21:50, base 18:01
to 18:03. More runners were running at that crash on the branch (1,832
against 1,297). None became a double run.

Evidence: each run directory (with `launch.out`, `runnerlogs/`, the kept
DB in `dbdir/` and the binary) is under `$G3/soak-<tree>/run`, and the
analysis is in `$G3/analysis/`.

#### Investigation (owner request 2026-10-20)

From the saved data only (no new soak). Scripts and outputs are in
`$G3/investigate/`. `calls.tsv`'s `t_s` is a call's end, so call start is
`t_s - ms`.

A. Slower status calls late in the branch soak: a backlog and memory effect,
not a branch effect.

- Why the backlog was larger. prodsim's portal actor is closed-loop: it
  sends 1,000-job adds back to back and spent about 1,770 of every 1,800s
  in add calls in both soaks. Burst size does not depend on completions.
  Branch adds were faster (mean per 1,000-job call 3.6 to 3.9s against 4.1
  to 4.8s in the middle hour, 5.8s against 7.2s in the last), so it started
  29 bursts against 26 and submitted 2.21M jobs against 1.92M (+286k). It
  also completed fewer (279k runs against 448k, -168k, from LSF dispatch).
  The two add up to the ~440k gap in live jobs.
- Status cost tracks live backlog the same way on both trees. `ws_seed` p50
  by interpolated live jobs: 750k-1M 3.5s against 3.8s (base, branch),
  1.25M-1.5M 6.1s against 6.2s, 1.5M-1.75M 9.3s against 8.0s;
  `status_portal_summary` p50 is equally close. Outside the 5 minutes after
  a manager start, branch `ws_seed` p95 is 9.9s at 1.5M-1.75M and 8.5s at
  1.75M-2.1M, against the base's 10.7s at 1.5M-1.75M. RSS by live backlog is
  also the same (1.5M-1.75M: max 65.6GB against 63.4GB; heap 37.0 against
  37.4GB) (`statuslat-*.txt`, `rssvslive-*.txt`).
- The q4 p95 figures come from a handful of calls that began in the first
  2 minutes after a start. The worst are 3 `ws_seed` calls of 81 to 84s and
  one `getrs repgroup=portal` of 118.8s (server-side "slow request"
  1m58.8s), all started within 10s of start 15 (23:26:14, 1.76M live, 162s
  start, after a clean stop). The base's largest start had 1.54M live and
  its `ws_seed` stayed under 10.6s (`poststart-*.txt`).
- Cause of those outliers: the post-start goroutine dump
  (`profiles/poststart.1792448775.6.goroutine.txt`) shows the seed and the
  `getrs` in `db.addCompleteJobStatusByRepGroup`, walking `bucketRTK` and
  doing bolt `Get`s (`Page.FastCheck`) for every live and complete key of each
  rep group. The 30s CPU profile there used only 106% CPU, against 170 to
  250% after the base's late starts, so the handlers were mostly waiting,
  not computing. The host has 91GB RAM and 7GB swap, now full. The branch
  manager reached 73 to 78GB RSS beside a 31 to 33GB DB, past what the
  host can keep resident; the base peaked at 67GB. Cold mmap pages faulted
  from NFS explain it. Not proven: no fault counts were sampled.
- Code. `git diff 6f1b41bb 2d5f1899` does not touch `writeStatusCountSeed`,
  `statusSeedCounts`, `getStatusByRepGroup`,
  `retrieveCompleteJobStatusByRepGroup` or `handleGetRepGroupStatus`.
  `serverWebI.go` only adds `JStatus.RunRAM/RunTime/RunDisk`. RAC/queue
  changes (`RegroupReady`, write turns) do not share a lock with the seed's
  bolt `View`. Item RLocks in `snapshotReadyJobs` and `addQueueItemStatus`
  appear in both trees' post-start profiles.

B. Hand-outs before the reservation was on disk, outside the stall: a load
effect. Write turns and hold removal were not shown to cause it. The base
has the same exposure.

- How it is counted. `soakgate.py --source d1` counts each manager-log line
  `reservation handed out before it was recorded on disk`.
  `persistReservation` logs it when the reservation's run-state write is
  not confirmed committed within `ReserveWriteWait` (10s), or the DB is
  closed. The write stays queued and normally commits shortly after.
- Where they are. Branch: 1,116 in 21:48:12 to 21:50:05, after the
  post-stall-crash restart (21:47:39), plus 9 after start 15. Base: 111 in
  18:01:46 to 18:03:23, after its post-stall-crash restart; 66 at 18:41:49
  to 50, a minute after a crash restart with 915k live (that minute's
  archive `maxTx` was 10.8s); and 121 at 19:33:37 during a clean stop.
  Those 121 were reserves that had waited 3m22s on the base's hold, then
  were handed out as the DB closed. The branch cannot do this, having no
  hold. So the summary above is wrong that all of the base's 298 came after
  the stall restart; 111 did.
- Rate in the matched window (first 150s after each post-stall-crash
  restart, runner `reserved a job` lines, `d1rate-*.txt`). Branch: 3,897
  hand-outs, 1,116 non-durable (28.6%), 1,832 runners in flight. Base: 3,361
  hand-outs, 111 non-durable (3.3%), 1,297 runners. Hand-outs per runner in
  flight were lower on the branch (2.1 against 2.6 per runner). Without the
  hold the branch did not reserve faster. A larger share of its run-state
  drains missed 10s: the lines come in bursts of 50 to 265 every ~10s
  (21:49:25, :36, :46, :57), each one whole best-effort drain.
- Why drains missed 10s. That window's archive fold lines show writers
  queuing longer for the bolt lock on the branch: meanLock 1.9 to 3.7s,
  maxLock to 10.6s, against 1.2 to 1.4s and 7.8s. Their own commits were
  similar (meanTx - meanLock 0.6 to 0.8s against 0.4 to 0.5s). Adds were
  matched (about 36k jobs in the 4 minutes in each). The branch had 41% more
  runners flushing starts, touches and final reports held through the
  outage, on a DB still behind FUSE.
- Write turns are not the cause at matched load. An in-process probe
  (`zz_turnsprobe_test.go`, toggle in `turnsprobe-toggle.diff`, run in a
  scratch copy of 2d5f1899) streams archives (60ms commit), back-to-back
  1,000-job adds and run-state durable writes. Four interleaved rounds
  each, turns on against off: run-state p50 136 to 147ms against 137 to
  154ms, p99 211 to 233ms against 215 to 268ms, the same archive folds
  (`turnsprobe-results.txt`). One indirect branch effect is left open:
  item 6.3d measured archives completing 13 to 17% faster without the
  hold, which keeps the writer saturated more of the time. This data cannot
  separate that from the extra runners.
- Crash exposure. A non-durable hand-out is exposed from hand-out until
  the first commit of its run-state or `Started` write. Branch `jstart`
  calls in that window took up to 27.8s, so the exposure lasts seconds to
  tens of seconds. A crash then recovers the job's older record. The job
  goes to Ready, the live runner's start and touches get `ErrBadJob`, its
  archive `ErrMustReserve`, and another runner runs it again
  (`.docs/bugfixes/260928-reserve-durability.md`). That is a double run,
  never acknowledged, the same as the 174 and 183 stall doubles. It is not
  a lost job: the job's live record stays on disk and it reruns. Once
  `Started` commits, `260917-start-durability.md` keeps the run safe. The
  base has the same 10s bounded wait and the same exposure. It showed it at
  a lower rate under lighter load, plus the shutdown hand-outs above. No
  crash landed in these windows: the branch's next crash was 21:52:17, two
  minutes after the last line, so none became a double.

Rerun. It is not needed for A, since status cost per live job is the same
on both trees. A rerun would diverge again, because the portal actor is
closed-loop on add speed. For B, matched load would mean matching runners in
flight at the post-stall crash, and LSF dispatch decides that, not the ramp:
over minutes when wr had runners pending, base RUN was at least 500 in 91%,
750 in 77%, 1,000 in 68%; branch 81%, 64% and 54%. The branch's last hour
reached 500 in only 50%. No cap gives both trees the same load reliably. A
cap of 750 (`RAMP="0:600 8:750"`) is the highest both held in about two
thirds of minutes, and running the branch first would test the fair-share
order. A cheaper check of B is a paired base and branch crash-restart at a
fixed runner count, counting non-durable lines per hand-out.

#### Crash-pair check of B (owner request 2026-10-20)

Result: B is not explained by load alone. At matched runner counts, the
stall crash shows no tree difference. But the branch has bursts of
non-durable hand-outs that the base never had: after a plain crash, and in
steady running with no crash at all. Each burst is one write stall of 10 to
20s that starts with a slow 1,000-job add. No double run came of it. The
trials are few (5 per tree), so the difference is indicative, not proven.

Setup. `$CP` is `$G3/crashpair`. `$CP/trial.sh <tree> <k>` runs one trial:
a fresh isolated prod-mode manager of that tree's prebuilt binary, on a
fresh copy of `fix120k.db` behind `fusestall` from the first start. prodsim
runs for 100 minutes with a fixed portal concurrency and limit (`TARGET`),
1,000-job add bursts every 5 minutes, and no operator limit changes.

- Crash A, at the start plus 18 minutes once our LSF RUN reaches `GOAL`:
  hold every fsync for 180s, `kill -9` the manager 90s in, release, then
  restart.
- Crash B, 8 minutes after restart A once RUN reaches `GOAL`: a plain
  `kill -9` and restart.

`$CP/run-all.sh` ran the trials one after another, alternating base and
branch, and `$CP/analyse-trial.sh` ran the soak tools and `cpmeasure.py` over
each one. The binaries are base 6f1b41bb (sha1 `c78cfb31...`) and branch
2d5f1899 (`ecf287e8...`), both `vcs.modified=false`. They ran on 2026-10-20
from 11:00 to 00:33. The trials had these settings:

- k1 and k2: `TARGET=600 GOAL=550`, with a 20-minute wait for the goal.
- k3 and k4: `TARGET=350 GOAL=420`, 20 minutes. k4 missed the goal at both
  crashes (407 and 347).
- k5 to k10: `TARGET=350 GOAL=400`, 60 minutes. Crash B of k9 went ahead at
  RUN 200. Crash B of k10 is void: prodsim had already finished at 00:20,
  before RUN reached 400 at 00:31, so its window holds 2 hand-outs.

The outputs are each trial's `prodsim-*/` (`soakgate.txt`, `cpmeasure.tsv`,
`manager.log`, `profiles/`) and `$CP/summary.txt`, `d1-where.txt`,
`adds-steady.txt`, `d1-vs-slowreads.txt` and `d1-vs-slowadds.txt`, with the
scripts that made them. The trials' DBs were deleted after `dbstart`.

Non-durable lines (D1) per runner `reserved a job` line in the 300s after
each restart. RUN is our LSF RUN at the crash:

| trial | crash A: RUN, D1 / hand-outs | crash B: RUN, D1 / hand-outs |
| --- | --- | --- |
| k1 base | 712, 30 / 3,218 | 550, 0 / 5,772 |
| k2 branch | 439, 0 / 1,289 | 551, 595 / 5,668 |
| k3 base | 450, 0 / 1,707 | 424, 0 / 4,350 |
| k4 branch | 407, 18 / 1,519 | 347, 73 / 2,633 |
| k5 base | 459, 3 / 1,768 | 401, 0 / 4,047 |
| k6 branch | 461, 6 / 3,592 | 451, 0 / 4,161 |
| k7 base | 422, 0 / 1,460 | 411, 0 / 4,056 |
| k8 branch | 410, 0 / 1,033 | 406, 204 / 3,977 |
| k9 base | 419, 11 / 1,283 | 200, 6 / 1,904 |
| k10 branch | 417, 1 / 1,793 | void |

- Crash A, the G3 post-stall-crash case, at matched RUN: no difference.
  Over k3 to k10, base 14 of 6,218 hand-outs (0.23%, in 2 of 4 trials,
  mean RUN 438) against branch 25 of 7,937 (0.31%, 3 of 4, mean RUN 424).
  All of them came in the first 150s. G3's 28.6% and 3.3% were at
  1,832 and 1,297 runners, well above the 400 to 460 here.
- Crash B: base 6 of 20,129 (0.03%, in 1 of 5 trials, k9 at RUN 200),
  against branch 872 of 16,439 (5.3%, in 3 of 4: k2, k4 and k8). Over k3
  onwards it is 0.04% against 2.6%. The branch's lines did not follow the
  restart closely. They came at 0 to 120s (k2), 50 to 160s (k4) and 262 to
  283s (k8).
- Steady running, outside the stall and both 300s windows
  (`d1-where.txt`): the base had none in any of its 5 trials (185k runs).
  The branch had 197 (k2, 12:48 to 12:49), 102 (k4, 15:02) and 22 (k8,
  19:29), and none in k6 or k10. Each burst is a single episode, not a rate.
  For 3 of 5 trials against 0 of 5, the one-sided Fisher exact p is 0.08.
- Doubles: in all 10 trials every double is inside the injected stall
  (`doubles inside=0..23 outside=0 acknowledged=0`), and `absent=0`. No
  burst was hit by a crash.

What a burst looks like (`adds-steady.txt`, `d1-vs-slowadds.txt`). The
manager logs a "slow request" for a 1,000-job add (12.1 to 12.6s server
side). `jstart`, `jarchive` and `jrelease` then take 10 to 19s. That
minute's archive fold shows `maxTx` and `maxLock` of 7 to 10.7s, with
`txs` down from 300 to 600 per minute to 9 to 87. A batch of run-state
drains misses the 10s `ReserveWriteWait`, and the 50 to 265 D1 lines come
together. The base had no add over 10s in steady running in any trial. The
branch had them in k2, k4 and k8, each at a burst. After restarts both trees
have slow adds (base k1, k3, k5, k9), and on the base they came with few or
no D1 lines.

Ruled out: long status reads. `d1-vs-slowreads.txt` shows `getrs` and
`getbr` calls of 25 to 36s on the base in k1, k7 and k9 with 0 to 6 D1
lines, and k2's 595 lines overlap no slow read.

Open. The add path or the add-first write turn (f04eaa3e) now looks like a
cause, at a load the in-process `zz_turnsprobe_test.go` did not model: its
adds committed in tens of milliseconds, while a slow add here holds the
writer for over 10s. This data cannot say whether the add was slow by itself,
with everything else queued behind it, or whether something else stalled
the writer and the add was its first victim. The 15s goroutine dumps cover
only the first 150s after each restart. Only k2's crash-B burst (12:50 to
12:51) falls inside them (`profiles/postcrashpair-plain.*`). There, 9 of the
10 dumps from 12:50:32 to 12:52:33 show the one bolt write transaction
inside disk I/O: `Tx.write` (pwrite) 4 times, `fdatasync` 4 times and
`DB.grow` once. The holder is a different writer from dump to dump: the
archive writer 6 times, the best-effort writer once, a `bolt.Batch` once,
and the new-jobs (add) writer once, in `DB.grow`. So in that burst the stall
was the DB's write and sync time through FUSE and NFS. No lock was held by a
slow handler, and no one writer owned the stall. Base and branch go through
the same FUSE and NFS path. Still open: why only the branch's commits reach
10s. The candidates are larger folded commits (`meanFold` 51 and 65 in that
burst's fold lines), or the add's file growth coming earlier in the queue
under write turns. The exposure is the same kind as in B above: a crash
inside a burst runs those jobs twice, none acknowledged.

Owner decision 2026-10-25: rerun the G3 soak pair (base, then branch) on the
final code, after items 6.4f and 6.4g, at the full production ramp (about 7
hours). If the farm gives the two runs different runner counts, judge them at
matched counts. This item is settled on that rerun.

- [ ] implemented
- [ ] reviewed

### Item 6.4b: reservation-write stalls past ReserveWriteWait

Question. Item 6.4's crash-pair check found that at about 400 runners the
branch has bursts in which reservation writes miss the 10s
`ReserveWriteWait` ("reservation handed out before it was recorded on
disk", D1) in steady running and after a plain crash, while the base
almost never does. Each burst comes with a 1,000-job add taking about 12s.
Which change causes it: write turns (f04eaa3e), larger folded commits, or
the removal of the hold?

Verdict: write turns. Two write-turn mechanisms are identified and fixed
(below), and the hold removal is not shown to cause it. The fix keeps
write turns and the 6.3e add benefit. It cuts head's D1 bursts, but the
probe cannot tell the fix from before write turns (pre, 959f3fd0) on D1
bursts: in the `slow1` probe 3 of 8 runs of the fix (fix2) had one, and
under the same conditions 1 of 12 pre runs did (`slow1` and the plain-pre
runs of `slowhold`; Fisher exact p=0.26). fix2's `Started` and archive
tails stay longer than pre's. That remaining gap is unexplained, though in
this probe the two trees' db code differs only by write-turns code (see the
`slow1` figures below).

1. An add of 1,000 jobs or more is too big for the new jobs writer and goes
   through `storeNewJobDataChunked`, one `bolt.Batch` transaction per
   bucket. Those transactions took no write turn. Write turns put the
   archive, best-effort and new jobs writers in one queue, so on bolt's lock
   a chunk competed with only the one turn holder, and a run of chunks took
   every other transaction. The best-effort writer, which carries the
   reservations' run-state writes and `Started`, then waited behind a chunk
   for each turn ahead of its own. On 959f3fd0 all four queued on bolt's
   lock, and a chunk came round once per cycle.
2. An add that keeps arriving went next every time a turn ended. When the
   archive writer's turn came between two adds, a best-effort write waited
   behind both, and the 6.3e bound ("at most three other transactions")
   did not hold. A burst's last add of 618 jobs (6MB of 10KB commands)
   goes through the new jobs writer, so this cost several seconds.

With the DB on a slow filesystem, one big-add chunk takes several seconds,
and the best-effort writer's wait grew from one chunk to two or three, past
10s.

#### Evidence

Crash-pair trials, every portal burst in steady running (outside 300s after
a start and outside the stall; `$P/trialdata/bursts.py`, `bursts.txt`).
Per burst, the worst archive-fold `maxTx` of the minutes it covers, base
(54 bursts) against branch (63):

| | p50 | p90 | bursts over 5s | over 7s | add call mean p50 |
| --- | --- | --- | --- | --- | --- |
| base | 2.17s | 3.26s | 4 | 1 | 2.26s |
| branch | 2.79s | 6.08s | 10 | 5 | 2.60s |

Mann-Whitney: `maxTx` p<0.0001, add mean p<0.0001. So the branch's writer
stalls longer at bursts generally, not only in item 6.4's three D1
episodes. The goroutine dumps cover only k2's crash-B burst (12:50 to
12:51), not its steady D1 burst (12:48 to 12:49). In that crash-B burst,
`dumpclass.py` shows, in 3 of its 4 dumps
(`postcrashpair-plain.17924970{32,47,62,78}`), a `bolt.Batch` big-add chunk
in or queued at bolt's lock beside one of the three turn writers, the
archive or best-effort writer, with the other of those two waiting for its
turn. The third turn writer, the new jobs writer, is neither in a
transaction nor waiting in those three dumps.

In-process probe (`$P/probe/zz_64bprobe_test.go`, `TestZZ64bProbe`, not
committed). It runs against the real `initDB` on a copy of `fix120k.db` on
NFS, behind `fusestall`, with backups on. 400 runner goroutines loop:
reservation run-state write (`updateJobRunStateDurableWithin`, 10s, as
`persistReservation` does), `Started` (durable run-state), a lognormal run
with a 24s median, then archive, or a durable release for 2%. Small adds of
5 jobs every 2s go through the new jobs writer. A portal burst is prodsim's:
two phases of 6,618 jobs with 10KB commands, 1,000 per add, back to back,
dedupe jobs in dep groups and compress jobs depending on them. A scratch
hook (`zz_64bhook.go`, plus `patch.py` on `db.go`) logs every archive,
best-effort, new jobs and big-add transaction: when its writer asked, when
it began and when it ended. Scratch detached worktrees of each tree were
used, and runs were interleaved one at a time with the order rotated each
round. Trees: pre is 959f3fd0; head is 2d5f1899, the same code as
f04eaa3e; fix is head with item 1 fixed; fix2 is head with items 1 and 2
fixed (the change below).

On plain FUSE over NFS (`ab1`, 4 rounds each, 300s, 3 bursts), nothing
reached 10s, but head's reservation writes were slower in the tail: p90
1.80 against 0.45s, p99 3.53 against 1.87s (p=0.021), max 4.86 against
3.05s (p=0.043). Its `Started` p99 was also worse: 4.86 against 2.94s
(p=0.043). Each best-effort wait over 1s had 1.21 big-add chunks
begin during it on head, against 0.76 on pre (p=0.021). Archive folds,
`maxTx` and add latency did not differ, but head archived 2% fewer jobs
per second: 12.55 against 12.80 (p=0.017).

To reach the LSF regime, `fusestall` was given a write limit (`-wbps`,
scratch build `fusestall-slow`) of 5MB/s, which makes a 1,000-job add take
about 12s, as in the D1 bursts. `slow1`: 8 rounds of each tree, one burst
per run from 30s into a 240s window. Medians over runs, p from Mann-Whitney
against pre (`slow1/stats-out.txt`):

| | pre | head | fix | fix2 |
| --- | --- | --- | --- | --- |
| D1 per run | 5 (1 to 11) | 716 (645 to 1,053), p=0.001 | 84 (11 to 162), p=0.001 | 3 (1 to 139), p=0.96 |
| runs with D1 over 11 | 0 of 8 | 8 of 8 | 7 of 8 | 3 of 8 (79, 81, 139) |
| reservation write mean | 1.52s | 4.30s | 1.60s | 1.20s |
| `Started` p99 | 11.8s | 21.6s, p=0.001 | 17.8s, p=0.001 | 15.6s, p=0.001 |
| archive p50 | 3.07s | 6.92s, p=0.001 | 3.32s, p=0.027 | 3.54s, p=0.009 |
| archive p99 | 11.2s | 24.7s, p=0.001 | 16.8s, p=0.001 | 15.0s, p=0.001 |
| archive `maxTx` | 8.7s | 14.2s | 14.8s | 8.8s, p=0.83 |
| longest best-effort wait | 8.7s | 15.5s | 14.0s | 8.6s, p=0.35 |
| big-add chunks per best-effort wait over 1s | 0.81 | 2.22 | 0.89 | 0.85 |
| 1,000-job add p50 | 11.9s | 11.1s | 11.6s | 11.8s |
| small add p50 | 0.59s | 0.11s | 0.22s | 0.27s |
| archives/s | 9.4 | 7.7 | 9.1 | 9.2 |

fix2 cuts head's D1 bursts, but the probe does not show it differs from pre
on them. Its median D1 is pre's (p=0.96), and 3 of its 8 runs had 79 to 139
(per run, fix2: 1, 2, 2, 3, 3, 79, 81, 139; `slow1` pre: 1, 2, 3, 4, 6, 10,
11, 11). No `slow1` pre run was above 11, but `slowhold`, queued right after
`slow1` by the same `queue.sh` under the same settings (`P64_DUR_S=240`,
`P64_FIRST_S=30`, `fusestall-slow -wbps 5000000`), ran 4 more plain pre
runs, and one of them had 113 (per run: 4, 4, 2, 113;
`slowhold/results.txt`). Pooled, 1 of 12 pre runs was above 11 against 3 of
8 fix2 runs (Fisher exact p=0.26; Mann-Whitney on D1 p=0.85), which is not
a clear difference.

fix2 is still significantly worse than pre on `Started` p99 (15.6s against
11.8s, p=0.001) and archive p99 (15.0s against 11.2s, p=0.001), though
better than head on both (21.6s and 24.7s; head against fix2 p=0.001 for
each). In `slow1` the `Started` p99 runs do not overlap: every fix2 run
(14.5 to 17.4s) is above every pre run (11.5 to 14.0s). With `slowhold`'s 4
plain pre runs pooled in, fix2 is still worse on both (p=0.002 each), but
one pre run overlaps: the run with 113 D1 had a `Started` p99 of 20.4s and
an archive p99 of 17.9s, above every fix2 run (at most 17.4s and 15.4s).
fix2's archive p50 is also higher than pre's (3.54s against 3.07s, +15%,
p=0.009). This probe does not show what causes the remaining gap: fix2
matches pre on archive `maxTx` and the longest best-effort wait.

Mechanism 1 shows in the big-add chunks begun during each best-effort wait
over 1s: 2.22 on head against 0.81 on pre, and 0.89 once chunks take turns
(fix). The transaction logs show mechanism 2. A typical longest wait on head
is r4-head's "big-add chunk 8.0s, add, archive 1.3s, add 6.8s" (16.2s), an
add having gone ahead twice. On fix the chunks no longer stack, but r1-fix's
"chunk 7.6s, add, archive, add 6.9s" (14.8s) remains: the 618-job add went
ahead twice. On pre and fix2 the longest waits are mostly one chunk plus
small transactions (8.0 to 9.5s); in one run of each (r4-pre, r4-fix2), a
618-job add instead.

Other candidates:

- Larger folded commits: no. `meanFold` and archive transaction time were
  the same on pre and head without the write limit; in `slow1`, head's
  `meanFold` (3.1) is the lowest of the four trees.
- The hold removal: not shown. The probe runs at the db layer, so the hold
  was imitated with a 450ms sleep before each reservation on pre
  (`slowhold`, 4 rounds each). The imitated hold raised D1 rather than
  reducing it, though not significantly (116 against 4, p=0.08), and
  per-runner rates in the trials are the same on both trees (item 6.4).
  The crash-pair trials compare base with the branch, but the probe's pre
  against head isolates write turns: their db code differs only by
  f04eaa3e.

#### Fix

`jobqueue/writeturns.go`, `jobqueue/db.go`:

- A big add's chunk transactions take a write turn
  (`writerChunkedAdd`, no preference, so turns go to it in arrival order)
  through `updateInTurn`, instead of `bolt.Batch`. A panic in a chunk's
  put now fails only that add, through `applyNewJobsOp`'s recover. This is
  a fix, not parity: in bbolt v1.4.3, `bolt.Batch`'s `safelyCall` recovers
  a panic only to return `trySolo`, and `DB.Batch` then reruns the function
  with a plain `db.Update` in the caller's goroutine, where it panics
  again. That panic reached `storeNewJobStores`' deferred
  `internal.LogPanic(..., true)`, which exits the manager. The existing
  `applyNewJobsOp` comment made the same false claim about `bolt.Batch`
  and is corrected (comment only).
- The new jobs writer goes next when it is waiting, unless it has already
  gone ahead of the longest-waiting writer of another kind since that
  writer began waiting. Every other writer then waits behind at most the
  transaction in flight, one add, and one transaction of each other writer
  waiting, however many goroutines take turns as the new jobs writer. An
  add still goes straight after the transaction in flight unless an add has
  already overtaken the oldest waiter of another kind.
- Comments that claimed `bolt.Batch` recovers a panic are corrected
  (comment only): on `errArchivePanic`, `errBestEffortWriteAborted`,
  `drainBestEffort`'s default error and `applyArchiveOp` in
  `jobqueue/db.go`, and on `TestStartDurabilityAbortedWriteIsNotCommitted`,
  `TestReliable4AddPanicStaysPerAdd` and
  `TestReliable4ArchivePanicStaysPerJob`. All seven were already on develop
  (6f1b41bb), as was the `applyNewJobsOp` one above.

Tests (`jobqueue/nohold_write_turns_test.go`):

- "a best-effort write queued before a big add's first transaction commits
  ahead of it": an archive is held in its transaction, an add waits, then a
  best-effort write, then a two-chunk big add. The add must commit next and
  the big add's first chunk two transactions later. It orders the add, the
  best-effort write and the big add by waiting for one, two and then three
  writers to be waiting for a turn. RED on HEAD: the chunk never waits for
  a turn, so the wait for three turn waiters times out (with an earlier
  form that ordered them by sleeping, the chunk committed straight after
  the archive, ahead of the waiting add, 5 of 5); GREEN after.
- "a big add whose put panics fails only itself and gives up its turn".
  It guards the recover and the turn's release, and requires the add's
  error to wrap `errNewJobsPanic`. On HEAD the panic killed the test binary:
  `bolt.Batch` recovered it in its batch run, returned `trySolo`, and reran
  the put with a plain `db.Update` in the caller's goroutine, which panicked
  again, whatever `MaxBatchDelay` is. It fails if `applyNewJobsOp` has no
  recover (the panic kills the test binary), or if the recover returns an
  error that does not wrap `errNewJobsPanic`.
- `TestNoHoldWriteTurnsAddOvertakesOnce` drives `writeTurns` directly with
  an add that keeps arriving. The archive and best-effort writers that were
  waiting each get their turn after one add, not one add per turn. RED on
  HEAD 3 of 3 (the second add went ahead of the archive writer again);
  GREEN after.
- "a big add's first transaction arriving while no add waits commits after
  a best-effort write queued before it": an archive is held in its
  transaction, a best-effort write waits for its turn, then a two-chunk big
  add arrives with no add waiting. The big add's first chunk must commit two
  transactions after the archive. It fails if chunks take the new jobs
  writer's turns (`updateInTurn(writerNewJobs, …)` in
  `storeNewJobDataChunked`): the chunk then commits straight after the
  archive, 3 of 3. The test above cannot see that change, because there the
  add has already overtaken the best-effort write.
- `TestNoHoldWriteTurnsAddOvertakesOnce`, "an add arriving after the archive
  writer's turn still goes ahead of the best-effort write": with no add
  waiting, the archive writer's turn ends and an add then arrives. The add
  must go next, since no add has overtaken the best-effort write. It fails
  if `give` counts every turn handed over as an add's turn (the best-effort
  write then goes first), 3 of 3.
- `TestNoHoldWriteTurnsAddOvertakesOnce`, "the chunk's turn does not count
  as an add going ahead of the archive": a big add's chunk and then an
  archive wait for the turn in flight. The chunk goes first, then an add
  arrives and must go next, ahead of the archive. It guards that a chunk's
  turn does not count as an add's: it fails if `give` counts
  `writerChunkedAdd` turns too (the archive then goes first), 3 of 3.
- "a best-effort write queued while a big add's first transaction is in
  flight commits between that transaction and the add's next one": a
  two-chunk big add is held inside its first chunk, and a best-effort write
  then waits for a turn (one turn waiter). Once released, the add's second
  chunk must commit two transactions after its first, the best-effort
  write's between them. The putter records every chunk's transaction id. It
  fails if the chunked add takes one turn for all its chunks
  (`db.turns.take(writerChunkedAdd)` and a deferred `give` at the top of
  `storeNewJobDataChunked`, each chunk straight to `db.bolt.Update`): the
  second chunk then commits straight after the first (6 expected, 5 got),
  and no other test fails. Every earlier test passed under that change,
  since each recorded only a big add's first transaction.
- `TestNoHoldWriteTurnsAddOvertakesOnce`, "the first add goes next, then
  the archive, ahead of the second add": two adds and then an archive wait
  for the turn in flight. Each queued writer sends its own label, not just
  its kind, when it gets the turn, so the test tells the two adds apart: the
  first add must go next, then the archive, then the second add. It guards
  that an add's turn is checked against the oldest waiter of another kind,
  not the oldest waiter: it fails if `nextTurn` checks only `waiting[0]`
  (`other := 0`, or -1 when `waiting[0]` is a new jobs writer), since the
  second add then goes ahead of the archive. It also guards that adds go in
  arrival order (M6 below: `nextTurn` picking the last waiting new jobs
  writer, which survived while the queuer sent only the writer's kind). The
  other direct cases use labels too.
- "a writer whose transaction panics gives up its turn": a best-effort
  turn's transaction panics, and the test recovers it; a later turn must
  still get through. It fails if `updateInTurn` calls `give` only after a
  returning `Update` (M8).
- "two big adds in progress at once each wait for a turn of their own": with
  an archive held in its transaction, two two-chunk big adds must both wait
  for a turn. It fails if big adds are serialised (M12).
- "a big add whose second transaction fails keeps its first one committed":
  the putter fails its second call. Its first call must have stored exactly
  `storeBatchGranularity` (1,000) of the add's 2,000 items, and every key of
  it must then be in the live bucket. It fails if earlier chunks are rolled
  back on a later chunk's error (M13), or if each chunk's transaction stores
  the whole bucket instead of its own chunk (M26: 2,000 stored, not 1,000).
- "a write that takes no turn does not wait for the turn in progress": with
  a turn held, `deleteLiveJobs` must still complete. It fails if
  `deleteLiveJobs` takes a turn (M14). It covers this one path, not every
  transaction that takes no turn.
- Mutants, one at a time in a scratch copy, each against
  `TestNoHoldWriteTurns` and `TestNoHoldWriteTurnsAddOvertakesOnce` with
  `-race`, one run each. Every one is killed except M17, which cannot
  change behaviour:

  | Mutant | Claim it breaks | Killed by |
  | --- | --- | --- |
  | M1 `nextTurn` returns 0 (FIFO) | an add goes next | first 6.3e case; direct cases |
  | M2 no "already gone ahead" rule | other writers' bound | "keeps arriving"; "two adds" |
  | M3 oldest other is the last one | longest-waiting writer goes | "keeps arriving" |
  | M4 no add waiting: last goes | longest-waiting writer goes | chunked DB cases; direct cases |
  | M5 every turn counts as an add's | `addTurns` counts adds | "after the archive writer's turn"; "chunk's turn" |
  | M6 last waiting add goes | adds in arrival order | "two adds" |
  | M7 chunk turns count as adds | big adds get no preference | "chunk's turn" |
  | M8 `give` not deferred | turn ends on panic | "transaction panics" |
  | M9 `other := 0` | any add's turn counts | "two adds" |
  | M10 chunks on `writerNewJobs` | big adds get no preference | "no add waits" |
  | M11 one turn for all chunks | each chunk takes a turn | "in flight ... next one" |
  | M12 big adds serialised | several big adds at once | "two big adds" |
  | M13 earlier chunks rolled back | earlier chunks stay committed | "second transaction fails" |
  | M14 `deleteLiveJobs` takes a turn | only `updateInTurn` takes turns | "takes no turn" |
  | M15 strict add/other alternation | add waits only behind writers it overtook | "keeps arriving" |
  | M17 fast-path `take` counts an add | none (equivalent) | survives |
  | M19 compare with the add's own arrival | any add's turn counts | "two adds" |
  | M21 `updateInTurn` takes no turn | writers take turns | first 6.3e case (its wait for turn waiters) |
  | M22 no recover in `applyNewJobsOp` | a chunk's panic fails only its add | "put panics" |
  | M23-M25 best-effort, archive or new jobs writer takes no turn | those writers take turns | first 6.3e case (its wait for turn waiters) |
  | M26 each chunk stores the whole bucket (`{bucket, s.encodes, s.put}`) | storeBatched-sized transactions | "second transaction fails" |

  M17 increments `addTurns` when `take` finds no turn in progress. No
  writer is waiting then, and each later waiter records `addTurns` at
  arrival and is compared against it only after `give` increments it, so
  the comparison is unchanged: the `addTurns` comment says this case need
  not be counted.
- The first 6.3e case ("an add commits in the transaction right after the
  archive in flight") orders the best-effort write and the add by waiting
  for one and then two writers to be waiting for a turn, instead of
  sleeping `reliable4ACAddSettle` after each (`noHoldWTBestEffortAsync` no
  longer sleeps; its other callers already waited for turn waiters). What
  it asserts is unchanged. It still fails under M1 (the add commits one
  transaction later, 7 against 6), and under M21 and M23 to M25 its wait
  for turn waiters now times out (the best-effort write, or for M25 the
  add, never waits for a turn), where the sleep form failed the ordering
  assertion; 1 of 1 runs each. The other 6.3e test passes unchanged.

#### Gates and figures

`gofmt` and `go vet ./jobqueue/` are clean and `make lint` reports 0
issues. `make test` and `make race`, with `OS_*` unset, pass: 1,004 passed
each (`$P/make-test.log`, `make-race.log`). `TestNoHoldWriteTurns` and
`TestNoHoldWriteTurnsAddOvertakesOnce` passed 5 runs in a row with
`-race` after the two mutant-guarding tests were added, and `make test`
and `make race` were rerun then (1,004 passed each; the new tests are
cases inside existing test functions). After the chunk-turn test and the
`errNewJobsPanic` check were added, the two tests passed 5 runs in a row
with `-race`, `gofmt`, `go vet` and `make lint` were clean again, and
`make test` and `make race` passed (1,004 passed each).

After the third review's fixes (comments, this record, and the first
chunked test ordered by turn waiters instead of sleeps), on 2026-10-22: the
two tests passed 5 runs in a row with `-race`, `gofmt`, `go vet` and
`make lint` (0 issues) were clean, and `make test` and `make race` passed:
1,004 passed each, 22 and 21 skipped (`$P/make-test.log`, `make-race.log`,
replacing the 2026-10-21 logs). The chunk-turn test still fails with chunks
on the new jobs writer's turns (3 of 3); the reordered test does not see
that change, as before.

After the fifth review's fixes (the chunk-interleaving test, this record's
D1 comparison with `slowhold`'s plain pre runs, and comments), on
2026-10-22: the two tests passed 5 runs in a row with `-race`, `gofmt`,
`go vet` and `make lint` (0 issues) were clean, and `make test` passed
(1,004 passed, 22 skipped). The first `make race` failed one test,
`TestReliable4ArchiveFoldMeansStaySelfConsistent`, which polls the
archive-fold log for every archive and timed out
(`$P/make-race-foldlog-fail.log`). It passed 6 runs in a row with `-race`
alone, and a rerun of `make race` passed (1,004 passed, 21 skipped).
`$P/make-test.log` and `make-race.log` replace the earlier logs. The
failure is not in this item's code: `archiveFoldStats.observeTx` adds
`txs` before `archives`, and a `take()` between the two logs too few
archives for its interval. The next `take()` then finds `txs` at 0 and
logs nothing, dropping the rest. The code is the same on develop.

After the seventh review's fixes (the two-adds-before-an-archive test, the
`nextTurn` comment, the `bolt.Batch` panic comments, and this record), on
2026-10-22: the write-turns tests passed 5 runs in a row with `-race`, the
new case failed under the `waiting[0]` mutant (1 of 1 runs), `gofmt`,
`go vet ./jobqueue/` and `make lint` (0 issues) were clean, and `make test`
and `make race` passed on the final tree: 1,004 passed each, 22 and 21
skipped (`$P/make-test.log`, `make-race.log`, replacing the earlier logs).

After the eighth review's fixes (labelled direct turns, so the two-adds case
tells the adds apart; four new cases; the "only `updateInTurn` takes turns"
comment; the test file header; and the mutant table above), on 2026-10-22:
every mutant in the table but the equivalent M17 failed the write-turns tests
(M6, M8 and M13 rechecked on the final tree), the write-turns tests passed 5
runs in a row with `-race`, `gofmt`, `go vet ./jobqueue/` and `make lint`
(0 issues) were clean, and `make test` and `make race` passed on the final
tree: 1,004 passed each, 22 and 21 skipped (`$P/make-test.log`,
`make-race.log`, replacing the earlier logs; the new tests are cases inside
existing test functions).

After the ninth review's fixes (the partial-add case asserting exactly
`storeBatchGranularity` items in its first transaction, M26, the first
6.3e case ordered by turn waiters, and this record), on 2026-10-22: M26
survived the earlier `ShouldBeGreaterThan, 0` assertion and fails the new
one (1,000 expected, 2,000 got; 1 of 1), and M1, M21 and M23 to M25 still
fail the first 6.3e case (1 of 1 each). The write-turns tests passed 10
runs in a row with `-race` (`-count=10`), `gofmt`, `go vet ./jobqueue/`
and `make lint` (0 issues) were clean, and `make test` passed (1,004
passed, 22 skipped). The first `make race` failed one test,
`TestLostJobBehavioursActOnTheLostRun`, at a `soPathsGone` check after
`soGoneWithin` had waited only for the lost run's working directory
(`$P/make-race-lostrun-fail.log`). It passed 40 runs of 40 with `-race`
both on this tree and, alongside it, on HEAD without the item's changes,
and a rerun of `make race` passed (1,004 passed, 21 skipped). The test
and the cleanup it checks are not touched by this item; it is the
existing race recorded as delivery-queue row 14. `$P/make-test.log`
and `make-race.log` replace the earlier logs.

Benchmarks on local disk, HEAD against fix2 (the working tree; the files
label it `fix`), 6 interleaved runs each, medians, Mann-Whitney
(`$P/bench-summary.txt`, `bench-addjobs10x.txt`):

- `BenchmarkAddJobs` (3,000-job adds, the chunked path), at a fixed 10
  iterations: 191 against 115ms per add (-40%), with bolt writes and pages
  per job (1.248 and 1.245), bytes and allocations the same. Each chunk no
  longer waits out `bolt.Batch`'s 10ms `MaxBatchDelay`. At the default
  benchtime fix2 ran twice as many iterations into a larger DB, which
  made its per-job pages and allocations look 15 to 38% higher; the
  fixed-iteration run removes that.
- `BenchmarkAddDuringWriterStreams` +2.2% (p=0.08), `BenchmarkArchiveJobs`
  +2.5% (p=0.15), `BenchmarkArchiveSpacedArrivals` 0.0%: no change.

Item 6.3e's in-process probe (`TestNoHoldAddStormProbe`, `fix120k.db` on
NFS, backups on, single-job adds; `$P/p63e/`), HEAD against fix2,
interleaved:

14 rounds each (`ab2.txt`, `ab3.txt`, `stats-out-fix2.txt`), medians over
runs, Mann-Whitney:

| Metric | HEAD | fix2 | change |
| --- | --- | --- | --- |
| add mean | 136.5ms | 135.3ms | -1%, p=0.84 |
| add p50 | 51.7ms | 59.6ms | +15%, p=0.63 |
| add p99 | 1,078ms | 1,093ms | +1%, p=0.84 |
| archive p50 | 283.9ms | 288.4ms | +2%, p=0.54 |
| archive p99 | 1,337ms | 1,416ms | +6%, p=0.84 |
| archives/s | 76.2 | 75.8 | 0%, p=0.60 |

Only the add p50 median moved more than 10%, and it is not significant: the
per-run values overlap fully (HEAD 38 to 78ms, fix2 25 to 75ms in the first
8 rounds), as item 6.3d found for this two-mode figure. The mean, which 6.3d
found steadier, is 1% lower. An earlier 8-round run with item 1 only
(`ab1.txt`, `stats-out.txt`) moved every figure by 10% or less, none
significantly (add p99 -10%, p=0.79; every other figure 4% or less).

These adds are single-job adds, which never take the chunked path, and
fix2 leaves their turn first unless an add has already overtaken the oldest
waiter of another kind, so no change was expected.

What changes for users: an add of 1,000 jobs or more no longer gets every
other transaction while chunks follow one another. In `slow1` its median is
11.8s against head's 11.1s (+7%) and pre's 11.9s. Small adds keep most of
6.3e's cut at a burst (0.27s against pre's 0.59s; head 0.11s). Each turn of
the archive and best-effort writers, which make reservation, `Started` and
archive writes, now waits behind at most the transaction in flight, one add,
and one transaction of each other writer waiting (the other of those two
writers, and a chunk of each big add in progress). A single write can wait
longer: first for its own writer's transaction in flight, and behind the
transactions that take no turn (deletes, modifications, limit group changes
and other stores through `bolt.Batch`), which still queue on bolt's lock.
These writes' tail is still longer than before write turns: in `slow1`,
`Started` p99 is 15.6s against pre's 11.8s and archive p99 15.0s against
11.2s (p=0.001 each), down from head's 21.6s and 24.7s. Every fix2 run's
`Started` p99 (14.5 to 17.4s) is above every `slow1` pre run's (11.5 to
14.0s), and the archive p50 is 3.54s against pre's 3.07s (+15%, p=0.009). D1
bursts are reduced from head's but not eliminated, and not shown to differ
from pre's: 3 of 8 fix2 runs had 79 to 139 reservations handed out before
they were recorded, against 1 of 12 pre runs (113, in `slowhold` under the
same settings as `slow1`; Fisher exact p=0.26). An add whose stores panic
now fails with an error instead of exiting the manager.

Not done: no LSF rerun of the crash-pair design with the fix. The
in-process probe reproduces the D1 bursts at the LSF bursts' add latency,
and the LSF trials take 100 minutes each.

`$P` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-64b`. It holds the
probe, the hook and patch script, the run scripts (`run1.sh`, `ab2.sh`,
`queue.sh`), `fusestall-slow` and its diff, every run's output
(`probe/ab1`, `probe/slow1`, `probe/slowhold`, each with `txlog.txt` and
`timeline.txt` per run), `stats.py`, `txsum.py`, the crash-pair burst and
dump analysis in `trialdata/`, and `p63e/`. `probe/discarded/` holds runs
that overlapped another run or used a burst schedule that depended on add
speed; none are counted.

#### LSF crash-pair rerun with 6.4b (c6ee8376)

Result: the branch no longer shows the D1 bursts that item 6.4 found. No
reservation write missed `ReserveWriteWait` in steady running on either tree.
After both kinds of crash, the branch handed out fewer non-durable
reservations than the base. Doubles stay inside the injected stall, and no
job was lost. Two limits apply. First, these trials had less steady running
than item 6.4's: 3 steady portal bursts per trial (15 per tree), against 50
to 59 per tree then. Second, the branch's portal add p90 and archive p99 are
more than 10% higher than the base's in the median, though not significantly
at 5 trials per tree. So the stall and hand-out criteria pass, and the
latency-tail criterion is not shown to pass.

Setup. `$Q` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-64c`. It holds
copies of item 6.4's crash-pair scripts with only their paths changed, and
uses the same tools binaries (`prodsim`, `fusestall`, `dbstart`), fixture
(`fix120k.db`) and NFS. One addition applies to both trees alike: `trial.sh`
takes 3 goroutine dumps 3s apart, at most once per 60s, whenever the manager
log gains a D1 or "slow request" line (`profiles/stall.*`,
`stalldumps.log`). Both binaries were built fresh to `$Q/bin/<tree>/wr` with
`go build -tags netgo`: base 6f1b41bb from a scratch worktree (sha1
`31c62dfc...`) and branch c6ee8376 (`0573b589...`), both
`vcs.modified=false`. `run-all.sh` ran 10 trials, alternating base (k1, k3,
..., k9) and branch (k2, ..., k10), all with `TARGET=350 GOAL=400
MAXWAIT_MIN=60` (item 6.4's k5 to k10 settings). They ran on 2026-10-22 and
23 from 22:45 to 06:00. Every trial reached `GOAL` without waiting, so each
ran about 37 minutes: crash A at 18 minutes (RUN 451 to 457), crash B 8
minutes after restart A (RUN 451 to 468), and the end 300s after restart B.
No crash B is void.

New analysis scripts in `$Q`:

- `lat64c.py`: steady add, reserve-gap and archive latency, and throughput.
- `latency64c.py`: the soak `latency.py`, with "steady" also leaving out the
  300s after every start and the time from the stall to restart A.
- `compare64c.py`: the tables and tests below, also over item 6.4's trials.
- `analysis/bursts2.py`: `bursts.py` with one fix. It also leaves out a burst
  that starts inside the stall. `bursts.py` kept those, which in this rerun
  was every trial's fourth burst.

Outputs: `compare.txt`, `bursts2.txt`, `old-g3/bursts2.txt` (item 6.4's
trials, rerun), `latency/`, and each trial's `prodsim-*/` (`soakgate.txt`,
`cpmeasure.tsv`, `manager.log`, `profiles/`). Doubles and lost jobs come
from this worktree's `soakgate.py`, which applies the #689 killed-first-run
rule.

Per trial: D1 lines against hand-outs in the 300s after each restart, steady
D1 lines (outside the stall and both 300s windows), and soakgate's doubles
(inside the stall, outside it, acknowledged) and absent jobs:

| trial | crash A: RUN, D1 / hand-outs | crash B: RUN, D1 / hand-outs | steady D1 | doubles | absent |
| --- | --- | --- | --- | --- | --- |
| k1 base | 455, 0 / 4,201 | 454, 0 / 4,236 | 0 | 7 / 0 / 0 | 0 |
| k2 branch | 451, 0 / 4,113 | 468, 1 / 4,121 | 0 | 1 / 0 / 0 | 0 |
| k3 base | 453, 0 / 4,166 | 452, 0 / 4,293 | 0 | 3 / 0 / 0 | 0 |
| k4 branch | 457, 1 / 3,851 | 458, 2 / 4,275 | 0 | 5 / 0 / 0 | 0 |
| k5 base | 454, 95 / 3,939 | 455, 17 / 4,227 | 0 | 1 / 0 / 0 | 0 |
| k6 branch | 451, 0 / 4,202 | 451, 0 / 4,288 | 0 | 0 / 0 / 0 | 0 |
| k7 base | 457, 0 / 4,156 | 457, 4 / 4,116 | 0 | 8 / 0 / 0 | 0 |
| k8 branch | 455, 0 / 4,059 | 452, 0 / 4,265 | 0 | 5 / 0 / 0 | 0 |
| k9 base | 455, 143 / 4,041 | 453, 0 / 4,167 | 0 | 2 / 0 / 0 | 0 |
| k10 branch | 451, 70 / 4,221 | 451, 1 / 4,344 | 0 | 0 / 0 / 0 | 0 |

Every trial's soakgate reports `totals ok`.

- Steady D1 bursts: base 0 of 5 trials, branch 0 of 5. In item 6.4 the
  branch (2d5f1899) had them in 3 of 5 trials (2 of 4 from k3 on), and the
  base in none.
- Crash A: base 238 of 20,503 (1.16%, in 2 of 5 trials) against branch 71
  of 20,446 (0.35%, 2 of 5). Per-trial rates do not differ (Mann-Whitney
  p=0.68). In k5, k9 and k10 the lines came with the burst that began
  during the stall. In item 6.4 the branch had 0.31% (k3 onwards).
- Crash B: base 21 of 21,039 (0.10%, 2 of 5) against branch 4 of 21,293
  (0.02%, 3 of 5, at most 2 lines a trial). Per-trial p=1.0. In item 6.4
  the branch had 277 of 10,771 (2.57%) and the base 0.04%, from k3 on.
- Doubles: every double is inside the injected stall (base 1 to 8 per
  trial, branch 0 to 5). None is outside it or acknowledged, and no job is
  absent in any trial.

Steady portal bursts (`bursts2.py`; outside the 300s after any start, the
stall and the crashes). Per burst, the worst archive-fold `maxTx` of the
minutes it covers:

| | bursts | maxTx p50 | p90 | max | over 5s | with D1 |
| --- | --- | --- | --- | --- | --- | --- |
| base | 16 | 2.49s | 4.20s | 7.33s | 1 | 1 |
| branch | 15 | 2.82s | 4.04s | 4.16s | 0 | 0 |
| item 6.4 base | 50 | 2.06s | 2.81s | 3.87s | 0 | 0 |
| item 6.4 branch | 59 | 2.75s | 5.13s | 8.98s | 6 | 3 |

- Base against branch here: Mann-Whitney p=0.25 on `maxTx` and p=0.32 on
  the add mean.
- The base's one burst with D1 lines (k5, 17 lines) began while the manager
  was down for crash B. Its lines are crash B's window, already counted
  above. `bursts2.py` does not leave out a burst that starts during a plain
  crash's outage.
- The item 6.4 rows are item 6.4's trials run through `bursts2.py`. With
  in-stall bursts left out, base and branch still differ there (p=5e-07;
  item 6.4b gave p<0.0001 with `bursts.py`).
- This rerun's branch does not differ from item 6.4's branch on `maxTx`
  (p=0.81), but none of its bursts reached 5s.
- This rerun's base is slower at bursts than item 6.4's base (p=0.026). So
  conditions differed between the two runs, and base against branch should
  be compared within each run.

Steady latency and throughput, as the median over trials, with an exact
Mann-Whitney test over the 5 per-trial values. Portal adds are prodsim's
1,000-job adds, 24 per trial. The reserve gap runs from a runner's outcome
line to its next "reserved a job" line, in whole seconds. Archive latency
runs from the E marker to the acknowledgement (`latency64c.py`). Its outcome
stamps are whole seconds, so values under 1s are noise.

| | base | branch | change | p |
| --- | --- | --- | --- | --- |
| portal add p50 | 2.32s | 2.64s | +14% | 0.42 |
| portal add p90 | 3.28s | 6.04s | +84% | 0.095 |
| portal add max | 5.10s | 8.38s | +64% | 0.55 |
| other adds p99 | 1.65s | 1.61s | -2% | 1.0 |
| reserve gap mean | 0.39s | 0.16s | -59% | 0.008 |
| reserve gap p99 | 3s | 3s | 0% | 0.99 |
| reserve gap max | 4s | 6s | +50% | 0.21 |
| reserve gaps of 10s or more | 0 | 0 | | |
| archive p99 | 1.49s | 2.88s | +93% | 0.31 |
| archives over 5s | 6 | 9 | +50% | 0.79 |
| exit-0 ends per minute | 729 | 730 | 0% | 1.0 |
| archive-fold archives per minute | 791 | 790 | 0% | 0.52 |

Per trial, portal add p90 was base 3.25, 6.18, 3.50, 3.01 and 3.28s against
branch 4.29, 6.39, 3.32, 6.04 and 6.84s. Archive p99 was base 1.49, 3.65,
1.26, 1.50 and 1.28s against branch 2.05, 3.26, 1.29, 2.88 and 3.25s. In
item 6.4's trials from k3 on, measured the same way, archive p99 was base
1.09, 1.02, 1.41 and 0.97s against branch 4.96, 1.94, 2.84 and 2.30s. So
this run does not show that 6.4b narrowed the archive p99 gap. Item 6.4b's
`slow1` probe found a remaining gap of the same kind (archive p99 15.0s
against pre's 11.2s). The throughput figures cover about 37 minutes per
trial, so they cannot be compared with item 6.4's longer trials.

Pass criteria:

- Stalls, no worse than base by more than 10%: pass. Steady D1 bursts were
  0 against 0, reserve gaps of 10s or more 0 against 0, and burst `maxTx`
  p90 4.04s against 4.20s. This rests on 15 steady bursts per tree. At item
  6.4's branch rate (3 D1 bursts in 59 steady bursts), 15 bursts would show
  none about 46% of the time, so the steady figure alone does not prove the
  bursts gone. The crash windows, where item 6.4's branch had most of its D1
  lines, add 10 more chances per tree.
- Non-durable hand-outs: pass. The branch had 0.35% and 0.02% against the
  base's 1.16% and 0.10% (crash A and crash B).
- Latency tails: not shown. Portal add p90 (+84%, p=0.095) and archive p99
  (+93%, p=0.31) are more than 10% worse in the median, but not
  significantly. Reserve gaps are not worse (mean -59%, p=0.008; p99 the
  same). Settling this needs more trials, or longer steady running per
  trial.
- Lost jobs: pass, 0 in all 10 trials.
- Doubles outside injected stalls: pass, 0 in all 10 trials.

Goroutine dumps: the watcher took 6 to 15 dumps per trial
(`profiles/stall.*`), on slow-request and D1 lines that came mostly in the
restart windows. Neither tree had a steady-running stall, so no stall
mechanism was analysed.

Cleanup: no manager, runner, LSF job or fusestall mount was left; every
`trial.log` reports `LSF jobs left with our prefix: 0`. The trials' DBs were
deleted after `dbstart`, and the scratch worktree and build cache were
removed.

- [x] implemented
- [x] reviewed

### Item 6.4c: residual Started/archive tail

Question. Item 6.4b left `Started` and archive tails longer on c6ee8376 than
before write turns (959f3fd0) in its slow-disk probe, and the LSF rerun had
portal add p90 and archive p99 more than 10% above the base in the median.
Which of these is a regression against the base 6f1b41bb, and what causes it?

Verdict: the probe tail is a regression against the base, of about 30% on the
`Started` and archive p99 at 5MB/s writes, and of 10 to 20% on archive mean and
p99 on unthrottled FUSE. The mechanism found for the slow-disk case is the
order in which writers ask for turns after a big add's chunk, not the big
add's preference. A fix that took the preference away from large adds was
built, tested and measured: it did not cut the tail, and it was reverted. No
fix is in the tree. The LSF gaps are not shown to be the same mechanism. The
options are below.

#### Setup

`$T` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-64c-tail`. The probe is
item 6.4b's `TestZZ64bProbe`, renamed `TestZZ64cProbe`
(`$T/probe/zz_64cprobe_test.go`, not committed), with two additions: every
reservation, `Started`, archive, release and add call is logged with its start
and end (`reqlog.txt`), and each turn-taking transaction's line in `txlog.txt`
also records when its write turn was taken (`zz_64bhook.go`, `patch2.py` on
`writeturns.go`; trees without turns record the request time). It ran in
scratch detached worktrees of base 6f1b41bb, pre 959f3fd0 and head 41a75a38
(code as c6ee8376), each built once, one run at a time, the order rotated each
round (`run1.sh`, `ab2.sh`). Mann-Whitney U over per-run values (`stats.py`,
item 6.4b's, extended); medians over runs.

- `slow3`: item 6.4b's `slow1` settings (`fusestall-slow -wbps 5000000`, one
  portal burst from 30s in a 240s window, 4,000 seed jobs, ~400 runners), 8
  rounds of base, pre and head.
- `plain4`: item 6.4b's `ab1` settings (plain `fusestall` over NFS, 300s, a
  burst every 75s, 12,000 seed jobs), where a 1,000-job add takes about 2.5s,
  as in the LSF crash-pair trials. 8 rounds of base, pre, head and fix3 (the
  candidate fix below). Round 1's pre and head runs overlapped this item's
  mutation tests; the figures below are rounds 2 to 8 (7 runs each) unless
  stated, and the 8-round figures agree in direction (`stats-out.txt`,
  `stats-out-r2to8.txt`).
- `slowfix`: `slow3`'s settings, base, head and fix3. Stopped after 3 rounds
  (9 runs) once the result was clear; the killed round-4 run is
  `r4-head-killed` and is not counted.

#### What regressed against the base

`slow3`, 8 runs per tree:

| | base | pre | head | head against base | head against pre |
| --- | --- | --- | --- | --- | --- |
| `Started` p99 | 12.29s | 13.21s | 15.73s | +28%, p=0.001 | +19%, p=0.012 |
| archive p99 | 11.44s | 12.46s | 14.79s | +29%, p=0.012 | +19%, p=0.093 |
| archive mean | 3.77s | 4.43s | 4.41s | +17%, p=0.046 | 0%, p=1.0 |
| archive p50 | 2.98s | 3.87s | 3.52s | +18%, p=0.17 | -9%, p=0.75 |
| 1,000-job add p50 | 12.10s | 11.97s | 11.64s | -4%, p=0.046 | -3%, p=0.21 |
| small add p50 | 589ms | 810ms | 239ms | -59%, p=0.002 | -70%, p=0.002 |
| archives/s | 9.40 | 8.95 | 9.10 | -3%, p=0.15 | +2%, p=0.71 |
| D1 per run | 9 | 31.5 | 43.5 | p=1.0 | p=0.67 |

Base and pre do not differ significantly on any figure (every p≥0.17), as
expected: at the db layer the probe drives, they differ only by the
learned-requirements cache's extra `Get` per stat put. So item 6.4b's tail
against pre is a tail against the base too.

`plain4`, rounds 2 to 8:

| | head against base | fix3 against base |
| --- | --- | --- |
| `Started` mean | +82%, p=0.030 | +39%, p=0.035 |
| `Started` p99 | -1%, p=0.34 | -6%, p=0.95 |
| archive mean | +10%, p=0.025 | +9%, p=0.035 |
| archive p99 | +19%, p=0.021 | +15%, p=0.035 |
| reservation p99 | +14%, p=0.035 | +52%, p=0.006 |
| 1,000-job add mean | +1%, p=0.66 | -3%, p=0.34 |
| small add mean | -10%, p=0.13 | -5%, p=0.95 |

Over all 8 rounds head is also worse on archive max (3.90 against 3.32s, +18%,
p=0.009) and on best-effort transactions that waited over 2s (9.5 against 5
per run, p=0.004). Pre matches the base within 4% on every latency here except
small add p50. Archives/s are within 1% on every tree.

#### Mechanism, slow disk

`$T/analysis/reqahead.py` takes each request slower than a threshold, finds
the writer transaction that served it, splits its time into waiting for that
writer's previous transaction, the turn wait, bolt's lock and its own
transaction, and sums the transactions that ran inside its life. `slow3`,
requests over 10s, medians over runs (`slow3/reqahead-10s.txt`):

| | base | pre | head |
| --- | --- | --- | --- |
| archives over 10s per run | 120 | 174 | 232 |
| new jobs (N) time inside one | 0.10s | 0.13s | 3.31s |
| N transactions over 3s inside one | 0 | 0 | 0.49 |
| `Started` over 10s per run | 436 | 481 | 545 |
| N time inside one | 0.11s | 0.14s | 1.49s |
| N transactions over 3s inside one | 0 | 0 | 0.19 |

The long N transaction is each burst phase's last add, 618 jobs (6MB of 10KB
commands), which fits one transaction and so goes through the new jobs writer
(5 to 7s at 5MB/s). `spans.py` shows where it lands. Before write turns, when
the phase's last chunk (the 1,000-job add's live-bucket chunk, 7 to 9s) ends,
the archive and best-effort writers commit straight away and ask for their
next turns at once, about 0.2s before the 618-job add arrives, so it goes
after them. On head, the small add waiting through that chunk goes first, as
6.3e intends; the archive and best-effort writers then commit and ask for
their next turns about 0.2 to 0.4s after the chunk, by which time the 618-job
add is already waiting. It goes first in arrival order, and every archive and
`Started` queued during the chunk waits for both: 15 to 17s two-cycle spans on
head, against at most 12 to 14s on pre (`slow1`'s txlogs, the same pattern in
`slow3`'s). In `slowfix` the same sequence shows with the candidate fix:
`C(t7.3) N(small) A B N(618, waited 0.3s, t6.7) A(waited 6.8s)`.

Item 6.4b's other figures fit this: `maxTx` and the longest best-effort wait
are the same on every tree (one chunk), but two long waits in a row for the
same requests happen only on head.

Not the cause, in this probe:

- Turn handoff. The gap from a transaction's end to the next one's start, when
  the next was already waiting, is 0.1 to 0.2ms on head against under 0.1ms
  (`txkinds.py`).
- Writers taking no turn. The probe has none in steady running: every write
  goes through the archive, best-effort or new jobs writer or a chunk.
- Lost coalescing. `meanFold` is 4.15 on head against 3.90 on the base
  (p=0.49).
- The hold removal: base and pre agree, and the probe has no hold.
- The large add's preference: it waited 0.33s median on head against 1.20s on
  the base, but taking the preference away (fix3) did not change where it
  landed, since it arrives before the archive and best-effort writers ask
  again.

#### Mechanism, unthrottled FUSE

`plain4` has no N transaction over 3s, so the mechanism above cannot apply.
Requests over 2s, medians over runs (`plain4/reqahead-2s.txt`): head has about
twice as many as the base (`Started` 207 against 110, archives 202 against 140,
reservation writes 41 against 26), with more chunk time inside each slow
`Started` (1.98 against 1.67s) and more N time (0.43 against 0.28s). Big-add
chunks begun during each best-effort wait over 1s are 0.83 on head against
0.62 on pre (p=0.001). So here the excess comes with big-add chunks, which
since 6.4b take write turns, but no single ordering was isolated. Not found.

#### LSF

The LSF trials have no per-transaction log. From each trial's steady archives
(`$T/lsf/lsfslow.py`, `lsfslow.txt`, latency64c's steady definition), the
archives slower than 2s were base 67, 417, 34, 84 and 24 against branch 138,
503, 44, 283 and 335. Attributing each to the portal add it overlaps most, the
share on a burst phase's 860-job add (the new jobs writer, the long-N case
above) is 0 to 47% on the base and 30 to 48% on the branch, so the branch's
extra slow archives are not concentrated on that add. Its slow portal adds are
the 1,000-job (chunked) adds in positions 2 and 3 of a phase (medians up to
5.5 and 6.2s in k4, against 3.0 and 3.3s in base k3). This looks more like the
unthrottled probe's shape than the slow-disk one, but with 5 trials and no
transaction log it is not established. The LSF portal add p90 gap has no
in-process counterpart: in `plain4` 1,000-job adds are within 4% of the base on
every tree.

#### Candidate fix tried (fix3), reverted

An add carrying more than 1MB took its write turn as a new kind,
`writerLargeNewJobs`, with no preference, judged per add so that a fold of
many small adds kept it. Tests (`TestNoHoldWriteTurns`, two cases, and a
direct case in `TestNoHoldWriteTurnsAddOvertakesOnce`): an add over the limit
commits after a best-effort write queued before it (RED on 41a75a38: it
committed straight after the archive, 3 of 3); two folded adds under the limit
keep the preference; a large add's turn is not counted as an add's. Mutants,
each with `-race`, all killed (`$T/fix/mutants.txt`): large adds keep the
preference, judge the fold's total, no add preferred, `nextTurn` prefers large
adds, `give` counts large adds. `gofmt` and `go vet` were clean. The probe's
fix3 binary judged the fold's total, which in this probe is the same thing: no
fold holds two adds that together pass 1MB.

Result: no cut. `slowfix`, 3 runs each, fix3 against head: `Started` p99 +10%
(p=0.28), archive p99 +3% (p=0.05), D1 114 against 3 per run (p=0.18); fix3
against the base: `Started` p99 +32% (16.47 against 12.52s, p=0.05). In
`plain4` fix3 is no better than head against the base (table above), and its
reservation p99 is worse (+52% against the base, p=0.006). The change was
reverted; its diff is `$T/fix/fix-64c.diff`. With no code change left, the
`make test`, `make race`, `make lint` and benchmark gates were not run for
this item.

#### Options

1. Order turns among the writers with no preference by the age of the oldest
   request each carries, not by when the writer asked: the archive writer has
   each op's `queued` time, an add its enqueue time, a chunk the time it was
   asked for, and the best-effort writer would need the time of the first
   write queued since its last swap. In the slow-disk sequence the archive and
   best-effort writers then carry requests from before the chunk and go ahead
   of the 618-job add that arrived during their turns. Small adds keep their
   preference and every writer keeps the 6.4b bound (one turn of each other
   writer), so no accepted benefit is given up, but it is not prototyped, and
   whether it touches the unthrottled shape is unknown. Recommended next step:
   prototype it in the probe against base and head, `slow3` and `plain4`
   settings, 8 rounds each. Done in item 6.4d: it cuts the tail to the base's
   or below, but in item 6.3e's add-storm probe it gives up the 6.3e add
   benefit, so it was reverted (see item 6.4d).
2. Take the small adds' preference away right after a long transaction. This
   gives up part of the 6.3e add benefit (a trade-off for the owner).
3. Revert write turns. This gives back the 6.3e add cut and 6.4b's D1 fix (a
   trade-off for the owner).
4. Accept the tail: in the slow-disk probe about +30% on `Started` and archive
   p99 against the base with small adds 59% faster, and on unthrottled FUSE
   about +10 to 20% on archive mean and p99, with throughput unchanged.

Evidence in `$T`: `probe/` (probe, hook, patches, scripts, `slow3/`,
`plain4/`, `slowfix/` with per-run `txlog.txt`, `reqlog.txt`, `timeline.txt`,
`stats-out*.txt` and `reqahead-*.txt`), `analysis/` (`txkinds.py`, `spans.py`,
`around.py`, `reqahead.py`), `lsf/` and `fix/`.

- [x] implemented
- [x] reviewed

### Item 6.4d: order turns by oldest request

Question. Item 6.4c's option 1: does ordering write turns by the age of the
oldest request each writer carries, rather than by when the writer asked, cut
the residual `Started` and archive tail against the base 6f1b41bb, while
keeping the 6.3e add benefit, 6.4b's D1 fix and throughput?

Verdict: no, not as a whole. It cuts the tail, to below the base's in the
slow-disk probe and to the base's or below on unthrottled FUSE, keeps 6.4b's
D1 fix, and raises archive throughput in the slow-disk probe. But in item 6.3e's
add-storm probe, single-job adds become 45% slower than head on the mean
(p=0.010) and 17% slower than the base (p=0.13), where head is 19% faster than
the base (p=0.002). So it gives up the 6.3e add benefit. Per the brief, the
change was reverted and no fix is in the tree. The options are below.

#### Design tried

`$D` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-64d`.

- Each request queued for the archive or best-effort writer marks, if that
  writer has none marked, the time and the count of add turns handed out so far
  (`writeTurns.queued`, called under `arMu` and in `trackBestEffortLocked` under
  `beMu`). The writer takes that mark with its queue (`swapped`) and carries it
  into its turn. Adds and chunks are marked when they ask, so adds stay FIFO
  among themselves, and chunks FIFO in ask order.
- Writers other than the new jobs writer go in the order of their marks.
- An add goes next unless an add has gone ahead of the oldest mark among the
  other waiting writers since that mark was made. This replaces 6.4b's
  per-asking count ("since that writer began waiting") with a per-request one.
- The writer the brief asked about, one still committing when the decision is
  made: when the archive or best-effort writer's turn ends and the requests it
  has queued since it took its queue would go next, it keeps the turn for its
  next transaction ("kept"), and no other writer goes until it asks again. The
  path between its `give` and its next `take` takes only its queue mutex, the
  `db.wg` mutex and each archived job's lock (`noteArchivesWritten`). None of
  their holders waits for a write turn. A best-effort write queued after the
  final drain began marks nothing, so no turn is kept for a stopped writer.
- The new bound, which the `writeTurns` doc comment stated: a request waits
  behind at most the transaction in flight, transactions carrying older
  requests (at most two per other coalescing writer, and one chunk of each big
  add in progress), and one add. A second add can overtake it only if a turn
  ends while its writer is between its own turn and asking again.

Two variants were measured in the probes: `opt1`, without the kept turn, and
`kept`, with it (`opt1` plus `P64D_RETAIN=1` in the prototype). The prototype
with the probe hooks is `$D/probe/opt1-final-with-hooks.diff`. The
implementation, with the kept turn and the tests below, is
`$D/mut/impl-final.diff`.

#### Probe results

Item 6.4c's probe (`TestZZ64cProbe`, its hooks and run scripts, copied to
`$D/probe/` with only paths changed). Scratch detached worktrees of base
6f1b41bb, head 41a75a38 and head plus the prototype were each built once. Runs
went one at a time, with the order rotated each round. Medians over runs, and
two-sided Mann-Whitney p values (`$D/analysis/table.py`, `stats.py`).

`slow3`, 5MB/s writes, one portal burst, 8 runs per tree (`$D/probe/slow3/`):

| Metric | base | head | opt1 | kept | kept against base | kept against head |
| --- | --- | --- | --- | --- | --- | --- |
| `Started` mean | 4.31s | 4.62s | 1.24s | 1.25s | -71%, p=0.001 | -73%, p=0.001 |
| `Started` p99 | 11.93s | 16.24s | 8.52s | 8.18s | -31%, p=0.001 | -50%, p=0.001 |
| archive mean | 3.93s | 4.49s | 3.35s | 3.23s | -18%, p=0.001 | -28%, p=0.001 |
| archive p99 | 11.06s | 15.01s | 9.69s | 9.10s | -18%, p=0.001 | -39%, p=0.001 |
| reservation p99 | 8.23s | 8.22s | 8.15s | 7.83s | -5%, p=0.046 | -5%, p=0.093 |
| D1 per run | 6.5 | 2 | 0 | 0 | p<0.001 | p<0.001 |
| 1,000-job add mean | 11.99s | 11.77s | 12.73s | 12.60s | +5%, p=0.001 | +7%, p=0.002 |
| 1,000-job add max | 15.72s | 16.14s | 18.78s | 18.26s | +16%, p=0.002 | +13%, p=0.003 |
| small add p50 | 603ms | 292ms | 632ms | 587ms | -3%, p=0.53 | +101%, p=0.001 |
| small add mean | 1.70s | 1.63s | 1.88s | 1.80s | +6%, p=0.21 | +10%, p=0.002 |
| archives/s | 9.35 | 9.20 | 10.4 | 10.5 | +12%, p=0.001 | +14%, p=0.001 |

D1 per run: base 2 to 11, head 1 to 129 (one run of 129), `opt1` 0 in 7 runs
and 5 in one, `kept` 0 in all 8. In item 6.4b's `slow1`, fix2 (head's code) had
1 to 139, with 3 of 8 runs over 11.

In `slow3` small adds lose head's lead: `kept`'s small-add p50 is the base's.
`smallahead.py` (`$D/probe/slow3/smallahead.txt`) shows a small add's turn
wait is mostly the remainder of the transaction in flight, not transactions
that went ahead of it (about 0.1 per add on `opt1` against 0.05 on head). Those
transactions are longer because the probe is closed-loop: with faster
`Started` writes the runners complete 12% more jobs, so each archive fold is
larger (`meanFold` 5.6 against 4.15 on head).

`plain4`, plain `fusestall` over NFS, a burst every 75s, 8 runs per tree
(`$D/probe/plain4/`):

| Metric | base | head | opt1 | kept | kept against base | kept against head |
| --- | --- | --- | --- | --- | --- | --- |
| `Started` mean | 164ms | 263ms | 190ms | 142ms | -14%, p=0.25 | -46%, p=0.001 |
| `Started` p99 | 2.87s | 3.14s | 2.58s | 1.85s | -35%, p=0.002 | -41%, p=0.001 |
| archive mean | 368ms | 414ms | 449ms | 371ms | +1%, p=1.0 | -10%, p=0.32 |
| archive p99 | 2.69s | 3.20s | 2.98s | 2.60s | -3%, p=0.14 | -19%, p=0.005 |
| reservation p99 | 1.81s | 2.20s | 2.30s | 2.01s | +11%, p=0.059 | -8%, p=0.29 |
| reservation max | 2.88s | 3.64s | 3.39s | 3.07s | +7%, p=0.29 | -16%, p=0.009 |
| 1,000-job add mean | 2.68s | 2.45s | 2.99s | 2.82s | +5%, p=0.17 | +15%, p=0.25 |
| 1,000-job add max | 5.12s | 5.03s | 6.75s | 5.97s | +17%, p=0.027 | +19%, p=0.005 |
| small add p50 | 82ms | 36ms | 72ms | 55ms | -34%, p=0.53 | +51%, p=0.14 |
| small add mean | 403ms | 332ms | 371ms | 315ms | -22%, p=0.074 | -5%, p=0.60 |
| archives/s | 12.9 | 12.8 | 12.85 | 12.9 | 0%, p=0.70 | +1%, p=0.032 |

`opt1` without the kept turn is no better than head here, and its reservation
p99 is 27% above the base's (p=0.009). Two of its runs (rounds 2 and 4) were
much slower, while the host's load rose to 12 and 21 on 8 cores, so part of
that may be interference. Both runs are counted. The kept turn is what makes
the unthrottled shape match the base.

Item 6.3e's add-storm probe (`TestNoHoldAddStormProbe`, `fix120k.db` on NFS,
backups on, single-job adds while archives stream), 10 interleaved rounds per
tree (`$D/p63e/ab1.txt`, `stats4.py`, `stats-out.txt`):

| Metric | base | head | opt1 | kept | kept against base | kept against head |
| --- | --- | --- | --- | --- | --- | --- |
| add mean | 169ms | 136ms | 195ms | 198ms | +17%, p=0.13 | +45%, p=0.010 |
| add p50 | 9.7ms | 44ms | 62ms | 76ms | +685%, p=0.002 | +72%, p=0.11 |
| add p99 | 1,325ms | 1,148ms | 1,467ms | 1,280ms | -3%, p=0.41 | +11%, p=0.019 |
| archive p99 | 1,353ms | 1,293ms | 1,468ms | 1,359ms | 0%, p=0.82 | +5%, p=0.76 |
| archives/s | 68.5 | 77.8 | 79.7 | 76.8 | +12%, p<0.001 | -1%, p=0.76 |

Head reproduces 6.3e's add benefit (add mean -19% against the base, p=0.002;
p99 -13%, p=0.008). Both variants lose it. The cause is the per-request rule
itself: once an add has gone, the next one waits until every writer carrying a
request queued before that add has had its turn, up to two turns each of the
archive and best-effort writers, which under a continuous archive stream almost
always hold such requests. Head lets the next add go after one turn of each
writer that was waiting. That rule is also what cuts the tail, since it stops a
618-job add, or a run of adds, from overtaking the same requests twice. The
archive throughput gain over the base stays (+12%).

Against the brief's criteria:

- Tail within 10% of the base: met by `kept` in both probes. Reservation p99 at
  `plain4` is +11% (p=0.059, not significant).
- 6.4b's D1 fix kept: met (0 in every `kept` run).
- Throughput kept: met.
- 6.3e add benefit kept (small adds faster than the base): not met. In the
  6.3e probe the add mean is above the base's, and in `slow3` the small-add p50
  is the base's. At `plain4` small adds stay faster than the base, though not
  significantly.

1,000-job adds also got slower in both probes (`kept` mean +5% against the
base, max +16 to 17%), which is the 618- and 1,000-job adds no longer
overtaking older requests.

#### Tests and mutants (reverted with the change)

The implementation was built test-first in the branch worktree and then
reverted. It is kept in `$D/mut/impl-final.diff`. Tests:

- `TestNoHoldWriteTurns`, three DB-level cases built on one sequence: a big
  add's chunk held in its transaction, a coalescing writer waiting with one
  request and a second queued behind it, an add (add1) and a second big add's
  chunk (held) waiting, then a second add (add2) arriving before that chunk
  commits. They assert that an archive, and a run-state write, queued before
  add1 went ahead of them commit ahead of add2, though their writer asks after
  add2. A third archive, or change write, joins the batch while add1 is held,
  so the batch is judged by its first request. The third case asserts that
  with no add waiting, the writer with the oldest request goes before a chunk
  that asked first. All three were RED on HEAD 41a75a38 in 3 of 3 runs
  (`aged`: the archive committed after add2; `agedbe`: add2 straight after the
  chunk; `oldest`: the chunk first).
- `TestNoHoldWriteTurns`, "an archive writer whose turn ends keeps it": an
  archive queued before add1 commits straight after the archive writer's
  previous transaction, ahead of add2 that arrived during it.
- `TestNoHoldWriteTurns`, "a best-effort write queued after the writer's final
  drain began keeps no turn for it": after close, a turn is still free.
- `TestNoHoldWriteTurnsKept`, direct: a best-effort write waiting from before
  the archive's queued request goes first, and a kept turn goes to its writer
  ahead of a chunk that asked meanwhile.

Each mutant was applied alone in a scratch copy and run against the three
write-turns test functions with `-race` (`$D/mut/mut64d.py`, `mutants.txt`;
A2 rerun in `mutants-A2-rerun.txt`, since its first form did not compile). All
21 were killed:

| Mutant | Claim it breaks |
| --- | --- |
| A1 others ordered by when they asked | oldest request first |
| A2 archive batch carries no mark | archive batches carry their first request's mark |
| A3 best-effort batch carries no mark | best-effort batches carry their first request's mark |
| A4 `queued` keeps the latest request | a batch is judged by its first request |
| A5 `swapped` does not clear the mark | each batch is marked afresh |
| A6 `enqueueArchive` does not mark | archives are marked |
| A7 best-effort writes do not mark | best-effort writes are marked |
| A8 marks after the final drain began | no turn is kept for a stopped writer |
| K1 a turn is never kept | the writer whose turn ends keeps it when its requests are next |
| K2 kept whenever requests are queued | kept only when they are the oldest |
| K3 a kept turn goes to any writer | a kept turn goes to its writer |
| K4 a kept turn frees the lock | nobody else goes during a kept turn |
| K5 an add goes when only the keeper is older | the keeper's requests count against adds |
| K6, K7 holder not recorded on hand-over, or on a free take | keeping is judged for the writer whose turn ends |
| P1 FIFO | an add goes next |
| P2 no overtake rule | one add per request |
| P3 every turn counts as an add | add turns are counted |
| P4 chunk turns count as adds | chunks get no preference |
| P5 last waiting add goes | adds FIFO |
| P6 `give` not deferred | a panicking transaction gives up its turn |

`go vet ./jobqueue/` was clean, and the write-turns tests passed 3 runs in a row
with `-race`. With the change reverted, the `make test`, `make race`,
`make lint` and benchmark gates were not run.

#### Options

1. Keep head (41a75a38) and accept the tail (6.4c's option 4): in the
   slow-disk probe `Started` and archive p99 are about 30% above the base, and
   about 10 to 20% above it on unthrottled FUSE, with small adds faster than
   the base.
2. Take this change and accept the add cost: in the 6.3e probe the add mean
   goes back to about the base's (+17%, p=0.13) and stays below the base's
   p99, while every `Started`, archive and D1 figure is at or below the base's
   and archive throughput is up 12%. This is a trade-off for the owner. It is
   ready in `$D/mut/impl-final.diff`, but its gates would need running.
3. A hybrid not yet tried: keep head's per-asking overtake rule for adds that
   fit in one fold, order the other writers by their oldest request, and
   give adds over about 1MB (6.4c's fix3 limit) no preference, so that they
   are ordered by their own request's age too. In `slow3` this puts the
   618-job add behind the archive and best-effort requests queued during the
   chunk, as 6.4c's mechanism needs, while single-job adds keep 6.3e's rule.
   Whether it cuts `plain4`'s tail, where the kept turn mattered, is unknown.
   It needs the same three probes. Done in item 6.4e: in the form that also
   lets only the writers an add has overtaken go before it, it keeps 6.4d's
   tail cut and the 6.3e add benefit, and is implemented there.

Recommendation: option 3 is the only one that may keep every accepted benefit,
so prototype it next in the three probes (`slow3`, `plain4`, 6.3e add-storm)
against base and head, before choosing between options 1 and 2.

Evidence in `$D`: `probe/` (scripts, `slow3/`, `plain4/` with per-run
`txlog.txt`, `reqlog.txt`, `results.txt`, `stats-out.txt`, `table.txt`,
`slow3/smallahead.txt`, `loadavg.txt` from the middle of `plain4` on),
`p63e/` (the 6.3e probe source, `ab.sh`, `ab1.txt`, `stats4.py`,
`stats-out.txt`), `analysis/` (`table.py`, `smallahead.py`), `mut/` (the
implementation diff, the mutant script and results) and `NOTES.txt`. Nothing is
left running, no `fusestall` mount is left, and the scratch worktrees and build
cache were removed.

- [x] implemented
- [x] reviewed

### Item 6.4e: hybrid turn order

Question. Item 6.4d's option 3: keep 6.3e's rule for small adds (an add goes
next unless it has already overtaken a waiting writer since that writer
asked), order every other writer by its oldest request with 6.4d's kept turn,
and give adds over about 1MB no preference. Does that keep 6.4d's cut of the
`Started`, archive and reservation tails, 6.4b's D1 fix and throughput, and
also 6.3e's add benefit?

Verdict: yes, in the form called hyb2 below. Against the base 6f1b41bb, in
the slow-disk probe (`slow3`) every `Started` and archive tail is 14 to 30%
lower and D1 is 0; on unthrottled FUSE (`plain4`) every `Started`, archive and
reservation p99 is 12 to 41% lower; in 6.3e's add-storm probe the add mean is
15% below the base (p=0.012) and not significantly above head's (+3%,
p=0.30). Throughput is the same or higher. Two figures sit at the 10% line:
`plain4` reservation max +10% (p=0.021) and 1,000-job add max, +9% in `slow3`
(p=0.003) and +11% in `plain4` (p=0.17), while the 1,000-job add means are +4%
in `slow3` and -4% in `plain4`.
One benchmark regresses against head: `BenchmarkAddDuringWriterStreams`,
6.3e's add benchmark, is 21% slower (p=0.004), from the ordering by oldest
request among the archive and best-effort writers (see Gates and benchmarks).
The change is in the working tree, uncommitted.

#### Design

`$H` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-64e`.

The first form tried (`hyb`, `$H/hybrid-prototype.diff`) is 6.4d's
implementation (`nohold-64d/mut/impl-final.diff`) with two changes: an add
over 1MB takes its turn as a new kind, `writerLargeNewJobs`, and the small
add's test went back to 6.3e's per-asking rule (an add goes unless the
longest-waiting other writer has been overtaken by an add since it asked). In
the add-storm probe it lost the add benefit: add mean 156.9ms against the
base's 161.1ms (-3%, p=0.88) and head's 142.0ms (+11%, p=0.08), 10 rounds
(`$H/p63e/ab1.txt`). The same with the kept turn disabled (`hybnk`) was no
better (163.0 against `hyb`'s 164.8ms, 4 rounds each, `$H/p63e/ab2.txt`), so
the kept turn was not the cause. The cause: when an add was held back by a
writer it had overtaken (A), `hyb` gave the turn to the writer with the oldest
request, which could be another writer (B) that asked after the add's last
turn; then A went, and only then the add, which waited behind two turns where
head makes it wait behind one.

`hyb2`, the form measured below and implemented:

- Small adds (`writerNewJobs`: every add in the fold carries at most
  `newJobsPreferMaxBytes`, 1MB) go next unless an add has gone ahead of a
  waiting writer since that writer asked. If one has, only the writers that
  were overtaken that way may go before the add, the one with the oldest
  request first. With more than one new jobs goroutine, adds go in the order
  they asked.
- With no small add waiting, the writer carrying the oldest request goes:
  the archive and best-effort writers carry the time of the oldest request
  in the queue they took (6.4d's marks); chunks and large adds the time they
  asked, so each kind stays FIFO.
- The kept turn (6.4d): when the archive or best-effort writer's turn ends
  with no small add waiting and its newly queued requests are older than every
  waiting writer's, it keeps the turn. It never keeps it while a small add is
  waiting.
- A large add's turn is not counted as an add's turn, and nor is a chunk's.

The threshold is 6.4c fix3's: more than 1MB of encoded bytes in one add,
judged per add, so that a fold of many small adds keeps the preference. The
chunking threshold could not serve: it counts jobs (1,000 per bucket), and the
add that caused 6.4c's slow-disk mechanism, a burst phase's last add of 618
jobs with 10KB commands (about 6MB), is under it. 1MB is far above the 1.5KB
single-job adds of an add storm.

The bound, as the `writeTurns` doc comment states it: a small add waits behind
at most the transaction in flight (or, if it asks while a turn is kept, that
writer's next transaction) and one transaction of each writer it has already
overtaken. While its writer waits for a turn, a request queued for any other
writer waits behind at most the transaction in flight, transactions carrying
older requests (at most two batches per other coalescing writer, one chunk of
each big add in progress), and the small adds that go ahead of its writer: one
once its writer is waiting. While its writer is between its turn ending and
asking again (replying to the requests it has just written, then taking its
queue), the requests it has queued are invisible to the ordering: only that
writer's own keep decision, as its turn ends, sees them. Any writer waiting or
asking in that gap can go ahead of them, whatever the age of its own request,
and how many do depends on how long the gap lasts. For example, with an
archive turn in flight and an archive request R queued for its next one, a
chunk asks and then a small add; the turn ends and the add goes (no turn is
kept while an add waits); the add's turn ends before the archive writer asks,
so the chunk goes; the archive writer then asks carrying R's time and commits
after the chunk. A direct test pins this (Tests below). The first form of this
bound, in the review draft, left that gap out and was false.

A batch is one transaction unless archives in it fail. `applyFolded` then
retries the rest after each failing archive and re-runs each failed one alone,
and every attempt passes the batch's oldest request (`drainArchives`). A
batch with k failing archives takes at most 2k+1 transactions: 2k+1 when the
rest commit, 2k when every archive in it fails, and 2j+1 when the rest fail as
a whole after j failing archives have been taken out (1 if the first
transaction does). All are ordered as older than a request queued after the
batch's first; two DB-level cases pin this for a retry and a solo re-run
(review fixes (3)). The new jobs writer's retries ask with the time they ask,
so they carry no older request, and a large add's solo re-run asks as a large
add, with no preference.

Owner follow-up, not done (it changes the ordering, so it would need the three
probes again, about 7 hours): with no small add waiting, `nextTurn` could also
weigh the `queuedSince` of an archive or best-effort writer that is not the
holder and is not waiting. That would cover the reply gap above, but not the
gap after its swap, when `queuedSince` has been cleared and the writer has not
yet asked.

#### Probe results

Scratch detached worktrees of base 6f1b41bb, head 41a75a38 and `hyb2`, each
built once with item 6.4c's hooks (`$H/probe/patch.py`, `patch2.py`;
`patch3.py` for the hybrid trees). Runs went one at a time, the order rotated
each round (`$H/queue3.sh`). Medians over runs and two-sided Mann-Whitney p
(`$H/analysis/table3.py`, item 6.4d's `stats.py` and `stats4.py`). Start-of-run
load averages were 1.4 to 7.4 on 8 cores (`run.log` per run, `loadavg.txt`),
so no round was rerun.

`slow3`, 5MB/s writes, one portal burst, 8 runs per tree
(`$H/probe/slow3/`):

| Metric | base | head | hyb2 | hyb2 against base | hyb2 against head |
| --- | --- | --- | --- | --- | --- |
| `Started` mean | 4.14s | 4.56s | 1.15s | -72%, p=0.001 | -75%, p=0.001 |
| `Started` p99 | 11.80s | 15.78s | 8.29s | -30%, p=0.001 | -47%, p=0.001 |
| archive mean | 3.74s | 4.39s | 3.07s | -18%, p=0.001 | -30%, p=0.001 |
| archive p99 | 10.75s | 14.79s | 9.21s | -14%, p=0.001 | -38%, p=0.001 |
| reservation p99 | 8.18s | 8.13s | 8.19s | 0%, p=0.53 | +1%, p=0.53 |
| reservation max | 10.0s | 10.0s | 8.57s | -14%, p=0.001 | -14%, p<0.001 |
| D1 per run | 5 | 4.5 | 0 | p<0.001 | p=0.001 |
| 1,000-job add mean | 11.80s | 11.70s | 12.31s | +4%, p=0.005 | +5%, p=0.002 |
| 1,000-job add max | 15.59s | 16.48s | 17.04s | +9%, p=0.003 | +3%, p=0.046 |
| small add p50 | 548ms | 303ms | 602ms | +10%, p=0.67 | +99%, p=0.036 |
| small add mean | 1.64s | 1.54s | 1.71s | +4%, p=0.059 | +11%, p=0.002 |
| archives/s | 9.5 | 9.2 | 10.6 | +12%, p=0.001 | +15%, p=0.001 |

`plain4`, plain `fusestall` over NFS, a burst every 75s, 8 runs per tree
(`$H/probe/plain4/`):

| Metric | base | head | hyb2 | hyb2 against base | hyb2 against head |
| --- | --- | --- | --- | --- | --- |
| `Started` mean | 148ms | 189ms | 107ms | -27%, p=0.012 | -43%, p=0.001 |
| `Started` p99 | 2.80s | 2.80s | 1.65s | -41%, p=0.001 | -41%, p=0.001 |
| archive mean | 325ms | 362ms | 298ms | -8%, p=0.014 | -18%, p=0.001 |
| archive p99 | 2.52s | 2.73s | 2.21s | -12%, p=0.003 | -19%, p=0.001 |
| reservation p99 | 1.62s | 1.79s | 1.38s | -15%, p=0.012 | -23%, p=0.001 |
| reservation max | 2.60s | 2.99s | 2.87s | +10%, p=0.021 | -4%, p=0.059 |
| D1 per run | 0 | 0 | 0 | | |
| 1,000-job add mean | 2.30s | 2.23s | 2.20s | -4%, p=0.012 | -2%, p=0.40 |
| 1,000-job add max | 4.59s | 4.66s | 5.08s | +11%, p=0.17 | +9%, p=0.003 |
| small add p50 | 40ms | 39ms | 35ms | -11%, p=0.53 | -10%, p=0.21 |
| small add mean | 343ms | 325ms | 289ms | -16%, p=0.046 | -11%, p=0.13 |
| archives/s | 13.0 | 12.9 | 13.0 | 0%, p=0.064 | +1%, p<0.001 |

Item 6.3e's add-storm probe (`TestNoHoldAddStormProbe`, `fix120k.db` on NFS,
backups on), 20 interleaved rounds per tree in two runs of 10
(`$H/p63e/ab3.txt`, `ab4.txt`; pooled in `ab34.txt`, `stats-out-ab34.txt`):

| Metric | base | head | hyb2 | hyb2 against base | hyb2 against head |
| --- | --- | --- | --- | --- | --- |
| add mean | 170.6ms | 140.9ms | 145.3ms | -15%, p=0.012 | +3%, p=0.30 |
| add p50 | 12.2ms | 63.9ms | 64.8ms | +432%, p<0.001 | +1%, p=0.89 |
| add p99 | 1,249ms | 1,062ms | 1,132ms | -9%, p=0.011 | +7%, p=0.051 |
| archive p50 | 17.6ms | 295.7ms | 287.5ms | +1,534%, p<0.001 | -3%, p=0.18 |
| archive p99 | 1,272ms | 1,368ms | 1,338ms | +5%, p=0.14 | -2%, p=0.89 |
| archives/s | 67.1 | 74.0 | 75.8 | +13%, p<0.001 | +2%, p=0.25 |

The first 10 rounds alone (`ab3.txt`) gave add mean -13% against the base
(p=0.028) and +7% against head (p=0.17), and archive p99 +14% against the
base (p=0.041; head +8%, p=0.20). The second 10 were run to settle that
archive p99: in them it was +2% (p=0.65), and pooled it is +5% (p=0.14). Head's
add benefit itself was weaker in this session than in 6.4d's (add mean -10%
to -19% against the base across the four sets of rounds, `ab1.txt` to
`ab4.txt`; `stats-out.txt` and `stats-out-ab2.txt` to `stats-out-ab4.txt`), so
the comparison with head is the firmer one for the hybrid.

Against the brief's criteria:

- `Started`, archive and reservation tails within 10% of the base, or no
  significant regression: met, except `plain4` reservation max, +10% (2.87
  against 2.60s, p=0.021). Reservation p99 in the same runs is 15% lower
  (p=0.012), and max is one observation per run. In 6.4d's `plain4`, `kept`
  had reservation max +7% (p=0.29).
- D1 no worse than head: met (0 in every `hyb2` run of both probes; head had
  4.5 per run in `slow3`).
- Throughput kept: met (archives/s +12% in `slow3`, +13% in the add storm, the
  same in `plain4`).
- 6.3e add benefit kept: met in the add-storm probe (-15% against the base,
  p=0.012; +3% against head, p=0.30). In `slow3`, as with 6.4d's `kept`, small
  adds lose head's lead: their mean is 11% above head's (p=0.002) and 4% above
  the base's (p=0.059). Faster `Started` writes let the runners complete 12%
  more jobs, so archive folds are larger (`meanFold` 4.8 against the base's 3.9
  and head's 4.2) and a small add waits longer for the transaction in flight.
  In `plain4` small adds are faster than the base (-16%, p=0.046).
- 1,000-job adds not more than 10% worse than the base: met on the mean (+4%
  in `slow3`, -4% in `plain4`). Their max is +9% (`slow3`, p=0.003) and +11%
  (`plain4`, p=0.17), which is these adds no longer overtaking older requests.

#### Change

`jobqueue/writeturns.go`, `jobqueue/db.go` (working tree, uncommitted):

- 6.4d's implementation (`nohold-64d/mut/impl-final.diff`): the archive and
  best-effort writers record when the first request since their last swap was
  queued (`writeTurns.queued`, `swapped`), carry it into their turn
  (`updateInTurnSince`), and the kept turn. A request's mark is now only its
  time (`time.Time`); 6.4d's per-request add count is gone with the
  per-request add rule.
- `writerLargeNewJobs` and `newJobsWriterKind`: a new jobs transaction holding
  any add over `newJobsPreferMaxBytes` (1MB) takes its turn with no
  preference (6.4c fix3's criterion, per add).
- `nextTurn`: with a small add waiting, only the writers it has overtaken
  since they asked can go before it (`oldestOther`); with none waiting, the
  oldest request goes, or the writer whose turn ends keeps it.
- The `writeTurns` doc comment states the new rules and the bound (above). A
  panicking transaction still ends its turn (`updateInTurnSince`'s deferred
  `give`), and a chunk's panic still fails only its add (6.4b). Chunks stay
  FIFO, each taking a turn, and the per-turn bound now names the small adds
  that can pass a writer as well as the older requests.

#### Tests

`jobqueue/nohold_write_turns_test.go`. Run on HEAD 41a75a38 in a scratch copy
(the new direct tests and the final-drain case, which use the new API, taken
out, and `newJobsPreferMaxBytes` given as a test constant), six of the
DB-level cases below fail in each of 3 runs (`$H/tdd/red-head-{1,2,3}.txt`):
the large-add archive sequence ("aged"), the small-add archive sequence
("agedsmall"), the run-state sequence ("agedbe"), the oldest-request case
("oldest"), the kept case with a large add2 ("kept") and fix3's large-add case
("large"). Two pass on HEAD and are guards that only mutants kill: the kept
case's companion with a small add2 (H8, below) and the folded-adds case (H4,
P3). The run-state sequence was later made a table over the three kinds of
best-effort write (review fixes, below), after the red runs.

- 6.4d's three sequence cases with the second add made large (over 1MB): an
  archive, and a run-state write, queued while add1 went ahead of them commit
  ahead of the large add2, though their writer asks after it; on HEAD the
  archive commits after add2 and the run-state writes wait behind bigA's
  chunk. With no add waiting, the writer with the oldest request goes before a
  chunk that asked first (unchanged from 6.4d).
- "an archive queued while an add went ahead of it does not hold back a later
  small add that asks before its writer does": the same sequence with a small
  add2, which must commit straight after bigB's chunk, the archives after it.
  It fails on HEAD because there the archives also wait behind bigA's second
  chunk; it guards that small adds keep 6.3e's rule.
- "an archive writer whose turn ends keeps it ... ahead of a later large add"
  (6.4d's kept case with a large add2), and its companion with a small add2,
  which must go first (no turn is kept while a small add waits).
- 6.4c fix3's cases: an add over the limit commits after a best-effort write
  queued before it (fails on HEAD: it committed straight after the archive);
  folded adds each under the limit keep the preference although the fold is
  over it; and a small add folded with one over the limit commits after a
  best-effort write queued before them (review fixes (2)). The sized adds are
  exactly 1MB (at the limit, so preferred) and 1MB plus one byte.
- Direct `writeTurns` cases: a large add's turn, like a chunk's, does not count
  as an add's (one table over both kinds);
  `TestNoHoldWriteTurnsSmallAddsByAsking`:
  an add held back by a writer it overtook waits for that writer only, not for
  another that carries an older request but asked after the add's last turn
  (the `hyb` mechanism above); and a writer whose turn ends while an add is
  held back keeps no turn although its queued requests are the oldest.
- 6.4d's final-drain case, for the best-effort writer, is kept unchanged, and
  gains an archive-writer companion (review fixes (4), below). 6.4d's
  `TestNoHoldWriteTurnsKept`
  keeps its cases and gains three: the gap counterexample (review fixes,
  below), and a kept turn used up once and a small add waiting for the
  keeper's next transaction (review fixes (2), below).

The write-turns tests pass 5 of 5 runs with `-race`
(`$H/tdd/green-race-x5.txt`; after the review fixes, again 5 of 5,
`$H/review1/green-race-x5.txt`; after review fixes (2), 5 of 5 again,
`$H/review2/green-race-x5.txt`; after review fixes (3), 5 of 5 again,
`$H/review3/green-race-x5.txt`; after review fixes (4), 5 of 5 again,
`$H/review4/green-race-x5.txt`).

Mutants, each applied alone to a scratch copy of the final tree and run
against the four write-turns test functions with `-race`
(`$H/mut/mut64e.py`, `mutants.txt`; K1, K2 rerun in `mutants-rerun.txt`
because their first form did not compile, H1 rerun there with every failing
line). All 27 are killed. Four more, X1 to X3 and H8, were added in review and
run the same way (`$H/review1/mutx.py`, `review1/mut/`); all four are killed:

| Mutant | Claim it breaks | Killed by |
| --- | --- | --- |
| H1 small adds held back by any writer with an older request | small adds judged by when writers asked | first 6.3e case; direct overtake cases |
| H2 a held-back add lets the oldest of all others go first | an add waits only for writers it overtook | "waits for that writer only" |
| H3 large adds keep the preference | large adds get none | large sequence and kept cases; fix3 case |
| H4 the fold's total is judged | judged per add | folded-adds case |
| H5 large add turns count as adds | only small adds' turns count | "its turn does not count" (large) |
| H6 a turn is kept while an add is held back | no kept turn while a small add waits | "keeps no turn" |
| H7 large adds preferred like small ones | large adds get none | large sequence and kept cases; fix3 case |
| A1 others by ask order | oldest request first | sequence cases, oldest case |
| A2 archive batch carries no time | archive batches carry their first request's time | sequence cases |
| A3 best-effort batch carries no time | best-effort batches carry it | run-state sequence case |
| A4 `queued` keeps the latest request | a batch is judged by its first | sequence cases |
| A5 `swapped` does not clear the time | each batch is timed afresh | timeout (test binary) |
| A6 `enqueueArchive` does not mark | archives are marked | sequence and kept cases |
| A7 best-effort writes do not mark | best-effort writes are marked | run-state sequence case |
| A8 marks after the final drain began | no turn kept for a stopped writer | final-drain case |
| MA `enqueueArchive` marks before its `arStopped` check | no turn kept for a stopped writer | archive final-drain case (timeout) |
| K1 a turn is never kept | kept turn | kept case; `TestNoHoldWriteTurnsKept` |
| K2 kept whenever requests are queued | kept only when oldest | timeout (`TestNoHoldWriteTurns`) |
| K3 a kept turn goes to any writer | kept for its writer | `TestNoHoldWriteTurnsKept` |
| K4 a kept turn frees the lock | nobody else goes | `TestNoHoldWriteTurnsKept` |
| K6, K7 holder not recorded on hand-over, or on a free take | kept judged for the writer whose turn ends | kept case; `TestNoHoldWriteTurnsKept` |
| P1 FIFO | an add goes next | first 6.3e case; direct cases |
| P2 no overtake rule | one add per writer's wait | direct overtake cases |
| P3 every turn counts as an add | add turns counted | small sequence case; folded case; direct |
| P4 chunk turns count as adds | chunks get no preference | small sequence case; "its turn does not count" |
| P5 last waiting add goes | adds FIFO | "two adds" |
| P6 `give` not deferred | a panicking transaction ends its turn | the panic cases (timeout) |
| H8 a turn is kept while a small add waits and no writer it overtook does | no kept turn while a small add waits | kept case's small-add companion; first 6.3e case; `TestNoHoldWriteTurnsKept` (timeout) |
| X1 `enqueueChangeInSlotLocked` appends its db.wg key without `trackBestEffortLocked` | change writes are marked | change row of the best-effort sequence table |
| X2 among writers an add overtook, ask order instead of oldest request | the overtaken writer with the oldest request goes first | "of two writers an add went ahead of" |
| X3 `enqueueExitLocked` appends its db.wg key without `trackBestEffortLocked` | exit writes are marked | exit row of the best-effort sequence table |
| M15 a fold loses the preference only if every add in it is over 1MB | any one add over 1MB loses it | "a small add folded with one too large" |
| M7 taking a kept turn does not clear `kept` | a kept turn is used once | "a kept turn is used up once its writer takes it" |
| M3 the limit is 1MB less 200KB | the limit is 1MB | folded-adds case (1MB adds) |
| M3b an add of exactly 1MB loses the preference | more than 1MB loses it | folded-adds case |
| M3c the limit is 1MB plus 200KB | the limit is 1MB | large sequence and kept cases; fix3 cases |
| K8 a small add takes a turn kept for another writer | a small add asking while a turn is kept waits for the keeper's next transaction | "a small add asking while the archive writer keeps the turn" |
| N28 the best-effort writer never keeps a turn | the best-effort writer keeps a turn as the archive writer does | "with nobody waiting the best-effort writer keeps the turn" |
| N27a among the writers an add overtook, a chunk is skipped | a chunk an add went ahead of goes before the next add | "an add that went ahead of it waits for it ... the next time" (chunk row) |
| N27b among the writers an add overtook, a large add is skipped | a large add an add went ahead of goes before the next add | "an add that went ahead of it waits for it ... the next time" (large row) |
| N8 a failed archive's solo re-run carries no time | every attempt of a batch carries its oldest request | solo row of the failing-archive table |
| N8b every attempt after a batch's first carries no time | every attempt of a batch carries its oldest request | both rows of the failing-archive table |
| N18 a large add's solo re-run asks as a small add | a large add gets no preference | "an add too large ... re-run alone after failing" |

A5, K2 and P6 are killed only by a timeout: the writers deadlock, and the
test fails when a bounded wait expires (3 minutes, with a message naming the
wait for P6) or when the test binary's timeout panics (A5, K2), not on an
order assertion. They are killed, but a failure under them is slow and says
less about the cause than the others'.

Review fixes (reviewer FAIL on the draft):

- The bound in the `writeTurns` doc comment and in Design above was false;
  it now states the gap in which a writer's requests are invisible to the
  ordering. `TestNoHoldWriteTurnsKept` pins the counterexample: "an archive
  queued while its writer has not yet asked again is invisible to the order:
  once an add has gone, a chunk that asked after it goes first".
- `TestNoHoldWriteTurnsSmallAddsByAsking` gained "of two writers an add went
  ahead of, the one carrying the older request goes first, though it asked
  after the other" (X2).
- The run-state sequence case is now a table over the three kinds of
  best-effort write, run-state, change and exit, each queued as the first
  write of its batch (X1, X3).
- `writeTurns.take` was test-only and is gone; the tests call
  `takeSince(kind, time.Time{})`.
- The `newJobsPreferMaxBytes` comment says that at production's ~25KB
  commands an add of about 41 jobs loses the preference, and why a fold of
  many small adds, up to 32MB, keeps it.
- `updateJobAfterChangeDurable`, `updateJobRunStateDurable` and
  `updateJobAfterExitDurable` now say that their caller must not hold a job's
  lock, `db.arMu`, `db.beMu` or `db.wgMutex`, since a kept turn would then
  deadlock; the `writeTurns` comment points at them.

Each new test fails under its mutant and passes without it.

Review fixes (2) (second reviewer FAIL). M15, M7 and M3 are the reviewer's
surviving mutants; K8, M3b and M3c were added here. All six, applied one at a
time to a scratch copy of the final tree and run against the write-turns
tests with `-race`, are killed (`$H/review2/mutr2.py`, `review2/mut/`), each by
the case named in the table; without them the tests pass
(`review2/mut/green-scratch.txt`).

- `newJobsWriterKind` judged a fold as large if any add in it is over 1MB,
  as documented, but no test folded a large add with a small one, so M15
  survived. New DB-level case: "a small add folded with one too large to go
  ahead of the other writers commits after a best-effort write queued before
  them" (the small add first, so a check of only the first add also fails).
- A kept turn must be used up when its writer takes it; nothing checked that,
  so M7 (two writers holding turns at once) survived. New direct case in
  `TestNoHoldWriteTurnsKept`: keep, take, hand on to a chunk, and a second chunk
  must wait. The direct tests' queuer now fails as soon as any writer is
  handed a turn while it waits for writers to queue, so M7 and K8 fail at once
  rather than on a 3-minute wait.
- The small-add bound was false with a kept turn: an add that asks while a
  turn is kept waits for that writer's next transaction too. It is restated in
  the `writeTurns` comment and in Design, and pinned by "a small add asking
  while the archive writer keeps the turn waits for that writer's next
  transaction" (K8).
- "At most two per other coalescing writer" did not hold when archives fail:
  `applyFolded`'s retries and solo re-runs all carry the batch's oldest
  request, so k failing archives give at most 2k+1 transactions (exact
  counts in Design). The bound now counts batches and says so (comment and
  Design).
- The sized adds are exactly the size asked for, 1MB and 1MB plus one byte,
  where they were nominally 768KB and 1,088KB (a few KB more once encoded),
  which left any limit between those untested (M3).
- The durable-wait comments gave the wrong reason for forbidding
  `db.wgMutex`: no writer takes it on its way to a kept turn (the writer
  comment's "db.wg mutex" is waitgroup's own). The rule stands because each
  `queueJob*` takes `db.wgMutex` itself; the comments now say that, and
  `updateJobRunStateDurableWithin`, which also waits for a turn, states the
  rule too. The `writeTurns` lock list names `db.arMu` and `db.beMu`,
  waitgroup's own mutex, and says the archive fold stats it records on the
  way are atomics.
- The benchmark note said allocations rise with `queued`'s marks; the A1
  variant keeps the marks and allocates as head does, so it now says they
  follow time per add.

The earlier 31 mutants, rerun against the final tests the same way
(`$H/review2/mut/mut64e-rerun.txt`, `mutx-rerun.txt`): all 31 are still
killed. A5, A8, H6, K2 and P6 are killed only by a bounded wait or the test
binary's timeout; H1, K6 and P1 hit one too, besides order assertions.

Review fixes (3) (third reviewer FAIL). N28, N27a, N27b, N8 and N8b are the
reviewer's surviving mutants, N18 its non-blocking one. All six, applied one
at a time to a scratch copy of the final tree and run against the write-turns
tests with `-race`, are killed, each by the case named in the table
(`$H/review3/mutr3.py`, `review3/mut/`, `review3/mut-summary.txt`); without
them the tests pass (`review3/mut/green-scratch.txt`).

- Nothing checked that the best-effort writer keeps a turn, so N28 survived.
  New direct case in `TestNoHoldWriteTurnsKept`: a best-effort turn ends with
  a write queued and nobody waiting, a chunk asks, and the best-effort
  writer's next transaction goes first.
- Nothing checked that a chunk or a large add an add has overtaken holds back
  the next add (N27a, N27b). New direct case, a table over both kinds, in
  `TestNoHoldWriteTurnsAddOvertakesOnce`: with the chunk (or large add) and an
  archive waiting, add1 goes; then add2 asks and waits for both, the chunk (or
  large add), whose request is older, first.
- Nothing checked that a batch's retry and solo re-run carry its oldest
  request (N8, N8b). New DB-level table in `TestNoHoldWriteTurns`: a batch of
  a good archive and one whose archive panics follows a blocker; while one
  attempt is held, a chunk asks and then an add; the add goes, and the next
  attempt, the retry of the good archive (row one) or the failed archive's
  solo re-run (row two), commits before the chunk. The solo re-run rolls back,
  so its transaction id is the one the next committed transaction gets; the
  case allows for that.
- A large add's solo re-run asks as a large add (N18). New DB-level case: a
  large add whose putter fails is held in its first transaction while a chunk
  and then an archive ask; the chunk goes, the solo re-run asks during it, and
  the archive, whose request is older, commits before the re-run.
- The `writeTurns` comment said a writer keeps its turn "with no add
  waiting"; it is with no small add waiting, as the code has it.
- "k failing archives take 2k+1 transactions" is now "at most 2k+1", with the
  exact counts (comment and Design).
- Probe results gave head's add-mean benefit as -12% to -19% across the four
  sets; `ab2` has -10%. The range is now -10% to -19%, with the sets named.
- The benchmark note gave the A1 variant's allocations as means (1,102 against
  1,104) in a section of medians; the medians are 1,092.5 against 1,090.

The review-2 mutants (M15, M7, M3, M3b, M3c, K8), rerun against the final
tests the same way (`$H/review3/mut-review2-set-summary.txt`), are all still
killed.

Review fixes (4) (fourth reviewer FAIL). MA is the reviewer's surviving
mutant: `enqueueArchive` marks the archive writer's request before its
`arStopped` check. An archive offered during the final drain is then rejected
but marked, the final drain's turn is kept for an archive writer that has
exited, and every later write turn waits forever, a shutdown hang. Applied to
a scratch copy of the final tree and run against the write-turns tests with
`-race`, it is killed, by a bounded wait, with "a turn is kept for the stopped
archive writer" (`$H/review4/mutr4.py`, `review4/mut/mut-MA.txt`); without it
the tests pass (`review4/mut/green-scratch.txt`).

- The stopped-writer rule was tested only for the best-effort writer. New
  DB-level case beside it: "an archive offered during the archive writer's
  final drain is rejected and keeps no turn for it once the database is
  closed". Archive A is held in its transaction while archive B is queued as
  `enqueueArchive` queues it (with `queued`) but without waking the writer;
  the database is closed, and once the writer is told to stop A is released,
  so B rides in the final drain, during whose transaction the observer offers
  another archive. That offer must be rejected, and after close a chunk's
  `updateInTurn` must return `ErrDatabaseNotOpen` within a bounded wait.
- The `writeTurns` and `nextTurn` comments said an add goes next "unless it
  has already gone ahead"; it is unless an add has, as Design has it.
- The small-add part of the bound in the `writeTurns` comment now reads "at
  most one small add per wait".
- `writerLargeNewJobs`'s comment says `writeTurns` treats it exactly as
  `writerChunkedAdd` (no preference, ordered by when it asked, never kept),
  and that the two are separate kinds only so each call site names its
  transaction: nothing logs or labels by kind.

#### Gates and benchmarks

`gofmt`, `go vet ./jobqueue/` and `make lint` (0 issues, `$H/make-lint.log`)
are clean. `make test` and `make race` with `OS_*` unset pass: 1,006 passed
each, 23 and 22 skipped (`$H/make-test.log`, `make-race.log`). Both were run
with `HOME` set to an empty scratch directory: with the user's home, every
test that connects to a manager failed with "could not reach the server", on
HEAD 41a75a38 too (`TestJobqueueBasics`; `$H/make-test-userhome-fail.log`),
because `~/.wr_config.yml`, changed on 2026-10-23 at 13:41 outside this item,
now names `~/.config/devtls/hgi-farm.crt` as the manager's CA and
certificate. The extra skip in `make test` is the S3 tests, which need
`~/.s3cfg`.

After the review fixes the same gates, run the same way (`HOME` an empty
scratch directory, `OS_*` unset), pass again: `make lint` 0 issues, `make
race` 1,006 passed and 22 skipped, `make test` 1,006 passed and 23 skipped
(`$H/review1/make-lint.log`, `make-race.log`, `make-test.log`).

After review fixes (2), run the same way: `make lint` 0 issues, `make race`
1,006 passed and 22 skipped, `make test` 1,006 passed and 23 skipped
(`$H/review2/make-lint.log`, `make-race.log`, `make-test.log`).

After review fixes (3), run the same way: `make lint` 0 issues, `make race`
1,006 passed and 22 skipped, `make test` 1,006 passed and 23 skipped
(`$H/review3/make-lint.log`, `make-race.log`, `make-test.log`).

After review fixes (4), run the same way: `make lint` 0 issues (after a blank
line the new case lacked, `review4/make-lint-wsl-fail.log`), `make race`
1,006 passed and 22 skipped, `make test` 1,006 passed and 23 skipped
(`$H/review4/make-lint.log`, `make-race.log`, `make-test.log`).

Benchmarks on local disk, head 41a75a38 against the working tree, 6
interleaved runs each, medians, Mann-Whitney (`$H/bench.sh`, `bench.txt`,
`bench-summary.txt`):

| Benchmark | head | change | p |
| --- | --- | --- | --- |
| `BenchmarkAddDuringWriterStreams` ns/op | 11.6ms | +20.9% (14.0ms) | 0.004 |
| `BenchmarkAddDuringWriterStreams` allocs/op | 1,037 | +16.5% | 0.004 |
| `BenchmarkAddDuringWriterStreams` archives/s | 243.4 | +0.6% | 0.010 |
| `BenchmarkAddJobs` ns/op (10 iterations) | 114ms | +2.6% | 0.26 |
| `BenchmarkAddJobs` bolt writes, pages per job | 1.248, 1.245 | 0% | 1.0 |
| `BenchmarkArchiveJobs` ns/op | 262ms | +2.7% | 0.26 |
| `BenchmarkArchiveSpacedArrivals` ns/op | 2.48s | -0.1% | 0.20 |

`BenchmarkAddDuringWriterStreams` (6.3e's: single-job adds back to back while
archives with a 20ms commit cost arrive every 4ms and best-effort writes
every 2ms) is the one regression: adds 21% slower than head, though still
about 40% faster than before write turns (6.3e: 22.9 against 11.9ms). Its
allocations per op (+16.5%) follow its time per op, not `queued`'s marks: the
benchmark counts the archive and best-effort streams' allocations too, which
go on while each add waits, and the A1 variant below, which keeps the marks,
allocates as head does (medians 1,092.5 against 1,090 per op,
`bench-variants2.txt`).
Variants of the change, measured the
same way (`$H/bench-variants.txt`, `bench-variants2.txt`, 6 runs each against
head): no kept turn +16% (p=0.004); a small add taking a kept turn from its
keeper +21% (p=0.004); the other writers ordered by when they asked instead of
by their oldest request (mutant A1) +1% (p=0.87). So the cost is the ordering
by oldest request among the archive and best-effort writers: with no add
waiting, the archive writer, whose batch's first request is usually the
oldest, goes right after each add, and the next add then waits out its 20ms
commit; ordered by asking, the best-effort writer's short transaction goes
first and the next add follows it. In the add-storm probe, the same effect is
+3% against head (p=0.30) on the add mean.

Not done: `make speed` and the real-LSF scenarios (DEVELOPERS.md §5 asks for
both before a PR on this hot path).

#### Options

1. Take the change: every probe tail at or below the base, D1 0, throughput
   kept, and the add-storm add mean 15% below the base, at the cost of the
   21% slower `BenchmarkAddDuringWriterStreams` against head (adds still 40%
   faster than before write turns), `slow3`'s small adds 11% slower than
   head's, and `plain4` reservation max +10% against the base.
2. Try ordering the archive and best-effort writers by when they asked, and by
   oldest request only against chunks and large adds. That would remove the
   benchmark cost (A1 variant: +1%), but A1 also orders those two against
   chunks by asking, and whether the probe tails need the archive and
   best-effort writers ordered against each other by request is not known; it
   needs the three probes again (about 7 hours).
3. Keep head (6.4c's option 4) and accept the tail.

Recommendation: option 1, unless the owner reads
`BenchmarkAddDuringWriterStreams` as a user-visible add latency, in which case
option 2 is the next prototype.

Evidence in `$H`: `NOTES.txt`; `queue.sh`, `queue2.sh`, `queue3.sh`;
`probe/` (scripts, hooks, patches, `slow3/`, `plain4/` with per-run
`txlog.txt`, `reqlog.txt`, `run.log`, `results.txt`, `table.txt`,
`stats-out.txt`; `discarded/`, one head run from a queue stopped to add
`hybnk`); `p63e/` (the probe, `ab.sh`, `ab1.txt` to `ab4.txt`, `ab34.txt`,
`stats-out*.txt`); `analysis/table3.py`; `hybrid-prototype.diff`,
`hybnk-prototype.diff`, `hyb2-prototype.diff` (the prototypes as measured) and
`impl.diff` (the working tree's change); `tdd/`; `mut/`; `bench*`;
`make-*.log`; `loadavg.txt`.

- [x] implemented
- [x] reviewed

### Item 6.4f: speed and LSF check of 6.4e

Question. Item 6.4e left `make speed` and the real-LSF scenarios undone
(DEVELOPERS.md section 5). Does 849cc680 pass them against the base 6f1b41bb,
and does item 6.4b's LSF crash-pair rerun still pass on it?

Verdict. No correctness failure: every scenario passed by its own criteria,
and no crash-pair trial lost a job or ran one twice outside the injected
stall. `make speed` fails only on `AddJobs` per-job rows, which are a
benchmark artefact, not a regression (below). The crash-pair passes on every
criterion, now including the latency tails 6.4b left unshown. One figure is
more than 10% worse than the base at p < 0.05: the `add-storm-lsf` add p50,
+37% (p=0.007), larger than 6.3e's owner-accepted +23%. 6.3e's p90 and p99
gains over the base are gone. Against pre-6.4e c6ee8376, run in the same
session, the add p50 is +14% (p=0.024).

`$F` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-64f`. Everything ran on
farm22-wrstat01 on 2026-10-24 and 25, one run at a time, with all `OS_*`
unset. `$F/NOTES.txt` logs each step.

Setup:

- Binaries. Each tree was built from a scratch detached worktree with a
  scratch `GOCACHE`, before any run. `go version -m` shows `vcs.revision`
  6f1b41bb (sha1 `0222b496...`), 849cc680 (`e475f1fc...`) and c6ee8376
  (`74c7fca9...`), each with `vcs.modified=false`
  (`$F/g2/*-wr.buildinfo.txt`). The crash-pair used copies at
  `$F/cp/bin/<tree>/wr`. The worktree's HEAD was eb30e536, which is 849cc680
  plus a docs-only commit, so `make speed` measured the same code.
- HOME. `~/.wr_config.yml`, changed on 2026-10-23 at 13:41, after items 6.3
  and 6.4b ran, names farm TLS certificates. wr reads it after
  `WR_CONFIG_DIR`, so isolated localhost managers would use those
  certificates (item 6.4e's tests failed this way). So every run had `HOME`
  set to an empty directory, which for the LSF runs held only a symlink to
  `~/.ssh`. That matches the config the earlier runs had.

#### make speed

`make speed`, quick mode, baseline the merge-base 6f1b41bb
(`$F/speed/run-1792845435/`): FAIL on 4 rows, all from `AddJobs`:

| Row | Base | Head | Change |
| --- | --- | --- | --- |
| `AddJobs` sec/op | 177.6ms | 138.0ms | -22.3% (p=0.002) |
| `AddJobs` B/op | 22.48Mi | 29.06Mi | +29.3% (p=0.002) |
| `AddJobs` allocs/op | 139.4k | 159.9k | +14.7% (p=0.002) |
| `AddJobs` bolt_pages/job | 1.071 | 1.474 | +37.6% (p=0.002) |
| `AddJobs` bolt_writes/job | 1.073 | 1.476 | +37.6% (p=0.002) |

Other rows benchstat marks as changed: `ReadyBacklogSnapshot` sec/op -15.4%,
`UpdateJobState` B/op -2.65%, `ArchiveSpacedArrivals` B/op +1.19% and allocs
+3.12%, `ArchiveJobs` allocs +3.22%, `AddDepGroupMember` B/op +3.31% and
allocs -1.26%, `BuildSchedulerGroupsBacklog` allocs +3.92% (102 to 106), and
`QueueLifecycle` B/op +0.02%. All are under 10%.

The `AddJobs` rows are an artefact of `-benchtime=1s`. Head is faster, so the
framework ran it 15 or 16 times against the base's 7, and the benchmark adds
every batch to the same growing database, so per-job pages and bytes grow
with that count (head's 16-iteration round has 1.514 pages/job, its
15-iteration rounds 1.474). Re-runs of `AddJobs` alone, 10 rounds each:

- Against 6f1b41bb (`run-1792846338`): the same 4 rows, sec/op -22.9%.
- Against 41a75a38, pre-6.4e (`run-1792846395`): no row changed (sec/op
  -1.8%, p=0.25). So 6.4e changed nothing here.
- Against 6f1b41bb with `SPEED_BENCHTIME=10x` (`run-1792846472`): pages/job
  1.245 on both, writes 1.248 on both, B/op 25.17Mi on both, allocs 148.5k on
  both, and sec/op -38.8% (p<0.001). PASS.

The scenarios ran once each in that run. A rerun of only the scenarios, 6
rounds each, on a quiet host after the LSF work (`SPEED_BENCH='^$'
SPEED_SCENARIO_COUNT=6`, `run-1792883370`), printed PASS:

- `report-storm`: jobs/s 832 against 3,147 (+278%), wall -74%, max archive
  78 against 99ms (+27%, p=0.275, not significant).
- `dep-granularity-check`: peak RSS 284 against 299MiB (+5.2%, p=0.002),
  recovery 194 against 193ms, add 463 against 502ms (+8.5%, p=0.12).

#### G2: real-LSF scenarios

Run as in items 6.3 and 6.3e: `$F/g2/g2.sh` and `drive.sh` are copies of
`nohold-63e-lsf`'s with only paths changed. Defaults, `WRDEV_DEBUG=1`, ports
52610 to 52613, the storm modes on copies of `fix120k.db` and `pristine10`.
Each run's `.out` saves the `bjobs -w` taken just before it, and all 26 say
"No unfinished job found". Order: `add-storm-lsf` base, branch, base, branch,
base, branch, branch, base, branch, base, branch, base (r1 to r12, 13:55 to
15:00). Then `report-storm-lsf` branch, base, base, branch (r13 to r16, 15:00 to
15:21). Then 8 extra `add-storm-lsf` runs, pre-6.4e c6ee8376 and branch
alternating (r19 to r26, 15:42 to 16:25). Last, `limit-drain` base, branch
(r17 and r18, 16:25 to 17:02).

- **`add-storm-lsf`: PASS, all 20 runs.** Every run printed `PASS: all <n>
  acknowledged adds`, with `missingAcked=0` and 0 in every safety field.
  Measured as in item 6.3e (`collect.sh` gives `addstorm-runs-all.tsv`;
  `stats.py`, exact Mann-Whitney). Medians, latencies in ms:

  | Metric | base (6) | branch (10) | pre-6.4e (4) | branch against base | branch against pre-6.4e | 6.3e: branch against base |
  | --- | --- | --- | --- | --- | --- | --- |
  | add p50 | 181 | 248 | 217.5 | +37%, p=0.007 | +14%, p=0.024 | +23%, p=0.11 |
  | add p90 | 771.5 | 754.5 | 736.5 | -2%, p=0.41 | +2%, p=0.56 | -25%, p=0.04 |
  | add p99 | 1,213 | 1,174.5 | 1,102 | -3%, p=0.12 | +7%, p=0.15 | -19%, p=0.009 |
  | add mean | 301.5 | 334.5 | 304 | +11%, p=0.073 | +10%, p=0.11 | -5%, p=0.24 |
  | jobs completed | 17,293 | 27,818.5 | 29,099 | +61%, p<0.001 | -4%, p=0.19 | +32%, p=0.004 |
  | archives/min | 3,873 | 6,259 | 6,565.5 | +62%, p<0.001 | -5%, p=0.19 | +31%, p=0.004 |
  | archive `meanWait` | 90.9 | 96.2 | 89.35 | +6%, p=0.23 | +8%, p=0.058 | +8%, p=0.39 |
  | archive `meanLock` | 3.1 | 3.9 | 3.2 | +26%, p=0.017 | +22%, p=0.093 | +22%, p=0.33 |
  | archive `meanTx` | 28 | 22.25 | 18.4 | -21%, p=0.007 | +21%, p=0.004 | -14%, p=0.31 |
  | storm done at kill | 1,999.5 | 1,851.5 | 1,715.5 | -7%, p=0.056 | +8%, p=0.036 | -4%, p=0.18 |
  | kids done | 445 | 195 | 221 | -56%, p=0.003 | -12%, p=0.18 | -78%, p=0.002 |
  | `maxStatusRPC` | 198 | 191.5 | 187.5 | -3%, p=0.71 | +2%, p=0.66 | -25%, p=0.06 |

  The pre-6.4e runs came about 1.5 hours after the base's, so the
  base-against-pre-6.4e figures cross two blocks of the session. In the
  alternating block alone (r19 to r26, 4 each), branch against pre-6.4e is
  add p50 +14% (p=0.34), mean +8% (p=0.34), p99 +4% and `meanTx` +14%
  (p=0.029). Branch run r22 is an outlier: 37,457 jobs completed and add
  p50 126ms. Without it, all 3 other branch runs (246 to 256ms) are above
  all 4 pre-6.4e runs (213 to 225ms). Pre-6.4e against the base: p50 +20%
  (p=0.010), p99 -9% (p=0.010), mean +1%.

  Against the pass criterion (nothing more than 10% worse than the base,
  except accepted costs):

  - Add p50, +37% (p=0.007). This exceeds the +23% the owner accepted for
    6.3e. About +20 points of it are on pre-6.4e already, and 6.4e adds
    about +14% on top. A likely cause, not shown: the hybrid order lets an
    add overtake a waiting writer only once, while 6.4b and 6.4e raised
    throughput (archives/min +62% against the base here, +31% in 6.3e's
    session), so an add more often waits behind an archive transaction.
    The in-process add-storm probe gave 6.4e's add mean as +3% against head
    (p=0.30); here it is +10% (p=0.11). It needs an owner decision: accept,
    or try 6.4e's option 2.
  - Add mean, +11% (p=0.073). Not significant. 6.3e's mean was 5% below
    the base.
  - Archive `meanLock`, +26% (p=0.017). It is about 0.8ms per
    transaction. Since 6.3e it includes the write-turn wait. `meanWait`,
    an archive's whole wait, is +6% (p=0.23), and the branch archives 62%
    more per minute. 6.3e found the same +22%, not significant then.
  - Kids done, -56%. This is the kid ordering the owner accepted on
    2026-10-19.

- **`report-storm-lsf`: PASS, all 4 runs.** Every run printed `FULLY DRAINED
  (complete=100000 buried=0)` with `badjobDelta=0 maxDelayed=0 maxLost=0`,
  and 0 confirmed dead (`rs-summary.txt`).

  | Run | Tree | Wall | Drain | `maxStatusRPC` | Longest GAP | archive `meanWait` / `meanLock` |
  | --- | --- | --- | --- | --- | --- | --- |
  | r13 | branch | 329s | 254s | 562ms | 25s | 263 / 15ms |
  | r14 | base | 297s | 238s | 491ms | 19s | 211 / 10ms |
  | r15 | base | 323s | 254s | 627ms | 24s | 248 / 10ms |
  | r16 | branch | 313s | 231s | 434ms | 21s | 238 / 19ms |

  Wall +3.5% and drain -1% in the median. No gap is over 60s. `meanLock` is
  17 against 10ms, the same direction as above. With 2 runs per tree this
  is not tested.
- **`limit-drain`: PASS, both runs.** Both printed `FULLY DRAINED
  (complete=60000 buried=0)`, with 0 lost, 0 bad job, 0 confirmed dead and 0
  archive reject. The base's wall was 1,059s (drain 1,022s) against the
  branch's 1,105s (drain 1,049s): +4% and +3%. Status RPC median and max:
  115 and 215ms against 123 and 154ms. Each manager was stopped with
  `wrdev.sh stop` (`*.stop.out`, verified pid).

#### LSF crash-pair rerun with 6.4e (849cc680)

Run exactly as item 6.4b's rerun. `$F/cp` holds copies of `nohold-64c`'s
`trial.sh`, `run-all.sh`, `analyse-trial.sh` and analysis scripts with only
paths changed. The tools binaries (`prodsim`, `fusestall`, `dbstart`,
`psimjob.sh`) are the same files (sha1-identical), and the fixture is
`fix120k.db`. There were 10 trials: base k1, k3, k5, k7 and k9, branch k2,
k4, k6, k8 and k10, all with `TARGET=350 GOAL=400 MAXWAIT_MIN=60`, from
17:02 to 00:09. Every trial reached `GOAL` without waiting. Crash A was at
RUN 451 to 457 and crash B at RUN 451 to 465, and no crash B is void.
`compare64f.py` (`compare64c.py` with its comparison sets changed) compares
this run's base and branch, and each against 6.4b's rerun
(`cp/compare.txt`). `bursts2.py` gives `cp/bursts2.txt`.

| trial | crash A: RUN, D1 / hand-outs | crash B: RUN, D1 / hand-outs | steady D1 | doubles (inside / outside / acked) | absent |
| --- | --- | --- | --- | --- | --- |
| k1 base | 455, 0 / 4,282 | 458, 0 / 4,229 | 0 | 3 / 0 / 0 | 0 |
| k2 branch | 451, 0 / 4,268 | 451, 0 / 4,321 | 0 | 0 / 0 / 0 | 0 |
| k3 base | 454, 0 / 4,137 | 452, 0 / 4,171 | 0 | 4 / 0 / 0 | 0 |
| k4 branch | 451, 0 / 4,253 | 456, 0 / 4,141 | 0 | 1 / 0 / 0 | 0 |
| k5 base | 453, 2 / 4,280 | 465, 0 / 4,249 | 0 | 2 / 0 / 0 | 0 |
| k6 branch | 451, 0 / 4,021 | 451, 0 / 4,213 | 0 | 0 / 0 / 0 | 0 |
| k7 base | 454, 0 / 4,266 | 453, 0 / 4,263 | 0 | 2 / 0 / 0 | 0 |
| k8 branch | 456, 0 / 4,223 | 456, 0 / 4,242 | 0 | 1 / 0 / 0 | 0 |
| k9 base | 457, 0 / 4,247 | 452, 0 / 4,251 | 0 | 3 / 0 / 0 | 0 |
| k10 branch | 451, 0 / 4,138 | 451, 0 / 4,138 | 0 | 0 / 0 / 0 | 0 |

Every trial's soakgate reports `totals ok`, and every `trial.log` reports
`LSF jobs left with our prefix: 0`.

- Steady D1 bursts: 0 of 5 trials on each tree (Fisher p=1.0). Reserve gaps
  of 10s or more: 0 on each tree.
- Crash A: base 2 of 21,212 (0.01%, in 1 of 5 trials) against branch 0 of
  20,903 (0%). Pooled Fisher two-sided p=0.5, per-trial Mann-Whitney p=1.0.
  6.4b's rerun had base 1.16% and branch 0.35%, both from bursts that began
  during the stall. No trial here had one.
- Crash B: 0 of 21,163 against 0 of 21,055 (p=1.0). 6.4b's rerun had 0.10%
  and 0.02%.
- Doubles: all inside the injected stall (base 2 to 4 per trial, branch 0
  or 1), none outside it, none acknowledged. No job is absent in any trial.
- Steady portal bursts (`bursts2.py`), 15 per tree. Worst `maxTx` p50, p90
  and max: base 2.35, 2.77 and 3.35s against branch 2.19, 2.64 and 2.76s.
  None is over 5s. Mann-Whitney p=0.16 on `maxTx`, p=0.82 on add mean.

Steady latency and throughput, medians over trials, exact Mann-Whitney over 5
per tree (as in 6.4b; seconds):

| | base | branch | change | p | 6.4b rerun: base / branch |
| --- | --- | --- | --- | --- | --- |
| portal add p50 | 2.15 | 2.14 | 0% | 0.69 | 2.32 / 2.64 |
| portal add p90 | 3.16 | 3.52 | +11% | 0.087 | 3.28 / 6.04 |
| portal add max | 4.65 | 4.21 | -9% | 0.69 | 5.10 / 8.38 |
| other adds p99 | 1.295 | 0.888 | -31% | 0.008 | 1.65 / 1.61 |
| reserve gap mean | 0.37 | 0.10 | -73% | 0.008 | 0.39 / 0.16 |
| reserve gap p99 | 2 | 1 | -50% | 0.008 | 3 / 3 |
| reserve gap max | 4 | 3 | -25% | 0.17 | 4 / 6 |
| archive p50 | under 1 | under 1 | | | |
| archive p99 | 1.17 | 1.06 | -9% | 0.42 | 1.49 / 2.88 |
| archives over 5s | 2 | 2 | 0% | 0.95 | 6 / 9 |
| exit-0 ends per minute | 726 | 721 | -1% | 0.22 | 729 / 730 |
| archive-fold archives per minute | 798 | 784 | -2% | 0.28 | 791 / 790 |

The archive p50 reads -0.42 to -0.43s on every trial. Its outcome stamps
are whole seconds, so it means under 1s, as 6.4b said. `Started` latency
cannot be measured by this method: the runner logs "started executing"
before its `jstart` call and nothing after it, and the trial manager runs
without `--debug`. The only `Started` figure available is the server's
"slow request" (over 10s) for `jstart`: 0 in the windows after both crashes
in all 10 trials (`jstartMax` 0). 6.4b's rerun had 10 to 16s ones in 4
branch windows (after crash B in 3 of 5 trials, 10.1 to 15.9s, and after
crash A in one, 11.0s) and in 4 base windows (11.4 to 13.1s).

Against 6.4b's rerun (c6ee8376), branch against branch: portal add p90
-42% (p=0.048), max -50% (p=0.016), other adds p99 -45% (p=0.016), reserve
gap mean -38% (p=0.008), max -50% (p=0.008), archive p99 -63% (p=0.008).
Throughput did not change (-1%). The two bases differ too: this run's base
has archive p99 -21% (p=0.008) against 6.4b's base, so this run was calmer.
Compare base against branch within each run, as 6.4b said.

Pass criteria, as in 6.4b:

- Stalls: pass. 0 steady D1 bursts against 0, no reserve gap of 10s, burst
  `maxTx` p90 2.64 against 2.77s.
- Non-durable hand-outs: pass. Crash A 0% against 0.01%, crash B 0% against
  0%.
- Latency tails: pass, the first time this has been shown. Portal add p90
  is +11% (p=0.087, not significant) and its max -9%. Archive p99 is -9%,
  other adds p99 -31%, and reserve gaps are shorter (mean -73%, p99 -50%).
  6.4b's +84% add p90 and +93% archive p99 gaps are gone. These trials, like
  6.4b's, have 3 steady portal bursts each.
- Lost jobs: pass, 0 in all 10 trials.
- Doubles outside injected stalls: pass, 0 in all 10 trials.

Figures more than 10% worse than the base on this branch, all checks
together:

- `add-storm-lsf` add p50, +37% (p=0.007), and archive `meanLock`, +26%
  (p=0.017). Both are above.
- Not significant: crash-pair portal add p90 +11% (p=0.087),
  `add-storm-lsf` add mean +11% (p=0.073), `report-storm` max archive +27%
  (p=0.275).
- `AddJobs` B/op, allocs and pages per job in `make speed`: the
  iteration-count artefact, equal at 10 iterations.

Cleanup: no manager, runner, LSF job (the final `bjobs -w` says "No
unfinished job found"), fusestall mount or prodsim was left. The G2 work
roots (`/nfs/hgi/wr/sb10-bigdb/nohold-64f-work`, with their DB copies and
binaries) were removed once each run's outputs, manager logs and adder logs
were saved in `$F/g2/`. `analyse-trial.sh` removed the trials' DBs. The
scratch worktrees were removed, and the scratch `GOCACHE` was emptied with
`GOCACHE=<dir> go clean -cache`.

Evidence in `$F`: `NOTES.txt`; `master.sh`, `extra.sh`, `speed.sh`,
`speedconfirm.sh` and their `.out` files; `speed/run-*/` (benchstat,
regressions, raw); `g2/` (scripts, every run's `.out`, `.managerlog.gz`,
`.lostsamples` and `.adderlogs.tgz`, `addstorm-runs-all.tsv`,
`stats-out-all.txt`, `stats-out-extra.txt`, `stats-out-pre-vs-base.txt`,
`rs-summary.txt`, `ld-summary.txt`, buildinfo); `cp/` (scripts, `bin/`,
`trials/k*/prodsim-*/`, `compare.txt`, `bursts2.txt`, `latency/`,
`analysis.log`, `run-all.out`).

Owner decision 2026-10-25: the add p50 is handled by item 6.4g, and its
remaining +28% against the base is accepted there. The other figures above
(add mean, archive `meanLock`, kids done) are within accepted costs or not
significant.

- [x] implemented
- [x] reviewed

### Item 6.4g: add p50 after 6.4e

Question. Item 6.4f found the `add-storm-lsf` add p50 on 849cc680 at +37%
against the base (p=0.007), beyond the +23% the owner accepted for 6.3e, and
+14% against pre-6.4e c6ee8376 (p=0.024). Item 6.4e had traced its one
benchmark regression, `BenchmarkAddDuringWriterStreams` +21%, to the ordering
by oldest request between the archive and best-effort writers. Does 6.4e's
option 2 (order those two writers among themselves by when they asked, and by
oldest request only against chunks and large adds) bring the add p50 back
while keeping 6.4e's tail cuts?

Verdict: yes, and it is implemented (uncommitted). On real LSF the add p50 is
back at c6ee8376's level (232.5 against 232.5ms, +0%, p=0.91) and 8% below
849cc680's (p=0.017), with the add mean 12% lower (p=0.009) and p99 8% lower
(p=0.015). Against the base the p50 is still +28% (p=0.002), above the +23%
accepted level, so it passes on the criterion's second arm (not significantly
worse than c6ee8376), not the first; p90 is the base's and p99 is 12% below it
(p=0.004). In-process every `Started`, archive and reservation tail is at or
below the base's except `plain4`'s reservation max (+6%, p=0.17, not
significant), and within 4% of 849cc680's, the add-storm probe's add mean
is 19% below the base (p<0.001) and 8% below 849cc680 (p=0.013), throughput is
unchanged, and the benchmark is back at c6ee8376's speed. No other change was
needed for the add p50.

`$G` is `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-64g`. Everything ran on
farm22-wrstat01 from 2026-10-25 00:23 BST to 10:50 GMT (clocks went back at
02:00), one run at a time, with all `OS_*` unset. `$G/NOTES.txt` logs each step.

#### Design

`p2` (the prototype, `$G/p2.diff`; the implementation is the same logic with
renamed variables and the doc comment rewritten). This section says what
changed from 6.4e and why; the ordering itself is stated only in the
`writeTurns` doc comment in `jobqueue/writeturns.go`. Rule 1 to Rule 3 there
are its three bullets on what happens when a turn ends (Rule 2 holds the
keep), and Bound 1 to Bound 6 are its bounds, in the order they appear.

- Between the archive and best-effort writers (the coalescing writers), 6.4e's
  order by oldest request becomes order by when they asked (Rule 3; Bound 2
  states the exception for a kept turn). The `writeTurns` doc says why:
  ordered by time, the archive writer went straight after each add and kept
  its turn over a waiting best-effort writer, so the next add waited out a
  whole archive commit.
- Against chunks and large adds the coalescing writers are still judged by
  time, now by the oldest time of the coalescing writers `nextOther` may
  choose from (Rule 3). Judging the pair by the older of their two requests
  keeps 6.4c's guarantee for the coalescing writer that holds it: with no
  small add waiting, a chunk cannot slip in between because the other asked
  first with a younger request.
- The kept turn only where it still helps: a coalescing writer that
  `nextOther` chooses to go next now stops the keep (Rule 2; Bound 3 says when
  the other coalescing writer can still keep its turn while one waits). With
  no small add waiting the keep still protects the requests queued during a
  chunk against a later chunk or large add, as 6.4d needed for `plain4`. With
  only the other coalescing writer waiting, as in
  `BenchmarkAddDuringWriterStreams`, the archive writer's turns no longer run
  back to back while the best-effort writer waits.
- Unchanged from 6.4e: small adds' overtake-once rule (Rule 1, Bound 1),
  large adds' lack of preference, chunk turns, the per-batch request marks, a
  kept turn going to its writer alone and once, and no turn kept for a
  stopped writer. Rule 1's choice among the writers an add has overtaken did
  change: it is `nextOther`'s (Rule 3), so of the two coalescing writers the
  one that asked first goes, where 6.4e chose the older request.

Bounds: the `writeTurns` doc comment states them, as Bound 1 to Bound 6.
Since review 4 the doc comment states only bounds proved against the code:
Bounds 1 to 5 against `nextTurn`, `nextOther`, `firstCoalescing`,
`oldestUncoalesced`, `mayGoNext` and `takeSince`, and Bound 6 against
`applyFolded` and `drainArchives`; Bound 6's clause on the time each attempt
of a batch carries is pinned by the DB-level `TestNoHoldWriteTurns` cases.
The tests that pin each clause of the rules and bounds are in the clause,
mutant and killing-test table under Review fixes (12), derived from running
each mutant against each test function on its own. Against 6.4e, Bound 2
now orders the two coalescing writers by when they asked, where 6.4e ordered
them by time; Bound 1 is unchanged.

#### Benchmark screen

`BenchmarkAddDuringWriterStreams`, local disk, 12 interleaved rounds before
the LSF runs (`$G/bench-screen1.txt`): median per add c6ee8376 12.12ms,
849cc680 14.84ms, `p2` 12.09ms. So `p2` removes the 6.4e cost, as 6.4e's A1
variant did. Final benchmarks against the implementation are under Gates and
benchmarks.

#### add-storm-lsf

Run as in item 6.4f (`$G/g2/g2.sh`, `drive.sh`, `collect.sh` copied from
`nohold-64f/g2` with paths changed; `stats64g.py` is its `stats.py` with the
item's pairs). Four trees, each binary built to a fresh path from a scratch
detached worktree with a scratch `GOCACHE` (`$G/build-*.log`,
`$G/g2/*-wr.buildinfo.txt`): base 6f1b41bb, pre (c6ee8376) and 849cc680 with
`vcs.modified=false`, `p2` (849cc680 plus `p2.diff`) with
`vcs.modified=true`. `HOME` was an empty directory holding only a link to
`~/.ssh`, as in 6.4f. 6 rounds, the tree order rotated each round (r1 to r24,
00:31 BST to 01:40 GMT). Every run printed `PASS: all <n> acknowledged adds`
with `missingAcked=0`, and every run's pre-run `bjobs -w` says "No unfinished
job found". Exact two-sided permutation Mann-Whitney over 6 runs per tree.
Medians, latencies in ms:

| Metric | base | c6ee8376 | 849cc680 | p2 | p2 against base | against 849cc680 | against c6ee8376 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| add p50 | 181 | 232.5 | 251.5 | 232.5 | +28%, p=0.002 | -8%, p=0.017 | 0%, p=0.91 |
| add p90 | 733.5 | 724 | 770.5 | 736.5 | 0%, p=0.94 | -4%, p=0.37 | +2%, p=0.43 |
| add p99 | 1,251 | 1,083.5 | 1,196 | 1,105.5 | -12%, p=0.004 | -8%, p=0.015 | +2%, p=0.24 |
| add mean | 303.5 | 302.5 | 350.5 | 307.5 | +1%, p=0.46 | -12%, p=0.009 | +2%, p=0.29 |
| jobs completed | 17,401 | 27,686.5 | 27,843.5 | 27,654 | +59%, p=0.002 | -1%, p=0.70 | 0%, p=0.59 |
| archives/min | 3,895 | 6,159 | 6,186.5 | 6,248.5 | +60%, p=0.002 | +1%, p=0.31 | +1%, p=0.39 |
| archive `meanWait` | 91 | 93.6 | 99.65 | 92.9 | +2%, p=0.86 | -7%, p=0.024 | -1%, p=0.51 |
| archive `meanLock` | 3.0 | 3.35 | 3.65 | 3.35 | +12%, p=0.022 | -8%, p=0.39 | 0%, p=0.71 |
| archive `meanTx` | 28 | 21.5 | 22.1 | 20.4 | -27%, p=0.002 | -8%, p=0.006 | -5%, p=0.51 |
| storm done at kill | 2,009.5 | 1,794.5 | 1,816 | 1,771.5 | -12%, p=0.002 | -2%, p=0.49 | -1%, p=0.39 |
| kids done | 523.5 | 193 | 199 | 198.5 | -62%, p=0.002 | 0%, p=0.62 | +3%, p=0.33 |
| `maxStatusRPC` | 192 | 217 | 195.5 | 219 | +14%, p=0.18 | +12%, p=0.13 | +1%, p=0.94 |

This session reproduces 6.4f: 849cc680 against the base is add p50 +39%
(p=0.002) and mean +15% (p=0.009), and c6ee8376 against the base is p50 +28%
(p=0.002) with p99 -13% (p=0.002). `p2` matches c6ee8376 on every add figure
(all p≥0.24). Against the base:

- Add p50 +28% is above the accepted +23%, but `p2` is c6ee8376's level, the
  criterion's second arm. The remaining gap is the one 6.3e's in-process
  analysis described (writer occupancy at higher archive throughput), not this
  ordering: c6ee8376 has it too, with no oldest-request ordering at all.
- Archive `meanLock` +12% (p=0.022), about 0.35ms per transaction; 849cc680 was
  +22% (p=0.006) here and 6.4f's +26%. `meanWait`, an archive's whole wait, is
  +2% (p=0.86).
- Storm jobs done at the kill -12% (p=0.002): every no-hold tree has it here
  (849cc680 -10%, c6ee8376 -11%, both p=0.002), and `p2` against 849cc680 is
  -2% (p=0.49). It is item 6.3e's open -4% to -10% observation, now
  significant with 6 runs a tree; not from this item. Kids done -62% is the
  accepted kid ordering.

Owner decision 2026-10-25: accept the add p50 at +28% against the base
(181 to 232.5ms), the level c6ee8376 already had.

#### In-process probes

The 6.4e scripts (`run1.sh`, `ab2.sh`, `p63e/ab.sh`, hooks `patch.py`,
`patch2.py`, `patch3.py`) copied to `$G/probe/` and `$G/p63e/` with only paths
changed. One test binary per tree, built once from a scratch worktree with the
hooks (`$G/probe/binaries.sha1`). Medians over runs, two-sided Mann-Whitney
(normal approximation, 6.4d's `mwu`). Start-of-run load averages were 1.3 to
3.7 on 8 cores for `slow3` and `plain4`, and 1.6 to 9.2 for the add-storm
probe (two short spikes at round boundaries; `ab1.txt` has each run's).

Item 6.3e's add-storm probe (`fix120k.db` on NFS, backups on), 10 rounds of
base, c6ee8376, 849cc680 and `p2` (`$G/p63e/ab1.txt`, `stats-out.txt`), ms:

| Metric | base | c6ee8376 | 849cc680 | p2 | p2 against base | against 849cc680 | against c6ee8376 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| add mean | 171.2 | 139.7 | 151.2 | 138.5 | -19%, p<0.001 | -8%, p=0.013 | -1%, p=0.29 |
| add p50 | 26.0 | 80.5 | 91.8 | 78.7 | +203%, p<0.001 | -14%, p=0.049 | -2%, p=0.41 |
| add p99 | 1,204.5 | 1,034.2 | 1,110.7 | 850.9 | -29%, p=0.001 | -23%, p=0.070 | -18%, p=0.13 |
| archive p50 | 32.7 | 308.6 | 317.9 | 308.6 | +843%, p<0.001 | -3%, p=0.17 | 0%, p=0.88 |
| archive p99 | 1,300.9 | 1,306.9 | 1,254.7 | 1,156.0 | -11%, p=0.33 | -8%, p=0.50 | -12%, p=0.50 |
| archives/s | 65.3 | 70.5 | 70.8 | 70.7 | +8%, p=0.001 | 0%, p=0.85 | 0%, p=0.82 |

`slow3` (5MB/s writes, one portal burst), 8 rounds of base, 849cc680 and `p2`
(`$G/probe/slow3/`, `table.txt`; `table4g.py` is 6.4e's `table3.py` with the
trees renamed):

| Metric | base | 849cc680 | p2 | p2 against base | p2 against 849cc680 |
| --- | --- | --- | --- | --- | --- |
| `Started` mean | 4.28s | 1.17s | 1.04s | -76%, p=0.001 | -11%, p=0.046 |
| `Started` p99 | 11.84s | 8.17s | 7.98s | -33%, p=0.001 | -2%, p=0.40 |
| archive mean | 3.73s | 3.03s | 3.08s | -17%, p=0.012 | +2%, p=0.17 |
| archive p99 | 10.89s | 8.96s | 9.05s | -17%, p=0.006 | +1%, p=0.46 |
| reservation p99 | 8.05s | 8.25s | 7.91s | -2%, p=0.75 | -4%, p=0.75 |
| reservation max | 10.0s | 8.86s | 8.68s | -13%, p=0.005 | -2%, p=0.25 |
| D1 per run | 1.5 | 0 | 0 | p=0.015 | p=0.32 |
| 1,000-job add mean | 11.81s | 12.28s | 12.35s | +5%, p=0.001 | +1%, p=0.83 |
| 1,000-job add max | 15.94s | 16.92s | 16.78s | +5%, p=0.006 | -1%, p=0.83 |
| small add p50 | 521ms | 623ms | 594ms | +14%, p=0.093 | -5%, p=0.92 |
| small add mean | 1.63s | 1.74s | 1.75s | +7%, p=0.002 | +1%, p=0.83 |
| archives/s | 9.4 | 10.6 | 10.6 | +13%, p<0.001 | 0%, p=0.95 |

D1 per run: base 0 to 3, 849cc680 0 in all 8, `p2` 0 in 7 and 3 in one.

`plain4` (plain `fusestall` over NFS, a burst every 75s), 8 rounds
(`$G/probe/plain4/`):

| Metric | base | 849cc680 | p2 | p2 against base | p2 against 849cc680 |
| --- | --- | --- | --- | --- | --- |
| `Started` mean | 121ms | 102ms | 104ms | -14%, p=0.046 | +2%, p=0.56 |
| `Started` p99 | 2.46s | 1.58s | 1.63s | -34%, p=0.002 | +4%, p=0.29 |
| archive mean | 320ms | 299ms | 298ms | -7%, p=0.010 | 0%, p=0.43 |
| archive p99 | 2.40s | 2.19s | 2.19s | -9%, p=0.002 | 0%, p=0.40 |
| reservation p99 | 1.67s | 1.46s | 1.44s | -14%, p=0.027 | -1%, p=0.83 |
| reservation max | 2.73s | 2.87s | 2.88s | +6%, p=0.17 | +1%, p=0.92 |
| D1 per run | 0 | 0 | 0 | | |
| 1,000-job add mean | 2.27s | 2.25s | 2.19s | -3%, p=0.21 | -3%, p=0.13 |
| 1,000-job add max | 4.67s | 5.09s | 5.01s | +7%, p=0.14 | -2%, p=0.35 |
| small add p50 | 38ms | 39ms | 25ms | -34%, p=0.021 | -36%, p=0.005 |
| small add mean | 342ms | 307ms | 282ms | -18%, p=0.003 | -8%, p=0.027 |
| archives/s | 13.0 | 13.0 | 13.0 | 0%, p=0.063 | 0%, p=0.32 |

Against the brief's criteria:

- `add-storm-lsf` add p50 within +23% of the base, or not significantly worse
  than c6ee8376's level: met on the second arm (0% against c6ee8376, p=0.91;
  +28% against the base, p=0.002). p90 and p99 not more than 10% worse than the
  base: met (0% and -12%).
- In-process `Started`, archive and reservation tails within 10% of the base or
  not significantly worse: met. Every one is at or below the base's except
  `plain4` reservation max, +6% (p=0.17).
- D1 no worse than 849cc680: met in `plain4` (0 everywhere); in `slow3` one
  `p2` run of 8 had 3 against none for 849cc680 (p=0.32, not significant;
  the base's runs had 0 to 3).
- Throughput kept: met (archives/s equal to 849cc680's in all three probes,
  archives/min +1% on LSF).
- 6.3e add-storm probe add mean significantly below the base: met (-19%,
  p<0.001).

Against 849cc680 the only significant changes are improvements: LSF add p50,
p99 and mean, archive `meanWait` and `meanTx`; the add-storm probe's add mean
and p50; `slow3`'s `Started` mean; `plain4`'s small adds.

Other cheap changes considered for the add p50, not tried, because `p2` already
reached c6ee8376's level and the rest of the gap to the base predates 6.4e:
letting a small add take a turn kept for another writer (6.4e measured it as no
help in the benchmark, and the kept turn now arises with the other coalescing
writer waiting only as Bound 3 in the `writeTurns` doc allows), and dropping
the kept turn when nobody waits (it guards the queued requests against a chunk
asking in the gap, which `plain4` needed in 6.4d).

#### Change

`jobqueue/writeturns.go` (working tree, uncommitted; `$G/impl.diff`):
`oldestOther` becomes `nextOther`, which implements Rule 3 of the `writeTurns`
doc, choosing among the coalescing writers with `firstCoalescing` and among
chunks and large adds with `oldestUncoalesced`, and says whether it chose a
coalescing writer. `mayGoNext` holds the filter both helpers use for which
writers count with a small add waiting (Rule 1), and `writerKind.coalescing`
names the two coalescing writers. `nextTurn` implements Rules 1 and 2. New in
its keep is the condition that `nextOther` chose no coalescing writer, so a
coalescing writer can keep its turn while the other coalescing writer waits,
when a chunk or large add older than that writer's request is chosen (Rule 2
has every condition). The split into helpers is for `make lint` (`gocognit`,
`nestif`); the behaviour is the prototype's, which the probes measured. The
`writeTurns`, `nextTurn` and `nextOther` doc comments state the new order;
only the `writeTurns` doc states why the coalescing writers go by asking, and
only it states Bounds 1 to 6. `db.go` is unchanged.

#### Tests

`jobqueue/nohold_write_turns_test.go`, red first on the 849cc680 code
(wr-nohold HEAD eb30e536, 3 of 3 runs, 6 failing assertions each,
`$G/tdd/red-cur-{1,2,3}.txt`):

- DB-level, `TestNoHoldWriteTurns`: "with no chunk or large add waiting, a
  best-effort write waiting when an archive commits goes ahead of the archive
  queued for the archive writer's next transaction, though that was queued
  first". On 849cc680 the archive
  writer kept its turn and the second archive committed straight after the
  first.
- New `TestNoHoldWriteTurnsCoalescingByAsking` (direct), with no add
  waiting: of the two coalescing writers the one that asked first goes though
  the other carries the older request; the coalescing writers go ahead of a
  chunk that asked before both, in the order they asked, when the one that
  asked last carries a request older than the chunk's; and a large add older
  than both goes first. A best-effort writer, the only writer waiting when the
  archive writer's turn ends, goes next, so the archive writer keeps no turn,
  though its queued archive is older.
- `TestNoHoldWriteTurnsSmallAddsByAsking`: 6.4e's X2 case ("of two writers an
  add went ahead of, the one carrying the older request goes first") now says
  the opposite for the two coalescing writers ("the one that asked first goes
  first, though the other carries the older request"), and gains a companion
  that keeps X2's claim where it still holds: of a chunk and an archive an add
  went ahead of, the archive goes first when it carries the older request,
  though it asked after the chunk.
- Added after the mutant run (H2 survived, below): "with only a chunk an add
  went ahead of and an archive that asked after that add's turn waiting, the
  next add goes after the chunk and before the archive, though the archive
  carries an older request". 6.4e's case for this claim used
  two coalescing writers, which are now ordered by asking, so it could no
  longer tell the rule apart from H2. It is a guard: it passes on 849cc680 and
  on the change, and fails under H2.
- Added in review 1, in `TestNoHoldWriteTurnsCoalescingByAsking`'s block
  with an archive writer's turn in flight and an archive queued: "with no add
  waiting, the archive writer keeps its turn while a best-effort writer waits,
  when a chunk that asked after the archive was queued, and before the
  best-effort writer asked, would go next" (the archive writer's next
  transaction, then the chunk, then the best-effort writer), and "a failing
  archive batch's retries keep the turn kept at the first attempt's end, and
  the next batch takes it, while only a chunk and a best-effort writer wait"
  (until review 5 its name also said the
  retries "do not take the archive queue"; the retry, the solo re-run, the
  next batch's first transaction, then the chunk, then the best-effort
  writer). They pin the keep rule as the code has it,
  which the first version of this record and of the doc comments misstated.
  Both pass on the change. Against the tests as they were before them, G8 (the
  reviewer's mutant: no turn kept while a coalescing writer waits) and G9
  (taking a kept turn clears its writer's queued time, so retries stop keeping
  it) both survived (`$G/review1/pre-review-tests-G8-G9.txt`); with them G8
  fails both cases and G9 the second (`$G/review1/red-new-tests.txt`).
- Added in review 2, in `TestNoHoldWriteTurnsCoalescingByAsking`: "a turn
  kept at the end of a failing archive batch's first attempt with nobody
  waiting is decided again at the retry's end: a best-effort writer, the only
  writer to ask during the retry, goes before the solo re-run" (in the block
  with an archive writer's turn in flight and an archive queued), and, in a new
  block with a failing batch's first attempt in flight carrying an old request
  and no archive queued since the swap, "each chunk of one big add, the only
  writer waiting as an attempt ends, goes before the next attempt, though the
  batch's request is older than every chunk's" (attempt, chunk 1, retry,
  chunk 2, solo re-run).
  They pin the two behaviours the review 2 counterexamples showed the doc
  comment had wrong; both pass on the change. Each is red under a mutant that
  makes the old wording true (G10, G11 below), run alone in a scratch test
  holding only these two cases (`$G/review2/zz_review2_focus_test.go.txt`,
  `new-cases-focused.txt`), since G11 also hangs an earlier case of the same
  test and so hides the later ones in the full run.
- Added in review 3, in `TestNoHoldWriteTurnsSmallAddsByAsking`: "a
  best-effort writer asking after an add went ahead of a chunk, with the next
  add waiting as that add's turn ends, goes after the chunk and that next add,
  though its request is older than the chunk's" (a chunk waits, add 1 goes
  ahead of it, then a best-effort writer carrying an older request and add 2
  ask: chunk, add 2, best-effort), the review 3 counterexample; and "an
  archive asking after an add went ahead of a chunk and a best-effort writer,
  with the next add waiting as that add's turn ends, goes after both and that
  next add, though its request is older than both of theirs" (a chunk and a
  best-effort writer wait, add 1 goes ahead of both, then an archive
  carrying an older request and add 2 ask: chunk, best-effort, add 2,
  archive), a second counterexample to the old wording, found while checking
  it. In `TestNoHoldWriteTurnsCoalescingByAsking`: "of a waiting large add and
  chunk, the one with the older request goes first". All three pass on the
  change. The first has the H2 guard's order with a best-effort writer for the
  archive, stated as the bound's case. Against review 2's tests G13 and G14
  survived the direct write-turns tests and G12 and G15 did not; with the new
  cases G12 and G15 fail the first, G12 and G14 the second and G13 the third
  (`$G/review3/new-mutants-focused.txt`; review 2's tests are
  `pre-review3-tests.go.txt`).
- Added in review 4, in `TestNoHoldWriteTurnsCoalescingByAsking`: "a
  best-effort writer chosen to go next keeps the archive writer from keeping
  its turn though a chunk waits too" (in the block with an archive writer's
  turn in flight and an archive queued at q: a chunk waits, then a
  best-effort writer whose request is between q and the chunk's: best-effort,
  archive, chunk), and, in a new block with an archive writer's turn in flight
  and a chunk waiting, "an archive queued after the chunk asked is younger
  than the chunk's request, so the chunk goes when the turn ends, though a
  best-effort writer that asked later waits too, the only other writer
  waiting" (chunk, best-effort, archive). They pin the keep rule's "nextOther chose no coalescing writer"
  and "older than the chosen chunk's" conditions, which R3 and R5 (the fourth
  reviewer's mutants) broke without failing any test. Each starts the archive
  writer's next ask right after the turn ends, so under the mutant the kept
  turn goes to it at once and the case fails on its first order assertion
  rather than on a wait. New `TestNoHoldWriteTurnsFailingBatchAttempts`
  drives `applyFolded` directly and pins the attempt counts the doc comment
  gives (2k+1 for k failing ops when at least one op does not fail, 2k when
  every op fails, 1 when the first transaction fails as a whole). All pass on the change.
- Added in review 5, for doc comment rules no test pinned. In
  `TestNoHoldWriteTurnsCoalescingByAsking`'s block with a big add's chunk in
  flight: "with no add waiting, the pair is judged by the older request either
  carries, though the first to ask carries it" (a chunk waits, then an archive
  carrying an older request asks, then a best-effort writer: archive, chunk,
  best-effort); and two ties, "with no add waiting, a coalescing writer
  carrying a request exactly as old as a waiting chunk's goes ahead of it" and
  "of a waiting chunk and large add whose
  requests are exactly as old, the one that asked first goes first". In its
  block with an archive writer's turn in flight and an archive queued: "the
  archive writer keeps no turn for queued requests exactly as old as a waiting
  chunk's". In `TestNoHoldWriteTurnsKept`, a new block with a best-effort
  writer's turn in flight and an archive queued for the archive writer: "the
  best-effort writer's keep is decided on its own queued requests: one queued
  after a waiting chunk asked keeps no turn, though the archive queued is
  older than the chunk's request" (chunk, then best-effort). These five are
  the reviewer's scratch tests (`$G/review5/zz_review5_test.go.txt`) in the
  file's style. DB-level, in `TestNoHoldWriteTurns`: "with only a chunk
  waiting that asked after it was queued, an archive queued during a failing
  batch's first attempt keeps the turn for the archive writer at each
  attempt's end, so the next batch, which carries it, commits ahead of the
  chunk". It is new, for V5, which no direct test can
  reach as it changes `db.go`. All six pass on the change, and each fails
  under its own mutant on its own line and nowhere else
  (`$G/review5-fix/red-new-cases.txt`).

The red runs used the new cases as first written; the later rewrite of some
assertions into single `give`/`noHoldWTNextTurn` steps (for `make lint`) does
not change what they assert.

All of 6.4e's other cases pass with their logic unchanged (some renamed in
reviews 8 and 11). The write-turns tests pass 5 of 5
runs with `-race` (`$G/tdd/green-race-x5.txt`).

Mutants (`$G/mut/mut64g.py`, `mutants.txt`): each applied alone to a scratch
copy of the final tree and run against the write-turns tests with `-race`; the
script marks a mutant whose text does not match exactly once, or that does not
compile, INVALID. The seven new ones (G1 to G7) cover this item's claims; 6.4e's
44 are re-expressed against the new code where it changed (H1, H2, H6, H7, A1,
K1, K2, P2, X2, N27a, N27b). A first pass on the prototype-shaped code
(`$G/mut/pass1/`) had 48 killed, H2 surviving and G2 and A1 invalid (unused
variables); that led to the H2 test above. On the final tree, the unmodified
tests pass and all 51 are killed. Review 1 added G8 and G9 and reran the whole
set of 53 against the final tests (`$G/review1/mut64g-r1.py`, the same script
with `nextOther`'s renamed index; `$G/review1/mutants.txt`): the unmodified
tests pass and all 53 are killed. Review 2 added G10 and G11 and reran the whole set
of 55 against the final tests (`$G/review2/mut64g-r2.py`, review 1's script
with the two added; `$G/review2/mutants.txt`): the unmodified tests pass and
all 55 are killed. Review 3 added G12 to G15 and reran the whole set of 59
against the final tests (`$G/review3/mut64g-r3.py`, review 2's script with the
four added; `$G/review3/mutants.txt`): the unmodified tests pass and all 59
are killed. Review 4 added R1 to R5 (the fourth reviewer's) and F1 and
reran the whole set of 65 against the final tests (`$G/review4/mut64g-r4.py`,
review 3's script with the six added; `$G/review4/mutants.txt`): the
unmodified tests pass and all 65 are killed. Review 5 added V1 to V5, V7,
V11, V15 and V19 (the fifth reviewer's) and reran the whole set of 74 against
the final tests (`$G/review5-fix/mut64g-r5.py`, the reviewer's
`$G/review5/mut64g-r5.py` unchanged; `$G/review5-fix/mutants.txt`): the
unmodified tests pass and all 74 are killed.

| Mutant | Claim it breaks | Killed by |
| --- | --- | --- |
| G1 coalescing writers ordered among themselves by oldest request (6.4e's order) | the coalescing writers go by asking | `CoalescingByAsking` "with no add waiting, of the archive and best-effort writers the one that asked first goes, though the other carries the older request", "with no add waiting, the archive and best-effort writers go ahead of a chunk that asked before both, in the order they asked, when the one that asked last carries a request older than the chunk's" and "with no add waiting, a large add whose request is older than both of theirs goes ahead of the archive and best-effort writers"; `SmallAddsByAsking` "of the archive and best-effort writers an add went ahead of, the one that asked first goes first, though the other carries the older request" (`$G/review12/mutants12b.txt`) |
| G2 a turn kept although `nextOther` chose a coalescing writer | no kept turn when a coalescing writer would go next | the DB-level case; `CoalescingByAsking` keep case |
| G8 no turn kept while a coalescing writer waits (the review 1 counterexample) | a turn is kept over a waiting coalescing writer when an older chunk or large add would go next | both review 1 cases |
| G9 taking a kept turn clears its writer's queued time | a failing batch's retries keep the turn | the review 1 retry case |
| G10 a turn kept at one attempt's end stays kept at each later one while no small add waits (review 2) | the keep is decided again at each attempt's end | the review 2 retry case (best-effort before the solo re-run) |
| G11 an archive turn ending with nothing queued since the swap is kept against younger chunks for the batch's own request (review 2) | a chunk waiting as an attempt ends goes before the next attempt | the review 2 chunk case alone; in the full run the test binary's timeout, as earlier cases hang on the kept turn |
| G12 under the small-add rule a coalescing writer the add has not gone ahead of counts too (the reviewer's I11) | a request queued for a coalescing writer waits behind the chunks and large adds that asked before its writer did, whatever their age | both review 3 bound cases; 6.4e's overtake cases |
| G15 with a small add held back, a coalescing writer the add has not gone ahead of goes before a chunk or large add it overtook that carries a younger request (review 3) | as G12 | the first review 3 bound case; the H2 guard |
| G14 under the small-add rule the requests of coalescing writers the add has not gone ahead of count against chunks and large adds (review 3) | as G12 | the second review 3 bound case |
| G13 the chunk or large add with the youngest request goes first (the reviewer's I12) | chunks and large adds go oldest first | the review 3 large-add-and-chunk case |
| R3 `nextOther` reports a coalescing choice only when no chunk or large add waits (review 4) | no turn is kept when `nextOther` chose a coalescing writer | the review 4 case "a best-effort writer chosen to go next keeps the archive writer from keeping its turn" |
| R5 the keep compares the holder's queued requests with the first-asking coalescing writer's time, not the chosen chunk's (review 4) | a turn is kept only for requests older than the chosen chunk's or large add's | the review 4 case "an archive queued after the chunk asked is younger than the chunk's request" |
| R1 no turn is kept when nobody waits (review 4) | a writer with requests queued keeps its turn when nobody waits | `TestNoHoldWriteTurnsKept` |
| R2 the coalescing pair is judged by the younger of their times (review 4) | the pair is judged by the older | "go ahead of a chunk that asked before both, in the order they asked" |
| R4 chunks and large adds ignore the overtaken filter (review 4) | with a small add waiting only writers an add has gone ahead of go | "an add that went ahead of it waits for it" (`AddOvertakesOnce`); `TestNoHoldWriteTurnsKept`'s case of an archive queued while its writer has not yet asked again; a bounded wait in `TestNoHoldWriteTurns` |
| F1 a failed op is not re-run alone (review 4) | a batch with k failing archives takes up to 2k+1 transactions | `TestNoHoldWriteTurnsFailingBatchAttempts`; a bounded wait in `TestNoHoldWriteTurns` |
| V1 the pair is judged by the last-asking coalescing writer's time (review 5) | the pair is judged by the older of their times, whichever asked first | the review 5 case "the pair is judged by the older request either carries" |
| V2 a chunk or large add whose time ties with the pair's goes first (review 5) | a chunk or large add goes ahead of the coalescing writers only with a time older than theirs | the review 5 case "exactly as old as a waiting chunk's goes ahead of it" |
| V3 a turn is kept for queued requests whose time ties with the chosen chunk's (review 5) | a turn is kept only for requests older than the chosen chunk's or large add's | the review 5 case "keeps no turn for queued requests exactly as old as a waiting chunk's" |
| V4 of chunks and large adds whose times tie, the last to ask goes first (review 5) | chunks and large adds go in the order they asked | the review 5 case "of a waiting chunk and large add whose requests are exactly as old" |
| V5 each attempt after a failing batch's first clears the archive writer's queued time (`db.go`, review 5) | the keep counts every request queued since the writer took its queue, at each attempt's end too | the review 5 DB-level case |
| V11 the best-effort writer keeps its turn for the archive writer's older queued requests (review 5) | a writer's keep is decided on the requests queued for it | the review 5 `Kept` case |
| V7, V15, V19 (review 5) | a writer asking after an add's turn is not overtaken by it; no turn is kept with nothing queued; a chunk's time is when it asked | already killed before review 5 (V7 by `AddOvertakesOnce` and DB-level cases, V15 by the test binary's timeout on a kept turn, V19 by DB-level order assertions) |
| G3 against chunks only the first-asking coalescing writer's request counts | the pair is judged by the older of their requests | "go ahead of a chunk that asked before both, in the order they asked" |
| G4 coalescing writers always before chunks and large adds | the oldest request goes first against chunks | the chunk and large-add rows of `TestNoHoldWriteTurnsAddOvertakesOnce`; a bounded wait in `TestNoHoldWriteTurns` |
| G5 chunks and large adds always before coalescing writers | the oldest request goes first against chunks | 6.4e's DB-level cases (a best-effort write before a big add's first transaction; the aged-archive and run-state sequences) |
| G6 kept whenever no coalescing writer waits, whatever the chunks' ages | kept only when older than the waiting chunks and large adds | a bounded wait in the large-add sequence case |
| G7 the last-asking coalescing writer goes first | the first to ask goes first | the folded-adds case; a bounded wait |
| A1 every writer other than a small add by ask order | oldest request against chunks | 6.4e's sequence cases |
| H2 a held-back add lets the next of all others go first | an add waits only for writers it overtook | the case added after the mutant run |
| H1, H6, H7, K1, K2, P2, N27a, N27b (re-expressed) | as in 6.4e | as in 6.4e |
| X2 (re-expressed: of the writers an add overtook, a coalescing writer and a chunk by asking) | the overtaken writer with the oldest request goes first, against chunks | "of a chunk and an archive an add went ahead of" |
| the other 33 of 6.4e's (H3 to H5, H8, A2 to A8, MA, K3, K4, K6 to K8, M7, N28, P1, P3 to P6, X1, X3, M15, M3, M3b, M3c, N8, N8b, N18) | as in 6.4e | as in 6.4e |

G4, G7, H1 and P1 also hit a bounded wait (3 minutes) besides order
assertions; among others, MA, A5, A8, K2, H6 and P6 are killed only by a bounded
wait or the test binary's timeout, as several were in 6.4e. That was in these
earlier full `-race` runs of the whole write-turns test set; for the result of
each test function run alone, see the matrix in Review fixes (12).

#### Gates and benchmarks

`gofmt` and `go vet ./jobqueue/` are clean, and `make lint` reports 0 issues
(`$G/gates/make-lint.log`). The first `make lint` failed on the first form of
`nextOther` (cognitive complexity 21, nested blocks) and on repeated label
strings and a missing blank line in the new tests
(`$G/gates/run1-before-lint-fix/`); the helpers above and assertions rewritten
as single `give`/`noHoldWTNextTurn` steps fixed them, and the write-turns tests
were rerun 5 of 5 with `-race` on the final tree
(`$G/tdd/green-race-x5-final.txt`).

`make race` and `make test`, run as the brief gives them (`HOME` an empty
scratch directory, `OS_*` unset), each report one failure, `TestLimiter` in
`limiter` (line 739, a `time<` limit an hour ahead is refused;
`$G/gates/make-race.log`, `make-test.log`). It is not this change: the base
6f1b41bb and 849cc680 fail it the same way today, and it passes with `TZ=UTC`.
These runs were on 2026-10-25, the day Europe/London's clocks went back, and the
host's zone is Europe/London; the cause in the test or the limiter was not
traced. With `TZ=UTC` both pass: `make race` 1,007 passed and 22 skipped, `make
test` 1,007 passed and 23 skipped (`make-race-tzutc.log`,
`make-test-tzutc.log`). 6.4e had 1,006; the one more is
`TestNoHoldWriteTurnsCoalescingByAsking`; review 4 added
`TestNoHoldWriteTurnsFailingBatchAttempts`, so later runs count 1,008. The
local-time runs print only the
failure, not counts, so they do not show the rest passed; the `TZ=UTC` runs do.

Benchmarks, local disk, 6 interleaved rounds, medians, Mann-Whitney
(`$G/bench.sh`, `bench.txt`, `benchsum.py`, `bench-summary.txt`):

| Benchmark | c6ee8376 | 849cc680 | 6.4g | against 849cc680 | against c6ee8376 |
| --- | --- | --- | --- | --- | --- |
| `BenchmarkAddDuringWriterStreams` ns/op | 11.84ms | 13.71ms | 11.86ms | -13.5%, p=0.004 | +0.2%, p=0.87 |
| `BenchmarkAddDuringWriterStreams` allocs/op | 1,060 | 1,192 | 1,060 | -11.0%, p=0.004 | +0.1%, p=1.0 |
| `BenchmarkAddDuringWriterStreams` archives/s | 243.8 | 244.4 | 243.8 | -0.2%, p=0.064 | 0%, p=1.0 |
| `BenchmarkAddJobs` ns/op (10 iterations) | | 113.7ms | 115.8ms | +1.8%, p=0.52 | |
| `BenchmarkAddJobs` bolt pages, writes per job | | 1.245, 1.248 | 1.245, 1.248 | 0% | |
| `BenchmarkArchiveJobs` ns/op | | 267.5ms | 266.3ms | -0.5%, p=1.0 | |
| `BenchmarkArchiveSpacedArrivals` ns/op | | 2.475s | 2.474s | 0%, p=0.63 | |

So 6.4e's one benchmark regression is gone, and the other writer benchmarks are
unchanged (bolt transactions, pages and writes per job identical).

The 6.4f crash-pair was not rerun. The change orders write turns among
writers that already take them; it does not move any write into or out of a
turn, change which writes are durable before a reply, or touch reservation,
run-state or recovery code. The crash-pair checks durability ordering
(hand-outs that must not survive a crash undone); its latency side (reserve
gaps, archive tails) is what the three probes and `add-storm-lsf` measured.

Not done: `make speed` (DEVELOPERS.md section 5) and `report-storm-lsf` and
`limit-drain` on LSF; 6.4f ran them on 849cc680, and this item changes only
which of two waiting writers goes first.

Evidence in `$G`: `NOTES.txt`; `master.sh` and `master.out`; `p2.diff`;
`env.sh`; `bench-screen1.txt`, `benchscreen.sh`; `g2/` (scripts, every run's
`.out`, `.managerlog.gz`, `.lostsamples`, `.adderlogs.tgz`, `addstorm-runs.tsv`,
`stats-out.txt`, buildinfo); `p63e/` (`ab1.txt`, `stats-out.txt`, scripts);
`probe/` (scripts, hooks, `table4g.py`, `slow3/`, `plain4/` with per-run
`txlog.txt`, `reqlog.txt`, `run.log`, `results.txt`, `table.txt`); `tdd/`;
`mut/`; `gates/`; `bench*`.

#### Review fixes (1)

The reviewer returned FAIL: the documented keep rule was false. With the
archive writer's turn ending while its queued requests date from t1, a chunk
waiting since t2 and a best-effort writer since t3 (t1 before t2 before t3),
`nextOther` chooses the chunk, so `nextTurn` keeps the turn for the archive
writer while the best-effort writer waits. The code was kept, as measured;
the documents now state what it does, and tests pin it.

- The keep rule is restated exactly in the `writeTurns` and `nextTurn` doc
  comments, `TestNoHoldWriteTurnsCoalescingByAsking`'s doc and the Design
  section: the holder keeps its turn when no small add waits, it has requests
  queued since it took its queue, `nextOther` chooses no coalescing writer
  (nobody waits, or a chunk or large add carries a request older than every
  waiting coalescing writer's), and the queued requests are older than that
  chunk's or large add's. The "order they asked" rule between the coalescing
  writers now names the keep as its exception, and the "other cheap changes"
  paragraph no longer says a turn is kept only with the other coalescing
  writer not waiting.
- The bound for a request queued for a coalescing writer is two transactions
  of the other coalescing writer besides the one in flight, not one, and the
  failure path no longer says a waiting best-effort writer always goes before
  each retry: retries do not take the archive queue, so a kept turn stays kept
  for every remaining attempt and the next batch's first transaction (up to
  2k+1 archive transactions and one more). Fixed in the doc comment and in the
  Bounds section.
- Two tests pin the keep and the retry chain (Tests); G8, the reviewer's
  mutant, and G9 survived the earlier tests and are killed by them. The whole
  set of 53 mutants was rerun and all are killed.
- The verdict no longer says every in-process tail is at or below the base's:
  `plain4`'s reservation max is +6% (p=0.17). The benchmark table's third data
  column is headed 6.4g. Item 6.4f's `jstart` sentence now counts 6.4b's one
  branch window after crash A (11.0s).
- Code, behaviour unchanged: `nextOther`'s index is renamed `first`, so
  `coalescing` names only `nextTurn`'s bool. `mayGoNext`'s
  `turn.kind != writerNewJobs` stays: it is not redundant, since
  `oldestUncoalesced` skips only coalescing writers itself and relies on it to
  skip small adds; without it `nextOther` could choose a small add. Its doc
  comment is reworded.
- Gates (`$G/review1/`): the first `make lint` flagged two repeated label
  strings (`goconst`) in the new cases' final order assertions, which were
  then written as single `give`/`noHoldWTNextTurn` steps, asserting the same
  order (`run1-before-lint-fix/`). On the final tree `make lint` reports 0
  issues; the write-turns tests pass 5 of 5 runs with `-race`
  (`green-race-x5.txt`); the 53 mutants were run again against the final tests,
  the unmodified tests passing and all 53 killed (`mutants.txt`; the run
  before the lint fix is in `mut-run1-before-lint-fix/`, also 53 of 53); and
  with `TZ=UTC` `make race` reports 1,007 passed and 22 skipped and `make
  test` 1,007 passed and 23 skipped (`make-race-tzutc.log`,
  `make-test-tzutc.log`; `TestLimiter` still fails in local time on every
  tree around the clock change, as above).

#### Review fixes (2)

A second reviewer returned FAIL on two documented bounds of a failing archive
batch's retries, both false. The code was kept, as measured; the doc comment
and the Bounds section now state what it does, and two direct tests pin it.

- "While no small add waits a turn kept at the end of one attempt is kept at
  the end of each later one" was false: the keep is decided again at each
  attempt's end. With the first keep made with nobody waiting, a best-effort
  writer that asks during the retry is chosen by `nextOther` at the retry's
  end, so no turn is kept and it goes before the solo re-run. The doc comment
  and the Bounds section now give the rule exactly as the code has it: the
  archive writer's queued requests do not change across the attempts and a
  chunk or large add asking meanwhile carries a later request, so a keep is
  repeated at the next attempt's end unless a small add waits then, or the
  best-effort writer, not waiting at the keep, has asked since with a request
  no younger than every waiting chunk's and large add's (any request, with
  none waiting). The "waits behind every remaining attempt" consequence is
  restated for the case where it holds: from an attempt's end at which a chunk
  or large add waits with a request older than the waiting best-effort
  writer's but younger than the archive writer's queued requests, while no
  small add asks.
- "One chunk of each big add in progress" was false while a batch retries:
  each attempt asks only after the one before it has ended, and the keep looks
  at the archive writer's queued requests (`queuedSince`), not the batch's own
  oldest request, so a chunk waiting as an attempt ends goes before the next
  attempt (attempt, chunk 1, retry, chunk 2, solo re-run: 2k or more chunks of
  one big add with k failing archives). The first bound now excepts a
  retrying batch, the gap paragraph counts the moment between attempts as a
  gap in which the batch's requests are invisible too, and the retry
  paragraph says the chunks and large adds waiting as each attempt ends can go
  before the next attempt, with no limit of one per big add.
- Tests (Tests above): the two review 2 cases. G10 (a turn kept at one
  attempt's end stays kept at each later one while no small add waits) makes
  the first false sentence true and fails the first case; G11 (an archive turn
  ending with nothing queued since the swap is kept against younger chunks for
  the batch's own request) makes the "one chunk per big add" bound true for a
  retrying batch and fails the second. G2 also fails the first. Run alone in a
  scratch test holding only the two cases, G10, G11 and G2 fail them and G6 and
  G9 do not (`$G/review2/new-cases-focused.txt`; same cases, same setup,
  `zz_review2_focus_test.go.txt`). On the final code the reviewer's
  `TestRev64gChunksBetweenAttempts` passes and `TestRev64gCounterexamples`,
  which asserts the two old claims, fails on both (`reviewer-repro.txt`).
- Not done (optional, behaviour-identical): making `oldestUncoalesced` skip
  small adds itself. `mayGoNext` already skips them for both callers, and
  review 1 kept it there; a second check would duplicate it, and dropping
  the first would re-express a dozen mutants keyed on `mayGoNext`'s text.
- Gates (`$G/review2/`, `gates.sh`): `make lint` reports 0 issues
  (`make-lint.log`); the write-turns tests pass 5 of 5 runs with `-race`
  (`green-race-x5.txt`); the unmodified tests pass and all 55 mutants are
  killed (`run-mutants.sh`, `mutants.txt`); and with `TZ=UTC` `make race` reports
  1,007 passed and 22 skipped and `make test` 1,007 passed and 23 skipped
  (`make-race-tzutc.log`, `make-test-tzutc.log`; the count is unchanged, as
  the new cases are in an existing test function).

#### Review fixes (3)

A third reviewer returned FAIL on one documented bound, false on the
small-add path. The code was kept, as measured; the doc comment and the
Bounds section now state what it does, and direct tests pin it.

- "A request queued for a coalescing writer waits behind the chunks and large
  adds that asked before the oldest request either coalescing writer
  carries" was false. A chunk waits, add 1 goes ahead of it, then a
  best-effort writer carrying a request older than the chunk's asks, and
  add 2: at the next turn only the writers add 1 went ahead of may go, so the
  chunk goes, then add 2, then the best-effort writer, though the chunk asked
  after the best-effort writer's request was queued. This is the order the
  H2 guard already pinned (with an archive). Checking the other bound
  sentences for the same small-add path found a second case under the same
  sentence: with a chunk and a best-effort writer both overtaken by add 1, and
  the best-effort writer's request younger than the chunk's, the chunk goes
  first though an archive that asked after add 1's turn carries a request
  older than the chunk's, so the archive's request waits behind it. The bound
  is now "the chunks and large adds that asked before its writer did, whatever
  the age of their requests", and the doc comment and the Bounds section say
  why that holds on both paths: with no small add waiting a chunk or large add
  goes first only with a request older than every waiting coalescing
  writer's; with one waiting, one the add went ahead of goes first when the
  writer is not among those the add went ahead of (it began waiting before
  the add's turn, which came before the writer asked) or when its request is
  older than every coalescing writer's among them. The other bounds hold on
  that path, rechecked against `nextTurn`, `nextOther`, `firstCoalescing`
  and `oldestUncoalesced`: with a small add waiting, a writer the add went
  ahead of passes one it did not go ahead of only if it asked first, and the
  writers it went ahead of go among themselves in the order used with no small
  add waiting. A writer that asked before a waiting chunk or large add is one
  the chunk bound allows (a chunk or large add that asked before it, or a
  coalescing writer carrying an older request), and one that asked before a
  waiting coalescing writer is the other coalescing writer's "asked before
  it" transaction. So the chunk bound, the "two more transactions of the other
  coalescing writer" and the small-add bound are unchanged.
- Tests (Tests above): the two bound cases and "of a waiting large add and
  chunk, the one with the older request goes first", which asserts directly
  what G13 (the reviewer's I12) broke and earlier only a bounded wait caught.
  G12 (the reviewer's I11) and G15 make the old wording true for the first
  case and fail it; G14 makes it true for the second and fails it. The
  reviewer's `TestRev64g3YoungerChunkAheadOfCoalescing`, which asserts the
  old bound, fails on the final code (`reviewer-repro.txt`).
- `TestNoHoldWriteTurnsSmallAddsByAsking`'s doc comment states a
  consequence of the bound for when another small add waits (narrowed in
  review 6, Review fixes (6) below).
- Gates (`$G/review3/`, `gates.sh`): `make lint` reports 0 issues
  (`make-lint.log`); the write-turns tests pass 5 of 5 runs with `-race`
  (`green-race-x5.txt`); the unmodified tests pass and all 59 mutants are
  killed (`mutants.txt`; G12 and G13 also hit a bounded wait in
  `TestNoHoldWriteTurns`); and with `TZ=UTC` `make race` reports 1,007 passed
  and 22 skipped and `make test` 1,007 passed and 23 skipped
  (`make-race-tzutc.log`, `make-test-tzutc.log`; the count is unchanged, as
  the new cases are in existing test functions).

#### Review fixes (4)

A fourth reviewer returned FAIL on one more false bound on the failing
archive batch path, the fourth round in a row to find one in the derived
bounds prose, and on two keep conditions no test pinned. The code was kept,
as measured. Rather than patch the sentence again, the `writeTurns` doc
comment and the Bounds section above were rewritten to state the ordering
rules exactly as `takeSince`, `nextTurn`, `nextOther`, `firstCoalescing`,
`oldestUncoalesced` and `mayGoNext` implement them, and only bounds that
follow from those rules in a step or two and that a direct test pins.

- False: "once a chunk or large add waits as an attempt ends with a request
  older than the waiting best-effort writer's but younger than the archive
  writer's queued requests, and while no small add asks, the best-effort
  writer waits behind every attempt of the batch still to run and the first
  transaction of the archive writer's next batch". With an older chunk W
  waiting too, no turn is kept, W goes, and the best-effort writer goes before
  the retry (the reviewer's `TestRev64g4OlderChunkBreaksBEWaitsBehindAttempts`:
  W, best-effort, retry, then the kept turn). Removed, with the rest of the
  retry chain.
- Dropped as intricate, though not shown false: the per-wait counts for a
  request queued for a coalescing writer ("at most the transaction in flight,
  two more transactions of the other coalescing writer ..., one chunk of each
  big add in progress, ... at most one small add per wait") and for a chunk or
  large add ("at most two batches of each with older requests and ... at most
  one batch of the other carrying only later requests"); the retry chain ("a
  turn kept at one attempt's end is kept again at the next one's end unless
  ...", "one big add's chunks can alternate with the attempts ..., 2k of them
  or more"); and the enumeration of what makes up the gap between a
  coalescing writer's turn and its next ask. Their replacements are weaker
  and say who can go ahead, not how many: while a coalescing writer waits,
  every writer that goes ahead of it asked before it did, is a small add, or
  is the other coalescing writer keeping its turn; a coalescing writer goes
  ahead of a waiting chunk or large add, with no small add waiting, only while
  a waiting coalescing writer carries a time no younger than its own or by a
  keep for older requests; and while a failing batch retries each attempt
  asks afresh, so these hold per attempt, not per batch, and any waiting
  writer (small adds, the best-effort writer, chunks and large adds whatever
  their times) can go between attempts.
- Minor findings: the "queued requests stay the same across the attempts"
  justification (true only once a request is queued) and "a turn kept with
  nobody waiting goes, as the next attempt ends, to a best-effort writer that
  asked in the meantime" (false if a small add also asked) went with the
  retry chain; "the writers that add has gone ahead of" is now "a writer an
  add has gone ahead of". The small-add bound is restated for a small add
  that asks while no other small add waits, and includes the small adds that
  asked before it, which the old wording left to a separate sentence.
- `nextTurn`'s and `nextOther`'s doc comments say the same as the rules (with
  a small add waiting no turn is kept; the keep compares with the chosen chunk's
  or large add's time only when one was chosen). The Design section's pair
  guarantee is qualified "with no small add waiting", and its keep bullet now
  reads "when a chunk or large add was chosen". `db.go`'s comments make no
  ordering or bound claims beyond `writeTurns`' and are unchanged.
- Tests (Tests above): the two review 4 keep cases and
  `TestNoHoldWriteTurnsFailingBatchAttempts`. Against the tests as they were
  before them R3 and R5 survive (`$G/review4/pre-review4-tests-R3-R5.txt`);
  with them each fails on an order assertion in its own case, the R3 case's
  "best-effort" and the R5 case's "chunk", after 0.06s
  (`new-cases-R3-R5.txt`, both runs). F1 fails the attempt-count test.

Superseded by the clause, mutant and killing-test table under Review fixes
(12), which review 12 showed some of these rows overstate; kept as the record
of this round. Each rule and bound in the doc comment, and the test that pins
it (test functions shortened: `AddOvertakesOnce`, `Kept`, `CoalescingByAsking`,
`SmallAddsByAsking`, `FailingBatchAttempts`, all `TestNoHoldWriteTurns*`;
"DB-level" is a case of `TestNoHoldWriteTurns`):

| Sentence | Test |
| --- | --- |
| The sentence before the rules, on asking for a turn | `Kept` "with nobody waiting the archive writer keeps the turn, and a chunk asking meanwhile waits for it", "a kept turn is used up once its writer takes it" |
| Rule 1: a turn ending with a small add waiting | `AddOvertakesOnce` "an add that keeps arriving goes ahead of each of them only once"; DB-level "an archive writer whose turn ends with a small add waiting does not keep" and `Kept` "an archive queued while its writer has not yet asked again counts only in that writer's keep" (no turn kept) |
| Rule 2: a turn ending with no small add waiting, and the keep | `Kept` both "with nobody waiting ... keeps the turn" cases, the review 5 best-effort case (its own queued requests); `CoalescingByAsking` "a best-effort writer, the only writer waiting when the turn ends, goes next", the review 1 keep case, both review 4 cases, the review 5 tie case (requests as old as the chunk's keep no turn); at a failing batch's attempts: `CoalescingByAsking` review 1 retry case, review 2 "decided again at the retry's end", DB-level review 5 "an archive queued during a failing batch's first attempt keeps the turn" |
| Rule 3: `nextOther` | `CoalescingByAsking` "with no add waiting ... the one that asked first goes", "go ahead of a chunk that asked before both, in the order they asked", the review 5 "the pair is judged by the older request either carries", "a large add whose request is older than both", "of a waiting large add and chunk"; ties: the review 5 "exactly as old as a waiting chunk's goes ahead of it" and "of a waiting chunk and large add whose requests are exactly as old" |
| The sentence after the rules, on a kept turn | `Kept` "a small add asking while the archive writer keeps the turn waits for that writer's next transaction", "a chunk asking meanwhile waits for it" |
| Bound 1, small adds | `AddOvertakesOnce` all three blocks; `Kept` small-add case; `SmallAddsByAsking` "with only an archive an add went ahead of and a best-effort writer that asked after that add's turn waiting, the next add goes after the archive and before the best-effort writer" |
| Bound 2, order within each kind | `CoalescingByAsking` "the one that asked first goes", "of a waiting large add and chunk", the review 5 tie "of a waiting chunk and large add whose requests are exactly as old"; `SmallAddsByAsking` "of the archive and best-effort writers an add went ahead of, the one that asked first goes first"; `AddOvertakesOnce` "the first add goes next, then the archive, ahead of the second add"; the keep exception: the review 1 keep case |
| Bound 3, a waiting coalescing writer | `SmallAddsByAsking` both review 3 cases; `CoalescingByAsking` "a large add whose request is older than both", the review 5 "the pair is judged by the older request either carries", the review 1 keep case, the review 4 best-effort case |
| Bound 4, a waiting chunk or large add | `CoalescingByAsking` "go ahead of a chunk that asked before both, in the order they asked", the review 5 "the pair is judged by the older request either carries" and "exactly as old as a waiting chunk's goes ahead of it", the review 4 chunk case; `Kept` "a chunk asking meanwhile waits for it" |
| Bound 5, requests queued between asks | `Kept` "an archive queued while its writer has not yet asked again counts only in that writer's keep" |
| Bound 6, a failing batch's transaction counts | `FailingBatchAttempts` |
| Bound 6, who goes between attempts | review 2 "a best-effort writer, the only writer to ask during the retry, goes before the solo re-run" and "each chunk of one big add, the only writer waiting as an attempt ends, goes before the next attempt"; small adds by rule 1 (DB-level no-keep case), with no retry-specific case |

- Mutants: the whole set of 65 (review 3's 59, R1 to R5 and F1) rerun
  against the final tests: the unmodified tests pass and all 65 are killed
  (`$G/review4/mutants.txt`).
- Gates (`$G/review4/`, `gates.sh`): `make lint` reports 0 issues
  (`make-lint.log`; a first run, `make-lint-run1-err113.log`, flagged two
  `err113` dynamic errors in the new test, made package-level); the write-turns
  tests pass 5 of 5 runs with `-race` (`green-race-x5.txt`); and with `TZ=UTC`
  `make race` reports 1,008 passed and 22 skipped and `make test` 1,008 passed
  and 23 skipped (`make-race-tzutc.log`, `make-test-tzutc.log`; one more than
  review 3, the new `TestNoHoldWriteTurnsFailingBatchAttempts`).

#### Review fixes (5)

A fifth reviewer returned FAIL only on rules no test pinned: it found no
false sentence, and its mutants V1 to V5 and V11 survived every test
(`$G/review5/mutants-r5.txt`), each killed by a scratch test asserting a
sentence of the doc comment (`$G/review5/zz_review5_test.go.txt`,
`mutants-r5-vs-scratch-tests.txt`). The code was kept, as measured.

- V1 (the coalescing pair judged by the time of the last of them to ask) and
  V11 (the best-effort writer's keep decided on the archive writer's queued
  requests): two cases pin Rule 2 and Rule 3 where they had no test (Tests
  above).
- V5 (each attempt after a failing batch's first clears the archive writer's
  queued time, in `db.go`): Bound 6's clause "and does not take the archive
  queue, so the keep at each attempt's end is decided by the rule above on
  the archive writer's queued requests, not the batch's" is deleted from the
  `writeTurns` doc comment and from the Bounds section, since Rule 2 states
  the keep for every turn's end, an attempt's included. The table row that
  pinned it is gone; its tests now pin Rule 2. No other current sentence says
  it: `TestNoHoldWriteTurnsCoalescingByAsking`'s doc comment no longer says
  the attempts "do not take the archive queue", and the review 1 retry case's
  name no longer says it either (the Tests bullet above quotes the new name).
  Review fixes (1) and (2) above keep their wording as the record of those
  reviews. Deleting the clause does not kill V5, which breaks Rule 2 on the
  `db.go` path that no direct test reaches, so the DB-level review 5 case
  pins it: an archive queued during a failing batch's first attempt keeps the
  turn at each attempt's end, and the next batch commits ahead of a chunk that
  asked after that archive was queued. Under V5 the chunk goes as soon as the
  retry ends.
- Ties (V2, V3, V4): Rule 2 ("older than the time of the chunk or large add
  nextOther chose") and Rule 3's comparison with the coalescing writers ("one
  older than every coalescing writer's") already say which way a tie goes.
  Rule 3's "the chunk or large add with the oldest time" did not, so it, and
  the `nextOther` and `oldestUncoalesced` doc comments, now add "of those with
  the same time, the one that asked first", which Bound 2's "in the order they
  asked" follows from. Three tie cases pin the three directions.
- V7, V15 and V19, the reviewer's other mutants, were already killed and are
  in the rerun set.
- Red: against the tests as they were before this round V1 to V5 and V11
  survive (`$G/review5/mutants-r5.txt`). With the new cases each fails on its
  own case's first order assertion and nowhere else, after at most 1.6s
  (`$G/review5-fix/red-new-cases.txt`, lines mapped to cases in
  `new-case-lines.txt`): V1 line 2002, V2 2019, V3 2124, V4 2033 (all
  `CoalescingByAsking`), V5 1092 (the DB-level case), V11 1924 (`Kept`). A
  first red run, before the `make lint` fix below, gave the same result
  (`aborted-run-before-lint-fix/red-new-cases.txt`).
- Gates (`$G/review5-fix/`, `gates.sh`): the first `make lint`
  (`make-lint-run1.log`) flagged two repeated label strings (`goconst`) in
  the new cases' final order assertions, written as `noHoldWTGiveTurns`
  slices, and one doc line over 120 columns; the assertions were rewritten as
  single `give`/`noHoldWTNextTurn` steps, asserting the same order, and the
  line rewrapped. On the final tree `make lint` reports 0 issues
  (`make-lint.log`). The whole set of 74 mutants (review 4's 65, V1 to V5,
  V7, V11, V15 and V19) was rerun in a scratch detached worktree against the
  final tests: the unmodified tests pass and all 74 are killed
  (`mutants.txt`; V15 by the test binary's timeout, as before). The
  write-turns tests pass 5 of 5 runs with `-race` (`green-race-x5.txt`). With
  `TZ=UTC` `make race` reports 1,008 passed and 22 skipped and `make test`
  1,008 passed and 23 skipped (`make-race-tzutc.log`, `make-test-tzutc.log`;
  the count is unchanged, as the new cases are in existing test functions).

#### Review fixes (6)

A sixth reviewer returned FAIL on wording only; the code and tests were kept.
Its scratch test (`$G/review6/zz_review6_test.go.txt`,
`counterexamples.txt`) shows two sentences that read wrongly. These changes
are to comments and this record only.

- A coalescing writer's time: the `writeTurns` doc comment and the Bounds
  preamble said a coalescing writer carries "the first request queued since
  it (last) took its queue", which reads as the `queuedSince` mark the keep
  decision uses. Both now say it carries when the oldest request in the queue
  it took was queued, the first queued since it took the queue before that,
  matching the `writeTurn.oldest` field comment. 6.4e's Design bullet on the
  same time is reworded to match. This round also added a clause saying that
  after the best-effort writer is stopped nothing marks its requests, so its
  final drain may carry its ask time; review 7 removed it, since nothing can
  be queued after `beStopped` is set (`close()` holds `db.Lock` with
  `db.closed` set, and `queueJobChangeAhead` checks `beStopped`). The sentences about the keep ("requests queued since it took its
  queue") mean the mark and are unchanged.
- The `writeTurns` doc comment's "Between those two a request's age gains
  little, since each asks again behind the other as soon as its turn ends"
  now ends "since each asks again soon after its turn ends".
- Design bullet 1 now says the coalescing writers go in the order they asked
  "except when one keeps its turn (below)".
- `TestNoHoldWriteTurnsSmallAddsByAsking`'s doc comment: the request waits
  behind the chunks and large adds the add went ahead of only "while another
  small add waits". With none waiting, the coalescing writer carrying the
  older request goes first (the reviewer's second case). The tail "behind
  the chunks and large adds that asked before its writer did" is dropped,
  since after the second add's turn a chunk that asked after the add's turn
  but before the writer, with a younger request, goes after it. The Review
  fixes (3) bullet that said the comment "states the bound's consequence"
  now says it states a consequence of the bound for when another small add
  waits.
- `firstCoalescing` and `oldestUncoalesced`: "-1 if none is waiting" is now
  "-1 if none counts", since with `onlyOvertaken` a waiting writer may not
  count.
- Gates (`$G/review6-fix/`): printed without comments, `writeturns.go` and
  `nohold_write_turns_test.go` are identical before and after this round
  (`strip-compare-writeturns.txt`, `strip-compare-test.txt`); `make lint`
  reports 0 issues (`make-lint.log`); the write-turns tests pass with `-race`
  (`race.txt`). No mutant rerun, as no code changed.

#### Review fixes (7)

A seventh reviewer returned FAIL on wording only
(`$G/review7/counterexamples.txt`); the code and tests were kept. These
changes are to comments and this record only.

- The clause "after the best-effort writer is stopped nothing marks its
  requests, so its final drain may carry its ask time" is removed from the
  `writeTurns` doc comment and the Bounds preamble. It was false: no
  best-effort write can be queued once `beStopped` is set, so the final drain
  carries the mark of its first write.
- `firstCoalescing`'s doc comment said it returns "the oldest request either
  waiting coalescing writer carries"; with `onlyOvertaken` it returns only
  the oldest of the writers that count. It now says "the oldest time of the
  coalescing writers that count".
- Gates (`$G/review7-fix/`): printed without comments, `writeturns.go` is
  identical before and after this round (`strip-compare-writeturns.txt`);
  `make lint` reports 0 issues (`make-lint.log`).

#### Review fixes (8)

An eighth reviewer returned FAIL on wording only
(`$G/review8/counterexamples.txt`); the code and tests were kept. These
changes are to comments, two test case names and this record only. After
fixing the three findings, one sweep read every sentence of the `writeTurns`
doc comment and its helpers' comments, the test function docs and case names
in `nohold_write_turns_test.go`, and this item's Design, Bounds and Change
for quantifiers wider than the code (the keep reads only `queuedSince`, the
first and so oldest request queued), names from before this item that
contradict the ask order of the coalescing writers, and claims missing their
conditions. Each was narrowed or deleted.

`writeturns.go`:

- Deleted (F1): "By the first rule, at most one small add gets a turn while
  another writer waits." Several small adds can wait, and each goes in turn;
  Bound 1 states the bound. The next sentence's "Without that rule" became
  "Without the first rule".
- Rule 2 (F3): "either nobody waits or those queued requests are older than
  the time of the chunk or large add nextOther chose" now says "the oldest of
  those queued requests is older than".
- `nextTurn` doc (F3): "those queued requests are older than the chosen
  chunk's or large add's time" now says "the oldest of those queued requests
  is older than".
- Bound 3 (F3): "the requests queued for the keeper are older still" now says
  "the oldest of the requests queued for the keeper is older still".
- Bound 4: "or by keeping the turn for requests older than that time" now says
  "or by keeping the turn when the oldest of the requests queued for it is
  older than that time".
- Bound 5: "those queued for it since it took its queue count only in its keep
  decision as its turn ends" now says "of those queued for it since it took
  its queue, only the oldest counts, and only in its keep decision as its turn
  ends".
- "The coalescing writers go among themselves by asking because" now says "by
  asking, but for a kept turn, because" (the exception Bound 2 states).
- "Against chunks and large adds the order is by time rather than by asking"
  now says "Against chunks and large adds the coalescing writers are judged by
  time rather than by asking": the coalescing writer that goes is still the
  first to ask, which may carry a younger time than a waiting chunk's.
- `nextOther` doc: "The coalescing writer that asked first goes unless a chunk
  or large add carries a time older than that of every coalescing writer that
  counts" now says "Of the writers that count, the coalescing writer that
  asked first goes unless a chunk or large add carries a time older than that
  of every coalescing writer", so the first-asking writer is also one that
  counts.
- `addTurns` field (pre-existing): "counts the turns give has handed to the
  new jobs writer" now says "counts the turns give has handed to small adds
  (writerNewJobs), not those handed to large adds (writerLargeNewJobs)".

`nohold_write_turns_test.go`:

- Case name (F2), DB-level: "with no add waiting, the writer whose oldest
  request is oldest goes next, not the one that asked first" is now "with no
  add waiting, an archive whose request is older than a waiting chunk's goes
  ahead of it, though the chunk asked first". The case is an archive against
  bigA's second chunk; between the coalescing writers the old name was false
  since this item.
- Case name, `Kept`: "an archive queued while its writer has not yet asked
  again is invisible to the order: once an add has gone, ..." is now "...
  counts only in that writer's keep: once an add has gone, ...". The queued
  archive counts in its writer's keep (Bound 5), so "invisible" held only
  because a small add stopped the keep in this case.
- File comment: "Items 6.3e to 6.4e" is now "6.3e to 6.4g", and "a pending
  add commits within a bounded number of other transactions" now says "a
  pending small add"; large adds have no preference.
- `TestNoHoldWriteTurnsKept` doc: "keeps it only when the requests it has
  queued since it took its queue would go next" now says "the oldest request
  it has queued".
- `TestNoHoldWriteTurnsCoalescingByAsking` doc: "go among themselves in the
  order they asked" gains "but for a kept turn"; "against chunks and large
  adds by their oldest request" now says "by the oldest request either
  carries"; "the queued requests of the writer whose turn ends are older
  still" now says "the oldest of the requests queued for the writer whose turn
  ends is older still"; and "against the requests queued for the writer whose
  turn ends" now says "against the oldest request queued for".
- `TestNoHoldWriteTurnsSmallAddsByAsking` doc: "a small add is held back only
  by the writers it has already gone ahead of" now says "besides the turn in
  flight or kept and the small adds that asked before it, a small add is held
  back only by the writers an add has gone ahead of"; and "though they carry
  younger requests" now says "even when they carry younger requests".

This record:

- Design bullet 2: "Against chunks and large adds the oldest request goes
  first, judged for the coalescing writers by" now says "Against chunks and
  large adds the coalescing writers are judged by", as in the doc comment.
- Design bullet 3 (F3): "its queued requests are older than that one's. Its
  queued requests are then the oldest of all" now says "the oldest of its
  queued requests is older than that one's. The oldest of its queued requests
  is then the oldest of all"; "So it still protects the requests queued during
  a chunk" gains "with no small add waiting".
- Design bullet 4: "with an add held back only the writers it overtook go
  first" now says "the writers an add has overtaken".
- Bounds 3, 4 and 5: as in the doc comment.
- Change: "the holder's queued requests are older than the chosen chunk's"
  now says "the oldest of the holder's queued requests is older than".
- The renamed `Kept` case's references: the mutant table's R4 row ("the
  invisible-archive case") and the Review fixes (4) table's Bound 5 row.

Kept as they are, after the sweep: names and sentences that describe one case
with one queued request ("keeps no turn for queued requests exactly as old as
a waiting chunk's", "decided on its own queued requests", "the requests it
has queued are older than every waiting writer's"), and Bound 1's "before
another small add has had one" (while the small add waits, any small add
given a turn is another).

- Gates (`$G/review8-fix/`): printed without comments by go/printer
  (`tools/strip.go`), `writeturns.go` is identical before and after this round
  (`strip-compare-writeturns.txt`) and the test file differs only in the two
  case names (`strip-diff-test.txt`); `make lint` reports 0 issues
  (`make-lint.log`); the write-turns tests pass with `-race`
  (`test-race.log`).

#### Review fixes (9)

A ninth reviewer returned FAIL on two sentences (`$G/review9/`); the code and
tests were kept. These changes are to comments and this record only.

- Change (this record): "`nextOther`, which returns the coalescing writer that
  asked first (`firstCoalescing`) unless the chunk or large add with the oldest
  request (`oldestUncoalesced`) is older than every request either coalescing
  writer carries" now says "which returns, of the writers that count, the
  coalescing writer that asked first ... is older than every request of the
  coalescing writers that count". With a small add waiting, a coalescing writer
  no add has gone ahead of does not count (the reviewer's
  `TestReview9ChangeSentence`).
- Design bullet 2 (this record), the same unqualified phrasing: "judged by the
  oldest request either carries: if that is no younger than every waiting
  chunk's and large add's, the coalescing writer that asked first goes" now
  says "judged by the oldest request carried by those of them that count (with
  a small add waiting, only the writers an add has gone ahead of count)", and
  the coalescing writer, chunks and large adds it compares are those that
  count. The other "every ... coalescing writer's" sentences in this item are
  on the path with no small add waiting, where every writer counts, or already
  say "among them", and were kept.
- `TestNoHoldWriteTurnsKept` doc: "a writer whose turn ends keeps it only when
  the oldest request it has queued since it took its queue would go next" now
  states Rule 2 of the `writeTurns` doc: with no small add waiting, it keeps
  it only when it has requests queued since it took its queue, `nextOther`
  chose no coalescing writer, and either nobody waits or the oldest of those
  queued requests is older than the time of the chunk or large add `nextOther`
  chose.
- Rewrapped to 80 columns the comment lines this item made longer: in
  `writeturns.go` the `coalescing` doc, the Bound 2 sentence ending "goes
  before one it has not", the `nextTurn` doc's last sentence and the
  `nextOther` doc; in `nohold_write_turns_test.go` the file comment and the
  `TestNoHoldWriteTurnsKept` doc.

- Gates (`$G/review9-fix/`): printed without comments by go/printer
  (`$G/review8-fix/tools/strip.go`), `writeturns.go` and
  `nohold_write_turns_test.go` are identical before and after this round
  (`strip-compare.txt`); `make lint` reports 0 issues (`make-lint.log`).

#### Review fixes (10)

A tenth reviewer returned FAIL on two paraphrases of the ordering rules
(`$G/review10/counterexamples.txt`); the code and tests were kept, and
reviews 6 to 10 each found the `writeTurns` doc comment's rules and bounds
true. These changes are to comments and this record only.

- F1, `TestNoHoldWriteTurnsCoalescingByAsking` doc: "and against chunks and
  large adds by the oldest request either carries" lacked "with no small add
  waiting". The doc no longer restates the rules (below).
- F2, Change (this record): "so it keeps its turn while the other coalescing
  writer waits if a chunk or large add older than that writer's request is
  chosen" stated a condition as sufficient that is not. It now says "so a
  coalescing writer can keep its turn while the other coalescing writer
  waits, when a chunk or large add older than that writer's request is
  chosen (Rule 2 has every condition)".
- Reviews 6 to 10 each failed on a paraphrase like these outside the
  `writeTurns` doc, so the paraphrases are gone: each restatement of an
  ordering rule, keep condition or bound in the places below is now a
  reference by number (Rule 1 to Rule 3, Bound 1 to Bound 6, numbered as
  they appear in the `writeTurns` doc), and each test doc says what its
  cases set up and check.
  - `nohold_write_turns_test.go`: the docs of
    `TestNoHoldWriteTurnsFailingBatchAttempts`,
    `TestNoHoldWriteTurnsAddOvertakesOnce`, `TestNoHoldWriteTurnsKept`,
    `TestNoHoldWriteTurnsCoalescingByAsking` and
    `TestNoHoldWriteTurnsSmallAddsByAsking`.
  - This item's Design section (all four bullets, with the rationale kept),
    its Bounds paragraph (the six restated bounds and the definition of a
    writer's time removed), the In-process probes paragraph on other cheap
    changes (its keep clause), the Change section (`nextOther`, `mayGoNext`
    and `nextTurn`), and the Sentence column of the Review fixes (4) table
    (the rows for asking, Rule 1, Rule 2, a kept turn and Bound 6).
  - `writeturns.go`: the `writeTurn.oldest` field comment, "when the oldest
    request this writer's transaction carries was queued", which does not
    hold for a chunk or large add (the moment it asked), now says it is the
    time the writer waits with, as the `writeTurns` doc defines it.
- Convey case names, the Tests section and earlier Review fixes sections
  were left: case names describe their own setup, and the rest is history.

- Gates (`$G/review10-fix/`): printed without comments by go/printer
  (`$G/review8-fix/tools/strip.go`), `writeturns.go` and
  `nohold_write_turns_test.go` are identical before and after this round
  (`strip-compare.txt`); `make lint` reports 0 issues (`make-lint.log`).

#### Review fixes (11)

An eleventh reviewer returned FAIL on three case names and one test doc
claim (`$G/review11/counterexamples.txt`); the code, the test logic and the
`writeTurns` doc rules and bounds were kept. These changes are to case names,
one test doc comment and this record only.

- `CoalescingByAsking` "the archive and best-effort writers go ahead of a
  chunk, in the order they asked, when either carries a request older than
  the chunk's" was false when the first to ask carries the older request:
  after it goes, the other, younger than the chunk, waits behind the chunk
  (`TestReview11EitherOlder`: best-effort, chunk, archive). The case has the
  last to ask carry it, and its name now says so.
- `SmallAddsByAsking` "a request queued for a coalescing writer waits behind
  a chunk that asked before its writer did, though the request is older than
  the chunk's: ..." led with a claim false with no next add waiting, when the
  older request goes first (`TestReview11WaitsBehindChunk`). The name now
  leads with the case's condition.
- DB-level "a best-effort write waiting when an archive commits goes ahead of
  the archive queued for the archive writer's next transaction, though that
  was queued first" was false with an older chunk waiting, which makes the
  archive writer keep its turn (`TestReview11BEAheadOfQueuedArchive`: archive,
  chunk, best-effort). The name now starts "with no chunk or large add
  waiting".
- `TestNoHoldWriteTurnsSmallAddsByAsking`'s doc said its cases check "that a
  writer whose turn ends while an add is held back keeps no turn", and its
  last case's name said it, but no case there pins it: the one turn that ends
  with an add held back and a request queued for its holder is that case's
  archive writer's, and the best-effort writer, a coalescing writer, waits to
  go next, so Rule 2 would keep no turn either. The claim is gone from the
  doc, which now
  says the test pins the order Rule 1 gives, and the case is renamed for the
  order it checks. In the Review fixes (4) table the Rule 1 row cites the
  cases that pin the no-keep clause instead: DB-level "an archive writer whose
  turn ends with a small add waiting does not keep" and `Kept` "an archive
  queued while its writer has not yet asked again counts only in that
  writer's keep", whose turn ends with an archive queued and a small add
  waiting.

Then one sweep read every Convey case name in `nohold_write_turns_test.go`
for a claim stated as general that is false when other writers wait (a small
add, an older chunk or large add, the other coalescing writer, or a kept
turn). Each such name now states the case's condition: which writers wait,
or that no add waits. The 35 renames, in file order (`DB` is
`TestNoHoldWriteTurns`, the others as in the Review fixes (4) table; a name
built from a loop label shows the labels in brackets):

| Test | Old name | New name |
| --- | --- | --- |
| DB | an add commits in the transaction right after the archive in flight, ahead of best-effort and archive writes queued before it | an add asking while an archive is in flight and only a best-effort write waits commits in the next transaction, ahead of that write and of an archive queued after the add asked |
| DB | a best-effort write queued before a big add's first transaction commits ahead of it, so a big add's transactions wait their turn too | with only an add and a best-effort write waiting behind the archive in flight, a big add's first transaction asking after them commits after both, so a big add's transactions wait their turn too |
| DB | a big add's first transaction arriving while no add waits commits after a best-effort write queued before it | a big add's first transaction asking while only a best-effort write waits behind the archive in flight commits after that write |
| DB | a best-effort write queued while a big add's first transaction is in flight commits between that transaction and the add's next one | a best-effort write queued while a big add's first transaction is in flight, with no other writer waiting, commits between that transaction and the add's next one |
| DB | an archive queued while an add went ahead of it commits ahead of a later large add, though its writer asks for its turn after that add | an archive queued before an add went ahead of its writer commits ahead of a big add's next chunk and a large add that asked after it was queued, the only writers waiting with it, though the chunk asked before its writer did |
| DB | an archive queued while an add went ahead of it does not hold back a later small add that asks for its turn before its writer does | with only an archive and a big add's next chunk waiting, both asking after the last add's turn, a small add goes first and the archive next, though the archive was queued before that add went ahead of its writer |
| DB | [a run-state, a change, an exit] write queued while an add went ahead of it commits ahead of a later large add, though its writer asks for its turn after that add | [a run-state, a change, an exit] write queued before an add went ahead of its writer commits ahead of a big add's next chunk and a large add that asked after it was queued, the only writers waiting with it, though the chunk asked before its writer did |
| DB | with no add waiting, an archive whose request is older than a waiting chunk's goes ahead of it, though the chunk asked first | with only an archive and a big add's next chunk waiting, the archive goes first when its request is older than the chunk's, though the chunk asked first |
| DB | an archive writer whose turn ends keeps it for archives queued before an add went ahead of them, ahead of a later large add | an archive writer whose turn ends with only a large add waiting keeps it for an archive queued before an add went ahead of its writer and before the large add asked |
| DB | a best-effort write waiting when an archive commits goes ahead of the archive queued for the archive writer's next transaction, though that was queued first | with no chunk or large add waiting, a best-effort write waiting when an archive commits goes ahead of the archive queued for the archive writer's next transaction, though that was queued first |
| DB | an add too large to go ahead of the other writers commits after a best-effort write queued before it | with an archive in flight and only a best-effort write waiting, an add too large to go ahead of the other writers commits after that write |
| DB | adds each small enough to go ahead keep going ahead when folded into a transaction larger than one add may be | adds each small enough to go ahead still go ahead when folded into a transaction larger than one add may be: the fold goes ahead of an archive, the only other writer waiting, that asked after the last add's turn began |
| DB | a small add folded with one too large to go ahead of the other writers commits after a best-effort write queued before them | a small add folded with one too large to go ahead of the other writers commits after a best-effort write queued before them, the only other writer waiting |
| DB | [the retry of a batch's archives that did not fail, a failed archive's solo re-run] goes ahead of a chunk that asked after the batch's first archive was queued | [the retry of a batch's archives that did not fail, a failed archive's solo re-run] goes ahead of a chunk that asked after the batch's first archive was queued, once an add that went ahead of the chunk has gone and nothing else waits |
| DB | an archive queued during a failing batch's first attempt keeps the turn for the archive writer at each attempt's end, so the next batch, which carries it, commits ahead of a chunk that asked after it was queued | with only a chunk waiting that asked after it was queued, an archive queued during a failing batch's first attempt keeps the turn for the archive writer at each attempt's end, so the next batch, which carries it, commits ahead of the chunk |
| DB | an archive commits after at most one more add transaction while adds stay queued | with only small adds waiting besides it, an archive commits after at most one more add transaction while they stay queued |
| `FailingBatchAttempts` | A batch with k failing ops takes 2k+1 transactions, the failing ops getting their own error and the rest none | A batch with k failing ops and at least one that does not fail takes 2k+1 transactions, the failing ops getting their own error and the rest none |
| `Kept` | an archive queued while its writer has not yet asked again counts only in that writer's keep: once an add has gone, a chunk that asked after it goes first | an archive queued while its writer has not yet asked again counts only in that writer's keep: once an add has gone, a chunk that asked after it, the only writer waiting, goes before it |
| `CoalescingByAsking` | the archive and best-effort writers go ahead of a chunk, in the order they asked, when either carries a request older than the chunk's | with no add waiting, the archive and best-effort writers go ahead of a chunk that asked before both, in the order they asked, when the one that asked last carries a request older than the chunk's |
| `CoalescingByAsking` | the pair is judged by the older request either carries, though the first to ask carries it: a chunk waits behind an archive that asked after it with an older request, then goes ahead of a best-effort writer that asked last | with no add waiting, the pair is judged by the older request either carries, though the first to ask carries it: (rest unchanged) |
| `CoalescingByAsking` | a coalescing writer carrying a request exactly as old as a waiting chunk's goes ahead of it | with no add waiting, a coalescing writer carrying a request exactly as old as a waiting chunk's goes ahead of it |
| `CoalescingByAsking` | a large add whose request is older than both of theirs goes ahead of the archive and best-effort writers | with no add waiting, a large add whose request is older than both of theirs goes ahead of the archive and best-effort writers |
| `CoalescingByAsking` | a best-effort writer waiting when the turn ends goes next, though the archive queued is older than its request, so the archive writer keeps no turn | a best-effort writer, the only writer waiting when the turn ends, goes next, though the archive queued is older than its request, so the archive writer keeps no turn |
| `CoalescingByAsking` | the archive writer keeps no turn for queued requests exactly as old as a waiting chunk's: the chunk goes next | the archive writer keeps no turn for queued requests exactly as old as a waiting chunk's: the chunk, the only writer waiting, goes next |
| `CoalescingByAsking` | a best-effort writer chosen to go next keeps the archive writer from keeping its turn though a chunk waits too: a best-effort writer whose request is younger than the archive queued but older than a waiting chunk's goes first | a best-effort writer chosen to go next keeps the archive writer from keeping its turn though a chunk waits too: with only that chunk waiting besides it, a best-effort writer whose request is younger than the archive queued but older than the chunk's goes first |
| `CoalescingByAsking` | the archive writer keeps its turn while a best-effort writer waits, when a chunk that asked after the archive was queued, and before the best-effort writer asked, would go next | with no add waiting, the archive writer keeps its turn while a best-effort writer waits, when a chunk that asked after the archive was queued, and before the best-effort writer asked, would go next |
| `CoalescingByAsking` | a failing archive batch's retries keep the turn kept at the first attempt's end, and the next batch takes it, while a chunk and a best-effort writer wait | a failing archive batch's retries keep the turn kept at the first attempt's end, and the next batch takes it, while only a chunk and a best-effort writer wait |
| `CoalescingByAsking` | a turn kept at the end of a failing archive batch's first attempt with nobody waiting is decided again at the retry's end: a best-effort writer that asked during the retry goes before the solo re-run | a turn kept at the end of a failing archive batch's first attempt with nobody waiting is decided again at the retry's end: a best-effort writer, the only writer to ask during the retry, goes before the solo re-run |
| `CoalescingByAsking` | an archive queued after the chunk asked is younger than the chunk's request, so the chunk goes when the turn ends, though a best-effort writer that asked later waits too | an archive queued after the chunk asked is younger than the chunk's request, so the chunk goes when the turn ends, though a best-effort writer that asked later waits too, the only other writer waiting |
| `CoalescingByAsking` | each chunk of one big add waiting as an attempt ends goes before the next attempt, though the batch's request is older than every chunk's | each chunk of one big add, the only writer waiting as an attempt ends, goes before the next attempt, though the batch's request is older than every chunk's |
| `SmallAddsByAsking` | an add held back by a writer it went ahead of waits for that writer only, not for another carrying an older request that asked after its turn | with only an archive an add went ahead of and a best-effort writer that asked after that add's turn waiting, the next add goes after the archive and before the best-effort writer, though the best-effort writer carries an older request |
| `SmallAddsByAsking` | an add held back by a chunk it went ahead of waits for that chunk only, not for an archive carrying an older request that asked after its turn | with only a chunk an add went ahead of and an archive that asked after that add's turn waiting, the next add goes after the chunk and before the archive, though the archive carries an older request |
| `SmallAddsByAsking` | a request queued for a coalescing writer waits behind a chunk that asked before its writer did, though the request is older than the chunk's: a best-effort writer asking after an add went ahead of a chunk goes after that chunk and the next add | a best-effort writer asking after an add went ahead of a chunk, with the next add waiting as that add's turn ends, goes after the chunk and that next add, though its request is older than the chunk's |
| `SmallAddsByAsking` | a request queued for a coalescing writer an add has not gone ahead of waits behind a chunk the add went ahead of whose request is younger than its own but older than that of a coalescing writer the add went ahead of | an archive asking after an add went ahead of a chunk and a best-effort writer, with the next add waiting as that add's turn ends, goes after both and that next add, though its request is older than both of theirs |
| `SmallAddsByAsking` | a writer whose turn ends while an add is held back keeps no turn, though the requests it has queued are older than every waiting writer's | the archive and best-effort writers an add went ahead of both go before the next add, in the order they asked |

Kept as they are, after the sweep: names whose claim holds whatever else
waits (a turn ending with a small add waiting keeps none; a kept turn goes to
its writer alone; the first-asking of two coalescing writers goes first
between them, with no add waiting or among those an add went ahead of; of a
waiting chunk and large add, the older or, tied, the first to ask goes
first; a big add's put or a writer's transaction that panics gives up its
turn; a large add's re-run gets no preference), names under a "Given" whose
writers are all named in the setup (`AddOvertakesOnce`, `Kept`'s best-effort
and nobody-waiting cases), and the panic, partial-commit, no-turn and
final-drain cases, which state no order.

Record quotes updated to the new names: this item's Tests section (the
DB-level case, the `CoalescingByAsking` summary, the H2 guard and the
review 1 to 5 cases, and the attempt counts of
`TestNoHoldWriteTurnsFailingBatchAttempts`), the mutant table's R2 and G3
rows, and the Review fixes (4) table's Rule 1, Rule 2, Rule 3, Bound 1,
Bound 4 and Bound 6 rows. The Tests sections of earlier items and the earlier
Review fixes sections keep the old names, as the record of those rounds.

- Gates (`$G/review11-fix/`): printed without comments by go/printer
  (`$G/review8-fix/tools/strip.go`), `writeturns.go` is identical before and
  after this round (`strip-compare-writeturns.txt`) and the test file differs
  only in Convey name strings (`strip-diff-test.txt`); `make lint` reports 0
  issues (`make-lint.log`); the write-turns tests pass with `-race`
  (`test-race.log`).

#### Review fixes (12)

A twelfth reviewer returned FAIL only on "this test pins Rule or Bound N"
claims that a mutant breaking that clause still passes
(`$G/review12/mutants12.txt`, `mutants12b.txt`, `mut12.py`); the code, the
test logic, the Convey case names and the `writeTurns` doc rules and bounds
were kept. Hand-written pin claims had been wrong in several rounds, so they
are gone, and the table below, derived mechanically, replaces them.

- `nohold_write_turns_test.go`: every "pinning Rule ... / Bound ..." claim is
  deleted from the test function doc comments, which now say only what each
  test's cases set up and check.
- `writeturns.go`: the sentence before the bounds no longer names five tests
  that "pin them". It says the first five bounds follow from the rules and the
  last from `applyFolded` and `drainArchives`, and points to this record for
  the tests that pin each.
- This record: the Bounds paragraph (Bound 6 is proved against
  `applyFolded` and `drainArchives`, its batch-time clause pinned by the
  DB-level cases, and it points here), the mutant table's G1 row (the killing
  cases by name, from `$G/review12/mutants12b.txt`), the Tests section ("pass
  with their logic unchanged (some renamed in reviews 8 and 11)"), Design
  bullet 4 (Rule 1's choice among overtaken writers changed, by asking) and
  the Change section (only the `writeTurns` doc states the bounds). The
  Review fixes (4) table is marked superseded and kept as history.

Method (`$G/review12-fix/`): Rules 1 to 3, the sentence after the rules on a
kept turn, and Bounds 1 to 6 of the `writeTurns` doc were split into clauses
(`clauses.txt`). Each clause got at least one mutant that breaks it, reusing
reviews 4, 5, 6 and 12's (`mut64g-r6.copy.py` holds reviews 4 to 6's) and
adding N1 to N5 and N2b where none fitted (`mutmatrix.py`). Each of the 65
mutants was applied alone in a scratch detached worktree holding this item's
code, and each of the six write-turns test functions was run on its own
(`-run '^Name$'`, `-count=1`, `-timeout 4m`, no `-race`), as review 12 did;
the unmodified tree passes all six. A test counts as killing a mutant only if
its run failed, by an assertion, a bounded wait or the binary's timeout. The
full matrix is `matrix.txt` (raw runs `matrix-w1.txt` to `matrix-w4.txt`);
`table.py` generates it and the table below (`clause-table.md`).

Two mutants survive every test. Y17 (chunks and large adds by asking,
whatever their times) is equivalent in the code, so Rule 3's "with the oldest
time" among chunks and large adds is pinned only against G13 and, for ties,
V4. N2 (a waiting coalescing writer's requests queued since its take count
in the order when older than the time it carries) is equivalent for the
`db.go` callers (corrected in review 13): a waiting coalescing writer carries
the time `swapped` returned (or, for a retry, the batch's time), which is no
younger than the swap, and every request queued after the swap is strictly
younger; a zero time cannot arise with a non-empty queue; and after
`beStopped` nothing calls `queued`. The direct tests reach N2's condition
only where a test writer asks with "now" (`TestNoHoldWriteTurnsKept` near
line 1789 and `TestNoHoldWriteTurnsCoalescingByAsking` near line 2233), and
there that writer is the only one waiting, so N2 changes nothing; the
DB-level test and the model checker (`TestReview7WriteTurnsBounds`) never
reach it (`$G/review13/n2probe-*.txt`). N2b, which makes those requests count
whenever there are any, is killed by a DB-level case. So Bound 5's "only in
its keep decision" has no live direction left unpinned. Four clauses have no
mutant: Rule 2's "(so only a coalescing writer can)", a consequence of which
callers call `queued`; Bound 2's "except that a coalescing writer that keeps
its turn goes again while the other waits", which scopes the clause before
it; Bound 5's "So a writer waiting or asking between the end of its turn and
its next ask can go ahead of those", a consequence of the clause before it;
and Bound 6's "The bounds above hold for each attempt's own wait", which
scopes the others. Bound 6's "between attempts any waiting writer can go,
among them small adds" has no attempt-specific mutant for small adds.

Test functions are shortened as in the Review fixes (4) table; `DB` is
`TestNoHoldWriteTurns`.

| Clause | Mutant | Killed by |
| --- | --- | --- |
| Rule 1: "With a small add waiting, no turn is kept." | M1 (review 12): with a small add waiting, the turn is kept by Rule 2's keep test | `Kept`, `DB` |
|  | H6 (6.4e): a turn is kept while an add is held back | `SmallAddsByAsking` |
|  | H8 (6.4e): a turn is kept while a small add waits and no writer it overtook does | `Kept`, `DB` |
| Rule 1: "If a small add has been given a turn since any waiting writer other than a small add began waiting ..., the writer nextOther chooses from those writers goes" (an overtaken writer goes before the add) | P2 (6.4e): no overtake rule, a waiting small add always goes | `AddOvertakesOnce`, `SmallAddsByAsking` |
|  | Y14 (review 6): a writer must be overtaken twice before it holds an add back | `AddOvertakesOnce`, `SmallAddsByAsking` |
|  | H1 (6.4e): a small add is held back by any waiting writer with an older time | `AddOvertakesOnce`, `Kept`, `SmallAddsByAsking`, `DB` |
| Rule 1: "a small add has been given a turn" (a small add's turn, not a large add's or a chunk's) | H5 (6.4e): large add turns count as add turns | `AddOvertakesOnce` |
|  | P4 (6.4e): chunk turns count as add turns | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
|  | Y4 (review 6): every turn given counts as an add turn | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
| Rule 1: "since ... began waiting" | V7 (review 5): a writer asking after an add's turn counts as overtaken by it | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
| Rule 1: "from those writers" (only overtaken writers count) | R4 (review 4): chunks and large adds ignore the overtaken filter | `AddOvertakesOnce`, `Kept`, `SmallAddsByAsking`, `DB` |
|  | G12 (review 3): with a small add waiting, a coalescing writer no add has gone ahead of counts too | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
|  | G14 (review 3): with a small add waiting, coalescing writers no add has gone ahead of count in the pair's time | `SmallAddsByAsking` |
|  | G15 (review 3): with a small add waiting, a coalescing writer no add has gone ahead of goes before an overtaken chunk with a younger time | `SmallAddsByAsking` |
|  | H2 (6.4e): a held-back add lets the next of all other writers go first | `SmallAddsByAsking` |
| Rule 1: "the writer nextOther chooses" among them (Rule 3's choice) | X2 (6.4e): among writers an add overtook, a coalescing writer and a chunk by asking | `SmallAddsByAsking` |
| Rule 1: "otherwise the small add that asked first goes" | P5 (6.4e): the last waiting small add goes | `AddOvertakesOnce` |
| Rule 2: "With no small add waiting, the writer nextOther chooses from all those waiting goes" | N1 (review 12 fix): with no small add waiting, nextOther still chooses only among overtaken writers | `AddOvertakesOnce`, `Kept`, `CoalescingByAsking`, `DB` |
| Rule 2: keep "when it has requests queued since it took its queue" (its own, since the take) | V15 (review 5): a coalescing writer keeps its turn with nothing queued | `AddOvertakesOnce`, `Kept`, `CoalescingByAsking`, `DB` |
|  | G9 (6.4g): taking a kept turn clears its writer's queued time | `CoalescingByAsking`, `DB` |
|  | A5 (6.4e): swapped does not clear the queued time | `Kept`, `CoalescingByAsking`, `DB` |
|  | V11 (review 5): the best-effort writer keeps its turn for the archive writer's older queued requests | `Kept` |
|  | V5 (review 5, db.go): each attempt after a failing batch's first clears the archive writer's queued time | `DB` |
| Rule 2: "(so only a coalescing writer can)" | none | not mutated: only the coalescing writers' queues call queued, so this follows from the callers |
| Rule 2: "nextOther chose no coalescing writer" | G2 (6.4g): a turn is kept although nextOther chose a coalescing writer | `CoalescingByAsking`, `DB` |
|  | R3 (review 4): nextOther reports a coalescing choice only when no chunk or large add waits | `CoalescingByAsking` |
|  | Y9 (review 6): nextOther reports a coalescing choice whenever a coalescing writer counts | `CoalescingByAsking` |
|  | G8 (review 1): no turn is kept while a coalescing writer waits | `CoalescingByAsking` |
| Rule 2: "either nobody waits" | R1 (review 4): no turn is kept when nobody waits | `Kept` |
| Rule 2: "the oldest of those queued requests" | A4 (6.4e): queued keeps the latest request, not the first | `DB` |
| Rule 2: "is older than the time of the chunk or large add nextOther chose" | G6 (6.4g): a turn is kept whenever no coalescing writer is chosen, whatever the chunk's time | `Kept`, `CoalescingByAsking`, `DB` |
|  | R5 (review 4): the keep compares with the first-asking coalescing writer's time, not the chosen chunk's | `CoalescingByAsking` |
|  | Y18 (review 6): the keep compares with the oldest coalescing writer's time, ignoring the chosen chunk | `Kept`, `CoalescingByAsking`, `DB` |
|  | V3 (review 5): a turn is kept for queued requests tying with the chosen chunk's time | `CoalescingByAsking` |
| Rule 3: "the coalescing writer that asked first" | G1 (6.4g): the coalescing writers go among themselves by oldest time | `CoalescingByAsking`, `SmallAddsByAsking` |
|  | G7 (6.4g): the last-asking coalescing writer goes first | `AddOvertakesOnce`, `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
| Rule 3: "the chunk or large add with the oldest time" | G13 (review 3): the chunk or large add with the youngest time goes first | `CoalescingByAsking`, `DB` |
|  | Y17 (review 6): chunks and large adds go by asking, whatever their times | none (survives): equivalent in the code: chunks and large adds ask through updateInTurn, so each carries the time it asked, read under the writeTurns mutex, and their ask order and time order agree (ties are V4) |
| Rule 3: "(of those with the same time, the one that asked first)" | V4 (review 5): of chunks and large adds tying in time, the last to ask goes first | `CoalescingByAsking` |
| Rule 3: "has one older than every coalescing writer's" | G3 (6.4g): against chunks only the first-asking coalescing writer's time counts | `CoalescingByAsking` |
|  | R2 (review 4): the coalescing pair is judged by the younger of their times | `CoalescingByAsking` |
|  | V1 (review 5): the pair is judged by the last-asking coalescing writer's time | `CoalescingByAsking` |
|  | G4 (6.4g): coalescing writers always go before chunks and large adds | `AddOvertakesOnce`, `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
|  | G5 (6.4g): chunks and large adds always go before coalescing writers | `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
|  | V2 (review 5): a chunk or large add tying with the pair's time goes first | `CoalescingByAsking` |
| After the rules: "A kept turn goes to its writer alone" | K3 (6.4e): a kept turn is taken by any writer | `Kept` |
|  | K8 (6.4e): a small add takes a turn kept for another writer | `Kept` |
| After the rules: "when it next asks" (once) | M7 (6.4e): taking a kept turn does not clear it | `Kept`, `DB` |
| After the rules: "until then no writer goes" | K4 (6.4e): a kept turn leaves no turn in progress | `Kept`, `CoalescingByAsking`, `DB` |
| Bound 1: "Once a small add waits, no turn is kept" | M1 (review 12): with a small add waiting, the turn is kept by Rule 2's keep test | `Kept`, `DB` |
|  | H6 (6.4e): a turn is kept while an add is held back | `SmallAddsByAsking` |
|  | H8 (6.4e): a turn is kept while a small add waits and no writer it overtook does | `Kept`, `DB` |
| Bound 1: "each turn after the one in flight or kept as it asks goes to it, to a small add that asked before it, or to a writer an add has gone ahead of" | H2 (6.4e): a held-back add lets the next of all other writers go first | `SmallAddsByAsking` |
|  | R4 (review 4): chunks and large adds ignore the overtaken filter | `AddOvertakesOnce`, `Kept`, `SmallAddsByAsking`, `DB` |
|  | G12 (review 3): with a small add waiting, a coalescing writer no add has gone ahead of counts too | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
|  | G15 (review 3): with a small add waiting, a coalescing writer no add has gone ahead of goes before an overtaken chunk with a younger time | `SmallAddsByAsking` |
|  | H1 (6.4e): a small add is held back by any waiting writer with an older time | `AddOvertakesOnce`, `Kept`, `SmallAddsByAsking`, `DB` |
| Bound 1: "No add has gone ahead of a writer that asks again after its turn, so while the small add waits it gets no second turn before another small add has had one" | V7 (review 5): a writer asking after an add's turn counts as overtaken by it | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
|  | Y4 (review 6): every turn given counts as an add turn | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
| Bound 1: "goes after the transaction in flight ... and at most one transaction of each writer then waiting that an add has already gone ahead of, however many others wait" | H2 (6.4e): a held-back add lets the next of all other writers go first | `SmallAddsByAsking` |
|  | G12 (review 3): with a small add waiting, a coalescing writer no add has gone ahead of counts too | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
|  | H1 (6.4e): a small add is held back by any waiting writer with an older time | `AddOvertakesOnce`, `Kept`, `SmallAddsByAsking`, `DB` |
| Bound 2: "Small adds go among themselves in the order they asked" | P5 (6.4e): the last waiting small add goes | `AddOvertakesOnce` |
| Bound 2: "as do chunks and large adds (whose time is when they asked)" | G13 (review 3): the chunk or large add with the youngest time goes first | `CoalescingByAsking`, `DB` |
|  | V4 (review 5): of chunks and large adds tying in time, the last to ask goes first | `CoalescingByAsking` |
| Bound 2: "and the two coalescing writers" | G1 (6.4g): the coalescing writers go among themselves by oldest time | `CoalescingByAsking`, `SmallAddsByAsking` |
|  | G7 (6.4g): the last-asking coalescing writer goes first | `AddOvertakesOnce`, `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
| Bound 2: "except that a coalescing writer that keeps its turn goes again while the other waits" | none | not mutated: it scopes the clause before it, carving out the kept turn Rule 2 allows, and states no order of its own |
| Bound 3: "each writer that goes ahead of it asked before it did, is a small add, or is the other coalescing writer keeping its turn" | G1 (6.4g): the coalescing writers go among themselves by oldest time | `CoalescingByAsking`, `SmallAddsByAsking` |
|  | G5 (6.4g): chunks and large adds always go before coalescing writers | `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
|  | V19 (review 5): a writer asking with no time carries the zero time | `AddOvertakesOnce`, `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
| Bound 3: "With no small add waiting, a chunk or large add goes ahead of it only with a time older than its own" | G5 (6.4g): chunks and large adds always go before coalescing writers | `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
|  | G3 (6.4g): against chunks only the first-asking coalescing writer's time counts | `CoalescingByAsking` |
|  | R2 (review 4): the coalescing pair is judged by the younger of their times | `CoalescingByAsking` |
|  | V1 (review 5): the pair is judged by the last-asking coalescing writer's time | `CoalescingByAsking` |
|  | V2 (review 5): a chunk or large add tying with the pair's time goes first | `CoalescingByAsking` |
| Bound 3: "the other coalescing writer keeps its turn only when a chunk or large add with a time older than the waiting writer's would go next" | G2 (6.4g): a turn is kept although nextOther chose a coalescing writer | `CoalescingByAsking`, `DB` |
|  | R3 (review 4): nextOther reports a coalescing choice only when no chunk or large add waits | `CoalescingByAsking` |
| Bound 3: "and the oldest of the requests queued for the keeper is older still" | G6 (6.4g): a turn is kept whenever no coalescing writer is chosen, whatever the chunk's time | `Kept`, `CoalescingByAsking`, `DB` |
|  | R5 (review 4): the keep compares with the first-asking coalescing writer's time, not the chosen chunk's | `CoalescingByAsking` |
|  | V3 (review 5): a turn is kept for queued requests tying with the chosen chunk's time | `CoalescingByAsking` |
|  | Y18 (review 6): the keep compares with the oldest coalescing writer's time, ignoring the chosen chunk | `Kept`, `CoalescingByAsking`, `DB` |
|  | V15 (review 5): a coalescing writer keeps its turn with nothing queued | `AddOvertakesOnce`, `Kept`, `CoalescingByAsking`, `DB` |
| Bound 3: "With a small add waiting, a writer an add has gone ahead of goes before one it has not, whatever their times" | G15 (review 3): with a small add waiting, a coalescing writer no add has gone ahead of goes before an overtaken chunk with a younger time | `SmallAddsByAsking` |
|  | G12 (review 3): with a small add waiting, a coalescing writer no add has gone ahead of counts too | `AddOvertakesOnce`, `SmallAddsByAsking`, `DB` |
| Bound 4: "a coalescing writer gets the turn only if a waiting coalescing writer carries a time no younger than the chunk's or large add's" | G4 (6.4g): coalescing writers always go before chunks and large adds | `AddOvertakesOnce`, `CoalescingByAsking`, `SmallAddsByAsking`, `DB` |
| Bound 4: "or by keeping the turn when the oldest of the requests queued for it is older than that time" | G6 (6.4g): a turn is kept whenever no coalescing writer is chosen, whatever the chunk's time | `Kept`, `CoalescingByAsking`, `DB` |
|  | R5 (review 4): the keep compares with the first-asking coalescing writer's time, not the chosen chunk's | `CoalescingByAsking` |
|  | V3 (review 5): a turn is kept for queued requests tying with the chosen chunk's time | `CoalescingByAsking` |
|  | Y18 (review 6): the keep compares with the oldest coalescing writer's time, ignoring the chosen chunk | `Kept`, `CoalescingByAsking`, `DB` |
|  | V15 (review 5): a coalescing writer keeps its turn with nothing queued | `AddOvertakesOnce`, `Kept`, `CoalescingByAsking`, `DB` |
|  | G11 (review 2): an archive turn ending with nothing queued is kept against younger chunks for the batch's own time | `AddOvertakesOnce`, `Kept`, `CoalescingByAsking`, `DB` |
| Bound 5: "The requests a coalescing writer has taken count in the order through the time it carries while it waits" | A2 (6.4e, db.go): an archive batch carries no time | `DB` |
|  | A3 (6.4e, db.go): a best-effort batch carries no time | `DB` |
|  | Y11 (review 6): a coalescing writer carries the time it took its queue | `CoalescingByAsking`, `DB` |
| Bound 5: "of those queued for it since it took its queue, only the oldest counts" | A4 (6.4e): queued keeps the latest request, not the first | `DB` |
| Bound 5: "and only in its keep decision as its turn ends" | N2 (review 12 fix): a waiting coalescing writer's requests queued since it took its queue count in the order | none (survives): equivalent for the db.go callers: a waiting writer carries the time swapped returned (or, for a retry, the batch's time), no younger than the swap, and every request queued after the swap is strictly younger; a zero time cannot arise with a non-empty queue; after beStopped nothing calls queued. The direct tests reach N2's condition only where a test writer asks with "now" (`Kept` near line 1789, `CoalescingByAsking` near line 2233), where that writer is the only one waiting, so N2 changes nothing; `DB` and the model checker never reach it. With N2b killed, no live direction of the clause is unpinned |
|  | N2b (review 12 fix): a waiting coalescing writer carries the time of its requests queued since it took its queue, when it has any | `DB` |
|  | V11 (review 5): the best-effort writer keeps its turn for the archive writer's older queued requests | `Kept` |
| Bound 5: "So a writer waiting or asking between the end of its turn and its next ask can go ahead of those, whatever its time" | none | not mutated: a consequence of the previous clause, stating no order of its own |
| Bound 6: "applyFolded retries the rest of a batch after each archive that fails in it, then re-runs each failed one alone" | Y16 (review 6, db.go): after one failing op the rest get the error without a retry | `FailingBatchAttempts`, `DB` |
|  | F1 (review 4, db.go): a failed op is not re-run alone | `FailingBatchAttempts`, `DB` |
|  | Y3 (review 6, db.go): the solo re-runs share one transaction | `FailingBatchAttempts` |
| Bound 6: "takes at most 2k+1 transactions" | N3 (review 12 fix, db.go): each failed op is re-run alone twice | `FailingBatchAttempts`, `DB` |
| Bound 6: "(2k when every archive in it fails" | F1 (review 4, db.go): a failed op is not re-run alone | `FailingBatchAttempts`, `DB` |
|  | Y3 (review 6, db.go): the solo re-runs share one transaction | `FailingBatchAttempts` |
| Bound 6: "1 when its first transaction fails as a whole)" | N4 (review 12 fix, db.go): a transaction failing as a whole re-runs each op alone | `FailingBatchAttempts` |
| Bound 6: "Each attempt carries the batch's time" | N8 (6.4e, db.go): a failed archive's solo re-run carries no time | `DB` |
|  | N8b (6.4e, db.go): every attempt after a batch's first carries no time | `DB` |
| Bound 6: "but asks afresh, only after the one before it has ended" | N5 (review 12 fix, db.go): a failed archive's solo re-run takes no write turn | `DB` |
| Bound 6: "The bounds above hold for each attempt's own wait, not for the batch" | none | not mutated: it scopes Bounds 1 to 5 and states no order of its own |
| Bound 6: "between attempts any waiting writer can go, among them small adds, the best-effort writer, and chunks and large adds whatever their times" | G10 (review 2): a turn kept at one attempt's end stays kept at each later one while no small add waits | `CoalescingByAsking` |
|  | G11 (review 2): an archive turn ending with nothing queued is kept against younger chunks for the batch's own time | `AddOvertakesOnce`, `Kept`, `CoalescingByAsking`, `DB` |
|  | none | small adds: no attempt-specific mutant; Rule 1's no-keep row covers every turn's end |

- Gates (`$G/review12-fix/`): printed without comments by go/printer
  (`$G/review8-fix/tools/strip.go`), `writeturns.go` and
  `nohold_write_turns_test.go` are identical before and after this round
  (`strip-compare.txt`; pre-edit copies in `before/`); `make lint` reports 0
  issues (`make-lint.log`).

#### Review fixes (13)

A thirteenth reviewer returned FAIL only on the Review fixes (12) table
(`$G/review13/`, with `n2probe-*.txt`). This round changes comments and this
record only; the code, the test logic and the Convey case names are unchanged.

- N2's reason was false: a test does reach its condition. The Review fixes
  (12) text and the Bound 5 "only in its keep decision" row now say N2 is
  equivalent for the `db.go` callers, that the direct tests reach its
  condition only with one writer waiting, where it changes nothing, and that
  the DB-level test and the model checker never reach it, so that row has no
  live direction left unpinned.
- Bound 5's "So a writer waiting or asking ..." row lists no mutant; it is a
  consequence of the clause before it.
- Bound 3's "With a small add waiting, a writer an add has gone ahead of goes
  before one it has not" row no longer lists G14, which stays under Rule 1's
  "from those writers"; G12 and G15 remain.
- Bound 2's carve-out for a coalescing writer that keeps its turn lists no
  mutant; it scopes the clause before it. G8 moved to Rule 2's "nextOther chose
  no coalescing writer" row, beside Y9, killed by `CoalescingByAsking`.
- `writeturns.go`: the first five bounds follow "from the rules and the
  definitions above them", and this record lists, for each bound, "the tests
  that pin it or why none does".
- The Tests section's note on mutants killed only by a bounded wait or the
  test binary's timeout says it describes the earlier full `-race` runs and
  points to the Review fixes (12) matrix for each test function's result.
- `TestNoHoldWriteTurnsFailingBatchAttempts`'s doc says it calls
  `applyFolded` with a fake transaction and counts the transactions it would
  take, each a write turn of its own in production.

- Gates (`$G/review13-fix/`): every mutant row's "Killed by" list in the
  Review fixes (12) table equals the failing tests in
  `$G/review12-fix/matrix.txt` (109 rows, 0 mismatches; `tablecheck.txt`,
  `tools/tablecheck.py`).
  Printed without comments (`$G/review8-fix/tools/strip.go`), `writeturns.go`
  and `nohold_write_turns_test.go` are identical before and after this round
  (`strip-compare.txt`; pre-edit copies in `before/`); `make lint` reports 0
  issues (`make-lint.log`).

#### Review fixes (14)

Review 14 (`$G/review14/`) found one false sentence, in the Tests section: G6,
K4 and M7 were listed as killed only by a bounded wait or the test binary's
timeout, but in the earlier full runs G6 also failed order assertions
(`$G/review4/mutants.txt`, `$G/review5-fix/mutants.txt`) and K4 and M7 failed
`TestNoHoldWriteTurnsKept`'s fast "given a turn while ... queued" check. They
are dropped from that list. The Gates section now notes that later runs count
1,008, since review 4 added `TestNoHoldWriteTurnsFailingBatchAttempts`. Only
this record changed.

- [x] implemented
- [x] reviewed
