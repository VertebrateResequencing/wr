# Status-count deltas arrive after the seed counted them (2026-10-08)

- Branch: `statusdelta-b6642140`
- Base: `origin/develop` at `2dda10c8` (#685)
- Worktree: `../wr-statusdelta`
- Queue owner: this branch, this checklist; item 8 of
  `.docs/reserve-runstate/delivery-queue.md`.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: targeted `go test` runs, plain and `-race`; `make lint`,
`make test`, `make race`; `cleanorder -min-diff` on the edited Go files. The
fix touches `queue.changed`, which runs on every queue move, so it also needs
`make speed` against the base and a focused reserve-under-load benchmark.

## Prior checked items that must not regress

- `260820-2.md` Bug D and D2: the seed is bracketed by `begin`/`end`
  boundaries under the connection's write mutex, no live delta lands inside
  the bracket, and the shipped client resets on `begin`
  (`TestReliable4StatusSeedBoundary`; the build-tagged
  `TestReliable4StatusSeedOverlap`).
- `260927-status-page-count-decoding.md`: the seed is counted from queue item
  states without decoding jobs (`TestReliable4StatusSeedCounts`).
- `TestReliable2WebRevertDeltaFeed`, `TestReliable2StatusFeedNeverDrops` (the
  status feed never drops a delta), `TestServerWebISuspendedStatus`, and the
  single-`SetChangedCallback` guard in `jobqueue/jobqueue_test.go`.

## Items

- [x] the queue's change callback runs in a goroutine, so a status delta can
      arrive after a new status-page connection's seed snapshot already
      counted that move, double-counting it until refresh; order deltas
      against the seed (e.g. a queue change sequence number) and make the
      test helper reset on `begin`
  - Source: delivery queue item 8; found as item 5 of
    `261008-kick-stale-job-b2ccc64b.md`.
  - Red test: `TestStatusLateDeltaAfterSeed` in
    `jobqueue/status_late_delta_test.go`. Before any job is added it wraps
    the queue's change callback so the callback for one move of a target job
    blocks. It makes the move, opens a status websocket, asks for the seed,
    waits until the recorded stream holds the seed's `end`, then releases the
    callback. It replays the stream through the shipped
    `websocket-handler.js` (`replay-stream.mjs`) and compares the shown
    counts, for the rep group and `+all+`, with the queue. Three moves:
    suspend (ready to suspended), reserve (ready to running) and add (new to
    ready).
  - Red command: `nice -n 19 timeout 900 go test -tags netgo ./jobqueue
    -count=1 -run 'TestStatusLateDeltaAfterSeed$' -v`, exit 1 on `2dda10c8`,
    3 of 3 runs, all three subtests failing (about 6 s). Also exit 1 under
    `CGO_ENABLED=1 ... -race` with the same failures and no race report.
    Excerpt (log lines filtered):

    ```text
    Expected: map[string]map[string]int{"+all+":{"ready":1, "suspended":1},
      "rg-late-delta-suspend":{"ready":1, "suspended":1}}
    Actual:   map[string]map[string]int{"+all+":{"ready":0, "suspended":2},
      "rg-late-delta-suspend":{"ready":0, "suspended":2}}
    Expected: map[string]map[string]int{"+all+":{"ready":1, "running":1},
      "rg-late-delta-reserve":{"ready":1, "running":1}}
    Actual:   map[string]map[string]int{"+all+":{"ready":0, "running":2},
      "rg-late-delta-reserve":{"ready":0, "running":2}}
    Expected: map[string]map[string]int{"+all+":{"ready":2},
      "rg-late-delta-add":{"ready":2}}
    Actual:   map[string]map[string]int{"+all+":{"ready":3},
      "rg-late-delta-add":{"ready":3}}
    --- FAIL: TestStatusLateDeltaAfterSeed (5.92s)
        --- FAIL: TestStatusLateDeltaAfterSeed/suspend (2.31s)
        --- FAIL: TestStatusLateDeltaAfterSeed/reserve (1.84s)
        --- FAIL: TestStatusLateDeltaAfterSeed/add (1.77s)
    ```

    Control: with the callback released before the websocket is opened
    (a throwaway edit, reverted), all three subtests pass. So each fails
    only because the delta arrives after the seed.
  - Other moves: every `queue.changed` call is made under the queue mutex
    and hands its callback to a new goroutine, so every move can be late:
    adds, reserve, release, bury, kick, TTR moves, delay to ready,
    dependency release, suspend, resume and removal. A move out of a bucket
    the seed still shows occupied double-counts at once (suspend, reserve).
    A move out of an empty bucket parks in the client's `pending` and
    corrupts a later arrival instead. The running to lost (TTR) and lost to
    running (touch) deltas are not queue moves: the first is sent under the
    queue mutex, the second outside it, and the seed reads `job.Lost` after
    its snapshot. Both keep the late-delivery residual as well as the seed
    walk's: the status pump takes such a delta from `receiver.In` and
    blocks on `writeMutex.Lock()`, which `sendCurrentStatusCounts` holds
    from before `begin` until after `end`. A job whose lost flag changes
    before the walk reads it, with its delta not yet written when the seed
    takes the mutex, is seeded in its new state and its delta is written
    after `end`.
  - Seed walk residual: `statusSeedCounts` takes `q.AllItems()` under the
    queue lock but reads each item's state later, so a move during the walk
    is in the seed and in a delta after `end`, even if the callback were
    synchronous. A fix that orders deltas against the seed must take the
    item states and the order mark in one queue lock hold.
  - Client model: `handleSeedBoundaryMessage` in
    `jobqueue/static/js/wr/websocket-handler.js` resets every tracker on
    `begin` and ignores `end`. `handleStateChangeMessage` keeps an occupancy
    model: an exit moves at most the bucket's occupancy and parks the rest
    in `pending`. The red test replays through this file, so its verdict is
    the browser's. The Go helper `deltaCounts` (`reliable2_webrevert_test.go`)
    adds and clamps instead, and `readJStateDeltasUntil` decodes only
    `jstateCount`, so it never sees `begin` and applies pre-seed deltas.
    The previous probe's `join` variant (delta released after the socket
    joins, before `begin`) fails `TestServerWebISuspendedStatus`'s
    `readJStateCounts` assertion after 3.1 s on `2dda10c8`, while the browser
    drops that delta: a helper fault, the reported flake.
  - Change callback consumers: one in production,
    `Server.createQueue` (`jobqueue/server.go`), feeding
    `emitChangeCallbackTransition`: the status-count deltas, then the
    per-job subscription updates (`wr add --sync`, the status page's job
    details), which can block in `waitForJobStartTime`. Scheduling, limit
    groups and RAC use the ready-added and TTR callbacks, not this one.
  - Fix options (owner chose A, 2026-10-08):
    - A (recommended): a per-queue change sequence. `changed` increments a
      `uint64` under the queue mutex it already holds and passes it to the
      callback. `jstateCount` carries it unserialised (`json:"-"`); deltas
      not from a queue move carry 0. A new queue method returns every
      item's state and the current sequence in one read-lock hold, and the
      seed counts from those states. Each status connection keeps its seed's
      sequence, set and read under its write mutex; the status pump drops a
      delta whose sequence is non-zero and at or below it. This closes the
      late delta and the walk residual for queue moves' live buckets. Lost
      and touch deltas keep both the late-delivery residual (any not yet
      written when the seed takes its snapshot) and the walk residual, and
      a RepGroup's `complete`
      seed keeps residual (b) of `260820-2.md` (see the follow-up below). No
      JS change. Hot path: one increment and one more argument per move, 8
      bytes per delta, one compare per write.
    - B: one ordered dispatcher goroutine per queue instead of a goroutine
      per change. Ordering alone does not fix this: a backlogged dispatcher
      still delivers after the seed. It needs a seed barrier in the same
      stream (A's mechanism again), and serialises subscription delivery
      behind `waitForJobStartTime`. Not recommended.
    - C: send the status deltas synchronously under the queue mutex and
      discard the connection's queued deltas when the seed takes its
      snapshot under the same lock. Correct without a sequence, but adds a
      job read lock, a map and a caster send per move under the queue
      mutex, which reserve latency would pay. Not recommended.
  - Test helper: `readJStateDeltasUntil` should decode `statusWSMessage` and
    restart its accumulator on `begin`, needed under any option, since
    deltas written before `begin` are still delivered.
  - Fix (option A):
    - `queue/queue.go`: `changed` increments a per-queue `changeSeq` under
      the queue mutex every move already holds for writing and passes it as
      a new last argument of `ChangedCallback`. New `Snapshot()` returns
      every item's data and state (`ItemSnapshot`) and the sequence in one
      read-lock hold.
    - `jobqueue/jobtransition.go`, `jobqueue/server.go`,
      `jobqueue/serverCLI.go`: the change callback passes the sequence
      through `emitChangeCallbackTransition` and `emitJobTransition` to
      `sendStatusCounts`, which sets the unexported (so never serialised)
      `jstateCount.seq`. The lost (TTR) and touch transitions pass 0.
    - `jobqueue/serverWebI.go`: `wsWriteMutexes` now holds `wsConnWriter`, a
      mutex plus `seedSeq`. `statusSeedCounts` counts from `Snapshot()` and
      returns its sequence; the seed handler stores it under the write
      mutex, and the status pump, under the same mutex, drops a delta with
      a non-zero `seq` at or below it. No JS or wire change.
    - Test helper `readJStateDeltasUntil`
      (`jobqueue/reliable2_webrevert_test.go`) decodes `statusWSMessage`,
      restarts its accumulator on `begin` like the page, and checks its
      predicate only outside a seed bracket.
    - New tests: `TestReadJStateDeltasUntilSeedBegin`
      (`jobqueue/status_late_delta_test.go`, a scripted stream with a
      delta before `begin`; fails on the old helper, false after 2 s) and
      `TestQueueChangeSequence` (`queue/queue_test.go`). Callers updated
      for the new signatures in `queue/queue_test.go`,
      `jobqueue/status_late_delta_test.go`,
      `jobqueue/reliable4_seedboundary_test.go`,
      `jobqueue/reliable4_seedoverlap_test.go` and
      `jobqueue/run_state_reset_test.go`. `TestServerWebISuspendedStatus`
      and the `SetChangedCallback` guard are unchanged.
  - Red to green: the red command passes, 3 of 3 runs plain and once under
    `CGO_ENABLED=1 -race`. Mutant: with the pump's drop disabled, all three
    subtests fail again.
  - Gates (`nice -n 19`, `GOFLAGS=-p=2`): targeted status, web, seed, delta
    and suspend tests in `./jobqueue` plus all of `./queue`, plain and
    `-race`: pass, including `TestReliable4StatusSeedBoundary`.
    `go vet` with the `reliability` and `reliability_repro` tags: clean.
    `make lint`: 0 issues. `make test`: 968 passed, 22 skipped.
    `CGO_ENABLED=1 make race`: 968 passed, 21 skipped. `cleanorder
    -min-diff`: this first run's claim was wrong; it wanted `Snapshot()`
    in `queue/queue.go` moved, fixed in the review follow-up below.
  - Speed: `make speed SPEED_BASE=2dda10c8`: PASS, nothing worse by more
    than 10% at p<0.05 (QueueLifecycle sec/op +1.2% geomean, n.s.; B/op
    and allocs/op flat). Focused interleaved run of QueueLifecycle and the
    three `*UnderReserveLoad` benchmarks, 5 rounds per tree (the run was
    cut in round 6 by its timeout): no sec/op or reserve latency change
    was significant; ResumeUnderReserveLoad allocs/op showed +32%
    (p=0.032) with B/op flat. Rerun alone at 10 rounds per tree:
    sec/op +1.0% (p=0.579), reserve-p99 -5.9% (p=0.912), allocs/op -6.4%
    (p=0.971), B/op +0.6% (p=0.353): PASS, so the earlier flag was noise
    from the concurrent reservers' varying reserve counts.
  - Review follow-up (cycle 2):
    - `queue/queue.go`: `cleanorder -min-diff` moved `Snapshot()` from
      after `BuryWaiting` to after `KickWith`. `cleanorder -dry` prints the
      whole file and exits 0 either way, so each of the 11 edited Go files
      was checked by comparing its `-dry` output to the file: all identical.
    - New test `TestStatusLostDeltaAfterSeed`
      (`jobqueue/status_late_delta_test.go`) for the pump's `seq != 0`
      guard, which no test covered. A job is reserved and started, and a
      status page seeds at a non-zero sequence. The test shortens the item's
      TTR and touches it in the queue, which wakes TTR processing, so the
      job goes lost (sequence 0 delta). The page then asks for a second
      seed while the job is lost, the TTR is lengthened again, and a client
      `Touch` brings the job back (sequence 0 delta). After each step the
      stream is replayed through `websocket-handler.js` and must show the
      job lost, then running, for the rep group and `+all+`.
      `waitForSeedEnd` became `waitForSeedEnds(recorder, n)`.
    - Red: with `alreadySeeded` changed to `ok && delta.seq <= w.seedSeq`,
      `nice -n 19 timeout 900 go test -tags netgo ./jobqueue -count=1 -run
      'TestStatusLostDeltaAfterSeed$' -v` exits 1:

      ```text
      Expected: map[string]int{"lost":1, "running":0}
      Actual:   map[string]int{"lost":0, "running":1}
      --- FAIL: TestStatusLostDeltaAfterSeed (2.12s)
      ```

      A mutant that drops only the touch delta (seq 0 with `FromState`
      lost) also exits 1, at the touch step (`Expected lost 0 running 1,
      Actual lost 1 running 0`). Both mutants were reverted by restoring a
      saved copy, confirmed identical with `cmp`. The test passes 4 of 4
      runs plain and once under `-race`.
    - Comments: the `jstatusSeedBoundary` and `statusSeedCounts` comments
      (`jobqueue/serverWebI.go`) and option A above no longer claim
      exactness. Two residuals remain. Lost and touch deltas carry no
      sequence, so one during the seed walk can be counted twice. Residual
      (b) of `260820-2.md`, pre-existing: a RepGroup's `complete` seed count
      is read from the database after `Snapshot()`, and a job is archived
      in the database before `removeArchivedItem` takes it out of the
      queue. A job archived before that read but removed after the snapshot
      is seeded as both running and complete, and its removal delta (its
      sequence is above the seed's) leaves `complete` one too high until a
      refresh.
    - Lock hold: `Snapshot()` holds the queue read lock while it takes two
      item read locks per item (`Data()` and `State()`). It runs only when
      a status page asks for a seed, so this is acceptable.
    - Gates (`nice -n 19`, `GOFLAGS=-p=2`): targeted `Status|WebI|Seed|
      Delta|Suspend|Queue` tests in `./jobqueue` plus all of `./queue`:
      pass plain; `Status|WebI|Seed|Delta|Suspend` in `./jobqueue` and all
      of `./queue` pass under `CGO_ENABLED=1 -race`. `make lint`: 0 issues.
      `make test`: 969 passed, 22 skipped. `CGO_ENABLED=1 make race`: 969
      passed, 21 skipped. `cleanorder -min-diff`: all 11 edited Go files
      unchanged. Production changes are a method move and comments, so
      `make speed` was not rerun.
  - Review follow-up (cycle 3):
    - Comments: cycle 2 understated the lost and touch residual as limited
      to the seed walk. The status pump takes a delta from `receiver.In`
      and then blocks on `writeMutex.Lock()`, which
      `sendCurrentStatusCounts` holds from before `begin` until after
      `end`. So a job whose lost flag changes before the walk reads it,
      with its delta (sequence 0, never dropped) not yet written when the
      seed takes the mutex, is seeded in its new state and its delta is
      written after `end`: the late-delivery bug, for sequence 0 deltas.
      The `jstatusSeedBoundary` and `statusSeedCounts` comments, the
      "Other moves" bullet and option A now say lost and touch deltas keep
      both the late-delivery residual and the walk residual. Residual (b)
      text is unchanged.
    - `testStatusLateDelta` (`jobqueue/status_late_delta_test.go`): the
      bare `<-held` is now `So(waitForClose(held), ShouldBeTrue)`, a new
      helper that selects on the channel with a 5 s timeout. With
      `close(held)` removed (a throwaway edit, restored and confirmed with
      `cmp`), each subtest fails after about 5 s instead of hanging until
      the `go test` timeout.
    - Gates (`nice -n 19`, `GOFLAGS=-p=2`): the three tests in
      `status_late_delta_test.go` pass plain and under
      `CGO_ENABLED=1 -race`. `make lint`: 0 issues. `make test`: 969
      passed, 22 skipped. `cleanorder -min-diff -dry` output matches the
      file for `jobqueue/serverWebI.go` and
      `jobqueue/status_late_delta_test.go`.
      Comments and test only, so `make speed` was not rerun.
- [x] `readAbsoluteStateUntil` (`jobqueue/serverWebI_test.go`) ignores seed
  boundaries and never resets on `begin`, like the old
  `readJStateDeltasUntil`; and its caller asserts both totals `== 0`, a
  predicate that already holds before any message is read, so that assertion
  passes vacuously and proves nothing. Its other caller is further down the
  same file.
  - Source: item 1 implementer, incidental; test-only, same helpers, so kept
    on this branch after item 1.
  - Also (item 1 cycle 3 reviewer, non-blocking): `readJStateDeltasUntil`
    checks its predicate only outside a seed bracket, but nothing tests that;
    removing the gate survives every test. Add a scripted-stream case where
    the predicate holds partway through a seed but not at `end`.
  - What the callers prove: `TestStatusCurrentAbsoluteState`'s last step
    reconnects after removing the rep group's jobs and must see a seed that
    shows the rep group and `+all+` empty. `TestServerWebI`'s "responds to
    current requests" must see a seed with exact running, complete and
    buried counts. Both are about the seed, so the helper must judge a whole
    seed, not whatever it has read so far.
  - Red (test-only, so mutants show the vacuity; each was reverted by
    restoring a saved copy and confirmed with `cmp`). Command:
    `nice -n 19 timeout 900 go test -tags netgo ./jobqueue -count=1 -run
    'TestStatusCurrentAbsoluteState$' -v` on `7723961d`:
    - M1, test mutant: the reconnect never asks for a seed (its
      `jstatusRequestCurrent` write replaced by `err = nil`), so the server
      sends nothing. Exit 0, `--- PASS: TestStatusCurrentAbsoluteState`.
    - M2, production mutant: `statusSeedCounts` reports `+all+` ready 2
      when the queue is empty (a seed still counting removed jobs). Exit 0,
      `--- PASS: TestStatusCurrentAbsoluteState`.
    - G, the gate: `readJStateDeltasUntil` with `!inSeed &&` removed (plus
      `_ = inSeed`, so it still compiles). The
      targeted `Status|WebI|Seed|Delta|Suspend|WebRevert` tests in
      `./jobqueue` exit 0 (64 s).
  - Fix (test-only):
    - `jobqueue/reliable2_webrevert_test.go`: `deltaCounts` gains `seeded`,
      which `readJStateDeltasUntil` sets on `end`; a `begin` still starts a
      fresh `deltaCounts`, clearing it.
    - `jobqueue/serverWebI_test.go`: `readAbsoluteStateUntil` now calls
      `readJStateDeltasUntil` with its predicate guarded by `acc.seeded`, so
      it resets on `begin`, checks only outside a seed bracket, and is only
      satisfied once a seed has ended. Callers and their predicates are
      unchanged. `cleanorder -min-diff` also moved two pre-existing
      `statusCountReconcile*` consts in this file (the file at `7723961d`
      already differed from its `-dry` output).
    - `jobqueue/status_late_delta_test.go`: new scripted-stream tests.
      `TestReadJStateDeltasUntilSeedBracket`: a seed whose first delta
      matches (`+all+` ready 1, running 0) but whose second (running 1) does
      not must not satisfy the reader. `TestReadAbsoluteStateUntilSeed`:
      wanting no jobs is false with no seed and false for a seed still
      counting jobs, and true for an empty seed after deltas received
      before its `begin`.
  - Red to green, same command and mutants after the fix:
    - M1: exit 1 (`Expected: true`, `Actual: false`, `--- FAIL:
      TestStatusCurrentAbsoluteState (3.83s)`).
    - M2: exit 1 (`Expected: true`, `Actual: false`, 3.64 s).
    - G: `-run 'TestReadJStateDeltasUntilSeed|TestReadAbsoluteStateUntilSeed$'`
      exits 1 (`Expected: false`, `Actual: true`, `--- FAIL:
      TestReadJStateDeltasUntilSeedBracket`).
    - R, no reset on `begin` (`acc = newDeltaCounts()` removed):
      `-run 'TestReadAbsoluteStateUntilSeed$'` exits 1 (`Expected: true`,
      `Actual: false`).
    - Unmutated, the new tests, `TestStatusCurrentAbsoluteState` and
      `TestServerWebI` pass.
  - Gates (`nice -n 19`, `GOFLAGS=-p=2`): targeted
    `Status|WebI|Seed|Delta|Suspend|WebRevert|ReadAbsolute|ReadJState`
    tests in `./jobqueue` pass plain (62 s) and under `CGO_ENABLED=1 -race`
    (97 s). `make lint`: 0 issues. `make test`: 971 passed, 22 skipped.
    `CGO_ENABLED=1 make race`: 971 passed, 21 skipped. `cleanorder -min-diff
    -dry` output matches all three edited Go files. Test-only, so no
    `make speed`.
