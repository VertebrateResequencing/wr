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
- [ ] **The warning storm during slow commits.** The mutex profile puts 57.8%
  of contention on the log15 handler lock, reached from the per-reservation
  "reservation not yet recorded on disk, handing the job out anyway" warning
  (jobqueue/serverCLI.go around :1112). There were 55k of these warnings,
  plus 175k slow-request warnings, in 16 minutes of slow commits.
- [ ] **Optional, investigate only.** `buildSchedulerGroups` (via
  readyAddedCallback) grew from 2.7s to 8.7s CPU per 30s as the backlog grew.
  Is it O(backlog) per ready-add?
