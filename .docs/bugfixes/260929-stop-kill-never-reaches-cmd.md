# 260929: a kill at a clean manager stop never reaches the command

Branch `fix-stop-kill-never-reaches-cmd`, based on `origin/develop` at
`83d2f693` (#646).

- [x] **During clean manager stops, the runner logged "kill requested
  externally" in 7 cases, then nothing until LSF SIGKILLed it about 90s
  later. The command was never killed. 2 of them exited 0 and were then
  re-run, which caused the only double runs at clean stops.** Code:
  jobqueue/client.go around :2870-2890 (killForServer -> newKillCmd around
  :1094). The command was not killed and nothing was logged, so something
  blocks or silently fails between the kill request and the signal.
  - Evidence (soak4, `prodsim-1790647117`, clean stops at 03:45:25 and
    04:00:06): of 4,193 runner logs with a kill request, 7 end on the
    request with nothing after it. In the other cases the next line comes
    1-2s later ("killed child of cmd", "failed to kill child of cmd" or
    "command ran OK"). The earlier round, `prodsim-1790629666`, has 6 more
    such logs.

    | runner log | cmd start | kill | psimjob marker end | LSF |
    | --- | --- | --- | --- | --- |
    | 02-59-44.node-13-18 | 03:43:34 | 03:45:33 | exit 0 at 03:46:02 | KILL 03:46:32 |
    | 02-59-46.node-13-12 | 03:44:49 | 03:45:34 | exit 0 at 03:46:24 | KILL 03:46:32 |
    | 02-59-42.node-13-10 | 03:43:11 | 03:45:37 | exit 0 at 03:46:20 | KILL 03:46:32 |
    | 03-20-45.node-14-16 | 03:44:52 | 03:45:37 | sigINT at 03:46:41 | KILL |
    | 03-48-52.node-14-15 | 03:56:08 | 04:00:07 | killed by LSF | KILL 04:01:46 |
    | 03-48-37.node-13-10 | 03:59:10 | 04:00:08 | exit 0 at 04:01:09 | KILL 04:01:39 |
    | 03-53-03.node-13-08 | 03:57:16 | 04:00:14 | killed by LSF | KILL 04:01:46 |

    Six of the 7 runners had also logged "gave up waiting for the resource
    checking goroutine to stop" on earlier jobs. The three stuck at 03:45
    logged it on 12 of their 13-16 jobs, starting minutes after they started
    (only 17 of the 20,045 runner logs have it at all). That goroutine
    spends each second in the same process-table lookup as the kill:
    `currentMemory` -> `sumChildrenMemory` and `currentProcessTreeCPUtime`
    -> `getChildProcesses`. Both use gopsutil's `Process.Children()`, which
    globs `/proc/[0-9]*/stat` and reads every process's stat, recursively
    (see 260627-1). On those runners it did not finish for more than 60s.
    The kill (`newKillCmd`) makes that lookup *before* `cmd.Process.Kill()`,
    so the kill never got as far as the signal. Execute then waited on
    `<-killDoneCh` for ever after the command exited, and nothing was
    logged. LSF CPU accounting for those runners shows little CPU used (13-15s
    over 47 minutes), so the lookup was probably stuck in the kernel on some
    entry rather than slow because it had no CPU. Either way, the lookup is
    what stalls.
  - Ruled out: touch-loop locks (the kill runs in its own goroutine since
    #634 and takes only `killMu`, which is never held while blocking);
    a kill before `cmd.Start` or before `killCmd` is installed (those log
    "was not started", or kill from `killedDuringStart`, and every stuck
    case had logged "started executing" well before the kill); the
    `cmdWaited` check (it returns at once and Execute then goes on to
    report, but these runners never reported); a changed pgid (wr kills
    by pid and a tree lookup, not by process group); and a swallowed error
    (every failure on the path is logged, or returned into `killErr`).
  - Red commands:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestChildProcessLookupReadsOnlyTheParent`
    exited 1. With `HOST_PROC` pointing at a fake /proc where one unrelated
    process's stat is a FIFO nobody writes to, `getChildProcesses` and
    `sumChildrenMemory` did not return within 5s:
    ```
    Line 109: Expected: true  Actual: false
    Line 120: Expected: true  Actual: false
    --- FAIL: TestChildProcessLookupReadsOnlyTheParent (10.01s)
    ```
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestKillCmdDoesNotWaitForeverForChildren`
    exited 1 with `childProcessesWithin` swapped back for `childProcesses`.
    The kill waited for ever on a child lookup that never returned, and the
    command was not killed:
    ```
    Line 127: Expected: true  Actual: false
    ```
  - Fix, `jobqueue/utils.go`: `processChildren` reads a process's children
    from `/proc/<pid>/task/<tid>/children` (Linux 3.5+), which reads only
    that process's own entries. It falls back to gopsutil's whole-table scan
    only when the kernel has no such files (`/proc/thread-self/children`
    missing). `getChildProcesses` and `sumChildrenMemory` use it, so the
    kill's sweep and the checking goroutine's per-second memory and CPU
    readings no longer read every process on the node. On
    `farm22-wrstat01` (404 processes, nice 19) a lookup went from 30ms to
    0.23ms.
  - Fix, `jobqueue/client.go`: `newKillCmd` gets the children through
    `childProcessesWithin`, which gives up after `killChildLookupLimit`
    (5s). The kill then kills the command without the children, logs
    "killed cmd", and returns `errChildLookupTimedOut`, which Execute
    reports as "killing the cmd also failed". A lookup it gave up on is left to
    finish in the background. So a stall anywhere in the lookup can no
    longer stop the command being killed, and it is logged.
  - Tests: `jobqueue/child_processes_test.go` (thread-children path with a
    blocking unrelated entry; the fallback when the kernel has no children
    files; a real child and grandchild tree), and
    `TestKillCmdDoesNotWaitForeverForChildren` in
    `jobqueue/kill_cmd_test.go`.
