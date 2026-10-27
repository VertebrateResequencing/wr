# Phase 2a: History segments

Ref: [spec.md](../spec.md) sections D2

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

Phase 2 starts after the Phase 1 gates ([phase1-gate.md](phase1-gate.md))
are reviewed.

### Item 2a.1: D2 - History segments

spec.md section: D2

Add immutable `.hist` segments in `jobqueue/store_segment.go` and
`jobqueue/store_history.go`: zstd blocks of at most 64KiB, the footer
sections, the `WRHIST01` trailer, the in-memory key, rep group and dep
group indexes, and the 64MiB footer LRU. Cover all 5 acceptance tests from
D2.

- [ ] implemented
- [ ] reviewed
