# Feature: speed up `wr status` under heavy load (perfect accuracy retained)

Make the `wr status` CLI faster at large scale by cutting the per-job work in
three code paths, **without changing any user-visible behaviour** and **without
sacrificing accuracy**. These are internal speedups only: for every mode and
flag combination, the command's output (lines, ordering, values, exit codes)
must remain byte-identical to today's.

Full analysis, cost/gain estimates, the exact code paths, and the design
rationale are in `.docs/statusspeed/background.md` — **read it first**; it is the
authoritative reference and means you should not need to re-discover any of this.
Also read repo-root `DEVELOPERS.md` (the reliability rules any change here must
respect). This work is on the `statusspeed` branch.

Implement all three of the following (#1, #2, #3). They are largely independent;
#1 and #3 are read-path-only, #2 adds a write-path record.

## Overarching invariants (apply to all three)

- **Perfect accuracy via on-demand derivation.** Counts/summaries must continue
  to be recomputed from real live state + the archive on each call. **Do not
  introduce any maintained server-side counter or per-RepGroup running
  aggregate** — that is the drift/startup-scan trap DEVELOPERS.md rules 2, 6 and
  10 forbid. (Design B in the background doc is explicitly rejected.)
- **No user-facing change.** Same output, same flags, same exit codes. The
  fast paths are selected internally; the user cannot tell which ran.
- **No startup cost / no history cold-scan** (rule 6). In particular #2 must
  **not** back-fill records for pre-existing completed jobs.
- **Old-manager compatibility.** A new-CLI talking to an old manager (that lacks
  a new RPC method) must transparently fall back to today's behaviour — reuse
  the existing `ErrUnknownCommand` fallback pattern (see `fastStatusUnsupported`
  / `getFastStatusSummaries` in `cmd/status.go`).
- **Locking discipline** (rules 1, 2). Keep the existing pattern: snapshot via
  `queue.AllItems()` (brief `queue.mutex.RLock` for the slice copy only) then
  per-job RLock during the scan; the `-i` path uses the `rgToKeys` index. Never
  hold `queue.mutex` or a server-wide lock across the scan or across the DB read.

## #1 — `plain` output: lightweight key+state fast path (read-only)

**Problem:** `-o plain` currently fetches full `*Job` clones for every matching
job (and in `-i rg` mode decodes every archived job) only to print
`key<TAB>state` lines. This is the least efficient mode relative to its output.

**Want:** a new server RPC + client method that returns `(key, state)` pairs
instead of full jobs, wired into the `plain` branch of `cmd/status.go`.

- Live jobs: enumerate as the counts path does (`AllItems()` for default/`-a`;
  `rgToKeys.Values(rg)` for `-i`), read state under RLock, emit `(key, state)`.
  No `copyJobForClient`.
- Complete jobs: state is always `complete`; enumerate keys from `bucketRTK` by
  repgroup prefix and emit `(key, "complete")` — **no job decode**. Preserve the
  live-wins-over-complete dedup (a complete key whose job is currently live is
  skipped; see `db.addCompleteJobStatusByRepGroup`) so no job is listed twice.
- Selection: support default (all incomplete), `-i` (repgroup, incl. `-z`
  substring and `-a`), and the state filters (`-r`/`-b`/`--pending`/etc). The
  path is usable only when no full-`Job` field is required — gate it exactly like
  the summary fast path: `-o plain` AND `!statusRequiresFullJobFetch()` (so not
  with `--host`, `-f`, `-l`, `-y`, `--recent`, `--missing_deps`). Outside that
  gate, keep today's full-fetch plain path.
- Output & semantics unchanged: one `key<TAB>state` line per job in the same
  order as today, and **exit code 1 if any job is buried**, else 0.
- Fallback: on `ErrUnknownCommand` from an old manager, fall back to the current
  full-fetch plain implementation.

## #2 — `summary` output: compact per-complete-job stat record (write path)

**Problem:** `-o summary` decodes every complete/archived job in scope just to
extract the handful of values `RepGroupStatus.AddCompleteJob` consumes.

**Want (Design A — per-complete-job immutable record):**

- At archive time, write a small immutable record into a **new bolt bucket**,
  keyed by the same job key used in `bucketJobsComplete`, holding exactly the
  fields the summary needs: `PeakRAM`, `PeakDisk`, `CPUtime`, `StartTime`,
  `EndTime` (walltime is derived as `EndTime - StartTime`, matching
  `Job.WallTime()` for completed jobs). Choose the encoding for compactness
  (fixed-width binary preferred over Binc for size + decode speed; implementer's
  call — see background doc for the size analysis).
- Write it inside the **existing** `archiveJobTx` Batch transaction (same place
  as `putJobStats`), so it commits atomically with the complete-job write and
  adds no extra fsync.
- Create the bucket via `CreateBucketIfNotExists` at DB open (alongside the
  existing buckets).
- Read path: in the summary-details path (`db.addCompleteJobStatus` with
  `includeDetails=true`, reached via `getStatusByRepGroup`), read the record and
  feed a new `RepGroupStatus.AddCompleteJobFields(...)` helper (numerically
  identical to `AddCompleteJob`). **If the record is absent** (a job archived by
  an older version), **fall back to decoding the full job** as today — so old
  DBs keep working and no back-fill is needed.
- Keep `bucketRTK`/the complete bucket authoritative for the complete **count**
  (so a missing/orphan record can never make the count wrong). If a
  complete-job-delete path exists, delete the record there too (tidiness/size).
- The resulting summary output (means, SDs, started/ended/elapsed, counts) must
  be identical to today's within normal float formatting.

**Do NOT** implement a per-RepGroup running aggregate (Design B): it is a
maintained counter and violates the accuracy invariant above.

## #3 — `details`/`table`/`json`: clone/decode only the representatives (read-only)

**Problem:** the full-fetch grouped path materializes **every** live job
(`copyJobForClient`) and decodes **every** complete-in-rg job before grouping,
then keeps only `limit` representatives per group.

**Want:** group first on the cheap scalar key already used
(`state.exitcode.failreason`, read via `getJobProps` under RLock, no clone), and
only materialize the ≤`limit` representatives that will actually be returned:

- Live jobs: read grouping fields cheaply; clone (`copyJobForClient` /
  `itemToJob`) only the representatives kept per group.
- Complete jobs: they all collapse to one group (`complete.0.`), so enumerate
  their keys cheaply (for the total/`Similar` count) and **decode only `limit`**
  of them, instead of decoding all.
- Preserve **exactly**: the grouping, the `Similar` counts, offset handling, the
  populate-STDOUT/STDERR/env behaviour, and the `--limit 0` "show all
  individually" special-case. Output for every mode/flag combination must be
  byte-identical to today's.
- This is a refactor of `limitJobs` / `filterAndGroupJobs` /
  `groupJobsByCharacteristics` / `addJobToGroup` / `applyOffsetToGroups` and the
  up-front materialization in `getAllQueueJobs` / `getQueueJobsByRepGroup` /
  `getDBJobsByRepGroup` / `getJobsCurrent` / `getJobsByRepGroup`. #3 is
  independent of #2.

## Out of scope / non-goals

- No change to the web UI or its status feed.
- No new user-facing flags, output formats, or output changes.
- No maintained counters/aggregates; no startup back-fill.
- Not trying to make the scans sub-linear — these are constant-factor wins.
  (The O(live) incomplete scan is intrinsic to perfect accuracy and stays.)

## Acceptance criteria (for the spec's tests)

- **Parity:** for each mode (`plain`, `summary`, `details`, `table`, `json`,
  `counts`) and representative flag combinations, the new fast path produces
  output identical to the pre-existing full/decode path (drive both and compare;
  make the old path reachable in tests). Include buried-job exit-code behaviour
  for `plain`.
- **#1:** with the new path, `plain` does not fetch full jobs / does not decode
  archived jobs (assert via the path taken and/or a benchmark); old-manager
  fallback works.
- **#2:** an archived job gets a record; summary uses it; a job archived without
  a record (simulating an old DB) still yields correct summary via decode
  fallback; the record write is inside the archive transaction; new bucket is
  created on open; DB remains readable by the fallback path.
- **#3:** `Similar` counts, grouping, offset and `--limit 0` output are
  unchanged; only `limit` representatives are materialized per group (assert the
  reduced clone/decode count via a benchmark or an injected counter).
- **Benchmarks** demonstrating the constant-factor win for #1 (large plain), #2
  (large archived summary) and #3 (large details), per the estimates in the
  background doc.

## Testing strategy & validation

**Development is VM-first and single-machine.** All implementation and the great
majority of validation happen on a small (e.g. 4-core, no-LSF) VM using the
`local` scheduler; switching machines mid-development is not feasible, so there
is **one** real-LSF gate, run **at the very end**, which may then feed fixes (see
the fix loop below). This is acceptable *because* the work is read-path
constant-factor reduction (#1, #3) plus one Put inside the existing archive
transaction (#2), and because the status-query cost these changes attack is a
function of **job count**, not of how jobs were scheduled.

**Build scale without LSF and without needing many runners.** Job count, not
concurrency, drives status cost, so on the VM:
- large **live** set: add many more jobs than cores (stay `ready`), plus jobs
  with unmet deps (`dependent`), buried jobs (`buried`), and suspended jobs
  (`suspended`);
- large **completed history**: add and drain many trivial (`true`) jobs, in one
  and in many report groups, to exercise #2 and the `-i`/`-a` complete-scan paths
  and to measure DB-size growth.

**Fully covered on the VM** (TDD throughout; GoConvey; `make test` / `make race`
with all `OS_*` unset — DEVELOPERS.md §3):
- output **parity** for every mode/flag combo (drive both the old full-fetch/
  decode path and the new fast path and assert identical output, incl. `plain`
  buried→exit-1);
- **#2**: record written in the archive txn, decode-fallback when a record is
  absent (old-DB simulation), bucket creation on open, DB-size growth;
- **#3**: `Similar`/grouping/offset/`--limit 0` invariants and "only `limit`
  representatives materialized";
- **benchmarks** demonstrating the per-mode constant-factor wins at 100k+ scale;
- `-race`.

**Constraint that makes a single end-gate safe (must hold, or re-plan).** Keep
#1/#3 read-path-only and #2 to a single Put in the existing `archiveJobTx` Batch
transaction: **no new locks, nothing held across an external call, and no change
to the RPC reader/concurrency model** (DEVELOPERS.md rules 1, 2, 9). Because
there is no mid-development LSF checkpoint, staying within this shape is what
justifies validating hot-path interaction only once, at the end. If any change
would cross this line, stop and reconsider the design rather than deferring the
risk to the end gate.

**Final real-LSF gate (run once, at the end; expect it may feed fixes).** A VM
cannot reproduce genuine distributed churn, so the gate must specifically target
the two LSF-only risks, not merely re-run correctness:
1. **#2 under real churn** — `developers/wrdev.sh churn`: confirm archive
   throughput and forward-progress are unchanged vs a pre-change baseline (the
   extra Put must not perturb the add→run→archive critical path or the scheduling
   callback).
2. **Status-during-churn responsiveness** — run `wr status` in every mode
   (especially `-o details`/`-o plain` on a big group and `-o summary` on a large
   history) repeatedly *while* `churn` runs; confirm each call stays low-ms and
   does not slow the fleet (DEVELOPERS.md §4 "control-RPC unresponsiveness").

(No web-feed change is made, so the `web-burst`/`flicker-check` web-UI repros are
not required for this work.)

**Fix loop.** If the gate reveals a regression, reproduce it as closely as
possible back on the VM (e.g. high local archive throughput; large live/complete
sets), fix, re-run the full local suite, then re-run the LSF gate. Budget for at
least one fix-and-re-gate iteration. Any fix that touches locking or concurrency
had no earlier real-scale exposure, so scrutinise it especially hard at the
re-gate.
