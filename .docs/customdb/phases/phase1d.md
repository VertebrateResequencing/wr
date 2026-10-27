# Phase 1d: Checkpoints and folder

Ref: [spec.md](../spec.md) sections C6, D1

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

### Item 1d.1: C6 - Checkpoints and spec addressing

spec.md section: C6

Add background checkpoints in `jobqueue/store_checkpoint.go` that bound
recovery and log size and keep every live spec addressable. Cover the
Phase 1 acceptance tests from C6 (4 of 5); test 2 is Phase 2 and runs in
2b.

- [ ] implemented
- [ ] reviewed

### Item 1d.2: D1 - Folder into bbolt history

spec.md section: D1

Add the background folder into `history.bolt` and the `fold` writer
stats in `jobqueue/store_fold.go`, on top of the overlay from 1c.2.
Cover all 7 acceptance tests from D1 in `jobqueue/store_fold_test.go`.
Depends on 1d.1.

- [ ] implemented
- [ ] reviewed
