# Phase 4: Fix 1d - Map freelist and offline compaction

Ref: [spec.md](../spec.md) sections D1, D2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout. This phase is independent of Phases 1, 2 and 3; it may run in
parallel with or after them. It is independently mergeable and must keep the
Section E regression guards green (re-run them after the phase).

D1 and D2 are distinct concerns (an `initDB` open option vs. a new offline
compaction subcommand) with no shared logic, so they form a parallel batch. Both
touch `jobqueue/db.go` (different functions) and `jobqueue/db_test.go`
(different test functions), and both use the same map-freelist open option
(`bolt.FreelistMapType`); coordinate those shared-file edits.

## Items

### Batch 1 (parallel)

#### Item 4.1: D1 - Open BoltDB with the map freelist [parallel with 4.2]

spec.md section: D1

In `jobqueue/db.go`, pass `&bolt.Options{FreelistType: bolt.FreelistMapType}`
(preserving any existing option fields) to all five production `bolt.Open` calls
in `initDB`: db.go:441, 449, 455, 480, 500. No startup or online compaction.
Validation (record, not assert): measure `initDB` open time with vs without the
map freelist against a copy of the real `.tmp/db` (benefit shows only at real
fragmentation).

Tests in `jobqueue/db_test.go`. Covers both D1 acceptance tests (a fresh db
opened by `initDB` and an existing db reopened both open without error and
existing db tests pass; a written-then-reopened db round-trips stored and
archived jobs identically - the option only affects freelist representation).

- [ ] implemented
- [ ] reviewed

#### Item 4.2: D2 - Offline compaction subcommand [parallel with 4.1]

spec.md section: D2

Add `wr manager compact` in `cmd/manager.go`, wired like `managerBackupCmd`
(cmd/manager.go:526). It refuses to run if the manager is up (pid/port check),
compacts the DB file to a temp file via `bolt.Compact` (source opened with the
map freelist), atomically replaces the original, and reports before/after sizes.

Tests in `jobqueue/db_test.go`. Covers both D2 acceptance tests (a stopped
churned db compacts to a valid db whose buckets and every job/lookup/counter
round-trip identically and whose output size is <= input; compaction invoked
while a manager runs exits non-zero and leaves the db untouched).

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill (review all items in the
batch together in a single review pass).

## Regression guards (Section E)

Re-run after this phase; all must stay green (spec.md Section E):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner`,
  `TestLostDetectionRecentContactNotLost`.
- `TestReliableFalseLostRerun`, `TestReliableCompletedRepGroupRemovedOnRefresh`
  (reliable harness dropped into `jobqueue/`;
  `go test -run TestReliable ./jobqueue`).
- `TestReliableFalseLostUnderSaturation` (`everLost == 0`; run from
  `.docs/reliable/harness/`, not committed).
- `make test`, `make race`, `make lint` all clean.
