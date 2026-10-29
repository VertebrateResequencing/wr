# Phase 0 gate: no behaviour change, speed

Ref: [spec.md](../spec.md) section A1

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
- The items below are this phase's gates. Run them after its
  sub-phases, one at a time.

## Items

Run these after 0a to 0d are all reviewed.

### Item 0g.1: Phase 0 speed gate

spec.md section: Implementation Order, Phase 0, Gate; A1

Show no behaviour change (the existing suite passes with no test removed
or skipped, A1 test 3) and run `make speed` (not `make speed-full`)
against `d1a4b6d7`: every measure within 10%. Record both runs' numbers in
the branch's docs.

Run 2026-10-29 at `98f03181`; both parts pass. `make test` at the base
gives 1008 passed and 23 skipped, at HEAD 1047 and 27: every base test
still runs, the 8 renamed ones and 33 moved or split Convey leaves are
mapped, and the 4 new skips are new opt-in tests. `make speed` passes in
all 3 runs, with every measure within 10% once the noisy single-round
report-storm max-archive-ms was rerun over 12 rounds (+0.9%, p=0.66).
Numbers and the test-set comparison are in
[results/phase0-gate.md](../results/phase0-gate.md).

- [x] implemented
- [x] reviewed
