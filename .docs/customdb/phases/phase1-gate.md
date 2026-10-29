# Phase 1 gate: crash, replay, soak, speed and NFS targets

Ref: [spec.md](../spec.md) sections A4, A5, A3, C3, F4

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

Run these after 1a to 1f are all reviewed, one at a time. Each needs the
LSF farm, NFS or hours of wall time, so the orchestrator schedules them
with the user. Each item's implementor runs the gate and records its
evidence (commands, commit, outputs) in the branch's docs; its reviewer
checks that evidence against the spec's criteria. A failing gate is a
defect to route through the `bugfix` skill, not a reason to relax the
gate.

### Item 1g.1: A4 on the log store, and fuzzing

spec.md section: Implementation Order, Phase 1, Gates; A4

Run A4 against the log store: 16 rounds local and 16 on NFS
(`WR_CRASHTEST_ROUNDS=16`, with `WR_CRASHTEST_DIR` under the NFS work
directory for the NFS run), plus 20 damaged copies each, with 0 missing.
This is the first run of A4 acceptance test 2 (damaged copies), deferred
from item 0d.1 ([phase0d.md](phase0d.md)); acceptance tests 1, 3 and 4 run
against the log store too. Run `FuzzScan` and `FuzzFlatSpec` for 10
minutes each with no crasher.

- [ ] implemented
- [ ] reviewed

### Item 1g.2: A5 replay of the Phase 0 recording

spec.md section: Implementation Order, Phase 1, Gates; A5

Replay the Phase 0 prodsim recording (item 0d.3; its path, shape and
first bbolt replay are in
[results/phase0-prodsim-recording.md](../results/phase0-prodsim-recording.md))
against the log store with 0 mismatches, removed methods mapped or
skipped as A5 says, and pass the A2 suite with `WR_TEST_STORE=all`. The
replay folds (the D1 flush hook) before each learned-requirements probe,
as A5 says. Those forced folds hide fold-liveness bugs (a folder that
stalls or never folds without the hook), so D1's tests must cover fold
liveness; this replay cannot.

- [ ] implemented
- [ ] reviewed

### Item 1g.3: Crash soak at production LSF concurrency

spec.md section: Implementation Order, Phase 1, Gates

Run the crash soak with a staged ramp (battery10 shape, about 6,000
runners, at least 3h, injected stalls and crashes, `relbury.sh` batches at
the injected kills as in the soak README), without `FIXTURE`. Pass when
`soakgate.py`'s `missing` line has `absent=0`, `relburycheck.py` prints
`problems: 0`, `soakgate.py --source wait` prints `nondurable source=wait
inside=0 outside=0` with an `abandoned` line ending `ok` and `stalls` at
least 1, and the `doubles` line has `outside=0`. Never replace the NFS wr
binary while the soak's runners are live.

- [ ] implemented
- [ ] reviewed

### Item 1g.4: make speed-full

spec.md section: Implementation Order, Phase 1, Gates

Run `make speed-full` against `d1a4b6d7`: no regression above 10%.

- [ ] implemented
- [ ] reviewed

### Item 1g.5: Daytime NFS targets

spec.md section: Implementation Order, Phase 1, Gates; A3; C3

Measured apart from the crash soak, both runs wholly between 10:00 and
17:00 on one weekday with the work directory on NFS: a stall-free soak
(`run.sh` with `SOAK_ROOT` on NFS, `USE_FUSE=0`, `HOURS=1`,
`RESTART_MIN=120`, no injectors) where every `"reservation wait"` line has
`p99` at most 50ms and `abandoned=0`, with no averaging across lines; and
`TestAddLatency1000` with `WR_ADDBENCH_DIR` under the NFS work directory
and `WR_TEST_STORE=log`, with 1,000-job add p50 at most 200ms in each of 3
runs.

- [ ] implemented
- [ ] reviewed

### Item 1g.6: wrdev.sh scenarios

spec.md section: Implementation Order, Phase 1, Gates

Run wrdev `crash-recovery` and every other `wrdev.sh` scenario; all pass.

- [ ] implemented
- [ ] reviewed
