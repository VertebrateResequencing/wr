# D1: key-laid-out files with purpose-built indexes (the owner's idea)

The owner's framing: store job details in files placed by key, with
near-zero-allocation encode and decode, purpose-built lookups for each query
shape, and the minimal state needed for clean crash recovery.

## Rejected starting point: one file per job

The literal form, one file (or a spec file plus a state file) per job in a
directory tree by key prefix, was measured and rejected, and the owner has
since ruled it out:

- Inodes: one per job at least, two with a separate state file, so 1M, 5M and
  10M stored jobs need 1-2M, 5-10M and 10-20M inodes, plus 65,536 directories
  for a two-level hex fan-out. Millions of small files on a shared NFS volume
  is not acceptable to the owner or to the filesystem's operators, and every
  backup, `du`, and compact walks them all.
- Commit cost: a crash-safe per-file update is create temp, write, fdatasync,
  rename, fsync the directory. On the NFS mount that took p50 4.3ms serially
  (155 commits/s), and at most about 1,450-1,500 commits/s with 16-64 in
  parallel, at p50 6-22ms and p99 up to 79ms (`benchmarks.md`, fsyncbench).
  The hot path needs about 400 transitions/s plus 200 adds/s at today's peak,
  and 1000-job adds would each need 1000 such commits: near the ceiling with
  no headroom for the bursts the soaks see.
- There is no group commit across files: every transition pays its own
  metadata round trips, where a log pays one fdatasync per batch.
- Recovery opens, reads and closes every live job's file: 800k NFS
  lookup+open+read round trips.
- Multi-file atomicity (spec + state + index entries) needs a journal anyway,
  at which point the files are an index over the journal.

## Design: many jobs per file, laid out by key

- 64 bucket files (`b000.log`..`b063.log`), bucket = first byte of the binary
  job key mod 64 (more buckets, or sealed 64MB segments per bucket once a
  bucket grows, keep files bounded). Each bucket file is an append-only log
  of framed records (`[len][crc32c][type][key][payload]`): a spec record,
  written once at add, and fixed 192-byte state records, appended at every
  transition. A job's records all live in one file, so "where is key K" is
  "bucket K[0] mod 64, offset from the in-memory index".
- In memory: key -> (spec offset, latest state). The state is a value type
  of 192 bytes with no pointers (`flat.State`), so a table of a million of
  them costs the GC nothing to scan.
- Purpose-built indexes, all derived and rebuildable from the bucket files,
  checkpointed periodically with each bucket's covered offset:
  - rep group -> postings (sorted job keys) plus counters (complete count,
    summed usage), so `retrieveCompleteJobStatusByRepGroup` is O(1) and
    "jobs by rep group" reads only those jobs' records;
  - dep group -> postings, reverse dep group -> postings;
  - end time -> key, an append-only log in end-time order (archives arrive in
    nearly that order);
  - rep group -> last end time; req group -> sorted resource samples.
- Encode and decode: the hand-written `flat` codec (`proto/internal/flat`):
  spec encode 120-205ns and 0 allocations, decode 160ns and 0 allocations
  into a reused Job (strings alias the record), 4 allocations into a fresh
  Job; state put/get 32ns, 0 allocations. Binc today: encode 2.5-2.7us,
  decode 6.6-11.4us with 13 allocations.

## Writes and queries

| Operation | D1 |
| --- | --- |
| Add | Spec records appended to their buckets; one group commit fsyncs every bucket the batch touched (all 64 for a 1000-job add) |
| Reserve, start, release, bury, kick | One 192-byte state record appended to the job's bucket; group commit |
| Archive | A state record with state complete (plus usage); the spec stays where it is and becomes history; indexes updated in memory |
| Delete | A state record with state deleted |
| Modify | New spec record under the new key, deleted state for the old key |
| Std out/err | A separate record type in the bucket, dropped by compaction once complete |
| Recovery | Load index checkpoint, then scan every bucket from its checkpointed offset (or from 0) |
| History queries | From the derived indexes; records read by offset |

## Crash recovery

- What is guaranteed: every transition whose batch's fdatasync returned is
  recovered; a torn tail of any bucket (partial frame, CRC mismatch) is cut at
  the last intact record. Each record carries a per-job sequence number, so
  the newest state wins regardless of which record a later scan meets first.
- What is lost: anything not yet synced; a record is never half-applied.
- Double runs: as today, recovery puts reserved/running jobs back in Run.
  Because a reservation's write costs about 1ms rather than seconds, waiting
  for it before handing out no longer needs a 10s escape hatch.
- Crash tests (`proto/cmd/crashtest`): see `benchmarks.md`.

## Group commit, NFS and local disk

Key layout is what makes this design worse than a single log: a batch of N
transitions over random keys touches min(N, 64) files, and each needs its own
fdatasync. They run in parallel, so latency is the slowest of them, but the
NFS server sees up to 64 COMMITs per batch instead of one. Measured numbers
are in `benchmarks.md`.

## Memory and allocations

In-memory key index: 16-byte key + offset + 192-byte state, about 250 bytes
per live job (200MB at 800k). History jobs need only their index entries
(about 40 bytes each plus postings): 400MB at 10M.

## Backup and compaction

- Backup: bucket logs are append-only, so an incremental backup copies new
  bytes since the last copy (rsync-like by offset), not the whole store.
- Compaction rewrites one bucket at a time (all records of finished and
  deleted jobs whose history has been moved to sealed history segments, all
  superseded state records), then renames over the old file. Small buckets
  make it incremental and bounded.

## Files and inodes

| Stored jobs | Files |
| --- | --- |
| 1M | 64 buckets (or ~64 + 20 sealed 64MB segments at 1.3KB/job) + ~6 index files ≈ 70-90 |
| 5M | ≈ 170 (1.3KB) to 850 (10KB) with 64MB sealed segments |
| 10M | ≈ 270 (1.3KB) to 1,650 (10KB) |

## Migration

A one-off offline converter (like `wr manager compact`) reads the bbolt file
and writes buckets and indexes; the bbolt file is kept for rollback.

## Complexity, risk, how much of wr changes

- New code: bucket logs, index maintenance and checkpoints, compaction,
  incremental backup, converter: about 3-4k lines plus tests.
- The `db` type's API (about 60 methods in `db.go`) would be reimplemented;
  the server and queue packages need not change if the API is kept.
- Risk: derived-index bugs (stale postings after modify/delete), compaction
  correctness, and the per-batch multi-file fsync.
- Verdict: it delivers the owner's goals, but laying files out by key buys
  nothing a single log plus an in-memory index does not, and costs a fsync per
  touched file per batch. Key layout matters for a store that must find
  records on disk without an index in memory; wr already holds every live job
  in memory and can hold a compact index of history.

## Measured (NFS, see benchmarks.md)

- Production shape (6,000 runners, 122 runs/s, 200 adds/s): reserve p50
  0.81ms, p99 5.3ms; 1000-job add p50 52ms. Indistinguishable from D2.
- Saturation: 1,183 runs/s (out of jobs) at reserve p50 17ms against D2's
  1.6ms, with 8.4 fdatasyncs per batch; at 5,150 runs/s, p50 21ms against
  10ms, with 59 syncs per batch.
- Recovery, 800k 10KB jobs: 12.4s cold, 3.0s warm (64 files read by 8
  streams), against 37.6s / 7.1s for D2's single file read by one stream.
- Crash tests: 16 kill -9 rounds, no acknowledged write lost; 320 truncations
  and byte flips recovered to a prefix.
- Files: 64 + 6 regardless of job count until buckets are segmented; see the
  table above.
