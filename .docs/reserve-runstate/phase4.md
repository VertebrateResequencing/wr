# Phase 4: Logging

Ref: [spec.md](spec.md) sections D1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: D1 - Each non-durable hand-out is logged

spec.md section: D1

Add `db.reservesNotDurable`, `db.rndMu`, the `reserveNotDurableLogMsg`
constant and `(*db).logReserveNotDurable(key string)`, which bumps the
counter and logs the info line on `context.Background()` under `rndMu`.
Call it from `persistReservation` (`jobqueue/serverCLI.go`) on any non-nil
error, including `errDBClosed`, before the job is handed out, keeping the
existing rate-limited warning and error line. Covering all 4 acceptance
tests from D1, in `jobqueue/reserve_durability_test.go` (test 1 with real
`clog.CreateFileHandlersAtLevels` handlers and a deferred
`clog.ToDefault()`). Depends on phase 3's `persistReservation` switch.

- [x] implemented
- [x] reviewed
