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

- [x] Storm reproduction. → `storm.mjs` drives the real `websocket-handler.js`
  via an injected fake WS that models the real 1-slot caster; 10k jobs
  new→ready→running→complete in 200-job batches, sampled ~200 Hz; trace captured.
- [x] Demonstrate **flicker**. → RepGroup row `rgExists=false`/total 0 while 10k
  jobs exist; worst dip below truth = **10000** (full disappearance); row stays
  gone for the storm; reproducible.
- [x] Demonstrate **overcount**. → 6 jobs added; snapshot reads complete=6, a
  lagging post-`SnapshotDone` `running→complete Count=3` delta → `complete(6+3)=9`
  (**9 > 6**), stuck (no self-heal).
- [x] Quantify **delta loss**. → `TestBaselineDeltaLossUnderStorm`: 10000 deltas
  vs slow drainer → ~**100%** dropped; frontend storm **88%** (268/310 → resync);
  each overflow drops **2** real deltas per 1 resync.
- [x] Quantify **message fan-out**. → 1 RG no-loss = **2 msgs/transition**, K RGs
  = **K+1**, loss adds 1+lostGroups, 50 RGs all-lost = **102**; 10k storm ≈ **300
  Send calls**. (Bytes/s assessed: tiny JSON per msg.)
- [x] Confirm the **resync race** has no consistent cut. → Proven from code: the
  callback runs detached (`go queue.changedCb`, queue.go:444); `current` reads
  `s.q.AllItems()` (serverWebI.go:451) then `s.db` complete jobs (serverWebI.go:468)
  at different instants, no lock spanning both and none shared with the emitter.
  `TestBaselineSnapshotFragmentClobbered`: a queued `SnapshotDone` is itself
  overwritten by an overflowing delta → client can hang `currentStatusInFlight`.
- [x] Re-examine the **lock-order inversion** risk. → `sfsmutex` is **absent**
  from committed `issuesfix` (attempt 5 reverted). Current orders: TTR holds
  `queue.Lock` across the callback → `job.Lock` → `statusCaster.Send` (caster is a
  leaf lock); snapshot acquires `queue`, `writeMutex`, `db` independently/un-nested.
  Danger for fixes: a new status mutex taken *under* the queue lock in the callback
  but *before* it in the snapshot path reintroduces the attempt-5 deadlock.
- [~] Baseline M1–M8. → M1, M4(fan-out), M5(loss) measured; M2/M3/M6/M8 and the
  real-server half of M7 assessed only (frontend+caster harness, no full server run).

**Findings**

Flicker is **Axis-2-driven**: under the storm ~88–100% of messages overflow the
1-slot buffer and become a single `StatusResync`; the resulting `current`
snapshot's per-RepGroup fragments **also** overflow the same buffer, so the
applied snapshot is empty/partial and `pruneEmptyLiveRepGroups` deletes the row
while thousands of jobs exist — and it can stay gone (clobbered `SnapshotDone`).
Overcount is **Axis-1 × no-cut**: the snapshot counts completed jobs from the DB
while the `running→complete` delta for those same jobs is emitted by a *separate
goroutine* and arrives after `SnapshotDone`, then re-added additively. **The
detached-goroutine callback means deltas can never be cleanly ordered against a
snapshot — this is unfixable while the payload is deltas.** Queue-mutation stays
non-blocking (caster `Send` returns under overflow), which a fix must preserve.

**Constraints any correct fix MUST satisfy (the bar):**
1. Give the count source a **consistent cut** — read/sequence state under the
   *same lock that mutates the queue*, **or** send **absolute idempotent state**
   so no cut is needed (a dropped/duplicated message becomes a harmless overwrite).
2. **Do not route recovery through the same lossy channel as live updates** (today
   snapshot fragments get clobbered) — coalescing must be idempotent-safe or the
   channel reliable.
3. **Eliminate or make sound the additive apply + `ignore` map** — they only paper
   over lost/out-of-order deltas and are the overcount surface.
4. **Respect lock order** — any new status mutex must sit entirely under, or
   strictly leaf-after, the `queue` lock (TTR holds `queue → job → caster`).
5. Keep the **queue-mutation path non-blocking** on a slow client.

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

- [x] Implement the spike. → server `statusState` = authoritative
  `map[RepGroup]map[JobState]int` + dirty set + leaf `mu` + coalesced `drainDirty`
  (`jobqueue/statusstate.go`, +274); client applies absolute msgs wholesale,
  delta/ignore deleted (`websocket-handler.js` 682→403).
- [x] M1: zero flicker and zero overcount under the storm. → absolute:
  **flicker=0, count-reset=0, overcount=0, converged=YES**; baseline (same storm,
  same ground truth): overcount=346 (max **+300**, reprp 6300/6000), **NOT converged**.
- [x] M2: steady state emits nothing, UI perfectly still. → absolute:
  `messagesAfterSteady=0`, 0 flicker, converged; baseline: **169 flicker-to-zero**
  events on `+all+`, loses 80 live jobs.
- [x] M5: heavy coalescing/drops still converge, no resync. → brutal (3 ms ticks,
  **45 coalesce drops**): flicker/overcount=0, **converged with NO resync mechanism
  at all**; unit test confirms a dropped intermediate self-heals on next drain.
- [x] M6: reconnect converges via a single full-state fetch. → converged to truth
  even with injected corruption (`running=999`); brief reset-then-restore only.
- [x] M7: `-race` clean, no lock-order inversion, copies under lock. →
  `TestStatusStateLockOrder` (real `queue.Queue`, 400-job storm, 8 reservers +
  concurrent drain) passes `-race` **3/3**; `statusState.mu` references the queue
  **nowhere** (pure leaf); `drainDirty` returns fresh map copies; the TTR
  `queue.mutex→job→mu` path is exercised.

**Prove it beats the other schemes**

- [x] M8: net code **removal**, zero new deps. → frontend **−279 LOC** (ignore map,
  StatusResync, SnapshotID/Done staging, prune/restore, delta arithmetic all gone);
  **zero** new dependencies (only stdlib `sync`), vs §2/§4 which add one.
- [x] M4: bytes acceptable at scale. → absolute ≈ **16%** of delta bytes/window at
  R∈{10,100,1000} repgroups (last-write-wins coalescing); 1 absolute msg 81–118 B
  vs 1 delta 93 B; a 1000-repgroup full push ≈ **118 KB once** on connect, then only
  dirty repgroups.
- [x] Composes with any transport. → the absolute object rides any channel; the
  1-slot buffer is already correct, so a later SSE/Centrifuge/Melody swap is
  *additive*, not a rewrite.

**Findings**

The direct empirical proof. Making the payload **idempotent absolute state**
eliminates the entire bug class: under one storm the current delta scheme
overcounts (6300 for 6000 jobs, never converges) and twitches (169 steady-state
flickers), while the absolute spike shows **zero flicker, zero overcount, exact
convergence** even under heavy drops **with no resync mechanism at all**. The
1-slot coalescing buffer becomes correct *by construction* (skip-to-newest), so
transport reliability drops from a correctness requirement to a bandwidth nicety
— which is exactly why §1 also beats the delta-keeping transport schemes (§2/§4)
and is simpler than §3/§5. It satisfies all five §0 constraints by construction
and is a **net code deletion**. **Verdict: §1 is the winner.** Caveats for the
real implementation (the spike kept the old delta+snapshot sends so existing tests
pass): production must (a) replace the `setupUpdateListener` status path with a
throttled `drainDirty` sender emitting the new absolute message, (b) seed
`statusState` at startup from a queue+DB scan, (c) delete the
delta/overflow/StatusResync/SnapshotID-Done machinery. M3 latency and the live
reconnect socket were modelled, not fully timed.

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

- [x] Feasibility: embed `centrifuge.Node` inside wr's existing server with its
  TLS + token auth and embedded-binary model; document integration shape.
  → Proved: outer HTTP handler runs existing `httpAuthorized`, then delegates to
  `NewWebsocketHandler` over `wss://` with self-signed cert (`Test_TLSAndTokenAuth`
  PASS). `go get centrifuge v0.38.0` + `centrifuge-go v0.12.0` succeeded.
- [x] Minimal spike: publish to a channel; connect a client; verify delivery,
  reconnect, **recovery of missed messages**. → `Test_RecoverMissedMessages`
  PASS: 4 missed pubs (offsets 4–7) replayed on resubscribe with `Recovered=true`.
- [x] M5/M6: forced loss + reconnect recovers AND flags unrecoverable gaps. →
  `Test_UnrecoverableGapSignalled` PASS: tiny window + 19 missed →
  `WasRecovering=true, Recovered=false` (the exact one-clean-sync signal).
- [x] M7: `-race` clean; Centrifuge owns per-client write serialisation. → spike
  `go test -race` PASS; no app-level write mutex needed (wsWriteMutex tangle can go).

**Prove it beats the other schemes**

- [x] M8: dependency weight, license, maintenance — justified vs §1? → **No.**
  +~23 modules, **+6.74 MiB stripped binary (+122%)**, links `rueidis`(Redis)/
  `prometheus`/`protobuf` even for in-memory use (monolithic, no build tag removes
  them); bumps go directive 1.23→1.25; MIT; healthy (1.4k★, powers Grafana Live)
  but **pre-v1** (minor versions may break API). Not justified vs §1's zero-dep
  code *removal*.
- [x] Recover deltas, or send absolute state over Centrifuge? → You'd send
  **absolute state** → Centrifuge's recovery becomes redundant; it adds no
  correctness over §1 for wr.
- [x] Frontend cost vs keeping the handler. → A **rewrite** of all 682 lines of
  `websocket-handler.js` + a second vendored minified SDK, vs §1's in-place edit.

**Findings**

Centrifuge does everything it claims and the integration is feasible (TLS+token
proved, recovery proved, unrecoverable-gap signal proved, `-race` clean). But it
is **the wrong axis**: it's an Axis-2 transport-recovery library, and wr's bug is
Axis-1 (non-idempotent deltas). It only wins in a world wr isn't in — multi-node
fan-out, millions of connections, Redis-backed history. For one in-process
manager with in-memory state and a handful of browser clients, the +6.74 MiB
binary, Redis/Prometheus dead weight, pre-v1 churn, and a full frontend rewrite
buy delta-recovery that §1 makes unnecessary. **Verdict: loses to §1 on
cost/benefit.** (Spike: standalone `centspike` module, ~436 LOC of tests, all
PASS, `-race` clean; wr tree left unmodified.)

---

## §3 Scheme C — Server-Sent Events with `Last-Event-ID` replay (native, no new server dep)

**Idea.** Status flows server→client over an `EventSource`; the browser handles
reconnect and sends `Last-Event-ID` automatically. The server keeps a small
bounded per-topic event log and replays events after a given id on reconnect —
native, protocol-level recovery. Client→server commands (current, details,
rerun, remove, resume, kill, …) move to plain HTTPS POST. Payload should still
be absolute state (Axis 1) so replay/loss is doubly safe.

**Prove it beats the current implementation**

- [x] Spike: SSE endpoint, absolute per-RepGroup state, monotonic ids,
  `EventSource` client, POST commands. → Built & run (`sse_server.go`, 344 LOC):
  `id: <epoch>.<seq>`, bounded 256-event replay log, over `ListenAndServeTLS`.
- [x] M6: kill/restart mid-storm → auto-reconnect + replay + converge + no
  warning spam. → After `kill -9`+restart the browser EventSource auto-reconnected
  (openCount 2), converged to the new sentinel in ~2.5–3.0 s, **1** onerror (no
  spam). Same-epoch curl replay returned **exactly** the missed seqs.
- [x] M5: drops self-heal via replay + idempotent payload. → `converged:true` with
  **no resync mechanism at all**; epoch-mismatch → clean full snapshot.
- [x] `EventSource` works with wr's token + TLS. → `httpAuthorized` already reads
  `r.Form.Get("token")`; `?token=` → 200, wrong token → 401; **HTTP/2 negotiated**
  over TLS (curl `ALPN: server accepted h2`) for wr's exact server pattern.

**Prove it beats the other schemes**

- [x] M8 + bidirectional cost + HTTP/2. → Zero server deps (stdlib). HTTP/2
  **confirmed**, so the 6-conn/origin limit is moot. **But** ~13 client→server
  command types → POST endpoints, and 3 other push streams (badServer/sched/
  **job-details subscription**) must be re-homed; ~**768 LOC added** vs §1 which
  *deletes* machinery.
- [x] M3/M4: framing + replay-log cost. → SSE absolute frame ≈ **144 B** (vs 71 B
  delta JSON); replay log = bounded 256-event pre-marshalled ring; storm 496
  frames, converge ~5 ms.
- [x] Bidirectional needs vs one WS (§1). → The stateful **per-client job-details
  subscription** (subscribe via command, receive `IsPushUpdate` pushes) is the
  hard case: POST-subscribe must be race-free-correlated to the right SSE stream
  via a connection id; one WS gives this for free. **This is Scheme C's main cost.**

**Findings**

SSE+absolute-state decisively beats the current implementation (no flicker, no
overcount, native browser reconnect+replay proven hard). **But the correctness
win is entirely from the absolute payload (Axis 1), which §1 shares** — and SSE
adds real structural cost §1 does not: splitting the page's *one bidirectional
WebSocket* into SSE + POST endpoints + a connection-correlation problem for the
subscription push. Critically, **plain `Last-Event-ID` is broken across a manager
restart (ids reset to 0)**; convergence required adding a home-grown **epoch** —
the very concept §2/Centrifuge ships built-in — so "native protocol-level
recovery" is only partly native. Reconnect latency (~3 s) is browser-governed and
barely tunable. **Verdict: a working sidegrade-with-tradeoffs, not a winner; for
wr's single-process/in-memory/few-clients reality §1's "ask once on reconnect" is
simpler and equally correct.**

---

## §4 Scheme D — `olahol/melody` broadcast framework (transport-only swap; control experiment)

**Idea.** Swap the caster for Melody's session management + safe buffered
broadcast (still gorilla under the hood, with concurrency-safe writes and
ping/pong). Melody has **no** history/recovery, so this is deliberately a
*control*: it isolates Axis 2 (transport) from Axis 1 (payload). 

**Prove it beats the current implementation**

- [x] Spike: route status broadcasts through Melody, keeping the **delta**
  payload, with a sane per-session send buffer (`MessageBufferSize=64`). →
  `.tmp/agent/melody-spike` real `httptest` WS server + real slow gorilla client.
- [x] M1: does melody-with-deltas still flicker/overcount under the storm? →
  **YES (prediction confirmed).** Real `websocket-handler.js` replay of melody's
  delivered stream ended at `complete=460` vs truth `4000` with a stuck `ignore`
  map; overcount race reproduced through the real handler (`100 → 101`).
- [x] M4: Melody's buffered broadcast doesn't block the queue path. → 12,000
  broadcasts in **12 ms** (1.0 µs/msg) to a backpressured session: melody
  **silently drops** (7,379 `ErrMessageBufferFull`, default no-op handler)
  rather than blocking.

**Prove it beats the other schemes**

- [x] Record as direct evidence that "go back to a 3rd-party broadcast library"
  (Axis 2 only) does **not** fix the bug; then test Melody + absolute state vs §1
  on M8. → Confirmed. Melody **drops the newest** message on overflow (no
  coalescing), so naive absolute-over-melody avoided flicker/overcount but left a
  **stale** terminal count (2175); only a §1-style coalescing sender converged
  exactly (`complete=4000`). M8: melody = BSD-2, ~1900 LOC, only runtime dep is
  `gorilla/websocket` (already vendored) → near-zero dep weight, **but** no
  history/recovery and nothing §1 needs → a net dependency with **no correctness
  benefit** over native §1.

**Findings**

This control cleanly isolates the two axes. Swapping **only the transport**
(caster → melody's safe buffered broadcast) leaves the bug fully intact: melody,
like any bounded buffer, sheds load under pressure by **silently dropping** —
fatal for non-idempotent deltas, and in one respect worse than today's caster
(silent drop doesn't even trigger wr's resync). Flicker/overcount vanish **only**
when the payload becomes idempotent absolute state; once it is, the transport's
drops are harmless and reliability becomes a mere bandwidth optimisation —
exactly the doc's central claim. **Verdict: refutes the "use a broadcast library"
answer; melody adds nothing over native §1.** (Spike: ~545 LOC Go test +
~159 LOC frontend replay through the real handler; `-race` clean.)

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

- [x] Spike: per-client accumulator + lock-aligned sequence + hold-timer writer;
  idempotent client apply. → `statusAccumulator` (RepGroup→absolute `stateCounts`,
  last-write-wins, hold-timer writer); real-handler `applyAbsoluteState`. `-race` clean.
- [x] M1/M5: no flicker, no overcount, self-healing under loss. → Playwright storm
  (15000×2 RepGroups): **0 flicker, 0 overcount**; converged exactly with 109/183
  dropped + 12 dups + 26 reorders and **0 resync requests**.
- [x] M7: prove the consistent cut + immutable copy-out + no inversion. →
  `TestSchemeECutHazard`: async-callback seq = **50/50 STALE**, under-mutation-lock
  seq = **0/50 STALE** (the cut is provably necessary *and* only correct under the
  lock); `stateCounts` is a pure value struct (copy-out test passes); `accMu` is a
  leaf lock, seq is lock-free `atomic` → no `sfsmutex↔queue` edge, no cycle.
- [x] M6: sequence-based reconnect continuity vs full resync. → shared-seq
  continuity preserves untouched RepGroups; `FullSnapshot` flag does one clean reset.

**Prove it beats the other schemes**

- [~] M4: bandwidth vs §1. → Assessed: §5 wire = §1's absolute object **+ one
  uint64 `Seq`/msg** (~8 B, negligible); not separately benchmarked.
- [~] M8: complexity vs §1. → Assessed: §5 is a **strict superset** of §1 (adds
  per-client accumulator maps × M clients + server-wide seq + reconnect-continuity
  protocol + **queue-package modification for the cut**).

**Findings**

§5 beats the current implementation decisively, but **loses to §1**. The feature
§5 adds over §1 — a monotonic sequence for a consistent cut — is only correct if
the sequence is assigned **under the queue mutation lock**, but `queue.changed()`
fires its callback in a detached goroutine *after* `queue.mutex.Unlock()`
(queue.go:444), so achieving the cut **forces a modification to the `queue`
package** (the same surgery whose careless form produced attempt-5's inversion).
Crucially, the cut is **not needed for correctness**: absolute idempotent state
*alone* (the part §5 shares with §1) removed the entire flicker/overcount class
with zero sequencing, proven by exact convergence under 60% loss + dups + reorder
and no resync. So §5's extra machinery buys only a reconnect-**bandwidth**
optimisation, at the cost of queue-package surgery and per-client accumulator
state that §1 avoids. **Verdict: §1 is the simpler subset that already wins for
wr's modest client counts.** (Useful artefact for the real fix: §5's
`applyAbsoluteState` is an *additive* handler — +58/−1 lines — that leaves the
legacy delta path intact, so the existing twitch fixture still passes.)

---

## Recommendation

**Implement §1 — Idempotent absolute per-RepGroup state, server-coalesced
(native, zero new dependencies).** The six investigations are unanimous and
convergent.

### Scoreboard summary

| Scheme | Beats current? | Beats §1? | One-line reason |
|---|---|---|---|
| §0 baseline | — | — | Proves the bug is Axis-1 (deltas) × no-consistent-cut; deltas are *unfixable* because the change callback runs in a detached goroutine after the queue unlocks. |
| **§1 absolute state (native)** | **YES** | — | **0 flicker / 0 overcount / exact convergence, no resync, net −279 LOC, 0 deps.** |
| §2 Centrifuge | yes | **no** | Wrong axis; you'd send absolute state over it anyway → its recovery is redundant; +6.7 MiB binary, Redis/Prometheus dead weight, pre-v1, frontend rewrite. |
| §3 SSE + Last-Event-ID | yes | **no** | Correctness comes from the absolute payload (shared with §1); adds a forced epoch + splits the one bidirectional WS into SSE+POST; +768 LOC. |
| §4 Melody (control) | **no** | **no** | Transport-only swap keeps deltas → still flickers/overcounts; proves "use a broadcast library" is the wrong answer. |
| §5 native accumulator | yes | **no** | Strict superset of §1; its sequence/cut needs queue-package surgery and buys only reconnect *bandwidth*, not correctness. |

### Why §1, directly answering the brief

The brief asked whether a reliable, widely-used third-party library — server and
frontend — should replace the home-grown broadcaster. The honest answer: **such
libraries exist and are excellent (Centrifuge is the gold standard; SSE+EventSource
is browser-native), but they solve the wrong problem here.** They harden the
*transport* (Axis 2). wr's bug is a *payload-semantics* defect (Axis 1): it streams
non-idempotent deltas, and **no transport, however reliable, makes delta arithmetic
safe** when the emitter is a detached goroutine that can never share a consistent
cut with the recovery snapshot (proved in §0; the five failed fixes in `260625-7`
all foundered on this). The §4 melody control proved a library swap alone leaves
the bug fully intact. Once the payload is **absolute idempotent state**, message
loss becomes a harmless overwrite, coalescing becomes correct by construction, and
the whole resync/snapshot-staging/ignore-map apparatus — the actual source of the
flicker and overcount — is *deleted*. §1 is therefore not just the best option, it
is a net simplification (−279 frontend LOC, 0 dependencies) that beats every
library/transport alternative on correctness, complexity, and cost simultaneously.

### Concrete design to implement (validated by the §1 spike)

- **Server (`jobqueue`):** add `statusState`, an authoritative
  `map[RepGroup]map[JobState]int` (+ a `+all+` aggregate) guarded by a **leaf**
  mutex that touches no other lock, plus a dirty-RepGroup set.
  - `applyTransition(from,to,repGroup,n)` updates the absolute counts (clamped
    ≥0) and marks the repgroup dirty. Call it from the existing change callback,
    the TTR callback, and the CLI lost/touch paths — exactly where `statusCaster.Send`
    is called today. It is a quick in-memory map update under the leaf lock, so it
    never blocks the queue-mutation path.
  - Seed `statusState` once at startup from a queue + completed-DB scan.
  - A single throttled sender per client drains the dirty set (`drainDirty`,
    coalesced last-write-wins, returning **fresh copies** — no escaping pointers)
    and writes one **absolute** message per dirty RepGroup.
- **Wire:** a new absolute per-RepGroup message (e.g. `jstateAbsolute`: RepGroup +
  the count for each state). On (re)connect the client gets the full current map
  once, then only dirty repgroups.
- **Client (`websocket-handler.js`):** apply each absolute message by replacing
  that RepGroup's counts wholesale. **Delete** delta arithmetic, the `ignore` map,
  `StatusResync`, and the `SnapshotID`/`SnapshotDone` staging + prune/restore.
  Reconnect = the existing socket reopen; no special resync path.
- **Delete** the caster overflow→resync logic (`casterOverflowValue`,
  `replacePending`) for the status channel and the `jstateCount` delta fan-out.
- **Lock discipline (§0 constraint 4, §1/§5 proof):** the new mutex must remain a
  leaf — never acquired before the `queue` lock anywhere, and the TTR path
  (`queue.mutex → job → statusState.mu`) must stay one-directional. This avoids the
  `260625-7` attempt-5 `sfsmutex↔queue` inversion. Verified `-race` clean on the
  real queue under a concurrent storm.

### Future-proofing

The payload change is transport-agnostic. If wr ever needs multi-node fan-out or
huge client counts, the absolute message can later ride SSE (§3) or Centrifuge
(§2) **additively**, without revisiting correctness — because absolute state is
already loss-tolerant. We are not foreclosing Axis-2 upgrades; we are removing the
need for one.
