# Raising the archive throughput ceiling: group commit, and splitting live from archive

**STATUS: exploration - but read the correction below before acting on idea 1.**
This is the home for the two design directions the operator wants pursued. Read
`prod-validation-260827.md` first for the measurements that motivate them.

> **CORRECTION, 2026-08-27: idea 1 is already built for the archive path, and
> already deployed.** `f7e36bc` (an ancestor of production's `fb5df01`) replaced
> the per-archive `db.bolt.Batch` with a single coalescing `archiveWriter` that
> folds every pending archive into ONE `db.Update` and replies to each waiter with
> its own error. So this document's "each completed job currently costs one bolt
> write transaction" is not true of the deployed code, and the 07:58-08:08 symptom
> happened *with* group commit running.
>
> `developers/wrdev.sh archive-ceiling` then measured what the shape is worth: on
> production's own filesystem, with the continuous backup streaming, the archive
> path does **364/s** and nothing within 12x of the client floor; mutated to one
> transaction per archive it does **21/s** with a mean archive of **70 s** and
> 1,827 reports past the floor - production's symptom exactly. So production is
> running the fast code and behaving like the slow code. **Find out why the writer
> is not batching there before designing more batching**; the leading hypothesis
> (something upstream of `db.archiveJob` serialising completions, most likely the
> queue mutex against `satisfyEmptiedDepGroups`) and the discriminating measurement
> are recorded in `prod-validation-260827.md`.
>
> The measurements also weaken idea 2's premise: 25 KB records - production's
> `portal_builder` size - cost only 8% more per completion than 256-byte ones here.

## What to do next, without waiting for a profile (2026-08-27)

The production profile is deferred until the operator can restart, but three of
these four items do not need it, and the second largely removes the need for it.

### 1. Read the completion path for per-completion exclusive locks

Free, and it targets everything below it. From a runner's final-state report
arriving to `archiveJob` being called, the path runs `getijForReport` ->
`s.q.Get` -> archive -> `q.Remove` -> membership release ->
`satisfyEmptiedDepGroups`. **If any step takes the queue *write* lock once per
completion, that alone caps completions at the rate that lock turns over**, and
the coalescing `archiveWriter` would never see two pending archives at once
however fast it is - which is exactly the "runs the fast code, behaves like the
slow code" observation.

Known constraint to respect while reading: `queue.mutex -> job` is an established
acquisition order (`releaseTimedOutItems` calls `ttrCb` under `queue.mutex`,
`queue/queue.go:1942`; `ttrCallback` takes `job.Lock()`, `server.go:4601`).

### 2. Make the manager report the batch size it actually achieves

The central question is *why the writer folds only one archive at a time in
production*, and nothing currently in the log answers it. An inert counter plus a
periodic line - the established convention (`db.archivedDecodes` `5c75a15`,
`Job.derivations` `8087866`, `db.archiveTxObserver` `f7e36bc`,
`db.depGroupSeenGets` this delivery) - would answer it from an **ordinary manager
log with no `--debug` and no pprof**.

If production reports a mean fold of ~1 while `archive-ceiling` reports ~100, the
diagnosis is settled on the spot. This is the highest value-per-line item here: it
makes the next restart decisive whether or not profiling is enabled.

### 3. Test the ingredient the hunt never varied: dep-group membership

The ingredient hunt varied NFS, the continuous backup, record size (25 KB, +8%)
and live-set size (80,000 jobs through the real RPC path) - and reproduced
nothing. It did **not** vary dep-group membership, which is the largest remaining
difference between the harness and production: **112,486 memberships** on a
dependency-heavy `portal_builder` workload.

That matters because the suspected serialisation point only bites when completions
actually empty groups. Give `archive-ceiling` (or a sibling) production's
dep-group shape and see whether 364/s collapses toward 14/s. If it does, the whole
thing is reproducible on this host and fixable without touching production.

### 4. Unrelated, actionable now

`wr add` of **150,000 jobs in one request takes 72.5 s**, and the client abandons
it at its own 60 s `ClientMinRequestTimeout` floor (80,000 jobs: 49.7 s). Measured
2026-08-27. Nothing to do with the throughput ceiling; a real defect on its own.

### Order

Items 1 and 2 together first - the read says where to instrument, the
instrumentation makes the next restart decisive. Then item 3, which is the one
that might reproduce the entire symptom locally today. Item 4 whenever.

## THE REPRODUCER, AND THE HONEST VERDICT ON THE FIXES (2026-08-28)

`wrdev.sh add-storm` (`d305332`) reproduces the production symptom. It was built
by an agent given **only the profile files** - no access to these documents, no
sight of the diff, no commit messages - and it independently reached the same
diagnosis: **85% of mutex delay under bbolt's writer lock is `bbolt.(*batch).run`,
i.e. the add path**, with the two purpose-built coalescing writers working fine and
the slow archives merely queueing behind it.

### It reproduces, and it discriminates

Real manager, real socket, real clients each adding **one** job, against a copy of
a big production-shaped DB on NFS with backups streaming. Farm-safe: mock
scheduler, inert runners, no LSF job, no command executed. It emits the identical
production warning line, down to `selector="jobs=1"` and `replyBytes=222`.

| | production | **@`11fb939`** | + mutation control | **@HEAD `214aa84`** |
| --- | --- | --- | --- | --- |
| single-job add p50 | 12.8 s | **40.2 s** | 1.67 s | **28.9 s** |
| adds over 10 s | 28,815 / 23 min | **1,367 / 120 s** | **0** | **1,924 / 120 s** |
| adds hitting the 60 s floor | - | **118** (117 gave up) | 0 | **0** |
| add throughput | - | 12.82/s | 179.07/s | **18.44/s** |
| scaling | 1.6x for 57x | **1.50x for 35x** | 26.56x for 35x | **1.93x for 35x** |
| write txns per add | - | 1.05 | 0.01 | **0.58** |
| queued in `beginRWTx` | 29 | **607** | 3 | **590** |
| verdict | - | **FAIL** | PASS | **FAIL** |

The mutation control is *not* a fix: it widens bbolt's coalescing window through
the existing `WR_MANAGERDBBATCHDELAY` knob, changing no wr code. Its purpose is to
prove the gate measures **transaction fragmentation** rather than merely a large
database - and it does, RED to GREEN on the same DB and host.

### The verdict on the seven commits

**They helped measurably. They did not fix the problem.** Black-box measurement at
HEAD - the number only, without inspecting what changed:

- write transactions per add roughly **halved** (1.05 -> 0.58)
- throughput **+44%** (12.82 -> 18.44/s)
- p50 **40.2 s -> 28.9 s**
- **nothing crossed the 60 s client floor**, against 118 crossings at `11fb939` of
  which 117 gave up - operationally the most meaningful single change, since that
  is the boundary where completed work starts being lost and re-run

But **HEAD still FAILS**: 1,924 single-job adds over 10 s in two minutes, p50
28.9 s, 590 write transactions queued at once, and throughput still rising only
1.93x for 35x the concurrency. The ceiling is intact; it has moved, not lifted.

**This is what the earlier sections should have said and did not.** The seven
commits were validated by transaction-count probes - a measure of the *mechanism*
hypothesised, not of the *symptom* observed - and were described as a fix on that
basis. The reproducer that should have come first now exists, and it says the
problem remains.

### The finding worth acting on

The mutation control reached **179 adds/s against 12.82** - a 14x improvement from
an existing operational knob with no code change. That is not a recommendation:
widening the batch delay trades latency on an idle manager for coalescing under
load, and nobody has measured the idle cost or argued the tradeoff. But a 14x
signal from a knob that already ships deserves a deliberate decision rather than
being left unexamined.

### Two refinements to earlier claims

- **The backup hurts archives, not adds.** From the reproducer's samples, slow
  adds accrue at the same rate whether or not the backup is streaming (45.4 vs
  40.9 per 2 s sample), while slow *archives* more than double (5.2 vs 12.2).
- **The env cache was never the issue**, independently confirmed: `db.storeEnv` is
  **9 events out of 111,988** in the production block profile. Consistent with the
  falsification recorded below.

### Caveats the reproducer itself records

Only adds completing inside the window are counted, so the slowest are dropped and
the latency figures are conservative. `txnsPerAdd` is a backstop rather than the
primary check: under deep saturation bbolt coalesces from queue length alone and
the ratio falls below 1 while the path is still broken (`11fb939` measured 1.05
with 607 transactions queued), so it must be read beside `maxBeginRWTx`. And the
harness is *harsher* than production - p50 40 s against 12.8 s - because the only
available fixture has a 4.5x larger freelist than production's post-compaction DB
and this host runs at load ~120.

**Disclosure:** the agent had seen three recent commit *subjects* before the
no-diff constraint reached it (none naming the add path, bolt batching,
`storeLimitGroups` or transactions); its analysis started from the profiles.

## FIXED, AND ONE HEADLINE FINDING FALSIFIED (2026-08-28)

All seven items are implemented and committed (`.docs/bugfixes/260827-2.md`):
`72d3d3d` `10d3908` `639fcd8` `462b253` `144c788` `4a63e87` `2b56048`.

### The `bucketEnvs` growth claim below is WRONG. It was measured and falsified.

The section that follows names "`bucketEnvs` grows by one never-deleted record per
add" as the upstream cause of the DB growth that starves the freelist, forces the
mmap remaps and inflates the backup. **That is not true**, and it was published
here on an unverified inference.

Bucket stats on `/nfs/hgi/wr/sb10-bigdb/prod.db`, taken with
`managerremotesameaslocal: true` already in force:

| bucket | keys | leaf bytes |
| --- | --- | --- |
| `bucketEnvs` | **549** | **1.31 MB** |
| `bucketJobsComplete` | 2,155,154 | 2.87 GB |

Envs are **0.04%** of leaf bytes. The mechanism was wrong too, and the code says
so: `Client.Execute` sets `cmd.Env` from `job.Env()`, **replacing** the
environment, so a `wr add` running as a wr job never sees the runner's
`LSB_JOBID`/`LSB_JOBINDEX` and the key is not unique per add.

So **the DB's growth is just archived jobs accumulating** - 2.15M complete records
- which is inherent to what the manager stores, not a leak. Mechanism C's growth
driver needs no separate explanation.

What *was* real in item 1: a 12-entry cache miss on an already-stored env cost a
redundant write transaction. That is fixed. `envCacheSize` stays at 12, nothing
user-visible changed, and no operator config change is needed.

### A second correction: the suggested counter would have measured the wrong thing

`db.bolt.Stats().TxN` counts **read** transactions only (`beginTx`, not
`beginRWTx`). Anything using it to count write transactions - as this document
previously suggested - measures the wrong quantity. The measurements below use
`db.bolt.View` + `tx.ID()`, which counts committed write transactions exactly and
needs no instrumentation.

### What was actually achieved: measured, A/B against a worktree at `062d05a`

| measurement | before | after |
| --- | --- | --- |
| single-job add, default coalescing | 2 | **1** |
| single-job add, coalescing off | 4 | **1** |
| single-job add, brand-new env, coalescing off | 5 | **2** |
| 1000-job add, coalescing off | 6 | **3** |
| `storeLimitGroups(empty)` / `(nil)` | 1 | **0** |
| re-`storeEnv` of a cache-evicted env | 1 | **0** |
| `storeBatched` at 1000 / 2000 items | 2 calls (1 empty) / 3 (1 empty) | **1 (0)** / **2 (0)** |

Counting the env write that item 1 removed, a single-job add went from **7 write
transactions to 1**, and its three *sequential* phases became one.

### The 2.4-4x completion-ceiling prediction is NOT demonstrated

Nothing above measures completions. That prediction converts add-path write-lock
time into completions per second, and no existing gate can show it:
`wrdev.sh archive-ceiling` drives `db.archiveJob` directly and **never adds a
job**, so it exercises precisely the path these items do not touch. Proving it
needs an **add-and-complete workload against a big-freelist DB on NFS**, which does
not exist as a gate and would have to be built. Treat the throughput figure as
unproven until then.

### Still open, recorded not fixed (checklist items 8-12)

- **`retrieveEnv` caches a miss**, so a later `storeEnv` of those bytes stores
  nothing and the env is never written. Independent of item 1.
- **A limit group can never be un-stored.** `NewCountGroupData` returns a
  non-count `GroupData` for a negative limit, so `storeLimitGroup`'s Delete branch
  is unreachable from every caller. `wr limit -g 'name:-1'` reports the group
  removed and `RemoveLimit` runs, but the record survives in `bucketLGs` and
  **vivifies back after a restart**. Needs a design decision.
- `bolt.Batch` can re-run its function, so the old `storeLimitGroups` could report
  a group twice; incidentally fixed by item 2's reset-inside-the-closure.
- Two load flakes, both pre-existing and both A/B'd:
  `TestDepGranularityRunnerSurvivesLongAbsence` was **4/10 at `062d05a` and 1/10
  after** (a fixed `time.After` where the test should wait on the archive having
  provably failed once); `TestJobSubscriptions` and
  `TestSubscriptionReconnectResync` are whole-suite-only, 0/10 in isolation.

### Process note

Item 1 went through a full implementor-plus-reviewer cycle. **Items 2-7 were
self-verified** - every diff read, every mutation re-run with md5-checked restores,
all gates run - but not independently reviewed, after two spend-limit interruptions
had already cost the run a lot of wall clock. Gates on the final tree: `make lint`
0 issues, `make test` **498 passed - 9 skipped - 29 packages**, `go vet` clean under
both tag sets, `-race` clean, and each of the seven commits builds and vets in
isolation.

## SUPERSEDING ANALYSIS (2026-08-27, code-first, independent)

A second independent analysis worked from the **code** rather than the profiles and
**overturns the central conclusion below**. Read this section first; the one that
follows is kept for its measurements, not its prescription.

### The three findings nobody had

**1. `bucketEnvs` grows by one never-deleted record per add - and it is the
upstream cause of the DB growth every other mechanism feeds on.**

`managerremotesameaslocal: true` is set in production
(`/nfs/hgi/wr/lsf/.wr_config.yml`), so `addEnvVars` (`cmd/add.go:713`) ships the
client's whole `os.Environ()`. On an LSF exec node that includes `LSB_JOBID`,
`LSB_JOBINDEX`, `LSB_HOSTS` and `TMPDIR`, so `byteKey` (`jobqueue/utils.go:145`)
is **unique per add**. The cache is **12 entries** (`db.go:69`), and `bucketEnvs`
has only `Put` and `Get` - **never `Delete`** (`db.go:3224`, `:3243`).

So every `wr add` pays an extra write transaction *and* permanently grows the DB
by a ~6-11 KB record. That growth is what starves the freelist, forces the mmap
remaps of "mechanism C", and inflates every backup copy. **Both prior documents
were silent on it while depending on the growth it causes.**

Kill-or-confirm: bucket stats on `bucketEnvs` - `KeyN` should be about the
cumulative add count, `LeafInuse` growing ~6-11 KB per add.

**2. A single-job add serialises THREE write transactions, and that is the latency
mechanism** - not the transaction count:

| # | source | sequential? | writes anything? |
| --- | --- | --- | --- |
| 1 | `storeEnv` -> `bolt.Batch` (`db.go:3852`) | **blocks before `createJobs`** | yes, a unique never-deleted record |
| 2 | `storeLimitGroups` -> `bolt.Batch` (`db.go:1230`), **unconditional** | **blocks before `storeNewJobs`** | usually **nothing** (`storeLimitGroup` returns unchanged without a Put) |
| 3-7 | `storeNewJobData` -> 5 x `launchBatchStore` | concurrent with each other | yes, ~7 keys |

A no-op bolt transaction is **not** free: `Tx.Commit` always runs
`commitFreelist`, always writes and fdatasyncs, always writes and fdatasyncs the
meta page. There is no early-out. So transaction 2 pays a full commit for zero work.

**This explains the distribution, which the count ratio could not.** The archive
writer's turn interval is 60/13 = 4.6 s and an archive totals ~7.5 s, so archives
*straddle* the 10 s log threshold and a minority cross. An add pays three
sequential waits: 3 x 4.6 s = **13.8 s against a measured 13.54 s tail mean and
12.97 s p50** - so nearly every add crosses. One fact accounts for the 6.5x count
ratio, both tail means, and the p50/p99 shapes.

**3. The 52-74 ms floor is the freelist page, rewritten in full on every commit -
and my reasoning for dismissing it was a category error.**

`Tx.Commit` always calls `commitFreelist`, which writes the **entire** free+pending
list, sized `16 + 8 x (free + pending)`; `Copyall` re-sorts the whole pending list
and, for the hashmap freelist wr uses, rebuilds and re-sorts the whole free list on
**every** commit. Production's ~111k free pages means an **890 KB freelist page
written and fsynced on every commit, including one that changes nothing.**

Reproduced on a shape-matched fixture, local ext4, same empty write transaction:

| free pages | freelist page | empty write tx |
| --- | --- | --- |
| ~0 | 24 B | **0.63 ms** |
| 111,273 | 890 KB | **9.30 ms** |
| 111,055 (measured in-process just after the deletes) | 890 KB | **61.3 ms** |
| 150,273 | 1.20 MB | 13.54 ms (worst 39.6 ms) |

**0.63 ms to 61 ms for identical work** - and that 61.3 ms is at exactly
production's free-page count, inside production's 52-74 ms band. The difference
between 9 ms and 61 ms is *state*: whether a contiguous run exists for the freelist
page itself, or it must come off the end of the file, moving the high-water mark
into `db.grow` -> `ftruncate` -> a **third** fsync.

**My error:** I used a 0.73 ms fsync measurement to rule durability out. That is
the *latency of an empty sync*; it says nothing about syncing 0.9 MB. That category
error is the whole of the "unexplained 68 ms".

### Why throughput FALLS between limit 500 and 2000

Four code-level reasons per-unit cost rises with concurrency:

1. **The backup's read transaction pins pending pages for its whole ~30 s**
   (`ReleasePendingPages` only releases below the oldest read txid), and the
   freelist page written per commit grows with pending count - so **the faster the
   manager runs, the more every commit costs during a copy.** Measured: pending
   218 -> 654 over 60 empty commits with one read tx held; at production's rates
   that is ~135k pending pages, roughly tripling the per-commit freelist write.
2. `ReleasePendingPages` is itself O(open readers + pending ids) inside
   `db.rwlock` + `db.metalock`.
3. Backup duty cycle rises with load - measured 9.2% at limit 500, **35% at limit
   2000** - so more competing NFS bandwidth raises the floor further.
4. **Retry amplification past 60 s:** `Client.request` does not retry, so a timed-out
   `wr add` fails the dedup command, fails the job, and wr re-runs it - the whole
   add happens again. That converts "flat" into "falls".

### Verdict on the fix proposed below: right target, wrong instrument, skips the cheap 80%

**Do not build a second coalescing writer yet.** The asymmetry is real -
`bolt.Batch`'s window is armed at batch creation and does not adapt to
back-pressure, so under saturation each 10 ms of queueing yields another
transaction, where `drainArchives` swaps the whole pending slice and folds with the
backlog. But:

- **The 2-5 concurrent `Batch` calls need no new machinery to become one
  transaction** - they are five independent bucket writes from five goroutines that
  then all block. Doing all five inside **one** `db.bolt.Batch` is strictly better.
  Note concurrency across bolt write transactions can never help: they serialise on
  one mutex, so those goroutines only *add* transactions. At `jobs=1` the
  concurrency is pure loss.
- **It misses the two sequential transactions**, which are what set the latency.
- **`bolt.Batch` already provides** per-caller error attribution with member-by-member
  solo retry and panic isolation. `applyArchives`/`applyArchiveOp` are a hand-rolled
  copy of it. Replacing `Batch` means re-owning that for no gain.

**Minimum change that moves the number** - three edits in `jobqueue/db.go`, no new
machinery, no ordering change:

1. `storeEnv`: raise `envCacheSize` from 12 to ~2x the runner count, or check
   `bucketEnvs` with a cheap `View` first. **Better, fix the cause: stop shipping
   per-runner `os.Environ()`** (finding 1).
2. `storeLimitGroups`: do the comparison in a `View`, take a write transaction only
   when something differs. Microseconds against a ~57 ms commit.
3. `storeNewJobData`: one `bolt.Batch` doing all five puts, no goroutines.

Effect at the measured mix: an add goes from **3 sequential phases / 7 transactions
to 1 phase / 1 transaction**; per-job add cost from ~35 ms toward ~5-12 ms of
write-lock time; an (add + completion) pair from ~40 ms to ~10-17 ms - **2.4-4x the
completion ceiling, roughly 60-110/s** - and add latency roughly thirds.

### The hazard to design for before ANY add-fold change

`prepareNewJobs` computes `jobsToQueue`/`jobsToUpdate` from a **read** transaction
taken *before* the write (`db.go:2984`). Archives are independent of one another;
adds interact through dep groups and `bucketRDTK`. **Widening the fold widens the
read-then-write window**, so two adds that are each other's dependency-resolution
input could both observe "no live member" and both be queued ready. The current
5-way split already has this window; a 100-way fold makes it 100x wider. This needs
a written argument, and it is the single thing to flag hardest.

### A plain bug found in passing

`storeBatched` (`db.go:3907`): `if offset := num - (num % batchSize); offset != 0`
fires with `offset == num` whenever `num % batchSize == 0`, calling
`storer(bucket, data[num:])` on an **empty slice** - one extra fully-committed
empty write transaction per store, five per bulk add, whenever the count divides
evenly. Should be `if rem := num % batchSize; rem != 0 { storer(data[num-rem:]) }`.

Relatedly, `storeBatched` splits large adds into 1000-item chunks, so a 150,000-job
add becomes **50+ write transactions** - which is the recorded `wr add 150000` at
72.5 s. bbolt handles multi-MB transactions fine; this repo's own
`compactTxMaxSize` is 64 MB.

### Ranked residuals, each with a killable observable

Beyond the above: `retrieveDependentJobs` on the add path is an O(dependents)
prefix scan with a full decode per hit, transitively, inside a read transaction -
rule-6 shape on the *add* path, scaling with 258,218 memberships (latent, not
firing yet). `readyItemData()` builds a `[]any` of every ready job under
`queue.mutex.RLock` - ~4.7 MB per pass at 296,949 jobs, coalesced to once per
500 ms. ~2 MB of heap per empty commit from `freePageIds` + `Copyall`, ~36 MB/s of
freelist garbage at 18 commits/s. `s.rpl` is a server-wide exclusive lock per
completion with an O(1) body but an O(n) `Values()`.

Looked for and **not** found: no unbuffered single-consumer channel on a hot path
(`arSignal`, `beSignal`, `op.result` are all buffered(1)); no hot-path `sync.Once`
beyond bbolt's own; and **no 6-request concurrency cap** - `numRPCReaders = 6`
bounds admission only, handlers run in fresh goroutines.

### Where this contradicts the measurements below

- **"Not a compaction story… the volume is small"** is the wrong test. 890 KB is
  small in bytes and enormous in cost. The row below reading "the same code, 7.4 GB
  DB, local disk | 172/s (5.8 ms/txn)" is essentially **all freelist and no
  payload** - read there as evidence the code path is fine. Compaction is not the
  fix, but the freelist is the largest single lever on the floor, and the floor
  multiplies everything.
- **"0.73 ms fsync, therefore not durability"** - category error, see finding 3.
- **"How much the backup costs cannot be determined from these profiles"** - it can
  be determined from the code plus two `bolt.Stats()` fields, since the copy pins
  pending pages and every commit during it pays for them. Closed form, not a
  profiling problem.

## RESOLVED: what the fixes are (2026-08-27)

Experiment has replaced the guesswork. Three mechanisms are confirmed and
separated - see "Three mechanisms, separated by experiment" in
`prod-validation-260827.md` for the evidence, including a mid-freeze goroutine
dump and a 16-minute live production sample. The fixes follow from them.

### Fix A - stop the add path monopolising bolt's write lock

**Evidence:** interleaving one `storeNewJobs` per archive takes throughput from
**357/s to 150/s** and p50 latency from 897 ms to 2,497 ms, hitting `add` and
`jarchive` equally, independent of freelist slack. The fold line reads
`meanLock=1.421s maxLock=3.9s`: the archive writer spends most of each transaction
*waiting for the write lock*, held by the add path's `bolt.Batch`.

**Why it matters most:** it is the largest measured effect, it reproduces on
demand, it is filesystem-independent, and it matches the real workload (dedup jobs
complete fast and each adds compress jobs).

**Shape:** the add path costs 3-6 write transactions per request
(`storeLimitGroups`, plus 2-5 concurrent `bolt.Batch` goroutines from
`storeNewJobData`), and the archive writer's plain `Update` cannot join a Batch.
Either route adds through the *same* coalescing writer the archives use, so both
share one transaction stream rather than fighting for the lock, or cut the add
path's transaction count. The first is more attractive because the machinery
already exists and is proven (`archiveWriter`, `f7e36bc`).

**Care needed:** `add` must remain durable before its reply, exactly as archive is.
Sharing a writer must not turn one caller's bad record into everyone's failure -
the existing per-waiter error reply is the model.

### Fix B - stop the backup copy's I/O disrupting the manager

**Evidence:** production today shows ~10 slow `jarchive`s of 17-27 s per 16
minutes, about one per backup copy cycle, with 12 full 8.99 GB copies at a 35%
duty cycle and the DB not growing at all - so this is copy I/O, not remap.

**This is the long-recorded "Part C copy-I/O relief", and it is already
prototyped:** incrementally fsyncing the copy every ~32 MB took a 10 GB DB's
freeze from **15.8 s to 0.7 s**, size-independent. It is the smallest, most
bounded, most immediately valuable piece of work here.

### Fix C - stop a growing file freezing behind the copy's read transaction

**Evidence:** confirmed by A/B on freelist slack alone (same DB compacted vs
as-is) and by goroutine dump - a writer blocked in `bbolt.(*DB).mmap` ->
`db.mmaplock.Lock()` beneath `archiveTx`, while the backup's `Tx.WriteTo` holds
`mmaplock.RLock()` for the whole copy. **The freeze lasts the copy's remaining
time**, so it scales with DB size over copy bandwidth: ~6 s in the harness,
~40 s at production's current copy speed, and worse under load.

**Currently dormant in production, and predicted to return:** free pages fell from
271,809 to 111,133 in 80 minutes with the file size static. 0.46 GB of slack
remains before writes must grow the file again.

**Shape options, needing evaluation rather than a choice made here:** grow the
mmap ahead of need *outside* copy windows; size `InitialMmapSize` so remaps are
rare; or take the backup from something other than a long-lived bolt read
transaction. Note the copy cannot simply be chunked into several short read
transactions - a bolt backup needs one consistent snapshot.

**A coupling that must be designed for:** an open read transaction pins every page
freed during it, so the copy starves the freelist and thereby *causes* the growth
that triggers the remap. Fix B shortens the copy and therefore shrinks fix C's
exposure; **fix A raises throughput and therefore grows the file faster, which
increases it.** A without C could make C fire more often.

### Recommended process

- **Fix B via `/bugfix` now.** Prototyped, bounded, addresses production's current
  pain, and independently valuable whatever else happens.
- **Fixes A and C via `/spec-writer`, together.** They are coupled through the
  freelist (above), both touch the DB write path where durability and backup
  consistency are at stake, and A's benefit changes C's exposure. The
  dep-granularity delivery is the precedent for how much that coupling costs if it
  is discovered mid-implementation rather than designed.

## The problem, stated precisely

Production completes **~14 jobs/s** however many runners are pointed at it. At
1,143 concurrent runners the archive RPC latency walked up to the 60 s
`ClientMinRequestTimeout` floor and **470 completion reports were lost in one
minute**. Throughput rose only ~1.6x for 57x the concurrency, so the constraint is
serialisation, not capacity.

**This section's original diagnosis was wrong and is kept only because the
measurements in it stand.** It assumed each completed job costs one bolt write
transaction. It does not: `archiveJob` hands the work to the coalescing
`archiveWriter` (`db.go:1579`), which folds every pending archive into one
transaction. What is true is that production behaves *as if* each completion cost
its own transaction - 14/s, latency at the floor - which is what the correction
block above is about.

The per-transaction work itself is six-plus buckets scattered across an 8.59 GB
tree: deletes from `bucketStdO`, `bucketStdE` and `bucketJobsLive`, a put into
`bucketJobsComplete`, two per-ReqGroup stat buckets and the rep-group end time.

Production's ~70 ms per completed job is **not** what one would first guess:

| component | measured | verdict |
| --- | --- | --- |
| durability (2 fsyncs per commit) | 0.73 ms each on `/nfs/hgi` | ~1.5 ms of 70 ms - **not the cause** |
| freelist rewrite per commit | 228,908 free pages = 10.9%, ~1.8 MB | real, not 70 ms |
| the same code, 7.4 GB DB, local disk | **172/s** (5.8 ms/txn) | so the code path is not the cause either |
| the same code, small DB, NFS, no backup | **~67/s** sustained | so NFS fsync is not a 14/s ceiling |
| **production: 8.59 GB DB, NFS, continuous 7.3 GB backup** | **14/s** | ~68 ms/txn unexplained |

**Nothing yet explains those 68 ms**, and it cannot be decomposed from outside the
process. The leading suspects are NFS write-*bandwidth* contention with the
continuous backup (81 MB/s sustained) and scattered dirty-page writes across a
large tree. A production profile is deferred until the operator can restart.

### Why that uncertainty mattered - and why it now blocks idea 1 instead

The original argument was that group commit divides every per-transaction cost by
the batch size, whatever those costs are, so it needed no diagnosis first. That
argument was sound *and* moot: the batching already exists, and
`wrdev.sh archive-ceiling` shows it delivering 364/s on production's own
filesystem with the backup streaming.

So the uncertainty now blocks rather than excuses: **something is preventing that
batching from happening in production**, and until that is identified, more
batching has nothing to divide. Everything below this line is retained as design
notes for the day a batching change is genuinely needed - it is not a plan of
work.

### Explicitly ruled out by the operator, 2026-08-27

- **Moving the DB to local disk.** Not to be relied on. May still be *measured* as
  a diagnostic (it is the cleanest way to price the filesystem's contribution),
  but no design may depend on it.
- **Compaction cadence, or moving the backup to another filesystem.** Same: useful
  as diagnostics, not as the fix.

So the fix must make the *work per completed job* smaller, on NFS, with backups
running.

---

## Idea 1: group commit

### The shape - ALREADY IMPLEMENTED, see the correction at the top

Concurrent archive requests are folded into one bolt write transaction. Each
caller blocks until the transaction **that contains its own write** commits, then
returns. Throughput becomes *batch size* per 70 ms instead of one per 70 ms: 20
per commit is 280/s, 100 per commit is over 1,400/s.

This is the standard database answer to exactly this shape of problem, and it is
much less radical than it sounds because **the durability contract does not
change**. The client still waits for a real fsync of its own data before being
told the job is archived. It simply shares that fsync with whoever else arrived in
the same window.

### Why it fits this codebase

- There is precedent: `ed763b0` introduced a single coalescing writer for the
  per-job-change storm, replacing an unbounded `go db.bolt.Batch` spawn. **That
  path is best-effort; this one is not**, which is the whole design difference -
  see the risks below.
- `bolt.Batch` already exists and does something adjacent: it coalesces concurrent
  callers into shared transactions and returns each caller's own error. It is
  worth evaluating before writing anything bespoke, including how it behaves when
  one caller's function fails (it retries the batch member-by-member) and whether
  its `MaxBatchSize`/`MaxBatchDelay` defaults suit a 60 s client floor.
- The archive path is already the place the reliable4 work has hardened most, so
  its invariants are written down: ownership-gated final-state reports and
  idempotent retry (`f4b9b55`), and the archive-then-`q.Remove`-then-release
  ordering that phase 4's review pinned.

### What to work out

1. **Batch trigger.** Size, delay, or both. A delay-based trigger adds latency to
   the *first* arrival in a quiet period, which matters because a lone archive on
   an idle manager should stay fast. Size-based alone stalls if the batch never
   fills. The usual answer is "commit when either N accumulate or T elapses",
   with T small (single-digit ms) so the idle case is barely affected.
2. **Error attribution.** If one job's encode or put fails, the others in the
   batch must still succeed. `bolt.Batch`'s retry-individually behaviour handles
   this but converts one bad record into a batch-sized slowdown.
3. **Ordering against `q.Remove`.** Today each archive commits, then leaves the
   queue, then releases its dep-group membership and satisfies emptied groups
   (`93d106b`, and the comment corrected in `fb5df01`). With a batch, several jobs
   commit together - so the post-commit steps become a loop, and the phase-4
   review's finding applies to each member: **a `q.Remove` error must not skip the
   membership release**, or a phantom member wedges that group's waiters until a
   restart.
4. **TTR and liveness while batching.** A job waiting up to T ms for its batch is
   still "running" from the queue's point of view. T must be far below any TTR
   margin, which it will be, but it should be stated rather than assumed.
5. **Interaction with the backup.** `f51af04` removed the per-archive
   `backgroundBackup` DB lock in favour of a dirty flag plus ticker. Fewer, larger
   transactions mean fewer dirty-flag settings - which may *reduce* backup
   frequency as a side effect. Worth measuring, not assuming.
6. **What the client sees on partial failure.** The reply contract must not change:
   `ErrMustReserve`, `jobAlreadyComplete` idempotency and the ownership gate all
   still have to hold per member.

### Expected gain, and how to prove it

The arithmetic says 10-100x depending on batch size, and it should hold under any
of the candidate causes. Prove it the way this repo demands - the gate must FAIL
before the fix:

- `archive-rate` (in-process, big DB, real `initDB`) is the natural harness; it
  already reports throughput, mean, p50, p99, max and queue depth.
- The production acceptance test is already written down in
  `prod-validation-260827.md`: **sustain limit 2000 (~1,000+ concurrent runners)
  with archive latency clear of 60 s and zero "failed to update server with cmd's
  final state" in the runner logs.**

### Risks

The archive path is the one place where losing or duplicating work is
unacceptable, and it has been rebuilt twice this year. Group commit widens the
window in which a crash loses *acknowledged-as-pending* work - but only up to the
batch delay, and only for callers who have not yet been told "archived", which is
the same exposure a slow transaction already has. The greater risk is subtler: a
bug in batch error attribution could tell a runner its job was archived when the
transaction rolled back. That is precisely the "successful work reported lost, or
lost work reported successful" family this project has spent months eliminating,
so the error paths deserve more test attention than the happy path.

---

## Idea 2: split the live set from the archive

### The shape

The DB is 8.59 GB, of which **~7.65 GB is live data and the great majority of that
is completed jobs** (the shape recorded earlier: ~2.15M complete against ~118k
live). Every completion pays to insert into that tree, and every backup re-copies
all of it.

Split the store in two:

- a **live/hot store** holding only incomplete jobs plus the small indexes the
  scheduling paths need - on the order of 118k records, a few hundred MB. All
  mutation happens here, so commits touch a small tree with a small freelist.
- an **archive store** holding completed jobs, written once and never updated -
  append-only, or a sequence of immutable segments with an index.

### Why this is the strategic option

It attacks the cause rather than amortising it, and it fixes **two** problems at
once:

1. **Throughput**: a completion becomes a small write to a small tree plus an
   append, instead of an insert into an 8.59 GB tree.
2. **Backup (issue 3)**: an immutable archive store needs backing up **once per
   segment**, not re-copied every 90 seconds. The continuous 7.3 GB copy - the
   thing currently competing for the NFS bandwidth that idea 1 is trying to use
   better - largely disappears. Only the small live store needs frequent backup.

It also shrinks the startup decode window (issue 1) as a side effect, since
recovery reads the live store only. And it is the honest version of the operator's
own append-only-journal instinct: the same "sequential append, no tree churn"
benefit, but **without** a second source of truth for live state, without a
replay-and-fold protocol, and without a crash-during-fold failure mode - because
nothing is ever folded back. A record is either live or archived, never both.

### What to work out

1. **Who reads the archive, and how.** `wr status` history, rep-group aggregates,
   `bucketRTK`/`bucketRGs`-shaped history paths, the REST contract in
   `.docs/issue-197/spec.md`, and the "archived history too big" bounds
   (`ErrArchivedHistoryTooBig`, `maxArchivedBytes`). Every one of these has to keep
   working, and rule 6 forbids a history scan on a startup or control path - so the
   archive store needs an index, not a scan.
2. **The move itself must be atomic across two stores.** Archiving means "remove
   from live, add to archive". Two stores means no single transaction covers both.
   Options: write to the archive first and treat a live-store delete failure as a
   retryable no-op (the record is idempotently archived - which the existing
   `jobAlreadyComplete` path already models), or keep a small intent record. This
   is the hard part of the design and deserves its own written argument.
3. **Migration from a 8.59 GB single DB**, without a startup history scan (rule 6).
   Probably: open the existing DB as the archive store and create a fresh live
   store, since the live set is small and can be re-derived from what recovery
   already reads.
4. **Segment lifecycle** - size, rotation, and how "back up each segment once"
   interacts with the existing `db_bk`/`db_bk.tmp` mechanism and the compaction
   tooling.
5. **What `wr manager compact` means** when there are two stores.

### Risks

This is a storage-format change to a system whose data loss modes have been
painstakingly mapped. It cannot be delivered as one commit, and it needs the same
phase-by-phase treatment as the dep-granularity work, including a green window
that is documented rather than discovered. The two-store atomicity question (point
2) is the one that decides whether it is tractable; if that argument cannot be
written down cleanly, the idea should be dropped in favour of idea 1 alone.

---

## How the two relate

They are complementary, not alternatives, and they compose in either order:

- **Idea 1 alone** raises throughput by amortising a large per-transaction cost.
  It leaves that cost large, and leaves the continuous full-DB backup in place.
- **Idea 2 alone** makes the per-transaction cost small and the backup cheap, but
  still does one transaction per completion - so the ceiling becomes
  *batch-size-of-one* against a much smaller cost. That may well be enough.
- **Both** gives a small cost, amortised.

**Suggested sequence:** idea 1 first, because it is contained, reversible, robust
to the unresolved diagnosis, and testable against an existing gate. Then take the
production profile. If it shows the backup copy dominating, idea 2's value is
mostly the backup half; if it shows tree-size costs dominating, idea 2's value is
mostly the throughput half. Either way the profile sharpens idea 2's design rather
than gating idea 1's start.

## Open questions for the operator

None blocking. Two worth answering before idea 2 is designed in detail:

1. How long must completed-job history remain queryable through the manager? If
   there is a retention horizon, the archive store's segment lifecycle gets much
   simpler.
2. Is the ~2.15M completed-job history queried in practice, or mostly retained
   because nothing has deleted it? That changes whether the archive store needs a
   real index or merely needs to exist.
