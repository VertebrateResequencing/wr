# reliable2 phase 2 — reproducing the *remaining* failures at LSF scale

Follow-up to `.docs/reliable2/` (the Option-R revert that fixed false-lost /
false-deleted and startup). The user reports that after deploying this branch
with the real client (`portal_builder`), the correctness issues are fixed but
**new/remaining problems** appear that "would not have happened with v0.36.5":

1. Web UI unresponsive to clicks on the bars.
2. `wr status` (CLI) unresponsive to detail requests.
3. `wr suspend` and `wr limit` changes time out (but eventually work).
4. (added mid-investigation) Web UI and `wr status` **disagree on the number
   of completed jobs** in a repgroup; not fixed by refresh; obvious when adding
   or rerunning jobs in a repgroup that was previously completed with many jobs.

This document records how to reproduce, what reproduced, what did not, and why.
Fix ideas are in `ideas.md`. A prerequisite scheduler bug found on the way is
written up as a `/bugfix` in `.docs/bugfixes/260722-1.md`.

---

## Environment (isolated; production never touched)

- Host `farm22-wrstat01` (8 cores), real IBM LSF. **Do not touch** the
  production `wr manager` processes (deployment `production`, job names `wrp_*`
  / others). The test manager is deployment `development`, config dir
  `/nfs/users/nfs_s/sb10/wr-r2/config` (ports 51780/51781), job names `wrd_*`.
- Binary: rebuilt from branch HEAD (`7229449`) with
  `CGO_ENABLED=1 go build -tags netgo -o /nfs/users/nfs_s/sb10/wr-r2/wr .`
  (on NFS so LSF exec nodes can run the runner).
- Safety: `bjobs | grep -c wrd_` must be 0 before; after, kill the manager and
  `bkill` all `wrd_` arrays. **`wr manager stop` hangs under load** (see §Issue
  3), so tear the dev manager down by killing its PID directly
  (`kill -9 $(cat .wr_development/pid)`) — but verify the PID is the dev binary
  first, and never kill a `production` manager.

```bash
export WR_CONFIG_DIR=/nfs/users/nfs_s/sb10/wr-r2/config
export WR=/nfs/users/nfs_s/sb10/wr-r2/wr
$WR manager start --deployment development -s lsf --debug   # --debug shows scheduling
```

Fast-failing jobs = `false`; fast-succeeding jobs = `true`. Both exit
immediately, exactly per the request ("simple commands that just exit").

---

## Tool: `wsprobe` — read the web UI's own data (for Issue 4 and any web-UI/status bug)

The web UI does **not** use the mangos RPC socket; its status bars are fed by
the `repGroupCounts` counter, pushed as `jstateAbsolute{RepGroup,Counts}` JSON
over the `/status_ws` websocket (`serverWebI.go:394`, `webInterfaceStatusWS`).
So to test what the **web UI actually shows** (as opposed to what the CLI or DB
say), read that endpoint. `wsprobe` is a ~40-line gorilla/websocket client that
connects, collects the latest `Counts` per RepGroup, and prints them — i.e. it
sees exactly what the browser would render. Source (committed):
`.docs/reliable2/phase2/wsprobe/`.

```bash
# build (its own module; uses the module cache, same gorilla/websocket as wr)
GOFLAGS=-mod=mod GOPROXY=off go build -C .docs/reliable2/phase2/wsprobe -o /tmp/wsprobe .

# the web token is the ?token=... in the URL wr prints at 'manager start'
# (stable across restarts); or capture it:
TOKEN=$($WR manager start --deployment development -s local 2>&1 \
        | grep -oE 'token=[A-Za-z0-9_-]+' | head -1 | cut -d= -f2)

# read the web UI's counts for a few seconds (host:webport token [seconds])
/tmp/wsprobe localhost:51781 "$TOKEN" 3
# -> WEBUI rg="rgX"   counts=map[running:8 ready:12]     (note: no 'complete' => 0)
# -> WEBUI rg="+all+" counts=map[running:8 ready:12]
```

Compare that against the CLI/DB view for the same repgroup at the same instant:
`wr status -i <rg> -o counts` (a DB scan). Any disagreement on `complete` (or any
state) is a web-vs-CLI divergence. Notes: a repgroup with **no live job** is
omitted from a fresh subscriber's seed (so `wsprobe` prints nothing for a
fully-completed repgroup — that alone is a divergence vs the CLI's scan); the
counter is **never seeded from DB history**, so after a restart the web counts
start from zero while the DB/CLI still hold the history (this is the Issue-4
mechanism, see below). `wsprobe` skips TLS verification (the dev manager uses a
self-signed cert).

---

## Prerequisite bug: a large batch of *identical-requirement* immediate jobs is unschedulable (→ `.docs/bugfixes/260722-1.md`)

The naive reproduction — 160,000 identical `false` jobs — **cannot be
scheduled at all**, so no load is generated:

- All 160k identical-requirement jobs collapse into ONE scheduler group
  (`server.go:3593-3614`, `group.count++`, no cap). `lsf.schedule`
  (`lsf.go:797`) passes the whole `stillNeeded` to `generateBsubName`
  (`lsf.go:980-988`), which emits an **uncapped** array `-J name[1-160000]`.
- LSF **hangs for minutes** accepting an array that large. Confirmed directly:
  `bsub -q normal -M 1024 -R '…' -J 'wrd_arraytest[1-160000]' -o /dev/null
  -e /dev/null false` did **not** return within 2 minutes (this farm's
  `MAX_JOB_ARRAY_SIZE` is 200000, so it is *within* the limit — a performance
  cliff, not a rejection). `submitToQueue` runs `bsubcmd.Output()` with **no
  timeout** (`lsf.go:825-844`), so the scheduler blocks; no runners ever launch,
  the debug log shows `rac scheduling jobs … count=160000` then silence, `0`
  `wrd_` jobs, and **no error**.
- `[1-3000]` and `[1-5]` submit and run fine.

Also found (secondary): when a `bsub` *does* error (e.g. wr's queue heuristic
picks `tiger22-inference`, which rejects via `esub` because it needs `-G sXXXX`),
`scheduleRunners` logs `warn "Server scheduling runners error"` and
`retryScheduleRunnersLater` (`server.go:5282`) retries **forever with the same
oversized count**. Note the heuristic-picks-a-bad-queue path is a *different*
thing from "queue/queues_avoid ignored": a per-job `queue:"normal"` **is**
honored (`determineQueue`, `lsf.go:1046-1067`, returns the forced queue
directly); the branch's first commit `9a311f8` fixed only the *client-side*
aliasing of `queues_avoid`, which is not involved here.

**Workaround used for the rest of this investigation:** spread the batch across
many scheduler groups by varying `memory` per job, so each group's array is
small (≤ ~1–3k). This is only a workaround — real `portal_builder` avoids the
bug incidentally because learned per-ReqGroup recommendations give its jobs
varied requirements.

```bash
# 160k across 64 memory-groups (arrays ~2500 each) — schedules fine
perl -e 'for my $i (1..160000){my $m=1000+(($i%64)*100);
  print "{\"cmd\":\"false #g$i\",\"queue\":\"normal\",\"memory\":\"${m}M\"}\n"}' > mg.json
$WR add -f mg.json --rep_grp mg --retries 0
```

---

## What reproduced

Fair share on `normal` limited me to **~250–2800 concurrent runners** across
runs (the user had "1000s"); this was enough to reproduce most symptoms and is
noted where it bounds a result.

### Issue B (the core): double-reservation churn — one command runs on two runners

The `jarchive: bad job` / `jrelease: not running` rejects are the **losing half
of a double-reserved job**: the same command is handed to two runners; one wins
(archives/removes the item) and the other's completion is rejected. Root cause
(isolated by code trace — the old "Idea 3"):

1. **The item's TTR is armed at Reserve, not at Started** (`queue.Reserve` calls
   `item.touch()` at `queue/queue.go:1509`; `handleStart` does not re-arm it).
   `ttrCallback` sends a **reserved-but-not-yet-Started** item to `SubQueueDelay`
   (`server.go:3352-3356`, the `StartTime.IsZero()` branch) → delay → ready →
   **re-reservable by a second runner**. So if a runner's first `Started`/`Touch`
   is delayed past the `ItemTTR` (60s) minus the reserve→first-touch latency —
   which happens under the single-reader RPC backlog during a connect storm — the
   job is double-reserved. (A job that *has* started is instead parked in
   `SubQueueRun` and its owner's late completion is still accepted — the retained
   v0.36.5 leniency; so the divergence is specifically the reserved-not-started
   window.)
2. **The LSF `checkCmd` bkill race** (`lsf.go:1177-1201`, race documented in the
   code): wr over-submits runners (one `[1-1000]` array per group) then `bkill`s
   the "excess non-RUN" every scheduling cycle; a PEND element that transitions
   to RUN (and reserves+starts a job) between the snapshot and the `bkill` is
   killed mid-job, orphaning its Run item, which later TTRs out and is
   re-reserved. (Confirmed indirectly: a clean 40k run left **38,302 array
   elements `EXIT`ed (bkilled) vs 2,506 `DONE`** — wr bkills the vast bulk of the
   runners it over-submits.)

**The two flavours differ in consequence, which matters for the fix:**

**B1 — succeeding jobs (`true`): the loser's archive is a HARMLESS DUPLICATE.**
`ErrBadJob` means the item was already removed by the winning runner
(`getij` `serverCLI.go:1691`), so **the job IS completed — no work is lost**.
Verified: a clean isolated 40k `true` run (40 groups, foreground manager)
completed **40,000 / 40,000**, `ready:0`, `badjob:0` despite the heavy bkill
churn. The **cost is wasted double-execution**, not lost completions. For
trivial `true` commands that is just noise; for the original workload's
**multi-minute `jq`/`zopfli` commands it is catastrophic** — every double-run
burns 2–3 CPU-minutes, and if it recurs the workflow appears to make little
progress (the original "19,394 bad job, complete≈0"). (An earlier draft of this
doc reported B1 as "frozen at 207 / forward progress lost"; that reading came
from the confounded stale-manager phase and from misreading the by-design
`complete: 0` of the unscoped CLI — corrected here: succeeding-job progress is
**not** lost, it is duplicated.)

**B2 — failing jobs (`false`): the loser's release livelocks the runner.**
160k `false` jobs (multi-group), runners ramping 656 → 1268 → 2781:
`6000+` × `releaseJob failed … Release(<key>): not running`. Here the loser
sends **`jrelease`** (client always releases a normal non-zero exit —
`client.go:2323/2339-2390/2165`); on the removed/not-Run item `Queue.Release`
returns `ErrNotRunning` (`queue/queue.go:1596-1602,131`), and because
`handleRelease` uses `getij(checkRunning=false)` (`serverCLI.go:1116-1134,1675`)
the client gets `ErrInternalError` (not `ErrBadJob`), which its
`reportFinalState` loop treats as transient and **retries for 24h,
disconnecting/reconnecting every 15s** (`client.go:2084-2131`), pinning the
runner so it never reserves again. **This is the real forward-progress killer**
for failing workloads (throughput → 0). Fixing the client give-up (Idea 1)
breaks the livelock; preventing double-reservation (Idea 2) removes the cause.

> Note: B2 pins runners in a slow 15s retry, which paradoxically keeps the RPC
> *rate* low, so control ops stayed fast during pure-`false` runs. B1 (success)
> keeps the RPC rate high (see Issue 1/2/3 below).

### Issues 1/2/3 — manager unresponsive to status / detail / suspend / limit under high-churn load

Under ~2000 runners on a high-churn (succeeding) workload, **every** control
RPC timed out:

```
counts   60.0s rc=124   (wr status -o counts)
limit    60.0s rc=124   (wr limit -g none)
suspend  60.0s rc=124   (wr suspend -i nosuch)
detail   60.0s rc=124   (wr status -i <rg> --limit 2)
```

i.e. the web-UI-bar-click server work (`getJobsByRepGroup`), `wr status`
details, and the `suspend`/`limit` control commands are all unresponsive — the
reported symptoms 1–3. Root: RPC **reception** is a single `sock.RecvMsg()`
reader (`server.go:2656/2671`); handling is dispatched to goroutines, but under
a high-churn reserve/start/touch/archive storm the reader cannot admit control
RPCs promptly, and the single change-callback drainer (below) backs up. (At
~250–350 runners, control ops were fast — the degradation is load-dependent, as
the user described "eventually work".)

**`wr manager stop` also hangs under this load** — my scripted stops timed out
and the daemons survived (which is *why* the environment accumulated stale
managers mid-investigation). This matches the earlier "manager stop needs
kill -9" report.

### Issue 4 — completed-count divergence between the **web UI** and the CLI (reproduced end-to-end)

> Correction: an earlier draft cited `wr status -o counts` (complete: 0) vs
> `wr status -i <rg> -o counts` (complete: 207) as the divergence. That is
> **wrong** — the CLI by design only reports *incomplete* jobs unless you scope
> to a repgroup, so `complete: 0` from the unscoped form is expected, not a bug.
> The real bug is between the **web UI** and the CLI, and is reproduced below by
> reading the web UI's own endpoint (`/status_ws`), not by comparing two CLI
> invocations.

**Reproduced with the local scheduler (no LSF needed) + `wsprobe`** (the
committed `/status_ws` client — see the "Tool: `wsprobe`" section above for how
to build/run it; it reads the exact `jstateAbsolute{RepGroup,Counts}` messages
the browser consumes):

1. Fresh manager, add 300 jobs to repgroup `rgX`, all complete. CLI
   `wr status -i rgX -o counts` → `complete: 300`. A web UI connected during the
   run saw them; a web UI that connects *now* (rgX terminal-only) is sent
   **nothing** for rgX — already a divergence (fresh load shows 0, CLI shows
   300), because a terminal-only repgroup is omitted from the subscriber seed
   (`rgcSeedCountCopy` returns nil when `!rgcHasLiveJob`, and `complete` is not
   "live": `rgcHasLiveJob`, `repgroupcounts.go`).
2. Restart the manager **preserving the DB** (`WR_RELIABILITY_KEEPDB=1`). The
   counter `repGroupCounts` is **not seeded from DB history** — it starts empty
   (see the comment at `serverWebI.go:930`). CLI still scans the DB:
   `wr status -i rgX -o counts` → `complete: 300`.
3. Add 20 live (`sleep 60`) jobs to `rgX` so it reappears as an active group,
   then read both views at the same instant:

```
CLI  wr status -i rgX -o counts   -> complete: 300  running: 8  ready: 12   (DB scan)
WEB  /status_ws  rgX              -> {running: 8, ready: 12}                (complete = 0)
WEB  /status_ws  +all+            -> {running: 8, ready: 12}
```

**The web UI shows `rgX` complete = 0; the CLI and the DB show 300.** Same
repgroup, same instant. It is **not fixed by refresh** (reconnecting gets the
same fresh, history-less seed), and it is **obvious exactly when you add/rerun
jobs in a previously-completed repgroup** (the added jobs make `rgX` reappear in
the web UI, but with complete=0 instead of its true 300) — matching the report
precisely.

Root: the web UI is driven by the in-memory `repGroupCounts` counter
(`jstateAbsolute` over `/status_ws`), which (a) is never seeded from DB history
so it loses all completes across a restart, and (b) omits terminal-only
repgroups from a fresh subscriber's seed. The CLI's per-repgroup view scans the
DB. Two sources of truth → they disagree on completed counts. This machinery is
exactly what a web-front-end revert would remove.

---

## What did NOT reproduce (honest limitations)

- **A clean "1000s of runners, responsive-but-slowly-degrading" latency
  curve.** Fair share (depleted by repeated large submits) capped me at a few
  hundred to ~2.8k runners, and the two churn regimes (B1 saturates RPCs, B2
  throttles them) prevented a single monotonic latency-vs-runners curve. The
  60s-timeout result above was taken during a higher-runner phase whose job mix
  was confounded by the stale-manager/port issue; it is a solid "unresponsive
  under load" reproduction but not a clean characterization.
- **queues_avoid being dropped by the scheduler** — did **not** reproduce; the
  forced queue is honored (see the prerequisite-bug section).

> (The trigger that moves a still-alive job out of Run — previously an open
> question — is now identified: the reserved-not-started TTR eviction and the
> `checkCmd` bkill race, see Issue B. A live goroutine dump at the stall was not
> needed to conclude it; the code trace plus the clean-40k-vs-churny-120k
> contrast were sufficient.)

---

## Why these happen now and (largely) not on v0.36.5

The release/archive rejection code, the TTR/lost handling, and the 24h client
retry are **essentially identical to v0.36.5** (verified against tag commit
`11fe092`: `queue/queue.go` `ErrNotRunning`, `serverCLI`/`server` release/bury
branching, `ttrCallback` shape, `client` retry constants). So this is **not** a
new strictness regression. What changed is the **saturation threshold**:

- v0.36.5 fanned out each transition batch on a **fresh goroutine**
  (`go queue.changedCb(...)`). reliable2 funnels **all** change-callbacks
  through a **single serial drainer** goroutine (`queue/queue.go:263`,
  `runChangedCallbacks`).
- reliable2 also runs `repGroupCounts.applyTransitions` on **every** transition
  **unconditionally** (`jobtransition.go:75`), and — because a connected status
  web UI registers into the same `clientSubscriptions` map —
  `hasAnyClientSubscriptions` (`server_subscription.go:532`) returns true and
  **defeats the intended idle fast-path**, so every transition also pays a
  per-job subscription scan inside that single drainer.

Net: reliable2's hot path is heavier **and** serialized, so the manager stops
keeping up (reserve/touch/archive backlog crosses the 60s TTR, control RPCs
queue) at a **lower** runner count than v0.36.5 — the churn (Issue B) and
unresponsiveness (Issues 1–3) trigger earlier. And the **count divergence
(Issue 4) exists only because the counter machinery was kept** — v0.36.5 had no
server-side count map (it computed deltas locally and broadcast them; nothing
accumulated/drifted).

**All of this machinery is exactly what a web-front-end revert would remove** —
see `ideas.md` for the "was not reverting the web front end a mistake?" answer.
