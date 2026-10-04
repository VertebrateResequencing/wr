# Soak9 follow-ups (2026-10-04)

- Branch: `soak9-followups-906fadf9`
- Base: `origin/develop` at `b5aa49a7` (#672)
- Queue owner: this branch and this checklist
- Source: the production-scale LSF crash soak "soak9" of develop `55cc2565`.
  Evidence, read only, is under `/nfs/hgi/wr/sb10-bigdb/soak9/`; its run
  directory `run/prodsim-1791103425` is called `$O` below. Each
  `$O/manager.log.<epoch>` is a copy of the whole manager log taken at a
  restart, so counts below come from one copy.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2`, `GOCACHE` outside the home
directory and `WR_LSF_TEST_KEY` unset: targeted `go test -tags netgo` runs of
the touched tests (plain and `-race`), `golangci-lint run` on the touched
packages, and `cleanorder -min-diff` on the edited Go files. The caller runs
`make test` and `make race`.

- [x] 1. A scheduler group whose jobs are all gone keeps being scheduled while
  its bsub keeps failing. The soak agent's first probe job had no queue, so wr
  picked queue tiger22-inference, whose esub refused bsub (needs a -G
  cost-code group). The job was removed at 09:52:47, but the manager kept
  retrying bsub for that now-empty scheduler group for ~16 minutes until a
  crash restart at 10:09: ~557 "scheduling runners error" and ~132
  "persistently failing" lines.
  - Evidence (`$O/manager.log.1791104981`, group
    `2001:30:1:0:6a9df116ae2cb47fa4adcb70142346f2`): 689 lines, all from
    `scheduleRunners`. The Warn "scheduling runners error" lines run from
    09:44:45 to 09:53 at about 500-680 a minute; the Error "persistently
    failing" lines start at 09:47 and carry on to 10:08:43, 15 minutes after
    the job went. By `consecutiveFailures`: 411 at 1, 146 at 2, 57 at 3, 40
    at 4, 32 at 5 and 3 at 6. So each rac pass's first attempt failed, and
    hundreds of separate retry chains each counted their own failures.
  - Red: `env -u WR_LSF_TEST_KEY go test -tags netgo -count=1 -run
    TestScheduleRetryStopsWhenGroupEmptied ./jobqueue/` exits 1 before the fix:
    with a mock scheduler that fails every request for more than 0 runners,
    the group's only job is deleted, and in the following 3s
    (after 0.5s to settle) the scheduler is still asked for runners
    `Expected: 0 / Actual: 12`, logged as "persistently failing".
  - Cause: every failed schedule attempt started its own retry goroutine
    (`retryScheduleRunnersLater`) holding the snapshot it was given, and
    retried that snapshot's count until an attempt succeeded. rac schedules a
    fresh snapshot of every group on every pass, and an archive or delete
    schedules another, so while bsub failed each pass added a retry chain, all
    asking for the count at the time they started. Removing the job decremented
    the live group and sent the scheduler 0, and the next rac pass dropped the
    group, but nothing told the chains: each kept asking for 1 runner, with a
    backoff that grows to 30 minutes, so only a restart stopped them.
  - Fix (`jobqueue/server.go`): a retry asks for the group's current count
    (`currentGroupCount`: its count in `previouslyScheduledGroups`, or 0 once
    the group is no longer scheduled), so it asks for 0 once the jobs are gone,
    which needs no bsub and ends the retrying. Only one retry loop runs per
    group name (`claimScheduleRetry`/`releaseScheduleRetry`); a failure while
    one is pending leaves the retrying to it, as it asks for the then-current
    count anyway. The loop keeps its backoff and its failure count across
    retries, so the Warn-to-Error escalation now means consecutive failures
    of the group. `scheduleRunners` is split into `attemptScheduleRunners`
    (one attempt, returns whether to retry) and the retry, with the logging in
    `logScheduleFailure`. A group that still has jobs is still retried.
    The mock scheduler's `ScheduleError` now receives the count asked for
    (`jobqueue/scheduler/mock.go`), so a test can fail only requests that would
    run bsub.
  - Tests (`jobqueue/schedule_retry_backoff_test.go`):
    `TestScheduleRetryStopsWhenGroupEmptied` (the red, through a real server)
    and `TestScheduleRetryFollowsGroupCount` (a hand-built server: 20 failed
    attempts for a group with a job are retried 2-10 times in 1s, then
    dropping the group's count to 0 ends the retry with a request for 0). Both
    pass, and `-race -count=3` of all `TestScheduleRetry*` passes, as do
    `TestReliable4ArchiveSchedulingAsync`, `TestReserveRefusedForDoomedElement`,
    `TestJobqueueRunnerScheduling`, `TestReliable3*`, `TestJobqueueMockRunner`
    and `TestReliable2ScheduleGroupDeadlock`.
  - Mutants, each killed: base `server.go` (both new tests fail: 13 requests
    after the delete; 78 retries and no drain); retry keeps its stale count
    (both fail); no one-loop-per-group (`TestScheduleRetryFollowsGroupCount`
    fails, 49 retries); a single retry instead of a loop
    (`TestScheduleRetryBackoff` and `TestScheduleRetryFollowsGroupCount` fail).
  - Speed: the success path only gains a call. A throwaway benchmark of
    `scheduleRunners` succeeding on a mock scheduler, `-count 6`: base median
    648 ns/op, fixed 651 ns/op, within noise.

- [ ] 2. After each crash restart, ~370–770 error-level "jtouch: bad job (not
  in queue...)" lines in the minute after. One traced example (key
  afec1f37…, runner log
  `/nfs/hgi/wr/sb10-bigdb/soak9/run/runnerlogs/26.10.04/11-03-41.node-14-27.3071079`):
  a touch arriving just after its job's successful archive.

- [ ] 3. `wr manager stop` returned rc 0 while the pid still existed (zombie
  for a few ms, aliveAtReturn=y on all 3 clean stops).

- [ ] 4. StartTime drift on 186 jobs.

- [ ] 5. Clean-stop re-run accounting.
