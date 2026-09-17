# Incidental bugfixes 2 (branch `incidental-fixes-2`)

Bugs found while working on the LSF-scale reliability branch (`reliable4`, PR
#555), split out so that branch stays on-topic and these are not held up behind
it.

**Filename convention.** The repo allocates checklists as `YYMMDD-N.md`, so
parallel branches keep reaching for the same next number and colliding as an
add/add conflict on files with no shared content - it has happened three times
now (`reliable4` vs develop's `260828-1.md`, and #558 vs #559 both adding
`260829-1.md`). This file carries an `-incidental` suffix no sequence allocator
will generate. Anything added here appends to this one file.

Quality gates (run with ALL `OS_*` unset, or they take ~16 minutes and run the
OpenStack tests): `make test`, `CGO_ENABLED=1 make race`, `make lint`.

## Already on develop, not repeated here

PR #555 merged as `4e5739fc`, bringing `.docs/bugfixes/260827-2.md` with it, so
the three items this file was created to carry are on develop already with
their evidence intact. Re-checked against develop `41a04a26` on 2026-09-17, all
three still unfixed:

- item 13, the temp-dir leak on *successful* runs. `internal/config_test.go`
  still has 9 `os.MkdirTemp("", ...)` calls,
  `jobqueue/scheduler/scheduler_test.go` 15, plus `wr_signal_marker` at
  `jobqueue/jobqueue_test.go:1943`.
- item 14, the unconditional writes to fixed shared `/tmp` paths:
  `jobqueue/jobqueue_test.go:8333` (`/tmp/ccfmod`) and `:8409` (`/tmp/csmod`).
- item 16, `wr limit -g <group>` reporting `math.MaxInt64` instead of `-1`.
  `handleGetSetLimitGroup` still does `int(limit.Limit())`
  (`jobqueue/serverCLI.go:1884`).

That file's "Further run-state leaks across a reservation" section records
`HostIP`, `FailReason`, `StdOutC`/`StdErrC` and `State` surviving a
reservation. Still true: `resetJobForReservation`
(`jobqueue/serverCLI.go:1051`) clears `RunnerPid` and not those four. Two
things that section does not say, which decide how to approach it:

- the structural fix is worth more than the four field fixes. One list, or one
  of the two lists deriving from the other, so the next run-describing field
  cannot drift between `resetJobExecutionFields`
  (`jobqueue/serverWebI.go:930`) and `resetJobForReservation`. Decide that
  before patching entries individually.
- do NOT change `State` without checking `wr status`'s consumers: the web UI
  reconciles absolute state client-side, and a reserved job reading its
  previous state may be load-bearing there.

## Open

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

- [ ] **`make lint` is red on develop, in two places.** Found while gating the
      item above; recorded rather than waived.

      Both re-confirmed on develop `41a04a26` on 2026-09-17 by a whole-repo
      `make lint` with a cleaned cache, golangci-lint v2.12.2 (the pinned
      version). Those two are the only issues it reports, and both still report
      with this branch's only source edit replaced by develop's version of the
      file, so neither comes from anything here:

      ```text
      jobqueue/behaviours.go:323:21: Function 'run' has too many statements (21 > 20) (funlen)
      jobqueue/modify_validation_test.go:421:1: File is not properly formatted (gci)
      ```

      `Behaviour.run` grew past the statement limit on develop, in #572 or #575.

      `modify_validation_test.go` is not `gofmt`-clean: one struct-literal field
      is not padded to align with the field above it. `gofmt -l` names the file
      and `golangci-lint fmt` fixes it. The line arrived with #547.

      CI does not catch either because `.github/workflows/golangci-lint.yml`
      runs `make lint GOLANGCI_LINT_ARGS=--new-from-rev=origin/master`, which
      reports `0 issues.` here. So the plain `make lint` in the local gate set
      is stricter than CI's, and this is the gap between them.

  - Note for whoever runs the gates: a `golangci-lint run` straight after
    editing a file in this clone also reported two G703 hits in
    `jobqueue/behaviours_test.go` that #580 had already suppressed with
    `#nosec`. `golangci-lint cache clean` removed them - the shared module path
    across the sibling `wr` clones poisons the cache, exactly as the earlier
    checklists warn.
