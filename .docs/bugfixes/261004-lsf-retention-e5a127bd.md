# LSF per-runner retention (2026-10-04)

- Branch: `lsf-retention-e5a127bd`
- Base: `origin/develop` at `b5caf8c4` (#669)
- Queue owner: `checklist-tidy-8e94211c`, `.docs/bugfixes/261004-checklist-tidy-8e94211c.md`
  (Delivery queue item 2); worktree `../wr-lsfretention`
- Source: `260927-queue-heap-retention.md`, "Not fixed, recorded for
  follow-up", and the Backlog of `261004-checklist-tidy-8e94211c.md`
  ("QUEUED, LSF part only, measure first").

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2`, `GOCACHE` outside the home
directory and `WR_LSF_TEST_KEY` unset (no real LSF jobs): `go test -tags netgo`
of `./jobqueue/scheduler` (skipping `^TestLSF$` and
`TestReliable4ConfirmDeadSSHLeak`, which need a real LSF or ssh),
`golangci-lint run` on that package, and `cleanorder -min-diff` on the edited
Go files. The caller runs `make test`, `make race` and `make speed`.

- [x] LSF `reservedElements` is pruned only at shutdown (`pruneReserved`,
  reached only from `busy()`). `doomedElements` keeps a finished group's
  doomed ids because its prefix is never scanned again. Both are about 100B
  per runner, and pruning them needs care so that `killExcessCmds` does not
  kill a reserved runner.
  - Measured first, with a throwaway test driving `lsf.schedule` and
    `claimForReserve` against a fake `bjobs -w` the way the manager does:
    groups of 100 runners (each a different cmd) are scheduled with 10 excess
    PEND elements, which are doomed and bkilled; the 100 claim reservations;
    the group's final count-0 pass still sees them RUN and the excess PEND;
    then all finish and that cmd's prefix is never scanned again. Heap is
    `HeapInuse` after two `runtime.GC()`; each structure's share is the drop
    when it is cleared.

    | Finished runners | Total heap | reservedElements | doomedElements | killDeferred |
    | ---: | ---: | ---: | ---: | ---: |
    | 1,000 | 336 KB | 1,000 ids, 90 KB | 100 ids, 106 KB | 100 ids, 66 KB |
    | 10,000 | 1.38 MB | 10,000 ids, 745 KB | 1,000 ids, 221 KB | 1,000 ids, 360 KB |

    So about 75B per finished runner in `reservedElements` and about 220B per
    killed excess element of a finished group in `doomedElements`, growing
    with N for the manager's lifetime. At 20k runners a day that is about
    45MB a month from `reservedElements` alone. `killDeferred` is not a leak:
    `sweepKillDeferrals` drops an entry 2 x `killBackoffMax` (10 minutes)
    after its deadline. Nothing else in `lsf.go` is per runner (`queues`,
    `months` and `sortedqs` are fixed at start-up). The growing set also cost
    time: `killExcessCmds` copies all of `reservedElements` every scan.
  - Red: `go test -tags netgo --count 1 ./jobqueue/scheduler -run TestLSFRetention`
    exit 1:

    ```text
    Expected: 0
    Actual:   1000
    Expected: 0
    Actual:   250
    --- FAIL: TestLSFRetentionFinishedRunners
    ```

  - Cause: the only prune ran from a full `bjobs` scan, and only `busy()` at
    shutdown asks for one. Per-cmd scans could not prune `reservedElements`,
    which is not grouped by prefix, and a doomed id is forgotten only by a
    later complete scan of its own prefix, which never comes once its group
    has finished.
  - Fix (`jobqueue/scheduler/lsf.go`, `lsf_doomed.go`): at most once per
    `reservedPruneInterval` (1 minute), `killExcessCmds`' scan (new
    `scanForExcess`) parses all of this deployment's jobs instead of one
    cmd's, still handing the collector only that cmd's, and a complete scan
    then forgets the reserved and doomed ids it did not report. `bjobs -w`
    already lists all the user's jobs, so this adds only parsing. The first
    prune comes one interval after the first scan. Only ids recorded before
    the scan began may be pruned (`pruneSnapshot`), since an element claimed
    or doomed while `bjobs` ran may postdate LSF's answer. The shutdown prune
    in `countCmds` now uses the same snapshot rule. The sets now hold the live
    runners plus those that finished in about the last minute.
  - Tests (`jobqueue/scheduler/lsf_retention_test.go`):
    `TestLSFRetentionFinishedRunners` (the measurement as a bound: zero ids
    left after a later pass, with a zero and a non-zero interval) and
    `TestLSFRetentionPruneSparesLiveElements`: a reserved element `bjobs`
    still reports is kept by another group's pruning pass and not bkilled by
    its own group's excess kill; a failed pruning scan keeps reservations; an
    element claimed during the pruning scan is not bkilled afterwards; an
    element doomed during it is still refused a claim.
  - Mutants (scratch copy, each run against the new and existing
    reserved/doomed tests): no prune in `scanForExcess` (FinishedRunners
    fails); pruning never due with a non-zero interval (FinishedRunners);
    pruning from current `reservedElements` instead of the snapshot,
    `present` from the cmd's prefix only, forgetting doomed ids outside the
    snapshot, deleting every snapshotted reserved id, and pruning after a
    failed scan (PruneSparesLiveElements fails for each). All killed.
  - After: same harness with the prune applied, 1,000 and 10,000 finished
    runners leave 0 reserved and 0 doomed ids.
  - Speed (scratch benchmark, `killExcessCmds` of one cmd over a 20,000-line
    fake `bjobs -w` of 200 cmds, `-count 6 -benchtime 50x`, medians on a
    loaded host): base 30.5ms, 5.60MB, 40,445 allocs; fix without a due
    prune 31.9ms, 5.60MB, 40,445 allocs (same work, noise); a pruning scan
    46.9ms, 8.43MB, 80,621 allocs, at most once a minute. Base with 100,000
    leaked reserved ids (a few months at prod rates) 54ms and 9.08MB on every
    scan.
  - Gates: scheduler package tests pass (exit 0); `golangci-lint run
    ./jobqueue/scheduler/...` 0 issues; `cleanorder -min-diff` applied.
