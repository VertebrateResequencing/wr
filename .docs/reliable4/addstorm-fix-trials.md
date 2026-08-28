# Making `wrdev.sh add-storm` pass: the trial log

## What this document is

`wrdev.sh add-storm` is the scale gate for the production symptom of 2026-08-27:
single-job `add`s taking tens of seconds, add throughput that barely rises with
client concurrency, and completion reports lost when a request outlives the 60 s
`ClientMinRequestTimeout` floor. At `HEAD` (`3365b48`) the gate FAILS. This
document is the working log of getting it to PASS: the seeded solution space, what
each trial did, what it measured, and what was learned - including the dead ends,
which are the point of writing it down.

Method, stated up front because the last round of work on this problem was
undone by not following it: **no assumption about cause or cure is trusted; each
idea is implemented in throwaway code and put in front of the gate.** The gate's
verdict is the only evidence that counts. A mechanism-level measurement
(transaction counts, mutex profiles) may explain a result but can never establish
one.

## The gate, and what passing it demands

`add-storm [low=20] [high=700] [secs=120] [thinkMs=2000]` serves a real manager
over the real socket, with `low` then `high` real clients each looping
think-then-add-ONE-job against a copy of a big production-shaped DB on NFS with
backups streaming. It PASSes only if all of:

| check | bound | at HEAD |
| --- | --- | --- |
| `overFloor` / `timedOut` | 0 | 0 |
| `overSlow` (adds over the 10 s slow-request threshold) | 0 | many |
| `highP50Ms` | <= 5000 | 28,900 |
| `throughputFactor` (high rate / low rate) | >= 10 | 1.93 |
| `txnsPerAdd` | <= 1.00 | 0.58 |

A missing or non-discriminating measurement is also a FAIL.

### What that arithmetic actually requires

The low phase offers 20/2 s = 10 adds/s and is not queued, so it achieves close to
10/s. `throughputFactor >= 10` therefore requires the high phase to sustain
**>= ~100 single-job adds/s**; its offered rate is 700/2 s = 350/s.

The measured facts that bound this:

- a bolt write transaction on this fixture costs **52-74 ms whatever it carries**,
  because `Tx.Commit` rewrites the whole freelist (475,598 free pages = 3.6 MiB)
  and fdatasyncs twice;
- so the database's write-transaction rate is fixed at roughly **11-16/s**;
- HEAD measured 18.44 adds/s at `txnsPerAdd` 0.58, i.e. ~10.7 txn/s - **the single
  writer is already saturated**.

Therefore **100 adds/s requires `txnsPerAdd` <= ~0.1, and 350/s requires ~0.03.**
Either an add's writes must ride in a transaction shared with 10-30 other adds, or
the per-commit cost must fall by an order of magnitude. Those are the only two
levers, and the solution space below is organised by which one an idea pulls.

### Why bbolt cannot do this by itself

`DB.Batch` sets `db.batch = nil` at the *top* of `batch.run()`, before it calls
`db.Update`. So the instant a batch starts, every subsequent arrival forms a *new*
batch on a *fresh* `MaxBatchDelay` (10 ms) timer, whose timer fires and whose
`Update` then queues on the writer lock. During one 90 ms commit that makes ~9 new
transactions, each carrying ~1/9 of the arrivals. bbolt coalesces *within* a 10 ms
window and never *across* a commit, which is exactly the wrong way round when a
commit costs 90 ms.

That also explains the mutation control: `WR_MANAGERDBBATCHDELAY=500` makes the
window wider than a commit, so a window's worth of arrivals lands in one
transaction, and the reproducer went from 12.82 to 179 adds/s.

## Fixture and how to run a trial

```
WR_AS_DB=/nfs/hgi/wr/sb10-bigdb/prod.db          # 7.38GiB, 475,598 free pages (24.6%)
WRDEV_AS_WORK=/nfs/hgi/wr/sb10-bigdb/aswork      # room for the copy + its backup
./developers/wrdev.sh add-storm 20 700 120 2000  # full gate, ~7 min
./developers/wrdev.sh add-storm 20 700 45 2000   # triage, ~4 min
```

The DB copy takes 33 s. Host is `farm22-wrstat01`, 8 cores, load ~125 - almost all
of it ~120 `find` processes in D state on NFS, so the fixture's storage is
contended in a way production's also is.

Fixture caveat, recorded so a PASS can be read honestly: this DB has **4.8x
production's free-page count** (475,598 vs 99,443 after production's 2026-08-25
compaction), so its per-commit fixed cost is several times production's and the
gate is correspondingly harsher. Passing on it is a strict result. Failing on it
while passing on a production-shaped copy would be a real finding, not an excuse -
`/nfs/hgi/wr/lsf/.wr_production/db_predep-granularity` (7.1 GB, static) is a
current-shape copy available for exactly that comparison.

## The seeded solution space

Organised by the lever each idea pulls. `txns/add` = fold more adds into one
commit. `ms/commit` = make each commit cheaper. `off-path` = stop the client
waiting for a bolt commit at all. `other-ceiling` = things that are not bolt.

### Lever 1: fewer write transactions per add (`txns/add`)

- **A1 - add-path group-commit writer.** A single long-lived goroutine that owns
  every add's bucket writes, mirroring `archiveWriter`/`applyArchives`: each wake
  folds *every* currently-pending add into ONE `db.bolt.Update` and replies to each
  caller individually, with the same per-caller error isolation (retry the batch
  minus the failing member, then re-run that member alone). Self-tuning by
  construction: whatever arrives during a commit rides in the next one, so the fold
  factor rises with load and is 1 when idle. This is the doc's "what to consider
  next" item 2.
- **A2 - generic group-commit `Batch` replacement.** Same machinery, but as a
  `db.batchWrite(fn func(*bolt.Tx) error) error` that replaces *every*
  `db.bolt.Batch` call site (`storeNewJobData`, `storeNewJobDataChunked`,
  `storeLimitGroups`, `storeLookups`, `storeBatched`, `store`, `storeEnv`,
  db.go:2743, db.go:3613). Strictly more coverage than A1 for the same mechanism.
- **A3 - one writer for the whole database.** Fold adds, archives and best-effort
  updates into a single writer, so all write paths share one commit and none can
  starve another. Removes the cross-path queueing the production mutex profile
  showed (archives queued behind adds) as well as the fragmentation. Needs a
  size/time cap so one commit cannot grow without bound.
- **A4 - adaptive `MaxBatchDelay`.** Measure recent commit latency and set
  `db.bolt.MaxBatchDelay` to about that (bounded, e.g. 10-250 ms), so bbolt's own
  window is wider than a commit under load and stays at 10 ms when idle. Tiny
  change, no new machinery, and it is what the mutation control proves works - the
  only question is whether the idle-latency objection recorded against A5 survives
  being made load-dependent.
- **A5 - just change the default `MaxBatchDelay`.** The shipping knob, evaluated
  rather than left unexamined (doc item 1). Cheapest possible change; the known
  objection is idle-manager latency.
- **A6 - fix bbolt.** Fork/`replace` bbolt so `batch.run()` detaches `db.batch`
  only *after* acquiring the writer lock, making `Batch` a true group commit for
  every caller. Upstreamable, and would need no wr-side machinery at all.
- **A7 - held-open transaction with a commit ticker.** One write transaction kept
  open, adds applied into it as they arrive, committed every N ms or M operations;
  each caller waits for the commit that includes it. Saves the per-transaction
  begin/spill cost too. Risk: a long-open RW transaction pins the freelist and
  delays page reuse, and a panic mid-transaction loses a whole window.

### Lever 2: cheaper commits (`ms/commit`)

- **B1 - `NoFreelistSync: true`.** Removes the 3.6 MiB freelist write from every
  commit - the single biggest component of the fixed cost. Previously DROPPED on
  the grounds that it breaks the fast-startup invariant, because bbolt then
  reconstructs the freelist by walking every page in the file on open. That cost
  has never been measured on a 7.4 GB DB. Measure it. If it is seconds, this is the
  cheapest big win available; if it is minutes, it is dead on its own but may live
  combined with B2 or B3.
- **B2 - persist the freelist out of band.** Keep `NoFreelistSync` but write the
  freelist ourselves periodically and on clean shutdown, so the rebuild scan
  happens only after an unclean stop. Needs bbolt cooperation (A6-style fork) or a
  sidecar file that bbolt would not read.
- **B3 - online compaction.** Periodically `bolt.Compact` the live DB into a new
  file and swap, keeping the free-page count - and therefore the freelist write -
  small. wr already has the compaction code. Attacks the fixture's 24.6% free
  pages head-on, and is the code version of the operational lever that already cut
  production's freelist by 45.9%.
- **B4 - larger page size.** A 4 KB page DB with 475,598 free pages would have
  ~59,000 at 32 KB, so ~1/8 the freelist bytes per commit and fewer branch pages
  to write. Page size is fixed at creation, so this means migrating via compaction
  into a bigger-page file. Untested, mechanically simple, and independent of every
  other idea here.
- **B5 - fewer free pages at source.** Find and stop whatever churns the live
  bucket hard enough to leave a quarter of the file free. Diagnostic first: which
  bucket's pages are free?

### Lever 3: take the commit off the request path (`off-path`)

- **C1 - split the live set from the archive.** Live jobs and their lookups in a
  small database, complete jobs in the big one written only by the archive writer.
  An add then commits against a ~100 MB file: small freelist, cheap fsync,
  sub-millisecond. Attacks the per-commit floor rather than the transaction count,
  and makes the backup incremental as a side effect. The doc's Idea 2, never
  started. Go/no-go is the two-store atomicity argument: `archiveJob` must move a
  record between files, so recovery has to tolerate a job present in both.
- **C2 - write-ahead log for adds.** Append the add's encoded records to a
  sequential log and fsync it (one fsync, no freelist, no random pages), reply to
  the client, and fold the log into bolt asynchronously in large batches; replay
  on recovery. Preserves the durability contract `createJobs` documents while
  removing bolt's fixed cost from the client's wait. Live jobs are served from the
  in-memory queue, so nothing reads the live bucket until recovery - which is what
  makes this tractable. The lookup buckets that *are* read by status queries need
  care.
- **C3 - reply on in-memory-queue + WAL only.** C2 framed as the minimum change to
  the durability contract, for comparison.
- **C4 - reply before durability.** Recorded and rejected: `createJobs` documents
  why it waits, and a workflow that loses a stored job breaks hopelessly.

### Lever 4: other ceilings, which may sit behind the bolt one (`other-ceiling`)

Each of these must be *measured*, because a fix on levers 1-3 that lands on a
second ceiling will look like a failed fix.

- **D1 - other writers competing during the storm.** Which other `Batch`/`Update`
  callers fire while adds are in flight (scheduling state, job change updates from
  the mock runners' state changes, `storeEnv`)? Cheap wins if any is unnecessary.
- **D2 - the backup.** Size its contribution by running with `WRDEV_AS_BACKUP=0`.
  Earlier work found the backup hurts archives, not adds; confirm that here rather
  than inherit it.
- **D3 - the queue mutex and the ready-add-check.** The storm leaves tens of
  thousands of ready jobs blocked by `addstorm`'s limit of 50, which is the exact
  shape of the bounded-rescan stall fixed in `85db7b2`. If `rac` or `enqueueItems`
  serialises against the add path, add latency will not fall however cheap the
  commits get.
- **D4 - `checkIfComplete` read amplification.** Every add does a bolt `View` and
  `Get` against a 2.87 GB, 2.15M-record bucket on NFS. Reads do not take the writer
  lock, but they do take page faults against an mmap of a file on NFS.
- **D5 - the socket and TLS.** 700 connections, mangos receiver/sender goroutines,
  and whatever serialisation the server's request handling imposes.

### Out of scope unless proven

- **Changing the gate.** The fixture's 4.8x freelist inflation is a real
  difference from production and is recorded above, but "the gate is too hard" is
  a conclusion to be *earned* with a passing run on a production-shaped copy, not
  an escape hatch.

## Trial order

Cheapest and most informative first; escalate only on evidence.

1. **T0 - baseline at HEAD.** Confirm the FAIL in this session, on this host, with
   this fixture. (Also establishes run wall time.)
2. **T1 - the mutation control (A5 at delay=500 ms).** Not a candidate fix; run
   first to prove the gate's PASS is reachable *today*, on this host, at this load.
   If the control does not PASS here, every later verdict is suspect and that must
   be fixed before anything else.
3. **T2 - A1, the add-path group-commit writer.** The main event: the highest
   prior-probability fix, and the one the previous round identified but never
   built.
4. **T3 - escalate on the residual.** If A1 helps but does not pass, read what it
   left behind: still bolt-bound (-> A2, A3, A7), or floor-bound (-> B1, B4, B3),
   or a second ceiling (-> lever 4 measurements).
5. **T4 - A4, adaptive delay.** Either as an alternative to A1 or combined with it.
6. **T5 - the floor.** B1 measured, then B4, then B3.
7. **T6 - off-path.** C2 then C1, only if levers 1 and 2 together cannot reach
   100 adds/s.

## Trials

### T0 - baseline at HEAD (`3365b48`) - FAIL, as expected

`add-storm 20 700 120 2000`, run 438 s wall (33 s of it the DB copy).

```
low  : clients=20  adds=1136 achieved=9.47/s  p50=124ms  p99=657ms  max=1014ms
       txnsPerAdd=0.91 maxBatchParked=5   maxBeginRWTx=4    overSlow=0
high : clients=700 adds=2239 achieved=18.66/s p50=30138ms p99=51352ms max=51431ms
       txnsPerAdd=0.58 maxBatchParked=693 maxBeginRWTx=573  overSlow=1930
throughputFactor=1.97x  queueSize=122263  backupMb=29935 (99.8 MB/s, 4 full copies)
FAIL: 1930 adds crossed the 10s slow-request threshold
```

Matches the recorded HEAD figures (18.44/s, p50 28.9 s, 1.93x, 590 queued), so the
fixture and host are behaving as the earlier measurement did, and the gate
discriminates from here.

Five things this baseline establishes that were not in the earlier record:

1. **The low phase gives the target its number.** 9.47 adds/s at p50 **124 ms**, so
   `throughputFactor >= 10` needs the high phase to reach **>= 94.7 adds/s**, and a
   single unqueued add already costs 124 ms.
2. **The queue is not empty at the start: `queueSize=122263`.** The fixture carries
   ~119,000 pre-existing *live* jobs, which the server loads into its in-memory
   queue and schedules for. That is production-faithful (production had 118k live)
   but it means the manager is doing substantial non-add work throughout, and any
   ceiling in the scheduling/`rac` path is in play (lever-4 item D3).
3. **The backup is copying 100 MB/s continuously** - four full 7.4 GB copies inside
   the 245 s of measurement. That is a large, sustained NFS write load competing
   with every commit's two fdatasyncs (lever-4 item D2).
4. **The fold factor under saturation is ~1.4, not ~10.** 693 goroutines parked in
   `DB.Batch` against 573 transactions queued in `beginRWTx`. In steady state
   arrivals equal departures (~18/s), so a 10 ms bbolt window almost always
   contains a single call - the queue is deep in *transactions*, not in batch
   members. That is the fragmentation stated precisely: **573 queued transactions
   x ~50 ms = the ~30 s median.**
5. **Nothing hit the 60 s client floor here** (`overFloor=0`, `timedOut=0`) even at
   p99 51 s, so on this fixture the gate is decided by `overSlow`, `highP50` and
   `throughputFactor`.

### T1 - mutation control, `WRDEV_AS_BATCH_DELAY_MS=500` - FAIL, and this changes the target

```
low  : clients=20  adds=997   achieved=8.31/s  p50=480ms  p99=732ms  max=758ms
       txnsPerAdd=0.20 maxBatchParked=9   maxBeginRWTx=0  overSlow=0
high : clients=700 adds=11597 achieved=96.64/s p50=6029ms p99=8230ms max=8449ms
       txnsPerAdd=0.02 maxBatchParked=598 maxBeginRWTx=15 overSlow=0
throughputFactor=11.63x  backupMb=23147 (90.8 MB/s)
FAIL: highP50=6029ms, over the 5000ms bound
```

**The control does not PASS on this host today.** The earlier record has it passing
with p50 1.67 s and 179 adds/s; today the same knob gives 96.64/s and p50 6.0 s.
Everything else it was claimed to prove, it does prove - `overSlow` 1930 -> **0**,
`maxBeginRWTx` 573 -> **15**, `txnsPerAdd` 0.58 -> **0.02**, throughput 18.66 ->
96.64/s, factor 1.97 -> 11.63x. It removes the fragmentation completely and still
misses the gate by 20%.

That is the most useful result so far, because it converts the gate from a list of
five thresholds into **one number**.

#### The gate is "sustain ~100 adds/s", and comfortably passing it means ~200/s

Each client loops think(2 s) + latency L, so with 700 clients the achieved rate R
and the latency are locked together:

    R = 700 / (2 + L)      =>      L = 700/R - 2

- the control's R = 96.64/s gives L = 5.24 s, and it measured mean 5.08 s / p50
  6.03 s. The model holds.
- `highP50 <= 5000 ms` therefore needs **R >= 100/s**.
- `throughputFactor >= 10` needs R >= 10 x lowRate: 83/s against this run's
  degraded 8.31/s baseline, or ~95/s against HEAD's unqueued 9.47/s.

So both surviving thresholds say the same thing: **~100 adds/s is the pass mark,
and there is no margin at it.** The control sat at p99 8.2 s and max 8.4 s with
`overSlow` at 0 - one worse commit and it would have failed on `overSlow` too. To
pass reliably the high phase wants **L ~ 1 s, i.e. R ~ 200/s**, and ideally to
become offer-bound at 350/s where L collapses to a fraction of a second.

#### Why 500 ms of delay cannot get there, and group commit can

221 write transactions in 120 s is a 543 ms cycle carrying ~52 adds each. The cycle
is the **timer**, not the commit: the commit itself is ~40-90 ms. A fixed delay
therefore pays 500 ms of pure waiting per cycle and caps R at
(delay-window arrivals)/(delay + commit).

Group commit (A1) has no timer: the cycle is one commit, and the fold is whatever
arrived during the previous one. At a ~60 ms commit that is a ~9x shorter cycle for
the same fold mechanism, so it should not be near the 100/s mark at all - unless the
add path's cost is somewhere other than the commit.

#### Two things this also settles

- **A5 (just raise the default delay) is dead as a fix.** It cost the low phase its
  latency exactly as predicted - p50 124 ms -> 480 ms, rate 9.47 -> 8.31/s - and
  still failed. A4 (adaptive delay) inherits the same structural problem: any delay
  is a wait the writer did not need. Both are demoted to fallbacks behind A1.
- **The remaining risk is no longer bolt's writer lock.** With `maxBeginRWTx` at 15
  and `txnsPerAdd` at 0.02, the writer lock is not the queue any more, and the
  system still only managed 96.64/s. Whatever the next ceiling is, the control has
  already walked us up to it: lever-4 measurements (CPU per add, the 122k-item
  in-memory queue, the socket, the 90-100 MB/s backup) become first-class, not
  contingency.

### T2 - A1, the add-path group-commit writer - **PASS**

One change, `jobqueue/db.go` only, +244/-13. A `newJobsOp` and a single
long-lived `newJobsWriter` goroutine, one-for-one with the existing
`archiveWriter`'s nine members: `enqueueNewJobs`, `swapNewJobs`, `drainNewJobs`,
`applyNewJobs`, `newJobsTx`, `applyNewJobsOp`, `failPendingNewJobs`,
`stopNewJobsWriter`, plus its own `njMu`/`njPending`/`njSignal`/`njStop`/
`njWriterDone`/`njStopped` fields, started in `initDB` and stopped from
`finaliseBackup` before the archive writer. `storeNewJobData` enqueues and waits
on its own reply instead of calling `db.bolt.Batch`. Per-caller error isolation
and the per-op `recover()` are copied from `applyArchives`/`applyArchiveOp`. The
`storesNeedChunking` escape to `storeNewJobDataChunked` is untouched.

```
low  : clients=20  adds=1154  achieved=9.62/s   p50=127ms  p99=243ms  max=291ms
       txnsPerAdd=0.79 maxBatchParked=0 maxBeginRWTx=0 overSlow=0
high : clients=700 adds=26483 achieved=220.69/s p50=1137ms p99=2322ms max=2923ms
       txnsPerAdd=0.01 maxBatchParked=0 maxBeginRWTx=0 overSlow=0
throughputFactor=22.95x  backupMb=19915 (80.3 MB/s)
PASS
```

| | HEAD (T0) | delay=500 (T1) | **A1 (T2)** | bound |
| --- | --- | --- | --- | --- |
| high add rate | 18.66/s | 96.64/s | **220.69/s** | ~100/s |
| `throughputFactor` | 1.97x | 11.63x | **22.95x** | >= 10x |
| `highP50Ms` | 30,138 | 6,029 | **1,137** | <= 5,000 |
| `highP99Ms` | 51,352 | 8,230 | **2,322** | - |
| `highMaxMs` | 51,431 | 8,449 | **2,923** | < 10,000 |
| `overSlow` | 1,930 | 0 | **0** | 0 |
| `txnsPerAdd` | 0.58 | 0.02 | **0.01** | <= 1.00 |
| `maxBeginRWTx` | 573 | 15 | **0** | - |
| low p50 / rate | 127 ms / 9.47/s | 480 ms / 8.31/s | **127 ms / 9.62/s** | - |
| verdict | FAIL | FAIL | **PASS** | |

**11.8x the add throughput of HEAD, and 2.3x the mutation control**, with every
threshold cleared by a wide margin rather than a whisker.

#### What the numbers say about why it works, and where the new ceiling is

- **The writer lock is gone as a queue.** `maxBeginRWTx` 573 -> 0, `maxBatchParked`
  693 -> 0. The add path no longer touches `DB.Batch` at all.
- **173 write transactions carried 26,483 adds** - a fold of ~153 per commit, at
  ~1.44 commits/s. The commit rate did *not* rise; the work per commit did. That is
  the whole mechanism, and it confirms the ~11-16 commits/s floor was never the
  thing to fix.
- **No idle-latency cost, unlike the delay knob.** Low-phase p50 stayed at 127 ms
  (T1's fixed delay took it to 480 ms) and low-phase p99/max *improved* on HEAD
  (243/291 ms against 657/1014 ms), because a lone add no longer waits behind other
  adds' separate transactions either.
- **The system is now offer-limited, not writer-limited.** 220.69/s achieved against
  350/s offered, and the latency model holds exactly: L = 700/220.69 - 2 = 1.17 s
  against a measured p50 of 1.137 s. The residual 36% shortfall is *not* bolt: with
  0.01 txn/add and nothing queued in `beginRWTx`, the remaining ~1.1 s per add is
  request handling, the 146k-item in-memory queue, `checkIfComplete`'s read per add,
  and this host's load. **That is the next ceiling if one is ever wanted, and the
  gate does not require reaching it.**

#### Confirmed, unasked, by the trial

`go build ./...`, `go vet -tags reliability_repro ./jobqueue/`,
`TestJobqueueBasics`, the whole of `reliable4_archive_coalesce_test.go` and
`reliable4_add_tx_test.go`, and `-race` over a subset of those - all clean.

#### Risks the trial identified, which the real implementation must answer

1. **The fold is uncapped.** 700 single-job adds fold into ~1,750 puts, which is
   safe, but an add may carry up to 999 items per bucket before `storesNeedChunking`
   fires, so N large-but-unchunked adds could fold into N x ~5,000 puts in one
   transaction. bbolt holds every dirty page in memory until commit, so that is an
   RSS spike *and* a longer commit - and a longer commit makes the next fold larger.
   Needs a size/op cap in `swapNewJobs`, or an argued reason not to have one.
2. **`storeNewJobDataChunked` still uses `db.bolt.Batch`.** A chunked add both
   fragments itself and competes with the new writer.
3. **`storeLimitGroups` and `storeEnv` are still unfolded `Batch` calls** on the add
   path. Both early-out in this workload (`planLimitGroups` reports no write needed;
   the env cache hits), so neither cost anything measurable here - but an add naming
   a *new* limit group puts one unfolded transaction in front of the folded one.
4. **Adds still queue behind the other writers** - `bestEffortWriter`,
   `archiveWriter`, `deleteLiveJobs`/`modifyLiveJobs`/`storeLookups`. A1 folds adds
   against each other and nothing else. That is the A3 argument, and the gate says
   it is not needed to pass.
5. **Shutdown termination is probabilistic, not bounded**, because `select` picks
   randomly between `njStop` and `njSignal` - the same property `archiveWriter`
   already has, but a 700-client storm exercises it harder than anything before.
6. **`WR_MANAGERDBBATCHDELAY` is now inert for adds**, so it is no longer an
   operational lever on the add path (it remains one for the other `Batch` callers),
   and `TestReliable4AddOneWriteTx`'s "with bolt's write coalescing disabled"
   premise is vacuous there (it still passes, at exactly 1).

### T3 - A1 repeated, unchanged - **PASS again**

```
low  : adds=1162  achieved=9.68/s   p50=107ms  p99=219ms  max=255ms  txnsPerAdd=0.81
high : adds=25870 achieved=215.58/s p50=1173ms p99=2565ms max=2932ms txnsPerAdd=0.01
throughputFactor=22.26x  overSlow=0 overFloor=0 timedOut=0 errors=0
PASS
```

Within 2.3% of T2 on every figure (215.58 vs 220.69 adds/s, p50 1173 vs 1137 ms,
max 2932 vs 2923 ms). **The pass is reproducible, not a lucky run**, and the margin
is a factor of ~2.2 on the binding threshold rather than a few percent.

### T4 - A1 plus a bounded fold - implementation

A1's fold was uncapped, which is fine for the storm (700 single-job adds are
~1.2 MB and ~2,100 puts, one comfortable transaction) and not fine in general: an
add stays on the folded path until `storesNeedChunking` fires, which allows ~999
items per bucket, and production's commands are ~25 KB. A deep queue of those
would dirty gigabytes in one transaction, and bbolt holds every dirty page in
memory until commit - and a longer commit makes the next fold larger, so it is
self-amplifying.

Added on top of A1, same file:

- `newJobsFoldMaxBytes = 32 MiB` and `newJobsFoldMaxPuts = 50,000`. 32 MiB is
  ~2.4x production's 13.5 MB per-commit freelist rewrite, so the fixed cost stays
  amortised (a 4 MiB budget would be actively harmful on that database), and it is
  >25x what the whole 700-client storm needs. The put bound exists because bytes
  alone do not bound the work: a lookup-bucket item is a key with *no value*, so a
  flood of them costs B+tree splits and a dirtied page per touched node while
  barely moving the byte budget.
- Each op's cost is computed **once**, in `storeNewJobData`, by `newJobsFoldCost` -
  never re-walked in the swap, which holds `njMu`.
- `splitNewJobsFold` spends the budget in arrival order and **always takes the head
  op**, so an add larger than the whole budget still makes progress. The remainder
  is copied to a fresh slice rather than resliced, so the taken ops' encoded bytes
  are not pinned by the pending queue's backing array.
- `swapNewJobs` re-arms `njSignal` **only when a remainder is left**, so the writer
  starts the next transaction without waiting for an arrival and without spinning
  on an empty queue.
- **`drainNewJobs(final)` and `failPendingNewJobs` both had to become loops.** This
  is the trap a bounded swap sets: `stopNewJobsWriter` -> `drainNewJobs(true)` was
  one swap plus one apply, which with a bound would have persisted one budget's
  worth and **dropped the rest on the floor with their callers still blocked on
  `<-op.result` forever**. Both now loop until a swap returns nothing, which
  terminates because the first final swap latches `njStopped` in the same critical
  section.

Four new tests in `jobqueue/reliable4_add_foldcap_test.go` (build-tagged
`reliability_repro`, because proving a 32 MiB budget engages costs ~100 MB of bolt
writes): the budget arithmetic and at-least-one-op rule, the non-final drain's
re-arm, the final drain persisting everything across several transactions, and
20 concurrent 200-job adds through the real `storeNewJobs` with bolt's write lock
held so they genuinely pile up - 6 transactions, 0 errors, 0 of 4,000 jobs lost.

**The cap was mutation-tested.** With both constants raised to `1<<40` the drain
tests FAIL ("1 transactions ... biggest fold 20 adds = 102400000 bytes") and the
concurrent test's transaction count drops 6 -> 3. So the counts are the cap, not
luck.

Clean: `gofmt`, `go build ./...`, `go vet -tags reliability_repro`,
`TestJobqueueBasics`, the reliable4 add/archive transaction tests, `-race` on both.
`golangci-lint run ./jobqueue/` reports exactly one thing: a `dupl` pair,
`applyArchives` <-> `applyNewJobs`, which is the deliberate mirror.

Caveats the trial recorded, which are the honest cost of the bound:

- an add queued behind a big fold can now wait one extra commit per budget, where
  before it rode along free (only large adds; the single-job storm never reaches
  the cap);
- the budget counts encoded key+value bytes, not bbolt's page overhead, node splits
  or overflow pages, so real peak dirty memory per transaction exceeds 32 MiB
  somewhat - under ~2x, but "32 MiB" is not an RSS guarantee;
- the true worst case per transaction is `max(32 MiB, one unchunked add)`, because
  of the at-least-one-op rule; the real guard on that is `storesNeedChunking`;
- the remainder copy is O(remaining) under `njMu`, which is tens of KB of pointers
  at today's queue depths but would itself become contention at a 100k-deep queue
  (a head index or ring buffer would remove it);
- shutdown may do an extra bounded transaction or two before the final drain,
  because `select` picks randomly between `njStop` and a re-armed `njSignal`. Every
  iteration makes progress and nothing is lost.

### T4 - A1 plus the bounded fold - **PASS**, and the best run of the four

```
low  : adds=1159  achieved=9.66/s   p50=113ms  p99=253ms  max=443ms  txnsPerAdd=0.81
high : adds=28312 achieved=235.93/s p50=929ms  p99=1834ms max=2574ms txnsPerAdd=0.01
throughputFactor=24.43x  overSlow=0 overFloor=0 timedOut=0 errors=0
PASS
```

The bound costs nothing on this workload, as designed - the storm's folds are
~1.2 MB against a 32 MiB budget, so the cap never engages, and the run came out
marginally *better* than the uncapped one (235.93 vs 220.69/s) which is host noise,
not an effect.

## SUMMARY

### The gate passes

`wrdev.sh add-storm` PASSES with one change to one file. Three runs:

| run | high rate | factor | p50 | p99 | max | overSlow | txns/add | verdict |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| T0 HEAD | 18.66/s | 1.97x | 30,138 ms | 51,352 ms | 51,431 ms | 1,930 | 0.58 | FAIL |
| T1 delay=500 | 96.64/s | 11.63x | 6,029 ms | 8,230 ms | 8,449 ms | 0 | 0.02 | FAIL |
| T2 A1 | 220.69/s | 22.95x | 1,137 ms | 2,322 ms | 2,923 ms | 0 | 0.01 | **PASS** |
| T3 A1 repeat | 215.58/s | 22.26x | 1,173 ms | 2,565 ms | 2,932 ms | 0 | 0.01 | **PASS** |
| T4 A1 + bound | 235.93/s | 24.43x | 929 ms | 1,834 ms | 2,574 ms | 0 | 0.01 | **PASS** |
| bound | - | >= 10x | <= 5,000 ms | - | - | 0 | <= 1.00 | |

**12.6x the add throughput of HEAD**, every threshold cleared by roughly a factor of
two, and the low-concurrency phase *improved* rather than paying for it (p50 124 ->
113 ms, p99 657 -> 253 ms).

### What fixed it

`db.storeNewJobData` no longer calls `db.bolt.Batch`. It enqueues its prepared
bucket writes on a single long-lived `newJobsWriter` goroutine, which folds every
currently-pending add into ONE `db.bolt.Update` and replies to each caller with its
own error - the same shape as the `archiveWriter` already in `jobqueue/db.go`, with
the same per-caller error isolation and per-op `recover()`, plus a 32 MiB /
50,000-put bound on how much one transaction may fold.

The mechanism, in one sentence: **bbolt's `Batch` coalesces within a 10 ms window
and never across a commit, which is exactly backwards when a commit costs 50-120 ms;
group commit coalesces across the commit, so the fold factor rises with load
instead of collapsing under it.** 28,312 adds rode on 205 write transactions.

### The three things worth knowing that were not known before

1. **The gate is one number, not five thresholds.** With 700 clients on a 2 s think
   time, `R = 700/(2 + L)`, so `highP50 <= 5 s` and `throughputFactor >= 10` both
   reduce to **sustain ~100 adds/s**, and the model predicted every run's latency
   from its rate to within 4%. Anyone tuning against this gate should tune the rate.
2. **The mutation control does not pass today** (96.64/s, p50 6.0 s) where the record
   has it passing at 179/s and 1.67 s. It removes the fragmentation completely and
   still misses by 3%. So a batch-delay knob was never a fix, and **the earlier
   record overstated what it proved** - it was 3% from failing.
3. **The per-commit floor was never the thing to fix.** The commit rate did not rise
   (1.4-1.7 commits/s in both the control and the fix); only the work per commit
   did. Every idea aimed at making commits cheaper - `NoFreelistSync`, a larger page
   size, online compaction, splitting the live set from the archive - was
   unnecessary, and the ~11-16 commits/s ceiling is not a problem while a commit can
   carry 150 adds.

### Ideas never needed, and why they are recorded rather than tried

The seeded space had 19 ideas across four levers. The gate passed on the second
trial, so the following were left untried **on purpose**, not from exhaustion, and
each is cheaper to revisit than to re-derive:

- **A2/A3/A6/A7** (generic `Batch` replacement, one writer for the whole database,
  a bbolt fork, a held-open transaction): more coverage of the same mechanism. A1
  folds adds against each other only; adds still queue behind `bestEffortWriter`,
  `archiveWriter` and the lookup/live-job writers. Not needed to pass, and A3 is the
  right next step if cross-path starvation ever shows up again.
- **A4/A5** (adaptive or raised `MaxBatchDelay`): **actively refuted** by T1. Any
  delay is a wait the writer did not need; it cost the low phase 4x its latency and
  still failed. Note that `WR_MANAGERDBBATCHDELAY` is now **inert for adds**, so it
  is no longer an operational lever on that path.
- **B1-B5** (the per-commit floor: `NoFreelistSync`, out-of-band freelist
  persistence, online compaction, larger page size, fewer free pages at source):
  unnecessary, per finding 3 above. `NoFreelistSync`'s open-time page-scan cost on a
  7.4 GB database remains unmeasured, and now need not be.
- **C1-C3** (split the live set from the archive; a write-ahead log for adds): the
  two structurally largest ideas in the space, both aimed at a floor that turned out
  not to bind. C1 would still make the backup incremental, which is a separate
  benefit on a separate problem.
- **D1-D5** (other ceilings): partly answered by the runs rather than by
  investigation. The fix left 235.93/s against 350/s offered with **nothing** queued
  in `beginRWTx` and 0.01 txn/add, so the residual ~930 ms per add is request
  handling, the 148k-item in-memory queue, `checkIfComplete`'s read per add, and a
  host at load 125 - **not** bolt. That is the next ceiling if one is ever wanted;
  the gate does not require reaching it.

### Outcome

Both landed, with real-LSF validation:

- `dc54666` **Coalesce concurrent adds into one bolt write transaction** - the
  writer, its bound, the shared `applyFolded`, five behaviour tests in the normal
  build and four bound tests repro-tagged. `make test` 503 passed, `make race` 503
  passed across 44 lanes, `make lint` 0 issues, scale gate 213.86 adds/s / 22.22x /
  p50 1,224 ms / `overSlow` 0.
- `6fda86f` **Stop the archive-fold reporter racing its interval var** - a
  pre-existing `make race` failure this work had to clear first, since `make race`
  was red at `8d6cd0a` before any of it.
- `cde2433` **Validate the add path against real LSF** - `add-storm-lsf` plus
  `add-storm-fixture`, Tier-B per `DEVELOPERS.md` §3.

#### The Tier-B fixture problem, which is the part worth remembering

`add-storm` is in-process: real server, real socket, real clients, but a **mock
scheduler**, so no `bsub`, no runner, and no command ever runs. It therefore cannot
touch the property this fix puts most at risk - that an add the client was TOLD
succeeded is durable, now that the commit happens on a shared writer goroutine
rather than the caller's.

Neither existing fixture could support that test:

- **`pristine6` has zero incomplete jobs.** A run against it exercises nothing that
  recovered live jobs do - re-enqueue, scheduler grouping, `rac` scanning,
  dependency release - and none of their competition for the single writer. A
  complete-only fixture tests the wrong thing, which is easy to miss because the
  run passes.
- **`prod.db` has 118,213 incomplete PRODUCTION jobs**, whose commands a real-LSF
  manager recovers and `bsub`s, running them as you. Measured, and refused: an
  attempt recovered all 118,213 and the guard killed the manager with nothing
  submitted.

So the fixture is generated instead: 20,000 jobs whose every command starts `echo
aslfix`, 5% of them adding further jobs themselves - production's actual shape, and
the mechanism that produced the add storm - with dependencies and dep groups
(10,000 ready + 10,000 dependent), all held in a limit group set to **0** so they
cannot run while being added, on top of a complete-only base so the file keeps its
production-sized freelist. `add-storm-lsf` audits every recovered command against
the manifest's safe prefix and only then raises the limit.

Because incomplete jobs are now *expected*, counting them is no longer a safety
test. Safety is layered instead: a sidecar manifest required **before any manager
starts** (so an unstamped database cannot reach a scheduler at all), the limit-0
block, the per-command prefix audit, and the post-recovery count as a backstop.
Every layer was made to fire.

Two runs, both PASS: 2113/2113 and 1327/1327 acknowledged adds present after
`kill -9` and a DB-preserving restart, 0 missing, p50 710/559 ms with nothing over
the 10 s threshold, while the recovered population completed 11,307 and 8,203 of
20,000 on real runners (peak 57/45 in RUN) and added 602 and 326 jobs of its own.
Run 1 also showed dependency release, `dependent` falling 10,000 -> 8,600.

### Suggested next steps
1. **Re-measure against a production-shaped database** if a number closer to what
   production will see is wanted: this fixture has 4.8x production's free-page count.
   `/nfs/hgi/wr/lsf/.wr_production/db_predep-granularity` is a static current-shape
   copy. Not required - the harsher fixture passing is the stronger result - but it
   would size the headroom production actually gets.
2. **Three bugs this work uncovered** are recorded and unfixed in
   `.docs/bugfixes/260828-1.md`: the suite leaks `/tmp/wrtest*` per call while
   `make clean` only removes `/tmp/wr` (25,023 dirs / ~105 GB had filled this
   host's `/tmp` and failed a `make race` run at HEAD); `TestOwnMemoryMB` flakes
   under `-race` on a 1 MB Pss tolerance; and `managerdbbatchdelay` /
   `managerdbbatchsize` now govern none of the manager's three write paths but
   `cmd/conf.go`, `jobqueue/server.go:222` and two `archivefold.go` comments still
   say they do.
3. **The three recorded bugs in `.docs/bugfixes/260827-2.md` (items 8, 10, 12)** are
   untouched by any of this; one has a restart-surviving symptom (a limit group can
   never be un-stored).
4. **The next ceiling, if one is ever wanted**, is not bolt: the fix left 235.93/s
   against 350/s offered with nothing queued in `beginRWTx`, so the residual ~930 ms
   per add is request handling, the 148k-item in-memory queue, `checkIfComplete`'s
   per-add read, and this host's load.

### Spec or bugfix?

**`/bugfix`, not `/spec-writer`.** One mechanism, in one file, with an exact
in-file precedent (`archiveWriter`) that already has its own test suite to mirror;
no user-facing behaviour change, no new configuration, no schema or format change,
nothing outside `jobqueue/db.go`. The acceptance criterion already exists as a
runnable gate, and the trial has both a reference implementation and four bound
tests. What the real pass must add on top of the trial:

- **fast behaviour tests in the normal build** (not `reliability_repro`), mirroring
  `reliable4_archive_coalesce_test.go`: adds coalesce, one bad add does not fail its
  batch-mates, a panic stays that add's error, and `close()` drains everything
  pending;
- **a decision on the `dupl` lint pair** `applyArchives` <-> `applyNewJobs` - either
  factor the shared per-caller error-isolation loop into one helper both use, or
  document why the mirror is deliberate;
- `make test`, `make race`, `make lint` clean, and the scale gate re-run on the
  committed code.
