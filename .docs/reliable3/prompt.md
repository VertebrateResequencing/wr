# Spec input: stop over-provisioning runners at LSF scale, and make lost-job confirmation failures loud instead of silent

## Goal & priority (non-negotiable)

Reliable running of jobs at LSF scale is the top priority. Despite #547–#552,
production (`v0.37.1-5-g06cee70` = `reliable3` HEAD) still **churned and then
permanently stalled** on a ~37k-job workload (limit group `results_portal`,
limit 2000): 30522 complete / 503 lost / 5633 pending, a trickle of progress, and
~185k of ~200k runners exiting having run 0 commands. Full evidence and the
causal chain are in **`.docs/reliable3/background.md`** — read it first.

Required outcomes:
- The manager does **not** request many multiples of the actually-runnable work as
  runners. The runner flood and idle-exit waste are drastically reduced — which
  removes the manager saturation that mass-produces false-lost jobs and the churn.
- When the manager **cannot confirm** a lost job's fate (ssh/forced-command
  misconfig, private key not loaded), it **fails loudly** (logs) instead of
  silently assuming "alive" forever — so a broken reclaim path is immediately
  diagnosable, not a silent stall.
- No user-facing behaviour change except more robust/efficient internals (per
  `[[speedups-internal-only]]`); do not break any `reliable2` KEEP feature or the
  concurrent-reader work.

Scope note: this spec does **not** change the lost-job reclaim *mechanism* itself.
The reclaim path stays the existing ssh `ProcessNotRunningOnHost` confirmation; the
live production stall is unstuck **operationally** by correcting the
`~mercury/.ssh/authorized_keys` forced command (validated line in `background.md`).
This spec's job is to (§1) make a broken confirmation impossible to miss and (§2)
remove the over-provisioning flood that mass-produces lost jobs in the first place.

## What is already done (baseline = branch `reliable3`)

- `reliable3` = `reliable2` + #551/#552; all of #547/#548/#550/#551/#552 live.
- **Concurrent RPC readers exist** (`numRPCReaders = 6`, `serveClients`/
  `serveClientsReader`, spec B1) — RPC admission is no longer the bottleneck; do
  not "add concurrent readers" again.
- **v0.36.5-lenient archive acceptance is present and working**
  (`handleArchive`→`getij(checkRunning=true)`): a holding owner's successful
  archive is accepted while the item is in `ItemStateRun`. Do not regress this.
- **Docs already added on this branch** (do not redo): expanded `privatekeypath`
  help in `cmd/conf.go` (ssh lost-job check + command/parse contract + example
  restricted `authorized_keys`), and a CONTRACT WARNING on
  `ProcessNotRunningOnHost` (`jobqueue/scheduler/scheduler.go`).
- **Operational, NOT part of this spec:** the deployed forced command in
  `~mercury/.ssh/authorized_keys` still emits the pre-#510 line count; correcting
  it (validated line in background.md) is a mercury-side change that unsticks the
  live manager. The competing `nf-main_rnaseq` pipeline / fair-share starvation is
  the user's to manage.
- **§2a IS ALREADY IMPLEMENTED on `reliable3`** — reimplemented via `/bugfix`
  (commit `324c746`, checklist `.docs/bugfixes/260724-1.md`): the
  shared-per-limit-group-budget fix in `countJobInGroup` (`server.go`,
  `seedLimitGroupBudgets`) + its regression test
  `TestReliable3LimitGroupOverProvision` (`jobqueue/reliable3_overprovision_test.go`).
  (An earlier hand-rolled prototype of the same fix was reverted so `/bugfix` could
  redo it via TDD + reviewer.) The `developers/wrdev.sh overprovision-check` gate
  landed earlier on the branch. Treat all three as baseline (like the docs above);
  the test + gate MUST stay green. Remaining §2 work (2b consistency + priority-fair
  allocation) is in §2.

## The desired solution (implement in this priority order)

### 1. Make lost-job death-confirmation failures loud and diagnosable
The production stall's root was that the ssh death-confirmation
(`confirmJobDead`→`ProcessNotRunningOnHost`, `scheduler.go:495`) returned `false`
("still alive / can't confirm") **every time all day** (0 confirmed-dead kills),
silently — because PR #510 changed the remote command from `ps -p <pid> | wc -l`
(dead ⟺ `"1\n"`) to `ps -o stat= -p <pid> 2>/dev/null || test $? -eq 1` (dead ⟺
empty), which silently broke the site's forced-command contract. The reclaim
mechanism is corrected operationally (Scope note); the code change here is to make
such a failure **loud**, so it can never again masquerade as a healthy manager:
- **1a. Distinguish outcomes and log the bad one.** `ProcessNotRunningOnHost` must
  distinguish "confirmed alive" / "confirmed dead" / "could not determine", and
  **log at warn** on could-not-determine (ssh error, `getHost` failure, or output
  that is neither empty nor a plausible process state). It must never silently
  return "assume alive" on unparseable output. (Consider rate-limiting the log so a
  persistent misconfig doesn't flood, but it must be visible.)
- **1b. Log the key-load failure.** `lsf.initialize` (`lsf.go:234`) currently does
  `if content, err := os.ReadFile(privatekeypath); err == nil { store }` and
  **swallows** a read error, leaving an empty key (all ssh then fails). Log a warn
  when the key cannot be read.

### 2. Stop over-provisioning runners — removes the flood that causes the churn
~95% of runners idle-exit; the manager requested `count=3313` for a 2000-limit
group, and up to **13,271 runners summed across sibling groups in one rac cycle**
(6.6× the limit); 126× `checkCmd bkill failed "No matching job found"` (culling
runners that already idle-exited). Two mechanisms (`background.md` finding 2):
- **2a. Limit-group capacity shared across sibling scheduler groups — DONE
  (`324c746`).** `groupRemainingCapacity` used to cache remaining capacity keyed by
  **scheduler group**, but the limit is per **limit group**, so each sibling
  scheduler-group string mapping to the same limit group independently received the
  full remaining capacity within one rac cycle (measured: up to 10 sibling groups,
  summed request 13,271 vs a 2000 limit, 455/1558 cycles over). `countJobInGroup`
  now shares a **single budget per limit group per rac cycle** (`server.go`,
  `seedLimitGroupBudgets`), decremented as each ready job is counted, so the
  aggregate ready request for a limit group can never exceed its remaining capacity.
  Proven by `TestReliable3LimitGroupOverProvision` (Σ ≈ K×limit pre-fix → Σ ≤ limit
  post-fix) and `wrdev.sh overprovision-check`. **Remaining refinement (spec this):**
  the budget is allocated **first-come** across siblings; if sibling groups differ in
  priority, allocate the shared budget preferring higher-priority groups (a two-pass
  build, or order the ready scan by group priority) so a low-priority sibling can't
  starve a high-priority one of the shared budget.
- **2b. Do not inflate `count` with phantom/lost Run entries.**
  `accountForRunningJobs` (`server.go:3929`) adds **all** Run-sub-queue jobs
  (including lost-parked phantoms) on top of the (now-capped) ready count, and the
  limiter-usage read is not atomic with the running snapshot — so a single group's
  `count` can still exceed the limit under a burst (3313 > 2000). Make the count
  accounting consistent (e.g. seed §2a's per-limit-group budget from the SAME
  limiter read used for the running snapshot, and/or cap each group's final `count`
  at remaining + its own running) so `count` for a limit-grouped scheduler group
  never exceeds the limit group's true remaining capacity even with running jobs
  present. (§2a's test covers the ready-only case; add a running-jobs case here.)
- Preserve enough headroom for wr's pull model (a modest over-request is fine), but
  never request N×limit or limit+phantoms.

### 3. Observability (support §1 and §2, and prevent silent recurrence)
- Log confirmed-alive/dead/could-not-determine outcomes (§1a) so a stuck reclaim is
  visible in the manager log.
- A debug/metric line for **requested-vs-reservable** runners per scheduler group
  and per limit group, and current limit-group utilisation (held vs limit), so
  over-provisioning and phantom-slot build-up are visible without a bbolt dump.

### 4. Documentation contract (mostly done — verify + extend)
- Keep the `cmd/conf.go` `privatekeypath` docs and the `ProcessNotRunningOnHost`
  CONTRACT WARNING (already added). If a future change touches the remote command or
  its parse, **update those docs in the same change** and add a migration note
  (sites with a forced command must update it), so this class of regression cannot
  recur silently.

## KEEP — must remain fully working (do not remove or break)

- The concurrent RPC readers (`numRPCReaders`, spec B1) and everything on the
  reliable2 KEEP list (subscriptions #503; live RAM/CPU/STDOUT #530/#534; reconnect
  resync; rerun/modify/suspend/resume; `wr status --recent`; `--sync`).
- v0.36.5-lenient archive acceptance (holding owner's success accepted while in
  `Run`) — the reliable2 correctness fix.
- The existing lost-job detection/confirmation behaviour: a genuinely dead runner is
  still detected and its job re-run; an alive/late-touched job is never wrongly
  reclaimed. §1 only adds logging around the outcome, it must not change the
  alive/dead decision for the working (correctly-configured) case.
- The prior-state recovery window (`recoverInBackground`/`isRecovering`/
  `ErrRecovering`/`rescheduleReadyAfterRecovery`).
- The scheduler-group deadlock fix (`scheduleRunners` on a snapshot, no sgroup lock
  across `bsub`; lock order `psgmutex` before `sgroup`) and the bsub-array cap
  (`maxBsubArraySize`) from reliable2/phase2 — do not reintroduce those hazards.

## Acceptance criteria (TDD targets)

1. **Over-provisioning bounded (§2a — already green, must STAY green + be extended).**
   `TestReliable3LimitGroupOverProvision` (committed, `324c746`) asserts that with
   several sibling scheduler groups mapping to one limit group and a ready backlog far
   exceeding the limit, the **sum of runners requested for that limit group is ≤ the
   limit**. It passes now and must not regress; run under `-race`. **Extend it** with
   (a) a case that also has running jobs in those groups, asserting a single group's
   `count` never exceeds remaining + its own running (§2b), and (b) a mixed-priority
   case asserting the shared budget favours the higher-priority sibling (§2a
   refinement).
2. **The wrdev.sh over-provisioning gate stays green (must pass).** `developers/wrdev.sh
   overprovision-check` (committed) runs the above test at production scale
   (default limit=2000, 50 siblings) and reports the summed requested-runner peak
   ≤ limit; on the pre-fix accounting it reported ~limit×siblings. Required gate.
3. **Loud on misconfig (§1).** A could-not-determine confirmation and a key-load
   failure each produce a log line (assert via a captured logger / test hook). The
   alive/dead decision for a correctly-configured check is unchanged.
4. **No regression.** Steady-state throughput ≥ current; a genuinely dead runner is
   still detected and its job re-run, an alive job is never wrongly reclaimed, and a
   holding owner's successful archive is still accepted; `make test`/`make race`/
   `make lint` green; concurrent-reader and reliable2 KEEP tests still pass.

## Reserved (out of scope now; revisit only if measured necessary)

- **Reducing `queue.mutex` contention** (finer-grained/sharded locking). The single
  global `queue.mutex` (`queue/queue.go:353`) is the throughput ceiling under the
  flood, but the primary lever is §2 (remove the flood); only pursue lock
  restructuring if, after §2, the mutex is still the bottleneck at target scale. It
  is high-risk (touches every transition) and must not reintroduce the reliable2
  deadlock class.
- **Damping scheduler-group re-learning oscillation** (coalescing sibling groups
  that share a limit group, or hysteresis on req re-learning). Legitimate group
  variance is expected; only pursue if group thrashing remains a material source of
  idle runners after §2.
- **A reclaim path independent of ssh** (e.g. an LSF-`bjobs`-native liveness check on
  the reserving runner's scheduler element, and/or a bounded give-up that releases a
  lost job after N failed confirmations). Deliberately deferred: the current
  ssh-based reclaim is being fixed operationally and made loud (§1); an alternative
  mechanism is only worth building if the ssh path proves unreliable again after §1
  makes its failures visible.

## Constraints

- Internal-only behaviour change (`[[speedups-internal-only]]`): job-running
  semantics and user-facing output must not change; this is robustness + efficiency.
- Honour the DEVELOPERS.md anti-patterns: no locks held across `bsub`/`bjobs`/
  `bkill`/`WriteJSON`; no server-wide lock on the transition hot path; lock order
  `psgmutex` before `sgroup`; cap/chunk bsub arrays.
- Validate at LSF scale on the farm before shipping (see Testing) — the in-process
  test proves the accounting invariant; the farm run proves it removes the flood.

## Testing (how to validate)

- **Committed deterministic Go test (must stay green): `TestReliable3LimitGroupOverProvision`**
  (`jobqueue/reliable3_overprovision_test.go`, `324c746`) — sets a limit group to
  limit N, feeds many ready jobs across K sibling scheduler groups (distinct
  requirements, same `~lg` limit-group suffix) through `countJobInGroup` with a
  shared budget map, and asserts the **summed `sgroup.count` across the siblings ≤
  N**. It failed pre-fix (Σ = K×N) and passes post-fix (Σ = N). Parameterisable via
  `WR_OP_LIMIT` / `WR_OP_SIBLINGS` / `WR_OP_READY` (defaults 4/5/20) so wrdev.sh can
  run it at production scale. Extend per acceptance #1 (running-jobs + priority
  cases). Run under `-race`.
- **Committed high-scale harness helper in `developers/wrdev.sh`** (see acceptance
  #2): reproduces the over-request with many sibling scheduler groups sharing one
  limit group and reports the summed requested-runner peak vs the limit. Must show
  ≤ limit (+headroom) with the fix. This is the required scale gate.
- Use the isolated dev/prod managers and helpers per repo-root **`DEVELOPERS.md`**
  and `developers/wrdev.sh` (never touch `--deployment production` / real `wrp_*`;
  dev wipes the DB so use an isolated prod-mode manager for restart/DB-preserving
  checks; `-f` + SIGQUIT for goroutine dumps; unset `OS_*` for fast tests per
  `[[skip-openstack-tests]]`).
- Farm-scale LSF run (fair-share permitting): a limit-grouped workload with sibling
  scheduler groups — assert the idle-exit rate drops sharply vs baseline, runners
  requested per limit group stays ≤ limit (+headroom), and completions continue
  (no permanent stall). Metrics: runners-requested vs jobs-completed, idle-exit
  count, `bad job` error rate, and (with the authorized_keys corrected) confirmed-
  dead kills > 0.
- For §1: a mock scheduler whose `ProcessNotRunningOnHost` returns garbled/erroring
  output asserts that a log line is emitted and the alive/dead decision for a
  correctly-configured check is unchanged.
- Read-only production artifacts used this investigation (for reference, do not run
  a manager on them): `/nfs/hgi/wr/lsf/.wr_production/log`, `runner_logs/`,
  `.tmp/db` (bbolt read only).

## Notes (authoritative clarifications, verified this investigation)

- **#510 attribution (verified via git):** `ProcessNotRunningOnHost` changed from
  `ps -p <pid> | wc -l` (dead ⟺ `stdout=="1\n"`) to
  `ps -o stat= -p <pid> 2>/dev/null || test $? -eq 1` (dead ⟺ `""`/`Z*`) in commit
  `939ae54` (#510). The deployed mercury forced command still emulates the old
  count contract, so current wr always reads "alive". Proven locally: the wrapper
  returns `3` for both a live pid 1 and a dead pid 999999 (wr's `2>/dev/null || test
  $? -eq 1` even injects pids `2` and `1`, which always exist).
- **The stall is limit-group-mediated.** Only limit-grouped workloads stall this
  way: unconfirmable lost jobs hold limit slots (`ttrCallback` parks lost in
  `SubQueueRun` without decrementing; slot freed only on complete/release/confirmed-
  dead). A workload without a limit group would churn but not stall on this.
- **`accountForRunningJobs` counts lost-parked phantoms** because lost jobs stay in
  `SubQueueRun`; that is a second reason a single group's `count` can exceed the
  limit (2b), independent of the sibling-group over-count (2a). The shared-budget
  invariant (§2a) computed from one consistent limiter read fixes both.
- **The 8928 `bad job` errors were concentrated in one hour (12:00)** and are largely
  benign duplicate-archive retries after a timed-out-but-server-side-successful first
  attempt (client `receive time out` → reconnect → `ErrBadJob` because the job already
  completed) — not, by themselves, discarded successes. The genuine harm is the
  wasted runners (§2) and the orphaned lost jobs that the (operationally-fixed)
  confirmation must reclaim.
- **Why jobs went lost (verified from runner logs):** it is saturation-driven
  false-lost + orphaned-after-failed-archive, NOT runner deaths. Sample of 126
  runners that started a command: **0 kill/termination signals**, ~44% `receive
  time out`, ~52% `bad job` + "will need to be rerun" — alive runners defeated by
  RPC latency. TTR is 60s and touch interval 15s (comfortable margin), so lost is
  caused by the manager not *processing* touches/archives in time under the flood,
  not by sparse touching or LSF eviction. Therefore §2 (remove the flood) is the
  primary *prevention* of lost jobs. The lost count ratchets via positive feedback
  (lost/orphaned jobs keep counting toward the runner target and hold slots → more
  runners → more flood → more false-lost).
