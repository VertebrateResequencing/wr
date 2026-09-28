# Test suite speed, second round (2026-09-28)

Follows [testsuite-speed-260926.md](testsuite-speed-260926.md). Measured
against develop at 8cb5ff5f.

## Method

- Every run is `make test` or `CGO_ENABLED=1 make race` under
  `taskset -c 0-3` with `WR_TESTSUITE_MAX_PARALLEL=4` and a warm build cache,
  as in the first round. `JOBQUEUE_REMOTES3_PATH` was unset, so these runs
  match CI and include no serial `jobqueue_mounts` lane.
- A run started only when the 1-minute load was under 10. Every timed run below
  started with it between 2 and 5 on this 8-CPU host.
- Lane start and end times come from the birth and last-modified times of each
  lane's log, hard-linked out of the suite's temp dir while it ran. "Lane-s" is
  the sum of the lanes' durations.
- Some baseline runs are left out: one `make test` that ran while tests were
  being profiled on the other CPUs (265s), and the cold-cache first run of each
  mode.

## Where the time went

- Both modes were throughput bound, not bound by one lane. In `make test` the
  lanes added up to 761 lane-s, 190s over 4 slots, and the lane phase took
  191s. In `make race` they added up to 980 lane-s, 245s per slot, and took
  257-260s. The first round's doc said the same of race mode.
- The suite still mostly waits. Baseline `make test` used 128% of 4 CPUs, and
  `make race` 190%.
- A CPU profile of the race `jq_default` lane put 27s of its 112s of CPU in
  `rsa.GenerateKey`. Every jobqueue, cmd and client test fixture that made a
  fresh manager dir made two new 2048-bit keys, about 150ms each time.
- Every cmd test server took about 1.05s to stop, whatever the test did. Its
  command socket readers block in `RecvMsg` for the 1s default `InterruptTime`
  before they notice the server stopping. `TestSelectionByCmdLine` starts 12
  servers, so 12 of its 16s were shutdown. jobqueue's and client's fixtures
  already set 10ms.
- A handful of tests spent most of their time on setup they repeated or on
  work done one item at a time, listed under Changes.
- Warm, the time before the first lane starts is 5-10s, so compiling is not
  worth attacking locally.

## Changes

One commit each, in this order. Lane and test timings were measured on their
own, as noted. The full-suite timings are in the next section.

- [x] cmd test servers use a 10ms `InterruptTime`. The 5 cmd lanes, on 2 CPUs:
  97.7s to 32.2s, with the same 88 tests and 5038 assertions passing.
- [x] Test fixtures copy one set of TLS files per process, from
  `internal/testcerts`, instead of making new RSA keys. The jobqueue, cmd and
  client lanes, run two at a time on 2 CPUs each: 545s to 473s, with the same
  505 tests passing. `testcerts.Write` refuses to overwrite, as `GenerateCerts`
  does. Tests that check certificate generation, or need a CA that differs
  from another server's, still call `GenerateCerts`. The cmd fixture has one
  more `So` per server, so its lanes count 73 more assertions.
- [x] `jq_default` is split into `jq_default_a_k`, `jq_default_l_r` and
  `jq_default`, by the letter after `Test`. `jq_default` keeps S-Z and any name
  outside the ranges, and a planner test checks that every jobqueue test still
  runs in exactly one lane. It moves no work, and at the new totals it saves no
  time locally: with it reverted, `jq_default` took 143s of a 145s lane phase
  in `make test`, level with the per-slot average. It keeps the leftover
  jobqueue tests, which every new jobqueue test joins, off the critical path as
  they grow.
- [x] `TestStatusCountReconcile` runs the harness's seeds as 4 concurrent node
  shards: 13.2s to 3.9s. `reconcile-harness.mjs --shard=i/n` runs only the
  seeds with `s % n == i`. Every scenario's pass condition is a maximum or a
  count over its seeds, so a run passes exactly when all its shards pass. The
  handlers from before #552 and #561 still fail, in some shard, every scenario
  that they fail unsharded.
- [x] The status-limit tests fetch their unbounded references once. In race
  mode, `TestReliable4StatusLimitPushdown` fell from 41.6s to 21.0s and
  `TestReliable4StatusUnboundedHistoryBudget` from 18.6s to 12.3s. GoConvey
  reruns a test's setup for every leaf, so each leaf seeded a new manager and
  decoded the whole history for the reference fetch. The history is the same
  every time, so the fetch, and the check of its error and decode cost, now
  happen on the first manager. Each leaf still checks the references' size and
  first job, and still measures its own decodes as a delta. The two tests count
  20 and 12 fewer assertions, as the fetch's error check no longer repeats per
  leaf.
- [x] `TestServerArchivedJobsLeaveTheHeap` archives from 10 clients at once:
  34s to 8s with another suite on the same disk, and 22s to 12.6s in the race
  suite. Each state change is its own fsync'd commit unless others are pending
  to fold into it, so one client doing 1200 of them in turn was fsync bound.
  With #633's queue and server fix reverted, the test still fails, with 24MB of
  growth.
- [x] cmd test servers use jobqueue's 1ms `ShutdownSocketWait`. The 5 cmd
  lanes, on 4 CPUs: 19.0s to 15.6s, the mean of two runs each.
- [x] `lanesLongestFirst` is re-sorted by the new lane durations. This moves no
  work.
- [x] The lost-run fixture waits for a stopped manager's lost-run behaviours
  before the next fixture writes the hooks they read. See Flakiness results.

## Full-suite timings

Baseline is 8cb5ff5f. "After" is 81fdab16, the final commit, three runs of
each mode.

| | Baseline | After |
| --- | --- | --- |
| `make test` wall | 196.9s | 152.8s, 151.1s, 152.8s |
| `make test` lanes | 761 lane-s, 191s phase | 577-587 lane-s, 145-147s phase |
| `make test` CPU | 209s user, 128% | 122s user, 105% |
| `make race` wall | 269.8s, 267.0s | 216.2s, 211.3s, 211.7s |
| `make race` lanes | 973-986 lane-s, 257-260s | 748-755 lane-s, 201-203s |
| `make race` CPU | 441-450s user, 190% | 315s user, 174% |

After the changes the longest lane is `jq_reliable4`, at 83s in `make test`
and 142s in `make race`, under the per-slot averages of 146s and 188s. Both
modes are still throughput bound.

## Expected CI wall time

This scales the local ratios, 0.77 for `make test` and 0.79 for `make race`,
onto CI run 36356822511 of 8cb5ff5f.

| | Before | After (estimate) |
| --- | --- | --- |
| `make test` step | 3m13s | about 2m30s |
| `make race` step | 4m41s | about 3m40s |
| Workflow wall time, set by the race job | about 5m10s | about 4m10s |

## Flakiness results

- At 81fdab16, three `make test` and three `make race` runs all passed. So did
  three of each at 04bdb02f, the commit before.
- One `make test` at 04bdb02f took 166.8s, because it hit the 60s ping stall
  the first round's doc describes, this time in
  `TestDepGranularityRunnerSurvivesLongAbsence`. It passed.
- **A data race exposed by faster setup.** A `make race` of the branch with
  the `jq_default` split reverted failed on a race report in
  `TestLostJobBehavioursSpareARecoveredJob`. The lost-run fixture's `stop()`
  returned while the stopped manager could still be running a lost run's
  behaviours, which read the package-level hooks that the next fixture's
  `installHooks` writes. The race is in 8cb5ff5f too. Running the `TestLost`
  and `TestKill` tests as four race processes on 4 CPUs, 10 times each, it hit
  3 of 400 runs at 8cb5ff5f and 12 of 400 at 04bdb02f, since fixtures now
  start about 150ms sooner. `stop()` now takes every lost-cleanup token of the
  stopped server, which waits for running behaviours to finish, with a 1
  minute hang detector. At 15 runs each, the fixed binary passed 600 of 600
  with no race reports, and 8cb5ff5f failed 1 of 600.
- **Under `stress -c 16`**, on all 8 CPUs, with the suite pinned to 4 of them,
  `make test` passed in 243s. `make race` failed in 363s, with the 1-minute load
  at 29, on two tests:
  - `TestSubscriptionReconnectDuringManagerShutdown`, the load-sensitive
    shutdown test in the first round's doc. Its reconnect did not end in
    `ErrRecvTimeout`. Run alone 8 times under the same stress, it failed 2
    times at 8cb5ff5f and 1 time at 81fdab16, so it predates these changes.
  - `TestKillRacingCmdExitKeepsTouching`, where the kill arrived before the
    command started. Run alone 40 times under the same stress, it passed every
    time at both 8cb5ff5f and 81fdab16. Nothing here changes it or the code it
    tests, but the one failure is recorded.

## Deferred

These are in files that open PRs change, or need production changes, so they
are left for later.

- **Bind retry budget.** `serverBindRetryBudget` is a 5s constant in
  `jobqueue/server.go`. It sets the time of
  `TestStartStatusTestServerRetriesTakenPort` (5.2s),
  `TestServeFailsCleanlyWhenPortTaken` (5.4s) and
  `TestDepGranularityStartupExitsWhenPortUnavailable` (5.6s). Making it a
  `ServerTimings` field would let those tests lower it. #641 reworks this path.
- **bjobs appearance poll.** `pollForBjob` in `jobqueue/scheduler/lsf.go`
  waits a whole `bjobsAppearPollFreq` (100ms) tick before its first poll, and
  `TestLSFArrayChunking` submits 160 arrays, so 16 of its 17.5s are that wait.
  Polling once before the ticker would also cut real submission latency. #641
  changes `lsf.go`.
- **Shutdown subscription test.**
  `TestSubscriptionReconnectDuringManagerShutdown` takes 23-29s, and sometimes
  hits the 60s ping stall. It is in `jobqueue/subscription_test.go`, which #634
  changes.
- **CI build cache key.** The key is only the go.mod and go.sum hash. After
  the first save each later run restores that same entry and saves nothing, so
  wr's own packages are compiled from scratch on every run. A key per commit,
  with the current key as a restore prefix, would keep the cache current but
  adds an upload to every job. It needs measuring on CI.
- **CI parallelism.** Both modes are throughput bound with half the CPU idle,
  so more than 4 slots would probably be faster. It was left alone for the same
  reason as in the first round.
