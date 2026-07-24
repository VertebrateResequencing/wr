# reliable3 — background: why jobs still churn and stall at LSF scale

## Context

Date: 2026-07-24. This investigates the **live production** `wr` manager after the
`reliable`/`reliable2` work (PRs #547–#552) was deployed, because jobs still
churned (`lost`/`delayed` oscillation) and then stalled at scale.

- Production binary: **`wr manager v0.37.1-5-g06cee70`** = `reliable3` HEAD — every
  #547/#548/#550/#551/#552 fix is live.
- Manager: run by user **`mercury`**, `managerhost farm22-ibackup01`, scheduler
  `lsf`. Config: `/nfs/hgi/wr/lsf/.wr_config.yml`.
- Workload: **~37,000 `portal_20260724T115039_diff` jobs** — short `jq` diffs of
  ~13 MB JSON files, submitted by a `results_frontend`/`portal_builder` run from
  `/lustre/.../mw31/pops/gen`. All are in **limit group `results_portal`, limit
  2000** (read from the DB; other limits e.g. `irods`=10, `bcftools`=100).
- Evidence, all **read-only** (production untouched):
  `/nfs/hgi/wr/lsf/.wr_production/log` (168k+ lines, 09:22–15:51 BST), a sample of
  `/nfs/hgi/wr/lsf/runner_logs/26.07.24/` (~200k files), `bjobs -u mercury`, and a
  `bbolt` read of the DB copy at `.tmp/db` (never opened by a manager).

## Reported symptoms (user, over the day)

- ~292 → 364 → 503 jobs going `lost` while >1000 ran; oscillation `lost`↔`delayed`
  (~500 in each), running rising then collapsing, repeating.
- By ~15:00: stalled at **30522 complete / 503 lost / 5633 pending**, with only 1–2
  jobs occasionally running to completion.
- Of the ~200k LSF runners spawned, **~185k exited having run 0 commands**.
- "490 runner logs appeared but only 16 jobs ran on 3 runners" in a short window.

## Findings

### 1. THE STALL — lost jobs are never reclaimed (confirmed root cause; a regression from PR #510)

When a running/reserved job stops being touched within its TTR (`ServerItemTTR =
60s`, `server.go:179`), `ttrCallback` (`server.go:3581`) marks it `Lost`, **parks
it in `SubQueueRun`, and does NOT release its limit slot** — correct, because the
runner might still be alive. The slot is only released when the job later
completes, is released, or is **confirmed dead**. Confirmation
(`confirmJobDead` → `Scheduler.ProcessNotRunningOnHost`, `scheduler.go:495`) sshes
to the job's host (as `mercury`, using `privatekeypath` = `~/.ssh/id_wr_farm`) and
runs `ps -o stat= -p <pid>`, treating **empty output as "dead"**. It returns
`false` ("still running / can't confirm") on **any** ssh/getHost error, and does so
**silently** (no log).

**It never succeeds in production.** `lvl=info` appears only 7 times in the whole
log (3 manager-starts, 4 recovery) — so `"killed a job after confirming it was
dead"` fired **zero times all day**, despite ~500 lost jobs. Live, only **10
`wrp_` runners are alive**, yet the `results_portal` limiter believes ~1500–2000
slots are occupied → ~1500+ **phantom slots** held by jobs whose runners are long
gone → the 2000 limit is exhausted → new work is limit-skipped → **stall**.

**Why confirmation always fails** — a contract regression. Git history of
`ProcessNotRunningOnHost`:
- Since #406/#463 it sent **`ps -p <pid> | wc -l`** and parsed dead ⟺ `stdout ==
  "1\n"` (header-only = no such process).
- **PR #510** ("Fix OpenStack reserved quota leak on spawn errors", commit
  `939ae54`) changed it to **`ps -o stat= -p <pid> 2>/dev/null || test $? -eq 1`**
  and parse dead ⟺ `stdout == "" || HasPrefix("Z")`.

Mercury's per-key **SSH forced command** in `~mercury/.ssh/authorized_keys` (a
security wrapper limiting the key to a `ps` check on all farm nodes) still
implements the **old count contract**:

```
command="echo $SSH_ORIGINAL_COMMAND | grep -o '[0-9]\+' | xargs ps -p | wc -l"
```

So current wr receives a **line count**, never `""`/`Z*` → `state == "" ||
HasPrefix(Z)` is **always false** → every job is judged "alive" → nothing is ever
confirmed dead. Extra insult: wr's `2>/dev/null || test $? -eq 1` injects the
literal digits `2` and `1` into the wrapper's `grep`, and pids 1 & 2 always exist,
so even a dead pid yields a count ≥ 2. Proven locally (no prod touched): the
deployed wrapper returns `3` for BOTH a live pid 1 AND a dead pid 999999.

This is latent since #510 (~a year, unrelated to the reliability work) and only
becomes catastrophic at LSF scale, where enough lost jobs accumulate to exhaust a
limit group. The key, ssh, and `ps` are all fine — the fault is purely the
command/parse **contract** between wr and the site's forced command.

**Immediate operational fix (validated locally, not applied — mercury's key):**
correct the forced command to emit the raw stat wr now expects, e.g.
`command="p=$(echo \"$SSH_ORIGINAL_COMMAND\" | grep -oE '[-]p [0-9]+' | grep -oE
'[0-9]+' | head -1); ps -o stat= -p \"${p:-0}\" 2>/dev/null || test $? -eq 1"`
(alive→`Ss`, dead→`""` exit 0; still ps-only, injection-safe; mercury's login
shell is bash). It takes effect on the next ssh connection (no sshd restart); the
manager's lost-job retry loop then reclaims the phantom slots and un-stalls
**without a manager restart** (restart is the guaranteed fallback).

### 2. Runner over-provisioning — ~95% of runners idle-exit

Runner-log sample: **1236 of ~1300 runners ran 0 commands** ("wr runner exiting,
having run 0 commands, because there are no more commands in scheduler group …"),
matching the user's ~185k/200k. Two distinct over-count mechanisms make the
manager request far more runners than can ever hold a limit slot:

- **(a) Per-scheduler-group limit accounting.** `groupRemainingCapacity`
  (`server.go:3858`) caches remaining capacity in `groupLimits[schedulerGroup]` —
  keyed by **scheduler group**, but the limit is per **limit group**. Since one
  limit group is shared by several scheduler-group strings (see finding 4), **each
  sibling scheduler group independently gets the full remaining capacity** in one
  rac cycle, so the aggregate runners requested across siblings exceed the limit.
  Quantified over the 12:00 hour (1558 rac cycles scheduling `results_portal`):
  up to **10 sibling groups co-scheduled in one cycle**, the **summed request hit
  13,271 runners in a single cycle (6.6× the 2000 limit)**, and 455/1558 cycles
  (~29%) requested more than the limit. (The limit is still enforced at reserve
  time via `incrementReserveLimit`, so no more than 2000 actually run — the excess
  just idle-exit, but every one still connects and adds RPC load.) **FIXED
  (`324c746`):** `countJobInGroup` now shares one budget per limit group per rac
  cycle, so the summed request across siblings ≤ the limit; proven by
  `TestReliable3LimitGroupOverProvision` (Σ = K×N pre-fix → N post-fix) and
  `wrdev.sh overprovision-check`.
- **(b) Running/phantom jobs added on top of a capped ready count.**
  `countJobInGroup` caps the *ready* count at remaining capacity, then
  `accountForRunningJobs` (`server.go:3929`) adds **all** Run-sub-queue jobs of the
  group (which includes lost-parked phantoms) on top. Combined with the
  non-atomic read of limiter usage vs the running snapshot, a single group's
  `count` exceeds the limit: observed **`count=3313` for the 2000 limit** at 12:44
  (`saw ready=1696` → `scheduling=3313`, i.e. +1617 running/phantom).

Direct corroboration: **126× `checkCmd bkill failed … "No matching job found"`** —
the scheduler trying to cull excess runners that had **already idle-exited** before
the `bkill` landed.

### 3. Churn under load — the queue mutex, not a single reader

The old reliable/reliable2 "single-reader mangos socket" story is **outdated**:
reliable2 added **`numRPCReaders = 6`** concurrent readers (`server.go:221`,
`serveClients`/`serveClientsReader`, spec B1), each dispatching every request to
its own goroutine. So RPC *admission* is no longer the bottleneck.

The remaining serialization point is the **single global `queue.mutex`**
(`queue/queue.go:353`): every `Reserve`/`Touch`/`Remove`/complete locks it. Under
the over-provisioned flood (thousands of runners reserving/touching/archiving), the
per-request goroutines pile up on this one lock, so RPC **response latency** climbs.
Consequences seen in the 12:00 hour (8928 of 8936 `bad job (not in queue or correct
sub-queue)` errors — `jtouch`/`jarchive` — all in that one hour):
- An alive runner's touch is processed *after* the 60s TTR → the job is
  false-lost-parked (the runner is fine; the manager just couldn't keep up).
- An alive runner finishes its command but its archive times out client-side
  (`receive time out`); it reconnects and retries, and the retry hits `ErrBadJob`.
  Where the first attempt had actually been processed server-side this is a
  **benign** duplicate retry; where it had not, the runner **gives up ("will need
  to be rerun") and exits**, orphaning the job — work done, unrecorded, runner
  gone → TTR expiry → lost (finding 1 then can't reclaim it).

TTR is 60s and runners touch every 15s (`ClientTouchInterval` = `client.go:109`),
so the margin is comfortable in normal operation — jobs go lost only because the
manager can't *process* the touches/archives in time, not because touches are too
sparse. Runner-log evidence (sample of 126 runners that actually started a
command): **0 showed any kill/termination signal**, ~44% logged `receive time out`
+ reconnected, ~52% logged `bad job` + "will need to be rerun". So the runners were
**alive and finished their work** — defeated by manager latency, not killed. The
lost jobs are saturation-driven false-lost + orphaned-after-failed-archive, **not
runner deaths**.

A holding owner's successful archive IS accepted while the item is still in
`ItemStateRun` and owned (`handleArchive`/`getij(checkRunning=true)`,
`serverCLI.go:1036`,`1711`) — the v0.36.5-lenient acceptance from reliable2 is
present and working. The churn is not discarded-success-by-design; it is
latency-driven false-lost + orphaning under the flood.

### 4. Scheduler-group thrashing amplifies over-provisioning

The `results_portal` jobs are spread over **six** scheduler-group strings —
`200:30`, `200:60`, `200:90`, `300:30`, `300:90`, `400:30` (…`~results_portal`) —
because wr re-learns memory/time per req-group (`recommendedReqForGroup`,
`server.go:3792`, rounding to "fewer larger groups"). The `jq` diffs genuinely vary
in size, so multiple groups is legitimate, not a bug — but each string is a
**separate scheduler cmd with its own runner pool**, and they all share the one
`results_portal` limit, which is exactly what feeds mechanism 2(a). When a job's
group changes after re-learning, runners already bsubbed for its old group find no
matching work and idle-exit.

### 5. Operational confound — a competing Nextflow pipeline

`mercury` is simultaneously running a large Nextflow RNA-seq pipeline directly on
LSF: `bjobs -u mercury` shows **2708 `nf-main_rnaseq_*` jobs (584 running)** vs only
**10 `wrp_` runners**. These big STAR/featureCounts jobs consume mercury's LSF
fair-share, so wr's runners spend longer PENDING and win fewer slots — a
throughput drag that deepens the backlog. Note the runner logs show wr runners
were **not** killed mid-job (finding 3: 0 kill signals), so fair-share is an
aggravator of the flood/backlog conditions, **not** the direct cause of the lost
jobs. This is an operational issue, not a wr bug, but it sets conditions that
expose them.

## Live state at 15:51 (manager alive, still stalled)

Trickling (~20 completions per 400 log lines). One rac instant scheduled three
sibling `results_portal` groups at once: `count=477 skip=8859` (200:30),
`count=5 skip=1` (200:90), `count=21 skip=6585` (300:30) — ~15,400 ready jobs
waiting behind a 2000 limit saturated by phantom slots.

## Conclusions — the causal chain

1. Over-provisioning (finding 2) + group thrashing (finding 4) flood LSF with
   runners; ~95% idle-exit.
2. The flood saturates the single `queue.mutex` + BoltDB writes (finding 3) → RPC
   latency into the tens of seconds → **this is why so many jobs went lost**: alive
   runners' touches are processed after the 60s TTR (false-lost), and their
   finished-command archives time out / are rejected (`bad job`) so the runner
   gives up and exits, orphaning the job. Fair-share competition (finding 5)
   deepens the backlog but does not kill runners mid-job.
3. Orphaned/false-lost jobs can never be confirmed dead (finding 1, the #510
   contract regression) → they hold `results_portal` slots forever AND keep
   counting toward the scheduler's runner target → **positive feedback**: more
   runners scheduled → more flood → more false-lost. The 2000 limit fills with
   phantoms → scheduling stalls (30522/503/5633 with a trickle).

Fixing finding 1 (operationally now, and in wr code for robustness) turns the
**permanent stall** into transient, self-healing churn. Fixing findings 2–4
reduces the churn itself and the LSF waste. Finding 5 is for the user to manage.

## What has been done in this investigation

- Confirmed root cause of the stall (finding 1), proven locally.
- Drafted + validated the corrected `authorized_keys` forced command (for mercury
  to apply; not applied here).
- Documentation added (commit `09c10aa`): expanded the `privatekeypath` help in the
  `wr conf` config template (`cmd/conf.go`) to explain the ssh lost-job check, the
  exact command/parse contract, and an example restricted `authorized_keys` line;
  and added a CONTRACT WARNING doc comment on `ProcessNotRunningOnHost`
  (`jobqueue/scheduler/scheduler.go`).
- **Implemented finding 2(a)'s fix via `/bugfix` (`324c746`, checklist
  `.docs/bugfixes/260724-1.md`):** the shared per-limit-group budget in
  `countJobInGroup` (`seedLimitGroupBudgets`) + its regression test
  `TestReliable3LimitGroupOverProvision` (fails pre-fix at Σ = K×N, passes post-fix
  at Σ = N; reviewer-verified; no regression in the limit-group / scheduler-deadlock
  tests), plus a `developers/wrdev.sh overprovision-check` gate that runs it at
  production scale. (An earlier hand-rolled prototype was reverted so `/bugfix` could
  redo it via TDD.)

## What should be done next

See `.docs/reliable3/prompt.md` (input to `/spec-writer`). Remaining code work:
make lost-job confirmation failures **loud** (§1: fail-visibly + log the swallowed
key-load error — the reclaim mechanism itself is corrected operationally via the
`authorized_keys` line); finish finding 2 (§2b: keep a single group's `count` ≤
remaining + its own running under the non-atomic running snapshot; and a
priority-fair allocation of the shared budget across siblings); and observability
(§3). Reserved: `queue.mutex` contention, group-thrash damping, and an
ssh-independent reclaim path — only if measured necessary. Validate end-to-end at
LSF scale on the farm.
