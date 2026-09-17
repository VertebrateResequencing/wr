# Make a job's start durable before the manager acknowledges it

Branch `fix-start-durability`. Split off `incidental-fixes-2` (PR #600), which
records rather than fixes and so was the wrong place for a change to the
manager's write path. The open counterpart on develop is BUG 20 in
`.docs/bugfixes/260829-1.md`, whose suggested "Next" step this work disproves.

**Filename suffix, not a sequence number.** Four branches were cut from this
work on the same day; `YYMMDD-N.md` would have had all four reaching for
`260917-1.md` and colliding as an add/add conflict, which
`260903-1-incidental.md` records happening three times already.

Quality gates (run with ALL `OS_*` unset, or they take ~16 minutes and run the
OpenStack tests): `make test`, `CGO_ENABLED=1 make race`, `make lint`. Note
`make lint` is GREEN on develop - `0 issues.` - provided the local `master` ref
is `b2f0ff9`. The two failures recorded against develop on `incidental-fixes-2`
(`jobqueue/behaviours.go:323` funlen and
`jobqueue/modify_validation_test.go:421` gci) were an artefact of a stale local
`master` in that clone, not of the code; see the gates note at the end.

## Acceptance criteria set by the repo owner

Ranked. A fix that satisfies the first two is acceptable even if it leaves the
third:

1. **A command must never run twice simultaneously.** This is the whole point;
   it is what `DEVELOPERS.md` rule 4's rationale exists to protect.
2. **The job must eventually run, automatically.** No manual intervention, no
   job parked for ever.
3. Latency is negotiable. A rare edge case that parks the job as running until
   its TTR expires and is then reclaimed is explicitly acceptable.

That ranking rules Option 4 (test-only) out and makes a recovery-side fix
(Option 2 below) preferable to paying a synchronous write on every job start,
provided it can tell "started but not persisted" from "never started".

## The defect

- [ ] **`TestJobqueueSignal` shard `signal_b` intermittently fails with
      `Expected: jobqueue.JobState("lost")` /
      `Actual: jobqueue.JobState("running")`
      at the `So(job2.State, ShouldEqual, JobStateLost)` line
      (`jobqueue/jobqueue_test.go:2100`), taking ~182s.** Diagnosed but NOT
      fixed - the fix is a decision about the manager's write path, not a test
      tweak. This is the same bug as **BUG 20 in `.docs/bugfixes/260829-1.md`**
      (which stays open there); this entry supersedes its "Next" step, because
      the disk-load hypothesis it suggested turned out to be only half the
      story.

      Three occurrences, all in `make race`, on three unrelated codebases, so it
      is pre-existing and belongs to no branch:

      | codebase | head | CI run | line |
      |---|---|---|---|
      | PR #558 | `18128ef` | 33273433200 | 2037 |
      | develop | `52314859` | 33776355757 | 2037 |
      | PR #555 | `8936a2bd` | 33971138022 | 2081 |

      Both of the last two were read back with `gh run view <id> --log-failed`;
      run 33971138022 has since been re-run green, so its failure is only under
      `--attempt 1`.

  - Red command (the failing lane, in isolation), all `OS_*` unset:

    ```bash
    WR_TEST_SHARD=b WR_TEST_RUNNER_BINARY=<non-race jobqueue.test> \
      WR_RUNNEREXECSHELL=/bin/bash \
      <race jobqueue.test> -test.run '^TestJobqueueSignal$' -test.v -test.count=1
    ```

  - **Measured reproduction rate: 0 in 1,736 runs.** Nine loop runs over six
    configurations, 8-way concurrent, always the `-race` build with a non-race
    runner binary as CI does: mild CPU load (800 runs), `taskset` to 2 cores
    plus busy loops (48), `GOMAXPROCS=1` (240), `GOMAXPROCS=2` (240), a `dd`
    fsync storm plus up to 20 busy loops (48 + 160 + 32), and the DB on NFS with
    an NFS fsync storm (48 + 120). The last 200 of those were instrumented,
    which is where the timings below come from. Not once. See "why it does not
    reproduce here" below - the reason is measured, not a shrug.

  - **Classification: the job NEVER becomes `lost`; it is re-run.** So raising
    the `runnerStartWait` bound is both useless and forbidden. Two independent
    proofs:

    1. `runServer` sets `serverConfig.Timings.ItemTTR = 200ms`
       (`jobqueue/jobqueue_test.go:1383`) for this daemon, not the 60s
       `ServerItemTTR` default, and lost-detection needs one TTR. The bound is
       180s - 900x the detection time. An instrumented passing run shows the
       whole recovery-to-lost sequence completing inside 1s.
    2. Both CI failures took 182.23s and 182.69s. Subtracting the 180s bound
       leaves 2.23s and 2.69s for everything else, against 2.23s for the same
       steps on an idle box here. The runner was therefore NOT CPU-starved, so a
       200ms TTR was not "a bit too slow".

  - **Root cause: the test crashes the manager before the manager has durably
    recorded that the job started, and nothing synchronises the two.**
    `handleStart` (`jobqueue/serverCLI.go:1094`) records the `running` state
    with `s.db.updateJobAfterChange` (`jobqueue/db.go:4119`) and returns without
    waiting for it. That call queues the encoded job for the best-effort writer
    (`launchJobChangeUpdate`, `jobqueue/db.go:4153`), a single long-lived
    goroutine that drains everything pending into one `db.bolt.Update` whenever
    it is next signalled (`bestEffortWriter`, `:1647`; `drainBestEffort`,
    `:1676`), and whose write error is logged rather than returned.

    Note the asymmetry: `db.archiveJob` (`jobqueue/db.go:3242`) enqueues its own
    op and blocks on `<-op.result`, so "job completed" IS durable before the
    manager acknowledges it while "job started" is not, despite `handleStart`'s
    own comment saying it saves to disk "so recovery is possible after a crash".

    Meanwhile the test's trigger to `SIGKILL` the manager is the marker file the
    job's *command* touches, and the command writes that marker before the
    runner can even send `Started` (the runner reports the command's pid, so it
    has to have started it first). Nothing therefore orders the kill against the
    manager learning, let alone persisting, that the job is running; measured
    over 200 runs, the test observed the marker 12-111ms after the manager
    handled `Started` in 199 of them and 1ms before it in one. If the write has
    not reached disk by the time the manager dies, the restarted manager reads
    the job back with `State == ""`, so `recoveredItemDef`
    (`jobqueue/server.go:4548`) takes its `default` branch instead of
    `StartQueue = SubQueueRun`, the job lands on the ready queue, a fresh runner
    reserves it, and `resetJobForReservation` clears `Lost`. `cmd2` loops until
    cleanup, so the job then reads `running` for ever and the double run is
    invisible to the test.

  - **Write path re-checked on develop `41a04a26`, 2026-09-17.** The diagnosis
    below was made before #555 merged, when `launchJobChangeUpdate` ran
    `db.bolt.Batch` in a per-change goroutine. #555 replaced that with the
    coalescing best-effort writer described above, so the commit-latency numbers
    in the table were measured on the older path. The causal chain is unchanged,
    and the window is no longer bounded by bolt's 10ms `MaxBatchDelay`: the
    drain happens whenever the writer goroutine is next scheduled.

  - Proven by fault injection, twice, each reproducing the CI signature exactly
    (one failure, at the State line, with the later recover/wait assertions
    still passing). With the `handleStart` write skipped, and separately with it
    delayed 250ms, the server log shows:

    ```text
    DIAG recoveredItemDef key=0042c41... state= lost=false exited=false pid=0 attempts=0
    DIAG resetJobForReservation key=0042c41... state=
    DIAG applyJobStart key=0042c41... pid=2343100 attempts=1
    ```

    i.e. recovered as not-running, then re-reserved and re-run. With the write
    left alone it is `state=running ... attempts=1` and the job is marked lost
    200ms later.

  - **Why it does not reproduce here, measured.** The margin that decides the
    outcome is `SIGKILL time - write commit time`, and it is dominated by the
    `process.Processes()` + `Cmdline()` sweep the test does between seeing the
    marker and killing the manager. On this box that sweep costs 73-1092ms
    because /proc holds ~2,700 processes; a GitHub runner's /proc holds a few
    dozen, so the same sweep costs single-digit ms there. Across 200
    instrumented loaded runs plus two idle ones:

    | regime | n | commit latency | /proc sweep | kill-minus-commit margin |
    |---|---|---|---|---|
    | idle | 2 | 13ms | 73ms | 53-100ms |
    | fsync storm + 20 busy loops | 32 | 14-49ms | 198-690ms | 280-837ms |
    | NFS DB + NFS fsync storm | 168 | 12-40ms | 91-1092ms | 112-1265ms |

    Load makes it *safer* here, because the sweep slows down far more than the
    write does. CI sits at the crossover (sweep ~10ms vs commit 12-49ms), which
    is why only CI sees it. `unshare --pid` to fake a small /proc is not
    permitted on this box, so the CI regime cannot be emulated locally.

  - **Do NOT "fix" this by raising `runnerStartWait`, widening the wait, or
    retrying**: the state never arrives. Two things need deciding, in this
    order:
    1. PRODUCT: make the `Started` -> `running` record durable before the
       manager acknowledges `Started`, symmetrically with `archiveJob`. The
       best-effort writer already folds all pending work into one transaction,
       so the per-start fsync cost the old `bolt.Batch` path had to argue about
       is already gone; what `handleStart` lacks is a way to wait for the drain
       that covers its own write. The shape for that exists -
       `launchJobChangeUpdate` records a `db.wg` key per queued change and
       `doneBestEffort` (`jobqueue/db.go:1717`) releases it after the write - so
       this is a question of which callers wait, not of new machinery. It is a
       reliability-critical write path (see `.docs/reliable4/`), so it needs the
       maintainer's call plus a scale check; the other `updateJobAfterChange`
       callers (suspend, resume, kick) can stay async.
       It is also a real production defect in its own right, not just a test
       enabler: a manager killed before its job's `Started` reaches disk re-runs
       that job on restart while its command is still alive, which is the double
       run DEVELOPERS.md §1 and its rule 4 exist to prevent.
    2. TEST: only once (1) holds can this test's precondition be established at
       a supported boundary - wait for the manager itself to report the job
       `running` before crashing it, instead of relying on a marker file the
       job's command writes ahead of `Started`, with the /proc sweep as
       accidental slack.

  - To re-diagnose: set `WR_TEST_SERVER_LOG=<path>` and the `--servermode`
    daemon writes its own debug log there (its stderr is otherwise discarded).
    That hook replaced the dead commented-out block in `runServer` and is the
    only code change committed for this item. The `DIAG` lines quoted above came
    from temporary `clog.Warn` calls in `recoveredItemDef`, `ttrCallback`,
    `confirmOrReleaseLostJob`, `resetJobForReservation`, `applyJobStart` and
    `launchJobChangeUpdate`, plus `WR_DIAG_SKIP_START_WRITE` /
    `WR_DIAG_START_WRITE_DELAY_MS` knobs around `handleStart`'s write; all
    reverted.

## The fix

- [x] **`handleStart` acknowledged a runner's `Started` before the `running`
      state was on disk, so a manager that died in that window re-ran a job
      whose command was still alive.** Step 1 (PRODUCT) above, done; step 2
      (TEST) is still open, see "What is NOT fixed" below.

  - Chosen option: **Option 1, `handleStart` waits for its own write**, not the
    preferred Option 2 (fix recovery). The recovery route was ruled out on
    evidence, below. What makes Option 1 affordable is that it is a wait, not a
    transaction: `drainBestEffort` already folds every pending write into ONE
    `db.bolt.Update`, so N concurrent starts still cost ONE commit, and the
    per-start `bolt.Batch` the original diagnosis had to argue about is
    already gone (#555). Nothing else changed: `suspendJob`, `resumeJob` and
    `kickJobs` still call the async `updateJobAfterChange`, because they
    acknowledge nothing a crash could act on.

  - **Why the recovery route was ruled out. No durable-or-live discriminator
    exists that can tell "started but not persisted" from "never started".**
    Three candidates were checked in the code, and all three fail:

    1. *Nothing about a started job is persisted earlier than `handleStart`'s
       write.* The live bucket is written at Add (`storeNewJobs`), by the four
       `updateJobAfterChange` callers, and by archive/delete/modify - that is
       the complete set of `bucketJobsLive` writers. `handleReserve` ->
       `respondWithReservedJob` -> `resetJobForReservation` mutates the
       in-memory `*Job` only (`ReservedBy`, the run token, the runner's
       host+pid) and writes nothing. So the record recovery reads is the
       Add-time one, and `State == ""` there is *identical* for a job that was
       reserved-and-started and for a job that has never been touched. There is
       no field left to discriminate on.
    2. *The runner's working directory is durable and earlier, but unusable.*
       `resolveWorkingDir` -> `mkHashedDir` does create `ActualCwd` before the
       Cmd starts, and the manager does not learn the path until `Started`
       reports it. Recovery could stat for it, and must not: a `CwdMatters` job
       creates no directory at all (`setActualCwd` refuses to record one), a
       cloud runner's host need not share a filesystem with the manager, a
       workspace left by an earlier crashed run is a false positive, and
       stat-ing the hashed tree for each of production's 150,472 recovered jobs
       is exactly the per-job I/O storm `.docs/bugfixes/260825-2.md` records
       having to remove from this same startup path.
    3. *The still-alive runner's live signal arrives too late to be a
       discriminator.* It does arrive - but `ClientTouchInterval` is 15s, so the
       first touch after a restart is up to 15s late, and `getij(cr, true)`
       rejects it with `ErrBadJob` because the recovered item is in the Ready
       sub-queue, not Run. In those 15s the restarted manager can complete
       recovery, run a rac cycle, schedule a runner and hand it the job, which
       is the double run. `retryStartReport` is no better: it only runs when the
       *first* `Started` attempt failed, and in this bug the first attempt
       succeeded - that success is the bug. Reconciling on touch would therefore
       be a race against reservation, and criterion 1 says never, not usually.

  - Red command (deterministic, no env knobs, ~8s), all `OS_*` unset:

    ```bash
    CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -v \
      -run '^TestStartDurability$'
    ```

    Without the fix (`jobqueue/db.go` and `jobqueue/serverCLI.go` reverted, the
    test kept):

    ```text
    Failures:

      * .../jobqueue/start_durability_test.go
      Line 196:
      Expected: 1
      Actual:   2

    17 total assertions

    --- FAIL: TestStartDurability (0.75s)
    ```

    Two runs of the command in the marker file, the first still alive: the
    double run itself, not a proxy for it. With the fix:

    ```text
    19 total assertions

    --- PASS: TestStartDurability (7.90s)
    ```

  - How the test opens the window without fault-injection knobs. Holding the
    single bbolt write transaction (`server.db.bolt.Begin(true)`, the seam
    `reliable4_besteffort_writer_test.go` already uses) keeps the queued start
    write off disk exactly as a manager that dies before its next drain leaves
    it. The crash image is bolt's own consistent snapshot of committed state
    (`server.BackupDB`), taken at the first instant the runner has been told its
    start was recorded - immediately for the unfixed manager, only after the
    commit for the fixed one - and written over the database the restarted
    manager opens. The assertion is then the criterion itself: a fresh client
    Reserves and Executes whatever the recovered manager gives it, and the
    command's marker file must still record exactly one run, with the first
    run's process still alive.

  - Files changed:
    - `jobqueue/db.go`: `beBatch` and the pending state gain a `waiters`
      list; `drainBestEffort`/`doneBestEffort` hand the drain's error to each
      waiter (buffered(1), never closed, as `archiveOp.reply` does);
      `updateJobAfterChange` is split into `queueJobChange` (encode + enqueue)
      plus the existing fire-and-forget entry point and a new
      `updateJobAfterChangeDurable` that blocks on its waiter.
    - `jobqueue/serverCLI.go`: `handleStart` calls
      `updateJobAfterChangeDurable` and returns `ErrInternalError` if the write
      failed. That is deliberately NOT a definitive rejection
      (`isDefinitiveReject`), so the runner keeps its healthy command running,
      its touch loop holds the TTR, and `retryStartReport` re-sends the start
      until it persists - instead of the command being killed or run on with an
      unrecorded start.
    - `jobqueue/start_durability_test.go`: the regression test above.

  - Deadlock and shutdown checked, not assumed. The waiter is created and
    enqueued under `db.RLock`, and waited on only after that lock is released,
    so it cannot block the drain it is waiting for. `db.close` takes `db.Lock`
    before setting `closed`, so an enqueue either precedes it (and is answered
    by `stopBestEffortWriter`'s final drain) or sees `closed` and returns
    `errDBClosed` without enqueuing - it can never enqueue onto a writer that
    has already exited. In practice `closeServerCommsAndDB` runs
    `waitForClientHandling` before `db.close`, so no `handleStart` is in flight
    when the db closes. Each request has its own goroutine
    (`serveClientsReader` -> `go dispatchClientRequest`, 6 readers), so a start
    that waits does not stall admission of other RPCs.

  - Scale checked, since this is the write path #555 exists for. The gate that
    drives `Started()` is `developers/wrdev.sh report-storm` (200 real client
    runners tight-looping reserve -> Started -> touch -> archive behind one
    limit group), run at `5000 200 2000 60` with and without the fix on the same
    host, minutes apart:

    | | completions | rate | max report RPC | max archive latency |
    |---|---|---|---|---|
    | develop | 5000/5000 | 528/s | 26ms | 45ms |
    | fixed | 5000/5000 | 597/s | 30ms | 80ms |

    Both accepted all 5000 `Started` reports and all 5000 archives, with zero
    bad-job rejections, zero receive timeouts and zero reconnects on either run;
    the rate difference is host noise on a shared box, not a signal. The reason
    the wait does not show up is the coalescing: 200 starts arriving together
    are answered by the same commit, so the fix adds one drain's latency to a
    start, not one transaction per start.

  - Accepted cost, consistent with criterion 3. A start now shares the archive
    path's exposure to a slow commit: under the reliable4 freeze conditions it
    would block up to the client's 60s `ClientMinRequestTimeout` floor, after
    which the runner treats it as transient, keeps the command running and
    re-reports. That is latency, not a lost or doubled run.

  - **What is NOT fixed, and must not be claimed.** Step 2 above is still open,
    and `TestJobqueueSignal` is untouched, so its `signal_b` flake is NOT
    fixed. This fix makes the *acknowledged* start durable; a manager SIGKILLed
    strictly before the acknowledgement still comes back with the job on the
    ready queue, and that is the window the signal test's marker-file trigger
    lands in. Polling the manager for `running` does not close it either:
    `applyJobStart` sets `State` in memory before the write is queued, so a
    client can observe `running` while the write is still pending. Closing it
    needs a precondition that observes the runner's `Started()` having
    RETURNED, which the signal test has no supported boundary for today - which
    is why the regression test above is a separate deterministic test rather
    than a change to that one.

  - Gates, all `OS_*` unset. `make lint`: **0 issues**, which is the passing
    baseline - any issue at all belongs to the change. Two things had to be put
    right before the verdict meant anything, both worth knowing for the next
    branch:
    - `.golangci.yml` sets `new-from-rev: master`, so the verdict depends on the
      LOCAL `master` ref. This clone had none, and golangci-lint then silently
      falls back to scanning the whole tree (44 issues) instead of erroring.
      `git fetch origin master:master` gives `b2f0ff9`; a stale local `master`
      is just as misleading, since linting from an older ancestor surfaces
      constructs that are already on `origin/master`
      (`jobqueue/behaviours.go:323` funlen and
      `jobqueue/modify_validation_test.go:421` gci are exactly that). Check
      `git rev-parse master` before believing a non-zero count.
    - golangci-lint's cache collides between the sibling clones that share this
      module path, so a run can report another clone's files;
      `golangci-lint cache clean` first.

    Against the whole-tree scan this branch adds nothing: 47 issues before the
    three new ones were fixed and 44 after, and the difference is exactly those
    three - `funlen` 14 -> 13 (`queueJobChange`), `nilnil` 1 -> 0 (the closed-db
    return), `noctx` 1 -> 0 (the test's `exec.Command`) - with every other count
    identical.
