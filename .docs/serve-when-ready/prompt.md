# Serve clients only when the manager is fully ready

Input for the `/spec-writer` workflow. This is a "changing fundamentals" change
(it reverses the reliable2 spec-B1 background-recovery decision), so it gets a
spec rather than a `/bugfix`. It SUPERSEDES the narrow "4b" per-RPC recovery
gating described in `.docs/reliable4/freeze-fix-plan.md`.

## Feature

On (re)start, the wr manager must NOT serve clients (the RPC API and the status
web UI) until it is FULLY READY — i.e. prior-state recovery is complete and the
in-memory queue + status reflect the true persisted state. `wr manager start`
waits for that readiness, reporting PROGRESS, and fails cleanly (message +
non-zero exit) on timeout or recovery error — it must never hang forever.

## Problem being fixed (current behaviour)

`Serve()` opens the client listeners and returns immediately; recovery of prior
jobs then runs in a BACKGROUND goroutine (`startPriorStateRecovery` →
`go recoverInBackground`, server.go:1280; spec B1). `wr manager start` waits only
for the "started on" listener log + a connectable token (cmd/manager.go), NOT for
recovery. So for the recovery window (tens of seconds on a large live backlog)
clients observe a half-initialised manager. Three operator-facing symptoms, one
root cause:

1. The status web UI shows NO jobs (or a partial set) — alarming, sometimes for
   tens of seconds after a restart.
2. Control RPCs `jsuspend`/`jresume` (serverCLI.go:1696/1707) operate on the
   not-yet-restored queue and SILENTLY no-op (`resumeJobs` returns 0; a prior job
   isn't in the queue yet, `suspendJob`/`resumeJob` return false at
   server.go:1812) — the operator's suspend/resume is lost. (The runner report
   paths `getij`/`getijForReport` DO gate with retryable `ErrRecovering` at
   serverCLI.go:1768/1825; the control paths do not. This is "4b".)
3. On first web load, completed jobs are missing for RepGroups that also have
   incomplete jobs, until the operator refreshes the page.

## Why this is safe/acceptable now (the tension to resolve explicitly)

Background recovery + the `ErrRecovering` window were introduced because restarts
took ~190s — but that was the COLD-SCAN of completed history for status seeding
(#547), already removed (spec C2: "startup must not scale with completed-job
count"). The remaining recovery only re-enqueues the LIVE/incomplete jobs (a cheap
live-bucket scan feeds `startPriorStateRecovery`), i.e. O(live) — the "tens of
seconds". Waiting for it before serving:
- PRESERVES C2: startup stays O(live), never O(completed). The C2 guard test
  (`TestReliable2FastStartupNoHistoryScan`, completed-only jobs → trivial
  recovery) must still pass.
- Trades "responsive but half-ready" for "comes up a little later, fully correct".
- Likely lets the `ErrRecovering` window machinery be removed/simplified, since no
  client can observe a mid-recovery state.

## User stories (for the spec author)

- As an operator restarting the manager, when I open the web UI after
  `wr manager start` returns, I immediately see the full, correct state (all live
  jobs and the correct completed jobs per RepGroup) with NO page refresh needed —
  never a blank/half bar.
- As an operator, `wr manager start` shows me recovery progress (e.g. "recovering
  N/M jobs") while it waits, so a slow restart looks like progress, not a hang.
- As an operator, if recovery errors or the manager process dies, `wr manager
  start` tells me and exits non-zero within a bounded time — it never waits
  forever.
- As an operator, a `wr suspend`/`wr resume`/`wr limit` issued as soon as
  `wr manager start` returns takes effect (never a silent no-op).
- As a surviving runner reconnecting after a manager restart, my re-sent archive
  is still accepted (crash-recovery guarantee preserved) — I simply retry the
  connection until the manager is ready (as I already do).

## Constraints / invariants (MUST hold)

- C2: startup MUST NOT scale with completed-job history (no cold-scan). Recovery
  stays O(live). Keep `TestReliable2FastStartupNoHistoryScan` green.
- Crash-recovery archive acceptance (DEVELOPERS.md §1): a runner that survived the
  restart must still have its re-sent archive accepted. Serve-when-ready satisfies
  this trivially (recovery completes before any client — including reconnecting
  runners — is served); do NOT regress it.
- Readiness time is O(live) and could be long on a huge live backlog: it MUST be
  observable (progress) and the CLI wait MUST be bounded (timeout + clean failure,
  never hang). The CLI already has `monitorManagerStartupProcess` (process-exit
  detection) + a connect deadline to build on; `recoveryProgress()` already
  exposes restored/total.
- Don't reintroduce a slow startup for the common small-live-backlog case.
- Preserve the reliable4 fixes already landed (best-effort write coalescing;
  confirm-dead grouping) — this is orthogonal.

## Acceptance tests (the spec should make these concrete)

1. During recovery, no client can observe a half-state: either it cannot connect
   yet (CLI still waiting, showing progress) or it is told the manager is
   starting; and suspend/resume/limit issued right after `start` returns take
   effect (the 4b silent no-op is gone by construction). (A fast test can use the
   existing `recoveryPauseHookForTest`, server.go:248, to hold the window.)
2. After `wr manager start` returns success, the first web-UI status payload shows
   the full correct state — all live jobs AND the correct completed jobs for
   RepGroups that have incomplete jobs — with no refresh. (Reproduce the
   completed-jobs-missing bug first; root-cause it — recovery-ordering vs a
   separate initial-payload assembly bug — and cover whichever it is.)
3. `wr manager start` reports progress and, on a stalled/failed recovery, exits
   non-zero with a clear message within a bounded time (never hangs).
4. C2 fast-startup test still passes (startup independent of completed-job count).

## Open design questions for the spec

- Listener strategy during recovery: (a) don't open the client listeners until
  ready (clients get connection-refused; the CLI keeps waiting with progress),
  vs (b) open them but serve a minimal "manager is recovering, N/M restored"
  response/page (nicer for a browser), vs (c) a lightweight readiness/health
  endpoint up early with the real endpoints gated. Choose one.
- Where readiness is signalled: make `Serve()` block until recovery completes
  (simplest — "started on" then means ready; but `Serve()` is used by many tests,
  so confirm they tolerate the wait / adjust them without weakening C2), vs a
  separate readiness signal the CLI polls.
- Fate of the `ErrRecovering` window machinery (spec B1: `getij`/`getijForReport`
  gating, the recovery window tests): remove it, or keep as a belt-and-braces
  safety net?
- The completed-jobs-until-refresh bug: is it recovery-ordering (fixed by
  serve-when-ready) or a distinct initial-web-payload assembly bug? Root-cause
  before assuming.

## Supersedes

The narrow "4b" (per-RPC `ErrRecovering` gating of suspend/resume/limit) in
`.docs/reliable4/freeze-fix-plan.md` — close that gap via this readiness model
instead of per-RPC gating.
