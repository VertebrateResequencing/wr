# Phase 5: Benchmark sign-off (benchmarks)

Ref: [spec.md](spec.md) sections D1

## Dependencies

Final gate. Depends on Phases 1-4 being implemented and reviewed (the
end-time index, written only in `archiveJobTx`, must be in place to
measure). No new logic; uses the existing benchmarks.

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

This phase records and verifies a performance bar, not new code. If any
benchmark regresses against baseline, it must be resolved before
completion (e.g. shrink the index value, or reconsider the key/value
layout) - not waved through.

## Items

### Item 5.1: D1 - No archive/add/modify regression

spec.md section: D1

Capture baseline numbers at the branch point and again after
implementation, then compare. Run the full suite with
`timeout 600 make bench` (covers `BenchmarkAddJobs`,
`BenchmarkUpdateJobState`, `BenchmarkArchiveJobs`,
`BenchmarkModifyLiveJobsReverseLookup`, etc.); capture `bolt_writes/job`
and `bolt_pages/job` from `BenchmarkAddJobs` and `BenchmarkArchiveJobs`,
and `ns/op` per benchmark. Record the comparison in the PR /
`.docs/recent`. Verify against the bar (all 4 acceptance criteria from
D1):
- `BenchmarkArchiveJobs` `bolt_writes/job` does not increase vs baseline.
- `BenchmarkArchiveJobs` `bolt_pages/job` within measurement noise;
  `ns/op` within ~5-10%.
- `BenchmarkAddJobs` `bolt_writes/job` and `bolt_pages/job` unchanged;
  `BenchmarkModifyLiveJobsReverseLookup` `ns/op` within ~5-10%.
- Any regression is resolved before completion, not waved through.

Test file: `jobqueue/db_bench_test.go` (existing benchmarks; no new
logic).

- [x] implemented
- [x] reviewed
