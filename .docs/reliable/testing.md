# Reliability testing strategy: wr manager under LSF-scale load (v0.36.5 vs current)

## Why this document exists

Since `v0.36.5` a large body of work landed to make the status **web UI**
accurate (absolute-state broadcasting `#533`, job subscriptions `#503`,
`statusState` seeding `#547`, and the callback-ordering rework in `#547`). The
concern: those changes may have slowed or destabilised the **actual running of
jobs** — especially on an LSF cluster with tens of thousands of jobs, thousands
running at once, and the manager database on **NFS**.

**Priority (from the brief):** the running of jobs must be fast and reliable,
with *no possibility of the web UI affecting operations*. Web-UI accuracy is
secondary; worst case we revert it to the pre-`0.36.5` behaviour.

This document is the **shared harness and baseline** that every idea in
`idea1.md … idea5.md` is measured against. It records exactly how to reproduce
each symptom and the measured `v0.36.5`-vs-current numbers.

## The two reported symptoms

1. **Runtime non-responsiveness** — with thousands of running jobs the manager
   becomes unresponsive (which then prompts a `kill -9`).
2. **Stuck restart** — a `kill -9`'d manager gets stuck / non-responsive on the
   way back up, seemingly hung.

Both must be reproduced and attributed before proposing fixes.

---

## Environment

- Host `farm22-wrstat01` (8 cores), real IBM LSF (`bsub`/`bjobs`), `ptrace_scope`
  restricts `strace`/`gdb` (use the Go execution tracer / env-gated timing).
- Storage axis (first-class variable): **local** `/dev/vda3` (`/tmp`) vs **NFS**
  (`/nfs/hgi/sb10`, and the repo's home NFS). The production manager DB is on NFS.
- Real production DB copy: `.tmp/db` — **1.7–6.2 GB** (sparse), **1,927,290**
  completed jobs, **3,498,928** reverse-lookup (RTK) entries, **24,018**
  repgroups, only **26** live jobs in the snapshot. Biggest repgroups:
  `ibackup_server_put` = 700,709; `ip13.bsftools_stats` = 361,320;
  `portal_…_compress` = 215,444. A read-only `Stats` walk of the complete bucket
  over NFS alone takes **3m07s** — a baseline for how slow full scans are here.

---

## Tooling (all under `/tmp/wr-reliable`, nothing committed)

- `wr-head` — current `HEAD` (`4e1b5d9`), clean, `go build -tags netgo`.
- `wr-v0365` — `v0.36.5` (`git worktree` at that tag), clean.
- `wr-head-safe` / `wr-v0365-safe` — the same code plus three **env-gated** test
  guards (patch kept at `/tmp/wr-reliable/reliability-hacks.patch`), so the real
  code can be exercised against the real DB with zero risk of running a real
  command:
  - `WR_RELIABILITY_NOSCHED=1` — `scheduleRunners` returns immediately (never
    spawns/`bsub`s a runner); also logs the scheduler group it *would* have used
    (`RELNOSCHED group=…`) for test-driver discovery.
  - `WR_RELIABILITY_KEEPDB=1` — do not wipe the dev-deployment DB on start (so we
    can start on a copy of the real DB / accumulate state across restarts).
  - `WR_RELIABILITY_TIMING=1` — log startup phase timings (`RELSTARTUP`,
    `RELPRIOR`, `RELEQ`) so `initDB` / `loadPriorState` sub-phases are visible.
- `loadrunner-head` / `loadrunner-v0365` — a tiny tool that connects as **N
  concurrent "runners"** and drives the real `Reserve→Started→Touch→Archive`
  client protocol **without executing any command**, so the manager hot path can
  be stressed at arbitrary scale on one node, and DBs with thousands of *running*
  jobs can be constructed. Modes: `drive` (reserve→archive until dry, reports
  throughput + reserve/archive latency), `hold` (reserve+start N jobs and keep
  them running), `ping` (measure `Ping` latency = responsiveness). It also runs a
  background pinger during `drive`/`hold`.
- `inspect` — read-only bbolt inspector (bucket presence, key counts, top
  repgroups by RTK count). Confirms the real DB is fully upgraded (both
  `depgroups` and `jobLookupEntries` buckets present ⇒ the one-time upgrade does
  **not** re-run).

### Safety protocol (followed for every run)

Isolated `WR_MANAGERDIR` + custom ports (`517xx`), `--deployment development`,
`WR_MANAGERHOST=localhost`; never touches the production manager. For LSF runs:
`WR_RELIABILITY_NOSCHED=1` guarantees no `bsub`; pre-run refuses if `wrd_` jobs
already exist; post-run `bjobs -w | grep wrd_` must be 0; a copy of the real DB
is used (never the live one) and no job command from it is ever run.

---

## Metrics

- **M1 Startup time-to-responsive** — wall time from `manager start` to a
  successful `manager status`, plus the `RELSTARTUP`/`RELPRIOR` phase breakdown.
  *This is the "stuck restart" metric.*
- **M2 Steady-state throughput** — jobs/s completing under real runners.
- **M3 Responsiveness under load** — `Ping` latency p50/p95/p99/max while the
  manager is busy. *This is the "runtime non-responsiveness" metric.*
- **M4 Add time** — wall time to `wr add` a batch (and add-time seeding cost for
  history-heavy repgroups).
- **M5 Scale decay** — does throughput fall as the completed history grows?

---

## Scenarios and results

### S1 — Steady-state churn throughput (M2, M3): NOT a regression

8 real runners (`-s local --max_cores 8`), 5000 trivial jobs, run to completion.

| storage | v0.36.5 | current (HEAD) | verdict |
|---|---|---|---|
| local | 204 jobs/s (24.6 s) | **343 jobs/s (14.6 s)** | HEAD faster |
| NFS   | 95 jobs/s (52.8 s)  | **110 jobs/s (45.6 s)** | HEAD faster |

`Ping` p95 stayed sub-few-ms for both. **Conclusion:** the `#503` per-job
slowdown was fixed by `#535` (gopsutil own-PSS) and current HEAD is *faster* than
`0.36.5` at this concurrency. Steady-state job execution is **not** regressed by
the web-UI work. So the reported symptoms are **not** a general throughput
regression — they are about **scale** and **startup**.

Repro:
```
exp1.sh <wr-binary> <label> <base-dir> 5000 8 true <port>   # both binaries, both storages
```

### S2 — Stuck restart with thousands of running jobs (M1)

Construct a DB with N running jobs (`loadrunner hold`), `kill -9` the manager,
restart, time to responsive. Storage = NFS. `WR_RELIABILITY_TIMING` gives the
phase split.

| scheduler | jobs | restart time-to-responsive | dominant phase |
|---|---|---|---|
| **local** | 40k incomplete / 10k running | **175 s** (repeatable each restart) | `recoveredItemDef` loop = 178 s |
| **LSF** (production) | 40k incomplete / 10k running | **8.3 s** | `recoverIncompleteJobs` decode ≈ 7 s |

Two distinct findings:

- **local-scheduler bug:** `local.recover` calls `process.Processes()` (a full
  `/proc` scan, made ~2× slower by `#503`'s gopsutil v3→v4) **per running job**,
  so recovery is O(running × processes) — 10k running ⇒ ~175 s, and it re-runs on
  every `kill -9`, i.e. the exact "stuck restart loop". This affects *local*
  deployments; the user's production is LSF (whose `recover` is a no-op), so this
  is **not** the production cause but is a real bug.
- **LSF path is fast for job *count* alone** (8.3 s for 40k) — the synthetic DB
  had *no completed history*, so `seedStatusStateForItemDefs` was cheap (360 ms).
  The production cost comes from history — see S3.

The whole of `loadPriorState` runs **before** the readiness token and
`serveClients`, so during it the manager is up but answers nothing (same ordering
as `0.36.5`, but with new work added — `markPersistedJobStatusGroups` and
`seedStatusStateForItemDefs`, both `#547`).

Repro:
```
exp_startup_ab.sh <wr-safe> <loadrunner> <base> 40000 10000 <port> local   # 175s
exp_startup_ab.sh <wr-safe> <loadrunner> <base> 40000 10000 <port> lsf     # 8.3s
```

### S3 — Production LSF restart on the REAL DB (M1): the web-UI seeding cost

The real cause of the LSF stuck-restart: on every start `statusState` is rebuilt
from scratch, and `seedStatusStateForItemDefs` (`#547`) scans **every live
repgroup's RTK entries** doing a **cold `Get` into the 1.9 M-entry complete
bucket** per entry. A single live job in a big repgroup (`ibackup_server_put`,
700k) forces a 700k-entry cold scan over NFS. This also fires at **add** time the
first time a history-heavy repgroup is seen.

**Measured (LSF, real DB copy on NFS, cold cache):**

- First `wr add` of one job to `ibackup_server_put` (700k history):
  **190.98 s** — a normal `add` blocked for **>3 minutes** by web-UI seeding.
- Immediately-subsequent adds to the next-biggest repgroups (`ip13.bsftools_stats`
  361k, `portal_…_compress` 215k, …): **~0.06 s each** — because the first cold
  scan faulted the 1.9 M complete bucket into the OS page cache, so later seeds
  are warm. **The cost is the first cold scan after a fresh process.**
- Fresh start on the real DB with only the snapshot's 26 live jobs:
  `initDB` 894 ms, `loadPriorState` 249 ms, total **1.8 s** (no big repgroup live
  yet ⇒ seeding cheap). The cost only appears once a big repgroup becomes live.
- **Restart with 46 incomplete jobs spread across the big repgroups:
  162.59 s to responsive**, of which `seedStatusStateForItemDefs` = **161.6 s**
  (`initDB` 538 ms, decode ~0 for 46 jobs, deps 4 ms, markPersisted 113 ms — all
  negligible). i.e. the entire 2.7-minute stuck-restart is the web-UI seeding
  re-scanning the live big repgroups' cold history, with only 46 jobs to recover.

**Attribution (answers "is it a regression?"):** the 160–190 s is entirely
`seedStatusStateForItemDefs` → `retrieveCompleteJobCountsByRepGroups`, a function
introduced by `#547`. It **does not exist in v0.36.5** (which has no `statusState`
and no per-repgroup seeding at all), so v0.36.5 pays neither the add-time nor the
restart-time cost — its restart here would be `initDB` + decode of 46 jobs ≈ a
couple of seconds. This is a **pure regression introduced by the web-UI-accuracy
work**, on both the `add` and startup paths.

**Confirms the brief's doubt about the 260713 fixes.** `260713-2` item 1 claimed
to fix slow startup by deleting the *global* completed-history scan and seeding
"only RepGroups becoming live". That reduced the worst case but **did not remove
the O(history) scan** — it merely scoped it to live repgroups. When a live
repgroup *is* one of the huge ones (700k), the scan is still 190 s. So the item
is correctly described as not really fixed for the real workload; the mechanism
is intact and still on the operational path.

This is the smoking gun for **both** symptoms: `seedStatusStateForItemDefs`
(`#547`, web-UI machinery) runs a **cold O(repgroup-history) scan on the `add`
path and on the restart path**. A single job added to (or recovered into) a
history-heavy repgroup blocks that operation for minutes. `serveClients`
dispatches each request in its own goroutine, so this does not freeze *every*
client, but the blast radius is still operational: the seeding holds
`statusSeedMutex` for the whole scan (so concurrent adds to other unseen
repgroups serialise behind it) and runs a **minutes-long BoltDB read
transaction**, which pins the mmap and contends with the write path when
concurrent archives need to grow the DB. At restart the scan is *entirely* on the
readiness path, so the manager is fully non-responsive for its duration. This is
precisely "the web UI affecting operations". (The exact whole-manager-freeze
blast radius during a live add was characterised from the code, not separately
timed; the add-stall and restart-stall themselves are measured above.)

Repro:
```
exp_realdb_seed.sh <base-dir> <port> 15     # LSF, real DB copy, safe
```

### S4 — initDB (bolt.Open) cost on the large churned DB (M1)

Opening the real 6.2 GB DB read-write (freelist load) before anything else:

| storage | initDB |
|---|---|
| local `/tmp` | 12.6 s |
| NFS | 1.0 s |

Pre-existing (`bolt.Open(nil)` default array freelist), but grows with DB
size/churn; contributes seconds to every start regardless of running-job count.

### S5 — Scale decay under high archive concurrency (M5)

`loadrunner drive`, NFS. Sustained throughput **decays within a run as the
completed bucket grows** (1000 workers/60k: 1110 → 296 archived/s; 300 workers/30k
HEAD: 2080 → 1178/s). **A/B (30k jobs, 300 workers):**

| | throughput | Ping p99 |
|---|---|---|
| v0.36.5 | 481 archived/s | 3.4 ms |
| current (HEAD) | **691 archived/s** | 4.6 ms |

**Not a regression:** HEAD is *faster* than v0.36.5 here too, and both decay the
same way — the decay is inherent BoltDB write-cost growth as the file grows, not
the `#547` per-archive read tx. So the archive/steady-state path is fine; the
per-archive `markPersistedJobStatusGroups` read tx is a minor cleanup target
(Ideas 1e/3), not a cause of the reported symptoms. 1000 idle-connected workers
alone push `Ping` p99 to ~15 ms — a mild connection-scaling cost, not a stall.

Repro:
```
exp_drive_ab.sh <wr-safe> <loadrunner> <base> 60000 1000 <port>
```

---

## Root-cause summary (what to fix)

**The single dominant cause of both reported symptoms is `#547`'s `statusState`
seeding (`seedStatusStateForItemDefs` → `retrieveCompleteJobCountsByRepGroups`),
which does a cold O(repgroup-history) scan into the 1.9 M complete bucket on the
`add` path and the restart path.** On the real DB over NFS this is **190 s to add
one job** to a big repgroup and **162 s of a 162 s restart**. It is web-UI
machinery on the operational critical path, and it **did not exist in v0.36.5** —
a pure regression from the web-UI-accuracy work.

Ranked:

1. **`statusState` seeding scans cold history (`#547`) — PRIMARY.** Blocks `add`
   (190 s) and restart (162 s). Both symptoms. → Ideas 1a/1b (move off the path),
   3 (maintain counts so no scan), 4 (remove from manager), 5 (no history in the
   hot store).
2. **Startup blocks on `loadPriorState` before `persistToken`/`serveClients`.**
   The structural reason a slow recovery = a non-responsive manager = the kill-9
   loop. → Idea 2 (respond first, recover in background).
3. **`local.recover` does a `/proc` scan per running job** (local scheduler only,
   worsened by `#503`): 10k running → 175 s. Not the LSF production cause but a
   real bug. → Idea 1c.
4. **`initDB` freelist load** grows with DB size (pre-existing, up to 12.6 s). →
   Ideas 1d, 5.
5. **Per-archive read-tx / per-touch decompression** — minor per-op cost; **not**
   a regression (S5: HEAD's archive throughput > v0.36.5's). Cleanup only. →
   Ideas 1e, 3.

**Steady-state throughput is NOT regressed** (S1: HEAD ≥ v0.36.5). The problems
are **startup blocking** and **history-scanning web-UI machinery on operational
paths**. Every idea is judged against the numbers above; see `idea1.md`–`idea5.md`
(surgical → non-blocking-startup → maintained-counters → decoupled-projector →
storage-split), each with its own trial checklist reusing this harness.
