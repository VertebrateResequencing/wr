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
