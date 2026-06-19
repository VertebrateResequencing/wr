# Phase 3: Wait for running

Ref: [spec.md](spec.md) sections C1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: C1 - Return when a job starts or has already ended

spec.md section: C1

Add `(*Scheduler).WaitForRunning(ctx, key, pollInterval)` in
`client/client.go` using `GetJobByKey(key, false, false)` polling. Return on
`running`, `lost`, `complete`, `buried`, or `unknown`; keep polling through
`reserved`; do not retry blank or missing key errors; use a 5 second interval
when `pollInterval <= 0`; and return `ctx.Err()` on cancellation or deadline.
Covering all 10 acceptance tests from C1. Builds on Phase 2.

- [ ] implemented
- [ ] reviewed
