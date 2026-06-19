# Push-based Job-Completion Subscription Specification

## Overview

wr Go clients today learn that a job finished only by polling the manager
over the request/reply mangos socket (`Client.request`,
`jobqueue/client.go:2031`). The manager already detects every state change
via `q.SetChangedCallback` (`jobqueue/server.go:1623`) and already pushes
per-job `JStatus` updates to subscribed browser websockets, but that stream
is unreachable from `jobqueue.Client`.

This feature exposes that push capability as a first-class Go client API. A
client subscribes by a set of internal job keys (the IDs returned by
`Client.AddAndReturnIDs`) or by a single exact `RepGroup`, and receives typed
`*JobUpdate` events on a channel the instant the manager observes a relevant
transition. A blocking add-and-wait entry point built on key subscription
replaces the `wr add --sync` poll loop (`cmd/add.go`).

Key behaviours:

- Delivery is a long-poll over the client's EXISTING mangos `req`/`rep`
  endpoint on `Port` -- the same endpoint, host, token and TLS the client
  already uses. Control calls (subscribe/unsubscribe) ride the client's
  existing primary connection; a dedicated SECOND cooked `req`/`rep`
  connection dialled to the SAME `Port` runs the long-poll loop. No new
  server listening port, no new config, no new credential.
- Only `complete` and `buried` are terminal (they unblock add-and-wait and
  count toward "all done"). `lost` is delivered as an informational,
  non-counting event.
- Catch-up at subscribe time covers live in-memory jobs plus a direct boltdb
  complete-bucket lookup for exactly the subscribed keys/RepGroup.
- Per-subscriber bounded buffer, block-never-drop, isolated: a stuck consumer
  stalls only its own subscription.
- At-least-once delivery; consumers dedup by job key.
- Reconnect re-subscribes transparently, keeping the channel open and
  surfacing a resync marker rather than a fatal error.

## Architecture

### Packages and files

- `jobqueue/subscription.go` (new): client-side `Subscription`, `JobUpdate`,
  `Client.SubscribeToJobKeys`, `Client.SubscribeToRepGroup`,
  `Client.AddAndWait`, the dedicated second `req`/`rep` socket dialled to the
  same `Port` (existing token + CA + cert domain), and its long-poll loop.
  Control calls (subscribe/unsubscribe) reuse `Client.request`
  (`client.go:2031`) on the primary connection. Test:
  `jobqueue/subscription_test.go`.
- `jobqueue/serverCLI.go` (edit): add the new request `Method`s to the
  `handleRequest` dispatch (`serverCLI.go:40`, `:67` switch) -- `subscribe`
  (by key set or RepGroup; registers the subscription, returns a subscription
  id plus the synchronous catch-up batch), `unsubscribe`, and the parked
  `waitForUpdates` (holds the reply like `reserve` does, `:191`/`:228`, until
  the per-subscriber queue has >=1 event or the hold-timeout elapses). Test:
  `jobqueue/serverCLI_test.go`.
- `jobqueue/server.go` (edit): add the server-side subscription registry
  (key-set or RepGroup scope per subscription id) plus a bounded,
  per-subscriber event queue, fed DIRECTLY from the `SetChangedCallback`
  emission (`:1694-1777`) -- the single source -- alongside the untouched
  browser `statusCaster`/`jobSubscriptions` path. Generalize tracking to
  support RepGroup aggregate state. Test: `jobqueue/jobqueue_test.go`.
- `cmd/add.go` (edit): replace `waitForJobCompletion` poll loop with
  `Client.AddAndWait`.

### Transport

The subscription is a LONG-POLL over the client's EXISTING mangos `req`/`rep`
endpoint on `Port` -- the same endpoint, host, token and TLS the client
already uses. No websocket, no `WebPort`, no second mangos `Listen`, no
pub/sub, no new server listening port.

The server's `rep` socket is ALREADY in raw mode
(`sock.SetOption(mangos.OptionRaw, true)`, `server.go:620`, "we use raw mode,
allowing us to respond to multiple clients in parallel"). The dispatch loop
reads each request with `sock.RecvMsg()` (`server.go:997`) and spawns a
goroutine per request (`server.go:1010`) that calls `s.handleRequest`
(`serverCLI.go:40`) and replies via `s.reply(m, sr)` (`serverCLI.go:938`).
Parking a reply for seconds is a PROVEN pattern here: the blocking `reserve`
request (`case "reserve"`, `serverCLI.go:191`) calls `reserveWithLimits` ->
`s.q.Reserve(group, wait)` (`serverCLI.go:913`), which blocks the handler
goroutine until a job is ready or `cr.Timeout` elapses, then replies. The
long-poll `waitForUpdates` handler is the SAME pattern: one blocked goroutine
per subscribed client, exactly as `reserve` already does.

Control plane (client's EXISTING primary connection, via `Client.request`,
`client.go:2031`):

- `subscribe`: a `clientRequest` with a new `Method` carrying either a SET of
  job keys (`cr.Keys`, `client.go:116`) or an exact `RepGroup`. The server
  registers a server-side subscription, returns a subscription id, and returns
  the synchronous CATCH-UP batch (already-terminal matching jobs from live
  memory plus the boltdb complete bucket via `retrieveCompleteJobsByKeys` /
  `retrieveCompleteJobsByRepGroup`, `db.go:818`/`:878`) in the
  `serverResponse` (`server.go:140`). See C1.
- `unsubscribe`: a `clientRequest` naming the subscription id; the server drops
  the registration and its per-subscriber queue.

Both control calls are quick request/reply round-trips. They MUST NOT use the
long-poll socket: a parked poll would block them, because both the primary and
the dedicated socket serialise all calls under the client mutex
(`request()` does `c.Lock()`/`c.Unlock()` around Send+Recv, `client.go:2032`).

Data plane (a DEDICATED SECOND connection):

- The client opens one additional ordinary cooked `req`/`rep` socket dialled to
  the SAME `Port`, with the SAME token and TLS (same `caFile`/`certDomain`
  the client already holds from `Connect`, `client.go:207`/`:262`). A second
  connection is required because the primary socket serialises all calls under
  its mutex (`client.go:2032`); a parked poll on the primary socket would
  block every other client call.
- Long-poll loop: the client sends a `waitForUpdates` request carrying the
  subscription id; the server PARKS the reply (like `reserve`) until >=1 event
  is buffered for that subscriber OR a hold-timeout elapses; the server then
  replies with the accumulated batch (may be empty on timeout); the client
  immediately re-polls. Latency is transition-bound: the server replies
  instantly when an event is already buffered; the only gap is ~1 RTT between
  cycles.
- The dedicated socket sets `OptionRecvDeadline` (as the primary does,
  `client.go:225`) large enough to cover a parked poll. The server's
  hold-timeout MUST stay safely UNDER the cooked-`req` resend interval (e.g.
  ~25-30s) so the parked reply lands before the request socket re-sends the
  request. The spec's concrete settings: client recv-deadline >= hold-timeout
  + 1 RTT margin; hold-timeout <= 25s.

Wire framing: the existing `clientRequest`/`serverResponse` binc/codec frames
(`client.go:113`, `server.go:140`) over the mangos socket -- the same
`Method`-dispatched request/reply shape every existing client call uses
(`request()`, `client.go:2031`). The catch-up batch and each long-poll batch
are carried as `[]*Job` / a small typed payload in `serverResponse`; the
per-job terminal/lost state, the RepGroup-aggregate, and the resync marker are
discriminated client-side into `*JobUpdate` (see Types). No JSON, no
websocket frames.

Reliability source: events are enqueued into a NEW, bounded, per-subscriber
server-side queue fed DIRECTLY from `SetChangedCallback` / `JStatus`
(`server.go:1623`/`:1694-1777`) -- the single source -- NOT via the lossy
`grafov/bcast` `statusCaster` (`statusCaster.Send`, `server.go:1682`, whose
error is ignored). The browser `/status_ws` + `bcast` path is UNTOUCHED: one
source, two independent delivery paths; the new long-poll path does not regress
the old browser path (see F1).

Single source of truth preserved: the `SetChangedCallback` (`server.go:1623`,
emission at `:1694-1777`) -- the SINGLE state-change source -- computes each
changed job's `JStatus` once and routes it both to the existing browser
per-key path (`jobSubscriptions`/`statusCaster`) AND to the new per-subscriber
queues (matched by key set / RepGroup scope). No second callback, no divergent
traversal; the browser still receives its `JStatus` with
`IsPushUpdate == true` exactly as today (see F1).

Buffering / back-pressure: per-subscriber. Each server-side subscription has
its own bounded event queue. When the callback enqueues an event for a
subscriber whose queue is full, the enqueue BLOCKS (back-pressure on that one
subscriber) rather than dropping a terminal event ("block, never drop
terminal"). A stuck or slow consumer therefore stalls only its own
subscription's queue, never the `SetChangedCallback` path or other
subscribers' queues (per-subscriber isolation; see D2). This is deliberately
unlike the lossy `bcast` `statusCaster` path, where a send can be dropped with
its error ignored (`server.go:1682`). The callback drains nothing inline on a
client socket: the parked `waitForUpdates` handler dequeues, so a slow poller
cannot leak goroutines.

### Types

```go
// JobUpdateKind discriminates the events on a Subscription channel.
type JobUpdateKind int

const (
    // JobUpdateTerminal: a subscribed job reached complete or buried.
    JobUpdateTerminal JobUpdateKind = iota
    // JobUpdateLost: a subscribed job entered the provisional lost state
    // (informational, non-counting; the job may revive or later settle).
    JobUpdateLost
    // JobUpdateRepGroupDone: all currently-known jobs in a subscribed
    // RepGroup are terminal (fired once; see B2).
    JobUpdateRepGroupDone
    // JobUpdateResync: the client transparently re-subscribed after a
    // reconnect; catch-up was re-run. Not an error; the channel stays open.
    JobUpdateResync
)

// JobUpdate is the single event type delivered on Subscription.Updates().
type JobUpdate struct {
    Kind       JobUpdateKind
    Key        string    // job key (empty for RepGroupDone and Resync)
    RepGroup   string    // job's RepGroup (or the subscribed RepGroup)
    State      JobState  // terminal/lost state (JobUpdateTerminal/Lost only)
    Exitcode   int
    FailReason string
    Started    *int64    // unix nanos, nil if never started
    Ended      *int64
    // RepGroupDone aggregate (JobUpdateRepGroupDone only):
    Complete   int
    Buried     int
    Lost       int
    Total      int
    JobKeys    []string   // per-job keys in the group
    JobStates  []JobState // parallel to JobKeys, terminal state of each
}

// Subscription is the handle returned by the Subscribe* methods.
type Subscription struct { /* unexported fields */ }

// Updates returns the receive-only channel of events. Closed on terminal
// teardown (clean Unsubscribe, context cancel/deadline, or unrecoverable
// disconnect). A transient resync does NOT close it.
func (s *Subscription) Updates() <-chan *JobUpdate

// Err returns the terminal teardown cause: nil after a clean Unsubscribe or
// while live; the context error on cancel/deadline; ErrSubscriptionClosed on
// unrecoverable disconnect. A transient resync does not set Err.
func (s *Subscription) Err() error

// Unsubscribe tears down the subscription, closing Updates() with nil Err.
// Idempotent.
func (s *Subscription) Unsubscribe()
```

```go
// ErrSubscriptionClosed is the typed sentinel for unrecoverable disconnect.
var ErrSubscriptionClosed = errors.New("jobqueue subscription closed: " +
    "unrecoverable disconnect")
```

### Client API signatures

```go
// SubscribeToJobKeys subscribes to terminal (complete/buried) and
// informational (lost) events for the given job keys. The returned
// Subscription is bound to ctx: when ctx is done, Updates() closes and Err()
// returns ctx.Err().
func (c *Client) SubscribeToJobKeys(ctx context.Context,
    keys []string) (*Subscription, error)

// SubscribeToRepGroup subscribes to a single exact RepGroup. It delivers one
// JobUpdateRepGroupDone event once every currently-known job in the group is
// terminal (no per-job terminal events). Bound to ctx as above.
func (c *Client) SubscribeToRepGroup(ctx context.Context,
    repGroup string) (*Subscription, error)

// AddAndWait adds jobs (like AddAndReturnIDs), then blocks until every
// just-added job reaches a terminal state (complete or buried), returning the
// jobs in their terminal state. It subscribes by the returned keys and
// dedups by key internally, counting distinct terminal keys. ignoreComplete
// matches AddAndReturnIDs. Returns the gathered []*Job and:
//   - nil error once all added jobs are terminal (a mix of complete and
//     buried is NOT an error);
//   - ctx.Err() if ctx fires first, with the jobs gathered so far and an
//     error naming the keys still not terminal.
func (c *Client) AddAndWait(ctx context.Context, jobs []*Job,
    envVars []string, ignoreComplete bool) ([]*Job, error)
```

### Server-side state

Add a subscription registry keyed by a server-issued subscription id. Each
entry holds the scope -- a SET of job keys OR one exact RepGroup -- and, for
RepGroup scope, the set of keys seen and their latest terminal/lost state for
aggregate tracking. Each entry owns a NEW bounded event queue (a buffered
channel) drained by the parked `waitForUpdates` handler for that subscription.
Guard the registry with its own mutex (analogous to the existing `jsmutex`,
`server.go:371`). This registry is SEPARATE from the browser-only
`jobSubscriptions` (`server.go:361`) and `statusCaster` (`server.go:354`),
which are left untouched. Both the browser path and the new per-subscriber
queues are driven by the same `SetChangedCallback` (`server.go:1623`), which
computes per-job `JStatus` once (`:1694-1777`) and routes to all matching
consumers.

### Error handling

- Subscribe with an invalid/missing token: the primary connection's
  `subscribe` request is rejected server-side by the existing client-auth check
  (`serverCLI.go:58-61`: `tokenMatches(cr.Token, s.token)` fails ->
  `srerr = ErrPermissionDenied`) before any subscription is registered.
  `SubscribeToJobKeys`/`SubscribeToRepGroup` return an error whose string
  contains `ErrPermissionDenied`. No subscription id is issued and no events
  are buffered.
- DB read failure during catch-up: subscribe returns an error wrapping
  `ErrDBError`; no partial subscription left registered.
- Unrecoverable disconnect (manager gone, re-dial of the primary and the
  dedicated long-poll socket to `Port` fails after the retry budget):
  `Updates()` closes, `Err()` returns `ErrSubscriptionClosed`.

## A. Client subscription transport and auth

### A1: Long-poll over the existing mangos Port

As a client, I want completion events over the manager's existing mangos
`req`/`rep` endpoint on `Port` -- the same endpoint, token and TLS I already
hold -- so upgrading is a go.mod bump + recompile with no browser, no new
config, no new credential, and no new listening port.

Control calls (subscribe/unsubscribe) ride the client's EXISTING primary
connection via `Client.request` (`client.go:2031`). For the long-poll the
client opens ONE additional ordinary cooked `req`/`rep` socket dialled to the
SAME `Port` with the SAME token and TLS (same `caFile`/`certDomain` from
`Connect`, `client.go:207`/`:262`); a second connection is required because the
primary socket serialises all calls under its mutex (`client.go:2032`). The
long-poll loop sends `waitForUpdates(subID)`; the server PARKS the reply (like
`reserve`, `serverCLI.go:191`/`:913`) until >=1 event is buffered or the
hold-timeout elapses, then replies with the accumulated batch (empty on
timeout); the client immediately re-polls. The server opens NO new listening
port -- it still listens only on the pre-existing `Port` and `WebPort`; the new
connection is a CLIENT dial to the existing `Port`.

**Package:** `jobqueue/`
**File:** `jobqueue/subscription.go`, `jobqueue/serverCLI.go`
**Test file:** `jobqueue/subscription_test.go`

**Acceptance tests:**

1. Given a running server and a client connected on `Port`, when the client
   calls `SubscribeToJobKeys` with one valid key, then a non-nil
   `*Subscription` and nil error are returned, `Subscription.Err()` is nil, and
   the subscription opened its dedicated long-poll connection by dialling the
   SAME mangos `Port` as the primary connection (assert the dedicated socket's
   dialled address equals `c.ServerInfo.Addr`/the configured `Port`, i.e. the
   pre-existing mangos `Port`, not a new port and not `WebPort`).
2. Given a subscribed client, when a subscribed job transitions to
   `complete`, then a `*JobUpdate` with `Kind == JobUpdateTerminal`,
   `State == JobStateComplete`, and matching `Key` is received on
   `Updates()` within 1s of the transition (not poll-bound), because the parked
   `waitForUpdates` reply returns promptly once the event is buffered.
3. Given the server already listens on `Port` (mangos) and `WebPort` (web), when
   one or more subscriptions are active, then the process opens no additional
   listening socket or port beyond the pre-existing `Port` and `WebPort` (assert
   `ServerInfo` still exposes only `Port` and `WebPort`, and snapshot the
   manager's listening ports before and after `SubscribeToJobKeys` and assert
   equality; the subscription is one extra CLIENT dial to the already-existing
   `Port`, never a new server listener).
4. Given an existing program that connects with `jobqueue.Connect` and uses
   only `Add`/`Get*` and never subscribes, when it runs unchanged against the
   new manager, then its behaviour is identical (the feature is purely
   additive; the primary connection and `request()` path are unaffected).

### A2: Unauthorized client cannot subscribe

As an operator, I want token auth enforced, so unauthorized clients get
nothing.

Subscriptions reuse the SAME client-auth mechanism as every other client call:
the token is carried on `clientRequest.Token` (`cr.Token = c.token`,
`client.go:2038`) and checked server-side in `handleRequest`
(`serverCLI.go:58-61`: `len(cr.Token) != tokenLength ||
!tokenMatches(cr.Token, s.token)` -> `srerr = ErrPermissionDenied`).

**Acceptance tests:**

1. Given a client whose token is wrong (43-byte but mismatched), when it
   calls `SubscribeToJobKeys`, then the `subscribe` request is rejected
   server-side (`serverCLI.go:58-61`) before any subscription is registered,
   and the call returns an error whose string contains `ErrPermissionDenied`
   with no usable `*Subscription`.
2. Given a dedicated long-poll socket that dials the mangos `Port` and sends a
   `waitForUpdates` (or `subscribe`) request carrying a wrong `Token`, when the
   server handles it, then the request is rejected with `ErrPermissionDenied`
   (`serverCLI.go:58-61`), no subscription is registered, and the server
   buffers and replies with no `JobUpdate` even if a matching job later
   completes (assert the request errors and zero events are received within
   2s).

## B. Subscription semantics

### B1: Per-key terminal events

As a caller holding job keys, I want one terminal event per key, so I can
react per job.

Deliver one `JobUpdateTerminal` event per subscribed key when that job
reaches `complete` or `buried`. A `lost` transition delivers a
`JobUpdateLost` event (non-counting). `running`/`ready`/etc. transitions
deliver nothing.

**Package:** `jobqueue/`
**File:** `jobqueue/subscription.go`, `jobqueue/server.go`
**Test file:** `jobqueue/subscription_test.go`

**Acceptance tests:**

1. Given three subscribed keys for jobs that will end complete, buried, and
   complete, when all three finish, then exactly three
   `JobUpdateTerminal` events arrive with states
   `[complete, buried, complete]` (order-independent; assert by key).
2. Given a subscribed key for a job that goes `running` then `lost`, when it
   becomes lost, then exactly one `JobUpdateLost` event with
   `State == JobStateLost` and `FailReason == FailReasonLost` arrives, and
   no `JobUpdateTerminal` arrives while it stays lost.
3. Given a subscribed key, when the job passes through `reserved` and
   `running` before completing, then only the final `JobUpdateTerminal`
   (no intermediate events) is delivered.
4. Given a lost job that is subsequently buried (via the kill/confirm path),
   then after the `JobUpdateLost` event a later `JobUpdateTerminal` with
   `State == JobStateBuried` is delivered for the same key.

### B2: RepGroup aggregate fires once when all known jobs terminal

As a batch submitter, I want one event when my whole RepGroup is done.

Deliver a single `JobUpdateRepGroupDone` when every job currently known in
the group is `complete` or `buried`. A `lost` job holds the event back until
it settles (or ctx fires). No per-job terminal events for RepGroup
subscriptions. An empty group (no matching jobs, none arrive) never fires.
The aggregate carries counts and per-job key/state lists.

**Acceptance tests:**

1. Given a RepGroup with two jobs ending complete and buried, when both are
   terminal, then exactly one `JobUpdateRepGroupDone` arrives with
   `Complete == 1`, `Buried == 1`, `Lost == 0`, `Total == 2`, and `JobKeys`
   of length 2 with matching `JobStates`.
2. Given a RepGroup of two jobs where one becomes `lost` and stays lost,
   when the other completes, then no `JobUpdateRepGroupDone` arrives within
   2s (the lost job holds it back).
3. Given the lost job in (2) then settles to `buried`, then a single
   `JobUpdateRepGroupDone` arrives with `Buried` counting it and `Lost == 0`.
4. Given a `SubscribeToRepGroup` for a RepGroup with no matching jobs and
   none added, when the bound ctx deadline (200ms) fires, then `Updates()`
   closes with no `JobUpdateRepGroupDone` event, and `Err()` is ctx's
   deadline error.
5. Given a RepGroup subscription, when its jobs finish, then no
   `JobUpdateTerminal` per-job events are delivered (only the aggregate).

## C. Catch-up / late subscribe

### C1: Already-terminal jobs delivered immediately

As a caller, I want a job that finished just before I subscribed to still be
reported, so add-and-wait never hangs.

At subscribe time the server computes catch-up = live in-memory jobs matching
the scope, plus `retrieveCompleteJobsByKeys` / `retrieveCompleteJobsByRepGroup`
(`db.go:818`/`:878`) on the boltdb complete bucket for exactly the subscribed
keys/RepGroup. Bound: "still present in the complete bucket"; where a reused
key matches more than one historical completion, the most recent terminal
record wins. Only `complete`/`buried` records produce an immediate catch-up
event. The catch-up batch is returned SYNCHRONOUSLY in the `subscribe`
reply on the primary connection (in `serverResponse`), before any long-poll
event, so it never depends on a live transition. The client emits the catch-up
events on `Updates()` before draining the long-poll loop.

**Package:** `jobqueue/`
**File:** `jobqueue/serverCLI.go`, `jobqueue/subscription.go`
**Test file:** `jobqueue/serverCLI_test.go`

**Acceptance tests:**

1. Given a job that completed and was archived before subscribing, when
   `SubscribeToJobKeys` is called with its key, then a `JobUpdateTerminal`
   with `State == JobStateComplete` is delivered within 1s and `Updates()`
   does not hang.
2. Given a RepGroup whose every job already completed and archived before
   subscribing, when `SubscribeToRepGroup` is called, then exactly one
   `JobUpdateRepGroupDone` with correct counts is delivered.
3. Given a key that is still `running` at subscribe time (no complete-bucket
   record, live and in-progress), when subscribed, then no event is
   delivered until it later reaches a terminal state; then exactly one
   `JobUpdateTerminal` arrives (no in-progress snapshot event).
4. Given a key matching no live job and no complete-bucket record, when
   subscribed, then no event is delivered for that key (caller relies on
   ctx).

## D. Lifecycle, buffering, dedup, reconnect

### D1: Context-bound and explicit Unsubscribe teardown

As a caller, I want clean teardown via ctx or Unsubscribe.

**Package:** `jobqueue/`
**File:** `jobqueue/subscription.go`
**Test file:** `jobqueue/subscription_test.go`

**Acceptance tests:**

1. Given an active subscription, when `Unsubscribe()` is called, then
   `Updates()` is closed and `Err()` returns nil; the client sends the
   `unsubscribe` request and closes its dedicated long-poll socket, and the
   server's subscription registry no longer holds that subscription id (assert
   via a follow-up subscribe count or internal accessor).
2. Given a subscription bound to a ctx with a 100ms deadline and no events,
   when the deadline passes, then `Updates()` closes and `Err()` returns a
   `context.DeadlineExceeded`-derived error.
3. Given a subscription bound to a cancelable ctx, when `cancel()` is
   called, then `Updates()` closes and `Err()` returns
   `context.Canceled`.
4. Given an active subscription, when `Unsubscribe()` is called twice, then
   the second call does not panic and `Err()` stays nil.

### D2: Bounded, isolated, block-never-drop buffer

As an operator, I want a slow consumer to stall only itself and never lose a
terminal event.

Each subscription has its own bounded server-side event queue; when full the
callback's enqueue BLOCKS (back-pressure on that subscriber) rather than
dropping. One stuck subscriber must not stall the manager's
`SetChangedCallback` path or other subscribers' queues (per-subscriber
isolation). This is unlike the lossy `bcast` `statusCaster` path
(`server.go:1682`) the new path deliberately avoids.

**Acceptance tests:**

1. Given two subscriptions A and B and A's `Updates()` never drained, when
   many terminal events fire for both, then B receives all of its events
   within 1s each (A's back-pressure does not stall B).
2. Given subscription A whose consumer stops reading, when A's buffer fills,
   then no event destined for A is dropped: after A resumes reading it
   eventually receives every terminal event for its keys (assert final
   count equals number of subscribed terminal jobs).
3. Given a stalled subscriber A, when other jobs (unsubscribed) transition,
   then the server's main callback still processes them (assert an
   independent client's `GetByRepGroup` reflects those transitions within
   1s).

### D3: At-least-once delivery; dedup by key

As a consumer, I accept a possible duplicate during the
subscribe/catch-up race and dedup by key.

A job finishing during the subscribe handshake may appear in both catch-up
and live streams. AddAndWait dedups by counting distinct terminal keys.

**Acceptance tests:**

1. Given `SubscribeToJobKeys` for a key whose job completes concurrently
   with the subscribe call, when events are read, then at least one
   `JobUpdateTerminal` for that key is delivered, and if two arrive they
   have identical `Key` and `State` (duplicate, not contradiction).
2. Given `AddAndWait` for N jobs where one duplicate terminal event is
   injected, then `AddAndWait` still returns after exactly N distinct keys
   are terminal (the duplicate does not cause over- or under-count).

### D4: Reconnect keeps channel open with resync marker

As a caller, I want subscriptions to survive a manager restart without
silently missing terminal events.

On disconnect (the long-poll Send/Recv on the dedicated socket errors, or the
control call fails, because the manager went away) with a recoverable manager,
the client transparently RE-DIALS BOTH the primary and the dedicated long-poll
connection to the SAME `Port` (re-reading `c.ServerInfo.Addr` in case the
address changed across the restart), re-issues the `subscribe` request (which
re-runs catch-up and returns a fresh subscription id), resumes the long-poll
loop, and emits a `JobUpdateResync` event on the same `Updates()` channel. The
channel stays open and `Err()` stays nil. Only an unrecoverable disconnect
(reconnect retry budget exhausted) closes the channel with
`ErrSubscriptionClosed`. Because the server holds no cross-restart subscription
state (out of scope), the re-issued `subscribe` and its synchronous re-run
catch-up are what recover any terminal transitions that happened during the
gap.

**Acceptance tests:**

1. Given an active key subscription and a job that completes while the
   manager is briefly restarted, when the client reconnects, then a
   `JobUpdateResync` event is delivered, the channel stays open, `Err()` is
   nil, and the job's `JobUpdateTerminal` is delivered via re-run catch-up
   (terminal event not silently missed).
2. Given an active subscription, when the manager is permanently stopped and
   reconnect retries are exhausted, then `Updates()` closes and `Err()`
   returns `ErrSubscriptionClosed`.
3. Given a transient reconnect, when it succeeds, then `Err()` is NOT set
   to a fatal value during or after the resync (resync is surfaced only via
   `JobUpdateResync`).

## E. Add-and-wait (primary use case)

### E1: AddAndWait blocks until all added jobs terminal, returns []*Job

As a caller, I want to add N jobs and block until exactly those finish,
getting their terminal jobs back inline.

Built on `SubscribeToJobKeys` using the keys from the add. Unblocks once the
count of distinct terminal keys equals the number of added job keys.
Returns the full terminal `[]*Job` (re-fetched via existing `Get*` so exit
code and stdout/stderr are inline). A mix of complete and buried is not a Go
error. Catch-up ensures a job that finished between add and subscribe is
counted.

**Package:** `jobqueue/`
**File:** `jobqueue/subscription.go`
**Test file:** `jobqueue/subscription_test.go`

**Acceptance tests:**

1. Given three jobs added via `AddAndWait` that end complete, buried,
   complete, when it returns, then error is nil, the returned slice has 3
   jobs, and their states are `[complete, buried, complete]` (by key), with
   `Exitcode` populated.
2. Given a single job added via `AddAndWait` that completes with exit code
   0, when it returns, then error is nil, the one returned job has
   `State == JobStateComplete` and `Exitcode == 0`.
3. Given a single job that fails, when `AddAndWait` returns, then error is
   nil and the returned job has `State == JobStateBuried` with its non-zero
   `Exitcode` preserved.
4. Given a job that completes microseconds before the internal subscribe,
   when `AddAndWait` runs, then it still returns (catch-up), not hangs.
5. Given two added jobs where one stays `lost` past a 200ms ctx deadline,
   when `AddAndWait` returns, then it returns the jobs gathered so far plus
   a non-nil error that is `context.DeadlineExceeded`-derived and names the
   unfinished key(s).
6. Given a `JobUpdateLost` event for an added job that later completes
   within ctx, when `AddAndWait` runs, then the lost event does not count
   toward completion and the call returns only after the true terminal
   event (no early/incorrect return).

### E2: cmd/add.go --sync uses AddAndWait

As a wr user, I want `wr add --sync` to react at actual completion, not on a
poll interval, with unchanged exit behaviour.

Replace `waitForJobCompletion`/`getJob` poll loop (`cmd/add.go:942`) with
`AddAndWait` on the single added job. Print head/tail stdout then stderr,
then `os.Exit(job.Exitcode)` exactly as today (`synchronousAdd`,
`cmd/add.go:924`).

**Package:** `cmd/`
**File:** `cmd/add.go`
**Test file:** `cmd/add_test.go`

**Acceptance tests:**

1. Given `wr add --sync` of a command that exits 0 and prints to stdout,
   when it finishes, then the CLI prints that stdout and exits 0.
2. Given `wr add --sync` of a command that exits 3, when the job is buried,
   then the CLI exits with code 3 (today's behaviour preserved).
3. Given `wr add --sync`, when run, then `cmd/add.go` contains no polling
   loop over `GetByEssence` for sync completion (the push path replaces it;
   assert by absence of `waitForJobCompletion`).

## F. No regression to browser updates

### F1: Single source of state-change events

As a maintainer, I want both the browser `/status_ws` stream and the new
push subscriptions driven by the one `SetChangedCallback`, with no second
divergent path.

**Package:** `jobqueue/`
**File:** `jobqueue/server.go`
**Test file:** `jobqueue/jobqueue_test.go`

**Acceptance tests:**

1. Given a browser-style websocket subscribed to a RepGroup and a Go-client
   push subscription on the same jobs, when the jobs complete, then both
   receive their updates (existing `JStatus IsPushUpdate` path still works;
   assert websocket still gets a `JStatus` with `IsPushUpdate == true`).
2. Given the `SetChangedCallback` body, when reviewed, then both the
   `statusCaster`/websocket emission and the new push emission derive from
   the same per-job data computed once in the callback (no second
   independent traversal feeding only one consumer).

## Implementation Order

1. **Server subscription registry + long-poll handlers (A, no regression
   scaffolding).** Add the `subscribe` (key set or RepGroup, returns sub id +
   synchronous catch-up batch), `unsubscribe`, and parked `waitForUpdates`
   `Method`s to the `handleRequest` dispatch (`serverCLI.go:67`), reusing the
   existing client-auth check (`serverCLI.go:58-61`) and the `reserve`
   parked-reply pattern (`serverCLI.go:191`/`:913`). Add the server-side
   subscription registry plus a bounded, per-subscriber event queue, fed
   DIRECTLY from `SetChangedCallback` (`server.go:1623`, the single source),
   leaving the browser `jobSubscriptions`/`statusCaster` path untouched. No new
   listening socket/port. Tests: A1, A2, F1.
2. **Client Subscription + per-key terminal/lost (B1, D1).** `Subscription`,
   `JobUpdate`, `SubscribeToJobKeys`, channel/Err/Unsubscribe, ctx binding.
   Tests: B1, D1.
3. **Catch-up (C).** Live + complete-bucket lookup returned in subscribe
   response; most-recent-record-wins. Tests: C1.
4. **RepGroup subscription + aggregate (B2).** Built on registry; lost holds
   back; empty never fires. Tests: B2.
5. **Buffering/isolation + dedup (D2, D3).** Per-subscriber buffer,
   block-never-drop, isolation. Tests: D2, D3.
6. **Reconnect/resync (D4).** Transparent re-dial + re-subscribe + resync
   marker; unrecoverable sentinel. Tests: D4.
7. **AddAndWait (E1).** Built on key subscription; dedup by key; bounded-wait
   escape. Tests: E1.
8. **cmd/add.go rewrite (E2) + browser-regression check (F1).** Tests: E2,
   F1.

Phases 1-7 are sequential (each builds on the prior). Phase 8 depends on 7.

## Appendix: Key Decisions

- **Long-poll over the existing mangos `Port` (not the websocket, not
  pub/sub).** The transport is a long-poll over the client's EXISTING mangos
  `req`/`rep` endpoint on `Port` -- same endpoint, host, token and TLS the
  client already holds. The server's `rep` socket is ALREADY raw mode
  (`server.go:620`) with goroutine-per-request dispatch (`server.go:1010`), and
  the codebase ALREADY parks a reply for seconds in exactly this shape: the
  blocking `reserve` -> `reserveWithLimits` -> `s.q.Reserve(group, wait)`
  (`serverCLI.go:191`/`:913`). The parked `waitForUpdates` handler is that same
  proven pattern (one blocked goroutine per subscribed client), not a new
  mechanism.
  - **Why not the `/status_ws` websocket:** it is provably unreliable for a
    never-drop guarantee. It depends on `grafov/bcast` pinned to a 2016 commit
    (`go.mod` `replace ... => ...e9affb593f6c` + the warning at
    `server.go:50`: "must be commit e9affb593f6c... or status web page updates
    break in certain cases"); its aggregate `statusCaster.Send` path is lossy
    with its error ignored (`server.go:1682`); and there is a
    join-after-connect race (`serverWebI.go:420`, the `caster.Join()` in
    `setupUpdateListener` happens after the connection is live) where events can
    be missed. Building "block, never drop terminal" on that substrate would
    mean hardening a known-broken, abandoned dependency.
  - **Why not mangos `pub`/`sub`:** each mangos socket binds one protocol, so a
    `pub`/`sub` pair cannot share `Port` and would force a forbidden SECOND
    listening port.
  - The client uses its EXISTING primary connection for the quick
    subscribe/unsubscribe control calls (subscribe returns a sub id + the
    synchronous catch-up batch) and opens ONE dedicated second cooked
    `req`/`rep` socket to the SAME `Port` (required because the primary socket
    serialises all calls under its mutex, `client.go:2032`) for the long-poll
    loop. The hold-timeout is kept safely under the cooked-`req` resend
    interval (~25-30s). The new method is purely additive/opt-in: upgrading is a
    go.mod bump + recompile, no new config, no new credential, no new listening
    port. wr is pinned to the deprecated `nanomsg.org/go-mangos v1.4.0`; a
    future `go.nanomsg.org/mangos/v3` migration and any deeper transport rework
    are explicitly out of scope.
- **Terminal = complete|buried only.** `lost` is provisional in wr (TTR
  callback sets `Job.Lost`; the kill/confirm path,
  `confirmJobDeadAndKill`/`releaseJob`, later settles it to buried/complete).
  So `lost` is informational and non-counting. The bounded-wait escape (ctx
  deadline) prevents AddAndWait hanging on a permanently-lost job; the error
  names the unfinished keys.
- **Catch-up bound.** "Still in the complete bucket"
  (`retrieveCompleteJobsByKeys`/`...ByRepGroup`), most-recent record wins for
  reused keys; no separate recent-set ring. Only complete/buried records fire
  immediate events; in-progress jobs get no snapshot.
- **Block-never-drop, isolated.** Completion events are low-volume; each
  subscription's own bounded server-side event queue applies back-pressure (the
  callback's enqueue blocks) rather than dropping, keeping a stuck consumer from
  affecting the manager callback or peers. This deliberately contrasts with the
  lossy `bcast` `statusCaster` path (`server.go:1682`) the new path avoids.
- **At-least-once + dedup-by-key.** The subscribe/catch-up vs live race may
  duplicate; AddAndWait counts distinct keys so duplicates never miscount.
- **Resync, not failure.** Reconnect keeps the channel open, re-runs
  catch-up, and emits `JobUpdateResync`; `Err()` reserves fatal status for
  unrecoverable disconnect (`ErrSubscriptionClosed`), clean unsubscribe
  (nil), and ctx cancel/deadline (ctx error).
- **Single callback source.** The existing `SetChangedCallback`
  (`server.go:1623`) computes per-job data once and routes to both the
  browser `statusCaster`/websocket path (untouched) and the new per-subscriber
  queues; no parallel notification mechanism. Two independent delivery paths,
  one source.

**Testing strategy:** GoConvey (`So(...)`); real server+client via the
existing test harness (`startServer`, in-process `Serve`), `t.TempDir()` for
DB. Time-bounded assertions use a select with a timeout channel, not
sleeps-then-check. For D2 isolation, assert final event counts (not per-event
`So` in large loops). Lost-state tests reuse the existing TTR-shortening
pattern (`ServerItemTTR`, `ServerLostJobCheckTimeout` overrides in
`jobqueue_test.go`). Implementors follow **go-implementor**; reviewers follow
**go-reviewer**; all per **go-conventions**.
