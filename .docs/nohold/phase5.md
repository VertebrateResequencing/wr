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

The tests are `TestNoHoldReadyPrepared` (E1 tests 1 to 6), built on the
probe's setup (`newReadyPreparedProbe`: reserved parent, suspended child,
learned 300 for `"racprobe"` seeded with `seedReqGroupStats`,
`ReqStatRefreshInterval: -1`, three RAC gates installed after polling
`racIdle()`). `reserveReturnsWhileGated` now returns the reserved job, not
its key, so the leaves can check its RAM, and no longer returns a done
channel. Test 4 runs with the RAC gated too, so the reserve after `Resume`
does not depend on Resume's callback. Tests 5 and 6 bury a job of its own
ReqGroup (`"ramfail"`, nothing learned), so the base's stale size is the
failed run's 10, as the spec says; in the `"racprobe"` group the base gave
300. E1 test 7 is the grep, which prints nothing; `clearRACWaiters`,
`readyCallbackExpected`, `itemWillBecomeReadyAfterDependencyUpdate` and
`updateLiveRerunItemWithReadyPending` are gone too. `itemDefTriggersReadyAdded`
stays: `readyItemDefJobs` uses it.

Production: `handleReserve` no longer waits; `finishRAC` only clears
`racRunning`; `racIdle()` is `!q.ReadyAddedCallbackRunning()`; shutdown's
clear of the flags is deleted (the RAC's own deferred `finishRAC` clears
`racRunning`). `resumeQueueItem` passes `resuming` straight to `ResumeWith`.
`kickJobs` no longer does the `q.Get` that decided whether to set the hold,
and its "prepared before RAC-pending" comment is reworded in passing, since
the code it described went. `skipReserve` has E2's rule, reading the count
through the existing `currentGroupCount` (map under `psgmutex`, released
before `q.HasReady`). Left for item 5.3: `resumeItemReadyHook`'s doc still
says "before it decides whether a ready-added callback is still pending".

Red evidence on the base 6f1b41bb (a scratch copy of the final test using
only the base's API: `putJobStat` seed, `!racPending && !racRunning` under
`rpmutex` polled instead of `racIdle()`, no `ReqStatRefreshInterval`):
control: the reserve does not return within 1s; P1: RAM 10, not 300; P2:
RAM 10; test 4: `GetByEssence` RAM 10 while paused; test 5: the reserve does
not return; test 6: RAM 10, not 1200. On this item all six pass.

Expected failures until item 5.3: `TestResumeRacingReserveKeepsRACPending`
(the other reserve returns at once) and
`TestRerunReplacementReadyCallbackBlocksReserve` (the reserve returns before
the callback).

Mutants, each in a scratch copy:

- `handleReserve` waiting until `racIdle()` (a hold): KILLED by all six
  `TestNoHoldReadyPrepared` leaves (no reserve returns within 1s).
- `slowRequestStallServer` waiting out its stall before returning: KILLED
  by the warn leaf of `TestReliable4SlowRequestHandleRequestWiring` (0
  warnings, not 1).
- `skipReserve` keeping the old count-only rule (skip whenever the count is
  0): SURVIVED the whole `jobqueue` package (only the two tests above fail).
  The existing `serverRC` tests reserve after an ungated RAC has already
  counted the group, so the race the phase's Instructions describe does not
  show up in them. E2 test 1 (item 5.2, RAC gated) must kill it.
- `skipReserve` never skipping: SURVIVED the `TestReliable2Reserve*` tests
  and the `serverRC` tests. E2 tests 2 and 3 (item 5.2) must kill it.

2026-10-26: `racRunning`, with `rpmutex` and `finishRAC`, was later removed
as unused. This item kept it as a test idle signal, but `racIdle()` (which
uses `q.ReadyAddedCallbackRunning()`) replaced that use.

- [x] implemented
- [x] reviewed

### Item 5.2: E2 - skipReserve needs a zero count and nothing ready

spec.md section: E2

Item 5.1 changed `skipReserve`; confirm it skips only a first reserve
with a scheduler group and a runner command whose
`previouslyScheduledGroups` count is 0 (or absent) and for which
`q.HasReady(cr.SchedulerGroup)` is false, reading the count under
`psgmutex` and releasing it before calling `q.HasReady`. Add tests
covering all 5 acceptance tests from E2, in `jobqueue/nohold_test.go`;
E2.1's red run is on the base. Depends on item 5.1.

`skipReserve` as item 5.1 left it matches E2: it returns false unless the
reserve is a first one with a scheduler group and a runner command, returns
false when `currentGroupCount` (which reads the map under `psgmutex` and
releases it before `getCount`) is above 0, and otherwise returns
`!q.HasReady(cr.SchedulerGroup)`, called with no `psgmutex` held. No
production change in this item.

The tests are `TestNoHoldSkipReserve` (E2 tests 1 to 5), with the helper
`noHoldTest.firstReserve`, which times a fresh client's first
`ReserveScheduled`. Test 3 polls until `racIdle()` and the group's count is
1, then has the test's runner reserve the job and asserts the count is still
1 and nothing is ready before the fresh reserve. Test 4 calls `skipReserve`
directly with first and later reserves, with and without a group. Test 5
gates the RAC before adding either job, reserves the first on the
limit-suffixed group (the runner's own first reserve, which proceeds since
the job is ready) and waits for the second to be ready.

Red evidence on the base 6f1b41bb (E2.1 alone in a scratch copy, using only
the base's API: `RunnerCmd` set in the config, `!racPending && !racRunning`
under `rpmutex` polled before gating): the fresh client's first reserve
never returns (`receive time out` after 60s). On this item all five pass.

Mutants, each in a scratch copy:

- `skipReserve` with the old count-only rule: KILLED by test 1 (the reserve
  returns nil) and test 5 (the runner's first reserve of the first job is
  skipped).
- `skipReserve` never skipping: KILLED by test 2 (the reserve waits its 5s
  timeout, not under 1s). Test 3 does not kill it, as test 3 asserts that a
  counted group is not skipped.
- `skipReserve` ignoring the count (skip whenever nothing is ready): KILLED
  by test 3 (the reserve returns in 167µs, not after 300ms).
- `prepareForReady` without its `updateJobRequirementsForRetry` call (the
  5.1 review's extra check): KILLED by `TestNoHoldReadyPrepare` (resume after
  learning, dependency release, delay expiry, live-rerun dependent, kick and
  resume after a restart) and `TestNoHoldModifyClearsBump`, so no new test
  was added for it.

Gates: `make lint` 0 issues; `go test -race -count 3 -run
'TestNoHold|TestReliable4Rac' ./jobqueue/` ok; `make test` with `OS_*`
unset fails only `TestResumeRacingReserveKeepsRACPending` and
`TestRerunReplacementReadyCallbackBlocksReserve` (item 5.3).

- [x] implemented
- [x] reviewed

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

Comments: `resumeItemReadyHook`'s doc now says only that it runs once
`ResumeWith` has returned. `prepareReadyJob`'s note says the queue's ready
hook gives the item its reserve group as it enters ready and this function
only moves it on a change. `ensureReserveGroup`, `recordSkippedReadyJob`
and `buildSchedulerGroups` no longer describe an add-time group: the ready
hook set it, and the RAC writes only a changed group. `readyAddedCallback`'s
"so newly added jobs get their reserve group" became "pick up newer learned
values". `closeServerCommsAndDB`'s doc lost "frees any clients waiting on a
reserve" (its code went in item 5.1). Item 5.1's `kickJobs` wording and
`recoveredItemDef` (reworded in phase 4) were already correct. A repo-wide
grep (Go, DEVELOPERS.md, developers/) finds no other description of the
hold.

Tests:

- `TestResumeRacingReserveGetsPreparedJob` (E3.1, `resume_order_test.go`):
  `ReqStatRefreshInterval: -1`, RAM 300 seeded with `seedReqGroupStats`
  after the suspend; the job `resumeItemReadyHook` reserves has RAM 300,
  and with the RAC gated another reserve returns nil within 1s
  (`reserveReturnsWhileGated`). That leaf opens the gate in a defer, so a
  held reserve cannot stall the runner's disconnect. The file's header no
  longer mentions the hold.
- `TestRerunReplacementReservableAtOnce` (E3.2, `jobqueue_test.go`): with
  the callback held (it no longer calls `finishRAC`), the reserve returns
  the child within 1s with RepGroup `"rerun-replacement-new"`.
- E3.3: the four `TestReliable4SlowRequestHandleRequestWiring` leaves pass.
- E3.4: the five listed files, and `reqstat_cache_test.go` (which read
  `racRunning` the same way), poll `server.racIdle()`; the
  `reliable4_rac_bound_test.go` comment no longer says an Add may not have
  set `racRunning`. All pass, the `reliability_repro` ones
  (`TestReliable4ReportStorm`, `TestReliable4BacklogRescan`) included.

Mutants, each in a scratch copy:

- `prepareForReady` not recomputing requirements: KILLED by
  `TestResumeRacingReserveGetsPreparedJob` (RAM 10, not 300).
- `resumeJobs` not warming the learned-value cache: KILLED by the same
  (RAM 10).
- `handleReserve` waiting until `racIdle()` (a hold): KILLED by both
  reworked tests (no return within 1s).

`make test` (`OS_*` unset) failed once, in `TestReleaseAfterLost`'s "left
delayed" case (line 314: the group's count never fell to 0). It passed 20
of 20 alone and 40 of 40 as 8 parallel processes. Cause, shown with a
scratch test: with the hold gone, a reserve can take a job after the RAC's
ready snapshot and before it counts, so `accountForRunningJobs` counts the
job again as running, giving a count of 2 for one job. The release takes
it to 1, and nothing recounts it. This comes from item 5.1's change, not
from this item; reported to the caller. A second `make test` passed.

Gates: `make lint` 0 issues; `go test -race -count 3 -run
'TestNoHold|TestReliable4Rac|TestResume|TestRerun' ./jobqueue/` ok;
`CGO_ENABLED=1 make race` (`OS_*` unset) passed; `make test` (`OS_*`
unset) failed once as above, then passed.

- [x] implemented
- [x] reviewed

### Item 5.4: RAC double-counts a job reserved during its run

Found by item 5.3's `make test` (`TestReleaseAfterLost`, "left delayed,
and another job wanting a runner", release_after_lost_test.go:314, the
group's count never fell to 0). With the hold gone, a runner can reserve
a job after the RAC took its ready snapshot. `buildSchedulerGroups` then
counts the job as ready from the stale snapshot and
`accountForRunningJobs` counts it again as running, so the group's
scheduled count is 2 for one job. Nothing recounts it when the job is
then released delayed, so the manager keeps wanting one runner too many.
The hold kept reserves out during a RAC run, so this window is new with
item 5.1 and stays on this branch.

Deterministic repro (scratch, package jobqueue, fails "Expected 1,
Actual 2"): gate the RAC after its ready snapshot; with `RunnerCmd`
set, have a runner `ReserveScheduled` the new job; open the gate;
`currentGroupCount(group)` is 2. Add it as a red test, fix the double
count (for example, count a job once when it is both in the ready
snapshot and running), and keep `make test` green.

Fix (cost proportional to the jobs that leave ready during a cycle, not to
the cycle's ready jobs):

- The queue's ready sub-queue records the items that `pop` (reserve) or
  `remove` (bury, suspend, ready to dependent, remove) take out of it while a
  recording window is open. `readyItemData` opens the window, under the
  queue's read lock, as it takes the snapshot it gives the ready-added
  callback, and `runReadyAddedCb` closes it once the callback returns, so the
  window covers exactly one callback; callbacks never overlap (the queue runs
  one at a time, a recall only after the last returns), and the RAC recheck
  goes through the same path. Recording is an append under the sub-queue's
  own lock, already held there.
- `queue.GetRunningDataAndLeftReady` returns the running data and, under the
  same read lock, the data of the recorded items not back in ready (empty
  outside a callback).
- `prepareAndCountReadyJob` sets the job's `racCountedIn` (an
  `atomic.Uint64`) to the `racID` of the group it counted it in;
  `Server.ensureGroup` gives every group a cycle makes a new, never reused
  `racID`. `accountForRunningJobs` (via `runningDataUncountingLeftReady`)
  takes 1 off the group whose `racID` a leaver carries, so a job counted by an
  earlier cycle, or skipped by this one, is left alone. The queue reports each
  leaver once however often it left ready (suspended, resumed and reserved, or
  reserved, released and reserved again), and the decrement also clears the
  job's `racCountedIn`, so a job is never uncounted twice.

So the counts are those of the moment the running jobs are read: a job
reserved during the cycle and still running (or lost, which stays in Run) is
counted once, as running; one whose run ended before then (archived,
released to delayed, buried) is not counted; one released back to ready and
not reserved again is counted once, as ready. Several scheduler groups and
the limit-group cap (applied afterwards) see corrected counts. Skipped
(limit-blocked) jobs carry no `racCountedIn` from this cycle, so a skipped job
reserved during it leaves `skipped` one high, which `sgroup.decrement` absorbs
on its completion and re-triggers the RAC, as before. No queue call is made
under the queue's lock; the RAC takes no new lock; the queue takes only its
own sub-queue lock, as `pop` and `remove` already did. Called outside a
callback (as the reliable3 accounting tests do), nothing is subtracted.

`racCountedIn` is a number rather than the `*sgroup`, and sits beside
`derived`, which the cycle already reads for every counted job: a pointer
store cost about 15ms per 100k counted jobs (GC write barrier and a cache line
the cycle did not otherwise touch), measured as +14 to +18% on
`BenchmarkRACCycleCounted`.

Tests: `TestNoHoldRACCountsReservedOnce` (`nohold_test.go`), each leaf gating
the RAC after its snapshot and changing the job before opening it: still
running (1), lost (1), archived (0), released to delayed (0), buried (0),
three scheduler groups with two reserved (1 each), 2 jobs of a limit group of
5 with one reserved (2), and a job an earlier callback counted, released to
ready and reserved again during this one while another job of its group is
ready (2; it reads the count as the gated callback left it, before the recall
the release triggers), and a job suspended, resumed and reserved during the
callback, so leaving ready twice (1). Red on the worktree before the fix: the
first 7 leaves (2 not 1, 2 not 1, 1 not 0, 1 not 0, 1 not 0, 2 not 1, 3 not
2); the eighth guards the `racID` check and passes without the fix; the ninth
guards the leaver dedupe. The eighth and ninth set `ShutdownRunnerWait` to 1s,
as stopping their manager otherwise waited out a 15s touch interval in about
half the runs. `TestGetRunningDataAndLeftReady`
(`queue/running_update_test.go`) covers the queue call: running, buried and
removed items are reported, an item released back to ready is not, an item
that leaves ready twice in one callback (reserved, released to ready, reserved
again) is reported once, nothing is reported outside a callback or after one
has finished, and the next callback sees only its own.

Mutants, each in a scratch copy: no decrement, no `racCountedIn` set, and no
`racID` given: KILLED by the first 7 leaves; leavers not recorded on `pop`:
KILLED by the first 7 leaves and the queue test; not recorded on `remove`,
and an item back in ready not filtered out: KILLED by the queue test; the
window never opened: KILLED by the first 7 leaves and the queue test; never
closed: KILLED by the queue test; the group found by the job's current
scheduler group instead of its `racID`: KILLED by the eighth leaf; leavers
not deduplicated (`leavers` returns every recorded entry): KILLED by the
queue test ("back data" twice), and, with the decrement's clearing of
`racCountedIn` also removed, by the ninth leaf (0 not 1). With the clearing
kept, the ninth leaf passes under that mutant, as the clearing alone stops
the second decrement; removing only the clearing is killed by nothing, as the
dedupe already stops it (defence in depth).

Benchmarks: `BenchmarkRACCycleCounted` (`rac_backlog_bench_test.go`, added to
`developers/speed.sh`'s default set and DEVELOPERS.md): one cycle through the
queue's ready-added callback counting 100k ready jobs with no limit group in 4
groups plus 2000 running, with 0 or 100 jobs reserved during the cycle.
`BenchmarkQueueLifecycle` gained `callback=running` variants that reserve and
remove while a ready-added callback runs (the reserve hot path with the
window open). `racCountedPutBack` waits, untimed, until no ready-added
callback is running after every op, including at 0 leavers, as the cycle's
callback hands back its result before `runReadyAddedCb` has finished; it
already did, and its doc now says so.

Speed (`developers/speed.sh`, base = this branch with 5.4's production change
reverted and the new benchmarks kept):

- first design (per-key check under the read lock): the cycle went 58ms to
  84ms (+44%), 6.9MiB to 14.7MiB, with about 21ms more read-lock hold.
- final design, 10 rounds, every default benchmark: no time regression.
  `RACCycleCounted/leavers=0` 83.67ms vs 83.44ms (~), `leavers=100` 92.62ms vs
  92.61ms (~), B/op unchanged (+0.11% at 100 leavers). The gate's one flag
  is `leavers=100` allocs/op 55 to 73 (+18 per cycle, for 100 leavers: the
  recorded slice, its dedupe and the lookup maps). `QueueLifecycle` times
  unchanged; the `callback=running` variants +17KiB (+4%) and +11 allocs per
  1000 items, the recorded slice. `BuildSchedulerGroupsBacklog` and
  `ReadyBacklogSnapshot` unchanged.

Gates: `make lint` 0 issues; `go test -race -count 3 -run
'TestNoHold|TestReliable4Rac|TestReleaseAfterLost|TestResume|TestRerun'
./jobqueue/` ok; `queue` `-race -count 3` ok; `TestReleaseAfterLost`
`-count 20` in 8 parallel processes, 160 of 160 passed; `make test` and
`CGO_ENABLED=1 make race` (`OS_*` unset) passed (998 passed each). After
the review fixes (dedupe tests, `racCountedIn` cleared on decrement), the same
lint, targeted `-race -count 3` jobqueue and queue runs, `make test` and
`CGO_ENABLED=1 make race` all passed again (998 passed each), and
`BenchmarkRACCycleCounted` still runs (5 ops each: 94.9ms leavers=0, 101.5ms
leavers=100).

- [x] implemented
- [x] reviewed
