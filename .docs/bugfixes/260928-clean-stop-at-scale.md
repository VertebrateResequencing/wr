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
    (the job not in the Run sub-queue, for example after the manager declared
    it lost and released it): `handleTouch` returns that error rather than
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

- [ ] **Runners that outlive a clean stop are stuck forever.** They keep the
      old manager's token, which `wr runner` read once and never reloads, so
      the new manager rejects them thousands of times. One job was lost for
      about 3h while its LSF job held a slot for more than 82 minutes.

- [ ] **A command that had already exited 0 before or during a clean stop must
      be recorded as complete, not buried and not later re-run.** The owner
      has decided that jobs killed by a clean stop stay buried ("stop means
      buried"), so only the exited-0 case is in scope.
