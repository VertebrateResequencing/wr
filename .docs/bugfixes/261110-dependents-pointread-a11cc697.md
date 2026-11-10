# Bugfix: quadratic dependent reads on the log store's add path

- Branch: customdb-f397b547
- Base: customdb-f397b547 at 27c10a45f55115aa7c8fb1cbff5b3fba4a609a9a
- Queue owner: customdb-f397b547, .docs/delivery-queue.md row 20
  (phase 1 gate item 1g.4, .docs/customdb/phases/phase1-gate.md)

## Items

- [x] 1g.4 (make speed-full against d1a4b6d7) fails: an add to a dep group
  that many queued jobs wait on is quadratic in the number of waiters on the
  log store. BenchmarkAddDepGroupMember (30,000 waiters): base 361ms per
  add, HEAD did not finish one add in 13m23s. dep-granularity-check add-sec
  0.5675s to 4.0905s (+620.79%, p=0.002) and peak RSS +18.38%. Root cause
  (from .docs/customdb/results/phase1-gate-1g4.md): planDependents ->
  dependentJobs -> dependentJob -> liveJobOf -> decodeJob reads each
  dependent's spec and state record separately through readRecord ->
  wal.ReadAt, which decompresses and checksums the whole compressed block
  for each record; one add's waiters share one block, so the cost is about
  2 x dependents x block size.
  - Source: phase 1 gate item 1g.4
  - Red command (exit 1 before the fix, 0.21s):
    `(unset $(compgen -v | grep '^OS_'); HOME=<scratch> CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestLogStoreDependentsReadCost)`

    ```text
    Line 58:
    Expected '1000' to be less than or equal to '22' (but it wasn't)!
    --- FAIL: TestLogStoreDependentsReadCost (0.20s)
    ```

    This excerpt is from a rerun after review round 1: the final test, alone
    in its file, against `27c10a45`'s code (exit 1). The first red run had
    a looser slack, so its bound read '28'. The test stores 10, then 500,
    waiters on a dep group in one add each, and
    counts record file reads (`logSegmentFileHook`) during a one-member add:
    20 and 1,000 before the fix, two per waiter; 1 and 1 after it.
  - The same per-record read of a block serves every history query's overlay
    entries (`historyRead.job` and `archivedBytes`): a query of a rep group
    whose recent completions were added in one big add decompresses that
    add's block once per entry, for up to `checkpointInterval` (5 minutes),
    until a checkpoint moves the specs. Its red check, run with the fixed
    test file against `27c10a45`'s code (exit 1, 2.1s):
    `go test -tags netgo --count 1 ./jobqueue -run TestLogStoreOverlayReadCost`

    ```text
    Line 150:
    Expected: 201
    Actual:   400
    (Should equal)!
    --- FAIL: TestLogStoreOverlayReadCost (2.12s)
    ```

    200 jobs stored in one add, archived one by one and held in the overlay:
    a rep group query and pricing its archived bytes each read 400 frames,
    two per job, where 201 frames hold the records.
  - Approach (not committed; the fix rounds below follow review): read many
    records by frame instead of each through its Ref.
    - `dependentJobs`' per-group scan (`logDependentsScan.group`) takes the
      Refs of all its live dependents under one `holdRefs` hold
      (`liveRefsOf`), then `liveJobsOf` reads them through `readJobBodies`:
      it sorts the records by Ref and reads each frame once
      (`readFrameJobBodies`, `takeJobBodies`), copying each body once, and
      decodes each job with `jobOf`. This replaces `liveJobOf` and
      `dependentJob`. Its archived dependents are read in one history read
      (`historyJobs`, which `historyJob` now calls).
    - A history read (`historyRead`) reads an overlay entry's spec and
      complete records through `overlayRecord`: the first time it needs a
      frame, it reads it once for every overlay entry's record in it, and
      keeps those records for the rest of the read, still under the read's
      hold. `historyRead.job` (`overlayJob`) and `archivedBytes` use it;
      `decodeJob` is gone.
    - The fold's `readRecordBodies` takes any sequence of Refs, and shares
      `frameIdxs` and `readFrameRecords` with `overlayRecord`.
  - Files: `jobqueue/store_log.go`, `jobqueue/store_fold.go`,
    `jobqueue/store_history_bolt.go`, `jobqueue/store_log_frameread_test.go`
    (new: `TestLogStoreDependentsReadCost`, `TestLogStoreOverlayReadCost`,
    `TestLogStoreRecordBodyType`, `TestLogStoreHistoryJobBadRecord`),
    `jobqueue/store_log_add_test.go` (two new `TestLogStoreAdd` cases: a
    waiter on two dep groups is found once; a complete dependent re-added
    with a new member is stored once, not also put back), and test helpers
    in `jobqueue/store_checkpoint_test.go`,
    `jobqueue/store_history_bolt_test.go` and
    `jobqueue/store_log_inspect_test.go`.
  - Mutants (go-test-strength, scratch copy, suite
    `-run 'Dep|LogStore|Checkpoint|Fold|History|Archived'`): 25 valid, 18
    killed at first; the 4 real survivors (no done marking, no newKeys
    check, no record type check, a swallowed history read error) each now
    fail a test added for them. 3 survivors are equivalent: not removing a
    read frame from `unread`, keeping records outside the wanted indices,
    and reading live keys' history entries too; each only costs memory or
    reads, never changes a result. Two kills are by suite timeout only (a
    live dependent taken as archived).
  - Speed A/B and gates: see the Fix section of
    `.docs/customdb/results/phase1-gate-1g4.md`. `AddDepGroupMember` B/op
    is +63% against the bbolt base, which `speed.sh` flags; time and peak
    RSS are better than base. Open for the gate owner.
  - Fix round 2 (B/op): `wal.ReadFrame` pools its read and decompression
    buffers (`jobqueue/wal/scan.go`), and `liveJobsOf` reads sorted
    `jobRecord`s frame by frame, copying each body once through `keyedBody`
    with no maps (`readJobBodies`, `readFrameJobBodies`, `takeJobBodies`);
    `recordBody` is gone. New test `TestLogStoreDependentsAcrossFrames`
    fails if a body is kept uncopied from the pooled buffer. A/B 2:
    sec/op -18.27%, add-sec -38.24%, peak RSS -19.04%, but B/op still
    +38.90% against bbolt, about 26MB per add of it the background fold of
    the benchmark's setup at b.N=3, and about 15MB the decompressed block.
    Not within 10% without a benchmark change or a retained store buffer;
    reported to the gate owner (see the 1g.4 results, Fix round 2).
  - Round 2 mutants (same suite): 8 valid, 6 killed. Equivalent: skipping
    a wanted index lower than the record's (unreachable, as a frame's
    indices run from 0 without gaps), and not keeping the pooled payload
    buffer (costs allocations only). Gates on the final code: `make lint`
    0 issues; `make test` 1271 passed (an earlier run failed
    `TestBoltOnlyTests` because a new test used `logKeyOf` from a bolt-only
    test file; fixed with a local `testLogKey`); `WR_TEST_STORE=all make
    test` 1271 passed; `make race` 1271 passed.
  - Fix round 3 (B/op within the gate): `wal.ReadFrameInto` with
    caller-owned `wal.FrameBuffers`; the log store keeps one set in a
    single slot (`takeReadBufs`/`putReadBufs`, dropped past `readBufsMax`,
    64MiB), used by `readFrameJobBodies` and `readFrameRecords`, with a
    fresh set for a reader that finds the slot taken.
    `BenchmarkAddDepGroupMember` waits for the log store's fold of its setup
    before timing (`settleStore`, a no-op on bbolt). New tests:
    `TestLogStoreConcurrentFrameReads` (`-race`) and
    `TestLogStoreReadBufsBound`. Guard mutants with `-race`: the slot not
    emptied on take, no lock on take, and the buffers given back before the
    read (in either reader) are killed; dropping the cap is killed by the
    bound test; overwriting an idle kept set is equivalent. A/B 3 at the
    gate's settings: PASS, B/op +8.54%, sec/op -26.87%, add-sec -36.39%,
    peak RSS -16.69% (see the 1g.4 results, Fix round 3).
  - Round 3 gates on the final code (scratch HOME, `OS_` unset): `make
    lint` 0 issues; `make test` 1273 passed; `WR_TEST_STORE=all make test`
    1273 passed; `make race` 1273 passed. A first run failed
    `TestBoltOnlyTests` because a comment in `dep_group_add_bench_test.go`
    said "history.bolt.", which matches its bbolt-reach pattern; the comment
    was reworded.
  - Fix round 4 (review round 1 FAIL): `TestLogStoreReadBufsBound` pins the
    cap at the measured size (mutants `>= 2*readBufsMax`, `>=` and `+1`
    killed); docs corrected; `jobqueue/wal` `TestReadFrameInto` added;
    `wal.ReadFrame`'s uncapped `sync.Pool` dropped. The pool now served only
    recovery and checkpoint reads, which before the fix read into new
    buffers. Dropping it restores that, and the A/B shows recovery-sec
    unchanged (~, p=0.18). A cap would have kept a second, global buffer
    cache beside the store's. `overlayRecord`'s comment notes it caches every
    overlay record of each frame it reads for the life of the read. A/B 4:
    AddDepGroupMember B/op +8.49%, report-storm and dep-granularity within
    10% or better. `speed.sh` flags reserve latency in the
    `*UnderReserveLoad` benchmarks, and a 27c10a45-vs-fix A/B shows the same
    figures before the fix: pre-existing, to be routed as a new bugfix item.
    Round 4: `make lint` 0 issues; `make test`, the all-store `make test`
    and `make race` each 1,274 passed.
  - Review round 2: PASS. Incidental: no test pins `wal.ReadFrameInto`
    reusing its payload buffer (removing `bufs.payload = payload` survives;
    cost is allocations only). Owner: customdb delivery queue row 20, with
    the reserve-latency bugfix item.
