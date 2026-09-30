# Make a job's release durable before the runner is told about it

Branch `fix-release-lost-in-crash`. The filename has a suffix rather than a
sequence number so it cannot collide with another branch's `260930-N.md`.

Quality gates, with all `OS_*` unset, `GOCACHE=/tmp/claude-11346/gocache-rellost`
and `GOFLAGS=-p=2` under `nice -n 19`: `make lint`, `make test`,
`CGO_ENABLED=1 make race`.

- [x] **A released job came back "running" after a crash and never re-ran.**
      Reported from the production-scale soak in
      `/nfs/hgi/wr/sb10-bigdb/soak6/` (code origin/develop 00be3401):

      > job ccfca92bf6b0013af87c1791078127ef (portal_dedupe
      > 20260929T222325.5565). Its command exited rc3 at 22:52:18; the runner
      > logged that the job would be retried (the release was acknowledged) and
      > the runner moved on to run other jobs. The manager was kill -9'd ~1s
      > later. Recovery brought the job back as "running". It stayed "running"
      > with no command behind it through 23:17 (27 minutes, two restarts) and
      > in the final DB, and never re-ran.

  - Evidence, runner log
    `run/runnerlogs/26.09.29/22-30-15.node-13-21.4091225` (runner pid
    4091225, command pid 616064):

    ```text
    22:51:46 msg="started executing" jobkey=ccfca92b... pid=616064
    22:52:18 lvl=warn msg="command [...] exited with code 3, which may be a temporary issue, so it will be tried again"
    22:53:17 msg="reserved a job" key=00537c60...   (then 22 more jobs)
    23:07:42 msg="wr runner exiting, having run 70 commands, ..."
    ```

    Manager log `run/prodsim-1790712929/manager.log` (crash at 22:52:19 per
    the rotated log name `manager.log.1790718739`):

    ```text
    22:51:59 msg="archive fold" ...                     (last line before the kill)
    22:53:12 msg="wr manager  started on ..., pid 562222"
    23:11:04 msg="wr manager  started on ..., pid 689245"
    23:12:18 lvl=warn msg="could not confirm whether a lost job's process is still running on its host" host=node-13-21 pid=616064 reason="the remote ps command failed: cloud SSHSession() cancelled"
    ```

    Final DB (`dbstart.tsv`) still holds the start-time record:

    ```text
    jobslive ccfca92bf6b0013af87c1791078127ef portal_dedupe 20260929T222325.5565 running -1 1 node-13-21 616064 1790718706293 0
    ```

  - Root cause: `handleRelease` -> `releaseJob` -> `finalizeReleasedJob` ->
    `db.updateJobAfterExit` only queues the released record for the
    best-effort writer and the reply goes out at once. Reservation
    (`persistReservation`), start (`handleStart`) and archive
    (`archiveCompletion`) all wait for their write to commit; release and bury
    never have (v0.32.4 wrote it in a goroutine, so this is not a regression
    from #642, #646, #647, #648 or #649). A kill in the drain window leaves the
    durable start record, recovery parks the job in Run, and confirm-dead never
    declares it dead while the runner pid lives (`jobConfirmedDead`), the
    runner having moved on. It re-checks only every
    `LostJobCheckRetryTime` (30m), and each restart re-marks it lost with a new
    `EndTime`, restarting both that and the 1h `LostRunnerBackstop` clock.
  - Red command (`GOCACHE=/tmp/claude-11346/gocache-rellost GOFLAGS=-p=2`):
    `nice -n 19 go test ./jobqueue -run 'TestReleaseDurability' -count=1`,
    exit 1. It holds bolt's write lock while the release (or bury) runs and
    snapshots the DB the moment the call returns:

    ```text
    release_durability_test.go:212: release acknowledged while its write was held off disk: true
    release_durability_test.go:226: recovered state running; fresh runner reserved it within 10s: false
    Expected 'running' to be in the container ([]jobqueue.JobState), but it wasn't!
    --- FAIL: TestReleaseDurability (10.74s)
    release_durability_test.go:259: bury acknowledged while its write was held off disk: true
    Expected: jobqueue.JobState("buried")
    Actual:   jobqueue.JobState("running")
    --- FAIL: TestReleaseDurabilityBury (0.28s)
    ```

    The same test fails the same way at 37fde351, before #642, so this is
    not a regression.
  - Fixed: `handleRelease` passes `durable` to `releaseJob`, whose exit write
    (`db.updateJobAfterExitDurable`) joins the best-effort writer's coalesced
    drain with a waiter, so the reply waits for the commit without a
    transaction per release. The reviewer counted 8 write transactions for
    2000 concurrent durable exits, the same as the async path. A re-sent report
    that finds the release already applied rewrites the job durably
    (`updateJobAfterChangeDurable`, live keys only, newest seq), and one on a
    job the same client already buried is acked once that is on disk
    (`handleReportOnOwnBuriedJob`) instead of ErrBadJob. A failed write returns
    ErrInternalError, which the runner re-sends. Manager-initiated releases do
    not wait. Files: jobqueue/db.go, jobqueue/server.go, jobqueue/serverCLI.go,
    new jobqueue/release_durability_test.go and
    jobqueue/release_durability_resend_test.go, CHANGELOG.md. Tests that
    released and then expected "delayed" within the 100ms test release delay
    now use a longer delay (rest_test.go, readd_queued_test.go,
    suspend_resume_test.go, cmd/suspend_test.go).

- [ ] **A re-sent release of a job whose first release spent its last spare
      retry is refused on every re-send.** Found while fixing the item above.
      With Retries=1 the first release leaves UntilBuried=1. On a re-send,
      `releaseJobSnapshot` treats the report as a fresh attempt, so remaining
      is 0 and bury=true. `applyReleaseQueueChange` then calls `q.Bury` on an
      item in Delay/Ready, which fails, and the report is answered with
      ErrInternalError. The runner re-sends until its retry time (about 24h)
      while the job itself re-runs from delay. Reproduced on unfixed code with
      `TestReleaseDurabilityResend` at Retries=1.

- [ ] **A live-record change can be queued out of order with its encoding.**
      Found in review of the first item. `queueJobChange` (jobqueue/db.go)
      encodes the job under `job.RLock`, releases it, and only takes its
      arrival sequence later under `beMu`. A write that encoded an older state
      (a kick, or a re-sent release now written durably) can queue after a
      concurrent `persistReservation` that encoded the newer reserved state.
      Latest-wins coalescing then keeps the older image on disk, while the
      reservation's waiter is told it committed. A crash before Started would
      recover the job off the Run queue, and it could run twice.
