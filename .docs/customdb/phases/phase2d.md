# Phase 2d: Conversion to segments and compact

Ref: [spec.md](../spec.md) sections E1, E6

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
  are not items here; they are in [phase2-gate.md](phase2-gate.md).

## Items

### Item 2d.1: E1 - Automatic conversion to segments

spec.md section: E1

Make conversion write `.hist` segments instead of `history.bolt`
(`jobqueue/store_convert.go`). Cover all 8 acceptance tests from E1
against the final store.

- [ ] implemented
- [ ] reviewed

### Item 2d.2: E6 - Compact

spec.md section: E6

Implement compact on the store directory in `jobqueue/store_compact.go`
(recover, fold and seal, checkpoint, delete covered wal files, merge
small segments) and convert-then-compact for an unconverted bbolt file via
`convertBoltDB`; delete `errCompactNotSupported` and the Phase-1-only
tests 5 and 6. Cover E6 tests 1 to 4. Depends on 2d.1.

- [ ] implemented
- [ ] reviewed
