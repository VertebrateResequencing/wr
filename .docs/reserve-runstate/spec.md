# Reserve Run-State Records Specification

## Overview

At about 6,000 concurrent runners the manager hands out tens of thousands of
reservations before they are on disk (battery10: 23,586, 2.43% of 972k runs;
soak9 at about 3,900 runners: 0.034%), and a crash then runs those jobs twice.
Each reservation and start re-encodes the whole job (about 10KB for portal
commands) into the best-effort writer's transaction, whose commit time grows
with its size. The A3 model showed that writing a small run-state record
instead cut reserve p50 from 7.39s to 1.04s and best-effort transaction time
from 1.45s to 60ms at 5,000 runners, and raised runs/s from 126 to 164.

This change makes the durable write of a reservation and of a start a small
run-state record in its own bucket. Recovery overlays it onto the job's full
live record. Every other write keeps writing the full record and deletes the
run-state record in the same transaction, so a run-state record is always
newer than the full record beneath it. Existing durability and ordering
guarantees are unchanged: release-before-reservation and change-versus-exit
ordering by `beSeq`, the 260917 start durability and the 260928 reserve
durability. Requirements learned before a reservation stay durable at
reservation.

The database schema version becomes 2. The manager refuses a database newer
than it understands, and refuses a version-0 database until it has been
compacted. Each reservation handed out before it was durable is logged at
info with its key and a running total. Downgrading after this version has
opened a database is not supported.

## Architecture

All code is in package `jobqueue` unless stated. New file
`jobqueue/db_runstate.go` holds the record, its bucket, the live-record
helpers and the overlay. New tests go in `jobqueue/db_runstate_test.go` and
`jobqueue/db_schema_test.go` unless a story says otherwise.

### Run-state record

```go
//nolint:gochecknoglobals // bucket names are shared BoltDB keys.
var bucketJobRunState = []byte("jobRunState")

// jobRunState is every persisted Job field that a reservation
// (resetJobForReservation, respondWithReservedJob) or a start
// (applyJobStart, acceptDuplicateStartLocked) sets, plus the requirements
// prepareReadyJob may learn in memory before the reservation, and
// RerunAfterRun, which recovery may clear in memory only. Field names
// and types match Job's, with no omitempty, so every field, zero or not, is
// encoded.
type jobRunState struct {
	State             JobState
	Exited            bool
	Exitcode          int
	FailReason        string
	Lost              bool
	Pid               int
	RunnerPid         int
	Host              string
	HostID            string
	HostIP            string
	ActualCwd         string
	StartTime         time.Time
	EndTime           time.Time
	PeakRAM           int
	PeakDisk          int64
	CPUtime           time.Duration
	StdOutC           []byte
	StdErrC           []byte
	ReservedBy        uuid.UUID
	RunnerReservation uint64
	Attempts          uint32
	DelayTime         time.Duration
	Requirements      *scheduler.Requirements
	RequirementsOrig  *scheduler.Requirements
	RerunAfterRun     bool
}

// newJobRunState returns j's run state, with deep copies of Requirements
// and RequirementsOrig (their Other maps included). The caller holds j's
// read lock.
func newJobRunState(j *Job) jobRunState

// applyTo sets every field of r on j and invalidates j's derived state
// (invalidateDerivedLocked), since Requirements feed it. The caller holds
// j's lock, or j is not yet shared.
func (r *jobRunState) applyTo(j *Job)

// runStateRecord returns the bucketJobRunState value for encodedRunState
// (db.encode of a jobRunState) over the live record live.
func runStateRecord(live, encodedRunState []byte) []byte

// runStateOver returns the encoded jobRunState in record, and true, if
// record was written over exactly the live record live; otherwise nil and
// false.
func runStateOver(live, record []byte) ([]byte, bool)
```

- Key: the job's key, as in `bucketJobsLive`.
- Value: 4-byte big-endian CRC-32C (Castagnoli) of the job's
  `bucketJobsLive` value at the time of the put, then the `db.ch` (binc)
  encoding of `jobRunState`. binc encodes the struct as a map by field name.
- A record "matches" when its CRC equals the CRC-32C of the current live
  value. Only a matching record is applied. A record whose live record is
  absent is "orphaned"; one that does not match is "stale".
- Size: at most 1,024 bytes for the A1 example job, whatever its Cmd size.
- The bucket is created by the bucket-creation `Update` in `initDB` (A1).

### Live-record helpers

```go
// putLiveRecord puts encoded as key's live record and deletes key's
// run-state record, in tx.
func putLiveRecord(tx *bolt.Tx, key, encoded []byte) error

// deleteLiveRecord deletes key's live record and run-state record, in tx.
func deleteLiveRecord(tx *bolt.Tx, key []byte) error
```

Every `Put` or `Delete` of a `bucketJobsLive` key in the package goes through
these two helpers. A run-state record is put only by drain step 3 below and
deleted only by these helpers and the B2 purge. This covers `beBatch` full
changes and exit ops, `archiveJobTx` (`recordCompleteTx`,
`keepLiveForRerunTx`), `putRunningRerunMark`, `storeLiveForRerun`,
`putBackArchivedDependentsTx`, `putNewLiveJobs`, `modifyLiveJobsTx`
(`deleteOldLiveJobs` and the put of the new keys) and `deleteLiveJobs`. A
review check is that `grep -n 'bucketJobsLive' jobqueue/*.go | grep -v
_test.go` shows no other `Put` or `Delete` on that bucket. Test files may
write the bucket directly (for example `moved_on_runner_test.go`,
`jobqueue_test.go`) to build fixtures.

Existing test helpers that read or compose raw live records are changed to
carry or read through the run-state record (A2 test 6):

- `withLiveRecord` (`moved_on_runner_test.go`) copies the key's
  `jobRunState` value from `from` along with its live record, and deletes the
  base's `jobRunState` value for the key if `from` has none.
- `storedLiveJob` and `storedLiveJobState` (`reserve_durability_test.go`,
  also used by `runner_report_followups_test.go`) decode the live record and
  apply a matching run-state record.
- `recordOf`, `liveJobRecord` and `liveJobRecordInImage`
  (`readd_queued_test.go`) return the live record followed by the key's
  `jobRunState` value (empty if none), so their byte comparisons cover both.
- Helpers that read, change and write back a live record, such as
  `rdrSetStoredMark` (`running_dependent_rerun_test.go`) and
  `hideLiveSubscriptionJobInDB` (`subscription_test.go`), decode through the
  overlay, change the job, write the full record back and delete the key's
  run-state record in the same transaction, as `putLiveRecord` does.
- Any other test that reads `bucketJobsLive` values to check a job's state
  reads through the overlay the same way.

### Best-effort writer

`beChange` gains a run-state slot:

```go
type beChange struct {
	encoded  []byte // latest full live record queued, nil if none
	seq      uint64
	runState []byte // latest encoded jobRunState queued after encoded
	rsSeq    uint64
}
```

- `enqueueChangeLocked` (full) sets `encoded`/`seq` and clears
  `runState`/`rsSeq`.
- A new `enqueueRunStateLocked(key string, encoded []byte, waiter chan
  error)` sets `runState`/`rsSeq` from `db.beSeq++` and keeps `encoded`.
- `beBatch.apply` for each key K, with F = `seq` (0 if `encoded` is nil),
  R = `rsSeq` (0 if none) and X = K's last exit seq in the batch (0 if
  none):
  1. Full change: written with `putLiveRecord` if `encoded != nil`, F > X
     and K's live record exists.
  2. Exit ops, in order: std and fail-stat effects always. The live record
     is written with `putLiveRecord` if (F == 0 or exit seq > F) and the
     live record exists.
  3. Run state, last: if R > 0, R > X and K's live record exists, put
     `runStateRecord(<live value now in tx>, runState)`.

```go
// queueJobRunState encodes job's run state under db.RLock and job.RLock,
// held until it is queued, as queueJobChange does for a full record.
func (db *db) queueJobRunState(job *Job, waiter chan error) error

// updateJobRunStateDurable is updateJobAfterChangeDurable for the run state.
func (db *db) updateJobRunStateDurable(job *Job) error

// updateJobRunStateDurableWithin is updateJobAfterChangeDurableWithin for
// the run state.
func (db *db) updateJobRunStateDurableWithin(job *Job, wait time.Duration) error
```

### Schema version (`jobqueue/db_schema.go`)

```go
const (
	dbSchemaVersionNoCompleteStd uint64 = 1
	// dbSchemaVersionRunState means the database may hold bucketJobRunState
	// records, which a manager that does not overlay them would ignore.
	dbSchemaVersionRunState uint64 = 2
	currentDBSchemaVersion = dbSchemaVersionRunState
)

var (
	errDBSchemaTooNew = errors.New("database schema version is newer than this wr supports")
	errDBNeedsCompact = errors.New("database must be compacted before this wr can use it")
)

// checkDBSchemaVersion returns errDBSchemaTooNew, wrapped as
// "%w: %s has schema version %d, this wr supports up to %d; use a newer wr"
// with dbFile, version and currentDBSchemaVersion, if version is newer than
// currentDBSchemaVersion; otherwise nil.
func checkDBSchemaVersion(dbFile string, version uint64) error

// readOnlyDBFileSchemaVersion opens path read-only (bolt ReadOnly, so the
// open writes nothing, even to a file whose freelist was never synced) with
// Timeout offlineDBOpenTimeout, so it fails rather than blocks on a file a
// running manager holds, returns its schema version and closes it.
func readOnlyDBFileSchemaVersion(path string) (uint64, error)
```

The version-0 error wraps `errDBNeedsCompact` with `dbFile` as:

```text
<errDBNeedsCompact>: <dbFile> was created by wr 0.37.2 or earlier and has
never been compacted; with the manager stopped, run `wr manager compact`,
then start the manager again
```

(one line; wrapped here for width).

## A: Run-state records

### A1: Record encoding and overlay

As the manager, I want a reservation's or start's state in a few hundred
bytes, so that writing it costs a fraction of re-encoding a 10KB job.

`initDB`'s bucket-creation `Update` creates `bucketJobRunState`.

**Package:** `jobqueue/`
**File:** `jobqueue/db_runstate.go`, `jobqueue/db.go`
**Test file:** `jobqueue/db_runstate_test.go`

Example job: Cmd of 10,000 `x`, State running, Host
`node-1-2-3.internal.sanger.ac.uk`, HostIP `172.27.71.182`, HostID a 36-char
UUID string, ActualCwd 100 bytes, Pid 7, RunnerPid 6, Attempts 1,
RunnerReservation 5, ReservedBy a fixed UUID, StartTime set, std nil,
Requirements `{RAM: 2000, Time: 2h, Cores: 1, Disk: 10, DiskSet: true}`,
RequirementsOrig `{RAM: 1000, Time: 1h, Disk: 10, DiskSet: true}`.

Older record: a full encoding of the same Cmd whose value differs from the
example job's in every `jobRunState` field (State reserved, Exited true,
Exitcode 3, FailReason `ram`, Lost true, Pid 1, RunnerPid 2, Host `old`,
HostID `oldid`, HostIP `10.0.0.1`, ActualCwd `/old`, StartTime and EndTime a
day earlier, PeakRAM 900, PeakDisk 9, CPUtime 1s, StdOutC `o`, StdErrC
`old`, ReservedBy another UUID, RunnerReservation 4, Attempts 0, DelayTime
5s, Requirements RAM 100, RequirementsOrig nil).

**Acceptance tests:**

1. Given the example job, when `runStateRecord(live, db.encode(newJobRunState
   (job)))` is built over its full encoding `live`, then the record is at most
   1,024 bytes and `len(live)` is at least 10,000.
2. Given that record, when `runStateOver(live, record)` is called, then it
   returns true, and decoding its bytes into a `jobRunState` and calling
   `applyTo` on a Job decoded from the older record gives a Job whose
   `db.encode` equals `db.encode` of the example job.
3. Given that record, when `runStateOver` is called with `live` changed in
   one byte, then it returns nil, false.
4. Given a record shorter than 4 bytes, when `runStateOver` is called, then
   it returns nil, false.
5. Given a `jobRunState` with every field zero, when it is applied to the
   example job, then every one of the job's `jobRunState` fields is its zero
   value, compared field by field by reflection: Lost is false, Requirements
   and RequirementsOrig are nil.
6. Reflection: for every field of `jobRunState`, `Job` has an exported field
   of the same name and type. Given a Job with every such field set to a
   distinct non-zero value by reflection, when `newJobRunState` of it is
   applied to a zero Job, then each field is equal (`reflect.DeepEqual`), and
   changing the source Job's `Requirements.RAM` and adding a key to its
   `Requirements.Other` map afterwards changes neither in the copy.

### A2: Reserve and start write run-state records

As the manager, I want `persistReservation` and `handleStart` to queue the
run-state record instead of the full job, keeping their existing waits.

`persistReservation` calls `updateJobRunStateDurableWithin(job,
ReserveWriteWait)`. `handleStart` calls `updateJobRunStateDurable(job)`,
including for an accepted duplicate start. Error handling, warnings and
replies are unchanged, except for D1's info line.

**Audit.** Before this phase is done, the implementor lists in the PR body
every persisted `Job` field that any path changes in memory without its own
durable write, between a job's last full write and its reservation or start,
and so relied on the reservation's or start's full write. Each is added to
`jobRunState` or given its own write. Known: `Requirements` and
`RequirementsOrig` (`prepareReadyJob` -> `updateJobRequirementsForRetry`,
`jobqueue/server.go`), added above. `DelayTime` is set in
`respondWithReservedJob`, so it is included. Candidate: `WaitingForDepGroups`
(`setWaitingForDepGroups`, called from `dependency.go`,
`running_dependent.go`, `server.go` and `serverCLI.go`); a job is reservable
only once its dependencies resolve, and recovery re-derives the field for
every recovered job through `dependency.go`'s `setWaitingForDepGroups` call.
The audit confirms that re-derivation with a test (A2 test 7); if recovery
does not re-derive it, it is added to `jobRunState`. Unexported fields
(`schedulerGroup`, `runID`, ...) were never persisted.

The audit also covers in-memory-only changes made at recovery, which today
the next reservation's full write stores. Known: `RerunAfterRun`.
`recoverRerunMark` (`running_dependent.go`) clears it in memory only when a
job recovers outside the run sub-queue (lost, or reserved with Pid 0; see
`recoversIntoRun`). Without the full write the stored `true` would survive,
and a later crash while the job runs would recover it running with a stale
mark, so `recoverRunningDependent` would return early and the job would run
again. It is added to `jobRunState`, so the reservation stores the cleared
value (A2 test 8). The audit lists every other field `recover*` functions
change in memory, and how each is persisted.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/db.go`
**Test file:** `jobqueue/db_runstate_test.go`

**Acceptance tests:**

1. Given a running server with a job whose Cmd is 10,000 bytes, added and
   its add write committed, and whose live record bytes L were read then,
   when a client reserves it and `Started` returns, then the job's live
   record is still byte-equal to L, a run-state record exists for its key,
   and `server.db.recoverIncompleteJobs()` returns the job with `db.encode`
   equal to `server.db.encodeJob` of the in-memory job.
2. Given the same setup, when the job is reserved but `Started` is not sent,
   then its recovered job has State reserved, ReservedBy the client's ID,
   Host and Pid the client's, and RunnerReservation the in-memory value.
3. Learned requirements: given a server where 10 jobs of ReqGroup `lr`
   requesting 100MB completed with PeakRAM 1500, so that the ReqGroup's
   recommendation is above 100MB, when an 11th `lr` job requesting 100MB
   (no override) is added, reserved, a crash image taken with
   `server.BackupDB` as `reserveDurabilityCrashAndRecover` does, and a new
   server started on that image, then the recovered job's
   `Requirements.RAM` equals the in-memory job's at reservation (above 100)
   and its `RequirementsOrig.RAM` is 100.
4. `TestReserveDurability`, `TestReserveDurabilityDeadRunner`,
   `TestReserveDurabilityStalledWrite`, `TestStartDurability` and
   `TestStartDurabilityAbortedWriteIsNotCommitted` pass unchanged.
5. Given two new benchmarks in `jobqueue/db_bench_test.go`, both
   `BenchmarkUpdateJobState`'s shape on jobs with 10,000-byte Cmds,
   alternating reserved and running: `BenchmarkUpdateJobRunState10KB`
   through `queueJobRunState` and `BenchmarkUpdateJobFull10KB` through
   `updateJobAfterChange`; when both run with `make bench
   BENCH='UpdateJob(RunState|Full)10KB'`, then the run-state benchmark's
   `bolt_pages/job` is at most half the full one's.
   `BenchmarkUpdateJobState` itself is unchanged (it is in `speed.sh`'s
   compared set).
6. The tests that use the helpers listed under "Live-record helpers"
   (`moved_on_runner_test.go`, `runner_report_followups_test.go`,
   `readd_queued_test.go`, `reserve_durability_test.go`) pass after the
   switch.
7. Given a job added with a dependency on dep group `g` that no job has yet
   (so `WaitingForDepGroups` is `[g]` in its add-time record), then a job in
   `g` added and completed, and the first job reserved, when a crash image
   is taken and a new server started on it, then the recovered job's
   `WaitingForDepGroups` is empty, as it is in memory.
8. Given a crash image in which a job is lost with a stored
   `RerunAfterRun` true (its run marked to rerun, then its runner gone),
   when a server recovers it (clearing the mark in memory), a runner
   reserves and starts it, a second crash image is taken and a new server
   recovers that, then the recovered job is running with `RerunAfterRun`
   false, its runner's reports are accepted, and its command's run marker
   shows exactly one run after the first recovery.

### A3: Drain ordering

As the manager, I want a drain to leave the same recovered state as when
every write was a full record, so that release-before-reservation and
change-versus-exit ordering still hold.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/reserve_durability_test.go`

Extend `TestBestEffortDrainKeepsArrivalOrder`: "reservation" queues via
`queueJobRunState` (state reserved, Exitcode -1); "release" queues an exit
op (state delayed, Exitcode 1, stderr `failed run's stderr`); "kick" queues
a full change (state ready, UntilBuried 3); "start" queues via
`queueJobRunState` (state running, Pid 7). Each case is one drain over one
seeded live job. "Recovered" means the job from `recoverIncompleteJobs`.
`storedLiveJobState` reads through the overlay. Tests 1-9 queue run states
with a new test helper `queueUnkickedBestEffortRunState`, which calls
`enqueueRunStateLocked` under the same locks without kicking the writer, as
`queueUnkickedBestEffortChange` and `queueUnkickedBestEffortExit` do, so
every op lands in the one `drainBestEffort` the test calls.

**Acceptance tests:**

1. release, reservation: recovered State reserved and Exitcode -1, run-state
   record present, stored stderr `failed run's stderr`.
2. reservation, release: recovered State delayed, no run-state record.
3. release, reservation, release: recovered State delayed, no run-state
   record.
4. kick, reservation: live record is the kick's encoding, recovered State
   reserved and UntilBuried 3.
5. reservation, kick: recovered State ready, no run-state record.
6. reservation, start: one run-state record, recovered State running and
   Pid 7.
7. Given a committed run-state record from an earlier drain, when a drain
   holds a full change (suspend) for the job, then no run-state record
   remains and recovered State is suspended.
8. Given a committed run-state record, when a drain holds a release exit op,
   then no run-state record remains and recovered State is delayed.
9. Given the job's live record deleted (as by an archive) before the drain,
   when a drain holds a reservation, then neither a live nor a run-state
   record exists for the key.
10. `TestBestEffortChangeKeepsEncodeOrder`, repeated with the reservation
    queued by `queueJobRunState`, passes: the reservation is what recovery
    sees.
11. `TestStartDurabilityAbortedWriteIsNotCommitted`, extended with a
    run-state waiter on the rolled-back drain, sees that waiter get
    `errBestEffortWriteAborted`.

### A4: Full writes and deletes supersede the run-state record

As the manager, I want every full live-record write and every removal to drop
the job's run-state record in the same transaction, so that no stale record
is left behind.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`, `jobqueue/db_runstate.go`
**Test file:** `jobqueue/db_runstate_test.go`

Each test starts from a live job with a committed matching run-state record
(State running).

**Acceptance tests:**

1. When the job is archived (`archiveCompletion`, not kept live), then its
   complete record exists and neither a live nor a run-state record does.
2. When it is archived with `RerunAfterRun` set so `archiveJobTx` keeps it
   live, then the live record is `rerunRecords`' live record (ReservedBy
   zero) and no run-state record exists.
3. When `deleteLiveJobs` removes it, then no run-state record exists.
4. When `modifyLiveJobs` replaces it under a new key, then neither the old
   nor the new key has a run-state record.
5. When `storeRunningRerunMarks` writes its mark, then the live record has
   RerunAfterRun true and no run-state record exists.
6. When `storeLiveForRerun` stores it, then no run-state record exists.
7. Given the job archived but its run-state record re-inserted directly (an
   orphan), when an add's dependent put-back (`putBackArchivedDependentsTx`)
   restores it, then no run-state record exists.

### A5: The add path keeps a handed-out job's records

As the manager, I want an add racing a reservation to leave both records
alone, as it does for a full reserved record today.

`putNewLiveJobs` treats an existing live record as handed out if a matching
run-state record exists, or if `liveRecordHandedOut` says so. Otherwise it
writes with `putLiveRecord`.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/db_runstate_test.go`

**Acceptance tests:**

1. Given a live add-time record (State ready, Attempts 0) and a matching
   run-state record (State reserved), when `putNewLiveJobs` stores a fresh
   copy of the job, then both records are byte-equal to before.
2. Given a live add-time record and a stale run-state record, when
   `putNewLiveJobs` stores a fresh copy, then the live record is the fresh
   copy and no run-state record exists.
3. Given no live record and an orphaned run-state record, when
   `putNewLiveJobs` stores the job, then the live record exists and no
   run-state record does.

## B: Recovery

### B1: Overlay at recovery

As the manager, I want recovery to see each job's latest durable run state,
so that `recoveredItemDef`, `recoverRunnerHold` and `recoverRunningJob` act
on it.

`recoverIncompleteJobs` applies each matching run-state record to its decoded
job before returning, so every later recovery step sees the overlaid job. It
walks `bucketJobRunState` alongside `bucketJobsLive`, both sorted by key, not
one `Get` per live job. `decodePriorJobs`' `recovering: decoded live jobs`
line gains `runStates=<applied>`.

```go
// runStateRecovery is what recoverIncompleteJobs did with run-state records.
type runStateRecovery struct {
	applied int // matching records overlaid
	dropped int // stale or orphaned records deleted
}

func (db *db) recoverIncompleteJobs() ([]*Job, runStateRecovery, error)
```

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`, `jobqueue/server.go`
**Test file:** `jobqueue/db_runstate_test.go`

**Acceptance tests:**

1. Given 3 live jobs, 2 with matching run-state records (State running, Host
   `h1`, Pid 7, Attempts 1) and 1 without, when `recoverIncompleteJobs` runs,
   then it returns 3 jobs, the 2 with State running, Host `h1`, Pid 7,
   Attempts 1 and their live records' Cmd and LimitGroups, the third as its
   live record says, and `applied` 2, `dropped` 0.
2. Given a server killed (no clean stop) after a job was reserved and before
   `Started`, as in `TestReserveDurability`, when a new server starts on the
   database, then its log has `recovering: decoded live jobs` with
   `runStates=1`, and the job is in the run sub-queue reserved by the
   original client.

### B2: Stale and orphaned records are dropped

As the manager, I want records that no longer apply to be ignored and
deleted at recovery, so that a later add of the same job cannot pick them up.

After its read, `recoverIncompleteJobs` deletes every stale or orphaned
run-state record in one write transaction. Inside that transaction each
candidate is checked again against the live bucket as it is then, and only
deleted if it is still stale or orphaned: on a running server (A2 test 1) a
drain may rewrite either record between the read and the write.
`decodePriorJobs`, which has the context, logs `clog.Warn(ctx, "recovering:
dropped stale job run-state records", "count", dropped)` if `dropped` is
above 0.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`
**Test file:** `jobqueue/db_runstate_test.go`

**Acceptance tests:**

1. Given a live job whose run-state record's CRC does not match its live
   record, when `recoverIncompleteJobs` runs, then the job is returned as its
   live record says, `dropped` is 1, and afterwards no run-state record exists
   for it.
2. Given a run-state record with no live record, when `recoverIncompleteJobs`
   runs, then `dropped` is 1 and the record is gone.
3. Given a stale record found by the read, when a test hook between the read
   and the write transaction rewrites it as a matching record, then the
   write keeps it and `dropped` is 0.
4. Given no stale or orphaned records, when `recoverIncompleteJobs` runs, then
   it commits nothing (bolt's `Stats().TxStats.GetWrite()` is unchanged)
   and logs no `dropped stale` line.

## C: Schema version

### C1: Open refuses unsupported databases and stamps version 2

As an operator, I want the manager to refuse a database it cannot read
correctly, without changing it, and to tell me what to do.

In `initDB`, once an open has succeeded (including after a restore from
backup) and before any write transaction (`openManagerBolt` sets
`NoFreelistSync`, so the open itself writes nothing):

1. Read the version with `dbFileSchemaVersion`. On an error (including
   `errBadDBSchemaVersion`, a malformed stamp), close the bolt handle and
   return it, before any write.
2. If `checkDBSchemaVersion` fails, close the bolt handle and return its
   error.
3. If the file existed before this open (`openedExistingDB`), the version is
   0 and the file has a `jobslive` bucket, close and return the version-0
   error. A file with no `jobslive` bucket (left by a first start that
   crashed before its first commit) is treated as new.
4. In the existing bucket-creation `Update`, stamp `currentDBSchemaVersion`
   if the database is new or its version is below it.

Nothing else reads the schema version, so `wr manager start` reports the
error as it reports any `initDB` failure.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`, `jobqueue/db_schema.go`
**Test file:** `jobqueue/db_schema_test.go`

**Acceptance tests:**

1. Given no database file, when `initDB` runs, then the file's version is 2
   and `bucketJobRunState` exists.
2. Given a database stamped 1 holding 2 live jobs, when `initDB` runs and
   recovery reads it, then 2 jobs are recovered and the version is 2.
3. Given a database stamped 2, when `initDB` runs, then the version stays 2.
4. Given a database stamped 3, when `initDB` runs, then the error satisfies
   `errors.Is(err, errDBSchemaTooNew)`, its text contains `schema version 3`
   and `supports up to 2`, and the file's SHA-256 is unchanged.
5. Given an existing unversioned database with a `jobslive` bucket, when
   `initDB` runs, then the error satisfies `errors.Is(err,
   errDBNeedsCompact)`, its text contains `wr manager compact`, and the
   file's SHA-256 is unchanged.
6. Given an existing bbolt file with no buckets, when `initDB` runs, then it
   succeeds and the version is 2.
7. Given a missing database file and an unversioned backup with a `jobslive`
   bucket, when `initDB` runs, then it returns the version-0 error, the
   database file now exists, and after `CompactDBFileStats(dbFile)` a second
   `initDB` succeeds at version 2.
8. `TestDBSchemaVersionOnOpen`'s "leaves an existing unversioned database
   unstamped" case is replaced by test 5. `reliable2_dbcompat_test.go`
   copies `db.golden` to a temp dir and compacts the copy before starting a
   server on it, and passes. Any other test that builds an unversioned
   database with a `jobslive` bucket and opens it with `initDB` stamps it
   first.
9. Given a database whose stamp is 3 bytes long, when `initDB` runs, then
   the error satisfies `errors.Is(err, errBadDBSchemaVersion)` and the
   file's SHA-256 is unchanged.

### C2: Compact stamps the current version

As an operator, I want `wr manager compact` to bring any supported database
to the current version, stripping output only once, and to leave a database
it refuses untouched.

- `CompactDBFileStats` first calls `readOnlyDBFileSchemaVersion(dbFile)` and
  `checkDBSchemaVersion`, returning any error (including
  `errBadDBSchemaVersion`) before `compactToTempFile` creates the temp
  file. `compactBoltInto` opens the source without
  `NoFreelistSync`, which writes a never-synced freelist on open, so the
  check must come before it.
- `compactBolt`: at version 1 or above, `bolt.Compact` as now, then stamp
  `currentDBSchemaVersion` in the destination. Below version 1,
  `compactStrippingStd`, whose `copyAll` stamps `currentDBSchemaVersion`.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`, `jobqueue/db_compact.go`, `jobqueue/db_schema.go`
**Test file:** `jobqueue/db_compact_std_test.go`

**Acceptance tests:**

1. Given an unversioned database whose complete records hold output, when
   compacted, then `OutputStripped` is true and the version is 2.
2. Given a version-1 database with a `bucketJobRunState` record, when
   compacted, then `compactStdDecodeObserver` is never called, the version is
   2, and the run-state record is byte-equal to before.
3. Given a version-2 database, when compacted, then the observer is never
   called and the version is 2.
4. Given a database stamped 3 and last written with `NoFreelistSync` (its
   freelist never synced, as a crashed manager leaves it), when compacted,
   then the error satisfies `errors.Is(err, errDBSchemaTooNew)`, the file's
   SHA-256 is unchanged, and no `*.compact-*` file is in its directory.
5. Given a database whose stamp is 3 bytes long, when compacted, then the
   error satisfies `errors.Is(err, errBadDBSchemaVersion)`, the file's
   SHA-256 is unchanged, and no `*.compact-*` file is in its directory.

### C3: The user sees the refusals

As an operator, I want `wr manager start` and `wr manager compact` to show me
the refusal, not just fail.

The manager child dies through `die("wr manager failed to start : %s",
err)` when `Serve` fails, which logs at error level (`lvl=eror`,
`cmd/root.go`); the daemon parent prints
`getBadLogLines()` and `startupErr`. No code change is expected unless a
test below fails.

**Package:** `cmd/`
**File:** `cmd/manager.go`
**Test file:** `cmd/manager_test.go`

**Acceptance tests:**

1. `TestManagerStartRefusesUnsupportedDB`: given an isolated config whose
   `ManagerDBFile` is an unversioned database with a `jobslive` bucket, when
   `wr manager start --foreground` runs as a subprocess (the test binary
   re-executed, as `manager_stop_test.go` does), then it exits non-zero, its
   stderr contains `wr manager compact`, and `getBadLogLines()` on its log
   returns a line containing `wr manager compact`. The same with a database
   stamped 3 shows `schema version 3` in both.
2. `TestManagerStartDaemonShowsRefusal`: the same two databases with the
   default daemon `wr manager start` as a subprocess: the parent exits
   non-zero and its output contains the `getBadLogLines()` line and `wr
   manager failed to start on port <port>:` followed by the startup error,
   with `wr manager compact` (version 0) or `schema version 3` (version 3)
   in that output.
3. `TestManagerCompactRefusesNewerDB`: given `managerCompactExit` replaced
   as in `TestManagerCompactRefusesWhileRunning` and `ManagerDBFile` a
   database stamped 3, when the compact command's `Run` executes, then the
   exit code is 1, the logged error contains `schema version 3`, and the
   file's SHA-256 is unchanged.

## D: Observability

### D1: Each non-durable hand-out is logged

As an operator, I want one log line per reservation handed out before it was
durable, with its key and a running total, so that a soak or incident has an
exact per-key count up to any crash.

- `db.reservesNotDurable atomic.Uint64`. `func (db *db)
  noteReserveNotDurable() uint64` increments it and returns the new total.
- `persistReservation`, on any non-nil error, including `errDBClosed`
  (`respondWithReservedJob` still hands the job out then, as around a clean
  stop), logs before the job is handed out:

  ```go
  clog.Info(context.Background(), reserveNotDurableLogMsg, "key", job.Key(),
  	"total", s.db.noteReserveNotDurable())
  ```

  with `const reserveNotDurableLogMsg = "reservation handed out before it was
  recorded on disk"`. The total counts from 0 in each manager process. The
  existing rate-limited warning and error line stay.
- The context is `context.Background()`, not the request's.
  `setupManagerLogging` (`cmd/manager.go`) adds the info-level file handler
  to clog's root logger and gives `Serve` a context carrying a warn-level
  file handler (debug with `--debug`). clog uses a context's handler in place
  of the root's, so `clog.Info` on a request context never reaches the file.
  A context with no handler goes to the root logger, whose file handler is at
  info.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/db.go`
**Test file:** `jobqueue/reserve_durability_test.go`

**Acceptance tests:**

1. Given logging set up as `setupManagerLogging` does it (handlers from
   `clog.CreateFileHandlersAtLevels(<tmp>/log, "info", "warn")`, the first
   added with `clog.AddHandler`, the second put on the context passed to
   `serve` with `clog.ContextWithLogHandler`), `ReserveWriteWait` 300ms and
   bolt's write transaction held for 5s (as in
   `TestReserveDurabilityStalledWrite`), when jobs A then B are reserved,
   then `<tmp>/log` holds exactly 2
   `reservation handed out before it was recorded on disk` lines, A's with
   `key=<A's key> total=1` and B's with `key=<B's key> total=2`, and
   `reservation not yet recorded on disk` is still logged. The test restores
   clog's root handler afterwards (deferred `clog.ToDefault()`).
2. Given writes that commit promptly, when 3 jobs are reserved, then no such
   line is logged and the counter is 0.
3. Given a server whose `db.closed` is set under the db's lock (as `close`
   sets it), when `persistReservation` runs for job A, then one D1 line with
   `key=<A's key> total=1` is logged.

## E: Tooling and documentation

### E1: dbstart overlays run-state records

As a soak analyst, I want `dbstart` to print a live job's latest durable run
state, so that `starttimes.py` and `relburycheck.py` stay correct.

For each `jobslive` record, if `jobRunState` has a record for the key whose
first 4 bytes are the big-endian CRC-32C of the live value, decode the rest
over the same `rec` (fields match by name). Otherwise print the live record
as now. Output columns are unchanged. `run` becomes `run(path string, out
io.Writer) error`.

**Package:** `main`
**File:** `developers/soak/dbstart/main.go`
**Test file:** `developers/soak/dbstart/main_test.go`

**Acceptance tests:**

1. Given a database built with bbolt whose `jobslive` holds key `k1` with Cmd
   `/x/psimjob.sh portal 12`, State `ready`, Attempts 0, and whose
   `jobRunState` holds a matching record with State `running`, Host `h1`,
   Pid 7, Attempts 1, when `run` writes to a buffer, then the `k1` line's
   state, attempts, host and pid columns are `running`, `1`, `h1`, `7`.
2. Given the same record with a CRC that does not match, then the line shows
   `ready`, `0`, empty host and pid `0`.
3. Given no `jobRunState` bucket (a version-1 database), then the line shows
   the live record's values.
4. Cross-check against the real format: given a server started with the
   exported `jobqueue.Serve` on a temp database, a job with Cmd
   `/x/psimjob.sh portal 13` added and reserved through `jobqueue.Connect`
   (no `Started`), and a crash image written with the exported
   `Server.BackupDB` to a file before `Stop`, when `run` reads that file,
   then the job's line shows state `reserved` and host `os.Hostname()` (the
   client sends its own hostname).

### E2: statinspect clearlive empties run-state records

`clearLive` also deletes every key of `jobRunState`, if that bucket exists,
in the same transaction, and prints `jobRunState keys: before=%d after=%d`
after the `jobslive` line. It becomes `clearLive(path string, out
io.Writer) error`; `main` prints the error and exits 1 as now.

**Package:** `main` (module `statinspect`)
**File:** `.docs/reliable2/harness/statinspect/main.go`
**Test file:** `.docs/reliable2/harness/statinspect/main_test.go` (GoConvey,
added to that module's `go.mod` at wr's version)

**Acceptance tests:**

1. Given a bbolt file with 2 `jobslive` keys and 3 `jobRunState` keys, when
   `clearLive` runs, then its output contains `jobslive keys: before=2
   after=0` and `jobRunState keys: before=3 after=0`, and both buckets are
   empty.
2. Given a bbolt file with 2 `jobslive` keys and no `jobRunState` bucket,
   when `clearLive` runs, then it returns nil and its output has no
   `jobRunState` line.

### E3: CHANGELOG and compact help

**File:** `CHANGELOG.md`, `cmd/manager.go`
**Test file:** `cmd/manager_test.go`

Under `## [Unreleased]`, first in `### Changed`:

```markdown
- The manager now refuses to start on a database created by wr 0.37.2 or
  earlier that has never been compacted. Stop the manager, run
  `wr manager compact` once, then start it again. The manager and
  `wr manager compact` also refuse a database written by a newer wr, naming
  both versions, and leave it unchanged.
- Once this version's manager has opened a database, going back to an
  earlier wr is not supported: an earlier version may run again commands
  that were reserved or running when this version stopped.
```

First in `### Fixed`:

```markdown
- With thousands of runners, the manager now records each reservation and
  start with a small write instead of rewriting the whole command, so far
  fewer commands are handed out before their reservation is on disk, and far
  fewer run twice if the manager crashes. Each command that is still handed
  out before its reservation is on disk is now logged at info with its key
  and a running total.
```

In `managerCompactCmd.Long`, the last paragraph becomes:

```text
A database created by wr 0.37.2 or earlier must be compacted once before
the manager will start on it. wr versions 0.37.0 to 0.37.2 kept the output
of every successfully completed command in the database, up to about 16KB
each; compact removes it and reports how many completed commands it was
removed from. Later compactions skip this. compact refuses (exiting
non-zero, leaving the database untouched) a database written by a newer
version of wr.
```

**Acceptance tests:**

1. In `cmd/manager_test.go`, `managerCompactCmd.Long` ends with the paragraph
   above, compared as an exact string.

Review check (not a test): `CHANGELOG.md` has the three entries in the
positions given.

### E4: Fixture compaction

As a gate runner, I want compacted copies of the version-0 fixtures, made
without touching the originals, so that every big-DB scenario runs on this
tree and on develop alike.

Sources, all version 0 and read-only to the gate:
`/nfs/hgi/wr/sb10-bigdb/pristine6`, `/nfs/hgi/wr/sb10-bigdb/pristine10` and
`/nfs/hgi/wr/sb10-bigdb/prod.db`. The gate directory is
`G=/nfs/hgi/wr/sb10-bigdb/runstate-gate`, new. Every `wrdev.sh` run in E4
and F sets `WRDEV_ROOT` under `$G` (never the `$HOME/wr-devtest` default:
the home quota is 50G and `add-storm-fixture` needs about 3x its 7.4GB
base) and sets `DEV_PORT DEV_WEB PROD_PORT PROD_WEB` explicitly, checked
free with `ss -ltn`, then runs `wrdev.sh build` first. Every command in E4
and F2, including `wrdev.sh build` and F2.5's `go test`, runs with all
`OS_*` unset and `GOCACHE=/tmp/claude-11346/gocache-runstate-gate`, never
the home directory's Go cache.

- New `wrdev.sh compact-fixture <src> <dst>`:
  - Refuses if `dst` exists, if `src` and `dst` are the same file, if
    `src.aslmanifest` exists (an add-storm fixture embeds its
    `<src>.jobcwd` path in its commands, so it is regenerated with
    `add-storm-fixture`, never copied), or if the dev manager is up.
  - `cp -p src dst`, then runs `$WR manager compact` on `dst` with the dev
    manager's isolated config pointed at `dst`. On failure it removes `dst`
    and exits non-zero. It never opens `src` for writing.
  - Listed in `usage` and `main`.
- All texts that point at the version-0 originals point at compacted copies
  made with `compact-fixture` instead: every help, comment, `die` and `echo`
  text found by `grep -n 'pristine\|prod\.db\|fix120k'
  developers/wrdev.sh developers/speed.sh` (after the rebase onto 021f734a
  these include the `writestorm-freeze`, `add-storm`, `add-storm-fixture`,
  `add-storm-lsf`, report-storm `WR_RS_DB` and `backup-stall-check` texts and
  the `usage` text), and the skip messages and header comments of
  `reliable4_addstorm_test.go`, `reliable4_reportstorm_test.go` and
  `reliable4_writestorm_freeze_test.go`.
- The gate's fixtures, built with this tree's `wrdev.sh` and
  `WRDEV_ROOT=$G/fixbuild/root DEV_PORT=51970 DEV_WEB=51971 PROD_PORT=51972
  PROD_WEB=51973`. Names match what `soak/sweep.sh` expects under
  `SWEEP_DB_DIR=$G/fixtures`:
  1. `compact-fixture` each source to `$G/fixtures/pristine6`,
     `$G/fixtures/pristine10` and `$G/fixtures/prod.db`.
  2. `WRDEV_PRISTINE_DB=$G/fixtures/pristine6
     WRDEV_ASL_FIXTURE=$G/fixtures/fix120k.db wrdev.sh add-storm-fixture
     120000 5 200`, which writes `fix120k.db`, its `.aslmanifest` and its
     own `fix120k.db.jobcwd` in `$G/fixtures`.

**File:** `developers/wrdev.sh`, `jobqueue/reliable4_addstorm_test.go`
**Test file:** none; each is run end to end before the PR is ready.

**Acceptance tests:**

1. Given a copy of `jobqueue/testdata/dbcompat/db.golden` as `src`, when
   `compact-fixture` runs, then this tree's manager starts on `dst` and
   `src`'s SHA-256 is unchanged.
2. Given `dst` already present, or `src.aslmanifest` present, when it runs,
   then it exits non-zero and no file changes.
3. Given step 2's fixture, when `WRDEV_PRISTINE_DB=$G/fixtures/fix120k.db
   wrdev.sh add-storm-lsf` starts, then it prints `fixture manifest OK`.

### E5: Soak gate classification

As a soak analyst, I want one step that computes F3's criteria from a soak's
output, so that they are counted, not judged.

"Injected stall" and "commit stall" both mean an injected FUSE commit stall:
`stall.sh` writes `<epoch> STALL START ...` and `<epoch> STALL END ...` to
`<outdir>/stall.log`. A stall window runs from a START to its END plus
`ReserveWriteWait` (10s); a START with no END ends at START plus
`STALL_SECS` (180) plus 10s. `crashon.sh stall` only triggers a crash during
a stall and adds no window.

New `developers/soak/soakgate.py --source d1|warning <outdir>
<runnerlogdir> <doubles.tsv> <dbstart.tsv>`:

- `--source` is required: `d1` for a tree with D1 lines (this change),
  `warning` for one without (the baseline). With `d1`, a log with no D1
  line counts 0, which is a valid result, not a fallback to warnings.
- Manager log: only `<outdir>/manager.log`. The `manager.log.<epoch>` files
  are `cp -f` copies of the whole growing log taken at each stop, so they
  are ignored. The log is split into one segment per manager process at
  each line matching `msg="wr manager \S* started on \S+, pid (\d+)"`, and a
  segment is named by that pid. The version may be empty (soak builds use
  `-buildvcs=false`, giving `wr manager  started on`, two spaces), which
  cmd's `managerStartedLogRegex` does not match. Lines before the first
  start are ignored, since no reservation is handed out before a manager has
  started.
- Non-durable hand-outs: with `d1`, D1 lines (`reservation handed out before
  it was recorded on disk`), whose `total` values within each segment must
  be exactly the set {1..max}, each once, in any order (the total is taken
  from an atomic increment and logged without a lock, so concurrent lines
  can be out of order); with `warning`, the rate-limited `reservation not
  yet recorded on disk` warning's lines plus their `repeats=` values. Each
  is inside or outside a window by its timestamp.
- Runs: the `runs=` value on the first line of `<outdir>/markers-analysis.txt`
  (markers.py's output).
- Peak RUN: the largest `RUN=<n>` in `<outdir>/lsf.tsv`.
- Double runs: for each `doubles.tsv` row, the first run is mapped to its
  runner log and that log's `reserved a job` line as `anyway.py` does (the
  run's marker host and pid, then the runner's `started executing ... pid=`
  line). The double is inside or outside a window by that line's time; one
  whose runner log or `reserved a job` line cannot be found counts as
  outside and is printed with `unmapped`. It is "acknowledged" if that
  runner log has a `command ran OK` line for the same key between the first
  run's start and the second run's start.
- Missing jobs: every psimjob (kind, id) with an `S` line in
  `<outdir>/markers/*.tsv` must have a row in `dbstart.tsv`, except that
  one prodsim may have removed is excused: kind `put` (added with
  `OnFailure Remove`) or `fofnput` (removed by `fofnPoll`'s
  `remove_buried`), whose last run's `E` marker has a non-zero exit code or
  which has no `E` marker.
- Prints exactly, then one line per outside or acknowledged double and per
  absent job:

  ```text
  nondurable source=<d1|warning> inside=<n> outside=<n> runs=<runs> outsidePct=<x.xxxx>
  totals <ok|GAP pid <pid> after <n>|n/a>
  doubles inside=<n> outside=<n> acknowledged=<n>
  missing ran=<n> absent=<n> excused=<n>
  peakRUN=<n>
  ```

  (`totals n/a` with `warning`. In `GAP`, `<pid>` is the first segment
  whose totals are not exactly {1..max}, and `<n>` the largest k such that
  each of 1..k appears in it exactly once.)

**File:** `developers/soak/soakgate.py`, `developers/soak/README.md`
(analysis steps)
**Test file:** `developers/soak/testdata/soakgate/`: one directory per
case below, each with an input outdir, runner logs, `doubles.tsv`,
`dbstart.tsv`, an `args` file and `expected.txt`, plus `run.sh` (F1.6),
which runs every case and diffs.

**Acceptance tests:**

1. Given `testdata/soakgate` with `stall.log` holding one window 1000-1180;
   one cumulative `manager.log` with two empty-version start lines
   (`msg="wr manager  started on 10.0.0.1:1, pid 11"` and `... pid 22"`), a
   D1 line at 1005 (`total=1`) and one at 1300 (`total=2`) after the first,
   and one at 1400 (`total=1`) after the second; a stale `manager.log.1350`
   holding a copy of the log up to 1350; `markers-analysis.txt` with
   `runs=10000`; `lsf.tsv` rows with `RUN=700` and `RUN=6100`; runner logs
   and a `doubles.tsv` with three double runs whose first reservations are
   at 1010, 1500 (with `command ran OK` before its second run) and one with
   no runner log; and markers for 3 jobs, all in `dbstart.tsv`; when
   `soakgate.py --source d1` runs, then its output equals `expected.txt`:
   `nondurable source=d1 inside=1 outside=2 runs=10000 outsidePct=0.0200`,
   `totals ok`, `doubles inside=1 outside=2 acknowledged=1`, `missing ran=3
   absent=0 excused=0`, `peakRUN=6100`, then the 1500 double's line and the
   unmapped double's line.
2. Given the same input with the 1400 line's `total=2`, then the totals line
   is `totals GAP pid 22 after 0`.
3. Given a warning-only log with one warning line inside the window and a
   `(repeated) repeats=3` line outside it, when run with `--source warning`,
   then the first two lines read `nondurable source=warning inside=1
   outside=3 runs=10000 outsidePct=0.0300` and `totals n/a`.
4. Given one ordinary marker job removed from `dbstart.tsv`, then the
   missing line is `missing ran=3 absent=1 excused=0`, followed by that
   job's line.
5. Given two more marker jobs absent from `dbstart.tsv`, a `fofnput` whose
   last `E` marker has exit 3 and a `fofnput` whose last exit is 0, then the
   missing line is `missing ran=5 absent=1 excused=1` and the exit-0 job is
   the one printed.
6. Given the same input with the first segment's two D1 lines swapped (the
   `total=2` line first), then the totals line is `totals ok`.
7. Given the first segment with a second `total=1` line, then the totals
   line is `totals GAP pid 11 after 0`.

## F: Gates

### F1: Local gates

With all `OS_*` unset and `GOCACHE` off the home directory:

1. `make lint`, `make test` and `CGO_ENABLED=1 make race` pass, and `go vet
   -tags netgo,reliability_repro ./jobqueue/` passes (B1 changes
   `recoverIncompleteJobs`' signature, which the `reliability_repro`-tagged
   `dbstart_probe_test.go` calls).
5. `cd .docs/reliable2/harness/statinspect && GOFLAGS=-mod=mod GOPROXY=off
   go mod tidy && go test ./...` passes. goconvey v1.8.1 (wr's version) and
   its dependencies resolve offline from the local module cache, where wr's
   own build put them; the updated `go.mod` and `go.sum` are committed.
6. `developers/soak/testdata/soakgate/run.sh` (new) passes: for each case
   directory under `developers/soak/testdata/soakgate/`, it runs
   `soakgate.py` with that case's `args` file and `diff`s the output against
   the case's `expected.txt`, exiting non-zero on any difference.
2. `make speed` against `SPEED_BASE` = the develop merge-base, with
   `SPEED_DIR=$G/speed-quick DEV_PORT=51990 DEV_WEB=51991 PROD_PORT=51992
   PROD_WEB=51993`, reports no worsening over its threshold. The PR body
   summarises its verdict and A2 test 5's two `bolt_pages/job` figures.
3. `make speed-full` with `SPEED_DIR=$G/speed-full
   SPEED_BIG_DB=$G/fixtures/pristine6 WR_AS_DB=$G/fixtures/prod.db
   WR_ARCHRATE_DB=$G/fixtures/pristine10 WR_AC_DB=$G/fixtures/pristine6
   DEV_PORT=51994 DEV_WEB=51995 PROD_PORT=51996 PROD_WEB=51997` reports no
   worsening. `speed.sh` puts its wrdev roots under `SPEED_DIR` and ignores
   `WRDEV_ROOT`.
4. For both, benchstat is the installed
   `/nfs/hgi/wr/sb10-bigdb/speed/tools/benchstat-v0.0.0-20260929162123-406019bb8b68`
   copied into `$SPEED_DIR/tools/` first, as battery10's `speed-go.sh` did
   (no network).

### F2: wrdev crash, recovery and big-DB modes

Fixtures are E4's, never the originals. With `WRDEV_ROOT=$G/f2/root
DEV_PORT=51980 DEV_WEB=51981 PROD_PORT=51982 PROD_WEB=51983` and this tree's
`wrdev.sh build`, each passes, in sequence:

1. `wrdev.sh crash-recovery` prints `PASS: re-sent archive accepted
   (complete=1), command ran exactly once` and exits 0.
2. `WRDEV_PRISTINE_DB=$G/fixtures/fix120k.db wrdev.sh add-storm-lsf`
   (defaults) exits 0, prints `recovered-job audit: <n>/<n> incomplete
   commands read` with both numbers equal to the manifest's incomplete
   count, its final `## VERDICT:` line has `missingAcked=0`, and it prints
   its `PASS: all <n> acknowledged adds` line. (Its `recoveredIncomplete`
   and `unsafeCommands` VERDICT lines appear only on its failure paths.)
3. `wrdev.sh dep-granularity-check` (defaults) exits 0.
4. Each of these exits 0, with the fixtures `soak/sweep.sh` and battery10's
   `speed-go.sh` use, all `$G/fixtures` compacted copies:
   - `WR_ARCHRATE_DB=$G/fixtures/pristine10 wrdev.sh archive-rate`
   - `WR_AC_DB=$G/fixtures/pristine6 WRDEV_AC_WORK=$G/f2/root wrdev.sh
     archive-ceiling`
   - `WR_AS_DB=$G/fixtures/prod.db WRDEV_AS_WORK=$G/f2/root wrdev.sh
     add-storm`
   - `WR_WSFREEZE_DB=$G/fixtures/pristine10 wrdev.sh writestorm-freeze`
   - `WRDEV_PRISTINE_DB=$G/fixtures/pristine10 wrdev.sh backup-stall-check`
5. `WR_AS_DB=$G/fixtures/prod.db CGO_ENABLED=1 go test -tags
   netgo,reliability_repro --count 1 -run TestReliable4AddStorm ./jobqueue/`
   passes.

### F3: Production-scale LSF crash soaks

Two soaks, one after the other, never concurrently: a baseline of develop
at the PR's merge-base, then this tree. Each uses its own tree's binary for
the whole run (never replaced mid-run).

Each tree is an export, as battery10 did: `git archive <sha> | tar -x -C
<dir>`, then `git init <dir>`, so `soak/config.sh`'s `git rev-parse
--show-toplevel` finds it. Each soak's launcher is battery10's
`soak-go.sh` with only these values changed (ports checked free with
`ss -ltn` first):

| Value | Baseline | Change |
| --- | --- | --- |
| tree | `$G/src-base` (merge-base) | `$G/src-change` (PR head) |
| `H` | `$G/src-base/developers/soak` | `$G/src-change/developers/soak` |
| `SOAK_ROOT` | `$G/soak-base/run` | `$G/soak-change/run` |
| `FIXTURE` | `$G/soak-base/fix120k.db` | `$G/soak-change/fix120k.db` |
| ports `DEV_PORT DEV_WEB PROD_PORT PROD_WEB PPROF_PORT` | `51950 51951 51952 51953 6250` | `51960 51961 51962 51963 6260` |
| `GOCACHE` | `/tmp/claude-11346/gocache-runstate-base` | `/tmp/claude-11346/gocache-runstate-change` |

- `PROD_JOBTOKEN` is left at its default, which `wrdev.sh` derives from
  `PROD_PORT`, the host and the wrdev root, so the two soaks' LSF job names
  differ and each tool kills or bkills only its own soak's jobs. Each soak
  runs all its commands on one host. Restarts are serialised by the
  `restart.lock` in each soak's own output dir.
- Each `FIXTURE` is a `cp -p` of `$G/fixtures/fix120k.db` made just before
  that soak; `wrdev.sh prodsim` copies it again into the soak's DB dir.
- Unchanged from `soak-go.sh`: `HOURS=3`, `SCALE=1`, its `RESTART_KINDS`
  (6 or more crashes), `USE_FUSE=1` with `STALL_SECS=180`, `RAMP0=600
  RAMP="0:600 8:1200 16:2100 25:3000 40:3600 60:4000"`, and the
  `crashon.sh` (stall and two burst), `crashafter.sh`, `relbury.sh` (r1,
  r2), `rundep.sh` and `stopstate.sh` injectors.
- Both soaks are analysed with the change tree's analysis tools (README
  steps, `dbstart`, E5's `soakgate.py`).

This tree's soak passes only if all hold:

1. `soakgate.py`'s `peakRUN=` is at least 5,500 in both soaks, and the two
   are within 10% of each other.
2. `soakgate.py --source d1` prints `totals ok`, an `outsidePct` of at most
   0.0340, and an `outside` count below that of the baseline's
   `soakgate.py --source warning`. The baseline's warning-based
   count is approximate: it counts only expired waits, loses a pending
   summary at a crash, and stamps a summary when it is emitted, which can
   move repeats outside a stall window and make the comparison more
   lenient. The absolute 0.034% bar decides.
3. `soakgate.py` prints `doubles` with `outside=0` and `acknowledged=0`.
4. `soakgate.py` prints `missing` with `absent=0`, and `relburycheck.py`
   reports problems 0.
5. Every `instance <n>: CHECK ...` line `rundepcheck.py` prints ends with
   `-noend-then-stop-clean` (a B or D run killed by a clean stop, which the
   owner ruled "stop means buried"); any other CHECK fails.

The PR body records both run directories, peak RUN, both soaks'
`soakgate.py` output, and the verdict.

## Implementation Order

1. **Phase 1: schema (C1, C2, C3).** First.
2. **Phase 2: record and cleanup (A1, A4, A5).** Depends on phase 1, whose
   `initDB` changes A1's bucket creation sits beside. Reserve and start still
   write full records, so behaviour is unchanged.
3. **Phase 3: switch the writes and overlay (A2, A3, B1, B2), with the A2
   audit.** Depends on phase 2. A2 must not land without B1: without the
   overlay a crash would recover pre-reservation state.
4. **Phase 4: logging (D1).** After phase 3.
5. **Phase 5: tools and docs (E1, E2, E3, E4, E5).** E1 and E2 depend on
   phase 2's format, E1 test 4 on phase 3, E3 on phases 1 and 3, E5 on D1.
   E4 needs phase 1's compact.
6. **Phase 6: gates (F1, F2, F3).** Sequential: F1, then F2, then F3. F3 is
   the last step before the PR is ready.

## Appendix: Key Decisions

- **Precedence comes from transaction structure plus a CRC.** No per-job
  counter is persisted. Every full live write or delete removes the run-state
  record in the same transaction, and a drain writes the run state after the
  job's full writes. So an existing run-state record is always newer than
  the full record beneath it. The CRC of the live record it was written over
  defines "stale" without trusting every path, and it makes a record left by
  an earlier wr (after an unsupported downgrade and re-upgrade) or by a
  defect harmless. Hashing about 10KB with CRC-32C costs about 1us.
- **Field set.** The record holds every persisted field that a reservation
  or start sets, including the ones `resetRunLocked` clears, so the overlay
  of an older full record reproduces the reserved or started job (A1 tests 2
  and 6, A2 test 1). The A2 audit covers fields changed elsewhere that relied
  on the reservation's full write.
- **Learned requirements go in the record, not a full write in
  `prepareReadyJob`.** `prepareReadyJob` runs for every schedulable job in
  each rac cycle; a full write there would put 10KB records back on the hot
  path this change removes, and in a transaction of their own. The two
  `Requirements` structs add about 200 bytes, keeping the record within
  1,024 bytes. A reservation without `Started` is all a crash needs to lose
  them, which A2 test 3 covers.
- **Coalescing keeps both slots.** A reservation queued after a kick in the
  same drain must not discard the kick's full record, since the run-state
  record does not carry `UntilBuried` and other fields. A later full change
  clears the run-state slot because its encoding already holds the newer run
  state.
- **A stale queued change racing an archive's keep-live put** behaves as a
  stale full change does today: the drain writes onto whatever live record
  exists. This change neither widens nor closes that window.
- **Recovery deletes stale and orphaned records.** Recovery runs once, before
  any writer is busy. Deleting there stops an orphan from matching a later
  re-add with byte-identical add-time encoding. `dbstart` and `statinspect`
  skip non-matching records and delete nothing of them.
- **No fold-back at stop, no downgrade.** As the owner decided. The CHANGELOG
  says so. The schema-version check protects later downgrades to this version
  or newer. It cannot protect a downgrade to v0.38.0, which does not check.
- **Compact refuses a newer database, checked read-only first.** Stamping 2
  onto a version-3 database would make it claim less than it holds. The
  read-only open is what keeps the refused file byte-identical.
- **Version-0 refusal is in `initDB`, before any write.** All opens,
  including restore from backup, go through it, so one check covers them, and
  a refused database is left unchanged for `wr manager compact`. Only files
  with a `jobslive` bucket are refused, so an empty file from a crashed first
  start is not mislabelled.
- **Per-hand-out info lines, warning kept.** The lines are logged on a
  context with no handler, so they reach the root logger's info-level file
  handler; the server's own context carries a warn-level handler that would
  drop them (D1 test 1 uses the real configuration). Each is written before
  the hand-out, so the count is exact up to a crash. battery10 would have
  produced about 23k lines in 3h, at most about 4k a minute. The rate-limited
  warning stays so existing log watchers and
  `TestReserveDurabilityStalledWrite` keep working.
- **Baseline soak.** The fixtures had to be compacted for this tree, which
  changes their freelist, so battery10's figures are not like for like. The
  baseline soak on the same compacted fixture is. `fix120k` is regenerated
  from compacted `pristine6`, not compacted itself, because its commands
  embed its source's `.jobcwd` and `add-storm-lsf` writes there, which would
  touch the original.
- **`errDBClosed` hand-outs are logged.** `respondWithReservedJob` hands the
  job out whatever `persistReservation` returns, so a hand-out during a
  clean stop is exposed like any other and must be in the exact count.
- **Testing.** GoConvey for new tests, following go-implementor and
  go-reviewer. Crash behaviour is tested with a crash image (`BackupDB`)
  restored under a new server, as the existing durability tests do. Existing
  ordering and durability tests stay and must pass unchanged except where a
  story says they are extended.
