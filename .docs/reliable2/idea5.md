# Idea 5 — Architectural overhaul: streaming transport + log-structured, idempotent job state

**Class:** full re-architecture of the manager↔runner interface (biggest hammer).

## Problem recap

All symptoms trace to one design choice: a **single request/response mangos
socket read by one goroutine**, with job state mutated in place and correctness
defined by "did the archive RPC arrive while the manager still agreed the job
was running". That is inherently fragile at fleet scale (M1–M5). Ideas 1–4
patch or decouple pieces of it; Idea 5 replaces the substrate so the fragility
can't exist.

## The idea

Re-architect the client interface around three principles:

1. **Streaming, horizontally-parallel transport.** Replace the single
   req/rep socket with a streaming RPC (e.g. gRPC bidi streams) or a
   respondent pool, so each runner has its own long-lived stream and the
   manager ingests touches/archives/reserves concurrently across a worker pool.
   Runner lifecycle traffic and operator/bulk reads are physically separate
   services. Touch/archive/status latency no longer couples to fleet size.
2. **Log-structured, idempotent state.** Express every job-state change as an
   append to a per-job event log (reserved→started→heartbeat→exited→archived),
   each event carrying (jobKey, attempt-epoch, monotonic seq). Current state is
   a fold over the log. Because events are idempotent and epoch-guarded, a
   duplicate/late/re-delivered `exited(success)` is absorbed, and a job's
   outcome is whatever the log says — never lost, never double-counted. This
   subsumes Idea 4's durability and Idea 1's epoch guard as first-class
   primitives.
3. **Liveness as a stream property.** A runner's stream being open *is* its
   lease (plus scheduler cross-check as in Idea 3); TTR-by-processing-latency
   disappears. Status/web-UI are projections built from the log by dedicated
   readers, so they never contend with ingestion (subsumes Idea 2's separation
   and v1's projector idea).

The manager becomes: concurrent stream ingest → append idempotent events →
fold to live state + projections. Correctness is defined by the log, not by
RPC timing.

## Why it solves the symptoms

- M1/M2: a succeeded command's `exited(0)` event is durable and idempotent →
  never discarded, never re-run.
- M4: `complete` is a fold of the log; a succeeded job cannot be projected
  `deleted`.
- M3: liveness = stream + scheduler truth, immune to processing latency.
- M5: status/web-UI are independent projections; runner load can't stall them.
- M6: the log + projections make startup a fast fold/replay of the tail (build
  on `#550`'s counters as the projection checkpoint).

## Risks / tradeoffs

- **Largest** change by far: new transport, new persistence model, migration of
  the existing BoltDB state, backward-compat for existing clients/runners during
  rollout. High cost, long timeline, big test burden.
- Risk of reintroducing bugs the current code already handles correctly
  (recovery, limit groups, dependencies, mounts, cloud).
- Only justified if Ideas 1–4 prove insufficient at true production scale;
  otherwise it's over-engineering (the same verdict v1 reached about its full
  projector). Best treated as the **north star** that Ideas 1–4 incrementally
  approximate (epoch → idempotent events; durable outcome → log; separate
  status → projections; concurrent intake → streaming).

## Trial checklist (prove it works — as a bounded prototype, not a rewrite)

- [ ] Land `harness/reliable2_churn_test.go` as the correctness oracle.
- [ ] Prototype the **event-log core only** in a temp package: append-only
      per-job events with (key, epoch, seq); a fold to `{running, complete,
      lost}`; assert idempotent/duplicate/late `exited(0)` always folds to
      exactly one `complete` (M1/M2/M4 at the model level).
- [ ] Prototype a **concurrent stream ingest** shim in front of the existing
      server (or a gRPC bidi PoC) with a loadrunner-style driver; measure
      touch/archive/status latency vs connection count — target flat (M2/M3/M5)
      vs the current ~15× status degradation.
- [ ] Show a projection (status counts / web-UI states) built off the log that
      never blocks on ingest (M5), and that a succeeded job projects `complete`
      never `deleted` (M4).
- [ ] Estimate migration surface (Bolt→log, client protocol compat) and write
      a go/no-go: does it beat Idea 1+2(+3/4) enough to justify the cost?
- [ ] If pursued: incremental delivery mapping each of Ideas 1→4 onto a piece
      of this target so value lands early and risk is staged.

## Trial results (2026-07-20)

**Not spiked (full re-architecture); reasoned assessment only.** The saturation
threshold finding (idea2 Trial results) confirms the substrate diagnosis — a
single serial reader + in-place strict state machine that diverges under load —
which this idea replaces wholesale. But nothing observed this session *requires*
the overhaul: the correctness half is already fixed by a proven ~15-line change
(Idea 1), and the throughput half is very likely reachable by making the
existing reader concurrent / offloading bulk reads (Idea 2) rather than
swapping transport + persistence. **Verdict:** keep as the north star that
Ideas 1–4 incrementally approximate (epoch→idempotent events, durable
outcome→log, separate status→projection, concurrent intake→streaming); pursue
only if a staged 1+2(+3/4) rollout demonstrably cannot meet the metrics at
production scale. Its own checklist deliberately scopes the *trial* to a bounded
event-log/stream-ingest prototype + a go/no-go vs the cheaper combination —
that go/no-go is the right first step, not a rewrite, and was out of scope for a
lean sequential session.
