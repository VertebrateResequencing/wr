# Phase 4: Documentation and release note

Ref: [spec.md](spec.md) sections D1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: D1 - Document the behavior change

spec.md section: D1

Update `cmd/add.go`, `cmd/status.go`, and `CHANGELOG.md` so add help explains
the new `--deps` behavior and unchanged `--cmd_deps` behavior, status help
documents `--missing_deps`, and the newest changelog section records the
changed dep-group semantics. Run focused GoConvey tests, then package-wide
`go test` where practical. Cover all 3 acceptance tests from D1.

- [ ] implemented
- [ ] reviewed
