# Phase 4: JSON construction and examples

Ref: [spec.md](spec.md) sections D1, D2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: D1 - Convert JobViaJSON with Scheduler defaults

spec.md section: D1

Add `(*Scheduler).JobDefaults` and `(*Scheduler).NewJobFromJSON` in
`client/client.go`. Map scheduler cwd, queue, queues-avoid, default RAM,
time, cores, disk, retries, and override into `jobqueue.JobDefaults`; return
the typed bad-request error for nil specs; otherwise call
`spec.Convert(s.JobDefaults())`. Tests live in `client/client_test.go`.
Covering all 4 acceptance tests from D1. Builds on Phases 1-3 and can run in
parallel with Phases 5 and 6.

- [x] implemented
- [x] reviewed

### Item 4.2: D2 - Document the no-shell path

spec.md section: D2

Add package examples in `client/example_test.go`:
`ExampleScheduler_SubmitJobsAndReturnIDs`,
`ExampleScheduler_SubmitJobsAndWait`,
`ExampleScheduler_SubmitJobsAndReturnIDs_rerunCompleted`, and
`ExampleScheduler_NewJobFromJSON`. Show scheduler/job creation, storing the
returned key, passing a `context.Context`, reading terminal job fields, using
`SubmitJobsOptions{RerunCompleted: true}`, and converting a
`jobqueue.JobViaJSON` with mount configs before submitting it. Covering all 5
acceptance tests from D2. Depends on item 4.1 and the public methods from
Phases 1-3.

- [x] implemented
- [x] reviewed
