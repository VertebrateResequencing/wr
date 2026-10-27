# Customdb Storage Engine Specification

## Overview

Replace the manager's bbolt database with a purpose-built store: one
append-only, group-committed log of small typed records for live jobs, the
live state in memory as today, periodic checkpoints, and completed jobs folded
in the background into immutable, zstd-compressed history segments with
purpose-built indexes. The owner accepted this design (D2) on 2026-10-27;
measurements and rationale are in [README.md](README.md),
[requirements.md](requirements.md), [d2-wal.md](d2-wal.md),
[benchmarks.md](benchmarks.md) and
[phase34-compression.md](phase34-compression.md). The `db` surface and its
leaks are in [phase0-prep.md](phase0-prep.md); the throwaway prototype in
[proto/](proto/) is the reference for framing, group commit, the crash test
and compression.

Three phases ship as one PR from branch `customdb-f397b547` (base `develop`):

- Phase 0 puts a `store` interface between the server and bbolt, adds a test
  inspection interface, per-writer stats, a kill -9 crash test and a
  record/replay harness. No behaviour changes.
- Phase 1 implements the live tier on the log (s2-compressed add blocks,
  checkpoints, recovery) with history still in bbolt, filled by a background
  folder that also fills the learned-requirement stats.
- Phase 2 replaces bbolt history with immutable segments and indexes, ships
  backups incrementally, and deletes bbolt.

User-facing behaviour does not change: same commands, flags, outputs, status
answers and config file (minus two removed knobs). wr never special-cases
where the database lives and never requires a fast local disk (D5 rejected).
D6 is out. D3 (lazy specs) is later, so the format lets every live job's spec
be addressed by file and offset.

## Architecture

### Packages and files

- `jobqueue/wal/` (new package, no jobqueue imports): framing, scanning,
  group-commit writer, block codecs, file naming.
  - `frame.go`, `scan.go`, `writer.go`, `codec.go`, `files.go`, tests beside
    them, `scan_fuzz_test.go`.
- `jobqueue/` (job-aware parts; they need `*Job`):
  - `store.go`: the `store` interface, `storeConfig`, `openStore`.
  - `store_bolt.go`: bbolt-only glue for `*db` (Phase 0-1; deleted in
    Phase 2 with `db.go`, `db_runstate.go`, `writeturns.go`,
    `archivefold.go`, `db_compact.go`, `db_prefetch*.go`, `db_schema.go`,
    `db_backupsync_*.go`). Phase 2 also takes bbolt out of
    `dependency.go` (`txDepReader` and `resolveDependencyChunk` go; the log
    store resolves through its own `depReader` over its in-memory dep-group
    sets) and `reqstat_cache.go` (`putStat` goes; the folder calls
    `invalidate`). `running_dependent.go` loses bbolt in Phase 0 (A1).
  - `store_stats.go`: per-writer and wait-distribution stats.
  - `store_flat.go`: the flat spec and run-state codec.
  - `store_log.go`: `logStore`, its write path and in-memory indexes.
  - `store_records.go`: record types and their encoding.
  - `store_recover.go`, `store_checkpoint.go`, `store_fold.go`.
  - `store_history_bolt.go` (Phase 1 only), `store_history.go` and
    `store_segment.go` (Phase 2).
  - `store_open.go`: location, lock, version, restore, dev wipe.
  - `store_convert.go`: bbolt to store converter.
  - `store_backup.go`: incremental shipping, `backup -p` archive.
  - `store_export.go`: exported read-only API for developer tools.
  - `store_record_test.go` (record/replay), `store_crash_test.go`,
    `store_inspect_test.go`.
- `internal/db_upgrade_status.go`: new conversion phase.
- `internal/config.go`, `cmd/conf.go`, `cmd/manager.go`, `cmd/cloud.go`.
- `developers/soak/dbstart/` rewritten; `developers/soak/soakgate.py`
  checks `notDurable` (Phase 1 gate); `developers/soak/bboltexp/` and
  `.docs/reliable2/harness/statinspect/` deleted (Phase 2).

### The store interface

`Server.db` changes type from `*db` to `store`. `openStore` replaces the
`initDB` call in `Serve`. The interface is the 43 methods phase0-prep.md
lists plus the replacements for its leaks, 49 in all, closed as follows:

- Change-ahead stays as four methods taking an opaque `*jobChangeAhead`. A
  store may return nil from `prepareJobChange`; `queueJobChangeAhead` with
  nil records the change afresh, as it does today.
- `s.db.reqStats.changes`/`.interval` and `db.recSecRound`/`recMBRound`
  become `reqStatChanges`, `setReqStatRefreshInterval` and `setRecRounding`.
- `s.db.rerunGuards.register/release` become `registerRerunGuard` and
  `releaseRerunGuard`. `rerunGuard.keeps`, `rerunGuards.archiving` and
  `Job.archiveOutcome` take `addStored func(member []byte) bool` instead of
  `*bolt.Bucket`. Ordering
  promise: an archive recorded after a guard registered sees it, and
  `addStored` is true only if the add's first job was recorded before the
  archive.
- `updateJobRunStateDurableWithin`, `logReserveNotDurable` and
  `setBatchTuning` stay in Phase 0 and are removed in Phase 1 (C3).
- Writer turns, folding, best-effort batching, backup pacing, prefetch and
  freelist handling stay inside `*db`.

```go
type store interface {
	recoverIncompleteJobs() ([]*Job, runStateRecovery, error)
	resolveDependencies(ctx context.Context, jobs []*Job,
		groups depGroupState) ([]resolvedJob, error)
	close(ctx context.Context) error
	backup(w io.Writer) error
	upgradedOnOpen() bool

	storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
		guard *rerunGuard) (storedNewJobs, error)
	registerRerunGuard() *rerunGuard
	releaseRerunGuard(g *rerunGuard)
	storeEnv(env []byte) (string, error)
	storeLimitGroups(groups map[string]*limiter.GroupData) (changed,
		removed []string, err error)
	storeRepGroupLookups(ctx context.Context, jobs []*Job) error
	checkIfLive(key string) (bool, error)
	checkIfComplete(key string) (bool, error)

	updateJobRunStateDurable(job *Job) error
	updateJobRunStateDurableWithin(job *Job, wait time.Duration) error // Phase 0
	logReserveNotDurable(key string)                                   // Phase 0
	updateJobAfterChangeDurable(job *Job) error
	updateJobAfterExit(ctx context.Context, job *Job, stdo, stde []byte,
		forceStorage bool)
	updateJobAfterExitDurable(job *Job, stdo, stde []byte,
		forceStorage bool) error
	expectJobExitUpdate() func()
	archiveCompletion(key string, job *Job) (archiveOutcome, error)
	prepareJobChange(ctx context.Context, job *Job,
		change func(*Job)) *jobChangeAhead
	queueJobChangeAhead(ctx context.Context, job *Job, ahead *jobChangeAhead,
		change func(*Job))
	moveJobChangeAhead(from, to *Job, ahead *jobChangeAhead) *jobChangeAhead
	discardJobChangeAhead(job *Job, ahead *jobChangeAhead)

	deleteLiveJobs(ctx context.Context, keys []string) error
	modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error
	storeLiveForRerun(jobs []*Job) error
	storeRunningRerunMarks(jobs []*Job) error

	retrieveCompleteJobsByKeys(keys []string) ([]*Job, error)
	retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error)
	retrieveOldestCompleteJobsByRepGroup(repgroup string,
		query completeJobsQuery) (*completeJobsPage, error)
	retrieveCompleteJobStatusByRepGroup(repgroup string,
		includeDetails bool) (*RepGroupStatus, error)
	retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error)
	retrieveLastCompletionTimeByRepGroup(
		repGroups []string) (map[string]time.Time, error)
	repGroupHasHistory(repGroup string) bool
	retrieveRepGroups() ([]string, error)
	spendArchivedBytesByRepGroup(repgroup string,
		budget *archivedBytesBudget) error
	retrieveJobStd(ctx context.Context, jobkey string) (stdo, stde []byte)

	retrieveEnv(ctx context.Context, envkey string) []byte
	retrieveStoredLimits(groups []string) ([]int64, error)

	cachedReqGroupRecs(reqGroup string) (*scheduler.Requirements, bool)
	refreshReqGroupRecs(reqGroup string) *scheduler.Requirements
	refreshReqGroupRecsLater(reqGroup string)
	warmReqGroupRecs(jobs []*Job)
	reqStatChanges() uint64
	setReqStatRefreshInterval(d time.Duration)
	setRecRounding(secRound, mbRound int)
	setBatchTuning(delay time.Duration, size int) // Phase 0
}

type storeKind uint8

const (
	storeKindLog  storeKind = iota // default from Phase 1
	storeKindBolt                  // Phase 0-1 only
)

type storeConfig struct {
	dbFile, dbBkFile, deployment string
	wipeDevDB, forceBackups      bool
	kind                         storeKind
}

func openStore(ctx context.Context, cfg storeConfig) (store, string, error)
```

`ServerConfig` gains unexported `storeKind`, set only by tests. In Phase 0
`openStore` always opens bbolt.

### Log format (`jobqueue/wal`)

Frame: `[u32 lenFlags][u32 crc32c(payload)][payload]`, little endian, CRC
Castagnoli. Bit 31 of `lenFlags` marks a block; length is the low 31 bits
and at most 1GiB. A block's payload is `[u8 codec][compressed bytes]`
(codec 1 = s2, 2 = zstd) whose decompressed bytes are a sequence of plain
frames. A block is all or nothing: a block that fails CRC, decompression or
inner framing ends the good data at the block's start, as a torn frame does.

```go
const (
	FrameHeader   = 8
	maxFrameLen   = 1 << 30
	CodecS2  byte = 1
	CodecZstd byte = 2
)

// Ref addresses one record: the file number, the offset of its outer frame,
// and its index inside that frame (0 for a plain frame). D3 reads a spec
// through a Ref without a format change.
type Ref struct {
	File uint32
	Off  uint64
	Idx  uint32
}

func AppendFrame(dst, rec []byte) []byte
func AppendBlock(dst []byte, codec byte, frames []byte) ([]byte, error)
func Scan(r io.Reader, fn func(ref Ref, rec []byte) error) (end int64,
	err error) // ref.File is 0; callers set it
func ReadAt(f io.ReaderAt, ref Ref) ([]byte, error)

type Commit struct{ /* durability channel + error */ }

func (c Commit) Wait() error
func (c Commit) WaitWithin(d time.Duration) (done bool, err error)

type Writer struct{ /* one goroutine, one open segment */ }

func NewWriter(dir string, file uint32, segmentSize int64) (*Writer, error)
func (w *Writer) Append(rec []byte) (Ref, Commit)
func (w *Writer) AppendBlock(block []byte, n int) (Ref, Commit) // pre-built
func (w *Writer) Rotate() (sealed uint32, err error)
func (w *Writer) Durable() (file uint32, off int64)
func (w *Writer) Stats() WriterStats
func (w *Writer) Close() error
```

`Append` and `AppendBlock` assign the record's Ref under the writer's mutex,
so Ref order is log order. The writer goroutine takes everything queued,
issues one `write()` and one `fdatasync`, then closes the batch's channel; no
lock is held across the sync (as `proto/internal/gc`). A write or sync error
fails that batch's commits and every later one with that error; the writer
does not retry. Segments rotate at `walSegmentSize` (256MiB) on a batch
boundary; rotation fsyncs the old file and the directory.

Files in the store directory, `NNNNNNNN` being an 8-hex-digit file number
from one counter (max existing + 1 at open):

| Name | What |
| --- | --- |
| `VERSION` | `wr-store 1\n` |
| `LOCK` | flock'd exclusive lock file |
| `NNNNNNNN.wal` | log segment |
| `NNNNNNNN.ckpt` | checkpoint |
| `NNNNNNNN.hist` | sealed history segment (Phase 2) |
| `history.bolt` | bbolt history (Phase 1 only) |
| `*.tmp` | in-progress output, deleted at open |

### Records

Every record payload starts `[u8 type][u64 seq]`. `seq` comes from one
store-wide atomic counter, taken while the job's read lock is held for the
snapshot being encoded, so for one key a higher seq is a later snapshot.
Replay applies a record for a key only if its seq is higher than the last
applied for that key. Keys are 16 binary bytes (wr's 32-hex md5).

| Type | Name | Body |
| --- | --- | --- |
| 1 | spec | key, flat spec |
| 2 | state | key, flat run state |
| 3 | delete | key |
| 4 | modify | old key, new key (same block: new spec, new state) |
| 5 | std | key, u8 out/err, bytes (as `stdo`/`stde` today; empty clears) |
| 6 | env | env key string, blob |
| 7 | limit | group, i64 limit, or removed flag |
| 8 | rglookup | rep group, key (`storeRepGroupLookups`) |
| 9 | histdrop | key (a completed job made live again) |

- Flat spec: every field `encodeJob` persists today that `jobRunState` does
  not. Strings and string slices are length-prefixed; Dependencies,
  Behaviours, MountConfigs and any `Requirements.Other` map may be an
  embedded Binc blob, present only when non-empty (as
  `proto/internal/flat`). First byte is the spec codec version (1).
- Flat run state: every `jobRunState` field, variable length, first byte
  version (1). An archive is a state record with State `complete`.
- Every multi-record operation (an add, a modify, a rerun put-back, an exit
  with std) is one s2 block, built and compressed by the calling goroutine
  before `AppendBlock`. Single state records are plain frames; the writer
  never compresses.
- Std follows today's lifecycle (`updateStd`, `recordCompleteTx`,
  `deleteOldLiveJobs`/`jobStd.restore`). Every exit's block carries a std
  record per stream: the new bytes if the exit failed or storage is forced,
  else empty, which clears earlier std. Replay drops a key's std on a
  complete state record or a delete record, and moves it from old key to new
  key on a modify record.

### Write path

- Durable waits are `Commit.Wait` on the record's batch. The rules for what
  waits are today's (requirements.md "Write kinds"): adds, start, archive,
  release, bury, delete, modify, limits, rerun marks wait; best-effort
  changes do not.
- Reservation (C3): wait at most `reserveDurableWait()`
  (`ClientMinRequestTimeout/4`); on timeout, return the job to the ready
  queue, record a release state record (not waited for) and answer "no job".
  Log order puts the release after the reserve, so recovery never sees a
  job handed out before its reservation is durable.
- `beSeq`, run-state overlays, change-versus-exit skipping and writer turns
  do not exist in `logStore`. `expectJobExitUpdate` keeps its contract:
  `retrieveJobStd` waits for exit updates expected or in flight.
- A read through a Ref whose batch is not yet written waits for that
  batch's commit.

### In-memory state of `logStore`

Per live key: last applied seq, Ref of its spec record, Ref of its last state
record, its rep group, dep groups and reverse dep groups (index membership
only). Plus: envs (blob by key), limits, dep groups ever seen, std Refs by
key, learned-requirement samples per req group (sorted slices),
history key index, history overlay (completions not yet folded: key to spec
Ref and state Ref). Live `*Job` values are never held by the store; methods
that return live jobs decode them through their Refs.

### Recovery

1. Take the lock, check `VERSION`, delete `*.tmp`.
2. Load the newest checkpoint with a valid trailer, decoding blocks with 8
   concurrent readers.
3. Replay every wal file from the checkpoint's `walFrom` in file order. A
   torn or corrupt tail of the last wal file is truncated there (logged at
   warn with the byte count). Damage in any earlier wal file, a missing wal
   file number, or no valid checkpoint is `errStoreCorrupt`.
4. Open history; rebuild the overlay from completions after the history's
   folded position.
5. Return live jobs (spec + latest state), as `recoverIncompleteJobs` does.

### Checkpoints

Every `checkpointInterval` (5 min) or `checkpointLogBytes` (256MiB of wal
since the last), and never sooner after the previous one ended than it took:

1. Rotate the writer; the sealed file number + 1 is the cut `walFrom`.
2. Wait until the folder's position is at or past the cut.
3. Merge the previous checkpoint with wal files before the cut using the
   recovery reducer, writing `NNNNNNNN.ckpt.tmp`: a header frame (`walFrom`,
   max seq, counts), zstd blocks of at most 64KiB uncompressed holding each
   live job's spec and state records, each live key's non-empty std, and
   every env, limit, dep-group and stat record, then a trailer frame (block
   offsets, magic `WRCKPT01`).
4. fsync, rename to `.ckpt`, fsync the directory, then swap every in-memory
   Ref (spec, last state, std) that points into a dropped file for its copy
   in the new checkpoint, then delete older checkpoints and wal files below
   the cut (each unlinked only when no reader holds it).

A checkpoint is a pure function of files on disk, so it is a consistent cut
whatever the hot path does meanwhile.

### Folder and history

The folder is one background goroutine, never on a reply path. At least once
a second it reads the log from its position up to `Writer.Durable()` and
folds into history every completion (spec + final state), rglookup and
histdrop, and every new learned-requirement sample (from completions and
from releases with a resource fail reason, as `putJobStats` and
`updateFailStat` choose today). Folding is idempotent and resumable from a
log position.

- Phase 1: history is `history.bolt` with today's history buckets
  (`jobscomplete`, `repgroupToKey`, `repgroups`, `depgroupToKey`,
  `reverseDepgroupToKey`, `depgroups`, `endTimeToKey`, `repgroupEndTime`,
  `jobRAM`, `jobDisk`, `jobSecs`) holding completed jobs only, plus `meta`
  holding the folded position, written in the same transaction. Existing
  bbolt query code is reused with live-bucket checks replaced by the store's
  live set.
- Phase 2: history is sealed `.hist` segments (layout in D2). The open
  segment is written as `NNNNNNNN.hist.tmp` and sealed (footer, fsync,
  rename) at `histSegmentBytes` (256MiB uncompressed) or `histSegmentAge`
  (1h). The folded position durable across a crash is the last sealed
  segment's; the open segment is rebuilt from the log at recovery.
- Wal files are deleted only when below both the checkpoint cut and the
  durable folded position.
- Until folded, a completion is in the overlay, and every history query
  answers from history plus overlay, so status straight after an archive is
  unchanged.
- `reqStatChanges` increments when the folder adds a distinct sample, which
  is where `reqStatCache` invalidation moves to.

### Location, lock, conversion, restore and backup

- `managerdbfile` (default `db`) is the store directory; `managerdbbkfile`
  is a backup directory or S3 prefix. No new config key. The upgrade sidecar
  stays at `dbFile + ".upgrade"`.
- `LOCK` is taken with `flock(LOCK_EX|LOCK_NB)`, retried until
  `managerDBOpenTimeout`, then `ErrDBLocked`.
- At open, `dbFile` may be: absent; a store directory; a bbolt file (bbolt
  meta magic `0xED0CDAED`) to convert; a self-contained backup (tar, "ustar"
  at offset 257) to restore. Anything else is "corrupt (?)".
- The self-contained backup is a tar of `VERSION`, the newest checkpoint,
  the wal files from its cut through a just-sealed one, and all history
  files (Phase 1: `history.bolt` via `tx.WriteTo`; Phase 2: sealed `.hist`).
  `backup(w)` writes it; `wr manager backup -p` stores it as one file.
- Incremental backup (production, or forced): every
  `minimumTimeBetweenBackups` (30s) at most, ship to the backup location, in
  this order: history files not yet shipped (Phase 1: `history.bolt` copied
  whole when changed; Phase 2: newly sealed `.hist`), new checkpoints, newly
  sealed wal files, then the durable prefix of the open wal file (to
  `NNNNNNNN.wal.tmp` then renamed, or uploaded over the object). Files the
  store has deleted are deleted from the backup only after a newer
  checkpoint has been shipped. History before log keeps the backup's log at
  or past its history's folded position.
- An old backup is a bbolt file at `dbBkFile` (default `db_bk`) or one S3
  object. Before the first incremental ship, if the backup path holds bbolt
  (meta magic), it is renamed to `dbBkFile + ".bolt-pre-" + v` (as E1;
  S3: copy then delete) and kept. Restore on a missing `dbFile` also accepts
  such a backup: it is copied or downloaded to `dbFile`, then converted (E1).

### Configuration and removals

- Phase 1 removes `managerdbbatchdelay`, `managerdbbatchsize`
  (`internal.Config`, `cmd/conf.go`, `cmd/manager.go`),
  `ServerTimings.DBBatchDelay`, `DBBatchSize`, `ReserveWriteWait`, and
  `serverReserveWriteWait*`. configor ignores unknown keys, so old config
  files load.
- Checkpoint, fold, segment and backup intervals and sizes are unexported
  package vars so tests can shorten them; nothing is user-settable.

### Errors

New sentinels in `jobqueue`: `errStoreCorrupt`, `errNotEnoughSpace`,
`errStoreVersionTooNew`. `ErrDBLocked` and `ErrDBUnreadable` keep their
meaning; `openErrorThatIsNotCorruption`'s errno list applies to the store
directory and its files too. A failed durable write returns its error to the
caller (as today); a writer error after `close` returns `errDBClosed`.

## A: Store interface and harnesses (Phase 0)

### A1: Store interface

As a wr developer, I want the server to reach storage only through `store`,
so that Phase 1 can supply a log implementation behind it.

`*db` implements `store` (`var _ store = (*db)(nil)`); `Server.db` is a
`store`; `rerunGuard`, `rerunGuards` and `archiveOutcome` take `addStored`
as above. No
behaviour changes.

**Package:** `jobqueue/`
**File:** `jobqueue/store.go`, `jobqueue/store_bolt.go`,
`jobqueue/running_dependent.go`, `jobqueue/server.go`,
`jobqueue/serverCLI.go`
**Test file:** `jobqueue/store_test.go`

**Acceptance tests:**

1. Given every non-test `.go` file in `jobqueue/` except `db.go`,
   `db_*.go`, `dependency.go`, `writeturns.go`, `reqstat_cache.go`,
   `archivefold.go` and `store_bolt.go`, when parsed
   with `go/parser`, then none imports `go.etcd.io/bbolt` and none refers to
   the identifier `db` as a type (count of violations is 0).
2. Given `rerunGuard` g with dependents `["k1"]` and member `"m"`, when
   `g.keeps(func([]byte) bool { return false }, "k1")` is called, then it
   returns false; with `addStored` returning true it returns true and
   `g.keptLive("k1")` is true.
3. Given the existing `jobqueue` suite, when run with `make test` and
   `make race`, then both pass with no test removed or skipped.

### A2: Test inspection interface

As a wr developer, I want tests to inspect stored state through a small
interface, so that the same tests run against bbolt and the log store.

Methods live in `_test.go` files on each implementation. The 46 test files
that call `db.bolt.View/Update/Batch` or take `*bolt.Tx` move onto it,
except tests of bbolt mechanics (freelist, remap, backup pacing, writer
turns, prefetch, compaction of bbolt), which stay bbolt-only and are deleted
in Phase 2. `forEachStore(t, fn)` runs `fn` once with the kind named by env
`WR_TEST_STORE` (`log` default from Phase 1, `bolt`), or both when it is
`all`.

```go
type recordKind uint8

const (
	kindLive recordKind = iota
	kindComplete
	kindStdOut
	kindStdErr
	kindEnv
	kindLimit
	kindStat
)

type storeInspector interface {
	liveJob(key string) (*Job, bool, error)     // latest durable spec+state
	completeJob(key string) (*Job, bool, error) // from history (+overlay)
	count(kind recordKind) (int, error)
	forceBackup() error // synchronous; returns after the backup is written
	flush() error       // waits until every queued write is durable
}
```

**Package:** `jobqueue/`
**File:** `jobqueue/store_inspect_test.go`
**Test file:** `jobqueue/store_inspect_test.go`

**Acceptance tests:**

1. Given a fresh store with 3 jobs added and 1 of them archived, when
   inspected, then `count(kindLive)` is 2, `count(kindComplete)` is 1, and
   `liveJob` of an archived key returns false.
2. Given `updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false)`
   for a job with Exitcode 1, when `flush()` returns, then
   `count(kindStdOut)` is 1 and `retrieveJobStd` returns `"o"` and `"e"`.
3. Given `grep -l 'db\.bolt\.\|\*bolt\.Tx'` over `jobqueue/*_test.go`, when
   run after the move, then only files named in a `boltOnlyTests` list in
   `store_inspect_test.go` match.

### A3: Per-writer stats

As an operator, I want a periodic log line per writer and per wait, so that
a burst can be attributed without a code change.

Follows `archivefold.go`: atomics only, no behaviour derived. Every
`archiveFoldReportInterval`, for each writer with activity, warn
`dbWriterStatsLogMsg = "db writer"` with `writer` (`newJobs`, `addChunk`,
`archive`, `bestEffort`, `batch`; Phase 1 adds `log` and `fold`), `txs`,
`items`,
`meanLock`, `maxLock` (waiting for turn and lock), `meanHold`, `maxHold`
(transaction including commit), `interval`; for `log`: `batches`,
`records`, `bytes`, `meanSync`, `maxSync`. Also warn
`reservationWaitLogMsg = "reservation wait"` (`n`, `p50`, `p99`, `max`,
`notDurable`) and `addWaitLogMsg = "add wait"` (`n`, `p50`, `p99`, `max`
over `storeNewJobsGuarded` calls). Lines with `n` or `txs` 0 are not
written.

```go
// waitHistogram is a lock-free log-linear histogram of durations.
type waitHistogram struct{ /* atomic buckets */ }

func (h *waitHistogram) observe(d time.Duration)
func (h *waitHistogram) take() waitSummary // resets

type waitSummary struct {
	n             uint64
	p50, p99, max time.Duration
}
```

**Package:** `jobqueue/`
**File:** `jobqueue/store_stats.go`
**Test file:** `jobqueue/store_stats_test.go`

**Acceptance tests:**

1. Given a histogram observing 1ms, 2ms, ..., 100ms once each, when taken,
   then `n` is 100, `max` is exactly 100ms, `p50` is in [45ms, 55ms] and
   `p99` is in [89ms, 110ms]; a second `take()` has `n` 0.
2. Given a server with the report interval at 50ms, when 3 adds and 5
   reservations are made, then within 500ms the captured log has one
   `"add wait"` line with `n=3` and one `"reservation wait"` line with
   `n=5`, and a `"db writer"` line with `writer=newJobs` (bbolt) or
   `writer=log` (log store).
3. Given an idle server for 3 intervals, when the log is read, then it has
   no `"db writer"`, `"add wait"` or `"reservation wait"` line.

### A4: Kill -9 crash test

As a wr developer, I want the prototype's crash test driving `store`, so
that every implementation must keep every acknowledged write.

The test re-executes its own binary as a child (pattern of
`TestOpenManagerBoltLimitChild`). The child opens the store, adds 300 jobs,
and runs one goroutine per job through 5 cycles of reserve
(`updateJobRunStateDurable`, State reserved), start (running) and release
(`updateJobAfterExitDurable`, exit 1, Attempts + 1), then reserve, start and
archive (`archiveCompletion`). After each call returns it appends
`key<TAB>attempts<TAB>step` to an ack file (fsynced). Steps order as
reserve < start < release < archive within an attempt count. The parent
sends SIGKILL to the child's exact pid after a random 1.5-4.5s, reopens the
store and checks. Rounds: env `WR_CRASHTEST_ROUNDS` (default 2), directory env
`WR_CRASHTEST_DIR` (default `t.TempDir()`), so the gate can run 16 rounds on
NFS. For the log store each round also makes 20 copies with the last wal
file truncated at a random offset or one random byte flipped, and recovers
each.

**Package:** `jobqueue/`
**File:** `jobqueue/store_crash_test.go`
**Test file:** `jobqueue/store_crash_test.go`

**Acceptance tests:**

1. Given a killed child, when the store is reopened, then for every acked
   (key, attempts, step) the recovered job is complete, or live with
   (Attempts, step implied by State) at or after the ack in that order;
   missing count is 0.
2. Given a damaged copy (log store), when recovered, then recovery returns
   no error, and every recovered job's state equals a state the child wrote
   for it (checked against a per-key list the child wrote before each
   append).
3. Given a store implementation with a fault that acknowledges before
   `Commit.Wait` (test-only wrapper `ackEarlyStore`), when 5 rounds run,
   then at least one round reports missing > 0 (the test can fail).

### A5: Record/replay harness

As a wr developer, I want every call across `store` recorded during a run
and replayable against any implementation, so that bbolt and the log store
can be shown to give the same answers.

`recordingStore` wraps a `store`. When env `WR_STORE_RECORD=<path>` is set
at `openStore`, the server's store is wrapped and each call is written, at
return, as one wal frame holding a Binc `recordedCall`. Time arguments
(`cutoff`) are recorded, never read from the clock at replay. `storeDigest`
hashes a canonical result: jobs sorted by key, with fields not persisted by
`encodeJob` zeroed, maps sorted. Replay runs calls serially in recorded
`End` order against a fresh store and reports a mismatch only for calls
that did not overlap a write call in the recording ([Start, End] seq
intervals).

```go
type recordedCall struct {
	Start, End uint64 // global call sequence at entry and at return
	Method     string
	Args       []byte // Binc of the arguments
	Digest     [32]byte
	Nanos      int64
	Write      bool
}

func replayRecording(ctx context.Context, path string,
	s store) (calls int, mismatches []replayMismatch, err error)

type replayMismatch struct {
	Seq    uint64
	Method string
}
```

`TestStoreReplay` runs `replayRecording` on `WR_STORE_REPLAY=<path>` against
the kind in `WR_TEST_STORE` and skips when the variable is unset; it is the
gate's runner, not an acceptance test of the harness.

**Package:** `jobqueue/`
**File:** `jobqueue/store_record.go`
**Test file:** `jobqueue/store_record_test.go`

**Acceptance tests:**

1. Given a scripted workload (add 100 jobs in 2 calls, reserve/start/archive
   50, delete 10, modify 5, status by rep group, recent completions with a
   fixed cutoff) recorded against bbolt, when replayed against a fresh bbolt
   store, then `calls` equals the recorded count and `mismatches` is empty.
2. Given that recording, when replayed against a store whose
   `retrieveCompleteJobsByKeys` drops its last result, then `mismatches` is
   non-empty and its first entry has Method
   `"retrieveCompleteJobsByKeys"`.
3. Given a recording whose last frame is truncated by 3 bytes, when
   replayed, then it replays every complete frame and returns no error.

## B: Log engine (Phase 1)

### B1: Framing and scanning

As a wr developer, I want framed, CRC-checked records whose scan stops at the
first damage, so that recovery always yields a prefix.

**Package:** `jobqueue/wal/`
**File:** `jobqueue/wal/frame.go`, `jobqueue/wal/scan.go`
**Test file:** `jobqueue/wal/scan_test.go`,
`jobqueue/wal/scan_fuzz_test.go`

**Acceptance tests:**

1. Given frames for records `"a"`, `"bb"`, `"ccc"`, when scanned, then fn
   sees 3 records in order with Refs `{0,0,0}`, `{0,9,0}`, `{0,19,0}` and
   `end` is 30.
2. Given an s2 block of 64 records followed by a plain record, when the file
   is cut at every byte offset, then each scan returns either 0, 64 or 65
   records, never a partial block, and `end` is the offset after the last
   whole frame.
3. Given a frame whose length field has bit 31 clear and value 2^30 + 1,
   when scanned, then scanning stops there with no error.
4. `FuzzScan`: for any byte input, `Scan` does not panic, `end <= len`, and
   for a valid log of N records with one mutated byte, the records returned
   are the first k original records unchanged, k <= N. Seeds: empty input,
   one frame, one block, a block with a bad codec byte, a length of
   0x7fffffff.

### B2: Group-commit writer

As a wr developer, I want one writer goroutine that makes each batch durable
with one write and one fdatasync, so that small records never wait behind
page rewrites.

**Package:** `jobqueue/wal/`
**File:** `jobqueue/wal/writer.go`
**Test file:** `jobqueue/wal/writer_test.go`

**Acceptance tests:**

1. Given 1,000 goroutines each appending one record and waiting, when all
   return, then `Stats().Records` is 1,000, `Stats().Syncs` is less than
   1,000, and a scan of the file finds 1,000 records.
2. Given a test sync hook that blocks the first sync for 200ms, when a
   record is appended, then `WaitWithin(50ms)` returns `(false, nil)` and
   `Wait()` returns nil after the hook releases.
3. Given a sync hook returning `EIO`, when two records are appended in
   separate batches, then both `Wait()` calls return an error wrapping
   `syscall.EIO`.
4. Given `segmentSize` 4KiB and 100 records of 100 bytes, when appended,
   then at least 2 `.wal` files exist, records never span files, and Refs
   name the file each record is in.

### B3: Block codecs

As a wr developer, I want s2 and zstd block helpers, so that adds compress in
their handler and history compresses per 64KiB block.

Uses `github.com/klauspost/compress` (already in `go.mod`; make it direct).
zstd uses `SpeedFastest` with no dictionary.

**Package:** `jobqueue/wal/`
**File:** `jobqueue/wal/codec.go`
**Test file:** `jobqueue/wal/codec_test.go`

**Acceptance tests:**

1. Given 64 frames of a repeated 1KiB command, when blocked with each codec
   and scanned, then all 64 records come back byte-equal and the block is
   smaller than 1/4 of the input.
2. Given a block whose codec byte is 9, when scanned, then scanning stops
   at the block with no error.

## C: Live tier on the log (Phase 1)

### C1: Flat codec

As a wr developer, I want a zero-allocation spec and run-state codec, so
that encoding is off the critical path.

```go
func appendFlatSpec(dst []byte, j *Job, ch codec.Handle) ([]byte, error)
func decodeFlatSpec(src []byte, j *Job, ch codec.Handle) error
func appendFlatRunState(dst []byte, rs *jobRunState) []byte
func decodeFlatRunState(src []byte, rs *jobRunState) error
```

**Package:** `jobqueue/`
**File:** `jobqueue/store_flat.go`
**Test file:** `jobqueue/store_flat_test.go`

**Acceptance tests:**

1. Given a Job with every field `encodeJob` persists set to a non-zero value
   (including Dependencies, Behaviours, MountConfigs and Requirements.Other),
   when spec and run state are flat-encoded and decoded into a fresh Job,
   then it equals `decodeJob(encodeJob(j))`.
2. Given a 10KiB-command Job and a reused destination Job, when decoded
   1,000 times, then allocations per decode are 0 for the spec without a
   blob (`testing.AllocsPerRun`).
3. `FuzzFlatSpec`: any bytes given to `decodeFlatSpec` return an error or
   a Job, never panic.
4. Given a spec whose version byte is 2, when decoded, then the error wraps
   `errStoreVersionTooNew`.

### C2: Adds, envs, limits and rep group lookups

As a user, I want `wr add` to be durable before it replies, as today, and
faster.

`storeNewJobsGuarded` builds one s2 block per call containing a spec and
state record per new job (and any rglookup, histdrop and put-back records
the add makes), appends it and waits. Dedup, dependency handling and the
260929 rule (a re-add never touches a queued or running job's record) are
unchanged. `storeEnv`, `storeLimitGroups`, `storeRepGroupLookups` append
their records and wait.

**Package:** `jobqueue/`
**File:** `jobqueue/store_log.go`, `jobqueue/store_records.go`
**Test file:** `jobqueue/store_log_add_test.go`

**Acceptance tests:**

1. Given a log store, when 1,000 jobs are added in one call, then the wal
   holds exactly one new outer frame, it is a block with codec s2, and it
   contains 1,000 spec records.
2. Given an added job that is running, when the same job is added again,
   then after reopen its state is still running with its ReservedBy.
3. Given an env blob stored twice, when reopened, then `count(kindEnv)` is
   1 and `retrieveEnv` returns the blob.
4. Given limit group `"g"` set to 5 then removed, when reopened, then
   `retrieveStoredLimits(["g"])` returns what it returns for a never-set
   group in bbolt (recorded by running the same steps under `bolt`).

### C3: Transitions and durable reservations

As a user, I want a job never handed to a runner before its reservation is
durable, even during a filesystem stall, so that a crash never runs it twice.

Each transition appends one state record. In `persistReservation`, the
server waits `reserveDurableWait()`; on timeout it does not hand out: it
releases the item to the ready sub-queue with no delay and no added attempt,
forgets the runner hold, sets State ready and clears ReservedBy, records the
change with `prepareJobChange`/`queueJobChangeAhead`, logs
`reserveAbandonedLogMsg` through `s.warns`, and answers with no job (as when
nothing is ready). On success `handOutReservation` restarts the TTR, as
today. `handOutReservation` checks the reservation's `Commit` with
`WaitWithin(0)`; if it is not done it still hands out but counts the job in
the `"reservation wait"` line's `notDurable` field (replacing
`logReserveNotDurable`'s count), so any non-durable hand-out is visible in
the manager log. Removed: `updateJobRunStateDurableWithin`,
`logReserveNotDurable`, `errDurableWriteWaitExpired`, `ReserveWriteWait`,
`setBatchTuning`.

```go
func reserveDurableWait() time.Duration // ClientMinRequestTimeout / 4
```

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/store_log.go`
**Test file:** `jobqueue/store_reserve_test.go`

**Acceptance tests:**

1. Given a healthy log store, when a runner reserves a job, then it gets
   the job and, after a crash-reopen, the job is reserved with that
   runner's ClientID as ReservedBy.
2. Given `ClientMinRequestTimeout` 800ms (bound 200ms) and a sync hook
   holding every sync for 2s, when a runner reserves, then within 400ms it
   gets no job, the item is in the ready sub-queue, Attempts is unchanged,
   and after the hold ends and the store is reopened the job is ready.
3. Given that held reservation, when the wal is scanned after the hold,
   then the job's reserve state record precedes its release state record
   and the release has the higher seq.
4. Given TTR 300ms and a sync hook delaying 150ms, when a reserved job is
   touched 250ms after hand-out, then it is not marked lost.
5. Given two state records for one key appended in reverse seq order
   (seq 7 with State buried, then seq 6 with State ready), when reopened,
   then the job is buried.
6. Given syncs held for 2s and a test hook making `persistReservation` skip
   its wait, when a runner reserves, then the next `"reservation wait"` line
   has `notDurable=1`; without the hook (test 2) it has `notDurable=0`.

### C4: Delete, modify and reruns

As a user, I want `wr remove` and `wr mod` to be durable and atomic, so
that a crash never resurrects or half-modifies a job.

Delete appends delete records (one block per call). Modify appends one block
per job: modify, new spec, new state. Put-backs (`storeLiveForRerun`) append
spec, state and histdrop in one block. Rerun marks are state records.

**Package:** `jobqueue/`
**File:** `jobqueue/store_log.go`
**Test file:** `jobqueue/store_log_ops_test.go`

**Acceptance tests:**

1. Given 3 live jobs, when 2 are deleted and the store reopened, then
   `count(kindLive)` is 1.
2. Given a modify of job k1 to key k2 whose block is truncated by 1 byte,
   when reopened, then k1 is live with its old spec and k2 does not exist.
3. Given a completed job put back live, when reopened, then `checkIfLive`
   is true and `checkIfComplete` is false.
4. Given a job that exited 1 with std `"o"`/`"e"`, when it then exits 0
   and the store is reopened, then `retrieveJobStd` returns empty and
   `count(kindStdOut)` is 0.
5. Given two jobs that exited 1 with std, when one is archived and the
   other deleted and the store reopened, then `count(kindStdOut)` and
   `count(kindStdErr)` are 0.
6. Given k1 exited 1 with std `"o"`/`"e"`, when modified to key k2, then
   `retrieveJobStd(k2)` returns `"o"`/`"e"` and `retrieveJobStd(k1)`
   returns empty, both before and after a checkpoint and reopen.

### C5: Recovery

As an operator, I want a restart to rebuild exactly the acknowledged state,
quickly, so that recovery neither loses nor resurrects work.

**Package:** `jobqueue/`
**File:** `jobqueue/store_recover.go`
**Test file:** `jobqueue/store_recover_test.go`

**Acceptance tests:**

1. Given 500 random operations (add, reserve, start, archive, release,
   delete, modify, kick) applied to a log store and to an in-memory model,
   with 3 checkpoints at random points, when reopened, then recovered live
   jobs equal the model's (key, State, Attempts, ReservedBy, spec fields).
2. Given the last wal file with 10 garbage bytes appended, when reopened,
   then recovery succeeds, the file is truncated to its last good frame,
   and a warn line names the 10 bytes dropped.
3. Given a byte flipped in a wal file that is not the last, when reopened,
   then `openStore` restores from backup if one exists, else returns an
   error wrapping `errStoreCorrupt`.
4. Given 100,000 live 1KiB jobs, when recovered, then the store's own heap
   growth (excluding returned jobs, measured after dropping them) is under
   40MB.

### C6: Checkpoints and spec addressing

As a wr developer, I want background checkpoints that bound recovery and
log size and keep every live spec addressable, so that D3 needs no format
change.

**Package:** `jobqueue/`
**File:** `jobqueue/store_checkpoint.go`
**Test file:** `jobqueue/store_checkpoint_test.go`

**Acceptance tests:**

1. Given a checkpoint after 2,000 jobs were added and 1,000 archived and
   folded, when it completes, then no wal file below its cut remains and
   reopening gives the same live set as before it.
2. Given a test hook that kills the checkpoint after writing `.ckpt.tmp`
   but before rename, when reopened, then the previous checkpoint is used,
   the `.tmp` is gone, and the live set equals the model.
3. Given 1,000 live jobs, 100 of them with std, and a checkpoint that
   drops every earlier wal file, when each live entry's spec, last state
   and std Ref is read with `wal.ReadAt`, then it decodes to that job's
   spec, state and std (1,000, 1,000 and 100 of each).
4. Given 4 goroutines writing continuously during 3 checkpoints, when
   reopened, then the live set equals the in-memory model.

### C7: Lock and version

As an operator, I want two managers never to share a store and an old store
version to be refused clearly.

**Package:** `jobqueue/`
**File:** `jobqueue/store_open.go`
**Test file:** `jobqueue/store_open_test.go`

**Acceptance tests:**

1. Given a store held open, when a second `openStore` runs with
   `managerDBOpenTimeout` 100ms, then it returns an error wrapping
   `ErrDBLocked` within 1s.
2. Given `VERSION` containing `wr-store 2`, when opened, then the error
   wraps `errStoreVersionTooNew` and says "use a newer wr"; no file changed.
3. Given an unreadable store directory (mode 0), when opened, then the error
   wraps `ErrDBUnreadable` and no restore is attempted.

## D: History and folding

### D1: Folder into bbolt history (Phase 1)

As a user, I want status, history and learned requirements to answer as
today while history is written in the background.

**Package:** `jobqueue/`
**File:** `jobqueue/store_fold.go`, `jobqueue/store_history_bolt.go`
**Test file:** `jobqueue/store_fold_test.go`

**Acceptance tests:**

1. Given the folder paused by a test hook, when a job of rep group `"rg"`
   is archived, then `checkIfComplete` is true,
   `retrieveCompleteJobsByKeys` returns it,
   `retrieveCompleteJobsRecent(before)` includes it,
   `retrieveLastCompletionTimeByRepGroup(["rg"])` is its EndTime, and
   `retrieveCompleteJobStatusByRepGroup("rg", false)` counts 1 complete.
2. Given that job, when the folder resumes and runs, then the same five
   answers are unchanged and the overlay is empty.
3. Given 3 completions in req group `"q"` with PeakRAM 100, 100, 200, when
   folded, then `reqStatChanges()` rose by 2 and
   `refreshReqGroupRecs("q")` equals what bbolt gives for the same three
   archives.
4. Given a fold killed after its bbolt transaction but before anything
   else, when reopened and folded again, then `count(kindComplete)` is
   unchanged (idempotent).
5. Given a fold hook that blocks the folder for 2s, when 100 archives are
   made meanwhile, then every `archiveCompletion` returns within 500ms and
   the `"db writer"` line has `writer=fold`.

### D2: History segments (Phase 2)

As an operator, I want completed jobs in immutable compressed segments, so
that history never rewrites pages and backup copies only new files.

Segment layout: zstd blocks of at most 64KiB uncompressed, each holding
completed-job records (key, flat spec, flat final state) and rglookup
entries; then a footer frame holding the block offsets, the folded position
it covers, and sorted sections equivalent to today's history buckets for the
jobs in it: key to Ref, rep group to keys, dep group to keys, reverse dep
group to keys, dep groups seen, end time to Ref, rep group end time, and new
learned-requirement samples; then a trailer (footer offset, CRC, magic
`WRHIST01`). In memory: the key index (key to Ref, about 50-100MB at 2.15M
jobs), rep group to segment numbers, dep group to segment numbers, each
segment's end-time range, the samples. Footer sections are read on demand
through a 64MiB LRU. A posting is valid only while the key index maps its key
to a record in history and the key is not live, so superseded and put-back
jobs drop out without rewriting segments.

**Package:** `jobqueue/`
**File:** `jobqueue/store_segment.go`, `jobqueue/store_history.go`
**Test file:** `jobqueue/store_segment_test.go`

**Acceptance tests:**

1. Given 10,000 completions folded with `histSegmentBytes` 1MiB, when
   sealed, then more than one `.hist` file exists, every block decompresses
   to at most 65,536 bytes, and every key is found by the key index.
2. Given a `.hist.tmp` left by a crash, when reopened, then it is deleted
   and its completions are refolded from the log (count unchanged).
3. Given a sealed segment with its trailer CRC damaged, when opened, then
   `openStore` treats the store as corrupt.
4. Given a job completed, put back, and completed again, when
   `retrieveCompleteJobsByRepGroup` runs, then it returns the job once,
   with the second run's EndTime.
5. Given 1,000,000 completed keys, when indexed, then heap growth of the key
   index is under 100MB.

### D3: History queries on segments (Phase 2)

As a user, I want every status and history answer to equal today's.

`spendArchivedBytesByRepGroup` prices each record at its uncompressed flat
record length.

**Package:** `jobqueue/`
**File:** `jobqueue/store_history.go`
**Test file:** `jobqueue/store_history_test.go`

**Acceptance tests:**

1. Given the A5 workload recording (made against bbolt), when replayed
   against the Phase 2 store, then `mismatches` is empty.
2. Given rep group `"big"` with 50,000 completions over 5 segments, when
   `retrieveCompleteJobStatusByRepGroup("big", false)` runs, then it reports
   50,000 complete and reads no record block (footer reads only, counted by
   a test counter).
3. Given `retrieveOldestCompleteJobsByRepGroup` with a limit of 10 on that
   group, when run, then it returns the 10 oldest by StartTime and counts
   50,000.

## E: Lifecycle (Phase 1, history parts Phase 2)

### E1: Automatic conversion

As an operator, I want my existing bbolt database converted on the first
`wr manager start`, with progress shown and the old file kept for rollback.

Steps, crash-safe: (1) check free space on `dbFile`'s filesystem is at least
the bbolt file's size, else `errNotEnoughSpace` naming needed and available
bytes; (2) create `dbFile + ".converting"` and write `SOURCE` naming
`dbFile + ".bolt-pre-" + v` (`v` = `ServerVersion`, or `dev` when empty),
fsync; (3) rename `dbFile` to that name; (4) build the store inside
`.converting` (Phase 1: checkpoint of live jobs with run-state overlays and
rerun marks applied, envs, limits, stats, std, and `history.bolt` from the
history buckets; Phase 2: `.hist` segments instead); (5) fsync, rename
`.converting` to `dbFile`, fsync the parent. At open, an existing
`.converting/SOURCE` resumes: clear the rest of `.converting` and redo from
the source (at `dbFile` or the pre name). Accepts bbolt schema 0, 1 and 2;
StdOutC/StdErrC of complete records are dropped. Progress uses
`internal.DBUpgradeConvertState = "convert database"` with Processed/Total
in jobs; `IsDBUpgradeState` returns true for it.

```go
func convertBoltDB(ctx context.Context, dbFile string,
	upgrade *dbUpgradeReporter) error
```

**Package:** `jobqueue/`, `internal/`
**File:** `jobqueue/store_convert.go`, `internal/db_upgrade_status.go`
**Test file:** `jobqueue/store_convert_test.go`

**Acceptance tests:**

1. Given a bbolt DB built by `*db` with live jobs (some with run-state
   records and rerun marks), completed jobs, std, envs, limits, stats,
   duplicate rep group lookups and dep groups, when the manager starts,
   then `dbFile` is a directory, `dbFile.bolt-pre-dev` has the original's
   sha256, and the A5 query set over all keys and rep groups gives equal
   digests for bbolt and the converted store.
2. Given a schema 0 bbolt DB with StdOutC on complete records, when
   converted, then no history record holds std and the completed count is
   unchanged; schema 1 and 2 DBs convert too.
3. Given a test hook crashing after each of steps 2, 3, 4 (mid-build) and
   before 5, when started again, then each yields the same store as an
   uninterrupted run and the bbolt file's sha256 is unchanged.
4. Given a free-space hook reporting 1 byte less than the bbolt size, when
   started, then the error wraps `errNotEnoughSpace`, `dbFile` is still the
   untouched bbolt file and no `.converting` exists.
5. Given a conversion in progress, when the sidecar is read, then State is
   `"convert database"`, Processed rises to Total, and
   `IsDBUpgradeState("convert database")` is true.

### E2: Open, restore and corruption

As an operator, I want the restore-from-backup behaviour of today for the
new store.

`openStore` order: dev wipe (E3); resume conversion; if `dbFile` is absent,
restore from backup (directory, S3 prefix, or self-contained file at the
backup path) into `dbFile + ".restoring"` then rename, or from an old bbolt
backup (file or object) then convert, or create an empty
store (`VERSION`, empty checkpoint); if a self-contained file, restore it the
same way and remove the file; if bbolt, convert; if a store, recover, and on
`errStoreCorrupt` with a backup, `os.RemoveAll(dbFile)` and restore. The
returned message strings keep today's wording with "db file" replaced by
"db".

**Package:** `jobqueue/`
**File:** `jobqueue/store_open.go`
**Test file:** `jobqueue/store_open_test.go`

**Acceptance tests:**

1. Given no `dbFile` and a backup directory, when opened, then the live and
   complete sets equal those at backup time and the message starts
   `"recreated missing db"`.
2. Given no `dbFile` and a self-contained file at the backup path, when
   opened, then the same holds.
3. Given `dbFile` is a self-contained file, when opened, then `dbFile`
   becomes a directory holding the restored store.
4. Given a corrupt store and a backup, when opened, then the store is
   restored and no file of the corrupt store remains.
5. Given an S3 backup location (fake `backupTarget`), when `dbFile` is
   missing, then every object under the prefix is downloaded and the store
   opens.
6. Given no `dbFile` and a bbolt file at the backup path, when opened, then
   `dbFile` is a converted store whose live and complete sets equal the
   bbolt file's, and the message starts `"recreated missing db"`.
7. Given the same with the bbolt file as one object in a fake S3 target,
   when opened, then the same holds.

### E3: Recursive deletes

As a developer, I want the dev wipe and restore paths to remove a directory
or S3 prefix, not fail silently on one.

Every `os.Remove(dbFile)`, `os.Remove(dbBkFile)` and
`accessor.DeleteFile(dbBkFile)` in the open path becomes `os.RemoveAll` or a
prefix delete through `backupTarget`.

```go
type backupTarget interface {
	list() ([]string, error)
	upload(local, name string) error
	download(name, local string) error
	rename(from, to string) error
	remove(name string) error
	removeAll() error
}
```

**Package:** `jobqueue/`
**File:** `jobqueue/store_open.go`, `jobqueue/store_backup.go`
**Test file:** `jobqueue/store_wipe_test.go`

**Acceptance tests:**

1. Given development deployment with `wipeDevDB` and a populated store
   directory and backup directory, when opened, then both are gone before
   an empty store is created (0 jobs).
2. Given the same with an S3 backup (fake target holding 5 objects), when
   opened, then the fake holds 0 objects.
3. Given a corrupt store restored from backup (E2 test 4), when listed,
   then no file from the corrupt store's directory remains.

### E4: Incremental backup

As an operator, I want backups that copy only new files, so that a backup
never stalls the manager or rewrites gigabytes.

**Package:** `jobqueue/`
**File:** `jobqueue/store_backup.go`
**Test file:** `jobqueue/store_backup_test.go`

**Acceptance tests:**

1. Given production deployment, two backup ticks with 100 archives between
   them, when the second tick ships, then it uploads only files new or
   changed since the first (counted by the fake target) and no sealed file
   twice.
2. Given a backup directory after a checkpoint deleted old wal files, when
   the next tick ships, then those files are deleted from the backup only
   after the new checkpoint is there.
3. Given a crash at a random point during shipping, when the backup
   directory is opened as a store, then it opens and every job in its
   history is complete and not live.
4. Given development deployment without `forceBackups`, when 3 ticks pass,
   then nothing is shipped.
5. Given a converted store and a bbolt file at `db_bk`, when the first tick
   ships, then `db_bk.bolt-pre-dev` has the old file's sha256, `db_bk` is a
   directory that opens as the store, and the same holds for a bbolt object
   in a fake S3 target.

### E5: Self-contained backup

As a user, I want `wr manager backup -p <file>` to still write one file
that a manager can start from.

**Package:** `jobqueue/`
**File:** `jobqueue/store_backup.go`
**Test file:** `jobqueue/store_backup_test.go`

**Acceptance tests:**

1. Given a running store, when `backup(w)` writes to a buffer while 4
   goroutines keep writing, then the bytes are a tar whose members are
   `VERSION`, one `.ckpt`, `.wal` files and history files, and opening it
   (E2) yields every job acknowledged before `backup` was called.
2. Given `Client.BackupDB(path)`, when called, then `path` is one regular
   file and starting a manager with it as `dbFile` restores it (E2 test 3).

### E6: Compact

As an operator, I want `wr manager compact` to tidy the new store offline.

Phase 2. With the lock held: recover, write a checkpoint, delete covered wal
files, merge `.hist` segments smaller than `histSegmentBytes` into new ones
(write, fsync, rename, then delete inputs). `CompactStats` before/after are
the directory's total bytes; unreadable records are counted as today.
Refusal while a manager runs is unchanged. On an unconverted bbolt file,
compact first runs `convertBoltDB` (same steps, free-space check and crash
safety as E1), then compacts the new store, so no older wr is needed. In
Phase 1 compact on a store directory returns an error saying it is not
supported yet (unreleased).

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/store_compact.go`, `cmd/manager.go`
**Test file:** `jobqueue/store_compact_test.go`

**Acceptance tests:**

1. Given a store with 10 small segments and 5 wal files, when compacted,
   then 1 segment and 1 wal file remain, and every query answer is
   unchanged (A5 digest compare).
2. Given a compaction killed after writing the merged segment but before
   deleting inputs, when opened, then each key is found once.
3. Given a manager holding the lock, when `CompactDBFileStats` runs, then
   it returns an error wrapping `ErrDBLocked`.
4. Given a bbolt file at `dbFile` and no manager, when `CompactDBFileStats`
   runs, then `dbFile` is a store directory, `dbFile.bolt-pre-dev` has the
   original's sha256, and the A5 query set gives equal digests for the
   bbolt file and the compacted store.

### E7: Cloud deploy and teardown

As a cloud user, I want deploy and teardown to keep carrying my database.

- `bootstrapDBBackupLocation` uploads a self-contained file made by
  `jobqueue.ExportStore` to the remote's `.wr_<deployment>/db` (restored on
  the remote's first start, E2 test 3). `ExportStore` takes the store lock
  and fails with `ErrDBLocked` if a manager holds it.
- Teardown with a non-S3 backup location `os.RemoveAll`s any
  `ManagerDBFile + ".old"`, renames an existing store directory to it, then
  calls `BackupDB(ManagerDBFile)`; the S3 branch is unchanged.

```go
func ExportStore(dbDir string, w io.Writer) error
```

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/store_export.go`, `cmd/cloud.go`
**Test file:** `jobqueue/store_export_test.go`, `cmd/cloud_test.go`

**Acceptance tests:**

1. Given a stopped store with 10 live and 10 complete jobs, when
   `ExportStore` writes a file and a manager starts with that file as
   `dbFile`, then it has the same 10 live and 10 complete jobs.
2. Given a running manager on the store, when `ExportStore` runs, then it
   returns an error wrapping `ErrDBLocked`.
3. Given teardown with a local store directory and a stale `db.old`, when
   the rename step runs (test seam), then `db.old` is the store directory
   and `db` is free for the backup file.

## F: Server cleanup, config, tools and docs

### F1: Config removal

As an operator, I want removed tuning knobs to be ignored, not fatal.

**Package:** `internal/`, `cmd/`, `jobqueue/`
**File:** `internal/config.go`, `cmd/conf.go`, `cmd/manager.go`,
`jobqueue/server.go`
**Test file:** `internal/config_test.go`

**Acceptance tests:**

1. Given a config file containing `managerdbbatchdelay: 50` and
   `managerdbbatchsize: 10`, when loaded, then loading succeeds and every
   other key keeps its value.
2. Given the source tree, when searched, then `DBBatchDelay`,
   `DBBatchSize`, `ReserveWriteWait` and `managerdbbatch` appear in no
   `.go` file.

### F2: Concurrent key-changing modifies (delivery-queue row 15)

As a user, I want two concurrent `wr mod` calls that change keys to persist
both jobs.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/store_modify_race_test.go`

**Acceptance tests:**

1. Given jobs A and B and two concurrent Modify calls that each change the
   key of both, run 50 times, when each pair returns and the manager is
   restarted, then every job present in memory before the restart is
   recovered with the same key and command (0 lost persists).

### F3: dbstart over the new store

As a soak operator, I want `dbstart` output unchanged on the new store, so
that `soakgate.py`, `starttimes.py` and `relburycheck.py` keep working.

`dbstart <db dir>` prints the same TSV columns from `jobqueue.ReadStore`;
`-schema` is removed (and `wrdev.sh compact-fixture`, whose purpose was
stamping bbolt fixtures, is deleted in Phase 2).

```go
// ReadStore calls fn for every live then every complete job in a stopped
// manager's store, taking the store lock.
func ReadStore(dbDir string, fn func(live bool, job *Job) error) error
```

**Package:** `developers/soak/dbstart`, `jobqueue/`
**File:** `developers/soak/dbstart/main.go`, `jobqueue/store_export.go`
**Test file:** `developers/soak/dbstart/main_test.go`

**Acceptance tests:**

1. Given a store with one live psimjob (reserved, host `h1`, pid 7) and one
   complete psimjob (exit 0), when `dbstart` runs, then it prints exactly
   two lines with the columns and values the bbolt `dbstart` printed for
   the same jobs (golden test data regenerated from a bbolt run).

### F4: Retire bbolt (Phase 2)

As a wr developer, I want bbolt gone, so that only one storage engine
remains.

Delete `db.go` and the files listed under Packages, `developers/soak/
bboltexp/`, `.docs/reliable2/harness/statinspect/`, the bbolt-only tests,
`wrdev.sh` modes `freelist-check`, `remap-stall-check` and
`compact-fixture`, and `go.etcd.io/bbolt` from `go.mod` (the converter keeps
a read-only bbolt reader, so the module stays if needed only by
`store_convert.go`). Delivery-queue row 18 closes with `archivefold.go`.

**Package:** `jobqueue/`
**File:** as listed
**Test file:** `jobqueue/store_test.go`

**Acceptance tests:**

1. Given the tree, when searched, then `go.etcd.io/bbolt` is imported only
   by `jobqueue/store_convert.go` and its tests.
2. Given `make test`, `make race` and `make lint`, when run, then all pass.

### F5: Documentation

As an operator, I want to know what changed on disk and how to roll back.

CHANGELOG (Unreleased, under Fixed by impact, per repo style): faster adds
and reservations, no backup stalls, reservations never handed out
undurable; the automatic one-way conversion; `managerdbfile` and
`managerdbbkfile` are now directories (or an S3 prefix); rollback = stop,
remove the `db` and `db_bk` directories, rename `db.bolt-pre-<version>` to
`db` and `db_bk.bolt-pre-<version>` to `db_bk`, start the older wr, losing
everything done since the conversion; the two removed knobs.
`cmd/conf.go` comments for the two paths. `DEVELOPERS.md`: store layout,
`WR_TEST_STORE`, `WR_STORE_RECORD`, `WR_STORE_REPLAY`, crash-test env vars,
and the fallback below for the key index.

**Package:** docs
**File:** `CHANGELOG.md`, `cmd/conf.go`, `DEVELOPERS.md`
**Test file:** none (reviewed against this list)

**Acceptance tests:**

1. Given `CHANGELOG.md`, when read, then its Unreleased section states the
   rollback procedure and that rolling back loses everything done since the
   conversion.

## Implementation Order

Phases are sequential; sub-phases within a phase are sequential unless
noted. Every sub-phase ends with `make test`, `make race` and `make lint`
passing. Work and measurements on NFS go under
`/nfs/hgi/wr/sb10-bigdb/customdb/` (README.md "Where to run on NFS").

### Phase 0: interface and harnesses (bbolt only)

- 0a: A1 (interface, server onto it).
- 0b: A2 (inspection interface, test moves).
- 0c: A3 (stats). Parallel with 0b.
- 0d: A4 (crash test against bbolt) and A5 (record/replay). Record a
  prodsim run on develop-equivalent bbolt (keep it on NFS) for Phase 1.
- Gate: no behaviour change; `make speed` within 10% of `d1a4b6d7`.

### Phase 1: live tier on the log, history in bbolt

- 1a: B1, B2, B3 (`jobqueue/wal`).
- 1b: C1.
- 1c: C2, C4, C5, C7 (log store live tier, recovery, lock, version), with
  `history.bolt` and the overlay from D1 so history queries work.
- 1d: C6 (checkpoints), D1 (folder, stats).
- 1e: C3 (reservations), F1, F2; switch default `storeKind` to log.
- 1f: E1, E2, E3, E5, E7 (Phase 1 forms), F3; incremental backup (E4) with
  `history.bolt` shipped whole.
- Gates:
  - A4 on the log store: 16 rounds local and 16 on NFS, plus 20 damaged
    copies each, 0 missing; `FuzzScan` and `FuzzFlatSpec` 10 minutes each
    with no crasher.
  - A5: the Phase 0 prodsim recording replays against the log store with 0
    mismatches; the A2 suite passes with `WR_TEST_STORE=all`.
  - Crash soak at production LSF concurrency with a staged ramp (battery10
    shape, about 6,000 runners, at least 3h, injected stalls and crashes):
    0 acknowledged writes lost, 0 non-durable hand-outs (including during
    stalls: `soakgate.py` sums `notDurable` over the manager log's
    `"reservation wait"` lines and requires 0), 0 double runs outside
    injected stalls.
  - `make speed-full`: no regression above 10% against `d1a4b6d7`.
  - Daytime NFS, from the A3 lines in the soak: reservation wait p99 at
    most 50ms, add wait p50 at most 200ms.
  - wrdev `crash-recovery` and every other `wrdev.sh` scenario pass.

### Phase 2: history segments, incremental backup, no bbolt

- 2a: D2.
- 2b: D3; the folder writes segments; learned samples from footers.
- 2c: E4 final form (sealed `.hist` files), E5 with segments.
- 2d: E1 to segments, E6.
- 2e: F4, F5.
- Gates: A4 and A5 as in Phase 1 against the final store (A5 against the
  Phase 0 recording); the crash soak repeated; `make speed-full` within
  10%; daytime NFS targets as Phase 1; `wr status -i` for production's
  biggest rep group on the converted `runstate-gate/fixtures/prod.db` no
  slower than develop on the bbolt fixture (both timings recorded in the
  PR).

## Appendix: Key Decisions

- **Checkpoint by merging files, not memory.** The checkpointer runs the
  recovery reducer over the previous checkpoint and sealed wal files, so the
  cut is consistent by construction and the hot path takes no lock for it
  (d2-wal.md "Risks").
- **Folder separate from checkpoints.** The folder trails the log by about
  a second, so learned requirements and history lag by about a second, not
  a checkpoint interval; the overlay covers the gap.
- **Global seq, not log order alone.** Snapshots are encoded outside the
  writer's lock; a seq taken under the job's read lock orders them, and
  replay keeps the highest per key. Log order still decides the reservation
  release, because it is snapshotted later.
- **Compression placement.** s2 per add block in the handler, none on state
  records, zstd per 64KiB block for checkpoints and history
  (phase34-compression.md "At saturation" and "Conclusions"). No
  dictionaries.
- **Key index in memory.** About 50-100MB at 2.15M completed jobs. If memory
  becomes a concern, the fallback is on-disk sorted per-segment key indexes
  with a per-segment bloom filter; DEVELOPERS.md records it.
- **No Phase 1 to Phase 2 migration.** Phase 1 never reaches a release;
  fixtures are bbolt files and reach each phase through the converter.
- **Archived-bytes budget** prices records by uncompressed flat length,
  which is smaller than Binc, so the 256MB guard fires slightly later than
  today for the same history.
- **Testing.** GoConvey per go-conventions; implementors follow
  go-implementor, reviewers go-reviewer. Fault-injection hooks (sync hold,
  sync error, crash points, free space) are unexported package vars reset
  with `Reset`. Every acceptance test has a GoConvey test; A4, A5 and the
  fuzz targets also run in the gates at larger sizes.
- **Error policy.** Durable-write errors reach the caller as today; damage
  at the log's tail is normal crash recovery, damage anywhere else is
  `errStoreCorrupt` and goes to backup restore; nothing is retried
  silently.
