# Solving the web status-bar flicker / transient overcount — without regression

## Summary

The flicker and transient overcount described in `issue.md` **can** be solved
with **no regression and no new reliability risk**, because the whole cause lives
in the **browser client's** reconstruction of the status counts from the
`jstateCount` delta feed. The fix is a **purely client-side** rewrite of how
`websocket-handler.js` turns the `from→to` delta stream into the per-RepGroup and
`+all+` bar counts: an **exact, order-independent occupancy reconciliation**.

It touches **zero** server code — no lock, no hot path, no startup scan, no wire
format change — so it cannot regress any of the reliability invariants in
`DEVELOPERS.md`. And it is strictly *more* correct than today's code: as a bonus
it fixes a genuine **permanent divergence** the current client suffers when a
browser connects *while a burst is already in flight* (see "What the current code
actually does", below).

## Root cause (precisely)

The status feed sends **deltas**: `{RepGroup, FromState, ToState, Count}`,
meaning "`Count` jobs moved `FromState`→`ToState`". The client keeps a running
per-state count per RepGroup (and a `+all+` live aggregate) by decrementing
`FromState` and incrementing `ToState`. Two independent facts make that stream
**unordered and unaligned**, and the client's reconstruction is not robust to
either:

1. **Emission is concurrent and therefore unordered.** Every queue transition
   fires the change callback in a *new goroutine* (`queue.changed()` →
   `go queue.changedCb(...)` in `queue/queue.go`). Those goroutines race, so the
   delta for a job's `running→complete` can reach the browser *before* the delta
   for that same job's `ready→running`. For fast `echo` jobs, a job's
   `ready→running` and `running→complete` happen within microseconds, so their
   deltas are adjacent in the stream and trivially reorder. During a 10 000-job
   burst this happens continuously — this is the dominant, continuously-visible
   cause.

2. **The scan-on-connect seed is not aligned with the live stream.** A client
   joins the never-drop `statusCaster` at connection setup, then sends
   `"current"`; the server snapshots the queue (`getJobsCurrent`) and sends the
   seed as `new→state` deltas. The snapshot and the live deltas are two
   unsynchronised views, so a job transitioning during the connect handshake can
   be counted in *both* the seed and a live delta.

The current client tries to cope with (1)/(2) with an ad-hoc `ignore` map: when a
`from` decrement would go negative it clamps to 0 and records an amount to
"ignore" from a future increment of that state. But it still applies the `to`
**increment in full immediately** even when the `from` side was unbacked — so the
job is momentarily counted in *both* its old and new state (**overcount**), or a
`from` decrement lands with no matching backing and the bar **dips/zeroes**. The
`ignore` consumption is also all-or-nothing (`ignore[to] >= count`), so a batched
seed larger than a unit `ignore` is not reconciled at all.

### What the current code actually does (measured)

Driving the **real** `websocket-handler.js` count logic with a faithful
out-of-order stream (harness in `jobqueue/testdata/status-count-reconcile/`):

| Scenario | worst RepGroup total error | worst `+all+` overcount | converges? |
|---|---|---|---|
| connected client, burst, reordered | up to ±8 transient | up to +2 transient | yes (self-heals) |
| **connect *during* a burst** (seed race) | up to ±16, **stuck** | +16 | **NO — 300/300 trials end permanently wrong (e.g. 1508 total / 1507 complete for 1500 jobs)** |

So the issue's "cosmetic and self-correcting" description holds for a client that
was *already connected*, but a client that connects *mid-burst* can be left
**permanently wrong** until it reconnects. The chosen fix eliminates both.

## The fix — exact, order-independent occupancy reconciliation (client-side)

Model the counts as a tiny **flow network**. Internally (per tracker) keep:

- `occ[state]` — how many jobs are currently in each state (always ≥ 0), for
  **every** state including the terminal ones;
- `pending[from][to]` — observed exits we could **not** apply yet because we had
  not seen those jobs enter `from` (the out-of-order / pre-seed case).

Applying one delta `{from→to: Count}`:

- **Creation / re-entry** (`from` is `new`, or a state this tracker does not
  hold): the jobs simply **enter** `to` — `occ[to] += Count`; then `settle(to)`.
- **Normal exit** (`from`≠`to`): record `pending[from][to] += Count`, then
  `settle(from)`. Crucially the `to` side is **not** credited until the jobs are
  actually shown to be in `from`; an unbacked exit waits in `pending`.
- `from`==`to` (e.g. `reserved`↔`running`, which map to the same bar bucket): a
  no-op.

`settle(node)` forwards a node's pending exits as fast as occupancy allows,
cascading into destination nodes (a forwarded job may itself have a pending
onward exit). It is **iterative with an explicit work queue — never recursive** —
so a transition **cycle** (a rerun's `complete→ready→running→complete`) cannot
re-enter `settle` for a node whose occupancy is mid-update and corrupt it. Each
move strictly reduces total pending, so it always terminates.

Finally, mirror the displayed buckets from `occ` onto the Knockout observables,
writing only genuine changes. For `+all+` only the **live** states are mirrored;
`complete`/`deleted` are tracked internally but never shown, so a completing job
correctly leaves the live bar and a later **rerun** re-adds it as a fresh
`new→ready` creation (which is how the server actually emits a rerun) — the live
count sheds the completed job and regains it exactly, never double-counted.

(The model is also robust to a hypothetical `complete→ready` transition delta —
`settle` treats it as an ordinary cyclic edge — which is why the regression
harness stresses a full `complete→ready→running→complete` cycle: it is a
worst-case exercise of `settle`'s re-entrancy safety, not the literal wire
sequence.)

### Why this is correct

- **Order-independent.** Occupancy is the net of applied flow; pending holds
  anything not yet backed and is drained deterministically. Any permutation of a
  valid, lossless delta stream yields the same occupancy once drained.
- **Always coherent.** `occ` is never negative and a `to` state is never credited
  a job whose presence in `from` has not been observed, so the summed bar never
  transiently exceeds the truth (**no overcount**) and never loses a backed job
  (**no dip/zero**).
- **Always convergent**, including through rerun cycles and a mid-burst connect —
  the residual permanent-divergence bug of the current client is gone.
- **Bounded.** `pending` only holds not-yet-backed exits; a lossless feed always
  supplies the backing, so it drains to empty. It is cleared on reconnect.

### Measured result (same harnesses)

Every scenario — connected-client burst, connect-mid-burst seed race, and a
mixed workload (complete / bury / delete / lost / **rerun**) — at reorder windows
from 0 to 60: **RepGroup total error 0, `+all+` overcount 0, negatives 0, and
100 % convergence to the exact final distribution.**

## Why the other candidate solutions were rejected

1. **Re-introduce a server-side absolute counter (revert of the #533 revert).**
   This is exactly what `.docs/reliable2/` removed and what `DEVELOPERS.md`
   rules 2 and 6 forbid: it put a server-wide exclusive lock on the
   per-transition hot path (tanking dispatch throughput) and cold-scanned all
   completed-job history at startup (minutes-long restarts). Any accurate
   server-side counter re-creates that pressure. Rejected outright.

2. **Serialise/order the server's delta emission** (drain the
   `go queue.changedCb(...)` callbacks through one ordered worker). This attacks
   cause (1) at source, but it changes the concurrency model of the
   reliability-critical queue/transition path — precisely what `DEVELOPERS.md`
   rule 9 warns "exposes latent bugs" and requires `-race` + real-scale proof
   for. It also does nothing for the seed-race (cause 2). Too much risk to the
   critical path for a cosmetic web fix.

3. **Server watermark + client replay.** Tag deltas with a sequence number and
   have the seed carry a watermark so the client discards already-seeded deltas.
   To be correct the snapshot must be a consistent cut of the delta stream, but
   emission is asynchronous (goroutine per callback) and the snapshot reads queue
   state — aligning them needs a lock spanning the snapshot *and* emission, i.e.
   holding `queue.mutex` across the full `getJobsCurrent` scan (violates rule 1)
   or the banned counter. Rejected.

4. **Send the seed as one atomic snapshot object** (a small server + wire
   change) to remove the incremental-seed transient seen only on a mid-burst
   connect. Unnecessary: with the client fix the only residual is the seed
   arriving as several `new→state` messages that briefly sum below the total
   *while still loading*, which the existing 350 ms Knockout rate-limit coalesces
   into a single paint (never rendered), and which the current code exhibits too.
   Not worth a server/wire change and its risk.

5. **Client-side rendering debounce only.** Hiding repaints does not fix the
   reconstructed count, so the overcount (and the mid-burst permanent divergence)
   would still surface. Incomplete.

6. **Patch the existing `ignore` map** (partial consumption, defer the `to`
   increment). Whack-a-mole: it does not give a principled guarantee against
   out-of-order emission in general and still diverges on a mid-burst connect.
   Replacing it with the occupancy model is simpler to reason about and provably
   correct.

The chosen client-side reconciliation is the only option that fixes **all** the
observed artefacts (flicker, overcount, and the mid-burst permanent divergence)
while touching **no** server-side reliability code.

## Scope of change

- `jobqueue/static/js/wr/websocket-handler.js` — replace the `ignore`-based
  delta application (`handleStateChangeMessage`, `applyIgnoredToState`,
  `applyFromDelta`, `terminalOnAll`) with the occupancy model
  (`reconModel`/`settle`/`syncDisplay`); clear the per-tracker model on reconnect
  in `resetLiveCounts`.
- No other client file changes; `inflight-tracking.js` already renders smoothly
  from coherent counts (its ordered `repgroup-bar-flicker` guard stays green).
- **No server-side change.**

## Regression guards (added)

- `jobqueue/testdata/status-count-reconcile/screenshot.mjs` — a browser fixture
  like `repgroup-bar-flicker` but driving an **out-of-order** storm plus a
  mid-burst connect, asserting the rendered bar never collapses **and** the
  reconstructed counts never overcount and converge exactly. It fails on the
  pre-fix handler and passes after. Wired into `make browser-test`.
- A Node contract test (in `serverWebI_test.go`, same `vm` pattern as
  `TestStatusPageLivePushUpdateBehaviour`) that drives the real
  `handleStateChangeMessage` with reordered streams and asserts exact,
  order-independent convergence with no transient overcount/dip.
- The reusable harnesses live under `jobqueue/testdata/status-count-reconcile/`
  and are runnable via `developers/wrdev.sh flicker-check` (see `DEVELOPERS.md`).
