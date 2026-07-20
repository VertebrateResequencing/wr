# Idea 3 — Decouple *liveness* from RPC-processing latency

**Class:** mid-level semantics change to lost-detection (no transport rewrite).

## Problem recap

wr equates "touch processed within TTR" with "runner alive". Under load the
manager processes a healthy runner's touch *late*, so an alive job is declared
`Lost` and flipped out of `SubQueueRun`; its subsequent successful archive is
then rejected (M1/M2), and the non-complete removal renders `deleted` (M4). F0
(`#550`) recorded touch *arrival* before queue processing and made `ttrCallback`
tolerant if `contactedWithin(TTR)` — but the *touch message itself* is
backlogged in the single reader under real saturation, so arrival isn't even
recorded in time.

## The idea

Make liveness independent of how fast the manager can *process* RPCs:

1. **Record contact at the transport edge.** Stamp the last-contact time the
   instant a runner's bytes arrive on the socket (in the read loop, before any
   dispatch/lock), keyed by connection→job, so a backlog can't make an alive
   runner look silent. (Extends F0 from `handleTouch` down to the reader.)
2. **Lease renewal, not RPC round-trips.** Give each running job a **lease**
   the runner renews with a tiny, fixed-size heartbeat that the manager can
   ingest cheaply and out-of-band of the heavy request path (a dedicated
   heartbeat channel/port, or UDP-style fire-and-forget). TTR loss fires only
   when the lease genuinely lapses.
3. **Cross-check with the scheduler's truth.** Before confirming a job dead,
   ask the scheduler whether the runner is still alive — for LSF that's the
   `bjobs` state wr already parses; a job whose LSF array element is still `RUN`
   is not lost, no matter how backlogged the manager is. (`confirmOrReleaseLost
   Job` already has a "confirm dead" hook; make it authoritative and cheap via
   the scheduler.)
4. **Never lose while running.** A job whose lease is alive OR whose scheduler
   state is `RUN` is never moved out of `SubQueueRun`, so its archive is never
   rejected on liveness grounds.

## Why it solves the symptoms

- M3/M1/M2: alive jobs stay in `Run`; their successful archives are accepted;
  churn disappears at the source (false loss) rather than being mopped up after.
- M4: jobs aren't removed in a non-complete state due to false loss → no
  `deleted` broadcast.
- M5 (status stall) is **not** directly fixed here (the heavy read path still
  shares the reader) — pair with Idea 2's separate status listener.

**v0.36.5 alignment:** v0.36.5 rarely false-lost because its cheap hot path kept
up (liveness was accurate as a side effect). This idea makes non-loss of alive
jobs *explicit and robust* regardless of manager load — a stronger guarantee
than v0.36.5's incidental one, so it holds even at scales v0.36.5 never faced.

## Risks / tradeoffs

- A separate heartbeat channel is new transport surface (auth, teardown).
- Scheduler cross-check adds `bjobs` load; must be cached/batched (wr already
  batches `bjobs`); cloud/local schedulers need their own liveness source or a
  fallback to the lease.
- Changes when-jobs-go-lost semantics; must still detect *genuinely* dead
  runners within ~1 TTR (keep `TestLostDetectionSilentRunner` green).

## Trial checklist (prove it works)

- [ ] Land `harness/reliable2_churn_test.go`; confirm current-code false-loss +
      archive-rejection under saturation (baseline M2/M3).
- [ ] Temp-implement edge-stamped contact (record in the read loop) and re-run:
      measure reduction in false-loss with no other change.
- [ ] Temp-add the LSF `RUN`-state cross-check in `confirmOrReleaseLostJob`
      (mock the scheduler in-process; on the farm use real `bjobs`); assert an
      alive job is never confirmed dead under saturation.
- [ ] (Optional) prototype a lightweight lease heartbeat channel; measure that
      lease renewal is unaffected by heavy `wr status`/archive load.
- [ ] Assert: M2=0, M1≈100%, M3 guards green, and a *genuinely* silent runner
      still goes Lost within ~1 TTR (`TestLostDetectionSilentRunner`).
- [ ] Pair with Idea 2's status listener for M5; M6/M7 unchanged.
- [ ] Farm: `portal_builder` at scale — no alive job marked lost; successful
      commands complete.

## Trial results

_(to be filled during trialling)_
