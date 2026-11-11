# Bugfix: reserve latency under bulk kick, resume and suspend on the log store

- Branch: customdb-f397b547
- Base: customdb-f397b547 at 09c38065
- Queue owner: customdb-f397b547, .docs/delivery-queue.md row 20
  (phase 1 gate item 1g.4, .docs/customdb/phases/phase1-gate.md)

## Items

- [x] 1g.4 (make speed-full against d1a4b6d7) fails on reserve latency in
  the serve()-based benchmarks, now that the log store is the default store:
  BenchmarkKickUnderReserveLoad, ResumeUnderReserveLoad and
  SuspendUnderReserveLoad: reserve-p50-us +244% to +678%, reserve-p99-us
  +64% to +121%; AddManyUnderReserveLoad: reserve-p99-us +18% to +20%; Kick
  allocs/op: +11.45%. Their sec/op improved by 48-87%. It was already like
  this at 27c10a45, before the latest fix.
  - Source: phase 1 gate item 1g.4
    (.docs/customdb/results/phase1-gate-1g4.md, "Fix round 4")
  - Root cause: see "Reserve latency fix" in
    .docs/customdb/results/phase1-gate-1g4.md. In short, two defects and one
    artefact:
    1. A kick, resume or suspend of many jobs takes the queue's lock once per
       job and went straight on to the next job. Unlocking wakes one waiting
       reserve, which then has to be scheduled before it can take the lock;
       the loop took it again first, every time, until the reserve had
       waited 1ms and Go's mutex handed it over (HEAD's reserve-p99 was 1.05
       to 1.35ms). bbolt's per-job encode, done outside the lock, had left a
       gap of about 70us between holds, long enough for a waiter to get in;
       the log store does no such work, so the gap fell to a few us.
    2. The log store did its whole state-record write inside the queue's
       lock: `queueJobChangeAhead` encoded the record and appended it to the
       wal (about 3us of allocation and encoding per job, 4 to 5us per
       item inside the lock against bbolt's 0.6us, 8 to 13us with reserves
       running). With the op paced to bbolt's speed, that alone doubled
       reserve-p50 (17 to 26us against 10 to 15us).
    3. AddManyUnderReserveLoad's reserve-p99 and Kick's allocs/op are not
       defects. The add's slow reserves per op are about as many as base's; there
       are fewer reserves in all because the add is twice as fast, so the
       same slow reserves make a higher percentile. Kick's extra allocations
       were the log store's folding of the benchmark's own setup adds,
       landing in the timed window.
  - Red commands, both against 09c38065's server.go and store_log.go with the
    new tests (exit 1):
    `(unset $(compgen -v | grep '^OS_'); HOME=<scratch> CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run 'TestBulkQueueOpsLetReservesIn|TestLogStoreJobChangeAppendedLater')`

    ```text
    bulk_queue_yield_test.go Line 212:
    Expected '200' to be between '1' and '2' or equal to one of them (but it wasn't)!
    (once each for the kick, the resume and the suspend)
    --- FAIL: TestBulkQueueOpsLetReservesIn (0.56s)
    store_log_change_test.go Line 86:
    Expected: 0
    Actual:   100
    --- FAIL: TestLogStoreJobChangeAppendedLater (0.01s)
    ```

    `TestBulkQueueOpsLetReservesIn` runs each bulk op on one processor with
    GC off and starts a reserve after the op's first job: before the fix the
    reserve ran only once all 200 jobs were done.
    `TestLogStoreJobChangeAppendedLater` then counted wal appends made while
    100 `queueJobChangeAhead` calls ran on one processor: 100 before the
    fix. (The Line 212 above is the assertion's line in the final file; the
    first red run cited Line 159, before cleanorder moved the helpers above
    the test.)

    Review round 1 found that version flaky: `logAppendedHook` is
    process-wide, so it could count another store's appends. It was
    rewritten to hold this store's change drain with a hook
    (`logChangeDrainHook`), check that all 100 calls return with their seqs
    taken and their records queued, release the drain, and check the
    records are durable across a reopen. A reservation made while one
    change is queued appends the queued changes first and takes a later
    seq, so that checks wal order and seq agree; seq outranking wal order is
    covered by `TestLogStoreStateSeqOrder` (and the reducer's
    `TestLogStoreReducerOrder` and `TestLogStoreReducerSeqRules`). It reads
    the change queue, so it does not compile against 09c38065; the "append
    on the caller" mutant below is 09c38065's behaviour, and fails it at
    Line 70 of round 1's file, Line 85 of the final file (Expected: 100,
    Actual: 0).
  - Approach:
    - `kickJobs`, `resumeJobs` and `suspendJobs` call `yieldQueueTurn`
      (`runtime.Gosched`) after each job, so a reserve woken by the loop's
      unlock runs before the loop takes the queue's lock again.
    - The log store's `queueJobChangeAhead` makes the change and takes the
      state record's seq and run state under the queue's lock, as before,
      and queues them (`logChangeQueue`); one goroutine at a time, started
      by the push that finds none running, encodes and appends them. A
      queued record counts in `writing`, so `flush` waits for it, and
      `close` waits for the queue to drain before closing the writer. The
      reviving path (a write in flight making the key live) is unchanged.
    - The `*UnderReserveLoad` benchmarks call `settleStore` before timing,
      as `BenchmarkAddDepGroupMember` does since 1g.4's fix round 3, so the
      log store's fold of the benchmark's setup adds is not timed as the op.
    - The spec's "Write path" says how a queued change reaches the wal.
  - Files: jobqueue/server.go, jobqueue/store_log.go,
    jobqueue/bulk_queue_yield_test.go (new),
    jobqueue/store_log_change_test.go (new),
    jobqueue/kick_resume_bench_test.go,
    jobqueue/dep_group_add_bench_test.go (`settleStore` flushes),
    jobqueue/store_log_inspect_test.go, .docs/customdb/spec.md,
    .docs/customdb/results/phase1-gate-1g4.md and its rl-*.txt files.
  - Mutants (each in its own plain copy of the fixed tree, each compiles,
    each killed):

    | Mutant | Killed by |
    | --- | --- |
    | `yieldQueueTurn` does nothing | TestBulkQueueOpsLetReservesIn (all three ops: 200) |
    | no yield in `kickJobs` | TestBulkQueueOpsLetReservesIn (kick: 200) |
    | no yield in `resumeJobs` | TestBulkQueueOpsLetReservesIn (resume: 200) |
    | no yield in `suspendJobs` | TestBulkQueueOpsLetReservesIn (suspend: 200) |
    | `queueJobChangeAhead` appends on the caller (`appendChange` for `push`) | TestLogStoreJobChangeAppendedLater (100 appends, want 0) |
    | `close` does not wait for the change queue | TestLogHeldCloseDrains (Expected 0) |
    | queued changes not counted in `writing` | TestLogStoreJobChangeAppendedLater (appends after flush, want 100) |

  - Speed: the four benchmarks against d1a4b6d7, 6 interleaved rounds:
    every Kick, Resume and Suspend figure passes (reserve-p99 -75% to
    -83%, p50 unchanged, allocs/op -9% to -14%); `speed.sh` fails only
    AddManyUnderReserveLoad/norc reserve-p99, +10.29% (p=0.009), the
    sampling artefact above: about as many slow reserves per add as base
    (over 1ms, mean 22 against 18), though, the add being twice as fast,
    about twice as many per second of add.
    `BenchmarkAddDepGroupMember`, report-storm and dep-granularity-check:
    PASS. Details and run directories in the results doc.
  - Review round 1 fixes (this round's code):
    - The change test was made deterministic (above).
    - A panic appending a queued change no longer wedges the store. The
      drain's `defer internal.LogPanic(..., false)` exited the goroutine
      without closing the drain, so later changes were never written,
      `writing` never fell (flush hung), `close` waited forever and adds of
      the key waited on its state write. `appendChange` now recovers a
      panic per record: it logs it at Crit with its stack, calls the
      record's `finished` (once, via `sync.OnceFunc`, whether or not the
      write took it), and the drain goes on, decrementing `writing` as for
      any record. Chosen to match the log store's own background
      goroutines (folder, checkpointer, backup ticker), which log a panic
      and do not exit the manager, and its failed writes, which are logged
      and lose the record; bbolt's writers exit the manager instead, which
      a test could not inject. `TestLogStoreJobChangeAppendPanic` panics the
      first of three queued changes via `logChangeAppendHook` and checks
      that flush returns, the other two are durable, no key is left in
      `stateWrites`, and a later add of the lost change's job and close
      return.
    - `settleStore` now calls the store's `flush`, which waits for queued
      job changes too, so the prepare step's bury, suspend and delay
      changes are appended before timing.
    - `queueJobChangeAhead` and `lockedStateFrames` share
      `takeStateLocked`, which takes the run state and seq.
    - Comments: `queueJobChangeAhead` names `releaseAbandonedItem`; the
      `close` comment and the `kickResumeBenchServer` comment are rewrapped.
    - A defect the gates found in the deferred append, fixed in this
      round: `make test` failed `TestResumeAfterChangeWritesResumedJob`
      once (resume_order_test.go Line 578: Expected "ready", Actual
      "suspended"). The test relies on a durable write made after a change
      committing no earlier than it, which held while the change was
      appended by its caller; with the change queued, the later write
      could be appended, and durable, first. `write` now appends the job
      changes queued before it (`appendQueuedChanges`, under the queue's
      new `appendMu`, which the drain also holds while it appends a
      batch), so the order of appends again follows the order of calls.
      The drain takes `closeMu`'s read lock before `appendMu`, as writes
      do, and fails what it finds queued once the store is closed.
      `TestLogStoreWriteAfterQueuedChange` holds the drain, queues a
      change, makes a durable write of another job, and requires the
      change to be durable when it returns.
    - `TestLogStoreCloseWaitsForQueuedChanges` holds the drain with a
      change queued, starts `close`, requires it not to refuse writes
      within a second, then releases the drain and requires the change to
      survive a reopen. (The existing `TestLogHeldCloseDrains` no longer
      kills the "close does not wait" mutant once writes append queued
      changes.)
  - Mutants of round 1's fixes, on round 1's code (plain copies, each
    compiles, each killed; lines are in round 1's
    jobqueue/store_log_change_test.go, since reordered):

    | Mutant | Killed by |
    | --- | --- |
    | change appended on the caller (09c38065's behaviour) | TestLogStoreJobChangeAppendedLater, Line 70 (Expected 100, Actual 0) |
    | seq taken lazily by the drain | TestLogStoreJobChangeAppendedLater, Line 69 (Expected 100, Actual 0) |
    | no `finished` on a recovered panic | TestLogStoreJobChangeAppendPanic, Line 153 (Expected 0, Actual 1) |
    | no recover in `appendChange` (round 1's run) | the panic crashes the test binary (FAIL) |
    | queued changes not counted in `writing` | TestLogStoreJobChangeAppendPanic, Line 147 (flush returned before the drain ran) |
    | `write` does not append the queued changes first | TestLogStoreWriteAfterQueuedChange, Line 199 (Expected 0, Actual 1) |
    | `close` does not wait for the change queue | TestLogStoreCloseWaitsForQueuedChanges, Line 233 (Expected false, Actual true) |

  - A/B of the four benchmarks against d1a4b6d7 on round 1's fixes, 6
    interleaved rounds (run-1794353174): every Kick, Resume and Suspend
    figure passes (reserve-p99 -70% to -80%, p50 not worse, allocs/op -7%
    to -10%); `speed.sh` fails AddMany reserve-p99, norc +17.46% (p=0.004)
    and rc +16.11% (p=0.026), the same per-add/per-second question, left to
    the gate owner.
  - Final A/B on all fixes (run-1794361157), the four benchmarks,
    `BenchmarkAddDepGroupMember`, report-storm and dep-granularity-check
    against d1a4b6d7, 6 interleaved rounds: `speed.sh` PASS. Kick, Resume
    and Suspend reserve-p99 -66% to -80%, p50 not worse, allocs/op -10% to
    -14%; AddMany reserve-p99 +9.7% and +12.6%, not significant.
  - Final gates, scratch `HOME`, every `OS_` variable unset, 01:10 to 01:39
    GMT on 2026-11-11: `make lint` 0 issues; `make test`,
    `WR_TEST_STORE=all make test` and `make race` each 1,279 passed. The
    change tests passed `-race -count 100` three times under a parallel
    `-race -count 50` load.
  - Review round 2 of the code (tests and docs; no deadlock, ordering or
    durability defect found):
    - `TestLogStoreWriteWaitsForBatchBeingAppended` pins the in-batch order
      that `appendMu` and decrementing `queued` after each append exist
      for: the drain takes two queued changes and is held appending the
      second (`logChangeAppendHook`, released after 500ms, at most 2s); a
      durable write of a third job must leave the second change durable
      when it returns.
    - The drain-holding helper (`openDrainHeldLogStore`) registers its
      release before the store's close, so close never waits out the
      hold; `TestLogStoreWriteAfterQueuedChange` now takes 0.01s, and every
      change test takes at most about 1s
      (`TestLogStoreCloseWaitsForQueuedChanges`, which watches close for
      1s by design).
    - `TestLogStoreRevivingJobChange`: a change of a job whose key a held
      put-back is making live is appended once the put-back finishes.
    - A panic in a change's write after it has counted itself in `writing`
      (`writeFramesCounted` sets a flag once it has) gives that count back;
      a `finished` that panics is recovered, not re-panicked through the
      `sync.OnceFunc`. `TestLogStoreJobChangeWritePanic` (a panic in
      `logAppendedHook` during the change's write) and
      `TestLogStoreJobChangeFinishPanic` (a queued change whose append and
      finish both panic) cover them.
    - `TestLogStoreDrainHoldsCloseMu`: each record the drain appends is
      appended with `closeMu` held (`TryLock` fails inside the append
      hook).
    - The `errModifyMismatch`/`jobRecordTypes` block is back where it was
      (`cleanorder -min-diff` moves it, so it was not rerun on
      store_log.go); the flush comment is rewrapped; the results doc's
      "Fix" section is updated, and the "outranks" wording replaced (seq
      outranking wal order is `TestLogStoreStateSeqOrder`'s).
  - Mutants of review round 2's points, on the final code (plain copies,
    each compiles, each killed; lines are in the final
    jobqueue/store_log_change_test.go):

    | Mutant | Killed by |
    | --- | --- |
    | m2: no `appendMu` in `appendQueuedChanges` | TestLogStoreWriteWaitsForBatchBeingAppended: the output reports the `So` inside `soLiveState` (Line 533 in round 2's file, 537 in the final one), called at Line 285 (Expected "buried", Actual "ready") |
    | m8: `queued` decremented before `appendChange` | the same, at the same lines |
    | m11: reviving change dropped, not go-appended | TestLogStoreRevivingJobChange, Line 398 (Expected true, Actual false) |
    | m14: drain appends without `closeMu` | TestLogStoreDrainHoldsCloseMu, Line 356 (Expected 0, Actual 2) |
    | no `writing` give-back on a panic after counting | TestLogStoreJobChangeWritePanic, Line 212 (flush still pending) |
    | no recover around `finished` on a panic | TestLogStoreJobChangeFinishPanic: the panic kills the test binary |

    The change tests passed `-race -count 30` three times while a `-race
    -count 20` run of the `TestLogStore*`, `TestLogHeld*` and
    `TestBulkQueueOpsLetReservesIn` tests ran alongside.
  - Gates on review round 2's code, scratch `HOME`, every `OS_` variable
    unset, 05:11 to 05:39 GMT on 2026-11-11: `make lint` 0 issues;
    `make test`, `WR_TEST_STORE=all make test` and `make race` each 1,284
    passed.
  - A/B of the four benchmarks on review round 2's code (run-1794375551):
    Kick, Resume and Suspend pass (reserve-p99 -73% to -83%); `speed.sh`
    fails AddManyUnderReserveLoad/rc reserve-p99, +23.37% (p=0.004; norc
    +15.5%, not significant), the same per-add against per-second
    question, left to the gate owner. Adds and reservations do go through
    the changed `write` (the `queued` load, an inline append of any queued
    changes, the `counted` flag), but the add's measured hold of the
    queue's lock shows no material cost from that.
  - Review round 3 of the code (two surviving mutants; code found sound):
    - `TestLogStoreChangeQueuedDuringBatch`: a change queued while the drain
      is appending an earlier batch must be appended by that drain (flush
      returns within 3s, and the change survives close and a reopen). It
      kills mD (`endDrainIfEmpty` ends the drain without checking for
      records queued meanwhile) 3 of 3 runs: Line 583, Expected nil,
      Actual 'still pending'.
    - `TestLogStoreChangeAfterClose`: a change queued after close is
      dropped, leaving no key in `stateWrites` and nothing counted in
      `writing`. It kills mE (`failQueuedChanges` not calling `finished`)
      3 of 3 runs: Line 611, Expected true, Actual false.
    - Both pass `-race -count 3`. The m2/m8 rows above now cite the line
      the failure output reports; the docs no longer say the fix leaves the
      add's path untouched (adds pay the changed `write`'s checks); the
      `yieldQueueTurn` comment's last sentence is joined to its paragraph.
    - Gates on review round 3's code, scratch `HOME`, every `OS_`
      variable unset, from 07:36 GMT on 2026-11-11: `make lint` 0 issues;
      `make test` 1,286 passed; `make race` 1,286 passed;
      `WR_TEST_STORE=all make test` failed once, on
      `TestClientAddAndWaitCancelledDuringOutage` (subscription_test.go
      Line 252: Expected '1m0.000850283s' to be less than '3.8s'), a known
      flake. A load-matched A/B, this tree and 09c38065 run at once with
      `WR_TEST_STORE=all`, `-count` 15, 30 and 60: this tree failed 1 of
      105 runs, 09c38065 failed 3 of 105, each time the same assertion.
      The failure happens after the manager has stopped, in the client's
      cancellation while another request holds its lock, which this fix
      does not change. `WR_TEST_STORE=all make test` was then rerun:
      1,286 passed (08:38 to 08:46 GMT).
  - Review round 2 (coordinator): the add path was checked for the same
    pattern and has none. `queue.AddMany` takes the queue's lock once per
    add and never re-takes it per item; the code it runs under the lock is
    the same on both trees and does no store I/O (`cachedReqGroupRecs`
    reads the shared `reqStatCache`); the log store writes the add's
    records before queueing, outside the lock. Instrumented, settled, 8
    rounds per tree: the AddMany hold is 16.3-18.9ms on base and 14.1-21.7ms
    on this fix, and slow reserves per op are at or below base's (over
    150us: mean 81 against 104; over 1ms: 16 against 17). No code changed,
    so the four-benchmark A/B and the gates were not rerun.
    AddManyUnderReserveLoad/norc reserve-p99 (+10.29%) is left to the gate
    owner.
  - The settle step was accepted in review round 2. Its effect, this fix
    with and without it, 6 interleaved rounds: allocs/op -32% to -46% and
    B/op -62% to -75% on Kick, Resume and Suspend, and reserve-p50 up
    to about base's level on Kick (+21%) and Resume (+39%); the add
    benchmarks unchanged (results doc, "The settle step's effect").
  - Gates on the first round's code, each with a scratch `HOME` and every `OS_`
    variable unset, 20:58 to 21:25 GMT: `make lint` 0 issues; `make test`
    1276 passed; `WR_TEST_STORE=all make test` 1276 passed; `make race`
    1276 passed. The two new tests also passed 5 times under `-race` with
    the `TestLogHeld*` tests.

- [x] TestReadFrameInto (jobqueue/wal/scan_test.go) never checks that
  ReadFrameInto reuses its payload buffer. Removing `bufs.payload = payload`
  survives. Add an assertion that kills that mutant.
  - Source: incidental from phase 1 gate item 1g.4's review
  - Red: in a plain copy with `bufs.payload = payload` replaced by
    `_ = payload` (compiles), the extended test fails (exit 1):
    `go test -tags netgo --count 1 ./jobqueue/wal/ -run TestReadFrameInto`

    ```text
    Line 842:
    Expected '0' to be greater than or equal to '5' (but it wasn't)!
    --- FAIL: TestReadFrameInto (0.00s)
    ```

  - Approach: a new Convey block reads the plain frame into fresh
    `FrameBuffers` twice: after the first read `Size()` must hold at least
    the payload, and the second read must reuse that storage (same
    `Size()`). The mutant leaves `Size()` at 0. Passes on the real code.
  - Files: jobqueue/wal/scan_test.go.

## Review round 4: PASS

A fresh reviewer re-ran the earlier rounds' mutants and a new mutation pass
over the change queue: no surviving non-equivalent mutant, and no deadlock,
lock-order, ordering, durability, leak or panic defect. Non-blocking notes,
accepted with no change:

- `appendChange`'s `if err != nil` branch is unreachable today (a queued
  change is a single record, and `writeFramesCounted` errors only on the
  block path); it stays as defensive code.
- No change test starts a second drain on the same store, so the "`drained`
  not reset" mutant survives `store_log_change_test.go` alone; the wider
  suite kills it (`TestResumeStoredWithoutLaterWrite`, Line 477).
- The drain appending instead of failing once closed is equivalent: after
  `w.Close()` an append finishes with `ErrClosed`.

AddManyUnderReserveLoad reserve-p99 (+10% to +23% across four A/Bs) is left
to the gate owner; 1g.4 stays unticked until then.
