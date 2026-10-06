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
- Kick ordering dependency: done after the rebase onto 72384e4c.
  `TestKickQueuedBeforeReservable` reserves the kicked job from
  `kickQueuedHook`, crash-images, and requires recovery to give the job
  reserved with the kicked `UntilBuried`. It fails (`UntilBuried` 0, want 4)
  with the write queued after that hook. With the write queued after
  `KickWith` returns but before the hook, every test passes: no seam exists
  between `KickWith` returning and that write.
- Follow-up from item 4.1's review (non-blocking): reserveNotDurableLines in
  reserve_durability_test.go could also require `lvl=info`, so a line logged
  at warn level would fail the test.
- Follow-ups from item 5.1's review (non-blocking): dbstart drops a job's
  line if a CRC-matching run-state body fails to decode (jobqueue falls back
  to the live record; dbstart could too); `dbstart -schema` with no path
  prints an open error (exit 1) rather than usage (exit 2).
- Follow-up from item 5.2's review (non-blocking): statinspect prints a
  doubled prefix for a missing jobslive bucket ("clearlive err: clearlive: no
  jobslive bucket"); drop the inner wrap.
- Follow-up from item 5.3's review (non-blocking): CHANGELOG "naming both
  versions" and the compact help's "newer version of wr" mean schema
  versions; consider "naming both schema versions".
- Follow-ups from item 5.4's review (non-blocking): add a d1 case with only
  warning lines (expect inside=0 outside=0) to pin "no fallback to
  warnings"; consider printing a failing outsidePct when runs=0.

## Fixtures (item 5.5)

`G=/nfs/hgi/wr/sb10-bigdb/runstate-gate`. Every `wrdev.sh` run below had all
`OS_*` unset, `GOCACHE=/tmp/claude-11346/gocache-runstate-gate`, `nice -n 19`,
ports checked free with `ss -ltn`, and `wrdev.sh build` first. Fixture builds
used `WRDEV_ROOT=$G/fixbuild/root DEV_PORT=51970 DEV_WEB=51971 PROD_PORT=51972
PROD_WEB=51973`; E4 tests 1 and 2 used `WRDEV_ROOT=$G/e4test/root` with ports
51974 to 51977. `dbstart` is `go build ./developers/soak/dbstart` at `$G/dbstart`.

Fixtures, built 2026-10-06 from this tree (`generator 2a0e4f9e` plus the
uncommitted phase 5 changes):

| Fixture | Command | Source size | Size | `dbstart -schema` |
| --- | --- | --- | --- | --- |
| `$G/fixtures/pristine6` | `compact-fixture /nfs/hgi/wr/sb10-bigdb/pristine6 $G/fixtures/pristine6` (99s) | 7,392,931,840 | 3,046,572,032 | `schemaVersion=2` |
| `$G/fixtures/pristine10` | `compact-fixture /nfs/hgi/wr/sb10-bigdb/pristine10 $G/fixtures/pristine10` (135s) | 10,729,893,888 | 4,337,278,976 | `schemaVersion=2` |
| `$G/fixtures/prod.db` | `compact-fixture /nfs/hgi/wr/sb10-bigdb/prod.db $G/fixtures/prod.db` (123s; "removed output stored by older wr versions from 75811 completed jobs") | 7,925,501,952 | 5,111,070,720 | `schemaVersion=2` |
| `$G/fixtures/fix120k.db` | `WRDEV_PRISTINE_DB=$G/fixtures/pristine6 WRDEV_ASL_FIXTURE=$G/fixtures/fix120k.db wrdev.sh add-storm-fixture 120000 5 200` | (base pristine6 above) | 3,352,190,976 | `schemaVersion=2` |

The originals' size and mtime were the same before and after. The
`fix120k.db.aslmanifest`: `aslfixture 2`, `size 3352190976`,
`mtime 1791324308`, `incomplete 120000`, `prefix echo aslfix`,
`blockgroup aslblock`, `repgroup rgaslfix`, `kidrepgroup rgaslfixkid`,
`jobcwd $G/fixtures/fix120k.db.jobcwd`, `base $G/fixtures/pristine6`,
`jobs 120000`, `selfaddpct 5`, `depgroups 200`. Its generator printed
`command audit: 120000 incomplete commands read, 0 of them NOT a form this
generator writes`. Compaction drops each original's freelist, so figures
measured on these copies are not like for like with battery10's.

E4 acceptance tests:

1. `cp jobqueue/testdata/dbcompat/db.golden $G/e4test/src.db`, then
   `wrdev.sh compact-fixture $G/e4test/src.db $G/e4test/dst.db` exited 0;
   `src.db`'s SHA-256 stayed
   `cff1df83216f0ec63fdc080fec698d96bae19e1da7ea2e6a296a7783efd550b9`
   and `dbstart -schema dst.db` printed `schemaVersion=2` (`src.db`:
   `schemaVersion=0`). With `dst.db` copied to
   `$G/e4test/root/.wr-prod_production/db`, `wrdev.sh prod-start local`
   exited 0, `wr status --deployment production -i
   reliable2-dbcompat-complete -o counts` printed `complete: 2`, and
   `wrdev.sh prod-stop` printed `killed our manager pid 1883124`.
2. With `dst.db` present: `wrdev: REFUSING: $G/e4test/dst.db already
   exists`, exit 1. With `src.db.aslmanifest` present: `wrdev: REFUSING:
   $G/e4test/src.db.aslmanifest exists, ...`, exit 1. A before/after listing
   (names, sizes, mtimes, SHA-256) of `$G/e4test` was identical for both. Also
   checked: same file (`dst.db` vs `./dst.db`) refused, exit 1; with the dev
   manager up, `REFUSING: the dev manager (pid 1884740) is up`, exit 1; a
   non-bolt source failed compaction (`invalid database`), exit 1, and its
   `dst` was removed.
3. `WRDEV_PRISTINE_DB=$G/fixtures/fix120k.db wrdev.sh add-storm-lsf` (fixbuild
   root, defaults), run to completion, printed `fixture manifest OK: 120000
   incomplete jobs, safe prefix 'echo aslfix', blocked by aslblock,` and
   exited 0 with `## VERDICT: ... presentAfterRestart=4476/4476
   missingAcked=0 ... overSlow=0 ...` and `PASS: all 4476 acknowledged adds`;
   cleanup reported `wrpiso51972h838732172_* jobs left in LSF: 0`. F2.2 runs
   it again for the gate verdict.
- Follow-ups from item 5.5's review (non-blocking): compact-fixture writes
  its private config before the `wr conf` refusals; doesn't assert
  ManagerHost is localhost; has no trap for SIGINT/timeout cleanup of dst;
  the aslmanifest refusal doesn't follow symlinks; freelist-bound repro texts
  could point at TestReliable4InflateDB.
