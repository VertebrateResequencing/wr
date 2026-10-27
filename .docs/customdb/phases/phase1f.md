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
`jobqueue/store_backup.go`). Cover all 5 acceptance tests from E3.

- [ ] implemented
- [ ] reviewed

### Item 1f.2: E1 - Automatic conversion (Phase 1 form)

spec.md section: E1

Add crash-safe conversion of a bbolt `dbFile` on first start in
`jobqueue/store_convert.go` and the progress sidecar in
`internal/db_upgrade_status.go`. Phase 1 form: `.converting` holds a
checkpoint of live jobs with run-state overlays plus the history buckets
in `history.bolt`, stats included. Cover all 8 acceptance tests from E1
against the Phase 1 form.

- [ ] implemented
- [ ] reviewed

### Item 1f.3: E5 - Self-contained backup (Phase 1 form)

spec.md section: E5

Make `backup(w)` and `Client.BackupDB(path)` write one tar file
(`VERSION`, one `.ckpt`, `.wal` files and history files, here
`history.bolt`) in `jobqueue/store_backup.go`. Cover both acceptance
tests from E5; test 2's restore half depends on 1f.4 and may be finished
there.

- [ ] implemented
- [ ] reviewed

### Item 1f.4: E2 - Open, restore and corruption

spec.md section: E2

Implement the `openStore` order and the restore steps in
`jobqueue/store_open.go`: dev wipe (1f.1), leftover `.restoring`
cleanup, resumed conversion (1f.2), restore from a store directory, a
self-contained file (1f.3) or an old bbolt backup, and corruption
handling. Cover all 15 acceptance tests from E2.

- [ ] implemented
- [ ] reviewed

### Item 1f.5: E4 - Incremental backup (Phase 1 form)

spec.md section: E4

Add incremental backup ticks in `jobqueue/store_backup.go` and
`daemonStopWait()` in `cmd/root.go`. Phase 1 form: `history.bolt` is
shipped whole (paced by `backupCopySyncBytes`) whenever its folded
position changed, in place of sealed segments. Cover the 13 acceptance
tests from E4 against the Phase 1 form, including Phase-1-only test 10;
2c replaces the history step with sealed `.hist` files.

- [ ] implemented
- [ ] reviewed

### Item 1f.6: E6 - Compact (Phase 1 form)

spec.md section: E6

Phase 1 form in `jobqueue/store_compact.go` and `cmd/manager.go`:
compact on an unconverted bbolt file compacts it as bbolt exactly as today
(`db_compact.go` stays), and on a store directory returns
`errCompactNotSupported`; refusal while a manager runs is unchanged.
Cover E6 tests 3, 5 and 6; tests 1, 2 and 4 are Phase 2 (2d).

- [ ] implemented
- [ ] reviewed

### Item 1f.7: E7 - Cloud deploy and teardown (Phase 1 form)

spec.md section: E7

Make cloud deploy and teardown carry a store directory as a
self-contained file made by `jobqueue.ExportStore`
(`jobqueue/store_export.go`, `cmd/cloud.go`). Cover all 6 acceptance
tests from E7. Depends on 1f.3.

- [ ] implemented
- [ ] reviewed

### Item 1f.8: F3 - dbstart over the new store

spec.md section: F3

Add `jobqueue.ReadStore` and make `developers/soak/dbstart/main.go`
print the same TSV columns from it; remove `-schema`. Cover the 1
acceptance test from F3.

- [ ] implemented
- [ ] reviewed

### Item 1f.9: F4 - soakgate reads "reservation wait" lines

spec.md section: F4

Make `developers/soak/soakgate.py --source wait` count non-durable
hand-outs and abandoned reservations from the `"reservation wait"`
lines C3 writes. Cover all 6 acceptance tests from F4.

- [ ] implemented
- [ ] reviewed

### Item 1f.10: C5 - Recovery clauses deferred from 1c

spec.md section: C5

Add C5 test 3's backup clause (a byte flipped in a non-last wal file
restores from backup when one exists, with E2) and test 8's
converted-store clause (the first open is of a store converted from bbolt
holding k, with E1) to `jobqueue/store_recover_test.go`. Depends on 1f.2
and 1f.4.

- [ ] implemented
- [ ] reviewed
