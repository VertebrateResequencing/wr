# reliable4 — TTR-miss archive-reject churn

Status: 2026-07-26. **FIXED (finalized, default on)** via /bugfix `260726-3` — Fix C
(record the runner's own pid; confirm a lost job dead only if BOTH the command AND
runner pids are gone) plus the wedged-runner kill backstop, both now the DEFAULT (the
`WR_EXP_RUNNERPID` / `WR_EXP_LOSTBACKSTOP_MS` gates are removed). The backstop is a real
config value, `ServerConfig.Timings.LostRunnerBackstop` (default `ServerLostRunnerBackstop`
= 1h; tests set it low). Regressions that run in `make test`:
`jobqueue/TestReliable4RunnerPidLiveness` (live runner's completed job not re-run, late
archive accepted), `jobqueue/TestReliable4LostRunnerBackstop` (wedged-but-alive runner
force-killed after the backstop, slot reclaimed), and
`jobqueue/scheduler/TestKillProcessCommandContract` (the kill-string forced-command
contract). The build-tagged reproducers (`reliable4_ttrmiss_test.go`,
`developers/wrdev.sh ttrmiss-check`) remain.

**OPERATIONAL REQUIREMENT for the backstop KILL in production:** wr force-kills a wedged
runner over ssh with `kill -9 <pid> 2>/dev/null || true # wr-kill -p <pid>`. Against the
existing ps-only forced command that farm operators install in `~/.ssh/authorized_keys`
(see cmd/conf.go privatekeypath docs) this degrades to a harmless `ps -p <pid>` — **no
kill, no error — a SAFE NO-OP** (the wedged job simply stays parked, exactly today's
behaviour, no regression). To actually ENABLE the backstop kill, the operator must install
the UPDATED forced command documented in cmd/conf.go (which also permits `kill -9` and
branches on the `kill` marker), or use an unrestricted key. The main fix (Part 1, not
re-running a live runner's completed job) needs NO operational change — it uses only the
existing `ps -o stat=` liveness check. Below is the original investigation write-up.

## What this is

The residual churn seen alongside the backup stall (both the LSF `backup-stall-check`
tail and the older reliable2 reports): a job whose command **succeeds** is falsely
treated as lost, **re-run**, and its late successful archive rejected
(`ErrBadJob` / `ErrMustReserve`) — wasting the successful work even though the
command never failed.

## Mechanism (verified against current reliable4 code)

1. A runner reports the **command's child pid** via `Started`
   (`client.go startedRequest(job, cmd.Process.Pid)`, ~:1716), NOT the `wr runner`
   process's own pid. So once the command exits, `job.Pid` is a genuinely-dead pid
   even while the runner process is alive and about to archive.
2. The runner **touches every ~TouchInterval** and keeps doing so until *just
   before* it archives (`client.go` `stopTouching` at ~:2122, immediately before
   `applyFinalState → Archive`). Each touch resets the 60s TTR. So a healthy,
   promptly-touching runner is **never** falsely lost — `ttrCallback`/`confirmJobDead`
   are only reached if touches **lapse for > TTR**.
3. When touches lapse for > TTR while the command has already finished (the runner
   **died** after success-but-before-archive, or is so **CPU-starved** its
   touch/archive goroutines can't run for ~a TTR), `ttrCallback` marks the job Lost
   (still parked in `SubQueueRun`), then `confirmOrReleaseLostJob → confirmJobDead`
   runs `ps` on the (command) pid → **dead** → `killLostJobAndTriggerBehaviours →
   killJob → releaseJob` moves the item out of `SubQueueRun` and **re-runs** it.
4. The runner's late `Archive` then hits `getij(cr,true)`'s
   `item.Stats().State != queue.ItemStateRun` → **`ErrBadJob`** (serverCLI.go:1746),
   or after re-reservation the owner check → **`ErrMustReserve`** (:1757). The
   success is discarded.

**Important nuance (thanks to the "the pid shouldn't be dead" question):** the pid
IS the command's, so it IS dead for a live runner — but the TTR only fires, and this
path is only reached, once **touches have lapsed**. So the trigger is
starvation / runner-death / extreme RPC delay, **not** every slightly-slow archive.
A healthy touching runner is safe. Current reliable4 already accepts a late archive
while the job is still parked-Lost-in-`SubQueueRun` (the restored v0.36.5 leniency);
the churn is the window **after** the confirm-dead rerun.

**Severity on current reliable4 is BOUNDED.** The catastrophic reliable2 form (single
socket reader serialising all RPCs → mass touch/archive backlog → mass false-lost,
never drains) is gone: `serveClients` now runs 6 concurrent readers and dispatches
each request to its own goroutine, so under load jobs churn-but-eventually-drain
rather than lock up (the LSF backup test drained 4000/4000 despite ~hundreds of
badjob). The remaining cost is **wasted reruns + discarded successes for the
starved minority**; permanent loss only if a job's every attempt is starved (needs
~all runners starved — unrealistic).

## Reproducer

`jobqueue/reliable4_ttrmiss_test.go` `TestReliable4BackupStall`… → `TestReliable4TtrMissChurn`
(build tag `reliability_repro`; `developers/wrdev.sh ttrmiss-check`). In-process, no
LSF: a runner pool does `Reserve → Started(deadPid) → [optionally keep touching] →
wait archiveDelay → Archive(success)`, classifying each archive. Knobs:
`WR_TTRMISS_{JOBS,RUNNERS,TTR_MS,ARCHIVE_DELAY_MS,SECONDS,TOUCH}`. `deadPid` is a
reaped child (`definitelyDeadPid`) so `confirmJobDead` deterministically confirms
death; the local scheduler runs a real `ps`.

Results (current reliable4, TTR=500ms):

| config | outcome |
|---|---|
| `TOUCH=1` (healthy, keeps touching), delay 3×TTR | **60/60 complete, 0 rejected** — touches protect |
| `TOUCH=0` (touches lapsed), delay 3×TTR | **0/60 complete, all 480 archives rejected** — never drains |

So churn requires the touch-lapse; a touching runner is safe. `TOUCH=0` with
`archiveDelay > TTR` is the deterministic worst case (models a starved/dead runner
whose command succeeded).

## Fix C (RECOMMENDED, validated) — record the runner pid and check BOTH

The root defect is that `confirmJobDead` checks the **command's** child pid, which
is dead the instant the command finishes — a false "dead" for a job that merely
completed and whose runner is slow/starved to archive. Fix: also record the
**runner** process's pid (its own `os.Getpid()`), and treat the job as dead only if
**both** the command AND the runner are gone.

Prototype behind `WR_EXP_RUNNERPID` (`Job.RunnerPid`, set in `client.go
startedRequest` and `serverCLI.go applyJobStart`; `server.go confirmJobDead` does a
second `ProcessNotRunningOnHost` on the runner pid and only returns dead if both are
gone; `RunnerPid==0` from old records falls back to the current command-pid-only
behaviour). Reproducer (`TOUCH=0`, no grace):

| runner | archiveDelay | outcome |
|---|---|---|
| alive (starved) | 3×TTR | **60/60 — no churn** |
| alive (starved) | 12×TTR | **60/60 — no churn** (grace could not do this) |
| dead (`WR_TTRMISS_RUNNER_DEAD=1`) | 3×TTR | 0/40 — job correctly RE-RUNS |

So it fixes the churn **for any delay** (a starved runner's process is alive — `ps`
sees it — however long it starves), while a genuinely-dead runner (both pids gone)
still re-runs promptly. It is the correct liveness signal and directly implements
"ideally don't end up in this situation": a live runner's completed job is **never**
re-run, so there is no rerun to race the archive, so the hard requirement is met
trivially (a re-run only ever happens when the original is truly gone). It reuses the
existing `ProcessNotRunningOnHost` mechanism — no state-machine change, no
owner/StartTime issues, no slot-hold for dead runners.

### The one gap in Fix C: a runner that is alive but WEDGED

A runner that is alive but wedged (deadlocked/stuck; never archives, never dies)
would keep its job parked-Lost — holding its limit slot — under Fix C.
- **Detect it externally? No, not reliably.** Via ssh+`ps` a wedged runner is
  indistinguishable from a merely-starved one or one legitimately blocked on slow
  I/O (all: alive, ~0 CPU, not touching). Only elapsed *time* separates them.
- **Local temp state files (runner records started/completed)? Avoid.** LSF reruns
  land on any node, so a marker on the first node's local disk is invisible to the
  rerun (would need shared storage — fragile — and still needs TTL cleanup); it also
  duplicates state the manager owns. This is reliable2's "runner-authoritative
  durable outcome" idea; not worth it here.
- **Better, file-free:** (a) a runner that merely hit a *transient* comms failure
  already **retries in the background**, so with Fix C it self-heals; (b) a *truly*
  wedged runner is rare and already bounded by the scheduler **walltime** (LSF kills
  it → both pids dead → Fix C reruns → slot reclaimed); (c) for tighter reclaim, a
  **max-parked-Lost backstop** that, once a job has been parked Lost far longer than
  any plausible archive delay, **force-kills the (wedged) runner process on its host**
  (`Scheduler.KillProcessOnHost`, `kill -9`) — then the *normal* dead-confirmation
  path finds both pids gone and re-runs the job. This is safer than a special
  "force-rerun despite alive" (no race with a recovering runner); and if the runner
  somehow recovers, its later archive is rejected (new-run-wins). Prototyped behind
  `WR_EXP_LOSTBACKSTOP_MS` (default a generous **1h** when the fix is active,
  configurable for tests), in `confirmJobDeadAndKillAfterRetryTime`. **Validated** by
  `TestReliable4TtrBackstopKill`: a job with a dead command pid and a live wedged
  runner is parked (not re-reserved) while the runner lives, then — after the
  backstop kills the runner — is reclaimed/re-run.

Note this is **no worse than today** for a wedged runner (today reruns it once the
command pid dies; Fix C+backstop reruns it at the backstop) and strictly better for
the common live-but-slow case.

## Fix A (earlier trial, superseded by C — trial code since removed) — grace before re-run

(Kept for the record; the `WR_EXP_LOSTGRACE_MS` trial code was removed once Fix C +
the kill-backstop superseded it.)

Prototype behind `WR_EXP_LOSTGRACE_MS` (`server.go confirmJobDeadAndKill` →
`killLostJobAfterGrace`): when the lost job's pid is confirmed dead, **wait a grace,
then re-run only if the job is still parked Lost-in-Run** (i.e. no late archive or
recovering touch resolved it meanwhile). During the grace the job stays in the
owner's reservation, so a late archive is accepted by the **existing** `handleArchive`
path — no state-machine change, no owner/StartTime issues.

Results (`TOUCH=0`):

| archiveDelay | grace | outcome |
|---|---|---|
| 3×TTR (1500ms) | 0 (default/off) | 0/60 — churn |
| 3×TTR (1500ms) | 2000ms | **60/60 — no churn** |
| 12×TTR (6000ms) | 2000ms | 0/40 — churn (grace too small) |
| 12×TTR (6000ms) | 8000ms | **40/40 — no churn** |

So it eliminates the churn **iff grace ≳ archiveDelay − TTR**, i.e. it copes with any
*bounded* starvation by sizing the grace. Trade-offs: a genuinely-dead-runner job
(and a confirmed-dead failed job) waits ~grace longer to re-run, **holding its
limit-group slot that long** — a *bounded* version of reliable3's phantom-slot
pressure, mild for a modest grace (~ItemTTR) but a reason not to set it to many
minutes. Does not cover unbounded starvation (indistinguishable from death; re-run
is then defensible).

## HARD REQUIREMENT (user): once a re-run has actually started, the NEW run wins

If a re-run has already begun (the grace expired, or the grace is off), the new run
must complete and **its** archive must be used; the original runner's stale late
archive must be **rejected**. This is the current behaviour (the stale archive hits
`ErrBadJob`/`ErrMustReserve`) and MUST be preserved. It also rules out the tempting
"accept-late / first-success-wins" idea (below): overriding an in-flight new run
with the original's stale success is wrong.

## Fix B (REJECTED) — accept-late-success / first-success-wins

Accept the original runner's late archive even after the job left the reservation
(complete it authoritatively regardless of sub-queue/owner). This **violates the
hard requirement**: if a re-run is already running, this would override it with the
stale earlier result. Do NOT do this. (It is also a fiddly state-machine change:
`respondWithReservedJob` resets `StartTime` on re-reserve, serverCLI.go:873, so a
reran job fails `canCompleteFromEndState`; a late accept would have to reconstruct
StartTime, bypass the owner check, and race the re-run.) Rejected on correctness
grounds, independent of complexity. A *safe* subset — if the archive's job is
already Complete, return success instead of `ErrBadJob` (pure idempotent dedup of a
duplicate archive) — is harmless and could be added, but does not by itself stop the
churn.

## Recommendation

**/bugfix — Fix C (record the runner pid, check both), and nothing that overrides an
in-flight re-run.** It fixes the root defect (the liveness check watched the wrong
process) so a live-but-slow/starved runner's completed job is never re-run at all —
"ideally we don't end up in this situation" — for any delay, while a genuinely-dead
runner still re-runs promptly. Validated in the reproducer (alive → 60/60 no churn
even at 12×TTR; dead → correctly re-runs). Simple and localized: a codec-compatible
`Job.RunnerPid` field, the runner reporting `os.Getpid()` in `Started`, and a second
`ProcessNotRunningOnHost` in `confirmJobDead`; it reuses the existing mechanism, adds
no concurrency, and makes the "new-run-wins" requirement hold by construction (a
re-run only happens when the original is truly gone). Make it the default (drop the
`WR_EXP_RUNNERPID` gate), plus a generous **max-parked-Lost backstop** (drop the
`WR_EXP_LOSTBACKSTOP_MS` gate; default well above any plausible archive delay) so a
rare wedged-but-alive runner's slot is still reclaimed. Keep the reproducer as the
gate, and add regression tests: (1) alive runner + late archive → completes once,
0 rejects, at a large delay; (2) dead runner (both pids gone) → re-runs; (3) old
record `RunnerPid==0` → unchanged command-pid-only behaviour; (4) a job parked Lost
beyond the backstop with an apparently-alive runner → re-runs (slot reclaimed).

Considered and rejected / inferior:
- *accept-late / first-success-wins* (Fix B) — REJECTED: would override an in-flight
  re-run with the original's stale success (violates the hard requirement).
- *grace before re-run* (Fix A) — works only for *bounded* starvation (grace must be
  sized to the delay) and delays every dead-runner re-run while holding its limit
  slot for the grace. Superseded by C, though a large grace remains an optional
  backstop for the rare wedged-but-alive runner.

This is simple enough to one-shot with **/bugfix**. No /spec-writer needed: the
catastrophic reliable2 single-reader form is already fixed (6 concurrent readers), and
Fix C removes the residual false-lost-of-a-live-runner churn at its source. Note that
the LSF-scale end-to-end gate would still need a real oversubscribed node to exercise
starvation; the in-process reproducer is the reliable gate.
