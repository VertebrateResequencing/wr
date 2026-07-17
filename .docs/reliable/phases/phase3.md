# Phase 3: Fix 1c - Local-scheduler recover-once

Ref: [spec.md](../spec.md) section C1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout. This phase is scoped to the `jobqueue/scheduler` package and is
independent of Phases 1, 2 and 4; it may run in parallel with or after them. It
is independently mergeable and must keep the Section E regression guards green
(re-run them after the phase).

## Items

### Item 3.1: C1 - Enumerate processes once per recovery

spec.md section: C1

In `jobqueue/scheduler/local.go`, introduce a process-lister seam on the `local`
struct (scheduler/local.go:152): `processLister func() ([]*process.Process,
error)` defaulting to `process.Processes`. `local.recover`
(scheduler/local.go:581) currently calls `process.Processes()`
(scheduler/local.go:582) on every call, and is called once per running job
(`recoverRunningJob` server.go:2810 -> `Scheduler.Recover` scheduler.go:428 ->
`local.recover`). Cache a single enumeration for the duration of one recovery
pass (reuse across `recover` calls; invalidate when the pass ends / after a
short freshness window), so N running jobs cause 1 enumeration. Keep tracking
each still-alive matching pid via `recoverPid` (scheduler/local.go:617) /
`recoveredPids` (scheduler/local.go:173). LSF is unaffected: `lsf.recover`
(lsf.go:1026) is a no-op and does no enumeration.

Tests in `jobqueue/scheduler/recover_test.go`. Covers all 3 acceptance tests
from C1 (a counting `processLister` double is invoked exactly once across
`Recover` for 50 distinct running cmds in one pass, and every matching alive pid
is tracked; the LSF scheduler does no enumeration over 50 `Recover` calls and
returns nil; two matching processes for one cmd track only one pid, preserving
the existing `recoverPid` de-dup).

- [x] implemented
- [x] reviewed

## Regression guards (Section E)

Re-run after this phase; all must stay green (spec.md Section E):

- `jobqueue/lost_detection_test.go`: `TestLostDetectionSilentRunner`,
  `TestLostDetectionRecentContactNotLost`.
- `TestReliableFalseLostRerun`, `TestReliableCompletedRepGroupRemovedOnRefresh`
  (reliable harness dropped into `jobqueue/`;
  `go test -run TestReliable ./jobqueue`).
- `TestReliableFalseLostUnderSaturation` (`everLost == 0`; run from
  `.docs/reliable/harness/`, not committed).
- `make test`, `make race`, `make lint` all clean.
