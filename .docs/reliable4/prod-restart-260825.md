# Live prod investigation — 2026-08-25 restart after the 10:05 death

**STATUS (12:15). ROOT CAUSE FOUND — see Finding 6.** Prior-state recovery
retains a **per-job copy of every dependency group's membership**, which is
O(live jobs x dep-group size) live heap: 97.55% of a 15.65 GB heap profile, in a
manager whose 150,472 decoded jobs account for only 376 MB. This is what has
OOM-killed the manager **four times** at 175-181 GB anon-RSS (kernel-confirmed).
The manager running now is on the same curve and is predicted to be killed at
roughly **12:45-12:55**. Findings 3, 4 and 5 below are superseded in part by
Finding 6; they are kept because their measurements stand.

Manager under investigation, started by the operator at **11:59:46** as `mercury`
on **farm22-ibackup01** (= 172.27.71.193, the same node the 10:05 death happened
on; 16 cores, **182.7 GB RAM**, LSF `lshosts`):

```
GODEBUG=gctrace=1 WR_PPROF_ADDR=0.0.0.0:6060 \
  /software/hgi/installs/wr/v0.37.2/wr manager start -f \
  --runner_filelog /nfs/hgi/wr/lsf/runner_logs 2>&1 \
  | tee /nfs/hgi/wr/sb10-pprof/wr-fg-1787655586.log
```

That binary reports **`v0.37.1-67-gbf53de0`**, i.e. it contains the recovery-
logging fix committed today. Live jobs recovered this run: **150,472**.

## Today's sequence, for context

| time | event |
|---|---|
| 08:57 | operator kills the cron-restarted **master** build (now `log_old`) |
| ~09:20 | `db_bk_precompact` kept; DB compacted **15.74 -> 7.05 GB** |
| 09:22:51 | manager `d11b83b` starts on the compacted DB |
| 09:23:28 | serving (so the synchronous live-bucket scan took **37 s**) |
| 09:44:32 | last `ErrRecovering`: recovery took **21 min**, silently |
| 09:44:46 | first RPC blocks — a **16m39s** server-wide stall begins |
| 10:01:25 | 113 slow requests all complete in the same second |
| ~10:05:19 | silent death mid-backup; `db_bk.tmp` abandoned at 1.1 GB |
| 11:59:46 | this run starts, with gctrace + pprof |

## Finding 1 — recovery is now visible in production (bf53de0 validated)

The manager log carries exactly what yesterday's incident lacked:

```
t=12:00:37 lvl=warn msg="recovering prior state" total=150472
t=12:01:37 lvl=warn msg="recovering: still recovering prior state" total=150472 elapsed=1m0.001s
t=12:02:37 lvl=warn msg="recovering: still recovering prior state" total=150472 elapsed=2m0.001s
```

One caveat worth recording for operators: these lines go to
`$MANAGERDIR/log`, **not** to the `-f` foreground terminal. The server logs on
the context handler, which is the file handler; the foreground stream only
carries `cmd`'s own `info()` lines (token, "started on", web URL) plus gctrace.
So `-f` plus `tee` does **not** capture them — read the manager log.

## Finding 2 — recovery is O(live jobs x dep-group membership) in bolt lookups

The goroutine dump (`goroutine?debug=1`, 12:03:13, 50 goroutines total) puts the
single recovery goroutine here:

```
recoverPriorJobsAndNote -> recoverPriorJobsWithHeartbeat -> recoverPriorJobs
  -> recoveredItemDef
    -> Dependencies.incompleteJobKeys
      -> Dependencies.incompleteJobKeysByDependency
        -> (*Dependency).incompleteJobKeysWithSeen
          -> (*db).retrieveIncompleteJobKeysByDepGroup   [2 frames]
```

Reading the code that stack names:

- `recoverPriorJobs` loops over all 150,472 recovered jobs and calls
  `recoveredItemDef` for each, building `itemdefs` before **any** enqueue.
- `recoveredItemDef` calls `job.Dependencies.incompleteJobKeys(s.db)`.
- `incompleteJobKeys` **always** calls `db.depGroupsEverSeen(...)`, which opens a
  bolt `View` transaction **unconditionally — even when the job has no
  dependencies at all** (`jobqueue/db.go`: the `View` wraps the loop, so an empty
  slice still costs a transaction).
- Each `DepGroup` dependency then calls `retrieveIncompleteJobKeysByDepGroup`,
  which opens another `View` and **cursor-scans `bucketDTK` (`depgroupToKey`) for
  that group's prefix**, doing a `bucketJobsLive.Get(key)` per member. That
  bucket holds **250,000+ keys**.

So the per-job cost is one transaction minimum, plus a prefix scan and a live-
bucket `Get` per dependency-group member. Summed over 150,472 jobs that is
hundreds of thousands of bolt read transactions and millions of B-tree lookups,
**single-threaded**, against a DB whose pages are entirely cold — the compaction
wrote a brand-new 7 GB file, so nothing is in page cache. This is the mechanism
behind both the 21-minute recovery at 09:23 and this run's.

Note the compaction did not *cause* this; it removed the page-cache warmth that
was hiding it. A warm restart on 2026-08-24 recovered a comparable job count in
~40 s.

## Finding 3 — recovery's memory is large and grows with progress

`GODEBUG=gctrace=1`, live heap after each GC (third number), this run:

| elapsed | total->peak->live heap | GC goal |
|---|---|---|
| 102 s | 3419 -> 3449 -> **1875 MB** | 3461 MB |
| 232 s | 9831 -> 9883 -> **5426 MB** | 9947 MB |
| 346 s | 15275 -> 15343 -> **8448 MB** | 15441 MB |

Live heap is growing at roughly **26 MB/s** while recovery walks the job list,
and the GC goal tracks at about 2x live. `heap?debug=1` at 12:03:13 reported
`Sys = 9.0 GB`, `HeapAlloc = 6.5 GB`, `HeapInuse = 6.6 GB`, `NextGC = 9.5 GB`.

Everything is retained until the single-batch enqueue: the 150,472 decoded
`*Job`s from `recoverIncompleteJobs`, the 150,472 `*queue.ItemDef`s built on top
of them, and the per-job dependency key slices and maps. Nothing is streamed or
released incrementally.

At 26 MB/s over a 21-minute recovery that extrapolates to tens of GB of live heap
and roughly double that in total heap. This node has 182.7 GB so it survives, but
the shape is O(live jobs) with a large constant, single-threaded, and invisible
without gctrace.

## Finding 4 — the 16m39s post-recovery stall (from the 10:05 run; this run pending)

From the dead manager's log, every number derived rather than assumed:

- 14 `getin repgroup=ibackup_fofn_ match=prefix` calls returned the **identical**
  trivial result (`replyJobs=92 replyBytes=127994`) with durations 1m05s, 2m05s,
  3m05s ... 13m57s — one arriving per minute from a polling client — and **all
  completed in the same second**, 10:01:25. Work that returns 92 jobs does not
  take 14 minutes; they were waiting.
- The longest blocked calls were `jtouch` **16m38.97s** and `add` 16m33.76s.
  `jtouch`, `add` and `getin` are three unrelated paths that share the **queue
  mutex**, so a single server-wide holder is implied.
- The window opens at 10:01:25 minus 16m39s = **09:44:46**, i.e. **14 seconds
  after `finishRecovering`**.

A `jtouch` blocked 16m39s is far past TTR, so the unblock hands the false-lost
machinery a pile of "lost" jobs — consistent with the `reserve` storm at 10:03.

Leading suspect remains `rescheduleReadyAfterRecovery` ->
`triggerReadyAddedCallback` over 150k freshly recovered ready jobs with a cold
scheduler-group memo (`8087866`). **Not yet confirmed.** A block/mutex profile
inside the window settles it; the capture is armed on this run.

## Finding 5 — the 10:05 death is still unexplained, and OOM is only half-credible

No panic, no error, no `die()` line: the log simply stops, and the backup was cut
off mid-write with `db_bk.tmp` abandoned at 1.1 GB. That means a signal, not a
Go-level failure. But `lshosts` reports **182.7 GB** on this node, so "wr alone
exhausted the node" does not hold at the double-digit-GB heaps measured here.

Two readings survive: the node was under **combined** memory pressure (it is a
shared LSF execution node, so other tenants' jobs count, and the OOM killer picks
the largest process, which wr would be), or something else signalled it. This is
settled on the node itself, not from here:

```
dmesg -T | grep -iE 'killed process|out of memory'
journalctl -k --since "2026-08-25 10:00" --until "2026-08-25 10:10"
```

Until that is checked, do not attribute the death to memory.

## Artifacts

Sampling scripts and captures are in this session's scratchpad
(`stall-260825/`): `goroutines.tsv` (30 s cadence), plus `block`/`mutex`/`heap`/
`goroutine` snapshots at 5-minute intervals and a dense event-triggered burst on
the post-recovery window. `goroutine?debug=2` is used sparingly, per the
2026-07-28 lesson that it ruined a CPU profile above ~50k goroutines (this run
sits at 50 goroutines during recovery, so it is currently cheap).


## Finding 6 — ROOT CAUSE: recovery retains one dep-group membership list per job

`dmesg -T` on farm22-ibackup01, supplied by the operator, shows **four** OOM
kills of `wr` (UID 13912 = mercury), not one:

| when | pid | anon-rss | total-vm |
|---|---|---|---|
| Mon 2026-08-24 15:54:06 | 1146901 | **180.5 GB** | 204.9 GB |
| Mon 2026-08-24 19:18:56 | 1284336 | **181.4 GB** | 206.2 GB |
| Mon 2026-08-24 20:15:59 | 1284341 | **181.4 GB** | 204.7 GB |
| Tue 2026-08-25 10:06:51 | 2305223 | **174.8 GB** | 190.5 GB |

So the 10:05 death was an OOM kill of wr itself on a 182.7 GB node — wr alone,
not combined tenant pressure — and the "manager died overnight" was the same
thing three times over. `journalctl -k` shows nothing because the operator's
account cannot read the kernel journal; `dmesg` is the channel that works.

`go tool pprof -top -inuse_space` against the live manager at 12:11:33, live heap
**15.65 GB**:

```
      flat  flat%   sum%        cum   cum%
10097.55MB 64.50% 64.50% 10156.55MB 64.88%  (*db).retrieveIncompleteJobKeysByDepGroup.func1
 5086.06MB 32.49% 96.99%  5086.06MB 32.49%  sortedStringSet
  159.28MB  1.02% 98.01%   159.28MB  1.02%  codec.(*decoderBase).detach2Str
  142.14MB  0.91% 98.91%   375.94MB  2.40%  (*db).recoverIncompleteJobs.func1.1
    2.50MB 0.016% 98.93% 15270.73MB 97.55%  (*Server).recoveredItemDef
```

**97.55% of the heap hangs off `recoveredItemDef`**, and the 150,472 decoded jobs
themselves are only **376 MB (2.4%)**. The memory is not the jobs — it is their
dependency key lists:

- `retrieveIncompleteJobKeysByDepGroup` returns **every** incomplete job key in a
  dep group, allocating a fresh `string(key)` per member (`jobKeys = append(...)`).
- It is called **once per job per dep-group dependency**, so every job that
  depends on group G gets its **own private copy** of G's whole membership.
- `sortedStringSet` then makes a second copy of each of those sets (its
  `make([]string, 0, len(set))`), which is the 5,086 MB.
- All of it is retained: the slices go into the `*queue.ItemDef`s that
  `recoverPriorJobs` accumulates for all 150,472 jobs before enqueuing any.

The cost is therefore **O(live jobs x dep-group membership)** — quadratic in the
common case where many live jobs share one big dep group. The de-duplicated cost
would be tiny: `depgroups` holds 6,299 distinct groups and `depgroupToKey` at
least 250,000 memberships in total, so resolving each distinct group once is
single-digit MB against the measured 175+ GB.

Growth this run, from gctrace's live-heap column, is a steady **~28 MB/s**:

| elapsed | live heap | total heap | GC goal |
|---|---|---|---|
| 346 s | 8448 MB | 15275 MB | 15441 MB |
| 607 s | 15694 MB | 28389 MB | 28699 MB |
| 715 s | **18728 MB** | 33875 MB | 34246 MB |

The GC goal tracks ~2x live, so live heap near 90 GB means total near 180 GB.
At 28 MB/s that is ~42 minutes from 12:11, i.e. **~12:54**; the previous run was
killed 44 minutes after start, which for this run would be **12:43**.

### This also re-reads Findings 3 and 4

Finding 3's "large memory" is this bug, not an inherent cost of holding 150k
jobs. And Finding 4's 16m39s stall is now more likely **GC saturation near the
memory ceiling** than a queue-mutex holder: at 715 s the GC was already burning
**2.5 s of CPU per cycle** across 16 Ps every ~54 s, and that grows with live
heap. A single-holder mutex theory is no longer needed to explain why `getin`,
`add` and `jtouch` all stalled and all completed at once. The armed block/mutex
capture will still distinguish them, but Finding 6 is the thing to fix first.

### Why GOMEMLIMIT will NOT save it

An earlier suggestion of mine to cap `GOMEMLIMIT` was wrong and is withdrawn.
This memory is **live (inuse_space), not garbage**: capping the limit would make
the GC run continuously against a heap it cannot shrink, degrading the manager to
a crawl and still ending in an OOM. The allocation has to stop being made.

## Next steps

1. **Fix the duplication, not the symptom.** Resolve each **distinct** dep group
   once per recovery — a single pass building `map[depGroup][]string` — and give
   every job that references it the shared result. That turns
   O(jobs x membership) into O(total membership) in both bolt lookups and bytes,
   fixing the 20-minute recovery and the OOM together.
   **Caveat found while checking feasibility:** `queue/item.go:239-241` mutates
   `item.dependencies[i]` in place during a key rename, so a shared backing slice
   is not safe as-is. Either make that path copy-on-write, or hand each item a
   copy while still sharing the *resolution* (which fixes the time cost and the
   duplicate `sortedStringSet` copy, but leaves per-item memory O(N x k)). Doing
   this properly is a design decision about how the queue stores dependencies, so
   route it through `/spec-writer` rather than a quick patch.
2. **Cheap independent win, already confirmed by reading the code:**
   `Dependencies.incompleteJobKeys` calls `db.depGroupsEverSeen(...)` which opens
   a bolt `View` **unconditionally, even for a job with no dependencies**. Skip
   it when the job has no dep groups: a few lines, up to 150,472 fewer
   transactions per recovery.
3. **Emergency lever for the operator, if prod must stay up before the fix
   lands:** the only thing that shrinks this heap is fewer live jobs in big dep
   groups. The 10:05 run had a ~4-minute serving window (10:01:25-10:05:19)
   between the stall clearing and the OOM; that is the window in which a
   `wr remove` of a chunk of the `ibackup_fofn_*` backlog would take effect.
   Nothing else available at runtime changes the curve.
4. **Do not compact again expecting relief.** Compaction is unrelated to this
   bug; it only removed the page-cache warmth that made recovery's *time* cost
   visible. The 2026-08-24 15:54 OOM happened on the uncompacted 15.7 GB DB.
5. Re-check `dmesg` after each future OOM: it is the only channel that recorded
   these four kills, and the ring buffer rolls over.

## Finding 7 — Finding 4's "stall" is GC-assist saturation, not a mutex holder

Measured on this run at 12:31, 29.8 minutes into recovery, with gctrace:

```
gc 70 @1656.347s  0.16+14082+0.04 ms clock,  29+0.44/56326/0+0.59 ms cpu,  65663->66964->57207 MB,  93856 MB goal
gc 71 @1790.443s  3.4+24294+0.18  ms clock,  54+17/97154/0+2.8   ms cpu,  78961->81146->69593 MB, 114415 MB goal
```

Live heap **69.6 GB**, and a single GC cycle now costs **24.3 s of wall clock and
97 s of CPU** across 16 Ps. At that point the collector is the workload: every
goroutine that allocates is conscripted into GC assist, so unrelated RPCs
(`getin`, `add`, `jtouch`) all block and then all complete together when a cycle
ends — exactly the 10:01:25 signature in Finding 4, with **no single lock holder
required to explain it**. Treat the queue-mutex hypothesis as retired unless the
armed block/mutex capture contradicts this.

Growth also **accelerates** — 28 MB/s at 715 s, 47 MB/s between 715 s and 1790 s
— because the death spiral feeds itself: more live heap, more GC CPU, slower
recovery, more time spent allocating.

### The escalation: prod can no longer complete startup

The 09:22 run finished recovery in 21 minutes. This run was **still recovering at
29 minutes** with live heap at 69.6 GB and a projected OOM at roughly 12:40, so it
is likely to be killed **before recovery finishes**, never having served a single
client request. That is worse than the earlier failures: it is no longer "prod
dies after an hour", it is "prod cannot start".

Note the two ways out are both blocked at runtime: client commands that would
shrink the live set (`wr remove`) need the queue, which is not populated until
recovery ends, and `add`-only availability during the window does not help. So
until the Finding 6 memory fix lands, bringing prod up needs the live job set
reduced **offline**, which wr has no built-in tool for. If that becomes the
chosen route, build and prove the surgery against `db_bk_precompact` (or another
copy) first, never against the live DB — and expect to justify every deleted key.
