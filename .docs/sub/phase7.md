# Phase 7: AddAndWait

Ref: [spec.md](spec.md) sections E1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 7.1: E1 - AddAndWait blocks until all added jobs terminal

spec.md section: E1

Implement `Client.AddAndWait(ctx, jobs, envVars, ignoreComplete)` built on
`SubscribeToJobKeys` using the keys from the add. Unblock once the count of
distinct terminal keys equals the number of added job keys (dedup by key;
`JobUpdateLost` does not count). Return the full terminal `[]*Job`
(re-fetched via existing `Get*` so `Exitcode` and stdout/stderr are inline);
a mix of complete and buried is not a Go error. On ctx fire first, return the
jobs gathered so far plus a `context.DeadlineExceeded`-derived error naming
the unfinished key(s). Catch-up ensures a job finishing between add and
subscribe is counted. Files: `jobqueue/subscription.go`. Covering all 6
acceptance tests from E1. Builds on Phases 1-6.

- [x] implemented
- [x] reviewed
