# Tidy unticked bugfix checklist items (2026-10-04)

- Branch: `checklist-tidy-8e94211c`
- Base: `origin/develop` at `fc3f8a5d`
- Queue owner: this branch and this checklist
- Source: an audit of the unticked items in `.docs/bugfixes/*.md` on
  `fc3f8a5d`. Each claim was checked against the code and the cited
  checklists before acting on it.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: targeted `go test -tags netgo` runs of the touched tests,
`golangci-lint run` on the touched packages, and `cleanorder` on the edited Go
files.

- [x] A. Tick the unticked items that other branches already fixed, closed
  or ruled on, each with a one-line pointer to the fix or the reason, and
  annotate stale handoff text.
  - Each pointer was checked against the cited checklist entry, and the
    cited PR against `git log origin/develop`. 23 items ticked in
    `260903-14.md`, `260910-1.md`, `260927-client-token-reload.md`,
    `260927-race-kill-bury-flakes.md`, `260928-load-sensitive-flakes.md`,
    `260928-small-soak-findings.md`, `260929-archive-before-start.md` and
    `260930-dep-group-rerun-gaps.md`.
  - Stale handoff text annotated, not ticked: the `260910-1.md` header,
    `260929-readd-overwrites-running-job.md` (2 residuals),
    `260930-moved-on-runner.md` (port pickers), `260930-release-durability.md`
    (2 follow-ups) and `261001-flakes-and-tooling.md` (PeakRAM).
  - The `make lint` item in `260903-14.md` is closed on evidence rather than
    a fix: CI's golangci-lint workflow runs `make lint` and passed on
    `fc3f8a5d`, and #603 moved its baseline to `origin/master`.
- [ ] B. `260903-14.md` item: a `jobqueue_test.go` Convey uses `os.Getwd()`
  and leaves `jobqueue/jobqueue_cwd/` debris in the tree. Use a temp dir.
- [ ] C. `261001-flakes-and-tooling.md` item: `loadrunner` with no `-group`
  falls back to `req.Stringify()` (`100:1:1:0`), which reserves nothing in
  drive and hold modes. The README mode list omits churn.
- [ ] D. `260928-load-sensitive-flakes.md` item:
  `TestSubscriptionReconnectDuringManagerShutdown` fails under load (3 or
  more sightings). The 3s `ShutdownSocketWait` leaves too small a margin over
  the 2s resubscribe floor, which starts only after `pingUntilUnread`.
- [ ] E. `260928-load-sensitive-flakes.md` item: `TestStartDurability` and
  `TestReliable2ReserveConfirmedDeadReclaimed` fail with "could not reach the
  server". Act only on a concrete, verifiable weakness; otherwise close and
  watch.
