# Phase 3: Duration parsing (cmd/duration.go)

Ref: [spec.md](spec.md) sections C5

## Dependencies

Independent of Phases 1-2; can run in parallel with Phase 2. Phase 4
depends on this phase (and on Phase 2).

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: C5 - d/w duration parsing

spec.md section: C5

Implement `parseRecentDuration(s string) (time.Duration, error)` in a new
file `cmd/duration.go`. It parses like `time.ParseDuration` but also
accepts a single trailing convenience unit `d` (days = 24h) or `w`
(weeks = 7*24h), e.g. "1d", "2w", "36h", "90m", "0.5d". A trailing `d`/`w`
applies only when the prefix parses as a non-negative finite
`strconv.ParseFloat`; otherwise fall back to `time.ParseDuration`. Combined
units like "1d12h" are not supported and error. Empty string returns
`errEmptyRecentDuration`; results `<= 0` are rejected; error messages
mention `--recent` and the accepted units. Test file:
`cmd/duration_test.go`, covering all 9 acceptance tests from C5.

- [x] implemented
- [x] reviewed
