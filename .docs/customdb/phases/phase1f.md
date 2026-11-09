# Phase 1f: Lifecycle, soak tooling and backup (Phase 1 forms)

Ref: [spec.md](../spec.md) sections E1, E2, E3, E4, E5, E6, E7, F3, F4, C5

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

Items are ordered so that each builds on the ones before: recursive deletes
and conversion first, then the self-contained format that restore reads,
then open and restore, then backup, compact, cloud and soak tooling.

### Item 1f.1: E3 - Recursive deletes

spec.md section: E3

Make every dev wipe and restore path remove a store directory or S3 prefix
recursively instead of failing silently (`jobqueue/store_open.go`,
`jobqueue/store_backup.go`). Cover E3 tests 1 to 3; tests 4 and 5 need
restore from backup and are finished in 1f.4.

- [x] implemented
- [x] reviewed

### Item 1f.2: E1 - Automatic conversion (Phase 1 form)

spec.md section: E1

Add crash-safe conversion of a bbolt `dbFile` on first start in
`jobqueue/store_convert.go` and the progress sidecar in
`internal/db_upgrade_status.go`. Phase 1 form: `.converting` holds a
checkpoint of live jobs with run-state overlays plus the history buckets
in `history.bolt`, stats included. Cover all 8 acceptance tests from E1
against the Phase 1 form.
Also resolve the 0b.1 incidentals on `reliable2_dbcompat_test.go`'s
recovery tests and on opening an older bbolt database, and the 1d.1
incidental on converting `keyRepgroups` ([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1f.3: E5 - Self-contained backup (Phase 1 form)

spec.md section: E5

Make `backup(w)` and `Client.BackupDB(path)` write one tar file
(`VERSION`, one `.ckpt`, `.wal` files and history files, here
`history.bolt`) in `jobqueue/store_backup.go`. Cover both acceptance
tests from E5; test 1's open and test 2's restore halves depend on 1f.4
and may be finished there. Resolve the `storeLockSeeded` and
`recordWorkload` parts of the 1e.5 incidental on the tests that skip on
the log store for want of a backup; its `conveyNeedingBackup` part is
1f.4's and 1f.5's ([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1f.4: E2 - Open, restore and corruption

spec.md section: E2

Implement the `openStore` order and the restore steps in
`jobqueue/store_open.go`: dev wipe (1f.1), leftover `.restoring` and `.corrupt`
handling, resumed conversion (1f.2), restore from a store directory, a
self-contained file (1f.3) or an old bbolt backup, and corruption handling.
Cover all 15 acceptance tests from E2, plus E3 tests 4 and 5 deferred from 1f.1
and the halves of E5's tests deferred from 1f.3: open E5 test 1's backup through
restore (`TestLogStoreSelfContainedBackup`, `store_backup_test.go`, opens it by
extracting the tar), and start a manager with `Client.BackupDB`'s file as
`dbFile` (`TestClientBackupDBSelfContained`, which also opens it by extracting).
Keep the lock taken first, as the 0b.1 incidental owned by 1c.2 says
([incidentals.md](incidentals.md)). Also resolve the 1d.2 incidentals on testing
that recovery deletes every `*.tmp` and on `dropBefore` keeping a state Ref, the
1e.5 incidental on a folded job recovering both complete and live after wal
damage, the 1e.5 review incidental on a last wal whose whole header fails its
CRC being removed as torn, the 1f.1 review incidental on `initDB`'s restore path
removing with plain `os.Remove`, the 1f.1 review round 3 incidental on the fake
S3's `DownloadFile` differing from minio's, the 1f.1 review round 6 incidental
on the development S3 key and deep S3 nesting, the 1f.2 incidental on which
conversion failures restore, the 1f.2 review round 4 incidental on a file at
`dbFile` that is not bbolt, the 1f.2 review round 5 incidental on a conversion's
abandon failing to remove `.converting`, the 1f.2 review round 6 incidental on a
bbolt file whose meta page 0 is damaged, the 1e.5 incidental on
`conveyNeedingBackup` (with 1f.5; un-skip `TestStoreCorruptRestoredFromBackup`
on the log store), and the 1f.3 incidental on `rdrCrash`'s crash leaf
([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1f.5: E4 - Incremental backup (Phase 1 form)

spec.md section: E4

Add incremental backup ticks in `jobqueue/store_backup.go` and
`daemonStopWait()` in `cmd/root.go`. Phase 1 form: `history.bolt` is
shipped whole (paced by `backupCopySyncBytes`) whenever its folded
position changed, in place of sealed segments. Cover the 14 acceptance
tests from E4 against the Phase 1 form, including Phase-1-only test 10;
2c replaces the history step with sealed `.hist` files. With 1f.4,
finish the 1e.5 incidental on the tests that skip on the log store for
want of a backup (make the log store's `forceBackup` a tick and un-skip
`TestStoreInspector`'s forced-backup leaf through `conveyNeedingBackup`;
1f.3 did `storeLockSeeded` and `recordWorkload`), and resolve the 1f.2
review round 4 incidental on asserting a converted checkpoint's
`histFrom`, the 1f.3 review round 1 incidental on the `history.bolt`
read transaction a backup holds during its copy, the 1f.3 review
round 3 incidental on backup starts racing close, the 1f.4
incidental on restore rebuilding wal files from their parts, and the
1f.4 review round 1 incidental on shipped offsets after recovery moves
the folded position back or reuses a torn file's number, and the 1f.4
review round 2 incidental on an open that dies after removing a torn
`walFrom` file ([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1f.6: E6 - Compact (Phase 1 form)

spec.md section: E6

Phase 1 form in `jobqueue/store_compact.go` and `cmd/manager.go`:
compact on an unconverted bbolt file compacts it as bbolt exactly as today
(`db_compact.go` stays), and on a store directory returns
`errCompactNotSupported`; refusal while a manager runs is unchanged.
Cover E6 tests 3, 5 and 6; tests 1, 2 and 4 are Phase 2 (2d).

- [x] implemented
- [x] reviewed

### Item 1f.7: E7 - Cloud deploy and teardown (Phase 1 form)

spec.md section: E7

Make cloud deploy and teardown carry a store directory as a
self-contained file made by `jobqueue.ExportStore`
(`jobqueue/store_export.go`, `cmd/cloud.go`). Cover all 6 acceptance
tests from E7. Depends on 1f.3 and 1f.4. Also resolve the 1f.4 review
round 4 incidental on tar members named `.` and `..`
([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1f.8: F3 - dbstart over the new store

spec.md section: F3

Add `jobqueue.ReadStore` and make `developers/soak/dbstart/main.go`
print the same TSV columns from it; remove `-schema`. Cover the 1
acceptance test from F3.
Also resolve the 1d.1 incidental on the startup history key index at prod
scale ([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1f.9: F4 - soakgate reads "reservation wait" lines

spec.md section: F4

Make `developers/soak/soakgate.py --source wait` count non-durable
hand-outs and abandoned reservations from the `"reservation wait"`
lines C3 writes. Cover all 6 acceptance tests from F4.
Also resolve the 1e.2 incidental on `anyway.py` describing the pre-C3
behaviour ([incidentals.md](incidentals.md)).

- [x] implemented
- [x] reviewed

### Item 1f.10: C5 - Recovery clauses deferred from 1c

spec.md section: C5

Add C5 test 3's backup clause (a byte flipped in a non-last wal file
restores from backup when one exists, with E2) and test 8's
converted-store clause (the first open is of a store converted from bbolt
holding k, with E1) to `jobqueue/store_recover_test.go`. Also resolve
the 1f.5 review round 5 incidental on `TestStoreCrash` making no backup
ticks and the 1f.5 review round 12 incidental on a spent wal file, mutant
W51 and `forgetRecoveryCuts`, and the 1f.8 review round 2 incidental on
`openStoppedStore` holding the store lock ([incidentals.md](incidentals.md)).
Depends on 1f.2 and 1f.4.

- [x] implemented
- [x] reviewed

### Item 1f.11: Switch the default store to log

spec.md section: Implementation Order, Phase 1, 1f; The store interface

Moved from 1e.4 ([blocker-phase1e-1e.4.md](blocker-phase1e-1e.4.md)).
Make `storeKindLog` the default `storeKind` (putting it first in
`store.go` again, as the 1e.1 incidental says) and `log` the
`WR_TEST_STORE` default, and show `make test` and `make race` pass on
it and with `WR_TEST_STORE=all`. Start from the patch and failure list
in the blocker file. Resolve the 0b.1 incidental on tests that reach
bbolt through `server.boltDB()` or `db.go` hooks
([incidentals.md](incidentals.md)), port or pin the out-of-package
bbolt reader `cmd/manager_queue_db_test.go` `readStoredJobs` (1f.8
replaced `developers/soak/dbstart`'s `TestRunReadsRealReservation` with
`TestReadStore`'s crash-image case in `jobqueue`), and find the root
cause of each server test that fails on the log store.
Also resolve the 1e.5 review round 3 incidental on log-store methods that
reject malformed keys, and the 1f.7 incidentals on making E7 test 6's
`cmd` half end-to-end, on deploy's leftover export files and on a
teardown killed between `syncLocalDB`'s renames
([incidentals.md](incidentals.md)).
Depends on 1e.5 and 1f.1 to 1f.10.

- [x] implemented
- [x] reviewed

### Item 1f.12: Stream `wr manager backup -p` to the client

spec.md section: E5

Added on the owner's ruling of 2026-11-07 (delivery-queue row 25). Make
`Server.handleBackup` (`jobqueue/serverCLI.go`) and `Client.BackupDB`
stream the self-contained backup to the client in chunks instead of
buffering it whole in a `bytes.Buffer`, so the manager's memory stays
bounded whatever the store's size, for both the log store and the bbolt
store. Keep the file the client writes byte-identical to today's, keep
E5's acceptance tests passing, and add a test that a backup larger than
one chunk arrives complete and that the manager does not hold it whole.
Resolve the 1f.3 review round 1 incidental on `Server.handleBackup`
and the 1f.11 review round 3 incidental on a deploy after a teardown
killed between `syncLocalDB`'s renames ([incidentals.md](incidentals.md)).

- [ ] implemented
- [ ] reviewed
