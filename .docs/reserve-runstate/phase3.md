# Phase 3: Switch the writes and overlay

Ref: [spec.md](spec.md) sections B1, B2, A3, A2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

The items run in sequence, all in `jobqueue/db.go` and its tests. The
overlay (B1) comes first because A3's and A2's tests read recovered jobs
through it. A2 must not land without B1: without the overlay a crash would
recover pre-reservation state. The phase is done only when item 3.4 is
reviewed, including the A2 audit in the PR body.

## Items

### Item 3.1: B1 - Overlay at recovery

spec.md section: B1

Make `recoverIncompleteJobs` return `([]*Job, runStateRecovery, error)`,
walking `bucketJobRunState` alongside `bucketJobsLive` (both sorted by key,
a missing bucket counting as empty) and applying each matching record with
`applyTo`. A matching record whose body fails to decode is treated as stale
and `decodePriorJobs` warns `recovering: undecodable job run-state record`
with its key. Add `runStates=<applied>` to `decodePriorJobs`'
`recovering: decoded live jobs` line. Update every caller listed in B1,
including the `reliability_repro`-tagged `dbstart_probe_test.go` (check
with `go vet -tags netgo,reliability_repro ./jobqueue/`).

Covering B1 acceptance test 1 in `jobqueue/db_runstate_test.go`. B1 test 2
needs a reservation to write a run-state record, so it is implemented and
reviewed with item 3.4; together the two items cover both B1 tests.

- [ ] implemented
- [ ] reviewed

### Item 3.2: B2 - Stale and orphaned records are dropped

spec.md section: B2

After its read, `recoverIncompleteJobs` deletes stale and orphaned
run-state records in one write transaction, re-checking each against the
live bucket inside it, with a test hook between the read and the write
transaction. Skip the delete on a read-only bolt handle and return
`dropped` 0. `decodePriorJobs` logs
`recovering: dropped stale job run-state records` with the count when it is
above 0. Covering all 4 acceptance tests from B2, in
`jobqueue/db_runstate_test.go`. Depends on item 3.1.

- [ ] implemented
- [ ] reviewed

### Item 3.3: A3 - Drain ordering

spec.md section: A3

Implement the best-effort writer changes under "Best-effort writer" in
spec.md's Architecture: `beChange`'s `runState`/`rsSeq` slot,
`enqueueChangeLocked` clearing it, `enqueueRunStateLocked`,
`queueJobRunState`, `updateJobRunStateDurable`,
`updateJobRunStateDurableWithin`, and `beBatch.apply`'s three steps (drain
step 1 still dereferencing the live bucket). Add the test helper
`queueUnkickedBestEffortRunState`. Extend
`TestBestEffortDrainKeepsArrivalOrder` and
`TestBestEffortChangeKeepsEncodeOrder`, keeping the existing full-change
cases. Covering all 14 acceptance tests from A3. Depends on items 3.1 and
3.2.

Review note on test files: A3 names `jobqueue/reserve_durability_test.go`,
which holds `TestBestEffortDrainKeepsArrivalOrder`,
`TestBestEffortChangeKeepsEncodeOrder` and the `queueUnkicked*` helpers,
but `TestStartDurabilityAbortedWriteIsNotCommitted` (test 11) lives in
`jobqueue/start_durability_test.go`. Extend each test where it already
lives; do not move it.

- [ ] implemented
- [ ] reviewed

### Item 3.4: A2 - Reserve and start write run-state records

spec.md section: A2

Switch `persistReservation` (`jobqueue/serverCLI.go`) to
`updateJobRunStateDurableWithin(job, ReserveWriteWait)` and `handleStart`,
including an accepted duplicate start, to
`updateJobRunStateDurable(job)`, with error handling, warnings and replies
unchanged. Change the existing test helpers listed under "Live-record
helpers" in spec.md's Architecture to read or carry the run-state record.
Add `BenchmarkUpdateJobRunState10KB` and `BenchmarkUpdateJobFull10KB` to
`jobqueue/db_bench_test.go`, leaving `BenchmarkUpdateJobState` unchanged.
Do the audit A2 describes and write it in the PR body: every persisted
`Job` field changed in memory without its own durable write before a
reservation or start, and every field the `recover*` functions change in
memory, each with how it is persisted. Covering all 10 acceptance tests
from A2, plus B1 acceptance test 2 (deferred from item 3.1). Depends on
items 3.1 to 3.3.

Review notes:

- Test 1's byte comparison of `db.encode` outputs: the binc handle is not
  canonical, so a Go map with more than one key (for example
  `Requirements.Other`) may encode in a different order each time. Keep the
  test job's maps to at most one key each, or confirm the encoding is
  deterministic for the job used, so the strict comparison stays strict and
  does not flake. Do not weaken it to a decoded comparison.
- Test 6: `storedLiveJobState` and `storedLiveJob` are also used by
  `jobqueue/release_after_lost_test.go` and
  `jobqueue/running_dependent_archive_test.go`. Those tests must pass after
  the helper change too.
- Test 10: `TestManagerQueueDefaultsAreNotStored`
  (`cmd/manager_queue_db_test.go`) is vacuous if no `jobRunState` record
  exists when it checks. Reserve a live job (one that stays live) before
  the manager's `Stop`, so a record exists, and assert at least one
  `jobRunState` record was checked before asserting none holds the queue or
  avoid key in `Requirements.Other`.
- B1 test 2: as in `TestReserveDurability`, kill the server without a
  clean stop after the reservation and before `Started`, then check the
  new server's `runStates=1` log line and that the job is in the run
  sub-queue reserved by the original client.

- [ ] implemented
- [ ] reviewed
