# Reliability testing strategy v2: reproducing the *real* failures with the *real* client

## Why this document exists

The previous investigation (`.docs/reliable/`, shipped as `#550`) attacked
**startup seeding** and made **touches cheap** (F0). It was measured almost
entirely with a synthetic `loadrunner` that drives
`Reserve→Started→Touch→Archive` **instantly** (no real command runs). That rig
never exercised the thing production actually does: **thousands of runners each
executing a real command that takes minutes**, all keep-alive-touching and
archiving through the manager at once.

This round reproduces the reported failures with the **real client**
(`results_frontend`'s `portal_builder`, which uses the wr Go `client` package)
against an **isolated** wr manager running **current code** (`reliable2`, i.e.
`develop` + `#547`/`#548`/`#550`) in **`-s lsf`** mode on the real farm.

### The three reported symptoms (as clarified by the user)

1. **Web UI shows jobs becoming "deleted"** at the moment the CLI / reality
   shows them becoming **complete**. (`wr status` on the CLI only ever lists
   *incomplete* jobs, so a job disappearing from `wr status` is normal and is
   **not** the bug; the bug is the web UI rendering `deleted` instead of
   `complete`.)
2. **Jobs end up lost** — work that actually ran (and often succeeded) is
   discarded and re-run.
3. **`wr status` (CLI) stalls** for a long time even when little is happening.

All three are reproduced here as facets of **one** root cause (below).

---

## Environment & isolation

- Host `farm22-wrstat01` (8 cores), real IBM LSF. A **production** wr manager
  (`--deployment production`) is running as the same user and must not be
  touched: the isolated test manager uses **`--deployment development`**, a
  dedicated `WR_CONFIG_DIR` (`/nfs/users/nfs_s/sb10/wr-r2/config`), a dedicated
  `managerdir` and **ports 51780/51781**. Dev job names are `wrd_*`, production
  is `wrp_*`, so they never collide and `bkill`/`checkCmd` can't cross over.
- The manager binary lives on **NFS** (`/nfs/users/nfs_s/sb10/wr-r2/wr`, built
  `CGO_ENABLED=1 go build -tags netgo`) so LSF exec nodes on other hosts can
  exec the runner it `bsub`s. `managerhost: localhost` is fine — wr advertises
  its non-loopback IP to runners itself (`currentServerIP`).
- Real client: `/software/hgi/utils/portal_builder` (a build of
  `results_frontend`), run from `/lustre/.../mw31/pops/gen` with
  `WR_CONFIG_DIR`/`WR_DEPLOYMENT=development` so it connects to the test
  manager. It calls `client.New(SchedulerSettings{QueuesAvoid:"interactive,inference"})`
  (no explicit queue) and adds `portal_diff` / `portal_compress` /
  `portal_dedupe` jobs; each diff/compress command is a `jq`/`zopfli` over
  ~13 MB JSON that runs for **~2–3 minutes**.
- Real DB: a copy of the production `.tmp/db` (1.9 M complete jobs, learned
  per-reqGroup stats). Its 4,795 incomplete jobs are cleared offline first
  (empty the `jobslive` bucket — see `harness/`) so no production commands run;
  the complete bucket + `jobRAM`/`jobDisk`/`jobSecs` recommendation stats are
  left intact. Dev-deployment wipes its DB on start, so the manager is built
  with a one-line env-gated guard (`WR_RELIABILITY_KEEPDB`, same idea as the v1
  harness) to load the real DB copy.

**Safety protocol:** pre-run `bjobs | grep -c wrd_` must be 0; post-run stop
the manager and `bkill` all `wrd_` arrays; production `wrp_` manager left
untouched.

---

## THE reproduction — real jobs + saturation = discarded successful work

Fresh dev DB, `portal_builder -input data/ -patch '$gene/volcano_limma_0.json'
-patch '$gene/volcano_hetero_0.json'` submitted **37,559** jobs. LSF dispatched
them to the **`normal`** queue (queues_avoid honoured — see below), peaking at
**~6,000–7,000 concurrent real runners** on other nodes.

Observed within minutes:

| metric | value |
|---|---|
| LSF array elements `EXIT` (non-zero) | **7,363** and climbing |
| manager log `jarchive(...): bad job (not in queue or correct sub-queue)` | **19,394** |
| `wr status -o counts` `complete` | **~0** the whole time |
| `delayed` | climbing 0 → 830 → 2,011 → 2,479 |
| `lost contact` (sampled) | **0** |
| individual job detail | `Stats of previous attempt: { Exit code: 0; Wall time: 2m31s }` + "2,239 other commands with the same status" |

So: commands **succeed** (`Exit code: 0`, 2–3 min), but the manager **rejects
their archive** ("bad job") and the job is put back to `delayed`/re-run. The
workflow makes **near-zero forward progress** while burning thousands of CPU-
minutes. `wr manager stop` under this load did **not** return (had to be
killed) — matching the reported "manager unresponsive, needs kill -9".

### Why the successful archive is rejected (mechanism)

Defaults: `ServerItemTTR = 60 s`, runners touch every `15 s`. A real diff job
runs 2–3 min, so it must have ~8–12 touches processed *on time*. The manager
reads **every** client RPC (reserve, touch, archive, release, and `wr status`)
through a **single goroutine** on one mangos socket
(`serveClients → receiveClientMessage`). With thousands of runners this reader
saturates, so:

1. A running job's touches are processed late; its TTR expires. `ttrCallback`
   (F0, `#550`) checks `contactedWithin(TTR)` and keeps contacted jobs in the
   run sub-queue — but under real saturation the *touch message itself* is
   backlogged in the socket reader, so the contact isn't recorded in time and
   the job is flipped to `Lost` (or otherwise leaves `SubQueueRun`).
2. The runner's command finishes (exit 0) and it sends `Archive`. But by then
   the queue item is no longer in `ItemStateRun` (or the job's `State` is no
   longer `Running`), so `handleArchive → markJobComplete →
   canCompleteFromQueueState` (serverCLI.go) returns `ErrBadJob` —
   **the successful result is thrown away** and the job is re-queued.
3. Re-run → same fate under continued saturation → **churn**.

F0/`#550` removed *per-touch* work and made lost-detection latency-tolerant,
but did **not**: (a) decouple the single-reader socket, nor (b) make a
genuinely-finished job's archive win over a late TTR flip. So the failure the
real workload produces is untouched by `#550`.

### Symptom 1 (web UI "deleted") is the same root

`jobtransition.go:changeCallbackToState()` decides the state broadcast to the
web UI when a job leaves the queue (`SubQueueRemoved`): it returns
`JobStateComplete` **only if** the removed job's `State == JobStateComplete`,
otherwise **`JobStateDeleted`**. Under the churn, a job's *successful* archive
is the thing that would set `State = Complete`, and that archive is rejected —
so when the job later leaves the queue (lost-confirmation / removal / a
duplicate path) with `State != Complete`, the web UI is told **`deleted`** even
though the command succeeded and the output exists on disk. That is exactly
"the web UI shows jobs becoming deleted when reality shows them becoming
complete."

### Symptom 3 (status stall) is the same root

`wr status -i <rg>` (the heavy `GetByRepGroup → AllItems` path) is read through
the *same* single reader, so it queues behind the runner fleet's
touch/reserve/archive traffic. Measured on this 8-core node: heavy `wr status`
went **26 ms → ~0.4 s (≈15×)** at ~6,000 connected runners and recovered when
they stopped; `wr status -o counts` (the cheap `statusState` path) stayed
~37 ms. The full multi-second freeze needs production scale + a reconnect storm
(runners re-dialing after their touches time out), but the **mechanism and
direction** reproduce here.

---

## Why v0.36.5 was immune (and what it tells us to do)

The user reports this churn never happened on `v0.36.5` — the release *before*
the web-UI-accuracy work (`#503` subscriptions, `#533` absolute-state
broadcasting, `#547` `statusState`/callback rework, `#548`). Diffing the hot
path confirms two concrete, load-bearing differences:

1. **v0.36.5's completion was lenient and recovery-friendly.** Its `jtouch` did
   `q.Touch` + clear a `Lost` **flag** (no snapshot/decompress/subscription
   work); its TTR callback parked a timed-out job in `SubQueueRun` as that flag;
   and its `jarchive` accepted the result with just
   `getij(checkRunning=true)` + `item.Stats().State == Run` + `owner` +
   `Exitcode==0` — crucially **no `job.State` gate and no strict state
   machine**. So a still-alive job whose touch/archive arrived late was simply
   recovered/completed; an alive job was never moved out of `Run` and never
   re-reserved. Late success = accepted.

2. **The current strict state machine + projection is new.** `#547`/`#548`
   introduced `canCompleteFromQueueState` (Run⇒`State==Running`,
   else Lost&&Delayed, else `ErrBadJob`), the `statusState` projection, and
   `changeCallbackToState` (which emits `JobStateDeleted` for any non-complete
   removal). None of these exist in v0.36.5. This layer is what converts a
   transient state-divergence under load into a **rejected successful archive**
   ("lost") and a **`deleted`** broadcast to the web UI. (`JobStateDeleted`
   existed as an enum value in v0.36.5, but nothing *emitted* it on an automatic
   removal — there was no change-callback→state projection.)

Net: v0.36.5 kept the manager cheap on the hot path and **let the runner's
successful result win** even when its own liveness view was briefly wrong; the
accuracy work raised the per-message/per-touch machinery **and** added a strict
state machine + projection that can throw that successful result away and
mislabel it `deleted`. (The exact trigger that moves a job out of `Run` under
real load — runner touch-RPC timeout → give-up/exit, LSF `bkill` of
"extraneous" runners, or release — was not fully isolated; but the invariant
that matters is v0.36.5's: an alive owner's success is never discarded.)

**This directly validates the idea ranking:** restore v0.36.5's two properties
*without* losing the new web-UI accuracy (internal-only, per the speedup rule).
- **Idea 1** restores lenient "owner's success wins" completion (+ its
  `changeCallbackToState` complete-wins fix kills the `deleted` broadcast); the
  attempt-epoch keeps it safe against genuine double-run — the closest match.
- **Idea 2** restores the "manager keeps up / cheap hot path" that `#503`/`#533`
  eroded.
- **Idea 3** hardens the liveness that v0.36.5 got for free by keeping up.
- **Ideas 4/5** go *beyond* v0.36.5 (durable/idempotent outcome), as belt-and-
  braces if 1+2(+3) prove insufficient at full production scale.

The evidence points to **Idea 1 + Idea 2** as the core combination (with Idea 3
hardening), i.e. re-establish v0.36.5's reliability semantics on top of the
retained accuracy machinery.

## What `#550` genuinely fixed (don't regress it)

- **Startup on the real 1.9 M-complete DB is fast now.** Manager start returned
  in **0.74 s** and was `manager status`-responsive in **0.07 s** on the real
  DB copy (live bucket cleared). The old ~162 s seeding stall is gone — the
  per-repGroup complete counter + non-blocking startup work.
- Touches are cheap; the F0 contact-based lost check passes the deterministic
  regression guard.

So of the historic three (startup stall / lost / deleted), **startup is
solved**; **lost and deleted are not** — they were never really about startup,
they're about the single-reader hot path under real multi-minute jobs.

---

## `queues_avoid` — investigated, does NOT reproduce as a drop

The user flagged (as a prerequisite) that `queues_avoid` "doesn't get used"
once recommendations are learned, so "no jobs get scheduled". Findings:

- On a **fresh** DB, portal jobs schedule to `normal` (avoiding
  interactive/inference). ✔
- On the **real** DB **with learned recommendations present**
  (`portal_compress` RAM_rec ≈ 200 MB etc.), portal jobs **still** schedule to
  `normal`. ✔ (verified after fixing an earlier invalid test where the dev DB
  had been silently wiped.)
- A dedicated in-process investigation (fresh + learned-recommendation +
  concurrent + `-race` + full `serve()` end-to-end showing the scheduler group
  RAM transition 400→200 while `scheduler_queues_avoid` persisted) could **not**
  reproduce a drop. Every path preserves `Other` deterministically
  (`reqForScheduler`, `Clone`, `Stringify` sorts keys, `applyRecommended*`
  touch only RAM/Disk/Time, `scheduler_queue` is never injected server-side).

**Real bug found instead (matches the user's "random based on map behaviour"
hint):** `client.determineOverrideAndReq` **mutates and returns the caller's
`*Requirements`**, so a caller that shares one `req` across `NewJob` calls
aliases a single `Other` map; under **concurrent** `NewJob` this is a data race
on that shared map / on the `req.Other` field (Go map corruption / panic),
which can non-deterministically lose `scheduler_queues_avoid`. Fixed by having
`determineOverrideAndReq` clone the incoming req into a fresh `Requirements`
+ fresh `Other` map (and set `OtherSet`) before adding the scheduler keys —
behaviour-preserving for scheduling, but callers can no longer race/alias.
(Tracked in `.docs/bugfixes/260720-1.md`.)

**Alternative explanation for "no jobs scheduled"** worth keeping in mind:
once a learned recommendation raises RAM/Time, the non-avoided queues may no
longer *fit*, so `determineQueue` correctly returns `ErrImpossible` and the
jobs are buried — which *looks* like "queues_avoid stopped working" but is
correct behaviour driven by a bad (possibly corrupted, see below) learned
requirement.

---

## Secondary finding — corrupted time-learning

`db.putJobStats` stores `secs := int(math.Ceil(job.EndTime.Sub(job.StartTime)
.Seconds()))` on every archive. In the real DB, every portal reqGroup's
`jobSecs` bucket contains `-9223372036` (≈ `MinInt64` ns overflow), i.e. jobs
were archived with a **zero `EndTime`**. A clean in-process completion records
a correct positive value, so this comes from an abnormal archive path (the
lost/re-run churn, or old versions). Effect today is mild — the time
recommendation is clamped to 1 s (→ rounded to 30 min in `reqForScheduler`) —
but the learning is garbage and should be made robust (ignore/repair
non-positive durations, and never store a stat for a job whose `EndTime` is
zero).

---

## Metrics (what each idea is judged on)

- **M1 Forward progress under real load** — fraction of *successfully executed*
  commands that are actually recorded `complete` (target: ~100%; today ≈0
  under saturation). The headline metric.
- **M2 Archive-rejection rate** — count of `jarchive: bad job` for jobs that
  exited 0 (target: 0).
- **M3 False-lost of alive jobs** — a running, on-time-touched job never
  observed `Lost` (v1's `TestReliableFalseLostUnderSaturation` guard).
- **M4 Web-UI state fidelity** — a job whose command succeeds is broadcast
  `complete`, never `deleted`.
- **M5 Status responsiveness under runner load** — heavy `wr status` latency
  vs connected-runner count (target: bounded, ≪ today's 15× degradation).
- **M6 Startup-to-responsive on the real DB** — must stay ≤ a few seconds
  (don't regress `#550`).
- **M7 Throughput** — steady-state jobs/s not regressed vs current.

---

## Minimal reproduction (regression-test candidate) — VALIDATED

`harness/reliable2_churn_test.go` (drop into `jobqueue/`, no LSF) reproduces the
core failure deterministically and **already reproduces on current `reliable2`
code**:

`TestReliable2DoubleReservationDiscardsSuccess`: short `ItemTTR`, tiny
`ReleaseDelayMin`. Runner A reserves+starts a job; the manager loses it (as
under saturation) and releases it; runner **B re-reserves the same job** and
starts re-running it; runner A then reports a **successful** completion for the
work it actually did. Result on current code:

```
RESULT archiveErrA=jobqueue jarchive(...): you must Reserve() a Job before
passing it to other methods   counts=map[running:1]
```

i.e. A's genuine success is **rejected** and the command is **re-run by B**
(forward progress lost). This is the exact class the farm shows 19,394× as
`jarchive: bad job`; the deterministic in-process variant surfaces the
`ErrMustReserve` sibling (re-reserved) while the farm also shows the `ErrBadJob`
sibling (flipped out of `Run`, not yet re-reserved). Crucially this is the case
`#548`'s `TestReliableFalseLostRerun` does **not** cover — that test never
re-reserves between loss and archive, so it passes on current code while the
real workload still churns.

The test currently asserts the **buggy** behaviour (`archiveErrA != nil`,
`complete == 0`) so it is a red oracle; a correct fix flips those asserts
(A's work recorded `complete`, or the job never re-reserved while A was alive)
and must not regress the v1 false-lost guard (M3) nor broadcast `deleted` for a
succeeded job (M4). This is the shared harness every idea trial re-runs.

Run it with:
```
cp .docs/reliable2/harness/reliable2_churn_test.go jobqueue/zz_repro_test.go
env -u OS_AUTH_URL -u OS_USERNAME ... CGO_ENABLED=1 go test -tags netgo -race \
    -run TestReliable2 -count=1 ./jobqueue ; rm jobqueue/zz_repro_test.go
```

To exercise the real client end-to-end, build the manager with the one-line
`WR_RELIABILITY_KEEPDB` guard (`harness/reliability-hacks.patch`) so it can load
a copy of the real `.tmp/db` (dev deployment otherwise wipes it on start), clear
the copy's `jobslive` bucket, start `-s lsf`, and run `portal_builder`.

---

## Acceptance criteria — every idea must

1. **M1 ≈ 100% / M2 = 0** under the in-process saturation repro (and, budget
   permitting, a farm `portal_builder` run): successful commands are recorded
   complete, not discarded/re-run.
2. **M4**: no `deleted` broadcast for a job whose command succeeded.
3. **M3**: keep `TestReliableFalseLostUnderSaturation` / the committed
   `TestLostDetectionRecentContactNotLost` green.
4. **M6**: real-DB startup stays ≤ a few seconds (no `#550` regression).
5. **M5**: heavy `wr status` latency under runner load is bounded and does not
   scale badly with connection count.
6. **M7**: steady-state throughput not regressed.
7. No user-facing behaviour change beyond fixing the bug (internal-only, per
   the project's speedup rule).
