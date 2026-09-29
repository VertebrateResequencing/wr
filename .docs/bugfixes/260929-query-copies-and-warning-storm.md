# 260929: prodsim round 4 query copies and warning storm

Branch `fix-query-copies-and-warning-storm`, based on `origin/develop` at
`83d2f693` (#646). Evidence is from prodsim round 4 at about 570k live jobs,
in `/nfs/hgi/wr/sb10-bigdb/soak4/run/prodsim-1790629666/`.

- [x] **Prefix and state queries copy the whole live queue before
  filtering.** `getQueueJobsByRepGroupMatch` (jobqueue/server.go around
  :7129) and `getAllQueueJobs` (around :7113) copy every item with
  itemToJob/copyJobForClient first. One in-flight handleGetIncomplete held
  3.05GB (29% of heap). `find_incomplete_prefix` peaked at 113s,
  `status -b --limit 1` had a p95 of 71s, and `status_fofn_z` had a p95 of
  28.7s.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run
    TestLiveQueriesCopyOnlyReturnedJobs` exited 1. Over 10,000 unrelated live
    jobs the result assertions passed, but a prefix query made 30617
    allocations, a `--limit 1` buried query 31224, and a limited buried
    `getJobsByRepGroup` on a 1,200-job report group 4222 (bound 1000).
  - Callers checked: `getJobsCurrent` (handleGetIncomplete, the REST jobs
    list, and the running and lost lookups) walked the whole queue for a blank
    or non-exact report group, and `getJobsByRepGroup` (`wr status -i`, with
    or without `-z`) copied every live job of each matching report group.
    `queuedJobsByKeys`, `inputToQueuedJobs` and the subscription catch-ups
    copy only jobs they return. `statusSeedCounts` and the status summaries
    do not copy.
  - `jobqueue/server_live_filter.go`: `liveJobFilter` decides from the live
    job, under the read lock the copy is taken under, what `limitJobs` would
    decide from its copy: the report group match, `jobMatchesFilters`, and,
    with a limit, `addJobToGroup`'s Offset+Limit per group. Jobs beyond that
    are counted, not copied, and the counts go to `countUndecodedJobs`, as
    the archived budget's already do, so Similar counts are unchanged.
  - `jobqueue/serverCLI.go`: `itemToJobIfAdmitted` copies only admitted jobs;
    `itemToJob` is it with no filter. `jobqueue/server.go`: `getJobsCurrent`
    and `getJobsByRepGroup` pass a filter; the two whole-queue walkers are one.
    With `GetStd`, buried jobs beyond the limit no longer each read their
    output from the database.
  - After: 12, 30 and 32 allocations. A throwaway benchmark over 300k live
    jobs: prefix query 772ms and 350MB per call before, 182ms and 2.4MB
    after; `--limit 1` buried 587ms and 350MB before, 159ms and 2.4MB after.
  - `jobqueue/live_query_copy_test.go` also checks every result against the
    old copy-everything-then-limit algorithm, including an offset.
  - CHANGELOG: Fixed entry.
- [x] **The warning storm during slow commits.** The mutex profile puts 57.8%
  of contention on the log15 handler lock, reached from the per-reservation
  "reservation not yet recorded on disk, handing the job out anyway" warning
  (jobqueue/serverCLI.go around :1112). There were 55k of these warnings,
  plus 175k slow-request warnings, in 16 minutes of slow commits.
  - The slow-request warnings have the same shape: in the round 4 logs they
    are 256k `jstart` and 229k `jarchive`, then 7.6k `reserve`, with fewer
    than 1,000 of every other method.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run
    TestWarningStormIsAggregated` exited 1. With the reservation write
    stalled, 20 reservations logged 20 full reservation warnings, and with
    the slow-request threshold at 1ns, 50 identical `getin` requests logged
    50 slow-request warnings (expected 1 each).
  - `jobqueue/warn_aggregator.go`: `warnAggregator` logs the first
    occurrence per key in full at once, then only counts that key until its
    interval (`warnAggregateInterval`, 1 minute) ends. A timer then logs one
    `<msg> (repeated)` line with `repeats`, `since`, `interval`,
    `maxDuration` where one was given, and the latest occurrence's fields
    prefixed `sample_`, so a sample job key is kept. Shutdown logs any open
    window's summary. A nil aggregator, as in bare test servers, logs every
    occurrence.
  - `jobqueue/serverCLI.go`: `persistReservation` warns through the
    server's aggregator under one key. `warnIfSlowRequest` warns under a key
    of method plus selector, so each distinct query shape (a rare `getbr`
    for one report group, say) is still logged in full once a minute.
    `jobqueue/server.go` builds the aggregator in `Serve` and stops it in
    `shutdown`.
  - After: the red test logs 1 full reservation warning plus a summary with
    `repeats=19` and a `sample_key`, and 1 full `getin` warning plus a
    summary with `repeats=49` and `maxDuration`. `TestWarnAggregator`: 50
    goroutines x 200 occurrences give 1 line plus 1 summary with
    `repeats=9999`. It also covers per-key windows, rollover, the timer
    flush and the nil aggregator.
  - `jobqueue/reliable4_slow_request_test.go`: calls pass a nil aggregator
    and keep their assertions.
  - CHANGELOG: Fixed entry.
- [x] **Optional, investigate only.** `buildSchedulerGroups` (via
  readyAddedCallback) grew from 2.7s to 8.7s CPU per 30s as the backlog grew.
  Is it O(backlog) per ready-add?
  - Yes, per ready-added callback cycle, not per job added. The queue
    coalesces adds, so there is one cycle at a time. Each cycle is passed every
    ready item, and `buildSchedulerGroups` snapshots each one under its read
    lock, sorts them by priority when any has a limit group, and checks each
    against its limit-group budget. Under steady adds the cycles run back to
    back, so CPU grows with the backlog. In
    `profiles/spike.009140.cpu.pprof` it had 7.13s of a 30s sample:
    `snapshotReadyJobs` 4.43s (mostly the per-job RLock and reading the job,
    which is memory-bound at this size), `scheduleReadyJobsByPriority` 2.70s,
    of which `seedLimitGroupBudgets` was 1.52s, nearly all `strings.Split` in
    `schedGroupToLimitGroups`, re-parsing the same few scheduler group
    strings for every job. The sort was 0.33s.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run
    TestRACParsesLimitGroupsOncePerGroup` exited 1. A cycle over 10,000
    limit-blocked ready jobs in 10 scheduler groups made 20029 allocations
    (bound 1000).
  - Cheap, safe fix: `jobqueue/server.go` `scheduleReadyJobsByPriority`
    keeps a per-cycle map from scheduler group to its parsed limit groups,
    which `readyJobLimitBlocked` uses. The budgets and their order are
    unchanged. After: 52 allocations. A throwaway benchmark over 200k
    limit-blocked ready jobs went from 103ms, 24MB and 400k allocations per
    cycle to 86ms, 14MB and 52 allocations. That removes the ~1.5s split cost
    seen in production, about a fifth of the callback's CPU.
  - Not fixed: the per-cycle O(backlog) snapshot and walk. Removing it would
    mean keeping per-scheduler-group counts up to date on every queue
    transition instead of recounting, which is not a cheap or safe change.
  - Test files: `live_query_copy_test.go` now uses `testCwd`, because a third
    `"/tmp"` literal tripped goconst.
  - CHANGELOG: Fixed entry.
