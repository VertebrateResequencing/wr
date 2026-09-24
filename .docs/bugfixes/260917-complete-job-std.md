# Bugfixes 2026-09-17

- [x] Reported: a successfully completed job's stdout and stderr are stored
      permanently in its `bucketJobsComplete` record, where nothing can read
      them back, inflating the db by ~10KB per job for ever.

  **Not a defect.** The mechanism in the report is exactly right and the
  measurement is real, but the premise that the stored output is unreachable is
  wrong. That record is the only place a successful job's output is kept, and
  the status web UI and the REST API both serve it from there. Removing it
  would delete a specified, tested feature. No code behaviour was changed.

  ## What the report established, and what it missed

  Verified, all three steps as described:

  - `Execute` (jobqueue/client.go) always sends `Stdout`/`Stderr` in the end
    state, whatever the exit code.
  - `markJobComplete` (jobqueue/serverCLI.go) checks
    `canCompleteFromEndState`, which requires `Exitcode == 0`, and only then
    copies them onto the `*Job`.
  - `db.archiveJob` (jobqueue/db.go), called by `archiveCompletedJob`, then
    encodes that whole `*Job`, those two `[]byte` fields included, into
    `bucketJobsComplete`. Nothing clears them.

  Also verified: `jobExitData.updateStd` declines the `bucketStdO`/`bucketStdE`
  write when `e.exitcode == 0 && !e.forceStorage`, `archiveJobTx` deletes both
  keys as it archives, and `jobCouldHaveStd` gates *retrieval from those two
  buckets* on
  `(job.Exited && job.Exitcode != 0) || job.State == JobStateBuried`. The
  completion path never reaches `updateStd` at all: only
  `finalizeReleasedJob`, on a release or bury, calls `updateJobAfterExit`.

  What those three do not say is that a successful job has no output anywhere.
  They say the std buckets do not hold it. The complete record does, and it is
  read straight out of the decoded `*Job` without ever consulting
  `jobCouldHaveStd`.

  `sendJobDetails` (jobqueue/serverWebI.go) asks `getJobsByRepGroup` for the
  rep group's history with `IncludeComplete: true` and `GetStd: true`. The
  archived job comes back through `getDBJobsByRepGroup` by one of two paths,
  depending on the request's `Limit`:

  - With a `Limit`, as the web UI sends: clicking a rep group sends
    `Limit: viewModel.currentLimit`, just set to 1
    (jobqueue/static/js/wr/repgroup-handler.js:62). That builds a
    `completeJobsBudget` and goes `oldestCompleteJobsByRepGroup` ->
    `db.retrieveOldestCompleteJobsByRepGroup` -> `db.decodeArchivedJobs` ->
    `db.decodeArchivedJob` -> `db.decodeJob`.
  - Without a `Limit`, as `TestStatusDetailsLiveCompatibility` sends: no
    budget, so it goes `allCompleteJobsByRepGroup` ->
    `getCompleteJobsByRepGroup` -> `db.retrieveCompleteJobsByRepGroup` ->
    `db.decodeArchivedJob` -> `db.decodeJob`.

  Either way the job carries `StdOutC`/`StdErrC` from the record.
  `jobPopulateStdEnv` leaves them alone: `jobCouldHaveStd` is false, so it does
  not overwrite them with the deleted bucket contents, and it does not clear
  them either. `job.ToStatus()` decompresses them into
  `JStatus.StdOut`/`StdErr`, and `jobqueue/static/status.html` renders them in
  the job details panel under `<!-- ko if: StdOut -->`.

  A lookup by key takes a third path. `getJobsByKeys` falls through to
  `completeJobsByKeys` for a key no longer in the queue, which decodes the
  record with `db.retrieveCompleteJobsByKeys` and passes `jobPopulateStdEnv` a
  literal `false` for `getStd`. That serves REST `/rest/v1/jobs/<key>`, where
  `jobsToStatuses` (jobqueue/serverREST.go) keeps the std fields only for
  `?std=true`.

  This is written down. `.docs/issue-98/spec.md`, story D1 acceptance test 2:

  > Given a live snapshot was applied and the job later archives complete with
  > final stdout `"final\n"` and stderr `"done\n"`, when status details are
  > requested after completion, then `StdOut == "final\n"`,
  > `StdErr == "done\n"`, `Exited == true`, and the final `PeakRAM` and
  > `CPUtime` values are the archive values, not stale live values.

  and its implementation is `TestStatusDetailsLiveCompatibility`
  (jobqueue/serverWebI_test.go:1917), which archives a job with
  `Exitcode: 0, Stdout: compressStd([]byte("final\n"))`, then asks the details
  websocket for the rep group's `JobStateComplete` jobs. By then the job has
  left the queue, so the answer can only come from the complete record. The
  test asserts `completeStatus.StdOut == "final\n"`.

  The line was added by "Add job subscriptions (#503)" and the feature it feeds
  by "Solve #98: add live job introspection (#530)" (#503 for the completion
  push update, which builds `job.ToStatus()` from this same in-memory job at
  `enqueueChangeCallbackSubscriptions`; #530 for the after-the-fact details
  view, which builds it from the record). It is not an oversight of the
  modernisation that moved it.

  The report's own evidence is consistent with this once the surface is named.
  `wr status -o d` shows nothing because the CLI gates the display on its own
  `showextra && job.Exitcode != 0` check (cmd/status.go:596), not on
  `jobCouldHaveStd`, and not because the manager has nothing to give: the same
  job's output over REST is there.

  ## Proof

  The red command as briefed was then used to prove the opposite of what it
  was written for. It drives a real isolated manager with a `local` scheduler,
  runs one successful and one failing job of identical shape, and puts the
  marker only in the output, so the command text cannot be matched by mistake.
  It then asks both user-visible surfaces. Its essential steps, with a
  development deployment whose config sets `managerscheduler: "local"`:

  ```bash
  OUTMARK=$(echo -n "COMPLETEJOBSTDOUTMARKER" | base64)
  ERRMARK=$(echo -n "COMPLETEJOBSTDERRMARKER" | base64)
  printf '%s\n' \
    "echo $OUTMARK | base64 -d; echo $ERRMARK | base64 -d >&2; exit 0" \
    > ok.cmds
  printf '%s\n' \
    "echo $OUTMARK | base64 -d; echo $ERRMARK | base64 -d >&2; exit 1" \
    > bad.cmds
  wr manager start --deployment development
  wr add -f ok.cmds -i stdprobe-ok --deployment development
  wr add -f bad.cmds -i stdprobe-bad --deployment development
  # once stdprobe-ok is complete and stdprobe-bad is buried:
  wr status -i stdprobe-ok -o d --deployment development
  wr status -i stdprobe-bad -o d --deployment development
  curl -sS -k -H "Authorization: Bearer $TOKEN" \
    "https://localhost:$WEB/rest/v1/jobs/$OKKEY?std=true"
  curl -sS -k -H "Authorization: Bearer $TOKEN" \
    "https://localhost:$WEB/rest/v1/jobs/$BADKEY?std=true"
  ```

  `$TOKEN` is the deployment's `client.token`, `$WEB` its `managerweb` port,
  and `$OKKEY`/`$BADKEY` the `Key` from each job's `wr status -o json`. The
  script grepped each output for the markers and printed:

  ```text
  --- CLI: wr status -o d
    PASS  successful job: CLI shows no stdout
    PASS  failed job: CLI shows stdout
  --- REST/web-UI surface: /rest/v1/jobs/<key>?std=true
    PASS  successful job: REST returns StdOut
    PASS  successful job: REST returns StdErr
    PASS  failed job: REST returns StdOut

  successful job JStatus StdOut field: 'COMPLETEJOBSTDOUTMARKER'
  successful job state:                complete exitcode 0

  ALL EXPECTATIONS MET
  ```

  The successful job's REST JSON reads `"State":"complete"`, `"Exitcode":0`,
  `"StdOut":"COMPLETEJOBSTDOUTMARKER"`, `"StdErr":"COMPLETEJOBSTDERRMARKER"`,
  while its `"Cmd"` holds only the base64. The job is gone from the live queue
  and `archiveJobTx` deleted its std buckets, so `bucketJobsComplete` is the
  only possible source.

  The candidate fix was then applied at the narrowest seam the report
  suggested: encoding the archive without the two fields, leaving the
  in-memory job untouched so the completion push update keeps its output.

  ```go
  job.Lock()
  stdo, stde := job.StdOutC, job.StdErrC
  job.StdOutC, job.StdErrC = nil, nil
  err := enc.Encode(job)
  job.StdOutC, job.StdErrC = stdo, stde
  job.Unlock()
  ```

  This command went from `--- PASS (0.52s), 36 total assertions` to a failure:

  ```bash
  go test -tags netgo --count 1 ./jobqueue -run TestStatusDetailsLiveCompatibility
  ```

  ```text
    * jobqueue/serverWebI_test.go
    Line 2021:
    Expected: "final\n"
    Actual:   ""
    (Should equal)!

  --- FAIL: TestStatusDetailsLiveCompatibility (0.77s)
  ```

  That is a spec'd acceptance test, so it cannot be weakened to let the fix
  through. The patch was reverted.

  ## The other questions the brief asked

  - **`forceStorage`**: its only production setter is `handleRelease`
    (jobqueue/serverCLI.go:1415), which passes `forceStorage: true` (line 1445)
    unconditionally for every release and bury. It is not a user-facing "keep
    my successful output" switch, because no flag, config key or API reaches
    it. It exists so a job buried with an exit code of 0 (buried for a
    non-exit reason, or before it ran at all) still gets its std into the std
    buckets, which is what `jobCouldHaveStd`'s `State == JobStateBuried` arm
    then serves. Nothing to preserve, because nothing was changed.
  - **The buried and failed path**: untouched. It stores through
    `updateJobAfterExit` -> `updateStd` into `bucketStdO`/`bucketStdE` and is
    read back by `jobPopulateStdEnv`. The probe above holds it: the failing
    job's output is still there over both surfaces.
  - **Old databases**: not applicable. The encoded shape of a complete record
    is unchanged, so `jobqueue/testdata/dbcompat/db.golden` and
    `jobqueue/reliable2_dbcompat_test.go` are unaffected. (That fixture would
    not have pinned this either way: it archives `true 1` and `true 2`, which
    emit nothing.)

  ## Is the cost worth naming anyway?

  Yes, and it is bounded, which the report's per-job figure does not make
  obvious. `stdSaverBytes = 4096` (jobqueue/client.go:160) makes the
  `prefixSuffixSaver` keep only each stream's first 4096 and last 4096 bytes,
  plus a short "... omitting N bytes ..." line, so a stream is at most about
  8.2KB raw however much the job printed. `compressStd` then zlib-compresses
  it at `BestCompression`. Incompressible binary output gains a few bytes of
  zlib framing, so the ceiling is about 8KiB compressed per stream and about
  16KiB per job when both streams carry incompressible binary. Base64 of
  random bytes, which the report printed, does compress: one capped stream of
  it goes from ~8.2KB to ~6.3KB. The ~10KB per job the report measured is
  therefore below that ceiling, not at it. A job that prints nothing costs
  nothing, and ordinary log output compresses much further.

  Should that price still be judged too high for a manager with the
  db-performance history of PR #555, the decision to make is a feature one:
  stop showing a completed job's output in the web UI, or put it somewhere
  cheaper than the record that every rep-group history scan decodes. It
  belongs in a spec that supersedes `.docs/issue-98` D1 test 2, not in a
  bugfix.

  ## What did change

  Comments only, no behaviour, because the misreading was invited by the code:

  - `jobqueue/server.go`, `completeJobsByKeys`: the trailing
    `// complete jobs don't have any std` was false, and it sits on the one
    line that looks like corroboration. It now says that the `getStd` passed
    to `jobPopulateStdEnv` is false because the record already carries the
    std and `archiveJobTx` deleted the buckets a fetch would read, and that
    this by-key path serves lookups by key, REST's among them, while the web
    UI's rep-group details view reads the record via `getDBJobsByRepGroup`.
  - `jobqueue/serverCLI.go`, `markJobComplete`: the
    `job.StdOutC = endState.Stdout` pair now carries the why: it deliberately
    survives into the archive record, which surfaces read it, which spec test
    pins it, and that two separate gates make it look like dead weight. One is
    the CLI's own `Exitcode != 0` check in `cmd/status.go`. The other is
    `jobCouldHaveStd`, which only limits std-bucket reads to failed or buried
    jobs.

  No new test. Nothing supported changed, and the behaviour at issue is already
  pinned by `TestStatusDetailsLiveCompatibility`, which this investigation
  confirmed is load-bearing by breaking it on purpose and watching it fail.
