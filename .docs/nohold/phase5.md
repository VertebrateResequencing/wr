# Phase 5: remove the hold

Ref: [spec.md](spec.md) sections E1, E2, E3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

Needs phase 4: without ready-time preparation, removing the hold would let
every reserve take unprepared items, and without E2 a fresh runner's first
reserve would be skipped. The items run in sequence, all in
`jobqueue/serverCLI.go`, `jobqueue/server.go` and their tests. Removing
the hold in item 5.1 breaks two existing tests that E3 reworks,
`TestResumeRacingReserveKeepsRACPending` and
`TestRerunReplacementReadyCallbackBlocksReserve`. The reviews of items 5.1
and 5.2 accept those two failing and nothing else: every other test in
`make test` must pass, so the `jobqueue` test package must compile after
item 5.1. The phase is done only when item 5.3 is reviewed with
`make test` green.

Red tests (E1.1 to E1.3, E1.5, E1.6, E2.1) must be shown failing on the
base before the fix, using only what the base has, as spec.md's Key
Decisions "Testing" describes (seed with `putJobStat`, drop
`ReqStatRefreshInterval`); the base has no `racIdle()`, so its red runs
wait for `!racPending && !racRunning` under `rpmutex`, as the probe does.

## Items

### Item 5.1: E1 - Reserves never wait for the RAC

spec.md section: E1

Remove everything spec.md "Removed" lists (`racPending`,
`waitingReserves`, `setRACPending`, `clearRACPending`, `clearRACWaiters`,
`waitForPendingReserves`, and the `readyCallbackExpected` logic and its
helpers), make `triggerReadyAddedCallback` only call
`q.TriggerReadyAddedCallback`, keep `racRunning` under `rpmutex`, and drop
the `racPending` part of `racIdle()`. In the same change, give
`skipReserve` E2's condition (item 5.2 describes it): without the hold, a
fresh client's first `ReserveScheduled` with a runner command set can run
before the RAC counts its group, so the old count-only check would skip it
and break existing tests such as those that set `serverRC` in
`jobqueue_test.go`. Add `TestNoHoldReadyPrepared`, replacing the probe.
Covering all 7 acceptance tests from E1, in `jobqueue/nohold_test.go`;
test 7 is the grep that must print nothing.

Test 7's grep covers comments and test files too, and Go compiles the
whole test package, so this item also does the parts of E3 that name the
removed identifiers: it reduces `finishRAC` to clearing `racRunning`;
rewrites or deletes the `racPending` block comment, the `rpmutex` field
comment, `resumeQueueItem`'s doc and shutdown's "free any waiting
reserves"; makes `slowRequestStallServer` hold the queue lock through
`SuspendWith` (E3 test 3); and makes
`TestResumeRacingReserveKeepsRACPending` poll until `server.racIdle()`
instead of reading `racPending`, with its comments no longer naming
`waitForPendingReserves`. Any other comment that names a removed
identifier, including in tests phases 3 and 4 added, is reworded too.

- [ ] implemented
- [ ] reviewed

### Item 5.2: E2 - skipReserve needs a zero count and nothing ready

spec.md section: E2

Item 5.1 changed `skipReserve`; confirm it skips only a first reserve
with a scheduler group and a runner command whose
`previouslyScheduledGroups` count is 0 (or absent) and for which
`q.HasReady(cr.SchedulerGroup)` is false, reading the count under
`psgmutex` and releasing it before calling `q.HasReady`. Add tests
covering all 5 acceptance tests from E2, in `jobqueue/nohold_test.go`;
E2.1's red run is on the base. Depends on item 5.1.

- [ ] implemented
- [ ] reviewed

### Item 5.3: E3 - Tests and comments that encode the hold

spec.md section: E3

Rewrite or delete the comments E3 lists that item 5.1 left (`kickJobs`'
"prepared before RAC-pending" comment, `prepareReadyJob`'s and
`ensureReserveGroup`'s notes about add-time groups, any left in
`recoveredItemDef`, and `resumeItemReadyHook`'s), and check the ones item
5.1 handled. Rename and rework `TestResumeRacingReserveKeepsRACPending`
and `TestRerunReplacementReadyCallbackBlocksReserve`, switch the listed
tests to `server.racIdle()`, and clean the hold out of
`resume_order_test.go`. `finishRAC` and `slowRequestStallServer` were
changed in item 5.1; confirm the four slow-request leaves pass. Covering
all 5 acceptance tests from E3. Depends on items 5.1 and 5.2.

- [ ] implemented
- [ ] reviewed
