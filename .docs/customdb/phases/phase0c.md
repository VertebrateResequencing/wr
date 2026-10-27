# Phase 0c: Per-writer stats

Ref: [spec.md](../spec.md) sections A3

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

### Item 0c.1: A3 - Per-writer stats

spec.md section: A3

Add `waitHistogram` and the periodic `dbWriterStatsLogMsg` ("db
writer") line for each bbolt writer (`newJobs`, `addChunk`, `archive`,
`bestEffort`, `batch`), plus the `reservationWaitLogMsg` ("reservation
wait") and `addWaitLogMsg` ("add wait") lines, in
`jobqueue/store_stats.go`, atomics only, following `archivefold.go`.
Add `TestAddLatency1000` in `jobqueue/store_addbench_test.go` as A3
defines it. Cover all 4 acceptance tests from A3 in
`jobqueue/store_stats_test.go` and `jobqueue/store_addbench_test.go`.
Depends on 0a. The spec lets 0b and 0c run in parallel; run them one
after the other.

- [ ] implemented
- [ ] reviewed
