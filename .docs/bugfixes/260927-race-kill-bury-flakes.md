- [x] TestJobqueueExecutionAndDependencyScenarios, lane jq_execution_details,
  "Jobs that fork and change processgroup can still be fully killed",
  jobqueue/jobqueue_test.go:4635. Expected FailReason "killed by user
  request", got "". Execute returned "finished running, but will need to be
  rerun due to a jobqueue server error: signal: killed", logged just after
  `jbury(...): bad job (not in queue or correct sub-queue)`. Failed in all 3
  full race runs, passed twice alone with -race.
  - Red command (lane under controlled load): `stress -c 40` alongside the race
    test binary run as the lane runs it, `GOMAXPROCS=2 WR_TEST_LANE=38
    WR_TEST_SHARD=c jobqueue.test -test.run
    '^TestJobqueueExecutionAndDependencyScenarios$'`. Before: 7 of 7 runs
    failed at line 4635 (exit 1). After: 5 of 5 passed.
  - Temporary prints (not committed) showed the sequence in a failing run:
    `killRunningJob lost=false` (the user's kill), then `ttrCallback marking
    lost`, then `killRunningJob lost=true onlyRun=true` (the lost-job release
    for a killed job), then `getijForReport ... state bury` for the runner's
    jbury.
  - Root cause (production): once kill was asked for, nothing renewed the
    job's TTR. `handleTouch` returned KillCalled before `q.Touch`, and the
    runner's touch loop ran the kill inline, blocking touches while the
    command's children were terminated (500ms grace each, and a `ps` walk),
    and then stopped touching altogether. A runner whose kill plus post-exit
    work outlasted the TTR had its job declared lost. With killCalled set,
    `confirmOrReleaseLostJob` releases it 50ms later, so the job was buried
    as lost (or released to run again, when it had retries left), and the
    runner's own bury was then refused as a bad job. The before-start kill of
    #614 had the same gap: a runner slow to reach cmd.Start after a kill was
    refused the same way.
  - Regression test: `jobqueue/kill_wind_down_test.go`
    (`TestKilledJobOutlivingTTRIsBuriedAsKilled`, TTR 1s, stall 3s): a
    runner slow after the kill (afterWaitHook), a slow kill
    (childProcessesHook), and a runner slow to reach the start after a
    before-start kill (gated docker socket). Without the fix it failed 3 of
    3 (line 98: Execute's error was not the killed Error; line 104: state
    ready, not buried). With only the server half, the two running cases
    still failed.
  - Fix: `handleTouch` now renews the TTR (and recovers a lost job) on every
    touch, and still answers KillCalled. The touch loop runs the server kill
    in its own goroutine and keeps touching until Execute stops it.
    CHANGELOG entry added, since a killed command could be retried.
