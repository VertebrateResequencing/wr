# Idea 1 — Surgical in-place fixes (lowest risk, ship first)

**Type:** targeted bug fixes, no architectural change.
**Goal:** get the confirmed operational costs off the critical paths with the
smallest, most reviewable diffs, keeping the current `statusState` design.

See `testing.md` for the harness, metrics and baseline numbers this idea is
proven against. The confirmed root causes are:

- **R1 (primary):** `seedStatusStateForItemDefs` (`#547`) does a **cold
  O(repgroup-history) scan** into the 1.9 M complete bucket **on the `add` path
  and the startup path** — measured **190 s** to add one job to a 700k-history
  repgroup. This is web-UI machinery blocking operations.
- **R2:** everything in `loadPriorState` runs before the manager answers clients.
- **R3:** `local.recover` scans `/proc` **per running job** (local scheduler).
- **R4:** `initDB` freelist load grows with DB size (seconds).
- **R5:** per-archive `markPersistedJobStatusGroups` read-tx + ungated per-touch
  stdout/stderr decompression add per-op cost at scale.

Each fix below is independent and separately shippable/revertable.

## Fix 1a — Never seed statusState on the add path (kills the 190 s add)

**Change:** remove the `seedStatusStateForItemDefs` call from `enqueueItems`
(the `add` path). Adds must never scan history. The web-UI "completed" count for
a not-yet-seen repgroup is filled in **lazily and asynchronously**:

- On `add`, if a repgroup is unseen, enqueue it on a background "seed queue" and
  return immediately (mark it seeded-pending in `statusState`).
- A single background goroutine drains the seed queue, does the
  `retrieveCompleteJobCountsByRepGroups` scan **off** any operational path, and
  fills the count. The web UI shows the live counts immediately and the
  historical "complete" total appears a moment later.

Because live transitions already update `statusState` incrementally, the only
thing seeding provides is the **historical completed count for display** — which
is not operationally required and can lag.

## Fix 1b — Seed at startup off the readiness path

**Change:** `recoverPriorJobs` must not seed. Recover the queue (so jobs run),
persist the token, start `serveClients` — then seed `statusState` for live
repgroups in a background goroutine. Startup no longer waits on history scans.
(If Idea 2 is adopted this is subsumed.)

## Fix 1c — Recover the local scheduler once, not per job

**Change:** `local.recover` currently calls `process.Processes()` (full `/proc`
scan) for **every** running job. Enumerate processes **once** at the start of
recovery, build a `cmdline → pid` map, and look each recovered job up in it.
Turns O(running × processes) into O(running + processes). (Also fixes the
`kill -9` loop for local deployments.)

## Fix 1d — Cheaper `initDB` open

**Change:** open BoltDB with `Options{FreelistType: FreelistMapType}` (and
evaluate `NoFreelistSync` + a periodic/online compaction of the manager DB).
Measure the freelist-load contribution to `initDB` on the real DB and pick the
option that minimises it without risking durability.

## Fix 1e — Gate the remaining per-op web-UI work

**Touch-path gate: DONE in F0 (a19d390)** — `emitLiveTouchSnapshot` now gates the
stdout/stderr decompression + `JobUpdate` build behind `hasAnyClientSubscriptions()`.
**Remaining:** batch/skip `markPersistedJobStatusGroups` on archive (it only feeds
web-UI counts; Idea 3 removes it entirely by maintaining the counts).

## Fix 1f — Keep `wr status` off the runner-traffic path

**Change:** `wr status -i <rg>` uses the heavy `GetByRepGroup` → `AllItems()` path,
whose request competes with every runner's touch/reserve/reconnect message on the
single-reader mangos socket (`testing.md` S6: 26 ms → 460 ms at 10k runners).
Two cheap mitigations: (i) make `wr status`/the web UI use the already-cheap
`-o counts` summary path (`statusState`) by default instead of `AllItems`; (ii)
consider a separate listener/socket for human/status clients so a runner message
storm can't queue ahead of them (Idea 4 does this properly). Also apply the
touch-path subscriber gate (Fix 1e) so each touch is a smaller message to process.

## Fix 1g — Robust lost-detection so a touched job is never falsely lost — DONE in F0 (a19d390)

Implemented: `handleTouch` records `recordJobContact(key)` before the contended
processing, and `ttrCallback` keeps a job running (fresh TTR) if it was
`contactedWithin(key, ItemTTR)`, only marking Lost when there was no contact in
the window. Makes `TestReliableFalseLostUnderSaturation` pass while
`TestLostDetectionSilentRunner` still detects genuinely-dead runners. (F0 did not
add a separate touch *ingest lane*; it relied on cheap touches + latency-tolerant
detection, which sufficed. A dedicated ingest lane remains a possible future
hardening if runner traffic alone ever saturates the reader.)

## Coverage of the full test set (see testing.md acceptance criteria)

| id | criterion | how Idea 1 covers it |
|---|---|---|
| B | startup responsive | 1a/1b (seeding off startup path) + 1d (freelist) |
| C | false-lost consequence stays fixed | untouched; regression-checked |
| D | removed-on-refresh stays fixed | 1a/1b async seed **must still populate complete-only RepGroups** for fresh subscribers (verify) |
| E | false-lost CAUSE | **DONE in baseline (F0, commit a19d390 — subsumes the old 1e + 1g); idea must not regress it** |
| F | status responsive under load | 1e + 1f (status off the runner-traffic path) |
| G | throughput not regressed | no hot-path work added |

Idea 1 is the natural home for the shared **F0** fix (it is the surgical grab-bag);
with 1e+1g it fixes E directly.

## Priority alignment

After 1a/1b, **no web-UI code runs on the `add`, reserve, touch, archive, or
startup-readiness paths that scans history or blocks**. Web-UI accuracy is
preserved (counts are correct once the async seed completes; live counts are
always correct). If the async seed is ever a problem it can be disabled entirely
with counts simply omitting historical completed totals — operations are never
affected either way.

## Trade-offs / risks

- The historical "completed" bar for a repgroup may appear a few seconds late
  after add/restart. Acceptable per the brief.
- Several small diffs across `server.go`, `serverCLI.go`, `scheduler/local.go`,
  `db.go`; each needs its own regression test.
- Does **not** remove the underlying O(history) scan — it just moves it off the
  hot path. Ideas 3/5 remove it entirely. If history keeps growing, the async
  seed itself gets slow (but harmlessly, in the background).

## Trial checklist (prove it works)

- [ ] **Baseline.** Reproduce `testing.md` S3 on current HEAD: confirm the
  ~190 s cold add to `ibackup_server_put` and the multi-phase startup on the real
  DB. Record numbers.
- [ ] **Fix 1a spike.** Behind `WR_SEED_ASYNC=1`, remove seeding from
  `enqueueItems` and add the background seed queue. Rebuild `wr-head-safe`.
- [ ] Re-run S3: assert the first `add` to `ibackup_server_put` now returns in
  **< 100 ms** (was 190 s); assert the web UI's completed count for that repgroup
  becomes correct within a few seconds (poll `statusState.snapshot()` via a test
  endpoint or the status websocket fixture).
- [ ] **Fix 1c spike.** Behind `WR_RECOVER_ONESCAN=1`, enumerate processes once
  in `local.recover`. Re-run `testing.md` S2 (local, 10k running): assert restart
  drops from ~175 s to seconds.
- [ ] **Fix 1b spike.** Move seeding after `serveClients`. Re-run S2 (LSF) and the
  real-DB restart (S3): assert time-to-responsive is dominated only by `initDB` +
  decode, not seeding.
- [ ] **Fix 1d spike.** Open the real DB with `FreelistMapType`; re-run S4:
  compare `initDB` ms local & NFS vs baseline.
- [ ] **Fix 1e spike.** With web UI disconnected, drive `testing.md` S5 (1000
  workers) with/without the touch/archive gates; compare archive latency decay.
- [ ] **No-regression.** `make test`, `make race` clean; web-UI Playwright
  fixtures still pass (counts converge); `statusState.snapshot()` equals ground
  truth after the async seed in a unit test.
- [ ] **A/B vs v0.36.5.** Confirm S1 steady-state throughput is unchanged or
  better, and S3 add/restart are now comparable to (or better than) `v0.36.5`
  (which had no seeding at all).
- [ ] **Fix 1g spike.** Add touch/archive ingest priority + ingest-time
  last-contact stamping; run `TestReliableFalseLostUnderSaturation` → must go
  **PASS** (was FAIL on HEAD).
- [ ] **Full acceptance set (testing.md).** With all fixes on: the 3 Go tests
  (`TestReliableFalseLostRerun`, `TestReliableCompletedRepGroupRemovedOnRefresh`,
  `TestReliableFalseLostUnderSaturation`) all **PASS**; `exp_startup_ab.sh`/
  `exp_realdb_seed.sh` restart ≤ a few s; `exp_reconnect.sh` `wr status` bounded
  under 10k conns; `exp1.sh`/`exp_drive_ab.sh` ≥ v0.36.5. Record all numbers.

## Trial results (spike on F0 baseline, 4613ea9)

Env-gated `WR_SEED_ASYNC` spike of 1a/1b: `seedStatusStateForItemDefs` pushes
unseen repgroups to a background worker instead of scanning inline. ~105 LOC, 2
files, **no hot-path (reserve/touch/archive) changes**.

**Effectiveness (measured, local /tmp, warm, one repgroup with 60000 complete):**
- Cold `wr add` of one job to the 60k-history repgroup: **134 ms → 60 ms**; the
  74 ms inline seed scan is fully off the add path (converges ~111 ms later in the
  background).
- Restart: `loadPriorState` seed **87 ms → 1 ms** (off the readiness path). Total
  restart wall unchanged *at this scale* because `initDB` freelist (~300 ms) now
  dominates → **pair with Fix 1d**. At production scale (S3: 700k-history repgroup,
  cold NFS) the seed was 161.6 s of a 162.6 s restart, so the spike converts
  **~162 s → ~1 s** there. Benefit scales with history × cache-coldness.
- Background convergence produced correct counts (`complete:60000`).

**F0 regression:** all 4 deterministic guards PASS (gate off AND on).
**G throughput:** not regressed (no hot-path change). **F responsiveness:** NOT
addressed (that's the single-RecvMsg-reader contention = Idea 4).

**Risks found:** (1) the additive historical merge can **double-count** a job that
completes during the seed window — a production version must make the merge
idempotent (snapshot-at-enqueue or reconcile) — exactly what **Idea 3** removes by
maintaining exact counts; (2) web-UI complete count lags by the scan duration
(operations unaffected); (3) worker needs a lifecycle (stop before db.close).
Nuance: `wr status -o counts` reads complete via a **direct DB scan**, not
`statusState`, so the async seed only affects the **web-UI websocket** seed — and
that CLI path itself does an O(history) scan per call (motivates Fix 1f).

**Verdict:** low-risk, small, reviewable **stopgap** for B (startup + add stall);
must be paired with 1d to move restart wall time; its double-count weakness is
eliminated by Idea 3, and it does nothing for F (Idea 4). Good "ship now",
superseded by counters later.
