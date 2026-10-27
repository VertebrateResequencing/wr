# Phase 2c: Final incremental backup and self-contained backup

Ref: [spec.md](../spec.md) sections E4, E5

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

### Item 2c.1: E4 - Incremental backup (final form)

spec.md section: E4

Ship sealed `.hist` files once each in place of the whole
`history.bolt` (`jobqueue/store_backup.go`). Cover E4's acceptance
tests against the final store; test 10 is Phase 1 only and is removed
with `history.bolt` shipping. Depends on 2b.

- [ ] implemented
- [ ] reviewed

### Item 2c.2: E5 - Self-contained backup with segments

spec.md section: E5

Make the self-contained tar carry `.hist` segments as its history files.
Cover both acceptance tests from E5 on the final store.

- [ ] implemented
- [ ] reviewed
