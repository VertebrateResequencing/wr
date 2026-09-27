- [x] Every DB read and write stalls until the backup copy finishes if the file
  grows past bbolt's mmap size during a backup. Before this fix,
  `backupToBackupFile` copied the whole file inside one read txn, including
  the fsync, and `openManagerBolt` opened the DB without InitialMmapSize. A
  write that needs a remap waits, holding the writer lock, for that read txn
  to close (bbolt's mmaplock), and every new read queues behind it. In the
  soak, a 7GB file crossed 7168MiB and caused 583 slow requests in a minute,
  with 41-64.6s waits (past the clients' 60s timeout). Production: a 10.9GB
  DB, whose copy takes about 2.4 min. (Prodsim finding 2,
  `.docs/bugfixes/260927-prodsim-findings.md` on branch `prodsim`.)
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestDBBackupRemap -v`
    (new test, `jobqueue/db_backup_remap_test.go`). It holds a real
    `backupToBackupFile` copy open mid-copy through `backupPaceHook`, then
    makes a write that grows the small database past twice its size, and a
    read after it. Before the fix, exit 1:

    ```
    Line 142:
    Expected: true
    Actual:   false
    --- FAIL: TestDBBackupRemap (10.09s)
    ```

    The write, and the read queued behind it, each waited out the test's 5s
    bound. After the fix it passes in 0.21s.
  - The prodsim reproducer, `TestProdsimBackupRemapStall` (reliability_repro,
    ~100MiB DB, copy paced to ~7s), is kept on the prodsim branch and not
    ported: `TestDBBackupRemap` is its fast, deterministic equivalent. Run on
    this host with the file copied in (not committed):

    | | worst trivial read | worst small write |
    | --- | --- | --- |
    | develop, writes cross the mapping | 6.549s | 6.611s |
    | fixed, same writes | 0s | 40ms |

    `wrdev.sh remap-stall-check` (DB on NFS, ~10.1s copy) against this branch:
    PASS, crossing row `maxRead=47ms maxWrite=267ms`. The prodsim write-up
    records 8.289s / 8.388s for it before the fix.
  - bbolt v1.4.3 semantics (from the module cache): `db.allocate` calls
    `db.mmap(minsz)` only when a commit needs a page at or past `db.datasz`.
    `mmap` takes `mmaplock.Lock()`, which waits for every read txn
    (`mmaplock.RLock()`) and blocks new ones. It maps
    `mmapSize(max(fileSize, minsz))`: doubling up to 1GiB, then whole-GiB
    steps. So a running manager only ever grows the mapping 1GiB at a time,
    and without headroom every GiB of growth is a remap. `InitialMmapSize`
    is the only knob, and it applies only at open. On non-Windows it does not
    grow the file: `grow` truncates to the used size plus `AllocSize`.
  - Fix, `jobqueue/db.go`:
    - `openManagerBolt` passes `InitialMmapSize: managerInitialMmapSize(size)`,
      recomputed from the file's size at every open: size + max(size, 4GiB),
      capped at max(size, 1TiB), and 0 on 32-bit platforms. 10.9GB maps
      21.8GB. A remap during a manager run now needs the database to double
      (or grow 4GiB when small) in that run, where before any GiB of growth
      did it. The soak's file first grew after 2.6h, by 131MB in 32s, so one
      2.4 min backup window cannot cross the headroom.
    - If that open fails with ENOMEM (`ulimit -v`), it reopens with bbolt's
      default mapping, so a limited host behaves as before.
    - `copyBackup` now opens the read txn itself and fsyncs after it closes.
      The file already holds the txn's snapshot when the txn closes, so the
      backup is the same consistent snapshot, but the txn no longer spans the
      final fsync. The S3 path already stages the copy locally and uploads it
      outside the txn. A local backup defaults to the DB's own directory, so
      staging it elsewhere first would only double the I/O.
  - Trade-off: the mapping is `MAP_SHARED`, `PROT_READ` and file-backed. It
    costs virtual address space, plus page tables only for pages actually read.
    It uses no RAM or swap beyond what reading the file already did, and no
    overcommit charge (`vm.overcommit_memory=2` counts only private writable
    mappings). It does count against `RLIMIT_AS`, so under a `ulimit -v` it
    takes address space the heap could have used, and past the limit it falls
    back (above). Residual risk: a database that doubles during one manager
    run remaps again in 1GiB steps, each exposed to the stall. A restart
    re-derives the headroom.
  - Tests: `TestDBBackupRemap` (red, above; also checks the backup is the
    snapshot from before the write), `TestManagerInitialMmapSize` (sizing),
    and `TestOpenManagerBoltUnderAddressLimit`. The last re-execs the test
    binary, sets its own `RLIMIT_AS` to its current size + 1GiB, and opens,
    writes and reads a db. With the fallback removed it fails with ENOMEM.
    `TestDBBackupCopy` now calls `copyBackup(path)` without a caller-supplied
    txn. Its assertions are unchanged.
  - Gates: `make lint` 0 issues; `make test` 722 passed, 21 skipped;
    `CGO_ENABLED=1 make race` 722 passed, 20 skipped. None of the known
    develop flakes failed.
