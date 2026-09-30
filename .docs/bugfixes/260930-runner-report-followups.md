# Follow-ups to the release durability fix

Branch `fix-runner-report-followups`, built on #654 (`fix-release-lost-in-crash`,
4cfae2b2). The items are the ones that PR left as possible follow-ups. The
filename has a suffix rather than a sequence number so it cannot collide with
another branch's `260930-N.md`.

Quality gates, with all `OS_*` unset, `GOCACHE=/tmp/claude-11346/gocache-followA`,
`GOFLAGS=-p=2` and `WR_TESTSUITE_MAX_PARALLEL=2` under `nice -n 19`:
`make lint`, `make test`, `CGO_ENABLED=1 make race`.

- [x] **1. An old owner's accepted report can act on a new run.** After
      getijForReport has accepted an old owner's report (jrelease/jbury), a new
      reservation can land before the release snapshot is taken, and the old
      owner's report then acts on the new run. Close it, e.g. by checking the
      job's ReservedBy against the reporter inside the snapshot lock, returning
      ErrMustReserve/ErrBadJob as appropriate.
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestOldOwnerReportAfterNewReservation$'`, exit 1 on 4cfae2b2 plus the
    test. The manager releases a lost job, the user suspends and resumes it,
    and a nil-in-production `releaseReportAcceptedHook` has a new runner
    reserve it after handleRelease accepted the old owner's report. The old
    owner's bury buried the new run and its release delayed it, both
    acknowledged:

    ```text
    Line 97: Expected: queue.ItemState("run") Actual: queue.ItemState("bury")
    Line 97: Expected: queue.ItemState("run") Actual: queue.ItemState("delay")
    --- FAIL: TestOldOwnerReportAfterNewReservation (1.29s)
    ```

    handleArchive already re-checks ReservedBy under the job lock
    (markJobComplete), so only release and bury have the gap.
  - Fixed: `releaseReport` carries the reporting runner's client ID
    (`reporter`, zero for a manager-initiated lost, TTR or kill release).
    `releaseJobSnapshot` checks it against `job.ReservedBy` under the same
    job lock as the rest of the snapshot; on a mismatch the snapshot is
    `supplanted`, `applyReleaseQueueChangeForRerun` returns
    `errReleaseReporterSupplanted` before touching the rerun mark or the
    queue, and `handleRelease` answers ErrMustReserve, which the runner gives
    up on. Nothing is finalized, counted or written. Files:
    jobqueue/server.go, jobqueue/serverCLI.go, jobqueue/running_dependent.go,
    new jobqueue/runner_report_followups_test.go (fixture `connect` in
    jobqueue/release_after_lost_test.go), CHANGELOG.md.
  - Review: `make lint` 0 issues; `go test ./jobqueue ./queue` passed (a
    first run without `-timeout` hit Go's 10m default at 600s on the loaded
    host; the project's suite runner uses 40m, so ad-hoc package runs here
    now pass `-timeout 40m`). Left for item 1b: a reservation moves the item
    to Run before it sets ReservedBy, so a snapshot between the two still
    passes the check, and the same holds for an archive's markJobComplete.
- [ ] **2. After the manager's own lost release, the owner's exit code and
      peak RAM are not recorded.** The release already marked the job exited.
      Record them when the owner's report arrives for a job still waiting from
      that lost release, without double-spending retries or double-decrementing
      counts.
- [ ] **3. An owner's jbury of a job the manager requeued as dependent gets
      ErrBadJob.** A job the manager has put back in the dependent queue with
      rerun deps (#649's RerunAfterRun / dep-group re-block) answers the
      owner's jbury with ErrBadJob, so the job is not buried. Owner policy:
      "stop means buried"; the owner's bury should leave it buried (a kick
      then makes it dependent, per spec B2 and #653).
- [ ] **5. A re-sent release after a kick may spend a retry from the kicked
      budget.** Re-check after #654's last commit changed kick handling; fix if
      still real.
