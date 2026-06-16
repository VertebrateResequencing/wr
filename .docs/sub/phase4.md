# Phase 4: RepGroup subscription + aggregate

Ref: [spec.md](spec.md) sections B2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: B2 - RepGroup aggregate fires once when all known jobs terminal

spec.md section: B2

Implement `Client.SubscribeToRepGroup` and the server-side RepGroup
aggregate tracking on the subscription registry: track the set of keys seen
and their latest terminal/lost state, and deliver a single
`JobUpdateRepGroupDone` (carrying `Complete`/`Buried`/`Lost`/`Total` counts
plus parallel `JobKeys`/`JobStates`) once every currently-known job in the
group is `complete`/`buried`. A `lost` job holds the event back until it
settles (or ctx fires); an empty group never fires; deliver no per-job
`JobUpdateTerminal` events for RepGroup subscriptions. Files:
`jobqueue/subscription.go`, `jobqueue/server.go`. Covering all 5 acceptance
tests from B2. Builds on Phases 1-3.

- [ ] implemented
- [ ] reviewed
