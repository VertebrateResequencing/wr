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
  - Review follow-up, test gap: deleting the loop's deferred
    `releaseScheduleRetry` survived every `TestScheduleRetry*` test, though a
    leaked claim would stop retries for that group name for the manager's
    life. `TestScheduleRetryAgainAfterRecovery` now covers it: a retry loop
    ends on success, then a new failure for the same group must be retried
    (at least 3 calls in 1s). Green, and `-race -count=3` of all
    `TestScheduleRetry*` passes; with the deferred release deleted it fails
    (`Expected '1' to be greater than or equal to '3'`), exit 1.
  - Review follow-up, a failure dropped as a loop ends: a retry loop could
    succeed while another attempt for the group failed after the loop's
    attempt; that attempt found the claim still held and left the retrying to
    a loop that then ended, so its failure was not retried until the next
    decrement or rac pass. Now `claimScheduleRetry` marks a running loop as
    wanted again when it refuses a claim, and the loop ends through
    `endScheduleRetry`, which under `srmutex` either ends it or, if marked,
    clears the mark and goes round again (one more attempt at the current
    count, after the reset backoff's Min). A loop stopped by shutdown
    releases its claim. Red: `TestScheduleRetryFailureAsLoopEnds`, which
    fails an attempt between the loop's successful attempt and its end
    through a test hook (`scheduleRetryEndingHook`, nil in production), fails
    before this change with `Expected: 4 / Actual: 3` (no retry of the
    dropped failure), exit 1; green after, and `-race -count=3` of all
    `TestScheduleRetry*` passes, as do the related scheduling tests listed
    above. Mutants: the claim not marking the loop, or `endScheduleRetry`
    ignoring the mark, fail that test; `endScheduleRetry` not releasing the
    claim fails `TestScheduleRetryAgainAfterRecovery`. Not killed, and
    accepted: dropping the release on the shutdown return, as no retry is
    ever claimed again once client handling has stopped.
  - Accepted, not fixed: a retry reads `currentGroupCount` and then calls
    Schedule, so a decrement's own asynchronous Schedule can land between the
    two and the retry's older, higher count reaches the scheduler last. It
    stands until the next decrement or rac pass. This is the same race
    `decrementGroupCount`'s comment already accepts for concurrent decrements,
    and is no worse than before, when the retry always sent its stale count.
  - Speed: the success path only gains a call. A throwaway benchmark of
    `scheduleRunners` succeeding on a mock scheduler, `-count 6`: base median
    648 ns/op, fixed 651 ns/op, within noise.

- [x] 2. After each crash restart, ~370–770 error-level "jtouch: bad job (not
  in queue...)" lines in the minute after. One traced example (key
  afec1f37…, runner log
  `/nfs/hgi/wr/sb10-bigdb/soak9/run/runnerlogs/26.10.04/11-03-41.node-14-27.3071079`):
  a touch arriving just after its job's successful archive.
  - Evidence: `$O/manager.log` has 2,660 such lines, all in the minute after a
    crash restart (10:10 68, 10:14 164, 11:03 367, 11:11 591, 11:29 774,
    11:35 685), and the runner logs have the same 2,660 as "could not touch"
    warnings. Every one was checked, not a sample: in its runner's log, each
    comes after that runner's own end of the run, 2,633 after "command ran OK"
    (it then archived) and 27 after "exited with code 3 ... will be tried
    again" (it then released). None came while the command was running. Most
    runners also logged a "send time out" touch while the manager was down:
    the touch was held up and arrived with, or after, the report. Of the 2,078
    keys, 5 were reserved twice: 3 were those released runs, re-run as
    expected, and in the other 2 the refused touch came from the second run,
    after its archive.
  - Red: `env -u WR_LSF_TEST_KEY go test -tags netgo -count=1 -run
    TestTouchAfterRunEndedLogLevel ./jobqueue/` exits 1 before the fix: a
    runner's touch just after it archived, and just after it released, are
    each logged at Error (`Expected: log15.Lvl(4) / Actual: log15.Lvl(1)`).
  - Cause: a runner keeps touching until its final report is done, and stops
    without waiting for a touch already in flight, so a touch held up while
    the manager was down arrives after the archive or release. The manager
    then finds the job gone from the queue, or no longer running, and refuses
    it as `ErrBadJob`, which it logged at Error like any other refusal.
  - Fix (`jobqueue/serverCLI.go`, `jobqueue/server.go`): when a touch is
    refused as a bad job, `touchAfterOwnRunEnded` checks whether it came from
    the runner that last ran the job, after that runner ended the run: the job
    is complete and its complete record's `ReservedBy` is the touching client,
    or the job is still queued but not running, its `ReservedBy` is the
    touching client and its `FailReason` is not `FailReasonLost` (the manager
    gave up on it). Then the logged detail is `touchAfterRunEnded`, and
    `isRoutineClientRefusal` treats that as routine, so it is logged at Debug,
    as #665 did for "unknown subscription". The runner's reply is unchanged
    (`ErrBadJob`). A touch of a job that does not exist, of one another runner
    ran, or of one the manager released as lost is still an Error. The check
    runs only on the refusal path, so successful touches cost nothing more.
  - Tests (`jobqueue/client_request_log_test.go`,
    `TestTouchAfterRunEndedLogLevel`, through a real server): after archive
    and after release, Debug; after a lost release, another runner's touch
    after an archive or a release, and a touch of a never-added job, Error.
    Passes with `-race -count=2`, as do `TestClientRequestErrorLogLevel` and
    the other touch tests (`TestClientTouchSendsLiveEndState`,
    `TestLiveTouchCaptureReleaseOnMarkersRetry`,
    `TestClientExecuteLiveTouchPayloads`,
    `TestLostJobSparesASecondRunThatNeverTouched`,
    `TestKillAfterCmdExitKeepsTouching`, `TestKillRacingCmdExitKeepsTouching`,
    `TestManagerLiveJTouch`, `TestReliable2OnTimeTouchedJobNeverLost`,
    `TestReliable2LostJobRecoversOnTouch`).
  - Mutants, each killed: no lost check (the lost-release case logs Debug);
    the complete check ignoring `ReservedBy` (another runner's touch after an
    archive logs Debug); the queued check ignoring `ReservedBy` (another
    runner's touch after a release logs Debug); touches never routine (the two
    red cases log Error).
  - Not done: making the runner wait for an in-flight touch before its final
    report would stop it sending these, but runners already deployed would
    still send them, and the report would wait for a touch that is retrying
    against a manager that is down.

- [x] 3. `wr manager stop` returned rc 0 while the pid still existed (zombie
  for a few ms, aliveAtReturn=y on all 3 clean stops).
  - Owner ruling 261004: harmless; recorded, no action.

- [x] 4. StartTime drift on 186 jobs.
  - Explained, no action. 186 jobs had a recorded StartTime more than 2s
    (at most 11.9s) before their marker's start. StartTime is the runner's own
    clock, taken just after `cmd.Start()` (`jobqueue/client.go` about line
    3736 at `55cc2565`) and recorded as given (`serverCLI.go`
    `reportedStartTime`, about line 1308), so manager lag cannot shift it.
    Every gap is positive (database earlier than marker): it is
    `psimjob.sh`'s own startup work on NFS before it writes its marker, on a
    few loaded hosts (node-14-11 50, node-13-10 38, node-13-24 27). Worst
    case, node-14-18 pid 1839413: the runner logged "started executing" at
    10:46:26, the database has 10:46:26.122 and the marker 10:46:37.977.
    Walltime and resource learning use EndTime minus StartTime, both from the
    runner's clock; lost and TTR use touches; nothing depends on the gap.
    Evidence: `/nfs/hgi/wr/sb10-bigdb/soak9/analysis/items45/starttimes-gt2s.tsv`.

- [x] 5. Clean-stop re-run accounting.
  - Explained, no action. The re-runs after clean-stop kills come from
    prodsim's operator chore `retry_portal` (`wr retry -i portal -z`,
    `developers/prodsim/actors.go` about line 601), which succeeded at
    10:46:45: all 2,205 portal re-runs started after it, none between the
    10:37 restart and the retry. One more was a build job killed by an LSF
    SIGINT at 10:21. Every run in flight at a clean stop (4,209 at the two
    mid-run stops) was buried and then re-run only by that retry, left
    buried, removed by the fofn watcher, or had exited 0 and was recorded
    complete. None was re-run by the manager itself, which fits the owner's
    ruling that a stop buries the jobs it kills. Evidence:
    `/nfs/hgi/wr/sb10-bigdb/soak9/analysis/items45/killed-at-clean-stops.tsv`.

## Soak9 results for earlier items

- `260929-archive-before-start.md`, "Crash 1 ... 624 double runs": does not
  reproduce without the binary swap; ticked there. Soak9 had 0 double runs in
  741,508 runs over six `kill -9` crashes at 2,300-3,600 running jobs (peak
  3,872), and no runner died without reporting.
- `261004-checklist-tidy-8e94211c.md`, Backlog item on the O(backlog)
  scheduling cycle: measured; numbers recorded there; owner decision pending,
  left unticked.
