# A kick changes the job its caller found, not the item's (2026-10-08)

- Branch: `kickjob-66783567`
- Base: `origin/develop` at `9fc0d795` (#684)
- Worktree: `../wr-kickjob`
- Queue owner: this branch, this checklist; item 3 of
  `.docs/reserve-runstate/delivery-queue.md`.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: targeted `go test` runs, plain and `-race`; `make lint`,
`make test`, `make race`; `cleanorder -min-diff` on the edited Go files.

## Prior checked items that must not regress

- `261006-kick-reservable-race-8213f1ec.md`, all items: the kick sets its
  fields and queues its write inside `KickWith`'s callback, before the item is
  reservable (`TestKickRacingReservation`, `TestKickQueuedBeforeReservable`);
  the write is encoded ahead by `prepareJobChange` and re-encoded if the job
  was write-locked since (`TestKickAfterModifyKeepsModification`,
  `TestOverlappingChangesAheadKeepModification`); a bury's write cannot queue
  after the kick's (`TestKickDuringBuryWriteStoresKicked`); a failed kick sets
  nothing, queues nothing and lets `Stop` finish
  (`TestFailedKickLeavesJobAndStops`, `TestFailedKickOfDependentJobStops`);
  the same for resume (`TestResumeRacingStart`,
  `TestResumeAfterChangeWritesResumedJob`, `TestResumeWritesResumedState`,
  `TestFailedResumeLeavesJobAndStops`, `TestResumeAfterWriterStopsStops`,
  `TestResumeRacingReserveKeepsRACPending`). Its speed rework (encode before
  the queue lock, no encode or db lock under it) must hold:
  `BenchmarkKickUnderReserveLoad`.
- The prior items that file lists: `TestReleaseAfterLost`,
  `TestResentReleaseAfterKick`, `TestBuriedDependentRestart`,
  `initialUntilBuried` saturation, `TestJobqueueSignal`, async kick write,
  `TestJobqueueModify`.
- `260930-dep-group-rerun-gaps.md` items 6, 7, 7b and 7c: an add that brings
  back or re-runs a complete job keeps its group memberships and rep group
  (`TestDepGroupRerunWindow`, `TestCrashMidAddQueuedRerun`), and
  `TestRerunReplacementReadyCallbackBlocksReserve`.

## Items

- [x] kickJobs changes the *Job its caller passed, not the item's data; a
      modify that replaced the item's *Job leaves the kick changing the old
      object (pre-existing).
  - Source: deferred note on the first item of
    `261006-kick-reservable-race-8213f1ec.md`; delivery queue item 3.
  - Verdict: real, but not through a modify. Every modify path
    (`modifyJobs`, `modifyJobsByKeys`) changes the item's own `*Job` in place
    under `job.Lock` and passes that same pointer to `q.ChangeKey` and
    `q.Update`, so the item keeps the object the kick holds. The write-lock
    count then re-encodes the kick's write with the modification
    (`TestKickAfterModifyKeepsModification`). A modify that changes the key
    after `kickJobs` computed it makes `KickWith` miss, so nothing is kicked
    or written, as if the modify had come first.
    The one path that puts a different `*Job` in a live item is an add that
    re-runs a queued complete job: `replaceLiveRerunItem` calls `q.Update`
    with the new job for any item not in Run whose job's `State` is
    `complete`. A waiter that a dep-group re-run brought back from the
    complete bucket keeps `State` `complete`, and `buryImpossibleItem` buries
    it without changing `State`. So an add of that buried job can replace the
    item's job between `handleKick`'s (or the web UI's) lookup and
    `KickWith`. The kick then sets `State` and `UntilBuried` on the old
    object and writes the old object's record over the add's. The item is
    ready, holding a job whose `State` is blank, and after a crash the job is
    recovered with the old rep group, so the re-add's rep group (and any
    other field it changed) is lost.
    Resume is not affected on its own: a suspended item's job has `State`
    `suspended`, so the add does not replace it. The only gap is inside
    `suspendJob`, between `q.Suspend` and its `job.Lock`, where the item is
    suspended and its job still `complete`. That is a suspend race, not
    tested here.
  - Test seam: the existing `jobChangeAheadHook` (`jobqueue/db.go`), called
    in `prepareJobChange` before `KickWith`.
  - Red test: `TestKickOfReplacedJobKicksItemsJob` in
    `jobqueue/kick_stale_job_test.go`. It completes a dep-group member and
    its waiter, adds a second member (bringing the waiter back complete), runs
    that member, reserves the ready waiter from the queue and buries it with
    `buryImpossibleItem`. It then kicks it with `Client.Kick`. In the hook, a
    second client adds the waiter again with a new rep group and
    `ignoreComplete` false. After a durable write of a parked job flushes the
    kick's write, one leaf asserts the item's job is `ready`. The other
    crashes the manager (`BackupDB` image) and asserts the recovered job has
    the new rep group and is ready.
  - Red command: `nice -n 19 timeout 600 go test -tags netgo ./jobqueue
    -count=1 -run 'TestKickOfReplacedJobKicksItemsJob$' -v`, exit 1 on
    `1fa012ae`, 4 of 4 runs, and exit 1 under `CGO_ENABLED=1 ... -race` with
    the same two failures and no race report (log lines filtered):

    ```text
    === RUN   TestKickOfReplacedJobKicksItemsJob
      Given a buried complete waiter that an add re-runs while it is being kicked
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
        the job the item holds is the one kicked ✘
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
        a crash recovers the re-added job ready under its new rep group ✔
    ✔✔✔✔✔✘
    Failures:
      * jobqueue/kick_stale_job_test.go
      Line 162:
      Expected: jobqueue.JobState("ready")
      Actual:   jobqueue.JobState("")
      (Should equal)!
      * jobqueue/kick_stale_job_test.go
      Line 176:
      Expected: "kick-stale-job-new"
      Actual:   "depgranularity-add-waiters"
      (Should equal)!
    107 total assertions
    --- FAIL: TestKickOfReplacedJobKicksItemsJob (0.60s)
    ```

    Line 162 is the item's job left unkicked in memory. Line 176 is the
    recovered job carrying the old rep group: the kick's write of the old
    object replaced the add's record. In a scratch copy, a throwaway
    `KickWith` callback that kicked and wrote the item's `data` when it
    differed from the caller's job turned the test green, with
    `TestKickAfterModifyKeepsModification` and `TestKickRacingReservation`
    still passing. So both leaves fail because of this bug, not the setup.
    The throwaway encoded under the queue lock, which the speed rework
    forbids, so it is not the fix.

  - Fixed: kickJobs' KickWith callback passes the item's current data to
    markKicked; when it is not the job kickJobs prepared, the prepared write
    moves to the item's job (db.moveJobChangeAhead) and is re-encoded under
    the queue lock in that rare case only. Files: jobqueue/server.go,
    jobqueue/db.go, jobqueue/kick_stale_job_test.go. Red command green; make
    lint, make test, make race pass; kick/resume-under-reserve-load benchmark
    shows no significant change vs develop 9fc0d795.
- [ ] suspendJob stale-object race: between q.Suspend and suspendJob's
      job.Lock the item's job still has State complete (a waiter brought back
      by a dep-group re-run), so a re-add can replace the item's *Job there and
      the suspend then changes and writes the old object. Same class as the
      kick item above; not yet reproduced.
  - Source: kick-stale-job red-test builder, incidental finding.
