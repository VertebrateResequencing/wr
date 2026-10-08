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
  - Follow-up (with item 3): the committed test's `So(live, ShouldNotPointTo,
    looked)` made goconvey format the whole `*Job` without its lock, so the
    test failed every run under `-race`. It is now `So(live != looked,
    ShouldBeTrue)`; the suspend test uses the same form. Test-only.
- [x] suspendJob stale-object race: between q.Suspend and suspendJob's
      job.Lock the item's job still has State complete (a waiter brought back
      by a dep-group re-run), so a re-add can replace the item's *Job there and
      the suspend then changes and writes the old object. Same class as the
      kick item above; not yet reproduced.
  - Source: kick-stale-job red-test builder, incidental finding.
  - Verdict: real, and simpler to reach than the kick case. A waiter that a
    dep-group re-run brought back keeps `State` `complete` while dependent
    and, once the new member completes, while ready, so it is suspendable
    with no bury. `suspendJob` takes `job` from `item.Data()` before
    `q.Suspend`, and `replaceLiveRerunItem` replaces the data of any item not
    in Run whose job is `complete` and not archive-pending, suspended items
    included. So an add with `ignoreComplete` false that lands after the
    `q.Get` and before `suspendJob`'s `job.Lock` (on either side of
    `q.Suspend`) puts a new `*Job` in the item. The suspend then sets
    `State` `suspended` on the old object and queues the old object's write
    over the add's record. In memory the suspended item holds a job whose
    `State` is blank, so status reports it wrongly. The stored record carries
    the old rep group, so after a crash the job is recovered under it and the
    re-add's rep group (and any other field it changed) is lost. Resume
    reads `item.Data()` inside `ResumeWith`, so it then acts on the item's
    job and does not compound this.
  - Test seam (red version): `suspendQueuedHook` in `jobqueue/server.go`,
    nil in production, called in `suspendJob` after `q.Suspend` succeeded
    and before `job.Lock` (mirrors `kickQueuedHook`). After the fix the test
    uses `jobChangeAheadHook` instead; see "Test re-seated" below.
  - Red test: `TestSuspendOfReplacedJobSuspendsItemsJob` in
    `jobqueue/suspend_stale_job_test.go`. It completes a dep-group member and
    its waiter, adds a second member (bringing the waiter back complete),
    runs that member so the waiter is ready with `State` `complete`, and
    suspends it with `Client.Suspend`. In the hook, a second client adds the
    waiter again with a new rep group and `ignoreComplete` false. After a
    durable write of a parked job flushes the suspend's write, one leaf
    asserts the item's job is `suspended` and the stored record is suspended
    under the new rep group. The other crashes the manager (`BackupDB`
    image) and asserts the recovered job has the new rep group and is
    suspended.
  - Red command: `nice -n 19 timeout 600 go test -tags netgo ./jobqueue
    -count=1 -run 'TestSuspendOfReplacedJobSuspendsItemsJob$' -v`, exit 1 on
    `b2efddea` plus the hook, 4 of 4 runs, and exit 1 under
    `CGO_ENABLED=1 ... -race` with the same two failures and no race report
    (log lines filtered):

    ```text
    === RUN   TestSuspendOfReplacedJobSuspendsItemsJob
      Given a ready complete waiter that an add re-runs while it is being suspended
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
        the job the item holds is the one suspended, and is stored ✘
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
        a crash recovers the re-added job suspended under its new rep group ✔
    ✔✔✔✔✔✘
    Failures:
      * jobqueue/suspend_stale_job_test.go
      Line 158:
      Expected: jobqueue.JobState("suspended")
      Actual:   jobqueue.JobState("")
      (Should equal)!
      * jobqueue/suspend_stale_job_test.go
      Line 176:
      Expected: "suspend-stale-job-new"
      Actual:   "depgranularity-add-waiters"
      (Should equal)!
    97 total assertions
    --- FAIL: TestSuspendOfReplacedJobSuspendsItemsJob (0.58s)
    ```

    Line 158 is the item's job left unsuspended in memory. Line 176 is the
    recovered job carrying the old rep group: the suspend's write of the old
    object replaced the add's record. A throwaway change that re-read the
    item's data after the hook and suspended that job turned the test green
    (100 assertions), so both leaves fail because of this bug, not the
    setup. It was reverted; it is not the fix, since the add can still land
    between that re-read and `job.Lock`.
  - Fix (shared with the item below, uncommitted): `queue.SuspendWith` calls
    back with the item's data under the queue lock once the item is known to
    be suspendable and before it moves; `Suspend` is `SuspendWith` with no
    callback. `suspendJob` checks `ItemState.Suspendable` first, encodes the
    suspended job with `prepareJobChange` before the queue lock, and its
    callback hands the item's data to `markItemJobChanged` (`markKicked`
    generalised to take the change), which moves the prepared write to the
    item's job when it differs. A failed suspend discards the prepared write
    and sets nothing. `suspendQueuedHook` now runs after the item has moved
    and its job is suspended.
  - Test re-seated: after the fix the window the hook used (item suspended,
    job still `complete`) no longer exists, so an add in `suspendQueuedHook`
    finds the job `suspended` and does not replace it. The add now lands in
    `jobChangeAheadHook`, after `suspendJob` looked up the item's job and
    before `SuspendWith`, the window that remains. Green on the fix. With the
    fix but a callback that queues the looked-up job and ignores the item's
    data, it fails both leaves (line 166 `State` `""`, want `suspended`; line
    184 rep group `depgranularity-add-waiters`, want
    `suspend-stale-job-new`), so it still catches this bug.
  - Also found, same ordering class as the kick and resume races fixed by
    `261006-kick-reservable-race-8213f1ec.md` (not fixed here, for routing):
    `suspendJob` sets `State` and queues its write after `q.Suspend` has
    returned, outside the queue lock. A resume in that window resumes the
    item (`markResumed` sets `State` `ready` and queues its write), then the
    suspend sets `State` `suspended` and writes it. The item is ready with a
    job whose `State` is `suspended`, and a crash recovers the job suspended,
    so the user's resume is lost. A probe (hook resumes via a second client;
    ready job, no dep group) failed both leaves: in-memory `State`
    `suspended` (want `ready`) and recovered `suspended` (want `ready`). By
    the same reasoning, a reservation or start after that resume and before
    the suspend's `job.Lock` would have its `State` overwritten with
    `suspended`. The `KickWith`/`ResumeWith` analogue (a `SuspendWith`
    callback under the queue lock, taking the item's data) would close both
    this and the stale-object race.

- [x] suspend ordering: suspendJob sets State and queues its write after
      q.Suspend returns, outside the queue lock. A resume in that window sets
      State ready and queues its write; the suspend then overwrites State with
      suspended and writes that, so the item is ready while its job says
      suspended, and a crash recovers it suspended (the user's resume is
      lost). A reservation or start after that resume would likewise have its
      State overwritten. Same class as the kick and resume ordering fixed by
      #683.
  - Source: suspend stale-object red-test builder, incidental finding (probe
    at /tmp/claude-11346/suspend_order_probe_test.go.txt, using
    suspendQueuedHook).
  - Test seam: `suspendQueuedHook` in `jobqueue/server.go`, called in
    `suspendJob` once the item is in the suspended sub-queue.
  - Red test: `TestSuspendRacingResumeKeepsResume` in
    `jobqueue/suspend_order_test.go`. It adds a ready job and suspends it
    with `Client.Suspend`; in the hook, a second client resumes it. After a
    durable write of a parked job flushes both writes, one leaf asserts the
    item's job and its stored record are `ready`. The other crashes the
    manager (`BackupDB` image) and asserts the recovered job is `ready`.
  - Red command: `nice -n 19 timeout 600 go test -tags netgo ./jobqueue
    -count=1 -run 'TestSuspendRacingResumeKeepsResume$' -v`, exit 1 on
    `b2efddea` plus the hook (log lines filtered):

    ```text
    the job and its stored record say it is ready ✔✘
    a crash recovers it ready ✔✔✔✔✔✔✔✘
    Failures:
      * jobqueue/suspend_order_test.go
      Line 121:
      Expected: jobqueue.JobState("ready")
      Actual:   jobqueue.JobState("suspended")
      * jobqueue/suspend_order_test.go
      Line 136:
      Expected: jobqueue.JobState("ready")
      Actual:   jobqueue.JobState("suspended")
    48 total assertions
    --- FAIL: TestSuspendRacingResumeKeepsResume (0.32s)
    ```

    Line 121 is the item's job, ready in the queue, saying `suspended` in
    memory. Line 136 is the job recovered `suspended` after a crash: the
    suspend's write came after the resume's and replaced it. (Line numbers
    are from the red version; the same assertions are now at 125 and 140.)
  - Fix: shared with the item above (`SuspendWith`). The suspend's `State`
    and write now come before the item leaves its sub-queue, so a resume,
    reservation or start can only follow them. Green; also
    `queue/suspend_with_test.go` (`TestSuspendWith`): the callback sees the
    item's data in its original state, a resume called from it gets in only
    after the move, and a failed suspend never calls back.
  - Gates (both items, uncommitted on `b2efddea`): `make lint` 0 issues;
    `make test` and `CGO_ENABLED=1 make race` pass (964 passed). `make speed`
    with `SPEED_BASE=9fc0d795`: PASS
    (`/tmp/wr-speed-sb10/run-1791449145/benchstat.txt`).
  - Focused benchmark: `BenchmarkSuspendUnderReserveLoad` in
    `jobqueue/kick_resume_bench_test.go` suspends 1000 delayed 130 KB jobs
    while 8 goroutines reserve. Base `9fc0d795` with the benchmark file
    copied in, `CGO_ENABLED=1` binaries, `-test.benchtime 3x`, interleaved
    rounds, benchstat `v0.0.0-20260929162123-406019bb8b68`, n=16:

    ```text
                    base      head      vs base
    sec/op          90.88m    83.00m    -8.67% (p=0.012)
    reserve-p50-us  6.412     6.995     +9.09% (p=0.007)
    reserve-p99-us  217.9     327.0    +50.03% (p=0.001)
    reserves/op     4.824k    4.108k   -14.85% (p=0.003)
    ```

    A further n=8 run gave p99 231.6 vs 434.4 (+88%, p=0.005), p50 n.s.
    Kick and resume did not change (n=8, all n.s.; kick p99 ~265-280 on both).
    Verdict: FAIL under the 10% / p<0.05 rule for p99. The suspend now sets
    its job's `State` and queues the prepared write under the queue lock
    (`job.Lock`, `beMu`, a map insert, a writer wake-up), as kick and resume
    have done since #683; base suspend held nothing under the lock (that was
    the bug). Waking the writer after the lock instead still gave +35%
    (p=0.038, ±102%), so it was not adopted.
    Owner ruling (2026-10-08): accept the cost. Suspend now pays what kick and
    resume already pay; suspends are rare and user-initiated. Drain-time
    encoding is not pursued for this release.
  - Failed suspend: `TestFailedSuspendLeavesJobAndStops` in
    `jobqueue/suspend_order_test.go`, mirroring
    `TestFailedKickLeavesJobAndStops`. In `jobChangeAheadHook` (after the
    `Suspendable` check and `prepareJobChange`, before `SuspendWith`) a runner
    client reserves the ready job, so `SuspendWith` fails with
    `ErrNotSuspendable`. It asserts the suspend reports 0, the job is
    `reserved` in memory and in its stored record, and `Stop` finishes within
    20 s. Red on M4 (no `discardJobChangeAhead` on the failure path):
    `nice -n 19 timeout 600 go test -tags netgo ./jobqueue -count=1 -run
    'TestFailedSuspendLeavesJobAndStops$' -v`, exit 1, `Stop` hung on the
    leaked write slot (log lines filtered):

    ```text
    suspending it suspends nothing, leaves it reserved, and the manager stops ✔✔✔✔✔✔✔✔✔✘
    Failures:
      * jobqueue/suspend_order_test.go
      Line 248:
      Expected: true
      Actual:   false
    --- FAIL: TestFailedSuspendLeavesJobAndStops (20.22s)
    ```

    Line 248 is the `Stop` check (`stopsPromptly`). Green with M4 reverted,
    plain and under `CGO_ENABLED=1 ... -race`. `stopsWithin` lost its
    duration parameter and became `stopsPromptly`: with a fourth caller all
    passing `failedChangeStopTimeout`, `unparam` flagged it.
  - Reviewer's mutants (cycle 1, with the test above):
    - M1: killed (`TestSuspendOfReplacedJobSuspendsItemsJob`).
    - M2: killed.
    - M3 (change made after `SuspendWith` returns, before the hook): survived;
      see the limitation below.
    - M4 (no `discardJobChangeAhead` on failure): killed
      (`TestFailedSuspendLeavesJobAndStops`).
    - M5: killed.
    - M6: killed (`TestSuspendWith`).
    - M7 (queue callback after unlock): killed by `queue/TestSuspendWith`
      only.
    - M8: killed (`TestQueueSuspendResume` hangs).
    - K3 (kick control): survived; the same limitation in #683's tests.
  - Known limitation (no code): no deterministic test proves `suspendJob`
    makes its change inside the `SuspendWith` callback rather than after
    `SuspendWith` returns. `suspendQueuedHook` runs after the change, so
    `TestSuspendRacingResumeKeepsResume` cannot tell M3 or M7 from the fix;
    catching them would need a cross-package seam inside the queue after the
    move. #683's kick and resume tests share the gap (control mutant K3
    survives), the same class as #683's D24 and D18 notes in
    `.docs/bugfixes/261006-kick-reservable-race-8213f1ec.md`.
- [ ] TestKickAfterModifyKeepsModification flake: it failed once (5.25 s) in a
  plain group run while item 3 was being implemented, then passed 10 further
  runs plus full `make test` and `make race`. The failure output was not
  captured.
  - Source: item 3 implementer, incidental. Owner: investigate with a red
    loop before any fix; if it cannot be reproduced, record that and move on.
  - Not reproduced (2026-10-08): about 760 runs, 0 failures: 20 single runs;
    200 as 8 parallel processes x 25 at `GOMAXPROCS=2`; 20 group runs of
    `Kick|Suspend|Resume|Stale|Modify`; 90 `-race` runs at `GOMAXPROCS=1`
    grouped with the other kick and stale-job tests; 192 as 16 parallel x 12
    at `GOMAXPROCS=1`. A pass takes 0.10-0.34 s. The failed run took 5.25 s,
    about one 5 s budget more than a pass, which fits only a `serve` port
    bind retry (direct `go test` without `WR_TEST_LANE`, other agents' tests
    on the same host) or `Stop`'s 5 s background wait after an assertion
    failure (the failure came while item 3 was rewriting the kick callback,
    so intermediate code may have leaked a write slot). Neither can be
    confirmed without the lost output. Left unchecked: no red loop, no fix.
- [ ] TestServerWebISuspendedStatus flake: it failed twice at
  `jobqueue/serverWebI_test.go:309` (`readJStateCounts`, 3 s timeout; expected
  suspended counts not seen, 3.21 s and 3.37 s) in group runs during item 3's
  review, under mutants that do not touch its path. Isolated and group reruns
  on the fix and on base `9fc0d795` were all green. Unverified guess: the
  queue's change callback runs in a goroutine, so its delta can race the
  scan-on-connect seed.
  - Source: item 3 reviewer, incidental. Logs (outside the repo):
    `flake-webisuspended-M5.log`, `flake-webisuspended-M4.log` in the
    session scratchpad. Owner: red loop before any fix; if it cannot be
    reproduced, record that and move on.
  - Reproduced deterministically (2026-10-08) by holding the suspend's change
    callback until the status socket has joined the delta feed: fails at the
    same `readJStateCounts` assertion after 3.1-3.3 s, on this branch and on
    base `9fc0d795`. Cause: `queue.changed` (`queue/queue.go`) runs the
    change callback in a goroutine (since 2016), so a status delta can reach
    a socket that joined after the move. The test helper
    `readJStateDeltasUntil` applies deltas from before the seed's `begin`
    boundary (the browser discards them). In production a late delta can
    also land after the seed's `end` boundary for a move the snapshot
    already counted, so the status page double-counts it until a refresh;
    the `jstatusSeedBoundary` doc (`jobqueue/serverWebI.go`) assumes deltas
    arrive soon after their move. Pre-existing, independent of this
    branch's fixes.
  - Deferred to its own branch: item 8 of
    `.docs/reserve-runstate/delivery-queue.md` (status-count deltas ordered
    against the seed snapshot, e.g. by a queue change sequence number; the
    test helper resets on `begin` as the browser does). Red probe:
    `TestProbeWebISuspendedLateDelta` (session scratchpad, to be turned into
    the red test there). Left unchecked here.
