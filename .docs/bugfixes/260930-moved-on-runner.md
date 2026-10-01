# Release a job its runner has moved on from

Branch `fix-moved-on-runner`. The filename has a suffix rather than a
sequence number so it cannot collide with another branch's `260930-N.md`.

Quality gates, with all `OS_*` unset, `GOCACHE=/tmp/claude-11346/gocache-followC`
and `GOFLAGS=-p=2` under `nice -n 19`: `make lint`, `make test`,
`CGO_ENABLED=1 make race`.

- [x] **A job left "running" under a runner that has moved on is stuck, or
      killed with the runner's current job.** Found in a production-scale
      soak before #654 (see `260930-release-durability.md`, "Not fixed"):

      > a job the manager believes is "running" can be one its runner has
      > dropped (e.g. the runner's report was lost to a manager crash, and the
      > runner moved on to other jobs). Lost-job handling then calls
      > confirm-dead (jobqueue/confirmdead.go, jobConfirmedDead), which needs
      > both the command pid and the runner pid gone over ssh. The runner
      > process is alive (running other jobs), so the check fails; it
      > re-checks only every LostJobCheckRetryTime (default 30m), each manager
      > restart resets the lost EndTime (and the 1h LostRunnerBackstop clock),
      > so with restarts <30m apart the job can stay stuck forever, and if it
      > reaches the backstop the live runner is force-killed along with its
      > unrelated current job. #654 closed the known path (release/bury now
      > durable before ack), but this is the defence in depth.

  - Red command (all `OS_*` unset, `nice -n 19`,
    `GOFLAGS=-p=2 GOCACHE=/tmp/claude-11346/gocache-followC`), run on the
    pre-fix code with only `jobqueue/moved_on_runner_test.go` added:
    `go test ./jobqueue -count=1 -run 'TestMovedOnRunner'`, exit 1:

    ```text
    Expected 'running' to be in the container ([]jobqueue.JobState), but it wasn't!
    --- FAIL: TestMovedOnRunner (0.82s)
    --- FAIL: TestMovedOnRunnerAfterRestart (1.11s)
    --- FAIL: TestMovedOnRunnerLateReport (0.12s)
    Expected: queue.ItemState("dependent")
    Actual:   queue.ItemState("run")
    --- FAIL: TestMovedOnRunnerRerunAfterRun (0.13s)
    FAIL	github.com/VertebrateResequencing/wr/jobqueue	2.588s
    ```

    The first job was still running after its runner reserved the next one.
    `TestMovedOnRunnerLeavesHeldJobs` passes before and after, as it should.
  - Fixed: `jobqueue/moved_on_runner.go` (new), `jobqueue/job.go`,
    `jobqueue/server.go`, `jobqueue/serverCLI.go`, tests in
    `jobqueue/moved_on_runner_test.go`. A reservation made by a `wr runner`
    (scheduler group, manager runner command, and an LSF scheduler element via
    `SetReserveSchedulerID`) records a persisted, monotonically increasing
    `Job.RunnerReservation`. An in-memory index of runner client -> held runs,
    rebuilt at recovery, lets a reserve, start or touch from that client
    release, as a lost job confirmed dead, any run it holds with a smaller
    number. The scheduler group alone is not a safe marker: Go clients call
    `ReserveScheduled` and hold several jobs. So runners outside LSF still rely
    on confirm-dead.

- [x] **Review hardening of the moved-on release** (reviewer findings on
      5360ce20, verified by reading the code):
  1. `releaseMovedOnRun` checks the run and then calls `releaseJob`
     non-atomically. `releaseJob` returns nil on `releaseAlreadyDone`, so if
     another path released J1 in between, the pinned lost-run behaviours fire
     a second time. If J1 was also re-reserved in that window, the new run
     could be released. Only release, and only trigger behaviours, when this
     call really released that run.
  2. `forgetCompletedRunnerHold` runs before `archiveCompletedJob`. If the
     archive write fails, J1 stays in Run with RunnerReservation 0 and only
     confirm-dead can catch it. Forget the hold and clear the field only after
     a successful archive; clear it in the complete record's encoding, as
     `db.rerunRecords` does for RerunAfterRun.
  3. DEVELOPERS.md hard rule 2: `runnerHolds.mu` is a server-wide exclusive
     mutex on every reserve, start, touch, release and archive. Make it
     per-client (for example a `sync.Map` of client to `{mu, runs}`), with an
     atomic CAS-max `next()`.
  4. `jobqueue/job.go` ~995: rewrap the over-long comment line.
  5. Recovery edge: `recoverRunnerHold` indexes J1 before `AddMany` puts it
     in the queue. A reserve in that gap makes `takeOlder` drop J1's entry
     while `runSubQueueJob` returns nil. Put the hold back when the job is
     missing during recovery.
  6. (Review of the hardening) A concurrent bury of the same run still
     triggers the behaviours twice. With J1 at Retries 0, the moved-on release
     and another `releaseJob` both snapshot J1 while it is in Run. The other
     buries it. Ours finds the item in Bury and gets `buriedItemOutcome` ->
     `releaseMoved` (default case), so `released` is true.
  - Red: `go test -tags netgo -count=1 ./jobqueue -run
    'TestReleaseConcurrentBuries|TestMovedOnRunnerConcurrentRelease'` failed
    before the fix (behaviour ran a second time; another runner's new run was
    released; group count 1 -> 0).
  - Fixed 1, 3, 4, 6. `releaseJob` wraps `releaseRun`, which reports whether
    it really released the run, and an optional `releaseReport.isRun`
    re-checks the run under the job's lock in the snapshot. Behaviours fire
    only when this release took the run out of Run. `runnerHolds` is now a
    `sync.Map` of per-client leaf mutexes with an atomic CAS-max counter (hard
    rule 2). Points 2 and 5 cannot happen: a job being archived is already
    exited, and `ttrCallback` sends it to delay if the archive fails. Clients
    are only served after recovery's `AddMany`. Tests:
    `TestMovedOnRunnerConcurrentRelease`, `TestMovedOnRunnerHoldsConcurrently`.
    It is in one commit with item 3, as both change the same lines of
    `buryReleasedItem`.

- [x] **Two concurrent buries of one run both finalize** (pre-existing since
      #654, 3eb294a2; found by the reviewer of item 2). `buryReleasedItem`
      returns `buriedItemOutcome(snap)` when the item is already in Bury, and
      its default case (snapshot neither buried nor waiting) is
      `releaseMoved`. So the second bury also runs `finalizeReleasedJob`,
      decrementing the scheduler group count twice and writing twice.
  - Fixed in `jobqueue/server.go`: an item already in Bury now always gets
    `releaseAlreadyDone`, which still sends the durable acknowledgement. The
    default case dates from 34129291. Of the other paths that bury an item,
    `buryItemWhereItIs` finalizes its own bury, and `buryImpossibleItem` only
    buries items the manager reserved from ready. So neither needs a second
    finalize. Test: `TestReleaseConcurrentBuries` (hook
    `releaseSnapshotTakenHook`). Remaining window: if another release has
    buried the item but not yet queued its write, the redundant reporter's
    durable acknowledgement writes the pre-bury state. That window is
    microseconds long.

- [x] **A late archive of a job the manager already released decrements the
      scheduler count a second time.** The archive takes the job out of Delay,
      and `finishArchive` decrements the scheduler group count again (seen in
      `TestMovedOnRunnerLateReport` as 1 -> 0). A runner that follows the
      protocol cannot send it after reserving its next job. A resend after a
      lost reply, or an older runner, might. Make the count change happen
      exactly once. (Requested by the coordinator.)
  - Red: the count assertion added to the archive case of
    `TestMovedOnRunnerLateReport` failed before the fix (`Expected: 1 /
    Actual: 0`). `TestArchiveRetryAfterTTRGivesBackOnce` fails without the TTR
    give-back.
  - Fixed: whatever takes a run out of Run gives its count back, once.
    `finishArchive` acts on the sub-queue the archive removed the item from:
    from Run it decrements; from Ready it recounts
    (`triggerReadyAddedCallback`), since a scheduling pass has counted it
    again; from Delay it does nothing. `ttrCallback`'s Exited-to-Delay branch
    (an archive whose write failed) now gives back the count off the queue lock
    (`decrementGroupCountLater`). `requeueRerun` skips its decrement on
    `ErrNotRunning`. The new `queue.RemoveUnlessState` reports the item's
    state; `RemoveUnless` keeps its v0.38.0 signature. Limit groups were
    already given back only once.

- [x] **Extend the moved-on release to runners on every scheduler, not only
      LSF.** Add an optional field to the reserve request that `wr runner` sets
      on every scheduler (local, OpenStack, LSF). It is backwards compatible:
      older runners do not send it and keep the ssh confirm-dead path.
      (Requested by the coordinator.)
  - Red: with the fixture's runner marked and sending no SchedulerID, as a
    local or OpenStack runner does, `go test -tags netgo -count=1 ./jobqueue
    -run '^TestMovedOnRunner$'` failed before the server change: the first job
    was still running.
  - Fixed: a new wire field `clientRequest.Runner`, set through
    `Client.SetReserveAsRunner` (meant for `wr runner` only), is called in
    `cmd/runner.go` on every scheduler. A runner reservation now needs a
    scheduler group, `cr.Runner` and a manager runner command. SchedulerID is
    no longer part of the rule, which supersedes item 1's LSF-only marker.
    Requests are binc maps keyed by field name and unknown fields are
    skipped, so an old manager ignores the field and an old runner sends
    false and keeps confirm-dead.

- [x] **Confirm-dead and the moved-on release can both trigger a lost run's
      behaviours.** `killRunningJob` (server.go) reports released=true
      whenever the run was killable, even when its `releaseJob` finds the run
      already released, e.g. by the moved-on release. So
      `killLostJobAndTriggerBehaviours` triggers the behaviours again, and an
      OnFailure `Run` behaviour could run twice. Use `releaseRun`'s outcome
      (keeping errors as released), and pass `isRun` for `onlyRun`. (Found by
      the reviewer of items 2 and 3.)
  - Red: `go test ./jobqueue -count=1 -run
    'TestMovedOnRunnerConfirmDeadRace'` failed before the fix: the behaviour
    ran twice, expected once.
  - Fixed in `killRunningJob`: it releases through `releaseRun`, with
    `isRun` checking the job is still that lost run when one is named. It
    reports released only when it really released the run; errors still count
    as released. `wr kill` is unchanged. Test: `TestMovedOnRunnerConfirmDeadRace`.
    `TestMovedOnRunnerLeavesHeldJobs` also checks that a scheduler element
    alone releases nothing.

- [x] **Test managers sometimes fail to bind their port ("the server's
      publication gave up").** Seen in this branch's gates in
      `TestLostJobBehavioursSpareARecoveredJob`
      (`lost_job_behaviours_test.go:192`), after `listen tcp 0.0.0.0:45993:
      bind: address already in use`. The same port, 45993, appears in the
      `260929-archive-before-start.md` evidence. Also recorded in
      `260929-running-dependent-rerun.md` as needing a separate fix. It passes
      on rerun. Suspected cause: the pick-then-release port race in
      `pickTestPort` via `isolateTestConfig` (`jobqueue/jobqueue_test.go`).
  - Real cause: rpc.statd holds an IPv6-only listener on `[::]:45993`
    (`ss -ltn`). The manager listens on `0.0.0.0`, which Go binds dual-stack,
    so that bind conflicts. The test pickers (`freeport.GetFreePort` on
    `localhost:0`, `pscFreePort` on `127.0.0.1:0`) only probe IPv4, so they
    could hand out 45993. It was not a pick-then-release race between tests.
  - Red: `TestPickedPortsBindForTheManager` (jobqueue) and
    `TestLaneFreePortOutsideALane` (client/testing) hold 1000 IPv6-only
    listeners and check the manager can bind every picked port. Both failed
    before the fix.
  - Fixed: a new `freeManagerPort()` asks the OS for `":0"`, the address the
    manager binds. `freeTestPort`, `laneFreePort` and `pscFreePort` use it, and
    `pscFreePort` re-checks the port after its parity bump.
  - Not fixed, outside this item: `cmd/status_test.go` `freeStatusTestPorts`
    still picks on `127.0.0.1:0` (its bind is retried). The production port
    finder `network/port/port.go`, used by `internal/config.go` to choose a
    user's port range, probes the hostname or loopback while the manager
    binds dual-stack.

- [x] **Stale "under LSF" in the RunnerReservation comment** (Copilot, PR
      #657, thread PRRT_kwDOAKD33M6n24d5, comment 4153211853,
      `jobqueue/job.go:995`). The comment says the field is set on a job
      reserved by a wr runner "under LSF", but runners on every scheduler set
      it now.
  - Red: `grep -n "under LSF" jobqueue/job.go` printed line 995. Fixed:
    the comment now says runners under any scheduler; the grep prints
    nothing.
- [x] CI `make race` on `7cf2d043` (this branch merged with develop
  `83efb248`, run 36894109586, job 110476656547) failed
  `jobqueue/confirmdead_slow_host_test.go` `TestConfirmDeadSlowHost`, "when a
  ps-only forced command answers one pid per command", line 159
  (`So(lost, ShouldEqual, slowHostRunners)`): `Expected: 10 Actual: 9`, so one
  of the ten reserved jobs was never seen `Lost` within `20*slowHostTTR` (6s).
  Local `make race` on the same head passed.
  - Not reproduced yet by: a 200ms or 700ms sleep after each key's
    `waitForJobLost` (2 and 3 runs passed, so a job confirmed dead keeps
    `Lost`); `taskset -c 0` on a `-race` test binary, `-count=5` twice; a
    `reservationQueuedHook` sleeping 2 TTRs for the first reservation, `-count=2`.
  - Cause (test): the setup loop made exactly 10 `Reserve` calls and kept
    the keys in a set. Under load the loop can outlast the first job's 300ms
    TTR, confirm-dead and release, so a later `Reserve` returns that job
    again and only 9 distinct jobs are watched. `Lost` is cleared only by a
    new reservation, so this is the only way to see fewer than 10 without a
    6s wait timing out (the CI test took 4.16s in all).
  - Red: a temporary 200ms sleep after `setServerJobPid` in the loop, `go
    test -tags netgo -count=1 -run '^TestConfirmDeadSlowHost$' ./jobqueue/`,
    failed both Conveys with `Expected: 10 Actual: 9` and `Actual: 8`.
  - Fix (test only): the loop reserves until it has 10 distinct jobs, giving
    each reservation a dead pid, bounded by `slowHostReclaimWait`. With the
    same sleep it passes 3 of 3 (11-13 reservations per Convey); `-race
    -count=5` passes. No assertion changed.
  - Reviewer: PASS. `make lint` 0 issues.
- [x] Local `make race` on `180df1a5` failed
  `jobqueue/archive_stall_test.go` `TestArchiveStallDoesNotRerunJob`, "a
  repeat of the runner's archive during the stall also succeeds", line 191
  (`So(pending(1), ShouldBeTrue)`), after a 30s wait; the log had `slow
  request method=jstart duration=30.01s`. Cause (test): the test holds a
  bolt write transaction once the job's `StartTime` is set in memory, but
  `handleStart` sets it (`applyJobStart`) before its durable write, so a
  start write still pending is stalled behind the held transaction. The
  runner settles its start report before its archive, so no archive arrives.
  - Red: a temporary 500ms sleep before `handleStart`'s
    `updateJobAfterChangeDurable`, `go test -tags netgo -count=1 -run
    '^TestArchiveStallDoesNotRerunJob$' ./jobqueue/`, failed at line 191.
    Unmodified, `-race -count=4` passed.
  - Fix (test only), `jobqueue/archive_stall_test.go`: the outer Convey sets
    the `startPersistedHook` seam before `serve()` (reset after the server
    stops) and waits, bounded by `startDurabilityAckWait`, for the start to be
    on disk before holding the transaction, instead of for an in-memory
    `StartTime`. The other tests that hold `bolt.Begin(true)` either call the
    synchronous `Started` first or stall the write they test on purpose.
  - After: with the same 500ms sleep it passes (2 of 2); `-race -count=3 -run
    '^TestArchiveStall'` passes.
  - Reviewer: PASS. `make lint` 0 issues.
