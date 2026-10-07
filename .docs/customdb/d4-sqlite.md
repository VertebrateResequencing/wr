# D4: SQLite in WAL mode (embedded-engine baseline)

## Sketch

Keep a general embedded engine but pick one whose write path is a log:
SQLite with `journal_mode=WAL`. One table of jobs keyed by binary key, with
the flat-encoded spec and the 192-byte state as separate columns, and
indexes for rep group, liveness and end time. A single connection and one
group-commit goroutine fold every queued write into one transaction.

The prototype (`proto/internal/store/sqlitestore.go`) uses the pure-Go driver
`modernc.org/sqlite` (no cgo, which wr's static `netgo` builds need) with
`locking_mode=EXCLUSIVE`: SQLite's WAL normally needs a shared-memory `-shm`
file, which does not work on NFS; exclusive locking keeps the WAL index in
process memory instead, which is safe because only the manager opens the file.

## Writes and queries

| Operation | D4 |
| --- | --- |
| Add | `INSERT` per job in the group transaction |
| Transitions | `UPDATE jobs SET state=? WHERE key=?` (the row's spec is untouched, but SQLite rewrites the b-tree page holding the row, and a 10KB row spans overflow pages) |
| Archive | `UPDATE` state, done=1, endtime |
| Queries | SQL with indexes: `WHERE rg=?`, `WHERE done=0`, `ORDER BY endtime`; counts by `GROUP BY` or a counters table |
| Recovery | `SELECT spec, state FROM jobs WHERE done=0` |

## Crash recovery

SQLite's WAL is mature and well tested for process and power failure on a
local filesystem. `synchronous=FULL` syncs the WAL at every commit;
`NORMAL` syncs only at checkpoints, which survives a process crash but not a
host crash. On NFS, SQLite's own documentation warns against concurrent
access because of broken POSIX locks; with exclusive locking from one
process, the remaining risk is the same as any design: NFS honouring
fdatasync.

## Group commit and fdatasync

Each transaction appends the changed pages to the WAL (page-sized, 4KB, so a
192-byte state change costs at least one 4KB page plus index pages) and syncs
it. Checkpoints copy WAL pages back into the main file, a second write of
everything, done in the commit path when the WAL passes
`wal_autocheckpoint` pages unless run separately.

## Memory and allocations

Each row read allocates through `database/sql`; the pure-Go driver is about
2-3x slower than C SQLite. The prototype's numbers are in `benchmarks.md`.

## Backup and compaction

`VACUUM INTO` or the online backup API gives a consistent copy; it is still a
whole-database copy. `VACUUM` compacts offline.

## Files and inodes

Three files (`jobs.sqlite`, `-wal`, at most a `-journal`) at any number of
jobs.

## Migration

Offline converter from bbolt; straightforward.

## Complexity, risk, how much of wr changes

- Least new storage code: schema, statements, a writer goroutine.
- Adds a large dependency (`modernc.org/sqlite`, a C-to-Go translation) or
  cgo. Its write amplification is lower than bbolt's (WAL, no copy-on-write
  of branches) but still page-based, and checkpoints are a second write.
- It keeps the problems that come from being a general store: page rewrites
  for 200-byte changes, a whole-file backup, and opaque performance cliffs
  (checkpoint stalls, overflow pages for 10KB rows).

## Measured (see benchmarks.md)

- NFS, production shape: reserve p50 22ms, p99 2.1s, max 3.0s; archive p99
  2.8s; 1000-job add p50 2.5s (it shares the one writer, so transitions wait
  behind it). Local disk: reserve p50 1.6ms, p99 276ms.
- Saturation on NFS: 399 runs/s, reserve p50 4.5s.
- Recovery of 120k 10KB jobs on NFS: 44s cold and warm (row-at-a-time reads
  through `database/sql`).
- Crash tests: 16 kill -9 rounds, no acknowledged write lost.
