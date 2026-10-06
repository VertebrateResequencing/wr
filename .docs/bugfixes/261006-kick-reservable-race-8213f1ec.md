# A kick undoes a reservation that overtakes it (2026-10-06)

- Branch: `kickorder-5e1122fd`
- Base: `origin/develop` at `021f734a` (#682)
- Worktree: `../wr-kickorder`
- Queue owner: this branch, this checklist. The run-state branch
  `reserve-runstate-2f00c336` depends on it.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: targeted `go test` runs, plain and `-race`;
`golangci-lint run ./jobqueue/...`; `cleanorder -min-diff` on the edited Go
files.

## Prior checked items that must not regress

- `260930-release-durability.md`, "A live-record change can be queued out of
  order with its encoding": `queueJobChange` holds `job.RLock` from encode
  through enqueue, so writes of one job queue in encode order
  (`TestBestEffortChangeKeepsEncodeOrder`). The same file's waiting-item
  release fix: a release of an item a kick already took out of Run spends no
  retry (`TestReleaseAfterLost` covers kick).
- `260930-runner-report-followups.md` item 5: a re-sent release after a kick
  leaves the job ready with the kicked budget (`TestResentReleaseAfterKick`).
- `260930-buried-dependent-restart.md` and spec B2: a buried job with
  unresolved dependencies stays buried, and a kick makes it dependent
  (`TestBuriedDependentRestart`).
- `260819-1.md`: `kickJobs` seeds `UntilBuried` with `initialUntilBuried`,
  which saturates at `MaxUint8`.
- `260924-run-state-reset.md`: kick skips run-queue items; the reservation
  after a kick carries a blank `FailReason` (`TestJobqueueSignal`).
- `260917-start-durability.md`: kick keeps the async `updateJobAfterChange`
  rather than a durable write, for write-storm reasons.
- `260925-add-test-connect-flake.md` and `260928-load-sensitive-flakes.md`:
  `TestJobqueueModify` (kick, then ready) and `TestJobqueueSignal` (kick,
  then reserve reads reserved) must stay deterministic.

## Items

- [x] kickJobs race: s.q.Kick makes the job reservable before the kick sets
      State/UntilBuried under job.Lock and queues its write; a reservation
      landing in that window has its State overwritten to ready and the
      kick's full change supersedes it (crash before Started recovers it ready
      while its runner may run it: double run).
  - Source: coordinator. Also noted: a reservation between the kick's
    `job.Lock` section and its `updateJobAfterChange` can commit durably
    before the kick's change is queued. On develop the reservation's full
    write includes the kicked `UntilBuried`; it matters to
    `reserve-runstate-2f00c336`, which writes smaller reservation records.
  - Test seam: `kickQueuedHook` in `jobqueue/server.go`, nil in production,
    called in `kickJobs` after `s.q.Kick` succeeds and before `job.Lock`.
  - Red test: `TestKickRacingReservation` in `jobqueue/kick_order_test.go`.
    It buries a job, pauses its kick in the hook, has a second client reserve
    the job, then lets the kick finish. One leaf asserts the server's job is
    still reserved. The other queues a durable write of another job, so the
    kick's write has committed, takes `BackupDB` as the crash image, restarts
    on it, and asserts a fresh runner is not given the job and it recovered
    reserved.
  - Red command: `nice -n 19 go test ./jobqueue -count=1 -run
    'TestKickRacingReservation$' -v`, exit 1 on `021f734a` plus the hook, 3
    of 3 runs (log lines filtered):

    ```text
    === RUN   TestKickRacingReservation
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
    ✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔✔
    ✔✔✔✔✔✔✘
    Failures:
      * jobqueue/kick_order_test.go
      Line 162:
      Expected: jobqueue.JobState("reserved")
      Actual:   jobqueue.JobState("ready")
      (Should equal)!
      * jobqueue/kick_order_test.go
      Line 207:
      Expected 7a86b3130dab3094599d694334bf7d8d to be empty (but it wasn't)!
    47 total assertions
    --- FAIL: TestKickRacingReservation (0.69s)
    ```

    Line 162 is the in-memory state after the kick. Line 207 is the
    recovered manager handing the job to a fresh runner: the double run.
    A throwaway guard in `kickJobs` that skips run-queue items after the hook
    turned the test green, which shows that both leaves fail for this race
    and not because of the setup. The guard was reverted and is not the fix,
    since the item can still move between that check and `job.Lock`.
  - Fixed: `queue.Queue.KickWith` runs a callback under the queue lock after
    confirming the item is buried and before it leaves the bury sub-queue
    (`Kick` delegates to it). `kickJobs` passes `markKicked`, which sets
    `UntilBuried` and `State` under `job.Lock` and queues the async
    `updateJobAfterChange`, so the kick's fields and write precede any
    reservation. A failed kick (not found, not buried) sets nothing and
    queues nothing. Files: `queue/queue.go`, `queue/kick_with_test.go`,
    `jobqueue/server.go`, `jobqueue/kick_order_test.go` (a third leaf checks
    a crash image taken when the job first becomes reservable recovers it
    ready with the kicked `UntilBuried`). Red command green 3 of 3;
    `make lint`, `make test`, `make race` pass. Speed gate (`make speed` plus
    a kick-under-reserve-load comparison) is owed before PR-ready, since
    `queue/` changed.

- [ ] resumeJob / resumeQueueItem race (jobqueue/server.go ~3130-3155):
      s.q.Resume makes a suspended item reservable before the resume sets
      job.State and queues updateJobAfterChange; a reservation in that window
      could have its State overwritten or its write superseded by the
      resume's (same class as the kick race above). Not yet reproduced.
  - Source: kick-race implementor, incidental finding.
