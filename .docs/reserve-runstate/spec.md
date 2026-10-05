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
durability.

The database schema version becomes 2. The manager refuses a database newer
than it understands, and refuses a version-0 database until it has been
compacted. A cumulative count of reservations handed out before they were
durable goes into the `archive fold` log line. Downgrading after this version
has opened a database is not supported.

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
// (applyJobStart, acceptDuplicateStartLocked) sets. Field names and types
// match Job's, with no omitempty, so every field, zero or not, is encoded.
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
}

// newJobRunState returns j's run state. The caller holds j's read lock.
func newJobRunState(j *Job) jobRunState

// applyTo sets every field of r on j. The caller holds j's lock, or j is
// not yet shared.
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
- Size: at most 768 bytes for the A1 example job, whatever its Cmd size.

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
review check is that `grep -n 'bucketJobsLive' jobqueue/*.go` shows no other
`Put` or `Delete` on that bucket.

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

**Package:** `jobqueue/`
**File:** `jobqueue/db_runstate.go`
**Test file:** `jobqueue/db_runstate_test.go`

Example job: Cmd of 10,000 `x`, State running, Host
`node-1-2-3.internal.sanger.ac.uk`, HostIP `172.27.71.182`, HostID a 36-char
UUID string, ActualCwd 100 bytes, Pid 7, RunnerPid 6, Attempts 1,
RunnerReservation 5, ReservedBy a fixed UUID, StartTime set, std nil.

**Acceptance tests:**

1. Given the example job, when `runStateRecord(live, db.encode(newJobRunState
   (job)))` is built over its full encoding `live`, then the record is at most
   768 bytes and `len(live)` is at least 10,000.
2. Given that record, when `runStateOver(live, record)` is called, then it
   returns true, and decoding its bytes into a `jobRunState` and calling
   `applyTo` on a Job decoded from an older full record (State reserved, Pid
   0, Attempts 0, PeakRAM 900, StdErrC `old`) gives a Job whose `db.encode`
   equals `db.encode` of the example job.
3. Given that record, when `runStateOver` is called with `live` changed in
   one byte, then it returns nil, false.
4. Given a record shorter than 4 bytes, when `runStateOver` is called, then
   it returns nil, false.
5. Given a `jobRunState` with every field zero, when it is applied to the
   example job, then the job's Host is "", Pid 0, Attempts 0, StartTime zero
   and ReservedBy the zero UUID. Zero values are encoded and applied.

### A2: Reserve and start write run-state records

As the manager, I want `persistReservation` and `handleStart` to queue the
run-state record instead of the full job, keeping their existing waits.

`persistReservation` calls `updateJobRunStateDurableWithin(job,
ReserveWriteWait)`. `handleStart` calls `updateJobRunStateDurable(job)`,
including for an accepted duplicate start. Error handling, warnings and
replies are unchanged.

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
3. `TestReserveDurability`, `TestReserveDurabilityDeadRunner`,
   `TestReserveDurabilityStalledWrite`, `TestStartDurability` and
   `TestStartDurabilityAbortedWriteIsNotCommitted` pass unchanged.
4. Given `BenchmarkUpdateJobRunState` (new, in `jobqueue/db_bench_test.go`:
   `BenchmarkUpdateJobState`'s shape, jobs with 10,000-byte Cmds, alternating
   reserved and running through `queueJobRunState`) and
   `BenchmarkUpdateJobState` run on the same 10,000-byte-Cmd jobs, when both
   run with `make bench BENCH=UpdateJob`, then the run-state benchmark's
   `bolt_pages/job` is at most half the full-change benchmark's.

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
`storedLiveJobState` reads through the overlay.

**Acceptance tests:**

1. release, reservation: recovered State reserved, run-state record present,
   stored stderr `failed run's stderr`.
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
run-state record in one write transaction. If it deleted any, it logs
`clog.Warn(ctx, "recovering: dropped stale job run-state records",
"count", n)`.

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
3. Given no stale or orphaned records, when `recoverIncompleteJobs` runs, then
   it commits nothing (bolt's `Stats().TxStats.GetWrite()` is unchanged)
   and logs no `dropped stale` line.

## C: Schema version

### C1: Open refuses unsupported databases and stamps version 2

As an operator, I want the manager to refuse a database it cannot read
correctly, without changing it, and to tell me what to do.

In `initDB`, once an open has succeeded (including after a restore from
backup) and before any write transaction:

1. Read the version with `dbFileSchemaVersion`.
2. If `checkDBSchemaVersion` fails, close the bolt handle and return its
   error.
3. If the file existed before this open (`openedExistingDB`) and the version
   is 0, close and return the version-0 error.
4. In the existing bucket-creation `Update`, create `bucketJobRunState` and
   stamp `currentDBSchemaVersion` if the database is new or its version is
   below it.

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
6. Given a missing database file and an unversioned backup, when `initDB`
   runs, then it returns the version-0 error, the database file now exists,
   and after `CompactDBFileStats(dbFile)` a second `initDB` succeeds at
   version 2.
7. `TestDBSchemaVersionOnOpen`'s "leaves an existing unversioned database
   unstamped" case is replaced by test 5. `reliable2_dbcompat_test.go`
   copies `db.golden` to a temp dir and compacts the copy before starting a
   server on it, and passes. Any other test that builds an unversioned
   database and opens it with `initDB` stamps it first.

### C2: Compact stamps the current version

As an operator, I want `wr manager compact` to bring any supported database
to the current version, stripping output only once.

`compactBolt`: run `checkDBSchemaVersion` on the source and return its error
before creating anything. At version 1 or above, `bolt.Compact` as now, then
stamp `currentDBSchemaVersion` in the destination. Below version 1,
`compactStrippingStd`, whose `copyAll` stamps `currentDBSchemaVersion`.
`CompactDBFileStats` removes its temp file on that error, as on any other.

**Package:** `jobqueue/`
**File:** `jobqueue/db.go`, `jobqueue/db_compact.go`
**Test file:** `jobqueue/db_compact_std_test.go`

**Acceptance tests:**

1. Given an unversioned database whose complete records hold output, when
   compacted, then `OutputStripped` is true and the version is 2.
2. Given a version-1 database with a `bucketJobRunState` record, when
   compacted, then `compactStdDecodeObserver` is never called, the version is
   2, and the run-state record is byte-equal to before.
3. Given a version-2 database, when compacted, then the observer is never
   called and the version is 2.
4. Given a database stamped 3, when compacted, then the error satisfies
   `errors.Is(err, errDBSchemaTooNew)`, the file's SHA-256 is unchanged, and
   no `*.compact-*` file is left in its directory.

## D: Observability

### D1: Cumulative non-durable hand-out count

As an operator, I want the log to hold how many reservations were handed out
before they were durable, so that a soak or incident can measure exposure
exactly, not from a rate-limited warning.

- `db.reservesNotDurable atomic.Uint64`, incremented by
  `func (db *db) noteReserveNotDurable()`.
- `persistReservation` calls it on `errDurableWriteWaitExpired` and on any
  other error except `errDBClosed`. The rate-limited warning and error log
  stay.
- `reportArchiveFold` adds `"reservesNotDurable", <cumulative since this
  manager started>` to the `archive fold` line. It also logs the line when no
  archive transaction happened in the interval, if the count changed since
  the last line. Such a line has `txs=0` and zero means, never NaN.
  `stopArchiveFoldReporter`'s final line carries the count too.

**Package:** `jobqueue/`
**File:** `jobqueue/archivefold.go`, `jobqueue/serverCLI.go`, `jobqueue/db.go`
**Test file:** `jobqueue/reliable4_archive_fold_log_test.go`

**Acceptance tests:**

1. Given `arFoldInterval` 100ms, `ReserveWriteWait` 300ms and bolt's write
   transaction held for 5s (as in `TestReserveDurabilityStalledWrite`), when
   2 jobs are reserved and no archive runs, then an `archive fold` line with
   `txs=0` and `reservesNotDurable=2` is logged within 1s of the second
   hand-out, and `reservation not yet recorded on disk` is still logged.
2. Given writes that commit promptly, when 3 jobs are reserved and 1 is
   archived, then the next `archive fold` line has `reservesNotDurable=0`.
3. Given no archives and no non-durable hand-outs in an interval, then no
   `archive fold` line is logged for it.

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

### E2: statinspect clearlive empties run-state records

`clearLive` also deletes every key of `jobRunState`, if that bucket exists,
in the same transaction, and prints `jobRunState keys: before=%d after=%d`
after the `jobslive` line.

**Package:** `main` (module `statinspect`)
**File:** `.docs/reliable2/harness/statinspect/main.go`
**Test file:** none; manual check.

**Acceptance tests:**

1. Given a copy of a stopped soak database with N > 0 run-state records,
   when `statinspect clearlive <copy>` runs, then it prints `jobRunState
   keys: before=N after=0`.
2. Given a version-1 database copy, when it runs, then it prints only the
   `jobslive` line and exits 0.

### E3: CHANGELOG and compact help

**File:** `CHANGELOG.md`, `cmd/manager.go`

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
  fewer run twice if the manager crashes. The manager's `archive fold` log
  line now includes `reservesNotDurable`, how many commands have been handed
  out before their reservation was on disk since the manager started.
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

1. `wr manager compact --help` prints the paragraph above.
2. `CHANGELOG.md` has the three entries in the positions given.

## F: Gates

### F1: Local gates

With all `OS_*` unset and `GOCACHE` off the home directory:

1. `make lint`, `make test` and `CGO_ENABLED=1 make race` pass.
2. `make speed` against `SPEED_BASE` = the develop merge-base reports no
   worsening over its threshold. The PR body summarises its verdict and A2
   test 4's two `bolt_pages/job` figures.

### F2: wrdev crash and recovery modes

Each passes on this tree, run in sequence on an isolated manager:

1. `wrdev.sh crash-recovery` prints `PASS: re-sent archive accepted
   (complete=1), command ran exactly once` and exits 0.
2. `wrdev.sh add-storm-lsf` (defaults, on a copy of an `add-storm-fixture`
   fixture) exits 0. Its `## VERDICT` lines show `recoveredIncomplete` equal
   to `manifestIncomplete` and `unsafeCommands=0`.
3. `wrdev.sh dep-granularity-check` (defaults) exits 0.

### F3: Production-scale LSF crash soak

Run `developers/soak/run.sh` with battery10's `soak-go.sh` settings and
injectors (`HOURS=3`, `SCALE=1`, `RESTART_KINDS` with 6 or more crashes,
`USE_FUSE=1` with the 180s FUSE commit stall, `RAMP0=600
RAMP="0:600 8:1200 16:2100 25:3000 40:3600 60:4000"`, fixture `fix120k`, the
`crashon.sh`, `relbury.sh`, `rundep.sh` and `stopstate.sh` injectors), with
this tree's binary for the whole run (never replaced mid-run). Analyse with
the README's analysis steps, `dbstart` built from this tree.

Definitions:

- Stall window: each injected FUSE commit stall or `crashon.sh stall`, from
  its start to its end plus `ReserveWriteWait`.
- Non-durable outside stalls: summed over manager runs, the increase in
  `reservesNotDurable` across `archive fold` lines, leaving out each line
  whose interval overlaps a stall window. Each manager run is counted from 0,
  up to its last line before it stopped.

Pass only if all hold:

1. Peak LSF RUN (`lsf.tsv`) is at least 5,500.
2. Non-durable outside stalls is at most 0.034% of `markers.py`'s run count.
3. Every double run in `markers.py` is classified by `doubles.py` and
   `anyway.py` as having its first reservation inside a stall window; any
   other is a failure.
4. No lost jobs: `relburycheck.py` problems 0, `runnerlogs.py` finds no run
   whose final report was acknowledged and that then ran again, and every
   key the soak added is in `dbstart` output as complete, buried or live.
5. `rundepcheck.py` has no CHECK outside the owner-ruled "stop means buried"
   cases.

The PR body records the run directory, peak RUN, run count, the non-durable
counts inside and outside stalls (against battery10's 23,586), double runs by
class, and the pass verdict.

## Implementation Order

1. **Phase 1: schema (C1, C2).** Independent. Can run in parallel with
   phase 2.
2. **Phase 2: record and cleanup (A1, A4, A5).** Adds the bucket, record,
   helpers and every full-write and delete path. Reserve and start still
   write full records, so behaviour is unchanged.
3. **Phase 3: switch the writes and overlay (A2, A3, B1, B2).** Depends on
   phases 1 and 2. A2 must not land without B1: without the overlay a crash
   would recover pre-reservation state.
4. **Phase 4: counter (D1).** After phase 3.
5. **Phase 5: tools and docs (E1, E2, E3).** E1 and E2 depend on phase 2's
   format. E3 after phases 1 and 3.
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
  of an older full record reproduces the reserved or started job (A1 test 2,
  A2 test 1). A field changed by any other path is persisted by that path's
  own full write, as today. `runID` and other unexported fields were never
  persisted.
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
  open databases read-only (or only clear the live bucket), so they skip
  non-matching records and delete nothing.
- **No fold-back at stop, no downgrade.** As the owner decided. The CHANGELOG
  says so. The schema-version check protects later downgrades to this version
  or newer. It cannot protect a downgrade to v0.38.0, which does not check.
- **Compact refuses a newer database.** Stamping 2 onto a version-3 database
  would make it claim less than it holds.
- **Version-0 refusal is in `initDB`, before any write.** All opens,
  including restore from backup, go through it, so one check covers them, and
  a refused database is left unchanged for `wr manager compact`.
- **Counter on the `archive fold` line.** That line is already the
  per-minute summary operators grep. Logging it when only the counter
  changed covers a commit stall, when no archive commits. Hand-outs in the
  last interval before a crash are not in the log. The rate-limited warning
  still marks them.
- **Testing.** GoConvey for new tests, following go-implementor and
  go-reviewer. Crash behaviour is tested by killing a server without a clean
  stop and starting another on the same file, as the existing durability
  tests do. Existing ordering and durability tests stay and must pass
  unchanged except where a story says they are extended.
