# Complete a job whose runner reports its end before its retried start

Branch `fix-archive-before-start-after-crash`. The filename has a suffix rather
than a sequence number so it cannot collide with another branch's `260929-N.md`
(see `260917-start-durability.md`).

Quality gates, with all `OS_*` unset: `make lint`, `make test`,
`CGO_ENABLED=1 make race`.

- [x] **After a manager crash, 1,091 jobs whose commands had exited 0 while
      the manager was down ran again.** Reported from prodsim round 4
      (`/nfs/hgi/wr/sb10-bigdb/soak4/run/prodsim-1790629666/`, runner logs in
      `runnerlogs.tgz`, tooling on branch `soak4` in `../wr-soak4`):

      > For crash 2, runner logs confirm the cause in 474/474 cases:
      > 1. The runner's start report timed out because the manager died before
      >    replying.
      > 2. The runner kept the command running and re-sent the start report in
      >    the background (jobqueue/client.go around :3112-3125,
      >    retryStartReport around :4010).
      > 3. The command exited 0, and its archive reached the restarted manager
      >    BEFORE the delayed start report.
      > 4. The manager rejected the archive as a bad request because the job's
      >    StartTime was still zero (jobqueue/serverCLI.go around :1426 and
      >    :1451-1453).
      > 5. The runner treats that as final and gives up (handleFinalStateError,
      >    "will need to be rerun" at client.go:3502).
      > 6. The start report arrived 2s later, and the job re-ran 7-9 minutes
      >    after that.
      >
      > Cosmetic: that message prints "%!w(<nil>)" because it wraps a nil error
      > (client.go:3502-3503). Fix that too.

  - Evidence, one crash-2 runner log (`runnerlogs/26.09.28/23-59-15.node-14-14.3338577`,
    command lines cut):

    ```text
    00:18:57 msg="started executing" jobkey=66a3063a...
    00:19:57 lvl=warn msg="could not report command start to server; keeping the healthy command running and re-reporting in the background" err="receive time out"
    00:20:03 lvl=eror msg="failed to update server with cmd's final state" err="jobqueue jarchive(66a3063a...): bad request (missing arguments?)"
    00:20:03 msg="reported command start to server after retrying"
    00:20:03 lvl=warn msg="command [...] finished running, but will need to be rerun due to a jobqueue server error: %!w(<nil>)"
    ```

    The manager (killed at 00:19:02, serving again at 00:20:02) logged 474
    `jarchive(...): bad request` errors. Among the crash-2 runs that exited 0
    while it was down, every one that ran again had started in the 6s before
    the kill (`-6s: 26/39, -5s: 172/366, -3s: 260/498`), and none that started
    earlier did. Those are the starts whose durable write was still waiting on
    a drain.

  - Root cause: two things the runner and the manager each did, which together
    lost the work.
    1. The runner sent its final report while its own start report was still
       unacknowledged. `retryStartReport` re-sent the start in the background
       on a `retryWait` ticker, while `reportFinalState` sent the archive at
       once, so both went to the restarted manager independently, in either
       order.
    2. The manager, which recovered the job into the run queue from its
       persisted reservation (#642), still held the reservation for that
       runner, but `canCompleteFromEndState` refused any completion while
       `StartTime` was zero. It returned `ErrBadRequest`, which
       `isDefinitiveReject` counts as final. The start that landed next made
       the job running, with no runner left to touch or report it, so its TTR
       lapsed and confirm-dead, finding both pids gone, re-ran it.

    The `%!w(<nil>)` came from `Execute` wrapping its outer `err`, which was
    nil by then, rather than the error `reportFinalState` gave up on.

  - Red command (deterministic, about 1s), all `OS_*` unset:

    ```bash
    CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -v \
      -run '^TestArchiveBeforeRetriedStart$'
    ```

    The test adds a job, reserves it, and takes bolt's snapshot of committed
    state the moment `Reserve()` returns: the reservation is on disk and the
    start is not. It runs the job through the real `Execute`, with a socket
    wrapper that stands in for a manager dying before it answers the first
    start report: that report is not forwarded, and its reply is a timeout
    once the manager has been crashed onto the snapshot and restarted. Every
    later start report also fails, as it would while the manager is down,
    until the manager has replied to an archive. The command then exits 0. The
    job must be complete, with a start time, and not handed to another runner,
    and the command must have run once. Before the fix (exit status 1, 3 of 3
    runs):

    ```text
    lvl=warn msg="could not report command start to server; keeping the healthy command running and re-reporting in the background" err="jobqueue jstart(): receive time out"
    lvl=eror msg="failed to update server with cmd's final state" err="jobqueue jarchive(493485b3...): bad request (missing arguments?)"
    Line 255:
    Expected: true
    Actual:   false
    --- FAIL: TestArchiveBeforeRetriedStart (0.83s)
    ```

    Line 255 is `errors.As(errExec, &jqErr)`: `Execute` returned "will need to
    be rerun", not success.

  - Fix, both sides, each enough on its own for this test:
    - Runner (`jobqueue/client.go`). A start report whose first attempt failed
      transiently is now a `pendingStartReport`, which records when the
      manager accepts or definitively rejects it. `reportFinalState` stops its
      background retries and, before each attempt to send the final state
      (archive, release or bury), re-sends the start until it is settled,
      inside the final state's own retry budget (`retryTime`) and with its
      reconnects. A transient failure of the start counts as a failed attempt
      of the final state. A definitive rejection of the start does not stop the
      final state from being sent: the manager judges that on its own, as
      before. `reportFinalState` now returns the error it gave up on, and
      `Execute` wraps that, so the "will need to be rerun" message names it.
    - Manager (`jobqueue/serverCLI.go`). `handleArchive` passes
      `markJobComplete` whether the item is in the run queue. An archive of a
      job in the run queue with no recorded start is accepted, the start being
      implied (`implyStartLocked`: `Attempts++`, and `StartTime` set to the
      end time, so no walltime is learnt for the ReqGroup rather than a guessed
      one; the archive carries no start time). The owner check comes first and
      is unchanged, so an archive from any client other than `ReservedBy` still
      gets `ErrMustReserve` (new-run-wins, #642/#646), and a job in Delay or
      Ready with no start is still refused as before. This covers a runner
      from before this fix, such as one still running from an older release
      across an upgrade. A start report that arrives after the archive finds
      the job gone and gets `ErrBadJob`, which the runner takes as settled.
      Release and bury never required a start, so they needed no change.

  - Other directions considered:
    - *Refuse such an archive with a new retryable error, rather than
      completing it.* Tried first, with only the server changed. It relies on
      the runner's start retry landing, and in `TestArchiveBeforeRetriedStart`
      it did not: the unfixed runner's archives reached the manager and were
      refused every 100ms, while its background start retries failed with
      "object closed" until `retryTime` ran out (the cause of that was not
      pursued, since the runner no longer retries that way). Completing the
      job is what the reservation already entitles its runner to, and does
      not depend on the start ever arriving.
    - *Add the start time to `JobEndState`.* It would give a truer walltime
      for the rare implied start, but changes the wire format, and a runner
      with this fix does not send an archive before its start is settled.

  - Tests, `jobqueue/archive_before_start_test.go`:
    - `TestArchiveBeforeRetriedStart`: the red test above.
    - `TestFinalStateWaitsForStartReport`: the runner's side alone. A real
      manager, with start reports failing for 2s; the socket records whether an
      archive was sent while no start had been accepted. Without the client
      change it fails (`Line 388: Expected: false Actual: true`), with only the
      server change in place.
    - `TestArchiveImpliesStart`: the manager's side alone. Another client's
      archive of a reserved, not started job gets `ErrMustReserve` and leaves
      it reserved; its runner's archive completes it with `StartTime` equal to
      the end time and `Attempts` 1, and a late start then gets `ErrBadJob`;
      and `markJobComplete` still refuses a job out of the run queue with no
      start.
    - `jobqueue/server_test.go`, `lost_job_behaviours_test.go` and
      `depgranularity_startup_test.go` were updated only for the new
      `markJobComplete` and `reportFinalState` signatures.

  - Gates: see the end of this file.

- [ ] **Crash 1 of the same soak (at 23:10:23) produced 624 of the 1,091
      double runs by another mechanism, which this fix does not cover.** Found
      while investigating the item above. Not fixed, and its cause is not
      proven; recorded here for a follow-up.

  - Evidence that it is a different mechanism:
    - The manager logged no `jarchive`, `jrelease`, `jstart` or `jtouch` error
      for any of the 588 crash-1 double-run keys that the new runners' logs
      name. After crash 1 it logged only about 146 `jtouch ... bad job` errors,
      for other keys, and one wrong-token `getbr`.
    - There were no `handing the job out anyway` warnings before crash 1 and no
      slow `jstart` requests, so reservations and starts were being recorded
      promptly.
    - Of the runs that exited 0 while the manager was down, about 25% ran
      again whatever their start time (for example `-40s: 51/224`,
      `-19s: 39/131`, `0s: 39/193`) and whatever their end time within the
      downtime, unlike crash 2, where only starts in the last 6s were affected.
    - It depends strongly on the host that ran the first run: node-14-13 68 of
      101, node-13-22 33 of 53, but node-14-08 0 of 57, node-13-07 0 of 46,
      node-13-13 0 of 42.
    - The second run's `reserved a job` line says `attempts=1`, so the first
      run's start had been recorded. For one example,
      `portal_dedupe 20260928T225323.3508` ran on node-14-16 from 23:10:16 to
      23:10:48 (manager down), and a new runner on node-14-27 reserved it at
      23:12:08, 70s after the manager was serving again.

  - Inference: those jobs were recovered as running, their first runners never
    reported to the restarted manager, and the jobs were re-run once their TTR
    lapsed and confirm-dead found both the command pid and the runner pid gone.
    Confirm-dead treats any inconclusive check as alive, so the runners had
    most likely exited or been killed without sending their archive. Nothing in
    the runner's own code path explains that: a transient archive failure is
    retried for `retryTime` (24h). The runners from before crash 1 were started
    without `--runner_filelog` (runner logs begin at 23:11:11), so their side
    cannot be seen. `orphans.tsv` shows 2,965 of 3,055 runners still running
    30s after the kill and 1,939 after 60s. The soak's database was not kept.

  - Next step: repeat a crash with runner logs enabled from the start, and keep
    the database, to see what the first runners of these jobs did.

- [x] **`make test` failed `TestDaemonStillRunningUnreadableArgv` (in `cmd`)
      once, under load.** Found by the gate run for the first item; it does
      not touch the code that item changed.

  - Evidence (`make test`, exit status 2):

    ```text
    cmd/manager_stop_shutdown_test.go Line 318:
    Expected: []string{"sleep", "60"}
    Actual:   []string(nil)
    --- FAIL: TestDaemonStillRunningUnreadableArgv (0.00s)
    ```

    It passed 5 of 5 targeted runs afterwards, so it needs the machine to be
    loaded (load average about 10-14 at the time).

  - Cause: the test read the child's argv straight after `exec.Cmd.Start`,
    which returns once the child has forked. Until the child's exec of
    `sleep` has finished, its `/proc/<pid>/cmdline` can read as empty, and
    `processArgs` returns nil for that.

  - Fix, `cmd/manager_stop_shutdown_test.go`: poll (`pollUntilTrue`, up to
    10s) until the child's argv reads as `sleep 60` before going on. What the
    test proves, that an unreaped zombie counts as stopped, is unchanged.
    `--count 20` of it passes.

- [x] **`TestReliable2RecoveryMachineryRetained` greps for
      `return nil, ErrRecovering` in `serverCLI.go`**, which the first item's
      change to `getijForReport`'s results made `return nil, nil,
      ErrRecovering`. The test now matches the new line (as two substrings,
      since `dupword` rejects the literal); the branch it guards is unchanged.

- [ ] **`make test` failed `TestManagerStopWhileShuttingDown` (in `cmd`) once,
      with `helper manager did not become ready` after 60s.** Found by a gate
      run for the first item; it does not touch the code that item changed.
      Not fixed here: it has no red command yet.

  - Evidence (`make test`, exit status 2):

    ```text
    cmd/manager_stop_shutdown_test.go Line 267:
    Expected: nil
    Actual:   'helper manager did not become ready'
    --- FAIL: TestManagerStopWhileShuttingDown (64.19s)
    ```

    It passed 3 of 3 targeted runs afterwards (about 11.6s each).

  - Likely cause, inferred rather than reproduced: the helper manager's port
    comes from `closedLocalPort`, which asks the kernel for a free port and
    closes it, so the port is in the ephemeral range (32768-60999 here). Under
    the full suite another connection can be given it as its source port, and
    then nothing can bind it until that connection closes (checked with a
    socket bound over a connected socket's source port: `EADDRINUSE`, even with
    `SO_REUSEADDR`). `reservePort` retries a port held by a socket that is not
    listening for up to `serverBindLingerBudget` (90s), which outlasts the
    test's 60s readiness wait. A fix would pick the helper's port outside the
    ephemeral range, with a red command that holds the chosen port as a
    connection's source port.

- [ ] **`CGO_ENABLED=1 make race` failed `TestManagerPortSelfConnect` (in
      `jobqueue`) once: the manager exited through `publishexit` instead of
      publishing.** Found by a gate run for the first item; it does not touch
      the code that item changed. Not fixed here: it has no red command yet.

  - Evidence (`make race`, exit status 2):

    ```text
    jobqueue/port_selfconnect_test.go Line 132:
    Expected: jobqueue.pscPublication("published")
    Actual:   jobqueue.pscPublication("exited through publishexit")
    lvl=warn msg="could not listen on the manager port yet, retrying" port=45993 err="listen tcp 0.0.0.0:45993: bind: address already in use"
    lvl=eror msg="could not listen on the manager port, so exiting" port=45993 err="listen tcp 0.0.0.0:45993: bind: address already in use"
    --- FAIL: TestManagerPortSelfConnect (19.10s)
    ```

    It passed 2 of 2 targeted `-race` runs afterwards (about 15s each).

  - Likely cause, inferred rather than reproduced: the test's ports come from
    `pscFreePort`, which finds a free port and releases it. The manager then
    held 45993 with its reservation, a bound, non-listening socket with
    `SO_REUSEADDR`, yet its listener could not bind it, so another socket
    bound the port meanwhile. Another reservation with `SO_REUSEADDR` can share
    the port, and a concurrent test process that picked the same free port
    would then take it with its own listener first. Same class as the item
    above: a free port picked and released under a parallel suite.

- Gates, all `OS_*` unset:
  - `make lint`: `0 issues.`
  - `make test`: `797 passed · 21 skipped · 32 packages · 1m32s`, exit 0
    (after the two flakes above had each failed one earlier run).
  - `CGO_ENABLED=1 make race`: `797 passed · 20 skipped · 32 packages ·
    2m34s`, exit 0, no data races.
