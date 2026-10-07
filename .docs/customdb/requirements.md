# What durability wr's manager needs

This is the inventory behind the designs in this directory. Code references
are to develop at 9fc0d795 (which includes the run-state change, #684).
Rates come from the battery10 soak (`/nfs/hgi/wr/sb10-bigdb/battery10/`,
be432093, 3h, peak 6,104 LSF runners), the runstate-gate soaks
(`runstate-gate/analysis/bursts-findings.md`) and `dbstats` (in `proto/`)
run on a copy of the compacted production fixture
`runstate-gate/fixtures/prod.db`.

## The shape of the problem

The manager already holds every live job in memory: the `queue` package's
items carry `*Job`, with their state, dependencies and sub-queue. While the
manager runs, the database is not the source of truth for live jobs; it is
the redo record that lets a restarted manager rebuild that memory, plus the
only home of completed-job history, std out/err, env blobs, limits and
resource statistics.

So the durability requirement splits cleanly in two:

1. Live jobs: a crash-consistent record of each live job's spec and its
   latest run state, good enough that recovery never re-runs a job a runner
   may still be running, never loses an acknowledged add, completion or
   release, and never resurrects a deleted job.
2. History: an append-mostly store of completed jobs, queried by key, by rep
   group, by end time and by dep group, which can grow to millions of
   records and tens of GB.

## Data held today

Buckets in `jobqueue/db.go` (`bucket*` vars) and `db_schema.go`, sized from
`prod.db` (5.1GB compacted; 7.9GB before compaction):

| Bucket | What | prod.db keys | Bytes |
| --- | --- | --- | --- |
| `jobslive` | Binc-encoded `Job` per live job | 118,213 | 187MB (p50 1.3KB, max 1.2MB) |
| `jobscomplete` | Binc `Job` per completed job | 2,155,154 | 2.95GB (p50 1.3KB, max 103KB) |
| `jobRunState` | CRC + small run state (since #684) | (none in prod.db) | ~400B each |
| `repgroupToKey` | `rg_::_key` lookup | 4,167,762 | 238MB of keys |
| `depgroupToKey` | `dg_::_key` lookup | 884,142 | 57MB |
| `reverseDepgroupToKey` | dependency's dg to dependent key | 20,159 | 1.4MB |
| `jobLookupEntries` | reverse index of the three above, for delete | 5,072,063 | 565MB |
| `endTimeToKey` | end time + key, for "recent" | 373,827 | 16MB |
| `repgroups`, `depgroups` | every rep/dep group ever seen | 25,854 / 5,636 | 1.4MB |
| `repgroupEndTime` | rg to last completion | 13,670 | 0.7MB |
| `stdo`, `stde` | compressed std out/err of failed/buried runs | 27,858 / 322,455 | 86MB |
| `envs` | content-addressed environment blobs | 549 | 1.3MB |
| `jobRAM`, `jobDisk`, `jobSecs` | per req group sorted samples, for recommendations | 117,635 | 5MB |
| `limitgroups` | limit per group | 39 | tiny |
| `meta` | schema version | 1 | tiny |

Of the 5.1GB file, about 3.1GB is job encodings and about 0.9GB is index
keys; the rest is B+tree overhead and free pages. Production commands are
small (cmd p50 300-350 bytes, p99 1.3KB); prodsim's portal jobs are 10KB,
which is what makes soak databases 7-21GB.

Job fields split naturally into an immutable spec (Cmd, Cwd, groups,
requirements as added, limits, deps, behaviours, mounts, container options,
env key: about 1KB-10KB, never changed except by `wr mod`) and a small mutable
run state (`jobRunState` in `db_runstate.go`: state, attempts, until-buried,
reserved-by, runner reservation, host, pid, runner pid, times, peak RAM/disk,
CPU time, exit code, fail reason, learned requirements, rerun mark; under
200 bytes in a fixed binary layout, see `proto/internal/flat/state.go`).

## Write kinds

Rates are at production scale (battery10 peak: 129 runs/s, 6,000 runners;
runstate soaks: adds of 10-13k jobs/min in 1000-job calls). "Durable before"
says what the manager waits for today.

| Write | Code path | Rate at scale | Durability today | What it must guarantee |
| --- | --- | --- | --- | --- |
| Add (new jobs) | `storeNewJobsGuarded` -> `storeNewJobStores` (chunked `bolt.Batch`, or the folding `newJobsWriter`) | 170-220 jobs/s in 1000-job calls; 2MB/s of 10KB specs | Before replying to the client | An acknowledged add survives; a re-add never overwrites the record of a queued or running job (260929) |
| Add-time lookups | RTK, RGs, DTK, depgroups, RDTK, jobLookupEntries | 3-6 index keys per job | Same transaction(s) as the add | Rebuildable from specs |
| Env | `storeEnv` | Per add call, dedup by content | Before the add | Content-addressed, idempotent |
| Reserve | `persistReservation` -> `updateJobRunStateDurableWithin(ReserveWriteWait=10s)` | ~130/s | Waits up to 10s, then hands out anyway (logged) | The job is not offered to another runner after a crash while its runner may run it |
| Start | `handleStart` -> `updateJobRunStateDurable` | ~130/s | Before replying | Recovery puts a running job back in Run, not Ready |
| Touch | `handleTouch` | Every running job, each TTR fraction | None (memory only) | Nothing on disk |
| Archive (success) | `archiveCompletion` -> archive writer (`archiveTx`, folded) | ~130/s, catch-up folds of 1,600 after restarts | Before replying | The job is complete, not live, after a crash; stats, end-time index, rg end time updated; std out/err dropped |
| Release (failure, retry) | `writeReleasedJob` -> `updateJobAfterExitDurable` (best-effort writer exit op, with std out/err) | Few % of runs; bursts on failures | Before replying (260930) | A released job comes back ready, not running |
| Bury | Same exit op | Rare | Before replying | Comes back buried |
| Kick, suspend, resume, lost marking, re-sent report answers | Best-effort writer full change | Operator-driven; lost marks bursty after crashes | Mostly queued, some durable | Ordering against reserve (#683, `UntilBuried`) |
| Rerun marks | `storeRunningRerunMarks`, `storeLiveForRerun` | Rare (dep re-runs) | Durable | Keep a re-run decision across a crash |
| Put back archived dependents | `putBackArchivedDependentsTx` | Rare | In the add | |
| Delete | `deleteLiveJobs` (`bolt.Batch`) | Operator; up to 100k at once | Before replying | No resurrection |
| Modify | `modifyLiveJobsTx` (delete old keys, put new, carry std) | Operator; up to 100k | Before replying | Key changes atomically |
| Limit groups | `storeLimitGroups` | Rare | Before replying | |
| Rep group lookup for duplicate adds | `storeRepGroupLookups` | Per duplicate add | Before replying | |
| Schema/meta | `putDBSchemaVersion` | Start, compact | | |
| Run-state drops | `dropRunStates` at recovery | Once per start | | |

Write volume at peak today: each reserve and start writes ~400B (since #684;
before it, the whole 1-10KB job), each archive writes the whole encoded job a
second time (into `jobscomplete`) plus 3-4 index keys and deletes the live
record, each add writes the job plus 3-6 index keys. bbolt turns each of
these into copy-on-write page rewrites of every B+tree page touched, so the
bytes committed are many times the logical bytes: the archive writer's own
commit costs 5.4ms per archive on NFS (bursts-findings §1).

## Read and query shapes

| Query | Code path | Frequency | Today |
| --- | --- | --- | --- |
| Recovery: every live job, overlaid with its run state | `recoverIncompleteJobs`, `readLiveJobsTx`, `overlayRunState` | Every start | Full scan + Binc decode, 6.4us/job (1.3KB) to 17.5us/job (10KB); 566k jobs 9.9s after a whole-file prefetch of 28s warm to 7min cold on NFS |
| Recovery: dependencies of every live job | `resolveDependencies` (chunked View) | Every start | DTK/depgroups gets |
| Is key live | `checkIfLive` | Add path, web, rerun | One Get |
| Is key complete (under this rep group) | `checkIfComplete`, `checkIfCompleteUnderRepGroup` | Every added job (dedup), archive idempotence | Complete Get + RTK Get, decode on mismatch |
| Dependents of dep groups | `retrieveDependentJobs` (DTK/RDTK prefix scans, live+complete gets) | Adds with dep groups | Prefix scans |
| Live keys in a dep group, dep group ever seen | `retrieveIncompleteJobKeysByDepGroup`, `depGroupEverSeen` | Adds, recovery | Prefix scan / Get |
| Completed jobs by keys | `retrieveCompleteJobsByKeys` | Status, subscriptions catch-up, touch after own run ended | Gets + decode |
| Completed jobs by rep group | `retrieveCompleteJobsByRepGroup`, `spendArchivedBytesByRepGroup` | `wr status -i`, web | RTK prefix scan + Get + decode per key; byte budget 256MB |
| Oldest N completed by rep group | `retrieveOldestCompleteJobsByRepGroup` | Status with limit | Scan + partial decode |
| Complete counts/usage by rep group | `retrieveCompleteJobStatusByRepGroup` | Status summaries, web seed | RTK scan + Get per key (49k gets for the biggest prod group) |
| Recent completions | `retrieveCompleteJobsRecent` | Status "recent" | `endTimeToKey` seek |
| Last completion time by rep group | `retrieveLastCompletionTimeByRepGroup`, `repGroupHasHistory` | Status | Get |
| All rep groups | `retrieveRepGroups` | Status listing | Bucket scan |
| Std out/err of a job | `retrieveJobStd` | Status of failed jobs | Get |
| Env blob | `retrieveEnv` (LRU) | Per reservation | Get |
| Resource recommendations | `recommendedReqGroup{Memory,Disk,Time}` | Per scheduling of a req group | Prefix scan of sorted samples |
| Stored limits | `retrieveStoredLimits`, `retrieveLimitGroup` | Adds, start | Get |

Every live-job query is answerable from memory while the manager runs; only
recovery reads live jobs from disk. Every history query can be served by
small purpose-built indexes (per-rep-group postings with counters, an end-time
log, a dep-group postings list) over an append-only history.

## Whole-file operations

- Continuous backup: `backupTicker` copies the whole file to the backup path
  (or S3) every max(30s, last copy's duration), paced by `sync_file_range`
  (260726-1). At 12-20GB each copy streams for 2-12 minutes over the same NFS
  and pins bbolt free pages.
- `wr manager backup` (`BackupDB`): streams a consistent copy to a client.
- `wr manager compact`: offline rewrite, strips std out/err of complete jobs
  (`db_compact.go`), stamps the schema version; now required for 0.37.2 and
  earlier databases.
- Prefetch at open (`db_prefetch.go`): the flock on NFS drops the client page
  cache, so the whole file is read with 8 streams before decoding.

## Failure model

- What the soaks inject, and what production mostly sees, is a process crash
  (kill -9, OOM kill, panic) of the manager, sometimes inside a stall of the
  NFS filesystem (FUSE commit stalls, ENOSPC). Data written with `write()`
  before a process crash is in the kernel's page cache and reaches the file
  even though the process is gone: fdatasync protects only against the host
  (or its NFS client) dying, or the NFS server losing acknowledged unstable
  writes.
- A host crash of the manager's node is the case fdatasync exists for. It has
  not been injected in any soak.
- Double-run prevention is a protocol property, not a storage property: a
  job handed to a runner must, after any crash, be recovered somewhere its
  runner's reports are accepted (Run), or be held until that runner is
  confirmed dead. Storage must only make sure that whatever recovery reads
  is a state the manager actually acknowledged or a later one.

## The problems seen, by cause

From `.docs/bugfixes/` (DB/transaction/crash class), `CHANGELOG.md` 0.38.0 and
Unreleased, battery10 and the runstate gate:

| Problem | Root cause | Would a simpler store remove it? |
| --- | --- | --- |
| Reservations handed out before durable (2.43% of runs in battery10; 1.39% after #684) | One bbolt write lock shared by four writers, each commit a large page rewrite plus fdatasync on NFS; commit time scales with bytes per commit | Yes, mostly: an append-only log of 200-byte transitions with one group commit makes a commit cost ~1ms plus bytes, not pages; archives and adds no longer block transitions behind them |
| Archive stall reruns (260928-archive-stall-rerun), start/release durability bugs (260917, 260930) | Writes queued but not durable when the reply went out; separate writers with no ordering | Partly: one writer with one sequence gives total order for free; the reply-after-durable rule still has to be applied in the server |
| Re-add overwrites running job's record (260929) | Full-record rewrites from paths that don't know the run state | Yes: specs immutable, state separate, a re-add can't touch state |
| `beSeq` ordering, CRC-matching run-state overlay, kick ordering (#683, #684) | Full rewrites and small records racing in one key-value store | Yes: per-job sequence numbers in a log make last-writer-wins trivial |
| Backup stalls, remap stalls, mmap growth (260726-1, 260727-2, 260927-backup-remap-stall) | Whole-file copy of a mutable B+tree; mmap remaps block on read txns | Yes: immutable segments are backed up by copying new files only; no mmap remap |
| Freelist and cold-start cost (260928-db-cold-start-and-freelist) | bbolt freelist, flock dropping NFS cache, page-fault-driven decode | Yes for freelist; a sequential log read is what the prefetch emulates |
| Write amplification as free space grows | B+tree pages scattered over a 7-21GB file | Yes |
| Slow status for big rep groups (A4) | RTK scan + Get per key | Yes with counters in an index; also fixable in bbolt |
| NFS commit stalls (FUSE stall, ENOSPC) | The filesystem | No. Any design that waits for durability stalls with the filesystem. Only a local-disk primary (D5) or not waiting (D6) avoid it |
| Recovery time proportional to live bytes on NFS | Reading GBs of specs over NFS | Only a design that recovers without reading specs (D3 lazy) or keeps a local copy (D5) |
