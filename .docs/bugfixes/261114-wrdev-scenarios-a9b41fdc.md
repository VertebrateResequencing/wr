# Bugfix: wrdev.sh scenarios failing on the log store (phase 1 gate 1g.6)

- Branch: customdb-f397b547
- Base: customdb-f397b547 at 8b7ccade
- Queue owner: customdb-f397b547, .docs/delivery-queue.md row 20
  (phase 1 gate item 1g.6, .docs/customdb/phases/phase1-gate.md)
- Evidence: .docs/customdb/results/phase1-gate-1g6.md

## Items

- [x] `wrdev.sh control-rpc-history` fails at its manager start: the
  isolated prod-mode manager will not open the seeded history database.
  - Source: phase 1 gate item 1g.6 sweep, 2026-11-14
  - Red command (from the repo root, with an isolated `HOME`, `WRDEV_ROOT`
    and ports):

    ```bash
    R=<root>; mkdir -p $R/.wr-prod_production
    WRDEV_ROOT=$R WR_HS_DB=$R/.wr-prod_production/db WR_HS_ARCHIVED=200000 \
      WR_HS_GROUPS=20 WR_HS_RG_PREFIX=hsrg go test -tags reliability_repro \
      ./jobqueue/ -run TestReliable4SeedArchivedHistory -count=1 -v
    WRDEV_ROOT=$R PROD_PORT=<p> PROD_WEB=<p+1> developers/wrdev.sh prod-start local
    ```

    Exit 1 (`prod-start: wr manager start failed (exit 1)`); the manager
    log:

    ```text
    lvl=eror msg="wr manager failed to start : bbolt database .../db, left as
    it was: the bbolt database holds a record that cannot be converted:
    jobscomplete key: malformed log store record: key \"hsrg0-0\" is not 32
    hex digits"
    ```

  - Root cause: a fixture defect exposed by the log store, not a store
    defect. `seedHistoryRepGroup` (`jobqueue/reliable4_history_seed_test.go`)
    keyed every seeded complete record `<repGroup>-<i>`, which no real job
    key looks like. bbolt never checked, so the scenario passed on
    d1a4b6d7 (the 6fcbf4ed release sweep, rc=0). The log store converts a
    bbolt file at open (spec E1) and refuses a `jobscomplete` key that is
    not a job key (`copyComplete`, `store_convert.go`; covered by
    `store_convert_test.go`'s "a complete record whose key is not a job
    key"), which is the intended behaviour.
  - Fix: the seed keys each record with `byteKey` of the old name, a
    32-hex-digit job key, so the conversion accepts it. One line in a
    `reliability_repro` test fixture; no product code changed.
  - Pre-existing check: d1a4b6d7's own wrdev.sh and seed pass
    control-rpc-history (`results/phase1-gate-1g6/ab-d1a4b6d7-control-rpc-history.out`),
    so the failure is new with the log store, as expected from the cause.
  - Green: the same commands convert 200,000 jobs in 1m14.8s and the
    manager starts; the sweep's rerun of control-rpc-history passes
    (limit 28ms, suspend 194ms, resume -z 207ms, status -z --limit 1
    1294ms). On the final code (fix round 2's rev2fix run) it passed
    again: limit 40ms, suspend 216ms, resume -z 208ms, status -z
    --limit 1 1161ms.
  - Files: `jobqueue/reliable4_history_seed_test.go`.

- [x] `wrdev.sh unsuspend-burst` aborts on a copied bbolt fixture: its
  manager is still converting the file to the log store when the 90s
  start timeout ends, so the add finds no manager.
  - Source: phase 1 gate item 1g.6 sweep, 2026-11-14
  - Red command (sweep s1, before the fix):
    `WRDEV_PRISTINE_DB=<compact-fixture copy of pristine10> developers/wrdev.sh unsuspend-burst`,
    exit 1:

    ```text
    could not read token file; has the manager been started? [open
    .../.wr-prod_production/client.token: no such file or directory]
    unsuspend-burst aborted - could not add jobs (wr add exit 1; manager up?)
    ```

    The manager log shows the conversion still at "counting the jobs to
    convert" when the mode gave up.
  - Root cause: tooling, not product. The log store converts a bbolt
    `managerdbfile` at its first start (spec E1); pristine10 (3,000,000
    jobs, 4.3GB) took 7m03s on NFS, 5m47s of it counting. unsuspend-burst
    and report-storm-lsf started their manager with `timeout 90`, and
    backup-stall-check through `prod-start`'s `timeout 90`.
    backup-stall-check and report-storm-lsf passed in the same sweep only
    because they ran after this fix. Review round 1 found two more starts
    on a bbolt file that converts: control-rpc-history's
    `cmd_prod_start local` (90s default, converting a 773MB seed of
    200,000 jobs that took 1m14.8s) and add-storm-lsf's
    `WRDEV_ASL_START_TIMEOUT` (300s), whose fixture is a bbolt file when
    an older wr built it.
  - Fix (`developers/wrdev.sh`): those five starts wait up to
    `WRDEV_BIGDB_START_TIMEOUT` (default 3600s) for their manager
    (add-storm-lsf through its `WRDEV_ASL_START_TIMEOUT` default), and
    the three fixture modes accept a `WRDEV_PRISTINE_DB` that is a log
    store directory (copied whole, item 4), so a converted fixture can be
    reused without paying for the conversion each run. `prod-start`
    keeps its 90s default.
  - Green: unsuspend-burst passes on the bbolt fixture (sweep s2,
    conversion included) and on a converted directory fixture. In sweep
    s3, control-rpc-history and add-storm-lsf on a 120,000-job bbolt
    fixture pass with their conversions inside the start (results doc).
    On the final code (rev2fix), unsuspend-burst on the pristine10 bbolt
    fixture passed in 575s with its conversion, and add-storm-lsf on a
    directory fixture passed.

- [x] `wrdev.sh add-storm-fixture` writes a log store directory, which
  `add-storm-lsf` (and `developers/soak/sweep.sh`) cannot use: the sweep
  recorded `add-storm-lsf SKIPPED-NOFIXTURE` and `wrdev.sh add-storm-lsf`
  dies at its `[ -f "$db" ]` check.
  - Source: phase 1 gate item 1g.6 sweep, 2026-11-14
  - Red command (sweep s1, before the fix):
    `WRDEV_PRISTINE_DB=<pristine6 copy> WRDEV_ASL_FIXTURE=<out> developers/wrdev.sh add-storm-fixture`
    then `WRDEV_PRISTINE_DB=<out> developers/wrdev.sh add-storm-lsf`.
    What s1 showed: the fixture's manifest recorded `size 146` (the
    directory's own size; `s1/32-add-storm-fixture.out`), and the sweep
    recorded `add-storm-lsf SKIPPED-NOFIXTURE` without calling the mode.
    The mode's own failure was shown afterwards, by running 8b7ccade's
    wrdev.sh add-storm-lsf on a directory fixture
    (`results/phase1-gate-1g6/r1/asl-dir-red.out`), exit 1:

    ```text
    wrdev: set WRDEV_PRISTINE_DB to a fixture built by '.../wrdev-head.sh add-storm-fixture'
    ```

  - Root cause: tooling. The generator's manager converts the bbolt base
    and leaves a log store directory at `$PROD_RUN/db`, which the
    generator moves to the fixture path. The manifest's size/mtime stamp,
    add-storm-lsf's existence check and its `cp -f`, and sweep.sh's
    `[ ! -f "$fix" ]` all assumed a file. `mv -f` would also move a new
    fixture into an earlier fixture's directory.
  - Fix: `asl_db_stamp` stamps a directory by its files' summed size and
    newest mtime (a bbolt file as before); add-storm-lsf accepts a
    directory and copies it whole (item 4); add-storm-fixture refuses an
    existing fixture path, file or directory; sweep.sh tests `-e`.
  - Green: sweep s2 built a 20,000-job fixture (manifest size
    3,169,058,421) and add-storm-lsf passed on it.
  - Review round 2: with add-storm-fixture refusing an existing path, a
    second sweep in the same sweepdir failed at add-storm-fixture on
    the first sweep's fixture. sweep.sh now removes its own fixture,
    `$SW/aslfixture-sweep.db` with its `.aslmanifest` and `.jobcwd`
    (`:?`-guarded), just before that mode runs; its preflight has
    already refused while any of the root's LSF jobs might use them. A
    stub wrdev.sh that refuses an existing fixture as wrdev.sh does
    shows the second run failing with 8b7ccade's sweep.sh and passing
    with this one
    ([rev2fix/sweep-rerun.out](../customdb/results/phase1-gate-1g6/rev2fix/sweep-rerun.out)).
    Files: `developers/soak/sweep.sh`.

- [x] The fixture copies `cp -rf` (items 2 and 3) copy a `WRDEV_PRISTINE_DB`
  that is a symlink as a symlink, so the manager writes through it into
  the pristine fixture.
  - Source: phase 1 gate item 1g.6 review round 1, finding 3
  - Red command: `results/phase1-gate-1g6/r1/cp-symlink-demo.sh -rf`
    copies a symlink to a fixture directory and to a fixture file, then
    appends to the copy as a manager would, exit 0:

    ```text
    dir  fixture: run copy is a SYMLINK; pristine now: orig|manager write|
    file fixture: run copy is a SYMLINK; pristine now: orig|manager write|
    ```

  - Root cause: GNU `cp -r` does not follow a symlink named on its
    command line. The `cp -f` it replaced did, so the 1g.6 change was a
    regression for file fixtures as well.
  - Fix: the four fixture copies (backup-stall-check, add-storm-lsf,
    report-storm-lsf, unsuspend-burst) use `cp -RLf`, which dereferences.
    `asl_db_stamp` follows a symlink too (`find -L`, `stat -L`), so
    add-storm-lsf stamps what it copies; on a real path the stamp is
    unchanged (the fix120k fixture's matches its manifest; a symlink to
    it and to the pristine10 store stamp the same as their targets).
  - Green: `cp-symlink-demo.sh -RLf` gives a real copy and leaves both
    fixtures `orig|` (`r1/cp-symlink-demo.out`). In r1, unsuspend-burst
    on a symlink to the converted pristine10 store directory passed, and
    the store's files (names, sizes, mtimes) hashed the same before and
    after (`r1/01-unsuspend-burst-symlink.out`).
  - Files: `developers/wrdev.sh`.

- [x] The backup cleanups `rm -rf "${WRDEV_PROD_BKFILE:?}"*` (report-storm-lsf,
  prodsim) delete every path that starts with the backup's name, such as
  another run's `db_bk_<token>2`.
  - Source: phase 1 gate item 1g.6 review round 1, finding 9
  - Red command: `results/phase1-gate-1g6/r1/bkfile-rm-demo.sh`, which
    runs the old and the fixed line on a directory holding this run's
    backup `db_bk_tokA`, its `db_bk_tokA.tmp`, and siblings
    `db_bk_tokA2`, `db_bk_tokAB` and `db_bk_tokA_old`:

    ```text
    before:            db_bk_tokA db_bk_tokA2 db_bk_tokAB db_bk_tokA_old db_bk_tokA.tmp
    after old line:
    after fixed line:  db_bk_tokA2 db_bk_tokAB db_bk_tokA_old
    ```

  - Root cause: a prefix glob where the db lines use the name and
    `name.*`.
  - Fix: `rm -rf "${WRDEV_PROD_BKFILE:?}" "${WRDEV_PROD_BKFILE:?}".*`,
    matching the db lines. Review minors in the same file: the
    `$PROD_RUN/db` cleanups in `asl_cleanup` and `prodsim_cleanup` and
    the `$pr/log` removals are `:?`-guarded, and add-storm-fixture
    refuses any existing fixture path (`-e`), not only a directory.
  - Green: the demo's fixed line (above). A 6-minute prodsim in r1 with
    `WRDEV_PROD_BKFILE=<dir>/db_bk_r1x` and siblings `db_bk_r1x2`,
    `db_bk_r1xB` and `db_bk_r1x_old` planted beside it ran its cleanup
    and left all three (`r1/05-prodsim-bk.out`). That run's manager log
    shows no backup, so it shows the siblings survive, not that a backup
    was removed; the demo shows that.
  - Files: `developers/wrdev.sh`.

- [x] `wrdev.sh unsuspend-burst` cannot fail on the log store: its only
  verdict is `peakbw > 5000`, a count of goroutines blocked in
  `bbolt.(*DB).Batch`, which the log store never has.
  - Source: phase 1 gate item 1g.6 review round 1, finding 2
  - Red command: any unsuspend-burst run on the log store reports
    `peak bw(Batch-blocked)=0` by construction; s2 and the store
    directory run printed `NO WRITE-STORM ... (peak bw=0)` and exited 0
    whatever their status RPCs did
    (`results/phase1-gate-1g6/s2/35-unsuspend-burst.out`).
  - Root cause: the gate measured the bbolt mechanism of prod's freeze,
    not its effect.
  - Fix: a second, store-neutral verdict. Every status RPC sampled after
    the burst must take less than `WRDEV_UB_MAX_STATUS_MS` (60000ms, the
    client's `ClientMinRequestTimeout` floor: a call that reaches it is
    one the client gives up on, which is how prod's freeze became churn),
    every status call must read counts (a fast refusal must not look
    responsive), and all N jobs must be ready after the burst. Any miss
    exits 1.
  - Green: sweep s3's unsuspend-burst passes the new gate (max status
    RPC 170ms, 0 failed calls, 100000/100000 ready;
    `s3/35-unsuspend-burst.out`), as does r1's on a store directory
    (644ms). With `WRDEV_UB_MAX_STATUS_MS=50` it exits 1 (`a status RPC
    took 877ms (bound 50ms)`, `r1/02-unsuspend-burst-lowbound.out`), so
    the gate can fail. On the final code (rev2fix): max status RPC
    191ms, 0 failed calls, 100000/100000 ready
    ([rev2fix/06-unsuspend-burst.out](../customdb/results/phase1-gate-1g6/rev2fix/06-unsuspend-burst.out)).
  - Files: `developers/wrdev.sh`.

- [x] `wrdev.sh crash-recovery` can pass without testing what it claims:
  it kills the manager once the manager reports the job running, whether
  or not its command has started under a live runner.
  - Source: phase 1 gate item 1g.6 review round 1, finding 5
  - Red command: `developers/wrdev.sh crash-recovery` in sweep s1 and s2
    exited 0 having killed the manager with `marker=0`, s1 also with no
    runner jobid (`results/phase1-gate-1g6/s1/44-crash-recovery.out`,
    `s2/44-crash-recovery.out`):

    ```text
    .../cr_count: No such file or directory
    job running; marker=0 my wrp_ jobid=
    --- killing prod manager mid-run (LSF runner  survives), then restarting (DB preserved) ---
    ...
    PASS: re-sent archive accepted (complete=1), command ran exactly once
    ```

  - Root cause: the kill waited for the manager's `running` count, which
    a job reaches when it is reserved, before its command runs, and took
    a missing jobid as no obstacle. A kill then tests a reserve, not the
    re-sent archive of a running command; the final check (complete=1,
    marker=1) passes either way.
  - Fix: the mode waits up to `WRDEV_CR_START_WAIT` (150s) until the
    manager reports the command's pid (set by the runner's Started call
    as the command starts, shown as `Host: <h> ...; Pid: <n>` by
    `wr status -i rgCR`) and the prefix's LSF job on that host is in
    RUN, and only then kills; otherwise it exits 1 as
    `FAIL (NOT MEASURED)`, stopping its manager and runner untested. The
    command sleeps 60s, not 30s, so the kill lands inside it. A final
    marker of 1 proves no new runner ran the command again. The marker
    reads no longer print an error when the file is absent.
  - The first version of this fix waited for the marker file instead of
    the manager's pid, then for a RUN jobid. In sweep s3 it exited 1 as
    not measured (`s3/44-crash-recovery.out`: `marker=1`, no jobid): the
    runner, LSF job 234819, ran from 06:53 to 06:54 and was done before
    the lookup saw it in RUN. Why the marker came too late was not
    pinned. Its r1 reruns passed but saw the marker 62-122s after the
    add (`r1/04-crash-recovery-*.out`), and the commands start about
    64s after it (`r1/07-*`), so a 30s sleep left little or no window.
    The marker, written on an exec node over NFS, is not a reliable
    start signal here; the manager's pid is.
  - Green: four r1 runs (`r1/07-crash-recovery-{1..4}.out`) each saw
    the command start 63-66s after the add, killed with `marker=1` and
    a RUN jobid on that host, and passed;
    `WRDEV_CR_START_WAIT=1` fails as not measured
    (`r1/06-crash-recovery-nowait.out`), though not for the reason its
    message gave: the manager had reported the pid at t+2s, and the one
    `bjobs` lookup then did not show the runner in RUN (results doc).
  - Review round 2, three more defects in the same wait:
    - `wr status` prints `Host: ...; Pid: n` for complete and lost jobs
      too (`cmd/status.go`), so a job that had already finished could
      pass the wait. The mode now takes the host only from an output
      that also shows `Status: running`.
    - One FAIL message covered two causes. It now says either that the
      manager did not report the job running with a pid, or that it did
      on host h but `bjobs` showed no job of the prefix in RUN there.
    - The PASS and restart-failure paths bkilled only `$jid`, so a
      runner the restarted manager submitted outlived the mode. All
      three exits now bkill, by exact jobid, every LSF job of the
      isolated manager's prefix (`cr_bkill_ours`).
  - Green (fix round 2, final code): two runs passed
    ([rev2fix/04-crash-recovery-1.out](../customdb/results/phase1-gate-1g6/rev2fix/04-crash-recovery-1.out),
    [-2](../customdb/results/phase1-gate-1g6/rev2fix/04-crash-recovery-2.out)).
    In the first LSF dispatched the runner within seconds: the manager
    reported the command running at t+4s and the kill came with
    `marker=0`, the marker not yet seen over NFS; the final marker of 1
    shows the command ran once. The second killed at t+68s with
    `marker=1`. With `WRDEV_CR_START_WAIT=1` the mode exits 1 with the
    no-pid message
    ([rev2fix/05-crash-recovery-nowait.out](../customdb/results/phase1-gate-1g6/rev2fix/05-crash-recovery-nowait.out)).
    The pid-but-no-RUN message was not triggered by a run.
  - Review round 3, two more:
    - Each exit ran `cr_bkill_ours` before stopping the manager, which
      could submit another runner after the `bjobs` listing. All three
      exits now stop the manager first, as other cleanups do.
    - The RUN lookup matched the host as a substring of `exec_host`
      (`index($4,h)`), so `node-5-1` matched a runner on `node-5-12`.
      An awk function now splits `exec_host` on `:`, strips each
      `N*` slot count and any domain, and compares the host exactly.
      The old and new matches on sample rows:
      [rev3fix/hostmatch.txt](../customdb/results/phase1-gate-1g6/rev3fix/hostmatch.txt).
  - Green (fix round 3, final code, wrdev.sh md5 `0a74c63b35e7`, binary
    md5 `4ea1f24558f6`, in a fresh root): two runs passed, killing at
    t+4s with `marker=0` and at t+66s with `marker=1`, each with a RUN
    jobid on the reported host, and ending with complete=1 and marker 1
    ([rev3fix/01-crash-recovery-1.out](../customdb/results/phase1-gate-1g6/rev3fix/01-crash-recovery-1.out),
    [-2](../customdb/results/phase1-gate-1g6/rev3fix/01-crash-recovery-2.out)).
    With `WRDEV_CR_START_WAIT=1` it exits 1 with the no-pid message
    ([rev3fix/02-crash-recovery-nowait.out](../customdb/results/phase1-gate-1g6/rev3fix/02-crash-recovery-nowait.out)).
    No job of the prefix was left in LSF and no manager running.
  - Files: `developers/wrdev.sh`.

- [x] The first `wr manager start` on a large bbolt file shows no progress
  for most of its conversion: on pristine10 the log said "counting the
  jobs to convert" and nothing more for 5m47s of 7m03s, and the status
  sidecar `wr manager start` reports from did not change either. Spec E1
  (shipped in 1f) requires the conversion "with progress shown".
  - Source: phase 1 gate item 1g.6 sweep (incidental), routed as a bug
    by review round 1, finding 8
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue/ -run TestStoreConvertReportsCountingProgress`,
    which converts 20,001 jobs with the log interval at 0 and reads the
    manager log at the first job converted, exit 1:

    ```text
    Line 2123:
    Expected '0' to be greater than or equal to '2' (but it wasn't)!
    --- FAIL: TestStoreConvertReportsCountingProgress (1.81s)
    ```

  - Root cause: `countJobs` (`jobqueue/store_convert.go`) walked
    `jobslive` and `jobscomplete` without calling the upgrade reporter,
    and on a cold multi-GB file that walk is most of the conversion.
  - Fix: counting is a `storeBuild` method whose `jobCounted`, after
    each counted job, checks `progressDue` and, when it is due, calls
    `writeProgress` with detail "counting the jobs to convert (N
    counted)", as converting's `jobDone` does. So the sidecar moves
    every 10,000 jobs or 2s and the default log gets a "database
    upgrade step still running" line every 30s. `setPhaseTotal` restarts
    Processed at 0 once the total is known, so the sidecar's Processed
    still rises from 0 to Total while converting (E1 test 5).
  - Green: the test passes, as do the other conversion and upgrade-log
    tests.
  - Review round 2 found the tests too weak, and fix round 2 changed
    them:
    - `TestStoreLongConvertReportsProgress` passed on any "still
      running" line of the convert state, and counting's lines are such
      lines now, so a conversion that logged no progress while
      converting (mutant M8) passed. It now asserts only on lines whose
      detail is `converting database (...)`.
    - `TestStoreConvertReportsCountingProgress` did not check the count
      in the detail, which `wr manager start` prints (mutant M3). It now
      asserts the last counting line's detail is `counting the jobs to
      convert (20000 counted)`. Its fixture is 20,000 jobs, a multiple
      of the progress step, plus ten orphaned run-state records, and it
      asserts each counting line's `processed` is new and rising, so
      reporting progress again while walking run state (mutant M7)
      fails.
    - `countJobs` checks `progressDue` before it formats the detail, as
      `jobDone` does (`storeBuild.jobCounted`).
  - Mutants: the review's eight, re-anchored on `jobCounted`, each run
    against the `Convert|Upgrade` tests in a scratch copy
    ([rev2fix/mutants.py](../customdb/results/phase1-gate-1g6/rev2fix/mutants.py),
    [rev2fix/mutants.out](../customdb/results/phase1-gate-1g6/rev2fix/mutants.out)).
    The unmutated copy passes, and all eight are KILLED: M1 (no
    counting progress), M3 (detail without the count), M4 (sidecar but
    no log), M5 (half the count) and M7 (progress during the run-state
    walk) by `TestStoreConvertReportsCountingProgress`; M2 and M6 (the
    reset of Processed missing or after the write) by
    `TestStoreConvertProgress`; M8 (no log while converting) by
    `TestStoreLongConvertReportsProgress`. Round 2 had M3 and M7
    SURVIVED, and M8 surviving the weakened test.
  - Real binary: the first start of the final binary on a copy of the
    compacted pristine10 bbolt fixture (3,000,000 jobs) logged a "still
    running" line every 30s through counting, the first `detail="counting
    the jobs to convert (200000 counted)"` at 30.1s, the last (2770000
    counted) at 5m2.4s, then converting lines, and finished in 6m43.4s.
    The sidecar, sampled every 5s, read 10000 counted at 08:12:28, 3s
    into the conversion. Redacted logs:
    [rev2fix/01-convert-manager.log](../customdb/results/phase1-gate-1g6/rev2fix/01-convert-manager.log),
    [rev2fix/01-convert-sidecar.txt](../customdb/results/phase1-gate-1g6/rev2fix/01-convert-sidecar.txt).
    The earlier "(230000 counted) at 31s" figure had no preserved log
    and is withdrawn.
  - `make test`, `make race` and `make lint` pass (results doc).
  - Review round 3 found the sidecar untested while counting: mutants N1
    (`jobCounted` calls `logProgress`, so only the log moves) and N2 (the
    sidecar rewritten with Processed 0 after each write) survived,
    though `wr manager start` prints from the sidecar. Fix round 3:
    - `countJobs` calls a new test seam, `convertAtCounted`, after each
      counted job (nil outside tests, like `convertAtJob`).
    - `TestStoreConvertSidecarShowsCounting` converts 10,000 jobs and
      reads the sidecar from that hook. After the second job counted,
      with the hook having slept `dbUpgradeProgressInterval` after the
      first, it must read State `convert database`, Processed 2 and
      Detail `counting the jobs to convert (2 counted)`, so progress
      paced by time alone is tested (mutant N8,
      `n%dbUpgradeProgressEntries == 0` in place of `progressDue(n)`). At
      the 10,000th it must read Processed 10000, Total 0 and that count
      in the detail; at the first job converted, a `converting
      database` detail and Total 10000.
    - The counting log test's strictly-increasing check is one
      `ShouldResemble` against the sorted, compacted copy, not
      `So(slices.IsSorted(...), ShouldBeTrue)`.
  - Mutants on the final code: round 2's M1-M8 and round 3's N1-N10,
    re-anchored where `countJobs` changed, each run against the
    `Convert|Upgrade` tests in a scratch copy
    ([rev3fix/mutants.py](../customdb/results/phase1-gate-1g6/rev3fix/mutants.py),
    [rev3fix/mutants.out](../customdb/results/phase1-gate-1g6/rev3fix/mutants.out)).
    The unmutated copy passes. M1-M8 are all KILLED. N1, N2, N4-N9 are
    KILLED; N1, N2 and N8 by the new test. Two survive:
    - N3 (`!counted || v == nil` to `!counted`, counting nested
      buckets): a defect no fixture reaches, as none has a nested
      bucket. The guard came with 1f.2 and only moved here. Recorded in
      `.docs/customdb/phases/incidentals.md` for 2d.1.
    - N10 (`setPhaseTotal` without `r.phaseDetail = detail`):
      equivalent. `setPhaseTotal` writes the sidecar from its `detail`
      argument, and `phaseDetail` is read only by `completePhase` when
      passed an empty detail and by `finish(nil)` while a phase is
      active; conversion passes `completePhase` its detail and ends the
      phase before `finish`. A probe that panics on either read after
      `setPhaseTotal` passes the `Convert|Upgrade` tests
      ([rev3fix/n10-probe.txt](../customdb/results/phase1-gate-1g6/rev3fix/n10-probe.txt)).
      The new test's `converting database` assertion stays, as it pins
      what the sidecar shows once the total is known.
  - Fix round 3 gates: `make lint` 0 issues, `make test` PASSED,
    `go vet -tags reliability_repro ./jobqueue/` OK, `make race` PASSED on
    a rerun after one unreproduced `TestStoreOpenLocked` failure in a
    file this item does not touch (results doc, incidentals.md).
  - Files: `jobqueue/store_convert.go`, `jobqueue/db.go`,
    `jobqueue/store_convert_test.go`.
  - Review round 4 found mutant R1 (`if b.upgrade.progressDue(n) {` in
    `jobCounted` changed to `if b.upgrade.active() {`) surviving: it
    rewrites the sidecar for every job counted, 3,000,000 file publishes
    over NFS for pristine10. Red: R1 and R7 (the start phase's detail
    changed) SURVIVED the round 3 tests
    ([rev4fix/mutants-r1-red.out](../customdb/results/phase1-gate-1g6/rev4fix/mutants-r1-red.out)).
    Fix round 4:
    - `TestStoreConvertSidecarShowsCounting` reads the sidecar at the
      first job counted, before its sleep, and requires the start
      phase still: Detail `counting the jobs to convert`, Processed 0.
    - `TestStoreConvertShowsStartBeforeCounting` checks that Detail
      too (the R7 incidental).
  - Mutants on the final code: M1-M8, N1-N10 and round 4's R1-R11 (R2
    the hook before the report, R3 the hook's error swallowed, R4
    Processed set to the total, R5 `progressDue(n+1)`, R6 no step
    writes, R7 the start detail changed, R8 no `setPhaseTotal`, R9 the
    hook called during the run-state walk, R10 a stale detail in
    `setPhaseTotal`, R11 `jobDone` always due), each against the
    `Convert|Upgrade` tests in a scratch copy
    ([rev4fix/mutants.py](../customdb/results/phase1-gate-1g6/rev4fix/mutants.py),
    [rev4fix/mutants.out](../customdb/results/phase1-gate-1g6/rev4fix/mutants.out)).
    The unmutated copy passes. 26 of 29 valid mutants are KILLED,
    R1 by `TestStoreConvertSidecarShowsCounting`. R6 was INVALID in the
    full run (it used `time`, which `store_convert.go` does not
    import) and is KILLED rerun with `b.upgrade.now()`. Three survive:
    - N3: the defect no fixture reaches, in incidentals.md for 2d.1.
    - N10: equivalent, as in round 3.
    - R3 (`return callConvertHook(convertAtCounted)` to `_ =
      callConvertHook(...)` then `return nil`): equivalent.
      `callConvertHook` returns nil while `logConvertHook` is nil, and
      only test files store a hook, so no product build can differ. No
      test stops a conversion at `convertAtCounted`; a stop while
      counting is the same read transaction as the tested stop at
      `convertAtCounting`.
  - Fix round 4 gates: see the results doc's
    [Fix round 4](../customdb/results/phase1-gate-1g6.md#fix-round-4).
  - Files (round 4): `jobqueue/store_convert_test.go`.

- [x] `TestStoreOpenLocked` is a reproducible test race, not unattributed
  (`jobqueue/store_lock_test.go` ~131-141, the "with no backup" case).
  The holder's log store folder runs its first pass `foldInterval` (1s,
  `store_fold.go`:65) after open; the open has already started a new
  wal, so that pass commits to `history.bolt`. The test's before/after
  snapshots around the bounded open of up to 500ms can straddle the 1s
  mark under `make race` load.
  - Source: phase 1 gate item 1g.6 review round 4, finding 2 (the
    unreproduced fix round 3 `make race` failure, then an incidental
    proposed for 2b)
  - Red command: 8b7ccade's `jobqueue/store_lock_test.go` with
    `time.Sleep(700 * time.Millisecond)` added after the "with no backup"
    case's first snapshot, then
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue/ -run TestStoreOpenLocked`,
    exit 1, the shape of the round 3 failure (only the holder's
    `history.bolt` differs, its mtime 1.008s after the open's new wal
    file):

    ```text
    Line 141:
    Expected: ... "queue.db/history.bolt":"... size=16805888 mod=1794654733183252402 sha256=be9f30c6..."
    Actual:   ... "queue.db/history.bolt":"... size=16805888 mod=1794654734191267006 sha256=85281b24..."
    --- FAIL: TestStoreOpenLocked (2.17s)
    ```

    The same sleep after the first case's snapshot fails it at line
    127: it has the same race
    ([rev4fix/openlocked-red-green.txt](../customdb/results/phase1-gate-1g6/rev4fix/openlocked-red-green.txt)).
  - Root cause: the test, not the store. The snapshot the bounded open
    is compared against was taken while the holder still had a write
    due of its own.
  - Fix: both cases take their first snapshot through a new
    `settledStoreDirIdentity`, which first flushes the holder through
    the store inspector (pending writes done and, for the log store,
    `foldNow` to the writer's durable end). After that the holder has
    nothing to fold, so its folder commits nothing more, and any
    difference in the after snapshot is the second open's. The
    comparison still covers every file, the holder's included, so the
    test's claim, that the losing open writes nothing, is as strong as
    before.
  - Green: with the 700ms sleep injected the test passes; with a 3s
    sleep after both cases' snapshots it passes; with 700ms after both,
    10 runs under `-race` pass; with no injection, 50 runs under
    `-race` pass (`-count 50`, 88s).
  - Files: `jobqueue/store_lock_test.go`.
