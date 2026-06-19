# Phase 2: Wait and typed status APIs

Ref: [spec.md](spec.md) sections B1, B2, B3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 2.1: B3 - Fetch typed status by key

spec.md section: B3

Add `(*Scheduler).GetJobByKey` in `client/client.go`, wrapping
`GetByEssence(&jobqueue.JobEssence{JobKey: key}, getStd, getEnv)`. Blank keys
return `jobqueue.Error{Op: "GetJobByKey", Err: jobqueue.ErrBadRequest}`.
Missing keys return `jobqueue.Error{Op: "GetJobByKey", Item: key, Err:
jobqueue.ErrBadJob}`. Tests live in `client/client_test.go`. Covering all 4
acceptance tests from B3. Builds on Phase 1.

- [ ] implemented
- [ ] reviewed

### Item 2.2: B2 - Wait later by key

spec.md section: B2

Add `(*Scheduler).WaitForJobs(ctx, keys...)` in `client/client.go`.
De-duplicate keys in input order, fetch current status with stdout/stderr via
item 2.1, return already `complete` or `buried` jobs immediately, and wait
only for non-terminal keys using `SubscribeToJobKeys` or a small helper in
`jobqueue/subscription.go` if needed. On context cancellation or deadline,
return terminal jobs gathered so far in key order with an error wrapping
`ctx.Err()` and naming unfinished keys. Covering all 6 acceptance tests from
B2. Depends on item 2.1.

- [ ] implemented
- [ ] reviewed

### Item 2.3: B1 - Submit and wait for terminal jobs

spec.md section: B1

Add `(*Scheduler).SubmitJobsAndWait(ctx, jobs, opts)` in `client/client.go`.
Use `jobqueue.Client.AddAndWait` with the env/completed policy from Phase 1,
de-duplicate returned keys in key order, return complete and buried jobs as
successful results, and surface context cancellation with unfinished keys while
preserving gathered terminal jobs. Covering all 5 acceptance tests from B1.
Depends on Phase 1 and should share terminal-key collection logic with item
2.2 where that keeps code simpler.

- [ ] implemented
- [ ] reviewed
