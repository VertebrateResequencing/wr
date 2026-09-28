- [x] Cold start (high). Taking an flock on an NFS file makes the client drop
  its cached pages. bbolt flocks at open (openManagerBolt), and
  recoverIncompleteJobs then decodes the live bucket with one synchronous page
  fault after another, at 0.3-0.4ms each. A restart with 121k live jobs took
  185s, and the first status-page seed after a start took 11-53s. A sequential
  8-stream prefetch of the file after open cut recovery of 57,770 jobs from
  39.4s to 16.3s. (Prodsim round 2, probes on branch faux-develop:
  `jobqueue/prodsim_cold_recovery_test.go`,
  `/nfs/hgi/wr/sb10-bigdb/prodsim2/startup-exp/results.txt`.)
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run 'TestManagerDBPrefetch|TestPrefetchFile' -v`
    (new, `jobqueue/db_coldstart_test.go`). It forces the "this open drops the
    cache" answer and asserts that initDB read the whole file. Before the fix,
    exit 1: only `recovering: opened database` was logged, with no
    `prefetched database` line.
  - Timing probe (reliability_repro, `jobqueue/dbstart_probe_test.go`). It
    runs on a writable copy of the aslfixture, with its live bucket grown from
    20k to 120k jobs by `TestDBStartProbeGrowLive`:
    `/nfs/hgi/wr/sb10-bigdb/dbstart/fixture.db`, 7.4GB, 756k free pages.
    Every open's flock drops the NFS cache, so every run starts cold. Open plus
    decode of 120k jobs, NFS:

    | way of warming the file after open | total |
    | --- | --- |
    | none (develop) | 23.9-30.1s |
    | 8 parallel sequential reads, then decode | 9.9-10.4s |
    | 8 readers alongside the decode | 11.4s (20k jobs: no better than none) |
    | one `FADV_WILLNEED` for the whole file | no gain: 5.15 caps it at `ra_pages` (128KB) |
    | `FADV_WILLNEED` in 128KB steps | 10.8s (the issuing blocks for 7.5s) |
    | `MAP_POPULATE` (single stream) | 19.4s (20k jobs) |
    | decode in 16 parallel key ranges | 24.4-28.3s (faults don't overlap) |
    | fixed `openManagerBolt` (synced freelist) | 9.7-14.7s |
    | fixed `openManagerBolt` after a crash (walks, see below) | 13.7s |

    With 20k live jobs a whole-file read (8-10s) is no faster than faulting
    (3-9.7s, noisy). The fix still reads the whole file, because everything
    after recovery (status seed, rep-group lookups, the first backup copy,
    which reads the whole file anyway) then runs warm.
  - Local disk (`/tmp`, ext4) keeps its cache across a flock. Warm, an 8-stream
    read added 0.5s to a 1.0s start. Cold (evicted with `FADV_DONTNEED`), it
    took 22.6-24.8s against 15.3-27.4s for faulting. So a local file whose
    freelist is synced is opened exactly as before.
  - Fix, `jobqueue/db_prefetch.go`, `db_prefetch_linux.go`,
    `db_prefetch_other.go`:
    - `openBoltPrefetched` starts `prefetchFile` (8 parallel sequential
      streams of `ReadAt`, 1MiB chunks) and then opens the file. It waits for
      the read before returning, bounded by `managerDBPrefetchTimeout` (1 min;
      NFS reads about 900MB/s). If the open fails, it cancels the read. It
      logs `prefetched database` at warn with the bytes read, the size,
      `complete` and the elapsed time.
    - It prefetches only when `fileOnNFS` (statfs `NFS_SUPER_MAGIC`; false off
      Linux) or when the file's newest meta records no synced freelist
      (`boltFreelistSynced`, which reads the two meta pages). In the second
      case bbolt walks every page inside `Open`, so the read must start before
      the open, not after it. What it reads before the flock is dropped with
      the cache, which is one lock round trip's worth.
    - `openManagerBolt` (all of initDB's opens, including the backup check) and
      the offline compaction's source open (`compactBoltInto`) both use it.
      `wr manager compact` of the 7.4GB copy on NFS went from 6m24s to 47s.
- [x] Freelist rewrite on every commit (high). The manager opens bbolt without
  NoFreelistSync, so every commit writes the whole freelist, and bbolt's map
  freelist builds and sorts a slice of every free page id to do it. With 585k
  free pages, commitFreelist took 5-29% of CPU, and freePageIds/allocate made
  up 67% of all allocation. It also caused 86-95% of the lock delay on the
  bbolt writer lock. A commit took 80-100ms on the soak DB versus 26-32ms on a
  fresh one. NoFreelistSync alone is unsafe: reopening then walks the whole DB
  cold, which took 5-6 min on 9GB over NFS.
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run 'TestManagerDBCommitFreelistCost|TestManagerDBFreelistCrashSafety' -v`
    (new, `jobqueue/db_coldstart_test.go`). The first test counts the page
    bytes (bbolt `TxStats.PageAlloc`) that a one-key commit allocates in a
    manager DB with 16Ki free pages. Before the fix, exit 1:

    ```
    Line 78:
    Expected '143360' to be less than or equal to '16384' (but it wasn't)!
    --- FAIL: TestManagerDBCommitFreelistCost (0.35s)
    Line 157:
    Expected: 16920
    Actual:   16886
    --- FAIL: TestManagerDBFreelistCrashSafety (0.78s)
    ```

    The crash-safety test guards the fix: after a manager dies without closing
    (a raw `bolt.Close`, which commits nothing), a reopen must keep every job,
    pass bbolt's `Check` before and after further writes, and report exactly
    the free pages there were. On develop, the reopen's own commits spend 34
    free pages rewriting the freelist, so the count drops.
  - Options measured against bbolt v1.4.3 on the 7.4GB copy (756k free pages),
    one-key commits, medians of 50:

    | freelist | NFS commit | local commit | allocated per commit |
    | --- | --- | --- | --- |
    | map, synced (develop) | 53.2ms | 22.8ms | 12,232KB |
    | array, synced | 54.3ms | 24.7ms | 11,854KB |
    | map, NoFreelistSync | 1.23ms | 0.62ms | 14KB |

    - Array freelist: no cheaper. Its `freePageIds` returns its slice without
      copying, but `Write` still copies every id into the freelist page and
      writes it (6MB here). `mergeSpans`, on every release, sorts and allocates
      a fresh full-length slice (`Pgids.Merge`), and its `Allocate` scans the
      whole array to find the freelist's contiguous pages.
    - NoFreelistSync: removes the write entirely. `tx.Commit` sets the meta's
      freelist to `PgidNoFreelist`, and an open then rebuilds the freelist by
      walking every reachable page (`db.freepages`). That open took 5m07s cold
      on NFS and 2m51s cold on local disk. Alongside the 8-stream read it took
      11.5s on NFS and 22.5s on cold local disk; `MAP_POPULATE` took 24.1s and
      42.1s. Warm local: 2.7s for the walk alone, 2.5-3.1s with the read.
    - Compaction: `compactBoltInto` is only used by the offline
      `wr manager compact` (`CompactDBFileStats`). It took this copy from
      7050MB to 3063MB, in 47s with the prefetch. But free pages come back
      with churn: growing the copy's live bucket by 100k jobs, 500 per commit,
      took it from 585k to 756k free pages, and in the soak the count fell
      from 585k to 111k and then climbed back to 349k. Running it at startup
      would double the start time and the disk used. It does not fix the
      per-commit cost, so it stays an offline tool for disk space.
  - Fix, `jobqueue/db.go`:
    - `openManagerBolt` opens with `NoFreelistSync: true`.
    - `finaliseBackup` (so a clean close) calls `syncFreelist` after every
      write has drained and before the final backup. It makes one commit with
      `NoFreelistSync` set back to false (set inside the Update, so bbolt's
      writer lock orders it), so a clean restart reads the freelist and does
      not walk.
    - After a crash, the open walks, and `openBoltPrefetched` reads the file
      alongside it (item 1).
  - Crash safety: an unsynced freelist is bbolt's supported mode (etcd's
    default). The meta pages still switch atomically, and the rebuilt freelist
    is exactly the unreachable pages (asserted above). Every wr release since
    the 2018 move to bbolt can open such a file: v0.11.0 onwards pin a bbolt
    (v1.3.1-coreos.6 or later) that walks an unsynced freelist and writes it at
    an open without NoFreelistSync. Releases up to v0.10.0 (boltdb/bolt)
    cannot.
  - A clean close's synced freelist is never trusted after later commits:
    every NoFreelistSync commit writes `PgidNoFreelist` into its meta
    (`tx.Commit`), and frees the old freelist page only as pending.
    `TestManagerDBStaleFreelist` crashes after a clean close and many commits,
    and after one commit whose meta is torn (so bbolt falls back to the synced
    meta), and checks each reopen with bbolt's `Check`.
  - Trade-offs:
    - A restart after a crash walks the database. On NFS it is still faster
      than develop: 13.7s against 24-30s for 120k jobs. On local disk it costs
      more: +2.4s warm, and about +5s total cold (19.0s against 13.9s).
    - A commit that fails after spilling (eg. a write error) now rebuilds the
      freelist with a walk, where it used to re-read the freelist page.
    - Backups are `tx.WriteTo` snapshots, which copy the meta as it stands. A
      periodic backup so records no freelist, and restoring it walks the
      database (prefetched). The final backup at a clean close follows
      `syncFreelist`, so it carries the freelist.
    - The clean-close commit writes the freelist once, about 50ms on NFS here.
