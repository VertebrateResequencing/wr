# A job that exits 0 around a clean stop runs again (2026-10-08)

- Branch: `cleanstop-6fbb5bf1`
- Base: `origin/develop` at `9fc0d795` (#684)
- Worktree: `../wr-cleanstop`
- Queue owner: this branch, this checklist. It is item 5 of
  `.docs/reserve-runstate/delivery-queue.md` on branch `kickjob-66783567`
  (PR #685).

Evidence: `.docs/reserve-runstate/pr-notes.md` on `kickjob-66783567`,
section "Production-scale soaks (item 6.3)", and the soak trees under
`/nfs/hgi/wr/sb10-bigdb/runstate-gate/` (`soak-base/run/` and
`soak-change/run/`: `runnerlogs/26.10.07/`, `prodsim-*/markers/`,
`prodsim-*/manager.log*`), with `analysis/stop-bury-findings.md`.

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: targeted `go test` runs, plain and `-race`;
`golangci-lint run ./jobqueue/...`; `cleanorder -min-diff` on the edited Go
files.

## Prior checked items that must not regress

- `260928-clean-stop-at-scale.md`: `ShutdownRunnerWait` (60s) bounds a
  stop's wait for runners (`TestShutdownRunnerWaitIsBounded`), a runner that
  outlives a clean restart reports to the next manager
  (`TestRunnerOutlivesCleanRestart`), and a command that has exited 0 when
  the stop's kill reaches it is complete after a restart
  (`TestExitedZeroDuringStopIsComplete`). A command the stop's kill really
  ended stays buried ("stop means buried").
- `260929-small-follow-ups.md`: `TestExitedZeroDuringStopIsComplete` sets
  up an exited, not yet waited-for shell (a zombie); keep that premise.
- `260902-8.md`: the checking rendezvous is bounded and buffered, so a
  checker blocked in something wr does not control cannot park `Execute`
  for ever, and a late checker cannot leak.
- `260925-kill-before-start.md`: the checker kills outside `stateMutex`
  (`killForCheck`); a kill decided after the wait does not act.

## Bug

A job that exits 0 around a scheduled clean stop must have its completion
reported, or it runs again after restart (1 in the change soak, 2 in the
baseline; pre-existing on develop). In the baseline each run exited 0 just
as the stop's kill reached its runner. In the change soak the run exited 0
10s before the stop began, then the runner waited 61s for its
resource-checking goroutine (jobqueue/client.go about 3880-4004) and aborted
on the stop's signal before reporting. The red tests cover both: a
completion racing the stop's kill, and a completion report held up by the
resource-check wait.

A scheduled clean stop is `wr manager stop` (SIGTERM). `Server.shutdown`
calls `beginShutdown` (touches now return kill), then
`waitForRunnersToDie`, which waits at most `ShutdownRunnerWait` (60s) while
the scheduler reports runners. After that, `scheduler.Cleanup` bkills the
runners left (LSF sends SIGINT, SIGTERM, then SIGKILL about 10s later),
and the database closes. A runner whose report has not landed by then
leaves its job recovered as running, and the next manager reruns it.

## Items

- [ ] A: a completion racing the stop's kill.
  - Blocker: no red test, because no wr defect is reproducible. Both
    baseline doubles were runs that wr saw killed, not runs that exited 0.
  - Evidence. Key 643290...(`20261007T074510.20444`, runner log
    `07-26-44.node-11-4-4.4129065`): end marker `E` status 0 stamped
    07:55:33.807; the runner logged "kill requested externally" and
    "Execute(...): killed by user request" both at 07:55:33. Key 61c6ea5c...
    (`20261007T074510.6291`, `07-54-09.node-13-10.2771326`): end marker
    07:55:33.168, kill 07:55:33, "killed by user request" 07:55:36. The pid
    in "started executing" equals the marker pid in both, so `sh -c` exec'd
    `psimjob.sh` and the runner waited for the script itself.
  - Why that proves a kill: "killed by user request" comes only from
    `classifyReleasedExit`, which needs a non-nil `*exec.ExitError` from
    `cmd.Wait()`. A wait status of 0 always archives (`classifyExecOutcome`
    returns `doarchive` when the error is nil, whatever the kill flags say),
    and a kill that reaches an exited but unreaped command does not change
    its status. `psimjob.sh`'s `finish 0` takes the marker's timestamp, then
    appends the line to an NFS file, and only then runs `exit 0`. A SIGKILL
    that lands in that gap leaves an `E 0` line for a run that never exited
    0. `260928-clean-stop-at-scale.md` found the same for that soak's
    doubles. The runner reported a killed command, so wr buried it ("stop
    means buried"), and prodsim's `retry_portal` (`wr retry`) kicked it at
    08:00:06. It ran again at 08:07.
  - Tried: `TestExitedZeroDuringStopIsComplete` (a real exit 0 that the
    stop's kill reaches before the runner has waited for it) passes on this
    base:
    `nice -n 19 timeout 300 env CGO_ENABLED=1 go test -tags netgo -count 3
    ./jobqueue/ -run 'TestExitedZeroDuringStopIsComplete$' -v`, exit 0,
    `--- PASS` 3 of 3. A killed-mid-exit command is also out of reach,
    because the runner cannot tell it from any other killed command.
  - Next: a soak tooling change, not a wr fix. `soakgate.py` should not
    count a first run as a double when the runner logged "killed by user
    request" for it, or when the job was buried before its next run.
  - Deferred (owner, 2026-10-08): a separate tooling branch, item 9 of
    `.docs/reserve-runstate/delivery-queue.md`, after the release-critical
    items. Left unchecked here: not a wr defect.
- [x] B: a completion report held up by the resource-check wait
  (`checkingFinishTimeout` in `jobqueue/client.go`).
  - Evidence. Key d1582bc1... (`20261007T115143.17261`, runner log
    `12-12-10.node-13-08.2081262`): started 12:25:35, end marker `E 0`
    12:26:28. The stop began 12:26:38. The runner logged "gave up waiting
    for the resource checking goroutine to stop" at 12:27:29, then "aborting
    due to signal" (interrupt) at 12:27:47, and nothing more. No "command
    ran OK". The manager logged "gave up waiting for runners to exit"
    (`waited=1m0.269s runningJobs=1`) at 12:27:39. The job ran again at
    12:44:11 on node-11-2-2.
  - Not rare: the change soak's runner logs hold 18 give-ups in 17 logs,
    outside stops too. In 17 of them the next line ("command ran OK",
    logged once `Execute` has returned after its report landed) follows
    1-79s later, and in 11 it took 7s or more, which fits `Execute` then
    blocking on `stateMutex`. The checker holds that
    mutex while it reads the process tree's CPU time
    (`currentProcessTreeCPUtime`, a `/proc` walk), and `Execute` takes the
    mutex right after the rendezvous gives up. A report can therefore lag the
    command's exit by more than two minutes. `ShutdownRunnerWait` and
    `checkingFinishTimeout` are both 60s, so a stop that begins after the
    exit always gives up first.
  - Root cause: `Execute` sends the final state only after
    `finishedChecking.await(checkingFinishTimeout)` and
    `stateMutex.Lock()`, so a slow resource read (a `/proc` walk, a statfs,
    or a disk walk on a busy node) holds up the completion report for up to
    60s, and more when the read holds `stateMutex`. A clean stop gives up
    on the runner within that time, and bkills it before it reports.
  - Seam: `Client.processTreeCPUtimeHook` (nil in production; used in place
    of `currentProcessTreeCPUtime` through `Client.processTreeCPUtime`), in
    `jobqueue/client.go`.
  - Red test: `TestCompletionDuringStopWithSlowResourceCheck` in
    `jobqueue/stop_slow_resource_check_test.go`. The hook blocks the checker
    under `stateMutex`. The command then exits 0, and once the runner has
    waited for it the manager is stopped. `shutdownRunnersWaitHook` stands
    in for `ShutdownRunnerWait`: it lets the stop wait up to 10s, or until
    `Execute` returns. After a restart from the same database the job must
    be complete with exit code 0. A control run with a hook that does not
    block passes.
  - Red: `nice -n 19 timeout 300 env CGO_ENABLED=1 go test -tags netgo
    -count 1 ./jobqueue/ -run 'TestCompletionDuringStopWithSlowResourceCheck$'`,
    exit 1, 4 of 4 runs (11.3-13.1s each):

    ```text
    msg="recovering: decoded live jobs" jobs=1 runStates=1
    Failures:
      * jobqueue/stop_slow_resource_check_test.go
      Line 164:
      Expected: jobqueue.JobState("complete")
      Actual:   jobqueue.JobState("running")
      (Should equal)!
    17 total assertions
    --- FAIL: TestCompletionDuringStopWithSlowResourceCheck (11.36s)
    FAIL	github.com/VertebrateResequencing/wr/jobqueue	11.371s
    ```

    The assertion quoted at line 164 moved once the test file gained its
    constants and imports: it is at line 174 now, and was at line 172 when
    the mutants below were run.

  - Fix options (design choice for the owner):
    1. Recommended: keep slow reads out of `stateMutex` in the checker
       (read the CPU time outside the lock, as the memory and disk reads
       already are, and lock only to publish them), and have `Execute` wait for the checker for a
       short bound (about 1-2s) instead of `checkingFinishTimeout`, then
       report with the peaks published so far. Once the command has been
       waited for, a check's kill is a no-op (`cmdWaited`), so all `Execute`
       loses is at most one second's peak sample. `cmd.ProcessState`'s
       Maxrss and rusage CPU time still give the final peak RAM and CPU.
       The red test needs both halves.
    2. Only shorten `checkingFinishTimeout`. It is not enough: `Execute`
       still blocks on `stateMutex` while the checker holds it (the red
       test still fails).
    3. Make the manager wait longer (`ShutdownRunnerWait` above 60s plus a
       margin). Not recommended: stops get slower, `wr manager stop` gives
       up 120s after its SIGTERM, and the `stateMutex` lag has no bound.
  - Related, not in scope: `Execute`'s own final `diskUsageCheck` and the
    unmount also run before the report, and can be slow on the same nodes.
  - Red rerun by the implementor before the fix: same command, exit 1,
    `Line 164: Expected: jobqueue.JobState("complete") Actual:
    jobqueue.JobState("running")`, 11.47s.
  - Fix (option 1), in `jobqueue/client.go`:
    - The checker reads the process tree's CPU time before taking
      `stateMutex`, like its memory and disk reads already were, and takes
      the lock only to publish peaks and compare them with the limits.
    - `Execute` waits for the checker with
      `checkingRendezvous.awaitReport`: at most `checkingReportWait` (2s),
      then it reports with the peaks published so far. Final peak RAM and
      CPU time still come from `cmd.ProcessState` (Maxrss, rusage), and
      `Execute` still takes its own final disk reading, so a stuck check
      loses at most one docker CPU sample and one live peak sample.
    - Exception: once the checker has started a kill (`killForCheck` calls
      `checkingRendezvous.killing` before `killCmd`), `Execute` keeps the
      old `checkingFinishTimeout` (60s) backstop, because that kill's
      verdict (`killedForMem`, `ranoutDisk`, `signalled`) decides how the
      job is reported. A kill started after `cmdWaited` was set does not
      act, and `killing` is noted before `killCmd` reads `cmdWaited`, so a
      kill not yet noted when the short wait ends cannot change the report.
    - No new shared state outside `stateMutex` apart from the rendezvous's
      atomic flag. A checker that wakes after `Execute` gave up blocks on
      `stateMutex` until `Execute` returns, as before.
  - Tests, in `jobqueue/stop_slow_resource_check_test.go`:
    - `TestCompletionDuringStopWithSlowResourceCheck` (the red test) now
      passes in about 3.3s.
    - `TestExecuteWithSlowResourceCheck`: a command that exits 0 while the
      checker is stuck in the CPU time read is reported within
      `checkingReportWait` plus 5s of its wait, and is complete; and a
      command the checker kills for memory, with a kill slower than
      `checkingReportWait` (`processStartHook` sleeps 3s), is buried with
      `FailReasonRAM` under an LSF scheduler name, where only wr's own
      memory kill attributes a SIGKILL to memory.
    - Mutants, each caught: CPU read back under `stateMutex` (red test
      fails, line 172 `running`); the old 60s wait (red test fails, and the
      report-time assertion fails); no long wait after a check's kill
      (`TestExecuteWithSlowResourceCheck` fails on `FailReasonRAM`).
  - Gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` under `/tmp`:
    - Red command: exit 0, about 3.4s.
    - Targeted, plain and `CGO_ENABLED=1 -race`, both exit 0: the two
      tests above, plus `TestKilledJobOutlivingTTRIsBuriedAsKilled`,
      `TestCheckingRendezvous`, `TestDockerMonitorUnresponsiveDaemon`,
      `TestTerminateChildren`, `TestKillCmdDoesNotWaitForeverForChildren`,
      `TestTerminateChildrenFollowUpKill`,
      `TestExitedZeroDuringStopIsComplete`,
      `TestSignalAfterCmdExitDoesNotBlameSignal`,
      `TestKillAfterCmdExitKillsNothing`,
      `TestKillAfterCmdExitKeepsTouching`,
      `TestKillRacingCmdExitKeepsTouching` and
      `TestShutdownRunnerWaitIsBounded`.
    - `make lint`: 0 issues. `cleanorder -min-diff` on both edited Go
      files: no changes.
    - `make test`: 962 passed, 22 skipped, PASSED (2m29s).
    - `CGO_ENABLED=1 make race`: 962 passed, 21 skipped, PASSED (3m26s),
      no data races.
    - `make speed SPEED_BASE=9fc0d795`: PASS, no benchmark or scenario
      worsened by more than 10% at p<0.05, and every scenario met its
      thresholds.
  - Review follow-up (reviewer PASSED the fix): the give-up Warn would fire
    after the 2s wait whenever a check is mid-read at exit, which is routine
    on busy nodes, and soak analysis reads those lines as real stalls.
    - `jobqueue/client.go`: `checkingRendezvous.awaitReport` takes its two
      waits as arguments and returns a `checkingReportOutcome`
      (`checkingStopped`, `checkingStillReading` or `checkingKillAbandoned`).
      `Execute` logs `checkingStillReadingMsg` ("reporting the command's end
      without waiting for the resource check in progress") at Info for the
      short give-up, and keeps `checkingKillAbandonedMsg` ("gave up waiting
      for the resource checking goroutine to stop") at Warn only for the 60s
      give-up while a check's kill is under way. Both keep the `cmd` field
      and the context's job key.
    - `jobqueue/docker_monitor_test.go`: `TestCheckingRendezvous` checks each
      outcome: still reading after the short wait with no kill; kill
      abandoned only after the long wait; stopped for a checker that
      finishes, and for one that finishes its kill after the short wait.
    - `jobqueue/stop_slow_resource_check_test.go`: `Execute` runs with a
      context log handler (`touchLogCapture`, plus a `levelsOf` method). The
      stuck-check case logs the Info line once and no Warn; a new case, a
      command whose checks finish promptly, logs neither line.
    - Mutant (the short give-up logged with the Warn message) fails
      `TestExecuteWithSlowResourceCheck` at the log-level assertion.
    - Gates, same settings: the targeted tests above, plain and
      `CGO_ENABLED=1 -race`, exit 0. `make lint`: 0 issues.
      `cleanorder -min-diff` on the three edited Go files: it moved the new
      type and constants to its order; a rerun makes no changes.
      `make test`: 962 passed, 22 skipped, PASSED (2m36s).
      `CGO_ENABLED=1 make race`: 962 passed, 21 skipped, PASSED (3m16s),
      no data races. `make speed` not rerun (log-only change).
