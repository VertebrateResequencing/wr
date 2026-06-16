# Phase 8: cmd/add.go rewrite + browser-regression check

Ref: [spec.md](spec.md) sections E2, F1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 8.1: E2 - cmd/add.go --sync uses AddAndWait

spec.md section: E2

Replace the `waitForJobCompletion`/`getJob` poll loop (`cmd/add.go:942`)
with `Client.AddAndWait` on the single added job in `synchronousAdd`
(`cmd/add.go:924`). Print head/tail stdout then stderr, then
`os.Exit(job.Exitcode)` exactly as today. Remove `waitForJobCompletion`.
Files: `cmd/add.go`. Covering all 3 acceptance tests from E2. Depends on
Phase 7's `AddAndWait`.

- [ ] implemented
- [ ] reviewed

### Item 8.2: F1 - Browser-regression check (no regression)

spec.md section: F1

Verify end-to-end that the browser `/status_ws` stream and the new push
subscriptions are both driven by the one `SetChangedCallback` with no
regression: a browser-style websocket and a Go-client push subscription on
the same jobs both receive their updates, and the websocket still gets a
`JStatus` with `IsPushUpdate == true`. Files: `jobqueue/server.go` (verify
only; no second divergent path). Covering both acceptance tests from F1 as a
final regression gate. Depends on item 8.1 and Phase 1's item 1.3.

- [ ] implemented
- [ ] reviewed
