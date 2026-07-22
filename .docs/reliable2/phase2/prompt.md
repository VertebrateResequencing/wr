# Spec input (phase 2): fully revert the web front end, and fix the double-reservation churn, the failed-job release livelock, and the single-reader unresponsiveness

## Goal & priority

Phase 1 (`.docs/reliable2/`, "Option R", already merged into branch
`reliable2`) fixed false-lost / false-deleted and startup, but a real
`portal_builder` deployment surfaced **remaining** problems that would not have
happened on v0.36.5. A fresh isolated-LSF reinvestigation (full diagnosis in
`.docs/reliable2/phase2/repro.md` and `ideas.md`, with a reusable `/status_ws`
client in `.docs/reliable2/phase2/wsprobe/`) reproduced them and identified root
causes. This spec implements the chosen fixes.

Reliable job execution remains the top priority; web-UI count *accuracy* is
explicitly secondary and may revert to v0.36.5 quality (indeed this spec removes
the machinery that tried to improve it).

**Four changes are wanted, in roughly this order:**

1. A **true web-front-end revert** to v0.36.5 (this moots the "keep-and-fix"
   ideas 4 and 6 from `ideas.md`).
2. **Idea 1** — stop the failed-job release livelock, **while keeping the 24h
   client retry** (it exists for manager-crash recovery — see below).
3. **Idea 2** — prevent the double reservation that is the root of the
   completion churn.
4. **Idea 5** — decouple the single RPC reader so status/control commands stay
   responsive under a large runner fleet.

## Baseline (what is already done)

- Branch `reliable2` (HEAD `7229449`) = `develop` + `#547`/`#548`/`#550` +
  Option R + the `queues_avoid` client fix (`.docs/bugfixes/260720-1.md`). Keep
  all of that.
- v0.36.5 = commit `11fe092` (the revert target for the front-end pieces).
- The full evidence, code trace (file:line), and per-idea reasoning are in
  `.docs/reliable2/phase2/repro.md` and `.docs/reliable2/phase2/ideas.md`. The
  spec author should read those; this prompt summarises the decisions.

---

## The desired solution

### 1. True web front-end revert to v0.36.5 (moots ideas 4 & 6)

Option R kept a slimmed per-RepGroup absolute counter to feed the (unchanged)
web front end. That retained counter machinery is the direct cause of the
completed-count divergence (repro.md Issue 4) **and** a load-bearing contributor
to the unresponsiveness (it adds per-transition work and serialises all
transition callbacks). **Revert the web front end — server machinery and page —
to v0.36.5**, accepting v0.36.5-quality web-UI counts.

Concretely (spec author to confirm exact surgery against `11fe092`):
- Remove the `#503`/`#533`/`#547` absolute-count + subscription machinery:
  `repGroupCounts` (`server.go:759`, init `server.go:2487`), `repgroupcounts.go`,
  `jobtransition.go` (`emitJobTransition`/`applyTransitions`),
  `server_subscription.go`, the `jstateAbsolute` message (`server.go:504`) and
  its `/status_ws` push (`serverWebI.go:935-990`).
- **Restore v0.36.5's change-callback dispatch**: v0.36.5 fans out each
  transition batch on a fresh goroutine (`go queue.changedCb(...)`,
  `11fe092:queue/queue.go:330`). The current single serial drainer
  `runChangedCallbacks` (`queue/queue.go:263`) was introduced by `#547`
  (`f7894b4`) for the counter machinery; with that machinery gone, restore the
  concurrent dispatch. (This is the specific thing that "raising the threshold"
  in idea 4 was about — it comes for free with a true revert.)
- Restore the v0.36.5 web page/asset behaviour for status display (counts the
  v0.36.5 way). CLI `wr status` stays scan-based (unchanged).

Because this removes the counter entirely, **idea 6 (fix the count divergence)
and idea 4 (make the counter conditional / de-serialise the drainer) are moot**
and must NOT be implemented — the revert subsumes both.

Acceptance: no `repGroupCounts`/`jstateAbsolute`/`runChangedCallbacks` in the
build; change-callbacks dispatched concurrently as in v0.36.5; the count
divergence in repro.md Issue 4 no longer reproduces (build `wsprobe` from
`.docs/reliable2/phase2/wsprobe/` and confirm the web `/status_ws` counts match
the CLI/DB, or that the endpoint no longer exists in its current form).

### 2. Idea 1 — stop the failed-job release livelock, keeping the 24h retry

Today a job that exits non-zero sends `jrelease`; if its queue item is no longer
in the Run sub-queue, `handleRelease` (`serverCLI.go:1116-1134`, via
`getij(cr, false)`) returns `ErrInternalError` (queue `ErrNotRunning`,
`queue/queue.go:1596-1602`). The client's `reportFinalState`
(`client.go:2084-2131`) only treats `ErrBadJob`/`ErrBadRequest` as give-up, so
`ErrInternalError` is retried for the full `retryTime` (24h), disconnecting and
reconnecting every 15s, pinning the runner so it never reserves again →
throughput collapse (repro.md Issue B2).

**Fix:** make a **live manager that authoritatively reports the reservation is
gone** return `ErrBadJob` (or otherwise land in the client give-up set) for the
not-in-Run release — mirroring `handleArchive`, which already returns `ErrBadJob`
via `getij(cr, true)`. Then the runner **abandons the dead reservation and
reserves the next job** instead of looping.

**Crucial constraint — keep `retryTime = 24h`.** That long retry exists for a
different, important case: a job that **completed successfully while the manager
was crashed** must be able to **record its success when the manager restarts
within a day**, so an expensive command is not needlessly re-run just because of
a manager crash. The distinction the fix must preserve:
- **Manager up, says "gone" (`ErrBadJob`)** → the reservation really is
  superseded (the double-reservation winner already recorded it, or it was
  reclaimed) → **give up promptly**, no 24h loop.
- **Manager unreachable (crash/restart)** → a *connection* error, **not**
  `ErrBadJob` → keep retrying up to `retryTime`; on restart within a day,
  re-send and record the outcome. **Do not shorten `retryTime` to fix the
  livelock.**

Design caveat for the spec author: the `ErrBadJob` give-up must **not discard a
genuine, unrecorded success**. After a crash+restart, a still-running job must be
recovered into the Run sub-queue (the KEEP'd recovery window,
`recoverInBackground`) so the re-sent archive/release succeeds; only a genuinely
superseded reservation should receive `ErrBadJob`. This is what makes give-up
safe. (Idea 2 below further reduces how often a live manager sees a not-in-Run
release at all.)

Acceptance:
- Live manager, item not in Run, runner reports a failed command → runner gives
  up promptly and proceeds to its next reserve; no `jrelease ... not running`
  retry storm, no 24h/15s reconnect loop.
- Manager killed while a genuine **success** is being reported, restarted within
  `retryTime` → the job is recorded `complete` and the (expensive) command is
  **not** re-run.
- `retryTime` remains 24h (unchanged).

### 3. Idea 2 — prevent the double reservation (root of the churn)

The `bad job`/`not running` rejects are the losing half of a command handed to
**two** runners (repro.md Issue B). For succeeding jobs the loser's reject is a
harmless duplicate (the winner completed it — verified 40k/40k), but the
**wasted double-execution** is catastrophic for multi-minute real commands; for
failing jobs it drives the Issue-B2 livelock. Fix the two independent causes,
**without leaving a reclaim hole**:

- **(a) Reserved-but-not-started TTR uses a `StartTime.IsZero()` death proxy.**
  The item TTR is armed at Reserve (`queue.Reserve → item.touch()`,
  `queue/queue.go:1509`); on expiry `ttrCallback` takes the
  `job.StartTime.IsZero()` branch and requeues to delay→ready with **no liveness
  check** (`server.go:3352-3356`) → a live-but-backlogged runner's job is
  re-reserved. **Do NOT fix this by arming the TTR at `Started`** — that leaves a
  hole (a client that dies between Reserve and Started would never be reclaimed
  and the job would stick in Run forever). Instead **keep a reclaim timer but
  make the requeue liveness-confirmed, not `StartTime`-based**: give the
  reserved-not-started path the same "park in Run (un-reservable) + confirm the
  runner is actually dead, then requeue" treatment the started path already has
  (`markJobLost`/`confirmOrReleaseLostJob`). To confirm death pre-`Started`, have
  the runner **report host+pid at Reserve** (it knows them immediately) so the
  scheduler check (`ProcessNotRunningOnHost`/LSF `bjobs`) works — a signal
  independent of the backlogged RPC stream. (An F0-style contact timestamp
  recorded at message-receive time, which reliable2 removed, is an acceptable
  complementary signal; the client already touches from Reserve,
  `client.go:1420-1427`.)
- **(b) `checkCmd` bkill race** (`lsf.go:1177-1201`, race documented in-code):
  wr over-submits runners then `bkill`s the "excess non-RUN"; a PEND→RUN element
  that just reserved+started a job can be killed mid-job (a clean 40k run bkilled
  38,302 of ~40k elements). Fix hole-free: re-check `RUN` immediately before
  `bkill` (drop any now-running), and/or don't over-submit so aggressively
  (relates to the array-size work tracked separately, see Out of scope). This is
  likely the dominant real-farm trigger at modest scale.

Acceptance (re-validate on **real LSF**, not only the in-process harness — the
harness uses `os.Getpid()` live processes and a TTR above the backlog, and so
passed while real LSF still churned):
- Near-zero `jarchive: bad job` and each command executes **once** (no wasted
  double-execution) at a few thousand runners.
- A deliberately-killed **reserved-but-not-started** runner's job is still
  reclaimed and re-run (no stuck-in-Run hole).

### 4. Idea 5 — decouple the single RPC reader

Even with per-request goroutine handling, RPCs are **admitted** one at a time by
a single reader (`serveClients → receiveClientMessage → sock.RecvMsg()`,
`server.go:2656/2671`), so under a churning fleet `wr status`, `wr limit` and
`wr suspend` queue behind reserve/touch/archive traffic and time out (repro.md
Issues 1–3; observed all four control ops timing out at 60s under ~2000
runners). Provide a path by which control + status RPCs cannot queue behind the
runner fleet — e.g. a **separate listener/socket** (or concurrent readers) for
control/status RPCs, or otherwise ensure admission fairness.

Acceptance: heavy `wr status`, `wr limit`, `wr suspend` latency stays bounded
(single-digit-to-low seconds, no timeouts) while a few thousand runners churn
instant jobs.

---

## KEEP — must remain fully working

- The **background recovery window** (`recoverInBackground` / `isRecovering` /
  `ErrRecovering` / `rescheduleReadyAfterRecovery`) — it is what makes Idea 1's
  crash-recovery safe (restores running jobs to Run on restart).
- v0.36.5 completion leniency already restored by Option R (an alive owner's
  success is never discarded); the `queues_avoid` client fix
  (`.docs/bugfixes/260720-1.md`); the `putJobStats` zero/negative-duration guard.
- Option R's gains: no false-lost of on-time jobs, no false-`deleted` broadcast,
  fast startup on a large real DB.
- Database compatibility: the reworked build must open databases already
  upgraded by current code without error or data loss.

## Out of scope (do not implement here)

- **Ideas 4 and 6** — mooted by the front-end revert (§1); implementing them
  would be redundant.
- **Idea 3** — was a diagnostic step; already completed and folded into Idea 2.
- The **uncapped LSF `bsub` array hang** for large identical-requirement batches
  is a separate, pre-existing bug tracked as a `/bugfix` in
  `.docs/bugfixes/260722-1.md` — not part of this spec (though Idea 2(b) touches
  the same over-submission behaviour; coordinate but do not duplicate).

## Constraints

- Ideas 1, 2, 5 are **internal-only**: no user-facing behaviour change beyond
  fixing the bugs (per the project's speedups-internal-only rule). The front-end
  revert (§1) **is** a deliberate user-facing change (web-UI counts revert to
  v0.36.5 quality) and is acceptable given the stated priority.
- Follow `go-conventions` (copyright headers, GoConvey tests). Build/test with
  `-tags netgo`; **unset all `OS_*` env vars** when running the test suite (keeps
  `make test`/`make race` fast — see project notes). Add behavioural regression
  tests first (TDD).
- All isolated-LSF validation must use the **development** deployment on ports
  51780/51781 and must never touch the **production** manager; `bkill` all
  `wrd_*` arrays after, and note that `wr manager stop` can hang under load (kill
  the dev pid directly, verifying it is the dev binary first).

## Final validation — re-run the reproductions (REQUIRED gate before "done")

Implementation is not complete until the reproductions that surfaced these
problems are **re-run and shown fixed**. This is deliberately two tiers, because
the headline symptoms cannot all be captured as ordinary committed tests.

**Tier A — committed regression tests (TDD; run in `make test` / `make race`).**
Each fix ships with behavioural tests that fail before and pass after:
- Idea 1: (i) live manager + item-not-in-Run + failed report → client gives up
  promptly, no 24h/15s loop, proceeds to next reserve; (ii) manager stopped mid
  genuine-**success** report and restarted within `retryTime` → job recorded
  `complete`, command **not** re-run.
- Idea 2: (i) a live-but-unconfirmed reserved-not-started reservation is **not**
  requeued/re-reserved; (ii) a **confirmed-dead** reserved-not-started
  reservation **is** reclaimed and re-run (no stuck-in-Run hole); (iii) a command
  is executed exactly once (no double-execution) across a reserve→timeout cycle.
- Front-end revert: the counter/subscription machinery is gone and CLI counts
  stay correct across a DB-preserving restart; change-callbacks dispatch
  concurrently (no single drainer).
- (The giant-`bsub`-array bug has its own scheduler unit test in its `/bugfix`.)

**Tier B — manual end-to-end reproductions (NOT ordinary committed tests).**
The headline symptoms only manifest with **real LSF at scale, a live manager,
the `/status_ws` websocket, and a DB-preserving restart** — they are not unit
tests. Re-run the documented procedures in `.docs/reliable2/phase2/repro.md` on
the isolated dev manager and confirm each symptom is gone; **record the results
in a new `.docs/reliable2/phase2/validation.md`**:
- Issue B1/B2 churn: multi-group `true`/`false` jobs → near-zero
  `jarchive: bad job` / `jrelease: not running`, each command runs once, forward
  progress ≈100%.
- Issues 1–3 responsiveness: `wr status` (details), `wr limit`, `wr suspend`
  stay responsive (no 60s timeouts) while a few thousand runners churn instant
  jobs.
- Issue 4 count divergence: build `.docs/reliable2/phase2/wsprobe/`, complete
  jobs in a repgroup, restart preserving the DB, add live jobs, and confirm the
  web `/status_ws` view agrees with the CLI/DB (or that the drifting counter
  endpoint is gone after the revert).
- Idea 1 crash-recovery: kill the manager mid genuine-success report, restart
  within `retryTime`, confirm the job is `complete` and not re-run.

Notes for whoever runs Tier B:
- The in-process `reliability`-tagged scale harness
  (`jobqueue/reliable2_scale_test.go`) is a committable approximation of the
  load, but was shown **insufficient** — it passed (M2=0) while real LSF still
  churned (it uses `os.Getpid()` live processes and a TTR above the backlog). It
  may *support* but must **not** be the sole evidence for the Issue-B /
  responsiveness claims; a real-LSF run is required.
- Be a good farm citizen: validate at a considerate scale, force jobs to an
  appropriate queue, `bkill` all `wrd_*` afterwards, and expect fair-share to
  cap concurrency (repeated large submits deplete it).
- A full farm run may not be possible in a headless/agent session. If so,
  complete Tier A + the gated in-process harness autonomously, and clearly flag
  Tier B as a **required human validation** before merge — do not mark the work
  done on Tier A alone.

---

## Notes — clarifications resolved before authoring

These decisions were made by the user during spec-writing and are binding. They
refine (and where noted override defaults in) the sections above.

### N1. Web-front-end revert is COUNTER-ONLY (refines §1)

Do a counter-only revert, NOT a full v0.36.5 web revert.
- REMOVE only the absolute-count machinery: `repGroupCounts` (server.go),
  `repgroupcounts.go`, the `jstateAbsolute` message and its `/status_ws` push
  (serverWebI.go), and the absolute-count half of
  `emitJobTransition`/`applyTransitions` (jobtransition.go).
- RESTORE from v0.36.5 (commit 11fe092): the concurrent change-callback dispatch
  — replace the serial `runChangedCallbacks` (queue/queue.go:263) with v0.36.5's
  per-batch `go queue.changedCb(...)` fan-out (11fe092:queue/queue.go:330); and
  the v0.36.5 web status-bar feed (`statusCaster`/`jstateCount`, scan-on-connect,
  and the v0.36.5 `websocket-handler.js`/page behaviour). Web-UI counts revert to
  v0.36.5 quality.
- KEEP (these are phase-1 Option R, already merged — do NOT remove): the #503
  per-job subscription delivery (`server_subscription.go`/`subscription.go`,
  `enqueueChangeCallbackSubscriptions`), live RAM/CPU/STDOUT introspection,
  reconnect/resync, and `wr add --sync`'s non-polling wait.
- Because `emitJobTransition` currently drives BOTH the counter AND #503
  delivery, the surgery must SPLIT them: keep the subscription-delivery half,
  delete the counter half. Ideas 4 and 6 remain out of scope/moot.

### N1a. Counter-only revert — exact web-side scope (refines N1)

The revert is SURGICAL: revert the counts only; keep everything else.
- KEEP every current KEEP web front-end feature: `IsPushUpdate` live
  RAM/CPU/STDOUT pushes, reconnect fresh-state, modify-job (`modify-job.js`), and
  in-flight tracking (`inflight-tracking.js`). Do NOT do a literal "restore
  v0.36.5 `websocket-handler.js`" — that would delete these KEEP front-ends. On
  the JS side edit ONLY the status-bar count consumption.
- PRESERVE the phase-1 terminal-hiding fresh-connect seed (the `liveSeedLocked`
  behaviour at serverWebI.go:926-929, from bugfixes 260626-2/260716-1/260721-1)
  so a page refresh does NOT re-show completed-only repgroups. Because that seed
  currently lives inside the `repGroupCounts` counter being removed, its
  terminal-hiding logic must be RE-HOMED onto the reverted delta feed, not
  dropped.
- SERVER: replace only `repGroupCounts`/`jstateAbsolute` with v0.36.5's
  `statusCaster`/`jstateCount` delta broadcast (+ scan-on-connect), retaining the
  terminal-hiding seed on top.
- Net user-visible regression is ONLY status-bar count flicker/overcount to
  v0.36.5 quality; no other web feature is lost and the terminal-hiding-on-refresh
  fix is retained.
- REJECTED: a wholesale status-page revert that drops the live-introspection /
  reconnect / modify-job / in-flight web front-ends.

### N1b. Effect of the counter-only choice on the (moot) ideas 4 & 6

The surgical counter-only revert does NOT change the mootness verdict: ideas 4
and 6 must still NOT be implemented as distinct work items.
- Idea 6 (fix count divergence): still moot — the diverging absolute counter
  (`repGroupCounts`/`jstateAbsolute`, the source of repro.md Issue 4) is removed
  outright and replaced by v0.36.5's delta feed; there is no absolute counter left
  to "fix," and count quality intentionally reverts to v0.36.5 level.
- Idea 4 "make the counter conditional": still moot — the counter is removed
  entirely.
- Idea 4 "de-serialise the drainer": still achieved BY the revert (N1 restores
  v0.36.5's concurrent `go queue.changedCb(...)`), not implemented as a separate
  idea.
- The counter-only choice's ONE consequence (versus a full revert): the restored
  concurrent `changedCb` fan-out now also drives the RETAINED #503 subscription
  delivery (`enqueueChangeCallbackSubscriptions`) concurrently. This was verified
  (code trace) NOT to reintroduce the unresponsiveness. The decisive fact: the
  per-transition serialiser being removed is the counter's single EXCLUSIVE
  `repGroupCounts.mu` (repgroupcounts.go:66-70, `Lock()`ed across the whole batch
  on every transition at :86-106) — NOT anything in the #503 path. Under a
  concurrent `go changedCb` fan-out that one exclusive mutex would re-serialise
  every transition goroutine; removing the counter removes it. The retained #503
  delivery, by contrast, uses only `csmutex.RLock` (shared reads never block each
  other; the sole `csmutex.Lock` writers are subscribe/unsubscribe/shutdown, never
  per transition), has a zero-subscriber early-out (`if
  !hasAnyClientSubscriptions() { return }`, jobtransition.go:200-202), performs
  the actual client delivery as a buffered channel send AFTER the lock is released
  (server_subscription.go:504-521), and is bounded by (batch × subscribers) with
  no full job/repgroup scans. (v0.36.5 itself already ran a per-transition
  detail-push loop over subscribed connections, so retaining #503 adds no
  materially more idle per-transition work than the v0.36.5 baseline — its idle
  path is actually leaner.) These are internal locking invariants with no
  user-facing change and need NO user decision; the revert MUST preserve them:
    1. THE decisive requirement — the revert MUST actually remove the
       `repGroupCounts.applyTransitions` call from the per-transition path.
       Leaving it in place re-serialises the restored concurrent dispatch
       regardless of #503.
    2. Keep the `hasAnyClientSubscriptions()` early-out at the top of the
       subscription closure (one `csmutex.RLock` + `len()`, then return when the
       subscriber map is empty).
    3. Keep `csmutex` an `RWMutex`; all per-transition access stays `RLock`; never
       add a per-transition `csmutex.Lock` (writers stay confined to
       subscribe/unsubscribe/shutdown).
    4. Never hold `csmutex` (or any server-wide lock) across the actual client
       delivery — delivery stays a buffered channel send performed after the lock
       is released.
    5. Use the restored v0.36.5 `statusCaster.Send` delta broadcast for the
       web-bar counts; do NOT reintroduce ANY single server-wide exclusive mutex
       on the per-transition path (that was exactly `repGroupCounts.mu`).
  Verify BOTH under `-race` AND under load (control-op responsiveness while a
  fleet churns), not `-race` alone. This is an implementation constraint on the
  revert, not a resurrection of idea 4.

### N2. Idea 2(a): host+pid reported in the Reserve request (refines §3a)

- The runner reports its own host + `os.Getpid()` in the Reserve `clientRequest`;
  the server records them on the job in `respondWithReservedJob` INSTEAD OF
  zeroing Host/Pid in `resetJobForReservation` (serverCLI.go:842-844). No new RPC;
  do NOT piggyback on the touch stream (that stream is exactly what Idea 5
  decouples). The reserve-time pid is the runner's own pid and is overwritten by
  the command's pid at Started.
- Reclaim reuses the EXISTING started-path machinery: split the `ttrCallback`
  `StartTime.IsZero() || job.Exited` branch (server.go:3352) so the `job.Exited`
  case still goes straight to `SubQueueDelay`, while the reserved-not-started case
  is parked in Run (un-reservable) and confirmed dead via
  `markJobLost`/`confirmOrReleaseLostJob` (which snapshots job.Host/job.Pid),
  using `ProcessNotRunningOnHost`/LSF `bjobs` — a signal independent of the
  backlogged RPC stream.
- Old-client fallback (reserve carries no host+pid → pid==0): keep the job PARKED
  in Run; never blindly re-reserve a reservation that cannot be confirmed dead. Do
  NOT revert to the old `StartTime`-based requeue for this case.

### N3. Idea 2(b): protect reserved LSF array elements from bkill (refines §3b)

- Fix by TRACKING RESERVED ELEMENTS, not by re-checking RUN before bkill: wr must
  record which submitted LSF array elements it has already handed a reservation
  to, and `killExcessCmds` (lsf.go:1176-1204) must NEVER bkill a tracked element.
  This is robust to `bjobs` status lag (a PEND→RUN element that just
  reserved+started is protected because wr knows it handed out that reservation,
  without depending on bjobs having caught up to RUN).
- The spec author must design how a reserved LSF array element is identified and
  correlated back to the runner/reservation so the protection is reliable.
- Boundary with bugfix 260722-1: this spec owns ONLY the "never bkill an element
  we have reserved to" protection. Reducing the VOLUME of over-submission (the
  array-cap / uncapped `bsub` array behaviour) belongs entirely to the separate
  260722-1 `/bugfix`. Coordinate; do not duplicate or double-fix.

### N4. Idea 5: concurrent readers on the existing socket (refines §4)

- Decouple by running MULTIPLE concurrent reader goroutines each calling
  `sock.RecvMsg()` (server.go:2670) on the EXISTING mangos socket — not a separate
  socket/port, not priority admission. No new port and no wire/protocol change;
  stay wire-compatible with existing runners and CLIs.
- HARD REQUIREMENT: the mangos REP-style socket must handle concurrent
  request/reply safely (historically a wr foot-gun). The spec author should
  investigate mangos `Context` objects (the idiomatic mechanism for concurrent
  request/reply on one REP socket) versus raw concurrent `RecvMsg`, and the design
  MUST be proven under `-race`. If concurrent handling on this socket proves
  genuinely unsafe, surface it as a blocker (per agent-conduct) rather than
  silently switching designs.

### N5. Test strategy and the "done" bar (refines Final validation)

- Tier-A committed tests use the Option-R deterministic style (`os.Getpid()` for
  an "alive" owner, a definitely-dead pid for "confirmed dead", short `ItemTTR`),
  covering the behaviours enumerated in the Tier A list. TDD: fail before, pass
  after; run under `make test`/`make race`.
- KEEP `jobqueue/reliable2_scale_test.go` (//go:build reliability) but add a
  header comment documenting that it under-reproduces (os.Getpid live processes +
  a TTR above the backlog → it passed M2=0 while real LSF churned). It is a
  non-authoritative support test, never the sole evidence.
- "Done" bar: completing Tier A + the gated in-process harness constitutes the
  CODING being done and may be completed autonomously. Tier-B real-LSF end-to-end
  validation (recorded in a new `.docs/reliable2/phase2/validation.md`) is a
  REQUIRED gate before merge — do NOT mark the overall work done on Tier A alone.
  See N6 for who runs Tier B: the implementing agent at the end when it can reach
  the isolated dev farm at scale, else a human as fallback. Either way it must be
  actually executed, never skipped or simulated.

### N6. Tier-B may be run by the implementing agent, not only a human (refines N5 / Final validation)

The original "required human validation" wording was only a fallback for a
session lacking farm access; it is NOT a requirement that a human specifically
run Tier B. This note supersedes the "human" wording in N5 and the Final
validation section.
- Tier-B real-LSF validation remains a REQUIRED gate before merge, producing
  `.docs/reliable2/phase2/validation.md`, and must never be skipped or claimed
  done on Tier-A alone (the in-process harness was shown insufficient).
- It SHOULD be run by the implementing agent at the END of the work when the
  session can reach real LSF at scale on the isolated DEVELOPMENT deployment. A
  human runs it only as a fallback when the agent genuinely cannot (no real-LSF
  access, or fair-share cannot permit a representative run).
- Whoever runs it MUST follow the existing safety constraints: use ONLY the dev
  manager (ports 51780/51781), never touch production; be a good farm citizen
  (considerate scale, force jobs to an appropriate queue, expect fair-share to
  cap concurrency); `bkill` all `wrd_*` arrays afterwards; and note `wr manager
  stop` can hang under load (kill the dev pid directly after verifying it is the
  dev binary).
- The surviving distinction: Tier-B is a REAL-LSF gate (not a committed unit
  test) and must actually be executed, not simulated.

### N7. Harden the phase-1 JS revert with the existing browser-test fixtures (refines N1a / §1)

The phase-1 JS status-bar edit (spec story A3) and the retained
terminal-hiding-on-refresh behaviour (A4) touch a historically fragile area
(terminal-hiding regressed three times: bugfixes 260626-2/260716-1/260721-1), so
add a browser-level regression guard.
- The existing browser-test fixtures covering this area MUST remain green after
  the JS status-bar edit; run `make browser-test`. The spec author must VERIFY
  the exact fixture names in the repo's browser-test setup (the candidates
  identified are `completed-repgroup-visibility`, `removed-jobs-refresh`,
  `repgroup-bar-flicker`) and use the real names/paths as they exist.
- Update those fixtures ONLY as strictly needed to reflect the reverted
  v0.36.5-quality count DISPLAY; do not weaken their terminal-hiding / refresh
  assertions.
- This is a phase-1 acceptance criterion guarding A3/A4: add it to the A3/A4
  acceptance tests and the Tier-A list, and reflect it in phase1. It is
  belt-and-suspenders on top of the server-side Go tests, which remain the
  primary guard.
