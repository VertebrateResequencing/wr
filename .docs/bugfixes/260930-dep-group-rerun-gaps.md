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
    becomes ready once it completes, and runs again. Its sibling
    `jobqueue/crash_mid_add_queued_rerun_test.go`
    (`TestCrashMidAddQueuedRerun`) does the same for a dependent that is ready,
    or reserved and not started, when the add reads it, and is then reserved,
    started and archived during the add. Disabling the add's in-write put-back
    fails its "before" cases; disabling `rerunGuard.keeps` fails its "after"
    cases.
  - Residuals (each needs an error after a committed write, or is an extra
    run rather than a lost one): an add that fails after its write but before
    it queues a put-back dependent leaves it live on disk and out of the queue
    until a restart, and a retry then does not queue it (before this fix the
    retry resurrected it); a guard key already live before the add, two adds
    guarding one dependent, or a chunked add crashing after its first chunk
    can each run the dependent once more. The chunked path relies on the same
    ordering but has no test of its own: it needs 1000 or more jobs in one
    bucket of one add, and its batch size is a set of constants with no test
    knob.
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
- [x] 8. Found by a gate run: `CGO_ENABLED=1 go test -race -tags netgo ./jobqueue
  -run 'DepGroup|DepGranularity|RunningDependent|Readd|Archive|Rerun|Modify|BringBacks'`
  fails `TestJobqueueModify` twice in a row on develop 499b350e too
  (`jobqueue_test.go:6599`, "schedgrp 200:30:1:0 not found, we have:
  800:30:1:0"): the RAM learned for `echo a` depends on which tests shared the
  process first. It passes alone and under `make race`'s split.
  - Fixed by #659, `261001-flakes-and-tooling.md`: TestJobqueueModify.
- [x] 9. Item 6 residual, a regression against develop: an add that fails
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
  - Root cause: the retry reads the dependent as a live dependent that has left
    the queue, so it reaches `archivedToRerun`, which skipped any job live on
    disk as dealt with by something else.
  - Fix (`jobqueue/running_dependent.go`): `archivedToRerun` treats a job that
    is complete-recorded, live on disk and not queued as already live: it is
    queued without being stored again. If the add that put it back is still
    running, both queue it and one counts a duplicate. `archivedNotLive` is
    renamed `archivedNotQueued`.
  - Test: `jobqueue/add_fails_after_write_test.go` (`TestAddFailsAfterWrite`,
    archived before and after the add's write). `newJobsStoredErrHook` is a
    test seam in `createJobs`.
  - Residuals: a failed add that is never retried still leaves the dependent
    out of the queue until a restart (on develop it stayed complete for good).
    A `wr remove` takes a job out of the queue before deleting its live record,
    so an add checking in between queues it with no live record; the older
    not-live branch already had the same race just after the delete. A modify
    rekey writes the database before `ChangeKey`, so a concurrent add can
    queue a dependent with the new key first, and `ChangeKey` then only logs.
- [x] 10. Found by a gate run (`make test` while fixing 7b, host load about
  18): `TestDepGranularitySidecarReportsElapsedTime` failed at
  `depgranularity_startup_test.go:935` (`second.UpdatedAt.After(first.UpdatedAt)`
  was false). It passed on rerun and 5 of 5 alone.
  - Fixed by #651, `260929-test-ci-reliability.md`, and #659,
    `261001-flakes-and-tooling.md`: TestDepGranularitySidecarReportsElapsedTime.
- [x] 7c. Found reviewing 7b: `updateDependentUnlessRunning` reads the queued
  job (`queuedJob`) and later calls `q.UpdateUnlessRunning` with it. If, in
  between, the archive removes the item and a second concurrent add queues its
  own copy under the key, the update puts the old job object on the new item
  (stale fields; if that copy is already running, its rerun mark is lost and
  the old record is written over its live one). Shared by #649's
  `updateLiveDependents` and 7b's `rerunArchivingItems`. Needs two adds to the
  dependent's dep group as its archive commits.
  - Red command (exit 1 before the fix; also red with the identity check
    disabled): `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestDependentUpdateRace`

    ```text
    Line 169:
    Expected: jobqueue.JobState("dependent")
    Actual:   jobqueue.JobState("complete")
    --- FAIL: TestDependentUpdateRace (0.77s)
    ```

    The scenario needs a dependent of two dep groups: one add reads the
    archiving job, the archive removes it, a second add (whose member is in
    the other group) queues a fresh copy, which is reserved and started, and
    the first add then marks the old object, so the rerun is lost.
  - Fix: `queue.UpdateHolderUnlessRunning` refuses under the queue lock, with
    `queue.ErrDataChanged`, when the item no longer holds the given data.
    `updateDependentUnlessRunning` then retries against the current holder
    (at most 3 times) with dependencies computed for it, marking it if it is
    running; a dependency lookup error fails the add as the first attempt's
    does. `queue.UpdateUnlessRunning` is kept for compatibility.
  - Tests: `jobqueue/dependent_update_race_test.go`
    (`TestDependentUpdateRace`, via the `dependentReadHook` seam) and
    `queue/holder_update_test.go`.
- [x] 11. Found by a gate run (`CGO_ENABLED=1 make race` after merging develop
  95faf168): `TestArchiveStallDoesNotRerunJob` "a repeat of the runner's
  archive during the stall also succeeds" failed at `archive_stall_test.go:191`
  (`pending(1)` false after 30s). The log shows the runner's `jstart` was the
  request held for 30s ("slow request method=jstart duration=30.009s"), so the
  test's held write transaction caught the start's write, not the archive's,
  and the run never reached its archive. It passed 3 of 3 alone under `-race`
  and the next full `make race` passed (840 passed).
  - Fixed by #657, `260930-moved-on-runner.md`: TestArchiveStallDoesNotRerunJob.
- [x] 12. Speed regression from item 6 (f4eb134a), measured in PR #662
  (`.docs/perf/261001-version-comparison.md` on branch `add-make-speed`):
  `dep-granularity-check`'s single `wr add` of a member into a 3000-member dep
  group with 30000 waiters is +12% at 12 rounds against develop (479 -> 537 ms,
  p<0.001), and +18.7% against f4eb134a's parent (470 -> 558 ms, p=0.002).
  The add's write transaction looks up every dependent it read
  (`putBackArchivedDependentsWith`/`putBackArchivedDependentsTx`), not only
  those whose archive could have committed since the read.
  - Red command (8 interleaved rounds of each build, compared by benchstat):
    `CGO_ENABLED=1 go test -tags netgo ./jobqueue -run '^$' -bench 'BenchmarkAddDepGroupMember$' -benchtime 10x`

    ```text
    4b743a37 (base)  334.3m ± 7%
    31d5ad10 (HEAD)  410.4m ± 6%  +22.79% (p=0.000 n=8)
    fix              340.1m ± 3%  ~ (p=0.382 n=8)
    ```

  - Root cause: item 6 cost the add work for every dependent it read, about
    40 ms per add for the put-back's bolt Gets and 50 ms for guarding:
    `guardDependents` looked up each dependent's queue item and locked the job
    to attach a guard, and `rerunGuard.release` locked each job again.
  - Fix (`jobqueue/{running_dependent.go,db.go,server.go,job.go}`): the add
    registers its guard on the db (`rerunGuards.register`) before it reads its
    dependents, and releases it once it has given them their dependencies.
    Every archive transaction notes its key on each registered guard under the
    registry lock (`rerunGuards.archiving`), or keeps the job live if it is one
    of a guard's dependents and that guard's first job is already live. The
    add's write puts back only the noted keys that are its dependents
    (`rerunGuard.atRiskKeys`). A guard registered while an archive's
    transaction is open takes that transaction's keys (`scanKeys`), since
    bolt's serial write transactions make it the only archive that both looked
    before the registration and can commit after the read. `Job.rerunGuards`
    and the per-job attach and release are gone.
  - Tests: `jobqueue/crash_mid_add_guard_test.go`:
    `TestCrashMidAddArchivedBeforeGuard` (the archive commits between the read
    and the guard learning the dependents, with or without its item removed),
    `TestCrashMidAddArchiveOpenAtStart` (the archive's transaction is open when
    the add registers), and `TestRerunGuardKeepsOnlyDependentsLive` (an
    unrelated job archived after the add's write leaves the live bucket).
    Benchmark: `BenchmarkAddDepGroupMember`
    (`jobqueue/dep_group_add_bench_test.go`).
  - Mutation evidence: `atRiskKeys` returning nothing fails
    `TestCrashMidAddRerun`, `TestCrashMidAddQueuedRerun` and both new crash
    tests. Dropping the `scanKeys` seed fails `ArchiveOpenAtStart`. Not noting
    before the dependents are known fails `ArchivedBeforeGuard`. `keeps`
    always false fails the two item 6 tests. Dropping the dependent check in
    `keeps` fails `KeepsOnlyDependentsLive`.
- [x] 13. Recurrence, reported per the owner's "leave it and watch" ruling, not
  fixed: `TestReliable4RacBoundedBySchedulable`
  (`jobqueue/reliable4_rac_bound_test.go:117`) failed in one `make test` run
  on 33e9786f (`Expected: 5 / Actual: 19` for `racScanWork`). The next
  `make test` passed (866 passed), and `CGO_ENABLED=1 make race` passed.
  The same failure is recorded in `260928-load-sensitive-flakes.md`.
  - Fixed by #663, `261002-rac-bound-7debc26e4164.md`.
