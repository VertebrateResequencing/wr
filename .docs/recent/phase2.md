# Phase 2: Server + client path (jobqueue)

Ref: [spec.md](spec.md) sections B1, B2

## Dependencies

Depends on Phase 1 (uses `retrieveCompleteJobsRecent` from the end-time
index). Can run in parallel with Phase 3 (duration parsing), which is
independent of Phases 1-2.

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

The items in this phase are sequential: B2 (window movement) exercises
the same `GetRecent` round-trip built in B1. Implement and review B1
before B2.

Tests use the existing server/client harness (`Serve`, `Connect`) and
complete jobs the real way: `Add` -> `Reserve` -> `Started` ->
`Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: ...})`.

## Items

### Item 2.1: B1 - GetRecent client/server round-trip

spec.md section: B1

Add `Period time.Duration` to `clientRequest` (reusing `Limit`, `GetStd`,
`GetEnv` and `serverResponse.Jobs`). Add request method constant
`requestMethodGetRecent = "getrec"` and a `dispatchMethod` case. Implement
`handleGetRecent` (returns `ErrBadRequest` when `cr.Period <= 0`, else
`jobsResponse(jobs)`), `getJobsRecent` (cutoff = now - period;
`retrieveCompleteJobsRecent` then `limitJobs` with Limit/GetStd/GetEnv, no
State), and `Client.GetRecent(period, limit, state, getStd, getEnv)`
(sends Method/Period/Limit/GetStd/GetEnv only). Files: `jobqueue/client.go`,
`jobqueue/server.go`, `jobqueue/serverCLI.go`. Test file:
`jobqueue/jobqueue_test.go`. Covering all 7 acceptance tests from B1.
Depends on Phase 1.

- [x] implemented
- [x] reviewed

### Item 2.2: B2 - GetRecent reflects window movement after time passes

spec.md section: B2

Add a test confirming a job archived with `EndTime` now-90s is returned by
`GetRecent(2*time.Minute, ...)` but not by `GetRecent(1*time.Minute, ...)`
(same data, narrower window excludes it). Test file:
`jobqueue/jobqueue_test.go`. Covering the 1 acceptance test from B2.
Depends on Item 2.1.

- [x] implemented
- [x] reviewed
