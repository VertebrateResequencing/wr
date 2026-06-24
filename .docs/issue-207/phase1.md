# Phase 1: Queue State

Ref: [spec.md](spec.md) sections A1, A2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: A1 - Suspend And Resume Queue Items

spec.md section: A1

Implement `ItemStateSuspended`, `SubQueueSuspended`, suspended queue storage,
`Queue.Suspend`, `Queue.Resume`, stats, callbacks, delay handling, and
`queue/item.go` and `queue/queue.go` tests, covering all 9 acceptance tests
from A1.

- [x] implemented
- [x] reviewed

### Item 1.2: A2 - Preserve Dependency Accounting

spec.md section: A2

Preserve dependency links, unresolved dependency tracking, and ready callback
accounting for suspended parent and child items in `queue/queue.go`, covering
all 4 acceptance tests from A2. Depends on item 1.1.

- [x] implemented
- [x] reviewed
