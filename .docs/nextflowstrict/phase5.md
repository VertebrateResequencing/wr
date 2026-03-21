# Phase 5: Config improvements

Ref: [spec.md](spec.md) sections D2, D1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 5.1: D2 - Skip unknown config scopes

spec.md section: D2

Update the config parser in `nextflowdsl/config.go` to skip
unknown top-level config scopes (`docker`, `singularity`,
`conda`, `env`, `manifest`, `timeline`, `report`, `trace`,
`dag`, `executor`, `notification`, `weblog`, `tower`) with a
warning instead of returning an error. Use the existing
`skipNamedBlock` helper or equivalent. Covering all 6 acceptance
tests from D2.

- [x] implemented
- [x] reviewed

### Item 5.2: D1 - Parse and follow includeConfig directives

spec.md section: D1

Depends on Item 5.1 (included files may contain unknown scopes).
Add `ParseConfigFromPath` and `ParseConfigFromPathWithParams`
functions to `nextflowdsl/config.go`. Resolve `includeConfig`
paths relative to the including file's directory. Detect circular
includes via a visited-path set. Parse included files and merge
results. Covering all 5 acceptance tests from D1.

- [x] implemented
- [x] reviewed
