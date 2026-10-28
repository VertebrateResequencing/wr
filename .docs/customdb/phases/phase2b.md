# Phase 2b: History queries on segments

Ref: [spec.md](../spec.md) sections D3, D1, D2, C6

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

### Item 2b.1: D3 - History queries on segments

spec.md section: D3

Answer every history query from segments in `jobqueue/store_history.go`,
including `spendArchivedBytesByRepGroup`'s pricing, so each answer equals
today's. Cover acceptance tests 2, 3 and 4 from D3, writing test 2's
segments with 2a.1's segment writer. Test 1 is deferred to item 2b.2: it
replays with segments sealed after every write call, which needs the
folder to seal segments. The folder keeps committing to `history.bolt`
until 2b.2 switches it, so the log store answers from `history.bolt`
until then and existing tests keep passing. Depends on 2a.

- [ ] implemented
- [ ] reviewed

### Item 2b.2: Folder writes segments; learned samples from footers

spec.md section: Implementation Order, Phase 2, 2b; D1; D2; D3; C6;
Folder and history

Make the folder seal `.hist` segments (`NNNNNNNN.hist.tmp`, footer,
fsync, rename) instead of committing to `history.bolt`, and load learned
requirement samples from segment footers. Keep D1's acceptance tests
passing on the segment store and add C6 test 2 (the Phase 2 checkpoint
with the open segment not yet sealed). Cover D2 acceptance tests 1, 2
and 4 in `jobqueue/store_segment_test.go`, deferred from item 2a.1, and
D3 acceptance test 1 in `jobqueue/store_history_test.go`, deferred from
item 2b.1, because they need the folder to seal segments. Depends on 2b.1.

- [ ] implemented
- [ ] reviewed
