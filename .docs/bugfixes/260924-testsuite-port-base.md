# Bugfixes 2026-09-24

fix-testsuite-nested-port-base

- [x] `make test` fails `TestSuiteTempReaping` (internal/testsuite) with
  `could not find a free port range for WR_TEST_PORT_BASE=<n>`, because the
  test's in-process `RunPlan` validates the outer suite's port base, and the
  outer suite's own lanes hold ports in exactly that block.
    - `internal/testsuite/runner.go`, `setRunPortBase`: when
      `WR_TEST_PORT_BASE` is unset it picks a free base and exports it with
      `os.Setenv`, so every lane inherits it. A nested `RunPlan` in a lane's
      test binary sees the variable, takes `useConfiguredRunPortBase`, and
      `runPortBaseAvailable` probes `base+1..3`, which the outer lanes hold. An
      operator who sets `WR_TEST_PORT_BASE` for a whole `make test` hits the
      same failure the same way.
    - RED COMMAND, run against develop 41a04a26, holding `base+1..3` (the
      check never probes `base` itself):

      ```
      python3 -c "
      import socket,time
      ss=[]
      for p in (19778,19779,19780):
          s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('',p)); s.listen(1); ss.append(s)
      time.sleep(150)" &
      sleep 2
      WR_TEST_PORT_BASE=19777 CGO_ENABLED=0 go test -tags netgo -count=1 -run '^TestSuiteTempReaping$' ./internal/testsuite/
      ```

      Actual:

      ```
      Line 94:
      Expected: nil
      Actual:   'could not find a free port range for WR_TEST_PORT_BASE=19777'
      --- FAIL: TestSuiteTempReaping (0.03s)
      FAIL	github.com/VertebrateResequencing/wr/internal/testsuite	0.030s
      ```

      The same command without `WR_TEST_PORT_BASE`, with the same ports held,
      passes.
    - In-process callers of `RunPlan`, and so of `setRunPortBase`: only two,
      both in `internal/testsuite/runner_test.go`, `TestSuiteTempReaping` and
      `TestSuiteLeavesForeignJobDirsAlone`. With the red command's held ports,
      `TestSuiteLeavesForeignJobDirsAlone` fails the same way. No other test
      calls `Run`, `setRunPortBase` or `useConfiguredRunPortBase`; the only
      production caller is `Run`, from `cmd/wr-testsuite`, which is always the
      outermost run.
    - Fix: both callers now go through one test helper, `runNestedPlan`, which
      runs `t.Setenv(envTestPortBase, "")` before calling `RunPlan`, so the
      nested run searches for a base of its own. This isolates the test from
      the outer run's environment rather than teaching `RunPlan` to tell an
      operator's base apart from a suite-exported one: a nested run only
      happens in these tests, and a marker variable would add a second
      production protocol just to serve them. Lanes still inherit the outer
      base through `os.Environ()`, and an operator's explicit
      `WR_TEST_PORT_BASE` is still validated and honoured for the outer run;
      `runner.go` is unchanged.
    - Regression test: `TestNestedRunFindsItsOwnPortBase` holds `base+1..3` of
      a free base, sets `WR_TEST_PORT_BASE` to it as an outer run would, and
      asserts the in-process `RunPlan` returns nil. Without the `t.Setenv` in
      `runNestedPlan` it fails with `could not find a free port range for
      WR_TEST_PORT_BASE=27517`.
    - The red command now passes, as do `TestSuiteLeavesForeignJobDirsAlone`
      and the whole package under the same held ports and explicit base.
    - Gates, `OS_*` unset, `master` at b2f0ff9: `make lint` gives `0 issues.`;
      `make test` with no `WR_TEST_PORT_BASE` gives `668 passed · 20 skipped ·
      29 packages`, PASSED; `WR_TEST_PORT_BASE=11000 make test` gives the same,
      PASSED; `CGO_ENABLED=1 make race` gives `668 passed · 19 skipped · 29
      packages`, PASSED.

- [x] PR #605 review (Copilot, comment 4091965277): `occupiedPortBase`'s
  cleanup discarded the error from `listener.Close()`, so a failed close went
  unreported. Lint misses it because the `std-error-handling` preset exempts
  `Close`. The cleanup now reports it with `t.Errorf`, not `So`, since the
  Convey context has ended by then. This is a test-only robustness change with
  no change to supported behaviour, so it needs no new test; the package tests
  and `make lint` pass.
