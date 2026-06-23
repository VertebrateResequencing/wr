# Phase 2: REST endpoint

Ref: [spec.md](spec.md) sections A1, A3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 2.1: A1 - Modify one editable job by key

spec.md section: A1

Wire `PATCH /rest/v1/jobs/<job-key>` in `jobqueue/serverREST.go`, reusing the
existing token and bearer auth paths, `JobModifier` application, modified-job
refetch, `JobModifyResponse`, and new-key to old-key mapping. Add REST
integration coverage in `jobqueue/rest_test.go` for single-job edits, auth,
stored status, old-key disappearance, and response shape. Cover all 5
acceptance tests from A1. Depends on phase 1's status fields and validation
helpers.

- [ ] implemented
- [ ] reviewed

### Item 2.2: A3 - Modify multiple jobs without changing identity fields

spec.md section: A3

Extend the `PATCH` path for RepGroup targets so multiple editable jobs can be
modified together while non-editable matches stay unchanged. Reject identity
changes such as `cmd` when more than one job would be modified, preserve
existing GET path semantics for ids, and return stable fresh `JStatus` rows.
Add REST integration coverage in `jobqueue/rest_test.go`. Cover all 2
acceptance tests from A3. Depends on item 2.1.

- [ ] implemented
- [ ] reviewed
