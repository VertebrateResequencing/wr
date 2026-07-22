# reliable2 phase 2 — fix ideas + "was not reverting the web front end a mistake?"

Companion to `repro.md`. Ordered roughly by leverage / independence.

---

## Was not reverting the web front end a mistake?

**Short answer: yes for correctness (Issue 4), and it was a meaningful
contributor to the performance issues (Issues 1–3) — but a front-end revert
alone would not fix the deepest churn (Issue B) or the two independent bugs
(giant array, 24h livelock).**

Breakdown:

- **Issue 4 (completed-count divergence): a mistake, and the direct cause.**
  This bug exists *only* because the server-side counter machinery
  (`repGroupCounts` + the `+all+` aggregate + the per-job subscription
  subsystem), which feeds the unchanged front end, was kept. v0.36.5 had no
  server-side count map to drift. Reverting the web front end to v0.36.5 (scan-
  based counts, local delta broadcast) removes the divergence outright. Keeping
  it means you must instead *fix and keep correct* an accumulating counter — a
  strictly harder invariant that is currently violated (0 vs 207).

- **Issues 1–3 (unresponsiveness): a contributor, not the sole cause.**
  Keeping the front end kept two things on the hot path that lower the
  saturation threshold: (a) `repGroupCounts.applyTransitions` on *every*
  transition, unconditionally; (b) the **single** change-callback drainer
  (`queue/queue.go:263`) that replaced v0.36.5's per-batch concurrent
  goroutines, made worse by `hasAnyClientSubscriptions` defeating the idle
  fast-path whenever the UI is open. Removing/【fixing】these raises the threshold
  back toward v0.36.5. **But** the ultimate ceiling is the single
  `sock.RecvMsg()` reader (Idea 2 in the original investigation, deferred out of
  scope), which a front-end revert does not touch.

- **Issue B (completion churn) and the two independent bugs: a revert would not
  fix them.** The archive/release rejection is ~identical to v0.36.5; the
  front-end machinery only changes *when* it triggers (threshold), not
  *whether*. The giant-array hang and the 24h `not running` retry-livelock are
  entirely independent of the front end.

**Recommendation:** either (i) fully revert the web front end to v0.36.5 (kills
Issue 4, raises the threshold, simplest), accepting the loss of the accurate
absolute per-repgroup counts feature; or (ii) keep the front end but treat its
machinery as a first-class correctness+performance surface — seed the counter
from DB history and stop omitting terminal repgroups (Idea 6), make the counter
work truly conditional on a connected UI, and restore concurrent
(non-serialized) callback delivery. Given the project's
"internal-only, no user-facing change" rule and that the counts feature is
user-visible, (ii) is more in-keeping — but (i) is the honest low-risk choice if
the counts feature is not worth this cost. This is a genuine product decision,
not purely technical.

---

## Idea 0 (prerequisite) — cap/chunk the LSF bsub array + bsub timeout

Already written up as `.docs/bugfixes/260722-1.md`. Cap the emitted array to a
safe max and submit multiple arrays; add a timeout to the `bsub` exec; back off
the infinite same-count retry. Without this, any large same-requirements batch
(the exact "simple immediate commands" repro) is unschedulable. Independent of
everything below.

## Idea 1 — break the failed-job release livelock (small, high value, independent)

`false`/failed jobs pin runners for 24h retrying a `not running` release. Two
minimal options:
- Make `handleRelease` (and `handleArchive`) return **`ErrBadJob`** (not
  `ErrInternalError`) when the item is not in Run — `handleRelease` currently
  calls `getij(cr, false)`; using `checkRunning=true`, or mapping the queue
  `ErrNotRunning` to `ErrBadJob` at the reply, puts it in the client's give-up
  set (`client.go:2118-2131`) so the runner **abandons the dead job and reserves
  the next one** instead of looping. This alone restores forward progress for
  failing workloads.
- Or bound `reportFinalState` retries (`retryTime=24h` is far too long for a
  final-state RPC) and stop the disconnect-per-retry churn.
Add a regression test: instant-fail job whose item left Run → runner gives up
and progresses, no 24h loop.

## Idea 2 — stop the double-reservation that causes Issue B (the real root cause)

Diagnosis complete (was "Idea 3"): the `bad job`/`not running` rejects are the
losing half of a **double-reserved** job — the same command handed to two
runners. So the correct fix is to **prevent the double reservation**, not
(as an earlier draft proposed) to "accept the loser's late success" — for
succeeding jobs the winner already completes the job (the loser's archive is a
harmless duplicate; a clean 40k run completed 40,000/40,000), so there is no
lost success to rescue; the harm is **wasted double-execution** (catastrophic
for the multi-minute real workload) plus the failing-job livelock (Idea 1).

Two independent causes to fix (see repro.md Issue B for the traced detail):

- **(a) A timed-out *reserved-but-not-started* item is requeued on a
  `StartTime.IsZero()` "death proxy" — with no liveness check.** The item's TTR
  is armed at Reserve (`queue.Reserve → item.touch()`, `queue/queue.go:1509`);
  when it fires, `ttrCallback` takes the `job.StartTime.IsZero()` branch and
  returns `SubQueueDelay` (`server.go:3352-3356`) → delay → ready →
  re-reservable. But `StartTime.IsZero()` only means "the `Started` RPC has not
  been *processed* yet", which under the single-reader backlog is **not** the
  same as "the client died" — so a live-but-backlogged runner's job is handed to
  a second runner.

  **The fix must keep a reclaim timer** (a client really can die between Reserve
  and Started — e.g. LSF `bkill`, node failure — and that reservation must be
  reclaimable, or the job is stuck in Run forever). So do **not** simply move the
  TTR to `Started`; that reintroduces the hole. Instead, **base the requeue on
  confirmed absence of the reserving runner, not on `StartTime`**, giving the
  reserved-not-started path the same "park-and-confirm-dead" treatment the
  started path already gets:
  - Have the runner report its **host+pid at Reserve** (it knows them
    immediately, before running anything), so the reserved-not-started TTR path
    can confirm death via the scheduler (`ProcessNotRunningOnHost` / LSF `bjobs`)
    exactly as the started path does. On TTR: mark Lost, **park in Run
    (un-reservable by others)**, confirm dead; requeue only if confirmed dead
    (→ no hole), keep if alive (→ no double-reservation). This liveness signal is
    independent of the backlogged RPC stream.
  - Complementary/alternative: restore an **F0-style contact timestamp** recorded
    at message-receive time (reliable2 removed it) and refuse to requeue a
    reserved item contacted within the window — the client already touches from
    Reserve (`client.go:1420-1427`, ticker starts *before* `Started`), so a live
    runner's touches prove liveness if they are drained in time.
  - Either way, extreme backlog can still starve the liveness signal itself —
    that residual is the single-reader ceiling (Idea 5), which this fix should be
    paired with.
- **(b) `checkCmd` bkill race** (`lsf.go:1177-1201`, race documented in-code):
  wr over-submits (`[1-1000]` per group) then `bkill`s "excess non-RUN" runners;
  a PEND→RUN element can be killed mid-job (a clean 40k run bkilled 38,302 of
  ~40k elements), and a killed busy runner's work is wasted and its job re-run.
  This is hole-free to fix and is likely the **dominant** real-farm driver at
  modest scale (where the reader is not 60s-backlogged): don't over-submit so
  aggressively (relates to the array-cap work, Idea 0), and re-check `RUN`
  immediately before `bkill` (drop any now-running), or track which submitted
  elements wr has already handed a reservation and never kill those.

Note (a) and (b) compose: (b) stops wr wasting work by killing its own busy
runners; (a) makes any genuine reclaim liveness-confirmed so it is never a
double-reservation and never a stuck job.

Re-validate on **real LSF** (not just the in-process harness — it uses
`os.Getpid()` live processes and a TTR above the backlog, and so passed M2=0
while real LSF still churned). Success metric: near-zero `bad job` **and**
near-zero double-execution (each command runs once) at a few thousand runners,
with a deliberately-killed reserved-not-started runner's job still reclaimed.

## Idea 4 — raise the saturation threshold (the front-end contribution to Issues 1–3)

- Replace the single change-callback drainer (`queue/queue.go:263`) with
  concurrent/fan-out delivery as in v0.36.5, or at least move counter/
  subscription work off the drainer's critical path.
- Make `repGroupCounts.applyTransitions` a true no-op when no web UI is
  connected, and fix `hasAnyClientSubscriptions` so the status-web-UI
  subscription does not defeat the per-job idle fast-path
  (`server_subscription.go:532`, `jobtransition.go:200`).
Either way, or a full front-end revert, restores headroom.

## Idea 5 — the real ceiling: decouple the single RPC reader (the original investigation's deferred "Idea 2")

`serveClients → receiveClientMessage → sock.RecvMsg()` (`server.go:2656/2671`)
admits one message at a time. Even with per-request goroutine handling, this
caps how fast control RPCs are *admitted* under a fleet storm, so `wr status`/
`suspend`/`limit` queue. This was consciously left out of scope; it is what
actually stands between "a few hundred" and "thousands" of responsive runners.
Consider a separate listener/socket for control+status RPCs so they can never
queue behind the runner fleet.

## Idea 6 — fix the completed-count divergence (Issue 4)

Reproduced end-to-end (repro.md Issue 4): the web UI's `repGroupCounts` counter
disagrees with the DB/CLI on completed counts because it has **two** history
gaps, both in the retained counter machinery:
- it is **never seeded from DB history** (`serverWebI.go:930` comment), so a
  manager restart drops all prior completes — the web UI then shows only
  completes from the current run while the CLI scan shows all; and
- `liveSeedLocked`/`rgcSeedCountCopy` **omit terminal-only repgroups** from a
  fresh subscriber's seed (`complete` is not counted as "live" by
  `rgcHasLiveJob`), so a freshly-loaded web UI shows 0 for a fully-completed
  repgroup.

Options: (a) **revert the web front end** → the divergence disappears (CLI-style
scan/local-delta, v0.36.5); or (b) keep the counter but **seed it from the DB on
startup** (the per-repgroup complete totals) and **include complete in the seed
for repgroups that have DB completes even when currently terminal-only** — i.e.
make the counter a faithful mirror of the DB, not a live-only accumulator. Add a
regression test: complete N jobs in `rg`, restart preserving the DB, add M live
jobs to `rg`, assert the `/status_ws` `jstateAbsolute` complete count for `rg`
== the DB/CLI per-repgroup complete count (N, then N+M as they finish). Note
this is a strictly harder invariant than v0.36.5 had — hence the front-end
question below leans toward (a) unless the accurate-counts feature is worth it.

---

## Suggested sequence

1. Idea 0 (bugfix 260722-1) — unblocks large same-req batches.
2. Idea 1 — stops the failing-job livelock (biggest forward-progress win, tiny).
3. Idea 2 — stop double-reservation (TTR-at-Started + `checkCmd` race), killing
   the wasted double-execution behind Issue B; re-validate on real LSF.
4. Decide the front-end question → Idea 6 (+ Idea 4) or a full revert.
5. Idea 5 (single-reader decouple) if "thousands responsive" is required.
