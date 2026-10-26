# Phase 3: fail-time bumps

Ref: [spec.md](spec.md) sections C1, C2, C3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

Needs phase 1 (A2's `With` callbacks) and phase 2 (the C tests set
`ReqStatRefreshInterval`, and `applyRunEndLocked` reads
`cachedReqGroupRecs`). The items run in sequence: all three share
`updateJobRequirementsForRetry`, the new `Job` fields and
`jobqueue/nohold_test.go`, and C2 and C3 build on C1's fail-time
recompute. The hold is still present, so C1.1 is green here; every
Section C test follows the setup rules at the head of spec.md's Section C
(gate installed only after polling `racIdle()`, no reserve after an
operation that still sets `racPending` while the gate is closed).

Red tests (C1.1, C1.9, C2.1, C3.1, C3.5) must be shown failing on the base
before the fix, using only what the base has, as spec.md's Key Decisions
"Testing" describes.

## Items

### Item 3.1: C1 - A release or bury applies the bump with the queue move

spec.md section: C1

In new `jobqueue/ready_prepare.go`, add the `FailBump*` bits and
`applyFailureBumpsLocked`, and move `updateJobRequirementsForRetry` there
from `jobqueue/server.go`, rewritten as the five steps in spec.md "Failure
bumps". Add the persisted `Job` fields
`FailBumpsCleared`, `ModifiedReqs` and `FailedRunReqs`, copy them in
`copyJobForClient`, and make `JobModifier.applyRequirements` set the bits
and record `ModifiedReqs` (C1.9, C1.11 and C1.13 assert them). Make
`newJobExitData` take `requiredRAM` from `FailedRunReqs.RAM` for a failed
job (C2 test 7 checks the stat). Add `applyRunEndLocked` and call it from
the `ReleaseWith`, `RequeueWith` and `BuryWith` callbacks in
`applyReleaseQueueChange` and `buryItemWhereItIs`, trimming
`finalizeReleasedJob` to the steps in "Run end applied with the queue
move". Add `recomputeJobRequirements` and call it from
`ackAlreadyReleased` (re-preparing a ready item as after a modify) and
`releaseBuriedWaiting`, as "Owner's report after a lost release" gives.

Also add `racIdle()` (spec.md "Removed": `!q.ReadyAddedCallbackRunning()`
and `!racPending` under `rpmutex` until phase 5) and switch B1 test 4 to
it. Move the probe's `racGates` and `reserveReturnsWhileGated` from
`.docs/nohold/racpending_probe_test.go.txt` into new
`jobqueue/nohold_test.go`. Audit every `s.q.` and `q.` call in `jobqueue`
for a job lock held across a queue call (spec.md "jobqueue ready hook")
and list the result in the phase's checklist; the known `modifyJob` case
is fixed in item 3.3.

Covering all 13 acceptance tests from C1, in `jobqueue/nohold_test.go`;
test 12 updates `TestJobqueueSignal` and lists every other updated
assertion in the phase's checklist.

Lock-order audit (spec "jobqueue ready hook"): every `s.q.`, `q.` and
`server.q.` call in non-test `jobqueue/*.go` (76 sites) was checked for a
job's lock held across it, directly or through a caller.

- Only `JobModifier.modifyJob` holds a job's lock across a queue call:
  `skipForDuplicateKey` -> `Server.checkJobByKey` -> `s.q.Get`. Item 3.3
  fixes it.
- `deletePass.remove`, `buryImpossibleItem`, `jobHasDependents`,
  `prepareReadyJob` and `lostJobRetryCheck` take and release the job's lock
  before or after their queue calls, never across them.
- The other locks held across queue calls are not job locks:
  `waitThenRecheckRAC` holds `racmutex` across `q.Stats` (why the hook reads
  no `racmutex`), `waitForDrainComplete` holds `ssmutex`, and
  `changeModifiedJobKeys` and `changeModifiedQueueKeys` hold the `rpl` lock
  across `q.ChangeKey`.
- The new `ReleaseWith`, `RequeueWith` and `BuryWith` callbacks
  (`runEndCallback`) lock the job under the queue's lock, the allowed order,
  and make no queue call; their only other locks are the reqstat cache's
  `mu`, through `cachedReqGroupRecs` and `refreshReqGroupRecsLater`.
  `recomputeJobRequirements` makes no queue call, and
  `prepareReadyJobAgain` holds no job lock across `q.Get` or
  `q.SetReserveGroup`.

C1 test 12 sweep: the only existing assertion on the manager's copy of a
failed job's requirements that changed is `TestJobqueueSignal`'s
`job2.Requirements.Time.Seconds()` (now at least 3601, comment rewritten).
Every other `Requirements.RAM`, `.Time` or `.Disk` assertion in
`jobqueue/*_test.go` reads a reserved or completed job, a job with no
`FailedRunReqs`, or the runner's own copy, and a full `jobqueue` run passes
unchanged. The `jobqueue_test.go` comment before `jq.Kick` (about line
7369) is C2 test 6, item 3.2.

Deliberate addition: when `prepareReadyJobAgain` (used by
`ackAlreadyReleased` after an owner's report on a lost release) moves a
ready item to a new reserve group, it also calls
`triggerReadyAddedCallback`. The spec does not mention this. Only runners
of the new group can reserve the item, and nothing else would schedule one
until the periodic `CheckRunnerTime` recheck; the RAC recount schedules it
and drops the old group's count. After a modify (item 3.3) the server's
`Resume` already triggers the RAC.

Signature difference: `applyRunEndLocked` takes a second parameter,
`rec *scheduler.Requirements`, which spec.md's signature does not have. A
`Job` cannot reach the database's learned-requirement cache, so
`runEndCallback` passes the cached values from `currentReqGroupRecs`. It
reads them under the job's lock, inside the queue's lock, so the lock order
is queue, then job, then the cache's `mu`.

- [x] implemented
- [x] reviewed

### Item 3.2: C2 - The high-peak bump applies to buried jobs

spec.md section: C2

Make `shouldIncreaseJobRAMAfterHighPeak` stop reading `job.State` and
return false for `FailReasonResource`. Rework the
`memory_attribution_test.go` leaves and add the new
`updateJobRequirementsForRetry` leaves listed in test 5, drop the State
lines from the `TestHighPeakMemoryRetryGrowth` leaves that set
`JobStateDelayed`, and rewrite the `jobqueue_test.go` comment of test 6.
Covering all 9 acceptance tests from C2, in `jobqueue/nohold_test.go` and
`jobqueue/memory_attribution_test.go`. Depends on item 3.1.

Notes: the new `updateJobRequirementsForRetry` leaves are in
`TestHighPeakRetryRecompute`. The `FailReasonResource` leaf also asserts that
with no learned value the job keeps RAM 64000; the spec's learned-200 case
gives 200 with or without the reset, so only this assertion fails when the
reset's `FailReasonResource` exclusion is removed. C2.3, C2.7, C2.8 and C2.9
already pass after item 3.1: the delay's expiry does not change the
manager's `job.State` (only a web status request for the job syncs it), so
in these tests a RAC recompute of a once-delayed job still saw
`JobStateDelayed`. C2.1 and the State-free leaves are the tests that fail
without this item.

Not in the spec: `wr status`'s "note: command used too much RAM" compared
the peak with `Requirements.RAM`, which is now raised when the job is buried,
so `cmd`'s `TestStatusDetailsPreservesHighMemoryFailureNote` failed.
`statusRequiredRAMForProblem` (`cmd/status.go`) now uses `FailedRunReqs.RAM`
when it is set, as `newJobExitData` does.

Also not in the spec: the web status page's over-limit highlights compared
peak RAM, walltime and peak disk with `ExpectedRAM`, `ExpectedTime` and
`RequestedDisk`, which a failed job now shows already raised, so a buried or
delayed job's overuse was no longer flagged. Those fields still mean the next
run's requirements (`modify-job.js` fills the modify form from them).
`JStatus` gains `RunRAM`, `RunTime` and `RunDisk`, taken from
`Job.lastRunReqs` (`FailedRunReqs` while a failure is pending, else
`Requirements`; `newJobExitData` uses the same helper), and the highlights in
`status.html` use them. `websocket-handler.js` keeps them across running push
updates as it does the expected values, and the screenshot fixtures carry
them. `TestNoHoldStatusPageOverLimit` evaluates the page's own highlight
conditions in node against a buried RAM, time and disk failure's `JStatus`,
and checks that a retry reserved at the raised RAM has `RunRAM` 1200: the
`FailReason` guard in `lastRunReqs` matters, since `FailedRunReqs` is never
cleared. `TestStatusPageLivePushUpdateBehaviour` checks that a reserved push
update keeps the Run fields.

- [x] implemented
- [x] reviewed

### Item 3.3: C3 - `wr mod` clears a pending bump

spec.md section: C3

After a modify, prepare every modified job whose item is ready again
(recompute under the job lock, release it, then `q.SetReserveGroup`)
before the server resumes, in both `handleModify` (`jobqueue/serverCLI.go`)
and `modifyJobsByKeys` (`jobqueue/serverREST.go`). Add the
`modifyKeyCheckHook` seam and make `JobModifier.modifyJob` release the
job's lock before its duplicate-key queue call, or check before taking
the lock. Covering all 10 acceptance tests from C3, in
`jobqueue/nohold_test.go`. Test 10 only discriminates once phase 4's hook
exists; the reviewer of phase 4 re-runs it. Depends on items 3.1 and 3.2.

The tests are `TestNoHoldModifyClearsBump`. `prepareModifiedJobs`
(`jobqueue/ready_prepare.go`) is called by `modifyJobs` (the CLI path, after
`persistModifiedJobs`) and by `modifyJobsByKeys` (after `storeModifiedJobs`,
also when that returns an error, since the jobs were modified in memory). For
each modified job whose item is ready it calls `recomputeJobRequirements` and
then `prepareReadyJobAgain`, so the lock order stays queue, then job.
`modifyJob` reads the old and new keys under the job's read lock, makes the
duplicate-key check (and calls `modifyKeyCheckHook`) holding no job lock, then
takes the write lock and applies the modification only if the job's key is
unchanged, checking again if it changed.

Test 10 only discriminates once phase 4 installs the ready hook: in phase 3
nothing locks a delayed job under the queue's lock when its delay expires, so
a `modifyJob` that holds the job's lock across `checkJobByKey` still passes.
The phase 4 reviewer must re-run test 10 against that mutant. With a test
ready hook that locks the job added to test 10 in a scratch copy, the mutant
deadlocked in `skipForDuplicateKey` -> `checkJobByKey` -> `q.Get` and the test
failed, while the fix passed.

Not in the spec: a REST-path leaf of test 5, since test 7's job is buried and
so does not show `modifyJobsByKeys` preparing a ready item. Also not in the
spec: the item 3.2 review's Override 2 concern. `prepareReadyJob` recomputes
only when there are learned values or `failureMayUpdateJobRequirements` is true,
and after a high-peak bump that check compares the peak with the bumped RAM. A
ready job with no learned values whose user sets `--override 2` would then
keep 1200. The modify path recomputes ready jobs without that guard, which the
leaf "a retried job, now ready, whose RAM a high peak bumped" proves (it fails
without `prepareModifiedJobs`). A buried or delayed job modified that way
still relies on the RAC's guarded recompute when it is kicked or its delay
expires, which phase 4's ready hook replaces with an unconditional one. The
phase 4 reviewer should check that case. Its job normally has its own
high-peak stat as a learned value, which makes the RAC recompute anyway.

- [x] implemented
- [x] reviewed
