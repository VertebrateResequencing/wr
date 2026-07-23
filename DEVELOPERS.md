# DEVELOPERS.md — working safely on wr's reliability-critical paths

Distilled from the `.docs/reliable`, `.docs/reliable2`, and
`.docs/reliable2/phase2` investigations so you do **not** have to re-read that
whole history to know what to (not) do. Read this before changing job
scheduling, the RPC server, the status web feed, or the LSF scheduler.

> This file and the `developers/` directory are **developer tooling and
> guidance, not part of the shipped binary or the test suite**. `developers/`
> contains only shell scripts, so `go build ./...`, `make test`, and
> `make lint` never touch it.

Helper: `developers/wrdev.sh` (run `developers/wrdev.sh help`). It encodes the
safe-testing rules below so you don't have to remember them.

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

6. **Never cold-scan completed-job history on startup.** Seeding any in-memory
   structure by scanning all historical completed jobs made restarts take
   minutes on a real DB. Startup time must not scale with completed-job count
   (there is an absolute-responsiveness test guarding this).

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
    change user-facing behaviour (the one deliberate exception on record is the
    web status counts reverting to v0.36.5 flicker/overcount quality). See
    `.docs/reliable2/` "speedups internal-only".

---

## 3. Safe testing on the farm — never disrupt production

Real-LSF validation ("Tier B") is required before merging reliability changes;
in-process tests have repeatedly passed while real LSF failed. Do it safely:

- **Never touch production.** Other managers run as `--deployment production`
  with `wrp_*` LSF jobs. Do not stop them, and **never** `bkill -J 'wrp_*'`.
  Always verify a PID's cmdline (deployment + your isolated managerdir + not a
  production PID) before `kill`.
- **Use an isolated deployment**: your own config dir, your own ports, your own
  managerdir, `wrd_*` job names (development) — `developers/wrdev.sh` sets this
  up. Force jobs to an appropriate queue and expect fair-share to cap
  concurrency; be a good farm citizen.
- **`wr manager stop` hangs under load.** Tear down by killing the *verified*
  dev PID (`kill -9 $(cat <managerdir>/pid)` after confirming it is your dev
  binary), then `bkill -J 'wrd_*' 0` (stuck RUN array elements:
  `bkill -r <jobid>`).
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
| **Deadlock / stall** (CLI fine, manager stops progressing) | reproduce at scale, then `wrdev.sh dump` | goroutine dump shows a lock cycle or a lock held across `bsub`/`bjobs` |
| **Crash-recovery** (a genuine success survives a restart) | `wrdev.sh crash-recovery` (prod-mode preserves the DB) | after restart within `retryTime`, the re-sent archive is accepted, `complete`, and the command ran **once** (marker file) |
| **Slow startup** | measure `Serve()` time with N completed-only jobs (see `jobqueue/reliable2_startup_test.go`) | startup does not scale with history size |

For churn/responsiveness the authoritative evidence is a **real-LSF** run; the
`//go:build reliability` in-process scale test under-reproduces (it uses
`os.Getpid()` live processes) — never rely on it alone.

---

## 5. When you need the full history

This doc is the distilled version. The primary sources, if you need the deep
context of a specific past problem:

- `.docs/reliable/`, `.docs/reliable2/`, `.docs/reliable2/phase2/` — the
  investigations, specs, and design decisions (notes N1–N6).
- `.docs/reliable2/phase2/validation.md` — the recorded real-LSF Tier-B results.
- `.docs/bugfixes/*.md` — dated, verbatim bug write-ups with root cause + fix
  (e.g. `260722-1` uncapped bsub array, `260723-1` web status delta drop). Scan
  these before changing an area; their checked items are behaviour that must not
  regress.
- `.docs/reliable2/phase2/wsprobe/` — the `/status_ws` probe (has a `--slow`
  mode for the burst repro).
