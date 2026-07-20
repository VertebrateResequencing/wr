# Choice: how to go forward (why this isn't a single-candidate spec yet)

I trialled the five ideas (notes in each `ideaN.md` "Trial results", shared
harness + evidence in `testing.md`). The result is **not** one clear winner to
hand straight to `/spec-writer`; it's a **two-part decision**, one part clear
and proven, the other a genuine human/architecture choice that also needs one
more piece of reproduction before committing. Hence this `choice.md` rather than
`prompt.md`.

## TL;DR

- The failures split cleanly into **correctness** (jobs "lost" / web-UI
  "deleted") and **throughput/responsiveness** (`wr status` stall, and the
  *wasted re-runs* that make a run never finish).
- **Correctness half — CLEAR & PROVEN.** Idea 1 ("a completed success from the
  runner that held the job is never discarded") plus the `changeCallbackToState`
  "complete-wins" guard. Spiked in-process: it flips the deterministic churn
  oracle from *work-discarded* to `complete:1`. Small, low-risk, internal-only,
  restores v0.36.5's semantics. **Do this regardless of what's chosen below.**
- **Throughput/responsiveness half — HUMAN CHOICE + needs one more repro.** The
  root is the single-reader hot path saturating above a runner-count threshold
  (Idea 2 territory), but the *form* of that fix is an architecture decision,
  it's unproven at scale this session, and the exact high-saturation discard
  sub-cases aren't fully pinned. Pick an approach and validate it against a
  reliable saturation oracle first.

## What the trials established (evidence)

1. **The churn is saturation-threshold-driven.** Same real `portal_builder`
   workload, current code: **healthy at ~3–4k concurrent runners** (jobs
   complete, ~0 archive rejections), **catastrophic at ~6–7k** (19,394
   `jarchive: bad job`, `complete`≈0, `wr manager stop` hung). The threshold is
   the single serial reader (`serveClients → receiveClientMessage`) falling
   behind.
2. **v0.36.5 was immune** because its hot path was cheaper (no `#503` per-touch
   snapshot machinery) → higher threshold, and its completion was lenient
   (accepts an owner's success on an in-`Run` job, no strict state gate) with no
   automatic `deleted` projection. The post-0.36.5 accuracy work lowered the
   threshold **and** made a crossing turn successful work into a rejected
   archive + a `deleted` broadcast. (See `testing.md`.)
3. **Idea 1 works** (spiked): completed-success-wins → the oracle records
   `complete:1` instead of discarding A's work.
4. **Neither the runner nor the manager releases an *alive* job** (runner keeps
   retrying touches; `confirmJobDeadAndKill` re-checks and only re-runs a
   confirmed-dead job; current code never sets `job.State=Lost`). So the exact
   path that moves a successful job out of `Run` at ~6–7k concurrency — enabling
   the discard — was **not fully pinned** (the diagnostic farm run landed below
   the threshold; LSF fair-share was depleted by repeated 37k-job runs, so the
   churn couldn't be re-triggered on demand).

## The decision

### Part A — correctness (recommended, clear): Idea 1 + deleted-broadcast fix

Implement now; it is the proven fix for "lost" and "deleted", is internal-only,
and is independent of the throughput approach. Production form needs the
**attempt-epoch** so a genuine double-run resolves to exactly one winner (the
spike used the simplest guard). This alone makes every successfully-executed
command end `complete` and never render `deleted`.

### Part B — throughput + status-stall (human choice)

The stall (M5) and the *wasted re-runs* need the hot path to keep up at
production runner counts. Options, cheapest→heaviest:

| Option | What | Pros | Cons |
|---|---|---|---|
| **B1: Idea 3 (edge-stamp contact)** | record runner contact at the transport read, before dispatch | tiny, attacks the *cause* (don't lose alive jobs); no new transport | doesn't fix `wr status` stall; unproven it alone prevents the discard |
| **B2: Idea 2 (concurrent reader + separate status listener)** | parallelise message intake; move bulk `wr status`/web reads off the runner socket | directly raises the threshold *and* fixes M5; no protocol change | concurrency on intake is the classic wr foot-gun; needs careful `-race` + scale proof |
| **B3: Idea 4 (durable outcome + reconcile)** | runner writes its outcome durably; manager reconciles idempotently | bulletproof correctness even across manager restart; subsumes Idea 1 | new durable-write surface + reconciliation; heavier than evidence demands |
| **B4: Idea 5 (streaming + log-structured state)** | re-architect transport + state | removes the fragility class entirely; scales | largest cost/risk; only if B1/B2 prove insufficient |

These aren't mutually exclusive (B1+B2 compose well). The choice is genuinely
the maintainer's: it trades implementation risk/scope against how much headroom
and future-proofing is wanted.

## Validate before committing to Part B

The one gap that makes Part B a "choose + prove" rather than "spec it now":
build a **reliable saturation oracle** and use it to (a) pin the exact
high-saturation discard sub-cases and (b) A/B the chosen throughput fix. Either:
- regain LSF fair-share and re-run `portal_builder` at ~6–7k concurrent with the
  `RELDIAG` archive-reject instrumentation (temp patch in `harness/`), or
- an in-process harness that lowers the threshold deterministically (temp
  per-message processing delay on the reader to simulate the heavier post-#503
  hot path) + many connections + runners holding jobs past a short TTR, with the
  same instrumentation.

## Recommended default (if a single path is wanted)

**Idea 1 (Part A) now**, then **B1 (edge-stamped contact) + B2's separate status
listener** as the low-risk throughput/responsiveness step, adding B2's
concurrent intake only if the oracle shows the threshold still bites; reserve
B3/B4. This restores all three of v0.36.5's lost properties (owner's success
wins; alive jobs stay alive; status off the runner hot path) with the least new
surface. If the maintainer confirms this combination (and the Part-B scope), it
becomes the `/spec-writer` input — a `prompt.md` can then be written from it.

## Why not `prompt.md` now

A spec now would either (a) under-scope to just Part A (leaving the status-stall
and wasted-re-runs unfixed — not holistic), or (b) over-commit to a specific
Part-B architecture that is unproven at this session's scale and whose form is a
maintainer decision. Better to lock Part A, let the maintainer pick Part B, and
prove it against the oracle first.
