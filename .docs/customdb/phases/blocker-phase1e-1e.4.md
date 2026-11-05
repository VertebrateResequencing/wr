# Blocker: phase 1e item 1e.4 (default-store switch)

## The item

Item 1e.4 was to make `storeKindLog` the default `storeKind` and `log` the
`WR_TEST_STORE` default, with `make test` and `make race` passing on it
and with `WR_TEST_STORE=all`.

## Why it cannot be done in 1e

The 1e.4 implementor (2026-11-05) made the switch, measured it and
reverted it; the patch is
[blocker-phase1e-1e.4-switch.patch](blocker-phase1e-1e.4-switch.patch).

- `make test` fails in about 25 tests across `jobqueue`, `cmd` and
  `developers/soak/dbstart` (the run stops early, so the list is not
  complete). Causes: the log store has no dev wipe (1f.1), so state
  carries between Convey blocks; it does no backup and returns
  `errLogStoreBackup` (1f.3, 1f.5); opening a path that holds a bbolt
  file fails with "VERSION: not a directory" (conversion, 1f.2);
  `boltDB()` panics (the 0b.1 incidental); and two tests outside
  `jobqueue` read bbolt directly and cannot pin the unexported store
  kind (`cmd/manager_queue_db_test.go` `readStoredJobs`;
  `developers/soak/dbstart` `TestRunReadsRealReservation`, F3, 1f.8).
- With a prototype dev wipe in a scratch copy, 8 server tests still fail
  on the log store, among them `TestJobqueueRunnerScheduling`,
  `TestJobqueueRunnerResourceLearning`, `TestJobqueueBasics` (cannot reach
  the server after a restart), `TestJobqueueModify` and
  `TestJobqueueExecutionAndDependencyScenarios`. Each needs a root cause.
- `testStoreKinds` mapped `WR_TEST_STORE=all` to bbolt only, so no
  `forEachStore` test has run on the log store. With that fixed, 9 fail
  on it: `TestStoreCrash`, `TestStoreRecordReplay`,
  `TestStoreReplayChangeHandles`, `TestStoreReplayLearned`,
  `TestStoreCheckIfComplete`, and four that need backup
  (`TestStoreInspector`, `TestStoreOpenLock`, `TestStoreOpenLocked`,
  `TestStoreCorruptRestoredFromBackup`).

## Resolution (orchestrator, 2026-11-05)

- 1e.4 keeps the incidentals it resolved; the switch moves to the new
  item 1f.11, after 1f.1 to 1f.10.
- The new item 1e.5 makes `WR_TEST_STORE=all` run the log store now and
  fixes the five non-backup `forEachStore` failures, because they may be
  log-store defects that 1f builds on.
- Phases 1f and later do not depend on the switch, so they continue.
