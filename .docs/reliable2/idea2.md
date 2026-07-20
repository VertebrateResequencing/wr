# Idea 2 — Decouple the runner hot path from the single-reader socket

**Class:** mid-level transport change (concurrency, no protocol rewrite).

## Problem recap

Every client RPC — runner `reserve`/`started`/`touch`/`archive`/`release`
**and** operator `wr status` / web-UI bulk reads — is read and dispatched by a
**single** goroutine on one mangos socket
(`serveClients → receiveClientMessage`). With thousands of runners the reader
saturates: touches/archives are processed late (→ false TTR loss → discarded
successful work, M1/M2/M4) and a heavy `wr status` queues behind the fleet
(M3/M5). This is the shared root of all three symptoms.

## The idea

Stop serialising the hot path. Two composable moves:

1. **Concurrent intake.** Replace the single `RecvMsg` loop with a bounded pool
   of reader/dispatch workers (or mangos' own concurrent surveyor/respondent
   pattern), so N runner messages are decoded and routed in parallel. All
   *state mutation* still funnels through the existing per-queue locks (so no
   new correctness surface), but *decode + dispatch + the read-mostly parts*
   (touch contact recording, status snapshots) parallelise. Touch/archive
   latency then stops scaling with fleet size.
2. **Class-of-service separation.** Give bulk/operator reads (`wr status`
   `AllItems`, web-UI catch-up) a **separate listener/port** (or a separate
   respondent socket) from runner lifecycle RPCs, so a slow `AllItems` can
   never delay a touch/archive. (v1 called the minimal version "Idea 4-lite".)

Because false-loss and archive-rejection are *caused* by processing latency,
removing the latency largely removes the churn without changing the
lost/archive semantics at all.

## Why it solves the symptoms

- M2/M1: touches and archives are processed promptly → far fewer TTR flips →
  far fewer rejected successful archives. (Best combined with Idea 1's
  accept-late-success as a correctness backstop for the residual tail.)
- M3/M5: `wr status` no longer waits behind the runner fleet.
- M4: fewer non-complete removals → fewer `deleted` broadcasts (Idea 1's
  broadcast guard still recommended).

**v0.36.5 alignment:** v0.36.5's `jtouch` was a tiny `q.Touch` + flag clear
(no snapshot/decompress/subscription), so the manager kept up under the same
load and jobs stayed alive in `Run`. `#503`/`#533` added the per-message/per-
touch machinery this idea makes cheap/parallel again — restoring the "manager
keeps up so nothing diverges" property that made v0.36.5 immune.

## Risks / tradeoffs

- Concurrency on the intake path is the classic wr foot-gun; must preserve the
  documented lock order (`queue.mutex → statusState.mu → subscription locks`)
  and not introduce data races (run everything under `-race`).
- mangos socket semantics (req/rep vs respondent) constrain how much
  parallelism is possible without a transport change; may need to move to a
  respondent/streaming socket.
- Bigger blast radius than Idea 1; needs careful load testing.

## Trial checklist (prove it works)

- [ ] Land the shared `harness/reliable2_churn_test.go` and a
      status-under-load probe (heavy `wr status` latency vs N connected
      loadrunner `hold` connections) — capture **baseline** numbers on current
      code (repro the ~15× status degradation and the archive rejections).
- [ ] Temp-implement a worker-pool intake (e.g. K decoders feeding a dispatch
      channel; state mutations still behind existing locks). Keep it behind an
      env flag so A/B is easy.
- [ ] Re-run under `-race`: assert no new races; archive-rejection count drops
      sharply (M2), forward progress rises (M1).
- [ ] Temp-add a separate status listener (or route `-o counts`/AllItems to a
      second respondent); measure heavy `wr status` latency vs connections —
      target flat/bounded, not 15× (M5).
- [ ] Keep M3 guards green; M6 startup unchanged; M7 throughput not regressed
      (v1 `exp_drive_ab`-style churn A/B).
- [ ] Farm: `portal_builder` run at a few thousand runners — `wr status`
      stays responsive while runners are active; commands that succeed reach
      `complete`.

## Trial results (2026-07-20)

**Root strongly corroborated; fix NOT yet spiked (blocked on reproduction).**
Two farm runs of the real `portal_builder` workload against current code
established a clear **saturation threshold**:
- ~3–4k concurrent runners (second run): **healthy** — jobs complete, ~0
  archive rejections (the single reject captured was a benign duplicate archive
  of an already-`complete` job: `itemState=delay jobState=complete exitcode=0`).
- ~6–7k concurrent runners (first run): **catastrophic** — 19,394
  `jarchive: bad job`, `complete`≈0, `wr manager stop` hung.

This threshold behaviour is exactly Idea 2's thesis: the failure is the
single-reader hot path falling behind above a runner-count threshold. v0.36.5's
cheaper hot path had a higher threshold (immune at this workload's concurrency)
— see testing.md "Why v0.36.5 was immune". So Idea 2 (make the reader
concurrent / separate bulk reads / cheapen per-message work → raise the
threshold) targets the root directly.

**Why not spiked:** proving it requires re-running at ~6–7k concurrent, but my
LSF fair-share was depleted by the repeated 37k-job runs (later runs only got
~3–4k slots — below threshold), so the churn could not be re-triggered on
demand this session; and a faithful concurrent-reader change is a substantial,
race-prone transport edit that shouldn't be spiked without a reliable oracle.
**Recommended before committing:** build a controllable saturation oracle —
either regain farm fair-share for a ~6–7k run, or an in-process harness that
lowers the threshold deterministically (e.g. a temp per-message processing
delay simulating the heavier post-#503 hot path) + the RELDIAG archive-reject
instrumentation — to (a) pin the exact discard sub-cases and (b) A/B the
concurrent-reader / separate-status-listener change. This is also the only idea
that addresses the status-stall (M5) and the *wasted re-run* (not just its
correctness), so it is the leading candidate for the throughput half of the
fix.
