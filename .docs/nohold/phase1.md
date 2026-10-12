# Phase 1: queue API

Ref: [spec.md](spec.md) sections A1, A2, A3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

No jobqueue behaviour changes in this phase: the hook is unset and the
`With` callbacks are nil. The items run in sequence, all in
`queue/queue.go`: A2's tests order its callbacks against A1's hook, and A3
changes the ready-added callback code that A1's single ready-push helper
sits beside. This phase is independent of phase 2 and may run in parallel
with it; both must be reviewed before phase 3 starts.

## Items

### Item 1.1: A1 - The hook runs for every entry into ready

spec.md section: A1

Add `ReadyHook` and `SetReadyHook` (spec.md "queue API"), and route every
push into the ready sub-queue through one internal helper that calls the
hook under the queue lock, after the operation's own changes (including
`KickWith` and `ResumeWith` callbacks) and before the push, pushing under
the group the hook returns. In `Update` and `updateUnlessRunning`, apply
the caller's reserve group and priority before the push, keeping
`applyItemPropertyChanges`' delay-first precedence. Covering all 7
acceptance tests from A1, in new `queue/ready_hook_test.go`.

- [x] implemented
- [x] reviewed

### Item 1.2: A2 - Callbacks on release, requeue and bury

spec.md section: A2

Add `ReleaseWith`, `RequeueWith` and `BuryWith`, calling the callback with
the item's data under the queue lock once the item is known to be in Run
and before it leaves it, and never on a failed call; make `Release`,
`Requeue` and `Bury` the `With` variants with a nil callback. Covering all
6 acceptance tests from A2, in new `queue/release_with_test.go`. Depends on
item 1.1's hook.

- [x] implemented
- [x] reviewed

### Item 1.3: A3 - HasReady and the callback-running flag

spec.md section: A3

Add `HasReady` and `ReadyAddedCallbackRunning`. Change
`rescheduleReadyAddedIfRecall` to hand off to the recall without clearing
`readyAddedCbRunning`, calling the unexported test seam `recallHandOffHook`
with `readyAddedCbMutex` not held before starting the recall's
`runReadyAddedCb` goroutine. Make `SetReadyAddedCallback` write
`readyAddedCb` under `readyAddedCbMutex`, and `readyAdded` and
`runReadyAddedCb` read it under the same lock. Covering all 6 acceptance
tests from A3, in `queue/ready_hook_test.go` (test 6 under
`go test -race ./queue`).

- [x] implemented
- [x] reviewed
