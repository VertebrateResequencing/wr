# Leave a running dependent to finish when its dep group gains a member

Branch `fix-running-dependent-rerun`. The filename has a suffix rather than a
sequence number so it cannot collide with another branch's `260929-N.md` (see
`260917-start-durability.md`).

Quality gates, with all `OS_*` unset and `GOCACHE` outside the home
directory: `make lint`, `make test`, `CGO_ENABLED=1 make race`.

- [x] **A running job whose dep group gains a member is clobbered.** Reported
      by the owner:

      > when a job is running and one of its dep groups gains a new member (a
      > new job is added with a dep_grp that the running job depends on), the
      > manager's q.Update replaces the running job in memory, so when the
      > running job finishes its archive is rejected and it runs again / state
      > goes wrong.

      Decided behaviour (option c): the running dependent is left to finish,
      not killed, and its in-memory record is not replaced; its completion is
      accepted and recorded normally; it then goes back to dependent, waiting
      on the new member, and runs again once its dependencies are satisfied,
      the same as a complete dependent. This must survive a manager restart
      between the add and the completion.

  - Red command:
    `GOCACHE=/tmp/claude-11346/gocache-rundep go test ./jobqueue -run 'TestRunningDependentRerun$' -count=1`
    exits 1:

    ```
    Line 119:
    Expected: queue.ItemState("run")
    Actual:   queue.ItemState("dependent")

    Line 79:
    Expected: nil
    Actual:   'jobqueue jarchive(cf81c78c45adb8e9d5bf58072e73fcf9): bad job (not in queue or correct sub-queue)'
    ```

  - Path: `db.storeNewJobs` -> `retrieveDependentJobs` returns every live
    dependent (freshly decoded from the live bucket) in `jobsToUpdate`, and
    `Server.updateJobDependencies` -> `applyDependencyUpdates` calls
    `s.q.Update` on each, which replaces the item's data with the decoded
    copy (`item.SetData`) and, via `queue.updateDependencies` ->
    `moveToDependentQueue`, moves a running item out of the run sub-queue.

  - Fix: a new persisted `Job.RerunAfterRun` mark, and
    `jobqueue/running_dependent.go` (the design is in its header comment).
    - After its write, the add goes through every live dependent it read.
      One that is queued but not running gets its new deps
      (`queue.UpdateUnlessRunning`). One that is running is marked under the
      queue lock, and the mark is stored in a bolt transaction under the job
      lock before the add replies; nothing is stored once its archive is
      pending (`db.storeRunningRerunMarks`). One that has already been
      archived and is no longer live is put back live, then queued like a
      resurrected complete dependent.
    - A marked job's archive writes its complete record and keeps a cleaned
      live record in the same transaction. The item is kept out of
      `queue.RemoveUnless`, and the last archive of the completion sends it
      back to dependent with `queue.Requeue`, so direct dependants keep
      waiting.
    - Release, and a lost job confirmed dead, go to dependent while deps are
      unresolved. Bury stays buried with the new deps, and a kick makes it
      dependent (spec B2).
    - Recovery marks a job recovered into run that has unresolved dep-group
      deps, keeping it in run, and clears the mark on any other job.
    - Duplicate in-flight archives of one completion (a runner resending
      after a stalled commit) are written once (`Job.archivedEndTime`), so the
      second one cannot undo a rerun queued by an add in between. This was a
      pre-existing bug the review found; it has its own CHANGELOG entry.
  - Files: `jobqueue/{running_dependent.go,db.go,job.go,server.go,serverCLI.go}`,
    `queue/queue.go`, `cmd/add.go` help, `.docs/dep-granularity/spec.md`,
    `CHANGELOG.md`. Tests: `jobqueue/running_dependent_rerun_test.go`,
    `jobqueue/running_dependent_archive_test.go` and
    `queue/running_update_test.go`, covering a clean restart, crash images,
    races driven by test hooks, release, bury and kick, lost jobs, and
    duplicate archives. The `db.archiveJob` test call sites were edited
    mechanically to drop the `ctx` parameter.
  - Known, not fixed (pre-existing, also affects the older resurrection of a
    complete dependent): an add that resurrects a job in the short in-memory
    window between its archive's `RemoveUnless` and that archive's
    `releaseDepGroupMembership`/rep-group lookup delete loses the resurrected
    job's in-memory dep-group membership and rep-group lookup until the next
    restart. Closing it needs an ordering between those two paths.
  - Known, not fixed: a crash after an add's write but before its reply loses
    the rerun of a dependent whose archive was written after the add read it.
    The client saw no reply, so it retries the add.
  - Newly observed, pre-existing flake, not caused by this change: a test
    manager's first `serve` sometimes fails with `bind: address already in
    use` ("the server's publication gave up"), for example in
    `TestReaddQueuedKeepsRecord` (`readd_queued_test.go:223`). The cause is
    the pick-then-release port race in `pickTestPort` via
    `isolateTestConfig`. It passed on rerun and needs a separate fix.
