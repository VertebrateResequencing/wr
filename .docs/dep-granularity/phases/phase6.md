# Phase 6: Proof that gates the merge (section F)

Ref: [spec.md](../spec.md) sections F1, F2, F3, F4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

F1 and F2 can be written as failing tests **before** phases 2-4 and used to
drive them, except F2 acceptance tests 2 and 3, which are regression guards that
already hold pre-change. If they were written early, this phase is where they
are confirmed and reviewed rather than authored. F3 is the scale gate and must
be proven to FAIL from a pristine `git worktree` at the pre-fix commit before it
is trusted. F4 runs after every phase, and again here.

The one new test file, `jobqueue/depgranularity_memory_test.go`, carries the
go-conventions copyright header; `jobqueue/depgranularity_add_test.go` (phase 4)
and `jobqueue/depgranularity_scale_test.go` (phase 5) already exist and are
added to. `jobqueue` tests use GoConvey `So()` assertions only and guard with
`if runnermode || servermode { return }`. Never put `So()` inside a loop of
more than 20 iterations; count and assert the final count - this phase's
fixtures run to thousands of members and waiters.

Then restart production on the result. That restart is the final evidence; no
real-LSF Tier-B run is required, because memory reproduces in-process and the
spec's figures are memory figures.

## Proving tests RED

- F1 tests 1, 2 and 3 are the primary evidence and must fail without the fix.
  All three share a file with test 3's `s.depGroups.memberships()`, a symbol
  this work introduces, so the pre-fix tree does not compile with them. Every
  RED run here is therefore **inside the fixed tree with the behaviour
  withheld**. F1 test 4 is not a committed test: it is a **mutation check the
  implementor performs and records** - with `dependencyKeys` reverted to
  expanding group members, tests 1 and 2 both fail. That mutation is not test
  3's route. Its `memberships()` half comes from C1's recovery rebuild, so
  withhold `registerDepGroupMembers` for that one.
- F2 test 1 is C2's five tests. F2 tests 2 and 3 are regression guards that
  already hold pre-change, each labelled so with its reason. Do not hunt for a
  pre-fix failure behind them.
- F3 test 1 is the pre-fix FAIL, run from a pristine worktree at the pre-fix
  commit - this delivery's branch point, `8b9ba00`, not the older `bf53de0`
  that F3's log-line references cite. F3 test 3 is a deliberately broken
  invocation (the fixture DB path removed) that must print
  `FAIL (NOT MEASURED)` and exit non-zero.

## Items

### Batch 1 (parallel)

#### Item 6.1: F1 - Memory is linear in the work [parallel with 6.2]

spec.md section: F1

Test-only, in `jobqueue/depgranularity_memory_test.go`. Two assertions, one
exact and one bounded. **The exact one is primary because it can never flake**:
the total number of dependency keys retained across all recovered items must
equal the number of declared group edges plus live essence edges, and must not
change when the group's membership grows. The bounded one measures actual
retained heap, following go-conventions' memory-bounded pattern and the in-tree
`memoMallocsDuring` helper (`jobqueue/reliable4_snapshot_memo_test.go:355`):
`runtime.GC()` plus `runtime.ReadMemStats` before and after, with the resolved
result **held live across the second read** so the GC cannot collect what is
being measured, and unsigned-underflow guarded.

Covers all 4 F1 acceptance tests. Carry these figures:

- Test 1: one group of M live members and W = **2,000** live waiters;
  `sum(len(rj.deps))` over `db.resolveDependencies` equals `W` for M = 200 and
  equals `W` for M = 2,000 - the retained key count does not scale with M at
  all.
- Test 2: across the same two fixtures, heap growth divided by the number of
  resolved jobs is below **2 KB** in both cases, and the M = 2,000 figure is no
  more than **1.5x** the M = 200 figure.
- Test 3: on the M = 2,000 fixture, a full `serve` recovery leaves
  `s.depGroups.memberships()` equal to the fixture's total (group, member) pair
  count, and the sum of `len(item.Dependencies())` over all queue items equal to
  the fixture's total declared edge count.

- [x] implemented
- [x] reviewed

#### Item 6.2: F2 - Transaction counts [parallel with 6.1]

spec.md section: F2

Test-only, in `jobqueue/reliable4_dependency_tx_test.go` and
`jobqueue/depgranularity_add_test.go`. Both directions stay pinned with bbolt's
own counter and no new instrumentation: one read transaction **per chunk** of
`dependencyResolutionChunkSize` jobs, never one per job, and never one for the
whole pass. Those counts have to survive C2's rewrite rather than sit in an
untouched file.

Covers all 3 F2 acceptance tests:

1. C2's five acceptance tests pass (phase 3).
2. D1 acceptance test 2 (phase 4): with M = 200 and M = 2,000 live members, the
   add path's `TxN` delta is the same within **1.25x**. A regression guard that
   already holds pre-change - `retrieveIncompleteJobKeysByDepGroup`
   (`db.go:3036`) opens exactly one `View` per waiter whatever the membership,
   so the pre-fix ratio is already ~1.0.
3. Recovery of N live jobs at the production chunk size of 1,000 grows the
   dependency pass's `TxN` by **exactly `ceil(N/1000)`**. A regression guard
   that already holds pre-change: every read the pass makes - each essence
   `checkIfLive`, each `bucketDepGroups` seen check - goes through `txDepReader`
   (`dependency.go:85-103`) on the chunk's own transaction, so neither adds a
   transaction, and that was already true at `c96dcbf`. Tightening the bound
   from a range to exactly `ceil(N/1000)` does not make it fail pre-fix. Keep it
   anyway: a bound that allowed one transaction per essence dependency or one
   per group would be loose enough to hide a regression to per-job reads inside
   a chunk.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill (review all items in
the batch together in a single review pass).

### Item 6.3: F3 - `developers/wrdev.sh dep-granularity-check`

spec.md section: F3

Add mode `dep-granularity-check [waiters] [members] [groups]` to
`developers/wrdev.sh`, defaulting to **30,000 waiters, 3,000 members in one
group and 6,300 groups total**, scalable to prod's shape (150,000 waiters,
19,000 members) on a large host via `WRDEV_DEPGRAN_*` env knobs. The defaults
are chosen so the pre-fix run fails without exhausting the dev host. It must
cover the **add path** as well as recovery.

Follow F3's seven procedure steps exactly. Two of them are the difference
between a real gate and a false PASS, and an earlier batch caught three
false-PASS gates:

- **Step 1**: build the fixture DB through `db.storeNewJobs` in a Go test
  (`TestDepGranularityFixture`, build tag `reliability_repro`, in
  `jobqueue/depgranularity_scale_test.go`, which phase 5's E9 created), **not**
  by writing `bucketJobsLive` directly, so `bucketRDTK` and `bucketDepGroups`
  are populated the way the real add path populates them. A fixture that
  populates only `bucketJobsLive` makes the pre-fix run resolve no member keys,
  never allocate quadratically, and false-PASS - the same failure mode as the
  `pristine10` history fixture. Step 2 stores the group's **members before** the
  waiters, for the reason F3 gives.

  **`bucketDTK` is the exception, flagged.** F3 step 1 has `storeNewJobs`
  populate that bucket too, and after A3 it no longer does: `dgLookups` is
  dropped, so a job stored with `DepGroups` leaves `bucketDTK` empty (A3
  acceptance test 1). Hand the pre-fix binary such a DB and
  `retrieveIncompleteJobKeysByDepGroupTx` (`db.go:3050`) finds no member keys
  for any group, every seen group looks satisfied, nothing quadratic is
  allocated and the gate PASSes pre-fix - the very false-PASS this bullet
  exists to stop - while F3 acceptance test 4 could never hold. The fixture
  must therefore write those entries itself, as the pre-upgrade data they are:
  one `depGroup + dbDelimiter + jobKey` key per (group, live member) pair, the
  shape `db.generateLookupKey` produces and exactly what pre-fix
  `storeNewJobs` wrote, in the pattern `replaceLookupRebuildTestBucket`
  (`db_test.go:1499`) already uses. That is the DB shape the upgrade meets in
  any case, since every live job in production was added by a pre-fix binary.
- **Step 4**: delimit the window with the two log lines that exist in **both**
  trees - `bf53de0`'s `recovering prior state` (`server.go:1497`) and
  `recovering: prior state recovered` (`server.go:1515`), both at warn. The
  progress line `recovering: still recovering prior state` (`server.go:1565`)
  contains the start line's text, so open the window on the first match, not
  the last. **Do not key the window on the manager becoming reachable, or on
  publication**: at the pre-fix commit there is no publication step, so a "wait
  until it answers" check returns at t = 0, the window collapses, `VmHWM` is
  read before recovery has allocated anything, and the gate PASSes pre-fix. Poll
  `VmHWM` from `/proc/<pid>/status` once a second from process start until the
  finish line or the process dies, keeping the largest value seen; sampling
  throughout also survives the pre-fix run being OOM-killed mid-recovery, when
  `/proc/<pid>/status` vanishes.

Step 5 runs `wr manager status --deployment production` once inside the window
and records `statusInWindow=` as `starting`, `up`, `nonresponsive` or `-`. It is
the only automated exercise of E5's one production change. On `-`, and only when
the run otherwise completed, redo it once with the waiter count doubled and
record the retry in `errors=`; widen once only, and do not widen a run that
already FAILs for another reason. Step 6 times `wr add` of one job into the big
group as `addSec`. Step 7 prints one `DEPGRAN-SUMMARY` line with `waiters=`,
`members=`, `peakRssMb=`, `recoverySec=`, `statusInWindow=`, `addSec=` and
`errors=`, writing `-` for any metric the run never reached.

Gate shape follows `archive-rate`: capture the pipeline exit status (`|| rc=$?`
... `return "$rc"`) so an appended cleanup `rm` cannot swallow a FAIL; `FAIL
(NOT MEASURED)` when the summary line is absent, when `peakRssMb` is missing,
zero or unparseable, or when `recovering prior state` never appeared; **plain
FAIL**, not `FAIL (NOT MEASURED)`, when the start line appeared but the finish
line never did, since that is the pre-fix outcome the gate exists to catch -
`peakRssMb` is still reported from the samples taken before the death, and
`errors=` names it; FAIL on any threshold exceeded; and FAIL on
`statusInWindow=nonresponsive` or on `-` after the doubled-waiter retry. `up`
is not a FAIL - it is the pre-fix answer, and acceptance test 1 does not rest
on it.

**Where the thresholds come from.** Each of `WRDEV_DEPGRAN_MAX_RSS_MB`,
`WRDEV_DEPGRAN_MAX_RECOVERY_SEC` and `WRDEV_DEPGRAN_MAX_ADD_SEC` is an
env-overridable shell default in the pattern `archive-rate` uses for
`WRDEV_ARCHRATE_MAX_MEAN_MS`. Set each to **2x the post-fix figure** acceptance
test 1 records at the default shape, rounded up to a round number, then check
the result is at most **half the recorded pre-fix figure** - a threshold the
pre-fix run would pass is not a gate. If a metric's pre-fix and post-fix figures
turn out closer than 4x, that metric does not discriminate at this shape: say so
in the recorded numbers and gate on the other two rather than picking a value
between them. Write the chosen values and the figures they came from into
`.docs/dep-granularity/` beside the gate, so a later run can tell a real
regression from a re-tuned threshold.

Files: `developers/wrdev.sh` and `jobqueue/depgranularity_scale_test.go`.
Covers all 5 F3 acceptance tests. Test 1 requires a pristine `git worktree` at
the pre-fix commit, a printed FAIL, and a recorded `peakRssMb` at least **4x**
the post-fix figure, with both numbers recorded in `.docs/dep-granularity/`.
Test 2 requires the fixed tree to print PASS with all three metrics measured
and non-zero, which is what stops a run that never reached `wr add` passing on
an `addSec=-`. Test 4 inspects the produced DB and requires `bucketDTK`,
`bucketRDTK` and `bucketDepGroups` all non-empty with `bucketDTK` holding
`members` entries for the big group. Test 5 requires
`statusInWindow=starting`, never `nonresponsive` or `-`, since a run ending at
`-` leaves E5's command wiring with no automated coverage at all.

**Running test 1 at the pre-fix commit, flagged.** Neither the gate mode nor
`TestDepGranularityFixture` exists at `8b9ba00`, and copying
`jobqueue/depgranularity_scale_test.go` across is not a safe way to get the
generator, since E9's test 2 shares the file and exercises a startup phase that
does not exist there. Build the fixture DB once in the fixed tree and give each
run its own copy of it, as `archive-rate` takes a pre-built `WR_ARCHRATE_DB`:
add a `WRDEV_DEPGRAN_DB` knob that skips generation and measures the supplied
file. This only works because step 1's fixture writes `bucketDTK` itself. Leave
those entries to `storeNewJobs` and the pre-fix run has nothing to expand, so
it hands back a PASS. `wrdev.sh` resolves `REPO` from its own path
(`developers/wrdev.sh:37`), so the pre-fix worktree's copy of the script builds
and measures the pre-fix binary. Pristine means the product code under test,
not the harness, and both runs then measure the same bytes. Record how it was
arranged beside the numbers.

**Host safety.** Run the gate against an isolated production-mode manager under
`$WRDEV_ROOT`, never against `/nfs/hgi/wr/lsf/.wr_production/`, and keep the
fixture copy and its heavy I/O off `/nfs/hgi`.

- [x] implemented
- [x] reviewed

### Item 6.4: F4 - Regression guards

spec.md section: F4

Covers all 5 F4 acceptance tests: the `queue` package; `jobqueue`, `cmd`,
`client` and `client/testing` unchanged and green, including the six named
`jobqueue` tests and every test that starts a server through the helpers E2
changes; the **nine** tests this work deliberately rewrites, each still
asserting the same property against the new shape and none deleted or weakened
(`TestDBReverseLookupIndex` and `TestDBDepGroups` from A3;
`TestReliable4DependencyResolutionUnchanged`,
`TestReliable4DependencyFreeTxCost` and `TestReliable4RecoveryDependencyPass`
from C2; `TestReliable2RecoveryWindowReturnsRecovering` and
`TestReliable2RecoveryRestoresIncompleteJobs` from E6; and
`TestServeReportsPostUpgradeStartupUntilTokenReady` and
`TestServeDoesNotReportPostUpgradeStartupForBrandNewDB` from E4); the three
quality-gate commands; and the two `wrdev.sh` gates
(`idle-backlog-cpu 50000 25` and `backlog-rescan-check 2000 50000`).

Run the gate commands as the spec writes them:

```bash
make lint
unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' '); timeout 1800 make test
unset $(env | grep -o '^OS_[A-Z_]*' | tr '\n' ' '); timeout 2400 make race
```

**Record `uptime` next to every result.** This is a shared node at load average
85-120 on 8 cores, and `TestJobqueueExecutionAndDependencyScenarios`,
`TestJobqueueProduction`, `TestServerWebI` and `TestSubscriptionReconnectResync`
are known load-dependent victims. Never accept a "known flake" attribution
without a load-matched A/B against a pristine worktree. A whole-package
`ErrNoServer` in `cmd` or `client` is the opposite of a flake: it is a helper
that never got its `Serving()` wait (E2), so check the helpers before spending a
load-matched A/B on it.

**Baseline figure, flagged.** F4 item 4 pins `make test` at "413 passed / 9
skipped / 29 packages or better", quoted from `bf53de0`. This branch is two
commits past that: at `8b9ba00`, `.docs/bugfixes/260825-3.md` records **421
passed, 9 skipped, 29 packages** for its own item 2 fix. Measure `make test` at
the branch point, take that count as the baseline rather than the literal 413,
and record which figure was used.

- [x] implemented
- [x] reviewed

## Phase gate

- All four items' acceptance tests pass, and phases 1-5's gates still hold.
- F3 recorded pre-fix and post-fix numbers, its three chosen thresholds and the
  figures behind them, plus E9's phase measurements and stated ceiling, all in
  `.docs/dep-granularity/` before merge.
- Then restart production on the result.


## Phase 6 outcome (2026-08-26)

Implemented and reviewed PASS. Every discriminator claim was reproduced
independently by the reviewer rather than taken on report.

### The gates discriminate - proven, not assumed

**F3, default shape (30,000 waiters x 3,000 members, 6,300 groups), pre-fix
worktree at `8b9ba00` against post-fix `f2ccaee`:** pre-fix climbs 763 -> 2,069
-> 4,502 -> 7,368 -> 9,896 MB with nothing in the log but "still recovering
prior state", and is killed at the harness RSS ceiling with recovery never
finishing. Post-fix: **285 MB, exit 0**, recovery 0.196 s, add 0.459 s. The
ceiling figure is a **floor** on the true pre-fix peak, so the ratio is
"at least 28x", not a measurement.

**Reduced shape (30,000 x 300), where the pre-fix arm completes** and all three
metrics have real numbers on both sides:

| metric | pre-fix | post-fix | ratio |
| --- | --- | --- | --- |
| peak RSS | 3,635 MB | 279 MB | 13.0x |
| recovery | 17.098 s | 0.129 s | 132x |
| add | 17.957 s | 0.408 s | 44x |

All three discriminate by far more than 4x, so all three are gated.

**The false PASS was demonstrated, not reasoned about.** Remove
`dgfWriteLegacyLookups` from the fixture - so `bucketDTK` is left to
`storeNewJobs`, which after A3 writes nothing - and the **pre-fix binary passes**
at 342 MB. That is this repo's five-times-repeated failure mode caught
deliberately. The reviewer additionally found the gate cannot silently accept a
blunt self-generated fixture: GoConvey's `FailureHalts` aborts the generator
before its `DEPGRAN-FIXTURE` line, so the gate takes its
`FAIL (NOT MEASURED)` branch - but that protection is **implicit**, depending on
the `Printf` sitting after the `So()`s while the gate ignores `go test`'s exit
status.

F1 and F2 mutations both re-run and confirmed: expanding group members again
gives 400,000 and 4,000,000 keys (3,175 and 16,418 B/job) and fails three named
lines; withholding `registerDepGroupMembers` fails one; a per-read reader fails
three; one-chunk-per-pass fails two of three, the 100-job case being one chunk
either way - which is exactly why the 2,500-job case was added.

### Two measurement defects found by measuring, not by reasoning

Both would have produced silent false PASSes, and both are now guarded by
`growth > 0` assertions in the same block as the bounds:

- A single `runtime.GC()` reads **zero** on some runs, because `HeapAlloc`
  counts unswept dead objects. It collects twice now.
- Without `runtime.KeepAlive` on the fixture, the big fixture's growth read
  **-565,128 bytes** - the collector took `depGroupMembers`' 2,000 per-job maps
  between the two reads. An underflow-guarded zero passes every bound.

Every other measurement in the phase was audited for the same failure mode and
is either an exact equality or explicitly sentinelled into `FAIL (NOT MEASURED)`.

### Three harness-fidelity bugs, each of which measured the harness

`date`/`awk`/`ps` per poll turned a 13 ms recovery into 259 ms of fork latency;
`VmHWM` sampled once a second reported **10 MB where the peak was 212 MB**; and
an inline status sample blinded the log reader for 25 ms so `recoverySec` read
0.000. Fixed with `$EPOCHREALTIME`, builtin `/proc` reads, a fifo-as-sleep, a
VmHWM read on every poll, and a backgrounded status sample whose
answered-after-the-window rule biases toward FAIL, never toward PASS.

### `wr manager status` on the daemonized path: expected, by operator decision

During the window it dies with "could not read token file; has the manager been
started?", because `connect` reads the token before `reportManagerStartupStatus`
is reached and E1 moved the token write to publication. **Operator decision
(2026-08-26): this is not a bug** - status is only expected to work once
`wr manager start` says the manager has started, which follows from the intent of
the whole E-series. `wr manager start` itself printed progress throughout the
measured trace. `.docs/dep-granularity/scale-gate.md` and spec E5's story text
were reworded to record it as expected; **do not make the pid-file branch consult
the sidecar, as that would undo E1.**

Consequence: E5's useful half is the **no-pid-file** branch (`starting: <phase>`,
which the gate asserts and which covers the instant before the pid file exists).
The pid-file branch's `reportManagerStartupStatus()` call at `cmd/manager.go:511`
is **unreachable dead weight** - `connect` either returns a client or `die()`s,
and E5's tests miss it because they drive the helper directly.

### Gates

`make lint` 0 issues; `go vet -tags netgo` and `-tags 'netgo reliability_repro'`
clean; `make test` **483 passed - 9 skipped - 29 packages** four times across
implementation and review at load 118-125; **`make race` 483 passed - 8 skipped,
0 data races** (F4 item 4, previously unrun in this delivery);
`-race --count 3` on the new tests clean; `cleanorder -min-diff -dry` zero diff
on all three changed Go files; `bash -n developers/wrdev.sh` OK;
`wrdev.sh backlog-rescan-check` and `idle-backlog-cpu` both PASS. No failures of
any kind in either review pass, so no flake attribution was needed.

Baseline arithmetic: `8b9ba00` was **421 passed - 9 skipped** (per
`.docs/bugfixes/260825-3.md`, not the older 413 figure); phases 1-5 add 61 test
functions of which one is behind `reliability_repro`, and phase 6 adds 2, so
421 + 60 + 2 = 483.

### Follow-ups, none blocking

1. `cmd/manager.go:511`'s `reportManagerStartupStatus()` is unreachable - delete
   it or rework the branch.
2. **E2 acceptance test 4 still has zero automated coverage** (`wr manager start
   -f` prints "started on" only after the bind). The gate now *prints* the
   evidence - post-fix "started on" lands 0-2 ms after "prior state recovered",
   pre-fix 16,099 ms before - but the FAIL condition was built and then removed,
   on the reasoning that it would fail the pre-fix arm on ordering before the
   memory metrics were compared, voiding the A/B. The reviewer judged that
   half-right: placed **last**, after every metric threshold, it cannot mask a
   blunt fixture, because a pre-fix or blunt run fails on memory first and never
   reaches it. Better still, the ordering is observable in a daemonized manager's
   own log, so a durable `cmd`-level test needs no in-process `startJQ`.
3. `dep_granularity_run` takes 16 positional parameters where the rest of
   wrdev.sh uses globals - one miscounted argument silently shifts everything.
4. The gate starts the manager with `-f`, so no pid file: an interrupted run
   leaves a manager `safe_kill` cannot reap, and the next run's `rm -rf` would
   delete a live manager's directory. Fails loudly rather than falsely; a trap or
   cmdline sweep would close it.
5. If `mkfifo` fails, `read -t -u 8` errors every iteration - a tight spin for up
   to `waitsec`. `mkfifo || die` closes it.
6. `MAX_RECOVERY_SEC=1` is ~5-6x the post-fix figure and `MAX_ADD_SEC=2` ~4x,
   rather than the "2x rounded up" the spec asks; 0.5 and 1 would be tighter.
   Both still clear the "at most half pre-fix" test.
7. Add a comment at the fixture step's `|| true` recording that the
   missing-`DEPGRAN-FIXTURE`-line branch is what catches a failed generator.
8. A fixture supplied via `WRDEV_DEPGRAN_DB` is validated only for existence -
   worth a line in the doc's caveats, since that is the A/B path.
9. Fixture shape is production's scaled down unevenly: live jobs 39,299 vs
   150,472 (3.8x) but memberships 9,299 vs >=250,000 (27x), with every non-big
   group holding one member. The concentration into one 3,000-member group is
   what drives the expansion and 28x discrimination proves it is enough, but the
   `WRDEV_DEPGRAN_*` knobs exist to reach the real shape.

### Follow-ups closed (2026-08-27)

Follow-ups 1, 2, 4, 5, 7 and 8 above are done, in a cleanup pass that changed no
product behaviour:

1. Deleted. The reachability argument was re-derived before touching anything:
   `connect(managerConnectTimeout)` warns, so a missing token file `die()`s at
   `cmd/root.go:371` and a `jobqueue.Connect` error at `:382`, and `Connect` has
   no `(nil, nil)` return path. The branch now reads exactly as it did before the
   E-series, with a comment where the call was saying why it must not come back.
2. Route 1 taken, and the check is now the gate's last, after every metric
   threshold. Its verdict comes from the ORDER the two lines sit in the manager's
   own log rather than from the poll loop's clock: the loop breaks on the finish
   line, so post-fix it usually never reads the `started on` line written 0-2 ms
   later - the clean run below printed no gap at all - and a wall-clock condition
   would have been a coin flip. A/B at 20,000 x 1,000 x 3,000: clean tree
   `PASS: peakRssMb=170, recovery in 107ms ... added in 486ms`; with `logStarted`
   moved above `<-server.Serving()` and nothing else changed, the same fixture
   gives `FAIL (WRONG ORDER): ... started on ... at line 5 ... before 'prior state
   recovered' at line 9` with peakRssMb 172, recoverySec 0.107, addSec 0.449 and
   `statusInWindow=starting` all still inside their thresholds - so it fails on
   ordering alone, and only after everything else has been compared.
   Route 2 (a durable `cmd`-level test) was rejected: the ordering is only
   observable in a manager process's own log, `cmd` has no test that builds or
   runs the wr binary, and adding one puts a binary build plus a real manager
   start into `make test`. A helper-driven test would have had exactly the
   weakness follow-up 1 exists because of.
4. `dep_granularity_sweep` reaps a leftover `-f` manager by cmdline before the
   `rm -rf`, and an `INT`/`TERM` trap covers the interrupt case while the gate's
   own manager is up. `safe_kill` still refuses any pid that is not our isolated
   binary, so a real production manager can never be a candidate. Proven by
   starting a manager exactly as the gate does: `pid file: <none>` (which is what
   defeats `safe_kill "$(mgr_pid ...)"`), `pgrep finds: 70097`,
   `killed our manager pid 70097`.
5. `mkfifo`/`exec 8<>` failure is now a `FAIL (NOT MEASURED)` return rather than
   a silent fall-through into a `waitsec`-long spin. It returns rather than
   `die()`s so that the manager started a few lines above is torn down instead of
   leaked.
7. Comment added, including why the protection is implicit (`FailureHalts` stops
   the generator before the `Printf`).
8. Recorded in `scale-gate.md`'s caveats.

3 was left alone deliberately: `dep_granularity_run`'s 16 positional parameters
are a miscount hazard, but they are currently correct and are validated only by a
gate run that takes minutes, so rewriting them to globals risks a validated gate
for no behaviour gain. 6 and 9 are threshold and fixture-shape judgements, not
defects.
