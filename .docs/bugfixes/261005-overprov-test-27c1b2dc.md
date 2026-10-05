# Bugfix: overprovision-check sibling group count

- Branch: `overprov-test-27c1b2dc`
- Base: `origin/develop` at `545e67d449d6b8685f5720b10d5bd8ad3b434e50`
- Queue owner: `overprov-test-27c1b2dc`, this checklist
- Worktree: `../wr-overprov`

## Items

- [x] `wrdev.sh overprovision-check` runs TestReliable3LimitGroupOverProvision
  at scale WR_OP_LIMIT=2000 WR_OP_SIBLINGS=50 WR_OP_READY=5000 and fails
  asserting len(groups)==50 (gets 49), while the real invariant (summed request
  2000 <= limit 2000) holds. Same on develop before #679.
  - Source: battery10 F1 (overnight battery RESULTS.md, Phase 2;
    sweep/logs/05-overprovision-check.out)
  - Red command (mirrors `cmd_overprovision_check` in `developers/wrdev.sh`):
    `WR_OP_LIMIT=2000 WR_OP_SIBLINGS=50 WR_OP_READY=5000 go test ./jobqueue/ -run TestReliable3LimitGroupOverProvision -count=1 -v`,
    exit 1:

    ```text
    ... => summed runner request=2000 (buggy per-group accounting would give ~100000)
      Expected: 50
      Actual:   49
    --- FAIL: TestReliable3LimitGroupOverProvision (1.11s)
    ```

    Also red at `WR_OP_SIBLINGS=10`, 20 and 100 (one group short each time);
    green at 9 and at the default 5.
  - Cause: sibling g got RAM `100+g*100`. Since #675 the test drives the live
    rac path, which counts jobs in scheduler groups built from
    `reqForScheduler`, and that adds `reqSchedExtraRAM` (100) to any RAM below
    `reqSchedSpecialRAM` (924). Sibling 8 (900MB) therefore becomes 1000MB,
    the same scheduler group as sibling 9 (1000MB). The collision needs 10 or
    more siblings, so the default size never hits it.
  - Files: `jobqueue/reliable3_overprovision_test.go`.
  - Approach: sibling g now gets RAM `reqSchedSpecialRAM+g*100`. `reqForScheduler`
    leaves every such RAM unchanged, so the siblings always form the requested
    number of distinct scheduler groups, and the `len(groups)` and over-provision
    assertions stay as they were.
  - Green: the red command exits 0 (`summed runner request=2000`), as do
    `developers/wrdev.sh overprovision-check` and `WR_OP_SIBLINGS` = default,
    9, 10, 20 and 100.
  - Mutant: keying `limitBudgets` in `racCycleGroupFor` by scheduler group as
    well as limit group (the original per-scheduler-group bug) fails the fixed
    test at wrdev scale (`summed runner request=100000`, `Expected '100000' to
    be less than or equal to '2000'`, exit 1), and at the default size (20 > 4).
