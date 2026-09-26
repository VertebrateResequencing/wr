- [x] Flake: TestSubscriptionReconnectDuringManagerShutdown
  (jobqueue/subscription_test.go, "The unsubscribe cleaning up a rejected
  replacement is bounded by the retry budget" case, about line 1933) failed in
  GitHub CI on PR #620 (head 7198425, which changes only developers/wrdev.sh)
  on 2026-09-26: `Expected '1.000096513s' to be less than '1s' (but it
  wasn't)!` for `So(took, ShouldBeLessThan, time.Second)` after
  unsubscribeRejectedWithin. Earlier checklists mention related subscription
  flakes (grep .docs/bugfixes for TestSubscriptionReconnect).
  - What the assertion guards. `took` runs from just before
    `unsubscribeRejectedReplacement` starts until it returns, with a 200ms
    budget (`unboundedRequestBudget`). The narrowed receive deadline is that
    200ms; `requestTimeout(subscriptionReconnectTimeout)` is the 60s socket
    floor it narrows from. The 1s is `subscriptionReconnectTimeout`, the
    spent-budget fallback. The step normally takes 200.3-201.0ms (12 of 12
    runs at GOMAXPROCS=1 under -race), so CI's value was 800ms over normal,
    not a sub-millisecond overrun of scheduling noise. The regressions: the
    step left on plain `request()` (the 260828-4 BUG 7 red) waits the 60s
    floor and fails `So(returned, ShouldBeTrue)` at the 2s limit; the
    fallback used regardless of budget waits 1.0007s, which the old bound
    caught by only 0.66ms.
  - Cause. The Convey subscribes with a real Subscription, whose poll
    goroutine is still live. When the shutdown sweep closes its subscription
    it reconnects the same Client on the default 24h budget, taking the
    client lock to swap the socket (the new one has a 1s send deadline) and
    again for a resubscribe that waits out the 60s floor. A trace of the
    unfixed test showed `reconnect SWAP` at the moment the unsubscribe's
    200ms ended (it had been waiting for the lock), then `reconnectOnce
    took=1m0.2s`. So the Convey timed the step behind a concurrent reconnect.
    The same lock contention exists in production, with a different
    partner: the step only runs once the subscription is stopping, and only
    `Unsubscribe` or a cancelled context set that, both of which then send
    their own unsubscribe on the same Client lock. That is the product bug
    in the next item. The Convey's job is narrower: it proves the step
    narrows its own receive deadline to its budget, so measuring it in
    isolation is right. No CI trace exists, so which of the two lock holds,
    or the 1s send deadline on a swapped socket, produced exactly 1.0001s is
    inferred, not observed.
  - Red (deterministic seam, reverted): in the unfixed test, a goroutine
    stood in for the poll goroutine's reconnect by holding `jq.Lock()` for
    800ms, taken just before `unsubscribeRejectedWithin`. Result:
    `Line 1940: Expected '1.003888192s' to be less than '1s' (but it
    wasn't)!`, with the error still `ErrSubscriptionClosed` joined with
    `receive time out`, so the product behaved correctly.
  - Fix (test only). The Convey sets the reconnect retry budget to 1ns
    (`applySubscriptionReconnectTimings`, as the spent-budget Convey already
    does) and checks the client picked it up, so the poll goroutine gives up
    in microseconds without touching the client. The step's own 200ms budget
    is still passed explicitly. The bound is now `unboundedRequestBudget`
    plus a 400ms `schedulingSlack` (600ms), halfway between the 200ms the
    step should take and the 1s the fallback regression takes. A static
    check that `subscriptionReconnectTimeout - bound >= schedulingSlack`
    keeps that gap from being tuned away. It also asserts `took >= budget`
    and `errors.Is(err, mangos.ErrRecvTimeout)`, so a fast failure or a
    send-deadline wait cannot pass.
  - After: traced runs show the poll goroutine's `reconnectOnce` returning
    `retry budget spent` in ~2us before the step starts, no socket swap
    during it, and `took` of 200.2, 201.0 and 201.0ms. The test also loses
    the occasional 60s resubscribe tail.
  - Mutations on the fixed test: M1, `rejectedReplacementUnsubscribeTimeout`
    always returning `subscriptionReconnectTimeout`, fails
    `Expected '1.004343079s' to be less than '600ms'`. M2, the step on plain
    `request()`, fails `So(returned, ShouldBeTrue)` (Line 1953, test 75.9s).
  - Siblings checked; none share the pattern. The `took < 1s` bounds in
    client_connect_test.go (lines 186, 269, 325) time a request on a Client
    with no subscription, so nothing contends for its lock, and they have
    800ms or more of margin. The other subscription Conveys bound
    `gaveUpAfter` by `ShutdownSocketWait` or `ClientMinRequestTimeout`, with
    seconds of margin, and time the poll goroutine itself rather than
    something racing it.
  - Gates: `make lint` 0 issues; `go test -count=5 -run TestSubscription
    ./jobqueue/` ok (236s); `make test` 711 passed / 20 skipped (6m51s);
    `CGO_ENABLED=1 make race` 711 passed / 19 skipped (10m6s).
