# Bugfixes 2026-09-24

Filename deliberately not `260924-1.md`, so that it cannot collide with a
sibling branch's sequence number.

- [x] A successfully completed job keeps its stdout and stderr in its
      `bucketJobsComplete` record for ever.

  ## Decision

  The repo owner has decided that a successful job keeping its output in the
  database is a bug, and that successful jobs should stop keeping it. This
  reverses `.docs/issue-98/spec.md` story D1 acceptance test 2, which required
  a job archived complete to report its final `StdOut`/`StdErr`.

  An earlier investigation on branch `fix-complete-job-std-storage`
  (`.docs/bugfixes/260917-complete-job-std.md`) traced every path below and
  concluded it was not a defect because the output is served and specified.
  Its facts stand; only that conclusion was overruled.

  ## Diagnosis

  Where the output is stored:

  - `Execute` (`jobqueue/client.go`) always sends `Stdout`/`Stderr` in the end
    state, whatever the exit code.
  - On success, `markJobComplete` (`jobqueue/serverCLI.go`) copied them onto
    the `*Job`, and `db.archiveJob`, called via `archiveCompletedJob`, encoded
    that whole `*Job`, output included, into `bucketJobsComplete`.
  - The `prefixSuffixSaver` keeps each stream's first and last 4096 bytes
    (`stdSaverBytes`), and `compressStd` zlib-compresses it, so a stream costs
    at most about 8KiB and a job about 16KiB. About 10KB per chatty job was
    measured.
  - The std buckets were already right. `archiveJobTx` deletes the job's
    `bucketStdO`/`bucketStdE` entries, and the completion path never writes
    them: only a release or bury reaches `updateJobAfterExit`'s `updateStd`.

  Who served the kept output. The record's `StdOutC`/`StdErrC` came back on
  every decoded complete job, whatever `getStd` the caller passed, because
  `jobPopulateStdEnv` neither overwrites nor clears them for a successful job
  (`jobCouldHaveStd` is false):

  - The web UI's rep-group details view: `sendJobDetails` ->
    `getDBJobsByRepGroup` -> `db.decodeArchivedJob` -> `db.decodeJob`, then
    `job.ToStatus()` decompresses them into `JStatus.StdOut`/`StdErr`. The web
    UI's single-job lookups and subscription status updates read them too.
  - Lookups by key via `getJobsByKeys` -> `completeJobsByKeys`, and rep-group
    lookups via `getJobsByRepGroup`: REST `/rest/v1/jobs/<key>?std=true` and
    `/rest/v1/jobs/<repgroup>?std=true`, and `handleGetByKeys` and the
    rep-group and recent handlers behind the Go APIs.
  - `wr status -o json`: `statusOutputGetsStd` is true for json, and
    `cmd/status.go` JSON-encodes `ToStatus()`, so it printed a successful
    job's output however the jobs were chosen (`-i`, `-f`, `-l`, `--recent`).
  - Any Go API that returns a completed job: the `jobqueue.Client` getters
    (`GetByEssence(s)`, `GetByRepGroup(Match)`, `GetRecent`, `AddAndWait*`)
    and the `client` package (`GetJobByKey`, `SubmitJobsAndWait`,
    `WaitForJobs`, `WaitForRunning` for a job that has already completed,
    `FindJobsByRepGroup*`).
  - `wr add --sync`, whose `printSynchronousJobOutput` (`cmd/add.go`) prints
    whatever output the re-fetched job carries. Since v0.37.0 it has printed a
    successful command's output, though its help says it outputs the head and
    tail of STDOUT and STDERR "if it had failed".
  - Not `wr status -o d`, which prints std only when
    `showextra && job.Exitcode != 0` (`cmd/status.go`), and is unchanged.

  The earlier investigation named only the web UI and REST; the rest were found
  while running the suite and in review.

  The kept output was new in v0.37.0. v0.36.5's `jarchive` applied the end
  state through `Job.updateAfterExit`, which never touched `StdOutC`/`StdErrC`,
  and its `jobqueue_test.go` case "The stdout/err of jobs is only kept for
  failed jobs" asserted `""` for a successful job fetched by `GetByEssence`.
  "Add job subscriptions (#503)" started copying the end state's output in
  `jarchive` and flipped that case. This fix restores the v0.36.5 behaviour.

  While a job runs, an issue-98 live snapshot in a touch puts its current
  output tail on `StdOutC`/`StdErrC` (`applyLiveSnapshot`). Only dropping the
  copy from the end state would therefore archive that stale tail instead.

  ## Red command

  `jobqueue/complete_job_std_test.go`, `TestCompleteJobKeepsNoStd`, drives a
  real server and client. The first case runs a successful and a failing job
  of the same shape through `Execute`, each printing `COMPLETEJOBSTDOUTMARKER`
  to stdout and `COMPLETEJOBSTDERRMARKER` to stderr. It decodes the successful
  job's `bucketJobsComplete` record and asserts `StdOutC`/`StdErrC` are empty,
  then fetches the failed job with `GetByEssence(_, true, false)` and asserts
  both markers come back. The second case touches a started job with a live
  tail, checks the server's job holds it, archives with a distinctive final
  output and asserts the record holds neither.

  ```bash
  CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestCompleteJobKeepsNoStd
  ```

  Before the fix:

  ```text
    * jobqueue/complete_job_std_test.go
    Line 104:
    Expected COMPLETEJOBSTDOUTMARKER to be empty (but it wasn't)!

    * jobqueue/complete_job_std_test.go
    Line 104:
    Expected COMPLETEJOBSTDOUTMARKER to be empty (but it wasn't)!

  40 total assertions

  --- FAIL: TestCompleteJobKeepsNoStd (0.30s)
  ```

  With only the end-state copy removed from `markJobComplete`, the first case
  passed and the second still failed on the live tail:

  ```text
    Line 104:
    Expected COMPLETEJOBLIVETAILMARKER
     to be empty (but it wasn't)!

  --- FAIL: TestCompleteJobKeepsNoStd (0.51s)
  ```

  After the fix:

  ```text
  55 total assertions

  --- PASS: TestCompleteJobKeepsNoStd (0.48s)
  ```

  ## Seam

  `Job.applySuccessfulEndStateLocked` (`jobqueue/serverCLI.go`) now sets
  `StdOutC` and `StdErrC` to nil, and `markJobComplete` no longer copies the
  end state's output. That function already sets every field a successful end
  state determines, under the job's write lock, and `markJobComplete` is its
  only caller, reached only from `handleArchive`. Clearing there removes the
  final output and any live tail in one place, before `db.archiveJob` encodes
  the job.

  Clearing inside `db.archiveJob` instead was rejected. It would need a write
  lock and a save/restore around the encode, and would leave the in-memory job
  holding output. The completion push update, which `q.Remove`'s change
  callback builds from that in-memory job, would then show output that a
  refresh of the same job no longer shows. Clearing it on the job itself gives
  every surface the same answer.

  Released, buried and failed jobs do not pass through `markJobComplete`, and
  nor does a lost job that is released or buried. They keep going through
  `updateJobAfterExit` -> `updateStd` into the std buckets and are read back
  by `jobPopulateStdEnv`, unchanged. The first test case pins the failed job's
  output. A job parked lost whose owner then reports success does pass through
  `markJobComplete` and, like any successful job, keeps no output. Its lost
  handling is untouched: `markJobComplete` still leaves `Lost` set, so its
  removal is still counted lost->complete.

  The `Job.StdOut`/`StdErr` doc comments already said `StdOutC` is only
  populated if the Job's Cmd ran but failed, which is now true.

  ## Existing databases

  Records archived before this change keep their output and still decode: the
  encoded shape of a `Job` is unchanged, and the two fields are simply nil in
  new records. `TestReliable2DBCompatOpen` and the recovery-window tests in
  `jobqueue/reliable2_dbcompat_test.go` pass. They pin that a DB written by
  pre-removal reliable2 code (`jobqueue/testdata/dbcompat/db.golden`) opens
  without error, that its two archived jobs count as complete, that its two
  incomplete jobs recover and are reservable, that recovery-window calls get
  `ErrRecovering`, and that the one-time index rebuilds do not re-run. They do
  not pin std content: the fixture's complete jobs are `true 1` and `true 2`,
  which print nothing.

  ## Test changes

  - New: `TestCompleteJobKeepsNoStd`, described above.
  - `TestStatusDetailsLiveCompatibility` (`jobqueue/serverWebI_test.go`), D1
    test 2: the completed job's `StdOut`/`StdErr` are now asserted `""`
    instead of `"final\n"`/`"done\n"`. The `Exited`, `PeakRAM` (654) and
    `CPUtime` (8) archive-value assertions and the `SSHCommand` assertion are
    unchanged.
  - `jobqueue/jobqueue_test.go`, `TestJobqueueExecutionAndDependencyScenarios`:
    the case #503 renamed "The stdout/err of archived jobs is retained, ..."
    gets back its v0.36.5 title "The stdout/err of jobs is only kept for
    failed jobs, ..." and its v0.36.5 assertions that the successful job
    fetched with `GetByEssence(_, true, false)` has `""` stdout and stderr. The
    same job's output read from the runner's own copy after `Execute`, which
    proves the cwd, HOME and TMPDIR, is unchanged, as are all the failed-job
    assertions.
  - `jobqueue/subscription_test.go`, `TestClientAddAndWait`, "AddAndWait
    returns a successful complete job with exit code 0": the job is archived
    with output as before, and its re-fetched stdout and stderr are now
    asserted `""`. The buried-job case beside it still asserts its stderr.
  - `client/client_test.go`, `TestSchedulerGetJobByKey`: "GetJobByKey fetches
    stdout and stderr for complete jobs when requested" is now "GetJobByKey
    fetches no stdout or stderr for a successful complete job" and asserts
    `""`. `GetJobByKey(_, true, _)` fetching a buried job's output stays
    covered by `TestSchedulerWaitForJobs`, which uses it.
  - `client/client_test.go`, `TestSchedulerSubmitJobsAndWait`: the complete
    job's stdout and stderr are asserted `""`. The buried job's stderr
    assertion is unchanged.
  - `client/client_test.go`, `TestSchedulerWaitForJobs`: "WaitForJobs returns
    already terminal jobs with stdout and stderr" is now "WaitForJobs returns
    already terminal jobs, with output only for the buried one", and the
    complete job's stdout and stderr are asserted `""`. The buried job's
    stderr assertion is unchanged.
  - `cmd/add_test.go`: `TestSynchronousAddPrintsStdoutAndExitsZero` ("wr add
    --sync prints stdout and exits zero for a successful job") faked a
    successful job carrying output, which the manager can no longer return. It
    is now `TestSynchronousAddPrintsNoOutputAndExitsZero` ("wr add --sync
    prints no output and exits zero for a successful job"): its helper returns
    a successful job with no output, and it asserts blank stdout and stderr and
    exit code 0. The warning test's fake `GetByEssence` likewise returns its
    successful job without the `"sync complete"` output. The shared
    `synchronousAddTestClient` fakes, which build output from their `stdout` and
    `stderr` fields, are unchanged, so the buried case still returns and prints
    its stderr.
  - `.docs/issue-98/spec.md` D1 test 2 carries a dated note that the
    requirement was reversed, pointing here.

  ## Gates

  With `OS_*` unset and `master` at `b2f0ff9`:

  - `make lint`: **0 issues.**
  - `make test`: **PASSED - 669 passed, 20 skipped, 29 packages, 6m29s.** The
    known `TestSuiteTempReaping`/`TestSuiteLeavesForeignJobDirsAlone` port
    failure did not occur, so the PR #605 cherry-pick was not needed. The first
    run, before the five tests above were updated, failed exactly those five.
  - `CGO_ENABLED=1 make race`: **PASSED - 669 passed, 19 skipped, 29 packages,
    9m32s**, at a 1-minute load of 2.7.
  - `cleanorder -min-diff` is a no-op on every edited Go file.

  After review (CHANGELOG consumer list, this doc's consumer list, the
  `cmd/add_test.go` fakes and the `applySuccessfulEndStateLocked` comment):

  - `make lint`: **0 issues.**
  - `CGO_ENABLED=0 go test -tags netgo -count=1 ./cmd/`: **ok**, 91.6s.
  - `make test`: the first run failed only `TestJobqueueRunnerKillRequests`
    (`runner_lifecycle_test.go:557`, the killed job ran to completion). That
    test covers kill via touch, not output, and `.docs/bugfixes/260916-1.md`
    records it flaking before. The re-run, with no other suite running, was
    **PASSED - 669 passed, 20 skipped, 29 packages, 6m14s.**
  - `CGO_ENABLED=1 make race`: **PASSED - 669 passed, 19 skipped, 29 packages,
    9m10s**, at a 1-minute load of 0.45 and with no other suite running.

- [x] Records already in `bucketJobsComplete` keep the output stored by
      v0.37.0 to v0.37.2, so the space is never recovered.

  ## Decision

  The repo owner wants `wr manager compact` to strip that stored output, once
  per database, with no flag. Knowing whether a database has had it done needs
  a schema version, which wr did not have: its one-time upgrades key off
  whether a bucket exists.

  ## Design

  - `jobqueue/db_schema.go` adds a `meta` bucket whose `schemaVersion` key
    holds an 8-byte big-endian version. A database with no version is version
    0. `dbSchemaVersionNoCompleteStd` (1) means no complete record holds
    `StdOutC` or `StdErrC`, and is `currentDBSchemaVersion`. The comment beside
    the constants says how to add a version.
  - `initDB` stamps a database it creates (`!openedExistingDB`) with the current
    version. It leaves an existing unversioned database unstamped, since
    unversioned means not yet cleaned. A database recreated from a backup is
    existing, so it carries whatever version the backup has.
  - `CompactDBFile` now returns a `CompactStats` (sizes before and after,
    whether the strip ran and succeeded, how many jobs it stripped, and how
    many complete records it could not read, with the first 10 of their keys)
    in place of its two sizes. `compactBolt` reads the source version. At
    version 1 or later it calls `bolt.Compact` exactly as before, with no
    decoding. Below 1 it calls `compactStrippingStd` (`jobqueue/db_compact.go`).
  - `compactStrippingStd` mirrors bbolt's `Compact`, which has no hook to change
    a value: the same walk order, bucket sequences copied, `FillPercent` 1.0 on
    every bucket written to, and a commit whenever the next key and value would
    take the transaction over `compactTxMaxSize`. Only values directly in the
    top-level `bucketJobsComplete` are touched. Each is decoded into a `Job`
    with a fresh `codec.BincHandle`, as `initDB` builds the db's. A record with
    no output is copied as its original bytes. Otherwise both fields are set to
    nil and the job is re-encoded as `archiveJob` encodes it. `decodeJob` is
    not used, because its `dropImpossibleCleanups` would change another field.
    It stamps the destination with version 1 in the final transaction.
  - It strips every complete record. Only successful jobs reach
    `bucketJobsComplete`: `archiveJobTx` is its only writer, reached only via
    `handleArchive`, and `canCompleteFromEndState` requires exit code 0.
    v0.36.5's `jarchive` refused a non-zero exit too. So no record there can
    need its output kept.
  - v0.36.0 to v0.36.5 (from #486) could also leave an earlier failed
    attempt's output in a complete record, when a job subscribed to in the web
    UI failed, was retried and then succeeded. Stripping it is still correct,
    because the job finally succeeded.
  - A record that cannot be decoded is copied as its original bytes, counted,
    and its key reported. Nothing at manager startup decodes complete records,
    so a database with one corrupt complete record runs today; failing the
    strip on it would leave compact, wr's only offline way to recover space,
    failing for ever. The destination is still stamped version 1, since output
    in a record nothing can read cannot be served.
  - The strip happens during the copy into the temporary file, so compact's
    guarantee that an error leaves the original untouched still holds.
  - `wr manager compact` adds the stripped count to its existing line when it
    is above 0, and logs any unreadable records at warn level, with their
    count and up to 10 keys. Its help says it removes output stored by 0.37.0
    to 0.37.2 the first time it compacts a database created by wr 0.37.2 or
    earlier. The CHANGELOG entry above says running it once recovers the
    space.

  Accepted edge case: after a database is stamped, downgrading to a wr that
  stores a successful job's output would store it again under a version 1
  stamp, and a later compact would not strip it. That is rare, and accepted.
  `TestDBCompactStripsOldCompleteStd` pins this: a version 1 database with
  output in its complete records is copied verbatim.

  ## Red command

  ```bash
  CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run 'TestDBSchemaVersionOnOpen|TestDBCompactStripsOldCompleteStd|TestDBCompactGoldenFixture'
  ```

  Before the change, with `CompactStats` and the schema version reader already
  in place so the tests compiled, the new db was not stamped, so there was no
  meta bucket to remove to make an unversioned one, and the golden db was not
  stripped:

  ```text
    Line 64:
    Expected: true
    Actual:   false
  --- FAIL: TestDBSchemaVersionOnOpen (0.01s)
    Line 447:
    Expected: nil
    Actual:   'bucket not found'
  --- FAIL: TestDBCompactStripsOldCompleteStd (0.06s)
    Line 233:
    Expected: true
    Actual:   false
  --- FAIL: TestDBCompactGoldenFixture (0.01s)
  ```

  After:

  ```text
  --- PASS: TestDBSchemaVersionOnOpen (0.01s)
  --- PASS: TestDBCompactStripsOldCompleteStd (0.16s)
  --- PASS: TestDBCompactGoldenFixture (0.01s)
  ```

  ## Tests

  In `jobqueue/db_compact_std_test.go`:

  - `TestDBSchemaVersionOnOpen`: `initDB` stamps a new db with version 1, and
    reopening an existing db whose meta bucket was removed leaves it without
    one.
  - `TestDBCompactStripsOldCompleteStd`, on an unversioned db written through
    `initDB` and `db.archiveJob` with `StdOutC` and `StdErrC` set, as pre-#608
    code wrote them, plus live jobs, std bucket entries, a complete job with no
    output, and a nested bucket with a sequence:
    - `CompactDBFile` strips all 20 records, reports it, stamps version 1 and
      shrinks the file. Every value outside `jobscomplete` and every bucket
      sequence is unchanged, the complete record with no output is
      byte-identical, and each stripped record decodes equal to its original
      with the two fields nil. The current code reads them back with empty
      output. A second compaction does no strip pass.
    - `compactStrippingStd` with a 1-byte `txMaxSize` commits before every key
      and produces the same data.
    - With 12 undecodable complete records, `CompactDBFile` succeeds, strips
      the other 20, copies each bad record byte for byte, reports 12
      unreadable with the first 10 keys, and stamps version 1.
    - With a malformed schema version, `CompactDBFile` fails, the original
      file's bytes are unchanged, and no temporary file is left.
    - On a version 1 db with output in its complete records, `CompactDBFile`
      decodes nothing (counted by the `compactStdDecodeObserver` seam), reports
      no strip, and copies every value and sequence verbatim.
  - `TestDBCompactGoldenFixture`: a copy of `testdata/dbcompat/db.golden` is
    unversioned, compacts with a strip pass that strips 0 records, is stamped,
    and keeps every other value. The golden file's bytes are unchanged.

  `TestDBCompactRoundTrip` (`jobqueue/db_test.go`) and
  `TestManagerCompactRefusesWhileRunning` (`cmd/manager_test.go`) take the new
  return type. New `TestManagerCompactReportsStrippedOutput` drives the
  command's `Run` with no manager up and asserts its log lines for a strip, a
  strip pass that stripped nothing, and unreadable records.

  Output from a real `wr manager compact` on an unversioned db with 20 jobs to
  strip, then run again, then on another such db with 12 corrupt complete
  records added:

  ```text
  INFO compacted .../wrdemo_development/db: 262.1 kB -> 131.1 kB; removed output stored by older wr versions from 20 completed jobs
  INFO compacted .../wrdemo_development/db: 131.1 kB -> 131.1 kB
  INFO compacted .../wrbad_development/db: 262.1 kB -> 131.1 kB; removed output stored by older wr versions from 20 completed jobs
  WARN could not read 12 completed jobs while removing their stored output, so copied them unchanged: corrupt00, corrupt01, corrupt02, corrupt03, corrupt04, corrupt05, corrupt06, corrupt07, corrupt08, corrupt09 (and 2 more)
  ```

  ## Gates

  With `OS_*` unset and master at `b2f0ff9`:

  - `make lint`: **0 issues.**
  - `make test`: the first run failed only `TestJobqueueRunnerKillRequests`
    (`runner_lifecycle_test.go:557`), the known kill-path flake. The re-run was
    **PASSED - 679 passed, 20 skipped, 29 packages, 6m14s.**
  - `CGO_ENABLED=1 make race`: **PASSED - 679 passed, 19 skipped, 29 packages,
    9m29s**, at a 1-minute load of 1.5 and with no other suite running.
  - `cleanorder -min-diff` was run on the new files and the edited test files.
    On `jobqueue/db.go` it wants to move about 2,500 lines of existing code even
    at `HEAD`, so that file was left in its existing order.

  After review (unreadable records copied rather than failing, the rollback
  defer, help text, log lines, `OutputStripped` set only on success):

  - `make lint`: **0 issues.**
  - `TestDBSchemaVersionOnOpen`, `TestDBCompactStripsOldCompleteStd`,
    `TestDBCompactGoldenFixture`, `TestDBCompactRoundTrip`: **ok.**
  - `make test`: **PASSED - 679 passed, 20 skipped, 29 packages, 6m21s.**
  - `CGO_ENABLED=1 make race`: **PASSED - 679 passed, 19 skipped, 29 packages,
    9m23s**, started at a 1-minute load of 3.0 with no other suite running.
