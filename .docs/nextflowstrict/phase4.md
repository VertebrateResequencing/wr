# Phase 4: Expanded operator parsing

Ref: [spec.md](spec.md) sections E1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: E1 - Accept high-priority operators without error

spec.md section: E1

Add `branch`, `combine`, `concat`, `set`, `view`, `ifEmpty`,
`splitCsv`, `splitFasta`, `splitFastq`, `transpose`, `unique`,
`distinct`, `toList`, `toSortedList`, `flatten`, `count`,
`reduce`, `multiMap`, `tap`, `dump`, and `collectFile` to
`supportedChannelOperators` in `nextflowdsl/parse.go`. Update
`parseChannelOperatorArgs` for operators taking channel args
(`combine`, `concat`, `tap`). New operators parse but produce a
warning if they affect job cardinality in ways the translator
cannot handle; `view` and `dump` are no-ops. Covering all 18
acceptance tests from E1.

- [ ] implemented
- [ ] reviewed
