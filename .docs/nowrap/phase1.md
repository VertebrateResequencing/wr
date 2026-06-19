# Phase 1: Submit job keys and duplicate policy

Ref: [spec.md](spec.md) sections A1, A2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: A1 - Return job keys from Scheduler

spec.md section: A1

Add `ErrDuplicateJobs` as an addressable package-level `var`,
`SubmitJobsOptions`, helper plumbing for env/completed policy, and
`(*Scheduler).SubmitJobsAndReturnIDs` in `client/client.go`. Extend the
private `jobqueueClient` interface as needed to call `AddAndReturnIDs`.
Queued duplicates should return successful keys from the new method, while
`SubmitJobs` duplicate failures should expose the exported sentinel and keep the
exact existing error string. Tests live in `client/client_test.go`. Cover all 4
acceptance tests from A1.

- [ ] implemented
- [ ] reviewed

### Item 1.2: A2 - Control completed-job reruns

spec.md section: A2

Implement the `SubmitJobsOptions` policy for `EnvVars` and
`RerunCompleted`: nil env means `os.Environ()`, an empty slice means no env,
the default skips completed matches, and rerun re-adds completed matches.
Exercise completed-job skip/re-add behavior and env persistence through the
client API. Cover all 4 acceptance tests from A2. Depends on item 1.1; env
assertions should fetch jobs with `jobqueue.Client.GetByEssence` in this phase.
Phase 2 adds the public `Scheduler.GetJobByKey` wrapper.

- [ ] implemented
- [ ] reviewed
