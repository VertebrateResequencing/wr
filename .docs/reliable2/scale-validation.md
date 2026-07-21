# Scale / throughput validation (spec §I, phase5 Item 5.2) — RESULT

This records the **in-process saturation validation** (the spec-sanctioned
alternative to the farm run, per `phases/phase5.md` Item 5.2: "an in-process
saturation harness that lowers the reader threshold"). It validates, at the
churn-triggering scale achievable on `farm22-wrstat01` (8 cores), that the
reworked `reliable2` build (completion/lost/deleted reverts A1/B1/C1 in place)
holds the reliability invariants.

Branch `reliable2`; the completion/lost/deleted reverts are all present.

## Verdict: PASS

At every tested connection count from 400 up to **3000 concurrent in-process
runners/connections**, with the TTR set above the saturated reader's RPC
backlog, the invariants hold exactly:

- **M1 forward progress = 1.0000** — every successfully-executed command is
  recorded `complete` (target ~100%).
- **M2 archive-rejections for exit-0 jobs = 0** — no `jarchive: bad job` /
  `ErrMustReserve` for a job that exited 0 (target 0).
- **M4 deleted broadcasts for succeeded jobs = 0**, and authoritative
  `deleted` count = 0 (target 0). No succeeded job is ever broadcast/counted
  `deleted`.
- **A1 invariant: 0 re-reservations** of an alive-owned job.
- **M5 status responsiveness: bounded and responsive** — heavy `wr status`
  under load stays in the single-digit-to-tens-of-milliseconds range (no
  multi-second stall; the pre-fix symptom was "unresponsive, needs kill -9").
- **M7 throughput** reported below (not regressed vs the A1-path expectation).

M3 (false-lost of on-time-touched jobs) and M6 (startup) are covered by the
committed unit suites (`TestLostDetectionRecentContactNotLost`,
`TestReliableFalseLostUnderSaturation`; Phase 2 C2 startup tests) and are not
re-measured here.

## The harness

- File: `jobqueue/reliable2_scale_test.go`, build-tagged `//go:build reliability`,
  so it is **excluded from the default build and `make test`** and runs only as
  an explicit gate. It reuses the in-package test helpers (`serve`, `Connect`,
  `jobqueueTestInit`) and reads the server internals it needs (`server.q`,
  `server.scheduler`, `registerStatusSubscription`/`subscribeToJobs`).
- It scales up the committed A1 oracle `TestReliable2HoldingRunnerArchiveAccepted`:
  1. Start a real in-process server with a short `ItemTTR`.
  2. Add N unique jobs (cmd `true …i`) in one RepGroup.
  3. Register the **web-UI status-details subscription** (the exact
     `newStatusServerSubscription`, `stateChanges=true`) scoped to every job key,
     and drain it, counting `JobStateDeleted` (M4) and `JobStateComplete`
     broadcasts.
  4. Measure heavy `wr status` (`GetStatusByRepGroupMatch` with
     `includeComplete`+`includeStatusDetails` = the `GetByRepGroup`/`AllItems`
     path) latency **idle**, then **under load** while runners churn (M5).
  5. Spawn up to a few thousand persistent runner connections; each loops
     `Reserve → Started(os.Getpid()) → Touch once → [hold cohort: stop touching
     and sleep 1.5×TTR so the TTR callback flags it Lost while its runner is
     alive] → Archive(exit 0)`. Using `os.Getpid()` keeps every runner's process
     genuinely alive, so the async dead-confirmation must find it alive and the
     job must stay owned (never re-reserved) per invariant A1/B1.
  6. Count executed/completed/archive-rejections/re-reservations, and confirm a
     large cohort genuinely reached **Lost-in-Run while its runner was alive**
     (the churn state whose success the pre-revert code discarded).
- **"Lowering the reader threshold":** the server has no separate tunable reader
  threshold — the client-command reader is structurally a single goroutine
  (`serveClients → receiveClientMessage → sock.RecvMsg()` in a loop). Saturation
  is therefore achieved by sheer concurrent-connection count, which is what the
  harness does.
- An embedded **confirm-dead probe** repeatedly asks
  `server.scheduler.ProcessNotRunningOnHost(os.Getpid())` during the churn and
  counts false "dead" verdicts (see the finding below).

### How to run

```bash
# unset ALL OS_ vars inline (OpenStack creds slow/redirect the scheduler tests)
env -u OS_OS_USERNAME -u OS_OS_PREFIX -u OS_REGION_NAME -u OS_PROJECT_DOMAIN_ID \
    -u OS_INTERFACE -u OS_AUTH_URL -u OS_LOCAL_USERNAME -u OS_FLAVOR_SETS \
    -u OS_FLAVOR_REGEX -u OS_USERNAME -u OS_PROJECT_ID -u OS_USER_DOMAIN_NAME \
    -u OS_PROJECT_NAME -u OS_PASSWORD -u OS_IDENTITY_API_VERSION \
  WR_SCALE_JOBS=2000 WR_SCALE_RUNNERS=2000 WR_SCALE_TTR_MS=8000 \
  CGO_ENABLED=1 go test -tags 'netgo reliability' -count=1 -timeout 20m \
    -run TestReliable2ScaleSaturation ./jobqueue -v
```

Env knobs: `WR_SCALE_JOBS` (default 2000), `WR_SCALE_RUNNERS` (default
`min(1000, jobs)`), `WR_SCALE_TTR_MS` (default 2000), `WR_SCALE_LOST_FRACTION`
(default 0.5), `WR_SCALE_M5_SAMPLES` (default 8), `WR_SCALE_DEBUG_LOG` (set to
stream manager logs). Default (no scale env) runs 1000 connections / 2000 jobs /
TTR 2s. Add `-race` for the race pass (use a smaller scale).

## Measured results

All rows below are PASS (M1=1.0, M2=0, M4=0, re-reservations=0, confirm-dead
false-positives=0, `deleted`=0, `complete`=jobs). TTR was chosen above the
saturated per-RPC backlog at each connection count (see the finding).

| connections | jobs | TTR | Lost-in-Run flips | M1 | M2 | M4 | M5 idle → under-load (ratio) | M7 |
|---|---|---|---|---|---|---|---|---|
| 1000 (default) | 2000 | 2 s | 519 | 1.0000 | 0 | 0 | 0.86 ms → 15.5 ms (18×) | 416 jobs/s |
| 2000 | 2000 | 8 s | 986 | 1.0000 | 0 | 0 | 0.85 ms → 22.4 ms (26×) | 141 jobs/s |
| 3000 | 3000 | 12 s | 1500 | 1.0000 | 0 | 0 | 1.08 ms → 7.3 ms (6.7×) | 143 jobs/s |
| 400 (`-race`) | 800 | 5 s | ~400 | 1.0000 | 0 | 0 | 4.5 ms → 19.6 ms (4.4×) | 79 jobs/s |

- **`-race`**: PASS with **no data race** reported in the harness or the
  exercised server paths.
- **M5**: absolute under-load latency stays in the tens of milliseconds — well
  within a "responsive" ceiling and nowhere near the multi-second freeze that
  the bug report described. The *ratio* is noisy and grows with connection count
  because the single-reader socket is architecturally unchanged by Option R
  (decoupling it is out-of-scope Idea 2) and the idle baseline is sub-millisecond;
  the harness therefore asserts an absolute responsiveness ceiling (500 ms), not
  a ratio. Throughput (M7) falls at larger TTR only because each held job
  occupies its runner for 1.5×TTR by design; it is not a manager regression.
- The **headline churn is genuinely exercised**: 519 / 986 / 1500 jobs were
  observed **Lost-in-Run while their runner was alive**, and every one of their
  successful archives was accepted (M2=0) — exactly v0.36.5's "an alive owner's
  success is never discarded". The catastrophic farm failure (successful
  archives discarded, `complete ≈ 0`, thousands of `bad job`) does **not**
  reproduce at any setting.

## Finding: a sub-second TTR at high connection count is a harness artefact (not a regression)

While characterising the harness we found that setting the TTR *below* the
saturated reader's per-RPC processing latency (e.g. TTR ≤ 1 s at ≥ 2000
connections, or ≤ 200 ms at 1000) produces re-reservations and exit-0 archive
rejections (e.g. 2000 conn / TTR 200 ms → executed 3808 vs completed 2000, M2
= 1808). We investigated this thoroughly rather than tuning around it:

- The confirm-dead probe shows `ProcessNotRunningOnHost(os.Getpid())` **never**
  falsely reports the alive process dead (0 / thousands, in isolation and during
  the churn). So the re-runs are **not** caused by a bad liveness verdict.
- With `WR_SCALE_DEBUG_LOG` the manager logs show the re-reserved jobs leave the
  run sub-queue with **no** kill/release/confirm-dead log line — they are
  requeued by the queue's own TTR path.
- Root cause: the queue arms an item's TTR at **Reserve** time. Under saturation
  a runner's `Started`/`Touch` RPC can sit in the single-reader backlog *longer
  than a sub-second TTR*, so when the TTR fires the manager has not yet recorded
  the job as started and correctly applies the **v0.36.5 `ttrCallback` rule
  `StartTime.IsZero() → SubQueueDelay`** (a job that was reserved but never
  started is requeued). The job is then re-reserved and re-run.

This is a property of making the TTR *shorter than a control-plane RPC round
trip*, which cannot happen on the real farm: there the TTR is 60 s while a
`Started`/`Touch` is a single tiny RPC that is processed in well under 60 s even
under a 6–7 k-runner backlog. The real-workload churn is the opposite regime — a
**multi-minute** job whose *touch* is late, which this build handles by parking
it Lost-in-Run and accepting its owner's late success (validated above). The
harness therefore requires the TTR set above the RPC backlog for the connection
count under test (≥ 2 s at 1000 conns, ≥ 8 s at 2000–3000); the header comment
and defaults encode this. Crucially, **even in the artefact regime, `complete` =
jobs and `deleted` = 0** — forward progress and web-UI fidelity are never lost;
only wasted double-execution occurs.

## In-process vs farm scale (honest limitation)

This is the **in-process saturation validation** — the spec-sanctioned
alternative to a farm run. It drives up to 3000 concurrent real client
connections through the real jobqueue server on one 8-core node, reproduces the
genuine churn state (alive runner flipped Lost, then archives success) at that
scale, and shows the reworked build holds M1/M2/M4 and stays responsive (M5),
with M7 reported.

It is **not** a farm run. A full **~6–7 k-runner `portal_builder` run on
`farm22-wrstat01`** (real LSF, real multi-minute `jq`/`zopfli` commands, the
real 1.9 M-complete DB copy, per `testing.md`'s harness + safety protocol)
remains a **recommended pre-merge confirmation**. That run needs the farm
environment (isolated dev-deployment manager on ports 51780/51781, the
`WR_RELIABILITY_KEEPDB` build guard, `bjobs | grep -c wrd_` == 0 pre-check, and
post-run `bkill` of all `wrd_` arrays leaving the production `wrp_` manager
untouched) and **could not be performed autonomously in this session**. No farm
run was performed here; only the in-process harness was executed.
