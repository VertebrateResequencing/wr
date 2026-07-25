# reliable4 — LSF-scale stall: rac backlog-rescan + false-lost churn

Live diagnosis of the **production** wr manager (branch `reliable4`, HEAD
`0abeb6b`; run by user `mercury` on `farm22-ibackup01`) on **2026-07-25**, from
`/nfs/hgi/wr/lsf/.wr_production/log`, `/nfs/hgi/wr/lsf/runner_logs/26.07.25/`,
`bjobs -u mercury`, and the config at `/nfs/hgi/wr/lsf/.wr_config.yml`. The
production manager and its jobs were **not** modified. Builds on
`.docs/reliable3` (the memory `reliable3-lsf-stall`).

## Symptoms reported / observed

- Portal workload `portal_20260724T115039_{dedupe,compress}` (each job runs the
  `portal_builder` jq tool), sharing **one** limit group `results_portal`,
  **limit = 2000**.
- ~200 lost jobs appearing/disappearing on `dedupe`; ~2000 `compress` running
  but drifting to `delayed` (~1000); `complete` frozen at 6512 since ~15:50.
- Web UI and `wr status` CLI **time out** — could not drill into why jobs were
  failing.
- No completed DB backup for 20+ minutes.

## What the fixes from #553 / #554 achieved (holding — do NOT re-fix)

1. **Over-provisioning cap (§2a, shared per-limit-group budget) is holding.**
   Per-rac-cycle SUM of scheduled runner requests across the sibling
   `mem:time` scheduler groups was a stable **1773 < 2000** (not N×2000).
2. **SSH dead-confirmation now works.** Previously "0 all day"; now the log
   shows **9,734 "killed a job after confirming it was dead" in 17 min**
   (~9.5/s). So the operational `~mercury/.ssh/authorized_keys` forced-command
   fix landed and the slot-reclaim path functions.

So this is a **new failure mode**, not a regression of the merged work.

## Root cause — two coupled loops

### Issue #1 — `rac` re-scans the whole ready backlog every cycle (O(backlog))

~84k portal jobs sit behind a 2000 limit, so **~80k are permanently in
`ready`+`limitskipped`** (observed `items≈80,438`; `limitskipped` ≈ 52k on the
`300:30` sibling + 28k on `1024:30`, every cycle).

The ready-added callback (`rac`) rebuilds scheduler groups by iterating **every
ready item every cycle**:

- `readyAddedCallback` → `buildSchedulerGroups` (server.go:3948) loops over
  `allitemdata` and calls `prepareReadyJob` for **all ~80k** ready jobs —
  `job.RLock`, requirement lookup, `job.schedulerGroupSnapshot()`, and (on
  change) `q.SetReserveGroup` — even for the ~78k that are limit-blocked and
  cannot be scheduled.
- `countReadyJobsByPriority` then (conditionally) sorts and counts all N
  snapshots.

`rac` runs **back-to-back continuously**: `runReadyAddedCb` re-fires via the
`recall` path (`recallBreak = 500ms`, queue.go:509) whenever any ready-add
happened during the previous scan; under this workload something is always
being added (see issue #3), so `source=recall` dominates and the manager is
**pinned** doing O(backlog) work to produce O(limit) useful scheduling — ~40×
waste, sustained. Measured: `rac started`→`rac finished` pairs occur roughly
back-to-back at ~1/s, each over ~80k items.

This CPU/lock load (the scan takes `queue.mutex.RLock` for the ready snapshot
and for `GetRunningData`, plus per-job locks) **starves the RPC/status paths**.

### Issue #3 — a transient outbound RPC timeout makes a runner kill a healthy command

Confirmed by a runner log (`26.07.25/15-53-03.node-14-25.151117`):

```
15:53:07  reserved a job ... attempts=5
15:53:07  started executing ... pid=151936
15:54:07  command [...] started running, but I killed it due to a jobqueue
          server error: receive time out          <-- exactly +60s
15:54:08  wr runner exiting, having run 1 commands
```

After `exec`, the runner calls `c.Started(job, pid)` to report its PID
(client.go:1675). Under server saturation that RPC **blocks the full client
request timeout (~60s) then returns "receive time out", and the runner kills
the still-healthy `portal_builder` process and bails** (client.go:1679-1685).
The command was running fine; it is destroyed purely because an *outbound
status-report* RPC could not get through.

Note the touch loop already does the right thing — on a touch error it logs
"could not touch" and `continue`s, retrying (client.go:1497-1504). Only the
post-exec `Started()` call (and the initial reserve, seen struggling with
`attempts=5`) turns a transient server-slowness into destroyed work.

The server side then: the reserved job is never confirmed started/kept touched
→ TTR (60s) expires → declared **lost** → later `ProcessNotRunningOnHost` finds
the process gone (the runner just killed it) → "confirmed dead" →
`killJob → releaseJob(FailReasonLost)` **releases it back to delay→ready for
retry** (the "delayed" jobs). So completed/near-complete work is discarded and
re-queued.

### The coupling (why it stalls to zero throughput)

Issue #3's mass false-lost **releases** are themselves `readyAdded` events →
they keep `rac`'s `recall` loop hot (issue #1) → the manager stays saturated →
more `Started`/touch RPCs time out → more #3. Positive feedback. Net result in
the 17-min window: **~0 completions logged** (1 complete-transition in the
whole 200 MB log) despite 1475 runners actively running+finishing jobs — their
results are thrown away and rerun. RPC error mix was **106 `jtouch` bad-job vs 1
`jarchive` bad-job**: jobs are lost *before* they can report, not failing to
archive.

## The status timeout is a symptom of issue #1 (not a separate bug)

When the user **suspended** the portal jobs, `items` in the callback dropped to
0, `rac` calmed to ~1 cycle/10s, completions resumed (872 "completed job" in a
later window), and **status became responsive again** — even though the same
jobs still exist (now suspended). So the web/CLI status timeout was the
**RPC-starvation consequence of the `rac` hot-loop**, and clears once the ready
backlog (hence `rac`) is gone. Fixing #1 fixes the observed status timeout.

**Latent scaling risk (documented, not fixed now):** the status/detail RPCs are
independently O(repgroup-size): `getStatusByRepGroup`/`getJobsByRepGroup` do a
per-key `s.q.Get()` (each taking `queue.mutex`) for every in-queue job of a
repgroup, plus read+sort **all** complete job records from the multi-GB DB
(server.go:1503-1520, 5157-5240). At ~84k this alone approaches the 60s client
timeout under any lock contention. It did not need fixing to restore status
here (freeing the CPU sufficed), so it is left as a reserved item rather than
risking a change to the status/DB path during an incident. Revisit if status
times out with a large *quiescent* queue.

## Amplifier (operational, not code)

The production manager is running with **DEBUG logging ON**: the log grew
72 MB→204 MB in 11 min (~12 MB/min, 75% `lvl=dbug`), dominated by `reserved job`
lines averaging **25 KB each** (escaped-JSON file-list args). This is a large
I/O drain compounding the saturation; turn debug logging off in production.

## Why farm-scale validation missed this

The #553 validation was a *draining* `churn 40000` that peaked ~5.2k runners and
fully drained — a runner-count test below the ~6-7k saturation threshold. This
failure is **not** runner-count driven (only 1475 runners). It is driven by a
**large persistent ready backlog behind a small limit group**: `rac` cost scales
with backlog size, and the false-lost feedback keeps it hot. That scenario was
never load-tested.

## Fixes to implement (this branch)

**Issue #1 — bound `rac` scan cost.** Stop doing O(backlog) per-job work each
cycle when the schedulable set is bounded by a small limit. The per-cycle
expensive work (`prepareReadyJob`-level operations and the recall re-scan) must
be O(schedulable + siblings), not O(ready backlog), while preserving:
priority-fair selection within a limit group, reservability of jobs that *are*
schedulable, and re-scheduling of blocked jobs once capacity frees (a
completion/release already triggers `rac`; the `CheckRunnerTime` timer backstops
it). Reproducer: `developers/wrdev.sh backlog-rescan-check`.

**Issue #3 — never destroy a running command because an outbound status RPC
timed out.** A transient failure of `Started()` (and, symmetrically, the reserve
/ touch RPCs) must not kill a healthy running process; keep the command running
and retry contact in the background (as the touch loop already does), so
server slowness costs latency, not lost work. Reproducer:
`developers/wrdev.sh runner-started-timeout-check`.

Together these break the feedback loop: #3 stops generating false-lost releases
(so `rac`'s recall stops spinning and far fewer jobs are rerun) and #1 caps the
cost of each scan.

**Operational (no code):** disable production debug logging.

## Reserved (only if it recurs after #1+#3)

- O(N) status/detail RPC path (per-key `q.Get` + full complete-jobs DB read).
- `queue.mutex` sharding / rac frequency damping beyond #1.
