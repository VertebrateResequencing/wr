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
- [x] Product bug, found by the reviewer of item 1: in the Go client,
  `Subscription.Unsubscribe` against an unresponsive manager could block the
  caller for 60s or more. `unsubscribeServer` was a plain `c.request()` on
  the 60s `ClientMinRequestTimeout` floor, and it competes for the same
  Client lock as the reconnect steps. With the production 24h
  `ClientRetryTime` budget, `resubscribeWithinBudget` and
  `unsubscribeRejectedReplacement` never narrow that floor, and time spent
  waiting for the lock counts against nothing. So an `Unsubscribe` arriving
  mid-resubscribe waited up to 60s for the lock and then 60s for its own
  request. A cancelled context goes through the same `unsubscribeServer`
  under `unsubOnce`, so a later `Unsubscribe` blocked behind it too. Callers:
  `wr add --sync` (cmd/add.go) and `client.WaitForJobs` (client/client.go),
  both via `defer sub.Unsubscribe()`.
  - The manager doesn't drop a subscription when its client goes away:
    `unregisterClientSubscription` is only called from `handleUnsubscribe`,
    the websocket teardown and a failed catch-up. So a bounded unsubscribe
    that gives up can leave a registration behind on a manager that is in
    fact alive.
  - Red (new Convey "Unsubscribe against an unresponsive manager is bounded,
    even behind a reconnect step" in
    TestSubscriptionReconnectDuringManagerShutdown).
    It uses a single RPC reader so the manager is deterministically in its
    unread shutdown window, a spent retry budget so the poll goroutine stays
    off the client, and a real `sub.resubscribeWithinBudget(now +
    ClientRetryTime)` in flight holding the lock. `ClientMinRequestTimeout`
    is shrunk for the test to `2 * subscriptionUnsubscribeTimeout` (10s).
    Before the fix: `Expected '11.50046666s' to be less than '6.4s'`, which
    is 10s of lock wait plus a 1.5s send timeout once the manager's socket
    closed. With the floor at 20s it was 21.5s.
  - Fix: `unsubscribeServer` uses a new `requestWithinIncludingLockWait`
    with `subscriptionUnsubscribeTimeout` (5s). That caps the lock wait (via
    `lockWithin`, which releases a lock that arrives after it gave up) and
    narrows the request's receive deadline to whatever is left. Unsubscribe
    is cleanup that runs as a caller finishes, so a manager that cannot
    answer a map delete in 5s is stopping or stalled. That bound is over
    1000 times a live manager's normal reply time and still returns promptly.
    Rejected alternatives: having the reconnect steps release the lock once
    stopping is set isn't possible, because a mangos Recv already waiting
    can't be interrupted without closing the Client's shared socket. Sending
    the unsubscribe in the background moves the hang to the caller's next
    `Disconnect`, which takes the same lock. Narrowing only the receive
    deadline (mutation M3 below) leaves the lock wait. The rejected
    replacement step still doesn't count lock wait against its budget. That
    budget is 24h in production, and after this fix the only other lock
    holder on the stop path is this 5s-bounded unsubscribe.
  - Accepted risk: if a reconnect or another goroutine's request holds the
    Client for the whole 5s while the manager is alive, the old
    subscription id stays registered on the manager. A reconnect's
    replacement registration is still removed by
    `unsubscribeRejectedReplacement`.
  - After: the Convey passes with Unsubscribe taking 5.0004s and 5.0005s,
    with the stand-in resubscribe still ending on `receive time out`, which
    shows it held the lock for the whole floor.
  - Mutation M3, `unsubscribeServer` on `requestWithin` with the same 5s
    (receive deadline narrowed, lock wait uncounted), fails
    `Expected '11.500596418s' to be less than '6.4s'`.
  - CHANGELOG: "### Fixed" entry added.
  - Gates: `make lint` 0 issues; `go test -count=5 -run TestSubscription
    ./jobqueue/` ok (295s); `make test` 711 passed / 20 skipped (6m56s);
    `CGO_ENABLED=1 make race` 711 passed / 19 skipped (10m23s).
