# Phase 3: Add `JobUpdateLive` delivery

Ref: [spec.md](spec.md) sections B2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer`
skills.

## Items

### Item 3.1: B2 - Push live updates to job subscribers

spec.md section: B2

Append `JobUpdateLive` to `JobUpdateKind`, add the B2 live fields and
`SSHCommand` to `JobUpdate` in `jobqueue/subscription.go`, and deliver live
updates from `jobqueue/server_subscription.go` and `jtouch` to key and status
websocket detail subscriptions. Preserve `AddAndWait` terminal waiting and
RepGroup semantics. Add `jobqueue/subscription_test.go` coverage for all 6
acceptance tests from B2. Depends on phase 2.

- [x] implemented
- [x] reviewed
