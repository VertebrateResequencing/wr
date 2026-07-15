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

**Change:** add the existing `hasAnyClientSubscriptions()`/web-connected gate to
the **touch** path (skip stdout/stderr decompression + `JobUpdate` build when no
one is subscribed), and batch/skip `markPersistedJobStatusGroups` on archive
(it only feeds web-UI counts; it can be derived from the incremental counts).

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
