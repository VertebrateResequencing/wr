# Phase 2: Add warning plumbing

Ref: [spec.md](spec.md) sections B1, B2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 2.1: B1 - Return warnings from add APIs

spec.md section: B1

Add `AddWarnings`, add-with-warnings client methods in `jobqueue/client.go`,
server response support in `jobqueue/serverCLI.go` and related request
plumbing, and `wr add` warning output in `cmd/add.go`. Preserve existing
public add method signatures and print `wr add --sync` warnings immediately
after IDs are returned. Cover all 5 acceptance tests from B1.

- [ ] implemented
- [ ] reviewed

### Item 2.2: B2 - Suppress warnings for seen or same-batch groups

spec.md section: B2

Ensure warning generation in `jobqueue/server.go` and `cmd/add.go` only reports
real never-seen waits, with no warnings for completed seen groups or same-batch
carriers. De-duplicate and sort returned warning group names. Cover all 2
acceptance tests from B2.

- [ ] implemented
- [ ] reviewed
