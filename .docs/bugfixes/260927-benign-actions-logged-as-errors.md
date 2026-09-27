# 260927: Normal client actions logged as errors

Branch `fix-benign-actions-logged-as-errors`, based on `origin/develop` at
`999b261` (#628). Finding 5 of the prodsim soak
(`.docs/bugfixes/260927-prodsim-findings.md` on branch `prodsim`).

- [x] wrstat-ui's hourly `SubmitJobs` of an empty list logs
  `jobqueue add(): bad request (missing arguments?)`. Production logs this every
  hour; the soak logged it 33 times. `client.Scheduler.SubmitJobs`
  (`client/client.go:1107`) could return early on zero jobs.
  - Where: the Go client sends `add` with `Jobs: nil` for an empty
    `var jobs []*Job`. `handleAdd` (jobqueue/serverCLI.go) refuses nil `Jobs`
    with `ErrBadRequest`, and `dispatchClientRequest` (jobqueue/server.go)
    logs every request error with `clog.Error`. The client only returns the
    error to its caller; it logs nothing.
  - Decision: keep the API and fix only the log level. An add of no jobs is
    still refused with `ErrBadRequest`, so no caller sees a change. The
    current wrstat-ui `scheduleSummarisers` already skips `SubmitJobs` when it
    has no jobs, so the hourly empty add in production comes from an older
    build or another poller. The prodsim `wrstatUI` actor submits the empty
    list on purpose to mimic it. Returning early in `SubmitJobs` would turn
    that error into success for every such caller. Note for the caller: a
    non-nil empty `[]*Job{}` is not caught by the nil check and goes on to
    `createJobs`, so nil and empty adds already differ. That is left alone.
- [x] Every finished `SubmitJobsAndWait` / `WaitForJobs` logs
  `jobqueue waitForUpdates(): subscription closed`, 69 times in the soak's last
  hour. The unsubscribe ends the in-flight long poll, and
  `handleWaitForUpdates` (`jobqueue/serverCLI.go:919`) returns it as
  `ErrBadRequest`.
  - Where: `waitForSubscriptionUpdates` (jobqueue/server_subscription.go)
    returns `errSubscriptionClosed` when the held subscription's `done`
    closes. Only `unregisterClientSubscription` (the client's unsubscribe, a
    reconnect's replacement, a failed catch-up) and the shutdown sweep close
    it, so it is never a failure. The shutdown case was already silenced by
    `inShutdown`. The Go client's `Subscription` logs nothing for it.
  - Decision: the reply to the client is unchanged (`ErrBadRequest`, which the
    stopping `Subscription` ignores). An unknown or missing subscription id is
    still logged as an error.
  - Red, both items: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue
    -run TestClientRequestErrorLogLevel` exited 1 before the fix:

    ```text
    Line 89:
    Expected 't=... lvl=eror msg="Server handle client request error" err="jobqueue add(): bad request (missing arguments?)"
    ' NOT to contain substring 'lvl=eror' (but it did)!
    Line 137:
    Expected 't=... lvl=eror msg="Server handle client request error" err="jobqueue waitForUpdates(): subscription closed"
    ' NOT to contain substring 'lvl=eror' (but it did)!
    ```

  - Fix: `replyError` (jobqueue/serverCLI.go) wraps the request's `Error` in
    `routineClientRequestError` when `isRoutineClientRefusal` says the refusal
    is normal: an `add` refused as a bad request with no jobs but an
    environment, or a `waitForUpdates` that failed with
    `errSubscriptionClosed`. The wrapper unwraps to the same `Error`, so
    `errors.As` callers are unaffected. `logClientRequestError`
    (jobqueue/server.go), split out of `dispatchClientRequest`, logs a
    wrapped error at debug level and everything else at error level as
    before.
  - Test: `jobqueue/client_request_log_test.go` drives `handleRequest` on a
    minimal server, then logs its error through `logClientRequestError` into
    a captured clog buffer. The two routine cases must log no `lvl=eror` line
    but still a `lvl=dbug` one, and the client must still get
    `ErrBadRequest`. A malformed add, an add of no jobs with no environment,
    and a wait on an unknown or missing subscription id must still log at
    error level.
  - CHANGELOG: Fixed entry added.
