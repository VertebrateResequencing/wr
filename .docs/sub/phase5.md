# Phase 5: Buffering/isolation + dedup

Ref: [spec.md](spec.md) sections D2, D3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 5.1: D2 - Bounded, isolated, block-never-drop buffer

spec.md section: D2

Give each subscription its own bounded server-side event queue; when full,
the `SetChangedCallback` enqueue blocks (back-pressure on that one
subscriber) rather than dropping a terminal event. Guarantee per-subscriber
isolation: a stuck subscriber must not stall the manager's
`SetChangedCallback` path or other subscribers' queues. Files:
`jobqueue/server.go`. Covering all 3 acceptance tests from D2. Builds on
Phases 1-4.

- [x] implemented
- [x] reviewed

### Item 5.2: D3 - At-least-once delivery; dedup by key

spec.md section: D3

Accept that a job finishing during the subscribe/catch-up handshake may
appear in both catch-up and live streams (at-least-once); ensure any
duplicate carries identical `Key`/`State` (duplicate, not contradiction).
Lay the client-side dedup-by-key groundwork that AddAndWait relies on
(counting distinct terminal keys). Files: `jobqueue/subscription.go`.
Covering all 2 acceptance tests from D3. Depends on item 5.1.

- [x] implemented
- [x] reviewed
