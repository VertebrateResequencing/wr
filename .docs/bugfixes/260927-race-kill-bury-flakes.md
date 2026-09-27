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
- [x] TestReservedRetryShowsNothingOfThePreviousRun, lane jq_default,
  jobqueue/run_state_reset_test.go:108. Expected state delayed, got ready.
  Seen once.
  - Red command: `GOMAXPROCS=2 WR_TEST_LANE=9 jobqueue.test -test.run
    '^TestReservedRetryShowsNothingOfThePreviousRun$' -test.count=20` (race
    build) at host load about 70. Before: 8 of 20 failed at line 108,
    `Expected: "delayed"`, `Actual: "ready"` (exit 1). After, under `stress
    -c 40`: 20 of 20 passed.
  - Root cause (test): the test sets `ReleaseDelayMin` to 10ms, so the
    released job leaves delayed for ready 10ms after `Release` returns. A
    loaded machine easily takes longer than that to get to the
    `GetByEssence` that asserted delayed. The manager was right in both
    cases.
  - Fix (test only), in `jobqueue/run_state_reset_test.go`: the assertion
    accepts delayed or ready, either of which says the run was released for
    its retry. The rest of that block still checks the failed run's host,
    fail reason, CPU time and output. A 300ms sleep before the read (to force
    ready, then reverted) left the test passing 3 of 3.
- [x] TestSubscriptionReconnectDuringManagerShutdown fails at
  jobqueue/subscription_test.go:2108 with "2.4ms measured against a 200ms
  minimum". The test asserts a lower bound on a duration, so check whether the
  bound is meaningful or whether the wait can end early for a legitimate
  reason. Seen in a full race run under heavy load; passed 3 of 3 alone.
  - Red command: `stress -c 40` alongside `GOMAXPROCS=2 WR_TEST_LANE=9
    jobqueue.test -test.run '^TestSubscriptionReconnectDuringManagerShutdown$'
    -test.count=15` (race build). Before: 6 of 15 failed, 4 at line 2108 and
    2 in the "Unsubscribe while a reconnect holds the client" Convey
    (`clientLockTakenWithin` false, and the resubscribe not ending in
    ErrRecvTimeout). A temporary log showed the failing unsubscribes
    returning in 1-8ms, 11-53ms after `server.Stop` began, where passing ones
    took 200-208ms.
  - Root cause (test): `pingUntilUnread` took a 10ms ping timing out as proof
    that the stopping manager had stopped reading. A loaded manager that is
    still reading can take longer than 10ms to answer, so the helper returned
    long before the window opened, and the steps that followed were answered
    at once instead of waiting on their own deadline. The 200ms lower bound
    is meaningful: it is what shows the step waited out its budget.
  - Fix (test only), in `jobqueue/client_connect_test.go`: `pingUntilUnread`
    now also takes the server. It keeps pinging (which gives the last reader
    the request it admits before exiting) until the server's
    `clientHandlingDone` is closed, meaning every RPC reader has exited, and
    then sends the one ping it reports on, which nothing can read. Its 4
    callers pass the server.
  - After, same load: 15 of 15 passed, and `TestPingBoundedDuringManagerShutdown`
    (the helper's other user) 15 of 15.
- [x] TestReliable4StatusSeedBoundary fails at
  jobqueue/reliable4_seedboundary_test.go:344. Seen in a full race run under
  heavy load; passed 3 of 3 alone.
  - Line 344 is `So(recorder.waitForMessages(1), ShouldBeTrue)`: the canary
    job's ready->running delta never reached the freshly dialled status
    websocket in 60s.
  - Red command (deterministic): `CGO_ENABLED=1 go test -race -run
    '^TestStatusWSOnDeltaFeedOnceDialled$' ./jobqueue`, which makes the
    handler sleep 1s just after the upgrade (`statusWSUpgradedHook`), then
    starts one job and waits for its delta. On the original handler plus the
    hook: FAIL at line 79 after 60s. Under load (`stress -c 40`,
    `GOMAXPROCS=2`, `-test.count=20`), the original test failed 0 of 20 on
    develop's handler and 1 of 20 on a first fix that only moved the joins
    ahead of the goroutines, at line 344 both times it failed.
  - Root cause (production): the status websocket handler joined the status,
    bad-server and scheduler casters inside the listener goroutines, which it
    started after the upgrade and after the goroutine that reads the page's
    requests. The page asks for its seed counts as soon as the socket opens,
    so on a loaded manager the seed could be taken before the join, and a
    transition between the two reached the page in neither, leaving its
    counts wrong until a refresh. The test's canary delta was lost the same
    way: the dial returns on the upgrade, not on the join.
  - Fix: `webInterfaceStatusWS` joins all three casters before the upgrade
    (closing them if the upgrade or the connection's registration fails) and
    hands the members to `setupUpdateListener`, which no longer joins. New
    test hook `statusWSUpgradedHook` and regression test
    `jobqueue/status_ws_join_test.go`. CHANGELOG entry added.
  - After, same load: `TestReliable4StatusSeedBoundary` 30 of 30 and
    `TestReliable4StatusSeedCounts` 30 of 30; the hook test passes.
- [x] TestJobqueueWithMounts, at jobqueue/jobqueue_test.go:9321. The S3
  database backup fails with "The specified key does not exist." The test
  sleeps a fixed 8s and then expects the backup to be there. It failed in 2 of
  3 full race runs, including one at low load, and passed each time it was run
  on its own with -race. Replace the fixed sleep with a bounded wait for the
  real condition, if that's what's wrong. Otherwise, find the real cause.
  - Red command: two copies of the race test binary run at once, as two
    checkouts' `make race` do, `WR_TEST_LANE=44` and `WR_TEST_LANE=50`, each
    `-test.run '^TestJobqueueWithMounts$'`. Before: both failed at line
    9321, `Actual: 'The specified key does not exist.'`, while the same
    binary run alone passed 2 of 2.
  - Root cause (test): every run of the test backs up to the same S3 key,
    `$JOBQUEUE_REMOTES3_PATH/db.bk.development`, and deletes it at the end
    of each Convey. Several agents' suites share this host and that path, so
    one run's delete (or overwrite) lands between another's upload and
    download. The fixed sleep was not the cause, but was replaced too.
  - Fix (test only), in `jobqueue/jobqueue_test.go`: the backup goes under
    this run's own name, the base of its pid-named `mountDir`. The 8s sleep is
    now a poll, bounded by a minute, that downloads the backup to a temp dir
    until it holds the one live job; it no longer downloads into the path
    the manager uses for its own temporary backup file.
  - After: the concurrent pair passed 2 runs of 2.
- [ ] TestReliable2KeepReconnectResync failed once in a plain `make test` run
  at load around 90; it passed on rerun.
  - Seen once, no repro. `stress -c 40` alongside `GOMAXPROCS=2
    WR_TEST_LANE=45 jobqueue.test -test.run
    '^TestReliable2KeepReconnectResync$' -test.count=30` (non-race build, as
    `make test` runs it) passed 30 of 30. The failing output was not
    captured, so the failing line is unknown. A candidate, unconfirmed: the
    test gives the subscription a 2s reconnect budget
    (`applySubscriptionReconnectTimings(..., 250ms, 2s)`), which a manager
    restart plus recovery at load 90 could outlast.
- [x] TestKillRacingCmdExitKeepsTouching failed at
  jobqueue/kill_after_exit_test.go:422 with "not started: killed by user
  request" in a full race run at load 15-32. It passed 10 of 10 alone on
  both develop and the branch.
  - Not explained by the first item's fix: it fails the same with it.
  - Red command (deterministic): `CGO_ENABLED=1 go test -race -run
    '^TestKillOnceStartedWaitsForTheStart$' ./jobqueue`, which reserves a
    job, calls `killOnceStarted`, and reports the start 500ms later. With the
    old helper: FAIL at line 124, the kill came before the start. Under load
    (`stress -c 40`, `GOMAXPROCS=2 WR_TEST_LANE=40 -test.count=20`, race
    build with the first item's fix), `TestKillRacingCmdExitKeepsTouching`
    failed 6 of 20: 2 at line 422 and 4 at line 425 (`heldKills` 0, so the
    kill was never held by the touch loop, because it came before the start).
  - Root cause (test): `killOnceStarted` took a non-zero pid as the sign that
    the command had started, but a reservation already records the runner's
    own host and pid (see `resetJobForReservation`), and a reserved job
    matches `JobStateRunning` in `GetByRepGroup`. So it killed at once after
    `Reserve`. The test only passed when Execute reached `cmd.Start` before
    its first touch, 50ms in; under load it got to the touch first, saw the
    kill, and never started the command.
  - Fix (test only), in `jobqueue/kill_helpers_test.go`: `killOnceStarted`
    waits for the job's `StartTime`, which only `Started` sets. The new
    `TestKillOnceStartedWaitsForTheStart` pins that. The other users of the
    helper (`reliable4_cmd_log_test.go` and the first item's
    `kill_wind_down_test.go`) now kill a started command, as they meant to.
  - After, same load: 20 of 20 passed.
- [ ] TestStatusCountReconcile hit its 120s timeout at load 80+ on develop.
  - Recorded only, as asked (low priority). Not investigated.
