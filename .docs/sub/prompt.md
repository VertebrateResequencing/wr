# Feature: Push-based job-completion subscription for the wr client

## Background

External and internal tools submit jobs to the wr manager through the Go
client library (`jobqueue.Connect` / `Client.Add`, grouping related jobs
under a `RepGroup`) and need to react the *moment* those jobs finish —
register output files elsewhere, fire a webhook, or push a "your results
are ready" event to a waiting front-end. A typical external caller submits
a command on behalf of some other system and must surface completion to
that system with no perceptible lag. wr itself has the same need: the
`wr add --sync` mode (`cmd/add.go`) submits a single job and must block
until that job finishes, then exit with the job's exit code and output —
today it does so by polling, and is the in-tree consumer that this feature
should let us rewrite.

Today the only way a Go client learns that a job finished is to **poll**.
The client API (`jobqueue/client.go`) is request/response only:

- `GetByRepGroup` (`jobqueue/client.go:1868`),
  `GetByRepGroupMatch` (`:1879`), `GetIncomplete` (`:1893`),
  `GetIncompleteByRepGroupMatch` (`:1904`),
  `GetLastCompletionTimeByRepGroup` (`:1918`), `GetByEssence` /
  `GetByEssences` — all synchronous, all over the `request()` req/rep
  transport (`:2031`). There is no push, stream, or subscribe primitive on
  `Client`.

Polling has three costs we want to remove: completion latency bounded by
the poll interval, wasted manager round-trips, and poor scaling when many
waiters each poll for their own jobs.

Crucially, **the manager already knows the instant a job changes state,
and already pushes that out** — just not to Go clients:

- The queue change hook `q.SetChangedCallback(...)`
  (`jobqueue/server.go:1623`) fires on every job state transition.
- It already broadcasts aggregate counts (`jstateCount`) and, for
  *subscribed* connections, per-job `JStatus` updates with
  `IsPushUpdate = true` (`jobqueue/server.go:1741`).
- Subscriptions are tracked per connection by job key in
  `jobSubscriptions` (`jobqueue/server.go:361`), managed by
  `subscribeToJobs` (`:3286`) and `unsubscribeFromJob` (`:3305`).
- This stream is delivered only over the browser web-UI websocket
  `/status_ws` (`jobqueue/server.go:851`,
  `webInterfaceStatusWS` in `jobqueue/serverWebI.go:200`,
  `setupUpdateListener` at `:430`). It is not reachable from
  `jobqueue.Client`.

So the push capability exists end-to-end inside the manager; this feature
is about exposing it to the Go client library through a first-class,
supported API, reusing the same state-change hook and `JStatus` payload
rather than building a parallel mechanism.

## What we want

A push/subscribe capability on the wr Go client so a client that
submitted jobs can be notified promptly (sub-second after the manager
observes the transition) when those jobs reach a terminal state, without
polling.

The **primary use case** is "block until the jobs I just added are done": a
caller adds jobs and wants to wait for exactly *those* jobs to reach a terminal
state, getting one push per added job as it finishes. This is keyed on the
specific internal job identities returned by the add — *not* a generic wait on
whatever happens to share a `RepGroup`. Each job has a stable internal key
(`Job.Key()` in `jobqueue/job.go`), and `Client.AddAndReturnIDs`
(`jobqueue/client.go`) already returns those keys for the jobs it queued, so the
caller holds exact handles to wait on. Subscribing by whole `RepGroup` is also
supported, but it is the secondary, coarser case.

### Add-and-wait (primary)

- An option/variant of adding jobs that, after queuing, blocks until every
  just-added job has reached a terminal state — driven by pushes on the specific
  internal job keys returned by the add, one push per job, unblocking once num
  pushes matches num jobs. The caller never names a `RepGroup` for this; it
  waits on the exact jobs it submitted.
- This must subsume what `wr add --sync` does today (wait for a single
  added job, then report its terminal state / exit code / output), and
  generalize it to N added jobs, so the polling loop in `cmd/add.go` can
  be replaced by this push path.
- Catch-up applies (see below): a job that finished between the add
  returning and the wait starting must still be reported, so add-and-wait
  cannot hang on an already-terminal job.

### Client-facing subscription API

- A method on `jobqueue.Client` to subscribe to completion events for a
  set of jobs identified by **one or more internal job keys** (the handles
  returned by `AddAndReturnIDs`, or assembled from `JobEssence`s) and/or by
  **RepGroup** (exact match — the coarser handle a submitter may own). Both
  scoping modes are first-class; key-based scoping is what the add-and-wait
  path uses.
- Delivery via an idiomatic Go push primitive: a receive-only channel of
  typed updates (e.g. `<-chan *JobUpdate`), or a registered callback. A
  per-job update carries at least: job key, `RepGroup`, terminal `JobState`
  (complete / buried / lost), exit code, fail reason, and start/end
  timing — the fields already present on `JStatus`
  (`jobqueue/serverWebI.go`). It need not carry full stdout/stderr; the
  consumer can fetch those via the existing `Get*` methods on receipt. The
  RepGroup aggregate event (below) instead identifies the `RepGroup`; the
  spec defines whether it carries per-job fields or aggregate terminal
  counts.
- **Terminal-state semantics:** when subscribed by job key(s), deliver one
  event per subscribed job as it transitions to `complete`, `buried`, or
  `lost` (`JobState` constants in `jobqueue/job.go`). When subscribed to a
  RepGroup, deliver a single event once *all* jobs in that RepGroup are
  terminal (no per-job events) — the "tell me when my whole batch is done"
  case.
- **Catch-up / late subscribe:** if a subscribed job is already terminal
  at subscribe time (e.g. a fast job finished microseconds before the
  client subscribed), its event must be delivered immediately so the
  caller never hangs forever waiting for an event that already fired. The
  spec must define the catch-up window (currently-live jobs plus
  recently-archived jobs).
- **Lifecycle:** subscription bound to a `context.Context` and/or an
  explicit `Unsubscribe` + `Close`; clean teardown when the client
  disconnects; bounded buffering with a documented overflow policy
  (drop-oldest vs. block) and a way for the consumer to detect that drops
  occurred.
- **Auth/transport:** subscriptions use the client's existing
  authenticated, TLS-secured connection (CA file + token, as
  `Connect`/`ConnectUsingConfig` already require — `jobqueue/client.go:207`,
  `:288`) and are subject to the same authorization as other client
  calls. The caller must not have to run a browser or hand-assemble
  websocket frames.

### Server side

- Generalize the existing subscription mechanism so subscriptions can be
  keyed by **a set of individual job keys** and by **RepGroup** (today it
  is per individual job key only), and so a non-browser (Go-client)
  transport can register and receive them.
- The `SetChangedCallback` hook (`jobqueue/server.go:1623`) and the
  `JStatus` payload must remain the single source of state-change events
  feeding **both** the browser `/status_ws` stream and the new client
  subscriptions — no second, divergent notification path.
- The transport is a long-poll over the client's existing mangos `req`/`rep`
  endpoint on `Port` (see the Notes "Transport" decision). The hard constraint
  is that it reuses the manager endpoint and credentials the client already has
  — no new config, no new listening port — delivers terminal events reliably
  (it must NOT depend on the lossy browser `/status_ws` + `grafov/bcast` push
  path), and is exposed as a normal Go method, not as a browser-only feature.

## Acceptance criteria

- Add N jobs, then block on the returned job keys, and receive a terminal
  event for each within a small bound of *actual* completion (not
  poll-interval bound) — including at least one job that fails (buried)
  and one that is lost.
- The single-job add-and-wait case behaves like `wr add --sync` does
  today (reports terminal state / exit code), with completion latency
  bounded by actual completion rather than a poll interval.
- Subscribe by RepGroup also works and yields a terminal event only when all
  jobs in that RepGroup are terminal.
- Subscribing *after* a job already completed still yields that job's
  terminal event (catch-up), for both key-based and RepGroup-based
  subscriptions.
- A subscription survives a manager restart / reconnect without silently
  missing terminal events, or surfaces an explicit, detectable gap that
  the client can recover from by re-syncing.
- No regression to the existing `/status_ws` browser updates; both
  consumers are driven by the same `SetChangedCallback` hook.
- An unauthorized / invalid-token client cannot subscribe.

## Out of scope

- Changing how jobs are submitted, scheduled, or run (beyond adding the
  block-until-done option to the existing add path).
- New web-UI features.
- Server-side persistence of subscriptions across manager restarts (the
  client re-subscribes on reconnect).
- Delivering full stdout/stderr in the push payload (identifiers +
  terminal status + exit/fail/timing metadata only; fetch output via the
  existing `Get*` methods).

## Reference points in the wr codebase

- `jobqueue/client.go` — `Connect` (`:207`), `ConnectUsingConfig`
  (`:288`), `Add` (`:418`), `AddAndReturnIDs` (`:433`, returns the added
  jobs' keys), `GetByRepGroup` (`:1868`), `GetByRepGroupMatch` (`:1879`),
  `GetIncomplete` (`:1893`), `GetIncompleteByRepGroupMatch` (`:1904`),
  `GetLastCompletionTimeByRepGroup` (`:1918`), `GetByEssence` /
  `GetByEssences`, `request()` (`:2031`), `RepGroupMatch` modes.
  Poll-only today.
- `cmd/add.go` — `wr add --sync` (`--sync` flag and `synchronousAdd`),
  the in-tree poll-until-terminal consumer to be rewritten on top of the
  new push path.
- `jobqueue/job.go` — `Job` struct, `Job.Key()` (the stable internal job
  identifier), `JobState` constants (`complete` / `buried` / `lost` / …),
  `RepGroup` / `ReqGroup`.
- `jobqueue/server.go` — `SetChangedCallback` state-change hook (`:1623`);
  `jstateCount`; `jobSubscriptions` (`:361`), `subscribeToJobs` (`:3286`),
  `unsubscribeFromJob` (`:3305`); `statusCaster` (`:354`);
  `/status_ws` route registration (`:851`); `IsPushUpdate` set (`:1741`).
- `jobqueue/serverWebI.go` — `webInterfaceStatusWS` (`:200`),
  `setupUpdateListener` (`:430`), `JStatus` struct with `IsPushUpdate`
  (`:76`).

## Notes

These decisions refine the requirements above and take precedence where they
add detail.

- **Transport:** the client subscription is delivered by **long-poll over the
  client's existing mangos `req`/`rep` endpoint on `Port`** — the same endpoint,
  host, token and TLS the client already uses; no new config, no new listening
  port. This was chosen over (a) the existing `/status_ws` websocket and (b)
  extending mangos with `pub`/`sub`:
  - The websocket push path is **provably unreliable** for a never-drop
    guarantee: it depends on `grafov/bcast` pinned to a 2016 commit
    (`go.mod` `replace` + the warning at `jobqueue/server.go:50`: "must be
    commit e9affb593f6c... or status web page updates break in certain cases"),
    its aggregate `statusCaster.Send` path is lossy with its error ignored
    (`server.go:1682`), and there is a join-after-connect race
    (`serverWebI.go:420`) where events can be missed. Building "block, never
    drop terminal" on that substrate would mean hardening a known-broken,
    abandoned dependency.
  - mangos `pub`/`sub` would force a **second listening port** (each mangos
    socket binds one protocol; two listeners cannot share `Port`).
  - Long-poll needs neither. The server's `rep` socket is already in **raw
    mode** (`server.go:620`, "we use raw mode, allowing us to respond to
    multiple clients in parallel") with a goroutine-per-request dispatch
    (`server.go:1010`), and the codebase **already parks a reply for seconds**
    in exactly this shape — the blocking `reserve` call
    (`reserveWithLimits` → `s.q.Reserve(group, wait)`, `serverCLI.go:913`). So a
    "wait for updates" request that the server holds until events are ready is a
    proven pattern, not a new mechanism.
  - Mechanism: the client uses its **existing primary connection** for quick
    control calls (subscribe / unsubscribe by job-key set or RepGroup → returns
    a subscription id plus the synchronous catch-up batch), and opens **one
    dedicated second connection to the same `Port`** (an ordinary cooked
    `req`/`rep` socket — a second connection is required because the primary
    socket serialises all calls under a mutex, `client.go:2032`) that runs the
    long-poll loop: send "wait for updates(subID)" → the server parks the reply
    until ≥1 buffered event exists for that subscriber or a hold-timeout (kept
    safely under the cooked-`req` resend interval, e.g. ~25–30s) elapses → reply
    with the accumulated batch (empty on timeout) → client immediately re-polls.
    Latency is transition-bound (the server replies instantly when an event is
    already buffered); the only gap is ~1 RTT between cycles.
  - **Reliability source:** events are buffered into a new, **bounded,
    per-subscriber server-side queue fed directly from `SetChangedCallback` /
    `JStatus`** (the single source) — NOT from the lossy `bcast` `statusCaster`.
    `block, never drop terminal` applies to this per-subscriber queue. The
    browser `/status_ws` + `bcast` path is left untouched (single source, two
    independent delivery paths; the new path does not regress the old one).
  Net result for callers: upgrading is a go.mod bump + recompile (the new method
  is purely additive/opt-in); no new config, no new credentials, and — because
  it rides the very `Port` clients already use — no new reachability
  requirement at all. The spec must specify the concrete request/reply framing,
  the dedicated socket's recv-deadline / resend settings (so a parked reply
  stays under the resend interval), and confirm parked long-polls scale (one
  blocked goroutine per subscribed client, as `reserve` already does).
  Modernisation note: wr is pinned to the deprecated `nanomsg.org/go-mangos
  v1.4.0`; a future `go.nanomsg.org/mangos/v3` migration and any deeper
  transport rework are explicitly out of scope for this feature.
- **Buffer overflow policy:** block (apply back-pressure); never drop a
  terminal event. Each subscriber has its own bounded buffer and is isolated,
  so a slow or stuck consumer stalls only its own subscription, never the
  manager's notification path or other subscribers. (Completion events are
  low-volume, so the back-pressure risk in normal use is small.)
- **Add-and-wait return value:** the blocking add-and-wait call returns the
  full terminal `[]*Job` (jobs in their terminal state) so callers — including
  the `wr add --sync` rewrite in `cmd/add.go` — can read exit code and
  stdout/stderr inline without a second round-trip. It still unblocks only once
  the number of terminal pushes equals the number of just-added jobs.
- **Subscribe API shape (key-based vs RepGroup):** expose subscription as an
  idiomatic Go method on `Client` returning a receive-only channel of typed
  per-job updates (`<-chan *JobUpdate`), with scope given by either a set of
  job keys or a single exact `RepGroup`. Lifecycle is bound to a
  `context.Context` plus an explicit unsubscribe/close; channel closure plus an
  error accessor surface teardown/disconnect (with block-never-drop, terminal
  events are not silently lost).
- **RepGroup aggregate event payload:** the single event fired when all jobs in
  a subscribed RepGroup are terminal identifies the `RepGroup` and carries
  aggregate terminal counts (complete / buried / lost / total) plus the list of
  per-job keys and their terminal states. It does not carry stdout/stderr;
  callers fetch output via the existing `Get*` methods.
- **Catch-up window:** catch-up covers currently-live jobs plus recently
  completed/archived jobs matching the subscribed keys/RepGroup — a bounded
  recent set, not unbounded boltdb history — so a reused key/RepGroup cannot
  match an ancient completion. The spec must state the concrete bound.
- **Terminal vs `lost`:** only `complete` and `buried` are *terminal* — they
  are what unblock add-and-wait and count toward the "all jobs done" contract.
  A `lost` job is provisional in wr (it can revive to `running`, or later
  settle to `buried`/`complete`), so a `lost` transition is delivered as an
  informational, non-counting event on the channel, not as a completion. The
  spec must define how a job that stays `lost` eventually reaches a true
  terminal state (via wr's existing kill/confirm path) so add-and-wait cannot
  hang indefinitely on a permanently-lost job; if no such guarantee exists,
  document it as an explicit caveat and offer the caller a bounded-wait /
  context-cancellation escape.
- **Catch-up snapshot:** the channel carries terminal events only. A subscribed
  job that is still in progress (`running`/`ready`/…) at subscribe time gets no
  initial snapshot event — only its eventual `complete`/`buried` event (plus any
  informational `lost`). add-and-wait knows how many jobs to expect from the add
  itself, so it needs no baseline snapshot.
- **Reconnect / gap recovery:** on a manager restart or reconnect the
  subscription channel stays open; the client transparently re-subscribes and
  re-runs catch-up, and exposes a detectable "resynced"/gap indication via the
  error accessor so the caller knows a re-sync occurred. Terminal events are not
  silently missed across the gap.
- **Concrete API surface:** expose two methods on `Client` — one to subscribe to
  a set of job keys, one to subscribe to a single exact `RepGroup` — each
  returning a `Subscription` handle that exposes the receive-only
  `Updates() <-chan *JobUpdate`, an `Err()` accessor, and an `Unsubscribe()`,
  and is bound to the passed `context.Context`. A single `JobUpdate` type with a
  discriminator distinguishes per-job terminal events from the RepGroup
  aggregate event. The blocking add-and-wait entry point is built on the
  key-based subscription and returns `[]*Job` as described above.
- **Concrete catch-up bound:** at subscribe time, catch-up = the live in-memory
  jobs plus a direct lookup of the boltdb complete bucket for exactly the
  subscribed keys / RepGroup. The bound is "still present in the complete
  bucket" (no separate global recent-set ring to maintain); where a reused key
  could match more than one historical completion, the most recent terminal
  record wins. Only `complete`/`buried` records trigger an immediate catch-up
  event.
- **RepGroup aggregate edge cases:** "all terminal" means every job currently
  known in the group is `complete` or `buried`; a job that is `lost` holds the
  aggregate event back until it settles (or the caller's context fires). The
  aggregate event requires at least one matching job — an empty RepGroup (no
  jobs match and none arrive) does not fire a spurious "zero done" event; the
  caller relies on its context/timeout in that case.
- **Delivery guarantee / dedup:** at-least-once per job. A job that finishes
  during the subscribe/catch-up handshake may be delivered twice (catch-up +
  live race); the spec states this and requires consumers to dedup by job key.
  add-and-wait dedups internally, counting distinct terminal keys, so a
  duplicate never miscounts.
- **`Err()` and teardown causes:** `Err()` reports the terminal teardown cause —
  nil after a clean `Unsubscribe`, the `context` error on cancel/deadline, and a
  typed sentinel on unrecoverable disconnect. A transient resync (after
  reconnect) does NOT close the channel or set a fatal `Err()`; it is surfaced
  as a distinct discriminator value on the `Updates()` channel so the caller can
  re-sync without tearing down.
- **Add-and-wait outcome contract:** add-and-wait returns the full `[]*Job` with
  a nil error once every added job reaches `complete`/`buried` — a mix of
  succeeded and failed (buried) jobs is not itself a Go error; the caller (and
  the `cmd/add.go` `--sync` rewrite) inspects each job's state/exit code and
  preserves today's behaviour of exiting with the command's exit code, including
  for buried jobs. If a job stays `lost` and never settles before the caller's
  context deadline, add-and-wait returns the jobs gathered so far plus a
  context-deadline error naming the unfinished keys (the bounded-wait escape).
