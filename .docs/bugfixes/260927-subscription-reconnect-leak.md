# 260927: Go client subscription reconnect strands the old registration

Branch `fix-subscription-reconnect-leak`, based on `origin/develop` at
`3350615` (#622).

- [x] Found in review of #622: after a Go client `Subscription` reconnects
  following a poll error while the manager is still up, the old subscription
  id is never unsubscribed, so the manager keeps it.
  - Confirmed by reading the code. `poll` (jobqueue/subscription.go) sends
    any `requestUpdates` error to `reconnectAfterPollError`, then `reconnect`
    and `reconnectOnce`. That covers a send or receive error on the
    subscription socket (its receive deadline is `serverSubscriptionHoldTime`
    plus `subscriptionSocketRecvMargin`, 30s, so a stalled network trips it),
    a socket reset, and an error reply. None of these need the manager to have
    gone away. `reconnectOnce` swaps the client's command socket, dials a new
    subscription socket and sends a fresh `subscribe` request that carried no
    id. `handleSubscribe` (jobqueue/serverCLI.go) always mints a new id via
    `registerClientSubscription`. `replaceSock` then overwrites `s.id`, so
    the old id is known nowhere on the client. On the manager,
    `unregisterClientSubscription` runs only from `handleUnsubscribe`, the
    websocket teardown, a failed catch-up, and the shutdown sweep
    `closeClientSubscriptions`. Nothing drops a subscription when a client
    disconnects.
  - Effect: each such reconnect leaves a `serverSubscription` in
    `s.clientSubscriptions` for the manager's lifetime, with its
    `deliverQueuedUpdates` goroutine and both queues. It keeps
    `hasAnyClientSubscriptions` true, so every job transition loses the
    zero-subscriber early-out and walks the stranded entries. A manager
    restart does not leak, because the new manager's map starts empty.
  - Red: new Convey "A reconnect to a manager that is still up leaves it
    holding only the replacement" in
    `TestSubscriptionReconnectReleasesOldRegistration`. On a live manager it
    subscribes, closes the subscription's socket (`sub.closeSock()`, the test
    stand-in for a blip), waits for the `JobUpdateResync` that marks a
    completed reconnect, then asserts the old id is gone and exactly one
    registration remains. Before the fix it fails at
    `So(oldStillRegistered, ShouldBeFalse)` (`Expected: false / Actual:
    true`). The failure doesn't depend on timing: the Resync is published only
    after the resubscribe returns.
  - Fix: the resubscribe names the id it replaces, and the manager drops that
    id once the replacement is registered and caught up.
    `Subscription.subscribeRequest` sets `SubscriptionID` to the current id
    (read under `sockMu`, since tests call `resubscribeWithinBudget` off the
    poll goroutine). `handleSubscribe` calls `unregisterClientSubscription` on
    it after a successful catch-up. A failed catch-up still unregisters only
    the new id and leaves the old one for the client's next attempt, which
    names it again because `s.id` is unchanged.
  - Why not a best-effort unsubscribe of the old id after `replaceSock`, as
    the brief suggested: it is a second round trip on the Client lock, and
    #622 records that `requestWithinIncludingLockWait` can give up without
    sending when that lock is held for 5s. The server-side swap happens in
    the same request as the registration, so nothing new can wait or give up.
    It also stays safe against an older manager. See the next item: with
    counter ids, a client-side unsubscribe sent to a restarted manager would
    remove whichever client's subscription reused that number. An older
    manager ignores `SubscriptionID` on `subscribe`.
- [x] Hazard the fix would have introduced, found while designing it: ids were
  `sub-N` from a per-`Server` counter, so a restarted manager reuses `sub-1`,
  `sub-2` and so on. A client reconnecting after a restart names its old
  manager's id, which may now belong to another client's live subscription.
  Dropping it would break that client's poll with `unknown subscription` and
  force a reconnect and resync.
  - Red: new Convey "A resubscribe naming an old manager's subscription leaves
    a restarted manager's own ones alone". It registers a subscription on one
    manager, restarts it, subscribes a second client on the new manager, then
    sends a `subscribe` naming the first manager's id. With the swap in place
    and counter ids it fails at `So(otherStillRegistered, ShouldBeTrue)`
    (`Expected: true / Actual: false`). The second client had been given
    `sub-1` again, and the swap removed it.
  - Fix: `storeClientSubscription` mints `"sub-" + uuid.NewV4()`, so ids
    differ between manager runs, and the unused `nextSubscriptionID` field is
    gone. Clients and the status websocket treat ids as opaque, and no code
    or test parses the `sub-N` form.
- [x] Invariants from earlier subscription fixes, checked:
  - #622 (260926-subscription-shutdown-timing): exactly one unsubscribe per
    registration still holds. `requestStop` and `replaceSock` still decide
    under `sockMu`. If a replacement is swapped in, its id is the one
    `unsubscribeServer` sends. If it is refused, `unsubscribeRejectedReplacement`
    removes it, and the old id it replaced was already dropped by the
    resubscribe, so `Unsubscribe`'s send of the old id is a harmless no-op.
    If `Unsubscribe` drops the old id before the resubscribe arrives, the
    resubscribe's drop is a no-op. The bounded unsubscribe code paths are
    unchanged.
  - 260828-4 BUG 4/7: a subscribe refused by a stopping manager returns
    before the new code runs, and the retry budget bounds on the resubscribe
    and rejected-replacement unsubscribe are untouched.
  - 260905-1: no new unlocked access to `Client.sock` or `Subscription.id`;
    `subscribeRequest` reads `s.id` under `sockMu.RLock`, and no caller holds
    `sockMu` across it.
- [x] Residual, not fixed (pre-existing and outside this item): if a
  resubscribe registers on the manager but its reply is lost (the client's
  receive times out), that replacement is stranded. The next attempt names
  the old id, not the lost one. Only a manager-side reaper keyed on client
  liveness would cover it. #622's accepted risk also still stands: an
  `Unsubscribe` that gives up after 5s leaves its registration behind. A new
  client talking to an older manager, or an old client to a new manager,
  behaves as before the fix: it still leaks, with no collision risk.
- [x] CHANGELOG: "### Fixed" entry added.
- [x] Gates:
  - `make lint`: 0 issues.
  - `go test -count=3 -run TestSubscription ./jobqueue/`: ok (175.8s).
  - `CGO_ENABLED=1 go test -race -count=3 -run TestSubscription ./jobqueue/`:
    ok (210.9s).
  - `make test`: 716 passed / 20 skipped (7m9s).
  - `CGO_ENABLED=1 make race`: 716 passed / 19 skipped (10m50s).
