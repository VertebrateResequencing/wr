# Phase 1e: Reservations, config removal, store-switch preparation

Ref: [spec.md](../spec.md) sections C3, F1, F2, A5 (test 4 only)

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.
Implementors and reviewers follow `testing-principles` for test work, and
reviewers apply `go-test-strength` to the tests each item adds.

Brief every subagent with these project constraints:

- Run one subagent at a time. Items run sequentially; there are no
  parallel batches, because of the user's rate limits.
- Work only on branch `customdb-f397b547` in the main clone. Do not
  switch branches or create worktrees for this work; `go-test-strength`
  mutants go in a scratch copy as `agent-conduct` describes.
- Commit messages carry no attribution lines: no `Co-Authored-By` and no
  `Generated with` trailers.
- Kill only exact PIDs that the agent started and verified. Never use
  `pkill -f` or any pattern kill, and guard empty variables in cleanup.
- Run `make test` and `make race` with every `OS_*` variable unset and an
  empty scratch `HOME` (DEVELOPERS.md, "Fast test suite"), for example:

  ```bash
  (unset $(compgen -v | grep '^OS_'); HOME=$(mktemp -d -p <scratchpad>) make test)
  ```

- Put NFS work and measurements under `/nfs/hgi/wr/sb10-bigdb/customdb/`
  (`.docs/customdb/README.md`, "Where to run on NFS").
- This sub-phase is complete when every item is reviewed and `make test`,
  `make race` and `make lint` pass.
- Gates that need the LSF farm, daytime NFS runs or multi-hour soaks
  are not items here; they are in [phase1-gate.md](phase1-gate.md).

## Items

### Item 1e.1: C3 - Transitions and durable reservations

spec.md section: C3; A5

Make each transition append one state record and make
`persistReservation` and `handOutReservation` hand a job out only after
its reservation is durable, with the abandoned-reservation path and the
`"reservation wait"` histogram line (`jobqueue/serverCLI.go`,
`jobqueue/store_log.go`, `jobqueue/db.go`). Add `reserveRunState` to
both stores and remove `updateJobRunStateDurableWithin`,
`logReserveNotDurable`, `errDurableWriteWaitExpired`,
`ReserveWriteWait` and `setBatchTuning`. Add A5's replay mapping for
those removed methods to `replayRecording` (`jobqueue/store_record.go`)
and cover A5 test 4, deferred from item 0d.2 because it needs
`reserveRunState`, in `jobqueue/store_record_test.go` by replaying
`jobqueue/testdata/store_record/removed_methods.rec`, which 0d.2
committed. Delete 0d.2's `WR_STORE_RECORD_REGEN` test that wrote that
file, since it calls the removed methods. Cover all 11
acceptance tests from C3 in `jobqueue/store_reserve_test.go`.
Also resolve the 0b.1 incidental on the listed tests that hold the bbolt
write lock to open their crash window ([incidentals.md](incidentals.md)).
Make the `reserveDurableWait` bound also cover a reserve's wait in the
log store's `lockUnlessReviving` (1c.4), so a reserve of a key being
revived by a modify or put-back never waits longer than the bound.

- [x] implemented
- [x] reviewed

### Item 1e.2: F1 - Config removal

spec.md section: F1

Make the removed tuning knobs ignored, not fatal (`internal/config.go`,
`cmd/conf.go`, `cmd/manager.go`, `jobqueue/server.go`). Cover both
acceptance tests from F1 in `internal/config_test.go`.

- [x] implemented
- [x] reviewed

### Item 1e.3: F2 - Concurrent key-changing modifies

spec.md section: F2

Make two concurrent key-changing `wr mod` calls persist both jobs
(`jobqueue/server.go`, `jobqueue/serverCLI.go`, `jobqueue/serverREST.go`,
delivery-queue row 15). Cover the 1 acceptance test from F2 in
`jobqueue/store_modify_race_test.go`.

- [x] implemented
- [x] reviewed

### Item 1e.4: Incidentals before the store switch

spec.md section: C3; C4; C5; The store interface

Resolve the incidentals routed here ([incidentals.md](incidentals.md)):
the 1c.4 incidental on the server-only clauses of C4 tests 9 to 11; the
1e.1 incidentals on `TestWriterReopensAfterAbandon` seeing a reopen
try's segment, on asserting a literal reserve bound and on an abandon
keeping the earlier attempt's details; the 1e.2 incidental on the
source walk skipping dot-directories; and the 1e.3 incidental on
`TestStoreModifyRace` asserting that keys changed. Switch C5 test 3
(`TestLogStoreRecoverDamage`, 1c.5) from `openLogStoreLocked` to
`openStore`, as the spec names it.

The default-store switch first planned here moved to item 1f.11: it
needs the log store's dev wipe, conversion and backup from 1f, and
`make test` fails without them ([blocker-phase1e-1e.4.md](blocker-phase1e-1e.4.md)).

- [x] implemented
- [x] reviewed

### Item 1e.5: `WR_TEST_STORE=all` runs the log store

spec.md section: A5; C5; The store interface

Make `testStoreKinds` (`jobqueue/store_inspect_test.go`) map `all` to
both stores and accept `log`, so every `forEachStore` test runs on the
log store. Fix the log-store failures this exposes that do not need
backup: `TestStoreCrash` ("wal file 00000002 does not start with a
header"), the replay mismatches in `TestStoreRecordReplay`,
`TestStoreReplayChangeHandles` and `TestStoreReplayLearned`, and
`TestStoreCheckIfComplete` (a malformed key returns an error, not
false). Find each root cause; fix the store, or the test where the
test is wrong. `TestStoreInspector`, `TestStoreOpenLock`,
`TestStoreOpenLocked` and `TestStoreCorruptRestoredFromBackup` need
backup: skip them on the log store with a note naming 1f.3 and 1f.5,
which un-skip them. Show `make test` and `make race` pass with
`WR_TEST_STORE=all`. Depends on 1e.4.
Also resolve the two 1e.4 review round 2 incidentals (the walk's
`fs.ErrNotExist` flake; a stale comment and `Reset` order)
([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed
