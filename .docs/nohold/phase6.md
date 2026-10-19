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

Results (2026-10-16; farm22-wrstat01, queue `normal`, `OS_*` unset).
The base is 6f1b41bb, built from a scratch worktree and run that
morning. The branch figures are the rerun at 959f3fd0, after item 6.3b,
run that evening. 6.3b changed only branch code, so the base figures
were reused.

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
    the queue order itself was not traced. More fixture jobs completed
    instead (18,122 and 17,106 against 12,943 and 13,253). The total
    is higher too (22,675 and 21,762 against 17,883 and 18,475).
  - **Add p50:** 296 against 198ms (+50%) and 285 against 102ms (+179%).
    Unlike the pre-6.3b runs, both rerun runs are above the base's
    slower run. The adder logs show it is not one burst. Per 30s
    window, the median was 169 to 422ms (run 1) and 185 to 435ms (run
    2), raised in both lifetimes; the first window was the lowest.
    Cause not found. Two untested candidates: the extra job throughput
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

Kids and sizes match the earlier runs. The branch ran 98 to 99% of its
fixture reservations at 200 in lifetime 1 and all of them in lifetime 2.
The base ran 57 to 60% at 600 before the kill. The branch completed
101 to 184 kids, against 388 to 718 on the base, for the ordering
reason 6.3c traced. Nothing is left running: the final `bjobs -w`
(`lsf-after-all.txt`) is empty. The scratch worktrees, the scratch
`GOCACHE` (emptied with `GOCACHE=<dir> go clean -cache`) and the work
roots were removed.

- [ ] implemented
- [ ] reviewed

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
| base 2 | 70, 51, 36, 33, 28% | 47% of 8,661 | 32% of 4,647 | 582 |
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

- [ ] implemented
- [ ] reviewed

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

- [ ] implemented
- [ ] reviewed

### Item 6.3e: adds wait behind archive writes (owner decision 2026-10-19)

Item 6.3d found the slower `wr add` on NFS comes from phase 5: without the
hold archives complete about 17% faster, the single bolt writer saturates
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
summary now includes the turn wait; it rose from 2.9 to 4.4ms to 3.6 to
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
| archive p50 | 19.6 | 305.8 | 298.8 | | -2%, p=0.38 |
| archive p99 | 1,000 | 1,018 | 1,148 | | +13%, p=0.43 |
| archives/s | 67.6 | 74.5 | 74.4 | +10%, p=0.016 | 0%, p=0.76 |

Against the criteria:

- Add mean at or below the base: met (-15%, p=0.04).
- Add p50 at or below the base: not met (67 against 16ms). Owner accepted
  2026-10-19.
- Archive p50 and archives/s within 10% of HEAD: met.
- Archive p99 within 10% of HEAD: +13% on the medians, not significant
  (p=0.43). The per-run values overlap fully (HEAD 781 to 1,388, turns 770
  to 1,387).

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

Not done: report-storm-lsf, add-storm-lsf and limit-drain on LSF (left for
the owner after review). The host was shared with another session's test
runs during the A/B; the rotating interleave spreads that load across trees.

Evidence in `/nfs/hgi/wr/sb10-bigdb/runstate-gate/nohold-63e/`: `ab1.txt`
(every probe run), `ab.sh`, `stats.py` and `stats-out.txt`, `sum.py` and
`sum-out.txt` (writer figures per run), the probe source, `bench.txt`,
`bench.sh`, `benchsum.py`, `bench-summary.txt`, and `turns.diff` (the change
as measured; the branch differs only in one comment).

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
