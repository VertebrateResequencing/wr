# 260927: Go clients never recover from a clean manager restart

Branch `fix-client-token-reload`, based on `origin/develop` at `cb9bb03`
(#626).

- [x] Found in a production-like soak (`.docs/bugfixes/260927-prodsim-findings.md`
  on the prodsim branch): after a clean `wr manager stop` and start,
  long-lived Go clients fail every call with "bad token" and never recover.
  The stop deletes the token (`deleteToken` in cmd/manager.go), so the new
  manager generates a new one. The Go client reads the token only once, at
  connect, and stamps it on every request (`encodeAndSend` in
  jobqueue/client.go). In the soak every Go-client actor failed for the
  remaining 57 minutes. In production, ibackup's server and wrstat-ui would
  silently stop submitting until someone restarted them. A crash-style
  restart keeps the token, so it isn't affected.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run
    'TestClientTokenReload$'`, exit 1 before the fix:

    ```
    Line 114:
    Expected: nil
    Actual:   'jobqueue getlgs(): bad token: permission denied'
    ...
    --- FAIL: TestClientTokenReload (2.57s)
    ```

    The test serves a manager with a token file, connects a client with
    `ConnectWithTokenFile` (at that point a plain wrapper that read the file
    and called `Connect`, which is what `ConnectUsingConfig` did), stops the
    manager, deletes the token file as `wr manager stop` does, and serves
    again, which writes a new token. After a `Ping` (which needs no token)
    shows the client has found the new manager, `GetLimitGroups` must succeed.
    With the reload disabled, the concurrent-requests and subscription cases
    fail too: all 8 concurrent calls get "bad token", and the subscription
    never gets its `JobUpdateResync`.
  - How the client knows where its token came from: `ConnectUsingConfig` read
    `config.ManagerTokenFile`, but `Connect` takes only a raw token, and the
    Client kept only the bytes. The `client` package's `New`, which ibackup's
    server and wrstat-ui's watch use, goes through `ConnectUsingConfig`, so
    those callers need no change.
  - Fix (jobqueue/client.go): new exported `ConnectWithTokenFile(addr, caFile,
    certDomain, tokenFile, timeout)`, which `ConnectUsingConfig` now calls, and
    which records the path on the Client. `requestLocked` now sends a request,
    and if the reply is `ErrPermissionDenied` calls `reloadToken`, then sends
    once more only if that adopted a new token. `reloadToken` does nothing
    without a token file, if the file cannot be read, if its contents are not a
    43-byte token (a manager part-way through writing it), or if it holds the
    very token just rejected. There is no loop: a request is sent at most
    twice. Resending is safe for every method, because the manager's
    `validateRequest` rejects a bad token before dispatching anything.
  - Concurrency: `requestLocked` runs under the client's main lock, so
    requests are serialised and only one can reload at a time. The token is
    read outside that lock by `quickReconnect`, the subscription `reconnect`
    (both hand it to `Connect`), the subscription poll's `waitForUpdates`
    request on its own socket, and the REST client. It is now guarded by its
    own `tokenMu`, a leaf lock, and read with `currentToken()`, so those paths
    pick up a reloaded token without a data race. A subscription reconnecting
    after a clean restart resubscribes through `requestWithin`, so the
    resubscribe reloads the token and later polls use it. On the bounded paths
    from #622 (`requestWithin`, `requestWithinIncludingLockWait`) the resend
    reuses the same narrowed deadline, so in the one reload case a request can
    take up to twice its bound; the first send is answered straight away with
    the rejection, so in practice it adds one round trip.
  - Design decision: keep deleting the token on a clean stop, and reload in
    the client. `deleteToken` was added in 1e3d6d2 ("Manager now re-uses
    existing token files, only deleted on clean stop"), which keeps the token
    over a crash so runners can reconnect. A clean stop is the only way an
    operator gets a new token. That matters because the token is the
    manager's only credential and is copied beyond the manager's directory:
    wr uploads it to every cloud server it creates
    (`~/.wr_<deployment>/client.token`), where runners read it. Keeping it
    across clean restarts would mean a leaked token stayed valid for as long
    as the deployment existed. The client reload grants nothing new: it only
    works for a process that can already read the owner-only (0600) token
    file, which is exactly who could have connected afresh anyway.
  - Out of scope: `Connect` with a raw token, including wr's own CLI commands
    and runners, is unchanged. A runner cannot outlive a clean stop, since
    `wr manager stop` says runners die with the manager and a drain only
    stops once none are left. The REST client uses the current token but
    does not itself retry on a 401.
  - Tests: `TestClientTokenReload` in
    jobqueue/client_token_reload_test.go covers the restart, 8 concurrent
    requests after a restart, a subscription resubscribing after a restart, a
    raw-token client still being refused, an unchanged wrong token file
    causing no reload, and a changed but still wrong file causing exactly one.
