# reliable2 Phase 2 — Tier-B real-LSF validation

Status: **reliable2 phase-2 fixes VALIDATED** (churn eliminated, control RPCs
responsive, scheduler deadlock found and fixed). One **out-of-scope** limit
remains at 160k scale: the deferred over-submission / slow-`bsub` problem
(bugfix 260722-1), which caps forward-progress throughput but is not a
reliable2 phase-2 regression.

Tier B is the required real-LSF gate (spec §E3). It was executed by the
implementing agent on the isolated **development** deployment (never
production). Tier A (all committed acceptance suites of phases 1–5.1 + KEEP
anchors) is green under `-race`/`make test`/`make race`/`make lint`.

## Environment

- Host `farm22-wrstat01`, real IBM LSF cluster `farm22`.
- Binary rebuilt from branch HEAD (`CGO_ENABLED=1 go build -tags netgo -o
  /nfs/users/nfs_s/sb10/wr-r2/wr .`) — final runs on commit `dfa196f`
  (includes the deadlock fix below).
- Dev manager: deployment `development`, ports 51780/51781, job names `wrd_*`,
  scheduler `-s lsf`, queue forced to `normal`, `--retries 0`. All production
  managers (`--deployment production`) were left untouched. Workload spread
  across 100 memory groups to avoid the uncapped-array hang (260722-1).
  `true` = fast success (rep group `rgtrue`); `false` = fast failure → buried
  (`rgfalse`). Teardown: `bkill -J 'wrd_*' 0` + `kill -9` the verified dev PID.

## Results

### Issues 1–3 — control-RPC responsiveness under churn: PASS

Throughout every run (40k and 160k, even while the scheduler was saturated),
`wr status -o counts`, `wr status -i <rg> --limit 2`, `wr limit`, and
`wr suspend` returned in **~30–220 ms** — never the 60 s timeout the pre-fix
build showed. The concurrent RPC readers (B1) admit control RPCs without
queuing behind the fleet. **Goal met.**

### Issue B/B2 churn at 40k (20k `true` + 20k `false`): PASS

Full clean drain, **zero churn**: `rgtrue` 20000/20000 `complete`, `rgfalse`
20000/20000 `buried`, `jarchive: bad job` = **0**, `jrelease: not running` =
**0**, forward progress **~100%**, each command ran once, control RPCs
42–44 ms. Confirms the C (double-reservation prevention) and D
(release-livelock give-up) fixes behave correctly on real LSF.

### Issue B/B2 churn at 160k — deadlock FOUND and FIXED

The first 160k run **hard-deadlocked** after ~150 completions. Root cause
(SIGQUIT goroutine dump, analysed then discarded): a lock-ordering wedge in wr's **scheduler-group
accounting** — `scheduleGroup` (server.go) held a per-`sgroup` write lock
across the entire `scheduleRunners`→`bsub` exec; concurrent archive handlers
held `psgmutex.RLock` and blocked acquiring one of those `bsub`-busy sgroups'
`RLock` in `hasSkips`, while the RAC callback blocked on `psgmutex.Lock`,
freezing everything that touches `psgmutex`. This code is **pre-existing**
(byte-identical at base `f116e42`); phase-2's concurrent readers only reach the
window faster.

**Fix (commit `dfa196f`):** `scheduleGroup` now runs `scheduleRunners` against a
`group.snapshot()` clone with **no sgroup lock held across `bsub`** — mirroring
the existing `unscheduleUnneededGroups` idiom. Added a deterministic
blocking-scheduler regression test `TestReliable2ScheduleGroupDeadlock`
(fail-before/pass-after under `-race`); `make test`/`make race`/`make lint`
green.

After the fix, the 160k re-run showed **the deadlock is gone**: the manager
stayed responsive, **`rgfalse` fully drained (80000/80000 buried)**, and
`jarchive: bad job` = **0**, `jrelease: not running` = **0** throughout. A
second SIGQUIT dump confirmed **zero lock waiters** (no RWMutex deadlock).

### Remaining 160k limit — deferred over-submission (bugfix 260722-1), OUT OF SCOPE

At 160k the `rgtrue` backlog stopped making forward progress (stuck at a few
thousand of 80000) even though the manager was deadlock-free and responsive.
The second goroutine dump shows why: the scheduler was saturated by **slow LSF
external commands** — 11 goroutines stuck 2+ minutes in `submitToQueue`
(`bsub` via `os/exec`), and ~59 in `killExcessCmds → parseBjobs` (`bjobs`) —
i.e. wr massively over-submits runners for instant jobs and then spends all its
scheduling capacity in `bsub`/`bjobs`/`bkill`. This is exactly the behaviour
repro.md documented (38,302 of ~40k array elements bkilled) and which the spec
**explicitly defers to bugfix 260722-1** (§N3 BOUNDARY, §E2 out-of-scope). The
`bsub`/`bjobs`/`bkill` submit path is **unchanged by phase-2**
(`git diff f116e42..HEAD` shows no change to `submitToQueue`/the bsub path).
This is a throughput cap from the deferred over-submission problem, not a
reliable2 phase-2 regression, and not a lock deadlock.

### Issue 4 — web `/status_ws` vs CLI: PASS (live-tracking); restart-history not CLI-testable on dev

Run with the local scheduler + the updated delta-feed `wsprobe`
(`.docs/reliable2/phase2/wsprobe/`):
- A terminal-only rep group `rgX` (300 jobs, all `complete`) yields **nothing**
  on the web `/status_ws` feed (terminal-hiding, v0.36.5 behaviour), while the
  CLI scan shows its count — no diverging absolute counter.
- After adding 20 live jobs to `rgX`, the web feed and the CLI **agree** on the
  live state at the same instant: web `rgX ready:18 running:2` (and `"+all+"`
  the same) vs CLI `running:2 ready:18`. The delta feed tracks live transitions
  correctly and there is **no diverging absolute counter** — the Issue-4 fix.

Limitation: the second Issue-4 sub-check "CLI still shows N complete after a
DB-preserving restart" is **not CLI-testable on the development deployment** —
`wr manager start` on `development` always wipes the DB (`initDB(..., wipe =
!dontWipeDevDB)`; `dontWipeDevDB` is an internal test-only config with no CLI/
env switch, so a CLI restart always starts empty; the `WR_RELIABILITY_KEEPDB=1`
in repro.md does not exist in the code). This exact property is covered
deterministically in-process by Tier-A test A3.2
(`TestReliable2WebRevertNoDivergingCounter`, dontWipeDevDB restart: CLI shows
`Counts[JobStateComplete]==N`, web has no diverging counter), verified genuine
in review.

### Idea-1 crash-recovery: covered by Tier-A (not CLI-testable on dev)

Attempted on real LSF with a single genuine-success job (a marker file counts
executions). The command ran **exactly once** (marker = 1 line; not re-run).
But a faithful crash-recovery test requires a DB-preserving restart so the
still-owned running job is recovered and the runner's re-sent archive is
accepted — and, as above, the dev deployment always wipes the DB on a CLI
restart, so the restarted manager cannot recover the job. This property is
therefore validated deterministically in-process by Tier-A test D2.1
(`TestReliable2ReleaseCrashRecovery`: stop mid genuine-success, dontWipeDevDB
restart within `retryTime`, re-sent archive accepted, `complete`, not re-run),
verified non-vacuous in review. `ClientRetryTime == 24h` is guarded by D2.2.

## Verdict

The reliable2 phase-2 changes are **validated on real LSF**:
- Issues 1–3 (responsiveness): control RPCs stay fast under churn — PASS.
- Issue B/B2 (churn): 0 `bad job` / 0 `not running`; 40k drains 100%; 160k
  drains deadlock-free with the false half fully buried — PASS.
- Issue 4 (web counts): delta feed tracks live jobs and agrees with the CLI;
  no diverging absolute counter; terminal-only groups hidden — PASS.
- A scale-only **deadlock** (pre-existing scheduler-group lock held across
  `bsub`) surfaced at 160k was root-caused and **fixed** (`dfa196f`, with a
  committed deterministic regression test); re-run confirmed deadlock-free.

Two DB-preserving-restart-dependent sub-checks (Issue-4 CLI-shows-N-after-restart
and Idea-1 crash-recovery) are **structurally not CLI-testable on the isolated
dev deployment** (a development `wr manager start` always wipes the DB); they
are covered deterministically in-process by Tier-A tests A3.2 and D2.1
(verified non-vacuous).

The only remaining 160k limitation is the pre-existing, explicitly-deferred
over-submission / slow-`bsub` problem (bugfix 260722-1), which caps throughput
at very large scale but does not regress the phase-2 behaviour and is out of
this spec's scope. **Recommendation:** the reliable2 phase-2 work is
merge-ready on its own terms; a clean 160k end-to-end throughput demonstration
depends on landing 260722-1 (over-submission cap).
