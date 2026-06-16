# Phase 6: Reconnect/resync

Ref: [spec.md](spec.md) sections D4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 6.1: D4 - Reconnect keeps channel open with resync marker

spec.md section: D4

On disconnect (long-poll Send/Recv on the dedicated socket errors, or the
control call fails) with a recoverable manager, transparently re-dial both
the primary and dedicated long-poll connections to the same `Port`
(re-reading `c.ServerInfo.Addr`), re-issue `subscribe` (re-running catch-up,
fresh sub id), resume the long-poll loop, and emit a `JobUpdateResync` event
on the same `Updates()` channel with `Err()` staying nil. Only an
unrecoverable disconnect (retry budget exhausted) closes `Updates()` with
`Err()` returning `ErrSubscriptionClosed`. Define the `ErrSubscriptionClosed`
sentinel. Files: `jobqueue/subscription.go`. Covering all 3 acceptance
tests from D4. Builds on Phases 1-5.

- [ ] implemented
- [ ] reviewed
