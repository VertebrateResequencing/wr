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
