# Public Go client API across a manager restart with a new token

- Branch: client-restart-950a7a96
- Base: origin/develop at 6ec427cd
- Queue owner: client-restart-950a7a96, this checklist

## Investigation

Owner question: `wr runner` survives the manager going down and coming back
with a new token. Does a long-running app using the public Go API (client
package `Scheduler`, and jobqueue `ConnectUsingConfig`,
`ConnectWithTokenFile`, `Connect`) get the same?

1. While the manager is down, what do Scheduler.SubmitJobs,
   SubmitJobsAndWait, SubmitJobsAndReturnIDs, WaitForJobs and WaitForRunning
   do: retry until it's back (for how long, governed by which setting), or
   error immediately?
2. Does a WaitForJobs / SubmitJobsAndWait spanning the restart and token
   change complete correctly once the jobs complete, without the app calling
   again?
3. Is a SubmitJobs interrupted mid-restart retried safely by the package, or
   left to the app; can the app tell whether the add happened?
4. Do the direct jobqueue APIs behave the same with
   ConnectUsingConfig/ConnectWithTokenFile? Can plain jobqueue.Connect with
   token bytes reload, and do the docs say to use the token-file route?

### Findings

Probe: scratch tests in client/zz_restart_probe_test.go (not committed),
run with `timeout 600 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache] go test
-tags netgo -count 1 ./client/ -v -run TestRestartProbe`, exit 0, 73s. Clean
stop (`Stop(ctx, true)`, token file removed), restart from the same
production-deployment config, new token asserted. Scheduler Timeout 3s;
server RetryWait 200ms, RetryTime 30s.

1. Down: every Scheduler method (SubmitJobs, SubmitJobsAndReturnIDs,
   SubmitJobsAndWait, WaitForJobs, WaitForRunning) returns after exactly
   SchedulerSettings.Timeout (the socket send deadline) with a bare mangos
   `send time out`; no retry beyond that window, and none of those submits
   is applied later. If the manager is back within Timeout the call succeeds
   (old token rejected, token file reloaded, resent). A WaitForRunning
   already polling fails on its first poll after the stop. After the restart
   the same Scheduler works for every method without reconnecting.

   ```
   WaitForRunning in progress at stop      elapsed=3.1s    err=send time out
   SubmitJobs while down                   elapsed=3.002s  err=send time out
   WaitForJobs called while down           elapsed=3s      err=send time out
   SubmitJobs spanning a short outage (Timeout 10s, back after 1.018s) err=<nil>
   ```
2. WaitForJobs and SubmitJobsAndWait spanning the restart return the right
   jobs, all complete, no error, without the app calling again, whether the
   jobs complete before or after the resubscribe (catch-up delivers them).
   They resubscribe after RetryWait and give up with ErrSubscriptionClosed
   once the server's RetryTime (default 24h) is spent.
3. An Add in flight at the stop is resent by mangos's req socket when it
   redials (pipe-loss resend), then token-reloaded and resent again. The
   stopping manager can persist part of the batch and drop the reply
   ("reply to client failed: object closed"), so SubmitJobs then returns
   ErrDuplicateJobs for the app's own single add. If the outage outlasts the
   receive deadline (max(Timeout, 60s)) the app gets `receive time out` and
   cannot tell whether none, some or all were added. SubmitJobsAndReturnIDs
   with the same jobs (default options) recovers the jobs not yet complete,
   adding only those neither queued nor complete and returning the queued
   ones' keys; jobs already complete are neither re-added nor returned.

   ```
   attempt 3 stop after 790ms: SubmitJobs err=some of the added jobs were duplicates; manager has 20000/20000
   attempt 1 stop after 1.003s: SubmitJobs err=receive time out; manager has 16000/20000; ReturnIDs again ids=20000 err=<nil>
   ```
4. ConnectWithTokenFile / ConnectUsingConfig clients behave as the
   Scheduler. A Connect(token bytes) client gets ErrPermissionDenied for
   every request after the restart (Ping still works). Docs: Connect says
   nothing about restarts and misstates its timeout (receive deadline is
   max(timeout, 60s)); ConnectWithTokenFile says to prefer it for long-lived
   clients but not how downtime behaves; jobqueue/doc.go's client example
   uses Connect(token) and calls Add with two arguments; the client package
   has no package doc and SchedulerSettings is undocumented.

Wrong for a long-running app (fixed below): a subscription reconnect gives
the parent Client a socket with a 1s send deadline instead of Timeout
(jobqueue/subscription.go Client.reconnect, measured 1.0s vs 3.0s), and
WaitForRunning aborts on a transient outage that WaitForJobs rides out.

Acceptable, documented below: calls fail after Timeout while down; a batch
interrupted by a stop may be partly added and SubmitJobs then reports
ErrDuplicateJobs; SubmitJobsAndReturnIDs recovers the jobs not yet complete,
neither re-adding nor returning those already complete.

Unverified, by reading only: with Timeout over 60s, mangos's default
1-minute resend timer (wr never sets OptionRetryTime) could resend a
request a live but slow manager is still handling.

## Bugs

- [x] A WaitForJobs, SubmitJobsAndWait or AddAndWait that survives a
  manager restart leaves its jobqueue Client with a socket whose send
  deadline is the subscription reconnect step's (up to 1s) instead of the
  connect Timeout, so every later request fails fast or waits differently
  from what the app configured (Client.reconnect in
  jobqueue/subscription.go dials with min(remaining, 1s)).
  - Red command: `timeout 900 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    CGO_ENABLED=1 go test -tags netgo -count 1 ./jobqueue/ -run
    TestClientTokenReload`, exit 1:

    ```
    Line 173:
    Expected: time.Duration(3000000000)
    Actual:   time.Duration(1000000000)
    --- FAIL: TestClientTokenReload (6.39s)
    ```
  - Fixed: jobqueue/client.go `setRequestDeadlines` sets send = timeout and
    receive = requestTimeout(timeout); Connect and Client.reconnect
    (jobqueue/subscription.go) both use it, reconnect applying the client's
    own timeout to the adopted socket while its dial stays bounded by the
    step budget. Regression test in jobqueue/client_token_reload_test.go
    reads both deadlines after a real restart for a 3s and a 75s client.
  - Not fixed, separate: reconnect copies ServerInfo but not the retryWait,
    retryTime and touchInterval derived from it, which only drift if the
    restarted manager has different timing settings.
- [x] Budget-bounded requests (requestWithinLocked in jobqueue/client.go:
  the reconnect resubscribe, the rejected-replacement unsubscribe, and
  Unsubscribe via requestWithinIncludingLockWait) narrow only the receive
  deadline, so on a dropped pipe their Send can block for the client's full
  connect timeout, overrunning the budget. Before the item above the
  reconnect path was accidentally capped near 1s; now it can overrun by up
  to the client's timeout, so this is fixed here.
  - Source: reviewer of the item above.
  - Red command: `timeout 300 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 ./jobqueue/ -run
    '^TestRequestWithinBoundsSendToGoneManager$'`, exit 1 (a 300ms-bounded
    ping, 20s connect timeout, pipe detached after Stop):

    ```
    Line 348:            So(returned, ShouldBeTrue)
    Expected: true
    Actual:   false
    --- FAIL: TestRequestWithinBoundsSendToGoneManager (5.31s)
    ```
  - Fixed: jobqueue/client.go requestWithinLocked narrows each of the send
    and receive deadlines that is wider than the bound (never widening),
    re-narrows both to the remainder for the token-reload resend, and
    restores every narrowed one, registering the restore before narrowing.
    A bounded request can still take up to about twice its bound (send and
    receive each get it); the requestWithin doc says so. Ping now fails on
    its own timeout against a gone manager; its callers were checked.
    Tests: TestRequestWithinBoundsSendToGoneManager (client_connect_test.go),
    send-deadline checks in TestClientTokenReload's bounded-resend case, and
    part-way and send-only restore-failure cases in
    TestRequestWithinReportsRestoreFailure; nine mutants checked killed.
- [x] Scheduler.WaitForRunning returns the first poll error when the manager
  goes down, while WaitForJobs rides out a restart for the server's
  RetryTime; a long-running app waiting for a job to start has to restart
  its wait by hand.
  - Red command: `timeout 900 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 ./client/ -run
    TestSchedulerWaitForRunningAcrossManagerRestart`, exit 1:

    ```
    Line 981:   (restart test: the wait must still be running while the manager is down)
    Expected: (*errors.errorString){s:"timed out waiting for WaitForJobs"}
    Actual:   errors.err("send time out")
    --- FAIL: TestSchedulerWaitForRunningAcrossManagerRestart (94.48s)
    ```
  - Fixed: client/client.go waitForRunning keeps its first poll failing fast
    (as WaitForJobs does), then polls through errors meaning the manager is
    unreachable (mangos send/receive timeouts, ErrClosedStop, ErrRecovering)
    until that spell outlasts the manager's ServerInfo.RetryTime (default
    jobqueue.ClientRetryTime); an answered poll resets the spell and other
    errors still return at once. ErrClosed is excluded: it only follows
    Disconnect. Doc comment states the contract. Tests: restart, repeated
    outages, stays-down and ctx-cancel cases in
    TestSchedulerWaitForRunningAcrossManagerRestart, error-table cases in
    TestSchedulerWaitForRunning.
- [x] The client package and jobqueue.Connect docs do not state the
  restart/downtime contract (fail after Timeout while down, token reload
  only via the token-file route, interrupted batches may be partly added
  and then reported as ErrDuplicateJobs, SubmitJobsAndReturnIDs as the
  idempotent recovery, waits survive within RetryTime); jobqueue/doc.go's
  client example uses Connect(token) and a two-argument Add.
  - Fixed: client/doc.go package doc states the restart contract (token-file
    reload, calls fail after Timeout while down, waits ride out RetryTime,
    interrupted batches and ErrDuplicateJobs, recovery with
    SubmitJobsAndReturnIDs and its limits for completed jobs);
    SchedulerSettings fields, New, SubmitJobs, Error and KillJobs docs in
    client/client.go; Connect and ConnectWithTokenFile docs in
    jobqueue/client.go; jobqueue/doc.go examples use ConnectUsingConfig and
    the real Add and Serve signatures; CHANGELOG entries under Fixed.
- [x] A subscription reconnect adopts the new manager's ServerInfo but keeps
  the retryWait, retryTime and touchInterval derived from the old one
  (Client.reconnect in jobqueue/subscription.go), so a manager restarted
  with different timings leaves the client using stale ones.
  - Source: coordinator, from the PR's "for the owner" list.
  - Red command: `timeout 600 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 ./jobqueue/ -run
    '^TestSubscriptionReconnectAdoptsManagerTimings$'`, exit 1:

    ```
    Line 1842:
    Expected: time.Duration(3000000000)
    Actual:   time.Duration(2000000000)
    --- FAIL: TestSubscriptionReconnectAdoptsManagerTimings (1.65s)
    ```
  - Fixed: Client.reconnect calls adoptServerTimings under the client lock,
    deriving each timing from the new ServerInfo by establishServerInfo's
    rule and keeping a field that differs from the old manager's derived
    value (a test override). The fields are guarded by a new timingsMu and
    read through currentTouchInterval/currentRetryWait/currentRetryTime.
  - Noted, not a bug: quickReconnect (Execute's final-state retry) swaps only
    the socket, which handleFinalStateError has already closed, and keeps the
    old ServerInfo; Execute reads touchInterval once at start.
- [x] With SchedulerSettings.Timeout over 60s, mangos's req socket may resend
  a request to a live but slow manager after its default 1-minute resend
  time (wr never sets OptionRetryTime), so the manager could get the same
  request twice. Prove with a test whether it can; if so, prevent it,
  keeping adds idempotent.
  - Source: coordinator, from the PR's "for the owner" list.
  - Proof (scratch run, real Connect/Add against a TLS rep-socket stand-in
    that counts copies and holds its reply, not committed): mangos v3.4.2
    arms a 1-minute resend timer per send and resends on the live pipe.

    ```
    timeout1s: hold=1m2s Add took 1m0.099s added=0 err=receive time out; manager received 1 add copies
    timeout120s: hold=1m5s Add took 1m5.015s added=1 err=<nil>; manager received 2 add copies
    ```
  - Red command: `timeout 900 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 ./jobqueue/ -run
    '^TestClientDoesNotResendSlowRequests$'`, exit 1: `Expected '1m0s' to
    be greater than '2m0s'`.
  - Fixed: jobqueue/client.go setConnectSocketOptions sets
    mangos.OptionRetryTime to clientRequestResendTime (MaxInt64), turning off
    timer resends while keeping the resend after a dropped connection (0
    would cancel those). After the fix the scratch run saw 1 copy. Test
    jobqueue/client_resend_test.go checks resend time exceeds the receive
    deadline for 120s and 10h timeouts and that a pipe-loss add still
    succeeds; the 0 and 3-minute mutants fail it.
- [x] A SubmitJobs whose add was in flight when the manager stopped, partly
  persisted and then resent to the restarted manager returns
  ErrDuplicateJobs for jobs only that call added. Its outcome should look
  like success, or be a clear typed error saying which jobs were added if
  success is not safe, and be documented.
  - Source: coordinator, from the PR's "for the owner" list.
  - Red command: `timeout 900 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 -timeout 20m ./client/ -run
    '^TestSchedulerSubmitJobsResentAfterItsReplyWasLost$'` (a TCP proxy
    drops the manager's add reply, so mangos redials and resends), exit 1:

    ```
    Line 180:
    Expected: nil
    Actual:   'some of the added jobs were duplicates'
    --- FAIL: TestSchedulerSubmitJobsResentAfterItsReplyWasLost (0.54s)
    ```
  - Fixed: requestOnceLocked watches add attempts with a chained pipe event
    hook (jobqueue/client_resend.go) and marks the request resent if a new
    pipe attaches after Send returned (mangos's pipe-loss resend);
    requestRidingOutOutages marks it after a receive-timeout retry; a
    token-reload resend alone does not count. New AddDuplicates.Resent and
    Client.AddWithDuplicatesContext; SubmitJobs returns nil when every job
    was added or the add was resent (with ignoreComplete false every
    duplicate is a queued job). Residual, documented: after a resend,
    identical jobs someone else queued earlier also count as success; rare
    false positives when a pipe breaks without the first copy arriving.
- [ ] SchedulerSettings.Timeout has no default: with the zero value, Connect
  gives the socket a send deadline of 0 (no deadline in mangos), so
  client.New with the manager down, and requests during an outage, may block
  until the manager returns instead of failing with ErrSendTimeout; the
  docs assume a non-zero Timeout. Test Timeout 0, then document the
  zero-value behaviour or give it a backwards-compatible default.
  - Source: coordinator (owner asked what Timeout defaults to).
  - Superseded by the owner decision below: Timeout gets a documented
    default when zero (item below), rather than only documenting it.

### Owner decision (via coordinator, 261002)

Make the client consistent with runners and waits: (1) client.New and
Connect* stay bounded by Timeout so a down or wrong manager fails fast at
startup, and a zero Timeout gets a sensible documented default rather than
blocking for ever; (2) once connected, requests ride out manager outages by
retrying with backoff up to the manager's RetryTime, reusing the token reload
and a safe resubmit of an interrupted add (no ErrDuplicateJobs for the app's
own jobs), logging warnings while retrying; (3) calls that take a context
honour it, and context-taking variants are added for those that do not,
keeping existing signatures; (4) a CHANGELOG entry for the behaviour change.

- [x] A zero SchedulerSettings.Timeout (and a zero Connect timeout) leaves the
  socket with no send deadline, so client.New with the manager down can block
  instead of failing fast; give zero a documented default.
  - Red command: `timeout 900 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 -timeout 20m ./jobqueue/ -run
    '^TestConnectWithoutTimeoutUsesDefault$'`, exit 1:

    ```
    client_connect_test.go Line 558: Expected: true  Actual: false   <- timeout 0, listener that never answers: Connect had not returned after 5s
    client_connect_test.go Line 604: Expected: time.Duration(120000000000)  Actual: time.Duration(0)   <- send deadline 0
    client_connect_test.go Line 625: Expected: true  Actual: false   <- request during an outage had not returned after 5s
    --- FAIL: TestConnectWithoutTimeoutUsesDefault (16.22s)
    ```
    client.New with Timeout 0 against a listener that never answers also
    blocked (10s scratch probe).
  - Fixed: new jobqueue.ClientDefaultConnectTimeout (120s, the CLI's
    default); Connect replaces a timeout <= 0 with it before dialling and
    stores it, so dial, readiness ping, request deadlines and reconnects all
    use it. cmd defaults of 120 now derive from it (unchanged values).
    Documented on Connect, SchedulerSettings.Timeout and the client package
    doc. `wr limit`, which passes an unset global 0, now gets 120s instead of
    no deadline.
- [x] Once connected, Scheduler requests (SubmitJobs, SubmitJobsAndReturnIDs,
  GetJobByKey, Find*, KillJobs, RemoveJobs, GetSchedulerAlerts, ...) fail
  after Timeout while the manager is down instead of retrying with backoff up
  to the manager's RetryTime, logging warnings, as runners and waits do.
  - Red command: `timeout 900 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 -timeout 20m ./client/ -run
    '^TestSchedulerRequestsAcrossManagerRestart$'`, exit 1:

    ```
    Line 456:
    Expected: (*errors.errorString){s:"timed out waiting for WaitForJobs"}
    Actual:   errors.err("send time out")
    Line 495:
    Expected '1.002568609s' to be greater than or equal to '3s' (but it wasn't)!
    --- FAIL: TestSchedulerRequestsAcrossManagerRestart (4.51s)
    ```
  - Fixed: opt-in jobqueue.Client.RetryWhileManagerUnreachable, enabled only
    by client.New (CLI, runners and manager connections unchanged). request()
    goes through requestContext, which for an opted-in client retries with
    jittered backoff (from min(250ms, RetryWait), capped at RetryWait,
    floored at 10ms) until the outage outlasts RetryTime or ctx is done,
    taking the client lock per attempt only, warning on the first failure and
    then once a minute, and logging when the manager answers again
    (jobqueue/client_outage.go). Send timeouts, ErrClosedStop and
    ErrRecovering are always retried; a receive timeout only for requests
    safe to apply twice (reads, kill, delete, and adds that skip complete
    jobs). WaitForJobs, SubmitJobsAndWait and WaitForRunning pass their ctx
    through, so cancelling ends the ride-out. This supersedes the
    WaitForRunning item's fail-fast first poll and its unreachableSpell, and
    Findings item 1 (calls no longer fail after Timeout once connected).
- [x] Scheduler calls without a context cannot be given up early once they
  retry; add context-taking variants, keeping the existing signatures.
  - Red command: `timeout 900 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 1 -timeout 20m ./client/ -run
    '^TestSchedulerRequestsAcrossManagerRestart$'`: first a compile failure
    (`s.SubmitJobsContext undefined`), then with ctx-ignoring stubs exit 1:
    every variant false in the "ended with context.Canceled" map.
  - Fixed: Scheduler SubmitJobsContext, SubmitJobsAndReturnIDsContext,
    GetJobByKeyContext, the four Find*Context, GetLastCompletionTimeByRepGroupContext,
    KillJobsContext and RemoveJobsContext, with jobqueue.Client AddContext,
    AddAndReturnIDsContext, GetByRepGroupMatchContext,
    GetIncompleteByRepGroupMatchContext, GetLastCompletionTimeByRepGroupContext,
    KillContext and DeleteContext; the plain methods wrap them with
    context.Background(). GetSchedulerAlerts (REST) has none yet.
- [x] TestSchedulerWaitForRunningAcrossManagerRestart ("rides out repeated
  outages", client/client_test.go ~315, added by this branch's WaitForRunning
  commit) fails intermittently under `CGO_ENABLED=1 -race` on the full
  client package: `Expected "timed out waiting for WaitForJobs", Actual
  "send time out"` (2 of 9 runs); likely no poll answered in the 1s window
  after a restart under load, so one unreachable spell spans both 3.5s
  outages and exceeds the 6s RetryTime.
  - Source: reviewer of the mangos resend item.
  - Not reproduced after 90913194: 60 targeted -race iterations and 8 full
    client race runs, two at a time, all passed. Each poll is now its own
    request with its own outage clock, so the two 3.5s outages only add up if
    one request goes unanswered through the at least 1s the manager is up
    between them; measured, in-flight requests were answered 93-140ms after
    each restart. No change made.
- [ ] After a manager restart, a subscription's first poll can reach the new
  manager with the old manager's subscription id, which the new manager
  logs at error level. Check whether the client then resubscribes without
  losing updates, and whether the log is noise for an expected event.
  - Source: coordinator, from #666's soak runtime checks.
  - Evidence (soak8 local1 manager log, one per restart, within a second of
    "wr manager started"):

    ```
    t=2026-10-02T20:09:08+0100 lvl=info msg="wr manager  started on [host]:51962, pid 2607562"
    t=2026-10-02T20:09:09+0100 lvl=eror msg="Server handle client request error" err="jobqueue waitForUpdates(): unknown subscription" caller=clog.go:344
    t=2026-10-02T20:14:02+0100 lvl=info msg="wr manager  started on [host]:51962, pid 2655220"
    t=2026-10-02T20:14:02+0100 lvl=eror msg="Server handle client request error" err="jobqueue waitForUpdates(): unknown subscription" caller=clog.go:344
    ```
- [ ] Scheduler.GetSchedulerAlerts goes over REST (jobqueue/client_rest.go),
  so it neither rides out an outage nor reloads the token: after a restart
  with a new token it likely fails with HTTP 401 until a mangos request
  reloads the token (restGet sends currentToken() and never reloads on 401;
  inferred from the code). Reading alerts dismisses issues, so it is not
  safe to resend after a response timeout.
  - Source: implementor of the outage-retry item.
- [ ] developers/prodsim uses client.New, so since 90913194 its actors wait
  through manager outages (up to RetryTime) instead of erroring after
  Timeout; check prodsim (and #666's developers/soak analysers, if merged)
  still report outages meaningfully, e.g. as time blocked rather than
  errors, and adjust here if needed.
  - Source: coordinator.
- [x] TestSubscriptionReconnectAdoptsManagerTimings (added by e89991b8) is
  flaky: 2 of 6 runs fail reading jq.ServerInfo.RetryTime right after the
  resync update (`Line 1834: Expected: time.Duration(31000000000) Actual:
  time.Duration(30000000000)`); either the resync can arrive before the
  reconnect adopts the new ServerInfo, or the test's read is unsynchronised.
  - Source: implementor of the context-variants item.
  - Red command: `timeout 1500 nice -n 19 env GOFLAGS=-p=2 GOCACHE=[cache]
    go test -tags netgo -count 20 -timeout 20m ./jobqueue/ -run
    '^TestSubscriptionReconnectAdoptsManagerTimings$'`, exit 1, 1 of 20
    (2 of 40 instrumented): `Line 1834: Expected 31s, Actual 30s`.
  - Cause (product bug): a stopping manager still answers Ping while it
    drains, so the reconnect adopted the old manager's ServerInfo; its
    resubscribe was refused, resent by mangos to the restarted manager and
    accepted, and the resync was published with stale info and timings.
  - Fixed: the subscribe reply carries the manager's ServerInfo
    (serverInfoCopy, shared with handlePing); reconnectOnce adopts it via
    Client.adoptServerInfo after replaceSock, so a resync means the client
    holds the info of the manager that registered it. New deterministic case
    restarts the manager between the reconnect's connect and resubscribe
    (red 5 of 5 before). Original test green at -count 30, plain and race.
- [ ] An add mangos resends after its pipe drops can re-add a job the first
  copy queued and that has since completed, so it runs twice: SubmitJobs
  (ignoreComplete false) re-adds complete jobs, and the pipe-loss resend
  bypasses wr's own rule of not resending such adds after a receive
  timeout. Implausible after a stop and restart (mangos redials every
  100ms), more plausible when a live manager's reply is slow and the
  connection loss is noticed tens of seconds later.
  - Source: implementor and reviewer of the resent-add item.
