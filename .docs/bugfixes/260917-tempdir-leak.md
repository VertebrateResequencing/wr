# Bugfixes 2026-09-17: the test suite's temp-dir leak

- [x] A successful test run still leaks fixed-prefix temp dirs into the shared
      `/tmp`.

  Item 13 of `.docs/bugfixes/260827-2.md`, recorded on 2026-08-29, marked
  **MOVED OFF THIS BRANCH** and never fixed. Still live on `origin/develop`
  (`41a04a26`) today, leaking the same 9 directories the original measurement
  recorded.

  These names are random-suffixed, so this is the tidiness family, not the
  destructive fixed-shared-path class of item 14 - with one exception noted
  below, which *is* a fixed shared path and is fixed here because it sits in
  the same test as the rest.

  - Red command. It gives the run under test a `TMPDIR` of its own and counts
    what is left in it, rather than globbing the shared `/tmp`: this box is
    shared, sibling clones run their own suites while the measurement is
    taken, and their temp dirs come and go under the same prefixes. Counting
    inside an isolated dir makes the number the run's own.

    ```bash
    measure() {
      d=$(mktemp -d)
      TMPDIR=$d CGO_ENABLED=0 go test -tags netgo -count=1 "$1" >/dev/null 2>&1
      echo "$1 rc=$? left $(ls -A "$d" | wc -l): $(ls -A "$d" | tr '\n' ' ')"
      rm -rf "$d"
    }
    measure ./internal/
    measure ./jobqueue/scheduler/
    ```

    Red, with the source fixes stashed (`rc=1` because the regression tests
    below are present and failing, which is the point):

    ```
    ./internal/ rc=1 left 10: tempHome1017758824/ temp_home1022028777/
      tempHome160390471/ tempHome1936395833/ tempHome2763466141/
      temp_home3641181128/ tempHome543467353/ temp_pwd2177501655/
      temp_wd2379168385/ testNoWrite
    ./jobqueue/scheduler/ rc=1 left 1: wr_schedulers_local_test_slee[_output_dir_2764218520/
    ```

    Green, after the fix:

    ```
    ./internal/ rc=0 left 0:
    ./jobqueue/scheduler/ rc=0 left 0:
    ```

    The nine directories match item 13's 2026-08-29 count exactly. The first
    measurement of this work was taken the way item 13 records it, by globbing
    `/tmp` before and after (189 -> 198, leaking 9, `rc=0`), which agrees; the
    isolated form above replaced it because the shared-`/tmp` count can be
    moved by another clone mid-measurement.

    One, not the 15 the call-site count suggests, for the scheduler: 14 of the
    15 `os.MkdirTemp("", ...)` calls in `scheduler_test.go` already clean up
    inside their Convey block, by `defer os.RemoveAll` or by the order
    recorder's `Close()`. The `wr_schedulers_local_test_*_dir_*` dirs that
    accumulated on this host are from runs that never reached those defers, not
    from passing ones. The single site with no cleanup at all is
    `testLocalFewerCPUs`.

  - Which sites got `t.TempDir()`:

    - `internal/config_test.go`, all nine `os.MkdirTemp("", ...)` calls. Five
      of them are the one in `getTempHome`, which gained a `t *testing.T`
      parameter; all four of its callers are inside `TestConfig`, as are the
      other eight sites, so `t` was in scope everywhere. Four of the nine
      (`temp_home`, `wr_conf_test` x3) already had a `defer os.RemoveAll` and
      did not leak, and were converted anyway: they are the same hand-rolled
      cleanup in the same test, and leaving half the file on `os.MkdirTemp`
      leaves the next edit a template to copy.
    - `internal/config_test.go`'s `/tmp/testNoWrite`, which is a **fixed**
      shared path opened `O_CREATE` at mode 0444, not a random-suffixed dir.
      It now uses `ft.FilePathInTempDir(t, ...)`, the existing helper the same
      Convey block already uses three lines above for `testWrite`.
    - `jobqueue/scheduler/scheduler_test.go`'s `testLocalFewerCPUs`, the one
      scheduler site with no cleanup at all. `t` is a parameter there, and the
      test already waits for `s.Busy(ctx)` to clear before it reads the dir, so
      nothing is still writing when cleanup runs.

  - Which sites needed something else, or nothing:

    - `jobqueue/jobqueue_test.go`'s `wr_signal_marker` got
      `newTestTempDir("signalmarker")` and kept its explicit
      `defer os.RemoveAll`. `t.TempDir()` would be wrong here: removing that
      dir is what ends the test's two blocking jobs, so removal cannot wait for
      the end of the test. PR #555's pid-tagged helper is what adds the part
      that was missing - a name the next run's reaper recognises if this run is
      killed before the defer.
    - `newStartOrderRecorder` and `testLocalBinPacking` in
      `scheduler_test.go` (9 dirs) were left alone. Neither has a `*testing.T`,
      and both clean up deterministically inside the Convey block (`Close()`,
      `defer os.RemoveAll`), which is why they contributed 0 to the measured
      leak. Threading a `t` through both to reach `t.TempDir()` would also move
      removal to the end of `TestLocal`, where that cleanup *fails the test* if
      a still-running scheduled job holds a file open - a new flake in the
      suite's most load-sensitive package, bought with no leak fixed.
    - The `os.MkdirTemp("./", ...)` sites in `scheduler_test.go` and
      `scheduler_lsf_test.go` write into the package directory rather than
      `/tmp`, so they are not this item.

  - Regression tests. Two, one per package, each a behavioural claim at the
    boundary the bug is visible at: run the package's own test binary again as a
    child with `TMPDIR` pointing at a directory of its own, then assert that
    directory is empty once the child has exited. What the child creates there
    is exactly what a real run adds to `/tmp`, so the assertion is the bug's own
    measurement rather than a proxy for it. Both follow the subprocess pattern
    `jobqueue/testtempdir_test.go` already uses.

    - `internal/testtempdir_test.go` runs the whole binary (3.5s), with an env
      var telling the child to skip this one test so it does not recurse. Red
      at `41a04a26`, naming every leaked entry:

      ```
      Expected [d tempHome2208102709/ d tempHome320510728/ d tempHome3995020231/
      d tempHome620108094/ d tempHome720560325/ d temp_home15703914/
      d temp_home461526919/ d temp_pwd1014172408/ d temp_wd163340835/
      - testNoWrite] to be empty (but it wasn't)!
      --- FAIL: TestTestBinaryTempDirs (2.47s)
      ```

    - `jobqueue/scheduler/testtempdir_test.go` runs the child with
      `-test.run '^(TestLocal|TestStartOrderRecorder)$'` (4.4s of the package's
      30s), which is every `os.MkdirTemp("", "wr_schedulers_local_test_*")`
      site including the fixed one. The rest of the package needs OpenStack
      credentials or an LSF cluster, so running it in the child would prove
      nothing and would double this package's exposure to its own timing
      flakes. Red at `41a04a26`:

      ```
      Expected [d wr_schedulers_local_test_slee[_output_dir_800563739/]
      to be empty (but it wasn't)!
      --- FAIL: TestTestBinaryTempDirs (4.54s)
      ```

    No third copy of the pattern was extracted into a shared helper: it is ~12
    lines, the two call sites differ in what they run and how they avoid
    recursion, and `jobqueue`'s existing version asserts something else again
    (that a dir a child *reported* creating is gone). The repo's established
    shape here is a package-local `testtempdir_test.go`, and that is what these
    are.

    Both children are given the environment the test binary was **started**
    with, snapshotted into a package-level `pristineEnv` at initialisation,
    not `os.Environ()`. Found the hard way: the first version handed
    `os.Environ()` over and failed the `make test` gate, because
    `TestConfig`'s last leaf calls `Config.ToEnv()`
    (`internal/config.go:220`), which reflectively sets one `WR_*` variable per
    config field and undoes none of them. By the time the parent reaches
    `TestTestBinaryTempDirs` the process carries a `WR_MANAGERPORT` and
    `WR_MANAGERWEB`, and the child's own `TestConfig` then reads those in
    preference to its config files and fails at `config_test.go:561`, `:778`
    and `:838`. The claim is about a real `go test` run of the package, and a
    real run starts from the environment the binary was given, so the snapshot
    is the correct input rather than a workaround. Mutation-verified: swapping
    `pristineEnv` back for `os.Environ()` fails the test, and restoring it
    passes.

    That process-wide `ToEnv()` pollution is a pre-existing test defect in its
    own right - any test added after `TestConfig` in this package inherits a
    `WR_*` environment it did not ask for - and is recorded here rather than
    fixed, because undoing it means snapshotting and restoring ~30 variables
    around a test this item does not otherwise touch.

  - `internal/config_test.go` still proves what it proved. Every converted site
    feeds either `HOME` or a working directory into config resolution, and
    `t.TempDir()` returns a path under `os.TempDir()` just as
    `os.MkdirTemp("", ...)` did, so `ShouldStartWith, os.TempDir()` at the
    relative-to-absolute case still holds. The `wr_conf_test` site that
    `os.Chdir`es into its dir keeps its deferred chdir back, which runs before
    the framework's cleanup, so nothing removes the process's cwd from under it.

  - **The dirs already in `/tmp` on this host need a manual sweep.** 259 of
    them by the end of this work, against ~150 at the start: the early
    shared-`/tmp` measurements had to run the leaking code, and each run added
    9 more. The isolated-`TMPDIR` form above stopped that, and is the reason it
    is the recorded red command. They are not this change's to delete - the box
    is shared, and a blanket `rm` over a `/tmp` glob is the exact hazard class
    item 14 records. The only thing this work removed was one
    `/tmp/testNoWrite`, to confirm the fixed code stops recreating it; `rm -f`
    succeeding on a sticky `/tmp` proved it was ours.

  - Gates, with `OS_*` unset and the golangci-lint cache cleaned first. The two
    suite gates were given an explicit `WR_TEST_PORT_BASE`, because the suite
    picks a port base at startup and binds later, so concurrent runs in sibling
    clones on this shared box can choose overlapping blocks; a first `make test`
    attempt without one failed `TestSuiteTempReaping` in `internal/testsuite`
    (a package this change does not touch) with `could not find a free port
    range for WR_TEST_PORT_BASE=14279`, which `.docs/bugfixes/260916-1.md`
    already records as contention rather than a defect:
    - `make lint`: `0 issues.`, gated against `master` =
      `b2f0ff973de7a2f76c7e845c60bb9bce07fae28c` (= `origin/master`), checked
      with `git rev-parse master` before the run. A stale or missing ref makes
      this gate lie in both directions, so the SHA is recorded.
    - `WR_TEST_PORT_BASE=22011 make test`: `669 passed - 20 skipped -
      29 packages - 6m41s`, rc=0, green first time with the port base.
    - `WR_TEST_PORT_BASE=22511 CGO_ENABLED=1 make race`: `669 passed -
      19 skipped - 29 packages - 13m59s`, rc=0, on the second attempt. The
      first failed `TestJobqueueExecutionAndDependencyScenarios` at
      `jobqueue_test.go:4513` ("Jobs that fork and change processgroup can
      still be fully killed"), `FailReason` `""` where
      `killed by user request` was expected. That test kills a real forking
      subprocess from a goroutine after a fixed `<-time.After(1 * time.Second)`
      and asserts the kill landed, so it races host load - under `-race` on a
      box with a sibling clone's suite running, the kill can arrive after the
      job has already gone. Nothing this change touches is in its path: the
      only edit to `jobqueue_test.go` here is `TestJobqueueSignal`'s marker
      dir. A fixed-sleep kill race is worth its own item, in the family of
      `.docs/bugfixes/260916-1.md`'s port-contention entry.
