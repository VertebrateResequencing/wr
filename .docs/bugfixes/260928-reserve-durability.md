# Make a job's reservation durable before the runner is told about it

Branch `fix-double-run-after-crash-restart`. The filename has a suffix rather
than a sequence number so it cannot collide with another branch's `260928-N.md`
(see `260917-start-durability.md`).

Quality gates, with all `OS_*` unset: `make lint`, `make test`,
`CGO_ENABLED=1 make race`.

- [x] **Jobs ran twice after the manager was crash-restarted.** Reported from
      the production-shaped soak in
      `/nfs/hgi/wr/sb10-bigdb/prodsim2/prodsim-1790539118/` (tooling on branch
      `faux-develop` in `../wr-prodsim2`):

      > The manager was crash-restarted (killed without a clean stop) at
      > 00:07:53. Within 5-21s of that restart, 6 archive requests from runners
      > were rejected with `you must Reserve() a Job` (getijForReport,
      > jobqueue/serverCLI.go around :2060-2100). The DB records show that 5 of
      > those jobs (portal jobs) later completed again in a second run,
      > starting at 00:13:30. So they ran twice. There were also 12 rejected
      > jtouch calls ("bad job"), and some were logged before the manager's
      > "started on" line. So requests are handled before recovery has
      > re-enqueued the prior running jobs. Recovery took about 3 minutes,
      > because of a cold NFS read that is being fixed separately. A longer
      > recovery means a bigger window.

  - Evidence, `manager.log` of that soak. The kill was at 00:04:48
    (`restarts.tsv`: `1790550288 stop rc=crash`). Recovery then read the
    121,573 live jobs for 2m59s, enqueued them, and only then bound the port:

    ```text
    00:07:53 msg="recovering: prior state recovered" restored=121573 total=121573
    00:07:53 msg="wr manager  started on 172.27.71.182:51822, pid 856671"
    00:07:53 err="jobqueue jrelease(cdb5bae3...): you must Reserve() a Job before passing it to other methods"
    00:07:53 err="jobqueue jtouch(e4f3d778...): bad job (not in queue or correct sub-queue)"
    00:07:58 err="jobqueue jarchive(502cdb92...): you must Reserve() a Job before passing it to other methods"
    00:08:03 err="jobqueue jarchive(cdb9776d...): you must Reserve() a Job before passing it to other methods"
    00:08:14 err="jobqueue jarchive(cdbc36f9...): you must Reserve() a Job before passing it to other methods"
    ```

  - **The "requests handled before recovery" reading does not hold.** Every
    rejection in this log, and in the 22:31:41 restart before it, comes after
    the `started on` line. The code agrees: `recoverInBackground` calls
    `publishServingSurface`, which binds the RPC port, only after
    `recoverPriorJobs` has enqueued every job, and a request that lands in the
    sub-millisecond gap before `finishRecovering` gets the retryable
    `ErrRecovering`. The length of recovery does not widen the window either.
    It only delays the runners' retries.

  - **Root cause: the manager wrote nothing at reservation, yet the runner
    starts the command on the strength of the reservation.**
    `handleReserve` -> `respondWithReservedJob` -> `resetJobForReservation`
    sets `ReservedBy`, the runner's host and pid, and `State = reserved` on the
    in-memory job only. The first write of the run is `handleStart`'s durable
    one, which the runner can only send after it has started the command.
    `260917-start-durability.md` made that write durable before the ack, but
    the window from reservation to that commit stayed open. A manager killed in
    it leaves the job's older record on disk: the Add-time one (`State` empty,
    `ReservedBy` zero), or that of an earlier attempt. Then:
    1. `recoveredItemDef` only puts `JobStateRunning` into the Run sub-queue,
       so the job goes to Ready.
    2. The live runner's retried `Started` (`retryStartReport`) and its touches
       reach `getij(cr, true)`, find the item outside Run, and get `ErrBadJob`.
       These are the "bad job" jtouch lines.
    3. Its final report reaches `getijForReport`, which accepts a Ready item
       but finds `ReservedBy` is not this client, and returns `ErrMustReserve`.
       These are the jarchive and jrelease lines. `isDefinitiveReject` makes
       the runner give up and discard the completed work.
    4. The job is on the ready queue, so the next runner scheduled for it runs
       it again. That is the second run at 00:13:30.

    The window is the reserve-to-start-commit time: the runner's setup plus a
    best-effort drain (`archive fold` shows mean 155ms and max 665ms that
    minute). Portal jobs run for a few seconds each behind a 150-300 slot
    limit, so a handful per crash is the expected count.

  - The soak's database was not kept, so the recorded state of those keys
    could not be read back. The error signatures, though, are exactly what the
    red test's manager returns for a runner in this position (a temporary
    probe, removed afterwards):

    ```text
    started: jobqueue jstart(322d300e...): bad job (not in queue or correct sub-queue)
    touch: jobqueue jtouch(322d300e...): bad job (not in queue or correct sub-queue)
    archive: jobqueue jarchive(322d300e...): you must Reserve() a Job before passing it to other methods
    ```

  - Red command (deterministic, about 8s), all `OS_*` unset:

    ```bash
    CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -v \
      -run '^TestReserveDurability$'
    ```

    The test adds a job, reserves it, and takes bolt's snapshot of committed
    state the moment `Reserve()` returns. It starts the command as the runner
    would, crashes the manager onto that snapshot, and restarts it. A fresh
    runner then reserves and executes whatever it is given, and the command's
    marker file must record one run. The original runner, reconnected with the
    same client id, must then have its `Started`, `Touch` and `Archive`
    accepted. Before the fix (exit status 1):

    ```text
    Line 206:
    Expected: 1
    Actual:   2
    (Should equal)!

    --- FAIL: TestReserveDurability (0.63s)
    ```

    Two runs of the command, the first still alive: the double run itself.

  - Fix:
    - `jobqueue/serverCLI.go`: `respondWithReservedJob` calls the new
      `persistReservation`, which writes the reserved job with
      `updateJobAfterChangeDurable` before the reply is built. It waits for
      the next coalesced best-effort drain, as `handleStart` already does, so
      concurrent reservations share one commit. It holds no server, queue or
      job lock while waiting, and the request timeout is at least
      `ClientMinRequestTimeout` (60s). A failed write is logged at error and
      the job is still handed out, which is the old behaviour. Refusing
      instead would leave the item reserved to a runner that never got it.
    - `jobqueue/server.go`: the new `recoversIntoRun` sends a recovered
      `JobStateReserved` job to the Run sub-queue, like a running one, when
      the reservation recorded the runner's host and pid. It gets its
      scheduler group recomputed, its limit groups re-incremented and
      `scheduler.Recover` called for its host, as `recoverRunningJob` does for
      running jobs. The runner's reports are then accepted. If the runner died
      too, the TTR lapses, `ttrCallback` marks the job lost, and confirm-dead
      checks the runner pid recorded at reserve. A dead runner means the job
      is re-run (`TestReserveDurabilityDeadRunner`). A reservation with no pid
      (an old client) could never be confirmed dead, so it is recovered onto
      the ready queue as before rather than parked forever
      (`TestRecoversIntoRun`).
    - `jobqueue/reserve_durability_test.go`: the three tests above.

  - Other directions considered and rejected:
    - *Hold runner RPCs until recovery has enqueued everything.* It already
      works this way, as shown above.
    - *Accept a report for a recovered Ready job nobody has reserved since.*
      This is the "reconcile on touch" option `260917-start-durability.md`
      ruled out. The job is still on the ready queue, so a new runner can take
      it before the old runner's first touch (up to `ClientTouchInterval`, 15s,
      later). That makes a double run less likely, not impossible.
    - *Runner retries a rejected archive.* The job has been re-queued, so a
      retry cannot stop the second run.

  - Cost: one more coalesced live-bucket write per run (reserve, start, exit),
    and reserve latency grows by the time to the next drain. Reservations that
    arrive together share a commit, as starts already do.

  - Not fixed, and not new: if the manager dies after the reservation commits
    but before the reply reaches the runner, the runner never gets the job but
    its pid is still alive. The recovered job then stays parked lost until
    `LostRunnerBackstop` (1h), which kills that runner pid and re-runs the job.
    This is the same outcome as a reserve reply lost to a client timeout today,
    and the window is now the time to send one reply rather than the time to a
    start commit.

  - Reserve latency, measured in review with a throwaway test (not kept): N
    clients each made 10 concurrent `Reserve()` calls against one local manager.
    For the "slow DB" rows, a 100ms sleep before each best-effort commit stood in
    for the 80-100ms commits a large freelist gave the soak.

    | commit    | persist | clients | reserves/s | p50   | p99   |
    | --------- | ------- | ------- | ---------- | ----- | ----- |
    | local     | no      | 200     | 15170      | 10ms  | 30ms  |
    | local     | yes     | 200     | 6691       | 25ms  | 50ms  |
    | +100ms    | no      | 200     | 17195      | 7ms   | 41ms  |
    | +100ms    | yes     | 1       | 10         | 105ms | 107ms |
    | +100ms    | yes     | 50      | 237        | 210ms | 217ms |
    | +100ms    | yes     | 200     | 918        | 215ms | 238ms |

    A reservation waits for at most the drain already in flight and then its
    own, so about two commits. Throughput grows with the number of runners
    reserving, because they share a drain. It is not bounded by one commit per
    reserve. Each run now spends about four commit times waiting on the manager
    (reserve and start). That is under 1s at 100ms commits, far inside the
    60s request timeout.

  - With the fix, the red command passes (`--- PASS: TestReserveDurability
    (7.85s)`), as do `TestReserveDurabilityDeadRunner` and
    `TestRecoversIntoRun`.

  - Gates, all `OS_*` unset: `make lint` gives `0 issues.`; `make test` gives
    `747 passed · 21 skipped · 31 packages · 4m14s PASSED`;
    `CGO_ENABLED=1 make race` gives
    `747 passed · 20 skipped · 31 packages · 5m4s PASSED`.

- [x] **A best-effort commit stalled past the runner's 60s request timeout
      would fail every reservation in flight.** Found in review of f5024d51.
      Before that commit, reservations did not wait on the DB at all. A runner
      that timed out never has the job, but the manager holds it as reserved to
      that runner. Its TTR lapses, and the runner is still alive, so the job is
      parked lost until `LostRunnerBackstop`. Round 1 of the soak saw 41-65s
      commit stalls. #632 fixed that cause, but others may exist.

  - Red command, all `OS_*` unset:

    ```bash
    CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -v \
      -run '^TestReserveDurabilityStalledWrite$'
    ```

    The test holds bolt's single write transaction, which stalls every drain,
    for 5s. It sets `ReserveWriteWait` to 300ms and asserts that `Reserve()`
    returns the job in less than 300ms plus 2s, that the warning is logged, and
    that the job then runs once. Before the fix (exit status 1):

    ```text
    Line 372:
    Expected '5.003087395s' to be less than '2.3s' (but it wasn't)!
    --- FAIL: TestReserveDurabilityStalledWrite (5.40s)
    ```

  - Fix:
    - `jobqueue/server.go`: new `ServerTimings.ReserveWriteWait`. Its default
      is `serverReserveWriteWait()`, the lesser of 10s and a quarter of
      `ClientMinRequestTimeout`, so 10s today. Tests set it low.
    - `jobqueue/db.go`: new `updateJobAfterChangeDurableWithin`. It returns
      `errDurableWriteWaitExpired` when the wait runs out. The write stays
      queued, and the waiter channel is buffered, so the drain's later reply
      never blocks the writer.
    - `jobqueue/serverCLI.go`: `persistReservation` waits at most
      `ReserveWriteWait`. When the wait runs out it logs a warning, "reservation
      not yet recorded on disk, handing the job out anyway; a manager crash
      before the job's start is recorded may run it twice", and hands the job
      out. That is the behaviour from before f5024d51, for that one
      reservation.

- [x] **`beBatch.apply` wrote coalesced changes before exit ops, whatever order
      they arrived in.** Found in review of f5024d51. If one drain holds both
      the previous run's release (an exit op) and the job's next reservation (a
      change), the stale release was written last, and the job recovered to
      the ready queue, as before the reservation fix. A released job waits at
      least `ReleaseDelayMin` (30s) before it can be reserved again, so this
      needed a drain that had been pending for 30s. That is exactly a stalled
      commit, as in the item above.

  - Red command, all `OS_*` unset:

    ```bash
    CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -v \
      -run '^TestBestEffortDrainKeepsArrivalOrder$'
    ```

    The test queues a release exit and a reservation change for one job, in
    both orders, without waking the writer. It then drains once on the test
    goroutine and decodes the live record. Before the fix (exit status 1):

    ```text
    reserve_durability_test.go:426: live record state is "delayed", want "reserved"
    --- FAIL: TestBestEffortDrainKeepsArrivalOrder/a_release_then_a_reservation_leaves_the_reservation (0.01s)
    --- PASS: TestBestEffortDrainKeepsArrivalOrder/a_reservation_then_a_release_leaves_the_release (0.00s)
    ```

  - Fix, `jobqueue/db.go`: every queued change and exit op gets an arrival
    number (`db.beSeq`, taken under `beMu`). Changes are kept as `beChange`
    values that hold it. `beBatch.apply` skips a change that arrived before one
    of the same job's exit ops in the batch. An exit op that arrived before the
    job's change still refreshes std and the fail-stat, but no longer rewrites
    the live record (`jobExitData.update` takes `writeLive`). A job's live
    record is therefore whichever arrived last. The new `enqueueChangeLocked`
    and `enqueueExitLocked` are the only places that queue a change or an exit
    op. `queueUnkickedBestEffortChange` in `start_durability_test.go` now uses
    the first of them, instead of writing `beChanges` itself.

  - Gates for both items, all `OS_*` unset: `make lint` gives `0 issues.`. The
    durability, recovery, best-effort and reliable2/3/4 tests pass under
    `-race`:

    ```bash
    CGO_ENABLED=1 go test -race -tags netgo --count 1 ./jobqueue -run \
      'TestReserveDurability|TestRecoversIntoRun|TestBestEffort|TestStartDurability|TestReliable4|TestReliable2|TestReliable3|Recovery|Recovered|WriteStorm|BestEffort'
    ```

    That gives `ok ... 294.841s`. `make test` gives
    `749 passed · 21 skipped · 31 packages · 3m23s PASSED`.
