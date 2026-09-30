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
- [ ] 6. Crash mid-add: a crash after an add's DB write but before its reply
  loses the re-run of a dependent whose archive was written after the add read
  it. The client got no reply, so it retries the add, but on the retry the new
  job already exists, so it is filtered as already queued and the dep group
  does not "gain" a member again, so the dependent is never re-run.
- [ ] 7b. Found while fixing item 7 (also the "Residual, not fixed" in
  `260929-readd-overwrites-running-job.md`): an add that reads a dependent W as
  complete after W's archive transaction committed but before `finishArchive`'s
  `RemoveUnless` puts W back in the live bucket, but its queue add sees the old
  item and counts W a duplicate. The archive then removes the item, so W is
  live on disk but out of the queue, and is not re-run until a restart.
- [ ] 8. Found by a gate run: `CGO_ENABLED=1 go test -race -tags netgo ./jobqueue
  -run 'DepGroup|DepGranularity|RunningDependent|Readd|Archive|Rerun|Modify|BringBacks'`
  fails `TestJobqueueModify` twice in a row on develop 499b350e too
  (`jobqueue_test.go:6599`, "schedgrp 200:30:1:0 not found, we have:
  800:30:1:0"): the RAM learned for `echo a` depends on which tests shared the
  process first. It passes alone and under `make race`'s split.
