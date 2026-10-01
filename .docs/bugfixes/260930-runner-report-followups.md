# Follow-ups to the release durability fix

Branch `fix-runner-report-followups`, built on #654 (`fix-release-lost-in-crash`,
4cfae2b2) and rebased onto develop 499b350e once #654 merged and v0.38.0 was
released. The items are the ones #654 left as possible follow-ups. The
filename has a suffix rather than a sequence number so it cannot collide with
another branch's `260930-N.md`.

Quality gates, with all `OS_*` unset, `GOCACHE=/tmp/claude-11346/gocache-followA`,
`GOFLAGS=-p=2` and `WR_TESTSUITE_MAX_PARALLEL=2` under `nice -n 19`:
`make lint`, `make test`, `CGO_ENABLED=1 make race`. An ad-hoc run of one
package passes `-timeout 40m`, as the suite runner does.

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
- [x] **1b. An old owner's report can still act on a new run caught part way
      through its reservation.** Coordinator's request, from item 1's
      remaining gap: a reservation moves the item to Run before setting
      ReservedBy, so a report whose snapshot falls between passes the
      ReservedBy check; the same gap exists for archive via markJobComplete.
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestOldOwnerReportDuringNewReservation$'`, exit 1 on b462d02d plus the
    test. After a lost release, suspend and resume, a new runner reserves the
    job, and a nil-in-production `reservationQueuedHook` in
    `respondWithReservedJob` has the old owner report after `q.Reserve` moved
    the item to Run and before `resetJobForReservation`. The bury buried the
    new run, the release delayed it, and the archive completed the job and
    removed its item, while the new runner was still handed the job:

    ```text
    Line 184: Expected: queue.ItemState("run") Actual: queue.ItemState("bury")
    Line 184: Expected: queue.ItemState("run") Actual: queue.ItemState("delay")
    Line 196: Expected: nil Actual: 'queue(cmds) Get(ec544e27...): not found'
    --- FAIL: TestOldOwnerReportDuringNewReservation (1.79s)
    ```

  - Fixed: `queue.Reserve` now counts the reservation (`ItemStats.Reserves`)
    under the same item lock as the move to Run (`switchReadyRunReserved`);
    an item recovered straight into Run no longer counts one, and nothing
    else read the count. `resetJobForReservation` records the item's count
    on the job (`Job.reservation`, server-only, not persisted, so 0 after
    recovery like the recovered item's). `Job.runHeldByLocked` accepts a
    report only if ReservedBy is the reporter and, for an item in Run, the
    two counts match. It is used under the job lock by `releaseJobSnapshot`
    (which now reads the item's stats once) and `markJobComplete`, and
    `getijForReport` returns the item for the latter. Files: queue/item.go,
    queue/queue.go, jobqueue/job.go, jobqueue/server.go,
    jobqueue/serverCLI.go, CHANGELOG.md; new `TestQueueReservesCountsReservations`
    in queue/queue_test.go; call-site updates in existing tests.
  - Review: PASS; `make lint` 0 issues; `go test ./jobqueue ./queue
    -timeout 40m` passed. Not covered, noted in review: an old owner's jstart
    or jtouch (getij checks only ReservedBy) landing in the same window; and
    a bury whose snapshot saw the owner's run in Run, if the manager releases
    that run and it is reserved again before the bury's queue change.

- [x] **2. After the manager's own lost release, the owner's exit code and
      peak RAM are not recorded.** The release already marked the job exited.
      Record them when the owner's report arrives for a job still waiting from
      that lost release, without double-spending retries or double-decrementing
      counts.
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestOwnerEndStateAfterLostRelease$'`, exit 1 on 4263cead plus the test.
    The manager confirms a started job dead and releases it (Exitcode -1,
    FailReasonLost); the owner then releases or buries it with exit code 3
    and peak RAM 1234. Both reports are acknowledged, but the job keeps the
    lost release's end state:

    ```text
    Line 256: Expected: 3 Actual: -1   (release)
    Line 256: Expected: 3 Actual: -1   (bury)
    --- FAIL: TestOwnerEndStateAfterLostRelease (1.33s)
    ```

    Cause: `Job.updateAfterExit` returns at once when `Exited` is already
    set, and a release of an item already waiting is `releaseAlreadyDone`,
    which skips `finalizeReleasedJob` altogether.
  - Fixed: `Job.replaceLostEndState` (jobqueue/job.go) records the owner's
    exited end state and fail reason, under the job lock, only while the job's
    FailReason is still FailReasonLost, which on a waiting job only the
    manager's lost release (confirm-dead or a kill of a lost job) leaves.
    `releaseJob` calls it for a runner's report on the `releaseAlreadyDone`
    path and then writes the job as a release does (durably before the ack),
    and `finalizeReleasedJob` calls it on the `releaseBuriedWaiting` path.
    No retry, scheduler group count or limit group is spent again, and a
    re-send finds the owner's FailReason and changes nothing. The end-state
    assignments are shared with `updateAfterExit` as `recordEndStateLocked`.
    Test `TestOwnerEndStateAfterLostRelease` also checks the stored record, a
    kill-released variant and a re-send. Files: jobqueue/job.go,
    jobqueue/server.go, jobqueue/runner_report_followups_test.go,
    jobqueue/reserve_durability_test.go (`storedLiveJob`), CHANGELOG.md.
  - Review: PASS on the fix; `make lint` 0 issues. The package gate failed
    once in `TestDepGranularityModifyChangesMemberKey` on a port bind,
    unrelated; it is item 6 below.
- [x] **3. An owner's jbury of a job the manager requeued as dependent gets
      ErrBadJob.** A job the manager has put back in the dependent queue with
      rerun deps (#649's RerunAfterRun / dep-group re-block) answers the
      owner's jbury with ErrBadJob, so the job is not buried. Owner policy:
      "stop means buried"; the owner's bury should leave it buried (a kick
      then makes it dependent, per spec B2 and #653).
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestOwnerBuryOfRequeuedDependent$'`, exit 1 on 88e9104e plus the test.
    A running dependent is marked to run again when its dep group gains a
    member, is marked lost and killed, so the manager requeues it as
    dependent; its owner then buries or releases it. Both get ErrBadJob,
    since getijForReport only accepts Run, Delay and Ready:

    ```text
    Line 342: Actual: 'jobqueue jbury(cf81c78c...): bad job (not in queue or correct sub-queue)'
    Line 356: Actual: 'jobqueue jrelease(cf81c78c...): bad job (not in queue or correct sub-queue)'
    --- FAIL: TestOwnerBuryOfRequeuedDependent (0.46s)
    ```

  - Fixed: `getijForReport` takes the sub-queue states its report accepts:
    a release or bury also accepts Dependent (`itemIsReleasable`), an archive
    does not (`itemIsInFlight`, as before), since archiving such an item
    would lose the run it still owes after its new dependencies.
    `itemIsWaiting` includes Dependent, so the owner's report spends no retry
    and gives back no count again; a release is `releaseAlreadyDone` (item 2's
    end-state recording applies), and a bury goes through `queue.BuryWaiting`,
    which now also buries a Dependent item with its dependencies recorded, so
    a kick makes it dependent. Files: queue/queue.go, queue/item.go
    (`switchDependentBury`), queue/queue_test.go, jobqueue/server.go,
    jobqueue/serverCLI.go, CHANGELOG.md.
  - Review: PASS; `make lint` 0 issues; `go test ./jobqueue ./queue` passed
    but for `TestSubscriptionLongPollOverExistingPort` failing on "bind:
    address already in use", item 6, which passed alone. Noted, not fixed: a
    hand-made request with a zero ClientID could release or bury a job that
    never ran (ReservedBy zero), as it already could for Delay and Ready; the
    real client always sends a random ID.
- [ ] **5. A re-sent release after a kick may spend a retry from the kicked
      budget.** Re-check after #654's last commit changed kick handling; fix if
      still real.
- [ ] **6. A jobqueue test can fail to start its manager on a busy host.**
      Found by item 2's review gate: `TestDepGranularityModifyChangesMemberKey`
      failed with `could not listen on the manager port, so exiting port=45077
      err="... bind: address already in use"` (depgranularity_recovery_test.go
      line 114, `dgrStartServer`). `isolateTestConfig` (jobqueue_test.go)
      picks a free port and the manager binds it later, so another process on
      the shared host can take it in between.
