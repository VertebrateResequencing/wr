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
    `archivefold.go`, `db_compact.go`, `db_prefetch*.go`, `db_schema.go`;
    `backupCopyWriter` moves to `store_backup.go` and `db_backupsync_*.go`
    becomes `store_backupsync_*.go`). Phase 2 also takes bbolt out of
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
  gains `--source wait` (F3b, Phase 1 gate); `developers/soak/bboltexp/` and
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
  `addStored` is true only if the add's first job was committed before the
  archive was recorded (log store: Write path, rerun guard).
- `updateJobRunStateDurableWithin`, `logReserveNotDurable` and
  `setBatchTuning` stay in Phase 0. Phase 1 (C3) removes them and adds
  `reserveRunState` to both stores, leaving 47.
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
	reserveRunState(job *Job) (pendingCommit, error)                  // Phase 1
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

// pendingCommit is one write's durability. done is false while the write
// is not yet durable; err is its result once done. wal.Commit satisfies it.
type pendingCommit interface {
	WaitWithin(d time.Duration) (done bool, err error)
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

// done, if non-nil, is called by the writer goroutine with the batch's
// result after its sync and before Wait returns. It must not block.
func (w *Writer) Append(rec []byte, done func(error)) (Ref, Commit)
func (w *Writer) AppendBlock(block []byte, n int,
	done func(error)) (Ref, Commit) // pre-built
func (w *Writer) Rotate() (sealed uint32, err error)
func (w *Writer) Durable() (file uint32, off int64)
func (w *Writer) Stats() WriterStats
func (w *Writer) Close() error
```

`Append` and `AppendBlock` assign the record's Ref under the writer's mutex,
so Ref order is log order. The writer goroutine takes everything queued,
issues one `write()` and one `fdatasync`, calls the batch's `done` funcs in
Ref order, then closes the batch's channel; no lock is held across the sync
(as `proto/internal/gc`). Segments rotate at
`walSegmentSize` (256MiB) on a batch boundary; rotation fsyncs the old file
and the directory. Every file's first frame is a header written by the
writer, `"WRWALHDR"`, u32 previous file number, u64 previous file's durable
end (its size on a normal rotation); `Scan` passes it to fn like any record
and `ParseHeader` recognises it.

Write or sync error:

- Only the failing batch's commits get the error.
- The writer abandons the file (a page cache after a failed fsync cannot be
  trusted: nothing more is written to it), opens the next file number,
  whose header records the abandoned file's last durable end, and carries
  on with later batches.
- If opening, writing or syncing a new file also fails, that batch's
  commits get the error too, and the writer waits a backoff (100ms,
  doubling per consecutive failure, capped at `walRetryCap`, 5s) before
  taking the next batch, which opens a new file number. A success resets
  the backoff. The writer never gives up; the manager keeps serving and
  every failed commit returns its error to its caller, as a failed bbolt
  transaction does today.
- `onError` is called for every failed batch. The log store's `onError`
  logs `walWriteFailedLogMsg = "wal write failed"` at error at most once a
  minute, with the error and `failures` (count since the last line).

```go
func ParseHeader(rec []byte) (prevFile uint32, prevEnd uint64, ok bool)
func NewWriter(dir string, file uint32, segmentSize int64,
	onError func(error)) (*Writer, error)
```

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
Keys are 16 binary bytes (wr's 32-hex md5).

A key has two independent planes, as today's `jobslive` and `jobscomplete`
buckets: live (spec, state, delete, modify) and history (complete). No live
record changes history and a `complete` record changes no live state, so a
key can be complete and live at once, as today after a keep-live archive or
a put-back.

One reducer applies records in memory (the `done` func), at replay and at
checkpoint:

- History: a `complete` record applies only if its seq is higher than the
  key's last applied `complete` seq.
- A spec record (add, put-back, modify's new key) makes the key live with
  that spec seq. A delete record, an archive's delete or modify's old key
  removes the key's live entry, its state and its std.
- A state or std record applies only if the key is live, its seq is above
  the key's spec seq and above the last applied seq of its kind (state, or
  that std stream; a `complete` record on a keep-live key sets the std seq
  to its own). Else it is ignored, as today's orphan drops are
  (`beBatch.applyRunStates`, `storeRunningRerunMarks`,
  `runStateDroppable`). So a best-effort state record or exit block
  snapshotted before an archive, delete or modify but committed after it
  never recreates a live entry or std.
- No tombstone: a re-add's spec seq is taken after the removing block has
  applied and the item left the queue, so it exceeds every seq snapshotted
  from the queued job. A snapshot from a stale `*Job` pointer racing both
  removal and re-add is the window today's key-based live check has too.

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
| 9 | complete | key, flat run state (the job's complete record) |
| 10 | depgroup | dep group name, first time it is added |
| 11 | exitstat | key, `jobExitData`'s sample fields (below) |

- Depgroup: today's `depgroups` bucket (dep groups ever added, never
  removed). An add block carries one per dep group not yet in the store's
  ever-seen set, so `depGroupEverSeen` stays true after every member is
  deleted. Checkpoints carry the whole set; conversion writes one per
  `depgroups` entry, or, when that bucket is absent, one per distinct dep
  group in `depgroupToKey` (as `initDB`'s `rebuildDepGroups` back-fills).
- Exitstat: the exit marker. Every exit op (each `writeReleasedJob` call:
  release, bury and a replaced lost end state alike) writes exactly one in
  its block, holding the fields `newJobExitData` captures today: req group,
  fail reason, PeakRAM, `lastRunReqs().RAM`, PeakDisk and secs. Nothing
  else writes one; replay ignores it for live state. FailReason survives on
  the job after an exit (cleared only by completion and `resetRunLocked`),
  so later state records (kick, `wr mod`, delay changes) carry it too; they
  are never sampled.
- Learned-requirement samples are history (Phase 1: `history.bolt`'s
  `jobRAM`, `jobDisk`, `jobSecs`; Phase 2: segment footers), loaded into
  memory at open and extended by the folder from `complete` records (as
  `putJobStats`) and exitstat records (as `updateFailStat`, including
  `shouldRecordHighPeakRAMStat`) only.

- Flat spec: every field `encodeJob` persists today that `jobRunState` does
  not. Strings and string slices are length-prefixed; Dependencies,
  Behaviours, MountConfigs and any `Requirements.Other` map may be an
  embedded Binc blob, present only when non-empty (as
  `proto/internal/flat`). First byte is the spec codec version (1).
- Flat run state: every `jobRunState` field, variable length, first byte
  version (1).
- Archive (`archiveCompletion`, as `archiveJobTx`): one block of a
  `complete` record holding `recordCompleteTx`'s complete record (for a
  keep-live archive, `rerunRecords`' complete record), then a `delete`
  record, or for a keep-live archive a state record of `rerunRecords`' live
  record. A history entry is a `complete` record plus the key's spec record
  current when it applies; a later `complete` record for the key replaces
  it, as today's `jobscomplete` Put does. Nothing removes a history entry:
  put-backs (`storeLiveForRerun`, `putBackArchivedDependentsTx`, archived
  dependents `retrieveDependentJobs` resurrects), `deleteLiveJobs` and
  re-adds touch only the live plane.
- Every multi-record operation (an add, a modify, an archive, a rerun
  put-back, an exit) is one s2 block, built and compressed by the
  calling goroutine before `AppendBlock`. Single state records are plain
  frames; the writer never compresses.
- Std follows today's lifecycle (`updateStd`, `recordCompleteTx`,
  `deleteOldLiveJobs`/`jobStd.restore`). Every exit's block is its state record,
  its exitstat record and a std record per stream: the new bytes if the exit
  failed or storage is forced, else empty, which clears earlier std. Replay
  drops a key's std on a `complete` record or a delete record, and moves it from
  old key to new key on a modify record.

### Write path

- Durable waits are `Commit.Wait` on the record's batch. The rules for what
  waits are today's (requirements.md "Write kinds"): adds, start, archive,
  release, bury, delete, modify, limits, rerun marks wait; best-effort
  changes do not.
- Reservation (C3): wait at most `reserveDurableWait()`
  (`min(10s, ClientMinRequestTimeout/4)`, today's bound); on timeout or any
  error, return the job to the ready queue, record a release state record
  (not waited for) and answer "no job". The release takes a higher seq than
  the reserve, so recovery never sees a job handed out before its reservation is
  durable. A crash after the reserve is durable but before the release is
  recovers the job as reserved by a runner that never had it; it is not run
  twice and spends no retry. A record without Host/Pid recovers straight to
  ready. Otherwise `releaseRunsMovedOnFrom` (`moved_on_runner.go`) normally
  releases it when that runner reserves its next job; failing that, TTR
  expiry and confirm-dead release it. Only a live runner that never
  reserves again parks it until `ServerTimings.LostRunnerBackstop` (1h),
  which kills the runner and so depends on the farm forced command.
  Waiting for the release would put the stall back on the reserve handler,
  so it is not waited for.
- `beSeq`, run-state overlays, change-versus-exit skipping and writer turns
  do not exist in `logStore`. `expectJobExitUpdate` keeps its contract:
  `retrieveJobStd` waits for exit updates expected or in flight.
- In-memory state reflects a record only once its commit succeeds. Every
  append passes a `done` func that, on nil, applies the record to the
  in-memory state below with the replay reducer (Records: seq rules and
  orphan drops, so apply order does not matter); on error it does
  nothing. So a failed batch leaves no live key, overlay entry, std, env,
  limit, dep group or Ref behind, and its Refs are never reachable;
  nothing reads them. A caller that waits sees its record applied when
  `Wait` returns nil.
- Keys of an add in flight sit in a pending set; a concurrent add that
  meets a pending key waits for that add's commit, then dedups against the
  result.
- Rerun guard. bbolt gets it right because transactions are serial; the
  log store gets the same from `logStore.guardMu` and one rule: never decide
  on a peer that is appended but not committed; wait for its `Commit`, then
  decide again.
  - `guardMu` is held by `registerRerunGuard`, by each archive from
    `rerunGuards.archiving` through building its block and `AppendBlock`,
    and by a guarded add whose guard has dependents from `atRiskKeys`
    through building its block (spec, state and put-back records) and
    `AppendBlock`. Seq order therefore equals decision order. Waits are
    made with `guardMu` released.
  - `inFlight` maps the key of each archive appended under `guardMu` to its
    `Commit`; `registerRerunGuard` seeds the new guard's archived set with
    those whose `WaitWithin(0)` is not done (bbolt's `scanKeys`). Entries
    found done are dropped; `done` funcs never take `guardMu`.
  - Archive: if a guard has the key as a dependent and its add's `Commit`
    (set under `guardMu` at its `AppendBlock`) is not done, wait for it.
    `addStored(member)` is then "member is live in memory" (committed).
  - Guarded add: if an at-risk key is in `inFlight` and not done, wait for
    it. A key is put back only if not live in memory.
  - Add commit fails: the archive that waited finds the member not live,
    notes the key and archives normally; nothing is put back, as when
    bbolt rolls back a failed add. The add returns the error (Errors); the
    user's retried add reruns the dependent as any add of a member does.

### In-memory state of `logStore`

Per live key: spec seq and last applied state and std seqs, Ref of its
spec record, Ref of its last state record, its rep group, dep groups and
reverse dep groups (index membership only). Plus: envs (blob by key),
limits, dep groups ever seen, std Refs by key, learned-requirement samples
per req group (sorted slices), history key index (with each key's last
applied `complete` seq), history overlay (completions not yet folded: key
to spec Ref and `complete` record Ref). Live `*Job` values are never held
by the store; methods that return live jobs decode them through their Refs.

### Recovery

1. Take the lock, check `VERSION`, delete `*.tmp` and any `SOURCE` (E1).
2. Load the newest checkpoint with a valid trailer, decoding blocks with 8
   concurrent readers.
3. Open history and read its durable folded position F (file, offset).
   Scan every wal file from min(F's file, the checkpoint's `walFrom`) in
   file order. Live records from `walFrom` on are applied to live state;
   `complete` and rglookup records at or after F rebuild the overlay.
4. Damage rules over that range: a file followed by another is valid up to
   the next file's header `prevEnd`; bytes beyond it are an abandoned or
   torn tail and are truncated (logged at warn with the byte count), as is
   a torn or corrupt tail of the last file. Damage before `prevEnd`, a
   missing file number, a missing or wrong header, or no valid checkpoint is
   `errStoreCorrupt`.
5. Return live jobs (spec + latest state), as `recoverIncompleteJobs` does.

Wal deletion rule (the only one): a wal file is deleted only when it is
below both the newest checkpoint's `walFrom` and F's file, where F is
durable at each fold commit in Phase 1 (`history.bolt` meta) and at each
segment seal in Phase 2, only when no reader holds it, and, while
incremental backup runs, only once the backup has shipped all its bytes
(else a backup still needing the file keeps a torn copy).

### Checkpoints

Every `checkpointInterval` (5 min) or `checkpointLogBytes` (256MiB of wal
since the last), and never sooner after the previous one ended than it took:

1. Rotate the writer; the sealed file number + 1 is the cut `walFrom`.
2. Wait until the folder's position is at or past the cut. While the
   folder is failing (Folder and history), this checkpoint is skipped, not
   waited for; the next interval tries again.
3. Merge the previous checkpoint with wal files before the cut using the
   recovery reducer, writing `NNNNNNNN.ckpt.tmp`: a header frame (`walFrom`,
   `histFrom` = durable F read at the start of this step, max seq, counts),
   zstd blocks of at most 64KiB uncompressed holding each
   live job's spec and state records, each live key's non-empty std, and
   every env, limit and depgroup record, then a trailer frame (block
   offsets, magic `WRCKPT01`). Phase 2 only: it also holds the spec record
   of each key with a `complete` record at or after `histFrom` and before
   the cut that is no longer live (completions in the open segment), so an
   overlay entry's spec survives wal deletion. In Phase 1 that set is
   empty: after step 2, `histFrom` is at or past the cut.
4. fsync, rename to `.ckpt`, fsync the directory, then swap every in-memory
   Ref (spec, last state, std, overlay spec) that points below the cut for
   its copy in the new checkpoint, then delete older checkpoints (except one
   a backup tick holds) and apply the wal deletion rule.

A checkpoint is a pure function of files on disk, so it is a consistent cut
whatever the hot path does meanwhile.

### Folder and history

The folder is one background goroutine, never on a reply path. At least once
a second it reads the log from its position up to `Writer.Durable()` and
folds into history every `complete` record (with its spec) and rglookup,
and every new learned-requirement sample, from `complete` and exitstat
records only (Records). It reads a file followed by another only up
to the next file's header `prevEnd`, so an abandoned batch is never folded.
Folding is idempotent and resumable from a log position. A failed fold
(read or history commit) is retried from the last durable position with
the writer's backoff (100ms doubling, capped at `walRetryCap`) and logged
as `foldFailedLogMsg = "history fold failed"` at error at most once a
minute with `failures`; it never stops the manager.

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
- Until folded, a completion is in the overlay, and every history query
  answers from history plus overlay, so status straight after an archive is
  unchanged.
- History membership never depends on live state; each query filters live
  keys exactly where today's does (`jobqueue/db.go`):
  - no live filter: `checkIfComplete`, add dedup
    (`checkIfCompleteUnderRepGroup`), `retrieveCompleteJobsByKeys`,
    `retrieveLastCompletionTimeByRepGroup`, `repGroupHasHistory`,
    `retrieveRepGroups` (rep groups ever added) and
    `spendArchivedBytesByRepGroup` (also charges live keys);
  - live keys skipped: `retrieveCompleteJobsByRepGroup`,
    `retrieveOldestCompleteJobsByRepGroup` (selection and count),
    `retrieveCompleteJobStatusByRepGroup`, `retrieveCompleteJobsRecent`;
  - the add's dependent scan (`dependentJobsScan.processKey`) reads a key's
    history entry only when the key is not live and not in the add.
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
  at offset 257) to restore. Anything else is corrupt and restored from
  backup as today's "corrupt (?)" db file (E2 order step 8).
- The self-contained backup is a tar of `VERSION`, the newest checkpoint C
  (chosen and held first), all history files (Phase 1: `history.bolt` via
  `tx.WriteTo`; Phase 2: sealed `.hist`), then the wal files from min(C's
  `walFrom`, the copied history's folded position's file) through a
  just-sealed one.
  `backup(w)` writes it; `wr manager backup -p` stores it as one file.
- Incremental backup (production, or forced): a tick runs every
  `minimumTimeBetweenBackups` (30s) at most. Invariant: the backup's
  checkpoint C has `histFrom` at or below its history's folded position
  F_b, so every completion at or after F_b finds its spec in C or in wal
  the backup keeps. A tick, in order:
  1. Picks the newest local checkpoint C and holds it, so the store keeps
     it until the tick ends.
  2. Ships history not yet shipped. Phase 1: `history.bolt` copied whole,
     only when its `meta` folded position differs from the last shipped
     copy's, under one read transaction that also reads that position.
     Phase 2: every sealed `.hist` not yet shipped. F only advances and C's
     `histFrom` was durable before C existed, so F_b is at or past it. A
     fold commit or seal after this step cannot break the invariant: a
     checkpoint written after it is shipped only by a later tick, after
     that tick's history step.
  3. If C is not yet shipped and its `histFrom` is at or below F_b, ships
     C (`.tmp` then rename locally; one object on S3), then deletes every
     other checkpoint from the backup. Otherwise it keeps the shipped one
     and logs at warn. The backup holds exactly one checkpoint, two only
     between the ship and that delete; restore uses the newest.
  4. Ships wal bytes as parts: for each wal file, the durable bytes not
     yet shipped (bounded by the next file's `prevEnd` for an abandoned
     file) go to one new part `NNNNNNNN.wal.OOOOOOOOOOOOOOOO` (16-hex start
     offset; `.tmp` then rename locally, one object on S3). No byte is
     shipped twice; checkpoints rotate the writer every 5 min, so a file
     has a handful of parts. History before log keeps the backup's log at
     or past F_b once the tick ends.
  5. Deletes all parts of a wal file from the backup only when the file is
     below both the shipped C's `walFrom` and F_b's file (Phase 1: the
     position read with the shipped `history.bolt` copy; Phase 2: the last
     shipped sealed segment's). The store's own deletion is not the test:
     its checkpoint and F are newer than the backup's.

  Every local copy goes through today's paced `backupCopyWriter`
  (`jobqueue/db.go` `copyBackup`/`pace`: async writeback every
  `backupCopySyncBytes`, 8MiB, via `backupPaceRange` in
  `db_backupsync_linux.go`, then one fsync), the fix for the backup copy
  burst that starved bbolt's fdatasync on NFS; it moves to
  `store_backup.go` when Phase 2 deletes `db.go`. Restore rebuilds each wal
  file by concatenating its parts in offset order, stopping at the first
  gap (the rest is a torn tail to recovery). `close` runs one final tick
  after the writer's last commit when anything is unshipped, as today's
  `finaliseBackup` writes a final backup on close.
- An S3 `dbBkFile` in development deployment (forced backups) keeps
  today's `"." + deployment` suffix (`initDB`), so the prefix is
  `<dbBkFile>.development/`.
- An old backup is a bbolt file at `dbBkFile` (default `db_bk`) or one S3
  object. Before the first incremental ship, if the backup path holds bbolt
  (meta magic), it is renamed to `dbBkFile + ".bolt-pre-" + v` (as E1;
  S3: copy then delete) and kept. Restore also accepts such a backup (E2).
- `daemonStopWait` (`cmd/root.go`) adds its 1s per 50MiB for the total
  size of the regular files under the `dbFile` directory (walked
  recursively) instead of `os.Stat(dbFile).Size()`; a regular file at
  `dbFile` (unconverted bbolt) still counts its own size, so a same-size
  database gets today's wait.

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
`errStoreVersionTooNew`, `errCompactNotSupported` (Phase 1 only).
`ErrDBLocked` and `ErrDBUnreadable` keep their meaning;
`openErrorThatIsNotCorruption`'s errno list applies to the store directory
and its files too. A failed durable write returns its error to the
caller (as today) and the manager keeps serving; a writer error after
`close` returns `errDBClosed`.

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
   `archivefold.go` and `store_bolt.go` (from Phase 1 also
   `store_history_bolt.go`, `store_convert.go` and `store_backup.go`; F4
   test 1 replaces this list in Phase 2), when parsed
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

Methods live in `_test.go` files on each implementation. The test files
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
`items`, `meanLock`, `maxLock` (waiting for turn and lock), `meanHold`,
`maxHold` (transaction including commit), `interval`; for `log`: `batches`,
`records`, `bytes`, `meanSync`, `maxSync`. Also warn `reservationWaitLogMsg =
"reservation wait"` (`n`, `p50`, `p99`, `max`, `notDurable`, `abandoned`) and
`addWaitLogMsg = "add wait"` (`n`, `p50`, `p99`, `max` over
`storeNewJobsGuarded` calls). Lines with `n` or `txs` 0 are not written.

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

`TestAddLatency1000` (`jobqueue/store_addbench_test.go`) is the add gate's
fixed-size measurement, since `"add wait"` mixes add sizes. It skips unless
`WR_ADDBENCH_DIR` is set; it starts a server whose `DBFile` is under that
directory, with the store kind from `WR_TEST_STORE`, adds 100,000 jobs, then
makes 100 sequential `client.Add` calls of 1,000 new jobs each with
`ignoreAdded` true (as `wr add`), and logs `n`, `p50` and `p99` of their
round trips. It fails when p50 exceeds `WR_ADDBENCH_P50` (default 200ms).

**Package:** `jobqueue/`
**File:** `jobqueue/store_stats.go`
**Test file:** `jobqueue/store_stats_test.go`,
`jobqueue/store_addbench_test.go`

**Acceptance tests:**

1. Given a histogram observing 1ms, 2ms, ..., 100ms once each, when taken,
   then `n` is 100, `max` is exactly 100ms, `p50` is in [45ms, 55ms] and
   `p99` is in [89ms, 110ms]; a second `take()` has `n` 0.
2. Given a server with the report interval at 50ms, when 3 adds and 5
   reservations are made, then within 500ms the captured log's
   `"add wait"` lines have `n` values summing to 3, its
   `"reservation wait"` lines `n` values summing to 5, and it has a
   `"db writer"` line with `writer=newJobs` (bbolt) or
   `writer=log` (log store).
3. Given an idle server for 3 intervals, when the log is read, then it has
   no `"db writer"`, `"add wait"` or `"reservation wait"` line.

4. Given `WR_ADDBENCH_DIR` a temp dir and `WR_ADDBENCH_P50` 1ns, when
   `TestAddLatency1000` runs as a child process, then it fails and its
   output has `n=100` and a `p50=` value.

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
intervals). `ctx` is never recorded; replay passes its own.

Arguments that are closures or live objects are recorded as data. A handle
(`*jobChangeAhead`, `*rerunGuard`, the func from `expectJobExitUpdate`) is
recorded as the `Start` of the call that returned it (0 for nil); replay
keeps a map from that id to the handle its own store returned and passes
the mapped handle (nil for 0 or unmapped). A `change` func is recorded as
the job it leaves: the wrapper passes the store a closure that calls
`change` and then encodes the job (the store holds the job's lock then); a
call whose store never ran `change` records the job unchanged.

| Call | Recorded | Replay |
| --- | --- | --- |
| `prepareJobChange` | job as `change` left it; result id | change = set the recorded fields; map result |
| `queueJobChangeAhead` | ahead id; job as `change` left it | same change; mapped ahead |
| `moveJobChangeAhead` | from, to, ahead id; result id | mapped ahead; map result |
| `discardJobChangeAhead` | job, ahead id | mapped ahead |
| `expectJobExitUpdate` | result id; calling the func records `expectJobExitUpdate.done` with that id | call the mapped func |
| `registerRerunGuard`, `releaseRerunGuard` | result id; guard id | map result; mapped guard |
| `storeNewJobsGuarded` | guard id with its member and dependent keys at entry | mapped guard given them (`setDependents`) first |
| `spendArchivedBytesByRepGroup` | budget's `remaining` at entry | fresh budget with that `remaining` |
| `reserveRunState` | job | call, then `WaitWithin(reserveDurableWait())` on the result; digest is the returned error |
| `resolveDependencies` | each `hasMembers` and `liveCommandVariants` query and answer (wrapped `groups`) | `groups` answering from those; an unrecorded query is a mismatch |

The digest of `spendArchivedBytesByRepGroup` covers only whether it
returned an error, since stores price records differently (D3).

Methods removed in Phase 1: a store without
`updateJobRunStateDurableWithin(job, wait)` gets `reserveRunState(job)`
then `WaitWithin(wait)` on the result (digest not compared), and
`logReserveNotDurable` and `setBatchTuning` are skipped. Skipped calls
count in `skipped`, never as mismatches.

```go
type recordedCall struct {
	Start, End uint64 // global call sequence at entry and at return
	Method     string
	Args       []byte // Binc of the arguments as tabled above
	Digest     [32]byte
	Nanos      int64
	Write      bool
}

func replayRecording(ctx context.Context, path string,
	s store) (replayStats, error)

type replayStats struct {
	calls, skipped int
	mismatches     []replayMismatch
}

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
   fixed cutoff, one kick through `prepareJobChange`/`queueJobChangeAhead`,
   one discarded change ahead, a guarded add, an archived-bytes spend, a
   dependency resolve) recorded against bbolt, when replayed against a fresh
   bbolt store, then `calls` equals the recorded count, `skipped` is 0 and
   `mismatches` is empty.
2. Given that recording, when replayed against a store whose
   `retrieveCompleteJobsByKeys` drops its last result, then `mismatches` is
   non-empty and its first entry has Method
   `"retrieveCompleteJobsByKeys"`.
3. Given a recording whose last frame is truncated by 3 bytes, when
   replayed, then it replays every complete frame and returns no error.
4. Given a bbolt recording with 3 `updateJobRunStateDurableWithin`, 1
   `logReserveNotDurable` and 1 `setBatchTuning` call, when replayed
   against a store wrapper that hides those three methods, then `skipped`
   is 2, `mismatches` is empty and the 3 jobs are reserved in the replayed
   store.
5. Given a recording with two changes prepared and then queued in reverse
   order, when replayed against a bbolt store wrapped to note handles, then
   each `queueJobChangeAhead` received the handle its own job's replayed
   `prepareJobChange` returned, and the replayed jobs' changed fields equal
   the recorded ones.

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
   1,000, and a scan of the file finds the header then 1,000 records.
2. Given a test sync hook that blocks the first sync for 200ms, when a
   record is appended, then `WaitWithin(50ms)` returns `(false, nil)` and
   `Wait()` returns nil after the hook releases.
3. Given a sync hook returning `EIO` once, when record a is appended and
   then record b in a later batch, then a's `Wait()` returns an error
   wrapping `syscall.EIO`, b's returns nil, b's Ref names file 2, nothing
   is written to file 1 after the failure, and file 2's header gives
   `prevFile` 1 and `prevEnd` file 1's size before a's batch.
4. Given `segmentSize` 4KiB and 100 records of 100 bytes, when appended,
   then at least 2 `.wal` files exist, records never span files, and Refs
   name the file each record is in.
5. Given a sync hook returning `EIO` for every sync and `walRetryCap`
   200ms, when 5 records are appended 100ms apart, then each `Wait()`
   returns an error wrapping `syscall.EIO` within 1s of its append and
   `onError` was called once per failed batch; when the hook is then
   cleared, the next record's `Wait()` returns nil within 1s, a scan finds
   it in a file numbered above every earlier one, and `done` funcs were
   called with the same results the `Wait()`s returned.

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
state record per new job (and any rglookup, depgroup and put-back spec and
state records the add makes), appends it and waits. Dedup, dependency
handling and the 260929 rule (a re-add never touches a queued or running
job's record) are unchanged. `storeEnv`, `storeLimitGroups` and
`storeRepGroupLookups` append their records and wait.

**Package:** `jobqueue/`
**File:** `jobqueue/store_log.go`, `jobqueue/store_records.go`
**Test file:** `jobqueue/store_log_add_test.go`

**Acceptance tests:**

1. Given a log store, when 1,000 jobs are added in one direct
   `db.storeNewJobsGuarded` call, then the wal holds exactly one new outer
   frame, it is a block with codec s2, and it contains 1,000 spec records.
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

Each transition appends one state record. `persistReservation` calls
`reserveRunState(job)` and then `WaitWithin(reserveDurableWait())` on the
handle; on timeout or any error but `errDBClosed` it does not hand out.
`logStore` returns the reserve record's `wal.Commit`. The bbolt store
starts a goroutine running its `Batch` update of the run state and returns
a handle that is done, with Batch's error, once Batch returns;
`WaitWithin` selects on that against a timer. The rules below live in the
server and are store-independent, so the bbolt store follows them from 1e
on too. It undoes each per-reserve side effect
that a release undoes today:

1. releases the item to the ready sub-queue with no delay and no added
   attempt;
2. calls `job.decrementLimitGroups(s.limiter)`, as `finalizeReleasedJob`
   and `ttrCallback` do, giving back the limit-group usage that
   `reserveWithLimits` took and `noteReserveLimitGroups` recorded;
3. forgets the runner hold (`s.runnerHolds.forget`);
4. under the job lock sets State ready, clears ReservedBy, Host, Pid and
   `handingOut` (which `handOutReservation` clears on hand-out);
5. records the reserved-to-ready change with
   `prepareJobChange`/`queueJobChangeAhead`, so status-feed counts move the
   job back from reserved to ready;
6. logs `reserveAbandonedLogMsg` through `s.warns` and answers with no job,
   as when nothing is ready.

It does not undo the rest. The scheduler group count is not decremented:
the item still wants a runner, and the ready-added recount
(`accountForRunningJobs`) counts it as ready instead of running. The
runner's `ClaimForReserve` claim stays, as it does today when the queue has
nothing ready; that runner then exits or reserves again.
`releaseRunsMovedOnFrom` is not called, as when nothing is ready. The
minted run token and the item's Reserves count are overwritten by the next
reserve. On `errDBClosed` it answers with no job and undoes nothing, which
is harmless: the limit usage and the runner's claim die with the manager.
Today (`serverCLI.go` `persistReservation`) hands the job out on
`errDBClosed`; answering no job instead is intended: it happens only at
shutdown, the reservation is not durable so recovery sees the job as
before it, and it keeps the invariant that no hand-out is ever
non-durable. The only visible effect is a runner getting no job while the
manager stops, which it already gets once the manager is gone, so it has
no CHANGELOG line.

On success `handOutReservation` restarts the TTR, as today. It checks the
reservation's handle with `WaitWithin(0)`; if it is not done it still
hands out but counts the job in the `"reservation wait"` line's
`notDurable` field (replacing `logReserveNotDurable`'s count), so any
non-durable hand-out is visible in the manager log. Each reservation
abandoned in step 6 is counted in that line's `abandoned` field (its wait
is observed too), since `s.warns` rate-limits `reserveAbandonedLogMsg`.
Removed: `updateJobRunStateDurableWithin`, `logReserveNotDurable`,
`errDurableWriteWaitExpired`, `ReserveWriteWait`, `setBatchTuning`.

```go
// reserveDurableWait is min(10s, ClientMinRequestTimeout/4), today's
// reservation write bound.
func reserveDurableWait() time.Duration
```

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/store_log.go`, `jobqueue/db.go`
**Test file:** `jobqueue/store_reserve_test.go`

**Acceptance tests:**

1. Given a healthy log store, when a runner reserves a job, then it gets
   the job and, after a crash-reopen, the job is reserved with that
   runner's ClientID as ReservedBy.
2. Given `ClientMinRequestTimeout` 800ms (bound 200ms) and a sync hook
   holding every sync for 2s, when a runner reserves, then within 1s it
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
   has `notDurable=1`; without the hook (test 2) it has `notDurable=0` and
   `abandoned=1`.
7. Given a job in limit group `"g"` with limit 1 and a sync hook returning
   `EIO` once, when a runner reserves with the job's scheduler group, then
   it gets no job, the item is in the ready sub-queue with Attempts
   unchanged, State ready, empty ReservedBy and `handingOut` false,
   `s.limiter.GetRemainingCapacity(ctx, ["g"])` is 1 (usage 0), and the
   status feed received a reserved-to-ready `jstateCount` delta for it; a
   second reserve with that group gets the job.
8. Given a test hook that drops the release record after a durable reserve
   timed out (test 2), when the store is reopened, then the job recovers as
   reserved by that ClientID, the wal holds no start record for it, and
   the server treats it as a reserved job whose runner vanished (as a
   bbolt store recovered with the same record does).
9. Given a sync hook returning `EIO` for every sync, when 3 adds are made
   within 2s, then each returns an error to the client, the server still
   answers a status request, and the captured log has exactly one
   `"wal write failed"` line with `failures` at least 1; when the hook is
   cleared, a fourth add succeeds and its job is live after reopen.
10. Given a bbolt store whose `Batch` is held 300ms by a test hook, when
    `reserveRunState(job)` returns, then `WaitWithin(50ms)` on its handle
    returns `(false, nil)` and `WaitWithin(1s)` returns `(true, nil)`; with
    the hold at 2s and test 2's timeouts, a runner gets no job and the item
    is in the ready sub-queue.

### C4: Delete, modify and reruns

As a user, I want `wr remove` and `wr mod` to be durable and atomic, so
that a crash never resurrects or half-modifies a job.

Delete appends delete records (one block per call). Modify appends one block
per job: modify, new spec, new state. Put-backs (`storeLiveForRerun`) append
spec and state records in one block and leave history as it is. Rerun marks
are state records.

**Package:** `jobqueue/`
**File:** `jobqueue/store_log.go`
**Test file:** `jobqueue/store_log_ops_test.go`,
`jobqueue/store_log_guard_test.go`

Rerun guard tests (9 to 11) run a server on a log store with running job d
depending on dep group `"g"` and an add of new member m to `"g"`.

**Acceptance tests:**

1. Given 3 live jobs, when 2 are deleted and the store reopened, then
   `count(kindLive)` is 1.
2. Given a modify of job k1 to key k2 whose block is truncated by 1 byte,
   when reopened, then k1 is live with its old spec and k2 does not exist.
3. Given a completed job put back live (`storeLiveForRerun`), when checked
   before and after reopen, then `checkIfLive` and `checkIfComplete` are
   both true, `retrieveCompleteJobsByKeys` returns it and
   `retrieveCompleteJobsByRepGroup` does not; when it is then deleted
   (`deleteLiveJobs`) and re-added with `ignoreAdded` true, then the add
   reports 1 already complete, stores nothing and `checkIfLive` is false.
4. Given a job that exited 1 with std `"o"`/`"e"`, when it then exits 0
   and the store is reopened, then `retrieveJobStd` returns empty and
   `count(kindStdOut)` is 0.
5. Given two jobs that exited 1 with std, when one is archived and the
   other deleted and the store reopened, then `count(kindStdOut)` and
   `count(kindStdErr)` are 0.
6. Given k1 exited 1 with std `"o"`/`"e"`, when modified to key k2, then
   `retrieveJobStd(k2)` returns `"o"`/`"e"` and `retrieveJobStd(k1)`
   returns empty, both before and after a checkpoint and reopen.
7. Given a running job with RerunAfterRun true, recorded by
   `storeRunningRerunMarks`, when archived and the store reopened, then
   the outcome is `archiveKeptLive`,
   `checkIfLive` and `checkIfComplete` are both true, its live record has
   RerunAfterRun false and ReservedBy zero, and
   `retrieveCompleteJobStatusByRepGroup` counts 0 complete.
8. Given live jobs k1, k2 and k3, each with a state record and
   an exit block (std `"o"`/`"e"`) snapshotted but not yet appended (test
   hook), when k1 is archived, k2 deleted and k3 modified to k4, and only
   then the held records are appended and committed, then `checkIfLive`
   is false for k1, k2 and k3, `count(kindLive)` is 1 (k4),
   `count(kindStdOut)` and `count(kindStdErr)` equal their values before
   the held appends, `retrieveJobStd(k4)` is unchanged, and re-adding k1
   with `ignoreAdded` true reports 1 already complete and stores nothing;
   the same holds after reopen and after a checkpoint then reopen.
9. Given a test hook after the add's `AppendBlock` and before its commit
   (a sync hook holding that batch), when d's successful archive starts
   there, then it has not returned 100ms later; when the sync is released,
   the outcome is `archiveKeptLive`, d's item state is
   `queue.ItemStateDependent` then `queue.ItemStateReady` once m completes,
   and d is reserved again; after reopen `checkIfLive(d)` is true.
10. Given d's archive appended but its commit held by a sync hook, when
    the add then runs, then it has not stored by 100ms; when the sync is
    released, d's archive outcome is `archiveRemovedLive`, the add puts d
    back (`checkIfLive(d)` true before and after reopen) and d runs again
    once m completes.
11. Given test 9's hold, when the held batch's sync returns `EIO`, then the
    add returns an error wrapping `syscall.EIO`, d's outcome is
    `archiveRemovedLive`, `checkIfLive(d)` is false and
    `checkIfComplete(d)` true before and after reopen; when m is added
    again, it succeeds and d is live and runs again once m completes.

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
4. Given a sync error mid-run (B2 test 3) after which file 1 is given 50
   extra bytes past its header-recorded `prevEnd`, when reopened, then
   recovery succeeds, file 1 is truncated to `prevEnd`, the failed batch's
   records are absent, and later records in file 2 are applied.
5. Given wal file 2 deleted while file 3 exists, when reopened, then the
   error wraps `errStoreCorrupt`.
6. Given 100,000 live 1KiB jobs, when recovered, then the store's own heap
   growth (excluding returned jobs, measured after dropping them) is under
   40MB.
7. Given live k2 (running) and k3 (running, earlier exit 1 with std
   `"x"`/`"y"`), a sync hook holding one sync until three writes are queued
   and then returning `EIO`, when a 1-job add of new key k1, the archive of
   k2 and k3's exit 1 with std `"o"`/`"e"` are made into that batch, then
   each returns an error; `checkIfLive(k1)` is false, `checkIfLive(k2)` is
   true and `checkIfComplete(k2)` false, `retrieveJobStd(k3)` returns
   `"x"`/`"y"`, and with the hook cleared a retried add of k1 stores it
   (`checkIfLive(k1)` true, not deduped); the same answers hold after
   reopen and after the folder has run.

### C6: Checkpoints and spec addressing

As a wr developer, I want background checkpoints that bound recovery and
log size and keep every live spec addressable, so that D3 needs no format
change.

**Package:** `jobqueue/`
**File:** `jobqueue/store_checkpoint.go`
**Test file:** `jobqueue/store_checkpoint_test.go`

**Acceptance tests:**

1. Phase 1: given a checkpoint after 2,000 jobs were added and 1,000
   archived and folded, when it completes, then no wal file below its cut
   remains and reopening gives the same live set as before it.
2. Phase 2: given the same with the open history segment not yet sealed,
   when the checkpoint completes, then wal files from the last sealed
   segment's position onward remain, and reopening gives the same live set
   and all 1,000 completions; after the segment seals and another
   checkpoint runs, no wal file below the cut remains.
3. Given a test hook that kills the checkpoint after writing `.ckpt.tmp`
   but before rename, when reopened, then the previous checkpoint is used,
   the `.tmp` is gone, and the live set equals the model.
4. Given 1,000 live jobs, 100 of them with std, and a checkpoint that
   drops every earlier wal file, when each live entry's spec, last state
   and std Ref is read with `wal.ReadAt`, then it decodes to that job's
   spec, state and std (1,000, 1,000 and 100 of each).
5. Given 4 goroutines writing continuously during 3 checkpoints, when
   reopened, then the live set equals the in-memory model.

### C7: Lock and version

As an operator, I want two managers never to share a store and an old store
version to be refused clearly.

**Package:** `jobqueue/`
**File:** `jobqueue/store_open.go`
**Test file:** `jobqueue/store_open_test.go`; test 4 in
`jobqueue/store_convert_test.go` (which may import bbolt, F4)

**Acceptance tests:**

1. Given a store held open, when a second `openStore` runs with
   `managerDBOpenTimeout` 100ms, then it returns an error wrapping
   `ErrDBLocked` within 1s.
2. Given `VERSION` containing `wr-store 2`, when opened, then the error
   wraps `errStoreVersionTooNew` and says "use a newer wr"; no file changed.
3. Given an unreadable store directory (mode 0), when opened, then the error
   wraps `ErrDBUnreadable` and no restore is attempted.
4. Given a closed store directory, when `bolt.Open(dir, 0600,
   &bolt.Options{Timeout: time.Second})` runs (what a downgraded wr's
   `initDB` does), then it returns an error matching `syscall.EISDIR`,
   `openErrorThatIsNotCorruption` turns it into one wrapping
   `ErrDBUnreadable`, and the directory's file names and every file's
   sha256 are unchanged.

## D: History and folding

### D1: Folder into bbolt history (Phase 1)

As a user, I want status, history and learned requirements to answer as
today while history is written in the background.

Learned stats lag the folder harmlessly: schedulers read them only
through the cache (`currentReqGroupRecs`, `recommendedReqForGroup`), which
already reloads a req group at most once per refresh interval, so no path
reads them synchronously after an archive.

**Package:** `jobqueue/`
**File:** `jobqueue/store_fold.go`, `jobqueue/store_history_bolt.go`
**Test file:** `jobqueue/store_fold_test.go`

**Acceptance tests:**

1. Given the folder paused by a test hook and rep group `"rg"` never seen
   before, when a job of `"rg"` is archived, then `checkIfComplete` is
   true, `retrieveCompleteJobsByKeys` and
   `retrieveCompleteJobsByRepGroup("rg")` return it,
   `retrieveOldestCompleteJobsByRepGroup("rg", limit 10)` returns it with
   count 1, `retrieveCompleteJobsRecent(before)` includes it,
   `retrieveLastCompletionTimeByRepGroup(["rg"])` is its EndTime,
   `retrieveCompleteJobStatusByRepGroup("rg", false)` counts 1 complete,
   `repGroupHasHistory("rg")` is true and `retrieveRepGroups()` includes
   `"rg"`.
2. Given that job, when the folder resumes and runs, then the same nine
   answers are unchanged and the overlay is empty.
3. Given 3 completions in req group `"q"` with PeakRAM 100, 100, 200; 2
   releases in `"q"` with FailReasonRAM (PeakRAM 300) and FailReasonTime;
   1 bury in `"q"` with FailReasonDisk (PeakDisk 50); and 1 release in
   `"q"` with FailReasonExit, PeakRAM 900 and required RAM 100 (a
   `shouldRecordHighPeakRAMStat` sample); then each released or buried job
   kicked, given a `wr mod` change (`modifyLiveJobs`) and a delay change
   (best-effort state record), when the folder is flushed (test hook: fold
   to `Writer.Durable()` and return), then the rise in `reqStatChanges()`
   and `refreshReqGroupRecs("q")` equal what bbolt gives for the same
   steps, and the wal holds exactly 4 exitstat records.
4. Given a fold killed after its bbolt transaction but before anything
   else, when reopened and folded again, then `count(kindComplete)` is
   unchanged (idempotent).
5. Given a fold hook that blocks the folder for 2s, when 100 archives are
   made meanwhile, then every `archiveCompletion` returns within 500ms and
   the `"db writer"` line has `writer=fold`.
6. Given the folder paused, a job archived and then put back live
   (`storeLiveForRerun`), when the folder resumes and the store is
   reopened, then before and after the fold `checkIfLive` and
   `checkIfComplete` are true, `retrieveLastCompletionTimeByRepGroup` and
   `repGroupHasHistory` still report it, and
   `retrieveCompleteJobsByRepGroup`, `retrieveCompleteJobsRecent` and
   `retrieveCompleteJobStatusByRepGroup` omit it.
7. Given scripted workloads recorded against bbolt (A5), each with every
   history query and a re-add with `ignoreAdded` true after each step:
   (a) archive then `storeLiveForRerun`; (b) a keep-live archive (rerun
   mark set while running); (c) archive, add of a new member to a dep group
   the archived job depends on (resurrection) and archive again; (d)
   archive, put back, `deleteLiveJobs`, re-add; when each is replayed
   against the log store with the folder paused and again with it flushed
   after every write call, then `mismatches` is empty.

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
to that segment's record, so a superseded record drops out without
rewriting segments; live keys are filtered per query as listed under
"Folder and history".

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
   index is under 47MB (100MB at 2.15M keys).

### D3: History queries on segments (Phase 2)

As a user, I want every status and history answer to equal today's.

`spendArchivedBytesByRepGroup` prices each history entry at the
uncompressed flat length of its spec and `complete` records, times
`archivedFlatScale` (unexported, per mille, fixed by test 4), so the guard
trips where today's Binc pricing does.

**Package:** `jobqueue/`
**File:** `jobqueue/store_history.go`
**Test file:** `jobqueue/store_history_test.go`

**Acceptance tests:**

1. Given the A5 workload recording and the four D1 test 7 recordings (all
   made against bbolt), when replayed against the Phase 2 store with
   segments sealed after every write call, then `mismatches` is empty.
2. Given rep group `"big"` with 50,000 completions over 5 segments, when
   `retrieveCompleteJobStatusByRepGroup("big", false)` runs, then it reports
   50,000 complete and reads no record block (footer reads only, counted by
   a test counter).
3. Given `retrieveOldestCompleteJobsByRepGroup` with a limit of 10 on that
   group, when run, then it returns the 10 oldest by StartTime and counts
   50,000.
4. Given a production-shaped completed job (300-byte Cmd, 60-byte Cwd,
   40-byte RepGroup, 30-byte ReqGroup, 3 DepGroups and 2 Dependencies of
   20 bytes, Requirements with RAM, Time, Cores, Disk and one Other entry,
   one Behaviour, EnvKey, Host, HostID, IP, StartTime, EndTime, PeakRAM,
   CPUtime), when n copies are priced against `maxArchivedBytes` by both
   the Phase 2 pricing and `len` of today's Binc record, then the count
   at which `ErrArchivedHistoryTooBig` trips differs by at most 10%.

## E: Lifecycle (Phase 1, history parts Phase 2)

### E1: Automatic conversion

As an operator, I want my existing bbolt database converted on the first
`wr manager start`, with progress shown and the old file kept for rollback.

Steps, crash-safe: (0) validate the source: open it read-only with
`bolt.Open` (which checks both meta pages' magic, version and checksum);
(1) check free space on `dbFile`'s filesystem is at least
the bbolt file's size, else `errNotEnoughSpace` naming needed and available
bytes; (2) create `dbFile + ".converting"` and write `SOURCE` naming
`dbFile + ".bolt-pre-" + v` (`v` = `ServerVersion`, or `dev` when empty),
fsync; (3) rename `dbFile` to that name; (4) build the store inside
`.converting` (Phase 1: checkpoint of live jobs with run-state overlays and
rerun marks applied, envs, limits, depgroups, std, and `history.bolt` from
the history buckets, stats included; Phase 2: `.hist` segments
instead); (5) fsync, rename `.converting` to `dbFile`, fsync the parent,
then delete `dbFile/SOURCE` (recovery deletes a leftover one). The bbolt
file at the pre name is kept for rollback. At open, an existing
`.converting/SOURCE` resumes: clear the rest of `.converting` and redo from
step 0 on the source (at `dbFile` or the pre name). A `.converting` with no
`SOURCE` (a crash in step 2) is removed and conversion starts at step 0.

Corrupt source: an error from step 0, or a read error or recovered bbolt
panic from the source during step 4, that `openErrorThatIsNotCorruption`
does not claim, means the source is corrupt. Then `os.RemoveAll`
`.converting`, rename the pre name back to `dbFile` if step 3 ran, fsync the
parent, and restore as E2 order step 8 with today's corrupt-db message. A
claimed error is returned as today with nothing renamed back or restored
(a resumed run sees `SOURCE` and retries). So a corrupt bbolt file never
loops on resume.

Accepts bbolt schema 0, 1 and 2. In step 0 it reads the stamp as
`readOnlyDBFileSchemaVersion` does and refuses, renaming nothing and
creating no `.converting`, a malformed stamp (`errBadDBSchemaVersion`) or
a newer schema (`checkDBSchemaVersion`'s `errDBSchemaTooNew`, "use a newer
wr"); neither is a corrupt source. Schema 0 is needed for real production
DBs: `db_schema.go` defines it as a DB with no recorded version, which older
wr wrote. In every schema,
StdOutC/StdErrC of complete records are dropped. A missing bucket reads as
empty, except where `initDB` back-fills one today: `depgroups` is derived
from `depgroupToKey` (Records); `jobLookupEntries` is a bbolt-only reverse
index the converter never reads. No other bucket is back-filled
(`repgroups` and the rest are created empty), and the schema 1 and 2 bumps
only record a version, so nothing else is derived. Progress uses
`internal.DBUpgradeConvertState = "convert database"` with Processed/Total
in jobs; `IsDBUpgradeState` returns true for it.

```go
func convertBoltDB(ctx context.Context, dbFile string,
	upgrade *dbUpgradeReporter) error

// buildStoreFromBolt is step 4: it reads boltFile read-only and writes a
// store into the empty directory dir.
func buildStoreFromBolt(ctx context.Context, boltFile, dir string,
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
   unchanged; schema 1 and 2 DBs convert too. Given a schema 0 DB with no
   `jobslive` and no `depgroups` bucket (so `initDB` accepts it) whose
   `depgroupToKey` holds groups `"d"` and `"e"`, each with one complete
   member, when converted, then jobs added depending on `"d"`, on `"e"` and
   on never-seen `"f"` get the same `resolveDependencies` results and
   `depGroupEverSeen` answers as on a copy of the DB opened by `initDB`.
3. Given a test hook crashing in step 2 before `SOURCE` is written, after
   each of steps 2, 3, 4 (mid-build) and before 5, when started again,
   then each yields the same store as an uninterrupted run and the bbolt
   file's sha256 is unchanged.
4. Given a free-space hook reporting 1 byte less than the bbolt size, when
   started, then the error wraps `errNotEnoughSpace`, `dbFile` is still the
   untouched bbolt file and no `.converting` exists.
5. Given a conversion in progress, when the sidecar is read, then State is
   `"convert database"`, Processed rises to Total, and
   `IsDBUpgradeState("convert database")` is true.
6. Given a finished conversion, when `dbFile` is listed, then it holds no
   `SOURCE`, and `dbFile.bolt-pre-dev` exists with the original's sha256.
7. Given a bbolt DB stamped schema `currentDBSchemaVersion + 1`, when the
   manager starts, then the error wraps `errDBSchemaTooNew` and contains
   "use a newer wr"; given one whose stamp is 4 bytes, the error wraps
   `errBadDBSchemaVersion`; in both, `dbFile` is still the untouched bbolt
   file, no `.converting` or `.bolt-pre-` file exists and no restore ran.

### E2: Open, restore and corruption

As an operator, I want the restore-from-backup behaviour of today for the
new store.

`openStore` order:

1. Dev wipe (E3).
2. `os.RemoveAll` any leftover `dbFile + ".restoring"` and
   `dbFile + ".corrupt"`.
3. Resume conversion.
4. `dbFile` absent: restore (below), or create an empty store (`VERSION`,
   empty checkpoint) when there is no backup.
5. `dbFile` a self-contained file: restore it, then remove the file;
   message `"restored db directory <dbFile> from self-contained backup
   file"`.
6. `dbFile` bbolt (meta magic): convert (E1); a corrupt source goes to 8.
7. `dbFile` a store: recover; on `errStoreCorrupt`, go to 8.
8. Otherwise (unrecognised, or corrupt from 6 or 7): restore from the
   backup with today's "corrupt (?) db file" messages; with no backup, or
   a backup that fails restore step 2, return the error as today with
   `dbFile` untouched.

Wiping before the S3 missing-file download loses no dev-mode restore:
today `initDB` downloads first and then wipes both copies, so the wipe
already makes that download moot.

Restore never touches `dbFile` until the backup is proven good, as today
(`initDB` opens the backup before replacing):

1. Copy or download the backup into `dbFile + ".restoring"`: a store
   backup's files, a self-contained file's tar members, or an old bbolt
   backup, opened read-only and built into it by `buildStoreFromBolt` (a
   downloaded bbolt object lives inside `.restoring` and is removed before
   step 3). S3 downloads land only inside
   `.restoring`.
2. Recover the store in `.restoring` fully (as `openStore` would), then
   close it and fsync. Any error: `os.RemoveAll` `.restoring`, leave
   `dbFile` untouched, return the error with today's message.
3. If `dbFile` exists, rename it to `dbFile + ".corrupt"`; rename
   `.restoring` to `dbFile`; fsync the parent; `os.RemoveAll` `.corrupt`.

A crash at any point leaves either the original `dbFile` (step 2 of the
order then discards the partial `.restoring`) or no `dbFile` (the restore
reruns from the same backup). The returned message strings are today's,
word for word (`initDB`).

**Package:** `jobqueue/`
**File:** `jobqueue/store_open.go`
**Test file:** `jobqueue/store_open_test.go`

**Acceptance tests:**

1. Given no `dbFile` and a backup directory, when opened, then the live and
   complete sets equal those at backup time and the message starts
   `"recreated missing db file"`.
2. Given no `dbFile` and a self-contained file at the backup path, when
   opened, then the same holds.
3. Given `dbFile` is a self-contained file, when opened, then `dbFile`
   becomes a directory holding the restored store and the message is
   `"restored db directory <dbFile> from self-contained backup file"`.
4. Given a corrupt store and a backup, when opened, then the store is
   restored and no file of the corrupt store remains.
5. Given an S3 backup location (fake `backupTarget`), when `dbFile` is
   missing, then every object under the prefix is downloaded and the store
   opens.
6. Given no `dbFile` and a bbolt file at the backup path, when opened, then
   `dbFile` is a converted store whose live and complete sets equal the
   bbolt file's, and the message starts `"recreated missing db file"`.
7. Given the same with the bbolt file as one object in a fake S3 target,
   when opened, then the same holds.
8. Given a corrupt store and a backup directory whose checkpoint trailer is
   damaged, when opened, then the error is returned, every file of the
   corrupt store is byte-identical to before, and no `.restoring` exists.
9. Given a corrupt store, a good backup, and a test hook crashing after
   each restore step (mid-copy, after recovery of `.restoring`, after the
   rename to `.corrupt`, after the rename to `dbFile`), when opened again
   without the hook, then the store equals the backup and neither
   `.restoring` nor `.corrupt` exists.
10. Given a corrupt store and an old bbolt backup at the backup path, when
    opened, then `dbFile` is a store whose live and complete sets equal the
    bbolt file's, the backup file is unchanged, and the message starts
    `"recreated corrupt (?) db file"`.
11. Given `dbFile` a bbolt file with both meta page checksums damaged and a
    good bbolt backup at `dbBkFile`, when opened, then `dbFile` is a store
    equal to the backup, the message starts `"recreated corrupt (?) db
    file"`, no `.converting` or `dbFile.bolt-pre-dev` exists, and a second
    open recovers the store without restoring or converting.
12. Given the same corrupt `dbFile` and a store-directory backup, when
    opened, then the same holds with the store equal to the backup.
13. Given `dbFile` a bbolt file whose meta pages are valid but a branch
    page is zeroed, and a store-directory backup, when opened, then the
    same holds (the failure arises during E1 step 4).
14. Given `dbFile` holds 4KiB of random bytes and a store-directory
    backup, when opened, then the store equals the backup and the message
    starts `"recreated corrupt (?) db file"`; with no backup, the error is
    returned and `dbFile` is byte-identical.

### E3: Recursive deletes

As a developer, I want the dev wipe and restore paths to remove a directory
or S3 prefix, not fail silently on one.

Every `os.Remove(dbFile)`, `os.Remove(dbBkFile)` and
`accessor.DeleteFile(dbBkFile)` in the open path becomes `os.RemoveAll` or a
prefix delete through `backupTarget`. The dev wipe keeps today's intent,
starting from an empty DB, so it removes everything the DB owns: `dbFile`,
`dbBkFile`, and any `dbFile + ".converting"`, `".restoring"`, `".corrupt"`,
`dbFile + ".bolt-pre-*"` and `dbBkFile + ".bolt-pre-*"` (S3: objects with
that prefix). The `.upgrade` sidecar is left, as today.

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
3. Given development deployment with `wipeDevDB`, a store directory, a
   `dbFile.converting` holding `SOURCE`, `dbFile.bolt-pre-dev` and
   `db_bk.bolt-pre-dev`, when opened, then none of them remains, no
   conversion ran and the new store has 0 jobs.
4. Given a corrupt store restored from backup (E2 test 4), when listed,
   then no file from the corrupt store's directory remains.
5. Given a corrupt store and a fake S3 target, once holding a good backup
   and once a backup with a damaged checkpoint, when opened, then in both
   cases no downloaded file remains anywhere (`.restoring` absent, no other
   new entry in `dbFile`'s parent directory).

### E4: Incremental backup

As an operator, I want backups that copy only new files, so that a backup
never stalls the manager or rewrites gigabytes.

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/store_backup.go`, `cmd/root.go`
**Test file:** `jobqueue/store_backup_test.go`,
`cmd/manager_stop_shutdown_test.go`

**Acceptance tests:**

1. Given production deployment, two backup ticks with 100 archives between
   them, when the second tick ships, then it uploads only files new or
   changed since the first (counted by the fake target), no sealed file
   twice, and the wal parts it uploads total exactly the wal bytes made
   durable since the first tick.
2. Given a backup directory after a checkpoint deleted old wal files, when
   the next tick ships, then those files are deleted from the backup only
   after the new checkpoint is there.
3. Given a crash at a random point during shipping, when the backup
   directory is opened as a store, then it opens and every history entry
   has State complete.
4. Given development deployment without `forceBackups`, when 3 ticks pass,
   then nothing is shipped.
5. Given a converted store and a bbolt file at `db_bk`, when the first tick
   ships, then `db_bk.bolt-pre-dev` has the old file's sha256, `db_bk` is a
   directory that opens as the store, and the same holds for a bbolt object
   in a fake S3 target.
6. Given production deployment and 10 archives after the last tick, when
   the store is closed before the next tick, then opening the backup
   directory as a store shows all 10 jobs complete.
7. Given one open wal file and 3 ticks with 10 adds before each, when the
   backup directory is listed, then that file has 3 parts whose offsets and
   sizes tile it from 0 without overlap, and restoring the backup (E2)
   yields all 30 jobs live; with the last part deleted, restore yields the
   first 20 and no error.
8. Given development deployment with `forceBackups` and S3 `dbBkFile`
   `s3://b/wr/db_bk` (fake target), when a tick ships, then every object is
   under `wr/db_bk.development/`.
9. Given a tick T1 that ships checkpoint C1 and history at folded
   position F_b in wal file f and deletes from the backup the wal files
   holding the adds of 20 jobs (all below C1's `walFrom` and f), then
   those 20 jobs completing in file f or later, and a test hook that
   during tick T2, after the history step and before the checkpoint
   step, folds and commits (Phase 1) or seals a segment (Phase 2) past
   those completions, then runs a checkpoint whose `walFrom` is past them
   and lets the store delete every wal file it may: when T2 finishes and
   the backup is restored (E2), then restore returns no error and the 20
   jobs and every job completed before T2 are complete, each with the Cmd
   it was added with; the same holds after a tick T3 and another restore,
   and after each tick the backup holds one checkpoint, whose `histFrom`
   is at or below the shipped history's folded position.
10. Phase 1: given `backupCopySyncBytes` set to the page size and
    `backupPaceHook` counting calls, when a tick ships a `history.bolt` of
    n bytes, then the hook ran at least n/pagesize - 1 times; when a second
    tick follows with no fold commit between, then it copies no
    `history.bolt`.
11. Given production deployment and a checkpoint that seals wal file w
    before a tick has shipped w's last 10 adds, when the checkpoint
    finishes, then w still exists locally; after the next tick it is
    deleted, and restoring the backup (E2) yields those 10 jobs live.
12. Given `config.ManagerDBFile` a directory holding a 10MiB file and a
    subdirectory holding a 20*`daemonStopBackupBytesPerSec` file, when
    `daemonStopWait()` runs, then it returns `daemonStopGiveup` + 21s; with
    the directory absent it returns `daemonStopGiveup`.

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

Phase 2. With the lock held: recover, fold the log and seal the open
segment, write a checkpoint (its rotation leaves one new empty wal file at
the cut), delete covered wal files, merge `.hist` segments smaller than
`histSegmentBytes` into new ones (write, fsync, rename, then delete
inputs). `CompactStats` before/after are
the directory's total bytes; unreadable records are counted as today.
Refusal while a manager runs is unchanged. On an unconverted bbolt file,
compact first runs `convertBoltDB` (same steps, free-space check and crash
safety as E1), then compacts the new store, so no older wr is needed. In
Phase 1 compact on a store directory returns `errCompactNotSupported`
(unreleased; Phase 2 deletes it).

**Package:** `jobqueue/`, `cmd/`
**File:** `jobqueue/store_compact.go`, `cmd/manager.go`
**Test file:** `jobqueue/store_compact_test.go`

**Acceptance tests:**

1. Given a store with 10 small segments and 5 wal files holding 100
   unsealed completions, when compacted, then 1 segment and 1 wal file
   remain, that file is empty and numbered the checkpoint's `walFrom`,
   and every query answer is
   unchanged (A5 digest compare).
2. Given a compaction killed after writing the merged segment but before
   deleting inputs, when opened, then each key is found once.
3. Given a manager holding the lock, when `CompactDBFileStats` runs, then
   it returns an error wrapping `ErrDBLocked`.
4. Given a bbolt file at `dbFile` and no manager, when `CompactDBFileStats`
   runs, then `dbFile` is a store directory, `dbFile.bolt-pre-dev` has the
   original's sha256, and the A5 query set gives equal digests for the
   bbolt file and the compacted store.
5. Phase 1 only: given a store directory at `dbFile` and no manager, when
   `CompactDBFileStats` runs, then the error is `errCompactNotSupported`,
   its text is exactly `compacting the new database store is not
   supported yet`, and the directory's files and sizes are unchanged.

### E7: Cloud deploy and teardown

As a cloud user, I want deploy and teardown to keep carrying my database.

- `bootstrapDBBackupLocation`: when `ManagerDBFile` is a store directory,
  it uploads a self-contained file made by `jobqueue.ExportStore` (the
  `backup -p` tar, written by `backup(w)`) to the remote's
  `.wr_<deployment>/db` (restored on the remote's first start, E2 test 3);
  when it is still a bbolt file it uploads it unchanged, as today, and the
  remote converts it on first start (E1).
- Deploy already dies if a local manager answers on its port
  (`cmd/cloud.go`, "is already running"); `ExportStore` also takes the
  store lock and fails with `ErrDBLocked` if a manager holds it, which only
  a manager not answering on that port can.
- Teardown with a non-S3 backup location `os.RemoveAll`s any
  `ManagerDBFile + ".old"`, renames an existing store directory to it, then
  calls `BackupDB(ManagerDBFile)`. The S3 branch also `os.RemoveAll`s
  `ManagerDBFile + ".old"` before its rename: today's `os.Rename` fails on
  a stale non-empty `.old` directory or a `.old` file when the source is a
  directory, and only warns, so a local start would open the stale store
  instead of restoring from S3.

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
3. Given a bbolt file as `ManagerDBFile`, when the upload step runs (test
   seam), then the uploaded file has the bbolt file's sha256 and
   `ExportStore` was not called.
4. Given teardown with a local store directory and a stale `db.old`, when
   the rename step runs (test seam), then `db.old` is the store directory
   and `db` is free for the backup file.
5. Given teardown with an S3 backup location, a local store directory
   `db` and a stale `db.old` directory holding files, when the rename step
   runs (test seam), then `db.old` is the store directory, `db` does not
   exist and no warning is logged; the same holds with a stale bbolt file
   at `db.old`.
6. Given the file the upload step sent (test seam) placed at a fresh
   remote deployment's `.wr_<deployment>/db`, when that remote manager
   first starts, then `db` is a store directory with the source store's
   live and complete jobs and the message is E2 test 3's.

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

### F3b: soakgate reads `"reservation wait"` lines

As a soak operator, I want `soakgate.py` to count non-durable hand-outs
from the new store's log lines, so that the Phase 1 crash-soak gate can be
checked once C3 removes the D1 line and the warning.

- New `--source wait`: in each manager segment (split and pre-start lines
  ignored as today), every `msg="reservation wait"` line adds its
  `notDurable=` to the hand-out count, placed inside or outside the stall
  windows by its `t=`, and its `abandoned=` to an abandoned total. It
  ignores D1 lines and warnings.
- `--source d1` and `--source warning` are kept unchanged, for soak trees
  from wr before this change; their cases (01 to 31) stay.
- The rotated-log refusal (`ROTATED`, exit 1) applies to every source.
- Output for `wait`: the `nondurable source=wait ...` line as today, then
  `totals n/a`, then a new line `abandoned n=<n> stalls=<k> <ok|NONE>`
  (k = stall windows; NONE when k > 0 and n = 0), then the rest as today.
  Other sources print no `abandoned` line.
- `RESERVE_WRITE_WAIT_S` becomes `RESERVE_WAIT_S = 10  # jobqueue's
  reserveDurableWait() with the default timeouts`, and the docstring's
  window rule says reserveDurableWait (10s) instead of ReserveWriteWait.
- `run.sh`'s header keeps its rule: every new rule has a case that fails
  under its mutant, or is listed with the reason it cannot.

**Package:** `developers/soak`
**File:** `developers/soak/soakgate.py`
**Test file:** `developers/soak/testdata/soakgate/run.sh` with new case
directories 32 to 36

**Acceptance tests:**

1. Case 32 (wait-counts): two manager segments; lines before the first
   start with `notDurable=5`; in-segment lines with `notDurable=0
   abandoned=2`, `notDurable=1 abandoned=0` inside a stall window and
   `notDurable=2 abandoned=1` outside; one stall. Expected starts
   `nondurable source=wait inside=1 outside=2`, then `totals n/a`, then
   `abandoned n=3 stalls=1 ok`.
2. Case 33 (wait-stall-none): one stall and only `notDurable=0
   abandoned=0` lines: `nondurable source=wait inside=0 outside=0` and
   `abandoned n=0 stalls=1 NONE`.
3. Case 34 (wait-no-stalls): no stall.log, `abandoned=0` lines:
   `abandoned n=0 stalls=0 ok`.
4. Case 35 (wait-ignores-old): D1 lines and `"reservation not yet recorded
   on disk"` warnings (one `(repeated)`) beside one `notDurable=0
   abandoned=1` line: `inside=0 outside=0` and `abandoned n=1 stalls=0
   ok`.
5. Case 36 (wait-rotated): a rotated manager log in the soak manager's
   directory: output `ROTATED <file>`, exit 1.
6. `run.sh` passes with all 36 cases, and each of these mutants of
   `soakgate.py` makes at least one of cases 32 to 36 fail: summing
   `abandoned` into the hand-out count; ignoring segments; dropping the
   window placement; NONE when k = 0; NONE never.

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
2. Given `CHANGELOG.md`, when read, then every entry listed above sits
   under Unreleased's `### Fixed` (in one of its `####` subsections), and
   none under `### Added` or `### Changed`.
3. Given `cmd/conf.go`, when read, then the `managerdbfile` comment says
   it is a directory, and the `managerdbbkfile` comment says it is a
   directory or an S3 prefix.

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
- Gate: no behaviour change; `make speed` (not `make speed-full`, which
  gates Phases 1 and 2) within 10% of `d1a4b6d7`.

### Phase 1: live tier on the log, history in bbolt

- 1a: B1, B2, B3 (`jobqueue/wal`).
- 1b: C1.
- 1c: C2, C4, C5, C7 (log store live tier, recovery, lock, version), with
  `history.bolt` and the overlay from D1 so history queries work.
- 1d: C6 (checkpoints), D1 (folder, stats).
- 1e: C3 (reservations), F1, F2; switch default `storeKind` to log.
- 1f: E1, E2, E3, E5, E7 (Phase 1 forms), F3, F3b; incremental backup (E4) with
  `history.bolt` shipped whole.
- Gates:
  - A4 on the log store: 16 rounds local and 16 on NFS, plus 20 damaged
    copies each, 0 missing; `FuzzScan` and `FuzzFlatSpec` 10 minutes each
    with no crasher.
  - A5: the Phase 0 prodsim recording replays against the log store with 0
    mismatches (removed methods mapped or skipped as A5 says); the A2 suite
    passes with `WR_TEST_STORE=all`.
  - Crash soak at production LSF concurrency with a staged ramp (battery10
    shape, about 6,000 runners, at least 3h, injected stalls and crashes,
    with `relbury.sh` batches at the injected kills as in the soak README):
    - 0 acknowledged writes lost: `soakgate.py`'s `missing` line has
      `absent=0` (every job a runner started is in the final
      `dbstart.tsv`, so no acknowledged add was lost) and
      `relburycheck.py` prints `problems: 0` (no acknowledged release or
      bury lost across a kill -9);
    - 0 non-durable hand-outs, including during stalls: `soakgate.py
      --source wait` (F3b) prints `nondurable source=wait inside=0
      outside=0`. `notDurable` is the production `WaitWithin(0)` check
      `handOutReservation` makes on every hand-out (C3), not a test hook,
      so it is a regression guard that is nonzero only through a defect.
      Its `abandoned` line must end `ok` with `stalls` at least 1, which
      shows the stall path ran;
    - 0 double runs outside injected stalls (`soakgate.py`'s `doubles`
      line, `outside=0`).

    The soak's backup ticks copy `history.bolt` whole (paced) whenever its
    folded position changed, a known caveat since Phase 1 is never released;
    the Phase 2 soak gives the authoritative backup-I/O result.
  - `make speed-full`: no regression above 10% against `d1a4b6d7`.
  - Daytime NFS (weekday, 10:00 to 17:00): reservation wait p99 at most
    50ms from the soak's `"reservation wait"` lines; 1000-job add p50 at
    most 200ms from `TestAddLatency1000` (A3) with `WR_ADDBENCH_DIR` under
    the NFS work directory and `WR_TEST_STORE=log`, passing 3 runs of 3.
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
  replay keeps the highest per key. The reservation release wins because
  it is snapshotted later and so has the higher seq.
- **Compression placement.** s2 per add block in the handler, none on state
  records, zstd per 64KiB block for checkpoints and history
  (phase34-compression.md "At saturation" and "Conclusions"). No
  dictionaries.
- **History independent of live state.** Today a key can be in
  `jobscomplete` and `jobslive` at once (keep-live archive, put-backs), and
  some queries filter live keys while `checkIfComplete` and add dedup do
  not. Two record planes keep every one of those answers, so dup counts and
  re-adds after `wr remove` are unchanged.
- **Key index in memory.** About 50-100MB at 2.15M completed jobs. If memory
  becomes a concern, the fallback is on-disk sorted per-segment key indexes
  with a per-segment bloom filter; DEVELOPERS.md records it.
- **No Phase 1 to Phase 2 migration.** Phase 1 never reaches a release;
  fixtures are bbolt files and reach each phase through the converter.
- **Archived-bytes budget** prices records by scaled uncompressed flat
  length (D3), pinned within 10% of Binc pricing for a typical record, so
  the 256MB guard shows no visible threshold change and needs no Binc
  encode on the query path.
- **Testing.** GoConvey per go-conventions; implementors follow
  go-implementor, reviewers go-reviewer. Fault-injection hooks (sync hold,
  sync error, crash points, free space) are unexported package vars reset
  with `Reset`. Every acceptance test has a GoConvey test; A4, A5 and the
  fuzz targets also run in the gates at larger sizes.
- **Error policy.** Durable-write errors reach the caller as today; damage
  at the log's tail (or an abandoned file's) is normal crash recovery,
  damage anywhere else is `errStoreCorrupt` and goes to backup restore; the
  only retries are the writer opening a new file after a failed batch and
  the folder retrying a fold, both with capped backoff, forever; the
  manager never stops on a write failure.
- **Apply on commit.** In-memory state changes only in a commit's `done`
  func, so a failed write leaves nothing to revert.
