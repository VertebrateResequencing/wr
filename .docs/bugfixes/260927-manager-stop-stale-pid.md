- [x] `wr manager stop` (cmd/manager.go, managerStopCmd) reads
  config.ManagerPidFile and calls stopdaemon(pid) (cmd/root.go), which sends
  SIGTERM to that pid without checking that the process is a wr manager. If the
  manager crashed or the host rebooted, the pid file was left behind, and the
  kernel reused that pid for another of the user's processes, `wr manager stop`
  terminates an unrelated process: a shell, an editor, another deployment's
  manager, or another daemon. Audit the same unchecked-pid pattern elsewhere:
  other ReadPidFile users (status, start), the waitForDaemonStop polling, and
  any runner or cloud pid-file handling.
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./cmd -run 'TestManagerStopStalePidFile|TestManagerStatusStalePidFile'`
    exit 1 on develop 61d292fa. The test starts `sleep 60`, writes its pid into
    the pid file of a deployment with no manager, and runs managerStopCmd's
    real Run:

    ```
    * cmd/manager_stop_test.go
    Line 169:
    Expected: false
    Actual:   true
    --- FAIL: TestManagerStopStalePidFile (0.10s)
    ```

    (the sleep had exited, killed by stop's SIGTERM; stop then reported the
    manager as "gracefully shut down"). The status case exited 1 with "is
    supposed to be running with pid N, but is non-responsive".
  - Fix: `cmd/pid_identity.go` identifies a process by its argv, read with
    gopsutil (`/proc/<pid>/cmdline` on Linux, the `kern.procargs2` sysctl on
    macOS, no cgo). `isManagerProcess` requires `manager` followed by `start`
    and `--deployment <this deployment>`, which daemonize() always adds
    (now via the shared `deploymentFlag` const). argv is unaffected by the
    binary being renamed or replaced in place, unlike `/proc/<pid>/exe`.
  - `cmd/manager.go`: stop warns that the pid file is stale and does not
    signal a pid that fails the check, then treats the pid file as absent
    ("wr manager does not seem to be running", exit 1), or stops a manager it
    can still reach through the ServerInfo PID path as before. A real but
    non-responsive manager passes the check and is still SIGTERMed. `wr manager
    status` ignores a stale pid file the same way and prints `stopped`.
  - `cmd/root.go`: stopdaemon records the pid's argv before SIGTERM, and
    waitForDaemonStop counts the pid as stopped once it is gone or its argv
    changed (reused pid, or a zombie with empty argv), instead of treating any
    live pid as still running.
  - Why not ServerInfo/port ownership alone: both need a responsive manager,
    and a non-responsive one must stay stoppable. Why not the pid file's flock:
    testing it means taking the lock, which races a starting manager, and on a
    shared NFS home the holder may be on another host.
  - Audit, not changed: `managerDBUpgradeProcessRunning` only reports status
    and is gated by a sidecar written during this start; the local scheduler's
    `pidAlive` monitors pids it spawned itself, not read from a file;
    `Scheduler.KillProcessOnHost` sends a contract-fixed `kill -9` to a
    runner pid over ssh (changing it needs the forced-command migration its
    CONTRACT WARNING describes).
  - Review: daemonize() only added `--deployment <resolved>` when argv had no
    `--deployment`, so `wr manager start --deployment prod` (an unknown name
    that resolves to the default deployment) left argv without the resolved
    name, and stop would have refused to SIGTERM that manager once it stopped
    responding. `daemonArgs` now always appends the resolved deployment (the
    last flag wins). A foreground (`-f`) manager writes no pid file, so stop
    reaches it through the ServerInfo PID path as before.
  - Tests: `cmd/manager_stop_test.go` (stale stop, non-responsive real
    manager still SIGTERMed, stale status, argv matcher, argv-change polling).
- [x] Found in the audit: the cloud ssh forwarder pid files
  (`cloud_resources.<provider>.fm.pid` / `.fw.pid`) are read by checkProcess
  (cmd/cloud.go), which only checks that the pid is alive; `wr cloud teardown`
  and deploy cleanup then SIGKILL it via killProcess, so a stale forwarder pid
  file can SIGKILL an unrelated process, and startForwarding skips starting a
  forwarder because it thinks the reused pid is one.
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./cmd -run TestCheckProcessStalePid`
    exit 1 without the fix: `Line 243: Expected: false Actual: true`
    (checkProcess reported a `sleep 30` as a running forwarder).
  - Fix: checkProcess also requires the pid's argv to contain
    `sshForwarderFlags` (`-qngNTL`), the option cluster startForwarding runs
    every forwarder with.
  - Tests: `TestCheckProcessStalePid` in `cmd/cloud_test.go`; the existing
    TestCleanupDeployForwardingProcesses fixture now starts a process with a
    forwarder-shaped argv instead of a bare `sleep`, since a bare `sleep` is
    exactly the unrelated process the check must now spare.
- [x] PR #639 review (Copilot): reject pid <= 0 everywhere a pid read from a
  file, or reported by a server, is signalled or probed. kill(0, sig) targets
  the caller's own process group and kill(-1, sig) every process the user
  owns. Sites: checkProcess and killProcess (cmd/cloud.go), daemonStillRunning,
  stopdaemon and waitForDaemonStop (cmd/root.go), the ServerInfo.PID stop
  path, and anything else grep finds. A pid file containing 0, -1 or garbage
  must signal nothing, and stop must report the pid file as stale or invalid.
  (Copilot raised daemonStillRunning separately; it is the same fix.)
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./cmd -run 'TestManagerStopInvalidPidFile|TestNonPositivePidsAreNeverSignalled'`
    exit 1 on c6e6ccb1:

    ```
    Line 155:
    Expected 't=... lvl=eror msg="wr manager does not seem to be running on port 40077" ...
    --- FAIL: TestManagerStopInvalidPidFile (0.00s)
    Line 173:
    Expected: false
    Actual:   true
    --- FAIL: TestNonPositivePidsAreNeverSignalled (0.05s)
    ```

    (a garbage pid file was ignored silently, and `stopdaemon(-pgid)` SIGTERMed
    a whole test process group). Pid files of 0 and -1 were already refused
    by this PR's argv check, which reads no argv for them.
  - Fix: `internal/pid.go` adds `ValidPid` (1..MaxInt32) and `SignalPid`, which
    returns `ErrInvalidPid` without calling kill for any other pid. Every
    signal or probe of a pid that wr did not just start goes through it:
    stopdaemon (so the pid file and ServerInfo.PID paths), daemonStillRunning
    (so waitForDaemonStop), checkProcess, killProcess,
    managerDBUpgradeProcessRunning, and the local scheduler's pidAlive.
    processArgs uses ValidPid too. The remaining kill calls in the repo are
    `exec.Cmd.Process.Kill` on processes wr started itself.
  - `wr manager stop` now warns that a pid file it cannot parse is invalid and
    was not signalled, then carries on as if there were no pid file.
  - Tests: `TestSignalPid` (internal), `TestNonPositivePidsAreNeverSignalled`
    and `TestManagerStopInvalidPidFile` (0, -1, garbage) in
    `cmd/manager_stop_test.go`.
