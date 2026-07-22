# Phase 5: KEEP regression sweep and two-tier validation (sections E1, E3)

Ref: [spec.md](../spec.md) sections E1, E3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

This is the final gate. Item 5.1 runs the full KEEP anchor suite green under
`-race` and adds the required header comment to the reliability-tagged scale
test; Item 5.2 performs the Tier-B real-LSF validation, which is a REQUIRED
real-LSF gate before merge (run by the implementing agent at the end when able,
else a human as a fallback; never skipped or simulated) and is NOT a committed
test. Depends on Phases 1-4 (the full set of changes must be in place). Do NOT
weaken or delete any existing KEEP test. Build/test with `-tags netgo`; unset
ALL `OS_*` env vars for `make test` / `make race`.

The "done" bar (N5/N6): completing Tier A (the committed acceptance suites of
Phases 1-4) plus the gated in-process harness is the CODING done and may be
completed autonomously. Tier B is a REQUIRED real-LSF gate before merge - the
overall work is NOT done on Tier A alone. It SHOULD be run by the implementing
agent at the END of the work when the session can reach real LSF at scale on
the isolated dev deployment; a human runs it only as a fallback when the agent
genuinely cannot (no real-LSF access, or fair-share cannot permit a
representative run). Either way it must be actually executed, never skipped or
simulated, and recorded in `validation.md`.

## Items

### Item 5.1: E1 - KEEP anchors stay green; scale-test header comment

spec.md section: E1

Do NOT remove or weaken any of the following; the existing anchor tests stay
green under `-race`:

- Background recovery window (`recoverInBackground`/`isRecovering`/
  `ErrRecovering`/`rescheduleReadyAfterRecovery`) - makes section D safe.
- #503 per-job subscription delivery, live RAM/CPU/STDOUT introspection
  (`emitLiveTouchSnapshot`), reconnect/resync, `wr add --sync` non-polling wait.
- Web JS KEEP: `IsPushUpdate` live pushes (`State` branch), reconnect
  fresh-state, `modify-job.js`, `inflight-tracking.js`.
- Phase-1 Option R gains: no false-lost of on-time jobs, no false-`deleted`
  broadcast, fast large-DB startup; the `queues_avoid` client fix
  (`.docs/bugfixes/260720-1.md`); the `putJobStats` zero/negative-duration
  guard.
- v0.36.5 completion leniency (an alive owner's success is never discarded).

Add the required header comment to `jobqueue/reliable2_scale_test.go`
(`//go:build reliability`): keep the test but document that it UNDER-reproduces
(it uses `os.Getpid()` live processes and a TTR above the backlog, so it passed
M2=0 while real LSF churned) and is a non-authoritative SUPPORT test, never the
sole evidence.

Covers the 1 E1 acceptance test: the existing KEEP anchor suites
(`jobqueue/subscription_test.go`, `jobqueue/live_jtouch_test.go`, the resync/
suspend/modify tests, `jobqueue/reliable2_keep_test.go`, the recovery tests,
`jobqueue/reliable2_completion_test.go`, `jobqueue/reliable2_lost_test.go`,
`jobqueue/reliable2_dbcompat_test.go`) remain green under `-race` after all
Phase 1-4 changes.

For this item, "implemented" means the full KEEP anchor suite runs green under
`-race` and the scale-test header comment is added; "reviewed" means no KEEP
test was weakened or deleted and the header comment is present.

- [ ] implemented
- [ ] reviewed

### Item 5.2: E3 - Tier-B real-LSF validation (required real-LSF gate)

spec.md section: E3

Tier A (committed, in `make test`/`make race`, each failing before and passing
after) is delivered by Phases 1-4 and re-confirmed here:

- A: counter machinery gone + CLI counts correct across a DB-preserving restart
  (A1.1, A3.2); change-callbacks dispatch concurrently, no serial drainer
  (A2.1-2).
- B: N concurrent readers `-race`-safe with correct per-client reply routing
  (B1.1); bounded control-RPC latency under in-process load (B1.2, supporting).
- C: reserved-not-started alive owner not re-reserved (C2.1); confirmed-dead
  reserved-not-started reclaimed, no hole (C2.2); old-client parks (C2.3);
  reserved LSF element never bkilled (C3.1).
- D: live-manager not-in-Run failed release -> client gives up promptly
  (D1.1-2); manager stopped mid genuine-success, restarted within `retryTime`
  -> recorded complete, not re-run (D2.1); `retryTime == 24h` (D2.2).

Tier B (NOT committed; a REQUIRED real-LSF gate before merge - run by the
implementing agent at the END when it can reach real LSF at scale, else a human
as a fallback; never skipped or simulated): re-run the documented procedures in
`.docs/reliable2/phase2/repro.md` on the ISOLATED DEV manager only (ports
51780/51781, never production; be a good farm citizen (considerate scale, force
jobs to an appropriate queue, expect fair-share to cap concurrency); `bkill` all
`wrd_*` after; kill the dev pid directly after verifying it is the dev binary,
because `wr manager stop` hangs under load), and record results in a NEW
`.docs/reliable2/phase2/validation.md`:

- Issue B/B2 churn: multi-group `true`/`false` jobs -> near-zero
  `jarchive: bad job` / `jrelease: not running`, each command runs once, forward
  progress ~100%.
- Issues 1-3 responsiveness: `wr status` (details), `wr limit`, `wr suspend`
  stay responsive (no 60s timeouts) while a few thousand runners churn instant
  jobs.
- Issue 4: build `.docs/reliable2/phase2/wsprobe/`, complete jobs in a rep
  group, restart preserving the DB, add live jobs, and confirm the web
  `/status_ws` view agrees with the CLI/DB the v0.36.5 way (or that the drifting
  absolute-counter endpoint is gone).
- Idea 1 crash-recovery: kill the manager mid genuine-success report, restart
  within `retryTime`, and confirm the job is `complete` and not re-run.

For this item, "implemented" means the Tier-B procedures were run on the
isolated dev manager and results recorded in
`.docs/reliable2/phase2/validation.md` (a real-LSF gate, not code) - the
implementing agent SHOULD run them itself at the end when it can reach the
isolated dev farm at scale; "reviewed" means the recorded `validation.md`
metrics were checked against the thresholds above. Only when the agent
genuinely cannot run Tier B (no real-LSF access, or fair-share cannot permit a
representative run) does a human run it as a fallback; in that case complete
Tier A + the in-process harness, FLAG Tier B as the required real-LSF gate not
yet performed (per N5/N6), and still produce `validation.md`. Either way
Tier B must be actually executed, never simulated. This is a gate, not code.

- [ ] implemented
- [ ] reviewed

## Merge gate

The change may merge only when, in addition to the recorded Item 5.2 Tier-B
result:

- All Tier-A acceptance suites (Phases 1-4) and the section E1 KEEP anchors
  above are green under `-race`.
- `make test`, `make race`, `make lint` all clean (with all `OS_*` env vars
  unset).
- `.docs/reliable2/phase2/validation.md` is recorded and its metrics meet the
  thresholds above. Tier B is a REQUIRED real-LSF gate (agent-run at the end
  when able, human fallback otherwise; actually executed, never simulated): the
  work is NOT done on Tier A alone.
