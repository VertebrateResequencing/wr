- [x] `wr kill` of a job that is reserved or still starting reports success but has no effect. Execute starts the touch loop (jobqueue/client.go:2334) with a placeholder whenKilledByServer (:2340-2342) that only signals killDoneCh. The real killCmd handler is installed only in the checking goroutine (:2701-2711), which is launched after mount/env/docker setup, cmd.Start (:2517) and the synchronous Started RPC (:2555). A touch reply carrying the kill before then hits the placeholder, which kills nothing. The touch loop then stops touching and queues stopChecking (:2349-2361). The command runs to completion and is archived `complete`, with no further touches, no RAM/disk/time checks, and its std pipes closed early. It surfaced as intermittent failures of TestJobqueueRunnerKillRequests (runner_lifecycle_test.go:557) in full local suites on 2026-09-25, on both 1c75ffb and d056b6f, so it predates #609. Its "Running" wait also matches a reserved job (server.go:6876, 6921-6930), and the kill landed during setup. TestReliable4KilledCmdLogsBoundedCmd (reliable4_cmd_log_test.go:310-321) has the same latent timing assumption.
  - Red command: `go test ./jobqueue -run TestKillBeforeCmdStartIsHonoured -count=1 -v`. The harness is jobqueue/kill_before_start_red_test.go (now committed as jobqueue/kill_before_start_test.go). It holds setup open with a fake docker socket until the first touch reply has come back. It failed 5/5, and 3/3 under -race:
    ```
      lvl=warn msg="kill requested externally" jobkey=179c...
      kill_before_start_red_test.go:138: final state=complete failReason="" exit=0
      Line 140: Expected '<nil>' to NOT be nil (but it was)!
    --- FAIL: TestKillBeforeCmdStartIsHonoured (3.67s)
    ```
  - Fixed in `jobqueue/client.go`'s `Execute`. The late-installed
    `whenKilledByServer` closure and `wkbsMutex` are gone. Before the touch
    loop starts, `Execute` declares `killCalled` and `killCmd` (nil until
    `cmd.Start`) under the existing `stateMutex`. A touch reply carrying a kill
    sets `killCalled`. If `killCmd` is installed, the touch loop kills as
    before (stops the ticker, queues `stopChecking`). If not, it keeps
    touching and queues nothing.
  - Just before `cmd.Start`, a set flag means the command is never started.
    `buryKilledBeforeStart` runs the failure behaviours, unmounts (no upload,
    since nothing ran) and buries with `FailReasonKilled`, recording the reason
    as stderr. Exitcode stays -1 and Exited stays false. The workspace is
    reclaimed as for any run that never started.
  - `killCmd` (now `newKillCmd`, with its error chaining split into small
    helpers; same messages and logs) is installed right after `cmd.Start`, so
    there is no longer a gap between the start and the `Started` RPC or the
    checking goroutine. If the flag was set in that window, `Execute` kills at
    once. Whichever of the touch loop and `Execute` sees both the flag and
    `killCmd` first does the kill, exactly once.
  - The harness became `jobqueue/kill_before_start_test.go`
    (`TestKillBeforeCmdStartIsHonoured`). It now also proves the command never
    ran (no marker file) and that Execute's error names the kill. Before the
    fix: FAIL, `Line 163: Expected '<nil>' to NOT be nil` (Execute returned
    nil, state complete). After: PASS 3/3, and 2/2 under `-race`.
  - `TestReliable4KilledCmdLogsBoundedCmd` could not hold by construction:
    with the fix, a kill landing before the start logs no "killed child of
    cmd". It now kills only once the manager has the command's pid (after
    Started), which is after `killCmd` is installed, so the kill always reaches
    a running command. `TestJobqueueRunnerKillRequests` has a comment saying
    its Running wait also matches a reserved job, and why its Exitcode -1
    assertion holds either way.
- [x] A kill whose touch reply arrives after `cmd.Wait` could kill an unrelated
  process tree. This predates the first item. The touch goroutine blocked on
  `stateMutex` until `Execute` returned, which could be minutes later, after
  behaviours and an uploading unmount, and then ran `killCmd` anyway.
  `cmd.Process.Kill()` is safe there, but the child sweep is not:
  `getChildProcesses(cmd.Process.Pid)` builds a gopsutil process from the bare
  pid, and `terminateChildren` SIGTERMs, then SIGKILLs, its descendants. If
  the pid had been reused, that is someone else's process tree. Found in review
  of the first item. A separate pre-existing race: the server-kill path and the
  checking goroutine's disk branch both wrote `killErr` unguarded.
  - Red command: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestKillAfterCmdExitKillsNothing -v`
    (jobqueue/kill_after_exit_test.go). The job's OnExit `run` behaviour
    touches a marker and sleeps 1s. Behaviours run only after the command has
    been waited for, so the test kills the job once the marker exists. A new
    in-package seam, `Client.childProcessesHook`, stands in for pid reuse: it
    names an unrelated `sleep 30` process as the old pid's child and counts
    sweeps. Before the fix it failed:
    ```
    lvl=warn msg="failed to kill child of cmd" ... pid=979184
    Line 142: Expected: 0  (the sweep ran once, after Execute returned)
    ```
    After the fix it passed 3/3, and 2/2 under `-race`.
  - Fix: `Execute` sets an atomic `cmdWaited` right after `cmd.Wait`. `killCmd`
    is now a no-op once the flag is set, which covers every caller, including
    a disk or memory check that fires just after the wait. The touch loop
    checks the flag under `stateMutex` and drops the kill, so a command that
    ended of its own accord is no longer marked killed. The server-kill error
    now goes to its own `serverKillErr`, which is ordered by `killDoneCh`.
    `Execute` folds it into `killErr` after the wait. The disk branch now
    writes `killErr` under `stateMutex`, as the memory and signal branches do.
- [x] A signal, disk or memory tick that fired after `cmd.Wait` still set
  `signalled`, `ranoutDisk` or `killedForMem`, even though the second item made
  `killCmd` do nothing by then. So a command that exited non-zero on its own
  could be reported as signalled or out of disk. This predates both earlier
  items. A clean exit (code 0) was unaffected, since it is archived whatever
  those flags say. Found in review of the second item.
  - Red command: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestSignalAfterCmdExitDoesNotBlameSignal -v`
    (jobqueue/kill_after_exit_test.go). A new in-package seam,
    `Client.afterWaitHook`, runs right after `cmd.Wait`. The test uses it to
    send the test process SIGUSR1, which Execute is listening for, and pauses
    so the signal is queued before the checking goroutine is told to stop. The
    command is `exit 3`. Before the fix it failed:
    ```
    Line 244:
    Expected: "command exited non-zero"
    Actual:   "runner received a signal to stop"
    ```
    After the fix it passed 4/4, and 2/2 under `-race` together with the other
    kill tests.
  - Fix: `killCmd` now reports whether it acted, and each caller sets its flag
    only if it did. That covers the signal branch (`signalled`, and
    `ranoutTime` with it), the disk branch (`ranoutDisk`) and the memory branch
    (`killedForMem`). The server kill is handled the same way: `killDoneCh` now
    carries whether the kill acted, and Execute sets `killCalled` from it, so a
    server kill that races the exit and does nothing no longer buries the job
    as killed.
  - A signal that arrives after the exit still asks the runner to stop. The job
    is reported as its command ended, but Execute's error is led by a
    `FailReasonSignal` `Error` (`signalledAfterExitErr`), so `cmd/runner.go`'s
    `errors.As` check still stops the runner. Before, that only happened for a
    non-zero exit, and only because the job was misreported.
  - Also, as a review nit on the second item, `TestKillAfterCmdExitKillsNothing`
    now counts touches made after the kill, not after Execute returned. That
    proves a touch whose reply carried the kill was made while Execute was still
    running.
- [x] Copilot on PR #614 (thread PRRT_kwDOAKD33M6mAlGt): `terminateChildren`
  decided whether to log "killed child of cmd" or "failed to kill child of cmd"
  from the error built up so far (`errk`), not from that child's own
  `Terminate()` result. So it could log success for a child it failed to
  terminate, or failure for one it did terminate, and the failure line never
  said what the error was. This predates the refactor: the old inline `killCmd`
  at d056b6f (client.go:2659-2667) had the same logic.
  - Red: `TestTerminateChildren` in the new jobqueue/kill_cmd_test.go
    terminates a child whose process has already gone. Before the fix it
    failed at `Line 53`, because the log had
    `lvl=info msg="killed child of cmd"` for a Terminate that failed. After the
    fix it passes.
  - Fix: the log now depends on that child's own result, and the failure line
    includes it as `err`.
- [x] Copilot on PR #614 (thread PRRT_kwDOAKD33M6mAlHT): `chainKillErr` wrapped
  `errk` even when the next step had worked, which gave
  `... failed: %!w(<nil>)`. This also predates the refactor: the old inline
  code's docker and child branches wrapped the same way.
  - Red: `TestChainKillErr` (same file). Before the fix it failed at
    `Line 67`, with `kill failed, and the next step failed: %!w(<nil>)` where
    `kill failed` was expected. After the fix it passes.
  - Fix: when the next step's error is nil, `chainKillErr` returns `errk`
    unchanged.
- [x] Copilot on PR #614 (client.go:~2533), and the gap left open in the second
  item: when a kill reply arrived after `cmd.Wait`, the touch loop took
  `stateMutex` before it checked `cmdWaited`. Execute holds `stateMutex` for
  all of its post-exit work, so the touch goroutine blocked there. It stopped
  touching, and stopped handling `stopTouching`, through behaviours,
  unmounting, uploading and reporting. The TTR was not at stake: touches after
  a kill never refresh it. The harm was a touch loop that stopped responding
  for however long the post-exit work took. The blocking predates the second
  item: the old
  `whenKilledByServer` handler took `stateMutex` too.
  - Red: `TestKillAfterCmdExitKeepsTouching` in
    jobqueue/kill_after_exit_test.go. The job's OnExit behaviour touches a
    marker, then waits until the test creates a release file. The test kills
    the job once the marker exists, and before releasing the behaviour it
    requires 3 more touches within 2s. Before the fix it failed at `Line 307`
    (`Expected: true`, `Actual: false`): only the touch whose reply carried the
    kill was made. After the fix it passes in 0.39s.
  - Fix: a new `killMu` guards `killCalled` and `killCmd`, and `cmdWaited` is
    set under it, so the touch loop's decision to kill is ordered with the wait
    without a separate check before locking. `killMu` is never held across
    anything that blocks, or while taking `stateMutex`, so the touch loop never
    waits on `stateMutex`.
  - Lock order: `killMu` and `stateMutex` never nest, in either direction.
    Execute releases `killMu` before it takes `stateMutex`. The memory branch
    calls `killCmd` while holding
    `stateMutex`, but the installed `killCmd` only reads the atomic
    `cmdWaited`, and `killForServer` takes no locks, so neither can deadlock.
    Execute reads `killCalled` into `serverKillCalled` under `killMu` when it
    sets `cmdWaited`, and nothing can set `killCalled` after that.
  - Still true, and unchanged: once a job is killed, the manager's touch
    handler replies with the kill and does not refresh the TTR. So touches
    after a kill keep the loop responsive, but they do not extend the
    reservation.
