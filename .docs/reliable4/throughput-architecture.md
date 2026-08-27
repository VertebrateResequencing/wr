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
