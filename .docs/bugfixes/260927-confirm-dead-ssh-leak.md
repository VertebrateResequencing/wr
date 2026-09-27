- [x] Possible new goroutine leak: `developers/wrdev.sh confirm-dead-leak`
  (TestReliable4ConfirmDeadSSHLeak, which drives
  `Scheduler.ProcessNotRunningOnHost` against localhost over ssh 40 times and
  counts ssh-client goroutines, bound 20) printed `base=0 peak=4 leaked=4` in 2
  of 3 runs on develop 3350615, but 0 in 4 of 4 runs on 619099e. Logs:
  `/nfs/hgi/wr/sb10-bigdb/sweep-260927/{fast,rerun,ab619099e}-confirm-dead-leak.log`.
  Bisect 619099e..3350615, find the leaked goroutines, and decide whether they
  are a real leak or a late close caught by the sampling window.
  - Red command: a diagnostic copy of the test (scratch, not committed) that
    runs the same 40 checks, then counts ssh goroutines every 100ms for 5s and
    dumps their stacks. It was dropped into each checkout, built with the
    named toolchain, and run 20 times. 4 configurations ran concurrently,
    which raised the rate over the sweep's:

    ```
    619099e go1.26.3  nonzero first sample 14/20, nonzero after 100ms 0/20
    619099e go1.27.1  nonzero first sample  7/20, nonzero after 100ms 0/20
    3350615 go1.26.3  nonzero first sample  5/20, nonzero after 100ms 0/20
    3350615 go1.27.1  nonzero first sample  9/20, nonzero after 100ms 0/20
    ```

    Every nonzero run had the timeline `4,0,0,...` (a few `3` or `2`). A
    sequential run of 10 on 3350615 gave the same shape in 2 of 10.
  - Stacks of the 4, taken at the first sample: one client's `mux.loop` in
    `handshakeTransport.readPacket` (state `runnable`, already woken by the
    closed socket), `Client.handleGlobalRequests`, `Client.handleChannelOpens`
    and the `NewClient` `mux.Wait` closer. These are the last check's client,
    whose `lsfHost.Close` -> `CloseSSHConnections` -> `ssh.Client.Close` had
    just returned. Close only closes the socket; these goroutines then exit
    asynchronously.
  - Bisect: not run. The "good" endpoint 619099e shows the transient on both
    toolchains, so there is no introducing commit and no toolchain effect. The
    sweep's 0 of 4 on 619099e was chance at this rate. Nothing on the ssh path
    changed between the commits either: cloud/ is untouched, and the
    scheduler.go and lsf.go hunks are in bkill trimming and process-state
    parsing.
  - Verdict: not a leak. The test took the PEAK of 20 samples starting the
    instant the last check returned, so a close still unwinding was counted
    as leaked. It stayed green only because 4 is under the bound of 20.
  - Fix (test only), in `jobqueue/scheduler/reliable4_confirmdead_leak_test.go`:
    after the checks, poll every 100ms for up to 5s until the count is back to
    base, and fail if any ssh goroutine is left (`leaked > 0`). The bound of 20
    is gone, so the test is stricter: the old bound missed one unclosed client
    (6 goroutines). The log line now prints `peak` (first sample, diagnostic)
    and `settled`. The comments no longer describe the fixed bug as current.
    The `developers/wrdev.sh` comment for the mode says the same.
  - After: 20 of 20 runs (4 concurrent x 5) passed with `settled=0
    leaked=0`, 9 of them having seen `peak=3` or `4` first.
    `wrdev.sh confirm-dead-leak` passed with `leaked=0`.
  - Mutations (reverted): M1, `lsfHost.Close` a no-op, gave `peak=240
    settled=240 leaked=240` and FAIL. M2, skipping the close on only the 20th
    check, gave `settled=6 leaked=6` and FAIL twice, which the old bound of 20
    would have passed.
  - Gates: `make lint` 0 issues; `make test` 715 passed, 20 skipped;
    `CGO_ENABLED=1 make race` 715 passed, 19 skipped.
- [x] PR #626 review: `reliable4CDLeakPoll` was 10ms, and each poll dumps
  every goroutine's stack into an 8MiB buffer, so a failing run took about
  500 dumps in 5s. It is now 100ms, the interval the goroutines were seen to
  unwind within. No red test: this is a sampling interval, and the check it
  drives is unchanged. The test still passes with `leaked=0`.
- [x] PR #626 review: `leaked := settled - base` could log a negative count
  if ssh goroutines that existed before the checks exited during them. It is
  now `max(settled-base, 0)`. No red test: the fail condition `leaked > 0`
  gives the same result either way, so only the log line changes.
