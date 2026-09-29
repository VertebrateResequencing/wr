# 260929: small follow-ups

Branch `small-follow-ups`, based on `origin/develop` at `00be340` (#649).

Checked and already fixed on develop, so not on this branch:

- A reconnect after a transient poll error left the old subscription id on
  the manager. Fixed by #625 (`260927-subscription-reconnect-leak.md`).
- `developers/wrdev.sh report-storm-lsf` always exited 1, because it ended
  with `[ -n "${WRDEV_PROD_BKFILE:-}" ] && rm -f ...`. Now fixed:
  `cmd_report_storm_lsf` ends with `return "$verdict"` (1c4a9415).
- F3: the status web page decoded the whole history to count it. Fixed by
  #640 (`260927-status-page-count-decoding.md`). `writeStatusCountSeed` uses
  `retrieveCompleteJobStatusByRepGroup(rg, false)`.
- F5: benign actions were logged as errors. Fixed by #640
  (`260927-benign-actions-logged-as-errors.md`).

- [x] `internal.LogPanic` (`internal/utils.go:266`) logs only the recovered
  value (`clog.Crit(ctx, desc+" panic", "err", err)`), not the goroutine's
  stack, so a panic's log says nothing about where it happened.
  - The Crit handler's own `stack` field stops at the first `runtime.` frame,
    which inside a deferred recover is `runtime.gopanic`, so it always ended
    inside LogPanic: `stack="[clog.go:350 utils.go:268]"`.
  - Red: `CGO_ENABLED=0 go test -tags netgo -count=1 ./internal/ -run
    TestLogPanicLogsThePanicSite` failed: the log was only `msg="test
    goroutine panic" err="deliberate test panic" stack="[clog.go:350
    utils.go:268]"`, with no `internal.panicsUnderLogPanic(`.
  - Fix: `internal/utils.go` LogPanic also logs `panic_stack`, which is
    `debug.Stack()` taken in the deferred recover and includes the panic
    site. It is a new key because the handler appends its own `stack`. The
    message, `err` and the `die` exit are unchanged. Test in
    `internal/utils_test.go`. CHANGELOG: a Fixed entry in a new Unreleased
    section.
  - Reviewer: PASS. A panic in another goroutine logged that goroutine's
    stack down to the exact panic line. `make lint` reports 0 issues.
- [x] The Linux manager port reservation (`jobqueue/port_reservation_linux.go`)
  is an AF_INET socket on `0.0.0.0`, but the manager's listener is
  dual-stack. A manager port held only by an IPv6-only listener (such as
  rpc.statd's `[::]:45993` on this host) is reserved without complaint, so
  `Serve` returns nil, and publication then retries its listen for the 5s
  bind budget and exits ("could not listen on the manager port, so
  exiting"). It should fail at once in `Serve` with "manager port P is in
  use by another process". Found while fixing the test port pickers on
  `test-ci-reliability`.
  - Red: a new Convey in `TestManagerPortSelfConnect`, "A manager port an
    IPv6-only listener holds fails Serve fast and says so",
    `CGO_ENABLED=0 go test -tags netgo -count=1 ./jobqueue -run
    'TestManagerPortSelfConnect$'`, failed at `port_selfconnect_test.go:232`:
    `Expected '<nil>' to NOT be nil`. Serve returned nil.
  - A second cause: with only a dual-stack reservation, the same test failed
    after 103s with `manager port P is still held by a socket that is not
    listening (such as a connection in TIME_WAIT) after 1m30s`.
    `localPortListening` dialled only IPv4 loopback, so an IPv6-only listener
    looked like a TIME_WAIT and got the 90s linger wait.
  - Fix:
    - `jobqueue/port_reservation_linux.go` reserves with an AF_INET6 socket
      on `[::]`, with SO_REUSEADDR and IPV6_V6ONLY explicitly 0, matching
      Go's dual-stack listener. If that fails for any reason other than
      EADDRINUSE, such as no IPv6, it reserves on AF_INET `0.0.0.0` as
      before.
    - `jobqueue/port_reservation.go` `localPortListening` dials both
      `127.0.0.1` and `::1`.
    - CHANGELOG: a Fixed entry.
  - Pinned behaviours were checked on this 5.15 kernel:
    - The dual-stack reservation still conflicts with every kind of existing
      listener.
    - Dials to both loopbacks are refused fast while the reservation is
      held.
    - The manager's listener and SO_REUSEADDR binders such as
      `dgsListenBesideReservation` can still bind beside it.
    - 30000 dials never took the reserved port for an ephemeral source port,
      whether over IPv4 or IPv6.
    - The TIME_WAIT, release and IPv4 fast-fail tests pass unchanged.
  - Reviewer: PASS. Red, the 90s intermediate failure and green were all
    confirmed. There are no fd leaks, and the fallback on non-EADDRINUSE
    errors is the old behaviour. `GOOS=darwin go build ./jobqueue` passes.
    The focused tests pass with and without `-race`, and `make lint` reports
    0 issues.
  - Noted, not new: `GOOS=darwin go vet ./jobqueue` fails at
    `jobqueue/utils_test.go:519` (`st.Dev` is int32 there).
