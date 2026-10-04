# Stop a limit lookup bringing back a removed limit (2026-10-04)

- Branch: `limiter-stale-d8688c70`
- Base: `origin/develop` at `29e8927c` (#677)
- Worktree: `../wr-limiterstale`
- Queue owner: this branch and this checklist
- Source: the deferred incidental in
  `.docs/bugfixes/261004-limit-order-7d84a298.md` on branch
  `limit-order-7d84a298` (not yet merged), found by that branch's implementor.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2`, `GOCACHE` outside the home
directory and `env -u WR_LSF_TEST_KEY`: targeted `go test -tags netgo` runs,
plain and `-race`; `golangci-lint run ./limiter/`; `cleanorder -min-diff` on
the edited Go files; `BenchmarkLimiter*` before and after with benchstat. The
caller runs `make test`, `make race` and `make speed`.

- [x] The limiter's DB callback can read a stale limit while `wr limit -g
  g:-1` runs: a reserve or `GetLimit` for a group not in memory has its
  callback read `g=5` from the DB; meanwhile `wr limit -g g:-1` deletes the DB
  record and calls `RemoveLimit`, which does nothing because the group isn't
  in memory; the limiter then re-locks and builds the group from the stale 5.
  Memory then enforces 5 while the DB has no limit, until the group is set
  again (a group created only by `GetLimit` has count 0, is never decremented
  and so never dropped). A `wr limit` that sets a value isn't affected, since
  its `SetLimit` puts the group in memory first.
  - Source: deferred incidental of `261004-limit-order-7d84a298.md`.
  - Red: `go test -tags netgo -count 1 ./limiter -run
    TestLimiterLimitChangedDuringLookup` exits 1 on `29e8927c` with the new
    test, plain and `-race`:

    ```text
      Line 365:
      Expected: (*limiter.GroupData){mode:limiter.groupMode(0), limit:0, current:0}
      Actual:   (*limiter.GroupData){mode:limiter.groupMode(1), limit:5, current:0}
      Line 386:
      Expected: -1
      Actual:   5
      Line 407:
      Expected: -1
      Actual:   5
      Line 430:
      Expected: (*limiter.GroupData){mode:limiter.groupMode(1), limit:3, current:0}
      Actual:   (*limiter.GroupData){mode:limiter.groupMode(1), limit:5, current:0}
    --- FAIL: TestLimiterLimitChangedDuringLookup (0.00s)
    ```

    Each case holds the callback's first lookup of `g` after it has read the
    stored limit of 5, stores the new limit, tells the limiter as `wr limit`
    does, then lets the lookup return. Line 365: `RemoveLimit` during a
    `GetLimit`, which returned 5. Line 386: `RemoveLimit` during an
    `Increment`, after which `g` reported limit 5. Line 407: the same during
    an `Increment` of `other` and `g`, so `g` is not the first group looked
    up. Line 430: `SetLimit(3)`
    and a `Decrement` of `g` (forgetting it at count 0) during a `GetLimit`,
    which rebuilt `g` from the stale 5.
  - Verified: both reported cases are real. `SetLimit` alone during a lookup
    was not lost on the old code (its two cases passed there): it puts the
    group in memory, and `vivifyGroup` never replaces a group in memory. It is
    lost only if the group is forgotten again before the lookup returns.
  - Cause: `Limiter.lockWithResolvedGroups` (`limiter/limiter.go`) calls the
    callback with `mu` released, then re-locks and treats the result as
    current, though a `RemoveLimit` or `SetLimit` may have run in between.
  - Fix: `limiter/limiter.go`: a `limitChanges` counter on the `Limiter`,
    bumped under `mu` by every `SetLimit` and `RemoveLimit`. Each pass of the
    resolution loop snapshots it before unlocking; if it changed by the time
    the lock is retaken, every resolution is discarded and looked up again.
    One counter for all groups keeps no state per group name; limits change
    rarely, so the extra lookups are rare. The loop repeats only while limits
    keep changing during its lookups.
  - Tests: `limiter/limiter_test.go`: new `TestLimiterLimitChangedDuringLookup`
    (remove during `GetLimit`, remove during `Increment`, remove during an
    `Increment` naming another group first, set during `GetLimit`, set during
    `Increment`, set then forgotten during `GetLimit`), using a
    `heldLookupDB` callback that blocks its first lookup of `g` on a
    channel, so the test is deterministic.
  - Mutants, each in a scratch copy, each failing the new test plain: no
    discard after a change (all four lines on the base); discarding only the
    first group's lookup, `resolved[0] = resolution{}` (407; added after
    review, when it survived the single-group cases); `RemoveLimit` not
    bumping the counter and `SetLimit` not bumping it (the remove cases and
    the set-then-forgotten case respectively); snapshot taken after
    re-locking, and discarding only the in-memory group pointers, not the
    looked-up data (each failing the single-group remove cases; run before
    the later cases were added).
  - Green: the red command exits 0 plain and `-race` (`-count 20`); the
    whole `./limiter` package exits 0 plain and `-race`;
    `TestJobqueueLimitGroups`, `TestLimitGroupReport`,
    `TestLimitSetOnRunningGroup`, `TestLimitAddKeepsNewerLimit`,
    `TestReliable3LimitGroupOverProvision`, `TestReliable4LimitGroupsNoWrite`,
    `TestReliable4LimitGroupRemoval` and
    `TestJobqueueSuspendResumeLimitGroups` exit 0 plain and `-race`.
    `golangci-lint run ./limiter/` 0 issues; `cleanorder -min-diff` on the
    edited Go files.
  - Benchmarks: test binaries for `29e8927c` and this fix, run alternately
    10 times, `-test.bench BenchmarkLimiter -test.benchmem`, benchstat:
    `LimiterIncDecUnlimited` 2.091µs to 2.069µs (~, p=0.953);
    `LimiterIncDec` 2.283µs to 2.219µs (~, p=0.315); `LimiterCapacity`
    1.968µs to 1.977µs (~, p=0.796). Allocations unchanged; B/op +8 in each,
    the 8-byte larger `Limiter` that each benchmark iteration creates with
    `New`, not a per-call cost.
