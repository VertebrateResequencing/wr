# Idea 4 — Decouple the web UI into a separate read-only projector

**Type:** process/architecture separation (large change; strongest guarantee).
**Goal:** make it **structurally impossible** for the web UI to affect
operations, by removing all status-projection work from the manager's execution
paths and running it as an isolated read-only consumer. This is the principled
version of the brief's "worst case, revert the web UI".

## The problem it targets

Every symptom in `testing.md` (190 s add, 162 s restart, per-archive read tx,
per-touch decompression, callback serialization) is web-UI projection code
executing **inside** the manager's operational paths and sharing its DB
transactions and locks. As long as that code lives on those paths, any future
web-UI feature can regress operations again (as `#547` did). The only way to
*guarantee* the brief is to move it out.

## Design

Split responsibilities cleanly:

- **The manager** does only: schedule, run, persist job state, and emit an
  **append-only transition log** (job key, from→to state, repgroup(s), timestamp)
  — a cheap sequential write it can do as a byproduct of the state writes it
  already makes. It contains **zero** count-seeding, `statusState`, subscription,
  or web-socket code on `add`/`reserve`/`touch`/`archive`/startup.
- **A separate status projector** (either a distinct process, or a strictly
  isolated goroutine with its **own** read-only DB handle / snapshot and its own
  listener) tails the transition log and maintains the absolute per-repgroup
  counts and serves the web UI / websocket. It can be arbitrarily slow, do full
  history scans, crash and restart — all without touching the manager. On start
  it does the one-time history scan **itself**, off the manager's critical path.
- The manager exposes the log (and a "current incomplete jobs" read) over a
  read-only channel; the projector subscribes.

Variant (smaller): keep it in-process but as a **fully detached subsystem** that
never shares a lock or transaction with the hot path and only ever reads a
consistent snapshot — i.e. the manager writes the log, a projector goroutine
reads it; no seeding/counting ever runs under an operational lock or on the
readiness path. (This is Idea 3 + Idea 1 taken to the limit, with a hard
architectural boundary enforced.)

You could even host the **pre-0.36.5 broadcaster** inside the projector, getting
back the old behaviour with zero operational risk while a better projector is
built.

## Priority alignment

By construction the manager's job-running paths contain no web-UI code, so the
web UI **cannot** slow or block operations — not now, not from any future
web-UI change. This is the only idea that makes the guarantee *architecturally*
rather than by careful coding.

## Trade-offs / risks

- Biggest structural change: a transition log format, a projector, and an IPC /
  read-only access channel; the web-socket command paths (rerun/remove/kill,
  job-details subscription) that are currently bidirectional must be re-homed
  (projector forwards commands to the manager, or those stay on the manager as
  pure operational RPCs).
- The UI is **eventually consistent** with the manager (fine per the brief).
- More moving parts to deploy/monitor (a second process or subsystem).
- Overlaps with `.docs/broadcast/investigate.md`: that doc chose in-process
  absolute-state; this revisits it specifically to enforce isolation from ops.

## Trial checklist (prove it works)

- [ ] **Baseline.** `testing.md` S1/S3/S5 on current HEAD.
- [ ] **Strip the manager.** Behind `WR_PROJECTOR=1`, compile out (or no-op)
  `statusState` seeding, subscription delivery, and per-touch/per-archive web-UI
  work from the manager's operational paths; have transitions append to a simple
  log (bucket or file) instead.
- [ ] **Ops unaffected.** Re-run S3 (real DB): assert cold `add` to
  `ibackup_server_put` is **< 50 ms** and restart is **~1–2 s** (no web-UI code on
  those paths at all). Re-run S1: throughput ≥ baseline. Re-run S5: no archive
  decay.
- [ ] **Stand up the projector.** A separate process/goroutine that opens the DB
  read-only (or tails the log), does its own one-time history count, and serves
  the status websocket. Point the existing Playwright fixtures at it.
- [ ] **UI correctness.** Drive a storm (`testing.md` M1-style) and assert the
  projector's counts converge to ground truth with zero flicker/overcount (reuse
  the `#533` fixtures), and that killing/restarting the projector does not affect
  the manager or jobs at all.
- [ ] **Command paths.** Verify web-UI actions (rerun/remove/kill, job-details
  live view) still work via the projector→manager command channel.
- [ ] **Isolation proof.** Deliberately make the projector do a 3-minute history
  scan while the manager runs a job storm; assert manager throughput and
  responsiveness are completely unaffected (the whole point).
- [ ] **No-regression.** `make test`, `make race`; document the deployment shape.

## Coverage of the full test set (see testing.md acceptance criteria)

| id | criterion | how Idea 4 covers it |
|---|---|---|
| B | startup responsive | **core** — seeding/history projection is off the manager entirely |
| C | false-lost consequence stays fixed | preserved (archive stays on the manager) |
| D | removed-on-refresh stays fixed | the projector owns the seed; it **must** keep complete-only RepGroups |
| E | false-lost CAUSE | **DONE in baseline (F0, a19d390); decoupling further reduces E's load; idea must not regress it** |
| F | status responsive under load | **core (strongest)** — status served by a separate listener/process runner traffic cannot delay |
| G | throughput not regressed | preserved/better (no web-UI work on hot path) |

**Honest scope:** F0 (a19d390) already fixes E; Idea 4 is the **strongest for F**
(status served off the runner path) and further reduces hot-path load. Every trial
re-runs the `harness/` F0 test to confirm no regression.

- [ ] **Full acceptance set (testing.md).** With Idea 4 (+F0 layer 3): the 3 Go
  tests all PASS; `exp_reconnect.sh`/`exp_status_load2.sh` `wr status` bounded
  under 10k conns (isolation proof); a projector doing a 3-min history scan does
  **not** affect manager throughput/`TestReliableFalseLostUnderSaturation`;
  `exp_startup_ab.sh` restart ≤ a few s; `exp1.sh` ≥ v0.36.5. Record numbers.
