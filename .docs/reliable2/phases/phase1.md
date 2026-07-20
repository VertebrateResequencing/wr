# Phase 1: Completion and lost-path revert (sections A, B)

Ref: [spec.md](../spec.md) sections A1, B1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout; every acceptance test runs under `-race`. This is the PRIMARY
correctness fix: it reverts the completion and TTR/lost paths to v0.36.5
semantics so a successful command is never discarded and an alive owner is never
re-reserved. It depends on nothing and unblocks the rest of the spec. Phases 1
and 2 are the reliability core and should be reviewed together for the
completion/lost/deleted invariants (this phase fixes discard + lost; Phase 2
removes the `deleted` projection sibling).

Items are sequential: they cluster in `jobqueue/serverCLI.go` and
`jobqueue/server.go` and share the run/lost path. A1 makes the owner's archive
always accepted; B1 makes a TTR-expired alive job park in `SubQueueRun` with a
recoverable `Lost` flag - together they guarantee A1's oracle (a parked-lost job
whose owner later archives success).

## Items

### Item 1.1: A1 - A holding runner's successful archive is always accepted

spec.md section: A1

Restore v0.36.5's lenient archive acceptance in `jobqueue/serverCLI.go`:

- `handleArchive` (serverCLI.go:1013): call `getij(cr, true)` so the item must
  be in `queue.ItemStateRun` (preserves the `ErrRecovering` retry path and the
  owner check that yields `ErrMustReserve`).
- `markJobComplete` (serverCLI.go:1033): DELETE the
  `canCompleteFromQueueState(item.Stats().State)` gate
  (`canCompleteFromQueueState` at serverCLI.go:1064). Keep the owner check
  (`ReservedBy == cr.ClientID` else `ErrMustReserve`) and the end-state check
  (`canCompleteFromEndState` else `ErrBadRequest`). On success set
  `State=JobStateComplete`, `FailReason=""`, `Lost=false`, apply the end state,
  and return key/repGroup/schedulerGroup. A `Lost`-but-parked-in-`Run` job whose
  owner archives success is accepted, because the item is in `Run` and the owner
  matches.

Tests in the new file `jobqueue/reliable2_completion_test.go`. Rewrite the churn
oracle per spec note 1 as the "an alive job is never re-reserved" model, using
package helpers `jobqueueTestInit(true)`, `serve`, `Connect`, `disconnect`,
`restFormTrue`, `testCwdPath`; set `serverConfig.Timings.ItemTTR = 500ms`; start
runner A's job with `jq.Started(reserved, os.Getpid())` so the async
dead-confirmation finds the PID alive (the determinism trick from
`TestLostDetectionSilentRunner`). Because this rewritten oracle SUPERSEDES the
old harness test, DELETE `.docs/reliable2/harness/reliable2_churn_test.go` (or at
minimum its `TestReliable2DoubleReservationDiscardsSuccess`) so no stale test of
the old/broken discard behaviour remains and no duplicate `package jobqueue` test
file lingers under `.docs/` (acceptance #1).

Implementor hint for acceptance test 5 (the non-owner archive expecting
`ErrMustReserve`): the stale archiver must act while the item is still in `Run`
and owned by another client, exercised through the new `getij(cr, true)`;
otherwise `getij` returns `ErrBadJob` (item not in `Run` / not in queue), not
`ErrMustReserve`. When the item is in `Run`, `getij`'s own owner check
(`cr.ClientID != job.ReservedBy`) returns `ErrMustReserve`; `markJobComplete`
retains a matching owner check as a secondary guard.

Covers all 5 acceptance tests from A1 (parked-lost job stays in `SubQueueRun`
with `Lost==true`; runner B's 20 `Reserve` calls all return nil; owner A's
`Archive` returns nil; the rep group shows `Counts[JobStateComplete]==1` with the
command run exactly once; a genuine non-owner archiver gets `ErrMustReserve`).

- [ ] implemented
- [ ] reviewed

### Item 1.2: B1 - Pure v0.36.5 ttrCallback; F0 contact grace removed

spec.md section: B1

Revert the TTR/lost path and delete the `#550` F0 runner-contact grace in
`jobqueue/server.go` and `jobqueue/serverCLI.go`:

- `ttrCallback` (server.go:3515): DELETE the `if s.contactedWithin(job.Key(),
  ttr)` block (server.go:3535-3541). Retain the rest: zero-start/exited ->
  `SubQueueDelay`; already-`Lost` -> parked `SubQueueRun` (no re-mark/re-confirm);
  otherwise set `Lost=true`, `FailReason=FailReasonLost`, `EndTime=now`, defer
  `markJobLost`, return `SubQueueRun`.
- Delete all F0 symbols: `recordJobContact` (server.go:1070), `contactedWithin`
  (server.go:1084), `forgetJobContacts` (server.go:1102), the `lastContact` map +
  `lastContactMu` (server.go:785,806) and its init (server.go:2651), the
  `handleTouch` `recordJobContact` call (serverCLI.go:910), and the
  `emitChangeCallbackTransition` `forgetJobContacts` block (jobtransition.go:214-
  216). Removing the `forgetJobContacts` call site in `jobtransition.go` here
  keeps the package compiling once the function is gone; Phase 2 (C1) does the
  rest of the `emitChangeCallbackTransition` rewrite.
- A late `touch` still clears `Lost` and resets the TTR: `handleTouch`
  (serverCLI.go:905) minus its `recordJobContact` call; `touchJob` ->
  `recoverLostTouchedJob` clears `Lost`/`EndTime` and records the lost->running
  transition through the retained chokepoint. Genuine death is still handled by
  `markJobLost` -> `confirmOrReleaseLostJob` -> `confirmJobDeadAndKill`
  (unchanged). Do NOT re-introduce any contact-based grace (spec note 7): a
  spuriously-set `Lost` under saturation is benign because a `Lost` job is parked
  in `Run`, never re-reserved while its runner is alive, and its owner's success
  is always accepted (A1).

Tests: edit `jobqueue/lost_detection_test.go` to DELETE
`TestLostDetectionRecentContactNotLost` (it pins the removed F0 grace) and KEEP
`TestLostDetectionSilentRunner` (B1 acceptance test 1). Add B1 acceptance tests 2
and 3 in the new file `jobqueue/reliable2_lost_test.go`.

Covers all 3 acceptance tests from B1 (silent runner still detected `Lost` via
the KEPT `TestLostDetectionSilentRunner`; an on-time-touched job stays
`Lost==false`/`FailReason==""` and in `SubQueueRun` across >= 4 TTRs; a
`Lost` job recovers to `Lost==false` with its TTR reset on one late `Touch`,
staying in `SubQueueRun`).

- [ ] implemented
- [ ] reviewed

## Regression guards (KEEP surfaces, section H)

Re-run after this phase; all must stay green (spec.md section H1, plus
`TestLostDetectionSilentRunner` per section B):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner` (KEEP).
- `jobqueue/subscription_test.go` (`#503`), `jobqueue/live_jtouch_test.go`
  (`#530`/`#534`, incl. ssh-to-host), the `JobUpdateResync` reconnect/resync
  tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, and the
  `wr add --sync` client test.
- `make test`, `make race`, `make lint` all clean.
