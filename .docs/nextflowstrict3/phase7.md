# Phase 7: Config Extensions

Ref: [spec.md](spec.md) sections M1, M2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 7.1: M1 - Parse `executor {}` scope with key extraction [parallel with 7.2]

spec.md section: M1

Add `Executor map[string]any` field to `Config` in config.go.
Remove `executor` from `skippedTopLevelConfigScopes` and
instead parse its key-value content into `Config.Executor`.
Extract `name`, `queueSize`, `queue`, and `clusterOptions`.
Handle profile-scoped executor blocks. Covering all 4
acceptance tests from M1.

- [x] implemented
- [x] reviewed

#### Item 7.2: M2 - Parse remaining config scopes [parallel with 7.1]

spec.md section: M2

Add `conda`, `dag`, `manifest`, `notification`, `report`,
`timeline`, `tower`, `trace`, and `weblog` to
`skippedTopLevelConfigScopes` in config.go so that all standard
Nextflow config scopes parse without error. `wave` is already
handled. Covering all 11 acceptance tests from M2.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
