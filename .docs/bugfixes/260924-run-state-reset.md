# Clear a run's state in one place, and all of it at reserve

Branch `fix-run-state-reset`. Named with a suffix rather than a `YYMMDD-N`
sequence number, so that parallel branches cannot collide on the file name.

## Diagnosis

wr has two independent lists of the fields that describe one run of a job, and
nothing keeps them in step:

- `resetJobForReservation` (`jobqueue/serverCLI.go`) clears them when a job is
  reserved for a new run: every retry and every requeue.
- `resetJobExecutionFields` (`jobqueue/serverWebI.go`) clears them when a job
  is rerun from the web UI.

Neither calls the other, so every new run-describing field has to be
remembered in both places, and the two already disagree. `RunnerPid` was
cleared only by the reservation path, and fixed there in #555 after a stale
pid led the wedged-runner backstop to `kill -9` an unrelated process. These
fields still survive a reservation:

- `HostIP`: `wr status` shows the new runner's host with the previous run's
  IP.
- `FailReason`: a freshly reserved retry still reads "lost" or "command
  exited non-zero".
- `StdOutC`/`StdErrC`: output from a previous failed run stays on the job.
- `State`: written at `Started` and at each exit, never at reserve, so it
  reads the previous run's value for the whole reservation.

These were recorded on develop in `.docs/bugfixes/260827-2.md` ("Further
run-state leaks across a reservation"), and the design questions in closed
PR #600.

## Decisions (repo owner, 2026-09-24)

- Structure: one shared function that both paths call, with any deliberate
  difference between the paths passed as an explicit argument.
- Clear everything at reserve, including `State` and `StdOutC`/`StdErrC`.
  Showing a previous attempt's output on a retry is not wanted.

## Items

- [x] Both run-reset paths use one shared function, and a reservation clears
      every run-describing field, including `HostIP`, `FailReason`,
      `StdOutC`/`StdErrC` and `State`.

## Progress

### Red commands (before any fix)

Reservation path, a new real-server test in
`jobqueue/run_state_reset_test.go`:

```
unset $(compgen -v | grep '^OS_')
CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue \
  -run TestReservedRetryShowsNothingOfThePreviousRun
```

Output:

```
  * .../jobqueue/run_state_reset_test.go
  Line 150:
  Expected '172.27.71.182' to be blank (but it wasn't)!

23 total assertions

--- FAIL: TestReservedRetryShowsNothingOfThePreviousRun (0.28s)
FAIL
```

GoConvey stops at the first failure, so a one-off probe logged every field the
reserved retry reported:

```
reserve-response HostIP="172.27.71.182" FailReason="command exited non-zero"
CPUtime=1s; GetByEssence State="reserved" HostIP="172.27.71.182"
FailReason="command exited non-zero" StdOut="RUNSTATERESETPREVIOUSSTDOUT"
StdErr="RUNSTATERESETPREVIOUSSTDERR"; live State="delayed"
```

`CPUtime` leaks too, which the diagnosis above did not list.

Web-rerun path, the existing "can rerun completed jobs" case of
`TestServerWebI`, extended to assert every run field:

```
CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run 'TestServerWebI$'
```

Output:

```
  * .../jobqueue/serverWebI_test.go
  Line 1389:
  Expected: 0
  Actual:   3534196
  (Should equal)!

1470 total assertions

--- FAIL: TestServerWebI (8.77s)
```

That is `RunnerPid`, which the web-rerun path never cleared. Every other field
that test asserts was already cleared.

### Field audit

Every field of `Job` (`jobqueue/job.go`), sorted by whether it describes one
run.

Run-describing, cleared by the shared `(*Job).resetRunLocked(state JobState,
exitcode int)`:

| Field | Written by the run at | Why a new run must not inherit it |
|---|---|---|
| `State` | Started, each exit | Argument: `JobStateReserved` at reserve, `JobStateReady` at web rerun. See the decision below. |
| `Exited` | exit | The TTR callback and the lost-job retry check ask it whether the run is over. |
| `Exitcode` | exit | Argument: -1 at reserve (as before, "has not exited"), 0 at web rerun (as before, what a newly added job reads). |
| `FailReason` | release, bury, lost | `wr status` and the web UI showed "command exited non-zero" or "lost contact with runner" on a fresh retry. |
| `Lost` | TTR expiry | Gates every lost-run decision; `ttrCallback` never re-marks a Lost job. |
| `killCalled` | `wr kill` | Would make the new run kill itself on its first touch. |
| `runID` | reserve (minted) | The run's identity; zeroed, then minted afresh by the reservation. A web rerun job is decoded from the database and has none anyway. |
| `Pid` | reserve (runner), Started (command) | Cleared, then set to the reserving runner's pid under the same lock. |
| `RunnerPid` | Started | Kept a dead or reused runner pid alive in `jobConfirmedDead` (#555). The web-rerun path was missing it. |
| `Host` | reserve, Started | Cleared, then set to the reserving runner's host under the same lock. |
| `HostID` | Started | `killJobsOnBadServers` matches it against condemned servers. |
| `HostIP` | Started | Paired the new runner's host with the previous run's IP. The reservation path was missing it. |
| `ActualCwd` | Started, touch, exit | What cleanup deletes and what a `run` behaviour executes in. |
| `StartTime`, `EndTime` | Started, exit | Walltime, lost-for backstop, `canCompleteFromEndState`. |
| `PeakRAM`, `PeakDisk` | touch, exit | Requirement bumps after a RAM or disk failure read them. |
| `CPUtime` | touch, exit | Shown in `wr status` and the web UI. The reservation path was missing it; this was not in the diagnosis. |
| `StdOutC`, `StdErrC` | touch (live tail) | `itemToJob` hands the in-memory copy out for a reserved, running or lost job, so the previous run's live tail showed on the retry. The reservation path was missing both. |

Not run-describing, left alone by the shared function:

- The job's definition: `Cmd`, `Cwd`, `CwdMatters`, `ChangeHome`,
  `RepGroup`, `ReqGroup`, `Group`, `Requirements`, `RequirementsOrig`,
  `Override`, `Priority`, `Retries`, `NoRetriesOverWalltime`, `LimitGroups`,
  `LimitGroupsForDisplay`, `Modules`, `DepGroups`, `Dependencies`,
  `WaitingForDepGroups`, `Behaviours`, `MountConfigs`, `BsubMode`,
  `MonitorDocker`, `WithDocker`, `WithSingularity`, `ContainerMounts`,
  `ContainerImageUser`, `EnvKey`, `EnvOverride`, `Queue`, `BsubID`.
- Counts across runs: `Attempts`, `UntilBuried`. The web rerun resets them in
  `resetJobStatusFields`, because a rerun starts the job's life again; a
  reservation must not, or retries would never run out.
- Set by the reservation itself: `ReservedBy` (in `resetJobForReservation`),
  `DelayTime` (after it, from `setItemDelay`) and `incrementedLimitGroups`
  (by `noteReserveLimitGroups`, BEFORE the reset). Clearing any of them in
  the shared function would wipe what the reservation had just recorded, and
  for `incrementedLimitGroups` would leak a limit-group slot. The web rerun
  still clears all three in `resetJobStatusFields`.
- Per-request or cached: `EnvC`, `EnvCRetrieved`, `Similar` (web rerun clears
  these in `resetJobStatusFields`), `schedulerGroup`, `derived`,
  `derivations`, `mountedFS` (client side only).

### Ordering at reserve

`noteReserveLimitGroups` runs inside `reserveWithLimits`, before the reset, and
is untouched by it. `resetJobForReservation` now takes the whole
`*clientRequest`: under one lock it clears the run, then records `ReservedBy`,
the reserving runner's `Host` and `Pid`, and a freshly minted run token.
`respondWithReservedJob` used to set `Host`/`Pid` under a second lock after
the reset, which is why the old reset had to skip them; doing it under the same
lock means nothing can observe the job holding neither the previous run's
host+pid nor this runner's.

### The `State` decision

`JobStateReserved`, because that is what `itemStateToJobState` already reports
for a run-queue item with no `StartTime`, so the manager's own `Job.State` now
agrees with what every client was already shown. `applyJobStart` then
overwrites it with `JobStateRunning`, and each exit with its own state.

Evidence that nothing breaks:

- **Count deltas.** No count contribution uses `job.State` as the "from":
  `changeCallbackCounts` takes it from the source sub-queue, `markJobLost` and
  the touch recovery hard-code theirs. The only `job.State` read is the "to"
  of a removal, and both removal paths set it first (`markJobComplete` to
  complete, `removeDeletableJobs` and the REST delete to deleted). The new test
  records the status page's `jstateCount` feed across ready, running, delayed,
  ready, reserved, and asserts the deltas net out to the seed a newly loaded
  page would get, per RepGroup and for `+all+`. It nets them rather than
  applying them in arrival order with clamping: a first version did the
  latter and failed about one run in thirty with a phantom `ready:1` in
  `+all+`, because the async change callbacks can deliver delayed->ready and
  ready->running in either order. That is the known out-of-order case the
  browser's occupancy reconciliation exists for, and it is not affected by this
  change.
- **Web UI.** Every status payload is built from a client copy whose `State`
  comes from the queue item (`itemToJob`, `statusSeedCounts`), or is
  overwritten with the transition's state (`statusFromSubscriptionUpdate`,
  `jobUpdateFromStatus`). `websocket-handler.js` reconciles counts only on
  `FromState`/`ToState` of `jstateCount` messages, and
  `mergeJobDetailsPushUpdate` only branches on the push update's own `State`,
  which is already `reserved` or `running` for a reserved job. No JS reads the
  manager's `Job.State`, so the reconcile harness, which drives the JS with
  delta streams, sees no different input.
- **Duplicate-start guard.** `applyJobStart` treats a Started as a duplicate
  only when `State == JobStateRunning` with the same pid and host. With the
  old stale `State` the first Started of a new run could in principle be taken
  for a duplicate of the previous run; with `Reserved` it never can, while a
  real duplicate within one run still sees `Running`.
- **Release idempotence.** `applyReleaseQueueChange` compares `State` with
  delayed/buried only when the item is already in delay or ready, which a
  reserved job never is.
- **Recovery.** Nothing persists a job between reserve and Started:
  `handleStart` writes `Running` durably, release and bury write their own
  state first, archive writes complete, and modify, suspend and kick refuse or
  skip run-queue items. Even if a `Reserved` job were persisted,
  `recoveredItemDef` sends every state but running, buried and suspended to the
  ready queue, which is where a reserved-but-unstarted job belongs.
- **`FailReason` and `Lost` after reserve.** `ttrCallback` and
  `lostJobRetryCheck` read `Exited` and `Lost`, which reserve already cleared,
  and `ttrCallback` sets `FailReason` rather than reading it. The requirement
  bumps (`failureMayUpdateJobRequirements`) read `FailReason` only in
  `prepareReadyJob`, on a ready job, which release has given a fresh one. After
  a reserve their other inputs (`PeakRAM`, `PeakDisk`, `StartTime`, `EndTime`)
  were already zeroed, so a stale `FailReason` could only have caused a
  spurious bump (a RAM failure with `PeakRAM` 0 still bumps to 1GB) if the job
  went back to ready without a release.

The delta-feed half of the new test was also run against the pre-fix code
(production files checked out from `HEAD`, the leak assertions removed) and
passed there too: the counts are the same before and after the change.

Two existing tests pinned the old leaks and were updated:

- `TestJobqueueSignal` asserted that the reserve response of a kicked job
  still carried `FailReasonTime` from the run before. It now asserts a blank
  reason; the raised time requirement it also checks, which is what that
  failure is for, is unchanged. No runner code reads a reserved job's
  `FailReason`.
- `TestLostJobRetryCheckFindsAReservedNotStartedRun` pinned the old stale
  value (`JobStateDelayed` through a whole reservation); it now asserts
  `JobStateReserved`, and the matching comment in `lostJobRetryCheck` no
  longer describes `State` as never written at reserve.

### Green commands (after the fix)

```
CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue \
  -run TestReservedRetryShowsNothingOfThePreviousRun
ok  	github.com/VertebrateResequencing/wr/jobqueue	0.297s

CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run 'TestServerWebI$'
ok
```

The reservation test then passed 40 runs in a row.

### Gates

- `make lint`: `0 issues.`
- `make test`: 682 passed, 20 skipped, 29 packages, 6m17s, PASSED.
- `CGO_ENABLED=1 make race` (load about 1, no other suite running): 682
  passed, 19 skipped, 29 packages, 9m33s, PASSED.
- No JS or HTML changed. `make browser-test` passed anyway, and
  `reconcile-harness.mjs` on the shipped `websocket-handler.js` reported
  "ALL SCENARIOS COHERENT AND CONVERGENT".
- `cleanorder -min-diff` was kept on `jobqueue/job.go` and
  `jobqueue/run_state_reset_test.go`; it made no change to `serverCLI.go`,
  `serverWebI.go`, `server.go` or `serverWebI_test.go`, and was reverted on
  `lost_job_behaviours_test.go`, where it moved an unrelated constant.
