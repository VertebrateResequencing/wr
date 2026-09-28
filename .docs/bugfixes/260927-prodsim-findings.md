# Bugfixes 2026-09-27: what the production-shaped soak found

Found by the new soak, `developers/wrdev.sh prodsim`, on its first 4h run
(`/nfs/hgi/wr/sb10-bigdb/prodsim/prodsim-1790504951/`). Real LSF, harmless
commands. The manager was an isolated prod-mode one (ports 51792/51793, LSF
names `wrpiso51792_*`) on a copy of the 7.4GB `aslfixture.db`.

Each finding was fixed in its own PR:

| finding | severity | fixed by |
| --- | --- | --- |
| 1. Archived jobs stay in the manager's heap | high | #633 |
| 2. A write that crosses the mmap size stalls the DB behind a backup | high | #632 |
| 3. Opening the status page decodes whole histories | medium | #636 |
| 4. Go clients never re-read the token after a clean restart | medium | #635 |
| 5. Normal client behaviour logged as errors | low | #637 |

Host load: the soak did not record load until after this run (the sampler now
does). Other agents ran heavy test suites on the same host that day, with load
around 90 at times. So treat the soak's absolute latencies as upper bounds.
Findings 1 and 2 have deterministic reproducers that do not depend on load.

## 1. Archived jobs stay in the manager's heap (high)

Fixed by #633. Every job that had gone through the queue stayed reachable
after it was archived, `Cmd`, env and all. The heap grew with the peak
population of every reserve group the manager had ever seen.

### Mechanism

This describes the code before #633.

`subQueue.Pop` (`queue/subqueue.go:441`) truncates the slice with
`q.setItemList(itemList[:lasti])` and never clears `itemList[lasti]`. The
popped `*Item` stays in the backing array, and with it `item.Data()`, which is
the `*Job`. `heap.Pop` and `heap.Remove` both end in this method, so it covers
every way an item leaves the ready, run and delay sub-queues. `buryQueue.pop`
(`queue/bury_queue.go:52`) has the same bug. `buryQueue.remove` and
`dependencyQueue.remove` clear the slot, with a comment saying why.

The ready sub-queue keeps one slice per reserve group in `groupedItems`, and
no path ever deletes a group from that map. An empty group's slice therefore
keeps its capacity and its stale pointers for the manager's lifetime, until a
later push outgrows it.

The retained memory is the sum, over every (sub-queue, reserve group) pair
ever used, of that pair's peak length:

- A workload that reuses its groups plateaus at its biggest burst.
- A workload that keeps making new groups grows without bound. Production
  does this: the reserve group ends in `~<limit groups>`, and `wrstat multi`
  gives every run a fresh `datetime<...` limit group. Memory learning and new
  req groups also change reserve groups.

### Evidence

1. **Soak.** One portal burst ran 2 x 20,000 jobs with 20KB commands, and all
   of them completed. With only ~22k tiny live jobs left, a forced-GC heap
   profile at 11:48 showed **524.74MB in a single stack of 21.25kB strings**
   (`codec.detach2Str` <- `Server.handleRequest`), which is about 24,700
   archived portal jobs. `HeapInuse` stayed at 0.8-1.4GB until the 13:00
   restart. That restart recovered the same 31,704 live jobs, and
   `HeapInuse` fell to 194-205MB.
2. **Growth, not a plateau** (`wrdev.sh retention-check`, local scheduler).
   Each round adds 1,000 jobs with 20KB commands, and nothing is live after
   any round:

   | round | own limit group per round | same limit group every round |
   | --- | --- | --- |
   | 1 | 28MB | 28MB |
   | 2 | 52MB | 28MB |
   | 3 | 76MB | 29MB |
   | 4 | 99MB | 29MB |
   | 5 | 123MB | 29MB |
   | 6 | 147MB | 29MB |

3. **The line is the cause.** The A/B was a local build that only added
   `itemList[lasti] = nil`, and it was reverted.
   A queue-level reproducer (since replaced by #633's untagged
   `TestRemovedItemsAreCollectable`) went from 0 of 2,000 removed items
   collectable to 2,000 of 2,000. `retention-check 4 1000 20` went from
   29 -> 102MB (FAIL) to 4 -> 5MB (PASS).

Production estimate: an 84k-job portal run with 25KB commands leaves about
2.1GB behind after it completes, and every later run in a new group adds its
own peak. Only a restart clears it, and production has been up since
2026-09-14.

Gates:

- #633's untagged tests: `queue/retention_test.go`
  (`TestRemovedItemsAreCollectable` and others) and
  `TestServerArchivedJobsLeaveTheHeap` in `jobqueue/`.
- `developers/wrdev.sh retention-check`, end to end against a manager. It
  fails on develop until #633 merges.

#633 clears the popped slot in `subQueue.Pop` and `buryQueue.pop`, and
deletes a ready group from `groupedItems` when it empties.

## 2. A write that crosses the mmap size stalls the database until the backup finishes (high)

Fixed by #632. While the periodic backup copy held its read transaction, any
commit that grew the database file past bbolt's current mmap size stalled
every database read and write in the manager until the copy ended.

### Mechanism

This describes the code before #632.

`backupToBackupFile` (`jobqueue/db.go:5095`) runs the whole copy inside one
`db.bolt.View`, and `copyBackup` (`jobqueue/db.go:947`) even does the
`f.Sync()` of the multi-GB copy inside that transaction (`:960`). The backup
runs about half the time: the gap between copies is max(30s, the previous
copy's duration).

The database is opened with default options (`jobqueue/db.go:2891`, no
`InitialMmapSize`). Above 1GiB, bbolt maps in whole-GiB steps. When a commit
allocates past the mapping, `db.allocate` calls `db.mmap`, which takes
`mmaplock.Lock()`. That waits for every open read transaction to close, with
the writer lock held. Meanwhile each new `View` blocks in
`mmaplock.RLock()` behind the pending `Lock`.

So every jstart (durable since #606), jarchive, add, and every status request
that reads the database waits for the rest of the backup copy. A long read
transaction also stops freed pages being reused, so the file grows fastest
exactly while a backup is running.

### Evidence

- **Soak.** Between the 14:06:20 and 14:06:52 samples, the file grew from
  7050MB to 7181MB. That was its first growth in 2.6h, and it crossed the
  7168MiB mapping. From ~14:07:10 to 14:08:06 every database-touching
  request stalled:
  - 583 "slow request" lines in the 14:08 minute: 311 jstart, 206 jarchive,
    59 reserve, plus adds, getbr and getrs.
  - The longest waits were 41-55s, and a `getin state=buried` took 64.6s,
    past the 60s client floor.
  - The client side agrees. `add_portal_compress` took 51s,
    `status_portal_summary` 55s, `status_buried` 65s, and a trivial
    `getlct` 6s.
  - `wr status -o counts`, which only reads memory, stayed at ~0.5s
    throughout. So it was a database stall, not a whole-manager freeze.
  - Two `jtouch ... bad job` errors followed at 14:08:09.
- **Deterministic, in-process, through wr's own `initDB` and
  `backupToBackupFile`:** `TestProdsimBackupRemapStall`. With the backup copy
  slowed to ~8s:

  | writes during the backup | worst trivial read | worst small write |
  | --- | --- | --- |
  | stay inside the mapping | 1ms | 17ms |
  | cross the mapping | **6.494s** | **6.554s** |

  On NFS (`wrdev.sh remap-stall-check`, 9.4s copy) the same two rows were
  8ms / 224ms and **8.289s / 8.388s**.

Production estimate: the DB is 10.9GB. `archive-ceiling` measured the backup
streaming at 75.8MB/s, so one copy takes about 2.4 min. Each GiB of growth
that lands during a copy stalls the database for up to that long, well past
the 60s client floor. That is the lost-report and TTR-churn failure the
reliable4 work was about. The same stall follows any other long read
transaction, such as an unbounded history scan.

Gates:

- #632's untagged `TestDBBackupRemap` holds a backup copy open and checks
  that a growing write and a later read both finish during it.
- `TestProdsimBackupRemapStall` (`reliability_repro`, run by
  `developers/wrdev.sh remap-stall-check`) measures how long the stall is,
  on a chosen filesystem and at a production-like copy pace, next to a
  baseline whose writes do not cross the mapping. `WR_PRODSIM_REMAP_DIR`
  puts the DB on NFS and `WR_PRODSIM_REMAP_BACKUP_SECS` sets the copy's
  length. It fails on develop until #632 merges.

#632 maps the manager's DB with headroom (`InitialMmapSize` derived from the
file size at each start), so a remap while a manager runs is rare.

## 3. Opening the status page decodes the whole history of every live rep group (medium)

Fixed by #636. `writeStatusCountSeed` (`jobqueue/serverWebI.go:718`, loop at
`:724-735`) called `getCompleteJobsByRepGroup` for every live rep group. That
fully decodes every archived record of the group, `Cmd` and env included,
only to count them, and every such record is `complete`. It did this on each page load and
on each websocket reconnect, holding that connection's write mutex.
`retrieveCompleteJobStatusByRepGroup(rg, false)` already counted without
decoding.

Soak evidence, with 3 simulated users refreshing every 5-60 simulated
minutes:
- The seed path took 13-14% of all manager CPU in the 30s profiles at t=6851s
  and t=7790s, and 9.7% at t=8747s.
- `ws_seed` latency: 93-145ms mean in the first 75 min, then 3.1-8.4s mean
  (max 37s) once more rep groups with history were live.

## 4. After a clean restart, long-lived Go clients fail every call and never recover (medium)

Fixed by #635, which makes a Go client reload its token file after a bad-token
rejection. `wr manager stop` deletes the client token (`cmd/manager.go:1686`,
`deleteToken`), so the next start mints a new one. A `jobqueue.Client` read
the token once, in `Connect`, and sent it with every request
(`jobqueue/client.go:4459`). Nothing re-read it on reconnect.

In the soak, after the graceful restart at 14:33, every Go-client actor
failed every call with `bad token: permission denied` for the rest of the run
(57 min, ~75 errors per 5 min). The actors were the ibackup server, the fofn
watcher, portal, wrstat, wrstat-ui and the waiter. CLI pollers and the web
users re-read the token and were fine. In production this meant ibackup's
server and fofn watcher, and wrstat-ui watch, silently stopped submitting
after an operator's clean restart until they were restarted themselves. A
crash-style restart keeps the token and is unaffected.

## 5. Normal client behaviour logged as errors (low)

Both fixed by #637.

- wrstat-ui's hourly `SubmitJobs` of an empty list logged
`jobqueue add(): bad request (missing arguments?)`. Production logged this
every hour; the soak logged it 33 times.
- Every finished `SubmitJobsAndWait` / `WaitForJobs` logged
`jobqueue waitForUpdates(): subscription closed`, 69 times in the soak's
last hour. The unsubscribe ends the in-flight long poll, and
`handleWaitForUpdates` (`jobqueue/serverCLI.go:919`) returned it as
`ErrBadRequest`.

## Known, re-observed

`wr status -i portal -z -o summary` (getrs by substring) took 2.2s median and
18.6s at p95 while 40k portal jobs with 20KB commands were in the history. This
is the O(history) status path that `.docs/reliable4/background.md` "Reserved"
already names, so no new PR was opened for it.

## Harness mistakes in the first run, so its data is not misread

- 13:00-13:10. Side experiments sourced `wrdev.sh` without `PROD_PORT` and
  rewrote the shared config to the default isolated port 51782. That port is
  still isolated, not production. The scheduled restart then came up on 51782,
  so clients got connection refused and send timeouts until a manual restart
  on 51792 at 13:10. That was followed by 379 slow requests, the reconnect
  storm after recovery. Ignore that window. `wrdev.sh prodsim` now writes a
  config dir of its own for each run, and checks with `wr conf` before every
  manager start and stop that the deployment still resolves to its isolated
  port and managerdir.
- `wrdev.sh` was edited while the soak's copy of it was running. After the run
  ended, bash read the new bytes and executed lines of the usage text as
  commands. They were harmless: an unknown `wrstat` subcommand and
  "command not found". Cleanup still ran and left 0 LSF jobs. `wrdev.sh` now
  runs its dispatch and exits on one line, so bash never reads past it, but
  still do not edit `wrdev.sh` while a mode is running.
