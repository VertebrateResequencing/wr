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

Phase 3 hand-off (see the notes under Items 3.3 and 3.1 in phase3.md): the
reviewer of this item must re-run C3.10 against the mutant that keeps the job
lock across `modifyJob`'s duplicate-key check, now that the ready hook exists,
and check `wr mod --override 2` on a buried or delayed high-peak-bumped job
with no learned values returns to the failed run's size once ready.

The tests are `TestNoHoldReadyPrepare` (D1 tests 1 to 10, in order).
`prepareForReady` (`jobqueue/ready_prepare.go`) locks the job, recomputes with
`currentReqGroupRecs` (which starts `refreshReqGroupRecsLater` when the values
are not current) and, with a runner command, sets `job.schedulerGroup` to
`job.derivedLocked().group` and returns it. `Serve` stores the runner command
with `setRC` just before `createQueue`; `setRC` and `runnerCommand` no longer
take `racmutex`. The warms are in `enqueueItems` (the item defs
`itemDefTriggersReadyAdded` accepts), `queueNewJobItems` (every item def, before
`replaceLiveRerunItems`), `kickJobs` and `resumeJobs` (the queued job of each
key, through the new `queuedJobs`). On the add path `enqueueItems` therefore
warms the ready item defs a second time; for a ReqGroup already current that is
a lock and a check, no load (D1.7 asserts exactly 1 load).

Lock audit of the new acquisitions: the hook takes the job's write lock under
the queue's write lock, and under it only the cache's `mu` (through
`cachedReqGroupRecs` and `refreshReqGroupRecsLater`, which starts the load in a
goroutine and does not wait for it). It makes no queue call, does no bolt I/O,
takes no Server mutex and never calls `schedulerGroupSnapshot`. Nothing takes a
job's lock while holding the cache's `mu`. The warms run with no queue or job
lock held (each takes a job's read lock briefly to read `ReqGroup`, then the
cache's `mu`); `queuedJobs` calls `q.Get` with no job lock held. The phase 3
audit still holds: no caller holds a job's lock across a queue call.

D1 tests 4 (dependency leaf) and 4 (live-rerun leaf) use a dep group, not an
essence dependency. The live-rerun leaf goes through the real add path: the
user re-adds (`ignoreComplete` false) a job whose complete item waits on a
parent of another ReqGroup, so only `queueNewJobItems`' warm loads its learned
300 (the spec's leaf calls `replaceLiveRerunItem` directly).

Red evidence: on 16f8070a (phases 1 to 3) every new D1 test fails except D1.6,
which guards the hook not counting `racScanWork` and passes there too. D1.10
fails on the base 6f1b41bb (a self-contained copy using only the base's API:
`RunnerCmd` config, the base's hold check, an inline RAC gate): after the delay
expires the item and job stay in group `110:30:1:0` with RAM 10.

Mutants (each run against `TestNoHold`): hook not installed, hook without the
recompute, hook under `prepareReadyJob`'s guard (`rec != nil ||
failureMayUpdateJobRequirements`), no warm in `queueNewJobItems`, `kickJobs`
or `resumeJobs`, hook not setting `job.schedulerGroup`, hook returning the old
group, hook returning a group with no runner command, and hook counting
`racScanWork` are all killed. No warm in `enqueueItems` survives: on the add
path `queueNewJobItems` already warms, so only recovery and resurrected item
defs depend on it, which D2's tests (item 4.2) cover. The hook taking
`racmutex` survives: D4 test 2 (item 4.4) covers it.

C3 test 10 is strengthened: it now has the runner command `serverRC` and the
RAC gated, fails the run from the RAM 10 group, and asserts that the ready item
under its new key and the job are in the RAM 1200 group, which the item only
reaches by being prepared as it becomes ready (the hook, or
`prepareModifiedJobs` once the modify sees it ready). Its old assertions
(reserve group "", RAM 1200) held without any preparation. Against the mutant
that keeps the job's lock across `modifyJob`'s duplicate-key check, the test
now deadlocks (the package's 10 minute test timeout fires): KILLED.

Override 2 hand-off: `TestNoHoldModifyClearsBump` gains "Given no learned
values (every load fails), the RAC gated and a job whose RAM a high peak
bumped", with a buried leaf (kicked, its item has RAM 10 when `Kick` returns)
and a delayed leaf (RAM 10 once ready). Failing every load keeps the cached
learned values nil, which is the "no learned values" case: the job's own
high-peak stat would otherwise give it learned values. Both fail on 16f8070a
(RAM 1200) and kill the guarded-hook mutant.

D1 test 11 sweep: a full `make test` passes with the hook, so no existing
assertion encodes unprepared add-time state. Tests that wait for the RAC to
prepare what the hook now prepares at once, unchanged since they still hold:
`suspend_resume_test.go`'s `waitForSchedulerGroup` polls,
`runner_scheduling_test.go`'s and `queues_avoid_scheduling_test.go`'s
`waitForScheduledGroupCount`, and `db_runstate_test.go`'s poll for the learned
RAM before the reserve. Comments updated: `reliable4_snapshot_memo_test.go`
(nothing derives at add time only because it has no runner command) and
`reliable4RSWaitForGroups` (the hook gives the group at add; the wait is for
the RAC's previously-scheduled groups). The `Server{rc: ...}` literals in
`schedule_retry_backoff_test.go`, `reliable2_scheduledeadlock_test.go` and
`reliable4_schedule_async_test.go` call `setRC`.

Speed (`developers/speed.sh`, base 16f8070a, head this item, interleaved):
the default quick set (6 rounds, plus `report-storm` and
`dep-granularity-check`) is PASS with no row changed at p < 0.05; and
`^Benchmark((Kick|Resume|Suspend)UnderReserveLoad|AddManyUnderReserveLoadAdhoc)$`
at 10 rounds is PASS with no row changed (ns/op geomean -3.5%; Kick 124.1ms to
113.2ms, Resume 109.8ms to 99.4ms, Suspend 93.0ms to 98.9ms, one Add of 10,000
jobs under reserve load 789ms to 780ms; reserve p50 and p99 unchanged).
`AddManyUnderReserveLoadAdhoc` was a scratch copy of what item 4.4's
`BenchmarkAddManyUnderReserveLoad` describes, in both trees. These benchmarks
have no runner command; a scratch micro-benchmark of the hook itself gives about
75ns per item with no runner command and about 750ns (7 allocations) with one,
so recovering about 118k live ready jobs adds about 90ms under the queue lock.

Test helpers renamed for unparam: `noHoldRunnerCmd(serverRC)` is now
`noHoldServerRC`, and `overrideModifier(jobOverrideAlwaysUseJobReqs)` is
`override2Modifier()`.

- [x] implemented
- [x] reviewed

### Item 4.2: D2 - Recovered ready items are prepared

spec.md section: D2

Make `recoveredItemDef` (`jobqueue/server.go`) stop special-casing ready
jobs and delete its comment about keeping group ""; recovered running jobs
still get their group there. Covering all 3 acceptance tests from D2, in
`jobqueue/nohold_test.go` (test 3 is the existing recovery tests). Depends
on item 4.1.

`recoveredItemDef` had no code that special-cased ready jobs: a decoded job's
`schedulerGroup` is "", only jobs recovered into Run had it recomputed, and
ready jobs kept "" only because nothing else set it. Item 4.1's hook now
prepares a recovered ready item as `enqueueItems` queues it (after its warm
loads the cold cache), so this item's change is the comment: it no longer says
ready jobs must keep group "" and says the hook prepares them. The intoRun
recompute stays.

The tests are `TestNoHoldRecoveredReadyPrepared` (D2 tests 1 and 2), with the
new helper `restartPaused`, which wraps `pausedRecoveringFixtureServer`. D2 test
3 is the existing recovery tests: `TestReliable3RecoveryRestoresSchedulerGroup`,
`TestReliable4RecoveredRunnerDead`, the `reserve_durability`,
`depgranularity_recovery`, `depgranularity_startup`, `reliable2_startup`,
`reliable2_dbcompat`, `server_startup`, `release_durability`,
`start_durability`, `kick_order`, `moved_on_runner` and
`reliable4_recovery_log` tests, and the rerun and crash-mid-add tests, which
pass unchanged.

Red evidence: D2.1 fails on the base 6f1b41bb (a self-contained copy using only
the base's API: `putJobStat`, the base's `racPending`/`racRunning` hold check,
an inline RAC gate): the recovered item is in group "" at RAM 10. Both D2 tests
also fail on 16f8070a (group "" and RAM 10). They pass on the item 4.1 tree
before this item's change, since 4.1's hook is what prepares the item.

Mutants (each run against `TestNoHold` and the recovery tests above):
`recoveredItemDef` keeping the old behaviour is the same code as this item
(the special case was only in the comment), so it cannot be killed and has no
functional mutant there. Recomputing the group for every recovered job, not
only intoRun ones, is killed by D2 test 2 (group `110:30:1:0`, not "") and by
`TestCrashMidAddRerun`. Recovered running jobs not getting their group is
killed by `TestReliable3RecoveryRestoresSchedulerGroup`. No warm in
`enqueueItems` (item 4.1's survivor) is killed by both D2 tests (group
`110:30:1:0` and RAM 10).

`reliable4_reportstorm_test.go`'s `reliable4RSLatBuckets` move stays:
`cleanorder -min-diff` makes it on an untouched copy of the file at 16f8070a
too.

Recovery time (a scratch test, not committed: 100,000 ready jobs over 100
ReqGroups each with learned RAM 300, runner command `serverRC`, timed from
`recoveryPauseHook` to recovery's end, 3 interleaved rounds of 5 restarts):
16f8070a medians 214, 227 and 213ms; this tree 387, 384 and 364ms. So the
hook plus `enqueueItems`' warm add about 160ms per 100k ready jobs (about
1.6us per job), every item getting group `400:30:1:0` and RAM 300 where
16f8070a leaves "" and 10. This item's comment change adds nothing on top of
item 4.1.

Review fix: `gateRAC`'s callback read `n.server.q` when a gate opened, which
raced `Stop` clearing the server's queue (D2's teardown always has recovery's
RAC waiting at the gate) and could reach another server's queue after a
restart; it now keeps the server, queue and gates it gated. The new `stop`
helper, used by `Reset`, `restart` and `restartPaused`, opens and clears
`n.gates` before stopping, so a restart no longer leaves a callback at the old
server's gate. `go test -race -count 10 -run TestNoHold` is clean 3 times.

- [x] implemented
- [x] reviewed

### Item 4.3: D3 - The RAC remains a floor

spec.md section: D3

Confirm the RAC still re-prepares long-waiting ready jobs from newer
learning, and that the reliable4 bounded scan still leaves the
limit-blocked remainder unprepared. Covering both acceptance tests from
D3, in `jobqueue/nohold_test.go` (test 2 is the existing
`TestReliable4Rac*` tests). Depends on item 4.1.

No production change: both behaviours already held. D3 test 1 is
`TestNoHoldRACFloor`. Its job is added with learned 300, so the hook puts it in
the RAM 300 group; RAM 800 is then seeded (stats {300, 800} recommend 800) and
`q.TriggerReadyAddedCallback` fires the ungated RAC. The test polls for 5s until
the job has RAM 800 and the RAM 800 scheduler group, `q.HasReady` is true for
the RAM 800 group and false for the RAM 300 group. It reads the item's reserve
group through `HasReady`, which takes the queue's lock, because reading
`item.ReserveGroup` directly would race with the ungated RAC's
`SetReserveGroup`. The new constant `noHoldNewerLearnedRAM` (800) holds the
seeded value. D3 test 2 is `TestReliable4RacBoundedBySchedulable`, which passes
unchanged. Its `racScanWork == limit` assertions show that the limit-blocked
remainder is not prepared.

Mutants, each in a scratch copy of this tree. Each of these 3 is killed by
`TestNoHoldRACFloor` after its 5s poll, so the hook's earlier preparation does
not mask the RAC:

- `prepareReadyJob` skipping the recompute (`updateJobRequirementsForRetry`
  never runs) leaves RAM 300.
- `prepareReadyJob` not calling `regroupJob` leaves the job's group.
- `regroupJob` setting the job's group but not calling `q.SetReserveGroup`
  leaves the item in the RAM 300 group.

`buildSchedulerGroups` also calling `prepareReadyJob` for limit-blocked jobs
is killed by `TestReliable4RacBoundedBySchedulable` (scan work 14 and 6 where 5
is expected), while every `TestNoHold` test passes.

That test checks "not prepared" only through the `racScanWork` counter. A
mutant that recomputed the remainder's requirements without going through
`prepareReadyJob` would not be caught. The spec's D3.2 is the existing tests,
so this item adds no test for that case.

- [x] implemented
- [x] reviewed

### Item 4.4: D4 - No bolt I/O under the queue lock

spec.md section: D4

Prove the hook does no bolt I/O, takes no `racmutex` and reads the runner
command from `rcValue`. Add `BenchmarkAddManyUnderReserveLoad` to
`jobqueue/kick_resume_bench_test.go`, modelled on
`BenchmarkKickUnderReserveLoad` (adds 10,000 jobs in one `Add` while
reservers run), for G1 test 3. Covering all 5 acceptance tests from D4, in
`jobqueue/nohold_test.go`, with test 4 as `CGO_ENABLED=1 make race` (all
`OS_*` unset). Depends on items 4.1 to 4.3.

No production change. The tests are `TestNoHoldNoIOUnderQueueLock` (D4 tests
1, 2, 3 and 5). D4 test 1 does not gate the RAC: with it gated, the hold makes
the reserves its setup needs after an add wait for ever. Instead each step
waits for `racIdle()` before invalidating the ReqGroup, so no RAC can load it
before the hook does, and the probe (`probeQueueLockOnLoad`) asserts that each
step started at least one load and that every load was checked. Delay expiry
and dependency satisfaction are the steps that reach the hook's own background
load; add, kick and resume load through their warms. D4 test 2 holds
`racmutex` from the test goroutine and polls `q.Stats()` (each call given 1s)
until the item is ready, unlocking before any assertion. D4 test 5 registers
the held load's release before `newNoHoldTest`, since Reset runs first in,
first out and `Stop` waits for the cache's loads.

Mutants, each in a scratch copy of this tree, against
`TestNoHoldNoIOUnderQueueLock`:

- The hook calling `refreshReqGroupRecs` (a bolt read when not current):
  KILLED by test 1 (2 loads under the lock: dependency satisfied and delay
  expiry).
- The hook taking `racmutex` (item 4.1's survivor, and the old
  `runnerCommand` path): KILLED by test 2 (`Stats` does not return).
- The hook reading a copy of the runner command taken in `Serve`, which
  `setRC` does not update: KILLED by test 3 (group `110:30:1:0`).
- `warmReqGroupRecs` loading even while a load is in flight: KILLED by test 5
  (`Add` does not return within 1s).
- The hook reading a plain, non-atomic field that `setRC` also writes:
  SURVIVED, including `-race` over `TestNoHold`, the `ScheduleRetry` tests,
  `TestReliable2ScheduleGroupDeadlock`, `TestReliable4ArchiveSchedulingAsync`
  and `TestJobqueueRunnerScheduling`. Every test's `setRC` is ordered before
  the hook by the queue's lock (for example `gateRAC`'s
  `SetReadyAddedCallback`), so the race detector sees no race. Only a test
  calling `setRC` concurrently with adds could catch it, and D4 does not ask
  for one.

Item 4.3's gap is closed: `TestReliable4RacBoundedBySchedulable` gains a leaf
that seeds learned RAM 500 for the low ReqGroup after the add, runs another
`buildSchedulerGroups` and asserts all 6 limit-blocked jobs keep RAM 100 (with
`ReqStatRefreshInterval` -1, so the seed is loadable at once). The mutant that
recomputes the skipped jobs' requirements from `recommendedReqForGroup` outside
`prepareReadyJob` is KILLED (6 resized) while `racScanWork` stays at 5.

`BenchmarkAddManyUnderReserveLoad` has sub-benchmarks `norc` and `rc`
(`serverRC`). Each round starts a server, replaces its ready-added callback
with a no-op (so no runners are scheduled and the RAC does not compete), adds
the 20,000 fillers, then times one `Add` of 10,000 small jobs of the fillers'
ReqGroup while 8 reservers reserve from the fillers' reserve group (read from a
filler's item, so "" on the base with a runner command and the real group
here). The server setup, filler jobs and metric reporting are shared with the
kick, resume and suspend benchmarks (`kickResumeBenchServe`,
`kickResumeBenchFillerJobs`, `reportUnderReserveLoad`), and the reservers take
a group.

Speed (`developers/speed.sh`, base 16f8070a with this file copied in, head this
tree, interleaved, `SPEED_BENCH='^BenchmarkAddManyUnderReserveLoad$'`, 10
rounds, run twice): sec/op, reserve p50 and p99 and reserves/op unchanged
(sec/op norc 762.9ms to 751.7ms, rc 766.6ms to 762.2ms); B/op rc +0.7%, allocs
rc +5.1% (848k to 892k), norc unchanged. reserve-max-us is FAIL in both runs:
norc 11.5ms to 16.2ms (+40%), rc 14.3ms to 24.3ms (+70%; first run 11.8ms to
25.1ms, +114%). A reserve that arrives at the start of the `AddMany` waits
for its whole write-locked section, and the hook is in that section. A CPU
profile of the head benchmark (6 runs each) attributes to `prepareForReady`
0.14s of `AddMany`'s 0.23s with a runner command (0.11s of it in
`derivedLocked`) and 0.05s of 0.17s without, about 0.8us and 0.3us per item.
So the hook's own cost is what exceeds 10%: it roughly doubles how long a bulk
add of 10,000 jobs holds the queue's write lock (about 8ms more with a runner
command), and recovering about 118k live jobs pays about 90ms under the lock
(item 4.2 measured about 160ms per 100k ready jobs end to end). The owner
decides whether to accept it (spec G1.3). The kick, resume and suspend
benchmarks were not re-run: this item changes no production code, and item
4.1 compared them on the same two trees.

Hook cost cut (the owner's G1.3 ruling: cut the hook's cost under the queue's
lock first, re-measure, and come back only if it is still more than 10% worse):

- `updateJobRequirementsForRetry` no longer always invalidates the job's
  derived-strings memo. Its defer now calls `invalidateDerivedIfChangedLocked`
  with the requirements' RAM, disk, DiskSet and time captured on entry
  (`recomputedReqsOf`). These are the only `jobDerived` inputs it changes:
  `RequirementsOrig` is not an input, and it never touches Key's inputs,
  Cores, Other, LimitGroups or the Requirements pointer. Nil Requirements
  always invalidate. Keeping the defer keeps the early-return guarantee.
  `invalidateDerivedLocked`'s comment says so.
- New `prepareBeforeQueueing` (`ready_prepare.go`), called in `enqueueItems`
  right after its warm, on the same ready item defs. With no queue lock held,
  it does what the hook will do: it recomputes each job's requirements from the
  cached learned values and, with a runner command, derives its group. The
  hook's recompute then normally changes nothing, so its `derivedLocked` is a
  memo hit. This covers adds and recovery (the reviewer's steps 2 and 3) in
  one place, after `handleUserSpecifiedJobLimitGroups` has normalised the
  limit groups (it invalidates the memo). Doing it in `prepareInputJobs`
  instead would come before that normalisation and before the cache is warmed,
  so learned values would change the requirements under the lock.
  `enqueueItems`' two callers queue only fresh jobs (decoded at recovery, or
  added jobs `jobsNotAlreadyQueued` let through; live-rerun replacements are
  removed first), so no running job is recomputed. Kick and resume are not
  pre-prepared: `kickJobs` and `resumeJobs` take any keys, and recomputing a
  job that is not buried or suspended would change a running job.
- Nothing else per item under the lock is cheap to cut. The hook still takes
  the job's lock, reads the learned-requirement cache (the cache's mutex and a
  map lookup) and recomputes, as the spec requires.

Tests: `TestNoHoldHookFindsGroupDerived`. With a runner command and learned
300, an added job's group is derived before its item is queued and the hook
derives nothing more (it wraps the queue's ready hook and records the job's
`derived` and `derivations`). Suspending and resuming a ready job with nothing
learned in between derives nothing more.
`TestReliable4SchedulerGroupSnapshotInvalidated` gains a time-only change (the
group must change) and a same-values recompute (the memo is kept and
`derivations` does not grow). Both new `TestNoHold` leaves fail on the tree
before the change (derived false, before 0; derivations 3 not 2).

Mutants, each in a scratch copy:

- never invalidate: KILLED by `TestNoHoldNoIOUnderQueueLock`,
  `OwnerReportAfterLost`, `ReadyPrepare`, `RACFloor` and
  `TestReliable4SchedulerGroupSnapshotInvalidated`.
- always invalidate: KILLED by `TestNoHoldHookFindsGroupDerived` and the
  snapshot test.
- no `prepareBeforeQueueing`: KILLED by `TestNoHoldHookFindsGroupDerived`.
- `prepareBeforeQueueing` deriving without the recompute: KILLED by
  `TestNoHoldHookFindsGroupDerived`.
- comparison ignoring RAM: KILLED by the same tests as never invalidate.
- comparison ignoring time: KILLED by the snapshot test's new time case. It
  survived every test before that case was added.
- comparison ignoring disk: KILLED by the snapshot test.

Re-measured (`speed.sh`, base 16f8070a with the benchmark file copied in,
interleaved, 10 rounds): `^BenchmarkAddManyUnderReserveLoad$` alone, then the
full G1.3 regex.

| Row | Before the cut (base → head) | After, AddMany alone | After, full G1.3 run |
|---|---|---|---|
| AddMany norc sec/op | 762.9 → 751.7ms (~) | 782.6 → 800.8ms (~) | 751.5 → 771.8ms (+2.7%) |
| AddMany rc sec/op | 766.6 → 762.2ms (~) | 794.3 → 825.1ms (+3.9%) | 772.4 → 788.5ms (~) |
| AddMany norc reserve-max | 11.5 → 16.2ms (+40%) | 13.4 → 18.5ms (+38%) | 12.6 → 16.5ms (+31%) |
| AddMany rc reserve-max | 14.3 → 24.3ms (+70%) | 14.8 → 20.5ms (+38%) | 12.9 → 17.2ms (+34%) |
| AddMany p50 / p99 | unchanged | unchanged | unchanged |
| AddMany rc allocs | +5.1% | +6.9% | +8.6% |

Full G1.3 run, the other rows: Kick sec/op -11.6% (better), allocs -14%;
Resume and Suspend sec/op unchanged, allocs -25% and -22%; every reserve-max
and p99 unchanged. Flagged: Resume reserve-p50 +16% (8.3 to 9.7us; re-run
alone below) and AddMany norc reserves/op +15%, which is more reserves served
while the add ran, so better, although speed.sh counts any increase as worse.
Resume re-run alone (10 rounds): PASS, no row changed (reserve-p50 9.3 to
10.6us, p=0.25), allocs -14.6%.

Where the remaining reserve-max comes from (scratch A/B, same head binary, 10
rounds, the hook swapped per sub-benchmark): norc with the hook 17.0ms,
without 13.9ms, so the hook costs about 3.1ms per 10,000 items (0.31us each).
rc with the hook 18.4ms, with a hook that only returns the fillers' group (no
per-item work) 13.7ms, with no hook 11.6ms. So the hook's own work costs about
4.7ms per 10,000 items (0.47us each, down from about 0.8us). The other ~2ms is
the items now landing in the busy, 20,000-item reserve group the reservers use
rather than a group of their own, which is the intended behaviour. A CPU
profile of the rc case (11 runs) puts `prepareForReady` at 0.04s of
`AddMany`'s 0.31s (it was 0.14s of 0.23s), with `derivedLocked` gone from it;
in norc it is 0.07s of 0.43s, of which 0.04s is the cache read. What remains is
touching each Job under the lock: its lock, the cache read and the recompute,
which the spec requires of the hook.

Recovery of about 118k live jobs therefore pays about 37ms (no runner
command) to 55ms (with one) under the queue's lock, down from about 90ms. The
lock hold of a bulk add is still about 30 to 40% longer than on the base. That
is still more than 10%, so it goes back to the owner (spec G1.3). The one
further cut seen is a lock-free cache read (each job keeping its cache
entry, with an atomic snapshot of the entry's value and generation). That
would save at most the 0.04s of 0.07s above, and with it norc's hook share.
Touching each Job cannot be removed while the hook prepares every item.

Owner's G1.3 ruling (2026-10-14): accept the remaining cost (a bulk add's
slowest reserve about 13ms to 17ms, p50 and p99 unchanged, recovery about
37-55ms once). The lock-free cache read is not wanted. Record the per-item
and recovery cost in the PR body.

- [x] implemented
- [x] reviewed
