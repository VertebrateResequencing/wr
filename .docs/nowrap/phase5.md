# Phase 5: Pretend mode support

Ref: [spec.md](spec.md) sections E1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 5.1: E1 - New methods work with PretendSubmissions

spec.md section: E1

Extend `pretendJobqueue` in `client/client.go` and any newly required private
`jobqueueClient` interface methods for the new Scheduler APIs.
`SubmitJobsAndReturnIDs` records delayed jobs and returns keys,
`SubmitJobsAndWait` records jobs and marks them complete, `WaitForJobs`
returns recorded jobs and completes incomplete matches, `GetJobByKey` returns
the exact recorded pointer or the typed missing-key error, and
`WaitForRunning` marks delayed/ready/reserved jobs as running before return.
Ensure both submit paths write pretend JSON exactly once when configured.
Covering all 6 acceptance tests from E1. Builds on Phases 1-3 and can run in
parallel with Phases 4 and 6.

- [ ] implemented
- [ ] reviewed
