# Phase 7: Targeted test run

Ref: [spec.md](spec.md) section Implementation Order

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 7.1: Implementation Order - Run targeted tests

spec.md section: Implementation Order

Run the bounded client test command:

```sh
timeout 10m env CGO_ENABLED=1 go test -tags netgo --count 1 \
  ./client -v -run Test
```

If `jobqueue` changed, also run the bounded jobqueue command:

```sh
timeout 10m env CGO_ENABLED=1 go test -tags netgo --count 1 \
  ./jobqueue -v -run TestClient
```

Resolve failures only within the scope of Phases 1-6. This verifies the full
implemented coverage for all 51 acceptance tests across A1-A2, B1-B3, C1,
D1-D2, E1, and F1. Depends on Phases 1-6.

- [ ] implemented
- [ ] reviewed
