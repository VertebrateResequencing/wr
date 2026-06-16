# Phase 3: Catch-up / late subscribe

Ref: [spec.md](spec.md) sections C1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: C1 - Already-terminal jobs delivered immediately

spec.md section: C1

At subscribe time, compute catch-up = live in-memory jobs matching the
scope plus `retrieveCompleteJobsByKeys` / `retrieveCompleteJobsByRepGroup`
(`db.go:818`/`:878`) on the boltdb complete bucket for exactly the
subscribed keys/RepGroup; most-recent terminal record wins for reused keys;
only `complete`/`buried` records produce an immediate event (no in-progress
snapshot). Return the catch-up batch synchronously in the `subscribe`
reply (`serverResponse`) before any long-poll event, and emit those events
on `Updates()` before draining the long-poll loop. Wrap DB read failures in
`ErrDBError` and leave no partial subscription registered. Files:
`jobqueue/serverCLI.go`, `jobqueue/subscription.go`. Covering all 4
acceptance tests from C1. Builds on Phases 1-2.

- [ ] implemented
- [ ] reviewed
