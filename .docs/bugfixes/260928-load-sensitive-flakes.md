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
- [ ] TestReliable4SchedulerGroupSnapshotMemoised
  - Seen once, no repro. The failing assertion was not captured. Lane 46:
    `-test.run '^TestReliable4SchedulerGroupSnapshotMemoised$'
    -test.count=10` at `stress -c 8` (load 23) passed 10 of 10; the whole
    `^TestReliable4` lane at `stress -c 8` (load 31) passed; both
    `^TestReliable4SchedulerGroupSnapshot` tests `-test.count=10` at `stress
    -c 40` (load 61) passed 20 of 20; the race build at `stress -c 8` passed
    5 of 5.
  - Checked and ruled out as the cause here: the derivation counts are per
    job and the server is paused, so a background cycle cannot perturb them
    (the fix recorded in the test's comment). The malloc bound is 8 per job;
    a temporary print showed a steady cycle making 40,007-40,061 mallocs
    (2 per job) every time under load, so another goroutine would have to
    allocate about 120,000 objects during one cycle to trip it. No change
    made.
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
