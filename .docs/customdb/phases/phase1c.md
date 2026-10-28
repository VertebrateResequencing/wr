# Phase 1c: Log store live tier, recovery, lock and version

Ref: [spec.md](../spec.md) sections C2, C4, C5, C7, D1

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

### Item 1c.1: C2 - Adds, envs, limits and rep group lookups

spec.md section: C2

Add the log store (`logStore`) in `jobqueue/store_log.go` and
`jobqueue/store_records.go`: `storeNewJobsGuarded` (one s2 block per
call), envs, limit groups and rep group lookups, durable before reply,
with the log store's `storeInspector` (A2) and the `log` writer's
`"db writer"` stats (A3). Include the state, std and `complete` records
that transitions append (one state record each, C3; C3's durable
reservation rules stay in 1e), which C2 test 2 and the tests of later
items drive. Add the replay on open in `jobqueue/store_recover.go`
that the reopen in these tests needs; 1c.4 extends it to its records and
1c.5 completes recovery.
Cover all 4 acceptance tests from C2.

- [ ] implemented
- [ ] reviewed

### Item 1c.2: C7 - Lock and version

spec.md section: C7

Add the store directory lock and `VERSION` check in
`jobqueue/store_open.go`, so two managers never share a store and a
store from a newer wr is refused. Cover all 4 acceptance tests from C7.
Depends on 1c.1.

- [ ] implemented
- [ ] reviewed

### Item 1c.3: D1 (history.bolt and overlay) - Folder into bbolt history

spec.md section: D1

Add `history.bolt` and the in-memory history overlay
(`jobqueue/store_history_bolt.go`) so that history queries on the log
store answer while completions are not yet folded. The folder itself and
its stats are item 1d.2, which completes D1's 7 acceptance tests; here,
test the overlay answers D1 test 1 names with no folder running.
Depends on 1c.1.

- [ ] implemented
- [ ] reviewed

### Item 1c.4: C4 - Delete, modify and reruns

spec.md section: C4

Add durable, atomic delete, modify and rerun records to
`jobqueue/store_log.go`. Cover all 11 acceptance tests from C4, except
the checkpoint clauses of tests 6 and 8, which run in item 1d.1. Depends
on 1c.3.

- [ ] implemented
- [ ] reviewed

### Item 1c.5: C5 - Recovery

spec.md section: C5

Add recovery in `jobqueue/store_recover.go`, rebuilding exactly the
acknowledged state. Cover the 8 acceptance tests from C5, except test 3's
backup clause and test 8's converted-store clause, which run in 1f (item
1f.10), test 7's "after the folder has run" clause, which runs in item
1d.2, and test 1's checkpoints and test 8's checkpoint clause, which run
in item 1d.1. Depends on 1c.4.

- [ ] implemented
- [ ] reviewed
