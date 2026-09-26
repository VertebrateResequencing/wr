# Test suite and CI speed (2026-09-26)

Findings and changes from speeding up `make test`, `make race` and the
`tests` workflow, measured against develop at 4cd2ee0.

## Where the time went

- CI ran `make test` (about 8 min) and then `make race` (about 12.5 min) in
  one job, so every push took 20-22 min.
- In both modes one lane, `jq_default`, took 6-8 min on CI and 7.5-9.5 min
  locally. Every other lane took about a minute or less. It ran over 380
  jobqueue tests in one process, so it alone set the wall time while the other
  three slots sat idle for most of the run.
- Lane priorities were not honoured. The runner started one goroutine per lane
  and each blocked on a semaphore. Goroutines acquire a semaphore in no
  particular order, so start order was effectively random. The weights table
  also missed `jq_default` and `cmd_default`, the two longest lanes.
- The suite is mostly waiting, not computing. Baseline `make test` used 4m19s
  of user CPU in 9m22s on 4 CPUs (about 12% utilisation).
- setup-go's cache key is only the Go version and the go.sum hash, and the lint
  workflow uses the same key. The lint job finishes first and saves it: the
  develop cache for the current key was saved about a minute after the run
  started. The test job then restored lint's cache and compiled its
  dependencies from scratch in both modes. Locally on 4 CPUs a cold build cache
  costs about 64s more for the test binaries and 75s more for the race ones.

## Changes

| Change | Measured effect |
| --- | --- |
| Run `make test` and `make race` as parallel matrix jobs | CI wall time becomes the slower job instead of the sum |
| Cache modules and build per target with actions/cache | Up to about 1 min of compile per job, once the cache is warm |
| Split `TestReliable2`, `TestReliable4` and `TestDepGranularity` into their own lanes | Critical path fell from `jq_default` (449 lane-s) to under the 4-slot average |
| Start lanes longest first, from an ordered worker pool | Long lanes no longer start last |
| `TestReliable2StatusFeedNeverDrops` naps per 50 deltas | 55s to 1.6s |
| `TestKillingALostJobSparesTheRunThatReplacesIt` holds the retry's TTR across its Started | No time saved; stops a flake the split exposed |

The CI split keeps both suites' coverage: the CGO_ENABLED=0 netgo run and the
race run each keep their own job with the same setup steps. A job named `test`
still reports, because branch protection requires that check. It fails unless
both targets passed and runs even when one fails, since a skipped required check
counts as passing.

The lane split and the ordering are one measurement, as the ordering only
matters once no single lane dominates.

## Local measurements

These are `time make test` and `time CGO_ENABLED=1 make race`, both under
`taskset -c 0-3` with `WR_TESTSUITE_MAX_PARALLEL=4` and a warm build cache.
This host sets `JOBQUEUE_REMOTES3_PATH`, so each run also includes the serial
`jobqueue_mounts` lane (about 10s), which CI does not run.

| Run | Before | After |
| --- | --- | --- |
| `make test` | 9m22s, 8m31s, 8m36s | 3m53s, 3m24s, 3m18s |
| `make race` | 11m26s | 4m52s, 4m44s, 4m39s |

All of these passed. Two more `make test` runs after the change failed in about
3m01s on the lost-job test described at the end, which is now fixed.

After the change the suite is throughput bound. In race mode the lanes add up
to 1038 lane-s, an average of 260s over 4 slots. The longest lanes are
`jq_reliable4` and `jq_default` at about 195s each. More lane splitting will
not help. Only less total work, or more slots, would.

## Estimated CI wall time

This scales the local ratios onto the last CI run, 36264990061 (setup 27s,
`make test` 8m00s, `make race` 12m34s).

| | Before | After (estimate) |
| --- | --- | --- |
| `make test` job | - | about 4 min |
| `make race` job | - | about 5-6 min |
| Workflow wall time | 21-22 min | about 6 min |

The first run after a go.mod or go.sum change misses the new cache and builds
cold, adding about a minute per job.

## Slowest tests

The top 20 before the change, from local `make test` lane logs, with what was
done:

| Test | Lane | Time | Decision |
| --- | --- | --- | --- |
| TestDepGranularityRunnerSurvivesLongAbsence | jq_default | 60.5s | Takes 2s. The 60s was the stall described below |
| TestReliable2StatusFeedNeverDrops | jq_default | 57.7s | Fixed, now 1.6s |
| TestJobqueueProduction | production | 20.4s | Real work, left |
| TestLSFArrayChunking | scheduler | 17.5s | Real work, left |
| TestJobqueueExecutionAndDependencyScenarios | jq_execution_details | 16.5s | Already sharded, left |
| TestSubscriptionReconnectDuringManagerShutdown | jq_default | 16.5s | Deliberate shutdown timings, left |
| TestJobqueueModify | modify_a | 15.8s | Already sharded, left |
| TestSelectionByCmdLine | cmd_default | 15.0s | No fixed sleeps, left |
| TestJobqueueSignal | signal_a | 14.7s | Already sharded, left |
| TestStatusCountReconcile | jq_default | 13.2s | Hang-detector timeout only, left |
| TestResumeCommand | cmd_resume | 13.2s | Own lane, left |
| TestScheduler | client_a | 13.0s | Own lane, left |
| TestServerWebI | server_webi | 11.4s | Own lane, left |
| TestAddPrintsDuplicateBreakdown | cmd_default | 10.6s | Connect timeouts are bounds, left |
| TestSuspendCommand | cmd_suspend | 10.5s | Own lane, left |
| TestReliable4PreStartReleaseRetries | jq_default | 10.1s | Real retries, left |
| TestJobqueueWithMounts | jobqueue_mounts | 9.7s | Live S3, serial by design, left |
| TestReliable4ExecImpossibleBuriedFirstAttempt | jq_default | 9.1s | Real work, left |
| TestReliable2FastStartupNoHistoryScan | jq_default | 8.9s | Timing-ratio test, left |
| TestClientExecuteLiveTouchPayloads | jq_payload | 8.9s | Own lane, left |

In race mode `TestReliable4StatusLimitPushdown` (46s),
`TestReliable2FastStartupNoHistoryScan` (41s),
`TestReliable4ControlPathsSkipArchivedHistory` (21s) and
`TestReliable4StatusUnboundedHistoryBudget` (20s) are the slowest. Each needs a
large history, because an O(history) path has to be unmistakable next to an
O(limit) one, and the race detector makes decoding it several times slower.
Shrinking the history would weaken what they prove, so they were left.

## Deliberately not changed

- `WR_TESTSUITE_MAX_PARALLEL=4` on CI. The suite uses about 40% of 4 CPUs in
  race mode after the change, so more slots would probably shorten it further,
  but load-sensitive tests are the suite's main source of flakes.
- The serial lanes (`jobqueue_mounts` locally, `queue` in race mode, and the
  live OpenStack lanes) stay serial. They cost about 10s and 4s.
- Timings that tests exercise on purpose: shutdown windows, TTRs, retry budgets,
  startup-time ratios and hang detectors.
- No test was skipped, loosened or given a smaller workload.

## Load-sensitive tests exposed by the split

Before the change most `jq_default` tests ran with the other slots idle,
because every other lane had finished. They now share the machine with three
busy lanes. Two load-sensitive tests showed up. Both also fail at the unchanged
code under synthetic load.

- `TestKillingALostJobSparesTheRunThatReplacesIt` (lost_job_behaviours_test.go)
  failed twice in five `make test` runs after the change: Expected `running`,
  Actual `lost`, at the final state check. It passed 20 of 20 alone and failed
  5 of 88 as four processes on two CPUs. By the time the test reports the
  retry's Started, the retry's own 1s TTR has expired, so it is lost and the
  Started is what takes it off lost. Nothing touches it afterwards, so the next
  1s expiry could mark it lost again before the check read it: in every failure
  the Started had applied (raw state `running`, new pid recorded). The test now
  waits for the retry to be lost, holds its queue item's TTR open for 20s, and
  keeps the original assertion that Started leaves it `running`. It passed 100
  of 100 under the same load and 20 of 20 race runs, and still fails if Started
  stops clearing the Lost flag.
- `TestSubscriptionReconnectDuringManagerShutdown`, in the Convey "The
  unsubscribe cleaning up a rejected replacement is bounded by the retry
  budget". Under load `pingUntilUnread` sometimes spends 60s, the
  ClientMinRequestTimeout floor, on one ping. Its doc comment says that happens
  when a ping goes out after the command socket closes and blocks on the send
  deadline. The Convey then usually still passes, because a 60s ping counts as
  unread, but it costs a minute. Sometimes it fails its `took < 1s` bound, with
  `1.000128984s`. It failed the same way on CI before this change (a CI run
  of PR #620 has `1.000096513s`), and open PR #622 works on this
  shutdown timing, so it was left alone. The same 60s stall hit
  `TestDepGranularityRunnerSurvivesLongAbsence` in the baseline run. Reproduce
  with three copies of
  `taskset -c 0-1 jobqueue.test -test.count 4 -test.run
  '^TestSubscriptionReconnectDuringManagerShutdown$'`, each with its own
  `WR_TEST_LANE`.
