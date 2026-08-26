# The dep-granularity scale gate: recorded numbers and thresholds

Spec F3. `developers/wrdev.sh dep-granularity-check [waiters] [members] [groups]`
points an isolated production-mode manager at a synthetic database of the shape
production had, and reports what recovering it costs: peak RSS, the recovery
window, what `wr manager status` says inside that window, and how long adding one
more member to the big dep group takes.

This file records the A/B the gate was validated against, and where its three
thresholds come from, so that a later run can tell a real regression from a
re-tuned threshold.

## What the fixture is

`TestDepGranularityFixture` (`jobqueue/depgranularity_scale_test.go`, build tag
`reliability_repro`) writes, through `db.storeNewJobs`:

- `members` live jobs in one dep group,
- one live job in each of the other `groups - 1` dep groups,
- `waiters` live jobs depending on the big group and belonging to no group.

Every one of them also depends on a dep group no job is ever added *with*, so the
whole database is permanently `dependent`: the manager under test schedules
nothing, launches no runner and submits no LSF job, which is what makes the gate
safe to run on a shared host.

Two details are the difference between this gate and a false PASS:

- **It goes through `storeNewJobs`**, so `bucketRDTK` and `bucketDepGroups` hold
  what the real add path puts there. A fixture that wrote `bucketJobsLive`
  directly would make the pre-fix binary resolve no member keys at all.
- **It writes `bucketDTK` itself.** After spec A3 `storeNewJobs` writes no
  `bucketDTK` entry, and the pre-fix binary resolves a dep-group dependency by
  cursoring exactly that bucket. Left to `storeNewJobs`, the bucket would be
  empty, every group would look satisfied to the pre-fix binary, nothing
  quadratic would allocate, and the pre-fix run would PASS. Those entries are
  pre-upgrade data by definition: every live job in production was added by a
  pre-fix binary.

That second point was checked rather than assumed - see "Proving the fixture is
the discriminator" below.

## How the pre-fix side was run

Neither the gate mode nor the fixture generator exists at the pre-fix commit, so:

- the pre-fix tree is a pristine `git worktree` at **`8b9ba00`**, this delivery's
  branch point;
- `developers/wrdev.sh` is copied into it, because pristine means the product
  code under test, not the harness, and `wrdev.sh` resolves `REPO` from its own
  path - so the worktree's copy builds and measures the pre-fix binary;
- the fixture is built once in the fixed tree and handed to the pre-fix run
  through `WRDEV_DEPGRAN_DB`, which skips generation and measures the supplied
  file. Both runs then measure the same bytes.

Everything ran under `WRDEV_ROOT` on local disk, against the isolated
production-mode manager on port 51782, never
`/nfs/hgi/wr/lsf/.wr_production/`.

## The numbers

Host: farm22-wrstat01, 8 cores, 91 GB RAM, load average 118-119. Measured
2026-08-26. Post-fix is `f2ccaee`; pre-fix is `8b9ba00`.

### Default shape: 30,000 waiters, 3,000 members, 6,300 groups

39,299 live jobs, a 109 MB database, and 90,000,000 (waiter, live member) pairs
for the pre-fix binary to expand.

| metric | pre-fix `8b9ba00` | post-fix `f2ccaee` | ratio |
| --- | --- | --- | --- |
| `peakRssMb` | **>= 12,020** | **287** | **>= 41x** |
| `recoverySec` | never finished | **0.170** | - |
| `statusInWindow` | `up` | `starting` | - |
| `addSec` | never reached | **0.419** | - |
| verdict | **FAIL**, exit 1 | **PASS**, exit 0 | |

The pre-fix run is the one the OOM kills were: it logged `recovering prior state`
and then only the heartbeat's `still recovering prior state` for as long as it was
allowed to live, growing steadily through 763 MB, 2,069 MB, 4,502 MB, 7,368 MB and
9,896 MB until `WRDEV_DEPGRAN_ABORT_RSS_MB` (set to 12,000 for these runs) killed
it. **12,020 MB is a floor, not the peak**: the real one is higher, and on a
182.7 GB production node it was ~180 GB five times over. Because recovery never
finished, `recoverySec` and `addSec` are `-` for that run and the gate reports the
plain FAIL branch rather than "not measured".

The post-fix manager's own phase lines for the same database:

| phase | elapsed |
| --- | --- |
| `recovering: opened database` | 218ms |
| `recovering: decoded live jobs` (39,299) | 262ms |
| `recovering: built dependency-group state` (9,299 memberships) | 21ms |
| `recovering: resolved prior job dependencies` | 58ms |
| `recovering: enqueued prior jobs` | 98ms |

`resolve + enqueue` is 156ms and the gate measured the window between the two
delimiter lines as 170ms, so the harness costs about 14ms of poll latency.

### Reduced shape: 30,000 waiters, 300 members, 6,300 groups

The default shape leaves two of the three metrics with no pre-fix number at all,
because the pre-fix run never finishes recovering. This shape keeps the live-job
count (36,599) and so the decode cost, and shrinks only the expansion factor, by
ten: 9,000,000 pairs rather than 90,000,000. The pre-fix run completes, so every
metric has both sides.

| metric | pre-fix `8b9ba00` | post-fix `f2ccaee` | ratio |
| --- | --- | --- | --- |
| `peakRssMb` | 3,789 | 274 | **13.8x** |
| `recoverySec` | 16.099 | 0.171 | **94x** |
| `statusInWindow` | `up` | `starting` | - |
| `addSec` | 18.177 | 0.451 | **40x** |
| verdict | **FAIL**, thresholds | **PASS** | |

All three metrics discriminate by more than 4x, so all three are gated on.

### What the pre-fix run says about publication

The gate also prints when `started on` was logged relative to
`recovering: prior state recovered`, which is spec E2 acceptance test 4 observed
end to end rather than asserted in a test:

- post-fix: **0 to 2ms after** the finish line, at every shape measured;
- pre-fix, reduced shape: **16,099ms before** it - the manager bound its listener,
  wrote its token and served clients for the whole 16-second recovery.

That is the only automated exercise of E2 acceptance test 4 there is; `startJQ`
`die()`s and daemonizes, so it is not reachable in-process.

The gate now **fails** on the wrong order, but the check is deliberately the
**last** one, after every metric threshold. There it cannot mask the memory gate:
a pre-fix binary, or a post-fix one measured against a blunt fixture, fails on
memory or recovery time first and never reaches it. Placed any earlier it would
fail the pre-fix arm of an A/B on ordering before the two sides' memory figures
had been compared at all, voiding the comparison the gate exists for.

The verdict is taken from the order the two lines sit in the manager's own log,
not from the wall-clock gap printed above it. The gap is evidence; the order is
the assertion. The poll loop breaks on the finish line, so it can miss a
`started on` written a millisecond later - post-fix that gap is 0 to 2 ms - while
both lines come from the same logger and so reach the file in the order they were
emitted, whatever the loop saw.

## The thresholds

Each is an env-overridable default in `developers/wrdev.sh`, set to **2x the
post-fix figure at the default shape**, rounded up to a round number, and then
checked to be **at most half the pre-fix figure**. A threshold the pre-fix run
would pass is not a gate.

| knob | default | 2x post-fix | half the pre-fix figure |
| --- | --- | --- | --- |
| `WRDEV_DEPGRAN_MAX_RSS_MB` | **700** | 622 (2 x 311) | >= 6,010 (default shape), 1,894 (reduced) |
| `WRDEV_DEPGRAN_MAX_RECOVERY_SEC` | **1** | 0.34 (2 x 0.170) | 8.0 (reduced shape) |
| `WRDEV_DEPGRAN_MAX_ADD_SEC` | **2** | 0.84 (2 x 0.419) | 9.1 (reduced shape) |

The recovery and add columns take their pre-fix figure from the reduced shape,
since the default-shape pre-fix run never produces one. Both are still an order of
magnitude clear of the threshold.

The memory column doubles the **largest** post-fix figure rather than the one in
the table above, because peak RSS is the metric that moves between runs: four
post-fix runs at the default shape measured 274, 278, 287 and 311 MB, most of it
the mmap residency of a 109 MB database. The other two barely move (recoverySec
0.170-0.222, addSec 0.406-0.519).

`WRDEV_DEPGRAN_ABORT_RSS_MB` (16,000 by default, 12,000 for these runs) is not a
threshold: it kills the manager when its peak RSS passes that, so a pre-fix run on
a shared host reports its FAIL rather than taking the host down. A run it fires on
is a FAIL either way, because recovery never reached the finish line.

## Proving the fixture is the discriminator

The `bucketDTK` warning above was checked by mutation, not taken on trust. With
`dgfWriteLegacyLookups` removed from the generator - leaving `storeNewJobs` to
populate the bucket, which after A3 it no longer does - the fixture is otherwise
identical, and the **pre-fix binary PASSES the gate**: with no entries to cursor,
every dep group looks satisfied, no keys are expanded, and the run recovers as
fast and as cheaply as the fixed one. That is the false PASS this step exists to
stop, and it is exactly the failure mode `.docs/` already records for the
`pristine10` history fixture, whose synthetic records were invisible to the code
path being gated.

The recorded run, at the default shape against the **pre-fix** binary:

```
DEPGRAN-SUMMARY waiters=30000 members=3000 peakRssMb=333 recoverySec=0.261 \
  statusInWindow=up addSec=0.603 errors=-
PASS: peakRssMb=333, recovery in 261ms, ... one more member added in 603ms
```

333 MB and a quarter-second, against 12,020 MB and a recovery that never finished
on the same binary and the same shape with the entries present. The generator's
own assertions catch the mutation too - `dtkBigGroup` reads 0 where F3 acceptance
test 4 wants `members` - so a fixture built this way cannot quietly become the one
the gate uses.

## Caveats worth knowing before re-running

- **`peakRssMb` covers the add as well as the recovery.** `VmHWM` is a high-water
  mark, so the gate reads it on every poll and once more after the `wr add`; the
  last reading of a live process is its peak. Sampled once a second, as the
  procedure first said, a post-fix run that recovers in under a second got one
  reading, taken at t=0 of a process that had allocated nothing, and reported
  10 MB where the real peak was 212 MB.
- **`recoverySec` has the resolution of the poll**, about 20ms while recovery is
  young and 200ms once it has been going five seconds. It is measured against the
  two log lines that exist in both trees, so it cannot use the post-fix-only
  `elapsed` fields; those are quoted above as a cross-check instead.
- **`statusInWindow` needs the window to outlast one `wr manager status`**, which
  costs 23-30ms. At the default shape the sidecar exists for around 400ms, so the
  sample lands comfortably inside; at a tenth of that shape it does not, and the
  gate widens the window once by doubling the waiters, as the procedure asks.
- **The gate uses `wr manager start -f`.** That is what makes the manager's pid
  knowable from t=0 and its log capturable, but it also means no pid file, so
  `wr manager status` takes its no-pid-file branch. The pid-file branch behaves
  differently during the window - see below. No pid file also means an
  interrupted run leaves a manager that `safe_kill "$(mgr_pid ...)"` cannot find,
  so the gate sweeps for one by cmdline before it `rm -rf`s `$PROD_RUN`, and traps
  `INT`/`TERM` while its own manager is up.
- **A fixture supplied through `WRDEV_DEPGRAN_DB` is validated only for
  existence.** The gate checks the path is a file and reports its size; it does
  not open it, count its jobs or check its shape, and the `waiters`/`members`/
  `groups` arguments then only label the `DEPGRAN-SUMMARY` line. That matters
  because supplying the fixture is exactly the A/B path - it is how the pre-fix
  worktree, which has no generator, is handed the same database - so a wrong or
  stale path buys a run that measures something other than what its summary says.
  Regenerate rather than reuse if there is any doubt about which shape a kept
  fixture has. The generated path is validated properly: no `DEPGRAN-FIXTURE`
  line is a `FAIL (NOT MEASURED)`.
- These figures are this host's, at load 118. The ratios are what to compare
  against, not the absolute milliseconds.

## Expected: `wr manager status` is silent on the daemonized path in the window

`managerStatusCmd` has two branches, and the gate only exercises one of them.
With a pid file present it calls `connect(managerConnectTimeout)`, and `connect`
reads the token file first and `die()`s when it is missing. E1 moved the token
write to publication, so during the startup window there is no token file unless
a previous run left one, and `wr manager stop` deletes it on a clean stop
(`cmd/manager.go` `deleteToken`).

That branch used to end with a `reportManagerStartupStatus()` call, which could
never run: `connect` either returns a client or `die()`s, so the only way past it
was a `(nil, nil)` from `jobqueue.Connect`, which has no such return. It has been
deleted, leaving the branch exactly as it stood before the E-series, and a
comment in its place saying why it must not be replaced.

So `wr manager stop` followed by `wr manager start` on a database that takes a
while to recover leaves `wr manager status` answering, for the whole window:

```
38ms  pid=yes sidecar=no  token=no rc=1: could not read token file; has the manager been started?
...
368ms pid=yes sidecar=yes token=no rc=1: could not read token file; has the manager been started?
513ms pid=yes sidecar=yes token=no rc=1: could not read token file; has the manager been started?
844ms pid=yes sidecar=no  token=yes rc=0: started
```

Reproduced twice against `f2ccaee` on the default-shape fixture, polling
`wr manager status` from process start.

**This is expected behaviour, not a defect, by the operator's decision
(2026-08-26):** "I wouldn't call wr manager status not working during the startup
window a bug. It should only be expected to work after wr manager start says the
manager has started." It follows directly from the intent of the whole E-series -
during the window the manager is meant to look to the outside world as though no
start has been attempted, and `wr manager start` is the one client that must see
progress. It does: throughout the trace above it was printing
`recovering prior state, 0s elapsed`. Do not "fix" the pid-file branch to consult
the sidecar; that would make the manager visible during the window, which is the
behaviour this work deliberately removed.

Two consequences worth recording rather than acting on. `wr manager status`'s
useful branch is the **no-pid-file** one, which turns "stopped" into
`starting: <phase>` - that is what this gate asserts, and what covers the instant
before the pid file exists (the 3 ms sample above answered `stopped`). And the
sidecar does not exist for the first ~350 ms at all (process start, config,
`initDB`, TLS key generation), so nothing can report a phase during `initDB`
itself - the one phase whose production cost is unmeasured.

Spec E5's story text predates this decision and reads the daemonized-path
behaviour as something to remove; it also predicts the wrong message (the branch
dies about the **token**, not about being non-responsive). Read this section as
the current position.
