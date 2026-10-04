# Reject a zero ClientID on runner reports (2026-10-04)

- Branch: `reject-clientid0-f6121ca8`
- Base: `origin/develop` at `3a97de47` (#670)
- Worktree: `../wr-clientid0`
- Queue owner: `checklist-tidy-8e94211c`, `.docs/bugfixes/261004-checklist-tidy-8e94211c.md`
  (Backlog and Delivery queue item 4)

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2`, `GOCACHE` outside the home
directory and `env -u WR_LSF_TEST_KEY`: targeted `go test -tags netgo` runs,
plain and `-race`; `golangci-lint run ./jobqueue/`; `cleanorder -min-diff` on
the edited Go files.

- [x] Reject ClientID 0 on release, bury, delay and ready.
  `260930-runner-report-followups.md` item 3, "Noted, not fixed": a hand-made
  request with a zero ClientID can release or bury a job that never ran
  (ReservedBy zero).
  - Source: `261004-checklist-tidy-8e94211c.md` Backlog, "QUEUED (branch 4
    below)".
  - Methods: delay and ready are not request methods but outcomes of
    `jrelease`. The manager compares `job.ReservedBy` with `cr.ClientID` for
    five methods, all through two helpers in `jobqueue/serverCLI.go`:
    `getij` (`jstart`, `jtouch`) and `getijForReport` (`jarchive`,
    `jrelease`, `jbury`, plus `handleReportOnOwnBuriedJob`, which `jrelease`
    and `jbury` reach when `getijForReport` returns ErrBadJob). The archive
    path's `markJobComplete` and the release path's `releaseJob` check the
    same client again. All five are covered.
  - Red: `go test -tags netgo -count 1 ./jobqueue -run
    TestZeroClientIDReportsRefused -v` exits 1 on `3a97de47`:

    ```text
    a jstart request with the zero client ID is refused and leaves the job alone ✘
    a jtouch request with the zero client ID is refused and leaves the job alone ✘
    a jarchive request with the zero client ID is refused and leaves the job alone ✘ (log: archive fold archives=1)
    a jrelease request with the zero client ID is refused and leaves the job alone ✘
    a jbury request with the zero client ID is refused and leaves the job alone ✘
    the client that reserves it can still release it, then bury it ✔
    Expected 'jobqueue jstart(...): bad job (not in queue or correct sub-queue)' to contain substring 'bad request (missing arguments?)'
    Expected 'jobqueue jtouch(...): bad job (not in queue or correct sub-queue)' to contain substring 'bad request (missing arguments?)'
    Expected '<nil>' to NOT be nil (but it was)!   (x3: archive, release, bury accepted)
    ```

  - Cause: a job no one has reserved has the zero `ReservedBy`, so a request
    with the zero client ID passed the reserver check. The zero-ID archive,
    release and bury of a ready job succeeded: the archive completed a job
    whose `StartTime` was set, as a job sent back to run again has. `jstart`
    and `jtouch` need the item in the run sub-queue, so on a ready job they
    already failed, with ErrBadJob; they are covered in case a run item ever
    has the zero `ReservedBy`.
  - Fix: `getijForReport` returns ErrBadRequest for a zero client ID with its
    missing-key check, before any lookup or state change, so the own-bury
    re-send path is not reached either. `getij` returns ErrBadRequest right
    after the queue lookup, so a missing key during recovery is still
    ErrRecovering, as `TestReliable2RecoveryWindowReturnsRecovering` expects
    of a `getij` call with no client ID. `handleReserve` already gives a zero
    client ID the same ErrBadRequest.
    The shared item-to-job and reserver check moved to `reservedJob`, used by
    both helpers, which keeps `getijForReport` within the gocyclo limit.
    Files: `jobqueue/serverCLI.go`, `jobqueue/zero_client_id_test.go`.
  - Callers checked: `jobqueue.Client` sends all five methods through
    `encodeAndSend`, which stamps `cr.ClientID = c.clientid`, and every
    `Client` comes from `newClientForSocket`, which sets a fresh
    `uuid.NewV4()`. Nothing else in the repository sends these methods: `cmd/`,
    REST and the web UI use other methods (such as `jkill`, `jmod`, `jkick`,
    `jdel`) or call server internals directly, and
    the manager's internal releases call `releaseJob` without a request
    (the zero `reporter` there means the manager itself and is untouched).
    Tests that send these methods directly (`client_payload_test.go`,
    `live_jtouch_test.go`, `archive_stall_test.go`, `moved_on_runner_test.go`)
    set a real client ID.
  - Tests: `TestZeroClientIDReportsRefused` sends each of the five methods
    with the zero client ID, through a real client connection, for a ready
    job that was never reserved but carries a start time. Each is refused with
    ErrBadRequest, and the job's state, attempts, retries left, fail reason
    and start time are unchanged. The real reserver can still release and
    then bury the job.
  - Mutants (scratch copy): dropping the `getijForReport` check fails the
    jarchive, jrelease and jbury leaves; dropping the `getij` check fails the
    jstart and jtouch leaves.
  - Gates: the new test passes plain (exit 0) and with `-race` (exit 0);
    `TestReliable2|TestArchiveStall|TestServerRejects|TestLiveJTouch|TestMovedOn|TestArchiveBeforeStart|TestClientPayload|TestReliable4|TestLostJob|^TestJobqueueBasics$|^TestJobqueueExecutionAndDependencyScenarios$|^TestJobqueueModify$`
    passes (exit 0); `golangci-lint run ./jobqueue/` 0 issues; `cleanorder
    -min-diff` placed `reservedJob`.
  - CHANGELOG: no entry. wr's own clients always send a client ID, so no
    user of wr sees a difference. The request protocol is internal to the
    Go client, and a third-party client sending no client ID gets an error
    only where it was acting on a job it never reserved.
