# Close the gaps left by the running-dependent rerun

Branch `fix-dep-group-rerun-gaps`. Follows the "Known, not fixed" items in
`260929-running-dependent-rerun.md`. The filename has a suffix rather than a
sequence number so it cannot collide with another branch's `260930-N.md`.

Quality gates, with all `OS_*` unset, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: `make lint`, `make test`, `CGO_ENABLED=1 make race`.

- [x] 7. Short in-memory window (pre-existing): an add that brings back a job
  (re-runs a complete dependent, or marks a running one RerunAfterRun) between
  that job's archive removing it from the queue and the archive's dep-group
  and rep-group cleanup, loses the job's in-memory group membership until the
  next manager restart. The older path that brings back complete dependents
  has the same exposure.
  - Red command (exit 1 on develop 499b350e plus the test seam):
    `CGO_ENABLED=1 go test -tags netgo -count 1 ./jobqueue -run 'TestDepGroupRerunWindow$'`

    ```text
    Line 129:
    Expected collection to have length equal to [1], but its length was [0] instead! contents: []
    Line 137:
    Expected: queue.ItemState("dependent")
    Actual:   queue.ItemState("ready")
    (each twice: the older resurrection path, and an add that read the
    dependent while it ran and found it archived when it updated it)
    --- FAIL: TestDepGroupRerunWindow (0.73s)
    ```

    Line 129: the brought-back job is not found by its rep group. Line 137: a
    job added later with `--deps` on a dep group the brought-back job is a
    member of is ready, not dependent, so it would run before the brought-back
    job runs again. With the seam moved after the archive's cleanup, the test
    passes.
  - Root cause: `finishArchive` removed the item with `q.RemoveUnless` and then
    always ran `releaseDepGroupMembership` and `rpl.Delete`. An add could bring
    the job back in between (`storeNewJobs` resurrecting a complete dependent,
    `updateLiveDependents` -> `resurrectArchivedDependents` for one it read
    running, or a re-add of the job itself). The add registers memberships
    before it queues the job and records the rep group once queued, so the
    archive's later cleanup dropped both, and satisfied any group the job had
    just emptied, releasing its waiters while it was back and incomplete.
    A "running one marked RerunAfterRun" has no window of its own: the mark
    keeps the item, so nothing is removed or released.
  - Fix: `jobqueue/bringback.go`, a per-key striped hold (the design is in its
    header). `createJobs` holds the keys it may bring back from before it
    registers memberships until it has queued them and recorded their rep
    groups; the key-changing modify paths hold their new keys. The archive's
    cleanup (`cleanUpArchived`) decides under the same shard lock: while held
    it is deferred to the last holder's release; otherwise, with an item
    queued under the key again, memberships are kept and only other rep groups
    are dropped; otherwise it cleans up as before. The per-shard maps are set
    to nil when they empty, so a large add does not pin memory.
  - Files: `jobqueue/{bringback.go,server.go,serverCLI.go,serverREST.go,running_dependent.go}`.
    `archiveRemovedHook` is a test seam in `finishArchive`.
  - Tests: `jobqueue/dep_group_rerun_window_test.go` (`TestDepGroupRerunWindow`:
    the older resurrection path, an add that read the dependent running, the
    archive finishing while the add holds the key, and a re-add under another
    rep group; a waiter already on the job's group stays dependent, a later
    `--deps` on it is dependent, and `GetByRepGroup` finds the job) and
    `jobqueue/bringback_test.go` (`TestBringBacksRetention`).
  - Not tested: the modify rekey hold, whose race needs a job with the new key
    to be added, run and archived during a paused modify.
- [x] 6. Crash mid-add: a crash after an add's DB write but before its reply
  loses the re-run of a dependent whose archive was written after the add read
  it. The client got no reply, so it retries the add, but on the retry the new
  job already exists, so it is filtered as already queued and the dep group
  does not "gain" a member again, so the dependent is never re-run.
  - Red command (exit 1 on 4b743a37):
    `CGO_ENABLED=1 go test -tags netgo -count 1 ./jobqueue -run 'TestCrashMidAddRerun$'`

    ```text
    Line 169:
    Expected: jobqueue.JobState("dependent")
    Actual:   jobqueue.JobState("complete")
    (4 times: the dependent archived before or after the add's write of the
    new member, times the client retrying the add or not)
    --- FAIL: TestCrashMidAddRerun (1.23s)
    ```

    The crash image is taken after the add's write of the new member and
    before `updateLiveDependents` stores its resurrection or mark. With the
    image taken after the add returns, the test passes.
  - Root cause: the add read its dependents in one bolt transaction, wrote the
    new member in a second, and only in a third put back live a dependent
    archived since the read (`resurrectArchivedDependents`) or stored the mark
    of a running one. A dependent's archive committing anywhere between the
    read and that third transaction deleted its live record, so a crash before
    the third left it complete, with nothing linking it to the new member. A
    retried add is filtered as already queued, so it re-derives nothing.
  - Fix (`jobqueue/{db.go,running_dependent.go,job.go,server.go}`): whichever
    of the add's write and the dependent's archive commits second keeps the
    dependent live, so the add is atomic on disk. Before writing, the add
    attaches an in-memory guard (`rerunGuard`) to each queued dependent it
    read, naming the smallest key the add stores. In the add's own write
    transaction (folded and chunked paths) any dependent it read that is no
    longer live but complete is put back live. An archive of a guarded job
    that finds the guard's key live (the add's write has committed) keeps the
    live record. The in-memory side then queues such a job without writing it
    again (`wasPutBack`). A durable mark on the dependent's own live record
    was rejected: the start and reservation writes rewrite that record from the
    unmarked in-memory job. Only adds that read dependents pay for it.
  - Test: `jobqueue/crash_mid_add_rerun_test.go` (`TestCrashMidAddRerun`): the
    archive before and after the add's write, with and without the client
    retrying; after restart the dependent is dependent on the new member,
    becomes ready once it completes, and runs again.
  - Residuals (each needs an error after a committed write, or is an extra
    run rather than a lost one): an add that fails after its write but before
    it queues a put-back dependent leaves it live on disk and out of the queue
    until a restart, and a retry then does not queue it (before this fix the
    retry resurrected it); a guard key already live before the add, two adds
    guarding one dependent, or a chunked add crashing after its first chunk
    can each run the dependent once more. Ready and reserved dependents, and
    the chunked path, rely on the same ordering but have no test of their own.
- [x] 7b. Found while fixing item 7 (also the "Residual, not fixed" in
  `260929-readd-overwrites-running-job.md`): an add that reads a dependent W as
  complete after W's archive transaction committed but before `finishArchive`'s
  `RemoveUnless` puts W back in the live bucket, but its queue add sees the old
  item and counts W a duplicate. The archive then removes the item, so W is
  live on disk but out of the queue, and is not re-run until a restart.
  - Red command (exit 1 on 55f512ce plus the `archiveCommittedHook` seam):
    `CGO_ENABLED=1 go test -tags netgo -count 1 ./jobqueue -run 'TestArchiveCommitWindow$'`

    ```text
    Line 87:
    Expected: jobqueue.JobState("dependent")
    Actual:   jobqueue.JobState("complete")
    --- FAIL: TestArchiveCommitWindow (0.84s)
    ```

    The add returned 1 insert and 1 duplicate, and the dependent's item was
    gone. After a crash and restart the same scenario passes.
  - Root cause: the add read the dependent from the complete bucket, put it
    back live in its own write, and returned a fresh copy to queue, but
    `q.AddMany` found the old run item (archive pending) and counted a
    duplicate; the archive's `RemoveUnless` then removed that item.
  - Fix: `queueNewJobItems` calls `rerunArchivingItems`
    (`jobqueue/running_dependent.go`). A resurrected job whose key still holds
    a different in-memory job with an archive pending goes through the #649
    "archiving" mark (`updateDependentUnlessRunning` -> `markRerunAfterRun`,
    under the queue lock), so `RemoveUnless` keeps the item and `requeueRerun`
    sends it back to dependent. It is counted as added and not passed to
    `AddMany`; its live record is the add's own write, not stored again. If
    the archive removed the item first, it is queued fresh as before.
  - Test: `jobqueue/archive_commit_window_test.go` (`TestArchiveCommitWindow`,
    with and without a restart). `archiveCommittedHook` is a test seam at the
    start of `finishArchive`.
- [ ] 8. Found by a gate run: `CGO_ENABLED=1 go test -race -tags netgo ./jobqueue
  -run 'DepGroup|DepGranularity|RunningDependent|Readd|Archive|Rerun|Modify|BringBacks'`
  fails `TestJobqueueModify` twice in a row on develop 499b350e too
  (`jobqueue_test.go:6599`, "schedgrp 200:30:1:0 not found, we have:
  800:30:1:0"): the RAM learned for `echo a` depends on which tests shared the
  process first. It passes alone and under `make race`'s split.
- [ ] 9. Item 6 residual, a regression against develop: an add that fails
  after its write committed (a DB error later in the add) but before it queues
  a dependent its transaction put back live, or that its guard kept live,
  leaves that dependent live on disk and out of the queue until a restart, and
  a retried add no longer queues it (`archivedNotLive` skips a live job). On
  develop the retry resurrected it.
  - Red command (exit 1 on 55f512ce plus the `newJobsStoredErrHook` seam; exit
    0 on 4b743a37 with the same seam):
    `CGO_ENABLED=1 go test -tags netgo -count 1 ./jobqueue -run 'TestAddFailsAfterWrite$'`

    ```text
    Line 169:
    Expected: jobqueue.JobState("dependent")
    Actual:   jobqueue.JobState("complete")
    (twice: archived before and after the add's write)
    --- FAIL: TestAddFailsAfterWrite (0.72s)
    ```

    The failed add also leaves the new member live on disk but not queued; the
    retry queues it, but not the dependent.
- [ ] 10. Found by a gate run (`make test` while fixing 7b, host load about
  18): `TestDepGranularitySidecarReportsElapsedTime` failed at
  `depgranularity_startup_test.go:935` (`second.UpdatedAt.After(first.UpdatedAt)`
  was false). It passed on rerun and 5 of 5 alone.
- [ ] 7c. Found reviewing 7b: `updateDependentUnlessRunning` reads the queued
  job (`queuedJob`) and later calls `q.UpdateUnlessRunning` with it. If, in
  between, the archive removes the item and a second concurrent add queues its
  own copy under the key, the update puts the old job object on the new item
  (stale fields; if that copy is already running, its rerun mark is lost and
  the old record is written over its live one). Shared by #649's
  `updateLiveDependents` and 7b's `rerunArchivingItems`. Needs two adds to the
  dependent's dep group as its archive commits.
