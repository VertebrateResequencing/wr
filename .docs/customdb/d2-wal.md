# D2: one write-ahead log of small transitions, in-memory state, checkpoints

## Sketch

Event sourcing with group commit. The manager already keeps every live job
in memory; D2 makes the log of what happened to them the only durable record
of live jobs, and moves finished jobs into an immutable history tier.

- `wal/NNNNNN.log`: one append-only log, rotated at 256MB. Records are framed
  (`[len u32][crc32c u32][payload]`) and typed: `spec` (written once, at add:
  key + flat-encoded spec), `state` (192 bytes: key, per-job sequence, state,
  attempts, host, pids, reserved-by, runner reservation, times, usage,
  learned requirements), `std` (compressed std out/err of a failed run),
  `delete`, `modify` (old key -> new key + new spec), `limit`, `env`
  (content-addressed blob), `rerun-mark`, `checkpoint` (marker).
- One writer goroutine (`proto/internal/gc`): callers append a record to the
  current batch and get its durability channel; the writer swaps the batch,
  writes it with one `write()`, one `fdatasync`, and closes the channel. No
  lock is held across the sync; while one batch syncs the next fills.
- In memory: what the queue holds today, plus a per-key index of where each
  live job's spec record is (so a spec can be re-read rather than kept).
- Checkpoint (every N MB of log or M minutes, in the background): write a
  snapshot of every live job (spec + latest state) to `snap-<lsn>` with the
  log position it covers, fsync, then delete log segments wholly before it.
  Because specs are immutable, the snapshot can reference spec bytes by
  segment and offset instead of copying them, as long as those segments are
  retained; simplest is to copy them (the snapshot is then the size of the
  live set: 1.2GB at 120k x 10KB).
- History tier: archive appends a completion `state` record to the log. At
  checkpoint, completed jobs' spec + final state move into sealed history
  segments (`hist-NNNN.seg`, append order) with per-segment sorted indexes
  (key -> offset; rep group -> postings; end time range; dep group ->
  postings) and global counters per rep group. Sealed files never change, so
  backup copies only new files.

## How each write and query is served

| Operation | D2 |
| --- | --- |
| Add (1000 jobs) | 1000 spec records in one batch: one write of ~10MB, one fdatasync |
| Reserve, start, release, bury, kick, suspend, lost | One 192-byte state record, group-committed with whatever else is waiting |
| Archive | One state record (complete + usage) |
| Delete, modify, rerun mark, limit, env | One record each |
| Recovery | Load newest snapshot, replay log after its LSN, rebuild queue |
| Live queries | Memory (as today) |
| Is key complete / complete by key | History key index (in memory: 16B key -> segment+offset), then one read |
| By rep group, counts, oldest N, recent, last end time | Rep group postings and counters; end-time index |
| Dependents / dep group ever seen | Dep group postings (memory for live, history index for complete) |
| Std out/err, env, recommendations | Small maps in memory, rebuilt from the log and snapshot |

## Crash recovery

- Guaranteed: every record in a batch whose fdatasync returned. A torn tail
  (short frame or CRC mismatch) ends the log; recovery truncates there and
  carries on. Per-job sequence numbers make later records win.
- Lost: only records in an unsynced batch, none of which were acknowledged.
- Ordering: one writer and one sequence give a total order. The ordering
  machinery today (`beSeq`, change-versus-exit skipping, CRC-matched
  run-state overlays, kick ordering) reduces to "apply records in log order".
- Double runs: the reservation write is ~1ms on NFS when the filesystem is
  healthy, so the manager can always wait for it. During a filesystem stall
  every design stalls; then the choice is today's (hand out after a wait, and
  log it) or D6's.
- Crash tests: `proto/cmd/crashtest` kills the writer at random points; every
  acknowledged transition was recovered, and random truncations/bit flips of
  the log recovered cleanly to a prefix (`benchmarks.md`).

## Group commit and fdatasync

Throughput is bounded by bytes, not by commits: a batch costs one fdatasync
(~0.7ms on this NFS mount when idle, see fsyncbench) plus its bytes (about
150-270MB/s for large appends). Transitions are 209 bytes framed, so 10,000
transitions per second is 2MB/s. The dominant bytes are adds (2MB/s at 200
10KB jobs/s). Nothing rewrites pages, freelists or B+tree branches.

## Memory and allocations

- Encode/decode with `flat`: zero allocations per transition on the write
  path (state record encoded into a pooled buffer, copied into the batch).
- Memory: the queue as today. If specs are re-read on demand instead of kept
  in `Job.Cmd`, the 20-25KB/live-job heap seen in the runstate soak could fall
  to about 1KB, but that is a change to the server and queue, not storage.

## Backup and compaction

- Continuous backup: ship sealed log segments, snapshots and history segments
  as they close (whole files, once each), plus the open segment's tail. No
  whole-file copy of a mutable store, so no backup stall and no pinned pages.
- `wr manager backup` streams the newest snapshot plus log tail (or a fresh
  checkpoint).
- Compaction is checkpointing; history segments can be merged offline.

## Files and inodes

Segments of 256MB; at 1.3KB per job (production) and 10KB (portal):

| Stored jobs | Files |
| --- | --- |
| 1M | 2-4 log segments + 1-2 snapshots + 5 (1.3KB) to 40 (10KB) history segments + indexes ≈ 15-60 |
| 5M | ≈ 30-200 |
| 10M | ≈ 60-400 |

## Migration

Offline converter: read the bbolt file once, write a snapshot of live jobs
and history segments (with indexes) from `jobscomplete`. Keep the bbolt file
for rollback until the new store has run.

## Complexity, risk, how much of wr changes

- New code: log writer and reader (done in the prototype, ~300 lines),
  record types, checkpointer, history segments and indexes, backup shipping,
  converter, a `wr manager` inspection tool: about 3-5k lines plus tests.
- `db.go`'s API can be kept, so `server*.go` changes are limited to dropping
  workarounds (ReserveWriteWait hand-out, run-state overlay, `beSeq`,
  separate writers, backup pacing, prefetch).
- Risks: checkpoint correctness (the snapshot must be a consistent cut; take
  it from a copy-on-write view or by noting the LSN and replaying), history
  index bugs, and an unbounded log if checkpoints fail.
