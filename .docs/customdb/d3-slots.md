# D3: immutable spec segments plus a fixed-size state slot table

## Sketch

Two tiers that never mix: what a job is (written once) and where it is in
its life (overwritten in place).

- `specs/NNNN.seg`: append-only spec segments (framed, flat-encoded). A spec
  is written once at add and never rewritten; `wr mod` writes a new spec.
- `slots.dat`: a table of fixed 192-byte state slots, one per live job, each
  with its own CRC, the job key, a per-job sequence number and the spec's
  segment offset. Every transition overwrites the job's slot with `pwrite`.
  A finished job's final state is written to its slot and appended to
  `history.log` in the same batch; once that batch is durable the slot is
  free for reuse.
- One group-commit writer covers all three files: a batch appends specs,
  pwrites slots and appends history, then fdatasyncs each touched file in
  parallel (normally two or three).
- History tier: as D2 (sealed segments of spec + final state with indexes).
  The spec of a completed job is already in a spec segment, so history needs
  only the final state and index entries; spec segments whose jobs are all
  finished become history segments without copying.

## Why fixed slots

- Recovery reads `slots.dat` sequentially (800k live jobs is 154MB) and has
  every live job's state, key and spec location. It needs no log replay and no
  scan of specs. A manager that keeps whole Jobs in memory, as today, then
  reads the live specs by offset; one that loads specs lazily (when handing a
  job out, for status) starts with only the 154MB read.
- The live set's footprint on disk is bounded by the peak live count, not by
  how many transitions happened: a job that is reserved, started and
  released ten times still owns one slot.

## Writes and queries

| Operation | D3 |
| --- | --- |
| Add | Spec append + slot pwrite per job, one batch |
| Reserve, start, release, bury, kick, suspend | Slot pwrite (192 bytes) |
| Archive | Slot pwrite + history append, slot freed after durable |
| Delete | Slot pwrite (deleted), freed |
| Modify | New spec, slot pwrite with new key/spec offset |
| Std out/err | Side log, referenced from the slot (offset field) |
| Recovery | Read slots.dat; optionally pread live specs |
| History queries | As D2 |

## Crash recovery

- A process crash leaves every write the kernel accepted, so slots are never
  torn by kill -9.
- A host crash can tear a slot write that straddles a page or NFS write
  boundary: 192 does not divide 4096. Production use needs 256-byte (or 512)
  slots aligned to pages, and a two-copy scheme (A/B slot pairs, newest valid
  sequence wins) so a torn slot falls back to the previous state rather than
  to nothing. With a single copy, a torn slot loses the job's state and only
  the spec's existence remains: recovery would have to treat it as possibly
  running (quarantine, as in D6).
- Slot reuse is ordered by the writer: a slot is only reused in a batch after
  the one that made its previous occupant's history durable.
- Double runs: as D2.

## Group commit and fdatasync

A batch syncs 1-3 files. pwrite of scattered 192-byte slots costs the NFS
client a read-modify-write of each 4KB page it does not hold in cache (and the
server a partial-page write); with the slot table cached it is a small write
per dirty page. Measured numbers are in `benchmarks.md`.

## Memory and allocations

Key -> slot map plus free list: about 40 bytes per live job. State
encode/decode: 32ns, 0 allocations.

## Backup and compaction

- Spec and history segments are immutable once sealed: ship them once.
- `slots.dat` is small (154MB at 800k) and is copied whole, or its dirty
  pages tracked.
- Compaction: rewrite spec segments whose live jobs are few (copying live
  specs forward and updating their slots); history segments merge offline.

## Files and inodes

| Stored jobs | Files |
| --- | --- |
| 1M | 1 slot file + 5 (1.3KB) to 40 (10KB) 256MB spec segments + history index files ≈ 10-50 |
| 5M | ≈ 30-200 |
| 10M | ≈ 60-400 |

## Migration

Offline converter: live jobs to spec segments + slots, complete jobs to
history segments.

## Complexity, risk, how much of wr changes

- More moving parts than D2: slot allocation and reuse, A/B slots for torn
  writes, spec segment compaction with slot updates.
- Biggest payoff only if the queue stops holding whole Jobs (lazy specs),
  which is a large change to `queue` and `server` (every `job.Cmd` user).
- Same `db` API possible for the rest.

## Measured (NFS, see benchmarks.md)

- Production shape: reserve p50 0.77ms, p99 5.1ms; archive p99 24ms.
- Saturation: p50 57ms at 1,183 runs/s and 38ms at 5,150 runs/s: scattered
  192-byte pwrites cost more on NFS than appends.
- Recovery, 800k 10KB jobs: with every spec read, 12.4s cold / 2.7s warm;
  lazy (slot table only), 567ms cold / 133ms warm, reading 154MB.
- Crash tests: 32 kill -9 rounds (16 before and 16 after fixing the
  prototype's lost spec offsets), no acknowledged write lost; 640
  truncations and byte flips of the history log recovered to a prefix.
