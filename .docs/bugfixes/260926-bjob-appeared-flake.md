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
