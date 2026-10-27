# Phase 2 gate: final store

Ref: [spec.md](../spec.md) sections A4, A5, D3, F5

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

Run these after 2a to 2e are all reviewed, one at a time. Each needs the
LSF farm, NFS or hours of wall time, so the orchestrator schedules them
with the user. Each item's implementor runs the gate and records its
evidence (commands, commit, outputs) in the branch's docs and the PR; its
reviewer checks that evidence against the spec's criteria. A failing gate
is a defect to route through the `bugfix` skill, not a reason to relax
the gate.

### Item 2g.1: A4 on the final store, and fuzzing

spec.md section: Implementation Order, Phase 2, Gates; A4

Repeat item 1g.1 of [phase1-gate.md](phase1-gate.md) against the final
store.

- [ ] implemented
- [ ] reviewed

### Item 2g.2: A5 replay of the Phase 0 recording

spec.md section: Implementation Order, Phase 2, Gates; A5

Replay the Phase 0 prodsim recording (item 0d.3) against the final store
with 0 mismatches, as in item 1g.2.

- [ ] implemented
- [ ] reviewed

### Item 2g.3: Crash soak repeated

spec.md section: Implementation Order, Phase 2, Gates

Repeat the crash soak of item 1g.3 with the same pass criteria against
the final store. This soak gives the authoritative backup-I/O result.

- [ ] implemented
- [ ] reviewed

### Item 2g.4: make speed-full

spec.md section: Implementation Order, Phase 2, Gates

Run `make speed-full` against `d1a4b6d7`: within 10%.

- [ ] implemented
- [ ] reviewed

### Item 2g.5: Daytime NFS targets

spec.md section: Implementation Order, Phase 2, Gates

Measure both daytime NFS runs of item 1g.5 against the final store, under
the same rules.

- [ ] implemented
- [ ] reviewed

### Item 2g.6: wrdev.sh scenarios

spec.md section: Implementation Order, Phase 2, Gates

Run wrdev `crash-recovery` and every remaining `wrdev.sh` scenario; all
pass (F5 deleted `freelist-check`, `remap-stall-check` and
`compact-fixture`).

- [ ] implemented
- [ ] reviewed

### Item 2g.7: wr status -i on the converted production fixture

spec.md section: Implementation Order, Phase 2, Gates; D3

Time `wr status -i` for production's biggest rep group on the converted
`runstate-gate/fixtures/prod.db` and on develop with the bbolt fixture;
the new store is no slower. Record both timings in the PR.

- [ ] implemented
- [ ] reviewed
