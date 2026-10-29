# Phase 0 prodsim recording (item 0d.3)

The A5 gates (items 1g.2 in [phase1-gate.md](../phases/phase1-gate.md) and
2g.2 in [phase2-gate.md](../phases/phase2-gate.md)) replay this recording:

```text
/nfs/hgi/wr/sb10-bigdb/customdb/recordings/phase0-prodsim-20261028b.rec
size   1,188,370,355 bytes
sha256 a107a2f33cfa3c40a6e696307ff0d431ff05c5b8f341b5375750ef4cc3c060b2
```

Recorded on 2026-10-28 by a manager built from `44e8cd6a` (branch
`customdb-f397b547`, Phase 0 bbolt store) with `WR_STORE_RECORD` set, on
real LSF. It holds 400,863 calls (seq 1..801,726, in `End` order) of one
manager process, which started on an empty database and was killed with
`kill -9` by `wrdev.sh prodsim`'s cleanup, so the recording has no
`close`.

Replay it with:

```bash
(unset $(compgen -v | grep '^OS_'); HOME=$(mktemp -d -p <scratchpad>) \
  WR_STORE_REPLAY=/nfs/hgi/wr/sb10-bigdb/customdb/recordings/phase0-prodsim-20261028b.rec \
  WR_TEST_STORE=<kind> go test -count=1 -timeout 0 -run '^TestStoreReplay$' ./jobqueue/)
```

A replay against fresh bbolt takes about 95s on local disk, with 0
mismatches since the learned-requirements fix below.

## Run shape

- `developers/wrdev.sh prodsim 1.5 6 1`: 1.5h real, 1 simulated minute =
  6s, scale 1, every actor, `-portal-jobs 2000 -portal-every 4h`, no
  restarts, isolated prod-mode manager on ports 51970-51973, LSF queue
  `normal`. Root `/nfs/hgi/wr/sb10-bigdb/customdb/rec0d3/`; outputs in
  `prodsim-1793222855/` there (`report.txt`, `calls.tsv`, `markers/`,
  `lsf.tsv`, manager log); the manager's final DB is in
  `.wr-prod_production/db`.
- Beside it, `developers/soak/rundep.sh <out> 300 3600` (12 instances of a
  running job whose dep group gains a member) and a side driver
  (`scripts/side.sh` in the root) that every 5 minutes (13 instances) adds
  a limit-0 group, re-adds it under a second rep group, modifies,
  suspends, resumes and removes it; adds failing jobs, reads their
  details, retries and removes them; adds a group, re-adds it with
  `--rerun` while running and again once complete, and dumps it as json;
  kills and retries a running group; and runs `wr status --recent`. One
  `wr manager backup` ran, and 3 REST `PATCH`es of complete rep groups
  (409).
- Jobs: 27,258 prodsim job runs (portal 20,216 in 5 bursts of 2 x 2,000
  jobs with 8KB commands, added 1,000 per call; ibackup fofn 4,285; wrstat
  stat 2,454 and walk/combine/tidy; pipeline 170 through
  `SubmitJobsAndWait`), plus the side and rundep jobs. Peak LSF RUN 295.
- Manager peak RSS 638MB, DB 244MB at the end.

## Calls by method

| method | calls |
| --- | ---: |
| `retrieveEnv` | 109,894 |
| `refreshReqGroupRecs` | 65,490 |
| `cachedReqGroupRecs` | 61,918 |
| `depGroupsEverSeen` | 35,440 |
| `updateJobRunStateDurable` | 27,766 |
| `updateJobRunStateDurableWithin` | 27,766 |
| `archiveCompletion` | 27,201 |
| `reqStatChanges` | 21,680 |
| `retrieveCompleteJobStatusByRepGroup` | 4,408 |
| `retrieveOldestCompleteJobsByRepGroup` | 3,085 |
| `warmReqGroupRecs` | 2,449 |
| `storeRunningRerunMarks` | 2,410 |
| `retrieveCompleteJobsByRepGroup`, `spendArchivedBytesByRepGroup` | 1,264 each |
| `storeNewJobsGuarded`, `storeEnv`, `registerRerunGuard`, `releaseRerunGuard` | 1,205 each |
| `prepareJobChange`, `queueJobChangeAhead` | 603 each |
| `expectJobExitUpdate`, `expectJobExitUpdate.done`, `updateJobAfterExitDurable` | 465 each |
| `retrieveRepGroups` | 396 |
| `retrieveCompleteJobsByKeys` | 264 |
| `retrieveJobStd` | 188 |
| `retrieveLastCompletionTimeByRepGroup` | 180 |
| `refreshReqGroupRecsLater` | 159 |
| `deleteLiveJobs` | 96 |
| `storeLimitGroups` | 47 |
| `retrieveLimitGroup` | 30 |
| `modifyLiveJobs`, `retrieveCompleteJobsRecent` | 13 each |
| `retrieveStoredLimits` | 10 |
| `repGroupHasHistory` | 3 |
| `checkIfLive` | 2 |
| `backup`, `recoverIncompleteJobs`, `upgradedOnOpen`, `setBatchTuning`, `setRecRounding`, `setReqStatRefreshInterval` | 1 each |

Never called: `resolveDependencies` (only when a manager recovers prior
jobs, which a recording replayed onto a fresh store cannot start from),
`logReserveNotDurable` (every reservation was durable in time),
`storeRepGroupLookups` (the re-add under a second rep group did not reach
it), `storeLiveForRerun`, `checkIfComplete`, `updateJobAfterChangeDurable`,
`updateJobAfterExit`, `moveJobChangeAhead`, `discardJobChangeAhead` and
`depGroupEverSeen` (race and recovery paths of the server).

## Replay against fresh bbolt at `44e8cd6a`

`TestStoreReplay`: 400,863 calls, 0 skipped, 30,701 mismatches, so it
fails. Every mismatch is in one of three non-write learned-requirements
methods; every other method replays with none, including
`retrieveCompleteJobStatusByRepGroup` (no float-sum mismatches).

| method | mismatches (TestStoreReplay) | a second replay |
| --- | ---: | ---: |
| `cachedReqGroupRecs` | 19,480 of 61,918 | 16,266 |
| `refreshReqGroupRecs` | 3,288 of 65,490 | 3,119 |
| `reqStatChanges` | 7,933 of 21,680 | 7,933 |

The `cachedReqGroupRecs` and `refreshReqGroupRecs` counts change between
replays: they depend on when the async `refreshReqGroupRecsLater` loads
finish, as the 0d.2 entry in [incidentals.md](../phases/incidentals.md)
predicted. `reqStatChanges` mismatched 7,933 calls both times, starting at
the same seqs, so its cause is not that timing alone.

### Cause

All three read the learned-requirements cache, whose answers depend on
when its loads ran, which a recording does not hold. A load of a ReqGroup
starts at most once per refresh interval (1s, recorded by
`setReqStatRefreshInterval`). The recording spans 1.5h, so a manager's
`warmReqGroupRecs` and refreshes usually found the interval passed and
loaded; a replay covers the same calls in about 80s, so it found the
interval not passed and kept the older values. That made the replay's
change stamp end 3 below the recording's (18 against 21) on every replay,
and made it repeat the same `reqStatChanges` mismatches. The background
loads of `refreshReqGroupRecsLater` finish at times a replay cannot
reproduce either, which is why the other two counts varied. Every value the
recording answered was one bbolt's learned requirements held at that point
in the replay, in the same order, so this was replay timing, not a bbolt
difference.

## Replay against fresh bbolt with the learned-requirements fix

Replay now runs with no refresh interval and probes the learned values
before each call naming a ReqGroup and after each write, accepting a
recorded answer that is one of the probed values, or a change stamp no
higher than the replayed store's (spec A5). Two replays of the recording
each gave 400,863 calls, 0 skipped and 0 mismatches (about 95s each).
Calls compared, the rest overlapping a write:

| method | calls compared | mismatches |
| --- | ---: | ---: |
| `cachedReqGroupRecs` | 51,060 of 61,918 | 0 |
| `refreshReqGroupRecs` | 24,409 of 65,490 | 0 |
| `reqStatChanges` | 7,997 of 21,680 | 0 |
| every other method | | 0 |
