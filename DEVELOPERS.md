# DEVELOPERS.md — working safely on wr's reliability-critical paths

Distilled from the `.docs/reliable`, `.docs/reliable2`, and
`.docs/reliable2/phase2` investigations so you do **not** have to re-read that
whole history to know what to (not) do. Read this before changing job
scheduling, the RPC server, the status web feed, or the LSF scheduler.

> This file and the `developers/` directory are **developer tooling and
> guidance, not part of the shipped binary**. `developers/` holds scripts,
> the Go command `developers/prodsim` and the Go soak helpers. `make lint`
> lints them and `go build ./...` compiles them; `make test` runs prodsim's
> tests, the only ones there.

Helper: `developers/wrdev.sh` (run `developers/wrdev.sh help`). It encodes the
safe-testing rules below so you don't have to remember them.

Gates: `make test`, `make race` and `make lint` run in CI. `make speed` does
not, but a change to a hot path must pass it before its PR is opened or merged
(see §5).

Open work: `.docs/delivery-queue.md` lists the branches in delivery and the
known bugs deferred from earlier work, each with what was found and the next
step. Check it before starting on an area, and add to it any bug you find but
do not fix.

---

## 1. The critical path — keep jobs running

The one thing that must never regress: a submitted command reaches a runner,
runs once, and its outcome is recorded. That flow is:

```
add → ready → (scheduler launches a runner) → Reserve → Started →
      Touch…Touch → Archive (success) | Release/Bury (failure)
```

Everything else (the web UI, status counts, scheduler-group accounting, metrics)
is **secondary**. If a change trades reliability of this flow for anything else,
it is wrong. Two invariants underpin it and must stay intact:

- **The background recovery window** (`recoverInBackground` / `isRecovering` /
  `ErrRecovering` / `rescheduleReadyAfterRecovery`; `recoveredRunningJobs`). On
  restart, still-owned running jobs are recovered into Run so a re-sent archive
  is accepted. This is what makes "give up on `ErrBadJob`" safe.
- **`ClientRetryTime` = 24h** and the client give-up set = exactly
  `ErrBadJob`/`ErrBadRequest`. Connection errors and `ErrRecovering` must keep
  retrying (crash recovery); only a live manager's authoritative "gone"
  (`ErrBadJob`) makes a runner give up.

---

## 2. Hard rules — anti-patterns that caused real outages

Each of these was a real, diagnosed failure. Do not reintroduce them.

1. **Never hold a lock across a slow/external call.** Holding a per-`sgroup`
   write lock across `scheduleRunners`→`bsub` deadlocked the whole manager at
   scale (archive handlers blocked on that lock while the scheduling callback
   waited for `psgmutex`). Rule: snapshot/`clone` what you need, release the
   lock, *then* call `bsub`/`bjobs`/`bkill`/`os/exec` or a client
   `conn.WriteJSON`. The same applies to `queue.mutex` — never held across a
   client write.

2. **No server-wide exclusive lock on the per-transition hot path.** An
   unconditional exclusive `mu.Lock()` per state transition (the old
   `repGroupCounts.applyTransitions`) serialised dispatch and tanked
   throughput. The transition path may take a shared `RLock` + a per-member
   mutex + a fast non-blocking enqueue — nothing server-wide-exclusive. Keep the
   `hasAnyClientSubscriptions()` zero-subscriber early-out; deliver to clients
   *after* releasing the lock.

3. **Never drop a non-idempotent message.** The status web feed sends
   `jstateCount` *deltas* (from→to increments). Dropping one (a 1-slot buffer
   overflow) permanently under-counts and freezes the bar. Idempotent full-set
   feeds (bad-server, scheduler-issue) may drop-on-overflow because the next set
   corrects it; **delta feeds must never drop** (use a never-drop queue). If you
   add a new feed, decide idempotent-vs-delta first.

4. **Never re-reserve a job on a `StartTime` proxy.** A reserved-but-not-yet-
   started job whose TTR expires must be parked and only requeued after its
   runner is *confirmed dead* (host+pid recorded at `Reserve`, confirmed via the
   scheduler). Blindly requeueing on `StartTime.IsZero()` double-reserves jobs
   under load (one command runs on two runners → wasted work + `jarchive: bad
   job` / `jrelease: not running` churn). Old clients (pid 0) must park, never
   blind-requeue.

5. **Never `bkill` an LSF array element wr has handed a reservation to.** wr
   over-submits runners then kills the excess; killing a element that just
   reserved+started a job orphans it. Track reserved elements
   (`LSB_JOBID[LSB_JOBINDEX]` == `killableID`) and skip them, robust to `bjobs`
   status lag.

6. **Never cold-scan completed-job history on startup, or on a control path.**
   Seeding any in-memory structure by scanning all historical completed jobs made
   restarts take minutes on a real DB. Startup time must not scale with
   completed-job count (there is an absolute-responsiveness test guarding this).
   The same applies to any **control** path: `wr resume -i <rg> -z` used to scan
   and decode every matching RepGroup's whole archived history, so it timed out
   and took the manager's heap from 0.35GB to 12.1GB. Fetching complete jobs must
   be an explicit ask (`repGroupOptions.IncludeComplete`), and a command that can
   only act on live jobs must say so with a state filter (`JobStateIncomplete` /
   `JobStateDeletable`) rather than asking for every state. When only a *fact*
   about the history is needed, ask an index for it: "has this RepGroup ever
   completed anything?" is one `bucketRGEndTime` Get (`db.repGroupHasHistory`),
   not a scan.

7. **Cap what you hand external tools.** An uncapped `bsub -J name[1-N]` for a
   huge same-requirements batch hangs LSF for minutes with no error. Cap+chunk
   array sizes and put a timeout on the `bsub` exec; back off (don't retry an
   identical failing submit forever).

8. **Reuse the house primitives.** For retry/backoff use
   `github.com/VertebrateResequencing/wr/backoff` (+ `backoff/time`,
   `backoff/mock`), not a hand-rolled loop. Do **not** re-add
   `github.com/grafov/bcast` — it was removed; if you ever do, it must be
   `replace`-pinned to commit `e9affb593f6c` or status web updates break.

9. **Adding concurrency exposes latent bugs.** Going from one RPC reader to N
   concurrent `RecvMsg` readers surfaced a pre-existing scheduler deadlock and a
   reconnect duplicate-resync. Concurrency changes need `-race` **and** a
   real-scale run (see §4); Tier-A tests alone are insufficient.

10. **Internal-only means internal-only.** Reliability/perf fixes must not
    change user-facing behaviour. The web status counts were once allowed to
    revert to v0.36.5 flicker/overcount quality as a deliberate tradeoff; that
    flicker has since been fixed **purely client-side** (an order-independent
    occupancy reconciliation in `jobqueue/static/js/wr/websocket-handler.js`),
    with **no** server change — so the reliability constraints here (no
    server-side counter, no hot-path lock, no startup scan) are untouched while
    the displayed counts are exact again. See `.docs/flicker/` and
    `.docs/reliable2/` "speedups internal-only". Do not "fix" this on the server
    (that is what rules 2 and 6 forbid).

---

## 3. Safe testing on the farm — never disrupt production

Real-LSF validation ("Tier B") is required before merging reliability changes;
in-process tests have repeatedly passed while real LSF failed. Do it safely:

- **Never touch production.** Other managers run as `--deployment production`
  with `wrp_*` LSF jobs. Do not stop them, and **never** `bkill -J 'wrp_*'`.
  Always verify a PID's cmdline (deployment + your isolated managerdir + not a
  production PID) before `kill`.
- **Use an isolated deployment**: your own config dir, your own ports, your own
  managerdir, and job names of your own (`WR_JOBNAME_TOKEN=<token>` gives
  `wrd<token>_*` and `wrp<token>_*`) — `developers/wrdev.sh` sets this up, with
  a token unique to the host and its `$WRDEV_ROOT` (`wrdev.sh job-token dev`). Force jobs to an appropriate queue and expect fair-share to cap
  concurrency; be a good farm citizen.
- **`wr manager stop` hangs under load.** Tear down by killing the *verified*
  dev PID (`kill -9 $(cat <managerdir>/pid)` after confirming it is your dev
  binary), then bkill only your own manager's jobs, `bkill -J
  'wrd<token>_*' 0` for a token of letters and digits (stuck RUN array
  elements: `bkill -r <jobid>`); `wrdev.sh stop` does both. **Never** `bkill
  -J 'wrd_*'`: every untokened dev manager of yours, in any session, names its
  jobs that way.
- **Development ALWAYS wipes the DB on `wr manager start`** (`dontWipeDevDB` is
  test-only; there is no CLI/env switch — `WR_RELIABILITY_KEEPDB` does not
  exist). To test a **DB-preserving restart** (web-vs-CLI-after-restart,
  crash-recovery) use an **isolated production-mode** manager (own config file,
  own ports, own managerdir — never the real production one). Prod-mode runners
  are `wrp_*`, so clean them up **by the exact jobid you launched**, never a
  `wrp_*` pattern. `wrdev.sh prod-*` does this.
- **Build for LSF**: `CGO_ENABLED=1 go build -tags netgo -o <nfs-path>/wr .` —
  on NFS so exec nodes can run the runner.
- **Fast test suite**: unset ALL `OS_*` env vars
  (`unset $(compgen -v | grep '^OS_')`) or `make test`/`make race` take ~16 min
  and the OpenStack tests run. `-race` needs `CGO_ENABLED=1`.
- **Goroutine dumps** (for a stall/deadlock): the daemon's stderr goes to
  `/dev/null` and this farm blocks ptrace (no `strace`/`dlv`/`gdb` attach), so
  start the manager foreground (`manager start -f`, which also enables pprof),
  capture its output to a file, reproduce, then `kill -3 <pid>` (SIGQUIT) to
  dump all goroutine stacks to that file. Look for many `sync.RWMutex.RLock`
  waiters + one `Lock` waiter (a lock cycle), or goroutines stuck in `os/exec`
  (`bsub`/`bjobs`) while holding a lock.

---

## 4. Reproducing each class of issue

`developers/wrdev.sh` automates the common ones. What to run and what to look
for:

| Symptom | Reproduce | Look for |
|---|---|---|
| **Completion churn / double-reservation** (`jarchive: bad job`, `jrelease: not running`) | `wrdev.sh churn` — thousands of fast `true`/`false` jobs across many memory groups on LSF | near-zero bad-job/not-running; each command runs once; ~100% forward progress |
| **Control-RPC unresponsiveness** (`wr status`/`limit`/`suspend` time out) | during `wrdev.sh churn`, time control RPCs (the monitor does this) | RPCs stay in low-ms, never the 60s client timeout, while the fleet churns |
| **Web-vs-CLI count divergence** | `wrdev.sh probe` (wsprobe on `/status_ws`) vs `wr status -i <rg> -o counts` | web delta feed agrees with the CLI; no accumulating server-side counter to drift |
| **Web status flicker/freeze under a burst** | `wrdev.sh web-burst` — a large fast-completing batch + a **slow** wsprobe (a fast reader can't reproduce it) | the reconstructed web count converges to the true total; nothing dropped/frozen |
| **Web status flicker / transient overcount / connect-mid-burst divergence** | `wrdev.sh flicker-check` — a deterministic node harness driving the real `websocket-handler.js` with out-of-order + seed-race + rerun-cycle delta streams, plus the browser fixtures | reconstructed per-RepGroup and `+all+` counts stay coherent (no transient overcount, no dip) and converge exactly; the browser bar never collapses. Runs without a manager or LSF |
| **Deadlock / stall** (CLI fine, manager stops progressing) | reproduce at scale, then `wrdev.sh dump` | goroutine dump shows a lock cycle or a lock held across `bsub`/`bjobs` |
| **Crash-recovery** (a genuine success survives a restart) | `wrdev.sh crash-recovery` (prod-mode preserves the DB) | after restart within `retryTime`, the re-sent archive is accepted, `complete`, and the command ran **once** (marker file) |
| **Anything that only shows when every client runs at once, for hours** | `wrdev.sh prodsim` — the production-shaped soak (see `developers/README.md`) | `report.txt`: call latencies that grow from the first quarter to the last, errors, and manager RSS/heap/goroutines/fds that keep climbing |
| **Archived jobs kept in the heap** | `wrdev.sh retention-check` | forced-GC `HeapInuse` stays flat over rounds with nothing live |
| **A backup copy stalling the DB once it grows past its mapping** | `wrdev.sh remap-stall-check` | a trivial read never waits for the copy |
| **Slow startup** | measure `Serve()` time with N completed-only jobs (see `jobqueue/reliable2_startup_test.go`) | startup does not scale with history size |

For churn/responsiveness the authoritative evidence is a **real-LSF** run; the
`//go:build reliability` in-process scale test under-reproduces (it uses
`os.Getpid()` live processes) — never rely on it alone.

---

## 5. Performance gate — `make speed`

`make speed` (`developers/speed.sh`) checks that the hot paths have not got
slower. It is **not** part of `make test`, `make race` or CI: it needs a quiet
host and about 30 minutes, and its numbers depend on that host.

**When to run it.** Run it before you open a PR, and again before it merges if
the hot-path code changed since, when the change touches any of:

- add (`createJobs`), reserve, touch, start, release/bury or archive
- recovery or the scheduler/rac
- `queue/`
- the write paths in `jobqueue/db.go`
- status queries

Summarise its verdict in the PR body: PASS or FAIL, the baseline it compared
against, and every row benchstat marks as changed. The PR body must explain
any regression the change accepts.

**What it measures.** For the tree under test and for a baseline, it runs:

- the hot-path Go benchmarks, 12 rounds each (two in each of 6 code
  layouts, below), interleaved so that drift in host load hits both trees
  alike:
  - `jobqueue`: `BenchmarkAddJobs`, `BenchmarkUpdateJobState`,
    `BenchmarkArchiveJobs`, `BenchmarkArchiveSpacedArrivals`,
    `BenchmarkReadyBacklogSnapshot`, `BenchmarkBuildSchedulerGroupsBacklog`,
    `BenchmarkRACCycleCounted`,
    `BenchmarkRepGroupStatusDetails`, `BenchmarkJobKey`,
    `BenchmarkModifyLiveJobsReverseLookup`, `BenchmarkJobCleanup*` and
    `BenchmarkAddDepGroupMember` (a tree without a benchmark, such as an
    older release, just reports nothing for it)
  - `queue`: `BenchmarkQueueLifecycle`
  - `limiter`: its benchmarks
- the local, farm-safe `wrdev.sh` scenarios `report-storm` and
  `dep-granularity-check`, recorded as benchmark lines.

benchstat compares the two trees. The gate fails when any time, allocation,
bolt write/transaction count or scenario figure gets more than 10% worse at
p < 0.05. It also fails when a scenario misses its own thresholds on the tree
under test. With about 30 comparisons per run, a lone benchmark can cross the
line by chance: confirm a failure with a re-run of just that one
(`SPEED_BENCH='^BenchmarkName$' SPEED_COUNT=18 make speed`) before acting on it.
The baseline defaults to the merge-base with `origin/develop`,
built in a temporary worktree.

**Code layouts.** A benchmark's speed can depend on where the linker places
the test binary's code. `BenchmarkReadyBacklogSnapshot` read 7 to 11% slower
after a commit that changed only test code: its product functions compiled to
the same instructions, and adding one to four empty Test functions to the
package lowered it from +8.67% against the baseline to between -1.4% and
+6.86%. Each binary's speed is stable from round to round, so more rounds of
one binary cannot average the placement out (bugfix 261119 item I). So
`speed.sh` builds every benchmarked package in `SPEED_LAYOUTS` layouts
(default 6): layout 0 is the tree as it is, and layout k adds k empty Test
functions in a generated `aaa_speed_layout_test.go`, which sorts before the
package's own test files and so moves all of their code, and the closures and
other code laid out after it. The product's top-level functions compile
before the test files and do not move, so noise from where product code lands
is not averaged out. `developers/speedlayout.sh` writes that file and a
`go build -overlay` file under the run directory, so neither tree is written
to. Round i runs both trees in layout (i-1) mod `SPEED_LAYOUTS`, and
benchstat compares the pooled rounds, so each tree's figure averages over the
same placements. Keep `SPEED_COUNT` a multiple of `SPEED_LAYOUTS`. A layout
costs one more build per package and tree (about 13s for jobqueue).

The layouts widen each tree's spread, so the default rounds rose from 6 to
12. Measured on `BenchmarkReadyBacklogSnapshot` (d1a4b6d7 against
8d82da66, 36 rounds of each, 6 per layout), each tree's six layouts spanned
about 15% (base -5.8% to +9.5%, head -6.7% to +10.1% of base's median), so
one layout alone compared the trees at anything from -12.5% to +10.2%,
while the pooled rounds read -1.3% (p=0.810). Resampling those rounds into
gates, with head's rounds scaled to model a slowdown: with one fixed layout,
as before, an unchanged head failed in 0 to 25% of draws depending on the
layout (none in four of the six), and a head scaled 15% slower failed in
72% (the layout drawn at random); with 6 layouts, an unchanged head never
failed, and a head scaled 15% slower failed in 77% at 6 rounds and 87% at
12. On the host itself, a head given an injected slowdown (a loop in
`memoisedSchedulerGroupSnapshot`) measured about 10 to 16% slower than its
own tree, varying by run (+15.64% and +9.59%). Against d1a4b6d7 it passed
all three 6-round runs and failed 5 of 6 12-round runs (+13.06%, +10.94%,
+10.09%, +12.94% and +12.40%; the pass read +7.87%). Expect a miss when a
run measures the slowdown under or near the 10% threshold.

Knobs:

- `SPEED_BASE=<ref>` picks another baseline, and `SPEED_BASE=none` skips the
  comparison.
- `SPEED_THRESHOLD` and `SPEED_COUNT` change the threshold and the number of
  rounds, and `SPEED_LAYOUTS` the number of code layouts (above).
- `SPEED_DIR` moves the work directory (default
  `${TMPDIR:-/tmp}/wr-speed-$USER`).

The header of `developers/speed.sh` lists the rest. Every run keeps its raw
results, `benchstat.txt` and the verdict's `judgement.txt` under
`$SPEED_DIR/run-<epoch>/`. `developers/speedjudge` reads benchstat's two
CSVs (below) and makes the verdict; its tests (part of `make test`) replay
recorded runs, 1g.4's among them, and the synthetic cases
`testdata/synthetic.py` writes.

**Benchmarks under reserve load.** jobqueue's `BenchmarkKickUnderReserveLoad`,
`BenchmarkResumeUnderReserveLoad`, `BenchmarkSuspendUnderReserveLoad` and
`BenchmarkAddManyUnderReserveLoad` time a background operation while 8
goroutines reserve from the same queue. They are not in the default set: add
them with `SPEED_BENCH` (12 rounds of both trees take about 3 hours). A raw
tail percentile (p99, max) of the reserves taken during the operation
misjudges them: a faster operation has a shorter window, which holds fewer
reserves, so the same few slow ones make a larger share and the percentile
rises with no reserve worse off. So when both trees report the window figures
below, the gate judges such a benchmark by them instead. It regresses only if
one of them is more than 10% worse at p < 0.05 (the same threshold and alpha
as every other figure):

- (a) `sec/op`: how long the operation took.
- (b) `slow-reserves/op`: how many reserves took over 1ms, per operation. 1ms
  is Go's mutex starvation threshold, the point past which a waiter is being
  kept from the lock, not queuing behind the other reservers. In 1g.4, an add
  twice as fast as base's still delayed as many reserves past it per add.
- (c) `max-qlock-us`: the longest wait of a paced reader of the queue's lock
  during the operation, averaged per operation. A reader waits only for the
  hold under way, so this approximates the longest hold the reader saw: it
  can fall short by up to the reader's period, or miss a short hold, and it
  adds the reader's own scheduling delay. It is close for an add's hold of
  about 15ms; for the per-item holds of kick, resume and suspend (a few
  microseconds) it mostly measures that delay.
- (d) how long the delayed reserves wait, by 19 figures per operation:
  - `slow-reserve-wait-us/op`: the total latency of the reserves counted in
    (b). At an equal count it rises when they wait longer. It is count times
    mean, so when (b) falls to 1/k of base's, each remaining reserve can wait
    up to k times as long before the total rises.
  - `reserves-over-<T>ms/op`, for T of 1.25, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10,
    12, 16, 20, 24, 32, 40, 48 and 64: how many reserves took over T ms.
    Removing a reserve cannot raise these and lengthening one can only raise
    them, so they fail a head whose few remaining slow reserves wait past a
    threshold more often than base's, which the total can miss. A rise from
    0 fails like any other, at p < 0.05.

  Each count compares head's reserves over 1.1T with base's over T. The
  harness also reports `reserves-over-<T>ms-tol/op`, the count over 1.1T;
  `speed.sh` gives head's those plain names in `head-tol.txt` and runs
  benchstat on it against `base.txt` a second time (`benchstat-tol.csv`),
  and the judge takes the counts from that run. Without the tolerance, a
  count amplifies a small change in waits clustered just under its
  threshold: an add's hold of 15 to 17ms delays reserves just under and
  just over 16ms, and a hold 5% longer nearly doubles the count over 16ms
  (+76% in the judge's `addmany-hold-5pc-longer` case) while (c) and the
  total rise 5%. With it, a head whose every wait is at most 10% longer than
  base's has no more reserves over 1.1T than base has over T, so a slowdown
  within the gate's 10% cannot fail a count (`addmany-hold-5pc-longer` and
  `addmany-hold-9pc-longer` pass; `addmany-hold-15pc-longer` fails).

  What (d) still lets through:
  - A wait that lengthens without passing 1.1 times the next threshold up,
    while its count per operation stays within 10% of base's count over the
    threshold below and the total falls. The thresholds are 1.2 to 1.33
    times apart, so base's waits just over one threshold can become waits
    just under 1.1 times the next: from just over 1ms ((b)'s threshold) to
    just under 1.375ms, 1.375 times as long; from just over 1.25, 2.5, 5,
    10, 20 or 40ms, 1.32 times; from just over 2, 4, 8, 16 or 32ms, 1.375
    times; from just over 1.5, 3, 6, 12, 24 or 48ms, 1.47 times (such as a
    few 4.3ms waits per operation where base's were just over 3ms). Past
    64ms the counts set no bound and only the total sees a longer wait.
    The judge's tests include the review's adversarial heads, and their
    verdicts are in the bugfix 261119 item D record.
  - Sparse long stalls. A stall that comes once in several operations gives
    a count such as 0.1 per operation in a few rounds and 0 in the rest, and
    6 rounds rarely make that significant (0 to 0.1 per operation gave
    p = 0.470 in the judge's 6-round test fixtures), so a new rare long
    stall can pass. `make speed` now defaults to 12 rounds, which gives the
    counts more power, but such a stall can still pass.
  - Stalls that arrive at a constant rate, such as background work. Every
    figure is per operation, so a head whose operation is twice as fast
    spans half as many of them; a head that halves the operation and doubles
    their rate looks unchanged. A fixed-length observation window per
    operation would close this.
  - A reserve still waiting when the operation ends. The harness drops
    reserves that return after it closes the window, so a reserve stalled
    across the operation's end is not counted at all.

  The slow reserves' median (`slow-reserve-p50-us`) was (d) until
  2026-11-23, and is now informational: a kick or resume that stopped
  holding the queue's lock per item removed nearly all of base's 1.1ms lock
  waits, so the few slow reserves left were the rarer, longer stalls base
  also had, and their median rose up to 48% while every (d) figure fell or
  stayed at 0 (bugfix 261119 item D).
- (e) `reserve-p50-us`: the median latency of every reserve taken during the
  operation. A median over thousands of reserves per operation, which a
  shorter window does not skew the way it skews the tail. It catches a
  slowdown of every reserve by less than 1ms, which (b) and (d) cannot see
  and (c) cannot tell from the probe's noise for per-item operations.

On an idle host, kick, resume and suspend delay almost no reserve past 1ms, so
(b) and (d) are mostly 0 on both trees, and (c) reads tens to hundreds of
microseconds. Load raises all three on both trees alike: on a loaded host,
1g.4's review measured 17 to 43 slow reserves per operation, of 1.2 to 3.5ms
at the median, and a `max-qlock-us` of 0.5 to 13ms.

A criterion whose figure either tree lacks fails, so a base given a harness
from before (d)'s figures fails (d) until it gets this tree's harness
(below). `reserve-p99-us`, `reserve-max-us`, `reserves/op` and
`slow-reserve-p50-us` are reported as informational; `B/op` and `allocs/op`
are judged as usual. The verdict prints a `WINDOW` block for each such
benchmark, naming any criterion that failed.

A base from before these figures reports none of them, so its under-load
benchmarks are judged by their raw figures, reserve percentiles included, and
the verdict notes it. To judge them by the window-aware rule, give that base
this tree's harness and pass it as a prepared tree. The harness's
`serveOnFreshPorts` also lets the base's under-load benchmarks and
`BenchmarkAddDepGroupMember` start their servers on fresh ports when another
process takes a manager port. `settleStore` is in
`jobqueue/store_settle_test.go`, which the harness does not include: a base
with a log store but without that file (its `settleStore` was in the
`dep_group_add_bench_test.go` the copy replaces) gets that file too, and a
base with no log store gets the stub, which does what `settleStore` does on
bbolt, nothing:

```bash
git archive <base-sha> | tar -x -C <dir>
cp jobqueue/kick_resume_bench_test.go <dir>/jobqueue/
cp jobqueue/dep_group_add_bench_test.go <dir>/jobqueue/
printf 'package jobqueue\n\nimport "testing"\n\nfunc settleStore(testing.TB, *Server) {}\n' \
  >"<dir>/jobqueue/settle_stub_test.go"
cp jobqueue/bigdb_copy_sync_test.go <dir>/jobqueue/
sed -i 's/wsfCopyFile(dbFile, work)/copyBigDBSynced(dbFile, work)/' \
  <dir>/jobqueue/reliable4_*_test.go
SPEED_BASE=<dir> make speed-full
```

The last two commands give the base the big-DB scenarios' synced copy
(below). `speed.sh` applies them itself to a tree it builds from a ref, but
not to a prepared directory.

Two outcomes are stricter than the awk rule `speed.sh` used before
`speedjudge`. A figure that rises from 0 on base fails (at p < `SPEED_ALPHA`)
in any unit where higher is worse, where the awk rule skipped it because
benchstat writes `?` for its change. And a `SPEED_ALPHA` above 0.05 now flags
a row benchstat marks `~`, as `speedjudge` reads the p-value rather than
benchstat's verdict at its own alpha of 0.05.

`make speed-full` adds the in-process big-DB scenarios: `add-storm`,
`archive-rate` and `archive-ceiling`. They need copies of production-shaped
databases (`SPEED_BIG_DB`, or `WR_AS_DB`/`WR_ARCHRATE_DB`/`WR_AC_DB` for each
one) and take over an hour with a baseline. Run it as well for a change to
the bolt write path or to commit coalescing.

Each of these scenarios copies its database to scratch and, through
`copyBigDBSynced` (`jobqueue/bigdb_copy_sync_test.go`), writes the copy and
the rest of that filesystem's dirty data to disk before its window opens.
Before it did, writeback of the unsynced 4GB copy, and of the test binary
`go test` had just linked into the same `/tmp`, stalled one bolt commit for
tens to hundreds of ms some seconds into the window. `archive-rate`'s max-ms
then read higher on the tree with the larger test binary, with the archive
path unchanged; with both synced, it read 9 to 35ms on both trees in windows
without writeback from other processes on the host, and up to 161ms in
windows with it (bugfix 261119 item M). A base without that file measures
the writeback again, so it needs the file and the call (the recipe above).

**What `make test` already guards.** Structural cost tests fail `make test`
outright, whatever the host's speed, for example:

- `TestDepGranularityAddTransactionCost`: an add's bolt transactions do not
  grow with dep-group size.
- `TestReliable4DependencyFreeTxCost`
- `TestManagerDBCommitFreelistCost`
- `TestDBStartProbeCommitCost`

A change that alters a hot path's transaction or decode count should come with
a test like these. `make speed` is for the costs that can only be timed.

**Bigger changes.** A change to scheduling, the RPC server's concurrency or the
LSF scheduler also needs the real-LSF scenarios (§3, §4): `add-storm-lsf`,
`report-storm-lsf`, `limit-drain`, and the `prodsim` soak at production
concurrency. [`developers/soak/README.md`](developers/soak/README.md) says how
to run that soak with crashes and commit stalls injected, and how to check the
result for double runs.

---

## 6. When you need the full history

This doc is the distilled version. The primary sources, if you need the deep
context of a specific past problem:

- `.docs/reliable/`, `.docs/reliable2/`, `.docs/reliable2/phase2/` — the
  investigations, specs, and design decisions (notes N1–N6).
- `.docs/flicker/` — the web status-bar flicker / transient-overcount
  investigation (`issue.md`) and the client-side fix (`solution.md`), reproduced
  and guarded by `jobqueue/testdata/status-count-reconcile/` (see
  `wrdev.sh flicker-check`).
- `.docs/reliable2/phase2/validation.md` — the recorded real-LSF Tier-B results.
- `.docs/bugfixes/*.md` — dated, verbatim bug write-ups with root cause + fix
  (e.g. `260722-1` uncapped bsub array, `260723-1` web status delta drop). Scan
  these before changing an area; their checked items are behaviour that must not
  regress.
- `.docs/reliable2/phase2/wsprobe/` — the `/status_ws` probe (has a `--slow`
  mode for the burst repro).
