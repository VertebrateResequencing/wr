# Count limit groups that have no limit (2026-10-04)

- Branch: `limit-uncounted-6a3d4650`
- Base: `origin/develop` at `6c4b3d25` (#674)
- Worktree: `../wr-limit448`
- Queue owner: this branch and this checklist
- Source: GitHub issue #448, "Limits not always working?", and the caller's
  triage of it.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2`, `GOCACHE` outside the home
directory and `env -u WR_LSF_TEST_KEY`: targeted `go test -tags netgo` runs,
plain and `-race`; `golangci-lint run ./limiter/ ./jobqueue/`; `cleanorder
-min-diff` on the edited Go files; limiter benchmarks before and after with
`-count 6` and benchstat. The caller runs `make test`, `make race` and `make
speed`.

- [x] Limits can be bypassed: with a limit of 16 on `rsync_118`, as many as 20
  of its commands were seen running (#448). The issue's second symptom, lost
  commands holding their limit slots until the manager restarts, is by design:
  a lost command still officially runs.
  - Fixes #448.
  - Source: GitHub #448; triage: a job reserved while its limit group has no
    limit is never counted, so a limit set later starts from 0.
  - Red: `go test -tags netgo -count 1 ./limiter -run
    TestLimiterCountsGroupsWithoutALimit` exits 1 on `6c4b3d25`, plain and
    `-race`:

    ```text
      Line 159:
      Expected: false
      Actual:   true
      Line 183:
      Expected: false
      Actual:   true
    --- FAIL: TestLimiterCountsGroupsWithoutALimit (0.00s)
    ```

    Line 159 is a 4th `Increment` after 3 were counted with no limit and
    `SetLimit` 2; line 183 a 3rd `Increment` after limit 2, 2 counted,
    `RemoveLimit` and `SetLimit` 2 again.

    `go test -tags netgo -count 1 ./jobqueue -run TestLimitSetOnRunningGroup`
    exits 1 on `6c4b3d25`, plain and `-race`, in all three cases (limit set
    with `wr limit`, limit set by adding a job with `name:2`, limit removed
    and set again):

    ```text
      Line 116:
      Expected: nil
      Line 116:
      Expected: nil
      Line 116:
      Expected: nil
    --- FAIL: TestLimitSetOnRunningGroup (1.04s)
    ```

    Line 116 is a reserve made with 3 jobs running and a limit of 2: it
    returned a job.

  - Cause: `Limiter.vivifyGroup` (`limiter/limiter.go`) only makes a group
    when the callback returns a valid limit, so `Increment` of a group with
    no limit counts nothing, while the job still records the group as
    incremented (`noteIncrementedLimitGroups`). A later `SetLimit` makes the
    group with a count of 0, letting up to the limit more run on top of those
    already running, and each of those, as it exits, then lowers the new count
    further through `Decrement`. `RemoveLimit` deleted the group and its count
    while its jobs ran, so removing and setting again did the same. Separately,
    `Server.storeLimitGroups` (`jobqueue/server.go`, the `wr add` and modify
    path) only called `SetLimit` for groups the database reported as changed,
    and a limit stored for the first time is reported unchanged; the limiter
    then picked the limit up from the database, again from a count of 0.
  - Fix: `limiter/group.go`, `limiter/limiter.go`: `Increment` now counts a
    group the callback knows no limit for, as an in-memory count-mode group
    whose limit is the `noLimit` sentinel (a sentinel, not a new field, keeps
    the group struct in its 64-byte allocation size class). Such groups are
    made only in `incrementGroups`, never by the read-only queries or a failed
    `Increment`, and `Decrement` forgets them at a count of 0 like any other,
    so a fresh group per run does not accumulate. `SetLimit` on a known group
    keeps its count. `RemoveLimit` on a count group in use makes it unlimited
    instead of deleting it; one not in use is deleted as before. `GetLimit`,
    `GetLimits`, `GetLowestLimit` and `GetRemainingCapacity` treat an unlimited
    group as before (-1, absent from `GetLimits`, skipped).
    `jobqueue/server.go`: `storeLimitGroups` gives the limiter every count
    limit it stored, not only the changed ones (narrowed by item 2 below to
    first stores, which are now reported as changed). `limiter/doc.go` describes
    this, and its example now uses the real callback signature.
    Tests: `limiter/limiter_test.go` (`TestLimiterCountsGroupsWithoutALimit`,
    `BenchmarkLimiterIncDecUnlimited`), `jobqueue/limit_uncounted_test.go`
    (`TestLimitSetOnRunningGroup`).
  - Callers checked: `incrementReserveLimit`/`noteReserveLimitGroups` and
    `Job.decrementLimitGroups` (reserve and exit) pair every `Increment` with
    one `Decrement`, so counting unlimited groups stays balanced.
    `recoverRunningJob` re-increments every recovered running job's groups
    after a restart, so counts, including unlimited groups', are rebuilt from
    running jobs. `setLimitGroup` (`wr limit`) already called `SetLimit` for
    any valid limit. The rac budget code (`seedBudgetsOf`,
    `readyJobLimitBlocked`, `readyJobsCanContendLimitBudget`,
    `capGroupCountsToLimits`) reads `GetRemainingCapacity` and `GetLimits`,
    which still report an unlimited group as -1 or absent. `getsetlg` and
    `wr limit` output go through `GetLimit`/`GetLimits` and are unchanged.
    DB persistence (`db.storeLimitGroups`, `retrieveLimitGroup`) is
    unchanged here (item 2 changes what it reports). Time-based groups are
    never unlimited and keep their behaviour.
  - Mutants, each run against the fixed code in a scratch copy, each failing
    a test (limiter package unless noted):
    `RemoveLimit` always deletes (line 183; jobqueue line 116);
    `incrementGroups` skips groups with no limit (159; jobqueue 116 x2);
    `storeLimitGroups` back to changed-only (jobqueue 116);
    `vivifyGroup` makes unlimited groups (235);
    `GetRemainingCapacity` does not skip unlimited (207);
    `decrement` never forgets an unlimited group (217);
    `GetLimits`, `GetLowestLimit` or `GetLimit` report an unlimited group
    (177/202, 205, 178/203 and `TestLimiter`);
    `removeLimit` a no-op (177); `canIncrement` ignores `noLimit` (155/187
    and `TestLimiter`).
  - Green: both red commands exit 0, plain and `-race`; `./limiter` whole
    package exits 0 plain and `-race`; 19 limit-related jobqueue tests
    (`TestJobqueueLimitGroups`, `TestJobqueueSuspendResumeLimitGroups`,
    `TestLimitGroupReport`, `TestReliable3*` limit and rac accounting tests,
    `TestReliable4LimitGroupRemoval`, `TestReliable4LimitGroupsNoWrite`,
    `TestReliable4AddOneWriteTx`, `TestReliable4RacBoundedBySchedulable`,
    `TestJobqueueModify`, `TestRESTJobModification*` and others) exit 0
    plain and `-race`. `golangci-lint` 0 issues.
  - Benchmarks (`go test -run '^$' -bench BenchmarkLimiter -benchmem -count
    6 ./limiter`, base `6c4b3d25` against this fix, benchstat):
    `LimiterIncDec` 2.433µs to 2.343µs (~, p=0.132), 544 B, 9 allocs, both
    unchanged; `LimiterCapacity` 2.212µs to 2.170µs (~, p=0.310), 632 B, 11
    allocs, both unchanged; `LimiterIncDecUnlimited` 2.085µs to 2.190µs
    (+5%, p=0.041), 600 B to 632 B, 24 to 11 allocs. The unlimited case now
    keeps a map entry while in use instead of calling the callback on every
    `Increment`; the benchmark's callback is free, but the manager's is a
    bolt read, which a group in use no longer needs.
- [x] Review of `9a383ba1` (low): `Server.storeLimitGroups` calls `SetLimit`
  for every count group in an add, including ones the database reported
  unchanged, outside any lock covering the database read. With `g:5` stored,
  `wr add -l g:5` reads "unchanged"; meanwhile `wr limit -g g:3` (database 3,
  `SetLimit` 3) or `g:-1` (record deleted, `RemoveLimit`) lands; then the
  add's `SetLimit(5)` brings back the stale limit in memory until the group is
  forgotten. Before `9a383ba1` only changed groups were applied.
  - Source: the caller's review of `9a383ba1`.
  - Red: `go test -tags netgo -count 1 ./jobqueue -run
    TestLimitAddKeepsNewerLimit` exits 1 on `9a383ba1`'s `storeLimitGroups`
    (applied as a mutant in a scratch copy), plain and `-race`:

    ```text
      Line 209:
      Expected: 3
      Actual:   5
      Line 216:
      Expected: -1
      Actual:   5
    --- FAIL: TestLimitAddKeepsNewerLimit
    ```

    The test races the add with `wr limit` through a new test-only hook,
    `limitGroupsStoredHook`, which `storeLimitGroups` calls between the
    database store and the limiter update (nil in production, like the
    package's other `*Hook` seams), and runs `setLimitGroup` there.
  - Fix: `jobqueue/db.go`: `planLimitGroupStore` reports a limit stored for
    the first time as changed (the group had no limit before), so
    `storeLimitGroups` returns it in `changed`. `jobqueue/server.go`:
    `Server.storeLimitGroups` is back to calling `SetLimit` only for changed
    groups. `TestReliable4LimitGroupsNoWrite`
    (`jobqueue/reliable4_add_tx_test.go`) asserted that a first store is not a
    change; it now asserts both new groups are reported changed. Its write
    transaction counts are unchanged.
  - Mutants: `SetLimit` for every count group again fails
    `TestLimitAddKeepsNewerLimit` at lines 209 and 216, plain and `-race`.
    A first store reported unchanged again fails `TestLimitSetOnRunningGroup`
    ("when set by adding a job with the limit", line 118) and
    `TestReliable4LimitGroupsNoWrite` (line 176), plain and `-race`.
  - Green: the red command exits 0 (and `-count 5`); the 19 limit-related
    jobqueue tests of item 1 plus `TestLimitAddKeepsNewerLimit` and
    `TestReliable4AddStorm` exit 0 plain and `-race`. `golangci-lint run
    ./limiter/ ./jobqueue/` 0 issues; `cleanorder -min-diff` on the edited
    files.
