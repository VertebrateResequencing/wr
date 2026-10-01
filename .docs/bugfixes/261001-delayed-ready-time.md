# Show a delayed job's real ready time

Branch `fix-delayed-ready-time`. Quality gates, with all `OS_*` unset,
`GOCACHE=/tmp/claude-11346/gocache-delaytime` and `GOFLAGS=-p=2` under
`nice -n 19`: `make lint`, `make test`, `CGO_ENABLED=1 make race`.

Reported by the owner from a production `wr status` (attempted 26/7/25):

```text
Status: delayed following a problem, prior to retrying; will become ready in -2562047h47m16.854775808s (attempted at 26/7/25-15:53:30)
Previous problem: lost contact with runner
```

`-2562047h47m16.854775808s` is `math.MinInt64` as a `time.Duration`:
`cmd/status.go` prints `time.Until(job.EndTime.Add(job.DelayTime))`, which
saturates when `EndTime` is the zero time.

The queue's own readiness is not affected: on release `queue.Item.restart`
sets `readyAt = now + delay`, independent of `Job.EndTime`. Recovery after a
restart re-adds delayed jobs with `Delay: 0`, so they come back ready. Only the
stored `EndTime` and what is derived from it are wrong.

- [x] **The manager's own release of a lost job zeroes its EndTime.**
      `lostJobReleaseReport` (the confirm-dead release after TTR expiry, the
      `LostRunnerBackstop` release, and `killRunningJob` of a lost job) passes
      `&JobEndState{Exitcode: -1, Exited: true}` with no `EndTime`, and
      `Job.updateAfterExit` copies that zero over the time `ttrCallback`
      stamped when it marked the job lost. The delayed or buried job then has
      a zero `EndTime`: `wr status` prints the saturated ready time above, and
      `WallTime()` grows for ever for a job that had started. Present since at
      least v0.32.0.
  - Red command (fix reverted), exit 1:
    `go test ./jobqueue -run TestReleasedLostJobKeepsTheTimeContactWasLost -count=1`

    ```text
    lost_job_behaviours_test.go Line 1202:   (job.EndTime.IsZero() ShouldBeFalse)
    Expected: false
    Actual:   true
    (twice: the confirm-dead release and the kill of a lost job)
    --- FAIL: TestReleasedLostJobKeepsTheTimeContactWasLost (2.48s)
    ```
  - Fixed in `jobqueue/job.go`: `updateAfterExit` keeps the job's own EndTime
    when the end state has none (stamping now only if the job has none
    either), so a lost job's release keeps the time contact was lost.
  - Tests: `TestReleasedLostJobKeepsTheTimeContactWasLost` in
    `jobqueue/lost_job_behaviours_test.go` (confirm-dead release and kill of a
    lost job); `TestREST` in `jobqueue/rest_test.go` asserted the bug (a
    lost-then-buried job's `Ended` was nil) and now expects it set.
- [x] **`wr status` derives the ready time from EndTime+DelayTime instead of
      the queue.** A delayed job whose `EndTime` is zero for any other reason
      (a runner release with no exit state, such as "not enough time to run",
      leaves the reservation's zeroed `EndTime`; or a job stored by an older
      manager) still prints a saturated or negative duration, and a job that
      never started prints "attempted at" the zero time. The client copy of a
      delayed job should carry the queue item's real ready time, and the
      display should never print a negative or saturated duration.
  - Red commands (field and helpers added, behaviour unchanged), exit 1:
    `go test -count=1 ./jobqueue -run TestDelayedJobReportsWhenItWillBeReady`
    and `go test -count=1 ./cmd/ -run 'TestStatusDelayedLine|TestStatusDetailsOfAJobReleasedWithNoExitState'`

    ```text
    delayed_ready_time_test.go Line 94 (now 96):
    Expected '0001-01-01 00:00:00 +0000 UTC' to happen between [release+DelayTime] (it happened '2562047h47m16.854775807s' outside threshold)!
    status_delayed_test.go:
    Expected: "...will become ready imminently"
    Actual:   "...will become ready in -2562047h47m16.854775808s (attempted at 01/1/1-00:00:00)"
    Expected: "Status: buried - you need to fix the problem and then `wr retry`"
    Actual:   "Status: buried - you need to fix the problem and then `wr retry` (attempted at 01/1/1-00:00:00)"
    --- FAIL: TestStatusDelayedLine
    --- FAIL: TestStatusDetailsOfAJobReleasedWithNoExitState
    ```
  - Fixed: new `Job.ReadyTime` (codec omitempty), set only on client copies
    from the queue item's `ReadyAt()` in `itemToJobIfAdmitted` and cleared on
    jobs clients add (`prepareInputJobs`), so nothing new is persisted; new
    `JStatus.Ready` for `wr status -o json` and REST; `cmd/status.go` prints
    the remaining time from `ReadyTime`, falls back to `EndTime+DelayTime`
    only when `EndTime` is set (older manager), says "imminently" when the
    time is past or unknown, and omits "attempted at" for a job that never
    started (delayed and buried lines).
  - Tests: `jobqueue/delayed_ready_time_test.go`,
    `cmd/status_delayed_test.go`.
- [x] **Recovery after a restart skips a delayed job's remaining delay.**
      Raised by the coordinator: `recoveredItemDef` re-adds every non-run,
      non-buried, non-suspended job with `Delay: 0`, so a job delayed before a
      restart is ready at once afterwards. Decide whether to restore the
      remaining delay.
  - Decision: no change; the long-standing behaviour is kept, so there is no
    red test.
    - The delay is a backoff courtesy, not a correctness guarantee: nothing
      breaks if a job is retried early, and the next failure still backs off
      again (`setItemDelay` at reservation).
    - The stored fields cannot reproduce the queue's real ready time. The
      queue's `readyAt` is not persisted; `EndTime` of a lost job is when
      contact was lost, before the confirm-dead release started the delay;
      and a runner release with no exit state stores no `EndTime` at all.
      Restoring would be a guess for some jobs and impossible for others.
    - Jobs delayed after "lost contact with runner" are often victims of the
      manager's own trouble that the restart addresses, so retrying them
      promptly after a restart is the useful outcome.
    - After a restart a recovered delayed job is in the ready sub-queue and
      `wr status` reports it as ready, so the display is consistent.
