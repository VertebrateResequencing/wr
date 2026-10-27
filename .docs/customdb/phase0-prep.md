# Phase 0 preparation: the `db` surface the interface must fix

Input for the Phase 0 spec, measured on develop d1a4b6d7. Phase 0 puts an
interface between the server and storage, so that Phase 1 can supply a log
implementation without touching the server beyond deleting workarounds.

## Size

The storage layer is 9,455 lines across `db.go` (6,684), `db_runstate.go`,
`dependency.go`, `archivefold.go`, `writeturns.go`, `reqstat_cache.go`,
`db_compact.go`, `db_prefetch.go` and `db_schema.go`. It defines 157 methods
on `*db`.

## Methods called from outside the storage files

43 methods plus the `initDB` constructor, grouped by what a store must answer.
Counts are call sites in non-test, non-storage files.

| Group | Methods |
| --- | --- |
| Lifecycle | `initDB` (function), `recoverIncompleteJobs`, `resolveDependencies`, `setBatchTuning`, `close`, `backup` |
| Add | `storeNewJobsGuarded`, `storeEnv` (2), `storeLimitGroups` (2), `storeRepGroupLookups`, `checkIfLive` (2), `checkIfComplete` |
| Transitions | `updateJobRunStateDurable`, `updateJobRunStateDurableWithin`, `logReserveNotDurable`, `updateJobAfterChangeDurable` (2), `updateJobAfterExit`, `updateJobAfterExitDurable`, `expectJobExitUpdate`, `archiveCompletion` |
| Change-ahead plumbing (#692) | `prepareJobChange` (3), `queueJobChangeAhead` (2), `moveJobChangeAhead`, `discardJobChangeAhead` (3) |
| Operator writes | `deleteLiveJobs`, `modifyLiveJobs` (3) |
| Dependency re-runs | `storeLiveForRerun` (2), `storeRunningRerunMarks` (2) |
| History queries | `retrieveCompleteJobsByKeys` (6), `retrieveCompleteJobsByRepGroup` (2), `retrieveOldestCompleteJobsByRepGroup`, `retrieveCompleteJobStatusByRepGroup` (2), `retrieveCompleteJobsRecent`, `retrieveLastCompletionTimeByRepGroup`, `repGroupHasHistory`, `retrieveRepGroups` (2), `spendArchivedBytesByRepGroup`, `retrieveJobStd` |
| Lookups | `retrieveEnv`, `retrieveStoredLimits` |
| Learned requirements | `cachedReqGroupRecs`, `refreshReqGroupRecs`, `refreshReqGroupRecsLater`, `warmReqGroupRecs` (4) |

## Leaks the interface has to close

These are places where the server depends on how bbolt is driven, not on what
is stored. Each needs a decision in the Phase 0 spec: lift it into the
interface as a storage-neutral operation, or keep it inside the bbolt
implementation and delete it in Phase 1.

- **Change-ahead plumbing.** `prepareJobChange`, `queueJobChangeAhead`,
  `moveJobChangeAhead` and `discardJobChangeAhead` exist so that a live record
  is encoded before the server takes its lock, which matters because a bbolt
  write rewrites the whole record. Under a log a change is a small state
  record, so the interface should offer "record this change" and let the
  bbolt implementation do its encoding ahead of time internally.
- **`updateJobRunStateDurableWithin` and `logReserveNotDurable`.** They expose
  ReserveWriteWait's "handed out before recorded" escape hatch to the server.
  The interface could return a durability wait the server bounds, as
  `gc.WaitWithin` does in the prototype.
- **`expectJobExitUpdate`.** This orders an exit op against a full change in
  the best-effort writer. In a single log it becomes "apply in log order".
- **`s.db.reqStats.changes`** (`server.go:2908`, `:2915`) and
  `db.reqStats.interval` (`server.go:5543`). The ready hook reads the cache's
  change stamp directly. It needs an accessor on the interface.
- **`s.db.rerunGuards`** (`server.go:7164`). A rerun guard's `keeps` takes a
  `*bolt.Bucket`, because it decides inside the archive transaction whether a
  re-added dependent stays live. The interface needs an ordering promise
  instead: "an archive applied after a rerun guard registered sees it".
- **Writer turns, archive folding, best-effort batching, backup pacing,
  prefetch and freelist handling.** These are internal to the bbolt
  implementation already, and are deleted when bbolt goes.

## Tests

46 test files call `db.bolt.View`, `Update` or `Batch`, or take a `*bolt.Tx`
directly, 237 times in total. Test-only fields such as `db.bak`,
`db.archivedDecodes`, `db.backupMu`, `db.envcache` and `db.wg` are also read
directly. Phase 0 should add a small inspection interface for tests (read a
live record, read a completed record, count records by kind, force a backup)
and move these tests onto it, so that they run against both implementations.
Tests that check bbolt mechanics (freelist, remap, backup pacing, writer
turns) stay bbolt-only and are deleted with it.

## Tools that read the database file

| Tool | Reads | Phase 1/2 need |
| --- | --- | --- |
| `wr manager compact` (`cmd/manager.go`, `jobqueue/db_compact.go`) | bbolt | A log equivalent: write a new checkpoint and drop old segments |
| `developers/soak/dbstart` | bbolt | Soak gates rely on it; reimplement over the interface |
| `developers/soak/bboltexp` | bbolt | bbolt-only experiment tool; retire |
| `db_prefetch.go`, `db_schema.go` | bbolt | Retire with bbolt; the log needs its own version header |
| `.docs/customdb/proto/cmd/dbstats` | bbolt | Source for the converter and for fixtures |

## Record and replay harness

The cheapest way to show that two implementations give the same answers is to
record every call the server makes across the interface during a prodsim run
(method, arguments, result digest, and timing), then replay the recording
against each implementation and compare results call by call. The recording
point is the interface Phase 0 creates, so the harness costs little once it
exists. Queries whose answers depend on timing, such as "recent completions",
need their time argument recorded rather than read from the clock.

## The crash test to port

`proto/cmd/crashtest` runs 300 writers through reserve, start and archive,
appends each acknowledged (key, sequence) to an ack file, kills the child
with SIGKILL at a random moment, and requires every acknowledged sequence to
survive recovery. It then truncates and corrupts copies of the log and
requires recovery to stop at a good prefix. Ported to `jobqueue`, it should
drive the interface, not a store, so that it runs against bbolt today and the
log in Phase 1.
