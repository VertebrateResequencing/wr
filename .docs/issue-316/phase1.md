# Phase 1: Persistence and dependency resolution

Ref: [spec.md](spec.md) sections A2, A1, A3, A4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: A2 - Seen completed groups do not block

spec.md section: A2

Add persistent dep-group seen storage in `jobqueue/db.go` and use it from
`jobqueue/dependency.go`, including `bucketDepGroups`, rebuild from
`bucketDTK` for old databases, and helper methods for seen checks. Store
non-empty `Job.DepGroups` values in the new bucket during new-job persistence.
Cover all 3 acceptance tests from A2.

- [x] implemented
- [ ] reviewed

### Item 1.2: A1 - Wait for never-seen dep-groups

spec.md section: A1

Add `Job.WaitingForDepGroups` in `jobqueue/job.go`, then update
`jobqueue/dependency.go` and `jobqueue/server.go` so dep-group dependencies
with no live carriers and no seen record use synthetic dependency keys,
populate the field, and remain unreservable until a carrier appears and
completes. Cover all 3 acceptance tests from A1.

- [x] implemented
- [ ] reviewed

### Item 1.3: A3 - Same-batch and live reblocking stay unchanged

spec.md section: A3

Preserve existing same-batch and live dep-group reblocking behavior in
`jobqueue/db.go` and `jobqueue/server.go`, including reverse dep-group
reevaluation when new carriers appear. Cover all 3 acceptance tests from A3.

- [x] implemented
- [ ] reviewed

### Item 1.4: A4 - Command dependencies stay unchanged

spec.md section: A4

Keep command and essence dependency semantics unchanged in
`jobqueue/dependency.go` and `cmd/add.go`: absent command targets still resolve
to no dependency, do not set `WaitingForDepGroups`, and do not warn. Cover all
3 acceptance tests from A4.

- [x] implemented
- [ ] reviewed
