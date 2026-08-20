# reliable4 farm-scale validation gate — 2026-08-20 (ALL SIX TARGETS PASS)

Spec: the "Validation gate for the whole batch" section of
`.docs/reliable4/prod-run-20260817.md`. This is Batch 4 of the plan in
`.docs/reliable4/next-steps-260819.md`.

Artifacts and all derivations: `/nfs/hgi/wr/sb10-pprof/valgate-20260820-082210/`
(41 MB; start at its `SUMMARY.md`).

## What was run

Commit `53c323f` (items A, B and C plus the seven earlier commits), on an
**isolated** prod-mode manager (`WR_JOBNAME_TOKEN=iso53782`, ports 53782/53783,
pprof `:6065`, `--runner_filelog`, `gctrace=1`, no `--debug`) over a **copy** of the
10 GB `pristine10` DB. Host farm22-wrstat01, 8 cores, load average 90-92 throughout.
`results_portal` limit **2000** (production's value, not 20000).

- Phase 1: `rgok` 77,000 x `sleep 1` (retries 30) + `rgfail` 38,000 x `sleep 1; exit 3`
  (retries 2) = **115,000 jobs**
- Phase 2: `rgok2` 77,000 x `sleep 4` (prod's 3.8 s walltime) + `rgfail2` 25,000
  failing = **102,000 jobs**
- **2000/2000 concurrent runners reached in under 2 minutes and sustained** — a
  materially harsher regime than 2026-08-17's ~686. **~365 archives/s vs prod's 12/s.**
- 342,999 executions; final state exactly 154,000 complete / 63,000 buried.

## The six targets

| # | Target | Was | Measured | |
|---|---|---|---|---|
| 1 | mean archive block < 5 s | 43 s | **3.87 s** ph1 / **3.22 s** ph2 / **3.55 s** overall | PASS |
| 1 | p99 < 60 s | over the 60 s floor | **< 10 s** — zero `slow request method=jarchive` across 154,000 archives | PASS |
| 2 | zero runner `receive time out` | 10 in 25 min | **0** / 342,999 executions | PASS |
| 3 | success jobs reaching `delayed` with exit 0 | continuous | **0** — `command ran OK` exactly 154,000, so zero re-runs | PASS |
| 4 | idle CPU, large limit-blocked backlog | 0.79 cores / 41.8% | **0.0027 cores** (0.27% of one core); `schedulerGroupSnapshot` 0.001 | PASS |
| 5 | `wr limit` / `wr suspend -i` < 5 s under full load | minutes / timed out | `limit` **33 ms** typical, p50 69, p99 673, **max 835 ms** (n=111 at 10 s spacing, zero over 5 s); `suspend -i rgok2` **595 ms**; `resume` **803 ms** | PASS |
| 6 | `wr resume -i <substr> -z` no heap excursion | 12.1 GB | **1,377 ms**, heap step **0** | PASS |

Target 1 was derived from the block profile (`-peek archiveCompletedJob`, delay
divided by archives; 98.9% of it inside `db.archiveJob`). Corroborated by **zero**
`BLOCKED` events — max minutes blocked in Batch/archiveJob/Commit/backup was 0 at
every 60 s census — peak 6,046 goroutines, and pprof fetch latency max 69 ms.

**The p99 instrument was item C1 itself.** The new slow-request warning fired
correctly for `add`, `jrelease` and `getrs` but **never once** for `jarchive` — so
it was demonstrably live, not silently broken. C1 validated itself while
validating Finding 2.

### Finding 6 — CONFIRMED FIXED
The handoff doc flagged that Finding 6 (control RPCs queueing behind the archive
backlog) was *expected* to fall out of Finding 2's fix but had never been
separately re-measured, and said to confirm it here rather than assume it.
Confirmed: `wr limit` returned in **33 ms** with ~2,000 archive waiters in flight.

## Item E — answered by measurement: DO NOT DO IT

The doc sequenced the queue-mutex work as "worth a look after Findings 2 and 3
land". It is not, and the measurement says so:

- `-peek 'RWMutex).Lock'` total block delay **~441,766 s -> 7,235.9 s = 0.89%** of
  all block delay (**61x less**), and the **ranking flipped**: `lockExistingItem`
  56.07% now leads, `Reserve` 43.00% second (was Reserve 63.6% / lockExistingItem
  24.5%).
- `handleReserve` **10.7% -> 2.46%** of block delay; `handleRelease` **39,190 ->
  8,918** block events.
- `bbolt freelist.(*hashMap).freePageIds` **18.6% -> 2.47% of CPU**, exactly as
  predicted from archives committing ~5x fewer transactions (`bolt_txns/job`
  0.995 -> **0.2000**, re-measured in-process at HEAD; it is not obtainable at
  runtime). This confirms the cost fell **for free** — and `NoFreelistSync` stays
  **dropped** (it breaks the fast-startup invariant; that 2026-07-29 decision stands).

**Two NEW hotspots displaced it, and are the better targets:**

1. **The DB backup chain is 27.68% of CPU at peak** — the new top consumer.
2. **`scheduler.(*lsf).snapshotReserved` + `lsf.reserved` = 21.35% of mutex hold.**
   bbolt's `db.rwlock` is no longer dominant at all (`Tx.close` 2.71%,
   `beginRWTx` 0.0006%, against 99.93% before).

## Items A, B and C in the wild

- **Item A was not exercised** — nothing failed to exec in this load. What *was*
  confirmed is the property A had to preserve: 342,999 reserves against 342,993
  predicted, i.e. exactly `Retries+1` attempts per failing job, with no unbounded
  pre-start retry loop.
- **Item B holds on the default path** (`wr status -i rg -z` = 1.4 s, heap step 0,
  over 154,000 archived) **but its recorded residual is reachable and serious**:
  `-o plain` = **6,975 ms**, VmHWM **+905 MB** (heap 380 -> 670 MB) on only 154,000
  records, extrapolating to **~12.6 GB on prod's ~2.15M**. The original 12.1 GB
  excursion is one flag away. Tracked as Bug D3 in `.docs/bugfixes/260820-2.md`.
- **Item C is emphatic**: **571 bytes of runner log per job** (196 MB over 342,999
  executions), longest line **199 chars** (was a 1,345,498-byte max). The manager
  log for 217,000 jobs is **6 lines**.

## Calibration traps this run found — read before the next profiling round

1. **`pristine10`'s 2.1M synthetic records are invisible to the status/control
   history paths** — its generator never populates `bucketRTK`/`bucketRGs`. So
   validating Finding 1 or item B against that fixture is a **FALSE PASS**: the code
   finds nothing to decode and looks fast however broken it is. Proven by
   measurement before the run; this gate used the 154,000 records the load itself
   archived. `pristine10` remains valid for DB *size*-driven effects (freelist,
   mmap page-in, backup copy cost).
2. **The pprof tracker's `archive > 50` FREEZE heuristic is now a false positive** —
   it fires on the coalescing writer's normal waiter queue. It produced 3 bogus
   FREEZE-STARTs here. Recalibrate `_track.sh` before trusting it.
3. A manager's **first** history query after restart on a 10.7 GB NFS DB costs
   **30-38 s of cold mmap page-in** (warm: 250-680 ms). C1 caught both, which is
   worth knowing before reading a cold-start latency as a regression.

## Housekeeping verified

Zero `wrpiso53782_*` LSF jobs left; the one `wrp_*` job belongs to a pre-existing
production manager and was untouched; all four managers not started by this run
confirmed alive afterwards; `pristine10` verified **unchanged** in both size
(10,729,893,888 bytes) and mtime (2026-07-26 02:37:04); the 10 GB DB copy, its
backup and 196 MB of runner logs removed.

**Only after this gate should a live-prod profiling round be considered** — that
condition is now met.
