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
