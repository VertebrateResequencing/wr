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
