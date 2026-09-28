# Clean `wr manager stop` fails at 700-2,300 LSF runners

Branch `fix-clean-stop-at-scale`, based on `origin/develop` at `f2888015`
(#642). The filename has a suffix rather than a sequence number so it cannot
collide with another branch's `260928-N.md`.

Evidence: the prodsim round-3 soak in
`/nfs/hgi/wr/sb10-bigdb/prodsim3/prodsim-1790596675/` (`restarts.tsv`,
`stopwatch.log`, `manager.log*`, `manager-stop.*.out`,
`profiles/stop.*.goroutine2.txt`, `markers/`), tooling on branch `soak3` in
`../wr-soak3`.

Quality gates, with all `OS_*` unset: `make lint`, `make test`,
`CGO_ENABLED=1 make race`.

- [x] **`wr manager stop` reported success, and deleted the token, while the
      manager was still alive.** All clean stops in the soak took 120.2s and
      each printed, in the same second:

      ```text
      WARN wr manager, running with pid 2518135 according to pid file .../pid, is still running 120s after I sent it a SIGTERM
      INFO wr manager running at 172.27.71.182:51842 was gracefully shut down
      ```

      The harness then found the pid still present and had to SIGKILL it. The
      manager log has no shutdown lines, and its freelist was never synced.
  - Root cause: a second SIGTERM, sent by `wr manager stop` itself, killed the
    manager part-way through its shutdown, and the stop then took the pid
    going away as a graceful stop.
    1. `stopdaemon` SIGTERMed the pid file's pid and gave up after 120s,
       because the manager was stuck in `waitForRunnersToDie` (next item).
    2. The fallback in `managerStopCmd` then connected to the still-listening
       manager, found it on this host, and called `stopdaemon` again on
       `ServerInfo.PID`: a second SIGTERM (344e714, 2018).
    3. `handleSignals` calls `signal.Stop` as soon as the first signal
       arrives (930514a, 2016), so the second SIGTERM had its default action
       and killed the manager outright, before its database was closed. That
       is why the log has no shutdown lines and `syncFreelist` never ran.
    4. The dying manager (38GB RSS in this soak) takes seconds to free its
       memory, and the kernel frees the memory holding its argv first, so
       `/proc/<pid>/cmdline` reads empty while the pid is still there. #640's
       `daemonStillRunning` compares argv with the argv from before the
       signal, saw a change, and reported the pid stopped in the same second.
       The stop printed "gracefully shut down" and called `deleteToken`. The
       harness's `ps -p` then still found the pid, and SIGKILLed it.
  - #640 is not the cause, only why it was reported in the same second. With
    `daemonStillRunning` reduced to #640's predecessor (signal 0 only), the red
    test below still fails the same way: the second SIGTERM still kills the
    manager, and the stop reports success as soon as the pid has gone.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./cmd -run
    'TestManagerStopWhileShuttingDown$'`, exit 1 before the fix:

    ```text
    Line 235:
    Expected 't=... lvl=warn msg="wr manager, running with pid 3582684 according to pid file .../pid, is still running 2s after I sent it a SIGTERM" caller=clog.go:338
    t=... lvl=info msg="wr manager running at 172.27.71.182:37023 was gracefully shut down"
    ' NOT to contain substring 'gracefully shut down' (but it did)!
    --- FAIL: TestManagerStopWhileShuttingDown (2.39s)
    ```

    A temporary probe in the same test showed the helper manager had been
    killed by SIGTERM (`sig=terminated`) and the token file removed. The test
    re-runs the test binary as a real jobqueue server with a mock scheduler
    whose only runner never exits, with an argv that looks like
    `wr manager start --deployment <d>`, and names it in the pid file. That
    manager hangs in its shutdown exactly as the soak's did. The stop's
    give-up time (`daemonStopGiveup`, formerly the constant
    `daemonStopGiveupS`) is now a var, so the test uses 2s.
  - Fix:
    - `cmd/root.go`: `stopdaemon` dies, via the new `dieDaemonStillStopping`,
      when the pid is still running `daemonStopGiveup` after the SIGTERM. It no
      longer falls back to anything, so there is no second SIGTERM, no success
      message and no `deleteToken`. The error says the manager has not been
      stopped and its token was kept, that it is most likely still shutting
      down (for example waiting for runners) and its log shows progress, that
      `wr manager stop` can be run again to keep waiting, and that
      `kill -9 <pid>` stops a hung one, with the next start recovering as after
      a crash. The fallback that connects to the manager is still used when
      the SIGTERM could not be sent at all.
    - `cmd/root.go`, `cmd/pid_identity.go`: `daemonStillRunning` counts an
      unreadable argv as stopped only once the pid is a zombie (new
      `isZombie`), so a manager still exiting is not reported stopped early.
    - `jobqueue/server.go`: `handleSignals` now shuts down through the new
      `shutdownIgnoringSignals`, which keeps handling SIGINT and SIGTERM until
      the shutdown is complete, logging "manager is already shutting down, so
      ignored a signal" for each, and only then calls `signal.Stop`. So running
      `wr manager stop` again, as the error advises, cannot kill the manager
      mid-shutdown. SIGKILL still stops it. The certificate-expiry shutdown
      goes the same way, and `handleSignals` now returns after it, rather than
      looping on channels nothing would send on again.
    - `cmd/manager.go`: the `wr manager stop` help says what happens when the
      manager is still running after 2 minutes.
  - Tests (cmd/manager_stop_shutdown_test.go): `TestManagerStopWhileShuttingDown`
    asserts the stop exits 1 without "gracefully shut down", says "still
    running" and "kept", leaves the token file and the manager alive, and sent
    only one SIGTERM (the manager logged no ignored signal). A second Convey
    SIGTERMs the shutting-down manager again and asserts it survives and logs
    the ignored signal; with the old `handleSignals` it fails at `Line 257:
    Expected: false` (the manager exited). `TestDaemonStillRunningUnreadableArgv`
    covers a zombie with unreadable argv counting as stopped. A process still
    in exit with its memory gone cannot be held in that state by a test.

- [x] **The manager's shutdown waits for its runners without a bound.**
      `waitForRunnersToDie` (jobqueue/server.go) loops `HasRunners` ->
      `lsf.busy` -> `countCmds` for as long as even one runner is still in
      LSF RUN, so the database is never closed, synced or backed up.
  - Evidence: every `profiles/stop.*.goroutine2.txt` has the SIGTERM handler's
    goroutine in `waitForRunnersToDie` (server.go:8010) under `shutdown` under
    `handleSignals`. `stopwatch.log` shows LSF's view during each stop:

    ```text
    stop 1: RUN=2124 for 60s after the SIGTERM, then RUN=1 from +73s to the end
    stop 2: RUN=2286 for 41s, then RUN=1 from +51s to the end
    stop 3: RUN=3132, RUN=594 at +20s to +71s, then RUN=3 from +81s to the end
    ```

  - Why a runner stays in LSF RUN: both. The fall from thousands to a handful
    over 40-80s is LSF catching up with runners that had already been told to
    die at their next touch (the manager's `TouchInterval` wait is 15s). The
    handful left are real runners that outlived the stop. The next manager's
    log has `jtouch(eebf1a03...)` rejected with "Client presented the wrong
    token" from 13:35:43 to 13:38:13, 12s after it started, then
    `jarchive(eebf1a03...)` rejected 1,932 times until the soak ended at
    16:29. So that runner's command was still running when the old manager
    died, ran on for 2.5 minutes, and exited 0. `jtouch/jarchive(dc4a2817...)`
    after stop 3 is the same. Such a runner never acted on a kill, since a
    killed command cannot exit 0. The soak cannot say why: runners kept no
    logs, the manager does not log request errors while shutting down
    (`dispatchClientRequest` skips them when `inShutdown`), and the database
    was not kept. One way it can happen is a touch rejected with `ErrBadJob`
    (the job not in the Run sub-queue, for example after a busy manager
    released it speculatively, as `getijForReport`'s comment describes):
    `handleTouch` returns that error rather than
    `KillCalled`, and the runner only logs a failed touch and carries on.
    Whatever the cause, the fix below makes such a runner unable to hold the
    shutdown, and the scheduler cleanup that follows bkills it.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run
    'TestShutdownRunnerWaitIsBounded$'`, exit 1 before the fix:

    ```text
    Line 122:
    Expected: true
    Actual:   false
    --- FAIL: TestShutdownRunnerWaitIsBounded (30.22s)
    ```

    A mock-scheduler runner reserves and starts a job and then never exits, so
    the scheduler reports it busy for ever; `server.Stop(ctx, true)` did not
    return within 30s.
  - Fix (jobqueue/server.go): new `ServerTimings.ShutdownRunnerWait`, default
    `ServerShutdownRunnerWait` = 60s, bounds `waitForRunnersToDie` in all,
    including its initial `TouchInterval` wait. When it runs out while the
    scheduler still reports runners, the new `logGaveUpOnRunners` warns "gave
    up waiting for runners to exit; finishing the shutdown without them, so
    the scheduler will kill any that are left, and the next manager will
    recover their jobs", with `waited` and `runningJobs` (the jobs still in
    the Run sub-queue, one per runner that has not reported). The shutdown
    then carries on as normal: `scheduler.Cleanup` (for LSF, `bkill -b` of
    every job of the deployment, so the survivors are killed), then
    `closeServerCommsAndDB`, whose `db.close` drains the writers, syncs the
    freelist and writes the final backup.
  - Why 60s: `wr manager stop` gives up 120s after its SIGTERM. The runner
    wait is half of that, leaving the other half for the cleanup and for
    saving the database. The final backup copies the whole database, and the
    comment on `managerDBOpenTimeout` notes that at 7GB on NFS that alone can
    take more than 30s. At this soak's 30GB it would take longer, so a clean
    stop can still exceed 120s. The stop now says so and keeps the token (item
    1) instead of killing the manager, so the stop is merely slow. Runners that
    exit after the bound report their jobs to the next manager (item 3).
  - Tests: `TestShutdownRunnerWaitIsBounded` (above) asserts the stop
    finishes, logs the give-up with `runningJobs=1`, leaves the freelist
    synced (`boltFreelistSynced`) and writes the final backup.
    `TestManagerStopWithARunnerThatNeverExits` (cmd) runs the whole
    `wr manager stop` against the helper manager from item 1 with a 1s runner
    wait: it exits 0, the manager exits by itself rather than by a signal, the
    token is removed, and the manager logged the give-up.

- [x] **Runners that outlive a clean stop are stuck forever.** They keep the
      old manager's token, which `wr runner` read once and never reloads, so
      the new manager rejects them thousands of times. One job was lost for
      about 3h while its LSF job held a slot for more than 82 minutes.
  - Evidence: the next manager's log (manager.log, cumulative over the soak's
    restarts) has 1,932 `jarchive(eebf1a03...)` and 107
    `jarchive(dc4a2817...)` rejections with "Client presented the wrong
    token", plus 55 and 3 `jtouch` rejections for the same keys.
  - Root cause: `wr runner` (cmd/runner.go) read the token file once and
    connected with `jobqueue.Connect`, which keeps the bytes. #640 gave Go
    clients `ConnectWithTokenFile`, which re-reads the token file when the
    manager rejects the token, but left runners on `Connect`, reasoning that a
    runner cannot outlive a clean stop. This soak shows it can (item 2). A
    clean stop deletes the token and the next manager makes a new one, so
    every touch and final report of such a runner is rejected. Its final
    report (`reportFinalState` in jobqueue/client.go) treats that as transient
    and retries for `ClientRetryTime`, 24h, holding the LSF slot all the while,
    and the job, recovered by the new manager as running, sits lost.
  - How runners get the token: from `config.ManagerTokenFile`, the same file
    the manager writes (`token()` in cmd/root.go), which on LSF is on the
    shared file system, so a runner can read the new manager's token.
  - Should an old manager's runner work for a new one? After a crash restart
    it already does: the token is kept, the runner's reconnect succeeds, and
    #642's recovery puts its job back in the Run sub-queue reserved by that
    runner, so its touches and final report are accepted. A clean stop that
    ends with runners still alive now leaves the database in the same state
    (item 2), so letting such a runner report to the new manager treats a clean
    restart the same way. It does not start new work: a final report that
    needed a reconnect sets `hadProblems`, so `Execute` returns
    `ErrStopReserving` and the runner exits "because we reconnected to a new
    server". Reloading grants nothing new either: only a process that can read
    the owner-only token file gets the token, which is who could connect
    afresh anyway (#640's reasoning).
  - Decision: (iii), both. (i) alone leaves a runner that cannot read a working
    token (for example one on a host whose copy of the token file is never
    updated) retrying for a day. (ii) alone would throw away the result of
    every orphaned job that exited 0, which item 4 must keep.
  - Red: `CGO_ENABLED=1 go test -tags netgo --count 1 ./cmd -run
    'TestRunnerOutlivesCleanRestart$'`, exit 1 before the fix, with the soak's
    signature in the log:

    ```text
    EROR Server handle client request error err="jobqueue jarchive(6b20da49...): Client presented the wrong token"
    EROR failed to update server with cmd's final state jobkey=6b20da49... err="jobqueue jarchive(6b20da49...): bad token: permission denied"
    INFO reconnected to server jobkey=6b20da49...
    Line 201:
    Expected: 0
    Actual:   -1
    --- FAIL: TestRunnerOutlivesCleanRestart (31.82s)
    ```

    The test runs the real `wr runner` in-process against an in-process
    manager whose touch interval is longer than the test, so the runner never
    learns of the stop, as the soak's did not. Its job's command waits for a
    file. The manager is stopped, its token deleted as `wr manager stop` does,
    and started again on the same database, then the command is let exit 0.
    The runner must exit 0 within 30s and the job must be complete. Before
    the fix it was still retrying with the old token. The second Convey starts
    the new manager with its token somewhere the runner cannot read: the
    runner must exit 1 within 30s. Before the fix: `Line 219: Expected: 1,
    Actual: -1`.
  - Fix:
    - `cmd/runner.go`: connect with `jobqueue.ConnectWithTokenFile(rserver,
      caFile, rdomain, config.ManagerTokenFile, timeout)`.
    - `jobqueue/client.go`: `reportFinalState` counts consecutive rejections
      for a bad token (new `countTokenRejection`) and gives up after
      `clientFinalStateTokenRejections` (3) of them, logging why. Each one
      comes after the client has already re-read its token file, so a
      rejection that persists will not go away. The runner then finds its
      `Reserve` rejected too, and exits, freeing its scheduler slot.

- [x] **A command that had already exited 0 before or during a clean stop must
      be recorded as complete, not buried and not later re-run.** The owner
      has decided that jobs killed by a clean stop stay buried ("stop means
      buried"), so only the exited-0 case is in scope.
  - Background: shutdown has made every touch return a kill since 1f75580
    (2017, #103), "so that their runners don't stay alive uselessly", and the
    runner buries a job whose command it killed for the manager with
    `FailReasonKilled` ("killed by user request", `classifyReleasedExit`).
    Both stay as they are.
  - Where an exit-0 result was lost: in the report, not the kill. A command
    whose exit status is 0 is always classified `doarchive`
    (`classifyExecOutcome` ignores a kill once the wait status is 0), and
    `handleArchive` has no shutdown gate, so an archive the stopping manager
    receives is recorded, durably (the archive writer commits before
    replying). What lost it was the manager no longer being there: before
    item 1 it was killed by the stop's second SIGTERM, and with item 2 it
    closes its socket after at most 60s of waiting. Either way the runner's
    archive then went to the next manager, which rejected its token for up to
    a day (item 3). eebf1a03 is that case: its archive was rejected 1,932
    times after its command exited 0 at 13:38.
  - Red: the first Convey of `TestRunnerOutlivesCleanRestart` (item 3), whose
    command exits 0 after the clean restart. Before item 3's fix the job was
    never recorded complete (`Line 201: Expected: 0, Actual: -1`, the runner
    still retrying its archive); now it is complete and the runner exits 0.
    The same code path serves a command that exits 0 during the stop but whose
    report arrives after the stopping manager has closed its socket.
  - Fix: item 3's (`2fefa24e`); nothing more was needed. This commit adds
    `TestExitedZeroDuringStopIsComplete` (jobqueue/stop_exited_zero_test.go)
    to pin the other half: a command that has exited 0 when its manager starts
    stopping, and that the stop's kill reaches after the exit but before the
    runner has waited for it (a temporary probe confirmed that order), is
    reported by `Execute` without error and is complete after the manager is
    restarted. It passed before any change here.
  - The soak's six "DOUBLE RUNS" (`markers-analysis.txt`) are not exit-0
    commands. Five of them are a job whose first run started 1-6s before a
    clean stop's SIGTERM (two in stop 1, one in stop 2, two in stop 3), and
    each first run's end marker is 15.5-16.1s after its start,
    which is when the runner's first touch after the SIGTERM (touch interval
    15s) returned the kill. They were buried, since `wr retry` re-ran them
    (retry acts only on buried jobs); an archived job cannot be buried. The end
    marker is not proof of exit 0: psimjob.sh writes it from an EXIT trap,
    appending to an NFS file, before bash exits, so a SIGKILL from the kill
    can land after the line is written and before the exit, and a bash killed
    by SIGTERM also runs the trap with `$?` from its last completed command
    (0; checked with a local script). The runner saw a killed command, which
    "stop means buried" keeps buried. The sixth, `build
    wrstat-ui-summarise-1790598870`, first ran on the new manager, starting 2s
    after it came up, so it is outside the stop window and not looked at
    here.
  - Residual risk, not fixed: item 2's bound means `scheduler.Cleanup` can
    bkill a runner that is still sending an exit-0 report. LSF sends SIGINT
    and SIGTERM first, which the runner, still inside `Execute`, treats as
    `signalledAfterExit` and reports the job as it ended; only a report still
    unsent at LSF's final SIGKILL (10s later by default) is lost, and that job
    is then recovered as running by the next manager.

## Gates

On this branch at the item 4 commit, with all `OS_*` unset: `make lint` 0
issues; `make test` PASSED (784 passed, 21 skipped, 1m31s); `CGO_ENABLED=1 make
race` PASSED (784 passed, 20 skipped, 3m1s). Each on the first run.
