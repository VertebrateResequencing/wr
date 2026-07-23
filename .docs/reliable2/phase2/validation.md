# reliable2 Phase 2 — Tier-B real-LSF validation

Status: **reliable2 phase-2 fixes VALIDATED on real LSF** — churn eliminated,
control RPCs responsive, Issue-4 web/CLI agreement, Idea-1 crash-recovery, and a
scale-only scheduler deadlock found and fixed (`dfa196f`). The 160k
over-submission stall was root-caused to bugfix **260722-1** and **also fixed**
(`5d60467`): the 160k churn now drains fully (160,000/160,000). The two
DB-preserving-restart checks (Issue-4-after-restart, Idea-1) were completed on
an isolated production-mode manager (prod preserves the DB). No known
outstanding issue.

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

### 160k over-submission stall — root-caused to bugfix 260722-1, then FIXED

Initially, after the deadlock fix, the 160k `rgtrue` backlog stopped making
forward progress (stuck at a few thousand of 80000) while `rgfalse` drained —
the manager stayed deadlock-free and responsive. A goroutine dump showed the
scheduler saturated by **slow LSF external commands**: goroutines stuck 2+
minutes in `submitToQueue` (`bsub`) and many in `killExcessCmds → parseBjobs`
(`bjobs`) — i.e. wr emitted one giant uncapped `bsub` array per group and then
drowned in `bsub`/`bjobs`/`bkill`. This is exactly bugfix **260722-1**.

**260722-1 was then fixed** (commit `5d60467`, via `/bugfix`): cap the emitted
`bsub` array at `maxBsubArraySize` (default 1000) and chunk the remainder; add a
bounded `bsub` exec timeout; add exponential retry backoff with Warn→Error
escalation. Local scheduler unchanged; C3 reserved-element protection intact.

**Re-run at 160k with both fixes: full drain.** `rgtrue` 80000 + `rgfalse`
80000 `buried` = **160,000 / 160,000 jobs terminal**, `jarchive: bad job` =
**0**, `jrelease: not running` = **0** throughout; runners scaled to hundreds
(capped arrays are accepted quickly); control RPCs stayed responsive. No
scheduling-resumption bug: a kick of fresh jobs scheduled and completed cleanly.
An early run showed a ~0.004% `bkill`-race bury under `--retries 0` (a runner
bkilled in the tiny window before its reserve registers; harmless with retries,
down from the pre-fix ~95% bkill rate).

**Final full-scale re-confirmation on complete HEAD `ed3f523`** (all phase-1..5
+ deadlock fix `dfa196f` + 260722-1 `5d60467` + the `wr/backoff` retry-backoff
refactor `ed3f523`): 160k churn **FULLY DRAINED at t+478s — 160,000/160,000**
(`rgtrue` 80000 `complete`/0 `buried`; `rgfalse` 80000 `buried`), **0 `bad job`
/ 0 `not running`** throughout, control RPCs **77-262 ms** the entire run,
runners scaled 100→500, steady forward progress, no deadlock, no stall (this run
had zero bkill-race buries). Everything works end-to-end at full scale.

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

**DB-preserving restart** (second Issue-4 sub-check): a `development`
`wr manager start` always wipes the DB (`initDB(..., wipe = !dontWipeDevDB)`;
`dontWipeDevDB` is a test-only config, no CLI/env switch — the
`WR_RELIABILITY_KEEPDB=1` in repro.md does not exist in code), so this was
re-run on an **isolated production-mode manager** (dev and prod differ only in
config-file naming, default manager dir, and DB-wipe behaviour; run with its own
config yml, ports 51782/3, and manager dir — never touching the real production
managers). Result: after a DB-preserving prod-mode restart, the CLI still shows
`complete: 300` (DB preserved) while the web `/status_ws` shows **nothing** for
the still-terminal-only rgX (no diverging counter). Then adding 20 live jobs to
rgX, web and CLI **agree exactly** at the same instant — web `rgX
{complete:300, ready:18, running:2}` vs CLI `{complete:300, running:2,
ready:18}` (the scan-on-connect seeds per-RepGroup complete from
`getCompleteJobsByRepGroup`; `"+all+"` tracks live-only). On the pre-revert code
the web showed a diverging `complete:0`; now there is no diverging counter.
Also covered deterministically in-process by Tier-A A3.2.

### Idea-1 crash-recovery: PASS (isolated production-mode manager)

Run on real LSF via the isolated prod-mode manager (prod preserves the DB, so a
still-owned running job is recovered on restart). A single genuine-success job
(marker file counts executions) was reserved+started on an LSF runner; the
manager was killed mid-run (the LSF runner survived), then restarted preserving
the DB within `retryTime`. Result: the runner's re-sent archive was **accepted**
— rgCR `complete: 1` — and the command ran **exactly once** (marker = 1 line,
not re-run). Cleanup killed only the specific runner jobid (never a `wrp_*`
pattern). Also covered deterministically in-process by Tier-A D2.1;
`ClientRetryTime == 24h` guarded by D2.2.

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

The 160k over-submission stall was root-caused to bugfix **260722-1** and
**fixed** (`5d60467`): the re-run drains **160,000 / 160,000** jobs with 0
churn, responsive throughout, no deadlock. **Recommendation:** the reliable2
phase-2 work plus the 260722-1 fix are merge-ready; the only residual is a
~0.004% `bkill`-race bury under `--retries 0` (harmless with retries), an
inherent reserve-window race far outside this work's scope.
