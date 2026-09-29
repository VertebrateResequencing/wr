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
- [ ] `TestDepGranularitySidecarReportsElapsedTime`
  (`jobqueue/depgranularity_startup_test.go:904`) was seen failing once under
  load, and was not reproduced in `260928-load-sensitive-flakes.md`. The
  candidate cause recorded there: the test samples the sidecar, waits a fixed
  `dgsHeartbeatInterval * dgsHeartbeatTicks` (200ms), and requires the second
  sample to have moved on, so a heartbeat goroutine starved for 200ms fails
  it.
