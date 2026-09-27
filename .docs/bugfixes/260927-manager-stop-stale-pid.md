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
  - Tests: `cmd/manager_stop_test.go` (stale stop, non-responsive real
    manager still SIGTERMed, stale status, argv matcher, argv-change polling).
