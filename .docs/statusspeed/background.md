# `wr status` CLI speed under heavy load — background & analysis

This document captures the full analysis behind the "status speed" work: why
`wr status` costs what it costs today, three accuracy-preserving speedups
(referred to throughout as **#1**, **#2**, **#3**), their expected gains, and
their costs. It exists so a future agent implementing this does **not** have to
re-derive any of it. Read this alongside repo-root `DEVELOPERS.md` (the
reliability rules any change here must respect).

Line numbers are as-of the `statusspeed` branch point and are hints only;
function names are the stable anchors.

---

## The question we investigated

> Under heavy load, is there scope for the status CLI to be faster while
> retaining its perfect accuracy? If not `details` mode, then simpler counting
> modes? Or is CLI speed not a significant issue anymore following the last few
> commits since v0.37.1?

## TL;DR

- A **fast path already exists** for `counts` and `summary` (added in #514,
  before v0.37.0): `Client.GetStatusByRepGroupMatch` → `Server.getStatusByRepGroup`,
  which computes compact per-state `RepGroupStatus` summaries server-side
  instead of shipping full jobs.
- The commits since v0.37.1 (#547–#552) were about reliability + web-UI
  accuracy, **not** the status query cost itself. They made `wr status` *feel*
  fast under load only **indirectly**: the manager now stays responsive under
  churn (no startup cold-scan, no server-wide hot-path lock), so the control
  RPC that `status` rides on isn't queued behind a blocked manager. The query
  cost per call is unchanged.
- All improvements here **cut constant factors, not the O(N) complexity.** Below
  a few thousand jobs in scope every one of them is imperceptible. They matter
  at large scale and, for #2, large completed-job **history**.
- Perfect accuracy is preserved because every mode still **derives counts on
  demand** from live state + the archive; none of these changes introduce a
  maintained server-side counter (which is exactly the drift/startup-scan trap
  the reliability work spent months escaping — DEVELOPERS.md rules 2, 6, 10).

---

## 1. How `wr status` works today

The CLI (`cmd/status.go`) picks between two server-side strategies:

**Fast summary path** — used when `-o counts`/`-o summary` **and**
`!statusRequiresFullJobFetch()` (`cmd/status.go:849 canUseFastStatusOutput`,
`:862 statusRequiresFullJobFetch`, `:867 getFastStatusSummaries`). Calls
`Client.GetStatusByRepGroupMatch` (`jobqueue/client.go:2943`, RPC method
`"getrs"`) → `Server.handleGetRepGroupStatus` (`jobqueue/serverCLI.go:1494`) →
`Server.getStatusByRepGroup` (`jobqueue/server.go:1362`). Returns compact
`map[string]*RepGroupStatus`; no full `Job` structs on the wire.

**Full-fetch path** — everything else (`details`, `table`, `json`, `plain`, and
any mode with a full-fetch requirement). Fetches full `*Job` structs:
`GetIncomplete` (`client.go:2964`) / `GetByRepGroup` (`client.go:2916`) etc. →
`Server.getJobsCurrent` (`server.go:5067`) / `getJobsByRepGroup`
(`server.go:4951`) → `limitJobs` (`server.go:5198`).

### Cost model per mode

| Mode | Live jobs | Complete/archived jobs | Wire |
|---|---|---|---|
| `counts` (default, no `-i`/`-a`) | scan, RLock + read a few fields, **no clone** | **not scanned at all** (`IncludeComplete` false) | tiny |
| `counts -i rg` / `-a` | same | **key-only scan** of `bucketRTK` by prefix, **no decode** | tiny |
| `summary` | same as counts | **full codec decode of every complete job** for RAM/disk/walltime/cputime | tiny |
| `details`/`table`/`json` | **full `copyJobForClient` clone of every live job**, then group, return `limit` reps + `Similar` | full decode of all complete-in-rg (`-i` mode) before grouping | small (grouped) |
| `plain` | full clone of **every** matching job | full decode of all complete-in-rg (`-i` mode) | **all jobs** (limit forced to 0, no grouping) |

Key implementation facts:

- **Counts are decode-free.** `db.addCompleteJobStatus` (`db.go:1792`) with
  `includeDetails=false` just does `summary.AddState(JobStateComplete, 1)`;
  `includeDetails=true` (summary) decodes the full `Job`.
- **Default `wr status` does not touch complete jobs at all.**
  `includeComplete := cmdIDStatus != "" || cmdAll` (`cmd/status.go:878`).
- **Grouping key is cheap.** `groupJobsByCharacteristics` groups by
  `fmt.Sprintf("%s.%d.%s", jState, jExitCode, jFailReason)` (`server.go:5326`) —
  all scalar fields readable via `getJobProps` under RLock, **no clone needed**.
  Complete jobs (state=complete, exit 0, no failreason) therefore all collapse
  into a **single group** `"complete.0."`.
- **Locking is already clean.** `queue.AllItems()` (`queue/queue.go:1001`) holds
  `queue.mutex.RLock()` only long enough to copy the `[]*Item` slice, then the
  scan uses per-job RLocks — no server-wide exclusive lock (DEVELOPERS.md
  rule 2 intact). The `-i` path uses the `rgToKeys` index
  (`server.go:503 rgToKeys.Values`) so it never walks the whole queue.
- **`copyJobForClient`** (`serverCLI.go:1811`) copies ~46 fields incl. nested
  `Requirements`, slices and maps. `itemToJob` (`serverCLI.go:1781`) wraps it.
- **Store codec is reflection-based Binc** (`db.go:661 new(codec.BincHandle)`,
  `db.go:340 ch codec.Handle`), so a full `Job` decode is dominated by
  reflection + allocations for its slices/maps/strings.

## 2. What the recent commits (#547–#552) changed

`git log v0.37.1..HEAD`: #547 (panic/web bugs), #548 (false lost contact), #550
(speed+reliability at LSF scale), #551 (restore v0.36.5 completion/lost
semantics; removed the #533 `persistedstatus.go`/`statusstate.go` machinery),
#552 (web flicker, client-side). They touched `server.go`/`db.go`/`serverCLI.go`
heavily but for **completion/lost semantics, startup cost, and web-UI
accuracy** — not the `status` query cost. The fast counts/summary path predates
them (#514). Their relevance to CLI speed is **indirect**: DEVELOPERS.md rule 6
(no startup cold-scan) and rule 2 (no server-wide lock on the transition hot
path) keep the manager responsive, so `wr status` returns promptly under a
churning fleet — but the query itself costs the same per call.

## 3. The accuracy invariant (why we do NOT add a counter)

wr deliberately derives status counts **on demand** rather than maintaining a
server-side counter. DEVELOPERS.md rule 2 (no server-wide lock / no server-side
counter on the hot path), rule 6 (no cold-scan of completed history at startup),
and rule 10 (internal-only; the web bar was made exact **client-side** with no
server change) all point the same way. The whole flicker/overcount saga
(`.docs/flicker/`) is what a maintained counter drifting looks like. **Every
speedup here must keep the on-demand-derivation property**: the CLI is the
source of truth precisely because it recomputes from real state.

## 4. The three improvements

### #1 — `plain` key+state fast path (read-only)

`plain` currently clones and ships **every** matching job (`cmd/status.go:271`),
and in `-i rg` mode decodes every archived job, purely to print `key\tstate`
lines. Add a lightweight server method that emits `(key, state)` pairs instead
of full jobs:

- **Live jobs**: enumerate (as counts does — `AllItems()` for default/`-a`,
  `rgToKeys.Values(rg)` for `-i`), RLock, read state. No clone.
- **Complete jobs**: state is trivially `complete`; enumerate keys from
  `bucketRTK` by repgroup prefix — **no decode at all**. Must replicate the
  live-wins-over-complete dedup (`db.go:1780`: skip a complete key whose job is
  currently live) so a retried job isn't double-listed.
- Gate it exactly like the summary fast path: usable only for `-o plain` when
  `!statusRequiresFullJobFetch()` (i.e. not with `--host`, `-f`, `-l`, `-y`,
  `--recent`, `--missing_deps`, which need fields not in a `(key,state)` pair).
  State filters (`-r`/`-b`/…) and `-i`/`-a`/default must be supported.
- Old-manager fallback: on `ErrUnknownCommand`, fall back to the current
  full-fetch plain path (same pattern as `fastStatusUnsupported` in
  `cmd/status.go:906`).

### #2 — `summary` compact per-complete-job stat record (write path)

`summary` decodes every complete job (`db.addCompleteJobStatus` with
`includeDetails=true`) just to feed `RepGroupStatus.AddCompleteJob`
(`status_summary.go:135`), which consumes only: `PeakRAM`, `PeakDisk`,
`WallTime()`, `CPUtime`, `StartTime`, `EndTime`. For a completed job
`WallTime() == EndTime.Sub(StartTime)` (`job.go:501`), so the record needs just
**PeakRAM, PeakDisk, CPUtime, StartTime, EndTime** (walltime derived).

**Design A (recommended): per-complete-job immutable record.**
- New bucket, keyed by the same job key as `bucketJobsComplete`, holding the ~5
  values above.
- Written at archive time inside the **existing** `archiveJobTx` Batch txn
  (`db.go:823`, invoked via `db.bolt.Batch` at `db.go:1525`) — the same place
  and pattern as `putJobStats` (`db.go:1536`), which already does 3 Puts per
  archive. Atomic with the complete-job write; no extra fsync (Batch coalesces).
- Read path: in `addCompleteJobStatus`, when `includeDetails`, read the record
  and feed a new `RepGroupStatus.AddCompleteJobFields(...)`; **if the record is
  absent, fall back to decoding the full job** (today's behaviour). This means
  **no startup back-fill scan** (rule 6 respected) — the speedup accrues as new
  jobs complete; old DBs keep working.
- `RepGroupStatus`/`StatusMeasure` already use a mergeable Welford accumulator
  (`status_summary.go:35`), so feeding it from a record vs a decoded job is
  numerically identical.
- The `bucketRTK`/complete-bucket key set stays authoritative for the *count*,
  so an orphaned record (from any future complete-delete path) is harmless
  (never read without its complete key); still delete it where a complete-delete
  path exists, for tidiness/size.

**Design B (rejected): per-RepGroup running aggregate.** Tiny disk (O(repgroups))
but it is a **maintained counter** → the drift class the project avoids: retries
(complete→live→complete) and deletes must subtract; a logic bug drifts with no
cheap repair (detection needs a full rescan). Against DEVELOPERS.md rules 2/6/10.
Do not do this.

### #3 — `details`/`table`/`json` clone-only-representatives (read-only)

Restructure the full-fetch path so grouping happens on the cheap scalar fields
(`state`, `exitcode`, `failreason` via `getJobProps` under RLock) **without**
`copyJobForClient`, and only the ≤`limit` representatives per group are actually
materialized (live: cloned; complete: decoded). Because complete jobs collapse
to one group, only `limit` of them are decoded; the total count for the
`Similar` figure comes from cheap key enumeration.

- Touches `limitJobs`/`filterAndGroupJobs`/`groupJobsByCharacteristics`/
  `addJobToGroup`/`applyOffsetToGroups` (`server.go:5198`–`5375`) and the
  up-front materialization in `getAllQueueJobs`/`getQueueJobsByRepGroup`
  (`server.go:5094`,`4997`) and `getDBJobsByRepGroup` (`server.go:5012`).
- **Does NOT depend on #2.** (Earlier framing wrongly coupled them; #2 is only
  for `summary`, which must aggregate over *all* complete jobs.)
- Must preserve **exactly**: grouping, `Similar` counts, offset, and the
  `--limit 0` "show all individually" special-case, and byte-identical output.

## 5. Expected gains

Per-job primitive cost **estimates** (not measured): Binc full-`Job` decode
~5–20 µs; full-`Job` clone ~1 µs; full-`Job` encode+wire+client-decode ~10–25 µs
(bandwidth-bound in bulk); compact-record decode ~1–2 µs; `(key,state)` ~1–2 µs
and ~60 B.

| Scenario | Now | After | Speedup | Wire |
|---|---|---|---|---|
| **#1** `-o plain`, 100k live | clone+wire ≈ **1.5 s**, ~50 MB | key+state ≈ **0.2 s** | **~8×** | **~10×** less |
| **#1** `-i biggroup -o plain`, 500k archived | decode+wire ≈ **12 s**, ~250 MB | enumerate keys, no decode ≈ **1 s** | **~12×** | ~8× less |
| **#2** `-i biggroup -o summary`, 500k archived | full decode ≈ **5 s** | compact record ≈ **0.75 s** | **~6×** | unchanged (tiny) |
| **#3** `-o details/table/json`, 100k live, limit 1 | clone all ≈ **0.1 s** | field-read + clone reps ≈ **0.01 s** | ~10× but only **0.1 s** absolute | unchanged |
| **#3** `-i biggroup -o details`, 500k archived, limit 1 | decode all to group ≈ **5 s** | enumerate + decode `limit` reps ≈ **ms** | **large** | unchanged |

### Under what circumstances the improvement is noticeable

- **#1 (plain):** perceptible from ~10k jobs returned; seconds → sub-second at
  ~100k+; **largest for `-i` on a group with lots of finished history** (the
  archived-decode term disappears entirely). Its cost is dominated by *wire*,
  which #1 attacks directly. Most universal win.
- **#2 (summary):** only when the query's **completed-job scope** is large (a
  repgroup or `-a` with ≳100k archived). **Zero effect on default `wr status`**
  (skips complete). It's a "reporting on a big finished pipeline" win.
- **#3 (details/table/json):** end-to-end latency barely moves for the human
  (wire already small); the win is **server-side CPU/allocations** — except
  `-i` on big archived history, where decoding only `limit` reps instead of all
  turns a multi-second scan into milliseconds.
- **Frequency compounds all of it.** A `watch wr status` / monitoring poll that
  each time clones 100k jobs (or decodes 500k archived) generates sustained GC
  pressure and repeatedly takes the O(N) `AllItems()` RLock snapshot. Cutting
  the per-item constant reduces the per-poll footprint even when a single call's
  latency wouldn't be noticed — i.e. the query stops perturbing dispatch under
  heavy load.
- **Below ~1–5k jobs in scope: expect no visible difference.** The query is
  already instant; the recent reliability work already fixed the real historical
  pain (status hanging because the *manager* was stuck).

## 6. Costs

The three diverge sharply. #1 and #3 are read-path-only (no storage, no write
cost); **all persistent cost is in #2.**

| | Extra DB size | Extra write-txn cost | Other |
|---|---|---|---|
| **#1 plain key+state** | **none** | **none** | reduces query memory |
| **#3 details/table/json reps** | **none** | **none** | reduces query CPU/allocs; refactor risk |
| **#2 summary record (Design A)** | **O(complete jobs)**, ~0.1–0.2 GB / million | +1 small `Put` per archive (no extra fsync) | monotonic growth; bigger backups |

- **#1 / #3 have no persistent cost.** They add no bucket, touch no write txn,
  and lower peak query memory. #1's cost is code (new RPC method + client + CLI
  branch + old-manager fallback) + the dedup-correctness obligation; it still
  streams O(job-count) *elements* (each ~10× smaller), it doesn't bound the
  count. #3's cost is **refactor regression risk** — `Similar`, offset and
  `--limit 0` semantics must be preserved exactly; needs thorough tests.
- **#2 Design A DB size:** ~5 numbers ≈ 40–48 B data; with codec framing +
  Bolt's random-key (MD5) B+tree overhead, **~100–200 B/job on disk → ~0.1–0.2
  GB per million completed jobs**, growing monotonically (completed history
  isn't pruned). Dominant cost. A fixed-width binary encoding instead of Binc
  trims it and makes decode ~0.1 µs.
- **#2 write-txn time:** one extra small `Put` into the existing archive Batch
  txn — one B+tree insert + sub-µs encode + a few extra dirty pages per commit.
  On the critical path but small; visible only under high-rate churn. Same
  pattern `putJobStats` already uses.
- **#2 migration:** old completed jobs have no record → fall back to full decode.
  No startup back-fill (rule 6). New bucket created via
  `CreateBucketIfNotExists` at DB open (like `db.go:537`+).
- **Cross-cutting:** #2 inflates `backgroundBackup` (`db.go:1529`) size/time
  proportionally; #1/#3 don't touch the DB so no backup impact. None add startup
  cost. #2's record is in the archive txn, so no new crash-atomicity failure
  mode. All three need tests; #1/#3 need old-manager/parity coverage; #2 needs
  write-path + missing-record-fallback coverage.

## 7. Existing per-ReqGroup stat buckets (why they can't be reused for #2)

`putJobStats` (`db.go:1536`) already writes `bucketJobRAM`/`bucketJobDisk`/
`bucketJobSecs` at archive time, but keyed `reqGroup + delim + %20d(value)`
(`putJobStat`, `db.go:1561`) — i.e. **deduplicated distinct (ReqGroup, value)
pairs**, consumed by the scheduler's resource recommender
(`recommendedReqGroupStat`, `db.go:2576`). They are (a) per-**ReqGroup**, not
per-RepGroup (summary's dimension), and (b) lossy (dedup drops per-value
frequency), so they **cannot** produce a correct per-RepGroup mean/SD. #2 needs
its own record. Their existence does confirm the archive path already tolerates
several small Puts per job — #2 is one more of the same.

## 8. Code map (anchors for the implementer)

- CLI: `cmd/status.go` (`Run` switch `:227`; `canUseFastStatusOutput` `:849`;
  `statusRequiresFullJobFetch` `:862`; `getFastStatusSummaries` `:867`;
  `printStatusCounts` `:893`; `fastStatusUnsupported` `:906`; plain case `:271`;
  `getJobs*` `:1003`+). `cmd/status_table.go` (`statusOutputUsesGroupedJobs`
  `:411`; `statusOutputGetsStd` `:422`; format consts `:48`).
- Client: `jobqueue/client.go` — `GetByRepGroup` `:2916`,
  `GetByRepGroupMatch` `:2929`, `GetStatusByRepGroupMatch` `:2943`,
  `GetIncomplete` `:2964`.
- Server dispatch/handlers: `jobqueue/serverCLI.go` — request-method consts
  `:56`; `handleGetByRepGroup` `:1470`; `handleGetRepGroupStatus` `:1494`;
  `handleGetIncomplete` `:1515`; `dispatchMethod` `:1612`; `itemToJob` `:1781`;
  `copyJobForClient` `:1811`.
- Server status/fetch: `jobqueue/server.go` — `rgToKeys.Values` `:503`;
  `getStatusByRepGroup` `:1362`; `addCompleteJobStatuses` `:1385`;
  `addAllQueueJobStatuses` `:1462`; `addQueueJobStatusesByRepGroup` `:1469`;
  `addQueueItemStatus` `:1481`; `getJobsByRepGroup` `:4951`;
  `getQueueJobsByRepGroup` `:4997`; `getDBJobsByRepGroup` `:5012`;
  `getJobsCurrent` `:5067`; `getAllQueueJobs` `:5094`; `limitJobs` `:5198`;
  `groupJobsByCharacteristics` `:5315` (group key `:5326`); `addJobToGroup`
  `:5361`.
- DB: `jobqueue/db.go` — bucket consts `:88`; codec `:661`;
  `archiveJob` `:1512`; `archiveJobTx` `:823`; `putJobStats` `:1536`;
  `retrieveCompleteJobStatusByRepGroup` `:1760`; `addCompleteJobStatusByRepGroup`
  `:1769` (dedup `:1780`); `addCompleteJobStatus` `:1792`; `setBatchTuning`
  `:682`.
- Summary types: `jobqueue/status_summary.go` — `RepGroupStatus` `:94`,
  `AddCompleteJob` `:135`, `StatusMeasure` `:35`.
- Queue: `queue/queue.go` — `AllItems` `:1001`.

## 9. Notes, corrections, open questions

- **Correction:** #3 does **not** require #2 (complete jobs collapse to one
  group, so only `limit` need decoding). #2 stands alone as the `summary` fix.
- **Open (worth measuring before committing to #2):** the true per-job on-disk
  record size and decode cost — encode the record both as Binc and as fixed-width
  binary and measure, plus micro-benchmark full-`Job` decode vs record decode vs
  `copyJobForClient` vs `(key,state)` serialization for representative job
  shapes, to size the wins precisely. The µs/byte figures in §5/§6 are reasoned
  estimates from the cost structure, not measurements.
- **Validation expectation:** anything touching the archive write path (#2) or
  the hot-path scan interaction needs real-LSF Tier-B validation
  (`developers/wrdev.sh churn` / `web-burst`), not just in-process tests
  (DEVELOPERS.md §3–§4).
