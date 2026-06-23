# Phase 3: CLI Commands

Ref: [spec.md](spec.md) sections C1, C2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: C1 - Suspend Selected Jobs

spec.md section: C1

Add `cmd/suspend.go`, selector validation matching retry/remove semantics,
matching-count output, live-job filtering for `-a`, and `cmd/suspend_test.go`,
covering all 11 acceptance tests from C1. Depends on phase 2.

- [ ] implemented
- [ ] reviewed

### Item 3.2: C2 - Resume Selected Jobs

spec.md section: C2

Add `cmd/resume.go`, validation matching `wr suspend`, `-a` support for all
suspended jobs, dependent and delayed resume behaviour, and
`cmd/resume_test.go`, covering all 14 acceptance tests from C2. Depends on
item 3.1 for shared selector and validation patterns.

- [ ] implemented
- [ ] reviewed
