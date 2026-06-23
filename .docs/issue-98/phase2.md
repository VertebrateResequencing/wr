# Phase 2: Apply live snapshots in `jtouch` behind the secure gate

Ref: [spec.md](spec.md) sections B1, D1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer`
skills.

## Items

### Item 2.1: B1 - Apply live snapshots on jtouch

spec.md section: B1

Implement `jtouch` live snapshot handling in `jobqueue/serverCLI.go` so the
manager detects present live data, enforces the authenticated HTTPS secure
gate, applies live fields to running jobs, and avoids setting terminal state.
Add `jobqueue/jobqueue_test.go` coverage for all 5 acceptance tests from B1.
Depends on phase 1.

- [ ] implemented
- [ ] reviewed

### Item 2.2: D1 - Preserve existing status behaviour without live data

spec.md section: D1

Guard the B1 changes so absent live data, older runner touches, completed job
archives, and `KillCalled` running jobs preserve existing status and touch
semantics. Add `jobqueue/jobqueue_test.go` and `jobqueue/serverWebI_test.go`
coverage for all 3 acceptance tests from D1. Depends on item 2.1.

- [ ] implemented
- [ ] reviewed
