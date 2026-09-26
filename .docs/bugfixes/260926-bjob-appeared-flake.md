- [x] CI on PR #618 (head 1ee9c6b, Go 1.27.1) failed `TestReliable4BjobAppearedBound` at jobqueue/scheduler/reliable4_bjob_appeared_test.go:142 with `Expected: true Actual: false`, in "Given an lsf whose `bjobs -w <id>` answers at once": waitForBjob reported the job as not appeared. The same PR passed on Go 1.27.1 at an earlier head, so it is intermittent.
  - Cause: the window test lowers `bjobsAppearTimeout` to 300ms through `setBjobsAppearTimeout`, which restored it with `t.Cleanup`. GoConvey runs each top-level Convey on the same `*testing.T`, so the restore only ran when the whole test ended. The "answers at once" Convey therefore ran with a 300ms window instead of the shipped 10s. `pollForBjob` makes its first poll after `bjobsAppearPollFreq` (100ms), so the fake bash `bjobs` had about 200ms to fork, exec and answer. A loaded CI runner can miss that. A debug `t.Logf` in that Convey printed `bjobsAppearTimeout=300ms`.
  - Not Go 1.27-specific. With 80 busy loops pinned to CPU 0, and the test binary run under `GOMAXPROCS=1 taskset -c 0 ... -test.count=20`, line 142 failed 20 of 20 runs on both Go 1.27.1 and Go 1.26.3. No other line failed.
  - Not a product issue. With the shipped values an appearance check has the full 10s window, and the exec bound is 30s.
  - Red command: `go test -count=5 -run TestReliable4BjobAppeared ./jobqueue/scheduler/`, with the new slow-answer Convey placed after the window test. Exit 1. Before the fix, 5 of 5 runs failed:
    ```
      Line 148:
      Expected: true
      Actual:   false
    --- FAIL: TestReliable4BjobAppearedBound (1.21s)
    ```
  - Fixed: `setBjobsAppearTimeout` now restores the window with a Convey `Reset`, so the lowered window lasts only for the Convey that set it. It no longer takes `t`.
  - Regression guard: a new Convey, "answers slowly, well within the shipped window", uses `bjobsAppearSleepSecs: 1` and asserts that waitForBjob still reports the job as appeared. One second is over three times the lowered window and a tenth of the shipped one, so the guard fails every run if the window leaks again. It also covers real behaviour: a bjobs that takes a second to answer on a busy farm must not turn a successful bsub into a failed schedule.
  - The bounds the test proves are unchanged: the window test still uses 300ms and `bjobsAppearWaitMax`, the exec-bound test still uses `testBjobsExecTimeout`, and "answers at once" still asserts it finishes within `bjobsAppearWaitMax`.
  - Red after: `go test -count=10 -run TestReliable4BjobAppeared ./jobqueue/scheduler/` passed (`ok ... 20.184s`). The same 80-loop single-CPU load run passed 20 of 20 on both Go 1.27.1 and Go 1.26.3.
  - Gates: `make lint` 0 issues; `go test -count=10 -run TestReliable4BjobAppeared` ok; `make test` passed; `CGO_ENABLED=1 make race` passed (711 passed, 19 skipped).
  - Files: jobqueue/scheduler/reliable4_bjob_appeared_test.go.
- [x] Reviewer sweep of b2d12ed found the same leak in cmd/manager_test.go TestWaitForLiveManagerStartup. The first Convey sets managerStartupConnectAttempt=1ms and managerStartupReportInterval=15ms (defaults 500ms and 30s), restored only with t.Cleanup, and the later siblings "daemon exits before ready" and "quick manager startup remains quiet" inherit them. "Remains quiet" expects zero reports and is safe only because its connector answers on the first attempt.
  - Red command: `go test -count=1 -run TestWaitForLiveManagerStartup ./cmd/`, with "remains quiet" given a connector that models a manager that is already up but needs 10ms to answer, so it connects only when given at least that long. Exit 1. Before the fix every attempt got the leaked 1ms budget, the wait ran past its 1s timeout and reported:
    ```
      Line 986:
      Expected: 0
      Actual:   1
    --- FAIL: TestWaitForLiveManagerStartup (1.08s)
    ```
  - Fixed: the three Conveys now restore config and the startup intervals they change with a Convey `Reset` instead of `t.Cleanup`, so each Convey's overrides end with it. The slow-answering connector stays as the regression guard.
  - Red after: `go test -count=3 -run TestWaitForLiveManagerStartup ./cmd/` ok.
  - Files: cmd/manager_test.go.
- [x] Reviewer sweep of b2d12ed found client/client_test.go TestFakeScheduler sets `PretendSubmissions = " "` and never restores it, so every later `New` in the package makes a fake scheduler that records submissions instead of submitting them.
  - Red command: `go test -count=1 -run 'TestFakeScheduler$' ./client/`, with a trailing top-level Convey asserting PretendSubmissions equals its value at the start of the test. Exit 1:
    ```
      Line 2081:
      Expected: ""
      Actual:   " "
    --- FAIL: TestFakeScheduler (0.00s)
    ```
  - Fixed: the Convey sets it through the existing `setPretendSubmissionsForTest` helper and `defer`s the restore, as TestSchedulerPretendNewMethods does. The defer runs at the end of each pass through the Convey, before any later Convey. A `t.Cleanup` would not: it runs only when the test ends, so the trailing check would still see `" "`. The trailing Convey stays as the regression guard.
  - Red after: `go test -count=3 -run 'TestFakeScheduler$' ./client/` ok.
  - Files: client/client_test.go.
- Gates for items 2 and 3: `make lint` 0 issues; `CGO_ENABLED=1 go test -tags netgo -count=3 ./cmd/ ./client/` ok; `make test` passed; `CGO_ENABLED=1 make race` passed (711 passed, 19 skipped).
