# reliable4 — Layer 2: owner-report reconciliation + idempotency

The "belt" of the holistic fix (user goals #1 server reconciliation + #2 client delivery
resilience). Layer 1 (backup coordination, f51af04) removed the DOMINANT freeze trigger; Layer 2
ensures that even if ANY residual freeze/overload still moves a job's item out of the Run
sub-queue (Part C not yet done, slow storage, or any future overload), an owner's final-state
report NEVER gets `bad job` -> discarded + re-run. This is the reverted `f4b9b55` idea done
PROPERLY — i.e. WITH the fix for the regression that got f4b9b55 reverted.

## Problem
Final-state report handlers resolve the job via `getij(cr, true)` which REQUIRES the queue item
in `ItemStateRun`. Under a freeze the item is moved out of Run (TTR -> lost -> release ->
Delay/Ready, or the first archive processed-then-reply-timed-out), so the runner's archive /
release / bury RPC is rejected `bad job (not in queue or correct sub-queue)`; the client treats
it as permanent and RE-RUNS already-successful work. (Prod: 6128 archive rejections on ~2000
succeeded compress jobs.)

## The fix — re-apply f4b9b55 + fix its regression
### From f4b9b55 (reachable at that commit; re-apply onto HEAD, files unchanged since 11910e3):
- `jobqueue/serverCLI.go` `getijForReport(cr) (*Job, string)`: resolve a FINAL-state report by
  OWNERSHIP + in-flight state, NOT sub-queue. Accept while the item is Run/Delay/Ready and
  `job.ReservedBy == cr.ClientID`; `ErrMustReserve` if a new owner took over (new-run-wins);
  `ErrBadJob` for terminal (Bury)/other; `ErrRecovering` if the item is missing during recovery.
- `jobAlreadyComplete(key)`: complete-bucket presence check (`db.retrieveCompleteJobsByKeys`) so a
  retry of an already-archived job returns success (idempotent), not `ErrBadJob` -> re-run.
- `handleArchive` and `handleRelease` (the latter also serves `jbury` via forceBury) use
  `getijForReport` + `jobAlreadyComplete`.
- `jobqueue/client.go` `handleFinalStateError`: also give up on `ErrMustReserve` (new-run-wins) so
  a stale runner abandons instead of looping the retry.
- `jobqueue/server_test.go` `TestSuccessfulArchiveOverridesLostReclaimBeforeRerun` sub-test 1:
  the v0.36.5 "released -> archive REJECTED, Complete==0" boundary becomes "owner's archive
  ACCEPTED, Complete==1" — that old boundary WAS the discard-and-rerun. (Deliberate, documented.)

### NEW — the regression fix f4b9b55 lacked (the reason it was reverted):
`jobqueue/server.go` `applyReleaseQueueChange`: it has idempotent cases for `bury`
(item already Bury) and `item.State == Delay`, but its `default` branch calls `q.Release(ctx,key)`
which requires the item in Run and ERRORS "not running" for an item in **Ready**. Since
`getijForReport` now accepts a release report for a Ready item (owner match), add a Ready case
MIRRORING the Delay one:
```
case item.Stats().State == queue.ItemStateReady:
    return currentState == JobStateReady, nil   // idempotent: already released to ready
```
(`applyReleaseQueueChange` returns `alreadyDone bool` — true when the item is already in the
target state, so `releaseJob` skips `finalizeReleasedJob`. Verify the Ready return value against
`releaseJobSnapshot`'s `currentState`.) Safe because ownership is already confirmed by
`getijForReport` before this point, so item-in-Ready means the manager released THIS runner's
reservation (TTR/lost) and the runner's release is redundant.

## Scope
Layer 2 = FINAL-state reports only: archive / release / bury. NOT `jstart`/`jtouch` — those are
keep-alive/ack, covered by Layer 1 (no more freezes delaying them past TTR) + the existing
reliable4 Started() handling (804e05a) + `ttrCallback` late-touch recovery + Layer 3. Revisit
start/touch only if report-storm-lsf shows residual jstart/jtouch churn after Layers 1+2.

## Regression test (TDD)
Bring back `jobqueue/reliable4_busyexit_test.go` `TestReliable4BusyExitStates` (from f4b9b55,
untagged, in `make test`): S1 (archive success after the item moved to Delay), S3 (idempotent
re-archive of an already-complete job), **S5 (failure release after the item moved to Delay/Ready
— this is the one the regression fix must make pass)** are RED without the fix; S2 (archive while
Lost-in-Run) and S4 (new-run-wins: stale owner rejected, job not completed out from under the new
owner) are guardrails. VERIFY the RED (S1/S3/S5 fail without the serverCLI.go + server.go fix).

## Must-not-regress boundaries
new-run-wins enforced by the ownership check (not sub-queue): keep TestReliable2Release (D1/D2),
TestSuccessfulArchiveOverridesLostReclaimBeforeRerun sub-test 2, S4 green. Also keep
TestReliable2Lost*, TestLostDetection*, TestReliable3Recovery*, TestReliable4* (incl. the
backstop/runner-pid liveness) and the Layer-1 backup-coordination tests green. Status-count
correctness for Delay/Ready->Complete (grep changeCallbackCounts / status-count-reconcile).

## Gates
`unset OS_*` then `make test` (~expect +S1-S5 tests, all pass); `make lint` 0 issues; `go vet`;
`go build -tags reliability_repro ./jobqueue/`. Validation is the busyexit unit test (forced
states) + reliable2/3 green — report-storm-lsf now drains cleanly post-Layer-1 so it won't
exercise the belt directly (the freeze trigger is gone); the forced-state test is the gate.
