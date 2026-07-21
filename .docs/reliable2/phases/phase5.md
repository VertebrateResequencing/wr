# Phase 5: KEEP regression sweep and scale/throughput validation (sections H1, I)

Ref: [spec.md](../spec.md) sections H1, I

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

This is the final gate. Item 5.1 adds the focused H1 KEEP checks and runs the
full KEEP anchor suite green under `-race`; Item 5.2 is a documented farm/
saturation VALIDATION step, not a GoConvey unit test. Depends on Phases 1-4 (the
full revert must be in place). Do NOT weaken any existing KEEP test.

## Items

### Item 5.1: H1 - Subscriptions, live data, resync, actions, add --sync

spec.md section: H1

The existing anchors MUST stay green and must not be deleted or weakened:
`jobqueue/subscription_test.go` (`#503` per-job subscriptions),
`jobqueue/live_jtouch_test.go` (`#530`/`#534` live RAM/CPU/STDOUT incl.
ssh-to-host), the `JobUpdateResync` reconnect/resync tests,
`jobqueue/suspend_resume_test.go` + `wr status --suspended`,
`jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`
(rerun/modify/suspend/resume), and the `wr add --sync` client test.

Add the focused H1 acceptance tests (map to acceptance #4). Covers all 4 H1
acceptance tests: (1) a subscriber progressing new->running->complete still
receives per-job `JobUpdate`s AND a live RAM/CPU/STDOUT snapshot from a touch
(`emitLiveTouchSnapshot`), unchanged by the removal; (2) a subscriber that
reconnects mid-run receives a `JobUpdateResync` and catches up; (3) a completed
job Reruns (web/REST), and an incomplete job's modify and suspend/resume still
work with `wr status --suspended` listing suspended jobs; (4) `wr add --sync` for
a completing command returns on completion via the subscription (non-polling),
unchanged. Run the full KEEP anchor suite green under `-race`.

- [x] implemented
- [x] reviewed

### Item 5.2: I - Scale / throughput validation (#7) - VALIDATION, not a unit test

spec.md section: I

This item is a documented validation gate, NOT a GoConvey unit test (the
in-process oracle A1 is necessary but not sufficient; see
`.docs/reliable2/testing.md`). Before shipping, validate at the churn-triggering
scale and record the result in `.docs/reliable2/` before merge:

- Steady-state throughput >= current (metric M7).
- A scale run at ~6-7k concurrent runners (the `portal_builder` workload on the
  farm per `testing.md`'s harness, or an in-process saturation harness that
  lowers the reader threshold) shows: successful commands recorded `complete`
  (M1 ~100%, M2 = 0 `jarchive: bad job` for exit-0 jobs), no `deleted` broadcast
  for succeeded jobs (M4), bounded heavy `wr status` latency (M5), and clean
  completion + responsive status matching v0.36.5.

For this item, "implemented" means the validation run was performed and its
result recorded in `.docs/reliable2/`; "reviewed" means the recorded metrics were
checked against the thresholds above. This is a gate, not code.

The in-process saturation harness (`jobqueue/reliable2_scale_test.go`,
`//go:build reliability`) was built and run at up to 3000 concurrent in-process
runners; result recorded in `.docs/reliable2/scale-validation.md` (PASS: M1=1.0,
M2=0, M4=0, bounded/responsive M5, M7 reported; genuine Lost-in-Run-while-alive
churn exercised and every successful archive accepted). A full ~6-7k-runner farm
`portal_builder` run remains a recommended pre-merge confirmation (needs the farm
environment; not performed autonomously).

- [x] implemented
- [x] reviewed

## Merge gate

The change may merge only when, in addition to the recorded Item 5.2 scale
result:

- All section H1 KEEP anchors above are green, plus
  `TestLostDetectionSilentRunner` (section B) and the new Phase 1-4 acceptance
  suites, under `-race`.
- `make test`, `make race`, `make lint` all clean.
