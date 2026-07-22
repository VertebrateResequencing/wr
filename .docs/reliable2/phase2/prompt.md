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
