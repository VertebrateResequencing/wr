# Phase 3: Remove recompute-counts and the remaining DB counter machinery (E)

Ref: [spec.md](../spec.md) section E1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout. This phase removes the accepted user-facing `wr manager
recompute-counts` subcommand and the remaining `db.go` per-RepGroup complete-
counter machinery it backed. Depends on Phase 2: once
`seedStatusStateForItemDefs` and `startCounterBackfill` are gone, the maintained
counter has no live consumer, so the recompute path and the runtime write-side
can be deleted without breaking any caller. This phase is a single sequential
item (`cmd/manager.go` + `jobqueue/db.go`).

## Items

### Item 3.1: E1 - Subcommand and backing function deleted

spec.md section: E1

Delete the subcommand in `cmd/manager.go`:

- `managerRecomputeCountsCmd` (cmd/manager.go:572), `managerRecomputeExit`
  (cmd/manager.go:565), the `recomputeCounts` var (cmd/manager.go:570), and the
  `managerCmd.AddCommand(managerRecomputeCountsCmd)` (cmd/manager.go:1198).
- Delete the recompute-counts CLI test(s) if present (they pin the removed
  command).

Delete the remaining per-RepGroup counter machinery in `jobqueue/db.go` (the
"remaining DB counter machinery" not already removed in Phase 2; spec
Architecture DELETIONS bullet):

- `RecomputeRepGroupCompleteCounts` (db.go:1290, the exported entry point),
  `recomputeRepGroupCompleteCounts` (db.go:1204), `recomputeRepGroupComplete`
  (db.go:1233), `ensureRecomputeBuckets`.
- The runtime write-side `adjustRepGroupComplete` and
  `adjustRepGroupCompleteForRTKKey` (db.go:1649, 1621) and their call sites
  (archive db.go:897, put-lookups db.go:1279, delete-lookups db.go:1605).
- Any read/backfill helpers not already removed in Phase 2:
  `retrieveMaintainedCompleteCounts` (db.go:1022),
  `backfillRepGroupCompleteCounts` (db.go:1067),
  `setRepGroupCompleteFromRawScan` (db.go:1128), `fullyBackfilled`,
  `markBackfillSentinel`.
- The buckets `bucketRepGroupComplete` ("repGroupCompleteCount", db.go:110),
  `bucketRepGroupBackfilled` ("repGroupCompleteBackfilled", db.go:115),
  `backfillSentinelKey`, and the two `CreateBucketIfNotExists` calls for those
  buckets (db.go:660, 665).

Implementor note: after `retrieveMaintainedCompleteCounts` is removed, the KEEP
compaction test `TestDBCompactRoundTrip` (`jobqueue/db_test.go`) still references
it and will no longer compile. EDIT that test to drop only its maintained-counter
assertions - do NOT delete the whole test; its bucket/job/lookup round-trip
coverage must survive.

DB-compatibility (spec section F): removing the `CreateBucketIfNotExists` calls
means a fresh DB no longer creates these buckets, and an already-upgraded DB
keeps them as harmless dead data - do NOT `DeleteBucket` them and do NOT add a
schema-version gate. This leaves the reworked build able to open a
current-code-upgraded DB (proven in Phase 4).

Covers both E1 acceptance tests: (1) `wr manager recompute-counts` is an unknown
subcommand (cobra error / non-zero exit) - the command no longer exists; (2)
`RecomputeRepGroupCompleteCounts` is not a symbol in the `jobqueue` package
(compile-time; the deletion is complete).

- [x] implemented
- [x] reviewed

## Regression guards (KEEP surfaces, section H)

Re-run after this phase; all must stay green (spec.md section H1, plus
`TestLostDetectionSilentRunner` per section B):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner` (KEEP).
- `jobqueue/subscription_test.go` (`#503`), `jobqueue/live_jtouch_test.go`
  (`#530`/`#534`, incl. ssh-to-host), the `JobUpdateResync` reconnect/resync
  tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, and the
  `wr add --sync` client test.
- `make test`, `make race`, `make lint` all clean.
