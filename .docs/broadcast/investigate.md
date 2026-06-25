# Broadcast system investigation

## Why this document exists

Commit `ac24a01a08622832793275f08c632d8acd317c19` ("Add job subscriptions
(#503)") replaced the third-party broadcast library `github.com/grafov/bcast`
with a hand-rolled in-process `caster` (`jobqueue/server.go`). Since then the
status web UI has suffered a string of related defects (see `.docs/bugfixes`):
stale counts (`260625-5`), disappearing completed repgroups and steady-state
"twitch" (`260625-6`), and finally a regression where the UI "flickers so fast
it looks like it's not there" while the per-repgroup total "keeps rising above
the total number of jobs actually added" (`260625-7`). Five separate fix
attempts in `260625-7` were all rejected at review, each one bolting more
sequence-number / snapshot-cutoff / generation machinery onto the same design
and uncovering a new race (culminating in a `sfsmutex`→`queue` vs `queue`→
`sfsmutex` lock-order inversion). The bug is currently **unresolved**.

This document does not assume the fix is "more machinery". It steps back, frames
the design space, evaluates whether a reliable widely-used third-party library
should replace the home-grown system, and defines falsifiable investigations —
each carried out in a throwaway git worktree by a subagent — to pick the scheme
that is provably better than the current implementation **and** better than the
other candidate schemes. Findings and ticked checkboxes are recorded inline. A
recommendation is given at the end.

## Architecture as it stands today

Server (`jobqueue/server.go`, `jobqueue/serverWebI.go`):

- `caster` is an in-process pub/sub. Each websocket client `Join()`s and gets a
  `casterMember` with a buffered channel `In` of capacity **1**.
- The queue change callback (`SetChangedCallback`) fires on every state
  transition and calls `statusCaster.Send(&jstateCount{FromState, ToState,
  Count})` — **once for `+all+`, once per affected RepGroup, plus `lost`
  variants**. So a single user action fans out into N messages.
- `Send` → per member → `sendOrReplace`. If the member's 1-slot buffer is full,
  `replacePending(casterOverflowValue(val))` **discards the real delta and
  substitutes a `StatusResync` marker** (`jobqueue/server.go:577-622`).
- On `{Request:"current"}` the server assigns a monotonic `SnapshotID`, walks
  all current + completed jobs, sends absolute per-RepGroup counts tagged with
  that id, then a `SnapshotDone`.

Client (`jobqueue/static/js/wr/websocket-handler.js`, Knockout view model):

- Deltas are applied arithmetically: `from(from()-count); to(to()+count)`.
- An `ignore` map compensates for transitions that would drive a count negative
  (out-of-order / lost deltas).
- Snapshots are staged by `SnapshotID` then applied wholesale on `SnapshotDone`;
  `pruneEmptyLiveRepGroups` + `restoreCompletedRepGroupsMissingFromSnapshot`
  then add/remove rows.
- A `StatusResync` triggers a fresh `current` request.
- Counts are Knockout observables with a 350 ms `rateLimit`.

The newer **job-subscription** path (`jobqueue/subscription.go`,
`setupStatusSubscriptionUpdateListener`) is a useful contrast: it accumulates
per-subscription updates **server-side keyed by job**, then delivers a batch
after a hold time. It is loss-tolerant by construction (last write per job
wins), unlike the status-count path.

## The two independent axes of the problem

Every candidate scheme is a point in a 2-D space. Naming the axes is the whole
point of this investigation, because four of the five failed fixes only moved
along Axis 2 while leaving Axis 1 untouched.

- **Axis 1 — payload semantics.** *Deltas* (`+n`/`-n`) are non-idempotent and
  require exactly-once, in-order delivery; a single dropped or duplicated
  message corrupts the count until a full resync. *Absolute state* (send the
  current count) is idempotent: a dropped intermediate is harmless because the
  next message overwrites it, and duplicates are no-ops.
- **Axis 2 — transport reliability.** *Best-effort lossy* (today's 1-slot
  coalescing buffer that converts overflow into a resync) vs *reliable /
  recoverable* (offset + replay, SSE `Last-Event-ID`, an ack'd queue, or a
  library that guarantees this).

Key claim to be proved or refuted by the investigations: **moving to absolute
state (Axis 1) removes the entire bug class on its own**, because coalescing
idempotent state is safe; transport reliability (Axis 2) then becomes a
bandwidth optimisation, not a correctness requirement. The corollary claim:
swapping only the transport (Axis 2) while keeping deltas — which is what a
naive "go back to a 3rd-party broadcast lib" does — will **not** fix the bug.

## Shared scoreboard (how every scheme is judged)

Each scheme is measured against the current `master` implementation on the same
harness (A/B on one machine, to remove cross-machine variance). Metrics:

- **M1 Correctness under storm.** Drive a realistic transition storm
  (10k–15k jobs new→ready→running→complete as fast as the queue emits, plus a
  bulk remove) and assert: final per-RepGroup and `+all+` counts equal ground
  truth; **no visible row ever drops to 0 while jobs still exist** (flicker);
  **no count ever exceeds jobs actually added** (overcount).
- **M2 Steady-state silence.** With no server-side state change for ≥10 s, the
  UI counts must not change at all and no `current`/resync must be issued.
- **M3 Smoothness & latency.** p50/p95 from server state change to UI showing
  it under load; UI must not thrash (bounded update rate, monotone-looking
  progress).
- **M4 Server cost & non-blocking.** Messages/s and bytes/s on the wire, CPU,
  allocs under the storm; the queue-mutation path (`SetChangedCallback`) must
  never block on a slow/stuck client.
- **M5 Loss self-healing.** Inject transport drops/coalescing; final state must
  still converge correctly without a resync that races the live stream.
- **M6 Reconnect recovery.** Drop the socket / `kill -9` + restart the manager
  mid-storm; the UI must converge to correct state with no stuck or stale
  counts and no warning spam.
- **M7 Concurrency safety.** `go test -race` clean; **must not reintroduce**
  the `sfsmutex`↔`queue` lock-order inversion from `260625-7` attempt 5; no
  deadlock with the TTR path; immutable snapshots (no mutable item pointers
  escaping a lock).
- **M8 Complexity & dependencies.** LOC delta; number of moving parts removed
  (ideally the `ignore` map, resync, snapshot staging all disappear); new
  dependency weight, license, and maintenance health.

## Harness available to every investigation

- **Frontend, deterministic:** the Playwright fixtures under
  `jobqueue/testdata/<scenario>/` serve the real `jobqueue/static`, inject a
  fake `WebSocket` via `addInitScript`, run the real handler, and sample the
  Knockout view model. This drives a controlled message storm with **no Go
  server needed** and is the primary tool for M1/M2/M3/M5. Reuse the cached
  Playwright package and Chromium from the main worktree by exporting
  `PLAYWRIGHT_PACKAGE_DIR=/home/ubuntu/wr/.tmp/agent/playwright/node_modules/playwright`
  and `PLAYWRIGHT_BROWSERS_PATH=/home/ubuntu/wr/.tmp/agent/ms-playwright`.
- **Server, real:** Go tests in `jobqueue/` against a real `Server` for M4/M6/M7
  and for end-to-end correctness. Use focused `go test -run` (and `-race`),
  never the full `make test`, to keep the shared box responsive.
- Put scratch under the worktree's `.tmp/agent/`. Never write outside the repo.

---

## §0 Baseline — probe the current implementation's weaknesses

**Goal.** Establish the bar: reproduce flicker + overcount in the *current
committed* code, and prove the precise mechanisms so we can attribute them to
Axis 1 vs Axis 2 rather than to incidental bugs.

**Checklist**

- [ ] Build a *storm* reproduction (not the narrow staged fixtures): real
  high-rate transition stream through the real `websocket-handler.js`, sampling
  the view model at high frequency. Capture a trace of (count vs ground truth)
  over time.
- [ ] Demonstrate **flicker**: a visible repgroup/`+all+` count dropping to 0 (or
  far below truth) while jobs still exist, triggered by resync↔delta racing.
- [ ] Demonstrate **overcount**: a repgroup `complete`/total exceeding jobs
  added, by showing a completion counted in a snapshot *and* re-added by a later
  additive delta (no consistent cut between snapshot and delta stream).
- [ ] Quantify **delta loss**: under the storm, count how often `sendOrReplace`
  overflows and converts a real delta into a `StatusResync` (instrument
  `casterOverflowValue`).
- [ ] Quantify **message fan-out**: messages emitted per user action vs per
  transition; bytes/s under the storm (M4 baseline).
- [ ] Confirm the **resync race** has no consistent cut: snapshot view of the
  queue vs deltas emitted concurrently can neither be cleanly before nor after
  the snapshot.
- [ ] Reproduce/again confirm the **lock-order inversion** risk space from
  `260625-7` attempt 5 (`sfsmutex`↔`queue`) so candidate schemes can be checked
  against it.
- [ ] Record baseline M1–M8 numbers for the A/B comparisons.

**Findings**

_(to be filled by the baseline subagent)_

---

## §1 Scheme A — Idempotent absolute per-RepGroup state, server-coalesced (native, minimal dependency)

**Idea.** Delete deltas. The server keeps the authoritative
`map[RepGroup]stateCounts` (it already can derive these). On any change it marks
the affected repgroup(s) dirty; a single per-client sender goroutine coalesces
dirty repgroups and sends **absolute** count objects (last-write-wins),
throttled (e.g. ≤20 Hz). The client replaces a repgroup's counts wholesale
(idempotent). The 1-slot coalescing buffer becomes *correct* because dropping an
intermediate absolute snapshot just skips to the newest state. The `ignore` map,
`StatusResync`, `SnapshotID`/`SnapshotDone` staging, and `pruneEmpty`/`restore`
heuristics are all **deleted**. Reconnect = ask once for the full current map.

This is the "fix Axis 1, keep the transport" option and the leading hypothesis.

**Prove it beats the current implementation**

- [ ] Implement the spike: server sends absolute per-RepGroup state objects;
  client applies them idempotently; remove delta application + `ignore` map.
- [ ] M1: zero flicker and zero overcount under the storm (vs baseline failing).
- [ ] M2: steady state emits nothing and the UI is perfectly still.
- [ ] M5: inject heavy coalescing/drops — final state still converges exactly
  (idempotent), with no resync mechanism at all.
- [ ] M6: reconnect converges with a single full-state fetch; no stuck counts.
- [ ] M7: `-race` clean; show the dirty-set + sender design has no lock-order
  inversion with the TTR/queue paths and copies counts out under the lock.

**Prove it beats the other schemes**

- [ ] M8: net **removal** of code (ignore map, resync, snapshot staging gone);
  **zero** new dependencies vs §2/§4 which add one.
- [ ] M4: bytes/s under the storm vs §2/§3/§5 — absolute counts are tiny and
  coalesced; quantify the (expected modest) overhead vs deltas and show it is
  acceptable at 10k+ jobs / many repgroups.
- [ ] Show it composes with any transport (so a later Axis-2 upgrade is additive,
  not a rewrite).

**Findings**

_(to be filled by the Scheme A subagent)_

---

## §2 Scheme B — `centrifugal/centrifuge` for transport + built-in recovery, `centrifuge-js` on the client

**Idea.** Replace the hand-rolled caster (and possibly the whole status WS) with
the Centrifuge Go library: publish status to a channel; rely on its history
window + per-publication **offset/epoch** automatic recovery so a client that
misses messages re-subscribes and is re-fed missed publications, and is told
when recovery is *impossible* (so it can do one clean full sync). The browser
uses the maintained `centrifuge-js` SDK (reconnect, recovery, subscription
multiplexing handled for us). This is the strongest *library* answer on Axis 2.

**Prove it beats the current implementation**

- [ ] Feasibility: embed `centrifuge.Node` inside wr's existing server with its
  TLS + token auth and embedded-binary model; document integration shape.
  (`go get` is available in the worktree; if blocked, report honestly.)
- [ ] Minimal spike: publish status changes to a channel; connect a client;
  verify delivery, reconnect, and **recovery of missed messages** after a forced
  drop (the headline feature today's code lacks).
- [ ] M5/M6: forced loss + reconnect recovers correctly and flags unrecoverable
  gaps for a clean resync (no racy home-grown resync).
- [ ] M7: `-race` clean; Centrifuge owns per-client write serialisation, so the
  wsWriteMutex tangle can go.

**Prove it beats the other schemes**

- [ ] M8: dependency weight (transitive deps, binary size delta), license, and
  maintenance health (release cadence, issues) — is the cost justified vs §1?
- [ ] Decide whether recovery of **deltas** is even desirable, or whether you'd
  *still* send absolute state over Centrifuge (i.e. does §2 add value over §1
  for wr's single-process, in-memory, modest-client-count reality?).
- [ ] Frontend cost: replacing the bespoke Knockout WS handler with
  `centrifuge-js` — integration effort and risk vs keeping the handler (§1/§3).

**Findings**

_(to be filled by the Scheme B subagent)_

---

## §3 Scheme C — Server-Sent Events with `Last-Event-ID` replay (native, no new server dep)

**Idea.** Status flows server→client over an `EventSource`; the browser handles
reconnect and sends `Last-Event-ID` automatically. The server keeps a small
bounded per-topic event log and replays events after a given id on reconnect —
native, protocol-level recovery. Client→server commands (current, details,
rerun, remove, resume, kill, …) move to plain HTTPS POST. Payload should still
be absolute state (Axis 1) so replay/loss is doubly safe.

**Prove it beats the current implementation**

- [ ] Spike: SSE endpoint emitting absolute per-RepGroup state with monotonic
  ids; `EventSource` client; commands over POST.
- [ ] M6: kill/restart the manager (and drop the network) mid-storm — verify the
  browser auto-reconnects and the server **replays** missed events by
  `Last-Event-ID` with correct convergence and no warning spam.
- [ ] M5: drops self-heal via replay + idempotent payloads.
- [ ] Confirm `EventSource` works with wr's token auth + TLS (note: `EventSource`
  cannot set custom headers — token must go in the URL/cookie as today).

**Prove it beats the other schemes**

- [ ] M8: simpler than §2 (no dependency, browser-native reconnect/replay) — but
  quantify the loss of a single bidirectional channel (extra POST endpoints) and
  any HTTP/1.1 connection-count concerns vs HTTP/2.
- [ ] M3/M4: latency and server cost of SSE framing + the replay log vs §1's
  coalesced WS pushes.
- [ ] Assess whether the bidirectional needs of the page (live actions, details
  subscriptions) make SSE+POST more or less complex than keeping one WS (§1).

**Findings**

_(to be filled by the Scheme C subagent)_

---

## §4 Scheme D — `olahol/melody` broadcast framework (transport-only swap; control experiment)

**Idea.** Swap the caster for Melody's session management + safe buffered
broadcast (still gorilla under the hood, with concurrency-safe writes and
ping/pong). Melody has **no** history/recovery, so this is deliberately a
*control*: it isolates Axis 2 (transport) from Axis 1 (payload). 

**Prove it beats the current implementation**

- [ ] Spike: route status broadcasts through Melody, keeping the **delta**
  payload, with a sane per-session send buffer.
- [ ] M1: test the central hypothesis — does melody-with-deltas still flicker /
  overcount under the storm? (Prediction: **yes**, because Melody under buffer
  pressure must still drop or block, and deltas remain non-idempotent.)
- [ ] M4: confirm Melody's buffered broadcast doesn't block the queue path.

**Prove it beats the other schemes**

- [ ] If the prediction holds, record this as direct evidence that a "go back to
  a 3rd-party broadcast library" answer (Axis 2 only) does **not** fix the bug —
  the payload (Axis 1) must change. Then test Melody **+ absolute state** and
  compare against §1 on M8 (does the dependency buy anything over native?).

**Findings**

_(to be filled by the Scheme D subagent)_

---

## §5 Scheme E — Native reliable sequenced state with a server-side per-client accumulator (no new deps)

**Idea.** Generalise the proven job-subscription accumulator to status counts.
Maintain a per-client outbound accumulator keyed by RepGroup holding the
**latest absolute counts** (coalescing = last write wins, lossless for state).
Assign a monotonic sequence under the *same lock* that mutates queue state so
there is a consistent cut. A dedicated writer drains the accumulator on a short
hold timer (like `serverSubscriptionHoldTime`), never blocking the queue path.
On reconnect the client sends its last sequence; the server either confirms
continuity or sends one clean full snapshot. This is the "make the existing
approach actually correct, natively" option and a direct test of whether the
five failed attempts were salvageable.

**Prove it beats the current implementation**

- [ ] Spike: per-client accumulator + lock-aligned sequence + hold-timer writer;
  client applies idempotent state keyed by sequence.
- [ ] M1/M5: no flicker, no overcount, self-healing under loss.
- [ ] M7: prove the consistent cut (sequence assigned under the mutation lock)
  and that the accumulator copies immutable counts out — explicitly avoiding the
  `260625-7` attempt-5 lock-order inversion and escaping-pointer bug.
- [ ] M6: sequence-based reconnect continuity vs a full resync.

**Prove it beats the other schemes**

- [ ] M4: bandwidth vs §1 (does per-client accumulation + sequencing buy enough
  over §1's simpler shared coalescing to justify its extra complexity?).
- [ ] M8: complexity vs §1 — is the accumulator worth it for wr's client counts,
  or is §1 the simpler subset that already wins?

**Findings**

_(to be filled by the Scheme E subagent)_

---

## Recommendation

_(to be filled after the investigations complete)_
