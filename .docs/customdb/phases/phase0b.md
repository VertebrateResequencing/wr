# Phase 0b: Test inspection interface

Ref: [spec.md](../spec.md) sections A2

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

## Items

### Item 0b.1: A2 - Test inspection interface

spec.md section: A2

Add `recordKind`, `storeInspector` and `forEachStore(t, fn)` (env
`WR_TEST_STORE`) in `jobqueue/store_inspect_test.go`, with the bbolt
implementation of the inspector, and move the tests that use
`db.bolt.View/Update/Batch` or `*bolt.Tx` onto it. Tests of bbolt
mechanics stay bbolt-only and are listed in `boltOnlyTests`. Cover all 3
acceptance tests from A2. Depends on 0a. The spec lets 0b and 0c run in
either order; run them one after the other.

- [ ] implemented
- [ ] reviewed
