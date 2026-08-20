# reliable4 — what is done, and what to do next (written 2026-08-19)

## READ THIS FIRST — you are a fresh agent picking up reliable4

This file is self-contained: it is the handoff after the 2026-08-17 live-prod
profiling round was **implemented**. You do not need to re-read the whole
`.docs/reliable4/` history to start work.

- **What was profiled and why:** `.docs/reliable4/prod-run-20260817.md` (the
  findings, the measured mechanisms, and the reusable pprof tooling). Its STATUS
  block now points here.
- **What was implemented, in detail:** `.docs/bugfixes/260818-1.md` — per-fix
  notes, the RED/GREEN numbers, every constraint honoured, and every follow-up.
  Read the follow-up bullets before starting any item below; several of the items
  here are those follow-ups with more detail added.
- **Binding project rules:** repo-root `DEVELOPERS.md`. Rules 2 (no new
  server-wide exclusive lock on the transition path), 6 (no history scan on
  startup *or on a control path*), 7 (cap what you hand external tools, time it
  out, back off) and 8 (use the house `backoff` package) all bear on the work
  below. Also read `developers/wrdev.sh` before touching scheduling / RPC /
  status-feed / LSF code.

Branch is `reliable4`. Nothing in this batch has been pushed.

---

## STATUS (updated 2026-08-20): items A, B, C done; the VALIDATION GATE PASSED

**Superseding update, 2026-08-20.** Everything below this block was written on
2026-08-19 and is kept for its reasoning and its hard-won process notes, but the
plan itself has moved on:

- **Item A** — `785a151`. Pre-start releases now spend a retry, so they cannot loop
  forever. The client reports whether it *attempted* the command (additive wire
  field, back-compatible both ways); one server predicate, evaluated once and
  carried on the report, drives both the bury decision and the decrement.
  Manager-initiated releases keep the StartTime-only rule, so a healthy-but-slow
  starting job still cannot burn its retries. Neither of the doc's two candidate
  fixes was used as written — candidate 1 was **measured wrong** (`cmd/runner.go`
  releases jobs it never tried to run). See `.docs/bugfixes/260819-1.md`.
- **Item B** — `5b90c53`. Archived decoding is bounded by the query's limit:
  5,000 -> 1 decode for the default query, and the web failed-jobs drill-down
  207 -> 2. **The trigger described below is wrong**: `-l` is `--cmdline`, not
  `--limit`; `--limit` has no shorthand and defaults to 1, so plain
  `wr status -i <substr> -z` was the heap bomb and no flag was needed.
- **Item C** — `53c323f`. Slow/heavy requests now warn (10 s threshold, offset by
  any wait the client explicitly asked for); `Cmd` is bounded in runner **and**
  manager log lines. Runner log 82,427 -> 1,415 bytes/job in the gate, 571 in the
  farm run; manager log 42,722 -> 2,250.
- **Item D** — **reproduced, and the hypothesis below is wrong.** No delta is
  dropped and none is missing: the client joins the live delta feed before its
  `"current"` snapshot is served, so any transition in that window is counted
  **twice**. The `limit -> 0` mass exit is not the cause — it is what made a
  pre-existing offset glaring. Fix in flight per `.docs/bugfixes/260820-2.md`.
- **Item E** — **answered by measurement: do not do it.** Queue-mutex block delay
  fell **61x** to 0.89% of the total and the ranking flipped. Two new hotspots
  displaced it: the **DB backup chain at 27.68% of peak CPU** and
  `lsf.snapshotReserved` at **21.35% of mutex hold**.
- **The validation gate (Batch 4) RAN and PASSED all six targets**, at a harsher
  regime than the profiling run it validates (2000/2000 sustained runners vs ~686,
  ~365 archives/s vs prod's 12/s). Finding 6 is confirmed fixed. Full report:
  **`.docs/reliable4/validation-gate-260820.md`** — read that before the next
  profiling round, especially its three calibration traps (notably: `pristine10`
  is a **false PASS** for anything history-related).

---

## ORIGINAL STATUS (2026-08-19): findings 1–5 are FIXED; the farm-scale validation gate is DEFERRED

Seven commits on `reliable4` (2026-08-18/19), each fix → review → commit, all with
`make lint` clean and `make test`/`make race` at **393 passed, 0 data races**:

| Commit | What | Measured effect |
|---|---|---|
| `8087866` | Finding 3 — memoise `Job` scheduler-group derivation | idle 50k limit-0 backlog **4,090 → 800 ms** CPU/25 s; steady rac pre-pass **340,013 → 0 allocs**, 125.9 → 4.4 ms/op |
| `f7e36bc` | Finding 2 — coalescing archive writer (one `db.Update` per drain, reply per waiter) | 660 archivers on a 9.99 GiB DB: mean **40,494 → 764 ms**, p99 54,874 → 2,752 ms, **17 → 145/s**, queue 603 → 110 deep; 100 archives cost 11 txns not 100 |
| `cbfac88` | scale-gate hygiene | 3 wrdev modes were dropping 10 GB DB copies beside the shared `pristine10` fixture with broken/absent cleanup |
| `5c75a15` | Finding 1 — control paths no longer scan archived history | `resume -i <substr> -z` **10,578 → 235 ms**, peak RSS **3,137 → 51 MB**, `decodeArchivedJob` 5–10k → **0** |
| `913976f` | made Bug 1's own regression guard deterministic under host load | defeating the memo now fails with 60,000; 20 consecutive green runs at load 86–93 |
| `0d22eda` | Finding 5 — exec-impossible commands buried on attempt 1 | unbounded retry loop → **1 reservation, 1 exec attempt**, real cause in `FailReason` |
| `11f1537` | Finding 4 — `bkill` bounded, timed out, backed off, summarised | argv 1,900 → **1,000**, log **104,353 → 363 bytes**/cycle, identical re-kills 1,900 → **0**, hung bkill 120 s → 62 s |

Two corrections this work made to the profiling doc's own conclusions, both
already folded into `.docs/bugfixes/260818-1.md`:

1. **Finding 5's retries were unbounded, not "~31".** `UntilBuried` only
   decrements when `!job.StartTime.IsZero()`, and server-side `StartTime` is set
   only by the post-exec `jstart` report — so a job that never started never
   decrements, and even `--retries 0` cannot stop it. The doc's "31" came from a
   different job class (a dedupe job with `FailReason "command exited non-zero"`).
   **This is now item A below** — Finding 5's fix only rescued the three permanent
   errnos it buries.
2. **`Attempts` for a pre-exec bury is 0, not 1.** `Attempts++` happens only in
   `applyJobStart` (`jobqueue/serverCLI.go:908`), which needs a real pid+host, so
   1 was unreachable without faking a start report; every existing pre-exec bury
   also leaves 0.

**Finding 6** (control RPCs queueing behind the archive backlog) was expected to
fall out of Finding 2 and has **not** been separately re-measured — confirm it in
the validation gate rather than assuming it.

**Finding 7** (web status counts diverged, 274 shown vs 4 actual) is
**un-diagnosed and deliberately untouched**. It is item D below.

### The farm-scale validation gate is deliberately deferred

The operator's decision (2026-08-19) is to **defer the farm-scale validation run
until more fixes land**, so that one farm run validates everything at once. Do not
treat that as "validation is optional": it is the only thing that confirms these
fixes on real LSF at real scale, and **it must happen before any live-prod
profiling round.** Its spec is the "Validation gate for the whole batch" section
at the end of `.docs/reliable4/prod-run-20260817.md`; the targets are restated in
Batch 4 below.

### Why the next step is NOT another production profile

Recorded so nobody re-opens this: a prod profile now would be both risky and hard
to read. Prod logged **nothing** for a 12-minute, 12 GB request, and runner-log
lines reached 1.3 MB — item C fixes exactly that, cheaply, and makes the next
session's evidence far better (the same reasoning that made `--runner_filelog`
worth adding for the 2026-08-17 run, which is the only reason Finding 5 was
visible at all). Items A and B need no new data at all. And the profiling doc's
own Finding 2 sequences the queue-mutex work (item E) as "worth a look **after
Findings 2 and 3 land**" — which is now.

---

## PROPOSED ORDER AND BATCHING

Five items, A–E, then the validation gate. Batches 1–3 are `/bugfix` runs; each
batch is independent, so they can be done in order without re-planning. Batch
boundaries are chosen so each one is a coherent review unit.

| Batch | Items | Route | Why grouped |
|---|---|---|---|
| **1** | **A**, **B** | `/bugfix` | The two understood correctness defects. Both are "the fix that landed covered the common case, not the general one". No new investigation needed. **Do this first** — A is a live reliability hazard and B is an available heap bomb. |
| **2** | **C** | `/bugfix` | Pure diagnostics/log-volume. Small, and it is what makes the eventual prod profile legible, so it should land before the validation gate. |
| **3** | **D** | investigate first, then `/bugfix` **only if it reproduces** | The last un-diagnosed finding. The doc is explicit: no fix until it is reproduced. Kept separate because it may produce nothing. |
| **4** | validation gate | operator-run, farm scale | Validates batches 1–3 plus the seven commits already landed. Confirm Finding 6 here. |
| **5** | **E** | measure first, then decide | The doc's own next step, but it is an optimisation, not a correctness bug. Cheapest to judge *after* the validation gate has shown the current archive/CPU behaviour at scale. |

If you have to cut scope: **A is the one that must not be dropped.**

---

## ITEM A — a release without a start never decrements `UntilBuried`, so any
## pre-start release retries forever, ignoring `--retries`

**Route:** `/bugfix`, batch 1. **This is the highest-priority item in this file.**

### Mechanism (already proven; no investigation needed)
- `releaseJobSnapshot` (`jobqueue/server.go:4956`, called from `:4928`):
  `if !bury && !job.StartTime.IsZero() { bury = job.UntilBuried == 1 }`
- `finalizeReleasedJob` (`jobqueue/server.go:5010`, the decrement at `:5017`):
  `} else if !job.StartTime.IsZero() { job.UntilBuried-- }`
- Server-side `StartTime` is set **only** in `applyJobStart`
  (`jobqueue/serverCLI.go:908`), which returns false and changes nothing when
  `crJob.Pid <= 0 || crJob.Host == ""`. `resetJobForReservation` re-zeroes
  `StartTime` on every reservation.
- `UntilBuried` starts at `Retries + 1`, so with `--retries 0` it is 1 and
  `1 <= 0` is never true without a decrement.

**Therefore:** any job released *before* it reported a start retries forever, with
no ceiling, burning a runner slot per iteration, invisible except in runner logs.
Measured in this batch: `untilBuried=3` still after 4 releases; a pre-fix scale run
sat at `delayed: 20, buried: 0` for 90 s while exec attempts climbed 20 → 47 across
22 runner logs.

**Finding 5's fix (`0d22eda`) closed this only for E2BIG/ENOENT/EACCES**, which it
buries. The transient start-failure path it deliberately preserves still loops
unboundedly — the `ETXTBSY` negative control in
`jobqueue/scheduler/../reliable4_execfail_test.go` demonstrates it, ending at
`reserves=4, state=delayed, untilBuried=3`. Real-world triggers: a permanently
`noexec` mount, a node with a broken loader, a shell path that stays busy, or any
other transient-looking-but-persistent pre-exec failure.

### Two candidate fixes (pick with evidence, do not guess)
1. **Decrement on any owner-reported release regardless of `StartTime`.** Simple,
   but check what `StartTime.IsZero()` is standing in for elsewhere before
   removing the gate — note the comment at `jobqueue/server.go:4073` about using
   "a reserved-not-started job on a `StartTime.IsZero()` proxy", and `:2558`,
   `:3844`, `:5290` which read the same field for other purposes. This gate may be
   deliberately protecting the false-lost/TTR machinery from counting a retry
   against a job that is legitimately still starting.
2. **A distinct pre-start attempt counter**, so pre-start and post-start failures
   have separate ceilings. Safer for the `StartTime` semantics, more surface area.

**Constraint that decides it:** the reason the gate exists is almost certainly to
avoid burying a job that is merely slow to report its start under load — i.e. the
false-lost problem this whole reliable* effort has been fighting. Whatever you do
must **not** make a healthy-but-slow-starting job burn its retries. Prove that
with a test, not an argument. If neither option can satisfy both properties in a
contained change, stop and write a `/spec-writer` prompt doc instead.

### TDD
Fast, deterministic, main suite. A job whose start fails transiently (reuse the
`ETXTBSY` technique already in `jobqueue/reliable4_execfail_test.go`: hold a write
handle open on the shell script, which makes `cmd.Start()` fail with a genuinely
transient errno) with `--retries 2`: assert it is buried after exactly
`Retries + 1` attempts, **not** retried forever. RED today (it reaches the test's
loop cap with `untilBuried` unchanged). Add the counterpart test that a job which
*does* report a start still gets its full retry budget, and one that a slow start
report is not penalised.

### Scale gate
Extend `developers/wrdev.sh exec-impossible-retries` (added by `0d22eda`) or add a
sibling mode: N jobs whose start fails transiently, assert
total exec attempts converge to `N * (Retries+1)` rather than growing without
bound over the window. The existing mode already measures exec attempts from
runner logs and buried counts from `wr status -o counts`, so it is a small delta.

---

## ITEM B — `wr status -i <substr> -z -l 1` still decodes the entire history first

**Route:** `/bugfix`, batch 1.

`5c75a15` stopped the *control* paths (suspend/resume) from touching archived
history, and made complete-job fetches explicit via `repGroupOptions.IncludeComplete`.
But for a caller that legitimately wants history — `wr status` — the limit is still
applied in `limitJobs` (`jobqueue/server.go:5696`) **after** the full decode:
`getDBJobsByRepGroup` (`jobqueue/server.go:5506`) →
`retrieveCompleteJobsByRepGroup` (`jobqueue/db.go:2315`) → `decodeArchivedJob` per
record, for every matching repgroup.

So the 12.1 GB heap excursion is still reachable today with a *status* command, and
the operator has no way to know that `-l 1` does not bound the work. Prod's
history is ~2.15M complete jobs.

**Fix:** push `Limit`/`Offset` down into `retrieveCompleteJobsByRepGroup` and its
RTK cursor so it stops decoding once it has enough. Watch the interaction with
`getRepGroupsList`'s multi-repgroup loop (a limit spans repgroups, so the pushdown
has to be applied across the loop, not per group) and with `limitJobs`' existing
filtering/grouping semantics — `filterAndGroupJobs` may legitimately discard
records after decode, so a naive "stop at N decoded" changes results. **Do not
change what `wr status` returns**; this is a "stop materialising what you were
always going to discard" fix. If the filter semantics make a correct pushdown
impossible without changing output, say so and propose the alternative.

### TDD
Seed a repgroup with N (e.g. 5,000) complete jobs; issue the `wr status -i <rg> -z
-l 1` request shape; assert `decodeArchivedJob` calls are **O(limit), not O(N)**
(the inert `db.archivedDecodes` counter added by `5c75a15` is already there for
this) and that the returned jobs are identical to today's for the same query.
Cover `-l`/`--limit` with and without `--offset`, and the multi-repgroup `-z` case.

### Scale gate
Extend `developers/wrdev.sh control-rpc-history` (added by `5c75a15`, and it already
seeds 200,000 archived jobs over 20 repgroups and measures VmHWM growth): add a
timed `wr status -i <substr> -z -l 1` and assert it returns fast with no heap
excursion. Note that mode's in-band reference scan deliberately *does* decode a
whole group, so keep the two measurements distinct.

---

## ITEM C — the manager is silent about slow/huge requests, and runner logs carry
## full 130 KB command lines

**Route:** `/bugfix`, batch 2. Two independent, small parts.

### C1 — warn on a slow or heavy request
Prod ran a **12-minute, 12 GB** request and logged **nothing**; the only reason it
was ever identified is that a profiling session happened to be attached. Add a
warning when a request exceeds a few seconds or allocates heavily. The dispatch
point is `handleRequest` (`jobqueue/serverCLI.go:435`) — it wraps every client RPC,
so one place covers all of them. Log the request method, the repgroup/selector if
present, the duration, and ideally an allocation delta (`runtime.ReadMemStats`
around the call is too heavy per-request; consider only sampling the alloc figure
when the duration threshold is already crossed).

Keep it cheap: this is on the hot path for every RPC, so the fast path must be a
single time comparison, not a profile. Make the threshold a package var so a test
can drive it. **This is the item that most improves the next prod profile.**

### C2 — stop logging the full `Cmd`
`cmd/runner.go:246` (`clog.Info(..., "reserved a job", ..., "cmd", job.Cmd)`) and
`cmd/runner.go:304` (`info("will start executing [%s]", job.Cmd)`) each log the
entire command line — ~130 KB × 2 per job for the pathological cases, and the
profiling doc measured a `reserved a job` p99 of 24,261 bytes and a max of
**1,345,498 bytes**. `0d22eda` cut the *multiplier* (an unrunnable job is now
attempted once instead of forever) but not the per-line size.

Reuse the helper `0d22eda` already added — `abbreviateCmdLine` /
`startFailureCmdMax` in `jobqueue/client.go` — rather than writing a second
truncator; if it needs exporting or relocating, do that. Keep the job key in the
log line so an operator can still get the full command from `wr status`.

### TDD
C1: a fake slow handler with the threshold driven down; assert exactly one warning
with the duration and method, and assert **no** warning below the threshold (so the
hot path stays quiet). C2: assert the logged line is bounded and still contains the
job key, and that the full `Cmd` is unchanged on the job itself.

### Scale gate
Probably none needed — say so explicitly rather than adding one that cannot fail.
If you want one, the honest measurement is log bytes per completed job over a
wrdev run, which `bkill-hygiene`'s log-bytes technique (`11f1537`) shows how to do.

---

## ITEM D — Finding 7: web status counts diverged (274 shown vs 4 actual)

**Route:** investigate first; `/bugfix` **only if it reproduces**. Batch 3.

The doc is explicit that there is **no fix until it is reproduced**, and that a
reconnect re-seeds the counts and hides the bug — which is exactly why the
operator's page refresh "fixed" it.

### Repro shape (from `.docs/reliable4/prod-run-20260817.md` Priority 6)
A `wrdev.sh` mode: run a load with `wsprobe` attached **throughout**, then
mid-flight set the limit to 0 so several hundred runners exit near-simultaneously;
compare wsprobe's reconstructed counts against REST truth **without** reconnecting.

- `wsprobe` is built from `.docs/reliable2/phase2/wsprobe`; a built copy is at
  `/nfs/hgi/wr/sb10-pprof/wsprobe`.
- Start from `jobqueue/jobtransition.go` (the `jstateCount` delta machinery — see
  the comments at `:43`, `:57`, `:96`: one delta per `(from, to, repGroup)` group)
  and `jobqueue/server_subscription.go` (the per-client queue and any drop path).
- **The question to answer first:** is a `running→X` delta *dropped*, or *never
  emitted*? Do not propose a fix before answering that.
- Related prior art: `.docs/bugfixes/` has the flicker fix (branch `flicker`),
  which was a purely client-side occupancy reconciliation in
  `websocket-handler.js` — check whether this is the same class of bug or a
  genuinely server-side delta loss, and do not re-fix what that already fixed.

If it does not reproduce, **say so and stop** — record what you tried and what it
would take (a live prod repro with `wsprobe` attached across a real limit drop).
An un-reproduced fix here is worse than none, because the symptom is cosmetic and
the machinery is on the status hot path.

---

## ITEM E — queue-mutex contention, `Reserve` in particular

**Route:** measure first, then decide. Batch 5, i.e. **after** the validation gate.

The 2026-08-17 block profile, `-peek 'RWMutex).Lock'`, cumulative:

```
281,736s  63.6%  queue.(*Queue).Reserve
108,516s  24.5%  queue.(*Queue).lockExistingItem
 25,774s   5.8%  queue.(*Queue).lockItemInState
 25,740s   5.8%  jobqueue.(*db).updateJobAfterExit
```

`handleReserve` accounted for 9.86 hrs of block delay (10.7%) and `handleRelease`
for 39,190 block events. The doc's own assessment: queue-mutex is "top contention
but O(log n)/sub-second, NOT the cause" of the freezes — that still holds — "but
`Reserve` is now the single largest consumer of it and is worth a look after
Findings 2 and 3 land."

Entry points: `queue.Reserve` (`queue/queue.go:1407`), `lockExistingItem`
(`queue/queue.go:1471`).

**Do not start with a fix.** This is an optimisation whose value depends on
numbers that the five landed fixes have already changed — the archive path no
longer holds the bolt lock for 43 s at a time, so the contention profile is
different now. Re-measure first (the validation gate in batch 4 will produce a
fresh block/mutex profile at scale; use it). Then decide whether anything is worth
doing, and be ready to conclude "not worth it". DEVELOPERS.md rule 2 applies with
full force here: no new server-wide exclusive lock on the transition path.

One related measurement to take at the same time, **not** a fix: prod showed
`bbolt freelist.(*hashMap).freePageIds` at **18.6% of all CPU** on the 10.3 GB DB.
Archives now commit roughly 5× fewer transactions (`bolt_txns/job` 0.995 → 0.200 in
the production arrival regime), so that cost should have fallen for free. Confirm
that before anyone reconsiders the `NoFreelistSync` idea — it was **dropped** on
2026-07-29 because it breaks the fast-startup invariant, and that decision stands.

---

## BATCH 4 — the farm-scale validation gate (operator-run)

Full spec: the "Validation gate for the whole batch" section at the end of
`.docs/reliable4/prod-run-20260817.md`. Shape: a farm-scale re-run equivalent to
the 2026-08-17 session — **~115k jobs** across a success group and a failure group,
`results_portal` limit **2000** (production's value — *not* 20000; the profiling
doc explains why), on the real ~10 GB DB shape.

Targets:

- mean archive block **< 5 s**, p99 **< 60 s** (was: mean 43 s, tail over the floor)
- **zero** runner-side `receive time out` (was 10 in 25 min)
- compress jobs reaching `delayed` with `Exitcode 0`: **zero** (was continuous)
- idle manager with a large limit-blocked backlog: CPU **near zero** (was 0.79
  cores / 41.8%)
- `wr limit` and `wr suspend -i <rg>` return in **< 5 s** under full load (was
  minutes / timed out) — **this is where Finding 6 gets confirmed**
- `wr resume -i <substr> -z` completes without a heap excursion (was 12.1 GB)

Capture a fresh block + mutex + CPU profile during the run; item E depends on it.
The reusable tooling (tiered tracker, log summariser, in-flight-handler census) is
listed under "Artifacts and tooling" in the profiling doc, all under
`/nfs/hgi/wr/sb10-pprof/`.

**Only after this gate passes should a live-prod profiling round be considered.**

---

## HOW TO WORK ON THIS REPO — hard-won specifics, please read

### Quality gates (exact commands)
- `make lint` — golangci-lint; must be **0 issues**.
- `unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' '); timeout 1800 make test`
  — **you must unset all `OS_*` vars** or the suite takes ~16 min instead of ~4.
  Baseline at `8060cff` is **393 passed · 9 skipped · 29 packages**.
- `unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' '); timeout 2400 make race`
  — same baseline, 0 data races.
- `make BENCH=<name> bench` — `BENCHTIME` defaults to `1x`, so a single-op ns/op
  figure on a loaded host is noisy; use `BENCHTIME=10x` for a steadier number.
  These benchmarks exist specifically to guard bolt write-coalescing.

### Every new `developers/wrdev.sh` scale gate must be proven to FAIL pre-fix
**Three separate false-PASS gates were caught by reviewers in this batch**, so this
is not hypothetical:
- `idle-backlog-cpu` PASSed when the pprof fetch failed (unparsed → `totms=0`).
- `archive-rate`'s first version PASSed on pre-fix code, because un-jittered
  archivers finish in lockstep and all arrivals land inside one 10 ms bbolt batch
  window, coalescing *without* the fix. Its jitter is load-bearing.
- `control-rpc-history` sampled its VmHWM baseline *after* the in-band reference
  scan, so the RSS metric could never fail post-fix.

Required pattern, now used by `idle-backlog-cpu`, `archive-rate`,
`control-rpc-history`, `exec-impossible-retries` and `bkill-hygiene`:
- a hard `FAIL (NOT MEASURED)` branch when the measurement is missing, zero or
  unparseable (no profile, dead manager, no summary line, zero samples/cycles/jobs)
  — **a gate that passes when the measurement is absent is worse than no gate**;
- capture the pipeline exit status (`|| rc=$?` … `return "$rc"`) so an appended
  cleanup `rm` cannot swallow a FAIL exit code;
- if the gate copies a big DB: pass `WRDEV_ROOT="$WRDEV_ROOT"` on the `go test`
  inline prefix (it is **not** exported), use a `local work=...` path and `rm -f`
  it in cleanup. `cbfac88` fixed three modes that were otherwise leaking 10 GB
  copies into the shared fixture dir.
- prove the A/B from a pristine `git worktree` at the pre-fix commit, and say so.

**The operator's standard for LSF-adjacent gates** (2026-08-19): they "don't need
to be 100%, just good enough to be indicative and usually show the problem before
the fix". Mock-exe (`bjobs`/`bkill`/`bsub`) at realistic scale is the right shape —
see `bkill-hygiene`. Do not skip a gate because a faithful one is impossible.

Existing modes: `archive-rate`, `backlog-rescan-check`, `bkill-hygiene`,
`control-rpc-history`, `exec-impossible-retries`, `flicker-check`,
`idle-backlog-cpu`, `overprovision-check`, `priority-fairness-check`,
`report-storm`, `writestorm-freeze`.

**Regression gates to re-run after any change here:**
`developers/wrdev.sh idle-backlog-cpu 50000 25` (guards `8087866`; expect ~700–850
ms CPU per 25 s) and `developers/wrdev.sh backlog-rescan-check 2000 50000` (guards
260725-2's `scanWork == limit` invariant; expect `scanWork=2000`).

Two pre-existing wrdev modes FAIL for unrelated reasons and are **stale
reproducers, not regressions**: `overcount-check` and `limit-stall-check` both
assert the *presence* of reliable3 bugs that have since been fixed.

### This editor's compile-error diagnostics are frequently stale gopls
**Five times in this batch** a `X is undefined` / `not constant` / type-mismatch
error was reported while `go vet` was clean and the suite compiled. In one case the
reported line numbers were offset by exactly 8 from the real call sites — gopls had
indexed an earlier draft. Always verify with the real toolchain before acting:
`go vet ./jobqueue/`, plus `go vet -tags "netgo reliability_repro" ./jobqueue/ ./cmd/`
for build-tagged files (a "No packages found … excluded due to its build tags"
warning on a `reliability_repro` file is expected and harmless). Verify, don't
dismiss — but don't rewrite working code to satisfy a phantom.

### Never accept a "known flake" attribution without a load-matched A/B
This is a shared farm node that sits at **load average 85–120 on 8 cores**. A full
`make race` failed three unrelated tests at load ~118 and passed the *same code* at
~86–104. Known load-dependent victims, each A/B-proven on an md5-identical tree
during this batch: `TestJobqueueExecutionAndDependencyScenarios`
(`jobqueue_test.go:4392`), `TestJobqueueProduction`, `TestServerWebI`,
`TestSubscriptionReconnectResync` (`subscription_test.go:1557`, a 3 s bound).
`.docs/bugfixes/260626-3.md` documents the contention-flake class.

Practice: record `uptime` next to every result; A/B against a pristine worktree
before blaming *or* excusing; and when the failing test is in the same subsystem as
your change, **verify** the package-boundary argument rather than asserting it (a
kill-path test failed right after a kill-path change in this batch; the boundary
argument turned out to hold, but only checking established that).

### Mutation-test your guards, not just your fix
Bug 5's reviewer reverted each guarantee one at a time and found **two mutations
that every test survived** — the fix's actual point (an unexplained `bkill` outcome
must not be assumed successful, and must reach an operator at the default log
level) was unpinned, so a later refactor could have silently restored the original
prod problem. For each guarantee you add, ask: *which single assertion fails if
this specific guarantee is removed?* If the answer is "none", the guarantee is not
protected.

### Host safety
- **A real production `wr manager` runs on this host** (the pid changes; it was
  1729307, then 2825311 — do **not** assume). Never kill a manager you did not
  start. `wrdev.sh`'s `is_ours` guard only ever touches `$WRDEV_ROOT/wr`.
- Do not submit or kill real LSF jobs from tests. Use mock exes.
- A long-running stray `jobqueue.test` (pid ~1129984, 20+ days old) belongs to an
  earlier session — leave it alone.
- `/nfs/hgi/wr/sb10-bigdb/` is a **shared fixture dir**; `pristine10` (10,729,893,888
  bytes) must stay byte-for-byte untouched. Verify size+mtime before and after.
- Test scratch belongs in `t.TempDir()` or `.tmp/agent/` in the repo.

### Agent/tooling failure modes seen in this batch
- **Always pass an explicit `timeout` to long commands and wait for them in the
  foreground.** An agent that backgrounds `make race` and ends its turn produces a
  stub result and can loop doing so, burning a lot of wall-clock.
- `pgrep -f "<pattern>"` **self-matches** the `bash -c` wrapper whose command line
  contains that pattern. This wedged an agent in an infinite poll loop waiting for
  a process that had already finished. Use an anchored/`-x` pattern, match the test
  binary, exclude `$$`, or just poll the log file.
- A transient API error kills a subagent but **not its edits** — check
  `git status` and resume it (its context is intact) rather than restarting.
- `git stash` may be blocked by the permission classifier; A/B by copying the file
  aside and restoring it, verifying with `md5sum`.

### Other project conventions worth knowing
- Inert test instrumentation is an established pattern here: `Job.derivations`
  (`8087866`), `db.archiveTxObserver` (`f7e36bc`), `db.archivedDecodes`
  (`5c75a15`). Mark it INERT in production and keep it off the hot path.
- `cleanorder -min-diff` is mandated on edited `.go` files by the go-implementor
  skill, but it wants ~1774 unrelated lines moved in `jobqueue/db.go`; apply it
  only where it does not create churn, and say when you skipped it.
- Do not change `schedulerGroupString`'s output format — scheduler group names are
  persisted and parsed elsewhere (`~results_portal` suffix parsing, LSF job names).
- `.docs/issue-197/spec.md` is a **binding written contract** for the REST
  modification endpoints; a reviewer caught a silent 409→404 change against it in
  this batch. Check for a spec before assuming a status code is untested collateral.

---

## Remaining smaller follow-ups (recorded, not scheduled)

From `.docs/bugfixes/260818-1.md`, not covered by A–E:

- **RPC request cancellation** so a disconnected/timed-out client stops the work.
  Prod had two `handleGetByRepGroup` goroutines still CPU-bound **12+ minutes**
  after the client gave up, one encoding a multi-GB reply for nobody. Plumbing
  cancellation across the RPC layer is plausibly a `/spec-writer` item, not a
  `/bugfix`. Item B reduces the damage; this removes the class.
- `jobqueue/scheduler/lsf.go` `cleanup()` (~`:2036`) still has the same
  unbounded-argv/no-timeout `bkill` shape `11f1537` fixed on the cycle path. It is
  one-shot at shutdown, but with tens of thousands of ids it can fail wholesale
  with E2BIG and orphan every LSF job it meant to reap. `bkillArgs` +
  `slices.Chunk` already exist, so it is a handful of lines.
- `getRepGroupsList` still calls `retrieveRepGroups()` for every non-exact match,
  so `-z` enumerates every historical repgroup. The expensive decode is gone, so
  this is now cheap — but it is still O(all repgroups ever).
- A narrow back-compat gap in the new 409 probe: `bucketRGEndTime` arrived in
  v0.36.5 with no rebuild on upgrade, so a repgroup whose last completion predates
  v0.36.5 and which has no live jobs answers 404 where the spec says 409.
  **Deliberately not fixed** — a rebuild would be a startup history scan, violating
  the very rule 6 that `5c75a15` extended.
- A warning-only (not rejecting) over-long-`Cmd` notice in `cmd/add.go`. Note
  rejecting would be **wrong**: with `job.Group` set, `buildExecCmd` feeds the
  command to `newgrp` on stdin, so an over-long `Cmd` execs fine.
- Known trap for anyone reusing `memoBacklogServer`
  (`jobqueue/reliable4_snapshot_memo_test.go`): it pauses the server, and the drain
  early-return at `jobqueue/server.go:4199` happens before `racRunning = true` and
  before `defer s.finishRAC()`, so a trigger that ran `setRACPending` while paused
  leaves `racPending` true with `waitingReserves` unclosed — a future test that
  reuses the helper **and reserves a job** would hang rather than fail. Call
  `Resume()` first.

---

## Operational notes that are not code

- The default-1h wedged-runner **kill backstop** (from `804e05a`, PR #555) still
  needs the farm `authorized_keys` forced command updated per `cmd/conf.go` to
  permit `kill -9`, otherwise it is a safe no-op. Same forced-command trap that
  bit reliable3 (`ps -o stat=` vs the old line-count).
- Prod debug logging was turned **on** during the 260725 investigation (~12 MB/min).
  Confirm whether it is still on before the next prod round.
- `client.token` is mode 600 `mercury`, so a non-mercury investigator cannot run
  `wr status`. Use the REST API with the token from the fg log's web-interface URL,
  and **always pass a `state`** — omitting it used to trigger Finding 1's history
  scan (now fixed for control paths, but `wr status` still decodes; see item B).
- Sample control RPCs at **≤10 s** when profiling; 60 s sampling missed the
  `wr limit` stall entirely. But do **not** take `goroutine?debug=2` more often
  than ~20–30 s above ~50k goroutines — that observer effect ruined the 2026-07-28
  CPU profile.
