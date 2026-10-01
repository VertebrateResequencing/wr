# 260929: test and CI reliability follow-ups

Branch `test-ci-reliability`, based on `origin/develop` at `00be340` (#649).
It carries the commits of `fix-load-sensitive-flakes`
(`260928-load-sensitive-flakes.md`), the db-close fix from
`fix-status-summary-scaling` and the CI build cache commit from
`polish-lsf-poll-bind-ci`, cherry-picked from their `83d2f693` base. The
lost-run fixture's `startedRequest` call gained the start time argument #647
added.

A reviewer scrutinised `Start a reservation's TTR when its runner is given the
job` against develop (#642 persisted reservations; #647 made the runner settle
its start report first and the manager trust its start time). Verdict: keep.
Reverting its production hunks still fails `TestReservationTTRStartsAtHandOut`
3 of 3 with `Expected: "reserved" Actual: "lost"`, every reservation path sets
and clears `handingOut`, and the race build is clean. The CI build cache
commit was reviewed too: PASS.

- [x] Review findings on the cherry-picked tests:
  (1) `TestReservationTTRStartsAtHandOut` (`jobqueue/reserve_ttr_test.go:110`)
  asserts `So(lost, ShouldBeFalse)` after `Reserve` returns, while the 200ms
  TTR restarted at hand-out is already running, so 200ms of CI load before
  the sample fails it. `reserved.State == JobStateReserved` already covers
  "not lost before hand-out".
  (2) `jobqueue/subscription_test.go:2555-2568`: the two new consts sit between
  `applySubscriptionReconnectTimings`'s doc comment and its func, so the func
  lost its doc comment and `subscriptionRestartRetryTime`'s begins with
  another function's text.
  - Red: with a 300ms sleep before the `serverJobState` sample,
    `CGO_ENABLED=0 go test -tags netgo -count=1 -run
    '^TestReservationTTRStartsAtHandOut$' ./jobqueue/` failed at line 111,
    `Expected: false, Actual: true`. With the fix and the same sleep, it
    passed.
  - Fix (test only): `jobqueue/reserve_ttr_test.go` drops the lost sample and
    says why. Reverting c56fc02's production hunks still fails the test, now
    at `reserved.State` (`Expected: "reserved" Actual: "lost"`).
    `jobqueue/subscription_test.go` moves the two consts above
    `applySubscriptionReconnectTimings`'s doc comment.
  - These are the changes the reviewer proposed. The coordinator checked the
    diff. `make lint` reports 0 issues, and `^TestSubscriptionReconnect`
    passes.
- [x] Test managers' first start sometimes fails `bind: address already in
  use`, because the test port helper picks a free port and releases it before
  the manager binds it (seen in `TestReaddQueuedKeepsRecord`,
  `jobqueue/readd_queued_test.go`).
  - Cause (test helper, not the pick-then-release race): the failing port,
    45993, is held by rpc.statd with an IPv6-only listener (`ss -tlne`:
    `[::]:45993 ... uid:118 ... v6only:1`). Outside a suite lane,
    `freeTestPort` falls back to `freeport.GetFreePort`, which probes only
    `127.0.0.1` and so can hand that port out. The manager's AF_INET port
    reservation on `0.0.0.0` does not conflict with a v6only socket, so it
    succeeds, but publication's dual-stack listen does, and gives up after the
    5s bind budget. The failing run's log had no "could not reserve" line, then
    `could not listen on the manager port yet, retrying port=45993`, then
    `the server's publication gave up`. Item 5 of
    `260925-add-test-connect-flake.md` failed on the same port. Lane runs
    (`make test`, CI) were never affected: the lane picker's `portCanListen`
    binds `:port` dual-stack, and lane ports are below the ephemeral range.
  - Red: the new `TestServeSkipsPortWithIPv6OnlyListener`, with only the
    `ephemeralTestPort` seam added, `CGO_ENABLED=0 go test -tags netgo
    -count=1 -run '^TestServeSkipsPortWithIPv6OnlyListener$' ./jobqueue/`,
    failed after 5.3s at `server_startup_test.go:165`: `Expected: nil Actual:
    'the server's publication gave up'`.
  - Fix (test only), `jobqueue/jobqueue_test.go`: `freeTestPort`'s non-lane
    fallbacks use `freeEphemeralTestPort`, which bind-checks up to 20
    candidates from `ephemeralTestPort` (`freeport.GetFreePort`) with
    `portCanListen`. The test, in `jobqueue/server_startup_test.go`, holds a
    `tcp6` listener, hands its port out first, and requires `serve` to
    succeed on another port.
  - Reviewer: PASS. Red and green confirmed, the diagnosis was reproduced with
    three binds on 45993, and the focused tests pass with and without
    `WR_TEST_LANE`. `make lint` reports 0 issues.
  - Found, not fixed here: `client/testing/server.go` `laneFreePort` has the
    same fallback (next item). The Linux port reservation is AF_INET only
    while the manager listens dual-stack, so a manager port held only by an
    IPv6-only listener passes `Serve` and publication exits 5s later, instead
    of `Serve` failing at once. That is a small production fix for a separate
    branch.
- [x] `client/testing/server.go` `laneFreePort` falls back to
  `freeport.GetFreePort()` outside a lane (around :147, :152, :169), with no
  dual-stack check, so its test managers can be handed a port held by an
  IPv6-only listener, as in the item above.
  - Red: the new `TestPrepareWrConfigSkipsPortWithIPv6OnlyListener`, with
    only the `ephemeralFreePort` seam added, `CGO_ENABLED=0 go test -tags
    netgo -count=1 -run '^TestPrepareWrConfigSkipsPortWithIPv6OnlyListener$'
    ./client/testing/`, failed: `Expected '41593' to NOT equal '41593'`.
    `PrepareWrConfig` handed out the port the `tcp6` listener held.
  - Fix (test helper only), `client/testing/server.go`: all three
    `laneFreePort` fallbacks use `freeEphemeralPort`, which bind-checks up to
    20 candidates with `portCanListen` (renamed from `lanePortAvailable`, a
    dual-stack listen on `0.0.0.0`). `PrepareWrConfig` picks both the manager
    and web ports this way.
  - Reviewer: PASS. Red and green confirmed. `portCanListen` was shown to
    reject rpc.statd's port 45993, which freeport's `127.0.0.1` probe accepts.
    `make lint` reports 0 issues, and `./client/testing/` passes with and
    without `WR_TEST_LANE`.
- [x] `TestDepGranularitySidecarReportsElapsedTime`
  (`jobqueue/depgranularity_startup_test.go:904`) was seen failing once under
  load, and was not reproduced in `260928-load-sensitive-flakes.md`. The
  candidate cause recorded there: the test samples the sidecar, waits a fixed
  `dgsHeartbeatInterval * dgsHeartbeatTicks` (200ms), and requires the second
  sample to have moved on, so a heartbeat goroutine starved for 200ms fails
  it.
  - Cause (test): the first sample is usually Serve's own write (elapsed 0s).
    After that, only the heartbeat's 50ms ticker goroutine rewrites the
    sidecar (`startRecoveryHeartbeat` -> `reportStillRecovering` ->
    `WriteDBUpgradeStatus`), so a heartbeat starved for over 200ms left the
    same file for the second sample. It is not a timestamp granularity
    problem: `UpdatedAt` is `time.Now()`, stored in JSON with nanoseconds.
  - Red: with a temporary 300ms sleep before the heartbeat's ticker,
    `CGO_ENABLED=0 go test -tags netgo -count=1 -run
    '^TestDepGranularitySidecarReportsElapsedTime$' ./jobqueue/` failed at
    line 935 (`So(second.UpdatedAt.After(first.UpdatedAt), ShouldBeTrue)`,
    `Expected: true Actual: false`).
  - Fix (test only), `jobqueue/depgranularity_startup_test.go`:
    `dgsWaitForSidecarRewrite` waits, bounded by `dgsServingWait` (30s), for a
    sample in the same state with a different `UpdatedAt`. Every assertion is
    kept, and the unused `dgsHeartbeatTicks` is gone.
  - After: with the same 300ms delay, 3 of 3 passed. Mutations: a sidecar that
    is never refreshed fails `found` after 30s, and one that is rewritten but
    whose elapsed never grows fails `Expected '0s' to be greater than '0s'`.
  - Reviewer: PASS. Red and green confirmed. Torn reads are ruled out: the
    file is written to a temp file and renamed. Samples from other phases are
    ruled out too: the hook parks recovery. `make lint` reports 0 issues.
- [x] `cmd/manager_stop_test.go` `TestManagerStopStalePidFile`'s second Convey
  ("wr manager stop still terminates a non-responsive manager named by the pid
  file", line ~273, `So(exited, ShouldBeTrue)`) failed under `make race` on
  develop-era code. Likely cause: the test calls `runManagerStopForTest` before
  the child's argv is readable as `sh ...`, so `wr manager stop` judges the pid
  stale (not a wr manager) and sends no SIGTERM. `TestDaemonStillRunning`
  already polls `processArgs(pid) != nil` for this reason. Check the first
  Convey, `TestManagerStatusStalePidFile` and `TestManagerStopInvalidPidFile`
  for the same pattern.
  - Red: a temporary probe test that starts the second Convey's `sh` child 300
    times with `startManagerStopTestProcess` and reads `processArgs(pid)`
    straight after each start, `go test -run TestZZArgvProbe -count=1 -v
    ./cmd`, saw an empty argv (`got []`) in 5, 3, 9 and 4 of 300 starts on 4
    runs. `exec.Cmd.Start` returns once the exec's close-on-exec pipe has
    closed, which is before the new image's argv is set up, so
    `/proc/<pid>/cmdline` briefly reads empty, `processArgs` gives nil and
    `isManagerProcess` is false.
  - Fix (test only), `cmd/manager_stop_test.go`: `startManagerStopTestProcess`
    now waits, bounded by `pollUntilTrue`, until `processArgs(pid)` equals the
    child's `cmd.Args`, after registering the cleanup that kills its process
    group. That covers every user of the helper: both
    `TestManagerStopStalePidFile` Conveys, `TestManagerStatusStalePidFile`,
    `TestNonPositivePidsAreNeverSignalled`, `TestDaemonStillRunning` (whose own
    non-nil poll is now redundant and gone) and `startShuttingDownManager`.
    `TestManagerStopInvalidPidFile` starts no process.
    `TestDaemonStillRunningUnreadableArgv` uses a bare `exec.Command` with its
    own poll.
  - Regression test: `TestManagerStopTestProcessArgv` starts the second
    Convey's `sh` child 300 times through the helper and asserts that none
    reads back a different argv. With the poll removed it failed with 9, 5, 5,
    8 and 3 mismatches; with it, it passed every run, and the probe showed
    0/300 on 7 runs.
  - Reviewer: PASS for this change. `make lint` 0 issues, `go test ./cmd/` and
    the focused `-race` run pass.
- [x] Found by the reviewer of the item above: `cmd/cloud_test.go`'s
  `startTestProcess` and `startTestForwarder` have the same race. The code
  under test (`cleanupDeployForwardingProcesses` -> `checkProcess` ->
  `isForwarderProcess` -> `processArgs`) judges a forwarder whose argv still
  reads empty as not running and does not kill it, so
  `So(processExited(managerDone/webDone), ShouldBeTrue)` in
  `TestCleanupDeployForwardingProcesses` and `TestHandleManagerConnectFailure`
  can flake. Also from that review: `TestManagerStopTestProcessArgv` keeps its
  300 process groups alive until the test ends; kill each once checked.
  - Red: a temporary probe test that calls `startTestForwarder` 1000 times and
    checks `isForwarderProcess(pid)` straight after each start, `go test -run
    TestZZForwarderProbe -count=1 -v ./cmd`, got 5, 6 and 10 of 1000 wrong on
    3 runs.
  - Fix (test only): the argv wait is now `waitForTestProcessArgv`
    (`cmd/manager_stop_test.go`), called by both `startManagerStopTestProcess`
    and `cmd/cloud_test.go`'s `startTestProcess`, so it covers
    `startTestForwarder`'s users and `TestCheckProcessStalePid` too. No other
    cmd test judges a child's argv without such a wait.
    `TestManagerStopTestProcessArgv` now kills and reaps each child once
    checked.
  - Regression test: `TestStartTestForwarderArgv` starts 1000 forwarders and
    asserts `isForwarderProcess` is true straight after each start. Without the
    wait it failed 5 of 6 runs (1-3 mismatches); with it, the probe showed
    0/1000 on 8 runs.
  - Reviewer: PASS. `make lint` 0 issues, `go test ./cmd/` and the focused
    `-race` run pass.
