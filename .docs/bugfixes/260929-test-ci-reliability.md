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

- [ ] Review findings on the cherry-picked tests:
  (1) `TestReservationTTRStartsAtHandOut` (`jobqueue/reserve_ttr_test.go:110`)
  asserts `So(lost, ShouldBeFalse)` after `Reserve` returns, while the 200ms
  TTR restarted at hand-out is already running, so 200ms of CI load before
  the sample fails it. `reserved.State == JobStateReserved` already covers
  "not lost before hand-out".
  (2) `jobqueue/subscription_test.go:2555-2568`: the two new consts sit between
  `applySubscriptionReconnectTimings`'s doc comment and its func, so the func
  lost its doc comment and `subscriptionRestartRetryTime`'s begins with
  another function's text.
- [ ] Test managers' first start sometimes fails `bind: address already in
  use`, because the test port helper picks a free port and releases it before
  the manager binds it (seen in `TestReaddQueuedKeepsRecord`,
  `jobqueue/readd_queued_test.go`).
