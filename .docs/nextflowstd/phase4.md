# Phase 4: Status --output Flag

Ref: [spec.md](spec.md) sections C1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: C1 - Add --output flag to nextflow status

spec.md section: C1

Add `--output`/`-o` bool flag to `newNextflowStatusCommand()` in
`cmd/nextflow.go`. Add `Output bool` to `nextflowStatusOptions`.
In `runNextflowStatus()`, return error if `--output` is set
without `--run-id`. After printing the count table, filter jobs to
complete/buried states and call `printJobsOutput()` with all
queried jobs for multi-instance detection. Depends on phases 1
and 2. Can run in parallel with phase 3. Covering all 7
acceptance tests from C1.

- [ ] implemented
- [ ] reviewed
