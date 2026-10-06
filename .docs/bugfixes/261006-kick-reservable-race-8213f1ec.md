# A kick undoes a reservation that overtakes it (2026-10-06)

- Branch: `kickorder-5e1122fd`
- Base: `origin/develop` at `021f734a` (#682)
- Worktree: `../wr-kickorder`
- Queue owner: this branch, this checklist. The run-state branch
  `reserve-runstate-2f00c336` depends on it.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: targeted `go test` runs, plain and `-race`;
`golangci-lint run ./jobqueue/...`; `cleanorder -min-diff` on the edited Go
files.

## Prior checked items that must not regress

- `260930-release-durability.md`, "A live-record change can be queued out of
  order with its encoding": `queueJobChange` holds `job.RLock` from encode
  through enqueue, so writes of one job queue in encode order
  (`TestBestEffortChangeKeepsEncodeOrder`). The same file's waiting-item
  release fix: a release of an item a kick already took out of Run spends no
  retry (`TestReleaseAfterLost` covers kick).
- `260930-runner-report-followups.md` item 5: a re-sent release after a kick
  leaves the job ready with the kicked budget (`TestResentReleaseAfterKick`).
- `260930-buried-dependent-restart.md` and spec B2: a buried job with
  unresolved dependencies stays buried, and a kick makes it dependent
  (`TestBuriedDependentRestart`).
- `260819-1.md`: `kickJobs` seeds `UntilBuried` with `initialUntilBuried`,
  which saturates at `MaxUint8`.
- `260924-run-state-reset.md`: kick skips run-queue items; the reservation
  after a kick carries a blank `FailReason` (`TestJobqueueSignal`).
- `260917-start-durability.md`: kick keeps the async `updateJobAfterChange`
  rather than a durable write, for write-storm reasons.
- `260925-add-test-connect-flake.md` and `260928-load-sensitive-flakes.md`:
  `TestJobqueueModify` (kick, then ready) and `TestJobqueueSignal` (kick,
  then reserve reads reserved) must stay deterministic.

## Items

- [x] kickJobs race: s.q.Kick makes the job reservable before the kick sets
      State/UntilBuried under job.Lock and queues its write; a reservation
      landing in that window has its State overwritten to ready and the
      kick's full change supersedes it (crash before Started recovers it ready
      while its runner may run it: double run).
  - Source: coordinator. Also noted: a reservation between the kick's
    `job.Lock` section and its `updateJobAfterChange` can commit durably
    before the kick's change is queued. On develop the reservation's full
    write includes the kicked `UntilBuried`; it matters to
    `reserve-runstate-2f00c336`, which writes smaller reservation records.
  - Test seam: `kickQueuedHook` in `jobqueue/server.go`, nil in production,
    called in `kickJobs` after `s.q.Kick` succeeds and before `job.Lock`.
  - Red test: `TestKickRacingReservation` in `jobqueue/kick_order_test.go`.
    It buries a job, pauses its kick in the hook, has a second client reserve
    the job, then lets the kick finish. One leaf asserts the server's job is
    still reserved. The other queues a durable write of another job, so the
    kick's write has committed, takes `BackupDB` as the crash image, restarts
    on it, and asserts a fresh runner is not given the job and it recovered
    reserved.
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestKickRacingReservation$' -v`, exit 1 on `021f734a` plus the hook, 3
    of 3 runs (log lines filtered):

    ```text
    === RUN   TestKickRacingReservation
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
    ✔✔✔✔✔✔✘
    Failures:
      * jobqueue/kick_order_test.go
      Line 162:
      Expected: jobqueue.JobState("reserved")
      Actual:   jobqueue.JobState("ready")
      (Should equal)!
      * jobqueue/kick_order_test.go
      Line 207:
      Expected 7a86b3130dab3094599d694334bf7d8d to be empty (but it wasn't)!
    47 total assertions
    --- FAIL: TestKickRacingReservation (0.69s)
    ```

    Line 162 is the in-memory state after the kick. Line 207 is the
    recovered manager handing the job to a fresh runner: the double run.
    A throwaway guard in `kickJobs` that skips run-queue items after the hook
    turned the test green, which shows that both leaves fail for this race
    and not because of the setup. The guard was reverted and is not the fix,
    since the item can still move between that check and `job.Lock`.
  - Fixed: `queue.Queue.KickWith` runs a callback under the queue lock after
    confirming the item is buried and before it leaves the bury sub-queue
    (`Kick` delegates to it). `kickJobs` passes `markKicked`, which sets
    `UntilBuried` and `State` under `job.Lock` and queues the async
    `updateJobAfterChange`, so the kick's fields and write precede any
    reservation. A failed kick (not found, not buried) sets nothing and
    queues nothing. Files: `queue/queue.go`, `queue/kick_with_test.go`,
    `jobqueue/server.go`, `jobqueue/kick_order_test.go` (a third leaf checks
    a crash image taken when the job first becomes reservable recovers it
    ready with the kicked `UntilBuried`). Red command green 3 of 3;
    `make lint`, `make test`, `make race` pass. Speed gate (`make speed` plus
    a kick-under-reserve-load comparison) is owed before PR-ready, since
    `queue/` changed.
  - Deferred, pre-existing (no change here): `kickJobs` changes the `*Job`
    its caller passed, not the item's data. If a modify replaced the item's
    `*Job` between the caller's lookup and the kick, the kick would change
    the old object while the item keeps the new one. Present on develop.

- [x] resumeJob / resumeQueueItem race (jobqueue/server.go ~3130-3155):
      s.q.Resume makes a suspended item reservable before the resume sets
      job.State and queues updateJobAfterChange; a reservation in that window
      could have its State overwritten or its write superseded by the
      resume's (same class as the kick race above). Not yet reproduced.
  - Source: kick-race implementor, incidental finding.
  - Verdict: real, though not as a lost reservation. `resumeJob` reads the
    item's state under `job.Lock` after `s.q.Resume`, so a reservation in the
    window is mapped to `reserved` and its write is not superseded with stale
    fields (`queueJobChange` encodes the current job). But if the runner also
    reports Started in the window, the resume maps the run item back to
    `reserved` and overwrites `running`. A re-sent start report (the runner's
    retry after a lost reply) then fails `acceptDuplicateStartLocked` and
    counts a second attempt. A crash does not double-run it: the record keeps
    `Pid`/`Host`, so `recoversIntoRun` still puts it in Run.
  - Test seam: `resumeQueuedHook` in `jobqueue/server.go`, nil in production,
    called in `resumeJob` after `resumeQueueItem` succeeds and before
    `job.Lock`.
  - Red test: `TestResumeRacingStart` in `jobqueue/resume_order_test.go`. It
    suspends a ready job, pauses its resume in the hook, has a runner reserve
    and start the job, then lets the resume finish. One leaf asserts the
    server's job is still running; the other re-sends the same start report
    and asserts the job counts one attempt.
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestResumeRacingStart$' -v`, exit 1 on `90a413bd` plus the hook, 3 of 3
    runs (log lines filtered):

    ```text
    === RUN   TestResumeRacingStart
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔
        the job stays running in memory ✘
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔
        its runner's re-sent start report is not counted as another attempt ✔✘
    Failures:
      * jobqueue/resume_order_test.go
      Line 143:
      Expected: jobqueue.JobState("running")
      Actual:   jobqueue.JobState("reserved")
      (Should equal)!
      * jobqueue/resume_order_test.go
      Line 153:
      Expected: 1
      Actual:   2
      (Should equal)!
    31 total assertions
    --- FAIL: TestResumeRacingStart (0.44s)
    ```

    A throwaway guard in `resumeJob` that left `State` alone when the item was
    in Run made the test pass, so both leaves fail because of this race and
    not because of the setup. The guard was reverted and is not proposed as
    the fix; the kick fix's `KickWith` pattern (a callback under the queue
    lock) is the analogue to consider.
  - Fixed: `queue.Queue.ResumeWith` runs a callback `func(data any, to
    ItemState)` under the queue lock once the item is confirmed suspended and
    before it leaves the suspended sub-queue (`Resume` delegates to it).
    `resumeJob` passes `markResumed`, which sets `State` from `to` under
    `job.Lock` and queues the async `updateJobAfterChange`, so the resume's
    state and write precede any reservation or start. A failed resume sets
    nothing and queues nothing. Files: `queue/queue.go`,
    `queue/resume_with_test.go`, `jobqueue/server.go`,
    `jobqueue/resume_order_test.go`. Red command green 4 of 4; `make lint`,
    `make test`, `make race` pass. Speed gate owed (shared with the kick fix).

- [x] resumeQueueItem decides whether to call clearRACPending from the live
      item state read after ResumeWith returns; if a runner reserves the item
      in between, the state reads run and RAC-pending is cleared although
      readyAdded("resumed") was queued, releasing waiting reserves before the
      ready-added callback has set racRunning (reserve/scheduling ordering;
      nothing lost or run twice; also present on develop).
  - Source: resume-race reviewer, incidental finding. Suggested fix: decide
    from the callback's `to` instead of live state.
  - Verdict: real, with a narrow effect. `resumeQueueItem` calls
    `setRACPending` before `ResumeWith`, so a reserve that takes the item
    between `ResumeWith` returning and the state read is one that was already
    past `waitForPendingReserves`, such as a runner waiting inside the queue's
    `Reserve` for something to become ready. The state read then sees Run and
    calls `clearRACPending`, although `ResumeWith` already queued
    `readyAdded("resumed")`. That closes every waiting reserve and clears
    `racPending` before the callback sets `racRunning`. So a reserve that
    arrives in the gap runs before or alongside the callback, which breaks
    the contract `waitForPendingReserves` documents. No job is lost or run
    twice. The resumed item is already reserved, so in this scenario the
    early reserve sees the same ready set and reserve groups the callback
    would leave. The effect is ordering only, except where a concurrent
    add or modify shares the `racPending` flag, which is a bool and not a
    count.
  - Test seam: `resumeItemReadyHook` in `jobqueue/server.go`, nil in
    production, called in `resumeQueueItem` after `ResumeWith` succeeds and
    before the `resumedTo` check (it ran before `item.Stats()` when the bug was reproduced).
  - Red test: `TestResumeRacingReserveKeepsRACPending` in
    `jobqueue/resume_order_test.go`. The test suspends a job, then lets
    pending callbacks drain (a reserve that finds nothing, then
    `racPending` and `racRunning` are checked false). It gates the queue's
    ready-added callback in front of the real `readyAddedCallback`. It then
    calls `resumeJobs` with the hook reserving the item through
    `reserveItem`, which stands in for a runner already past
    `waitForPendingReserves`. Once the gated callback has been called, a
    second client's `Reserve(50ms)` must not return within 1s while the gate
    is shut. After the gate opens it returns no job.
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestResumeRacingReserveKeepsRACPending$' -v`, exit 1 on `aa37f59b` plus
    the hook, 3 of 3 runs, and exit 1 under `-race` with no race report (log
    lines filtered):

    ```text
    === RUN   TestResumeRacingReserveKeepsRACPending
    ✔✔✔✔✔✔✔✔✔✔✔✔✔
        another reserve waits until the ready-added callback has run ✘
    Failures:
      * jobqueue/resume_order_test.go
      Line 278:
      Expected: false
      Actual:   true
    14 total assertions
    --- FAIL: TestResumeRacingReserveKeepsRACPending (0.36s)
    ```

    Line 278 is the second reserve returning while the callback the resume
    queued is still gated. A throwaway change made the test pass 3 of 3, with
    `TestResumeRacingStart` still passing. The change had `resumeQueueItem`
    record `to` from the `ResumeWith` callback and clear RAC-pending only
    when `to` is not ready, as the reviewer suggested. That shows the test
    fails because of this race and not because of the setup. The change
    was reverted.
  - Fixed: `resumeQueueItem` records the `to` its `ResumeWith` callback
    receives and calls `clearRACPending` only when `to` is not ready, which
    matches exactly whether `ResumeWith` queued `readyAdded`. The unused item
    parameter is gone (`suspendedItem` became `suspendedJob`). File:
    `jobqueue/server.go`; test `TestResumeRacingReserveKeepsRACPending`. Red
    command green 5 of 5; `make lint`, `make test`, `make race` pass.
  - Deferred, separate (needs design; for the owner): `racPending` is one
    shared bool, so a clear by one operation (a resume to Dependent, a failed
    kick or resume, an add of nothing, an error path in a live-rerun or
    dependent update) drops another operation's hold before its ready-added
    callback runs. Ordering only. A counter is not a drop-in fix because the
    queue merges `readyAdded` calls into one callback and `finishRAC` resets
    everything when it ends.

## Speed gate

Run on 2026-10-06 against head `a04b6dc9` (plus the uncommitted benchmark
file below) and base `021f734a`, on an 8-core host with no LSF, with
`nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` under `/tmp`.

### make speed

`make speed` (quick mode, `-count 6`, base built from the merge-base in a
temporary worktree). Verdict: PASS, "no benchmark or scenario worsened by more
than 10% at p<0.05, and every scenario met its thresholds". Nothing changed
significantly. `QueueLifecycle` geomean was -4.9% (n.s.) and the jobqueue
sec/op geomean was +3.7% (n.s.). The `report-storm` scenario ran at 749 vs
748 jobs/s. Results: `/tmp/wr-speed-sb10/run-1791299536/benchstat.txt`.

None of those benchmarks kicks or resumes a job, so the gate does not cover
the changed path. A focused benchmark was added for it.

### Kick and resume under reserve load

`jobqueue/kick_resume_bench_test.go` adds `BenchmarkKickUnderReserveLoad` and
`BenchmarkResumeUnderReserveLoad`. Each round starts a real server and adds
1000 target jobs, each with a distinct 130 KB path-like command. The kick
benchmark buries the targets and the resume benchmark suspends them. Each
round then adds 20000 small ready fillers. Eight goroutines call
`reserveItem` with a 50 µs pause between calls while `kickJobs` or
`resumeJobs` handles all 1000 targets. It reports the op's time (ns/op) and
the p50, p99 and max latency of reserves that began during the op and got a
job.

Command: test binaries built from each tree with `go test -c -tags netgo`
(`CGO_ENABLED=0`), run as `-test.bench UnderReserveLoad -test.benchtime=1x`
for 6 interleaved base/head rounds, then compared with benchstat
(`v0.0.0-20260929162123-406019bb8b68`):

```text
                         │    base     │     head     │ vs base
sec/op  Kick                 109.2m      111.3m          ~ (p=0.937)
sec/op  Resume               107.6m       80.7m          ~ (p=0.132)
reserve-p50-us  Kick          6.38      1167.5      +18208% (p=0.002)
reserve-p50-us  Resume        6.07      1123.0      +18399% (p=0.002)
reserve-p99-us  Kick         752.1      5653.5        +652% (p=0.002)
reserve-p99-us  Resume       818.8      6622.5        +709% (p=0.002)
reserves/op  Kick            4294        600.5         -86% (p=0.002)
reserves/op  Resume          3572        336.5         -91% (p=0.002)
```

Verdict: FAIL, a material regression in reserve latency. Kick and resume
take as long as before, but a reserve that runs alongside them now waits a
median of about 1.1 ms instead of about 6 µs (roughly 180 times longer). p99
is about 8 times longer, and reservers complete 86-91% fewer reserves in the
window. Each item now encodes its whole job (about 100 µs for a 130 KB
command) and queues the write while holding the queue mutex. The op loop
takes that mutex again straight away, so waiting reservers get it only when
Go's `sync.Mutex` switches to starvation mode after a 1 ms wait. That
explains a p50 just over 1 ms. `queueJobChange` also takes `db.RLock`, and
`queueJobExit` holds `db.Lock` while it encodes an exiting job. Under load, a
kick under the queue mutex can therefore also wait on concurrent job exits.

The fix is unchanged. Options, for the owner:

1. Encode before the queue lock and enqueue under it: snapshot-encode the job
   with the kicked or resumed fields applied before `KickWith`/`ResumeWith`.
   The callback then sets the fields under `job.Lock` and enqueues the
   pre-encoded bytes, which is cheap. Ordering and the crash-image leaf still
   hold because the write is queued before the item is reservable. The cost
   is a change detector (such as a per-job change counter) so the callback
   re-encodes in the rare case that the job changed after the snapshot, and
   a discarded encode when the kick or resume fails.
2. Queue the write by key in the callback and encode it at drain time under
   `job.RLock`. Latest-wins coalescing makes a later encode safe, and the
   queue position is still taken before the item is reservable. This changes
   the best-effort writer's contract and its archive guard, so the change is
   wider.
3. Set only the fields in the callback and queue the write after
   `KickWith` returns. This is the cheapest option, but it reopens the
   on-disk ordering gap. `TestKickRacingReservation`'s crash-image leaf would
   fail, and it matters to `reserve-runstate-2f00c336`.

Batching all keys under one lock hold would cut the hand-offs but hold the
mutex for the whole op (about 100 ms here), which is worse for p99.

### Rework (option 1)

The owner chose option 1. Uncommitted on top of `a04b6dc9`:

- `db.prepareJobChange` encodes the job's live record with the kick's or
  resume's fields applied, before the queue lock. It applies them under
  `job.Lock`, encodes, and puts them back, so nobody sees them early. It also
  takes the write's `db.wg` slot (under `db.RLock`, still before the queue
  lock). `kickJobs` prepares before it sets RAC-pending, so reserves are not
  held up for the encode.
- The `KickWith`/`ResumeWith` callback calls `db.queueJobChangeAhead`. It sets
  the fields under `job.Lock` and queues the prepared bytes under `beMu`
  alone, with no db lock and no encode. A failed kick or resume releases the
  slot (`discardJobChangeAhead`), so nothing is set or queued. The kick's
  debug log moved out of the callback.
- Change detection: while a prepared change is outstanding on a job
  (`changesAhead` > 0), `Job.Lock` also counts write locks (`writeLocks`). If
  the count moved between the prepare and the callback, beyond the callback's
  own lock, the callback encodes the job afresh under its lock. That case is
  rare, for example a modify of the buried or suspended job in between. Both
  fields return to 0 when no prepared change is outstanding. A counter that
  only grew failed `client`'s `TestFakeScheduler`, whose `ShouldResemble`
  compares whole `Job` structs.
- Resume: `suspendedJob` predicts the target state from the item's unresolved
  dependencies, and the prepare encodes that one state. If the callback's `to`
  differs (a dependency resolved in between), the prepared write is outdated
  and re-encoded. Encoding both states would double the op's encode cost to
  cover a rare case.
- The best-effort writer latches `beStopped` before its final drain. A
  callback that arrives later releases its slot rather than queueing a write
  that would never drain, as `errDBClosed` does for `queueJobChange`.
- Taking the db lock out of the callback exposed an ordering gap with exits.
  `queueJobExit` released the job's read lock between its encode and its
  enqueue and relied on `db.Lock` alone. So a kick could queue between a
  bury's encode and its enqueue, and the older buried record won on disk while
  the job was ready in memory. `queueJobExit` now holds `job.RLock` from encode
  through enqueue, as `queueJobChange` does. Red test
  `TestKickDuringBuryWriteStoresKicked`: 3 of 3 failures (stored `buried`,
  want `ready`) before that change, green after.
- The gate's second note is resolved: nothing under the queue mutex takes the
  db lock now. The callback can still wait on `job.Lock` for an encode of the
  same job (an exit or change of that job in flight), never of another job.

New tests: `TestKickAfterModifyKeepsModification` (a modify landing between
prepare and callback keeps its `Retries`/`Priority` in the kick's write, and
`UntilBuried` follows the new `Retries`), `TestResumeAfterChangeWritesResumedJob`
(the same for a resume, plus a dependency resolved in between, stored `ready`),
and `TestKickDuringBuryWriteStoresKicked`. Removing the write-lock comparison
in `endChangeAheadLocked` failed the two modify leaves (stored Retries 3 and
Priority 1). Removing the counting in `Job.Lock` instead is equivalent for
correctness, since every changed job is then re-encoded, so only the benchmark
catches it. Removing the resume `outdate` failed the dependency leaf (stored
`dependent`).

`TestFailedKickLeavesJobAndStops` and `TestFailedResumeLeavesJobAndStops`
cover the failed kick or resume. A kick of a reserved job, and a resume whose
item was resumed by another path after the prepare, must return 0, leave the
job's in-memory `State` and `UntilBuried` unchanged, and let `server.Stop`
finish within 20 s. Dropping `db.wg.Done` from `discardJobChangeAhead` failed
both on the stop (it hung until the 20 s bound). Dropping the field restore
in `encodeChanged` failed both on the state (`ready`, want `reserved` or
`suspended`).

`TestResumeWritesResumedState` checks what a plain resume stores, with
nothing changing in between: a job with no unresolved dependencies is stored
`ready`, and one with an unresolved dependency `dependent`. It failed both
leaves (stored `suspended`) with `resumeJob` preparing an empty change, and
with `encodeChanged` skipping `change(job)`; no other resume test caught
either. `TestResumeAfterWriterStopsStops` starts `Stop` from
`jobChangeAheadHook`, waits for `beStopped`, then lets the resume finish: the
resume counts 1 and `Stop` returns within 20 s. Dropping `db.wg.Done` from the
`beStopped` branch, or the `beStopped` check itself, failed it on the stop.

Benchmark rerun the same way, on the final code: a detached scratch worktree
of `021f734a` with `kick_resume_bench_test.go` copied in, `CGO_ENABLED=0`
binaries, 6 interleaved base/head rounds of `-test.bench UnderReserveLoad
-test.benchtime=1x`, and benchstat `v0.0.0-20260929162123-406019bb8b68`. One
base kick round printed no result (its stderr was discarded), so base kick
has n=5. Host load average rose from 3.2 to 8.0 during the run (other users),
so variance is high:

```text
                         │    base     │     head     │ vs base
sec/op  Kick                 112.7m      132.8m          ~ (p=0.429)
sec/op  Resume               100.0m       91.4m          ~ (p=0.818)
reserve-p50-us  Kick          4.86        5.36           ~ (p=0.978)
reserve-p50-us  Resume        6.17        5.46           ~ (p=0.394)
reserve-p99-us  Kick         802.7      1099.0           ~ (p=0.126)
reserve-p99-us  Resume      1104        1137             ~ (p=0.818)
reserves/op  Kick            3967        4171            ~ (p=0.931)
reserves/op  Resume          3047        2714            ~ (p=0.240)
```

Verdict: PASS. The roughly 1.1 ms p50 and 6 ms p99 are gone, and no metric
differs significantly from base. Two earlier runs during the rework agree.
With the kick's debug log still inside the callback, kick p50 was 11.8 vs
5.5 µs (p=0.015) and nothing else was significant. With the log moved out
and the counter not yet scoped, nothing was significant.

`make speed`, run before the counter scoping (which adds only a branch to
`Job.Lock` and a job lock to a failed kick or resume): PASS, "no benchmark or
scenario worsened by more than 10% at p<0.05, and every scenario met its
thresholds". Results: `/tmp/wr-speed-sb10/run-1791302448/benchstat.txt`.

#### Wake-up and mutation sweep (review 4)

The best-effort writer has no ticker, so a write queued without
`kickBestEffortWriter` waits for some later write. Every earlier test made a
durable write before reading the store, so deleting the wake-up at the end of
`queueJobChangeAhead` passed. `TestKickStoredWithoutLaterWrite` (bury, wait
until stored `buried`, kick) and `TestResumeStoredWithoutLaterWrite` (suspend,
wait until stored `suspended`, resume) make no later write and poll the store
for up to 5 s for `ready`. With the wake-up removed both failed 3 of 3 (stored
`buried` and `suspended`); restored (`cmp` against a scratchpad copy), both
pass.

A sweep then applied 46 mutants, one at a time, to every production line the
rework changed against `a04b6dc9`, in a scratch worktree. Each ran the tests
matching `Kick|Resume|Failed|BestEffort|Suspend` plus `TestJobqueueModify`,
`TestJobqueueSignal`, `TestBuriedDependentRestart` and `TestReleaseAfterLost`.
None was INVALID. Two new tests and one new seam came out of it:

- `bestEffortSwappedHook` in `drainBestEffort`, called after it takes the
  pending writes, nil in production. `TestResumeDuringFinalDrainStops` makes a
  resume's change from inside the final drain's hook and requires `Stop` within
  20 s. Latching `beStopped` after the final drain (the reviewer's survivor)
  failed it 4 of 4.
- `TestKickAfterDBClosedStillKicks` closes the db, then kicks a buried job. It
  must not panic, and must leave the job `ready` with the kicked `UntilBuried`.

| Mutant | Result | Test or reason |
| --- | --- | --- |
| D01 drop the `beStopped` latch | KILLED | `TestResumeAfterWriterStopsStops` |
| D02 latch after the final drain | KILLED (new) | `TestResumeDuringFinalDrainStops` |
| D03 drop `beSeq++` in `enqueueChangeInSlotLocked` | equivalent | Moved, not new. A change then shares the last exit's seq, and both comparisons (`<`, `>`) are strict, so it still counts as after that exit |
| D04 drop the `wgKey` append; D05 drop the change | KILLED | stop and store tests |
| D06 `prepareJobChange` ignores a closed db | KILLED (new) | `TestKickAfterDBClosedStillKicks`: `wg.Done` of a slot never taken panics, negative counter |
| D07 release the slot in prepare | KILLED | stop tests |
| D08 skip `change`; D09 skip the restore | KILLED | `TestResumeWritesResumedState`, failed-change tests |
| D10 skip `beginChangeAheadLocked`; J03 no increment | speed-only | `changesAhead` wraps on the first end, so the first change re-encodes and every later lock counts. Writes stay correct |
| D11 unlock and relock before the change | speed-only | The relock goes through `Job.Lock` while the change is outstanding, so it is counted and forces a re-encode. Writes stay correct |
| D12 `addOpenWG` skips the closed check | equivalent | The slot is still released, by the `beStopped` branch or the discard. Only an encode is wasted, after close |
| D13 skip the change in the callback | KILLED | kick and resume tests |
| D14 nil ahead skips the change too | KILLED (new) | `TestKickAfterDBClosedStillKicks`: job left `buried` |
| D15 no wake-up | KILLED (new) | `Test{Kick,Resume}StoredWithoutLaterWrite` |
| D16 no `beStopped` check; D17 no `wg.Done` there | KILLED | `TestResumeAfterWriterStopsStops` |
| D18 unlock and relock the job between change and enqueue | KILLED (new, review 7) | `TestKickQueuesBeforeLaterChange`: a change in the gap queues its write before the kick's stale bytes, stored `Priority` 1, not 9 |
| D19 `&&` to `\|\|`; D20 ignore `outdate` | KILLED | modify and dependency leaves |
| D21 always re-encode; J01 never count; J04 compare to `began` | speed-only | Every callback re-encodes. The reserve-load benchmark covers it |
| D22 discard skips the end; J06 no reset; J07 no decrement | speed-only | Counting stays on for that job (one increment per lock), and comparisons stay relative, so writes stay correct |
| D23 discard skips `wg.Done` | KILLED | failed-change stop tests |
| D24 `queueJobExit` drops the job lock before enqueue | KILLED | `TestKickDuringBuryWriteStoresKicked`, but only for a release before `jobExitSnapshotHook`. A release after the hook, between encode and enqueue, is the same class as D18 and has no deterministic test, as `queueJobExit` has no seam in that gap |
| J02 always count | KILLED | `client` `TestFakeScheduler` (non-zero fields), outside the sweep's selection |
| J05 always unchanged | KILLED | modify leaves |
| J08 reset `writeLocks` on every end, not only the last | KILLED (new, review 5) | `TestJobChangeAheadOverlapping`, `TestOverlappingChangesAheadKeepModification`: with two changes outstanding, ending the first forgets a later modify, so the second queues stale bytes and the modify is lost on disk |
| S01 no `outdate`; S02 flipped; S03 no discard; S04 nil ahead; S07 ignore `to` | KILLED | resume tests |
| S05 always predict ready; S06 always dependent | speed-only | A wrong prediction is outdated and re-encoded |
| S08 no prepare; S10 no discard; S11 discard on success; S13 nil ahead; S14, S15 `kickChange` fields | KILLED | kick tests |
| S09 prepare after RAC-pending | speed-only | Reserves wait out the encode. The benchmark covers it |
| S12 drop the debug log | equivalent | Logging only |
| K1 discard only when a ready callback was expected | KILLED (new, review 6) | `TestFailedKickOfDependentJobStops`: a failed kick of a dependent job leaks its write slot, and `Stop` misses 20 s |
| S16 outdate only when predicted dependent and resumed ready | KILLED (new, review 6) | `TestResumeAfterChangeWritesResumedJob`, "a dependency gained in between": stored `ready`, not `dependent`, 3 of 3 |
| `<= began+1` for the change comparison | equivalent | Review 6 |
| `changesAhead > 1` or `== 0` | speed-only | Review 6 |
| swapped `&&` operands in `currentJobChange` | speed-only | Review 6 |
| no nil guard in `outdate` or the discard | untested | Review 6: unreachable after client handling stops |
| encode-error paths in `prepareJobChange` and `queueJobChangeAhead` skip `wg.Done` | untested | Review 7: both release the slot, but a codec encode of a `Job` cannot realistically fail, so no test reaches them |
| `Job.Lock` counts before `j.RWMutex.Lock()` | race-only | Review 7: the counter is then written outside the lock; only `make race` could catch it, and only when two lockers overlap |

Of 47 valid mutants, 32 were killed: 25 by the existing `jobqueue` tests,
J02 by `client`, and 6 only by the new tests (D02, D06, D14, D15, D18, J08).
Three are equivalent (D03, D12, S12) and 12 speed-only.

Review 5 found J08 surviving outside the sweep. Two tests now cover two
overlapping changes ahead of one job. `TestJobChangeAheadOverlapping` drives
the `Job` counters directly: begin A, begin B, end A, a locked change, then B
must report the job changed. `TestOverlappingChangesAheadKeepModification`
prepares two kicks of a job stored `buried`, discards the first, modifies
`Priority` and queues the second, then requires the stored job to have the new
`Priority`. Both pass, and both failed under J08 (stored `Priority` 1, not 9);
`job.go` was restored and matched its scratchpad copy under `cmp`.

Review 6 found K1 surviving: `TestFailedKickLeavesJobAndStops` covers only a
failed kick that expected a ready callback. `TestFailedKickOfDependentJobStops`
kicks a job whose item depends on an incomplete parent, so no callback is
expected and the kick fails; it requires no kick and `Stop` within 20 s. It
passes, and failed under K1 (`Stop` still running at 20 s). Review 6 also
noted no test for a resume predicted ready that resumes dependent.
`TestResumeAfterChangeWritesResumedJob` gained a leaf whose
`jobChangeAheadHook` gives the suspended item an unresolved dependency through
`q.Update`, without write-locking the job; the job must be stored
`dependent`. It passes, and failed under S16. Both mutants ran in a scratch
copy; its `server.go` matched the worktree's under `cmp` after each restore.
`db.snapshotJobExit` had no production caller, so it is gone, and
`queueUnkickedBestEffortExit` calls `snapshotJobExitLocked` under
`job.RLock`.

Review 7 found D18 surviving and its reason wrong: a seam inside the gap
proves that a change made there is kept, which is behaviour, not shape.
`jobChangeAheadQueueingHook`, nil in production, is called by
`queueJobChangeAhead` after `currentJobChange` returns and before it takes
`beMu`, with the job still write-locked. `TestKickQueuesBeforeLaterChange`
buries a job and waits until it is stored `buried`, then kicks it. From the
hook, a goroutine sets `Priority` 9 under `sjob.Lock()` and calls
`updateJobAfterChange`; the hook waits up to 1 s for it, which in correct code
blocks on the job lock until the kick's write is queued. After a durable write
of another job, the job must be stored `ready` with `Priority` 9. It passed 3
of 3, and failed 3 of 3 with the job lock released right after
`currentJobChange` (stored `Priority` 1). `db.go` was restored and matched its
scratchpad copy under `cmp`.
