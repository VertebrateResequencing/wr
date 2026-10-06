# Notes for the PR body

- Concurrent adds (item 2.3): If two adds of the same new job race and the
  second add's fresh copy is written after a reservation's run state is
  queued but before it is drained, the run state lands over that fresh copy,
  so until the job's next full write its stored non-run fields are the second
  add's; this window is no wider than today's stale full change, and a test
  (TestAddKeepsHandedOutRunState) pins the outcome.
- Compact on a 0-byte file (item 1.2): `wr manager compact` now fails on an
  empty file (the read-only version check cannot open it), where before it
  would have initialised and compacted it.
- Speed (item 2.2): every full live write now also deletes a run-state key;
  covered by phase 6's make speed / speed-full comparison.
- Recovery cleanup (item 3.2): a failed delete of stale/orphaned run-state
  records at startup is logged as a warning ("recovering: failed to drop
  stale job run-state records") and recovery continues; the spec did not
  cover this case. Leftover records are harmless (every reader re-checks the
  CRC) and the next recovery retries.
- runStateRecovery carries `undecodable` and `dropErr` beyond the spec's
  struct, so decodePriorJobs can log them.

## A2 audit

A reservation and a start now write only `jobRunState`, so every persisted
`Job` field changed in memory since the job's last full write must be in
`jobRunState`, have its own durable write, or be re-derived at recovery.

Changed in memory between the last full write and a reservation or start:

| Field | Where | How it is persisted |
| --- | --- | --- |
| `Requirements`, `RequirementsOrig` | `prepareReadyJob` -> `updateJobRequirementsForRetry` (`applyRecommendedJob*`, `increaseJobRAMAfterHighPeak`, `increaseJobDiskAfterFailure`, `increaseJobTimeAfterFailure`) | In `jobRunState` (A2 test 3) |
| `DelayTime` | `respondWithReservedJob` | In `jobRunState` |
| `State`, `Lost`, `EndTime`, `PeakRAM`, `CPUtime` and the other run fields | queue callbacks, `markJobLost`, touches (`recoverLostTouchedJob`, live status), reservation and start | In `jobRunState` |
| `WaitingForDepGroups` | `setWaitingForDepGroups` (add, `reflectModifiedJobInQueue`, `updateModifiedQueueJobs`, `updateLiveDependents`, `updateDependentUnlessRunning`, `rerunDependencies`) | Not stored by the reservation; recovery re-derives it for every job in `resolveDependencyChunk` (A2 test 7) |
| `UntilBuried` | `applyRelease`; `kickJobs` | The release's exit-op write; the kick's own full change, which A3 test 4 shows survives a reservation queued after it. `kickJobs` does not yet guarantee that order: `s.q.Kick` makes the item reservable before the kick sets `UntilBuried` under `job.Lock` and before it queues its full change. A reservation that locks the job between those steps queues its run state, which can commit before the kick's full change is queued; a crash then recovers the pre-kick `UntilBuried`, 0 for a buried job, so its next failure buries it again instead of retrying. Before this change the reservation's full write stored the kicked value. The same window already causes a double run on develop: a job reserved before the kick takes `job.Lock` has its State set back to ready, the kick's full change supersedes the reservation, and a crash before Started recovers it ready while its runner may run it. A separate kick-ordering fix, merged before this PR and required by it, closes both by setting the fields and queueing the kick's write before the job becomes reservable |
| `RerunAfterRun` | `markRerunAfterRun` (add), `takeRerunAfterRun` (release), `endArchive` (archive) | `storeRunningRerunMarks`, the release's write and the archive's writes respectively |
| `Behaviours` | `dropImpossibleCleanups` | At add before the add's encode, and on every `decodeJob`, so recovery re-applies it |

Set at add before the add's record is encoded, so in it: `EnvKey`,
`RerunAfterRun` and `RunnerReservation` (cleared), `ReadyTime` (cleared),
`UntilBuried`, `BsubID`, `LimitGroups` and `LimitGroupsForDisplay`
(`prepareInputJobs`, `handleUserSpecifiedJobLimitGroups`). Every
`JobModifier` setter (`Cmd`, `Cwd`, `ReqGroup`, `Group`, `Requirements`,
`Override`, `Priority`, `Retries`, `NoRetriesOverWalltime`, `LimitGroups`,
`DepGroups`, `Dependencies`, `Behaviours`, `MountConfigs`, the container
fields, `BsubID`) is written by the modify's full write
(`modifyLiveJobsTx`). `RunnerReservation` is cleared by
`forgetCompletedRunnerHold` only for the complete record.

Set only on copies sent to clients, never stored: `ReadyTime`
(`itemToJobIfAdmitted`), `EnvC` and `EnvCRetrieved` (`jobPopulateStdEnv`),
`Similar` (`addJobToGroup`, `countUndecodedJobs`), `State` and `UntilBuried`
in `refreshJobFromLiveItem`, `RepGroup` (`stampRepGroup`, the web status
handlers) and `resetJobStatusFields`. Nothing in the manager sets `Queue`.
Unexported fields (`schedulerGroup`, `runID`, `incrementedLimitGroups`, ...)
were never persisted.

Changed in memory by recovery:

| Field | Where | How it is persisted |
| --- | --- | --- |
| every `jobRunState` field | `recoverIncompleteJobs` overlay | Read from disk |
| `Behaviours` | `decodeJob` -> `dropImpossibleCleanups` | Re-applied on every decode |
| `WaitingForDepGroups` | `resolveDependencyChunk` | Re-derived at every recovery |
| `RerunAfterRun` cleared | `recoverRerunMark`, for a job recovered outside the run sub-queue | In `jobRunState`, so the next reservation stores it (A2 test 8) |
| `RerunAfterRun` set | `recoverRunningDependent` -> `markRecoveredRerun` | `storeRunningRerunMarks`, unless already stored |
| `schedulerGroup`, `incrementedLimitGroups` | `recoveredItemDef`, `recoverRunningJob` | Unexported, never persisted |

`recoverRunnerHold` reads the job and changes no field of it.

A2 test 1 checks this audit end to end for a reserved and started job: after
reserve and start its live record is byte-equal to the add-time record, and
recovery reads a job whose encoding equals the in-memory job's.
