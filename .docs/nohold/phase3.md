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

- [ ] implemented
- [ ] reviewed

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

- [ ] implemented
- [ ] reviewed

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

- [ ] implemented
- [ ] reviewed
