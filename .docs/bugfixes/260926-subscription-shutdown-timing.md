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
    budget is 24h in production. On the stop path it can wait behind this
    5s-bounded unsubscribe, and behind any request the caller makes
    concurrently on the same Client, which this fix does not bound.
  - Accepted risk: any request that holds a shared Client's lock for the
    whole 5s makes the unsubscribe give up without sending, leaving the
    subscription registered on a manager that may be alive and healthy.
    That covers a reconnect step against a slow manager, and also ordinary
    long-held requests a caller makes concurrently on the same Client: for
    example `Reserve(timeout)`, which a healthy manager holds open for up
    to its timeout while nothing is ready. wr's own callers (`wr add
    --sync`, `client.WaitForJobs`) make no such concurrent requests, but a
    Go API user sharing one Client can. A reconnect's replacement
    registration is still removed by `unsubscribeRejectedReplacement`.
  - After: the Convey passes with Unsubscribe taking 5.0004s and 5.0005s,
    with the stand-in resubscribe still ending on `receive time out`, which
    shows it held the lock for the whole floor.
  - Mutation M3, `unsubscribeServer` on `requestWithin` with the same 5s
    (receive deadline narrowed, lock wait uncounted), fails
    `Expected '11.500596418s' to be less than '6.4s'`.
  - CHANGELOG: "### Fixed" entry added.
  - Cancel-then-Unsubscribe, which the CHANGELOG claims (added after review):
    new Convey "Unsubscribe after cancelling the context is bounded against an
    unresponsive manager". Same setup as above, except the subscription keeps a
    full retry budget and a 1 minute retry wait, so it is still live when its
    context is cancelled. The test waits for the cancellation to stop it before
    calling `Unsubscribe`, so the context watcher's unsubscribe holds
    `unsubOnce` and `Unsubscribe` waits on it. In 4 of 4 traced runs `Err()`
    ended as `context canceled` joined with `errClientBusy`, which only the
    watcher's path sets, and cancel-to-return took 5.0001-5.0010s. With
    `unsubscribeServer` back on plain `request()` it fails with `Expected
    '19.999677825s' to be less than '6.4s'`: 10s waiting for the lock, then 10s
    on the floor, all while the caller waited in `Unsubscribe`.
  - Gates: `make lint` 0 issues; `go test -count=5 -run TestSubscription
    ./jobqueue/` ok (295s); `make test` 711 passed / 20 skipped (6m56s);
    `CGO_ENABLED=1 make race` 711 passed / 19 skipped (10m23s).
- [x] Reported race (from review of item 2): `unsubscribeServer` read `s.id`
  without `sockMu` while `replaceSock` writes it under `sockMu`, so an
  `Unsubscribe` landing just after `replaceSock` passed its `isStopping`
  check would unsubscribe the old id and leave the replacement registered,
  which `unsubscribeRejectedReplacement` doesn't cover.
  - Finding: the reported wrong-id outcome and data race do not happen on
    the code as it was, but only because of an ordering that nothing
    documented. `requestStop` closed `stop` outside `sockMu` and then called
    `closeSock`, which takes `sockMu.RLock`. A stop landing between
    `replaceSock`'s check and its swap therefore waited in `closeSock` until
    the swap finished, and `unsubscribeServer`, which always runs after a
    completed `requestStop`, then read the new id with a happens-before edge
    from `replaceSock`'s unlock. So the stop and replace decisions were not
    atomic, but the id read was saved by an unrelated lock.
  - Hook: `replaceSockDecidedHook`, a nil-in-production test seam that runs
    inside `replaceSock` after it decides to swap and before it swaps. New
    test `TestSubscriptionStopDuringReplace`: against a healthy manager, it
    registers a replacement and dials its socket as a reconnect would, then
    calls `replaceSock`. The hook starts `Unsubscribe` and gives the stop up
    to 200ms to land in the window. The test asserts that the replacement is
    no longer registered afterwards, and that the stop did not land mid-swap.
  - Before, under `-race`: the stop landed in the window (traced
    `stop landed in window: true`), the replacement was still removed, and
    there was no race report. The new test fails only on
    `So(stopLandedMidSwap, ShouldBeFalse)`. Mutation: with `closeSock`
    reading `s.sock` unlocked as well, the old code reports two DATA RACEs
    and leaves the replacement registered
    (`So(stillRegistered, ShouldBeFalse)` fails). That is the reported bug,
    one edit away.
  - Fix: `requestStop` closes `stop` and takes the socket to close under
    `sockMu.Lock`, the lock `replaceSock` decides and swaps under. So a stop
    either comes before the swap decision (which is then refused, and
    `unsubscribeRejectedReplacement` removes the replacement) or after the
    swap (and `unsubscribeServer` sends the replacement's id). Exactly one
    of them unsubscribes each registration. `unsubscribeServer` also reads
    `s.id` under `sockMu.RLock`, and its comment now says it must follow
    `requestStop` and why the id it reads is final. The comment also says
    that a long-held request on a shared Client can use up the 5s.
  - After, under `-race`, 3 of 3: stop landed in window false, replacement
    removed, no race report.
  - Gates (items 1-3 of this round): `make lint` 0 issues; `go test
    -count=3 -run TestSubscription ./jobqueue/` ok (237s), and with `-race`
    ok (258s); `make test` 712 passed / 20 skipped (7m21s);
    `CGO_ENABLED=1 make race` 712 passed / 19 skipped (10m23s).
- [x] Follow-ups from review of items 2 and 3, before the PR:
  - Test speed: `subscriptionUnsubscribeTimeout` is now a package var (still
    5s in production), with a test-only `setSubscriptionUnsubscribeTimeout`
    helper. The two unsubscribe Conveys set it to 1s, so everything derived
    from it scales down: the floor to 2s, the cancel Convey's socket wait
    to 4s, and the bound to 2.4s. `Unsubscribe` still takes exactly the
    bound, because the lock is held throughout.
    TestSubscriptionReconnectDuringManagerShutdown went from ~48s to
    24.7-29.1s over 3 runs. The mutations still fail both Conveys: M3
    (deadline narrowed, lock wait uncounted) gives 3.00s and 3.00s against
    2.4s, and plain `request()` gives 4.00s and 4.00s against 2.4s.
  - TestSubscriptionStopDuringReplace no longer asserts that the stop did not
    land mid-swap, because that describes the mechanism rather than the
    behaviour. The behavioural check stays: the replacement is not left
    registered. As a result the code before item 3's fix now passes this
    test, as it behaved correctly. The mutation with the stop and both
    reads taken outside `sockMu` still fails it under `-race`, with a DATA
    RACE and `So(stillRegistered, ShouldBeFalse)` failing.
  - `closeSock` is only used by tests now, so it moved to
    subscription_test.go.
  - Gates: `make lint` 0 issues; `go test -count=3 -run TestSubscription
    ./jobqueue/` ok (169s), and with `-race` ok (187s); `make test` 712
    passed / 20 skipped (6m57s); `CGO_ENABLED=1 make race` 712 passed / 19
    skipped (9m51s).
