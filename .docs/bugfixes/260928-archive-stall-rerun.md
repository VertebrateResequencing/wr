# Keep a finished job in the run queue until its archive commits

Branch `fix-double-run-under-commit-stall`. The filename has a suffix rather
than a sequence number so it cannot collide with another branch's `260928-N.md`
(see `260917-start-durability.md`).

Quality gates: `make lint`, `make test`, `CGO_ENABLED=1 make race`.

- [x] **Jobs that exited 0 ran again while a database commit was stalled, with
      no manager crash.** Reported from prodsim round 3, run 1
      (`/nfs/hgi/wr/sb10-bigdb/prodsim3/prodsim-1790591410/`):

      > The /nfs/hgi filesystem filled up (ENOSPC), so one DB commit stalled for
      > 3m45s. That window produced 8,092 slow requests, 1,015 "reservation not
      > yet recorded on disk, handing the job out anyway" warnings, 49 jarchive
      > "you must Reserve() a Job" rejections, and 2,569 "Remove not found"
      > errors. In that window, 26 portal jobs that had exited 0 ran again.
      > There was NO manager crash. The manager ran without --debug.

  - Evidence: there were two stalls, not one. `archive fold` lines in
    `manager.log`:

    ```text
    12:50:23 txs=4 archives=4808 maxFold=4172 maxWait=3m45.651s maxTx=3m45.663s
    12:52:23 txs=2 archives=502 maxFold=501 maxWait=1m52.038s maxTx=1m52.038s
    12:52:15 msg="slow request" method=add selector="jobs=10" duration=1m53.56684101s replyErr="failed to use database"
    12:52:15 err="jobqueue add(): no space left on device"
    ```

    The first stall ran from about 12:46:11 to 12:50:00, the second from about
    12:50:24 to 12:52:16.

  - Evidence: `markers/` records 27 portal keys that ran again after a run that
    exited 0. My own pass over the S/E markers gave this (the reported 26
    counted them another way):

    ```text
    count  run1 ended  run2 started  run2 exit
     1     12:46       12:50:11      none
     1     12:47       12:50:11      none
    22     12:50       12:52:13      0
     3     12:50       12:52:25      none
    ```

    For example, `portal_dedupe 20260928T124108.16543` ran on node-13-07 from
    12:50:19.440 to 12:50:27.904 and exited 0. It ran again on node-13-08 from
    12:52:13.502 to 12:52:20.476 and exited 0. The 22 second runs all started
    within 40ms of each other, most of them on node-13-08. The 57 keys handed
    out at 12:52:13 (`handing the job out anyway ... waited=10s`, so reserved
    at 12:52:03) each get a `jarchive(...): queue(cmds) Remove(...): not found`
    at 12:52:22-25. Logs without --debug carry no commands, and the database
    was not kept, so a marker id cannot be joined to its key.

  - Every re-run fits a single mechanism:
    1. A command ends, and the runner stops touching (`Execute` sends
       `stopTouching` before `reportFinalState`) and sends `jarchive`.
    2. `handleArchive` -> `markJobComplete` sets `Exited = true` and
       `State = complete` in memory. Then `archiveCompletedJob` waits in
       `db.archiveJob` for the fold transaction, which is stalled.
    3. The item is still in the Run sub-queue, and nothing touches it. After
       ItemTTR (60s) `ttrCallback` runs. Its first branch sends any `Exited`
       job to `SubQueueDelay`, the path meant for an item that was released
       and is waiting out its delay.
    4. After the 30s delay the job is ready. A runner reserves it, and #642's
       `ReserveWriteWait` fallback hands it out 10s later. That fallback is
       working as designed: it is not the cause.
    5. The fold commits and the first archive's `q.Remove` takes the item away
       from under the second runner. A second run that was still starting then
       gets `jstart ... bad job` and is killed (the runs with no end marker). A
       short second run finishes and gets `Remove not found`.

    The timings match: run1 ends at about 12:50:26, then +60s TTR, +30s delay
    and +10s ReserveWriteWait gives 12:52:13. The stall-1 pair ended at
    12:46-12:47 and was handed out at 12:50:11, as soon as reservations
    flowed again.

  - The other signatures come from the client's 60s request floor. A runner
    whose archive times out sends it again. When the retry reaches the queue
    before a second runner reserves the job, both archives fold into one
    commit. The second `q.Remove` then fails and returns `ErrInternalError`
    ("Remove not found", 2,633 in the whole log), which is harmless but noisy.
    When a second runner reserves the job first, the retry gets
    `ErrMustReserve` ("you must Reserve", 49). The first archive is still
    queued and commits anyway.

  - Ruled out: a crash-restart, since the only restart (12:14) came 32
    minutes before the stalls. Confirm-dead is not needed to explain these
    runs. Its log lines are at info level, so this warn-level log cannot show
    whether it ran. But `ttrCallback` tests `Exited` before it marks a job
    lost, so a job whose archive is in flight goes straight to Delay and is
    never confirm-checked. For confirm-dead to re-run a job, the job would
    have to have been declared lost while its command was still running, and
    these commands ran for 4-9s after the first stall had ended. Touches queued
    behind the stall are ruled out for the same reason: the commands had
    finished, so their runners were not touching. The `build
    wrstat-ui-summarise-*` and `put` repeats in the markers happened outside
    both stalls and were not investigated here.

  - Red command:
    `CGO_ENABLED=0 go test -tags netgo -count=1 -run 'TestArchiveStallDoesNotRerunJob|TestKillLostRunLeavesPendingArchive' ./jobqueue/`.
    Before the fix it failed 5 times in 5 runs:

    ```text
    archive_stall_test.go Line 156: Expected: false  Actual: true   (a second runner reserved the job)
    archive_stall_test.go Line 216: Expected: nil    Actual: 'jobqueue jarchive(...): internal error'
    archive_stall_test.go Line 311: Expected: false  Actual: true   (killLostRun released the job)
    ```

    Each case was checked red on its own by removing only its part of the fix.

  - Fix: `Job.archivesPending` (server side only) counts the successful
    completions whose archive is in flight. `markJobComplete` increments it,
    and `archiveCompletedJob` decrements it when it returns, whatever the
    outcome. While it is non-zero:
    - `ttrCallback` keeps the job in Run with a fresh TTR, before the
      `Exited` -> Delay branch;
    - `killRunningJob` neither marks nor releases it, so neither
      confirm-dead nor `wr kill` can re-run a job that has already finished.

    If the write fails, the count drops back to zero and the old path applies:
    the job is Exited in Run, the runner's retry can still archive it, and a
    TTR expiry releases it if no retry does. A second archive of the same
    completion whose `q.Remove` finds the job gone now succeeds, because its
    own write committed. The first archive already did the dep-group, rpl and
    group-count bookkeeping.

  - #642's guarantees are unchanged. Reservations are still written durably
    within ReserveWriteWait, and nothing changed in the start, recovery or
    new-run-wins (`ErrMustReserve`) paths.

  - Files: `jobqueue/job.go`, `jobqueue/serverCLI.go`, `jobqueue/server.go`,
    `jobqueue/archive_stall_test.go`, `CHANGELOG.md`.

  - Review: `archivesPending` is only changed under the job lock, on one path.
    `markJobComplete` increments it as its last fallible step, `handleArchive`
    calls `archiveCompletedJob` straight after, and that defers the decrement
    before anything else. `db.archiveJob` always returns: the archive writer
    replies to every op, and `failPendingArchives` fails any left at shutdown.
    Recovery builds new Jobs, so the count starts at 0. The review added two
    tests to `TestKillLostRunLeavesPendingArchive`: a user's `wr kill` while an
    archive is pending neither marks nor releases the job (it counts as 0
    killed), and an archive whose write fails drops the hold, so a TTR expiry
    sends the job to delay again. It also reworded confirm-dead's "did not
    kill" log line to cover a success being saved.

  - Review: the fix made `-race` fail `TestLostCwdMattersJobSparesItsSecondRun`
    in 4 of 4 runs of the targeted set. The last leaf of
    `TestLostJobRetryCheckFindsAReservedNotStartedRun` calls `markJobComplete`
    with the manager parked at its dead-check. Its kill used to release the
    job, and `awaitLostRunBehaviours` waited for the behaviours that followed.
    Now the archive is pending, so the kill is refused and no behaviour runs.
    Nothing then ordered the manager's read of `lostJobKilledHook` before the
    next fixture's `installHooks`. The leaf now lets the manager go on and
    waits for its kill decision, and asserts that it is refused.
