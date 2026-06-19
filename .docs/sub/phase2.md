# Phase 2: Client Subscription + per-key terminal/lost

Ref: [spec.md](spec.md) sections B1, D1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 2.1: B1 - Per-key terminal events

spec.md section: B1

Implement the client-side `Subscription`, `JobUpdate`/`JobUpdateKind`
types, `Client.SubscribeToJobKeys`, and the dedicated second `req`/`rep`
long-poll socket and loop in `jobqueue/subscription.go`. Deliver one
`JobUpdateTerminal` per subscribed key on `complete`/`buried`, a
`JobUpdateLost` (non-counting) on `lost`, and nothing for
`running`/`ready`/etc. Wire the per-job state discrimination from the
server-side queue (`jobqueue/server.go`). Covering all 4 acceptance tests
from B1. Builds on the Phase 1 registry/handlers.

- [x] implemented
- [x] reviewed

### Item 2.2: D1 - Context-bound and explicit Unsubscribe teardown

spec.md section: D1

Implement ctx binding (close `Updates()` and set `Err()` to ctx error on
cancel/deadline), `Subscription.Unsubscribe` (sends `unsubscribe`, closes
the dedicated socket, drops server registration, idempotent, closes
`Updates()` with nil `Err()`), and `Subscription.Err()`/`Updates()`
semantics. Covering all 4 acceptance tests from D1. Depends on item 2.1.

- [x] implemented
- [x] reviewed
