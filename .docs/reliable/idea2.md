# Idea 2 — Non-blocking startup: respond first, recover in the background

**Type:** architectural reorder of `Serve()` (medium change).
**Goal:** the manager becomes responsive to clients in ~1 s **regardless** of DB
size, running-job count, history, scheduler, or storage — so a `kill -9` restart
can never be "stuck". Recovery and seeding happen after the manager is already
answering.

## The problem it targets

`testing.md` proves the manager is **up but silent** for the entire
`loadPriorState` (decode + seeding), which is 162 s on the real DB (and 175 s
with the local scheduler). This is because `Serve()` runs, in order: `initDB` →
`createQueue` → **`loadPriorState`** → `persistToken` → `serveClients`. The
readiness gate (token + a working `serveClients`) is **after** all recovery. So
any slow recovery = a non-responsive manager = the operator kills it = the loop.

`v0.36.5` has the **same ordering** — so this idea is a generic structural fix
that also protects against future slow-startup regressions, not just `#547`.

## Design

Reorder and background the recovery:

1. `initDB` (keep — but see Idea 5 for making it cheap), `createQueue`.
2. **`persistToken` + start `serveClients` + web interface immediately.** The
   manager now answers `ping`, `status`, `add`, etc.
3. Enter a **"recovering"** mode. Kick off `loadPriorState` in a background
   goroutine that:
   - streams recovered jobs into the queue in batches (so `reserve`/scheduling
     can begin as soon as the first batch lands), and
   - seeds `statusState` off to the side (or not at all — see Idea 1a/Idea 3).
4. While recovering, client requests behave sanely:
   - `ping`/`status`/`manager status` work immediately (status may report
     "recovering: N/M jobs restored").
   - `reserve` returns jobs already restored; scheduling ramps up as recovery
     proceeds.
   - `add` works immediately (new jobs are independent of recovery); guard
     against a recovered job and a re-added identical job colliding by having
     recovery skip keys already present.

## Priority alignment

The manager is **never** non-responsive on startup — the "stuck restart" symptom
is eliminated by construction, whatever the cause (seeding, decode, deps, local
`/proc` scan, or a future addition). Web-UI seeding, if kept at all, runs in the
background and cannot delay readiness. This is the strongest structural guarantee
that "the web UI cannot affect operations" at startup.

## Trade-offs / risks

- **Concurrency is the hard part.** Clients can now observe a partially-recovered
  queue. Needs: a clear "recovering" state; recovery that is idempotent and
  batched; careful ordering so a job isn't handed out before its dependencies are
  restored; and correct handling of `add`/`remove`/`kill` that races recovery.
- Requires a "recovery in progress" flag consulted by the relevant handlers, and
  tests for every race.
- Bigger review surface than Idea 1, but no data-model or storage change.

## Trial checklist (prove it works)

- [ ] **Baseline.** `testing.md` S3: real-DB restart 162 s non-responsive.
- [ ] **Spike the reorder.** Behind `WR_ASYNC_RECOVERY=1`, in `Serve()` move
  `persistToken` + `go serveClients` before `loadPriorState`, and run
  `loadPriorState` in a goroutine that sets/clears an `s.recovering` flag.
- [ ] **Readiness.** Re-run S3: assert `manager status` responds in **< 2 s**
  (just `initDB`), while the manager log shows recovery still running for ~160 s
  afterwards. This alone proves the symptom is gone.
- [ ] **Correctness during recovery.** While recovering: (a) `wr add` a new job →
  returns immediately, job runs; (b) `wr status` returns and shows recovery
  progress; (c) once recovery completes, `statusState.snapshot()` and the queue
  equal the pre-kill ground truth (no lost/duplicated jobs).
- [ ] **Race tests.** Add Go tests: kill+restart with a client hammering
  `add`/`status`/`reserve` throughout recovery; assert final state == ground
  truth and `-race` is clean. Specifically test re-adding an identical job that is
  also being recovered (no double-run, no double-count).
- [ ] **Reserve ramp.** With 10k recovered running jobs (LSF, `testing.md` hold
  setup), assert reserves/scheduling begin within seconds of start, not after the
  full recovery.
- [ ] **No-regression.** S1 steady-state throughput unchanged; `make test`,
  `make race` clean; existing crash-recovery test ("Running jobs are recovered
  after a hard server crash") still passes with async recovery.
- [ ] **Compose.** Confirm it stacks with Idea 1c (local one-scan recover) and
  Idea 3/5 so that even the background recovery is fast.

## Coverage of the full test set (see testing.md acceptance criteria)

| id | criterion | how Idea 2 covers it |
|---|---|---|
| B | startup responsive | **core** — respond first, recover in background (definitive, any DB/history/scheduler) |
| C | false-lost consequence stays fixed | preserved; background recovery must not lose/duplicate jobs (race tests) |
| D | removed-on-refresh stays fixed | preserved; seed still includes complete-only RepGroups |
| E | false-lost CAUSE | **DONE in baseline (F0, a19d390); idea must not regress it** |
| F | status responsive under load | startup responsiveness is core; **steady-state** responsiveness needs F0 (+ Idea 1f) |
| G | throughput not regressed | preserved |

**Honest scope:** F0 (a19d390) is already in the baseline, so E is green; Idea 2's
job is **B (startup responsiveness)** by its core. Steady-state responsiveness (F)
beyond F0's touch gating still needs help (Idea 1f / Idea 4). Every trial re-runs
the `harness/` F0 test to confirm no regression.

- [ ] **Full acceptance set (testing.md).** With Idea 2 + F0: the 3 Go tests all
  PASS (esp. `TestReliableFalseLostUnderSaturation`, which needs F0);
  `exp_startup_ab.sh`/`exp_realdb_seed.sh` restart ≤ a few s (core);
  `exp_reconnect.sh` `wr status` bounded under 10k conns; `exp1.sh`/
  `exp_drive_ab.sh` ≥ v0.36.5. Record numbers.

## Trial results (spike on F0 baseline)

Env-gated `WR_ASYNC_RECOVERY` spike: in `Serve`, `persistToken` + `serveClients` +
web interface run BEFORE `loadPriorState`, which runs in a background goroutine
(with an `s.recovering` flag). ~51 exec LOC, one file. Needed one concurrency fix
(guard `recoveredRunningJobs` with a mutex).

**Effectiveness — restart time-to-responsive (40k incomplete / 10k running, LSF+NOSCHED):**

| storage | baseline (gate off) | spike (gate on) |
|---|---|---|
| local /tmp | 1.15 s (loadPriorState 624 ms on readiness path) | **0.51 s** |
| NFS | 12.61 s (loadPriorState 12.23 s) | **0.34 s** (37×) |

To-responsive ≈ `initDB + createQueue + web + token`, **independent of recovery
cost** — so it generalises to the S3 real-DB case (~1 s responsive even though the
162 s seeding still runs in the background). **Fixes B definitively and
generically** (any cause: decode, deps, seeding, history, NFS). Correctness during
recovery verified: status/ping/add all respond immediately; final accounting
correct (40000, lost=0, no dups).

**Key limitation:** the minimal reorder does NOT stream — `recoverIncompleteJobs`
decodes all jobs then one `AddMany` at the end, so **recovered jobs are invisible
and non-reservable until recovery fully completes** (NFS: queue 0/0 for ~10 s then
jumps to full). idea2.md's "stream in batches / reserve ramp" needs chunked,
dependency-aware decode+enqueue (more work). New work added during recovery IS
immediately live.

**Concurrency risks:** recoveredRunningJobs map race (fixed); re-add colliding
with a recovered job (foreseen — AddMany dedups by key only, so state can be wrong
if the re-add beats recovery); dependent-job ordering during recovery (foreseen).
Everything else already lock-safe.

**F0 regression:** 4/4 deterministic tests PASS (gate off AND on).

**Verdict:** **necessary but not sufficient.** It guarantees the manager is never
"stuck" on startup (the strongest fix for testing.md cause #2), but the recovery
*work* still happens in the background, so recovered jobs aren't usable quickly and
F is untouched. The real win is **Idea 2 + Idea 3** (maintained counters make
recovery cheap → responsive AND fully recovered in seconds) or **2 + 1a/1b/1d**;
**1c** removes the local `/proc` recovery cost; **4** adds F. Best combined, not alone.
