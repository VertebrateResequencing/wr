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
   with the same jobs is idempotent and recovers in every case.

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
ErrDuplicateJobs; SubmitJobsAndReturnIDs is the idempotent recovery.

Unverified, by reading only: with Timeout over 60s, mangos's default
1-minute resend timer (wr never sets OptionRetryTime) could resend a
request a live but slow manager is still handling.

## Bugs

- [ ] A WaitForJobs, SubmitJobsAndWait or AddAndWait that survives a
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
- [ ] Budget-bounded requests (requestWithinLocked in jobqueue/client.go:
  the reconnect resubscribe, the rejected-replacement unsubscribe, and
  Unsubscribe via requestWithinIncludingLockWait) narrow only the receive
  deadline, so on a dropped pipe their Send can block for the client's full
  connect timeout, overrunning the budget. Before the item above the
  reconnect path was accidentally capped near 1s; now it can overrun by up
  to the client's timeout, so this is fixed here.
  - Source: reviewer of the item above.
- [ ] Scheduler.WaitForRunning returns the first poll error when the manager
  goes down, while WaitForJobs rides out a restart for the server's
  RetryTime; a long-running app waiting for a job to start has to restart
  its wait by hand.
- [ ] The client package and jobqueue.Connect docs do not state the
  restart/downtime contract (fail after Timeout while down, token reload
  only via the token-file route, interrupted batches may be partly added
  and then reported as ErrDuplicateJobs, SubmitJobsAndReturnIDs as the
  idempotent recovery, waits survive within RetryTime); jobqueue/doc.go's
  client example uses Connect(token) and a two-argument Add.
