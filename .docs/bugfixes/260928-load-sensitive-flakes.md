# 260928: load-sensitive test flakes

Branch `fix-load-sensitive-flakes`, based on `origin/develop` at `83d2f693`
(#646). Each flake was seen once or a few times in full-suite runs under load,
and passed when run alone.

Controlled load for every repro: `nice -n 19 stress -c N` (N is given per
item) alongside the non-race test binary (`go test -tags netgo -c`), run as
its lane runs it with `GOMAXPROCS=2`, `WR_TEST_LANE=<lane>`, `nice -n 19`.
A soak was running on the host throughout, so the 1-minute load figures
quoted include it.

- [x] TestConfirmDeadSlowHost (goroutine count 9 vs 10 at :72, and it failed
  twice in one session)
  - Line 72 is `So(got, ShouldEqual, slowHostRunners)`: 9 of the 10 lost jobs
    were reclaimed in the first round, not a goroutine count.
  - Red: lane 48, `-test.run '^TestConfirmDeadSlowHost$' -test.count=6`,
    `stress -c 40` (load 27 to 53). Before: 1 of 6 failed, `Expected: 10
    Actual: 1` in the real-shell variant. At `stress -c 8` (load 21) 10 of 10
    and 5 of 5 passed.
  - Cause (test): the slow host's 800ms per-command delay left the `sh` and
    `ps` each command runs only 200ms of the 1s check timeout. A temporary
    print of each command's run time showed 60-190ms at load 21 and up to
    390ms at load 50; the failing run's batch ended in `context deadline
    exceeded`. The lost jobs usually reach the coordinator in two batches
    (1+9 or 3+7, as the reservations spread past the 50ms coalesce window),
    so a timed-out 1-pid batch gives the 9 of 10 seen, and a timed-out 9-pid
    batch the 1 of 10 here. An unconfirmed job waits the 1h retry time.
  - Fix (test only), `jobqueue/confirmdead_slow_host_test.go`: the check
    timeout is now 3s, giving each command at least 2s of slack. The forced
    variant's delay goes from 0.2s to 0.4s, so its 11 commands (4.4s) still
    add up to more than the timeout. The real-shell variant's "a per-pid
    check would take past the reclaim wait" guard was timing-based; it is now
    a count of the commands the host was sent, which must be fewer than the
    pids. The reclaim wait goes from 5s to 30s: only the first round can
    reclaim anything (the retry time is 1h), and the loop ends as soon as all
    10 are back, so the wait only bounds how long a failure takes.
  - After, same load (`stress -c 40`, load 28 to 51): 6 of 6 passed.
  - Mutations: bounding the whole host round by the check timeout again
    (`context.WithTimeout` in `checkHost`) fails the forced variant with
    `Expected: 10 Actual: 7`. Checking each pid in its own command
    (`checkEachProcess` for each chunk in `ProcessesNotRunningOnHost`) fails
    the real-shell variant with `Expected '10' to be less than '10'`.
- [x] TestReliable4SchedulerGroupSnapshotMemoised
  - Seen once, no repro. The failing assertion was not captured. Lane 46:
    `-test.run '^TestReliable4SchedulerGroupSnapshotMemoised$'
    -test.count=10` at `stress -c 8` (load 23) passed 10 of 10; the whole
    `^TestReliable4` lane at `stress -c 8` (load 31) passed; both
    `^TestReliable4SchedulerGroupSnapshot` tests `-test.count=10` at `stress
    -c 40` (load 61) passed 20 of 20; the race build at `stress -c 8` passed
    5 of 5; with the test and `stress -c 30` on one core it passed 4 of 4.
  - Checked and ruled out as the cause here: the derivation counts are per
    job and the server is paused, so a background cycle cannot perturb them
    (the fix recorded in the test's comment). The malloc bound is 8 per job;
    a temporary print showed a steady cycle making 40,007-40,061 mallocs
    (2 per job) every time under load, so another goroutine would have to
    allocate about 120,000 objects during one cycle to trip it. No change
    made.
  - Closed, watch: not reproduced.
- [x] TestClientExecuteLiveTouchPayloads (a peak-RAM sample of 0)
  - Red: lane 48, `-test.run '^TestClientExecuteLiveTouchPayloads$'
    -test.count=3`, with the test binary and `stress -c 30` both pinned to
    one core (`taskset -c 7`), so the load is heavy for the test but costs
    the host one core. Before: 3 of 3 failed, `Expected '0' to be greater
    than or equal to '1'` in "Execute sends cumulative CPU time and observed
    peak RAM". Unpinned at `stress -c 8`, 10 of 10 passed.
  - Cause (test): the command held its memory for a fixed 5s, and Execute
    samples resources once a second, each sample walking /proc (the command's
    smaps, then a scan of every process's stat for its children, twice). A
    temporary print showed a sample taking 0.3-2.1s at `stress -c 40` and
    6-7s pinned: the one sample that read 36MB finished after the command had
    exited, when touches, and so live snapshots, had stopped.
  - Fix (test only), `jobqueue/client_payload_test.go`: the command now holds
    its memory until a release file appears, capped at 60s. A new capture
    hook, `recordAndReleaseOnceSeen`, writes the file once touches have
    carried both a CPU time of at least 1ms and a peak RAM of at least 1MB.
    Unloaded, the Convey now ends after the first sample rather than 5s.
  - After, same pinned load: 5 of 5 passed.
  - Mutation: `executeLiveState.updateResources` never raising `peakRAM`
    fails the Convey (after the 60s cap) with `Expected '0' to be greater
    than or equal to '1'`.
- [x] TestSubscriptionReconnectDuringManagerShutdown (it also takes 23-29s)
  - Not reproduced: lane 9, 7 runs with `stress -c 8` and the test on one
    core (`GOMAXPROCS=1`) passed, taking 37-42s each. Its earlier failures
    (260928's speed doc, and 260927-client-token-reload.md) were in "Unsubscribe
    against an unresponsive manager is bounded, even behind a reconnect step"
    (the resubscribe not ending in `ErrRecvTimeout`) and
    `clientLockTakenWithin` in the Convey after it.
  - Why it takes 23s unloaded, from a temporary per-Convey timer: 2.6s, 0.1s,
    0.3s, 8.8s, 0.2s, 3.3s, 0.2s, 3.2s, 4.2s. The 8.8s Convey ("A reconnect
    whose budget is spent gives up ...") gives up in 51ms, and then `Stop`
    takes 7s: with a 1ns budget the subscription sends nothing, so the one
    RPC reader waits out its 5s `InterruptTime`, then the 2s
    `ShutdownSocketWait`. The others are their configured
    `ShutdownSocketWait`s of 2-4s. These are the test's designed windows, so
    nothing was cut.
  - Candidate, unconfirmed: in the last two Conveys the resubscribe that must
    end in `ErrRecvTimeout` waits out a 2s floor that starts only after
    `pingUntilUnread` returns, while the command socket closes
    `ShutdownSocketWait` (3s, and 4s) after the readers exit. In the last
    Convey the poll goroutine's own resubscribe can hold the client for up to
    that 2s floor first, which leaves little or no margin; a resubscribe
    still waiting when the socket closes ends in a different error. It needs
    a dedicated heavy-load run to confirm. No change made.
  - Closed, watch: every failure predates #634's `pingUntilUnread` fix, and
    the candidate above is refuted; see `261004-checklist-tidy-8e94211c.md`
    item D.
- [x] TestFuseMountReaping (pidInFuseWait premise false)
  - Also recorded, unfixed, in 260927-client-token-reload.md.
  - Red: lane 9, `-test.run '^TestFuseMountReaping$'`, no added stress (host
    load 17-21 from other work). A temporary version that, when the premise
    was false, printed every child thread's wchan and state 40 times at 50ms
    intervals caught it once in 20 runs: the blocked thread was `R
    (running)` with wchan `0`, and back in `request_wait_answer` 50ms later.
    The unmodified test then passed 40 of 40, so the rate is about 1 in 60.
  - Cause (test): the premise was one sample of a state that is briefly left
    and re-entered. The child waits for the same condition before reporting,
    so the setup was right; only the parent's single read was not.
  - Fix (test only), `jobqueue/testfusemount_test.go`: the premise is now
    `awaitTrue(processWaitTimeout, ...)` on `pidInFuseWait(child.pid)`.
  - After, same conditions: 30 of 30 passed.
  - Mutation: starting that Convey's child without the wedge fails the
    premise with `Expected: true Actual: false`.
- [x] TestDepGranularityAddTransactionCost (99 <= 53.75)
  - Also recorded, unfixed, in 260927-client-token-reload.md.
  - Not reproduced under the allowed load: lane 47, 20 runs with the test
    and `stress -c 8` on one core (`GOMAXPROCS=1`), and 5 runs of
    `^TestDepGranularityAdd` with `stress -c 8` on cores 0-3, all passed.
  - Cause (test), found by measurement rather than a failing run: a
    temporary print of each side's count gave 97-100 transactions for both
    the 200- and 2000-member groups. With the server paused while the 20
    adds were measured, both were exactly 40 (2 per add). So about 60 of
    the ~100 come from the ready-added callback each add triggers (with no
    RunnerCmd it runs prepareReadyJob, which reads recommended requirements
    from the database), and how many callbacks run depends on how many adds
    arrive while one is already running. The failure's figures fit that:
    the small side's at most 43 is the 40 add-path reads plus 3, the big
    side's 99 is the usual total.
  - Fix (test only), `jobqueue/depgranularity_add_test.go`:
    `dgaAddMemberTxCost` pauses the server (which stops the callbacks, not
    adds) and waits for any running callback to finish before counting. The
    1.25 ratio assertion is unchanged.
  - After: `^TestDepGranularityAdd` 3 runs of 7 tests passed; both sides
    count 40 every time.
  - Mutation: one extra read per 100 live members of each dep group an added
    job joins (in `registerDepGroupMembers`) fails with `Expected '440' to
    be less than or equal to '100'`.
- [x] TestSubscriptionReconnectResync
  - Red: lane 9, `-test.run '^TestSubscriptionReconnectResync$'
    -test.count=4`, with the test and `stress -c 40` on one core (`taskset
    -c 7`; before the host rules below were set). Before: 4 of 4 failed,
    `Expected: true Actual: false` on `So(ok, ShouldBeTrue)` after
    `collectSubscriptionUpdates`. A temporary print showed Stop taking
    265-341ms and the break-to-restarted time 2.08-2.34s, so 2.3-2.6s of the
    subscription's 2s reconnect budget was gone before the manager was back.
    With `stress -c 20` on that core it was 1.08-1.44s plus 76-177ms of
    Stop, and passed. With `stress -c 8` on one core it passed 6 of 6.
  - Cause (test): the Conveys that restart the manager gave the
    subscription a 2s reconnect budget, which starts when Stop breaks the
    long poll and so has to cover the rest of Stop and the whole restart.
    Past it the subscription gives up and closes, as it should. The updates
    that follow were then also waited for with a fixed 2s.
  - Fix (test only), `jobqueue/subscription_test.go`: a
    `subscriptionRestartRetryTime` of 30s for the two Conveys that restart
    the manager ("A restarted manager delivers ..." and "A successful
    transient reconnect ..."), and a `subscriptionUpdateWait` of 10s for
    `collectSubscriptionUpdates` and the transient Convey's resync receive.
    Every caller of `collectSubscriptionUpdates` expects its updates to
    arrive, so the wait only bounds a failure. The permanently stopped
    manager Convey, which is what tests the budget running out, is
    unchanged.
  - After, with the allowed load (`stress -c 8` and the test on one core,
    `GOMAXPROCS=1`): 5 of 5 passed. The failing load is no longer allowed on
    this host, so a deterministic seam stands in for it: a temporary extra
    2.5s sleep before the restart failed the old test 2 of 2 and passed the
    new one 2 of 2.
  - Mutation: not publishing the resync marker after a reconnect
    (`reconnectAfterPollError`) fails both restart Conveys.
- [x] TestReliable2KeepReconnectResync (seen once)
  - Its earlier record, in 260927-race-kill-bury-flakes.md, names the 2s
    reconnect budget as an unconfirmed candidate. It is the same Convey
    shape as the item above (Stop, break the socket, restart, archive,
    collect 2 updates) with the same 2s budget, and the measurement there
    confirms that a restart under load can outlast it.
  - Fix (test only), `jobqueue/reliable2_keep_test.go`: it uses
    `subscriptionRestartRetryTime`; its `collectSubscriptionUpdates` wait
    comes from the item above.
  - Before and after: the 2.5s slower-restart seam failed the old test 2 of
    2 and passed the new one 2 of 2; with the allowed load the new one
    passed 5 of 5. The resync-marker mutation above fails it too.
- [x] TestDepGranularitySidecarReportsElapsedTime
  - Seen once, no repro, and the failing assertion was not captured. Lane
    47, `-test.count=30` with no added stress (host load 18), 40 with
    `stress -c 8` and the test on one core (`GOMAXPROCS=1`), and 30 of the
    race build the same way: all passed.
  - Candidate, unconfirmed: it samples the sidecar, waits a fixed 200ms
    (4 of the shortened 50ms heartbeats) and requires the second sample to
    have moved on, so a heartbeat goroutine starved for 200ms would fail it.
    No change made.
  - Fixed by #651, `260929-test-ci-reliability.md`, and #659,
    `261001-flakes-and-tooling.md`: TestDepGranularitySidecarReportsElapsedTime.
- [x] TestLostCwdMattersJobSparesItsSecondRun ("never declared lost and
  confirmed dead")
  - Not reproduced under the allowed load: lane 49, 20 runs with `stress -c
    8` and the test on one core (`GOMAXPROCS=1`) passed.
  - Cause (test): the lost-run fixture reported the run started with
    `Started`, which tells the manager the runner pid is the live test
    process, and only then replaced the manager's copy of that pid with a dead
    one (`runnerExited`, which also started a `/bin/true` to get the pid).
    The run's 1s TTR is running throughout. A run that expires in between is
    declared lost holding a live runner pid, so its first dead-check cannot
    confirm it, the retry is 30 minutes away, and `waitForDeadCheckWindow`
    fails after 20s with this message. A temporary print put reserve to
    `runnerExited` at 64-69ms under `stress -c 8`, against the 1s TTR, which
    a full race run at high load can use up. A temporary 1.5s sleep before
    `runnerExited` failed the Convey with exactly the reported message.
  - Fix (test only), `jobqueue/lost_job_behaviours_test.go`: the fixture
    forks both dead pids before the reservation, and
    `startedByExitedRunner` sends the Started request with the dead runner
    pid already in it, so the manager never holds the live one.
    `TestKilledLostJobsReplacementIsStillWatched`, the other `runnerExited`
    caller, does the same.
  - After: the same 1.5s sleep placed after the Started passes; lane 49's
    `^TestLost|^TestKilledLost` 2 runs of 15 tests passed; the two changed
    tests passed 10 of 10 each under `stress -c 8` on one core.
  - Mutation: `killLostRun` killing whatever run the job is on (passing no
    run to `killRunningJob`) fails `So(l.waitForKillDecision(),
    ShouldBeFalse)`.
- [x] TestJobqueueSignal, jobqueue/jobqueue_test.go:1839, lane signal_a:
  after `jq.Kick` of the time-limit-buried cmd2 and `reserveJobsByCmd`, the
  reserved job2 read back as State "lost" instead of "reserved". Failed once
  in CI (run 36550679514) on PR #647's branch; passed 6 of 6 locally on both
  that branch and develop. (Added by the coordinator.)
  - **PRODUCT BUG.** The job2 checked is the one `Reserve` returned, so the
    manager itself answered the reservation with state lost.
    `respondWithReservedJob` reserves the item (which starts its TTR), then
    `persistReservation` waits up to `ReserveWriteWait` (10s) for the
    reservation to reach disk, and only then builds the response. The signal
    daemon's TTR is 200ms, so a write slower than that (a CI disk's fsync)
    had `ttrCallback` mark the job lost and start confirming its runner dead
    during the wait, and the runner was handed a job already lost. With the
    default 60s TTR the 10s wait cannot outlast it, so production only hits
    this with a short `ItemTTR`, but the TTR also ran down during the wait,
    leaving the runner less than a TTR to its first touch.
  - Red (deterministic): the new `TestReservationTTRStartsAtHandOut`
    (`jobqueue/reserve_ttr_test.go`) runs a manager with a 200ms TTR, holds
    a bolt write transaction for 1s so the reservation's write waits, and
    reserves. `CGO_ENABLED=0 go test -tags netgo -count=1 -run
    '^TestReservationTTRStartsAtHandOut$' ./jobqueue` failed:
    `Expected: jobqueue.JobState("reserved") Actual:
    jobqueue.JobState("lost")`.
  - Fix: a server-side `Job.handingOut`, set in `resetJobForReservation`.
    `ttrCallback` leaves such a job in the run sub-queue (the queue re-arms
    its TTR) instead of marking it lost. After the write wait,
    `handOutReservation` touches the item, so the TTR restarts when the
    runner is given the job, and then clears the flag. Files:
    `jobqueue/job.go`, `jobqueue/server.go`, `jobqueue/serverCLI.go`,
    CHANGELOG.
  - After: the new test passes 3 of 3 with `TestReserveDurability*` and
    `TestStartDurability*`. It also checks that an untouched reservation
    still goes lost after hand-out; never clearing `handingOut` fails that
    with `Expected: true Actual: false`. `TestJobqueueSignal` shard a passed
    3 of 3 under `stress -c 8` on cores 0-3 both before and after, so the
    CI failure could not be reproduced in the test itself.
- [x] TestRESTJobModificationValidation
  - Not reproduced under the allowed load (lane 6, 10 runs with `stress -c 8`
    and the test on one core), and the failing assertion was not captured.
  - Cause (test): "PATCH modifies delayed jobs and preserves their state"
    releases a job and then checks, three times over, that a PATCH left it
    delayed. The test server's release delay is `ReleaseDelayMin` 100ms
    (100-200ms with the backoff's jitter), after which the job is ready
    whatever the PATCH did. The other Conveys also ran on the 1s short TTR,
    so a reserved or running job the test never touches went lost if the
    Convey took longer than that. A temporary 300ms sleep after the job was
    seen delayed failed the old test 2 of 2 with `Expected
    jobqueue.JobState("delayed") Actual: jobqueue.JobState("ready")`.
  - Fix (test only), `jobqueue/rest_test.go`: the test's server uses the
    default TTR (`jobqueueTestInit(false)`) and a 1h `ReleaseDelayMin`. No
    Convey relies on either expiring: the lost-job Convey sets `Lost`
    itself.
  - After: the same 300ms sleep passes 2 of 2; `^TestREST` 3 runs of 11
    tests passed.
  - Mutation: making a REST modify end the delay of the delayed jobs it
    modified (`SetDelay(key, 0)` in `modifyJobsByKeys`) fails with
    `Expected jobqueue.JobState("delayed") Actual:
    jobqueue.JobState("ready")`, which the old test could not tell apart
    from its own delay expiring.
- [x] TestReliable4RecoveryDependencyState (timed out waiting for the
  recovery pause hook)
  - Not reproduced under the allowed load: lane 46, 10 runs with `stress -c
    8` and the test on one core passed. A temporary print put Serve's return
    to the hook at 60us-0.8ms in every run.
  - Cause (test): `pausedRecoveringFixtureServer`, which 25 tests use, waits
    only 2s for the background recovery goroutine to reach its pause hook.
    That message has no other source, so the goroutine was not scheduled, or
    not run as far as the hook, for 2s: nothing before the hook blocks on
    anything but the scheduler (`recoverInBackground` starts the heartbeat,
    then calls the hook).
  - Fix (test only), `jobqueue/reliable2_dbcompat_test.go`: the wait is a
    hang detector, `recoveryPauseHookWait`, of 30s. It ends as soon as the
    hook is reached.
  - Before and after: a temporary 2.5s sleep in the hook before it signals
    failed the old test with this message and passes the new one.
  - Mutation: a hook that never signals fails with the same message after
    30s.
- [x] TestStartDurability and TestReliable2ReserveConfirmedDeadReclaimed
  ("could not reach the server" / "receive time out")
  - Not reproduced: lane 45, `TestReliable2ReserveConfirmedDeadReclaimed`
    15 runs with `stress -c 8` and the test on one core passed. The failing
    lines were not captured.
  - Analysis, unconfirmed: "could not reach the server" is `Connect`
    failing, and both tests connect with the suite's 1.5s
    `clientConnectTime`, which a heavily loaded host can outlast;
    `TestReliable2ReserveConfirmedDeadReclaimed` makes a fresh Connect every
    250ms while it polls. A request's own receive deadline has the 60s
    `ClientMinRequestTimeout` floor, so "receive time out" after connecting
    would need a 60s manager stall. Separately,
    `TestReliable2ReserveConfirmedDeadReclaimed` sets the dead pid after
    `Reserve` inside its 500ms TTR, the same shape as the lost-run fixture
    item above, but that would fail as `reReserved` nil, not as either
    message. No change made.
  - The reclaim test now connects once; `TestStartDurability` is closed,
    watch. See `261004-checklist-tidy-8e94211c.md` item E.
- [x] TestStatusCountReconcile (a 120s node timeout at load 80+)
  - Recorded, uninvestigated, in 260927-race-kill-bury-flakes.md.
  - Cause (test): the 120s is the context bounding the 4 node harness
    shards. Each shard is pure CPU work: run alone, 3.4-3.8s wall and user
    time. With all 4 shards and `stress -c 8` on one core the test took
    44s, so 120s means about 30 times less CPU than they need, which a host at
    load 80+ with the suite at nice 19 can give them. A failing scenario is
    reported in the harness's output, which the test asserts on; the bound
    only has to catch a harness that never finishes.
  - Fix (test only), `jobqueue/serverWebI_test.go`: the bound is
    `statusCountReconcileHangWait`, 10 minutes, still well inside the
    lane's 40m timeout.
  - After: 1 of 1 passed pinned as above (44s). Not reproduced before: the
    allowed load cannot starve it that far.
  - Check: with the bound set to 1s the test fails with `Expected: nil
    Actual: 'signal: killed'`, so a harness that overruns it still fails.
    The assertions are unchanged.

## Gate flakes seen while verifying the status summary fix (branch fix-status-summary-scaling)

- [x] `TestFuseMountReaping` (`jobqueue/testfusemount_test.go:491`) failed in
  one `make test` run at 1-min load ~38: `Expected: true / Actual: false` for
  `So(pidInFuseWait(child.pid), ShouldBeTrue)`, the premise that the wedged
  child is blocked in a fuse request. It then passed `--count 3` alone. This
  is the same failure `260927-client-token-reload.md` records as seen under
  load, and it does not touch the status summary code. Recorded here, not
  fixed.
  - Fixed by #651, this file: TestFuseMountReaping.
- [x] `TestReliable4RacBoundedBySchedulable`
  (`jobqueue/reliable4_rac_bound_test.go:117`) failed in one `make test` run
  at load ~11-16: `Expected: 5 / Actual: 18` for `racScanWork`. It then passed
  `--count 5` alone. The counter is server-wide, so a scheduling pass the
  server ran by itself during the measured window would also add to it. It
  does not touch the status summary code. Recorded here, not fixed.
  - Fixed by #663, `261002-rac-bound-7debc26e4164.md`.
- [x] `make race` failed `TestCleanupKeepsMountCachesSpelledInAnotherCase`
  with a data race on its `clog.ToBufferAtLevel` buffer
  (`jobqueue/workspace_test.go:464`). The writer was an `archiveFoldReporter`
  goroutine that `initDB` started for `TestDBCheckIfComplete`. That test,
  `TestDBRetrieveCompleteJobsRecent` and `TestDBEndTimeIndex` never closed
  their db, so each db's reporter kept running and logged its minutely
  "archive fold" warning into whichever test had captured the global logger.
  - Fix: `jobqueue/db_test.go` closes the db in those three tests with the
    same `defer` the other db tests use, which stops the reporter.
