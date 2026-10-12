# Phase 4: ready hook

Ref: [spec.md](spec.md) sections D1, D2, D3, D4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

Needs phases 1 to 3. The items run in sequence: D1 installs the hook that
D2, D3 and D4 test, and all four add tests to `jobqueue/nohold_test.go`
and change `jobqueue/server.go`. The hold is still present, so Section D
tests check the queue item (`server.q.Get(key)`), not reserves.

Red tests (D1.10, D2.1) must be shown failing on the base before the fix,
as spec.md's Key Decisions "Testing" describes.

## Items

### Item 4.1: D1 - Every item is prepared as it becomes ready

spec.md section: D1

In `jobqueue/ready_prepare.go`, add `(*Server).prepareForReady` (spec.md
"jobqueue ready hook") and set it with `q.SetReadyHook` in `createQueue`
before any item is added. Replace the `rc` field with
`rcValue atomic.Pointer[string]`, stored by `setRC` and `Serve` and read by
`runnerCommand()`, `readyAddedCallback`, `createJobs` and the hook, and
switch the `Server{rc: ...}` test literals to `setRC`. Call
`warmReqGroupRecs` in `enqueueItems`, `queueNewJobItems` (before
`replaceLiveRerunItems`), `kickJobs` and `resumeJobs`, and nowhere else.
Covering all 11 acceptance tests from D1, in `jobqueue/nohold_test.go`;
test 11's sweep of tests assuming add-time state is listed in the phase's
checklist. Re-run C3 test 10, which discriminates from this item on.

- [ ] implemented
- [ ] reviewed

### Item 4.2: D2 - Recovered ready items are prepared

spec.md section: D2

Make `recoveredItemDef` (`jobqueue/server.go`) stop special-casing ready
jobs and delete its comment about keeping group ""; recovered running jobs
still get their group there. Covering all 3 acceptance tests from D2, in
`jobqueue/nohold_test.go` (test 3 is the existing recovery tests). Depends
on item 4.1.

- [ ] implemented
- [ ] reviewed

### Item 4.3: D3 - The RAC remains a floor

spec.md section: D3

Confirm the RAC still re-prepares long-waiting ready jobs from newer
learning, and that the reliable4 bounded scan still leaves the
limit-blocked remainder unprepared. Covering both acceptance tests from
D3, in `jobqueue/nohold_test.go` (test 2 is the existing
`TestReliable4Rac*` tests). Depends on item 4.1.

- [ ] implemented
- [ ] reviewed

### Item 4.4: D4 - No bolt I/O under the queue lock

spec.md section: D4

Prove the hook does no bolt I/O, takes no `racmutex` and reads the runner
command from `rcValue`. Add `BenchmarkAddManyUnderReserveLoad` to
`jobqueue/kick_resume_bench_test.go`, modelled on
`BenchmarkKickUnderReserveLoad` (adds 10,000 jobs in one `Add` while
reservers run), for G1 test 3. Covering all 5 acceptance tests from D4, in
`jobqueue/nohold_test.go`, with test 4 as `CGO_ENABLED=1 make race` (all
`OS_*` unset). Depends on items 4.1 to 4.3.

- [ ] implemented
- [ ] reviewed
