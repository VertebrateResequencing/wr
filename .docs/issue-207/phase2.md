# Phase 2: Jobqueue Behaviour

Ref: [spec.md](spec.md) sections B1, B2, B3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 2.1: B1 - Persist And Recover Suspended Jobs

spec.md section: B1

Add `JobStateSuspended` mappings, queue recovery for suspended jobs, persisted
state updates, status grouping, and `jobqueue/job.go`, `jobqueue/server.go`,
`jobqueue/db.go`, and `jobqueue/serverCLI.go` tests, covering all 5
acceptance tests from B1. Depends on phase 1.

- [ ] implemented
- [ ] reviewed

### Item 2.2: B2 - Client And Server APIs

spec.md section: B2

Implement `Client.Suspend`, `Client.Resume`, `jsuspend`, `jresume`, key
conversion, server request handling, and related `jobqueue/client.go`,
`jobqueue/serverCLI.go`, and `jobqueue/server.go` tests, covering all 8
acceptance tests from B2. Depends on item 2.1.

- [ ] implemented
- [ ] reviewed

### Item 2.3: B3 - Preserve Limit-Group Scheduling Accounting

spec.md section: B3

Keep suspended jobs in limit-group metadata while excluding them from runner
scheduling counts, and verify resumed limit checks plus delayed suspended
scheduler behaviour in `jobqueue/server.go` and `jobqueue/serverCLI.go`,
covering all 3 acceptance tests from B3. Depends on item 2.2.

- [ ] implemented
- [ ] reviewed
