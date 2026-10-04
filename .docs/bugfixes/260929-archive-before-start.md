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

  - Fix, both sides, each enough on its own for this test. The manager side
    was later removed at the owner's request, and the start report now carries
    the command's real start time: see the redesign item below. What follows
    records the first version.
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

- [x] **Crash 1 of the same soak (at 23:10:23) produced 624 of the 1,091
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

  - Likely explained (owner memory, 260929): soak round 4 replaced `run/wr` in
    place at 22:45; the `kill -9` at crash 1 dropped the old inode's last
    reference and ~1,000 runners still executing it died paging in code, so
    they never archived. Fits the host dependence and missing archives. A
    production-scale crash soak with runner logs from the start is scheduled
    to confirm (261004).

  - Closed by soak9 (261004, develop `55cc2565`, runner logs from the start,
    no binary swap): 0 double runs in 741,508 runs over six `kill -9` crashes
    at 2,300-3,600 running jobs (peak 3,872), and no runner died without
    reporting. So this does not reproduce without the binary swap. Evidence
    under `/nfs/hgi/wr/sb10-bigdb/soak9/`:
    `run/prodsim-1791103425/markers-analysis.txt`, `analysis/doubles.txt` and
    `analysis/runnerlogs.txt`; see `261004-soak9-followups-906fadf9.md`.

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
    10s) until the child's argv reads as non-empty before checking it. What
    the test proves, that an unreaped zombie counts as stopped, is unchanged.
    `--count 20` of it passes. Branch `fix-stop-kill-never-reaches-cmd` makes
    the same fix, so this hunk is now byte-for-byte that branch's, and the two
    merge without a conflict in that file.

- [x] **`TestReliable2RecoveryMachineryRetained` greps for
      `return nil, ErrRecovering` in `serverCLI.go`**, which the first item's
      change to `getijForReport`'s results made `return nil, nil,
      ErrRecovering`. The test now matches the new line (as two substrings,
      since `dupword` rejects the literal); the branch it guards is unchanged.

- [x] **`make test` failed `TestManagerStopWhileShuttingDown` (in `cmd`) once,
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
  - Fixed by #667, `261002-incidental-darwin-gofmt-95822cb8.md`:
    TestManagerStopWhileShuttingDown.

- [x] **`CGO_ENABLED=1 make race` failed `TestManagerPortSelfConnect` (in
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
  - Fixed by #651, `260929-test-ci-reliability.md` (port 45993 is held by an
    IPv6-only listener), and #657, `260930-moved-on-runner.md` (`pscFreePort`
    checks `portCanListen`).

- [x] **Review: a start that reached the manager while its runner's archive,
      which had implied it, waited on its commit was accepted, and made the
      completed job running again.** Found in review of the first item, which
      makes an archive followed by a late start the expected order for a
      runner from before that fix.

  - Evidence: `TestLateStartDuringImpliedArchive` holds a bolt write
    transaction so the archive of a reserved, not started job waits on its
    commit (`archivesPending` 1), then sends the runner's start on a second
    connection. Before this fix the start was not refused: it was answered
    with success once the commit was released (`Line 564: Expected: true
    Actual: false`, at `errors.As(errStart, &jqErr)`).

  - Cause: `applyJobStart` only treats a start as a duplicate when the job is
    `Running`, and `markJobComplete` had already set it `Complete`, so the
    late start set it `Running` again, with a new `StartTime`, a zero
    `EndTime` and another `Attempt`. The archived record was right in this
    test, because `db.archiveJob` had encoded it already, but a start landing
    between `markJobComplete` and that encode would archive a running job
    with no end time. Either way, the removal from the queue was then counted
    from running to running, not to complete.

  - Fix, `jobqueue/serverCLI.go`: `applyJobStart` refuses a start with
    `ErrBadJob`, changing nothing, while `archivePendingLocked` holds. That
    is what the start gets once the archive has removed the job, and both
    runners treat it as settled. `applyJobStart` now returns the `Err*`
    string, so its test callers compare with blank.

- [x] **Review: the background start retries in the rejected alternative
      failed with "object closed".** Explained, not a separate bug:
      `handleFinalStateError` closes the client's socket after every failed
      final-state attempt and only `quickReconnect`, at the top of the next
      loop, replaces it after a `retryWait` sleep. The background start
      retries, on the same `retryWait` ticker, share that socket
      (`c.request` uses the current `c.sock` under the client lock), so they
      nearly always ran while it was closed. The fix stops those retries
      before the first final-state attempt, and settles the start inside that
      loop, after its reconnect, so nothing else uses the socket there.

- [x] **Review gate: `CGO_ENABLED=1 make race` failed `TestReliable2Release`
      (in `jobqueue`) once with a data race.** It does not touch the code the
      first item changed.

  - Evidence: `WARNING: DATA RACE`, a write by `queue.(*Queue).
    moveReadyDelayedItems` (`subqueue.go:456`, `item.go:334`) against a read
    by `fmt.Sprintf` inside `assertions.ShouldNotBeNil`, called at
    `reliable2_release_test.go:141`, then `race detected during execution of
    test`.

  - Cause: `ShouldNotBeNil` formats a non-nil value, and the test gave it the
    queue item of a job it had just released to the delay queue, which the
    queue's delay processing moves to ready, writing the item's fields.

  - Fix, `jobqueue/reliable2_release_test.go`: compare the item with nil
    instead. `--count 10` of it under `-race` passes.

- [x] **Review gate: `make test` failed `TestRESTJobModificationValidation`
      (in `jobqueue`) once: a released job read as `ready`, not `delayed`,
      after a PATCH.** It does not touch the code the first item changed. Not
      fixed here: it has no red command yet.

  - Evidence (`rest_test.go` Line 781, `Expected: "delayed"`, `Actual:
    "ready"`, in "PATCH modifies delayed jobs and preserves their state").
    It passed 5 of 5 targeted runs afterwards, and the next `make test`.

  - Likely cause, inferred rather than reproduced: the job's release delay
    ran out between the release and the PATCH's reply on a loaded machine,
    so the queue moved it to ready. A fix would give the test's job a delay
    it cannot outlast.
  - Fixed by #651, `260928-load-sensitive-flakes.md`:
    TestRESTJobModificationValidation.

- [x] **Redesign, at the owner's request: the manager must not accept
      out-of-order or missing runner messages, and the runner must send its
      start, with the correct start time, before its final report.**

  - Removed, `jobqueue/serverCLI.go`: the implied start. `canCompleteFromEndState`
    again requires a recorded `StartTime`, `markJobComplete` and
    `getijForReport` are back to their develop signatures, and
    `implyStartLocked` is gone. An archive of a job whose start the manager has
    not recorded is refused with `ErrBadRequest`, as before this branch, even
    from the runner that holds the reservation. The runner side of the first
    item is what stops the crash re-run now. A runner from an older release,
    still running across an upgrade, no longer has a manager-side safety net
    for this case: that is the owner's explicit choice. Such a runner's
    completed work is discarded, and the job re-run, exactly as in the soak.

  - The start time. The runner used to set `job.StartTime` when it built the
    start report, and the manager ignored it and recorded its own `time.Now()`
    on receipt, so a start report retried after a crash recorded when the
    report got through, not when the command started. Now:
    - Runner (`jobqueue/client.go`): `Execute` takes the time as soon as
      `cmd.Start()` succeeds and `startedRequest` sends it as the request
      job's `StartTime`, so every retry of the report carries the same time.
      `Started`, the public API, reports the time it is called.
    - Manager (`jobqueue/serverCLI.go`, `reportedStartTime`): the reported
      start is recorded as given, at the owner's decision. A zero reported
      time, from a runner too old to send one, is taken as the manager's
      `time.Now()`, as before. The manager does not adjust the reported time,
      and nothing new is stored per job: a job's walltime is the runner's end
      time minus the runner's start time, both on the runner's clock, so a
      difference between the runner's and the manager's clocks does not affect
      it.
    - Walltime, the learnt time stats and `wr status` then use the command's
      real start.

  - The late-start guard (the review item above, `startRefusalLocked`) is
    kept. Without the implied start, a job can only have an archive pending
    after its start was recorded, but a runner from an older release can
    still send a background retry of that same start, whose reply was lost,
    while its archive commits. The job is `Complete` by then, so the retry is
    not taken for a duplicate, and without the guard it would make the job
    running again with a new start and no end time.
    `TestLateStartDuringImpliedArchive` is now `TestLateStartDuringArchive`:
    the job is started first, and the retried start arrives while the archive
    waits on a held commit. Disabling the guard makes it fail at Line 610
    (`errors.As(errStart, &jqErr)`).

  - Tests, `jobqueue/archive_before_start_test.go`:
    - `TestArchiveBeforeRetriedStart` now also asserts that the recorded
      `StartTime` equals the runner's own start time and is before the
      restarted manager came up. With the manager recording `time.Now()` on
      receipt it fails at Line 280. With only the runner's settling disabled
      it fails as before (`bad request`), so the runner fix alone covers the
      crash.
    - `TestArchiveImpliesStart` is now `TestArchiveWithoutStartIsRefused`:
      another client's archive gets `ErrMustReserve`, the owner's archive
      before its start gets `ErrBadRequest` and the job stays reserved, and
      after `Started` the archive completes it with `Attempts` 1.
    - `TestReportedStartTime` covers the two cases: a reported time is used
      as given, and a zero one falls back to now.
    - `TestFinalStateWaitsForStartReport` is unchanged.
    - `jobqueue_test.go` (`TestJobqueueExecutionAndDependencyScenarios`)
      asserted that the manager's walltime for a job was no more than the
      runner's, which held while the manager recorded the start later than
      the runner did. Both gates failed there after the change, by about 30ns
      (`Expected '106.872027ms' to be less than or equal to '106.871996ms'`):
      the runner's times carry monotonic clock readings and the manager's
      copies do not. The two assertions now check that the manager's
      `StartTime` equals the runner's, and that the walltimes agree to within
      1ms.

- Gates, all `OS_*` unset:
  - `make lint`: `0 issues.`
  - `make test`: `797 passed · 21 skipped · 32 packages · 1m32s`, exit 0
    (after the two flakes above had each failed one earlier run).
  - `CGO_ENABLED=1 make race`: `797 passed · 20 skipped · 32 packages ·
    2m34s`, exit 0, no data races.
- Review gates, after the review fixes, all `OS_*` unset:
  - `make lint`: `0 issues.`
  - `make test`: `798 passed · 21 skipped · 32 packages · 1m31s`, exit 0
    (after `TestRESTJobModificationValidation` failed one earlier run).
  - `CGO_ENABLED=1 make race`: `798 passed · 20 skipped · 32 packages ·
    2m43s`, exit 0, no data races (after the `TestReliable2Release` race
    above failed one earlier run).
- Redesign gates, all `OS_*` unset:
  - `make lint`: `0 issues.`
  - `make test`: `803 passed · 21 skipped · 32 packages · 1m31s`, exit 0.
  - `CGO_ENABLED=1 make race`: `803 passed · 20 skipped · 32 packages ·
    2m51s`, exit 0, no data races.
- Gates after recording the runner's start time as given, all `OS_*` unset:
  - `make lint`: `0 issues.`
  - `make test`: `803 passed · 21 skipped · 32 packages · 1m31s`, exit 0.
  - `CGO_ENABLED=1 make race`: `803 passed · 20 skipped · 32 packages ·
    2m51s`, exit 0, no data races.
