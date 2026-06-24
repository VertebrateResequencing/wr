# Phase 3: Status diagnostics

Ref: [spec.md](spec.md) sections C1, C2, C3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: C3 - Filter jobs waiting on never-seen dep-groups

spec.md section: C3

Add the server and client filter path for current jobs whose
`WaitingForDepGroups` is non-empty, including
`Client.GetIncompleteWaitingForDepGroups`, `clientRequest.WaitingForDepGroups`,
request handling in `jobqueue/serverCLI.go`, server filtering in
`jobqueue/server.go`, and `cmd/status.go` flag wiring and validation for
`--missing_deps`. Cover the filtering and validation behavior from C3; Item
3.3 covers the shared `waiting-deps` plain-output assertion.

- [x] implemented
- [ ] reviewed

### Batch 1 (parallel, after Item 3.1 is reviewed)

#### Item 3.2: C1 - Expose never-seen waits in status data [parallel with C2]

spec.md section: C1

Expose `WaitingForDepGroups` through `Job.ToStatus`, `JStatus`, REST status
JSON, REST `waiting_deps=true`, and web status details in `jobqueue/job.go`,
`jobqueue/serverREST.go`, `jobqueue/serverWebI.go`, and
`jobqueue/static/status.html` plus `jobqueue/static/js/wr/*.js`. Preserve
exported Go field JSON names. Cover all 4 acceptance tests from C1.

- [x] implemented
- [ ] reviewed

#### Item 3.3: C2 - Display never-seen waits in CLI status [parallel with C1]

spec.md section: C2

Update `cmd/status.go` and `cmd/status_table.go` so details output explains
never-seen dep-group waits, table and plain output show `waiting-deps`, JSON
retains `State:"dependent"` and exported field names, and counts still report
these jobs as dependent. Cover all 5 acceptance tests from C2.

- [x] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
