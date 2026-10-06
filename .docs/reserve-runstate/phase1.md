# Phase 1: Schema

Ref: [spec.md](spec.md) sections C1, C2, C3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: C1 - Open refuses unsupported databases and stamps version 2

spec.md section: C1

In `jobqueue/db_schema.go`, add `dbSchemaVersionRunState`,
`currentDBSchemaVersion`, `errDBSchemaTooNew`, `errDBNeedsCompact`,
`checkDBSchemaVersion`, `readOnlyDBFileSchemaVersion` and
`bucketJobRunState`, as given under "Schema version" and "Run-state record"
in spec.md's Architecture. In `initDB` (`jobqueue/db.go`), read and check
the version before any write transaction, refuse an existing version-0 file
with a `jobslive` bucket, and create `bucketJobRunState` and stamp
`currentDBSchemaVersion` in the bucket-creation `Update`. In
`jobqueue/reliable4_backup_repro_test.go`, make `createReliable4Buckets`
stamp `currentDBSchemaVersion` and add `bucketJobRunState` to
`reliable4AllBuckets`. Covering all 10 acceptance tests from C1, in
`jobqueue/db_schema_test.go` (test 10 in
`jobqueue/reliable4_backup_repro_test.go` as
`TestReliable4InflateDBOpens`, `reliability_repro` tag).

Review note for test 8: re-running the existing tests is not enough; these
need code changes:

- `populateCompactStdDB(..., unversioned=true)` (`db_compact_std_test.go`,
  about lines 299-305) currently reopens the unversioned file with `initDB`,
  which C1 refuses. Change it so it populates through a stamped `initDB`
  handle and calls `unstampDB` only after that handle's last close, never
  opening the unversioned file with `initDB`, as test 8 states.
- `reliable2_dbcompat_test.go` does not compact today. Its uses of
  `copyFixtureToTempDB` (about lines 93, 179 and 226) must compact the copy
  before starting a server on it.
- Grep the tests for every other caller that builds or copies an
  unversioned database with a `jobslive` bucket (`db.golden`,
  `copyFixtureToTempDB`, `unstampDB`, raw `bolt.Open` fixtures) and stamp or
  compact it first; add `compactedFixtureCopy` for
  `reliable4_recovery_log_test.go`'s `recoveryLogFixtureConfig`.

- [ ] implemented
- [ ] reviewed

### Item 1.2: C2 - Compact stamps the current version

spec.md section: C2

In `CompactDBFileStats` (`jobqueue/db_compact.go`), after the `os.Stat`,
call `readOnlyDBFileSchemaVersion` and `checkDBSchemaVersion` and return any
error before `compactToTempFile` creates a temp file. Make `compactBolt`
stamp `currentDBSchemaVersion` in the destination at version 1 or above,
and make `compactStrippingStd`'s `copyAll` stamp `currentDBSchemaVersion`.
Covering all 5 acceptance tests from C2, in
`jobqueue/db_compact_std_test.go`. Depends on item 1.1's constants, bucket
and read-only version reader.

- [ ] implemented
- [ ] reviewed

### Item 1.3: C3 - The user sees the refusals

spec.md section: C3

Add `TestManagerStartHelperProcess`, modelled on
`TestManagerStopHelperProcess`, and the three tests
`TestManagerStartRefusesUnsupportedDB`,
`TestManagerStartDaemonShowsRefusal` and `TestManagerCompactRefusesNewerDB`
in `cmd/manager_test.go`. Change `cmd/manager.go` only if a test fails.
Covering all 3 acceptance tests from C3. Depends on items 1.1 (start
refusal) and 1.2 (compact refusal).

- [ ] implemented
- [ ] reviewed
