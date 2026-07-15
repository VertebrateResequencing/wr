# Idea 3 — Incremental persisted status counters (never scan history)

**Type:** data-model change (medium change, removes the root cause).
**Goal:** make the web-UI's per-repgroup counts a **maintained, persisted
quantity** so they are read in O(repgroups), never computed by scanning
O(history). This deletes the 190 s add / 162 s restart cost at its source.

## The problem it targets

`testing.md` S3 proves the entire add-time (190 s) and restart-time (162 s) cost
is `seedStatusStateForItemDefs` → `retrieveCompleteJobCountsByRepGroups`, which
**counts completed jobs by cursor-scanning every RTK entry for a repgroup and
doing a random `Get` into the 1.9 M complete bucket**. It is O(history) and cold
on NFS. It runs because the "completed" count for a repgroup is *derived on
demand* from the raw history.

## Design

Stop deriving; **maintain** it.

- Add a dedicated persisted bucket, e.g. `repGroupStateCounts`, keyed by
  repgroup, holding the absolute `map[JobState]int` (or a compact encoding of the
  few states). This is the persisted twin of the in-memory `statusState.counts`.
- Update it **incrementally and atomically with the job transition that causes
  it**: on archive (`complete++`), on add (`ready/dependent++`), on remove
  (`deleted++`/decrement), etc. — the same transition points that already call
  `applyTransition` in memory. The write rides the **same BoltDB transaction** as
  the job's own state write, so it stays consistent across crashes.
- **Startup seeding becomes:** load `repGroupStateCounts` for live repgroups —
  O(live repgroups) small reads, no history scan. `add` seeding disappears
  (the counter already exists / is created empty).
- Provide a one-off **repair/recompute** tool (run offline, or lazily in the
  background) that rebuilds the counters from history for a DB that predates them
  — so the expensive scan happens **once, off-line**, never on an operational
  path.

This keeps the `#533` "absolute idempotent state" web-UI design (counts are
absolute, coalescing-safe) but makes the *source* of the absolute counts a cheap
maintained value instead of a live history scan.

## Priority alignment

No operational path (`add`, `reserve`, `touch`, `archive`, startup) ever scans
history again. The web UI is fed from a counter that costs ~nothing to read and
is updated as a byproduct of writes the manager already does. Web-UI work is
reduced to a handful of extra bytes per transaction — it cannot block operations.

## Trade-offs / risks

- **Consistency is the hard part.** The counter update must be atomic with the
  job write (same bolt tx) or a crash mid-transition drifts the count. Needs the
  counter write folded into `archiveJob`/`storeNewJobs`/remove within one
  `bolt.Update`, plus the offline recompute/repair for safety and migration.
- One-time migration cost for existing large DBs (the recompute), but off-line.
- Slightly more write work per transition (one small bucket `Put`), negligible
  vs the fsync already happening — and it *removes* the per-archive
  `markPersistedJobStatusGroups` read tx (Idea 1e / R5), likely a net win.

## Trial checklist (prove it works)

- [ ] **Baseline.** `testing.md` S3: 190 s add, 162 s restart, both = seeding.
- [ ] **Spike the counter bucket.** Add `repGroupStateCounts`; on
  `archiveJob`/`storeNewJobs`/remove, write the incremental delta in the **same**
  `bolt.Update` tx. Add a `recomputeRepGroupCounts()` offline builder.
- [ ] **Replace seeding.** Behind `WR_COUNTER_SEED=1`, make
  `seedStatusStateForItemDefs` (startup + add) read `repGroupStateCounts` instead
  of scanning; recompute once for the real DB via the offline tool.
- [ ] **Kill the cost.** Re-run S3 on the real DB: assert the cold `add` to
  `ibackup_server_put` drops from 190 s to **< 50 ms**, and the restart drops from
  162 s to **~1–2 s** (initDB + decode only).
- [ ] **Correctness.** Unit test: drive add→run→complete→remove→re-add churn and
  assert `repGroupStateCounts` == a fresh recompute == `statusState.snapshot()`
  == ground truth, including cross-repgroup (jobs under multiple repgroups) and
  the archived-rerun path that `#547`/`260708` cover.
- [ ] **Crash consistency.** Kill -9 mid-churn; on restart assert counters match
  ground truth (they were written atomically with job state); run the repair tool
  and assert it is a no-op (no drift).
- [ ] **Scale.** Re-run S5 (1000-worker drive): assert throughput no longer
  decays with history (the per-archive read tx is gone) and archive latency is
  flat.
- [ ] **No-regression.** `make test`, `make race`, web-UI Playwright fixtures
  pass; the `#533` twitch/overcount fixtures still pass (absolute counts intact).
- [ ] **A/B vs v0.36.5.** S1 throughput ≥ v0.36.5; S3 add/restart now *better*
  than v0.36.5 (which scanned nothing but also had no counts — we now have both
  cheap and correct).
