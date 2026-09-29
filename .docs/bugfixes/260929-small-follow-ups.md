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

- [ ] `internal.LogPanic` (`internal/utils.go:266`) logs only the recovered
  value (`clog.Crit(ctx, desc+" panic", "err", err)`), not the goroutine's
  stack, so a panic's log says nothing about where it happened.
- [ ] The Linux manager port reservation (`jobqueue/port_reservation_linux.go`)
  is an AF_INET socket on `0.0.0.0`, but the manager's listener is
  dual-stack. A manager port held only by an IPv6-only listener (such as
  rpc.statd's `[::]:45993` on this host) is reserved without complaint, so
  `Serve` returns nil, and publication then retries its listen for the 5s
  bind budget and exits ("could not listen on the manager port, so
  exiting"). It should fail at once in `Serve` with "manager port P is in
  use by another process". Found while fixing the test port pickers on
  `test-ci-reliability`.
