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
