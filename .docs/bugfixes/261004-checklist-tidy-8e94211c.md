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
- [x] B. `260903-14.md` item: a `jobqueue_test.go` Convey uses `os.Getwd()`
  and leaves `jobqueue/jobqueue_cwd/` debris in the tree. Use a temp dir.
  - Red, on a tree with no `jobqueue/jobqueue_cwd`:
    `WR_TEST_SHARD=b CGO_ENABLED=1 go test -tags netgo -count 1 ./jobqueue
    -run '^TestJobqueueModify$'` (exit 0), then
    `test ! -e jobqueue/jobqueue_cwd` exited 1: the test left
    `jobqueue/jobqueue_cwd/4/f/0/<key>-<uuid>/cwd`, two empty workspaces.
    `git status --ignored` does not show it, because the tree holds only
    empty directories.
  - Fix (test only), `jobqueue/jobqueue_test.go`: "You can modify the cwd of
    a job, with and without cwd_matters" gives the job `t.TempDir()` instead
    of the package directory. Its assertions are unchanged.
  - Green: the same commands exit 0 and 0, sharded and unsharded.
  - Ticked in `260903-14.md` too.
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

## Backlog (not scheduled)

Unfinished work recorded only as prose in other checklists, some of it inside
ticked items. Listed here so a scan for unticked items finds it.

- [ ] `260904-4.md`, "A production finding for a separate PR": ext4 hands a
  freed inode to the next `mkdir`, so at scale `openChain` refuses a hashed
  level re-made by another run (`errNotBelowBaseDir`). Cleanup then reports a
  failure and leaves the empty hashed levels and the `<AppName>_cwd` base.
- [ ] `260927-queue-heap-retention.md`, "Not fixed, recorded for follow-up":
  the limiter never forgets time or datetime groups and keeps `toNotify`
  channels until a decrement; LSF `reservedElements` and `doomedElements`
  are not pruned; OpenStack `spawnCanceller` can keep an empty inner map per
  cmd.
- [ ] `260929-query-copies-and-warning-storm.md`, "Not fixed": each
  scheduling cycle still snapshots and walks the whole ready backlog
  (O(backlog)).
- [ ] `260927-subscription-reconnect-leak.md`, "Residual, not fixed": a
  resubscribe that registers but whose reply is lost strands that
  replacement subscription; only a manager-side reaper keyed on client
  liveness would cover it.
- [ ] `260930-runner-report-followups.md` item 3, "Noted, not fixed": a
  hand-made request with a zero ClientID can release or bury a job that never
  ran (ReservedBy zero).
