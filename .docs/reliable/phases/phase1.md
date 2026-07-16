# Phase 1: Idea 3 - Maintained persisted per-repgroup COMPLETE counter

Ref: [spec.md](../spec.md) sections A1, A2, A3, A4, A5

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout. Ground truth for every counter assertion is the RAW scan
`retrieveCompleteJobCountsByRepGroups` (db.go:902); the maintained counter
MUST equal it by construction. This phase is independently mergeable and must
keep the Section E regression guards green (re-run them after the phase).

Items in this phase are sequential: they cluster in `jobqueue/db.go` and
`jobqueue/server.go`, A2 depends on A1's counter, A3/A4 share the per-repgroup
"SET counter[rg] = raw scan in the same tx" primitive, and A5 uses A4's
`recomputeRepGroupCompleteCounts` as its drift check.

## Items

### Item 1.1: A1 - Counter buckets and the four maintenance hooks

spec.md section: A1

Implement the persisted counter and its runtime maintenance in
`jobqueue/db.go`, tests in `jobqueue/repgroup_counter_test.go`:

- Create both buckets via `CreateBucketIfNotExists` in `initDB` (db.go:515+):
  `bucketRepGroupComplete = []byte("repGroupCompleteCount")` (repGroup ->
  8-byte big-endian uint64) and
  `bucketRepGroupBackfilled = []byte("repGroupCompleteBackfilled")` (repGroup
  -> nil marker; sentinel key "" -> nil = fully backfilled).
- `adjustRepGroupComplete(tx *bolt.Tx, repGroup string, delta int) error`:
  read-modify-write of `bucketRepGroupComplete[repGroup]`, clamped >= 0; if the
  clamp fires (computed value < 0) log the anomaly (points admin at A4
  recompute).
- `splitRTKKey(rtkKey []byte) (repGroup string, jobKey []byte, ok bool)`: split
  at the LAST `dbDelimiter` ("_::_", db.go:63).
- `retrieveMaintainedCompleteCounts(repGroups []string) (map[string]int, error)`
  O(len(repGroups)) point reads of `bucketRepGroupComplete`; 0 for absent
  (consumed by A2 seeding; A1 tests assert against it).
- The four tx-scoped hooks (all read-modify-write inside the existing bolt tx,
  no in-memory mirror, so a `db.bolt.Batch` re-split cannot double-apply):
  1. RTK entry created for (R,K), not pre-existing AND K in complete ->
     `counter[R]++`. Site `putLookups` (db.go:2705, `Put` db.go:2708) gated on
     `bytes.Equal(bucket, bucketRTK)`, pre-existence `lookup.Get(doublet[0]) ==
     nil`. Covers add (`storeNewJobData` db.go:1203 -> `storeLookups`
     db.go:2695) and modify (`modifyLiveJobsTx` db.go:2264 -> `putAllLookups`
     db.go:2377).
  2. RTK entry deleted for (R,K), K in complete -> `counter[R]--`. Site
     `deleteLookupEntriesForJobKey` (db.go:2402, `Delete` db.go:2416) for each
     collected delete whose `d.bucket` equals `bucketRTK` (verified sole runtime
     RTK-deletion site).
  3. Key added to complete bucket, not already present -> for every R with (R,K)
     in RTK, `counter[R]++`. Site `archiveJobTx` (db.go:807, complete `Put`
     db.go:818); capture `wasComplete := tx.Bucket(bucketJobsComplete).Get(key)
     != nil` BEFORE the Put, increment only when `!wasComplete`; enumerate Rs
     via `persistedRepGroupsForJobKey` (persistedstatus.go:38).
  4. Key deleted from complete: never happens (append-only) -> no hook.

Covers all 8 acceptance tests from A1 (each asserts the maintained counter ==
RAW scan after the step, including the cross-repgroup, key-changing-modify,
idempotent re-archive, pre-existence, remove-does-not-delete-RTK, and mixed
>= 3-repgroup churn cases).

- [x] implemented
- [x] reviewed

### Item 1.2: A2 - Seeding reads the counter, not the scan

spec.md section: A2

In `seedStatusStateForItemDefs` (server.go:921) replace the server.go:936 call
`s.db.retrieveCompleteJobCountsByRepGroups(repGroups)` with
`s.db.retrieveMaintainedCompleteCounts(repGroups)`. Everything else in seeding
is unchanged: `seedRepGroupComplete` (statusstate.go:113), `statusSeedMutex`,
the `unseededStatusRepGroups` filter. No change to `wr status` / CLI transport.
Must land together with A1 (both live in Phase 1, so they merge atomically).

Covers all 3 acceptance tests from A2 (sentinel-differs-from-scan proves the
counter is read; restart seeds N from the counter; Regression D
`TestReliableCompletedRepGroupRemovedOnRefresh` stays green).

- [x] implemented
- [x] reviewed

### Item 1.3: A3 - One-time online background backfill

spec.md section: A3

Implement `backfillRepGroupCompleteCounts(ctx context.Context) error` in
`jobqueue/db.go`, tests in `jobqueue/repgroup_counter_test.go`. For each
repGroup from `retrieveRepGroups()` (db.go:1584) lacking a marker, in ONE
`bolt.Update` tx SET `counter[rg] = rawCompleteJobCountByRepGroup(rg)` computed
in that same tx, then Put its marker; set the sentinel key "" when all done.
SET (not additive) reconciles with concurrent runtime increments (bolt
serialises write txs). Idempotent, crash-resumable; new DB (no repgroups) is a
no-op.

Wire `Serve()` to launch `backfillRepGroupCompleteCounts` in a background
goroutine after readiness (composes with Idea 2; the "responsive immediately +
background backfill" integration is proven in Phase 2 Item 2.4).

Covers all 3 acceptance tests from A3 (pre-upgrade DB backfills to RAW scan with
markers; interrupted-then-rerun processes only unmarked repgroups; concurrent
archives into a repgroup reconcile to RAW scan under `-race`).

- [x] implemented
- [x] reviewed

### Item 1.4: A4 - Offline recompute/repair subcommand

spec.md section: A4

Implement `recomputeRepGroupCompleteCounts(ctx context.Context) (drift int, err
error)` in `jobqueue/db.go`: for every repGroup SET `counter[rg] = raw scan`
(+ marker); returns the number of repGroups whose stored value differed from the
raw scan (drift), for logging; idempotent. May reuse the per-repgroup
SET-in-same-tx primitive introduced by A3.

Add `wr manager recompute-counts` in `cmd/manager.go`, wired like
`managerBackupCmd` (cmd/manager.go:526): refuses to run if the manager is up
(pid file / port check), opens the DB directly with the map-freelist option
(`bolt.FreelistMapType`; a direct open in cmd, independent of Phase 4 D1's
initDB change), calls `recomputeRepGroupCompleteCounts`, logs the drift, closes.

Tests in `jobqueue/repgroup_counter_test.go`. Covers all 3 acceptance tests from
A4 (correct counters -> drift 0 no-op; corrupted counters -> all repaired to RAW
scan with drift == number corrupted; subcommand refuses while a manager runs and
does not modify the db).

- [x] implemented
- [x] reviewed

### Item 1.5: A5 - Crash consistency

spec.md section: A5

Tests only, in `jobqueue/repgroup_counter_test.go`; depends on A4's
`recomputeRepGroupCompleteCounts` for the drift assertion. Covers both A5
acceptance tests: (1) after an archive churn via `archiveJob`, reopen the db
without a clean counter shutdown -> every counter == RAW scan and recompute
reports drift == 0; (2) the suite's in-process crash-recovery path hard-stops
mid-completion and restarts with no completion double-counted or lost (counter
== RAW scan). Note in the test that the true out-of-process hard-crash exemplar
(a `--servermode` server SIGKILLed and restarted with `--keepdb`) lives in
`TestJobqueueSignal`, not here.

- [x] implemented
- [x] reviewed

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
