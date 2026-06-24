# Phase 4: Add live status JSON, REST, websocket, and UI updates

Ref: [spec.md](spec.md) sections C1, C2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer`
skills.

## Items

### Item 4.1: C1 - Include live fields and SSH command in status

spec.md section: C1

Add `SSHCommand` to `JStatus` in `jobqueue/job.go`, populate live status
fields from running jobs, implement the running-job SSH command construction
and quoting rules, and ensure REST/status JSON exposes only authenticated job
details. Add `jobqueue/job_test.go`, `jobqueue/serverWebI_test.go`, and
`jobqueue/rest_test.go` coverage for all 7 acceptance tests from C1. Depends
on phase 2.

- [x] implemented
- [ ] reviewed

### Item 4.2: C2 - Render live introspection in running job details

spec.md section: C2

Update `jobqueue/static/status.html`,
`jobqueue/static/js/wr/websocket-handler.js`,
`jobqueue/static/js/wr/utility.js`, and
`jobqueue/static/css/wr-0.36.0.css` to render live RAM, CPU time, stdout,
stderr, and a copyable SSH command for running job details without adding an
embedded terminal. Add `jobqueue/serverWebI_test.go` coverage for all 6
acceptance tests from C2. Depends on item 4.1 and phase 3.

- [x] implemented
- [ ] reviewed
