- [x] CI flake. TestAddPrintsDuplicateBreakdown (cmd/add_test.go:1342, in completeTestJobs) failed with 'jobqueue Connect(): could not reach the server' on a loaded GitHub runner (PR #609 run, head 93479b6b; passed on re-run). completeTestJobs connects with a 2s timeout.
  - Lane: `make test` (CGO_ENABLED=0, netgo), 4-vCPU runner.
  - Cause (diagnosed): the connect-time ping's reply took over 2s because of a scheduling stall on the shared runner. The server's ping path takes no lock an add could hold (serverCLI.go:597, 704, 1922), so the defect is the cmd tests' 2s connect budget. CPU starvation, stress and SIGSTOP loops never reproduced it locally; the longest connect seen was 103ms in 24,000.
  - Red command: `cd cmd && CGO_ENABLED=0 go test -tags netgo -count=1 -run 'TestZZRedCompleteTestJobsSlowPing$' .` The throwaway harness is cmd/zz_red_connect_stall_test.go and is not committed. A TLS proxy holds back the ping record by PING_DELAY (default 2.5s), and the real completeTestJobs goes through it. It failed 4 of 4:
    ```
      Line 1342:
      Expected: nil
      Actual:   'jobqueue Connect(): could not reach the server'
    --- FAIL: TestZZRedCompleteTestJobsSlowPing (3.23s)
    ```
    Control: PING_DELAY=1.5s passed 3 of 3.
  - Fix: cmd/status_test.go adds `testConnectTimeoutSeconds` (30) and `testConnectTimeout`, next to `testServerPublishTimeout`. Every cmd test `jobqueue.Connect` that passed 2s now passes `testConnectTimeout` (cmd/add_test.go, cmd/status_test.go, cmd/suspend_test.go). Every `timeoutint = 2` in cmd/add_test.go, which the CLI connect() in `wr add` uses, is now `testConnectTimeoutSeconds`. `rtimeoutint = 1` is a job's reserve timeout, not a connect budget, so it is unchanged, as are the existing `timeoutint = 120` resets and the 2s `Reserve` waits. Server code is unchanged.
  - After the fix, the red command passes with the default 2.5s PING_DELAY and with PING_DELAY=5s. No regression test is committed: the change widens a test budget and alters no supported behaviour, and the harness needs a TLS-record proxy that only simulates a runner stall.
