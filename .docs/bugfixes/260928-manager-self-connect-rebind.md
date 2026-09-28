# Bugfixes 2026-09-28

fix-manager-self-connect-rebind

- [x] After a clean stop, the new manager exited with `bind: address already
  in use`. `ss` showed 127.0.0.1:PORT -> 127.0.0.1:PORT in TIME-WAIT. The
  mechanism: while the manager is down, local Go clients redial it. mangos
  redials every 100ms with no backoff (mangos v3 internal/core/socket.go
  around :32-34), and `wr manager start`'s readiness poll runs every 250ms
  (cmd/manager.go around :105 and :741-770). Eventually a dial's ephemeral
  source port equals the manager's port. TCP simultaneous open then connects
  the socket to itself. Go's dialer closes it, which leaves the port in
  TIME_WAIT for 60s. A dialled socket has no SO_REUSEADDR, so the manager's
  bind fails. The manager retries the bind for only 5s (jobqueue/server.go
  around :133-134, listenWithRetries around :2033), and it binds only after
  the whole of recovery. Linux gives out even source ports first, so even
  listen ports (the default web port, and any configured even port) are
  exposed. (Prodsim round 2 finding; probe `TestProdsimSelfConnectBlocksRebind`
  and `wrdev.sh selfconnect-check` on branch faux-develop.)
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestManagerPortSelfConnect -v`
    (new test, `jobqueue/port_selfconnect_test.go`). All four cases fail on
    develop 8cb5ff5f, exit 1, in 15s:

    ```
    Line 113:
    Expected '<nil>' to NOT be nil (but it was)!
    Line 138:
    Expected: jobqueue.pscPublication("published")
    Actual:   jobqueue.pscPublication("exited through publishexit")
    Line 162:
    Expected: nil
    Actual:   'the server's publication gave up'
    Line 187:
    Expected '<nil>' to NOT be nil (but it was)!
    --- FAIL: TestManagerPortSelfConnect (15.17s)
    ```

    In order: a socket can self-connect on a recovering manager's port (with
    the self-connect assertion removed, publication then exits through
    publishexit); a sweep of 20000 real `net.Dial`s to a recovering manager's
    even port leaves it in TIME_WAIT, so publication exits; a port held by a
    non-listening socket without SO_REUSEADDR (a stand-in for a TIME_WAIT that
    lasts 7s rather than 60s) for longer than 5s kills the manager; and Serve
    returns no error when another process listens on the port. After the fix
    all four pass, in 15s.
  - Slow version, a real TIME_WAIT made before the manager starts:
    `CGO_ENABLED=1 go test -tags netgo,reliability_repro --count 1 ./jobqueue -run TestManagerStartsOverSelfConnectTimeWait -v`
    (`jobqueue/port_selfconnect_repro_test.go`). It self-connects the port,
    checks `net.Listen` now fails with EADDRINUSE, and starts a manager there.
    After the fix: `the manager published after 1m1s`, PASS, logging that the
    port is still in use every 10s while it waits. Before it, the
    same scenario is the third red case above, with publication giving up
    after 5s.
  - Kernel semantics, tested on this host (5.15, ip_local_port_range
    32768-60999, tcp_tw_reuse=2) with a throwaway program, not assumed:
    - After a self-connect is closed, bind to the port fails with EADDRINUSE
      for 0.0.0.0 and 127.0.0.1, with SO_REUSEADDR and with SO_REUSEPORT. A
      dial to it is refused, so a TIME_WAIT can be told apart from a
      listener.
    - A socket bound with SO_REUSEADDR but not listening makes dials to the
      port get ECONNREFUSED, and Go's listener (SO_REUSEADDR) can still bind
      and listen beside it, so a handover leaves no gap. A reservation cannot
      be bound over a live listener.
    - Without a reservation, dialling a closed even port in a loop left it in
      TIME_WAIT after 6600 dials (0.28s). With the reservation held, 60000
      dials never took the port, and it was bindable afterwards: a port with
      a bind bucket that `bind()` made is skipped by `connect()`'s ephemeral
      search.
  - Options evaluated:
    - (a) Hold the ports from the start: done, as a bound but not listening
      reservation rather than an early listener. `.docs/dep-granularity/spec.md`
      E1 rejects a listening-but-unread socket, because mangos' first dial
      would succeed and the client's ping would burn its whole connect
      timeout. A reservation keeps "not up yet" as a fast ErrNoServer.
    - (b) Retry for longer than TIME_WAIT: done, in the reservation, 90s while
      nothing is listening on the port, logging every 10s. A reservation made
      while the port is already in TIME_WAIT (a self-connect while the manager
      was down) still has to wait it out.
    - (c) Stop wr's clients self-connecting: Go's net already detects the
      self-connect (tcpsock_posix.go `selfConnect`) and redials, and mangos'
      tls+tcp transport dials through `tls.DialWithDialer` with its own
      `net.Dialer`, so no wr client ever sees one. The close of the
      self-connected socket that Go does internally is what leaves the
      TIME_WAIT, and mangos exposes no dialer Control hook to set SO_LINGER 0
      on it. Not done.
    - (d) mangos backoff: `OptionReconnectTime`/`OptionMaxReconnectTime`
      exist, and would slow the sweep but not stop it, and would make every
      client slower to reconnect to a restarted manager. With (a) and (b)
      the sweep can no longer block the manager. Not done.
  - Fix:
    - New `jobqueue/port_reservation.go`. `reserveServerPorts` binds each port
      with SO_REUSEADDR (close-on-exec, under `syscall.ForkLock`, so a runner
      cannot inherit it) without listening. On EADDRINUSE it retries every
      500ms: for up to 5s while a dial to the port connects, then fails with
      "manager port P is in use by another process: bind: address already in
      use"; and for up to 90s while it does not. The web port gets the same
      retry but a failure only logs an error, as a web bind failure always
      has. A port that is not a positive number, or any other bind error,
      gets no reservation and falls through to publication's bind as before.
    - `jobqueue/server.go`: Serve reserves the ports straight after the
      certificates, before initDB, and releases them on an error return.
      Publication releases each reservation once its real listener is bound
      beside it (`persistTokenAndListen`, `serveWebInterface`), and shutdown
      releases both. `portStillListening` is replaced by the shared
      `localPortListening`. Doc comments on Serve, the bind retry constants
      and listenWithRetries say what the reservation does.
  - Tests changed, not weakened:
    - `TestDepGranularityStartupExitsWhenPortUnavailable`,
      `TestDepGranularityStartupRetriesPortBind` and
      `dgsStopWithForeignListener` bound their foreign listener before Serve.
      Serve now waits out a listener already on the port, so they bind it
      after Serve returns, beside the reservation (`dgsListenBesideReservation`),
      which is how another process can still take a port from a manager that
      has not published. Their assertions are unchanged, and they pass on
      develop too.
    - `TestServeFailsCleanlyWhenPortTaken` (260925-add-test-connect-flake
      item 5) takes the port before Serve and asserted
      errServePublishGaveUp. Serve now finds the port taken itself, so the
      serve helper returns Serve's "manager port P is in use by another
      process" error. The behaviour that item protects (an error for the test
      to fail on, not a dead test binary) is unchanged. cmd's
      tryStartTestServer retries a Serve error containing "address already in
      use", which the new error still contains.
  - CHANGELOG "### Fixed" entry, which says that even-numbered ports were
    exposed.
  - Review follow-up:
    - Reservations are Linux-only (`port_reservation_linux.go`, with
      `port_reservation_other.go` a no-op). BSD-derived kernels such as macOS
      let a wildcard SO_REUSEADDR bind succeed over another address's
      TIME_WAIT, so the bug does not arise there, and their rules for binding
      a listener beside a reservation differ and cannot be tested here. On
      non-Linux, Serve behaves as before the fix.
    - The Linux socket is made with SOCK_CLOEXEC, not socket() then
      CloseOnExec under ForkLock.
    - The serve test helper no longer retries a Serve error wrapping
      errPortInUse, which Serve already retried for 5s: that took
      `TestServeFailsCleanlyWhenPortTaken` from 25s to 5s.
    - New `TestManagerPortReservationRelease` checks that reservations are
      released on repeated reserve/release, on a Serve error after reserving,
      on publication, and on a stop inside the startup window, across three
      start/stop cycles on the same ports. Each of its Serve-level cases fails
      when the matching release is removed.
  - Gates: `make lint` 0 issues; `make test` 745 passed, 21 skipped;
    `CGO_ENABLED=1 make race` 745 passed, 20 skipped.
