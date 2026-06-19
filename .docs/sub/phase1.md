# Phase 1: Server subscription registry + long-poll handlers

Ref: [spec.md](spec.md) sections A1, A2, F1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: A1 - Long-poll over the existing mangos Port

spec.md section: A1

Add the `subscribe`, `unsubscribe`, and parked `waitForUpdates` request
`Method`s to the `handleRequest` dispatch (`serverCLI.go:67` switch),
reusing the existing client-auth check (`serverCLI.go:58-61`) and the
`reserve` parked-reply pattern (`serverCLI.go:191`/`:913`). Add the
server-side subscription registry (key-set or RepGroup scope per
subscription id) plus a bounded, per-subscriber event queue, fed directly
from `SetChangedCallback` (`server.go:1623`, the single source), leaving the
browser `jobSubscriptions`/`statusCaster` path untouched. Open no new
listening socket/port: subscriptions are an extra client dial to the
pre-existing `Port`. Files: `jobqueue/serverCLI.go`, `jobqueue/server.go`,
plus minimal client-side `subscription.go` scaffolding to exercise the
dedicated long-poll socket. Covering all 4 acceptance tests from A1.

- [x] implemented
- [x] reviewed

### Item 1.2: A2 - Unauthorized client cannot subscribe

spec.md section: A2

Ensure `subscribe`/`waitForUpdates` requests reuse the same token auth as
every other client call: token on `clientRequest.Token`, checked in
`handleRequest` (`serverCLI.go:58-61`) -> `ErrPermissionDenied` before any
subscription is registered or events buffered. Covering all 2 acceptance
tests from A2.

- [x] implemented
- [x] reviewed

### Item 1.3: F1 - Single source of state-change events

spec.md section: F1

Route both the existing browser `statusCaster`/websocket emission and the
new per-subscriber queue emission from the one `SetChangedCallback`
(`server.go:1623`, emission at `:1694-1777`), computing per-job `JStatus`
once with no second divergent traversal. Confirm the browser
`JStatus IsPushUpdate == true` path still works. Covering all 2 acceptance
tests from F1. Depends on item 1.1's registry/queue.

- [x] implemented
- [x] reviewed
