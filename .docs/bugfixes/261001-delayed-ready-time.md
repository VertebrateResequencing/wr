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
- [ ] **`wr status` derives the ready time from EndTime+DelayTime instead of
      the queue.** A delayed job whose `EndTime` is zero for any other reason
      (a runner release with no exit state, such as "not enough time to run",
      leaves the reservation's zeroed `EndTime`; or a job stored by an older
      manager) still prints a saturated or negative duration, and a job that
      never started prints "attempted at" the zero time. The client copy of a
      delayed job should carry the queue item's real ready time, and the
      display should never print a negative or saturated duration.
