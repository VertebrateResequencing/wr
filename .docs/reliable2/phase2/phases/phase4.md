# Phase 4: Release-livelock give-up (section D)

Ref: [spec.md](../spec.md) sections D1, D2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout: the release and crash-recovery tests run under `-race`; each
acceptance test must fail before and pass after. Build/test with `-tags netgo`;
unset ALL `OS_*` env vars for `make test` / `make race`; GoConvey `So()`
assertions; copyright headers on new files.

This phase is LAST of the code phases. D1 is a one-line handler change, but it
is only SAFE once the KEEP'd recovery window (unchanged) is confirmed intact
(D2) and once Phase 3 reduces how often a live manager even sees a not-in-Run
release. It depends on the recovery window (already present) and is validated
after Phase 3 so the crash-recovery test runs against the finished
reserve/lost path. The two items are sequential (they share the new test file
`jobqueue/reliable2_release_test.go`): D1 (the code change) then D2 (the
crash-recovery + `retryTime` behaviour guard, no code change).

The client give-up set stays exactly `ErrBadJob`/`ErrBadRequest`
(`handleFinalStateError`, client.go:2123); connection errors are neither, so
crash-recovery retries continue. No new error types.

## Items

### Item 4.1: D1 - Not-in-Run release from a live manager returns ErrBadJob

spec.md section: D1

Make a live manager tell a runner whose failed command's reservation is gone
(the double-reservation loser) to give up promptly, so it abandons the dead
reservation and reserves its next job instead of looping for 24h.

Change `handleRelease` (serverCLI.go:1116-1134): call `getij(cr, true)` instead
of `getij(cr, false)`, mirroring `handleArchive` (serverCLI.go:1013). Then a
release whose item is not in the Run sub-queue returns `ErrBadJob` (getij,
serverCLI.go:1694-1697), landing in the client's give-up set
(`handleFinalStateError`, client.go:2123). A legitimate release (item in Run,
owner matches) is unchanged (`srerr == ""` -> `releaseJob` proceeds). This
covers both `jrelease` and `jbury` (serverCLI.go:1614-1617).

Distinction preserved:

- Manager up, item gone (superseded) -> `ErrBadJob` -> give up promptly (no
  24h/15s loop).
- Manager unreachable (crash) -> a connection error, NOT `ErrBadJob` -> keep
  retrying (Item 4.2).
- During the recovery window, a not-yet-restored item -> `getij` returns
  `ErrRecovering` (retryable), so a genuine unrecorded outcome still lands once
  recovery restores the item.

Tests in the new file `jobqueue/reliable2_release_test.go` (run under `-race`).
Covers all 3 D1 acceptance tests (map to Issue B2): (1) with a live manager and
a job key whose item is NOT in the Run sub-queue (removed by a winning runner),
a client's `jrelease` (a failed command's release) yields `ErrBadJob`
(fail-before: `getij(cr, false)` -> `releaseJob` -> `ErrNotRunning` ->
`ErrInternalError`); (2) in the client `reportFinalState` loop, an `ErrBadJob`
release error makes `handleFinalStateError` return `giveUp == true` and the loop
exits promptly (no 24h retry, no 15s reconnect storm), so the runner proceeds to
its next reserve (fail-before: `ErrInternalError` returned `giveUp == false`);
(3) a legitimate release of a job the client owns whose item IS in Run (normal
non-zero-exit release-for-retry) yields `nil` (or the job is buried after
retries) - regression guard that the fix does not break normal releases.

- [x] implemented
- [x] reviewed

### Item 4.2: D2 - Crash-recovery success still recorded; retryTime stays 24h

spec.md section: D2

Ensure a runner whose command SUCCEEDED while the manager was crashed can still
record that success when the manager restarts within `retryTime`, so an
expensive command is not needlessly re-run. This is what makes D1's give-up
safe: only a genuinely superseded reservation (item gone, manager up, not
recovering) receives `ErrBadJob`.

No code change to `retryTime` (stays 24h, `ClientRetryTime`) or to the KEEP'd
recovery window (`recoverInBackground`/`isRecovering`/`ErrRecovering`/
`rescheduleReadyAfterRecovery`; `confirmOrReleaseLostJob` permanently protects
recovered jobs via `recoveredRunningJobs`, server.go:3430-3433). On restart the
still-owned running job is recovered into Run, so the re-sent archive succeeds.
This item is a behaviour + guard test only.

Tests in `jobqueue/reliable2_release_test.go` (run under `-race`). Covers both
D2 acceptance tests (map to Issue B2 crash-recovery): (1) a job reserved+started
(PID `os.Getpid()`) with a genuine success being reported, when the manager is
stopped mid-report and restarted preserving the DB within `retryTime`, has its
re-sent archive accepted after recovery, is recorded `JobStateComplete`, and its
command is NOT re-run (`GetStatusByRepGroupMatch` shows
`Counts[JobStateComplete] == 1`) - guards that D1's give-up did not discard a
genuine unrecorded success; (2) `ClientRetryTime` is 24h (unchanged) - guard.

- [x] implemented
- [x] reviewed

## Regression guards (KEEP surfaces, section E1)

Re-run after this phase; all must stay green under `-race` (spec.md section E1):

- Background recovery window tests (`recoverInBackground`/`isRecovering`/
  `ErrRecovering`/`rescheduleReadyAfterRecovery`) - these directly underpin D.
- `jobqueue/subscription_test.go` (#503), `jobqueue/live_jtouch_test.go`
  (live RAM/CPU/STDOUT incl. ssh-to-host), the `JobUpdateResync` reconnect/
  resync tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, the
  `wr add --sync` client test.
- `jobqueue/reliable2_keep_test.go`, `jobqueue/reliable2_completion_test.go`,
  `jobqueue/reliable2_lost_test.go`, `jobqueue/reliable2_dbcompat_test.go`.
- `make test`, `make race`, `make lint` all clean (with all `OS_*` env vars
  unset).
