# Could wr's manager replace bbolt with a purpose-built store?

Exploration on branch `customdb-f397b547` (develop 9fc0d795). Contents:

- [requirements.md](requirements.md): what the manager actually needs to be
  durable, every write kind and query shape, rates at production scale, and
  which recent problems a different store would or would not remove.
- Six designs, each with how it serves every write and query, crash
  recovery, fsync strategy, memory, backup, compaction, file counts,
  migration and risk:
  [D1 key-laid-out files (the owner's idea)](d1-keyfiles.md),
  [D2 write-ahead log + memory + checkpoints](d2-wal.md),
  [D3 immutable specs + state slots](d3-slots.md),
  [D4 SQLite in WAL mode](d4-sqlite.md),
  [D5 local-disk primary + replication to NFS](d5-local-primary.md),
  [D6 runners as the source of truth, with leases](d6-runner-truth.md).
- [benchmarks.md](benchmarks.md): methods and numbers.
- [proto/](proto/): the throwaway prototype (its own Go module; the root
  module's `go list`, `make lint` and `make test` do not see it).

## Answer

Yes. wr's durability problem is small and regular, and a purpose-built store
solves it much better than a general B+tree on NFS. On the same NFS mount, in
the same hour, with the battery10 load shape (6,000 runners, 122 runs/s,
1000-job adds of 10KB jobs every 5s, 120k live jobs):

| | reserve p50 / p99 | 1000-job add p50 | saturation |
| --- | --- | --- | --- |
| Append-only group-commit log (D2) | 0.80ms / 4.0ms | 58ms | ran out of jobs at 5,150 runs/s (p99 57ms) |
| bbolt, wr's layout, ONE writer, small run state (kinder than develop) | 1.6s / 4.8s | 2.2s | 405 runs/s at p50 3.8s |
| SQLite WAL | 22ms / 2.1s | 2.5s | 399 runs/s |

Every acknowledged write was fdatasynced in all three. Kill -9 rounds (16 for
each of D1, D2 and SQLite, 32 for D3) lost no acknowledged write, and 1,280
random truncations and byte flips of the logs all recovered cleanly to a
prefix.

The owner's intuition is right that the in-memory queue is not the problem
and that the cost is in durability, but the measurements narrow down what
part of durability:

- **It is not fsync as such.** An idle NFS fdatasync of a small append costs
  0.7ms, the same as this host's local disk. bbolt with `NoSync` on NFS still
  had reservation p99 of 1.9s and 1.6s adds. What is slow is bbolt's write
  pattern on NFS: every commit rewrites thousands of scattered pages (a
  1000-job add is about 10MB of overflow pages plus index pages), holding the
  only write lock while it does, and the soak's "85% of lock time is
  fdatasync" is the time to flush those pages, not the cost of a sync. A log
  writes the same logical bytes sequentially, and a 192-byte transition does
  not wait behind an add's 10MB because they share one batch.
- **What any design keeps:** an NFS stall (the soaks' injected FUSE stall,
  ENOSPC) stalls every durable write in every design; only a local-disk
  primary (D5) or not writing on the hand-out path (D6) avoids it. Recovery
  that reads every live job's spec is bounded by NFS read bandwidth (8GB of
  800k 10KB jobs took 12-38s for the log designs); only lazy spec loading
  (D3) or a local primary avoids it. Host crashes need fdatasync and CRC
  framing in any design.
- **What a simpler store genuinely removes:** the multiple writers and their
  priority and ordering machinery (`beSeq`, change-versus-exit skipping,
  CRC-matched run-state overlays, kick ordering), the 10s ReserveWriteWait
  and "handed out before recorded" escape hatch, full-record rewrites
  overwriting run state (260929), whole-file backup copies and their stalls
  and pinned pages, mmap remap stalls, freelist walks and syncs, page-cache
  prefetch tricks, and the growth of commit cost with database size.
- **Encode/decode:** a hand-written codec (`proto/internal/flat`) encodes a
  10KB job in 205ns with 0 allocations (Binc: 2.7us) and decodes it in 160ns
  with 0 allocations into a reused Job (Binc: 11.4us, 13 allocations); the
  192-byte run state encodes and decodes in 32ns with 0 allocations.

## Recommendation

Go down the custom-storage route with **D2: one append-only, group-committed
log of small typed records, the live state in memory as today, periodic
checkpoints, and completed jobs moved into immutable history segments with
purpose-built indexes**, using the `flat` codec. Take from the owner's D1 its
per-query-shape history indexes (rep group postings with counters, end-time
index, dep group postings), but not its key-laid-out files: at saturation,
fsyncing every bucket a batch touches made D1 2-10 times slower than one log
(17ms against 1.6ms p50; 59 syncs per batch at 5,150 runs/s), and the one
thing key layout bought, parallel recovery reads (12s against 38s cold for
800k), comes equally from reading a segmented log in parallel. Keep D3 (lazy
specs) and D6 (leases) as later, optional phases; reject SQLite (D4: 30-500x
slower than a log on NFS, page-based, a large dependency) and treat D5 (local
primary) as a deployment option the owner can choose once D2 exists, since it
is D2 plus a 300-line shipper (replica lag 217ms plus up to 1s to sync).

Why D2 over the alternatives:

- It is the smallest thing that removes the problems: a log writer and
  reader (about 300 lines in the prototype), record types, a checkpointer.
  The prototype's writer, with CRC framing and tail truncation, passed every
  crash test unchanged.
- One writer, one sequence: every ordering rule in today's write path becomes
  "apply records in log order, newest per-job sequence wins".
- Bytes, not pages: commit cost is one fdatasync per batch plus the batch's
  bytes; 15,450 durable transitions/s plus 1,000 adds/s still had p99 57ms on
  NFS.
- It keeps the `db` type's API, so `server*.go` and `queue` change only where
  workarounds are deleted.

Why not continue with bbolt and the delivery queue (writer priority,
transaction caps, a 20s ReserveWriteWait, per-writer stats): those make the
current design degrade more gracefully, and bursts-findings.md's option B is
the right next step if the custom route is not taken. But the measurements
above are of bbolt with those problems already solved (one writer, small
run-state records, folding everything into one transaction), and it was
still 2,000 times slower than a log at p50 on NFS, with second-scale tails
from every add. Priority and caps redistribute that latency; they cannot
remove it, and each new soak at higher concurrency has found the next
saturation point.

## Plan and phases

| Phase | Work | What it fixes | Effort |
| --- | --- | --- | --- |
| 0 | Freeze the `db` API as an interface; port the prototype's crash test and a record/replay harness to `jobqueue`; add per-writer stats to develop (useful either way) | Nothing yet; makes the swap testable | 1-2 weeks |
| 1 | Live tier on the log: specs at add, state records for every transition, archives as completion records; checkpoints; recovery = snapshot + replay. History stays in bbolt, filled asynchronously from the log by a background folder that is never on a reply path (idempotent, resumable from a log position) | Every hot-path write and its stalls; ReserveWriteWait hand-outs; ordering machinery; recovery read volume becomes the live set only | 4-6 weeks incl. converter, wrdev crash/recovery modes, `make speed`, one production-scale soak |
| 2 | Replace bbolt history with immutable segments and the per-query-shape indexes; incremental backup by shipping new files; drop bbolt, prefetch, freelist and backup pacing code | Backup stalls, whole-file copies, file growth and compaction, slow status for big rep groups | 4-6 weeks |
| 3 (optional) | Lazy specs: the queue holds the 192-byte state and a spec reference, not the whole Job (D3's recovery: 154MB and 0.6s for 800k jobs instead of 8GB) | Recovery time on NFS, 20-25KB heap per live job | 4+ weeks, touches `queue` and every `job.Cmd` user |
| 4 (optional) | D6 leases, or D5 local primary | Hand-outs independent of NFS stalls | 2-4 weeks each |

Phases 0-1 are about 6-8 weeks of agent-driven delivery including soaks;
phases 0-2, which retire bbolt, about 3-4 months.

## Risks the custom route adds

- A storage engine wr owns: correctness rests on wr's tests, not on bbolt's
  years of use. The surface is small (append, scan, CRC, truncate; the
  prototype's crash tests and a fuzz test of the scanner should gate it), but
  checkpoints and history indexes are where bugs would hide: a checkpoint
  must be a consistent cut, and a stale postings entry would mis-answer
  status queries.
- No downgrade: once converted, an older wr cannot read the store (the
  converter keeps the bbolt file for rollback until the new store has run).
- Every tool that reads the database directly (`dbstart`, `statinspect`,
  `bboltexp`, `wr manager compact`, backup restore) needs a new
  implementation, and the soak tooling depends on some of them.
- The measurements were taken on a quiet NFS at night. Every design gets
  slower under daytime load; the relative ordering is what was measured.
  Phase 1's soak is the real gate.
- Process crashes were tested; host crashes (page cache lost) were only
  approximated by truncations and byte flips.
