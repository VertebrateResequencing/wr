# Phase 0d: Crash test and record/replay

Ref: [spec.md](../spec.md) sections A4, A5

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
  are not items here; they are in [phase0-gate.md](phase0-gate.md).
  Item 0d.3 is not a gate: the spec places its prodsim recording in 0d.

## Items

### Item 0d.1: A4 - Kill -9 crash test

spec.md section: A4

Port the prototype's crash test to drive `store` in
`jobqueue/store_crash_test.go` (the test re-executes its own binary as a
child) and run it against bbolt. Cover acceptance tests 1, 3 and 4 from A4
against bbolt. Test 2 (damaged copies) applies only to the log store:
write its copy-and-recover step so it runs only when the store under test
is the log store; it first runs in item 1g.1
([phase1-gate.md](phase1-gate.md)).

- [x] implemented
- [x] reviewed

### Item 0d.2: A5 - Record/replay harness

spec.md section: A5

Add `recordingStore`, `recordedCall`, `storeDigest` and
`replayRecording` in `jobqueue/store_record.go`, enabled by env
`WR_STORE_RECORD` at `openStore`, plus the `TestStoreReplay` runner (env
`WR_STORE_REPLAY`). The recording's wal frames need the plain-frame
append and scan of `jobqueue/wal/frame.go` and `jobqueue/wal/scan.go`,
so this item adds that part early; item 1a.1 ([phase1a.md](phase1a.md))
completes it. Cover acceptance tests 1, 2, 3 and 5 from A5.

Test 4 and the removed-methods mapping it checks are deferred to item
1e.1 ([phase1e.md](phase1e.md)): they replay onto `reserveRunState`,
which C3 adds in Phase 1, and 1e.1 removes the methods a live bbolt store
would need to record test 4's input. So this item also commits that
input: a bbolt recording at
`jobqueue/testdata/store_record/removed_methods.rec` of 3 adds, then 3
`updateJobRunStateDurableWithin` calls reserving those jobs, 1
`logReserveNotDurable` and 1 `setBatchTuning` call. A test in
`jobqueue/store_record_test.go` that runs only when env
`WR_STORE_RECORD_REGEN` is set writes it. Depends on 0d.1 only for
ordering.

- [x] implemented
- [x] reviewed

### Item 0d.3: Record the Phase 0 prodsim run

spec.md section: Implementation Order, Phase 0, 0d

With 0d.2 in place, record a prodsim run on develop-equivalent bbolt with
`WR_STORE_RECORD` set, and keep the recording on NFS under
`/nfs/hgi/wr/sb10-bigdb/customdb/`. Phase 1 and Phase 2 replay this
recording (A5 gates). Record its path, the commit it was made on, and the
run's shape in the branch's docs. The prodsim run uses LSF; the
implementor runs it unless the farm is unavailable, in which case it
reports the blocker instead of substituting a local run.

- [ ] implemented
- [ ] reviewed
