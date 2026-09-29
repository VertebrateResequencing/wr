# Stop a re-add overwriting the stored record of a queued job

Branch `fix-readd-overwrites-running-job`. The filename has a suffix rather
than a sequence number so it cannot collide with another branch's `260929-N.md`
(see `260917-start-durability.md`).

Quality gates, with all `OS_*` unset: `make lint`, `make test`,
`CGO_ENABLED=1 make race`.

- [x] **A job re-added while running runs twice after a manager crash.**
      Reported from prodsim round 5 (reproduction
      `developers/soak4/readdcrash.sh` on branch `soak4` in `../wr-soak4`):

      > `client.Scheduler.SubmitJobs` and `wr add --rerun` add with
      > `ignoreComplete=false`. ibackup's server does this in production,
      > re-adding the same put jobs every minute. In that mode
      > `jobsNotAlreadyQueued` doesn't filter out jobs already in the queue.
      > `storeNewJobs` then calls `putEncodedJobs`, which overwrites a RUNNING
      > job's live DB record with a fresh, unstarted copy. In memory the add
      > only counts as a duplicate, so nothing looks wrong until a manager
      > crash. Recovery then reads the fresh copy (not reserved or running),
      > dispatches the job again, and the old runner's reports are rejected
      > ("bad job"), so the job runs twice concurrently. Seen 2 times in the
      > soak, and reproduced 2/2 with readdcrash.sh.

  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run 'TestReaddRunningJobSurvivesCrash|TestReaddQueuedKeepsRecord|TestStoreNewJobsKeepsHandedOutRecord'`,
    exit 1 before the fix:

    ```text
    TestReaddRunningJobSurvivesCrash, runCount(marker) after recovery:
      Expected: 1
      Actual:   2
    TestReaddQueuedKeepsRecord, live record unchanged by the re-add:
      Expected: true  Actual: false   (reserved, running, delayed, buried)
    TestStoreNewJobsKeepsHandedOutRecord, live record unchanged by the add:
      Expected: true  Actual: false   (reserved, running, lost, delayed, buried)
    ```

    With the record-equality assertion moved first, the crash test also showed
    the recovered image's live record differs from the started one.

  - Root cause: `ignoreComplete=false` is meant to re-run jobs that
    COMPLETED. `jobsNotAlreadyQueued` returned every input job unfiltered in
    that mode, so `prepareNewJobs` encoded each fresh input copy and the
    new-job writer (`newJobStores`' live-bucket store, `putEncodedJobs`) put it
    over the live record of a job that was already queued, whatever its state.
    `enqueueItems` then counted the job a duplicate and left the in-memory job
    alone, so memory and disk disagreed until a crash made recovery read the
    fresh copy. The same unfiltered path also let a duplicate re-add of a
    queued dep-group member re-run that group's completed dependents.
  - Fix:
    - `jobsNotAlreadyQueued` now leaves out every queued job in both modes and
      counts it a duplicate, so nothing about it is stored. With
      `ignoreComplete=false` it keeps only a complete job a dependency re-run
      put back in the queue and that has not been handed out again
      (`queuedCompleteRerunnable`: job state complete, no archive pending, item
      not in the run sub-queue), which `replaceLiveRerunItems` re-runs as
      before. `resurrectedCompleteRepGroup` uses the same test, so a complete
      job whose archive is under way or failed (still in the run sub-queue with
      a runner) is no longer replaced in the queue under that runner.
    - The add's live-bucket write (`putNewLiveJobs`, used by the folded and the
      chunked add paths) no longer replaces a live record whose job has been
      handed out (reserved, running, lost, delayed, buried, suspended, or
      attempted or started; a complete job's record is still replaced). That
      covers two adds of the same new job racing past the queue check, where
      the first one's job is reserved before the second one's write lands.
    - A queued job re-added with `ignoreComplete=false` under another
      RepGroup can still be found by that RepGroup, as before
      (`TestJobqueueExecutionAndDependencyScenarios` "You can add dups and a new
      one under a new RepGroup"): `recordQueuedRepGroups` stores only its
      RepGroup lookup (`db.storeRepGroupLookups`) and records it in `s.rpl`.
      A re-add under the queued job's own RepGroup, which is ibackup's
      every-minute pattern, already has that lookup, so it writes nothing to
      the database and does not mark it for backup (review follow-up).
    - Changed prior behaviour: `.docs/dep-granularity/spec.md` D1 acceptance
      tests 5 and 6 documented that a `--rerun` re-add of a live member without
      its dep group drops it from the group and releases the group's waiters,
      justified by the fresh copy overwriting the stored record, which a
      restart would read. That overwrite is this bug, so the justification no
      longer holds: the re-add is now a duplicate, the member stays in the
      group, and a restart agrees. `TestDepGranularityAddRerunDropsDepGroup` is
      renamed `TestDepGranularityAddRerunKeepsDepGroup` and asserts that, and
      `TestDepGranularityAddDropAndJoinInOneCall` now expects both members and
      a release only once both complete. The spec carries a superseded note.
  - Tests (`jobqueue/readd_queued_test.go`):
    `TestReaddRunningJobSurvivesCrash` (add, reserve, start, re-add with
    `ignoreComplete=false`, crash onto a bolt snapshot, restart: the job
    recovers running and owned by its runner, a fresh runner is not given it,
    the runner's touch and archive are accepted, it ran once, and the stored
    record is the started one); `TestReaddQueuedKeepsRecord` (ready, reserved,
    running, delayed, buried and dependent jobs keep their stored record and
    state on a re-add and count as duplicates; a complete job is re-run with a
    fresh record); `TestStoreNewJobsKeepsHandedOutRecord` (the writer guard).
    `wr add --rerun` of a complete job and dep-group re-runs are also covered by
    the existing suite.
  - Audit of other paths that write a live record or replace an in-memory job
    from another copy:
    - Modify (`modifyJobs`): pauses the server, skips run-queue items, rejects a
      key change onto an existing job (`skipForDuplicateKey`), and writes the
      modified in-memory jobs. No mismatch found.
    - Kick (`kickJobs`) and the best-effort/exit writers (`applyChanges`,
      `jobExitData.update`): write the in-memory job, and only while its live
      record exists. No mismatch found.
    - NOT FIXED, reported: adding a new member to a dep group whose dependent
      job is RUNNING. `retrieveDependentJobs` returns the dependent decoded
      from its live record, and `updateJobDependencies` -> `q.Update` replaces
      the in-memory job with that decoded copy and moves the run item to the
      dependent sub-queue. A probe showed the item go from `run` to
      `dependent`, the in-memory job object replaced, and the runner's archive
      rejected with "bad job (not in queue or correct sub-queue)". The job then
      runs again once the new member completes, possibly while the first run is
      still going. The right behaviour for a running dependent (leave it, kill
      and re-block it, or re-queue it after it finishes) is a design choice,
      so it is left for a decision.
    - Residual, not fixed: a dependency re-run that resurrects an archived job
      in the moment between `archiveCompletedJob`'s database archive and its
      `q.Remove` writes a live record for it while the queue add counts it a
      duplicate and the remove then drops it, so it re-runs only after a
      restart. Narrow, and not a double run.
  - Real-artifact check, `REPRO_ROOT=... REPRO_WR=<binary> bash readdcrash.sh 1`
    (local scheduler, kill -9 of the manager after two `wr add --rerun`s of the
    running job): the develop 7a74da59 binary ran the command twice (`marks`
    shows two `run` pids, and the log has "jtouch ... you must Reserve()" and
    "jarchive ... you must Reserve()" errors); the fixed binary ran it once,
    Attempts 1, with no errors.
  - Review (follow-up commit): `jobsNotAlreadyQueued` now passes on only the
    duplicates added under a RepGroup other than their queued job's, so a
    same-RepGroup re-add costs no bolt commit or backup;
    `queuedCompleteRerunnable` reads `item.State()` rather than building
    `item.Stats()`. `TestReaddQueuedKeepsRecord` gains a no-database-write case
    (red on 31a59b6a: 7 bolt writes before, 9 after), and
    `TestStoreNewJobsKeepsHandedOutRecord` also covers suspended and attempted
    ready records. Checked and left as is: re-adding a buried or delayed job
    with `--rerun` never reset or retried it in a running manager (the queue
    add was always a no-op duplicate; only a restart read the fresh record), so
    no workflow loses a retry mechanism; `wr retry` remains the way to do that.
    D1 tests 5 and 6 changed only unreleased behaviour (dep-granularity, #555,
    is in `[Unreleased]`), so the CHANGELOG's "Adding a command that is already
    queued now changes nothing about it" covers it.
  - Gates: `make lint` 0 issues; `make test` 806 passed, 21 skipped;
    `CGO_ENABLED=1 make race` 806 passed, 20 skipped.
